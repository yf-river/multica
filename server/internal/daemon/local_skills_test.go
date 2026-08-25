package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func writeTestLocalSkill(t *testing.T, root, rel string, files map[string]string) string {
	t.Helper()

	skillDir := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	for path, content := range files {
		fullPath := filepath.Join(skillDir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("mkdir parents for %s: %v", path, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return skillDir
}

func runtimeSkillKeys(skills []protocol.RuntimeLocalSkillSummary) []string {
	keys := make([]string, 0, len(skills))
	for _, skill := range skills {
		keys = append(keys, skill.Key)
	}
	return keys
}

func TestListRuntimeLocalSkills_Claude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTestLocalSkill(t, filepath.Join(home, ".claude", "skills"), "review-helper", map[string]string{
		"SKILL.md":           "---\nname: Review Helper\ndescription: Review pull requests\n---\n# Review Helper\n",
		"templates/check.md": "checklist",
		"LICENSE":            "ignored",
		".secret":            "ignored",
	})

	skills, supported, err := listRuntimeLocalSkills("claude")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	if !supported {
		t.Fatal("claude should be supported")
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}

	skill := skills[0]
	if skill.Key != "review-helper" {
		t.Fatalf("key = %q, want review-helper", skill.Key)
	}
	if skill.Name != "Review Helper" {
		t.Fatalf("name = %q, want Review Helper", skill.Name)
	}
	if skill.Description != "Review pull requests" {
		t.Fatalf("description = %q", skill.Description)
	}
	if skill.FileCount != 2 {
		t.Fatalf("file_count = %d, want 2", skill.FileCount)
	}
	if skill.SourcePath != "~/.claude/skills/review-helper" {
		t.Fatalf("source_path = %q", skill.SourcePath)
	}
}

func TestListRuntimeLocalSkills_Kiro(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTestLocalSkill(t, filepath.Join(home, ".kiro", "skills"), "review-helper", map[string]string{
		"SKILL.md": "---\nname: Kiro Review\ndescription: Review code with Kiro\n---\n# Kiro Review\n",
	})

	skills, supported, err := listRuntimeLocalSkills("kiro")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	if !supported {
		t.Fatal("kiro should be supported")
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Key != "review-helper" {
		t.Fatalf("key = %q, want review-helper", skills[0].Key)
	}
	if skills[0].Name != "Kiro Review" {
		t.Fatalf("name = %q, want Kiro Review", skills[0].Name)
	}
	if skills[0].SourcePath != "~/.kiro/skills/review-helper" {
		t.Fatalf("source_path = %q", skills[0].SourcePath)
	}
}

func TestListRuntimeLocalSkills_FollowsSymlinkedSkillDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	target := writeTestLocalSkill(t, filepath.Join(home, ".agents", "skills"), "lark-doc", map[string]string{
		"SKILL.md":  "---\nname: Lark Doc\ndescription: Drive lark docs\n---\n# Lark Doc\n",
		"helper.md": "stub",
	})

	skillsRoot := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		t.Fatalf("mkdir skills root: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(skillsRoot, "lark-doc")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	writeTestLocalSkill(t, skillsRoot, "review-helper", map[string]string{
		"SKILL.md": "---\nname: Review Helper\n---\n",
	})

	skills, supported, err := listRuntimeLocalSkills("claude")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	if !supported {
		t.Fatal("claude should be supported")
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d (%v)", len(skills), skills)
	}

	bySymlinkKey := skills[0]
	if bySymlinkKey.Key != "lark-doc" {
		bySymlinkKey = skills[1]
	}
	if bySymlinkKey.Key != "lark-doc" {
		t.Fatalf("symlinked skill missing from result: %v", skills)
	}
	if bySymlinkKey.Name != "Lark Doc" {
		t.Fatalf("symlinked skill name = %q, want Lark Doc", bySymlinkKey.Name)
	}
	if bySymlinkKey.SourcePath != "~/.claude/skills/lark-doc" {
		t.Fatalf("symlinked skill source_path = %q", bySymlinkKey.SourcePath)
	}
}

func TestListRuntimeLocalSkills_CodexUsesSharedCODEXHOME(t *testing.T) {
	home := t.TempDir()
	codexHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)

	writeTestLocalSkill(t, filepath.Join(codexHome, "skills"), "debugger", map[string]string{
		"SKILL.md": "# Debugger\n",
	})
	writeTestLocalSkill(t, filepath.Join(home, ".codex", "skills"), "wrong-home", map[string]string{
		"SKILL.md": "# Wrong Home\n",
	})

	skills, supported, err := listRuntimeLocalSkills("codex")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	if !supported {
		t.Fatal("codex should be supported")
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Key != "debugger" {
		t.Fatalf("key = %q, want debugger", skills[0].Key)
	}
	if skills[0].SourcePath != filepath.Join(codexHome, "skills", "debugger") {
		t.Fatalf("source_path = %q", skills[0].SourcePath)
	}
}

// opencode (and possibly future providers) lay skills out one level deep,
// e.g. ~/.config/opencode/skills/release/reporter/SKILL.md.
// loadRuntimeLocalSkillBundle already accepts that nested key, so the list
// endpoint must surface those skills too — otherwise the import dialog
// hides skills the load endpoint can fetch and users can't pick them.
//
// The walker also has to short-circuit at the outermost SKILL.md it finds:
// nested SKILL.md files inside an already-registered skill (e.g. inside
// `top/SKILL.md`'s own template tree) are part of the parent skill's
// bundle, not separate skills.
func TestListRuntimeLocalSkills_DescendsIntoNestedSkillDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	root := filepath.Join(home, ".config", "opencode", "skills")

	// Top-level skill — should register at key="top" and its child SKILL.md
	// must NOT register as a separate skill.
	writeTestLocalSkill(t, root, "top", map[string]string{
		"SKILL.md":           "---\nname: Top\n---\n",
		"templates/SKILL.md": "not a real skill — sub-template that happens to share the filename",
	})

	// Nested skill — only valid SKILL.md is at depth 2.
	writeTestLocalSkill(t, root, "release/reporter", map[string]string{
		"SKILL.md": "---\nname: Release Reporter\n---\n",
	})

	skills, supported, err := listRuntimeLocalSkills("opencode")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	if !supported {
		t.Fatal("opencode should be supported")
	}

	keys := make([]string, 0, len(skills))
	for _, s := range skills {
		keys = append(keys, s.Key)
	}
	// Two registered skills, "top" and "release/reporter" — and crucially
	// NOT "top/templates" (the inner SKILL.md must be ignored once the
	// parent qualified).
	wantKeys := []string{"release/reporter", "top"}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("keys = %v, want %v", keys, wantKeys)
	}
}

func TestLoadRuntimeLocalSkillBundle_OpenCode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTestLocalSkill(t, filepath.Join(home, ".config", "opencode", "skills"), "release/reporter", map[string]string{
		"SKILL.md":           "---\nname: Release Reporter\ndescription: Summarize release notes\n---\n# Release Reporter\n",
		"docs/template.md":   "template body",
		"examples/sample.md": "sample body",
	})

	bundle, supported, err := loadRuntimeLocalSkillBundle("opencode", "release/reporter")
	if err != nil {
		t.Fatalf("loadRuntimeLocalSkillBundle: %v", err)
	}
	if !supported {
		t.Fatal("opencode should be supported")
	}
	if bundle.Name != "Release Reporter" {
		t.Fatalf("name = %q", bundle.Name)
	}
	if bundle.Description != "Summarize release notes" {
		t.Fatalf("description = %q", bundle.Description)
	}
	if len(bundle.Files) != 2 {
		t.Fatalf("expected 2 supporting files, got %d", len(bundle.Files))
	}
	if bundle.Files[0].Path != "docs/template.md" || bundle.Files[0].Content != "template body" {
		t.Fatalf("unexpected first file: %+v", bundle.Files[0])
	}
	if bundle.Files[1].Path != "examples/sample.md" || bundle.Files[1].Content != "sample body" {
		t.Fatalf("unexpected second file: %+v", bundle.Files[1])
	}
	if bundle.SourcePath != "~/.config/opencode/skills/release/reporter" {
		t.Fatalf("source_path = %q", bundle.SourcePath)
	}
}

func TestLoadRuntimeLocalSkillBundle_Cursor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTestLocalSkill(t, filepath.Join(home, ".cursor", "skills"), "docs-helper", map[string]string{
		"SKILL.md":         "---\nname: Docs Helper\n---\n# Docs Helper\n",
		"notes/tips.md":    "tips",
		"examples/a.txt":   "example",
		".hidden/skip.txt": "ignore",
	})

	bundle, supported, err := loadRuntimeLocalSkillBundle("cursor", "docs-helper")
	if err != nil {
		t.Fatalf("loadRuntimeLocalSkillBundle: %v", err)
	}
	if !supported {
		t.Fatal("cursor should be supported")
	}
	if bundle.Name != "Docs Helper" {
		t.Fatalf("name = %q", bundle.Name)
	}
	if len(bundle.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(bundle.Files))
	}
	if bundle.SourcePath != "~/.cursor/skills/docs-helper" {
		t.Fatalf("source_path = %q", bundle.SourcePath)
	}
}

func TestListRuntimeLocalSkills_DiscoversUniversalAgentsRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTestLocalSkill(t, filepath.Join(home, ".agents", "skills"), "universal-helper", map[string]string{
		"SKILL.md":     "---\nname: Universal Helper\ndescription: Cross-tool skill\n---\n# Universal Helper\n",
		"docs/info.md": "info",
	})

	skills, supported, err := listRuntimeLocalSkills("claude")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	if !supported {
		t.Fatal("claude should be supported")
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d (%v)", len(skills), skills)
	}
	if skills[0].Key != "universal-helper" {
		t.Fatalf("key = %q, want universal-helper", skills[0].Key)
	}
	if skills[0].Name != "Universal Helper" {
		t.Fatalf("name = %q, want Universal Helper", skills[0].Name)
	}
	if skills[0].SourcePath != "~/.agents/skills/universal-helper" {
		t.Fatalf("source_path = %q", skills[0].SourcePath)
	}
	if skills[0].FileCount != 2 {
		t.Fatalf("file_count = %d, want 2", skills[0].FileCount)
	}
}

func TestLoadRuntimeLocalSkillBundle_ImportsFromUniversalRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTestLocalSkill(t, filepath.Join(home, ".agents", "skills"), "shared-skill", map[string]string{
		"SKILL.md":        "---\nname: Shared Skill\ndescription: Imported from agents root\n---\n# Shared Skill\n",
		"examples/use.md": "usage",
		"scripts/run.sh":  "echo hi",
	})

	bundle, supported, err := loadRuntimeLocalSkillBundle("claude", "shared-skill")
	if err != nil {
		t.Fatalf("loadRuntimeLocalSkillBundle: %v", err)
	}
	if !supported {
		t.Fatal("claude should be supported")
	}
	if bundle.Name != "Shared Skill" {
		t.Fatalf("name = %q, want Shared Skill", bundle.Name)
	}
	if len(bundle.Files) != 2 {
		t.Fatalf("expected 2 supporting files, got %d", len(bundle.Files))
	}
	if bundle.SourcePath != "~/.agents/skills/shared-skill" {
		t.Fatalf("source_path = %q", bundle.SourcePath)
	}
}

func TestLocalSkills_ProviderRootWinsOnKeyConflict(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTestLocalSkill(t, filepath.Join(home, ".claude", "skills"), "dup", map[string]string{
		"SKILL.md": "---\nname: Provider Copy\n---\n# provider\n",
	})
	writeTestLocalSkill(t, filepath.Join(home, ".agents", "skills"), "dup", map[string]string{
		"SKILL.md": "---\nname: Universal Copy\n---\n# universal\n",
	})

	skills, _, err := listRuntimeLocalSkills("claude")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 deduped skill, got %d (%v)", len(skills), skills)
	}
	if skills[0].Name != "Provider Copy" {
		t.Fatalf("name = %q, want Provider Copy (provider root must win)", skills[0].Name)
	}
	if skills[0].SourcePath != "~/.claude/skills/dup" {
		t.Fatalf("source_path = %q, want provider path", skills[0].SourcePath)
	}

	bundle, _, err := loadRuntimeLocalSkillBundle("claude", "dup")
	if err != nil {
		t.Fatalf("loadRuntimeLocalSkillBundle: %v", err)
	}
	if bundle.Name != "Provider Copy" {
		t.Fatalf("bundle name = %q, want Provider Copy", bundle.Name)
	}
	if bundle.SourcePath != "~/.claude/skills/dup" {
		t.Fatalf("bundle source_path = %q, want provider path", bundle.SourcePath)
	}
}

func TestListRuntimeLocalSkills_MergesBothRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTestLocalSkill(t, filepath.Join(home, ".claude", "skills"), "provider-only", map[string]string{
		"SKILL.md": "---\nname: Provider Only\n---\n",
	})
	writeTestLocalSkill(t, filepath.Join(home, ".agents", "skills"), "universal-only", map[string]string{
		"SKILL.md": "---\nname: Universal Only\n---\n",
	})

	skills, _, err := listRuntimeLocalSkills("claude")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	keys := runtimeSkillKeys(skills)
	wantKeys := []string{"provider-only", "universal-only"}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("keys = %v, want %v", keys, wantKeys)
	}
}

func TestListRuntimeLocalSkills_MissingUniversalRootIsNotAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTestLocalSkill(t, filepath.Join(home, ".claude", "skills"), "only-provider", map[string]string{
		"SKILL.md": "---\nname: Only Provider\n---\n",
	})
	skills, supported, err := listRuntimeLocalSkills("claude")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	if !supported {
		t.Fatal("claude should be supported")
	}
	if len(skills) != 1 || skills[0].Key != "only-provider" {
		t.Fatalf("expected only-provider, got %v", skills)
	}
}

// Both roots missing → empty list, no error.
func TestListRuntimeLocalSkills_BothRootsMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	skills, supported, err := listRuntimeLocalSkills("claude")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	if !supported {
		t.Fatal("claude should be supported")
	}
	if len(skills) != 0 {
		t.Fatalf("expected empty, got %v", skills)
	}
}

// Nested layouts (a skill two levels deep) work in the universal root too.
func TestListRuntimeLocalSkills_NestedSkillInUniversalRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTestLocalSkill(t, filepath.Join(home, ".agents", "skills"), "release/reporter", map[string]string{
		"SKILL.md": "---\nname: Release Reporter\n---\n",
	})

	skills, _, err := listRuntimeLocalSkills("opencode")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	if len(skills) != 1 || skills[0].Key != "release/reporter" {
		t.Fatalf("expected release/reporter, got %v", skills)
	}
	bundle, _, err := loadRuntimeLocalSkillBundle("opencode", "release/reporter")
	if err != nil {
		t.Fatalf("loadRuntimeLocalSkillBundle: %v", err)
	}
	if bundle.Name != "Release Reporter" {
		t.Fatalf("bundle name = %q", bundle.Name)
	}
}

func TestLoadRuntimeLocalSkillBundle_FallsThroughToUniversalOnNotExist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTestLocalSkill(t, filepath.Join(home, ".claude", "skills"), "something-else", map[string]string{
		"SKILL.md": "---\nname: Something Else\n---\n",
	})
	writeTestLocalSkill(t, filepath.Join(home, ".agents", "skills"), "only-universal", map[string]string{
		"SKILL.md": "---\nname: Only Universal\n---\n# only universal\n",
	})

	bundle, _, err := loadRuntimeLocalSkillBundle("claude", "only-universal")
	if err != nil {
		t.Fatalf("loadRuntimeLocalSkillBundle: %v", err)
	}
	if bundle.Name != "Only Universal" {
		t.Fatalf("name = %q, want Only Universal", bundle.Name)
	}
	if bundle.SourcePath != "~/.agents/skills/only-universal" {
		t.Fatalf("source_path = %q", bundle.SourcePath)
	}
}

func TestLoadRuntimeLocalSkillBundle_DoesNotMaskReadErrorWithUniversalFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	clashDir := filepath.Join(home, ".claude", "skills", "clash")
	if err := os.MkdirAll(filepath.Join(clashDir, "SKILL.md"), 0o755); err != nil {
		t.Fatalf("mkdir SKILL.md-as-dir: %v", err)
	}
	writeTestLocalSkill(t, filepath.Join(home, ".agents", "skills"), "clash", map[string]string{
		"SKILL.md": "---\nname: Universal Clash\n---\n",
	})

	bundle, _, err := loadRuntimeLocalSkillBundle("claude", "clash")
	if err == nil {
		t.Fatalf("expected an error, got bundle %+v", bundle)
	}
	if bundle != nil {
		t.Fatalf("expected nil bundle on error, got %+v", bundle)
	}
}

func TestListRuntimeLocalSkills_PerRootVisitedAllowsCrossRootSymlinkAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	target := writeTestLocalSkill(t, filepath.Join(home, ".agents", "skills"), "foo", map[string]string{
		"SKILL.md": "---\nname: Foo\n---\n",
	})

	claudeRoot := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(claudeRoot, 0o755); err != nil {
		t.Fatalf("mkdir claude root: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(claudeRoot, "bar")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	skills, _, err := listRuntimeLocalSkills("claude")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	keys := runtimeSkillKeys(skills)
	wantKeys := []string{"bar", "foo"}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("keys = %v, want %v (per-root visited must not collapse the alias)", keys, wantKeys)
	}
}

func TestLoadRuntimeLocalSkillBundle_ProviderDirWithoutSkillMdFallsThrough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTestLocalSkill(t, filepath.Join(home, ".claude", "skills"), "shadowed", map[string]string{
		"notes.md": "not a skill — no SKILL.md here",
	})
	writeTestLocalSkill(t, filepath.Join(home, ".agents", "skills"), "shadowed", map[string]string{
		"SKILL.md":     "---\nname: Real Shadowed\ndescription: The valid one\n---\n# Real Shadowed\n",
		"docs/info.md": "info",
	})

	skills, _, err := listRuntimeLocalSkills("claude")
	if err != nil {
		t.Fatalf("listRuntimeLocalSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d (%v)", len(skills), skills)
	}
	if skills[0].Key != "shadowed" {
		t.Fatalf("list surfaced %+v, want key=shadowed", skills[0])
	}
	if skills[0].SourcePath != "~/.agents/skills/shadowed" {
		t.Fatalf("list source_path = %q, want ~/.agents/skills/shadowed", skills[0].SourcePath)
	}

	bundle, _, err := loadRuntimeLocalSkillBundle("claude", "shadowed")
	if err != nil {
		t.Fatalf("loadRuntimeLocalSkillBundle: %v (load disagreed with list)", err)
	}
	if bundle.Name != "Real Shadowed" {
		t.Fatalf("bundle name = %q, want Real Shadowed", bundle.Name)
	}
	if bundle.SourcePath != "~/.agents/skills/shadowed" {
		t.Fatalf("bundle source_path = %q, want ~/.agents/skills/shadowed", bundle.SourcePath)
	}
}

func TestLoadRuntimeLocalSkillBundle_ProviderNonDirFallsThrough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	claudeRoot := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(claudeRoot, 0o755); err != nil {
		t.Fatalf("mkdir claude root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeRoot, "filish"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	writeTestLocalSkill(t, filepath.Join(home, ".agents", "skills"), "filish", map[string]string{
		"SKILL.md": "---\nname: Filish\n---\n# Filish\n",
	})

	bundle, _, err := loadRuntimeLocalSkillBundle("claude", "filish")
	if err != nil {
		t.Fatalf("loadRuntimeLocalSkillBundle: %v", err)
	}
	if bundle.Name != "Filish" || bundle.SourcePath != "~/.agents/skills/filish" {
		t.Fatalf("bundle = %+v, want universal Filish", bundle)
	}
}
