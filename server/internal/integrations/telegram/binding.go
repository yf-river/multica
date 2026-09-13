package telegram

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 角色定位：Telegram 的"用户身份绑定"令牌流程，对齐 slack/binding.go，只是查询全部走
// 带 channel_type='telegram' 的通用 channel_* 表。场景：一个未绑定的 Telegram 用户给
// bot 发消息，会收到"请先绑定账号"的引导（见 replier.go / outbound.go 的提示文案），
// 点开引导链接进入产品内的兑换页；兑换成功后，该用户的 Telegram 数字 id 就与他的
// Multica 账号绑定，此后引擎按消息逐条查这条绑定关系来判定"这条消息属于谁"。
//
// 安全设计核心：库里只存令牌的 SHA-256 哈希（防库被拖走时令牌被冒用）；令牌是一次性的
//（兑换即消费）；"兑换 + 建绑定"在一个事务里提交，兑换失败不会白白烧掉令牌。

// BindingTokenTTL 限定令牌有效期；channel_binding_token 表上的 CHECK 约束同时强制
// 同样的 15 分钟上限——代码与数据库两侧一致，防止只改一侧造成漂移。
const BindingTokenTTL = 15 * time.Minute

var (
	// ErrBindingTokenInvalid：令牌未知 / 已被消费 / 已过期。三种失败合并成同一个
	// 不透明错误，是为了避免给攻击者提供"令牌是否有效"的时序侧信道（重放探测）。
	ErrBindingTokenInvalid = errors.New("telegram: binding token invalid or expired")
	// ErrBindingAlreadyAssigned：这个 Telegram 用户 id 已经绑到另一个不同的 Multica 用户。
	ErrBindingAlreadyAssigned = errors.New("telegram: user id is already bound to a different user")
	// ErrBindingNotWorkspaceMember：兑换者不是该令牌所属工作区的成员。
	ErrBindingNotWorkspaceMember = errors.New("telegram: redeemer is not a workspace member")
)

// BindingToken 是刚签发出来的令牌；原始值 Raw 只在签发这一刻原样返回给调用方（用于拼
// 兑换链接），此后系统只保存它的哈希。
type BindingToken struct {
	Raw       string
	ExpiresAt time.Time
}

// RedeemedBindingToken 是成功兑换后的结果，携带兑换出的绑定三元组。
type RedeemedBindingToken struct {
	WorkspaceID    pgtype.UUID
	InstallationID pgtype.UUID
	TelegramUserID string
}

// BindingTokenService 负责签发与兑换 Telegram 绑定令牌。兑换是事务性的：消费令牌与写入
// 绑定行一起提交，所以一次失败的绑定永远不会烧掉令牌（中途任何一步报错都会回滚消费）。
type BindingTokenService struct {
	q   *db.Queries
	tx  engine.TxStarter
	now func() time.Time
}

// NewBindingTokenService 构造服务；now 可注入便于测试时间相关的过期逻辑。
func NewBindingTokenService(q *db.Queries, tx engine.TxStarter) *BindingTokenService {
	return &BindingTokenService{q: q, tx: tx, now: time.Now}
}

// Mint 为 (installation, telegramUserID) 签发一枚一次性绑定令牌。
func (s *BindingTokenService) Mint(ctx context.Context, workspaceID, installationID pgtype.UUID, telegramUserID string) (BindingToken, error) {
	raw, err := randomBindingToken(32)
	if err != nil {
		return BindingToken{}, fmt.Errorf("generate token: %w", err)
	}
	expiresAt := s.now().Add(BindingTokenTTL)
	// 落库的是哈希而非原文；Raw 只存在于返回值里，随后由调用方拼进兑换链接（HTTPS）。
	if _, err := s.q.CreateChannelBindingToken(ctx, db.CreateChannelBindingTokenParams{
		TokenHash:      hashBindingToken(raw),
		WorkspaceID:    workspaceID,
		InstallationID: installationID,
		ChannelType:    string(TypeTelegram),
		ChannelUserID:  telegramUserID,
		ExpiresAt:      pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		return BindingToken{}, fmt.Errorf("persist token: %w", err)
	}
	return BindingToken{Raw: raw, ExpiresAt: expiresAt}, nil
}

// RedeemAndBind 原子地完成两件事：消费一枚原始令牌 + 把 Telegram 用户 id 绑定到
// multicaUserID。multicaUserID 必须取自兑换者当前的登录会话，绝不来自令牌本身
// （令牌里只有"来自哪个工作区/哪个安装/哪个 Telegram 用户"，没有任何能冒充的字段）。
func (s *BindingTokenService) RedeemAndBind(ctx context.Context, raw string, multicaUserID pgtype.UUID) (RedeemedBindingToken, error) {
	if s.tx == nil {
		return RedeemedBindingToken{}, errors.New("telegram: BindingTokenService missing TxStarter")
	}
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return RedeemedBindingToken{}, fmt.Errorf("begin tx: %w", err)
	}
	// 兜底回滚：成功路径走到 Commit 后，defer 里的 Rollback 只会返回"事务已结束"的
	// 空错误并被丢弃，不会造成额外影响。
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	row, err := qtx.ConsumeChannelBindingToken(ctx, hashBindingToken(raw))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RedeemedBindingToken{}, ErrBindingTokenInvalid
		}
		return RedeemedBindingToken{}, fmt.Errorf("consume token: %w", err)
	}
	if err := validateBindingTokenChannel(row); err != nil {
		// 令牌表是所有渠道适配器共用的。这里防止"拿着别家渠道发的令牌跑来 Telegram
		// 兑换"：直接返回会让消费随外层事务一起回滚，令牌不会被烧掉。
		return RedeemedBindingToken{}, err
	}

	// 显式成员资格闸门（无成员外键约束）：在 Commit 之前返回即回滚消费，
	// 因此非成员的兑换尝试不会白白烧掉一张令牌。
	if _, err := qtx.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      multicaUserID,
		WorkspaceID: row.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RedeemedBindingToken{}, ErrBindingNotWorkspaceMember
		}
		return RedeemedBindingToken{}, fmt.Errorf("check membership: %w", err)
	}

	// 建绑定行。SQL 语义（channel.sql:CreateChannelUserBinding）：冲突时只在
	// multica_user_id 与既有行一致时才 DO UPDATE——即"同一个人重复绑定"静默更新并返回
	// 新行；若是**另一个人**想抢这个已被绑定的 Telegram 用户 id，UPDATE 匹配零行，
	// RETURNING 无结果、上层拿到 pgx.ErrNoRows，这里就映射为 ErrBindingAlreadyAssigned。
	if _, err := qtx.CreateChannelUserBinding(ctx, db.CreateChannelUserBindingParams{
		WorkspaceID:    row.WorkspaceID,
		MulticaUserID:  multicaUserID,
		InstallationID: row.InstallationID,
		ChannelType:    string(TypeTelegram),
		ChannelUserID:  row.ChannelUserID,
		Config:         []byte(`{}`),
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RedeemedBindingToken{}, ErrBindingAlreadyAssigned
		}
		return RedeemedBindingToken{}, fmt.Errorf("create binding: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return RedeemedBindingToken{}, fmt.Errorf("commit: %w", err)
	}
	return RedeemedBindingToken{
		WorkspaceID:    row.WorkspaceID,
		InstallationID: row.InstallationID,
		TelegramUserID: row.ChannelUserID,
	}, nil
}

// validateBindingTokenChannel 校验被消费的令牌确实是 telegram 渠道发的。
// 不匹配时返回"令牌无效"这个不透明错误，避免泄露令牌所属渠道的细节。
func validateBindingTokenChannel(row db.ChannelBindingToken) error {
	if row.ChannelType != string(TypeTelegram) {
		return ErrBindingTokenInvalid
	}
	return nil
}

// randomBindingToken 生成 n 字节的密码学随机数并编码为 URL 安全 base64 字符串。
// 32 字节 = 256 位熵，足够防止暴力猜测。
func randomBindingToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashBindingToken 计算令牌的 SHA-256 摘要（十六进制）。库里存的一律是这个摘要。
func hashBindingToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
