// 运行复盘里的「实时」数据逻辑：运行尚未结束时，根据进行中的任务/节点把
// 摘要时长实时推算，并提供一个每秒走动的时间源与事件刷新时机工具。

import { useEffect, useState } from "react";
import { isActiveAgentTaskStatus } from "@multica/core/agents";
import type { AgentTask, IssueTimelineNode, IssueTimelineSummary } from "@multica/core/types";
import type {
  TaskLifecyclePayload,
  TaskMessagePayload,
} from "@multica/core/types/events";
import { formatDuration, parseTimeMs } from "./run-review-format";

const RUN_REVIEW_MESSAGE_REFRESH_DEBOUNCE_MS = 1_200;
const RUN_REVIEW_MESSAGE_REFRESH_MAX_WAIT_MS = 4_000;
const RUN_REVIEW_LIVE_DURATION_TICK_MS = 1_000;

/** 复盘相关的事件负载：任务生命周期事件或任务消息事件的并集。 */
export type RunReviewTaskEventPayload =
  | TaskLifecyclePayload
  | TaskMessagePayload;

/**
 * 从摘要里算「总耗时」：优先 agent 执行 + 人工确认 + 子任务等待；都没有时退回
 * 服务端给出的 total_duration_ms。
 */
export function runReviewTotalDurationMs(summary: IssueTimelineSummary | undefined): number {
  if (!summary) return 0;
  const agentExecution = summary.agent_execution_duration_ms;
  const childIssueWait = summary.child_issue_wait_duration_ms ?? 0;
  if (agentExecution != null) {
    return agentExecution + (summary.human_confirmation_duration_ms ?? 0) + childIssueWait;
  }
  return summary.total_duration_ms ?? 0;
}
/**
 * 拼一段可读的耗时摘要文案，如「Agent 执行 12m · 人工确认 未记录 · 子任务等待 3m」。
 */
export function buildRunReviewDurationSummary(summary: IssueTimelineSummary | undefined): string {
  const agentExecution = summary?.agent_execution_duration_ms ?? summary?.total_duration_ms ?? 0;
  const humanConfirmation = summary?.human_confirmation_duration_ms;
  const childIssueWait = summary?.child_issue_wait_duration_ms;
  return [
    `Agent 执行 ${formatDuration(agentExecution)}`,
    `人工确认 ${humanConfirmation == null ? "未记录" : formatDuration(humanConfirmation)}`,
    `子任务等待 ${childIssueWait == null ? "未记录" : formatDuration(childIssueWait)}`,
  ].join(" · ");
}

/**
 * 把摘要的各段耗时「实时化」：若有进行中的任务/节点，则取其已流逝时长（可能
 * 超过已存值），得到到「现在」为止的时长摘要；没有任何进行中活动时原样返回。
 */
export function buildRunReviewLiveSummary(
  summary: IssueTimelineSummary | undefined,
  activeTasks: AgentTask[],
  timelineNodes: IssueTimelineNode[],
  nowMs: number,
): IssueTimelineSummary | undefined {
  if (!summary) return summary;
  const liveAgentDurationMs = Math.max(
    0,
    ...activeTasks.map((task) => liveElapsedMs(task.started_at ?? task.dispatched_at ?? task.created_at, nowMs)),
    ...timelineNodes
      .filter((node) => node.node_type === "agent_task" && isActiveTimelineNode(node))
      .map((node) => liveElapsedMs(node.started_at, nowMs)),
  );
  const liveHumanConfirmationDurationMs = Math.max(
    0,
    ...timelineNodes
      .filter((node) => node.node_type === "human_confirmation" && isActiveTimelineNode(node))
      .map((node) => liveElapsedMs(node.started_at, nowMs)),
  );
  if (liveAgentDurationMs <= 0 && liveHumanConfirmationDurationMs <= 0) return summary;
  const agentExecutionMs = Math.max(summary.agent_execution_duration_ms ?? summary.total_duration_ms ?? 0, liveAgentDurationMs);
  const hasHumanConfirmation = summary.human_confirmation_duration_ms != null || liveHumanConfirmationDurationMs > 0;
  const humanConfirmationMs = hasHumanConfirmation
    ? Math.max(summary.human_confirmation_duration_ms ?? 0, liveHumanConfirmationDurationMs)
    : summary.human_confirmation_duration_ms;
  const childIssueWaitMs = summary.child_issue_wait_duration_ms ?? 0;
  const totalDurationMs = hasHumanConfirmation
    ? agentExecutionMs + (humanConfirmationMs ?? 0) + childIssueWaitMs
    : Math.max(summary.total_duration_ms ?? 0, agentExecutionMs);
  return {
    ...summary,
    total_duration_ms: totalDurationMs,
    agent_execution_duration_ms: agentExecutionMs,
    human_confirmation_duration_ms: humanConfirmationMs,
  };
}

/** 让时间线上进行中的节点，其 duration_ms 跟随「现在」实时增长。 */
export function buildRunReviewLiveTimelineNodes(timelineNodes: IssueTimelineNode[], nowMs: number): IssueTimelineNode[] {
  return timelineNodes.map((node) => {
    if (!isActiveTimelineNode(node)) return node;
    const durationMs = liveElapsedMs(node.started_at, nowMs);
    if (durationMs <= (node.duration_ms ?? 0)) return node;
    return { ...node, duration_ms: durationMs };
  });
}

/**
 * 复盘还在活跃时返回一个每秒走动的时间源（毫秒时间戳），供「实时时长」渲染。
 */
export function useRunReviewLiveNow(active: boolean) {
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNowMs(Date.now());
    const timer = setInterval(() => setNowMs(Date.now()), RUN_REVIEW_LIVE_DURATION_TICK_MS);
    return () => clearInterval(timer);
  }, [active]);
  return nowMs;
}

/** 某个时间点起算、到 now 为止已流逝的毫秒数（尚未开始或时间异常按 0 计）。 */
function liveElapsedMs(startedAt: string | null | undefined, nowMs: number) {
  const startedMs = parseTimeMs(startedAt);
  if (startedMs === null || startedMs > nowMs) return 0;
  return nowMs - startedMs;
}

/** 时间线上是否还有进行中的节点（决定要不要继续实时刷新）。 */
export function hasActiveTimelineNode(timelineNodes: IssueTimelineNode[]) {
  return timelineNodes.some(isActiveTimelineNode);
}

/** 判断某个时间线节点是否「仍在进行」：活动状态 + 有开始时间 + 未结束。 */
export function isActiveTimelineNode(node: Pick<IssueTimelineNode, "status" | "started_at" | "completed_at">) {
  return isActiveAgentTaskStatus(node.status) && Boolean(node.started_at) && !node.completed_at;
}

/** 判断收到的事件是否与正在展示的这条 issue 有关（决定要不要刷新复盘）。 */
export function shouldRefreshRunReviewForTaskEvent(
  issueId: string,
  payload: Pick<RunReviewTaskEventPayload, "issue_id"> | null | undefined,
): boolean {
  return Boolean(issueId && payload?.issue_id === issueId);
}

/**
 * 算下一次消息刷新的延迟：距上次刷新不超过 debounce，且最迟在 maxWait 内总会
 * 触发一次（避免长期无刷新）；从没刷新过就直接用 debounce。
 */
export function runReviewMessageRefreshDelayMs(
  nowMs: number,
  lastRefreshAtMs: number,
  debounceMs = RUN_REVIEW_MESSAGE_REFRESH_DEBOUNCE_MS,
  maxWaitMs = RUN_REVIEW_MESSAGE_REFRESH_MAX_WAIT_MS,
): number {
  if (lastRefreshAtMs <= 0) return debounceMs;
  const elapsedMs = Math.max(0, nowMs - lastRefreshAtMs);
  if (elapsedMs >= maxWaitMs) return 0;
  return Math.min(debounceMs, maxWaitMs - elapsedMs);
}

/** 从任务列表里取最近结束的那条（completed/failed/cancelled），按结束/开始时间取新。 */
export function latestTerminalAgentTask(tasks: AgentTask[]) {
  return tasks
    .filter((task) => task.status === "completed" || task.status === "failed" || task.status === "cancelled")
    .toSorted((a, b) => taskTimeMs(b) - taskTimeMs(a))[0];
}

/** 任务在时间轴上的排序时间：优先结束时间，其次开始时间，最后创建时间。 */
function taskTimeMs(task: AgentTask) {
  return parseTimeMs(task.completed_at) ?? parseTimeMs(task.started_at) ?? parseTimeMs(task.created_at) ?? 0;
}
