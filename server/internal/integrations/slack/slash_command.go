// slash_command.go：Slack `/issue`、`/new`、`/clear` 斜杠命令的端到端实现。
//
// 为什么是「真·斜杠命令」而不是消息前缀：Slack 客户端会把以 `/` 开头的消息拦截
// 成斜杠命令、根本不送给应用，所以消息前缀形态的 `/issue` 在 Slack 上永远到不了
// 我们手里（MUL-3908）。只有在应用 manifest 里把 `/issue` 注册成真正的斜杠命令，
// 它才会以 EventTypeSlashCommand 形态、走同一条 Socket Mode 连接送达。
//
// `/issue` 是「快速建单」入口，但它自己并不建 issue：它把发起人用自然语言写的
// 描述当 prompt，向该安装的 agent 投递一个 quick-create 任务——与 Web 端「快速
// 建单」弹窗走同一条流水线（TaskService.EnqueueQuickCreateTask）。agent 在后台
// 把 prompt 整理成规范的 `multica issue create`，issue 因而得到正式标题 + 结构化
// 描述，而不是用户随手敲的那行原文。因为建单是异步的，命令先用 response_url 回
// 一条【仅发起人可见】的临时确认——此刻还没有 issue 编号可回——agent 完成后经
// 共享的 quick-create 完成路径，把结果作为 Multica inbox 通知推给发起人。它不开
// 启任何聊天会话/聊天运行。
//
// `/new` 与 `/clear` 是私聊里的会话/上下文控制（StartSlackDMChat /
// ClearSlackDMContext）；在频道里用会回一条引导，提示改用 @Multica 提及形式。
//
// 安装路由与身份/成员校验镜像消息路径（resolvers.go），让斜杠命令的快速建单与
// 其它 Slack 入口遵守同样的工作区边界和账号绑定；这些校验就地保留，避免触碰已
// 验证过的入站流水线。
package slack

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/slack-go/slack"

	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const issueSlashCommand = "/issue"
const newSlashCommand = "/new"
const clearSlashCommand = "/clear"

// 以下是对用户可见的临时回复文案，刻意保持简短；只有发起命令的人看得到。
const (
	slashUsageText            = "Tell me what to file, e.g. `/issue the login button does nothing on Safari`."
	slashQueuedText           = "✅ On it — I'm turning that into an issue. You'll get a Multica notification when it's ready."
	slashNotMemberText        = "You're not a member of this Multica workspace, so I can't file an issue for you."
	slashLinkAccountFallback  = "Link your Slack account to Multica first, then try `/issue` again."
	slashIssueLimitText       = "⚠️ This workspace has reached its issue limit. Open Multica to view the available recovery options."
	slashInternalErrorText    = "⚠️ Something went wrong creating the issue. Please try again."
	slashDisabledText         = "This Slack app isn't connected to Multica (or was disconnected). Ask a workspace admin to reconnect it."
	slashNewStartedText       = "✅ Started a new Multica chat."
	slashNewThreadGuideText   = "In a channel, start the new chat from the target thread with `@Multica /new`."
	slashClearStartedText     = "✅ Cleared the agent context in this Multica chat."
	slashClearThreadGuideText = "In a channel, clear the target thread's context with `@Multica /clear`."
)

// slashQueries 是斜杠命令处理器需要的窄接口查询子集。*db.Queries 满足它；测试
// 供假实现。安装/成员解析与消息路径的 resolver（resolvers.go）一致，但就地保留，
// 让已验证的入站流水线不受扰动。
type slashQueries interface {
	GetChannelInstallationByAppID(ctx context.Context, arg db.GetChannelInstallationByAppIDParams) (db.ChannelInstallation, error)
	GetChannelUserBindingByUserID(ctx context.Context, arg db.GetChannelUserBindingByUserIDParams) (db.ChannelUserBinding, error)
	GetMemberByUserAndWorkspace(ctx context.Context, arg db.GetMemberByUserAndWorkspaceParams) (db.Member, error)
}

// quickCreateEnqueuer 是斜杠命令把发起人 prompt 交给 agent 所需的
// *service.TaskService 窄接口。*service.TaskService 满足它；测试供假实现。
type quickCreateEnqueuer interface {
	EnqueueQuickCreateTask(ctx context.Context, workspaceID, requesterID, agentID, squadID pgtype.UUID, prompt, priority, dueDate string, projectID, parentIssueID pgtype.UUID, attachmentIDs []pgtype.UUID) (db.AgentTaskQueue, error)
}

// slashControlStarter 是 `/new`、`/clear` 两个控制命令背后的会话操作面。
type slashControlStarter interface {
	StartSlackDMChat(ctx context.Context, inst engine.ResolvedInstallation, userID pgtype.UUID, cmd slack.SlashCommand, envelopeID string) error
	ClearSlackDMContext(ctx context.Context, inst engine.ResolvedInstallation, userID pgtype.UUID, cmd slack.SlashCommand, envelopeID string) error
}

// SlashCommandProcessor 端到端处理 Slack 的 `/issue`、`/new`、`/clear` 命令。
type SlashCommandProcessor struct {
	q           slashQueries
	tasks       quickCreateEnqueuer
	control     slashControlStarter
	binding     bindingMinter
	appURL      string
	bindingPath string
	logger      *slog.Logger
	// respond 把一条临时回复 POST 到命令的 response_url。做成可注入的函数，
	// 测试就能在不碰 Slack 的前提下抓到回复内容。
	respond func(ctx context.Context, responseURL, text string) error
}

// SlashCommandConfig 配置处理器。Binding + AppURL 是「未绑定用户」回复引导的前
// 提；缺了它们该情形就退化成一句朴素指引。Tasks + Queries 则是命令能干活的
// 硬前提。
type SlashCommandConfig struct {
	Queries     *db.Queries
	Tasks       quickCreateEnqueuer
	Control     slashControlStarter
	Binding     bindingMinter
	AppURL      string
	BindingPath string // 绑定页路径，默认 "/slack/bind"
	Logger      *slog.Logger
}

// NewSlashCommandProcessor 构造处理器。默认的 responder 把一条临时消息 POST 到
// 命令的 response_url（带签名的 webhook，不需要 bot 令牌）。
func NewSlashCommandProcessor(cfg SlashCommandConfig) *SlashCommandProcessor {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	bindingPath := cfg.BindingPath
	if bindingPath == "" {
		bindingPath = "/slack/bind"
	}
	if !strings.HasPrefix(bindingPath, "/") {
		bindingPath = "/" + bindingPath
	}
	p := &SlashCommandProcessor{
		q:           cfg.Queries,
		tasks:       cfg.Tasks,
		control:     cfg.Control,
		binding:     cfg.Binding,
		appURL:      strings.TrimRight(cfg.AppURL, "/"),
		bindingPath: bindingPath,
		logger:      logger,
	}
	p.respond = func(ctx context.Context, responseURL, text string) error {
		return slack.PostWebhookContext(ctx, responseURL, &slack.WebhookMessage{
			ResponseType: slack.ResponseTypeEphemeral,
			Text:         text,
		})
	}
	return p
}

// Handle 处理一条斜杠命令并投出临时回复。它在脱离的 goroutine 里被调用（socket
// 接收循环早已 ACK 过），因此永不返回错误——每个结局都化为一句对用户可见的消息。
func (p *SlashCommandProcessor) Handle(ctx context.Context, cmd slack.SlashCommand) {
	p.HandleEnvelope(ctx, cmd, "")
}

// HandleEnvelope 与 Handle 相同，只是额外保留 Socket Mode 的持久信封 id——控制
// 类命令靠它做去重。
func (p *SlashCommandProcessor) HandleEnvelope(ctx context.Context, cmd slack.SlashCommand, envelopeID string) {
	command := strings.TrimSpace(cmd.Command)
	if !strings.EqualFold(command, issueSlashCommand) && command != newSlashCommand && command != clearSlashCommand {
		return
	}
	var text string
	if command == newSlashCommand || command == clearSlashCommand {
		text = p.processControl(ctx, cmd, envelopeID)
	} else {
		text = p.process(ctx, cmd)
	}
	if text == "" || cmd.ResponseURL == "" {
		return
	}
	if err := p.respond(ctx, cmd.ResponseURL, text); err != nil {
		p.logger.WarnContext(ctx, "slack slash command: response_url reply failed",
			"app_id", cmd.APIAppID, "error", err)
	}
}

// processControl 处理 `/new` / `/clear` 两个控制命令，返回要回给用户的文案：
// 校验安装、账号绑定、成员身份都通过后，才在私聊里真正调用会话控制（Start/
// Clear）。
func (p *SlashCommandProcessor) processControl(ctx context.Context, cmd slack.SlashCommand, envelopeID string) string {
	inst, err := p.resolveInstallation(ctx, cmd.APIAppID, cmd.TeamID)
	if err != nil {
		if errors.Is(err, engine.ErrInstallationNotFound) {
			return slashDisabledText
		}
		return slashInternalErrorText
	}
	if !inst.Active {
		return slashDisabledText
	}
	userID, err := p.resolveUser(ctx, inst, cmd.UserID)
	if err != nil {
		switch {
		case errors.Is(err, engine.ErrSenderUnbound):
			return p.bindingText(ctx, inst, cmd.UserID)
		case errors.Is(err, engine.ErrSenderNotMember):
			return slashNotMemberText
		default:
			return slashInternalErrorText
		}
	}
	// 命令可用性与频道路由引导，同样要过「安装存在、账号已绑定、成员在工作区」
	// 三道闸，与其它 Slack 入口保持一致。
	// 斜杠负载不带 thread_ts；在频道里瞎猜一个目标线程去「开新会话/清上下文」，
	// 可能动到毫不相干的对话。频道用户请用 @Multica 提及形态——那会走常规 Router，
	// 带精确的线程根。
	if !strings.HasPrefix(cmd.ChannelID, "D") {
		if cmd.Command == clearSlashCommand {
			return slashClearThreadGuideText
		}
		return slashNewThreadGuideText
	}
	if p.control == nil {
		return slashInternalErrorText
	}
	var startErr error
	if cmd.Command == clearSlashCommand {
		startErr = p.control.ClearSlackDMContext(ctx, inst, userID, cmd, envelopeID)
	} else {
		startErr = p.control.StartSlackDMChat(ctx, inst, userID, cmd, envelopeID)
	}
	if startErr != nil {
		if errors.Is(startErr, engine.ErrDuplicate) {
			if cmd.Command == clearSlashCommand {
				return slashClearStartedText
			}
			return slashNewStartedText
		}
		p.logger.WarnContext(ctx, "slack slash command: session control failed",
			"outcome", "session_control_failed", "command", cmd.Command,
			"channel_type", string(TypeSlack), "app_id", cmd.APIAppID, "error", startErr)
		return slashInternalErrorText
	}
	if cmd.Command == clearSlashCommand {
		return slashClearStartedText
	}
	return slashNewStartedText
}

// process 跑 `/issue` 命令并返回要回给用户的临时文案。
func (p *SlashCommandProcessor) process(ctx context.Context, cmd slack.SlashCommand) string {
	prompt := strings.TrimSpace(cmd.Text)
	if prompt == "" {
		return slashUsageText
	}

	inst, err := p.resolveInstallation(ctx, cmd.APIAppID, cmd.TeamID)
	if err != nil {
		if !errors.Is(err, engine.ErrInstallationNotFound) {
			p.logger.WarnContext(ctx, "slack slash command: resolve installation failed",
				"app_id", cmd.APIAppID, "error", err)
			return slashInternalErrorText
		}
		return slashDisabledText
	}
	if !inst.Active {
		return slashDisabledText
	}

	userID, err := p.resolveUser(ctx, inst, cmd.UserID)
	if err != nil {
		switch {
		case errors.Is(err, engine.ErrSenderUnbound):
			return p.bindingText(ctx, inst, cmd.UserID)
		case errors.Is(err, engine.ErrSenderNotMember):
			return slashNotMemberText
		default:
			p.logger.WarnContext(ctx, "slack slash command: resolve user failed",
				"app_id", cmd.APIAppID, "error", err)
			return slashInternalErrorText
		}
	}

	// 把用户原始的自然语言描述交给该安装的 agent 去快速建单；agent 在后台整理
	// 出规范 issue，并记到已绑定的成员名下。不传 project/parent/附件、也不走
	// squad 路由——斜杠命令直接指向安装自己的 agent。
	if _, err := p.tasks.EnqueueQuickCreateTask(
		ctx,
		inst.WorkspaceID,
		userID,
		inst.AgentID,
		pgtype.UUID{}, // 无 squad——直接派给安装的 agent
		prompt,
		"",            // 不显式设优先级
		"",            // 不显式设截止日
		pgtype.UUID{}, // 无 project
		pgtype.UUID{}, // 无父 issue
		nil,           // 无附件
	); err != nil {
		var limitErr *service.IssueLimitReachedError
		if errors.As(err, &limitErr) {
			return slashIssueLimitText
		}
		p.logger.WarnContext(ctx, "slack slash command: enqueue quick-create failed",
			"app_id", cmd.APIAppID, "error", err)
		return slashInternalErrorText
	}
	return slashQueuedText
}

// resolveInstallation 把命令的 api_app_id（外加事件所属 team）映射到它的安装，
// 应用与入站路由相同的 team 作用域保护。
func (p *SlashCommandProcessor) resolveInstallation(ctx context.Context, appID, teamID string) (engine.ResolvedInstallation, error) {
	inst, err := p.q.GetChannelInstallationByAppID(ctx, db.GetChannelInstallationByAppIDParams{
		ChannelType: string(TypeSlack),
		AppID:       appID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.ResolvedInstallation{}, engine.ErrInstallationNotFound
		}
		return engine.ResolvedInstallation{}, err
	}
	if !installationServesTeam(inst.Config, teamID) {
		return engine.ResolvedInstallation{}, engine.ErrInstallationNotFound
	}
	return engine.ResolvedInstallation{
		ID:              inst.ID,
		WorkspaceID:     inst.WorkspaceID,
		AgentID:         inst.AgentID,
		InstallerUserID: inst.InstallerUserID,
		Active:          inst.Status == "active",
		Platform:        inst,
	}, nil
}

// resolveUser 把 Slack 用户 id 映射到已绑定的 Multica 用户，并复核工作区成员
// 身份（绑定与成员之间没有外键，得显式查）。产品级两种失败分别返回
// engine.ErrSenderUnbound 或 engine.ErrSenderNotMember。
func (p *SlashCommandProcessor) resolveUser(ctx context.Context, inst engine.ResolvedInstallation, slackUserID string) (pgtype.UUID, error) {
	binding, err := p.q.GetChannelUserBindingByUserID(ctx, db.GetChannelUserBindingByUserIDParams{
		InstallationID: inst.ID,
		ChannelUserID:  slackUserID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, engine.ErrSenderUnbound
		}
		return pgtype.UUID{}, err
	}
	if _, err := p.q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      binding.MulticaUserID,
		WorkspaceID: inst.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, engine.ErrSenderNotMember
		}
		return pgtype.UUID{}, err
	}
	return binding.MulticaUserID, nil
}

// bindingText 铸造一枚一次性绑定令牌并返回「先关联你的账号」引导，镜像出站
// 回复器 NeedsBinding 的消息。绑定服务或应用 URL 没配置时退化成一句朴素指引。
func (p *SlashCommandProcessor) bindingText(ctx context.Context, inst engine.ResolvedInstallation, slackUserID string) string {
	if p.binding == nil || p.appURL == "" {
		return slashLinkAccountFallback
	}
	token, err := p.binding.Mint(ctx, inst.WorkspaceID, inst.ID, slackUserID)
	if err != nil {
		p.logger.WarnContext(ctx, "slack slash command: mint binding token failed",
			"installation_id", inst.ID, "error", err)
		return slashLinkAccountFallback
	}
	bindURL := p.appURL + p.bindingPath + "?token=" + url.QueryEscape(token.Raw)
	// 把 URL 包成显式 Slack 链接，让 base64url 令牌里的 `_`/`-` 不被 mrkdwn 误
	// 转（与回复器同一理由）。
	return "👋 To file issues, link your Slack account to Multica: <" +
		bindURL + "|link your account>\n(This link expires in 15 minutes.)"
}
