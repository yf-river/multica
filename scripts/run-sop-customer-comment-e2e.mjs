#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const outputDir = path.join(repoRoot, "artifacts", "acceptance");
const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("GOAL_TEST_INT_API_URL") || "http://127.0.0.1:18762";
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || "goal-test-daemon";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || "e2e-password";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || "goal-test-daemon";
const provider = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER") || "codex";
const model = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL") || "gpt-5.3-codex-spark";
const repoRef = trimEnv("ACCEPTANCE_REPO_REF") || "v5.0.0_dev_sop";
const taskTimeoutMs = Number(trimEnv("ACCEPTANCE_TASK_TIMEOUT_MS") || 2_700_000);
const pollIntervalMs = Number(trimEnv("ACCEPTANCE_POLL_INTERVAL_MS") || 5_000);
const runSandbox = trimEnv("ACCEPTANCE_RUN_SERVICE_SANDBOX") !== "0";
const suffix = Date.now();

const repoSpecs = [
  {
    key: "usercenter",
    title: "usercenter",
    projectPath: "ChainWeaver/ida/user-center",
    url: `https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/${repoRef}`,
  },
  {
    key: "gateway",
    title: "gateway",
    projectPath: "ChainWeaver/ida/gateway",
    url: `https://git.code.tencent.com/ChainWeaver/ida/gateway/commits/${repoRef}`,
  },
  {
    key: "ida-deployment",
    title: "ida-deployment",
    projectPath: "ChainWeaver/ida/ida-deployment",
    url: `https://git.code.tencent.com/ChainWeaver/ida/ida-deployment/commits/${repoRef}`,
  },
];

const evidence = {
  schema: "multica.sop_customer_comment_e2e.v1",
  generated_at: new Date().toISOString(),
  api_url: apiURL,
  account,
  workspace_slug: workspaceSlug,
  provider,
  model,
  repo_ref: repoRef,
  release_commit: gitText(["rev-parse", "--short=12", "HEAD"]),
  branch: gitText(["branch", "--show-current"]),
  commands: [],
  comments: [],
  task_rounds: [],
  result: "unknown",
};

let activeToken = "";
let activeWorkspaceId = "";
let activeIssueId = "";

try {
  const login = await post("/auth/login", { account, password }, { auth: false });
  const token = login.token;
  if (!token) fail("登录响应缺少 token");
  activeToken = token;

  const currentUser = login.user?.id ? login.user : await get("/api/me", token);
  if (!currentUser?.id) fail("无法解析当前用户");
  evidence.current_user = pickUser(currentUser);

  const workspace = await resolveWorkspace(token);
  activeWorkspaceId = workspace.id;
  evidence.workspace = pickWorkspace(workspace);

  const runtime = await resolveOnlineRuntime(token, workspace.id);
  evidence.runtime = pickRuntime(runtime);

  const template = await post("/api/squads/internal-template", { template_key: "user-center", model }, token);
  const squad = template?.squad;
  const agents = Array.isArray(template?.agents) ? template.agents : [];
  const pm = agents.find((item) => item.role_key === "pm" || item.name === "pm") || agents[0];
  if (!squad?.id || !pm?.id) fail("user-center 内置小队模板缺少 squad 或 pm agent");
  evidence.squad = { id: squad.id, name: squad.name, profile_key: squad?.sop_profile?.profile_key || "" };
  evidence.agents = agents.map((item) => ({
    id: item.id,
    name: item.name,
    role_key: item.role_key,
    runtime_id: item.runtime_id,
    model: item.model,
  }));

  const projects = await ensureCanonicalProjectsAndRepos(token, workspace, currentUser, squad);
  evidence.projects = Object.fromEntries(Object.entries(projects).map(([key, project]) => [key, pickProject(project)]));

  const issue = await post("/api/issues", {
    workspace_id: workspace.id,
    title: `客户评论式 SOP 验收：新增 usercenter API ${suffix}`,
    description: [
      "这是一条客户视角验收任务。",
      "",
      "背景：usercenter 需要新增一个对外 API，最终要能通过 gateway HTTP 入口访问，并由 ida-deployment 提供 apiData/权限/渲染配置。",
      "",
      "请先只确认收到并等待我后续评论推进；不要一次性创建子任务，不要跳到最终验收。",
    ].join("\n"),
    status: "todo",
    priority: "high",
    assignee_type: "squad",
    assignee_id: squad.id,
    project_id: projects.usercenter.id,
    allow_duplicate: true,
  }, token);
  if (!issue?.id) fail("创建父任务响应缺少 id");
  activeIssueId = issue.id;
  evidence.issue = pickIssue(issue);

  await waitForSOPRun(issue.id, squad.id, token, "等待父任务 SOP Run 生成");
  const initialTask = await waitNextTerminalTask(issue.id, pm.id, new Set(), token, "等待父任务初始接收任务完成");
  requireCompletedTask(initialTask, "父任务初始接收任务");

  await postCustomerComment(issue.id, token, [
    "客户补充 1：请按真正 SOP 方式先做澄清。",
    "",
    "请读取当前 issue、项目和资源库上下文，判断新增 usercenter API 是否会产生 gateway、ida-deployment 依赖。",
    "这一轮只做澄清和依赖判断，不要改代码，不要创建同项目阶段子任务。",
  ].join("\n"));
  const clarifyTask = await waitNextTerminalTask(issue.id, pm.id, new Set([initialTask.id]), token, "等待评论 1 触发的澄清任务完成");
  requireCompletedTask(clarifyTask, "评论 1 澄清任务");

  await postCustomerComment(issue.id, token, [
    "客户确认 2：这个 API 需要真实端到端联调。",
    "",
    "请创建跨项目 child issue：",
    `- gateway：目标项目名 gateway，project_id=${projects.gateway.id}，目标小队 id=${squad.id}，负责路由/影响检查/转发验证。`,
    `- ida-deployment：目标项目名 ida-deployment，project_id=${projects["ida-deployment"].id}，目标小队 id=${squad.id}，负责 apiData、权限和 Helm render 验证。`,
    "",
    "要求：",
    "- child issue 必须挂到当前父任务。",
    "- child issue 初始状态为 backlog。",
    "- child issue 不要代表 01-05 阶段，只代表跨项目交付物。",
    "- 创建后请回读 children，确认 parent、project、assignee 正确。",
  ].join("\n"));
  const childTask = await waitNextTerminalTask(issue.id, pm.id, new Set([initialTask.id, clarifyTask.id]), token, "等待评论 2 触发的跨项目子任务创建完成");
  requireCompletedTask(childTask, "评论 2 跨项目子任务创建任务");

  const children = await verifyCrossProjectChildren(issue, projects, squad, token);
  await approveAndWaitChildren(children, projects, token);

  const sandboxEvidence = runSandbox ? await runHistoricalServiceSandbox() : { skipped: true };
  evidence.service_sandbox = sandboxEvidence;
  if (runSandbox && !sandboxEvidence.ok) fail("历史 service sandbox curl 验收失败");

  await completeChildren(children, token);

  const wake = await verifyParentWakeAfterChildrenDone(issue, squad, pm, new Set([initialTask.id, clarifyTask.id, childTask.id]), token);
  evidence.child_done_wake = wake;

  await postCustomerComment(issue.id, token, [
    "客户补充 3：所有跨项目子任务已经完成，请继续父任务最终 verify。",
    "",
    "验收要求：",
    "- 读取 gateway 和 ida-deployment 子任务 closure。",
    "- 核对父子任务关系、trace、usage、运行复盘数据。",
    "- 核对 service sandbox curl 验收证据。",
    "- 如果满足验收，请把父任务状态更新为 done。",
  ].join("\n"));
  const verifyTask = await waitNextTerminalTask(issue.id, pm.id, new Set([initialTask.id, clarifyTask.id, childTask.id, wake.requeued_task_id]), token, "等待评论 3 触发的最终 verify 任务完成");
  requireCompletedTask(verifyTask, "评论 3 最终 verify 任务");

  const finalIssue = await get(`/api/issues/${issue.id}`, token);
  const finalTree = await get(`/api/issues/${issue.id}/execution-tree`, token);
  const finalTrace = await get(`/api/issues/${issue.id}/trace`, token);
  const finalUsage = await get(`/api/issues/${issue.id}/usage`, token);
  evidence.final = {
    issue: pickIssue(finalIssue),
    execution_tree: summarizeExecutionTree(finalTree),
    trace_event_count: countItems(finalTrace?.events, finalTrace?.total),
    usage: finalUsage,
  };

  if (finalIssue.status !== "done") {
    fail(`父任务最终状态=${finalIssue.status}，期望 done`);
  }
  if (evidence.final.trace_event_count <= 0) fail("父任务缺少 trace 事件");
  if ((Number(finalUsage?.total_input_tokens || 0) + Number(finalUsage?.total_output_tokens || 0) + Number(finalUsage?.total_cache_read_tokens || 0) + Number(finalUsage?.total_cache_write_tokens || 0)) <= 0) {
    fail("父任务 usage token 总量为 0");
  }

  evidence.result = "completed";
  writeEvidence(evidence);
  console.log(JSON.stringify(evidence, null, 2));
} catch (error) {
  fail(error?.stack || error?.message || String(error));
}

async function ensureCanonicalProjectsAndRepos(token, workspace, currentUser, squad) {
  const resolvedRepos = {};
  for (const spec of repoSpecs) {
    const resolved = await post(`/api/workspaces/${workspace.id}/repos/resolve`, {
      url: spec.url,
      default_branch: repoRef,
    }, token);
    resolvedRepos[spec.key] = resolved;
  }

  const existingRepos = Array.isArray(workspace.repos) ? workspace.repos : [];
  const mergedRepos = [...existingRepos];
  for (const repo of Object.values(resolvedRepos)) {
    const index = mergedRepos.findIndex((item) => item.url === repo.url);
    if (index >= 0) mergedRepos[index] = { ...mergedRepos[index], ...repo };
    else mergedRepos.push(repo);
  }
  const updatedWorkspace = await patch(`/api/workspaces/${workspace.id}`, { repos: mergedRepos }, token);
  evidence.workspace_repos = {
    count: Array.isArray(updatedWorkspace.repos) ? updatedWorkspace.repos.length : 0,
    synced: Object.fromEntries(Object.entries(resolvedRepos).map(([key, repo]) => [key, pickRepo(repo)])),
  };

  const projectsData = await get("/api/projects", token);
  const allProjects = Array.isArray(projectsData?.projects) ? projectsData.projects : [];
  const out = {};
  for (const spec of repoSpecs) {
    const existing = allProjects.find((item) => item.title === spec.title);
    const body = {
      workspace_id: workspace.id,
      title: spec.title,
      description: `canonical ${spec.title} 项目：客户评论式 SOP 验收使用。`,
      status: "in_progress",
      priority: "medium",
      lead_type: "member",
      lead_id: currentUser.id,
    };
    const project = existing?.id
      ? await put(`/api/projects/${existing.id}`, body, token)
      : await post("/api/projects", body, token);
    await ensureProjectGongfengResource(project, spec, resolvedRepos[spec.key], token);
    out[spec.key] = project;
  }
  return out;
}

async function ensureProjectGongfengResource(project, spec, repo, token) {
  const resources = await get(`/api/projects/${project.id}/resources`, token);
  const items = Array.isArray(resources?.resources) ? resources.resources : Array.isArray(resources) ? resources : [];
  const existing = items.find((item) =>
    item.resource_type === "gongfeng_repo" &&
    String(item.resource_ref?.project_path || "") === spec.projectPath
  );
  const resourceRef = {
    provider: "gongfeng",
    url: repo.url,
    project_path: spec.projectPath,
    resource_kind: "commits",
    ref: repoRef,
    branch: repoRef,
    head_commit: repo.head_commit || repo.commit_sha || "",
    commit_sha: repo.commit_sha || repo.head_commit || "",
    connection_status: repo.connection_status || "",
    sync_status: repo.sync_status || "synced",
    test_status: repo.test_status || "",
    last_tested_at: repo.last_tested_at || "",
    last_synced_at: repo.last_synced_at || "",
    title: `${spec.title} ${repoRef}`,
  };
  if (existing?.id) {
    await put(`/api/projects/${project.id}/resources/${existing.id}`, {
      resource_ref: resourceRef,
      label: `${spec.title} ${repoRef}`,
    }, token);
  } else {
    await post(`/api/projects/${project.id}/resources`, {
      resource_type: "gongfeng_repo",
      resource_ref: resourceRef,
      label: `${spec.title} ${repoRef}`,
    }, token);
  }
}

async function postCustomerComment(issueID, token, content) {
  const comment = await post(`/api/issues/${issueID}/comments`, {
    content,
    type: "comment",
  }, token);
  evidence.comments.push({
    id: comment.id || comment.comment_id,
    author_type: comment.author_type,
    content_excerpt: content.slice(0, 240),
  });
  return comment;
}

async function verifyCrossProjectChildren(issue, projects, squad, token) {
  const children = await poll(async () => {
    const data = await get(`/api/issues/${issue.id}/children`, token);
    const items = Array.isArray(data?.issues) ? data.issues : Array.isArray(data) ? data : [];
    const gateway = items.find((item) => item.project_id === projects.gateway.id);
    const deployment = items.find((item) => item.project_id === projects["ida-deployment"].id);
    if (gateway?.id && deployment?.id) return [gateway, deployment];
    return null;
  }, 120_000, "等待 gateway 和 ida-deployment child issue 出现");

  const summaries = children.map(pickIssue);
  for (const child of children) {
    if (child.parent_issue_id !== issue.id) {
      fail(`子任务 ${child.identifier || child.id} parent_issue_id=${child.parent_issue_id}，期望 ${issue.id}`);
    }
    if (child.status !== "backlog") {
      fail(`子任务 ${child.identifier || child.id} 初始状态=${child.status}，期望 backlog`);
    }
    if (child.assignee_type && child.assignee_type !== "squad") {
      fail(`子任务 ${child.identifier || child.id} assignee_type=${child.assignee_type}，期望空或 squad`);
    }
    if (child.assignee_type === "squad" && child.assignee_id !== squad.id) {
      fail(`子任务 ${child.identifier || child.id} assignee_id=${child.assignee_id}，期望 ${squad.id}`);
    }
  }
  evidence.cross_project_children = {
    count: children.length,
    gateway: summaries.find((item) => item.project_id === projects.gateway.id),
    deployment: summaries.find((item) => item.project_id === projects["ida-deployment"].id),
    verified_by_public_api: true,
  };
  return children;
}

async function approveAndWaitChildren(children, projects, token) {
  const byProject = new Map([
    [projects.gateway.id, "gateway"],
    [projects["ida-deployment"].id, "ida-deployment"],
  ]);
  const results = [];
  for (const child of children) {
    const target = byProject.get(child.project_id) || child.project_id;
    const approved = await put(`/api/issues/${child.id}`, { status: "todo" }, token);
    const tasksBefore = await listIssueTasks(child.id, token);
    const terminal = await waitAnyTerminalTask(child.id, new Set(tasksBefore.map((item) => item.id)), token, `等待 ${target} 子任务运行完成`);
    requireCompletedTask(terminal, `${target} 子任务`);
    const trace = await get(`/api/issues/${child.id}/trace`, token);
    const usage = await get(`/api/issues/${child.id}/usage`, token);
    const messages = await get(`/api/tasks/${terminal.id}/messages`, token);
    const totalTokens = Number(usage?.total_input_tokens || 0) + Number(usage?.total_output_tokens || 0) + Number(usage?.total_cache_read_tokens || 0) + Number(usage?.total_cache_write_tokens || 0);
    const traceEvents = Array.isArray(trace?.events) ? trace.events : [];
    const hasUsageUnavailable = traceEvents.some((event) => event?.event_type === "llm.usage_unavailable");
    if (countItems(messages?.items || messages) <= 0) fail(`${target} 子任务完成但缺少 task messages`);
    if (countItems(trace?.events, trace?.total) <= 0) fail(`${target} 子任务缺少 trace`);
    if (totalTokens <= 0 && !hasUsageUnavailable) fail(`${target} 子任务缺少 usage，也没有 usage_unavailable trace`);
    results.push({
      target,
      approved: pickIssue(approved),
      task: pickTask(terminal),
      trace_event_count: countItems(trace?.events, trace?.total),
      message_count: countItems(messages?.items || messages),
      total_tokens: totalTokens,
      usage_unavailable: hasUsageUnavailable,
    });
  }
  evidence.child_task_execution = results;
}

async function completeChildren(children, token) {
  const done = [];
  for (const child of children) {
    const updated = await put(`/api/issues/${child.id}`, { status: "done" }, token);
    done.push(pickIssue(updated));
  }
  evidence.children_completed = done;
}

async function verifyParentWakeAfterChildrenDone(issue, squad, pm, knownParentTaskIDs, token) {
  const parentComment = await poll(async () => {
    const comments = await get(`/api/issues/${issue.id}/comments?roots_only=true&summary=true`, token);
    const items = Array.isArray(comments) ? comments : comments.items ?? [];
    return items.find((item) =>
      item.author_type === "system" &&
      String(item.content || "").includes(`mention://squad/${squad.id}`)
    ) || null;
  }, 120_000, "等待全部子任务 done 后系统评论唤醒父任务");

  const requeued = await waitNextTerminalTask(issue.id, pm.id, knownParentTaskIDs, token, "等待 child-done 系统评论触发父任务继续运行");
  return {
    parent_comment_id: parentComment.id || parentComment.comment_id,
    parent_comment_mentions_squad: String(parentComment.content || "").includes(`mention://squad/${squad.id}`),
    requeued_task_id: requeued.id,
    requeued_task_status: requeued.status,
  };
}

async function runHistoricalServiceSandbox() {
  const started = Date.now();
  try {
    const stdout = execFileSync("node", ["scripts/goal-test-historical-service-sandbox.mjs"], {
      cwd: repoRoot,
      encoding: "utf8",
      maxBuffer: 1024 * 1024 * 24,
      env: { ...process.env },
    });
    const parsed = parseLastJSONObject(stdout);
    return {
      ok: parsed?.ok === true,
      duration_ms: Date.now() - started,
      stdout_tail: tail(stdout, 40),
      report: parsed || null,
    };
  } catch (error) {
    return {
      ok: false,
      duration_ms: Date.now() - started,
      error: error?.message || String(error),
      stdout_tail: tail(error?.stdout || "", 40),
      stderr_tail: tail(error?.stderr || "", 40),
    };
  }
}

async function waitForSOPRun(issueID, squadID, token, label) {
  const run = await poll(async () => {
    const data = await get(`/api/issues/${issueID}/sop-runs`, token);
    const items = Array.isArray(data?.items) ? data.items : [];
    return items.find((item) => item.squad_id === squadID || item.profile_key === "generic-project-sop-flow" || item.profile_key === "user-center-sop-flow") || null;
  }, 60_000, label);
  evidence.sop_run = {
    id: run.id,
    profile_key: run.profile_key,
    current_step_key: run.current_step_key,
    status: run.status,
    event_count: Array.isArray(run.events) ? run.events.length : 0,
  };
  return run;
}

async function waitNextTerminalTask(issueID, agentID, knownIDs, token, label) {
  return poll(async () => {
    const tasks = sortTasks(await listIssueTasks(issueID, token));
    const task = tasks.find((item) =>
      !knownIDs.has(item.id) &&
      (item.agent_id === agentID || item.assignee_id === agentID)
    );
    if (!task) return null;
    evidence.task_poll_snapshot = {
      label,
      total_tasks: tasks.length,
      latest: tasks.slice(0, 5).map(pickTask),
    };
    if (isActiveTask(task)) return null;
    evidence.task_rounds.push({ label, task: pickTask(task) });
    return task;
  }, taskTimeoutMs, label);
}

async function waitAnyTerminalTask(issueID, knownIDs, token, label) {
  return poll(async () => {
    const tasks = sortTasks(await listIssueTasks(issueID, token));
    const task = tasks.find((item) => !knownIDs.has(item.id));
    if (!task || isActiveTask(task)) return null;
    evidence.task_rounds.push({ label, task: pickTask(task) });
    return task;
  }, taskTimeoutMs, label);
}

async function listIssueTasks(issueID, token) {
  const tasks = await get(`/api/issues/${issueID}/task-runs`, token);
  return Array.isArray(tasks) ? tasks : tasks.items ?? [];
}

function requireCompletedTask(task, label) {
  if (!task?.id) fail(`${label} 缺少 task`);
  if (task.status === "completed") return;
  const text = JSON.stringify(task);
  if (/401|Unauthorized|Missing bearer|auth|authentication|invalid_request|not supported with|image_generation|agent_error\.provider_auth_or_access|额度|容量|quota|capacity|rate.?limit|agent_error\.provider_capacity_or_rate_limit/i.test(text)) {
    evidence.external_dependency_failure = true;
    evidence.external_dependency_boundary = `${label} 失败发生在外部模型/认证/容量边界`;
  }
  fail(`${label} 未完成：status=${task.status} error=${task.error || ""} failure_reason=${task.failure_reason || ""}`);
}

function isActiveTask(task) {
  return ["queued", "dispatched", "running", "waiting_local_directory"].includes(task.status);
}

function sortTasks(tasks) {
  return [...tasks].sort((a, b) => new Date(b.created_at || b.started_at || 0).getTime() - new Date(a.created_at || a.started_at || 0).getTime());
}

async function resolveWorkspace(token) {
  const data = await get("/api/workspaces", token);
  const items = Array.isArray(data) ? data : data.items ?? [];
  const workspace = items.find((item) => item.slug === workspaceSlug);
  if (!workspace?.id) fail(`未找到工作区 ${workspaceSlug}`);
  return workspace;
}

async function resolveOnlineRuntime(token, workspaceID) {
  const data = await get(`/api/runtimes?workspace_id=${encodeURIComponent(workspaceID)}`, token);
  const items = Array.isArray(data) ? data : data.items ?? [];
  const runtime = items
    .filter((item) => item.provider === provider && item.status === "online")
    .sort((a, b) => new Date(b.last_seen_at || 0).getTime() - new Date(a.last_seen_at || 0).getTime())[0];
  if (!runtime?.id) fail(`未找到在线 ${provider} runtime`);
  const network = runtime.metadata?.network;
  if (network?.status === "unavailable") {
    fail(`${provider} runtime 网络不可用：${network.failure_hint || network.error || "unknown"}`);
  }
  return runtime;
}

async function get(pathname, token) {
  return request("GET", pathname, undefined, token);
}

async function post(pathname, body, tokenOrOptions) {
  const token = typeof tokenOrOptions === "string" ? tokenOrOptions : "";
  return request("POST", pathname, body, token);
}

async function put(pathname, body, token) {
  return request("PUT", pathname, body, token);
}

async function patch(pathname, body, token) {
  return request("PATCH", pathname, body, token);
}

async function request(method, pathname, body, token) {
  const url = `${apiURL}${pathname}`;
  const headers = { "content-type": "application/json" };
  if (token) headers.Authorization = `Bearer ${token}`;
  if (token && activeWorkspaceId) headers["X-Workspace-ID"] = activeWorkspaceId;
  evidence.commands.push(`${method} ${pathname}`);
  const res = await fetch(url, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) {
    throw new Error(`${method} ${pathname} 返回 ${res.status}: ${text.slice(0, 1000)}`);
  }
  if (!text.trim()) return null;
  return JSON.parse(text);
}

async function poll(fn, timeoutMs, label) {
  const started = Date.now();
  let lastError = null;
  while (Date.now() - started < timeoutMs) {
    try {
      const result = await fn();
      if (result) return result;
    } catch (error) {
      lastError = error;
    }
    await new Promise((resolve) => setTimeout(resolve, pollIntervalMs));
  }
  throw new Error(`${label} 超时${lastError ? `；最后错误：${lastError.message || lastError}` : ""}`);
}

function summarizeExecutionTree(tree) {
  return {
    root_issue_id: tree?.root?.issue_id || tree?.root_issue_id || "",
    child_count: Array.isArray(tree?.root?.children) ? tree.root.children.length : Array.isArray(tree?.children) ? tree.children.length : 0,
    task_count: tree?.summary?.task_count ?? tree?.task_count ?? null,
    trace_count: tree?.summary?.trace_count ?? tree?.trace_count ?? null,
    exception_count: tree?.summary?.exception_count ?? tree?.exception_count ?? null,
  };
}

function countItems(value, fallback = 0) {
  return Array.isArray(value) ? value.length : Number(fallback || 0);
}

function pickUser(user) {
  return { id: user.id, account: user.account || "", name: user.name || "" };
}

function pickWorkspace(workspace) {
  return { id: workspace.id, slug: workspace.slug, name: workspace.name };
}

function pickRuntime(runtime) {
  return {
    id: runtime.id,
    name: runtime.name,
    provider: runtime.provider,
    status: runtime.status,
    last_seen_at: runtime.last_seen_at,
    network: runtime.metadata?.network || null,
  };
}

function pickProject(project) {
  return { id: project.id, title: project.title, status: project.status, lead_type: project.lead_type || null, lead_id: project.lead_id || null };
}

function pickRepo(repo) {
  return {
    url: repo.url,
    project_path: repo.project_path,
    default_branch: repo.default_branch,
    head_commit: repo.head_commit,
    commit_sha: repo.commit_sha,
    connection_status: repo.connection_status,
    sync_status: repo.sync_status,
    test_status: repo.test_status,
  };
}

function pickIssue(issue) {
  return {
    id: issue.id,
    identifier: issue.identifier || "",
    title: issue.title,
    status: issue.status,
    project_id: issue.project_id || null,
    parent_issue_id: issue.parent_issue_id || null,
    assignee_type: issue.assignee_type || null,
    assignee_id: issue.assignee_id || null,
  };
}

function pickTask(task) {
  return {
    id: task.id,
    status: task.status,
    agent_id: task.agent_id,
    assignee_id: task.assignee_id,
    runtime_id: task.runtime_id,
    is_leader_task: task.is_leader_task,
    created_at: task.created_at,
    started_at: task.started_at,
    completed_at: task.completed_at,
    error: task.error || "",
    failure_reason: task.failure_reason || "",
  };
}

function parseLastJSONObject(output) {
  const text = String(output || "").trim();
  for (let index = text.lastIndexOf("{"); index >= 0; index = text.lastIndexOf("{", index - 1)) {
    try {
      return JSON.parse(text.slice(index));
    } catch {
      // Keep scanning logs.
    }
  }
  return null;
}

function tail(output, lines = 20) {
  return String(output || "").trim().split(/\r?\n/).filter(Boolean).slice(-lines);
}

function gitText(args) {
  try {
    return execFileSync("git", args, { cwd: repoRoot, encoding: "utf8" }).trim();
  } catch {
    return "";
  }
}

function trimEnv(name) {
  return String(process.env[name] || "").trim();
}

function writeEvidence(data) {
  mkdirSync(outputDir, { recursive: true });
  const stamp = new Date(data.generated_at || new Date().toISOString()).toISOString().replace(/[:.]/g, "-");
  const pathByTime = path.join(outputDir, `sop-customer-comment-e2e-${stamp}.json`);
  const latestPath = path.join(outputDir, "sop-customer-comment-e2e-latest.json");
  data.evidence_path = pathByTime;
  data.latest_evidence_path = latestPath;
  const content = `${JSON.stringify(data, null, 2)}\n`;
  writeFileSync(pathByTime, content);
  writeFileSync(latestPath, content);
}

function fail(message) {
  evidence.error = message;
  evidence.result = evidence.external_dependency_failure ? "external_dependency_failure" : "failed";
  writeEvidence(evidence);
  console.error(JSON.stringify(evidence, null, 2));
  process.exit(1);
}
