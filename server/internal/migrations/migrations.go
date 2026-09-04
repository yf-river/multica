package migrations

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/selfexec"
)

const maxSearchDepth = 4

var candidateLeaves = []string{
	"migrations",
	filepath.Join("server", "migrations"),
}

// ResolveDir returns the first migrations directory that exists from the
// current working directory.
func ResolveDir() (string, error) {
	seen := make(map[string]bool)
	for _, root := range searchRoots() {
		base := root
		for range maxSearchDepth + 1 {
			for _, leaf := range candidateLeaves {
				dir := filepath.Clean(filepath.Join(base, leaf))
				if seen[dir] {
					continue
				}
				seen[dir] = true
				info, err := os.Stat(dir)
				if err == nil && info.IsDir() {
					return dir, nil
				}
			}
			base = filepath.Join(base, "..")
		}
	}
	return "", fmt.Errorf("migrations directory not found")
}

func searchRoots() []string {
	roots := []string{"."}
	if exe, err := selfexec.Resolve(); err == nil {
		roots = append(roots, filepath.Dir(exe))
	}
	return roots
}

// Files returns sorted migration files for the given direction ("up" or
// "down").
func Files(direction string) ([]string, error) {
	dir, err := ResolveDir()
	if err != nil {
		return nil, err
	}

	suffix := "." + direction + ".sql"
	if strings.EqualFold(strings.TrimSpace(os.Getenv("MULTICA_MIGRATION_BASELINE")), "true") {
		baseline := filepath.Join(dir, "000_initial_schema"+suffix)
		if _, err := os.Stat(baseline); err != nil {
			return nil, fmt.Errorf("baseline migration not found: %w", err)
		}
		return []string{baseline}, nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "*"+suffix))
	if err != nil {
		return nil, err
	}
	// The clean-install baseline is a snapshot of the schema produced by the
	// historical migrations, so the two paths are mutually exclusive.
	baseline := filepath.Join(dir, "000_initial_schema"+suffix)
	filtered := files[:0]
	for _, file := range files {
		if file != baseline {
			filtered = append(filtered, file)
		}
	}
	files = filtered

	if direction == "down" {
		sort.Sort(sort.Reverse(sort.StringSlice(files)))
	} else {
		sort.Strings(files)
	}
	return files, nil
}

// AllVersions returns every "up" migration version found on disk, in apply
// order. The readiness check verifies that all of them are recorded in
// schema_migrations — checking only the lexically-last version would miss an
// out-of-order migration (one numbered below an already-applied later one),
// letting a server report ready while running against a schema that lacks it.
func AllVersions() ([]string, error) {
	files, err := Files("up")
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no up migrations found")
	}
	versions := make([]string, len(files))
	for i, f := range files {
		versions[i] = ExtractVersion(f)
	}
	return versions, nil
}

// ExtractVersion strips the .up.sql / .down.sql suffix from a migration file.
func ExtractVersion(filename string) string {
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, ".up.sql")
	base = strings.TrimSuffix(base, ".down.sql")
	return base
}
