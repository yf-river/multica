package schema

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type stubQuerier struct {
	matches bool
	version int
	hash    string
	err     error
}

func (s stubQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	return stubSchemaRow(s)
}

type stubSchemaRow stubQuerier

func (r stubSchemaRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*bool)) = r.matches
	*(dest[1].(*int)) = r.version
	*(dest[2].(*string)) = r.hash
	return nil
}

func TestCurrentHash(t *testing.T) {
	if len(currentHash) != sha256HexLength {
		t.Fatalf("currentHash length = %d, want %d", len(currentHash), sha256HexLength)
	}
}

func TestVerifyCurrent(t *testing.T) {
	err := VerifyCurrent(context.Background(), stubQuerier{
		matches: true,
		version: currentVersion,
		hash:    currentHash,
	})
	if err != nil {
		t.Fatalf("VerifyCurrent() error = %v", err)
	}
}

func TestVerifyCurrentRejectsMismatch(t *testing.T) {
	err := VerifyCurrent(context.Background(), stubQuerier{
		matches: false,
		version: currentVersion + 1,
		hash:    strings.Repeat("0", sha256HexLength),
	})
	if !errors.Is(err, errVersionMismatch) {
		t.Fatalf("VerifyCurrent() error = %v, want errVersionMismatch", err)
	}
}

func TestVerifyCurrentReportsQueryError(t *testing.T) {
	queryErr := errors.New("query failed")
	err := VerifyCurrent(context.Background(), stubQuerier{err: queryErr})
	if !errors.Is(err, queryErr) {
		t.Fatalf("VerifyCurrent() error = %v, want wrapped query error", err)
	}
}

func TestVerifyCurrentRejectsNilDatabase(t *testing.T) {
	err := VerifyCurrent(context.Background(), nil)
	if err == nil {
		t.Fatal("VerifyCurrent() error = nil, want error")
	}
}
