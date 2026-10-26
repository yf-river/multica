package agent

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexSupervisionPreservesModelRouteAndBlocksToolOverrides(t *testing.T) {
	opts := ExecOptions{MCPToolsOnly: true, CustomArgs: []string{"--enable", "shell_tool", "-c", "features.shell_tool=true", "--config", "profiles.work.features.apps=true", "--config", `model_provider="private"`, "-c", `model_providers.private.base_url="http://localhost/owned"`}}
	args := strings.Join(buildCodexArgs(opts, slog.Default()), " ")
	for _, forbidden := range []string{"--enable", "features.shell_tool=true", "profiles.work.features.apps=true"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("supervision tool override survived: %s", forbidden)
		}
	}
	for _, retained := range []string{`model_provider="private"`, `model_providers.private.base_url="http://localhost/owned"`, `features.shell_tool=false`, `features.code_mode.direct_only_tool_namespaces=["mcp__supervision"]`} {
		if !strings.Contains(args, retained) {
			t.Fatalf("supervision lost model route or tool policy: %s", retained)
		}
	}
}

func TestCodexSupervisionProfilesKeepNativeModelSettings(t *testing.T) {
	home := t.TempDir()
	original := "model_provider = 'private'\n[profiles.'work.quoted']\nmodel = 'gpt-6.1-sol'\n[profiles.'work.quoted'.features]\nshell_tool = true\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	args, err := codexSupervisionProfileArgs(home)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, wanted := range []string{`profiles."work.quoted".features.shell_tool=false`, `profiles."work.quoted".features.multi_agent_v2.enabled=false`} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("profile escaped native tool restriction: %s", wanted)
		}
	}
	if strings.Contains(joined, "model_provider=") || strings.Contains(joined, ".model=") {
		t.Fatal("tool policy replaced a model route")
	}
	raw, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if string(raw) != original {
		t.Fatal("profile source was modified")
	}
}

func TestCodexSupervisionRequiresVerifiedFreshDedicatedRuntime(t *testing.T) {
	for _, unverified := range []string{"0.164.0-alpha.1", "0.164.0+custom", "0.164.1", "0.165.0"} {
		if codexSupervisionVersionSupported(unverified) {
			t.Fatalf("unverified release was accepted: %s", unverified)
		}
	}
	cfg := Config{BuiltinRuntime: true, CodexVersion: "0.164.0"}
	opts := ExecOptions{MCPToolsOnly: true, McpConfig: json.RawMessage(`{"mcpServers":{"supervision":{"command":"owned-mcp"}}}`)}
	if err := validateCodexSupervision(cfg, opts); err != nil {
		t.Fatal(err)
	}
	for _, other := range []Config{{BuiltinRuntime: true, CodexVersion: "0.163.0"}, {BuiltinRuntime: true, CodexVersion: "0.165.0"}, {CodexVersion: "0.164.0"}, {BuiltinRuntime: true, CodexVersion: "0.164.0", CodexPool: &CodexProcessPool{}}, {BuiltinRuntime: true, CodexVersion: "0.164.0", LaunchPrefix: []string{"unverified-prefix"}}} {
		if validateCodexSupervision(other, opts) == nil {
			t.Fatal("unverified execution accepted supervisor permissions")
		}
	}
	for _, change := range []func(*ExecOptions){
		func(o *ExecOptions) { o.ResumeSessionID = "old-thread" }, func(o *ExecOptions) { o.NativeGoal = true },
		func(o *ExecOptions) { o.McpConfig = json.RawMessage(`{"mcpServers":{"unrelated":{}}}`) },
		func(o *ExecOptions) {
			o.McpConfig = json.RawMessage(`{"mcpServers":{"supervision":{},"unrelated":{}}}`)
		},
		func(o *ExecOptions) { o.CustomArgs = []string{"-c", `profiles={work={model="other"}}`} },
		func(o *ExecOptions) { o.CustomArgs = []string{"--config=profiles.work={model=\"other\"}"} },
	} {
		other := opts
		change(&other)
		if validateCodexSupervision(cfg, other) == nil {
			t.Fatal("supervision accepted wider or persistent native execution")
		}
	}
}

func TestCodexSupervisionThreadConfigCannotRestoreShell(t *testing.T) {
	c := &codexClient{cfg: Config{CodexVersion: "0.164.0"}, mcpToolsOnly: true, threadConfig: map[string]any{"features.shell_tool": true, "features": map[string]any{"shell_tool": true}}}
	config := c.currentThreadConfig()
	if _, exists := config["features.shell_tool"]; exists {
		t.Fatal("conflicting dotted key survived")
	}
	if config["features"].(map[string]any)["shell_tool"] != false {
		t.Fatal("thread restored shell capability")
	}
}
