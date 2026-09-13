package telegram

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 角色定位：Telegram 安装（install）的后端逻辑，对齐 slack/install.go + byo_install.go。
// 场景：工作区管理员在 @BotFather 创建机器人后把 token 粘进 Multica，本文件负责校验 token、
// 加密存储、落库与管理查询。InstallService 独占 token 的静态加密——没有任何调用方能写入
// 一条带明文 token 的 channel_installation 记录——并封装共享的持久化事务与
// list / get / revoke 管理接口。
//
// 为什么校验必须"在线"：光看 token 形状只能排除明显拼错；只有真的调用一次 Telegram 的
// getMe 才能确认 token 有效、拿到 bot 用户名（@提及识别要用），同时 getWebhookInfo 检查
// bot 是否已被 webhook 托管（长轮询与 webhook 互斥）。

var (
	// ErrInstallationNotFound 表示"该工作区内没有匹配行"。
	ErrInstallationNotFound = errors.New("telegram installation not found")
	// ErrInvalidBotToken 表示粘进来的 token 格式不对（应为 "<数字id>:<密钥>"），
	// 由 handler 映射成 400。
	ErrInvalidBotToken = errors.New("telegram: bot token must look like 123456:ABC-DEF…")
	// ErrCredentialsRejected 表示 Telegram 亲自拒绝了 token。刻意与"网络不通"区分开——
	// 不能因为部署环境代理/网络挂了，就让用户去轮换一条本来有效的凭据。
	ErrCredentialsRejected = errors.New("telegram: Telegram rejected this bot token")
	// ErrCredentialsUnverifiable 表示 Multica 无法完成这次在线 Telegram 校验
	// （网络/超时等，拿不到 Telegram 的明确裁决）。此时 token 尚未落库或修改。
	ErrCredentialsUnverifiable = errors.New("telegram: could not reach Telegram to verify this bot")
	// ErrBotOwnedByAnotherWorkspace：粘的 bot 已经接在**另一个** Multica 工作区的活跃拥有者上。
	ErrBotOwnedByAnotherWorkspace = errors.New("telegram: this bot is already connected to a different Multica workspace")
	// ErrBotOwnedBySameWorkspace：bot 已经接在**同一个**工作区的另一个活跃 agent 上。
	ErrBotOwnedBySameWorkspace = errors.New("telegram: this bot is already connected to another agent in this workspace")
	// ErrBotOwnedByArchivedAgent：拥有这个 bot 的 agent 已被归档。
	ErrBotOwnedByArchivedAgent = errors.New("telegram: this bot is connected to an archived agent in this workspace")
	// ErrWebhookConfigured：这个 bot 正被一个外发 webhook 托管。长轮询和 webhook 互斥，
	// 安装时绝不能悄悄删掉另一个集成的 webhook。
	ErrWebhookConfigured = errors.New("telegram: bot has an outgoing webhook configured")
)

// installQueries 是 InstallService 需要的若干生成查询的切片，抽成接口是为了测试注入假实现
// （与 Slack 适配器同款模式）。
type installQueries interface {
	WithTx(tx pgx.Tx) installQueries
	UpsertChannelInstallation(ctx context.Context, arg db.UpsertChannelInstallationParams) (db.ChannelInstallation, error)
	ReclaimDeadChannelInstallationByAppID(ctx context.Context, arg db.ReclaimDeadChannelInstallationByAppIDParams) (pgtype.UUID, error)
	GetChannelInstallationOwnerByAppID(ctx context.Context, arg db.GetChannelInstallationOwnerByAppIDParams) (db.GetChannelInstallationOwnerByAppIDRow, error)
	ListChannelInstallationsByWorkspace(ctx context.Context, arg db.ListChannelInstallationsByWorkspaceParams) ([]db.ChannelInstallation, error)
	GetChannelInstallationInWorkspace(ctx context.Context, arg db.GetChannelInstallationInWorkspaceParams) (db.ChannelInstallation, error)
	SetChannelInstallationStatus(ctx context.Context, arg db.SetChannelInstallationStatusParams) error
}

// dbInstallQueries 是 installQueries 接口的真实实现：内嵌 *db.Queries，转发所有方法。
type dbInstallQueries struct{ *db.Queries }

func (q dbInstallQueries) WithTx(tx pgx.Tx) installQueries {
	return dbInstallQueries{q.Queries.WithTx(tx)}
}

// InstallService 负责 bot token 的静态加密与安装事务。box 必须非 nil——即使开发环境
// 也拒绝明文存储 token。
type InstallService struct {
	box        *secretbox.Box
	q          installQueries
	tx         engine.TxStarter
	httpClient *http.Client
	logger     *slog.Logger

	// apiBase 为 getMe 校验调用覆盖 Bot API 域名（测试指向 httptest server）。
	// 空值用真实 API。
	apiBase string
}

// NewInstallService 把服务绑定到查询、事务启动器和加密盒上。
func NewInstallService(q *db.Queries, tx engine.TxStarter, box *secretbox.Box, logger *slog.Logger) (*InstallService, error) {
	if q == nil {
		return nil, errors.New("telegram: InstallService requires queries")
	}
	return newInstallService(dbInstallQueries{q}, tx, box, logger)
}

// newInstallService 做依赖非 nil 校验后组装服务。入口参数全查一遍而不是靠运行时 panic，
// 是为了把"装配错误"提前到进程启动阶段暴露。
func newInstallService(q installQueries, tx engine.TxStarter, box *secretbox.Box, logger *slog.Logger) (*InstallService, error) {
	if box == nil {
		return nil, errors.New("telegram: InstallService requires a non-nil secretbox.Box")
	}
	if q == nil {
		return nil, errors.New("telegram: InstallService requires queries")
	}
	if tx == nil {
		return nil, errors.New("telegram: InstallService requires a tx starter")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &InstallService{
		box:        box,
		q:          q,
		tx:         tx,
		httpClient: newCredentialVerificationClient(),
		logger:     logger,
	}, nil
}

// RegisterParams 是一次 bot 安装的入参：这个 bot 代表哪个 agent、谁在安装、粘进来的
// BotFather token。
type RegisterParams struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	InitiatorID pgtype.UUID
	BotToken    string
}

// Register 为一个 agent 安装用户自带的 Telegram bot：先用 getMe 在线校验 token
// （顺带拿到 bot 用户名用于 @提及识别），静态加密 token，再以 (workspace, agent) 为键、
// bot id 占路由槽位的方式持久化安装。
func (s *InstallService) Register(ctx context.Context, p RegisterParams) (db.ChannelInstallation, error) {
	token := strings.TrimSpace(p.BotToken)
	botID, err := parseBotID(token)
	if err != nil {
		return db.ChannelInstallation{}, err
	}
	api := newBotAPI(s.apiBase, token, s.httpClient)
	me, err := api.GetMe(ctx)
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("telegram getMe: %w", classifyCredentialVerificationError(err))
	}
	// getMe 成功只说明"这个 token 能登录"，还要确认对端确实是个带用户名的 bot。
	if !me.IsBot || me.Username == "" {
		return db.ChannelInstallation{}, fmt.Errorf("%w: response is not a bot with a username", ErrCredentialsRejected)
	}
	webhook, err := api.GetWebhookInfo(ctx)
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("telegram getWebhookInfo: %w", classifyCredentialVerificationError(err))
	}
	// bot 已被 webhook 托管时 getUpdates 不工作；要装长轮询必须先处理 webhook。
	if webhook.URL != "" {
		return db.ChannelInstallation{}, ErrWebhookConfigured
	}

	sealed, err := s.box.Seal([]byte(token))
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("encrypt telegram bot token: %w", err)
	}
	cfgJSON, err := json.Marshal(installConfig{
		AppID:             botID,
		BotUsername:       me.Username,
		BotTokenEncrypted: base64.StdEncoding.EncodeToString(sealed),
	})
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("encode telegram installation config: %w", err)
	}
	return s.persistInstall(ctx, installPersist{
		wsID:        p.WorkspaceID,
		agentID:     p.AgentID,
		installerID: p.InitiatorID,
		appIDKey:    botID,
		configJSON:  cfgJSON,
	})
}

// classifyCredentialVerificationError 区分两种情况：Telegram 明确拒绝凭据（401），
// 还是根本没拿到裁决（网络不通/超时）。getMe 与 getWebhookInfo 共用同一个分类器，
// 这样任一步骤遇代理故障时，给用户的下一步行动提示都一样。
func classifyCredentialVerificationError(err error) error {
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusUnauthorized {
		return fmt.Errorf("%w: %v", ErrCredentialsRejected, err)
	}
	return fmt.Errorf("%w: %v", ErrCredentialsUnverifiable, err)
}

// installPersist 携带 persistInstall 要落库的已解析字段。
type installPersist struct {
	wsID        pgtype.UUID
	agentID     pgtype.UUID
	installerID pgtype.UUID
	appIDKey    string
	configJSON  []byte
}

// pgUniqueViolation 是 PostgreSQL 唯一约束冲突的 SQLSTATE 错误码。
const pgUniqueViolation = "23505"

const credentialVerificationTimeout = 15 * time.Second

func newCredentialVerificationClient() *http.Client {
	return &http.Client{Timeout: credentialVerificationTimeout}
}

// persistInstall 以 (workspace_id, agent_id, channel_type) 为键 upsert 安装：
// 一个 agent 只允许一个 Telegram bot。若 (channel_type, app_id) 路由索引唯一冲突，
// 说明粘的 bot 已被**另一个活跃的** agent 或工作区占用——拒绝而不是抢占，并给出精确的
// 冲突说明（与 Slack #4810 同策略）。
func (s *InstallService) persistInstall(ctx context.Context, p installPersist) (db.ChannelInstallation, error) {
	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("begin install tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	// upsert 之前，先从路由槽位上清掉任何"已死的"旧占有者（比如所属 agent 已删除、
	// 状态不是 active 的安装），否则槽位被死记录占着，重装同一 bot 会被误判成冲突。
	if _, err := qtx.ReclaimDeadChannelInstallationByAppID(ctx, db.ReclaimDeadChannelInstallationByAppIDParams{
		ChannelType: string(TypeTelegram),
		AppID:       p.appIDKey,
		WorkspaceID: p.wsID,
		AgentID:     p.agentID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return db.ChannelInstallation{}, fmt.Errorf("reclaim dead telegram installation: %w", err)
	}

	inst, err := qtx.UpsertChannelInstallation(ctx, db.UpsertChannelInstallationParams{
		WorkspaceID:     p.wsID,
		AgentID:         p.agentID,
		ChannelType:     string(TypeTelegram),
		Config:          p.configJSON,
		InstallerUserID: p.installerID,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		// 路由槽位撞了"活着"的占有者：查一下是谁，好让 handler 给出准确文案。
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return db.ChannelInstallation{}, s.liveOwnerConflictErr(ctx, p.wsID, p.appIDKey)
		}
		return db.ChannelInstallation{}, fmt.Errorf("upsert telegram installation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.ChannelInstallation{}, fmt.Errorf("commit telegram install: %w", err)
	}
	return inst, nil
}

// liveOwnerConflictErr 查清路由槽位当前被谁占着，按占用者分类返回冲突错误，供 handler
// 渲染准确提示（另一工作区 / 同工作区另一 agent / 被归档的 agent）。
func (s *InstallService) liveOwnerConflictErr(ctx context.Context, requestingWorkspaceID pgtype.UUID, appID string) error {
	owner, err := s.q.GetChannelInstallationOwnerByAppID(ctx, db.GetChannelInstallationOwnerByAppIDParams{
		ChannelType: string(TypeTelegram),
		AppID:       appID,
	})
	if err != nil {
		// 查不到占用者属于异常状态（冲突确实发生了），按最保守的"他区占用"报错。
		return ErrBotOwnedByAnotherWorkspace
	}
	switch {
	case owner.WorkspaceID != requestingWorkspaceID:
		return ErrBotOwnedByAnotherWorkspace
	case owner.AgentArchivedAt.Valid:
		return ErrBotOwnedByArchivedAgent
	default:
		return ErrBotOwnedBySameWorkspace
	}
}

// ListByWorkspace 返回工作区内全部 Telegram 安装，供管理界面用。
func (s *InstallService) ListByWorkspace(ctx context.Context, wsID pgtype.UUID) ([]db.ChannelInstallation, error) {
	return s.q.ListChannelInstallationsByWorkspace(ctx, db.ListChannelInstallationsByWorkspaceParams{
		WorkspaceID: wsID,
		ChannelType: string(TypeTelegram),
	})
}

// GetInWorkspace 是限定工作区的查询：别的 workspace 伪造一个安装 id 来查，只会得到
// NotFound，不会泄露"这条记录是否存在"。
func (s *InstallService) GetInWorkspace(ctx context.Context, id, wsID pgtype.UUID) (db.ChannelInstallation, error) {
	inst, err := s.q.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID:          id,
		WorkspaceID: wsID,
		ChannelType: string(TypeTelegram),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ChannelInstallation{}, ErrInstallationNotFound
		}
		return db.ChannelInstallation{}, err
	}
	return inst, nil
}

// Revoke 把安装状态翻转为 'revoked'。行保留用于审计，已有的聊天会话留在 Multica 不删；
// Supervisor 会停止监督这条安装，于是它的轮询循环自然收尾、出站消息也随之停发。
func (s *InstallService) Revoke(ctx context.Context, id pgtype.UUID) error {
	return s.q.SetChannelInstallationStatus(ctx, db.SetChannelInstallationStatusParams{
		ID:     id,
		Status: "revoked",
	})
}
