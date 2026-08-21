import { describe, expect, it } from "vitest";
import type { AgentTask, AgentTaskArtifact, Issue, IssueExecutionTreeResponse, IssueTimelineNode, TaskTraceEvent } from "@multica/core/types";
import type { TaskMessagePayload } from "@multica/core/types/events";
import type { PromptEvaluationToolCallChain } from "@multica/core/types/prompt-evaluation";
import {
  artifactDownloadHref,
  artifactXlsxHyperlinkHref,
  buildAgentNodeRows,
  buildIssueReviewDraftCaseRequest,
  buildRunReviewDurationSummary,
  buildRunReviewDurationTooltipRows,
  buildRunReviewLiveSummary,
  buildRunReviewLiveTimelineNodes,
  buildRunReviewNodeXlsxSheets,
  buildRunReviewEventGroups,
  buildRunReviewEventRows,
  buildRunReviewOptimizerHref,
  buildRunReviewRawEventsXlsxSheets,
  buildEventTaskLabelById,
  buildTimelineBarRows,
  buildTimelineAgentRows,
  cacheReuseRate,
  filterVisibleRunReviewEventRows,
  issueRunRowActivityLabel,
  issueRunRowMetaLabels,
  runReviewMessageRefreshDelayMs,
  runReviewTotalDurationMs,
  shouldShowTimelineSegmentText,
  shouldRefreshRunReviewForTaskEvent,
  timelineTiming,
  timelineSegmentStyle,
  timelineSegmentTooltipRows,
  timelineSegmentWidthPercent,
} from "./run-reviews-page";
import { sopStageDisplayName } from "../../common/sop-stage-labels";

function trace(overrides: Partial<TaskTraceEvent> = {}): TaskTraceEvent {
  return {
    id: "trace-1",
    workspace_id: "workspace-1",
    task_id: "task-1",
    issue_id: "issue-1",
    agent_id: "agent-1",
    runtime_id: "runtime-1",
    squad_id: null,
    project_id: null,
    source: "task_service",
    event_type: "task.failed",
    event_name: "任务已失败",
    status: "failed",
    attempt: 1,
    duration_ms: 1200,
    queue_wait_ms: null,
    run_ms: null,
    total_ms: null,
    provider: "codex",
    model: "gpt-test",
    input_tokens: 10,
    output_tokens: 20,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    failure_reason: "provider timeout",
    error_type: "provider_network",
    trigger_comment_id: null,
    autopilot_run_id: null,
    chat_session_id: null,
    metadata: { phase: "verify", stderr: "timeout" },
    created_at: "2026-06-09T10:03:00.000Z",
    ...overrides,
  };
}

function message(overrides: Partial<TaskMessagePayload> = {}): TaskMessagePayload {
  return {
    task_id: "task-1",
    issue_id: "issue-1",
    seq: 2,
    type: "tool_result",
    tool: "curl-check",
    output: "Error: HTTP 500 from upstream",
    created_at: "2026-06-09T10:02:00.000Z",
    ...overrides,
  };
}

function tool(overrides: Partial<PromptEvaluationToolCallChain> = {}): PromptEvaluationToolCallChain {
  return {
    id: "tool-1",
    task_id: "task-1",
    tool: "curl-check",
    status: "已配对",
    use_seq: 1,
    result_seq: 2,
    input: { url: "/health" },
    output: "Error: HTTP 500 from upstream",
    duration_ms: 900,
    result_category: "异常线索",
    failure_signal: true,
    failure_reason: "HTTP 500 from upstream",
    summary: "工具 curl-check 已配对：调用 #1，结果 #2",
    created_at: "2026-06-09T10:01:00.000Z",
    completed_at: "2026-06-09T10:02:30.000Z",
    ...overrides,
  };
}

function timelineNode(overrides: Partial<IssueTimelineNode> = {}): IssueTimelineNode {
  return {
    issue_id: "issue-1",
    node_id: "task:task-1",
    node_type: "agent_task",
    agent_id: "agent-1",
    agent_name: "01-需求澄清",
    status: "completed",
    started_at: "2026-06-09T10:00:00.000Z",
    completed_at: "2026-06-09T10:01:00.000Z",
    duration_ms: 60_000,
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    message_count: 0,
    agent_turn_count: 0,
    trace_event_count: 0,
    usage_unavailable_trace: false,
    summary: "节点摘要",
    evidence_refs: [],
    ...overrides,
  };
}

function task(overrides: Partial<AgentTask> = {}): AgentTask {
  return {
    id: "task-1",
    agent_id: "agent-1",
    runtime_id: "runtime-1",
    issue_id: "issue-1",
    status: "completed",
    priority: 0,
    dispatched_at: "2026-06-09T10:00:00.000Z",
    started_at: "2026-06-09T10:00:00.000Z",
    completed_at: "2026-06-09T10:02:00.000Z",
    result: "handoff: 需求边界明确，继续方案设计",
    error: null,
    created_at: "2026-06-09T09:59:00.000Z",
    trigger_summary: "用户要求优化运行复盘事件流",
    ...overrides,
  };
}

function artifact(overrides: Partial<AgentTaskArtifact> = {}): AgentTaskArtifact {
  return {
    id: "att-1",
    task_id: "task-1",
    comment_id: "comment-1",
    issue_id: "issue-1",
    filename: "01-需求澄清.md",
    title: "01-需求澄清",
    kind: "stage_markdown",
    content_type: "text/markdown",
    size_bytes: 128,
    download_url: "/api/attachments/att-1/download",
    markdown_url: "/api/attachments/att-1/download",
    created_at: "2026-06-09T10:01:00.000Z",
    ...overrides,
  };
}

describe("buildRunReviewOptimizerHref", () => {
  it("keeps issue context on the visible test suites route", () => {
    expect(buildRunReviewOptimizerHref((view) => `/acme/evaluation/${view}`, "issue with space")).toBe(
      "/acme/evaluation/runs?issue=issue%20with%20space",
    );
  });
});

describe("run review duration summary", () => {
  it("uses agent execution plus recorded waiting time instead of wall clock duration", () => {
    const summary = {
      issue_id: "issue-1",
      node_count: 1,
      total_duration_ms: 120000,
      wall_clock_duration_ms: 300000,
      agent_execution_duration_ms: 120000,
      human_confirmation_duration_ms: 180000,
      child_issue_wait_duration_ms: 60000,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_read_tokens: 0,
      total_cache_write_tokens: 0,
      message_count: 0,
      agent_turn_count: 0,
      trace_event_count: 0,
      usage_unavailable: false,
      acceptance_status: "done",
      full_analysis_deep_link: "",
    };

    expect(runReviewTotalDurationMs(summary)).toBe(360000);
    expect(buildRunReviewDurationSummary(summary)).toBe("Agent 执行 2m · 人工确认 3m · 子任务等待 1m");
    expect(buildRunReviewDurationTooltipRows(summary)).toEqual([
      ["Agent 执行耗时", "2m"],
      ["人工确认耗时", "3m"],
      ["子任务等待耗时", "1m"],
    ]);
  });

  it("ignores wall clock duration even when unclassified idle gaps exist", () => {
    const summary = {
      issue_id: "issue-1",
      node_count: 1,
      total_duration_ms: 120000,
      wall_clock_duration_ms: 600000,
      agent_execution_duration_ms: 120000,
      human_confirmation_duration_ms: 60000,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_read_tokens: 0,
      total_cache_write_tokens: 0,
      message_count: 0,
      agent_turn_count: 0,
      trace_event_count: 0,
      usage_unavailable: false,
      acceptance_status: "done",
      full_analysis_deep_link: "",
    };

    expect(runReviewTotalDurationMs(summary)).toBe(180000);
  });

  it("does not show artificial waiting time when timing data is missing", () => {
    expect(buildRunReviewDurationTooltipRows(undefined)).toEqual([
      ["Agent 执行耗时", "0m"],
      ["人工确认耗时", "未记录"],
      ["子任务等待耗时", "未记录"],
    ]);
  });
});

describe("run review cache metrics", () => {
  it("uses cache reuse rate for the displayed hit rate", () => {
    expect(cacheReuseRate(7_630_464, 436_950)).toBeCloseTo(0.945837, 5);
  });

  it("returns null when no cache tokens were reported", () => {
    expect(cacheReuseRate(0, 0)).toBeNull();
  });
});

describe("run review realtime helpers", () => {
  it("only refreshes the selected issue for task events", () => {
    expect(shouldRefreshRunReviewForTaskEvent("issue-1", { issue_id: "issue-1" })).toBe(true);
    expect(shouldRefreshRunReviewForTaskEvent("issue-1", { issue_id: "issue-2" })).toBe(false);
    expect(shouldRefreshRunReviewForTaskEvent("issue-1", null)).toBe(false);
  });

  it("debounces message refreshes while bounding long streaming waits", () => {
    expect(runReviewMessageRefreshDelayMs(10_000, 0, 1_200, 4_000)).toBe(1_200);
    expect(runReviewMessageRefreshDelayMs(10_000, 9_000, 1_200, 4_000)).toBe(1_200);
    expect(runReviewMessageRefreshDelayMs(10_000, 6_500, 1_200, 4_000)).toBe(500);
    expect(runReviewMessageRefreshDelayMs(10_000, 6_000, 1_200, 4_000)).toBe(0);
  });

  it("derives live running durations without changing completed nodes", () => {
    const nowMs = Date.parse("2026-06-09T10:03:00.000Z");
    const runningTask = task({
      status: "running",
      started_at: "2026-06-09T10:00:00.000Z",
      completed_at: null,
    });
    const runningNode = {
      issue_id: "issue-1",
      node_id: "task:task-1",
      node_type: "agent_task",
      status: "running",
      started_at: "2026-06-09T10:01:00.000Z",
      completed_at: "",
      duration_ms: 15_000,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      message_count: 0,
      agent_turn_count: 0,
      trace_event_count: 0,
      usage_unavailable_trace: false,
      summary: "running",
      evidence_refs: [],
    } as IssueTimelineNode;
    const completedNode = {
      ...runningNode,
      node_id: "task:task-2",
      status: "completed",
      completed_at: "2026-06-09T10:02:00.000Z",
      duration_ms: 60_000,
    } as IssueTimelineNode;
    const summary = {
      issue_id: "issue-1",
      node_count: 2,
      total_duration_ms: 60_000,
      agent_execution_duration_ms: 60_000,
      wall_clock_duration_ms: 60_000,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_read_tokens: 0,
      total_cache_write_tokens: 0,
      message_count: 0,
      agent_turn_count: 0,
      trace_event_count: 0,
      usage_unavailable: false,
      acceptance_status: "running",
      full_analysis_deep_link: "",
    };

    expect(buildRunReviewLiveSummary(summary, [runningTask], [runningNode, completedNode], nowMs)).toMatchObject({
      total_duration_ms: 180_000,
      agent_execution_duration_ms: 180_000,
    });
    expect(buildRunReviewLiveTimelineNodes([runningNode, completedNode], nowMs).map((node) => node.duration_ms)).toEqual([
      120_000,
      60_000,
    ]);
  });

  it("counts active human confirmation separately from active agent execution", () => {
    const nowMs = Date.parse("2026-06-09T10:05:00.000Z");
    const pendingHumanNode = timelineNode({
      node_id: "human_confirmation:pending:task-0",
      node_type: "human_confirmation",
      status: "running",
      started_at: "2026-06-09T10:01:00.000Z",
      completed_at: "",
      duration_ms: 0,
      summary: "等待用户确认密码策略边界",
    });
    const summary = {
      issue_id: "issue-1",
      node_count: 2,
      total_duration_ms: 60_000,
      agent_execution_duration_ms: 60_000,
      human_confirmation_duration_ms: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_read_tokens: 0,
      total_cache_write_tokens: 0,
      message_count: 0,
      agent_turn_count: 0,
      trace_event_count: 0,
      usage_unavailable: false,
      acceptance_status: "running",
      full_analysis_deep_link: "",
    };

    expect(buildRunReviewLiveSummary(summary, [], [pendingHumanNode], nowMs)).toMatchObject({
      total_duration_ms: 300_000,
      agent_execution_duration_ms: 60_000,
      human_confirmation_duration_ms: 240_000,
    });
    expect(buildRunReviewLiveTimelineNodes([pendingHumanNode], nowMs).map((node) => node.duration_ms)).toEqual([
      240_000,
    ]);
  });
});

describe("issue run review list row labels", () => {
  it("keeps project and status while hiding empty child progress", () => {
    const issue = {
      project: { id: "project-1", title: "goal-test", icon: null },
      status: "todo",
      child_progress: { done: 0, total: 0 },
    } as Issue;

    expect(issueRunRowMetaLabels(issue)).toEqual(["goal-test", "状态 待办"]);
  });

  it("shows child progress only when an issue has children", () => {
    const issue = {
      project: null,
      status: "in_progress",
      child_progress: { done: 1, total: 3 },
    } as Issue;

    expect(issueRunRowMetaLabels(issue)).toEqual(["未绑定项目", "状态 进行中", "子任务 1/3"]);
  });

  it("only shows special activity labels and omits the default review label", () => {
    const activity = (runningCount: number, queuedCount: number): Pick<Issue, "agent_activity"> => ({
      agent_activity: { running_count: runningCount, queued_count: queuedCount, agent_ids: [] },
    });

    expect(issueRunRowActivityLabel(activity(0, 0))).toBeNull();
    expect(issueRunRowActivityLabel(activity(2, 1))).toEqual({
      label: "运行 2",
      tone: "running",
    });
    expect(issueRunRowActivityLabel(activity(0, 3))).toEqual({
      label: "排队 3",
      tone: "queued",
    });
  });
});

describe("buildRunReviewEventRows", () => {
  it("uses consistent Chinese SOP stage display names", () => {
    expect(sopStageDisplayName("01-clarify")).toBe("01-需求澄清");
    expect(sopStageDisplayName("02-design")).toBe("02-方案设计");
    expect(sopStageDisplayName("03-task-split")).toBe("03-任务拆分");
    expect(sopStageDisplayName("04-implement")).toBe("04-开发");
    expect(sopStageDisplayName("05-verify")).toBe("05-验证测试");
    expect(sopStageDisplayName("custom-agent")).toBe("custom-agent");
  });

  it("keeps canonical diagnostic events while hiding duplicated node summaries", () => {
    const tree = {
      root: {
        tasks: [],
        task_messages: [message()],
        trace_events: [trace()],
        tool_call_chains: [tool()],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse;
    const timelineNodes = [
      {
        node_id: "task:task-1",
        node_type: "agent_task",
        status: "failed",
        started_at: "2026-06-09T10:00:00.000Z",
        completed_at: "2026-06-09T10:00:30.000Z",
        duration_ms: 4000,
        input_tokens: 0,
        output_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        message_count: 1,
        agent_turn_count: 1,
        trace_event_count: 1,
        usage_unavailable_trace: false,
        summary: "Agent task failed",
        evidence_refs: [{ type: "agent_task", id: "task-1" }],
      },
    ] as IssueTimelineNode[];

    const rows = buildRunReviewEventRows(tree, timelineNodes);

    expect(rows.map((row) => row.kind)).toEqual(["trace", "tool"]);
    expect(rows.find((row) => row.kind === "trace")?.metadataDetail).toContain("stderr");
    expect(rows.find((row) => row.kind === "tool")?.detail).toContain("\"url\": \"/health\"");
    expect(rows.find((row) => row.kind === "tool")?.detail).toContain("raw_tool: curl-check");
    expect(rows.find((row) => row.kind === "tool")?.linkedRawPayloads).toEqual([
      expect.objectContaining({
        label: "关联 task_message #2 工具结果",
        payload: expect.objectContaining({ seq: 2, type: "tool_result" }),
      }),
    ]);
    expect(rows.find((row) => row.kind === "trace")?.tokenTotal).toBe(30);
  });

  it("deduplicates source fetch timeline nodes when a source trace already exists", () => {
    const tree = {
      root: {
        tasks: [],
        task_messages: [],
        trace_events: [
          trace({
            id: "source-trace",
            event_type: "source.fetch",
            event_name: "来源已拉取",
            status: "completed",
            metadata: { source_url: "https://www.tapd.cn/wiki/1" },
          }),
        ],
        tool_call_chains: [],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse;
    const timelineNodes = [
      {
        node_id: "source:tapd",
        node_type: "source_fetch",
        status: "completed",
        started_at: "2026-06-09T10:00:00.000Z",
        completed_at: "2026-06-09T10:00:01.000Z",
        duration_ms: 1000,
        input_tokens: 0,
        output_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        message_count: 0,
        agent_turn_count: 0,
        trace_event_count: 1,
        usage_unavailable_trace: false,
        summary: "来源已拉取",
        evidence_refs: [{ type: "source", id: "tapd" }],
      },
    ] as IssueTimelineNode[];

    const rows = buildRunReviewEventRows(tree, timelineNodes);

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      kind: "trace",
      rawSourceLabel: "task_trace_event",
      object: "source.fetch",
    });
  });

  it("keeps all canonical rows available for raw export instead of truncating at 50", () => {
    const taskMessages = Array.from({ length: 55 }, (_, index) => message({
      seq: index + 1,
      type: "text",
      content: `message ${index + 1}`,
      created_at: `2026-06-09T10:${String(index).padStart(2, "0")}:00.000Z`,
    }));
    const tree = {
      root: {
        tasks: [],
        task_messages: taskMessages,
        trace_events: [],
        tool_call_chains: [],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse;

    const rows = buildRunReviewEventRows(tree, []);
    const [sheet] = buildRunReviewRawEventsXlsxSheets(rows);

    expect(rows).toHaveLength(55);
    expect(sheet?.rows).toHaveLength(56);
    expect(sheet?.rows.some((row) => row.includes("message:task-1:55"))).toBe(true);
    expect(sheet?.rows.some((row) => row.includes("message 55"))).toBe(true);
  });

  it("filters human-readable event rows without dropping raw export evidence", () => {
    const tree = {
      root: {
        tasks: [],
        task_messages: [
          message({ seq: 1, type: "text", content: "已完成需求澄清", output: "" }),
        ],
        trace_events: [
          trace({ id: "queued", event_type: "task.queued", event_name: "任务入队", status: "queued", failure_reason: "", error_type: "" }),
          trace({ id: "started", event_type: "task.started", event_name: "任务已开始", status: "running", failure_reason: "", error_type: "" }),
          trace({ id: "completed", event_type: "task.completed", event_name: "任务完成", status: "completed", failure_reason: "", error_type: "" }),
          trace({ id: "usage", event_type: "llm.usage_reported", event_name: "模型用量已上报", status: "completed", failure_reason: "", error_type: "" }),
          trace(),
        ],
        tool_call_chains: [
          tool({ id: "read-file", failure_signal: false, failure_reason: "", output: "ok" }),
        ],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse;
    const rows = buildRunReviewEventRows(tree, [
      timelineNode({
        node_id: "squad_step:sop-1",
        node_type: "squad_step",
        summary: "05-验证测试",
        status: "completed",
      }),
      timelineNode({
        node_id: "human_confirmation:comment-1:task-2",
        node_type: "human_confirmation",
        summary: "等待人工确认",
        status: "completed",
      }),
      timelineNode({
        node_id: "child_issue_ref:gateway",
        node_type: "child_issue_ref",
        summary: "gateway 子任务",
        status: "completed",
      }),
      timelineNode({
        node_id: "source_fetch:tapd",
        node_type: "source_fetch",
        summary: "来源已拉取",
        status: "completed",
      }),
      timelineNode({
        node_id: "approval:wakeup",
        node_type: "approval",
        summary: "唤醒 PM",
        status: "completed",
      }),
    ]);

    const visibleRows = filterVisibleRunReviewEventRows(rows);
    const visibleObjects = visibleRows.map((row) => row.object);
    const [rawSheet] = buildRunReviewRawEventsXlsxSheets(rows);
    const rawObjects = rawSheet?.rows.slice(1).map((row) => row[7]) ?? [];

    expect(visibleObjects).toEqual(expect.arrayContaining([
      "消息 #1",
      "task.failed",
      "human_confirmation",
      "child_issue_ref",
      "source_fetch",
    ]));
    expect(visibleRows.some((row) => row.kind === "tool")).toBe(true);
    expect(visibleObjects).not.toEqual(expect.arrayContaining([
      "task.queued",
      "task.started",
      "task.completed",
      "llm.usage_reported",
      "squad_step",
      "approval",
    ]));
    expect(rawObjects).toEqual(expect.arrayContaining([
      "task.queued",
      "task.started",
      "task.completed",
      "llm.usage_reported",
      "squad_step",
      "approval",
    ]));
  });

  it("groups task events once and keeps group events in timeline order", () => {
    const rows = [
      {
        id: "trace:2",
        kind: "trace",
        category: "Trace",
        timestampMs: 200,
        timeLabel: "06/09 10:02",
        taskId: "task-1",
        sourceLabel: "task_service",
        object: "task.completed",
        title: "任务完成",
        outcome: "已完成",
        summary: "",
        detail: "",
        metadataDetail: "",
        durationMs: 0,
        tokenTotal: 20,
        severity: "normal",
        rawSourceLabel: "task_trace_event",
        rawPayload: { id: "trace-2" },
      },
      {
        id: "message:1",
        kind: "message",
        category: "文本",
        timestampMs: 100,
        timeLabel: "06/09 10:01",
        taskId: "task-1",
        sourceLabel: "模型输出",
        object: "消息 #1",
        title: "文本",
        outcome: "已记录",
        summary: "hello",
        detail: "hello",
        metadataDetail: "",
        durationMs: 0,
        tokenTotal: 0,
        severity: "normal",
        rawSourceLabel: "task_message",
        rawPayload: { seq: 1 },
      },
    ] satisfies ReturnType<typeof buildRunReviewEventRows>;

    const groups = buildRunReviewEventGroups(rows, new Map([["task-1", "01-需求澄清"]]));

    expect(groups).toHaveLength(1);
    expect(groups[0]?.label).toBe("01-需求澄清");
    expect(groups[0]?.taskId).toBe("task-1");
    expect(groups[0]?.tokenTotal).toBe(20);
    expect(groups[0]?.events.map((event) => event.id)).toEqual(["message:1", "trace:2"]);
  });

  it("labels repeated PM task groups by task intent instead of only agent name", () => {
    const labels = buildEventTaskLabelById([
      timelineNode({
        node_id: "task:pm-summary",
        agent_name: "pm-v2 · PM-项目经理",
        summary: "需求摘要生成",
      }),
      timelineNode({
        node_id: "task:pm-wait",
        agent_name: "pm-v2 · PM-项目经理",
        summary: "等待用户确认密码策略边界",
      }),
    ]);

    expect(labels.get("pm-summary")).toBe("需求摘要生成");
    expect(labels.get("pm-wait")).toBe("等待用户确认密码策略边界");
  });

  it("ignores markdown dividers when labeling PM task groups", () => {
    const labels = buildEventTaskLabelById([
      timelineNode({
        node_id: "task:pm-wait",
        agent_name: "pm-v2 · PM-项目经理",
        summary: "---",
      }),
      timelineNode({
        node_id: "task:pm-heading",
        agent_name: "pm-v2 · PM-项目经理",
        summary: "---\n\n## 01-需求澄清已完成，需等待用户确认",
      }),
    ]);

    expect(labels.get("pm-wait")).toBe("pm-v2 · PM-项目经理");
    expect(labels.get("pm-heading")).toBe("01-需求澄清已完成，需等待用户确认");
  });

  it("keeps duplicate user input snapshots out of the default visible event stream", () => {
    const rows = buildRunReviewEventRows({
      root: {
        tasks: [],
        task_messages: [],
        tool_call_chains: [],
        trace_events: [
          trace({
            id: "input-1",
            task_id: "pm-1",
            event_type: "user_input.received",
            event_name: "用户输入已接收",
            status: "completed",
            failure_reason: "",
            error_type: "",
            metadata: {
              input_kind: "issue",
              summary: "增强密码强度",
              content_snapshot: "IDA-81 增强密码强度\n目标项目 usercenter",
            },
          }),
          trace({
            id: "input-2",
            task_id: "pm-2",
            event_type: "user_input.received",
            event_name: "用户输入已接收",
            status: "completed",
            failure_reason: "",
            error_type: "",
            metadata: {
              input_kind: "issue",
              summary: "增强密码强度",
              content_snapshot: "IDA-81 增强密码强度\n目标项目 usercenter",
            },
          }),
        ],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse, []);

    expect(rows.filter((row) => row.object === "issue")).toHaveLength(2);
    expect(filterVisibleRunReviewEventRows(rows).filter((row) => row.object === "issue")).toHaveLength(1);
  });

  it("keeps system events in separate raw groups when they do not belong to a task", () => {
    const rows = [
      {
        id: "node:source",
        kind: "node",
        category: "来源",
        timestampMs: 100,
        timeLabel: "06/09 10:01",
        sourceLabel: "source_fetch",
        object: "source_fetch",
        title: "来源已拉取",
        outcome: "已完成",
        summary: "",
        detail: "",
        metadataDetail: "",
        durationMs: 0,
        tokenTotal: 0,
        severity: "normal",
        rawSourceLabel: "timeline_node",
        rawPayload: { node_id: "source" },
      },
      {
        id: "node:approval",
        kind: "node",
        category: "唤醒",
        timestampMs: 200,
        timeLabel: "06/09 10:02",
        sourceLabel: "approval",
        object: "approval",
        title: "人工确认",
        outcome: "已完成",
        summary: "",
        detail: "",
        metadataDetail: "",
        durationMs: 0,
        tokenTotal: 0,
        severity: "normal",
        rawSourceLabel: "timeline_node",
        rawPayload: { node_id: "approval" },
      },
    ] satisfies ReturnType<typeof buildRunReviewEventRows>;

    const groups = buildRunReviewEventGroups(rows);

    expect(groups.map((group) => group.label)).toEqual(["source_fetch", "approval"]);
    expect(groups.every((group) => group.taskId === undefined)).toBe(true);
  });

  it("turns raw exec_command calls into semantic review actions", () => {
    const tree = {
      root: {
        tasks: [],
        task_messages: [],
        trace_events: [],
        tool_call_chains: [
          tool({
            id: "search",
            tool: "exec_command",
            input: { command: "rg -n \"task_trace_event\" server packages" },
            output: "server/pkg/db/queries/task_trace_event.sql:1:-- name",
            created_at: "2026-06-09T10:01:00.000Z",
            completed_at: "2026-06-09T10:01:02.000Z",
            failure_signal: false,
            failure_reason: "",
          }),
          tool({
            id: "read",
            tool: "exec_command",
            input: { command: "sed -n '1,220p' packages/views/run-reviews/components/run-reviews-page.tsx" },
            output: "\"use client\";",
            created_at: "2026-06-09T10:02:00.000Z",
            completed_at: "2026-06-09T10:02:02.000Z",
            failure_signal: false,
            failure_reason: "",
          }),
          tool({
            id: "typecheck",
            tool: "exec_command",
            input: { command: "pnpm --filter @multica/views typecheck" },
            output: "tsc --noEmit",
            created_at: "2026-06-09T10:03:00.000Z",
            completed_at: "2026-06-09T10:03:02.000Z",
            failure_signal: false,
            failure_reason: "",
          }),
          tool({
            id: "go-test",
            tool: "exec_command",
            input: { command: "cd server && GOWORK=off go test ./internal/handler/ -run TestGetIssueExecutionTreeAggregatesHierarchySOPTraceAndWakeups" },
            output: "ok",
            created_at: "2026-06-09T10:04:00.000Z",
            completed_at: "2026-06-09T10:04:02.000Z",
            failure_signal: false,
            failure_reason: "",
          }),
        ],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse;

    const rows = buildRunReviewEventRows(tree, []);
    const titles = rows.map((row) => row.title);

    expect(titles).toEqual([
      "运行后端单测：TestGetIssueExecutionTreeAggregatesHierarchySOPTraceAndWakeups",
      "运行类型检查：@multica/views",
      "查看文件：.../components/run-reviews-page.tsx",
      "搜索代码：task_trace_event",
    ]);
    expect(rows.every((row) => !row.title.includes("exec_command") && !row.sourceLabel.includes("exec_command"))).toBe(true);
    expect(rows[0]?.detail).toContain("raw_tool: exec_command");
  });

  it("keeps long raw output out of the main list and extracts a useful failure summary", () => {
    const longOutput = [
      "running tests",
      "irrelevant setup line",
      "Error: HTTP 500 from upstream service",
      "stack frame 1",
      "stack frame 2",
      "stack frame 3",
    ].join("\n");
    const tree = {
      root: {
        tasks: [],
        task_messages: [],
        trace_events: [],
        tool_call_chains: [
          tool({
            id: "curl",
            tool: "exec_command",
            input: { command: "curl http://localhost:8080/api/health" },
            output: longOutput,
            failure_signal: false,
            failure_reason: "",
          }),
        ],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse;

    const [row] = buildRunReviewEventRows(tree, []);

    expect(row?.title).toBe("检查接口：http://localhost:8080/api/health");
    expect(row?.outcome).toBe("异常线索");
    expect(row?.severity).toBe("error");
    expect(row?.summary).toBe("异常摘要：Error: HTTP 500 from upstream service");
    expect(row?.summary).not.toContain("stack frame");
    expect(row?.detail).toContain("stack frame 3");
  });

  it("does not mark successful command output as failed just because it contains failed counters", () => {
    const tree = {
      root: {
        tasks: [],
        task_messages: [],
        trace_events: [],
        tool_call_chains: [
          tool({
            id: "helm-lint",
            tool: "exec_command",
            input: { command: "helm lint helm/public 2>&1 | tail -10" },
            output: "Stdout: ==> Linting helm/public\n1 chart(s) linted, 0 chart(s) failed\n\nStderr: (empty)\nExit Code: 0\nSignal: (none)",
            failure_signal: false,
            failure_reason: "",
          }),
        ],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse;

    const [row] = buildRunReviewEventRows(tree, []);

    expect(row?.severity).toBe("normal");
    expect(row?.summary).not.toContain("异常摘要");
  });

  it("does not mark JSON-wrapped successful tool outputs as failed", () => {
    const wrappedOutput = JSON.stringify([{
      type: "text",
      text: "Command: helm lint helm/public 2>&1\nStdout: ==> Linting helm/public\n1 chart(s) linted, 0 chart(s) failed\n\nStderr: (empty)\nExit Code: 0\nSignal: (none)",
    }]);
    const tree = {
      root: {
        tasks: [],
        task_messages: [],
        trace_events: [],
        tool_call_chains: [
          tool({
            id: "helm-lint-json",
            tool: "Bash",
            input: { command: "helm lint helm/public 2>&1" },
            output: wrappedOutput,
            failure_signal: false,
            failure_reason: "",
          }),
        ],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse;

    const [row] = buildRunReviewEventRows(tree, []);

    expect(row?.severity).toBe("normal");
    expect(row?.summary).not.toContain("异常摘要");
    expect(row?.summary).not.toContain("[{\"type\"");
  });

  it("downgrades backend keyword failure signals when successful output proves they are benign", () => {
    const tree = {
      root: {
        tasks: [],
        task_messages: [],
        trace_events: [],
        tool_call_chains: [
          tool({
            id: "git-diff",
            tool: "Bash",
            input: { command: "cd /repo && git diff check_rendered_rules.sh 2>&1" },
            output: "Command: cd /repo && git diff check_rendered_rules.sh 2>&1\nStdout: +fail() {\n+  echo \"FAIL: missing render output\"\n+}\n\nStderr: (empty)\nExit Code: 0\nSignal: (none)",
            failure_signal: true,
            failure_reason: "工具结果包含失败信息",
          }),
          tool({
            id: "git-branch",
            tool: "Bash",
            input: { command: "cd /repo && git branch -a 2>&1" },
            output: "Command: cd /repo && git branch -a 2>&1\nStdout: remotes/origin/v2.1.0_qc_timeout\nv2.1.0_qc_timeout\n\nStderr: (empty)",
            failure_signal: true,
            failure_reason: "工具结果包含超时信息",
          }),
          tool({
            id: "comment",
            tool: "Bash",
            input: { command: "multica issue comment add AIS-145 --content-file ./reply.md 2>&1" },
            output: "Command: multica issue comment add AIS-145 --content-file ./reply.md 2>&1\nStdout: | helm/public | PASS (0 failed) |\nComment added to issue AIS-145.\nExit Code: 0",
            failure_signal: false,
            failure_reason: "",
          }),
          tool({
            id: "read-source",
            tool: "Read",
            input: { file_path: "internal/helper/password_validator.go" },
            output: JSON.stringify([{ type: "text", text: "var ErrPasswordWeak = errors.New(\"密码强度校验失败\")" }]),
            failure_signal: true,
            failure_reason: "工具结果包含错误信息",
          }),
          tool({
            id: "grep-source",
            tool: "Grep",
            input: { pattern: "ErrPassword" },
            output: JSON.stringify([{ type: "text", text: "resp_code_test.go:42:ErrPasswordExpired" }]),
            failure_signal: true,
            failure_reason: "工具结果包含失败信息",
          }),
          tool({
            id: "local-artifact",
            tool: "Bash",
            input: { command: "curl -s http://localhost:18760/uploads/workspaces/ws/artifact.md 2>&1 | head -80" },
            output: "Command: curl -s http://localhost:18760/uploads/workspaces/ws/artifact.md 2>&1 | head -80\nStdout: 失败场景：密码过短时应返回业务错误。\n\nStderr: (empty)",
            failure_signal: true,
            failure_reason: "工具结果包含失败信息",
          }),
          tool({
            id: "task-create-success",
            tool: "TaskCreate",
            input: { subject: "读取 harness/testing.md 和 failures.md" },
            output: JSON.stringify([{ type: "text", text: "Task #7 created successfully: 读取 harness/testing.md 和 failures.md" }]),
            failure_signal: true,
            failure_reason: "工具结果包含失败信息",
          }),
          tool({
            id: "issue-get-success",
            tool: "Bash",
            input: { command: "multica issue get IDA-12 --output json 2>&1" },
            output: "Command: multica issue get IDA-12 --output json 2>&1\nStdout: {\"source_summary_error\":\"\",\"title\":\"错误处理需求\"}\n\nStderr: (empty)",
            failure_signal: true,
            failure_reason: "工具结果包含错误信息",
          }),
          tool({
            id: "comment-list-success",
            tool: "Bash",
            input: { command: "multica issue comment list IDA-12 --recent 20 --output json 2>&1" },
            output: "Command: multica issue comment list IDA-12 --recent 20 --output json 2>&1\nStdout: [{\"content\":\"用户希望确认错误处理与失败用例。\"}]\n\nStderr: (empty)",
            failure_signal: true,
            failure_reason: "工具结果包含失败信息",
          }),
          tool({
            id: "metadata-set-success",
            tool: "Bash",
            input: { command: "multica issue metadata set IDA-12 --key pr_number --value 113" },
            output: "Command: multica issue metadata set IDA-12 --key pr_number --value 113\nStdout: KEY VALUE TYPE\npr_number 113 number",
            failure_signal: true,
            failure_reason: "工具结果包含错误信息",
          }),
        ],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse;

    const rows = buildRunReviewEventRows(tree, []);

    expect(rows).toHaveLength(10);
    expect(rows.every((row) => row.severity === "normal")).toBe(true);
    expect(rows.map((row) => row.summary).join("\n")).not.toContain("异常摘要");
  });

  it("keeps real tool use errors visible for content-only tools", () => {
    const tree = {
      root: {
        tasks: [],
        task_messages: [],
        trace_events: [],
        tool_call_chains: [
          tool({
            id: "read-missing",
            tool: "Read",
            input: { file_path: "missing.txt" },
            output: JSON.stringify([{ type: "text", text: "<tool_use_error>File does not exist.</tool_use_error>" }]),
            failure_signal: true,
            failure_reason: "工具调用返回错误",
          }),
        ],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse;

    const [row] = buildRunReviewEventRows(tree, []);

    expect(row?.severity).toBe("error");
    expect(row?.summary).toContain("异常摘要");
    expect(row?.summary).toContain("工具调用返回错误");
  });

  it("does not mark successful text summaries as failed because they mention zero failures", () => {
    const tree = {
      root: {
        tasks: [],
        task_messages: [
          message({
            seq: 49,
            type: "text",
            content: "Helm lint 兼容性验证全部 PASS，3 个 chart 全部通过，0 failed",
            output: "",
          }),
        ],
        trace_events: [],
        tool_call_chains: [],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse;

    const [row] = buildRunReviewEventRows(tree, []);

    expect(row?.severity).toBe("normal");
    expect(row?.summary).not.toContain("异常摘要");
  });

  it("renders unknown tools as useful fallback events while preserving raw evidence", () => {
    const tree = {
      root: {
        tasks: [],
        task_messages: [],
        trace_events: [],
        tool_call_chains: [
          tool({
            id: "unknown",
            tool: "custom_runtime_probe",
            input: { payload: { mode: "deep" } },
            output: "probe completed",
            failure_signal: false,
            failure_reason: "",
          }),
        ],
        children: [],
      },
    } as unknown as IssueExecutionTreeResponse;

    const [row] = buildRunReviewEventRows(tree, []);

    expect(row?.category).toBe("custom_runtime_probe");
    expect(row?.title).toBe("custom_runtime_probe");
    expect(row?.outcome).toBe("已返回");
    expect(row?.detail).toContain("raw_tool: custom_runtime_probe");
  });

  it("builds stable artifact download links from attachment ids", () => {
    expect(artifactDownloadHref(artifact({
      download_url: "/uploads/workspaces/ws-1/stale.md",
      markdown_url: "/uploads/workspaces/ws-1/stale.md",
    }))).toBe("/api/attachments/att-1/download");

    expect(artifactDownloadHref(artifact({
      download_url: "/uploads/workspaces/ws-1/stale.md",
      markdown_url: "/uploads/workspaces/ws-1/stale.md",
    }), "https://api.example.test")).toBe("https://api.example.test/api/attachments/att-1/download");
  });

  it("builds absolute Excel artifact hyperlinks so desktop spreadsheet apps can open them", () => {
    expect(artifactXlsxHyperlinkHref(artifact({
      download_url: "/uploads/workspaces/ws-1/stale.md",
      markdown_url: "/uploads/workspaces/ws-1/stale.md",
    }), "https://api.example.test")).toBe("https://api.example.test/api/attachments/att-1/download");

    expect(artifactXlsxHyperlinkHref(artifact({
      id: "",
      download_url: "/uploads/workspaces/ws-1/01-clarify.md",
      markdown_url: "/uploads/workspaces/ws-1/01-clarify.md",
    }), "https://api.example.test")).toBe("https://api.example.test/uploads/workspaces/ws-1/01-clarify.md");
  });

  it("exports node XLSX sheet with Chinese summary, compact rows, and artifact links", () => {
    const issue = {
      id: "issue-1",
      identifier: "ISS-1",
      title: "优化,运行复盘",
      status: "done",
    } as Issue;
    const summary = {
      issue_id: "issue-1",
      node_count: 1,
      total_duration_ms: 120000,
      total_input_tokens: 10,
      total_output_tokens: 20,
      total_cache_read_tokens: 30,
      total_cache_write_tokens: 4,
      agent_execution_duration_ms: 60000,
      human_confirmation_duration_ms: 30000,
      child_issue_wait_duration_ms: 15000,
      message_count: 2,
      agent_turn_count: 3,
      trace_event_count: 1,
      usage_unavailable: false,
      acceptance_status: "done",
      full_analysis_deep_link: "",
    };
    const node = {
      issue_id: "issue-1",
      node_id: "task:task-1",
      node_type: "agent_task",
      agent_name: "01-clarify",
      status: "completed",
      started_at: "2026-06-09T10:00:00.000Z",
      actual_started_at: "2026-06-09T10:00:00.000Z",
      completed_at: "2026-06-09T10:01:00.000Z",
      duration_ms: 60000,
      input_tokens: 1,
      output_tokens: 2,
      cache_read_tokens: 3,
      cache_write_tokens: 4,
      message_count: 1,
      agent_turn_count: 5,
      trace_event_count: 1,
      usage_unavailable_trace: false,
      summary: "done",
      evidence_refs: [{ type: "attachment", id: "att-1", href: "/api/attachments/att-1/download" }],
      artifacts: [
        artifact({
          download_url: "/uploads/workspaces/ws-1/01-需求澄清.md",
          markdown_url: "/uploads/workspaces/ws-1/01-需求澄清.md",
        }),
        artifact({
          id: "att-2",
          filename: "02-design.md",
          title: "02-design",
          download_url: "/api/attachments/att-2/download",
          markdown_url: "/api/attachments/att-2/download",
        }),
      ],
    } as IssueTimelineNode;
    const nodeWithoutArtifacts = {
      ...node,
      node_id: "task:task-2",
      agent_name: "02-design",
      status: "running",
      duration_ms: 0,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      agent_turn_count: 0,
      artifacts: [],
    } as IssueTimelineNode;

    const [sheet, artifactSheet] = buildRunReviewNodeXlsxSheets(
      issue,
      summary,
      [
        { key: "task-1", label: "01-需求澄清", node },
        { key: "task-2", label: "02-方案设计", node: nodeWithoutArtifacts },
      ] as never,
      [],
    );

    expect(sheet?.name).toBe("节点数据");
    expect(sheet?.rows[0]).toEqual([
      "总耗时",
      "Agent 执行耗时",
      "人工确认耗时",
      "子任务等待耗时",
      "总 Token",
      "输入 Token",
      "输出 Token",
      "缓存读 Token",
      "缓存写 Token",
      "缓存命中率",
      "执行轮次",
    ]);
    expect(sheet?.rows[1]).toEqual(["1m 45s", "1m", "30s", "15s", "64", "10", "20", "30", "4", "88.2%", "3"]);
    expect(sheet?.rows[2]).toEqual([]);
    expect(sheet?.rows[3]).toEqual([
      "节点",
      "Agent",
      "开始时间",
      "结束时间",
      "执行耗时",
      "Token 合计",
      "输入 Token",
      "输出 Token",
      "缓存读 Token",
      "缓存写 Token",
      "缓存命中率",
      "执行轮次",
      "产物",
    ]);
    expect(sheet?.rows[4]).toEqual([
      "01-需求澄清",
      "01-clarify",
      expect.any(String),
      expect.any(String),
      "1m",
      "10",
      "1",
      "2",
      "3",
      "4",
      "42.9%",
      "5",
      "01-需求澄清\n02-design",
    ]);
    expect(sheet?.rows[5]).toEqual([
      "02-方案设计",
      "02-design",
      expect.any(String),
      expect.any(String),
      "0m",
      "0",
      "0",
      "0",
      "0",
      "0",
      "暂无",
      "0",
      "-",
    ]);
    expect(sheet?.hyperlinks).toEqual([
      { row: 4, col: 12, target: "http://localhost:3000/api/attachments/att-1/download", tooltip: "01-需求澄清" },
    ]);
    expect(artifactSheet?.name).toBe("产物链接");
    expect(artifactSheet?.rows).toEqual([
      ["节点", "Agent", "产物", "链接"],
      ["01-需求澄清", "01-clarify", "01-需求澄清", "http://localhost:3000/api/attachments/att-1/download"],
      ["01-需求澄清", "01-clarify", "02-design", "http://localhost:3000/api/attachments/att-2/download"],
    ]);
    expect(artifactSheet?.hyperlinks).toEqual([
      { row: 1, col: 2, target: "http://localhost:3000/api/attachments/att-1/download", tooltip: "01-需求澄清" },
      { row: 2, col: 2, target: "http://localhost:3000/api/attachments/att-2/download", tooltip: "02-design" },
    ]);
    expect(sheet?.rows.flat()).not.toContain("row_type");
    expect(sheet?.rows.flat()).not.toContain("issue_id");
    expect(sheet?.rows.flat()).not.toContain("total_duration_ms");
    expect(sheet?.rows.flat()).not.toContain("node_input_tokens");
    expect(sheet?.rows.flat()).not.toContain("child_issue");
    expect(sheet?.rows.flat()).not.toContain("状态");
    expect(sheet?.rows.flat()).not.toContain("已完成");
    expect(sheet?.rows.flat()).not.toContain("运行中");
  });

  it("aggregates repeated runs from the same agent node", () => {
    const baseNode = {
      issue_id: "issue-1",
      node_id: "task:base",
      node_type: "agent_task",
      agent_id: "agent-pm",
      agent_name: "PM-项目经理",
      status: "completed",
      started_at: "2026-06-09T10:00:00.000Z",
      completed_at: "2026-06-09T10:01:00.000Z",
      duration_ms: 60_000,
      input_tokens: 10,
      output_tokens: 20,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      message_count: 1,
      agent_turn_count: 2,
      trace_event_count: 1,
      usage_unavailable_trace: false,
      summary: "pm run",
      evidence_refs: [],
      artifacts: [artifact({
        id: "att-old",
        task_id: "task-1",
        filename: "handoff.md",
        title: "handoff",
        created_at: "2026-06-09T10:01:00.000Z",
      })],
    } as IssueTimelineNode;

    const rows = buildAgentNodeRows([
      { ...baseNode, node_id: "task:task-1" },
      {
        ...baseNode,
        node_id: "task:task-2",
        started_at: "2026-06-09T10:02:00.000Z",
        completed_at: "2026-06-09T10:03:00.000Z",
        input_tokens: 5,
        output_tokens: 7,
        agent_turn_count: 1,
        artifacts: [artifact({
          id: "att-new",
          task_id: "task-2",
          filename: "handoff.md",
          title: "handoff",
          created_at: "2026-06-09T10:03:00.000Z",
        })],
      },
    ]);

    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      key: "agent-pm",
      label: "PM-项目经理",
      runCount: 2,
      taskIds: ["task-1", "task-2"],
    });
    expect(rows[0]?.node.duration_ms).toBe(120_000);
    expect(rows[0]?.node.input_tokens).toBe(15);
    expect(rows[0]?.node.output_tokens).toBe(27);
    expect(rows[0]?.node.agent_turn_count).toBe(3);
    expect(rows[0]?.node.artifacts).toHaveLength(1);
    expect(rows[0]?.node.artifacts?.[0]?.id).toBe("att-new");
  });

  it("keeps repeated PM runs on one horizontal timeline row while keeping node aggregation available", () => {
    const baseNode = {
      issue_id: "issue-1",
      node_id: "task:pm-1",
      node_type: "agent_task",
      agent_id: "agent-pm",
      agent_name: "PM-项目经理",
      status: "completed",
      started_at: "2026-06-09T10:00:00.000Z",
      completed_at: "2026-06-09T10:01:00.000Z",
      duration_ms: 60_000,
      input_tokens: 10,
      output_tokens: 20,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      message_count: 1,
      agent_turn_count: 2,
      trace_event_count: 1,
      usage_unavailable_trace: false,
      summary: "pm run",
      evidence_refs: [],
      artifacts: [],
    } as IssueTimelineNode;

    const timelineRows = buildTimelineAgentRows([
      baseNode,
      {
        ...baseNode,
        node_id: "task:pm-2",
        started_at: "2026-06-09T10:02:00.000Z",
        completed_at: "2026-06-09T10:03:00.000Z",
      },
    ]);

    expect(timelineRows).toHaveLength(1);
    expect(timelineRows[0]?.label).toBe("PM-项目经理");
    expect(timelineRows[0]?.key).toBe("agent-pm");
    expect(timelineRows[0]?.segments?.map((segment) => segment.key)).toEqual(["agent-pm:pm-1", "agent-pm:pm-2"]);
    expect(timelineRows[0]?.segments?.map((segment) => segment.ordinal)).toEqual([1, 2]);
    expect(timelineRows[0]?.segments?.map((segment) => segment.total)).toEqual([2, 2]);
    expect(timelineRows[0]?.node?.duration_ms).toBe(120_000);
    expect(buildAgentNodeRows(timelineRows.map((row) => row.node as IssueTimelineNode))[0]?.runCount).toBe(1);
    expect(buildAgentNodeRows(timelineRows.flatMap((row) => row.segments?.map((segment) => segment.node) ?? []))[0]?.runCount).toBe(2);
  });

  it("keeps human confirmation and child waits on dedicated horizontal timeline rows", () => {
    const agentNode = {
      issue_id: "issue-1",
      node_id: "task:pm-1",
      node_type: "agent_task",
      agent_id: "agent-pm",
      agent_name: "PM-项目经理",
      status: "completed",
      started_at: "2026-06-09T10:00:00.000Z",
      completed_at: "2026-06-09T10:01:00.000Z",
      duration_ms: 60_000,
      input_tokens: 10,
      output_tokens: 20,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      message_count: 1,
      agent_turn_count: 2,
      trace_event_count: 1,
      usage_unavailable_trace: false,
      summary: "pm run",
      evidence_refs: [],
      artifacts: [],
    } as IssueTimelineNode;
    const waitNode = {
      issue_id: "issue-1",
      node_id: "human_confirmation:comment-1:pm-2",
      node_type: "human_confirmation",
      status: "completed",
      started_at: "2026-06-09T10:01:00.000Z",
      completed_at: "2026-06-09T10:06:00.000Z",
      duration_ms: 300_000,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      message_count: 0,
      agent_turn_count: 0,
      trace_event_count: 0,
      usage_unavailable_trace: false,
      summary: "等待人工确认：确认继续",
      evidence_refs: [{ type: "comment", id: "comment-1" }],
    } as IssueTimelineNode;
    const childNode = {
      issue_id: "issue-1",
      node_id: "child_issue_ref:child-1",
      node_type: "child_issue_ref",
      child_issue_id: "child-1",
      status: "done",
      started_at: "2026-06-09T10:06:00.000Z",
      completed_at: "2026-06-09T10:10:00.000Z",
      duration_ms: 240_000,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      message_count: 0,
      agent_turn_count: 0,
      trace_event_count: 0,
      usage_unavailable_trace: false,
      summary: "跨项目验收标记：gateway request id / middleware acceptance marker",
      evidence_refs: [{ type: "child_issue", id: "child-1" }],
    } as IssueTimelineNode;

    const rows = buildTimelineBarRows(
      buildTimelineAgentRows([agentNode]),
      [{ key: "child-1", label: "gateway", issue: { id: "child-1", status: "done", title: "gateway" } as Issue }],
      [agentNode, waitNode, childNode],
    );

    expect(rows.map((row) => row.key)).toEqual(["human-confirmation", "child-issue-wait", "agent-pm"]);
    expect(rows[0]).toMatchObject({
      label: "人工确认",
      kind: "human_confirmation",
      subtitle: "已完成",
    });
    expect(rows[0]?.segments).toHaveLength(1);
    expect(rows[0]?.segments[0]).toMatchObject({
      key: "human_confirmation:comment-1:pm-2",
      label: "等待人工确认：确认继续",
      durationMs: 300_000,
      tokenTotal: 0,
    });
    expect(rows[1]).toMatchObject({
      label: "子任务等待",
      kind: "child",
      subtitle: "已完成",
    });
    expect(rows[1]?.segments).toHaveLength(1);
    expect(rows[1]?.segments[0]).toMatchObject({
      key: "child_issue_ref:child-1",
      label: "等待子任务完成：跨项目验收标记：gateway request id / middleware acceptance marker",
      durationMs: 240_000,
      tokenTotal: 0,
    });
    expect(timelineSegmentTooltipRows(rows[2]!, rows[2]!.segments[0]!).map(([label]) => label)).toEqual([
      "节点",
      "开始",
      "结束",
      "耗时",
      "Token",
      "执行轮次",
    ]);
    expect(timelineSegmentTooltipRows(rows[2]!, rows[2]!.segments[0]!)).toContainEqual(["Token", "30"]);
    expect(timelineSegmentTooltipRows(rows[0]!, rows[0]!.segments[0]!).map(([label]) => label)).toEqual([
      "节点",
      "开始",
      "结束",
      "耗时",
    ]);
  });

  it("uses agent responsibility windows without separate dispatch wait segments", () => {
    const firstRun = {
      issue_id: "issue-1",
      node_id: "task:pm-1",
      node_type: "agent_task",
      agent_id: "agent-pm",
      agent_name: "PM-项目经理",
      status: "completed",
      started_at: "2026-06-09T10:00:00.000Z",
      actual_started_at: "2026-06-09T10:00:00.000Z",
      completed_at: "2026-06-09T10:01:00.000Z",
      duration_ms: 60_000,
      input_tokens: 10,
      output_tokens: 20,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      message_count: 1,
      agent_turn_count: 1,
      trace_event_count: 1,
      usage_unavailable_trace: false,
      summary: "pm first run",
      evidence_refs: [],
      artifacts: [],
    } as IssueTimelineNode;
    const secondRun = {
      ...firstRun,
      node_id: "task:pm-2",
      started_at: "2026-06-09T10:01:00.000Z",
      actual_started_at: "2026-06-09T10:06:00.000Z",
      completed_at: "2026-06-09T10:07:00.000Z",
      duration_ms: 360_000,
      summary: "pm second run",
    } as IssueTimelineNode;

    const agentRows = buildTimelineAgentRows([firstRun, secondRun]);
    const rows = buildTimelineBarRows(agentRows, [], [firstRun, secondRun]);

    expect(rows.map((row) => row.key)).toEqual(["agent-pm"]);
    expect(rows[0]?.segments.map((segment) => segment.node.node_type)).toEqual(["agent_task", "agent_task"]);
    expect(rows[0]?.segments[1]).toMatchObject({
      key: "agent-pm:pm-2",
      label: "PM-项目经理",
      durationMs: 360_000,
      tokenTotal: 30,
    });
    const tooltipRows = timelineSegmentTooltipRows(rows[0]!, rows[0]!.segments[1]!);
    expect(tooltipRows).toContainEqual(["开始", expect.stringMatching(/:01:00$/)]);
    expect(tooltipRows.map(([label]) => label)).not.toContain("接手");
    expect(tooltipRows.map(([label]) => label)).not.toContain("实际开始");
    expect(tooltipRows.map(([label]) => label)).not.toContain("片段");
  });

  it("uses true timeline proportions for short run segments", () => {
    const spanMs = 753_000;
    const shortRunStart = 0;
    const shortRunEnd = 20_108;

    const width = timelineSegmentWidthPercent(shortRunStart, shortRunEnd, spanMs);

    expect(width).toBeCloseTo(2.67, 2);
    expect(timelineSegmentStyle(shortRunStart, shortRunEnd, 0, spanMs)).toMatchObject({
      left: "0%",
      width: `${width}%`,
    });
    expect(shouldShowTimelineSegmentText(width)).toBe(false);
    expect(shouldShowTimelineSegmentText(10.76)).toBe(true);
  });

  it("does not inflate short timeline runs to one minute", () => {
    const timing = timelineTiming({
      issue_id: "issue-1",
      node_id: "task:short-run",
      node_type: "agent_task",
      agent_id: "agent-pm",
      agent_name: "PM-项目经理",
      status: "completed",
      started_at: "2026-06-09T10:00:00.000Z",
      completed_at: "2026-06-09T10:00:20.000Z",
      duration_ms: 20_000,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      message_count: 0,
      agent_turn_count: 0,
      trace_event_count: 0,
      usage_unavailable_trace: false,
      summary: "",
      evidence_refs: [],
      artifacts: [],
    } as IssueTimelineNode);

    expect(timing.durationMs).toBe(20_000);
    expect((timing.endMs ?? 0) - (timing.startMs ?? 0)).toBe(20_000);
  });

  it("exports raw event XLSX rows with detail, metadata, and raw evidence", () => {
    const rows = [
      {
        id: "event-1",
        kind: "trace",
        category: "Trace",
        timestampMs: 100,
        timeLabel: "06/09 10:00",
        taskId: "task-1",
        sourceLabel: "codex",
        object: "task.failed",
        title: "任务失败",
        outcome: "异常",
        summary: "异常摘要：timeout",
        detail: "line 1\nline 2, with comma",
        metadataDetail: 'metadata:\n{"quote":"yes"}',
        durationMs: 1200,
        tokenTotal: 30,
        severity: "error",
        rawSourceLabel: "task_trace_event",
        rawPayload: { id: "trace-1", event_type: "task.failed" },
        linkedRawPayloads: [{ label: "关联 task_message #1 文本", payload: { seq: 1, content: "hello" } }],
      },
    ] satisfies ReturnType<typeof buildRunReviewEventRows>;

    const [sheet] = buildRunReviewRawEventsXlsxSheets(rows);

    expect(sheet?.name).toBe("RAW 交互信息");
    expect(sheet?.rows[0]?.slice(0, 5)).toEqual(["id", "类型", "分类", "时间", "时间戳(ms)"]);
    expect(sheet?.rows[0]?.slice(-3)).toEqual(["原始来源", "raw_json", "linked_raw_json"]);
    expect(sheet?.rows[1]).toContain("line 1\nline 2, with comma");
    expect(sheet?.rows[1]).toContain('metadata:\n{"quote":"yes"}');
    expect(sheet?.rows[1]).toContain("task_trace_event");
    expect(sheet?.rows[1]).toContain('{\n  "id": "trace-1",\n  "event_type": "task.failed"\n}');
    expect(String(sheet?.rows[1]?.at(-1))).toContain("关联 task_message #1 文本");
  });

  it("creates a draft evaluation case with run snapshot, prompt snapshots, tools, and assertions", () => {
    const issue = {
      id: "issue-1",
      identifier: "ISS-1",
      title: "优化运行复盘",
      status: "done",
      project: { title: "goal-test" },
    } as Issue;
    const tree = {
      root: {
        tasks: [task()],
        task_messages: [
          message({
            seq: 1,
            type: "text",
            content: "01 阶段输入：用户希望事件流可诊断。handoff: 已明确验收口径。",
          }),
        ],
        trace_events: [trace({ event_type: "task.completed", event_name: "任务完成", status: "completed", failure_reason: "", error_type: "" })],
        tool_call_chains: [
          tool({
            id: "tool-search",
            tool: "exec_command",
            input: { command: "rg -n \"生成评测用例\" packages/views" },
            output: "packages/views/run-reviews/components/run-reviews-page.tsx:226",
            failure_signal: false,
            failure_reason: "",
          }),
          tool({
            id: "tool-branch",
            tool: "Bash",
            input: { command: "cd /repo && git branch -a 2>&1" },
            output: "Command: cd /repo && git branch -a 2>&1\nStdout: remotes/origin/v2.1.0_qc_timeout\n\nStderr: (empty)",
            failure_signal: true,
            failure_reason: "工具结果包含超时信息",
          }),
        ],
        children: [],
      },
      issue_summary: {
        issue_id: "issue-1",
        node_count: 1,
        total_duration_ms: 120000,
        total_input_tokens: 10,
        total_output_tokens: 20,
        total_cache_read_tokens: 0,
        total_cache_write_tokens: 0,
        message_count: 1,
        agent_turn_count: 1,
        trace_event_count: 1,
        usage_unavailable: false,
        acceptance_status: "done",
        full_analysis_deep_link: "",
      },
      timeline_nodes: [
        {
          issue_id: "issue-1",
          node_id: "task:task-1",
          node_type: "agent_task",
          agent_id: "agent-1",
          agent_name: "01-clarify",
          status: "completed",
          started_at: "2026-06-09T10:00:00.000Z",
          completed_at: "2026-06-09T10:02:00.000Z",
          duration_ms: 120000,
          input_tokens: 10,
          output_tokens: 20,
          cache_read_tokens: 0,
          cache_write_tokens: 0,
          message_count: 1,
          agent_turn_count: 1,
          trace_event_count: 1,
          usage_unavailable_trace: false,
          summary: "需求澄清完成",
          evidence_refs: [{ type: "agent_task", id: "task-1" }],
        },
      ],
    } as unknown as IssueExecutionTreeResponse;
    const stageRows = [
      {
        key: "01",
        label: "01-需求澄清",
        names: ["01-clarify"],
        node: tree.timeline_nodes?.[0],
      },
      {
        key: "02",
        label: "02-方案设计",
        names: ["02-design"],
        node: undefined,
      },
    ];

    const request = buildIssueReviewDraftCaseRequest({
      issue,
      tree,
      stageRows: stageRows as never,
      childLanes: [],
      assetId: "asset-1",
      promptId: null,
    });
    const input = request.input as Record<string, unknown>;
    const expected = request.expected as Record<string, unknown>;
    const runSnapshot = input.run_snapshot as Record<string, unknown>;
    const stages = runSnapshot.stages as Array<Record<string, unknown>>;
    const toolEvidence = runSnapshot.tool_evidence as Array<Record<string, unknown>>;
    const promptSnapshots = runSnapshot.prompt_skill_snapshots as Array<Record<string, unknown>>;
    const assertions = expected.assertions as Record<string, unknown>;

    expect(request.status).toBe("draft");
    expect(request.tags).toEqual(expect.arrayContaining(["run-snapshot", "prompt-snapshot", "skill-snapshot"]));
    expect(stages[0]).toMatchObject({
      stage: "01-需求澄清",
      task_id: "task-1",
      input_summary: expect.stringContaining("用户要求优化"),
      output_summary: expect.stringContaining("需求边界明确"),
    });
    expect(toolEvidence[0]).toMatchObject({
      category: "搜索",
      action: "搜索代码：生成评测用例",
    });
    expect(toolEvidence.find((item) => item.id === "tool-branch")).toMatchObject({
      failure_signal: false,
      failure_reason: "",
    });
    expect(promptSnapshots[0]).toMatchObject({
      role: "01-需求澄清",
      task_id: "task-1",
      content_hash: expect.stringMatching(/^fnv1a:/),
      skill_path: ".codebuddy/skills/01-clarify/SKILL.md",
    });
    expect(assertions.required_stages).toContain("01-需求澄清");
    expect(assertions.disallow_missing_required_stage).toBe(true);
    expect(assertions.must_keep_evidence).toBe(true);
    expect(assertions.must_report_blocker_on_failure).toBe(true);
    expect(request.expected_contains).toContain("05-验证测试");
  });
});
