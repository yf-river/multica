//go:build windows

// 环境锁（Windows 实现）：与 envlock_unix.go 提供的 API 相同，
// 但底层用 Windows 的 LockFileEx/UnlockFileEx。加锁同样"不等待"，
// 锁随句柄关闭而释放。

package execenv

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// openLockFile 打开（必要时创建）锁文件，并返回可加锁的句柄。
// 必须用 FILE_SHARE_DELETE 共享方式打开，而 os.OpenFile 默认不请求该标志。
//
// 为什么需要它：Windows 不允许删除"仍被某个进程打开"的文件。若不加这个标志，
// 一把被持有的锁就会把整个环境根目录钉住——GC 无法回收目录，测试清理临时目录
// 也会失败。Unix 本就允许删除一个已被打开的文件，加了这个标志后 Windows 的
// 行为与 Unix 一致：锁只负责协调并发执行，不应阻止目录被回收。
func openLockFile(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// lockFileExclusiveNonBlocking 对 f 尝试加排他锁，加锁时立即返回、绝不阻塞
// （LOCKFILE_FAIL_IMMEDIATELY）。返回的 ok 为 false 表示锁已被别人持有。
func lockFileExclusiveNonBlocking(f *os.File) (ok bool, err error) {
	overlapped := new(windows.Overlapped)
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, overlapped,
	)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return false, err
}

// unlockFile 主动释放这把锁。
func unlockFile(f *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, overlapped)
}
