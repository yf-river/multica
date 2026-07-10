import { useEffect, useState } from "react";
import type { AgentTask, IssueTimelineNode, IssueTimelineSummary } from "@multica/core/types";
import type {
  TaskCancelledPayload,
  TaskCompletedPayload,
  TaskDispatchPayload,
  TaskFailedPayload,
  TaskMessagePayload,
  TaskQueuedPayload,
  TaskRunningPayload,
  TaskWaitingLocalDirectoryPayload,
} from "@multica/core/types/events";
import { formatDuration, parseTimeMs } from "./run-review-format";

const RUN_REVIEW_MESSAGE_REFRESH_DEBOUNCE_MS = 1_200;
const RUN_REVIEW_MESSAGE_REFRESH_MAX_WAIT_MS = 4_000;
const RUN_REVIEW_LIVE_DURATION_TICK_MS = 1_000;

export type RunReviewTaskEventPayload =
  | TaskQueuedPayload
  | TaskDispatchPayload
  | TaskRunningPayload
  | TaskWaitingLocalDirectoryPayload
  | TaskCompletedPayload
  | TaskFailedPayload
  | TaskCancelledPayload
  | TaskMessagePayload;

export function runReviewTotalDurationMs(summary: IssueTimelineSummary | undefined): number {
  if (!summary) return 0;
  const agentExecution = summary.agent_execution_duration_ms;
  const childIssueWait = summary.child_issue_wait_duration_ms ?? 0;
  if (agentExecution != null) {
    return agentExecution + (summary.human_confirmation_duration_ms ?? 0) + childIssueWait;
  }
  return summary.total_duration_ms ?? 0;
}

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

export function buildRunReviewLiveTimelineNodes(timelineNodes: IssueTimelineNode[], nowMs: number): IssueTimelineNode[] {
  return timelineNodes.map((node) => {
    if (!isActiveTimelineNode(node)) return node;
    const durationMs = liveElapsedMs(node.started_at, nowMs);
    if (durationMs <= (node.duration_ms ?? 0)) return node;
    return { ...node, duration_ms: durationMs };
  });
}

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

function liveElapsedMs(startedAt: string | null | undefined, nowMs: number) {
  const startedMs = parseTimeMs(startedAt);
  if (startedMs === null || startedMs > nowMs) return 0;
  return nowMs - startedMs;
}

export function hasActiveTimelineNode(timelineNodes: IssueTimelineNode[]) {
  return timelineNodes.some(isActiveTimelineNode);
}

export function isActiveTimelineNode(node: Pick<IssueTimelineNode, "status" | "started_at" | "completed_at">) {
  return isActiveStatus(node.status) && Boolean(node.started_at) && !node.completed_at;
}

export function shouldRefreshRunReviewForTaskEvent(
  issueId: string,
  payload: Pick<RunReviewTaskEventPayload, "issue_id"> | null | undefined,
): boolean {
  return Boolean(issueId && payload?.issue_id === issueId);
}

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

export function isActiveStatus(status: string | undefined) {
  return status === "queued" ||
    status === "dispatched" ||
    status === "waiting_local_directory" ||
    status === "running";
}

export function isActiveTask(task: AgentTask) {
  return isActiveStatus(task.status);
}

export function isRetryableTask(task: AgentTask) {
  return task.status === "failed" || task.status === "cancelled";
}

export function latestTerminalAgentTask(tasks: AgentTask[]) {
  return tasks
    .filter((task) => task.status === "completed" || task.status === "failed" || task.status === "cancelled")
    .toSorted((a, b) => taskTimeMs(b) - taskTimeMs(a))[0];
}

function taskTimeMs(task: AgentTask) {
  return parseTimeMs(task.completed_at) ?? parseTimeMs(task.started_at) ?? parseTimeMs(task.created_at) ?? 0;
}
