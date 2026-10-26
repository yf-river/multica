//go:build !linux

package execenv

import "errors"

func ensureCodexNativeStateIdle(string) error {
	return errors.New("Codex native state source inspection requires Linux")
}
