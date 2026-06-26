"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Ban, CheckCircle2, ChevronRight, Loader2, RotateCcw, Square, XCircle } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { issueKeys, issueExecutionTreeOptions, issueTaskTraceOptions, issueSOPRunsOptions } from "@multica/core/issues/queries";
import type { AgentTask, IssueExecutionNode, IssueExecutionTreeResponse, IssueTimelineNode, SquadSOPRun, TaskFailureReason, TaskTraceEvent } from "@multica/core/types";
import { useTimeAgo } from "../../i18n";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { ActorAvatar } from "../../common/actor-avatar";
import { formatDuration } from "../../agents/components/agent-activity-hover-content";
import { TranscriptButton } from "../../common/task-transcript";
import { failureReasonLabel } from "../../agents/components/tabs/task-failure";
import { useT } from "../../i18n";
import { TerminateTaskConfirmDialog } from "./terminate-task-confirm-dialog";

// Right-panel section that lists every agent run for this issue. Active
// runs sit at the top (always visible when present); past runs (terminal
// statuses) collapse behind a "Show past runs (N)" toggle.
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

// Past-runs sort priority: newest first by timestamp. When two runs
// share the same timestamp, failed ranks above cancelled, which ranks
// above completed.
const PAST_STATUS_RANK: Record<string, number> = {
  failed: 0,
  cancelled: 1,
  completed: 2,
};

export function ExecutionLogSection({ issueId }: ExecutionLogSectionProps) {
  const { t } = useT("issues");
  const [open, setOpen] = useState(true);
  const [showPast, setShowPast] = useState(false);

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

  const hasExecutionTree = hasMeaningfulExecutionTree(executionTree);

  if (activeTasks.length === 0 && pastTasks.length === 0 && traceEvents.length === 0 && sopRuns.length === 0 && !hasExecutionTree) return null;

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
        {activeTasks.length > 0 && (
          <span className="ml-auto inline-flex items-center gap-1 text-info">
            <span className="h-1.5 w-1.5 rounded-full bg-info animate-pulse" />
            <span className="font-mono tabular-nums">{activeTasks.length}</span>
          </span>
        )}
      </button>
      {open && (
        <div className="space-y-0.5 pl-2">
          {activeTasks.map((task) => (
            <ActiveTaskRow key={task.id} task={task} issueId={issueId} />
          ))}

          {hasExecutionTree && executionTree && (
            <>
              {activeTasks.length > 0 && (
                <div className="my-1.5 border-t border-border/60" />
              )}
              <IssueTimelineSummaryCard tree={executionTree} />
              <div className="my-1.5 border-t border-border/60" />
              <CollaborationExecutionTree tree={executionTree} />
            </>
          )}

          {sopRuns.length > 0 && (
            <>
              {(activeTasks.length > 0 || hasExecutionTree) && (
                <div className="my-1.5 border-t border-border/60" />
              )}
              <SOPRunSummary runs={sopRuns} />
            </>
          )}

          {pastTasks.length > 0 && (
            <>
              {(activeTasks.length > 0 || hasExecutionTree || sopRuns.length > 0) && (
                <div className="my-1.5 border-t border-border/60" />
              )}
              <button
                type="button"
                onClick={() => setShowPast(!showPast)}
                className="flex w-full items-center gap-1 rounded px-1 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent/40 hover:text-foreground"
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
          {traceEvents.length > 0 && (
            <>
              {(activeTasks.length > 0 || hasExecutionTree || pastTasks.length > 0 || sopRuns.length > 0) && (
                <div className="my-1.5 border-t border-border/60" />
              )}
              <TraceEventSummary events={traceEvents} />
            </>
          )}
        </div>
      )}
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

function IssueTimelineSummaryCard({ tree }: { tree: IssueExecutionTreeResponse }) {
  const nodes = tree.timeline_nodes ?? [];
  const summary = tree.issue_summary;
  if (nodes.length === 0 || !summary) return null;
  const preview = nodes.slice(0, 6);
  const tokenTotal = summary.total_input_tokens + summary.total_output_tokens + summary.total_cache_read_tokens + summary.total_cache_write_tokens;
  return (
    <div className="rounded-md border border-border/70 bg-muted/25 px-2 py-1.5" data-testid="issue-timeline-summary">
      <div className="mb-1 flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
        <span>Issue 运行时间流</span>
        <span className="font-mono tabular-nums">
          {summary.node_count} 节点{tokenTotal > 0 ? ` / ${tokenTotal.toLocaleString()} tokens` : ""}
        </span>
      </div>
      <div className="mb-1.5 grid gap-1 text-[11px] leading-5 text-muted-foreground sm:grid-cols-2">
        <span className="truncate">总耗时 {formatNullableMilliseconds(summary.total_duration_ms)}</span>
        <span className="truncate">消息 {summary.message_count} / 轮次 {summary.agent_turn_count}</span>
        <span className="truncate">trace {summary.trace_event_count}</span>
        <span className="truncate">验收 {summary.acceptance_status}</span>
        {summary.usage_unavailable && (
          <span className="truncate text-warning">存在 usage_unavailable_trace</span>
        )}
        {summary.failure_summary && (
          <span className="truncate text-destructive">失败：{summary.failure_summary}</span>
        )}
      </div>
      <div className="overflow-hidden rounded border border-dashed border-border/70 bg-background/45">
        <div className="grid grid-cols-[88px_minmax(0,1fr)_72px_72px] gap-2 border-b border-border/60 px-1.5 py-1 text-[11px] font-medium text-muted-foreground">
          <span>节点</span>
          <span>摘要</span>
          <span className="text-right">耗时</span>
          <span className="text-right">token</span>
        </div>
        {preview.map((node) => (
          <IssueTimelineNodeRow key={node.node_id} node={node} />
        ))}
      </div>
      {nodes.length > preview.length && (
        <div className="mt-1 text-[11px] leading-5 text-muted-foreground">
          还有 {nodes.length - preview.length} 个时间流节点未展开
        </div>
      )}
    </div>
  );
}

function IssueTimelineNodeRow({ node }: { node: IssueTimelineNode }) {
  const tokenTotal = node.input_tokens + node.output_tokens + node.cache_read_tokens + node.cache_write_tokens;
  return (
    <div className="grid grid-cols-[88px_minmax(0,1fr)_72px_72px] gap-2 border-b border-border/40 px-1.5 py-1 text-[11px] leading-5 last:border-b-0">
      <span className="truncate text-muted-foreground" title={node.node_type}>
        {timelineNodeTypeLabel(node.node_type)}
      </span>
      <span className="min-w-0 truncate text-foreground" title={node.summary}>
        {node.summary || node.status}
        {node.child_issue_id ? ` · ${node.child_issue_id}` : ""}
        {node.usage_unavailable_trace ? " · usage unavailable" : ""}
        {node.evidence_refs.length > 0 ? ` · 证据 ${node.evidence_refs.length}` : ""}
      </span>
      <span className="text-right tabular-nums text-muted-foreground">{formatNullableMilliseconds(node.duration_ms)}</span>
      <span className="text-right tabular-nums text-muted-foreground">{tokenTotal > 0 ? tokenTotal.toLocaleString() : "-"}</span>
    </div>
  );
}

function timelineNodeTypeLabel(type: IssueTimelineNode["node_type"]): string {
  switch (type) {
    case "agent_task":
      return "Agent";
    case "squad_step":
      return "SOP";
    case "tool_call":
      return "工具";
    case "evidence":
      return "证据";
    case "approval":
      return "审批";
    case "child_issue_ref":
      return "子任务";
    case "source_fetch":
      return "来源";
    case "status_change":
      return "状态";
  }
}

function CollaborationExecutionTree({ tree }: { tree: IssueExecutionTreeResponse }) {
  const childPreview = tree.root.children.slice(0, 4);
  const summary = tree.summary ?? {};
  return (
    <div className="rounded-md border border-border/70 bg-muted/25 px-2 py-1.5" data-testid="issue-collaboration-execution-tree">
      <div className="mb-1 flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
        <span>协作执行树</span>
        <span className="font-mono tabular-nums">{Number(summary["子任务数"] ?? 0)} 个子任务</span>
      </div>
      <div className="mb-1.5 grid gap-1 text-[11px] leading-5 text-muted-foreground sm:grid-cols-2">
        <span className="truncate">根任务 {tree.root.issue.identifier}</span>
        <span className="truncate">任务 {Number(summary["任务数"] ?? 0)} / 完成 {Number(summary["完成任务数"] ?? 0)}</span>
        <span className="truncate">SOP {Number(summary["SOP执行数"] ?? 0)} / 事件 {Number(summary["SOP事件数"] ?? 0)}</span>
        <span className="truncate">观测 {Number(summary["观测事件数"] ?? 0)} / 唤醒 {Number(summary["唤醒评论数"] ?? 0)}</span>
        <span className="truncate">工具 {Number(summary["工具调用数"] ?? 0)} / 异常 {Number(summary["异常工具数"] ?? 0)}</span>
      </div>
      <div className="space-y-1">
        <ExecutionTreeNodeRow node={tree.root} root />
        {childPreview.map((child) => (
          <ExecutionTreeNodeRow key={child.issue.id} node={child} />
        ))}
        {tree.root.children.length > childPreview.length && (
          <div className="pl-3 text-[11px] text-muted-foreground">
            还有 {tree.root.children.length - childPreview.length} 个子任务未展开
          </div>
        )}
      </div>
    </div>
  );
}

function ExecutionTreeNodeRow({ node, root = false }: { node: IssueExecutionNode; root?: boolean }) {
  const terminalTasks = node.tasks.filter((task) => ["completed", "failed", "cancelled"].includes(task.status)).length;
  const latestWakeup = node.wakeup_comments.at(-1);
  const toolSummary = node.tool_call_summary ?? [];
  const toolChains = node.tool_call_chains ?? [];
  const toolCallCount = toolSummary.reduce((sum, item) => sum + item.total_calls, 0);
  const toolAttentionCount = toolSummary.reduce((sum, item) => sum + item.failure_signal_calls + item.missing_result_calls + item.orphan_result_calls, 0);
  return (
    <div className={`grid gap-0.5 border-l-2 ${root ? "border-info/70" : "border-emerald-500/70"} pl-2 text-xs`}>
      <div className="flex min-w-0 items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-foreground">
          {root ? "父任务" : "子任务"} {node.issue.identifier} · {node.issue.title}
        </span>
        <span className="shrink-0 rounded border border-border/70 px-1.5 py-0.5 text-[11px] text-muted-foreground">
          {node.issue.status}
        </span>
      </div>
      <div className="grid gap-x-2 gap-y-0.5 text-[11px] leading-5 text-muted-foreground sm:grid-cols-2">
        <span className="truncate">任务 {node.tasks.length} / 终态 {terminalTasks}</span>
        <span className="truncate">SOP {node.sop_runs.length} / trace {node.trace_events.length}</span>
        <span className="truncate">子任务 {node.children.length}</span>
        <span className="truncate">工具 {toolCallCount} / 异常 {toolAttentionCount}</span>
        <span className="truncate">唤醒评论 {node.wakeup_comments.length}</span>
      </div>
      {toolSummary.length > 0 && (
        <div className="grid gap-0.5 text-[11px] leading-5 text-muted-foreground" data-testid={`issue-execution-tool-summary-${node.issue.id}`}>
          {toolSummary.slice(0, 3).map((item) => (
            <div key={item.tool} className="truncate">
              工具 {item.tool}：调用 {item.total_calls}，异常线索 {item.failure_signal_calls}，最慢 {formatNullableMilliseconds(item.max_duration_ms ?? 0)}
            </div>
          ))}
        </div>
      )}
      {toolChains.length > 0 && (
        <div className="space-y-0.5 rounded border border-dashed border-border/70 bg-background/40 px-1.5 py-1 text-[11px] leading-5 text-muted-foreground" data-testid={`issue-execution-tool-chains-${node.issue.id}`}>
          <div className="font-medium text-foreground">工具链明细</div>
          {toolChains.slice(0, 3).map((chain) => (
            <div key={chain.id} className="min-w-0">
              <div className="truncate">
                {chain.tool || "未记录工具"} · {chain.result_category || chain.status}
                {chain.duration_ms ? ` · ${formatNullableMilliseconds(chain.duration_ms)}` : ""}
                {chain.failure_signal ? " · 异常线索" : ""}
              </div>
              <div className="truncate">
                调用 #{chain.use_seq || "-"} / 结果 #{chain.result_seq || "-"}
                {chain.failure_reason ? ` · ${chain.failure_reason}` : ""}
              </div>
            </div>
          ))}
          {toolChains.length > 3 && (
            <div className="truncate">还有 {toolChains.length - 3} 条工具链未展开</div>
          )}
        </div>
      )}
      {latestWakeup && (
        <div className="truncate text-[11px] leading-5 text-muted-foreground">
          最近唤醒：{latestWakeup.content}
        </div>
      )}
    </div>
  );
}

function SOPRunSummary({ runs }: { runs: SquadSOPRun[] }) {
  const recent = runs.slice(0, 3);
  return (
    <div className="rounded-md border border-border/70 bg-muted/25 px-2 py-1.5" data-testid="issue-sop-run-summary">
      <div className="mb-1 flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
        <span>小队 SOP 执行</span>
        <span className="font-mono tabular-nums">{runs.length} 次</span>
      </div>
      <div className="space-y-1">
        {recent.map((run) => {
          const evidenceCount = Number(run.metrics?.["证据数"] ?? run.events.length);
          const stageMetrics = parseSOPStageMetrics(run.metrics?.["阶段指标"]);
          const stageTokenTotal = stageMetrics.reduce((sum, item) => sum + item.input_tokens + item.output_tokens, 0);
          const stageTurnTotal = stageMetrics.reduce((sum, item) => sum + item.agent_turn_count, 0);
          return (
            <div key={run.id} className="space-y-0.5">
              <div className="flex min-w-0 items-center gap-2 text-xs">
                <span className="min-w-0 flex-1 truncate text-foreground">
                  {run.profile_key || "小队流程"} · {run.status}
                </span>
                <span className="shrink-0 text-muted-foreground">
                  {formatNullableMilliseconds(run.total_duration_ms)}
                </span>
              </div>
              <div className="flex min-w-0 items-center gap-2 text-[11px] text-muted-foreground">
                <span className="min-w-0 flex-1 truncate">
                  当前阶段：{run.current_step_key || "未记录"}
                </span>
                <span className="shrink-0">{evidenceCount} 条证据</span>
              </div>
              {stageMetrics.length > 0 && (
                <div className="rounded border border-dashed border-border/70 bg-background/45 px-1.5 py-1 text-[11px] leading-5 text-muted-foreground">
                  <div className="mb-0.5 truncate">
                    阶段指标 {stageMetrics.length} 阶段
                    {stageTurnTotal > 0 ? ` / ${stageTurnTotal} 轮` : ""}
                    {stageTokenTotal > 0 ? ` / ${stageTokenTotal.toLocaleString()} tokens` : ""}
                  </div>
                  {stageMetrics.slice(0, 6).map((stage) => {
                    const tokens = stage.input_tokens + stage.output_tokens;
                    return (
                      <div key={stage.step_key} className="grid gap-x-2 sm:grid-cols-[minmax(0,1fr)_auto]">
                        <span className="truncate">
                          {stage.step_name || stage.step_key} · {stage.status || "未开始"}
                        </span>
                        <span className="shrink-0 tabular-nums">
                          {formatNullableMilliseconds(stage.duration_ms)}
                          {stage.agent_turn_count > 0 ? ` · ${stage.agent_turn_count} 轮` : ""}
                          {tokens > 0 ? ` · ${tokens.toLocaleString()} tok` : ""}
                        </span>
                      </div>
                    );
                  })}
                </div>
              )}
              {run.events.slice(-2).toReversed().map((event) => (
                <div key={event.id} className="flex min-w-0 items-center gap-2 pl-2 text-[11px]">
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">
                    {event.step_name || event.step_key || "未命名阶段"} · {event.event_type}
                  </span>
                  <span className="shrink-0 text-muted-foreground">
                    {event.reason || event.status}
                  </span>
                </div>
              ))}
            </div>
          );
        })}
      </div>
    </div>
  );
}

type SOPStageMetric = {
  step_key: string;
  step_name: string;
  status: string;
  duration_ms: number;
  agent_turn_count: number;
  input_tokens: number;
  output_tokens: number;
};

function parseSOPStageMetrics(raw: unknown): SOPStageMetric[] {
  if (!Array.isArray(raw)) return [];
  return raw
    .map((item): SOPStageMetric | null => {
      if (!item || typeof item !== "object") return null;
      const record = item as Record<string, unknown>;
      const stepKey = stringMetric(record.step_key);
      if (!stepKey) return null;
      return {
        step_key: stepKey,
        step_name: stringMetric(record.step_name),
        status: stringMetric(record.status),
        duration_ms: numberMetric(record.duration_ms),
        agent_turn_count: numberMetric(record.agent_turn_count),
        input_tokens: numberMetric(record.input_tokens),
        output_tokens: numberMetric(record.output_tokens),
      };
    })
    .filter((item): item is SOPStageMetric => item !== null);
}

function stringMetric(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function numberMetric(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function TraceEventSummary({ events }: { events: TaskTraceEvent[] }) {
  const recent = events.slice(-6);
  const tokenTotal = events.reduce((sum, event) => sum + traceEventTokenTotal(event), 0);
  const lifecycleCount = events.filter((event) => event.event_type.startsWith("task.")).length;
  const usageCount = events.filter(
    (event) => event.event_type === "llm.usage_reported" || traceEventTokenTotal(event) > 0,
  ).length;
  const rootTaskId = events.find((event) => event.task_id)?.task_id ?? "未记录";

  return (
    <div className="rounded-md border border-border/70 bg-muted/25 px-2 py-1.5" data-testid="issue-trace-event-summary">
      <div className="mb-1 flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
        <span>观测事件</span>
        <span className="font-mono tabular-nums">
          {events.length} 条{tokenTotal > 0 ? ` / ${tokenTotal.toLocaleString()} tokens` : ""}
        </span>
      </div>
      <div className="mb-1.5 space-y-1 rounded border border-dashed border-border/70 bg-background/45 px-2 py-1.5">
        <div className="flex min-w-0 flex-wrap items-center gap-1.5 text-[11px]">
          <span className="font-medium text-foreground">任务事件树</span>
          <span className="rounded border border-border/70 px-1.5 py-0.5 text-muted-foreground">生命周期 {lifecycleCount}</span>
          <span className="rounded border border-border/70 px-1.5 py-0.5 text-muted-foreground">用量事件 {usageCount}</span>
          <span className="rounded border border-border/70 px-1.5 py-0.5 text-muted-foreground">token {tokenTotal.toLocaleString()}</span>
        </div>
        <div className="break-all text-[11px] leading-5 text-muted-foreground">
          根任务 {rootTaskId}
        </div>
      </div>
      <div className="space-y-1">
        {recent.map((event, index) => (
          <TraceEventTreeRow
            key={event.id}
            event={event}
            index={events.length - recent.length + index + 1}
          />
        ))}
      </div>
    </div>
  );
}

function TraceEventTreeRow({
  event,
  index,
}: {
  event: TaskTraceEvent;
  index: number;
}) {
  const metadataSummary = traceMetadataSummary(event.metadata);
  const failure = traceFailureSummary(event);
  return (
    <div className="grid gap-0.5 border-l-2 border-emerald-500/70 pl-2 text-xs">
      <div className="flex min-w-0 items-center gap-2">
        <span className="shrink-0 font-mono text-[11px] text-muted-foreground">#{index}</span>
        <span className="min-w-0 flex-1 truncate text-foreground">
          {event.event_name || traceEventStageLabel(event.event_type)}
        </span>
        <span className="shrink-0 rounded border border-border/70 px-1.5 py-0.5 text-[11px] text-muted-foreground">
          {traceEventStageLabel(event.event_type)}
        </span>
      </div>
      <div className="grid gap-x-2 gap-y-0.5 text-[11px] leading-5 text-muted-foreground sm:grid-cols-2">
        <span className="truncate">状态 {event.status || "未知"}</span>
        <span className="truncate">耗时 {formatTraceEventDuration(event)}</span>
        <span className="truncate">模型 {event.provider || "未记录"}/{event.model || "未记录"}</span>
        <span className="truncate">token {traceEventTokenTotal(event).toLocaleString()}</span>
      </div>
      {failure && (
        <div className="rounded bg-destructive/10 px-1.5 py-0.5 text-[11px] leading-5 text-destructive">
          {failure}
        </div>
      )}
      {metadataSummary && (
        <div className="truncate text-[11px] leading-5 text-muted-foreground">
          元数据：{metadataSummary}
        </div>
      )}
    </div>
  );
}

function traceEventTokenTotal(event: TaskTraceEvent): number {
  return event.input_tokens + event.output_tokens + event.cache_read_tokens + event.cache_write_tokens;
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

function formatTraceEventDuration(event: TaskTraceEvent): string {
  const parts = [
    event.queue_wait_ms != null ? `排队 ${formatMilliseconds(event.queue_wait_ms)}` : "",
    event.run_ms != null ? `执行 ${formatMilliseconds(event.run_ms)}` : "",
    event.duration_ms != null ? `阶段 ${formatMilliseconds(event.duration_ms)}` : "",
    event.total_ms != null ? `总计 ${formatMilliseconds(event.total_ms)}` : "",
  ].filter(Boolean);
  return parts.join(" / ") || formatTraceMetric(event);
}

function formatTraceMetric(event: TaskTraceEvent): string {
  const tokenTotal = traceEventTokenTotal(event);
  if (tokenTotal > 0) return `${tokenTotal.toLocaleString()} tokens`;
  const ms = event.run_ms ?? event.duration_ms ?? event.total_ms ?? event.queue_wait_ms;
  if (typeof ms === "number") return formatMilliseconds(ms);
  return event.status;
}

function traceFailureSummary(event: TaskTraceEvent): string {
  const parts = [
    event.failure_reason && event.failure_reason !== "无" ? `失败原因：${event.failure_reason}` : "",
    event.error_type ? `错误类型：${event.error_type}` : "",
  ].filter(Boolean);
  return parts.join(" · ");
}

function traceMetadataSummary(metadata: Record<string, unknown>): string {
  return Object.entries(metadata)
    .slice(0, 3)
    .map(([key, value]) => `${key}=${formatTraceMetadataValue(value)}`)
    .join("，");
}

function formatTraceMetadataValue(value: unknown): string {
  if (typeof value === "string") return truncateTraceText(value, 48);
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  if (value == null) return "空";
  return truncateTraceText(JSON.stringify(value), 48);
}

function truncateTraceText(value: string, max: number): string {
  return value.length > max ? `${value.slice(0, max)}...` : value;
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

// ─── Past row ──────────────────────────────────────────────────────────────

function PastRow({ task, issueId }: { task: AgentTask; issueId: string }) {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const [retrying, setRetrying] = useState(false);
  const label = useStatusLabel(task.status);
  const trigger = useTriggerText(task);
  const time = task.completed_at ? timeAgo(task.completed_at) : "—";
  const failureLabel =
    task.status === "failed" && task.failure_reason
      ? failureReasonLabel[task.failure_reason as TaskFailureReason]
      : null;

  // Retry only makes sense for terminal-but-not-success rows. Passing
  // task.id targets this specific row's agent — without it, the rerun
  // endpoint would fall back to the issue's current assignee and the
  // wrong agent would fire on rows whose agent has since been displaced
  // (e.g. reassignment, squad worker, or a one-off @-mention agent).
  const canRetry = task.status === "failed" || task.status === "cancelled";

  const handleRetry = async () => {
    if (retrying) return;
    setRetrying(true);
    try {
      await api.rerunIssue(issueId, task.id);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.execution_log.retry_failed));
    } finally {
      // Reset on both success and failure: the past row stays mounted
      // (its task.id is unchanged), so leaving `retrying` true on success
      // would pin the button as a permanent spinner.
      setRetrying(false);
    }
  };

  return (
    <RowShell task={task}>
      <TriggerText text={trigger} />
      <RowStatus title={failureLabel ?? label}>
        <TaskStatusIcon status={task.status} />
        <span className="sr-only">{failureLabel ?? label}</span>
        <span className="text-muted-foreground">{time}</span>
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

function TaskStatusIcon({ status }: { status: AgentTask["status"] }) {
  switch (status) {
    case "completed":
      return <CheckCircle2 aria-hidden="true" className="h-3.5 w-3.5 text-success" />;
    case "failed":
      return <XCircle aria-hidden="true" className="h-3.5 w-3.5 text-destructive" />;
    case "cancelled":
      return <Ban aria-hidden="true" className="h-3.5 w-3.5 text-muted-foreground" />;
    default:
      return null;
  }
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
