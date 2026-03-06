package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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
	PaymentMode   string `json:"paymentMode"`             // "stripe" | "onchain"
	CheckoutURL   string `json:"checkoutUrl,omitempty"`
	CheckoutID    string `json:"checkoutId,omitempty"`
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

	b := &store.Bounty{
		RepoID:      req.RepoID,
		IssueNumber: req.IssueNumber,
		AmountCents: req.AmountCents,
		Currency:    req.Currency,
		CreatedAt:   time.Now().UTC(),
		Status:      store.BountyStatusPendingPayment,
	}
	h.store.CreateBounty(b)

	// ── Stripe path (optional) ────────────────────────────────────────────────
	// If Stripe is not configured, fall through to the on-chain path. This
	// allows maintainers to fund bounties directly via the smart contract
	// without needing to set up Stripe at all.
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
	// The bounty record is created in the DB as an off-chain index entry.
	// The maintainer funds it directly on the smart contract. The CRE workflow
	// verifies merges and triggers `releaseBounty` on-chain; developer funds
	// accumulate in the contract until they call `withdraw()`.
	//
	// Mark the bounty as funded immediately — on-chain state is the source of
	// truth; this status just enables PR-matching and pending-payout queries.
	_ = h.store.SetBountyStatus(b.ID, store.BountyStatusFunded)

	writeJSON(w, http.StatusCreated, &createBountyResponse{
		BountyID:      b.ID,
		PaymentMode:   "onchain",
		PaymentStatus: string(store.BountyStatusFunded),
	})
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
