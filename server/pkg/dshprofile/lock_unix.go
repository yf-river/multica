//go:build !windows

package dshprofile

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func lock(f *os.File, exclusive bool) error {
	mode := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		mode = unix.LOCK_EX | unix.LOCK_NB
	}
	err := unix.Flock(int(f.Fd()), mode)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return ErrBusy
	}
	return err
}
