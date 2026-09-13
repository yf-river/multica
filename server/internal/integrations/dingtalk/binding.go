// binding.go：钉钉用户「绑定令牌」的铸造与兑换——把钉钉用户 id 绑定到 Multica
// 账号，是「你还没绑定，点这里关联」流程的后端。
//
// 流程：未绑定的钉钉用户给 bot 发消息，会收到一条引导「关联你的账号」（令牌在
// 这里铸造、由 OutboundReplier 送达），点开站内兑换页完成兑换后，他的钉钉 staff
// id 就与 Multica 账号绑定了。整体镜像 slack.BindingTokenService，只是跑在
// channel_type='dingtalk' 的通用 channel_* 查询上；兑换时还会额外校验令牌确属
// 钉钉渠道（见 validateBindingTokenChannel）。
package dingtalk

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

// BindingTokenTTL 限制令牌的有效期。channel_binding_token 表上的 CHECK 约束同步
// 了这同一个 15 分钟上限——即便调用方配置失误，也造不出更长命的令牌。
const BindingTokenTTL = 15 * time.Minute

var (
	// ErrBindingTokenInvalid：令牌不存在 / 已被消费 / 已过期。三种失败统一用这
	// 一个不透明的错误，避免暴露「到底是哪种」给攻击者当重放试探的时间差探针
	// （防时序 oracle）。
	ErrBindingTokenInvalid = errors.New("dingtalk: binding token invalid or expired")
	// ErrBindingAlreadyAssigned：这个钉钉用户 id 已绑给别的 Multica 用户（账号
	// 过户必须走显式解绑）。
	ErrBindingAlreadyAssigned = errors.New("dingtalk: user id is already bound to a different user")
	// ErrBindingNotWorkspaceMember：兑换者不是该令牌所属工作区的成员。在 HTTP
	// 边界会被翻译成 403。
	ErrBindingNotWorkspaceMember = errors.New("dingtalk: redeemer is not a workspace member")
)

// BindingToken 是一枚刚铸造出来的令牌。原始值只返回一次（嵌在绑定 URL 里），
// 库里只落它的哈希。
type BindingToken struct {
	Raw       string
	ExpiresAt time.Time
}

// RedeemedBindingToken 是兑换成功后的结果。
type RedeemedBindingToken struct {
	WorkspaceID    pgtype.UUID
	InstallationID pgtype.UUID
	DingTalkUserID string
}

// BindingTokenService 负责铸造与兑换钉钉绑定令牌。兑换是事务性的：消费令牌和
// 插入 channel_user_binding 行要么一起成功要么一起回滚，绑定失败绝不烧令牌。
type BindingTokenService struct {
	q   *db.Queries
	tx  engine.TxStarter
	now func() time.Time
}

// NewBindingTokenService 构造服务。tx（*pgxpool.Pool）供事务性兑换路径使用。
func NewBindingTokenService(q *db.Queries, tx engine.TxStarter) *BindingTokenService {
	return &BindingTokenService{q: q, tx: tx, now: time.Now}
}

// Mint 为 (installation, dingtalkUserID) 铸造一枚一次性绑定令牌，返回原始密钥与
// 过期时间。原始值必须经钉钉送达用户（传输全程被平台加密），且绝不可写日志。
func (s *BindingTokenService) Mint(ctx context.Context, workspaceID, installationID pgtype.UUID, dingtalkUserID string) (BindingToken, error) {
	raw, err := randomBindingToken(32)
	if err != nil {
		return BindingToken{}, fmt.Errorf("generate token: %w", err)
	}
	expiresAt := s.now().Add(BindingTokenTTL)
	if _, err := s.q.CreateChannelBindingToken(ctx, db.CreateChannelBindingTokenParams{
		TokenHash:      hashBindingToken(raw),
		WorkspaceID:    workspaceID,
		InstallationID: installationID,
		ChannelType:    string(TypeDingTalk),
		ChannelUserID:  dingtalkUserID,
		ExpiresAt:      pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		return BindingToken{}, fmt.Errorf("persist token: %w", err)
	}
	return BindingToken{Raw: raw, ExpiresAt: expiresAt}, nil
}

// RedeemAndBind 原子地消费一枚原始令牌，并把钉钉用户 id 绑定到 multicaUserID
// （该 id 取自当前会话，绝不取自令牌）。可能返回 ErrBindingTokenInvalid /
// ErrBindingAlreadyAssigned / ErrBindingNotWorkspaceMember。
func (s *BindingTokenService) RedeemAndBind(ctx context.Context, raw string, multicaUserID pgtype.UUID) (RedeemedBindingToken, error) {
	if s.tx == nil {
		return RedeemedBindingToken{}, errors.New("dingtalk: BindingTokenService missing TxStarter")
	}
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return RedeemedBindingToken{}, fmt.Errorf("begin tx: %w", err)
	}
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
		// 消费与绑定共享同一事务，这里 return 会把「消费令牌」一并回滚。其它适配
		// 器铸的令牌绝不能拿它来建一条指向该适配器安装的钉钉绑定。
		return RedeemedBindingToken{}, err
	}

	// 显式的工作区成员校验（没有成员外键可用）：在 Commit 之前 return 会顺带把
	// 「消费令牌」一起回滚，非成员的兑换尝试不会浪费掉这枚令牌。
	if _, err := qtx.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      multicaUserID,
		WorkspaceID: row.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RedeemedBindingToken{}, ErrBindingNotWorkspaceMember
		}
		return RedeemedBindingToken{}, fmt.Errorf("check membership: %w", err)
	}

	if _, err := qtx.CreateChannelUserBinding(ctx, db.CreateChannelUserBindingParams{
		WorkspaceID:    row.WorkspaceID,
		MulticaUserID:  multicaUserID,
		InstallationID: row.InstallationID,
		ChannelType:    string(TypeDingTalk),
		ChannelUserID:  row.ChannelUserID,
		Config:         []byte(`{}`),
	}); err != nil {
		// pgx.ErrNoRows 表示该 ChannelUserID 已有绑定、且指向的是另一个用户——
		// 是 SQL 里 ON CONFLICT DO UPDATE WHERE multica_user_id=… 的门槛在拒绝
		// 我们覆盖别人的绑定，按「已被占用」处理。
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
		DingTalkUserID: row.ChannelUserID,
	}, nil
}

// validateBindingTokenChannel 拒绝拿非钉钉渠道铸的令牌来钉钉兑换：channel_binding_token
// 表是多适配器共用的，跨适配器的令牌绝不能变成一条钉钉绑定。
func validateBindingTokenChannel(row db.ChannelBindingToken) error {
	if row.ChannelType != string(TypeDingTalk) {
		return ErrBindingTokenInvalid
	}
	return nil
}

// randomBindingToken 用密码学安全随机数生成 n 字节，再以 URL 安全的 base64（无
// 填充）编码——令牌会进绑定 URL，不能带 +/ 或 = 这类跟 URL 语法打架的字符。
func randomBindingToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashBindingToken 对原始令牌做 SHA-256 并返回十六进制串。库里只存哈希：万一库
// 被拖走，攻击者也拿不到能直接兑换的令牌原文。
func hashBindingToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
