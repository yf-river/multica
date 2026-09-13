// durable_event.go —— 服务层的“持久化事件”边界：
// 把“描述一次已提交业务变更”的事件在调用方的事务里写入 outbox（重放日志），提交后
// 再经事件总线广播。集中了任务生命周期（queued/dispatch/running/终态等）、agent 状态
// 等事件的落库 + 幂等键/流键约定，并负责从任务行反查它所属工作区。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// recordDurableEventTx 是“描述一次已提交领域变更的事件”在服务层唯一的写入入口。
// 调用方自带事务查询句柄，因此事件信封写不进去，就回滚掉对应的业务变更——绝不会
// 留下一个永远无法重放的状态。
func recordDurableEventTx(ctx context.Context, queries *db.Queries, event events.Event) (events.Event, error) {
	return eventoutbox.Enqueue(ctx, queries, event)
}

// RecordDurableEventTx 在调用方的事务里记录一个事件。当“变更”和“它的通知”分属
// 不同包时，handler 用这个小边界；显式传递查询句柄，能防止事件意外地在业务写入
// 提交之后才被提交。
func RecordDurableEventTx(ctx context.Context, queries *db.Queries, event events.Event) (events.Event, error) {
	return recordDurableEventTx(ctx, queries, event)
}

// publishCommittedEvent 把一条已提交（已落库）的事件发到总线。bus 或 event.ID 为空
// 时跳过——空 ID 说明事件从未持久化，不该广播。
func publishCommittedEvent(bus *events.Bus, event events.Event) {
	if bus != nil && event.ID != "" {
		bus.PublishRecovered(event)
	}
}

// publishPersistedTaskEvent 从库中取出“某个任务某类事件”那条已持久化记录（按幂等
// 键定位），校验类型一致后广播。供“事务已提交、但想补发那条落库事件”的路径使用。
func publishPersistedTaskEvent(ctx context.Context, queries *db.Queries, bus *events.Bus, eventType string, task db.AgentTaskQueue) error {
	if queries == nil {
		return errors.New("persisted task event: queries are required")
	}
	row, err := queries.GetDomainEventByIdempotencyKey(ctx, "task:"+eventType+":"+util.UUIDToString(task.ID))
	if err != nil {
		return fmt.Errorf("load persisted %s event: %w", eventType, err)
	}
	event := eventoutbox.EventFromRow(row)
	if event.Type != eventType {
		return fmt.Errorf("persisted task event type %q does not match %q", event.Type, eventType)
	}
	publishCommittedEvent(bus, event)
	return nil
}

// PublishCommittedEvent 供参与更大事务（例如 Life 认知输出 + 任务完成）的 handler
// 使用。调用方必须只在那个事务提交之后调用它。
func (s *TaskService) PublishCommittedEvent(event events.Event) {
	if s != nil {
		publishCommittedEvent(s.Bus, event)
	}
}

// recordTaskLifecycleEventTx 在调用方还握着“改了任务行的事务”时，记录一个任务状态
// 事实。工作区在同一事务里解析，因此并发删除不会产出一个指向另一快照的事件。
func recordTaskLifecycleEventTx(ctx context.Context, queries *db.Queries, eventType string, task db.AgentTaskQueue, extra ...map[string]any) (events.Event, error) {
	workspaceID, err := resolveTaskWorkspaceWithQueries(ctx, queries, task)
	if err != nil {
		return events.Event{}, err
	}
	if workspaceID == "" {
		return events.Event{}, fmt.Errorf("task %s has no workspace", util.UUIDToString(task.ID))
	}
	event := taskEvent(eventType, workspaceID, task, extra...)
	event.IdempotencyKey = taskLifecycleIdempotencyKey(eventType, task, event)
	event.StreamKey = "task:" + util.UUIDToString(task.ID)
	return recordDurableEventTx(ctx, queries, event)
}

// taskLifecycleIdempotencyKey 决定任务生命周期事件的幂等键。终态任务事件保留稳定键
// ——恢复和回执代码要在事务返回后靠它找到已提交的终态事实；非终态迁移则在下面追加
// 各自的迁移时间戳，使后续的 dispatch/start/wait 周期能各自表达、而不至于互相覆盖。
func taskLifecycleIdempotencyKey(eventType string, task db.AgentTaskQueue, event events.Event) string {
	key := "task:" + eventType + ":" + util.UUIDToString(task.ID)
	if taskTerminalEventType(eventType) {
		return key
	}
	switch eventType {
	case protocol.EventTaskQueued, protocol.EventTaskDeferred:
		return key
	case protocol.EventTaskDispatch:
		if task.DispatchedAt.Valid {
			return key + ":" + task.DispatchedAt.Time.UTC().Format("20060102150405.999999999")
		}
	case protocol.EventTaskRunning:
		if task.StartedAt.Valid {
			return key + ":" + task.StartedAt.Time.UTC().Format("20060102150405.999999999")
		}
	case protocol.EventTaskWaitingLocalDirectory:
		if task.PrepareLeaseExpiresAt.Valid {
			return key + ":" + task.PrepareLeaseExpiresAt.Time.UTC().Format("20060102150405.999999999")
		}
	}
	// 残缺/遗留行可能没有迁移时间戳。用事件信封摘要仍能区分不同载荷，同时让重试的
	// 同一次迁移保持幂等。
	payload, _ := json.Marshal(event.Payload)
	digest := sha256.Sum256(payload)
	return key + ":" + fmt.Sprintf("%x", digest[:8])
}

// recordTaskTerminalEventTx 是 worker 和清理路径所用的终态拼写。它刻意与 running、
// waiting 迁移共用同一个信封构建器，保证每条持久化的任务生命周期事件都遵守同样的
// 身份与流顺序规则。
func recordTaskTerminalEventTx(ctx context.Context, queries *db.Queries, eventType string, task db.AgentTaskQueue, extra ...map[string]any) (events.Event, error) {
	return recordTaskLifecycleEventTx(ctx, queries, eventType, task, extra...)
}

// RecordTaskTerminalEventTx 是“在 TaskService 之外给任务收尾”的后台 worker 共用的
// 边界。任务行和它的持久化终态事件必须使用同一把事务作用域查询句柄。
func RecordTaskTerminalEventTx(ctx context.Context, queries *db.Queries, eventType string, task db.AgentTaskQueue, extra ...map[string]any) (events.Event, error) {
	return recordTaskTerminalEventTx(ctx, queries, eventType, task, extra...)
}

// RecordTaskLifecycleEventTx 记录一次非终态的任务迁移，如 daemon 派发、启动或等待
// 本地目录。导出给“在 TaskService 之外执行状态变更”的服务边界使用。
func RecordTaskLifecycleEventTx(ctx context.Context, queries *db.Queries, eventType string, task db.AgentTaskQueue, extra ...map[string]any) (events.Event, error) {
	return recordTaskLifecycleEventTx(ctx, queries, eventType, task, extra...)
}

// recordAgentStatusEventTx 把一次 agent 状态投影和产生它的那次行更新绑在一起。状态
// 本身是一个聚合事实，不带这个信封的实时广播会在重启后消失。
func recordAgentStatusEventTx(ctx context.Context, queries *db.Queries, agent db.Agent) (events.Event, error) {
	event := events.Event{
		Type:           protocol.EventAgentStatus,
		IdempotencyKey: "agent:status:" + util.UUIDToString(agent.ID) + ":" + agent.Status + ":" + agent.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "agent:" + util.UUIDToString(agent.ID),
		WorkspaceID:    util.UUIDToString(agent.WorkspaceID),
		ActorType:      "system",
		Payload:        map[string]any{"agent": agentToMap(agent)},
	}
	return recordDurableEventTx(ctx, queries, event)
}

// RecordTaskQueuedEventTx 在“创建任务的那个事务”里记录排队迁移。Life 认知 worker
// 这类后台生产者没有 TaskService 实例，但它们需要和交互式入队路径一样的持久化
// 生命周期契约。
func RecordTaskQueuedEventTx(ctx context.Context, queries *db.Queries, workspaceID string, task db.AgentTaskQueue) (events.Event, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return events.Event{}, errors.New("task queued event: workspace is required")
	}
	event := taskEvent(protocol.EventTaskQueued, workspaceID, task)
	event.IdempotencyKey = "task:queued:" + util.UUIDToString(task.ID)
	event.StreamKey = "task:" + util.UUIDToString(task.ID)
	return recordDurableEventTx(ctx, queries, event)
}

// resolveTaskWorkspaceWithQueries 在同一事务里反查任务所属工作区：依次看 issue、聊天
// 会话、autopilot 运行、quick-create 上下文，最后退回 agent 行。靠后几个兜底是为
// runtime 恢复/归档 agent 清理这类“不再挂 issue/chat/autopilot 链接”的任务准备的。
func resolveTaskWorkspaceWithQueries(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (string, error) {
	if queries == nil {
		return "", errors.New("task workspace: queries are required")
	}
	if task.IssueID.Valid {
		issue, err := queries.GetIssue(ctx, task.IssueID)
		if err == nil {
			return util.UUIDToString(issue.WorkspaceID), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("load task issue workspace: %w", err)
		}
	}
	if task.ChatSessionID.Valid {
		session, err := queries.GetChatSession(ctx, task.ChatSessionID)
		if err == nil {
			return util.UUIDToString(session.WorkspaceID), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("load task chat workspace: %w", err)
		}
	}
	if task.AutopilotRunID.Valid {
		run, err := queries.GetAutopilotRun(ctx, task.AutopilotRunID)
		if err == nil {
			autopilot, loadErr := queries.GetAutopilot(ctx, run.AutopilotID)
			if loadErr != nil {
				return "", fmt.Errorf("load task autopilot workspace: %w", loadErr)
			}
			return util.UUIDToString(autopilot.WorkspaceID), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("load task autopilot run workspace: %w", err)
		}
	}
	if quickCreate, ok := ParseQuickCreateContext(task); ok {
		if _, err := util.ParseUUID(quickCreate.WorkspaceID); err != nil {
			return "", fmt.Errorf("task quick-create workspace: %w", err)
		}
		return quickCreate.WorkspaceID, nil
	}
	// runtime 恢复或归档 agent 清理创建的任务，可能不再保留 issue/chat/autopilot
	// 链接。agent 行是剩余的那个权威工作区归属者，而且可以在记录终态事件前于同一
	// 事务里安全解析。
	agent, err := queries.GetAgent(ctx, task.AgentID)
	if err == nil {
		return util.UUIDToString(agent.WorkspaceID), nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return "", fmt.Errorf("load task agent workspace: %w", err)
}

// taskTerminalEventType 判断某事件类型是否是任务终态（completed/failed/cancelled）。
func taskTerminalEventType(eventType string) bool {
	switch eventType {
	case protocol.EventTaskCompleted, protocol.EventTaskFailed, protocol.EventTaskCancelled:
		return true
	default:
		return false
	}
}

// hasPersistedTaskTerminalEvent 检查某任务的某类终态事件是否已按稳定幂等键落库
// （作为“重放/去重”判断用）。
func hasPersistedTaskTerminalEvent(ctx context.Context, queries *db.Queries, eventType string, task db.AgentTaskQueue) bool {
	if queries == nil || !taskTerminalEventType(eventType) || !task.ID.Valid {
		return false
	}
	_, err := queries.GetDomainEventByIdempotencyKey(ctx, "task:"+eventType+":"+util.UUIDToString(task.ID))
	return err == nil
}
