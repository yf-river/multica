package execenv

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// Files to symlink from the shared ~/.codex/ into the per-task CODEX_HOME.
// The daemon points CODEX_HOME at a runtime profile before tasks are prepared,
// so this links to the runtime profile, not the user's interactive Codex home.
// Auth stays shared at the runtime level so token refreshes propagate across
// task-local homes without pushing session/log/state writes into the user home.
var codexSymlinkedFiles = []string{
	"auth.json",
}

// Files to copy from the shared ~/.codex/ into the per-task CODEX_HOME.
// Copies are isolated — changes don't affect the shared home.
var codexCopiedFiles = []string{
	"config.json",
	"config.toml",
	"instructions.md",
}

// prepareCodexHome creates a per-task CODEX_HOME directory and seeds
// it from the daemon's shared runtime Codex home. Auth is symlinked to the
// runtime profile; config files are copied; session/log/state files stay local
// to the task home. Daemon-owned Codex policy is applied through app-server
// argv overrides when the process starts, leaving the copied config untouched.
func prepareCodexHome(codexHome string, logger *slog.Logger) error {
	sharedHome := resolveSharedCodexHome()

	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		return fmt.Errorf("create codex-home dir: %w", err)
	}

	// Symlink shared files (auth).
	for _, name := range codexSymlinkedFiles {
		src := filepath.Join(sharedHome, name)
		dst := filepath.Join(codexHome, name)
		if err := ensureSymlink(src, dst); err != nil {
			logger.Warn("execenv: codex-home symlink failed", "file", name, "error", err)
		}
	}

	// Surface the resulting auth.json state (file kind only, never contents)
	// so operators diagnosing token-refresh failures can tell whether the
	// per-task home is tracking the shared ~/.codex/auth.json or has drifted
	// into a stale local copy.
	logCodexAuthState(filepath.Join(codexHome, "auth.json"), logger)

	// Sync config files from the shared source (isolated per task).
	for _, name := range codexCopiedFiles {
		src := filepath.Join(sharedHome, name)
		dst := filepath.Join(codexHome, name)
		if err := syncCopiedFile(src, dst); err != nil {
			logger.Warn("execenv: codex-home sync failed", "file", name, "error", err)
		}
	}

	// Drop `[[skills.config]]` entries inherited from the user's
	// ~/.codex/config.toml. Codex Desktop writes plugin-backed skills with a
	// `name` and no `path`, which the CLI's stricter TOML parser rejects with
	// `missing field path` and bails out of `thread/start`. Multica writes the
	// agent's active skills directly to `codex-home/skills/`, so the
	// user-level registry is redundant here. See codex_skill_strip.go.
	if err := sanitizeCopiedCodexConfig(filepath.Join(codexHome, "config.toml")); err != nil {
		logger.Warn("execenv: codex-home sanitize config failed", "error", err)
	}

	if err := exposeSharedCodexPluginCache(codexHome, sharedHome); err != nil {
		logger.Warn("execenv: codex-home plugin cache exposure failed", "error", err)
	}

	return nil
}

// resolveSharedCodexHome returns the path to the user's shared Codex home.
// Checks $CODEX_HOME first, falls back to ~/.codex.
func resolveSharedCodexHome() string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		abs, err := filepath.Abs(v)
		if err == nil {
			return abs
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".codex") // last resort fallback
	}
	return filepath.Join(home, ".codex")
}

func exposeSharedCodexPluginCache(codexHome, sharedHome string) error {
	src := filepath.Join(sharedHome, "plugins", "cache")
	dst := filepath.Join(codexHome, "plugins", "cache")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return fmt.Errorf("create shared plugin cache dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create codex plugin dir: %w", err)
	}

	if fi, err := os.Lstat(dst); err == nil {
		isLink := fi.Mode()&os.ModeSymlink != 0
		if isLink {
			if target, readlinkErr := os.Readlink(dst); readlinkErr == nil && target == src {
				return nil
			}
			if err := os.Remove(dst); err != nil {
				return fmt.Errorf("remove stale plugin cache link: %w", err)
			}
		} else {
			if err := os.RemoveAll(dst); err != nil {
				return fmt.Errorf("remove stale plugin cache path: %w", err)
			}
		}
	}

	if err := createDirLink(src, dst); err != nil {
		return fmt.Errorf("expose shared plugin cache: %w", err)
	}
	return nil
}

// ensureSymlink ensures dst tracks src. If src doesn't exist, it's a no-op.
// If dst is already a symlink pointing at src, it's a no-op. Otherwise — a
// wrong-target symlink, a broken symlink, or a regular file left over from a
// prior createFileLink copy fallback — dst is removed and recreated via
// createFileLink so the per-task home doesn't drift from the shared source.
//
// The "regular file" branch matters on Windows: when os.Symlink fails (no
// Developer Mode / not elevated), createFileLink falls back to copying the
// file. Without this re-creation step, a once-stale auth.json would never
// pick up token refreshes from the shared ~/.codex/auth.json, leaving Codex
// stuck on a revoked refresh token across env reuses (issue #2081).
func ensureSymlink(src, dst string) error {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil // source doesn't exist — skip
	}

	if fi, err := os.Lstat(dst); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(dst); err == nil && target == src {
				return nil // symlink already points to src
			}
		}
		// Wrong-target symlink, broken symlink, or stale regular file —
		// drop it so createFileLink can re-link/re-copy from the current src.
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("remove stale dst %s: %w", dst, err)
		}
	}

	return createFileLink(src, dst)
}

// logCodexAuthState records the kind of auth.json the per-task CODEX_HOME
// ended up with — symlink (with target), regular file (with size + mtime),
// or missing — so an operator chasing refresh_token_reused / token_expired
// reports can immediately tell whether the per-task home is tracking the
// shared ~/.codex/auth.json or has drifted into a stale local copy.
//
// Never logs the file contents.
func logCodexAuthState(authPath string, logger *slog.Logger) {
	fi, err := os.Lstat(authPath)
	if err != nil {
		logger.Info("execenv: codex auth.json absent", "path", authPath, "error", err)
		return
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(authPath)
		logger.Info("execenv: codex auth.json is symlink", "path", authPath, "target", target)
		return
	}
	logger.Info("execenv: codex auth.json is regular file",
		"path", authPath,
		"size", fi.Size(),
		"mtime", fi.ModTime().UTC(),
	)
}

// syncCopiedFile mirrors a per-task dst onto the current state of the shared
// src so the per-task copy tracks the shared source across Reuse() runs:
//
//   - src present, dst absent:  copy src → dst
//   - src present, dst present: drop dst and re-copy src → dst (refresh)
//   - src absent,  dst present: drop dst (the shared source has been removed,
//     so the per-task stale copy must not linger)
//   - src absent,  dst absent:  no-op
//
// Regression for MUL-2646: the prior "don't overwrite" guard left per-task
// config.toml / config.json / instructions.md stuck on whatever snapshot they
// were seeded with at first Prepare. A user who edited ~/.codex/config.toml
// between runs — switching the active [model_providers.X] base_url, pointing
// env_key at a freshly rotated API key, or removing the file outright to
// drop a provider — kept hitting the stale per-task copy on session resume,
// with Codex calling the new URL using the old key (or replaying a provider
// the user had since deleted from the shared config).
//
// Every copied file disappears in lockstep when its shared source is removed.
func syncCopiedFile(src, dst string) error {
	_, srcErr := os.Stat(src)
	srcMissing := os.IsNotExist(srcErr)
	if srcErr != nil && !srcMissing {
		return fmt.Errorf("stat src %s: %w", src, srcErr)
	}

	if _, err := os.Lstat(dst); err == nil {
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("remove stale dst %s: %w", dst, err)
		}
	}

	if srcMissing {
		return nil
	}
	return copyFile(src, dst)
}

// copyFile copies src to dst unconditionally.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s → %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dst, err)
	}
	return nil
}
