// task.go —— 任务队列核心服务 TaskService。
//
// 职责：任务入队（issue 指派 / @提及 / 交接接力 / 群组长 / 快速创建 / 频道聊天 / 延时 fireAt）、
// 认领与状态推进（queued→dispatched→started→completed/failed/cancelled）、取消与广播、失败重试、
// 委托失败恢复、完成/失败后的评论落库与指标/通知/分析上报，以及 attribution 责任归属解析、
// 任务级 Redis 提示缓存（reclaim）。多条路径经 TxStarter/runInTx 在事务内落库后再对外广播。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/chattitle"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/runtimeapps"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
	"github.com/multica-ai/multica/server/pkg/skillbundle"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// TaskService 聚合任务队列全部业务操作（入队、认领、推进状态、取消、完成/失败收尾等）。
// 依赖多为可选注入：SourceContextStorage / FeatureFlags / EmptyClaim / ReclaimCheck /
// Composio / QuickActions 为 nil 时对应能力自动降级（见各字段说明），便于自托管与单测。
type TaskService struct {
	// Queries 生成代码的数据库查询句柄；普通路径直用，事务内改用事务句柄 qtx。
	Queries *db.Queries
	// TxStarter 开启事务并产出事务内 db.Queries 的工厂，供需要原子落库的路径使用。
	TxStarter TxStarter
	// Hub 实时连接中心，向在线客户端推送任务状态变更。
	Hub *realtime.Hub
	// Bus 事件总线：任务入队/终态等事件经此发布，供审计、通知等订阅方消费。
	Bus *events.Bus
	// Analytics 分析上报客户端，记录任务来源/耗时等使用数据。
	Analytics analytics.Client
	// Metrics 业务指标，nil 时所有 capture*/Record* 调用直接跳过。
	Metrics *obsmetrics.BusinessMetrics
	// Wakeup 通知守护进程“可能有可认领任务”；nil 表示不通知。
	Wakeup TaskWakeupNotifier
	// restricted action 提供 Cloud 版按工作区限制 issue 数量的能力。
	// nil 表示自托管与隔离测试服务不设上限。
	// SourceContextStorage 仅用于对终态 quick-create 快照做有界 30 天清理。
	// nil 表示没有存储、禁用该清理。
	SourceContextStorage SourceContextObjectStore
	// FeatureFlags 服务端功能开关路由。nil 合法，回落到各调用点的默认行为。
	FeatureFlags *featureflag.Service
	// EmptyClaim 缓存“某运行时当前无排队任务”，让守护进程轮询在稳态空闲时跳过一次 Postgres 扫描。
	// 可选：nil 时禁用快路径，每次认领都走 DB。router.go 用共享 Redis client 注入。
	EmptyClaim *EmptyClaimCache
	// ReclaimCheck 安排“下一次该运行时可能出现可回收的陈旧 dispatched 任务”的时间点，
	// 使空闲认领轮询不再无条件执行回收 UPDATE；miss/error 状态仍保留 DB 兜底。
	ReclaimCheck *ReclaimCheckCache
	// Composio 计算每个任务的 MCP overlay（Composio 阶段 3，MUL-3721）——把“当前用户已连接应用 → MCP
	// 会话 URL”接到各 Enqueue* 路径。可选：nil 的 ComposioOverlayBuilder 使 overlay 步骤变 no-op，
	// 任何未启用 Composio 的 Multica 部署行为与原来完全一致。router.go 在 composiointeg.NewService 成功后注入，
	// 具体类型为 *composio.Service。
	Composio ComposioOverlayBuilder
	// QuickActions 通过服务端内置 LLM 层生成聊天后续建议。可选：nil（或被禁用的 client）整项关闭——
	// 不写 pending 标记、不产出建议 pills——这是未配置 MULTICA_LLM_* 的自托管部署的预期状态。
	// router.go 用支持聊天自动起标题的同一个 *llm.Client 注入。
	QuickActions ChatQuickActionsLLM
	// quickActionsInFlight（chat session id → struct{}）记录正在生成建议的会话，与 quickActionsRunning
	// 一起做建议生成的准入控制：同会话同时只允许一轮，进程级还有总量上限。
	quickActionsInFlight sync.Map
	// quickActionsRunning 是进程级“正在生成建议”的并发计数器，控制同时进行的建议生成总量。
	// 上述两者的零值均可用：不经 NewTaskService 构造的 TaskService 也能正确限流，不会死锁或全丢。
	quickActionsRunning atomic.Int64

	// analyticsContextMu 保护下面的分析上下文 LRU 缓存。
	analyticsContextMu sync.Mutex
	// analyticsContextCache 按 taskAnalyticsContextKey 缓存任务的分析上下文，避免重复查库。
	analyticsContextCache map[string]analytics.TaskContext
	// analyticsContextOrder 记录缓存键插入顺序，用于超出上限时淘汰最旧条目。
	analyticsContextOrder []string

	// 修改 1. UpstreamRuns 在任务进入终态时关闭它名下的上游同步批次。可选：nil 表示不收敛
	// （直连 service 的测试夹具），生产由 main 注入 upstream.Service。
	UpstreamRuns UpstreamRunCloser
}

// UpstreamRunCloser 关闭某个任务名下仍在进行的上游同步批次。status 只取 failed（任务失败）
// 或 interrupted（任务结束但批次没走完）。
type UpstreamRunCloser interface {
	CloseTaskRuns(ctx context.Context, taskID, status, message string) (int, error)
}

// SourceContextObjectStore 抽象对象存储：仅用于清理终态 quick-create 任务关联的 source-context 对象。
type SourceContextObjectStore interface {
	DeleteObject(ctx context.Context, key string) error
	KeyFromURL(rawURL string) string
}

// ComposioOverlayBuilder 是 TaskService 在入队时构建每个任务 MCP overlay 的接缝，
// 由 internal/integrations/composio.Service.BuildTaskOverlay 实现；测试用内联 fake，避免起整套 Composio SDK。
//
// 契约：零值 MCPOverlayResult 表示“本次运行无 overlay”——覆盖实现强制的一切门槛（无 owner / 空 allowlist /
// 与活跃连接交集为空 / 会话 URL 为空）。非空 MCPOverlay 即原样写入 agent_task_queue.runtime_mcp_overlay；
// ConnectedApps 是非机密元数据，与之同存供 daemon brief 注入。返回非 nil 错误会上抛给调用方，但按
// best-effort 处理——overlay 计算失败绝不能导致入队失败。
//
// agent 按值传入，让构建方直接读 OwnerID 与 ComposioToolkitAllowlist 而无需再查一次 DB；各入队路径本来就
// 为运行时/归档检查加载过 agent，按值传入零成本，还能省掉热路径上一次多余的 GetAgent 往返。
type ComposioOverlayBuilder interface {
	BuildTaskOverlay(ctx context.Context, originatorUserID pgtype.UUID, agent db.Agent) (runtimeapps.MCPOverlayResult, error)
}

// TaskWakeupNotifier 通知指定 runtime 有可认领任务（HTTP 唤醒通道）。
type TaskWakeupNotifier interface {
	NotifyTaskAvailable(runtimeID, taskID string)
}

// triggerSummaryMaxLen 限制触发评论摘要长度，让任务行保持廉价（它会出现在每个任务列表响应中）。
// 200 对一段落评论足以形成可辨认的预览。
const triggerSummaryMaxLen = 200

// truncateForSummary 把 s 截断到 maxRunes 个 rune，超长时末尾补“…”；按 rune（非字节）截断，
// 多字节字符（中文/emoji）各算一个。先去掉首尾空白，避免开头的换行浪费预算。
func truncateForSummary(s string, maxRunes int) string {
	// 用 strings.Builder + Grow 规避循环里 `+=` 的 O(N²) 反复扩容。Grow 按字节长度预分配，
	// 是 rune 级输出（换行/回车/Tab 都换成空格后 ASCII 空白按字节等长）的上界，所以全程不再扩容。
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\n', '\r', '\t':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	rs := []rune(strings.TrimSpace(b.String()))
	if len(rs) <= maxRunes {
		return string(rs)
	}
	return string(rs[:maxRunes]) + "…"
}

// maxSynthesizedFallbackCommentRunes 限制“完成兜底评论”的长度：当 agent 运行中没留下自己的评论时，
// CompleteTask 会用任务最终输出合成一条。真正的最终助手消息最多几千词；更大的通常是失控的原始流导出——
// 所有流式文本增量拼接在一起、每条 tool_use 事件还带一行字面 `tool call`——某些 runtime/provider 在长而
// 工具密集的运行里会把这当作任务的 Output。这类导出（实测 190–264KB）绝不能（哪怕部分地）贴到 issue 线程（GH #5455）。
const maxSynthesizedFallbackCommentRunes = 8000

// oversizedFallbackCommentNotice 是超限时整段替换用的固定提示文案，直接面向 issue 读者，故保留英文。
const oversizedFallbackCommentNotice = "This task completed, but its output was too large to post safely. The raw output was not posted. Review the task in this issue's Execution log."

// truncateFallbackCommentBody 限制合成的完成兜底评论正文。与 truncateForSummary（把换行拍平成一行的
// 行快照）不同，它原样保留未超限的真实最终消息。超限输出不可信：已知故障形态把过程叙述与工具痕迹放在最前，
// 保留任何片段都可能泄露执行细节且仍然丢掉最终答案，因此整体替换为固定提示。调用方传入的是已脱敏正文。
func truncateFallbackCommentBody(body string, maxRunes int) string {
	if utf8.RuneCountInString(body) <= maxRunes {
		return body
	}
	return oversizedFallbackCommentNotice
}

const (
	taskAnalyticsContextCacheMax = 4096
	// RuntimeClaimFreshnessSeconds 是所有任务释放路径（延时提升、陈旧 dispatch 回收、新认领）可接受的最大
	// DB 心跳年龄。它必须超过 60s 的 DB 心跳刷新间隔、一次约 15s 的 daemon 心跳和约 30s 的批调度 tick。
	// 150s 在 105s 最坏年龄之上留 45s 缓冲。runtime 清扫阈值须与之对齐：runtime 不应在陈旧到会被标记
	// 离线之后才开始干活。
	RuntimeClaimFreshnessSeconds = 150.0
	// claimResponseRecoveryWindow 必须超过 daemon client.Timeout（/tasks/claim 的 30s 加 /tasks/{id}/start
	// 的 30s）再加调度余量。更长的启动前工作由 prepareLeaseDuration 保护，而不是拉长这个全局崩溃恢复窗口。
	claimResponseRecoveryWindow = 90 * time.Second
	// prepareLeaseDuration 是任务开始前准备阶段（下载代码等）的租约时长，避免“已认领但尚未 start”的任务被误判陈旧。
	prepareLeaseDuration = 45 * time.Second
)

// trackTaskForReclaim 把“该 runtime 在 checkAfter 之后可能含有可回收的陈旧 dispatched 任务”写入 Redis 提示
// （best-effort）。仅对 dispatched 状态且 runtime/ID 均有效的任务生效。
func (s *TaskService) trackTaskForReclaim(task db.AgentTaskQueue, checkAfter time.Time) {
	if !task.RuntimeID.Valid || !task.ID.Valid || task.Status != "dispatched" {
		return
	}
	// Redis 提示仅作参考，且刻意用“紧邻 DB 命令捕获的应用时钟已过截止时间”。拿 PostgreSQL 的绝对
	// dispatched_at/lease 时间戳与 time.Now 比较会混用时钟；真正执行回收查询时仍以 PostgreSQL 为准。
	// 用带超时的后台 context 让这次 best-effort 写入能活过请求取消，又不至于让 Redis 无限拖住热路径。
	s.ReclaimCheck.Track(
		context.Background(),
		util.UUIDToString(task.RuntimeID),
		util.UUIDToString(task.ID),
		checkAfter,
	)
}

// extendTaskReclaimHint 把回收提示的检查时间推迟到更晚的 checkAfter（例如延长 prepare 租约时使用）。
func (s *TaskService) extendTaskReclaimHint(task db.AgentTaskQueue, checkAfter time.Time) {
	if !task.RuntimeID.Valid || !task.ID.Valid || task.Status != "dispatched" {
		return
	}
	// 保留更晚的初始恢复截止时间：只有当反复延长确实把任务保护得更久时，短 prepare 租约才应移动提示。
	// 时钟/context 理由见 trackTaskForReclaim。
	s.ReclaimCheck.TrackLater(
		context.Background(),
		util.UUIDToString(task.RuntimeID),
		util.UUIDToString(task.ID),
		checkAfter,
	)
}

// forgetTaskReclaim 在任务进入终态（不再可能被回收）时清除对应的 Redis 回收提示。
func (s *TaskService) forgetTaskReclaim(task db.AgentTaskQueue) {
	if !task.RuntimeID.Valid || !task.ID.Valid {
		return
	}
	// 清理是 best-effort 并使用缓存自带的超时；请求取消不能让一个已提交的任务状态转换残留过期提示。
	s.ReclaimCheck.Forget(
		context.Background(),
		util.UUIDToString(task.RuntimeID),
		util.UUIDToString(task.ID),
	)
}

// buildCommentTriggerSummary 拉取触发评论内容并截断，存到任务行的 trigger_summary。评论缺失（已删除 /
// 非本工作区等）时返回无效 pgtype.Text，使该列保持 NULL——前端此时回退显示结构化标签。
//
// workspaceID 把查询限定在任务自己的工作区：摘要之后会出现在认领 / 任务历史响应里，
// 因此外部评论 UUID 进入入队/合并路径时绝不能泄露其它工作区的文本，即使只是截断形式（MUL-4252）。
func (s *TaskService) buildCommentTriggerSummary(ctx context.Context, workspaceID, commentID pgtype.UUID) pgtype.Text {
	if !commentID.Valid {
		return pgtype.Text{}
	}
	comment, err := s.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID:          commentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return pgtype.Text{}
	}
	summary := truncateForSummary(comment.Content, triggerSummaryMaxLen)
	if summary == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: summary, Valid: true}
}

// ResolveOriginatorFromTriggerComment 是对外包装，供评论合并路径（MUL-4195）为一条新到的评论算出
// 链路顶端的“人类发起人”，以便合并时以发起人未变为门槛。workspaceID 把评论查询限定在任务工作区（MUL-4252）。
// 链路规则见 resolveOriginatorFromTriggerComment。
func (s *TaskService) ResolveOriginatorFromTriggerComment(ctx context.Context, workspaceID, commentID pgtype.UUID) pgtype.UUID {
	return s.resolveOriginatorFromTriggerComment(ctx, workspaceID, commentID)
}

// AttributionForMergedComment 为一条要合入已排队任务的评论（MUL-4302）解析完整的 attribution 快照。
// 合并会把运行重新归属到新评论背后的人类，因此整个快照——source、evidence、委托血缘与两个人名列——必须
// 作为一份 attribution.Result 一起移动；只重贴人名列会让运行显示“B 负责”却仍指向 A 的陈旧 source/evidence/level。
// isMention 决定 agent 撰写的标签（mention / thread-parent 走 delegation，否则 comment_source），与全新入队路由一致。
//
// 合并重新面对原入队时的 fail-closed 决策：合并把有效触发、负责人类与 evidence 换成新评论，
// “入队时已校验过”不再成立。这里把评论交给 applyAttributionFallback——与全新入队完全相同的 fail-closed 闸门——
// 当新评论无法精确归因且工作区禁止 owner_fallback 降级时返回 ErrAttributionFailClosed。调用方此时必须拒绝合并，
// 保留原（精确归因的）任务快照，而不是把已排队运行重贴成降级的 owner_fallback（Elon 必修）。
func (s *TaskService) AttributionForMergedComment(ctx context.Context, workspaceID, commentID pgtype.UUID, isMention bool, agent db.Agent) (attribution.Result, error) {
	agentAuthoredSource := attribution.SourceCommentSource
	if isMention {
		agentAuthoredSource = attribution.SourceDelegation
	}
	attr := s.attributionFromTriggerComment(ctx, workspaceID, commentID, agentAuthoredSource)
	return s.applyAttributionFallback(ctx, attr, agent)
}

// BuildCommentTriggerSummary 是对外包装，供评论合并路径（MUL-4195）把合并后任务的 trigger_summary 刷新为
// 最新触发评论的快照。workspaceID 限定查询范围（MUL-4252）。
func (s *TaskService) BuildCommentTriggerSummary(ctx context.Context, workspaceID, commentID pgtype.UUID) pgtype.Text {
	return s.buildCommentTriggerSummary(ctx, workspaceID, commentID)
}

// BuildRuntimeMCPOverlayForMerge 为 (originatorUserID, agent) 重算 Composio MCP overlay 与 connected-app
// 元数据，用于合并时重贴合并任务的 originator（MUL-4195 review must-fix #1）。overlay 是 (originator, agent)
// 的纯函数；与 originator_user_id 一起重贴，使合入运行的应用连接能力与审计归因跟随最新触发评论的 originator，
// 而非任务原来的那个。失败软回退为空（与入队路径相同），瞬时 Composio 故障绝不阻塞合并。
func (s *TaskService) BuildRuntimeMCPOverlayForMerge(ctx context.Context, originatorUserID pgtype.UUID, agent db.Agent) (overlay, connectedApps []byte) {
	data := s.buildRuntimeMCPOverlay(ctx, originatorUserID, agent)
	return data.Overlay, data.ConnectedApps
}

// NewTaskService 构造任务服务，只注入核心依赖（DB、事务、实时中心、事件总线）。其余可选能力（指标、分析、
// Redis 缓存、Composio、QuickActions 等）由调用方在返回对象上按需赋值，缺省时对应功能自动降级。
// wakeups 取第一个作为 Wakeup（调用侧通常是单例）。
func NewTaskService(q *db.Queries, tx TxStarter, hub *realtime.Hub, bus *events.Bus, wakeups ...TaskWakeupNotifier) *TaskService {
	var wakeup TaskWakeupNotifier
	if len(wakeups) > 0 {
		wakeup = wakeups[0]
	}
	return &TaskService{Queries: q, TxStarter: tx, Hub: hub, Bus: bus, Wakeup: wakeup}
}

// trivialDoneMarkers 是一组“任务已结束”的极短措辞（含俄文/中文），用于识别 agent 只回了“done”这类
// 没有实质内容、不需要回贴为评论的输出。
var trivialDoneMarkers = []string{
	"done",
	"готово",
	"готова",
	"сделано",
	"完成",
	"完了",
}

// isTrivialDoneOutput 判断 agent 输出是否为无正文的“完成”式措辞：去掉首尾标点/空白后与标记表完全相等。
func isTrivialDoneOutput(output string) bool {
	normalized := strings.TrimSpace(strings.ToLower(output))
	normalized = strings.Trim(normalized, ".!！。… ")
	for _, marker := range trivialDoneMarkers {
		if normalized == marker {
			return true
		}
	}
	return false
}

// captureTaskQueued 记录“任务入队”业务指标（来源 / 运行模式）。
func (s *TaskService) captureTaskQueued(ctx context.Context, task db.AgentTaskQueue) {
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskEnqueued(source, runtimeMode)
	}
}

// runtimeMCPOverlayData 携带可选的 per-task MCP overlay 原始 JSON：overlay 本体与 connected-app 元数据。
type runtimeMCPOverlayData struct {
	Overlay       json.RawMessage
	ConnectedApps json.RawMessage
}

// buildRuntimeMCPOverlay 计算可选的 per-task Composio MCP overlay。各 Enqueue* 路径必须在插入排队行【之前】
// 调用它，否则守护进程可能在去 Composio 的网络往返期间抢走任务而漏掉 overlay。
func (s *TaskService) buildRuntimeMCPOverlay(ctx context.Context, originatorUserID pgtype.UUID, agent db.Agent) runtimeMCPOverlayData {
	if s == nil || s.Composio == nil {
		return runtimeMCPOverlayData{}
	}
	if !featureflags.ComposioMCPAppsEnabled(ctx, s.FeatureFlags) {
		return runtimeMCPOverlayData{}
	}
	result, err := s.Composio.BuildTaskOverlay(ctx, originatorUserID, agent)
	if err != nil {
		slog.Warn("runtime mcp overlay: BuildTaskOverlay failed; task will run without composio overlay",
			"originator_user_id", util.UUIDToString(originatorUserID),
			"agent_id", util.UUIDToString(agent.ID),
			"error", err,
		)
		return runtimeMCPOverlayData{}
	}
	if len(result.MCPOverlay) == 0 {
		slog.Debug("runtime mcp overlay: no composio overlay for task",
			"originator_user_id", util.UUIDToString(originatorUserID),
			"agent_id", util.UUIDToString(agent.ID),
		)
		return runtimeMCPOverlayData{}
	}
	data := runtimeMCPOverlayData{Overlay: result.MCPOverlay}
	if len(result.ConnectedApps) > 0 {
		raw, err := json.Marshal(result.ConnectedApps)
		if err != nil {
			slog.Warn("runtime mcp overlay: marshal connected app metadata failed",
				"originator_user_id", util.UUIDToString(originatorUserID),
				"agent_id", util.UUIDToString(agent.ID),
				"error", err,
			)
			return data
		}
		data.ConnectedApps = raw
	}
	return data
}

// resolveOriginatorFromTriggerComment 返回触发某 Enqueue* 路径的评论其链路顶端的“人类用户”ID。
// 链路规则（MUL-3869）：
//
//   - 触发评论由成员撰写 → originator = author_id（该成员就是顶端人类）。
//   - 触发评论由 agent 撰写 → 经 comment.source_task_id 读父任务，继承其 originator_user_id。这是 agent
//     扇出的关键场景：agent A @提及 agent B，评论作者是 A，但必须浮出最初让 A 干活的人类，不能在第一个
//     agent 跳点处丢失。
//   - 评论缺失 / source task 未知 / 父 originator 为 NULL → 无效 pgtype.UUID。BuildTaskOverlay 把它视作
//     “无 overlay”（门槛 1）。
//
// nil 接收者 / nil Queries 时落到无效值，保证没接 DB 的单测配置安全。workspaceID 把评论查询限定在任务
// 工作区，外部评论 UUID 无法解析出其它租户的发起人（MUL-4252）。
func (s *TaskService) resolveOriginatorFromTriggerComment(ctx context.Context, workspaceID, commentID pgtype.UUID) pgtype.UUID {
	// originator 的“值”与 agent 撰写的来源标签无关，这里任何标签都可用；传 comment_source 仅为占位。
	return s.attributionFromTriggerComment(ctx, workspaceID, commentID, attribution.SourceCommentSource).UserID
}

// attributionFromTriggerComment 解析评论触发运行的完整 attribution（负责人类 + 来源标签 + 委托血缘 + evidence）。
// 它只做 DB 读取，再把收集到的事实交给纯函数 attribution.ClassifyComment，让分类保持无副作用且可单测。
// 返回的 UserID 与 MUL-4302 之前的 originator 解析结果逐字节一致，因此授权行为（Composio overlay、
// canInvokeAgent A2A 闸门）不变。workspaceID 把评论查询限定在任务工作区（MUL-4252）。
//
// agentAuthoredSource 为“agent 撰写触发评论”的场景选择标签：issue-指派者回复路径用
// attribution.SourceCommentSource；显式 mention / thread-parent / squad-leader 路径用 attribution.SourceDelegation。
func (s *TaskService) attributionFromTriggerComment(ctx context.Context, workspaceID, commentID pgtype.UUID, agentAuthoredSource attribution.Source) attribution.Result {
	if s == nil || s.Queries == nil || !commentID.Valid {
		return attribution.Result{Source: attribution.SourceUnattributed}
	}
	comment, err := s.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID:          commentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return attribution.Result{Source: attribution.SourceUnattributed}
	}
	return s.attributionFromComment(ctx, comment, agentAuthoredSource)
}

// attributionFromComment 用一条已加载好的触发评论来分类运行，让已有该行数据（例如要读 author_type）的调用方
// 不必重查一次。行为与 attributionFromTriggerComment 之前的内联逻辑逐字节一致，授权行为不变。
func (s *TaskService) attributionFromComment(ctx context.Context, comment db.Comment, agentAuthoredSource attribution.Source) attribution.Result {
	facts := attribution.CommentFacts{
		CommentID:  comment.ID,
		AuthorType: comment.AuthorType,
		AuthorID:   comment.AuthorID,
	}
	// agent 撰写的评论：沿 comment.source_task_id → 父任务 → 父 originator_user_id 上溯（迁移 120 起每条
	// agent 写评论路径都会写入该列）。source_task 缺失 / NULL 时 ParentOriginator 无效，ClassifyComment 会
	// 把它映射为 unattributed。
	if comment.AuthorType == "agent" && comment.SourceTaskID.Valid {
		facts.SourceTaskID = comment.SourceTaskID
		if parent, err := s.Queries.GetAgentTask(ctx, comment.SourceTaskID); err == nil {
			facts.ParentOriginator = parent.OriginatorUserID
			facts.ParentAccountable = parent.AccountableUserID
		}
	}
	return attribution.ClassifyComment(facts, agentAuthoredSource)
}

// resolveOriginatorForIssueTask 返回 issue 型派发的顶端人类。评论触发的运行沿用评论链语义；直接指派/创建
// 回退到 issue 的成员创建者。带显式任务来源链的 agent 创建 issue——quick_create（daemon quick-create 流程）
// 或 agent_create（agent 普通 `issue create`，MUL-4305）——继承来源任务的 originator，因为 origin_id 指向
// 创建该 issue 的 agent_task_queue 行。其它 agent/系统来源（含 autopilot）刻意保持 unattributed。
func (s *TaskService) resolveOriginatorForIssueTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID) pgtype.UUID {
	return s.attributionForIssueTask(ctx, issue, triggerCommentID, attribution.SourceCommentSource, pgtype.UUID{}).UserID
}

// attributionForIssueTask 解析 issue 型入队的完整 attribution。评论触发的运行沿用评论链语义；直接指派/创建
// 回退到 issue 的成员创建者；agent 创建的 quick-create issue 以委托方式继承来源任务的人类。accountable 值
// 与 resolveOriginatorForIssueTask 逐字节一致（后者现在委托到这里），形成单一事实来源且不影响授权。
// agentAuthoredSource 标注 agent 撰写触发评论的场景（见 attributionFromComment）。
func (s *TaskService) attributionForIssueTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, agentAuthoredSource attribution.Source, actorUserID pgtype.UUID) attribution.Result {
	// 成员直接操作既是 accountable 也是 originator，优先级高于一切触发评论 / origin / rule（MUL-4302 §4/§5）。
	// 覆盖指派/提升、手动 autopilot 触发与手动 rerun——其中 rerun 可能为 daemon 的 prompt 上下文继承一条
	// triggerCommentID，但必须归因到点击 rerun 的成员，而非原评论背后的人类。因此先判 actor，再走
	// 评论 / origin 分支。
	if actorUserID.Valid {
		return attribution.ClassifyDirect(attribution.DirectFacts{IssueID: issue.ID, ActorUserID: actorUserID})
	}
	if triggerCommentID.Valid {
		if s == nil || s.Queries == nil {
			return attribution.Result{Source: attribution.SourceUnattributed}
		}
		// 限定工作区查询：外部评论 UUID 无法解析出其它租户的人类（MUL-4252）。
		comment, err := s.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
			ID:          triggerCommentID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return attribution.Result{Source: attribution.SourceUnattributed}
		}
		// 成员/agent 的触发评论能解析出人类（direct_human / delegation / comment_source）。而 SYSTEM 撰写的
		// 评论——目前是 Stage 完成时的“子任务完成”评论（issue_child_done.go），它唤醒父 assignee 且不带
		// actor——不含人类，也不属于任何委托链。直接分类它会一路降级到 owner_fallback（agent 自己的 owner），
		// 这对 Stage 级联是错的：被唤醒的运行应向“谁导致了父 issue 存在”负责。因此 system 评论跳过评论分支，
		// 落到下方父 issue 自己的溯源链——与直接入队解析的同一套 creator / agent_create-origin / autopilot-origin
		// 逻辑——只有该溯源本身也没有人类时才到 owner_fallback（MUL-4302；由 Bohan 在 stage-cascade 兜底上提出）。
		if comment.AuthorType != "system" {
			return s.attributionFromComment(ctx, comment, agentAuthoredSource)
		}
	}
	// autopilot 来源的 issue（origin_id 为 autopilot id），由 schedule / webhook 触发：没有人类授权这次运行，
	// originator 保持 NULL，但要向“当前对触发该 run 的规则生效配置负责”的人类（创建者，随后是最后一位实质
	// 编辑者）负责——即 trigger_owner（MUL-4302；Elon 必修），找不到该成员时降级到规则发布者。解析方式与
	// run_only 派发一致，使两种 autopilot 执行模式归因相同。（手动触发带 actor，已在上面处理。）issue 只存
	// autopilot id，所以要桥接 issue → 活跃 run → trigger_id 才能找到 trigger。
	if s != nil && s.Queries != nil && issue.OriginType.Valid &&
		issue.OriginType.String == "autopilot" && issue.OriginID.Valid {
		var triggerID pgtype.UUID
		if run, err := s.Queries.GetAutopilotRunByIssue(ctx, issue.ID); err == nil {
			triggerID = run.TriggerID
		}
		return triggerOwnerAttribution(ctx, s.Queries, triggerID, issue.WorkspaceID, issue.OriginID, attribution.EvidenceIssueAssignment, issue.ID)
	}
	facts := attribution.DirectFacts{
		IssueID:     issue.ID,
		CreatorType: issue.CreatorType,
		CreatorID:   issue.CreatorID,
	}
	// 成员创建的 issue 无需 DB 读即可解析。只有带来源链的 agent 创建 issue（quick_create、agent_create）才
	// 需要加载来源任务来继承其人类，且仅在 DB 可用时（nil Queries 让单测配置安全并产出 unattributed）。两种
	// 来源类型都用“创建该 issue 的 agent_task_queue 行”写 origin_id，因此顶端人类就是该任务的
	// originator_user_id（MUL-4305）。
	if !(issue.CreatorType == "member" && issue.CreatorID.Valid) &&
		s != nil && s.Queries != nil && issue.OriginType.Valid && issue.OriginID.Valid &&
		(issue.OriginType.String == "quick_create" || issue.OriginType.String == "agent_create") {
		facts.OriginType = issue.OriginType.String
		facts.OriginTaskID = issue.OriginID
		if task, err := s.Queries.GetAgentTask(ctx, issue.OriginID); err == nil {
			facts.OriginOriginator = task.OriginatorUserID
			facts.OriginAccountable = task.AccountableUserID
		}
	}
	return attribution.ClassifyDirect(facts)
}

// ruleOwnerAttribution 从 autopilot 当前生效（最新）规则版本快照解析运行的 rule_owner attribution（MUL-4302 §3.4）。
// 两种 autopilot 执行模式共用——run_only 派发与 create_issue 入队——保证归因一致。originator 保持 NULL
// （autopilot 不携带人类授权）；只设置审计负责方，即该版本规则的成员发布者。版本缺失（本功能上线前发布的
// autopilot，或还没有版本）或发布者非成员/不存在时降级为 unattributed，绝不编造人类。永不返回错误：
// attribution 不能拖垮入队，降级标签是诚实的兜底。
func ruleOwnerAttribution(ctx context.Context, q *db.Queries, workspaceID, autopilotID pgtype.UUID, evidenceKind attribution.EvidenceKind, evidenceRefID pgtype.UUID) attribution.Result {
	if q == nil || !autopilotID.Valid {
		return attribution.RuleOwner(pgtype.UUID{}, pgtype.UUID{}, evidenceKind, evidenceRefID)
	}
	ver, err := q.GetActiveAutopilotRuleVersion(ctx, db.GetActiveAutopilotRuleVersionParams{
		WorkspaceID: workspaceID,
		AutopilotID: autopilotID,
	})
	if err != nil {
		return attribution.RuleOwner(pgtype.UUID{}, pgtype.UUID{}, evidenceKind, evidenceRefID)
	}
	var publisher pgtype.UUID
	if ver.PublishedByType == "member" {
		publisher = ver.PublishedByID
	}
	return attribution.RuleOwner(publisher, ver.ID, evidenceKind, evidenceRefID)
}

// triggerOwnerAttribution 把 autopilot schedule/webhook 运行解析到“当前对该触发规则生效配置负责”的人类
// （MUL-4302；Bohan + Elon 必修）。triggerID 来自 autopilot_run.trigger_id。trigger 行的 published_by 初始为
// 创建者，之后转移给实质编辑者，因此运行归因到“最后塑造它的人”而非最初创建者。没有记录发布者的 trigger
// （早于本迁移）或发布者为 agent 时，降级到 ruleOwnerAttribution（先规则发布者，再 owner_fallback）——即
// autopilot 此前的粗粒度行为，无回归。永不报错：attribution 不能拖垮入队。
func triggerOwnerAttribution(ctx context.Context, q *db.Queries, triggerID, workspaceID, autopilotID pgtype.UUID, evidenceKind attribution.EvidenceKind, evidenceRefID pgtype.UUID) attribution.Result {
	if q != nil && triggerID.Valid {
		// published_by 是当前对这条 trigger 生效配置负责的成员：创建者直到有人实质编辑（该 trigger 的 cron/过滤/
		// webhook，或把其全部 trigger 一起抬升的 autopilot 级变更），之后是编辑者。因此运行归因到“最后塑造
		// 它的人”而不是最初创建者——编辑其它 trigger 永远不会移动这一条（MUL-4302；Elon 必修）。
		if trig, err := q.GetAutopilotTrigger(ctx, triggerID); err == nil &&
			trig.PublishedByType.Valid && trig.PublishedByType.String == "member" && trig.PublishedByID.Valid {
			return attribution.TriggerOwner(trig.PublishedByID, evidenceKind, evidenceRefID)
		}
	}
	return ruleOwnerAttribution(ctx, q, workspaceID, autopilotID, evidenceKind, evidenceRefID)
}

// ErrAttributionFailClosed 表示一次运行解析不出精确的负责人类、入队被【拒绝】而非启动。覆盖三种情况，都
// 意味着“无法保证这次运行有可负责的人类”（MUL-4302 §1/§3.5）：工作区启用了 fail-closed；工作区策略读不到
// （无法确认允许兜底——宁可关停也不跑）；或 owner_fallback 没有 agent owner 可回退。入队路径上抛它，保证
// 运行绝不启动。
var ErrAttributionFailClosed = errors.New("attribution: no precise accountable human and enqueue refused (fail-closed policy, policy read failed, or no agent owner)")

// ErrDuplicatePendingTask 表示一次全新入队输给了并发的另一次：同一 (issue, agent) 已有 queued/dispatched
// 任务，pending-task 唯一索引拒绝了插入（#5914）。这是良性结果——已有兄弟运行覆盖该目标——而非服务故障。
// 入队路径返回它，让调用方上报“已合并”形态的成功 / 结构化 409，而不是把原生 Postgres 约束当 500。它以裸
// 形式返回（原始驱动文本、含索引名，只在 debug 级别记一次日志且绝不包装），上层日志/响应不会泄露约束名
// （#5914，Elon review）。
var ErrDuplicatePendingTask = errors.New("a pending task for this issue and agent already exists")

// isDuplicatePendingTaskErr 判断 err 是否为 pending-task 唯一索引冲突（并发入队抢跑）。同时接受新旧两个
// 索引名，兼容滚动部署期间 v1/v2 共存。
func isDuplicatePendingTaskErr(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	switch pgErr.ConstraintName {
	case "idx_one_pending_task_per_issue_agent", "idx_one_pending_task_per_issue_agent_v2":
		return true
	default:
		return false
	}
}

// pendingSlotTakenErr 判断 err 是否表示“入队时该 (issue, agent) 的 pending 槽位已被占用”。
//
// RerunIssue 会收到两种形态：issue-指派路径抛原始唯一约束冲突；enqueueMentionTaskWithCommentPlan 把它
// 规范化为裸的 ErrDuplicatePendingTask 哨兵——而目标不是 issue 当前 agent 指派者的每次 rerun（群组长、
// 被 task_id 重新触发的顶替 agent、被 mention 的 agent）都走这条路径。只匹配原始 pgconn 错误会让这些场景
// 不触发回收，系统重试抢到槽位就会被当成硬错误上抛。
func pendingSlotTakenErr(err error) bool {
	return isDuplicatePendingTaskErr(err) || errors.Is(err, ErrDuplicatePendingTask)
}

// applyAttributionFallback 把工作区的“降级归因”策略应用到一个解析为 unattributed（无精确人类）的结果上。
// PRECISE 结果原样通过（完全不读策略）。对 unattributed 运行，accountable 绝不为空的保证按 fail-closed
// 强制——绝不清零静默入队一条 accountable_user_id 为 NULL 的任务：
//
//   - 策略读取失败（或无工作区）→ 拒绝。瞬时 DB 抖动时无法确认工作区允许兜底，就不跑不可归因任务。
//     （只有罕见的 unattributed 路径才付出这次读取；精确运行从不读。）
//   - fail-closed 工作区 → 拒绝。
//   - 其它情况 → owner_fallback（accountable = agent owner，仅审计，originator 不动）。若没有有效 agent owner，
//     owner_fallback 仍为 unattributed → 拒绝，绝不入队 NULL-accountable 任务。
//
// 把它放在入队边界（而非纯分类器内），是因为 owner_fallback 需要 agent owner，而每条入队路径手头都有。
func (s *TaskService) applyAttributionFallback(ctx context.Context, attr attribution.Result, agent db.Agent) (attribution.Result, error) {
	if attr.Source != attribution.SourceUnattributed {
		return attr, nil
	}
	if s == nil || s.Queries == nil || !agent.WorkspaceID.Valid {
		return attr, fmt.Errorf("%w: workspace policy unavailable", ErrAttributionFailClosed)
	}
	failClosed, err := s.Queries.GetWorkspaceAttributionFailClosed(ctx, agent.WorkspaceID)
	if err != nil {
		// 无法确认工作区允许兜底 → fail closed，而不是静默运行一条不可归因的任务。
		return attr, fmt.Errorf("%w: policy read failed: %v", ErrAttributionFailClosed, err)
	}
	if failClosed {
		return attr, ErrAttributionFailClosed
	}
	fallback := attribution.OwnerFallback(attr, agent.OwnerID)
	if fallback.Source == attribution.SourceUnattributed {
		// owner_fallback 解析不出负责人类（无有效 agent owner）：拒绝，绝不入队 NULL-accountable 任务。
		return attr, fmt.Errorf("%w: no agent owner to attribute", ErrAttributionFailClosed)
	}
	return fallback, nil
}

// attributionCreateParams 把解析出的 attribution 映射到 CreateAgentTask 的溯源列。originator_source 总是
// 写入（新行绝不为 NULL）；委托血缘与 evidence 仅在存在时写入。
func attributionCreateParams(attr attribution.Result) (source pgtype.Text, delegatedFrom pgtype.UUID, evidenceKind pgtype.Text, evidenceRef pgtype.UUID) {
	source = pgtype.Text{String: attr.Source.String(), Valid: true}
	delegatedFrom = attr.DelegatedFromTaskID
	evidenceKind = pgtype.Text{String: string(attr.EvidenceKind), Valid: attr.EvidenceKind != ""}
	evidenceRef = attr.EvidenceRefID
	return
}

// OriginatorForIssueTask 把 resolveOriginatorForIssueTask 暴露给 service 包之外（handler 层的群组长访问
// 闸门），让闸门用与入队路径持久化在任务行上完全相同的解析来判定顶端人类。没有共享入口时，agent 触发的
// 指派在闸门处看到空 originator，会误拒本可正确归因的私有群组长（MUL-4305）。
func (s *TaskService) OriginatorForIssueTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID) pgtype.UUID {
	return s.resolveOriginatorForIssueTask(ctx, issue, triggerCommentID)
}

// captureTaskDispatched 记录“任务已派发”的 trace 事件与业务指标。
func (s *TaskService) captureTaskDispatched(ctx context.Context, task db.AgentTaskQueue) {
	s.recordTaskTraceEvent(ctx, task, "task.dispatched", "任务已领取", taskTraceOptions{QueueWaitMs: durationMilliseconds(task.CreatedAt, task.DispatchedAt)})
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskDispatched(util.UUIDToString(task.ID), source, runtimeMode, taskQueueWaitSeconds(task))
	}
}

// AnalyticsContextForTask 返回任务的分析上下文（含工作区/来源/运行模式，优先命中缓存）。
func (s *TaskService) AnalyticsContextForTask(ctx context.Context, task db.AgentTaskQueue) analytics.TaskContext {
	return s.taskAnalyticsContext(ctx, task)
}

// captureTaskStarted 记录“任务已开始”的 trace 事件与业务指标。
func (s *TaskService) captureTaskStarted(ctx context.Context, task db.AgentTaskQueue) {
	s.recordTaskTraceEvent(ctx, task, "task.started", "任务已开始", taskTraceOptions{QueueWaitMs: durationMilliseconds(task.CreatedAt, task.DispatchedAt)})
	if s.Metrics != nil {
		source, runtimeMode, provider := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskStarted(source, runtimeMode, provider)
	}
}

// captureTaskCompleted 记录“任务已完成”的 trace 事件与终态指标。
func (s *TaskService) captureTaskCompleted(ctx context.Context, task db.AgentTaskQueue) {
	runMs := durationMilliseconds(task.StartedAt, task.CompletedAt)
	s.recordTaskTraceEvent(ctx, task, "task.completed", "任务已完成", taskTraceOptions{DurationMs: runMs, RunMs: runMs, TotalMs: durationMilliseconds(task.CreatedAt, task.CompletedAt), QueueWaitMs: durationMilliseconds(task.CreatedAt, task.DispatchedAt)})
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskTerminal(util.UUIDToString(task.ID), source, runtimeMode, task.Status, taskRunSeconds(task), taskTotalSeconds(task), task.Attempt)
	}
}

// captureTaskFailed 记录“任务已失败”的 trace 事件与指标（含失败原因/错误类型归类）。
func (s *TaskService) captureTaskFailed(ctx context.Context, task db.AgentTaskQueue) {
	failureReason := taskFailureReason(task)
	runMs := durationMilliseconds(task.StartedAt, task.CompletedAt)
	s.recordTaskTraceEvent(ctx, task, "task.failed", "任务已失败", taskTraceOptions{DurationMs: runMs, RunMs: runMs, TotalMs: durationMilliseconds(task.CreatedAt, task.CompletedAt), QueueWaitMs: durationMilliseconds(task.CreatedAt, task.DispatchedAt), FailureReason: failureReason, ErrorType: taskErrorType(failureReason)})
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskTerminal(util.UUIDToString(task.ID), source, runtimeMode, task.Status, taskRunSeconds(task), taskTotalSeconds(task), task.Attempt)
		s.Metrics.RecordTaskFailed(source, runtimeMode, failureReason)
	}
}

// captureTaskCancelled 记录“任务已取消”的 trace 事件与终态指标，并顺带撤销该任务的 mat_ 任务 token。
func (s *TaskService) captureTaskCancelled(ctx context.Context, task db.AgentTaskQueue) {
	s.recordTaskTraceEvent(ctx, task, "task.cancelled", "任务已取消", taskTraceOptions{FailureReason: "cancelled", ErrorType: "cancelled", TotalMs: durationMilliseconds(task.CreatedAt, task.CompletedAt)})
	if s.Metrics != nil {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskTerminal(util.UUIDToString(task.ID), source, runtimeMode, task.Status, taskRunSeconds(task), taskTotalSeconds(task), task.Attempt)
	}
	// 撤销为任务铸造的 mat_ token。取消是终态转换，运行中的 agent 进程不再需要回调；提前删除可收窄
	// “被入侵进程仍能在 24h 过期前持续向 API 鉴权”的时间窗。失败非致命——过期与 FK 级联是持久防线。MUL-2600。
	if err := s.Queries.DeleteTaskTokensByTask(ctx, task.ID); err != nil {
		slog.Warn("cancel task: failed to revoke task tokens",
			"task_id", util.UUIDToString(task.ID), "error", err)
	}
}

// CaptureTaskUsage 上报一次 LLM 调用的 token 用量与费用。costUSDTicks 是 provider 自报价格（单位
// 1e-10 美元），未上报时为 0——指标层优先采用它而不是自己的费率表。
func (s *TaskService) CaptureTaskUsage(ctx context.Context, task db.AgentTaskQueue, provider, model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, costUSDTicks int64) {
	if s.Metrics == nil {
		return
	}
	source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
	s.Metrics.RecordLLMUsage(source, runtimeMode, provider, model, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, costUSDTicks)
}

// CaptureQueuedExpiredTasks 批量记录“排队超期被清掉”的任务指标。
func (s *TaskService) CaptureQueuedExpiredTasks(ctx context.Context, tasks []db.AgentTaskQueue) {
	if s.Metrics == nil {
		return
	}
	for _, task := range tasks {
		source, runtimeMode, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskQueuedExpired(source, runtimeMode)
	}
}

// CaptureLeaseExpiredTasks 批量记录“租约到期”的任务指标。
func (s *TaskService) CaptureLeaseExpiredTasks(ctx context.Context, tasks []db.AgentTaskQueue) {
	if s.Metrics == nil {
		return
	}
	for _, task := range tasks {
		source, _, _ := s.taskMetricsContext(ctx, task)
		s.Metrics.RecordTaskLeaseExpired(source)
	}
}

// cachedTaskAnalyticsContext 按缓存键读取任务的分析上下文；缓存未初始化或未命中返回 false。
func (s *TaskService) cachedTaskAnalyticsContext(task db.AgentTaskQueue) (analytics.TaskContext, bool) {
	key := taskAnalyticsContextKey(task)
	if key == "" {
		return analytics.TaskContext{}, false
	}
	s.analyticsContextMu.Lock()
	defer s.analyticsContextMu.Unlock()
	if s.analyticsContextCache == nil {
		return analytics.TaskContext{}, false
	}
	tc, ok := s.analyticsContextCache[key]
	return tc, ok
}

// storeTaskAnalyticsContext 把任务分析上下文写入缓存；超过 taskAnalyticsContextCacheMax 条时按插入顺序
// 淘汰最旧（简单 FIFO 式 LRU）。工作区为空的任务不入缓存。
func (s *TaskService) storeTaskAnalyticsContext(task db.AgentTaskQueue, tc analytics.TaskContext) {
	if tc.WorkspaceID == "" {
		return
	}
	key := taskAnalyticsContextKey(task)
	if key == "" {
		return
	}
	s.analyticsContextMu.Lock()
	defer s.analyticsContextMu.Unlock()
	if s.analyticsContextCache == nil {
		s.analyticsContextCache = make(map[string]analytics.TaskContext)
	}
	if _, ok := s.analyticsContextCache[key]; !ok {
		s.analyticsContextOrder = append(s.analyticsContextOrder, key)
		if len(s.analyticsContextOrder) > taskAnalyticsContextCacheMax {
			oldest := s.analyticsContextOrder[0]
			s.analyticsContextOrder = s.analyticsContextOrder[1:]
			delete(s.analyticsContextCache, oldest)
		}
	}
	s.analyticsContextCache[key] = tc
}

// taskAnalyticsContextKey 由任务身份字段拼接缓存键；任务 ID 无效时返回空键（调用方据此不入缓存）。
func taskAnalyticsContextKey(task db.AgentTaskQueue) string {
	taskID := util.UUIDToString(task.ID)
	if taskID == "" {
		return ""
	}
	return strings.Join([]string{
		taskID,
		util.UUIDToString(task.RuntimeID),
		util.UUIDToString(task.IssueID),
		util.UUIDToString(task.ChatSessionID),
		util.UUIDToString(task.AutopilotRunID),
	}, "|")
}

// taskMetricsContext 计算任务归属的指标 source（issue/chat/autopilot/quick_create/other）、运行模式与 provider。
func (s *TaskService) taskMetricsContext(ctx context.Context, task db.AgentTaskQueue) (source, runtimeMode, provider string) {
	tc := s.taskAnalyticsContext(ctx, task)
	source = "other"
	switch {
	case task.ChatSessionID.Valid:
		source = "chat"
	case task.IssueID.Valid:
		if tc.Source == analytics.SourceAutopilot {
			source = "autopilot_issue"
		} else {
			source = "issue"
		}
	case task.AutopilotRunID.Valid:
		source = "autopilot"
	default:
		if _, ok := s.parseQuickCreateContext(task); ok {
			source = "quick_create"
		} else if tc.Source != "" {
			source = tc.Source
		}
	}
	return source, tc.RuntimeMode, tc.Provider
}

// taskAnalyticsContext 组装任务的分析上下文：先查缓存，未命中则依次用 runtime/agent/issue/chat-session/
// autopilot-run/quick-create 信息回填工作区、来源与发起用户，最后写回缓存。
func (s *TaskService) taskAnalyticsContext(ctx context.Context, task db.AgentTaskQueue) analytics.TaskContext {
	if tc, ok := s.cachedTaskAnalyticsContext(task); ok {
		return tc
	}
	tc := analytics.TaskContext{
		AgentID: util.UUIDToString(task.AgentID),
		TaskID:  util.UUIDToString(task.ID),
		Source:  analytics.SourceManual,
	}
	if task.IssueID.Valid {
		tc.IssueID = util.UUIDToString(task.IssueID)
	}
	if task.ChatSessionID.Valid {
		tc.ChatSessionID = util.UUIDToString(task.ChatSessionID)
		tc.Source = analytics.SourceChat
	}
	if task.AutopilotRunID.Valid {
		tc.AutopilotRunID = util.UUIDToString(task.AutopilotRunID)
		tc.Source = analytics.SourceAutopilot
	}

	if task.RuntimeID.Valid {
		if rt, err := s.runtimeLookup().Get(ctx, task.RuntimeID); err == nil {
			tc.WorkspaceID = util.UUIDToString(rt.WorkspaceID)
			tc.RuntimeMode = rt.RuntimeMode
			tc.Provider = rt.Provider
		}
	}
	if tc.WorkspaceID == "" || tc.RuntimeMode == "" {
		if agent, err := s.Queries.GetAgent(ctx, task.AgentID); err == nil {
			if tc.WorkspaceID == "" {
				tc.WorkspaceID = util.UUIDToString(agent.WorkspaceID)
			}
			if tc.RuntimeMode == "" {
				tc.RuntimeMode = agent.RuntimeMode
			}
		}
	}

	if task.IssueID.Valid {
		if issue, err := s.Queries.GetIssue(ctx, task.IssueID); err == nil {
			tc.WorkspaceID = util.UUIDToString(issue.WorkspaceID)
			if issue.CreatorType == "member" {
				tc.UserID = util.UUIDToString(issue.CreatorID)
			}
			if issue.OriginType.Valid {
				switch issue.OriginType.String {
				case "autopilot":
					tc.Source = analytics.SourceAutopilot
					if ap, err := s.Queries.GetAutopilot(ctx, issue.OriginID); err == nil {
						if ap.CreatedByType == "member" {
							tc.UserID = util.UUIDToString(ap.CreatedByID)
						}
					}
				case "quick_create":
					tc.Source = analytics.SourceManual
				}
			}
		}
	}
	if task.ChatSessionID.Valid {
		if cs, err := s.Queries.GetChatSession(ctx, task.ChatSessionID); err == nil {
			tc.WorkspaceID = util.UUIDToString(cs.WorkspaceID)
			tc.UserID = util.UUIDToString(cs.CreatorID)
		}
	}
	if task.AutopilotRunID.Valid {
		if run, err := s.Queries.GetAutopilotRun(ctx, task.AutopilotRunID); err == nil {
			if ap, err := s.Queries.GetAutopilot(ctx, run.AutopilotID); err == nil {
				tc.WorkspaceID = util.UUIDToString(ap.WorkspaceID)
				if ap.CreatedByType == "member" {
					tc.UserID = util.UUIDToString(ap.CreatedByID)
				}
			}
		}
	}
	if qc, ok := s.parseQuickCreateContext(task); ok {
		tc.WorkspaceID = qc.WorkspaceID
		tc.UserID = qc.RequesterID
		tc.Source = analytics.SourceManual
	}
	s.storeTaskAnalyticsContext(task, tc)
	return tc
}

// taskQueueWaitSeconds 返回任务排队等待秒数（created→dispatched）。
func taskQueueWaitSeconds(task db.AgentTaskQueue) float64 {
	return durationSeconds(task.CreatedAt, task.DispatchedAt)
}

// taskRunSeconds 返回任务实际运行秒数（started→completed）。
func taskRunSeconds(task db.AgentTaskQueue) float64 {
	return durationSeconds(task.StartedAt, task.CompletedAt)
}

// taskTotalSeconds 返回任务全生命周期秒数（created→completed）。
func taskTotalSeconds(task db.AgentTaskQueue) float64 {
	return durationSeconds(task.CreatedAt, task.CompletedAt)
}

// durationSeconds 返回两个时间点之差（秒）；任一端无效返回 -1，差值为负按 0 计。
func durationSeconds(start, end pgtype.Timestamptz) float64 {
	if !start.Valid || !end.Valid {
		return -1
	}
	seconds := end.Time.Sub(start.Time).Seconds()
	if seconds < 0 {
		return 0
	}
	return seconds
}

// taskFailureReason 返回任务的失败原因，未记录时默认 agent_error。
func taskFailureReason(task db.AgentTaskQueue) string {
	if task.FailureReason.Valid && task.FailureReason.String != "" {
		return task.FailureReason.String
	}
	return "agent_error"
}

// taskErrorType 把失败原因归为指标用的错误类型：runtime_offline/recovery→runtime，超时类→timeout，
// 迭代上限/兜底消息→agent_output，取消类→cancelled，其余→agent_error。
func taskErrorType(reason string) string {
	switch reason {
	case "runtime_offline", "runtime_recovery":
		return "runtime"
	case "timeout", "codex_semantic_inactivity":
		return "timeout"
	case "iteration_limit", "agent_fallback_message":
		return "agent_output"
	case "cancelled", "user_cancelled":
		return "cancelled"
	default:
		return "agent_error"
	}
}

// EnqueueTaskForIssue 为“已指派给 agent 的 issue”创建排队任务。不存上下文快照——agent 运行时经 multica CLI
// 自取所需数据。triggerCommentID 可选：评论触发场景传入（取第一个有效值）。
func (s *TaskService) EnqueueTaskForIssue(ctx context.Context, issue db.Issue, triggerCommentID ...pgtype.UUID) (db.AgentTaskQueue, error) {
	var commentID pgtype.UUID
	if len(triggerCommentID) > 0 {
		commentID = triggerCommentID[0]
	}
	return s.enqueueIssueTask(ctx, issue, commentID, false, "", pgtype.UUID{}, pgtype.UUID{}, pgtype.Timestamptz{})
}

// EnqueueDeferredChannelIssueTask 为媒体版频道 /issue 回合持久化指派任务，但暂不可认领。fireAt 截止时间是
// 崩溃安全的兜底；频道路由在分离的附件事务落定后会尽快提升该任务。
func (s *TaskService) EnqueueDeferredChannelIssueTask(ctx context.Context, issue db.Issue, fireAt time.Time) (db.AgentTaskQueue, error) {
	return s.enqueueIssueTask(ctx, issue, pgtype.UUID{}, false, "", pgtype.UUID{}, pgtype.UUID{}, pgtype.Timestamptz{Time: fireAt, Valid: true})
}

// createDeferredChannelIssueTaskWithQueries 通过调用方的事务句柄插入这个受媒体门控的惰性任务。IssueService
// 传入事务内 Queries，使 issue 与任务原子可见。刻意不让事务内的 service 触碰 Composio：任务处于 deferred
// 时不会被认领，可选的对外 overlay 在提交后水合，避免跨网络调用期间持有数据库锁。
func (s *TaskService) createDeferredChannelIssueTaskWithQueries(ctx context.Context, q *db.Queries, issue db.Issue, fireAt time.Time) (db.AgentTaskQueue, error) {
	txService := &TaskService{Queries: q}
	return txService.enqueueIssueTask(ctx, issue, pgtype.UUID{}, false, "", pgtype.UUID{}, pgtype.UUID{}, pgtype.Timestamptz{Time: fireAt, Valid: true})
}

// hydrateDeferredChannelIssueTaskOverlay 在 issue+任务的事务提交后补填可选 Composio overlay。条件更新拒绝
// 覆盖“提交后竞态中被评论合并赢得、并已用匹配 overlay 重新归因过”的任务。
func (s *TaskService) hydrateDeferredChannelIssueTaskOverlay(ctx context.Context, task db.AgentTaskQueue) error {
	if s == nil || s.Queries == nil || s.Composio == nil || !featureflags.ComposioMCPAppsEnabled(ctx, s.FeatureFlags) {
		return nil
	}
	agent, err := s.Queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		return fmt.Errorf("load agent for deferred channel issue task overlay: %w", err)
	}
	overlay := s.buildRuntimeMCPOverlay(ctx, task.OriginatorUserID, agent)
	if len(overlay.Overlay) == 0 {
		return nil
	}
	updated, err := s.Queries.SetDeferredChannelIssueTaskRuntimeOverlay(ctx, db.SetDeferredChannelIssueTaskRuntimeOverlayParams{
		ID:                       task.ID,
		RuntimeMcpOverlay:        overlay.Overlay,
		RuntimeConnectedApps:     overlay.ConnectedApps,
		ExpectedOriginatorUserID: task.OriginatorUserID,
	})
	if err != nil {
		return fmt.Errorf("set deferred channel issue task overlay: %w", err)
	}
	if updated == 0 {
		slog.Debug("deferred channel issue task overlay skipped: task plan changed",
			"task_id", util.UUIDToString(task.ID))
	}
	return nil
}

// EnqueueTaskForIssueWithHandoff 是携带交接说明（MUL-3375）的指派/提升变体。说明走专属任务列，daemon 经
// assignment-handoff 分支渲染。空说明的行为与 EnqueueTaskForIssue 完全一致。actorUserID 是执行指派/提升的
// 成员，成为本次运行的负责人类（MUL-4302 §4）；调用方没有成员 actor 时为无效值。
func (s *TaskService) EnqueueTaskForIssueWithHandoff(ctx context.Context, issue db.Issue, handoffNote string, actorUserID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueIssueTask(ctx, issue, pgtype.UUID{}, false, handoffNote, actorUserID, pgtype.UUID{}, pgtype.Timestamptz{})
}

// ResolveIssueReviewSHA 返回 issue 当前处于评审中的提交 head SHA（其最相关关联 PR 的 head_sha）；issue
// 无关联 PR 时返回空串。调用方把该值同时用于评审循环去重与入队路径，使钉在旧 head 上的 pending review 任务
// 在 HEAD 推进后不再满足新请求（TEN-356）。空串是安全默认：让去重回退到 TEN-356 之前的 (issue_id, agent_id)
// 键，并把任务的 head_sha 列留 NULL。
//
// 查询失败软处理——任何 DB 错误（含“无关联 PR”）都返回空串，瞬时的 github 表故障不会把评审过度去重；
// 最坏只是回到 TEN-356 之前的合并行为。
func (s *TaskService) ResolveIssueReviewSHA(ctx context.Context, issueID pgtype.UUID) string {
	if !issueID.Valid {
		return ""
	}
	sha, err := s.Queries.GetIssueReviewHeadSha(ctx, issueID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("resolve issue review sha failed",
				"issue_id", util.UUIDToString(issueID), "error", err)
		}
		return ""
	}
	return sha
}

// headShaText 把解析出的评审 SHA 包装成去重/入队查询期望的 pgtype.Text。空 SHA 编为无效（NULL）Text，
// 让查询走自己的兜底分支。
func headShaText(sha string) pgtype.Text {
	return pgtype.Text{String: sha, Valid: sha != ""}
}

// ResolveIssueReviewSHAParam 是 ResolveIssueReviewSHA 的 pgtype.Text 包装版本，service 与 handler 两层
// 调用点都能单次调用、按“被评审 head”做去重键（TEN-356）。
func (s *TaskService) ResolveIssueReviewSHAParam(ctx context.Context, issueID pgtype.UUID) pgtype.Text {
	return headShaText(s.ResolveIssueReviewSHA(ctx, issueID))
}

// enqueueIssueTask 是 EnqueueTaskForIssue 与手动 rerun 路径共享的实现入口：forceFreshSession=true 会把任务
// 标记为“新会话”，daemon 认领时跳过 (agent_id, issue_id) 的恢复查找——用户已判定旧输出不佳，全新会话是
// 预期行为。无合并评论计划时收敛到 enqueueIssueTaskWithCommentPlan。
func (s *TaskService) enqueueIssueTask(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, forceFreshSession bool, handoffNote string, actorUserID pgtype.UUID, rerunOfTaskID pgtype.UUID, fireAt pgtype.Timestamptz) (db.AgentTaskQueue, error) {
	return s.enqueueIssueTaskWithCommentPlan(ctx, issue, triggerCommentID, nil, forceFreshSession, handoffNote, actorUserID, rerunOfTaskID, fireAt)
}

// enqueueIssueTaskWithCommentPlan 是 issue 入队的核心实现：加载并校验 agent（无指派/已归档/无 runtime 拒绝），
// 解析 attribution 并执行 fail-closed 兜底，构建 Composio overlay，随后在单事务内插入任务行（fireAt 有效时走
// CreateDeferredChannelIssueTask 并跳过 queued 事件）并记录 queued 事件；提交后按“先广播 queued、再唤醒
// daemon”的顺序发布。coalescedCommentIDs 是合入本任务的评论 ID 列表（摘要以最新评论为准）。
func (s *TaskService) enqueueIssueTaskWithCommentPlan(ctx context.Context, issue db.Issue, triggerCommentID pgtype.UUID, coalescedCommentIDs []pgtype.UUID, forceFreshSession bool, handoffNote string, actorUserID pgtype.UUID, rerunOfTaskID pgtype.UUID, fireAt pgtype.Timestamptz) (db.AgentTaskQueue, error) {
	if !issue.AssigneeID.Valid {
		slog.Error("task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", "issue has no assignee")
		return db.AgentTaskQueue{}, fmt.Errorf("issue has no assignee")
	}

	agent, err := s.Queries.GetAgent(ctx, issue.AssigneeID)
	if err != nil {
		slog.Error("task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		slog.Debug("task enqueue skipped: agent is archived", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agent.ID))
		return db.AgentTaskQueue{}, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		slog.Error("task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", "agent has no runtime")
		return db.AgentTaskQueue{}, fmt.Errorf("agent has no runtime")
	}

	// issue 指派者对 agent 撰写评论的回复属 comment_source attribution（委托的特例）；成员评论或直接指派为
	// direct_human。attr.UserID 与 MUL-4302 之前的解析值相同，overlay/授权行为不变；多出的字段仅供审计溯源。
	attr := s.attributionForIssueTask(ctx, issue, triggerCommentID, attribution.SourceCommentSource, actorUserID)
	// 没有精确人类 → owner_fallback（accountable = agent owner），
	// 若工作区为 fail-closed 则拒绝入队（MUL-4302 §3.5）。
	attr, err = s.applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		slog.Warn("task enqueue refused: attribution fail-closed", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(issue.AssigneeID))
		return db.AgentTaskQueue{}, err
	}
	originatorUserID := attr.UserID
	runtimeMCPOverlay := s.buildRuntimeMCPOverlay(ctx, originatorUserID, agent)
	attrSource, attrDelegatedFrom, attrEvidenceKind, attrEvidenceRef := attributionCreateParams(attr)
	createParams := db.CreateAgentTaskParams{
		ID:                   dbid.NewV7(),
		AgentID:              issue.AssigneeID,
		RuntimeID:            agent.RuntimeID,
		IssueID:              issue.ID,
		Priority:             priorityToInt(issue.Priority),
		TriggerCommentID:     triggerCommentID,
		CoalescedCommentIds:  coalescedCommentIDs,
		TriggerSummary:       s.buildCommentTriggerSummary(ctx, issue.WorkspaceID, triggerCommentID),
		ForceFreshSession:    pgtype.Bool{Bool: forceFreshSession, Valid: forceFreshSession},
		HandoffNote:          pgtype.Text{String: handoffNote, Valid: handoffNote != ""},
		OriginatorUserID:     originatorUserID,
		AccountableUserID:    attr.AccountableUserID,
		RuleVersionID:        attr.RuleVersionID,
		RerunOfTaskID:        rerunOfTaskID,
		RuntimeMcpOverlay:    runtimeMCPOverlay.Overlay,
		RuntimeConnectedApps: runtimeMCPOverlay.ConnectedApps,
		OriginatorSource:     attrSource,
		DelegatedFromTaskID:  attrDelegatedFrom,
		TriggerEvidenceKind:  attrEvidenceKind,
		TriggerEvidenceRefID: attrEvidenceRef,
		// 盖上被评审 head，使去重能区分本次运行与针对新 HEAD 的后续请求（TEN-356）。
		HeadSha: headShaText(s.ResolveIssueReviewSHA(ctx, issue.ID)),
	}
	var task db.AgentTaskQueue
	var queuedEvent events.Event
	err = s.runInTx(ctx, func(qtx *db.Queries) error {
		var createErr error
		if fireAt.Valid {
			task, createErr = qtx.CreateDeferredChannelIssueTask(ctx, db.CreateDeferredChannelIssueTaskParams{
				ID:                   dbid.NewV7(),
				AgentID:              createParams.AgentID,
				RuntimeID:            createParams.RuntimeID,
				IssueID:              createParams.IssueID,
				Priority:             createParams.Priority,
				TriggerCommentID:     createParams.TriggerCommentID,
				CoalescedCommentIds:  createParams.CoalescedCommentIds,
				TriggerSummary:       createParams.TriggerSummary,
				ForceFreshSession:    createParams.ForceFreshSession,
				IsLeaderTask:         createParams.IsLeaderTask,
				HandoffNote:          createParams.HandoffNote,
				SquadID:              createParams.SquadID,
				HeadSha:              createParams.HeadSha,
				OriginatorUserID:     createParams.OriginatorUserID,
				AccountableUserID:    createParams.AccountableUserID,
				RuntimeMcpOverlay:    createParams.RuntimeMcpOverlay,
				RuntimeConnectedApps: createParams.RuntimeConnectedApps,
				OriginatorSource:     createParams.OriginatorSource,
				DelegatedFromTaskID:  createParams.DelegatedFromTaskID,
				RuleVersionID:        createParams.RuleVersionID,
				RerunOfTaskID:        createParams.RerunOfTaskID,
				TriggerEvidenceKind:  createParams.TriggerEvidenceKind,
				TriggerEvidenceRefID: createParams.TriggerEvidenceRefID,
				FireAt:               fireAt,
			})
		} else {
			task, createErr = qtx.CreateAgentTask(ctx, createParams)
		}
		if createErr != nil {
			return createErr
		}
		if !fireAt.Valid && s.TxStarter != nil {
			queuedEvent, createErr = RecordTaskQueuedEventTx(ctx, qtx, util.UUIDToString(issue.WorkspaceID), task)
			if createErr != nil {
				return createErr
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("create task: %w", err)
	}

	slog.Info("task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"agent_id", util.UUIDToString(issue.AssigneeID),
		"force_fresh_session", forceFreshSession,
	)
	if fireAt.Valid {
		return task, nil
	}
	// 顺序重要：先广播，再通知 daemon。notifyTaskAvailable 会触发进程内通道，daemon 经 HTTP 认领后，
	// 认领路径会自行发出 task:dispatch。若把 queued 广播放到后面，派发事件可能先于 queued 事件到达客户端
	//（罕见但在构造上不安全）。按期望的观察顺序发布，让正确性不依赖时序。
	if queuedEvent.ID != "" {
		publishCommittedEvent(s.Bus, queuedEvent)
	} else {
		s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	}
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

// EnqueueTaskForMention 为 issue 上被 @提及的 agent 创建排队任务。与 EnqueueTaskForIssue 不同，
// 这里显式传入 agent ID，而不是从 issue 指派者推导。
func (s *TaskService) EnqueueTaskForMention(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTask(ctx, issue, agentID, triggerCommentID, false, pgtype.UUID{}, false, "", pgtype.UUID{}, pgtype.UUID{})
}

// EnqueueTaskForThreadParent 为“成员回复的那条直接父评论”的作者 agent 创建排队任务。
func (s *TaskService) EnqueueTaskForThreadParent(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTask(ctx, issue, agentID, triggerCommentID, false, pgtype.UUID{}, false, "", pgtype.UUID{}, pgtype.UUID{})
}

// EnqueueTaskForSquadLeader 是 EnqueueTaskForMention 的群组长角色变体。生成的任务带 is_leader_task=true，
// 使下游的自我触发守卫能区分“agent 以群组长身份发评论时的触发（跳过）”与“以成员身份发评论时的触发（不跳过）”。
// 这对同时担任同群组长与成员的 agent 很关键——见迁移 090。
//
// squadID 写入任务的 squad_id 列，daemon 认领时能据此找到群组并注入其 briefing，无论群组长任务是如何触发的
// （评论 @squad、issue 指派、autopilot、子 issue 完成回调）。见迁移 127。
func (s *TaskService) EnqueueTaskForSquadLeader(ctx context.Context, issue db.Issue, leaderID pgtype.UUID, squadID pgtype.UUID, triggerCommentID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTask(ctx, issue, leaderID, triggerCommentID, true, squadID, false, "", pgtype.UUID{}, pgtype.UUID{})
}

// EnqueueTaskForSquadLeaderWithHandoff 是携带交接说明（MUL-3375）的指派/提升变体，说明进入群组长运行的开场
// 上下文。空说明行为与 EnqueueTaskForSquadLeader 完全一致。actorUserID 是执行指派/提升的成员，成为本次运行
// 的负责人类（MUL-4302 §4）；调用方无成员 actor 时为无效值。
func (s *TaskService) EnqueueTaskForSquadLeaderWithHandoff(ctx context.Context, issue db.Issue, leaderID pgtype.UUID, squadID pgtype.UUID, handoffNote string, actorUserID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTask(ctx, issue, leaderID, pgtype.UUID{}, true, squadID, false, handoffNote, actorUserID, pgtype.UUID{})
}

// enqueueMentionTask 是 mention / thread-parent / squad-leader 类入队共享的实现入口（无合并评论计划时）。
func (s *TaskService) enqueueMentionTask(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID, isLeader bool, squadID pgtype.UUID, forceFreshSession bool, handoffNote string, actorUserID pgtype.UUID, rerunOfTaskID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTaskWithCommentPlan(ctx, issue, agentID, triggerCommentID, nil, isLeader, squadID, forceFreshSession, handoffNote, actorUserID, rerunOfTaskID)
}

// enqueueMentionTaskWithCommentPlan 是 mention 系入队的核心实现：加载并校验 agent，解析 attribution（来源标签
// 用 SourceDelegation 并做 fail-closed 兜底），构建 Composio overlay，在单事务内插入任务行并记录 queued 事件；
// 提交后按“先广播 queued、再通知 daemon”的顺序发布。coalescedCommentIDs 是合入本任务的评论 ID 列表。
// 与 issue 指派路径不同，此处若插入被 pending-task 唯一索引拒绝，会转成良性 ErrDuplicatePendingTask 结果。
func (s *TaskService) enqueueMentionTaskWithCommentPlan(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID, coalescedCommentIDs []pgtype.UUID, isLeader bool, squadID pgtype.UUID, forceFreshSession bool, handoffNote string, actorUserID pgtype.UUID, rerunOfTaskID pgtype.UUID) (db.AgentTaskQueue, error) {
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		slog.Error("mention task enqueue failed: agent not found", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		slog.Debug("mention task enqueue skipped: agent is archived", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
		return db.AgentTaskQueue{}, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		slog.Error("mention task enqueue failed: agent has no runtime", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
		return db.AgentTaskQueue{}, fmt.Errorf("agent has no runtime")
	}

	// 显式 mention / thread-parent / squad-leader 跳点如果来自 agent 撰写的评论，属委托
	//（继承父任务背后的人类）；成员 mention 则是 direct_human。attr.UserID 与 MUL-4302 之前的值一致，授权不变。
	attr := s.attributionForIssueTask(ctx, issue, triggerCommentID, attribution.SourceDelegation, actorUserID)
	// 没有精确人类 → owner_fallback（accountable = agent owner），
	// 若工作区为 fail-closed 则拒绝入队（MUL-4302 §3.5）。
	attr, err = s.applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		slog.Warn("mention task enqueue refused: attribution fail-closed", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
		return db.AgentTaskQueue{}, err
	}
	originatorUserID := attr.UserID
	runtimeMCPOverlay := s.buildRuntimeMCPOverlay(ctx, originatorUserID, agent)
	attrSource, attrDelegatedFrom, attrEvidenceKind, attrEvidenceRef := attributionCreateParams(attr)
	createParams := db.CreateAgentTaskParams{
		ID:                   dbid.NewV7(),
		AgentID:              agentID,
		RuntimeID:            agent.RuntimeID,
		IssueID:              issue.ID,
		Priority:             priorityToInt(issue.Priority),
		TriggerCommentID:     triggerCommentID,
		CoalescedCommentIds:  coalescedCommentIDs,
		TriggerSummary:       s.buildCommentTriggerSummary(ctx, issue.WorkspaceID, triggerCommentID),
		IsLeaderTask:         pgtype.Bool{Bool: isLeader, Valid: isLeader},
		ForceFreshSession:    pgtype.Bool{Bool: forceFreshSession, Valid: forceFreshSession},
		HandoffNote:          pgtype.Text{String: handoffNote, Valid: handoffNote != ""},
		SquadID:              squadID,
		OriginatorUserID:     originatorUserID,
		AccountableUserID:    attr.AccountableUserID,
		RuleVersionID:        attr.RuleVersionID,
		RerunOfTaskID:        rerunOfTaskID,
		RuntimeMcpOverlay:    runtimeMCPOverlay.Overlay,
		RuntimeConnectedApps: runtimeMCPOverlay.ConnectedApps,
		OriginatorSource:     attrSource,
		DelegatedFromTaskID:  attrDelegatedFrom,
		TriggerEvidenceKind:  attrEvidenceKind,
		TriggerEvidenceRefID: attrEvidenceRef,
		// 盖上被评审 head，使去重能区分本次运行与针对新 HEAD 的后续请求（TEN-356）。
		HeadSha: headShaText(s.ResolveIssueReviewSHA(ctx, issue.ID)),
	}
	var task db.AgentTaskQueue
	var queuedEvent events.Event
	err = s.runInTx(ctx, func(qtx *db.Queries) error {
		var createErr error
		task, createErr = qtx.CreateAgentTask(ctx, createParams)
		if createErr != nil {
			return createErr
		}
		if s.TxStarter != nil {
			queuedEvent, createErr = RecordTaskQueuedEventTx(ctx, qtx, util.UUIDToString(issue.WorkspaceID), task)
			if createErr != nil {
				return createErr
			}
		}
		return nil
	})
	if err != nil {
		// 同一 (issue, agent) 的并发入队抢跑成功，唯一索引拒绝了本次插入。这是良性结果——已有兄弟运行覆盖该目标——
		// 因此只记 debug 日志并返回类型化哨兵，调用方映射为“已合并”结果 / 409，而不是把原始约束名泄露成 500（#5914）。
		if isDuplicatePendingTaskErr(err) {
			slog.Debug("mention task enqueue coalesced: pending task already exists", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
			return db.AgentTaskQueue{}, ErrDuplicatePendingTask
		}
		slog.Error("mention task enqueue failed", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("create task: %w", err)
	}

	slog.Info("mention task enqueued", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID), "is_leader_task", isLeader)
	// 顺序理由同 EnqueueTaskForIssue（先广播 queued、再通知 daemon）。
	if queuedEvent.ID != "" {
		publishCommittedEvent(s.Bus, queuedEvent)
	} else {
		s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	}
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

// EnqueueDeferredAssigneeFallback 创建一个惰性任务：只有 PromotedDueDeferredTasksForRuntime 把它从 deferred
// 翻成 queued 后才可认领。用于“主路由任务在规定时间内无人接管”的降级指派场景。
func (s *TaskService) EnqueueDeferredAssigneeFallback(ctx context.Context, issue db.Issue, agentID, squadID pgtype.UUID, escalationForTaskID pgtype.UUID, triggerCommentID pgtype.UUID, fireAt time.Time) (db.AgentTaskQueue, error) {
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		slog.Error("deferred fallback enqueue failed: agent not found", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		slog.Debug("deferred fallback enqueue skipped: agent is archived", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
		return db.AgentTaskQueue{}, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		slog.Error("deferred fallback enqueue failed: agent has no runtime", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
		return db.AgentTaskQueue{}, fmt.Errorf("agent has no runtime")
	}

	// 降级指派者与主路由任务响应的是同一条触发评论，因此从该评论解析 attribution（成员作者 → direct_human；
	// agent 作者 → comment_source 链）并在创建时盖章。之后 Promotion 只翻状态，在这里盖章可让最终运行避开
	// NULL-source 旁路（MUL-4302 §2）。overlay 刻意留给既有提升路径——本次改动仅涉及 attribution。此处没有
	// 直接 actor：降级由评论路由，attribution 随评论走。
	attr := s.attributionForIssueTask(ctx, issue, triggerCommentID, attribution.SourceCommentSource, pgtype.UUID{})
	// 没有精确人类 → owner_fallback（accountable = agent owner），
	// 工作区为 fail-closed 时拒绝入队（MUL-4302 §3.5）。
	attr, err = s.applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		slog.Warn("deferred fallback enqueue refused: attribution fail-closed", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID))
		return db.AgentTaskQueue{}, err
	}
	attrSource, attrDelegatedFrom, attrEvidenceKind, attrEvidenceRef := attributionCreateParams(attr)
	isLeader := squadID.Valid
	task, err := s.Queries.CreateDeferredAgentTask(ctx, db.CreateDeferredAgentTaskParams{
		ID:                   dbid.NewV7(),
		AgentID:              agentID,
		RuntimeID:            agent.RuntimeID,
		IssueID:              issue.ID,
		Priority:             priorityToInt(issue.Priority),
		TriggerCommentID:     triggerCommentID,
		TriggerSummary:       s.buildCommentTriggerSummary(ctx, issue.WorkspaceID, triggerCommentID),
		IsLeaderTask:         pgtype.Bool{Bool: isLeader, Valid: isLeader},
		SquadID:              squadID,
		EscalationForTaskID:  escalationForTaskID,
		FireAt:               pgtype.Timestamptz{Time: fireAt, Valid: true},
		OriginatorUserID:     attr.UserID,
		AccountableUserID:    attr.AccountableUserID,
		OriginatorSource:     attrSource,
		DelegatedFromTaskID:  attrDelegatedFrom,
		TriggerEvidenceKind:  attrEvidenceKind,
		TriggerEvidenceRefID: attrEvidenceRef,
	})
	if err != nil {
		slog.Error("deferred fallback enqueue failed", "issue_id", util.UUIDToString(issue.ID), "agent_id", util.UUIDToString(agentID), "error", err)
		return db.AgentTaskQueue{}, fmt.Errorf("create deferred task: %w", err)
	}

	slog.Info("deferred fallback task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issue.ID),
		"agent_id", util.UUIDToString(agentID),
		"fire_at", fireAt.UTC().Format(time.RFC3339),
	)
	return task, nil
}

// QuickCreateContext 是 quick-create 任务 context 列上存的 JSON 载荷。daemon 通过 Type == "quick_create"
// 识别该变体并切换 quick-create 提示词模板；完成路径用 RequesterID + WorkspaceID 写收件箱通知。
//
// ProjectID 是用户在弹窗里选的项目（可选）。非空时 daemon 认领会解析项目标题与资源，提示词模板指示 agent
// 传 `--project <uuid>`，使新 issue 落到该项目下。
//
// SquadID 在用户选了群组（而非单个 agent）时非空。任务仍按群组 leader agent 入队（CreateQuickCreateTask 是
// agent 维度的）；SquadID 是提示，daemon 认领时把群组长 briefing 叠加到 agent 的 Instructions 上，与指派给
// 群组的 issue 任务行为一致。
type QuickCreateContext struct {
	Type          string   `json:"type"`
	Prompt        string   `json:"prompt"`
	RequesterID   string   `json:"requester_id"`
	WorkspaceID   string   `json:"workspace_id"`
	Priority      string   `json:"priority,omitempty"`
	DueDate       string   `json:"due_date,omitempty"`
	ProjectID     string   `json:"project_id,omitempty"`
	SquadID       string   `json:"squad_id,omitempty"`
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
	// ParentIssueID 是新 issue 应挂在哪个父 issue 下的可选 UUID。用户在已有 issue 上点“添加子 issue”打开弹窗
	// 时设置；daemon 认领会解析父 issue 标识，提示词模板指示 agent 传 `--parent <uuid>`，让子 issue 关系在
	// 手动 → agent 模式切换后仍然保留。
	ParentIssueID string `json:"parent_issue_id,omitempty"`
	// SourceContextID 标识本 quick-create 链路唯一产出的 issue 必须挂接的那份不可变 pending 源上下文捕获。
	SourceContextID string `json:"source_context_id,omitempty"`
}

// QuickCreateContextType 标记任务为 quick-create 作业。
const QuickCreateContextType = "quick_create"

// EnqueueQuickCreateTask 创建一条与 issue / chat / autopilot 都无关联的排队任务——用户自然语言提示存于任务
// context JSONB，agent 负责把它转成一次 `multica issue create` 调用。入队前先校验 agent 可达（未归档、
// 有 runtime），让 API 能提前拒绝，而不是排一条永远没人认领的任务。
//
// projectID 可选（用户未选时为零值 pgtype.UUID）；由 handler 负责在传入前校验它属于同一工作区。
//
// squadID 在用户选择群组为执行者时非空。handler 已把它解析成群组 leader agent 作为 agentID；squadID 提示
// 被盖进任务 context，daemon 派发时据此注入群组长 briefing。
//
// parentIssueID 可选（用户未从“添加子 issue”打开弹窗时为零值）；由 handler 负责校验属于同一工作区。
func (s *TaskService) EnqueueQuickCreateTask(ctx context.Context, workspaceID, requesterID pgtype.UUID, agentID, squadID pgtype.UUID, prompt, priority, dueDate string, projectID, parentIssueID pgtype.UUID, attachmentIDs []pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueQuickCreateTask(ctx, workspaceID, requesterID, agentID, squadID, prompt, priority, dueDate, projectID, parentIssueID, attachmentIDs, nil)
}

// EnqueueQuickCreateTaskWithSourceContext 是带“待挂接源上下文捕获”的 quick-create 入队变体：先对源 issue
// 的描述/评论做一次快照式源上下文捕获，再入队。用于带附件/来源正文的快速建单，保证 agent 执行期间源材料
// 不可变。
func (s *TaskService) EnqueueQuickCreateTaskWithSourceContext(ctx context.Context, workspaceID, requesterID pgtype.UUID, agentID, squadID pgtype.UUID, prompt, priority, dueDate string, projectID, parentIssueID pgtype.UUID, attachmentIDs []pgtype.UUID, capture SourceContextCapture) (db.AgentTaskQueue, error) {
	return s.enqueueQuickCreateTask(ctx, workspaceID, requesterID, agentID, squadID, prompt, priority, dueDate, projectID, parentIssueID, attachmentIDs, &capture)
}

// enqueueQuickCreateTask 是 quick-create 入队的核心实现：预检 issue 容量、校验 agent 可达，序列化 context
// JSONB；若带源上下文捕获，则在同一事务内锁定源 issue 与锚点评论、核对上下文指纹未变后再插任务并持久化
// 捕获；否则直接插任务并记录 queued 事件。提交后发布事件并唤醒 daemon。
func (s *TaskService) enqueueQuickCreateTask(ctx context.Context, workspaceID, requesterID pgtype.UUID, agentID, squadID pgtype.UUID, prompt, priority, dueDate string, projectID, parentIssueID pgtype.UUID, attachmentIDs []pgtype.UUID, capture *SourceContextCapture) (db.AgentTaskQueue, error) {
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("agent has no runtime")
	}

	payload := QuickCreateContext{
		Type:        QuickCreateContextType,
		Prompt:      prompt,
		RequesterID: util.UUIDToString(requesterID),
		WorkspaceID: util.UUIDToString(workspaceID),
		Priority:    priority,
		DueDate:     dueDate,
	}
	if projectID.Valid {
		payload.ProjectID = util.UUIDToString(projectID)
	}
	if squadID.Valid {
		payload.SquadID = util.UUIDToString(squadID)
	}
	if parentIssueID.Valid {
		payload.ParentIssueID = util.UUIDToString(parentIssueID)
	}
	if capture != nil {
		payload.SourceContextID = util.UUIDToString(capture.ID)
	}
	if len(attachmentIDs) > 0 {
		payload.AttachmentIDs = make([]string, 0, len(attachmentIDs))
		for _, id := range attachmentIDs {
			if id.Valid {
				payload.AttachmentIDs = append(payload.AttachmentIDs, util.UUIDToString(id))
			}
		}
	}
	contextJSON, err := json.Marshal(payload)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("marshal quick-create context: %w", err)
	}

	// 提交 quick-create 弹窗的请求者就是 direct_human 发起人与负责人。quick-create 是唯一一条没有“前驱行”可
	// 指向统一 evidence 对的入队路径：这次运行的全部工作就是创建 issue，入队时不存在可引用的评论/issue/会话
	// /run（issue 稍后经 LinkTaskToIssue 反链）。因此 evidence 刻意置 NULL；负责人类记录在 originator/
	// accountable_user_id 上，所以这不是 NULL-source 旁路——source 仍会盖成 direct_human（MUL-4302 §2）。
	attr := attribution.DirectHumanRun(requesterID, "", pgtype.UUID{})
	// 请求者无法解析时降级为 owner_fallback（accountable = agent owner），
	// 若工作区为 fail-closed 则拒绝（MUL-4302 §3.5）。
	attr, err = s.applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	attrSource, _, attrEvidenceKind, attrEvidenceRef := attributionCreateParams(attr)
	runtimeMCPOverlay := s.buildRuntimeMCPOverlay(ctx, requesterID, agent)
	taskID := dbid.NewV7()
	createParams := db.CreateQuickCreateTaskParams{
		ID:                   taskID,
		AgentID:              agentID,
		RuntimeID:            agent.RuntimeID,
		Priority:             3,
		Context:              contextJSON,
		OriginatorUserID:     requesterID,
		AccountableUserID:    attr.AccountableUserID,
		RuntimeMcpOverlay:    runtimeMCPOverlay.Overlay,
		RuntimeConnectedApps: runtimeMCPOverlay.ConnectedApps,
		OriginatorSource:     attrSource,
		TriggerEvidenceKind:  attrEvidenceKind,
		TriggerEvidenceRefID: attrEvidenceRef,
	}
	var task db.AgentTaskQueue
	var queuedEvent events.Event
	err = s.runInTx(ctx, func(qtx *db.Queries) error {
		if capture != nil {
			if _, err := qtx.LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{
				ID: capture.SourceIssueID, WorkspaceID: workspaceID,
			}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrSourceIssueDeleted
				}
				return err
			}
			locked, err := qtx.LockCommentAncestorPath(ctx, db.LockCommentAncestorPathParams{
				CommentID: capture.AnchorCommentID, WorkspaceID: workspaceID, IssueID: capture.SourceIssueID,
			})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrSourceIssueDeleted
				}
				return err
			}
			if len(locked) == 0 {
				return ErrAnchorCommentDeleted
			}
			current, err := BuildSourceContext(ctx, qtx, workspaceID, capture.AnchorCommentID)
			if err != nil {
				return err
			}
			if current.Digest != capture.Digest {
				return ErrSourceContextChanged
			}
		}

		var err error
		task, err = qtx.CreateQuickCreateTask(ctx, createParams)
		if err != nil {
			return err
		}
		if capture != nil {
			if _, err = PersistSourceContext(ctx, qtx, *capture, pgtype.UUID{}, task.ID); err != nil {
				return err
			}
		}
		queuedEvent, err = RecordTaskQueuedEventTx(ctx, qtx, util.UUIDToString(workspaceID), task)
		return err
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create quick-create task with source context: %w", err)
	}

	slog.Info("quick-create task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"agent_id", util.UUIDToString(agentID),
		"squad_id", payload.SquadID,
		"requester_id", util.UUIDToString(requesterID),
		"workspace_id", util.UUIDToString(workspaceID),
		"project_id", payload.ProjectID,
		"parent_issue_id", payload.ParentIssueID,
	)
	// 与其它所有 Enqueue* 路径一致：踢一下 daemon WS，让任务立即被认领，而不是等下一个 30s 轮询周期。
	// 否则弹窗立即关闭而任务一直停在 queued，用户会以为“quick create 没触发”，直到下一个 sleepWithContextOrWakeup tick。
	publishCommittedEvent(s.Bus, queuedEvent)
	s.NotifyTaskEnqueued(ctx, task)
	return task, nil
}

// RetrySourceContextQuickCreate 手动重试一条失败的“无 issue”quick-create，同时保留原封不动的 pending 源
// 上下文与克隆附件。上下文行是串行化点：只有当前持有 origin_task_id 的任务才能创建并接收后继者，因此
// 双击或与自动重试竞态都无法同时产生两条活的尝试。
//
// 只允许原始请求者重试该恢复项，保证 direct_human attribution 真实可信，也防止其它工作区成员仅凭知道任务
// ID 就重放一条私密提示词。
func (s *TaskService) RetrySourceContextQuickCreate(ctx context.Context, workspaceID, requesterID, sourceTaskID pgtype.UUID, canInvoke func(agent db.Agent) bool) (*db.AgentTaskQueue, error) {
	parent, err := s.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{
		ID: sourceTaskID, WorkspaceID: workspaceID,
	})
	if err != nil || parent.Status != "failed" {
		return nil, ErrSourceContextRetryUnavailable
	}
	quickCreate, ok := s.parseQuickCreateContext(parent)
	if !ok || quickCreate.SourceContextID == "" || quickCreate.RequesterID != util.UUIDToString(requesterID) {
		return nil, ErrSourceContextRetryUnavailable
	}
	contextID, err := util.ParseUUID(quickCreate.SourceContextID)
	if err != nil {
		return nil, ErrSourceContextRetryUnavailable
	}
	agent, err := s.Queries.GetAgent(ctx, parent.AgentID)
	if err != nil || agent.ArchivedAt.Valid || !agent.RuntimeID.Valid {
		return nil, ErrSourceContextRetryUnavailable
	}
	if canInvoke != nil && !canInvoke(agent) {
		return nil, ErrRerunInvokeNotAllowed
	}
	overlay := s.buildRuntimeMCPOverlay(ctx, requesterID, agent)

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin source context manual retry: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	locked, err := qtx.GetPendingIssueSourceContextByOriginTask(ctx, db.GetPendingIssueSourceContextByOriginTaskParams{
		WorkspaceID: workspaceID, OriginTaskID: sourceTaskID,
	})
	if err != nil || locked.ID != contextID {
		return nil, ErrSourceContextRetryUnavailable
	}
	child, err := qtx.CreateManualQuickCreateRetryTask(ctx, db.CreateManualQuickCreateRetryTaskParams{
		ActorUserID:          requesterID,
		RuntimeMcpOverlay:    overlay.Overlay,
		RuntimeConnectedApps: overlay.ConnectedApps,
		NewTaskID:            dbid.NewV7(),
		SourceTaskID:         sourceTaskID,
	})
	if err != nil {
		return nil, fmt.Errorf("create source context manual retry: %w", err)
	}
	if _, err := qtx.TransferPendingIssueSourceContextTask(ctx, db.TransferPendingIssueSourceContextTaskParams{
		NewTaskID: child.ID, WorkspaceID: workspaceID, ID: contextID, OldTaskID: sourceTaskID,
	}); err != nil {
		return nil, fmt.Errorf("transfer source context manual retry: %w", err)
	}
	queuedEvent, err := RecordTaskQueuedEventTx(ctx, qtx, util.UUIDToString(workspaceID), child)
	if err != nil {
		return nil, fmt.Errorf("record source context manual retry event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit source context manual retry: %w", err)
	}
	publishCommittedEvent(s.Bus, queuedEvent)
	s.NotifyTaskEnqueued(ctx, child)
	return &child, nil
}

// ErrChatTaskAgentArchived 表示 EnqueueChatTask 因目标 agent 已归档而拒绝入队。这是可产品化的状态——向用户
// 展示“该 agent 已被归档”而不是重试。
var ErrChatTaskAgentArchived = errors.New("chat task: agent archived")

// ErrChatTaskAgentNoRuntime 表示 EnqueueChatTask 因 agent 从未关联 runtime（agent.runtime_id IS NULL）而拒绝
// 入队，即“agent 未配置 daemon”，可产品化为“agent 离线”。
//
// 重要：这与“daemon 当前掉线”不是一回事。当 agent.runtime_id 已设置时，EnqueueChatTask 正常入队，daemon
// 下次上线后认领；那条路径返回任务行而非本错误。
var ErrChatTaskAgentNoRuntime = errors.New("chat task: agent has no runtime")

// ErrChatQuickActionsNoTurn 表示对一条没有合格助手回合可续的会话请求了建议重新生成（空会话，或最新回合是
// no_response / failure、无可借鉴内容）。
var ErrChatQuickActionsNoTurn = errors.New("chat quick actions: no assistant turn to regenerate")

// ErrChatQuickActionsUnavailable 表示部署未配置 LLM 层（无 MULTICA_LLM_API_KEY / MULTICA_LLM_BASE_URL），
// 根本无法生成建议。该场景下自动生成静默降级；显式刷新则得到此错误，客户端能说明“为什么没反应”。
var ErrChatQuickActionsUnavailable = errors.New("chat quick actions: llm layer not configured")

// ErrChatQuickActionsStale 表示客户端要刷新的回合已不是会话的最新助手回合（刷新按钮渲染后又落了一条新回复）。
// 重新生成被拒绝，避免客户端的乐观 pending 标记指向一条最终的 chat:quick_actions 事件对不上的回合（MUL-5149）。
var ErrChatQuickActionsStale = errors.New("chat quick actions: refresh target is stale")

// ErrChatQuickActionsBusy 表示会话已有进行中的工作——正在运行、即将改动最新回复的回合，或另一轮重新生成——
// 此时拒绝新的刷新，避免产生陈旧目标补充或重复消耗额度的生成（MUL-5149）。
var ErrChatQuickActionsBusy = errors.New("chat quick actions: session busy")

// ErrChatSessionArchived 表示一次发送或防抖的频道冲刷在与会话归档的竞态中落败，因此绝不能持久化新回合。
var ErrChatSessionArchived = errors.New("chat task: session archived")

// PreparedChatTaskEnqueue 是调用方开启任务入队事务【之前】构建的、不透明且无副作用的输入快照：
// 含 attribution 结果（负责人类/来源标签/evidence）与 Composio overlay，避免在网络 I/O 期间持有锁。
type PreparedChatTaskEnqueue struct {
	accountableUser  pgtype.UUID
	attrSource       pgtype.Text
	attrEvidenceKind pgtype.Text
	runtimeOverlay   runtimeMCPOverlayData
}

// MemberMayInvokeAgent 在渠道消息落库前核对发送者的真实调用权限；查询失败须交回平台重试。
func (s *TaskService) MemberMayInvokeAgent(ctx context.Context, agentID, userID pgtype.UUID) (bool, error) {
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load agent: %w", err)
	}
	return memberMayInvokeAgent(ctx, s.Queries, agent, userID, agent.WorkspaceID)
}

// PrepareChatTaskEnqueue performs reads and optional external integration work
// before the caller opens a transaction. BuildTaskOverlay may perform network
// I/O and must never run while /new holds route-rotation locks.
func (s *TaskService) PrepareChatTaskEnqueue(
	ctx context.Context,
	agentID, initiatorUserID pgtype.UUID,
) (PreparedChatTaskEnqueue, error) {
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil {
		slog.Error("chat task preparation failed", "agent_id", util.UUIDToString(agentID), "error", err)
		return PreparedChatTaskEnqueue{}, fmt.Errorf("load agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return PreparedChatTaskEnqueue{}, ErrChatTaskAgentArchived
	}
	if !agent.RuntimeID.Valid {
		return PreparedChatTaskEnqueue{}, ErrChatTaskAgentNoRuntime
	}

	attr := attribution.DirectHumanRun(
		initiatorUserID, attribution.EvidenceChat, pgtype.UUID{},
	)
	attr, err = s.applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		slog.Warn("chat task enqueue refused: attribution fail-closed",
			"agent_id", util.UUIDToString(agentID))
		return PreparedChatTaskEnqueue{}, err
	}
	attrSource, _, attrEvidenceKind, _ := attributionCreateParams(attr)
	return PreparedChatTaskEnqueue{
		accountableUser: attr.AccountableUserID,
		attrSource:      attrSource, attrEvidenceKind: attrEvidenceKind,
		runtimeOverlay: s.buildRuntimeMCPOverlay(ctx, initiatorUserID, agent),
	}, nil
}

// EnqueueChatTask 创建一条 Direct Chat 任务。若该 Chat 还绑定了频道，其上下文代次仍会快照，但不创建外部
// 投递快照：一方（first-party）发送只回给 first-party 客户端。
func (s *TaskService) EnqueueChatTask(
	ctx context.Context,
	chatSession db.ChatSession,
	initiatorUserID pgtype.UUID,
	forceFreshSession bool,
) (db.AgentTaskQueue, error) {
	return s.enqueueChatTask(
		ctx, chatSession, initiatorUserID, forceFreshSession, 0, false,
		pgtype.UUID{}, 0,
	)
}

// EnqueueChannelChatTask 为一个持久的频道上下文代次创建频道自有任务，并在同一事务内冻结其外部投递路由。
func (s *TaskService) EnqueueChannelChatTask(
	ctx context.Context,
	chatSession db.ChatSession,
	initiatorUserID pgtype.UUID,
	forceFreshSession bool,
	contextRevision int64,
	bindingID pgtype.UUID,
	routeRevision int64,
) (db.AgentTaskQueue, error) {
	if contextRevision <= 0 {
		return db.AgentTaskQueue{}, errors.New("channel chat task requires a context revision")
	}
	return s.enqueueChatTask(
		ctx, chatSession, initiatorUserID, forceFreshSession,
		contextRevision, true, bindingID, routeRevision,
	)
}

// enqueueChatTask 是 Direct Chat / 频道 Chat 入队的编排实现：先 PrepareChatTaskEnqueue 做无锁准备，
// 再开事务调 enqueueChatTaskTx，提交成功后执行 FinalizeChatTaskEnqueue 的后提交效应（发布事件、唤醒 daemon）。
// requireDelivery=true 时强制要求可投递的频道绑定存在（expectedBindingID/routeRevision 参与路由匹配校验）。
func (s *TaskService) enqueueChatTask(
	ctx context.Context,
	chatSession db.ChatSession,
	initiatorUserID pgtype.UUID,
	forceFreshSession bool,
	contextRevision int64,
	requireDelivery bool,
	expectedBindingID pgtype.UUID,
	expectedRouteRevision int64,
) (db.AgentTaskQueue, error) {
	prepared, err := s.PrepareChatTaskEnqueue(ctx, chatSession.AgentID, initiatorUserID)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if s.TxStarter == nil {
		return db.AgentTaskQueue{}, errors.New("chat task enqueue: transaction starter is required")
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("begin chat task enqueue: %w", err)
	}
	defer tx.Rollback(ctx)
	task, err := s.enqueueChatTaskTx(
		ctx, s.Queries.WithTx(tx), chatSession, initiatorUserID,
		forceFreshSession, contextRevision, requireDelivery,
		expectedBindingID, expectedRouteRevision, prepared,
	)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("commit chat task enqueue: %w", err)
	}
	s.FinalizeChatTaskEnqueue(ctx, task)
	return task, nil
}

// EnqueuePreparedChannelChatTaskInTx 把第一个 /new 回合加进调用方持有的路由切换事务。准备必须在开该事务前
// 完成；提交后的效应属于 FinalizeChatTaskEnqueue。
func (s *TaskService) EnqueuePreparedChannelChatTaskInTx(
	ctx context.Context,
	tx pgx.Tx,
	chatSession db.ChatSession,
	initiatorUserID pgtype.UUID,
	forceFreshSession bool,
	contextRevision int64,
	prepared PreparedChatTaskEnqueue,
) (db.AgentTaskQueue, error) {
	if contextRevision <= 0 {
		return db.AgentTaskQueue{}, errors.New("prepared channel chat task requires a context revision")
	}
	return s.enqueueChatTaskTx(
		ctx, s.Queries.WithTx(tx), chatSession, initiatorUserID,
		forceFreshSession, contextRevision, true, pgtype.UUID{}, 0, prepared,
	)
}

// enqueueChatTaskTx 是 chat 任务入队的事务内实现：锁定会话并校验 active/归档，重载并校验 agent，锁定频道
// 绑定与上下文代次（requireDelivery 时校验路由未被替换/回收），处理媒体 pending（把任务 fireAt 延后或整体
// 转 deferred），插入任务行并持久化 queued/deferred 事件。全部经调用方事务句柄执行，出错由外层回滚。
func (s *TaskService) enqueueChatTaskTx(
	ctx context.Context,
	qtx *db.Queries,
	chatSession db.ChatSession,
	initiatorUserID pgtype.UUID,
	forceFreshSession bool,
	contextRevision int64,
	requireDelivery bool,
	expectedBindingID pgtype.UUID,
	expectedRouteRevision int64,
	prepared PreparedChatTaskEnqueue,
) (db.AgentTaskQueue, error) {
	currentSession, err := qtx.LockChatSessionForEnqueue(ctx, chatSession.ID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("lock chat session: %w", err)
	}
	if currentSession.Status != "active" {
		return db.AgentTaskQueue{}, ErrChatSessionArchived
	}

	agent, err := qtx.GetAgentForClaimUpdate(ctx, chatSession.AgentID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("reload chat agent: %w", err)
	}
	if agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, ErrChatTaskAgentArchived
	}
	if !agent.RuntimeID.Valid {
		return db.AgentTaskQueue{}, ErrChatTaskAgentNoRuntime
	}

	binding, bindingErr := qtx.LockChannelChatSessionBindingForContext(ctx, chatSession.ID)
	if bindingErr != nil && !errors.Is(bindingErr, pgx.ErrNoRows) {
		return db.AgentTaskQueue{}, fmt.Errorf("lock channel chat binding: %w", bindingErr)
	}
	if requireDelivery {
		if errors.Is(bindingErr, pgx.ErrNoRows) {
			return db.AgentTaskQueue{}, fmt.Errorf("lock channel chat binding: %w", pgx.ErrNoRows)
		}
		routeMatches := expectedBindingID.Valid &&
			binding.ID == expectedBindingID &&
			binding.RouteRevision == expectedRouteRevision
		if binding.RetiredAt.Valid && !routeMatches {
			return db.AgentTaskQueue{}, fmt.Errorf("lock retired channel chat binding: %w", pgx.ErrNoRows)
		}
		if expectedBindingID.Valid && !routeMatches {
			return db.AgentTaskQueue{}, fmt.Errorf("lock expected channel chat route: %w", pgx.ErrNoRows)
		}
	}

	pendingFresh := false
	if bindingErr == nil {
		if contextRevision <= 0 {
			contextRevision = binding.ContextRevision
		}
		generation, err := qtx.LockChannelChatContextGenerationByRevision(
			ctx, db.LockChannelChatContextGenerationByRevisionParams{
				ChatSessionID: chatSession.ID, Revision: contextRevision,
			},
		)
		if err != nil {
			return db.AgentTaskQueue{}, fmt.Errorf("lock channel context generation: %w", err)
		}
		pendingFresh = generation.PendingFresh
	}
	if pendingFresh {
		forceFreshSession = true
	}

	mediaPendingUntil, err := qtx.GetChannelMediaPendingUntil(
		ctx, db.GetChannelMediaPendingUntilParams{
			ChatSessionID: chatSession.ID,
			ChannelContextRevision: pgtype.Int8{
				Int64: contextRevision, Valid: contextRevision > 0,
			},
		},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return db.AgentTaskQueue{}, fmt.Errorf("load channel media pending deadline: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		mediaPendingUntil = pgtype.Timestamptz{}
	}

	task, err := qtx.CreateChatTask(ctx, db.CreateChatTaskParams{
		ID:                   dbid.NewV7(),
		AgentID:              chatSession.AgentID,
		RuntimeID:            agent.RuntimeID,
		Priority:             2,
		ChatSessionID:        chatSession.ID,
		InitiatorUserID:      initiatorUserID,
		FireAt:               mediaPendingUntil,
		OriginatorUserID:     initiatorUserID,
		AccountableUserID:    prepared.accountableUser,
		ForceFreshSession:    pgtype.Bool{Bool: forceFreshSession, Valid: true},
		RuntimeMcpOverlay:    prepared.runtimeOverlay.Overlay,
		RuntimeConnectedApps: prepared.runtimeOverlay.ConnectedApps,
		OriginatorSource:     prepared.attrSource,
		TriggerEvidenceKind:  prepared.attrEvidenceKind,
		TriggerEvidenceRefID: chatSession.ID,
		ChannelContextRevision: pgtype.Int8{
			Int64: contextRevision, Valid: contextRevision > 0,
		},
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create chat task: %w", err)
	}
	if requireDelivery {
		if _, err := qtx.CreateChannelTaskDeliveryFromSession(
			ctx, db.CreateChannelTaskDeliveryFromSessionParams{
				TaskID: task.ID, ChatSessionID: chatSession.ID,
			},
		); err != nil {
			return db.AgentTaskQueue{}, fmt.Errorf("snapshot channel task delivery: %w", err)
		}
	}

	task, err = qtx.SetChatTaskInputOwnerSelf(ctx, task.ID)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("set channel chat task input owner: %w", err)
	}
	if err := qtx.LinkUnownedChannelChatMessagesToTask(
		ctx, db.LinkUnownedChannelChatMessagesToTaskParams{
			TaskID: task.ID, ChatSessionID: chatSession.ID,
		},
	); err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("seal channel chat task input: %w", err)
	}
	switch corrected, err := qtx.DeferChatTaskForSealedPendingMedia(ctx, task.ID); {
	case err == nil:
		task = corrected
	case !errors.Is(err, pgx.ErrNoRows):
		return db.AgentTaskQueue{}, fmt.Errorf("defer chat task for sealed pending media: %w", err)
	}
	if pendingFresh {
		if err := qtx.ClearChannelChatContextPendingFresh(
			ctx, db.ClearChannelChatContextPendingFreshParams{
				ChatSessionID: chatSession.ID, Revision: contextRevision,
			},
		); err != nil {
			return db.AgentTaskQueue{}, fmt.Errorf("clear channel context pending fresh: %w", err)
		}
		if err := qtx.ClearChannelChatSessionPendingFreshForRevision(
			ctx, db.ClearChannelChatSessionPendingFreshForRevisionParams{
				ChatSessionID: chatSession.ID, Revision: contextRevision,
			},
		); err != nil {
			return db.AgentTaskQueue{}, fmt.Errorf("clear channel pending fresh: %w", err)
		}
	}
	createdEventType := protocol.EventTaskQueued
	if task.Status == "deferred" {
		createdEventType = protocol.EventTaskDeferred
	}
	createdEvent := taskEvent(createdEventType, util.UUIDToString(currentSession.WorkspaceID), task)
	createdEvent.IdempotencyKey = "task:" + strings.TrimPrefix(createdEventType, "task:") + ":" + util.UUIDToString(task.ID)
	createdEvent.StreamKey = "task:" + util.UUIDToString(task.ID)
	if _, err := recordDurableEventTx(ctx, qtx, createdEvent); err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("record chat task creation event: %w", err)
	}
	return task, nil
}

// FinalizeChatTaskEnqueue 只做提交后效应：deferred 任务尝试“媒体就绪即提升”，普通任务发布 queued 事件并唤醒
// daemon。
func (s *TaskService) FinalizeChatTaskEnqueue(ctx context.Context, task db.AgentTaskQueue) {
	if task.Status == "deferred" {
		slog.Info("chat task deferred for channel media",
			"task_id", util.UUIDToString(task.ID),
			"chat_session_id", util.UUIDToString(task.ChatSessionID),
			"agent_id", util.UUIDToString(task.AgentID),
			"fire_at", task.FireAt.Time,
		)
		if err := s.PromoteChannelChatTasksIfMediaReady(ctx, task.ChatSessionID); err != nil {
			slog.Warn("chat task media-ready fence failed; deferred task falls back to its deadline",
				"task_id", util.UUIDToString(task.ID),
				"chat_session_id", util.UUIDToString(task.ChatSessionID),
				"error", err,
			)
		}
		return
	}

	slog.Info("chat task enqueued",
		"task_id", util.UUIDToString(task.ID),
		"chat_session_id", util.UUIDToString(task.ChatSessionID),
		"agent_id", util.UUIDToString(task.AgentID),
	)
	if err := publishPersistedTaskEvent(ctx, s.Queries, s.Bus, protocol.EventTaskQueued, task); err != nil {
		slog.Warn("chat task queued event was persisted but could not be published", "task_id", util.UUIDToString(task.ID), "error", err)
	}
	s.NotifyTaskEnqueued(ctx, task)
}

// RegenerateChatQuickActions 为会话的最新助手回合跑一轮新的建议生成，让用户不发送新消息就能刷新 quick-action
// pills（MUL-5149）。生成走与自动轮次相同的服务端 LLM 路径，因此这里不排 agent 任务、不恢复任何 provider
// 会话——只做目标校验。返回目标助手消息 ID（供客户端挂 pending 占位）与该回合的任务行，后者交给调用方
// 喂给 GenerateChatQuickActionsAsync。
//
// 这是显式用户操作，忽略按设备的 quick-actions 开关（该开关只控制发送时的自动生成）。
func (s *TaskService) RegenerateChatQuickActions(ctx context.Context, chatSession db.ChatSession, expectedMessageID pgtype.UUID) (targetMessageID pgtype.UUID, targetTask db.AgentTaskQueue, err error) {
	if s.QuickActions == nil || !s.QuickActions.Enabled() {
		return pgtype.UUID{}, db.AgentTaskQueue{}, ErrChatQuickActionsUnavailable
	}

	// 目标是最近一条助手回合。只有普通 message 回合能产出建议——no_response / failure 回合没有可依托的内容，
	// 且生成器按该回合的任务 id 落写。
	target, err := s.Queries.GetLatestAssistantChatMessageForSession(ctx, chatSession.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, db.AgentTaskQueue{}, ErrChatQuickActionsNoTurn
		}
		return pgtype.UUID{}, db.AgentTaskQueue{}, fmt.Errorf("load latest assistant turn: %w", err)
	}
	if target.MessageKind != protocol.ChatMessageKindMessage || !target.TaskID.Valid {
		return pgtype.UUID{}, db.AgentTaskQueue{}, ErrChatQuickActionsNoTurn
	}
	// 确认客户端要刷新的仍是【最新】那条回合。多客户端竞态下，按钮渲染后可能有更新的回复落库；此时基于新
	// 上下文重新生成却挂到陈旧回合上，客户端的 pending 标记会等一条永远不匹配的 chat:quick_actions。因此拒绝，
	// 让客户端回滚并在新回合上重新提供刷新（MUL-5149）。
	if target.ID != expectedMessageID {
		return pgtype.UUID{}, db.AgentTaskQueue{}, ErrChatQuickActionsStale
	}
	// 会话上有回合正在运行时拒绝：它即将替换最新回复，现在生成的建议会挂到用户几秒后就会划过去的回合上。
	busy, err := s.Queries.HasActiveChatTaskForSession(ctx, chatSession.ID)
	if err != nil {
		return pgtype.UUID{}, db.AgentTaskQueue{}, fmt.Errorf("check active chat task: %w", err)
	}
	if busy {
		return pgtype.UUID{}, db.AgentTaskQueue{}, ErrChatQuickActionsBusy
	}
	// 刷新已不再创建任务行，上面的检查看不到本会话已在进行的生成——否则第二个刷新（或与自动生成竞态的那个）
	// 会被接受，多花一次上游调用并抢写同一行。这里拒绝它，让客户端回滚乐观 pending 标记。
	if s.chatQuickActionsInFlight(chatSession.ID) {
		return pgtype.UUID{}, db.AgentTaskQueue{}, ErrChatQuickActionsBusy
	}

	task, err := s.Queries.GetAgentTask(ctx, target.TaskID)
	if err != nil {
		return pgtype.UUID{}, db.AgentTaskQueue{}, fmt.Errorf("load target turn task: %w", err)
	}

	slog.Info("chat quick-actions regenerate accepted",
		"chat_session_id", util.UUIDToString(chatSession.ID),
		"target_task_id", util.UUIDToString(target.TaskID),
		"target_message_id", util.UUIDToString(target.ID),
	)
	// 只做校验：真正开始生成由调用方负责。把派发留在外面，意味着刷新门槛可在没有后台 goroutine 抢写同一行
	// 的情况下被单测覆盖。
	return target.ID, task, nil
}

// PromoteChannelChatTasksIfMediaReady 在会话里所有未过期的媒体标记都被清除后，立即把频道任务置为可认领。
// 若进程先行崩溃，常规 deferred 任务提升器会在其持久化的 fire_at 截止时间提升它们，跨重启保留占位兜底。
func (s *TaskService) PromoteChannelChatTasksIfMediaReady(ctx context.Context, sessionID pgtype.UUID) error {
	var tasks []db.AgentTaskQueue
	var persisted []events.Event
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		tasks, err = qtx.PromoteChannelChatTasksIfMediaReady(ctx, sessionID)
		if err != nil {
			return err
		}
		persisted = make([]events.Event, 0, len(tasks))
		for _, task := range tasks {
			workspaceID, err := resolveTaskWorkspaceWithQueries(ctx, qtx, task)
			if err != nil {
				return fmt.Errorf("resolve promoted chat task workspace: %w", err)
			}
			event, err := RecordTaskQueuedEventTx(ctx, qtx, workspaceID, task)
			if err != nil {
				return fmt.Errorf("record promoted chat task event: %w", err)
			}
			persisted = append(persisted, event)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("promote channel chat tasks after media: %w", err)
	}
	for i, task := range tasks {
		slog.Info("channel media-ready chat task promoted",
			"task_id", util.UUIDToString(task.ID),
			"chat_session_id", util.UUIDToString(sessionID),
			"agent_id", util.UUIDToString(task.AgentID),
		)
		publishCommittedEvent(s.Bus, persisted[i])
		s.NotifyTaskEnqueued(ctx, task)
	}
	return nil
}

// PromoteDeferredChannelIssueTask 使一条受媒体门控的 /issue 任务变为可认领。返回 ErrNoRows 表示截止时间
// 清扫器已提升过它（或任务已被取消），因此在两条提升路径间保持幂等。
func (s *TaskService) PromoteDeferredChannelIssueTask(ctx context.Context, taskID pgtype.UUID) error {
	var task db.AgentTaskQueue
	var queuedEvent events.Event
	found := false
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		task, err = qtx.PromoteDeferredChannelIssueTask(ctx, taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		workspaceID, err := resolveTaskWorkspaceWithQueries(ctx, qtx, task)
		if err != nil {
			return fmt.Errorf("resolve promoted issue task workspace: %w", err)
		}
		queuedEvent, err = RecordTaskQueuedEventTx(ctx, qtx, workspaceID, task)
		return err
	})
	if err != nil {
		return fmt.Errorf("promote deferred channel issue task: %w", err)
	}
	if !found {
		return nil
	}
	slog.Info("channel media-ready issue task promoted",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(task.IssueID),
		"agent_id", util.UUIDToString(task.AgentID),
	)
	publishCommittedEvent(s.Bus, queuedEvent)
	s.NotifyTaskEnqueued(ctx, task)
	return nil
}

// DirectChatSendResult 携带一次事务性 direct-chat 发送持久化的各行结果，
// handler 可据此广播用户消息并组织响应，无需再次读库。
type DirectChatSendResult struct {
	Task               db.AgentTaskQueue
	Message            db.ChatMessage
	BoundAttachmentIDs []pgtype.UUID
	Queued             bool
	InitialTitle       string
	// 这两个 durable 事件与回合在同一事务内落库。handler 可在提交后发布它们；
	// 只需要行数据的调用方可忽略这些字段。
	MessageEvent events.Event
	// TaskQueuedEvent 是任务入队事件（task:queued），见上方 MessageEvent 说明。
	TaskQueuedEvent events.Event
}

// SendDirectChatMessage 原子地持久化一轮 web/mobile direct-chat 消息：承载任务（经 chat_input_task_id
// 认领自己的输入批次）、绑定到该任务的用户消息、附件绑定与会话 touch 一并提交（MUL-4351）。只在提交后
// 通知 daemon，因此 daemon 绝不会看到“有消息没任务”或“有任务没输入归属”——后续认领精确读到本任务的用户
// 消息，而不是扫描尾部历史。
//
// 调用方必须先闸门会话并预检 agent（归档 / 无 runtime），把已加载的 agent 传入。事务锁下会重做这些检查，
// 因为任一行都可能在入队前变化。
func (s *TaskService) SendDirectChatMessage(
	ctx context.Context,
	session db.ChatSession,
	agent db.Agent,
	initiatorUserID pgtype.UUID,
	content string,
	attachmentIDs []pgtype.UUID,
	uploaderType string,
	uploaderID pgtype.UUID,
	healthReportIDs ...[]pgtype.UUID,
) (*DirectChatSendResult, error) {
	// 在事务【之前】构建 per-task Composio overlay——它可能做网络 I/O，不能在持有 DB 事务时执行。
	overlay := s.buildRuntimeMCPOverlay(ctx, initiatorUserID, agent)

	// 为聊天发送者做完整 attribution，同样在事务前解析（策略读取 + 兜底不能带着事务跑）——与 EnqueueChatTask
	// 写的 direct_human 章一致。没有它会形成绕过：direct-chat 路径只设 originator_user_id，却把 accountable
	// /source/evidence 留 NULL，违反单向不变量并丢失审计来源（MUL-4302 §2）。
	attr := attribution.DirectHumanRun(initiatorUserID, attribution.EvidenceChat, session.ID)
	attr, err := s.applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		return nil, err
	}
	attrSource, _, attrEvidenceKind, attrEvidenceRef := attributionCreateParams(attr)

	var out DirectChatSendResult
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		// 把这次发送与“同一会话的并发运行时重绑”串行化（MUL-5163）。必须先取锁、并在锁下重读 agent：调用方加载
		// 的 runtime_id 到这里可能已过期；被重绑挡在后面的发送若继续，会把任务盖成刚切走的那个 runtime——用户看到
		// “已切换”的确认，回复却跑在旧 runtime 上。先锁 chat_session 也与删除路径的加锁顺序一致，二者不会死锁。
		if _, err := qtx.LockChatSessionForRuntimeBind(ctx, session.ID); err != nil {
			return fmt.Errorf("lock chat session: %w", err)
		}
		currentSession, err := qtx.GetChatSession(ctx, session.ID)
		if err != nil {
			return fmt.Errorf("reload chat session: %w", err)
		}
		if currentSession.Status != "active" {
			return ErrChatSessionArchived
		}
		carrier, err := qtx.GetAgentForClaimUpdate(ctx, session.AgentID)
		if err != nil {
			return fmt.Errorf("reload chat agent: %w", err)
		}
		if carrier.ArchivedAt.Valid {
			return ErrChatTaskAgentArchived
		}
		if !carrier.RuntimeID.Valid {
			return ErrChatTaskAgentNoRuntime
		}

		// 新任务在 DB 里的状态是 “queued”，直到 daemon 认领。产品侧的队列语义是位置式的：只有本会话里已有另一条
		// 可见任务排在这条前面时，这次发送才算“跟随消息”。deferred 重试也算（它先恢复更早的一轮）。会话 + agent
		// 两把锁把兄弟发送、重试、终态写入与认领都串行化在这轮读取前后。
		queued, err := qtx.HasPendingChatTurnForSession(ctx, session.ID)
		if err != nil {
			return fmt.Errorf("check direct chat queue position: %w", err)
		}
		out.Queued = queued

		task, err := qtx.CreateChatTask(ctx, db.CreateChatTaskParams{
			ID:                   dbid.NewV7(),
			AgentID:              session.AgentID,
			RuntimeID:            carrier.RuntimeID,
			Priority:             2, // medium priority for chat; matches EnqueueChatTask
			ChatSessionID:        session.ID,
			InitiatorUserID:      initiatorUserID,
			OriginatorUserID:     attr.UserID,
			AccountableUserID:    attr.AccountableUserID,
			ForceFreshSession:    pgtype.Bool{Bool: false, Valid: true},
			RuntimeMcpOverlay:    overlay.Overlay,
			RuntimeConnectedApps: overlay.ConnectedApps,
			OriginatorSource:     attrSource,
			TriggerEvidenceKind:  attrEvidenceKind,
			TriggerEvidenceRefID: attrEvidenceRef,
		})
		if err != nil {
			return fmt.Errorf("create direct chat task: %w", err)
		}
		// 在同一事务内先认领本任务自己的输入批次（chat_input_task_id = id），再写 task_id = task.id 的用户消息。
		task, err = qtx.SetChatTaskInputOwnerSelf(ctx, task.ID)
		if err != nil {
			return fmt.Errorf("stamp direct chat input owner: %w", err)
		}
		out.Task = task

		// 在插入首条公开用户消息前，为显式空白的 Chat 初始化标题。CAS 查询同时防手动改名与并发首发的覆盖；
		// 把它放进本事务可避免“只提交了标题、没有消息”的中间态。
		if title := chattitle.Derive(content); title != "" {
			if _, err := qtx.InitializeChatSessionTitle(ctx, db.InitializeChatSessionTitleParams{ID: session.ID, Title: title}); err == nil {
				out.InitialTitle = title
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("initialize direct chat title: %w", err)
			}
		}

		// 创建已归属本任务（task_id = task.id）的用户消息，使它在诞生瞬间就属于这条不可变的输入批次。
		msg, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
			ID:            dbid.NewV7(),
			ChatSessionID: session.ID,
			Role:          "user",
			Content:       content,
			TaskID:        task.ID,
			MessageKind:   pgtype.Text{String: protocol.ChatMessageKindMessage, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("create user chat message: %w", err)
		}
		out.Message = msg
		if len(healthReportIDs) > 0 && len(healthReportIDs[0]) > 0 {
			if err := qtx.LinkChatMessageHealthReports(ctx, db.LinkChatMessageHealthReportsParams{ID: msg.ID, HealthReportIds: healthReportIDs[0]}); err != nil {
				return fmt.Errorf("link private health reports: %w", err)
			}
		}

		if len(attachmentIDs) > 0 {
			bound, err := qtx.LinkAttachmentsToChatMessage(ctx, db.LinkAttachmentsToChatMessageParams{
				ChatMessageID: msg.ID,
				ChatSessionID: session.ID,
				WorkspaceID:   session.WorkspaceID,
				UploaderType:  uploaderType,
				UploaderID:    uploaderID,
				AttachmentIds: attachmentIDs,
			})
			if err != nil {
				return fmt.Errorf("link chat attachments: %w", err)
			}
			out.BoundAttachmentIDs = bound

			// 显式空白的 /new Chat 也可从 first-party 客户端收到首轮；对纯附件回合，用第一个真正绑定成功的附件来
			// 初始化标题。媒体标题 CAS 刻意在消息插入后执行，仍拒绝覆盖手动改名或竞争中的首条消息。
			if out.InitialTitle == "" && strings.TrimSpace(content) == "" && len(bound) > 0 {
				attachments, err := qtx.ListAttachmentsByChatMessage(ctx, db.ListAttachmentsByChatMessageParams{
					ChatMessageID: msg.ID,
					WorkspaceID:   session.WorkspaceID,
				})
				if err != nil {
					return fmt.Errorf("list bound chat attachments for title: %w", err)
				}
				if len(attachments) > 0 {
					title := chattitle.Derive(attachments[0].Filename)
					if title != "" {
						if _, err := qtx.InitializeChatSessionMediaTitle(ctx, db.InitializeChatSessionMediaTitleParams{
							ID: session.ID, MessageID: msg.ID, Title: title,
						}); err == nil {
							out.InitialTitle = title
						} else if !errors.Is(err, pgx.ErrNoRows) {
							return fmt.Errorf("initialize direct attachment chat title: %w", err)
						}
					}
				}
			}
		}

		if err := qtx.TouchChatSession(ctx, session.ID); err != nil {
			return fmt.Errorf("touch chat session: %w", err)
		}
		workspaceID := util.UUIDToString(currentSession.WorkspaceID)
		taskQueuedEvent := taskEvent(protocol.EventTaskQueued, workspaceID, out.Task)
		taskQueuedEvent.IdempotencyKey = "task:queued:" + util.UUIDToString(out.Task.ID)
		taskQueuedEvent.StreamKey = "task:" + util.UUIDToString(out.Task.ID)
		out.TaskQueuedEvent, err = recordDurableEventTx(ctx, qtx, taskQueuedEvent)
		if err != nil {
			return fmt.Errorf("record direct chat task queued event: %w", err)
		}
		messageEvent := events.Event{
			Type:           protocol.EventChatMessage,
			IdempotencyKey: "chat-message:created:" + util.UUIDToString(out.Message.ID),
			StreamKey:      "chat:" + util.UUIDToString(currentSession.ID),
			WorkspaceID:    workspaceID,
			ActorType:      uploaderType,
			ActorID:        util.UUIDToString(uploaderID),
			TaskID:         util.UUIDToString(out.Task.ID),
			ChatSessionID:  util.UUIDToString(currentSession.ID),
			Payload: protocol.ChatMessagePayload{
				ChatSessionID: util.UUIDToString(currentSession.ID),
				MessageID:     util.UUIDToString(out.Message.ID),
				Role:          out.Message.Role,
				Content:       out.Message.Content,
				TaskID:        util.UUIDToString(out.Task.ID),
				CreatedAt:     util.TimestampToString(out.Message.CreatedAt),
			},
		}
		out.MessageEvent, err = recordDurableEventTx(ctx, qtx, messageEvent)
		if err != nil {
			return fmt.Errorf("record direct chat message event: %w", err)
		}
		return nil
	}); err != nil {
		slog.Error("direct chat send failed",
			"chat_session_id", util.UUIDToString(session.ID),
			"agent_id", util.UUIDToString(session.AgentID),
			"error", err)
		return nil, err
	}

	slog.Info("direct chat task enqueued",
		"task_id", util.UUIDToString(out.Task.ID),
		"chat_session_id", util.UUIDToString(session.ID),
		"agent_id", util.UUIDToString(session.AgentID))
	// 发布已提交的 durable 信封而非重建一份临时事件：让实时通知与事务记录的持久事实一致，也避免出现
	// 第二种 task:queued 事件形态。
	publishCommittedEvent(s.Bus, out.TaskQueuedEvent)
	s.NotifyTaskEnqueued(ctx, out.Task)
	return &out, nil
}

// CancelTasksForIssue 取消 issue 上所有活跃任务，调和每个受影响 agent 的状态，并广播 task:cancelled 事件，
// 让前端清掉正在运行的任务卡片。
//
// 调用方只应是显式的 issue 生命周期清理路径——DeleteIssue 与 BatchDeleteIssues：宿主 issue 行将被删除，
// 其任务不能残留成孤儿。普通状态翻转（含置为 cancelled）不再路由到这里（MUL-4465）：取消 issue 不是隐式的
// “停止全部运行”开关。不要重新加回状态驱动的调用方。
//
// #1587 之前该路径“删行即返回”，把每个受影响的 agent 永久卡在 status="working"，需要手工执行
// `multica agent update <id> --status idle` 才能解卡。现在它与 CancelTask、RerunIssue 一致：调和 agent
// 状态并广播 task:cancelled。
func (s *TaskService) CancelTasksForIssue(ctx context.Context, issueID pgtype.UUID) error {
	var cancelled []db.AgentTaskQueue
	var persisted []events.Event
	// 取消与其收尾一起提交：若收尾在取消提交后才失败，将永远无法修复——outbox 扫描会排除“覆盖任务已终态且已
	// 持有回执”的评论，导致它既不重放也不结算，永远挂在部分索引里。
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		cancelled, err = qtx.CancelAgentTasksByIssue(ctx, issueID)
		if err != nil {
			return err
		}
		if err := SettleDeliveredDelegatedFailureRecoveries(ctx, qtx, cancelled...); err != nil {
			return err
		}
		if s.TxStarter != nil {
			persisted, err = enqueueTaskTerminalEvents(ctx, qtx, protocol.EventTaskCancelled, cancelled)
		}
		return err
	}); err != nil {
		return err
	}
	for _, event := range persisted {
		publishCommittedEvent(s.Bus, event)
	}
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		if s.TxStarter == nil {
			s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
		}
	}
	// 按去重后的 agent 各调和一次，而不是每行一次：取消 issue 常同时停掉同一 agent 的多条任务，而每次调和都是
	// 一次 DB 写加一次状态广播。与 CancelTasksForAgent 的单次调和形态一致（D#3319）。
	for _, agentID := range distinctAgentIDs(cancelled) {
		s.ReconcileAgentStatus(ctx, agentID)
	}
	s.notifyTasksFinished(cancelled)
	return nil
}

// distinctAgentIDs 按首次出现顺序返回取消行里出现的每个 agent id。批量取消常停掉同一 agent 的多条任务；
// 按去重 agent（而非按行）调和，可把冗余的 RefreshAgentStatusFromTasks 写与状态广播收敛到每 agent 一次，
// 且不改变最终的 agent 状态。
func distinctAgentIDs(cancelled []db.AgentTaskQueue) []pgtype.UUID {
	seen := make(map[pgtype.UUID]struct{}, len(cancelled))
	ids := make([]pgtype.UUID, 0, len(cancelled))
	for _, t := range cancelled {
		if _, dup := seen[t.AgentID]; dup {
			continue
		}
		seen[t.AgentID] = struct{}{}
		ids = append(ids, t.AgentID)
	}
	return ids
}

// CancelTasksForAgent 取消某 agent 的全部活跃任务（queued + dispatched + running），调和其状态并广播
// task:cancelled。供 agent 级“取消全部任务”操作使用——形态与 CancelTasksForIssue 相同，只是按 agent_id 收敛。
//
// 返回被取消的行，供调用方统计 / 记日志。
func (s *TaskService) CancelTasksForAgent(ctx context.Context, agentID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	var cancelled []db.AgentTaskQueue
	var persisted []events.Event
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		cancelled, err = qtx.CancelAgentTasksByAgent(ctx, agentID)
		if err != nil {
			return err
		}
		if err := SettleDeliveredDelegatedFailureRecoveries(ctx, qtx, cancelled...); err != nil {
			return err
		}
		if s.TxStarter != nil {
			persisted, err = enqueueTaskTerminalEvents(ctx, qtx, protocol.EventTaskCancelled, cancelled)
		}
		return err
	}); err != nil {
		return nil, err
	}
	for _, event := range persisted {
		publishCommittedEvent(s.Bus, event)
	}
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		if s.TxStarter == nil {
			s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
		}
	}
	// 循环结束后调和一次即可——agent 依据剩余任务数从 working→available 转换，无需按行调用（刚取消的行都
	// 属于同一个 agent）。
	s.ReconcileAgentStatus(ctx, agentID)
	s.notifyTasksFinished(cancelled)
	return cancelled, nil
}

// CancelTasksByTriggerComment 取消“计划评论批次里包含该被编辑/删除评论”的活跃任务。方法名沿用历史叫法以
// 保持调用点稳定。必须在删除操作清掉 trigger FK 之前执行；返回的行让 handler 能重路由每个幸存的输入。
func (s *TaskService) CancelTasksByTriggerComment(ctx context.Context, commentID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	var cancelled []db.AgentTaskQueue
	var persisted []events.Event
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		cancelled, err = qtx.CancelAgentTasksByTriggerComment(ctx, commentID)
		if err != nil {
			return err
		}
		if err := SettleDeliveredDelegatedFailureRecoveries(ctx, qtx, cancelled...); err != nil {
			return err
		}
		if s.TxStarter != nil {
			persisted, err = enqueueTaskTerminalEvents(ctx, qtx, protocol.EventTaskCancelled, cancelled)
		}
		return err
	}); err != nil {
		return nil, err
	}
	for _, event := range persisted {
		publishCommittedEvent(s.Bus, event)
	}
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		if s.TxStarter == nil {
			s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
		}
	}
	// 按去重后的 agent 各调和一次，而不是每行一次：一条被编辑/删除的触发评论可能取消同一 agent 的多条任务，
	// 而每次调和都是一次 DB 写加一次状态广播（D#3319）。
	for _, agentID := range distinctAgentIDs(cancelled) {
		s.ReconcileAgentStatus(ctx, agentID)
	}
	s.notifyTasksFinished(cancelled)
	return cancelled, nil
}

// BroadcastCancelledTasks 调和每个受影响 agent 的状态并为每行发出 task:cancelled。调用方必须在【取消事务
// 提交后】再调用，避免订阅方观察到一条事务可能还会回滚的 “cancelled” 事件。
//
// workspaceID 由调用方提供而非按任务解析，因为这些调用方刚提交的事务可能删除了解析要读的行：chat 任务的
// 工作区经 chat_session 到达，而 DeleteChatSession 与 runtime 拆除都会删掉该会话（拆除经由删除其挂靠的系统
// agent）。此后 ResolveTaskWorkspaceID 查不到任何东西、返回 ""，publishTaskEvent 会把无工作区的事件在到达
// 总线前丢掉：行已取消，却没人被告知，每个队列视图与频道指示器都还显示着已不存在的运行。每个调用方都已
// 知道工作区——就是那个会话/成员/runtime 正被拆除的工作区——因此该查询本就不必要且不会失败。
func (s *TaskService) BroadcastCancelledTasks(ctx context.Context, workspaceID string, cancelled []db.AgentTaskQueue) {
	s.BroadcastCancelledTasksWithEvents(ctx, workspaceID, cancelled, nil)
}

// BroadcastCancelledTasksWithEvents 执行批量取消的提交后效应，并发布调用方在自己事务里记录的持久信封。
// 不传信封时按幂等键回查持久化任务事件，覆盖“任务链接被本次提交本身删除”的拆除路径。
func (s *TaskService) BroadcastCancelledTasksWithEvents(ctx context.Context, workspaceID string, cancelled []db.AgentTaskQueue, persisted []events.Event) {
	byTask := make(map[string]events.Event, len(persisted))
	for _, event := range persisted {
		if event.TaskID != "" {
			byTask[event.TaskID] = event
		}
	}
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		s.ReconcileAgentStatus(ctx, t.AgentID)
		if event, ok := byTask[util.UUIDToString(t.ID)]; ok {
			publishCommittedEvent(s.Bus, event)
		} else {
			s.publishTaskEventAfterCommit(ctx, protocol.EventTaskCancelled, workspaceID, t)
		}
	}
	s.notifyTasksFinished(cancelled)
}

// BroadcastTaskQueued 为客户端发出提交后的队列失效通知（task:queued）。
func (s *TaskService) BroadcastTaskQueued(ctx context.Context, task db.AgentTaskQueue) {
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	s.publishTaskEventAfterCommit(ctx, protocol.EventTaskQueued, workspaceID, task)
}

// CaptureCancelledTasks 为一次已提交的批量取消记录分析数据。它刻意【不】结算委托失败恢复：执行到这里时取消
// 已提交，此处失败无法回滚，搁浅的回执将永远无法修复。调用方须在“执行取消的那个事务内”做结算。
func (s *TaskService) CaptureCancelledTasks(ctx context.Context, cancelled []db.AgentTaskQueue) {
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
	}
}

// CancelledChatMessageResult 描述被取消聊天任务的用户输入行处置结果（恢复为草稿 / 写“已停止”消息）。
type CancelledChatMessageResult struct {
	ChatSessionID  string
	MessageID      string
	Content        string
	RestoreToInput bool
	// Attachments 是从被删除用户消息上摘下的附件行——它们靠 ON DELETE CASCADE 之外的显式摘除存活，
	// 以便恢复后的草稿重新发送时重新绑定。
	Attachments []db.Attachment
}

// CancelTaskResult 携带单任务取消的完整结果：任务行，以及聊天场景下用户可见的取消处置。
type CancelTaskResult struct {
	Task                 db.AgentTaskQueue
	CancelledChatMessage *CancelledChatMessageResult
}

// ErrTaskNoLongerQueued 表示队列编辑/删除目标已不是 queued（已被认领或取消），CAS 未命中。
var ErrTaskNoLongerQueued = errors.New("task is no longer queued")

// CancelTaskOptions 携带调用方关于“请求取消的客户端”所知道的信息。
type CancelTaskOptions struct {
	// ClientSupportsDraftRestore=true 表示调用方可通过 durable 草稿恢复路径拿回提示词（#5219）。只有这种客户端
	// 才会拿到“延迟判定”的结果；其它客户端保持同步的空转录判定——取消响应是它们取回提示词的唯一机会。
	// 见 protocol.AppCapabilityChatDraftRestoreV1。
	ClientSupportsDraftRestore bool
	// QueuedOnly 把队列编辑/删除变成按会话收敛的 compare-and-set（配合 ExpectedChatSession），只作用于仍在
	// 排队的聊天任务。
	QueuedOnly          bool
	ExpectedChatSession pgtype.UUID
	QueueAction         string
	// ErrorMessage / FailureReason 设置时会持久化到被取消行上。仅用于“用户未主动要求”的取消（例如 worktree
	// 认领闸门拒绝过老的 daemon）：用户发起的取消无需解释，但服务器发起的取消若不落原因，会以无从解释的
	// “cancelled”呈现，唯一痕迹只是用户永远看不到的 daemon 日志里的 4xx。
	ErrorMessage  string
	FailureReason string
	// UserInitiated 区分 issue UI/API 的取消操作与自动服务器修复。显式用户取消会终结性地确认任务中计划的任何
	// 委托失败恢复信号；自动取消必须让该信号保持可重放。
	UserInitiated bool
}

// CancelTask 为自动服务器路径按 ID 取消单条任务。它【不】确认委托失败恢复输入；显式取消 issue 任务请用
// CancelTaskByUser。会广播 task:cancelled 让前端立即更新。
func (s *TaskService) CancelTask(ctx context.Context, taskID pgtype.UUID) (*db.AgentTaskQueue, error) {
	// 本包装的调用方都是自动化的非聊天修复/daemon 路径，因此 finalizeCancelledChatMessage 在门槛被读到前就
	// 返回了。即便真有 chat 任务走到这里，也没有客户端在等同步恢复；durable 路径是唯一能把提示词还回去的方式。
	result, err := s.CancelTaskWithResult(ctx, taskID, CancelTaskOptions{ClientSupportsDraftRestore: true})
	if err != nil {
		return nil, err
	}
	return &result.Task, nil
}

// CancelTaskByUser 是显式 issue 任务取消路径。与自动服务器取消不同，它终结性确认任务携带的任何委托失败
// 恢复信号，让清扫器尊重用户决定而不是重建任务。
func (s *TaskService) CancelTaskByUser(ctx context.Context, taskID pgtype.UUID) (*db.AgentTaskQueue, error) {
	result, err := s.CancelTaskWithResult(ctx, taskID, CancelTaskOptions{
		ClientSupportsDraftRestore: true,
		UserInitiated:              true,
	})
	if err != nil {
		return nil, err
	}
	return &result.Task, nil
}

// CancelTaskWithReason 取消一条由【服务器】决定停止的任务，并把可操作的原因持久化到行上。它与 CancelTask
// 走完全相同的完整取消流程——审计捕获、chat 处置、agent 状态调和、task:cancelled 广播、NotifyTaskFinished——
// 因为直接裸用 CancelAgentTaskWithReason 查询会让 agent 状态 pill 继续转、卡片陈旧，容量/串行等待方也无人唤醒。
func (s *TaskService) CancelTaskWithReason(ctx context.Context, taskID pgtype.UUID, errorMessage, failureReason string) (*db.AgentTaskQueue, error) {
	result, err := s.CancelTaskWithResult(ctx, taskID, CancelTaskOptions{
		ClientSupportsDraftRestore: true,
		ErrorMessage:               errorMessage,
		FailureReason:              failureReason,
	})
	if err != nil {
		return nil, err
	}
	return &result.Task, nil
}

// CancelTaskWithResult 取消单条任务并返回用户面向调用方需要的聊天专用清理结果。
func (s *TaskService) CancelTaskWithResult(ctx context.Context, taskID pgtype.UUID, opts CancelTaskOptions) (*CancelTaskResult, error) {
	// 两字段都会持久化到下方被取消行的 TEXT 列，且至少一个调用方把外部传入的路径插进 ErrorMessage。任一含 NUL
	// 都会让取消回滚、任务继续跑——与 GH #7098 在 fail/complete 路径上相同的卡死形态。
	opts.ErrorMessage = util.SanitizeTextForPostgres(opts.ErrorMessage)
	opts.FailureReason = util.SanitizeTextForPostgres(opts.FailureReason)

	if opts.UserInitiated && (opts.ErrorMessage != "" || opts.FailureReason != "") {
		return nil, errors.New("user-initiated cancellation cannot carry a server failure reason")
	}
	var (
		task                 db.AgentTaskQueue
		cancelledChatMessage *CancelledChatMessageResult
		terminalEvent        events.Event
		err                  error
	)
	if opts.QueuedOnly {
		if opts.QueueAction != "edit" && opts.QueueAction != "remove" {
			return nil, errors.New("queue action must be edit or remove")
		}
		err = s.runInTx(ctx, func(qtx *db.Queries) error {
			if _, err := qtx.LockChatSessionForTask(ctx, taskID); err != nil {
				return fmt.Errorf("lock queued chat session: %w", err)
			}
			task, err = qtx.CancelQueuedAgentTask(ctx, db.CancelQueuedAgentTaskParams{
				ID:            taskID,
				ChatSessionID: opts.ExpectedChatSession,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTaskNoLongerQueued
			}
			if err != nil {
				return fmt.Errorf("cancel queued task: %w", err)
			}
			cancelledChatMessage, err = s.settleQueuedChatInput(ctx, qtx, task, opts.QueueAction)
			if err != nil {
				return err
			}
			if s.TxStarter != nil {
				terminalEvent, err = recordTaskTerminalEventTx(ctx, qtx, protocol.EventTaskCancelled, task,
					map[string]any{"queue_action": opts.QueueAction})
				if err != nil {
					return fmt.Errorf("record queued task cancellation event: %w", err)
				}
			}
			return err
		})
	} else {
		// 状态翻转与 chat 恢复指针的推进必须一起提交。拆成两条语句时，指针还指向上一回合的会话，`cancelled` 状态
		// 却已对其它连接可见，排队的后续消息可能据此恢复旧会话。
		err = s.runInTx(ctx, func(qtx *db.Queries) error {
			if err := lockChatSessionForTaskWrite(ctx, qtx, taskID); err != nil {
				return err
			}
			var (
				cancelled db.AgentTaskQueue
				err       error
			)
			if opts.UserInitiated {
				cancelled, err = qtx.CancelAgentTaskByUser(ctx, taskID)
			} else if opts.ErrorMessage != "" || opts.FailureReason != "" {
				cancelled, err = qtx.CancelAgentTaskWithReason(ctx, db.CancelAgentTaskWithReasonParams{
					ID:            taskID,
					Error:         pgtype.Text{String: opts.ErrorMessage, Valid: opts.ErrorMessage != ""},
					FailureReason: pgtype.Text{String: opts.FailureReason, Valid: opts.FailureReason != ""},
				})
			} else {
				cancelled, err = qtx.CancelAgentTask(ctx, taskID)
			}
			if err != nil {
				return err
			}
			task = cancelled
			// CancelAgentTaskByUser 在同一条语句里追加恢复回执，因此返回的行已携带它。
			if err := SettleDeliveredDelegatedFailureRecoveries(ctx, qtx, cancelled); err != nil {
				return err
			}
			if !cancelled.ChatSessionID.Valid {
				if s.TxStarter != nil {
					terminalEvent, err = recordTaskTerminalEventTx(ctx, qtx, protocol.EventTaskCancelled, cancelled,
						map[string]any{"user_initiated": opts.UserInitiated})
					if err != nil {
						return fmt.Errorf("record task cancellation event: %w", err)
					}
				}
				return nil
			}
			if err := qtx.AdvanceCancelledChatSessionPointer(ctx, cancelled.ID); err != nil {
				return err
			}
			if s.TxStarter != nil {
				terminalEvent, err = recordTaskTerminalEventTx(ctx, qtx, protocol.EventTaskCancelled, cancelled,
					map[string]any{"user_initiated": opts.UserInitiated})
				if err != nil {
					return fmt.Errorf("record task cancellation event: %w", err)
				}
			}
			return nil
		})
	}
	if errors.Is(err, ErrTaskNoLongerQueued) {
		return nil, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err := s.Queries.GetAgentTask(ctx, taskID)
		if err != nil {
			return nil, fmt.Errorf("cancel task: %w", err)
		}
		return &CancelTaskResult{Task: existing}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cancel task: %w", err)
	}

	slog.Info("task cancelled", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
	s.captureTaskCancelled(ctx, task)
	// 修改 6. 取消链原来只翻任务状态，批次会继续占着唯一的活动批次索引直到租约过期。
	s.closeUpstreamRuns(ctx, task, "interrupted", "任务已取消，批次已关闭")
	if !opts.QueuedOnly {
		cancelledChatMessage = s.finalizeCancelledChatMessage(ctx, task, opts)
	}

	// 调和 agent 状态
	s.ReconcileAgentStatus(ctx, task.AgentID)

	// 提交后广播持久化的取消事件。兜底分支让未注入事务启动器的直连 service 测试夹具仍可用。
	if terminalEvent.ID != "" {
		publishCommittedEvent(s.Bus, terminalEvent)
	} else {
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, task)
	}
	s.NotifyTaskFinished(task)

	return &CancelTaskResult{
		Task:                 task,
		CancelledChatMessage: cancelledChatMessage,
	}, nil
}

// CancelQueuedChatTasks 原子地取消聊天会话里所有排队的后续消息。会话锁保持删除路径的 session → agent →
// task 顺序；agent 锁阻止 ClaimTask 在更新中途把某行提升走。
func (s *TaskService) CancelQueuedChatTasks(ctx context.Context, sessionID, agentID pgtype.UUID) error {
	var tasks []db.AgentTaskQueue
	var persisted []events.Event
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		if _, err := qtx.LockChatSessionForDelete(ctx, sessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("lock chat session: %w", err)
		}
		if _, err := qtx.GetAgentForClaimUpdate(ctx, agentID); err != nil {
			return fmt.Errorf("lock chat agent: %w", err)
		}
		var err error
		tasks, err = qtx.CancelQueuedAgentTasksForSession(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("cancel queued chat tasks: %w", err)
		}
		if err := SettleDeliveredDelegatedFailureRecoveries(ctx, qtx, tasks...); err != nil {
			return err
		}
		for _, task := range tasks {
			if _, err := s.settleQueuedChatInput(ctx, qtx, task, "remove"); err != nil {
				return err
			}
		}
		if s.TxStarter != nil {
			var err error
			persisted, err = enqueueTaskTerminalEvents(ctx, qtx, protocol.EventTaskCancelled, tasks)
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	for _, task := range tasks {
		slog.Info("task cancelled", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
		s.captureTaskCancelled(ctx, task)
	}
	if len(tasks) > 0 {
		s.ReconcileAgentStatus(ctx, agentID)
	}
	if s.TxStarter != nil {
		for _, event := range persisted {
			publishCommittedEvent(s.Bus, event)
		}
	} else {
		for _, task := range tasks {
			s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, task)
		}
	}
	s.notifyTasksFinished(tasks)
	return nil
}

// lockChatSessionForTaskWrite 对任务所属的 chat_session 行加锁。它必须是任何“最终会同时持有该会话行与任务
// 行”的事务的首条语句——聊天任务的每个终态路径皆然：完成、失败、取消、取消回合收尾，以及 daemon 的飞行中
// pin。
//
// chat_session → agent_task_queue 是仓库全局顺序（见 chat.sql 的 LockChatSessionForTask）。DeleteChatSession
// 与 FinalizeDeferredCancelledChat 已按此取锁；终态上报此前没有，因为它们只先写任务行后写会话行、且没有其它
// 路径同时争用两者。一旦取消与 pin 路径也开始持有会话行，“先任务”与“先会话”并存，任何交叉组合都可能死锁——
// PostgreSQL 会用 40P01 中止其中一个，而 runInTx 没有重试。这是无法按路径局部修复的：要么所有写入方统一
// 顺序，要么谁都不安全。
//
// ErrNoRows 表示没有可锁的东西——非 chat 任务，或会话已被删除（FK 把该列置 NULL）——调用方因此也没有需要
// 保护的会话写。
func lockChatSessionForTaskWrite(ctx context.Context, qtx *db.Queries, taskID pgtype.UUID) error {
	if _, err := qtx.LockChatSessionForTask(ctx, taskID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock chat session for task write: %w", err)
	}
	return nil
}

// chatInputOwnerID 解析任务用户消息输入批次所挂的 id：已设置时用 chat_input_task_id（自动重试克隆继承父任务
// 的，使溯源检查能达父任务的封存消息），否则回退到任务自身 id（兼容旧行）。
func chatInputOwnerID(task db.AgentTaskQueue) pgtype.UUID {
	if task.ChatInputTaskID.Valid {
		return task.ChatInputTaskID
	}
	return task.ID
}

// createAssistantChatMessage 写入一条 assistant 结果消息，并在调用方事务内把“新可见的排队 direct head”重新
// 锚定。两条语句必须同处一个事务，这是转录顺序边界：不能让读者看到回复已提交、而下一个 head 仍带着更早的
// 入队时间。调用方必须确保结算中的任务已移出可见 head 状态集后再调用本助手；完成与失败在同一事务里更早
// 结算，取消则在它自己先前的事务里提交该状态（#5219）。否则 head 查询仍会选中结算中的任务，重锚定其后继
// 便成为 no-op。
func createAssistantChatMessage(ctx context.Context, qtx *db.Queries, params db.CreateChatMessageParams) (db.ChatMessage, error) {
	if params.Role != "assistant" {
		return db.ChatMessage{}, fmt.Errorf("create assistant chat message: invalid role %q", params.Role)
	}
	row, err := qtx.CreateChatMessage(ctx, params)
	if err != nil {
		return db.ChatMessage{}, err
	}
	if err := qtx.ReanchorNextQueuedDirectChatInput(ctx, db.ReanchorNextQueuedDirectChatInputParams{
		AssistantCreatedAt: row.CreatedAt,
		ChatSessionID:      row.ChatSessionID,
	}); err != nil {
		return db.ChatMessage{}, fmt.Errorf("reanchor next queued direct chat input: %w", err)
	}
	return row, nil
}

// settleQueuedChatInput 处理被取消的排队聊天输入：若该任务已摄入频道消息（不可恢复，写一条 “Stopped.”
// assistant 消息封口）；否则按 action=edit/remove 摘附件、删除用户输入行并构造 CancelledChatMessageResult；
// edit 场景把删掉的内容（含附件）另存为草稿恢复行供重新发送。无用户输入（ErrNoRows）时返回 nil。
func (s *TaskService) settleQueuedChatInput(
	ctx context.Context,
	qtx *db.Queries,
	task db.AgentTaskQueue,
	action string,
) (*CancelledChatMessageResult, error) {
	if !task.ChatSessionID.Valid {
		return nil, nil
	}
	inputOwnerID := chatInputOwnerID(task)
	channelIngested, err := qtx.TaskHasChannelIngestedMessages(ctx, inputOwnerID)
	if err != nil {
		return nil, fmt.Errorf("check queued chat channel provenance: %w", err)
	}
	if channelIngested {
		if _, err := createAssistantChatMessage(ctx, qtx, db.CreateChatMessageParams{
			ID:            dbid.NewV7(),
			ChatSessionID: task.ChatSessionID,
			Role:          "assistant",
			Content:       "Stopped.",
			TaskID:        task.ID,
			ElapsedMs:     computeChatElapsedMs(task),
		}); err != nil {
			return nil, fmt.Errorf("create cancelled queued chat message: %w", err)
		}
		return nil, nil
	}

	var detached []db.Attachment
	if action == "edit" {
		detached, err = qtx.DetachAttachmentsFromUserChatMessageByTask(ctx, inputOwnerID)
		if err != nil {
			return nil, fmt.Errorf("detach edited queued chat attachments: %w", err)
		}
	}
	deleted, err := deleteUserChatInput(ctx, qtx, inputOwnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("delete queued chat input: %w", err)
	}
	cancelled := &CancelledChatMessageResult{
		ChatSessionID: util.UUIDToString(deleted.ChatSessionID),
		MessageID:     util.UUIDToString(deleted.ID),
		Content:       deleted.Content,
	}
	if action == "remove" {
		return cancelled, nil
	}

	attachmentIDs := make([]pgtype.UUID, 0, len(detached))
	for _, attachment := range detached {
		attachmentIDs = append(attachmentIDs, attachment.ID)
	}
	if _, err := qtx.CreateChatDraftRestore(ctx, db.CreateChatDraftRestoreParams{
		ID:            deleted.ID,
		ChatSessionID: task.ChatSessionID,
		TaskID:        task.ID,
		Content:       deleted.Content,
		AttachmentIds: attachmentIDs,
	}); err != nil {
		return nil, fmt.Errorf("create queued chat draft restore: %w", err)
	}
	cancelled.RestoreToInput = true
	cancelled.Attachments = detached
	return cancelled, nil
}

// deleteUserChatInput 删除被取消/编辑回合的成员输入，并释放该回合收养的 onboarding kickoff——优先交给会话的
// 下一条排队回合，否则退回无人认领（MUL-5827）。两步必须一起做：只删输入而让 kickoff 仍绑在已死任务上，
// 会把 onboarding 上下文搁浅在永远不会发生的运行里。
func deleteUserChatInput(ctx context.Context, qtx *db.Queries, inputOwnerID pgtype.UUID) (db.ChatMessage, error) {
	if err := qtx.ReleaseOnboardingKickoffFromTask(ctx, inputOwnerID); err != nil {
		return db.ChatMessage{}, fmt.Errorf("release onboarding kickoff: %w", err)
	}
	return qtx.DeleteUserChatMessageByTask(ctx, inputOwnerID)
}

// finalizeCancelledChatMessage 在取消提交后处理聊天任务的用户输入：判定可恢复性（无转录则删输入并生成草稿
// 恢复；否则写一条 “Stopped.”）。对“已开始但当前为空”且客户端支持 durable 恢复的任务，先标记延迟判定
// （由 FinalizeDeferredCancelledChat 在 daemon 确认冲刷后结算），避免把仍在途的转录尾巴误判为空。
func (s *TaskService) finalizeCancelledChatMessage(ctx context.Context, task db.AgentTaskQueue, opts CancelTaskOptions) *CancelledChatMessageResult {
	if !task.ChatSessionID.Valid {
		return nil
	}
	var cancelled *CancelledChatMessageResult
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		// 与其它终态路径相同协议：本事务先标记任务行，再写 chat_message 行（其 FK 会在会话上取 KEY SHARE 锁）——
		// 再次涉及两行，所以仍先锁会话。
		if err := lockChatSessionForTaskWrite(ctx, qtx, task.ID); err != nil {
			return err
		}
		messages, err := qtx.ListTaskMessages(ctx, task.ID)
		if err != nil {
			return fmt.Errorf("list cancelled chat task messages: %w", err)
		}
		restorable := len(messages) == 0
		if restorable {
			// 频道摄入的用户消息是平台发送方所写内容的持久记录——发送方没有可恢复草稿的 Multica 编辑器。判据是
			// 不可变的逐消息 channel_ingested 戳，而非 channel_chat_session_binding 行：归档会话或重绑安装会删除
			// 绑定，而消息（及仍可取消的任务）仍在。按键是输入批次归属者 id，使自动重试克隆（继承
			// chat_input_task_id）与父任务得出相同结论。频道任务走下方 “Stopped.” 结算，而不是删除其封存的
			// 输入批次。
			channelIngested, err := qtx.TaskHasChannelIngestedMessages(ctx, chatInputOwnerID(task))
			if err != nil {
				return fmt.Errorf("check cancelled chat channel provenance: %w", err)
			}
			restorable = !channelIngested
		}
		if restorable && task.StartedAt.Valid && opts.ClientSupportsDraftRestore {
			// 已开始任务的 daemon 靠轮询得知取消，且可能仍在冲刷转录尾巴，此刻的 “空” 并不可信。把判定推迟到
			// daemon 确认冲刷（cancel-ack）或清扫器宽限期到期（#5219）。“非空”无需推迟：迟到的行只会追加。
			//
			// 是否延迟以客户端能力为门槛：客户端与服务器不会同步升级；读不了 durable 恢复的客户端会把空取消响应
			// 当作 “没有可放回的”，从而丢失提示词。这类客户端落到下方旧式同步分支——保留 #5219 前的竞态（在途
			// 转录尾巴仍可能被误判为空）——这正好与旧服务器的行为一致，且严格优于直接丢弃输入。
			if _, err := qtx.MarkChatFinalizeDeferred(ctx, task.ID); err != nil {
				return fmt.Errorf("mark chat finalize deferred: %w", err)
			}
			return nil
		}
		if restorable {
			inputOwnerID := chatInputOwnerID(task)
			// 先摘附件、后删用户消息——附件 FK 是 ON DELETE CASCADE，先删会毁掉恢复草稿需要重绑的行。
			detached, err := qtx.DetachAttachmentsFromUserChatMessageByTask(ctx, inputOwnerID)
			if err != nil {
				return fmt.Errorf("detach cancelled chat message attachments: %w", err)
			}
			deleted, err := deleteUserChatInput(ctx, qtx, inputOwnerID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("delete empty cancelled chat user message: %w", err)
			}
			// 现在必定可恢复：删除不会返回 kickoff 行，返回的总是成员亲手输入的内容（MUL-5827）。
			cancelled = &CancelledChatMessageResult{
				ChatSessionID:  util.UUIDToString(deleted.ChatSessionID),
				MessageID:      util.UUIDToString(deleted.ID),
				Content:        deleted.Content,
				RestoreToInput: true,
				Attachments:    detached,
			}
			return nil
		}
		if _, err := createAssistantChatMessage(ctx, qtx, db.CreateChatMessageParams{
			ID:            dbid.NewV7(),
			ChatSessionID: task.ChatSessionID,
			Role:          "assistant",
			Content:       "Stopped.",
			TaskID:        task.ID,
			ElapsedMs:     computeChatElapsedMs(task),
		}); err != nil {
			return fmt.Errorf("create cancelled chat message: %w", err)
		}
		return nil
	}); err != nil {
		slog.Error("failed to finalize cancelled chat message",
			"task_id", util.UUIDToString(task.ID),
			"chat_session_id", util.UUIDToString(task.ChatSessionID),
			"error", err,
		)
		return nil
	}
	return cancelled
}

// RebroadcastCancelledTask 在“终态后的投递信息落到任务行上”之后（cancel-ack 的分支名或保留工作树错误）
// 重新广播一条已取消任务。原始 task:cancelled 广播发生在取消时刻、早于 daemon 的 ack——客户端可能已拉取过
// 不带该投递信息的行，且不会自行再拉。消费方把 task:cancelled 视为幂等缓存失效，因此重放是安全的。
func (s *TaskService) RebroadcastCancelledTask(ctx context.Context, taskID pgtype.UUID) {
	task, err := s.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		slog.Warn("rebroadcast cancelled task: load failed",
			"task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	if task.Status != "cancelled" {
		// complete/fail 回调已发布过携带行最终字段的自身终态事件，没有陈旧信息需要刷新。
		return
	}
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	s.publishTaskEventAfterCommit(ctx, protocol.EventTaskCancelled, workspaceID, task)
}

// FinalizeDeferredCancelledChat 结算 finalizeCancelledChatMessage 为“已开始但为空”的取消聊天任务推迟的
// 空/非空判定（#5219）。由 daemon 的 cancel-ack（转录冲刷完成）与清扫器宽限期兜底调用；标记认领是原子的，
// 并发调用方不会把同一任务结算两次，无 pending 标记的调用是 no-op。结算结果以 chat:cancel_finalized 广播——
// 取消的 HTTP 响应早已返回。
func (s *TaskService) FinalizeDeferredCancelledChat(ctx context.Context, taskID pgtype.UUID) bool {
	var (
		task          db.AgentTaskQueue
		payload       protocol.ChatCancelFinalizedPayload
		terminalEvent events.Event
		changed       bool
		settled       bool
	)
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		// 先锁任务的 chat_session。chat_draft_restore 没有 FK（MUL-3515），下面这条插入不会自行在会话上加锁——
		// 否则，恰在我们提交前清扫了该表的 workspace/agent/session 删除会把我们的恢复行（存着用户提示词）
		// 永久变成孤儿。删除方清扫前取同一把锁，因此必然有一方阻塞：要么他们等待、清扫时看到我们的行，要么我们
		// 等待、发现没有会话可恢复。先于任务认领锁会话也固定了全局加锁顺序（chat_session → agent_task_queue），
		// 避免与删除方的级联死锁。
		_, err := qtx.LockChatSessionForTask(ctx, taskID)
		sessionGone := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !sessionGone {
			return fmt.Errorf("lock chat session for deferred finalize: %w", err)
		}

		// 在结算事务内认领标记：结算失败时认领一并回滚，清扫器可重试，而不是留下“标记已清但结果未结算”的任务。
		// 行锁仍把 daemon ack 与清扫器串行化——输家的 UPDATE 会阻塞到赢家提交，之后匹配不到行（ErrNoRows）——
		// 因此同一任务绝不会被结算两次。
		claimed, err := qtx.ClaimChatFinalizeDeferred(ctx, taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim deferred chat finalize: %w", err)
		}
		changed = true
		task = claimed
		if sessionGone {
			// 会话已级联删除（其 FK 反正会把下面的列置 NULL）：没有转录可结算，也没有可放恢复的位置。上面的认领
			// 已清掉标记，清扫器会停止重试。
			return nil
		}
		if !claimed.ChatSessionID.Valid {
			return nil
		}
		settled = true
		payload.ChatSessionID = util.UUIDToString(claimed.ChatSessionID)
		payload.TaskID = util.UUIDToString(claimed.ID)
		payload.InitiatorUserID = util.UUIDToString(claimed.InitiatorUserID)

		messages, err := qtx.ListTaskMessages(ctx, claimed.ID)
		if err != nil {
			return fmt.Errorf("list cancelled chat task messages: %w", err)
		}
		restorable := len(messages) == 0
		if restorable {
			// 与 finalizeCancelledChatMessage 相同的不可变来源守卫：频道任务绝不“恢复-删除”其封存输入。同步路径
			// 已不再延迟这类任务；这里是兜底滚动部署期间旧副本创建的标记。
			channelIngested, err := qtx.TaskHasChannelIngestedMessages(ctx, chatInputOwnerID(claimed))
			if err != nil {
				return fmt.Errorf("check cancelled chat channel provenance: %w", err)
			}
			restorable = !channelIngested
		}
		if restorable {
			inputOwnerID := chatInputOwnerID(claimed)
			// 转录在 daemon 冲刷后仍为空：与同步空分支结局相同，但取消 HTTP 响应早已返回，广播只是 best-effort。
			// 恢复在本事务内持久化，由“创建者授权”的 draft-restores 端点提供，错过事件的客户端下次打开会话仍能
			// 取回；事件本身不含内容，只是失效提示。
			detached, err := qtx.DetachAttachmentsFromUserChatMessageByTask(ctx, inputOwnerID)
			if err != nil {
				return fmt.Errorf("detach cancelled chat message attachments: %w", err)
			}
			deleted, err := deleteUserChatInput(ctx, qtx, inputOwnerID)
			if errors.Is(err, pgx.ErrNoRows) {
				payload.Outcome = ""
				return nil
			}
			if err != nil {
				return fmt.Errorf("delete empty cancelled chat user message: %w", err)
			}
			attachmentIDs := make([]pgtype.UUID, 0, len(detached))
			for _, a := range detached {
				attachmentIDs = append(attachmentIDs, a.ID)
			}
			if _, err := qtx.CreateChatDraftRestore(ctx, db.CreateChatDraftRestoreParams{
				ID:            deleted.ID,
				ChatSessionID: claimed.ChatSessionID,
				TaskID:        claimed.ID,
				Content:       deleted.Content,
				AttachmentIds: attachmentIDs,
			}); err != nil {
				return fmt.Errorf("create chat draft restore: %w", err)
			}
			payload.Outcome = protocol.ChatCancelOutcomeRestored
			payload.MessageID = util.UUIDToString(deleted.ID)
		} else {
			row, err := createAssistantChatMessage(ctx, qtx, db.CreateChatMessageParams{
				ID:            dbid.NewV7(),
				ChatSessionID: claimed.ChatSessionID,
				Role:          "assistant",
				Content:       "Stopped.",
				TaskID:        claimed.ID,
				ElapsedMs:     computeChatElapsedMs(claimed),
				FailureReason: claimed.FailureReason,
			})
			if err != nil {
				return fmt.Errorf("create cancelled chat message: %w", err)
			}
			payload.Outcome = protocol.ChatCancelOutcomeStopped
			payload.MessageID = util.UUIDToString(row.ID)
			payload.Content = row.Content
			payload.MessageKind = row.MessageKind
			if row.CreatedAt.Valid {
				payload.CreatedAt = row.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
			}
			if row.ElapsedMs.Valid {
				payload.ElapsedMs = row.ElapsedMs.Int64
			}
		}
		if s.TxStarter != nil && payload.Outcome != "" {
			workspaceID, workspaceErr := resolveTaskWorkspaceWithQueries(ctx, qtx, claimed)
			if workspaceErr != nil || workspaceID == "" {
				if workspaceErr == nil {
					workspaceErr = errors.New("task has no workspace")
				}
				return fmt.Errorf("resolve cancelled chat workspace: %w", workspaceErr)
			}
			terminalEvent, err = recordDurableEventTx(ctx, qtx, events.Event{
				Type:           protocol.EventChatCancelFinalized,
				IdempotencyKey: "chat-cancel-finalized:" + util.UUIDToString(claimed.ID),
				StreamKey:      "chat:" + util.UUIDToString(claimed.ChatSessionID),
				WorkspaceID:    workspaceID,
				ActorType:      "system",
				TaskID:         util.UUIDToString(claimed.ID),
				ChatSessionID:  util.UUIDToString(claimed.ChatSessionID),
				Payload:        payload,
			})
			if err != nil {
				return fmt.Errorf("record cancelled chat finalization event: %w", err)
			}
		}
		return nil
	}); err != nil {
		slog.Error("failed to finalize deferred cancelled chat",
			"task_id", util.UUIDToString(taskID),
			"error", err,
		)
		return false
	}
	if !settled || payload.Outcome == "" {
		return changed
	}
	if terminalEvent.ID != "" {
		publishCommittedEvent(s.Bus, terminalEvent)
	} else {
		s.broadcastChatCancelFinalized(ctx, task, payload)
	}
	return changed
}

// broadcastChatCancelFinalized 以内存事件形式向工作区广播 chat:cancel_finalized（无事务启动器的夹具路径）。
func (s *TaskService) broadcastChatCancelFinalized(ctx context.Context, task db.AgentTaskQueue, payload protocol.ChatCancelFinalizedPayload) {
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	if workspaceID == "" {
		return
	}
	s.Bus.Publish(events.Event{
		Type:          protocol.EventChatCancelFinalized,
		WorkspaceID:   workspaceID,
		ActorType:     "system",
		ActorID:       "",
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		Payload:       payload,
	})
}

// ClaimTask 在当前 runtime 上为某 agent 原子认领下一条排队任务，并遵守 max_concurrent_tasks 上限。
func (s *TaskService) ClaimTask(ctx context.Context, agentID pgtype.UUID) (*db.AgentTaskQueue, error) {
	return s.claimTask(ctx, agentID, pgtype.UUID{}, claimScope{claimPath: claimPathAgent})
}

// claimTask 是 daemon 轮询路径使用的按 runtime 收敛的认领原语。对外包装 ClaimTask 不传 runtimeID，因此解析
// agent 当前绑定的 runtime。把 SQL 认领本身收敛到 runtime，可防止 runtime A 上一个离线的候选 agent 导致该
// agent 在 runtime B 上的任务被派发后又被调用方的 runtime 守卫丢弃。
func (s *TaskService) claimTask(ctx context.Context, agentID, runtimeID pgtype.UUID, scope claimScope) (*db.AgentTaskQueue, error) {
	if blocked, err := s.releaseClaimsBlocked(ctx); err != nil || blocked {
		return nil, err
	}
	start := time.Now()
	outcome := "unknown"
	var getAgentMs, countRunningMs, claimAgentMs, reanchorMs, updateStatusMs, dispatchMs int64
	var claimed *db.AgentTaskQueue
	var reclaimCheckAfter time.Time
	var dispatchEvent events.Event
	var decisionDetail claimDecisionDetail
	defer func() {
		s.maybeLogClaimSlow(agentID, outcome, start, getAgentMs, countRunningMs, claimAgentMs, reanchorMs, updateStatusMs, dispatchMs)
		s.recordClaimDecision(agentID, runtimeID, outcome, scope, decisionDetail)
	}()

	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		t0 := time.Now()
		agent, err := qtx.GetAgentForClaimUpdate(ctx, agentID)
		getAgentMs = time.Since(t0).Milliseconds()
		if err != nil {
			outcome = "error_get_agent"
			return fmt.Errorf("agent not found: %w", err)
		}
		claimRuntimeID := runtimeID
		if !claimRuntimeID.Valid {
			claimRuntimeID = agent.RuntimeID
		}
		if !claimRuntimeID.Valid {
			outcome = "no_runtime"
			return nil
		}
		// agent 重绑后 daemon 可能仍握着陈旧候选。在算容量前先拒绝。ClaimAgentTask 会在状态转换前重复这道围栏；
		// 认领 handler 返回任何载荷前还会再检查一遍刚加载的 Agent。runtime 变更的拆除负责串行化并结算剩余的排队行。
		if runtimeID.Valid && agent.RuntimeID != runtimeID {
			outcome = "runtime_mismatch"
			return nil
		}

		t0 = time.Now()
		running, err := qtx.CountRunningTasks(ctx, agentID)
		countRunningMs = time.Since(t0).Milliseconds()
		if err != nil {
			outcome = "error_count_running"
			return fmt.Errorf("count running tasks: %w", err)
		}
		if running >= int64(agent.MaxConcurrentTasks) {
			decisionDetail = claimDecisionDetail{running: running, maxConcurrent: int64(agent.MaxConcurrentTasks)}
			outcome = "no_capacity"
			return nil
		}

		t0 = time.Now()
		reclaimCheckAfter = t0.Add(claimResponseRecoveryWindow + ReclaimCheckHintSafetyMargin)
		task, err := qtx.ClaimAgentTask(ctx, db.ClaimAgentTaskParams{
			AgentID:          agentID,
			RuntimeID:        claimRuntimeID,
			PrepareLeaseSecs: prepareLeaseDuration.Seconds(),
			RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
		})
		claimAgentMs = time.Since(t0).Milliseconds()
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				outcome = "no_tasks"
				return nil
			}
			outcome = "error_claim"
			return fmt.Errorf("claim task: %w", err)
		}

		// 一条空闲的、任务自有的 direct-chat 行可能已作为位置队列的 head 可见。正常完成会在提交前把后继者重锚到
		// 助手结果旁；这条认领时查询是对旧行或乱序行的兼容兜底。它只在认领事务内移动该行，因此一旦派发，全量
		// 列表与游标读者都能看到修正后的边界。重试子任务保持根输入归属者，陈旧派发回收走独立查询，这两条路径
		// 都不会再次移动已可见的回合。
		if task.ChatSessionID.Valid && task.ChatInputTaskID.Valid && task.ChatInputTaskID == task.ID {
			t0 = time.Now()
			if err := qtx.ReanchorClaimedDirectChatInput(ctx, db.ReanchorClaimedDirectChatInputParams{
				DispatchedAt: task.DispatchedAt,
				TaskID:       task.ID,
			}); err != nil {
				reanchorMs = time.Since(t0).Milliseconds()
				outcome = "error_reanchor_chat_input"
				return fmt.Errorf("reanchor claimed direct chat input: %w", err)
			}
			reanchorMs = time.Since(t0).Milliseconds()
		}

		claimedTask := task
		claimed = &claimedTask
		if s.TxStarter != nil {
			dispatchEvent, err = buildTaskDispatchEvent(ctx, qtx, task)
			if err != nil {
				outcome = "error_record_dispatch"
				return fmt.Errorf("build task dispatch event: %w", err)
			}
			dispatchEvent, err = recordDurableEventTx(ctx, qtx, dispatchEvent)
			if err != nil {
				outcome = "error_record_dispatch"
				return fmt.Errorf("record task dispatch event: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		if outcome == "unknown" {
			outcome = "error_transaction"
		}
		return nil, err
	}
	if claimed == nil {
		return nil, nil
	}
	s.trackTaskForReclaim(*claimed, reclaimCheckAfter)

	slog.Info("task claimed", "task_id", util.UUIDToString(claimed.ID), "agent_id", util.UUIDToString(agentID))
	s.captureTaskDispatched(ctx, *claimed)

	// 依据活跃任务刷新 agent 状态，避免在刚取消的认领之后，出现陈旧的、无条件的 working 写入竞态。
	t0 := time.Now()
	s.ReconcileAgentStatus(ctx, agentID)
	updateStatusMs = time.Since(t0).Milliseconds()

	// 在认领事务提交后广播 task:dispatch。生产认领已携带 durable 信封；没有事务启动器的服务保留小嵌入器
	// 使用的内存兼容路径。
	t0 = time.Now()
	if dispatchEvent.ID != "" {
		publishCommittedEvent(s.Bus, dispatchEvent)
	} else {
		s.broadcastTaskDispatch(ctx, *claimed)
	}
	dispatchMs = time.Since(t0).Milliseconds()

	outcome = "claimed"
	return claimed, nil
}

// ClaimTaskForRuntime 为某 runtime 认领下一条可运行任务，同时遵守每个 agent 的 max_concurrent_tasks 上限。
//
// 空认领快路径：配置了 EmptyClaim 且最近一次检查确认该 runtime 无排队任务时，直接返回、不碰 Postgres。
// 缓存会在每次入队时（notifyTaskAvailable）同步失效，因此刚入队的任务在下次调用即可认领，不必等 TTL。
func (s *TaskService) ClaimTaskForRuntime(ctx context.Context, runtimeID pgtype.UUID) (*db.AgentTaskQueue, error) {
	if blocked, err := s.releaseClaimsBlocked(ctx); err != nil || blocked {
		return nil, err
	}
	start := time.Now()
	var (
		outcome          = "no_task"
		listMs, loopMs   int64
		listCount, tried int
		claimedFlag      bool
	)
	defer func() {
		totalMs := time.Since(start).Milliseconds()
		if totalMs < 300 {
			return
		}
		slog.Info("claim_for_runtime slow",
			"runtime_id", util.UUIDToString(runtimeID),
			"outcome", outcome,
			"total_ms", totalMs,
			"list_pending_ms", listMs,
			"list_pending_count", listCount,
			"agents_tried", tried,
			"claim_loop_ms", loopMs,
			"claimed", claimedFlag,
		)
	}()

	runtimeKey := util.UUIDToString(runtimeID)
	if err := s.PromoteDueDeferredTasksForRuntime(ctx, runtimeID); err != nil {
		outcome = "error_promote_deferred"
		s.recordClaimDecision(pgtype.UUID{}, runtimeID, outcome, claimScope{claimPath: claimPathRuntime}, claimDecisionDetail{})
		return nil, err
	}

	// 陈旧响应回收须放在 EmptyClaim 之前，因为只表达 queued 的缓存无法代表 dispatched 工作。ReclaimCheck
	// 独立决定是否跳过该 UPDATE，直到任务提示或有界 DB 兜底到期。
	checkStarted := time.Now()
	if due := s.ReclaimCheck.DueRuntimeIDs(ctx, []string{runtimeKey}, checkStarted); len(due) > 0 {
		reclaimCheckAfter := time.Now().Add(claimResponseRecoveryWindow + ReclaimCheckHintSafetyMargin)
		stale, err := s.Queries.ReclaimStaleDispatchedTaskForRuntime(ctx, db.ReclaimStaleDispatchedTaskForRuntimeParams{
			RuntimeID:         runtimeID,
			ClaimRecoverySecs: claimResponseRecoveryWindow.Seconds(),
			PrepareLeaseSecs:  prepareLeaseDuration.Seconds(),
			RuntimeStaleSecs:  RuntimeClaimFreshnessSeconds,
		})
		if err == nil {
			s.ReclaimCheck.MarkChecked(
				ctx,
				[]string{runtimeKey},
				checkStarted,
				time.Now().Add(ReclaimCheckRetryInterval),
			)
			s.trackTaskForReclaim(stale, reclaimCheckAfter)
			outcome = "reclaimed_dispatched"
			s.recordClaimDecision(stale.AgentID, runtimeID, outcome, claimScope{claimPath: claimPathRuntime}, claimDecisionDetail{})
			claimedFlag = true
			slog.Info("stale dispatched task reclaimed",
				"task_id", util.UUIDToString(stale.ID),
				"runtime_id", runtimeKey,
				"agent_id", util.UUIDToString(stale.AgentID),
			)
			return &stale, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			outcome = "error_reclaim_dispatched"
			s.recordClaimDecision(pgtype.UUID{}, runtimeID, outcome, claimScope{claimPath: claimPathRuntime}, claimDecisionDetail{})
			return nil, fmt.Errorf("reclaim stale dispatched task: %w", err)
		}
		s.ReclaimCheck.MarkChecked(
			ctx,
			[]string{runtimeKey},
			checkStarted,
			time.Now().Add(ReclaimCheckRetryInterval),
		)
	}

	if s.EmptyClaim.IsEmpty(ctx, runtimeKey) {
		outcome = "empty_cache_hit"
		s.recordClaimDecision(pgtype.UUID{}, runtimeID, outcome, claimScope{claimPath: claimPathRuntime}, claimDecisionDetail{})
		return nil, nil
	}

	// 在 SELECT 之前采样失效版本。若并发入队在此读与 SELECT 后的 MarkEmpty 之间 Bump 了版本，下一次 IsEmpty
	// 会看到该空键带的是陈旧版本而拒绝它——从而关掉那个会让刚入队任务一直卡到空键 TTL 过期的竞态。
	preSelectVersion := s.EmptyClaim.CurrentVersion(ctx, runtimeKey)

	t0 := time.Now()
	tasks, err := s.Queries.ListQueuedClaimCandidatesByRuntime(ctx, runtimeID)
	listMs = time.Since(t0).Milliseconds()
	listCount = len(tasks)
	if err != nil {
		outcome = "error_list"
		s.recordClaimDecision(pgtype.UUID{}, runtimeID, outcome, claimScope{claimPath: claimPathRuntime}, claimDecisionDetail{})
		return nil, fmt.Errorf("list queued claim candidates: %w", err)
	}

	if len(tasks) == 0 {
		s.EmptyClaim.MarkEmpty(ctx, runtimeKey, preSelectVersion)
		outcome = "empty_db"
		s.recordClaimDecision(pgtype.UUID{}, runtimeID, outcome, claimScope{claimPath: claimPathRuntime}, claimDecisionDetail{})
		return nil, nil
	}

	loopStart := time.Now()
	triedAgents := map[string]struct{}{}
	// Candidate rows per agent, counted before the loop so each claimTask call
	// can report whether the runtime saw work for that agent (the #8551
	// attribution split between candidates_blocked and no_candidates).
	candidatesByAgent := make(map[string]int, len(tasks))
	for i := range tasks {
		candidatesByAgent[util.UUIDToString(tasks[i].AgentID)]++
	}
	var claimed *db.AgentTaskQueue
	for _, candidate := range tasks {
		agentKey := util.UUIDToString(candidate.AgentID)
		if _, seen := triedAgents[agentKey]; seen {
			continue
		}
		triedAgents[agentKey] = struct{}{}
		tried++

		task, err := s.claimTask(ctx, candidate.AgentID, runtimeID, claimScope{
			claimPath:  claimPathRuntime,
			candidates: candidatesByAgent[agentKey],
		})
		if err != nil {
			loopMs = time.Since(loopStart).Milliseconds()
			outcome = "error_claim"
			return nil, err
		}
		if task != nil && task.RuntimeID == runtimeID {
			claimed = task
			break
		}
	}
	loopMs = time.Since(loopStart).Milliseconds()
	if claimed != nil {
		claimedFlag = true
		outcome = "claimed"
	} else if len(tasks) > 0 {
		// Work was visible for at least one agent but every claim attempt lost
		// (capacity cap, SQL serialization fence, or runtime binding). This is
		// the key signal for attributing queue waits to a fence rather than to
		// an empty queue (#8551).
		slog.Debug("claim poll exhausted candidates without a claim",
			"runtime_id", runtimeKey,
			"candidates", len(tasks),
		)
	}

	return claimed, nil
}

// FinalizeTaskClaim 原子地持久化任务级 agent token、可选的短生命周期 daemon token（供 Remote MCP broker
// 使用），以及评论任务响应里嵌入的确切评论 id。handler 必须在完整载荷构建完成后、写出任何响应字节前调用。
// 任何失败都会回滚全部写入，使认领可安全退回队列。
func (s *TaskService) FinalizeTaskClaim(
	ctx context.Context,
	task db.AgentTaskQueue,
	token db.CreateTaskTokenParams,
	deliveredCommentIDs []pgtype.UUID,
	recordCommentReceipt bool,
	daemonTokens ...db.CreateDaemonTokenParams,
) ([]pgtype.UUID, error) {
	if len(daemonTokens) > 1 {
		return nil, fmt.Errorf("finalize task claim: expected at most one daemon token, got %d", len(daemonTokens))
	}
	receipt := task.DeliveredCommentIds
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		// 令牌写入与运行时/Agent 的最终授权共用事务和行锁。
		if _, err := qtx.LockWorkspaceForChatSessionCreate(ctx, token.WorkspaceID); err != nil {
			return fmt.Errorf("lock workspace for claim delivery: %w", err)
		}
		runtime, err := qtx.LockAgentRuntime(ctx, task.RuntimeID)
		if err != nil {
			return fmt.Errorf("lock runtime for claim delivery: %w", err)
		}
		if !runtime.OwnerID.Valid || runtime.OwnerID != token.UserID || runtime.WorkspaceID != token.WorkspaceID {
			return fmt.Errorf("runtime ownership changed before claim delivery")
		}
		agent, err := qtx.GetAgentForUpdate(ctx, task.AgentID)
		if err != nil {
			return fmt.Errorf("lock agent for claim delivery: %w", err)
		}
		if agent.RuntimeID != task.RuntimeID || agent.WorkspaceID != runtime.WorkspaceID || agent.ArchivedAt.Valid {
			return fmt.Errorf("agent binding changed before claim delivery")
		}
		if runtime.Visibility == "private" && (!agent.OwnerID.Valid || agent.OwnerID != runtime.OwnerID) {
			return fmt.Errorf("private runtime access denied before claim delivery")
		}

		if _, err := qtx.CreateTaskToken(ctx, token); err != nil {
			return fmt.Errorf("create task token: %w", err)
		}
		if len(daemonTokens) == 1 {
			// 机会式有界清理：让短命的 per-task daemon 凭据不至于累积，又无需另加清扫器。
			if err := qtx.DeleteExpiredDaemonTokens(ctx); err != nil {
				return fmt.Errorf("delete expired daemon tokens: %w", err)
			}
			// Bind the credential to the same owner the task token was just
			// minted for (the locked runtime owner), so it
			// can never reach a runtime another member later registers under
			// this daemon_id (MUL-8154).
			daemonToken := daemonTokens[0]
			daemonToken.OwnerID = token.UserID
			if !daemonToken.OwnerID.Valid {
				return fmt.Errorf("create remote MCP daemon token: runtime owner required")
			}
			if _, err := qtx.CreateDaemonToken(ctx, daemonToken); err != nil {
				return fmt.Errorf("create remote MCP daemon token: %w", err)
			}
		}
		if !recordCommentReceipt {
			return nil
		}
		persisted, err := qtx.SetTaskDeliveredCommentIDs(ctx, db.SetTaskDeliveredCommentIDsParams{
			DeliveredCommentIds:      deliveredCommentIDs,
			TaskID:                   task.ID,
			RuntimeID:                task.RuntimeID,
			DispatchedAt:             task.DispatchedAt,
			ExpectedTriggerCommentID: task.TriggerCommentID,
		})
		if err != nil {
			return fmt.Errorf("set delivered comment ids: %w", err)
		}
		receipt = persisted
		return nil
	})
	if err != nil {
		return nil, err
	}
	return receipt, nil
}

// RequeueTaskAfterClaimFailure 在载荷最终化失败、HTTP 响应尚未写出时，立即释放一条精确匹配的 dispatched
// 认领。SQL CAS 包含 dispatched_at，晚到的 handler 无法回滚更新的回收。这不是全新入队：不要重复记录入队分析。
func (s *TaskService) RequeueTaskAfterClaimFailure(ctx context.Context, task db.AgentTaskQueue) (*db.AgentTaskQueue, error) {
	var requeued db.AgentTaskQueue
	var queuedEvent events.Event
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		requeued, err = qtx.RequeueAgentTaskAfterClaimFailure(ctx, db.RequeueAgentTaskAfterClaimFailureParams{
			TaskID:       task.ID,
			RuntimeID:    task.RuntimeID,
			DispatchedAt: task.DispatchedAt,
		})
		if err != nil {
			return err
		}
		if s.TxStarter != nil {
			workspaceID, workspaceErr := resolveTaskWorkspaceWithQueries(ctx, qtx, requeued)
			if workspaceErr != nil {
				return fmt.Errorf("resolve requeued task workspace: %w", workspaceErr)
			}
			queuedEvent, err = RecordTaskQueuedEventTx(ctx, qtx, workspaceID, requeued)
			if err != nil {
				return fmt.Errorf("record requeued task event: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("requeue task after claim failure: %w", err)
	}
	s.forgetTaskReclaim(requeued)
	s.ReconcileAgentStatus(ctx, requeued.AgentID)
	if queuedEvent.ID != "" {
		publishCommittedEvent(s.Bus, queuedEvent)
	} else {
		s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, requeued)
	}
	s.notifyTaskAvailable(requeued)
	slog.Info("task requeued after claim finalization failure",
		"task_id", util.UUIDToString(requeued.ID),
		"runtime_id", util.UUIDToString(requeued.RuntimeID),
	)
	return &requeued, nil
}

// ClaimTasksForRuntimes 是机器级（MUL-4257）批量版 ClaimTaskForRuntime：单次调用跨 runtimeIDs 里的每个
// runtime 认领至多 maxTasks 条任务，让 daemon 用一次 HTTP 请求与恒定次数 DB 查询即可轮询其全部 runtime，
// 而不是每 runtime 一次请求（与一次 promote/reclaim/list 循环）。
//
// 它保留与单数路径完全相同的语义，只是集合化：
//  1. 跨集合提升到期的 deferred 任务（一条 UPDATE）；
//  2. 跨集合回收至多 maxTasks 条陈旧派发任务（一条 UPDATE）——必须早于空缓存检查，因为丢失认领响应会把
//     任务移出 `queued`，空缓存表达不了；
//  3. 短路缓存为空认领结论的 runtime，对其余 runtime 在候选 SELECT 前采样失效版本；
//  4. 跨非空集合列出排队候选（一条 SELECT）；
//  5. 把仍为空的 runtime 标记为空，让它们下次空闲轮询跳过 Postgres；
//  6. 经按 runtime 收敛的助手按去重 agent 认领，保留 per-(issue, agent) 串行化、agent 并发上限及全部派发
//     副作用，直到达到 maxTasks。
//
// 返回切片同时含回收与全新认领的任务，每条已带 runtime_id，daemon 据此在本地路由到对应 runtime。
func (s *TaskService) ClaimTasksForRuntimes(ctx context.Context, runtimeIDs []pgtype.UUID, maxTasks int) ([]db.AgentTaskQueue, error) {
	if blocked, err := s.releaseClaimsBlocked(ctx); err != nil || blocked {
		return nil, err
	}
	if len(runtimeIDs) == 0 || maxTasks <= 0 {
		return nil, nil
	}

	// 防御性去重 runtime ID，即使 daemon 真发了重复值，MarkEmpty/版本簿记也保持无歧义。
	seen := make(map[string]struct{}, len(runtimeIDs))
	uniqueIDs := make([]pgtype.UUID, 0, len(runtimeIDs))
	runtimeInSet := make(map[string]struct{}, len(runtimeIDs))
	for _, rid := range runtimeIDs {
		key := util.UUIDToString(rid)
		runtimeInSet[key] = struct{}{}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		uniqueIDs = append(uniqueIDs, rid)
	}

	claimed := make([]db.AgentTaskQueue, 0, maxTasks)

	// 1. 跨整个集合提升到期 deferred 任务（与单数路径相同，先提升）。重放单数服务方法
	// PromoteDueDeferredTasksForRuntime 执行的逐行副作用——关键是 EmptyClaim.Bump（经 NotifyTaskEnqueued →
	// notifyTaskAvailable），让刚提升的 deferred 任务在步骤 3 的空缓存过滤【之前】失效其 runtime 的缓存空结论；
	// 否则先前空闲轮询留下的陈旧 MarkEmpty 会短路该 runtime，提升出来的任务将一直无人认领直到空键 TTL 过期。
	// 同时发出 deferred→queued UI 事件与入队分析样本。
	promoted, err := s.promoteDeferredTasks(ctx, uniqueIDs, true)
	if isDuplicatePendingTaskErr(err) {
		// 与单 runtime 路径同样的容忍，而且在这里更重要：一行争用失败会把整个批次里【每个】runtime 的认领都
		// 拖垮。本 tick 不提升，让认领继续。
		slog.Info("promote deferred tasks (batch): slot taken by a concurrent enqueue, skipping this tick")
		promoted = nil
	} else if err != nil {
		s.recordClaimDecision(pgtype.UUID{}, pgtype.UUID{}, "error_promote_deferred", claimScope{claimPath: claimPathBatch}, claimDecisionDetail{})
		return nil, fmt.Errorf("promote deferred tasks: %w", err)
	}
	for _, task := range promoted {
		slog.Info("deferred fallback task promoted (batch)",
			"task_id", util.UUIDToString(task.ID),
			"runtime_id", util.UUIDToString(task.RuntimeID),
			"agent_id", util.UUIDToString(task.AgentID),
		)
		if s.TxStarter == nil {
			s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
		}
		s.NotifyTaskEnqueued(ctx, task)
	}

	// 2. 当任一 runtime 的任务时间表或有界 DB 兜底到期时回收丢失响应的派发任务。查询始终收到完整机器级集合，
	// 使各 runtime 兜底保持对齐，并以可预测节奏付出固定 UPDATE 的建锁成本。缓存为 nil/缺失/失败时保留
	// 历史查询路径。
	runtimeKeys := make([]string, 0, len(uniqueIDs))
	for _, rid := range uniqueIDs {
		runtimeKeys = append(runtimeKeys, util.UUIDToString(rid))
	}
	checkStarted := time.Now()
	dueKeys := s.ReclaimCheck.DueRuntimeIDs(ctx, runtimeKeys, checkStarted)
	var reclaimed []db.AgentTaskQueue
	var reclaimCheckAfter time.Time
	if len(dueKeys) > 0 {
		reclaimCheckAfter = time.Now().Add(claimResponseRecoveryWindow + ReclaimCheckHintSafetyMargin)
		reclaimed, err = s.Queries.ReclaimStaleDispatchedTasksForRuntimes(ctx, db.ReclaimStaleDispatchedTasksForRuntimesParams{
			RuntimeIds:        uniqueIDs,
			ClaimRecoverySecs: claimResponseRecoveryWindow.Seconds(),
			PrepareLeaseSecs:  prepareLeaseDuration.Seconds(),
			RuntimeStaleSecs:  RuntimeClaimFreshnessSeconds,
			MaxTasks:          int32(maxTasks),
		})
		if err != nil {
			s.recordClaimDecision(pgtype.UUID{}, pgtype.UUID{}, "error_reclaim_dispatched", claimScope{claimPath: claimPathBatch}, claimDecisionDetail{})
			return nil, fmt.Errorf("reclaim stale dispatched tasks: %w", err)
		}
		// UPDATE 的固定建锁成本远大于数组宽度。把完整机器级集合一起查询并推进，各 runtime 兜底才不会漂移成
		// “几乎每次 daemon 轮询都做一次 UPDATE”。
		s.ReclaimCheck.MarkChecked(
			ctx,
			runtimeKeys,
			checkStarted,
			time.Now().Add(ReclaimCheckRetryInterval),
		)
	}
	for i := range reclaimed {
		s.trackTaskForReclaim(reclaimed[i], reclaimCheckAfter)
		claimed = append(claimed, reclaimed[i])
		s.recordClaimDecision(reclaimed[i].AgentID, reclaimed[i].RuntimeID, "reclaimed_dispatched", claimScope{claimPath: claimPathBatch}, claimDecisionDetail{})
		slog.Info("stale dispatched task reclaimed (batch)",
			"task_id", util.UUIDToString(reclaimed[i].ID),
			"runtime_id", util.UUIDToString(reclaimed[i].RuntimeID),
			"agent_id", util.UUIDToString(reclaimed[i].AgentID),
		)
	}
	if len(claimed) >= maxTasks {
		return claimed[:maxTasks], nil
	}

	// 3. 空缓存短路 + 对剩余 runtime 采样版本。
	nonEmpty := make([]pgtype.UUID, 0, len(uniqueIDs))
	versions := make(map[string]int64, len(uniqueIDs))
	for _, rid := range uniqueIDs {
		key := util.UUIDToString(rid)
		if s.EmptyClaim.IsEmpty(ctx, key) {
			continue
		}
		versions[key] = s.EmptyClaim.CurrentVersion(ctx, key)
		nonEmpty = append(nonEmpty, rid)
	}
	if len(nonEmpty) == 0 {
		s.recordClaimDecision(pgtype.UUID{}, pgtype.UUID{}, "empty_cache_hit", claimScope{claimPath: claimPathBatch}, claimDecisionDetail{})
		slog.Debug("batch claim: all runtimes empty-cached", "runtimes", len(uniqueIDs))
		return claimed, nil
	}

	// 4. 跨非空集合做一次候选 SELECT。
	candidates, err := s.Queries.ListQueuedClaimCandidatesByRuntimes(ctx, nonEmpty)
	if err != nil {
		s.recordClaimDecision(pgtype.UUID{}, pgtype.UUID{}, "error_list", claimScope{claimPath: claimPathBatch}, claimDecisionDetail{})
		// Steps 2/6 commit reclaimed/claimed tasks in their own transactions,
		// so `claimed` may already hold tasks dispatched server-side. Dropping
		// them with a 500 makes the daemon HTTP-fall-back and claim a SECOND
		// batch into the same free slots (the first batch then waits for stale
		// reclaim) — the same double-claim this PR set out to remove
		// (MUL-4257). Prefer partial success: hand back what committed so the
		// handler finalizes and returns it; the errored candidates stay queued
		// for the next poll.
		if len(claimed) > 0 {
			slog.Error("batch claim: candidate query failed after partial success; returning claimed tasks to avoid loss",
				"error", err, "claimed", len(claimed))
			return claimed, nil
		}
		return nil, fmt.Errorf("list queued claim candidates: %w", err)
	}

	// 5. 把“零候选”的 runtime 标记为空，使它们下次空闲轮询跳过 Postgres。有至少一个候选的 runtime 刻意不标记
	//（正向结果总是重查 DB，与单数路径一致）。
	withCandidates := make(map[string]struct{}, len(candidates))
	for i := range candidates {
		withCandidates[util.UUIDToString(candidates[i].RuntimeID)] = struct{}{}
	}
	for _, rid := range nonEmpty {
		key := util.UUIDToString(rid)
		if _, ok := withCandidates[key]; !ok {
			s.EmptyClaim.MarkEmpty(ctx, key, versions[key])
		}
	}
	if len(candidates) == 0 {
		// Poll-level decision: the candidate query saw nothing claimable for
		// any runtime in the batch (it already filters wakeup validity and
		// agent/runtime binding, so this is about visible candidates, not
		// about the queue being empty).
		s.recordClaimDecision(pgtype.UUID{}, pgtype.UUID{}, "empty_db", claimScope{claimPath: claimPathBatch}, claimDecisionDetail{})
		return claimed, nil
	}

	// 6. 经按 runtime 收敛的助手按去重 agent 认领，保留 per-(issue, agent) 串行化、容量上限与派发副作用。
	triedAgents := make(map[string]struct{}, len(candidates))
	// Candidate rows per agent for the claim-decision scope; see the singular
	// path above for why it is counted before the loop.
	candidatesByAgent := make(map[string]int, len(candidates))
	for i := range candidates {
		candidatesByAgent[util.UUIDToString(candidates[i].AgentID)]++
	}
	for i := range candidates {
		if len(claimed) >= maxTasks {
			slog.Debug("batch claim: reached max tasks",
				"claimed", len(claimed),
				"candidates_remaining", len(candidates)-i,
			)
			break
		}
		agentKey := util.UUIDToString(candidates[i].AgentID)
		if _, tried := triedAgents[agentKey]; tried {
			continue
		}
		triedAgents[agentKey] = struct{}{}

		task, err := s.claimTask(ctx, candidates[i].AgentID, candidates[i].RuntimeID, claimScope{
			claimPath:  claimPathBatch,
			candidates: candidatesByAgent[agentKey],
		})
		if err != nil {
			// 每次按收敛域的认领都在自己的事务里提交，因此更早的迭代（及步骤 2 的回收）已在服务端派发。此处返回 nil 会
			// 丢掉它们、迫使 daemon 经 HTTP 回退双重认领（MUL-4257）。改为返回部分批次；失败 agent 的任务保持 queued。
			if len(claimed) > 0 {
				slog.Error("batch claim: claim task failed after partial success; returning claimed tasks to avoid loss",
					"error", err, "claimed", len(claimed))
				return claimed, nil
			}
			return nil, fmt.Errorf("claim task: %w", err)
		}
		if task == nil {
			continue
		}
		// SQL 认领已收敛到候选 runtime。保留该守卫作为防御性契约检查，防止未来的查询改动把工作路由到本 daemon
		// 不托管的 runtime。
		if _, ok := runtimeInSet[util.UUIDToString(task.RuntimeID)]; !ok {
			slog.Debug("batch claim: skipping claimed task outside requested runtime set",
				"task_id", util.UUIDToString(task.ID),
				"runtime_id", util.UUIDToString(task.RuntimeID),
			)
			continue
		}
		claimed = append(claimed, *task)
	}

	return claimed, nil
}

// cancelSupersededDeferredRetries 丢弃“已被活跃任务超越”的 deferred 自动重试行，确保一次 rerun 点击仍只产生
// 恰好一次后续运行。在提升前立刻执行——提升正是陈旧重试可能变得可认领的时刻。best-effort：这里的失败不能
// 打断认领循环，最多把行留给下一个 tick。
func (s *TaskService) cancelSupersededDeferredRetries(ctx context.Context, runtimeIDs []pgtype.UUID) {
	if len(runtimeIDs) == 0 {
		return
	}
	cancelled, err := s.Queries.CancelSupersededDeferredRetriesForRuntimes(ctx, runtimeIDs)
	if err != nil {
		slog.Warn("cancel superseded deferred retries failed", "error", err)
		return
	}
	for _, task := range cancelled {
		slog.Info("deferred auto-retry cancelled: superseded by an active task",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"agent_id", util.UUIDToString(task.AgentID),
		)
		s.captureTaskCancelled(ctx, task)
		s.ReconcileAgentStatus(ctx, task.AgentID)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, task)
	}
}

// PromoteDueDeferredTasksForRuntime 把某 runtime 到期的 deferred 任务提升为 queued，并执行相应的广播/唤醒
// 副作用；命中 pending 槽位竞争时静默跳过本 tick（行保持 deferred，下一轮自然跳过）。
func (s *TaskService) PromoteDueDeferredTasksForRuntime(ctx context.Context, runtimeID pgtype.UUID) error {
	tasks, err := s.promoteDeferredTasks(ctx, []pgtype.UUID{runtimeID}, false)
	if isDuplicatePendingTaskErr(err) {
		// 查询内部的 NOT EXISTS 围栏看不到尚未提交的入队，同一瞬间落地的 rerun 仍会在此碰撞。单行让出槽位不能
		// 拖垮整个认领：该行保持 deferred，下一 tick（rerun 已可见）会干净跳过它。代价只是一个轮询间隔，
		// 绝不是卡死。
		slog.Info("promote due deferred tasks: slot taken by a concurrent enqueue, skipping this tick",
			"runtime_id", util.UUIDToString(runtimeID))
		return nil
	}
	if err != nil {
		return fmt.Errorf("promote due deferred tasks: %w", err)
	}
	for _, task := range tasks {
		slog.Info("deferred fallback task promoted",
			"task_id", util.UUIDToString(task.ID),
			"runtime_id", util.UUIDToString(runtimeID),
			"agent_id", util.UUIDToString(task.AgentID),
		)
		if s.TxStarter == nil {
			s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
		}
		s.NotifyTaskEnqueued(ctx, task)
	}
	return nil
}

// promoteDeferredTasks 把“deferred-重试提升、被超越重试的取消及其 durable 生命周期事件”做成一次数据库提交。
// deferred 行在成为 queued 前刻意保持静默；调用方只在本方法成功后唤醒 daemon。
func (s *TaskService) promoteDeferredTasks(ctx context.Context, runtimeIDs []pgtype.UUID, batch bool) ([]db.AgentTaskQueue, error) {
	if len(runtimeIDs) == 0 {
		return nil, nil
	}
	if s.TxStarter == nil {
		// 没有事务启动器的单测/嵌入夹具保留旧式直查行为；生产服务始终走原子路径。
		s.cancelSupersededDeferredRetries(ctx, runtimeIDs)
		if batch {
			return s.Queries.PromoteDueDeferredTasksForRuntimes(ctx, db.PromoteDueDeferredTasksForRuntimesParams{
				RuntimeIds: runtimeIDs, RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
			})
		}
		return s.Queries.PromoteDueDeferredTasksForRuntime(ctx, db.PromoteDueDeferredTasksForRuntimeParams{
			RuntimeID: runtimeIDs[0], RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
		})
	}
	var promoted []db.AgentTaskQueue
	var cancelled []db.AgentTaskQueue
	var persisted []events.Event
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		cancelled, err = qtx.CancelSupersededDeferredRetriesForRuntimes(ctx, runtimeIDs)
		if err != nil {
			return fmt.Errorf("cancel superseded deferred retries: %w", err)
		}
		if batch {
			promoted, err = qtx.PromoteDueDeferredTasksForRuntimes(ctx, db.PromoteDueDeferredTasksForRuntimesParams{
				RuntimeIds: runtimeIDs, RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
			})
		} else {
			promoted, err = qtx.PromoteDueDeferredTasksForRuntime(ctx, db.PromoteDueDeferredTasksForRuntimeParams{
				RuntimeID: runtimeIDs[0], RuntimeStaleSecs: RuntimeClaimFreshnessSeconds,
			})
		}
		if err != nil {
			return err
		}
		persisted = make([]events.Event, 0, len(cancelled)+len(promoted))
		for _, task := range cancelled {
			event, eventErr := recordTaskTerminalEventTx(ctx, qtx, protocol.EventTaskCancelled, task,
				taskFailedFields("", "superseded_by_newer_task", false))
			if eventErr != nil {
				return fmt.Errorf("record superseded retry cancellation: %w", eventErr)
			}
			persisted = append(persisted, event)
		}
		for _, task := range promoted {
			workspaceID, workspaceErr := resolveTaskWorkspaceWithQueries(ctx, qtx, task)
			if workspaceErr != nil {
				return fmt.Errorf("resolve promoted task workspace: %w", workspaceErr)
			}
			event, eventErr := RecordTaskQueuedEventTx(ctx, qtx, workspaceID, task)
			if eventErr != nil {
				return fmt.Errorf("record promoted task event: %w", eventErr)
			}
			persisted = append(persisted, event)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i, task := range cancelled {
		slog.Info("deferred auto-retry cancelled: superseded by an active task",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(task.IssueID),
			"agent_id", util.UUIDToString(task.AgentID),
		)
		s.captureTaskCancelled(ctx, task)
		s.ReconcileAgentStatus(ctx, task.AgentID)
		publishCommittedEvent(s.Bus, persisted[i])
	}
	offset := len(cancelled)
	for i, task := range promoted {
		slog.Info("deferred fallback task promoted",
			"task_id", util.UUIDToString(task.ID),
			"runtime_id", util.UUIDToString(task.RuntimeID),
			"agent_id", util.UUIDToString(task.AgentID),
		)
		publishCommittedEvent(s.Bus, persisted[offset+i])
	}
	return promoted, nil
}

// maybeLogClaimSlow 在每次 ClaimTask 调用总延迟超过 300ms 时发一条结构化日志，使生产尾部可诊断又不会在
// 正常轮询频率下刷屏。经 defer 调用，能覆盖完整路径——含认领后的 updateAgentStatus / broadcastTaskDispatch
// （两者都可能打 DB）与任何错误出口。
func (s *TaskService) maybeLogClaimSlow(agentID pgtype.UUID, outcome string, start time.Time, getAgentMs, countRunningMs, claimAgentMs, reanchorMs, updateStatusMs, dispatchMs int64) {
	totalMs := time.Since(start).Milliseconds()
	if totalMs < 300 {
		return
	}
	slog.Info("claim_task slow",
		"agent_id", util.UUIDToString(agentID),
		"outcome", outcome,
		"total_ms", totalMs,
		"get_agent_ms", getAgentMs,
		"count_running_ms", countRunningMs,
		"claim_agent_ms", claimAgentMs,
		"reanchor_chat_input_ms", reanchorMs,
		"update_status_ms", updateStatusMs,
		"dispatch_ms", dispatchMs,
	)
}

// StartTask 把任务从 dispatched/waiting_local_directory 推进到 running：事务内执行状态翻转并记录 running
// 事件，提交后清除回收提示、取消该任务的延时降级计时器、调和 agent 状态并广播。
func (s *TaskService) StartTask(ctx context.Context, taskID pgtype.UUID) (*db.AgentTaskQueue, error) {
	var task db.AgentTaskQueue
	var runningEvent events.Event
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		task, err = qtx.StartAgentTask(ctx, taskID)
		if err != nil {
			return err
		}
		if s.TxStarter != nil {
			runningEvent, err = RecordTaskLifecycleEventTx(ctx, qtx, protocol.EventTaskRunning, task)
			if err != nil {
				return fmt.Errorf("record task running event: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("start task: %w", err)
	}
	s.taskStarted(ctx, task, runningEvent)
	return &task, nil
}

// StartTaskForClaim serializes the ownership check and transition with reclaim,
// cancellation and other start requests. A replay linearizes at the locked read;
// a cancellation that commits later can still cancel the acknowledged task.
func (s *TaskService) StartTaskForClaim(ctx context.Context, claim db.LockAgentTaskStartClaimParams) (*db.AgentTaskQueue, error) {
	if !claim.ID.Valid || !claim.RuntimeID.Valid || !claim.DispatchedAt.Valid {
		return nil, fmt.Errorf("start task: incomplete claim")
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin task start: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	task, err := qtx.LockAgentTaskStartClaim(ctx, claim)
	if err != nil {
		return nil, fmt.Errorf("lock task start claim: %w", err)
	}
	var runningEvent events.Event
	replay := task.Status == "running"
	if !replay {
		task, err = qtx.StartAgentTask(ctx, task.ID)
		if err != nil {
			return nil, fmt.Errorf("start claimed task: %w", err)
		}
		runningEvent, err = RecordTaskLifecycleEventTx(ctx, qtx, protocol.EventTaskRunning, task)
		if err != nil {
			return nil, fmt.Errorf("record claimed task running event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit task start: %w", err)
	}
	if !replay {
		s.taskStarted(ctx, task, runningEvent)
	}
	return &task, nil
}

func (s *TaskService) taskStarted(ctx context.Context, task db.AgentTaskQueue, runningEvent events.Event) {
	s.forgetTaskReclaim(task)
	s.cancelDeferredEscalationsForTask(ctx, task.ID)

	slog.Info("task started", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
	s.captureTaskStarted(ctx, task)
	// 停在 waiting_local_directory 期间，本任务可能已被从持久化的 working 状态里调和掉。进入 running 后立即
	// 恢复 working；常规 dispatched → running 路径本来就已是 working，因此这里刻意幂等。
	s.ReconcileAgentStatus(ctx, task.AgentID)
	// 通知每个已连接的 workspace WS 客户端：本任务发生了 (dispatched | waiting_local_directory) → running 转换。
	// 否则全工作区的 `agentTaskSnapshot` 查询只在 30s staleTime 后刷新，任何区分 queued/running 的 UI（如
	// issue 卡片上的 agent 活动指示器）会在用户最关心的转换上滞后最多半分钟。
	if runningEvent.ID != "" {
		publishCommittedEvent(s.Bus, runningEvent)
	} else {
		s.broadcastTaskEvent(ctx, protocol.EventTaskRunning, task)
	}
}

// cancelDeferredEscalationsForTask 在任务被“主运行”真正接管（started）时，取消针对它的全部延时降级
// （assignee fallback）任务并记日志。
func (s *TaskService) cancelDeferredEscalationsForTask(ctx context.Context, taskID pgtype.UUID) {
	cancelled, err := s.Queries.CancelDeferredEscalationsForTask(ctx, taskID)
	if err != nil {
		slog.Warn("cancel deferred escalations for task failed", "task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	for _, task := range cancelled {
		slog.Info("deferred fallback task cancelled",
			"task_id", util.UUIDToString(task.ID),
			"primary_task_id", util.UUIDToString(taskID),
			"reason", "primary_acknowledged",
		)
	}
}

// CancelDeferredEscalationsForIssueAgent 取消“为该 issue+agent 排程的延时降级任务”——当 agent 在该 issue
// 上发表了评论、证明自己已接管时调用。
func (s *TaskService) CancelDeferredEscalationsForIssueAgent(ctx context.Context, issueID, agentID pgtype.UUID) {
	cancelled, err := s.Queries.CancelDeferredEscalationsForIssueAgent(ctx, db.CancelDeferredEscalationsForIssueAgentParams{
		IssueID: issueID,
		AgentID: agentID,
	})
	if err != nil {
		slog.Warn("cancel deferred escalations for issue agent failed",
			"issue_id", util.UUIDToString(issueID),
			"agent_id", util.UUIDToString(agentID),
			"error", err)
		return
	}
	for _, task := range cancelled {
		slog.Info("deferred fallback task cancelled",
			"task_id", util.UUIDToString(task.ID),
			"issue_id", util.UUIDToString(issueID),
			"agent_id", util.UUIDToString(agentID),
			"reason", "agent_comment_acknowledged",
		)
	}
}

// ExtendTaskPrepareLease 在 daemon 解析缓存输入、准备执行环境期间，让“已认领但未开始”的任务保持受保护。
func (s *TaskService) ExtendTaskPrepareLease(ctx context.Context, taskID, runtimeID pgtype.UUID) (*db.AgentTaskQueue, error) {
	task, err := s.Queries.ExtendAgentTaskPrepareLease(ctx, db.ExtendAgentTaskPrepareLeaseParams{
		ID:        taskID,
		RuntimeID: runtimeID,
		LeaseSecs: prepareLeaseDuration.Seconds(),
	})
	if err != nil {
		return nil, fmt.Errorf("extend task prepare lease: %w", err)
	}
	if task.Status == "dispatched" {
		// 用成功响应时刻作为 DB 租约起点的保守应用钟近似。宁可因查询延迟略晚调度，也不要过早失败检查把提示
		// 压到下一个兜底点才恢复。
		s.extendTaskReclaimHint(task, time.Now().Add(prepareLeaseDuration))
	} else {
		s.forgetTaskReclaim(task)
	}
	return &task, nil
}

// MarkTaskWaitingLocalDirectory 把一条 dispatched 任务停到 waiting_local_directory 状态，等待另一条在途
// 任务释放 project_resource 路径锁。reason 携带短人类可读提示（通常是被争用的路径），UI 会显示在状态旁。
// 返回更新后的行，供 daemon 确认转换、广播携带最新快照。
func (s *TaskService) MarkTaskWaitingLocalDirectory(ctx context.Context, taskID pgtype.UUID, reason string) (*db.AgentTaskQueue, error) {
	reason = sanitizeWaitReason(reason)
	var task db.AgentTaskQueue
	var waitingEvent events.Event
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		task, err = qtx.MarkAgentTaskWaitingLocalDirectory(ctx, db.MarkAgentTaskWaitingLocalDirectoryParams{
			ID:               taskID,
			WaitReason:       pgtype.Text{String: reason, Valid: reason != ""},
			PrepareLeaseSecs: prepareLeaseDuration.Seconds(),
		})
		if err != nil {
			return err
		}
		if s.TxStarter != nil {
			extra := map[string]any{}
			if reason != "" {
				extra["wait_reason"] = reason
			}
			waitingEvent, err = RecordTaskLifecycleEventTx(ctx, qtx, protocol.EventTaskWaitingLocalDirectory, task, extra)
			if err != nil {
				return fmt.Errorf("record task waiting event: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("mark task waiting_local_directory: %w", err)
	}
	s.forgetTaskReclaim(task)

	slog.Info("task waiting_local_directory",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(task.IssueID),
		"reason", reason,
	)
	// waiting_local_directory 是“已认领/排队”的工作而非执行中的工作。认领路径在行派发时把 agent 标为 working，
	// 因此它在停靠时应立即调和，而不是让持久化状态一直陈旧到终态转换。
	s.ReconcileAgentStatus(ctx, task.AgentID)
	// 让 reason 随事件走，而不只存在行里。否则客户端只有在下一次拉取时才明白任务为何停靠，期间状态胶囊
	// 什么都没说——这正是该提示要弥合的空白。reason 上面已脱敏，socket 不会携带 REST 载荷会拦下的路径。
	extra := map[string]any{}
	if reason != "" {
		extra["wait_reason"] = reason
	}
	if waitingEvent.ID != "" {
		publishCommittedEvent(s.Bus, waitingEvent)
	} else {
		s.broadcastTaskEvent(ctx, protocol.EventTaskWaitingLocalDirectory, task, extra)
	}
	return &task, nil
}

// legacyLocalDirectoryWaitPrefix 是显示名改动之前 daemon 写等待原因的形式：字面词、空格、绝对路径。
const legacyLocalDirectoryWaitPrefix = "local_directory "

// sanitizeWaitReason 丢弃携带绝对文件系统路径的等待原因，再修剪剩余部分。
//
// 版本错位让它是承重逻辑而非防御：新版 daemon 发显示名（localDirectoryAssignment.DisplayName），正因为
// 该文本会到达会话上的每个客户端并进入截图。老 daemon 配对这台服务器仍发 `local_directory /Users/<name>/repo
// (held by task abc12345)`；客户端现在会渲染该字段，原样透传等于把用户主目录路径与账户名摆上屏幕。泄露正是
// 由“展示该字段”引入的，所以守卫必须与展示同在。
//
// 选择丢弃整串而非编辑它：客户端本就处理“无原因”（旧服务器从不发），退化为纯 waiting 标签——正是改动前的
// 行为，永不出错。
//
// 等不再有受支持的 daemon 输出旧格式后再删除。
func sanitizeWaitReason(reason string) string {
	reason = strings.TrimSpace(reason)
	rest, ok := strings.CutPrefix(reason, legacyLocalDirectoryWaitPrefix)
	if ok && startsWithAbsolutePath(rest) {
		return ""
	}
	return reason
}

// startsWithAbsolutePath 判断 s 是否以“daemon 运行任意平台上的绝对路径”开头：POSIX（/…）、Windows 盘符
// （C:\… 或 C:/…）、UNC 共享（\\host\share）。只针对 daemon 自身输出做检查，问题仅是“旧 daemon 是否把
// 路径放在这里”，不是通用解析。
//
// 它刻意【不】匹配当前 daemon 追加的持有子句——`local_directory (held by task abc12345)` 是本服务器在目录
// 真的叫 “local_directory” 时产出的，而那是用户自己的标签，不是路径。
func startsWithAbsolutePath(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '/' || strings.HasPrefix(s, `\\`) {
		return true
	}
	// 盘符形式：单个 ASCII 字母 + 冒号 + 分隔符。
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		c := s[0]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return false
}

// CompleteTask 把任务标记为完成。这里【不】改 issue 状态——agent 经 CLI 自行管理。
//
// chat 任务场景下，CompleteAgentTask 与 chat_session 的恢复指针更新在同一事务内完成。这关掉了“任务翻成
// completed 与 chat_session.session_id 刷新之间的窗口期里，下一条排队 chat 消息可能被认领，导致新任务基于
// 陈旧（或 NULL）会话恢复”的竞态。durableWorkDir 是终态投递元数据而非恢复指针：仅在 daemon 确认一次性
// worktree 已删除后才会填充。
func (s *TaskService) CompleteTask(ctx context.Context, taskID pgtype.UUID, result []byte, sessionID, workDir, branchName string, sessionRolloutMissing bool, retiredSessionID, durableWorkDir string) (*db.AgentTaskQueue, error) {
	task, _, err := s.CompleteTaskWithTransition(ctx, taskID, result, sessionID, workDir, branchName, sessionRolloutMissing, retiredSessionID, durableWorkDir)
	return task, err
}

// CompleteTaskWithTransition reports whether this call won the running ->
// completed compare-and-swap. Callers with transaction-external side effects
// must only run them when transitioned is true; a replay against an already
// terminal task is still an idempotent success but must not emit them again.
func (s *TaskService) CompleteTaskWithTransition(ctx context.Context, taskID pgtype.UUID, result []byte, sessionID, workDir, branchName string, sessionRolloutMissing bool, retiredSessionID, durableWorkDir string) (*db.AgentTaskQueue, bool, error) {
	// Life 后台任务只有在 life_job_complete 校验并持久化其结构化领域输出后才算完成。daemon 在 agent 进程退出后
	// 仍会发普通终态回调；此处拒绝这第二次完成，并把任务原子地交回其自身重试生命周期，避免队列行与认知行分叉。
	if current, err := s.Queries.GetAgentTask(ctx, taskID); err == nil {
		if _, governed := lifeTaskEnvelopeForQueue(current); governed && current.Status == "running" {
			failed, failErr := s.failLifeCognitionTask(ctx, taskID,
				ErrLifeStructuredOutputRequired.Error(),
				string(taskfailure.ReasonAgentEmptyOrUnparseableOutput))
			if failErr != nil {
				return nil, false, failErr
			}
			return failed, false, ErrLifeStructuredOutputRequired
		}
	}

	var issueReplyEvents []events.Event
	var task db.AgentTaskQueue
	var terminalEvent events.Event
	var quickActionsPending bool
	// chatAssistantMsg 是下面完成事务内为 chat 任务写入的唯一助手结果行。只在事务提交后广播（chat:done）。
	var chatAssistantMsg *db.ChatMessage
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		if err := lockChatSessionForTaskWrite(ctx, qtx, taskID); err != nil {
			return err
		}
		t, err := qtx.CompleteAgentTask(ctx, db.CompleteAgentTaskParams{
			ID:                    taskID,
			Result:                result,
			SessionID:             pgtype.Text{String: sessionID, Valid: sessionID != ""},
			WorkDir:               pgtype.Text{String: workDir, Valid: workDir != ""},
			DurableWorkDir:        pgtype.Text{String: durableWorkDir, Valid: durableWorkDir != ""},
			BranchName:            pgtype.Text{String: branchName, Valid: branchName != ""},
			SessionRolloutMissing: sessionRolloutMissing,
			RetiredSessionID:      pgtype.Text{String: retiredSessionID, Valid: retiredSessionID != ""},
		})
		if err != nil {
			return err
		}
		task = t

		// 与状态翻转原子：二者之间崩溃会让一条已完成的义务永远看起来 pending。
		if err := SettleDeliveredDelegatedFailureRecoveries(ctx, qtx, t); err != nil {
			return err
		}

		if t.ChatSessionID.Valid {
			// 把 chat_session 的 runtime_id 与 session_id 一起钉住，使下次认领可套用 runtime 守卫。两字段同进退：
			// 没有可记录的 session_id 时不动 runtime_id（NULL → COALESCE 保留现值）。
			var sessionRuntimeID pgtype.UUID
			if sessionID != "" {
				sessionRuntimeID = t.RuntimeID
			}
			// SQL 中的 COALESCE 保证空输入不会抹掉既有恢复指针；DB 错误仍照常上抛。
			if err := qtx.UpdateChatSessionSession(ctx, db.UpdateChatSessionSessionParams{
				ID:        t.ChatSessionID,
				SessionID: pgtype.Text{String: sessionID, Valid: sessionID != ""},
				WorkDir:   pgtype.Text{String: workDir, Valid: workDir != ""},
				RuntimeID: sessionRuntimeID,
			}); err != nil {
				return fmt.Errorf("update chat session resume pointer: %w", err)
			}
			// 通过“抛弃会话”恢复的回合仍须在此退役旧会话。上面的 COALESCE 做不到：一次不带新 id 就成功的全新重试
			// 会传 sessionID == ""，从而保留指针——而该指针正指向被毒化的会话（GH #6066）。放在更新之后执行，
			// 让真正的新 id 先赢，随后的匹配守卫便找不到要清的东西。
			if retiredSessionID != "" {
				if err := qtx.ClearChatSessionSessionIfMatches(ctx, db.ClearChatSessionSessionIfMatchesParams{
					ID:        t.ChatSessionID,
					SessionID: pgtype.Text{String: retiredSessionID, Valid: true},
					RuntimeID: t.RuntimeID,
				}); err != nil {
					return fmt.Errorf("clear retired chat session resume pointer: %w", err)
				}
			}

			// 助手结果行与状态翻转、恢复指针更新写入【同一】事务（MUL-4351）。任务自有的 direct 任务恰好写一行
			//（message 或 no_response）；legacy/channel 任务空输出时不写行（见 writeChatCompletionOutcome）。此处失败
			// 会回滚整个完成，daemon 重试终态回调；上面的状态 CAS 保证重放不会写出第二行。
			msg, err := s.writeChatCompletionOutcome(ctx, qtx, t, result)
			if err != nil {
				return fmt.Errorf("write chat assistant outcome: %w", err)
			}
			chatAssistantMsg = msg
			quickActionsPending = s.chatQuickActionsEligibleWithQueries(ctx, qtx, t, msg)
		}
		if t.IssueID.Valid {
			issueReplyEvents, err = s.writeIssueCompletionReply(ctx, qtx, t, result)
			if err != nil {
				return fmt.Errorf("write issue completion reply: %w", err)
			}
		}
		if s.TxStarter != nil {
			var extra map[string]any
			if t.ChatSessionID.Valid {
				extra = map[string]any{"quick_actions_pending": quickActionsPending}
			}
			terminalEvent, err = recordTaskTerminalEventTx(ctx, qtx, protocol.EventTaskCompleted, t, extra)
			if err != nil {
				return fmt.Errorf("record task completion event: %w", err)
			}
		}
		return nil
	}); err != nil {
		// 并行 agent 竞态下，本调用执行时任务可能已被完成/取消/失败。此时 UPDATE … WHERE status = 'running'
		// 匹配不到行。把它当作幂等成功——与 CancelTask 相同的模式。
		if existing, lookupErr := s.Queries.GetAgentTask(ctx, taskID); lookupErr == nil {
			if errors.Is(err, pgx.ErrNoRows) && (existing.Status == "completed" || existing.Status == "failed" || existing.Status == "cancelled") {
				slog.Info("complete task: already finalized",
					"task_id", util.UUIDToString(taskID),
					"current_status", existing.Status,
					"agent_id", util.UUIDToString(existing.AgentID),
				)
				return &existing, false, nil
			}
			slog.Warn("complete task failed",
				"task_id", util.UUIDToString(taskID),
				"current_status", existing.Status,
				"issue_id", util.UUIDToString(existing.IssueID),
				"chat_session_id", util.UUIDToString(existing.ChatSessionID),
				"agent_id", util.UUIDToString(existing.AgentID),
				"error", err,
			)
		} else {
			slog.Warn("complete task failed: task not found",
				"task_id", util.UUIDToString(taskID),
				"lookup_error", lookupErr,
			)
		}
		return nil, false, fmt.Errorf("complete task: %w", err)
	}

	slog.Info("task completed", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID))
	s.captureTaskCompleted(ctx, task)
	// 修改 4. 同步任务结束时批次没走完成流程，就不能继续挂成活动批次。
	s.closeUpstreamRuns(ctx, task, "interrupted", "任务结束时批次未推进")

	if len(issueReplyEvents) > 0 {
		s.CancelDeferredEscalationsForIssueAgent(ctx, task.IssueID, task.AgentID)
	}
	for _, event := range issueReplyEvents {
		publishCommittedEvent(s.Bus, event)
	}

	// quick-create 任务：定位 agent 刚创建的 issue，向请求者推送收件箱确认。agent 没有 issue/chat 链接，因此
	// 上面的常规完成路径不适用。通过“任务开始以来、该 agent 在该请求者工作区创建的最新 issue”定位新 issue——
	// 比从 agent stdout 解析标识符更健壮。
	if qc, ok := s.parseQuickCreateContext(task); ok {
		s.notifyQuickCreateCompleted(ctx, task, qc, result)
	}

	// chat 任务在提交后广播 chat:done。唯一助手结果行（message 或 no_response）与恢复指针已在上方事务内持久化。
	// unread 由读游标（chat_session.last_read_at）对比其后的助手消息推导——no_response 行也是 role='assistant'
	// 且 created_at 更新，因此与文本回复一样计入未读，无需逐回复打标。
	if task.ChatSessionID.Valid {
		// 助手结果行（message / no_response）与附件绑定已由 writeChatCompletionOutcome 在上方完成事务内写入。
		// 提交后再广播 chat:done。
		//
		// quick-actions 决策在此处、广播前做出，使 pending 标记与其后真正执行的生成轮次永不一致：只有生成
		// 确实即将运行时客户端才展示骨架占位。
		suggest := s.chatQuickActionsEligible(ctx, task, chatAssistantMsg)
		if terminalEvent.ID == "" {
			// 没有事务启动器的服务是测试/嵌入夹具；生产 chat 完成由 durable 投影投递。
			s.broadcastChatDone(ctx, task, chatAssistantMsg, suggest)
		}
		if suggest {
			// 分离执行：回复已投递，用户下一轮绝不应等待建议。
			s.GenerateChatQuickActionsAsync(task, ChatQuickActionsAutomatic)
		}
	}

	// 调和 agent 状态
	s.ReconcileAgentStatus(ctx, task.AgentID)

	// 广播已记录的事件。没有事务启动器的服务是测试/嵌入夹具：保留其进程内行为，而生产环境永远走 outbox 行。
	if terminalEvent.ID != "" {
		publishCommittedEvent(s.Bus, terminalEvent)
	} else {
		s.broadcastTaskEvent(ctx, protocol.EventTaskCompleted, task)
	}

	return &task, true, nil
}

// CompleteLifeTaskInTx 用调用方持有的事务收尾一个后台 Life 认知任务。Life 输出与任务终态事件必须一起提交；
// 否则一次崩溃可能留下“推导已提交、队列任务仍在 running”（或相反）的分叉。
//
// Life 认知任务不持有 chat 会话或 issue。其响应经 durable 任务事件与 Life API 投递，因此这条小路径刻意
// 不执行 CompleteTask 的交互式聊天副作用。
func (s *TaskService) CompleteLifeTaskInTx(ctx context.Context, q *db.Queries, taskID pgtype.UUID, result []byte) (db.AgentTaskQueue, events.Event, error) {
	if q == nil {
		return db.AgentTaskQueue{}, events.Event{}, errors.New("complete Life task: transaction queries are required")
	}
	task, err := q.CompleteAgentTask(ctx, db.CompleteAgentTaskParams{
		ID:     taskID,
		Result: result,
	})
	if err != nil {
		return db.AgentTaskQueue{}, events.Event{}, err
	}
	if task.ChatSessionID.Valid || task.IssueID.Valid {
		return db.AgentTaskQueue{}, events.Event{}, errors.New("complete Life task: task has interactive work attached")
	}
	if err := SettleDeliveredDelegatedFailureRecoveries(ctx, q, task); err != nil {
		return db.AgentTaskQueue{}, events.Event{}, err
	}
	event, err := recordTaskTerminalEventTx(ctx, q, protocol.EventTaskCompleted, task)
	if err != nil {
		return db.AgentTaskQueue{}, events.Event{}, err
	}
	return task, event, nil
}

// chatNoResponseFallback 是 no_response 助手行上存储的非空英文正文。新客户端按 message_kind='no_response'
// 渲染本地化的“本轮无文字回复”；忽略 message_kind 的旧客户端仍会看到这段文本而非空气泡（MUL-4351）。
const chatNoResponseFallback = "The agent finished this turn without a text reply."

// writeChatCompletionOutcome 在调用方的完成事务内，为一条已完成的 chat 任务写入助手 chat_message 结果行，
// 返回该行（未写入时返回 nil）。
//
// Direct（web/mobile）任务遵循显式单结果契约：非空最终输出变成普通助手消息，空/纯空白输出变成一条可见的
// no_response 结果、携带非空英文兜底正文。它绝不自动重试：空输出是合法终态（纯工具回合），重跑会重复
// 已经执行的副作用。
//
// 频道（Slack/Lark）与 legacy 任务保留旧行为：非空输出写普通助手消息，空输出【不】写行，于是 chat:done 携带
// 空内容、频道外发静默丢弃（MUL-4351 review）：no_response 兜底正文绝不能推到外部频道。频道任务如今与
// direct 任务一样持有封存输入批次（chat_input_task_id 已设），判别依据是所属批次上不可变的 channel_ingested
// 戳——按键为批次归属者 id，使自动重试克隆（继承 chat_input_task_id）与父任务同判——而 NULL 归属者表示
// legacy 任务。
func (s *TaskService) writeChatCompletionOutcome(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, result []byte) (*db.ChatMessage, error) {
	// result 是 handler 重新编组的 daemon 请求，因此总是合法 JSON；本分支只关心 Output 为空的情况。
	var payload protocol.TaskCompletedPayload
	_ = json.Unmarshal(result, &payload)
	// 与 issue 评论路径相同的反转义：agent stdout 的字面 `\n` 变成真实换行，聊天面板才能渲染段落分隔。
	body := util.UnescapeBackslashEscapes(payload.Output)
	// 从【每条】chat 完成中剥掉带内 quick-actions 页脚——保留语法绝不能进入存储的转录。包括 agent 发起的开场
	// 回合（chat_input_task_id NULL），它之前漏在剥离门槛外、把原始页脚泄露进内容；频道输出从不携带该语法，
	// 因此对频道切分是 no-op。
	//
	// 解析出的页脚被【丢弃】：建议现在来自服务端轮次（SupplementChatQuickActions）。若在此处采纳页脚，会把
	// 升级前的会话钉在已退役、质量更低的带内建议上，并压制本应替换它的新轮次。
	body, _ = splitChatQuickActions(body)
	isEmpty := strings.TrimSpace(body) == ""

	// MUL-4899 完成边界观测：度量 runtime brief 里的投递契约是否真的落在聊天表面上。严格非阻塞：
	// 无论结果如何回复都会写入。
	s.observeChatOutputLocalPath(task, body)

	// agent 在任务期间上传、尚未绑定到任何归属者的附件（以 task_id 标记）属于这条回复的一部分。它们让空文本
	// 回合成为真正的图片/文件响应——而非 no_response——且需要一个行来挂载。计数与绑定都在 qtx 上执行，
	// 使消息创建与绑定成为一次原子结果。
	wsUUID, _ := util.ParseUUID(s.ResolveTaskWorkspaceID(ctx, task))
	var pendingAttachments int64
	if wsUUID.Valid {
		n, err := qtx.CountUnboundChatAttachmentsForTask(ctx, db.CountUnboundChatAttachmentsForTaskParams{
			WorkspaceID: wsUUID,
			TaskID:      task.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("count chat attachments: %w", err)
		}
		pendingAttachments = n
	}

	// 频道/legacy 空完成且无内容可展示：不写助手行，只发空 chat:done 供打字/生命周期使用。保持 Slack/Lark 的
	// 静默丢弃路径——外发补丁器会原样转发任何非空内容，因此兜底正文绝不能为频道任务写入。附件仍强制写行——
	// agent 产出了用户必须看到的交付物。
	if isEmpty && pendingAttachments == 0 {
		if !task.ChatInputTaskID.Valid {
			return nil, nil // legacy task
		}
		channelIngested, err := qtx.TaskHasChannelIngestedMessages(ctx, task.ChatInputTaskID)
		if err != nil {
			return nil, fmt.Errorf("check chat completion channel provenance: %w", err)
		}
		if channelIngested {
			return nil, nil // channel task
		}
	}

	params := db.CreateChatMessageParams{
		ID:            dbid.NewV7(),
		ChatSessionID: task.ChatSessionID,
		Role:          "assistant",
		TaskID:        task.ID,
		ElapsedMs:     computeChatElapsedMs(task),
	}
	switch {
	case !isEmpty:
		params.Content = redact.Text(body)
		// message_kind 保持 NULL → COALESCE 默认成 'message'。
		//
		// 唯一例外如今只剩部署窗口情形。开场白由服务器直接写入（MUL-5827），新任务不再产生它——但前一个服务器入队的
		// kickoff 任务仍可能被本服务器认领，其回复【就是】该成员的开场白。把它留成普通 'message' 会让该会话永远
		// 失去 starter cards。
		//
		// 以“该批次只是 kickoff、别无其它”为门槛——这正是旧形态：新 kickoff 会与真实成员消息同批出现，若给那
		// 一轮打标，会在并非开场白的回复下二次渲染卡片。
		//
		// 按键是 chatInputOwnerID 而非 task.ID：自动重试克隆会继承根的 chat_input_task_id 却拿到全新 id（MUL-4351），
		// kickoff 用户行始终绑定在根上。
		openingOnly, err := qtx.TaskInputIsOnboardingKickoffOnly(ctx, chatInputOwnerID(task))
		if err != nil {
			return nil, fmt.Errorf("check onboarding kickoff input: %w", err)
		}
		if openingOnly.Bool {
			params.MessageKind = pgtype.Text{String: protocol.ChatMessageKindOnboardingOpening, Valid: true}
		}
	case pendingAttachments > 0:
		// 纯图片/文件回复：一条带空文本的真实 'message' 结果——附件卡片就是响应本身，不能读作 no_response。
		params.Content = ""
	default:
		// 任务自有的 direct 任务、空输出、无附件：显式、可见的 no_response 结果。
		params.Content = chatNoResponseFallback
		params.MessageKind = pgtype.Text{String: protocol.ChatMessageKindNoResponse, Valid: true}
	}
	row, err := createAssistantChatMessage(ctx, qtx, params)
	if err != nil {
		return nil, err
	}

	// 把任务仍未被认领的附件绑定到刚写的回复上。
	if pendingAttachments > 0 && wsUUID.Valid {
		bound, err := qtx.BindChatAttachmentsToMessage(ctx, db.BindChatAttachmentsToMessageParams{
			ChatMessageID: row.ID,
			WorkspaceID:   wsUUID,
			TaskID:        task.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("bind chat attachments: %w", err)
		}
		if len(bound) > 0 {
			slog.Info("bound chat attachments to assistant reply",
				"task_id", util.UUIDToString(task.ID),
				"message_id", util.UUIDToString(row.ID),
				"count", len(bound),
			)
		}
	}
	return &row, nil
}

// observeChatOutputLocalPath 在 chat 回复引用运行时本地路径时记录一条指标（MUL-4899）。仅观测——不改动
// 回复、不使完成失败，也不声称已修复任何问题。
//
// 两个硬约束塑造了它：
//
//  1. 纯词法判断。路径在 daemon 机器上，服务器无法像 CLI lint 那样 os.Stat。可确定不靠猜的信号只有两个：
//     `file://` URL 与任务自身记录的 work_dir 前缀。更微妙的东西就是猜，猜出来的假信号不值得上仪表盘。
//  2. 路径、正文或它们的任何片段都不得进入指标或日志——只有分类结果与任务 id。
func (s *TaskService) observeChatOutputLocalPath(task db.AgentTaskQueue, body string) {
	if s.Metrics == nil || strings.TrimSpace(body) == "" {
		return
	}
	kind := ""
	switch {
	case strings.Contains(strings.ToLower(body), "file://"):
		kind = "file_url"
	case task.WorkDir.Valid && task.WorkDir.String != "" && strings.Contains(body, task.WorkDir.String):
		kind = "workdir_path"
	default:
		return
	}
	s.Metrics.RecordChatOutputLocalPath(kind)
	slog.Warn("chat reply references a runtime-local path",
		"task_id", util.UUIDToString(task.ID),
		"kind", kind,
	)
}

// FailTask 把任务标记为失败。这里【不】改 issue 状态——agent 经 CLI 自行管理。
//
// sessionID/workDir 可选：agent 在失败前建立过真实会话（如会话中崩溃、被取消、工具报错）时，daemon 应传入，
// 以便在任务行与 chat_session 上保留恢复指针——否则下一条 chat 回合会静默开全新会话、丢失记忆。
// durableWorkDir 只持久化在任务行，绝不替换用于会话恢复的真实 workDir。
//
// failureReason 是供自动重试路径消费的粗分类。未知时传 ""——服务器会对原始错误文本跑
// taskfailure.Classify，使持久化的 failure_reason 落在规范的精分类体系，而不是 legacy 的 "agent_error" 粗桶。
// daemon 调用方已产出精化原因时（classifyPoisonedError、超时/runtime 分类器等），其值会被原样保留。
func (s *TaskService) FailTask(ctx context.Context, taskID pgtype.UUID, errMsg, sessionID, workDir, branchName, failureReason string, sessionRolloutMissing bool, retiredSessionID, durableWorkDir string) (*db.AgentTaskQueue, error) {
	task, _, err := s.FailTaskWithTransition(ctx, taskID, errMsg, sessionID, workDir, branchName, failureReason, sessionRolloutMissing, retiredSessionID, durableWorkDir)
	return task, err
}

// FailTaskWithTransition is the failure counterpart to
// CompleteTaskWithTransition. The bool is false for an idempotent replay that
// observed an already-terminal row.
func (s *TaskService) FailTaskWithTransition(ctx context.Context, taskID pgtype.UUID, errMsg, sessionID, workDir, branchName, failureReason string, sessionRolloutMissing bool, retiredSessionID, durableWorkDir string) (*db.AgentTaskQueue, bool, error) {
	// Strip bytes PostgreSQL cannot store before anything else reads errMsg, so
	// the classifier, the transaction and every downstream consumer see the one
	// text we will actually persist (GH #7098). Kept at the service boundary
	// rather than only in the /fail handler because failClaimedTaskBeforeLaunch
	// reaches FailTask without going through request decoding.
	errMsg = util.SanitizeTextForPostgres(errMsg)

	// MUL-2946：调用方未给原因时，从错误文本综合出精化原因。这是防止 "agent_error" 粗行进入
	// agent_task_queue.failure_reason 的最后一道写路径防线——其它路径要么直接给已分类原因（清扫器经 SQL 写
	// 'queued_expired' / 'runtime_offline' / 'timeout' / 'runtime_recovery'），要么走 daemon 的
	// classifyPoisonedError + taskfailure.Classify 链。
	if failureReason == "" {
		failureReason = taskfailure.Classify(errMsg).String()
	}
	// MUL-5370：daemon 按各自节奏升级，依赖新 daemon 侧标签的修复只覆盖恰好升过级的主机。旧 daemon 对失败的
	// skill-bundle 下载上报一个【非空】兜底原因，上方分支刻意不去动它——没有这步归一化，重试与可操作文案都会
	// 跳过每个未升级主机。它在空原因分支之后执行，使那里综合出的 legacy 原因也被归一化；又在下方重试预计算
	// 之前，保证是升级后的原因在决定重试资格。
	failureReason = taskfailure.NormalizeDaemonReason(failureReason, errMsg).String()

	// Life 认知任务自带重试预算并持有独立认领围栏。走普通任务路径失败会制造第二次队列重试，而任务本身会一直
	// 运行到调和器发现为止。改为在单个事务里同时收尾两行。
	if current, err := s.Queries.GetAgentTask(ctx, taskID); err == nil {
		if _, governed := lifeTaskEnvelopeForQueue(current); governed && lifeTaskActive(current.Status) {
			return s.failLifeCognitionTaskWithTransition(ctx, taskID, errMsg, failureReason)
		}
	}

	// 预计算自动重试，使重试子任务能在失败【同一】事务内创建（MUL-4351）。原子化处理关掉了“失败提交与重试出现
	// 之间，更新的 chat 任务可能认领现已空闲的会话、插队到重试之前”的窗口。overlay 构建可能做网络 I/O
	//（Composio），因此在此处——事务之前——解析，且仅对可重试失败解析，常见 agent_error 路径完全跳过该工作。
	var (
		wantRetry        bool
		retryOverlay     runtimeMCPOverlayData
		retryFireAt      pgtype.Timestamptz
		retryMaxAttempts pgtype.Int4
	)
	if retryableReasons[failureReason] {
		if parent, perr := s.Queries.GetAgentTask(ctx, taskID); perr != nil {
			slog.Warn("fail task auto-retry: load parent failed",
				"task_id", util.UUIDToString(taskID), "error", perr)
		} else if retryEligible(failureReason, parent) {
			wantRetry = true
			// 把“按原因得出的有效预算”持久化到子任务，让重试链自描述（例如 provider_network → max_attempts=3），
			// 而不是泄露 attempt=N/max_attempts=2 的自相矛盾行。
			retryMaxAttempts = pgtype.Int4{Int32: retryAttemptCeiling(failureReason, parent.MaxAttempts), Valid: true}
			// 该原因的调度需要退避时延迟本次尝试（provider_network 的最终尝试等约 5s）；零延迟则 fire_at 保持 NULL，
			// 子任务创建后立即可认领。
			if delay := retryDelayForAttempt(failureReason, parent.Attempt); delay > 0 {
				retryFireAt = pgtype.Timestamptz{Time: time.Now().Add(delay), Valid: true}
			}
			if agent, aerr := s.Queries.GetAgent(ctx, parent.AgentID); aerr != nil {
				// best-effort：overlay 缺失不致命——子任务只是不带 Composio overlay 运行。
				slog.Warn("fail task auto-retry: load agent for overlay failed",
					"task_id", util.UUIDToString(taskID),
					"agent_id", util.UUIDToString(parent.AgentID), "error", aerr)
			} else {
				retryOverlay = s.buildRuntimeMCPOverlay(ctx, parent.OriginatorUserID, agent)
			}
		}
	}

	var task db.AgentTaskQueue
	var retried *db.AgentTaskQueue
	var terminalEvent events.Event
	var retryQueuedEvent events.Event
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		if err := lockChatSessionForTaskWrite(ctx, qtx, taskID); err != nil {
			return err
		}
		t, err := qtx.FailAgentTask(ctx, db.FailAgentTaskParams{
			ID:             taskID,
			Error:          pgtype.Text{String: errMsg, Valid: true},
			FailureReason:  pgtype.Text{String: failureReason, Valid: failureReason != ""},
			SessionID:      pgtype.Text{String: sessionID, Valid: sessionID != ""},
			WorkDir:        pgtype.Text{String: workDir, Valid: workDir != ""},
			DurableWorkDir: pgtype.Text{String: durableWorkDir, Valid: durableWorkDir != ""},
			// 失败的运行仍可能产出分支：worktree 模式会在拆除前提交 agent 留下的一切，正是为了让部分成果存活。
			// 在此丢弃分支名会让那次提交失去指向。
			BranchName:            pgtype.Text{String: branchName, Valid: branchName != ""},
			SessionRolloutMissing: sessionRolloutMissing,
			RetiredSessionID:      pgtype.Text{String: retiredSessionID, Valid: retiredSessionID != ""},
		})
		if err != nil {
			return err
		}
		task = t

		// 与状态翻转原子，同完成路径。已收到恢复评论的失败协调者即已消耗义务：既有的 delivered_comment_ids
		// 覆盖检查本就从不看覆盖任务的状态。
		if err := SettleDeliveredDelegatedFailureRecoveries(ctx, qtx, t); err != nil {
			return err
		}

		// 恢复不安全（resume-unsafe）的会话保留在任务行上供观测，但绝不提升为 chat 级恢复指针。
		//
		// 仅仅“不覆盖”并不够：认领 handler 在查 GetLastChatTaskSession【之前】先读 chat_session.session_id，
		// 已持死会话的指针会绕过该查询的一切过滤器，下一回合便会恢复它（GH #6066）。在同一事务内清除，
		// 用精确的会话与 runtime 匹配，使并发回合更新的指针不受影响。用 ResumeUnsafeFailure（而非仅原因）判断，
		// 让未升级 daemon 的 agent_error.unknown 行也能被错误文本兜住。
		if t.ChatSessionID.Valid && ResumeUnsafeFailure(failureReason, errMsg) {
			deadSession := sessionID
			if deadSession == "" {
				deadSession = t.SessionID.String
			}
			if deadSession != "" {
				if err := qtx.ClearChatSessionSessionIfMatches(ctx, db.ClearChatSessionSessionIfMatchesParams{
					ID:        t.ChatSessionID,
					SessionID: pgtype.Text{String: deadSession, Valid: true},
					RuntimeID: t.RuntimeID,
				}); err != nil {
					return fmt.Errorf("clear poisoned chat session resume pointer: %w", err)
				}
			}
		}
		// 运行显式退役的会话也必须清掉，无论终态如何：替换它的全新会话重试很可能已成功，否则指针仍会指向
		// 它重试所离开的那份转录。
		if t.ChatSessionID.Valid && retiredSessionID != "" {
			if err := qtx.ClearChatSessionSessionIfMatches(ctx, db.ClearChatSessionSessionIfMatchesParams{
				ID:        t.ChatSessionID,
				SessionID: pgtype.Text{String: retiredSessionID, Valid: true},
				RuntimeID: t.RuntimeID,
			}); err != nil {
				return fmt.Errorf("clear retired chat session resume pointer: %w", err)
			}
		}

		// 用 ResumeUnsafeFailure 而非 resumeUnsafeFailureReason：只看原因的检查会放过未升级 daemon 的
		// agent_error.unknown 行，从而重新钉住上面刚清掉的会话。
		if t.ChatSessionID.Valid && !ResumeUnsafeFailure(failureReason, errMsg) {
			// 把 chat_session 的 runtime_id 与 session_id 一起钉住，使下次认领可套用 runtime 守卫。两字段同进退：
			// 没有可记录的 session_id 时不动 runtime_id（NULL → COALESCE 保留现值）。
			var sessionRuntimeID pgtype.UUID
			if sessionID != "" {
				sessionRuntimeID = t.RuntimeID
			}
			if err := qtx.UpdateChatSessionSession(ctx, db.UpdateChatSessionSessionParams{
				ID:        t.ChatSessionID,
				SessionID: pgtype.Text{String: sessionID, Valid: sessionID != ""},
				WorkDir:   pgtype.Text{String: workDir, Valid: workDir != ""},
				RuntimeID: sessionRuntimeID,
			}); err != nil {
				return fmt.Errorf("update chat session resume pointer: %w", err)
			}
		}

		// 与失败原子地创建重试子任务。CreateRetryTask 读刚失败的父行（同事务），因此继承 chat_input_task_id 与
		// 提升后的 chat 重试优先级；广播/通知在提交后进行。这个后继检查是优化，【不是】安全机制：它是 READ
		// COMMITTED 下不加锁的普通计数，rerun 总能在它和下方插入之间提交；它只在后继已可见时省下工作并给出
		// 可读的跳过日志。竞态下的正确性由 CreateRetryTask 的 ON CONFLICT DO NOTHING 承担——让出槽位而非
		// 抛错，因此绝不会中止本事务（本事务还带着父任务的 failed 状态）。
		createRetry := wantRetry
		if createRetry {
			successor, herr := hasRunnableSuccessor(ctx, qtx, t)
			if herr != nil {
				return fmt.Errorf("check runnable successor: %w", herr)
			}
			if successor {
				slog.Info("fail task auto-retry skipped: a successor is already pending",
					"task_id", util.UUIDToString(taskID),
					"issue_id", util.UUIDToString(t.IssueID),
					"agent_id", util.UUIDToString(t.AgentID),
				)
				createRetry = false
			}
		}
		if createRetry {
			child, cerr := qtx.CreateRetryTask(ctx, db.CreateRetryTaskParams{
				NewTaskID:            dbid.NewV7(),
				ID:                   taskID,
				FireAt:               retryFireAt,
				MaxAttempts:          retryMaxAttempts,
				RuntimeMcpOverlay:    retryOverlay.Overlay,
				RuntimeConnectedApps: retryOverlay.ConnectedApps,
			})
			switch {
			case cerr == nil:
				transferErr := transferPendingSourceContextToRetry(ctx, qtx, t, child)
				if errors.Is(transferErr, pgx.ErrNoRows) {
					deleted, deleteErr := qtx.DeleteUnstartedQuickCreateRetryTask(ctx, child.ID)
					if deleteErr != nil || deleted != 1 {
						return fmt.Errorf("discard source context retry without attach authority: changed=%d: %w", deleted, deleteErr)
					}
					slog.Info("fail task auto-retry skipped: source context already transferred or attached",
						"task_id", util.UUIDToString(taskID),
						"source_context_retry_id", util.UUIDToString(child.ID),
					)
				} else if transferErr != nil {
					return fmt.Errorf("transfer pending source context to retry: %w", transferErr)
				} else {
					if err := qtx.CopyChannelTaskDelivery(ctx, db.CopyChannelTaskDeliveryParams{
						ChildTaskID: child.ID, ParentTaskID: taskID,
					}); err != nil {
						return fmt.Errorf("copy retry channel delivery: %w", err)
					}
					retried = &child
					// 立即重试是一次真实的 ∅ → queued 转换。在父任务终态写入旁记录它，崩溃便不会留下“子任务可认领
					// 但对 durable 消费者不可见”。deferred 子任务刻意只在提升事务把它翻成 queued 时才宣告。
					if child.Status == "queued" && s.TxStarter != nil {
						workspaceID, workspaceErr := resolveTaskWorkspaceWithQueries(ctx, qtx, t)
						if workspaceErr != nil {
							return fmt.Errorf("resolve retry task workspace: %w", workspaceErr)
						}
						retryQueuedEvent, err = RecordTaskQueuedEventTx(ctx, qtx,
							workspaceID, child)
						if err != nil {
							return fmt.Errorf("record retry task queued event: %w", err)
						}
					}
				}
			case errors.Is(cerr, pgx.ErrNoRows):
				// 语句什么都没写：要么宿主工作区在途中被拆除，要么 rerun 在上方不加锁检查之后抢走了 pending 槽位
				//（ON CONFLICT DO NOTHING）。两者都不构成中止理由——本事务仍握着父任务的 failed 状态，丢了它会让任务
				// 卡在 'running'。记下这个失败，不带重试继续。
				slog.Info("fail task auto-retry not created: no row written",
					"task_id", util.UUIDToString(taskID),
					"issue_id", util.UUIDToString(t.IssueID),
					"agent_id", util.UUIDToString(t.AgentID),
				)
			default:
				return fmt.Errorf("create retry task: %w", cerr)
			}
		}

		// 终态且未重试的 chat 失败是一条可见的助手结果。趁会话锁与失败事务仍持有就持久化它，再重锚下一条 direct
		// head。否则后继消息可能在状态翻转与写行之间被认领，把它的用户输入排到所跟随的失败之前。
		if t.ChatSessionID.Valid && retried == nil {
			// 本回合已死，它拥有的一切必须转移。收养的 onboarding kickoff 若仍绑在永不再跑的任务上：下一回合会无上下文
			// 地到达 Mika，也没有“她已问候过该成员”的记录，于是她会再次自我介绍（MUL-5827）。该查询把它交给会话的
			// 下一条排队回合——包括成员在本回合仍在跑时排的那条（发送时收养捕获不到的）。
			//
			// 刻意以 retried == nil 为门槛。重试子任务继承根的 chat_input_task_id，kickoff 仍绑定在根上，重试仍能读到它
			//——在这里释放会把即将运行的回合的上下文剥掉。
			if err := qtx.ReleaseOnboardingKickoffFromTask(ctx, chatInputOwnerID(t)); err != nil {
				return fmt.Errorf("release onboarding kickoff: %w", err)
			}
			if _, err := createAssistantChatMessage(ctx, qtx, db.CreateChatMessageParams{
				ID:            dbid.NewV7(),
				ChatSessionID: t.ChatSessionID,
				Role:          "assistant",
				Content:       redact.Text(errMsg),
				TaskID:        t.ID,
				FailureReason: pgtype.Text{String: failureReason, Valid: failureReason != ""},
				ElapsedMs:     computeChatElapsedMs(t),
			}); err != nil {
				return fmt.Errorf("write chat failure outcome: %w", err)
			}
		}
		if s.TxStarter != nil {
			terminalEvent, err = recordTaskTerminalEventTx(ctx, qtx, protocol.EventTaskFailed, t,
				taskFailedFields(errMsg, failureReason, retried != nil))
			if err != nil {
				return fmt.Errorf("record task failure event: %w", err)
			}
		}
		return nil
	}); err != nil {
		if existing, lookupErr := s.Queries.GetAgentTask(ctx, taskID); lookupErr == nil {
			if errors.Is(err, pgx.ErrNoRows) {
				slog.Info("fail task: already finalized",
					"task_id", util.UUIDToString(taskID),
					"current_status", existing.Status,
					"agent_id", util.UUIDToString(existing.AgentID),
				)
				return &existing, false, nil
			}
			slog.Warn("fail task failed",
				"task_id", util.UUIDToString(taskID),
				"current_status", existing.Status,
				"issue_id", util.UUIDToString(existing.IssueID),
				"chat_session_id", util.UUIDToString(existing.ChatSessionID),
				"agent_id", util.UUIDToString(existing.AgentID),
				"error", err,
			)
		} else {
			slog.Warn("fail task failed: task not found",
				"task_id", util.UUIDToString(taskID),
				"lookup_error", lookupErr,
			)
		}
		return nil, false, fmt.Errorf("fail task: %w", err)
	}

	slog.Warn("task failed", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(task.IssueID), "error", errMsg, "failure_reason", failureReason)
	s.captureTaskFailed(ctx, task)
	// 修改 5. 失败任务名下的同步批次立刻收敛，不然新任务 start 会一直撞 ErrBusy。
	s.closeUpstreamRuns(ctx, task, "failed", "任务失败，批次已关闭")

	// 自动重试子任务（若有）已在上方事务内创建，因此
	// 此时不会再有更新的 chat 任务插队。立即对外宣告：先广播 queued、再通知 daemon——顺序理由见
	// EnqueueTaskForIssue。deferred 子任务（经 fire_at 启用退避）尚未 queued：PromoteDueDeferredTasksForRuntime
	// 会在 fire_at 到达时发出其 queued 事件与 daemon 唤醒，因此在这里宣告是错误的。
	if retried != nil {
		slog.Info("task auto-retry enqueued",
			"parent_task_id", util.UUIDToString(task.ID),
			"child_task_id", util.UUIDToString(retried.ID),
			"reason", failureReason,
			"attempt", retried.Attempt,
			"max_attempts", retried.MaxAttempts,
			"status", retried.Status,
		)
		if retried.Status == "queued" {
			if retryQueuedEvent.ID != "" {
				publishCommittedEvent(s.Bus, retryQueuedEvent)
			} else {
				s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, *retried)
			}
			s.NotifyTaskEnqueued(ctx, *retried)
		}
	}

	// 委托任务到达终态失败后必须把控制权交还委托它的任务。只在既有的重试决策之后执行：可重试失败保持当前
	// 策略，子尝试进行中保持静默。恢复信号是平台在源任务 issue 上写的一条评论，显式路由给该任务的 agent；
	// recoverDelegatedTaskFailure 会与既有协调者运行合并，并按失败任务 id 去重。
	if retried == nil {
		_, recoveryErr := s.recoverDelegatedTaskFailure(ctx, task)
		if recoveryErr != nil {
			slog.Warn("delegated task failure recovery failed",
				"task_id", util.UUIDToString(task.ID),
				"delegated_from_task_id", util.UUIDToString(task.DelegatedFromTaskID),
				"error", recoveryErr,
			)
		}
	}

	// 即将立即重试时跳过每条失败的系统评论——新任务会向用户呈现自己的状态；我们也不希望每次 daemon 抖动都
	// 用 “task timed out” 刷屏 issue。委托失败除协调者恢复信号外仍保留这条既有失败评论，保证跨 issue 交接的
	// 两侧都可见。
	if errMsg != "" && task.IssueID.Valid && retried == nil {
		s.createAgentComment(ctx, task.IssueID, task.AgentID, redact.Text(errMsg), "system", task.TriggerCommentID, task.ID)
	}

	// quick-create 任务：向请求者推送失败收件箱通知，让其在保留原始提示词的前提下重试或退回高级表单。
	// 已有自动重试 pending 时跳过——新尝试会写自己的结果。
	if retried == nil {
		if qc, ok := s.parseQuickCreateContext(task); ok {
			attached, attachedErr := s.sourceContextAttachedByTask(ctx, task, qc)
			switch {
			case attachedErr != nil:
				slog.Error("quick-create failure: source context outcome lookup failed",
					"task_id", util.UUIDToString(task.ID), "error", attachedErr)
				s.notifyQuickCreateUnconfirmed(ctx, task, qc)
			case attached:
				// CLI create 在 runtime 上报自身失败前已提交。已挂接的上下文是目标确实存在的权威证据，因此走正常
				// 的成功收件箱调和，而不是让误导性的失败行诱发重复重试。
				s.notifyQuickCreateCompleted(ctx, task, qc, nil)
			default:
				s.notifyQuickCreateFailed(ctx, task, qc, errMsg)
			}
		}
	}
	// 调和 agent 状态
	s.ReconcileAgentStatus(ctx, task.AgentID)

	// 广播 durable 的终态事实。没有事务启动器的夹具保留单测使用的 legacy 进程内路径。
	if terminalEvent.ID != "" {
		publishCommittedEvent(s.Bus, terminalEvent)
	} else {
		s.broadcastTaskFailedEvent(ctx, task, errMsg, failureReason, retried != nil)
	}

	return &task, true, nil
}

// retryableReasons 枚举自动重试路径允许处理的失败原因。agent 侧错误（编译失败、模型拒绝等）被刻意排除——
// 那些是用户应看到的真实问题，不是基础设施抖动。
//
// 唯一的 agent_error.* 例外是 provider_network：流中段的 provider 断连（如 Claude Code 的 “API Error:
// Connection closed mid-response”）是瞬时基础设施抖动而非 agent 决策。无人值守的 issue 运行否则会因此终止，
// 而交互式 chat 之所以能扛住，只是因为 CLI 自身的进程内重试碰巧先恢复——于是让平台直接重试它（MUL-4910）。
// 它可安全恢复（不在 resumeUnsafeFailureReason 里），因此重试子任务继承会话并续聊，而不是从头重启。
// skill_bundle_unavailable 同理可重试：agent 进程根本没启动，没有幂等可谈；已下载的 bundle 都缓存在磁盘上，
// 重试从缓存继续而不是整体重拉（MUL-5370）。
var retryableReasons = map[string]bool{
	string(taskfailure.ReasonRuntimeOffline):         true,
	string(taskfailure.ReasonRuntimeRecovery):        true,
	string(taskfailure.ReasonTimeout):                true,
	"codex_semantic_inactivity":                      true,
	string(taskfailure.ReasonAgentProviderNetwork):   true,
	string(taskfailure.ReasonSkillBundleUnavailable): true,
}

// runtime_offline 的重试以 deferred 而非 queued 开始：其正值的 fire_at 让它在新一轮 runtime 心跳回归后
// 走健康门控的提升。队列清扫器同样豁免这条重试血缘，覆盖“提升后、认领前 runtime 再次掉线”的竞态。
// 该延迟只是状态标记，不是重连策略；心跳门控才是权威。
//
// 瞬时 provider 流中断（provider_network）有专属三级时间表（MUL-4910）：首跑 + 立即重试 + 一次约 5s 延迟的
// 重试。扛过立即重试的抖动会在最终尝试前获得短暂冷却，而不是背靠背连发。其它可重试原因保持任务通用
// max_attempts 上限并立即重试。
const (
	runtimeOfflineRetryDeferral   = time.Second
	providerNetworkMaxAttempts    = 3
	providerNetworkFinalRetryWait = 5 * time.Second
)

// retryAttemptCeiling 返回自动重试路径为某个失败原因允许的尝试次数。它只【放宽】任务通用 max_attempts，
// 且只针对带专属时间表的原因；其它一律保持列值（默认 2 = 首跑 + 一次重试）。
//
// max_attempts <= 1 显式禁用自动重试（055_task_lease_and_retry.up.sql：”1 disables retry”），因此绝不被
// 覆盖——被禁用的任务不能因上限抬高而复活。调用方把该值持久化进重试子任务（CreateRetryTask 的
// max_attempts），使行自洽：provider_network 链记录 attempt=3、max_attempts=3，而不是自相矛盾的
// attempt=3、max_attempts=2（MUL-4910）。
func retryAttemptCeiling(reason string, taskMaxAttempts int32) int32 {
	if taskMaxAttempts <= 1 {
		return taskMaxAttempts
	}
	if reason == string(taskfailure.ReasonAgentProviderNetwork) && taskMaxAttempts < providerNetworkMaxAttempts {
		return providerNetworkMaxAttempts
	}
	return taskMaxAttempts
}

// retryDelayForAttempt 返回在 failedAttempt 失败后，下一次尝试应延迟多久。runtime_offline 总是获得正值的
// fire_at，以等待健康门控提升路径。provider_network 的最终尝试延迟约 5s；其它重试保持立即（零延迟 → 子任务
// 以 queued 创建、立刻可认领）。调用方经 fire_at 把返回值传给 CreateRetryTask。
func retryDelayForAttempt(reason string, failedAttempt int32) time.Duration {
	if reason == string(taskfailure.ReasonRuntimeOffline) {
		return runtimeOfflineRetryDeferral
	}
	if reason == string(taskfailure.ReasonAgentProviderNetwork) &&
		failedAttempt >= providerNetworkMaxAttempts-1 {
		return providerNetworkFinalRetryWait
	}
	return 0
}

// resumeUnsafeFailureReason 判断失败原因是否会毒化 agent【会话】（而非 workdir）：恢复同一会话会立即重放
// 卡死/超大状态。与 GetLastTaskSession / GetLastChatTaskSession 的恢复黑名单保持同步。（CreateRetryTask 的
// fresh-session CASE WHEN 只需其中也自动可重试的子集，目前是 codex_semantic_inactivity。）
func resumeUnsafeFailureReason(reason string) bool {
	switch reason {
	// 毒化 agent【会话】（而非 workdir）的失败：恢复同一会话会立即重放卡死/超大的状态。
	// 与 GetLastTaskSession / GetLastChatTaskSession 恢复黑名单同步。（CreateRetryTask 的 fresh-session
	// CASE WHEN 只需其中自动可重试的子集，当前是 codex_semantic_inactivity。）
	// codex_resume_oversized 是本集合最强成员：codex 滚动只会变大，某线程的恢复响应一旦溢出读端，未来每次
	// 尝试都会溢出。
	case "iteration_limit", "agent_fallback_message", "api_invalid_request", "codex_semantic_inactivity", "agent_error.context_overflow", "codex_resume_oversized":
		return true
	default:
		return false
	}
}

// ResumeUnsafeFailure 判断失败任务的 agent 会话在重试时【不得】被恢复。它把 failure_reason 毒集
// （resumeUnsafeFailureReason）与 GetLastTaskSession / GetLastChatTaskSession 恢复查询对原始错误文本的
// 同款纵深防御结合起来：Anthropic 400 invalid_request_error 意味着会话历史本身不可处理，即使 failure_reason
// 被误分类或未分类（MUL-1921 之前写入的 legacy 'agent_error' 行，或部署窗口行）。只有 failure_reason 的
// 调用方（例如失败时刻）可传空 errorText。
//
// 这是手动重试认领路径的共享事实来源——它直接读确切源任务而非 GetLastTaskSession，否则会绕过错误文本守卫。
func ResumeUnsafeFailure(failureReason, errorText string) bool {
	if resumeUnsafeFailureReason(failureReason) {
		return true
	}
	lower := strings.ToLower(errorText)
	if strings.Contains(lower, "400") && strings.Contains(lower, "invalid_request_error") {
		return true
	}
	// provider 凭据解析失败在恢复上是确定性的：缺失的 api_key / auth_token / auth header 已烙进会话的 provider
	// 状态，因此 rerun 必须全新开始，而不是在记录的 (agent, issue) 会话上重放同样的鉴权错误。taskfailure.Classify
	// 刻意把该错误留成 agent_error.unknown，因此这个与原因无关的文本守卫是对新旧已持久化行的承重保护。
	// 与 GetLastTaskSession / GetLastChatTaskSession 恢复查询保持同步。
	//
	// 短语本身在 taskfailure.AuthMethodUnresolved，与 daemon 回合内 fresh-session 重试门共享，使两层对
	// “哪些错误意味着该会话永不可恢复”的判断不可能分歧。
	if taskfailure.AuthMethodUnresolved(errorText) {
		return true
	}
	// 对与 provider 无关的空消息形态做同样的纵深防御：太老、不带 classifyPoisonedError 新分支的 daemon 会上报
	// agent_error.unknown，没有这条守卫时手动重试路径会愉快地恢复 provider 刚拒绝的转录（GH #6066）。
	return taskfailure.UnresumableHistory(errorText)
}

// retryEligible 判断失败任务是否符合自动重试：基础设施型 failure_reason、尝试预算未耗尽、非 autopilot 运行、
// 且关联了 issue 或 chat 会话。FailTask 的事务内重试与孤儿清扫器的 MaybeRetryFailedTask 共用，保证两者对
// “哪些失败会重跑”判断一致。
func retryEligible(failureReason string, t db.AgentTaskQueue) bool {
	return retryableReasons[failureReason] &&
		t.Attempt < retryAttemptCeiling(failureReason, t.MaxAttempts) &&
		!t.AutopilotRunID.Valid &&
		(t.IssueID.Valid || t.ChatSessionID.Valid || isSourceContextQuickCreateTask(t))
}

// isSourceContextQuickCreateTask 判断任务是否为“携带待挂接源上下文的 quick-create”任务（无 issue/chat/
// autopilot 链接，context 里带 SourceContextID）——这类任务没有现成可指的目标，需要独立支持重试。
func isSourceContextQuickCreateTask(task db.AgentTaskQueue) bool {
	if len(task.Context) == 0 || task.IssueID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid {
		return false
	}
	var quickCreate QuickCreateContext
	return json.Unmarshal(task.Context, &quickCreate) == nil &&
		quickCreate.Type == QuickCreateContextType && quickCreate.SourceContextID != ""
}

// hasRunnableSuccessor 判断另一条尚未开始的任务是否已占据 idx_one_pending_task_per_issue_agent_v2 允许的
// 每 (issue, agent) 唯一 queued/dispatched 槽位。
//
// 两条自动重试路径在插入重试子任务前都会查询它。手动 rerun 现在可排在仍在运行的任务【之后】而不必取消它，
// 因此到该任务失败时其槽位可能已被占用，而重试的目的是给 issue 一个“已经存在的可运行后继”。
//
// 仅供参考：查询不加锁，返回后 rerun 提交永远可能发生。让竞态无害的是 CreateRetryTask 的 ON CONFLICT DO
// NOTHING。提前跳过只是避免无谓工作并记录为何不重试。
//
// chat / quick-create 任务不带 issue_id，索引对它们不适用，重试永不会碰撞。
func hasRunnableSuccessor(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (bool, error) {
	if !task.IssueID.Valid {
		return false, nil
	}
	return q.HasPendingTaskForIssueAndAgent(ctx, db.HasPendingTaskForIssueAndAgentParams{
		IssueID: task.IssueID,
		AgentID: task.AgentID,
	})
}

// MaybeRetryFailedTask 在最近失败的任务属于基础设施型失败（daemon 崩溃、runtime 离线、派发/运行超时）且
// 未耗尽 max_attempts 预算时，为其派生一条全新 queued 尝试。子任务继承 agent/runtime/issue/chat 链接，
// 对可安全恢复的失败还继承父任务 session_id/work_dir，使后端支持时 agent 能续聊。返回新任务；未创建重试时
// 返回 nil。
//
// autopilot 任务不在此自动重试——autopilot 调度器掌管自己的重跑节奏，不希望双重触发。
func (s *TaskService) MaybeRetryFailedTask(ctx context.Context, parent db.AgentTaskQueue) (*db.AgentTaskQueue, error) {
	if parent.Status != "failed" {
		return nil, nil
	}
	reason := ""
	if parent.FailureReason.Valid {
		reason = parent.FailureReason.String
	}
	if !retryableReasons[reason] {
		return nil, nil
	}
	// 用按原因计算的上限而非原始 max_attempts 列：在第二次尝试被恢复的孤儿 provider_network 任务仍应获准其
	// 延迟的第三次尝试（retryAttemptCeiling 把上限抬到 3）。与下方 retryEligible 保持一致——它对主 FailTask
	// 路径应用同一上限。
	if parent.Attempt >= retryAttemptCeiling(reason, parent.MaxAttempts) {
		slog.Info("task auto-retry skipped: budget exhausted",
			"task_id", util.UUIDToString(parent.ID),
			"attempt", parent.Attempt,
			"max_attempts", parent.MaxAttempts,
			"ceiling", retryAttemptCeiling(reason, parent.MaxAttempts),
		)
		return nil, nil
	}
	// autopilot 有自己的重试语义（不双重触发）；无 issue/chat 链接的任务无处上报其重试——retryEligible 同时
	// 覆盖两者，使该清扫器路径与 FailTask 的事务内重试保持一致。
	if !retryEligible(reason, parent) {
		return nil, nil
	}

	var runtimeMCPOverlay runtimeMCPOverlayData
	agent, agentErr := s.Queries.GetAgent(ctx, parent.AgentID)
	if agentErr != nil {
		// best-effort：解析 overlay 所需的 agent 失败不致命。记日志继续——若 agent 真没了，daemon 稍后会在认领
		// 时拒绝。
		slog.Warn("task auto-retry: load agent for overlay failed",
			"parent_task_id", util.UUIDToString(parent.ID),
			"agent_id", util.UUIDToString(parent.AgentID),
			"error", agentErr,
		)
	} else {
		runtimeMCPOverlay = s.buildRuntimeMCPOverlay(ctx, parent.OriginatorUserID, agent)
	}
	// 镜像 FailTask 的事务内退避 + 有效预算持久化：把 provider_network 最终尝试经 fire_at 延迟约 5s（零延迟则
	// fire_at 留 NULL，子任务立即生效），并把按原因计算的上限写入子任务 max_attempts，保证重试链自洽。
	var retryFireAt pgtype.Timestamptz
	if delay := retryDelayForAttempt(reason, parent.Attempt); delay > 0 {
		retryFireAt = pgtype.Timestamptz{Time: time.Now().Add(delay), Valid: true}
	}
	// 与 FailTask 路径相同的参考性槽位检查，理由相同：后继已可见时跳过该工作。输掉竞态由 CreateRetryTask 让出
	// 槽位处理，本调用方把它读作 “无重试”。
	if successor, herr := hasRunnableSuccessor(ctx, s.Queries, parent); herr != nil {
		slog.Warn("task auto-retry: successor check failed; attempting retry anyway",
			"parent_task_id", util.UUIDToString(parent.ID), "error", herr)
	} else if successor {
		slog.Info("task auto-retry skipped: a successor is already pending",
			"parent_task_id", util.UUIDToString(parent.ID),
			"issue_id", util.UUIDToString(parent.IssueID),
			"agent_id", util.UUIDToString(parent.AgentID),
		)
		return nil, nil
	}
	if s.TxStarter == nil {
		return nil, errors.New("task auto-retry: transaction starter is required")
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("task auto-retry: begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	var retryQueuedEvent events.Event
	child, err := qtx.CreateRetryTask(ctx, db.CreateRetryTaskParams{
		NewTaskID:            dbid.NewV7(),
		ID:                   parent.ID,
		FireAt:               retryFireAt,
		MaxAttempts:          pgtype.Int4{Int32: retryAttemptCeiling(reason, parent.MaxAttempts), Valid: true},
		RuntimeMcpOverlay:    runtimeMCPOverlay.Overlay,
		RuntimeConnectedApps: runtimeMCPOverlay.ConnectedApps,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// 工作区被拆除，或 pending 槽位在上方检查与本次插入之间被占用。与 FailTask 路径相同的契约：无重试、
		// 无错误。
		slog.Info("task auto-retry not created: no row written",
			"parent_task_id", util.UUIDToString(parent.ID),
			"reason", reason,
		)
		return nil, nil
	}
	if err != nil {
		slog.Warn("task auto-retry failed",
			"parent_task_id", util.UUIDToString(parent.ID),
			"reason", reason,
			"error", err,
		)
		return nil, err
	}
	if err := transferPendingSourceContextToRetry(ctx, qtx, parent, child); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Info("task auto-retry skipped: source context already transferred or attached",
				"parent_task_id", util.UUIDToString(parent.ID),
				"source_context_retry_id", util.UUIDToString(child.ID),
			)
			return nil, nil
		}
		return nil, err
	}
	if err := qtx.CopyChannelTaskDelivery(ctx, db.CopyChannelTaskDeliveryParams{
		ChildTaskID: child.ID, ParentTaskID: parent.ID,
	}); err != nil {
		return nil, fmt.Errorf("copy auto-retry channel delivery: %w", err)
	}
	if child.Status == "queued" {
		workspaceID, workspaceErr := resolveTaskWorkspaceWithQueries(ctx, qtx, parent)
		if workspaceErr != nil {
			return nil, fmt.Errorf("resolve auto-retry task workspace: %w", workspaceErr)
		}
		retryQueuedEvent, err = RecordTaskQueuedEventTx(ctx, qtx, workspaceID, child)
		if err != nil {
			return nil, fmt.Errorf("record auto-retry task queued event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("task auto-retry: commit: %w", err)
	}
	slog.Info("task auto-retry enqueued",
		"parent_task_id", util.UUIDToString(parent.ID),
		"child_task_id", util.UUIDToString(child.ID),
		"reason", reason,
		"attempt", child.Attempt,
		"max_attempts", child.MaxAttempts,
		"status", child.Status,
	)
	// queued 子任务经历 ∅ → queued（同 EnqueueTaskFor*）：先广播 queued、再通知 daemon——顺序理由见
	// EnqueueTaskForIssue。deferred 子任务（已启用退避）保持惰性，直到 PromoteDueDeferredTasksForRuntime 发出
	// 其 queued 事件与唤醒。
	if child.Status == "queued" {
		if retryQueuedEvent.ID != "" {
			publishCommittedEvent(s.Bus, retryQueuedEvent)
		} else {
			s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, child)
		}
		s.NotifyTaskEnqueued(ctx, child)
	}
	return &child, nil
}

// transferPendingSourceContextToRetry 把父任务仍持有的 pending 源上下文捕获改挂到重试子任务上（带源上下文的
// quick-create 专用；父任务没有该上下文时为空操作）。
func transferPendingSourceContextToRetry(ctx context.Context, q *db.Queries, parent, child db.AgentTaskQueue) error {
	if len(parent.Context) == 0 {
		return nil
	}
	var quickCreate QuickCreateContext
	if json.Unmarshal(parent.Context, &quickCreate) != nil || quickCreate.Type != QuickCreateContextType || quickCreate.SourceContextID == "" {
		return nil
	}
	workspaceID, err := util.ParseUUID(quickCreate.WorkspaceID)
	if err != nil {
		return err
	}
	contextID, err := util.ParseUUID(quickCreate.SourceContextID)
	if err != nil {
		return err
	}
	_, err = q.TransferPendingIssueSourceContextTask(ctx, db.TransferPendingIssueSourceContextTaskParams{
		NewTaskID: child.ID, WorkspaceID: workspaceID, ID: contextID, OldTaskID: parent.ID,
	})
	return err
}

// ErrRerunInvokeNotAllowed 表示 RerunIssue 因当前操作者无权调用解析出的目标 agent 而拒绝重跑。handler 将其
// 映射为结构化 403（不取消、不创建任何任务）。
var ErrRerunInvokeNotAllowed = errors.New("rerun: operator not allowed to invoke target agent")

// RerunIssue 为 issue 上的 agent 创建一条全新 queued 任务，供手动 rerun 端点使用。
//
// 目标 agent 解析：
//   - sourceTaskID 有效：重跑跑过该任务的 agent（并复用其 leader/worker 角色）。执行日志重试按钮即用它，
//     使逐行重试在后续指派变更后仍成立，正确重放被点击行的群组 worker 或 mention agent。源任务的
//     trigger_comment_id 也被继承（调用方未传时），使评论/mention 触发任务的逐行重跑仍是评论触发的——daemon
//     的 buildCommentPrompt 以 TriggerCommentID 为键，丢了它会退化成不带原评论的普通 issue 运行。
//   - sourceTaskID 为空：回退到 issue 当前指派者（agent 或群组长）。保留只有 issue ID、没有具体目标任务
//     的调用方所依赖的 CLI/API 契约。
//
// 重试【总是】在源任务 workdir 仍存在于磁盘时复用它（MUL-4869）：瞬时失败——网络、provider 5xx/限流、
// runtime_offline、超时，或用户已修复的鉴权/配额/配置错误——不应丢掉已完成的工作。只有 agent 会话是条件
// 恢复的，且该决定由 daemon 认领 handler 依据 SOURCE 任务（经 rerun_of_task_id）稍后做出，而非烧进本行。
// enqueueRerunTask 钉死 force_fresh_session=true，使滚动部署期间的旧认领 handler 降级为全新开始而不是按
// (agent, issue) 最近会话恢复；新认领 handler 对 rerun 忽略该标记，只读取确切源任务（rerun_of_task_id）以复用
// 其 workdir，并在失败未毒化会话（见 service.ResumeUnsafeFailure）且源任务在同一 runtime 时恢复其会话。
// 目录确实不可复用（被 GC、认领 runtime 上不存在、从未记录）时 daemon 回退到全新 workdir。孤儿中途失败的
// 自动重试（HandleFailedTasks → MaybeRetryFailedTask → CreateRetryTask）走自己的路径，MUL-1128 的中途恢复
// 契约因此保留。
func (s *TaskService) RerunIssue(ctx context.Context, issueID pgtype.UUID, sourceTaskID pgtype.UUID, triggerCommentID pgtype.UUID, actorUserID pgtype.UUID, canInvoke func(agent db.Agent) bool) (*db.AgentTaskQueue, error) {
	issue, err := s.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("load issue: %w", err)
	}

	// 解析 rerun 的目标 agent。
	var (
		agentID             pgtype.UUID
		isLeader            bool
		squadID             pgtype.UUID
		coalescedCommentIDs []pgtype.UUID
	)
	if sourceTaskID.Valid {
		sourceTask, err := s.Queries.GetAgentTask(ctx, sourceTaskID)
		if err != nil {
			return nil, fmt.Errorf("load source task: %w", err)
		}
		if !sourceTask.IssueID.Valid || util.UUIDToString(sourceTask.IssueID) != util.UUIDToString(issueID) {
			return nil, fmt.Errorf("source task does not belong to this issue")
		}
		agentID = sourceTask.AgentID
		isLeader = sourceTask.IsLeaderTask
		// 携带源任务的群组来源，使 leader 任务的重跑在认领时仍注入群组 briefing（见迁移 127 / daemon 认领
		// handler）。
		squadID = sourceTask.SquadID
		// 继承触发来源，使评论/mention 触发任务的逐行重跑仍是评论触发。没有它会跳过 daemon 的 buildCommentPrompt
		//（以 TriggerCommentID 为键），rerun 退化为失去原评论上下文的普通 issue 运行。仅在调用方未显式传时
		// 覆盖。
		if !triggerCommentID.Valid {
			coalescedCommentIDs = append([]pgtype.UUID{}, sourceTask.CoalescedCommentIds...)
			if sourceTask.TriggerCommentID.Valid {
				triggerCommentID = sourceTask.TriggerCommentID
			} else if len(coalescedCommentIDs) > 0 {
				triggerCommentID, coalescedCommentIDs, err = s.promoteNewestSurvivingComment(ctx, coalescedCommentIDs)
				if err != nil {
					return nil, fmt.Errorf("repair source comment plan: %w", err)
				}
			}
		}
	} else {
		switch {
		case issue.AssigneeType.String == "agent" && issue.AssigneeID.Valid:
			agentID = issue.AssigneeID
		case issue.AssigneeType.String == "squad" && issue.AssigneeID.Valid:
			squad, err := s.Queries.GetSquad(ctx, issue.AssigneeID)
			if err != nil {
				return nil, fmt.Errorf("issue is assigned to a squad but squad not found")
			}
			agentID = squad.LeaderID
			isLeader = true
			squadID = issue.AssigneeID
		default:
			return nil, fmt.Errorf("issue is not assigned to an agent or squad")
		}
	}

	// 在改动任何东西之前，对【解析后】的目标重新校验调用权限（MUL-4525）。task_id rerun 以此门控历史 agent，
	// 被改派过的 issue 就不能被用来重放操作者仅可查看的私有 agent。拒绝即 fail closed：先不取消任何任务，
	// 也不创建新任务。
	if canInvoke != nil {
		targetAgent, err := s.Queries.GetAgent(ctx, agentID)
		if err != nil {
			return nil, fmt.Errorf("load target agent: %w", err)
		}
		if !canInvoke(targetAgent) {
			return nil, ErrRerunInvokeNotAllowed
		}
	}

	// 只清尚未开始执行的任务：它们正是新入队在 idx_one_pending_task_per_issue_agent_v2 下会碰撞的行；替换它们
	// 不损失任何工作，且让新运行归因给重跑的成员（MUL-4302 §5），而非继承创建 pending 行的人。
	//
	// running / waiting_local_directory 任务被刻意保留：里面有 agent 在干活。两种状态都不在唯一索引里，因此
	// 下方入队会在活跃任务【之后】插入 queued 行，ClaimAgentTask 的 per-(issue, agent) 串行化让它停到活跃运行
	// 终态为止。Rerun 过去也会取消它们，于是“请 agent 再来一轮”会静默杀掉它正在做的那轮；打断在途运行是
	// CancelTask / `multica issue cancel-task` 的职责。
	clearPendingSlot := func() int {
		var cancelled []db.AgentTaskQueue
		var persisted []events.Event
		// 与取消原子：pending 协调者任务可能已持有恢复回执，一旦本提交后结算失败，下游无人能修复。
		cerr := s.runInTx(ctx, func(qtx *db.Queries) error {
			var err error
			cancelled, err = qtx.CancelPendingTasksByIssueAndAgent(ctx, db.CancelPendingTasksByIssueAndAgentParams{
				IssueID: issueID,
				AgentID: agentID,
			})
			if err != nil {
				return err
			}
			if err := SettleDeliveredDelegatedFailureRecoveries(ctx, qtx, cancelled...); err != nil {
				return err
			}
			if s.TxStarter != nil {
				var err error
				persisted, err = enqueueTaskTerminalEvents(ctx, qtx, protocol.EventTaskCancelled, cancelled)
				return err
			}
			return nil
		})
		if cerr != nil {
			slog.Warn("rerun: cancel pending tasks failed",
				"issue_id", util.UUIDToString(issueID),
				"agent_id", util.UUIDToString(agentID),
				"error", cerr,
			)
		}
		for _, t := range cancelled {
			s.captureTaskCancelled(ctx, t)
			s.ReconcileAgentStatus(ctx, t.AgentID)
		}
		if s.TxStarter != nil {
			for _, event := range persisted {
				publishCommittedEvent(s.Bus, event)
			}
		} else {
			for _, t := range cancelled {
				s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
			}
		}
		return len(cancelled)
	}
	cancelledCount := clearPendingSlot()

	// 手动 rerun 是一次归因给“重跑的成员”而非“原运行的人类”的全新 direct_human 触发（MUL-4302 §5）；
	// actorUserID 携带该成员。sourceTaskID 是 rerun 血缘：随 CreateAgentTask 插入走（rerun_of_task_id），使 queued
	// 事件 / daemon 认领不会看到 NULL 血缘，并与系统重试的 retry_of_task_id 保持区分（§5）。
	task, err := s.enqueueRerunTask(ctx, issue, agentID, triggerCommentID, coalescedCommentIDs, isLeader, squadID, actorUserID, sourceTaskID)
	if pendingSlotTakenErr(err) {
		// 上面的清理与本入队是两次独立提交，期间并发的 FailTask 创建的系统重试可能抢走 pending 槽位。CreateRetryTask
		// 在槽位已占时会让出，但它无法让给“尚不存在的行”，因此在该窗口内提交的重试会先到。再清一次并重试：
		// 有意的人类操作才应持有槽位。限制为单次额外尝试——第二次冲突意味着有人在循环入队，值得暴露而不是
		// 空转。
		slog.Info("issue rerun: pending slot taken concurrently, reclaiming",
			"issue_id", util.UUIDToString(issueID),
			"agent_id", util.UUIDToString(agentID),
		)
		cancelledCount += clearPendingSlot()
		task, err = s.enqueueRerunTask(ctx, issue, agentID, triggerCommentID, coalescedCommentIDs, isLeader, squadID, actorUserID, sourceTaskID)
	}
	if err != nil {
		return nil, err
	}
	slog.Info("issue rerun enqueued",
		"task_id", util.UUIDToString(task.ID),
		"issue_id", util.UUIDToString(issueID),
		"agent_id", util.UUIDToString(agentID),
		"source_task_id", util.UUIDToString(sourceTaskID),
		"is_leader", isLeader,
		"cancelled_pending", cancelledCount,
	)
	return &task, nil
}

// promoteNewestSurvivingComment 修复“原触发评论已被删除”的手动 rerun（FK 清掉 trigger_comment_id 而
// UUID 数组评论计划仍存活）。在入队前提升，让正常入队路径用真实评论重算 originator 与用户级 connected-app
// 能力，而不是带着已删触发器的陈旧安全上下文。
func (s *TaskService) promoteNewestSurvivingComment(ctx context.Context, ids []pgtype.UUID) (pgtype.UUID, []pgtype.UUID, error) {
	type survivingComment struct {
		id        pgtype.UUID
		createdAt time.Time
	}
	survivors := make([]survivingComment, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !id.Valid {
			continue
		}
		key := util.UUIDToString(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		comment, err := s.Queries.GetComment(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return pgtype.UUID{}, nil, err
		}
		survivors = append(survivors, survivingComment{id: comment.ID, createdAt: comment.CreatedAt.Time})
	}
	if len(survivors) == 0 {
		return pgtype.UUID{}, nil, nil
	}
	newest := 0
	for i := 1; i < len(survivors); i++ {
		if survivors[i].createdAt.After(survivors[newest].createdAt) ||
			(survivors[i].createdAt.Equal(survivors[newest].createdAt) &&
				util.UUIDToString(survivors[i].id) > util.UUIDToString(survivors[newest].id)) {
			newest = i
		}
	}
	remaining := make([]pgtype.UUID, 0, len(survivors)-1)
	for i, comment := range survivors {
		if i != newest {
			remaining = append(remaining, comment.id)
		}
	}
	return survivors[newest].id, remaining, nil
}

// enqueueRerunTask 为 issue 上给定 agent 入队全新任务。目标 agent 恰为 issue 的单 agent 指派者时走指派驱动
// 路径（enqueueIssueTask），保持 issue-指派簿记同步；否则（群组成员、已被改派的旧指派者、mention agent）
// 走 mention 路径。
//
// force_fresh_session 刻意在每条 rerun 行钉为 true：它是回滚安全的 legacy 信号——旧认领 handler（滚动
// 部署中）以 !force_fresh_session 门控整个恢复查找，于是全新开始而不是按 (agent, issue) 最近查询恢复
// （该查询可能挑中与用户点击不同的执行）。新认领 handler 对 rerun 忽略此标记，改读确切源任务（rerun_of_task_id）
// 复用其 workdir，并在失败未毒化会话时恢复会话（MUL-4869）。
func (s *TaskService) enqueueRerunTask(ctx context.Context, issue db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID, coalescedCommentIDs []pgtype.UUID, isLeader bool, squadID pgtype.UUID, actorUserID pgtype.UUID, rerunOfTaskID pgtype.UUID) (db.AgentTaskQueue, error) {
	if issue.AssigneeType.String == "agent" && issue.AssigneeID.Valid &&
		util.UUIDToString(issue.AssigneeID) == util.UUIDToString(agentID) {
		return s.enqueueIssueTaskWithCommentPlan(ctx, issue, triggerCommentID, coalescedCommentIDs, true, "", actorUserID, rerunOfTaskID, pgtype.Timestamptz{})
	}
	return s.enqueueMentionTaskWithCommentPlan(ctx, issue, agentID, triggerCommentID, coalescedCommentIDs, isLeader, squadID, true, "", actorUserID, rerunOfTaskID)
}

// 下方这些批量终态写入是清扫器、归档与 daemon 恢复路径——一条语句收尾许多任务。它们放在 TaskService 上而
// 非裸查询，是为了让语句与其委托失败结算共享同一事务。
//
// 这不是风格偏好。HandleFailedTasks 与 CaptureCancelledTasks 在调用方提交【之后】运行，此处发起的结算无法回滚
//——也无法修复，因为 ListPendingDelegatedFailureRecoveries 会排除“覆盖任务已终态且已持回执”的评论。这样的行
// 既不重放也不结算，会永远停在部分索引里，恢复本变更集要消除的无界历史扫描。

// FailTasksForOfflineRuntimes 把“runtime 离线超过重连宽限”的在途任务判为失败。
func (s *TaskService) FailTasksForOfflineRuntimes(ctx context.Context, arg db.FailTasksForOfflineRuntimesParams) ([]db.AgentTaskQueue, error) {
	return s.terminateTasksInTx(ctx, protocol.EventTaskFailed, func(qtx *db.Queries) ([]db.AgentTaskQueue, error) {
		return qtx.FailTasksForOfflineRuntimes(ctx, arg)
	})
}

// FailExpiredRuntimeReconnectRetries 把到达最终重连截止的 deferred 重试判为失败。
func (s *TaskService) FailExpiredRuntimeReconnectRetries(ctx context.Context, arg db.FailExpiredRuntimeReconnectRetriesParams) ([]db.AgentTaskQueue, error) {
	return s.terminateTasksInTx(ctx, protocol.EventTaskFailed, func(qtx *db.Queries) ([]db.AgentTaskQueue, error) {
		return qtx.FailExpiredRuntimeReconnectRetries(ctx, arg)
	})
}

// FailStaleTasks 把“runtime 停止上报”的已认领工作判为失败。
func (s *TaskService) FailStaleTasks(ctx context.Context, arg db.FailStaleTasksParams) ([]db.AgentTaskQueue, error) {
	return s.terminateTasksInTx(ctx, protocol.EventTaskFailed, func(qtx *db.Queries) ([]db.AgentTaskQueue, error) {
		return qtx.FailStaleTasks(ctx, arg)
	})
}

// ExpireStaleQueuedTasks 把“runtime 永未回归”的排队工作判为失败。
func (s *TaskService) ExpireStaleQueuedTasks(ctx context.Context, arg db.ExpireStaleQueuedTasksParams) ([]db.AgentTaskQueue, error) {
	return s.terminateTasksInTx(ctx, protocol.EventTaskFailed, func(qtx *db.Queries) ([]db.AgentTaskQueue, error) {
		return qtx.ExpireStaleQueuedTasks(ctx, arg)
	})
}

// RecoverOrphanedTasksForRuntime 把重启的 daemon 报告丢失的工作判为失败。
func (s *TaskService) RecoverOrphanedTasksForRuntime(ctx context.Context, runtimeID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	return s.terminateTasksInTx(ctx, protocol.EventTaskFailed, func(qtx *db.Queries) ([]db.AgentTaskQueue, error) {
		return qtx.RecoverOrphanedTasksForRuntime(ctx, runtimeID)
	})
}

// CancelTasksForArchivedAgent 取消正在归档的 agent 的全部活跃任务，并在同一事务里结算其恢复回执。
//
// 与 CancelTasksForAgent 不同，它不为每任务发 task:cancelled：调用方发布的 agent:archived 事件已使每个客户端
// 的活跃任务视图失效，逐行事件只是冗余噪音。
func (s *TaskService) CancelTasksForArchivedAgent(ctx context.Context, agentID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	return s.terminateTasksInTx(ctx, protocol.EventTaskCancelled, func(qtx *db.Queries) ([]db.AgentTaskQueue, error) {
		return qtx.CancelAgentTasksByAgent(ctx, agentID)
	})
}

// closeUpstreamRuns 在任务终态后关闭它名下的上游同步批次。
//
// 修改 2. 任务被取消或失败后，批次如果还挂着，唯一的活动批次索引会让下一次同步一直撞
// ErrBusy 直到租约自然过期。这里只记日志不返回错误：任务终态已经提交，而批次也能在下次
// start 时被回收，同步台账的问题不能反过来影响任务终态。
func (s *TaskService) closeUpstreamRuns(ctx context.Context, task db.AgentTaskQueue, status, message string) {
	if s.UpstreamRuns == nil || !task.ID.Valid {
		return
	}
	if _, err := s.UpstreamRuns.CloseTaskRuns(ctx, util.UUIDToString(task.ID), status, message); err != nil {
		slog.Warn("close upstream sync run failed",
			"task_id", util.UUIDToString(task.ID), "run_status", status, "error", err)
	}
}

func (s *TaskService) terminateTasksInTx(ctx context.Context, eventType string, fail func(*db.Queries) ([]db.AgentTaskQueue, error)) ([]db.AgentTaskQueue, error) {
	var failed []db.AgentTaskQueue
	var persisted []events.Event
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		failed, err = fail(qtx)
		if err != nil {
			return err
		}
		if err := SettleDeliveredDelegatedFailureRecoveries(ctx, qtx, failed...); err != nil {
			return err
		}
		// 清扫器变更与 API 回调一样是真实的终态转换。在提交前记录，使崩溃不会让客户端与投影永远不知道这次转换。
		if s.TxStarter != nil && len(failed) > 0 {
			persisted, err = enqueueTaskTerminalEvents(ctx, qtx, eventType, failed)
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if s.TxStarter != nil {
		for _, event := range persisted {
			publishCommittedEvent(s.Bus, event)
		}
	}
	// 修改 3. 清扫器、归档取消和离线回收都从这里落终态，统一收敛它们名下的上游同步批次。
	runStatus, runMessage := "failed", "任务失败，批次已关闭"
	if eventType == protocol.EventTaskCancelled {
		runStatus, runMessage = "interrupted", "任务已取消，批次已关闭"
	}
	for _, task := range failed {
		s.closeUpstreamRuns(ctx, task, runStatus, runMessage)
	}
	return failed, nil
}

func enqueueTaskTerminalEvents(ctx context.Context, queries *db.Queries, eventType string, tasks []db.AgentTaskQueue) ([]events.Event, error) {
	if !taskTerminalEventType(eventType) {
		return nil, fmt.Errorf("unsupported task terminal event %q", eventType)
	}
	persisted := make([]events.Event, 0, len(tasks))
	for _, task := range tasks {
		event, err := recordTaskTerminalEventTx(ctx, queries, eventType, task)
		if err != nil {
			return nil, err
		}
		persisted = append(persisted, event)
	}
	return persisted, nil
}

// HandleFailedTasks 为一批复新失败的任务执行失败后副作用：可选自动重试、task:failed 广播、agent 状态调和，
// 以及（当 issue 无剩余活跃任务且未被重试时）把 issue 重置回 todo，让 daemon 能再次拾起。
//
// 清扫器和 recover-orphans 共用本入口；FailTaskWithTransition 负责单次执行失败的副作用。
func (s *TaskService) HandleFailedTasks(ctx context.Context, tasks []db.AgentTaskQueue) int {
	if len(tasks) == 0 {
		return 0
	}

	affectedAgents := make(map[string]pgtype.UUID)
	processedIssues := make(map[string]bool)
	retriedIssues := make(map[string]bool)
	retried := 0

	for _, t := range tasks {
		// 先自动重试，让 issue 保持 in_progress，而不是在一个 tick 里反复 todo → in_progress。
		retryPending := false
		if child, _ := s.MaybeRetryFailedTask(ctx, t); child != nil {
			retryPending = true
			retried++
			if t.IssueID.Valid {
				retriedIssues[util.UUIDToString(t.IssueID)] = true
			}
		}
		if !retryPending {
			if _, err := s.recoverDelegatedTaskFailure(ctx, t); err != nil {
				slog.Warn("handle failed tasks: delegated failure recovery failed",
					"task_id", util.UUIDToString(t.ID),
					"delegated_from_task_id", util.UUIDToString(t.DelegatedFromTaskID),
					"error", err,
				)
			}
			if t.IssueID.Valid && t.Error.String != "" {
				s.createAgentComment(ctx, t.IssueID, t.AgentID, redact.Text(t.Error.String), "system", t.TriggerCommentID, t.ID)
			}
		}

		failureReason := "agent_error"
		if t.FailureReason.Valid && t.FailureReason.String != "" {
			failureReason = t.FailureReason.String
		}
		s.captureTaskFailed(ctx, t)

		workspaceID := ""
		if t.IssueID.Valid {
			if issue, err := s.Queries.GetIssue(ctx, t.IssueID); err == nil {
				workspaceID = util.UUIDToString(issue.WorkspaceID)
				// 只有当该 issue 没有其它活跃任务、且刚未入队重试时，才重置卡住的 in_progress issue。
				issueKey := util.UUIDToString(t.IssueID)
				// 只有 “agent 在积极工作” 才重置。in_review 与 blocked 排除——那时 issue 归人类或外部依赖所有——
				// 自定义状态解析为其继承的规范状态，因此自定义 review 门与 In Review 一样被排除（MUL-6243）。
				effectiveStatus := issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, issue.Status)
				if effectiveStatus == "in_progress" && !processedIssues[issueKey] && !retriedIssues[issueKey] {
					processedIssues[issueKey] = true
					resetEvent, reset, resetErr := s.resetIssueAfterTaskFailure(ctx, t.IssueID, issue)
					if resetErr != nil {
						slog.Warn("handle failed tasks: reset stuck issue failed",
							"issue_id", issueKey,
							"error", resetErr,
						)
					} else if reset {
						// issue 变更与其事件是一个提交。实时监听者在事务 durable 后才收到事件，重启不会让看板陈旧。
						publishCommittedEvent(s.Bus, resetEvent)
					}
				}
			}
		}
		if workspaceID == "" {
			workspaceID = s.ResolveTaskWorkspaceID(ctx, t)
		}

		// 批量清扫器路径在“改变行”的同一事务里记录终态事件。当它们的提交后调和到达本方法时不再发第二份进程内
		// 副本；没有事务启动器的 legacy 测试服务保留直发总线行为。
		if s.TxStarter == nil || !hasPersistedTaskTerminalEvent(ctx, s.Queries, protocol.EventTaskFailed, t) {
			s.publishTaskFailedEvent(workspaceID, t, t.Error.String, failureReason, retryPending)
		}

		affectedAgents[util.UUIDToString(t.AgentID)] = t.AgentID
	}

	for _, agentID := range affectedAgents {
		s.ReconcileAgentStatus(ctx, agentID)
	}
	s.notifyTasksFinished(tasks)
	return retried
}

const (
	delegatedFailureErrorSummaryRunes       = 800
	delegatedFailureRecoveryMaxTaskAttempts = 3
	delegatedFailureRecoveryCommentType     = "progress_update"
)

// SettleDeliveredDelegatedFailureRecoveries 退役给定已终态任务实际收到的每一条委托失败恢复评论，使这些评论
// 移出 idx_comment_delegated_failure_unsettled，而不是每个清扫 tick 都被 ListPendingDelegatedFailureRecoveries
// 重新证明已结算。没有它，outbox 扫描即便查不到东西也会随总历史增长。
//
// 不变量：任何把任务移到终态的路径都必须与终态写入【同一个 qtx】到达这里——逐任务写入与批量取消皆然——使
// 标记与状态变更要么原子提交、要么完全不提交。被“已提交但未结算”的终态写入搁浅的行以后无法修复：
// ListPendingDelegatedFailureRecoveries 会排除覆盖任务已终态且持回执的评论，于是无人重放结算、也无人标记它，
// 索引重新获得它本要消除的无界增长。
//
// 同理，提交后助手——BroadcastCancelledTasks、CaptureCancelledTasks、HandleFailedTasks——绝不可调用本函数：它们
// 在调用方已提交后运行，此处的失败既不能回滚也无法补偿。不要重新引入事务外的 best-effort 结算；旧的已被删除，
// 不是保留。
//
// 只在任务已终态后调用。dispatched 任务的回执仍可替换（SetTaskDeliveredCommentIDs），提前结算会把合法的回收
// 窗口冻结成永久丢失的恢复。SettleDelegatedFailureRecoveriesForTask 在 SQL 中重查终态，误打的早调用更新不到
// 任何行，而不是丢掉恢复。
//
// 没有投递回执的任务（几乎每个任务都是）只付出一次切片长度检查，零查询。
func SettleDeliveredDelegatedFailureRecoveries(ctx context.Context, q *db.Queries, tasks ...db.AgentTaskQueue) error {
	for _, t := range tasks {
		if len(t.DeliveredCommentIds) == 0 {
			continue
		}
		if _, err := q.SettleDelegatedFailureRecoveriesForTask(ctx, t.ID); err != nil {
			return fmt.Errorf("settle delegated failure recoveries for task %s: %w", util.UUIDToString(t.ID), err)
		}
	}
	return nil
}

type delegatedFailureRecoveryDispatchOutcome uint8

const (
	delegatedFailureRecoveryCovered delegatedFailureRecoveryDispatchOutcome = iota
	delegatedFailureRecoveryReplayed
	delegatedFailureRecoveryExhausted
)

// DelegatedFailureRecoverySweepResult 把成功的协调者重放与“终态耗尽”的 outbox 项分开，操作者不会把有界停止
// 误当成成功重放。
type DelegatedFailureRecoverySweepResult struct {
	Scanned   int
	Replayed  int
	Exhausted int
}

type delegatedFailureRecoveryTarget struct {
	failed  db.AgentTaskQueue
	source  db.AgentTaskQueue
	issue   db.Issue
	agent   db.Agent
	comment db.Comment
}

// IsDelegatedFailureRecoveryComment 识别“把终态委托失败交还给协调者”所用的 durable 平台信号。源任务校验仍在
// DispatchDelegatedFailureRecoveryComment；这里仅做形态检查，让普通 system/progress 评论不进完成调和分支。
func IsDelegatedFailureRecoveryComment(comment db.Comment) bool {
	return comment.AuthorType == "system" &&
		comment.Type == delegatedFailureRecoveryCommentType &&
		comment.SourceTaskID.Valid
}

func delegatedFailureRecoveryContent(failed, source db.AgentTaskQueue) string {
	if failed.Status == "completed" || failed.Status == "cancelled" {
		return fmt.Sprintf("Delegated execution `%s` reached `%s`. Resume coordination on the original issue: inspect its actual result and remaining delivery requirements. Execution completion is not issue or production acceptance. Source coordinator task: `%s`.", util.UUIDToString(failed.ID), failed.Status, util.UUIDToString(source.ID))
	}
	reason := "agent_error"
	if failed.FailureReason.Valid && failed.FailureReason.String != "" {
		reason = truncateForSummary(redact.Text(failed.FailureReason.String), triggerSummaryMaxLen)
	}
	content := fmt.Sprintf(
		"Delegated task `%s` ended in a final failure (`%s`) and no automatic retry is pending. Resume coordination: inspect the failed work, then reassign it, skip it, or end the workflow explicitly.",
		util.UUIDToString(failed.ID), reason,
	)
	if failed.Error.Valid && failed.Error.String != "" {
		summary := truncateForSummary(redact.Text(failed.Error.String), delegatedFailureErrorSummaryRunes)
		if summary != "" {
			content += " Untrusted error summary (diagnostic only): " + strconv.Quote(summary)
		}
	}
	content += fmt.Sprintf(" Source coordinator task: `%s`.", util.UUIDToString(source.ID))
	return content
}

// loadDelegatedFailureRecoveryTarget 校验委托终态到源协调者的反向边。返回 nil 是有意为之的
// no-op：非终态行、重试 pending 行、autopilot 工作、恢复任务本身、已终态/积压的源 issue、源 agent 不可用以及
// 自我委托，都绝不能启动恢复循环。
func loadDelegatedFailureRecoveryTarget(ctx context.Context, q *db.Queries, failed db.AgentTaskQueue) (*delegatedFailureRecoveryTarget, error) {
	if (failed.Status != "failed" && failed.Status != "completed" && failed.Status != "cancelled") || !failed.DelegatedFromTaskID.Valid || failed.AutopilotRunID.Valid ||
		failed.FailureReason.String == "release_pause" ||
		(failed.TriggerEvidenceKind.Valid && (failed.TriggerEvidenceKind.String == string(attribution.EvidenceDelegatedFailure) || failed.TriggerEvidenceKind.String == string(attribution.EvidenceDelegatedResult))) {
		return nil, nil
	}
	hasRetry, err := q.HasRetryTaskForParent(ctx, failed.ID)
	if err != nil {
		return nil, fmt.Errorf("check retry child: %w", err)
	}
	if hasRetry {
		return nil, nil
	}
	source, err := q.GetAgentTask(ctx, failed.DelegatedFromTaskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load source task: %w", err)
	}
	if source.AutopilotRunID.Valid || !source.IssueID.Valid || source.AgentID == failed.AgentID {
		return nil, nil
	}
	issue, err := q.GetIssue(ctx, source.IssueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load source issue: %w", err)
	}
	effectiveStatus := issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status)
	if effectiveStatus == issuestatus.Done || effectiveStatus == issuestatus.Cancelled || effectiveStatus == issuestatus.Backlog {
		return nil, nil
	}
	agent, err := q.GetAgent(ctx, source.AgentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load source agent: %w", err)
	}
	if agent.ArchivedAt.Valid || !agent.RuntimeID.Valid || agent.WorkspaceID != issue.WorkspaceID {
		return nil, nil
	}
	if covered, err := delegatedIssueBarrierCoversResult(ctx, q, failed, issue, agent); err != nil || covered {
		return nil, err
	}
	return &delegatedFailureRecoveryTarget{failed: failed, source: source, issue: issue, agent: agent}, nil
}

// ensureDelegatedFailureRecoveryComment 为每条失败任务创建一条 durable 恢复信号。失败行的锁串行化 FailTask 与
// 清扫器调用方；评论本身由平台撰写，不产生订阅者或 mention 通知副作用。
func (s *TaskService) ensureDelegatedFailureRecoveryComment(ctx context.Context, failedID pgtype.UUID) (*delegatedFailureRecoveryTarget, bool, error) {
	var target *delegatedFailureRecoveryTarget
	created := false
	var createdEvent events.Event
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		failed, err := qtx.GetAgentTaskForDelegatedFailureUpdate(ctx, failedID)
		if err != nil {
			return fmt.Errorf("lock failed task: %w", err)
		}
		target, err = loadDelegatedFailureRecoveryTarget(ctx, qtx, failed)
		if err != nil || target == nil {
			return err
		}
		comment, err := qtx.GetDelegatedFailureRecoveryComment(ctx, db.GetDelegatedFailureRecoveryCommentParams{
			IssueID:      target.issue.ID,
			WorkspaceID:  target.issue.WorkspaceID,
			SourceTaskID: failed.ID,
		})
		if err == nil {
			target.comment = comment
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("find recovery comment: %w", err)
		}
		createdComment, err := qtx.CreateComment(ctx, db.CreateCommentParams{
			ID:           dbid.NewV7(),
			IssueID:      target.issue.ID,
			WorkspaceID:  target.issue.WorkspaceID,
			AuthorType:   "system",
			AuthorID:     pgtype.UUID{Valid: true},
			Content:      delegatedFailureRecoveryContent(target.failed, target.source),
			Type:         delegatedFailureRecoveryCommentType,
			ParentID:     target.source.TriggerCommentID,
			SourceTaskID: failed.ID,
		})
		if err != nil {
			return fmt.Errorf("create recovery comment: %w", err)
		}
		target.comment = createdComment.Comment()
		created = true
		createdEvent, err = recordDurableEventTx(ctx, qtx, events.Event{
			Type:           protocol.EventCommentCreated,
			IdempotencyKey: "comment:created:" + util.UUIDToString(createdComment.ID),
			StreamKey:      "issue:" + util.UUIDToString(target.issue.ID),
			WorkspaceID:    util.UUIDToString(target.issue.WorkspaceID),
			ActorType:      "system",
			Payload: map[string]any{
				"comment":      commentEventFields(createdComment.Comment()),
				"issue_title":  target.issue.Title,
				"issue_status": target.issue.Status,
			},
		})
		if err != nil {
			return fmt.Errorf("record recovery comment event: %w", err)
		}
		return nil
	}); err != nil {
		return nil, false, err
	}
	if target == nil {
		return nil, false, nil
	}
	if created && createdEvent.ID != "" {
		publishCommittedEvent(s.Bus, createdEvent)
	}
	return target, created, nil
}

// delegatedFailureRecoveryExhaustionContent 生成自动恢复耗尽时的英文说明文本（面向 issue 读者，保留英文）。
func delegatedFailureRecoveryExhaustionContent(target *delegatedFailureRecoveryTarget) string {
	return fmt.Sprintf(
		"Automatic recovery for delegated task `%s` stopped after %d coordinator tasks ended before receiving the recovery signal. No more recovery tasks will be created automatically; resume or dismiss the work manually. Source coordinator task: `%s`.",
		util.UUIDToString(target.failed.ID),
		delegatedFailureRecoveryMaxTaskAttempts,
		util.UUIDToString(target.source.ID),
	)
}

// delegatedFailureRecoveryAttribution 从失败任务（优先）与其源任务回退解析恢复说明的发起人/负责人：有发起人则
// 负责人取发起人；都没有则用源任务的归因。
func delegatedFailureRecoveryAttribution(target *delegatedFailureRecoveryTarget) (pgtype.UUID, pgtype.UUID) {
	originator := target.failed.OriginatorUserID
	accountable := target.failed.AccountableUserID
	if originator.Valid {
		accountable = originator
	}
	if !originator.Valid && !accountable.Valid {
		originator = target.source.OriginatorUserID
		accountable = target.source.AccountableUserID
		if originator.Valid {
			accountable = originator
		}
	}
	return originator, accountable
}

// exhaustDelegatedFailureRecovery 在受控的自动尝试用尽后原子地结算恢复 outbox、创建一条可见的系统解释评论，
// 并通知负责人类。返回值表示本调用方是否创建了该终态结果。先更新最新一次尝试可串行化并发清扫器；第二个调用方
// 随后观察到第一个已写的解释，不会再次上报耗尽。
func (s *TaskService) exhaustDelegatedFailureRecovery(ctx context.Context, target *delegatedFailureRecoveryTarget) (bool, error) {
	var exhaustedComment db.Comment
	var exhaustedInbox db.InboxItem
	var commentEvent events.Event
	var inboxEvent events.Event
	created := false
	inboxCreated := false
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		if _, err := qtx.AcknowledgeExhaustedDelegatedFailureRecovery(ctx, db.AcknowledgeExhaustedDelegatedFailureRecoveryParams{
			CommentID:    target.comment.ID,
			FailedTaskID: target.failed.ID,
			MaxAttempts:  delegatedFailureRecoveryMaxTaskAttempts,
		}); err != nil {
			return fmt.Errorf("acknowledge exhausted delegated failure recovery: %w", err)
		}

		// 上面的回执落在最新一次尝试的行上，该行可能仍在运行，任务维度的结算看不到它。耗尽按其自身条件即是终态
		//——尝试预算已用尽，下方可见的解释告知用户为何不再运行——因此直接在同一事务内退役该评论。
		if _, err := qtx.SettleDelegatedFailureRecoveryComment(ctx, target.comment.ID); err != nil {
			return fmt.Errorf("settle exhausted delegated failure recovery: %w", err)
		}

		comment, err := qtx.GetDelegatedFailureRecoveryExhaustionComment(ctx, db.GetDelegatedFailureRecoveryExhaustionCommentParams{
			IssueID:      target.issue.ID,
			WorkspaceID:  target.issue.WorkspaceID,
			SourceTaskID: target.failed.ID,
		})
		if err == nil {
			exhaustedComment = comment
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("find delegated failure exhaustion comment: %w", err)
		}

		createdComment, err := qtx.CreateComment(ctx, db.CreateCommentParams{
			ID:           dbid.NewV7(),
			IssueID:      target.issue.ID,
			WorkspaceID:  target.issue.WorkspaceID,
			AuthorType:   "system",
			AuthorID:     pgtype.UUID{Valid: true},
			Content:      delegatedFailureRecoveryExhaustionContent(target),
			Type:         "system",
			ParentID:     target.source.TriggerCommentID,
			SourceTaskID: target.failed.ID,
		})
		if err != nil {
			return fmt.Errorf("create delegated failure exhaustion comment: %w", err)
		}
		exhaustedComment = createdComment.Comment()
		created = true
		commentEvent, err = recordDurableEventTx(ctx, qtx, events.Event{
			Type:           protocol.EventCommentCreated,
			IdempotencyKey: "comment:created:" + util.UUIDToString(createdComment.ID),
			StreamKey:      "issue:" + util.UUIDToString(target.issue.ID),
			WorkspaceID:    util.UUIDToString(target.issue.WorkspaceID),
			ActorType:      "system",
			Payload: map[string]any{
				"comment":      commentEventFields(createdComment.Comment()),
				"issue_title":  target.issue.Title,
				"issue_status": target.issue.Status,
			},
		})
		if err != nil {
			return fmt.Errorf("record delegated exhaustion comment event: %w", err)
		}

		// 耗尽结算刻意不 @mention 协调者 agent：那样会入队第四次恢复运行，破坏尝试上限。改为给“发起（或对）委托
		// 工作负责”的人类创建一条 durable 的 action-required 收件箱项。
		_, recipient := delegatedFailureRecoveryAttribution(target)
		if recipient.Valid {
			_, err = qtx.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
				UserID:      recipient,
				WorkspaceID: target.issue.WorkspaceID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("validate delegated failure exhaustion recipient: %w", err)
			}

			details, err := json.Marshal(map[string]any{
				"failed_task_id":       util.UUIDToString(target.failed.ID),
				"source_task_id":       util.UUIDToString(target.source.ID),
				"coordinator_agent_id": util.UUIDToString(target.agent.ID),
				"max_attempts":         delegatedFailureRecoveryMaxTaskAttempts,
			})
			if err != nil {
				return fmt.Errorf("encode delegated failure exhaustion details: %w", err)
			}
			exhaustedInbox, err = qtx.CreateInboxItem(ctx, db.CreateInboxItemParams{
				ID:            dbid.NewV7(),
				WorkspaceID:   target.issue.WorkspaceID,
				RecipientType: "member",
				RecipientID:   recipient,
				Type:          "task_failed",
				Severity:      "action_required",
				IssueID:       target.issue.ID,
				Title:         target.issue.Title,
				Body:          pgtype.Text{String: exhaustedComment.Content, Valid: true},
				ActorType:     pgtype.Text{String: "system", Valid: true},
				ActorID:       pgtype.UUID{},
				Details:       details,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("create delegated failure exhaustion inbox item: %w", err)
			}
			inboxCreated = true
			inboxEvent, err = recordDurableEventTx(ctx, qtx, events.Event{
				Type:           protocol.EventInboxNew,
				IdempotencyKey: "inbox:new:" + util.UUIDToString(exhaustedInbox.ID),
				StreamKey:      "inbox:" + util.UUIDToString(recipient),
				WorkspaceID:    util.UUIDToString(target.issue.WorkspaceID),
				ActorType:      "system",
				Payload: map[string]any{
					"item": func() map[string]any {
						item := InboxItemFields(exhaustedInbox)
						item["issue_status"] = target.issue.Status
						return item
					}(),
				},
			})
			if err != nil {
				return fmt.Errorf("record delegated exhaustion inbox event: %w", err)
			}
		}
		return nil
	}); err != nil {
		return false, err
	}

	if created && commentEvent.ID != "" {
		publishCommittedEvent(s.Bus, commentEvent)
	}
	if inboxCreated && inboxEvent.ID != "" {
		publishCommittedEvent(s.Bus, inboxEvent)
	}
	return created, nil
}

// dispatchDelegatedFailureRecovery 把恢复评论路由给源协调者，而不依赖通用 mention 解析：有预认领任务就并入它；
// 否则创建一条专用 queued 后继。唯一同时阻断两种写入的状态是 dispatched 任务（认领载荷已构建、pending-task
// 唯一槽位仍被持有），该窄竞态下把评论登记为“已计划但未投递”，让完成调和来安排后续。三轮循环围住这些写入
// 闭合状态变化。
func (s *TaskService) dispatchDelegatedFailureRecovery(ctx context.Context, target *delegatedFailureRecoveryTarget, excludeTaskID pgtype.UUID) (delegatedFailureRecoveryDispatchOutcome, error) {
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		covered, err := s.Queries.HasTaskCoveringDelegatedFailureComment(ctx, db.HasTaskCoveringDelegatedFailureCommentParams{
			IssueID:       target.issue.ID,
			AgentID:       target.agent.ID,
			CommentID:     target.comment.ID,
			ExcludeTaskID: excludeTaskID,
		})
		if err != nil {
			return delegatedFailureRecoveryCovered, fmt.Errorf("check recovery coverage: %w", err)
		}
		if covered {
			return delegatedFailureRecoveryCovered, nil
		}

		recoveryTasks, err := s.Queries.CountDelegatedFailureRecoveryTasks(ctx, target.failed.ID)
		if err != nil {
			return delegatedFailureRecoveryCovered, fmt.Errorf("count delegated failure recovery tasks: %w", err)
		}
		if recoveryTasks >= delegatedFailureRecoveryMaxTaskAttempts {
			exhausted, err := s.exhaustDelegatedFailureRecovery(ctx, target)
			if err != nil {
				return delegatedFailureRecoveryCovered, err
			}
			if exhausted {
				return delegatedFailureRecoveryExhausted, nil
			}
			return delegatedFailureRecoveryCovered, nil
		}

		if merged, err := s.Queries.MergeDelegatedFailureCommentIntoPendingTask(ctx, db.MergeDelegatedFailureCommentIntoPendingTaskParams{
			CommentID:      target.comment.ID,
			TriggerSummary: s.buildCommentTriggerSummary(ctx, target.issue.WorkspaceID, target.comment.ID),
			IssueID:        target.issue.ID,
			AgentID:        target.agent.ID,
		}); err == nil {
			slog.Info("delegated failure recovery merged into pending coordinator task",
				"failed_task_id", util.UUIDToString(target.failed.ID),
				"coordinator_task_id", util.UUIDToString(merged.ID),
			)
			return delegatedFailureRecoveryReplayed, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return delegatedFailureRecoveryCovered, fmt.Errorf("merge recovery into pending task: %w", err)
		}

		originator, accountable := delegatedFailureRecoveryAttribution(target)
		source := attribution.SourceDelegation
		if !originator.Valid && !accountable.Valid {
			source = attribution.SourceUnattributed
		}
		ruleVersionID := target.failed.RuleVersionID
		if !ruleVersionID.Valid {
			ruleVersionID = target.source.RuleVersionID
		}
		overlay := s.buildRuntimeMCPOverlay(ctx, originator, target.agent)
		var task db.AgentTaskQueue
		var queuedEvent events.Event
		err = s.runInTx(ctx, func(qtx *db.Queries) error {
			var createErr error
			task, createErr = qtx.CreateAgentTask(ctx, db.CreateAgentTaskParams{
				ID:                   dbid.NewV7(),
				AgentID:              target.agent.ID,
				RuntimeID:            target.agent.RuntimeID,
				IssueID:              target.issue.ID,
				Priority:             priorityToInt(target.issue.Priority),
				TriggerCommentID:     target.comment.ID,
				TriggerSummary:       s.buildCommentTriggerSummary(ctx, target.issue.WorkspaceID, target.comment.ID),
				IsLeaderTask:         pgtype.Bool{Bool: target.source.IsLeaderTask, Valid: target.source.IsLeaderTask},
				SquadID:              target.source.SquadID,
				OriginatorUserID:     originator,
				AccountableUserID:    accountable,
				RuntimeMcpOverlay:    overlay.Overlay,
				RuntimeConnectedApps: overlay.ConnectedApps,
				OriginatorSource:     pgtype.Text{String: string(source), Valid: true},
				DelegatedFromTaskID:  target.failed.ID,
				RuleVersionID:        ruleVersionID,
				TriggerEvidenceKind:  pgtype.Text{String: string(delegatedRecoveryEvidenceKind(target.failed)), Valid: true},
				TriggerEvidenceRefID: target.failed.ID,
				HeadSha:              headShaText(s.ResolveIssueReviewSHA(ctx, target.issue.ID)),
			})
			if createErr != nil {
				return createErr
			}
			queuedEvent, createErr = RecordTaskQueuedEventTx(ctx, qtx, util.UUIDToString(target.issue.WorkspaceID), task)
			return createErr
		})
		if err == nil {
			slog.Info("delegated failure recovery task enqueued",
				"failed_task_id", util.UUIDToString(target.failed.ID),
				"source_task_id", util.UUIDToString(target.source.ID),
				"recovery_task_id", util.UUIDToString(task.ID),
				"coordinator_agent_id", util.UUIDToString(target.agent.ID),
			)
			publishCommittedEvent(s.Bus, queuedEvent)
			s.NotifyTaskEnqueued(ctx, task)
			return delegatedFailureRecoveryReplayed, nil
		}
		if !isDuplicatePendingTaskErr(err) {
			return delegatedFailureRecoveryCovered, fmt.Errorf("create recovery task: %w", err)
		}

		// dispatched 任务仍握着唯一 queued/dispatched 槽位，但其认领载荷不可变。把评论登记为未投递，让完成调和
		// 创建后继者。running 任务不持有该槽位，它们走上面的 durable queued-后继路径。
		if active, err := s.Queries.RegisterPlannedCommentForActiveTask(ctx, db.RegisterPlannedCommentForActiveTaskParams{
			CommentID: target.comment.ID,
			IssueID:   target.issue.ID,
			AgentID:   target.agent.ID,
		}); err == nil {
			slog.Info("delegated failure recovery registered behind dispatched coordinator task",
				"failed_task_id", util.UUIDToString(target.failed.ID),
				"coordinator_task_id", util.UUIDToString(active.ID),
			)
			return delegatedFailureRecoveryReplayed, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return delegatedFailureRecoveryCovered, fmt.Errorf("register recovery on dispatched task: %w", err)
		}
	}
	return delegatedFailureRecoveryCovered, fmt.Errorf("delegate failure recovery could not acquire coordinator task slot")
}

// recoverDelegatedTaskFailure 是 FailTask 与 HandleFailedTasks 共享的终态后钩子。handled 表示该失败是否为
// 合格的可恢复委托终态；生产终态路径刻意在更丰富的协调者恢复信号之外保留 legacy 的原始错误通知。
func (s *TaskService) recoverDelegatedTaskFailure(ctx context.Context, failed db.AgentTaskQueue) (handled bool, err error) {
	target, _, err := s.ensureDelegatedFailureRecoveryComment(ctx, failed.ID)
	if err != nil || target == nil {
		return false, err
	}
	_, err = s.dispatchDelegatedFailureRecovery(ctx, target, pgtype.UUID{})
	return true, err
}

// RecoverPendingDelegatedFailures 重放 durable 的恢复 outbox。平台恢复评论即义务：只有当携带它执行的任务仍能
// 运行、或某任务把它记入 delivered_comment_ids 后，义务才算完成。这让稍后的清扫器能修复“评论创建与协调者
// 派发之间进程崩溃或瞬时数据库错误”，而不产生重复的可运行任务。
func (s *TaskService) RecoverPendingDelegatedFailures(ctx context.Context, maxPerTick int32) (DelegatedFailureRecoverySweepResult, error) {
	result := DelegatedFailureRecoverySweepResult{}
	if maxPerTick <= 0 {
		return result, nil
	}
	pending, err := s.Queries.ListPendingDelegatedFailureRecoveries(ctx, maxPerTick)
	if err != nil {
		return result, fmt.Errorf("list pending delegated failure recoveries: %w", err)
	}
	result.Scanned = len(pending)

	errs := make([]error, 0)
	for _, comment := range pending {
		outcome, recoveryErr := s.dispatchDelegatedFailureRecoveryComment(ctx, comment, pgtype.UUID{})
		if recoveryErr != nil {
			errs = append(errs, fmt.Errorf("dispatch recovery comment %s: %w", util.UUIDToString(comment.ID), recoveryErr))
			continue
		}
		switch outcome {
		case delegatedFailureRecoveryReplayed:
			result.Replayed++
		case delegatedFailureRecoveryExhausted:
			result.Exhausted++
		}
	}
	return result, errors.Join(errs...)
}

// DispatchDelegatedFailureRecoveryComment 供完成调和在“恢复信号晚于协调者任务被认领”时使用。已完成的任务因
// 该评论是计划给它的、但并未投递而排除在覆盖检查外；路由随后合并/入队恰好一条后续。
func (s *TaskService) DispatchDelegatedFailureRecoveryComment(ctx context.Context, comment db.Comment, completedTaskID pgtype.UUID) error {
	_, err := s.dispatchDelegatedFailureRecoveryComment(ctx, comment, completedTaskID)
	return err
}

// dispatchDelegatedFailureRecoveryComment 是 DispatchDelegatedFailureRecoveryComment 的内部实现：先校验评论
// 确是委托失败恢复评论并加载失败任务，再构建目标并派发。作用域不匹配时报错。
func (s *TaskService) dispatchDelegatedFailureRecoveryComment(ctx context.Context, comment db.Comment, completedTaskID pgtype.UUID) (delegatedFailureRecoveryDispatchOutcome, error) {
	if !IsDelegatedFailureRecoveryComment(comment) {
		return delegatedFailureRecoveryCovered, nil
	}
	failed, err := s.Queries.GetAgentTask(ctx, comment.SourceTaskID)
	if err != nil {
		return delegatedFailureRecoveryCovered, fmt.Errorf("load failed recovery source: %w", err)
	}
	target, err := loadDelegatedFailureRecoveryTarget(ctx, s.Queries, failed)
	if err != nil || target == nil {
		return delegatedFailureRecoveryCovered, err
	}
	if target.issue.ID != comment.IssueID || target.issue.WorkspaceID != comment.WorkspaceID {
		return delegatedFailureRecoveryCovered, fmt.Errorf("delegated failure recovery comment scope mismatch")
	}
	target.comment = comment
	return s.dispatchDelegatedFailureRecovery(ctx, target, completedTaskID)
}

// runInTx 在单个 DB 事务内执行 fn。TxStarter 为 nil（例如部分测试直接构造 TaskService）时，fn 直接跑在
// 普通 Queries 句柄上、无事务保证。
func (s *TaskService) runInTx(ctx context.Context, fn func(*db.Queries) error) error {
	if s.TxStarter == nil {
		return fn(s.Queries)
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReportProgress 经事件总线广播一次进度更新（task:progress）。
func (s *TaskService) ReportProgress(ctx context.Context, taskID string, workspaceID string, summary string, step, total int) {
	s.Bus.Publish(events.Event{
		Type:        protocol.EventTaskProgress,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		ActorID:     "",
		TaskID:      taskID,
		Payload: protocol.TaskProgressPayload{
			TaskID:  taskID,
			Summary: summary,
			Step:    step,
			Total:   total,
		},
	})
}

// ReconcileAgentStatus 依据当前工作任务集刷新 agent 状态。状态已正确时查询返回空行，避免改写 updated_at
// 或广播零信息事件。
func (s *TaskService) ReconcileAgentStatus(ctx context.Context, agentID pgtype.UUID) {
	var agent db.Agent
	var statusEvent events.Event
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		agent, err = qtx.RefreshAgentStatusFromTasks(ctx, agentID)
		if err != nil {
			return err
		}
		if s.TxStarter != nil {
			statusEvent, err = recordAgentStatusEventTx(ctx, qtx, agent)
			if err != nil {
				return fmt.Errorf("record agent status event: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return
	}
	slog.Debug("agent status reconciled", "agent_id", util.UUIDToString(agentID), "status", agent.Status)
	if statusEvent.ID != "" {
		publishCommittedEvent(s.Bus, statusEvent)
	} else {
		s.publishAgentStatus(agent)
	}
}

// updateAgentStatus 直接把 agent 状态写为给定值（事务内）并广播状态事件；用于显式状态更新路径。
func (s *TaskService) updateAgentStatus(ctx context.Context, agentID pgtype.UUID, status string) {
	var agent db.Agent
	var statusEvent events.Event
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		agent, err = qtx.UpdateAgentStatus(ctx, db.UpdateAgentStatusParams{
			ID:     agentID,
			Status: status,
		})
		if err != nil {
			return err
		}
		if s.TxStarter != nil {
			statusEvent, err = recordAgentStatusEventTx(ctx, qtx, agent)
			if err != nil {
				return fmt.Errorf("record agent status event: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return
	}
	if statusEvent.ID != "" {
		publishCommittedEvent(s.Bus, statusEvent)
	} else {
		s.publishAgentStatus(agent)
	}
}

// publishAgentStatus 向总线发布 agent:status 事件（无事务启动器夹具的内存路径）。
func (s *TaskService) publishAgentStatus(agent db.Agent) {
	s.Bus.Publish(events.Event{
		Type:        protocol.EventAgentStatus,
		WorkspaceID: util.UUIDToString(agent.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload:     map[string]any{"agent": agentToMap(agent)},
	})
}

// LoadAgentSkills 加载 agent 的技能及其文件，供任务执行使用。
//
// 读取失败会被【上报】，绝不吞成更短的技能集。两次读取对 agent 整个技能集都是全有或全无——文件加载一条
// 查询覆盖全部技能——因此吞错不是降级载荷，而是静默替换：要么每个技能失去支持文件，要么 agent 失去全部技能。
// 下游无从把它与“真没有技能”区分，因为 bundle 哈希基于“实际加载到的内容”计算，daemon 自己的校验会通过，
// agent 在缺失规则的情况下启动。调用方必须处置失败（保留认领待重投，或 resolve 返回 5xx），而不是派发之。
func (s *TaskService) LoadAgentSkills(ctx context.Context, agentID pgtype.UUID) ([]AgentSkillData, error) {
	skills, err := s.Queries.ListAgentSkills(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("list agent skills: %w", err)
	}
	if len(skills) == 0 {
		return nil, nil
	}
	return s.skillsWithFiles(ctx, skills)
}

// skillsWithFiles 一次往返加载给定全部技能的文件（而非每技能一次查询），并按给定顺序组装结果。认领时全量
// 加载与 resolve 时收敛加载共用，避免两者在技能顺序、单技能内文件顺序、以及“无文件技能的文件列表为 nil（而非
// 空）”上分叉。
func (s *TaskService) skillsWithFiles(ctx context.Context, skills []db.Skill) ([]AgentSkillData, error) {
	skillIDs := make([]pgtype.UUID, len(skills))
	for i, sk := range skills {
		skillIDs[i] = sk.ID
	}
	// 单趟线性按 skill_id 分组——查询按 skill_id、path 排序。
	// skill_id, path.
	files, err := s.Queries.ListSkillFilesBySkillIDs(ctx, skillIDs)
	if err != nil {
		return nil, fmt.Errorf("list skill files for %d skills: %w", len(skills), err)
	}
	filesBySkill := make(map[string][]AgentSkillFileData, len(skills))
	for _, f := range files {
		id := util.UUIDToString(f.SkillID)
		filesBySkill[id] = append(filesBySkill[id], AgentSkillFileData{Path: f.Path, Content: f.Content, Kind: f.Kind, FileID: util.UUIDToString(f.ID), Mode: f.Mode, SHA256: "sha256:" + f.Sha256, SizeBytes: f.SizeBytes})
	}

	result := make([]AgentSkillData, 0, len(skills))
	for _, sk := range skills {
		result = append(result, AgentSkillData{
			ID:          util.UUIDToString(sk.ID),
			Name:        sk.Name,
			Description: sk.Description,
			Content:     sk.Content,
			Files:       filesBySkill[util.UUIDToString(sk.ID)],
		})
	}
	return result, nil
}

// LoadAgentSkillBundles 返回 agent 可见的全部技能（含内置），带稳定 bundle 哈希与供瘦认领用的轻量 refs。
// 对 workspace 技能读取错误采取 fail closed，理由见 LoadAgentSkills：由部分读取拼出的 bundle 集与正确集
// 无法区分。
func (s *TaskService) LoadAgentSkillBundles(ctx context.Context, agentID pgtype.UUID) ([]AgentSkillData, []AgentSkillRefData, error) {
	skills, err := s.LoadAgentSkills(ctx, agentID)
	if err != nil {
		return nil, nil, err
	}
	skills = append(skills, s.BuiltinSkills()...)
	bundles, refs := BuildAgentSkillBundles(skills)
	return bundles, refs, nil
}

// BuiltinSkillID 是内置技能被寻址所用的 ref id。内置技能没有数据库行，其身份即名字——这是把名字变成身份
// 的唯一位置，既供分发 id 的 bundle 构建方用，也供查回它的解析方用。
func BuiltinSkillID(name string) string { return "builtin:" + name }

// AgentSkillBundleKey 是 daemon 技能 ref 解析所用的 (source, id) 身份键。source 是键的一部分，因为内置技能
// 与 workspace 技能即使同名也是不同 bundle。
func AgentSkillBundleKey(source, id string) string { return source + "\x00" + id }

// AgentSkillBundleRef 是 daemon 请求解析的一个技能。
type AgentSkillBundleRef struct {
	ID     string
	Source string
}

// LoadRequestedAgentSkillBundles 只返回给定 refs 对应的 bundle，按 AgentSkillBundleKey 键控。agent 看不到的
// ref 直接缺席于 map——ListAgentSkillsByIDs 的连接谓词即授权，“无行”与“不允许”是同一答案，调用方把两者
// 都报为 not-found。
//
// 存在原因是 daemon 每个请求只解析一个技能（GH #4505，使每次下载都有独立按大小的截止时间并各自缓存）。若从
// agent 的完整 bundle 集里取，服务器每次请求都要重做整个 agent：N 次请求、每次读并哈希全部 N 个技能只为
// 返回一个。只加载所问内容使该操作线性化，因此 resolve 路径绝不能复用 LoadAgentSkillBundles。
func (s *TaskService) LoadRequestedAgentSkillBundles(ctx context.Context, agentID pgtype.UUID, refs []AgentSkillBundleRef) (map[string]AgentSkillData, error) {
	requestedIDs := make([]pgtype.UUID, 0, len(refs))
	seenWorkspace := make(map[string]struct{}, len(refs))
	wantBuiltin := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		switch ref.Source {
		case skillbundle.SourceBuiltin:
			wantBuiltin[ref.ID] = struct{}{}
		case skillbundle.SourceWorkspace:
			if _, ok := seenWorkspace[ref.ID]; ok {
				continue
			}
			id, err := util.ParseUUID(ref.ID)
			if err != nil {
				// 无法解析的 id 匹配不到任何行，与“agent 没有该 id”结局相同。跳过它可避免单个畸形 ref 拖垮同批其它 refs。
				continue
			}
			seenWorkspace[ref.ID] = struct{}{}
			requestedIDs = append(requestedIDs, id)
		}
		// 其它来源没有服务端生产者，解析结果为空，调用方上报 not-found。
	}

	var requested []AgentSkillData
	if len(requestedIDs) > 0 {
		skills, err := s.Queries.ListAgentSkillsByIDs(ctx, db.ListAgentSkillsByIDsParams{
			AgentID:  agentID,
			SkillIds: requestedIDs,
		})
		if err != nil {
			return nil, fmt.Errorf("list agent skills by ids: %w", err)
		}
		if len(skills) > 0 {
			// 与 LoadAgentSkills 相同的 fail-closed 规则：文件读失败会产出哈希与校验都像完整版的 bundle，因此绝不能
			// 被提供出去。
			loaded, err := s.skillsWithFiles(ctx, skills)
			if err != nil {
				return nil, err
			}
			requested = append(requested, loaded...)
		}
	}
	if len(wantBuiltin) > 0 {
		for _, builtin := range s.BuiltinSkills() {
			if _, ok := wantBuiltin[BuiltinSkillID(builtin.Name)]; ok {
				requested = append(requested, builtin)
			}
		}
	}

	bundles, _ := BuildAgentSkillBundles(requested)
	resolved := make(map[string]AgentSkillData, len(bundles))
	for _, bundle := range bundles {
		resolved[AgentSkillBundleKey(bundle.Source, bundle.ID)] = bundle
	}
	return resolved, nil
}

// BuildAgentSkillBundles 计算技能清单/引用：补齐 source/id 推导（空 source 按 id 判内置或工作区），生成
// manifest 哈希、大小与逐文件 SHA256/大小，返回完整 bundle 数据与轻量 ref 数据。
func BuildAgentSkillBundles(skills []AgentSkillData) ([]AgentSkillData, []AgentSkillRefData) {
	bundles := make([]AgentSkillData, 0, len(skills))
	refs := make([]AgentSkillRefData, 0, len(skills))
	for _, skill := range skills {
		source := skill.Source
		id := skill.ID
		if source == "" {
			if id == "" {
				source = skillbundle.SourceBuiltin
			} else {
				source = skillbundle.SourceWorkspace
			}
		}
		if id == "" && source == skillbundle.SourceBuiltin {
			id = BuiltinSkillID(skill.Name)
		}
		skill.Source = source
		skill.ID = id

		files := make([]skillbundle.File, 0, len(skill.Files))
		for _, file := range skill.Files {
			files = append(files, skillbundle.File{Path: file.Path, Content: file.Content, Kind: file.Kind, Mode: file.Mode, SHA256: file.SHA256, SizeBytes: file.SizeBytes})
		}
		manifest := skillbundle.BuildManifest(skillbundle.Skill{
			ID:          skill.ID,
			Source:      skill.Source,
			Name:        skill.Name,
			Description: skill.Description,
			Content:     skill.Content,
			Files:       files,
		})
		skill.Hash = manifest.Hash
		skill.SizeBytes = manifest.SizeBytes
		fileRefsByPath := make(map[string]skillbundle.FileRef, len(manifest.Files))
		for _, file := range manifest.Files {
			fileRefsByPath[file.Path] = file
		}
		for i := range skill.Files {
			if ref, ok := fileRefsByPath[skill.Files[i].Path]; ok {
				skill.Files[i].SHA256 = ref.SHA256
				skill.Files[i].SizeBytes = ref.SizeBytes
			}
		}
		bundles = append(bundles, skill)

		refFiles := make([]AgentSkillFileRefData, 0, len(manifest.Files))
		for _, file := range manifest.Files {
			refFiles = append(refFiles, AgentSkillFileRefData{
				Path:      file.Path,
				SHA256:    file.SHA256,
				SizeBytes: file.SizeBytes,
			})
		}
		refs = append(refs, AgentSkillRefData{
			ID:          skill.ID,
			Source:      skill.Source,
			Name:        skill.Name,
			Description: skill.Description,
			Hash:        manifest.Hash,
			SizeBytes:   manifest.SizeBytes,
			FileCount:   manifest.FileCount,
			Files:       refFiles,
		})
	}
	return bundles, refs
}

// AgentSkillData 表示任务执行响应里的一项技能（含清单字段）。
type AgentSkillData struct {
	ID          string               `json:"id"`
	Source      string               `json:"source,omitempty"`
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Hash        string               `json:"hash,omitempty"`
	SizeBytes   int64                `json:"size_bytes,omitempty"`
	Content     string               `json:"content"`
	Files       []AgentSkillFileData `json:"files,omitempty"`
}

// AgentSkillFileData 表示技能内的一个支持文件。
type AgentSkillFileData struct {
	Kind      string `json:"kind,omitempty"`
	FileID    string `json:"file_id,omitempty"`
	Mode      int32  `json:"mode,omitempty"`
	Data      []byte `json:"data,omitempty"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	SHA256    string `json:"sha256,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

// AgentSkillRefData 表示技能 bundle 的轻量引用（不含正文，含哈希/大小/文件 refs）。
type AgentSkillRefData struct {
	ID          string                  `json:"id"`
	Source      string                  `json:"source"`
	Name        string                  `json:"name"`
	Description string                  `json:"description,omitempty"`
	Hash        string                  `json:"hash"`
	SizeBytes   int64                   `json:"size_bytes"`
	FileCount   int                     `json:"file_count"`
	Files       []AgentSkillFileRefData `json:"files,omitempty"`
}

// AgentSkillFileRefData 表示 bundle 引用里单个文件的轻量元数据。
type AgentSkillFileRefData struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// computeChatElapsedMs 返回从任务创建（用户点发送）到终态（完成/失败）的墙钟时长，存于助手 chat_message，
// 供 UI 渲染 “Replied in 38s” / “Failed after 12s”。用 created_at 而非 started_at：用户感知的是含排队与派发
// 的总等待时间，不只是 daemon 实际运行时长。
func computeChatElapsedMs(task db.AgentTaskQueue) pgtype.Int8 {
	if !task.CompletedAt.Valid || !task.CreatedAt.Valid {
		return pgtype.Int8{}
	}
	ms := task.CompletedAt.Time.Sub(task.CreatedAt.Time).Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return pgtype.Int8{Int64: ms, Valid: true}
}

// priorityToInt 把 issue 的四象限映射为任务行整数值，保证紧急且重要的任务先执行。
func priorityToInt(p string) int32 {
	switch p {
	case "important_urgent":
		return 4
	case "important_not_urgent":
		return 3
	case "not_important_urgent":
		return 2
	case "not_important_not_urgent":
		return 1
	default:
		return 0
	}
}

// NotifyTaskEnqueued 是 TaskService 之外调用方（如 AutopilotService.dispatchRunOnly）直接向 agent_task_queue
// 插行后使用的跨包 shim：失效空认领缓存并踢 daemon WS，使新任务不必等下一个轮询即被认领。
func (s *TaskService) NotifyTaskEnqueued(ctx context.Context, task db.AgentTaskQueue) {
	s.captureTaskQueued(ctx, task)
	s.notifyTaskAvailable(task)
}

// NotifyTaskFinished 在任务到达终态后失效某 runtime 的空认领结论并发出 best-effort daemon 唤醒。任务 ID
// 刻意不进唤醒载荷：已完成任务本身不可用，提示只表示“某排队的后继可能因 agent 容量或串行化屏障解除而可
// 认领”。
func (s *TaskService) NotifyTaskFinished(task db.AgentTaskQueue) {
	s.forgetTaskReclaim(task)
	s.notifyRuntimeMayHaveWork(task.RuntimeID, "")
}

// notifyTasksFinished 是批量终态转换使用的批量形式。按 runtime 合并：一台机器上取消多条任务只产生一次缓存
// bump 与一次 websocket 提示，而不是一连串相同工作。
func (s *TaskService) notifyTasksFinished(tasks []db.AgentTaskQueue) {
	seen := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		if !task.RuntimeID.Valid {
			continue
		}
		s.forgetTaskReclaim(task)
		runtimeKey := util.UUIDToString(task.RuntimeID)
		if _, ok := seen[runtimeKey]; ok {
			continue
		}
		seen[runtimeKey] = struct{}{}
		s.notifyRuntimeMayHaveWork(task.RuntimeID, "")
	}
}

// NotifyTasksFinished 是“持有事务、最终化了多条任务”的 handler 使用的批量提交后唤醒。
func (s *TaskService) NotifyTasksFinished(tasks []db.AgentTaskQueue) {
	s.notifyTasksFinished(tasks)
}

// notifyTaskAvailable 在任务插入后执行：先 bump 该 runtime 的失效版本，使任何即将写下 “空”结论的在途认领在
// 读取时被拒；再踢 daemon WS，让 daemon 不必等下一轮询即可认领。顺序重要——Bump 必须先于唤醒，否则唤醒
// 驱动的认领可能读到仍有效的空结论并返回 null。
func (s *TaskService) notifyTaskAvailable(task db.AgentTaskQueue) {
	s.notifyRuntimeMayHaveWork(task.RuntimeID, util.UUIDToString(task.ID))
}

// notifyRuntimeMayHaveWork 是“先 bump、后唤醒”的共享原语，供全新入队与可解除排队工作的终态转换共用。
func (s *TaskService) notifyRuntimeMayHaveWork(runtimeID pgtype.UUID, taskID string) {
	if !runtimeID.Valid {
		return
	}
	runtimeKey := util.UUIDToString(runtimeID)
	// 用后台 context：缓存 bump / 唤醒必须活过创建任务的请求，否则客户端提前断开可能让空结论留在原地、把刚入队的
	// 任务卡到 TTL 过期。缓存自身给每次 Redis 调用设了短超时，卡死的 Redis 不会阻塞入队。
	s.EmptyClaim.Bump(context.Background(), runtimeKey)
	if s.Wakeup == nil {
		return
	}
	s.Wakeup.NotifyTaskAvailable(runtimeKey, taskID)
}

// buildTaskDispatchEvent 构建 task:dispatch 的持久事件：解包任务 context 注入载荷，解析工作区，生成带
// 转换时间戳的幂等键（一次任务可能被多次派发，重放按流序、相同重试幂等）。
func buildTaskDispatchEvent(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (events.Event, error) {
	var payload map[string]any
	if task.Context != nil {
		_ = json.Unmarshal(task.Context, &payload)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["task_id"] = util.UUIDToString(task.ID)
	payload["runtime_id"] = util.UUIDToString(task.RuntimeID)
	payload["issue_id"] = util.UUIDToString(task.IssueID)
	payload["agent_id"] = util.UUIDToString(task.AgentID)
	payload["status"] = task.Status
	// chat_session_id 是聊天窗口在 daemon 认领瞬间把 `chatKeys.pendingTask` 直写为 running 的路由键。没有它，
	// 状态胶囊会一直停在 “Queued” 直到完成。
	if task.ChatSessionID.Valid {
		payload["chat_session_id"] = util.UUIDToString(task.ChatSessionID)
	}

	workspaceID, err := resolveTaskWorkspaceWithQueries(ctx, queries, task)
	if err != nil {
		return events.Event{}, err
	}
	if workspaceID == "" {
		return events.Event{}, fmt.Errorf("task %s has no workspace", util.UUIDToString(task.ID))
	}
	event := taskEvent(protocol.EventTaskDispatch, workspaceID, task, payload)
	// 认领崩溃或重排后任务可能被多次派发。带上转换时间戳，让每次真实派发可按流序重放，而完全相同的一次重试
	// 保持幂等。
	event.IdempotencyKey = taskLifecycleIdempotencyKey(protocol.EventTaskDispatch, task, event)
	event.StreamKey = "task:" + util.UUIDToString(task.ID)
	return event, nil
}

// broadcastTaskDispatch 构建并在内存中广播 task:dispatch（无事务启动器夹具路径）。
func (s *TaskService) broadcastTaskDispatch(ctx context.Context, task db.AgentTaskQueue) {
	event, err := buildTaskDispatchEvent(ctx, s.Queries, task)
	if err != nil {
		slog.Warn("task dispatch event unavailable", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	s.Bus.Publish(event)
}

// taskEvent 构建共享的任务生命周期事件契约。作用域提示刻意在信封上重复：现有监听者仍兼容 payload map，
// 而实时层可在启用按资源扇出后不经解码即路由。
func taskEvent(eventType, workspaceID string, task db.AgentTaskQueue, extra ...map[string]any) events.Event {
	payload := map[string]any{
		"task_id":  util.UUIDToString(task.ID),
		"agent_id": util.UUIDToString(task.AgentID),
		"issue_id": util.UUIDToString(task.IssueID),
		"status":   task.Status,
	}
	e := events.Event{
		Type:        eventType,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		ActorID:     "",
		TaskID:      util.UUIDToString(task.ID),
		Payload:     payload,
	}
	if task.ChatSessionID.Valid {
		chatSessionID := util.UUIDToString(task.ChatSessionID)
		payload["chat_session_id"] = chatSessionID
		e.ChatSessionID = chatSessionID
	}
	for _, fields := range extra {
		for key, value := range fields {
			payload[key] = value
		}
	}
	return e
}

// publishTaskEvent 向总线发布任务事件；workspace 为空时丢弃。
func (s *TaskService) publishTaskEvent(eventType, workspaceID string, task db.AgentTaskQueue, extra ...map[string]any) {
	if workspaceID == "" {
		return
	}
	s.Bus.Publish(taskEvent(eventType, workspaceID, task, extra...))
}

// publishTaskEventAfterCommit 在调用方已提交对应状态转换后重放 durable 信封。一些提交后路径会删除本可提供
// 任务作用域的 issue/chat 会话/agent，因此从行重建事件可能静默丢失通知。生产服务在无法加载持久信封时 fail
// closed；只有没有事务启动器的嵌入式服务使用内存路径。
func (s *TaskService) publishTaskEventAfterCommit(ctx context.Context, eventType, workspaceID string, task db.AgentTaskQueue, extra ...map[string]any) {
	if s.TxStarter != nil {
		if err := publishPersistedTaskEvent(ctx, s.Queries, s.Bus, eventType, task); err != nil {
			slog.Error("persisted task event unavailable after commit; durable delivery will retry from the outbox",
				"event_type", eventType,
				"task_id", util.UUIDToString(task.ID),
				"error", err,
			)
		}
		return
	}
	s.publishTaskEvent(eventType, workspaceID, task, extra...)
}

// broadcastTaskEvent 解析任务工作区并向内存总线广播事件（无事务启动器夹具路径）。
func (s *TaskService) broadcastTaskEvent(ctx context.Context, eventType string, task db.AgentTaskQueue, extra ...map[string]any) {
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	s.publishTaskEvent(eventType, workspaceID, task, extra...)
}

// taskFailedFields 追加频道外发所需的终态失败上下文，同时不改动既有任务事件消费方依赖的长期 map 载荷。
// 错误文本会被脱敏并在自动重试 pending 时省略，使消费方区分“中间失败尝试”与“用户可见的终态失败”。
func taskFailedFields(errMsg, failureReason string, retryPending bool) map[string]any {
	fields := map[string]any{
		"failure_reason": failureReason,
		"retry_pending":  retryPending,
	}
	if errMsg != "" && !retryPending {
		fields["error"] = redact.Text(errMsg)
	}
	return fields
}

func (s *TaskService) publishTaskFailedEvent(workspaceID string, task db.AgentTaskQueue, errMsg, failureReason string, retryPending bool) {
	s.publishTaskEvent(protocol.EventTaskFailed, workspaceID, task, taskFailedFields(errMsg, failureReason, retryPending))
}

func (s *TaskService) broadcastTaskFailedEvent(ctx context.Context, task db.AgentTaskQueue, errMsg, failureReason string, retryPending bool) {
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	s.publishTaskFailedEvent(workspaceID, task, errMsg, failureReason, retryPending)
}

// ResolveTaskWorkspaceID 判断任务的工作区 ID：issue 任务来自 issue；chat 任务来自 chat 会话；autopilot 任务
// 经其 run 来自 autopilot；quick-create 任务来自 context JSONB。全部链接都无法解析时返回 ""——调用方把它
// 视为 “not found”。
func (s *TaskService) ResolveTaskWorkspaceID(ctx context.Context, task db.AgentTaskQueue) string {
	var supervision SupervisionContext
	if json.Unmarshal(task.Context, &supervision) == nil && supervision.Type == "quality_supervision" {
		return supervision.WorkspaceID
	}
	if task.IssueID.Valid {
		if issue, err := s.Queries.GetIssue(ctx, task.IssueID); err == nil {
			return util.UUIDToString(issue.WorkspaceID)
		}
	}
	if task.ChatSessionID.Valid {
		if cs, err := s.Queries.GetChatSession(ctx, task.ChatSessionID); err == nil {
			return util.UUIDToString(cs.WorkspaceID)
		}
	}
	if task.AutopilotRunID.Valid {
		if run, err := s.Queries.GetAutopilotRun(ctx, task.AutopilotRunID); err == nil {
			if ap, err := s.Queries.GetAutopilot(ctx, run.AutopilotID); err == nil {
				return util.UUIDToString(ap.WorkspaceID)
			}
		}
	}
	// quick-create 任务没有 issue/chat/autopilot 链接——工作区在 context JSONB 里。此前这里返回 "" 正是阻塞
	// requireDaemonTaskAccess（daemon 的 /start、/progress、/complete、/fail 返回 404）并静默丢弃 task:dispatch /
	// task:completed 广播的原因，也是 quick-create 任务看似卡在 queued 的原因。
	if qc, ok := s.parseQuickCreateContext(task); ok {
		return qc.WorkspaceID
	}
	return ""
}

// broadcastChatDone 向工作区广播 chat:done，载荷含消息内容/类型与 quick-actions 状态；消息为 nil 时只发
// 会话级收尾。
func (s *TaskService) broadcastChatDone(ctx context.Context, task db.AgentTaskQueue, msg *db.ChatMessage, quickActionsPending bool) {
	workspaceID := s.ResolveTaskWorkspaceID(ctx, task)
	if workspaceID == "" {
		return
	}
	payload := protocol.ChatDonePayload{
		ChatSessionID:       util.UUIDToString(task.ChatSessionID),
		TaskID:              util.UUIDToString(task.ID),
		QuickActionsPending: quickActionsPending,
	}
	if msg != nil {
		payload.MessageID = util.UUIDToString(msg.ID)
		payload.Content = msg.Content
		payload.MessageKind = msg.MessageKind
		if len(msg.QuickActions) > 0 {
			_ = json.Unmarshal(msg.QuickActions, &payload.QuickActions)
		}
		if msg.CreatedAt.Valid {
			payload.CreatedAt = msg.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		if msg.ElapsedMs.Valid {
			payload.ElapsedMs = msg.ElapsedMs.Int64
		}
	}
	s.Bus.Publish(events.Event{
		Type:          protocol.EventChatDone,
		WorkspaceID:   workspaceID,
		ActorType:     "system",
		ActorID:       "",
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		Payload:       payload,
	})
}

// broadcastIssueUpdated 发布前端实时调和（onIssueUpdated）依赖的 issue:updated 事件，使 issue 在状态列/过滤
// 器间移动并调和桶计数。prevStatus 为写入前的状态，客户端据此用 status_changed 门控调和。
//
// `issue` 载荷是 map（IssueToMap），workspace WS 扇出（listeners.go SubscribeAll）原样编组广播——这正是驱动
// UI 调和的东西。注意它不覆盖完整 HTTP UpdateIssue 副作用：activity-log 与 inbox 监听者把 `issue` 类型断言为
// handler.IssueResponse，遇到 map 会跳过，因此后台状态重置不会产生状态变更的活动/通知。这对实时陈旧修复
// （#4648 / MUL-3782）是有意为之；把这些副作用并入意味着统一载荷类型，留作后续。
func (s *TaskService) broadcastIssueUpdated(ctx context.Context, issue db.Issue, prevStatus string) {
	prefix := s.getIssuePrefix(issue.WorkspaceID)
	s.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"issue":          IssueToMapResolved(ctx, s.Queries, issue, prefix),
			"status_changed": prevStatus != issue.Status,
			"prev_status":    prevStatus,
		},
	})
}

// resetIssueAfterTaskFailure 把“无剩余活跃工作”的判定与 issue:updated 事件做成一个事务。worker 可能收到
// 重复终态回调，因此在查队列前先锁 issue 行；只有首个回调能把 in_progress 移回 todo 并入队对应 durable
// 事件。
func (s *TaskService) resetIssueAfterTaskFailure(ctx context.Context, issueID pgtype.UUID, observed db.Issue) (events.Event, bool, error) {
	if s.TxStarter == nil {
		// 少数进程内单测夹具刻意不装事务启动器。保留其历史行为，而生产环境始终走上面的原子分支。
		effectiveStatus := issuestatus.Effective(ctx, s.Queries, observed.WorkspaceID, observed.Status)
		if effectiveStatus != "in_progress" {
			return events.Event{}, false, nil
		}
		hasActive, err := s.Queries.HasActiveTaskForIssue(ctx, issueID)
		if err != nil || hasActive {
			return events.Event{}, false, err
		}
		updated, err := s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: issueID, Status: "todo", WorkspaceID: observed.WorkspaceID})
		if err != nil {
			return events.Event{}, false, err
		}
		s.broadcastIssueUpdated(ctx, updated, observed.Status)
		return events.Event{}, true, nil
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return events.Event{}, false, fmt.Errorf("begin issue failure reset: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	issue, err := qtx.LockIssueForTaskFailureReset(ctx, db.LockIssueForTaskFailureResetParams{
		ID: issueID, WorkspaceID: observed.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return events.Event{}, false, nil
	}
	if err != nil {
		return events.Event{}, false, fmt.Errorf("lock issue for failure reset: %w", err)
	}
	if issuestatus.Effective(ctx, qtx, issue.WorkspaceID, issue.Status) != "in_progress" {
		return events.Event{}, false, nil
	}
	hasActive, err := qtx.HasActiveTaskForIssue(ctx, issueID)
	if err != nil {
		return events.Event{}, false, fmt.Errorf("check active tasks for failure reset: %w", err)
	}
	if hasActive {
		return events.Event{}, false, nil
	}
	updated, err := qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID: issueID, Status: "todo", WorkspaceID: observed.WorkspaceID,
	})
	if err != nil {
		return events.Event{}, false, fmt.Errorf("update issue after task failure: %w", err)
	}
	event := events.Event{
		Type:           protocol.EventIssueUpdated,
		IdempotencyKey: "issue:failure-reset:" + util.UUIDToString(updated.ID) + ":" + strconv.FormatInt(updated.Revision, 10),
		StreamKey:      "issue:" + util.UUIDToString(updated.ID),
		WorkspaceID:    util.UUIDToString(updated.WorkspaceID),
		ActorType:      "system",
		Payload: map[string]any{
			"issue":          IssueToMapResolved(ctx, qtx, updated, s.getIssuePrefixWithQueries(ctx, qtx, updated.WorkspaceID)),
			"status_changed": issue.Status != updated.Status,
			"prev_status":    issue.Status,
		},
	}
	event, err = recordDurableEventTx(ctx, qtx, event)
	if err != nil {
		return events.Event{}, false, fmt.Errorf("record issue failure reset event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return events.Event{}, false, fmt.Errorf("commit issue failure reset: %w", err)
	}
	return event, true, nil
}

// getIssuePrefixWithQueries 用给定查询句柄读取工作区的 issue 前缀（供事件载荷渲染 issue 标识）。
func (s *TaskService) getIssuePrefixWithQueries(ctx context.Context, queries *db.Queries, workspaceID pgtype.UUID) string {
	ws, err := queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return ""
	}
	return ws.IssuePrefix
}

// getIssuePrefix 读取工作区的 issue 前缀。
func (s *TaskService) getIssuePrefix(workspaceID pgtype.UUID) string {
	ws, err := s.Queries.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return ""
	}
	return ws.IssuePrefix
}

// commentEventFields 渲染 HTTP handler 之外发布的 comment:created 广播所携带的 `comment` 对象（agent 回复、
// 委托失败恢复、恢复耗尽）。
//
// created_at 经 util.TimestampToString——与 REST CommentResponse 所用【相同】的助手——使客户端经 WS 收到的时间戳
// 与重拉该评论得到的逐字节一致。这些载荷过去用字面 "Z" 后缀格式化时间，而那不是 Go 的 Z07:00 偏移 token：
// pgx 把 timestamptz 解码到进程时区（pgtype/timestamptz.go，未配置 ScanLocation），于是进程 TZ 非 UTC 的部署
// 会把本地墙上时钟数字贴上 UTC 标签广播出去。客户端随后把条目放在偏离真实时刻整整一个时区差的位置，重拉
// 替换值后又被移动一次——表现为时间线条目跳位。
func commentEventFields(c db.Comment) map[string]any {
	return map[string]any{
		"id":               util.UUIDToString(c.ID),
		"issue_id":         util.UUIDToString(c.IssueID),
		"author_type":      c.AuthorType,
		"author_id":        util.UUIDToString(c.AuthorID),
		"content":          c.Content,
		"type":             c.Type,
		"parent_id":        util.UUIDToPtr(c.ParentID),
		"source_task_id":   util.UUIDToPtr(c.SourceTaskID),
		"updated_at":       util.TimestampToString(c.UpdatedAt),
		"resolved_at":      util.TimestampToPtr(c.ResolvedAt),
		"resolved_by_type": util.TextToPtr(c.ResolvedByType),
		"resolved_by_id":   util.UUIDToPtr(c.ResolvedByID),
		"created_at":       util.TimestampToString(c.CreatedAt),
	}
}

func (s *TaskService) createAgentComment(ctx context.Context, issueID, agentID pgtype.UUID, content, commentType string, parentID, sourceTaskID pgtype.UUID) {
	if content == "" {
		return
	}
	if s.TxStarter == nil {
		slog.Warn("cannot create agent comment without transaction starter", "issue_id", util.UUIDToString(issueID))
		return
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("begin agent comment transaction failed", "error", err, "issue_id", util.UUIDToString(issueID))
		return
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	replyEvents, err := createAgentCommentTx(ctx, qtx, issueID, agentID, content, commentType, parentID, sourceTaskID)
	if err != nil {
		slog.Warn("create agent comment failed", "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		return
	}
	s.CancelDeferredEscalationsForIssueAgent(ctx, issueID, agentID)
	for _, event := range replyEvents {
		publishCommittedEvent(s.Bus, event)
	}
}

// AutoUnresolveThreadOnReply 在已解决线程收到回复时清掉线程根的 resolved_at 并广播 comment:unresolved。用户侧
// Handler.CreateComment 与 agent 侧 TaskService.createAgentComment 共用，使“已解决又被回复”的状态永不失步
// （PR #2300 上 Emacs 标记的 bug 之一）。错误只记日志——回复本身已提交，失步可在下次读取时恢复。
func (s *TaskService) AutoUnresolveThreadOnReply(ctx context.Context, parent *db.Comment, workspaceID, actorType, actorID string) {
	if parent == nil || !parent.ResolvedAt.Valid {
		return
	}
	if s.TxStarter == nil {
		slog.Warn("cannot auto-unresolve thread without transaction starter", "comment_id", util.UUIDToString(parent.ID))
		return
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		slog.Warn("begin auto-unresolve transaction failed", "error", err, "comment_id", util.UUIDToString(parent.ID))
		return
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	updated, err := qtx.UnresolveCommentIfResolved(ctx, parent.ID)
	if err != nil {
		slog.Warn("auto-unresolve on reply failed", "error", err, "comment_id", util.UUIDToString(parent.ID))
		return
	}
	event, err := recordDurableEventTx(ctx, qtx, events.Event{
		Type:           protocol.EventCommentUnresolved,
		IdempotencyKey: "comment:unresolved:" + util.UUIDToString(updated.ID) + ":" + strconv.FormatInt(updated.Revision, 10),
		StreamKey:      "issue:" + util.UUIDToString(updated.IssueID),
		WorkspaceID:    workspaceID,
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"comment": commentEventFields(updated),
		},
	})
	if err != nil {
		slog.Warn("enqueue auto-unresolve event failed", "error", err, "comment_id", util.UUIDToString(updated.ID))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Warn("commit auto-unresolve transaction failed", "error", err, "comment_id", util.UUIDToString(updated.ID))
		return
	}
	publishCommittedEvent(s.Bus, event)
}

// IssueToMap 把 issue 行渲染成 issue:created / issue:updated 广播载荷在 "issue" 键下的 map 形态。凡在 HTTP
// handler 之外发布该事件——autopilot 与频道引擎 /issue 命令发 issue:created、后台卡住 issue 状态重置发
// issue:updated——都以它作为该形态的单一事实来源。workspace WS 扇出原样编组给 UI，cmd/server 的
// extractIssueFields 从中读 id / creator_id / workspace_id 决定自动订阅谁。
//
// 该 map 必须与同一事件的另一渲染 handler.IssueResponse 键兼容。客户端把两者都当作完整 Issue 直接插入列表缓存、
// 不做运行时校验，因此这里缺的字段会读回 undefined 直到下次重拉——见 TestIssueToMap_KeysMatchIssueResponse，
// 两种渲染一旦分叉测试即失败。
// builtInStatusCategory 在“无需读目录即可得知”时返回状态类目——即 7 个内置状态，其 key == category。
func builtInStatusCategory(status string) string {
	if issuestatus.IsBuiltIn(status) {
		return status
	}
	return ""
}

// IssueToMapResolved 是 IssueToMap 带【权威】status_category / status_name 的版本：两者都经状态目录解析，
// 自定义状态不会以空值输出。后台事件走这里；客户端把该载荷视为完整 issue 并按类目归档（MUL-6243）。
//
// 两个字段来自【一次】目录读取。分开解析会在每个携带自定义状态的事件上使查询翻倍，而 HTTP 渲染已通过其
// Resolver 共享单次读取（MUL-6749）。
func IssueToMapResolved(ctx context.Context, q issuestatus.Querier, issue db.Issue, issuePrefix string) map[string]any {
	m := IssueToMap(issue, issuePrefix)
	category, name := issuestatus.EffectiveAndName(ctx, q, issue.WorkspaceID, issue.Status)
	m["status_category"] = category
	m["status_name"] = name
	return m
}

// IssueToMap 把 issue 行渲染成事件/通知用 map。status_category / status_name 见内联注释（MUL-6243/6749）。
func IssueToMap(issue db.Issue, issuePrefix string) map[string]any {
	return map[string]any{
		"id":           util.UUIDToString(issue.ID),
		"workspace_id": util.UUIDToString(issue.WorkspaceID),
		"number":       issue.Number,
		"identifier":   IssueIdentifier(issuePrefix, issue.Number),
		"title":        issue.Title,
		"description":  util.TextToPtr(issue.Description),
		"status":       issue.Status,
		// 镜像 handler.IssueResponse.StatusCategory：内置状态本身就是其类目，因此无需查目录即可解析。
		// 自定义状态为空，由消费方经目录解析（MUL-6243）。
		"status_category": builtInStatusCategory(issue.Status),
		// 镜像 handler.IssueResponse.StatusName。内置状态不带名字——客户端按键本地化——自定义状态由持有目录的
		// IssueToMapResolved 填充。无条件输出，使本渲染不会丢失 HTTP 渲染携带的键（MUL-6749）。
		"status_name":      "",
		"priority":         issue.Priority,
		"assignee_type":    util.TextToPtr(issue.AssigneeType),
		"assignee_id":      util.UUIDToPtr(issue.AssigneeID),
		"creator_type":     issue.CreatorType,
		"creator_id":       util.UUIDToString(issue.CreatorID),
		"parent_issue_id":  util.UUIDToPtr(issue.ParentIssueID),
		"project_id":       util.UUIDToPtr(issue.ProjectID),
		"position":         issue.Position,
		"stage":            util.Int4ToPtr(issue.Stage),
		"start_date":       util.DateToPtr(issue.StartDate),
		"due_date":         util.DateToPtr(issue.DueDate),
		"created_at":       util.TimestampToString(issue.CreatedAt),
		"updated_at":       util.TimestampToString(issue.UpdatedAt),
		"last_activity_at": util.TimestampToNanoPtr(issue.LastActivityAt),
		"revision":         issue.Revision,
		"metadata":         util.JSONObjectOrEmpty(issue.Metadata),
		"properties":       util.JSONObjectOrEmpty(issue.Properties),
	}
}

// IssueIdentifier 渲染人类可读的 issue 键（“MUL-42”）。防御性地解析工作区前缀的调用方可传 ""：失败的工作区
// 查询不应表面成孤立 “-42”，因此数字单独呈现为 “#42”。HTTP 层永不传 ""——handler.getIssuePrefix 渲染任何
// 内容前都会从工作区名推导前缀。
func IssueIdentifier(issuePrefix string, number int32) string {
	if issuePrefix == "" {
		return "#" + strconv.Itoa(int(number))
	}
	return issuePrefix + "-" + strconv.Itoa(int(number))
}

// parseQuickCreateContext 在任务 context JSONB 含 type == "quick_create" 时返回该载荷，否则 bool 为 false
// 供调用方短路。链接了 issue / chat / autopilot 的任务即使碰巧带 context blob 也绝不是 quick-create，
// 因此先过滤掉。
func (s *TaskService) parseQuickCreateContext(task db.AgentTaskQueue) (QuickCreateContext, bool) {
	if task.IssueID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid {
		return QuickCreateContext{}, false
	}
	if len(task.Context) == 0 {
		return QuickCreateContext{}, false
	}
	var qc QuickCreateContext
	if err := json.Unmarshal(task.Context, &qc); err != nil {
		return QuickCreateContext{}, false
	}
	if qc.Type != QuickCreateContextType {
		return QuickCreateContext{}, false
	}
	return qc, true
}

// sourceContextAttachedByTask 判断源上下文捕获是否已由本任务落定到它产出的 issue 上（quick-create 专用；
// 无捕获任务直接返回 false）。
func (s *TaskService) sourceContextAttachedByTask(ctx context.Context, task db.AgentTaskQueue, qc QuickCreateContext) (bool, error) {
	if qc.SourceContextID == "" {
		return false, nil
	}
	workspaceID, workspaceErr := util.ParseUUID(qc.WorkspaceID)
	contextID, contextErr := util.ParseUUID(qc.SourceContextID)
	if workspaceErr != nil || contextErr != nil {
		return false, nil
	}
	row, err := s.Queries.GetIssueSourceContextByID(ctx, db.GetIssueSourceContextByIDParams{
		WorkspaceID: workspaceID,
		ID:          contextID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.State == "attached" && row.IssueID.Valid && row.OriginTaskID == task.ID, nil
}

// maxQuickCreateFailureDetailRunes 限制从 quick-create 任务最终输出里提取的失败原因长度。真正的 CLI 错误
// （重复提示、校验错误）很短；远大于此的输出是失控原始流导出，其头部是过程叙述而非真实原因（与 GH #5455
// 相同故障形态），因此降级为通用提示而不当作错误原因呈现。
const maxQuickCreateFailureDetailRunes = 2000

// quickCreateOversizedFailureDetail 是输出超限时的固定英文提示文案（面向用户，保留英文）。
const quickCreateOversizedFailureDetail = "Quick create failed, but the agent's output was too large to show the reason safely. Check the task's execution log for details."

// quickCreateFailureDetail 从 quick-create 任务最终输出提取用户可见的失败原因。quick-create 提示词指示
// agent 在 `multica issue create` 失败时仅以 CLI 错误作为输出退出，因此这里通常携带着真实原因（如指名现有
// issue 的活跃重复消息）。无可用的输出时返回 ""，让调用方回退到通用文案；脱敏由 notifyQuickCreateFailed 负责。
func quickCreateFailureDetail(result []byte) string {
	var payload protocol.TaskCompletedPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		return ""
	}
	// 与评论兜底路径相同的反转义：agent stdout 的字面 `\n` 在原因到达用户前变成真实换行。
	body := strings.TrimSpace(util.UnescapeBackslashEscapes(payload.Output))
	if body == "" {
		return ""
	}
	if utf8.RuneCountInString(body) > maxQuickCreateFailureDetailRunes {
		return quickCreateOversizedFailureDetail
	}
	return body
}

// notifyQuickCreateCompleted 向请求者写入指向 agent 刚创建 issue 的成功收件箱通知。issue 由 daemon 注入的
// MULTICA_QUICK_CREATE_TASK_ID 环境变量盖上 origin_type=quick_create + origin_id=<task_id>，因此该查找是
// 确定性的——对同一 agent 并行创建其它 issue（如 max_concurrent_tasks > 1 时指派任务旁再跑一次 quick-create）
// 也健壮。
func (s *TaskService) notifyQuickCreateCompleted(ctx context.Context, task db.AgentTaskQueue, qc QuickCreateContext, result []byte) {
	workspaceID, err := util.ParseUUID(qc.WorkspaceID)
	if err != nil {
		slog.Warn("quick-create completion: invalid workspace id", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	issue, err := s.Queries.GetIssueByOrigin(ctx, db.GetIssueByOriginParams{
		WorkspaceID: workspaceID,
		OriginType:  pgtype.Text{String: "quick_create", Valid: true},
		OriginID:    task.ID,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			// 查找本身失败（DB 故障、超时等），并非确认 “无 issue”：agent 很可能已创建 issue，失败收件箱会误报。
			// 但任务已完成、没有任何东西会重试这次调和，静默返回会让运行以“完全没有收件箱结果”收场。因此改写一条
			// 中性的终态通知——用户总能拿到结果，且它绝不断言我们并未观察到的失败。
			slog.Error("quick-create completion: issue lookup failed, writing unconfirmed inbox",
				"task_id", util.UUIDToString(task.ID),
				"agent_id", util.UUIDToString(task.AgentID),
				"workspace_id", qc.WorkspaceID,
				"error", err,
			)
			s.notifyQuickCreateUnconfirmed(ctx, task, qc)
			return
		}
		// 没有创建 issue——agent 跑到完成但 CLI create 调用必然失败（最常见是活跃重复守卫）。quick-create 提示词
		// 指示 agent 仅以 CLI 错误作为输出退出，因此优先把它当作失败原因而非通用文案；输出为空时回退到
		// notifyQuickCreateFailed 自身默认值。正是这步把含糊的 “agent 完成却未创建 issue” 变成具体原因（#5885）。
		detail := quickCreateFailureDetail(result)
		slog.Warn("quick-create completion: no issue found, writing failure inbox",
			"task_id", util.UUIDToString(task.ID),
			"agent_id", util.UUIDToString(task.AgentID),
			"workspace_id", qc.WorkspaceID,
			"has_detail", detail != "",
		)
		s.notifyQuickCreateFailed(ctx, task, qc, detail)
		return
	}

	s.persistQuickCreateOutcome(ctx, task, qc, "quick_create_done", issue.Title, "", &issue)
}

// 两种“非成功”quick-create 结果的收件箱类型。它们刻意不同：客户端渲染不同——failed 类型带显式 "Failed:"
// 前缀，不能套到我们无法核实的结果上。早于 unconfirmed 类型的旧客户端会落入既有 default 分支，原样渲染行的
// 标题/正文，而那已是中性措辞。
const (
	inboxTypeQuickCreateFailed      = "quick_create_failed"
	inboxTypeQuickCreateUnconfirmed = "quick_create_unconfirmed"
)

// quickCreateOutcomeID 为某任务+某结果的收件箱行生成确定性 UUID（MD5 名字空间），使重复终态回调重试幂等。
func quickCreateOutcomeID(taskID pgtype.UUID, inboxType string) pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.NewMD5(uuid.Nil, []byte("multica:quick-create:"+util.UUIDToString(taskID)+":"+inboxType)), Valid: true}
}

// persistQuickCreateOutcome 是 quick-create 结果收件箱的共享落库实现：序列化 details（含原始提示与脱敏错误），
// 有事务启动器时用带超时的独立事务——把产物 issue 回链到任务、建收件箱行、记录实时事件一次提交；否则直接
// 写行并即时发布。issue 非空时在其 details 里附带 issue_id 与人类可读 identifier。
func (s *TaskService) persistQuickCreateOutcome(
	ctx context.Context,
	task db.AgentTaskQueue,
	qc QuickCreateContext,
	inboxType, title, body string,
	issue *db.Issue,
) {
	requesterID, err := util.ParseUUID(qc.RequesterID)
	if err != nil {
		return
	}
	workspaceID, err := util.ParseUUID(qc.WorkspaceID)
	if err != nil {
		return
	}
	details, err := json.Marshal(map[string]any{
		"task_id":           util.UUIDToString(task.ID),
		"agent_id":          util.UUIDToString(task.AgentID),
		"original_prompt":   qc.Prompt,
		"error":             redact.Text(body),
		"source_context_id": qc.SourceContextID,
	})
	if err != nil {
		return
	}
	issueID := pgtype.UUID{}
	issueStatus := ""
	if issue != nil {
		issueID = issue.ID
		issueStatus = issue.Status
		prefix := s.getIssuePrefix(workspaceID)
		var detailValues map[string]any
		if err := json.Unmarshal(details, &detailValues); err == nil {
			detailValues["issue_id"] = util.UUIDToString(issue.ID)
			detailValues["identifier"] = fmt.Sprintf("%s-%d", prefix, issue.Number)
			if encoded, marshalErr := json.Marshal(detailValues); marshalErr == nil {
				details = encoded
			}
		}
	}
	if s.TxStarter == nil {
		item, createErr := s.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
			ID:            quickCreateOutcomeID(task.ID, inboxType),
			WorkspaceID:   workspaceID,
			RecipientType: "member",
			RecipientID:   requesterID,
			Type:          inboxType,
			Severity:      "action_required",
			IssueID:       issueID,
			Title:         title,
			Body:          pgtype.Text{String: redact.Text(body), Valid: true},
			ActorType:     pgtype.Text{String: "agent", Valid: true},
			ActorID:       task.AgentID,
			Details:       details,
		})
		if createErr == nil {
			s.publishQuickCreateInbox(item, qc.WorkspaceID, util.UUIDToString(task.AgentID), issueStatus)
		}
		return
	}

	// 完成调和在任务终态事务之后运行。用第二个事务，使“回链产物 issue、创建收件箱行、记录实时事件”仍共享一次
	// durable 提交。固定行 id 让 daemon 回调即便重复，重试也无害。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), quickCreateNotifyTimeout)
	defer cancel()
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		slog.Error("quick-create outcome: begin transaction failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	if issue != nil {
		if err := qtx.LinkTaskToIssue(ctx, db.LinkTaskToIssueParams{ID: task.ID, IssueID: issue.ID}); err != nil {
			slog.Error("quick-create outcome: link task to issue failed", "task_id", util.UUIDToString(task.ID), "issue_id", util.UUIDToString(issue.ID), "error", err)
			return
		}
	}
	item, err := qtx.CreateQuickCreateOutcomeInbox(ctx, db.CreateQuickCreateOutcomeInboxParams{
		WorkspaceID:   workspaceID,
		RecipientType: "member",
		RecipientID:   requesterID,
		Type:          inboxType,
		Severity:      "action_required",
		IssueID:       issueID,
		Title:         title,
		Body:          pgtype.Text{String: redact.Text(body), Valid: true},
		ActorType:     pgtype.Text{String: "agent", Valid: true},
		ActorID:       task.AgentID,
		Details:       details,
		ID:            quickCreateOutcomeID(task.ID, inboxType),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			slog.Error("quick-create outcome: commit without recipient failed", "error", commitErr)
		}
		return
	}
	if err != nil {
		slog.Error("quick-create outcome: create inbox failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	event := events.Event{
		Type:           protocol.EventInboxNew,
		IdempotencyKey: "inbox:new:" + util.UUIDToString(item.ID),
		StreamKey:      "inbox:" + util.UUIDToString(requesterID),
		WorkspaceID:    qc.WorkspaceID,
		ActorType:      "agent",
		ActorID:        util.UUIDToString(task.AgentID),
		Payload: map[string]any{
			"item": func() map[string]any {
				out := InboxItemFields(item)
				out["issue_status"] = issueStatus
				return out
			}(),
		},
	}
	event, err = recordDurableEventTx(ctx, qtx, event)
	if err != nil {
		slog.Error("quick-create outcome: record inbox event failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Error("quick-create outcome: commit failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	publishCommittedEvent(s.Bus, event)
}

// notifyQuickCreateFailed 写入携带原始提示词 + agent ID 的失败收件箱通知，前端可据此渲染 “Edit as advanced
// form” 入口、预填旧建单弹窗，无需用户重打。只在该运行【确认】未产出 issue 时使用；若仅“未核实”，用
// notifyQuickCreateUnconfirmed，别把不确定的失败当确定失败告诉用户。
func (s *TaskService) notifyQuickCreateFailed(ctx context.Context, task db.AgentTaskQueue, qc QuickCreateContext, errMsg string) {
	if errMsg == "" {
		errMsg = "Quick create did not finish successfully"
	}
	s.writeQuickCreateOutcomeInbox(ctx, task, qc, inboxTypeQuickCreateFailed, "Quick create failed", errMsg)
}

// quickCreateUnconfirmedDetail 是“结果无法核实”的 quick-create 运行的用户可见英文文案。不得声称失败：agent
// 很可能已创建 issue。它指向唯一安全的下一步——重试前先检查，避免重试静默制造守卫本要防止的重复。
const quickCreateUnconfirmedDetail = "Couldn't confirm whether the issue was created. Check your recent issues before retrying — creating it again may produce a duplicate."

// notifyQuickCreateUnconfirmed 为结果未核实的 quick-create 运行写入一条【中性】终态通知。任务已完成、没有
// 任何东西会重跑这次调和，因此不写就返回会让请求者完全没有收件箱结果——这正是本函数要防的失败形态。
//
// 它用自己的收件箱类型而非复用 quick_create_failed：每个客户端都会给 failed 类型加 "Failed:" 前缀，
// 无论标题与正文多中性都会断言我们未观察到的失败。Severity 保持 action_required，因为用户确实需要查看。
func (s *TaskService) notifyQuickCreateUnconfirmed(ctx context.Context, task db.AgentTaskQueue, qc QuickCreateContext) {
	s.writeQuickCreateOutcomeInbox(ctx, task, qc, inboxTypeQuickCreateUnconfirmed, "Quick create needs a check", quickCreateUnconfirmedDetail)
}

// quickCreateNotifyTimeout 限制 writeQuickCreateOutcomeInbox 里分离的终态通知写入时长：足以完成一次健康
// DB 往返，又短到卡死的连接池不会钉住完成 goroutine。
const quickCreateNotifyTimeout = 5 * time.Second

// writeQuickCreateOutcomeInbox 写入两种非成功结果（已知失败与未核实）共用的收件箱行。调用方掌管面向用户的
// 措辞与行类型；行结构——原始提示词、agent id、脱敏消息——一致，前端恢复入口对两者都可用。
func (s *TaskService) writeQuickCreateOutcomeInbox(ctx context.Context, task db.AgentTaskQueue, qc QuickCreateContext, inboxType, title, errMsg string) {
	s.persistQuickCreateOutcome(ctx, task, qc, inboxType, title, errMsg, nil)
}

// publishQuickCreateInbox 发出 WS 事件，让请求者的收件箱列表立即更新。载荷形状镜像其它收件箱监听者
// （notification_listeners.go）。
func (s *TaskService) publishQuickCreateInbox(item db.InboxItem, workspaceID, agentID, issueStatus string) {
	resp := map[string]any{
		"id":             util.UUIDToString(item.ID),
		"workspace_id":   util.UUIDToString(item.WorkspaceID),
		"recipient_type": item.RecipientType,
		"recipient_id":   util.UUIDToString(item.RecipientID),
		"type":           item.Type,
		"severity":       item.Severity,
		"issue_id":       util.UUIDToPtr(item.IssueID),
		"title":          item.Title,
		"body":           util.TextToPtr(item.Body),
		"read":           item.Read,
		"archived":       item.Archived,
		"created_at":     util.TimestampToString(item.CreatedAt),
		"actor_type":     util.TextToPtr(item.ActorType),
		"actor_id":       util.UUIDToPtr(item.ActorID),
		"details":        json.RawMessage(item.Details),
		"issue_status":   issueStatus,
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventInboxNew,
		WorkspaceID: workspaceID,
		ActorType:   "agent",
		ActorID:     agentID,
		Payload:     map[string]any{"item": resp},
	})
}

// agentToMap 构建用于广播 agent 状态更新的简单 map。
func agentToMap(a db.Agent) map[string]any {
	var rc any
	if a.RuntimeConfig != nil {
		json.Unmarshal(a.RuntimeConfig, &rc)
	}
	return map[string]any{
		"id":                   util.UUIDToString(a.ID),
		"workspace_id":         util.UUIDToString(a.WorkspaceID),
		"runtime_id":           util.UUIDToString(a.RuntimeID),
		"name":                 a.Name,
		"description":          a.Description,
		"avatar_url":           util.TextToPtr(a.AvatarUrl),
		"runtime_mode":         a.RuntimeMode,
		"runtime_config":       rc,
		"visibility":           a.Visibility,
		"status":               a.Status,
		"max_concurrent_tasks": a.MaxConcurrentTasks,
		"owner_id":             util.UUIDToPtr(a.OwnerID),
		"skills":               []any{},
		"created_at":           util.TimestampToString(a.CreatedAt),
		"updated_at":           util.TimestampToString(a.UpdatedAt),
		"archived_at":          util.TimestampToPtr(a.ArchivedAt),
		"archived_by":          util.UUIDToPtr(a.ArchivedBy),
	}
}

// createAgentCommentTx 将评论、线程状态和通知事件写入调用者的事务。
func createAgentCommentTx(ctx context.Context, qtx *db.Queries, issueID, agentID pgtype.UUID, content, commentType string, parentID, sourceTaskID pgtype.UUID) ([]events.Event, error) {
	// 与插入同一事务内解析 issue 与线程根。存储的 parent_id 始终是正被回复的那条确切评论。
	issue, err := qtx.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	var rootComment *db.Comment
	if parentID.Valid {
		if root, rootErr := qtx.GetThreadRoot(ctx, db.GetThreadRootParams{
			CommentID:   parentID,
			WorkspaceID: issue.WorkspaceID,
		}); rootErr == nil {
			rootComment = &root
		}
	}
	created, err := qtx.CreateComment(ctx, db.CreateCommentParams{
		ID:           dbid.NewV7(),
		IssueID:      issueID,
		WorkspaceID:  issue.WorkspaceID,
		AuthorType:   "agent",
		AuthorID:     agentID,
		Content:      content,
		Type:         commentType,
		ParentID:     parentID,
		SourceTaskID: sourceTaskID,
	})
	if err != nil {
		return nil, err
	}
	comment := created.Comment()
	commentFields := commentEventFields(comment)
	commentFields["revision"] = comment.Revision
	createdEvent, err := recordDurableEventTx(ctx, qtx, events.Event{
		Type:           protocol.EventCommentCreated,
		IdempotencyKey: "comment:created:" + util.UUIDToString(comment.ID),
		StreamKey:      "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID:    util.UUIDToString(issue.WorkspaceID),
		ActorType:      "agent",
		ActorID:        util.UUIDToString(agentID),
		Payload: map[string]any{
			"comment":        commentFields,
			"issue_title":    issue.Title,
			"issue_status":   issue.Status,
			"issue_revision": created.IssueRevision,
		},
	})
	if err != nil {
		return nil, err
	}
	var unresolvedEvent events.Event
	var reopened bool
	if rootComment != nil {
		unresolvedEvent, reopened, err = UnresolveThreadOnReply(ctx, qtx, rootComment,
			util.UUIDToString(issue.WorkspaceID), "agent", util.UUIDToString(agentID))
		if err != nil {
			return nil, err
		}
		if reopened {
			unresolvedEvent, err = recordDurableEventTx(ctx, qtx, unresolvedEvent)
			if err != nil {
				return nil, err
			}
		}
	}
	result := []events.Event{createdEvent}
	if reopened {
		result = append(result, unresolvedEvent)
	}
	return result, nil
}

// writeIssueCompletionReply 与任务完成原子提交，重试不会丢失或重复最终回复。
func (s *TaskService) writeIssueCompletionReply(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, result []byte) ([]events.Event, error) {
	suppress, err := HasSquadLeaderNoActionEvaluationForTask(ctx, qtx, task)
	if err != nil {
		return nil, err
	}
	if suppress {
		return nil, nil
	}
	commented, err := qtx.HasAgentCommentedForTask(ctx, db.HasAgentCommentedForTaskParams{
		IssueID: task.IssueID, AuthorID: task.AgentID, Since: task.StartedAt, TaskID: task.ID,
	})
	if err != nil {
		return nil, err
	}
	if commented {
		return nil, nil
	}
	var payload protocol.TaskCompletedPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, err
	}
	body := util.UnescapeBackslashEscapes(payload.Output)
	if strings.TrimSpace(body) == "" || (task.TriggerCommentID.Valid && isTrivialDoneOutput(body)) {
		return nil, nil
	}
	content := truncateFallbackCommentBody(redact.Text(body), maxSynthesizedFallbackCommentRunes)
	return createAgentCommentTx(ctx, qtx, task.IssueID, task.AgentID, content, "comment", task.TriggerCommentID, task.ID)
}
