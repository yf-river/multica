package execenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexSkillManifestReflectsChangedSkills(t *testing.T) {
	shared := t.TempDir()
	t.Setenv("CODEX_HOME", shared)
	writeUserSkill(t, filepath.Join(shared, "skills", "skill-creator"), "creator-v1")
	home := t.TempDir()
	hydrate := func() []codexSkillManifestEntry {
		t.Helper()
		if err := hydrateCodexSkills(home, nil, nil, testLogger()); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(home, "skills-manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var entries []codexSkillManifestEntry
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatal(err)
		}
		return entries
	}
	first := hydrate()
	if len(first) != 1 {
		t.Fatalf("expected installed skill, got %v", first)
	}
	if _, err := os.ReadFile(first[0].Path); err != nil {
		t.Fatal(err)
	}
	writeUserSkill(t, filepath.Join(shared, "skills", "skill-creator"), "creator-v2")
	second := hydrate()
	if second[0].SHA256 == first[0].SHA256 {
		t.Fatal("changed skill has stale fingerprint")
	}
	if err := os.RemoveAll(filepath.Join(shared, "skills", "skill-creator")); err != nil {
		t.Fatal(err)
	}
	if len(hydrate()) != 0 {
		t.Fatal("removed skill remains advertised")
	}
}
