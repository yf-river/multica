package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeProvenanceKeepsInstructionsOutOfDiagnostic(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "workdir")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteRuntimeProvenance(root, work, "codex", "private workspace instructions", TaskContextForEnv{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "runtime-instructions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private workspace instructions") {
		t.Fatal("diagnostic leaked instruction body")
	}
	entries, err := os.ReadDir(work)
	if err != nil || len(entries) != 0 {
		t.Fatalf("diagnostic changed user checkout: %v, %v", entries, err)
	}
}
