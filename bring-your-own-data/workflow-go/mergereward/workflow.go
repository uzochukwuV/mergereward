package main

// MergeReward CRE Workflow
//
// Trigger: CRE HTTP trigger — the MergeReward backend POSTs a merge event
// to the CRE_TRIGGER_URL when a GitHub PR is merged and a bounty matches.
//
// Each DON node independently:
//   1. Calls the GitHub REST API to verify the PR is truly merged
//      (Confidential HTTP — GITHUB_TOKEN secret never leaves the TEE)
//   2. Reaches consensus on the verification result across all nodes
//   3. Calls the backend's /internal/payout endpoint to release the Stripe transfer
//      (Confidential HTTP — BACKEND_TOKEN secret never leaves the TEE)
//
// Because step 3 uses Stripe idempotency keys (keyed on bountyId), multiple
// DON nodes calling /internal/payout is safe — only one transfer goes through.
//
// Production extension: replace step 3 with an EVM write to
// MergeReward.releaseBounty(bountyId, developerAddress) via the
// Chainlink KeystoneForwarder for a fully on-chain payout path.

import (
	"encoding/json"
	"fmt"
	"log/slog"

	crehttp "github.com/smartcontractkit/cre-sdk-go/capabilities/networking/http"
	"github.com/smartcontractkit/cre-sdk-go/capabilities/scheduler/cron"
	"github.com/smartcontractkit/cre-sdk-go/cre"
)

// ─── Config ──────────────────────────────────────────────────────────────────

// Config is loaded from config.json and injected into every workflow execution.
type Config struct {
	// Schedule controls how often the cron-based fallback polls for pending payouts.
	// Use a short interval (e.g. "*/1 * * * *") for demos; in production this
	// workflow is driven by the CRE HTTP trigger instead.
	Schedule string `json:"schedule"`

	// BackendURL is the base URL of the MergeReward backend, e.g.
	// "https://api.mergereward.example.com"
	BackendURL string `json:"backendURL"`

	// GitHubAPIURL is the GitHub REST API base, usually "https://api.github.com"
	GitHubAPIURL string `json:"githubAPIURL"`
}

// ─── Domain types ─────────────────────────────────────────────────────────────

// MergeEvent mirrors the payload sent by the backend when a PR is merged.
type MergeEvent struct {
	BountyID        string `json:"bountyId"`
	RepoID          string `json:"repoId"`       // "owner/repo"
	IssueNumber     int    `json:"issueNumber"`
	PRNumber        int    `json:"prNumber"`
	DeveloperGitHub string `json:"developerGitHub"`
}

// PendingPayoutsResponse is returned by GET /internal/pending-payouts.
type PendingPayoutsResponse struct {
	Events []MergeEvent `json:"events"`
}

// GitHubPR is the minimal subset of a GitHub Pull Request API response we need.
type GitHubPR struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Merged bool   `json:"merged"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
}

// VerifyResult is the consensus-aggregated result of the GitHub API check.
// All DON nodes must independently agree that the PR was merged by the
// expected GitHub user before any payout is triggered.
type VerifyResult struct {
	// Merged is true only when GitHub confirms the PR was closed + merged.
	Merged bool `consensus_aggregation:"mode" json:"merged"`
	// AuthorMatches is true when the PR author equals the bounty claimer.
	AuthorMatches bool `consensus_aggregation:"mode" json:"authorMatches"`
	// BountyID is passed through for use in the payout step.
	BountyID string `consensus_aggregation:"mode" json:"bountyId"`
}

// ─── Workflow registration ────────────────────────────────────────────────────

// InitWorkflow registers the MergeReward workflow handlers.
// The cron trigger here is used as the demo mechanism; in production the
// CRE platform routes the HTTP trigger payload directly to onMergeTrigger.
func InitWorkflow(config *Config, logger *slog.Logger, secretsProvider cre.SecretsProvider) (cre.Workflow[*Config], error) {
	cronCfg := &cron.Config{Schedule: config.Schedule}

	workflow := cre.Workflow[*Config]{
		cre.Handler(
			cron.Trigger(cronCfg),
			onCronTick,
		),
	}
	return workflow, nil
}

// ─── Cron handler (demo / fallback) ──────────────────────────────────────────

// onCronTick fires on the configured schedule, fetches all pending merge events
// from the backend, and processes each one through the verify → payout pipeline.
func onCronTick(config *Config, runtime cre.Runtime, _ *cron.Payload) (string, error) {
	logger := runtime.Logger()
	logger.Info("mergereward: cron tick — polling for pending payouts")

	pending, err := fetchPendingEvents(config, runtime)
	if err != nil {
		return "", fmt.Errorf("failed to fetch pending events: %w", err)
	}

	logger.Info("pending events", "count", len(pending))
	processed := 0
	for _, evt := range pending {
		if err := processEvent(config, runtime, evt); err != nil {
			logger.Error("failed to process event", "bountyId", evt.BountyID, "err", err)
			continue
		}
		processed++
	}

	return fmt.Sprintf("processed %d/%d events", processed, len(pending)), nil
}

// ─── Core pipeline ────────────────────────────────────────────────────────────

// processEvent runs the full verify → payout pipeline for a single merge event.
func processEvent(config *Config, runtime cre.Runtime, evt MergeEvent) error {
	logger := runtime.Logger()
	logger.Info("processing event", "bountyId", evt.BountyID, "repo", evt.RepoID, "pr", evt.PRNumber)

	// Step 1 — Confidential HTTP: each DON node independently verifies the
	// PR via the GitHub REST API.  The GITHUB_TOKEN secret is encrypted per
	// node in the TEE and is never visible on-chain or to other nodes.
	verifyResult, err := crehttp.SendRequest(
		config,
		runtime,
		&crehttp.Client{},
		verifyPRMerge(evt),
		cre.ConsensusAggregationFromTags[*VerifyResult](),
	).Await()
	if err != nil {
		return fmt.Errorf("github verification failed: %w", err)
	}

	if !verifyResult.Merged {
		logger.Warn("consensus: PR not merged — skipping payout", "bountyId", evt.BountyID)
		return nil
	}
	if !verifyResult.AuthorMatches {
		logger.Warn("consensus: PR author != claimer — skipping payout", "bountyId", evt.BountyID)
		return nil
	}

	logger.Info("consensus: PR verified as merged", "bountyId", evt.BountyID)

	// Step 2 — Confidential HTTP: trigger the Stripe transfer via the backend.
	// The BACKEND_TOKEN is stored in the TEE and never exposed to the outside.
	// The backend uses Stripe idempotency keys so concurrent DON node calls
	// are safe — only one transfer will be executed.
	if err := triggerPayout(config, runtime, evt.BountyID); err != nil {
		return fmt.Errorf("payout trigger failed: %w", err)
	}

	logger.Info("payout triggered", "bountyId", evt.BountyID)
	return nil
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

// fetchPendingEvents calls GET /internal/pending-payouts on the backend to
// retrieve merge events that have been recorded but not yet paid out.
func fetchPendingEvents(config *Config, runtime cre.Runtime) ([]MergeEvent, error) {
	result, err := crehttp.SendRequest(
		config,
		runtime,
		&crehttp.Client{},
		fetchPendingFn,
		cre.ConsensusAggregationFromTags[*PendingPayoutsResponse](),
	).Await()
	if err != nil {
		return nil, err
	}
	return result.Events, nil
}

// fetchPendingFn is the per-node function that fetches pending events from the
// backend. The BACKEND_TOKEN is used as a bearer token.
func fetchPendingFn(config *Config, logger *slog.Logger, req *crehttp.SendRequester) (*PendingPayoutsResponse, error) {
	resp, err := req.SendRequest(&crehttp.Request{
		Method: "GET",
		Url:    config.BackendURL + "/internal/pending-payouts",
		// NOTE: Authorization header uses BACKEND_TOKEN injected by the CRE
		// secrets mechanism.  In the actual TEE runtime the secrets provider
		// populates this automatically; the header key is declared here for
		// clarity.
		Headers: map[string]string{
			"Authorization": "Bearer {{secret:BACKEND_TOKEN}}",
		},
	}).Await()
	if err != nil {
		return nil, fmt.Errorf("fetch pending payouts: %w", err)
	}

	var out PendingPayoutsResponse
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("unmarshal pending payouts: %w", err)
	}

	logger.Info("pending payouts response", "count", len(out.Events))
	return &out, nil
}

// verifyPRMerge returns a per-node function that calls the GitHub REST API to
// confirm the PR was merged and that the PR author matches the expected claimer.
//
// This is the critical Confidential HTTP step: the GITHUB_TOKEN is encrypted
// inside the TEE and never leaves it.  Multiple independent nodes each make
// this call, and CRE consensus ensures all agree before proceeding.
func verifyPRMerge(evt MergeEvent) func(*Config, *slog.Logger, *crehttp.SendRequester) (*VerifyResult, error) {
	return func(config *Config, logger *slog.Logger, req *crehttp.SendRequester) (*VerifyResult, error) {
		// GitHub GET /repos/{owner}/{repo}/pulls/{pull_number}
		url := fmt.Sprintf("%s/repos/%s/pulls/%d", config.GitHubAPIURL, evt.RepoID, evt.PRNumber)

		resp, err := req.SendRequest(&crehttp.Request{
			Method: "GET",
			Url:    url,
			Headers: map[string]string{
				// GITHUB_TOKEN is a CRE secret — injected per-node in the TEE.
				"Authorization": "Bearer {{secret:GITHUB_TOKEN}}",
				"Accept":        "application/vnd.github+json",
				"X-GitHub-Api-Version": "2022-11-28",
			},
		}).Await()
		if err != nil {
			return nil, fmt.Errorf("github api request failed: %w", err)
		}

		var pr GitHubPR
		if err := json.Unmarshal(resp.Body, &pr); err != nil {
			return nil, fmt.Errorf("unmarshal github pr: %w", err)
		}

		logger.Info("github pr", "number", pr.Number, "merged", pr.Merged, "author", pr.User.Login)

		return &VerifyResult{
			Merged:        pr.Merged,
			AuthorMatches: pr.User.Login == evt.DeveloperGitHub,
			BountyID:      evt.BountyID,
		}, nil
	}
}

// triggerPayout calls POST /internal/payout on the backend.
// The BACKEND_TOKEN is a CRE secret, keeping the API key confidential.
// The backend uses Stripe idempotency to ensure exactly-once payment
// even if multiple DON nodes invoke this endpoint concurrently.
func triggerPayout(config *Config, runtime cre.Runtime, bountyID string) error {
	body, err := json.Marshal(map[string]string{"bountyId": bountyID})
	if err != nil {
		return err
	}

	_, err = crehttp.SendRequest(
		config,
		runtime,
		&crehttp.Client{},
		func(config *Config, logger *slog.Logger, req *crehttp.SendRequester) (*struct{}, error) {
			resp, err := req.SendRequest(&crehttp.Request{
				Method: "POST",
				Url:    config.BackendURL + "/internal/payout",
				Headers: map[string]string{
					"Authorization": "Bearer {{secret:BACKEND_TOKEN}}",
					"Content-Type":  "application/json",
				},
				Body: body,
			}).Await()
			if err != nil {
				return nil, fmt.Errorf("payout request failed: %w", err)
			}
			logger.Info("payout response", "status", resp.StatusCode, "bountyId", bountyID)
			return &struct{}{}, nil
		},
		cre.ConsensusAggregationFromTags[*struct{}](),
	).Await()

	return err
}
