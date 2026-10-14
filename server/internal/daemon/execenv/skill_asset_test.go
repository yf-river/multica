package execenv

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSkillAssetsMaterializeOriginalBytesAndMode(t *testing.T) {
	root := t.TempDir()
	data := []byte("\x89PNG\x00\xff")
	skills := []SkillContextForEnv{{Name: "design", Content: "# Design", Files: []SkillFileContextForEnv{{Path: "images/reference.png", Data: data, Mode: 0644}, {Path: "scripts/run.sh", Content: "#!/bin/sh\nprintf ready", Mode: 0755}, {Path: "nested/SKILL.md", Content: "# Child", Mode: 0644}}}}
	if err := writeSkillFiles(root, skills, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "design/images/reference.png"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("image mismatch: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "design/scripts/run.sh"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("executable mode missing: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "design/nested/SKILL.md")); err != nil {
		t.Fatal(err)
	}
}
