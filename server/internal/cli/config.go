package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const defaultCLIConfigPath = ".multica/config.json"

// CLIConfig holds persistent CLI settings.
type CLIConfig struct {
	ServerURL   string `json:"server_url,omitempty"`
	AppURL      string `json:"app_url,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Token       string `json:"token,omitempty"`

	// ProfileCommandOverrides is a per-machine map of custom runtime
	// profile_id -> absolute executable path (MUL-3284). A workspace custom
	// runtime profile records the command_name the daemon resolves on PATH,
	// but the same logical profile may live at a different path on each
	// machine (or not be on PATH at all). This map lets an operator pin the
	// exact binary for a profile on this host via
	// `multica runtime profile set-path`; the daemon prefers it over the
	// PATH lookup in appendProfileRuntimes. Empty / absent means "resolve the
	// profile's command_name on PATH" — the default behavior. The mapping is
	// intentionally local-only (it is never sent to the server) because the
	// path is a property of this machine, not of the shared profile.
	ProfileCommandOverrides map[string]string `json:"profile_command_overrides,omitempty"`
}

// CLIConfigPathForProfile returns the config file path for the given profile.
// An empty profile returns the default path (~/.multica/config.json).
// A named profile returns ~/.multica/profiles/<name>/config.json.
func CLIConfigPathForProfile(profile string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve CLI config path: %w", err)
	}
	if profile == "" {
		return filepath.Join(home, defaultCLIConfigPath), nil
	}
	return filepath.Join(home, ".multica", "profiles", profile, "config.json"), nil
}

// ProfileDir returns the base directory for a profile's state files (pid, log).
// An empty profile returns ~/.multica/. A named profile returns ~/.multica/profiles/<name>/.
func ProfileDir(profile string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve profile dir: %w", err)
	}
	if profile == "" {
		return filepath.Join(home, ".multica"), nil
	}
	return filepath.Join(home, ".multica", "profiles", profile), nil
}

// LoadCLIConfig reads the CLI config from disk (default profile).
func LoadCLIConfig() (CLIConfig, error) {
	return LoadCLIConfigForProfile("")
}

// LoadCLIConfigForProfile reads the CLI config for the given profile.
func LoadCLIConfigForProfile(profile string) (CLIConfig, error) {
	path, err := CLIConfigPathForProfile(profile)
	if err != nil {
		return CLIConfig{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CLIConfig{}, nil
		}
		return CLIConfig{}, fmt.Errorf("read CLI config: %w", err)
	}
	var cfg CLIConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return CLIConfig{}, fmt.Errorf("parse CLI config: %w", err)
	}
	return cfg, nil
}

// SaveCLIConfig writes the CLI config to disk atomically (default profile).
func SaveCLIConfig(cfg CLIConfig) error {
	return SaveCLIConfigForProfile(cfg, "")
}

// SaveCLIConfigForProfile writes the CLI config for the given profile.
func SaveCLIConfigForProfile(cfg CLIConfig, profile string) error {
	path, err := CLIConfigPathForProfile(profile)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create CLI config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode CLI config: %w", err)
	}

	// Write to a temp file in the same directory, then rename for atomicity.
	tmp, err := os.CreateTemp(dir, ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp config file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp config file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename config file: %w", err)
	}
	return nil
}
