package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLIConfig_BackwardCompat_OldFileLoads verifies that a historical
// four-field config still loads correctly.
func TestCLIConfig_BackwardCompat_OldFileLoads(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Write a 4-field config exactly as the historical daemon would have.
	cfgDir := filepath.Join(tmp, ".multica")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	historical := `{
  "server_url": "https://api.multica.ai",
  "app_url": "https://app.multica.ai",
  "workspace_id": "ws-123",
  "token": "mul_abcdef"
}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(historical), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadCLIConfigForProfile("")
	if err != nil {
		t.Fatalf("LoadCLIConfig on historical file: %v", err)
	}

	if cfg.ServerURL != "https://api.multica.ai" {
		t.Errorf("ServerURL: got %q, want historical value", cfg.ServerURL)
	}
	if cfg.Token != "mul_abcdef" {
		t.Errorf("Token: got %q, want historical value", cfg.Token)
	}
}

// TestCLIConfig_BackwardCompat_RemovedBackendsOmittedFromJSON verifies that
// saving a current config does not reintroduce the removed backend overrides.
func TestCLIConfig_BackwardCompat_RemovedBackendsOmittedFromJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg := CLIConfig{
		ServerURL: "https://api.multica.ai",
		Token:     "mul_xyz",
	}
	if err := SaveCLIConfigForProfile(cfg, ""); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, ".multica", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" {
		t.Fatal("config file is empty")
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal saved config: %v", err)
	}
	if _, ok := raw["backends"]; ok {
		t.Errorf("backends key should be omitted when nil, got: %s", string(data))
	}
}

// TestCLIConfig_ProfileCommandOverrides_RoundTrip verifies that pinning a
// per-machine profile command path survives a save/load cycle AND that
// unrelated fields (server_url and token) are preserved across the
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
}

// TestCLIConfig_ProfileCommandOverrides_OmittedWhenEmpty verifies the
// omitempty tag keeps the key out of the on-disk JSON when no overrides are
// set, so configs for users who never pin a path stay byte-stable.
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
}

// TestCLIConfig_UnknownFieldsArePreserved verifies forward-compat: a future
// daemon that adds, say, a `backends.codex` key should not have its data
// destroyed when an older daemon (without knowledge of that key) reads and
// re-saves the file. Today Go's encoding/json silently DROPS unknown fields
// on round-trip. This test documents the gap so future maintainers know.
//
// Skipped today (encoding/json does not preserve unknown fields), but the
// test is written so a future change to a preserve-unknown encoder
// (json.RawMessage, mapstructure, etc.) will pick it up.
func TestCLIConfig_UnknownFieldsArePreserved(t *testing.T) {
	t.Skip("documenting known limitation: encoding/json drops unknown fields on round-trip; future PR can switch to a preserving encoder")

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfgDir := filepath.Join(tmp, ".multica")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	withFutureField := `{
  "server_url": "https://api.multica.ai",
  "token": "mul_xyz",
  "backends": {
    "future_backend_abc": {"state_dir": "/x"},
    "future_backend_xyz": {"some_setting": "preserve me"}
  }
}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(withFutureField), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadCLIConfigForProfile("")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveCLIConfigForProfile(cfg, ""); err != nil {
		t.Fatal(err)
	}

	// After round-trip, future_backend_xyz should still be in the file.
	data, _ := os.ReadFile(filepath.Join(cfgDir, "config.json"))
	if !strings.Contains(string(data), "future_backend_xyz") {
		t.Error("unknown field future_backend_xyz was dropped on round-trip")
	}
}
