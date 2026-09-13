// install.go：Slack 安装管理的后端（MUL-3666）——令牌的静态加密、共用的
// persistInstall 事务，以及安装的 list / get / revoke 管理面。
//
// Slack 走 BYO（自带应用）模型：工作区管理员自建 Slack 应用、装进自己的 Slack
// 工作区，再把 bot 令牌（xoxb-）和应用级令牌（xapp-）贴进 Multica（粘贴校验
// 的路径在 byo_install.go）。InstallService 负责这些令牌的静态加密——任何调用
// 方都不可能写出带明文令牌的 channel_installation——外加共用的 persistInstall
// 事务与 list/get/revoke 管理操作。
package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	// ErrInstallationNotFound：本工作区里查不到匹配的安装行。
	ErrInstallationNotFound = errors.New("slack installation not found")
	// ErrTeamOwnedByAnotherWorkspace：贴来的 Slack 应用已被【另一个】 Multica
	// 工作区里一个在线的 owner 连着——那会撞上 (channel_type, app_id) 路由索引。
	// 一个 Slack 应用就是一个 bot 身份、映射一个 agent；想在这里复用它，得先
	// 在另一个工作区把它断开。
	ErrTeamOwnedByAnotherWorkspace = errors.New("slack: this Slack app is already connected to a different Multica workspace")
	// ErrTeamOwnedBySameWorkspace：应用已连到【同一个】工作区里另一个（在线的、
	// 未归档的）agent。早先那个笼统的错误一律赖「别的 workspace」，报得不准；
	// 单独点名同工作区的情况，用户才知道该去点哪个可操作的 Disconnect（#4810）。
	ErrTeamOwnedBySameWorkspace = errors.New("slack: this Slack app is already connected to another agent in this workspace")
	// ErrTeamOwnedByArchivedAgent：应用还挂在某个已归档的 agent 名下（归档可逆，
	// 所以 bot 仍归它）。用户通过恢复那个 agent 或断开它的 bot 来解套。
	ErrTeamOwnedByArchivedAgent = errors.New("slack: this Slack app is connected to an archived agent in this workspace")
)

// installQueries 是 InstallService 需要的生成查询子集。WithTx 返回绑到同一事务
// 的同一接口，让 persistInstall 的 upsert 原子执行（也让测试无需真库就能注入
// 假实现）。
type installQueries interface {
	WithTx(tx pgx.Tx) installQueries
	UpsertChannelInstallation(ctx context.Context, arg db.UpsertChannelInstallationParams) (db.ChannelInstallation, error)
	ReclaimDeadChannelInstallationByAppID(ctx context.Context, arg db.ReclaimDeadChannelInstallationByAppIDParams) (pgtype.UUID, error)
	GetChannelInstallationOwnerByAppID(ctx context.Context, arg db.GetChannelInstallationOwnerByAppIDParams) (db.GetChannelInstallationOwnerByAppIDRow, error)
	ListChannelInstallationsByWorkspace(ctx context.Context, arg db.ListChannelInstallationsByWorkspaceParams) ([]db.ChannelInstallation, error)
	GetChannelInstallationInWorkspace(ctx context.Context, arg db.GetChannelInstallationInWorkspaceParams) (db.ChannelInstallation, error)
	SetChannelInstallationStatus(ctx context.Context, arg db.SetChannelInstallationStatusParams) error
}

// dbInstallQueries 把 *db.Queries 适配成 installQueries——生成的 WithTx 返回的
// 是 *db.Queries，这里包一层让它返回接口（与 engine.ChatSession 用的适配器
// 同款模式）。
type dbInstallQueries struct{ *db.Queries }

func (q dbInstallQueries) WithTx(tx pgx.Tx) installQueries {
	return dbInstallQueries{q.Queries.WithTx(tx)}
}

// InstallService 负责 bot + app 令牌的静态加密（任何调用方都写不出带明文令牌
// 的 channel_installation）与共用的安装事务。box 必须非 nil——连开发环境也拒绝
// 明文存储。
type InstallService struct {
	box        *secretbox.Box
	q          installQueries
	tx         engine.TxStarter
	httpClient *http.Client
	logger     *slog.Logger

	// apiURL 覆盖 Slack API 的 base 地址，仅供 BYO 的 auth.test 调用使用（测试
	// 指向 httptest server）。留空则走真实 Slack API。
	apiURL string
}

// NewInstallService 把服务绑到查询集、事务启动器（*pgxpool.Pool）与加密盒上。
// 列出/吊销与 BYO 注册都只需要 box（静态加密密钥）；这里没有托管 OAuth 凭据。
func NewInstallService(q *db.Queries, tx engine.TxStarter, box *secretbox.Box, logger *slog.Logger) (*InstallService, error) {
	if q == nil {
		return nil, errors.New("slack: InstallService requires queries")
	}
	return newInstallService(dbInstallQueries{q}, tx, box, logger)
}

// newInstallService 是可测的核心：它收 installQueries 接口，测试无需真库即可
// 注入假实现（外加假的 TxStarter）。
func newInstallService(q installQueries, tx engine.TxStarter, box *secretbox.Box, logger *slog.Logger) (*InstallService, error) {
	if box == nil {
		return nil, errors.New("slack: InstallService requires a non-nil secretbox.Box")
	}
	if q == nil {
		return nil, errors.New("slack: InstallService requires queries")
	}
	if tx == nil {
		return nil, errors.New("slack: InstallService requires a tx starter")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &InstallService{
		box:        box,
		q:          q,
		tx:         tx,
		httpClient: http.DefaultClient,
		logger:     logger,
	}, nil
}

// installPersist 携带 persistInstall 落行所需的解析后字段。appIDKey 是存在
// config->>'app_id' 里的值——真实 Slack app id——必须与 configJSON 里的 app_id
// 一致；它是查询/ON CONFLICT 的键。installerSlackID 是要自动绑定的安装者
// Slack 用户 id，传 "" 表示跳过（BYO 粘贴不携带 authed_user，安装者走的是首次
// 消息触发的常规绑定令牌流程）。
type installPersist struct {
	wsID        pgtype.UUID
	agentID     pgtype.UUID
	installerID pgtype.UUID
	// appIDKey 是存在 config->>'app_id' 的 Slack app id，必须与 configJSON 里的
	// app_id 一致。它用于「已死 owner 的回收」和「定位仍在线的 owner」两类查询，
	// 后者支撑起准确的冲突文案。
	appIDKey string
	// configJSON 携带供入站路由用的 Slack app id（config->>'app_id'）；而行本身
	// 以 (workspace, agent) 为主键——每个 agent 一个 bot。
	configJSON []byte
}

// pgUniqueViolation 是 Postgres 唯一约束冲突的 SQLSTATE。
const pgUniqueViolation = "23505"

// persistInstall 以 (workspace_id, agent_id, channel_type) 为主键 upsert 安装行：
// 每个 agent 恰好一个 Slack bot。重连 agent——包括断开后换绑一个【新的】 Slack
// 应用——是原地更新那行，不会撞 (workspace, agent, channel) 唯一键。
//
// (channel_type, app_id) 路由索引是另一个唯一约束，但不是本次 upsert 的冲突
// 目标，所以这里一旦出现唯一冲突，说明贴来的 Slack 应用已连到【别的】agent 或
// 别的 Multica 工作区——直接拒绝（ErrTeamOwnedByAnotherWorkspace）而不是夺走
// 它。无需退役聊天会话：一行的 agent_id 永不变（它是键的一部分），既有会话对
// 同一 agent 仍然有效。
func (s *InstallService) persistInstall(ctx context.Context, p installPersist) (db.ChannelInstallation, error) {
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("begin install tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	// 在 upsert 之前，先把 (slack, app_id) 路由槽从任何【已死】的旧 owner 手里
	// 腾出来——包括已被吊销的占位行，或所属工作区/agent 已删除的孤儿行
	// （#4810）——这样 bot 旧主已消失时能被重新绑定。仍在线的 owner（active 或
	// archived 的 agent）保持原样，让它撞下面的唯一索引，我们再把它翻成一句准确
	// 的冲突提示。
	if _, err := qtx.ReclaimDeadChannelInstallationByAppID(ctx, db.ReclaimDeadChannelInstallationByAppIDParams{
		ChannelType: string(TypeSlack),
		AppID:       p.appIDKey,
		WorkspaceID: p.wsID,
		AgentID:     p.agentID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// pgx.ErrNoRows 只表示没有「已死」的行可回收——是空操作，不是失败。
		return db.ChannelInstallation{}, fmt.Errorf("reclaim dead slack installation: %w", err)
	}

	inst, err := qtx.UpsertChannelInstallation(ctx, db.UpsertChannelInstallationParams{
		WorkspaceID:     p.wsID,
		AgentID:         p.agentID,
		ChannelType:     string(TypeSlack),
		Config:          p.configJSON,
		InstallerUserID: p.installerID,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return db.ChannelInstallation{}, s.liveOwnerConflictErr(ctx, p.wsID, p.appIDKey)
		}
		return db.ChannelInstallation{}, fmt.Errorf("upsert slack installation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("commit slack install: %w", err)
	}
	return inst, nil
}

// liveOwnerConflictErr 在「回收已死 owner」跑完后，查清现在到底是谁占着
// (slack, app_id) 路由槽，让 persistInstall 返回准确的哨兵错误，而不是以前那个
// 一律赖「别的 workspace」的笼统提示（#4810）。注意要用基池（s.q）读：失败
// 的 upsert 已经作废了当前事务。若槽现在空出来了（并发断开）或查询出错，就回退
// 到通用的跨工作区哨兵——此时重试即可成功。
func (s *InstallService) liveOwnerConflictErr(ctx context.Context, requestingWorkspaceID pgtype.UUID, appID string) error {
	owner, err := s.q.GetChannelInstallationOwnerByAppID(ctx, db.GetChannelInstallationOwnerByAppIDParams{
		ChannelType: string(TypeSlack),
		AppID:       appID,
	})
	if err != nil {
		return ErrTeamOwnedByAnotherWorkspace
	}
	switch {
	case owner.WorkspaceID != requestingWorkspaceID:
		return ErrTeamOwnedByAnotherWorkspace
	case owner.AgentArchivedAt.Valid:
		return ErrTeamOwnedByArchivedAgent
	default:
		return ErrTeamOwnedBySameWorkspace
	}
}

// ListByWorkspace 返回工作区里全部 Slack 安装（含 active 与 revoked），供管理
// 界面展示。
func (s *InstallService) ListByWorkspace(ctx context.Context, wsID pgtype.UUID) ([]db.ChannelInstallation, error) {
	return s.q.ListChannelInstallationsByWorkspace(ctx, db.ListChannelInstallationsByWorkspaceParams{
		WorkspaceID: wsID,
		ChannelType: string(TypeSlack),
	})
}

// GetInWorkspace 是限定工作区的查询：拿别的工作区伪造的安装 id 来查，返回
// NotFound 而不是泄露「这个 id 存在」。
func (s *InstallService) GetInWorkspace(ctx context.Context, id, wsID pgtype.UUID) (db.ChannelInstallation, error) {
	inst, err := s.q.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID:          id,
		WorkspaceID: wsID,
		ChannelType: string(TypeSlack),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ChannelInstallation{}, ErrInstallationNotFound
		}
		return db.ChannelInstallation{}, err
	}
	return inst, nil
}

// Revoke 把安装状态翻成 'revoked'。行保留用于审计；重新安装会把它翻回
// 'active'。Supervisor 随即停止监管该安装（ListActiveInstallations 只筛 active），
// 它的 Socket Mode 连接随之收尾，出站投递也不再走它。
func (s *InstallService) Revoke(ctx context.Context, id pgtype.UUID) error {
	return s.q.SetChannelInstallationStatus(ctx, db.SetChannelInstallationStatusParams{
		ID:     id,
		Status: "revoked",
	})
}
