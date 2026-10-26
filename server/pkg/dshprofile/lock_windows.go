//go:build windows

package dshprofile

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

func lock(f *os.File, exclusive bool) error {
	mode := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		mode |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	err := windows.LockFileEx(windows.Handle(f.Fd()), mode, 0, 1, 0, new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return ErrBusy
	}
	return err
}
