package execenv

import (
	"os"
	"testing"
)

// 1. Evidence resolves existing owner records but cannot allocate roots or follow a changed task owner.
func TestExistingRootEvidenceOwnership(t *testing.T) {
	params := RootDirParams{WorkspacesRoot: t.TempDir(), WorkspaceID: "workspace-owned", TaskID: "task-owned"}
	if _, err := ExistingRootDir(params); err == nil {
		t.Fatal("missing root accepted")
	}
	root, err := ResolveRootDir(params)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err = writeEnvRootOwner(root, params.WorkspaceID, params.TaskID); err != nil {
		t.Fatal(err)
	}
	if actual, err := ExistingRootDir(params); err != nil || actual != root {
		t.Fatalf("owned root %s %v", actual, err)
	}
	if err = writeEnvRootOwner(root, params.WorkspaceID, "different-task"); err != nil {
		t.Fatal(err)
	}
	if _, err = ExistingRootDir(params); err == nil {
		t.Fatal("changed task owner accepted")
	}
}
