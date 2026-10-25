// autopilot.go —— Autopilot（用户配置的自动化规则）的执行入口与生命周期同步。
// 职责：把一次触发（定时/手动/webhook/API）落成一条 autopilot_run 记录，再按
// execution_mode 分流——create_issue 创建 issue 并派 agent 任务、run_only 直接把
// agent 任务入队；同时负责幂等去重、崩溃恢复（webhook/plan 两条断点续跑路径）、
// 配额预留/结算、准入闸门（MUL-1899/MUL-4525）与 run 终态（completed/failed/skipped）
// 的同步回写。文件末尾是 issue 标题模板的插值/校验与指标上报小工具。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issueguard"
	"github.com/multica-ai/multica/server/internal/issueposition"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// TxStarter 抽象「开启数据库事务」的能力，由 pgxpool.Pool 实现。拆成接口是让上层能
// 决定是否走事务：生产注入 Pool 保证「建 run + 建 issue/task」的原子性；TxStarter 为
// nil（测试/降级路径）时，本文件相关函数会退化成直接操作 s.Queries 的非事务写法。
type TxStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// AutopilotService 是 Autopilot（自动化规则）从触发到派发、再到结果回写的核心服务。
//
// 数据流：触发入口（计划任务/手动/webhook/API）→ 幂等建 autopilot_run → 按
// execution_mode 派发（create_issue 走 issue 创建，run_only 直接入 agent 任务队列）→
// 订阅 issue/task 终态把 run 收尾；配额与指标在各关键节点落账。
//
// 注意 TxStarter 可为 nil：此时所有「先写 run 再写下游资源」的两段式操作退化为非事务
// 直连 s.Queries，代码里因此有大量 `if s.TxStarter != nil` 分叉。
type AutopilotService struct {
	Queries   *db.Queries
	TxStarter TxStarter
	Bus       *events.Bus
	TaskSvc   *TaskService
}

// DefaultAutopilotTriggerTimezone 是 Autopilot 触发的兜底时区：触发没有配置时区、
// 或配置的时区加载失败时，都用它来渲染触发相关时间文本。导出给调度器算下次运行时间
// 时用同一个默认值，保证两边口径一致。
const DefaultAutopilotTriggerTimezone = "UTC"

// autopilotRecentDuplicateWindow 是「近期重复」防抖窗口：该时长内同 autopilot 已创建过
// 同名 issue 就跳过本次，避免 webhook 重推/手动连点产生一堆内容相同的 issue。
const autopilotRecentDuplicateWindow = 60 * time.Second

// NewAutopilotService 组装 AutopilotService。tx 传 nil 即表示退化为非事务路径（测试等）。
func NewAutopilotService(q *db.Queries, tx TxStarter, bus *events.Bus, taskSvc *TaskService) *AutopilotService {
	return &AutopilotService{Queries: q, TxStarter: tx, Bus: bus, TaskSvc: taskSvc}
}

func (s *AutopilotService) createAutopilotRun(ctx context.Context, _ pgtype.UUID, _ string, _ string, params db.CreateAutopilotRunParams) (db.AutopilotRun, bool, error) {
	run, err := s.Queries.CreateAutopilotRun(ctx, params)
	return run, false, err
}

// autopilotRuleConfigSummary 是发布（publish）autopilot 时截取的「承担追责含义」的配置快照，
// 随每个 rule-version 落库存档供审计展示（MUL-4302 §7）。纯外观字段（标题/描述/issue 标题模板）
// 故意不纳入——改它们不转移责任。触发配置（cron/webhook/event_filters）在另一张表、不内联到这里；
// 改触发同样会触发重发布并记录操作成员与时间，只是本快照只承载 autopilot 行的核心配置。
type autopilotRuleConfigSummary struct {
	AssigneeType  string `json:"assignee_type"`
	AssigneeID    string `json:"assignee_id"`
	Status        string `json:"status"`
	ExecutionMode string `json:"execution_mode"`
}

// RecordAutopilotRuleVersion 为一次「实质性发布」（MUL-4302 §3.4）追加一条 rule-version 快照，
// 记录发布人与生效时的核心配置。被两类调用方共享：handler 的发布路径（create/update/触发编辑/
// 归档，跑在它们的事务里）和失败监控器的 system 自动暂停（另一个包）。q 由调用方传入，通常是
// 事务内的 *db.Queries。publishedByType 取 "member"（携带操作成员 id）或 "system"（id 无效，
// 例如自动暂停监控器）。
func RecordAutopilotRuleVersion(ctx context.Context, q *db.Queries, ap db.Autopilot, publishedByType string, publishedByID pgtype.UUID) error {
	summary, err := json.Marshal(autopilotRuleConfigSummary{
		AssigneeType:  ap.AssigneeType,
		AssigneeID:    util.UUIDToString(ap.AssigneeID),
		Status:        ap.Status,
		ExecutionMode: ap.ExecutionMode,
	})
	if err != nil {
		return fmt.Errorf("marshal rule version config summary: %w", err)
	}
	if _, err := q.CreateAutopilotRuleVersion(ctx, db.CreateAutopilotRuleVersionParams{
		AutopilotID:     ap.ID,
		WorkspaceID:     ap.WorkspaceID,
		PublishedByType: publishedByType,
		PublishedByID:   publishedByID,
		ConfigSummary:   summary,
	}); err != nil {
		return fmt.Errorf("create autopilot rule version: %w", err)
	}
	return nil
}

// DispatchAutopilot 是自动触发的核心执行入口（计划任务/webhook worker/API 等「没有真人
// 在环」的调用方）。
// 它创建一条 run，随后按 execution_mode 创建 issue 或把直接 agent 任务入队。
//
// 在排队 run_only 工作前会先对被指派的 agent runtime 做一次准入检查：runtime 不在线就记一条
// `skipped` 的 run（带 failure_reason）并直接返回、不排队。这就是 MUL-1899 的「触发时准入」
// 闸门——没有它，暂停的笔记本/离线 daemon 会让定时 autopilot 往 agent_task_queue 堆成千上万条
// 注定执行不了的任务。
//
// create_issue 模式不同：它的首要契约是留下可持久审计轨迹。只要 assignee 有 runtime、只是
// 暂时离线，dispatch 仍会创建 issue 与 issue task——工作可见，等 runtime 回来即可认领。
//
// assignee_type='squad' 时闸门打向 squad leader（MUL-2429 的 Path A：Autopilot-on-squad ≈
// Autopilot-on-leader），leader 仅离线时同样享受 create_issue 审计轨迹例外。
func (s *AutopilotService) DispatchAutopilot(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	source string,
	payload []byte,
) (*db.AutopilotRun, error) {
	// 本入口没有 member actor（计划任务/webhook/API，或未解析出 member 的手动触发），所以
	// 归属解析到 rule_owner。这些调用方不会向人展示单次 run 的原因码，故此处丢弃；也带不出
	// webhookDeliveryID——持久化 webhook 投递走 AdmitAutopilotWebhookDelivery，不走本入口。
	run, _, err := s.dispatchAutopilot(ctx, autopilot, triggerID, source, payload, pgtype.Timestamptz{}, pgtype.UUID{}, pgtype.UUID{}, source+":"+uuid.NewString())
	return run, err
}

// DispatchAutopilotManual 是成员手动「立即运行」autopilot 的入口。与计划/webhook/API 派发
// （无真人 → rule_owner）不同，手动触发是直接的人类行为：run 归属 direct_human 到 actorUserID，
// 它同时是 originator（授权上下文）和 accountable 人（MUL-4302 §4），两种 execution_mode 皆然。
// actorUserID 无效时行为与 DispatchAutopilot(source="manual") 完全一致。
func (s *AutopilotService) DispatchAutopilotManual(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	payload []byte,
	actorUserID pgtype.UUID,
) (*db.AutopilotRun, dispatch.ReasonCode, error) {
	return s.DispatchAutopilotManualWithKey(ctx, autopilot, triggerID, payload, actorUserID, uuid.NewString())
}

// DispatchAutopilotManualWithKey 保留调用方自带的请求键做幂等：同一 HTTP 请求被重试时，
// 不会二次占用配额或二次执行。
func (s *AutopilotService) DispatchAutopilotManualWithKey(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	payload []byte,
	actorUserID pgtype.UUID,
	idempotencyKey string,
) (*db.AutopilotRun, dispatch.ReasonCode, error) {
	// 手动路径是唯一会把单次 run 结果展示给真人的入口，因此要返回准入阶段决定的类型化原因码。
	// 手动路径不带 webhook 投递。
	key := "manual:" + util.UUIDToString(autopilot.ID) + ":" + idempotencyKey
	return s.dispatchAutopilot(ctx, autopilot, triggerID, "manual", payload, pgtype.Timestamptz{}, pgtype.UUID{}, actorUserID, key)
}

// AdmitAutopilotWebhookDelivery 为一次持久化 webhook 投递创建（或复用）幂等的 run，但暂不执行
// 其下游 issue/task 副作用。HTTP 入口同步调用它，好让公开 webhook 响应保住「200 accepted/skipped
// + run_id」的契约，而可恢复的派发仍由数据库 worker 负责。
func (s *AutopilotService) AdmitAutopilotWebhookDelivery(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	payload []byte,
	deliveryID pgtype.UUID,
) (*db.AutopilotRun, error) {
	if !deliveryID.Valid {
		return nil, fmt.Errorf("admit webhook delivery: delivery_id is required")
	}

	existing, err := s.Queries.GetAutopilotRunByWebhookDelivery(ctx, deliveryID)
	switch {
	case err == nil:
		return &existing, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("admit webhook delivery: lookup existing run: %w", err)
	}

	// Webhook 准入没有 member actor → automation principal（rule_owner）；单次 run 的原因码
	// 也不面向人展示，故此处丢弃。
	if reason, _, skip := s.shouldSkipDispatch(ctx, autopilot, pgtype.UUID{}); skip {
		run, err := s.recordSkippedRun(
			ctx,
			autopilot,
			triggerID,
			"webhook",
			payload,
			pgtype.Timestamptz{},
			deliveryID,
			reason,
		)
		if err != nil {
			return s.recoverConcurrentWebhookAdmission(
				ctx,
				deliveryID,
				fmt.Errorf("admit webhook delivery: create skipped run: %w", err),
			)
		}
		return run, nil
	}

	initialStatus := "issue_created"
	if autopilot.ExecutionMode == "run_only" {
		initialStatus = "running"
	}
	run, _, err := s.createAutopilotRun(ctx, autopilot.WorkspaceID, "webhook", "webhook:"+util.UUIDToString(deliveryID), db.CreateAutopilotRunParams{
		ID:                dbid.NewV7(),
		AutopilotID:       autopilot.ID,
		TriggerID:         triggerID,
		Source:            "webhook",
		Status:            initialStatus,
		TriggerPayload:    payload,
		SquadID:           autopilotSquadAttribution(autopilot),
		WebhookDeliveryID: deliveryID,
	})
	if err != nil {
		return s.recoverConcurrentWebhookAdmission(
			ctx,
			deliveryID,
			fmt.Errorf("admit webhook delivery: create run: %w", err),
		)
	}
	s.captureAutopilotRunStarted(autopilot, run, "webhook")
	return &run, nil
}

func (s *AutopilotService) recoverConcurrentWebhookAdmission(
	ctx context.Context,
	deliveryID pgtype.UUID,
	cause error,
) (*db.AutopilotRun, error) {
	// 另一个 server 副本可能在 ingress 落库之后、本进程做准入查询之前抢先认领了这条持久化投递。
	// (delivery/run) 唯一索引只让一方成功，输的一方直接复用赢家建好的 run。
	var pgErr *pgconn.PgError
	if !errors.As(cause, &pgErr) || pgErr.Code != "23505" {
		return nil, cause
	}
	existing, err := s.Queries.GetAutopilotRunByWebhookDelivery(ctx, deliveryID)
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("admit webhook delivery: reload concurrent run: %w", err)
	}
	return nil, cause
}

// DispatchAutopilotForWebhookDelivery 是持久化 webhook worker 的入口。webhook_delivery_id 会落
// 在 run 行上并被部分唯一索引保护，所以进程崩溃后重新认领排队中的投递会复用原 run，而不是
// 再造第二个 issue 或任务。
func (s *AutopilotService) DispatchAutopilotForWebhookDelivery(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	payload []byte,
	deliveryID pgtype.UUID,
) (*db.AutopilotRun, error) {
	run, err := s.AdmitAutopilotWebhookDelivery(ctx, autopilot, triggerID, payload, deliveryID)
	if err != nil {
		return nil, err
	}
	if isAutopilotRunComplete(*run) {
		if autopilot.ExecutionMode == "create_issue" && run.IssueID.Valid {
			// run 已「完成」但下游 task 可能因进程崩溃而缺失，补一次入队。
			if repairErr := s.ensureWebhookCreateIssueTask(ctx, autopilot, *run); repairErr != nil {
				return run, repairErr
			}
		}
		return run, nil
	}

	// run_only 任务可能已在进程死掉之前提交，但当时没来得及把 task_id 回填到 run 上。
	// 补上这条链接并把 daemon 唤醒；补不上才走下面的半成品续跑。
	if autopilot.ExecutionMode == "run_only" && !run.TaskID.Valid {
		repaired, found, repairErr := s.repairAutopilotRunTaskLink(ctx, *run)
		if repairErr != nil {
			return run, fmt.Errorf("dispatch for webhook delivery: %w", repairErr)
		}
		if found {
			return repaired, nil
		}
	}
	// webhook worker 派发没有 member actor、也不面向人展示原因码：actorUserID 无效、原因码丢弃。
	dispatched, _, err := s.dispatchAutopilotRun(ctx, autopilot, triggerID, "webhook", run, pgtype.UUID{})
	return dispatched, err
}

// ensureWebhookCreateIssueTask 修补 create_issue 的崩溃窗口：issue/run 事务已提交、但普通 task
// 入队事务尚未提交之间的断点。只要 issue 上已存在任何 task，就说明下游所有权已移交、无需处理；
// 否则按原派发完全相同的 assignee 路径重新入队一条。
func (s *AutopilotService) ensureWebhookCreateIssueTask(ctx context.Context, autopilot db.Autopilot, run db.AutopilotRun) error {
	tasks, err := s.Queries.ListTasksByIssue(ctx, run.IssueID)
	if err != nil {
		return fmt.Errorf("dispatch for webhook delivery: inspect issue tasks: %w", err)
	}
	if len(tasks) > 0 {
		return nil
	}
	issue, err := s.Queries.GetIssue(ctx, run.IssueID)
	if err != nil {
		return fmt.Errorf("dispatch for webhook delivery: load linked issue: %w", err)
	}
	if effective := issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, issue.Status); effective != "todo" && effective != "in_progress" {
		return nil
	}
	if autopilot.AssigneeType == "squad" {
		leader, _, err := s.resolveAutopilotLeader(ctx, autopilot)
		if err != nil {
			return fmt.Errorf("dispatch for webhook delivery: resolve squad leader: %w", err)
		}
		if _, err := s.TaskSvc.EnqueueTaskForSquadLeader(ctx, issue, leader.ID, autopilot.AssigneeID, pgtype.UUID{}); err != nil {
			return fmt.Errorf("dispatch for webhook delivery: repair squad task: %w", err)
		}
		return nil
	}
	if _, err := s.TaskSvc.EnqueueTaskForIssue(ctx, issue); err != nil {
		return fmt.Errorf("dispatch for webhook delivery: repair issue task: %w", err)
	}
	return nil
}

// repairAutopilotRunTaskLink 收口 run_only 的崩溃窗口：task 已创建提交、但 autopilot_run.task_id
// 没来得及回填。只要查到 task 就证明下游所有权已移交：还活跃的工作重新唤醒，已终态的工作走
// 正常 finalizer 重放，绝不重复派发。
func (s *AutopilotService) repairAutopilotRunTaskLink(ctx context.Context, run db.AutopilotRun) (*db.AutopilotRun, bool, error) {
	task, err := s.Queries.GetAutopilotTaskByRun(ctx, run.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("lookup linked task: %w", err)
	}
	updated, err := s.Queries.UpdateAutopilotRunRunning(ctx, db.UpdateAutopilotRunRunningParams{
		ID:     run.ID,
		TaskID: task.ID,
	})
	if err != nil {
		return nil, false, fmt.Errorf("repair task linkage: %w", err)
	}
	switch task.Status {
	case "completed", "failed", "cancelled":
		s.SyncRunFromTask(ctx, task)
		updated, err = s.Queries.GetAutopilotRun(ctx, run.ID)
		if err != nil {
			return nil, false, fmt.Errorf("reload terminal repaired run: %w", err)
		}
	default:
		s.TaskSvc.NotifyTaskEnqueued(ctx, task)
	}
	return &updated, true, nil
}

// DispatchAutopilotForPlan 是定时触发的入口——调用方已经知道本次发生（occurrence）的规范 UTC
// plan_time。plan_time 会落到 autopilot_run.planned_at，(trigger_id, planned_at) 部分唯一索引配合
// 本方法的幂等查询，保证「同一个计划 occurrence 不会产生两条成功的 run」，哪怕 sys_cron_executions
// 里的陈旧抢单（stale-steal）在前一次尝试之后再次进入本方法。
//
// 已存在 (trigger_id, planned_at) run 时的语义：
//
//   - 该 run 已 COMPLETE（终态，或 in-flight 但下游链接齐备——create_issue 看 issue_id、
//     run_only 看 task_id）：原样返回。调用方随后在 sys_cron_executions 记 SUCCESS，不会产生
//     重复的 issue/task。
//   - 该 run 处于 PARTIAL 状态（前一次尝试写了 run 行、建下游 issue/task 前崩溃）：标记为 FAILED
//     （带恢复原因）并清空其 planned_at，释放部分唯一索引槽位，然后按正常流程在同一个 plan_time
//     新建 run。没有这个分支的话，派发途中崩溃会让后续重试看到 in-flight run 就原样返回，调度器
//     于是把该 occurrence 记为 SUCCESS，而实际 issue/task 从未创建（#4443 review）。
//
// triggerID 与 plannedAt 都必须有效；传零值会静默关掉幂等保护。手动/webhook/api 调用方请用
// DispatchAutopilot。
func (s *AutopilotService) DispatchAutopilotForPlan(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	source string,
	payload []byte,
	plannedAt time.Time,
) (*db.AutopilotRun, error) {
	if !triggerID.Valid {
		return nil, fmt.Errorf("dispatch for plan: trigger_id is required")
	}
	if plannedAt.IsZero() {
		return nil, fmt.Errorf("dispatch for plan: planned_at is required")
	}
	plannedTS := pgtype.Timestamptz{Time: plannedAt.UTC(), Valid: true}

	// 快速路径：之前的尝试已经为这同一个 occurrence 建了 run。即便没有下面的查询，
	// 部分唯一索引 uq_autopilot_run_trigger_planned 也会拒掉重复 INSERT；先查一次是为了
	// 能在 run 完整时直接短路，并有机会在重试前先恢复半成品 run。
	existing, err := s.Queries.GetAutopilotRunByTriggerAndPlanned(ctx, db.GetAutopilotRunByTriggerAndPlannedParams{
		TriggerID: triggerID,
		PlannedAt: plannedTS,
	})
	switch {
	case err == nil && isAutopilotRunComplete(existing):
		// 之前的尝试已产出完整 run：直接还给它，让 handler 可以在 sys_cron_executions 记
		// SUCCESS，不重复任何下游副作用。
		return &existing, nil

	case err == nil:
		if autopilot.ExecutionMode == "run_only" && !existing.TaskID.Valid {
			repaired, found, repairErr := s.repairAutopilotRunTaskLink(ctx, existing)
			if repairErr != nil {
				return nil, fmt.Errorf("dispatch for plan: %w", repairErr)
			}
			if found {
				return repaired, nil
			}
		}
		// 崩溃尝试留下的半成品 run。标记为失败（带恢复原因）并释放其部分唯一索引槽位，
		// 好让下面的全新派发能插入新行。
		slog.Warn("autopilot dispatch for plan: recovering partial run",
			"run_id", util.UUIDToString(existing.ID),
			"trigger_id", util.UUIDToString(triggerID),
			"planned_at", plannedAt.UTC().Format(time.RFC3339),
			"status", existing.Status,
			"issue_set", existing.IssueID.Valid,
			"task_set", existing.TaskID.Valid,
		)
		recovered, err := s.recoverPartialAutopilotRun(ctx, existing)
		if err != nil {
			return nil, fmt.Errorf("dispatch for plan: recover partial run: %w", err)
		}
		if !recovered {
			return nil, fmt.Errorf("dispatch for plan: partial run changed concurrently; retry")
		}
		// 往下落到下方全新的派发。

	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("dispatch for plan: lookup existing run: %w", err)
	}

	// 计划派发没有 member actor → 归属到 rule_owner；单次 run 的原因码不面向人，故丢弃。
	// 计划路径也不带 webhook 投递。
	key := "schedule:" + util.UUIDToString(triggerID) + ":" + plannedAt.UTC().Format(time.RFC3339Nano)
	run, _, err := s.dispatchAutopilot(ctx, autopilot, triggerID, source, payload, plannedTS, pgtype.UUID{}, pgtype.UUID{}, key)
	return run, err
}

// isAutopilotRunComplete 判断一条已存在的 (trigger_id, planned_at) autopilot_run 是否能在
// 陈旧抢单重试时安全复用。
//
// 所谓「完整」指以下任一情况：
//
//   - run 处于终态（completed / failed / skipped）：下游无更多工作，调用方原样返回即可。
//
//   - run 处于 in-flight、但其下游副作用已可观测：
//
//   - issue_created 且 issue_id 有效——issue 已存在，此后由 issue 事件监听者负责建 task。
//
//   - running 且 task_id 有效——task 已入队，task 终止时由监听者收口 run。
//
// 其余情况——尤其是 issue_created/running 却带 NULL issue_id/task_id，以及短暂的 'pending'
// 状态——都是半成品 run：run 行先于下游资源创建，陈旧抢单的重试绝不能把它当完整处理
// （#4443 review）。
func isAutopilotRunComplete(run db.AutopilotRun) bool {
	switch run.Status {
	case "completed", "failed", "skipped":
		return true
	case "issue_created":
		return run.IssueID.Valid
	case "running":
		return run.TaskID.Valid
	default:
		return false
	}
}

// dispatchAutopilot 是两个公开 Dispatch 入口共享的核心。plannedAt 是定时触发该 occurrence 的
// 规范 UTC 时间；手动/webhook/api 派发传零值 pgtype.Timestamptz，得到的 autopilot_run 行
// planned_at 为 NULL。webhookDeliveryID 只由持久化 webhook worker 传入。
func (s *AutopilotService) dispatchAutopilot(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	source string,
	payload []byte,
	plannedAt pgtype.Timestamptz,
	webhookDeliveryID pgtype.UUID,
	actorUserID pgtype.UUID,
	idempotencyKey string,
) (*db.AutopilotRun, dispatch.ReasonCode, error) {
	if reason, code, skip := s.shouldSkipDispatch(ctx, autopilot, actorUserID); skip {
		run, err := s.recordSkippedRun(ctx, autopilot, triggerID, source, payload, plannedAt, webhookDeliveryID, reason)
		return run, code, err
	}

	// 按 execution_mode 决定 run 的初始状态。
	initialStatus := "issue_created"
	if autopilot.ExecutionMode == "run_only" {
		initialStatus = "running"
	}

	run, reused, err := s.createAutopilotRun(ctx, autopilot.WorkspaceID, source, idempotencyKey, db.CreateAutopilotRunParams{
		ID:                dbid.NewV7(),
		AutopilotID:       autopilot.ID,
		TriggerID:         triggerID,
		Source:            source,
		Status:            initialStatus,
		TriggerPayload:    payload,
		SquadID:           autopilotSquadAttribution(autopilot),
		PlannedAt:         plannedAt,
		WebhookDeliveryID: webhookDeliveryID,
	})
	if err != nil {
		return nil, dispatch.ReasonInternalError, fmt.Errorf("create run: %w", err)
	}
	if reused {
		return &run, dispatch.ReasonCode(run.ReasonCode.String), nil
	}
	s.captureAutopilotRunStarted(autopilot, run, source)
	return s.dispatchAutopilotRun(ctx, autopilot, triggerID, source, &run, actorUserID)
}

// dispatchAutopilotRun 对一条已持久化的 run 执行下游副作用。把「建 run」与「派发」拆开，
// 是让 webhook worker 在「run 创建成功、issue/task 创建前」崩溃后，能沿同一条幂等锚点续跑。
func (s *AutopilotService) dispatchAutopilotRun(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	source string,
	run *db.AutopilotRun,
	actorUserID pgtype.UUID,
) (*db.AutopilotRun, dispatch.ReasonCode, error) {
	switch autopilot.ExecutionMode {
	case "create_issue":
		triggerTimezone := s.resolveAutopilotTriggerTimezone(ctx, triggerID)
		if err := s.dispatchCreateIssue(ctx, autopilot, run, triggerTimezone, actorUserID); err != nil {
			if skipped, code := s.handleDispatchSkip(ctx, autopilot, run, err); skipped != nil {
				return skipped, code, nil
			}
			s.failRun(ctx, run.ID, err.Error())
			s.captureAutopilotRunFailed(autopilot, *run, source, err.Error())
			return run, dispatchFailReasonCode(err), fmt.Errorf("dispatch create_issue: %w", err)
		}
	case "run_only":
		if err := s.dispatchRunOnly(ctx, autopilot, run, actorUserID); err != nil {
			if skipped, code := s.handleDispatchSkip(ctx, autopilot, run, err); skipped != nil {
				return skipped, code, nil
			}
			s.failRun(ctx, run.ID, err.Error())
			s.captureAutopilotRunFailed(autopilot, *run, source, err.Error())
			return run, dispatchFailReasonCode(err), fmt.Errorf("dispatch run_only: %w", err)
		}
	default:
		s.failRun(ctx, run.ID, "unknown execution_mode: "+autopilot.ExecutionMode)
		s.captureAutopilotRunFailed(autopilot, *run, source, "unknown execution_mode: "+autopilot.ExecutionMode)
		return run, dispatch.ReasonInternalError, fmt.Errorf("unknown execution_mode: %s", autopilot.ExecutionMode)
	}

	// 更新 autopilot 上的 last_run_at，反映「这一轮确实评估了触发」。
	s.Queries.UpdateAutopilotLastRunAt(ctx, autopilot.ID)

	return run, "", nil
}

// dispatchFailReasonCode 给一路掉到 failRun 的派发错误分类原因码。只用类型化判断
// （绝不子串匹配）：fail-closed 的归属拒绝是 attribution_blocked，其余一律按未分类内部错误。
func dispatchFailReasonCode(err error) dispatch.ReasonCode {
	if errors.Is(err, ErrAttributionFailClosed) {
		return dispatch.ReasonAttributionBlocked
	}
	return dispatch.ReasonInternalError
}

// dispatchCreateIssue 创建 issue 并为 agent 入队一条 task。
//
// autopilot 被指派给 squad 时（MUL-2429 Path A），创建的 issue 继承 assignee_type='squad' +
// assignee_id=squad；既有的 issue 监听链（shouldEnqueueSquadLeaderOnAssign →
// enqueueSquadLeaderTask）随后把工作路由给 squad leader——与人手动把 issue 指派给该 squad 完全
// 一样。
//
// issue 的 creator 永远是实际干活的 agent（squad 取解析出的 leader，否则就是 assignee agent 本身），
// 这样 activity/@mention 会用正确的作者身份渲染。
func (s *AutopilotService) dispatchCreateIssue(ctx context.Context, ap db.Autopilot, run *db.AutopilotRun, triggerTimezone string, actorUserID pgtype.UUID) error {
	leader, _, err := s.resolveAutopilotLeader(ctx, ap)
	if err != nil {
		return fmt.Errorf("resolve leader: %w", err)
	}
	var createdEvent events.Event
	var templateSubs []db.AutopilotSubscriber
	var subscriberInboxEvents []events.Event

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := s.Queries.WithTx(tx)

	title := s.interpolateTemplate(ap, *run, triggerTimezone)
	description := s.buildIssueDescription(ap, *run, triggerTimezone)

	// 派发时刷新一次 autopilot 行，用当前的项目绑定，而不是调用方可能缓存的过期快照。
	currentAutopilot, err := qtx.GetAutopilotInWorkspace(ctx, db.GetAutopilotInWorkspaceParams{
		ID:          ap.ID,
		WorkspaceID: ap.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("refresh autopilot: %w", err)
	}
	projectID := currentAutopilot.ProjectID

	if duplicate, found, err := issueguard.LockAndFindRecentAutopilotDuplicate(
		ctx, qtx, ap.WorkspaceID, ap.ID, projectID, title, autopilotRecentDuplicateWindow,
	); err != nil {
		return fmt.Errorf("recent duplicate guard: %w", err)
	} else if found {
		return &errDispatchSkipped{reason: "recent duplicate autopilot issue: " + util.UUIDToString(duplicate.ID), code: dispatch.ReasonAlreadyActive}
	}

	issueNumber, err := qtx.IncrementIssueCounter(ctx, ap.WorkspaceID)
	newPosition, err := issueposition.NextTopPosition(ctx, tx, ap.WorkspaceID, "todo")
	if err != nil {
		return fmt.Errorf("get next issue position: %w", err)
	}

	issue, err := qtx.CreateIssueWithOrigin(ctx, db.CreateIssueWithOriginParams{
		ID:           dbid.NewV7(),
		WorkspaceID:  ap.WorkspaceID,
		Title:        title,
		Description:  description,
		Status:       "todo",
		Priority:     "not_important_not_urgent",
		AssigneeType: pgtype.Text{String: ap.AssigneeType, Valid: true},
		AssigneeID:   ap.AssigneeID,
		// autopilot 派发给的 agent 才是 issue 的 creator，而不是最初配置 autopilot 的人。
		// 后者单独用 origin_type=autopilot + origin_id 记录。squad 型 autopilot 的 creator
		// 是解析出的 leader——与 issue 监听器最终会入队的是同一个 agent。
		CreatorType:   "agent",
		CreatorID:     leader.ID,
		ParentIssueID: pgtype.UUID{},
		Position:      newPosition,
		StartDate:     pgtype.Date{},
		DueDate:       pgtype.Date{},
		Number:        issueNumber,
		ProjectID:     projectID,
		OriginType:    pgtype.Text{String: "autopilot", Valid: true},
		OriginID:      ap.ID,
	})
	if err != nil {
		return fmt.Errorf("create issue: %w", err)
	}

	// 把默认订阅者模板的展开放在与 issue 插入相同的事务里、且在 EventIssueCreated 触发之前——
	// 这样通知监听器在第一条事件上就能看到完整订阅者集合，而不是与那个本会补全模板的监听器赛跑。
	templateSubs, err = qtx.ListAutopilotSubscribers(ctx, ap.ID)
	if err != nil {
		return fmt.Errorf("list autopilot subscribers: %w", err)
	}
	for _, sub := range templateSubs {
		if _, err := qtx.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
			IssueID:  issue.ID,
			UserType: sub.UserType,
			UserID:   sub.UserID,
			Reason:   "autopilot",
		}); err != nil {
			return fmt.Errorf("add autopilot subscriber to issue: %w", err)
		}
		// autopilot 订阅者在 issue 创建时就已经知道了。与其等第二个监听器从 issue 事件反推，
		// 不如在同一个事务里把第一条 inbox 信号写掉。inbox 事件同样是持久的，所以「提交后、
		// 实时扇出前」的重启也不会丢通知。
		if sub.UserType != "member" {
			continue
		}
		details, _ := json.Marshal(map[string]string{
			"autopilot_id": util.UUIDToString(ap.ID),
			"reason":       "autopilot",
		})
		item, err := qtx.CreateProjectedInboxItem(ctx, db.CreateProjectedInboxItemParams{
			ID:            ProjectedInboxID("autopilot:subscriber-inbox:"+util.UUIDToString(run.ID), "member", util.UUIDToString(sub.UserID), "issue_subscribed", util.UUIDToString(issue.ID)),
			WorkspaceID:   ap.WorkspaceID,
			RecipientType: "member",
			RecipientID:   sub.UserID,
			Type:          "issue_subscribed",
			Severity:      "info",
			IssueID:       issue.ID,
			Title:         issue.Title,
			Body:          pgtype.Text{},
			ActorType:     pgtype.Text{String: "agent", Valid: true},
			ActorID:       leader.ID,
			Details:       details,
		})
		if err != nil {
			return fmt.Errorf("create autopilot subscriber inbox: %w", err)
		}
		inboxEvent := events.Event{
			Type:           protocol.EventInboxNew,
			IdempotencyKey: "autopilot:subscriber-inbox:" + util.UUIDToString(run.ID) + ":" + util.UUIDToString(sub.UserID),
			StreamKey:      "inbox:" + util.UUIDToString(ap.WorkspaceID) + ":" + util.UUIDToString(sub.UserID),
			WorkspaceID:    util.UUIDToString(ap.WorkspaceID),
			ActorType:      "agent",
			ActorID:        util.UUIDToString(leader.ID),
			Payload: map[string]any{
				"item": func() map[string]any {
					fields := InboxItemFields(item)
					fields["issue_status"] = issue.Status
					return fields
				}(),
			},
		}
		inboxEvent, err = recordDurableEventTx(ctx, qtx, inboxEvent)
		if err != nil {
			return fmt.Errorf("record autopilot subscriber inbox event: %w", err)
		}
		subscriberInboxEvents = append(subscriberInboxEvents, inboxEvent)
	}

	// 在 issue 插入的同一事务内把 run 链接到 issue。这样「近期重复」防护只统计完全可观测的
	// autopilot issue，也避免出现崩溃窗口——恢复时会看到一个孤儿 issue 却没有关联 run。
	updatedRun, err := qtx.UpdateAutopilotRunIssueCreated(ctx, db.UpdateAutopilotRunIssueCreatedParams{
		ID:      run.ID,
		IssueID: issue.ID,
	})
	if err != nil {
		return fmt.Errorf("link run to issue: %w", err)
	}
	*run = updatedRun
	workspace, err := qtx.GetWorkspace(ctx, ap.WorkspaceID)
	if err != nil {
		return fmt.Errorf("load workspace for issue event: %w", err)
	}
	createdEvent = events.Event{
		Type:           protocol.EventIssueCreated,
		IdempotencyKey: "issue:created:" + util.UUIDToString(issue.ID),
		StreamKey:      "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID:    util.UUIDToString(ap.WorkspaceID),
		ActorType:      "agent",
		ActorID:        util.UUIDToString(leader.ID),
		Payload: map[string]any{
			"issue": IssueToMapResolved(ctx, qtx, issue, workspace.IssuePrefix),
		},
	}
	createdEvent, err = recordDurableEventTx(ctx, qtx, createdEvent)
	if err != nil {
		return fmt.Errorf("record issue-created event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	// issue 事件随 issue 事务一起落库。实时监听器与持久投影在提交后才看得到它，所以重启
	// 不会丢失创建信号。
	publishCommittedEvent(s.Bus, createdEvent)
	for _, inboxEvent := range subscriberInboxEvents {
		publishCommittedEvent(s.Bus, inboxEvent)
	}
	s.captureIssueCreatedFromAutopilot(ap, run, issue, leader.ID)

	// 走既有流程入队 agent task。squad 型 autopilot 路由给触发时解析出的 leader（MUL-2429
	// Path A）；agent 型走标准 issue 路径。两条路最终都在 agent_task_queue 里以 agent_id = leader
	// 落地。MANUAL 触发（actorUserID 有效）是直接的人类行为：走带 actor 的入队入口，让归属解析为
	// 触发成员的 direct_human（originator == accountable == actor，MUL-4302 §4）。计划/webhook 派发
	// 没有 actor，走普通入口，autopilot 出处的 issue 归属到 rule_owner。*WithHandoff 变体即既有的
	// 带 actor 入队方法，这里 handoff 说明为空。
	if ap.AssigneeType == "squad" {
		// 闭合失败（fail-closed）的调用闸门：校验准入主体（手动点击者，否则为 creator——见
		// autopilotAdmitInvoke）是否仍被允许调用 leader。用于拦截早于「保存时闸门」的存量配置、
		// 以及已不再满足闸门的配置（MUL-3963 / MUL-4525）。
		if !s.autopilotAdmitInvoke(ctx, ap, leader, actorUserID) {
			return fmt.Errorf("not allowed to invoke private squad leader")
		}
		if actorUserID.Valid {
			if _, err := s.TaskSvc.EnqueueTaskForSquadLeaderWithHandoff(ctx, issue, leader.ID, ap.AssigneeID, "", actorUserID); err != nil {
				return fmt.Errorf("enqueue squad leader task: %w", err)
			}
		} else if _, err := s.TaskSvc.EnqueueTaskForSquadLeader(ctx, issue, leader.ID, ap.AssigneeID, pgtype.UUID{}); err != nil {
			return fmt.Errorf("enqueue squad leader task: %w", err)
		}
	} else if actorUserID.Valid {
		if _, err := s.TaskSvc.EnqueueTaskForIssueWithHandoff(ctx, issue, "", actorUserID); err != nil {
			return fmt.Errorf("enqueue task for issue: %w", err)
		}
	} else if _, err := s.TaskSvc.EnqueueTaskForIssue(ctx, issue); err != nil {
		return fmt.Errorf("enqueue task for issue: %w", err)
	}

	slog.Info("autopilot dispatched (create_issue)",
		"autopilot_id", util.UUIDToString(ap.ID),
		"assignee_type", ap.AssigneeType,
		"issue_id", util.UUIDToString(issue.ID),
		"leader_id", util.UUIDToString(leader.ID),
		"run_id", util.UUIDToString(run.ID),
	)
	return nil
}

// errDispatchSkipped 包装一种「准入闸门已过、派发途中又冒出」的就绪失败。dispatchRunOnly 在
// 「解析出的 leader 于准入与建 task 之间离线/被归档」时返回它；DispatchAutopilot 识别后记一条
// `skipped` 的 run（带包装的原因），而不是 `failed`。
//
// 没有这个哨兵类型，既有的 failRun 路径会把这类竞态记成失败，并从手动触发 handler 冒一个 500——
// 既不对（工作根本没尝试、谁也没犯错）又太吵（失败监控器会把唯一过错是「runtime 不稳」的
// autopilot 自动暂停）。
type errDispatchSkipped struct {
	reason string
	// code 是本分支决定的、类型化且稳定的准入原因码，一路带到响应里（MUL-4525）——
	// 绝不从上方的可读 reason 字符串反推。
	code dispatch.ReasonCode
}

func (e *errDispatchSkipped) Error() string { return e.reason }

// dispatchRunOnly 不建 issue，直接把 agent 任务入队。
//
// squad 型 autopilot 的实际执行 agent 是触发时解析出的 squad leader（MUL-2429 Path A）。上游准入
// 检查（shouldSkipDispatch）用到的「归档 / 未绑 runtime / runtime 离线」闸门在这里再跑一遍兜底：
// leader 在准入与派发之间变了、或 runtime 恰好在这段空隙离线，仍然 fail-closed，不给一条注定
// 白跑的任务放行。
func (s *AutopilotService) dispatchRunOnly(ctx context.Context, ap db.Autopilot, run *db.AutopilotRun, actorUserID pgtype.UUID) error {
	agent, _, err := s.resolveAutopilotLeader(ctx, ap)
	if err != nil {
		// 与 shouldSkipDispatch 相同的「准入 vs 失败」分类：行消失或 squad 在准入与派发之间被
		// 归档，属于 skip 而非失败。
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errSquadArchived) {
			return &errDispatchSkipped{reason: formatAdmissionReason(ap, "assignee no longer resolvable"), code: dispatch.ReasonTargetUnavailable}
		}
		return fmt.Errorf("resolve leader: %w", err)
	}
	verdict, err := AgentReadiness(ctx, s.runtimeLookup(), agent)
	if err != nil {
		return fmt.Errorf("check agent readiness: %w", err)
	}
	if !verdict.Ready() {
		return &errDispatchSkipped{reason: formatAdmissionReason(ap, verdict.Detail), code: verdict.Reason}
	}

	// squad 型 autopilot 的闭合失败调用闸门（准入主体 = 手动点击者，否则 creator——见
	// autopilotAdmitInvoke）。
	if ap.AssigneeType == "squad" && !s.autopilotAdmitInvoke(ctx, ap, agent, actorUserID) {
		return &errDispatchSkipped{reason: formatAdmissionReason(ap, "not allowed to invoke private squad leader"), code: dispatch.ReasonInvocationNotAllowed}
	}

	// 归属按触发方式分叉。MANUAL 触发是直接的人类行为：触发成员记为 direct_human，同时是
	// originator（run 携带其授权上下文）与 accountable 人（MUL-4302 §4）。计划/webhook 触发没有人——
	// originator_user_id 保持 NULL，审计上 accountable 的人是对当前触发的生效配置负有责任的成员
	// （其 creator，其次最近实质性编辑它的人）——即 trigger_owner，从 run.TriggerID 解析
	// （MUL-4302; Elon must-fix）；解析不到就降级到 rule version 的发布者（rule_owner），再降级到
	// 无归属。无论哪条路，证据都指向 autopilot run，绝不产生 NULL-source 绕过。
	var autopilotAttr attribution.Result
	if actorUserID.Valid {
		autopilotAttr = attribution.DirectHumanRun(actorUserID, attribution.EvidenceAutopilotRun, run.ID)
	} else {
		autopilotAttr = triggerOwnerAttribution(ctx, s.Queries, run.TriggerID, ap.WorkspaceID, ap.ID, attribution.EvidenceAutopilotRun, run.ID)
	}
	// 没有解析出精确的人（无 rule version 的 autopilot）时降级到 owner_fallback（accountable =
	// agent owner）；工作区 fail-closed 则跳过派发（MUL-4302 §3.5）。
	autopilotAttr, err = s.TaskSvc.applyAttributionFallback(ctx, autopilotAttr, agent)
	if err != nil {
		return &errDispatchSkipped{reason: formatAdmissionReason(ap, "workspace fail-closed: no accountable human for autopilot run"), code: dispatch.ReasonAttributionBlocked}
	}
	apSource, _, apEvidenceKind, apEvidenceRef := attributionCreateParams(autopilotAttr)
	createTask := db.CreateAutopilotTaskParams{
		ID:             dbid.NewV7(),
		AgentID:        agent.ID,
		RuntimeID:      agent.RuntimeID,
		Priority:       0,
		AutopilotRunID: run.ID,
		// 快照 autopilot 标题，让 task 行无需再回连 autopilot 就能自解释；截断原因与
		// comment 驱动的摘要相同（节省传输成本）。
		TriggerSummary: pgtype.Text{
			String: truncateForSummary(ap.Title, triggerSummaryMaxLen),
			Valid:  ap.Title != "",
		},
		OriginatorUserID:     autopilotAttr.UserID,
		AccountableUserID:    autopilotAttr.AccountableUserID,
		RuleVersionID:        autopilotAttr.RuleVersionID,
		OriginatorSource:     apSource,
		TriggerEvidenceKind:  apEvidenceKind,
		TriggerEvidenceRefID: apEvidenceRef,
	}
	var task db.AgentTaskQueue
	var queuedEvent events.Event
	if s.TxStarter == nil {
		task, err = s.Queries.CreateAutopilotTask(ctx, createTask)
		if err != nil {
			return fmt.Errorf("create autopilot task: %w", err)
		}
		updatedRun, updateErr := s.Queries.UpdateAutopilotRunRunning(ctx, db.UpdateAutopilotRunRunningParams{ID: run.ID, TaskID: task.ID})
		if updateErr != nil {
			slog.Warn("failed to update run with task_id", "run_id", util.UUIDToString(run.ID), "error", updateErr)
		} else {
			*run = updatedRun
		}
	} else {
		tx, txErr := s.TxStarter.Begin(ctx)
		if txErr != nil {
			return fmt.Errorf("begin run-only dispatch: %w", txErr)
		}
		defer tx.Rollback(ctx)
		qtx := s.Queries.WithTx(tx)
		task, err = qtx.CreateAutopilotTask(ctx, createTask)
		if err != nil {
			return fmt.Errorf("create autopilot task: %w", err)
		}
		updatedRun, updateErr := qtx.UpdateAutopilotRunRunning(ctx, db.UpdateAutopilotRunRunningParams{ID: run.ID, TaskID: task.ID})
		if updateErr != nil {
			return fmt.Errorf("link run to autopilot task: %w", updateErr)
		}
		*run = updatedRun
		queuedEvent = taskEvent(protocol.EventTaskQueued, util.UUIDToString(ap.WorkspaceID), task)
		queuedEvent.IdempotencyKey = "task:queued:" + util.UUIDToString(task.ID)
		queuedEvent.StreamKey = "task:" + util.UUIDToString(task.ID)
		queuedEvent, err = recordDurableEventTx(ctx, qtx, queuedEvent)
		if err != nil {
			return fmt.Errorf("record autopilot task queued event: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit run-only dispatch: %w", err)
		}
	}

	// 丢掉空认领缓存并唤醒 daemon。dispatchRunOnly 是通过 Queries.CreateAutopilotTask 直接插的
	// task 行（绕过了 TaskService.Enqueue*），所以少了这一步的话 runtime 收不到唤醒，任何缓存的
	// "empty" 结论都会把任务按到 TTL 过期。
	if queuedEvent.ID != "" {
		publishCommittedEvent(s.Bus, queuedEvent)
	}
	s.TaskSvc.NotifyTaskEnqueued(ctx, task)

	slog.Info("autopilot dispatched (run_only)",
		"autopilot_id", util.UUIDToString(ap.ID),
		"task_id", util.UUIDToString(task.ID),
		"run_id", util.UUIDToString(run.ID),
	)
	return nil
}

// SyncRunFromIssue 在关联 issue 到达终态时同步 autopilot run 的状态。
func (s *AutopilotService) SyncRunFromIssue(ctx context.Context, issue db.Issue) {
	if !issue.OriginType.Valid || issue.OriginType.String != "autopilot" {
		return
	}

	run, err := s.Queries.GetAutopilotRunByIssue(ctx, issue.ID)
	if err != nil {
		return // 该 issue 没有关联的活跃 run
	}
	autopilot, err := s.Queries.GetAutopilot(ctx, run.AutopilotID)
	if err != nil {
		return
	}

	wsID := util.UUIDToString(issue.WorkspaceID)

	// 自定义状态终结 run 的方式与它所继承的规范状态一致。内置状态键无需查询即可解析为自己，
	// 因此任何没定义自定义状态的工作区这里都是空操作。下面的失败原因有意保留 issue.Status 原始值
	// 而非归一化后的 key，让审计轨迹点名的是人真正选择的状态（MUL-6243）。
	effectiveStatus := issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, issue.Status)

	switch effectiveStatus {
	case "done", "in_review":
		var updatedRun db.AutopilotRun
		if s.TxStarter != nil {
			updatedRun, _, err = s.settleAutopilotRunDurably(ctx, run.ID, "completed", nil, "", "", false)
		} else {
			updatedRun, err = s.completeAutopilotRun(ctx, db.UpdateAutopilotRunCompletedParams{ID: run.ID})
		}
		if err != nil {
			slog.Warn("failed to complete autopilot run", "run_id", util.UUIDToString(run.ID), "error", err)
			return
		}
		s.captureAutopilotRunCompleted(autopilot, updatedRun)
		if s.TxStarter == nil {
			s.publishRunDone(wsID, updatedRun, "completed")
		}
	case "cancelled", "blocked":
		reason := "issue " + issue.Status
		var updatedRun db.AutopilotRun
		if s.TxStarter != nil {
			updatedRun, _, err = s.settleAutopilotRunDurably(ctx, run.ID, "failed", nil, reason, "", false)
		} else {
			updatedRun, err = s.failAutopilotRun(ctx, db.UpdateAutopilotRunFailedParams{
				ID:            run.ID,
				FailureReason: pgtype.Text{String: reason, Valid: true},
			})
		}
		if err != nil {
			slog.Warn("failed to fail autopilot run", "run_id", util.UUIDToString(run.ID), "error", err)
			return
		}
		s.captureAutopilotRunFailed(autopilot, updatedRun, updatedRun.Source, reason)
		if s.TxStarter == nil {
			s.publishRunDone(wsID, updatedRun, "failed")
		}
	}
}

// SyncRunFromTask 在 run_only 任务完成/失败时同步 autopilot run。
func (s *AutopilotService) SyncRunFromTask(ctx context.Context, task db.AgentTaskQueue) {
	if !task.AutopilotRunID.Valid {
		return
	}

	run, err := s.Queries.GetAutopilotRun(ctx, task.AutopilotRunID)
	if err != nil {
		return
	}

	autopilot, err := s.Queries.GetAutopilot(ctx, run.AutopilotID)
	if err != nil {
		return
	}
	wsID := util.UUIDToString(autopilot.WorkspaceID)

	switch task.Status {
	case "completed":
		var updatedRun db.AutopilotRun
		if s.TxStarter != nil {
			updatedRun, _, err = s.settleAutopilotRunDurably(ctx, run.ID, "completed", task.Result, "", "", false)
		} else {
			updatedRun, err = s.completeAutopilotRun(ctx, db.UpdateAutopilotRunCompletedParams{
				ID:     run.ID,
				Result: task.Result,
			})
		}
		if err != nil {
			slog.Warn("failed to complete autopilot run from task", "run_id", util.UUIDToString(run.ID), "error", err)
			return
		}
		s.captureAutopilotRunCompleted(autopilot, updatedRun)
		if s.TxStarter == nil {
			s.publishRunDone(wsID, updatedRun, "completed")
		}
	case "failed", "cancelled":
		reason := taskFailureReasonForAutopilotRun(task)
		var updatedRun db.AutopilotRun
		if s.TxStarter != nil {
			updatedRun, _, err = s.settleAutopilotRunDurably(ctx, run.ID, "failed", nil, reason, "", false)
		} else {
			updatedRun, err = s.failAutopilotRun(ctx, db.UpdateAutopilotRunFailedParams{
				ID:            run.ID,
				FailureReason: pgtype.Text{String: reason, Valid: true},
			})
		}
		if err != nil {
			slog.Warn("failed to fail autopilot run from task", "run_id", util.UUIDToString(run.ID), "error", err)
			return
		}
		s.captureAutopilotRunFailed(autopilot, updatedRun, updatedRun.Source, reason)
		if s.TxStarter == nil {
			s.publishRunDone(wsID, updatedRun, "failed")
		}
	}
}

// SyncRunFromLinkedIssueTask 在 create_issue run 的关联 issue task 先于 issue 本体到达终态并失败时，
// 把 autopilot run 置为 failed。create_issue 的任务通过 issue_id 关联而非 autopilot_run_id，所以
// SyncRunFromTask 看不到它们。没有这个兜底，run 会永远挂在 `issue_created`；又因为失败率自动暂停
// 监控器不统计 issue_created/running 的 run，持续失败的 autopilot 也永远触发不了自动暂停。
//
// 所谓「终态」指该 issue 已没有仍活跃的任务。FailTask 对基础设施型失败（超时、runtime 离线/恢复、
// codex 无进展）会在广播失败事件之前先排一条自动重试，所以这里只要查到活跃任务，就意味着另一次
// 尝试已在路上——等它，而不是过早把 run 判死。重试耗尽（或失败本就不可重试）后，run 才带着该
// 任务的原因失败。
func (s *AutopilotService) SyncRunFromLinkedIssueTask(ctx context.Context, task db.AgentTaskQueue) {
	if task.AutopilotRunID.Valid || !task.IssueID.Valid || task.Status != "failed" {
		return
	}
	// 只有 create_issue 的 run 经 issue_id 关联（其关联 issue 按构造永远是 origin_type=autopilot），
	// 所以查到这里既识别出在飞的 create_issue run，又能一次查询就滤掉普通 issue/chat 任务失败的
	// 常见情况。
	run, err := s.Queries.GetAutopilotRunByIssue(ctx, task.IssueID)
	if err != nil {
		return // 该 issue 没有关联的活跃 run
	}
	// 仍有活跃任务——通常是 FailTask 刚排的自动重试——说明派发还没终态；等最后一次尝试。
	hasActive, err := s.Queries.HasActiveTaskForIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("failed to check active tasks for autopilot issue failure",
			"issue_id", util.UUIDToString(task.IssueID),
			"task_id", util.UUIDToString(task.ID),
			"error", err,
		)
		return
	}
	if hasActive {
		return
	}
	autopilot, err := s.Queries.GetAutopilot(ctx, run.AutopilotID)
	if err != nil {
		return
	}

	reason := taskFailureReasonForAutopilotRun(task)
	var updatedRun db.AutopilotRun
	if s.TxStarter != nil {
		updatedRun, _, err = s.settleAutopilotRunDurably(ctx, run.ID, "failed", nil, reason, "", false)
	} else {
		updatedRun, err = s.failAutopilotRun(ctx, db.UpdateAutopilotRunFailedParams{
			ID:            run.ID,
			FailureReason: pgtype.Text{String: reason, Valid: reason != ""},
		})
	}
	if err != nil {
		slog.Warn("failed to fail autopilot run from linked issue task",
			"run_id", util.UUIDToString(run.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"task_id", util.UUIDToString(task.ID),
			"error", err,
		)
		return
	}
	s.captureAutopilotRunFailed(autopilot, updatedRun, updatedRun.Source, reason)
	if s.TxStarter == nil {
		s.publishRunDone(util.UUIDToString(autopilot.WorkspaceID), updatedRun, "failed")
	}
}

// taskFailureReasonForAutopilotRun 提取要写进 autopilot_run.failure_reason 的失败原因：优先任务
// 错误原文，其次任务失败原因，最后给兜底文案。
func taskFailureReasonForAutopilotRun(task db.AgentTaskQueue) string {
	if task.Error.Valid && strings.TrimSpace(task.Error.String) != "" {
		return task.Error.String
	}
	if task.FailureReason.Valid && strings.TrimSpace(task.FailureReason.String) != "" {
		return task.FailureReason.String
	}
	return "task failed"
}

// handleDispatchSkip 识别派发函数返回的 errDispatchSkipped，并把在飞的 run 改写为 `skipped`
// （而不是 `failed`）。真 skip 时返回更新后的 run，否则返回 nil——调用方在 nil 时落入失败路径。
//
// 放在这里而不是 dispatchRunOnly 内部，是因为 run 行由栈上层的 DispatchAutopilot 创建，
// 「失败还是跳过」的裁定权归派发入口所有；这样 dispatchRunOnly 就不用掺状态改写逻辑。
func (s *AutopilotService) handleDispatchSkip(ctx context.Context, ap db.Autopilot, run *db.AutopilotRun, err error) (*db.AutopilotRun, dispatch.ReasonCode) {
	var skipErr *errDispatchSkipped
	if !errors.As(err, &skipErr) {
		return nil, ""
	}
	var updated db.AutopilotRun
	var uerr error
	if s.TxStarter != nil {
		updated, _, uerr = s.settleAutopilotRunDurably(ctx, run.ID, "skipped", nil, skipErr.reason, skipErr.code, true)
	} else {
		updated, uerr = s.skipAutopilotRun(ctx, db.UpdateAutopilotRunSkippedParams{
			ID:            run.ID,
			FailureReason: pgtype.Text{String: skipErr.reason, Valid: true},
			ReasonCode:    pgtype.Text{String: string(skipErr.code), Valid: skipErr.code != ""},
		})
	}
	if uerr != nil {
		slog.Warn("failed to mark dispatch as skipped",
			"run_id", util.UUIDToString(run.ID), "error", uerr)
		// 更新失败就保持 run 现状（running/issue_created）；失败监控器最终会把它判失败，
		// 至少我们没有假装它成功。
		return nil, ""
	}
	*run = updated
	slog.Info("autopilot dispatch skipped post-admission",
		"autopilot_id", util.UUIDToString(ap.ID),
		"run_id", util.UUIDToString(run.ID),
		"reason", skipErr.reason,
	)
	// 与 recordSkippedRun（飞前 skip）及成功路径对齐，同样 bump last_run_at：从调度器/UI 的
	// 角度看，这一轮确实评估了触发，只不过准入后闸门拦下了一次迟到的就绪恶化。
	if s.TxStarter == nil {
		s.Queries.UpdateAutopilotLastRunAt(ctx, ap.ID)
		s.publishRunDone(util.UUIDToString(ap.WorkspaceID), updated, "skipped")
	}
	return run, skipErr.code
}

// failRun 把 run 落成 failed（终态）。内部错误一律走 dispatch.ReasonInternalError；错误仅记日志，
// 不向调用方传播。
func (s *AutopilotService) failRun(ctx context.Context, runID pgtype.UUID, reason string) {
	if s.TxStarter != nil {
		if _, _, err := s.settleAutopilotRunDurably(ctx, runID, "failed", nil, reason, dispatch.ReasonInternalError, false); err != nil {
			slog.Warn("failed to mark autopilot run as failed", "run_id", util.UUIDToString(runID), "error", err)
		}
		return
	}
	if _, err := s.failAutopilotRun(ctx, db.UpdateAutopilotRunFailedParams{
		ID:            runID,
		FailureReason: pgtype.Text{String: reason, Valid: true},
		ReasonCode:    pgtype.Text{String: string(dispatch.ReasonInternalError), Valid: true},
	}); err != nil {
		slog.Warn("failed to mark autopilot run as failed", "run_id", util.UUIDToString(runID), "error", err)
	}
}

// shouldSkipDispatch 是 MUL-1899 的「触发前准入」检查。返回 (reason, true) 表示此刻派发只会排一条
// 注定白跑的任务——即 assignee（squad 型 autopilot 则为 squad leader）已被删/归档、没绑 runtime、
// 或 runtime 当前不在线。happy path 返回 ("", false)。
//
// 错误分两类：
//   - pgx.ErrNoRows / errSquadArchived（行真不存在或被归档）→ 硬 skip。重试不会有任何变化；
//     攒一堆失败 run 反而会污染失败率自动暂停监控器。
//   - 其余（断连、语句超时等）→ fail-open：记日志但不 skip，避免一次瞬时 DB 抖动就悄悄吞掉一条
//     定时 run。migration 096 移除了 autopilot 上的 agent 外键，所以 agent assignee 缺失现在是闸门
//     必须处理的真实情形（此前是被级联删除）。
func (s *AutopilotService) shouldSkipDispatch(ctx context.Context, ap db.Autopilot, actorUserID pgtype.UUID) (string, dispatch.ReasonCode, bool) {
	if !ap.AssigneeID.Valid {
		return "autopilot has no assignee", dispatch.ReasonTargetUnavailable, true
	}
	agent, squadResolved, err := s.resolveAutopilotLeader(ctx, ap)
	if err != nil {
		// 对「重试结果必然相同」的情形做硬 skip。日志无条件打，让运维仍能看到一串悬空行——
		// 指向已被删除 agent / 被归档 squad 的记录。
		missing := errors.Is(err, pgx.ErrNoRows)
		archived := errors.Is(err, errSquadArchived)
		slog.Warn("autopilot admission: failed to resolve leader",
			"autopilot_id", util.UUIDToString(ap.ID),
			"assignee_type", ap.AssigneeType,
			"assignee_id", util.UUIDToString(ap.AssigneeID),
			"missing", missing,
			"archived", archived,
			"error", err,
		)
		switch {
		case archived:
			// squad 行还在但已归档——DeleteSquad 的转移逻辑本应已把这个 autopilot 的 assignee
			// 改写成 leader；显式点名该情形，好让真漏过转移时的失败原因仍可读。
			return "assignee squad is archived", dispatch.ReasonTargetUnavailable, true
		case missing && squadResolved:
			return "assignee squad cannot be resolved", dispatch.ReasonTargetUnavailable, true
		case missing && !squadResolved:
			// agent 行没了。migration 096 之后外键也没了，这是新的「agent 在我们脚下被硬删」场景。
			// 跳过而非 fail-open：我们知道重试无济于事。
			return "assignee agent no longer exists", dispatch.ReasonTargetUnavailable, true
		}
		// 瞬时 DB 错误——fail-open，让下一个调度 tick 有机会成功。
		return "", "", false
	}
	verdict, err := AgentReadiness(ctx, s.runtimeLookup(), agent)
	if err != nil {
		slog.Warn("autopilot admission: failed to load runtime",
			"autopilot_id", util.UUIDToString(ap.ID),
			"runtime_id", util.UUIDToString(agent.RuntimeID),
			"error", err,
		)
		return "", "", false
	}
	if !verdict.Ready() {
		// 只是机器离线的话，create_issue 工作仍然照发：issue 在服务端写掉，run 等笔记本回来。
		// 不可用的 runtime 不在此列——在有人修复前那里什么都跑不起来，造一个注定失败的 issue
		// 不是改进。
		if ap.ExecutionMode == "create_issue" && verdict.Availability == AgentWaitable {
			slog.Info("autopilot admission: allowing create_issue dispatch for offline runtime",
				"autopilot_id", util.UUIDToString(ap.ID),
				"runtime_id", util.UUIDToString(agent.RuntimeID),
				"reason", verdict.Detail,
			)
		} else {
			return formatAdmissionReason(ap, verdict.Detail), verdict.Reason, true
		}
	}
	// autopilot 层的调用闸门（MUL-3963 / MUL-4525）。准入主体取决于触发方式：MANUAL「立即运行」
	// （actorUserID 有效）是直接的人类行为，按当前 CLICKER 的权限门控——而不是 autopilot creator 的——
	// 这样准入与归属记到同一个人身上、绝不分叉。自动化（计划/webhook/api，actorUserID 无效）没有
	// 人参与，退回 creator。管理员不会越过他们并不拥有的私有 agent；agent 创建的 autopilot 按工作区
	// 主体评判。squad 型 autopilot 的闸门打向解析出的 leader。
	if !s.autopilotAdmitInvoke(ctx, ap, agent, actorUserID) {
		if actorUserID.Valid {
			return "you are not allowed to trigger this autopilot's assignee agent", dispatch.ReasonInvocationNotAllowed, true
		}
		return "autopilot creator lacks access to private assignee agent", dispatch.ReasonInvocationNotAllowed, true
	}
	return "", "", false
}

// formatAdmissionReason 把 AgentReadiness 的通用原因改写成准入闸门措辞——失败监控器与既有告警
// 是按这套措辞调优的。前缀必须保持稳定：仪表盘按子串对 skip 原因分组（MUL-1899 告警的触发词是
// "offline at dispatch time"）。
//
// squad 型 autopilot 的文案会点名 squad，让只看 failure_reason 字段的运维不用回连
// autopilot_run.squad_id 就知道是谁家 leader 挂了。
func formatAdmissionReason(ap db.Autopilot, raw string) string {
	prefix := "assignee "
	if ap.AssigneeType == "squad" {
		prefix = "squad leader "
	}
	switch raw {
	case "agent is archived":
		return prefix + "agent is archived"
	case "agent has no runtime bound":
		return prefix + "agent has no runtime bound"
	default:
		// raw 形如 "agent runtime is X"——把 runtime 状态亮出来，同时保留 MUL-1899 留下的
		// 传统 "at dispatch time" 后缀，让既有告警查询无需改动。
		return raw + " at dispatch time"
	}
}

// errSquadArchived 表示 autopilot 的 squad assignee 已被归档。把它与「squad 找不到/能加载但加载
// 失败」区分开，准入闸门才能给出精确的 skip 措辞，失败监控器也不会把「无法解析」当噪音——这
// 是一个已知、符合预期的归档后情形。
var errSquadArchived = errors.New("squad is archived")

// resolveAutopilotLeader 返回真正会执行 autopilot 工作的 agent。assignee_type='agent' 时就是
// assignee 本人；assignee_type='squad' 时是 squad 的 leader_id。第二个返回值在走了 squad 分支时为
// true——调用方借此区分「加载 agent 失败」与「加载 squad 失败」，准入闸门再据此在 fail-open
// （已知好 agent 上的瞬时 DB 错误）与 fail-closed（squad 行没了，重试无意义）之间选择。
//
// 被归档的 squad 在这里一并拒绝：TransferSquadAutopilotsToLeader 会在 DeleteSquad 时把存活的
// autopilot 翻成 assignee_type='agent'，但闸门仍要对任何漏过转移的行 fail-closed（例如 squad 经由
// 绕过 handler 的路径被归档），归档 squad 绝不能产出工作。
//
// 未知的 assignee_type 值返回错误。assignee_type 在 DB 层有 CHECK 约束兜底，所以这里只在未来有
// 代码绕过检查插入新行时才会触发。
func (s *AutopilotService) resolveAutopilotLeader(ctx context.Context, ap db.Autopilot) (agent db.Agent, squadResolved bool, err error) {
	switch ap.AssigneeType {
	case "", "agent":
		agent, err = s.Queries.GetAgent(ctx, ap.AssigneeID)
		return agent, false, err
	case "squad":
		squad, err := s.Queries.GetSquad(ctx, ap.AssigneeID)
		if err != nil {
			return db.Agent{}, true, fmt.Errorf("load squad: %w", err)
		}
		if squad.ArchivedAt.Valid {
			return db.Agent{}, true, errSquadArchived
		}
		agent, err = s.Queries.GetAgent(ctx, squad.LeaderID)
		if err != nil {
			return db.Agent{}, true, fmt.Errorf("load squad leader: %w", err)
		}
		return agent, true, nil
	default:
		return db.Agent{}, false, fmt.Errorf("unknown assignee_type %q", ap.AssigneeType)
	}
}

// autopilotSquadAttribution 取 autopilot_run 行的 squad_id 归属钩子。仅当 assignee_type='squad'
// 时填充。第一版报表不消费它；它存在的意义是未来做 squad 成本视图时不用补数——见 RFC §4.e
// （MUL-2429）。
func autopilotSquadAttribution(ap db.Autopilot) pgtype.UUID {
	if ap.AssigneeType == "squad" && ap.AssigneeID.Valid {
		return ap.AssigneeID
	}
	return pgtype.UUID{}
}

// recordSkippedRun 以给定原因持久化一条 `skipped` 的 autopilot_run，并发出与正常终态切换相同的
// WS/analytics 信号。返回 run + nil error，让调用方（调度 tick、手动触发 handler）把它当作一次
// 成功但无操作的派发。
func (s *AutopilotService) recordSkippedRun(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	source string,
	payload []byte,
	plannedAt pgtype.Timestamptz,
	webhookDeliveryID pgtype.UUID,
	reason string,
	reasonCode ...dispatch.ReasonCode,
) (*db.AutopilotRun, error) {
	if s.TxStarter != nil {
		return s.recordSkippedRunDurably(ctx, autopilot, triggerID, source, payload, plannedAt, webhookDeliveryID, reason, reasonCode...)
	}
	code := pgtype.Text{}
	if len(reasonCode) > 0 && reasonCode[0] != "" {
		code = pgtype.Text{String: string(reasonCode[0]), Valid: true}
	}
	run, err := s.Queries.CreateAutopilotRun(ctx, db.CreateAutopilotRunParams{
		ID:                dbid.NewV7(),
		AutopilotID:       autopilot.ID,
		TriggerID:         triggerID,
		Source:            source,
		Status:            "skipped",
		TriggerPayload:    payload,
		SquadID:           autopilotSquadAttribution(autopilot),
		PlannedAt:         plannedAt,
		WebhookDeliveryID: webhookDeliveryID,
		ReasonCode:        code,
	})
	if err != nil {
		return nil, fmt.Errorf("create skipped run: %w", err)
	}

	updated, err := s.Queries.UpdateAutopilotRunSkipped(ctx, db.UpdateAutopilotRunSkippedParams{
		ID:            run.ID,
		FailureReason: pgtype.Text{String: reason, Valid: true},
		ReasonCode:    code,
	})
	if err == nil {
		run = updated
	} else {
		slog.Warn("failed to set skip reason on autopilot run",
			"run_id", util.UUIDToString(run.ID), "error", err)
	}

	slog.Info("autopilot dispatch skipped",
		"autopilot_id", util.UUIDToString(autopilot.ID),
		"run_id", util.UUIDToString(run.ID),
		"source", source,
		"reason", reason,
	)

	// 同步 bump last_run_at，让调度推进与「上次运行」UI 都反映本 tick 确实评估了触发。
	s.Queries.UpdateAutopilotLastRunAt(ctx, autopilot.ID)

	s.publishRunDone(util.UUIDToString(autopilot.WorkspaceID), run, "skipped")
	return &run, nil
}

// recordSkippedRunDurably 是 recordSkippedRun 的事务版：建 run、写 skip 原因、bump last_run_at 与
// 持久化 run-done 事件放在同一事务里，供 TxStarter != nil（生产）的调用方使用。
func (s *AutopilotService) recordSkippedRunDurably(
	ctx context.Context,
	autopilot db.Autopilot,
	triggerID pgtype.UUID,
	source string,
	payload []byte,
	plannedAt pgtype.Timestamptz,
	webhookDeliveryID pgtype.UUID,
	reason string,
	reasonCode ...dispatch.ReasonCode,
) (*db.AutopilotRun, error) {
	code := pgtype.Text{}
	if len(reasonCode) > 0 && reasonCode[0] != "" {
		code = pgtype.Text{String: string(reasonCode[0]), Valid: true}
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin skipped autopilot run: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	run, err := qtx.CreateAutopilotRun(ctx, db.CreateAutopilotRunParams{
		ID:                dbid.NewV7(),
		AutopilotID:       autopilot.ID,
		TriggerID:         triggerID,
		Source:            source,
		Status:            "skipped",
		TriggerPayload:    payload,
		SquadID:           autopilotSquadAttribution(autopilot),
		PlannedAt:         plannedAt,
		WebhookDeliveryID: webhookDeliveryID,
		ReasonCode:        code,
	})
	if err != nil {
		return nil, fmt.Errorf("create skipped run: %w", err)
	}
	run, err = qtx.UpdateAutopilotRunSkipped(ctx, db.UpdateAutopilotRunSkippedParams{
		ID:            run.ID,
		FailureReason: pgtype.Text{String: reason, Valid: true},
		ReasonCode:    code,
	})
	if err != nil {
		return nil, fmt.Errorf("set skipped run reason: %w", err)
	}
	if err := qtx.UpdateAutopilotLastRunAt(ctx, autopilot.ID); err != nil {
		return nil, fmt.Errorf("update autopilot last run: %w", err)
	}
	event, err := recordDurableEventTx(ctx, qtx, autopilotRunDoneEvent(autopilot.WorkspaceID, run, "skipped"))
	if err != nil {
		return nil, fmt.Errorf("record skipped run event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit skipped autopilot run: %w", err)
	}
	publishCommittedEvent(s.Bus, event)
	slog.Info("autopilot dispatch skipped", "autopilot_id", util.UUIDToString(autopilot.ID), "run_id", util.UUIDToString(run.ID), "source", source, "reason", reason)
	return &run, nil
}

// publishRunDone 向事件总线发一条「autopilot run 收尾」的实时事件（非持久化），供在线端即时刷新。
func (s *AutopilotService) publishRunDone(workspaceID string, run db.AutopilotRun, status string) {
	s.Bus.Publish(events.Event{
		Type:        protocol.EventAutopilotRunDone,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		Payload: map[string]any{
			"run_id":       util.UUIDToString(run.ID),
			"autopilot_id": util.UUIDToString(run.AutopilotID),
			"status":       status,
		},
	})
}

// autopilotRunDoneEvent 构造持久化版本的 run-done 事件（带幂等键与流键，写入 durable event 表），
// 供事务内落库、提交后再扇出。
func autopilotRunDoneEvent(workspaceID pgtype.UUID, run db.AutopilotRun, status string) events.Event {
	return events.Event{
		Type:           protocol.EventAutopilotRunDone,
		IdempotencyKey: "autopilot:run-done:" + util.UUIDToString(run.ID) + ":" + status,
		StreamKey:      "autopilot:" + util.UUIDToString(run.AutopilotID),
		WorkspaceID:    util.UUIDToString(workspaceID),
		ActorType:      "system",
		Payload: map[string]any{
			"run_id":       util.UUIDToString(run.ID),
			"autopilot_id": util.UUIDToString(run.AutopilotID),
			"status":       status,
		},
	}
}

func (s *AutopilotService) captureIssueCreatedFromAutopilot(ap db.Autopilot, run *db.AutopilotRun, issue db.Issue, leaderID pgtype.UUID) {
	if s.TaskSvc == nil || s.TaskSvc.Analytics == nil {
		return
	}
	// PostHog 里的 agent_id 应是实际跑活的那个 agent（squad 型取解析出的 leader），这样按 agent
	// 统计的任务数与 daemon 上报的才对得上。
	obsmetrics.RecordEvent(s.TaskSvc.Analytics, s.TaskSvc.Metrics, analytics.IssueCreated(
		autopilotActorID(ap),
		util.UUIDToString(ap.WorkspaceID),
		util.UUIDToString(issue.ID),
		util.UUIDToString(leaderID),
		"",
		util.UUIDToString(run.ID),
		analytics.SourceAutopilot,
		analytics.PlatformServer,
	))
}

// captureAutopilotRunStarted 上报 run 开始的分析事件。triggerSource 同时充当 cadence 代理字段——
// 见 metrics/labels_pr3.go 里的 autopilot cadence 说明。
func (s *AutopilotService) captureAutopilotRunStarted(ap db.Autopilot, run db.AutopilotRun, triggerSource string) {
	if s.TaskSvc == nil || s.TaskSvc.Analytics == nil {
		return
	}
	obsmetrics.RecordEvent(s.TaskSvc.Analytics, s.TaskSvc.Metrics, analytics.AutopilotRunStarted(
		autopilotActorID(ap),
		util.UUIDToString(ap.WorkspaceID),
		util.UUIDToString(ap.ID),
		util.UUIDToString(run.ID),
		triggerSource, // cadence 代理：见 metrics/labels_pr3.go 的 autopilot cadence 说明
		s.autopilotAssigneeAnalytics(ap),
		triggerSource,
	))
}

// captureAutopilotRunCompleted 上报 run 成功结束的分析事件。
func (s *AutopilotService) captureAutopilotRunCompleted(ap db.Autopilot, run db.AutopilotRun) {
	if s.TaskSvc == nil || s.TaskSvc.Analytics == nil {
		return
	}
	obsmetrics.RecordEvent(s.TaskSvc.Analytics, s.TaskSvc.Metrics, analytics.AutopilotRunCompleted(
		autopilotActorID(ap),
		util.UUIDToString(ap.WorkspaceID),
		util.UUIDToString(ap.ID),
		util.UUIDToString(run.ID),
		run.Source,
		s.autopilotAssigneeAnalytics(ap),
		run.Source,
		autopilotRunDurationMS(run),
	))
}

// captureAutopilotRunFailed 上报 run 失败的分析事件，reason 为空时给 "unknown" 兜底。
func (s *AutopilotService) captureAutopilotRunFailed(ap db.Autopilot, run db.AutopilotRun, triggerSource, reason string) {
	if s.TaskSvc == nil || s.TaskSvc.Analytics == nil {
		return
	}
	if reason == "" {
		reason = "unknown"
	}
	obsmetrics.RecordEvent(s.TaskSvc.Analytics, s.TaskSvc.Metrics, analytics.AutopilotRunFailed(
		autopilotActorID(ap),
		util.UUIDToString(ap.WorkspaceID),
		util.UUIDToString(ap.ID),
		util.UUIDToString(run.ID),
		triggerSource,
		s.autopilotAssigneeAnalytics(ap),
		triggerSource,
		reason,
		autopilotErrorType(reason),
		false,
		autopilotRunDurationMS(run),
	))
}

// autopilotAssigneeAnalytics 构造 autopilot 的 PostHog assignee 描述。squad 型 autopilot 的 agent_id
// 尽力取解析出的 leader（保证按 agent 的漏斗一致）；解析失败时降级为原始 assignee_id，而不是丢弃
// 事件——仪表盘上数据不完整，好过静默的归属缺口。
func (s *AutopilotService) autopilotAssigneeAnalytics(ap db.Autopilot) analytics.AutopilotAssignee {
	assignee := analytics.AutopilotAssignee{
		AssigneeType: ap.AssigneeType,
	}
	if ap.AssigneeType == "squad" {
		assignee.SquadID = util.UUIDToString(ap.AssigneeID)
		if leader, _, err := s.resolveAutopilotLeader(context.Background(), ap); err == nil {
			assignee.AgentID = util.UUIDToString(leader.ID)
		} else {
			assignee.AgentID = util.UUIDToString(ap.AssigneeID)
		}
	} else {
		assignee.AgentID = util.UUIDToString(ap.AssigneeID)
	}
	return assignee
}

// autopilotErrorType 把失败原因粗略归类成分析用的错误类型。靠子串判断（而非错误值），因为这里
// 拿到的是已格式化的 reason 文本；分类边界需要与下游分析团队约定的类型保持一致。
func autopilotErrorType(reason string) string {
	switch {
	case strings.Contains(reason, "unknown execution_mode"):
		return "configuration"
	case strings.HasPrefix(reason, "issue "):
		return "issue_terminal"
	case strings.Contains(reason, "create issue"), strings.Contains(reason, "enqueue task"), strings.Contains(reason, "dispatch"):
		return "dispatch_error"
	case strings.HasPrefix(reason, "task "):
		return "task_error"
	default:
		return "autopilot_error"
	}
}

// autopilotActorID 把 autopilot 的创建者折算成分析事件里的 actor 字符串：agent 创建者带 "agent:"
// 前缀，member 用裸 id，无有效创建者时用 "system"。
func autopilotActorID(ap db.Autopilot) string {
	id := util.UUIDToString(ap.CreatedByID)
	if ap.CreatedByType == "agent" && id != "" {
		return "agent:" + id
	}
	if id != "" {
		return id
	}
	return "system"
}

func autopilotRunDurationMS(run db.AutopilotRun) int64 {
	if !run.CompletedAt.Valid {
		return 0
	}
	start := run.TriggeredAt
	if !start.Valid {
		start = run.CreatedAt
	}
	if !start.Valid {
		return 0
	}
	ms := run.CompletedAt.Time.Sub(start.Time).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

// resolveAutopilotTriggerTimezone 解析触发的配置时区，失败或未配置一律回退
// DefaultAutopilotTriggerTimezone，保证渲染不因时区数据缺失而中断。
func (s *AutopilotService) resolveAutopilotTriggerTimezone(ctx context.Context, triggerID pgtype.UUID) string {
	if !triggerID.Valid || s == nil || s.Queries == nil {
		return DefaultAutopilotTriggerTimezone
	}

	trigger, err := s.Queries.GetAutopilotTrigger(ctx, triggerID)
	if err != nil {
		slog.Warn("failed to load autopilot trigger timezone; falling back to UTC",
			"trigger_id", util.UUIDToString(triggerID),
			"error", err,
		)
		return DefaultAutopilotTriggerTimezone
	}

	timezone := strings.TrimSpace(trigger.Timezone.String)
	if !trigger.Timezone.Valid || timezone == "" {
		return DefaultAutopilotTriggerTimezone
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		slog.Warn("invalid autopilot trigger timezone; falling back to UTC",
			"trigger_id", util.UUIDToString(triggerID),
			"timezone", timezone,
			"error", err,
		)
		return DefaultAutopilotTriggerTimezone
	}
	return timezone
}

func formatAutopilotRunTimestamp(run db.AutopilotRun, timezone string) string {
	triggeredAt := autopilotRunTriggeredAt(run)
	loc, label := autopilotTriggerLocation(timezone)
	return triggeredAt.In(loc).Format("2006-01-02 15:04") + " " + label
}

func formatAutopilotRunDate(run db.AutopilotRun, timezone string) string {
	triggeredAt := autopilotRunTriggeredAt(run)
	loc, _ := autopilotTriggerLocation(timezone)
	return triggeredAt.In(loc).Format("2006-01-02")
}

func autopilotRunTriggeredAt(run db.AutopilotRun) time.Time {
	if run.TriggeredAt.Valid {
		return run.TriggeredAt.Time
	}
	if run.CreatedAt.Valid {
		return run.CreatedAt.Time
	}
	return time.Now().UTC()
}

func autopilotTriggerLocation(timezone string) (*time.Location, string) {
	label := strings.TrimSpace(timezone)
	if label == "" {
		label = DefaultAutopilotTriggerTimezone
	}
	loc, err := time.LoadLocation(label)
	if err != nil {
		return time.UTC, DefaultAutopilotTriggerTimezone
	}
	return loc, label
}

// buildIssueDescription 在用户提供的描述末尾追加一段 autopilot 系统指令：请 agent 理解实际工作后
// 重命名 issue。webhook 来源的 run 还会追加一个 payload 段，把事件上下文内联给 agent——否则 agent
// 只能看到 issue 正文，永远看不到 run 的 trigger_payload。
func (s *AutopilotService) buildIssueDescription(ap db.Autopilot, run db.AutopilotRun, triggerTimezone string) pgtype.Text {
	triggeredAt := formatAutopilotRunTimestamp(run, triggerTimezone)
	var b strings.Builder
	b.WriteString(ap.Description.String)
	b.WriteString("\n\n---\n*Autopilot run triggered at ")
	b.WriteString(triggeredAt)
	b.WriteString(". After starting work, rename this issue to accurately reflect what you are doing.*")

	if run.Source == "webhook" && len(run.TriggerPayload) > 0 {
		event := "webhook.received"
		var payloadJSON []byte
		var env struct {
			Event        string          `json:"event"`
			EventPayload json.RawMessage `json:"eventPayload"`
		}
		if err := json.Unmarshal(run.TriggerPayload, &env); err == nil {
			if env.Event != "" {
				event = env.Event
			}
			if len(env.EventPayload) > 0 {
				if pretty, err := prettifyJSON(env.EventPayload); err == nil {
					payloadJSON = pretty
				}
			}
		}
		if len(payloadJSON) == 0 {
			if pretty, err := prettifyJSON(run.TriggerPayload); err == nil {
				payloadJSON = pretty
			} else {
				payloadJSON = run.TriggerPayload
			}
		}
		b.WriteString("\n\nWebhook event: ")
		b.WriteString(event)
		b.WriteString("\n\nWebhook payload:\n```json\n")
		b.Write(payloadJSON)
		b.WriteString("\n```")
	}

	return pgtype.Text{String: b.String(), Valid: true}
}

func prettifyJSON(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.MarshalIndent(v, "", "  ")
}

// issueTitleTemplateTokenRE 匹配 issue 标题模板里的任意 {{...}} token。刻意容忍花括号内有空白
// （{{ date }}），让用户两种写法都行；规范写法仍是 {{date}}。
var issueTitleTemplateTokenRE = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

// interpolateTemplate 替换 issue 标题模板中受支持的 {{name}} 占位符。花括号内空白（{{ date }}）
// 同样容忍，保证渲染层接受 ValidateIssueTitleTemplate 接受的每一种写法——否则用户会保存一个通过
// 校验、触发时却原样吐出字面 token 的模板。
func (s *AutopilotService) interpolateTemplate(ap db.Autopilot, run db.AutopilotRun, triggerTimezone string) string {
	tmpl := ap.Title
	if ap.IssueTitleTemplate.Valid && ap.IssueTitleTemplate.String != "" {
		tmpl = ap.IssueTitleTemplate.String
	}
	triggerDate := formatAutopilotRunDate(run, triggerTimezone)
	return issueTitleTemplateTokenRE.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := strings.TrimSpace(match[2 : len(match)-2])
		switch name {
		case "date":
			return triggerDate
		default:
			return match
		}
	})
}

// SupportedIssueTitleTemplateVariables 列出 interpolateTemplate 会替换的占位符。改动时必须与上面
// 的替换逻辑以及 autopilots.mdx / autopilots.zh.mdx 文档同步。
var SupportedIssueTitleTemplateVariables = []string{"date"}

// ValidateIssueTitleTemplate 拒绝任何含受支持集合之外 {{...}} token 的模板。空模板合法（autopilot
// 回退用自己的 Title）。错误信息点名第一个违规 token，让 CLI 反馈可直接照做。
func ValidateIssueTitleTemplate(tmpl string) error {
	if tmpl == "" {
		return nil
	}
	for _, m := range issueTitleTemplateTokenRE.FindAllStringSubmatch(tmpl, -1) {
		name := m[1]
		if !isSupportedIssueTitleVariable(name) {
			return fmt.Errorf(
				"unknown template variable %q; supported: {{%s}}",
				name,
				strings.Join(SupportedIssueTitleTemplateVariables, "}}, {{"),
			)
		}
	}
	return nil
}

func isSupportedIssueTitleVariable(name string) bool {
	for _, v := range SupportedIssueTitleTemplateVariables {
		if name == v {
			return true
		}
	}
	return false
}

func (s *AutopilotService) getIssuePrefix(workspaceID pgtype.UUID) string {
	ws, err := s.Queries.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return ""
	}
	return ws.IssuePrefix
}

// autopilotAdmitInvoke 判断派发的准入主体能否调用目标 agent（MUL-4525）。MANUAL「立即运行」
// （actorUserID 有效）是直接的人类行为，按当前点击者的访问权限门控，准入与归属因此记到同一
// 成员身上；自动化（计划/webhook/api，actorUserID 无效）没有真人参与，退回 autopilot creator。
// 两条分支都 fail-closed、绝不给管理员开后门。
func (s *AutopilotService) autopilotAdmitInvoke(ctx context.Context, ap db.Autopilot, agent db.Agent, actorUserID pgtype.UUID) bool {
	if actorUserID.Valid {
		return s.canMemberInvokeAgent(ctx, agent, actorUserID, ap.WorkspaceID)
	}
	return s.canCreatorInvokeAgent(ctx, ap, agent)
}

// canMemberInvokeAgent 按调用权限模型（MUL-3963）判断某个具体成员能否调用该 agent。它是
// handler.canInvokeAgent 以 member 为有效用户时的镜像——用于手动 autopilot「立即运行」，此时准入
// 主体是点击者而非 creator。任何查询错误都 fail-closed；无管理员旁路。
func (s *AutopilotService) canMemberInvokeAgent(ctx context.Context, agent db.Agent, memberUserID pgtype.UUID, workspaceID pgtype.UUID) bool {
	userID := util.UUIDToString(memberUserID)
	if userID == "" {
		return false
	}
	if util.UUIDToString(agent.OwnerID) == userID {
		return true
	}
	if agent.PermissionMode != "public_to" {
		return false
	}
	targets, err := s.Queries.ListAgentInvocationTargets(ctx, agent.ID)
	if err != nil {
		return false
	}
	isWorkspaceMember := false
	if _, err := s.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      memberUserID,
		WorkspaceID: workspaceID,
	}); err == nil {
		isWorkspaceMember = true
	}
	for _, t := range targets {
		switch t.TargetType {
		case "workspace":
			if isWorkspaceMember {
				return true
			}
		case "member":
			if util.UUIDToString(t.TargetID) == userID {
				return true
			}
		}
	}
	return false
}

// canCreatorInvokeAgent 按调用权限模型（MUL-3963）判断 autopilot 的 creator 能否调用目标 agent。
// 以 autopilot creator 作为有效用户镜像 handler.canInvokeAgent：
//   - member creator 且拥有该 agent → 始终放行
//   - private agent → 仅 owner（无管理员旁路、无 agent-created 旁路）
//   - public_to agent → workspace 目标放行任意 workspace-member creator（agent 创建的 autopilot
//     按 workspace 主体放行）；member 目标放行匹配的 creator；team 目标无效（惰性）。
//
// 任何查询错误都 fail-closed。
func (s *AutopilotService) canCreatorInvokeAgent(ctx context.Context, ap db.Autopilot, agent db.Agent) bool {
	creatorID := util.UUIDToString(ap.CreatedByID)
	if ap.CreatedByType == "member" && util.UUIDToString(agent.OwnerID) == creatorID {
		return true
	}
	if agent.PermissionMode != "public_to" {
		// private（或未知模式）：默认拒绝；只有上面 owner 分支能过。管理员与 agent 创建的
		// autopilot 都不能旁路。
		return false
	}
	targets, err := s.Queries.ListAgentInvocationTargets(ctx, agent.ID)
	if err != nil {
		return false
	}
	// agent 创建的 autopilot 是工作区内部主体：workspace 目标即放行。member creator 必须真是
	// 工作区成员。
	workspaceBroad := ap.CreatedByType == "agent"
	isWorkspaceMember := false
	if ap.CreatedByType == "member" {
		if _, err := s.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID:      ap.CreatedByID,
			WorkspaceID: ap.WorkspaceID,
		}); err == nil {
			isWorkspaceMember = true
		}
	}
	for _, t := range targets {
		switch t.TargetType {
		case "workspace":
			if isWorkspaceMember || workspaceBroad {
				return true
			}
		case "member":
			if ap.CreatedByType == "member" && util.UUIDToString(t.TargetID) == creatorID {
				return true
			}
		}
	}
	return false
}
