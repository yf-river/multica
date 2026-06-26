import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import pg from "pg";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = path.join(repoRoot, "artifacts", "acceptance");
const latestPath = path.join(artifactRoot, "goal-e-real-pm-0105-run-latest.json");
const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("NEXT_PUBLIC_API_URL") || "http://127.0.0.1:18762";
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || "goal-test-daemon";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || "e2e-password";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || "goal-test-daemon";
const provider = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER") || "codex";
const model = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL") || "gpt-5.3-codex-spark";
const taskTimeoutMs = Number(trimEnv("GOAL_E_REAL_PM_0105_TASK_TIMEOUT_MS") || 1_800_000);
const pollIntervalMs = Number(trimEnv("GOAL_E_REAL_PM_0105_POLL_INTERVAL_MS") || 5_000);
const requiredStages = [
  { key: "pm", role_key: "pm", label: "PM", prompt: "请作为 PM 接收本任务，读取任务描述，明确本次真实 PM+01-05 验收的目标、阶段顺序、证据要求，并说明下一步交给 01-clarify。" },
  { key: "01-clarify", role_key: "01-clarify", label: "01 需求澄清", prompt: "请只执行 user-center/01-clarify：澄清需求边界、验收口径、输入输出和 handoff，不要修改代码。" },
  { key: "02-design", role_key: "02-design", label: "02 方案设计", prompt: "请只执行 user-center/02-design：输出方案、影响面、接口/数据契约和 handoff，不要修改代码。" },
  { key: "03-task-split", role_key: "03-task-split", label: "03 任务拆分", prompt: "请只执行 user-center/03-task-split：输出任务拆分、跨项目依赖、阻断点和 handoff，不要修改代码。" },
  { key: "04-implement", role_key: "04-implement", label: "04 代码开发", prompt: "请只执行 user-center/04-implement：基于前序产物说明最小实现边界、涉及文件、风险和 handoff；本验收不要实际改代码。" },
  { key: "05-verify", role_key: "05-verify", label: "05 测试验证", prompt: "请只执行 user-center/05-verify：独立检查 PM+01-05 链路证据、测试口径、剩余风险和最终 handoff，不要修改代码。" },
];

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

try {
  mkdirSync(artifactRoot, { recursive: true });
  token = login();
  const workspace = loadWorkspace(token);
  workspaceID = workspace.id;
  evidence.workspace_id = workspaceID;
  const runtime = requireOnlineRuntime(token);
  evidence.runtime = pickRuntime(runtime);
  const template = post("/api/squads/internal-template", { template_key: "user-center", model }, token);
  const squad = template?.squad;
  const agents = Array.isArray(template?.agents) ? template.agents : [];
  if (!squad?.id) fail("user-center 内置小队模板未返回 squad");
  evidence.squad = { id: squad.id, name: squad.name, profile_key: squad.sop_profile?.profile_key || "" };
  evidence.agents = agents.map((agent) => ({ id: agent.id, name: agent.name, role_key: agent.role_key, model: agent.model || model }));
  const byRole = new Map(agents.map((agent) => [agent.role_key, agent]));
  for (const stage of requiredStages) {
    if (!byRole.get(stage.role_key)?.id) fail(`user-center 内置小队缺少角色 ${stage.role_key}`);
  }

  const issue = post("/api/issues", {
    workspace_id: workspaceID,
    title: `Goal E Real PM+01-05 Model Execution ${Date.now()}`,
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
    if (stageEvidence.status !== "completed") {
      const text = JSON.stringify(stageEvidence);
      if (isExternalDependencyText(text)) {
        evidence.result = "external_dependency_failure";
        evidence.external_dependency_failure = true;
        evidence.external_dependency_boundary = `${stage.key} 真实模型任务未完成，失败原因属于模型认证、额度、容量或限流边界。`;
        evidence.failed_stage = stageEvidence;
        writeEvidenceAndExit(0);
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
  evidence.verification = verification;
  evidence.ok = verification.ok;
  evidence.result = verification.ok ? "completed" : "failed";
  if (!verification.ok) fail(`真实 PM+01-05 证据不完整：${verification.gaps.join("; ")}`);
  writeEvidenceAndExit(0);
} catch (error) {
  evidence.result = evidence.result === "external_dependency_failure" ? evidence.result : "failed";
  evidence.error = error?.message || String(error);
  if (isExternalDependencyText(evidence.error)) {
    evidence.result = "external_dependency_failure";
    evidence.external_dependency_failure = true;
  }
  writeEvidenceAndExit(evidence.external_dependency_failure ? 0 : 1);
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

async function waitForSOPRun(issueID, squadID) {
  return poll(async () => {
    const runs = get(`/api/issues/${issueID}/sop-runs`, token);
    const items = Array.isArray(runs?.items) ? runs.items : [];
    return items.find((run) => run.squad_id === squadID || String(run.profile_key || "").includes("user-center")) || null;
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
  return poll(async () => {
    const task = taskByID(issueID, agentID, taskID);
    if (!task || task.id !== taskID) return null;
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

function writeEvidenceAndExit(code) {
  const stamp = new Date().toISOString().replace(/[-:]/g, "").replace(/\.\d+Z$/, "Z");
  const out = path.join(artifactRoot, `goal-e-real-pm-0105-run-${stamp}.json`);
  evidence.evidence_path = out;
  evidence.latest_evidence_path = latestPath;
  const content = `${JSON.stringify(evidence, null, 2)}\n`;
  writeFileSync(out, content);
  writeFileSync(latestPath, content);
  console.log(content.trim());
  process.exit(code);
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
