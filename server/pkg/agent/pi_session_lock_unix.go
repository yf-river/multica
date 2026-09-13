//go:build !windows

package agent

// 文件职责：Unix 平台上"Pi 会话记录文件互斥锁"的实现——用非阻塞 flock 独占锁
// 锁住转写文件本身。与 pi_session_lock_windows.go 互斥编译。

import (
	"os"

	"golang.org/x/sys/unix"
)

// tryLockPiSessionFile 对转写文件加非阻塞独占锁。daemon 可能同时启动多个任务，而
// Pi 直接往这份 JSONL 追加，所以锁必须覆盖完整的子进程生命周期。daemon 挂了内核
// 会自动释放它。
func tryLockPiSessionFile(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, false, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		// 已有别家在锁 → 没抢到（返回 false 而非错误）。
		if err == unix.EWOULDBLOCK {
			return nil, false, nil
		}
		return nil, false, err
	}
	return f, true, nil
}

// releasePiSessionFileLock 解锁并关闭句柄。
func releasePiSessionFileLock(f *os.File) {
	if f == nil {
		return
	}
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	_ = f.Close()
}
