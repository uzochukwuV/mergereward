package api

import (
	"net/http"

	"mergereward-backend/internal/ws"
)

func (h *Handler) handleWS(w http.ResponseWriter, r *http.Request) {
	ws.ServeWS(h.hub, w, r)
}
