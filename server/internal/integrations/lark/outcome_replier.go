// outcome_replier.go：按 dispatcher/引擎给出的“判定结果”回贴对应的飞书侧
// 回复卡片。
package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// OutcomeReplier 对 Dispatcher 的判定作出反应，回贴适当的飞书侧回复卡片。
// 这是 hub.go 里 `EventEmitter` 契约的出站半边：NeedsBinding 向发送者的
// open_id 发绑定引导；AgentOffline / AgentArchived 往聊天里推状态通知；
// FreshPending / IssueUsage 推指令引导。OutcomeIngested 由 Patcher 负责
// （任务生命周期）；OutcomeDropped 保持静默。
//
// Reply 按设计是 best-effort：一次飞书瞬时故障绝不能弄挂入站管线。任何命令
// 状态或聊天消息在到达这里时已经持久化，回复失败也不可能回滚它们。错误只记
// 日志并被吞掉。
type OutcomeReplier interface {
	Reply(ctx context.Context, inst Installation, msg InboundMessage, res DispatchResult)
}

// OutcomeReplierQueries 是 replier 需要的 *db.Queries 的窄子集。用接口钉住，
// 方便测试注入 fake。
type OutcomeReplierQueries interface {
	GetAgent(ctx context.Context, id pgtype.UUID) (db.Agent, error)
}

// BindingTokenMinter 是 outcome replier 从 BindingTokenService 需要的窄依赖。
// 保持接口形态，测试就能在不构造 DB 支撑的令牌服务的情况下钉住飞书绑定 URL。
type BindingTokenMinter interface {
	Mint(ctx context.Context, workspaceID, installationID pgtype.UUID, openID OpenID) (BindingToken, error)
}

// noopReplier 是“Lark 没接出站 APIClient（stub）或没接 BindingTokenService”
// 时的安全默认。它会记录每个本该产生回复的结果，让操作者在生产日志里看到缺口。
type noopReplier struct {
	log *slog.Logger
}

// Reply 实现 OutcomeReplier：把本应回复但没接线的结果记成警告日志。
func (n *noopReplier) Reply(ctx context.Context, inst Installation, msg InboundMessage, res DispatchResult) {
	switch res.Outcome {
	case OutcomeNeedsBinding, OutcomeAgentOffline, OutcomeAgentArchived, OutcomeFreshPending, OutcomeChatStarted, OutcomeIssueUsage:
		n.log.Warn("lark outcome replier: outbound reply skipped (replier not wired)",
			"outcome", string(res.Outcome),
			"installation_id", uuidString(inst.ID),
			"chat_id", string(msg.ChatID),
			"open_id", string(msg.SenderOpenID),
		)
	}
}

// NewNoopOutcomeReplier 返回 no-op replier。当生产接线不完整（如 stub
// APIClient、无绑定令牌服务）时作为兜底。
func NewNoopOutcomeReplier(log *slog.Logger) OutcomeReplier {
	if log == nil {
		log = slog.Default()
	}
	return &noopReplier{log: log}
}

// LarkOutcomeReplier 是生产环境的 OutcomeReplier。它组合了：
//
//   - APIClient —— 发送绑定引导卡片（面向 open_id）和离线/归档通知卡片
//     （面向 chat_id）。
//   - BindingTokenService —— 为 NeedsBinding 流程铸造一次性绑定令牌。
//   - CredentialsResolver —— 每次调用解密 app_secret（明文 secret 绝不驻留在
//     内存里的安装行上）。
//   - OutcomeReplierQueries —— 取卡片上展示的 agent 名。
//
// replier 在启动时构造一次，被 Hub 的所有 supervisor goroutine 共享；所有
// 依赖必须 goroutine 安全（标准实现都是）。
type LarkOutcomeReplier struct {
	client       APIClient
	bindingSvc   BindingTokenMinter
	credentials  CredentialsResolver
	queries      OutcomeReplierQueries
	appURL       string // 例如 https://multica.example，末尾斜杠已去掉
	bindingPath  string // 绑定 URL 的路径部分，默认 "/lark/bind"
	noticeHeader string // 离线/归档卡片用的 header 文案
	log          *slog.Logger
}

// OutcomeReplierConfig 装配生产 replier。AppURL 是用户点击兑换绑定令牌或打开
// issue 的 Multica Web 应用主机（如 https://multica.example），来自
// MULTICA_APP_URL，刻意与 MULTICA_PUBLIC_URL 分开——后者是给 webhook 与
// daemon 端点用的后端/API 公网 URL。AppURL 为空意味着绑定流程只能记录
// open_id，无法产出可点击卡片。其余字段在构造时兜底。
type OutcomeReplierConfig struct {
	APIClient   APIClient
	BindingSvc  BindingTokenMinter
	Credentials CredentialsResolver
	Queries     OutcomeReplierQueries
	AppURL      string
	BindingPath string
	Logger      *slog.Logger
}

// NewLarkOutcomeReplier 校验配置并返回生产 replier。缺失依赖时退回 noop，
// 让部分配置的部署也能稳健启动。
func NewLarkOutcomeReplier(cfg OutcomeReplierConfig) OutcomeReplier {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	if cfg.APIClient == nil || cfg.BindingSvc == nil || cfg.Credentials == nil || cfg.Queries == nil {
		return NewNoopOutcomeReplier(log)
	}
	if !cfg.APIClient.IsConfigured() {
		log.Warn("lark outcome replier: APIClient.IsConfigured()=false; downgrading to noop replier")
		return NewNoopOutcomeReplier(log)
	}
	if cfg.AppURL == "" {
		log.Warn("lark outcome replier: MULTICA_APP_URL not set; binding prompt CTA will not work")
	}
	bindingPath := cfg.BindingPath
	if bindingPath == "" {
		bindingPath = "/lark/bind"
	}
	if !strings.HasPrefix(bindingPath, "/") {
		bindingPath = "/" + bindingPath
	}
	return &LarkOutcomeReplier{
		client:       cfg.APIClient,
		bindingSvc:   cfg.BindingSvc,
		credentials:  cfg.Credentials,
		queries:      cfg.Queries,
		appURL:       strings.TrimRight(cfg.AppURL, "/"),
		bindingPath:  bindingPath,
		noticeHeader: "Multica",
		log:          log,
	}
}

// Reply 实现 OutcomeReplier。仔细读——switch 是“哪些结果会产出回复”的真相
// 来源，漏掉一个分支就静默丢掉用户可见的副作用。
func (r *LarkOutcomeReplier) Reply(ctx context.Context, inst Installation, msg InboundMessage, res DispatchResult) {
	switch res.Outcome {
	case OutcomeNeedsBinding:
		if err := r.sendBindingPrompt(ctx, inst, res); err != nil {
			r.log.Warn("lark outcome replier: binding prompt failed",
				"installation_id", uuidString(inst.ID),
				"open_id", string(res.SenderOpenID),
				"err", err.Error(),
			)
		}
	case OutcomeAgentOffline:
		if err := r.sendChatNotice(ctx, inst, msg, agentOfflineCopy); err != nil {
			r.log.Warn("lark outcome replier: offline notice failed",
				"installation_id", uuidString(inst.ID),
				"chat_id", string(msg.ChatID),
				"err", err.Error(),
			)
		}
	case OutcomeAgentArchived:
		if err := r.sendChatNotice(ctx, inst, msg, agentArchivedCopy); err != nil {
			r.log.Warn("lark outcome replier: archived notice failed",
				"installation_id", uuidString(inst.ID),
				"chat_id", string(msg.ChatID),
				"err", err.Error(),
			)
		}
	case OutcomeFreshPending:
		if err := r.sendChatNotice(ctx, inst, msg, freshPendingCopy); err != nil {
			r.log.Warn("lark outcome replier: fresh-start confirmation failed",
				"installation_id", uuidString(inst.ID),
				"chat_id", string(msg.ChatID),
				"err", err.Error(),
			)
		}
	case OutcomeChatStarted:
		if err := r.sendChatNotice(ctx, inst, msg, chatStartedCopy); err != nil {
			r.log.Warn("lark outcome replier: new-chat confirmation failed", "installation_id", uuidString(inst.ID), "chat_id", string(msg.ChatID), "err", err.Error())
		}
	case OutcomeIssueUsage:
		copy := issueUsageCopy
		if res.IssueUsageHadMedia {
			copy = issueUsageWithMediaCopy
		}
		if err := r.sendChatNotice(ctx, inst, msg, copy); err != nil {
			r.log.Warn("lark outcome replier: issue usage reply failed",
				"installation_id", uuidString(inst.ID),
				"chat_id", string(msg.ChatID),
				"err", err.Error(),
			)
		}
	case OutcomeIngested:
		// agent 的聊天回复本身走 Patcher。/issue 命令会有即时的产品结果：要么
		// 是新建的 issue，要么是拦住它的活跃重复 issue。以 IssueID.Valid 为门，
		// 普通聊天消息在这里保持安静。
		if res.IssueID.Valid {
			if err := r.sendIssueOutcome(ctx, inst, msg, res); err != nil {
				r.log.Warn("lark outcome replier: issue outcome reply failed",
					"installation_id", uuidString(inst.ID),
					"chat_id", string(msg.ChatID),
					"issue_id", uuidString(res.IssueID),
					"err", err.Error(),
				)
			}
		}
	case OutcomeDropped:
		// OutcomeDropped 只是信息性的，没有用户可见回复。
	}
}

// sendBindingPrompt 铸造绑定令牌并发绑定引导卡片给发送者。
func (r *LarkOutcomeReplier) sendBindingPrompt(ctx context.Context, inst Installation, res DispatchResult) error {
	if res.SenderOpenID == "" {
		return errors.New("missing sender open_id")
	}
	if r.appURL == "" {
		return errors.New("app_url not configured")
	}
	token, err := r.bindingSvc.Mint(ctx, inst.WorkspaceID, inst.ID, res.SenderOpenID)
	if err != nil {
		return fmt.Errorf("mint binding token: %w", err)
	}
	bindURL := r.appURL + r.bindingPath + "?token=" + url.QueryEscape(token.Raw)
	creds, err := r.installationCredentials(inst)
	if err != nil {
		return err
	}
	return r.client.SendBindingPromptCard(ctx, BindingPromptParams{
		InstallationID: creds,
		OpenID:         res.SenderOpenID,
		BindURL:        bindURL,
	})
}

// sendIssueOutcome 把“创建成功确认”或“命中活跃重复冲突”以纯文本发出，配置了
// AppURL 时带相关 issue 链接。
func (r *LarkOutcomeReplier) sendIssueOutcome(ctx context.Context, inst Installation, msg InboundMessage, res DispatchResult) error {
	if msg.ChatID == "" {
		return errors.New("missing chat_id")
	}
	creds, err := r.installationCredentials(inst)
	if err != nil {
		return err
	}
	text := issueCreatedText(res, r.appURL)
	if res.IssueDuplicate {
		text = issueDuplicateText(res, r.appURL)
	}
	// 与 Patcher 共用同一套“分类后退”逻辑：只有当“话题回帖”因话题收不了
	// 消息（触发消息被撤回、话题被禁用、消息被聚合）而失败时才退回群聊级发送，
	// 让产品结果不丢；transport/5xx/限流失败保持失败，而不是漏进群聊。
	return sendWithThreadFallback(r.log, "send issue outcome text", inboundReplyTarget(msg), func(t ReplyTarget) error {
		_, err := r.client.SendTextMessage(ctx, SendTextParams{
			InstallationID: creds,
			ChatID:         msg.ChatID,
			Text:           text,
			ReplyTarget:    t,
		})
		return err
	})
}

// inboundReplyTarget 当入站触发消息在飞书话题（话题）里时，让出站回复贴回该
// 话题。它镜像 threadReplyTarget（事件驱动 Patcher 用的），但读的是 replier
// 手上已有的实时 InboundMessage，因此不需要 DB 往返。thread_id 为空时返回零值
// ReplyTarget——群聊级发送，即非话题消息的原有行为。
func inboundReplyTarget(msg InboundMessage) ReplyTarget {
	if msg.ThreadID != "" && msg.MessageID != "" {
		return ReplyTarget{MessageID: msg.MessageID, InThread: true}
	}
	return ReplyTarget{}
}

// issueCreatedText 拼用户可见的创建确认文案。有可用的标识符时优先于裸数字——
// DispatchResult.IssueIdentifier 在可用时已经编码了 workspace 前缀。AppURL
// 可选：为空（自托管操作者没配 MULTICA_APP_URL）时消息仍确认 issue，只是少了
// 可点击的深链。
func issueCreatedText(res DispatchResult, appURL string) string {
	identifier := res.IssueIdentifier
	if identifier == "" {
		identifier = fmt.Sprintf("#%d", res.IssueNumber)
	}
	title := strings.TrimSpace(res.IssueTitle)
	var line string
	if title == "" {
		line = fmt.Sprintf("Created %s", identifier)
	} else {
		line = fmt.Sprintf("Created %s — %s", identifier, title)
	}
	if appURL == "" {
		return line
	}
	return line + "\n" + strings.TrimRight(appURL, "/") + "/issues/" + identifier
}

// issueDuplicateText 拼“未创建，命中活跃 issue”的用户可见冲突文案。
func issueDuplicateText(res DispatchResult, appURL string) string {
	identifier := res.IssueIdentifier
	if identifier == "" {
		identifier = fmt.Sprintf("#%d", res.IssueNumber)
	}
	title := strings.TrimSpace(res.IssueTitle)
	var line string
	if title == "" {
		line = fmt.Sprintf("Not created — active issue %s already exists.", identifier)
	} else {
		line = fmt.Sprintf("Not created — active issue %s already exists: %s", identifier, title)
	}
	if appURL == "" {
		return line
	}
	return line + "\n" + strings.TrimRight(appURL, "/") + "/issues/" + identifier
}

// sendChatNotice 向群里发一条带 header/body 的通知卡片（离线/归档等结果用）。
func (r *LarkOutcomeReplier) sendChatNotice(ctx context.Context, inst Installation, msg InboundMessage, body string) error {
	if msg.ChatID == "" {
		return errors.New("missing chat_id")
	}
	creds, err := r.installationCredentials(inst)
	if err != nil {
		return err
	}
	header := r.noticeHeader
	if agent, aerr := r.queries.GetAgent(ctx, inst.AgentID); aerr == nil && agent.Name != "" {
		header = agent.Name
	}
	cardJSON, err := renderNoticeCard(header, body)
	if err != nil {
		return fmt.Errorf("render notice card: %w", err)
	}
	// 与 sendIssueOutcome 相同的分类后退：只有“话题收不了消息”导致的话题回帖
	// 失败才退回群聊级发送；模棱两可的/transport 失败保持失败。
	return sendWithThreadFallback(r.log, "send notice card", inboundReplyTarget(msg), func(t ReplyTarget) error {
		_, err := r.client.SendInteractiveCard(ctx, SendCardParams{
			InstallationID: creds,
			ChatID:         msg.ChatID,
			CardJSON:       cardJSON,
			ReplyTarget:    t,
		})
		return err
	})
}

// installationCredentials 解密安装行的 app_secret 并组装飞书 API 凭据。
func (r *LarkOutcomeReplier) installationCredentials(inst Installation) (InstallationCredentials, error) {
	secret, err := r.credentials.DecryptAppSecret(inst)
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

// renderNoticeCard 为离线/归档等结果生成一张最小纯文本交互卡片。飞书要求每个
// 我们后续可能 patch 的卡片都带 update_multi=true；这些通知卡片是一次性的，
// 所以 update_multi 留 false（卡片保持原样）。header / body 与本集成其它地方
// 的中文语气一致。
func renderNoticeCard(header, body string) (string, error) {
	doc := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": "grey",
			"title":    map[string]any{"tag": "plain_text", "content": header},
		},
		"elements": []any{
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "plain_text",
					"content": body,
				},
			},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// agentOfflineCopy 与 agentArchivedCopy 等是“daemon/agent 不可用”两种结果的
// 用户可见中文文案，与 §4.6 设计一致：离线 agent 在 daemon 回来后会自动继续；
// 归档 agent 需要操作者介入。
const (
	agentOfflineCopy        = "Agent 当前离线，消息已记录。下次 daemon 上线后会自动继续处理。"
	agentArchivedCopy       = "这个 Agent 已被归档，无法继续处理消息。请联系工作区管理员恢复或重新绑定。"
	freshPendingCopy        = "✅ 已准备从空上下文运行。你的下一条聊天消息仍会进入当前对话，但不会带上之前的上下文。"
	chatStartedCopy         = "✅ 已新建 Multica 对话。你的下一条消息会进入该对话。"
	issueUsageCopy          = "请填写任务标题，格式如下：\n\n`/issue <标题>`\n`[描述]`（可选）"
	issueUsageWithMediaCopy = "请添加标题，并与图片或视频一起重新发送（*图片或视频可以位于命令之前或之后*）：\n\n`/issue <标题>`\n`[描述]`（可选）"
)
