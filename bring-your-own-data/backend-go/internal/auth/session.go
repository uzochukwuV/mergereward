package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	sessionCookie = "mr_session"
	stateCookie   = "mr_oauth_state"
	sessionTTL    = 7 * 24 * time.Hour
	stateTTL      = 5 * 60 // seconds
)

// Claims is the JWT payload stored in the session cookie.
type Claims struct {
	GitHubLogin string `json:"github_login"`
	GitHubID    int64  `json:"github_id"`
	jwt.RegisteredClaims
}

type contextKey struct{}

// IssueSession signs a JWT and writes it as an HttpOnly cookie.
func IssueSession(w http.ResponseWriter, secret, login string, id int64) error {
	claims := Claims{
		GitHubLogin: login,
		GitHubID:    id,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(sessionTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    signed,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	return nil
}

// ClearSession deletes the session cookie.
func ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// ValidateSession parses and validates the session cookie, returning the claims.
func ValidateSession(r *http.Request, secret string) (*Claims, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, errors.New("no session")
	}
	token, err := jwt.ParseWithClaims(cookie.Value, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// ClaimsFromContext retrieves Claims injected by RequireAuth or InjectAuth.
func ClaimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(contextKey{}).(*Claims)
	return c
}

// RequireAuth rejects unauthenticated requests with 401.
func RequireAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, err := ValidateSession(r, secret)
			if err != nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), contextKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// InjectAuth injects claims when a valid session exists, but does not block.
func InjectAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if claims, err := ValidateSession(r, secret); err == nil {
				ctx := context.WithValue(r.Context(), contextKey{}, claims)
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ─── OAuth state cookie ───────────────────────────────────────────────────────

// SetStateCookie generates a random state nonce, stores it in a short-lived
// cookie, and returns it for inclusion in the GitHub OAuth redirect URL.
func SetStateCookie(w http.ResponseWriter) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	state := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   stateTTL,
	})
	return state, nil
}

// ValidateState checks the state cookie against the value returned by GitHub.
func ValidateState(r *http.Request, state string) bool {
	if state == "" {
		return false
	}
	cookie, err := r.Cookie(stateCookie)
	if err != nil {
		return false
	}
	return cookie.Value == state
}
