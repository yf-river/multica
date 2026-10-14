package execenv

import (
	"errors"
	"fmt"
	"os"
)

func recordSkillFile(path string, data []byte, perm os.FileMode, m *sidecarManifest) error {
	if m != nil {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%w: %s", errPathPreExists, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := writeFileAtomic(path, data, perm); err != nil {
		return err
	}
	if m != nil {
		m.Files = append(m.Files, path)
	}
	return nil
}
