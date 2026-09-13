// config.go 负责 cli（multica 命令行）与 daemon 的持久化配置：定义配置文件
// (~/.multica/config.json 或按 profile 分目录)的磁盘布局与读写，以及这份配置
// 的结构体 CLIConfig。daemon 通过一个私有的「每任务配置根目录」（见
// MULTICA_TASK_CONFIG_ROOT）隔离每次任务运行时能看到的配置，避免任务代码顺着
// HOME/XDG 找到 daemon 主人的 profile 凭证。
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultCLIConfigPath = ".multica/config.json"

	// TaskConfigRootEnv 让 daemon 管理的 CLI 调用指向「私有、按任务隔离」的 Multica
	// 配置目录。这个变量名刻意是 Multica 专属的：一期加固既要给工具链保留真实的
	// HOME/XDG 环境，又要阻止任务代码隐式发现 daemon 主人的 profile。
	TaskConfigRootEnv = "MULTICA_TASK_CONFIG_ROOT"
)

// CLIConfig 存放 CLI 的持久化设置。
type CLIConfig struct {
	ServerURL   string `json:"server_url,omitempty"`
	AppURL      string `json:"app_url,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Token       string `json:"token,omitempty"`

	// DeviceName 是用这个 profile 启动的 daemon 在服务器「Runtimes」界面里显示的
	// 人类可读名字。当同一台机器上有多个 daemon 用不同 profile 跑时（共享服务器上
	// 一个 Linux 用户只扛一个 profile 的常见形态），os.Hostname() 会把它们全折成
	// 一行无法区分的 "VM-…"。给每个 profile 配 device_name（通常设成
	// "<host>-<profile>" 或运维的真名）能让运行实例列表可辨认。
	//
	// 取值优先级（高者胜）：--device-name flag、MULTICA_DAEMON_DEVICE_NAME 环境
	// 变量、本字段、os.Hostname()。
	DeviceName string `json:"device_name,omitempty"`

	// RuntimeName 是 daemon 自己在 UI 里显示的运行时标签（"Runtime display name"）。
	// 形态和 DeviceName 一样，但作用域是「运行时行」而不是「主机行」：一个 profile
	// 下要跑多个相互独立的 daemon 时（少见但支持），可以给每个指定不同的名字。
	// 取值优先级（高者胜）：--runtime-name flag、MULTICA_AGENT_RUNTIME_NAME 环境
	// 变量、本字段、内置 DefaultRuntimeName。
	RuntimeName string `json:"runtime_name,omitempty"`

	// WorkspacesRoot 是 daemon 创建任务工作目录的根目录。按 profile 持久化，避免
	// 依赖进程级环境变量，同时保住 profile 之间的隔离。空串表示「没设——走环境变量/
	// 内置默认值」。取值优先级（高者胜）：--workspaces-root flag、
	// MULTICA_WORKSPACES_ROOT 环境变量、本字段、识别 profile 的内置默认值。
	WorkspacesRoot string `json:"workspaces_root,omitempty"`

	// MaxConcurrentTasks 限制 daemon 并行执行任务的数量。持久化在这里，省得每次
	// daemon 启动/自动重启都重传 --max-concurrent-tasks。0 表示「没设——走环境变量/
	// 内置默认值」。取值优先级（高者胜）：--max-concurrent-tasks flag、
	// MULTICA_DAEMON_MAX_CONCURRENT_TASKS 环境变量、本字段、默认值。
	MaxConcurrentTasks int `json:"max_concurrent_tasks,omitempty"`

	// PollInterval 是 daemon 轮询服务器取新任务的间隔（Go duration 字符串，例如
	// "10s"、"500ms"）。持久化一次的动机与 MaxConcurrentTasks 相同。空串("")表示
	// 「没设——走环境变量/内置默认值」。这里只接受严格为正的时长：`config set
	// poll_interval` 会在写入时拒绝非正值（包括 "0s"）与无法解析的 duration，所以
	// 能落到本字段的值一定格式正确。用 `config set poll_interval ""` 可清掉已持久化
	// 的值。取值优先级（高者胜）：--poll-interval flag、
	// MULTICA_DAEMON_POLL_INTERVAL 环境变量、本字段、DefaultPollInterval。
	PollInterval string `json:"poll_interval,omitempty"`

	// HeartbeatInterval 是 daemon 向服务器发心跳的间隔（Go duration 字符串）。持久化
	// 一次的动机同 PollInterval。空串表示「没设——走环境变量/内置默认值」；`config
	// set heartbeat_interval` 拒绝零与负时长。取值优先级：--heartbeat-interval flag、
	// MULTICA_DAEMON_HEARTBEAT_INTERVAL 环境变量、本字段、DefaultHeartbeatInterval。
	HeartbeatInterval string `json:"heartbeat_interval,omitempty"`

	// AgentTimeout 是单次 agent 运行的「墙钟时间」绝对上限（Go duration 字符串）。
	// 与其它时长旋钮不同，这里 "0s" 是有意义、非默认的值：它显式关闭上限，让单次
	// 运行只受「无活动看门狗」约束（见 DefaultAgentTimeout）。为了区分「没持久化」
	// 与「持久化成关闭状态」，这里用指针：nil=没设，非 nil=用这个字符串（可以是
	// "0s"）。`config set agent_timeout` 接受任意非负 Go duration；"" 清空持久化值。
	// 取值优先级：--agent-timeout flag（含显式 0）、MULTICA_AGENT_TIMEOUT 环境变量、
	// 本字段、DefaultAgentTimeout。
	AgentTimeout *string `json:"agent_timeout,omitempty"`

	// CodexSemanticInactivityTimeout 是 Codex 专用的「无语义活动」看门狗窗口
	// （Go duration 字符串）。持久化一次的语义同 PollInterval：空=没设，正数=用此值。
	// 取值优先级：--codex-semantic-inactivity-timeout flag、
	// MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT 环境变量、本字段、
	// DefaultCodexSemanticInactivityTimeout。
	CodexSemanticInactivityTimeout string `json:"codex_semantic_inactivity_timeout,omitempty"`

	// CodexHandshakeTimeout 给 Codex app-server 的启动 RPC 设上限（Go duration
	// 字符串）。持久化一次的语义同 PollInterval。取值优先级：
	// --codex-handshake-timeout flag、MULTICA_CODEX_HANDSHAKE_TIMEOUT 环境变量、
	// 本字段、DefaultCodexHandshakeTimeout。
	CodexHandshakeTimeout string `json:"codex_handshake_timeout,omitempty"`

	// DisableAutoUpdate 为 true 时，关掉 daemon 周期性的 CLI 自更新轮询。它只允许
	// 单向持久化——--no-auto-update flag 同样是单向的——因为环境变量/默认值在
	// Multica Cloud 上已经解析成「启用」。缺省/false 表示「交给环境变量/默认值决定」。
	// 取值优先级：--no-auto-update flag、MULTICA_DAEMON_AUTO_UPDATE=false 环境变量、
	// 本字段、云端/自托管默认值。
	DisableAutoUpdate bool `json:"disable_auto_update,omitempty"`

	// AutoUpdateCheckInterval 是 daemon 轮询 GitHub 看有没有新 CLI 版本的间隔
	// （Go duration 字符串）。持久化一次的语义同 PollInterval。取值优先级：
	// --auto-update-interval flag、MULTICA_DAEMON_AUTO_UPDATE_INTERVAL 环境变量、
	// 本字段、DefaultAutoUpdateCheckInterval。
	AutoUpdateCheckInterval string `json:"auto_update_check_interval,omitempty"`

	// DisableAutoReload 为 true 时，daemon 不再在磁盘上的 multica 二进制被带外替换
	// 后自动重启成新版本。与 DisableAutoUpdate 一样是单向的，并且刻意与它分开：
	// 「别从 GitHub 拉新版本」和「别追随我自己装的二进制」是两个不同的决定。
	// 取值优先级：--no-auto-reload flag、MULTICA_DAEMON_AUTO_RELOAD=false 环境变量、
	// 本字段、默认值（启用）。
	DisableAutoReload bool `json:"disable_auto_reload,omitempty"`

	// ProfileCommandOverrides 是「每机器」的自定义运行时映射：profile_id -> 可执行
	// 文件绝对路径（MUL-3284）。workspace 自定义运行时 profile 里记的是 command_name
	// （daemon 到 PATH 上解析），但同一个逻辑 profile 在不同机器上可能装在别的路径
	// （甚至根本不在 PATH 上）。这张表让运维在特定主机上用 `multica runtime profile
	// set-path` 固定该 profile 的精确二进制；daemon 在 appendProfileRuntimes 里优先
	// 用它、其次才是 PATH 查找。空/缺省表示「到 PATH 上解析该 profile 的
	// command_name」——默认行为。这张映射刻意只留在本机（绝不上传服务器），因为路径
	// 属于这台机器的属性，不属于共享的 profile。
	ProfileCommandOverrides map[string]string `json:"profile_command_overrides,omitempty"`
}

// CLIConfigPath 返回 CLI 配置文件（默认 profile）的路径。
func CLIConfigPath() (string, error) {
	return CLIConfigPathForProfile("")
}

// CLIConfigPathForProfile 返回给定 profile 的配置文件路径。空 profile 用默认路径
// (~/.multica/config.json)；具名 profile 用 ~/.multica/profiles/<name>/config.json。
// 当 daemon 设置了 TaskConfigRootEnv 时，同一套 profile 布局会直接放在那个私有任务
// 目录下，而不是用户 home 目录。
func CLIConfigPathForProfile(profile string) (string, error) {
	root, taskLocal, err := multicaConfigRoot()
	if err != nil {
		return "", fmt.Errorf("resolve CLI config path: %w", err)
	}
	if taskLocal {
		if err := validateTaskLocalProfile(profile); err != nil {
			return "", fmt.Errorf("resolve CLI config path: %w", err)
		}
	}
	if profile == "" {
		if taskLocal {
			return filepath.Join(root, "config.json"), nil
		}
		return filepath.Join(root, defaultCLIConfigPath), nil
	}
	if taskLocal {
		return filepath.Join(root, "profiles", profile, "config.json"), nil
	}
	return filepath.Join(root, ".multica", "profiles", profile, "config.json"), nil
}

// ProfileDir 返回某个 profile 的状态文件（pid、log）所在目录。空 profile 返回
// ~/.multica/；具名 profile 返回 ~/.multica/profiles/<name>/。任务场景下的等效路径
// 解析到 TaskConfigRootEnv 之下。
func ProfileDir(profile string) (string, error) {
	root, taskLocal, err := multicaConfigRoot()
	if err != nil {
		return "", fmt.Errorf("resolve profile dir: %w", err)
	}
	if taskLocal {
		if err := validateTaskLocalProfile(profile); err != nil {
			return "", fmt.Errorf("resolve profile dir: %w", err)
		}
	}
	if profile == "" {
		if taskLocal {
			return root, nil
		}
		return filepath.Join(root, ".multica"), nil
	}
	if taskLocal {
		return filepath.Join(root, "profiles", profile), nil
	}
	return filepath.Join(root, ".multica", "profiles", profile), nil
}

// multicaConfigRoot 决定配置文件的「根」：若设了 MULTICA_TASK_CONFIG_ROOT（daemon
// 任务私有目录），根就是它且 taskLocal=true（要求绝对路径）；否则根是当前用户 home
// 目录。返回的 taskLocal 供调用方决定是否要走 task 特化的路径布局与权限收紧。
func multicaConfigRoot() (root string, taskLocal bool, err error) {
	if rawRoot := strings.TrimSpace(os.Getenv(TaskConfigRootEnv)); rawRoot != "" {
		root := filepath.Clean(rawRoot)
		if !filepath.IsAbs(root) {
			return "", false, fmt.Errorf("%s must be an absolute path", TaskConfigRootEnv)
		}
		return root, true, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	return home, false, nil
}

// validateTaskLocalProfile 校验 task 场景下的 profile 名不能逃出任务目录（禁止
// "."/".."、绝对路径、含路径分隔符或不是干净路径的名字）。
func validateTaskLocalProfile(profile string) error {
	if profile == "" {
		return nil
	}
	if profile == "." || profile == ".." || filepath.IsAbs(profile) || strings.ContainsAny(profile, `/\\`) || filepath.Clean(profile) != profile {
		return fmt.Errorf("invalid task-local Multica profile name %q", profile)
	}
	return nil
}

// LoadCLIConfig 从磁盘读 CLI 配置（默认 profile）。
func LoadCLIConfig() (CLIConfig, error) {
	return LoadCLIConfigForProfile("")
}

// LoadCLIConfigForProfile 读指定 profile 的 CLI 配置。配置文件不存在时返回零值配置
// （不算错误，首次使用即可写）。
func LoadCLIConfigForProfile(profile string) (CLIConfig, error) {
	path, err := CLIConfigPathForProfile(profile)
	if err != nil {
		return CLIConfig{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CLIConfig{}, nil
		}
		return CLIConfig{}, fmt.Errorf("read CLI config: %w", err)
	}
	var cfg CLIConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return CLIConfig{}, fmt.Errorf("parse CLI config: %w", err)
	}
	return cfg, nil
}

// SaveCLIConfig 把 CLI 配置原子地写盘（默认 profile）。
func SaveCLIConfig(cfg CLIConfig) error {
	return SaveCLIConfigForProfile(cfg, "")
}

// SaveCLIConfigForProfile 把指定 profile 的 CLI 配置写盘。task 私有目录场景下会把
// 一路目录权限收紧到 0o700，防止同一台机器上的其它任务/用户读到配置里的令牌。
func SaveCLIConfigForProfile(cfg CLIConfig, profile string) error {
	path, err := CLIConfigPathForProfile(profile)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	dirMode := os.FileMode(0o755)
	if strings.TrimSpace(os.Getenv(TaskConfigRootEnv)) != "" {
		dirMode = 0o700
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("create CLI config directory: %w", err)
	}
	// task 场景下把从配置目录一路到 task 根目录的所有祖先目录都收紧成 0o700，
	// 保证私有目录链上任何一级都不会被其它用户穿透。
	if dirMode == 0o700 {
		root, _, err := multicaConfigRoot()
		if err != nil {
			return fmt.Errorf("resolve task-local CLI config root: %w", err)
		}
		for current := dir; ; current = filepath.Dir(current) {
			if err := os.Chmod(current, 0o700); err != nil {
				return fmt.Errorf("restrict task-local CLI config directory: %w", err)
			}
			if current == root {
				break
			}
			parent := filepath.Dir(current)
			if parent == current {
				return fmt.Errorf("task-local CLI config directory %q escapes root %q", dir, root)
			}
		}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode CLI config: %w", err)
	}

	// 先在同目录写临时文件再 rename，保证「要么是旧完整内容、要么是新完整内容」，
	// 进程在写一半时崩溃也不会留下半个损坏的配置文件。
	tmp, err := os.CreateTemp(dir, ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp config file: %w", err)
	}
	// 配置文件里可能含令牌，落盘权限收紧成 0600（仅本人可读写）。
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp config file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename config file: %w", err)
	}
	return nil
}
