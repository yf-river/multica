// binding.go —— 企业微信智能机器人「用户绑定 token」流程。一个还没绑定的企微
// 用户给机器人发消息，会收到一条「请先绑定你的账号」的引导（token 在这里铸
// 造，由 OutboundReplier 送出），点开链接进入产品内的兑换页后，他的企微
// userid 就绑定到了自己的 Multica 账号上。实现与 slack.BindingTokenService
// 对齐——跑在通用的 channel_binding_token / channel_user_binding 表上，
// channel_type='wecom'。
//
// 为什么必须显式绑定：aibot 智能机器人事件携带的是匿名 userid（"T" 前缀），
// 它在每个 (bot, user) 内稳定，但和企业真实的 userid / 邮箱毫无关系。任何
// 隐式身份推断（邮箱前缀匹配、企业用户查询）都走不通。显式绑定表是唯一正确
// 的答案。
package wecom

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

// BindingTokenTTL 限定一枚 token 的存活时间。channel_binding_token 上的 CHECK
// 约束也强制同一个 15 分钟上限，防止配错的调用方铸出更长的 token。
const BindingTokenTTL = 15 * time.Minute

// BindingTokenMintInterval 是同一个平台用户两次领取新绑定链接之间的最小间隔。
// 每个未绑定用户的消息都会走到 needs_binding 这个结局，所以如果没有这个下限，
// 用户一口气敲六行字就会写六条 token、收到六个链接——而其中只有最后一个他会点。
//
// 这是个「突发窗口」，不是整场会话的闸门，区别很重要。压制一次铸造等于断言
// 「之前发出的那条链接已经到了用户手上」，而这一点没有任何校验：send 在传输层
// 接受帧的那一刻就返回了，socket 的 ack 是异步的。如果第一条引导真丢了，一个
// 过长的窗口会把它变成死局——每条消息都回「点我发给你的链接」，指的却是根本没
// 到的东西，用户得等窗口过去才能绑定。
//
// 上面说的是「发出去没回音」的情况；「确知发失败」也一样被覆盖：token 行在投递
// 尝试之前就写好了，所以一条被传输层直接拒绝的引导（重连中/租约翻转期间没有
// 存活连接）照样压掉接下来一分钟的铸造。这里对那个错误没有任何反应——约束伤害
// 的是窗口本身而不是投递回执，这也是窗口必须短的另一个理由。
//
// 六十秒足够完成这个节流本来的任务：一口气打的六行字只写一行，一直打下去的用户
// 最多一分钟写一行——而 token 行十五分钟就过期。判断错了的代价是「多发一条消
// 息」，不是「机器人絮絮叨叨十分钟说自己已经答过了」。
//
// 它必须稳稳落在 BindingTokenTTL 之内，好让被节流的用户被指回的那条链接仍留有
// 真实的剩余时间。
const BindingTokenMintInterval = time.Minute

var (
	// ErrBindingTokenInvalid：token 未知 / 已被消费 / 已过期。三种情况共用一个
	// 不透明错误，避免被当成判断「重放时机」的旁路信号（oracle）。
	ErrBindingTokenInvalid = errors.New("wecom: binding token invalid or expired")
	// ErrBindingAlreadyAssigned：这个企微 userid 已经绑到了另一个 Multica 用户
	// 名下（账号转移必须走显式解绑，第一版迭代未实现——管理员可以直接删行）。
	ErrBindingAlreadyAssigned = errors.New("wecom: user id is already bound to a different user")
	// ErrBindingNotWorkspaceMember：兑换者不是 token 所属工作区的成员。
	// 在 HTTP 边界翻译成 403。
	ErrBindingNotWorkspaceMember = errors.New("wecom: redeemer is not a workspace member")
)

// BindingToken 是一枚新铸出的 token。原始值只完整返回一次（嵌进绑定 URL）；
// 落库的只有它的哈希。
type BindingToken struct {
	Raw       string
	ExpiresAt time.Time

	// Reused 表示节流压掉了这次铸造，因为一条活链接已经躺在用户会话里。此场景
	// Raw 为空、也没有办法找回它——表里从头到尾只存过哈希——所以调用方只能让
	// 用户回头去点更早的那条消息，而不是拼一个新的 URL。ExpiresAt 携带的是那条
	// 活 token 的过期时间，不是新铸 token 的。
	Reused bool
}

// RedeemedBindingToken 是一次成功兑换后返回的结果。
type RedeemedBindingToken struct {
	WorkspaceID    pgtype.UUID
	InstallationID pgtype.UUID
	WecomUserID    string
}

// bindingMintQueries 是铸造路径要用到的生成查询的切片。刻意收窄：铸造是整个
// 服务里唯一不在事务内运行的部分，所以也是唯一能用假实现驱动单元测试的部分。
// *db.Queries 满足该接口。
type bindingMintQueries interface {
	CreateChannelBindingToken(ctx context.Context, arg db.CreateChannelBindingTokenParams) (db.ChannelBindingToken, error)
	FindLiveChannelBindingToken(ctx context.Context, arg db.FindLiveChannelBindingTokenParams) (db.ChannelBindingToken, error)
}

// BindingTokenService 负责铸造和兑换企微绑定 token。兑换是事务性的：消费 token
// 与插入 channel_user_binding 行一起提交，所以一次失败的绑定绝不会烧掉一枚
// token。
type BindingTokenService struct {
	q    *db.Queries
	mint bindingMintQueries
	tx   engine.TxStarter
	now  func() time.Time
}

// NewBindingTokenService 构造该服务。tx（*pgxpool.Pool）用于事务化的兑换路径。
func NewBindingTokenService(q *db.Queries, tx engine.TxStarter) *BindingTokenService {
	return &BindingTokenService{q: q, mint: q, tx: tx, now: time.Now}
}

// Mint 为 (installation, wecomUserID) 铸造一枚一次性绑定 token，返回原始值 +
// 过期时间。原始值必须走 aibot WebSocket 送出（传输过程由平台加密），绝不写
// 日志。
//
// 有节流：如果该用户已有一条在 BindingTokenMintInterval 内铸出、且尚未消费的
// 活 token，则不写行，返回结果带 Reused、Raw 为空——原始密钥从未落库，自然也
// 没有东西可以交还。此时调用方应让用户去点会话里已有的那条链接。正是这个机制
// 防止未绑定用户的消息流「一条消息一行 token」。
//
// 是尽力而为，不是保证：查活 token 和插入是两条语句，所以同一用户的两个并发
// 回复可能同时查空、同时铸造。实际上它们被拉开很大间距——入站读取循环一次只
// 分发一帧，第二条回复整整落后一个分发周期——而且抢输的代价只是一行 token 加
// 一条链接，都只属于那个用户本人、都按常规 TTL 过期。想再收紧就得在入站热路径
// 上持锁，为一个装饰性节流不值当。
func (s *BindingTokenService) Mint(ctx context.Context, workspaceID, installationID pgtype.UUID, wecomUserID string) (BindingToken, error) {
	if s.mint == nil {
		return BindingToken{}, errors.New("wecom: BindingTokenService missing queries")
	}
	live, err := s.mint.FindLiveChannelBindingToken(ctx, db.FindLiveChannelBindingTokenParams{
		InstallationID: installationID,
		ChannelType:    channelTypeWecom,
		ChannelUserID:  wecomUserID,
		MintInterval:   pgtype.Interval{Microseconds: BindingTokenMintInterval.Microseconds(), Valid: true},
	})
	switch {
	case err == nil:
		return BindingToken{ExpiresAt: live.ExpiresAt.Time, Reused: true}, nil
	case errors.Is(err, pgx.ErrNoRows):
		// 该用户名下没有活 token——继续走下面新铸一枚。
	default:
		return BindingToken{}, fmt.Errorf("wecom: look up live token: %w", err)
	}

	raw, err := randomBindingToken(32)
	if err != nil {
		return BindingToken{}, fmt.Errorf("wecom: generate token: %w", err)
	}
	expiresAt := s.now().Add(BindingTokenTTL)
	if _, err := s.mint.CreateChannelBindingToken(ctx, db.CreateChannelBindingTokenParams{
		TokenHash:      hashBindingToken(raw),
		WorkspaceID:    workspaceID,
		InstallationID: installationID,
		ChannelType:    channelTypeWecom,
		ChannelUserID:  wecomUserID,
		ExpiresAt:      pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		return BindingToken{}, fmt.Errorf("wecom: persist token: %w", err)
	}
	return BindingToken{Raw: raw, ExpiresAt: expiresAt}, nil
}

// RedeemAndBind 原子地消费一枚原始 token，并把企微 userid 绑定到 multicaUserID
// 名下（后者取自会话，绝不取自 token）。可能返回 ErrBindingTokenInvalid /
// ErrBindingAlreadyAssigned / ErrBindingNotWorkspaceMember。
func (s *BindingTokenService) RedeemAndBind(ctx context.Context, raw string, multicaUserID pgtype.UUID) (RedeemedBindingToken, error) {
	if s.tx == nil {
		return RedeemedBindingToken{}, errors.New("wecom: BindingTokenService missing TxStarter")
	}
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return RedeemedBindingToken{}, fmt.Errorf("wecom: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	row, err := qtx.ConsumeChannelBindingToken(ctx, hashBindingToken(raw))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RedeemedBindingToken{}, ErrBindingTokenInvalid
		}
		return RedeemedBindingToken{}, fmt.Errorf("wecom: consume token: %w", err)
	}
	// 防线：token 行的 channel_type 必须是 'wecom'——兑换接口是按 wecom 定的域，
	// 但它下面的表是各平台共用的。不然偷到一条 slack token 的人把链接粘到这里，
	// 就会经由 wecom 接口悄悄绑走一个 slack userid。
	if row.ChannelType != channelTypeWecom {
		return RedeemedBindingToken{}, ErrBindingTokenInvalid
	}

	// 显式成员资格闸门（没有成员外键）：在 Commit 之前就 return 会把消费回滚，
	// 所以非成员的一次尝试不会烧掉 token。
	if _, err := qtx.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      multicaUserID,
		WorkspaceID: row.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RedeemedBindingToken{}, ErrBindingNotWorkspaceMember
		}
		return RedeemedBindingToken{}, fmt.Errorf("wecom: check membership: %w", err)
	}

	if _, err := qtx.CreateChannelUserBinding(ctx, db.CreateChannelUserBindingParams{
		WorkspaceID:    row.WorkspaceID,
		MulticaUserID:  multicaUserID,
		InstallationID: row.InstallationID,
		ChannelType:    channelTypeWecom,
		ChannelUserID:  row.ChannelUserID,
		Config:         []byte(`{}`),
	}); err != nil {
		// pgx.ErrNoRows 说明已有绑定指向了另一个用户——ON CONFLICT DO UPDATE
		// WHERE multica_user_id=… 这个守门条件把它拦下了。
		if errors.Is(err, pgx.ErrNoRows) {
			return RedeemedBindingToken{}, ErrBindingAlreadyAssigned
		}
		return RedeemedBindingToken{}, fmt.Errorf("wecom: create binding: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return RedeemedBindingToken{}, fmt.Errorf("wecom: commit: %w", err)
	}
	return RedeemedBindingToken{
		WorkspaceID:    row.WorkspaceID,
		InstallationID: row.InstallationID,
		WecomUserID:    row.ChannelUserID,
	}, nil
}

// randomBindingToken 用加密安全随机源生成 n 字节、再经 base64url 编码的原始
// token 字符串（32 字节 ≈ 43 个字符）。
func randomBindingToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashBindingToken 计算原始 token 的 SHA-256 十六进制摘要——落库只存摘要，
// 库被拖走也换不回能用的 token。
func hashBindingToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
