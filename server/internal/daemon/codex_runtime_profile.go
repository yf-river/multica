package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var codexRuntimeProfileFiles = []string{
	"auth.json",
	"config.json",
	"config.toml",
	"instructions.md",
}

// ensureCodexRuntimeProfile gives daemon-managed Codex tasks a shared profile
// home that is separate from the user's interactive CODEX_HOME. On first use
// it silently seeds the profile from the existing CODEX_HOME or ~/.codex so
// users do not have to log in again.
func ensureCodexRuntimeProfile(daemonID string) error {
	target := strings.TrimSpace(os.Getenv("MULTICA_CODEX_HOME"))
	source := strings.TrimSpace(os.Getenv("MULTICA_CODEX_SOURCE_HOME"))
	if source == "" {
		source = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if target == "" {
		defaultTarget, err := defaultCodexRuntimeProfileHome(daemonID)
		if err != nil {
			return err
		}
		target = defaultTarget
	}
	if source == "" {
		if home, err := os.UserHomeDir(); err == nil {
			source = filepath.Join(home, ".codex")
		}
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve MULTICA_CODEX_HOME %q: %w", target, err)
	}
	sourceAbs := ""
	if source != "" {
		if abs, err := filepath.Abs(source); err == nil {
			sourceAbs = abs
		}
	}
	if err := os.MkdirAll(targetAbs, 0o700); err != nil {
		return fmt.Errorf("create codex runtime profile %s: %w", targetAbs, err)
	}
	if sourceAbs != "" && sourceAbs != targetAbs {
		if err := seedCodexRuntimeProfile(targetAbs, sourceAbs); err != nil {
			return err
		}
	}
	_ = os.Setenv("MULTICA_CODEX_HOME", targetAbs)
	_ = os.Setenv("CODEX_HOME", targetAbs)
	return nil
}

func defaultCodexRuntimeProfileHome(daemonID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home for codex runtime profile: %w", err)
	}
	id := sanitizeRuntimeProfilePathPart(daemonID)
	if id == "" {
		id = "default"
	}
	return filepath.Join(home, ".multica", "runtimes", id, "codex", "CODEX_HOME"), nil
}

func sanitizeRuntimeProfilePathPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), ".-")
}

func seedCodexRuntimeProfile(target, source string) error {
	for _, name := range codexRuntimeProfileFiles {
		dst := filepath.Join(target, name)
		if _, err := os.Lstat(dst); err == nil {
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("stat codex runtime profile file %s: %w", dst, err)
		}
		src := filepath.Join(source, name)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return fmt.Errorf("stat codex source profile file %s: %w", src, err)
		}
		if err := copyCodexRuntimeProfileFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyCodexRuntimeProfileFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read codex source profile file %s: %w", src, err)
	}
	mode := os.FileMode(0o600)
	if filepath.Base(src) == "instructions.md" {
		mode = 0o644
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return fmt.Errorf("write codex runtime profile file %s: %w", dst, err)
	}
	return nil
}
