// 文件职责：实现 `agent copy` 命令——把既有 agent 的可移植配置克隆成一个全新
// agent（可换 runtime），源 agent 原样不动。它是网页端「Duplicate」动作的
// CLI/无头等价物（MUL-5279）。命令是对既有端点的薄组合——GET 源 agent、再 POST
// 一个 create——所以不需要专属 server API：`POST /api/agents` 本来就在与 agent
// 行相同的 DB 事务里绑定 skill_ids，克隆永远不会以半配置状态出现在人前。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/agentconfig"
	"github.com/multica-ai/multica/server/internal/cli"
)

// agentCopyCmd 是 `agent copy` 命令，复制语义详见其 Long 帮助文本
// （哪些字段默认带、哪些可覆盖、哪些永不复制、跨 runtime 时的取舍）。
var agentCopyCmd = &cobra.Command{
	Use:   "copy <source-agent-id>",
	Short: "Copy an existing agent into a new one (optionally on a different runtime)",
	Long: `Copy an existing agent's portable configuration into a brand-new agent.

The source agent is left untouched. By default the copy lands on the same
runtime as the source; pass --runtime-id to fork it onto a different runtime.

Copied by default without a dedicated override flag: conversation starters.

Copied by default, each overridable with the matching flag: name (suffixed
" (copy)"), description, instructions, avatar, custom_args, max_concurrent_tasks,
invocation permission (permission_mode + allow-list), assigned workspace skills,
and — only when the target runtime is unchanged — model, thinking_level and
service_tier.

Secret and machine-local fields are never copied: custom_env, mcp_config and
runtime_config. Supply fresh values for the copy with the same secret-safe flags
as 'agent create' when the target machine needs them.

Runtime-specific fields do not travel across a runtime change: when --runtime-id
selects a different runtime, --model is required (pass --model "" to accept the
target runtime's default), and thinking_level / service_tier are dropped unless
set explicitly.`,
	Args: exactArgs(1),
	RunE: runAgentCopy,
}

func init() {
	agentCmd.AddCommand(agentCopyCmd)
	registerAgentCopyFlags(agentCopyCmd)
}

// registerAgentCopyFlags 注册 runAgentCopy 读取的全部 flag。init 与测试共用，
// 保证两者永不走样。
func registerAgentCopyFlags(cmd *cobra.Command) {
	cmd.Flags().String("name", "", "Name for the new agent (default: \"<source name> (copy)\")")
	cmd.Flags().String("runtime-id", "", "Target runtime ID (default: the source agent's runtime). A different value forks the agent onto that runtime.")
	cmd.Flags().String("description", "", "Override the copied description")
	cmd.Flags().String("instructions", "", "Override the copied instructions")
	cmd.Flags().String("model", "", "Model identifier for the copy. Required when --runtime-id selects a different runtime (pass \"\" to accept the target runtime default). Empty otherwise = runtime default.")
	cmd.Flags().String("thinking-level", "", "Override thinking level. Not carried across a runtime change unless set here.")
	cmd.Flags().String("service-tier", "", "Override Codex service tier. Not carried across a runtime change unless set here.")
	cmd.Flags().String("custom-args", "", "Override custom CLI arguments as a JSON array.")
	cmd.Flags().Int32("max-concurrent-tasks", 6, "Override maximum concurrent tasks")
	cmd.Flags().String("visibility", "", "Override visibility: private or workspace (legacy; mapped to --permission-mode)")
	cmd.Flags().String("permission-mode", "", "Override invocation permission mode: private or public_to. Authoritative over --visibility.")
	cmd.Flags().Bool("public-to-workspace", false, "public_to: allow every workspace member to invoke the copy.")
	cmd.Flags().StringSlice("public-to-member", nil, "public_to: allow the given member user id(s) to invoke the copy. Repeatable.")
	cmd.Flags().Bool("no-skills", false, "Do not copy the source agent's workspace skill assignments.")
	// 机密/本机专属字段永不从源复制；这些 flag 为克隆体提供新值，镜像
	// 'agent create'。
	cmd.Flags().String("custom-env", "", "Set custom_env on the copy as a JSON object (never copied from the source). Prefer --custom-env-stdin/--custom-env-file for secrets. Pass '{}' for an empty map.")
	cmd.Flags().Bool("custom-env-stdin", false, "Read --custom-env from stdin. Mutually exclusive with --custom-env and --custom-env-file.")
	cmd.Flags().String("custom-env-file", "", "Read --custom-env from a file path (suggested mode: 0600). Mutually exclusive with --custom-env and --custom-env-stdin.")
	cmd.Flags().String("mcp-config", "", "Set mcp_config on the copy as a JSON object (never copied from the source). Prefer --mcp-config-stdin/--mcp-config-file for secrets.")
	cmd.Flags().Bool("mcp-config-stdin", false, "Read --mcp-config from stdin. Mutually exclusive with --mcp-config and --mcp-config-file.")
	cmd.Flags().String("mcp-config-file", "", "Read --mcp-config from a file path (suggested mode: 0600). Mutually exclusive with --mcp-config and --mcp-config-stdin.")
	cmd.Flags().String("runtime-config", "", "Set runtime_config on the copy as a JSON string (never copied from the source).")
	cmd.Flags().String("output", "json", "Output format: table or json")
}

// runAgentCopy 读源 agent 并把其可移植字段组装成 create 请求，再 POST 出去。
// custom_env/mcp_config/runtime_config 从源读不回来（GET 本就脱敏/掩码），只有
// 命令行显式提供时才设置。
func runAgentCopy(cmd *cobra.Command, args []string) error {
	var maxConcurrentTasksOverride *int32
	if cmd.Flags().Changed("max-concurrent-tasks") {
		v, _ := cmd.Flags().GetInt32("max-concurrent-tasks")
		if err := validateAgentMaxConcurrentTasksFlag(v); err != nil {
			return err
		}
		maxConcurrentTasksOverride = &v
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var src map[string]any
	if err := client.GetJSON(ctx, "/api/agents/"+args[0], &src); err != nil {
		return fmt.Errorf("get source agent: %w", err)
	}

	srcRuntimeID := strVal(src, "runtime_id")

	// 解析目标 runtime：默认跟随源 runtime，--runtime-id 覆盖；值不同即为
	// 跨 runtime fork。
	targetRuntimeID := srcRuntimeID
	if cmd.Flags().Changed("runtime-id") {
		v, _ := cmd.Flags().GetString("runtime-id")
		if v == "" {
			return fmt.Errorf("--runtime-id must not be empty")
		}
		targetRuntimeID = v
	}
	if targetRuntimeID == "" {
		return fmt.Errorf("source agent has no runtime; pass --runtime-id to choose a target runtime")
	}
	sameRuntime := targetRuntimeID == srcRuntimeID

	// 名字：默认 "<源名> (copy)"，--name 覆盖。
	name := strVal(src, "name") + " (copy)"
	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		if v == "" {
			return fmt.Errorf("--name must not be empty")
		}
		name = v
	}

	body := map[string]any{
		"name":       name,
		"runtime_id": targetRuntimeID,
	}

	// 纯文本字段：从源复制，用对应 flag 覆盖。
	body["description"] = strVal(src, "description")
	if cmd.Flags().Changed("description") {
		v, _ := cmd.Flags().GetString("description")
		body["description"] = v
	}
	body["instructions"] = strVal(src, "instructions")
	if cmd.Flags().Changed("instructions") {
		v, _ := cmd.Flags().GetString("instructions")
		body["instructions"] = v
	}
	if conversationStarters, ok := src["conversation_starters"].([]any); ok {
		body["conversation_starters"] = conversationStarters
	}

	// 头像引用跟着走（两个 agent 指向同一张已上传图片 URL），与网页端
	// Duplicate 流程一致。
	if av, ok := src["avatar_url"]; ok && av != nil {
		body["avatar_url"] = av
	}

	// custom_args：有就复制，--custom-args 覆盖。
	if ca, ok := src["custom_args"].([]any); ok && len(ca) > 0 {
		body["custom_args"] = ca
	}
	if cmd.Flags().Changed("custom-args") {
		v, _ := cmd.Flags().GetString("custom-args")
		ca, err := parseCustomArgs(v)
		if err != nil {
			return err
		}
		body["custom_args"] = ca
	}

	// max_concurrent_tasks：合法的源值可移植。历史行可能早于 1-50 的不变量时代；
	// 直接丢弃这些值，让 create 用默认值而不是把 Duplicate 变成一次 400。显式
	// flag 在任何 API 请求前校验、保持权威。
	if maxConcurrentTasksOverride != nil {
		body["max_concurrent_tasks"] = *maxConcurrentTasksOverride
	} else if v, ok := copiedAgentMaxConcurrentTasks(src["max_concurrent_tasks"]); ok {
		body["max_concurrent_tasks"] = v
	}

	// runtime 专属字段（model / thinking_level / service_tier）只在它们被选定的
	// runtime 上有意义。留在同 runtime 时照抄；fork 到别的 runtime 时丢弃并要求
	// 显式 --model（传 "" 接受目标默认）——镜像网页端 Duplicate 在切换 runtime
	// 时清空 model 的行为。
	if sameRuntime {
		if v := strVal(src, "model"); v != "" {
			body["model"] = v
		}
		if v := strVal(src, "thinking_level"); v != "" {
			body["thinking_level"] = v
		}
		if v := strVal(src, "service_tier"); v != "" {
			body["service_tier"] = v
		}
	} else if !cmd.Flags().Changed("model") {
		return fmt.Errorf("copying to a different runtime (--runtime-id) requires --model, because the source model may not exist on the target runtime; pass --model \"\" to accept the target runtime default")
	}
	if cmd.Flags().Changed("model") {
		v, _ := cmd.Flags().GetString("model")
		body["model"] = v
	}
	if cmd.Flags().Changed("thinking-level") {
		v, _ := cmd.Flags().GetString("thinking-level")
		body["thinking_level"] = v
	}
	if cmd.Flags().Changed("service-tier") {
		v, _ := cmd.Flags().GetString("service-tier")
		body["service_tier"] = v
	}

	// 调用权限：默认照抄源 agent 的 permission_mode + allow-list；一旦给了任何
	// 权限 flag（或旧版 --visibility）就改为完全由它定义——二者从不混用。
	permOverride := cmd.Flags().Changed("permission-mode") ||
		cmd.Flags().Changed("public-to-workspace") ||
		cmd.Flags().Changed("public-to-member") ||
		cmd.Flags().Changed("visibility")
	if permOverride {
		if cmd.Flags().Changed("visibility") {
			v, _ := cmd.Flags().GetString("visibility")
			body["visibility"] = v
		}
		applyAgentPermissionFlags(cmd, body)
	} else {
		if pm := strVal(src, "permission_mode"); pm != "" {
			body["permission_mode"] = pm
		}
		if it, ok := src["invocation_targets"]; ok && it != nil {
			body["invocation_targets"] = it
		}
	}

	// 工作区技能：照抄源绑定，让它们在同一 create 事务里挂上；除非 --no-skills。
	if noSkills, _ := cmd.Flags().GetBool("no-skills"); !noSkills {
		if skills, ok := src["skills"].([]any); ok {
			ids := make([]string, 0, len(skills))
			for _, s := range skills {
				m, ok := s.(map[string]any)
				if !ok {
					continue
				}
				if id := strVal(m, "id"); id != "" {
					ids = append(ids, id)
				}
			}
			if len(ids) > 0 {
				body["skill_ids"] = ids
			}
		}
	}

	// 机密/本机专属字段永不从源复制（GET 会脱敏/掩码）。仅当显式提供时才设置，
	// 通道与 'agent create' 的 secret-safe 一致。
	if ce, ok, err := resolveCustomEnv(cmd); err != nil {
		return err
	} else if ok {
		body["custom_env"] = ce
	}
	if mc, ok, err := resolveMcpConfig(cmd); err != nil {
		return err
	} else if ok {
		body["mcp_config"] = mc
	}
	if cmd.Flags().Changed("runtime-config") {
		v, _ := cmd.Flags().GetString("runtime-config")
		var rc any
		if err := json.Unmarshal([]byte(v), &rc); err != nil {
			return fmt.Errorf("--runtime-config must be valid JSON: %w", err)
		}
		body["runtime_config"] = rc
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/agents", body, &result); err != nil {
		return fmt.Errorf("copy agent: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}

	fmt.Printf("Agent copied: %s (%s)\n", strVal(result, "name"), strVal(result, "id"))
	return nil
}

// copiedAgentMaxConcurrentTasks 校验源 agent 的 max_concurrent_tasks 是否可移植
// （整数且在 1-50 合法区间内，规避历史越界行），合法才返回 (值, true)。
func copiedAgentMaxConcurrentTasks(value any) (int32, bool) {
	number, ok := value.(float64)
	if !ok || number != math.Trunc(number) ||
		number < float64(agentconfig.MinMaxConcurrentTasks) ||
		number > float64(agentconfig.MaxMaxConcurrentTasks) {
		return 0, false
	}
	value32 := int32(number)
	return value32, agentconfig.IsValidMaxConcurrentTasks(value32)
}
