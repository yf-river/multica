import { api } from "@multica/core/api";
import type {
  CreatePromptEvaluationCaseRequest,
  Issue,
  IssueExecutionNode,
  IssueExecutionTreeResponse,
} from "@multica/core/types";
import type { TaskMessagePayload } from "@multica/core/types/events";
import { SOP_STAGE_DEFINITIONS, normalizeSopStageName } from "../../common/sop-stage-labels";
import {
  extractErrorLine,
  flattenExecutionNodes,
  semanticToolAction,
  summarizeToolOutput,
  taskMessageText,
  toolMessageKey,
  toolOutputText,
} from "./run-review-events";
import { firstNonEmpty, formatJSON, stringFromUnknown, truncateText } from "./run-review-format";
import { buildChildLanes, buildStageRows } from "./run-review-timeline";

const STAGES = SOP_STAGE_DEFINITIONS;
const ISSUE_REVIEW_DRAFT_DATASET_NAME = "Issue 复盘评测 Draft";

export async function createIssueReviewDraftCase(
  issue: Issue,
  tree: IssueExecutionTreeResponse | undefined,
  stageRows: ReturnType<typeof buildStageRows>,
  childLanes: ReturnType<typeof buildChildLanes>,
) {
  if (!tree) throw new Error("执行树尚未加载，不能生成评测用例");
  const assets = await api.listPromptEvaluationAssets({ asset_type: "数据集", status: "启用" });
  let asset = assets.items.find((item) => item.name === ISSUE_REVIEW_DRAFT_DATASET_NAME);
  if (!asset) {
    asset = await api.createPromptEvaluationAsset({
      name: ISSUE_REVIEW_DRAFT_DATASET_NAME,
      description: "从运行复盘生成的 eval case draft，需人工确认后进入 active 评测集。",
      asset_type: "数据集",
      status: "启用",
      payload: {
        schema_version: 1,
        schema: "multica.training_evaluation.payload.v1",
        语义版本: "multica.training_evaluation.v1",
        cases: [],
        payload_contract: {
          source: "run-review",
          review_flow: "draft -> approved -> active",
        },
      },
    });
  }
  return api.createPromptEvaluationCase(buildIssueReviewDraftCaseRequest({
    issue,
    tree,
    stageRows,
    childLanes,
    assetId: asset.id,
    promptId: asset.prompt_id,
  }));
}

export function buildIssueReviewDraftCaseRequest({
  issue,
  tree,
  stageRows,
  childLanes,
  assetId,
  promptId,
}: {
  issue: Issue;
  tree: IssueExecutionTreeResponse;
  stageRows: ReturnType<typeof buildStageRows>;
  childLanes: ReturnType<typeof buildChildLanes>;
  assetId: string;
  promptId?: string | null;
}): CreatePromptEvaluationCaseRequest {
  const stageFacts = stageRows.map((stage) => ({
    stage: stage.label,
    status: stage.node ? stage.node.status : "missing",
    agent: stage.node?.agent_name ?? stage.key,
    duration_ms: stage.node?.duration_ms ?? 0,
    token_total: (stage.node?.input_tokens ?? 0) + (stage.node?.output_tokens ?? 0),
    turns: stage.node?.agent_turn_count ?? 0,
    artifacts: stage.node?.artifacts ?? [],
    evidence_refs: stage.node?.evidence_refs ?? [],
  }));
  const childFacts = childLanes.map((lane) => ({
    lane: lane.key,
    issue_id: lane.issue.id,
    identifier: lane.issue.identifier,
    title: lane.issue.title,
    status: lane.issue.status,
  }));
  const caseName = `${issue.identifier ? `${issue.identifier} ` : ""}${issue.title}`.trim() || `issue ${issue.id}`;
  const runSnapshot = buildIssueReviewRunSnapshot(issue, tree, stageRows, stageFacts, childFacts);
  return {
    asset_id: assetId,
    prompt_id: promptId ?? null,
    case_name: `Draft: ${caseName}`,
    variables: {
      issue_id: issue.id,
      issue_identifier: issue.identifier,
      issue_title: issue.title,
      project: issue.project?.title ?? "",
      current_status: issue.status,
      source: "run-review",
    },
    expected_contains: ["PM-项目经理", "01-需求澄清", "02-方案设计", "03-任务拆分", "04-开发", "05-验证测试", "证据"],
    input: {
      source: "run-review",
      issue: {
        id: issue.id,
        identifier: issue.identifier,
        title: issue.title,
        project: issue.project?.title ?? null,
        status: issue.status,
      },
      run_review: {
        issue_summary: tree.issue_summary ?? null,
        stage_facts: stageFacts,
        child_lanes: childFacts,
        timeline_node_count: tree.timeline_nodes?.length ?? 0,
      },
      run_snapshot: runSnapshot,
    },
    expected: {
      expected_behavior: "能复现该 issue 的 PM+01-05 执行链路，识别实际关联的跨项目子任务，并保留可追溯证据。",
      validation: "检查 DAG/子任务、阶段节点、token/耗时/轮次、实际 child lane、evidence refs 和结构化断言。",
      assertions: buildIssueReviewAssertions(issue, stageRows, childLanes, tree),
      approval_required: true,
      review_flow: "draft -> approved -> active",
    },
    tags: [
      "issue-review",
      "draft",
      "run-snapshot",
      "prompt-snapshot",
      "skill-snapshot",
      `issue:${issue.id}`,
      issue.project?.title ?? "unknown-project",
    ],
    status: "draft",
  };
}

function buildIssueReviewRunSnapshot(
  issue: Issue,
  tree: IssueExecutionTreeResponse,
  stageRows: ReturnType<typeof buildStageRows>,
  stageFacts: Array<Record<string, unknown>>,
  childFacts: Array<Record<string, unknown>>,
) {
  const nodes = flattenExecutionNodes(tree);
  const nodeByTaskId = new Map<string, IssueExecutionNode>();
  for (const node of nodes) {
    for (const task of node.tasks ?? []) nodeByTaskId.set(task.id, node);
  }
  const stages = stageRows.map((stage) => buildRunSnapshotStage(stage, nodeByTaskId));
  const promptSkillSnapshots = stages.map((stage) => buildPromptSkillSnapshot(stage));
  return {
    schema_version: 1,
    schema: "multica.run_review.snapshot.v1",
    source: "run-review",
    captured_at: new Date().toISOString(),
    issue: {
      id: issue.id,
      identifier: issue.identifier,
      title: issue.title,
      project: issue.project?.title ?? null,
      status: issue.status,
    },
    summary: tree.issue_summary ?? null,
    stage_facts: stageFacts,
    child_lanes: childFacts,
    stages,
    tool_evidence: buildRunSnapshotToolEvidence(nodes),
    prompt_skill_snapshots: promptSkillSnapshots,
    evidence_refs: buildRunSnapshotEvidenceRefs(tree, stages, promptSkillSnapshots),
    timeline_node_count: tree.timeline_nodes?.length ?? 0,
    source_limits: {
      prompt_capture: "best_effort_task_trace_snapshot",
      content_truncation_chars: 1200,
      raw_tool_output_truncation_chars: 1200,
      formal_prompt_library_write: false,
    },
    review_flow: "draft -> approved -> active",
  };
}

type StageRowForSnapshot = ReturnType<typeof buildStageRows>[number];

function buildRunSnapshotStage(stage: StageRowForSnapshot, nodeByTaskId: Map<string, IssueExecutionNode>) {
  const taskId = stage.node?.node_type === "agent_task" ? stage.node.node_id.replace(/^task:/, "") : "";
  const node = taskId ? nodeByTaskId.get(taskId) : undefined;
  const task = node?.tasks?.find((item) => item.id === taskId);
  const messages = (node?.task_messages ?? []).filter((message) => !taskId || message.task_id === taskId);
  const traceEvents = (node?.trace_events ?? []).filter((event) => !taskId || event.task_id === taskId);
  const toolChains = (node?.tool_call_chains ?? []).filter((chain) => !taskId || chain.task_id === taskId);
  const outputText = firstNonEmpty(
    stringFromUnknown(task?.result),
    task?.error ?? "",
    latestMessageText(messages),
    traceEvents.find((event) => event.failure_reason)?.failure_reason ?? "",
  );
  const inputText = firstNonEmpty(
    task?.trigger_summary ?? "",
    messages.find((message) => message.type === "tool_use")?.input ? formatJSON(messages.find((message) => message.type === "tool_use")?.input) : "",
    messages.find((message) => message.content)?.content ?? "",
  );
  const handoffText = messages
    .map((message) => taskMessageText(message))
    .find((text) => /handoff|交接|结论|阻断|验收|完成/i.test(text));
  return {
    stage: stage.label,
    stage_key: stage.key,
    status: stage.node ? stage.node.status : "missing",
    agent: stage.node?.agent_name ?? stage.key,
    task_id: taskId || null,
    agent_id: stage.node?.agent_id ?? task?.agent_id ?? null,
    runtime_id: task?.runtime_id ?? traceEvents.find((event) => event.runtime_id)?.runtime_id ?? null,
    started_at: stage.node?.started_at ?? task?.started_at ?? null,
    completed_at: stage.node?.completed_at ?? task?.completed_at ?? null,
    duration_ms: stage.node?.duration_ms ?? 0,
    token_total: (stage.node?.input_tokens ?? 0) + (stage.node?.output_tokens ?? 0),
    turns: stage.node?.agent_turn_count ?? 0,
    input_summary: truncateText(inputText, 420),
    output_summary: truncateText(outputText, 420),
    handoff_summary: truncateText(handoffText ?? "", 420),
    failure_reason: task?.error ?? traceEvents.find((event) => event.failure_reason)?.failure_reason ?? "",
    message_refs: messages.slice(0, 20).map((message) => ({ type: "task_message", task_id: message.task_id, seq: message.seq })),
    trace_refs: traceEvents.slice(0, 20).map((event) => ({ type: "trace_event", id: event.id, task_id: event.task_id })),
    tool_refs: toolChains.slice(0, 20).map((chain) => ({ type: "tool_call_chain", id: chain.id, task_id: chain.task_id })),
    artifacts: stage.node?.artifacts ?? [],
    evidence_refs: stage.node?.evidence_refs ?? [],
    prompt_capture_text: truncateText(firstNonEmpty(inputText, task?.trigger_summary ?? "", latestMessageText(messages)), 1200),
    runtime: {
      provider: firstNonEmpty(traceEvents.find((event) => event.provider)?.provider ?? ""),
      model: firstNonEmpty(traceEvents.find((event) => event.model)?.model ?? ""),
    },
  };
}

function buildRunSnapshotToolEvidence(nodes: IssueExecutionNode[]) {
  const chains = nodes.flatMap((node) => node.tool_call_chains ?? []);
  const messages = nodes.flatMap((node) => node.task_messages ?? []).filter((message) => message.type === "tool_use" || message.type === "tool_result");
  const chainRows = chains.map((chain) => {
    const semantic = semanticToolAction(chain.tool, chain.input, chain.output);
    const backendFailure = chain.failure_signal && !semantic.suppressFailureSignal;
    return {
      id: chain.id,
      task_id: chain.task_id,
      source: "tool_call_chain",
      tool: chain.tool,
      category: semantic.category,
      action: semantic.title,
      object: semantic.object,
      status: chain.status,
      outcome: semantic.outcome,
      failure_signal: backendFailure || semantic.severity === "error",
      failure_reason: firstNonEmpty(backendFailure ? chain.failure_reason : "", semantic.severity === "error" ? extractErrorLine(toolOutputText(chain.output ?? "")) : ""),
      input_summary: truncateText(chain.input ? formatJSON(chain.input) : "", 420),
      output_summary: truncateText(firstNonEmpty(semantic.summary, summarizeToolOutput(toolOutputText(chain.output ?? ""))), 420),
      raw_output_excerpt: truncateText(chain.output ?? "", 1200),
      duration_ms: chain.duration_ms ?? 0,
      created_at: chain.created_at,
      completed_at: chain.completed_at,
      evidence_ref: { type: "tool_call_chain", id: chain.id, task_id: chain.task_id },
    };
  });
  const chainMessageKeys = new Set<string>();
  for (const chain of chains) {
    if (chain.task_id && chain.use_seq) chainMessageKeys.add(toolMessageKey(chain.task_id, chain.use_seq));
    if (chain.task_id && chain.result_seq) chainMessageKeys.add(toolMessageKey(chain.task_id, chain.result_seq));
  }
  const orphanRows = messages
    .filter((message) => !chainMessageKeys.has(toolMessageKey(message.task_id, message.seq)))
    .map((message) => {
      const semantic = semanticToolAction(message.tool, message.input, message.output);
      return {
        id: `${message.task_id}:${message.seq}`,
        task_id: message.task_id,
        source: "task_message",
        tool: message.tool ?? "",
        category: semantic.category,
        action: semantic.title,
        object: semantic.object,
        status: message.type,
        outcome: semantic.outcome,
        failure_signal: semantic.severity === "error",
        failure_reason: extractErrorLine(toolOutputText(message.output ?? "")),
        input_summary: truncateText(message.input ? formatJSON(message.input) : "", 420),
        output_summary: truncateText(firstNonEmpty(semantic.summary, summarizeToolOutput(toolOutputText(message.output ?? ""))), 420),
        raw_output_excerpt: truncateText(message.output ?? "", 1200),
        duration_ms: 0,
        created_at: message.created_at ?? "",
        completed_at: message.created_at ?? "",
        evidence_ref: { type: "task_message", task_id: message.task_id, seq: message.seq },
      };
    });
  return [...chainRows, ...orphanRows].slice(0, 80);
}

function buildPromptSkillSnapshot(stage: ReturnType<typeof buildRunSnapshotStage>) {
  const promptText = stage.prompt_capture_text;
  const agentName = typeof stage.agent === "string" ? stage.agent : "";
  const skillPath = sopSkillPathForAgent(agentName);
  const captureStatus = promptText ? "captured_excerpt" : skillPath ? "ref_only" : "missing";
  return {
    role: stage.stage,
    stage_key: stage.stage_key,
    task_id: stage.task_id,
    agent: stage.agent,
    source: promptText ? "task_trace" : skillPath ? "skill_ref" : "missing",
    capture_status: captureStatus,
    content_summary: truncateText(promptText, 420),
    content_excerpt: promptText,
    content_hash: promptText ? stableContentHash(promptText) : "",
    skill_path: skillPath,
    skill_hash: "",
    runtime_provider: stage.runtime.provider,
    model: stage.runtime.model,
    evidence_refs: [
      ...(stage.message_refs as Array<Record<string, unknown>>).slice(0, 5),
      ...(stage.trace_refs as Array<Record<string, unknown>>).slice(0, 5),
    ],
  };
}

function sopSkillPathForAgent(agentName: string) {
  const normalized = normalizeSopStageName(agentName);
  const stage = SOP_STAGE_DEFINITIONS.find((item) => item.names.some((name) => normalizeSopStageName(name) === normalized));
  if (!stage || stage.key === "pm") return "";
  const canonical = stage.names.find((name) => /^[0-5]{2}-[a-z-]+$/.test(name)) ?? "";
  return canonical ? `.codebuddy/skills/${canonical}/SKILL.md` : "";
}

function buildRunSnapshotEvidenceRefs(
  tree: IssueExecutionTreeResponse,
  stages: Array<ReturnType<typeof buildRunSnapshotStage>>,
  promptSkillSnapshots: Array<ReturnType<typeof buildPromptSkillSnapshot>>,
) {
  const timelineRefs = (tree.timeline_nodes ?? []).slice(0, 80).map((node) => ({ type: "timeline_node", id: node.node_id, node_type: node.node_type }));
  const stageRefs = stages.flatMap((stage) => [
    ...(stage.message_refs as Array<Record<string, unknown>>),
    ...(stage.trace_refs as Array<Record<string, unknown>>),
    ...(stage.tool_refs as Array<Record<string, unknown>>),
  ]);
  const promptRefs = promptSkillSnapshots.map((snapshot) => ({
    type: "prompt_skill_snapshot",
    role: snapshot.role,
    task_id: snapshot.task_id,
    content_hash: snapshot.content_hash,
    capture_status: snapshot.capture_status,
  }));
  return [...stageRefs, ...timelineRefs, ...promptRefs].slice(0, 200);
}

function buildIssueReviewAssertions(
  issue: Issue,
  stageRows: ReturnType<typeof buildStageRows>,
  childLanes: ReturnType<typeof buildChildLanes>,
  tree: IssueExecutionTreeResponse,
) {
  const requiredStages = STAGES.map((stage) => stage.label);
  const missingStages = stageRows.filter((stage) => !stage.node).map((stage) => stage.label);
  const terminalStatus = issue.status === "done" ? "done" : tree.issue_summary?.acceptance_status ?? issue.status;
  return {
    required_stages: requiredStages,
    missing_required_stages: missingStages,
    disallow_missing_required_stage: true,
    must_identify_child_issues: childLanes.length > 0,
    expected_child_issue_count: childLanes.length,
    must_keep_evidence: true,
    must_report_blocker_on_failure: true,
    must_update_done_when_verified: stageRows.some((stage) => stage.key === "05" && stage.node?.status === "completed"),
    expected_terminal_status: terminalStatus,
    require_prompt_skill_snapshot_refs: true,
    require_tool_evidence_on_tool_use: true,
  };
}

function latestMessageText(messages: TaskMessagePayload[]) {
  const message = messages
    .filter((item) => item.type === "text" || item.type === "error")
    .toSorted((a, b) => (b.seq ?? 0) - (a.seq ?? 0))[0];
  return taskMessageText(message ?? ({} as TaskMessagePayload));
}

function stableContentHash(value: string) {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return `fnv1a:${(hash >>> 0).toString(16).padStart(8, "0")}`;
}
