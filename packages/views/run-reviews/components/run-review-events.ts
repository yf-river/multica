import type {
  AgentTask,
  IssueExecutionNode,
  IssueExecutionTreeResponse,
  IssueTimelineNode,
  TaskTraceEvent,
} from "@multica/core/types";
import type { TaskMessagePayload } from "@multica/core/types/events";
import type { PromptEvaluationToolCallChain } from "@multica/core/types/prompt-evaluation";
import { usageTokenTotal } from "../../runtimes/utils";
import {
  firstNonEmpty,
  formatDateTime,
  formatJSON,
  parseTimeMs,
  shortId,
  stringFromUnknown,
  statusLabel,
  truncateText,
} from "./run-review-format";

export function filterRunReviewEventRows(eventRows: RunReviewEventRowData[], query: string): RunReviewEventRowData[] {
  const q = query.trim().toLowerCase();
  if (!q) return eventRows;
  return eventRows.filter((event) => runReviewEventSearchText(event).toLowerCase().includes(q));
}

export function filterVisibleRunReviewEventRows(eventRows: RunReviewEventRowData[]): RunReviewEventRowData[] {
  const seenUserInputSnapshots = new Set<string>();
  return eventRows.filter((event) => {
    if (!shouldShowRunReviewEventRow(event)) return false;
    if (isDuplicateVisibleUserInputSnapshot(event, seenUserInputSnapshots)) return false;
    return true;
  });
}

function shouldShowRunReviewEventRow(event: RunReviewEventRowData): boolean {
  if (event.severity !== "normal") return true;
  if (event.kind === "message" || event.kind === "tool") return true;

  if (event.kind === "trace") {
    const eventType = event.object.toLowerCase();
    if (event.category === "输入" || eventType === "user_input.received") return true;
    if (eventType.includes("source") || eventType.includes("fetch")) return true;
    if (eventType === "task.failed" || eventType === "task.cancelled" || eventType === "task.blocked") return true;
    if (eventType === "llm.usage_unavailable" || eventType === "task.waiting_local_directory") return true;
    return !RUN_REVIEW_NOISY_TRACE_EVENT_TYPES.has(eventType);
  }

  if (event.kind === "node") {
    const nodeType = event.object.toLowerCase();
    return RUN_REVIEW_VISIBLE_NODE_TYPES.has(nodeType);
  }

  return true;
}

function isDuplicateVisibleUserInputSnapshot(event: RunReviewEventRowData, seen: Set<string>): boolean {
  if (event.kind !== "trace" || event.severity !== "normal") return false;
  const raw = event.rawPayload as Partial<TaskTraceEvent> | undefined;
  if (raw?.event_type !== "user_input.received") return false;
  const metadata = raw.metadata ?? {};
  const snapshot = stringFromUnknown(metadata.content_snapshot).trim();
  if (!snapshot) return false;
  const key = [
    stringFromUnknown(metadata.input_kind),
    stringFromUnknown(metadata.source_url),
    snapshot,
  ].join("\n");
  if (seen.has(key)) return true;
  seen.add(key);
  return false;
}

const RUN_REVIEW_NOISY_TRACE_EVENT_TYPES = new Set([
  "task.queued",
  "task.dispatched",
  "task.started",
  "task.completed",
  "llm.usage_reported",
]);

const RUN_REVIEW_VISIBLE_NODE_TYPES = new Set([
  "human_confirmation",
  "child_issue_ref",
  "source_fetch",
]);

function runReviewEventSearchText(event: RunReviewEventRowData) {
  return [
    event.id,
    event.kind,
    event.category,
    event.outcome,
    event.title,
    event.summary,
    event.detail,
    event.metadataDetail,
    event.sourceLabel,
    event.object,
    event.taskId,
    event.rawSourceLabel,
  ].filter(Boolean).join(" ");
}

export interface RunReviewEventRowData {
  id: string;
  kind: "message" | "trace" | "tool" | "node";
  category: string;
  timestampMs: number;
  timeLabel: string;
  taskId?: string;
  sourceLabel: string;
  object: string;
  title: string;
  outcome: string;
  summary: string;
  detail: string;
  metadataDetail: string;
  durationMs: number;
  tokenTotal: number;
  severity: "normal" | "warning" | "error";
  rawSourceLabel?: string;
  rawPayload?: unknown;
  linkedRawPayloads?: Array<{ label: string; payload: unknown }>;
}

export interface RunReviewEventGroupData {
  key: string;
  label: string;
  taskId?: string;
  events: RunReviewEventRowData[];
  startMs: number | null;
  endMs: number | null;
  timeRangeLabel: string;
  tokenTotal: number;
  severity: "normal" | "warning" | "error";
  outcome: string;
}

export function buildRunReviewEventGroups(
  eventRows: RunReviewEventRowData[],
  taskLabelById: Map<string, string> = new Map(),
): RunReviewEventGroupData[] {
  const grouped = new Map<string, { key: string; label: string; taskId?: string; events: RunReviewEventRowData[] }>();
  for (const event of eventRows) {
    const key = event.taskId
      ? `task:${event.taskId}`
      : `system:${event.rawSourceLabel || event.kind}:${event.sourceLabel || event.object || event.id}`;
    const existing = grouped.get(key);
    if (existing) {
      existing.events.push(event);
      continue;
    }
    const label = event.taskId
      ? taskLabelById.get(event.taskId) || `任务 ${shortId(event.taskId)}`
      : firstNonEmpty(event.sourceLabel, event.category, event.object, "系统事件");
    grouped.set(key, { key, label, taskId: event.taskId, events: [event] });
  }

  return [...grouped.values()]
    .map((group) => {
      const events = group.events.toSorted((left, right) => eventSortTime(left) - eventSortTime(right) || left.id.localeCompare(right.id));
      const times = events.map((event) => event.timestampMs).filter((value) => value > 0);
      const startMs = times.length ? Math.min(...times) : null;
      const endMs = times.length ? Math.max(...times) : null;
      const severity = groupSeverity(events);
      return {
        ...group,
        events,
        startMs,
        endMs,
        timeRangeLabel: formatEventGroupTimeRange(startMs, endMs),
        tokenTotal: events.reduce((total, event) => total + event.tokenTotal, 0),
        severity,
        outcome: severity === "error" ? "异常线索" : severity === "warning" ? "需关注" : "正常",
      };
    })
    .toSorted((left, right) => eventGroupSortTime(left) - eventGroupSortTime(right) || left.label.localeCompare(right.label));
}

export function buildEventTaskLabelById(timelineNodes: IssueTimelineNode[]) {
  const labels = new Map<string, string>();
  for (const node of timelineNodes) {
    if (node.node_type !== "agent_task") continue;
    const taskId = node.node_id.replace(/^task:/, "");
    if (!taskId) continue;
    labels.set(taskId, firstNonEmpty(timelineTaskIntentLabel(node), node.agent_name, `任务 ${shortId(taskId)}`));
  }
  return labels;
}

function timelineTaskIntentLabel(node: IssueTimelineNode) {
  const summary = cleanSemanticMarkdownLine(node.summary ?? "");
  if (!summary) return "";
  const lowerSummary = summary.toLowerCase();
  const agentName = node.agent_name?.trim() ?? "";
  if (agentName && summary === agentName) return "";
  if (lowerSummary.startsWith("sop leader:")) return "";
  if (summary === "SOP leader task") return "";
  if (summary.startsWith("Agent task ")) return "";
  return truncateText(summary, 72);
}

function cleanSemanticMarkdownLine(value: string) {
  const line = value
    .split("\n")
    .map((rawLine) => {
      let next = rawLine.trim();
      if (!next || isNonSemanticMarkdownLine(next)) return "";
      next = next.replace(/^#+\s*/, "").replace(/\*\*/g, "").replace(/^`+|`+$/g, "").trim();
      if (!next || isNonSemanticMarkdownLine(next)) return "";
      return next;
    })
    .find(Boolean);
  return line ?? "";
}

function isNonSemanticMarkdownLine(value: string) {
  return value === "---" || value === "..." || /^```/.test(value) || value.replace(/[-=_*`~\s]/g, "") === "";
}

function eventSortTime(event: RunReviewEventRowData) {
  return event.timestampMs > 0 ? event.timestampMs : Number.MAX_SAFE_INTEGER;
}

function eventGroupSortTime(group: RunReviewEventGroupData) {
  return group.startMs ?? Number.MAX_SAFE_INTEGER;
}

function groupSeverity(events: RunReviewEventRowData[]): RunReviewEventGroupData["severity"] {
  if (events.some((event) => event.severity === "error")) return "error";
  if (events.some((event) => event.severity === "warning")) return "warning";
  return "normal";
}

function formatEventGroupTimeRange(startMs: number | null, endMs: number | null) {
  if (startMs === null) return "";
  if (endMs === null || endMs === startMs) return formatDateTime(startMs);
  return `${formatDateTime(startMs)} - ${formatDateTime(endMs)}`;
}

export function buildRunReviewEventRows(
  tree: IssueExecutionTreeResponse | undefined,
  timelineNodes: IssueTimelineNode[],
): RunReviewEventRowData[] {
  const nodes = flattenExecutionNodes(tree);
  const rows: RunReviewEventRowData[] = [];
  const hasSourceFetchTrace = nodes.some((node) => (node.trace_events ?? []).some(isSourceFetchTrace));
  for (const node of nodes) {
    const messageByKey = new Map((node.task_messages ?? []).map((message) => [toolMessageKey(message.task_id, message.seq), message]));
    const coveredToolMessageKeys = new Set<string>();
    for (const chain of node.tool_call_chains ?? []) {
      if (chain.task_id && chain.use_seq) coveredToolMessageKeys.add(toolMessageKey(chain.task_id, chain.use_seq));
      if (chain.task_id && chain.result_seq) coveredToolMessageKeys.add(toolMessageKey(chain.task_id, chain.result_seq));
    }
    for (const message of node.task_messages ?? []) {
      if ((message.type === "tool_use" || message.type === "tool_result") && coveredToolMessageKeys.has(toolMessageKey(message.task_id, message.seq))) {
        continue;
      }
      rows.push(message.type === "tool_use" || message.type === "tool_result"
        ? runReviewToolMessageEvent(message)
        : runReviewMessageEvent(message));
    }
    for (const event of node.trace_events ?? []) {
      rows.push(runReviewTraceEvent(event));
    }
    for (const chain of node.tool_call_chains ?? []) {
      const linkedMessages = chain.task_id
        ? [
            chain.use_seq ? messageByKey.get(toolMessageKey(chain.task_id, chain.use_seq)) : undefined,
            chain.result_seq ? messageByKey.get(toolMessageKey(chain.task_id, chain.result_seq)) : undefined,
          ].filter(Boolean) as TaskMessagePayload[]
        : [];
      rows.push(runReviewToolEvent(chain, linkedMessages));
    }
  }
  for (const node of timelineNodes) {
    if (node.node_type === "agent_task" || node.node_type === "tool_call" || node.node_type === "status_change") continue;
    if (node.node_type === "source_fetch" && hasSourceFetchTrace) continue;
    rows.push(runReviewTimelineNodeEvent(node));
  }
  return rows
    .toSorted((a, b) => {
      if (a.timestampMs !== b.timestampMs) return b.timestampMs - a.timestampMs;
      return a.id.localeCompare(b.id);
    });
}
export function toolMessageKey(taskId: string, seq: number) {
  return `${taskId}:${seq}`;
}
export function flattenExecutionNodes(tree: IssueExecutionTreeResponse | undefined): IssueExecutionNode[] {
  if (!tree) return [];
  const result: IssueExecutionNode[] = [];
  const walk = (node: IssueExecutionNode) => {
    result.push(node);
    for (const child of node.children ?? []) walk(child);
  };
  walk(tree.root);
  return result;
}
export function flattenExecutionTasks(tree: IssueExecutionTreeResponse | undefined): AgentTask[] {
  return flattenExecutionNodes(tree).flatMap((node) => node.tasks ?? []);
}

function runReviewMessageEvent(message: TaskMessagePayload): RunReviewEventRowData {
  const detailParts = [
    message.content ? `content:\n${message.content}` : "",
    message.input ? `input:\n${formatJSON(message.input)}` : "",
    message.output ? `output:\n${message.output}` : "",
  ].filter(Boolean);
  const summary = firstNonEmpty(
    taskMessageText(message),
    message.input ? formatJSON(message.input) : "",
    "任务消息未记录正文",
  );
  const failure = message.type === "error" || hasFailureSignal(summary);
  return {
    id: `message:${message.task_id}:${message.seq}`,
    kind: "message",
    category: taskMessageKindLabel(message.type),
    timestampMs: parseTimeMs(message.created_at) ?? 0,
    timeLabel: formatEventTime(message.created_at),
    taskId: message.task_id,
    sourceLabel: "模型输出",
    object: `消息 #${message.seq}`,
    title: taskMessageKindLabel(message.type),
    outcome: message.type === "error" ? "异常" : "已记录",
    summary: conciseEventSummary(summary, failure),
    detail: detailParts.join("\n\n"),
    metadataDetail: "",
    durationMs: 0,
    tokenTotal: 0,
    severity: failure ? "error" : "normal",
    rawSourceLabel: "task_message",
    rawPayload: message,
  };
}

function runReviewToolMessageEvent(message: TaskMessagePayload): RunReviewEventRowData {
  const semantic = semanticToolAction(message.tool, message.input, message.output);
  const detailParts = [
    message.tool ? `raw_tool: ${message.tool}` : "",
    message.input ? `input:\n${formatJSON(message.input)}` : "",
    message.output ? `output:\n${message.output}` : "",
  ].filter(Boolean);
  return {
    id: `message:${message.task_id}:${message.seq}`,
    kind: "tool",
    category: semantic.category,
    timestampMs: parseTimeMs(message.created_at) ?? 0,
    timeLabel: formatEventTime(message.created_at),
    taskId: message.task_id,
    sourceLabel: semantic.sourceLabel,
    object: semantic.object,
    title: semantic.title,
    outcome: semantic.outcome,
    summary: semantic.summary,
    detail: detailParts.join("\n\n"),
    metadataDetail: "",
    durationMs: 0,
    tokenTotal: 0,
    severity: semantic.severity,
    rawSourceLabel: "task_message",
    rawPayload: message,
  };
}

function isSourceFetchTrace(event: TaskTraceEvent): boolean {
  const type = event.event_type.toLowerCase();
  return type.includes("source") || type.includes("fetch");
}

function runReviewTraceEvent(event: TaskTraceEvent): RunReviewEventRowData {
  const tokenTotal = usageTokenTotal(event);
  const durationMs = event.run_ms ?? event.duration_ms ?? event.total_ms ?? event.queue_wait_ms ?? 0;
  const failure = [event.failure_reason, event.error_type].filter(Boolean).join(" · ");
  const metadata = event.metadata ?? {};
  const isUserInput = event.event_type === "user_input.received";
  const userInputKind = stringFromUnknown(metadata.input_kind);
  const userInputSummary = stringFromUnknown(metadata.summary);
  const userInputSnapshot = stringFromUnknown(metadata.content_snapshot);
  const sourceLabel = isUserInput
    ? [event.source, userInputKind].filter(Boolean).join(" / ") || event.event_type
    : [event.source, event.provider, event.model].filter(Boolean).join(" / ") || event.event_type;
  return {
    id: `trace:${event.id}`,
    kind: "trace",
    category: isUserInput ? "输入" : "Trace",
    timestampMs: parseTimeMs(event.created_at) ?? 0,
    timeLabel: formatEventTime(event.created_at),
    taskId: event.task_id,
    sourceLabel,
    object: isUserInput ? userInputKind || event.event_type : event.event_type,
    title: isUserInput ? "用户原始输入" : event.event_name || traceEventStageLabel(event.event_type),
    outcome: failure || event.event_type === "task.failed" ? "异常" : statusLabel(event.status),
    summary: isUserInput ? conciseEventSummary(userInputSummary, false) : failure ? conciseEventSummary(failure, true) : "",
    detail: isUserInput && userInputSnapshot ? `原文快照：\n${userInputSnapshot}` : "",
    metadataDetail: Object.keys(metadata).length > 0 ? `metadata:\n${formatJSON(metadata)}` : "",
    durationMs,
    tokenTotal,
    severity: event.event_type === "task.failed" || Boolean(failure) ? "error" : "normal",
    rawSourceLabel: "task_trace_event",
    rawPayload: event,
  };
}

function runReviewToolEvent(chain: PromptEvaluationToolCallChain, linkedMessages: TaskMessagePayload[] = []): RunReviewEventRowData {
  const semantic = semanticToolAction(chain.tool, chain.input, chain.output);
  const backendFailure = chain.failure_signal && !semantic.suppressFailureSignal;
  const detailParts = [
    chain.tool ? `raw_tool: ${chain.tool}` : "",
    chain.input ? `input:\n${formatJSON(chain.input)}` : "",
    chain.output ? `output:\n${chain.output}` : "",
    chain.failure_reason && backendFailure ? `failure:\n${chain.failure_reason}` : "",
  ].filter(Boolean);
  return {
    id: `tool:${chain.task_id || "unknown-task"}:${chain.id}`,
    kind: "tool",
    category: semantic.category,
    timestampMs: parseTimeMs(chain.completed_at) ?? parseTimeMs(chain.created_at) ?? 0,
    timeLabel: formatEventTime(chain.completed_at || chain.created_at),
    taskId: chain.task_id,
    sourceLabel: semantic.sourceLabel,
    object: semantic.object,
    title: semantic.title,
    outcome: semantic.outcome,
    summary: backendFailure ? conciseEventSummary(firstNonEmpty(chain.failure_reason, semantic.summary), true) : semantic.summary,
    detail: detailParts.join("\n\n"),
    metadataDetail: "",
    durationMs: chain.duration_ms ?? 0,
    tokenTotal: 0,
    severity: backendFailure ? "error" : chain.status === "缺少结果" || chain.status === "孤立结果" ? "warning" : semantic.severity,
    rawSourceLabel: "tool_call_chain",
    rawPayload: chain,
    linkedRawPayloads: linkedMessages.map((message) => ({
      label: `关联 task_message #${message.seq} ${taskMessageKindLabel(message.type)}`,
      payload: message,
    })),
  };
}

interface SemanticToolAction {
  category: string;
  sourceLabel: string;
  object: string;
  title: string;
  outcome: string;
  summary: string;
  severity: RunReviewEventRowData["severity"];
  suppressFailureSignal?: boolean;
}

export function semanticToolAction(tool: string | undefined, input: Record<string, unknown> | undefined, output: string | undefined): SemanticToolAction {
  const command = stringFromUnknown(input?.command);
  if (command) return semanticCommandAction(command, output);

  const path = firstNonEmpty(
    stringFromUnknown(input?.file_path),
    stringFromUnknown(input?.path),
    stringFromUnknown(input?.target_file),
  );
  if (path) {
    const action = semanticFileToolAction(tool);
    const outputSignal = output ? outputOutcome(output) : null;
    if (outputSignal?.severity === "error") {
      return {
        category: action.category,
        sourceLabel: action.sourceLabel,
        object: shortPath(path),
        title: `${action.titlePrefix}：${shortPath(path)}`,
        outcome: outputSignal.outcome,
        summary: outputSignal.summary,
        severity: outputSignal.severity,
      };
    }
    return {
      category: action.category,
      sourceLabel: action.sourceLabel,
      object: shortPath(path),
      title: `${action.titlePrefix}：${shortPath(path)}`,
      outcome: "已记录",
      summary: "",
      severity: "normal",
      suppressFailureSignal: true,
    };
  }

  const query = firstNonEmpty(stringFromUnknown(input?.query), stringFromUnknown(input?.pattern));
  if (query) {
    const outputSignal = output ? outputOutcome(output) : null;
    if (outputSignal?.severity === "error") {
      return {
        category: "搜索",
        sourceLabel: "搜索",
        object: truncateText(query, 96),
        title: `搜索：${truncateText(query, 96)}`,
        outcome: outputSignal.outcome,
        summary: outputSignal.summary,
        severity: outputSignal.severity,
      };
    }
    return {
      category: "搜索",
      sourceLabel: "搜索",
      object: truncateText(query, 96),
      title: `搜索：${truncateText(query, 96)}`,
      outcome: "已记录",
      summary: "",
      severity: "normal",
      suppressFailureSignal: true,
    };
  }

  const outputSignal = output ? outputOutcome(output) : null;
  if (tool === "patch_apply") {
    return {
      category: "修改",
      sourceLabel: "代码修改",
      object: "",
      title: "应用代码修改",
      outcome: outputSignal?.outcome ?? "已记录",
      summary: outputSignal?.summary ?? "",
      severity: outputSignal?.severity ?? "normal",
      suppressFailureSignal: outputSignal?.suppressFailureSignal,
    };
  }

  const fallback = readableToolName(tool);
  return {
    category: fallback === "工具调用" ? "工具" : fallback,
    sourceLabel: fallback,
    object: "",
    title: fallback,
    outcome: outputSignal?.outcome ?? "已记录",
    summary: outputSignal?.summary ?? "",
    severity: outputSignal?.severity ?? "normal",
    suppressFailureSignal: outputSignal?.suppressFailureSignal,
  };
}

function semanticCommandAction(command: string, output: string | undefined): SemanticToolAction {
  const normalized = command.trim();
  const segment = meaningfulCommandSegment(normalized);
  const executable = commandExecutable(segment);

  if (executable === "rg" || executable === "grep" || executable === "find" || isGitSubcommand(segment, "grep")) {
    return {
      category: "搜索",
      sourceLabel: "代码搜索",
      object: searchQueryFromCommand(segment) || truncateText(segment, 96),
      title: `搜索代码：${searchQueryFromCommand(segment) || truncateText(segment, 96)}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (["sed", "cat", "nl", "ls", "head", "tail"].includes(executable) || isGitReadCommand(segment)) {
    return {
      category: "查看",
      sourceLabel: "读取上下文",
      object: readTargetFromCommand(segment) || truncateText(segment, 96),
      title: `${readCommandTitlePrefix(segment, executable)}：${readTargetFromCommand(segment) || truncateText(segment, 96)}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (isPnpmTypecheck(segment)) {
    return {
      category: "验证",
      sourceLabel: "类型检查",
      object: pnpmFilterFromCommand(segment) || "TypeScript",
      title: `运行类型检查：${pnpmFilterFromCommand(segment) || "TypeScript"}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (isPnpmTest(segment)) {
    return {
      category: "验证",
      sourceLabel: "前端测试",
      object: testTargetFromCommand(segment) || pnpmFilterFromCommand(segment) || "Vitest",
      title: `运行前端单测：${testTargetFromCommand(segment) || pnpmFilterFromCommand(segment) || "Vitest"}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (isGoTest(segment)) {
    const target = goTestTargetFromCommand(segment);
    return {
      category: "验证",
      sourceLabel: "后端测试",
      object: target,
      title: `运行后端单测：${target}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (isBuildCommand(segment)) {
    return {
      category: "构建",
      sourceLabel: "构建",
      object: truncateText(segment, 96),
      title: `运行构建：${truncateText(segment, 96)}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (executable === "curl" || executable === "http" || executable === "wget") {
    return {
      category: "接口",
      sourceLabel: "接口检查",
      object: httpTargetFromCommand(segment) || truncateText(segment, 96),
      title: `检查接口：${httpTargetFromCommand(segment) || truncateText(segment, 96)}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (executable === "make" && (segment.includes("dev") || segment.includes("start") || segment.includes("server"))) {
    return {
      category: "服务",
      sourceLabel: "运行服务",
      object: truncateText(segment, 96),
      title: `运行服务：${truncateText(segment, 96)}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (executable === "make" && (segment.includes("check") || segment.includes("verify") || segment.includes("test"))) {
    return {
      category: "验证",
      sourceLabel: "验证",
      object: truncateText(segment, 96),
      title: `运行验证：${truncateText(segment, 96)}`,
      ...semanticOutputState(output, segment),
    };
  }

  return {
    category: "命令",
    sourceLabel: "命令执行",
    object: truncateText(segment || normalized, 96),
    title: `执行命令：${truncateText(segment || normalized, 96)}`,
    ...semanticOutputState(output, segment),
  };
}

function semanticOutputState(
  output: string | undefined,
  command?: string,
): Pick<SemanticToolAction, "outcome" | "summary" | "severity" | "suppressFailureSignal"> {
  if (!output) return { outcome: "已记录", summary: "", severity: "normal" };
  return outputOutcome(output, command);
}

function outputOutcome(
  output: string,
  command?: string,
): Pick<SemanticToolAction, "outcome" | "summary" | "severity" | "suppressFailureSignal"> {
  const normalizedOutput = toolOutputText(output);
  if (toolOutputHasToolUseError(normalizedOutput)) {
    return { outcome: "异常线索", summary: conciseEventSummary("工具调用返回错误", true), severity: "error" };
  }
  const exitCode = toolOutputExitCode(normalizedOutput);
  if (exitCode !== null) {
    if (exitCode === 0) {
      return { outcome: "已返回", summary: conciseEventSummary(summarizeToolOutput(normalizedOutput), false), severity: "normal", suppressFailureSignal: true };
    }
    return {
      outcome: "异常线索",
      summary: conciseEventSummary(`Exit Code: ${exitCode}`, true),
      severity: "error",
    };
  }
  if (
    toolOutputHasSuccessfulExitCode(normalizedOutput) ||
    outputHasOnlyBenignFailureCounters(normalizedOutput) ||
    outputIsReadOnlyCommandContent(command, normalizedOutput)
  ) {
    return { outcome: "已返回", summary: conciseEventSummary(summarizeToolOutput(normalizedOutput), false), severity: "normal", suppressFailureSignal: true };
  }
  const errorLine = extractErrorLine(normalizedOutput);
  if (errorLine) {
    return { outcome: "异常线索", summary: conciseEventSummary(errorLine, true), severity: "error" };
  }
  const httpStatus = toolOutputHTTPStatusCode(normalizedOutput);
  if (httpStatus !== null) {
    return {
      outcome: "异常线索",
      summary: conciseEventSummary(`HTTP ${httpStatus}`, true),
      severity: "error",
    };
  }
  return {
    outcome: "已返回",
    summary: conciseEventSummary(summarizeToolOutput(normalizedOutput), false),
    severity: "normal",
    suppressFailureSignal: true,
  };
}

export function toolOutputText(output: string) {
  const trimmed = output.trim();
  if (!trimmed.startsWith("[")) return output;
  try {
    const parts = JSON.parse(trimmed) as unknown;
    if (!Array.isArray(parts)) return output;
    const texts = parts
      .map((part) => part && typeof part === "object" && "text" in part ? stringFromUnknown((part as { text?: unknown }).text).trim() : "")
      .filter(Boolean);
    return texts.length ? texts.join("\n") : output;
  } catch {
    return output;
  }
}

function toolOutputHasSuccessfulExitCode(output: string) {
  return /\bExit Code:\s*0\b/i.test(output) || /\bexit\s+(?:status|code)\s*[:=]?\s*0\b/i.test(output);
}

function toolOutputExitCode(output: string) {
  const patterns = [
    /\bExit Code:\s*(\d+)\b/i,
    /\bexit\s+(?:status|code)\s*[:=]?\s*(\d+)\b/i,
    /\bexited\s+with\s+(?:status|code)\s*[:=]?\s*(\d+)\b/i,
  ];
  for (const pattern of patterns) {
    const match = pattern.exec(output);
    if (!match?.[1]) continue;
    const value = Number.parseInt(match[1], 10);
    if (Number.isFinite(value)) return value;
  }
  return null;
}

function toolOutputHTTPStatusCode(output: string) {
  const patterns = [
    /\bhttp(?:\/[\d.]+)?\s*(?:status\s*)?([45]\d{2})\b/i,
    /\bstatus(?:\s*code)?\s*[:=]?\s*([45]\d{2})\b/i,
  ];
  for (const pattern of patterns) {
    const match = pattern.exec(output);
    if (!match?.[1]) continue;
    const value = Number.parseInt(match[1], 10);
    if (Number.isFinite(value)) return value;
  }
  return null;
}

function toolOutputHasToolUseError(output: string) {
  return output.toLowerCase().includes("<tool_use_error>");
}

function outputHasOnlyBenignFailureCounters(output: string) {
  const lower = output.toLowerCase();
  if (/(?:\berror:|\bexception\b|\bpanic:|\bfatal\b|\bpermission denied\b|\bprovider timeout\b|\btimed out\b|\bhttp\s+[45]\d\d\b|\bstatus\s+[45]\d\d\b|错误|异常|超时|无权限|权限拒绝)/i.test(output)) {
    return false;
  }
  return isBenignFailureCounterLine(lower);
}

function outputIsReadOnlyCommandContent(command: string | undefined, output: string) {
  const normalizedCommand = meaningfulCommandSegment(command ?? "").toLowerCase();
  if (!normalizedCommand) return false;
  const isReadOnlyGitCommand = normalizedCommand.startsWith("git diff") ||
    normalizedCommand.startsWith("git branch") ||
    normalizedCommand.startsWith("git show") ||
    normalizedCommand.startsWith("git status") ||
    normalizedCommand.startsWith("git log");
  const executable = commandExecutable(normalizedCommand);
  const isReadOnlyShellCommand = ["cat", "sed", "nl", "ls", "head", "tail", "rg", "grep", "find"].includes(executable);
  const isCommentListCommand = normalizedCommand.startsWith("multica issue comment list");
  const isLocalArtifactRead = ["curl", "wget"].includes(executable) &&
    (normalizedCommand.includes("/uploads/") || normalizedCommand.includes("/api/attachments/"));
  if (!isReadOnlyGitCommand && !isReadOnlyShellCommand && !isCommentListCommand && !isLocalArtifactRead) return false;
  return !toolOutputHasNonEmptyStderr(output);
}

function toolOutputHasNonEmptyStderr(output: string) {
  return output.split("\n").some((line) => {
    const match = /^\s*stderr:\s*(.*)$/i.exec(line);
    if (!match) return false;
    const value = match[1]?.trim() ?? "";
    return value !== "" && value !== "(empty)";
  });
}

export function extractErrorLine(output: string) {
  const patterns = [
    /^\s*Error:\s*.+/i,
    /^\s*Traceback\b.*/i,
    /^\s*RuntimeError:\s*.+/i,
    /^\s*Exception\b.*/i,
    /^\s*--- FAIL[:\s].+/i,
    /^\s*FAIL(?:\s|:).+/i,
    /^\s*panic:\s*.+/i,
    /^\s*FATAL\b.+/i,
    /^\s*make(?:\[\d+\])?: \*\*\* .*\bError\s+\d+\b.*/i,
    /^\s*command failed\b.*/i,
  ];
  const lines = output.split("\n").map((line) => line.trim()).filter(Boolean);
  for (const pattern of patterns) {
    const line = lines.find((item) => pattern.test(item));
    if (line) return line;
  }
  return "";
}

function conciseEventSummary(value: string, isFailure: boolean) {
  const text = truncateText(value, 220);
  if (!text) return "";
  return isFailure ? `异常摘要：${text}` : text;
}

function meaningfulCommandSegment(command: string) {
  const segments = command
    .split(/\s+(?:&&|\|\|)\s+|;\s*/g)
    .map((item) => item.trim())
    .filter(Boolean);
  const candidate = segments.find((segment) => {
    const executable = commandExecutable(segment);
    return executable !== "cd" && executable !== "export" && executable !== "";
  });
  return candidate ?? command.trim();
}

function commandExecutable(command: string) {
  const tokens = shellWords(command);
  const executable = tokens.find((token) => {
    if (!token) return false;
    if (/^[A-Z_][A-Z0-9_]*=/.test(token)) return false;
    return true;
  });
  return stripCommandPath(executable ?? "").toLowerCase();
}

function shellWords(command: string) {
  return command.match(/"[^"]*"|'[^']*'|\S+/g)?.map((token) => token.replace(/^["']|["']$/g, "")) ?? [];
}

function stripCommandPath(value: string) {
  const parts = value.split("/");
  return parts[parts.length - 1] ?? value;
}

function isGitSubcommand(command: string, subcommand: string) {
  const tokens = shellWords(command);
  const gitIndex = tokens.findIndex((token) => stripCommandPath(token).toLowerCase() === "git");
  return gitIndex >= 0 && tokens[gitIndex + 1] === subcommand;
}

function isGitReadCommand(command: string) {
  const tokens = shellWords(command);
  const gitIndex = tokens.findIndex((token) => stripCommandPath(token).toLowerCase() === "git");
  const subcommand = gitIndex >= 0 ? tokens[gitIndex + 1] : "";
  return ["show", "diff", "status", "log", "branch"].includes(subcommand ?? "");
}

function searchQueryFromCommand(command: string) {
  const tokens = shellWords(command);
  const executable = commandExecutable(command);
  if (executable === "find") return tokens.find((token) => token !== "find" && !token.startsWith("-")) ?? "";
  const startIndex = isGitSubcommand(command, "grep") ? tokens.findIndex((token) => token === "grep") + 1 : 1;
  return tokens.slice(startIndex).find((token) => !token.startsWith("-") && !token.includes("=")) ?? "";
}

function readCommandTitlePrefix(command: string, executable: string) {
  if (isGitReadCommand(command)) return "查看 Git 信息";
  if (executable === "ls") return "查看目录";
  return "查看文件";
}

function readTargetFromCommand(command: string) {
  const tokens = shellWords(command);
  const candidates = tokens.filter((token) => !token.startsWith("-") && !/^\d/.test(token) && !token.includes("="));
  const target = candidates[candidates.length - 1];
  if (!target || target === commandExecutable(command)) return "";
  return shortPath(target);
}

function isPnpmTypecheck(command: string) {
  return commandExecutable(command) === "pnpm" && /\btypecheck\b/.test(command);
}

function isPnpmTest(command: string) {
  return commandExecutable(command) === "pnpm" && (/\btest\b/.test(command) || /\bvitest\b/.test(command));
}

function isGoTest(command: string) {
  return commandExecutable(command) === "go" && /\btest\b/.test(command);
}

function isBuildCommand(command: string) {
  const executable = commandExecutable(command);
  return (executable === "pnpm" || executable === "go" || executable === "make") && /\bbuild\b/.test(command);
}

function pnpmFilterFromCommand(command: string) {
  const tokens = shellWords(command);
  const index = tokens.indexOf("--filter");
  return index >= 0 ? tokens[index + 1] ?? "" : "";
}

function testTargetFromCommand(command: string) {
  const tokens = shellWords(command);
  const runIndex = tokens.indexOf("run");
  const runTarget = runIndex >= 0 ? tokens[runIndex + 1] : undefined;
  if (runTarget) return shortPath(runTarget);
  const testIndex = tokens.indexOf("test");
  const testTarget = testIndex >= 0 ? tokens[testIndex + 1] : undefined;
  if (testTarget) return shortPath(testTarget);
  return "";
}

function goTestTargetFromCommand(command: string) {
  const tokens = shellWords(command);
  const runIndex = tokens.indexOf("-run");
  const runTarget = runIndex >= 0 ? tokens[runIndex + 1] : undefined;
  if (runTarget) return runTarget;
  const testIndex = tokens.indexOf("test");
  const testTarget = testIndex >= 0 ? tokens[testIndex + 1] : undefined;
  if (testTarget) return testTarget;
  return "go test";
}

function httpTargetFromCommand(command: string) {
  return shellWords(command).find((token) => /^https?:\/\//.test(token) || token.startsWith("/api/")) ?? "";
}

function semanticFileToolAction(tool: string | undefined) {
  const normalized = (tool ?? "").toLowerCase();
  if (normalized.includes("edit") || normalized.includes("write") || normalized.includes("patch")) {
    return { category: "修改", sourceLabel: "文件修改", titlePrefix: "修改文件" };
  }
  return { category: "查看", sourceLabel: "读取上下文", titlePrefix: "查看文件" };
}

function readableToolName(tool: string | undefined) {
  const normalized = (tool ?? "").toLowerCase();
  if (!normalized) return "工具调用";
  if (normalized === "exec_command") return "命令执行";
  if (normalized === "patch_apply") return "代码修改";
  return tool ?? "工具调用";
}

export function summarizeToolOutput(output: string) {
  const firstLine = output.split("\n").find((line) => line.trim().length > 0) ?? "";
  return truncateText(firstLine, 220);
}

function shortPath(path: string) {
  const normalized = path.replace(/^["']|["']$/g, "");
  const parts = normalized.split("/").filter(Boolean);
  if (parts.length <= 2) return normalized;
  return `.../${parts.slice(-2).join("/")}`;
}

function runReviewTimelineNodeEvent(node: IssueTimelineNode): RunReviewEventRowData {
  const tokenTotal = node.input_tokens + node.output_tokens + node.cache_read_tokens + node.cache_write_tokens;
  return {
    id: `node:${node.node_id}`,
    kind: "node",
    category: timelineNodeKindLabel(node.node_type),
    timestampMs: parseTimeMs(node.completed_at) ?? parseTimeMs(node.started_at) ?? 0,
    timeLabel: formatEventTime(node.completed_at || node.started_at),
    taskId: node.node_type === "agent_task" ? node.node_id.replace(/^task:/, "") : node.root_task_id,
    sourceLabel: node.agent_name || node.node_type,
    object: node.node_type,
    title: firstNonEmpty(cleanSemanticMarkdownLine(node.summary), timelineNodeKindLabel(node.node_type)),
    outcome: statusLabel(node.status),
    summary: node.usage_unavailable_trace ? "模型用量未返回" : "",
    detail: node.evidence_refs.length > 0 ? `evidence_refs:\n${formatJSON(node.evidence_refs)}` : "",
    metadataDetail: "",
    durationMs: node.duration_ms,
    tokenTotal,
    severity: node.status === "failed" || node.status === "blocked" ? "error" : "normal",
    rawSourceLabel: "timeline_node",
    rawPayload: node,
  };
}

function taskMessageKindLabel(type: TaskMessagePayload["type"]): string {
  switch (type) {
    case "tool_use":
      return "工具调用";
    case "tool_result":
      return "工具结果";
    case "thinking":
      return "思考";
    case "error":
      return "错误";
    case "text":
    default:
      return "文本";
  }
}

export function taskMessageText(message: TaskMessagePayload): string {
  return message.content || message.output || "";
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
    case "llm.usage_unavailable":
      return "模型用量未返回";
    default:
      return eventType || "未分类事件";
  }
}

function timelineNodeKindLabel(type: IssueTimelineNode["node_type"]): string {
  switch (type) {
    case "agent_task":
      return "任务";
    case "squad_step":
      return "SOP";
    case "child_issue_ref":
      return "子任务";
    case "source_fetch":
      return "来源";
    case "approval":
      return "唤醒";
    case "human_confirmation":
      return "人工确认";
    case "dispatch_wait":
      return "调度等待";
    case "tool_call":
      return "工具";
    case "status_change":
      return "状态";
    case "evidence":
    default:
      return "证据";
  }
}

function formatEventTime(value: string | undefined) {
  if (!value) return "";
  const ms = parseTimeMs(value);
  if (ms === null) return "";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(ms));
}

function hasFailureSignal(value: string) {
  const line = extractErrorLine(value);
  return Boolean(line && !isBenignFailureCounterLine(line));
}

function isBenignFailureCounterLine(value: string) {
  const lower = value.toLowerCase();
  return (
    /\b0\s+(?:chart\(s\)\s+)?(?:failed|failure|failures|errors)\b/.test(lower) ||
    /\b(?:pass|passed|success|successful|通过|成功)\b.*\b0\s+(?:failed|failure|failures|errors)\b/.test(lower) ||
    /\b0\s+(?:failed|failure|failures|errors)\b.*\b(?:pass|passed|success|successful|通过|成功)\b/.test(lower)
  );
}
