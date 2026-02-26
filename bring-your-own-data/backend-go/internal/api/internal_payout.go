package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"mergereward-backend/internal/payments"
	"mergereward-backend/internal/store"
	"mergereward-backend/internal/ws"
)

type internalPayoutRequest struct {
	BountyID string `json:"bountyId"`
}

type internalPayoutResponse struct {
	Status           string `json:"status"`
	StripeTransferID string `json:"stripeTransferId,omitempty"`
}

func (h *Handler) handleInternalPayout(w http.ResponseWriter, r *http.Request) {
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

	var req internalPayoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.BountyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bountyId is required"})
		return
	}

	b, ok := h.store.GetBounty(req.BountyID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bounty not found"})
		return
	}
	if b.Status != store.BountyStatusFunded {
		writeJSON(w, http.StatusConflict, internalPayoutResponse{Status: "not_funded"})
		return
	}
	if b.MergedPR == nil {
		writeJSON(w, http.StatusConflict, internalPayoutResponse{Status: "not_merged"})
		return
	}
	if b.Payout != nil {
		writeJSON(w, http.StatusOK, internalPayoutResponse{Status: "already_paid", StripeTransferID: b.Payout.StripeTransferID})
		return
	}

	dev, ok := h.store.GetDeveloper(b.MergedPR.Author)
	if !ok || dev.StripeAccountID == "" || !dev.StripeOnboarded {
		writeJSON(w, http.StatusConflict, internalPayoutResponse{Status: "developer_not_onboarded"})
		return
	}

	stripeClient, err := h.stripeClient()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Platform fee: 5%
	fee := (b.AmountCents * 5) / 100
	payoutAmount := b.AmountCents - fee

	transferID, err := stripeClient.CreateTransfer(payments.TransferParams{
		DestinationAccountID: dev.StripeAccountID,
		AmountCents:          payoutAmount,
		Currency:             b.Currency,
		BountyID:             b.ID,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	_ = h.store.RecordPayout(b.ID, store.Payout{StripeTransferID: transferID, CreatedAt: time.Now().UTC()})

	h.hub.Broadcast(ws.Event{Type: "bounty.paid", Data: map[string]any{
		"bountyId":         b.ID,
		"stripeTransferId": transferID,
		"developer":        b.MergedPR.Author,
		"amountCents":      payoutAmount,
		"currency":         b.Currency,
		"feeCents":         fee,
	}})

	writeJSON(w, http.StatusOK, internalPayoutResponse{Status: "paid", StripeTransferID: transferID})
}
