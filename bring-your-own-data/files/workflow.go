package main

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/smartcontractkit/cre-sdk-go/capabilities/networking/http"
	"github.com/smartcontractkit/cre-sdk-go/cre"
)

// ─── Config ──────────────────────────────────────────────────────────────────
// Loaded from config.json (non-secret values)

type Config struct {
	// Backend base URL, e.g. https://api.mergereward.xyz
	BackendURL string `json:"backendURL"`
}

// ─── Incoming event (from backend) ───────────────────────────────────────────

type MergedPREvent struct {
	BountyID        string `json:"bountyId"`
	RepoID          string `json:"repoId"`          // "owner/repo"
	IssueNumber     uint64 `json:"issueNumber"`     // GitHub issue number
	PRNumber        uint64 `json:"prNumber"`        // GitHub PR number
	DeveloperGitHub string `json:"developerGitHub"` // GitHub login
}

// Consensus-aggregated: all DON nodes must agree on the same event.

type VerifiedMerge struct {
	BountyID        string `consensus_aggregation:"mode" json:"bountyId"`
	DeveloperGitHub string `consensus_aggregation:"mode" json:"developerGitHub"`
}

func InitWorkflow(config *Config, logger *slog.Logger, secretsProvider cre.SecretsProvider) (cre.Workflow[*Config], error) {
	workflow := cre.Workflow[*Config]{
		cre.Handler(
			http.Trigger(&http.TriggerConfig{}),
			onPRMergedWebhook,
		),
	}
	return workflow, nil
}

func onPRMergedWebhook(config *Config, runtime cre.Runtime, payload *http.TriggerPayload) (string, error) {
	logger := runtime.Logger()

	var event MergedPREvent
	if err := json.Unmarshal(payload.Body, &event); err != nil {
		return "", fmt.Errorf("failed to parse PR merge event: %w", err)
	}
	logger.Info("PR merge event received",
		"bountyId", event.BountyID,
		"repo", event.RepoID,
		"issue", event.IssueNumber,
		"pr", event.PRNumber,
		"developer", event.DeveloperGitHub,
	)

	verified, err := verifyPRMergeWithGitHub(config, runtime, &event)
	if err != nil {
		return "", fmt.Errorf("GitHub verification failed: %w", err)
	}

	logger.Info("GitHub verification passed", "bountyId", verified.BountyID, "developer", verified.DeveloperGitHub)

	// Confidential HTTP call to backend to execute Stripe Connect transfer.
	payoutURL := fmt.Sprintf("%s/internal/payout", config.BackendURL)

	_, err = http.SendRequest(
		config,
		runtime,
		&http.Client{},
		func(cfg *Config, logger *slog.Logger, req *http.SendRequester) (*string, error) {
			b, _ := json.Marshal(map[string]string{"bountyId": verified.BountyID})
			resp, err := req.SendRequest(&http.Request{
				Method: "POST",
				Url:    payoutURL,
				Headers: map[string]string{
					"Content-Type":  "application/json",
					"Authorization": fmt.Sprintf("Bearer %s", mustSecret(runtime, "CRE_BACKEND_TOKEN")),
				},
				Body:          b,
				Timeout:       &durationpb.Duration{Seconds: 15},
				CacheSettings: &http.CacheSettings{},
			}).Await()
			if err != nil {
				return nil, err
			}
			s := string(resp.Body)
			return &s, nil
		},
		cre.ConsensusAggregationFromTags[*string](),
	).Await()
	if err != nil {
		return "", fmt.Errorf("backend payout call failed: %w", err)
	}

	return fmt.Sprintf("payout processed for bounty %s", verified.BountyID), nil
}

// ─── GitHub Verification (Confidential HTTP) ─────────────────────────────────

type GitHubPR struct {
	Merged bool `json:"merged"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
}

func verifyPRMergeWithGitHub(config *Config, runtime cre.Runtime, event *MergedPREvent) (*VerifiedMerge, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", event.RepoID, event.PRNumber)

	result, err := http.SendRequest(
		config,
		runtime,
		&http.Client{},
		func(cfg *Config, logger *slog.Logger, req *http.SendRequester) (*VerifiedMerge, error) {
			resp, err := req.SendRequest(&http.Request{
				Method: "GET",
				Url:    url,
				Headers: map[string]string{
					"Authorization":       fmt.Sprintf("Bearer %s", mustSecret(runtime, "GITHUB_TOKEN")),
					"Accept":              "application/vnd.github+json",
					"X-GitHub-Api-Version": "2022-11-28",
				},
				Timeout:       &durationpb.Duration{Seconds: 10},
				CacheSettings: &http.CacheSettings{},
			}).Await()
			if err != nil {
				return nil, fmt.Errorf("GitHub API request failed: %w", err)
			}

			var pr GitHubPR
			if err := json.Unmarshal(resp.Body, &pr); err != nil {
				return nil, fmt.Errorf("failed to parse GitHub response: %w", err)
			}
			if !pr.Merged {
				return nil, fmt.Errorf("PR #%d is not merged", event.PRNumber)
			}
			if pr.User.Login != event.DeveloperGitHub {
				return nil, fmt.Errorf("PR author mismatch: got %q, expected %q", pr.User.Login, event.DeveloperGitHub)
			}

			return &VerifiedMerge{BountyID: event.BountyID, DeveloperGitHub: event.DeveloperGitHub}, nil
		},
		cre.ConsensusAggregationFromTags[*VerifiedMerge](),
	).Await()

	if err != nil {
		return nil, err
	}
	return result, nil
}

func mustSecret(runtime cre.Runtime, key string) string {
	val, err := runtime.SecretsProvider().Get(key)
	if err != nil {
		panic(fmt.Sprintf("missing required secret: %s", key))
	}
	return val
}
