package agent

// 文件职责：Windows 平台对"exec 格式错误"的平台识别——把 ERROR_BAD_EXE_FORMAT
// 归到 exec-format 错误的统一判定里（本文件由 `_windows` 文件名后缀自动限定平台）。

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isPlatformExecFormatError 认 Windows 上"这个文件不是我跑得了的程序"的拼写。
//
// 桩问题不只发生在 POSIX：npm 12 在 Windows 上也拦安装脚本，于是 `bin/claude.exe`
// 一直保持发货时的文本占位桩，CreateProcess 用 ERROR_BAD_EXE_FORMAT 拒它——Windows
// 从不为它报 ENOEXEC，所以没有这个函数，同一份坏安装在这里会读成"一次临时抖动"，
// 让运行时装死在线。
func isPlatformExecFormatError(err error) bool {
	return errors.Is(err, windows.ERROR_BAD_EXE_FORMAT)
}
