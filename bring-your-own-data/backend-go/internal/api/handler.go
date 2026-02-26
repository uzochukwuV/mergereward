package api

import (
	"encoding/json"
	"net/http"

	"mergereward-backend/internal/github"
	"mergereward-backend/internal/payments"
	"mergereward-backend/internal/store"
	"mergereward-backend/internal/ws"
)

type Deps struct {
	Store *store.Memory
	Hub   *ws.Hub
}

type Handler struct {
	store *store.Memory
	hub   *ws.Hub
}

func NewHandler(d Deps) *Handler {
	return &Handler{store: d.Store, hub: d.Hub}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /ws", h.handleWS)
	mux.HandleFunc("POST /webhooks/github", h.handleGitHubWebhook)
	mux.HandleFunc("POST /webhooks/stripe", h.handleStripeWebhook)

	mux.HandleFunc("POST /bounties", h.handleCreateBounty)
	mux.HandleFunc("POST /developers/stripe/onboard", h.handleDeveloperStripeOnboard)

	// Called by CRE (Confidential HTTP) after verifying the merge.
	mux.HandleFunc("POST /internal/payout", h.handleInternalPayout)

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
