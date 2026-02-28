package api

import (
	"encoding/json"
	"net/http"
	"os"

	"mergereward-backend/internal/auth"
	"mergereward-backend/internal/github"
	"mergereward-backend/internal/payments"
	"mergereward-backend/internal/store"
	"mergereward-backend/internal/ws"
)

type Deps struct {
	Store         store.Store
	Hub           *ws.Hub
	SessionSecret string
}

type Handler struct {
	store         store.Store
	hub           *ws.Hub
	sessionSecret string
}

func NewHandler(d Deps) *Handler {
	return &Handler{
		store:         d.Store,
		hub:           d.Hub,
		sessionSecret: d.SessionSecret,
	}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// ── Auth ──────────────────────────────────────────────────────────────────
	mux.HandleFunc("GET /auth/github", h.handleAuthGitHub)
	mux.HandleFunc("GET /auth/github/callback", h.handleAuthGitHubCallback)
	mux.Handle("GET /auth/me",
		auth.InjectAuth(h.sessionSecret)(http.HandlerFunc(h.handleAuthMe)))
	mux.HandleFunc("POST /auth/logout", h.handleAuthLogout)

	// ── Real-time ─────────────────────────────────────────────────────────────
	mux.HandleFunc("GET /ws", h.handleWS)

	// ── Webhooks ──────────────────────────────────────────────────────────────
	mux.HandleFunc("POST /webhooks/github", h.handleGitHubWebhook)
	mux.HandleFunc("POST /webhooks/stripe", h.handleStripeWebhook)

	// ── Bounties ──────────────────────────────────────────────────────────────
	mux.HandleFunc("POST /bounties", h.handleCreateBounty)

	// Claim requires a valid GitHub session; the handler reads login from context.
	mux.Handle("POST /bounties/claim",
		auth.RequireAuth(h.sessionSecret)(http.HandlerFunc(h.handleClaimBounty)))

	// ── Developers ────────────────────────────────────────────────────────────
	mux.HandleFunc("POST /developers/stripe/onboard", h.handleDeveloperStripeOnboard)

	// ── Internal (CRE-only, bearer-token protected) ────────────────────────────
	mux.HandleFunc("POST /internal/payout", h.handleInternalPayout)
	mux.HandleFunc("GET /internal/pending-payouts", h.handleInternalPendingPayouts)

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) stripeClient() (*payments.StripeClient, error) {
	return payments.NewStripeClientFromEnv()
}

func (h *Handler) githubVerifier() *github.WebhookVerifier {
	return github.NewWebhookVerifierFromEnv()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
