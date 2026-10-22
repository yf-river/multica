"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronRight, Loader2, RotateCcw, Square } from "lucide-react";
import { toast } from "sonner";
import { api, dispatchReasonCode } from "@multica/core/api";
import { issueKeys } from "@multica/core/issues/queries";
import { useCustomPricingStore } from "@multica/core/runtimes/custom-pricing-store";
import type { AgentTask } from "@multica/core/types";
import { useTimeAgo } from "../../i18n";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { ActorAvatar } from "../../common/actor-avatar";
import { formatDuration } from "../../agents/components/agent-activity-hover-content";
import { CodeDiffCard } from "../../common/task-transcript/code-diff-card";
import { TranscriptButton } from "../../common/task-transcript";
import { cancelReasonLabel, failureReasonLabel } from "../../agents/components/tabs/task-failure";
import { useT } from "../../i18n";
import {
  formatTokens,
  formatUsd,
  summarizeTaskUsage,
  summarizeTaskUsageAcross,
} from "../../runtimes/utils";
import { TerminateTaskConfirmDialog } from "./terminate-task-confirm-dialog";
import { IssueUsageDialog } from "./issue-usage-dialog";
import { TaskStatusIcon } from "./task-status-icon";
import { useStatusLabel, useTriggerText } from "./task-run-labels";

// 本文件：ExecutionLogSection —— 问题详情右侧面板"执行日志"区段。
// 列出本问题的每一次 agent 运行。活跃运行置顶（存在时始终可见）；历史
// 运行（终态）收进"显示历史运行 (N)"开关后面。
//
// 取代对象：
//   - 正文内 live 卡里那个"点击展开"的时间线（"agent 正在工作"的 live
//     信号现在由头部 IssueAgentHeaderChip 承载）
//   - 主内容下方的独立 <TaskRunHistory>
//
// 行布局——朴素的左/右 flex：
//   1. Agent 头像（无状态点——agent 可用性不是这里的重点；行右列
//      承载任务状态）
//   2. 触发描述 flex 撑开并可截断
//   3. 状态是普通 shrink-0 右列；悬停时原位被动作按钮替换（状态被移除，
//      不是被盖住）。左侧文字保持 flex-1，行内永不出现在中间留缝。
//      不要在这里玩遮罩/内边距体操。
//
// 一次查询（`listTasksByIssue`）同时驱动两个桶——后端返回所有状态，
// 前端在客户端过滤成"活跃 vs 历史"。本问题的 WS task:* 事件会触发
// invalidate，列表因此无需轮询即可更新。

interface ExecutionLogSectionProps {
  issueId: string;
  /** 显示在用量弹窗的副标题里，让面板说明自己合计的是谁。 */
  identifier?: string;
}

// 历史运行排序优先级：按时间戳新的在前。两条运行同时间戳时，failed 排在
// cancelled 前、cancelled 排在 completed 前。
const PAST_STATUS_RANK: Record<string, number> = {
  failed: 0,
  cancelled: 1,
  completed: 2,
};

/**
 * 执行日志主区段：活跃运行置顶 + 可折叠的历史运行（职责见文件头）。
 */
export function ExecutionLogSection({ issueId, identifier }: ExecutionLogSectionProps) {
  const { t } = useT("issues");
  const [open, setOpen] = useState(true);
  const [showPast, setShowPast] = useState(false);
  const [usageOpen, setUsageOpen] = useState(false);

  // 缓存键注册在 `issueKeys.tasks`（packages/core/issues/queries.ts），
  // 全局 useRealtimeSync 的 `task:` 前缀路径通过 `["issues", "tasks"]`
  // 前缀匹配 invalidate 它——无需本地 WS 订阅，且即使本组件未挂载
  // （例如用户从 agent 侧取消、随后导航到这里）缓存也保持新鲜。
  const { data: tasks = [] } = useQuery({
    queryKey: issueKeys.tasks(issueId),
    queryFn: () => api.listTasksByIssue(issueId),
    staleTime: 30_000,
    refetchOnWindowFocus: true,
  });

  const activeTasks = useMemo(
    () =>
      tasks.filter(
        (t) =>
          t.status === "queued" ||
          t.status === "dispatched" ||
          // 被 daemon 因 busy local_directory 停放的任务——仍然活跃
          //（在等一个路径锁），不是终态。把它列在这里，正是告诉用户
          // agent 还活着、会继续跑。
          t.status === "waiting_local_directory" ||
          t.status === "running",
      ),
    [tasks],
  );

  const pastTasks = useMemo(() => {
    const past = tasks.filter(
      (t) =>
        t.status === "completed" ||
        t.status === "failed" ||
        t.status === "cancelled",
    );
    return past.toSorted((a, b) => {
      const at = a.completed_at ?? a.created_at;
      const bt = b.completed_at ?? b.created_at;
      const timeDiff = new Date(bt).getTime() - new Date(at).getTime();
      if (timeDiff !== 0) return timeDiff;
      return (
        (PAST_STATUS_RANK[a.status] ?? 99) -
        (PAST_STATUS_RANK[b.status] ?? 99)
      );
    });
  }, [tasks]);

  if (activeTasks.length === 0 && pastTasks.length === 0) return null;

  return (
    // `@container/execution-log`：头部三项只有超过某宽度才能并排，而决定
    // 这一宽度的是侧栏本身——一个可缩放 260–420px 的面板——不是视口。
    // 该容器驱动的分层参见 IssueUsageTotal。
    <div className="@container/execution-log">
      {/* 头部是两个互不隶属的目标，不是一个：标签 + 箭头折叠区段，
          右侧合计打开用量明细。按钮嵌按钮是非法的 HTML，
          所以它们是 flex 行里的兄弟，而不是按钮包按钮。 */}
      <div className="mb-2 flex w-full items-center gap-1">
        <button
          type="button"
          className={`flex min-w-0 items-center gap-1 whitespace-nowrap rounded-md px-2 py-1 text-caption font-medium transition-colors hover:bg-accent/70 ${
            open ? "" : "text-muted-foreground hover:text-foreground"
          }`}
          onClick={() => setOpen(!open)}
        >
          {/* 区段标签是这里唯一可收缩的一项，所以它带着 nowrap + 省略号对。
              没有它，被挤压的按钮把"执行日志"折成两行（MUL-5804）——
              区段标题换行是布局 bug，不是窄列该有的样子。下面的分层让省略号
              在面板 260px 最小值时永不出现在；它是更长译文的后备，
              不是日常状态。 */}
          <span className="truncate">{t(($) => $.execution_log.section)}</span>
          <ChevronRight
            className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${
              open ? "rotate-90" : ""
            }`}
          />
        </button>
        {activeTasks.length > 0 && (
          <span className="ml-auto inline-flex shrink-0 items-center gap-1 text-info">
            <span className="h-1.5 w-1.5 rounded-full bg-info animate-pulse" />
            <span className="font-mono text-caption tabular-nums">{activeTasks.length}</span>
          </span>
        )}
        <IssueUsageTotal
          tasks={tasks}
          alone={activeTasks.length === 0}
          onOpen={() => setUsageOpen(true)}
        />
      </div>
      {open && (
        <div className="space-y-0.5 pl-2">
          {activeTasks.map((task) => (
            <ActiveTaskRow key={task.id} task={task} issueId={issueId} />
          ))}

          {pastTasks.length > 0 && (
            <>
              {activeTasks.length > 0 && (
                <div className="my-1.5 border-t border-border/60" />
              )}
              <button
                type="button"
                onClick={() => setShowPast(!showPast)}
                className="flex w-full items-center gap-1 rounded px-1 py-1 text-caption text-muted-foreground transition-colors hover:bg-accent/40 hover:text-foreground"
              >
                <ChevronRight
                  className={`!size-3 shrink-0 stroke-[2.5] transition-transform ${
                    showPast ? "rotate-90" : ""
                  }`}
                />
                {showPast
                  ? t(($) => $.execution_log.hide_past, { count: pastTasks.length })
                  : t(($) => $.execution_log.show_past, { count: pastTasks.length })}
              </button>
              {showPast && (
                <div className="mt-0.5 space-y-0.5">
                  {pastTasks.map((task) => (
                    <PastRow key={task.id} task={task} issueId={issueId} />
                  ))}
                </div>
              )}
            </>
          )}
        </div>
      )}
      <IssueUsageDialog
        open={usageOpen}
        onOpenChange={setUsageOpen}
        identifier={identifier ?? ""}
        tasks={tasks}
      />
    </div>
  );
}

// ─── 本问题用量合计 ─────────────────────────────────────────────────────────

// 整条问题的全部花费，作为头部一个触手可及的物件："2.1M · $4.92"。
// 不展开任何东西就回答了"这个问题花了多少"，也是按次明细的入口。
//
// 本问题的所有运行都没记录用量时什么都不渲染——那些运行全部早于用量上报的
// 问题，会拿回它原来的旧头部，而不是出现一个会被读成"这是免费的"
// 的"0 · $0.00"。
//
// 窄区段会丢掉 token 数字只留金额。窄端总得让点什么——头部完整形态在
// 活跃运行 chip 旁需要约 246px，而侧栏 260px 最小值只剩 228px——
// token 数是缺失代价最小的一块：金额回答了"这个问题花了多少钱"，
// 精确的 token 拆分点一下就开。它是一个会退让的数字，而不是数字的位数：
// 被截断的"$31.1…"会读成与问题实际花费不同的数字。
/**
 * 用量合计触发器（打开按次用量弹窗）。是"已花多少"的一眼入口。
 */
export function IssueUsageTotal({
  tasks,
  alone,
  onOpen,
}: {
  tasks: AgentTask[];
  alone: boolean;
  onOpen: () => void;
}) {
  const { t } = useT("issues");
  // 自定义费率是在 `estimateCost` 内部命令式读取的，所以费率的保存不会
  // 自己触发这里重渲染——订阅并把 memo 依赖在快照上，否则头部合计会
  // 一直引用旧价格直到任务列表重新拉取。
  const pricings = useCustomPricingStore((s) => s.pricings);
  const total = useMemo(
    () => summarizeTaskUsageAcross(tasks.map((task) => task.usage)),
    [tasks, pricings],
  );
  if (!total) return null;

  // 两个阈值是因为头部有两种形态，而分层只该在行真正挤爆的那一档让读者
  // 失去一个数字：活跃运行 chip 旁完整形态需要约 246px、单独约 218px。
  // 写成整段字面类——Tailwind 扫的是源码文本，拼出来的字符串两个都不会
  // 生成。用 `@max-…`（而不是在 `@min-…` 显示）让在本区段 `@container`
  // 之外渲染的宿主降级到完整形态，而不是永远丢 token。
  const narrowTier = alone
    ? "@max-[14rem]/execution-log:hidden"
    : "@max-[16rem]/execution-log:hidden";

  return (
    <Tooltip>
      <TooltipTrigger
        render={<button type="button" onClick={onOpen} />}
        className={`flex shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 text-caption tabular-nums transition-colors hover:bg-accent/70 ${
          alone ? "ml-auto" : ""
        }`}
      >
        <span className={`font-medium ${narrowTier}`}>
          {formatTokens(total.tokens)}
        </span>
        <span className={`text-faint-foreground ${narrowTier}`}>·</span>
        <span className="text-muted-foreground">{formatUsd(total.cost)}</span>
      </TooltipTrigger>
      <TooltipContent>{t(($) => $.execution_log.usage_total_tooltip)}</TooltipContent>
    </Tooltip>
  );
}

// 触发描述与状态标签放在 ./task-run-labels——这样用量弹窗列出的运行
// 与本区段展示的是同一套说法。

// ─── 行视觉配置 ─────────────────────────────────────────────────────────

const STATUS_TONE: Record<AgentTask["status"], string> = {
  queued: "text-warning",
  dispatched: "text-warning",
  // 与 queued/dispatched 同色——视觉上"停住"让用户看出任务被停放，
  // 但由状态标签加以区分。
  waiting_local_directory: "text-warning",
  running: "text-info",
  completed: "text-success",
  failed: "text-destructive",
  cancelled: "text-muted-foreground",
};

// ─── 活跃行 ────────────────────────────────────────────────────────────────

// 一条活跃（running / queued / dispatched / 停放）任务行。运行中的行只把
// 状态收敛成一个实时跳动的计时器；记录与停止作为悬停动作仍然可用。
// 记录内容经 TranscriptButton 点击时懒加载，所以本行不再为渲染计数而
// 预取任务消息。
/**
 * 活跃任务行（含取消按钮与确认弹窗）。
 */
export function ActiveTaskRow({
  task,
  issueId,
  onTranscriptOpenChange,
}: {
  task: AgentTask;
  issueId: string;
  onTranscriptOpenChange?: (open: boolean) => void;
}) {
  const { t } = useT("issues");
  const [cancelling, setCancelling] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const tone = STATUS_TONE[task.status];
  const label = useStatusLabel(task.status, task.failure_reason);
  const trigger = useTriggerText(task);

  // 运行中的行显示实时跳动的耗时（跳动的数字承载"活着"，时长承载
  // "跑了多久"）。只有运行中的行才跳。
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (task.status !== "running") return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [task.status]);
  const elapsed =
    task.status === "running"
      ? formatDuration(
          task.started_at ?? task.dispatched_at ?? task.created_at,
          now,
        )
      : "";

  // 只有存在消息时记录才有意义——纯 queued 与 waiting_local_directory
  // 的任务还没流出任何 agent 输出。
  const showTranscript =
    task.status !== "queued" && task.status !== "waiting_local_directory";

  const handleCancel = async () => {
    if (cancelling) return;
    setCancelling(true);
    try {
      await api.cancelTask(issueId, task.id);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.execution_log.cancel_failed));
      setCancelling(false);
    }
  };

  const requestCancel = () => {
    if (cancelling) return;
    setConfirmOpen(true);
  };

  // 活跃行刻意没有 token 数字：daemon 只在 `runner.run` 返回后上报一次用量
  // （server/internal/daemon/daemon.go），且该写入不发布实时事件——所以运行中
  // 的任务没有用量可显示，即便有，中途也收不到更新。硬渲染这个分支只可能被
  // 手写的夹具命中，而那等于在测一个生产环境造不出来的场景。要恢复它，
  // 请与"增量上报 + 缓存失效"的改动同批做。
  return (
    <RowShell task={task}>
      <TriggerText text={trigger} />
      <TaskCommentCoverage task={task} />
      <RowStatus title={label}>
        {task.status === "running" ? (
          <>
            <span className="text-info tabular-nums">{elapsed}</span>
            <span className="sr-only">{label}</span>
          </>
        ) : (
          <span className={`${tone} min-w-0 truncate`}>{label}</span>
        )}
      </RowStatus>
      <RowActions>
        {showTranscript && (
          <TranscriptButton
            task={task}
            agentName=""
            isLive={task.status === "running"}
            title={t(($) => $.execution_log.transcript_tooltip)}
            onOpenChange={onTranscriptOpenChange}
          />
        )}
        <Tooltip>
          <TooltipTrigger
            render={
              <button
                type="button"
                onClick={requestCancel}
                disabled={cancelling}
                aria-label={t(($) => $.execution_log.cancel_task_aria)}
              />
            }
            className="flex items-center justify-center rounded p-1 text-destructive transition-colors hover:bg-destructive/10 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {cancelling ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Square className="h-3.5 w-3.5" />
            )}
          </TooltipTrigger>
          <TooltipContent>{t(($) => $.execution_log.cancel_task_tooltip)}</TooltipContent>
        </Tooltip>
      </RowActions>
      <TerminateTaskConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        onConfirm={() => void handleCancel()}
        showRunningNote={
          task.status === "running" ||
          task.status === "dispatched" ||
          task.status === "waiting_local_directory"
        }
      />
    </RowShell>
  );
}

// ─── 历史行 ────────────────────────────────────────────────────────────────

/**
 * 历史任务行（终态）：失败/取消原因 + 重试按钮。
 */
function PastRow({ task, issueId }: { task: AgentTask; issueId: string }) {
  const { t } = useT("issues");
  const { t: tAgents } = useT("agents");
  const timeAgo = useTimeAgo();
  const [retrying, setRetrying] = useState(false);
  const label = useStatusLabel(task.status, task.failure_reason);
  const trigger = useTriggerText(task);
  const time = task.completed_at ? timeAgo(task.completed_at) : "—";
  // 失败运行总会自我解释。取消则只在该取消是 SERVER 因持久原因发起的
  //（worktree 认领门、保留工作交付）——用户主动取消保持朴素的"已取消"。
  const failureLabel =
    task.status === "failed"
      ? failureReasonLabel(task.failure_reason, tAgents)
      : cancelReasonLabel(task, tAgents);
  // 悬停状态标记展示本地化的原因，绝不展示原始 `task.error`。那个字段是
  // daemon 与服务端写给分类和日志用的、面向运维的英文散文（#7411）——
  // 把它贴进 tooltip，等于在出事的瞬间让每个非英文工作区都读英文，
  // 还会把绝对的 worktree 路径与机器名拖进悬停文本和截图。完整诊断
  // 在记录的 Run 详情里一键可达。
  const statusTitle = failureLabel ?? label;

  // 运行花销放在相对时间戳曾占的槽位里。
  //
  // 侧栏 288px，行上已经有头像、触发文本与状态标记；第三列会直接挤掉
  // 触发文本——而那是人们扫这个列表时最想看的。列表按最新在前排序，
  // "哪次运行在先"已由位置表达——精确的"何时"是细节，"花多少"是新问题。
  // 被挪走的时间戳连同时长与模型一起进了下面的行 tooltip，
  // 这些以前根本没在这里露出过。
  //
  // `null`（未记录用量）渲染成破折号，绝不渲染 0：用量上报之前的那次运行
  // 不是免费，只是我们手里没有它的数字。
  const usage = summarizeTaskUsage(task.usage);
  const rowTitle = [
    time,
    task.started_at && task.completed_at
      ? formatDuration(task.started_at, new Date(task.completed_at).getTime())
      : "",
    usage?.models.join(", ") ?? "",
  ]
    .filter(Boolean)
    .join(" · ");

  // 重试只对"终态但非成功"的行有意义。传 task.id 精确定位这一行的 agent——
  // 不带它，重跑接口会回退到问题的当前负责人，于是行上 agent 已被换掉时
  //（例如改派、squad worker、或一次性 @-mention agent）会触发错的那个 agent。
  const canRetry = task.failure_reason !== "release_pause" && (task.status === "failed" || task.status === "cancelled");

  const handleRetry = async () => {
    if (retrying) return;
    setRetrying(true);
    try {
      await api.rerunIssue(issueId, task.id);
    } catch (e) {
      // 重跑现在又被操作者的 invoke 权限重新门控（MUL-4525）：结构化的
      // 403 表示"这个 agent 不能被触发"，不是瞬时故障——本地化它，
      // 而不是把服务端的通用消息原样弹出。
      toast.error(
        dispatchReasonCode(e) === "invocation_not_allowed"
          ? t(($) => $.execution_log.retry_blocked)
          : e instanceof Error
            ? e.message
            : t(($) => $.execution_log.retry_failed),
      );
    } finally {
      // 成功失败都复位：历史行保持挂载（它的 task.id 没变），
      // 成功路径若把 `retrying` 留在 true，按钮会被永久钉成转圈。
      setRetrying(false);
    }
  };

  return (
    <div className="min-w-0 space-y-1">
    <RowShell task={task} title={rowTitle}>
      <TriggerText text={trigger} />
      <TaskCommentCoverage task={task} />
      <RowStatus title={statusTitle}>
        <TaskStatusIcon status={task.status} />
        <span className="sr-only">
          {[failureLabel ?? label, time].filter(Boolean).join(" · ")}
        </span>
        {usage ? (
          <span className="tabular-nums">{formatTokens(usage.tokens)}</span>
        ) : (
          <span className="text-faint-foreground">—</span>
        )}
      </RowStatus>
      <RowActions>
        <TranscriptButton task={task} agentName="" title={t(($) => $.execution_log.transcript_tooltip)} />
        {canRetry && (
          <Tooltip>
            <TooltipTrigger
              render={
                <button
                  type="button"
                  onClick={handleRetry}
                  disabled={retrying}
                  aria-label={t(($) => $.execution_log.retry_task_aria)}
                />
              }
              className="flex items-center justify-center rounded p-1 text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50"
            >
              {retrying ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <RotateCcw className="h-3.5 w-3.5" />
              )}
            </TooltipTrigger>
            <TooltipContent>{t(($) => $.execution_log.retry_task_tooltip)}</TooltipContent>
          </Tooltip>
        )}
      </RowActions>
    </RowShell>
    <CodeDiffCard result={task.result} />
    </div>
  );
}

// ─── 行公共外壳 ─────────────────────────────────────────────────────────

/**
 * 行外壳：头像 + 内容 + tooltip 承载。子组件按活跃/历史形态注入。
 */
function RowShell({
  task,
  title,
  children,
}: {
  task: AgentTask;
  /** 承载右列再也放不下的细节（时间、时长、模型）。放在行上而非 RowStatus
   *  上，因为 RowStatus 悬停时会被动作按钮换掉——那里的 title 会在指针
   *  恰好到达的那一刻消失。 */
  title?: string;
  children: React.ReactNode;
}) {
  return (
    <div
      title={title || undefined}
      className="group/execution-log-row flex items-center gap-2 overflow-hidden rounded px-1 py-1.5 transition-colors hover:bg-accent/40"
    >
      {task.agent_id ? (
        <ActorAvatar
          actorType="agent"
          actorId={task.agent_id}
          size="sm"
          enableHoverCard
        />
      ) : (
        <span className="inline-block h-5 w-5 shrink-0 rounded-full bg-muted" />
      )}
      {children}
    </div>
  );
}

function TriggerText({ text }: { text: string }) {
  return <span className="min-w-0 flex-1 truncate text-caption text-muted-foreground">{text}</span>;
}

/**
 * 该任务状态是否支持"包含评论数"徽标。
 */
function supportsCommentCoverage(status: AgentTask["status"]): boolean {
  switch (status) {
    case "queued":
    case "dispatched":
    case "waiting_local_directory":
    case "running":
    case "completed":
    case "failed":
    case "cancelled":
      return true;
    default:
      return false;
  }
}

/**
 * "包含 N 条评论"标签：说明这次运行把哪些评论捎带进了上下文。
 * 只有覆盖 > 1 条评论时渲染。
 */
export function TaskCommentCoverage({ task }: { task: AgentTask }) {
  const { t } = useT("issues");
  if (!supportsCommentCoverage(task.status)) return null;

  // 排队中的行展示计划覆盖：coalesced_comment_ids 刻意不含最新触发器。
  // 一旦被认领，优先用服务端实际的投递回执。只有那些字段缺失的旧行
  // 才回退到计划；显式的 [] 表示认领没有投递任何评论。
  const plannedCommentIds = [
    task.trigger_comment_id,
    ...(task.coalesced_comment_ids ?? []),
  ];
  const coverageIds =
    task.status !== "queued" && task.delivered_comment_ids !== undefined
      ? task.delivered_comment_ids
      : plannedCommentIds;
  const commentIds = new Set(
    coverageIds.filter((id): id is string => Boolean(id)),
  );
  if (commentIds.size <= 1) return null;

  return (
    <span className="shrink-0 whitespace-nowrap text-micro text-muted-foreground">
      {t(($) => $.execution_log.included_comments, { count: commentIds.size })}
    </span>
  );
}

function RowStatus({
  children,
  title,
}: {
  children: React.ReactNode;
  title?: string;
}) {
  return (
    <div
      title={title}
      className="flex h-7 shrink-0 items-center justify-end gap-1 overflow-hidden whitespace-nowrap text-caption [@media(hover:hover)]:group-hover/execution-log-row:hidden"
    >
      {children}
    </div>
  );
}

// 动作槽——触屏设备默认可见。在支持悬停的表面，行悬停时原位替换状态列。
function RowActions({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-7 items-center gap-0.5 [@media(hover:hover)]:hidden [@media(hover:hover)]:group-hover/execution-log-row:flex">
      {children}
    </div>
  );
}
