// dispatch.go：给「待派发的 agent 任务」装配 Composio 的 MCP 覆盖层（overlay）。
// 凡是能发起某个 agent 运行的人，在运行它时都应能用该 agent 拥有者挂上去的 Composio
// 应用。本文件负责把「拥有者的工具白名单 ∩ 拥有者当前活跃的连接」收敛成一个专属 MCP
// 会话，再序列化进任务队列的 runtime_mcp_overlay 字段，由 daemon 派发时合并进该 agent
// 的 MCP 配置。
package composio

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/runtimeapps"
	"github.com/multica-ai/multica/server/internal/util"
	sdk "github.com/multica-ai/multica/server/pkg/composio"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// mcpOverlayServerName 是把 Composio 会话写进合并后 MCP 配置时，在 `mcpServers` 下用的
// 确定性键名。daemon 侧按 server 名合并，因此这个常量就是本集成的命名空间：将来某个
// 提供方想叠加自己的覆盖层，必须另选不同的名字（如 "pipedream"）以免撞名；而 agent
// 自带 mcp_config 里名叫 "composio" 的条目会被本覆盖层有意覆盖——覆盖层携带的是实时
// 的、按用户划分的会话 URL，agent 配置里最多只有一条泛化的服务级条目。
const mcpOverlayServerName = "composio"

// composioMCPServer 是 Claude 风格 {"mcpServers": {...}} 配置里一个 MCP server 条目的
// 线上结构，所有受支持的运行时（Cursor、Codex、Claude、OpenCode、Hermes/Kiro）都消费
// 这种格式。
//
// `type: http` 把这个条目标记为 streamable HTTP 的 MCP 端点——即 Composio 会话助手
// 返回的形态。Headers 携带 Composio 的 API key，所以任何调用方在打印本结构体时都绝不
// 能忘记对 Headers 做脱敏。
type composioMCPServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// mcpOverlayPayload 是写入 agent_task_queue.runtime_mcp_overlay、由 daemon 领取任务时
// 读取的「每任务一份」的覆盖层 JSON。
//
// 结构刻意取 agent.mcp_config（Claude 风格 `mcpServers` map）的子集，让 daemon 的合并
// 退化成按 server 名做扁平的字典并集。若搞得更复杂（能力过滤、环境变量注入……），每个
// sidecar 生成器都得单独学一遍 overlay；保持结构相同，合并就能一直是纯替换。
type mcpOverlayPayload struct {
	MCPServers map[string]composioMCPServer `json:"mcpServers"`
}

// BuildTaskOverlay 返回派发 `agent` 任务时应写入的覆盖层载荷；下述任一闸门不过时返回
// 零值——意味着不建 Composio 会话、也不签发任何 token。
//
// MUL-3963：Composio MCP 现在跟随「agent 调用权限」而非要求发起者==拥有者。安全边界在
// 上游——canInvokeAgent 决定谁能为这个 agent 排队运行；因此能走到派发这一步的任务，
// 必然已被授权运行该 agent，也就被授权使用拥有者给它挂上的 Composio 应用。覆盖层永远
// 按 AGENT 拥有者的已连接应用视角构建：拥有者把那些应用作为 agent 能力的一部分，分享给
// 所有获准调用它的人（private→仅拥有者；public_to→白名单）。当共享的 agent 启用了
// Composio 应用时，前端会就此提醒拥有者。
//
// originatorUserID 仅保留用于审计日志（记录是谁触发的）——它不再参与覆盖层的闸门判定。
//
// 闸门依次为：
//
//  1. agent 没有拥有者——无法解析「已连接应用」视角。不产出覆盖层。
//
//  2. agent.composio_toolkit_allowlist 为空/NULL——拥有者从未启用任何工具包。
//     默认关闭：不产出覆盖层。
//
//  3. 把白名单与 OWNER 当前活跃的 Composio 连接求交后，没有任何工具包存在活跃连接。
//     没有可挂载的东西。
//
//  4. Composio 返回的会话没有 URL——防御性检查。
//
// CreateSession 以拥有者作为会话用户调用，同时带上 `toolkits.enable` 白名单过滤和
// `connected_accounts` 固定，使会话被收窄到（拥有者白名单 ∩ 拥有者活跃连接）。
func (s *Service) BuildTaskOverlay(ctx context.Context, originatorUserID pgtype.UUID, agent db.Agent) (runtimeapps.MCPOverlayResult, error) {
	// 闸门 1：覆盖层是 agent 拥有者的已连接应用视角。没有拥有者就无从投影。
	if !agent.OwnerID.Valid {
		return runtimeapps.MCPOverlayResult{}, nil
	}
	ownerUserID := agent.OwnerID
	// 闸门 2：agent 拥有者没有白名单任何工具包。NULL 与空 `{}` 一视同仁——都表示
	// "无覆盖层"。
	allowSet := normaliseAllowlistToSet(agent.ComposioToolkitAllowlist)
	if len(allowSet) == 0 {
		return runtimeapps.MCPOverlayResult{}, nil
	}

	// 解析 OWNER 的活跃连接并与白名单求交。这个交集既是给 Composio CreateSession 调用
	// 过滤的权威输入，也是下面提前退出的依据。
	rows, err := s.store.ListActiveUserComposioConnections(ctx, ownerUserID)
	if err != nil {
		return runtimeapps.MCPOverlayResult{}, fmt.Errorf("composio: build task overlay: list connections: %w", err)
	}
	pinned := pinConnectedAccounts(rows, allowSet)
	if len(pinned) == 0 {
		// 闸门 3：拥有者白名单了「并未连接过——或事后已撤销」的工具包。
		return runtimeapps.MCPOverlayResult{}, nil
	}
	// `toolkits.enable` 收窄工具路由暴露面；再配合同一个 connected-account 固定，让会话
	// 永远不可能浮出（白名单 ∩ 活跃连接）之外的账户。
	slugs := make([]string, 0, len(pinned))
	for slug := range pinned {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	resp, err := s.sdk.CreateSession(ctx, sdk.CreateSessionRequest{
		UserID:            util.UUIDToString(ownerUserID),
		Toolkits:          map[string]any{"enable": slugs},
		ConnectedAccounts: pinned,
	})
	if err != nil {
		return runtimeapps.MCPOverlayResult{}, fmt.Errorf("composio: build task overlay: create session: %w", err)
	}
	// 闸门 4：Composio 回了 200 却没有 MCP URL。按"无覆盖层"处理，而不是把一个空 URL
	// 的 server 接上线——后者在每个运行时都会大声报错。
	if resp == nil || resp.MCP.URL == "" {
		return runtimeapps.MCPOverlayResult{}, nil
	}

	payload := mcpOverlayPayload{
		MCPServers: map[string]composioMCPServer{
			mcpOverlayServerName: {
				Type:    "http",
				URL:     resp.MCP.URL,
				Headers: s.sdk.MCPAuthHeaders(),
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return runtimeapps.MCPOverlayResult{}, fmt.Errorf("composio: marshal task overlay: %w", err)
	}
	apps := make([]runtimeapps.ConnectedApp, 0, len(slugs))
	for _, slug := range slugs {
		apps = append(apps, runtimeapps.ConnectedApp{
			Provider:    "composio",
			ServerName:  mcpOverlayServerName,
			ToolkitSlug: slug,
			ToolkitName: runtimeapps.DisplayNameForToolkitSlug(slug),
		})
	}
	return runtimeapps.MCPOverlayResult{MCPOverlay: raw, ConnectedApps: apps}, nil
}

// normaliseAllowlistToSet 把 agent.composio_toolkit_allowlist 这个 TEXT[] 列映射成
// slug→{} 集合。每个条目防御性地转小写并去空格（API 层写入时已做过归一，但 DB 级迁移
// /绕道写入可能绕过它，而重新归一化不过一次 map 遍历的成本）。结果为空时触发
// BuildTaskOverlay 里的闸门 3，NULL 列与 `{}` 数组表现一致。
func normaliseAllowlistToSet(allow []string) map[string]struct{} {
	if len(allow) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(allow))
	for _, s := range allow {
		slug := lowerTrim(s)
		if slug == "" {
			continue
		}
		out[slug] = struct{}{}
	}
	return out
}

// pinConnectedAccounts 把发起者的活跃连接行与白名单集合求交，返回 Composio
// /tool_router/session 端点期望的 `connected_accounts` map 形态：白名单里每个工具包
// slug 一条，值是该工具包下发起者已连接账户 id 的数组。产品目前允许每个工具包一个活跃
// 账户，所以每个数组只有一个元素。
//
// 重复时取最新：行按 connected_at DESC 到达（见 ListActiveUserComposioConnections），
// 因此同一 slug 先看到的行就是最近连接的账户，与 CreateMCPSession 已文档化的
// 单账户/工具包不变量一致。
func pinConnectedAccounts(rows []db.UserComposioConnection, allowSet map[string]struct{}) map[string]any {
	pinned := make(map[string]any, len(rows))
	for _, row := range rows {
		slug := lowerTrim(row.ToolkitSlug)
		if slug == "" {
			continue
		}
		if _, allowed := allowSet[slug]; !allowed {
			continue
		}
		if _, dup := pinned[slug]; dup {
			continue
		}
		pinned[slug] = []string{row.ConnectedAccountID}
	}
	return pinned
}

// lowerTrim 是内联的小工具函数，让白名单与连接 slug 的比较口径一致，又不必为了
// 永远只是 ASCII 的 slug 拖进整个 unicode 库。
func lowerTrim(s string) string {
	// 本可以用 strings.ToLower + TrimSpace，但只为两个 ASCII 变换、在热点路径上引入
	// strings 不划算。全 ASCII 常见场景下这段手工循环是无分配的。
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	if start == end {
		return ""
	}
	// 先探测是否存在大写，避免为全小写的常见输入白白分配内存。
	upper := false
	for i := start; i < end; i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			upper = true
			break
		}
	}
	if !upper {
		return s[start:end]
	}
	b := make([]byte, end-start)
	for i := start; i < end; i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i-start] = c
	}
	return string(b)
}
