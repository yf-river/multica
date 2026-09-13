// 本文件负责给 daemon 派发的任务拼装「来源上下文」（protocol.TaskSourceContext）：
// 当 issue 是从外部系统（TAPD、工蜂等）同步而来时，把它的来源信息与需要带给 agent
// 的外部账号凭据写进任务声明。
//
// 核心概念：
//   - 来源（source）：写在该 issue 的 metadata 里（source_provider / source_url /
//     tapd_workspace_id 等），由外部同步入站时落下；
//   - 凭据上下文（credential context）：issue 创建者或触发用户（继承语义
//     task_creator_or_trigger_user）在「账号级默认外部凭据 profile」里存的 token。
//     buildIssueSourceContext 只把凭据的「描述信息」（是否已配置、profile id/名/状态）
//     放进任务；真正把 token 注入 agent MCP 配置的是 injectSourceCredentialMCPEnv——
//     认领时才重新读取 profile（profile 可能已停用，注入前必须失败关闭）。
//
// 注入时若 agent 的 MCP 配置里还没有对应 server（mcp-server-tapd / gongfeng），会补上
// 默认启动条目（uvx 起 TAPD server；node 起工蜂 server）。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 继承语义与内置 MCP server 名称。
const (
	taskSourceInheritance = "task_creator_or_trigger_user"
	tapdMCPServerName     = "mcp-server-tapd"
	gongfengMCPServerName = "gongfeng"
)

// buildIssueSourceContext 从 issue.metadata 里解析来源信息，组出任务来源上下文。
// 无来源（provider 为空）返回 (nil, nil)——没有来源的任务照常跑。TAPD 来源还会附带
// 账号级凭据描述；fetch 状态缺失时按凭据是否已配置推断 pending / blocked。
func (h *Handler) buildIssueSourceContext(ctx context.Context, issue db.Issue, credentialUserID pgtype.UUID) (*protocol.TaskSourceContext, error) {
	metadata := decodeIssueMetadataObject(issue.Metadata)
	provider := strings.ToLower(metadataStringValue(metadata, "source_provider"))
	if provider == "" {
		return nil, nil
	}

	source := &protocol.TaskSourceContext{
		Provider:            provider,
		URL:                 metadataStringValue(metadata, "source_url"),
		ExternalCredentials: map[string]protocol.TaskExternalCredentialContext{},
	}

	switch provider {
	case externalCredentialProviderTAPD:
		fetchStatus := metadataStringValue(metadata, "source_fetch_status")
		fetchError := metadataStringValue(metadata, "source_fetch_error")
		credential, err := h.sourceCredentialContext(ctx, credentialUserID, externalCredentialProviderTAPD, tapdMCPServerName)
		if err != nil {
			return nil, err
		}
		source.ExternalCredentials[externalCredentialProviderTAPD] = credential
		if fetchStatus == "" {
			if credential.Configured {
				fetchStatus = "pending_mcp_fetch"
			} else {
				fetchStatus = "blocked_missing_profile"
				fetchError = "no usable account-level TAPD credential profile for task creator or trigger user"
			}
		}
		source.TAPD = &protocol.TAPDTaskSourceContext{
			WorkspaceID:   metadataStringValue(metadata, "tapd_workspace_id"),
			ResourceType:  metadataStringValue(metadata, "tapd_resource_type"),
			ResourceID:    metadataStringValue(metadata, "tapd_resource_id"),
			FetchProvider: firstNonEmpty(metadataStringValue(metadata, "source_fetch_provider"), "tapd_mcp"),
			FetchStatus:   fetchStatus,
			FetchError:    fetchError,
			Title:         metadataStringValue(metadata, "source_fetch_title"),
			Summary:       metadataStringValue(metadata, "source_fetch_summary"),
			BodyExcerpt:   metadataStringValue(metadata, "source_fetch_body_excerpt"),
			Version:       metadataStringValue(metadata, "source_fetch_version"),
		}
	case externalCredentialProviderGongfeng:
		credential, err := h.sourceCredentialContext(ctx, credentialUserID, externalCredentialProviderGongfeng, gongfengMCPServerName)
		if err != nil {
			return nil, err
		}
		source.ExternalCredentials[externalCredentialProviderGongfeng] = credential
	default:
		return source, nil
	}

	if len(source.ExternalCredentials) == 0 {
		source.ExternalCredentials = nil
	}
	return source, nil
}

// sourceCredentialContext 查某用户账号级的默认外部凭据 profile（按 provider），
// 产出凭据上下文。用户无效或没有 profile（ErrNoRows）都返回「未配置」的上下文而非
// 错误——没有凭据只是意味着 agent 拿不到外部系统，不是任务派发失败。
func (h *Handler) sourceCredentialContext(ctx context.Context, userID pgtype.UUID, provider, mcpServer string) (protocol.TaskExternalCredentialContext, error) {
	out := protocol.TaskExternalCredentialContext{
		Provider:    provider,
		Scope:       "account",
		Inheritance: taskSourceInheritance,
		UserID:      uuidToString(userID),
		MCPServer:   mcpServer,
	}
	if !userID.Valid {
		return out, nil
	}
	profile, err := h.Queries.GetDefaultExternalCredentialProfileForUser(ctx, db.GetDefaultExternalCredentialProfileForUserParams{
		UserID:   userID,
		Provider: provider,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		return protocol.TaskExternalCredentialContext{}, fmt.Errorf("load %s credential profile: %w", provider, err)
	}
	out.ProfileID = uuidToString(profile.ID)
	out.ProfileName = profile.Name
	out.ProfileStatus = profile.Status
	env, err := h.externalCredentialProfileEnv(profile)
	if err != nil {
		return protocol.TaskExternalCredentialContext{}, err
	}
	out.Configured = len(env) > 0
	return out, nil
}

// injectSourceCredentialMCPEnv 在认领时把已配置凭据的真实环境变量注入 agent 的 MCP
// 配置（为对应 server 的 env）。任何被标记为已配置的 profile 现在读不到或已停用，都
// 失败关闭（返回错误，宁可任务不派发也不给 agent 残缺凭据）。未做任何改动时原样返回
// 传入的配置。
func (h *Handler) injectSourceCredentialMCPEnv(ctx context.Context, mcpConfig json.RawMessage, source *protocol.TaskSourceContext) (json.RawMessage, error) {
	if source == nil || len(source.ExternalCredentials) == 0 {
		return mcpConfig, nil
	}
	config, err := normalizeMCPConfigForInjection(mcpConfig)
	if err != nil {
		return nil, err
	}
	changed := false
	for provider, credential := range source.ExternalCredentials {
		if !credential.Configured || credential.MCPServer == "" || credential.UserID == "" {
			continue
		}
		userID := parseUUID(credential.UserID)
		if !userID.Valid {
			continue
		}
		profile, err := h.Queries.GetDefaultExternalCredentialProfileForUser(ctx, db.GetDefaultExternalCredentialProfileForUserParams{
			UserID:   userID,
			Provider: provider,
		})
		if err != nil {
			return nil, fmt.Errorf("reload %s credential profile: %w", provider, err)
		}
		if profile.Status == "disabled" {
			return nil, fmt.Errorf("%s credential profile became disabled", provider)
		}
		env, err := h.externalCredentialProfileEnv(profile)
		if err != nil {
			return nil, err
		}
		if len(env) == 0 {
			continue
		}
		if mergeMCPServerEnv(config, credential.MCPServer, env) {
			changed = true
		}
	}
	if !changed {
		return mcpConfig, nil
	}
	out, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode source credential MCP config: %w", err)
	}
	return json.RawMessage(out), nil
}

// normalizeMCPConfigForInjection 把 agent 的 MCP 配置规范化成可注入的 map 结构
// （确保顶层是对象、存在 mcpServers 字段）。非法配置返回错误。
func normalizeMCPConfigForInjection(raw json.RawMessage) (map[string]any, error) {
	var config map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &config); err != nil || config == nil {
			return nil, errors.New("agent MCP config must be a JSON object")
		}
	}
	if config == nil {
		config = map[string]any{}
	}
	servers, _ := config["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		config["mcpServers"] = servers
	}
	return config, nil
}

// mergeMCPServerEnv 把一组环境变量合并进 mcpServers[serverName].env；返回是否有任何
// 变化。合并前确保该 server 条目存在（缺失时按默认模板补一个）。
func mergeMCPServerEnv(config map[string]any, serverName string, env map[string]string) bool {
	servers, _ := config["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		config["mcpServers"] = servers
	}
	entry, _ := servers[serverName].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
		servers[serverName] = entry
	}
	changed := ensureDefaultMCPServerEntry(serverName, entry)
	entryEnv, _ := entry["env"].(map[string]any)
	if entryEnv == nil {
		entryEnv = map[string]any{}
		entry["env"] = entryEnv
	}
	for key, value := range env {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if current, _ := entryEnv[key].(string); current == value {
			continue
		}
		entryEnv[key] = value
		changed = true
	}
	return changed
}

// ensureDefaultMCPServerEntry 若某 server 还没有任何 command/url，就用内置默认启动
// 模板把它补出来（TAPD 用 uvx 起 mcp-server-tapd，工蜂用 node 起本地 server 脚本）。
func ensureDefaultMCPServerEntry(serverName string, entry map[string]any) bool {
	if strings.TrimSpace(mcpStringValue(entry["command"])) != "" || strings.TrimSpace(mcpStringValue(entry["url"])) != "" {
		return false
	}
	switch serverName {
	case tapdMCPServerName:
		entry["command"] = "uvx"
		entry["args"] = []any{
			"mcp-server-tapd",
			"--api-base-url=https://api.tapd.cn",
			"--tapd-base-url=https://www.tapd.cn",
			"--keep-links=true",
			"--tools-set=lookup_tapd_tool",
		}
		return true
	case gongfengMCPServerName:
		entry["command"] = "node"
		entry["args"] = []any{firstNonEmpty(os.Getenv("GONGFENG_MCP_SERVER_ENTRYPOINT"), "/data/ida/gongfeng-mcp-server/dist/index.js")}
		return true
	default:
		return false
	}
}

// externalCredentialProfileEnv 把凭据 profile 展开成要注入 agent 的环境变量：
// TAPD 产出 TAPD_ACCESS_TOKEN；工蜂产出 GONGFENG_PRIVATE_TOKEN 及若干可选的
// 服务器端配置（走环境变量，便于部署时注入）。没有任何可注入内容时返回 nil。
func (h *Handler) externalCredentialProfileEnv(profile db.ExternalCredentialProfile) (map[string]string, error) {
	token, err := h.resolveExternalCredentialToken(profile)
	if err != nil {
		return nil, err
	}
	switch profile.Provider {
	case externalCredentialProviderTAPD:
		if token == "" {
			return nil, nil
		}
		return map[string]string{"TAPD_ACCESS_TOKEN": token}, nil
	case externalCredentialProviderGongfeng:
		env := map[string]string{}
		if token != "" {
			env["GONGFENG_PRIVATE_TOKEN"] = token
		}
		for _, key := range []string{"GONGFENG_API_BASE", "GONGFENG_WORKDIR", "GONGFENG_SSH_KEY_PATH", "GONGFENG_KNOWN_HOSTS_PATH"} {
			if value := strings.TrimSpace(os.Getenv(key)); value != "" {
				env[key] = value
			}
		}
		if len(env) == 0 {
			return nil, nil
		}
		return env, nil
	default:
		return nil, nil
	}
}

// decodeIssueMetadataObject 把 issue.metadata 字节串解成对象；空/非法返回空 map，
// 让调用方无需判错。
func decodeIssueMetadataObject(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return map[string]any{}
	}
	return metadata
}

// metadataStringValue 取 metadata 里的字符串字段（去首尾空白）；非字符串一律按空串。
func metadataStringValue(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	switch value := metadata[key].(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

// mcpStringValue 取 MCP 配置里某字段的字符串值；非字符串一律按空串。
func mcpStringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}
