//go:build windows

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// extendedLengthPrefix 与 extendedLengthUNCPrefix 是 GetFinalPathNameByHandleW 以
// VOLUME_NAME_DOS 返回的两种形态：本地路径回成 `\\?\C:\dir\file`，网络路径回成
// `\\?\UNC\server\share\file`。
const (
	extendedLengthPrefix    = `\\?\`
	extendedLengthUNCPrefix = `\\?\UNC\`
)

// trimExtendedLengthPrefix 当路径短到不需要扩展长度前缀时，把它还原成普通的
// Win32 形式；否则原样返回。
//
// 该前缀不是装饰性的：路径超过 MAX_PATH 之后它是命名文件的唯一手段，所以长路径要
// 保留它。但它也并非被普遍理解：cmd.exe 直接拒绝它，而通过 npm 安装的 agent 是经
// `.cmd` 垫片进入的——Windows 只能把这样的路径交给命令解释器去跑——因此一个保留
// 前缀的启动目标根本无法执行。只要前缀不是必需就摘掉，能让两种形态都可启动，也能
// 让解析出的路径与拿来匹配的配置路径可比对。见 #6883。
func trimExtendedLengthPrefix(path string) string {
	var trimmed string
	switch {
	case strings.HasPrefix(path, extendedLengthUNCPrefix):
		trimmed = `\\` + path[len(extendedLengthUNCPrefix):]
	case strings.HasPrefix(path, extendedLengthPrefix):
		trimmed = path[len(extendedLengthPrefix):]
		// 只有盘符限定的路径（`C:\...`）才是去掉前缀后仍然合法的 Win32 路径。
		// 其它形态（比如卷 GUID）必须保留前缀。
		if len(trimmed) < 3 || trimmed[1] != ':' || !os.IsPathSeparator(trimmed[2]) {
			return path
		}
	default:
		return path
	}
	if !fitsWithoutExtendedLengthPrefix(trimmed) {
		return path
	}
	return trimmed
}

// fitsWithoutExtendedLengthPrefix 判断 path 是否短到能作为普通 Win32 路径使用。
//
// MAX_PATH 按 UTF-16 代码单元计数且包含结尾 NUL，所以这里不能用 len(path)：Go 量的
// 是 UTF-8 字节数，每个非 ASCII 字符占的字节比代码单元多。一个带 CJK 用户名的用户
// 目录下的路径会被多算、被判过长、从而保留一个并不需要的前缀——正好让最不可能被
// 测到的那批用户重新吃上 .cmd 启动失败的亏。
func fitsWithoutExtendedLengthPrefix(path string) bool {
	// UTF16FromString 返回字符串加结尾 NUL——这正是 MAX_PATH 的计数单位。它只在
	// 遇到内嵌 NUL 时失败，真实路径不会有——按“保留前缀”处理，而不是乱猜。
	encoded, err := windows.UTF16FromString(path)
	if err != nil {
		return false
	}
	return len(encoded) <= syscall.MAX_PATH
}

// canonicalPath 返回 path 的最终真实路径（follow 一切符号链接/junction，并解析成
// Win32 完整路径）；失败时返回错误。
func canonicalPath(path string) (string, error) {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(
		pathUTF16,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)

	buffer := make([]uint16, syscall.MAX_PATH)
	for {
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return "", err
		}
		// 缓冲区不够时返回值是所需长度（含 NUL），按它扩容后重试；够用则收工。
		if length < uint32(len(buffer)) {
			return trimExtendedLengthPrefix(windows.UTF16ToString(buffer[:length])), nil
		}
		buffer = make([]uint16, length+1)
	}
}

// discoveredExecutablePath 把探测到的可执行文件路径规整成绝对路径；已是绝对路径时
// 原样返回，失败（如 Abs 出错）则退回原值。
func discoveredExecutablePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// canonicalConfiguredExecutablePath 规整用户配置里给出的可执行文件路径；Windows 上
// 与 discoveredExecutablePath 同义。
func canonicalConfiguredExecutablePath(path string) string {
	return discoveredExecutablePath(path)
}

var executablePathForLaunch = executablePathForLaunchWindows

// executablePathForLaunchWindows 计算真正要拉起的进程路径：先取绝对路径，再解析到
// 最终真实文件（follow 链接/junction），保证启动的是目标本体而非悬空的中间链接。
func executablePathForLaunchWindows(path string) (string, bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", true, err
	}
	resolved, err := canonicalPath(abs)
	return resolved, true, err
}
