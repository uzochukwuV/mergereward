package api

import (
	"encoding/json"
	"net/http"
	"time"

	"mergereward-backend/internal/payments"
	"mergereward-backend/internal/store"
	"mergereward-backend/internal/ws"
)

type developerStripeOnboardRequest struct {
	GitHubLogin string `json:"githubLogin"`
	ReturnURL   string `json:"returnUrl"`
	RefreshURL  string `json:"refreshUrl"`
}

type developerStripeOnboardResponse struct {
	StripeAccountID string `json:"stripeAccountId"`
	OnboardingURL   string `json:"onboardingUrl"`
}

func (h *Handler) handleDeveloperStripeOnboard(w http.ResponseWriter, r *http.Request) {
	var req developerStripeOnboardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.GitHubLogin == "" || req.ReturnURL == "" || req.RefreshURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "githubLogin, returnUrl, refreshUrl are required"})
		return
	}

	stripeClient, err := h.stripeClient()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	dev, ok := h.store.GetDeveloper(req.GitHubLogin)
	acctID := ""
	if ok {
		acctID = dev.StripeAccountID
	}
	if acctID == "" {
		acctID, err = stripeClient.CreateExpressAccount()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
	}

	url, err := stripeClient.CreateAccountLink(payments.AccountLinkParams{
		AccountID:  acctID,
		ReturnURL:  req.ReturnURL,
		RefreshURL: req.RefreshURL,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	now := time.Now().UTC()
	h.store.UpsertDeveloper(store.Developer{
		GitHubLogin:     req.GitHubLogin,
		StripeAccountID: acctID,
		CreatedAt:       now,
	})

	h.hub.Broadcast(ws.Event{Type: "developer.stripe.onboard", Data: map[string]any{
		"githubLogin":     req.GitHubLogin,
		"stripeAccountId": acctID,
	}})

	writeJSON(w, http.StatusOK, developerStripeOnboardResponse{
		StripeAccountID: acctID,
		OnboardingURL:   url,
	})
}
