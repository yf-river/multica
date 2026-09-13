// registration_service.go：设备流安装生命周期服务——开设备流会话、后台轮询，
// 成功后在同一事务里落安装行 + 安装者绑定。
package lark

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// pgUniqueViolation 是 Postgres 唯一约束违例的 SQLSTATE。在死拥主回收跑完
// 之后仍撞上 (channel_type, config->>'app_id') 索引的重绑 upsert，意味着一个
// LIVE 拥主仍持有该槽位。
const pgUniqueViolation = "23505"

// RegistrationSessionStatus 是 `begin` 会话所处的判别状态。HTTP 状态端点把
// 底层字符串原样序列化，前端无需解析措辞即可做模式匹配。
type RegistrationSessionStatus string

const (
	// RegistrationStatusPending 表示二维码已铸造、后台 goroutine 仍在轮询 Lark。
	// 前端按我们在 poll_interval_seconds 里返回的节奏继续轮询我们的状态端点。
	RegistrationStatusPending RegistrationSessionStatus = "pending"

	// RegistrationStatusSuccess 表示设备流返回了凭据，且 lark_installation +
	// 安装者绑定这一对已提交。`installation_id` 已填充。前端关闭对话框并让
	// 安装缓存失效。
	RegistrationStatusSuccess RegistrationSessionStatus = "success"

	// RegistrationStatusError 表示会话到达终态失败（过期、用户拒绝、Lark 协议
	// 错误、后续 bot-info / DB 错误）。`error_reason` 设成一个稳定码，前端不用
	// 解析 `error_message` 就能渲染正确文案。
	RegistrationStatusError RegistrationSessionStatus = "error"
)

// Reason 码是服务在失败会话上存储的稳定字符串，前端可直接 switch 而不用解析
// 措辞。
const (
	RegistrationReasonExpired              = "expired"
	RegistrationReasonAccessDenied         = "access_denied"
	RegistrationReasonProtocol             = "lark_protocol_error"
	RegistrationReasonBotInfoFailed        = "bot_info_failed"
	RegistrationReasonInstallationConflict = "installation_conflict"
	RegistrationReasonInstallerBindFailed  = "installer_bind_failed"
	RegistrationReasonInternalError        = "internal_error"
)

// RegistrationServiceConfig 配置服务。
type RegistrationServiceConfig struct {
	// SessionTTL 限制成功/错误会话在被 GC 前留在进程内缓存里的时长。默认 30
	// 分钟——够前端在对话框关闭后拉取最终状态，又短到被放弃的会话不会永久占着
	// 内存。与设备流有效期（Lark 的 expire_in，约 10 分钟）相互独立。
	SessionTTL time.Duration

	// Now 可覆盖，用于确定性过期测试。
	Now func() time.Time

	// Logger 用于协议级警告（Lark 错误码、成功后 bot info 失败）。nil 用
	// slog.Default()。
	Logger *slog.Logger
}

// withDefaults 填充配置默认值。
func (c RegistrationServiceConfig) withDefaults() RegistrationServiceConfig {
	if c.SessionTTL == 0 {
		c.SessionTTL = 30 * time.Minute
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// RegistrationService 掌管设备流安装生命周期。它是唯一做以下事情的地方：
//
//  1. 对 Lark 开新的设备流会话（Begin），
//  2. 进程内跟踪该会话的轮询状态，
//  3. 跑后台轮询 goroutine，
//  4. 成功后用新铸的凭据调 APIClient.GetBotInfo，再在单个事务里写
//     lark_installation + 安装者的 lark_user_binding。
//
// 进程内会话存储是有意为之：设备流会话寿命短（<10 分钟），二维码离开发起它的
// 同一浏览器会话就没有价值，把半成品安装持久化进 Postgres 只会加一次迁移 + GC
// 清扫，却不带来任何用户能在服务器重启后复用的产品能力。
type RegistrationService struct {
	cfg         RegistrationServiceConfig
	client      *RegistrationClient
	api         APIClient
	queries     *ChannelStore
	tx          TxStarter
	installs    *InstallationService
	binder      InstallerBinder
	authQueries authQueriesAdapter

	// bus 可选。接上（SetEventBus）后，成功的安装会在行提交那一刻发布
	// lark_installation:created，所有 workspace 客户端无需等某个浏览器把状态端点
	// 轮询到 success 就刷新连接徽标。nil 合法——安装照常工作，只是不推那帧 WS。
	bus *events.Bus

	mu       sync.Mutex
	sessions map[string]*registrationSession
}

// authQueriesAdapter 是服务在开会话前需要的最小查询面：agent ↔ workspace 归属
// 校验。保持接口形态，测试就能注入 stub 而不是真实 *db.Queries + Postgres
// fixture。
type authQueriesAdapter interface {
	GetAgentInWorkspace(ctx context.Context, params db.GetAgentInWorkspaceParams) (db.Agent, error)
}

// NewRegistrationService 装配设备流 client、APIClient（成功后 GetBotInfo 用）与
// DB 写入路径。任一必需依赖缺失都会以构造错误浮出，避免启动时静默半初始化、
// 运行时安装按钮一律 500。
func NewRegistrationService(
	cfg RegistrationServiceConfig,
	client *RegistrationClient,
	api APIClient,
	queries *db.Queries,
	tx TxStarter,
	installs *InstallationService,
	binder InstallerBinder,
) (*RegistrationService, error) {
	if client == nil {
		return nil, errors.New("lark registration: RegistrationClient is required")
	}
	if api == nil {
		return nil, errors.New("lark registration: APIClient is required")
	}
	if queries == nil {
		return nil, errors.New("lark registration: queries is required")
	}
	if tx == nil {
		return nil, errors.New("lark registration: TxStarter is required")
	}
	if installs == nil {
		return nil, errors.New("lark registration: InstallationService is required")
	}
	if binder == nil {
		return nil, errors.New("lark registration: InstallerBinder is required")
	}
	return &RegistrationService{
		cfg:         cfg.withDefaults(),
		client:      client,
		api:         api,
		queries:     NewChannelStore(queries),
		tx:          tx,
		installs:    installs,
		binder:      binder,
		authQueries: queries,
		sessions:    make(map[string]*registrationSession),
	}, nil
}

// SetEventBus 在构造【之后】接入可选事件总线，这样六个位置参数的构造校验分支
// 保持不动、总线保持 nil 安全。接上后，finishSuccess 会在行提交点——权威的真
// 相时刻——发布 lark_installation:created，而不是依赖 HTTP 状态轮询 handler
// 只在一个浏览器恰好轮询到 success 时才发。
func (s *RegistrationService) SetEventBus(bus *events.Bus) {
	s.bus = bus
}

// publishInstalled 在可选总线上发布 lark_installation:created。镜像撤销路径
// （RevokeLarkInstallation 在其 handler 里发布 lark_installation:revoked）：两个
// 事件都经 SubscribeAll 扇出广播到整个 workspace，前端对 lark_installation 前缀
// 让 larkKeys.installations 失效，于是每个挂载表面（agent 集成 tab、inspector、
// Settings）无需刷新页面就更新连接徽标。覆盖全新安装与 revoked→active 重装——
// 两者都走同一条 UpsertLarkInstallation 写入。nil 安全。
func (s *RegistrationService) publishInstalled(workspaceID, installationID pgtype.UUID) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(events.Event{
		Type:        protocol.EventLarkInstallationCreated,
		WorkspaceID: uuidString(workspaceID),
		ActorType:   "system",
		Payload:     map[string]any{"installation_id": uuidString(installationID)},
	})
}

// registrationSession 是一次进行中安装的进程内状态。
type registrationSession struct {
	id          string
	workspaceID pgtype.UUID
	agentID     pgtype.UUID
	initiatorID pgtype.UUID

	deviceCode string
	domain     string
	qrCodeURL  string
	interval   time.Duration
	expiresAt  time.Time
	// region 是安装启动时所在的那朵云。轮询循环把它当作本地 `region` 变量的
	// 初值读入；若 poll 流中途浮出 tenant_brand，本地变量翻成 RegionLark，但会话
	// 字段保持用户当初的选择（它只是信息性的——权威的云经循环本地变量随
	// finishSuccess 流回来）。
	region Region

	mu             sync.Mutex
	status         RegistrationSessionStatus
	installationID pgtype.UUID
	errorReason    string
	errorMessage   string
	gcAfter        time.Time
}

// snapshot 返回会话当前状态的只读快照。
func (s *registrationSession) snapshot() RegistrationSessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return RegistrationSessionState{
		ID:             s.id,
		Status:         s.status,
		InstallationID: s.installationID,
		ErrorReason:    s.errorReason,
		ErrorMessage:   s.errorMessage,
		// InitiatorID 在 BeginInstall 之后不可变，所以不持锁读也安全——仍把它
		// 放进来，让状态 handler 可以把读限定到会话发起者。
		InitiatorID: s.initiatorID,
	}
}

// markSuccess 把会话标记为成功并记录安装 id 与 GC 时间。
func (s *registrationSession) markSuccess(installationID pgtype.UUID, gcAfter time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = RegistrationStatusSuccess
	s.installationID = installationID
	s.gcAfter = gcAfter
}

// markError 把会话标记为错误（幂等）。
func (s *registrationSession) markError(reason, msg string, gcAfter time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 幂等：若并行 goroutine 已终止该会话（例如两次状态读之间触发过期），别覆盖
	// 第一个原因——用户已经看到它了。
	if s.status != RegistrationStatusPending {
		return
	}
	s.status = RegistrationStatusError
	s.errorReason = reason
	s.errorMessage = msg
	s.gcAfter = gcAfter
}

// RegistrationSessionState 是 handler 序列化给前端的只读快照。内部互斥锁在构造
// 时被隐藏。InitiatorID 不序列化给客户端——handler 只用它给状态读授权（会话
// 发起者或 workspace 管理员）。
type RegistrationSessionState struct {
	ID             string
	Status         RegistrationSessionStatus
	InstallationID pgtype.UUID
	ErrorReason    string
	ErrorMessage   string
	InitiatorID    pgtype.UUID
}

// BeginInstallParams 是来自 handler 的可信输入——workspace、agent 与发起用户已
// 经被 handler 鉴权并授权（canManageAgent：agent 的 owner 或 workspace
// owner/admin；agent 属于该 workspace）。服务在下面按纵深防御再查一次
// agent↔workspace 归属。
type BeginInstallParams struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	InitiatorID pgtype.UUID
	// Region 选设备流对着哪朵云的 accounts 主机 begin——飞书（大陆，
	// accounts.feishu.cn）或 Lark（国际版，accounts.larksuite.com）。用户在 UI
	// 里显式选择（“绑定到飞书” vs “绑定到 Lark”），于是前置渲染的二维码就指向
	// 正确的云，Lark 用户不必先打飞书 URL 再靠 tenant-brand 自动切换。空/未知
	// 值退回飞书（与 RegionOrDefault 一致），没有新字段的旧调用方保持可用。
	Region Region
}

// BeginInstallResult 是 handler 回显给前端的公共负载。session_id 是前端轮询
// 状态用的不透明句柄；我们刻意不回显 device_code 或轮询间隔（那是内部调度
// 状态）。
type BeginInstallResult struct {
	SessionID           string
	QRCodeURL           string
	ExpiresInSeconds    int
	PollIntervalSeconds int
}

// BeginInstall 开启全新设备流会话并启动后台轮询 goroutine。返回负载喂给前端
// 的二维码对话框；轮询 goroutine 一直跑到成功、终态失败或 device_code 过期。
//
// session_id 是返回给浏览器的唯一不透明令牌——device_code 只在服务器侧
// （device_code 泄漏出去，Lark 会接受来自任何地方的 poll，所以绝不停回显）。
func (s *RegistrationService) BeginInstall(ctx context.Context, p BeginInstallParams) (BeginInstallResult, error) {
	if !p.WorkspaceID.Valid || !p.AgentID.Valid || !p.InitiatorID.Valid {
		return BeginInstallResult{}, errors.New("lark registration: workspace, agent, and initiator are required")
	}
	// agent↔workspace 预检——没有它，调用方靠猜 UUID 就能对着别人 workspace 的
	// agent 开安装会话，而对 Lark 铸造的 device_code 仍会产出凭据。handler 为跑
	// canManageAgent 已经加载过这个 agent；在这里复查让服务自我保护。
	//
	// 我们留着 agent：它的名字预填 Lark PersonalAgent 创建表单上的 bot 名（见
	// botNamePreset），装出的 bot 读起来是 "<agent> - Multica" 而不是
	// "{用户姓名}的智能助手"。
	agent, err := s.authQueries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          p.AgentID,
		WorkspaceID: p.WorkspaceID,
	})
	if err != nil {
		return BeginInstallResult{}, fmt.Errorf("lark registration: agent not in workspace: %w", err)
	}

	// 归一化请求地域：空/未知 → 飞书，与存储层相同的向后兼容不变量
	// （RegionOrDefault）。既保护设备流 client 不受 handler 传来的错误值伤害，
	// 也意味着地域前的调用方（省略该字段）继续拿到历史的大陆优先行为。
	region := RegionOrDefault(string(p.Region))

	begin, err := s.client.Begin(ctx, botNamePreset(agent.Name), region)
	if err != nil {
		return BeginInstallResult{}, fmt.Errorf("lark registration: begin: %w", err)
	}

	now := s.cfg.Now()
	sessionID, err := randomSessionID()
	if err != nil {
		return BeginInstallResult{}, fmt.Errorf("lark registration: mint session id: %w", err)
	}
	sess := &registrationSession{
		id:          sessionID,
		workspaceID: p.WorkspaceID,
		agentID:     p.AgentID,
		initiatorID: p.InitiatorID,
		deviceCode:  begin.DeviceCode,
		domain:      begin.Domain,
		qrCodeURL:   begin.QRCodeURL,
		interval:    begin.Interval,
		expiresAt:   now.Add(begin.ExpiresIn),
		region:      region,
		status:      RegistrationStatusPending,
	}
	s.mu.Lock()
	s.sessions[sessionID] = sess
	s.mu.Unlock()

	// 轮询 goroutine 活得比请求 ctx 久，所以这里不能复用 ctx。我们按
	// device_code 有效期给它配自己的 ctx——最坏情况是会话在 Lark 窗口关闭后
	// 静默超时，下一次状态读时以 RegistrationReasonExpired 浮给用户。
	go s.runPolling(sess)

	return BeginInstallResult{
		SessionID:           sessionID,
		QRCodeURL:           begin.QRCodeURL,
		ExpiresInSeconds:    int(begin.ExpiresIn / time.Second),
		PollIntervalSeconds: int(begin.Interval / time.Second),
	}, nil
}

// GetSession 返回进行中或刚结束会话的当前状态。workspace UUID 是必须的，
// 保证一个 workspace 发起的会话不能被另一个 workspace 轮询（session id 本就
// 不可猜，但这里纵深防御不花钱）。
//
// 未知/过期/已 GC 的会话返回 ErrRegistrationSessionNotFound；前端把它当
// error_reason="session_lost" 处理——提示用户重开安装。
func (s *RegistrationService) GetSession(workspaceID pgtype.UUID, sessionID string) (RegistrationSessionState, error) {
	if strings.TrimSpace(sessionID) == "" {
		return RegistrationSessionState{}, ErrRegistrationSessionNotFound
	}
	s.gcExpiredLocked()
	s.mu.Lock()
	sess, ok := s.sessions[sessionID]
	s.mu.Unlock()
	if !ok {
		return RegistrationSessionState{}, ErrRegistrationSessionNotFound
	}
	if !uuidEqual(sess.workspaceID, workspaceID) {
		// 当作未找到——泄漏“存在但 workspace 不对”会让攻击者跨 workspace 枚举
		// session id。
		return RegistrationSessionState{}, ErrRegistrationSessionNotFound
	}
	return sess.snapshot(), nil
}

// runPolling 是后台循环。模式与上游 SDK 一致：等待 → 轮询 → 按结果分支。
// 任何终态结果要么记录安装+绑定（成功），要么把会话标记为错误。
func (s *RegistrationService) runPolling(sess *registrationSession) {
	// 用 Lark 的有效期限定整个轮询寿命——窗口一关，任何 poll 都不可能成功。
	ctx, cancel := context.WithDeadline(context.Background(), sess.expiresAt)
	defer cancel()

	interval := sess.interval
	if interval <= 0 {
		interval = time.Duration(registrationDefaultPollSeconds) * time.Second
	}
	domain := sess.domain
	deviceCode := sess.deviceCode
	// region 跟踪本安装属于哪朵云。它从用户在 begin 时刻的选择开始（默认飞书；
	// 前端现在暴露直接以 accounts.larksuite.com begin 的显式 Lark CTA）。下面
	// 的 SwitchedDomain 分支仍作为安全网被尊重——用户点了飞书 CTA 却实际用
	// Lark 国际版账号授权时，poll 流浮出 tenant_brand="lark"，我们把本地变量
	// 相应翻转。于是在 finishSuccess 时刻 `region` 是该安装权威的云，先来自用户
	// 的 UI 选择、再来自协议的角色切换——绝不靠字符串匹配 accounts 主机名
	// （staging/mock 域名也能正确分类）。
	region := sess.region
	if region == "" {
		region = RegionFeishu
	}

	for {
		select {
		case <-ctx.Done():
			s.cfg.Logger.Info("lark registration: session expired",
				"session_id", sess.id,
				"workspace_id", uuidString(sess.workspaceID))
			sess.markError(RegistrationReasonExpired, "QR expired before authorization", s.gcDeadline())
			return
		case <-time.After(interval):
		}

		res, err := s.client.Poll(ctx, domain, deviceCode)
		if err != nil {
			var re *RegistrationError
			if errors.As(err, &re) {
				s.cfg.Logger.Warn("lark registration: protocol error",
					"session_id", sess.id, "code", re.Code, "desc", re.Description)
				sess.markError(RegistrationReasonProtocol, re.Error(), s.gcDeadline())
				return
			}
			// 瞬时 transport 错误（DNS、网络）——记日志，等下一个滴答重试而不是
			// 杀掉会话，让 30 秒的跨地域抖动能自愈。
			s.cfg.Logger.Warn("lark registration: transport error, will retry",
				"session_id", sess.id, "err", err)
			continue
		}

		switch {
		case res.SwitchedDomain != "":
			// tenant-brand 切换——不遵守间隔立即重新指向，与上游 SDK 行为一致。
			// Lark 在转换轮次上恰好发一次品牌提示，携带凭据的响应落在对新域名的
			// 下一次调用上。
			//
			// 两个方向都处理（feishu→lark 与 lark→feishu），分流 CTA 的“进错门”
			// 路径无论用户点了哪个 CTA 都能恢复。新地域搭在同一 PollResult 上，
			// 我们永远不需要在这里从主机字符串反推——staging / mock accounts
			// 主机于是也能正确分类，而无须主机名前缀匹配。
			domain = res.SwitchedDomain
			region = res.SwitchedRegion
			s.cfg.Logger.Info("lark registration: switched cloud after tenant-brand mismatch",
				"session_id", sess.id, "domain", domain, "region", string(region))
			continue
		case res.ClientID != "" && res.ClientSecret != "":
			s.finishSuccess(ctx, sess, res, region)
			return
		case res.Err != nil:
			reason := RegistrationReasonProtocol
			if res.Err.Code == "access_denied" {
				reason = RegistrationReasonAccessDenied
			} else if res.Err.Code == "expired_token" {
				reason = RegistrationReasonExpired
			}
			s.cfg.Logger.Info("lark registration: terminal error",
				"session_id", sess.id, "code", res.Err.Code, "desc", res.Err.Description)
			sess.markError(reason, res.Err.Error(), s.gcDeadline())
			return
		case res.Status == "slow_down":
			// 尊重 Lark 的退避——按 RFC 8628 §3.5 加 5 秒。
			interval += 5 * time.Second
		default:
			// authorization_pending ——保持间隔，继续循环。
		}
	}
}

// finishSuccess 跑轮询后的收尾：bot info 查询 + 安装插入 + 安装者绑定，全部在
// 单个 DB 事务里。
func (s *RegistrationService) finishSuccess(ctx context.Context, sess *registrationSession, res *PollResult, region Region) {
	// 把探测到的地域带上凭据，让下面的 GetBotInfo 打对开放平台主机：Lark 国际版
	// 安装必须打到 open.larksuite.com，而不是飞书默认。
	creds := InstallationCredentials{AppID: res.ClientID, AppSecret: res.ClientSecret, Region: region}
	// 重新注册既有 Bot 会在相同 client_id 下发新 client_secret，Lark 会撤销所有
	// 用旧 secret 铸造的 tenant_access_token。API client 按 app_id 缓存 token——
	// 而 app_id 没变——所以先叫它丢弃该条目，再用新凭据铸造；否则下面第一次调用
	// 以及之后每次出站都会重放一个 Lark 现在拒绝的 token（#7611）。
	if invalidator, ok := s.api.(TokenCacheInvalidator); ok {
		invalidator.InvalidateTokenCache(res.ClientID)
	}
	info, err := s.api.GetBotInfo(ctx, creds)
	if err != nil {
		s.cfg.Logger.Warn("lark registration: bot info failed",
			"session_id", sess.id, "err", err)
		sess.markError(RegistrationReasonBotInfoFailed, err.Error(), s.gcDeadline())
		return
	}
	if info.OpenID == "" {
		sess.markError(RegistrationReasonBotInfoFailed, "bot info missing open_id", s.gcDeadline())
		return
	}

	// 在事务外加密 app_secret，免得加密开销坐进 DB 锁里。InstallationService 的
	// Upsert 本会替我们做，但我们需要加密 blob 出现在事务范围的 queries 句柄里，
	// 让安装者绑定与安装插入一起提交——在这里复刻 Seal。
	sealed, err := s.installs.box.Seal([]byte(res.ClientSecret))
	if err != nil {
		s.cfg.Logger.Error("lark registration: seal app_secret",
			"session_id", sess.id, "err", err)
		sess.markError(RegistrationReasonInternalError, err.Error(), s.gcDeadline())
		return
	}

	tx, err := s.tx.Begin(ctx)
	if err != nil {
		s.cfg.Logger.Error("lark registration: begin tx",
			"session_id", sess.id, "err", err)
		sess.markError(RegistrationReasonInternalError, err.Error(), s.gcDeadline())
		return
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	// 同一飞书 app（app_id）被一个 DEAD 前拥主持有时——本 workspace 里【另一个】
	// agent 留下的 revoked 占位，或 workspace/agent 已删除的孤儿（#4810）——那行
	// 仍占着 (channel_type, config->>'app_id') 唯一槽位，会挡住下面的
	// UpsertChannelInstallation INSERT。先回收它——事务同时包住 delete 与 upsert，
	// 两者之间失败能干净回滚。LIVE 拥主保留在位上（同一 agent 自己的 revoked 行
	// 会被 upsert 就地复活；active/archived agent 继续拥主），于是 upsert 会在
	// 下面暴露冲突而不是抢走 bot。
	if err := qtx.ReclaimDeadInstallationByAppID(ctx, sess.workspaceID, sess.agentID, res.ClientID); err != nil {
		s.cfg.Logger.Warn("lark registration: reclaim dead installation",
			"session_id", sess.id, "err", err)
		sess.markError(RegistrationReasonInternalError, err.Error(), s.gcDeadline())
		return
	}

	inst, err := qtx.UpsertLarkInstallation(ctx, UpsertInstallationParams{
		WorkspaceID:        sess.workspaceID,
		AgentID:            sess.agentID,
		AppID:              res.ClientID,
		AppSecretEncrypted: sealed,
		BotOpenID:          string(info.OpenID),
		BotUnionID:         textOrNull(info.UnionID),
		InstallerUserID:    sess.initiatorID,
		Region:             string(region),
	})
	if err != nil {
		s.cfg.Logger.Warn("lark registration: upsert installation",
			"session_id", sess.id, "err", err)
		// 这里的唯一违例意味着 app_id 槽位被 LIVE 拥主持有（上面的回收已清掉所有
		// 死拥主）。浮出谁持有它——本 workspace 的另一个 agent、归档 agent，还是
		// 别的 workspace——而不是泄漏原始 Postgres 错误。
		msg := err.Error()
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			msg = s.liveOwnerConflictMessage(ctx, sess.workspaceID, res.ClientID)
		}
		sess.markError(RegistrationReasonInstallationConflict, msg, s.gcDeadline())
		return
	}

	if err := s.binder.BindInstallerTx(ctx, qtx, InstallerBindParams{
		WorkspaceID:    sess.workspaceID,
		InstallationID: inst.ID,
		MulticaUserID:  sess.initiatorID,
		LarkOpenID:     res.OpenID,
	}); err != nil {
		s.cfg.Logger.Warn("lark registration: bind installer",
			"session_id", sess.id, "err", err)
		sess.markError(RegistrationReasonInstallerBindFailed, err.Error(), s.gcDeadline())
		return
	}

	if err := tx.Commit(ctx); err != nil {
		s.cfg.Logger.Error("lark registration: commit",
			"session_id", sess.id, "err", err)
		sess.markError(RegistrationReasonInternalError, err.Error(), s.gcDeadline())
		return
	}
	sess.markSuccess(inst.ID, s.gcDeadline())
	// 在提交点发布，让每个 workspace 客户端的连接徽标无需刷新页面就更新——
	// 而不是只有恰好把状态端点轮询到 success 的那个 tab 才更新。
	s.publishInstalled(sess.workspaceID, inst.ID)
	s.cfg.Logger.Info("lark registration: install complete",
		"session_id", sess.id,
		"workspace_id", uuidString(sess.workspaceID),
		"agent_id", uuidString(sess.agentID),
		"installation_id", uuidString(inst.ID))
}

// liveOwnerConflictMessage 为“飞书 app 的路由槽位被 LIVE 拥主持有、重绑被拒”
// 拼用户可见文案。它指出拥主是哪一类，让用户知道怎么恢复——而不是旧的全覆盖
// 文案“已连接到不同的 Multica workspace”（真实拥主就在本 workspace 时这话是
// 谎言，见 #4810）。在基础连接池上查（不是在已中止的 upsert 事务上）。若槽位
// 此刻其实空了（upsert 与本读之间发生并发断连），一条通用提示就够了——用户
// 重试即可。
func (s *RegistrationService) liveOwnerConflictMessage(ctx context.Context, requestingWorkspaceID pgtype.UUID, appID string) string {
	owner, err := s.queries.InstallationOwnerByAppID(ctx, appID)
	if err != nil {
		return "This Feishu app is already connected to another agent. Disconnect it there first, then connect it here."
	}
	switch {
	case owner.WorkspaceID != requestingWorkspaceID:
		return "This Feishu app is already connected to a different Multica workspace. Disconnect it there before connecting it here."
	case owner.AgentArchivedAt.Valid:
		return "This Feishu app is connected to an archived agent in this workspace. Restore that agent, or disconnect its bot, before connecting it here."
	default:
		return "This Feishu app is already connected to another agent in this workspace. Disconnect it there first, then connect it here."
	}
}

// gcDeadline 返回基于 SessionTTL 的 GC 截止时刻。
func (s *RegistrationService) gcDeadline() time.Time {
	return s.cfg.Now().Add(s.cfg.SessionTTL)
}

// gcExpiredLocked 丢弃任何 `gcAfter` 已过的会话。Pending 会话不在这里 GC——
// runPolling 在终止时会设置它们的 gcAfter，而按 deadline 过期的会话会自行关闭。
func (s *RegistrationService) gcExpiredLocked() {
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.sessions {
		sess.mu.Lock()
		drop := !sess.gcAfter.IsZero() && sess.gcAfter.Before(now)
		sess.mu.Unlock()
		if drop {
			delete(s.sessions, id)
		}
	}
}

// ErrRegistrationSessionNotFound 是服务对未知/已 GC 会话返回的错误。handler 把
// 它映射成 404。
var ErrRegistrationSessionNotFound = errors.New("lark registration: session not found")

// randomSessionID 生成 URL-safe base64 的随机会话 id。
func randomSessionID() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// uuidEqual 比较两个 pgtype.UUID 是否相等（任一方 invalid 即不等）。
func uuidEqual(a, b pgtype.UUID) bool {
	if !a.Valid || !b.Valid {
		return false
	}
	return a.Bytes == b.Bytes
}

// botNamePreset 构造我们预填在 Lark PersonalAgent 创建表单上的显示名，让装出的
// bot 读起来是 "<agent> - Multica" 而不是 Lark 自动生成的“{用户姓名}的智能
// 助手”。Lark 把它当安装者仍可编辑的默认值；我们永远不会锁最终名。agent 名为空
// （防御——schema 里 Agent.Name 是 NOT NULL）时退化成纯 "Multica"，而不是悬空
// 的 " - Multica"。
func botNamePreset(agentName string) string {
	name := strings.TrimSpace(agentName)
	if name == "" {
		return "Multica"
	}
	return name + " - Multica"
}

// uuidString 是本包本地的 UUID→字符串辅助（见 hub.go）；为避免符号冲突，重声明
// `func uuidString(u pgtype.UUID) string` 已被移除。
//
// InstallationService.box 未导出，但本文件能访问它，因为两者都在 package `lark`
// 里；我们在 finishSuccess 里直接读它，让 Seal 发生在 DB 事务之外（否则加密调用
// 期间会一直持着行锁）。
