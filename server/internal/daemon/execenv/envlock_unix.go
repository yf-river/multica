//go:build !windows

// 环境锁：一组用"文件锁"（flock）协调任务环境并发的工具函数。
// 目的是回答"上一次执行是否还活着"，避免同一目录被并发清理或复用。

package execenv

import (
	"os"

	"golang.org/x/sys/unix"
)

// openLockFile 以"不存在则创建、可读写"的方式打开锁文件，返回可加锁的文件句柄。
//
// 这把锁真正要回答的问题是："上一次执行是否还活着？"。锁由内核在文件被关闭、
// 或持有进程退出（包括异常崩溃）时自动释放——因此不需要心跳、PID 表或清理
// 陈旧状态，这正是用"文件锁"而不是普通"标记文件"的原因。
func openLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
}

// lockFileExclusiveNonBlocking 对 f 尝试加一把排他性"建议锁"，加锁时绝不等待
// （LOCK_NB）。返回的 ok 为 false 表示锁已被其他进程持有（即并发冲突），
// 此时 err 为 nil；只有真正的系统错误才返回非 nil 的 err。
func lockFileExclusiveNonBlocking(f *os.File) (ok bool, err error) {
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if err == unix.EWOULDBLOCK {
		return false, nil
	}
	return false, err
}

// unlockFile 主动释放这把建议锁。其实关闭文件也会自动解锁，这里显式释放只是
// 让"用完即放"的意图在调用点表达得更清楚。
func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
