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
	PaymentMode      string `json:"paymentMode"` // "stripe" | "onchain"
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
		writeJSON(w, http.StatusConflict, internalPayoutResponse{Status: "not_funded", PaymentMode: paymentMode()})
		return
	}
	if b.MergedPR == nil {
		writeJSON(w, http.StatusConflict, internalPayoutResponse{Status: "not_merged", PaymentMode: paymentMode()})
		return
	}
	if b.Payout != nil {
		writeJSON(w, http.StatusOK, internalPayoutResponse{
			Status:           "already_paid",
			StripeTransferID: b.Payout.StripeTransferID,
			PaymentMode:      paymentMode(),
		})
		return
	}

	// ── On-chain path (Stripe not configured) ────────────────────────────────
	// The actual payout is executed by the CRE KeystoneForwarder calling
	// MergeReward.releaseBounty() on the smart contract — NOT by this endpoint.
	// Funds accumulate in developerBalances[developer] inside the contract until
	// the developer calls withdraw().
	//
	// Recording a payout here without evidence of the on-chain release would
	// produce a "paid" DB record for a bounty that still holds its ETH.
	// Instead we return onchain_release_required so the CRE workflow knows it
	// must execute the EVM write. The frontend then calls
	// POST /bounties/{id}/confirm-released after detecting the DeveloperFunded
	// or BountyReleased contract event.
	if !payments.ConnectEnabled() {
		writeJSON(w, http.StatusAccepted, internalPayoutResponse{
			Status:      "onchain_release_required",
			PaymentMode: "onchain",
		})
		return
	}

	// ── Stripe path ───────────────────────────────────────────────────────────
	dev, ok := h.store.GetDeveloper(b.MergedPR.Author)
	if !ok || dev.StripeAccountID == "" || !dev.StripeOnboarded {
		writeJSON(w, http.StatusConflict, internalPayoutResponse{Status: "developer_not_onboarded", PaymentMode: "stripe"})
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

	if err := h.store.RecordPayout(b.ID, store.Payout{StripeTransferID: transferID, CreatedAt: time.Now().UTC()}); err != nil {
		// Transfer succeeded on Stripe's side but DB write failed. Log and surface
		// the error so the operator can reconcile; do not broadcast bounty.paid.
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":            "stripe transfer succeeded but failed to record payout: " + err.Error(),
			"stripeTransferId": transferID,
		})
		return
	}

	h.hub.Broadcast(ws.Event{Type: "bounty.paid", Data: map[string]any{
		"bountyId":         b.ID,
		"stripeTransferId": transferID,
		"developer":        b.MergedPR.Author,
		"amountCents":      payoutAmount,
		"currency":         b.Currency,
		"feeCents":         fee,
		"paymentMode":      "stripe",
	}})

	writeJSON(w, http.StatusOK, internalPayoutResponse{Status: "paid", StripeTransferID: transferID, PaymentMode: "stripe"})
}

// paymentMode returns "stripe" when Stripe is configured, otherwise "onchain".
func paymentMode() string {
	if payments.ConnectEnabled() {
		return "stripe"
	}
	return "onchain"
}
