// 文件职责：实现 `multica config` 命令（show/set）——读写 CLI 本地配置
// config.json。配置既存于单机文件也存于 profile 目录（见 cli.LoadCLIConfigForProfile），
// 其中一批「daemon 键」（poll_interval、agent_timeout 等）与 `daemon start` 的
// --flag/环境变量互为镜像：优先级为 --flag > MULTICA_… env > config.json > 内置默认。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/openviking"
)

// configCmd 是 `multica config` 父命令，直接执行时等价于 `config show`。
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage configuration for multica",
	RunE:  runConfigShow,
}

// configShowCmd 显示当前生效的 CLI 配置。
var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current CLI configuration",
	RunE:  runConfigShow,
}

// configSetSupportedKeys 是 `config set` 支持的白名单，同时被其 switch 分支和
// --help 文案引用，保证加新 key 时校验、报错文本、文档三处只改一处。顺序与
// `config show` 的输出一致。
var configSetSupportedKeys = []string{
	"server_url",
	"app_url",
	"workspace_id",
	"device_name",
	"runtime_name",
	"workspaces_root",
	"max_concurrent_tasks",
	"poll_interval",
	"heartbeat_interval",
	"agent_timeout",
	"codex_semantic_inactivity_timeout",
	"codex_handshake_timeout",
	"disable_auto_update",
	"auto_update_check_interval",
	"disable_auto_reload",
}

// configSetCmd 是 `config set <key> <value>` 命令，详细语义见其 Long 帮助文本
// （key 清单、优先级、duration 规则、disable_* 单向开关、空串清除等）。
var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a CLI configuration value",
	Long: "Supported keys: " +
		"server_url, app_url, workspace_id, " +
		"device_name, runtime_name, workspaces_root, max_concurrent_tasks, poll_interval, " +
		"heartbeat_interval, agent_timeout, " +
		"codex_semantic_inactivity_timeout, codex_handshake_timeout, " +
		"disable_auto_update, auto_update_check_interval, disable_auto_reload.\n\n" +
		"The daemon keys (device_name, runtime_name, workspaces_root, max_concurrent_tasks, " +
		"poll_interval, heartbeat_interval, agent_timeout, " +
		"codex_semantic_inactivity_timeout, codex_handshake_timeout, " +
		"disable_auto_update, auto_update_check_interval, disable_auto_reload) mirror their " +
		"--flag / env counterparts and are read by `daemon start` when " +
		"neither the flag nor the env var is set. " +
		"Precedence: --flag > MULTICA_… env > config.json > built-in default. " +
		"Duration keys take a positive Go duration (e.g. '10s', '500ms', '1m30s'); " +
		"'0s' and negative values are rejected — except agent_timeout, where " +
		"'0s' is meaningful and explicitly disables the wall-clock cap. " +
		"disable_auto_update and disable_auto_reload take 'true' or 'false' " +
		"(single-direction: setting one to 'true' turns that behavior off, " +
		"'false' clears the override so env/default decides). Pass an empty " +
		"string to clear a persisted " +
		"value (e.g. `config set poll_interval \"\"`).",
	Args: exactArgs(2),
	RunE: runConfigSet,
}

func init() {
	configCmd.AddCommand(&cobra.Command{Use: "check-openviking", Short: "Validate OpenViking environment isolation without sending requests", RunE: func(cmd *cobra.Command, args []string) error { return openviking.ValidateEnvironment() }})
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configSetCmd)
}

// runConfigShow 执行 `config`（或 `config show`）：要求先确定配置根目录（拒绝在
// 未定位配置目录时运行），按 profile 加载配置，把各项逐个打印到 stdout。
func runConfigShow(cmd *cobra.Command, _ []string) error {
	if err := requireTaskLocalConfigRoot(); err != nil {
		return err
	}
	profile := resolveProfile(cmd)
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		return err
	}

	path, _ := cli.CLIConfigPathForProfile(profile)
	fmt.Fprintf(os.Stdout, "Config file: %s\n", path)
	if profile != "" {
		fmt.Fprintf(os.Stdout, "Profile:      %s\n", profile)
	}
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "server_url:", valueOrDefault(cfg.ServerURL, "(not set)"))
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "app_url:", valueOrDefault(cfg.AppURL, "(not set)"))
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "workspace_id:", valueOrDefault(cfg.WorkspaceID, "(not set)"))
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "device_name:", valueOrDefault(cfg.DeviceName, "(not set)"))
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "runtime_name:", valueOrDefault(cfg.RuntimeName, "(not set)"))
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "workspaces_root:", valueOrDefault(cfg.WorkspacesRoot, "(not set)"))
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "max_concurrent_tasks:", intOrDefault(cfg.MaxConcurrentTasks, "(not set)"))
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "poll_interval:", valueOrDefault(cfg.PollInterval, "(not set)"))
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "heartbeat_interval:", valueOrDefault(cfg.HeartbeatInterval, "(not set)"))
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "agent_timeout:", agentTimeoutDisplay(cfg.AgentTimeout))
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "codex_semantic_inactivity_timeout:", valueOrDefault(cfg.CodexSemanticInactivityTimeout, "(not set)"))
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "codex_handshake_timeout:", valueOrDefault(cfg.CodexHandshakeTimeout, "(not set)"))
	fmt.Fprintf(os.Stdout, "%-34s %t\n", "disable_auto_update:", cfg.DisableAutoUpdate)
	fmt.Fprintf(os.Stdout, "%-34s %s\n", "auto_update_check_interval:", valueOrDefault(cfg.AutoUpdateCheckInterval, "(not set)"))
	fmt.Fprintf(os.Stdout, "%-34s %t\n", "disable_auto_reload:", cfg.DisableAutoReload)
	return nil
}

// runConfigSet 执行 `config set <key> <value>`：先加载现有配置，applyConfigSet
// 就地改值并校验，通过后才落盘保存（校验失败绝不写盘）。workspaces_root 会先
// 转成绝对路径再存，所以成功提示里回显的是规范化后的值。
func runConfigSet(cmd *cobra.Command, args []string) error {
	if err := requireTaskLocalConfigRoot(); err != nil {
		return err
	}
	key, value := args[0], args[1]

	profile := resolveProfile(cmd)
	cfg, err := cli.LoadCLIConfigForProfile(profile)
	if err != nil {
		return err
	}

	if err := applyConfigSet(&cfg, key, value); err != nil {
		return err
	}

	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		return err
	}

	storedValue := value
	if key == "workspaces_root" {
		storedValue = cfg.WorkspacesRoot
	}
	fmt.Fprintf(os.Stderr, "Set %s = %s\n", key, storedValue)
	return nil
}

// applyConfigSet 按 (key, value) 就地改 cfg，返回 nil 表示改动完成。从
// runConfigSet 拆出来是为了让测试不碰磁盘也能覆盖各校验分支。未知 key / 非法值
// 都报错；调用方只在返回 nil 时才保存。
//
// 校验规则把「落盘配置的合法性」卡在写入时刻，这样 daemon 每次启动不必复查：
// 空 duration 串视为「清除该字段」（与 `config set server_url ""` 清 URL 一致），
// 无法解析的 duration 提前拒绝，而不是先存进去等运行时报错。
func applyConfigSet(cfg *cli.CLIConfig, key, value string) error {
	switch key {
	case "server_url":
		cfg.ServerURL = value
	case "app_url":
		cfg.AppURL = value
	case "workspace_id":
		cfg.WorkspaceID = value
	case "device_name":
		cfg.DeviceName = value
	case "runtime_name":
		cfg.RuntimeName = value
	case "workspaces_root":
		value = strings.TrimSpace(value)
		if value == "" {
			cfg.WorkspacesRoot = ""
			return nil
		}
		root, err := filepath.Abs(value)
		if err != nil {
			return fmt.Errorf("resolve workspaces_root: %w", err)
		}
		cfg.WorkspacesRoot = root
	case "max_concurrent_tasks":
		if value == "" {
			cfg.MaxConcurrentTasks = 0
			return nil
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("max_concurrent_tasks must be an integer: %w", err)
		}
		if n < 0 {
			return fmt.Errorf("max_concurrent_tasks must be >= 0 (got %d)", n)
		}
		cfg.MaxConcurrentTasks = n
	case "poll_interval":
		if value == "" {
			cfg.PollInterval = ""
			return nil
		}
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("poll_interval must be a Go duration (e.g. 10s, 500ms): %w", err)
		}
		// 拒绝 0 与负数 duration。把 "0s" 存进去会让 `config show` 看着已配置、
		// 而 daemon 启动时被静默忽略（解析器只替换严格正值）——正是 #3824 评审中
		// 指出的坑。空串是清除已存值的唯一方式。
		if d <= 0 {
			return fmt.Errorf("poll_interval must be positive (got %s); use `config set poll_interval \"\"` to clear it", d)
		}
		cfg.PollInterval = value
	case "heartbeat_interval":
		if err := assignPositiveDuration(&cfg.HeartbeatInterval, key, value); err != nil {
			return err
		}
	case "agent_timeout":
		// agent_timeout 是唯一允许 "0s" 的 duration 键——它显式关闭墙钟上限
		// （见 cli.CLIConfig.AgentTimeout）。经指针存原始串，从而区分「未设置」
		// （nil）与「显式禁用」（非 nil 且为 "0s"）以及任意正数。
		if value == "" {
			cfg.AgentTimeout = nil
			return nil
		}
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("agent_timeout must be a Go duration (e.g. 10m, 0s to disable): %w", err)
		}
		if d < 0 {
			return fmt.Errorf("agent_timeout must be >= 0 (got %s); use 0s to disable the cap or \"\" to clear the persisted value", d)
		}
		s := value
		cfg.AgentTimeout = &s
	case "codex_semantic_inactivity_timeout":
		if err := assignPositiveDuration(&cfg.CodexSemanticInactivityTimeout, key, value); err != nil {
			return err
		}
	case "codex_handshake_timeout":
		if err := assignPositiveDuration(&cfg.CodexHandshakeTimeout, key, value); err != nil {
			return err
		}
	case "disable_auto_update":
		if err := assignBool(&cfg.DisableAutoUpdate, key, value); err != nil {
			return err
		}
	case "auto_update_check_interval":
		if err := assignPositiveDuration(&cfg.AutoUpdateCheckInterval, key, value); err != nil {
			return err
		}
	case "disable_auto_reload":
		if err := assignBool(&cfg.DisableAutoReload, key, value); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown config key %q (supported: %s)", key, joinKeys(configSetSupportedKeys))
	}
	return nil
}

// assignBool 把 value 严格解析成 bool 写进 dst，供两个 disable_* 开关共用。
// 空串清除该字段（置 false）。
func assignBool(dst *bool, key, value string) error {
	if value == "" {
		*dst = false
		return nil
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("%s must be 'true' or 'false' (got %q)", key, value)
	}
	*dst = b
	return nil
}

// assignPositiveDuration 把 value 解析成严格正的 Go duration 并写原始串进 dst。
// 被除 agent_timeout 外的所有持久化 daemon duration 键共用——只有 agent_timeout
// 的零值有意义。空串清除字段。
func assignPositiveDuration(dst *string, key, value string) error {
	if value == "" {
		*dst = ""
		return nil
	}
	normalized := strings.TrimSpace(value)
	d, err := time.ParseDuration(normalized)
	if err != nil {
		return fmt.Errorf("%s must be a Go duration (e.g. 10s, 500ms): %w", key, err)
	}
	if d <= 0 {
		return fmt.Errorf("%s must be positive (got %s); use `config set %s \"\"` to clear it", key, d, key)
	}
	*dst = normalized
	return nil
}

// agentTimeoutDisplay 渲染 agent_timeout 的三态值供 `config show`：nil = 未持久化
// （回落 env/默认）；非 nil "0s" = 显式禁用；其它非 nil = 已持久化的 duration 串。
func agentTimeoutDisplay(v *string) string {
	if v == nil {
		return "(not set)"
	}
	if *v == "" {
		return "(not set)"
	}
	if d, err := time.ParseDuration(*v); err == nil && d == 0 {
		return *v + " (disabled)"
	}
	return *v
}

// valueOrDefault 空串时返回 fallback（表格展示 "(not set)" 用）。
func valueOrDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// intOrDefault 0 值时返回 fallback（表格展示 "(not set)" 用）。
func intOrDefault(v int, fallback string) string {
	if v == 0 {
		return fallback
	}
	return strconv.Itoa(v)
}

// joinKeys 把支持键清单拼成 "a, b, c" 供报错信息展示。手写小循环避免为
// strings.Join 绕 Sprintf 的折腾。
func joinKeys(keys []string) string {
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += k
	}
	return out
}
