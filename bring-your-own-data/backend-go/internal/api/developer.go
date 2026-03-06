package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"mergereward-backend/internal/auth"
	"mergereward-backend/internal/payments"
)

type setWalletRequest struct {
	WalletAddress string `json:"walletAddress"`
}

// handleGetMe returns the current developer's full profile including wallet address.
// Extends the basic /auth/me response with developer-specific fields.
func (h *Handler) handleGetMe(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}
	dev, _ := h.store.GetDeveloper(claims.GitHubLogin)

	resp := map[string]any{
		"githubLogin": claims.GitHubLogin,
		"githubId":    claims.GitHubID,
		"paymentMode": paymentMode(),
	}

	if dev != nil {
		resp["stripeOnboarded"] = dev.StripeOnboarded
		resp["walletAddress"] = dev.WalletAddress
	} else {
		resp["stripeOnboarded"] = false
		resp["walletAddress"] = ""
	}

	// Tell the frontend whether Stripe is available so it can conditionally
	// render onboarding / payout UI elements.
	resp["stripeEnabled"] = payments.ConnectEnabled()

	writeJSON(w, http.StatusOK, resp)
}

// handleSetWallet saves the developer's EVM wallet address (for on-chain payouts).
// The developer can update this at any time; the on-chain `withdraw()` call uses
// whatever address the developer calls from — this is just stored for display.
func (h *Handler) handleSetWallet(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}

	var req setWalletRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if !isValidEVMAddress(req.WalletAddress) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid EVM wallet address (must be 0x + 40 hex chars)"})
		return
	}

	if err := h.store.SetDeveloperWallet(claims.GitHubLogin, req.WalletAddress); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "developer record not found — log in again"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":        "ok",
		"walletAddress": req.WalletAddress,
	})
}

// isValidEVMAddress returns true for a checksummed or lowercase 0x-prefixed 20-byte hex address.
func isValidEVMAddress(addr string) bool {
	if len(addr) != 42 {
		return false
	}
	if !strings.HasPrefix(addr, "0x") && !strings.HasPrefix(addr, "0X") {
		return false
	}
	for _, c := range addr[2:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
