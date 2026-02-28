package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"mergereward-backend/internal/payments"
	"mergereward-backend/internal/store"
)

type createBountyRequest struct {
	RepoID      string `json:"repoId"`
	IssueNumber int    `json:"issueNumber"`
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"` // e.g. "usd"
	SuccessURL  string `json:"successUrl"`
	CancelURL   string `json:"cancelUrl"`
}

type createBountyResponse struct {
	BountyID      string `json:"bountyId"`
	CheckoutURL   string `json:"checkoutUrl"`
	CheckoutID    string `json:"checkoutId"`
	StripeMode    string `json:"stripeMode"`
	PaymentStatus string `json:"paymentStatus"`
}

func (h *Handler) handleCreateBounty(w http.ResponseWriter, r *http.Request) {
	var req createBountyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.RepoID == "" || req.IssueNumber <= 0 || req.AmountCents <= 0 || req.Currency == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing required fields"})
		return
	}
	if req.SuccessURL == "" || req.CancelURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "successUrl and cancelUrl are required"})
		return
	}

	b := &store.Bounty{
		RepoID:      req.RepoID,
		IssueNumber: req.IssueNumber,
		AmountCents: req.AmountCents,
		Currency:    req.Currency,
		CreatedAt:   time.Now().UTC(),
		Status:      store.BountyStatusPendingPayment,
	}
	h.store.CreateBounty(b)

	stripeClient, err := h.stripeClient()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	session, err := stripeClient.CreateBountyCheckoutSession(payments.CreateCheckoutParams{
		BountyID:    b.ID,
		RepoID:      b.RepoID,
		IssueNumber: b.IssueNumber,
		AmountCents: b.AmountCents,
		Currency:    b.Currency,
		SuccessURL:  req.SuccessURL,
		CancelURL:   req.CancelURL,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	if session.URL == "" || session.ID == "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "stripe did not return session url"})
		return
	}

	if err := h.store.AttachStripeCheckout(b.ID, session.ID); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bounty not found"})
		return
	}

	writeJSON(w, http.StatusCreated, &createBountyResponse{
		BountyID:      b.ID,
		CheckoutURL:   session.URL,
		CheckoutID:    session.ID,
		StripeMode:    "payment",
		PaymentStatus: string(b.Status),
	})
}
