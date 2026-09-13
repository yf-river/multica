// plugin_mcp_transport.go —— MCP（Model Context Protocol）传输类型的插件 hook：
// 一个指向“插件作者自己已经在跑”的 MCP 服务器的 hook，Multica 采纳其工具。
// 负责：发现工具、管理员审批钉住（按名称+schema 摘要）、把已批准的 hook 转成
// broker 连接，以及携带密钥鉴权头。
package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

// `mcp` 传输方式：hook 指向一个插件作者已在运行的 MCP 服务器，Multica 采纳它的
// 工具。
//
// 与 `http` hook 的全部区别在于“谁决定形状”。http hook 在 manifest 里声明一个
// 端点，由管理员读过并批准；而 MCP 服务器在运行时自己决定工具清单，并且随时可能
// 改——所以“安装这个插件”如果照单全收，就等于永久授权去运行那台服务器下周才提供
// 的任何东西。
//
// 因此有了审批：管理员看到发现出来的工具，按名称和 schema 摘要把它们钉住。之后才
// 出现的工具不会被采纳；schema 跑偏的工具会停止被调用。daemon 端的强制已经存在
// （validatePinnedRemoteMCPTools 在 broker 启动时拒绝），所以这里是把插件的 hook
// 接进那个检查早已认识的连接形态里。

// PluginMCPApproval 是一个 hook 已获批的工具清单。
type PluginMCPApproval struct {
	Tools      []remotemcp.Tool `json:"tools"`
	ApprovedAt string           `json:"approved_at"`
	ApprovedBy string           `json:"approved_by,omitempty"`
}

// PluginMCPApprovals 以 hook key 为键、映射到它的审批记录。
type PluginMCPApprovals map[string]PluginMCPApproval

// decodeMCPApprovals 从安装记录里反序列化审批 JSON；空/坏数据退化成空表（调用方
// 会按“没批过”处理，行为上偏保守）。
func decodeMCPApprovals(raw []byte) PluginMCPApprovals {
	approvals := PluginMCPApprovals{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &approvals)
	}
	return approvals
}

// DiscoverMCPHookTools 询问该 hook 的 MCP 服务器当前提供哪些工具。
//
// 只读、由管理员驱动：光是“发现”不会采纳任何东西。管理员从这里挑选并批准——
// 批准才是让工具可被调用的那一步。
func (s *PluginService) DiscoverMCPHookTools(ctx context.Context, installation db.PluginInstallation, hookKey string) ([]remotemcp.Tool, error) {
	hook, err := FindHook(installation, hookKey)
	if err != nil {
		return nil, err
	}
	if hook.Transport.Type != plugincontract.TransportMCP {
		return nil, pluginErrf(PluginErrorInvalid, "hook %q is not an mcp transport", hookKey)
	}
	// 第二层防线。manifest 校验已经拒绝“传输 URL 不在已声明 net: 范围内”的 hook，
	// 所以正常装进来的 manifest 走不到这里——保留这层是因为下面的目标域检查要从这里
	// 取允许清单，空清单必须“失败即拒绝”，而不是被读成“没有限制”。
	domains := plugincontract.NetDomains(decodeScopes(installation.GrantedScopes))
	if len(domains) == 0 {
		return nil, pluginErrf(PluginErrorForbidden, "this Plugin was granted no net: scope, so it cannot reach an MCP server")
	}

	headers, err := s.mcpCredentialHeaders(ctx, installation, hook)
	if err != nil {
		return nil, err
	}
	// 和其他所有对外调用同一套端点护栏：目标必须在用户同意的 `net:` 集合内，且要
	// 能公开解析。
	tools, _, err := remotemcp.Discover(ctx, hook.Transport.URL, domains, nil, headers)
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorUnavailable, Message: "could not reach the Plugin's MCP server", Err: err}
	}
	return tools, nil
}

// ApproveMCPHookTools 为单个 hook 钉住一份工具清单。
//
// 按摘要钉，而不只是按名字：服务器保留工具名、却改了它的参数，等于悄悄改了 agent
// 实际调用的东西，而 agent 无从察觉。
func (s *PluginService) ApproveMCPHookTools(ctx context.Context, installation db.PluginInstallation, hookKey string, names []string, userID pgtype.UUID) (db.PluginInstallation, error) {
	discovered, err := s.DiscoverMCPHookTools(ctx, installation, hookKey)
	if err != nil {
		return db.PluginInstallation{}, err
	}
	byName := make(map[string]remotemcp.Tool, len(discovered))
	for _, tool := range discovered {
		byName[tool.Name] = tool
	}

	approved := make([]remotemcp.Tool, 0, len(names))
	for _, name := range names {
		tool, ok := byName[name]
		if !ok {
			// 批准一个服务器当前并不提供的工具，等于钉住一个没有 schema 的名，
			// broker 会在启动时拒绝整条连接。
			return db.PluginInstallation{}, pluginErrf(PluginErrorInvalid, "tool %q is not offered by this MCP server", name)
		}
		approved = append(approved, tool)
	}

	approvals := decodeMCPApprovals(installation.McpApprovals)
	if len(approved) == 0 {
		// 一个都不批，就是管理员“收回某个 hook”的方式——删除该条记录，而不是存一份
		// 会被读成“已批准，但里面是空的”的空允许清单。
		delete(approvals, hookKey)
	} else {
		approvals[hookKey] = PluginMCPApproval{
			Tools:      approved,
			ApprovedAt: time.Now().UTC().Format(time.RFC3339),
			ApprovedBy: uuidString(userID),
		}
	}

	encoded, err := json.Marshal(approvals)
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorInvalid, Message: "encode approvals", Err: err}
	}
	updated, err := s.Queries.SetPluginMCPApprovals(ctx, db.SetPluginMCPApprovalsParams{
		ID:           installation.ID,
		McpApprovals: encoded,
	})
	if err != nil {
		return db.PluginInstallation{}, &PluginError{Kind: PluginErrorUnavailable, Message: "store approvals", Err: err}
	}
	return updated, nil
}

// AgentMCPConnections 把已批准的 mcp hook 转成 broker 连接。
//
// 随认领（claim）载荷一起返回、和 http 传输的工具并列，于是 daemon 用已有的机制
// 一并处理两者：现有 broker 代理到 MCP 服务器，validatePinnedRemoteMCPTools 在
// 某个已批准工具消失或 schema 跑偏时拒绝启动。
//
// 没有审批的 hook 不产出连接。这正是关键点——安装插件不等于授权，批准工具才是。
func (s *PluginService) AgentMCPConnections(ctx context.Context, workspaceID pgtype.UUID) ([]remotemcp.Connection, error) {
	installations, err := s.Queries.ListWorkspacePluginInstallations(ctx, workspaceID)
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorUnavailable, Message: "list plugin installations", Err: err}
	}

	connections := make([]remotemcp.Connection, 0)
	for _, installation := range installations {
		if !installation.Enabled {
			continue
		}
		connections = append(connections, mcpConnectionsFor(installation)...)
	}
	return connections, nil
}

// mcpConnectionsFor 把单个安装里的已批准 mcp hook 转成 broker 连接。从工作区查询里
// 拆出来，是为了让决定“一个 hook 是否会被提供”的三个条件——mcp 传输、agent 触发、
// 非空审批——可以不依赖数据库做测试。
func mcpConnectionsFor(installation db.PluginInstallation) []remotemcp.Connection {
	manifest, err := ParseInstallationManifest(installation)
	if err != nil {
		// 一份读不懂的 manifest 不能把其他所有插件的工具都藏掉。
		return nil
	}
	approvals := decodeMCPApprovals(installation.McpApprovals)
	domains := plugincontract.NetDomains(decodeScopes(installation.GrantedScopes))

	connections := make([]remotemcp.Connection, 0)
	for _, hook := range manifest.Contributes.Hooks {
		if hook.Transport.Type != plugincontract.TransportMCP {
			continue
		}
		if !HookAllowsTrigger(hook, plugincontract.TriggerAgent) {
			continue
		}
		approval, ok := approvals[hook.Key]
		if !ok || len(approval.Tools) == 0 {
			continue
		}
		credentialHeader := ""
		if field, found := manifest.Config.Field(hook.Key + "_credential"); found && field.Type == plugincontract.ConfigSecret {
			credentialHeader = "Authorization"
		}
		connections = append(connections, remotemcp.Connection{
			InstallationID:  uuidString(installation.ID),
			ContributionID:  remotemcp.PluginContributionPrefix + uuidString(installation.ID) + ":" + hook.Key,
			ContributionKey: PluginToolName(manifest.Key, hook.Key),
			Endpoint:        hook.Transport.URL,
			Transport:       "http",
			// 和同意页展示的是同一份精确主机集合。broker 在拨号时还会复查，所以
			// manifest 之后即便把自己的 hook 改指别处，也到不了任何新地方。
			EndpointAllowedHosts: domains,
			CredentialHeader:     credentialHeader,
			ApprovedTools:        approval.Tools,
			ToolSchemaDigest:     toolSetDigest(approval.Tools),
			// 插件的 MCP 服务器宕机不能让任务失败，原因和失败的 http hook 只是一个
			// 工具错误相同：agent 应该仍然能处理该 issue。
			FailurePolicy: "optional",
		})
	}
	return connections
}

// toolSetDigest 钉住的是整个已批准集合、而不只是逐个工具。摘要出错时返回空串：
// broker 于是把集合层视为“未钉住”，同时仍逐个检查工具——这是朝安全方向降级。
func toolSetDigest(tools []remotemcp.Tool) string {
	digest, err := remotemcp.ToolSetDigest(tools)
	if err != nil {
		return ""
	}
	return digest
}

// mcpCredentialHeaders 按 manifest 是否为本 hook 声明了密钥配置，从安装的密钥里
// 拼出鉴权请求头。
//
// 复用现有密钥存储——只写、secretbox 加密、任何读端点都不回显——而不是再开一个
// 放插件凭据的地方。
func (s *PluginService) mcpCredentialHeaders(ctx context.Context, installation db.PluginInstallation, hook plugincontract.Hook) (map[string][]string, error) {
	manifest, err := ParseInstallationManifest(installation)
	if err != nil {
		return nil, err
	}
	field, ok := manifest.Config.Field(hook.Key + "_credential")
	if !ok || field.Type != plugincontract.ConfigSecret {
		return nil, nil
	}
	secret, err := s.decryptedSecret(ctx, installation.ID, hook.Key+"_credential")
	if err != nil || secret == "" {
		// 声明了但没填的密钥是配置缺口，不该拿一个让人困惑的传输错误去打断发现流程。
		return nil, nil
	}
	return map[string][]string{"Authorization": {secret}}, nil
}

// decryptedSecret 解开一条已存储的密钥。
//
// 这是插件密钥全流程里唯一一次解密用于实际使用的地方，而这里用它去给插件作者
// “自己的服务器”做鉴权——绝不通过任何读端点回显，这正是独立密钥表存在的意义：
// 在结构上保证这一点。
func (s *PluginService) decryptedSecret(ctx context.Context, installationID pgtype.UUID, key string) (string, error) {
	if s.Secrets == nil {
		return "", pluginErrf(PluginErrorUnavailable, "plugin secrets are disabled: MULTICA_PLUGIN_SECRET_KEY is not configured")
	}
	row, err := s.Queries.GetPluginSecret(ctx, db.GetPluginSecretParams{InstallationID: installationID, Key: key})
	if err != nil {
		return "", err
	}
	plaintext, err := s.Secrets.Open(row.Ciphertext)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// ApprovedMCPTools 返回单个 hook 的钉住集合，按名字索引。
func (s *PluginService) ApprovedMCPTools(installation db.PluginInstallation, hookKey string) map[string]remotemcp.Tool {
	approval, ok := decodeMCPApprovals(installation.McpApprovals)[hookKey]
	if !ok {
		return map[string]remotemcp.Tool{}
	}
	byName := make(map[string]remotemcp.Tool, len(approval.Tools))
	for _, tool := range approval.Tools {
		byName[tool.Name] = tool
	}
	return byName
}

// MCPHookCredential 解析单个 mcp hook 的凭据，供 daemon 的 broker 使用。manifest
// 没声明时返回空串。
func (s *PluginService) MCPHookCredential(ctx context.Context, installation db.PluginInstallation, hookKey string) (string, string, error) {
	hook, err := FindHook(installation, hookKey)
	if err != nil {
		return "", "", err
	}
	headers, err := s.mcpCredentialHeaders(ctx, installation, hook)
	if err != nil {
		return "", "", err
	}
	for name, values := range headers {
		if len(values) > 0 {
			return name, values[0], nil
		}
	}
	return "", "", nil
}
