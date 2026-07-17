package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSkillFilesIgnoresBundledSkillMd(t *testing.T) {
	t.Parallel()
	skillsDir := filepath.Join(t.TempDir(), ".claude", "skills")

	skills := []SkillContextForEnv{
		{
			Name:    "Issue Review",
			Content: "Primary skill body.",
			Files: []SkillFileContextForEnv{
				{Path: "README.md", Content: "readme"},
				{Path: "SKILL.md", Content: "duplicate primary, must be skipped"},
				{Path: "./SKILL.md", Content: "non-canonical duplicate, must be skipped"},
				{Path: "helper.go", Content: "package main"},
			},
		},
	}

	manifest := &sidecarManifest{}
	if err := writeSkillFiles(skillsDir, skills, manifest); err != nil {
		t.Fatalf("writeSkillFiles errored on a bundled SKILL.md: %v", err)
	}

	skillDir := filepath.Join(skillsDir, "issue-review")

	got, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(got), "Primary skill body.") {
		t.Errorf("SKILL.md = %q, want primary content", string(got))
	}
	if strings.Contains(string(got), "must be skipped") {
		t.Error("SKILL.md was overwritten by a bundled duplicate")
	}

	for _, name := range []string{"README.md", "helper.go"} {
		if _, err := os.Stat(filepath.Join(skillDir, name)); err != nil {
			t.Errorf("expected supporting file %s to be written: %v", name, err)
		}
	}
}
