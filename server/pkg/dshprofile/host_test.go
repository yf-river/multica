package dshprofile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDshHostSnapshotRunsWithoutOriginalInstallation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "original")
	if err := os.MkdirAll(filepath.Join(source, "lib"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"package.json": `{"name":"@deepseek-ai/dsh","version":"0.2.1-rc.3"}`, "lib/bin.js": "PINNED_DSH_HOST_OK\n"}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	node := filepath.Join(root, "original-node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\ncat \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	host := Host{Kind: "node-package", Node: node, Entrypoint: filepath.Join(source, "lib/bin.js"), PackageRoot: source, Version: "0.2.1-rc.3"}
	launcher, err := SnapshotHost(context.Background(), filepath.Join(root, "snapshots"), host)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(node); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), source) || strings.Contains(string(data), node) {
		t.Fatal("snapshot launcher references original installation")
	}
	if runtime.GOOS != "windows" {
		output, err := exec.Command(launcher).Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(output) != "PINNED_DSH_HOST_OK\n" {
			t.Fatalf("wrong host output: %q", output)
		}
	}
}

func TestDshHostSnapshotRejectsExternalDependencyLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permission differs; regular snapshot coverage runs on Windows")
	}
	root := t.TempDir()
	source := filepath.Join(root, "package")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"package.json": `{"name":"@deepseek-ai/dsh","version":"0.2.1-rc.3"}`, "bin.js": "code"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	node := filepath.Join(root, "node")
	if err := os.WriteFile(node, []byte("fake node"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(node, filepath.Join(source, "outside")); err != nil {
		t.Fatal(err)
	}
	_, err := SnapshotHost(context.Background(), filepath.Join(root, "snapshots"), Host{Kind: "node-package", Node: node, Entrypoint: filepath.Join(source, "bin.js"), PackageRoot: source, Version: "0.2.1-rc.3"})
	if err == nil || !strings.Contains(err.Error(), "leaves installation") {
		t.Fatalf("unsafe external dependency link accepted: %v", err)
	}
}
