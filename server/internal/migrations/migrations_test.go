package migrations

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFilesKeepsBaselineAndHistoricalMigrationsMutuallyExclusive(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	serverDir := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(serverDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	t.Run("historical", func(t *testing.T) {
		t.Setenv("MULTICA_MIGRATION_BASELINE", "")
		files, err := Files("up")
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if filepath.Base(file) == "000_initial_schema.up.sql" {
				t.Fatal("historical migration path must not include the clean-install baseline")
			}
		}
	})

	t.Run("baseline", func(t *testing.T) {
		t.Setenv("MULTICA_MIGRATION_BASELINE", "true")
		files, err := Files("up")
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 || filepath.Base(files[0]) != "000_initial_schema.up.sql" {
			t.Fatalf("baseline migration files = %v", files)
		}
	})
}
