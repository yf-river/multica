package execenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type openclawCLIStub struct {
	t         *testing.T
	bin       string
	responses map[string]openclawResponse
	calls     []openclawCall
}

type openclawCall struct {
	bin  string
	args []string
}

type openclawResponse struct {
	stdout string
	err    error
}

func installOpenclawStub(t *testing.T, responses map[string]openclawResponse) *openclawCLIStub {
	t.Helper()
	stub := &openclawCLIStub{
		t:         t,
		bin:       "/test/stub/openclaw",
		responses: responses,
	}
	prev := openclawExec
	openclawExec = stub.exec
	t.Cleanup(func() { openclawExec = prev })
	return stub
}

func (s *openclawCLIStub) exec(_ context.Context, bin string, args ...string) (string, error) {
	s.calls = append(s.calls, openclawCall{bin: bin, args: append([]string(nil), args...)})
	key := strings.Join(args, " ")
	resp, ok := s.responses[key]
	if !ok {
		return "", fmt.Errorf("openclawCLIStub: unexpected args %q", key)
	}
	return resp.stdout, resp.err
}

func mustReadJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read synthesized cfg: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse synthesized cfg: %v", err)
	}
	return got
}

func mustReadOpenclawWrapperMCPServers(t *testing.T, path string) (map[string]any, map[string]any) {
	t.Helper()

	got := mustReadJSON(t, path)
	mcp, ok := got["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("wrapper missing mcp block: %v", got)
	}
	servers, ok := mcp["servers"].(map[string]any)
	if !ok {
		t.Fatalf("mcp.servers is not an object: %v", mcp)
	}

	return got, servers
}

func newOpenclawConfigTestDirs(t *testing.T) (string, string) {
	t.Helper()
	envRoot := t.TempDir()
	workDir := filepath.Join(envRoot, "workdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	return envRoot, workDir
}

func writeOpenclawUserConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openclaw.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write user cfg: %v", err)
	}
	return path
}

func mustPrepareOpenclawConfig(t *testing.T, envRoot, workDir string, prep OpenclawConfigPrep) OpenclawConfigResult {
	t.Helper()
	result, err := prepareOpenclawConfig(envRoot, workDir, prep)
	if err != nil {
		t.Fatalf("prepareOpenclawConfig: %v", err)
	}
	return result
}

func openclawConfigResponses(configFileOutput string) map[string]openclawResponse {
	return map[string]openclawResponse{
		"config file":                   {stdout: configFileOutput},
		"config get agents.list --json": {stdout: "null"},
	}
}

// OpenClaw owns JSON5 parsing; Multica emits only per-task overrides.
func TestPrepareOpenclawConfigDelegatesParsingToCLI(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)

	userConfigDir := t.TempDir()
	userConfigPath := filepath.Join(userConfigDir, "openclaw.json")
	json5Body := `// JSON5 comment
{
  agents: {
    defaults: { workspace: "/global", },
    list: [
      { id: "scout", workspace: "/scout", },
      { id: "coder", model: "openai/gpt-5", },
    ],
  },
}
`
	if err := os.WriteFile(userConfigPath, []byte(json5Body), 0o600); err != nil {
		t.Fatalf("write user cfg: %v", err)
	}

	stub := installOpenclawStub(t, map[string]openclawResponse{
		"config file": {stdout: userConfigPath + "\n"},
		"config get agents.list --json": {stdout: `[
			{ "id": "scout", "workspace": "/Users/alice/projects/scout" },
			{ "id": "coder", "model": "openai/gpt-5" }
		]`},
	})

	result := mustPrepareOpenclawConfig(t, envRoot, workDir, OpenclawConfigPrep{OpenclawBin: stub.bin})
	cfgPath := result.ConfigPath
	if cfgPath != filepath.Join(envRoot, openclawConfigFile) {
		t.Errorf("cfgPath = %q, want %q", cfgPath, filepath.Join(envRoot, openclawConfigFile))
	}

	got := mustReadJSON(t, cfgPath)

	include, ok := got["$include"].([]any)
	if !ok || len(include) != 1 || include[0] != userConfigPath {
		t.Errorf("$include = %v, want [%q]", got["$include"], userConfigPath)
	}

	if result.IncludeRoot != userConfigDir {
		t.Errorf("IncludeRoot = %q, want %q (dirname of active config so wrapper can $include across dirs)", result.IncludeRoot, userConfigDir)
	}

	agents := got["agents"].(map[string]any)
	defaults := agents["defaults"].(map[string]any)
	if defaults["workspace"] != workDir {
		t.Errorf("agents.defaults.workspace = %v, want %q", defaults["workspace"], workDir)
	}

	list := agents["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("agents.list length = %d, want 2", len(list))
	}
	for i, item := range list {
		entry := item.(map[string]any)
		if entry["workspace"] != workDir {
			t.Errorf("agents.list[%d].workspace = %v, want %q (per-agent overrides must be rewritten so they don't beat defaults)", i, entry["workspace"], workDir)
		}
	}
	if list[0].(map[string]any)["id"] != "scout" {
		t.Errorf("agents.list[0].id lost in carryover: %v", list[0])
	}
	if list[1].(map[string]any)["model"] != "openai/gpt-5" {
		t.Errorf("agents.list[1].model lost in carryover: %v", list[1])
	}
}

// CLI/config failures must not produce a partial wrapper.
func TestPrepareOpenclawConfigFailsClosedOnCLIError(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)

	stub := installOpenclawStub(t, map[string]openclawResponse{
		"config file": {err: errors.New("exec: openclaw: no such file or directory")},
	})

	_, err := prepareOpenclawConfig(envRoot, workDir, OpenclawConfigPrep{OpenclawBin: stub.bin})
	if err == nil {
		t.Fatal("prepareOpenclawConfig succeeded on CLI failure; expected fail closed")
	}
	if !strings.Contains(err.Error(), "locate openclaw active config") {
		t.Errorf("error message %q does not name the failed step", err.Error())
	}

	if _, err := os.Stat(filepath.Join(envRoot, openclawConfigFile)); !os.IsNotExist(err) {
		t.Errorf("wrapper config should not exist after fail-closed; got err = %v", err)
	}
}

func TestPrepareOpenclawConfigFailsClosedOnMalformedAgentsList(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)

	userConfigPath := writeOpenclawUserConfig(t, `{}`)

	stub := installOpenclawStub(t, map[string]openclawResponse{
		"config file":                   {stdout: userConfigPath},
		"config get agents.list --json": {stdout: "<<<garbage>>>"},
	})

	_, err := prepareOpenclawConfig(envRoot, workDir, OpenclawConfigPrep{OpenclawBin: stub.bin})
	if err == nil {
		t.Fatal("prepareOpenclawConfig succeeded on malformed agents.list output; expected fail closed")
	}
	if !strings.Contains(err.Error(), "agents.list") {
		t.Errorf("error message %q does not name the failed step", err.Error())
	}
}

func TestPrepareOpenclawConfigKeyMissingTreatedAsEmpty(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)

	userConfigPath := writeOpenclawUserConfig(t, `{}`)

	stub := installOpenclawStub(t, map[string]openclawResponse{
		"config file":                   {stdout: userConfigPath},
		"config get agents.list --json": {err: errors.New("openclaw: No value at agents.list")},
	})

	result := mustPrepareOpenclawConfig(t, envRoot, workDir, OpenclawConfigPrep{OpenclawBin: stub.bin})
	cfgPath := result.ConfigPath
	got := mustReadJSON(t, cfgPath)
	if _, present := got["agents"].(map[string]any)["list"]; present {
		t.Errorf("agents.list should be omitted when user has none, got %v", got["agents"])
	}
	if got["agents"].(map[string]any)["defaults"].(map[string]any)["workspace"] != workDir {
		t.Errorf("defaults.workspace not set when agents.list missing")
	}
}

func TestPrepareOpenclawConfigFreshInstallNoOnDiskConfig(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)

	missingPath := filepath.Join(t.TempDir(), "openclaw.json")

	stub := installOpenclawStub(t, map[string]openclawResponse{
		"config file": {stdout: missingPath},
	})

	result := mustPrepareOpenclawConfig(t, envRoot, workDir, OpenclawConfigPrep{OpenclawBin: stub.bin})
	cfgPath := result.ConfigPath
	got := mustReadJSON(t, cfgPath)
	if _, present := got["$include"]; present {
		t.Errorf("$include should be absent for fresh install, got %v", got["$include"])
	}
	if got["agents"].(map[string]any)["defaults"].(map[string]any)["workspace"] != workDir {
		t.Errorf("defaults.workspace not set on fresh-install wrapper")
	}
	if result.IncludeRoot != "" {
		t.Errorf("IncludeRoot = %q on fresh install, want empty (no $include emitted)", result.IncludeRoot)
	}
}

func TestPrepareOpenclawConfigExpandsTilde(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)

	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	if err := os.MkdirAll(filepath.Join(fakeHome, ".openclaw"), 0o755); err != nil {
		t.Fatalf("mkdir home/.openclaw: %v", err)
	}
	realPath := filepath.Join(fakeHome, ".openclaw", "openclaw.json")
	if err := os.WriteFile(realPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write user cfg: %v", err)
	}

	stub := installOpenclawStub(t, openclawConfigResponses("~/.openclaw/openclaw.json\n"))

	result := mustPrepareOpenclawConfig(t, envRoot, workDir, OpenclawConfigPrep{OpenclawBin: stub.bin})
	cfgPath := result.ConfigPath
	got := mustReadJSON(t, cfgPath)
	include := got["$include"].([]any)
	if include[0] != realPath {
		t.Errorf("$include[0] = %v, want %q (tilde must be expanded to absolute)", include[0], realPath)
	}
	wantRoot := filepath.Join(fakeHome, ".openclaw")
	if result.IncludeRoot != wantRoot {
		t.Errorf("IncludeRoot = %q, want %q (must be expanded absolute dirname)", result.IncludeRoot, wantRoot)
	}
}

// The active config path is the last non-empty CLI output line.
func TestPrepareOpenclawConfigParsesPathFromUITerminalOutput(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)

	userConfigPath := writeOpenclawUserConfig(t, `{}`)

	stdoutWithUI := "Doctor warning\nstate migration warning\n" + userConfigPath + "\n"

	stub := installOpenclawStub(t, openclawConfigResponses(stdoutWithUI))

	result := mustPrepareOpenclawConfig(t, envRoot, workDir, OpenclawConfigPrep{OpenclawBin: stub.bin})

	got := mustReadJSON(t, result.ConfigPath)
	include := got["$include"].([]any)
	if include[0] != userConfigPath {
		t.Errorf("$include[0] = %v, want %q (path must be extracted from last non-empty line)", include[0], userConfigPath)
	}
}

// Every cross-directory include must be covered by the surfaced include root.
func TestPrepareOpenclawConfigWrapperLoadableUnderIncludeConfinement(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)

	userConfigPath := writeOpenclawUserConfig(t, `{}`)

	stub := installOpenclawStub(t, openclawConfigResponses(userConfigPath))

	result := mustPrepareOpenclawConfig(t, envRoot, workDir, OpenclawConfigPrep{OpenclawBin: stub.bin})

	got := mustReadJSON(t, result.ConfigPath)
	rawIncludes, ok := got["$include"].([]any)
	if !ok || len(rawIncludes) == 0 {
		t.Fatalf("wrapper has no $include entries, but a user config is present: %v", got)
	}

	wrapperDir := filepath.Dir(result.ConfigPath)
	granted := []string{wrapperDir}
	if result.IncludeRoot != "" {
		granted = append(granted, result.IncludeRoot)
	}
	for _, raw := range rawIncludes {
		target, ok := raw.(string)
		if !ok {
			t.Fatalf("$include entry is not a string: %T %v", raw, raw)
		}
		targetDir := filepath.Dir(target)
		allowed := false
		for _, g := range granted {
			if targetDir == g {
				allowed = true
				break
			}
		}
		if !allowed {
			t.Errorf("$include target %q has dirname %q which is not in granted include roots %v — OpenClaw would refuse to load it",
				target, targetDir, granted)
		}
	}
}

// Managed MCP servers replace user-global servers without leaking credentials.
func TestPrepareOpenclawConfigStrictReplacesUserMcpServers(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)

	userCfgPath := writeOpenclawUserConfig(t, `{}`)
	resolvedUser := `{
		"mcp": {"servers": {
			"global_one": {"command": "/bin/echo", "args": ["user"]},
			"shared":     {"command": "/bin/old-version"}
		}},
		"gateway": {"port": 18789},
		"providers": {"anthropic": {"apiKey": "sk-user-secret"}}
	}`
	stub := installOpenclawStub(t, map[string]openclawResponse{
		"config file":                   {stdout: userCfgPath},
		"config get --json":             {stdout: resolvedUser},
		"config get agents.list --json": {stdout: "null"},
	})

	mcpConfig := json.RawMessage(`{
		"mcpServers": {
			"shared":       {"command": "/bin/new-version"},
			"managed_only": {"url": "https://mcp.example.com", "transport": "streamable-http"}
		}
	}`)

	result := mustPrepareOpenclawConfig(t, envRoot, workDir, OpenclawConfigPrep{
		OpenclawBin: stub.bin,
		McpConfig:   mcpConfig,
	})

	got, servers := mustReadOpenclawWrapperMCPServers(t, result.ConfigPath)
	if len(servers) != 2 {
		t.Errorf("mcp.servers has %d entries, want 2 (managed only — global_one must not leak): %v", len(servers), servers)
	}
	if _, leaked := servers["global_one"]; leaked {
		t.Errorf("mcp.servers.global_one leaked into wrapper from user config: %v", servers)
	}
	if shared, ok := servers["shared"].(map[string]any); !ok || shared["command"] != "/bin/new-version" {
		t.Errorf("mcp.servers.shared = %v, want managed `command: /bin/new-version` (managed overrides user same-name)", shared)
	}
	if managed, ok := servers["managed_only"].(map[string]any); !ok || managed["url"] != "https://mcp.example.com" {
		t.Errorf("mcp.servers.managed_only missing or wrong shape: %v", managed)
	}

	// The wrapper's $include must point at the sanitized snapshot, NOT the
	// live user config — otherwise OpenClaw would deep-merge user.mcp back in.
	include, _ := got["$include"].([]any)
	if len(include) != 1 {
		t.Fatalf("wrapper $include has %d entries, want 1: %v", len(include), include)
	}
	snapshotPath, _ := include[0].(string)
	if snapshotPath == userCfgPath {
		t.Fatalf("wrapper $includes the live user config (%q) — strict replace requires the sanitized snapshot", userCfgPath)
	}
	wantSnapshot := filepath.Join(envRoot, openclawUserSnapshotFile)
	if snapshotPath != wantSnapshot {
		t.Errorf("$include = %q, want sanitized snapshot %q", snapshotPath, wantSnapshot)
	}

	// Snapshot must exist, must drop the `mcp` block, and must preserve the
	// non-mcp keys (gateway, providers, secrets) so OpenClaw still has API
	// keys and other config the user relied on.
	snap := mustReadJSON(t, snapshotPath)
	if _, present := snap["mcp"]; present {
		t.Errorf("snapshot still contains an `mcp` block — strict replace not enforced: %v", snap["mcp"])
	}
	if gw, ok := snap["gateway"].(map[string]any); !ok || gw["port"] != float64(18789) {
		t.Errorf("snapshot lost gateway.port carryover: %v", snap["gateway"])
	}
	if _, ok := snap["providers"].(map[string]any); !ok {
		t.Errorf("snapshot lost providers carryover: %v", snap)
	}

	// The snapshot lives in envRoot alongside the wrapper, so the daemon
	// does NOT need to grant an OPENCLAW_INCLUDE_ROOTS entry for it.
	if result.IncludeRoot != "" {
		t.Errorf("IncludeRoot = %q, want empty (snapshot lives in envRoot, no cross-dir include)", result.IncludeRoot)
	}
}

// Strict replacement removes user servers but preserves unrelated MCP settings.
func TestPrepareOpenclawConfigStrictPreservesNonServerMcpKeys(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)
	userCfgPath := writeOpenclawUserConfig(t, `{}`)
	resolvedUser := `{
		"mcp": {
			"sessionIdleTtlMs": 300000,
			"servers": {"global_one": {"command": "/bin/echo"}}
		},
		"gateway": {"port": 18789}
	}`
	stub := installOpenclawStub(t, map[string]openclawResponse{
		"config file":                   {stdout: userCfgPath},
		"config get --json":             {stdout: resolvedUser},
		"config get agents.list --json": {stdout: "null"},
	})
	mcpConfig := json.RawMessage(`{"mcpServers": {"managed_only": {"command": "uvx", "args": ["m"]}}}`)

	result := mustPrepareOpenclawConfig(t, envRoot, workDir, OpenclawConfigPrep{
		OpenclawBin: stub.bin,
		McpConfig:   mcpConfig,
	})

	snapPath := filepath.Join(envRoot, openclawUserSnapshotFile)
	snap := mustReadJSON(t, snapPath)
	snapMcp, ok := snap["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot lost the mcp block entirely; mcp.sessionIdleTtlMs should have survived: %v", snap)
	}
	if _, leaked := snapMcp["servers"]; leaked {
		t.Errorf("snapshot still has mcp.servers; strict scope must drop it: %v", snapMcp)
	}
	// json.Unmarshal decodes JSON numbers as float64.
	if ttl, ok := snapMcp["sessionIdleTtlMs"].(float64); !ok || ttl != 300000 {
		t.Errorf("snapshot lost mcp.sessionIdleTtlMs (should be preserved): %v", snapMcp)
	}

	// Wrapper still emits the managed-only server set on top, so the
	// effective view post-include is exactly the managed set.
	_, servers := mustReadOpenclawWrapperMCPServers(t, result.ConfigPath)
	if _, ok := servers["managed_only"]; !ok {
		t.Errorf("wrapper missing managed_only: %v", servers)
	}
	if _, leaked := servers["global_one"]; leaked {
		t.Errorf("global_one leaked into wrapper: %v", servers)
	}
}

func TestPrepareOpenclawConfigStrictEmptyManagedSetDropsUserMcp(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)
	userCfgPath := writeOpenclawUserConfig(t, `{}`)
	resolvedUser := `{"mcp": {"servers": {"global_one": {"command": "/bin/echo"}}}}`

	cases := map[string]json.RawMessage{
		"object_empty":          json.RawMessage(`{}`),
		"mcp_servers_empty_map": json.RawMessage(`{"mcpServers": {}}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			stub := installOpenclawStub(t, map[string]openclawResponse{
				"config file":                   {stdout: userCfgPath},
				"config get --json":             {stdout: resolvedUser},
				"config get agents.list --json": {stdout: "null"},
			})
			result := mustPrepareOpenclawConfig(t, envRoot, workDir, OpenclawConfigPrep{
				OpenclawBin: stub.bin,
				McpConfig:   raw,
			})
			_, servers := mustReadOpenclawWrapperMCPServers(t, result.ConfigPath)
			if len(servers) != 0 {
				t.Errorf("mcp.servers has %d entries on managed-empty, want 0 (global_one must not leak): %v", len(servers), servers)
			}
			snapPath := filepath.Join(envRoot, openclawUserSnapshotFile)
			snap := mustReadJSON(t, snapPath)
			if _, present := snap["mcp"]; present {
				t.Errorf("snapshot still has `mcp` — strict empty must drop the user block: %v", snap["mcp"])
			}
		})
	}
}

func TestPrepareOpenclawConfigNullMcpConfigKeepsUserInclude(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)
	userCfgPath := writeOpenclawUserConfig(t, `{}`)
	userCfgDir := filepath.Dir(userCfgPath)

	cases := map[string]json.RawMessage{
		"nil":   nil,
		"empty": json.RawMessage(""),
		"null":  json.RawMessage("null"),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			stub := installOpenclawStub(t, openclawConfigResponses(userCfgPath))
			result := mustPrepareOpenclawConfig(t, envRoot, workDir, OpenclawConfigPrep{
				OpenclawBin: stub.bin,
				McpConfig:   raw,
			})
			got := mustReadJSON(t, result.ConfigPath)
			if _, present := got["mcp"]; present {
				t.Errorf("wrapper has mcp block when mcp_config = %q: %v", name, got["mcp"])
			}
			include, _ := got["$include"].([]any)
			if len(include) != 1 || include[0] != userCfgPath {
				t.Errorf("$include = %v, want live user config %q on inherit path", got["$include"], userCfgPath)
			}
			if _, err := os.Stat(filepath.Join(envRoot, openclawUserSnapshotFile)); !os.IsNotExist(err) {
				t.Errorf("inherit path wrote a snapshot file (should not): err=%v", err)
			}
			if result.IncludeRoot != userCfgDir {
				t.Errorf("IncludeRoot = %q, want %q (cross-dir hop for live $include)", result.IncludeRoot, userCfgDir)
			}
		})
	}
}

func TestPrepareOpenclawConfigManagedSetFreshInstall(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)
	missingPath := filepath.Join(t.TempDir(), "openclaw.json")
	stub := installOpenclawStub(t, map[string]openclawResponse{
		"config file": {stdout: missingPath},
	})
	mcpConfig := json.RawMessage(`{"mcpServers": {"context7": {"command": "uvx", "args": ["context7-mcp"]}}}`)

	result := mustPrepareOpenclawConfig(t, envRoot, workDir, OpenclawConfigPrep{
		OpenclawBin: stub.bin,
		McpConfig:   mcpConfig,
	})
	got, servers := mustReadOpenclawWrapperMCPServers(t, result.ConfigPath)
	entry, _ := servers["context7"].(map[string]any)
	if entry == nil || entry["command"] != "uvx" {
		t.Errorf("context7 entry missing/wrong on fresh install: %v", servers)
	}
	args, _ := entry["args"].([]any)
	if len(args) != 1 || args[0] != "context7-mcp" {
		t.Errorf("context7.args = %v", args)
	}
	if _, present := got["$include"]; present {
		t.Errorf("fresh install should not emit $include: %v", got["$include"])
	}
}

func TestPrepareOpenclawConfigFailsClosedOnResolvedConfigError(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)
	userCfgPath := writeOpenclawUserConfig(t, `{}`)
	stub := installOpenclawStub(t, map[string]openclawResponse{
		"config file":                   {stdout: userCfgPath},
		"config get agents.list --json": {stdout: "null"},
		"config get --json":             {err: errors.New("openclaw: schema validation failed")},
	})
	mcpConfig := json.RawMessage(`{"mcpServers": {"context7": {"command": "uvx"}}}`)

	_, err := prepareOpenclawConfig(envRoot, workDir, OpenclawConfigPrep{
		OpenclawBin: stub.bin,
		McpConfig:   mcpConfig,
	})
	if err == nil {
		t.Fatal("prepareOpenclawConfig succeeded when `config get --json` errored; expected fail closed")
	}
	if !strings.Contains(err.Error(), "resolved config") {
		t.Errorf("error %q does not name the resolved-config step", err.Error())
	}
	if _, err := os.Stat(filepath.Join(envRoot, openclawConfigFile)); !os.IsNotExist(err) {
		t.Errorf("wrapper exists after fail-closed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(envRoot, openclawUserSnapshotFile)); !os.IsNotExist(err) {
		t.Errorf("snapshot exists after fail-closed: %v", err)
	}
}

func TestPrepareOpenclawConfigFailsClosedOnMalformedMcpConfig(t *testing.T) {
	envRoot, workDir := newOpenclawConfigTestDirs(t)
	userCfgPath := writeOpenclawUserConfig(t, `{}`)

	cases := map[string]json.RawMessage{
		"unparseable_json":      json.RawMessage(`{not-json}`),
		"entry_missing_command": json.RawMessage(`{"mcpServers": {"bad": {}}}`),
		"entry_wrong_shape":     json.RawMessage(`{"mcpServers": {"bad": "not-an-object"}}`),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			stub := installOpenclawStub(t, openclawConfigResponses(userCfgPath))
			_, err := prepareOpenclawConfig(envRoot, workDir, OpenclawConfigPrep{
				OpenclawBin: stub.bin,
				McpConfig:   raw,
			})
			if err == nil {
				t.Fatalf("prepareOpenclawConfig succeeded on %s; expected fail closed", name)
			}
			if !strings.Contains(err.Error(), "mcp_config") && !strings.Contains(err.Error(), "mcp_servers") {
				t.Errorf("error %q does not name the mcp_config step", err.Error())
			}
		})
	}
}

// Skill writes must land under the workspace path consumed by OpenClaw.
func TestPrepareOpenclawSkillWriteMatchesScanPath(t *testing.T) {
	envRoot := t.TempDir()
	workDir := filepath.Join(envRoot, "workdir")
	for _, sub := range []string{workDir, filepath.Join(envRoot, "output"), filepath.Join(envRoot, "logs")} {
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	stub := installOpenclawStub(t, map[string]openclawResponse{
		"config file": {stdout: filepath.Join(t.TempDir(), "absent-openclaw.json")},
	})

	skills := []SkillContextForEnv{
		{Name: "Issue Review", Content: "Review issues thoroughly."},
		{Name: "Local Dev", Content: "Spin up the local dev env."},
	}

	result := mustPrepareOpenclawConfig(t, envRoot, workDir, OpenclawConfigPrep{OpenclawBin: stub.bin})
	cfgPath := result.ConfigPath
	if err := writeContextFiles(workDir, "openclaw", TaskContextForEnv{
		IssueID:     "issue-1",
		AgentSkills: skills,
	}, nil); err != nil {
		t.Fatalf("writeContextFiles: %v", err)
	}

	cfg := mustReadJSON(t, cfgPath)
	wsDir := cfg["agents"].(map[string]any)["defaults"].(map[string]any)["workspace"].(string)
	for _, s := range skills {
		want := filepath.Join(wsDir, "skills", sanitizeSkillName(s.Name), "SKILL.md")
		if _, err := os.Stat(want); err != nil {
			t.Errorf("openclaw scan target %s missing — Multica's write path and the openclaw scanner are out of sync: %v", want, err)
		}
	}
}

func TestPrepareEnvironmentOpenclawWiresConfigPath(t *testing.T) {
	wsRoot := t.TempDir()

	stub := installOpenclawStub(t, map[string]openclawResponse{
		"config file": {stdout: filepath.Join(t.TempDir(), "absent.json")},
	})

	env, err := Prepare(PrepareParams{
		WorkspacesRoot: wsRoot,
		WorkspaceID:    "ws-1",
		TaskID:         "11111111-2222-3333-4444-555555555555",
		AgentName:      "scout",
		Provider:       "openclaw",
		OpenclawBin:    stub.bin,
		Task: TaskContextForEnv{
			IssueID: "issue-1",
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if env.OpenclawConfigPath == "" {
		t.Fatal("Prepare(openclaw) did not set OpenclawConfigPath")
	}
	got := mustReadJSON(t, env.OpenclawConfigPath)
	workspace := got["agents"].(map[string]any)["defaults"].(map[string]any)["workspace"]
	if workspace != env.WorkDir {
		t.Errorf("agents.defaults.workspace = %v, want %q", workspace, env.WorkDir)
	}
	if env.OpenclawIncludeRoot != "" {
		t.Errorf("OpenclawIncludeRoot = %q on fresh install, want empty", env.OpenclawIncludeRoot)
	}
}

func TestPrepareEnvironmentOpenclawWiresIncludeRoot(t *testing.T) {
	wsRoot := t.TempDir()

	userCfgPath := writeOpenclawUserConfig(t, `{}`)
	userCfgDir := filepath.Dir(userCfgPath)
	stub := installOpenclawStub(t, openclawConfigResponses(userCfgPath))

	env, err := Prepare(PrepareParams{
		WorkspacesRoot: wsRoot,
		WorkspaceID:    "ws-1",
		TaskID:         "33333333-2222-3333-4444-555555555555",
		AgentName:      "scout",
		Provider:       "openclaw",
		OpenclawBin:    stub.bin,
		Task:           TaskContextForEnv{IssueID: "issue-1"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if env.OpenclawIncludeRoot != userCfgDir {
		t.Errorf("OpenclawIncludeRoot = %q, want %q (dirname of active config so daemon can grant OPENCLAW_INCLUDE_ROOTS)", env.OpenclawIncludeRoot, userCfgDir)
	}
}

func TestPrepareEnvironmentOpenclawFailsClosed(t *testing.T) {
	wsRoot := t.TempDir()

	stub := installOpenclawStub(t, map[string]openclawResponse{
		"config file": {err: errors.New("openclaw config validation failed")},
	})

	_, err := Prepare(PrepareParams{
		WorkspacesRoot: wsRoot,
		WorkspaceID:    "ws-1",
		TaskID:         "22222222-2222-3333-4444-555555555555",
		AgentName:      "scout",
		Provider:       "openclaw",
		OpenclawBin:    stub.bin,
		Task:           TaskContextForEnv{IssueID: "issue-1"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("Prepare(openclaw) succeeded when CLI errored; expected fail closed")
	}
	if !strings.Contains(err.Error(), "prepare openclaw config") {
		t.Errorf("error message %q does not name the openclaw config step", err.Error())
	}
}

func TestPrepareEnvironmentNonOpenclawSkipsConfig(t *testing.T) {
	wsRoot := t.TempDir()

	stub := installOpenclawStub(t, map[string]openclawResponse{})

	taskIDs := map[string]string{
		"claude":   "aaaaaaaa-1111-2222-3333-444444444444",
		"opencode": "bbbbbbbb-1111-2222-3333-444444444444",
		"hermes":   "cccccccc-1111-2222-3333-444444444444",
		"kiro":     "dddddddd-1111-2222-3333-444444444444",
	}
	for provider, taskID := range taskIDs {
		t.Run(provider, func(t *testing.T) {
			env, err := Prepare(PrepareParams{
				WorkspacesRoot: wsRoot,
				WorkspaceID:    "ws-1",
				TaskID:         taskID,
				AgentName:      "scout",
				Provider:       provider,
				Task:           TaskContextForEnv{IssueID: "issue-1"},
			}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatalf("Prepare(%s): %v", provider, err)
			}
			if env.OpenclawConfigPath != "" {
				t.Errorf("provider %s should not get an OpenclawConfigPath, got %q", provider, env.OpenclawConfigPath)
			}
			if _, err := os.Stat(filepath.Join(env.RootDir, openclawConfigFile)); !os.IsNotExist(err) {
				t.Errorf("provider %s left a stray openclaw-config.json", provider)
			}
		})
	}
	if len(stub.calls) != 0 {
		t.Errorf("non-openclaw providers shelled out to openclaw CLI %d times: %+v", len(stub.calls), stub.calls)
	}
}

func TestBuildPerTaskOpenclawConfigOmitsGatewayWhenZero(t *testing.T) {
	t.Parallel()

	cfg := buildPerTaskOpenclawConfig(
		"", false, "", nil, "/workdir", nil, false,
		OpenclawGatewayPin{},
	)
	if _, present := cfg["gateway"]; present {
		t.Errorf("zero gateway must not emit a gateway block, got %v", cfg["gateway"])
	}
}

func TestBuildPerTaskOpenclawConfigWritesGatewayBlock(t *testing.T) {
	t.Parallel()

	pin := OpenclawGatewayPin{
		Host:  "gw.internal",
		Port:  18789,
		Token: "secret-token",
		TLS:   true,
	}
	cfg := buildPerTaskOpenclawConfig(
		"", false, "", nil, "/workdir", nil, false,
		pin,
	)

	gw, ok := cfg["gateway"].(map[string]any)
	if !ok {
		t.Fatalf("expected gateway map, got %T: %v", cfg["gateway"], cfg["gateway"])
	}
	if gw["host"] != "gw.internal" {
		t.Errorf("gateway.host = %v, want %q", gw["host"], "gw.internal")
	}
	if gw["port"] != 18789 {
		t.Errorf("gateway.port = %v, want %d", gw["port"], 18789)
	}
	auth, ok := gw["auth"].(map[string]any)
	if !ok {
		t.Fatalf("expected gateway.auth map, got %T: %v", gw["auth"], gw["auth"])
	}
	if auth["mode"] != "token" {
		t.Errorf("gateway.auth.mode = %v, want %q", auth["mode"], "token")
	}
	if auth["token"] != "secret-token" {
		t.Errorf("gateway.auth.token = %v, want %q", auth["token"], "secret-token")
	}
	if gw["tls"] != true {
		t.Errorf("gateway.tls = %v, want true", gw["tls"])
	}
}

func TestBuildPerTaskOpenclawConfigPartialGatewayOmitsZeroFields(t *testing.T) {
	t.Parallel()

	cfg := buildPerTaskOpenclawConfig(
		"", false, "", nil, "/workdir", nil, false,
		OpenclawGatewayPin{Host: "gw.internal", Port: 18789},
	)
	gw := cfg["gateway"].(map[string]any)
	if _, present := gw["auth"]; present {
		t.Errorf("auth block must be omitted when token is empty, got %v", gw["auth"])
	}
	if _, present := gw["tls"]; present {
		t.Errorf("tls field must be omitted when false, got %v", gw["tls"])
	}
}
