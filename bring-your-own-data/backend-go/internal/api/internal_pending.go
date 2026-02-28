package api

import (
	"crypto/subtle"
	"net/http"
	"os"

	"mergereward-backend/internal/store"
)

// pendingPayoutEvent is a single pending merge event returned to the CRE workflow.
type pendingPayoutEvent struct {
	BountyID        string `json:"bountyId"`
	RepoID          string `json:"repoId"`
	IssueNumber     int    `json:"issueNumber"`
	PRNumber        int    `json:"prNumber"`
	DeveloperGitHub string `json:"developerGitHub"`
}

type pendingPayoutsResponse struct {
	Events []pendingPayoutEvent `json:"events"`
}

// handleInternalPendingPayouts is called by the CRE workflow on each cron tick.
// It returns all bounties that:
//   - are funded (Stripe payment confirmed)
//   - have a merged PR recorded
//   - have NOT been paid out yet
//
// The CRE workflow then independently verifies each via the GitHub API
// before calling /internal/payout for each verified event.
//
// Auth: same CRE_BACKEND_TOKEN bearer token as /internal/payout.
func (h *Handler) handleInternalPendingPayouts(w http.ResponseWriter, r *http.Request) {
	tok := os.Getenv("CRE_BACKEND_TOKEN")
	if tok == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "CRE_BACKEND_TOKEN is not set"})
		return
	}
	got := r.Header.Get("Authorization")
	want := "Bearer " + tok
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	events := h.store.PendingPayouts()
	out := make([]pendingPayoutEvent, 0, len(events))
	for _, b := range events {
		if b.MergedPR == nil || b.ClaimerGitHubLogin == "" {
			continue
		}
		out = append(out, pendingPayoutEvent{
			BountyID:        b.ID,
			RepoID:          b.MergedPR.RepoID,
			IssueNumber:     b.IssueNumber,
			PRNumber:        b.MergedPR.PRNumber,
			DeveloperGitHub: b.ClaimerGitHubLogin,
		})
	}

	writeJSON(w, http.StatusOK, pendingPayoutsResponse{Events: out})
}
