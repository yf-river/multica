import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import pg from "pg";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = acceptanceDir(repoRoot);
const latestPath = path.join(artifactRoot, "goal-e-real-pm-0105-run-latest.json");
const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("NEXT_PUBLIC_API_URL") || "http://127.0.0.1:18762";
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || "goal-test-daemon";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || "e2e-password";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || "goal-test-daemon";
const provider = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER") || "codex";
const model = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL") || "gpt-5.3-codex-spark";
const taskTimeoutMs = Number(trimEnv("GOAL_E_REAL_PM_0105_TASK_TIMEOUT_MS") || 1_800_000);
const pollIntervalMs = Number(trimEnv("GOAL_E_REAL_PM_0105_POLL_INTERVAL_MS") || 5_000);
const canonicalEvidenceTitlePrefix = "Goal E Canonical Demo Real PM+01-05 Model Execution";
const squadName = "pm";
const sopProfileKey = "goal-e-pm-0105-live";
const requiredStages = [
  { key: "pm", role_key: "pm", agent_name: "pm", member_role: "pm", label: "PM", prompt: "请作为 pm 接收本任务，读取任务描述，明确本次真实 PM+01-05 验收的目标、阶段顺序、证据要求，并说明下一步交给 01-clarify。" },
  { key: "01-clarify", role_key: "01-clarify", agent_name: "01", member_role: "01", label: "01 需求澄清", prompt: "请只执行 01-clarify：澄清需求边界、验收口径、输入输出和 handoff，不要修改代码。" },
  { key: "02-design", role_key: "02-design", agent_name: "02", member_role: "02", label: "02 方案设计", prompt: "请只执行 02-design：输出方案、影响面、接口/数据契约和 handoff，不要修改代码。" },
  { key: "03-task-split", role_key: "03-task-split", agent_name: "03", member_role: "03", label: "03 任务拆分", prompt: "请只执行 03-task-split：输出任务拆分、跨项目依赖、阻断点和 handoff；严禁创建子 issue。" },
  { key: "04-implement", role_key: "04-implement", agent_name: "04", member_role: "04", label: "04 代码开发", prompt: "请只执行 04-implement：基于前序产物说明最小实现边界、涉及文件、风险和 handoff；本验收不要实际改代码。" },
  { key: "05-verify", role_key: "05-verify", agent_name: "05", member_role: "05", label: "05 测试验证", prompt: "请只执行 05-verify：独立检查 PM+01-05 链路证据、测试口径、剩余风险和最终 handoff，不要修改代码。" },
];
const targetAgentNames = new Set(requiredStages.map((stage) => stage.agent_name));

const evidence = {
  schema: "multica.goal_e_real_pm_0105_run.v1",
  generated_at: new Date().toISOString(),
  run_type: "real_pm_0105_model_execution",
  api_url: apiURL,
  account,
  workspace_slug: workspaceSlug,
  provider,
  model,
  model_execution_required: true,
  orchestration_mode: "public_api_squad_issue_with_agent_mentions",
  commands: [],
  stages: [],
  result: "unknown",
  ok: false,
};

let token = "";
let workspaceID = "";
const agentSnapshots = new Map();

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.once(signal, () => {
    evidence.result = "interrupted";
    evidence.ok = false;
    evidence.error = `received ${signal} before final PM+01-05 verification completed`;
    writeEvidenceSnapshot("interrupted");
    process.exit(signal === "SIGINT" ? 130 : 143);
  });
}

try {
  mkdirSync(artifactRoot, { recursive: true });
  token = login();
  const workspace = loadWorkspace(token);
  workspaceID = workspace.id;
  evidence.workspace_id = workspaceID;
  const runtime = requireOnlineRuntime(token);
  evidence.runtime = pickRuntime(runtime);
  const { squad, agents } = await ensureTargetPM0105Structure(runtime);
  evidence.squad = { id: squad.id, name: squad.name, profile_key: squad.sop_profile?.profile_key || "" };
  evidence.agents = agents.map((agent) => ({ id: agent.id, name: agent.name, role_key: agent.role_key, model: agent.model || model }));
  const byRole = new Map(agents.map((agent) => [agent.role_key, agent]));
  for (const stage of requiredStages) {
    if (!byRole.get(stage.role_key)?.id) fail(`PM+01-05 通用小队缺少角色 ${stage.role_key}`);
  }

  const issue = post("/api/issues", {
    workspace_id: workspaceID,
    title: `${canonicalEvidenceTitlePrefix} ${Date.now()}`,
    description: [
      "本任务用于 Goal E 最终硬验收：必须真实运行 PM、01-clarify、02-design、03-task-split、04-implement、05-verify 六个模型任务。",
      "每个阶段都要输出中文结果、trace/task id、阶段证据、失败/阻断原因和 handoff。",
      "不要修改代码；本次只验证小队 SOP 运行可观测、可闭环、可复测。",
    ].join("\n"),
    status: "todo",
    priority: "high",
    assignee_type: "squad",
    assignee_id: squad.id,
  }, token);
  if (!issue?.id) fail("创建真实 PM+01-05 issue 失败");
  evidence.issue = { id: issue.id, identifier: issue.identifier || "", title: issue.title };
  evidence.canonical_issue_id = issue.id;
  const issueCreatedAt = issue.created_at || new Date().toISOString();
  writeEvidenceSnapshot("issue-created");
  await verifyNoUnexpectedIssues(issue.id, issueCreatedAt, "issue 创建后");

  const sopRun = await waitForSOPRun(issue.id, squad.id);
  evidence.sop_run = {
    id: sopRun.id,
    profile_key: sopRun.profile_key,
    current_step_key: sopRun.current_step_key,
    status: sopRun.status,
  };

  for (const stage of requiredStages) {
    const agent = byRole.get(stage.role_key);
    let task = await waitForExistingStageTask(issue.id, agent.id, issueCreatedAt, stage);
    if (stage.key !== "pm") {
      if (!task) {
        const triggeredAt = new Date().toISOString();
        const comment = post(`/api/issues/${issue.id}/comments`, {
          content: `[@${agent.name}](mention://agent/${agent.id}) ${stage.prompt}`,
        }, token);
        evidence.commands.push(`trigger_stage_comment ${stage.key} comment_id=${comment?.id || ""}`);
        task = await waitForTask(issue.id, agent.id, triggeredAt, stage);
      } else {
        evidence.commands.push(`reuse_pm_created_stage_task ${stage.key} task_id=${task.id}`);
      }
    }
    if (!task) task = await waitForTask(issue.id, agent.id, issueCreatedAt, stage);
    await recordSOPEvent(sopRun.id, stage, "步骤开始", "进行中", task, { trigger: stage.key, model });
    const terminalTask = await waitForTerminalTask(issue.id, agent.id, task.id, stage);
    const stageEvidence = await buildStageEvidence(issue.id, terminalTask, agent, stage);
    await verifyNoUnexpectedIssues(issue.id, issueCreatedAt, `${stage.key} 完成后`);
    if (stageEvidence.status !== "completed") {
      const text = JSON.stringify(stageEvidence);
      if (isExternalDependencyText(text)) {
        evidence.result = "external_dependency_failure";
        evidence.external_dependency_failure = true;
        evidence.external_dependency_boundary = `${stage.key} 真实模型任务未完成，失败原因属于模型认证、额度、容量或限流边界。`;
        evidence.failed_stage = stageEvidence;
        await writeEvidenceAndExit(0);
      }
      evidence.failed_stage = stageEvidence;
      fail(`${stage.key} 真实模型任务未完成：${stageEvidence.status} ${stageEvidence.failure_reason || stageEvidence.error || ""}`);
    }
    await recordSOPEvent(sopRun.id, stage, "步骤完成", "已完成", terminalTask, {
      task_id: terminalTask.id,
      agent_id: agent.id,
      provider,
      model: stageEvidence.model,
      message_count: stageEvidence.message_count,
      token_total: stageEvidence.token_total,
      trace_event_count: stageEvidence.trace_event_count,
    }, stageEvidence.duration_ms);
    evidence.stages.push(stageEvidence);
    writeEvidenceSnapshot(`${stage.key}-completed`);
  }

  const finalRun = await waitForCompletedSOPRun(issue.id);
  evidence.sop_run_final = {
    id: finalRun.id,
    status: finalRun.status,
    current_step_key: finalRun.current_step_key,
    completed_at: finalRun.completed_at,
    total_duration_ms: finalRun.total_duration_ms,
    metrics: finalRun.metrics,
  };
  const verification = verifyFinalEvidence(finalRun, evidence.stages);
  const observabilityVerification = verifyLiveObservability(issueCreatedAt);
  evidence.live_observability_verification = observabilityVerification;
  if (!observabilityVerification.ok) {
    verification.ok = false;
    verification.gaps.push(...observabilityVerification.gaps);
  }
  evidence.verification = verification;
  evidence.ok = verification.ok;
  evidence.result = verification.ok ? "completed" : "failed";
  if (!verification.ok) fail(`真实 PM+01-05 证据不完整：${verification.gaps.join("; ")}`);
  await writeEvidenceAndExit(0);
} catch (error) {
  evidence.result = evidence.result === "external_dependency_failure" ? evidence.result : "failed";
  evidence.error = error?.message || String(error);
  if (isExternalDependencyText(evidence.error)) {
    evidence.result = "external_dependency_failure";
    evidence.external_dependency_failure = true;
  }
  await writeEvidenceAndExit(evidence.external_dependency_failure ? 0 : 1);
}

function login() {
  const res = post("/auth/login", { account, password }, null);
  if (!res?.token) fail("登录响应缺少 token");
  return res.token;
}

function loadWorkspace(authToken) {
  const workspaces = get("/api/workspaces", authToken);
  const items = Array.isArray(workspaces) ? workspaces : workspaces.items ?? [];
  const workspace = items.find((item) => item.slug === workspaceSlug);
  if (!workspace?.id) fail(`未找到工作区 ${workspaceSlug}`);
  return workspace;
}

function requireOnlineRuntime(authToken) {
  const runtimes = get(`/api/runtimes?workspace_id=${encodeURIComponent(workspaceID)}`, authToken);
  const runtime = (Array.isArray(runtimes) ? runtimes : runtimes.items ?? [])
    .filter((item) => item.provider === provider && item.status === "online" && new Date(item.last_seen_at || 0).getTime() > Date.now() - 180_000)
    .sort((a, b) => new Date(b.last_seen_at || 0).getTime() - new Date(a.last_seen_at || 0).getTime())[0];
  if (!runtime?.id) fail(`未找到 3 分钟内在线的 ${provider} runtime`);
  return runtime;
}

async function ensureTargetPM0105Structure(runtime) {
  const agents = [];
  for (const stage of requiredStages) {
    const agent = await ensureTargetAgent(runtime, stage);
    agents.push(agent);
  }
  const squad = await ensureTargetSquad(agents);
  await ensureOnlyTargetAgentsRemainActive(agents);
  return { squad, agents };
}

async function ensureTargetAgent(runtime, stage) {
  let existing = findAgentByName(stage.agent_name);
  if (existing?.id) {
    await snapshotAgent(existing.id);
    if (existing.archived_at) {
      post(`/api/agents/${existing.id}/restore`, {}, token);
      existing = get(`/api/agents/${existing.id}`, token);
    }
    put(`/api/agents/${existing.id}`, targetAgentMutationBody(runtime, stage), token);
    const updated = get(`/api/agents/${existing.id}`, token);
    return { id: updated.id, name: updated.name, role_key: stage.role_key, model: updated.model || model };
  }

  const created = post("/api/agents", {
    name: stage.agent_name,
    ...targetAgentMutationBody(runtime, stage),
    runtime_id: runtime.id,
    visibility: "workspace",
  }, token);
  await snapshotAgent(created.id);
  return { id: created.id, name: created.name, role_key: stage.role_key, model: created.model || model };
}

function findAgentByName(name) {
  const agents = get("/api/agents?include_archived=true", token);
  const items = Array.isArray(agents) ? agents : agents.items ?? [];
  return items.find((agent) => agent.name === name) || null;
}

function targetAgentMutationBody(runtime, stage) {
  return {
    description: `Goal E 通用 PM+01-05 验收角色：${stage.label}`,
    instructions: targetAgentInstructions(stage),
    runtime_id: runtime.id,
    runtime_config: {
      provider: runtime.provider,
      goal_e_acceptance_role: stage.role_key,
      temporary_read_only_run_guard: true,
    },
    visibility: "workspace",
    max_concurrent_tasks: 1,
    model,
    custom_args: readOnlyCustomArgs(),
    mcp_config: {},
  };
}

function targetAgentInstructions(stage) {
  return [
    `你是 Goal E 真实 PM+01-05 验收链路中的 ${stage.agent_name}。`,
    `本阶段只执行 ${stage.role_key}，必须输出中文结论、输入理解、阶段产物、handoff、风险和可复测证据。`,
    "这是验收任务，不是实际研发任务：严禁修改代码、运行测试、创建/删除/更新 issue、创建子任务、清理数据库、部署、调用 multica CLI 写操作或改动任何外部系统。",
    "如果你需要引用证据，只能基于当前任务描述、前序评论和只读上下文进行总结；不要尝试补做工程操作。",
    "如果工具或权限阻断，明确写出阻断原因，不要绕过守卫。",
  ].join("\n");
}

function readOnlyCustomArgs() {
  if (provider !== "codex") return [];
  return [
    "-c", "sandbox_mode=\"read-only\"",
    "-c", "approval_policy=\"never\"",
  ];
}

async function ensureTargetSquad(agents) {
  const byRole = new Map(agents.map((agent) => [agent.role_key, agent]));
  const leader = byRole.get("pm");
  if (!leader?.id) fail("pm agent 不存在，无法创建 pm 小队");
  const profile = targetSOPProfile();
  let squad = findSquadByName(squadName);
  const body = {
    name: squadName,
    description: "唯一 SOP 流程执行小队：围绕 usercenter、gateway、ida-deployment 三个项目运行 PM + 01-05。",
    instructions: "使用 PM、01-clarify、02-design、03-task-split、04-implement、05-verify 六个通用角色推进；运行必须可观测、可闭环、可复测。",
    leader_id: leader.id,
    sop_profile: profile,
  };
  if (squad?.id) {
    squad = put(`/api/squads/${squad.id}`, body, token);
  } else {
    squad = post("/api/squads", body, token);
  }
  await ensureSquadMembers(squad.id, agents);
  return get(`/api/squads/${squad.id}`, token);
}

function targetSOPProfile() {
  return {
    profile_key: sopProfileKey,
    mode: "goal_e_pm_0105_stage_chain",
    model_policy: { "默认模型": model, "额度不足回退模型": "gpt-5.4-mini" },
    steps: requiredStages.map((stage) => ({
      key: stage.key,
      name: stage.label,
      role_key: stage.role_key,
    })),
    acceptance: [
      "六个阶段均有独立 task_id",
      "每个阶段均有 message、trace、usage 和 duration",
      "运行看板必须能看到 PM+01-05 stage breakdown",
      "不得创建非预期 issue 或清理运行证据",
    ],
  };
}

function findSquadByName(name) {
  const squads = get("/api/squads", token);
  const items = Array.isArray(squads) ? squads : squads.items ?? [];
  return items.find((squad) => squad.name === name) || null;
}

async function ensureSquadMembers(squadID, agents) {
  const members = get(`/api/squads/${squadID}/members`, token);
  const items = Array.isArray(members) ? members : members.items ?? [];
  for (const stage of requiredStages) {
    const agent = agents.find((item) => item.role_key === stage.role_key);
    const existing = items.find((item) => item.member_type === "agent" && item.member_id === agent.id);
    const body = { member_type: "agent", member_id: agent.id, role: stage.member_role };
    if (existing) {
      if (existing.role !== stage.member_role) patch(`/api/squads/${squadID}/members/role`, body, token);
    } else {
      post(`/api/squads/${squadID}/members`, body, token);
    }
  }
}

async function ensureOnlyTargetAgentsRemainActive(agents) {
  const targetIDs = new Set(agents.map((agent) => agent.id));
  const all = get("/api/agents?include_archived=true", token);
  const items = Array.isArray(all) ? all : all.items ?? [];
  const archived = [];
  for (const agent of items) {
    if (agent.archived_at || targetIDs.has(agent.id) || targetAgentNames.has(agent.name)) continue;
    post(`/api/agents/${agent.id}/archive`, {}, token);
    archived.push({ id: agent.id, name: agent.name });
  }
  evidence.archived_non_target_agents = archived;
}

async function snapshotAgent(agentID) {
  if (agentSnapshots.has(agentID)) return;
  const snapshot = await loadAgentSnapshot(agentID);
  if (snapshot) agentSnapshots.set(agentID, snapshot);
}

async function loadAgentSnapshot(agentID) {
  const databaseURL = readDatabaseURL();
  if (!databaseURL) return null;
  const client = new pg.Client({ connectionString: databaseURL });
  await client.connect();
  try {
    const result = await client.query(`
      SELECT id::text, name, description, instructions, runtime_id::text, runtime_config::text,
             visibility, max_concurrent_tasks, custom_args::text, mcp_config::text,
             model, thinking_level
      FROM agent
      WHERE id = $1 AND workspace_id = $2
    `, [agentID, workspaceID]);
    return result.rows[0] || null;
  } finally {
    await client.end();
  }
}

async function restoreAgentSnapshots() {
  const restored = [];
  const failures = [];
  for (const snapshot of agentSnapshots.values()) {
    try {
      const body = {
        description: snapshot.description || "",
        instructions: snapshot.instructions || "",
        runtime_id: snapshot.runtime_id,
        runtime_config: parseJSONDefault(snapshot.runtime_config, {}),
        visibility: snapshot.visibility || "workspace",
        max_concurrent_tasks: Number(snapshot.max_concurrent_tasks || 1),
        custom_args: parseJSONDefault(snapshot.custom_args, []),
        mcp_config: snapshot.mcp_config ? parseJSONDefault(snapshot.mcp_config, {}) : null,
        model: snapshot.model || "",
        thinking_level: snapshot.thinking_level || "",
      };
      put(`/api/agents/${snapshot.id}`, body, token);
      restored.push({ id: snapshot.id, name: snapshot.name });
    } catch (error) {
      failures.push({ id: snapshot.id, name: snapshot.name, error: error?.message || String(error) });
    }
  }
  if (restored.length > 0 || failures.length > 0) {
    evidence.agent_setting_restore = { restored, failures };
  }
}

function parseJSONDefault(raw, fallback) {
  if (raw === null || raw === undefined || raw === "") return fallback;
  try {
    return JSON.parse(raw);
  } catch {
    return fallback;
  }
}

async function waitForSOPRun(issueID, squadID) {
  return poll(async () => {
    const runs = get(`/api/issues/${issueID}/sop-runs`, token);
    const items = Array.isArray(runs?.items) ? runs.items : [];
    return items.find((run) => run.squad_id === squadID || String(run.profile_key || "").includes(sopProfileKey)) || null;
  }, 60_000, "等待 SOP run 创建");
}

async function waitForCompletedSOPRun(issueID) {
  return poll(async () => {
    const runs = get(`/api/issues/${issueID}/sop-runs`, token);
    const items = Array.isArray(runs?.items) ? runs.items : [];
    return items.find((run) => run.status === "已完成") || null;
  }, 60_000, "等待 SOP run 完成");
}

async function waitForTask(issueID, agentID, triggeredAt, stage) {
  const triggerTime = new Date(triggeredAt).getTime() - 2_000;
  return poll(async () => {
    const task = preferredTaskForAgent(issueID, agentID, triggerTime);
    if (!task) return null;
    return task;
  }, 90_000, `等待 ${stage.key} 任务入队`);
}

async function waitForTerminalTask(issueID, agentID, taskID, stage) {
  let consecutiveMissing = 0;
  return poll(async () => {
    const task = taskByID(issueID, agentID, taskID);
    if (!task || task.id !== taskID) {
      consecutiveMissing += 1;
      if (consecutiveMissing >= 3) fail(`${stage.key} 任务 ${taskID} 已从 live task-runs 消失，判定为证据自毁/清理缺口`);
      return null;
    }
    consecutiveMissing = 0;
    if (["queued", "dispatched", "running", "waiting_local_directory"].includes(task.status)) return null;
    return task;
  }, taskTimeoutMs, `等待 ${stage.key} 真实模型任务完成`);
}

async function waitForExistingStageTask(issueID, agentID, issueCreatedAt, stage) {
  const minCreatedAt = new Date(issueCreatedAt).getTime() - 2_000;
  if (stage.key === "pm") return waitForTask(issueID, agentID, issueCreatedAt, stage);
  const task = preferredTaskForAgent(issueID, agentID, minCreatedAt);
  if (task) return task;
  const started = Date.now();
  while (Date.now() - started < 20_000) {
    const next = preferredTaskForAgent(issueID, agentID, minCreatedAt);
    if (next) return next;
    await new Promise((resolve) => setTimeout(resolve, pollIntervalMs));
  }
  return null;
}

function preferredTaskForAgent(issueID, agentID, minCreatedAt) {
  const activeOrder = new Map([
    ["running", 0],
    ["dispatched", 1],
    ["queued", 2],
    ["waiting_local_directory", 3],
    ["completed", 4],
    ["failed", 5],
  ]);
  const tasks = listIssueTasks(issueID)
    .filter((task) => task.agent_id === agentID || task.assignee_id === agentID)
    .filter((task) => task.status !== "cancelled")
    .filter((task) => new Date(task.created_at || 0).getTime() >= minCreatedAt)
    .sort((a, b) => {
      const statusDelta = (activeOrder.get(a.status) ?? 99) - (activeOrder.get(b.status) ?? 99);
      if (statusDelta !== 0) return statusDelta;
      return new Date(a.created_at || 0).getTime() - new Date(b.created_at || 0).getTime();
    });
  return tasks[0] || null;
}

function taskByID(issueID, agentID, taskID) {
  return listIssueTasks(issueID)
    .find((task) => task.id === taskID && (task.agent_id === agentID || task.assignee_id === agentID)) || null;
}

function listIssueTasks(issueID) {
  const tasks = get(`/api/issues/${issueID}/task-runs`, token);
  return Array.isArray(tasks) ? tasks : tasks.items ?? [];
}

async function recordSOPEvent(runID, stage, eventType, status, task, eventEvidence, durationMs = null) {
  const body = {
    event_type: eventType,
    status,
    step_name: stage.label,
    role_key: stage.role_key,
    task_id: task.id,
    evidence: eventEvidence,
    reason: eventType === "步骤完成" ? "真实模型任务已完成并回读 task/messages/usage/trace" : "真实模型任务已入队或开始执行",
  };
  if (Number.isFinite(durationMs) && durationMs > 0) body.duration_ms = Math.round(durationMs);
  return post(`/api/sop-runs/${runID}/steps/${stage.key}/events`, body, token);
}

async function buildStageEvidence(issueID, task, agent, stage) {
  const messages = task.id ? get(`/api/tasks/${task.id}/messages`, token) : [];
  const messageItems = Array.isArray(messages) ? messages : messages.items ?? [];
  const usage = await loadTaskUsage(task.id);
  const trace = await loadTaskTrace(task.id);
  const startedAt = task.started_at || task.created_at || "";
  const completedAt = task.completed_at || "";
  const durationMs = Math.max(0, new Date(completedAt || 0).getTime() - new Date(startedAt || 0).getTime());
  const usageRows = usage.map((row) => ({
    provider: row.provider,
    model: row.model,
    input_tokens: Number(row.input_tokens || 0),
    output_tokens: Number(row.output_tokens || 0),
    cache_read_tokens: Number(row.cache_read_tokens || 0),
    cache_write_tokens: Number(row.cache_write_tokens || 0),
  }));
  const tokenTotal = usageRows.reduce((sum, row) =>
    sum + row.input_tokens + row.output_tokens + row.cache_read_tokens + row.cache_write_tokens, 0);
  const traceRows = trace.map((row) => ({
    id: row.id,
    event_type: row.event_type,
    event_name: row.event_name,
    status: row.status,
    provider: row.provider,
    model: row.model,
    duration_ms: row.duration_ms,
    failure_reason: row.failure_reason,
  }));
  const observedModel = usageRows.find((row) => row.model)?.model || traceRows.find((row) => row.model)?.model || agent.model || model;
  const modelExecution = task.status === "completed" &&
    durationMs > 0 &&
    messageItems.length > 0 &&
    tokenTotal > 0 &&
    traceRows.length > 0 &&
    JSON.stringify([...usageRows, ...traceRows]).includes(observedModel);
  return {
    key: stage.key,
    role_key: stage.role_key,
    label: stage.label,
    agent_id: agent.id,
    agent_name: agent.name,
    task_id: task.id,
    status: task.status,
    model: observedModel,
    model_execution: modelExecution,
    started_at: startedAt,
    completed_at: completedAt,
    duration_ms: durationMs,
    message_count: messageItems.length,
    turn_count: messageItems.length,
    usage_rows: usageRows,
    token_total: tokenTotal,
    trace_event_count: traceRows.length,
    trace_events: traceRows.slice(0, 20),
    error: task.error || "",
    failure_reason: task.failure_reason || "",
    issue_id: issueID,
  };
}

async function loadTaskUsage(taskID) {
  const databaseURL = readDatabaseURL();
  if (!databaseURL) fail("DATABASE_URL 不可用，无法按 task_id 严格核验 usage");
  const client = new pg.Client({ connectionString: databaseURL });
  await client.connect();
  try {
    const result = await client.query(`
      SELECT provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
      FROM task_usage
      WHERE task_id = $1
      ORDER BY created_at ASC
    `, [taskID]);
    return result.rows;
  } finally {
    await client.end();
  }
}

async function loadTaskTrace(taskID) {
  const databaseURL = readDatabaseURL();
  if (!databaseURL) fail("DATABASE_URL 不可用，无法按 task_id 严格核验 trace");
  const client = new pg.Client({ connectionString: databaseURL });
  await client.connect();
  try {
    const result = await client.query(`
      SELECT id::text, event_type, event_name, status, provider, model, duration_ms, failure_reason
      FROM task_trace_event
      WHERE task_id = $1
      ORDER BY created_at ASC
    `, [taskID]);
    return result.rows;
  } finally {
    await client.end();
  }
}

async function verifyNoUnexpectedIssues(allowedIssueID, issueCreatedAt, label) {
  const databaseURL = readDatabaseURL();
  if (!databaseURL) fail("DATABASE_URL 不可用，无法核验运行期间是否创建了非预期 issue");
  const client = new pg.Client({ connectionString: databaseURL });
  await client.connect();
  try {
    const since = new Date(Math.max(0, new Date(issueCreatedAt).getTime() - 2_000)).toISOString();
    const result = await client.query(`
      SELECT i.id::text,
             concat(w.issue_prefix, '-', i.number::text) AS identifier,
             i.title,
             i.status,
             i.created_at
      FROM issue i
      JOIN workspace w ON w.id = i.workspace_id
      WHERE i.workspace_id = $1
        AND i.created_at >= $2
        AND i.id <> $3
      ORDER BY i.created_at ASC
    `, [workspaceID, since, allowedIssueID]);
    if (result.rowCount > 0) {
      evidence.unexpected_issue_mutation = { label, rows: result.rows };
      fail(`${label} 检测到非预期 issue 创建：${result.rows.map((row) => `${row.identifier || row.id}:${row.title}`).join("; ")}`);
    }
  } finally {
    await client.end();
  }
}

function verifyFinalEvidence(sopRun, stages) {
  const gaps = [];
  const byKey = new Map(stages.map((stage) => [stage.key, stage]));
  for (const expected of requiredStages) {
    const stage = byKey.get(expected.key);
    if (!stage) {
      gaps.push(`${expected.key} missing`);
      continue;
    }
    if (stage.status !== "completed") gaps.push(`${expected.key} status=${stage.status}`);
    if (!stage.model_execution) gaps.push(`${expected.key} model_execution=false`);
    if (!stage.task_id) gaps.push(`${expected.key} missing task_id`);
    if (!stage.started_at || !stage.completed_at || stage.duration_ms <= 0) gaps.push(`${expected.key} missing duration`);
    if (stage.message_count <= 0 || stage.turn_count <= 0) gaps.push(`${expected.key} missing messages/turns`);
    if (stage.token_total <= 0) gaps.push(`${expected.key} missing token usage`);
    if (stage.trace_event_count <= 0) gaps.push(`${expected.key} missing trace`);
  }
  const metrics = Array.isArray(sopRun?.metrics?.["阶段指标"]) ? sopRun.metrics["阶段指标"] : [];
  for (const expected of requiredStages) {
    const metric = metrics.find((item) => item.step_key === expected.key);
    if (!metric) {
      gaps.push(`${expected.key} missing SOP metric`);
      continue;
    }
    if (metric.status !== "已完成") gaps.push(`${expected.key} SOP status=${metric.status}`);
    if (Number(metric.task_count || 0) <= 0) gaps.push(`${expected.key} SOP task_count=0`);
    if (Number(metric.message_count || 0) <= 0) gaps.push(`${expected.key} SOP message_count=0`);
    if (Number(metric.agent_turn_count || 0) <= 0) gaps.push(`${expected.key} SOP agent_turn_count=0`);
    const metricTokens = Number(metric.input_tokens || 0) + Number(metric.output_tokens || 0) +
      Number(metric.cache_read_tokens || 0) + Number(metric.cache_write_tokens || 0);
    if (metricTokens <= 0) gaps.push(`${expected.key} SOP token_total=0`);
  }
  return {
    ok: gaps.length === 0,
    gaps,
    required_stage_count: requiredStages.length,
    observed_stage_count: stages.length,
    sop_metric_stage_count: metrics.length,
  };
}

function verifyLiveObservability(issueCreatedAt) {
  const since = new Date(Math.max(0, new Date(issueCreatedAt).getTime() - 60_000)).toISOString();
  const summary = get(`/api/workspaces/${workspaceID}/observability/summary?since=${encodeURIComponent(since)}`, token);
  const rows = Array.isArray(summary?.sop_stage_breakdown) ? summary.sop_stage_breakdown : [];
  const byKey = new Map(rows.map((row) => [row.step_key, row]));
  const gaps = [];
  for (const expected of requiredStages) {
    const row = byKey.get(expected.key);
    if (!row) {
      gaps.push(`${expected.key} missing live observability stage`);
      continue;
    }
    if (Number(row.task_count || 0) <= 0) gaps.push(`${expected.key} live task_count=0`);
    if (Number(row.message_count || 0) <= 0) gaps.push(`${expected.key} live message_count=0`);
    const tokens = Number(row.input_tokens || 0) + Number(row.output_tokens || 0) +
      Number(row.cache_read_tokens || 0) + Number(row.cache_write_tokens || 0);
    if (tokens <= 0) gaps.push(`${expected.key} live token_total=0`);
  }
  return {
    ok: gaps.length === 0,
    gaps,
    since,
    observed_stage_keys: rows.map((row) => row.step_key),
    sop_run_sample_total: Number(summary?.sop_run_sample_total || 0),
    sop_event_count: Number(summary?.["指标"]?.["SOP 事件数"] || 0),
  };
}

async function poll(fn, timeoutMs, label) {
  const started = Date.now();
  let last = null;
  while (Date.now() - started < timeoutMs) {
    last = await fn();
    if (last) return last;
    await new Promise((resolve) => setTimeout(resolve, pollIntervalMs));
  }
  fail(`${label}超时，最后结果：${JSON.stringify(last)}`);
}

function get(pathname, authToken) {
  return request("GET", pathname, null, authToken);
}

function post(pathname, body, authToken) {
  return request("POST", pathname, body, authToken);
}

function put(pathname, body, authToken) {
  return request("PUT", pathname, body, authToken);
}

function patch(pathname, body, authToken) {
  return request("PATCH", pathname, body, authToken);
}

function request(method, pathname, body, authToken) {
  const url = `${apiURL}${pathname}`;
  const args = ["--noproxy", "*", "-sS", "-w", "\n%{http_code}", "-X", method, url, "-H", "content-type: application/json"];
  if (authToken) args.push("-H", `Authorization: Bearer ${authToken}`);
  if (authToken && workspaceID) args.push("-H", `X-Workspace-ID: ${workspaceID}`);
  if (body !== null && body !== undefined) args.push("--data", JSON.stringify(body));
  evidence.commands.push(`curl ${args.map(redactArg).map(shellQuote).join(" ")}`);
  const out = execFileSync("curl", args, { encoding: "utf8", maxBuffer: 16 * 1024 * 1024 });
  const splitAt = out.lastIndexOf("\n");
  const responseBody = splitAt >= 0 ? out.slice(0, splitAt) : out;
  const status = Number(splitAt >= 0 ? out.slice(splitAt + 1).trim() : 0);
  if (status < 200 || status >= 300) {
    fail(`${method} ${pathname} 返回 ${status}: ${responseBody}`);
  }
  return responseBody.trim() ? JSON.parse(responseBody) : null;
}

function readDatabaseURL() {
  if (trimEnv("DATABASE_URL")) return trimEnv("DATABASE_URL");
  const envFile = path.join(repoRoot, ".run", "env", "goal-test-int.env");
  if (!existsSync(envFile)) return "";
  for (const line of readFileSync(envFile, "utf8").split(/\r?\n/)) {
    const match = line.match(/^DATABASE_URL=(.*)$/);
    if (match) return match[1].trim();
  }
  return "";
}

function pickRuntime(runtime) {
  return { id: runtime.id, name: runtime.name, provider: runtime.provider, status: runtime.status, last_seen_at: runtime.last_seen_at };
}

function isExternalDependencyText(text) {
  return /401|Unauthorized|Missing bearer|auth|authentication|invalid_request|not supported with|image_generation|agent_error\.provider_auth_or_access|额度|容量|quota|capacity|rate.?limit|agent_error\.provider_capacity_or_rate_limit/i.test(String(text || ""));
}

function fail(message) {
  throw new Error(message);
}

async function writeEvidenceAndExit(code) {
  if (token && workspaceID) {
    await restoreAgentSnapshots();
  }
  writeEvidenceSnapshot("final");
  console.log(JSON.stringify(evidence, null, 2));
  process.exit(code);
}

function writeEvidenceSnapshot(reason) {
  const stamp = new Date().toISOString().replace(/[-:]/g, "").replace(/\.\d+Z$/, "Z");
  if (!evidence.evidence_path) {
    evidence.evidence_path = path.join(artifactRoot, `goal-e-real-pm-0105-run-${stamp}.json`);
  }
  evidence.latest_evidence_path = latestPath;
  evidence.last_snapshot_reason = reason;
  evidence.last_snapshot_at = new Date().toISOString();
  const content = `${JSON.stringify(evidence, null, 2)}\n`;
  writeFileSync(evidence.evidence_path, content);
  writeFileSync(latestPath, content);
}

function trimEnv(name) {
  return (process.env[name] || "").trim();
}

function shellQuote(raw) {
  if (/^[A-Za-z0-9_./:=?&%-]+$/.test(raw)) return raw;
  return `'${raw.replace(/'/g, "'\\''")}'`;
}

function redactArg(raw) {
  return String(raw).replace(/Bearer\s+[A-Za-z0-9._-]+/g, "Bearer <redacted>");
}
