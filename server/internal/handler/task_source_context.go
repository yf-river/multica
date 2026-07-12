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
)

const (
	taskSourceInheritance = "task_creator_or_trigger_user"
	tapdMCPServerName     = "mcp-server-tapd"
	gongfengMCPServerName = "gongfeng"
)

func (h *Handler) buildIssueSourceContext(ctx context.Context, issue db.Issue, credentialUserID pgtype.UUID) (*TaskSourceContext, error) {
	metadata := decodeIssueMetadataObject(issue.Metadata)
	provider := strings.ToLower(metadataStringValue(metadata, "source_provider"))
	if provider == "" {
		return nil, nil
	}

	source := &TaskSourceContext{
		Provider:            provider,
		URL:                 metadataStringValue(metadata, "source_url"),
		ExternalCredentials: map[string]TaskExternalCredentialContext{},
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
		source.TAPD = &TAPDTaskSourceContext{
			WorkspaceID:   firstNonEmpty(metadataStringValue(metadata, "tapd_workspace_id"), metadataStringValue(metadata, "tapd_workspace")),
			ResourceType:  firstNonEmpty(metadataStringValue(metadata, "tapd_resource_type"), metadataStringValue(metadata, "tapd_type")),
			ResourceID:    firstNonEmpty(metadataStringValue(metadata, "tapd_resource_id"), metadataStringValue(metadata, "tapd_wiki_id")),
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

func (h *Handler) sourceCredentialContext(ctx context.Context, userID pgtype.UUID, provider, mcpServer string) (TaskExternalCredentialContext, error) {
	out := TaskExternalCredentialContext{
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
		return TaskExternalCredentialContext{}, fmt.Errorf("load %s credential profile: %w", provider, err)
	}
	out.ProfileID = uuidToString(profile.ID)
	out.ProfileName = profile.Name
	out.ProfileStatus = profile.Status
	env, err := h.externalCredentialProfileEnv(profile)
	if err != nil {
		return TaskExternalCredentialContext{}, err
	}
	out.Configured = len(env) > 0
	return out, nil
}

func (h *Handler) injectSourceCredentialMCPEnv(ctx context.Context, mcpConfig json.RawMessage, source *TaskSourceContext) (json.RawMessage, error) {
	if source == nil || len(source.ExternalCredentials) == 0 {
		return mcpConfig, nil
	}
	config := normalizeMCPConfigForInjection(mcpConfig)
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

func normalizeMCPConfigForInjection(raw json.RawMessage) map[string]any {
	var config map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &config)
	}
	if config == nil {
		config = map[string]any{}
	}
	servers, _ := config["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		config["mcpServers"] = servers
	}
	return config
}

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
			env["GONGFENG_ACCESS_TOKEN"] = token
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

func (h *Handler) resolveExternalCredentialToken(profile db.ExternalCredentialProfile) (string, error) {
	if len(profile.EncryptedSecret) > 0 {
		if h.ExternalCredentialBox == nil {
			return "", errors.New("external credential decryptor is not configured")
		}
		plain, err := h.ExternalCredentialBox.Open(profile.EncryptedSecret)
		if err != nil {
			return "", fmt.Errorf("decrypt external credential: %w", err)
		}
		return strings.TrimSpace(string(plain)), nil
	}
	ref := strings.TrimSpace(profile.SecretRef)
	if strings.HasPrefix(ref, "env:") {
		return strings.TrimSpace(os.Getenv(strings.TrimSpace(strings.TrimPrefix(ref, "env:")))), nil
	}
	if strings.HasPrefix(ref, "server-managed:") {
		parts := strings.Split(ref, ":")
		if len(parts) >= 2 {
			switch parts[1] {
			case externalCredentialProviderTAPD:
				return strings.TrimSpace(os.Getenv("TAPD_ACCESS_TOKEN")), nil
			case externalCredentialProviderGongfeng:
				return firstNonEmpty(os.Getenv("GONGFENG_ACCESS_TOKEN"), os.Getenv("GONGFENG_PRIVATE_TOKEN")), nil
			}
		}
	}
	return "", nil
}

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

func mcpStringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
