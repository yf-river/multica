// feishu_channel.go：channel.Channel 的飞书实现——把飞书接入到与渠道无关的
// engine 的第一个适配器。
package lark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// feishuChannel 是 channel.Channel 的飞书实现——第一个由与渠道无关的引擎
// （MUL-3620）驱动的适配器。它包住既有的 Lark 传输层：Connect 为这个安装跑
// 共享的 WS 长连接 connector，把每个解码事件规范化为 channel.InboundMessage
// 后交给引擎的共享入站 handler（经 channel.Config.Handler 注入的 Router）；
// Send 通过 Lark HTTP API 发文本回复。每个 channel_installation 由注册的
// Factory 构建一个实例；connector 在实例间共享。
//
// Channel 只持有 Connect/Send 需要的凭据（从每安装的 config blob 解码出来）。
// 安装的【身份】（workspace / agent / installer）由 Router 的
// InstallationResolver 逐消息解析，所以这里刻意不持有。
type feishuChannel struct {
	inst    Installation
	conn    EventConnector
	handler channel.InboundHandler
	sender  APIClient
	creds   CredentialsResolver
	logger  *slog.Logger
}

var _ channel.Channel = (*feishuChannel)(nil)

// Type 返回该渠道的类型标识（飞书）。
func (c *feishuChannel) Type() channel.Type { return channel.TypeFeishu }

// Connect 为这个安装跑飞书长连接，阻塞到 ctx 被取消或链路断开——engine.Supervisor
// 正是靠这个契约把租约续期与连接存活绑在一起。每个解码事件被规范化为
// channel.InboundMessage 并交给引擎 handler。connector 丢弃 (DispatchResult)
// 返回值、只对 error 反应，所以流回来的就是 handler 的 error。
func (c *feishuChannel) Connect(ctx context.Context) error {
	return c.conn.Run(ctx, c.inst, func(emitCtx context.Context, lm InboundMessage) (DispatchResult, error) {
		if c.handler == nil {
			return DispatchResult{}, errors.New("lark: inbound handler not configured")
		}
		return DispatchResult{}, c.handler(emitCtx, channelMessageFromLark(lm))
	})
}

// Disconnect 是空操作：connector 的接收循环靠 ctx 取消来拆除（Supervisor 会
// 取消 run 的 ctx）。可安全重复调用。
func (c *feishuChannel) Disconnect(ctx context.Context) error { return nil }

// Send 通过 Lark IM API 发送一条最小文本回复。富卡片、媒体和流式卡片 patch
// 仍走既有的 Patcher / OutcomeReplier 路径；这里是跨平台 OutboundMessage 路径。
func (c *feishuChannel) Send(ctx context.Context, out channel.OutboundMessage) (channel.SendResult, error) {
	if c.sender == nil {
		return channel.SendResult{}, errors.New("lark: api client not configured")
	}
	creds, err := c.installationCredentials()
	if err != nil {
		return channel.SendResult{}, err
	}
	msgID, err := c.sender.SendTextMessage(ctx, SendTextParams{
		InstallationID: creds,
		ChatID:         ChatID(out.ChatID),
		Text:           out.Text,
		ReplyTarget:    outboundReplyTarget(out),
	})
	if err != nil {
		return channel.SendResult{}, err
	}
	return channel.SendResult{MessageID: msgID}, nil
}

// Capabilities 声明飞书适配器支持的能力。只是声明——引擎不做任何降级处理
// （见 channel.Capability 文档）。
func (c *feishuChannel) Capabilities() channel.Capability {
	return channel.CapText |
		channel.CapRichCard |
		channel.CapThreadReply |
		channel.CapQuoteReply |
		channel.CapAttachment |
		channel.CapTypingIndicator |
		channel.CapMessageEdit
}

// installationCredentials 解析本安装的调用凭据。
func (c *feishuChannel) installationCredentials() (InstallationCredentials, error) {
	return installationCredentialsFor(c.inst, c.creds)
}

// installationCredentialsFor 用 resolver 解密安装行的 app_secret，组装成飞书
// API 凭据。
func installationCredentialsFor(inst Installation, resolver CredentialsResolver) (InstallationCredentials, error) {
	if resolver == nil {
		return InstallationCredentials{}, errors.New("lark: credentials resolver missing")
	}
	secret, err := resolver.DecryptAppSecret(inst)
	if err != nil {
		return InstallationCredentials{}, fmt.Errorf("decrypt app_secret: %w", err)
	}
	creds := InstallationCredentials{
		AppID:     inst.AppID,
		AppSecret: secret,
		Region:    RegionOrDefault(inst.Region),
	}
	if inst.TenantKey.Valid {
		creds.TenantKey = inst.TenantKey.String
	}
	return creds, nil
}

// channelMessageFromLark 把解码后的飞书 InboundMessage 规范化为跨平台
// channel.InboundMessage。原结构体被塞进 Raw，让飞书 resolvers 能读到规范化
// 信封没携带的平台特有字段（app_id、event_type、创建时间）。CommandBody 映射
// 到规范化的 CommandText 字段，因为命令分类是共享的。
func channelMessageFromLark(lm InboundMessage) channel.InboundMessage {
	raw, _ := json.Marshal(lm)
	var reply *channel.ReplyCtx
	if lm.ParentID != "" || lm.RootID != "" {
		reply = &channel.ReplyCtx{MessageID: lm.ParentID, RootID: lm.RootID}
	}
	return channel.InboundMessage{
		EventID:        lm.EventID,
		MessageID:      lm.MessageID,
		Type:           channelMsgType(lm.MessageType),
		Text:           lm.Body,
		CommandText:    lm.CommandBody,
		ReplyTo:        reply,
		AddressedToBot: lm.AddressedToBot,
		ForceFresh:     lm.ForceFreshSession,
		Source: channel.Source{
			ChannelType: channel.TypeFeishu,
			ChatID:      string(lm.ChatID),
			ChatType:    channel.ChatType(string(lm.ChatType)),
			SenderID:    string(lm.SenderOpenID),
			ThreadID:    lm.ThreadID,
		},
		Raw: raw,
	}
}

// channelMsgType 把原始 Lark msg_type 映射到规范化枚举。文本类 Lark 类型
// （text / post / merge_forward / interactive）都压平为 text（人类可读内容在
// Body 里）；媒体类型一一映射过去。
func channelMsgType(larkMsgType string) channel.MsgType {
	switch larkMsgType {
	case "image":
		return channel.MsgTypeImage
	case "file":
		return channel.MsgTypeFile
	case "audio":
		return channel.MsgTypeAudio
	case "media", "video":
		return channel.MsgTypeVideo
	case "", "text", "post", "merge_forward", "interactive":
		return channel.MsgTypeText
	default:
		return channel.MsgTypeUnknown
	}
}

// outboundReplyTarget 把跨平台 OutboundMessage 的回复提示映射成 Lark
// ReplyTarget。只有引用回复（设置了 ReplyTo）才走回复接口；裸的“继续某话题”
// 退化为普通群聊发送。
func outboundReplyTarget(out channel.OutboundMessage) ReplyTarget {
	if out.ReplyTo == "" {
		return ReplyTarget{}
	}
	return ReplyTarget{MessageID: out.ReplyTo, InThread: out.ThreadID != ""}
}

// FeishuChannelDeps 打包飞书 Factory 闭包捕获的共享依赖：WS connector（一个
// 共享实例）、出站 HTTP client、凭据 resolver。入站 handler 由引擎在每次构建
// 时经 channel.Config.Handler 提供。
type FeishuChannelDeps struct {
	Connector   EventConnector
	APIClient   APIClient
	Credentials CredentialsResolver
	Logger      *slog.Logger
}

// RegisterFeishu 在 reg 上以 channel.TypeFeishu 注册飞书 Factory，使
// engine.Supervisor 能为每个安装构建 feishuChannel。“加一个渠道”就等于这一
// 次调用加适配器——不需要改引擎。
func RegisterFeishu(reg *channel.Registry, deps FeishuChannelDeps) {
	reg.Register(channel.TypeFeishu, newFeishuFactory(deps))
}

// newFeishuFactory 返回一个按安装构建 feishuChannel 的 channel.Factory。
func newFeishuFactory(deps FeishuChannelDeps) channel.Factory {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(cfg channel.Config) (channel.Channel, error) {
		if deps.Connector == nil {
			return nil, errors.New("lark: feishu factory missing connector")
		}
		// cfg.Raw 是每安装的 config blob（channel_installation 的 .config JSONB）：
		// app_id、加密 app_secret、tenant_key、region……。我们从它构建一个只带
		// 凭据的 Installation；workspace / agent 身份由 Router 逐消息解析，这里
		// 不需要。
		inst, err := installationFromRow(db.ChannelInstallation{
			ChannelType: channelTypeFeishu,
			Config:      cfg.Raw,
		})
		if err != nil {
			return nil, fmt.Errorf("decode feishu installation config: %w", err)
		}
		return &feishuChannel{
			inst:    inst,
			conn:    deps.Connector,
			handler: cfg.Handler,
			sender:  deps.APIClient,
			creds:   deps.Credentials,
			logger:  logger,
		}, nil
	}
}

// channelInstallationStore 把 *db.Queries 适配成 engine.InstallationStore。它
// 枚举全部渠道类型的活跃安装（替代旧的“只枚举飞书”启动清单），并管理每安装
// 的 WS 租约，把每行翻译成引擎的 Installation：ChannelType 选择 Factory，
// Config 原样携带平台 config JSONB，Fingerprint 是对 channel_type + config 的
// 通用哈希，凭据轮换会因此触发任何平台的连接重建。
type channelInstallationStore struct {
	q *db.Queries
}

// ChannelInstallationStore 暴露引擎使用的两条 PostgreSQL 接缝：持久化安装
// 发现与显式回滚的租约后端。
type ChannelInstallationStore interface {
	engine.InstallationStore
	engine.LeaseStore
}

// NewChannelInstallationStore 构建基于泛化 channel_* 表的 engine.InstallationStore。
func NewChannelInstallationStore(q *db.Queries) ChannelInstallationStore {
	return &channelInstallationStore{q: q}
}

// ListActiveInstallations 实现 engine.InstallationStore：列出全部活跃安装。
func (s *channelInstallationStore) ListActiveInstallations(ctx context.Context) ([]engine.Installation, error) {
	rows, err := s.q.ListAllActiveChannelInstallations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]engine.Installation, 0, len(rows))
	for _, row := range rows {
		out = append(out, engine.Installation{
			ID:          row.ID,
			ChannelType: channel.Type(row.ChannelType),
			Fingerprint: rowFingerprint(row),
			Config:      row.Config,
		})
	}
	return out, nil
}

// PostgreSQL 仍是显式的回滚/自托管租约后端。它的清扫优化不返回任何已知
// 持有者，所以 SQL 的 CAS 保持权威，每个无主安装在每次轮询里只尝试一次
// （绝不会出现盲目的按安装循环）。
func (s *channelInstallationStore) ListHeldWSLeases(_ context.Context, _ []pgtype.UUID) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

// TryAcquireWSLease 实现 engine.LeaseStore 的抢占。
func (s *channelInstallationStore) TryAcquireWSLease(ctx context.Context, arg engine.AcquireLeaseParams) error {
	return s.acquireOrRenewWSLease(ctx, arg)
}

// RenewWSLease 实现 engine.LeaseStore 的续期。
func (s *channelInstallationStore) RenewWSLease(ctx context.Context, arg engine.AcquireLeaseParams) error {
	return s.acquireOrRenewWSLease(ctx, arg)
}

// acquireOrRenewWSLease 是抢占/续期共用的 SQL CAS。
func (s *channelInstallationStore) acquireOrRenewWSLease(ctx context.Context, arg engine.AcquireLeaseParams) error {
	_, err := s.q.AcquireChannelWSLease(ctx, db.AcquireChannelWSLeaseParams{
		NewToken:     pgtype.Text{String: arg.Token, Valid: true},
		NewExpiresAt: pgtype.Timestamptz{Time: arg.ExpiresAt, Valid: true},
		ID:           arg.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.ErrLeaseNotAcquired
		}
		return err
	}
	return nil
}

// ReleaseWSLease 实现 engine.LeaseStore 的释放。
func (s *channelInstallationStore) ReleaseWSLease(ctx context.Context, arg engine.ReleaseLeaseParams) error {
	return s.q.ReleaseChannelWSLease(ctx, db.ReleaseChannelWSLeaseParams{
		ID:           arg.ID,
		CurrentToken: pgtype.Text{String: arg.Token, Valid: true},
	})
}

var _ engine.InstallationStore = (*channelInstallationStore)(nil)
var _ engine.LeaseStore = (*channelInstallationStore)(nil)

// rowFingerprint 把 channel_installation 行携带凭据的 config 浓缩成不透明
// 字符串。平台 config 的任何变化（飞书重装会轮换 app_id / app_secret / region）
// 都会翻转指纹，Supervisor 据此重启连接。config JSONB 只携带 secret 密文
// （绝无明文），所以对它做哈希既安全又与渠道无关——不直接读任何平台字段。
func rowFingerprint(row db.ChannelInstallation) string {
	h := sha256.New()
	_, _ = h.Write([]byte(row.ChannelType))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(row.Config)
	return hex.EncodeToString(h.Sum(nil))
}
