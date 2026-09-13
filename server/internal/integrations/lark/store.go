// store.go：飞书集成的 channel 化存储层——channel_* 表与其 JSONB config 的
// 序列化/反序列化边界。
//
// MUL-3515 把 lark_* 表泛化成了 channel_*（一个 channel_type 判别列 + 一个
// 存放平台特定标识/凭据的 JSONB `config` blob）。本文件是包内唯一处理这个
// JSONB (反)序列化的边界：包其余部分继续使用扁平领域结构体，其字段与退役的
// db.Lark* 行一一对应，因此调用点的改动只是机械改名而不是重塑。
//
// 飞书 config blob 恰好携带原先平铺在 lark_installation / lark_user_binding
// 上的那些列：
//
//	installation: app_id, app_secret_encrypted (base64), tenant_key,
//	              bot_open_id, bot_union_id, region
//	user binding: union_id
//
// app_secret_encrypted 是 secretbox 密文，以 base64 字符串存储。解码器刻意容忍
// 空白：迁移回填经 PostgreSQL encode(...,'base64') 写入，它会每 76 字符做一次
// MIME 换行，而 ~72 字节的密封 secret 正好超过这个长度；编码器始终输出不换行
// 的 base64，所以 Go 写的行本就干净；读时剥掉换行让两种来源可互换。

package lark

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Installation 是 channel_installation 行的扁平、飞书形态视图。它保持与被替代
// 的 lark_installation 行字段对等，所以切换在 ~190 个调用点上只是改名。飞书
// 特有字段（AppID、AppSecretEncrypted、TenantKey、BotOpenID、BotUnionID、
// Region）来自 JSONB config；其余是平铺列。
type Installation struct {
	ID                 pgtype.UUID
	WorkspaceID        pgtype.UUID
	AgentID            pgtype.UUID
	AppID              string
	AppSecretEncrypted []byte
	TenantKey          pgtype.Text
	BotOpenID          string
	InstallerUserID    pgtype.UUID
	Status             string
	WsLeaseToken       pgtype.Text
	WsLeaseExpiresAt   pgtype.Timestamptz
	InstalledAt        pgtype.Timestamptz
	CreatedAt          pgtype.Timestamptz
	UpdatedAt          pgtype.Timestamptz
	BotUnionID         pgtype.Text
	Region             string
}

// UserBinding 是 channel_user_binding 行的扁平视图。ChannelUserID 即飞书
// open_id；UnionID（次要身份）放在 JSONB config 里。
type UserBinding struct {
	ID             pgtype.UUID
	WorkspaceID    pgtype.UUID
	MulticaUserID  pgtype.UUID
	InstallationID pgtype.UUID
	ChannelUserID  string
	UnionID        pgtype.Text
	BoundAt        pgtype.Timestamptz
}

// ChatSessionBinding 是 channel_chat_session_binding 行的扁平视图。
type ChatSessionBinding struct {
	ID             pgtype.UUID
	ChatSessionID  pgtype.UUID
	InstallationID pgtype.UUID
	ChannelChatID  string
	ChatType       string
	// Config 在 ChannelChatID 是复合 "chat:thread"（话题隔离键）时携带真正的
	// 聊天 id（larkBindingConfig）；否则是 "{}"。
	Config        []byte
	CreatedAt     pgtype.Timestamptz
	LastMessageID pgtype.Text
	LastThreadID  pgtype.Text
}

// InboundMessageDedup 是 channel_inbound_message_dedup 行的扁平视图。每个字段
// 都是平铺列（无 JSON），因此与 channel 行 1:1 镜像。
type InboundMessageDedup struct {
	InstallationID pgtype.UUID
	MessageID      string
	ReceivedAt     pgtype.Timestamptz
	ProcessedAt    pgtype.Timestamptz
	ClaimToken     pgtype.UUID
}

// BindingTokenRow 是 channel_binding_token 行的扁平视图。ChannelUserID 是令牌
// 兑换后将绑定的飞书 open_id。（命名 *Row 是为避免与 binding_token.go 里
// BindingToken——返回给调用方的“刚铸造原始令牌”形态——冲突。）
type BindingTokenRow struct {
	TokenHash      string
	WorkspaceID    pgtype.UUID
	InstallationID pgtype.UUID
	ChannelUserID  string
	ExpiresAt      pgtype.Timestamptz
	ConsumedAt     pgtype.Timestamptz
	CreatedAt      pgtype.Timestamptz
}

// OutboundCardMessage 是 channel_outbound_card_message 行的扁平视图。
type OutboundCardMessage struct {
	ID                   pgtype.UUID
	ChatSessionID        pgtype.UUID
	TaskID               pgtype.UUID
	ChannelChatID        string
	ChannelCardMessageID string
	Status               string
	LastPatchedAt        pgtype.Timestamptz
	CreatedAt            pgtype.Timestamptz
}

// feishuInstallConfig 是飞书渠道 channel_installation.config 的 JSON 形态。
// app_secret_encrypted 手工解码（见 decodeSecret）而不是用 json []byte 字段，
// 好让 SQL 回填写入的 MIME 换行 base64 也能往返。omitempty 对应迁移里的
// jsonb_strip_nulls。
type feishuInstallConfig struct {
	AppID              string `json:"app_id"`
	AppSecretEncrypted string `json:"app_secret_encrypted,omitempty"`
	TenantKey          string `json:"tenant_key,omitempty"`
	BotOpenID          string `json:"bot_open_id,omitempty"`
	BotUnionID         string `json:"bot_union_id,omitempty"`
	Region             string `json:"region,omitempty"`
}

// feishuBindingConfig 是 channel_user_binding.config 的 JSON 形态。
type feishuBindingConfig struct {
	UnionID string `json:"union_id,omitempty"`
}

// installationFromRow 把 channel_installation 行（平铺列 + JSONB config）解码成
// 扁平 Installation 领域结构体。
func installationFromRow(row db.ChannelInstallation) (Installation, error) {
	var cfg feishuInstallConfig
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &cfg); err != nil {
			return Installation{}, fmt.Errorf("decode installation config: %w", err)
		}
	}
	secret, err := decodeSecret(cfg.AppSecretEncrypted)
	if err != nil {
		return Installation{}, fmt.Errorf("decode app_secret_encrypted: %w", err)
	}
	return Installation{
		ID:                 row.ID,
		WorkspaceID:        row.WorkspaceID,
		AgentID:            row.AgentID,
		AppID:              cfg.AppID,
		AppSecretEncrypted: secret,
		TenantKey:          textOrNull(cfg.TenantKey),
		BotOpenID:          cfg.BotOpenID,
		InstallerUserID:    row.InstallerUserID,
		Status:             row.Status,
		WsLeaseToken:       row.WsLeaseToken,
		WsLeaseExpiresAt:   row.WsLeaseExpiresAt,
		InstalledAt:        row.InstalledAt,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
		BotUnionID:         textOrNull(cfg.BotUnionID),
		Region:             cfg.Region,
	}, nil
}

// encodeInstallConfig 从 Installation 的飞书字段构建 channel_installation.config
// JSONB。secret 以不换行的 base64 输出。
func encodeInstallConfig(inst Installation) ([]byte, error) {
	cfg := feishuInstallConfig{
		AppID:      inst.AppID,
		TenantKey:  inst.TenantKey.String,
		BotOpenID:  inst.BotOpenID,
		BotUnionID: inst.BotUnionID.String,
		Region:     inst.Region,
	}
	if len(inst.AppSecretEncrypted) > 0 {
		cfg.AppSecretEncrypted = base64.StdEncoding.EncodeToString(inst.AppSecretEncrypted)
	}
	return json.Marshal(cfg)
}

// userBindingFromRow 把 channel_user_binding 行解码成 UserBinding。
func userBindingFromRow(row db.ChannelUserBinding) (UserBinding, error) {
	var cfg feishuBindingConfig
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &cfg); err != nil {
			return UserBinding{}, fmt.Errorf("decode user binding config: %w", err)
		}
	}
	return UserBinding{
		ID:             row.ID,
		WorkspaceID:    row.WorkspaceID,
		MulticaUserID:  row.MulticaUserID,
		InstallationID: row.InstallationID,
		ChannelUserID:  row.ChannelUserID,
		UnionID:        textOrNull(cfg.UnionID),
		BoundAt:        row.BoundAt,
	}, nil
}

// encodeBindingConfig 从 UserBinding 构建 channel_user_binding.config。返回做
// 过 null-strip 的 JSON（union_id 缺席即 "{}"），这样 upsert 里的
// `config || jsonb_strip_nulls(EXCLUDED.config)` 合并不会用这次写入覆盖掉先前
// 捕获的 union_id。
func encodeBindingConfig(b UserBinding) ([]byte, error) {
	return json.Marshal(feishuBindingConfig{UnionID: b.UnionID.String})
}

// chatSessionBindingFromRow 把 channel_chat_session_binding 行拷贝进扁平领域
// 结构体。Config 保持原始字节；outboundChatID 会解码它。
func chatSessionBindingFromRow(row db.ChannelChatSessionBinding) ChatSessionBinding {
	return ChatSessionBinding{
		ID:             row.ID,
		ChatSessionID:  row.ChatSessionID,
		InstallationID: row.InstallationID,
		ChannelChatID:  row.ChannelChatID,
		ChatType:       row.ChatType,
		Config:         row.Config,
		CreatedAt:      row.CreatedAt,
		LastMessageID:  row.LastMessageID,
		LastThreadID:   row.LastThreadID,
	}
}

// dedupFromRow 把 channel_inbound_message_dedup 行拷贝进扁平领域结构体。没有
// JSON：每个字段都是平铺列。
func dedupFromRow(row db.ChannelInboundMessageDedup) InboundMessageDedup {
	return InboundMessageDedup{
		InstallationID: row.InstallationID,
		MessageID:      row.MessageID,
		ReceivedAt:     row.ReceivedAt,
		ProcessedAt:    row.ProcessedAt,
		ClaimToken:     row.ClaimToken,
	}
}

// bindingTokenFromRow 把 channel_binding_token 行拷贝进扁平领域结构体。
func bindingTokenFromRow(row db.ChannelBindingToken) BindingTokenRow {
	return BindingTokenRow{
		TokenHash:      row.TokenHash,
		WorkspaceID:    row.WorkspaceID,
		InstallationID: row.InstallationID,
		ChannelUserID:  row.ChannelUserID,
		ExpiresAt:      row.ExpiresAt,
		ConsumedAt:     row.ConsumedAt,
		CreatedAt:      row.CreatedAt,
	}
}

// outboundCardFromRow 把 channel_outbound_card_message 行拷贝进扁平领域结构体。
func outboundCardFromRow(row db.ChannelOutboundCardMessage) OutboundCardMessage {
	return OutboundCardMessage{
		ID:                   row.ID,
		ChatSessionID:        row.ChatSessionID,
		TaskID:               row.TaskID,
		ChannelChatID:        row.ChannelChatID,
		ChannelCardMessageID: row.ChannelCardMessageID,
		Status:               row.Status,
		LastPatchedAt:        row.LastPatchedAt,
		CreatedAt:            row.CreatedAt,
	}
}

// decodeSecret 对存储的 app secret 密文做 base64 解码。它容忍 PostgreSQL
// encode(...,'base64') 引入的换行包裹，所以 SQL 回填与 encodeInstallConfig
// 写的 secret 都能解码。空字符串产出 nil 切片（表示注册到一半、secret 还没
// 密封的安装）。
func decodeSecret(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(stripWhitespace(s))
}

// stripWhitespace 移除 MIME base64 换行引入的 ASCII 空白。
func stripWhitespace(s string) string {
	if !strings.ContainsAny(s, "\n\r \t") {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		default:
			return r
		}
	}, s)
}
