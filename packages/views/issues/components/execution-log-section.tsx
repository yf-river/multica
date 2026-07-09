"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronRight, Loader2, Square } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { issueKeys, issueExecutionTreeOptions, issueTaskTraceOptions, issueSOPRunsOptions } from "@multica/core/issues/queries";
import { useWorkspacePaths } from "@multica/core/paths";
import type { AgentTask, IssueExecutionTreeResponse, TaskTraceEvent } from "@multica/core/types";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { ActorAvatar } from "../../common/actor-avatar";
import { formatDuration } from "../../agents/components/agent-activity-hover-content";
import { TranscriptButton } from "../../common/task-transcript";
import { useT } from "../../i18n";
import { usageTokenTotal } from "../../runtimes/utils";
import { TerminateTaskConfirmDialog } from "./terminate-task-confirm-dialog";

// Right-panel section for the issue's live/debug surface. Active runs stay
// visible with recent events; terminal runs are summarized into one compact
// run-review card and the full audit remains on the run review page.
//
// Replaces:
//   - the click-to-expand timeline that used to live inside the in-body live
//     card (the live "agent is working" signal now lives in the header via
//     IssueAgentHeaderChip)
//   - the standalone <TaskRunHistory> below the main content
//
// Row layout — simple left/right flex:
//   1. Agent avatar (no status dot — agent availability is not the
//      story here; the row's right column carries the task status)
//   2. Trigger description flexes and truncates
//   3. Status is a normal shrink-0 right column; on hover it is replaced
//      in place by the action buttons (status is removed, not covered).
//      Left text keeps flex-1 so the row never shows a mid-row gap. Do
//      not use masks/padding gymnastics here.
//
// One query (`listTasksByIssue`) drives both buckets — the back-end
// returns every status, the front-end filters into active vs past on the
// client. WS task:* events for this issue trigger an invalidate so the
// list updates without polling.

interface ExecutionLogSectionProps {
  issueId: string;
}

export function ExecutionLogSection({ issueId }: ExecutionLogSectionProps) {
  const { t } = useT("issues");
  const [open, setOpen] = useState(true);

  // Cache key registered in `issueKeys.tasks` (packages/core/issues/queries.ts)
  // so the global useRealtimeSync `task:` prefix path invalidates it via
  // a `["issues", "tasks"]` prefix-match — no local WS subscriptions
  // needed, and the cache stays fresh even when this component isn't
  // mounted (e.g. user cancels from agent-side, then navigates here).
  const { data: tasks = [] } = useQuery({
    queryKey: issueKeys.tasks(issueId),
    queryFn: () => api.listTasksByIssue(issueId),
    staleTime: 30_000,
    refetchOnWindowFocus: true,
  });
  const { data: traceData } = useQuery(issueTaskTraceOptions(issueId));
  const traceEvents = traceData?.events ?? [];
  const { data: sopData } = useQuery(issueSOPRunsOptions(issueId));
  const sopRuns = sopData?.items ?? [];
  const { data: executionTree } = useQuery(issueExecutionTreeOptions(issueId));
  const allTasks = useMemo(
    () => mergeTasks(tasks, collectExecutionTreeTasks(executionTree)),
    [tasks, executionTree],
  );
  const allTraceEvents = useMemo(
    () => mergeTraceEvents(traceEvents, collectExecutionTreeTraceEvents(executionTree)),
    [traceEvents, executionTree],
  );

  const activeTasks = useMemo(
    () =>
      tasks.filter(
        (t) =>
          t.status === "queued" ||
          t.status === "dispatched" ||
          // Daemon-parked task on a busy local_directory — still active
          // (waiting on a path lock), not terminal. Surfacing it here is
          // what tells the user the agent is alive and will resume.
          t.status === "waiting_local_directory" ||
          t.status === "running",
      ),
    [tasks],
  );

  const latestSOPRun = useMemo(() => sopRuns[0] ?? null, [sopRuns]);
  const currentStage = latestSOPRun?.current_step_key || "";
  const recentEvents = useMemo(
    () => buildMacroEvents(allTraceEvents, allTasks).slice(0, 5),
    [allTraceEvents, allTasks],
  );
  const executionSummary = useMemo(
    () => buildExecutionSummary(allTasks, allTraceEvents, sopRuns),
    [allTasks, allTraceEvents, sopRuns],
  );
  const shouldShowExecutionLog = activeTasks.length > 0;

  if (!shouldShowExecutionLog) return null;

  return (
    <div data-testid="issue-execution-log-section">
      <button
        type="button"
        className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-xs font-medium transition-colors mb-2 hover:bg-accent/70 ${
          open ? "" : "text-muted-foreground hover:text-foreground"
        }`}
        onClick={() => setOpen(!open)}
      >
        {t(($) => $.execution_log.section)}
        <ChevronRight
          className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${
            open ? "rotate-90" : ""
          }`}
        />
        {activeTasks.length > 0 ? (
          <span className="ml-auto inline-flex items-center gap-1 text-info">
            <span className="h-1.5 w-1.5 rounded-full bg-info animate-pulse" />
            <span className="font-mono tabular-nums">{activeTasks.length}</span>
          </span>
        ) : executionSummary.totalTasks > 0 ? (
          <span className="ml-auto font-mono text-[11px] tabular-nums text-muted-foreground">
            {executionSummary.totalTasks}
          </span>
        ) : null}
      </button>
      {open && (
        <div className="space-y-0.5 pl-2">
          {activeTasks.map((task) => (
            <ActiveTaskRow key={task.id} task={task} issueId={issueId} />
          ))}

          <ExecutionRunSummary
            currentStage={currentStage}
            summary={executionSummary}
            events={recentEvents}
          />
        </div>
      )}
    </div>
  );
}

export function IssueRunReviewSummaryCard({ issueId }: ExecutionLogSectionProps) {
  const { data: tasks = [] } = useQuery({
    queryKey: issueKeys.tasks(issueId),
    queryFn: () => api.listTasksByIssue(issueId),
    staleTime: 30_000,
    refetchOnWindowFocus: true,
  });
  const { data: traceData } = useQuery(issueTaskTraceOptions(issueId));
  const traceEvents = traceData?.events ?? [];
  const { data: executionTree } = useQuery(issueExecutionTreeOptions(issueId));
  const treeTasks = useMemo(() => collectExecutionTreeTasks(executionTree), [executionTree]);
  const terminalTasks = useMemo(
    () =>
      mergeTasks(tasks, treeTasks).filter(
        (t) =>
          t.status === "completed" ||
          t.status === "failed" ||
          t.status === "cancelled",
      ),
    [tasks, treeTasks],
  );
  const treeTraceEvents = useMemo(
    () => collectExecutionTreeTraceEvents(executionTree),
    [executionTree],
  );
  const reviewTraceEvents = useMemo(
    () => mergeTraceEvents(traceEvents, treeTraceEvents),
    [traceEvents, treeTraceEvents],
  );
  const shouldShowReviewCard =
    hasMeaningfulExecutionTree(executionTree) ||
    terminalTasks.length > 0 ||
    (tasks.length === 0 && traceEvents.length > 0);

  if (!shouldShowReviewCard) return null;

  return (
    <RunReviewSummaryCard
      issueId={issueId}
      tree={executionTree}
      terminalTasks={terminalTasks}
      traceEvents={reviewTraceEvents}
    />
  );
}

function RunReviewSummaryCard({
  issueId,
  tree,
  terminalTasks,
  traceEvents,
}: {
  issueId: string;
  tree: IssueExecutionTreeResponse | undefined;
  terminalTasks: AgentTask[];
  traceEvents: TaskTraceEvent[];
}) {
  const paths = useWorkspacePaths();
  const runReviewHref = `${paths.runReviews()}?issue=${encodeURIComponent(issueId)}`;
  const summary = tree?.issue_summary;
  const aggregate = tree?.summary ?? {};
  const failedTasks = terminalTasks.filter((task) => task.status === "failed");
  const cancelledTasks = terminalTasks.filter((task) => task.status === "cancelled");
  const tokenTotal = summary
    ? summary.total_input_tokens + summary.total_output_tokens + summary.total_cache_read_tokens + summary.total_cache_write_tokens
    : traceEvents.reduce((sum, event) => sum + traceEventTokenTotal(event), 0);
  const taskCount = Number(aggregate["任务数"] ?? terminalTasks.length);
  const anomalyCount =
    Number(aggregate["异常工具数"] ?? 0) + failedTasks.length + cancelledTasks.length;
  const failureSummary = summary?.failure_summary || latestTaskFailureSummary(failedTasks[0] ?? cancelledTasks[0]);
  return (
    <div className="rounded-md border border-info/30 bg-info/5 px-2 py-2 text-xs" data-testid="issue-run-review-summary-card">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="font-medium text-foreground">运行复盘</div>
          <div className="mt-1 grid gap-x-3 gap-y-0.5 text-[11px] leading-5 text-muted-foreground sm:grid-cols-2">
            <span className="truncate">验收：{summary?.acceptance_status ?? deriveAcceptanceStatus(terminalTasks)}</span>
            <span className="truncate">总耗时：{formatNullableMilliseconds(summary?.total_duration_ms)}</span>
            <span className="truncate">任务数：{taskCount}</span>
            <span className="truncate">异常数：{anomalyCount}</span>
            <span className="truncate sm:col-span-2">Token：{tokenTotal > 0 ? tokenTotal.toLocaleString() : "未记录"}</span>
          </div>
          {failureSummary && (
            <div className="mt-1 line-clamp-2 text-[11px] leading-5 text-destructive">
              {failureSummary}
            </div>
          )}
        </div>
        <div className="flex shrink-0 flex-wrap gap-1">
          <a
            className="rounded border bg-background px-2 py-1 text-[11px] hover:bg-accent"
            href={runReviewHref}
          >
            查看完整复盘
          </a>
        </div>
      </div>
    </div>
  );
}

function hasMeaningfulExecutionTree(tree: IssueExecutionTreeResponse | undefined): boolean {
  if (!tree?.root) return false;
  const summary = tree.summary ?? {};
  return (
    Number(summary["任务数"] ?? 0) > 0 ||
    Number(summary["子任务数"] ?? 0) > 0 ||
    Number(summary["SOP执行数"] ?? 0) > 0 ||
    Number(summary["观测事件数"] ?? 0) > 0 ||
    Number(summary["工具调用数"] ?? 0) > 0 ||
    Number(summary["唤醒评论数"] ?? 0) > 0 ||
    Number(tree.timeline_nodes?.length ?? 0) > 0
  );
}

interface ExecutionSummary {
  totalTasks: number;
  activeTasks: number;
  completedTasks: number;
  failedTasks: number;
  cancelledTasks: number;
  agentCount: number;
  firstClaimedAt: string;
  firstStartedAt: string;
  lastCompletedAt: string;
}

function ExecutionRunSummary({
  currentStage,
  summary,
  events,
}: {
  currentStage: string;
  summary: ExecutionSummary;
  events: MacroEvent[];
}) {
  return (
    <div className="rounded-md border border-border/70 bg-muted/25 px-2 py-1.5" data-testid="issue-active-run-signals">
      <div className="space-y-1">
        <div className="flex min-w-0 items-center justify-between gap-2 text-[11px] text-muted-foreground">
          <span>当前阶段</span>
          <span className="min-w-0 truncate text-foreground">{currentStage || "未记录"}</span>
        </div>
        <div className="grid gap-x-3 gap-y-0.5 text-[11px] leading-5 text-muted-foreground sm:grid-cols-2">
          <span className="truncate">Agent：{summary.agentCount > 0 ? summary.agentCount : "未记录"}</span>
          <span className="truncate">
            任务：{summary.totalTasks}
            {summary.activeTasks > 0 ? ` 进行中 ${summary.activeTasks}` : ""}
          </span>
          <span className="truncate">已完成：{summary.completedTasks}</span>
          <span className="truncate">
            异常：{summary.failedTasks + summary.cancelledTasks}
          </span>
          <span className="truncate">首次领取：{summary.firstClaimedAt || "未记录"}</span>
          <span className="truncate">首次开始：{summary.firstStartedAt || "未记录"}</span>
          <span className="truncate sm:col-span-2">最后完成：{summary.lastCompletedAt || "未记录"}</span>
        </div>
        {events.length > 0 && (
          <div className="space-y-0.5">
            <div className="text-[11px] text-muted-foreground">最近事件</div>
            {events.map((event) => (
              <RecentMacroEventRow key={event.id} event={event} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

interface MacroEvent {
  id: string;
  eventType: string;
  eventName: string;
  status: string;
  createdAt: string;
  failureSummary: string;
}

function RecentMacroEventRow({ event }: { event: MacroEvent }) {
  return (
    <div className="grid gap-0.5 border-l-2 border-info/60 pl-2 text-[11px] leading-5">
      <div className="flex min-w-0 items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-foreground">
          {event.eventName || traceEventStageLabel(event.eventType)}
        </span>
        <span className="shrink-0 text-muted-foreground">
          {traceEventStageLabel(event.eventType)}
        </span>
      </div>
      <div className="truncate text-muted-foreground">
        {[event.status || "未知", formatTimestamp(event.createdAt)].filter(Boolean).join(" · ")}
      </div>
      {event.failureSummary && (
        <div className="line-clamp-2 text-destructive">{event.failureSummary}</div>
      )}
    </div>
  );
}

function buildExecutionSummary(
  tasks: AgentTask[],
  traceEvents: TaskTraceEvent[],
  sopRuns: { started_at?: string; completed_at?: string | null }[],
): ExecutionSummary {
  const agentIds = new Set<string>();
  tasks.forEach((task) => {
    if (task.agent_id) agentIds.add(task.agent_id);
  });
  traceEvents.forEach((event) => {
    if (event.agent_id) agentIds.add(event.agent_id);
  });

  const activeTasks = tasks.filter((task) => isActiveStatus(task.status)).length;
  const firstClaimedAt = earliest(
    tasks.map((task) => task.dispatched_at),
    traceEvents
      .filter((event) => event.event_type === "task.dispatched")
      .map((event) => event.created_at),
  );
  const firstStartedAt = earliest(
    tasks.map((task) => task.started_at),
    sopRuns.map((run) => run.started_at ?? null),
    traceEvents
      .filter((event) => event.event_type === "task.started")
      .map((event) => event.created_at),
  );
  const lastCompletedAt = latest(
    tasks.map((task) => task.completed_at),
    sopRuns.map((run) => run.completed_at ?? null),
    traceEvents
      .filter((event) => event.event_type === "task.completed")
      .map((event) => event.created_at),
  );

  return {
    totalTasks: tasks.length,
    activeTasks,
    completedTasks: tasks.filter((task) => task.status === "completed").length,
    failedTasks: tasks.filter((task) => task.status === "failed").length,
    cancelledTasks: tasks.filter((task) => task.status === "cancelled").length,
    agentCount: agentIds.size,
    firstClaimedAt: formatTimestamp(firstClaimedAt),
    firstStartedAt: formatTimestamp(firstStartedAt),
    lastCompletedAt: formatTimestamp(lastCompletedAt),
  };
}

function buildMacroEvents(traceEvents: TaskTraceEvent[], tasks: AgentTask[]): MacroEvent[] {
  const fromTrace = traceEvents
    .filter((event) => MACRO_TRACE_EVENT_TYPES.has(event.event_type))
    .map((event) => ({
      id: event.id,
      eventType: event.event_type,
      eventName: event.event_name,
      status: event.status,
      createdAt: event.created_at,
      failureSummary: traceFailureSummary(event),
    }));

  if (fromTrace.length > 0) {
    return fromTrace.sort((a, b) => compareTimestampDesc(a.createdAt, b.createdAt));
  }

  return tasks
    .flatMap((task) => syntheticTaskEvents(task))
    .sort((a, b) => compareTimestampDesc(a.createdAt, b.createdAt));
}

function syntheticTaskEvents(task: AgentTask): MacroEvent[] {
  return [
    task.created_at
      ? {
          id: `${task.id}:queued`,
          eventType: "task.queued",
          eventName: "任务已入队",
          status: "queued",
          createdAt: task.created_at,
          failureSummary: "",
        }
      : null,
    task.dispatched_at
      ? {
          id: `${task.id}:dispatched`,
          eventType: "task.dispatched",
          eventName: "任务已领取",
          status: "dispatched",
          createdAt: task.dispatched_at,
          failureSummary: "",
        }
      : null,
    task.started_at
      ? {
          id: `${task.id}:started`,
          eventType: "task.started",
          eventName: "任务已开始",
          status: "running",
          createdAt: task.started_at,
          failureSummary: "",
        }
      : null,
    task.completed_at
      ? {
          id: `${task.id}:${task.status}`,
          eventType: terminalTaskEventType(task.status),
          eventName: terminalTaskEventName(task.status),
          status: task.status,
          createdAt: task.completed_at,
          failureSummary: latestTaskFailureSummary(task),
        }
      : null,
  ].filter((event): event is MacroEvent => event !== null);
}

const MACRO_TRACE_EVENT_TYPES = new Set([
  "task.queued",
  "task.dispatched",
  "task.started",
  "task.waiting_local_directory",
  "task.completed",
  "task.failed",
  "task.cancelled",
  "user_input.received",
]);

function isActiveStatus(status: AgentTask["status"]): boolean {
  return (
    status === "queued" ||
    status === "dispatched" ||
    status === "waiting_local_directory" ||
    status === "running"
  );
}

function terminalTaskEventType(status: AgentTask["status"]): string {
  if (status === "failed") return "task.failed";
  if (status === "cancelled") return "task.cancelled";
  return "task.completed";
}

function terminalTaskEventName(status: AgentTask["status"]): string {
  if (status === "failed") return "任务已失败";
  if (status === "cancelled") return "任务已取消";
  return "任务已完成";
}

function collectExecutionTreeTasks(tree: IssueExecutionTreeResponse | undefined): AgentTask[] {
  if (!tree?.root) return [];
  const out: AgentTask[] = [];
  walkExecutionTree(tree.root, (node) => out.push(...node.tasks));
  return out;
}

function collectExecutionTreeTraceEvents(tree: IssueExecutionTreeResponse | undefined): TaskTraceEvent[] {
  if (!tree?.root) return [];
  const out: TaskTraceEvent[] = [];
  walkExecutionTree(tree.root, (node) => out.push(...node.trace_events));
  return out;
}

function walkExecutionTree(
  node: IssueExecutionTreeResponse["root"],
  visit: (node: IssueExecutionTreeResponse["root"]) => void,
) {
  visit(node);
  node.children.forEach((child) => walkExecutionTree(child, visit));
}

function mergeTasks(...groups: AgentTask[][]): AgentTask[] {
  const map = new Map<string, AgentTask>();
  groups.flat().forEach((task) => map.set(task.id, task));
  return [...map.values()];
}

function mergeTraceEvents(...groups: TaskTraceEvent[][]): TaskTraceEvent[] {
  const map = new Map<string, TaskTraceEvent>();
  groups.flat().forEach((event) => map.set(event.id, event));
  return [...map.values()];
}

function earliest(...groups: (string | null | undefined)[][]): string {
  const values: string[] = groups.flat().filter(isPresentString);
  const sorted = values.sort(compareTimestampAsc);
  return sorted[0] ?? "";
}

function latest(...groups: (string | null | undefined)[][]): string {
  const values: string[] = groups.flat().filter(isPresentString);
  const sorted = values.sort(compareTimestampDesc);
  return sorted[0] ?? "";
}

function isPresentString(value: string | null | undefined): value is string {
  return Boolean(value);
}

function compareTimestampAsc(a: string, b: string): number {
  return Date.parse(a) - Date.parse(b);
}

function compareTimestampDesc(a: string, b: string): number {
  return Date.parse(b) - Date.parse(a);
}

function formatTimestamp(value: string | null | undefined): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())} ${pad(date.getUTCHours())}:${pad(date.getUTCMinutes())}`;
}

function traceEventTokenTotal(event: TaskTraceEvent): number {
  return usageTokenTotal(event);
}

function traceEventStageLabel(eventType: string): string {
  switch (eventType) {
    case "task.queued":
      return "任务入队";
    case "task.dispatched":
      return "任务领取";
    case "task.started":
      return "任务开始";
    case "task.waiting_local_directory":
      return "等待本地目录";
    case "task.completed":
      return "任务完成";
    case "task.failed":
      return "任务失败";
    case "task.cancelled":
      return "任务取消";
    case "llm.usage_reported":
      return "模型用量";
    default:
      return eventType || "未分类事件";
  }
}

function traceFailureSummary(event: TaskTraceEvent): string {
  const parts = [
    event.failure_reason && event.failure_reason !== "无" ? `失败原因：${event.failure_reason}` : "",
    event.error_type ? `错误类型：${event.error_type}` : "",
  ].filter(Boolean);
  return parts.join(" · ");
}

function latestTaskFailureSummary(task: AgentTask | undefined): string {
  if (!task) return "";
  if (task.error) return task.error;
  if (task.failure_reason) return `失败原因：${task.failure_reason}`;
  if (task.status === "cancelled") return "任务已取消";
  return "";
}

function deriveAcceptanceStatus(tasks: AgentTask[]): string {
  if (tasks.some((task) => task.status === "failed")) return "失败";
  if (tasks.some((task) => task.status === "cancelled")) return "已取消";
  if (tasks.some((task) => task.status === "completed")) return "待复盘";
  return "待运行";
}

function formatNullableMilliseconds(ms: number | null | undefined): string {
  if (typeof ms !== "number") return "进行中";
  return formatMilliseconds(ms);
}

function formatMilliseconds(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return rest === 0 ? `${minutes}m` : `${minutes}m ${rest}s`;
}

// ─── Trigger description ────────────────────────────────────────────────────

// Primary source: the canonical snapshot taken at task creation time
// (comment text / autopilot title). Survives source edits/deletes and
// is information-dense — far better than a structural label.
//
// Retry tasks inherit the parent's trigger_summary on the DB side (so the
// snapshot survives across attempts), but a row that just shows the
// inherited summary is indistinguishable from its parent. We prepend
// "Retry #N" when parent_task_id is set so retries are scannable as
// retries even when their summary is inherited.
//
// Fallback chain for legacy tasks created before the snapshot field
// shipped, OR for sources we don't snapshot (direct assignment / chat):
// degrade to a short structural label by trigger source. New tasks
// (post-061 migration) almost always hit the snapshot path.

// ─── Row visual config ─────────────────────────────────────────────────────

const STATUS_TONE: Record<AgentTask["status"], string> = {
  queued: "text-warning",
  dispatched: "text-warning",
  // Same tone as queued/dispatched — visually "stopped" so users see the
  // task is parked, but distinguished by the status label.
  waiting_local_directory: "text-warning",
  running: "text-info",
  completed: "text-success",
  failed: "text-destructive",
  cancelled: "text-muted-foreground",
};

// ─── Active row ────────────────────────────────────────────────────────────

import { stripMentionMarkdown } from "../utils/strip-mention-markdown";

function useTriggerText(task: AgentTask): string {
  const { t } = useT("issues");
  const isRetry = !!task.parent_task_id;
  const retryPrefix = isRetry
    ? task.attempt && task.attempt > 1
      ? t(($) => $.execution_log.trigger_retry_attempt_prefix, { attempt: task.attempt })
      : t(($) => $.execution_log.trigger_retry_prefix)
    : "";

  if (task.trigger_summary) return retryPrefix + stripMentionMarkdown(task.trigger_summary);
  if (isRetry) {
    return task.attempt && task.attempt > 1
      ? t(($) => $.execution_log.trigger_retry_attempt, { attempt: task.attempt })
      : t(($) => $.execution_log.trigger_retry);
  }
  if (task.autopilot_run_id) return t(($) => $.execution_log.trigger_autopilot);
  if (task.trigger_comment_id) return t(($) => $.execution_log.trigger_comment);
  return t(($) => $.execution_log.trigger_initial);
}

function useStatusLabel(status: AgentTask["status"]): string {
  const { t } = useT("issues");
  switch (status) {
    case "queued": return t(($) => $.execution_log.status_queued);
    case "dispatched": return t(($) => $.execution_log.status_dispatched);
    case "waiting_local_directory":
      return t(($) => $.execution_log.status_waiting_local_directory);
    case "running": return t(($) => $.execution_log.status_running);
    case "completed": return t(($) => $.execution_log.status_completed);
    case "failed": return t(($) => $.execution_log.status_failed);
    case "cancelled": return t(($) => $.execution_log.status_cancelled);
  }
}

// One active (running / queued / dispatched / parked) task row. Running rows
// keep status to a single live elapsed timer; transcript and stop stay available
// as hover actions. Transcript content lazy-loads on click via TranscriptButton,
// so the row no longer fetches task messages just to render a count.
export function ActiveTaskRow({
  task,
  issueId,
}: {
  task: AgentTask;
  issueId: string;
}) {
  const { t } = useT("issues");
  const [cancelling, setCancelling] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const tone = STATUS_TONE[task.status];
  const label = useStatusLabel(task.status);
  const trigger = useTriggerText(task);

  // Running rows show a live-ticking elapsed timer (the ticking digits carry
  // "alive", the duration carries "how long"). Only running rows tick.
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

  // Transcript only meaningful once messages exist — pure-queued and
  // waiting_local_directory tasks haven't streamed any agent output yet.
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

  return (
    <RowShell task={task}>
      <TriggerText text={trigger} />
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

// ─── Shared row chrome ─────────────────────────────────────────────────────

function RowShell({
  task,
  children,
}: {
  task: AgentTask;
  children: React.ReactNode;
}) {
  return (
    <div className="group/execution-log-row flex items-center gap-2 overflow-hidden rounded px-1 py-1.5 transition-colors hover:bg-accent/40">
      {task.agent_id ? (
        <ActorAvatar
          actorType="agent"
          actorId={task.agent_id}
          size={20}
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
  return <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{text}</span>;
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
      className="flex h-7 shrink-0 items-center justify-end gap-1 overflow-hidden whitespace-nowrap text-xs [@media(hover:hover)]:group-hover/execution-log-row:hidden"
    >
      {children}
    </div>
  );
}

// Action slot — visible by default for touch devices. On hover-capable
// surfaces, it replaces the status column in place on row hover.
function RowActions({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-7 items-center gap-0.5 [@media(hover:hover)]:hidden [@media(hover:hover)]:group-hover/execution-log-row:flex">
      {children}
    </div>
  );
}
