package codediff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func command(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	command(t, dir, "init")
	command(t, dir, "config", "user.email", "test@example.com")
	command(t, dir, "config", "user.name", "Test")
	write(t, dir, "tracked.txt", "original\n")
	command(t, dir, "add", ".")
	command(t, dir, "commit", "-m", "base")
	return dir
}
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	tree, err := Snapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
func collect(t *testing.T, dir, tree string) *Diff {
	t.Helper()
	d, err := Collect(context.Background(), dir, tree)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestRunBoundariesPreserveIndexAndExcludeExistingChanges(t *testing.T) {
	dir := repo(t)
	write(t, dir, "existing.txt", "pre-existing\n")
	write(t, dir, "tracked.txt", "before run\n")
	command(t, dir, "add", "tracked.txt")
	index := command(t, dir, "diff", "--cached")
	head := command(t, dir, "rev-parse", "HEAD")
	before := snapshot(t, dir)
	write(t, dir, "tracked.txt", "after run\n")
	d := collect(t, dir, before)
	if len(d.Files) != 1 || d.Files[0].Path != "tracked.txt" || !strings.Contains(d.Files[0].Patch, "-before run\n+after run") || strings.Contains(d.Files[0].Patch, "original") {
		t.Fatalf("unexpected diff: %+v", d)
	}
	if command(t, dir, "diff", "--cached") != index || command(t, dir, "rev-parse", "HEAD") != head {
		t.Fatal("capture changed user's index or HEAD")
	}
}
func TestCommittedAddedDeletedBinaryAndUnusualPaths(t *testing.T) {
	dir := repo(t)
	before := snapshot(t, dir)
	_ = os.Remove(filepath.Join(dir, "tracked.txt"))
	write(t, dir, "new\tfile.txt", "new\n")
	write(t, dir, "binary.bin", "a\x00b")
	command(t, dir, "add", ".")
	command(t, dir, "commit", "-m", "agent commit")
	d := collect(t, dir, before)
	if len(d.Files) != 3 {
		t.Fatalf("got %+v", d)
	}
	byPath := map[string]File{}
	for _, f := range d.Files {
		byPath[f.Path] = f
	}
	if !byPath["binary.bin"].Binary || byPath["tracked.txt"].Deleted != 1 || byPath["new\tfile.txt"].Added != 1 {
		t.Fatalf("unexpected files: %+v", d.Files)
	}
}
func TestNoChangesAndHistoryAndRepeatedRuns(t *testing.T) {
	dir := repo(t)
	before := snapshot(t, dir)
	if len(collect(t, dir, before).Files) != 0 {
		t.Fatal("no-op captured changes")
	}
	write(t, dir, "tracked.txt", "first\n")
	first := collect(t, dir, before)
	beforeSecond := snapshot(t, dir)
	write(t, dir, "tracked.txt", "second\n")
	second := collect(t, dir, beforeSecond)
	if !strings.Contains(second.Files[0].Patch, "-first\n+second") || strings.Contains(first.Files[0].Patch, "second") {
		t.Fatal("historical or second-run baseline changed")
	}
}
func TestBoundsAndIgnoredFiles(t *testing.T) {
	dir := repo(t)
	write(t, dir, ".gitignore", "secret\n")
	before := snapshot(t, dir)
	write(t, dir, "secret", "never captured")
	write(t, dir, "large.txt", strings.Repeat("line\n", 50000))
	d := collect(t, dir, before)
	if len(d.Files) != 1 || !d.Truncated || !d.Files[0].Truncated || len(d.Files[0].Patch) > MaxPatchBytes {
		t.Fatalf("unexpected bounds: files=%d truncated=%v patch bytes=%d", len(d.Files), d.Truncated, len(d.Files[0].Patch))
	}
}
func TestNestedDirectoryRejected(t *testing.T) {
	dir := repo(t)
	sub := filepath.Join(dir, "sub")
	_ = os.Mkdir(sub, 0700)
	if _, err := Snapshot(context.Background(), sub); err == nil {
		t.Fatal("accepted enclosing unrelated repository")
	}
}
func TestEmptyRepository(t *testing.T) {
	dir := t.TempDir()
	command(t, dir, "init")
	before := snapshot(t, dir)
	write(t, dir, "first.txt", "hello\n")
	d := collect(t, dir, before)
	if len(d.Files) != 1 || d.Files[0].Added != 1 {
		t.Fatalf("got %+v", d)
	}
}
