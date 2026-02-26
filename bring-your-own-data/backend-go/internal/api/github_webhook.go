package api

import (
	"encoding/json"
	"net/http"

	"mergereward-backend/internal/ai"
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
