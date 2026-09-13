// plugin_agent_tools.go —— `agent` 触发器：把插件 hook 作为 MCP 工具暴露给 agent 调用（经服务端签名执行）。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

// `agent` 触发器：把插件 hook 作为 MCP 工具交给 agent。
//
// 这是第四个调用点，也是唯一不是“宿主决定调用”的那个：agent 看到工具、读描述、自主选择。正是这个选择
// 让 hook 有可能从 agent 侧被触达——另一方案（每回合前后都必须跑的 hook）等于让第三方握住产品主循环，
// 本设计中任何地方都没有这种位置。
//
// daemon 只把它们渲染成工具，【不】调用插件。工具调用回到服务器，由服务器执行签名请求。签名密钥由部署密钥
// 派生、绝不离开本进程；同样重要的是，经服务器路由意味着限流、熔断、`net:` 目的地检查与调用记录都对它生效，
// 与其它每个触发器完全一致，而不是在 daemon 侧重造并漂移。

// PluginHookTool 是一个 hook，以 agent 将看到的方式描述。
type PluginHookTool struct {
	InstallationID string `json:"installation_id"`
	HookKey        string `json:"hook_key"`
	// Name 是 agent 看到的工具名，带命名空间使两个插件贡献同名 hook key 也不冲突。
	Name string `json:"name"`
	// Description 是 manifest 的 hook 描述原样——agent 读它来决定是否调用，因此由插件作者撰写、宿主不转述。
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// toolNameUnsafe 匹配 MCP 工具名不应包含的一切。各家 agent/provider 接受范围不一；字母、数字、下划线是
// 到处可用的交集。
var toolNameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

// PluginToolName 为 hook 加命名空间，使两个插件都能贡献名为 “summarize” 的 hook 而不互相遮蔽。
//
// 朴素做法——两半各自清洗后以下划线连接——不是单射，测试抓到了它：插件 key 用 `.` 与 `-`，两者都得变 `_`，
// 于是 `a.b` 与 `a-b` 坍缩成同一个，`a.b_`+`c` 与 `a.b`+`c` 也碰撞。两个不同插件会给 agent 同一个工具名，
// 后注册的会替两者应答。
//
// 因此前缀携带完整插件 key 的短摘要，而非有损音译。可读部分是最后一段（人能认出）；摘要负责唯一。
//
// `__` 分隔符安全：hook key 不可能包含它——其模式要求每个下划线后跟字母数字，双下划线不可表示。
func PluginToolName(pluginKey, hookKey string) string {
	clean := func(value string) string {
		return strings.Trim(toolNameUnsafe.ReplaceAllString(value, "_"), "_")
	}
	segments := strings.Split(pluginKey, ".")
	readable := clean(segments[len(segments)-1])
	if readable == "" {
		readable = "plugin"
	}
	digest := sha256.Sum256([]byte(pluginKey))
	return readable + "_" + hex.EncodeToString(digest[:])[:6] + "__" + clean(hookKey)
}

// AgentHookTools 列出本工作区运行中的 agent 可以调用的 hook。
//
// 跳过错用安装，这正是“禁用插件在下次任务生效、而非只在 UI 生效”的机制。卸载的插件根本没有行。
func (s *PluginService) AgentHookTools(ctx context.Context, workspaceID pgtype.UUID) ([]PluginHookTool, error) {
	installations, err := s.Queries.ListWorkspacePluginInstallations(ctx, workspaceID)
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorUnavailable, Message: "list plugin installations", Err: err}
	}

	tools := make([]PluginHookTool, 0)
	for _, installation := range installations {
		if !installation.Enabled {
			continue
		}
		manifest, err := ParseInstallationManifest(installation)
		if err != nil {
			// 一份读不了的 manifest 不能藏起其它所有插件的工具。
			continue
		}
		for _, hook := range manifest.Contributes.Hooks {
			if !HookAllowsTrigger(hook, plugincontract.TriggerAgent) {
				continue
			}
			tools = append(tools, PluginHookTool{
				InstallationID: uuidString(installation.ID),
				HookKey:        hook.Key,
				Name:           PluginToolName(manifest.Key, hook.Key),
				Description:    hook.Description,
				InputSchema:    hook.InputSchema,
			})
		}
	}
	// 稳定排序，使任务工具列表在两次认领之间不会无故重排——那会表现为 provider 侧提示缓存的搅动。
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, nil
}

// InvokeAgentHook 执行一次 agent 触发的 hook。
//
// actor 是 AGENT——不是提交 issue 的人，也不是插件：agent 选择调用它，因此它产生的写入归 agent，
// 与 agent 直接写出时完全一样。author_type 已有对应值，这也是该触发器无需新值的原因。
func (s *PluginService) InvokeAgentHook(ctx context.Context, installationID, hookKey string, agentID pgtype.UUID, input json.RawMessage) (HookResult, error) {
	caller, err := s.AuthorizePluginAction(ctx, installationID, pgtype.UUID{}, "")
	if err != nil {
		return HookResult{}, err
	}
	hook, err := FindHook(caller.Installation, hookKey)
	if err != nil {
		return HookResult{}, err
	}
	return s.InvokeHook(ctx, HookInvocation{
		Installation: caller.Installation,
		Hook:         hook,
		Trigger:      plugincontract.TriggerAgent,
		Actor:        HookActor{Type: "agent", ID: agentID},
		Input:        rawInputOrNil(input),
	}, 1)
}

// rawInputOrNil 把空原始输入变成 nil。
func rawInputOrNil(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
