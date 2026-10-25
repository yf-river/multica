package codediff

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCrossControlCleanRepositoryHasNoRunChanges(t *testing.T) {
	d := repo(t)
	b := snapshotAt(t, d)
	defer b.Close()
	r := BuildRoot(context.Background(), RootSpec{Path: d, Before: b}, DefaultOptions())
	if len(r.Files) != 0 {
		t.Fatalf("clean control changed: %v", r.Files)
	}
}
func TestCrossPreexistingDeletionIsNotRunChange(t *testing.T) {
	d := repo(t)
	os.Remove(filepath.Join(d, "tracked.txt"))
	oracleBefore := command(t, d, "diff", "--name-status")
	b := snapshotAt(t, d)
	defer b.Close()
	t.Logf("Git working tree unchanged during run: %v", oracleBefore == command(t, d, "diff", "--name-status"))
	r := BuildRoot(context.Background(), RootSpec{Path: d, Before: b}, DefaultOptions())
	if len(r.Files) != 0 {
		t.Fatalf("preexisting deletion attributed to no-op run: %v", r.Files)
	}
}
func TestCrossRestoringPreexistingDeletionIsRunChange(t *testing.T) {
	d := repo(t)
	os.Remove(filepath.Join(d, "tracked.txt"))
	b := snapshotAt(t, d)
	defer b.Close()
	write(t, d, "tracked.txt", "original\n")
	r := BuildRoot(context.Background(), RootSpec{Path: d, Before: b}, DefaultOptions())
	if len(r.Files) != 1 {
		t.Fatalf("restored file was not recorded as run edit: %v", r.Files)
	}
}
func TestCrossControlOrdinaryEditIsVisible(t *testing.T) {
	d := repo(t)
	b := snapshotAt(t, d)
	defer b.Close()
	write(t, d, "tracked.txt", "actual edit\n")
	r := BuildRoot(context.Background(), RootSpec{Path: d, Before: b}, DefaultOptions())
	f, ok := filesByPath(r)["tracked.txt"]
	if !ok || !strings.Contains(f.Patch, "-original") || !strings.Contains(f.Patch, "+actual edit") {
		t.Fatalf("normal edit missing: %v", r.Files)
	}
}
func TestCrossControlPreexistingModifiedFileIsNotRunChange(t *testing.T) {
	d := repo(t)
	write(t, d, "tracked.txt", "preexisting edit\n")
	b := snapshotAt(t, d)
	defer b.Close()
	r := BuildRoot(context.Background(), RootSpec{Path: d, Before: b}, DefaultOptions())
	if len(r.Files) != 0 {
		t.Fatalf("preexisting content edit leaked: %v", r.Files)
	}
}
func TestCrossControlPlainSecretRedacted(t *testing.T) {
	if _, ok := RedactLine("token=AUDIT_SYNTHETIC_ONLY_01234567890123456789"); !ok {
		t.Fatal("plain token control not redacted")
	}
}
func TestCrossControlCloneUniqueBodiesIsClean(t *testing.T) {
	src := repo(t)
	write(t, src, "different.txt", "different\n")
	command(t, src, "add", ".")
	command(t, src, "commit", "-m", "fixture unique files")
	d := t.TempDir()
	b := snapshotAt(t, d)
	defer b.Close()
	command(t, d, "clone", src, "project")
	r := BuildRoot(context.Background(), RootSpec{Path: d, Before: b}, DefaultOptions())
	if len(r.Files) != 0 {
		t.Fatalf("unique-body clone control not clean: %v", r.Files)
	}
}
func TestCrossControlNestedContentEditVisible(t *testing.T) {
	src := repo(t)
	d := t.TempDir()
	b := snapshotAt(t, d)
	defer b.Close()
	command(t, d, "clone", src, "project")
	write(t, d, "project/tracked.txt", "edited\n")
	r := BuildRoot(context.Background(), RootSpec{Path: d, Before: b}, DefaultOptions())
	f, ok := filesByPath(r)["project/tracked.txt"]
	if !ok || !strings.Contains(f.Patch, "+edited") {
		t.Fatalf("nested modify control missing: %v", r.Files)
	}
}
func TestCrossControlFullScanDoesNotInventDeletion(t *testing.T) {
	d := t.TempDir()
	write(t, d, "b.txt", "b\n")
	write(t, d, "c.txt", "c\n")
	b := snapshotAt(t, d)
	defer b.Close()
	write(t, d, "a.txt", "new\n")
	r := BuildRoot(context.Background(), RootSpec{Path: d, Before: b}, DefaultOptions())
	if len(r.Files) != 1 || r.Files[0].Path != "a.txt" || r.Files[0].Change != "added" {
		t.Fatalf("full scan control inconsistent: %v", r.Files)
	}
}

func TestGong78DownloadedInputAndBusinessImage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	inputs := t.TempDir()
	c := StartCapture(ctx, CaptureSpec{WorkDir: dir, InputDirectory: inputs, Options: DefaultOptions()})
	data := []byte{0, 1, 2, 3}
	path := filepath.Join(dir, "input.png")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := RecordInput(inputs, path, data); err != nil {
		t.Fatal(err)
	}
	if diff := c.Finish(ctx); len(diff.Files) != 0 {
		t.Fatalf("input download reported as edit: %+v", diff.Files)
	}
	c = StartCapture(ctx, CaptureSpec{WorkDir: dir, InputDirectory: t.TempDir(), Options: DefaultOptions()})
	os.WriteFile(path, []byte{0, 9, 8, 7}, 0644)
	if diff := c.Finish(ctx); len(diff.Files) != 1 {
		t.Fatalf("business image edit missing: %+v", diff.Files)
	}
}
func TestGong78ExternalDirectoryChanges(t *testing.T) {
	ctx := context.Background()
	external := repo(t)
	dir := t.TempDir()
	c := StartCapture(ctx, CaptureSpec{WorkDir: dir, Repos: []string{external}, Options: DefaultOptions()})
	write(t, external, "tracked.txt", "external edit\n")
	diff := c.Finish(ctx)
	found := false
	for _, root := range diff.Roots {
		if root.Path == external && len(root.Files) == 1 && strings.Contains(root.Files[0].Patch, "external edit") {
			found = true
		}
	}
	if !found {
		t.Fatalf("external edit missing: %+v", diff.Roots)
	}
}

func TestGong78ExternalSkillDirectory(t *testing.T) {
	ctx := context.Background()
	external := t.TempDir()
	write(t, external, "SKILL.md", "original skill\n")
	c := StartCapture(ctx, CaptureSpec{WorkDir: t.TempDir(), Repos: []string{external}, Options: DefaultOptions()})
	write(t, external, "SKILL.md", "updated skill\n")
	diff := c.Finish(ctx)
	found := false
	for _, root := range diff.Roots {
		if root.Path == external && len(root.Files) == 1 && strings.Contains(root.Files[0].Patch, "updated skill") {
			found = true
		}
	}
	if !found {
		t.Fatalf("non-Git skill edit missing: %+v", diff.Roots)
	}
}

func TestGong78LargeFileModeChange(t *testing.T) {
	before := &fileMeta{Mode: 0100644, Size: 100, SizeOnly: true}
	after := &fileMeta{Mode: 0100755, Size: 100, SizeOnly: true}
	if classify(before, nil, after) != "modified" {
		t.Fatal("size-only capture lost mode change")
	}
}
func TestGong78CloneModeRespectsGitExecutableSemantics(t *testing.T) {
	baseline := &baselineFile{Mode: 0100644, Sum: "same"}
	after := &fileMeta{Mode: 0100664, Sum: "same"}
	if classify(nil, baseline, after) != "" {
		t.Fatal("clone umask reported as run edit")
	}
	after.Mode = 0100755
	if classify(nil, baseline, after) != "modified" {
		t.Fatal("executable mode change absent")
	}
}
