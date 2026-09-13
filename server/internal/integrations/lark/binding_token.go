// binding_token.go：飞书“绑定令牌”的铸造与兑换服务——支撑“你还没绑定，点
// 这里”的扫码绑定流程。
package lark

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
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// BindingToken 是刚铸造的令牌的对外形态。原始 token 只此一次返回给调用方——
// 它是 Bot 回复的绑定 URL 里内嵌的、不可猜测的秘密。本次调用返回后，服务器端
// 只存 hash，原始值无法从 DB 恢复。
type BindingToken struct {
	Raw       string
	ExpiresAt time.Time
}

// RedeemedBindingToken 是成功兑换后返回给调用方的行。兑换路径用这些字段来写
// lark_user_binding 行。
type RedeemedBindingToken struct {
	WorkspaceID    pgtype.UUID
	InstallationID pgtype.UUID
	LarkOpenID     OpenID
}

// InstallerBinder 是 RegistrationService 需要的窄接口：在和安装插入同一个业务
// 步骤里记录安装者的 lark_user_binding 行。没有这一步，安装者发的第一条入站
// 消息就会被当作 `unbound_user` 丢弃，而 Bot 会向这个几秒前刚授权安装的人回复
// “你还没绑定，点这里……”
//
// 实现必须在 (installation_id, lark_open_id) 上幂等：同一用户重装不应报错。
//
// `qtx` 是执行绑定的 channel 化句柄。由调用方开事务，让安装插入与绑定写入
// 一起提交；nil 表示“用服务自己的（非事务）queries 句柄”。
type InstallerBinder interface {
	BindInstallerTx(ctx context.Context, qtx *ChannelStore, p InstallerBindParams) error
}

// InstallerBindParams 携带 InstallerBinder 需要的入参。保持结构体形态，将来
// 加 union_id（Phase 2）不会破坏调用方。
type InstallerBindParams struct {
	WorkspaceID    pgtype.UUID
	InstallationID pgtype.UUID
	MulticaUserID  pgtype.UUID // 安装者的 Multica 账号
	LarkOpenID     OpenID      // 安装者在该安装下的 open_id
}

// BindingTokenService 铸造与兑换“你还没绑定，点这里”流程用的绑定令牌。TTL
// 固定为 BindingTokenTTL（15 分钟）；DB CHECK 施加同样的上限，配置错误的调用方
// 也无法悄悄铸造更长命的令牌。
//
// 兑换（RedeemAndBind）是事务性的：消费令牌与插入 lark_user_binding 行一起
// 提交——绑定失败不会烧掉令牌；绑定成功也不会留下“已消费但没用上”的令牌。
type BindingTokenService struct {
	queries *ChannelStore
	tx      TxStarter
	now     func() time.Time
}

// NewBindingTokenService 构造默认服务。时钟可注入，方便测试固定时间得到确定的
// 过期行为；生产调用方用带 time.Now 的 NewBindingTokenServiceWithClock。
func NewBindingTokenService(queries *db.Queries, tx TxStarter) *BindingTokenService {
	return NewBindingTokenServiceWithClock(queries, tx, time.Now)
}

// NewBindingTokenServiceWithClock 是给测试留的接缝；生产调用方应使用
// NewBindingTokenService。queries 被包进 ChannelStore，使 lark_* 调用落到
// channel_* 行（MUL-3515）。
func NewBindingTokenServiceWithClock(queries *db.Queries, tx TxStarter, now func() time.Time) *BindingTokenService {
	return &BindingTokenService{queries: NewChannelStore(queries), tx: tx, now: now}
}

// Mint 铸造一个新的单次绑定令牌，返回原始 secret 与过期时间。原始值必须经
// 安全通道发给预期接收者——飞书私聊在传输层由平台加密——并且绝不记日志。
// Mint 是本包里唯一产出原始令牌的函数；此后的读取都按 hash 进行。
func (s *BindingTokenService) Mint(ctx context.Context, workspaceID, installationID pgtype.UUID, openID OpenID) (BindingToken, error) {
	raw, err := randomToken(32)
	if err != nil {
		return BindingToken{}, fmt.Errorf("generate token: %w", err)
	}
	hash := hashToken(raw)
	expiresAt := s.now().Add(BindingTokenTTL)

	row, err := s.queries.CreateLarkBindingToken(ctx, CreateBindingTokenParams{
		TokenHash:      hash,
		WorkspaceID:    workspaceID,
		InstallationID: installationID,
		ChannelUserID:  string(openID),
		ExpiresAt:      pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return BindingToken{}, fmt.Errorf("persist token: %w", err)
	}
	return BindingToken{Raw: raw, ExpiresAt: row.ExpiresAt.Time}, nil
}

// RedeemAndBind 在单个 DB 事务里原子地消费一个原始令牌并写入 lark_user_binding
// 行。兑换者的身份取自已传的 multicaUserID（由 handler 从会话取，绝不取自令牌），
// 所以偷来的令牌无法把一个飞书 open_id 绑到攻击者账号上。
//
// 失败模式以类型化错误返回：
//
//   - ErrBindingTokenInvalid：令牌不存在 / 已消费 / 已过期。三种情况返回同一个
//     不透明错误，避免给重放竞争提供时序 oracle。
//
//   - ErrBindingAlreadyAssigned：该 (installation, open_id) 已存在绑定，且指向
//     一个【不同】的 Multica 用户。这种情况【不】消费令牌——回滚，既不影响
//     现有绑定的真正持有者，也让 ops 仍可显式吊销多余的令牌。账号转移必须走
//     显式 unbind，而不是靠兑换。
//
//   - ErrBindingNotWorkspaceMember：兑换者不是该令牌所属 workspace 的成员
//     （会撞上 member(workspace_id, user_id) 复合外键）。同样回滚。
//
// 成功路径上消费与绑定一起提交：返回成功保证 consumed_at 写入与绑定行都落库；
// 返回错误则保证两者都没落。
func (s *BindingTokenService) RedeemAndBind(ctx context.Context, raw string, multicaUserID pgtype.UUID) (RedeemedBindingToken, error) {
	if s.tx == nil {
		return RedeemedBindingToken{}, errors.New("lark: BindingTokenService missing TxStarter")
	}
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return RedeemedBindingToken{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	row, err := qtx.ConsumeLarkBindingToken(ctx, hashToken(raw))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RedeemedBindingToken{}, ErrBindingTokenInvalid
		}
		return RedeemedBindingToken{}, fmt.Errorf("consume token: %w", err)
	}

	// 显式成员资格闸门。曾用来拒绝非成员兑换者的 lark_user_binding -> member
	// 外键已移除（MUL-3515 §4），所以在这里检查。在 Commit 前 return 会把消费
	// 回滚，因此非成员的尝试不会烧掉令牌——与外键违例的结果一致。
	isMember, err := qtx.IsWorkspaceMember(ctx, row.WorkspaceID, multicaUserID)
	if err != nil {
		return RedeemedBindingToken{}, fmt.Errorf("check membership: %w", err)
	}
	if !isMember {
		return RedeemedBindingToken{}, ErrBindingNotWorkspaceMember
	}

	_, err = qtx.CreateLarkUserBinding(ctx, CreateUserBindingParams{
		WorkspaceID:    row.WorkspaceID,
		MulticaUserID:  multicaUserID,
		InstallationID: row.InstallationID,
		ChannelUserID:  row.ChannelUserID,
	})
	if err != nil {
		// 这里的 pgx.ErrNoRows 表示冲突行存在但其 multica_user_id 与我们不同，
		// 因此 ON CONFLICT DO UPDATE 上的 WHERE 子句拒绝了这次重绑。见
		// queries/channel.sql 里 CreateChannelUserBinding 的注释。
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
		LarkOpenID:     OpenID(row.ChannelUserID),
	}, nil
}

// BindInstallerTx 是设备流安装的自动绑定路径：刚授权安装的用户被记录为绑到
// 自己的 open_id，于是 Bot 私聊里的第一条入站消息能通过 `bound` 身份检查，用户
// 不会被塞一张多余的“点这里绑定”卡片。
//
// `qtx` 是 RegistrationService 的事务范围 queries 句柄。服务开的事务包裹了
// lark_installation 插入与这次绑定写入，半吊子的安装（只有安装行、没有安装者
// 绑定）落不了地。`qtx` 为 nil 时用服务自己的（非事务）queries 句柄——对不
// 需要原子性的测试来说正合适。
//
// 令牌兑换刻意不共享这条代码路径：
//   - RedeemAndBind 在同一个事务里消费服务器铸造的令牌并插入绑定；这正是防
//     重放的机制。
//   - BindInstallerTx 由设备流成功钩子调用，其身份权威证明是飞书验证过的轮询
//     响应（open_id 与刚铸造的 client_id / client_secret 一起返回）。没有令牌可
//     消费，人为造一个反而只会扩大攻击面。
//
// 底层 CreateLarkUserBinding 查询在 multica_user_id 匹配时对 (installation_id,
// lark_open_id) 幂等（ON CONFLICT DO UPDATE 的门控写在 SQL 上），所以同一用户
// 重装只是无操作的元数据刷新。换成【不同】用户重装会以 ErrBindingAlreadyAssigned
// 暴露——registration 调用方把它当作硬错误，前端展示成“这个 Lark 账号已在别处
// 绑定”，防止一个 workspace 管理员悄悄改绑另一个人的 PersonalAgent 安装。
func (s *BindingTokenService) BindInstallerTx(ctx context.Context, qtx *ChannelStore, p InstallerBindParams) error {
	q := qtx
	if q == nil {
		q = s.queries
	}
	// 显式成员资格闸门，替代被移除的 member 外键（MUL-3515 §4）：安装者必须
	// 是它要绑入的 workspace 的成员。
	isMember, err := q.IsWorkspaceMember(ctx, p.WorkspaceID, p.MulticaUserID)
	if err != nil {
		return fmt.Errorf("check membership: %w", err)
	}
	if !isMember {
		return ErrBindingNotWorkspaceMember
	}
	_, err = q.CreateLarkUserBinding(ctx, CreateUserBindingParams{
		WorkspaceID:    p.WorkspaceID,
		MulticaUserID:  p.MulticaUserID,
		InstallationID: p.InstallationID,
		ChannelUserID:  string(p.LarkOpenID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBindingAlreadyAssigned
		}
		return fmt.Errorf("bind installer: %w", err)
	}
	return nil
}

// ErrBindingTokenInvalid 是 RedeemAndBind 在令牌 hash 不存在、令牌已消费或已
// 过期时返回的错误。调用方绝不能区分这些子情况——这种区分会给令牌重放竞争
// 提供时序 oracle，而且对产品毫无价值（无论哪种，用户看到的都是同一句“链接
// 无效或已过期，请重新申请”）。
var ErrBindingTokenInvalid = errors.New("binding token invalid or expired")

// ErrBindingAlreadyAssigned 是 RedeemAndBind 在 (installation, open_id) 已有
// lark_user_binding 行且指向不同 Multica 用户时返回的错误。账号转移必须走显式
// unbind 流程；绑定令牌不能用来从别的用户手里抢走一个已绑定的 open_id。
var ErrBindingAlreadyAssigned = errors.New("lark open_id is already bound to a different user")

// ErrBindingNotWorkspaceMember 是 RedeemAndBind 与 BindInstallerTx 在用户不是
// （或不再是）目标 workspace 成员时返回的错误，由显式 IsWorkspaceMember 检查
// 发现（MUL-3515 §4 移除了原先强制该约束的 member 外键）。HTTP 边界转成 403。
var ErrBindingNotWorkspaceMember = errors.New("redeemer is not a workspace member")

// randomToken 生成 n 字节的密码学随机令牌并做 URL-safe base64 编码。
func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	// URL-safe 让令牌能干净地嵌进绑定 URL 而无需转义。RawURLEncoding 去掉了
	// `=` 填充（解码器本来就允许省略），否则它会在用户可见的 URL 里显得难看。
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken 用 SHA-256 哈希原始令牌，返回 hex 字符串。DB 里只存 hash。
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
