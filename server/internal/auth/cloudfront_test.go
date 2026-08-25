package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"
)

func decodeCloudFrontBase64(t *testing.T, encoded string) string {
	t.Helper()
	standard := strings.NewReplacer("-", "+", "_", "=", "~", "/").Replace(encoded)
	decoded, err := base64.StdEncoding.DecodeString(standard)
	if err != nil {
		t.Fatalf("decode CloudFront base64: %v", err)
	}
	return string(decoded)
}

func TestCloudFrontSignedURLWithContentDisposition(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer := &CloudFrontSigner{
		keyPairID:  "K123",
		privateKey: key,
	}

	got := signer.SignedURLWithContentDisposition(
		"https://static.example.test/uploads/report.md?existing=1",
		`attachment; filename="report.md"`,
		time.Unix(1893456000, 0),
	)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse signed URL: %v", err)
	}
	q := u.Query()
	if got := q.Get("response-content-disposition"); got != `attachment; filename="report.md"` {
		t.Fatalf("response-content-disposition = %q", got)
	}
	if got := q.Get("Key-Pair-Id"); got != "K123" {
		t.Fatalf("Key-Pair-Id = %q", got)
	}
	if q.Get("Signature") == "" {
		t.Fatalf("missing Signature in %q", got)
	}

	policy := decodeCloudFrontBase64(t, q.Get("Policy"))
	if !strings.Contains(policy, "response-content-disposition=attachment%3B+filename%3D%22report.md%22") {
		t.Fatalf("policy did not include signed response-content-disposition: %s", policy)
	}
}

func TestNewCloudFrontSignerFromEnvRejectsPartialSigningConfig(t *testing.T) {
	clear := func(t *testing.T) {
		t.Helper()
		for _, key := range []string{
			"CLOUDFRONT_KEY_PAIR_ID", "CLOUDFRONT_DOMAIN", "COOKIE_DOMAIN",
			"CLOUDFRONT_PRIVATE_KEY_SECRET", "CLOUDFRONT_PRIVATE_KEY",
		} {
			t.Setenv(key, "")
		}
	}

	t.Run("private key without key pair", func(t *testing.T) {
		clear(t)
		t.Setenv("CLOUDFRONT_PRIVATE_KEY", "configured")
		if _, err := NewCloudFrontSignerFromEnv(); err == nil || !strings.Contains(err.Error(), "KEY_PAIR_ID") {
			t.Fatalf("NewCloudFrontSignerFromEnv error = %v, want key-pair error", err)
		}
	})

	t.Run("key pair without domain", func(t *testing.T) {
		clear(t)
		t.Setenv("CLOUDFRONT_KEY_PAIR_ID", "K123")
		if _, err := NewCloudFrontSignerFromEnv(); err == nil || !strings.Contains(err.Error(), "DOMAIN") {
			t.Fatalf("NewCloudFrontSignerFromEnv error = %v, want domain error", err)
		}
	})

	t.Run("public CDN domain alone does not opt into signing", func(t *testing.T) {
		clear(t)
		t.Setenv("CLOUDFRONT_DOMAIN", "cdn.example.test")
		signer, err := NewCloudFrontSignerFromEnv()
		if err != nil || signer != nil {
			t.Fatalf("NewCloudFrontSignerFromEnv = (%v, %v), want (nil, nil)", signer, err)
		}
	})
}
