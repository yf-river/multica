// 运行复盘里「时间线/条形图」的数据整理层：把执行树的时间线节点按 Agent 阶段、
// 子任务等待、人工确认分组，聚合成条形图的「行 × 段」，并提供各节点 Token /
// 产物 / 展示名等派生数据的纯函数。UI 展示逻辑不在这里。

import type { AgentTaskArtifact, IssueExecutionTreeResponse, IssueTimelineNode } from "@multica/core/types";
import { isActiveAgentTaskStatus } from "@multica/core/agents";
import { normalizeSopStageName, sopStageDisplayName } from "../../common/sop-stage-labels";
import { isActiveTimelineNode } from "./run-review-live";
import { formatDateTime, formatDuration, formatNumber, parseTimeMs, statusLabel } from "./run-review-format";

/** 时间线上某个段的描述（同一条条形可能被拆成多段，如某 Agent 多次运行）。 */
interface TimelineNodeSegment {
  key: string;
  label: string;
  node: IssueTimelineNode;
  ordinal: number;
  total: number;
}

/** 时间线某一行：要么整行就是一个节点（node），要么拆成多段（segments）。 */
export type TimelineNodeRow = {
  key: string;
  label: string;
  node?: IssueTimelineNode;
  segments?: TimelineNodeSegment[];
};
/** 子任务泳道：由执行树根节点的子任务派生而来。 */
export type ChildLane = ReturnType<typeof buildChildLanes>[number];

/** 条形图里一段的展示数据（几何、状态、Token、轮次等）。 */
export interface TimelineBarSegment {
  key: string;
  label: string;
  node: IssueTimelineNode;
  status: string;
  startMs: number | null;
  endMs: number | null;
  durationMs: number;
  tokenTotal: number;
  turns: number;
  ordinal: number;
  total: number;
}

/** 条形图里的一行：阶段 / 子任务 / 人工确认三种之一，内含若干段。 */
export interface TimelineBarRow {
  key: string;
  label: string;
  kind: "stage" | "child" | "human_confirmation";
  status: string;
  subtitle: string;
  segments: TimelineBarSegment[];
  missing: boolean;
}

/**
 * 把时间线节点组装成条形图的行：人工确认、子任务等待、Agent 阶段按顺序排列，
 * 每行内部再按各次运行拆成段。
 */
export function buildTimelineBarRows(
  stageRows: TimelineNodeRow[],
  _childLanes: ChildLane[],
  timelineNodes: IssueTimelineNode[],
): TimelineBarRow[] {
  const stageBars = stageRows.filter((stage) => stage.node).map((stage) => {
    const segments = timelineRowSegments(stage);
    return {
      key: stage.key,
      label: stage.label,
      kind: "stage" as const,
      status: stage.node?.status ?? "missing",
      subtitle: timelineRowSubtitle(stage.node?.status ?? "missing", segments.length),
      segments,
      missing: !stage.node,
    };
  });
  const childIssueNodes = timelineNodes.filter((item) => item.node_type === "child_issue_ref");
  const childIssueSegments = childIssueNodes.map((node, index) => (
    timelineNodeSegment(node.node_id, childIssueSegmentLabel(node), node, index + 1, childIssueNodes.length)
  ));
  const childIssueBars = childIssueSegments.length > 0 ? [{
    key: "child-issue-wait",
    label: "子任务等待",
    kind: "child" as const,
    status: "completed",
    subtitle: timelineRowSubtitle("completed", childIssueSegments.length),
    segments: childIssueSegments,
    missing: false,
  }] : [];
  const humanConfirmationNodes = timelineNodes.filter((item) => item.node_type === "human_confirmation");
  const humanConfirmationSegments = humanConfirmationNodes.map((node, index) => (
    timelineNodeSegment(node.node_id, humanConfirmationSegmentLabel(node), node, index + 1, humanConfirmationNodes.length)
  ));
  const humanConfirmationStatus = humanConfirmationNodes.some((node) => isActiveTimelineNode(node)) ? "running" : "completed";
  const humanConfirmationBars = humanConfirmationSegments.length > 0 ? [{
    key: "human-confirmation",
    label: "人工确认",
    kind: "human_confirmation" as const,
    status: humanConfirmationStatus,
    subtitle: timelineRowSubtitle(humanConfirmationStatus, humanConfirmationSegments.length),
    segments: humanConfirmationSegments,
    missing: false,
  }] : [];
  return [...humanConfirmationBars, ...childIssueBars, ...stageBars];
}

function humanConfirmationSegmentLabel(node: IssueTimelineNode) {
  return node.summary || "人工确认";
}

function childIssueSegmentLabel(node: IssueTimelineNode) {
  return `等待子任务完成：${node.summary || "子任务"}`;
}


/**
 * 生成某一段的 tooltip 键值对：开始/结束/耗时，非「人工确认/子任务」的行再加
 * Token 与执行轮次。
 */
export function timelineSegmentTooltipRows(row: TimelineBarRow, segment: TimelineBarSegment): Array<[string, string]> {
  const rows: Array<[string, string]> = [
    ["节点", row.label],
    ["开始", segment.startMs === null ? "未知" : formatDateTime(segment.startMs)],
    ["结束", segment.endMs === null ? "未知" : formatDateTime(segment.endMs)],
    ["耗时", formatDuration(segment.durationMs)],
  ];
  if (row.kind !== "human_confirmation" && row.kind !== "child") {
    rows.push(["Token", formatNumber(segment.tokenTotal)], ["执行轮次", formatNumber(segment.turns)]);
  }
  return rows;
}

function timelineRowSegments(row: TimelineNodeRow): TimelineBarSegment[] {
  const segments = row.segments?.length
    ? row.segments
    : row.node
      ? [{ key: row.key, label: row.label, node: row.node, ordinal: 1, total: 1 }]
      : [];
  return segments.map((segment) => timelineNodeSegment(segment.key, segment.label, segment.node, segment.ordinal, segment.total));
}

function timelineNodeSegment(
  key: string,
  label: string,
  node: IssueTimelineNode,
  ordinal: number,
  total: number,
): TimelineBarSegment {
  const timing = timelineTiming(node);
  return {
    key,
    label,
    node,
    status: node.status,
    ...timing,
    durationMs: node.duration_ms ?? timing.durationMs,
    tokenTotal: (node.input_tokens ?? 0) + (node.output_tokens ?? 0),
    turns: node.agent_turn_count ?? 0,
    ordinal,
    total,
  };
}

/** 把一段的起止时间换算成在时间轴上的 left/width 百分比样式。 */
export function timelineSegmentStyle(startMs: number, endMs: number, minMs: number, spanMs: number) {
  return {
    left: `${timelineSegmentLeftPercent(startMs, minMs, spanMs)}%`,
    width: `${timelineSegmentWidthPercent(startMs, endMs, spanMs)}%`,
  };
}

function timelineSegmentLeftPercent(startMs: number, minMs: number, spanMs: number) {
  return Math.max(0, ((startMs - minMs) / Math.max(spanMs, 1)) * 100);
}

function timelineSegmentWidthPercent(startMs: number, endMs: number, spanMs: number) {
  return Math.max(0, ((endMs - startMs) / Math.max(spanMs, 1)) * 100);
}


function timelineRowSubtitle(status: string, runCount: number) {
  return runCount > 1 ? `${formatNumber(runCount)} 次 · ${statusLabel(status)}` : statusLabel(status);
}

function timelineTiming(node: IssueTimelineNode | undefined) {
  if (!node) return { startMs: null, endMs: null, durationMs: 0 };
  const start = parseTimeMs(node.started_at);
  const completed = parseTimeMs(node.completed_at);
  const duration = Math.max(node.duration_ms ?? 0, 0);
  if (start === null && completed === null) return { startMs: null, endMs: null, durationMs: duration };
  const fallbackDuration = Math.max(duration, 1);
  const startMs = start ?? Math.max((completed as number) - fallbackDuration, 0);
  const endMs = Math.max(completed ?? startMs + fallbackDuration, startMs);
  return { startMs, endMs, durationMs: Math.max(duration, endMs - startMs) };
}

/** 把时间戳格式化成「HH:mm」时间轴的刻度文案。 */
export function formatTimeTick(value: number) {
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(value));
}

/**
 * 把时间线上的 Agent 任务节点按「Agent」聚合成行：同一 Agent 的多次运行合成
 * 一行、拆多段，段按开始时间排好序。
 */
export function buildTimelineAgentRows(nodes: IssueTimelineNode[]): TimelineNodeRow[] {
  const agentNodes = nodes.filter((node) => node.node_type === "agent_task");
  const grouped = new Map<string, TimelineNodeRow>();
  for (const [index, node] of agentNodes.entries()) {
    const groupKey = timelineAgentRowGroupKey(node, index);
    const label = timelineAgentRowBaseLabel(node, index);
    const taskId = node.node_id.replace(/^task:/, "");
    const existing = grouped.get(groupKey);
    const segment = {
      key: `${groupKey}:${taskId || index}`,
      label,
      node,
      ordinal: 1,
      total: 1,
    };
    if (!existing) {
      grouped.set(groupKey, { key: groupKey, label, node: { ...node }, segments: [segment] });
      continue;
    }
    existing.node = existing.node ? mergeTimelineNode(existing.node, node) : { ...node };
    existing.segments = [...(existing.segments ?? []), segment];
  }

  return [...grouped.values()].map((row) => {
    const segments = [...(row.segments ?? [])].sort(compareTimelineSegments);
    const total = segments.length || 1;
    return {
      ...row,
      segments: segments.map((segment, index) => ({
        ...segment,
        ordinal: index + 1,
        total,
      })),
    };
  });
}

function compareTimelineSegments(left: TimelineNodeSegment, right: TimelineNodeSegment) {
  const leftStart = parseTimeMs(left.node.started_at) ?? parseTimeMs(left.node.completed_at) ?? 0;
  const rightStart = parseTimeMs(right.node.started_at) ?? parseTimeMs(right.node.completed_at) ?? 0;
  if (leftStart !== rightStart) return leftStart - rightStart;
  return left.key.localeCompare(right.key);
}

function timelineAgentRowGroupKey(node: IssueTimelineNode, index: number) {
  const taskId = node.node_id.replace(/^task:/, "");
  const agentName = node.agent_name || taskId || `agent-node-${index + 1}`;
  return node.agent_id || normalizeSopStageName(agentName) || agentName || taskId || `agent-node-${index + 1}`;
}

function timelineAgentRowBaseLabel(node: IssueTimelineNode, index: number) {
  const taskId = node.node_id.replace(/^task:/, "");
  const agentName = node.agent_name || taskId || `agent-node-${index + 1}`;
  return sopStageDisplayName(agentName) || node.summary || agentName;
}

/** 从执行树响应里提取子任务泳道（子任务标题与 issue）。 */
export function buildChildLanes(tree: IssueExecutionTreeResponse | undefined) {
  return (tree?.root?.children ?? []).map((child) => ({
    key: child.issue.id,
    label: child.issue.project?.title || child.issue.title || child.issue.identifier || "子任务",
    issue: child.issue,
  }));
}
/** 产物显示名：优先标题，其次文件名，最后 id。 */
export function artifactDisplayName(artifact: AgentTaskArtifact) {
  return artifact.title || artifact.filename || artifact.id;
}

/** 按语义键去重产物：同「类型+标题+文件名」只留最新创建的一条，并按名排序。 */
export function dedupeArtifacts(artifacts: AgentTaskArtifact[]) {
  const byKey = new Map<string, AgentTaskArtifact>();
  for (const artifact of artifacts) {
    const key = artifactSemanticKey(artifact);
    const existing = byKey.get(key);
    if (!existing || artifactCreatedMs(artifact) >= artifactCreatedMs(existing)) {
      byKey.set(key, artifact);
    }
  }
  return [...byKey.values()].sort(compareArtifactsForDisplay);
}

function artifactSemanticKey(artifact: AgentTaskArtifact) {
  return [
    artifact.kind || "",
    artifact.title || "",
    artifact.filename || "",
  ].join(":").toLowerCase();
}

function artifactCreatedMs(artifact: AgentTaskArtifact) {
  const parsed = Date.parse(artifact.created_at);
  return Number.isFinite(parsed) ? parsed : 0;
}

function compareArtifactsForDisplay(left: AgentTaskArtifact, right: AgentTaskArtifact) {
  const leftName = left.title || left.filename || left.id;
  const rightName = right.title || right.filename || right.id;
  if (leftName !== rightName) return leftName.localeCompare(rightName, "zh-CN");
  return artifactCreatedMs(right) - artifactCreatedMs(left);
}
/**
 * 把 Agent 任务节点聚合为「每个 Agent 一行」的展示数据（用于复盘表格/导出）：
 * 同一 Agent 多次运行会合并成一个节点、统计运行次数与任务 id。
 */
export function buildAgentNodeRows(nodes: IssueTimelineNode[]) {
  const grouped = new Map<string, { key: string; label: string; node: IssueTimelineNode; runCount: number; taskIds: string[] }>();
  for (const [index, node] of nodes.filter((item) => item.node_type === "agent_task").entries()) {
    const rawTaskId = node.node_id.replace(/^task:/, "");
    const agentName = node.agent_name || rawTaskId || `agent-node-${index + 1}`;
    const key = node.agent_id || normalizeSopStageName(agentName) || agentName || rawTaskId || `agent-node-${index + 1}`;
    const label = sopStageDisplayName(agentName) || node.summary || agentName;
    const existing = grouped.get(key);
    if (!existing) {
      grouped.set(key, { key, label, node: { ...node }, runCount: 1, taskIds: rawTaskId ? [rawTaskId] : [] });
      continue;
    }
    existing.node = mergeTimelineNode(existing.node, node);
    existing.runCount += 1;
    if (rawTaskId) existing.taskIds.push(rawTaskId);
  }
  return [...grouped.values()];
}

// 把两次运行合并成一个节点行：数值字段相加、时间取最早开始/最晚结束，作为该
// Agent 在整条时间线上的汇总节点。
function mergeTimelineNode(left: IssueTimelineNode, right: IssueTimelineNode): IssueTimelineNode {
  const leftStart = parseTimeMs(left.started_at);
  const rightStart = parseTimeMs(right.started_at);
  const leftCompleted = parseTimeMs(left.completed_at);
  const rightCompleted = parseTimeMs(right.completed_at);
  const primaryNode = left.node_type === "agent_task" ? left : right.node_type === "agent_task" ? right : left;
  return {
    ...primaryNode,
    status: mergeNodeStatus(left.status, right.status),
    started_at: earliestTime(left.started_at, right.started_at),
    actual_started_at: earliestTime(left.actual_started_at, right.actual_started_at),
    completed_at: latestTime(left.completed_at, right.completed_at),
    duration_ms: (left.duration_ms ?? 0) + (right.duration_ms ?? 0),
    input_tokens: (left.input_tokens ?? 0) + (right.input_tokens ?? 0),
    output_tokens: (left.output_tokens ?? 0) + (right.output_tokens ?? 0),
    cache_read_tokens: (left.cache_read_tokens ?? 0) + (right.cache_read_tokens ?? 0),
    cache_write_tokens: (left.cache_write_tokens ?? 0) + (right.cache_write_tokens ?? 0),
    message_count: (left.message_count ?? 0) + (right.message_count ?? 0),
    agent_turn_count: (left.agent_turn_count ?? 0) + (right.agent_turn_count ?? 0),
    trace_event_count: (left.trace_event_count ?? 0) + (right.trace_event_count ?? 0),
    usage_unavailable_trace: left.usage_unavailable_trace || right.usage_unavailable_trace,
    artifacts: dedupeArtifacts([...(left.artifacts ?? []), ...(right.artifacts ?? [])]),
    evidence_refs: [...(left.evidence_refs ?? []), ...(right.evidence_refs ?? [])],
    summary: rightCompleted !== null && (leftCompleted === null || rightCompleted >= leftCompleted)
      ? right.summary
      : left.summary,
    node_id: primaryNode.node_id,
    node_type: primaryNode.node_type,
    agent_name: left.agent_name || right.agent_name,
    agent_id: left.agent_id || right.agent_id,
    // 各次运行可能重叠，给图表/tooltip 留一个墙上时钟（wall-clock）时长的提示。
    duration_ms_wall_clock: leftStart !== null || rightStart !== null || leftCompleted !== null || rightCompleted !== null
      ? Math.max((latestNumber(leftCompleted, rightCompleted) ?? 0) - (earliestNumber(leftStart, rightStart) ?? 0), 0)
      : undefined,
  } as IssueTimelineNode;
}

function mergeNodeStatus(left: string, right: string) {
  if (left === "failed" || right === "failed" || left === "blocked" || right === "blocked") return "failed";
  if (isActiveAgentTaskStatus(left) || isActiveAgentTaskStatus(right)) return "running";
  if (left === "completed" && right === "completed") return "completed";
  return right || left;
}

function earliestTime(left: string | undefined, right: string | undefined) {
  const leftMs = parseTimeMs(left);
  const rightMs = parseTimeMs(right);
  const ms = earliestNumber(leftMs, rightMs);
  return ms === null ? left || right || "" : new Date(ms).toISOString();
}

function latestTime(left: string | undefined, right: string | undefined) {
  const leftMs = parseTimeMs(left);
  const rightMs = parseTimeMs(right);
  const ms = latestNumber(leftMs, rightMs);
  return ms === null ? left || right || "" : new Date(ms).toISOString();
}

function earliestNumber(...values: Array<number | null>) {
  const present = values.filter((value): value is number => value !== null);
  return present.length ? Math.min(...present) : null;
}

function latestNumber(...values: Array<number | null>) {
  const present = values.filter((value): value is number => value !== null);
  return present.length ? Math.max(...present) : null;
}
/** 一个节点输入/输出/缓存读/缓存写 Token 的总和。 */
export function nodeTokenTotal(node: IssueTimelineNode | undefined) {
  if (!node) return 0;
  return node.input_tokens + node.output_tokens + node.cache_read_tokens + node.cache_write_tokens;
}

/** Agent 行在表格里的展示名：运行多次时附上次数，如「前端开发 (3 次)」。 */
export function agentNodeDisplayLabel(row: ReturnType<typeof buildAgentNodeRows>[number]) {
  return row.runCount > 1 ? `${row.label} (${row.runCount} 次)` : row.label;
}
