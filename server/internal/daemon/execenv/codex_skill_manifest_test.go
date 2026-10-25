package execenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestCodexSkillManifestRecordsInvalidMetadataWithoutBlockingHydration(t *testing.T) {
	for _, body := range []string{
		"---\nname: sop-eval\ndescription: [TODO: complete this skill]\n---\n# Skill",
		"---\nname: [invalid, name]\ndescription: text\n---\n# Skill",
		"---\nname: broken\ndescription: [unterminated\n---\n# Skill",
	} {
		t.Run(body, func(t *testing.T) {
			shared := t.TempDir()
			t.Setenv("CODEX_HOME", shared)
			writeUserSkill(t, filepath.Join(shared, "skills", "invalid"), body)
			writeUserSkill(t, filepath.Join(shared, "skills", "valid"), "---\nname: valid\ndescription: readable skill\n---\n# Valid")
			home := t.TempDir()
			if err := hydrateCodexSkills(home, nil, nil, testLogger()); err != nil {
				t.Fatalf("user metadata blocked task: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(home, "skills-manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var entries []codexSkillManifestEntry
			if err := json.Unmarshal(data, &entries); err != nil {
				t.Fatal(err)
			}
			malformed := strings.Contains(body, "[unterminated")
			want := 2
			if malformed {
				want = 1
			}
			if len(entries) != want {
				t.Fatalf("entries=%d want=%d", len(entries), want)
			}
			valid := entries[len(entries)-1]
			if valid.Name != "valid" || valid.Description != "readable skill" || valid.MetadataError != "" {
				t.Fatalf("valid skill damaged: %+v", valid)
			}
			if !malformed && (entries[0].MetadataError != "" || entries[0].SHA256 == "" || entries[0].Description == "") {
				t.Fatalf("safe normalization did not produce valid metadata: %+v", entries[0])
			}
			original, err := os.ReadFile(filepath.Join(shared, "skills", "invalid", "SKILL.md"))
			if err != nil || string(original) != body {
				t.Fatal("source skill was modified")
			}

		})
	}
}
