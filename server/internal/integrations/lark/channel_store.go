// channel_store.go：飞书集成在生产环境的数据层——MUL-3515 把 lark_* 泛化为
// channel_* 之后，这里负责飞书相关的全部读写。
//
// ChannelStore 内嵌 *db.Queries（所以所有通用查询——chat_session、chat_message、
// member、workspace、agent——都原样可用），并补充飞书特有的 store 方法；每个
// 方法底层是一条 channel_* 查询，在 JSONB-config 边界（store.go）做翻译。
//
// 方法接收/返回本包的扁平领域类型（Installation、UserBinding、
// ChatSessionBinding、InboundMessageDedup、BindingTokenRow、
// OutboundCardMessage）以及 params.go 里的 *Params 类型。本 store 只读写
// channel_*，绝不碰 lark_*；queries/lark.sql 已删除。物理 lark_* 表为发布/回滚
// 安全保留一个版本（见 migration 124 的 ROLLOUT 说明），由后续清理迁移删除。
package lark

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// channelTypeFeishu 是本飞书 store 读写的所有行的 channel_type 判别值。
const channelTypeFeishu = "feishu"

// ChannelStore 是飞书 channel 化数据层：内嵌 *db.Queries，补齐飞书专属方法。
type ChannelStore struct {
	*db.Queries
}

// NewChannelStore 包装 *db.Queries，让 lark 包的 DB 接缝都解析到 channel_* 行。
func NewChannelStore(q *db.Queries) *ChannelStore {
	return &ChannelStore{Queries: q}
}

// WithTx 返回绑定到 tx 的 ChannelStore。它遮蔽 db.Queries.WithTx（后者返回
// *db.Queries），使事务性调用方（聊天摄入、令牌兑换）在事务内也能用上带
// channel 后端的 store 方法。
func (s *ChannelStore) WithTx(tx pgx.Tx) *ChannelStore {
	return &ChannelStore{Queries: s.Queries.WithTx(tx)}
}

// IsWorkspaceMember 判断 userID 当前是否为 workspaceID 的成员。lark_user_binding
// -> member 外键移除后（MUL-3515 §4），绑定行不再证明成员资格，所以入站身份
// 步骤会显式调用它复查。ErrNoRows -> 不是成员。
func (s *ChannelStore) IsWorkspaceMember(ctx context.Context, workspaceID, userID pgtype.UUID) (bool, error) {
	_, err := s.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      userID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ---- 安装 ----

// GetLarkInstallationByAppID 按飞书 app_id 查安装记录。
func (s *ChannelStore) GetLarkInstallationByAppID(ctx context.Context, appID string) (Installation, error) {
	row, err := s.Queries.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{
		ChannelType: channelTypeFeishu,
		AppID:       appID,
	})
	if err != nil {
		return Installation{}, err
	}
	return installationFromRow(row)
}

// GetLarkInstallation 按 id 查单条飞书安装。
func (s *ChannelStore) GetLarkInstallation(ctx context.Context, id pgtype.UUID) (Installation, error) {
	row, err := s.Queries.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          id,
		ChannelType: channelTypeFeishu,
	})
	if err != nil {
		return Installation{}, err
	}
	return installationFromRow(row)
}

// GetLarkInstallationInWorkspace 在限定 workspace 内按 id 查飞书安装。
func (s *ChannelStore) GetLarkInstallationInWorkspace(ctx context.Context, arg GetInstallationInWorkspaceParams) (Installation, error) {
	row, err := s.Queries.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID:          arg.ID,
		WorkspaceID: arg.WorkspaceID,
		ChannelType: channelTypeFeishu,
	})
	if err != nil {
		return Installation{}, err
	}
	return installationFromRow(row)
}

// ListLarkInstallationsByWorkspace 列出某 workspace 下的全部飞书安装。
func (s *ChannelStore) ListLarkInstallationsByWorkspace(ctx context.Context, workspaceID pgtype.UUID) ([]Installation, error) {
	rows, err := s.Queries.ListChannelInstallationsByWorkspace(ctx, db.ListChannelInstallationsByWorkspaceParams{
		WorkspaceID: workspaceID,
		ChannelType: channelTypeFeishu,
	})
	if err != nil {
		return nil, err
	}
	return installationsFromRows(rows)
}

// ListActiveLarkInstallations 列出全部活跃飞书安装（回填等启动任务用）。
func (s *ChannelStore) ListActiveLarkInstallations(ctx context.Context) ([]Installation, error) {
	rows, err := s.Queries.ListActiveChannelInstallations(ctx, channelTypeFeishu)
	if err != nil {
		return nil, err
	}
	return installationsFromRows(rows)
}

// UpsertLarkInstallation 新建或刷新一条飞书安装（把飞书字段折进 JSONB config）。
func (s *ChannelStore) UpsertLarkInstallation(ctx context.Context, arg UpsertInstallationParams) (Installation, error) {
	cfg, err := encodeInstallConfig(Installation{
		AppID:              arg.AppID,
		AppSecretEncrypted: arg.AppSecretEncrypted,
		TenantKey:          arg.TenantKey,
		BotOpenID:          arg.BotOpenID,
		BotUnionID:         arg.BotUnionID,
		Region:             arg.Region,
	})
	if err != nil {
		return Installation{}, err
	}
	row, err := s.Queries.UpsertChannelInstallation(ctx, db.UpsertChannelInstallationParams{
		WorkspaceID:     arg.WorkspaceID,
		AgentID:         arg.AgentID,
		ChannelType:     channelTypeFeishu,
		Config:          cfg,
		InstallerUserID: arg.InstallerUserID,
	})
	if err != nil {
		return Installation{}, err
	}
	return installationFromRow(row)
}

// ReclaimDeadInstallationByAppID 在重绑之前，用一条语句释放 (feishu,
// config->>'app_id') 路由槽位：移除同一 Lark/飞书 app 的“死”前拥主——本
// workspace 内【另一个】agent 留下的 revoked 占位，或其 workspace/agent 已删除
// 的孤儿（#4810）——连同该安装的所有依赖行。活跃拥主被刻意保留：同一 agent
// 自己的 revoked 行（由随后的 upsert 复活），以及任何 agent 仍存在的 ACTIVE
// 拥主——包括 ARCHIVED agent，因为归档可逆——这样 upsert 会暴露冲突而不是
// 悄悄抢走机器人。完整契约与 TOCTOU / EvalPlanQual 论证见
// ReclaimDeadChannelInstallationByAppID。
func (s *ChannelStore) ReclaimDeadInstallationByAppID(ctx context.Context, workspaceID, agentID pgtype.UUID, appID string) error {
	_, err := s.Queries.ReclaimDeadChannelInstallationByAppID(ctx, db.ReclaimDeadChannelInstallationByAppIDParams{
		ChannelType: channelTypeFeishu,
		AppID:       appID,
		WorkspaceID: workspaceID,
		AgentID:     agentID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// pgx.ErrNoRows 只表示“没有死行”——空操作，不是失败。
		return err
	}
	return nil
}

// InstallationOwnerByAppID 返回 (feishu, app_id) 路由槽位的当前拥主，供调用方
// 拼出准确的重绑冲突提示。在 ReclaimDeadInstallationByAppID 之后调用，所以这里
// 的行是活跃拥主；pgx.ErrNoRows 表示槽位空闲。
func (s *ChannelStore) InstallationOwnerByAppID(ctx context.Context, appID string) (db.GetChannelInstallationOwnerByAppIDRow, error) {
	return s.Queries.GetChannelInstallationOwnerByAppID(ctx, db.GetChannelInstallationOwnerByAppIDParams{
		ChannelType: channelTypeFeishu,
		AppID:       appID,
	})
}

// SetLarkInstallationStatus 切换飞书安装的状态（active/revoked）。
func (s *ChannelStore) SetLarkInstallationStatus(ctx context.Context, arg SetInstallationStatusParams) error {
	return s.Queries.SetChannelInstallationStatus(ctx, db.SetChannelInstallationStatusParams{
		ID:     arg.ID,
		Status: arg.Status,
	})
}

// SetLarkInstallationBotUnionID 通过“读改写 + SetChannelInstallationConfig”把
// bot_union_id 折进 JSONB config（channel_installation 没有专门的 union_id 列）。
// 这是操作者触发的 union_id 回填，按 id 操作、实际是单写者，所以非原子的 RMW
// 也安全——channel.sql 注释里给该查询文档化的正是同一形态。
func (s *ChannelStore) SetLarkInstallationBotUnionID(ctx context.Context, arg SetInstallationBotUnionIDParams) error {
	row, err := s.Queries.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          arg.ID,
		ChannelType: channelTypeFeishu,
	})
	if err != nil {
		return err
	}
	inst, err := installationFromRow(row)
	if err != nil {
		return err
	}
	inst.BotUnionID = arg.BotUnionID
	cfg, err := encodeInstallConfig(inst)
	if err != nil {
		return err
	}
	return s.Queries.SetChannelInstallationConfig(ctx, db.SetChannelInstallationConfigParams{
		ID:     arg.ID,
		Config: cfg,
	})
}

// BackfillLarkInstallationRegionToLark 把遗留的 Lark 国际版安装地域回填为 lark。
func (s *ChannelStore) BackfillLarkInstallationRegionToLark(ctx context.Context) (int64, error) {
	return s.Queries.BackfillChannelInstallationRegionToFeishuLark(ctx)
}

// ---- WS 租约 ----

// AcquireLarkWSLease 抢占某安装的 WS supervisor 租约。
func (s *ChannelStore) AcquireLarkWSLease(ctx context.Context, arg AcquireWSLeaseParams) (Installation, error) {
	row, err := s.Queries.AcquireChannelWSLease(ctx, db.AcquireChannelWSLeaseParams{
		NewToken:     arg.NewToken,
		NewExpiresAt: arg.NewExpiresAt,
		ID:           arg.ID,
	})
	if err != nil {
		return Installation{}, err
	}
	return installationFromRow(row)
}

// ReleaseLarkWSLease 释放调用方仍持有的 WS 租约。
func (s *ChannelStore) ReleaseLarkWSLease(ctx context.Context, arg ReleaseWSLeaseParams) error {
	return s.Queries.ReleaseChannelWSLease(ctx, db.ReleaseChannelWSLeaseParams{
		ID:           arg.ID,
		CurrentToken: arg.CurrentToken,
	})
}

// ---- 用户绑定 ----

// GetLarkUserBindingByOpenID 按 open_id 查用户绑定。
func (s *ChannelStore) GetLarkUserBindingByOpenID(ctx context.Context, arg GetUserBindingByOpenIDParams) (UserBinding, error) {
	row, err := s.Queries.GetChannelUserBindingByUserID(ctx, db.GetChannelUserBindingByUserIDParams{
		InstallationID: arg.InstallationID,
		ChannelUserID:  arg.ChannelUserID,
	})
	if err != nil {
		return UserBinding{}, err
	}
	return userBindingFromRow(row)
}

// CreateLarkUserBinding 创建用户绑定（把 union_id 折进 JSONB config）。
func (s *ChannelStore) CreateLarkUserBinding(ctx context.Context, arg CreateUserBindingParams) (UserBinding, error) {
	cfg, err := encodeBindingConfig(UserBinding{UnionID: arg.UnionID})
	if err != nil {
		return UserBinding{}, err
	}
	row, err := s.Queries.CreateChannelUserBinding(ctx, db.CreateChannelUserBindingParams{
		WorkspaceID:    arg.WorkspaceID,
		MulticaUserID:  arg.MulticaUserID,
		InstallationID: arg.InstallationID,
		ChannelType:    channelTypeFeishu,
		ChannelUserID:  arg.ChannelUserID,
		Config:         cfg,
	})
	if err != nil {
		return UserBinding{}, err
	}
	return userBindingFromRow(row)
}

// ---- 会话绑定 ----

// GetLarkChatSessionBinding 按 (installation, 渠道聊天 id) 查会话绑定。
func (s *ChannelStore) GetLarkChatSessionBinding(ctx context.Context, arg GetChatSessionBindingParams) (ChatSessionBinding, error) {
	row, err := s.Queries.GetChannelChatSessionBinding(ctx, db.GetChannelChatSessionBindingParams{
		InstallationID: arg.InstallationID,
		ChannelChatID:  arg.ChannelChatID,
	})
	if err != nil {
		return ChatSessionBinding{}, err
	}
	return chatSessionBindingFromRow(row), nil
}

// GetLarkChatSessionBindingBySession 按 chat_session_id 查飞书会话绑定。
func (s *ChannelStore) GetLarkChatSessionBindingBySession(ctx context.Context, chatSessionID pgtype.UUID) (ChatSessionBinding, error) {
	row, err := s.Queries.GetChannelChatSessionBindingBySession(ctx, db.GetChannelChatSessionBindingBySessionParams{
		ChatSessionID: chatSessionID,
		ChannelType:   channelTypeFeishu,
	})
	if err != nil {
		return ChatSessionBinding{}, err
	}
	return chatSessionBindingFromRow(row), nil
}

// GetChannelTaskDelivery 按 task id 查渠道任务投递信息。
func (s *ChannelStore) GetChannelTaskDelivery(ctx context.Context, taskID pgtype.UUID) (db.ChannelTaskDelivery, error) {
	return s.Queries.GetChannelTaskDelivery(ctx, taskID)
}

// GetChannelChatSessionBindingBySession 直接透传通用查询（返回原始行）。
func (s *ChannelStore) GetChannelChatSessionBindingBySession(ctx context.Context, arg db.GetChannelChatSessionBindingBySessionParams) (db.ChannelChatSessionBinding, error) {
	return s.Queries.GetChannelChatSessionBindingBySession(ctx, arg)
}

// UpdateLarkChatSessionBindingReplyTarget 记录最近一次入站触发消息 + 线程。
func (s *ChannelStore) UpdateLarkChatSessionBindingReplyTarget(ctx context.Context, arg UpdateChatSessionBindingReplyTargetParams) error {
	return s.Queries.UpdateChannelChatSessionBindingReplyTarget(ctx, db.UpdateChannelChatSessionBindingReplyTargetParams{
		ReplyChatSessionID: arg.ChatSessionID,
		LastMessageID:      arg.LastMessageID,
		LastThreadID:       arg.LastThreadID,
	})
}

// ---- 入站去重 ----

// ClaimLarkInboundDedup 认领一条入站消息的去重行。
func (s *ChannelStore) ClaimLarkInboundDedup(ctx context.Context, arg ClaimInboundDedupParams) (InboundMessageDedup, error) {
	row, err := s.Queries.ClaimChannelInboundDedup(ctx, db.ClaimChannelInboundDedupParams{
		InstallationID: arg.InstallationID,
		MessageID:      arg.MessageID,
	})
	if err != nil {
		return InboundMessageDedup{}, err
	}
	return dedupFromRow(row), nil
}

// MarkLarkInboundDedupProcessed 把已认领消息标记为已处理。
func (s *ChannelStore) MarkLarkInboundDedupProcessed(ctx context.Context, arg MarkInboundDedupProcessedParams) (int64, error) {
	return s.Queries.MarkChannelInboundDedupProcessed(ctx, db.MarkChannelInboundDedupProcessedParams{
		InstallationID: arg.InstallationID,
		MessageID:      arg.MessageID,
		ClaimToken:     arg.ClaimToken,
	})
}

// ReleaseLarkInboundDedup 处理失败时释放认领。
func (s *ChannelStore) ReleaseLarkInboundDedup(ctx context.Context, arg ReleaseInboundDedupParams) (int64, error) {
	return s.Queries.ReleaseChannelInboundDedup(ctx, db.ReleaseChannelInboundDedupParams{
		InstallationID: arg.InstallationID,
		MessageID:      arg.MessageID,
		ClaimToken:     arg.ClaimToken,
	})
}

// ---- 审计 ----

// RecordLarkInboundDrop 写一条入站丢弃审计行（生成新的 v7 UUID，打飞书渠道类型）。
func (s *ChannelStore) RecordLarkInboundDrop(ctx context.Context, arg RecordInboundDropParams) error {
	return s.Queries.RecordChannelInboundDrop(ctx, db.RecordChannelInboundDropParams{
		ID:               dbid.NewV7(),
		ChannelType:      channelTypeFeishu,
		EventType:        arg.EventType,
		DropReason:       arg.DropReason,
		InstallationID:   arg.InstallationID,
		ChannelChatID:    arg.ChannelChatID,
		ChannelEventID:   arg.ChannelEventID,
		ChannelMessageID: arg.ChannelMessageID,
	})
}

// ---- 绑定令牌 ----

// CreateLarkBindingToken 铸造一条绑定令牌（存 hash、channel_user_id 为飞书 open_id）。
func (s *ChannelStore) CreateLarkBindingToken(ctx context.Context, arg CreateBindingTokenParams) (BindingTokenRow, error) {
	row, err := s.Queries.CreateChannelBindingToken(ctx, db.CreateChannelBindingTokenParams{
		TokenHash:      arg.TokenHash,
		WorkspaceID:    arg.WorkspaceID,
		InstallationID: arg.InstallationID,
		ChannelType:    channelTypeFeishu,
		ChannelUserID:  arg.ChannelUserID,
		ExpiresAt:      arg.ExpiresAt,
	})
	if err != nil {
		return BindingTokenRow{}, err
	}
	return bindingTokenFromRow(row), nil
}

// ConsumeLarkBindingToken 消费一条绑定令牌（按 hash 标记已用）。
func (s *ChannelStore) ConsumeLarkBindingToken(ctx context.Context, tokenHash string) (BindingTokenRow, error) {
	row, err := s.Queries.ConsumeChannelBindingToken(ctx, tokenHash)
	if err != nil {
		return BindingTokenRow{}, err
	}
	return bindingTokenFromRow(row), nil
}

// ---- 出站卡片 ----

// GetLarkOutboundCardByTask 按 task 查飞书出站卡片。
func (s *ChannelStore) GetLarkOutboundCardByTask(ctx context.Context, taskID pgtype.UUID) (OutboundCardMessage, error) {
	row, err := s.Queries.GetChannelOutboundCardByTask(ctx, db.GetChannelOutboundCardByTaskParams{
		TaskID:      taskID,
		ChannelType: channelTypeFeishu,
	})
	if err != nil {
		return OutboundCardMessage{}, err
	}
	return outboundCardFromRow(row), nil
}

// CreateLarkOutboundCardMessage 记录一条出站卡片消息。
func (s *ChannelStore) CreateLarkOutboundCardMessage(ctx context.Context, arg CreateOutboundCardMessageParams) (OutboundCardMessage, error) {
	row, err := s.Queries.CreateChannelOutboundCardMessage(ctx, db.CreateChannelOutboundCardMessageParams{
		ChatSessionID:        arg.ChatSessionID,
		ChannelType:          channelTypeFeishu,
		ChannelChatID:        arg.ChannelChatID,
		ChannelCardMessageID: arg.ChannelCardMessageID,
		Status:               arg.Status,
		TaskID:               arg.TaskID,
	})
	if err != nil {
		return OutboundCardMessage{}, err
	}
	return outboundCardFromRow(row), nil
}

// UpdateLarkOutboundCardStatus 推进一条出站卡片的状态。
func (s *ChannelStore) UpdateLarkOutboundCardStatus(ctx context.Context, arg UpdateOutboundCardStatusParams) error {
	return s.Queries.UpdateChannelOutboundCardStatus(ctx, db.UpdateChannelOutboundCardStatusParams{
		ID:     arg.ID,
		Status: arg.Status,
	})
}

// installationsFromRows 把一批 channel_installation 行映射成领域 Installations，
// 遇到第一个 config 解码错误就上抛。
func installationsFromRows(rows []db.ChannelInstallation) ([]Installation, error) {
	out := make([]Installation, len(rows))
	for i, row := range rows {
		inst, err := installationFromRow(row)
		if err != nil {
			return nil, err
		}
		out[i] = inst
	}
	return out, nil
}
