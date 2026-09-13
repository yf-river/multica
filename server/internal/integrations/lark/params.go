// params.go：飞书渠道化存储所需的“领域参数类型”定义。
//
// 这些类型替代了由 queries/lark.sql 生成的 db.*LarkParams（已退役）结构，
// 字段命名与 store.go 中的领域实体一致、不带渠道色彩。store
// (channel_store.go) 会把它们映射到 channel_* 的写入，并在 DB 边界把飞书
// 特有的标识符折叠进 JSONB config 中。
package lark

import "github.com/jackc/pgx/v5/pgtype"

// GetInstallationInWorkspaceParams 把安装信息查询限定在某个 workspace 内。
type GetInstallationInWorkspaceParams struct {
	ID          pgtype.UUID
	WorkspaceID pgtype.UUID
}

// UpsertInstallationParams 承载一次安装/重装所需的扁平化飞书安装字段。
type UpsertInstallationParams struct {
	WorkspaceID        pgtype.UUID
	AgentID            pgtype.UUID
	AppID              string
	AppSecretEncrypted []byte
	BotOpenID          string
	InstallerUserID    pgtype.UUID
	TenantKey          pgtype.Text
	BotUnionID         pgtype.Text
	Region             string
}

// SetInstallationStatusParams 切换某条安装记录的状态（active/revoked）。
type SetInstallationStatusParams struct {
	ID     pgtype.UUID
	Status string
}

// SetInstallationBotUnionIDParams 记录机器人的 union_id（用于回填）。
type SetInstallationBotUnionIDParams struct {
	ID         pgtype.UUID
	BotUnionID pgtype.Text
}

// AcquireWSLeaseParams 为某个安装抢占（fence）WS supervisor 租约。
type AcquireWSLeaseParams struct {
	NewToken     pgtype.Text
	NewExpiresAt pgtype.Timestamptz
	ID           pgtype.UUID
}

// ReleaseWSLeaseParams 释放调用方仍持有的 WS supervisor 租约。
type ReleaseWSLeaseParams struct {
	ID           pgtype.UUID
	CurrentToken pgtype.Text
}

// GetUserBindingByOpenIDParams 按渠道原生用户 id 查询用户绑定记录。
type GetUserBindingByOpenIDParams struct {
	InstallationID pgtype.UUID
	ChannelUserID  string
}

// CreateUserBindingParams 把某个 workspace 成员绑定到渠道原生用户 id。
type CreateUserBindingParams struct {
	WorkspaceID    pgtype.UUID
	MulticaUserID  pgtype.UUID
	InstallationID pgtype.UUID
	ChannelUserID  string
	UnionID        pgtype.Text
}

// GetChatSessionBindingParams 按渠道聊天 id 查询聊天绑定记录。
type GetChatSessionBindingParams struct {
	InstallationID pgtype.UUID
	ChannelChatID  string
}

// UpdateChatSessionBindingReplyTargetParams 记录最近一次入站触发消息 + 会话
// 线程，出站 patcher 据此把回复贴进对应的线程里。
type UpdateChatSessionBindingReplyTargetParams struct {
	ChatSessionID pgtype.UUID
	LastMessageID pgtype.Text
	LastThreadID  pgtype.Text
}

// ClaimInboundDedupParams 为一条消息认领两阶段幂等去重行。
type ClaimInboundDedupParams struct {
	InstallationID pgtype.UUID
	MessageID      string
}

// MarkInboundDedupProcessedParams 把已认领的消息标记为已处理（带 fence 防串改）。
type MarkInboundDedupProcessedParams struct {
	InstallationID pgtype.UUID
	MessageID      string
	ClaimToken     pgtype.UUID
}

// ReleaseInboundDedupParams 在处理失败时释放认领（带 fence）。
type ReleaseInboundDedupParams struct {
	InstallationID pgtype.UUID
	MessageID      string
	ClaimToken     pgtype.UUID
}

// RecordInboundDropParams 写一条不含正文内容的丢弃审计行。
type RecordInboundDropParams struct {
	EventType        string
	DropReason       string
	InstallationID   pgtype.UUID
	ChannelChatID    pgtype.Text
	ChannelEventID   pgtype.Text
	ChannelMessageID pgtype.Text
}

// CreateBindingTokenParams 铸造一个短时效的渠道绑定令牌。
type CreateBindingTokenParams struct {
	TokenHash      string
	WorkspaceID    pgtype.UUID
	InstallationID pgtype.UUID
	ChannelUserID  string
	ExpiresAt      pgtype.Timestamptz
}

// CreateOutboundCardMessageParams 为某个 task/会话记录一条出站卡片消息。
type CreateOutboundCardMessageParams struct {
	ChatSessionID        pgtype.UUID
	ChannelChatID        string
	ChannelCardMessageID string
	Status               string
	TaskID               pgtype.UUID
}

// UpdateOutboundCardStatusParams 推进某条出站卡片消息的状态。
type UpdateOutboundCardStatusParams struct {
	ID     pgtype.UUID
	Status string
}
