package codediff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	command(t, dir, "init", "-b", "main")
	command(t, dir, "config", "user.email", "test@example.com")
	command(t, dir, "config", "user.name", "Test")
	write(t, dir, "tracked.txt", "original\n")
	command(t, dir, "add", ".")
	command(t, dir, "commit", "-m", "base")
	return dir
}

func snapshotAt(t *testing.T, dir string) *Snapshot {
	t.Helper()
	snap, err := TakeSnapshot(context.Background(), dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func filesByPath(root Root) map[string]File {
	out := map[string]File{}
	for _, file := range root.Files {
		out[file.Path] = file
	}
	return out
}

// A plain directory has no git tree to compare, which used to be the whole
// reason a run showed nothing. Every file it wrote must still appear.
func TestPlainDirectoryCapturesEveryFile(t *testing.T) {
	dir := t.TempDir()
	before := snapshotAt(t, dir)
	defer before.Close()
	write(t, dir, "notes.txt", "one\n")
	write(t, dir, "src/app.go", "package main\n\nfunc main() {}\n")

	root := BuildRoot(context.Background(), RootSpec{Path: dir, Before: before, Kind: "workdir"}, DefaultOptions())
	if root.Unavailable {
		t.Fatalf("unexpected unavailable: %s", root.Reason)
	}
	files := filesByPath(root)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %+v", root.Files)
	}
	if files["notes.txt"].Change != "added" || !strings.Contains(files["notes.txt"].Patch, "+one") {
		t.Fatalf("notes.txt not captured with a patch: %+v", files["notes.txt"])
	}
	if files["src/app.go"].Lines != 3 {
		t.Fatalf("line count missing: %+v", files["src/app.go"])
	}
}

// Ignored and untracked files are changes too: the user asks what the agent did,
// not what git would commit.
func TestIgnoredAndUntrackedFilesAreIncluded(t *testing.T) {
	dir := repo(t)
	write(t, dir, ".gitignore", "dist/\n*.log\n")
	before := snapshotAt(t, dir)
	defer before.Close()
	write(t, dir, "dist/bundle.js", "built\n")
	write(t, dir, "debug.log", "log line\n")
	write(t, dir, "tracked.txt", "changed\n")

	root := BuildRoot(context.Background(), RootSpec{Path: dir, Before: before, Kind: "workdir"}, DefaultOptions())
	files := filesByPath(root)
	for _, path := range []string{"dist/bundle.js", "debug.log", "tracked.txt"} {
		if _, ok := files[path]; !ok {
			t.Fatalf("missing %s in %+v", path, root.Files)
		}
	}
	if !strings.Contains(files["tracked.txt"].Patch, "-original") {
		t.Fatalf("tracked change has no baseline patch: %+v", files["tracked.txt"])
	}
}

// A repository checked out during the run is a baseline, not a pile of new
// files: only the files the agent actually touched count.
func TestCheckedOutRepositoryUsesUpstreamBaseline(t *testing.T) {
	source := repo(t)
	write(t, source, "README.md", "readme\n")
	command(t, source, "add", ".")
	command(t, source, "commit", "-m", "readme")

	work := t.TempDir()
	before := snapshotAt(t, work)
	defer before.Close()
	command(t, work, "clone", source, "project")
	write(t, work, "project/tracked.txt", "edited after checkout\n")

	root := BuildRoot(context.Background(), RootSpec{Path: work, Before: before, Kind: "workdir"}, DefaultOptions())
	files := filesByPath(root)
	if len(files) != 1 {
		t.Fatalf("expected only the edited file, got %+v", root.Files)
	}
	if _, ok := files["project/tracked.txt"]; !ok {
		t.Fatalf("edited file missing: %+v", root.Files)
	}
}

// The GONG-57 shape: the agent creates a worktree outside its task directory and
// commits there. The card must show it, based on the branch creation point.
func TestWorktreeOutsideTaskDirectoryIsCaptured(t *testing.T) {
	repoDir := repo(t)
	work := t.TempDir()
	outside := filepath.Join(t.TempDir(), "wt")
	capture := StartCapture(context.Background(), CaptureSpec{
		WorkDir: work,
		Repos:   []string{repoDir},
		Options: DefaultOptions(),
	})
	command(t, repoDir, "worktree", "add", "-b", "try/gong", outside, "HEAD")
	write(t, outside, "tracked.txt", "worktree edit\n")
	command(t, outside, "add", ".")
	command(t, outside, "commit", "-m", "worktree change")

	diff := capture.Finish(context.Background())
	if diff == nil {
		t.Fatal("no diff produced")
	}
	var external *Root
	for index := range diff.Roots {
		if diff.Roots[index].Kind == "external" {
			external = &diff.Roots[index]
		}
	}
	if external == nil {
		t.Fatalf("worktree outside the task directory was not captured: %+v", diff.Roots)
	}
	if external.Attribution != "task" {
		t.Fatalf("unexpected attribution: %+v", external)
	}
	files := filesByPath(*external)
	if len(files) != 1 || !strings.Contains(files["tracked.txt"].Patch, "-original") {
		t.Fatalf("unexpected files: %+v", external.Files)
	}
	if external.Commits != 1 {
		t.Fatalf("commit count should describe the branch, got %d", external.Commits)
	}
}

func TestConcurrentRunsMarkWorktreeUnassigned(t *testing.T) {
	repoDir := repo(t)
	work := t.TempDir()
	outside := filepath.Join(t.TempDir(), "wt")
	capture := StartCapture(context.Background(), CaptureSpec{
		WorkDir:              work,
		Repos:                []string{repoDir},
		ConcurrentLocalTasks: 1,
		Options:              DefaultOptions(),
	})
	command(t, repoDir, "worktree", "add", "-b", "try/other", outside, "HEAD")
	write(t, outside, "tracked.txt", "someone else's edit\n")
	diff := capture.Finish(context.Background())
	for _, root := range diff.Roots {
		if root.Kind != "external" {
			continue
		}
		if root.Attribution != "unassigned" {
			t.Fatalf("expected unassigned, got %+v", root)
		}
		if root.Reason == "" {
			t.Fatal("an unassigned root must explain itself")
		}
		return
	}
	t.Fatalf("external worktree missing: %+v", diff.Roots)
}

func TestSecretFilesAreListedButNotCopied(t *testing.T) {
	dir := t.TempDir()
	before := snapshotAt(t, dir)
	defer before.Close()
	write(t, dir, ".env", "API_KEY=abcdef1234567890abcdef\n")
	write(t, dir, "config.yaml", "api_key: sk-abcdefghijklmnopqrstuvwxyz012345\n")

	root := BuildRoot(context.Background(), RootSpec{Path: dir, Before: before}, DefaultOptions())
	files := filesByPath(root)
	env, ok := files[".env"]
	if !ok {
		t.Fatalf(".env not listed: %+v", root.Files)
	}
	if !env.Redacted || env.Patch != "" {
		t.Fatalf(".env content leaked: %+v", env)
	}
	config := files["config.yaml"]
	if !config.Redacted || strings.Contains(config.Patch, "sk-abcdefghijklmnopqrstuvwxyz012345") {
		t.Fatalf("secret inside a patch leaked: %+v", config)
	}
}

func TestDependencyDirectoriesAreSummarised(t *testing.T) {
	dir := t.TempDir()
	before := snapshotAt(t, dir)
	defer before.Close()
	write(t, dir, "node_modules/left-pad/index.js", "module.exports = 1\n")
	write(t, dir, "src/index.js", "console.log(1)\n")

	root := BuildRoot(context.Background(), RootSpec{Path: dir, Before: before}, DefaultOptions())
	if _, ok := filesByPath(root)["node_modules/left-pad/index.js"]; ok {
		t.Fatal("dependency file should not be listed individually")
	}
	if len(root.Dirs) != 1 || root.Dirs[0].Path != "node_modules" || root.Dirs[0].Files != 1 {
		t.Fatalf("unexpected directory summary: %+v", root.Dirs)
	}
	if _, ok := filesByPath(root)["src/index.js"]; !ok {
		t.Fatal("ordinary file missing")
	}
}

func TestCaptureFailureIsRecordedNotSilent(t *testing.T) {
	capture := StartCapture(context.Background(), CaptureSpec{
		WorkDir: filepath.Join(t.TempDir(), "missing"),
		Options: DefaultOptions(),
	})
	diff := capture.Finish(context.Background())
	if diff == nil || !diff.Unavailable || diff.Reason == "" || len(diff.Roots) != 1 || !diff.Roots[0].Unavailable {
		t.Fatalf("a failed capture must explain itself: %+v", diff)
	}
}

func TestLargePatchIsTruncated(t *testing.T) {
	dir := t.TempDir()
	before := snapshotAt(t, dir)
	defer before.Close()
	write(t, dir, "big.txt", strings.Repeat("line\n", 80_000))

	root := BuildRoot(context.Background(), RootSpec{Path: dir, Before: before}, DefaultOptions())
	file := filesByPath(root)["big.txt"]
	if len(file.Patch) > MaxPatchBytes || !file.Truncated {
		t.Fatalf("patch not bounded: %d bytes truncated=%v", len(file.Patch), file.Truncated)
	}
	if file.Added != 80_000 {
		t.Fatalf("counts must describe the whole change, got %d", file.Added)
	}
}

func TestUnifiedPatchShape(t *testing.T) {
	patch, added, deleted := buildPatch("a.txt", "one\ntwo\nthree\n", "one\nTWO\nthree\nfour\n")
	if added != 2 || deleted != 1 {
		t.Fatalf("unexpected counts: +%d -%d", added, deleted)
	}
	if !strings.HasPrefix(patch, "--- a/a.txt\n+++ b/a.txt\n@@") {
		t.Fatalf("unexpected header: %s", patch)
	}
	if !strings.Contains(patch, "-two") || !strings.Contains(patch, "+TWO") {
		t.Fatalf("missing change lines: %s", patch)
	}
}

func TestSnapshotKeepsFilesWhenBudgetIsTight(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 200; i++ {
		write(t, dir, filepath.Join("many", string(rune('a'+i%26)), "f.txt"), "x\n")
	}
	options := DefaultOptions()
	options.TimeBudget = 50 * time.Millisecond
	snap, err := TakeSnapshot(context.Background(), dir, options)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if len(snap.Files) == 0 {
		t.Fatal("budget exhaustion must keep what was already seen")
	}
}

func TestExclusionsDoNotSwallowTheRoot(t *testing.T) {
	parent := t.TempDir()
	work := filepath.Join(parent, "workdir")
	write(t, work, "a.txt", "one\n")
	snap, err := TakeSnapshot(context.Background(), work, Options{
		TimeBudget:      5 * time.Second,
		ExcludePaths:    []string{parent},
		MaxFiles:        100,
		MaxFileBytes:    1 << 20,
		MaxHashBytes:    1 << 20,
		MaxContentBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if len(snap.Files) != 1 {
		t.Fatalf("an ancestor exclusion must not hide the root's files: %+v", snap.Files)
	}
}
