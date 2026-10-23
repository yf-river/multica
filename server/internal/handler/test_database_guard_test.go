package handler

import (
	"os"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestDatabaseURLRejectsImplicitOrMismatchedTargets(t *testing.T) {
	const target = "postgres://fixture:private@127.0.0.1:55457/guard_test?sslmode=disable"
	for _, tc := range []struct{ name, target, declared string }{
		{"both missing", "", ""},
		{"target missing", "", target},
		{"declaration missing", target, ""},
		{"different database", target, strings.Replace(target, "guard_test", "other_test", 1)},
		{"target override", target + "&host=example.invalid", target + "&host=example.invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tc.target)
			t.Setenv("MULTICA_TEST_DATABASE_URL", tc.declared)
			if _, err := testutil.TestDatabaseURL(); err == nil {
				t.Fatal("unsafe test target accepted")
			} else if strings.Contains(err.Error(), "private") {
				t.Fatal("test password exposed")
			}
		})
	}
}

func TestDatabaseURLPreservesExplicitTarget(t *testing.T) {
	target := os.Getenv("DATABASE_URL")
	actual, err := testutil.TestDatabaseURL()
	if err != nil {
		t.Fatal(err)
	}
	if actual != target {
		t.Fatal("checked target differs from returned connection")
	}
}
