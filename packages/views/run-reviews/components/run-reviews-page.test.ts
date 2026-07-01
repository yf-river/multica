import { describe, expect, it } from "vitest";
import type { AgentTask, Issue, IssueExecutionTreeResponse, IssueTimelineNode, TaskTraceEvent } from "@multica/core/types";
import type { TaskMessagePayload } from "@multica/core/types/events";
import type { PromptEvaluationToolCallChain } from "@multica/core/types/prompt-evaluation";
import {
  buildIssueReviewDraftCaseRequest,
  buildRunReviewNodeCsv,
  buildRunReviewEventRows,
  buildRunReviewOptimizerHref,
  buildRunReviewRawEventsCsv,
  issueRunRowActivityLabel,
  issueRunRowMetaLabels,
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

describe("buildRunReviewOptimizerHref", () => {
  it("keeps issue context on the visible test suites route", () => {
    expect(buildRunReviewOptimizerHref((view) => `/acme/training/${view}`, "issue with space")).toBe(
      "/acme/training/test-suites?issue=issue%20with%20space&mode=optimize",
    );
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

  it("keeps diagnostic task messages, trace events, and tool chains before node summaries", () => {
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

    expect(rows.map((row) => row.kind)).toEqual(["trace", "tool", "node"]);
    expect(rows.find((row) => row.kind === "trace")?.metadataDetail).toContain("stderr");
    expect(rows.find((row) => row.kind === "tool")?.detail).toContain("\"url\": \"/health\"");
    expect(rows.find((row) => row.kind === "tool")?.detail).toContain("raw_tool: curl-check");
    expect(rows.find((row) => row.kind === "trace")?.tokenTotal).toBe(30);
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

  it("exports node CSV with summary metrics and per-node token breakdown", () => {
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
      artifacts: [{
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
      }],
    } as IssueTimelineNode;

    const csv = buildRunReviewNodeCsv(
      issue,
      summary,
      [{ key: "task-1", label: "01-需求澄清", node }] as never,
      [],
    );

    expect(csv).toContain("total_duration_ms,total_token,total_thinking_rounds");
    expect(csv).toContain('summary,issue-1,ISS-1,"优化,运行复盘",120000,64,3');
    expect(csv).toContain('agent_node,issue-1,ISS-1,"优化,运行复盘",120000,64,3,task-1,01-需求澄清,completed,01-clarify');
    expect(csv).toContain(",60000,1,2,3,4,10,5,1");
    expect(csv).toContain("01-需求澄清.md </api/attachments/att-1/download>");
  });

  it("exports raw event CSV with escaped detail and metadata evidence", () => {
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
      },
    ] satisfies ReturnType<typeof buildRunReviewEventRows>;

    const csv = buildRunReviewRawEventsCsv(rows);

    expect(csv).toContain("id,kind,category,time,timestamp_ms");
    expect(csv).toContain('"line 1\nline 2, with comma"');
    expect(csv).toContain('"metadata:\n{""quote"":""yes""}"');
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
