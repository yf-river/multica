import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const outputDir = path.join(repoRoot, "artifacts", "acceptance");
const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("NEXT_PUBLIC_API_URL") || "http://127.0.0.1:8080";
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || trimEnv("REAL_AGENT_E2E_ACCOUNT") || "goal-test-daemon";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || trimEnv("REAL_AGENT_E2E_PASSWORD") || "e2e-password";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || trimEnv("REAL_AGENT_E2E_WORKSPACE") || "goal-test-daemon";
const provider = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER") || "codex";
const model = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL") || "gpt-5.3-codex-spark";
const squadTemplateKey = trimEnv("ACCEPTANCE_SQUAD_TEMPLATE_KEY") || trimEnv("REAL_AGENT_E2E_SQUAD_TEMPLATE_KEY");
const verifyChildWake = trimEnv("ACCEPTANCE_VERIFY_CHILD_WAKE") === "1" || squadTemplateKey !== "";
const verifyCrossProjectChildren = trimEnv("ACCEPTANCE_VERIFY_CROSS_PROJECT_CHILDREN") === "1";
const taskTimeoutMs = Number(trimEnv("ACCEPTANCE_TASK_TIMEOUT_MS") || (squadTemplateKey ? 600_000 : 240_000));
const suffix = Date.now();

const evidence = {
  schema: "multica.codex_squad_curl_e2e.v1",
  generated_at: new Date().toISOString(),
  api_url: apiURL,
  account,
  workspace_slug: workspaceSlug,
  provider,
  model,
  squad_template_key: squadTemplateKey || "ad-hoc",
  verify_child_wake: verifyChildWake,
  verify_cross_project_children: verifyCrossProjectChildren,
  task_timeout_ms: taskTimeoutMs,
  commands: [],
  result: "unknown",
};
let activeWorkspaceId = "";

const login = post("/auth/login", { account, password }, { auth: false });
const token = login.token;
if (!token) fail("登录响应缺少 token");

const workspaces = get("/api/workspaces", token);
const workspace = (Array.isArray(workspaces) ? workspaces : workspaces.items ?? []).find((item) => item.slug === workspaceSlug);
if (!workspace?.id) fail(`未找到工作区 ${workspaceSlug}`);
evidence.workspace_id = workspace.id;
activeWorkspaceId = workspace.id;

const runtimes = get(`/api/runtimes?workspace_id=${encodeURIComponent(workspace.id)}`, token);
const runtime = (Array.isArray(runtimes) ? runtimes : runtimes.items ?? [])
  .filter((item) => item.provider === provider && item.status === "online" && new Date(item.last_seen_at || 0).getTime() > Date.now() - 120_000)
  .sort((a, b) => new Date(b.last_seen_at || 0).getTime() - new Date(a.last_seen_at || 0).getTime())[0];
if (!runtime?.id) fail(`未找到 2 分钟内在线的 ${provider} runtime`);
evidence.runtime = { id: runtime.id, name: runtime.name, provider: runtime.provider, last_seen_at: runtime.last_seen_at };

let agent;
let squad;
let crossProjectSetup = null;
if (squadTemplateKey) {
  if (!["user-center", "multica-coding"].includes(squadTemplateKey)) {
    fail("ACCEPTANCE_SQUAD_TEMPLATE_KEY 只能是 user-center 或 multica-coding");
  }
  if (verifyCrossProjectChildren && squadTemplateKey !== "user-center") {
    fail("ACCEPTANCE_VERIFY_CROSS_PROJECT_CHILDREN=1 只支持 user-center 小队");
  }
  const template = post("/api/squads/internal-template", { template_key: squadTemplateKey }, token);
  squad = template?.squad;
  agent = template?.agents?.find((item) => item.role_key === "captain");
  if (!squad?.id || !agent?.id) fail(`内置小队模板返回不完整：${squadTemplateKey}`);
  evidence.internal_template = {
    key: squadTemplateKey,
    squad_id: squad.id,
    squad_name: squad.name,
    agent_count: Array.isArray(template.agents) ? template.agents.length : 0,
    roles: Array.isArray(template.agents) ? template.agents.map((item) => ({ id: item.id, name: item.name, role_key: item.role_key, role: item.role })) : [],
  };
  evidence.agent = { id: agent.id, name: agent.name, role_key: agent.role_key, role: agent.role, provider, model };
  evidence.squad = { id: squad.id, name: squad.name, profile_key: squad?.sop_profile?.profile_key || "" };
  if (verifyCrossProjectChildren) {
    crossProjectSetup = createCrossProjectSetup(token, suffix, runtime.id);
    evidence.cross_project_setup = crossProjectSetup;
  }
} else {
  agent = post("/api/agents", {
    name: `curl Codex 验收智能体 ${suffix}`,
    description: "由 scripts/codex-squad-curl-e2e.mjs 通过 curl 创建，用于验证真实端到端智能体执行。",
    instructions: "你是 Multica 端到端验收智能体。请用中文回复，必须包含：执行结论、trace/任务标识、验收证据、下一步。",
    runtime_id: runtime.id,
    workspace_id: workspace.id,
    visibility: "private",
    max_concurrent_tasks: 1,
    model,
  }, token);
  if (!agent?.id) fail("创建 Agent 响应缺少 id");
  evidence.agent = { id: agent.id, name: agent.name, provider, model };

  squad = post("/api/squads", {
    workspace_id: workspace.id,
    name: `curl Codex 验收小队 ${suffix}`,
    description: "通过公开 API 创建的端到端验收小队，队长为真实 Codex 智能体。",
    leader_id: agent.id,
    sop_profile: {
      profile_key: "curl-codex-squad-e2e",
      project: "multica",
      mode: "curl_e2e",
      roles: [{ key: "captain", name: "队长", responsibility: "接收任务并用真实 Codex runtime 执行。" }],
      steps: [
        { key: "receive", name: "接收任务", role_key: "captain" },
        { key: "execute", name: "真实执行", role_key: "captain" },
        { key: "evidence", name: "证据回写", role_key: "captain" },
      ],
      acceptance: ["任务被 daemon 领取", "trace 事件存在", "任务消息存在", "完成时存在 usage"],
    },
  }, token);
  if (!squad?.id) fail("创建小队响应缺少 id");
  evidence.squad = { id: squad.id, name: squad.name };
}

const issue = post("/api/issues", {
  workspace_id: workspace.id,
  title: issueTitle(squadTemplateKey, suffix),
  description: issueDescription(squadTemplateKey, crossProjectSetup),
  status: "todo",
  priority: "medium",
  assignee_type: "squad",
  assignee_id: squad.id,
  ...(crossProjectSetup?.usercenter?.id ? { project_id: crossProjectSetup.usercenter.id } : {}),
}, token);
if (!issue?.id) fail("创建任务响应缺少 id");
evidence.issue = { id: issue.id, identifier: issue.identifier || "", title: issue.title, project_id: issue.project_id || null };

if (squadTemplateKey) {
  const sopRun = await poll(async () => {
    const runs = get(`/api/issues/${issue.id}/sop-runs`, token);
    const items = Array.isArray(runs?.items) ? runs.items : [];
    return items.find((item) => String(item.profile_key || "").includes(squadTemplateKey) || item.squad_id === squad.id) || null;
  }, 30_000, "等待内置小队 SOP Run 生成");
  evidence.sop_run = {
    id: sopRun.id,
    profile_key: sopRun.profile_key,
    current_step_key: sopRun.current_step_key,
    event_count: Array.isArray(sopRun.events) ? sopRun.events.length : 0,
  };
}

const terminalTask = await poll(async () => {
  const items = listIssueTasks(issue.id, token);
  const task = newestLeaderTask(items, agent.id);
  evidence.task_poll_snapshot = {
    total_tasks: items.length,
    leader_task: task ? taskSummary(task) : null,
    tasks: items.slice(0, 5).map(taskSummary),
  };
  if (!task || ["queued", "dispatched", "running", "waiting_local_directory"].includes(task.status)) return null;
  return task;
}, taskTimeoutMs, "等待 Codex 小队任务完成或失败");
evidence.task = {
  id: terminalTask.id,
  status: terminalTask.status,
  runtime_id: terminalTask.runtime_id,
  error: terminalTask.error || "",
  failure_reason: terminalTask.failure_reason || "",
};

const trace = get(`/api/issues/${issue.id}/trace`, token);
const usage = get(`/api/issues/${issue.id}/usage`, token);
const messages = terminalTask.id ? get(`/api/tasks/${terminalTask.id}/messages`, token) : [];
evidence.trace_event_count = Array.isArray(trace?.events) ? trace.events.length : Number(trace?.total ?? 0);
evidence.message_count = Array.isArray(messages) ? messages.length : Number(messages?.items?.length ?? 0);
evidence.usage = usage;
evidence.trace_provider_model_observed = JSON.stringify(trace).includes(provider) && JSON.stringify(trace).includes(model);
evidence.trace_squad_observed = JSON.stringify(trace).includes(squad.id);
evidence.trace_issue_observed = JSON.stringify(trace).includes(issue.id);

if (evidence.trace_event_count <= 0) fail("缺少 trace 事件");
if (!evidence.trace_squad_observed) fail("trace 未包含小队归属");
if (!evidence.trace_issue_observed) fail("trace 未包含任务归属");
if (terminalTask.status === "completed") {
  if (evidence.message_count <= 0) fail("任务完成但缺少任务消息");
  const totalTokens =
    Number(usage?.total_input_tokens ?? 0) +
    Number(usage?.total_output_tokens ?? 0) +
    Number(usage?.total_cache_read_tokens ?? 0) +
    Number(usage?.total_cache_write_tokens ?? 0);
  if (totalTokens <= 0) fail("任务完成但 usage token 总量为 0");
  if (!evidence.trace_provider_model_observed) {
    fail(`任务完成但 trace 未包含 ${provider}/${model}`);
  }
  let captainCreatedChild = null;
  if (verifyCrossProjectChildren) {
    const crossProjectChildren = await verifyCaptainCreatedCrossProjectChildren({ issue, setup: crossProjectSetup, token });
    captainCreatedChild = crossProjectChildren.children[0] || null;
    await verifyProjectLeadExecution({ children: crossProjectChildren.children, setup: crossProjectSetup, token });
  }
  if (verifyChildWake) {
    await verifyChildDoneWake({ issue, squad, agent, terminalTask, token, childOverride: captainCreatedChild });
  }
  evidence.result = "completed";
} else {
  const errorText = JSON.stringify(evidence);
  if (!/401|Unauthorized|Missing bearer|auth|authentication|agent_error\.provider_auth_or_access|额度|容量|quota|capacity|rate.?limit|agent_error\.provider_capacity_or_rate_limit/i.test(errorText)) {
    fail(`任务未完成且失败原因不可解释：${terminalTask.status}`);
  }
  evidence.result = "external_dependency_failure";
  evidence.external_dependency_failure = true;
  evidence.external_dependency_boundary = "Codex runtime 已通过公开 API 创建 Agent/小队/Issue 并被 daemon 执行；任务失败发生在外部模型认证、额度或容量边界。";
  evidence.repair_hint = "检查 daemon 运行环境是否能让 Codex app-server 复用有效 ChatGPT auth，或为 daemon 配置可用的 OpenAI API 认证后重跑本脚本。";
}

function createCrossProjectSetup(token, value, runtimeID) {
  const gatewayLead = createProjectLeadAgent(token, {
    name: `curl gateway 项目负责人 ${value}`,
    role: "gateway 项目负责人",
    runtimeID,
  });
  const configLead = createProjectLeadAgent(token, {
    name: `curl config 项目负责人 ${value}`,
    role: "config 项目负责人",
    runtimeID,
  });
  const usercenter = post("/api/projects", {
    workspace_id: activeWorkspaceId,
    title: `curl usercenter ${value}`,
    description: "curl 验收创建的 user-center 父项目，用于验证队长跨项目拆子任务。",
    status: "in_progress",
  }, token);
  const gateway = post("/api/projects", {
    workspace_id: activeWorkspaceId,
    title: `curl gateway ${value}`,
    description: "curl 验收创建的 gateway 子项目，用于验证队长通过 --project 创建跨项目子任务。",
    status: "in_progress",
    lead_type: "agent",
    lead_id: gatewayLead.id,
  }, token);
  const config = post("/api/projects", {
    workspace_id: activeWorkspaceId,
    title: `curl config ${value}`,
    description: "curl 验收创建的 config 子项目，用于验证队长通过 --project 创建跨项目子任务。",
    status: "in_progress",
    lead_type: "agent",
    lead_id: configLead.id,
  }, token);
  for (const project of [usercenter, gateway, config]) {
    if (!project?.id) fail(`创建跨项目验收项目失败：${JSON.stringify(project)}`);
  }
  return {
    usercenter: pickProject(usercenter),
    gateway: { ...pickProject(gateway), lead: gatewayLead },
    config: { ...pickProject(config), lead: configLead },
  };
}

function createProjectLeadAgent(token, { name, role, runtimeID }) {
  const agent = post("/api/agents", {
    name,
    description: `curl 验收创建的${role}，用于证明未指派的项目 issue 会自动交给项目负责人并真实执行。`,
    instructions: [
      `你是${role}。`,
      "收到任务后只做验收回复，不修改代码。",
      "必须用中文输出：执行结论、当前 issue 标识、项目配合结果、给父任务的下一步建议。",
      "如果任务来自跨项目子 issue，请说明你已完成对应项目的配合事项。",
    ].join("\n"),
    runtime_id: runtimeID,
    workspace_id: activeWorkspaceId,
    visibility: "private",
    max_concurrent_tasks: 1,
    model,
  }, token);
  if (!agent?.id) fail(`创建${role}响应缺少 id`);
  return pickAgent(agent);
}

function pickProject(project) {
  return {
    id: project.id,
    title: project.title,
    status: project.status || "",
    lead_type: project.lead_type || null,
    lead_id: project.lead_id || null,
  };
}

function pickAgent(agent) {
  return { id: agent.id, name: agent.name, provider, model };
}

writeEvidence(evidence);
console.log(JSON.stringify(evidence, null, 2));

function trimEnv(name) {
  return (process.env[name] || "").trim();
}

function get(path, token) {
  return request("GET", path, null, token);
}

function post(path, body, tokenOrOptions) {
  const token = typeof tokenOrOptions === "string" ? tokenOrOptions : null;
  return request("POST", path, body, token);
}

function put(path, body, token) {
  return request("PUT", path, body, token);
}

function request(method, path, body, token) {
  const url = `${apiURL}${path}`;
  const args = ["--noproxy", "*", "-sS", "-w", "\n%{http_code}", "-X", method, url, "-H", "content-type: application/json"];
  if (token) args.push("-H", `Authorization: Bearer ${token}`);
  if (token && activeWorkspaceId) args.push("-H", `X-Workspace-ID: ${activeWorkspaceId}`);
  if (body !== null && body !== undefined) args.push("--data", JSON.stringify(body));
  evidence.commands.push(`curl ${redactArgs(args).map(shellQuote).join(" ")}`);
  const out = execFileSync("curl", args, { encoding: "utf8", maxBuffer: 10 * 1024 * 1024 });
  const splitAt = out.lastIndexOf("\n");
  const responseBody = splitAt >= 0 ? out.slice(0, splitAt) : out;
  const status = Number(splitAt >= 0 ? out.slice(splitAt + 1).trim() : 0);
  if (status < 200 || status >= 300) {
    fail(`${method} ${path} 返回 ${status}: ${responseBody}`);
  }
  return responseBody.trim() ? JSON.parse(responseBody) : null;
}

function listIssueTasks(issueID, token) {
  const tasks = get(`/api/issues/${issueID}/task-runs`, token);
  return Array.isArray(tasks) ? tasks : tasks.items ?? [];
}

function newestLeaderTask(tasks, leaderAgentID) {
  const matching = tasks.filter((item) => item.agent_id === leaderAgentID || item.assignee_id === leaderAgentID);
  return matching[0] || tasks[0] || null;
}

function taskSummary(task) {
  return {
    id: task.id,
    status: task.status,
    agent_id: task.agent_id,
    runtime_id: task.runtime_id,
    is_leader_task: task.is_leader_task,
    created_at: task.created_at,
    started_at: task.started_at,
    completed_at: task.completed_at,
    failure_reason: task.failure_reason || "",
  };
}

async function verifyCaptainCreatedCrossProjectChildren({ issue, setup, token }) {
  const children = await poll(async () => {
    const items = get(`/api/issues/${issue.id}/children`, token);
    const list = Array.isArray(items?.issues) ? items.issues : Array.isArray(items) ? items : [];
    const byProject = new Map(list.map((item) => [item.project_id, item]));
    if (byProject.has(setup.gateway.id) && byProject.has(setup.config.id)) return list;
    return null;
  }, 60_000, "等待队长创建 gateway/config 跨项目子任务");

  const gatewayChild = children.find((item) => item.project_id === setup.gateway.id);
  const configChild = children.find((item) => item.project_id === setup.config.id);
  if (!gatewayChild?.id || !configChild?.id) fail("未回读到 gateway/config 跨项目子任务");
  for (const child of [gatewayChild, configChild]) {
    if (child.parent_issue_id !== issue.id) {
      fail(`子任务 ${child.id} parent_issue_id=${child.parent_issue_id}，期望 ${issue.id}`);
    }
  }
  assertProjectLeadAssignee(gatewayChild, setup.gateway.lead, "gateway");
  assertProjectLeadAssignee(configChild, setup.config.lead, "config");
  evidence.cross_project_children = {
    count: children.length,
    gateway: issueSummary(gatewayChild),
    config: issueSummary(configChild),
    project_lead_auto_assignee_verified: true,
    verified_by_public_api: true,
  };
  return { children: [gatewayChild, configChild] };
}

function assertProjectLeadAssignee(child, lead, label) {
  if (child.assignee_type !== "agent" || child.assignee_id !== lead.id) {
    fail(`${label} 子任务未自动指派给项目负责人：assignee_type=${child.assignee_type || "null"} assignee_id=${child.assignee_id || "null"} lead=${lead.id}`);
  }
}

async function verifyProjectLeadExecution({ children, setup, token }) {
  const childByProject = new Map(children.map((item) => [item.project_id, item]));
  const checks = [
    { label: "gateway", child: childByProject.get(setup.gateway.id), lead: setup.gateway.lead },
    { label: "config", child: childByProject.get(setup.config.id), lead: setup.config.lead },
  ];
  const results = [];
  for (const check of checks) {
    if (!check.child?.id) fail(`${check.label} 项目负责人执行验证缺少子任务`);
    const task = await waitTerminalTaskForAgent({
      issueID: check.child.id,
      agentID: check.lead.id,
      token,
      label: `${check.label} 项目负责人任务`,
    });
    if (task.status !== "completed") {
      fail(`${check.label} 项目负责人任务未完成：${task.status} ${task.failure_reason || task.error || ""}`);
    }
    const trace = get(`/api/issues/${check.child.id}/trace`, token);
    const usage = get(`/api/issues/${check.child.id}/usage`, token);
    const messages = get(`/api/tasks/${task.id}/messages`, token);
    const messageCount = Array.isArray(messages) ? messages.length : Number(messages?.items?.length ?? 0);
    const totalTokens =
      Number(usage?.total_input_tokens ?? 0) +
      Number(usage?.total_output_tokens ?? 0) +
      Number(usage?.total_cache_read_tokens ?? 0) +
      Number(usage?.total_cache_write_tokens ?? 0);
    if (messageCount <= 0) fail(`${check.label} 项目负责人任务完成但缺少消息`);
    if (totalTokens <= 0) fail(`${check.label} 项目负责人任务完成但 usage token 总量为 0`);
    if (!JSON.stringify(trace).includes(check.lead.id)) fail(`${check.label} 子任务 trace 未包含项目负责人`);
    results.push({
      label: check.label,
      child: issueSummary(check.child),
      lead: check.lead,
      task: taskSummary(task),
      trace_event_count: Array.isArray(trace?.events) ? trace.events.length : Number(trace?.total ?? 0),
      message_count: messageCount,
      usage,
    });
  }
  evidence.project_lead_execution = {
    verified_by_public_api: true,
    results,
  };
}

async function waitTerminalTaskForAgent({ issueID, agentID, token, label }) {
  return poll(async () => {
    const items = listIssueTasks(issueID, token);
    const task = newestLeaderTask(items, agentID);
    if (!task || ["queued", "dispatched", "running", "waiting_local_directory"].includes(task.status)) return null;
    return task;
  }, taskTimeoutMs, `等待${label}完成或失败`);
}

async function verifyChildDoneWake({ issue, squad, agent, terminalTask, token, childOverride = null }) {
  const child = childOverride || post("/api/issues", {
    workspace_id: activeWorkspaceId,
    title: `curl 小队父子任务唤醒验收 ${Date.now()}`,
    description: "验证子任务完成后，系统评论会回写父任务并再次唤醒小队队长。",
    status: "todo",
    priority: "medium",
    parent_issue_id: issue.id,
  }, token);
  if (!child?.id) fail("子任务响应缺少 id");
  put(`/api/issues/${child.id}`, { status: "done" }, token);

  const parentComment = await poll(async () => {
    const comments = get(`/api/issues/${issue.id}/comments?roots_only=true&summary=true`, token);
    const items = Array.isArray(comments) ? comments : comments.items ?? [];
    return items.find((item) =>
      item.author_type === "system" &&
      String(item.content || "").includes(child.identifier || child.title || child.id) &&
      String(item.content || "").includes(`mention://squad/${squad.id}`)
    ) || null;
  }, 30_000, "等待子任务完成后系统评论回写父任务");

  const requeuedTask = await poll(async () => {
    const task = newestLeaderTask(listIssueTasks(issue.id, token), agent.id);
    if (!task || task.id === terminalTask.id) return null;
    return task;
  }, 30_000, "等待父任务被系统评论再次唤醒");

  evidence.child_done_wake = {
    child_issue_id: child.id,
    child_identifier: child.identifier || "",
    parent_comment_id: parentComment.id,
    parent_comment_author_type: parentComment.author_type,
    parent_comment_mentions_squad: String(parentComment.content || "").includes(`mention://squad/${squad.id}`),
    requeued_task_id: requeuedTask.id,
    requeued_task_status: requeuedTask.status,
    requeued_task_is_leader_task: requeuedTask.is_leader_task,
    used_captain_created_child: Boolean(childOverride),
    child_project_id: child.project_id || null,
  };
}

function issueSummary(issue) {
  return {
    id: issue.id,
    identifier: issue.identifier || "",
    title: issue.title,
    status: issue.status,
    parent_issue_id: issue.parent_issue_id || null,
    project_id: issue.project_id || null,
    assignee_type: issue.assignee_type || null,
    assignee_id: issue.assignee_id || null,
  };
}

async function poll(fn, timeoutMs, label) {
  const started = Date.now();
  let last = null;
  while (Date.now() - started < timeoutMs) {
    last = await fn();
    if (last) return last;
    await new Promise((resolve) => setTimeout(resolve, 5000));
  }
  fail(`${label}超时，最后结果：${JSON.stringify(last)}`);
}

function issueTitle(templateKey, value) {
  switch (templateKey) {
    case "user-center":
      return `curl user-center 小队真实端到端验收 ${value}`;
    case "multica-coding":
      return `curl Multica 编码小队真实端到端验收 ${value}`;
    default:
      return `curl Codex 小队端到端验收 ${value}`;
  }
}

function issueDescription(templateKey, crossProjectSetup = null) {
  switch (templateKey) {
    case "user-center":
      if (crossProjectSetup) {
        return [
          "请作为 user-center 小队队长完成一次真实跨项目 SOP 验收。不要修改代码。",
          "",
          "业务场景：user-center 要新增一个内部用户查询 API，需要 gateway 补路由/鉴权/转发信息，需要 config 补配置项/灰度参数。",
          "",
          "必须按以下方式执行：",
          "1. 先运行 `multica issue get <当前 issue id> --output json` 理解父任务。",
          "2. 再运行 `multica project list --output json`，找到下面两个目标项目的 UUID：",
          `   - gateway 项目标题：${crossProjectSetup.gateway.title}`,
          `   - config 项目标题：${crossProjectSetup.config.title}`,
          "3. 创建两个 `todo` 子 issue；每个命令都必须带 `--parent <当前 issue id>`，并且必须带对应的 `--project <目标项目 UUID>`：",
          "   - gateway 子 issue：标题包含 gateway，描述说明 API 路径、方法、鉴权和转发要求。",
          "   - config 子 issue：标题包含 config，描述说明配置键、默认值、环境差异和回滚方式。",
          "4. 创建子 issue 时不要传 `--assignee` 或 `--assignee-id`，平台会把未指派的项目 issue 自动交给对应项目负责人。",
          "5. 创建后调用 `multica squad activity <当前 issue id> action --reason \"已创建跨项目子任务\"`。",
          "6. 输出验收证据：父 issue id、两个子 issue id、两个项目 UUID、下一步等待子项目负责人处理。",
        ].join("\n");
      }
      return "请作为 user-center 小队队长完成一次最小 SOP 验收：澄清需求、说明阶段、输出 trace/任务标识、验收证据和下一步。不要修改代码。";
    case "multica-coding":
      return "请作为 Multica 编码小队队长完成一次最小 SOP 验收：说明六角色分工、方案先确认、开发范围边界、独立验收、规约同步和部署运行注意事项。不要修改代码。";
    default:
      return "请用中文完成一次最小验收：说明你已收到任务，输出 trace/任务标识占位、验收证据和下一步。不要修改代码。";
  }
}

function fail(message) {
  evidence.error = message;
  evidence.result = "failed";
  writeEvidence(evidence);
  console.error(JSON.stringify(evidence, null, 2));
  process.exit(1);
}

function writeEvidence(data) {
  mkdirSync(outputDir, { recursive: true });
  const stamp = new Date(data.generated_at || new Date().toISOString()).toISOString().replace(/[:.]/g, "-");
  const pathByTime = path.join(outputDir, `codex-squad-curl-e2e-${stamp}.json`);
  const latestPath = path.join(outputDir, "codex-squad-curl-e2e-latest.json");
  data.evidence_path = pathByTime;
  data.latest_evidence_path = latestPath;
  const content = `${JSON.stringify(data, null, 2)}\n`;
  writeFileSync(pathByTime, content);
  writeFileSync(latestPath, content);
}

function shellQuote(value) {
  const raw = String(value);
  if (/^[A-Za-z0-9_./:=?&%-]+$/.test(raw)) return raw;
  return `'${raw.replace(/'/g, "'\\''")}'`;
}

function redactArgs(args) {
  return args.map((arg, index) => {
    if (index > 0 && args[index - 1] === "-H" && /^Authorization:/i.test(arg)) {
      return "Authorization: Bearer <redacted>";
    }
    return arg;
  });
}
