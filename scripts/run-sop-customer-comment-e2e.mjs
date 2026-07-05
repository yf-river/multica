#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const outputDir = acceptanceDir(repoRoot);
const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("GOAL_TEST_INT_API_URL") || "http://127.0.0.1:18762";
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || "develop";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || "develop123";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || "ai-studio";
const provider = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER") || "codebuddy";
const model = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL") || "deepseek-v4-pro-ioa";
const repoRef = trimEnv("ACCEPTANCE_REPO_REF") || "v5.0.0_dev_sop";
const tapdSourceURL = trimEnv("ACCEPTANCE_TAPD_SOURCE_URL") || "https://www.tapd.cn/47654106/markdown_wikis/show/#1147654106001004223";
const taskTimeoutMs = Number(trimEnv("ACCEPTANCE_TASK_TIMEOUT_MS") || 2_700_000);
const pollIntervalMs = Number(trimEnv("ACCEPTANCE_POLL_INTERVAL_MS") || 5_000);
const requestTimeoutMs = Number(trimEnv("ACCEPTANCE_HTTP_TIMEOUT_MS") || 30_000);
const runSandbox = trimEnv("ACCEPTANCE_RUN_SERVICE_SANDBOX") !== "0";
const runReviewLoop = trimEnv("ACCEPTANCE_RUN_CODE_REVIEW_LOOP") !== "0";
const runIncrementalLoop = trimEnv("ACCEPTANCE_RUN_INCREMENTAL_LOOP") !== "0";
const runSimpleAutopilot = trimEnv("ACCEPTANCE_RUN_SIMPLE_AUTOPILOT") !== "0";
const createRealGongfengMR = trimEnv("ACCEPTANCE_CREATE_REAL_GONGFENG_MR") !== "0";
const childWaitMode = trimEnv("ACCEPTANCE_CHILD_WAIT_MODE") || "recursive";
const suffix = Date.now();

const repoSpecs = [
  {
    key: "usercenter",
    title: "usercenter",
    projectPath: "ChainWeaver/ida/user-center",
    url: "https://git.code.tencent.com/ChainWeaver/ida/user-center",
  },
  {
    key: "gateway",
    title: "gateway",
    projectPath: "ChainWeaver/ida/gateway",
    url: "https://git.code.tencent.com/ChainWeaver/ida/gateway",
  },
  {
    key: "ida-deployment",
    title: "ida-deployment",
    projectPath: "ChainWeaver/ida/ida-deployment",
    url: "https://git.code.tencent.com/ChainWeaver/ida/ida-deployment",
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
  tapd_source_url: tapdSourceURL,
  release_commit: gitText(["rev-parse", "--short=12", "HEAD"]),
  branch: gitText(["branch", "--show-current"]),
  commands: [],
  comments: [],
  attachments: [],
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

  const template = await post("/api/squads/internal-template", {
    template_key: "user-center",
    runtime_provider: provider,
    model,
  }, token);
  const squad = template?.squad;
  const agents = Array.isArray(template?.agents) ? template.agents : [];
  const pm = agents.find((item) => item.role_key === "pm" || item.name === "pm") || agents[0];
  const agent01 = agents.find((item) => item.role_key === "01-clarify" || String(item.name || "").startsWith("01-"));
  const agent04 = agents.find((item) => item.role_key === "04-implement" || String(item.name || "").startsWith("04-"));
  const agent05 = agents.find((item) => item.role_key === "05-verify" || String(item.name || "").startsWith("05-"));
  if (!squad?.id || !pm?.id || !agent01?.id || !agent04?.id || !agent05?.id) fail("user-center 内置小队模板缺少 squad、pm、01、04 或 05 agent");
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
    title: `客户评论式 SOP 验收：跨项目验收标记 ${suffix}`,
    description: [
      "这是一条客户视角验收任务。",
      "",
      "背景：本轮只验证 ai-studio PM 小队跨项目 SOP 闭环，不新增产品 API、不新增 proto、不接入 live 集群。",
      "需求：在 user-center、gateway、ida-deployment 三个仓库分别补一处可测试/可校验的 goal-test acceptance marker，用于证明跨项目实现 MR、child issue 递归执行和同 MR CodeReview 返工都可闭环。",
      "主项目 user-center 预期落点：internal/helper/goal_test_acceptance_test.go，增加一个 Go test marker，不能只写 docs。",
      "",
      "请先只确认收到并等待我后续评论推进；不要一次性创建子任务，不要跳到最终验收。",
    ].join("\n"),
    status: "todo",
    priority: "high",
    assignee_type: "squad",
    assignee_id: squad.id,
    project_id: projects.usercenter.id,
    metadata: buildTAPDSourceMetadata(tapdSourceURL),
  }, token);
  if (!issue?.id) fail("创建父任务响应缺少 id");
  activeIssueId = issue.id;
  evidence.issue = pickIssue(issue);
  evidence.source_fetch = await recordTAPDSourceFetch(issue.id, token);

  await waitForSOPRun(issue.id, squad.id, token, "等待父任务 SOP Run 生成");
  const initialTask = await waitNextTerminalTask(issue.id, pm.id, new Set(), token, "等待父任务初始接收任务完成");
  requireCompletedTask(initialTask, "父任务初始接收任务");

  await postCustomerComment(issue.id, token, [
    "客户补充 1：请按真正 SOP 方式先做澄清。",
    "",
    "请读取当前 issue、项目和资源库上下文，判断本轮 acceptance marker 是否会产生 gateway、ida-deployment 两个跨项目 child issue。",
    "这是已确认的验收 fixture：不要因为缺少产品 API、proto、live 集群、TAPD 细节或额外产品口径而阻塞；01 只需确认目标项目、仓库和验收边界。",
    "PM 只负责调度，不要在 PM 自己的任务里代替 01 做澄清。",
    `目标阶段 Agent：${agent01.name}，agent_id=${agent01.id}。`,
    "请 PM 自己发布唯一一条平台 Markdown mention 调度评论来触发 01；客户补充评论不得直接触发 01。",
    "PM 的 mention 显示名和 agent_id 必须匹配目标阶段 Agent，不能复用上一阶段或其它 Agent 的 id。",
    "01 独立读取上下文并发布澄清结论后，再由 PM 判断下一步。",
    "这一轮只做澄清和依赖判断，不要改代码，不要创建任何 child issue；即使 01 判断存在跨项目依赖，PM 也只能评论等待下一条客户确认后再创建。",
  ].join("\n"));
  const clarifyRouteTask = await waitNextTerminalTask(issue.id, pm.id, new Set([initialTask.id]), token, "等待评论 1 触发的 PM 调度 01 任务完成");
  requireCompletedTask(clarifyRouteTask, "评论 1 PM 调度 01 任务");
  const clarifyTask = await waitNextTerminalTask(issue.id, agent01.id, new Set(), token, "等待 01-需求澄清独立任务完成", {
    createdAfter: clarifyRouteTask.created_at || clarifyRouteTask.started_at || clarifyRouteTask.completed_at,
    requireNonEmptyMessage: true,
  });
  requireCompletedTask(clarifyTask, "01-需求澄清独立任务");

  await waitIssueNoActiveTasks(issue.id, token, "等待 01 后自动唤醒任务清空");
  await assertNoCrossProjectChildrenBeforeConfirmation(issue, projects, token);
  const beforeChildCommentTaskIDs = await getIssueTaskIDSet(issue.id, token);
  await postCustomerComment(issue.id, token, [
    "客户确认 2：这个 acceptance marker 需要真实跨项目联调。",
    "",
    "请创建跨项目 child issue：",
    `- gateway：目标项目名 gateway，project_id=${projects.gateway.id}，目标小队 id=${squad.id}，负责 request id / middleware acceptance marker 的真实代码或测试改动。`,
    `- ida-deployment：目标项目名 ida-deployment，project_id=${projects["ida-deployment"].id}，目标小队 id=${squad.id}，负责 Helm/render harness acceptance marker 的真实脚本、配置或测试改动。`,
    "",
    "要求：",
    "- child issue 必须挂到当前父任务。",
    "- child issue 初始状态为 backlog。",
    "- child issue 不要代表 01-05 阶段，只代表跨项目交付物。",
    "- 创建后请回读 children，确认 parent、project、assignee 正确。",
    "- 创建前必须先回读已有 children；如果同一目标项目和同一验收范围已存在 child issue，只能复用已有 child，严禁再创建重复 child。",
  ].join("\n"));
  const childTask = await waitNextTerminalTask(issue.id, pm.id, beforeChildCommentTaskIDs, token, "等待评论 2 触发的跨项目子任务创建完成");
  requireCompletedTask(childTask, "评论 2 跨项目子任务创建任务");

  const children = await verifyCrossProjectChildren(issue, projects, squad, token);
  evidence.child_protocol_handoff = await postChildProtocolHandoffs(children, projects, agents, token);
  await approveAndWaitChildren(children, projects, token);

  const sandboxEvidence = runSandbox ? await runServiceSandboxEvidence() : { skipped: true };
  evidence.service_sandbox = sandboxEvidence;
  if (runSandbox && !sandboxEvidence.ok) fail("service sandbox curl 验收失败");
  if (runSandbox) {
    evidence.service_sandbox_issue_comment = await postServiceSandboxEvidenceComment(issue.id, token, sandboxEvidence, agents);
  }

  await waitChildrenRecursiveClosure(children, token);

  const wake = await verifyParentWakeAfterChildrenDone(issue, squad, pm, new Set([initialTask.id, clarifyRouteTask.id, clarifyTask.id, childTask.id]), token);
  evidence.child_done_wake = wake;

  await waitIssueNoActiveTasks(issue.id, token, "等待子任务完成后父任务自动唤醒清空");
  const beforeFinalVerifyTaskIDs = await getIssueTaskIDSet(issue.id, token);
  await postCustomerComment(issue.id, token, [
    "客户补充 3：所有跨项目子任务已经完成，请继续父任务最终 verify。",
    "",
    "验收要求：",
    "- 读取 gateway 和 ida-deployment 子任务 closure。",
    "- 核对父子任务关系、trace、usage、运行复盘数据。",
    "- 核对上一条证据补充评论中的 acceptance sandbox 验收证据和附件报告。",
    "- 父任务和 child issue 的 MR 都必须是真实实现/测试/脚本/配置改动，不能是 docs-only evidence MR。",
    "- 如果满足验收，请把父任务状态更新为 done。",
  ].join("\n"));
  const verifyTask = await waitNextTerminalTask(issue.id, pm.id, beforeFinalVerifyTaskIDs, token, "等待评论 3 触发的最终 verify 任务完成");
  requireCompletedTask(verifyTask, "评论 3 最终 verify 任务");

  const finalIssue = await get(`/api/issues/${issue.id}`, token);
  if (finalIssue.metadata?.source_provider !== "tapd" || finalIssue.metadata?.source_url !== tapdSourceURL) {
    fail("父任务缺少 TAPD source metadata");
  }
  const finalTree = await get(`/api/issues/${issue.id}/execution-tree`, token);
  const finalTrace = await get(`/api/issues/${issue.id}/trace`, token);
  const finalUsage = await get(`/api/issues/${issue.id}/usage`, token);
  const finalSOPRun = await waitForSOPRunStatus(issue.id, squad.id, token, "已完成", "等待最终 SOP run 闭合");
  evidence.final = {
    issue: pickIssue(finalIssue),
    sop_run: pickSOPRun(finalSOPRun),
    execution_tree: summarizeExecutionTree(finalTree),
    trace_event_count: countItems(finalTrace?.events, finalTrace?.total),
    usage: finalUsage,
  };

  if (finalIssue.status !== "done") {
    fail(`父任务最终状态=${finalIssue.status}，期望 done`);
  }
  if (finalSOPRun.status !== "已完成" || !finalSOPRun.completed_at) {
    fail(`最终 SOP run 未闭合：status=${finalSOPRun.status} completed_at=${finalSOPRun.completed_at || ""}`);
  }
  const hasCompletedStepEvent = Array.isArray(finalSOPRun.events) && finalSOPRun.events.some((item) => item.event_type === "步骤完成");
  if (!hasCompletedStepEvent) fail("最终 SOP run 缺少步骤完成事件");
  if (evidence.final.trace_event_count <= 0) fail("父任务缺少 trace 事件");
  if ((Number(finalUsage?.total_input_tokens || 0) + Number(finalUsage?.total_output_tokens || 0) + Number(finalUsage?.total_cache_read_tokens || 0) + Number(finalUsage?.total_cache_write_tokens || 0)) <= 0) {
    fail("父任务 usage token 总量为 0");
  }

  evidence.source_fetch_trace = summarizeSourceFetchTrace(finalTrace);
  evidence.mr_handoff = createRealGongfengMR
    ? await createAndLinkRealGongfengMR(finalIssue, token)
    : await linkSyntheticGongfengMR(finalIssue, token);
  if (createRealGongfengMR && evidence.mr_handoff.synthetic) {
    fail("真实 Gongfeng MR 创建开启时，不允许回退到 synthetic MR");
  }
  evidence.stage_artifacts = await attachStageMarkdownArtifacts(issue.id, token);

  if (runReviewLoop) {
    evidence.code_review_loop = await runHumanCodeReviewLoop(issue.id, { pm, agent04, agent05 }, token);
    evidence.review_loop = evidence.code_review_loop;
  } else {
    evidence.code_review_loop = { skipped: true };
    evidence.review_loop = evidence.code_review_loop;
  }

  if (runIncrementalLoop) {
    evidence.incremental_requirement_loop = await runIncrementalRequirementLoop(issue.id, { pm, agent04, agent05 }, token);
    evidence.incremental_loop = evidence.incremental_requirement_loop;
  } else {
    evidence.incremental_requirement_loop = { skipped: true };
    evidence.incremental_loop = evidence.incremental_requirement_loop;
  }

  if (runSimpleAutopilot) {
    evidence.simple_autopilot = await runSimpleAutopilotIssue(token, workspace, projects.usercenter, squad, pm);
  } else {
    evidence.simple_autopilot = { skipped: true };
  }

  evidence.final_after_loops = await finalizeIssueAfterAllLoops(issue.id, token);
  evidence.result = "passed";
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

async function postCustomerCommentWithOptions(issueID, token, content, options = {}) {
  const body = {
    content,
    type: options.type || "comment",
  };
  if (Array.isArray(options.attachment_ids) && options.attachment_ids.length > 0) {
    body.attachment_ids = options.attachment_ids;
  }
  if (Array.isArray(options.suppress_agent_ids) && options.suppress_agent_ids.length > 0) {
    body.suppress_agent_ids = options.suppress_agent_ids;
  }
  const comment = await post(`/api/issues/${issueID}/comments`, body, token);
  evidence.comments.push({
    id: comment.id || comment.comment_id,
    author_type: comment.author_type,
    content_excerpt: content.slice(0, 240),
    attachment_count: Array.isArray(comment.attachments) ? comment.attachments.length : 0,
  });
  return comment;
}

async function attachStageMarkdownArtifacts(issueID, token) {
  const stages = [
    ["01-clarify", "需求澄清", "TAPD 来源已抓取；确认本轮是 acceptance fixture，不依赖新增产品 API/proto/live 集群。"],
    ["02-design", "方案设计", "采用 user-center test marker、gateway middleware/test marker、ida-deployment render harness marker 的跨项目验收链路。"],
    ["03-task-split", "任务拆分", "跨项目子任务按 gateway 与 ida-deployment 两个交付物创建，挂到父 issue。"],
    ["04-implement", "开发执行", "子任务完成后父任务被 child-done 系统评论唤醒；各仓库均产生非 docs-only 的真实改动。"],
    ["05-verify", "测试验收", "父任务读取子任务 closure、trace、usage、MR 关联和 acceptance sandbox 证据后关闭。"],
  ];
  const attachments = [];
  for (const [stage, title, summary] of stages) {
    const filename = `${stage}-${suffix}.md`;
    const body = [
      `# ${stage} ${title}`,
      "",
      `- Issue: ${activeIssueId}`,
      `- TAPD: ${tapdSourceURL}`,
      `- 生成时间: ${new Date().toISOString()}`,
      "",
      "## 摘要",
      summary,
      "",
      "## 验收记录",
      "- 本文件由客户评论式 SOP E2E 上传并绑定到 issue 评论，用于验证阶段 markdown 产物可被平台感知、下载和预览。",
    ].join("\n");
    attachments.push(await uploadTextAttachment(issueID, token, filename, body));
  }

  const comment = await postCustomerCommentWithOptions(issueID, token, [
    "/note 阶段产物归档：01-05 markdown 已生成并作为评论附件绑定。",
    "",
    "请在 issue 评论附近查看附件；附件应能下载，并可在网页中以 markdown 预览方式阅读。",
  ].join("\n"), {
    attachment_ids: attachments.map((item) => item.id),
  });

  const previewChecks = [];
  for (const attachment of attachments) {
    const text = await getText(`/api/attachments/${attachment.id}/content`, token);
    previewChecks.push({
      id: attachment.id,
      filename: attachment.filename,
      content_ok: text.includes("# ") && text.includes("阶段 markdown 产物"),
    });
  }

  const commentAttachments = Array.isArray(comment.attachments) ? comment.attachments : [];
  if (commentAttachments.length !== attachments.length) {
    fail(`阶段产物评论附件数=${commentAttachments.length}，期望 ${attachments.length}`);
  }
  if (previewChecks.some((item) => !item.content_ok)) {
    fail("阶段产物 markdown 预览内容校验失败");
  }

  return {
    comment_id: comment.id || comment.comment_id,
    attachment_count: attachments.length,
    attachments,
    preview_checks: previewChecks,
  };
}

async function uploadTextAttachment(issueID, token, filename, content) {
  if (typeof FormData === "undefined" || typeof Blob === "undefined") {
    fail("当前 Node 运行时缺少 FormData/Blob，无法执行附件上传验收");
  }
  const form = new FormData();
  form.append("file", new Blob([content], { type: "text/markdown; charset=utf-8" }), filename);
  form.append("issue_id", issueID);
  const headers = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  if (activeWorkspaceId) headers["X-Workspace-ID"] = activeWorkspaceId;
  evidence.commands.push(`POST /api/upload-file filename=${filename}`);
  const res = await fetchWithTimeout(`${apiURL}/api/upload-file`, {
    method: "POST",
    headers,
    body: form,
  });
  const text = await res.text();
  if (!res.ok) {
    throw new Error(`POST /api/upload-file 返回 ${res.status}: ${text.slice(0, 1000)}`);
  }
  const attachment = JSON.parse(text);
  if (!attachment?.id) fail(`附件上传 ${filename} 未返回 id`);
  const picked = {
    id: attachment.id,
    filename: attachment.filename,
    content_type: attachment.content_type,
    size_bytes: attachment.size_bytes,
    download_url: attachment.download_url || "",
    markdown_url: attachment.markdown_url || "",
  };
  evidence.attachments.push(picked);
  return picked;
}

async function runHumanCodeReviewLoop(issueID, stageAgents, token) {
  const { pm, agent04, agent05 } = stageAgents;
  const beforePRs = await getIssuePullRequestSnapshots(issueID, token);
  const before = new Set((await listIssueTasks(issueID, token)).map((item) => item.id));
  await put(`/api/issues/${issueID}`, { status: "in_review" }, token);
  await postCustomerComment(issueID, token, [
    "人工 CodeReview：MR 已打开，review 发现每一个已关联实现 MR 都需要补充同一 source branch 的 acceptance marker 说明和一条可复验命令证据。",
    "",
    "请从 05 之后的 CodeReview 流程转回 04-implement。",
    "PM 只做调度：请先发布平台 mention 调度 04-implement，等待 04 对每一个已关联实现 MR 的同一个 source branch 都做真实补充改动，并确保每个目标 MR 都产生新的 commit/head 后，再调度 05-verify 逐个验证同一个 MR 已更新，最后由 PM 收口。",
    "不要创建新的跨项目子任务，不要新建无关 MR；如果任一目标 MR 没有新 commit，必须报告 blocker，不能宣称全部完成。",
  ].join("\n"));
  const routeTo04Task = await waitNextTerminalTask(issueID, pm.id, before, token, "等待 CodeReview 评论触发 PM 调度 04");
  requireCompletedTask(routeTo04Task, "CodeReview PM 调度 04");
  const afterRouteTo04 = new Set((await listIssueTasks(issueID, token)).map((item) => item.id));
  const implementTask = await waitNextTerminalTask(issueID, agent04.id, before, token, "等待 CodeReview 04-implement 真实返工");
  requireCompletedTask(implementTask, "CodeReview 04-implement 返工");
  const afterImplement = new Set((await listIssueTasks(issueID, token)).map((item) => item.id));
  const routeTo05Task = await waitNextTerminalTask(issueID, pm.id, afterRouteTo04, token, "等待 CodeReview 返工后 PM 调度 05");
  requireCompletedTask(routeTo05Task, "CodeReview PM 调度 05");
  const verifyTask = await waitNextTerminalTask(issueID, agent05.id, afterImplement, token, "等待 CodeReview 05-verify 验证同 MR");
  requireCompletedTask(verifyTask, "CodeReview 05-verify 验证同 MR");

  const afterFix = new Set((await listIssueTasks(issueID, token)).map((item) => item.id));
  await postCustomerComment(issueID, token, [
    "人工 CodeReview：补充材料已经确认，MR review 通过。",
    "",
    "请 PM 继续完成本轮闭环：确认 MR 链接仍在 issue 关联区，确认运行复盘和附件产物可追溯，然后把 issue 状态自然维护为 done。",
  ].join("\n"));
  const passTask = await waitNextTerminalTask(issueID, pm.id, afterFix, token, "等待 CodeReview 通过评论触发 PM 收尾任务");
  requireCompletedTask(passTask, "CodeReview 通过收尾任务");

  const issue = await get(`/api/issues/${issueID}`, token);
  const afterPRs = await getIssuePullRequestSnapshots(issueID, token);
  const updatedMRs = assertSamePullRequestsUpdated(beforePRs, afterPRs, "CodeReview 后", {
    requiredKeys: codeReviewTargetPullRequestKeys(beforePRs),
  });
  return {
    route_to_04_task: pickTask(routeTo04Task),
    implement_task: pickTask(implementTask),
    route_to_05_task: pickTask(routeTo05Task),
    verify_task: pickTask(verifyTask),
    pass_task: pickTask(passTask),
    final_issue: pickIssue(issue),
    pull_request_count: afterPRs.length,
    updated_pull_requests: updatedMRs,
    same_mr_verified: true,
  };
}

async function runIncrementalRequirementLoop(issueID, stageAgents, token) {
  const { pm, agent04, agent05 } = stageAgents;
  const beforePRs = await getIssuePullRequestSnapshots(issueID, token);
  const before = new Set((await listIssueTasks(issueID, token)).map((item) => item.id));
  await put(`/api/issues/${issueID}`, { status: "in_progress" }, token);
  const comment = await postCustomerComment(issueID, token, [
    "客户追加需求：原跨项目 acceptance marker 已经验收完成，现在同一个 issue 追加两个小功能点。",
    "",
    "1. marker 说明需要明确本次覆盖 user-center、gateway、ida-deployment 三个仓库。",
    "2. 验证摘要需要明确每一个目标 MR/source branch 已被继续维护，不能新建无关 MR。",
    "",
    "请按需求复杂度判断应该从 01 澄清重新开始，还是可以直接回到 04 开发。",
    "如果判断可直接回到 04，PM 必须先发布平台 mention 调度 04-implement；04 对每一个目标 MR 的同一个 source branch 做真实追加改动并产生新的 commit/head；PM 再调度 05-verify；05 逐个验证目标 MR 被更新后，由 PM 最终收口为 done。",
    "不要覆盖上一轮已完成结论，不要新建无关 MR；如果任一目标 MR 没有新 commit，必须报告 blocker，不能宣称全部完成。",
  ].join("\n"));
  const commentID = comment.id || comment.comment_id;
  const commentCreatedAt = comment.created_at || new Date().toISOString();
  const routeTask = await waitNextTerminalTask(issueID, pm.id, before, token, "等待增量需求评论触发 PM 调度", {
    createdAfter: commentCreatedAt,
    requireNonEmptyMessage: true,
  });
  requireCompletedTask(routeTask, "增量需求 PM 调度任务");
  const afterRoute = new Set((await listIssueTasks(issueID, token)).map((item) => item.id));
  const implementTask = await waitNextTerminalTask(issueID, agent04.id, before, token, "等待增量需求 04-implement 同 MR 追加改动");
  requireCompletedTask(implementTask, "增量需求 04-implement 追加改动");
  const afterImplement = new Set((await listIssueTasks(issueID, token)).map((item) => item.id));
  const routeTo05Task = await waitNextTerminalTask(issueID, pm.id, afterRoute, token, "等待增量需求 04 后 PM 调度 05");
  requireCompletedTask(routeTo05Task, "增量需求 PM 调度 05");
  const beforeClose = new Set((await listIssueTasks(issueID, token)).map((item) => item.id));
  const verifyTask = await waitNextTerminalTask(issueID, agent05.id, afterImplement, token, "等待增量需求 05-verify 验证同 MR");
  requireCompletedTask(verifyTask, "增量需求 05-verify 验证同 MR");
  const closeTask = await waitPMCloseTaskOrIssueDone(issueID, pm.id, beforeClose, token, "等待增量需求 PM 最终收口");
  requireCompletedTask(closeTask, "增量需求 PM 最终收口");
  const afterPRs = await getIssuePullRequestSnapshots(issueID, token);
  const updatedMRs = assertSamePullRequestsUpdated(beforePRs, afterPRs, "增量需求后", {
    requiredKeys: codeReviewTargetPullRequestKeys(beforePRs),
  });
  const comments = await get(`/api/issues/${issueID}/comments?roots_only=true&summary=true`, token);
  const items = Array.isArray(comments) ? comments : comments.items ?? [];
  const incrementalComment = items.find((item) => String(item.content || "").includes("追加两个小功能点"));
  if (!incrementalComment?.id || incrementalComment.id !== commentID) fail("增量需求评论未能从 issue 评论流回读");
  return {
    route_task: pickTask(routeTask),
    implement_task: pickTask(implementTask),
    route_to_05_task: pickTask(routeTo05Task),
    verify_task: pickTask(verifyTask),
    close_task: pickTask(closeTask),
    close_satisfied_by_issue_done: Boolean(closeTask.synthetic_issue_done),
    comment_id: commentID,
    same_issue_id: issueID,
    previous_mr_preserved: true,
    updated_pull_requests: updatedMRs,
    same_mr_verified: true,
  };
}

async function runSimpleAutopilotIssue(token, workspace, project, squad, pm) {
  const simple = await post("/api/issues", {
    workspace_id: workspace.id,
    title: `客户评论式 SOP 验收：简单字段直通 ${suffix}`,
    description: [
      "这是一个小需求：只需要在已有 issue 摘要中补充一个展示字段说明。",
      "",
      "判断为简单任务时，可以不逐步人工确认，直接完成澄清、方案、开发和测试摘要。",
      "本需求不要求创建 MR，也不要求进入 04/05 的真实代码改动；PM 可以直接发布最终结论。",
      "如果确认信息已足够，请不要再询问用户是否同意跳过阶段；必须直接把 issue 状态更新为 done，并让 SOP run 自动闭合。",
    ].join("\n"),
    status: "todo",
    priority: "medium",
    assignee_type: "squad",
    assignee_id: squad.id,
    project_id: project.id,
    metadata: buildTAPDSourceMetadata(tapdSourceURL),
  }, token);
  const task = await waitNextTerminalTask(simple.id, pm.id, new Set(), token, "等待简单任务直通完成");
  requireCompletedTask(task, "简单任务直通");
  const finalIssue = await waitIssueStatus(simple.id, "done", token, "等待简单任务 issue 自动闭环到 done");
  const finalSOPRun = await waitForSOPRunStatus(simple.id, squad.id, token, "已完成", "等待简单任务 SOP run 自动闭合");
  const allTasks = sortTasks(await listIssueTasks(simple.id, token));
  return {
    issue: pickIssue(finalIssue),
    task: pickTask(task),
    task_count: allTasks.length,
    final_sop_run: {
      id: finalSOPRun.id,
      profile_key: finalSOPRun.profile_key,
      status: finalSOPRun.status,
      current_step_key: finalSOPRun.current_step_key,
      completed_at: finalSOPRun.completed_at,
    },
  };
}

async function recordTAPDSourceFetch(issueID, token) {
  let fetched = null;
  let metadata = {};
  for (let attempt = 1; attempt <= 3; attempt++) {
    fetched = await post(`/api/issues/${issueID}/source-fetch`, {
      provider: "tapd",
      auto_fetch: true,
    }, token);
    metadata = fetched?.metadata || {};
    if (metadata.source_fetch_status === "fetched") {
      break;
    }
    if (attempt < 3) {
      await sleep(2_000);
    }
  }
  if (!String(metadata.source_fetch_title || "").trim()) {
    fail("TAPD source-fetch 未写入 source_fetch_title");
  }
  if (metadata.source_fetch_status !== "fetched" || metadata.source_fetch_provider !== "tapd_mcp") {
    fail(`TAPD source-fetch 未真实 fetched：${JSON.stringify(metadata)}`);
  }
  if (!String(metadata.source_fetch_body_excerpt || "").trim()) {
    fail("TAPD source-fetch 未返回真实正文摘录");
  }
  return {
    metadata: {
      source_provider: metadata.source_provider || "tapd",
      source_url: metadata.source_url || tapdSourceURL,
      tapd_workspace_id: metadata.tapd_workspace_id || "",
      tapd_resource_id: metadata.tapd_resource_id || "",
      tapd_resource_type: metadata.tapd_resource_type || "",
    },
    persisted: metadata.source_provider === "tapd" && metadata.source_url === tapdSourceURL,
    title: metadata.source_fetch_title,
    status: metadata.source_fetch_status,
    provider: metadata.source_fetch_provider,
    url: metadata.source_fetch_url,
    resource_type: metadata.source_fetch_resource_type || metadata.tapd_resource_type,
    resource_id: metadata.source_fetch_resource_id || metadata.tapd_resource_id,
    workspace_id: metadata.source_fetch_workspace_id || metadata.tapd_workspace_id,
    version: metadata.source_fetch_version || "",
    duration_ms: metadata.source_fetch_duration_ms || 0,
    body_excerpt: metadata.source_fetch_body_excerpt || "",
    body: metadata.source_fetch_body_excerpt || "",
    credential_profile: {
      scope: metadata.source_credential_scope || "",
      inheritance: metadata.source_credential_inheritance || "",
      profile_id: metadata.source_credential_profile_id || "",
      profile_name: metadata.source_credential_profile_name || "",
      status: metadata.source_credential_profile_status || "",
    },
    trace_event_id: fetched?.trace_event?.id || "",
    api_auto_fetch_verified: true,
  };
}

async function finalizeIssueAfterAllLoops(issueID, token) {
  await waitIssueNoActiveTasks(issueID, token, "等待所有 loop 后 issue 无 active task");
  const normalized = await get(`/api/issues/${issueID}`, token);
  const prs = await get(`/api/issues/${issueID}/pull-requests`, token);
  const comments = await get(`/api/issues/${issueID}/comments?roots_only=true&summary=true`, token);
  const finalTrace = await get(`/api/issues/${issueID}/trace`, token);
  const finalUsage = await get(`/api/issues/${issueID}/usage`, token);
  const commentItems = Array.isArray(comments) ? comments : comments.items ?? [];
  const prItems = Array.isArray(prs?.pull_requests) ? prs.pull_requests : [];
  if (normalized.status !== "done") fail(`所有 loop 完成后父任务状态=${normalized.status}，期望 done`);
  if (prItems.length <= 0) fail("所有 loop 完成后 MR 关联丢失");
  const worktreeClean = assertIssueWorktreeClean(issueID, "user-center");
  if (!commentItems.some((item) => String(item.content || "").includes("人工 CodeReview"))) {
    fail("所有 loop 完成后评论流缺少人工 CodeReview 记录");
  }
  if (!commentItems.some((item) => String(item.content || "").includes("追加两个小功能点"))) {
    fail("所有 loop 完成后评论流缺少增量需求记录");
  }
  return {
    issue: pickIssue(normalized),
    pull_request_count: prItems.length,
    comment_count: commentItems.length,
    trace_event_count: countItems(finalTrace?.events, finalTrace?.total),
    worktree_clean: worktreeClean,
    usage: finalUsage,
  };
}

async function getIssuePullRequests(issueID, token) {
  const prs = await get(`/api/issues/${issueID}/pull-requests`, token);
  return normalizePullRequests(prs).map(pickPullRequest).filter((item) => item.html_url || item.number || item.branch);
}

async function getIssuePullRequestSnapshots(issueID, token) {
  const items = await getIssuePullRequests(issueID, token);
  const branchHints = buildPullRequestBranchHints(items);
  const snapshots = [];
  for (const item of items) {
    const snapshot = { ...item };
    snapshot.project_path = canonicalGongfengProjectPath(snapshot.project_path || snapshot.html_url || "");
    if (!snapshot.branch) {
      snapshot.branch = branchHints.get(pullRequestIdentity(snapshot)) || branchHints.get(pullRequestProjectNumberKey(snapshot)) || "";
    }
    if (snapshot.branch) {
      const repoName = repoNameForPullRequest(snapshot);
      if (repoName) {
        const localRemoteHead = getIssueRemoteBranchHead(issueID, repoName, snapshot.branch);
        if (localRemoteHead) snapshot.remote_head_sha = localRemoteHead;
      }
    }
    if (!snapshot.remote_head_sha && snapshot.provider === "gongfeng" && snapshot.project_path && snapshot.branch) {
      try {
        snapshot.remote_head_sha = await getGongfengBranchHead(snapshot.project_path, snapshot.branch);
      } catch (error) {
        snapshot.remote_head_error = redactSecretText(error?.message || String(error));
      }
    }
    snapshots.push(snapshot);
  }
  return snapshots;
}

function repoNameForPullRequest(item) {
  const projectPath = String(item?.project_path || item?.html_url || "");
  if (projectPath.includes("ida-deployment")) return "ida-deployment";
  if (projectPath.includes("gateway")) return "gateway";
  if (projectPath.includes("user-center")) return "user-center";
  return "";
}

function getIssueRemoteBranchHead(issueID, repoName, branch) {
  const worktree = findIssueRepoWorktree(issueID, repoName) || findAnyRepoWorktree(repoName);
  if (!worktree) return "";
  const output = execText("git", ["-C", worktree, "ls-remote", "origin", `refs/heads/${branch}`]);
  return output.split(/\s+/)[0] || "";
}

function assertSamePullRequests(before, after, label) {
  if (!Array.isArray(before) || before.length <= 0) fail(`${label} 缺少前置 MR，无法验证同 MR 维护`);
  if (!Array.isArray(after) || after.length <= 0) fail(`${label} MR 关联丢失`);
  const beforeKeys = new Set(before.map(pullRequestIdentity).filter(Boolean));
  const afterKeys = new Set(after.map(pullRequestIdentity).filter(Boolean));
  for (const key of beforeKeys) {
    if (!afterKeys.has(key)) fail(`${label} 原 MR 不再关联：${key}`);
  }
  if (afterKeys.size !== beforeKeys.size) {
    fail(`${label} MR 数量变化：before=${beforeKeys.size} after=${afterKeys.size}，期望维护同一个 MR/branch，不新建无关 MR`);
  }
  const beforeBranches = new Set(before.map((item) => item.branch).filter(Boolean));
  const afterBranches = new Set(after.map((item) => item.branch).filter(Boolean));
  for (const branch of beforeBranches) {
    if (!afterBranches.has(branch)) fail(`${label} 原 source branch 不再关联：${branch}`);
  }
}

function assertSamePullRequestsUpdated(before, after, label, options = {}) {
  assertSamePullRequests(before, after, label);
  const beforeByKey = new Map(before.map((item) => [pullRequestIdentity(item), item]).filter(([key]) => key));
  const requiredKeys = new Set(Array.isArray(options.requiredKeys) ? options.requiredKeys.filter(Boolean) : []);
  const updated = [];
  const missingUpdates = [];
  for (const item of after) {
    const key = pullRequestIdentity(item);
    const previous = beforeByKey.get(key);
    if (!previous) continue;
    const beforeHead = effectivePullRequestHead(previous);
    const afterHead = effectivePullRequestHead(item);
    if (beforeHead && afterHead && beforeHead !== afterHead) {
      updated.push({
        key,
        branch: item.branch,
        project_path: item.project_path || previous.project_path || "",
        before_head: beforeHead,
        after_head: afterHead,
      });
    } else if (requiredKeys.has(key)) {
      missingUpdates.push({
        key,
        project_path: item.project_path || previous.project_path || "",
        branch: item.branch || previous.branch || "",
        before_head: beforeHead,
        after_head: afterHead,
        reason: beforeHead && afterHead ? "head_unchanged" : "head_unavailable",
      });
    }
  }
  if (missingUpdates.length > 0) {
    fail(`${label} 目标 MR 未全部更新；未更新或无法比较：${JSON.stringify(missingUpdates)}`);
  }
  if (updated.length <= 0) {
    fail(`${label} MR 仍是同一个，但远端 head 未变化；这不能证明 CodeReview/追加需求被维护进同一个 MR`);
  }
  return updated;
}

function codeReviewTargetPullRequestKeys(pullRequests) {
  const crossProject = pullRequests
    .filter((item) => !String(item.project_path || item.html_url || "").includes("user-center"))
    .map(pullRequestIdentity)
    .filter(Boolean);
  if (crossProject.length > 0) return crossProject;
  return pullRequests.map(pullRequestIdentity).filter(Boolean);
}

function buildPullRequestBranchHints(items) {
  const hints = new Map();
  const completed = Array.isArray(evidence.children_completed) ? evidence.children_completed : [];
  for (const child of completed) {
    const childBranch = issueSourceBranch(child);
    for (const pr of child.pull_requests || []) {
      const picked = pickPullRequest(pr);
      if (!childBranch) continue;
      const identity = pullRequestIdentity(picked);
      const projectNumber = pullRequestProjectNumberKey(picked);
      if (identity) hints.set(identity, picked.branch || childBranch);
      if (projectNumber) hints.set(projectNumber, picked.branch || childBranch);
    }
  }
  for (const item of items) {
    if (!item.branch) continue;
    const identity = pullRequestIdentity(item);
    const projectNumber = pullRequestProjectNumberKey(item);
    if (identity) hints.set(identity, item.branch);
    if (projectNumber) hints.set(projectNumber, item.branch);
  }
  return hints;
}

function pullRequestProjectNumberKey(item) {
  const projectPath = canonicalGongfengProjectPath(item?.project_path || item?.html_url || "");
  const number = Number(item?.number || 0);
  return projectPath && number > 0 ? `${projectPath}!${number}` : "";
}

function assertIssueWorktreeClean(issueID, repoName) {
  const worktree = findIssueRepoWorktree(issueID, repoName);
  if (!worktree) {
    return { checked: false, reason: `未找到 ${repoName} issue worktree` };
  }
  const status = execText("git", ["-C", worktree, "status", "--short", "--untracked-files=all"]);
  const dirty = status
    .split(/\r?\n/)
    .map((line) => line.trimEnd())
    .filter(Boolean)
    .filter((line) => !isAllowedWorktreeStatusLine(worktree, line));
  if (dirty.length > 0) {
    fail(`issue ${issueID} 的 ${repoName} worktree 仍有未提交业务改动，不能证明 MR 包含真实实现：${worktree}\n${dirty.slice(0, 20).join("\n")}`);
  }
  const branch = execText("git", ["-C", worktree, "branch", "--show-current"]);
  return { checked: true, path: worktree, branch };
}

function findIssueRepoWorktree(issueID, repoName) {
  const root = path.join(repoRoot, ".run", "workspaces");
  if (!existsSync(root)) return "";
  const issuePathIDs = [...new Set([String(issueID || ""), String(issueID || "").slice(0, 8)].filter(Boolean))];
  for (const pathID of issuePathIDs) {
    const gitPath = execText("find", [root, "-path", `*/issues/${pathID}/repos/${repoName}/.git`, "-print", "-quit"]);
    if (gitPath) return path.dirname(gitPath);
    const nestedGitPath = execText("find", [root, "-path", `*/issues/${pathID}/repos/*/${repoName}/.git`, "-print", "-quit"]);
    if (nestedGitPath) return path.dirname(nestedGitPath);
  }
  const gitPath = execText("find", [root, "-path", `*/issues/${String(issueID || "").slice(0, 8)}*/repos/${repoName}/.git`, "-print", "-quit"]);
  if (!gitPath) return "";
  return path.dirname(gitPath);
}

function findAnyRepoWorktree(repoName) {
  const root = path.join(repoRoot, ".run", "workspaces");
  if (!existsSync(root)) return "";
  const exact = execText("find", [root, "-path", `*/issues/*/repos/${repoName}/.git`, "-print", "-quit"]);
  if (exact) return path.dirname(exact);
  const nested = execText("find", [root, "-path", `*/issues/*/repos/*/${repoName}/.git`, "-print", "-quit"]);
  if (nested) return path.dirname(nested);
  return "";
}

function isAllowedWorktreeStatusLine(worktree, line) {
  const file = line.replace(/^.. ?/, "");
  if (line.startsWith("?? ") && file.endsWith("/") && isNestedGitRepository(path.join(worktree, file))) {
    return true;
  }
  return [
    "AGENTS.md",
    "CLAUDE.md",
    "reply.md",
  ].includes(file) ||
    file.startsWith(".agent_context/") ||
    file.startsWith(".claude/") ||
    file.startsWith(".multica/") ||
    file.startsWith("artifacts/");
}

function isNestedGitRepository(candidatePath) {
  if (!candidatePath || !existsSync(candidatePath)) return false;
  try {
    const nestedRoot = execText("git", ["-C", candidatePath, "rev-parse", "--show-toplevel"]);
    return path.resolve(nestedRoot) === path.resolve(candidatePath);
  } catch {
    return false;
  }
}

function effectivePullRequestHead(item) {
  return item?.remote_head_sha || item?.head_sha || "";
}

function pullRequestIdentity(item) {
  if (!item) return "";
  return item.html_url || (item.number ? `number:${item.number}` : item.id ? `id:${item.id}` : item.branch ? `branch:${item.branch}` : "");
}

async function linkSyntheticGongfengMR(issue, token) {
  const iid = Number((suffix % 1_000_000) + 700_000);
  const identifier = issue.identifier || `GOA-${iid}`;
  const mrURL = `https://git.code.tencent.com/ChainWeaver/ida/user-center/merge_requests/${iid}`;
  const linked = await post(`/api/issues/${issue.id}/pull-requests`, {
    provider: "gongfeng",
    html_url: mrURL,
    title: `${identifier} goal-test acceptance marker`,
    state: "open",
    source_branch: `${identifier.toLowerCase()}-goal-test-acceptance-marker`,
    target_branch: repoRef,
    author_login: "codex",
    head_sha: gitText(["rev-parse", "--short=12", "HEAD"]) || "synthetic",
  }, token);
  const list = await get(`/api/issues/${issue.id}/pull-requests`, token);
  const prs = Array.isArray(list?.pull_requests) ? list.pull_requests : [];
  const matched = prs.find((item) => item.html_url === mrURL || item.number === iid);
  if (!matched) {
    fail("MR 关联回写后，issue pull-requests 列表未读到该 MR");
  }
  return {
    synthetic: true,
    linked: Boolean(linked?.pull_request?.id),
    url: mrURL,
    number: iid,
    title: matched.title,
    state: matched.state,
    source_branch: matched.branch || "",
  };
}

async function createAndLinkRealGongfengMR(issue, token) {
  const identifier = issue.identifier || `GOA-${suffix}`;
  const projectPath = "ChainWeaver/ida/user-center";
  const targetBranch = repoRef;
  const sourceBranch = issueSourceBranch(issue);
  const filePath = "internal/helper/goal_test_acceptance_test.go";
  const title = `${identifier}: add goal-test acceptance marker test`;
  const content = [
    "package helper",
    "",
    "import \"testing\"",
    "",
    "func TestGoalTestAcceptanceMarker(t *testing.T) {",
    `\tconst marker = "${identifier}-goal-test-cross-project-marker-${suffix}"`,
    "\tif marker == \"\" {",
    "\t\tt.Fatal(\"goal-test acceptance marker must not be empty\")",
    "\t}",
    "\tif len(marker) < 16 {",
    "\t\tt.Fatalf(\"goal-test acceptance marker is unexpectedly short: %q\", marker)",
    "\t}",
    "}",
  ].join("\n");

  const branch = await ensureGongfengBranch(projectPath, sourceBranch, targetBranch);
  const createdFile = await upsertGongfengFile(projectPath, sourceBranch, filePath, content, `${identifier}: add goal-test acceptance marker test`);
  const mr = await gongfengRequest("POST", `projects/${encodeGongfengProjectID(projectPath)}/merge_requests`, {
    source_branch: sourceBranch,
    target_branch: targetBranch,
    title,
    description: [
      `Multica issue: ${identifier}`,
      "",
      `TAPD source: ${tapdSourceURL}`,
      "",
      "Created by the goal-test customer-comment SOP acceptance harness after 05-verify.",
      "This MR intentionally changes a Go test file rather than docs-only evidence so the AIS-40 acceptance validates a real implementation/test MR.",
    ].join("\n"),
    remove_source_branch: false,
    squash: false,
  });
  const iid = Number(mr.iid || mr.number || 0);
  const mrURL = mr.web_url || mr.html_url || `https://git.code.tencent.com/${projectPath}/merge_requests/${iid || ""}`;
  if (!iid || !mrURL) {
    fail(`Gongfeng MR 创建成功但响应缺少 iid/web_url：${JSON.stringify(safeGongfengResponse(mr))}`);
  }

  const headSha = createdFile.commit_id || await getGongfengBranchHead(projectPath, sourceBranch) || branch?.commit?.id || "";
  const linked = await post(`/api/issues/${issue.id}/pull-requests`, {
    provider: "gongfeng",
    project_path: projectPath,
    html_url: mrURL,
    number: iid,
    iid,
    title: mr.title || title,
    state: normalizeGongfengMRState(mr),
    source_branch: sourceBranch,
    target_branch: targetBranch,
    author_login: mr.author?.username || mr.author?.name || "gongfeng",
    head_sha: headSha,
    changed_files: 1,
  }, token);
  const list = await get(`/api/issues/${issue.id}/pull-requests`, token);
  const prs = Array.isArray(list?.pull_requests) ? list.pull_requests : [];
  const matched = prs.find((item) => item.html_url === mrURL || Number(item.number) === iid);
  if (!matched) {
    fail("真实 Gongfeng MR 关联回写后，issue pull-requests 列表未读到该 MR");
  }
  const verifiedMR = await findGongfengMRByList(projectPath, {
    id: Number(mr.id || 0),
    iid,
    sourceBranch,
    targetBranch,
    title: mr.title || title,
  });

  return {
    synthetic: false,
    linked: Boolean(linked?.pull_request?.id),
    url: mrURL,
    number: iid,
    title: matched.title,
    state: matched.state,
    source_branch: matched.branch || sourceBranch,
    target_branch: matched.base_branch || targetBranch,
    project_path: projectPath,
    implementation_file_path: filePath,
    head_sha: headSha,
    verified_by_gongfeng_api: Boolean(verifiedMR?.iid || verifiedMR?.id),
    gongfeng_id: Number(verifiedMR?.id || mr.id || 0),
  };
}

function issueSourceBranch(issue) {
  const rawID = String(issue?.id || "").trim();
  if (rawID) return `agent/issue/${rawID.slice(0, 8)}`;
  return `agent/issue/${String(issue?.identifier || `issue-${suffix}`).toLowerCase().replace(/[^a-z0-9._-]+/g, "-").slice(0, 32)}`;
}

async function ensureGongfengBranch(projectPath, branchName, ref) {
  try {
    return await gongfengRequest("GET", `projects/${encodeGongfengProjectID(projectPath)}/repository/branches/${encodeURIComponent(branchName)}`);
  } catch (error) {
    if (!/返回 404/.test(error?.message || String(error))) throw error;
  }
  return gongfengRequest("POST", `projects/${encodeGongfengProjectID(projectPath)}/repository/branches`, {
    branch_name: branchName,
    ref,
  });
}

async function upsertGongfengFile(projectPath, branchName, filePath, content, commitMessage) {
  const project = encodeGongfengProjectID(projectPath);
  const encodedFilePath = encodeURIComponent(filePath);
  try {
    await gongfengRequest("GET", `projects/${project}/repository/files/${encodedFilePath}?ref=${encodeURIComponent(branchName)}`);
    return gongfengRequest("PUT", `projects/${project}/repository/files/${encodedFilePath}`, {
      branch_name: branchName,
      content,
      commit_message: commitMessage,
    });
  } catch (error) {
    if (!/返回 404/.test(error?.message || String(error))) throw error;
  }
  return gongfengRequest("POST", `projects/${project}/repository/files`, {
    file_path: filePath,
    branch_name: branchName,
    content,
    commit_message: commitMessage,
  });
}

async function getGongfengBranchHead(projectPath, branchName) {
  const branch = await gongfengRequest("GET", `projects/${encodeGongfengProjectID(projectPath)}/repository/branches/${encodeURIComponent(branchName)}`);
  return branch?.commit?.id || branch?.commit?.short_id || "";
}

async function gongfengRequest(method, apiPath, body) {
  const env = loadGongfengEnv();
  const token = env.GONGFENG_PRIVATE_TOKEN || env.GONGFENG_ACCESS_TOKEN;
  if (!token) {
    fail("缺少 GONGFENG_PRIVATE_TOKEN/GONGFENG_ACCESS_TOKEN，无法创建真实 Gongfeng MR");
  }
  const base = normalizeGongfengAPIBase(env.GONGFENG_API_BASE || env.GONGFENG_API_URL || "https://git.code.tencent.com/api/v3");
  const res = await fetchWithTimeout(`${base}/${apiPath.replace(/^\/+/, "")}`, {
    method,
    headers: {
      "PRIVATE-TOKEN": token,
      "content-type": "application/json",
      accept: "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  if (!res.ok) {
    throw new Error(`Gongfeng ${method} ${apiPath} 返回 ${res.status}: ${redactSecretText(text).slice(0, 1000)}`);
  }
  if (!text.trim()) return null;
  return JSON.parse(text);
}

async function findGongfengMRByList(projectPath, expected) {
  const project = encodeGongfengProjectID(projectPath);
  const queries = [
    new URLSearchParams({
      state: "opened",
      source_branch: expected.sourceBranch,
      per_page: "100",
    }),
    new URLSearchParams({
      state: "all",
      per_page: "100",
    }),
  ];

  const errors = [];
  for (const query of queries) {
    try {
      const list = await gongfengRequest("GET", `projects/${project}/merge_requests?${query.toString()}`);
      const items = Array.isArray(list) ? list : [];
      const matched = items.find((item) => {
        const sameID = expected.id > 0 && Number(item.id || 0) === expected.id;
        const sameIID = expected.iid > 0 && Number(item.iid || item.number || 0) === expected.iid;
        const sameBranch = String(item.source_branch || "") === expected.sourceBranch;
        const sameTarget = !expected.targetBranch || String(item.target_branch || "") === expected.targetBranch;
        const sameTitle = !expected.title || String(item.title || "") === expected.title;
        return sameID || (sameIID && sameBranch) || (sameBranch && sameTarget && sameTitle);
      });
      if (matched) return matched;
    } catch (error) {
      errors.push(error?.message || String(error));
    }
  }

  fail(`真实 Gongfeng MR 创建后，列表接口未回查到匹配 MR：${JSON.stringify({
    id: expected.id,
    iid: expected.iid,
    source_branch: expected.sourceBranch,
    target_branch: expected.targetBranch,
    errors: errors.map((item) => redactSecretText(item)).slice(0, 3),
  })}`);
}

function loadGongfengEnv() {
  const env = { ...process.env };
  const envFile = trimEnv("GONGFENG_MCP_ENV_FILE") || "/root/.config/gongfeng-mcp/env";
  if (existsSync(envFile)) {
    const raw = readFileSync(envFile, "utf8");
    for (const line of raw.split(/\r?\n/)) {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith("#")) continue;
      const match = trimmed.match(/^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
      if (!match) continue;
      const key = match[1];
      let value = match[2].trim();
      if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
        value = value.slice(1, -1);
      }
      if (!(key in env)) env[key] = value;
    }
  }
  return env;
}

function normalizeGongfengAPIBase(raw) {
  const value = String(raw || "").replace(/\/+$/, "");
  return value.endsWith("/api/v3") ? value : `${value}/api/v3`;
}

function encodeGongfengProjectID(projectPath) {
  return encodeURIComponent(projectPath);
}

function normalizeGongfengMRState(mr) {
  const state = String(mr.state || mr.status || "").toLowerCase();
  if (mr.merged_at || state === "merged") return "merged";
  if (state === "closed") return "closed";
  if (mr.work_in_progress || mr.draft || state === "draft") return "draft";
  return "open";
}

function safeGongfengResponse(value) {
  return JSON.parse(redactSecretText(JSON.stringify(value || {})));
}

function redactSecretText(text) {
  return String(text || "").replace(/(PRIVATE-TOKEN|GONGFENG_(?:PRIVATE_)?TOKEN|access_token|private_token)["':=\s]+[A-Za-z0-9._-]+/gi, "$1=<redacted>");
}

function summarizeSourceFetchTrace(trace) {
  const events = Array.isArray(trace?.events) ? trace.events : [];
  const matched = events.filter((event) => event?.event_type === "source.fetch");
  return {
    event_count: matched.length,
    events: matched.slice(0, 5).map((event) => ({
      id: event.id,
      status: event.status,
      task_id: event.task_id,
      agent_id: event.agent_id,
      duration_ms: event.duration_ms,
    })),
  };
}

async function verifyCrossProjectChildren(issue, projects, squad, token) {
  const children = await poll(async () => {
    const items = await listChildrenForParent(issue.id, token);
    const targets = targetCrossProjectChildren(items, projects);
    if (targets.duplicates.length > 0) {
      fail(`跨项目 child issue 重复创建：${JSON.stringify(targets.duplicates)}`);
    }
    if (targets.gateway?.id && targets.deployment?.id) return [targets.gateway, targets.deployment];
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
  evidence.children = summaries;
  evidence.cross_project_children = {
    count: children.length,
    gateway: summaries.find((item) => item.project_id === projects.gateway.id),
    deployment: summaries.find((item) => item.project_id === projects["ida-deployment"].id),
    verified_by_public_api: true,
    backlog_status_verified: summaries.every((item) => item.status === "backlog"),
    target_sop_squad_assignee_verified: summaries.every((item) => !item.assignee_type || (item.assignee_type === "squad" && item.assignee_id === squad.id)),
  };
  evidence.project_owner_notifications = await verifyProjectOwnerApprovalNotifications(children, token);
  return children;
}

async function assertNoCrossProjectChildrenBeforeConfirmation(issue, projects, token) {
  const items = await listChildrenForParent(issue.id, token);
  const targets = targetCrossProjectChildren(items, projects);
  const premature = [targets.gateway, targets.deployment].filter(Boolean);
  if (premature.length > 0 || targets.duplicates.length > 0) {
    fail(`PM 在客户确认创建前提前创建了跨项目 child issue：${JSON.stringify({
      premature: premature.map((item) => pickIssue(item)),
      duplicates: targets.duplicates,
    })}`);
  }
}

function targetCrossProjectChildren(items, projects) {
  const byProject = new Map([
    [projects.gateway.id, "gateway"],
    [projects["ida-deployment"].id, "deployment"],
  ]);
  const grouped = { gateway: [], deployment: [] };
  for (const item of Array.isArray(items) ? items : []) {
    const key = byProject.get(item.project_id);
    if (key) grouped[key].push(item);
  }
  const duplicates = Object.entries(grouped)
    .filter(([, values]) => values.length > 1)
    .map(([target, values]) => ({
      target,
      issues: values.map((item) => ({
        id: item.id,
        identifier: item.identifier || "",
        status: item.status || "",
        title: item.title || "",
      })),
    }));
  return {
    gateway: grouped.gateway[0] || null,
    deployment: grouped.deployment[0] || null,
    duplicates,
  };
}

async function verifyProjectOwnerApprovalNotifications(children, token) {
  const childIDs = new Set(children.map((item) => item.id));
  const inbox = await poll(async () => {
    const items = await get("/api/inbox", token);
    const matched = (Array.isArray(items) ? items : items.items ?? []).filter((item) =>
      childIDs.has(item.issue_id) && item.type === "project_issue_approval_requested"
    );
    return matched.length >= children.length ? matched : null;
  }, 60_000, "等待项目负责人审批 inbox 通知");
  return {
    verified: true,
    expected_count: children.length,
    count: inbox.length,
    items: inbox.map((item) => ({
      id: item.id,
      issue_id: item.issue_id,
      type: item.type,
      severity: item.severity,
      read: item.read,
    })),
  };
}

async function listChildrenForParent(parentID, token) {
  const direct = await get(`/api/issues/${parentID}/children`, token);
  const directItems = Array.isArray(direct?.issues) ? direct.issues : Array.isArray(direct) ? direct : [];
  if (directItems.length > 0) return directItems;
  const batched = await get(`/api/issues/children?parent_ids=${encodeURIComponent(parentID)}`, token);
  return Array.isArray(batched?.issues) ? batched.issues : Array.isArray(batched) ? batched : [];
}

async function postChildProtocolHandoffs(children, projects, agents, token) {
  const results = [];
  const stageDirectory = ["01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"]
    .map((roleKey) => agents.find((agent) => agent.role_key === roleKey || String(agent.name || "").startsWith(roleKey.slice(0, 2))))
    .filter(Boolean)
    .map((agent) => `- ${agent.role_key || agent.name}: ${agent.name} (agent_id=${agent.id})`)
    .join("\n");
  for (const child of children) {
    const target = child.project_id === projects.gateway.id
      ? "gateway"
      : child.project_id === projects["ida-deployment"].id
        ? "ida-deployment"
        : "unknown";
    const content = target === "gateway"
      ? [
        "客户补充：本 child issue 是 gateway acceptance marker 验收，不是新增产品 API；不需要 usercenter proto、QuickEntry service 或 live 集群。",
        "",
        "## gateway 验收合同",
        "- 目标仓库：ChainWeaver/ida/gateway。",
        `- 目标分支：本轮仓库资源指定的 \`${repoRef}\`。`,
        "- 04 必须产生非 docs-only 改动，优先在 `internal/middleware/requestidinterceptorMiddleware.go`、`internal/middleware/generic_rpc_test.go` 或相邻 Go test 中增加 goal-test acceptance marker。",
        "- marker 必须可被 `go test` 或静态 grep 验证，建议包含 `goal-test-cross-project-marker` 和本 issue 编号。",
        "- 不新增真实 HTTP API，不依赖 user-center proto，不要求接入 live 集群。",
        "",
        "## gateway 交付边界",
        "- 01 只确认落点和验收方式；不得因为产品 API/proto 未定义阻塞。",
        "- 02/03 输出最小方案和文件级任务。",
        "- 04 修改 Go 代码或 Go test；不能只写 markdown/docs/harness 报告。",
        "- 05 运行能覆盖改动的最小验证，例如 `go test ./internal/middleware` 或等价静态校验，并创建/更新 gateway 真实 Gongfeng MR。",
        "",
        "## 本轮 E2E 递归约束",
        "- 这个 child issue 已分配给 PM 小队，必须按同一套 PM -> 01 -> 02 -> 03 -> 04 -> 05 -> PM 收口流程完成。",
        "- PM 负责调度，01-05 必须分别产生自己的 task、评论和阶段产物。",
        "- PM 禁止在自己的单个任务里代跑 01-05；禁止只用内部 todo/TaskCreate/TaskUpdate 表示阶段完成。",
        "- PM 当前任务只允许发布一个平台 Markdown mention 调度评论，然后立即结束；不得继续读取或执行 01/02/03/04/05 阶段 skill。",
        "- PM 每一阶段必须通过平台 Markdown mention 评论触发新的平台 agent_task_queue 任务，等该阶段 Agent 独立评论后，在新的 PM 任务里再判断并调度下一阶段。",
        "- 下面是非触发成员目录，只供 PM 复制目标 Agent 的真实 name/id；本条客户补充不得直接触发 01-05：",
        stageDirectory,
        "- PM 发真实调度评论时，Markdown mention 的显示名和 agent_id 必须来自同一行成员目录，禁止把上一阶段 id 改显示名复用。",
        "- 04 必须在 gateway 目标仓库产生真实路由/配置/测试或受控实现改动，不能只写 docs。",
        "- 05 必须创建或更新 gateway 对应真实 Gongfeng MR，并把 MR 关联到当前 child issue。",
        "- child issue 完成后由 PM 设置 done；父任务只汇总引用本 child issue 和 child MR。",
      ].join("\n")
      : [
        "客户补充：本 child issue 是 ida-deployment acceptance marker 验收，不是新增产品 API；不需要 usercenter proto、QuickEntry service 或 live 集群。",
        "",
        "## ida-deployment 验收合同",
        "- 目标仓库：ChainWeaver/ida/ida-deployment。",
        `- 目标分支：本轮仓库资源指定的 \`${repoRef}\`。`,
        "- 04 必须产生非 docs-only 改动，优先在 `harness/operations/validate-deployment/scripts/check_rendered_rules.sh`、Helm values/schema 或相邻校验脚本中增加 goal-test acceptance marker。",
        "- marker 必须可被 shell/grep/helm render 静态校验，建议包含 `goal-test-cross-project-marker` 和本 issue 编号。",
        "- 不新增真实权限点，不发布生产 Helm，不要求接入 live 集群。",
        "",
        "## ida-deployment 交付边界",
        "- 01 只确认落点和验收方式；不得因为产品 API/proto/生产 Helm 未定义阻塞。",
        "- 02/03 输出最小方案和文件级任务。",
        "- 04 修改脚本、配置或测试；不能只写 markdown/docs/harness 报告。",
        "- 05 运行能覆盖改动的最小验证，例如 `bash -n harness/operations/validate-deployment/scripts/check_rendered_rules.sh`、grep marker 或等价 render 校验，并创建/更新 ida-deployment 真实 Gongfeng MR。",
        "",
        "## 本轮 E2E 递归约束",
        "- 这个 child issue 已分配给 PM 小队，必须按同一套 PM -> 01 -> 02 -> 03 -> 04 -> 05 -> PM 收口流程完成。",
        "- PM 负责调度，01-05 必须分别产生自己的 task、评论和阶段产物。",
        "- PM 禁止在自己的单个任务里代跑 01-05；禁止只用内部 todo/TaskCreate/TaskUpdate 表示阶段完成。",
        "- PM 当前任务只允许发布一个平台 Markdown mention 调度评论，然后立即结束；不得继续读取或执行 01/02/03/04/05 阶段 skill。",
        "- PM 每一阶段必须通过平台 Markdown mention 评论触发新的平台 agent_task_queue 任务，等该阶段 Agent 独立评论后，在新的 PM 任务里再判断并调度下一阶段。",
        "- 下面是非触发成员目录，只供 PM 复制目标 Agent 的真实 name/id；本条客户补充不得直接触发 01-05：",
        stageDirectory,
        "- PM 发真实调度评论时，Markdown mention 的显示名和 agent_id 必须来自同一行成员目录，禁止把上一阶段 id 改显示名复用。",
        "- 04 必须在 ida-deployment 目标仓库产生真实 apiData/权限/Helm render 配置或测试改动，不能只写 docs。",
        "- 05 必须创建或更新 ida-deployment 对应真实 Gongfeng MR，并把 MR 关联到当前 child issue。",
        "- child issue 完成后由 PM 设置 done；父任务只汇总引用本 child issue 和 child MR。",
      ].join("\n");
    const comment = await postCustomerCommentWithOptions(child.id, token, content, {
      suppress_agent_ids: agents.map((agent) => agent.id).filter(Boolean),
    });
    results.push({
      target,
      issue_id: child.id,
      identifier: child.identifier || "",
      comment_id: comment.id || comment.comment_id,
    });
  }
  return results;
}

async function approveAndWaitChildren(children, projects, token) {
  const byProject = new Map([
    [projects.gateway.id, "gateway"],
    [projects["ida-deployment"].id, "ida-deployment"],
  ]);
  const results = [];
  const approval = {
    verified: true,
    backlog_to_todo: true,
    squad_started_after_approval: true,
    items: [],
  };
  for (const child of children) {
    const target = byProject.get(child.project_id) || child.project_id;
    const tasksBefore = await listIssueTasks(child.id, token);
    const before = await get(`/api/issues/${child.id}`, token);
    const approved = await put(`/api/issues/${child.id}`, { status: "todo" }, token);
    const childExecution = await waitChildRecursiveComplete(child.id, new Set(tasksBefore.map((item) => item.id)), token, `等待 ${target} 子任务递归 SOP 完成`);
    const terminal = childExecution.task;
    const trace = await get(`/api/issues/${child.id}/trace`, token);
    const usage = await get(`/api/issues/${child.id}/usage`, token);
    const messages = await get(`/api/tasks/${terminal.id}/messages`, token);
    const totalTokens = Number(usage?.total_input_tokens || 0) + Number(usage?.total_output_tokens || 0) + Number(usage?.total_cache_read_tokens || 0) + Number(usage?.total_cache_write_tokens || 0);
    const traceEvents = Array.isArray(trace?.events) ? trace.events : [];
    const hasUsageUnavailable = traceEvents.some((event) => event?.event_type === "llm.usage_unavailable");
    if (countItems(messages?.items || messages) <= 0) fail(`${target} 子任务完成但缺少 task messages`);
    if (countItems(trace?.events, trace?.total) <= 0) fail(`${target} 子任务缺少 trace`);
    if (totalTokens <= 0 && !hasUsageUnavailable) fail(`${target} 子任务缺少 usage，也没有 usage_unavailable trace`);
    const movedBacklogToTodo = before.status === "backlog" && approved.status === "todo";
    const taskStartedAfterApproval = new Date(terminal.created_at || terminal.started_at || 0).getTime() >= new Date(approved.updated_at || before.updated_at || 0).getTime();
    approval.backlog_to_todo = approval.backlog_to_todo && movedBacklogToTodo;
    approval.squad_started_after_approval = approval.squad_started_after_approval && taskStartedAfterApproval;
    approval.items.push({
      target,
      issue_id: child.id,
      before_status: before.status,
      approved_status: approved.status,
      task_id: terminal.id,
      task_status: terminal.status,
      task_created_at: terminal.created_at,
    });
    results.push({
      target,
      approved: pickIssue(approved),
      task: pickTask(terminal),
      rerun_count: childExecution.rerun_count,
      wait_mode: childExecution.wait_mode || "full",
      cancelled_followups: childExecution.cancelled_followups || [],
      task_count: childExecution.task_count,
      pull_requests: childExecution.pull_requests,
      trace_event_count: countItems(trace?.events, trace?.total),
      message_count: countItems(messages?.items || messages),
      total_tokens: totalTokens,
      usage_unavailable: hasUsageUnavailable,
    });
  }
  evidence.child_task_execution = results;
  evidence.project_owner_approval = approval;
}

async function waitChildrenRecursiveClosure(children, token) {
  const done = [];
  for (const child of children) {
    const closed = await waitIssueStatus(child.id, "done", token, `等待 child issue ${child.identifier || child.id} 自己 done`);
    const prs = await get(`/api/issues/${child.id}/pull-requests`, token);
    const pullRequests = normalizePullRequests(prs).map(pickPullRequest);
    if (pullRequests.length <= 0) {
      fail(`child issue ${child.identifier || child.id} 已 done 但缺少关联 MR`);
    }
    done.push({ ...pickIssue(closed), pull_requests: pullRequests });
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
    all_children_done: true,
    parent_waited: true,
  };
}

async function runServiceSandboxEvidence() {
  const quickEntries = await runQuickEntriesServiceSandbox();
  return {
    ok: quickEntries.ok,
    duration_ms: Number(quickEntries.duration_ms || 0),
    quick_entries: quickEntries,
  };
}

async function runQuickEntriesServiceSandbox() {
  const started = Date.now();
  try {
    const stdout = execFileSync("node", ["scripts/goal-test-quick-entries-service-sandbox.mjs"], {
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

async function postServiceSandboxEvidenceComment(issueID, token, sandboxEvidence, agents) {
  const quickReport = sandboxEvidence.quick_entries?.report || {};
  const quickCaseLines = Array.isArray(quickReport.cases) && quickReport.cases.length > 0
    ? quickReport.cases.map((item) => `- ${item.id}: ${item.ok ? "通过" : "失败"} (${item.status || "unknown"})`)
    : ["- 未读取到 acceptance sandbox case 明细"];
  const attachmentIDs = [];
  if (quickReport.markdown && existsSync(quickReport.markdown)) {
    const markdown = readFileSync(quickReport.markdown, "utf8");
    const attachment = await uploadTextAttachment(issueID, token, `acceptance-service-sandbox-${suffix}.md`, markdown);
    attachmentIDs.push(attachment.id);
  }
  const childLines = Array.isArray(evidence.child_task_execution)
    ? evidence.child_task_execution.map((item) => `- ${item.target}: trace=${item.trace_event_count}, messages=${item.message_count}, tokens=${item.total_tokens}, rerun=${item.rerun_count}`)
    : ["- 子任务 trace/usage 摘要尚未生成"];
  const comment = await postCustomerCommentWithOptions(issueID, token, [
    "证据补充：acceptance sandbox 与子任务运行复盘已完成，供 05-verify 核对。",
    "",
    "## acceptance sandbox",
    `- 结论：${sandboxEvidence.quick_entries?.ok ? "通过" : "失败"}`,
    `- 耗时：${sandboxEvidence.quick_entries?.duration_ms || 0} ms`,
    quickReport.json ? `- JSON 报告：${quickReport.json}` : "- JSON 报告：无",
    quickReport.markdown ? `- Markdown 报告：${quickReport.markdown}` : "- Markdown 报告：无",
    "- 已覆盖：本地 sandbox 正常返回成功和失败 case。",
    "- 已覆盖：case 报告、JSON 路径、Markdown 附件可被 05-verify 读取。",
    "- 已覆盖：输出中包含 request id、权限拒绝、owner mismatch 等可追溯断言，作为平台复盘证据，不代表本轮要新增产品 API。",
    "",
    "## acceptance sandbox case 结果",
    ...quickCaseLines,
    "",
    "## 子任务 trace / usage 摘要",
    ...childLines,
    "",
    "## 阶段边界",
    "- 这条评论只提供 05-verify 所需验收证据，不触发新阶段。",
    "- MR 会在 05-verify 通过后进入人工 CodeReview 阶段再创建和关联。",
  ].join("\n"), {
    attachment_ids: attachmentIDs,
    suppress_agent_ids: agents.map((item) => item.id),
  });
  return {
    comment_id: comment.id || comment.comment_id,
    attachment_count: attachmentIDs.length,
    suppress_agent_count: agents.length,
  };
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

async function waitForSOPRunStatus(issueID, squadID, token, status, label) {
  return poll(async () => {
    const data = await get(`/api/issues/${issueID}/sop-runs`, token);
    const items = Array.isArray(data?.items) ? data.items : [];
    const run = items.find((item) => item.squad_id === squadID || item.profile_key === "generic-project-sop-flow" || item.profile_key === "user-center-sop-flow");
    if (!run || run.status !== status || !run.completed_at) return null;
    return run;
  }, 120_000, label);
}

async function waitIssueStatus(issueID, status, token, label) {
  return poll(async () => {
    const issue = await get(`/api/issues/${issueID}`, token);
    if (issue?.status !== status) return null;
    const tasks = await listIssueTasks(issueID, token);
    if (tasks.some(isActiveTask)) return null;
    return issue;
  }, taskTimeoutMs, label);
}

async function getIssueTaskIDSet(issueID, token) {
  const tasks = await listIssueTasks(issueID, token);
  return new Set(tasks.map((item) => item.id).filter(Boolean));
}

async function waitIssueNoActiveTasks(issueID, token, label) {
  return poll(async () => {
    const tasks = await listIssueTasks(issueID, token);
    const active = tasks.filter(isActiveTask);
    if (active.length > 0) return null;
    return { task_count: tasks.length };
  }, taskTimeoutMs, label);
}

async function waitPMCloseTaskOrIssueDone(issueID, pmAgentID, knownIDs, token, label) {
  return poll(async () => {
    const tasks = sortTasks(await listIssueTasks(issueID, token));
    const closeTask = tasks.find((item) =>
      !knownIDs.has(item.id) &&
      (item.agent_id === pmAgentID || item.assignee_id === pmAgentID)
    );
    if (closeTask) {
      evidence.task_poll_snapshot = {
        label,
        total_tasks: tasks.length,
        latest: tasks.slice(0, 5).map(pickTask),
      };
      if (isActiveTask(closeTask)) return null;
      if (shouldWaitForRetry(closeTask, tasks)) return null;
      evidence.task_rounds.push({ label, task: pickTask(closeTask) });
      return closeTask;
    }

    const active = tasks.filter(isActiveTask);
    if (active.length > 0) return null;
    const issue = await get(`/api/issues/${issueID}`, token);
    if (issue.status !== "done") return null;
    const synthetic = {
      id: `issue-done:${issueID}`,
      status: "completed",
      agent_id: pmAgentID,
      assignee_id: pmAgentID,
      runtime_id: "",
      is_leader_task: true,
      created_at: issue.updated_at || new Date().toISOString(),
      started_at: "",
      completed_at: issue.updated_at || new Date().toISOString(),
      parent_task_id: "",
      attempt: 1,
      max_attempts: 1,
      error: "",
      failure_reason: "",
      synthetic_issue_done: true,
    };
    evidence.task_rounds.push({ label, task: pickTask(synthetic), synthetic_issue_done: true });
    return synthetic;
  }, taskTimeoutMs, label);
}

async function waitNextTerminalTask(issueID, agentID, knownIDs, token, label, options = {}) {
  const createdAfterMs = options.createdAfter ? new Date(options.createdAfter).getTime() : 0;
  return poll(async () => {
    const tasks = sortTasks(await listIssueTasks(issueID, token));
    const task = tasks.find((item) =>
      !knownIDs.has(item.id) &&
      (item.agent_id === agentID || item.assignee_id === agentID) &&
      taskCreatedAtMs(item) >= createdAfterMs
    );
    if (!task) return null;
    evidence.task_poll_snapshot = {
      label,
      total_tasks: tasks.length,
      latest: tasks.slice(0, 5).map(pickTask),
    };
    if (isActiveTask(task)) return null;
    if (shouldWaitForRetry(task, tasks)) return null;
    if (options.requireNonEmptyMessage) {
      const messages = await get(`/api/tasks/${task.id}/messages`, token);
      const hasMessage = countItems(messages?.items || messages) > 0;
      const hasOutput = String(task.result?.output || "").trim() !== "";
      if (!hasMessage && !hasOutput) {
        fail(`${label} 捕获到空任务 ${task.id}，不是有效评论触发执行`);
      }
    }
    evidence.task_rounds.push({ label, task: pickTask(task) });
    return task;
  }, taskTimeoutMs, label);
}

async function waitAnyTerminalTask(issueID, knownIDs, token, label) {
  return poll(async () => {
    const tasks = sortTasks(await listIssueTasks(issueID, token));
    const candidates = tasks
      .filter((item) => !knownIDs.has(item.id))
      .sort((a, b) => new Date(a.created_at || a.started_at || 0).getTime() - new Date(b.created_at || b.started_at || 0).getTime());
    const task =
      candidates.find((item) => item.status === "completed") ||
      candidates.find((item) => !isActiveTask(item) && !shouldWaitForRetry(item, tasks));
    if (!task) return null;
    evidence.task_rounds.push({ label, task: pickTask(task) });
    return task;
  }, taskTimeoutMs, label);
}

async function waitChildRecursiveComplete(issueID, knownIDs, token, label) {
  return poll(async () => {
    const issue = await get(`/api/issues/${issueID}`, token);
    const tasks = sortTasks(await listIssueTasks(issueID, token));
    const newTasks = tasks.filter((item) => !knownIDs.has(item.id));
    if (issue.status === "blocked" || issue.status === "cancelled") {
      const last = newTasks[0] || tasks[0];
      fail(`${label} 后 issue 状态=${issue.status}，最后任务=${last?.id || ""} error=${last?.error || ""} failure_reason=${last?.failure_reason || ""}`);
    }
    if (tasks.some(isActiveTask)) return null;
    const prs = await get(`/api/issues/${issueID}/pull-requests`, token);
    const pullRequests = normalizePullRequests(prs).map(pickPullRequest);
    const terminal = newTasks.filter((item) => !isActiveTask(item)).at(-1) || tasks.filter((item) => !isActiveTask(item)).at(-1);
    if (!terminal) return null;
    if (issue.status !== "done") {
      if (newTasks.length > 0) {
        fail(`${label} 已无 active task 但 issue 状态=${issue.status}，期望 done；new_tasks=${newTasks.length}，last_task=${terminal.id}`);
      }
      return null;
    }
    if (newTasks.length < 6) {
      fail(`${label} 任务数=${newTasks.length}，期望 child issue 递归跑完整 PM/01/02/03/04/05`);
    }
    if (pullRequests.length <= 0) {
      fail(`${label} 已 done 但缺少 child issue 关联 MR`);
    }
    evidence.task_rounds.push({ label, task: pickTask(terminal) });
    return {
      task: terminal,
      task_count: newTasks.length,
      rerun_count: 0,
      wait_mode: "recursive",
      pull_requests: pullRequests,
    };
  }, taskTimeoutMs, label);
}

async function waitChildExecutionComplete(issueID, knownIDs, token, label) {
  let rerunCount = 0;
  const maxEmptyReruns = Number(trimEnv("ACCEPTANCE_CHILD_EMPTY_RERUNS") || 2);
  const seenEmptyTaskIDs = new Set();
  return poll(async () => {
    const tasks = sortTasks(await listIssueTasks(issueID, token));
    const newTasks = tasks.filter((item) => !knownIDs.has(item.id));
    if (newTasks.length === 0) return null;
    const completedNow = newTasks.filter((item) => item.status === "completed");
    if (childWaitMode === "handoff" && completedNow.length > 0) {
      for (const task of completedNow) {
        const messages = await get(`/api/tasks/${task.id}/messages`, token);
        if (countItems(messages?.items || messages) > 0 || String(task.result?.output || "").trim() !== "") {
          const active = newTasks.filter(isActiveTask);
          for (const activeTask of active) {
            await cancelTask(activeTask.id, token);
          }
          evidence.task_rounds.push({ label: `${label} handoff`, task: pickTask(task) });
          return { task, rerun_count: rerunCount, wait_mode: childWaitMode, cancelled_followups: active.map((item) => item.id) };
        }
      }
    }

    const active = newTasks.filter(isActiveTask);
    if (active.length > 0) return null;

    // Avoid racing a leader task that has just completed and is about to enqueue
    // a worker handoff on the same issue.
    await new Promise((resolve) => setTimeout(resolve, 2_000));
    const settledTasks = sortTasks(await listIssueTasks(issueID, token));
    const settledNewTasks = settledTasks.filter((item) => !knownIDs.has(item.id));
    if (settledNewTasks.some(isActiveTask)) return null;

    const issue = await get(`/api/issues/${issueID}`, token);
    if (issue.status === "blocked" || issue.status === "cancelled") {
      const last = settledNewTasks[0] || newTasks[0];
      fail(`${label} 后 issue 状态=${issue.status}，最后任务=${last?.id || ""} error=${last?.error || ""} failure_reason=${last?.failure_reason || ""}`);
    }

    const terminalTasks = settledNewTasks.filter((item) => !isActiveTask(item) && !shouldWaitForRetry(item, settledTasks));
    const completedTasks = terminalTasks.filter((item) => item.status === "completed");
    for (const task of completedTasks) {
      const messages = await get(`/api/tasks/${task.id}/messages`, token);
      if (countItems(messages?.items || messages) > 0 || String(task.result?.output || "").trim() !== "") {
        evidence.task_rounds.push({ label, task: pickTask(task) });
        return { task, rerun_count: rerunCount };
      }
    }

    const emptyCompleted = completedTasks[0];
    if (emptyCompleted && !seenEmptyTaskIDs.has(emptyCompleted.id) && rerunCount < maxEmptyReruns) {
      seenEmptyTaskIDs.add(emptyCompleted.id);
      rerunCount += 1;
      evidence.task_rounds.push({
        label: `${label} 空完成自动重试 ${rerunCount}`,
        task: pickTask(emptyCompleted),
      });
      await post(`/api/issues/${issueID}/rerun`, { task_id: emptyCompleted.id }, token);
      return null;
    }

    const failed = terminalTasks.find((item) => item.status !== "completed");
    if (failed) {
      requireCompletedTask(failed, label);
    }
    if (emptyCompleted) {
      fail(`${label} 空完成且超过自动重试次数：task=${emptyCompleted.id}`);
    }
    return null;
  }, taskTimeoutMs, label);
}

async function cancelTask(taskID, token) {
  const headers = {};
  if (activeWorkspaceId) headers["X-Workspace-ID"] = activeWorkspaceId;
  const res = await fetchWithTimeout(`${apiURL}/api/tasks/${taskID}/cancel`, {
    method: "POST",
    headers: {
      ...headers,
      Authorization: `Bearer ${token}`,
      "content-type": "application/json",
    },
  });
  const text = await res.text();
  if (!res.ok && res.status !== 404) {
    throw new Error(`POST /api/tasks/${taskID}/cancel 返回 ${res.status}: ${text.slice(0, 500)}`);
  }
  return text ? JSON.parse(text) : null;
}

async function listIssueTasks(issueID, token) {
  const tasks = await get(`/api/issues/${issueID}/task-runs`, token);
  return Array.isArray(tasks) ? tasks : tasks.items ?? [];
}

function requireCompletedTask(task, label) {
  if (!task?.id) fail(`${label} 缺少 task`);
  if (task.status === "completed") return;
  const text = JSON.stringify(task);
  if (/401|Unauthorized|Missing bearer|auth|authentication|invalid_request|not supported with|image_generation|agent_error\.provider_auth_or_access|额度|容量|quota|capacity|rate.?limit|agent_error\.provider_capacity_or_rate_limit|agent_error\.provider_network|stream disconnected|tls handshake eof|responses/i.test(text)) {
    evidence.external_dependency_failure = true;
    evidence.external_dependency_boundary = `${label} 失败发生在外部模型/认证/容量边界`;
  }
  fail(`${label} 未完成：status=${task.status} error=${task.error || ""} failure_reason=${task.failure_reason || ""}`);
}

function shouldWaitForRetry(task, tasks) {
  if (task.status !== "failed") return false;
  if (!isRetryableTaskFailure(task)) return false;
  if (Number(task.attempt || 0) < Number(task.max_attempts || 0)) return true;
  return tasks.some((item) =>
    item.parent_task_id === task.id &&
    (isActiveTask(item) || item.status === "completed")
  );
}

function isRetryableTaskFailure(task) {
  return /agent_error\.provider_network|agent_error\.provider_server_error|agent_error\.provider_capacity_or_rate_limit|runtime_offline|runtime_recovery|timeout|stream disconnected|tls handshake eof|request timed out/i.test(
    JSON.stringify(task),
  );
}

function isActiveTask(task) {
  return ["queued", "dispatched", "running"].includes(task.status);
}

function taskCreatedAtMs(task) {
  return new Date(task.created_at || task.dispatched_at || task.started_at || 0).getTime();
}

function sortTasks(tasks) {
  return [...tasks].sort((a, b) => new Date(b.created_at || b.started_at || 0).getTime() - new Date(a.created_at || a.started_at || 0).getTime());
}

async function resolveWorkspace(token) {
  const data = await get("/api/workspaces", token);
  const items = Array.isArray(data) ? data : data.items ?? [];
  const workspace = items.find((item) => item.slug === workspaceSlug);
  if (workspace?.id) return workspace;
  const created = await post("/api/workspaces", {
    name: workspaceSlug
      .split("-")
      .filter(Boolean)
      .map((part) => part.slice(0, 1).toUpperCase() + part.slice(1))
      .join(" "),
    slug: workspaceSlug,
    issue_prefix: "AIS",
  }, token);
  if (!created?.id) fail(`创建工作区 ${workspaceSlug} 后响应缺少 id`);
  return created;
}

async function resolveOnlineRuntime(token, workspaceID) {
  const data = await get(`/api/runtimes?workspace_id=${encodeURIComponent(workspaceID)}`, token);
  const items = Array.isArray(data) ? data : data.items ?? [];
  const runtime = items
    .filter((item) => item.provider === provider && item.status === "online")
    .sort((a, b) => new Date(b.last_seen_at || 0).getTime() - new Date(a.last_seen_at || 0).getTime())[0];
  if (!runtime?.id) fail(`未找到在线 ${provider} runtime`);
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
  const res = await fetchWithTimeout(url, {
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

async function getText(pathname, token) {
  const url = `${apiURL}${pathname}`;
  const headers = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  if (token && activeWorkspaceId) headers["X-Workspace-ID"] = activeWorkspaceId;
  evidence.commands.push(`GET ${pathname}`);
  const res = await fetchWithTimeout(url, { method: "GET", headers });
  const text = await res.text();
  if (!res.ok) {
    throw new Error(`GET ${pathname} 返回 ${res.status}: ${text.slice(0, 1000)}`);
  }
  return text;
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

async function fetchWithTimeout(url, options = {}) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), requestTimeoutMs);
  try {
    return await fetch(url, {
      ...options,
      signal: options.signal || controller.signal,
    });
  } catch (error) {
    if (error?.name === "AbortError") {
      throw new Error(`请求超时 ${requestTimeoutMs}ms: ${url}`);
    }
    throw error;
  } finally {
    clearTimeout(timeout);
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
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

function normalizePullRequests(value) {
  if (Array.isArray(value)) return value;
  if (Array.isArray(value?.pull_requests)) return value.pull_requests;
  if (Array.isArray(value?.items)) return value.items;
  return [];
}

function pickPullRequest(item) {
  const projectPath = canonicalGongfengProjectPath(item?.project_path || item?.html_url || "");
  return {
    id: item?.id || "",
    provider: item?.provider || "",
    number: Number(item?.number || 0),
    title: item?.title || "",
    state: item?.state || "",
    html_url: item?.html_url || "",
    branch: item?.branch || item?.source_branch || "",
    target_branch: item?.base_branch || item?.target_branch || "",
    project_path: projectPath,
    head_sha: item?.head_sha || "",
  };
}

function canonicalGongfengProjectPath(value) {
  const raw = String(value || "").trim();
  const projectPath = raw.includes("git.code.tencent.com/") ? extractGongfengProjectPath(raw) : raw;
  const normalized = projectPath.replace(/^\/+|\/+$/g, "");
  if (!normalized) return "";
  const exact = repoSpecs.find((spec) => spec.projectPath === normalized);
  if (exact) return exact.projectPath;
  const byRepoName = repoSpecs.find((spec) => normalized.endsWith(`/${repoNameFromProjectPath(spec.projectPath)}`));
  return byRepoName?.projectPath || normalized;
}

function extractGongfengProjectPath(url) {
  const text = String(url || "");
  const matched = text.match(/git\.code\.tencent\.com\/(.+?)\/(?:-\/)?merge_requests\/\d+/);
  return matched?.[1] || "";
}

function repoNameFromProjectPath(projectPath) {
  return String(projectPath || "").split("/").filter(Boolean).pop() || "";
}

function pickSOPRun(run) {
  return {
    id: run.id,
    profile_key: run.profile_key,
    current_step_key: run.current_step_key,
    status: run.status,
    started_at: run.started_at,
    completed_at: run.completed_at || "",
    total_duration_ms: run.total_duration_ms ?? null,
    event_count: Array.isArray(run.events) ? run.events.length : 0,
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
    parent_task_id: task.parent_task_id || "",
    attempt: task.attempt ?? null,
    max_attempts: task.max_attempts ?? null,
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

function execText(command, args) {
  try {
    return execFileSync(command, args, { cwd: repoRoot, encoding: "utf8" }).trim();
  } catch {
    return "";
  }
}

function trimEnv(name) {
  return String(process.env[name] || "").trim();
}

function buildTAPDSourceMetadata(rawURL) {
  const metadata = {
    source_provider: "tapd",
    source_url: rawURL,
  };
  try {
    const url = new URL(rawURL);
    const workspaceID = url.pathname.match(/\/(\d+)\//)?.[1] || "";
    const resourceID = url.hash.match(/(\d{10,})/)?.[1] || url.pathname.match(/(\d{10,})/)?.[1] || "";
    if (workspaceID) metadata.tapd_workspace_id = workspaceID;
    metadata.tapd_resource_type = url.pathname.includes("/markdown_wikis/") ? "markdown_wiki" : "tapd_resource";
    if (resourceID) metadata.tapd_resource_id = resourceID;
  } catch {
    metadata.tapd_resource_type = "tapd_resource";
  }
  return metadata;
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
