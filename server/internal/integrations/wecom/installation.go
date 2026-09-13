// installation.go —— wecom 的 channel_installation 行「写入面」。智能机器人
// 密钥的 secretbox 加密集中在这里做，保证没有调用方在本文件边界之外碰明文；
// 而且它是通往 channel_installation 里 wecom 行的唯一路径——无论管理 CLI 还是
// HTTP 安装接口，最终都走 Upsert。
package wecom

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// InstallationParams 是给 InstallationService 的、带明文的输入。调用方把从
// 企微管理后台抄来的原始 (BotID, Secret) 对交进来；服务在数据碰库之前就把
// Secret 密封好。
type InstallationParams struct {
	WorkspaceID     pgtype.UUID
	AgentID         pgtype.UUID
	InstallerUserID pgtype.UUID

	// BotID 是企业微信管理后台展示的智能机器人标识。每个机器人稳定不变；既当
	// 订阅帧里的鉴权身份，也当持久化在 config->>'app_id' 的路由键。
	BotID string

	// Secret 是明文的长连接密钥，只在机器人创建时于管理后台展示一次。在本服务
	// 边界处就被密封。
	Secret string

	// BotDisplayName 是机器人在会话里显示的名字。可选项；为什么需要它、空值回退
	// 成什么见 Installation.BotDisplayName。对「同一个机器人」做重装时空着这个值，
	// 会保留行上已有的名字。
	BotDisplayName string
}

// InstallationService 通过共享的 channel_installation 表来创建、刷新、撤销
// wecom 智能机器人安装。它要求非 nil 的 *secretbox.Box，避免调用方无意间退回
// 明文存储——这与 lark.InstallationService 强制的不变量一致。
type InstallationService struct {
	store *Store
	tx    engine.TxStarter
	box   *secretbox.Box

	// probe 在下面的回收（reclaim）拿凭据动手之前，先证明调用方真的掌控这
	// 台机器人。绝不会为 nil：NewInstallationService 把它默认成真实握手探测、
	// 且拒绝显式传 nil，Upsert 也拒绝在没有它的情况下运行。控制权证明是一条
	// 安全不变量，所以没有「失败即放行」的开关——安装时连不上企微就吃 503、
	// 稍后重试，绝不会允许拿未核验的凭据去覆盖别人的行。
	probe CredentialProbe
}

// InstallationOption 用于在构造时配置该服务。
type InstallationOption func(*InstallationService)

// WithCredentialProbe 覆盖控制权校验。生产环境省略它，用 NewHandshakeProbe
// (nil, "")；测试传假实现。传 nil 在构造时即报错，而不是「关掉这项校验」的
// 途径。
func WithCredentialProbe(p CredentialProbe) InstallationOption {
	return func(s *InstallationService) { s.probe = p }
}

// ErrProbeRequired 在某个服务试图「不带凭证探测」被构造——或运行时才暴露——
// 时返回。控制权证明为一条会硬删别的工作区安装、及其下所有绑定的语句把门，
// 所以 nil 探测是接线 bug，不是一种部署模式。在构造时就失败关闭，让这个错误
// 在启动时浮出水面，而不是第一次安装时才爆。
var ErrProbeRequired = errors.New("wecom: InstallationService requires a non-nil CredentialProbe")

// NewInstallationService 把服务绑定到 queries 句柄、一个事务启动器（保证
// 加锁-读-探测-回收-写 这个序列原子执行）、以及一把用于静态加密的 secretbox
// 钥匙。Box 为 nil 时返回错误；调用方应把它亮出来（不要退回明文）。
//
// 凭证探测默认打真实的企业微信握手。不允许开 socket 的调用方（测试）通过
// WithCredentialProbe 传假实现；没有任何选项能把这校验关掉。
func NewInstallationService(queries *db.Queries, tx engine.TxStarter, box *secretbox.Box, opts ...InstallationOption) (*InstallationService, error) {
	if box == nil {
		return nil, errors.New("wecom: InstallationService requires a non-nil secretbox.Box")
	}
	svc := &InstallationService{
		store: NewStore(queries),
		tx:    tx,
		box:   box,
		probe: NewHandshakeProbe(nil, ""),
	}
	for _, o := range opts {
		o(svc)
	}
	if svc.probe == nil {
		return nil, ErrProbeRequired
	}
	return svc, nil
}

// Upsert 创建或刷新一条安装记录。channel_installation 的冲突键是
// (workspace_id, agent_id, channel_type)，所以对已存在的 (workspace, agent,
// wecom) 三元组重复执行 Upsert，会轮换行上每个字段、并把状态翻回 'active'。
// 返回的 Installation 反映写库之后的真实状态。
//
// 整个序列——锁住机器人的路由槽、读当前主人、拒绝或探测、回收、写入——跑在
// 同一事务里。顺序本身就是关键：
//
//   - idx_channel_installation_type_appid 在 (channel_type, config->>'app_id')
//     上是 UNIQUE，且不含 workspace——所以一个 bot id 的路由槽在整个部署上是
//     全局的，而 ReclaimDeadChannelInstallationByAppID 会连同持有它的那个主人、
//     以及其下所有用户绑定、会话绑定、挂起 token 一起硬删。Bot id 不是秘密——
//     每个和机器人说过话的成员都能读到——所以回收必须用「调用方确实掌控该机器
//     人」的证明来把门。这就是 probe。
//
//   - 但 probe 本身对活的平台是一个副作用：它要订阅，而企微规定每个机器人只允许
//     一个在线订阅者，所以订阅会挤掉当前在线的人。在知道谁是槽主人之前就跑它，
//     意味着一个「即将被拒绝」的请求照样能把真正主人顶下线——反复来几次就是
//     对一台调用方本无权碰的机器人做拒绝服务。所以先读主人：注定失败的请求直接
//     返回冲突，本地和企业微信两端都没碰过任何东西。
//
//   - 两步读的都是「可能被并发安装或重连改动」的状态，所以它们在槽自身上面串行化
//     （LockChannelInstallationAppIDSlot，一个事务级 advisory 锁）。在事务外先读
//     一遍会留下 TOCTOU（检查与使用之间竞态）窗口。
func (s *InstallationService) Upsert(ctx context.Context, p InstallationParams) (Installation, error) {
	if err := validateInstallationParams(p); err != nil {
		return Installation{}, err
	}
	if s.probe == nil {
		// 双保险：为绕过 NewInstallationService、直接用 struct 字面量构造的服务
		// 准备的。任何东西都不得未经证明就走到回收，所以要在别的「可能先失败、
		// 把这个问题盖住」的检查之前先查它。
		return Installation{}, ErrProbeRequired
	}
	if s.tx == nil {
		return Installation{}, errors.New("wecom: InstallationService requires a transaction starter")
	}

	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return Installation{}, fmt.Errorf("wecom: begin install tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.store.WithTx(tx)

	// 串行化边界。持有到提交/回滚为止，保证下面每一步对「这台机器人归谁」读到
	// 的是同一个一致答案。
	if err := qtx.Queries.LockChannelInstallationAppIDSlot(ctx, db.LockChannelInstallationAppIDSlotParams{
		ChannelType: channelTypeWecom,
		AppID:       p.BotID,
	}); err != nil {
		return Installation{}, fmt.Errorf("wecom: lock bot routing slot: %w", err)
	}

	// 现在是谁占着这个槽——在锁内、任何外部动作之前读。
	if err := botSlotConflictErr(ctx, qtx, p); err != nil {
		return Installation{}, err
	}

	// 此刻已经没有活的连接会被波及：槽是空闲、revoked、孤儿，或本来就是调用方
	// 自己的。在回收拿它动手之前，先证明控制权。
	if err := s.probe.Probe(ctx, p.BotID, p.Secret); err != nil {
		return Installation{}, err
	}

	sealed, err := s.box.Seal([]byte(p.Secret))
	if err != nil {
		return Installation{}, fmt.Errorf("wecom: encrypt secret: %w", err)
	}

	// 先回收、再写入。UpsertChannelInstallation 冲突在 (workspace_id, agent_id,
	// channel_type) 上，而 (channel_type, app_id) 这个槽由
	// idx_channel_installation_type_appid 守护。Disconnect 只把状态翻成
	// 'revoked'——并不释放行——所以没有回收这一步的话，一台从 agent A 撤销的
	// 机器人永远接不上 agent B：upsert 撞不上自己的 ON CONFLICT、反而踩了唯一
	// 索引，管理员只会被一句「请先到那边断开」打发，可那台机器人早就断开了、
	// 也没有任何 UI 控件能释放它。ReclaimDeadChannelInstallationByAppID 删除本
	// bot 槽位的任何 DEAD 主人（被「不同 (workspace, agent)」持有的 revoked 行、
	// 或 workspace/agent 已被删的孤儿行），并在同一条语句里清掉它的依赖行；而
	// 活主人（active 或归档 agent）则留在原地，去触发下面的唯一索引冲突。
	// botSlotConflictErr 在上面已经拒绝过所有活主人，所以走到这次唯一性违例只
	// 剩「槽在锁下竟然换了手」一种可能；botOwnerConflictErr 仍会把它翻译成准确
	// 的 409，而不是裸 Postgres 报错串。与 slack/install.go 的 persistInstall
	// 对齐。
	if _, err := qtx.Queries.ReclaimDeadChannelInstallationByAppID(ctx, db.ReclaimDeadChannelInstallationByAppIDParams{
		ChannelType: channelTypeWecom,
		AppID:       p.BotID,
		WorkspaceID: p.WorkspaceID,
		AgentID:     p.AgentID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// pgx.ErrNoRows 只代表「没有死行可收」——是空操作，不是失败。
		return Installation{}, fmt.Errorf("wecom: reclaim dead installation: %w", err)
	}

	// 这条 upsert 即将覆盖的行，用事务句柄读，好和写入串行化。首次安装时为全零。
	carried, err := currentInstallation(ctx, qtx.Queries, p.WorkspaceID, p.AgentID)
	if err != nil {
		return Installation{}, err
	}
	// 会话名在对话框里是可选项，所以管理员在轮换泄露的密钥时常把它留空——而一
	// 旦留空被清掉，群里的斜杠命令就会退回这个字段本来要替代的「空白启发式猜
	// 名」。因此保留行上已有的名字。换机器人（bot SWAP）则不同：旧名字属于旧机
	// 器人，把它带过去会让新机器人应答一个不是自己的 @。
	displayName := p.BotDisplayName
	if displayName == "" && carried.BotID == p.BotID {
		displayName = carried.BotDisplayName
	}
	cfg, err := encodeInstallConfig(Installation{
		BotID:           p.BotID,
		SecretEncrypted: sealed,
		BotDisplayName:  displayName,
	})
	if err != nil {
		return Installation{}, err
	}

	row, err := qtx.Queries.UpsertChannelInstallation(ctx, db.UpsertChannelInstallationParams{
		WorkspaceID:     p.WorkspaceID,
		AgentID:         p.AgentID,
		ChannelType:     channelTypeWecom,
		Config:          cfg,
		InstallerUserID: p.InstallerUserID,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			// 说明还有活主人占着槽。用非事务连接去读主人（这条事务此刻已处于
			// aborted 状态，用不了），把它的名字报出来。
			return Installation{}, s.botOwnerConflictErr(ctx, p.WorkspaceID, p.BotID)
		}
		return Installation{}, fmt.Errorf("wecom: upsert installation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Installation{}, fmt.Errorf("wecom: commit install tx: %w", err)
	}
	return installationFromRow(row)
}

// currentInstallation 读取某条 upsert 即将替换的 (workspace, agent, wecom) 行；
// 不存在时返回零值 Installation。
//
// 没有按这个三元组做键的查询——upsert 的冲突键直到现在都没有任何读路径需要过
// ——所以这里只能过滤该 workspace 的 wecom 行（数量也就几行）。在调用方的查询
// 句柄上执行，正是它与写入可串行化的原因。
func currentInstallation(ctx context.Context, q *db.Queries, workspaceID, agentID pgtype.UUID) (Installation, error) {
	rows, err := q.ListChannelInstallationsByWorkspace(ctx, db.ListChannelInstallationsByWorkspaceParams{
		WorkspaceID: workspaceID,
		ChannelType: channelTypeWecom,
	})
	if err != nil {
		return Installation{}, fmt.Errorf("wecom: read current installation: %w", err)
	}
	for _, row := range rows {
		if row.AgentID == agentID {
			return installationFromRow(row)
		}
	}
	return Installation{}, nil
}

// 下面是 Upsert 解决不了的那一种冲突的哨兵错误：这台机器人已经连在别处了。
// UpsertChannelInstallation 冲突在 (workspace_id, agent_id, channel_type) 上，
// 而 idx_channel_installation_type_appid 在 (channel_type, config->>'app_id')
// 上是 UNIQUE——所以把「同一台机器人」连到第二个 agent 时，根本不撞 ON CONFLICT
// 子句，而是直接踩唯一索引。没有这些哨兵，管理员会在 toast 里读到裸 Postgres
// 文本（"duplicate key value violates unique constraint …"）。
//
// 一台机器人一条连接：企微长连接每个机器人只允许一个在线订阅者，两个 agent
// 没法共用。出路永远是先释放机器人，这正是每条消息要说的。
var (
	// ErrBotOwnedBySameWorkspace —— 管理员自己工作区里的另一个 agent 占着这台
	// 机器人。在同一个设置页就能解除。
	ErrBotOwnedBySameWorkspace = errors.New("wecom: this bot is already connected to another agent in this workspace")

	// ErrBotOwnedByArchivedAgent —— 持有者是归档 agent，不会出现在 agent 列表
	// 里，于是机器人看起来空闲、其实没有。
	ErrBotOwnedByArchivedAgent = errors.New("wecom: this bot is connected to an archived agent in this workspace")

	// ErrBotOwnedByAnotherWorkspace —— 持有者完全在视线之外，只有那边有权限的
	// 人才能释放。
	ErrBotOwnedByAnotherWorkspace = errors.New("wecom: this bot is already connected to a different Multica workspace")
)

// pgUniqueViolation 是 Postgres 的 unique_violation SQLSTATE。
const pgUniqueViolation = "23505"

// botSlotConflictErr 只回答一个问题：这次安装到底能不能动 (wecom, bot_id) 路由
// 槽？能就返回 nil，不能就返回该拒的哨兵。必须在持有
// LockChannelInstallationAppIDSlot 的事务内调用——在外面，答案就是一句猜测，
// 很可能在它被用上之前就被并发安装或重连推翻了。
//
// 它是 Upsert 能产生的两个副作用共同的前置闸门：探测的订阅（会挤掉这台机器
// 人在企微的在线连接）和回收的硬删（会带走行及其绑定）。在这里拒绝，调用方零
// 代价、现任主人零代价。
//
// 分类口径与 ReclaimDeadChannelInstallationByAppID 自己对「死」的定义对齐，
// 二者不会对「哪些行可以拿」逐渐产生分歧：
//
//	无行             → 槽空闲。
//	孤儿行           → workspace 或 agent 行已不在；回收释放它，且它没连着任何人。
//	revoked          → 主人明确说「这机器人我不要了」；回收收走它（若本来就是这个
//	                   调用方自己的行，则 upsert 原地重新激活）。
//	active、自己的    → 对这台 (workspace, agent) 已持有的机器人做重装或密钥轮换。
//	                   不算冲突：upsert 原地刷新，回收也会放过它。
//	active、别人的    → 拒绝。整个闸门就是为这一种情况存在的。
func botSlotConflictErr(ctx context.Context, qtx *Store, p InstallationParams) error {
	owner, err := qtx.Queries.GetChannelInstallationSlotOwnerByAppID(ctx, db.GetChannelInstallationSlotOwnerByAppIDParams{
		ChannelType: channelTypeWecom,
		AppID:       p.BotID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("wecom: read bot routing slot owner: %w", err)
	}
	switch {
	case !owner.WorkspaceExists || !owner.AgentExists:
		return nil
	case owner.Status == string(InstallationRevoked):
		return nil
	case owner.WorkspaceID == p.WorkspaceID && owner.AgentID == p.AgentID:
		return nil
	case owner.WorkspaceID != p.WorkspaceID:
		return ErrBotOwnedByAnotherWorkspace
	case owner.AgentArchivedAt.Valid:
		return ErrBotOwnedByArchivedAgent
	default:
		return ErrBotOwnedBySameWorkspace
	}
}

// botOwnerConflictErr 报出是谁占着 (wecom, bot_id) 路由槽，好让 handler 告诉
// 管理员该去哪处理，与 slack 的 owner-conflict 分类对齐。它在 upsert 失败之后
// 才读，所以槽已被释放、或查询失败时，都退回跨工作区那条消息：它是唯一在
// 「该去哪找」这件事上永远不会说错的话，而且重试本来就会成功。
func (s *InstallationService) botOwnerConflictErr(ctx context.Context, requestingWorkspaceID pgtype.UUID, botID string) error {
	owner, err := s.store.Queries.GetChannelInstallationOwnerByAppID(ctx, db.GetChannelInstallationOwnerByAppIDParams{
		ChannelType: channelTypeWecom,
		AppID:       botID,
	})
	if err != nil {
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

// Revoke 把状态翻成 'revoked'——行被保留以便审计轨迹仍可查询，后续一次 Upsert
// 会原子地把它翻回 'active'。revoked 行会被路由器的安装解析器跳过
// （Active=false → 无效事件丢弃并记审计）。
func (s *InstallationService) Revoke(ctx context.Context, id pgtype.UUID) error {
	return s.store.Queries.SetChannelInstallationStatus(ctx, db.SetChannelInstallationStatusParams{
		ID:     id,
		Status: string(InstallationRevoked),
	})
}

// ErrInstallationNotFound 由 GetInWorkspace 在「给定 (id, workspace) 没有行」
// 或「行属于别的 channel_type」时返回。它不同于裸 pgx.ErrNoRows，好让 HTTP
// handler 不必 import pgx 就能把它映射成 404。
var ErrInstallationNotFound = errors.New("wecom: installation not found")

// ListByWorkspace 按创建顺序返回某工作区所有的 wecom 安装。设置页和
// Agent-Integrations 页用它渲染「已连接机器人」列表；revoked 行也一并返回，
// 好让运维看到历史（UI 按 Status 过滤）。
func (s *InstallationService) ListByWorkspace(ctx context.Context, workspaceID pgtype.UUID) ([]Installation, error) {
	rows, err := s.store.Queries.ListChannelInstallationsByWorkspace(ctx, db.ListChannelInstallationsByWorkspaceParams{
		WorkspaceID: workspaceID,
		ChannelType: channelTypeWecom,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Installation, 0, len(rows))
	for _, row := range rows {
		inst, err := installationFromRow(row)
		if err != nil {
			return nil, fmt.Errorf("wecom: decode installation %s: %w", row.ID.String(), err)
		}
		out = append(out, inst)
	}
	return out, nil
}

// GetInWorkspace 按 (id, workspace_id) 限定加载一条安装，这样从别的工作区伪造
// 一个 UUID 只会得到 not-found，而不是泄露「这条记录存在」。行缺失或行存在但
// 属于别的 channel_type 时都返回 ErrInstallationNotFound。
func (s *InstallationService) GetInWorkspace(ctx context.Context, id, workspaceID pgtype.UUID) (Installation, error) {
	row, err := s.store.Queries.GetChannelInstallationInWorkspace(ctx, db.GetChannelInstallationInWorkspaceParams{
		ID:          id,
		WorkspaceID: workspaceID,
		ChannelType: channelTypeWecom,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Installation{}, ErrInstallationNotFound
		}
		return Installation{}, err
	}
	return installationFromRow(row)
}

// ErrInvalidInstallationParams 标记「调用方填点东西就能修好」的请求，区别于
// 我们自己的失败。它存在的目的是让 HTTP 层能区分：缺字段是调用方的 400；
// ErrCredentialsRejected 和 ErrCredentialsUnverifiable 承载「真的问了企微」的
// 两种结果（400 和 503）；剩下的一切是 500——因为在 Postgres 只是短暂失联的
// 时候告诉管理员「你凭据错了」，会推着他们去轮换一把没问题、且轮换后无法找回
// 的企微密钥。完整矩阵见 writeWecomInstallError。
var ErrInvalidInstallationParams = errors.New("wecom: invalid installation parameters")

// validateInstallationParams 是写库前对必填字段的轻量检查。它不对企业微信做
// 任何核验。
func validateInstallationParams(p InstallationParams) error {
	missing := ""
	switch {
	case !p.WorkspaceID.Valid:
		missing = "workspace_id"
	case !p.AgentID.Valid:
		missing = "agent_id"
	case !p.InstallerUserID.Valid:
		missing = "installer_user_id"
	case p.BotID == "":
		missing = "bot_id"
	case p.Secret == "":
		missing = "secret"
	default:
		return nil
	}
	return fmt.Errorf("%w: %s is required", ErrInvalidInstallationParams, missing)
}
