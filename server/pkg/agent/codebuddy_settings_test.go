package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCodebuddyHookSettingsRetainHumanRulesAndTaskPolicy(t *testing.T) {
	dir := t.TempDir()
	human := `{"env":{"HUMAN":"yes"},"permissions":{"deny":["Bash(rm *)"]},"skillOverrides":{"private":"on","human":"on"},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"human-hook"}]}]}}`
	task := `{"permissions":{"deny":["Skill(private)"]},"skillOverrides":{"private":"off"}}`
	for name, content := range map[string]string{"human.json": human, "task.json": task} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	opts := ExecOptions{Cwd: dir, ExtraArgs: []string{"--settings", "missing-earlier.json"}, CustomArgs: []string{"--settings=human.json"}, CodebuddySettingsPath: filepath.Join(dir, "task.json")}
	hooks := testCodebuddyHooks(t, nil, opts)
	raw, err := os.ReadFile(hooks.settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"human-hook", "Bash(rm *)", "Skill(private)", `"private":"off"`, `"human":"on"`, `"HUMAN":"yes"`, CodebuddyHookHelperArg} {
		if !bytes.Contains(raw, []byte(expected)) {
			t.Fatalf("merged settings lost %s", expected)
		}
	}
	for name, original := range map[string]string{"human.json": human, "task.json": task} {
		raw, _ := os.ReadFile(filepath.Join(dir, name))
		if string(raw) != original {
			t.Fatalf("settings source %s was mutated", name)
		}
	}
}

func TestCodebuddyHookSettingsInlineAndInvalidSource(t *testing.T) {
	settings, err := codebuddyHookSettings(ExecOptions{CustomArgs: []string{"--settings", `{"mcpServers":{"human":{"command":"owned-probe"}}}`}})
	if err != nil || !codebuddySettingsHaveMCP(settings) {
		t.Fatalf("inline MCP settings were not preserved: %v", err)
	}
	for _, args := range [][]string{{"--settings"}, {"--settings="}, {"--settings", "--debug"}, {"--settings", "missing.json"}, {"--settings", "{invalid}"}, {"--settings", "null"}} {
		if _, err := codebuddyHookSettings(ExecOptions{Cwd: t.TempDir(), CustomArgs: args}); err == nil {
			t.Fatalf("invalid settings were silently dropped: %q", args)
		}
	}
}

func TestCodebuddyCustomSettingsContentInvalidatesResident(t *testing.T) {
	cfg := codebuddyFixtureConfig(t)
	dir := t.TempDir()
	name := filepath.Join(dir, "settings.json")
	opts := ExecOptions{Cwd: dir, CustomArgs: []string{"--settings", "settings.json"}}
	write := func(value string) string {
		t.Helper()
		if err := os.WriteFile(name, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		fingerprint, err := codebuddyResidentFingerprint(cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		return fingerprint
	}
	first := write(`{"env":{"HUMAN":"first"}}`)
	if write("{\n  \"env\": {\"HUMAN\": \"first\"}\n}") != first {
		t.Fatal("formatting changed the effective settings identity")
	}
	if write(`{"env":{"HUMAN":"second"}}`) == first {
		t.Fatal("changed settings content retained a stale process")
	}
}

func TestCodebuddyNativeModelsInvalidateResident(t *testing.T) {
	for _, scope := range []string{"account", "project"} {
		t.Run(scope, func(t *testing.T) {
			cfg := codebuddyFixtureConfig(t)
			opts := ExecOptions{Cwd: t.TempDir()}
			dir := cfg.Env["CODEBUDDY_CONFIG_DIR"]
			if scope == "project" {
				dir = filepath.Join(opts.Cwd, ".codebuddy")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := codebuddyResidentFingerprint(cfg, opts)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"models":[{"id":"custom","url":"http://localhost/owned"}]}`), 0600); err != nil {
				t.Fatal(err)
			}
			after, err := codebuddyResidentFingerprint(cfg, opts)
			if err != nil || before == after {
				t.Fatalf("native model source did not invalidate resident: %v", err)
			}
		})
	}
}
