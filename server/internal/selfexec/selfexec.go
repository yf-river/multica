// Package selfexec 负责解析"当前进程背后那个可执行文件"的绝对路径。
package selfexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Resolve 优先用操作系统上报的可执行文件路径。某些启动环境可能缺失该元数据，
// 于是回退到按常规可执行查找语义处理 argv[0]（而不是把裸命令名当成相对当前目录
// 的文件）。
func Resolve() (string, error) {
	return resolveWith(os.Executable, os.Args)
}

// resolveWith 是 Resolve 的可注入实现，便于测试传假的 osExecutable / args。
func resolveWith(osExecutable func() (string, error), args []string) (string, error) {
	exePath, err := osExecutable()
	if err == nil {
		return exePath, nil
	}
	osExecutableErr := fmt.Errorf("os.Executable: %w", err)

	if len(args) == 0 || args[0] == "" {
		return "", errors.Join(osExecutableErr, errors.New("argv[0] is empty"))
	}

	// argv[0] 走 PATH 查找得到候选路径，再转绝对路径并确认是常规文件，
	// 避免解析出目录或缺失的条目。
	candidate, fallbackErr := exec.LookPath(args[0])
	if fallbackErr == nil {
		candidate, fallbackErr = filepath.Abs(candidate)
	}
	if fallbackErr == nil {
		var info os.FileInfo
		info, fallbackErr = os.Stat(candidate)
		if fallbackErr == nil && !info.Mode().IsRegular() {
			fallbackErr = fmt.Errorf("%s is not a regular file", candidate)
		}
	}
	if fallbackErr != nil {
		return "", errors.Join(
			osExecutableErr,
			fmt.Errorf("resolve argv[0] %q: %w", args[0], fallbackErr),
		)
	}

	return candidate, nil
}
