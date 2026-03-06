package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"mergereward-backend/internal/auth"
	"mergereward-backend/internal/payments"
	"mergereward-backend/internal/store"
	"mergereward-backend/internal/ws"
)

// txHashRE matches a 0x-prefixed 64-hex-character EVM transaction hash.
var txHashRE = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

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
	PaymentMode   string `json:"paymentMode"`         // "stripe" | "onchain"
	CheckoutURL   string `json:"checkoutUrl,omitempty"`
	CheckoutID    string `json:"checkoutId,omitempty"`
	PaymentStatus string `json:"paymentStatus"`
}

func (h *Handler) handleCreateBounty(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}

	var req createBountyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.RepoID == "" || req.IssueNumber <= 0 || req.AmountCents <= 0 || req.Currency == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing required fields"})
		return
	}

	b := &store.Bounty{
		RepoID:             req.RepoID,
		IssueNumber:        req.IssueNumber,
		AmountCents:        req.AmountCents,
		Currency:           req.Currency,
		CreatedAt:          time.Now().UTC(),
		Status:             store.BountyStatusPendingPayment,
		CreatorGitHubLogin: claims.GitHubLogin,
	}
	h.store.CreateBounty(b)

	// ── Stripe path (optional) ────────────────────────────────────────────────
	if payments.ConnectEnabled() {
		if req.SuccessURL == "" || req.CancelURL == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "successUrl and cancelUrl are required for stripe payment"})
			return
		}

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
			PaymentMode:   "stripe",
			CheckoutURL:   session.URL,
			CheckoutID:    session.ID,
			PaymentStatus: string(b.Status),
		})
		return
	}

	// ── On-chain path (Stripe not configured) ────────────────────────────────
	// The bounty record is created with status pending_onchain. It stays there
	// until the frontend confirms the on-chain createBounty() tx via
	// POST /bounties/{id}/fund-onchain. Only after that transition to "funded"
	// does the bounty become eligible for the payout pipeline.
	//
	// We must NOT auto-mark as funded here: unfunded bounties would silently
	// enter the payout pipeline and produce incorrect "paid" records in the DB.
	if err := h.store.SetBountyStatus(b.ID, store.BountyStatusPendingOnchain); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to initialise bounty status: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, &createBountyResponse{
		BountyID:      b.ID,
		PaymentMode:   "onchain",
		PaymentStatus: string(store.BountyStatusPendingOnchain),
	})
}

// ── On-chain confirmation endpoints ─────────────────────────────────────────

type onchainConfirmRequest struct {
	TxHash string `json:"txHash"` // transaction hash for audit trail
}

// handleFundOnchain is called by the frontend after the maintainer's
// createBounty() on-chain transaction is confirmed. Transitions the bounty from
// pending_onchain → funded, making it eligible for the payout pipeline.
//
// Auth: any authenticated user (in practice the maintainer's browser).
// The txHash is stored for audit but is not verified on-chain here.
func (h *Handler) handleFundOnchain(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bounty id required"})
		return
	}

	var req onchainConfirmRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if !txHashRE.MatchString(req.TxHash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "txHash must be a 0x-prefixed 64-hex-character transaction hash"})
		return
	}

	b, ok := h.store.GetBounty(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bounty not found"})
		return
	}

	// Only the bounty creator may confirm on-chain funding.
	if b.CreatorGitHubLogin != claims.GitHubLogin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the bounty creator may confirm on-chain funding"})
		return
	}

	if b.Status != store.BountyStatusPendingOnchain {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "bounty is not awaiting on-chain funding",
			"status": string(b.Status),
		})
		return
	}

	if err := h.store.SetFundTxHash(id, req.TxHash); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to record fund tx hash: " + err.Error()})
		return
	}
	if err := h.store.SetBountyStatus(id, store.BountyStatusFunded); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to mark bounty as funded: " + err.Error()})
		return
	}

	h.hub.Broadcast(ws.Event{Type: "bounty.funded", Data: map[string]any{
		"bountyId":    id,
		"txHash":      req.TxHash,
		"paymentMode": "onchain",
	}})

	writeJSON(w, http.StatusOK, map[string]string{"status": "funded"})
}

// handleConfirmReleased is called by the frontend after it detects the
// DeveloperFunded or BountyReleased event on-chain, confirming that CRE's
// releaseBounty() call went through. Records the payout in the DB and
// broadcasts the bounty.paid WebSocket event.
//
// Auth: any authenticated user (in practice the developer whose wallet balance grew).
// The txHash is stored as the payment reference; on-chain re-verification is
// out of scope for this handler (an indexer can be added later).
func (h *Handler) handleConfirmReleased(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bounty id required"})
		return
	}

	var req onchainConfirmRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if !txHashRE.MatchString(req.TxHash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "txHash must be a 0x-prefixed 64-hex-character transaction hash"})
		return
	}

	b, ok := h.store.GetBounty(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bounty not found"})
		return
	}

	// Only the developer who claimed the bounty may confirm the on-chain release.
	if b.ClaimerGitHubLogin == "" || b.ClaimerGitHubLogin != claims.GitHubLogin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the bounty claimer may confirm on-chain release"})
		return
	}

	if b.Status != store.BountyStatusFunded {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "bounty is not in funded state",
			"status": string(b.Status),
		})
		return
	}
	if b.MergedPR == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no merged PR recorded for this bounty"})
		return
	}
	// Idempotent: if already confirmed with the same txHash, return success.
	if b.Payout != nil {
		if b.Payout.StripeTransferID == req.TxHash {
			writeJSON(w, http.StatusOK, map[string]string{"status": "already_confirmed"})
		} else {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "bounty already confirmed with a different transaction hash"})
		}
		return
	}

	// Store the on-chain tx hash as the payment reference.
	if err := h.store.RecordPayout(id, store.Payout{
		StripeTransferID: req.TxHash,
		CreatedAt:        time.Now().UTC(),
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to record payout: " + err.Error()})
		return
	}

	h.hub.Broadcast(ws.Event{Type: "bounty.paid", Data: map[string]any{
		"bountyId":    id,
		"txHash":      req.TxHash,
		"developer":   b.MergedPR.Author,
		"amountCents": b.AmountCents,
		"currency":    b.Currency,
		"paymentMode": "onchain",
	}})

	writeJSON(w, http.StatusOK, map[string]string{"status": "confirmed"})
}

// handleGetBounty returns a single bounty by ID.
func (h *Handler) handleGetBounty(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bounty id required"})
		return
	}
	b, ok := h.store.GetBounty(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bounty not found"})
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// handleListBounties returns the bounty for a specific repo+issue (if it exists).
func (h *Handler) handleListBounties(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repoID := q.Get("repoId")
	issueStr := q.Get("issueNumber")
	if repoID == "" || issueStr == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repoId and issueNumber query params required"})
		return
	}
	issueNumber, err := strconv.Atoi(issueStr)
	if err != nil || issueNumber <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "issueNumber must be a positive integer"})
		return
	}
	b, ok := h.store.FindBountyByRepoIssue(repoID, issueNumber)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"bounty": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bounty": b})
}
