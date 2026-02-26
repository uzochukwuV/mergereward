package api

import (
	"io"
	"net/http"

	"github.com/stripe/stripe-go/v76/webhook"

	"mergereward-backend/internal/store"
	"mergereward-backend/internal/ws"
)

func (h *Handler) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	stripeClient, err := h.stripeClient()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
		return
	}

	event, err := webhook.ConstructEvent(payload, r.Header.Get("Stripe-Signature"), stripeClient.WebhookSecret)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid signature"})
		return
	}

	bountyID, status, handled := stripeClient.HandleEvent(event)
	if !handled {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	if bountyID != "" {
		_ = h.store.SetBountyStatus(bountyID, store.BountyStatus(status))
		h.hub.Broadcast(ws.Event{Type: "bounty.payment", Data: map[string]any{
			"bountyId": bountyID,
			"status":  status,
		}})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
