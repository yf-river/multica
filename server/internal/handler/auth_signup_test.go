package handler

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newTestHandler(cfg Config) *Handler {
	return &Handler{
		cfg: cfg,
	}
}

func TestSignupGating(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		account string
		want    string
	}{
		{"signup enabled", Config{AllowSignup: true}, "alice", ""},
		{"signup disabled", Config{AllowSignup: false}, "alice", "user registration is disabled on this self-hosted instance"},
		{"allowlist match", Config{AllowSignup: false, AllowedAccounts: []string{"alice"}}, "alice", ""},
		{"allowlist case insensitive", Config{AllowSignup: false, AllowedAccounts: []string{"Alice"}}, "alice", ""},
		{"allowlist mismatch", Config{AllowSignup: true, AllowedAccounts: []string{"alice"}}, "bob", "account not allowed on this instance"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.cfg)
			if got := h.signupRestriction(tt.account); got != tt.want {
				t.Fatalf("signupRestriction() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVerifyPasswordCurrentEncoding(t *testing.T) {
	const encoded = "pbkdf2_sha256$210000$bXVsdGljYS1jdXJyZW50IQ$P2O4EHC/ii6Tw0uTqYTYlM8ub0Vc0Fhc0Kzf+1SHSVA"
	if !verifyPassword("Strong1!", encoded) {
		t.Fatal("current PBKDF2-SHA256 encoding did not verify")
	}
	if verifyPassword("wrong password", encoded) {
		t.Fatal("wrong password verified against current encoding")
	}
}

func TestValidatePasswordRejectsDisallowedCharacterAfterAllowedSpecial(t *testing.T) {
	if got := validatePassword("Aa1! password"); got != "password contains invalid characters" {
		t.Fatalf("validatePassword accepted a space after a valid special character: %q", got)
	}
}

type mockDB struct {
	db.DBTX
	getUserErr error
}

func (m *mockDB) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	return &mockRow{err: m.getUserErr}
}

func (m *mockDB) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("INSERT 1"), nil
}

type mockRow struct {
	pgx.Row
	err error
}

func (m *mockRow) Scan(dest ...interface{}) error {
	return m.err
}
