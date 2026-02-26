package api

import (
	"encoding/json"
	"net/http"

	"mergereward-backend/internal/ws"
)

type claimBountyRequest struct {
	BountyID    string `json:"bountyId"`
	GitHubLogin string `json:"githubLogin"`
}

func (h *Handler) handleClaimBounty(w http.ResponseWriter, r *http.Request) {
	var req claimBountyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.BountyID == "" || req.GitHubLogin == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bountyId and githubLogin are required"})
		return
	}

	if err := h.store.SetBountyClaimer(req.BountyID, req.GitHubLogin); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	h.hub.Broadcast(ws.Event{Type: "bounty.claimed", Data: map[string]any{
		"bountyId":    req.BountyID,
		"githubLogin": req.GitHubLogin,
	}})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
