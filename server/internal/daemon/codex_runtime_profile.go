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

// ensureCodexRuntimeProfile resolves the shared Codex home used to seed
// task-local CODEX_HOME directories. By default it uses the user's active
// Codex home directly; an explicit MULTICA_CODEX_HOME remains supported for
// operators who still want a separate runtime profile.
func ensureCodexRuntimeProfile(_ string) error {
	target := strings.TrimSpace(os.Getenv("MULTICA_CODEX_HOME"))
	source := strings.TrimSpace(os.Getenv("MULTICA_CODEX_SOURCE_HOME"))
	if source == "" {
		source = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if source == "" {
		if home, err := os.UserHomeDir(); err == nil {
			source = filepath.Join(home, ".codex")
		} else {
			return fmt.Errorf("resolve user home for codex runtime profile: %w", err)
		}
	}
	if target == "" {
		target = source
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
