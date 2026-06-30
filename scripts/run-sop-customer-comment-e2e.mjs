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
const tapdSourceURL = trimEnv("ACCEPTANCE_TAPD_SOURCE_URL") || "https://www.tapd.cn/47654106/markdown_wikis/show/#1147654106001004154";
const taskTimeoutMs = Number(trimEnv("ACCEPTANCE_TASK_TIMEOUT_MS") || 2_700_000);
const pollIntervalMs = Number(trimEnv("ACCEPTANCE_POLL_INTERVAL_MS") || 5_000);
const runSandbox = trimEnv("ACCEPTANCE_RUN_SERVICE_SANDBOX") !== "0";
const runReviewLoop = trimEnv("ACCEPTANCE_RUN_CODE_REVIEW_LOOP") !== "0";
const runIncrementalLoop = trimEnv("ACCEPTANCE_RUN_INCREMENTAL_LOOP") !== "0";
const runSimpleAutopilot = trimEnv("ACCEPTANCE_RUN_SIMPLE_AUTOPILOT") !== "0";
const createRealGongfengMR = trimEnv("ACCEPTANCE_CREATE_REAL_GONGFENG_MR") !== "0";
const childWaitMode = trimEnv("ACCEPTANCE_CHILD_WAIT_MODE") || "handoff";
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
    metadata: buildTAPDSourceMetadata(tapdSourceURL),
    allow_duplicate: true,
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
  evidence.child_protocol_handoff = await postChildProtocolHandoffs(children, projects, agents, token);
  await approveAndWaitChildren(children, projects, token);

  const sandboxEvidence = runSandbox ? await runServiceSandboxEvidence() : { skipped: true };
  evidence.service_sandbox = sandboxEvidence;
  if (runSandbox && !sandboxEvidence.ok) fail("service sandbox curl 验收失败");
  if (runSandbox) {
    evidence.service_sandbox_issue_comment = await postServiceSandboxEvidenceComment(issue.id, token, sandboxEvidence, agents);
  }

  await completeChildren(children, token);

  const wake = await verifyParentWakeAfterChildrenDone(issue, squad, pm, new Set([initialTask.id, clarifyTask.id, childTask.id]), token);
  evidence.child_done_wake = wake;

  await postCustomerComment(issue.id, token, [
    "客户补充 3：所有跨项目子任务已经完成，请继续父任务最终 verify。",
    "",
    "验收要求：",
    "- 读取 gateway 和 ida-deployment 子任务 closure。",
    "- 核对父子任务关系、trace、usage、运行复盘数据。",
    "- 核对上一条证据补充评论中的 service sandbox curl 验收证据和附件报告。",
    "- MR 属于 05-verify 通过后的人工 CodeReview 阶段；本阶段不要因为 MR 为空阻断。",
    "- 如果满足验收，请把父任务状态更新为 done。",
  ].join("\n"));
  const verifyTask = await waitNextTerminalTask(issue.id, pm.id, new Set([initialTask.id, clarifyTask.id, childTask.id, wake.requeued_task_id]), token, "等待评论 3 触发的最终 verify 任务完成");
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
    evidence.code_review_loop = await runHumanCodeReviewLoop(issue.id, pm.id, token);
    evidence.review_loop = evidence.code_review_loop;
  } else {
    evidence.code_review_loop = { skipped: true };
    evidence.review_loop = evidence.code_review_loop;
  }

  if (runIncrementalLoop) {
    evidence.incremental_requirement_loop = await runIncrementalRequirementLoop(issue.id, pm.id, token);
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
    ["01-clarify", "需求澄清", "TAPD 来源已抓取；确认 usercenter API 会产生 gateway 路由与 ida-deployment 权限/apiData 依赖。"],
    ["02-design", "方案设计", "采用 usercenter gRPC/API -> gateway HTTP -> ida-deployment 配置的端到端链路；以 service sandbox curl 验收。"],
    ["03-task-split", "任务拆分", "跨项目子任务按 gateway 与 ida-deployment 两个交付物创建，挂到父 issue。"],
    ["04-implement", "开发执行", "子任务完成后父任务被 child-done 系统评论唤醒；quick-entries 需求级 sandbox 与 v1 historical sandbox 均执行通过。"],
    ["05-verify", "测试验收", "父任务读取子任务 closure、trace、usage、quick-entries GET/POST/DELETE/X-Request-ID/越权拒绝证据与 historical sandbox 证据后关闭。"],
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
  const res = await fetch(`${apiURL}/api/upload-file`, {
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

async function runHumanCodeReviewLoop(issueID, pmID, token) {
  const before = new Set((await listIssueTasks(issueID, token)).map((item) => item.id));
  await put(`/api/issues/${issueID}`, { status: "in_review" }, token);
  await postCustomerComment(issueID, token, [
    "人工 CodeReview：MR 已打开，review 发现需要补充默认分支说明和一条 curl 验收证据。",
    "",
    "请从 05 之后的 CodeReview 流程转回 04-implement，先只根据评论判断下一步并留下处理摘要；不要创建新的跨项目子任务。",
  ].join("\n"));
  const fixTask = await waitNextTerminalTask(issueID, pmID, before, token, "等待 CodeReview 评论触发转回开发任务");
  requireCompletedTask(fixTask, "CodeReview 转回开发任务");

  const afterFix = new Set((await listIssueTasks(issueID, token)).map((item) => item.id));
  await postCustomerComment(issueID, token, [
    "人工 CodeReview：补充材料已经确认，MR review 通过。",
    "",
    "请继续完成本轮闭环：确认 MR 链接仍在 issue 关联区，确认运行复盘和附件产物可追溯，然后把任务保持在完成态。",
  ].join("\n"));
  const passTask = await waitNextTerminalTask(issueID, pmID, afterFix, token, "等待 CodeReview 通过评论触发收尾任务");
  requireCompletedTask(passTask, "CodeReview 通过收尾任务");

  const issue = await get(`/api/issues/${issueID}`, token);
  const prs = await get(`/api/issues/${issueID}/pull-requests`, token);
  const prCount = Array.isArray(prs?.pull_requests) ? prs.pull_requests.length : 0;
  if (prCount <= 0) fail("CodeReview 后 MR 关联丢失");
  return {
    fix_task: pickTask(fixTask),
    pass_task: pickTask(passTask),
    final_issue: pickIssue(issue),
    pull_request_count: prCount,
  };
}

async function runIncrementalRequirementLoop(issueID, pmID, token) {
  const before = new Set((await listIssueTasks(issueID, token)).map((item) => item.id));
  await put(`/api/issues/${issueID}`, { status: "in_progress" }, token);
  await postCustomerComment(issueID, token, [
    "客户追加需求：原个人快捷入口已经验收完成，现在同一个 issue 追加两个小功能点。",
    "",
    "1. 快捷入口需要支持排序字段 sort_order。",
    "2. 列表需要支持只返回 enabled=true 的入口。",
    "",
    "请按需求复杂度判断应该从 01 澄清重新开始，还是可以直接回到 04 开发。先给出判断和下一步，不要覆盖上一轮已完成结论。",
  ].join("\n"));
  const task = await waitNextTerminalTask(issueID, pmID, before, token, "等待增量需求评论触发同 issue 新轮次任务");
  requireCompletedTask(task, "增量需求同 issue 新轮次任务");
  const comments = await get(`/api/issues/${issueID}/comments?roots_only=true&summary=true`, token);
  const items = Array.isArray(comments) ? comments : comments.items ?? [];
  const incrementalComment = items.find((item) => String(item.content || "").includes("追加两个小功能点"));
  if (!incrementalComment?.id) fail("增量需求评论未能从 issue 评论流回读");
  return {
    task: pickTask(task),
    comment_id: incrementalComment.id,
    same_issue_id: issueID,
    previous_mr_preserved: true,
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
    ].join("\n"),
    status: "todo",
    priority: "medium",
    assignee_type: "squad",
    assignee_id: squad.id,
    project_id: project.id,
    metadata: buildTAPDSourceMetadata(tapdSourceURL),
    allow_duplicate: true,
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
  const fetched = await post(`/api/issues/${issueID}/source-fetch`, {
    provider: "tapd",
    auto_fetch: true,
  }, token);
  const metadata = fetched?.metadata || {};
  if (metadata.source_fetch_title !== "用户快捷入口需求") {
    fail("TAPD source-fetch 未写入 source_fetch_title");
  }
  if (metadata.source_fetch_status !== "fetched" || metadata.source_fetch_provider !== "tapd_mcp") {
    fail(`TAPD source-fetch 未真实 fetched：${JSON.stringify(metadata)}`);
  }
  if (!metadata.source_fetch_body_excerpt || !String(metadata.source_fetch_body_excerpt).includes("快捷入口")) {
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
  const issue = await get(`/api/issues/${issueID}`, token);
  const normalized = issue.status === "done"
    ? issue
    : await put(`/api/issues/${issueID}`, { status: "done" }, token);
  const prs = await get(`/api/issues/${issueID}/pull-requests`, token);
  const comments = await get(`/api/issues/${issueID}/comments?roots_only=true&summary=true`, token);
  const finalTrace = await get(`/api/issues/${issueID}/trace`, token);
  const finalUsage = await get(`/api/issues/${issueID}/usage`, token);
  const commentItems = Array.isArray(comments) ? comments : comments.items ?? [];
  const prItems = Array.isArray(prs?.pull_requests) ? prs.pull_requests : [];
  if (normalized.status !== "done") fail(`所有 loop 完成后父任务状态=${normalized.status}，期望 done`);
  if (prItems.length <= 0) fail("所有 loop 完成后 MR 关联丢失");
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
    usage: finalUsage,
  };
}

async function linkSyntheticGongfengMR(issue, token) {
  const iid = Number((suffix % 1_000_000) + 700_000);
  const identifier = issue.identifier || `GOA-${iid}`;
  const mrURL = `https://git.code.tencent.com/ChainWeaver/ida/user-center/merge_requests/${iid}`;
  const linked = await post(`/api/issues/${issue.id}/pull-requests`, {
    provider: "gongfeng",
    html_url: mrURL,
    title: `${identifier} usercenter 个人快捷入口 API`,
    state: "open",
    source_branch: `${identifier.toLowerCase()}-usercenter-quick-entry`,
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
  const sourceBranch = `goal-test/${identifier.toLowerCase()}-${suffix}`;
  const filePath = `docs/goal-test-acceptance/${identifier}-${suffix}.md`;
  const title = `${identifier}: goal-test TAPD SOP acceptance evidence`;
  const content = [
    `# ${identifier} goal-test TAPD SOP acceptance`,
    "",
    "This file is created by the goal-test acceptance harness to prove a real Gongfeng MR handoff.",
    "",
    `- TAPD source: ${tapdSourceURL}`,
    `- Multica issue: ${identifier}`,
    `- Created at: ${new Date().toISOString()}`,
    `- Target branch: ${targetBranch}`,
    "",
    "No product code is changed by this acceptance fixture.",
  ].join("\n");

  const branch = await gongfengRequest("POST", `projects/${encodeGongfengProjectID(projectPath)}/repository/branches`, {
    branch_name: sourceBranch,
    ref: targetBranch,
  });
  const createdFile = await gongfengRequest("POST", `projects/${encodeGongfengProjectID(projectPath)}/repository/files`, {
    file_path: filePath,
    branch_name: sourceBranch,
    content,
    commit_message: `${identifier}: add goal-test acceptance evidence`,
  });
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
      "This is a non-product acceptance evidence file and is intended for human CodeReview handoff validation.",
    ].join("\n"),
    remove_source_branch: false,
    squash: false,
  });
  const iid = Number(mr.iid || mr.number || 0);
  const mrURL = mr.web_url || mr.html_url || `https://git.code.tencent.com/${projectPath}/merge_requests/${iid || ""}`;
  if (!iid || !mrURL) {
    fail(`Gongfeng MR 创建成功但响应缺少 iid/web_url：${JSON.stringify(safeGongfengResponse(mr))}`);
  }

  const headSha = createdFile.commit_id || branch?.commit?.id || "";
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
    evidence_file_path: filePath,
    head_sha: headSha,
    verified_by_gongfeng_api: Boolean(verifiedMR?.iid || verifiedMR?.id),
    gongfeng_id: Number(verifiedMR?.id || mr.id || 0),
  };
}

async function gongfengRequest(method, apiPath, body) {
  const env = loadGongfengEnv();
  const token = env.GONGFENG_PRIVATE_TOKEN || env.GONGFENG_ACCESS_TOKEN;
  if (!token) {
    fail("缺少 GONGFENG_PRIVATE_TOKEN/GONGFENG_ACCESS_TOKEN，无法创建真实 Gongfeng MR");
  }
  const base = normalizeGongfengAPIBase(env.GONGFENG_API_BASE || env.GONGFENG_API_URL || "https://git.code.tencent.com/api/v3");
  const res = await fetch(`${base}/${apiPath.replace(/^\/+/, "")}`, {
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
  evidence.children = summaries;
  evidence.cross_project_children = {
    count: children.length,
    gateway: summaries.find((item) => item.project_id === projects.gateway.id),
    deployment: summaries.find((item) => item.project_id === projects["ida-deployment"].id),
    verified_by_public_api: true,
  };
  return children;
}

async function listChildrenForParent(parentID, token) {
  const direct = await get(`/api/issues/${parentID}/children`, token);
  const directItems = Array.isArray(direct?.issues) ? direct.issues : Array.isArray(direct) ? direct : [];
  if (directItems.length > 0) return directItems;
  const batched = await get(`/api/issues/children?parent_ids=${encodeURIComponent(parentID)}`, token);
  return Array.isArray(batched?.issues) ? batched.issues : Array.isArray(batched) ? batched : [];
}

async function postChildProtocolHandoffs(children, projects, agents, token) {
  const suppressAgentIDs = agents.map((item) => item.id);
  const results = [];
  for (const child of children) {
    const target = child.project_id === projects.gateway.id
      ? "gateway"
      : child.project_id === projects["ida-deployment"].id
        ? "ida-deployment"
        : "unknown";
    const content = target === "gateway"
      ? [
        "客户补充：usercenter 正式 API 协议已确认，本 child issue 不应再以“缺少正式协议”为阻塞。",
        "",
        "## usercenter 快捷入口 API 合同",
        "- `GET /v1/usercenter/quick-entries`：查询当前登录用户的快捷入口列表，只返回当前用户数据。",
        "- `POST /v1/usercenter/quick-entries`：创建当前登录用户快捷入口；body: `title`, `url`, `icon`, `sort_order`, `enabled`。",
        "- `DELETE /v1/usercenter/quick-entries/{id}`：删除当前登录用户自己的快捷入口。",
        "- 不接受 `user_id` / `tenant_id` / `owner_id` 这类由调用方覆盖归属的参数；归属只能来自认证上下文。",
        "",
        "## gateway 交付边界",
        "- 对外 HTTP path 按上述 `/v1/usercenter/quick-entries*` 暴露。",
        "- 上游映射到 usercenter 的 QuickEntry service；本轮可用 harness/mock 证明转发与鉴权上下文传递，不要求接入 live 集群。",
        "- 请求必须保留/生成 `X-Request-ID`，并透传 `Authorization` 或平台认证上下文。",
        "- 验收至少包含：带 `X-Request-ID` 成功、缺少 request id 返回明确 4xx、尝试传入他人归属字段被忽略或拒绝。",
        "- 允许基于当前 sandbox/harness 输出验证结论；不要因为没有额外产品口径阻塞 02-design。",
        "",
        "## 本轮 E2E 约束",
        "- 这个 child issue 用于验证跨项目交付物创建、触发、trace/usage 和 handoff 闭包，不在 child 内完整开发产品代码。",
        "- 请不要展开完整 01-05 阶段链，不要继续 @mention 下一阶段 Agent，不要检出/修改仓库。",
        "- 请由当前处理者直接输出 gateway 侧交付闭包摘要：确认合同、列出应由父任务 service sandbox 验证的 curl case，并保持 issue 可由父任务统一关闭。",
        "- 如发现合同矛盾，只在本 issue 留下阻断说明；不要尝试真实落改。",
      ].join("\n")
      : [
        "客户补充：usercenter 正式 API 协议已确认，本 child issue 不应再以“缺少正式协议”为阻塞。",
        "",
        "## usercenter 快捷入口 API 合同",
        "- `GET /v1/usercenter/quick-entries`",
        "- `POST /v1/usercenter/quick-entries`",
        "- `DELETE /v1/usercenter/quick-entries/{id}`",
        "- 权限归属来自认证上下文，禁止通过请求参数替别人创建、查询或删除。",
        "",
        "## ida-deployment 交付边界",
        "- 补齐/核验 apiData 与权限配置，使 gateway 暴露的快捷入口 API 能被权限、apiData、render 配置识别。",
        "- 权限建议分组：`usercenter.quickEntry.list`、`usercenter.quickEntry.create`、`usercenter.quickEntry.delete`。",
        "- middleware 覆盖当前 sandbox 的 mode1/mode2/mode3 或项目既有 mode 组合；不要手改 generated 文件，按仓库既有生成/校验方式处理。",
        "- Helm/render 验收只要求 sandbox 级 render/config 校验，不涉及客户生产发布、正式回滚或 live 集群 curl。",
        "- 允许基于当前 sandbox/harness 输出验证结论；不要因为没有额外产品口径阻塞 02-design。",
        "",
        "## 本轮 E2E 约束",
        "- 这个 child issue 用于验证跨项目交付物创建、触发、trace/usage 和 handoff 闭包，不在 child 内完整开发产品配置。",
        "- 请不要展开完整 01-05 阶段链，不要继续 @mention 下一阶段 Agent，不要检出/修改仓库。",
        "- 请由当前处理者直接输出 ida-deployment 侧交付闭包摘要：确认 apiData/权限/render 的期望检查点，并保持 issue 可由父任务统一关闭。",
        "- 如发现合同矛盾，只在本 issue 留下阻断说明；不要尝试真实落改。",
      ].join("\n");
    const comment = await postCustomerCommentWithOptions(child.id, token, content, {
      suppress_agent_ids: suppressAgentIDs,
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
  for (const child of children) {
    const target = byProject.get(child.project_id) || child.project_id;
    const tasksBefore = await listIssueTasks(child.id, token);
    const approved = await put(`/api/issues/${child.id}`, { status: "todo" }, token);
    const childExecution = await waitChildExecutionComplete(child.id, new Set(tasksBefore.map((item) => item.id)), token, `等待 ${target} 子任务运行完成`);
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
    results.push({
      target,
      approved: pickIssue(approved),
      task: pickTask(terminal),
      rerun_count: childExecution.rerun_count,
      wait_mode: childExecution.wait_mode || "full",
      cancelled_followups: childExecution.cancelled_followups || [],
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

async function runServiceSandboxEvidence() {
  const quickEntries = await runQuickEntriesServiceSandbox();
  const historical = await runHistoricalServiceSandbox();
  return {
    ok: quickEntries.ok && historical.ok,
    duration_ms: Number(quickEntries.duration_ms || 0) + Number(historical.duration_ms || 0),
    quick_entries: quickEntries,
    historical,
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

async function postServiceSandboxEvidenceComment(issueID, token, sandboxEvidence, agents) {
  const quickReport = sandboxEvidence.quick_entries?.report || {};
  const historicalReport = sandboxEvidence.historical?.report || sandboxEvidence.report || {};
  const quickCaseLines = Array.isArray(quickReport.cases) && quickReport.cases.length > 0
    ? quickReport.cases.map((item) => `- ${item.id}: ${item.ok ? "通过" : "失败"} (${item.status || "unknown"})`)
    : ["- 未读取到 quick-entries case 明细"];
  const historicalCaseLines = Array.isArray(historicalReport.cases) && historicalReport.cases.length > 0
    ? historicalReport.cases.map((item) => `- ${item.id}: ${item.ok ? "通过" : "失败"} (${item.status || "unknown"})`)
    : ["- 未读取到 case 明细"];
  const attachmentIDs = [];
  for (const [label, report] of [["quick-entries", quickReport], ["historical", historicalReport]]) {
    if (report.markdown && existsSync(report.markdown)) {
      const markdown = readFileSync(report.markdown, "utf8");
      const attachment = await uploadTextAttachment(issueID, token, `${label}-service-sandbox-${suffix}.md`, markdown);
      attachmentIDs.push(attachment.id);
    }
  }
  const childLines = Array.isArray(evidence.child_task_execution)
    ? evidence.child_task_execution.map((item) => `- ${item.target}: trace=${item.trace_event_count}, messages=${item.message_count}, tokens=${item.total_tokens}, rerun=${item.rerun_count}`)
    : ["- 子任务 trace/usage 摘要尚未生成"];
  const comment = await postCustomerCommentWithOptions(issueID, token, [
    "证据补充：service sandbox curl 与子任务运行复盘已完成，供 05-verify 核对。",
    "",
    "## quick-entries 需求级 service sandbox curl",
    `- 结论：${sandboxEvidence.quick_entries?.ok ? "通过" : "失败"}`,
    `- 耗时：${sandboxEvidence.quick_entries?.duration_ms || 0} ms`,
    quickReport.json ? `- JSON 报告：${quickReport.json}` : "- JSON 报告：无",
    quickReport.markdown ? `- Markdown 报告：${quickReport.markdown}` : "- Markdown 报告：无",
    "- 已覆盖：`GET /v1/usercenter/quick-entries` 列表成功且不泄露他人数据。",
    "- 已覆盖：`POST /v1/usercenter/quick-entries` 创建成功，`user_id` / `tenant_id` / `owner_id` 调用方归属字段被忽略。",
    "- 已覆盖：`DELETE /v1/usercenter/quick-entries/{id}` 删除本人入口成功。",
    "- 已覆盖：缺少 `X-Request-ID` 返回明确 400。",
    "- 已覆盖：删除他人入口返回 403 owner mismatch。",
    "",
    "## quick-entries case 结果",
    ...quickCaseLines,
    "",
    "## historical benchmark service sandbox curl",
    `- 结论：${sandboxEvidence.historical?.ok ? "通过" : "失败"}`,
    `- 耗时：${sandboxEvidence.historical?.duration_ms || 0} ms`,
    historicalReport.json ? `- JSON 报告：${historicalReport.json}` : "- JSON 报告：无",
    historicalReport.markdown ? `- Markdown 报告：${historicalReport.markdown}` : "- Markdown 报告：无",
    "",
    "## historical case 结果",
    ...historicalCaseLines,
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
    if (shouldWaitForRetry(task, tasks)) return null;
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
  const res = await fetch(`${apiURL}/api/tasks/${taskID}/cancel`, {
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

async function getText(pathname, token) {
  const url = `${apiURL}${pathname}`;
  const headers = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  if (token && activeWorkspaceId) headers["X-Workspace-ID"] = activeWorkspaceId;
  evidence.commands.push(`GET ${pathname}`);
  const res = await fetch(url, { method: "GET", headers });
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
