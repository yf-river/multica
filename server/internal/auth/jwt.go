package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

const defaultJWTSecret = "multica-dev-secret-change-in-production"

const (
	composeJWTSecretPlaceholder = "change-me-in-production"
	minimumJWTSecretBytes       = 32
)

var (
	jwtSecret     []byte
	jwtSecretOnce sync.Once
)

func JWTSecret() []byte {
	jwtSecretOnce.Do(func() {
		secret := os.Getenv("JWT_SECRET")
		if secret == "" {
			secret = defaultJWTSecret
		}
		jwtSecret = []byte(secret)
	})

	return jwtSecret
}

// ValidateJWTConfiguration rejects weak signing keys in production while
// preserving the zero-setup local development fallback.
func ValidateJWTConfiguration(appEnv, secret string) error {
	if !strings.EqualFold(strings.TrimSpace(appEnv), "production") {
		return nil
	}
	return ValidateJWTSecret(secret)
}

// ValidateJWTSecret returns a safe diagnostic without echoing secret data.
func ValidateJWTSecret(secret string) error {
	if secret == "" {
		return errors.New("JWT_SECRET is not set")
	}
	if secret != strings.TrimSpace(secret) {
		return errors.New("JWT_SECRET must not contain surrounding whitespace")
	}
	if secret == defaultJWTSecret || secret == composeJWTSecretPlaceholder {
		return errors.New("JWT_SECRET uses a published placeholder")
	}
	if len(secret) < minimumJWTSecretBytes {
		return fmt.Errorf("JWT_SECRET must contain at least %d bytes", minimumJWTSecretBytes)
	}
	return nil
}

// GeneratePATToken creates a new personal access token: "mul_" + 40 random hex chars.
func GeneratePATToken() (string, error) {
	b := make([]byte, 20) // 20 bytes = 40 hex chars
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate PAT token: %w", err)
	}
	return "mul_" + hex.EncodeToString(b), nil
}

// DerivePATToken deterministically derives a PAT for one authenticated create
// operation. The caller-owned request key is public; security comes from the
// same deployment secret that already authorizes user sessions. This lets the
// server replay a response after it was lost without storing the raw PAT.
func DerivePATToken(userID, requestKey string) string {
	mac := hmac.New(sha256.New, JWTSecret())
	_, _ = mac.Write([]byte("pat-create\x00" + userID + "\x00" + requestKey))
	return "mul_" + hex.EncodeToString(mac.Sum(nil)[:20])
}

// GenerateDaemonToken creates a new daemon auth token: "mdt_" + 40 random hex chars.
func GenerateDaemonToken() (string, error) {
	b := make([]byte, 20) // 20 bytes = 40 hex chars
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate daemon token: %w", err)
	}
	return "mdt_" + hex.EncodeToString(b), nil
}

// GenerateAgentTaskToken creates a new task-scoped agent auth token:
// "mat_" + 40 random hex chars. The token is single-purpose — bound to a
// specific (agent_id, task_id) pair on the server side — and is what the
// daemon injects into the agent process in place of its own owner PAT.
// See MUL-2600.
func GenerateAgentTaskToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate agent task token: %w", err)
	}
	return "mat_" + hex.EncodeToString(b), nil
}

// HashToken returns the hex-encoded SHA-256 hash of a token string.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
