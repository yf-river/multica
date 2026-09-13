// install.go：钉钉安装管理的后端——AppSecret 的静态加密、共用的 persistInstall
// 事务，以及安装的 list / get / revoke 管理面。
//
// 钉钉走 BYO（自带应用）模型：agent 所有者或工作区管理员自建钉钉 Stream 模式
// 机器人，再把 AppKey（client id）+ AppSecret（client secret）贴进 Multica
// （粘贴校验路径在 byo_install.go）。InstallService 负责 AppSecret 的静态加密——
// 任何调用方都不可能写出带明文密钥的 channel_installation——外加共用的
// persistInstall 事务与 list/get/revoke 管理操作。
package dingtalk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	// ErrInstallationNotFound：本工作区里查不到匹配的安装行。
	ErrInstallationNotFound = errors.New("dingtalk installation not found")
	// ErrRobotOwnedByAnotherWorkspace：贴来的钉钉机器人已被【另一个】Multica
	// 工作区里一个在线的 owner 连着——那会撞上 (channel_type, app_id) 路由索引。
	// 一个机器人就是一个 bot 身份、映射一个 agent；想复用它得先在另一工作区把它
	// 断开。
	ErrRobotOwnedByAnotherWorkspace = errors.New("dingtalk: this DingTalk robot is already connected to a different Multica workspace")
	// ErrRobotOwnedBySameWorkspace：机器人已连到【同一个】工作区里另一个（在线的、
	// 未归档的）agent。点名这一情况，用户才知道该去点哪个可操作的 Disconnect
	// （#4810）。
	ErrRobotOwnedBySameWorkspace = errors.New("dingtalk: this DingTalk robot is already connected to another agent in this workspace")
	// ErrRobotOwnedByArchivedAgent：机器人还挂在某个已归档的 agent 名下（归档可
	// 逆，所以机器人仍归它）。用户通过恢复那个 agent 或断开它的机器人来解套。
	ErrRobotOwnedByArchivedAgent = errors.New("dingtalk: this DingTalk robot is connected to an archived agent in this workspace")
)

// installQueries 是 InstallService 需要的生成查询子集。WithTx 返回绑到同一事务
// 的同一接口，让 persistInstall 的 upsert 原子执行（也让测试无需真库就能注入假
// 实现）。
type installQueries interface {
	WithTx(tx pgx.Tx) installQueries
	LockDingTalkInstallationOwner(ctx context.Context, arg db.LockDingTalkInstallationOwnerParams) error
	GetDingTalkInstallationOwnerForUpdate(ctx context.Context, arg db.GetDingTalkInstallationOwnerForUpdateParams) (db.GetDingTalkInstallationOwnerForUpdateRow, error)
	DeleteDingTalkInstallationForReplacement(ctx context.Context, arg db.DeleteDingTalkInstallationForReplacementParams) (pgtype.UUID, error)
	UpsertChannelInstallation(ctx context.Context, arg db.UpsertChannelInstallationParams) (db.ChannelInstallation, error)
	ReclaimDeadChannelInstallationByAppID(ctx context.Context, arg db.ReclaimDeadChannelInstallationByAppIDParams) (pgtype.UUID, error)
	GetChannelInstallationOwnerByAppID(ctx context.Context, arg db.GetChannelInstallationOwnerByAppIDParams) (db.GetChannelInstallationOwnerByAppIDRow, error)
	ListChannelInstallationsByWorkspace(ctx context.Context, arg db.ListChannelInstallationsByWorkspaceParams) ([]db.ChannelInstallation, error)
	GetChannelInstallationInWorkspace(ctx context.Context, arg db.GetChannelInstallationInWorkspaceParams) (db.ChannelInstallation, error)
	SetChannelInstallationStatus(ctx context.Context, arg db.SetChannelInstallationStatusParams) error
}

// dbInstallQueries 把 *db.Queries 适配成 installQueries——生成的 WithTx 返回的
// 是 *db.Queries，这里包一层让它返回接口（与 slack.InstallService 用的适配器
// 同款模式）。
type dbInstallQueries struct{ *db.Queries }

func (q dbInstallQueries) WithTx(tx pgx.Tx) installQueries {
	return dbInstallQueries{q.Queries.WithTx(tx)}
}

// InstallService 负责 AppSecret 的静态加密（任何调用方都写不出带明文密钥的
// channel_installation）与共用的安装事务。box 必须非 nil——连开发环境也拒绝明文
// 存储。
type InstallService struct {
	box               *secretbox.Box
	q                 installQueries
	tx                engine.TxStarter
	httpClient        *http.Client
	logger            *slog.Logger
	validationTimeout time.Duration

	// apiBase 覆盖钉钉 Open-API 的 base 地址，仅供 BYO 的 access_token 校验调用
	// 使用（测试指向 httptest server）。留空则走真实钉钉 API。
	apiBase string
}

// NewInstallService 把服务绑到查询集、事务启动器（*pgxpool.Pool）与加密盒上。
// 列出/吊销与 BYO 注册都只需要 box（静态加密密钥）；这里没有托管 OAuth 凭据。
func NewInstallService(q *db.Queries, tx engine.TxStarter, box *secretbox.Box, logger *slog.Logger) (*InstallService, error) {
	if q == nil {
		return nil, errors.New("dingtalk: InstallService requires queries")
	}
	return newInstallService(dbInstallQueries{q}, tx, box, logger)
}

// newInstallService 是可测的核心：它收 installQueries 接口，测试无需真库即可
// 注入假实现（外加假的 TxStarter）。
func newInstallService(q installQueries, tx engine.TxStarter, box *secretbox.Box, logger *slog.Logger) (*InstallService, error) {
	if box == nil {
		return nil, errors.New("dingtalk: InstallService requires a non-nil secretbox.Box")
	}
	if q == nil {
		return nil, errors.New("dingtalk: InstallService requires queries")
	}
	if tx == nil {
		return nil, errors.New("dingtalk: InstallService requires a tx starter")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &InstallService{
		box:               box,
		q:                 q,
		tx:                tx,
		httpClient:        http.DefaultClient,
		logger:            logger,
		validationTimeout: 10 * time.Second,
	}, nil
}

// installPersist 携带 persistInstall 落库所需的所有已解析字段。configJSON 里带
// AppKey（config->>'app_id'），供入站路由用；行本身以 (workspace, agent) 为主
// 键——每个 agent 一个机器人。
type installPersist struct {
	wsID        pgtype.UUID
	agentID     pgtype.UUID
	installerID pgtype.UUID
	// appIDKey 是存到 config->>'app_id' 的 AppKey；它必须与 configJSON 里的
	// app_id 相等。它驱动「死旧主回收」与「活主查询」，后者用于给出准确的冲突文案。
	appIDKey   string
	configJSON []byte
}

// pgUniqueViolation 是 Postgres 唯一约束冲突的 SQLSTATE 错误码。
const pgUniqueViolation = "23505"

// persistInstall 给每个 (workspace, agent) 存一条钉钉机器人。
//
// 重连【同一个】AppKey：原地更新行、保留该安装作用域的状态。换成【另一个】
// AppKey：废弃旧状态并插入全新 installation id——钉钉的 senderStaffId 只按组织
// 作用，用户与会话绑定绝不能从一种机器人身份迁移到另一种。
//
// (channel_type, app_id) 路由索引是【唯一另一条】唯一约束。它不是本次 upsert 的
// 冲突目标，所以把机器人绑到【不同】agent 会撞它。因此 upsert 前先回收该 AppKey
// 的「已死」旧主（已吊销的占位行、或所属工作区/agent 已被删除的孤儿），让机器人
// 能迁到新 agent；在线的旧主会撞唯一索引、被回以准确的冲突哨兵拒绝。
func (s *InstallService) persistInstall(ctx context.Context, p installPersist) (db.ChannelInstallation, error) {
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("begin install tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	// 替换会删除再重建唯一的 (workspace, agent, channel) 行。先把这条逻辑槽位在
	// 删建之间的空档串行化，防止并发安装去原地更新刚新建的身份行。
	if err := qtx.LockDingTalkInstallationOwner(ctx, db.LockDingTalkInstallationOwnerParams{
		WorkspaceID: p.wsID,
		AgentID:     p.agentID,
	}); err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("lock dingtalk installation owner: %w", err)
	}

	// upsert 前先把 (dingtalk, app_id) 路由槽从任何【已死】旧主手里腾出来——已吊销
	// 的占位行、或所属工作区/agent 已被删除的孤儿（#4810）——这样旧主已不在的机器
	// 人能重新绑定。在线的旧主（活跃 agent，含已归档的）原样留下，让它在下面的
	// upsert 撞唯一索引，再转成准确的冲突。
	if _, err := qtx.ReclaimDeadChannelInstallationByAppID(ctx, db.ReclaimDeadChannelInstallationByAppIDParams{
		ChannelType: string(TypeDingTalk),
		AppID:       p.appIDKey,
		WorkspaceID: p.wsID,
		AgentID:     p.agentID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// pgx.ErrNoRows 只是表示没有可回收的死主——无操作，不是失败。
		return db.ChannelInstallation{}, fmt.Errorf("reclaim dead dingtalk installation: %w", err)
	}

	current, err := qtx.GetDingTalkInstallationOwnerForUpdate(ctx, db.GetDingTalkInstallationOwnerForUpdateParams{
		WorkspaceID: p.wsID,
		AgentID:     p.agentID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return db.ChannelInstallation{}, fmt.Errorf("load current dingtalk installation: %w", err)
	}
	if err == nil && current.AppID != p.appIDKey {
		if _, err := qtx.DeleteDingTalkInstallationForReplacement(ctx, db.DeleteDingTalkInstallationForReplacementParams{
			InstallationID: current.ID,
			WorkspaceID:    p.wsID,
			AgentID:        p.agentID,
		}); err != nil {
			return db.ChannelInstallation{}, fmt.Errorf("retire replaced dingtalk installation: %w", err)
		}
	}

	inst, err := qtx.UpsertChannelInstallation(ctx, db.UpsertChannelInstallationParams{
		WorkspaceID:     p.wsID,
		AgentID:         p.agentID,
		ChannelType:     string(TypeDingTalk),
		Config:          p.configJSON,
		InstallerUserID: p.installerID,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return db.ChannelInstallation{}, s.liveOwnerConflictErr(ctx, p.wsID, p.appIDKey)
		}
		return db.ChannelInstallation{}, fmt.Errorf("upsert dingtalk installation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("commit dingtalk install: %w", err)
	}
	return inst, nil
}

// liveOwnerConflictErr 在「死主回收」跑完后，判断 (dingtalk, app_id) 路由槽到底
// 被谁占着，让 persistInstall 能返回一个 handler 渲染成精确文案的哨兵，而不是永远
// 甩锅「另一个工作区」的笼统错误（#4810）。死主回收跑在已失败 upsert 的事务上，
// 这里改在基础连接（s.q）上读——失败 upsert 已经 abort 了原事务。槽已空（并发断
// 连）或查询出错时，回退到通用的跨工作区哨兵——重试一次就能成功。
func (s *InstallService) liveOwnerConflictErr(ctx context.Context, requestingWorkspaceID pgtype.UUID, appID string) error {
	owner, err := s.q.GetChannelInstallationOwnerByAppID(ctx, db.GetChannelInstallationOwnerByAppIDParams{
		ChannelType: string(TypeDingTalk),
		AppID:       appID,
	})
	if err != nil {
		return ErrRobotOwnedByAnotherWorkspace
	}
	switch {
	case owner.WorkspaceID != requestingWorkspaceID:
		return ErrRobotOwnedByAnotherWorkspace
	case owner.AgentArchivedAt.Valid:
		return ErrRobotOwnedByArchivedAgent
	default:
		return ErrRobotOwnedBySameWorkspace
	}
}

// ListByWorkspace 返回工作区内全部钉钉安装（含已吊销），供管理面展示。
func (s *InstallService) ListByWorkspace(ctx context.Context, wsID pgtype.UUID) ([]db.ChannelInstallation, error) {
	return s.q.ListChannelInstallationsByWorkspace(ctx, db.ListChannelInstallationsByWorkspaceParams{
		WorkspaceID: wsID,
		ChannelType: string(TypeDingTalk),
	})
}

// GetInWorkspace 是限定工作区的按 id 查找：其它工作区伪造的安装 id 返回
// NotFound，而不是泄露「这个 id 是否存在」。
func (s *InstallService) GetInWorkspace(ctx context.Context, id, wsID pgtype.UUID) (db.ChannelInstallation, error) {
	inst, err := s.q.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID:          id,
		WorkspaceID: wsID,
		ChannelType: string(TypeDingTalk),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ChannelInstallation{}, ErrInstallationNotFound
		}
		return db.ChannelInstallation{}, err
	}
	return inst, nil
}

// Revoke 把状态翻成 'revoked'。行保留作审计；重装时再翻回 'active'。Supervisor
// 停止监督该安装（ListActiveInstallations 只过滤 active），它的 Stream 连接随之收
// 尾，出站也停发。
func (s *InstallService) Revoke(ctx context.Context, id pgtype.UUID) error {
	return s.q.SetChannelInstallationStatus(ctx, db.SetChannelInstallationStatusParams{
		ID:     id,
		Status: "revoked",
	})
}
