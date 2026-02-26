package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
)

var ErrMissingSecret = errors.New("GITHUB_WEBHOOK_SECRET is required")

type WebhookVerifier struct {
	secret []byte
}

func NewWebhookVerifierFromEnv() *WebhookVerifier {
	return &WebhookVerifier{secret: []byte(os.Getenv("GITHUB_WEBHOOK_SECRET"))}
}

func (v *WebhookVerifier) ReadAndVerify(r *http.Request) ([]byte, error) {
	if len(v.secret) == 0 {
		return nil, ErrMissingSecret
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}

	sig := r.Header.Get("X-Hub-Signature-256")
	if sig == "" {
		return nil, errors.New("missing X-Hub-Signature-256")
	}

	if !verifySignature256(v.secret, body, sig) {
		return nil, errors.New("invalid signature")
	}

	return body, nil
}

func verifySignature256(secret, body []byte, header string) bool {
	// header format: "sha256=<hex>"
	parts := strings.SplitN(header, "=", 2)
	if len(parts) != 2 {
		return false
	}
	if parts[0] != "sha256" {
		return false
	}
	given, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	expected := mac.Sum(nil)
	return hmac.Equal(given, expected)
}
