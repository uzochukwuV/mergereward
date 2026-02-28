package api

import (
	"net/http"
	"os"

	"mergereward-backend/internal/auth"
	"mergereward-backend/internal/store"
)

// handleAuthGitHub starts the GitHub OAuth flow.
// The client is redirected to GitHub, which then redirects back to
// /auth/github/callback with a code and state parameter.
func (h *Handler) handleAuthGitHub(w http.ResponseWriter, r *http.Request) {
	if !auth.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "GitHub OAuth is not configured (GITHUB_CLIENT_ID / GITHUB_CLIENT_SECRET missing)",
		})
		return
	}

	state, err := auth.SetStateCookie(w)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate state"})
		return
	}
	http.Redirect(w, r, auth.AuthURL(state), http.StatusFound)
}

// handleAuthGitHubCallback handles the GitHub OAuth redirect.
// It exchanges the code for an access token, fetches the GitHub user, upserts
// a developer record, issues a session JWT cookie, and redirects to the frontend.
func (h *Handler) handleAuthGitHubCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if !auth.ValidateState(r, state) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid oauth state"})
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing code"})
		return
	}

	accessToken, err := auth.ExchangeCode(code)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "token exchange failed: " + err.Error()})
		return
	}

	ghUser, err := auth.FetchUser(accessToken)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "github user fetch failed: " + err.Error()})
		return
	}

	// Ensure a developer record exists for this user.
	h.store.UpsertDeveloper(store.Developer{
		GitHubLogin: ghUser.Login,
	})

	secret := os.Getenv("SESSION_SECRET")
	if err := auth.IssueSession(w, secret, ghUser.Login, ghUser.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session issue failed"})
		return
	}

	// Redirect to the frontend; override with FRONTEND_URL env if set.
	redirectTo := envOr("FRONTEND_URL", "/auth/me")
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

// handleAuthMe returns the currently authenticated user's profile.
func (h *Handler) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}
	dev, _ := h.store.GetDeveloper(claims.GitHubLogin)
	writeJSON(w, http.StatusOK, map[string]any{
		"githubLogin":     claims.GitHubLogin,
		"githubId":        claims.GitHubID,
		"stripeOnboarded": dev != nil && dev.StripeOnboarded,
	})
}

// handleAuthLogout clears the session cookie.
func (h *Handler) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	auth.ClearSession(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}
