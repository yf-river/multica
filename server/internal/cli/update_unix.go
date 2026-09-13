//go:build !windows

// update_unix.go 是 CLI 自更新流程在 Unix 平台上的实现：用「直接 rename」原子替换
// 正在运行的二进制。Windows 平台对应的逻辑见 update_windows.go。
package cli

import "os"

// replaceBinary 用刚下载好的新二进制替换正在运行的可执行文件。Unix 上内核会让
// 正在运行的进程继续持有旧 inode，所以直接 rename 覆盖是安全的。
func replaceBinary(tmpPath, exePath string) error {
	return os.Rename(tmpPath, exePath)
}

// CleanupStaleUpdateArtifacts 在 Unix 上是空操作——没有需要回收的旁车(sidecar)文件。
func CleanupStaleUpdateArtifacts() {}
