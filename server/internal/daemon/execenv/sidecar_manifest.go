package execenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// sidecarManifestFile records workdir paths created by Prepare. It lives in
// daemon scratch rather than the user's workdir.
const sidecarManifestFile = ".multica_sidecar_manifest.json"

// errPathPreExists protects user-owned paths. Tracked cleanup can safely delete
// files only when recordWriteFile created them.
var errPathPreExists = errors.New("execenv: refuse to overwrite pre-existing path")

// sidecarManifest contains only paths created by Multica. Dirs are stored in
// creation order and removed in reverse order.
type sidecarManifest struct {
	Files []string `json:"files,omitempty"`
	Dirs  []string `json:"dirs,omitempty"`
}

// recordMkdirAll records only directories it creates. A nil manifest is used
// for isolated Codex-home content that environment teardown removes wholesale.
func recordMkdirAll(path string, m *sidecarManifest) error {
	if path == "" {
		return os.MkdirAll(path, 0o755)
	}
	if m == nil {
		return os.MkdirAll(path, 0o755)
	}
	// Collect missing ancestors without claiming pre-existing user directories.
	var toCreate []string
	cur := filepath.Clean(path)
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("stat ancestor %s: %w", cur, err)
		}
		toCreate = append(toCreate, cur)
		parent := filepath.Dir(cur)
		if parent == cur || parent == "." {
			break
		}
		cur = parent
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	// Store root-first so cleanup can remove leaf-first.
	for i, j := 0, len(toCreate)-1; i < j; i, j = i+1, j-1 {
		toCreate[i], toCreate[j] = toCreate[j], toCreate[i]
	}
	m.Dirs = append(m.Dirs, toCreate...)
	return nil
}

// recordWriteFile refuses every pre-existing filesystem entry when tracking is
// enabled. A nil manifest is reserved for isolated Codex-home content.
func recordWriteFile(path string, data []byte, m *sidecarManifest) error {
	if m == nil {
		return os.WriteFile(path, data, 0o644)
	}
	_, statErr := os.Lstat(path)
	if statErr == nil {
		return fmt.Errorf("%w: %s", errPathPreExists, path)
	}
	if !errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Errorf("stat target %s: %w", path, statErr)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	m.Files = append(m.Files, path)
	return nil
}

// allocateCollisionFreeSkillDir preserves an occupied natural slug and tries
// provider-discoverable sibling names. The bound prevents an infinite probe.
func allocateCollisionFreeSkillDir(skillsParent, baseSlug string) (slug, dir string, err error) {
	const maxAttempts = 64
	for i := 0; i < maxAttempts; i++ {
		var candidate string
		switch i {
		case 0:
			candidate = baseSlug
		case 1:
			candidate = baseSlug + "-multica"
		default:
			candidate = fmt.Sprintf("%s-multica-%d", baseSlug, i)
		}
		path := filepath.Join(skillsParent, candidate)
		if _, statErr := os.Lstat(path); statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				return candidate, path, nil
			}
			return "", "", fmt.Errorf("stat candidate %s: %w", path, statErr)
		}
	}
	return "", "", fmt.Errorf("allocate collision-free skill dir under %s: exhausted %d attempts for base %q", skillsParent, maxAttempts, baseSlug)
}

// writeSidecarManifest persists even an empty manifest so cleanup knows
// tracking ran for this environment.
func writeSidecarManifest(envRoot string, m *sidecarManifest) error {
	if envRoot == "" {
		return nil
	}
	if m == nil {
		m = &sidecarManifest{}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal sidecar manifest: %w", err)
	}
	return os.WriteFile(filepath.Join(envRoot, sidecarManifestFile), data, 0o644)
}

// CleanupSidecars removes tracked files and then tracked directories
// leaf-first. Missing paths and non-empty directories are safe no-ops; other
// I/O errors are returned after the remaining entries have been attempted.
func CleanupSidecars(envRoot string) error {
	manifestPath := filepath.Join(envRoot, sidecarManifestFile)
	data, err := os.ReadFile(manifestPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read sidecar manifest %s: %w", manifestPath, err)
	}
	var m sidecarManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse sidecar manifest %s: %w", manifestPath, err)
	}

	var firstErr error
	captureErr := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	for _, f := range m.Files {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			captureErr(fmt.Errorf("remove %s: %w", f, err))
		}
	}

	for i := len(m.Dirs) - 1; i >= 0; i-- {
		d := m.Dirs[i]
		err := os.Remove(d)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			continue
		}
		entries, readErr := os.ReadDir(d)
		switch {
		case readErr != nil && !errors.Is(readErr, fs.ErrNotExist):
			captureErr(fmt.Errorf("rmdir %s: %w", d, err))
		case len(entries) > 0:
			// Preserve content added after Prepare.
		default:
			captureErr(fmt.Errorf("rmdir %s: %w", d, err))
		}
	}

	if err := os.Remove(manifestPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		captureErr(fmt.Errorf("remove manifest %s: %w", manifestPath, err))
	}

	return firstErr
}

// removeReusedManagedSkillDirs reclaims only managed skill directories before
// a cloud workdir is refreshed. CleanupSidecars intentionally preserves
// non-empty directories and therefore cannot perform this reuse-only step.
func removeReusedManagedSkillDirs(envRoot, skillsParent string) error {
	if envRoot == "" || skillsParent == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(envRoot, sidecarManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read sidecar manifest for reuse skill rollback: %w", err)
	}
	var m sidecarManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse sidecar manifest for reuse skill rollback: %w", err)
	}

	cleanParent := filepath.Clean(skillsParent)
	var firstErr error
	for _, d := range m.Dirs {
		if filepath.Dir(filepath.Clean(d)) != cleanParent {
			continue
		}
		if err := os.RemoveAll(d); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("remove managed skill dir %s: %w", d, err)
		}
	}
	return firstErr
}
