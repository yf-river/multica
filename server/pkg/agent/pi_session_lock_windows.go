//go:build windows

package agent

// 文件职责：Windows 平台上"Pi 会话记录文件互斥锁"的实现——flock(LOCK_EX|LOCK_NB)
// 的 Windows 等价物。见 tryLockPiSessionFile 的说明；与 pi_session_lock_unix.go
// 互斥编译。

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLockPiSessionFile 是 flock(LOCK_EX|LOCK_NB) 的 Windows 版。Windows 的字节
// 范围锁是强制的（mandatory），直接锁转写文件本身会让 Pi 读不了它要追加写入的
// 文件，所以改锁一个旁路文件（path+".lock"）。FILE_SHARE_DELETE 保证：当清理与
// 一次已取消的执行竞争时，已持有的锁不会把会话目录钉死。
func tryLockPiSessionFile(path string) (*os.File, bool, error) {
	lockPath := path + ".lock"
	p, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, false, err
	}
	f := os.NewFile(uintptr(h), lockPath)
	overlapped := new(windows.Overlapped)
	// 非阻塞独占锁：抢不到立刻返回 false，而不是挂起等锁。
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, overlapped,
	)
	if err == nil {
		return f, true, nil
	}
	_ = f.Close()
	// 锁被占用（LOCK_VIOLATION）或异步锁定尚未完成（IO_PENDING）都算"没抢到"，
	// 返回 (false, nil) 而非错误。
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return nil, false, nil
	}
	return nil, false, err
}

// releasePiSessionFileLock 释放锁并关闭句柄。
func releasePiSessionFileLock(f *os.File) {
	if f == nil {
		return
	}
	overlapped := new(windows.Overlapped)
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, overlapped)
	_ = f.Close()
}
