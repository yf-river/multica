//go:build !windows

package daemon

import (
	"path/filepath"
	"strings"
)

// canonicalPath 解析符号链接得到规范绝对路径（POSIX 语义；Windows 另有实现）。
func canonicalPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// discoveredExecutablePath 对普通可执行文件符号链接做规范化，但保留由已知的
// "按调用名分发"垫片（name-dispatching shim）支撑的入口点。Volta 的 volta-shim
// 与 Vite Plus 的 vp 都从被调用的名字里挑选受管包；直接启动最终目标会把
// `claude --version` 变成 `volta-shim --version` 或 `vp --version`，于是 daemon
// 读到的是版本管理器的版本而非 agent 的，把 runtime 误剔除（#6183、#6702）。
//
// 入口点的父目录仍会被规范化：这能让路径在"所在目录是符号链接或临时的版本管理器
// 前缀"时保持稳定，同时保留分发器需要的 basename——与 buildLoginShellResolveScript
// 一直通过 `pwd -P` 应用的语义相同。
func discoveredExecutablePath(path string) string {
	real := canonicalExecutablePath(path)
	if !isNameDispatchingAgentShim(real) {
		return real
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return real
	}
	realDir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return abs
	}
	return filepath.Join(realDir, filepath.Base(abs))
}

// isNameDispatchingAgentShim 判断规范化后的路径 basename 是否属于已知的、按
// argv[0] 分发受管包的垫片。
func isNameDispatchingAgentShim(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	// 故意收录 "vp" 这个又短又泛化的记号：它是 Vite Plus 的共享分发器，与 Volta
	// 一样从 argv[0] 挑选受管包。这个精确匹配清单只收录"已确认需要被调用入口名"
	// 的分发器。不要去掉可执行扩展名：这些管理器用 wrapper 或 trampoline 而非
	// 按名分发的符号链接，那是本文件永不编译的 Windows 形态。
	return base == "volta-shim" || base == "vp"
}

var executablePathForLaunch = executablePathForLaunchDefault

// executablePathForLaunchDefault 是 POSIX 上的默认实现：无 Windows 安装器
// junction 概念，直接返回"未处理"。
func executablePathForLaunchDefault(string) (string, bool, error) {
	return "", false, nil
}

// canonicalConfiguredExecutablePath 是 POSIX 实现：路径原样返回（Windows 上
// 解析稳定 junction）。
func canonicalConfiguredExecutablePath(path string) string {
	return path
}
