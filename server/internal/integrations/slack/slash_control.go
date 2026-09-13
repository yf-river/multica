// slash_control.go：`/new`、`/clear` 斜杠命令的会话控制实现——在 Slack 私聊里
// 开启新聊天 / 清空 agent 上下文。
//
// /new 调 engine.ChatSession.StartSession 起一个新会话：正文为空只建会话、等下一
// 句话进来；非空则把这句话当首条消息、在提交会话的同一事务里投任务。/clear 走
// EnsureSession + MarkPendingFreshWithDedup（空正文）或 AppendUserMessage
// (ForceFresh)（带正文——先把这条消息补进去再清）。两者共用「route changed 就
// 重试」的循环（maxSlackControlRouteRetries 封顶），并用 deduper + Socket Mode
// 信封 id 做防重。任务投递成功后才广播会话生命周期事件与收尾（FinalizeChatTaskEnqueue）。
package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/slack-go/slack"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// slashControlTasks 是会话控制里投递/收尾聊天任务所需的 *service.TaskService
// 窄接口。
type slashControlTasks interface {
	PrepareChatTaskEnqueue(ctx context.Context, agentID, initiator pgtype.UUID) (service.PreparedChatTaskEnqueue, error)
	EnqueuePreparedChannelChatTaskInTx(ctx context.Context, tx pgx.Tx, session db.ChatSession, initiator pgtype.UUID, forceFresh bool, contextRevision int64, prepared service.PreparedChatTaskEnqueue) (db.AgentTaskQueue, error)
	FinalizeChatTaskEnqueue(ctx context.Context, task db.AgentTaskQueue)
}

// slackDMControlStarter 是 /new 与 /clear 背后真正的执行器（实现
// slashControlStarter 接口）。它持有引擎的 ChatSession 与会话生命周期事件
// 广播面。
type slackDMControlStarter struct {
	q         *db.Queries
	session   *engine.ChatSession
	tasks     slashControlTasks
	lifecycle engine.ChannelChatLifecycle
}

// maxSlackControlRouteRetries 封顶 /new、/clear 遇到「路由变了」时的重试次数。
// 路由修订每轮会推进，正常情况下一次或两次就稳定；这个上限防止极端情况下无限
// 空转。
const maxSlackControlRouteRetries = 8

// NewSlackDMControlStarter 组装控制执行器：用 TypeSlack 与空标题规则建一个引擎
// ChatSession，供 /new、/clear 共用。
func NewSlackDMControlStarter(q *db.Queries, tx engine.TxStarter, tasks slashControlTasks, lifecycle engine.ChannelChatLifecycle) slashControlStarter {
	return &slackDMControlStarter{
		q: q, tasks: tasks, lifecycle: lifecycle,
		session: engine.NewChatSession(q, tx, TypeSlack, engine.SessionTitles{}),
	}
}

// StartSlackDMChat 处理 `/new`：在 Slack 私聊里开一个新会话（可选带首条消息），
// 全程持有信封去重，成功后才广播会话已启动、并触发标题生成。
func (s *slackDMControlStarter) StartSlackDMChat(ctx context.Context, inst engine.ResolvedInstallation, userID pgtype.UUID, cmd slack.SlashCommand, envelopeID string) error {
	if envelopeID == "" {
		return errors.New("slack /new: missing socket envelope id")
	}
	d := &deduper{q: s.q}
	claim, err := d.Claim(ctx, inst.ID, envelopeID)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = d.Release(context.WithoutCancel(ctx), inst.ID, envelopeID, claim)
		}
	}()

	body := strings.TrimSpace(cmd.Text)
	var prepared service.PreparedChatTaskEnqueue
	if body != "" {
		prepared, err = s.tasks.PrepareChatTaskEnqueue(ctx, inst.AgentID, userID)
		if err != nil {
			return fmt.Errorf("slack /new: prepare task: %w", err)
		}
	}
	var task db.AgentTaskQueue
	var started engine.StartSessionResult
	// 会话路由可能在解析之后被别的入口重绑（route revision 推进）；StartSession
	// 这时返回 ErrRouteChanged，重新来一轮即可，最多试 maxSlackControlRouteRetries 次。
	for attempt := 0; ; attempt++ {
		task = db.AgentTaskQueue{}
		started, err = s.session.StartSession(ctx, engine.StartSessionInput{
			EnsureSessionInput: engine.EnsureSessionInput{
				WorkspaceID: inst.WorkspaceID, AgentID: inst.AgentID, InstallationID: inst.ID,
				Sender: userID, BindingKey: cmd.ChannelID, ChatType: channel.ChatTypeP2P,
			},
			Initiator: userID,
			Body:      body, DedupMessageID: envelopeID, ClaimToken: claim,
			PersistMessage: body != "", HistoryBoundaryPending: true,
			BeforeCommit: func(ctx context.Context, tx pgx.Tx, session db.ChatSession) error {
				if body == "" {
					return nil
				}
				var enqueueErr error
				task, enqueueErr = s.tasks.EnqueuePreparedChannelChatTaskInTx(ctx, tx, session, userID, false, 1, prepared)
				return enqueueErr
			},
		})
		if !errors.Is(err, engine.ErrRouteChanged) {
			break
		}
		slog.InfoContext(ctx, "slack /new route changed; retrying",
			"outcome", "chat_route_retry", "channel_type", string(TypeSlack),
			"installation_id", inst.ID, "attempt", attempt+1,
		)
		if attempt+1 >= maxSlackControlRouteRetries {
			return fmt.Errorf("slack /new route did not stabilize after %d retries: %w", maxSlackControlRouteRetries, err)
		}
	}
	if err != nil {
		return err
	}
	committed = true
	if s.lifecycle != nil {
		s.lifecycle.ChannelChatStarted(engine.ChannelChatStartedEvent{
			WorkspaceID: inst.WorkspaceID, CreatorID: userID, AgentID: inst.AgentID,
			SessionID: started.SessionID, InstallationID: inst.ID,
			ChannelType: TypeSlack, RouteRevision: started.RouteRevision,
			Title: started.Append.InitialTitle,
		})
		if started.Append.InitialTitle != "" {
			s.lifecycle.GenerateChannelChatTitle(inst.WorkspaceID, userID, started.SessionID, started.Append.InitialTitle, body)
		}
	}
	if body == "" {
		return nil
	}
	s.tasks.FinalizeChatTaskEnqueue(ctx, task)
	return nil
}

// ClearSlackDMContext 处理 `/clear`：清空该 Slack 私聊会话里的 agent 上下文。
// 空正文只做 pending/fresh 标记；带正文则先把这句「清空」请求本身作为新上下文的
// 首条消息追加进去再清。同样带信封去重与路由重试，任务成功投递后才收尾。
func (s *slackDMControlStarter) ClearSlackDMContext(ctx context.Context, inst engine.ResolvedInstallation, userID pgtype.UUID, cmd slack.SlashCommand, envelopeID string) error {
	if envelopeID == "" {
		return errors.New("slack /clear: missing socket envelope id")
	}
	d := &deduper{q: s.q}
	claim, err := d.Claim(ctx, inst.ID, envelopeID)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = d.Release(context.WithoutCancel(ctx), inst.ID, envelopeID, claim)
		}
	}()

	body := strings.TrimSpace(cmd.Text)
	var prepared service.PreparedChatTaskEnqueue
	if body != "" {
		prepared, err = s.tasks.PrepareChatTaskEnqueue(ctx, inst.AgentID, userID)
		if err != nil {
			return fmt.Errorf("slack /clear: prepare task: %w", err)
		}
	}

	var task db.AgentTaskQueue
	for attempt := 0; ; attempt++ {
		sessionID, ensureErr := s.session.EnsureSession(ctx, engine.EnsureSessionInput{
			WorkspaceID: inst.WorkspaceID, AgentID: inst.AgentID, InstallationID: inst.ID,
			Sender: userID, BindingKey: cmd.ChannelID, ChatType: channel.ChatTypeP2P,
		})
		if ensureErr != nil {
			return ensureErr
		}

		task = db.AgentTaskQueue{}
		if body == "" {
			err = s.session.MarkPendingFreshWithDedup(ctx, sessionID, "", inst.ID, envelopeID, claim)
		} else {
			_, err = s.session.AppendUserMessage(ctx, engine.AppendInput{
				SessionID: sessionID, Sender: userID, InstallationID: inst.ID,
				Body: body, CommandText: clearSlashCommand,
				DedupMessageID: envelopeID, ClaimToken: claim, ForceFresh: true,
				BeforeCommit: func(ctx context.Context, tx pgx.Tx, session db.ChatSession, contextRevision int64, _ pgtype.UUID, _ int64) error {
					var enqueueErr error
					task, enqueueErr = s.tasks.EnqueuePreparedChannelChatTaskInTx(
						ctx, tx, session, userID, true, contextRevision, prepared,
					)
					return enqueueErr
				},
			})
		}
		if !errors.Is(err, engine.ErrRouteChanged) {
			break
		}
		slog.InfoContext(ctx, "slack /clear route changed; retrying",
			"outcome", "context_clear_route_retry", "channel_type", string(TypeSlack),
			"installation_id", inst.ID, "attempt", attempt+1,
		)
		if attempt+1 >= maxSlackControlRouteRetries {
			return fmt.Errorf("slack /clear route did not stabilize after %d retries: %w", maxSlackControlRouteRetries, err)
		}
	}
	if err != nil {
		return err
	}
	committed = true
	if task.ID.Valid {
		s.tasks.FinalizeChatTaskEnqueue(ctx, task)
	}
	return nil
}
