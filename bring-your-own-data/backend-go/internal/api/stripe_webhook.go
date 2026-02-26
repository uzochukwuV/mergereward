package api

import (
	"io"
	"net/http"
	"time"

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

	res := stripeClient.HandleEvent(event)
	if !res.Handled {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	if res.BountyID != "" {
		_ = h.store.SetBountyStatus(res.BountyID, store.BountyStatus(res.BountyStatus))
		h.hub.Broadcast(ws.Event{Type: "bounty.payment", Data: map[string]any{
			"bountyId": res.BountyID,
			"status":  res.BountyStatus,
		}})
	}

	if res.ConnectedAccountID != "" && res.AccountOnboarded != nil {
		// Find developer by account ID (simple scan in memory store for demo).
		// In prod, you'd have an indexed lookup.
		now := time.Now().UTC()
		login := ""
		for _, dev := range h.store.AllDevelopers() {
			if dev.StripeAccountID == res.ConnectedAccountID {
				login = dev.GitHubLogin
				break
			}
		}
		if login != "" {
			h.store.SetDeveloperOnboarded(login, *res.AccountOnboarded, now)
			h.hub.Broadcast(ws.Event{Type: "developer.stripe.updated", Data: map[string]any{
				"githubLogin":     login,
				"stripeAccountId": res.ConnectedAccountID,
				"onboarded":       *res.AccountOnboarded,
			}})
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
