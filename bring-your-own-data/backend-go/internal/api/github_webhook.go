package api

import (
	"encoding/json"
	"io"
	"net/http"

	"mergereward-backend/internal/ai"
	"mergereward-backend/internal/cre"
	"mergereward-backend/internal/github"
	"mergereward-backend/internal/store"
	"mergereward-backend/internal/ws"
)

func (h *Handler) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	verifier := h.githubVerifier()
	body, err := verifier.ReadAndVerify(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	if event != "pull_request" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	var prEvent github.PullRequestEvent
	if err := json.Unmarshal(body, &prEvent); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid github event json"})
		return
	}

	merge, ok := github.ExtractMergedPR(prEvent)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	issueNumber, ok := github.ExtractIssueNumber(merge.Body)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "no linked issue"})
		return
	}

	b, ok := h.store.FindBountyByRepoIssue(merge.RepoFullName, issueNumber)
	if !ok {
		h.hub.Broadcast(ws.Event{Type: "pr.merged.unmatched", Data: map[string]any{
			"repoId":      merge.RepoFullName,
			"issueNumber": issueNumber,
			"prNumber":    merge.Number,
		}})
		writeJSON(w, http.StatusOK, map[string]string{"status": "no matching bounty"})
		return
	}

	_ = h.store.RecordPRMerge(b.ID, store.MergedPR{
		RepoID:   merge.RepoFullName,
		PRNumber: merge.Number,
		SHA:      merge.MergeCommitSHA,
		Author:   merge.AuthorLogin,
		MergedAt: merge.MergedAt,
		Body:     merge.Body,
	})

	h.hub.Broadcast(ws.Event{Type: "pr.merged", Data: map[string]any{
		"bountyId":     b.ID,
		"repoId":       merge.RepoFullName,
		"issueNumber":  issueNumber,
		"prNumber":     merge.Number,
		"author":       merge.AuthorLogin,
		"mergeCommit":  merge.MergeCommitSHA,
		"mergedAt":     merge.MergedAt,
		"bountyStatus": b.Status,
	}})

	// Source of truth is the GitHub OAuth login that claimed the bounty.
	// We only forward to CRE if the PR author matches the claimer.
	if b.ClaimerGitHubLogin == "" {
		h.hub.Broadcast(ws.Event{Type: "pr.merged.unclaimed", Data: map[string]any{
			"bountyId": b.ID,
			"author":  merge.AuthorLogin,
		}})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "bounty not claimed"})
		return
	}
	if b.ClaimerGitHubLogin != merge.AuthorLogin {
		h.hub.Broadcast(ws.Event{Type: "pr.merged.claimer_mismatch", Data: map[string]any{
			"bountyId": b.ID,
			"claimer":  b.ClaimerGitHubLogin,
			"author":   merge.AuthorLogin,
		}})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "claimer mismatch"})
		return
	}

	// Forward to CRE HTTP trigger for confidential verification + payout authorization.
	// Include the developer's registered EVM wallet address so the CRE workflow
	// can pass it to releaseBounty(bountyId, developerWallet) on the contract.
	var developerWallet string
	if dev, ok := h.store.GetDeveloper(b.ClaimerGitHubLogin); ok {
		developerWallet = dev.WalletAddress
	}

	creClient, err := cre.NewFromEnv()
	if err == nil {
		resp, err := creClient.PostMergeEvent(cre.MergeEvent{
			BountyID:        b.ID,
			RepoID:          merge.RepoFullName,
			IssueNumber:     issueNumber,
			PRNumber:        merge.Number,
			DeveloperGitHub: merge.AuthorLogin,
			DeveloperWallet: developerWallet,
			PaymentMode:     paymentMode(),
		})
		if err != nil {
			h.hub.Broadcast(ws.Event{Type: "cre.error", Data: map[string]any{"bountyId": b.ID, "error": err.Error()}})
		} else {
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			h.hub.Broadcast(ws.Event{Type: "cre.forwarded", Data: map[string]any{"bountyId": b.ID, "status": resp.StatusCode}})
		}
	} else {
		h.hub.Broadcast(ws.Event{Type: "cre.config_missing", Data: map[string]any{"bountyId": b.ID, "error": err.Error()}})
	}

	if ai.Enabled() {
		go func() {
			score, summary, err := ai.ScoreMergedPR(ai.ScoreRequest{
				RepoFullName: merge.RepoFullName,
				PRNumber:     merge.Number,
				Title:        merge.Title,
				Body:         merge.Body,
				MergeSHA:     merge.MergeCommitSHA,
			})
			if err != nil {
				h.hub.Broadcast(ws.Event{Type: "ai.error", Data: map[string]any{
					"bountyId": b.ID,
					"error":   err.Error(),
				}})
				return
			}

			_ = h.store.SetAIResult(b.ID, store.AIResult{Score: score, Summary: summary})
			h.hub.Broadcast(ws.Event{Type: "ai.result", Data: map[string]any{
				"bountyId": b.ID,
				"score":   score,
				"summary": summary,
			}})
		}()
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
