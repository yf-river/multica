// 定位 Reasonix 运行归属用户的配置文件路径。
//
// 注意这不是"尽力而为"的查找：每任务的 reasonix.toml 会替换用户设置过的
// [permissions]，因此 daemon 一旦没找到某份配置，就等于任务静默丢掉了用户
// 的整条 deny 列表。找到的文件必须是 Reasonix 子进程"实际会加载"的那一份，
// 并且要在所有平台、用户可能挪动的各种方式下都成立。

package execenv

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// 下面的解析是 Reasonix 自身逻辑（DeepSeek-Reasonix 里的
// internal/config/paths.go，v2）的移植，而不是对其的近似：
//
//   - REASONIX_HOME 优先，需先做 ${VAR} / ${VAR:-default} 展开、~ 处理与
//     绝对路径规范化。
//   - 未设置时，home 在 Windows 上是 %AppData%\reasonix（回退到
//     %USERPROFILE%\AppData\Roaming\reasonix），其他平台是 ~/.reasonix。
//   - 该 home 下的 config.toml 是主路径。当它不存在"且"未设置 REASONIX_HOME
//     时，Reasonix 会改读旧的 OS 支持目录与 XDG 位置——所以 daemon 也得去
//     那些地方找。
//
// 这里与上游不一致是"安全 bug"而非外观问题。务必与上游保持同步。

// reasonixGOOS 就是 runtime.GOOS，允许被覆盖以便测试在任意运行环境覆盖
// Windows 分支的解析。除测试外绝不被赋值。
var reasonixGOOS = runtime.GOOS

// reasonixVarRef 匹配 ${VAR} 与 ${VAR:-default}，对应 Reasonix 的 ExpandVars。
var reasonixVarRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// reasonixEnv 是任务内 Reasonix 子进程会看到的环境：agent 的 custom_env
// （已剥离 daemon 黑名单键）叠加在 daemon 自身环境之上，与 agent 进程构造
// 其子进程环境的方式一致。
//
// 用 map 而非单一 home 字符串，关键在"键是否存在"：若某 agent 把
// REASONIX_HOME="" 视为"显式覆盖为空"，子进程就会退回平台默认值——若此处
// 去读 daemon 自己的 REASONIX_HOME，指向的配置就会与任务真正使用的配置
// 不一致。
type reasonixEnv map[string]string

// lookup 按子进程的规则解析一个变量：agent 声明过就用其覆盖值，否则回退
// 到 daemon 自身环境。
func (e reasonixEnv) lookup(name string) string {
	if v, ok := e[name]; ok {
		return v
	}
	if reasonixGOOS == "windows" {
		// Windows 的环境变量名不区分大小写，那里的 os.Getenv 也一样；
		// agent 若写了 "APPDATA"，仍应覆盖 "AppData"。
		for k, v := range e {
			if strings.EqualFold(k, name) {
				return v
			}
		}
	}
	return os.Getenv(name)
}

// expandVars 替换 ${VAR} / ${VAR:-default}。未设置"或"为空时取默认值，
// 与 Reasonix 行为一致。
func (e reasonixEnv) expandVars(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return reasonixVarRef.ReplaceAllStringFunc(s, func(match string) string {
		groups := reasonixVarRef.FindStringSubmatch(match)
		if value := e.lookup(groups[1]); value != "" {
			return value
		}
		if groups[2] != "" {
			return groups[3]
		}
		return ""
	})
}

// cleanDir 按 Reasonix cleanEnvDir 的方式读取一个"目录型"变量：去空白、
// 变量展开、解析开头的 ~、转绝对路径、清理冗余段。
func (e reasonixEnv) cleanDir(name string) string {
	dir := strings.TrimSpace(e.lookup(name))
	if dir == "" {
		return ""
	}
	dir = e.expandVars(dir)
	if dir == "~" {
		if home := e.userHomeDir(); home != "" {
			dir = home
		}
	} else if strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		if home := e.userHomeDir(); home != "" {
			dir = filepath.Join(home, dir[2:])
		}
	}
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	return filepath.Clean(dir)
}

// userHomeDir 基于子进程的环境移植 os.UserHomeDir。
func (e reasonixEnv) userHomeDir() string {
	if reasonixGOOS == "windows" {
		return e.lookup("USERPROFILE")
	}
	return e.lookup("HOME")
}

// userConfigDir 基于子进程的环境移植 os.UserConfigDir。
func (e reasonixEnv) userConfigDir() string {
	switch reasonixGOOS {
	case "windows":
		return e.lookup("AppData")
	case "darwin", "ios":
		home := e.userHomeDir()
		if home == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support")
	default:
		if dir := e.lookup("XDG_CONFIG_HOME"); dir != "" {
			if !filepath.IsAbs(dir) {
				return "" // os.UserConfigDir 会拒绝相对路径的 XDG_CONFIG_HOME
			}
			return dir
		}
		home := e.userHomeDir()
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".config")
	}
}

// isolatedHome 对应 Reasonix 的 IsolatedHomeDir：REASONIX_HOME 非空即表示
// 一个自包含运行时，会关闭下面所有的传统回退。
func (e reasonixEnv) isolatedHome() string {
	return e.cleanDir("REASONIX_HOME")
}

// homeDir 对应 Reasonix 的 reasonixHomeDir。
func (e reasonixEnv) homeDir() string {
	if dir := e.isolatedHome(); dir != "" {
		return dir
	}
	if reasonixGOOS == "windows" {
		if dir := e.userConfigDir(); dir != "" {
			return filepath.Join(dir, "reasonix")
		}
		if home := e.userHomeDir(); home != "" {
			return filepath.Join(home, "AppData", "Roaming", "reasonix")
		}
		return ""
	}
	if home := e.userHomeDir(); home != "" {
		return filepath.Join(home, ".reasonix")
	}
	if dir := e.userConfigDir(); dir != "" {
		return filepath.Join(dir, "reasonix")
	}
	return ""
}

// userConfigPath 是 Reasonix home 下的主 config.toml。
func (e reasonixEnv) userConfigPath() string {
	dir := e.homeDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, reasonixUserConfigFile)
}

// legacyOSSupportDir 是"引入 Reasonix home 之前"的 OS 应用支持目录；当
// REASONIX_HOME 隔离了运行时、或该目录本就是当前 home 时返回空。
func (e reasonixEnv) legacyOSSupportDir() string {
	if e.isolatedHome() != "" {
		return ""
	}
	dir := e.userConfigDir()
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, "reasonix")
	if current := e.homeDir(); current != "" && reasonixSamePath(path, current) {
		return ""
	}
	return path
}

// legacyUserConfigPath 是旧版 OS 应用支持位置上的 config.toml。
func (e reasonixEnv) legacyUserConfigPath() string {
	dir := e.legacyOSSupportDir()
	if dir == "" {
		return ""
	}
	path := filepath.Join(dir, reasonixUserConfigFile)
	if primary := e.userConfigPath(); primary != "" && reasonixSamePath(path, primary) {
		return ""
	}
	return path
}

// legacyXDGConfigPaths 是被弃用的 XDG 位置，仅 Unix 且仅在 REASONIX_HOME
// 未设置时生效。
func (e reasonixEnv) legacyXDGConfigPaths() []string {
	if e.isolatedHome() != "" || reasonixGOOS == "windows" {
		return nil
	}
	seen := map[string]bool{}
	var paths []string
	add := func(path string) {
		if path == "" {
			return
		}
		path = filepath.Clean(path)
		if seen[path] {
			return
		}
		seen[path] = true
		paths = append(paths, path)
	}
	if dir := e.cleanDir("XDG_CONFIG_HOME"); dir != "" {
		add(filepath.Join(dir, "reasonix", reasonixUserConfigFile))
	}
	if home := e.userHomeDir(); home != "" {
		add(filepath.Join(home, ".config", "reasonix", reasonixUserConfigFile))
	}
	return paths
}

// userConfigLoadPath 返回子进程实际会读的 config.toml：主路径存在就用主
// 路径，否则用第一个存在的传统位置，再否则回到主路径（即它当前还不存在）。
// 移植自 userConfigLoadPath。
func (e reasonixEnv) userConfigLoadPath() string {
	primary := e.userConfigPath()
	if primary == "" {
		return e.legacyUserConfigPath()
	}
	if _, err := os.Stat(primary); err == nil {
		return primary
	}
	if legacy := e.legacyUserConfigPath(); legacy != "" {
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
	}
	for _, legacy := range e.legacyXDGConfigPaths() {
		if legacy == "" || reasonixSamePath(legacy, primary) {
			continue
		}
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
	}
	return primary
}

// reasonixSamePath 判断两个路径是否指向同一位置（都转绝对路径后按清理
// 结果比较）。
func reasonixSamePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if abs, err := filepath.Abs(a); err == nil {
		a = abs
	}
	if abs, err := filepath.Abs(b); err == nil {
		b = abs
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
