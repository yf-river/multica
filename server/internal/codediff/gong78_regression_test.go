package codediff

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditRound2QuotedJSONSecretRedacted(t *testing.T) {
	synthetic := "AUDIT_SYNTHETIC_NOT_A_REAL_CREDENTIAL_0123456789"
	line := `{"token":"` + synthetic + `"}`
	if _, hit := RedactLine(line); !hit {
		t.Error("quoted JSON token key was not detected")
	}
	dir := t.TempDir()
	before, err := TakeSnapshot(context.Background(), dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	if err = os.WriteFile(filepath.Join(dir, "config.json"), []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	root := BuildRoot(context.Background(), RootSpec{Path: dir, Before: before}, DefaultOptions())
	for _, f := range root.Files {
		if strings.Contains(f.Patch, synthetic) {
			t.Errorf("synthetic credential remained in persisted patch for %s", f.Path)
		}
	}
}
func TestAuditRound2ExecutableModeChangeVisible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.sh")
	os.WriteFile(path, []byte("#!/bin/sh\necho audit\n"), 0644)
	before, err := TakeSnapshot(context.Background(), dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	os.Chmod(path, 0755)
	root := BuildRoot(context.Background(), RootSpec{Path: dir, Before: before}, DefaultOptions())
	if len(root.Files) == 0 {
		t.Fatal("chmod 0644 -> 0755 disappeared from run diff")
	}
}
func TestAuditRound2SymlinkChangeVisible(t *testing.T) {
	dir := t.TempDir()
	before, err := TakeSnapshot(context.Background(), dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	if err = os.Symlink("release-v2", filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	root := BuildRoot(context.Background(), RootSpec{Path: dir, Before: before}, DefaultOptions())
	if len(root.Files) == 0 {
		t.Fatal("new symlink disappeared without skipped/truncated indication")
	}
}
func TestAuditRound2CloneWithIdenticalFilesIsClean(t *testing.T) {
	source := repo(t)
	write(t, source, "same-a.txt", "identical\n")
	write(t, source, "same-b.txt", "identical\n")
	command(t, source, "add", ".")
	command(t, source, "commit", "-m", "fixture identical files")
	work := t.TempDir()
	before := snapshotAt(t, work)
	defer before.Close()
	command(t, work, "clone", source, "project")
	root := BuildRoot(context.Background(), RootSpec{Path: work, Before: before, Kind: "workdir"}, DefaultOptions())
	if len(root.Files) != 0 {
		t.Fatalf("untouched clone reported as changed: %v", root.Files)
	}
}
func TestAuditRound2DeletionInNewNestedCloneVisible(t *testing.T) {
	source := repo(t)
	work := t.TempDir()
	before := snapshotAt(t, work)
	defer before.Close()
	command(t, work, "clone", source, "project")
	os.Remove(filepath.Join(work, "project", "tracked.txt"))
	root := BuildRoot(context.Background(), RootSpec{Path: work, Before: before, Kind: "workdir"}, DefaultOptions())
	f, ok := filesByPath(root)["project/tracked.txt"]
	if !ok || f.Change != "deleted" {
		t.Fatalf("deleted tracked file missing: %v", root.Files)
	}
}
func TestAuditRound2PartialScanDoesNotInventDeletion(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "b.txt", "b\n")
	write(t, dir, "c.txt", "c\n")
	opts := DefaultOptions()
	opts.MaxFiles = 2
	before, err := TakeSnapshot(context.Background(), dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	write(t, dir, "a.txt", "new\n")
	root := BuildRoot(context.Background(), RootSpec{Path: dir, Before: before}, opts)
	if _, err = os.Stat(filepath.Join(dir, "c.txt")); err != nil {
		t.Fatal(err)
	}
	for _, f := range root.Files {
		if f.Path == "c.txt" && f.Change == "deleted" {
			t.Fatal("scan cap reported existing c.txt as deleted")
		}
	}
}
