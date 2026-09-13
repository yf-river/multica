//go:build !windows

package agent

// 文件职责：非 Windows 平台对"exec 格式错误"的平台识别——不需要额外补充，
// execve 把所有"这不是程序"的情况都报成 ENOEXEC（IsExecFormatError 已经判过）。

// isPlatformExecFormatError 在 Windows 之外没什么可加的：execve 把所有"这不是个
// 程序"的情况都报成 ENOEXEC，而 IsExecFormatError 已经检查过它了。
func isPlatformExecFormatError(error) bool { return false }
