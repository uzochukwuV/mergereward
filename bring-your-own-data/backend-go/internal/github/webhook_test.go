package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookVerifier_ReadAndVerify(t *testing.T) {
	secret := []byte("testsecret")
	payload := `{"hello":"world"}`

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	r := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(payload))
	r.Header.Set("X-Hub-Signature-256", sig)

	v := &WebhookVerifier{secret: secret}
	b, err := v.ReadAndVerify(r)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(b) != payload {
		t.Fatalf("body mismatch: %q", string(b))
	}
}
