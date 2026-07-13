package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIConfigWithoutOverridesLoads(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfgDir := filepath.Join(tmp, ".multica")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	minimal := `{
  "server_url": "https://api.multica.ai",
  "app_url": "https://app.multica.ai",
  "workspace_id": "ws-123",
  "token": "mul_abcdef"
}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadCLIConfigForProfile("")
	if err != nil {
		t.Fatalf("LoadCLIConfig: %v", err)
	}

	if cfg.ServerURL != "https://api.multica.ai" {
		t.Errorf("ServerURL: got %q", cfg.ServerURL)
	}
	if cfg.Token != "mul_abcdef" {
		t.Errorf("Token: got %q", cfg.Token)
	}
	if cfg.Backends != nil {
		t.Errorf("Backends should be nil, got %+v", cfg.Backends)
	}
}

// TestCLIConfig_OpenClawOverride_RoundTrip verifies that setting BinaryPath
// and StateDir survives a save/load cycle.
func TestCLIConfig_OpenClawOverride_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	original := CLIConfig{
		ServerURL: "https://api.multica.ai",
		Token:     "mul_xyz",
		Backends: &BackendOverrides{
			OpenClaw: &OpenClawOverride{
				BinaryPath: "/opt/openclaw-prod/bin/openclaw",
				StateDir:   "/var/lib/openclaw-prod",
			},
		},
	}
	if err := SaveCLIConfigForProfile(original, ""); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadCLIConfigForProfile("")
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Backends == nil || loaded.Backends.OpenClaw == nil {
		t.Fatalf("Backends.OpenClaw should be non-nil after round-trip, got %+v", loaded.Backends)
	}
	if loaded.Backends.OpenClaw.BinaryPath != original.Backends.OpenClaw.BinaryPath {
		t.Errorf("BinaryPath round-trip: got %q, want %q",
			loaded.Backends.OpenClaw.BinaryPath, original.Backends.OpenClaw.BinaryPath)
	}
	if loaded.Backends.OpenClaw.StateDir != original.Backends.OpenClaw.StateDir {
		t.Errorf("StateDir round-trip: got %q, want %q",
			loaded.Backends.OpenClaw.StateDir, original.Backends.OpenClaw.StateDir)
	}
}

// TestCLIConfig_OpenClawOverride_PartialFieldsOmitted verifies that an
// override with only one field set does not emit empty strings for the
// unset field. Users can intentionally set only BinaryPath
// (or only StateDir) and have the other follow the tool default,
// without an empty string overriding env-var precedence.
func TestCLIConfig_OpenClawOverride_PartialFieldsOmitted(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg := CLIConfig{
		ServerURL: "https://api.multica.ai",
		Token:     "mul_xyz",
		Backends: &BackendOverrides{
			OpenClaw: &OpenClawOverride{
				StateDir: "/var/lib/openclaw-prod",
				// BinaryPath intentionally left empty
			},
		},
	}
	if err := SaveCLIConfigForProfile(cfg, ""); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, ".multica", "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	openclaw, ok := raw["backends"].(map[string]any)["openclaw"].(map[string]any)
	if !ok {
		t.Fatalf("could not navigate to backends.openclaw in: %s", string(data))
	}
	if _, present := openclaw["binary_path"]; present {
		t.Errorf("binary_path should be omitted when empty, got: %s", string(data))
	}
	if _, present := openclaw["state_dir"]; !present {
		t.Errorf("state_dir should be present when set, got: %s", string(data))
	}
}

// TestCLIConfig_ProfileCommandOverrides_RoundTrip verifies that pinning a
// per-machine profile command path survives a save/load cycle AND that
// unrelated fields (server_url, token, backends) are preserved across the
// round-trip — the set-path / unset-path CLI commands rely on a
// load->modify->save cycle never dropping config the user already had.
func TestCLIConfig_ProfileCommandOverrides_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	original := CLIConfig{
		ServerURL:   "https://api.multica.ai",
		AppURL:      "https://app.multica.ai",
		WorkspaceID: "ws-123",
		Token:       "mul_xyz",
		Backends: &BackendOverrides{
			OpenClaw: &OpenClawOverride{StateDir: "/var/lib/openclaw-prod"},
		},
		ProfileCommandOverrides: map[string]string{
			"prof-1": "/opt/bin/company-codex",
			"prof-2": "/usr/local/bin/special-claude",
		},
	}
	if err := SaveCLIConfigForProfile(original, ""); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadCLIConfigForProfile("")
	if err != nil {
		t.Fatal(err)
	}

	// The override map must round-trip intact.
	if len(loaded.ProfileCommandOverrides) != 2 {
		t.Fatalf("ProfileCommandOverrides len = %d, want 2: %+v", len(loaded.ProfileCommandOverrides), loaded.ProfileCommandOverrides)
	}
	if got := loaded.ProfileCommandOverrides["prof-1"]; got != "/opt/bin/company-codex" {
		t.Errorf("prof-1 override = %q, want /opt/bin/company-codex", got)
	}
	if got := loaded.ProfileCommandOverrides["prof-2"]; got != "/usr/local/bin/special-claude" {
		t.Errorf("prof-2 override = %q, want /usr/local/bin/special-claude", got)
	}

	// Every other field must be preserved (no clobbering on round-trip).
	if loaded.ServerURL != original.ServerURL {
		t.Errorf("ServerURL = %q, want %q", loaded.ServerURL, original.ServerURL)
	}
	if loaded.AppURL != original.AppURL {
		t.Errorf("AppURL = %q, want %q", loaded.AppURL, original.AppURL)
	}
	if loaded.WorkspaceID != original.WorkspaceID {
		t.Errorf("WorkspaceID = %q, want %q", loaded.WorkspaceID, original.WorkspaceID)
	}
	if loaded.Token != original.Token {
		t.Errorf("Token = %q, want %q", loaded.Token, original.Token)
	}
	if loaded.Backends == nil || loaded.Backends.OpenClaw == nil ||
		loaded.Backends.OpenClaw.StateDir != "/var/lib/openclaw-prod" {
		t.Errorf("Backends.OpenClaw not preserved: %+v", loaded.Backends)
	}
}

// TestCLIConfig_ProfileCommandOverrides_OmittedWhenEmpty verifies the
// omitempty tags keep unused override keys out of the on-disk JSON.
func TestCLIConfig_ProfileCommandOverrides_OmittedWhenEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg := CLIConfig{ServerURL: "https://api.multica.ai", Token: "mul_xyz"}
	if err := SaveCLIConfigForProfile(cfg, ""); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, ".multica", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["profile_command_overrides"]; ok {
		t.Errorf("profile_command_overrides should be omitted when empty, got: %s", string(data))
	}
	if _, ok := raw["backends"]; ok {
		t.Errorf("backends should be omitted when empty, got: %s", string(data))
	}
}
