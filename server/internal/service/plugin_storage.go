// plugin_storage.go —— 插件键值存储：工作区/user 两种作用域，写时强制配额（永不驱逐）。
package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 插件存储作用域。恰好两个；加第三个是产品决策，不是配置旋钮。
const (
	// PluginStorageWorkspace 是团队共享状态。scope_id 为工作区 id。
	PluginStorageWorkspace = "workspace"
	// PluginStorageUser 是逐成员状态，含该成员在插件外部服务的自有凭据。scope_id 为用户 id。
	PluginStorageUser = "user"
)

// 存储配额。写时强制、绝不驱逐：撞上限的插件得到显式错误，因为静默丢弃最旧键会破坏插件认为已存的状态。
const (
	MaxPluginStorageKeyBytes   = 1024
	MaxPluginStorageValueBytes = 100 * 1024
	MaxPluginStorageKeys       = 1000
	MaxPluginStorageTotalBytes = 5 * 1024 * 1024
)

// PluginStorageKey 是列出结果里的一个键。值刻意缺席：列出是为了让插件发现自己写过什么，不是批量读状态。
type PluginStorageKey struct {
	Key       string `json:"key"`
	SizeBytes int64  `json:"size_bytes"`
	UpdatedAt string `json:"updated_at"`
}

// ResolveStorageScope 把作用域名映射到行的 scope_id（workspace 用工作区 id，user 用用户 id）。
func ResolveStorageScope(scopeType string, workspaceID, userID pgtype.UUID) (pgtype.UUID, error) {
	switch scopeType {
	case PluginStorageWorkspace:
		return workspaceID, nil
	case PluginStorageUser:
		return userID, nil
	default:
		return pgtype.UUID{}, pluginErrf(PluginErrorInvalid, "storage scope must be %q or %q", PluginStorageWorkspace, PluginStorageUser)
	}
}

// validateStorageKey 校验存储键非空且不超长。
func validateStorageKey(key string) error {
	if key == "" {
		return pluginErrf(PluginErrorInvalid, "storage key is required")
	}
	if len(key) > MaxPluginStorageKeyBytes {
		return pluginErrf(PluginErrorQuota, "storage key exceeds %d bytes", MaxPluginStorageKeyBytes)
	}
	return nil
}

// GetStorageValue 读取一个值。该路径永远到不了 plugin_secret：机密在另一张表，没有把密文返回给请求
// handler 的读查询。
func (s *PluginService) GetStorageValue(ctx context.Context, installationID pgtype.UUID, scopeType string, scopeID pgtype.UUID, key string) (string, error) {
	if err := validateStorageKey(key); err != nil {
		return "", err
	}
	row, err := s.Queries.GetPluginStorageValue(ctx, db.GetPluginStorageValueParams{
		InstallationID: installationID,
		ScopeType:      scopeType,
		ScopeID:        scopeID,
		Key:            key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", pluginErrf(PluginErrorNotFound, "storage key not found")
	}
	if err != nil {
		return "", &PluginError{Kind: PluginErrorUnavailable, Message: "read plugin storage", Err: err}
	}
	return row.Value, nil
}

// ListStorageKeys 列出作用域内的键元数据（键名/大小/更新时间，UTC 格式化）。
func (s *PluginService) ListStorageKeys(ctx context.Context, installationID pgtype.UUID, scopeType string, scopeID pgtype.UUID) ([]PluginStorageKey, error) {
	rows, err := s.Queries.ListPluginStorageKeys(ctx, db.ListPluginStorageKeysParams{
		InstallationID: installationID,
		ScopeType:      scopeType,
		ScopeID:        scopeID,
	})
	if err != nil {
		return nil, &PluginError{Kind: PluginErrorUnavailable, Message: "list plugin storage", Err: err}
	}
	keys := make([]PluginStorageKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, PluginStorageKey{
			Key:       row.Key,
			SizeBytes: row.SizeBytes,
			UpdatedAt: row.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return keys, nil
}

// EnforceStorageQuota 在“排除候选键后的作用域用量”之上判断一次写入是否放得下。保持纯函数、独立于写入，
// 使三条上限有唯一规范测试，也让“每个界限都按字节比较”一目了然——用量查询同样因这个原因报 octet_length。
//
// 已知边界：用量在写入前读取、不加锁，因此并发写同一作用域可能各自超一点。为弥合这点串行化每个插件 KV
// 写的代价大于超额的代价；这些限制是阻止失控增长，不是精确到字节。
func EnforceStorageQuota(usage db.GetPluginStorageUsageRow, valueBytes int) error {
	if valueBytes > MaxPluginStorageValueBytes {
		return pluginErrf(PluginErrorQuota, "storage value exceeds %d bytes", MaxPluginStorageValueBytes)
	}
	if usage.KeyCount+1 > MaxPluginStorageKeys {
		return pluginErrf(PluginErrorQuota, "storage scope already holds the maximum of %d keys", MaxPluginStorageKeys)
	}
	if usage.TotalBytes+int64(valueBytes) > MaxPluginStorageTotalBytes {
		return pluginErrf(PluginErrorQuota, "storage scope exceeds its %d byte budget", MaxPluginStorageTotalBytes)
	}
	return nil
}

// SetStorageValue 在检查全部配额后写一个值。用量查询排除正被写的键，因此替换值按“替换”计量，
// 不会因既有行已占用的空间而撞上限。
func (s *PluginService) SetStorageValue(ctx context.Context, installationID pgtype.UUID, scopeType string, scopeID pgtype.UUID, key, value string) error {
	if err := validateStorageKey(key); err != nil {
		return err
	}
	usage, err := s.Queries.GetPluginStorageUsage(ctx, db.GetPluginStorageUsageParams{
		InstallationID: installationID,
		ScopeType:      scopeType,
		ScopeID:        scopeID,
		Key:            key,
	})
	if err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "read plugin storage usage", Err: err}
	}
	if err := EnforceStorageQuota(usage, len(value)); err != nil {
		return err
	}
	if _, err := s.Queries.UpsertPluginStorageValue(ctx, db.UpsertPluginStorageValueParams{
		InstallationID: installationID,
		ScopeType:      scopeType,
		ScopeID:        scopeID,
		Key:            key,
		Value:          value,
	}); err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "write plugin storage", Err: err}
	}
	return nil
}

// DeleteStorageValue 删除一个键；无行被删（键不存在）时返回 not-found。
func (s *PluginService) DeleteStorageValue(ctx context.Context, installationID pgtype.UUID, scopeType string, scopeID pgtype.UUID, key string) error {
	if err := validateStorageKey(key); err != nil {
		return err
	}
	rows, err := s.Queries.DeletePluginStorageValue(ctx, db.DeletePluginStorageValueParams{
		InstallationID: installationID,
		ScopeType:      scopeType,
		ScopeID:        scopeID,
		Key:            key,
	})
	if err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "delete plugin storage", Err: err}
	}
	if rows == 0 {
		return pluginErrf(PluginErrorNotFound, "storage key not found")
	}
	return nil
}
