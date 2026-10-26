package agent

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodebuddySupervisionConfigKeepsModelsAndIsolatesStartup(t *testing.T) {
	source, root := t.TempDir(), t.TempDir()
	settings := `{"model":"human-default","variantModels":{"lite":"human-lite"},"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"human-hook"}]}]},"enabledPlugins":{"human-plugin":true},"permissions":{"allow":["Bash"]}}`
	models := `{"models":[{"id":"human-default","apiKey":"owned-test-key","url":"http://localhost/owned"}]}`
	for name, raw := range map[string]string{"settings.json": settings, "models.json": models} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{"CODEBUDDY_CONFIG_DIR": source, "HOME": t.TempDir()}
	directory, err := PrepareCodebuddySupervisionConfig(root, env)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"human-default", "human-lite"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("model choice lost: %s", want)
		}
	}
	for _, forbidden := range []string{"human-hook", "human-plugin", "Bash"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("supervision inherited startup or tool settings: %s", forbidden)
		}
	}
	raw, _ = os.ReadFile(filepath.Join(directory, "models.json"))
	if string(raw) != models || env["CODEBUDDY_CONFIG_DIR"] != source {
		t.Fatal("native model definition or caller environment was changed")
	}
	raw, _ = os.ReadFile(filepath.Join(source, "settings.json"))
	if string(raw) != settings {
		t.Fatal("human settings were modified")
	}
	info, _ := os.Stat(directory)
	if info.Mode().Perm() != 0700 {
		t.Fatal("role configuration is not private")
	}
}

func TestCodebuddySupervisionToolsCannotBeWidenedByCustomArgs(t *testing.T) {
	opts := ExecOptions{MCPToolsOnly: true, ApprovalPolicy: "never", Model: "explicit-model", ThinkingLevel: "high", SystemPrompt: "bound-supervision-skill", CustomArgs: []string{"--tools", "default", "--plugin-dir", "/human-plugin", "--settings", `{"hooks":{}}`, "-y"}}
	args := buildCodebuddyArgs(opts, slog.Default())
	joined := strings.Join(args, " ")
	for _, forbidden := range []string{"default", "/human-plugin", `{"hooks":{}}`, " -y"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("role tools widened by custom args: %s", forbidden)
		}
	}
	for _, want := range []string{"explicit-model", "high", "bound-supervision-skill", "--strict-mcp-config", `{"disableAllHooks":true}`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("supervision lost execution contract: %s", want)
		}
	}
	for i, arg := range args {
		if arg == "--tools" && (i+1 == len(args) || args[i+1] != "") {
			t.Fatal("supervision exposed builtin tools")
		}
	}
	if _, err := (&codebuddyBackend{}).Execute(t.Context(), "", ExecOptions{MCPToolsOnly: true, NativeGoal: true}); err == nil {
		t.Fatal("MCP-only supervision accepted native Goal execution")
	}
}
