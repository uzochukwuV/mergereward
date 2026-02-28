package api

import (
	"encoding/json"
	"net/http"

	"mergereward-backend/internal/auth"
	"mergereward-backend/internal/ws"
)

type claimBountyRequest struct {
	BountyID string `json:"bountyId"`
}

// handleClaimBounty is protected by auth.RequireAuth. The GitHub login is taken
// from the verified JWT session — not from the request body — so a caller
// cannot claim a bounty on behalf of another user.
func (h *Handler) handleClaimBounty(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		// Should not happen when RequireAuth middleware is applied, but be safe.
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}

	var req claimBountyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.BountyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bountyId is required"})
		return
	}

	if err := h.store.SetBountyClaimer(req.BountyID, claims.GitHubLogin); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	h.hub.Broadcast(ws.Event{Type: "bounty.claimed", Data: map[string]any{
		"bountyId":    req.BountyID,
		"githubLogin": claims.GitHubLogin,
	}})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
