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
		isNew   bool
		wantErr bool
	}{
		{"allow_signup_true_new", Config{AllowSignup: true}, "alice", true, false},
		{"allow_signup_false_new", Config{AllowSignup: false}, "alice", true, true},
		{"allow_signup_false_existing", Config{AllowSignup: false}, "alice", false, false},
		{"account_allowlist_match", Config{AllowSignup: false, AllowedAccounts: []string{"alice"}}, "alice", true, false},
		{"account_allowlist_case_insensitive", Config{AllowSignup: false, AllowedAccounts: []string{"Alice"}}, "alice", true, false},
		{"account_allowlist_mismatch", Config{AllowSignup: true, AllowedAccounts: []string{"alice"}}, "bob", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(tt.cfg)
			err := h.checkSignupAllowed(tt.account, tt.isNew)
			if (err != nil) != tt.wantErr {
				t.Fatalf("got err=%v wantErr=%v", err, tt.wantErr)
			}
		})
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
