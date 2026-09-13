// Reasonix 运行时的权限处理：为每次任务生成一份 reasonix.toml，在"照搬
// 运行归属用户自己的 [permissions] 规则"的基础上，把 `ask` 工具加入 deny——
// 无人值守的任务里没有人能回答问题，而 Reasonix 把无人应答视为放弃、直接
// 取消整轮。拒绝该工具则让模型自己决策并继续干活。

package execenv

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// reasonixProjectConfigFile 是 Reasonix 的"项目级"配置文件名。Reasonix 的
// 配置优先级为 flag > 项目 ./reasonix.toml > 用户 config.toml > 默认值，
// 且以会话 cwd（对任务而言即工作目录）为根解析——所以写在这里的配置只对
// 本任务生效，不会影响运行归属用户自己的交互式会话。
const reasonixProjectConfigFile = "reasonix.toml"

// reasonixUserConfigFile 是运行归属用户自己的 Reasonix 配置；其位置在
// reasonix_user_config.go 里解析。该文件永远不会被写——daemon 只读取
// 用户在此设置的权限。
const reasonixUserConfigFile = "config.toml"

// reasonixAskTool 是每任务配置要拒绝的 Reasonix 工具。
const reasonixAskTool = "ask"

// reasonixProjectConfigHeader 是渲染出的 [permissions] 表之前的说明头。
//
// `ask` 会在一轮对话中途向人类抛出一道选择题。无人值守的任务没有人类：
// Reasonix 会给每个 ACP 会话都接一个 asker，于是运行时自带的"没有交互式
// 用户——自己拿主意吧"兜底根本够不到，daemon 只能拒绝这个问题，而 Reasonix
// 把"被拒"当作"被 dismiss"，进而取消整轮。任务就为一个"模型本可自己拍板"
// 的决定而失败。
//
// deny 规则是唯一能覆盖"整轮"的杠杆。Reasonix 在"只读的 allow 兜底放行"
// 之前先检查 deny 规则，所以即使 `ask` 是只读的，deny 也能约束到它；并且
// executor、planner 与子 agent 都依据同一份策略做决定——而工具调用钩子只能
// 够到 executor。模型拿到"被权限策略拒绝"就像拿到普通工具结果一样，继续干活。
const reasonixProjectConfigHeader = `# Managed by Multica. Written per task, removed when the task env is cleaned up.
# Edits are not preserved.
#
# [permissions] restates the runtime owner's own table from the Reasonix user
# config, with the ask tool added to deny: no human can answer a question in an
# unattended task, and an unanswered question cancels the Reasonix turn. Denying
# the tool instead lets the model take its own decision and keep going.

`

// reasonixOwnerPermissions 返回运行归属用户配置的 [permissions] 表；
// 没有配置或没有该表时返回 nil。"文件存在但读不了/解析不了"视为错误：
// 调用方绝不能写出一个"并非从用户表派生"的权限表，因为项目文件是"替换"
// 用户的规则而不是"追加"到用户规则之上。
func reasonixOwnerPermissions(path string) (map[string]any, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read reasonix user config %s: %w", path, err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse reasonix user config %s: %w", path, err)
	}
	raw, ok := cfg["permissions"]
	if !ok || raw == nil {
		return nil, nil
	}
	perms, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("reasonix user config %s: [permissions] is %T, want a table", path, raw)
	}
	return perms, nil
}

// withReasonixAskDenied 复制用户的权限表并把 `ask` 加进其 deny 列表。
// 整张表都要搬过来而不只是 deny：项目文件会覆盖它声明的所有键，所以无论
// Reasonix 是按键还是按表合并这些层，把用户设置过的每个键都复述一遍才能
// 保住他们的策略。
func withReasonixAskDenied(ownerPermissions map[string]any) (map[string]any, error) {
	deny, err := reasonixDenyList(ownerPermissions["deny"])
	if err != nil {
		return nil, err
	}
	merged := make(map[string]any, len(ownerPermissions)+1)
	for k, v := range ownerPermissions {
		merged[k] = v
	}
	for _, rule := range deny {
		if rule == reasonixAskTool {
			// 用户本来就已经禁了 ask，无需重复添加。
			merged["deny"] = deny
			return merged, nil
		}
	}
	merged["deny"] = append(deny, reasonixAskTool)
	return merged, nil
}

// reasonixDenyList 把用户的 deny 规则读成字符串切片。形状无法如实复述的
// 一律报错，而不是静默重置为 ["ask"]——那会把用户写下的每条规则都丢掉。
func reasonixDenyList(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	entries, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("reasonix user config: [permissions] deny is %T, want an array of strings", raw)
	}
	rules := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		rule, ok := entry.(string)
		if !ok {
			return nil, fmt.Errorf("reasonix user config: [permissions] deny entry is %T, want a string", entry)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// reasonixProjectConfig 基于 userConfigPath（可以不存在）处的用户配置，
// 渲染出本任务的 reasonix.toml 内容。
func reasonixProjectConfig(userConfigPath string) ([]byte, error) {
	ownerPermissions, err := reasonixOwnerPermissions(userConfigPath)
	if err != nil {
		return nil, err
	}
	permissions, err := withReasonixAskDenied(ownerPermissions)
	if err != nil {
		return nil, err
	}
	body, err := toml.Marshal(map[string]any{"permissions": permissions})
	if err != nil {
		return nil, fmt.Errorf("encode reasonix project config: %w", err)
	}
	return append([]byte(reasonixProjectConfigHeader), body...), nil
}

// writeReasonixProjectConfig 在 workDir 落下本任务的 reasonix.toml，在照搬
// 运行归属用户自己权限的基础上额外禁用 `ask` 工具。taskEnv 是 agent 的
// （已净化）custom_env，它连同 daemon 自身环境一起决定 Reasonix 子进程会
// 加载哪份用户配置（见 reasonix_user_config.go）。
//
// 仓库自带的 reasonix.toml 一律不碰：sidecar 清理是"纯删除 daemon 创建的
// 路径"，所以绝不该覆盖用户内容。这种情况记警告而非失败——任务照常运行，
// `ask` 保持可用，若真有提问进来，会命中 reasonix 后端已有的 fail-closed
// 权限处理。
//
// 用户的配置读不了/无法复述时同理：daemon 宁可什么都不写，也不写一张
// 会悄悄丢掉用户 deny 规则的表。
func writeReasonixProjectConfig(workDir string, taskEnv map[string]string, manifest *sidecarManifest, logger *slog.Logger) error {
	if workDir == "" {
		return nil
	}
	userConfig := reasonixEnv(taskEnv).userConfigLoadPath()
	content, err := reasonixProjectConfig(userConfig)
	if err != nil {
		if logger != nil {
			logger.Warn("execenv: cannot restate the reasonix user permissions; leaving the task without a project config — the reasonix ask tool stays enabled for this task",
				"user_config", userConfig,
				"error", err,
			)
		}
		return nil
	}
	path := filepath.Join(workDir, reasonixProjectConfigFile)
	err = recordWriteFile(path, content, 0o644, manifest)
	if errors.Is(err, errPathPreExists) {
		if logger != nil {
			logger.Warn("execenv: project reasonix.toml already exists; leaving it untouched — the reasonix ask tool stays enabled for this task",
				"path", path,
			)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", reasonixProjectConfigFile, err)
	}
	return nil
}
