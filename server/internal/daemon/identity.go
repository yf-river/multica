package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/cli"
)

// daemonIDFileName is the file that stores this machine's stable daemon
// identifier. Once created, the UUID inside is the daemon's identity forever
// — hostname changes, .local suffix drift, profile switches and system
// renames no longer mint a new identity.
const daemonIDFileName = "daemon.id"

// EnsureDaemonID returns a stable UUID for this daemon instance, persisting
// it to disk on first call. Identity is machine-scoped: every profile on the
// same machine shares one UUID stored at `~/.multica/daemon.id`. Profile
// boundaries are about which backend/account a daemon is talking to, not
// about the physical machine's identity, so a single host running both the
// CLI-spawned daemon and the desktop-spawned daemon (or toggling profiles)
// registers as one runtime everywhere rather than N.
//
// If the file exists but is corrupt (unparseable), it is regenerated so the
// daemon can continue starting up instead of hard-failing.
func EnsureDaemonID() (string, error) {
	dir, err := cli.ProfileDir("")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, daemonIDFileName)

	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			if _, perr := uuid.Parse(id); perr == nil {
				return id, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read daemon id file: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create profile directory: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate daemon id: %w", err)
	}

	if err := writeDaemonIDFile(path, id.String()); err != nil {
		return "", err
	}
	return id.String(), nil
}

// writeDaemonIDFile writes the UUID to path atomically with 0600 mode.
func writeDaemonIDFile(path, id string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".daemon-*.id.tmp")
	if err != nil {
		return fmt.Errorf("create temp daemon id file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(id + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp daemon id file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp daemon id file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp daemon id file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename daemon id file: %w", err)
	}
	return nil
}
