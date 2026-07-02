#!/usr/bin/env node

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import pg from "pg";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const outputDir = acceptanceDir(repoRoot);
const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("GOAL_TEST_INT_API_URL") || "http://127.0.0.1:18762";
const databaseURL = trimEnv("GOAL_TEST_DATABASE_URL") || readGoalTestDatabaseURLForAPI(apiURL) || trimEnv("DATABASE_URL") || "postgres://multica:multica@9.134.129.162:5432/multica_goal_test_int?sslmode=disable";
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || "develop";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || "develop123";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || "ai-studio";
const provider = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER") || "codebuddy";
const model = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL") || "deepseek-v4-pro-ioa";
const sopAgentCustomArgs = [];
const taskTimeoutMs = Number(trimEnv("PASSWORD_SOP_TASK_TIMEOUT_MS") || "600000");
const pollIntervalMs = Number(trimEnv("PASSWORD_SOP_POLL_INTERVAL_MS") || "5000");
const httpRetries = Number(trimEnv("PASSWORD_SOP_HTTP_RETRIES") || "5");
const maxSOPTasks = Number(trimEnv("PASSWORD_SOP_MAX_TASKS") || "14");
const runID = trimEnv("PASSWORD_SOP_RUN_ID") || `password-strength-${Date.now()}`;
const tapdURL = "https://www.tapd.cn/47654106/markdown_wikis/show/#1147654106001004223";
const tapdBody = '修改密码时，密码强度 8-32 位，至少包含3种字符类型（大写字母、小写字母、数字、特殊字符），特殊字符只包含："!@#$%^&*()_+|~=`{}[]:";\'<>?,./"。';
const gongfengProjectPath = trimEnv("PASSWORD_SOP_GONGFENG_PROJECT_PATH") || "ChainWeaver/ida/user-center";
const gongfengTargetBranch = trimEnv("PASSWORD_SOP_GONGFENG_TARGET_BRANCH") || "v5.0.0_dev_sop";
const safeRunID = runID.replace(/[^a-zA-Z0-9._-]+/g, "-").slice(0, 80);
const gongfengSourceBranch = trimEnv("PASSWORD_SOP_GONGFENG_SOURCE_BRANCH") || `agent/password-strength-${safeRunID}`;
const gongfengEvidenceDir = `docs/ai-studio-sop-acceptance/${safeRunID}`;
const gongfengEvidenceFile = `${gongfengEvidenceDir}/acceptance.md`;
const legacyFlatEvidenceFile = `docs/ai-studio-sop-acceptance/${safeRunID}.md`;

const evidence = {
  schema: "multica.password_strength_sop_e2e.v1",
  generated_at: new Date().toISOString(),
  run_id: runID,
  api_url: apiURL,
  workspace_slug: workspaceSlug,
  provider,
  model,
  tapd: {
    url: tapdURL,
    workspace_id: "47654106",
    resource_type: "markdown_wiki",
    resource_id: "1147654106001004223",
    title: "增强密码强度",
    version: "2026-07-02 01:07:27",
  },
  repository: {
    provider: "gongfeng",
    project_path: gongfengProjectPath,
    target_branch: gongfengTargetBranch,
    source_branch: gongfengSourceBranch,
    evidence_dir: gongfengEvidenceDir,
    evidence_file: gongfengEvidenceFile,
  },
  checks: [],
  pm_route_recoveries: [],
  stages: [],
  ok: false,
};

let token = "";
let workspace = null;
let issue = null;

try {
  const login = await post("/auth/login", { account, password }, null);
  token = login.token;
  if (!token) fail("login response missing token");
  const currentUser = login.user?.id ? login.user : await get("/api/me");
  evidence.user = pick(currentUser, ["id", "account", "name"]);

  workspace = await resolveWorkspace();
  evidence.workspace = pick(workspace, ["id", "slug", "name"]);
  await assertNoActiveTasks("preflight");

  const runtime = await resolveRuntime();
  evidence.runtime = pick(runtime, ["id", "name", "provider", "status", "last_seen_at"]);

  const project = await ensureProject(currentUser);
  evidence.project = pick(project, ["id", "title", "status"]);

  const agents = await createStageAgents(runtime);
  evidence.agents = Object.fromEntries(Object.entries(agents).map(([key, agent]) => [key, pick(agent, ["id", "name", "runtime_id", "model"])]));
  const squad = await createSOPSquad(agents);
  evidence.squad = pick(squad, ["id", "name", "leader_id", "scope"]);
  await addSquadMembers(squad, agents);

  issue = await createPasswordIssue(project, squad, currentUser);
  evidence.issue = pickIssue(issue);

  const fetchFailed = await post(`/api/issues/${issue.id}/source-fetch`, {
    provider: "tapd",
    fetch_provider: "tapd_mcp",
    status: "fetch_failed",
    url: tapdURL,
    workspace_id: evidence.tapd.workspace_id,
    resource_type: evidence.tapd.resource_type,
    resource_id: evidence.tapd.resource_id,
    error: "TAPD MCP returned 401 unauthorized during password-strength acceptance",
    duration_ms: 321,
  });
  evidence.source_fetch_failed = {
    metadata: pickSourceFetch(fetchFailed?.metadata || {}),
    trace_event_id: fetchFailed?.trace_event?.id || "",
  };
  check("source_fetch_failed_recorded", fetchFailed?.metadata?.source_fetch_status === "fetch_failed", evidence.source_fetch_failed);

  const recovery = await postComment([
    "人工恢复 TAPD 需求内容：",
    "",
    `- 标题：${evidence.tapd.title}`,
    `- 版本：${evidence.tapd.version}`,
    `- 链接：${tapdURL}`,
    "",
    "正文：",
    tapdBody,
    "",
    "请后续 SOP 阶段以这条评论作为 TAPD fetch_failed 后的人工恢复来源，不要再把登录页当需求内容。",
  ].join("\n"));
  evidence.manual_recovery_comment = pickComment(recovery);
  check("manual_recovery_comment_created", Boolean(recovery?.id), evidence.manual_recovery_comment);

  const knownTasks = new Set();
  await updateIssue(issue.id, { status: "todo", assignee_type: "squad", assignee_id: squad.id });
  await runNaturalSOPFlow(agents, knownTasks);

  const finalIssue = await get(`/api/issues/${issue.id}`);
  evidence.final_issue = pickIssue(finalIssue);
  check("issue_in_review", finalIssue.status === "in_review", evidence.final_issue);

  const linkedMRs = await get(`/api/issues/${issue.id}/pull-requests`);
  evidence.linked_pull_requests = normalizePullRequests(linkedMRs).map(pickPullRequest);
  check("platform_mr_linked", evidence.linked_pull_requests.some((item) =>
    item.html_url &&
    item.html_url.includes("/merge_requests/") &&
    item.number > 0
  ), {
    expected_branch: gongfengSourceBranch,
    linked_pull_requests: evidence.linked_pull_requests,
  });

  const comments = normalizeList(await get(`/api/issues/${issue.id}/comments`));
  evidence.comment_summary = summarizeComments(comments);
  const artifactComments = comments.filter((item) => Array.isArray(item.attachments) && item.attachments.length > 0);
  evidence.artifact_comments = artifactComments.map(pickComment);
  check("auto_artifact_comments_present", artifactComments.length >= 5, { count: artifactComments.length });

  const attachmentRefs = comments.flatMap((comment) => (Array.isArray(comment.attachments) ? comment.attachments : [])
    .filter((att) => att.id)
    .map((att) => ({ id: att.id, filename: att.filename || "", source_task_id: comment.source_task_id || "" })));
  evidence.attachments = [];
  const attachmentTexts = [];
  for (const { id, filename, source_task_id } of attachmentRefs) {
    const text = await getText(`/api/attachments/${id}/content`);
    attachmentTexts.push(text);
    evidence.attachments.push({ id, filename, source_task_id, preview_ok: text.includes("增强密码强度"), excerpt: text.slice(0, 180) });
  }
  const repositoryEvidenceText = [
    JSON.stringify(evidence.linked_pull_requests),
    comments.map((comment) => String(comment.content || "")).join("\n"),
    attachmentTexts.join("\n"),
  ].join("\n");
  evidence.repository_acceptance_file = {
    expected_file: gongfengEvidenceFile,
    expected_dir: gongfengEvidenceDir,
    forbidden_legacy_file: legacyFlatEvidenceFile,
    expected_file_mentioned: repositoryEvidenceText.includes(gongfengEvidenceFile),
    forbidden_legacy_file_mentioned: repositoryEvidenceText.includes(legacyFlatEvidenceFile),
  };
  check(
    "repository_acceptance_file_run_scoped",
    evidence.repository_acceptance_file.expected_file_mentioned &&
      !evidence.repository_acceptance_file.forbidden_legacy_file_mentioned,
    evidence.repository_acceptance_file,
  );
  const presentFilenames = new Set(evidence.attachments.map((item) => item.filename).filter(Boolean));
  const requiredStageTaskIDs = evidence.stages
    .filter((item) => ["01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"].includes(item.stage))
    .map((item) => item.task?.id)
    .filter(Boolean);
  const attachmentTaskIDs = new Set(evidence.attachments.map((item) => item.source_task_id).filter(Boolean));
  const visibleMarkdownTaskIDs = new Set(comments
    .filter((comment) => isVisibleStageMarkdownComment(comment))
    .map((comment) => comment.source_task_id)
    .filter(Boolean));
  const missingStageArtifactTaskIDs = requiredStageTaskIDs.filter((taskID) => !attachmentTaskIDs.has(taskID) && !visibleMarkdownTaskIDs.has(taskID));
  check("artifact_previews_available", evidence.attachments.length >= 6 &&
    evidence.attachments.every((item) => item.preview_ok) &&
    missingStageArtifactTaskIDs.length === 0, {
    count: evidence.attachments.length,
    filenames: [...presentFilenames],
    required_stage_task_ids: requiredStageTaskIDs,
    attachment_stage_task_ids: [...attachmentTaskIDs],
    visible_markdown_stage_task_ids: [...visibleMarkdownTaskIDs],
    missing_stage_artifact_task_ids: missingStageArtifactTaskIDs,
    failed: evidence.attachments.filter((item) => !item.preview_ok).map((item) => item.id),
  });

  const sopRuns = normalizeRuns(await get(`/api/issues/${issue.id}/sop-runs`));
  const sopRun = sopRuns.find((item) => item.squad_id === squad.id) || sopRuns[0] || null;
  evidence.sop_run = summarizeSOPRun(sopRun);
  const completedSteps = new Set((sopRun?.events || []).filter((event) => event.event_type === "步骤完成").map((event) => event.step_key));
  check("sop_steps_completed", ["pm", "01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"].every((key) => completedSteps.has(key)), {
    completed: [...completedSteps],
  });

  const tree = await get(`/api/issues/${issue.id}/execution-tree`);
  evidence.execution_tree = summarizeExecutionTree(tree);
  check("execution_tree_has_artifact_refs", evidence.execution_tree.artifact_count >= 6, evidence.execution_tree);

  const trace = await get(`/api/issues/${issue.id}/trace`);
  const sourceFetchEventCount = (trace?.events || []).filter((event) => event.event_type === "source.fetch").length;
  evidence.trace = {
    event_count: countItems(trace?.events, trace?.total),
    source_fetch_events: sourceFetchEventCount,
    source_fetch_metadata_status: finalIssue.metadata?.source_fetch_status || null,
  };
  check("trace_contains_source_fetch", sourceFetchEventCount >= 1 || evidence.trace.source_fetch_metadata_status === "fetch_failed" || evidence.trace.source_fetch_metadata_status === "fetched", evidence.trace);

  const usage = await get(`/api/issues/${issue.id}/usage`);
  evidence.usage = usage;
  const tokenTotal = Number(usage?.total_input_tokens || 0) + Number(usage?.total_output_tokens || 0) + Number(usage?.total_cache_read_tokens || 0) + Number(usage?.total_cache_write_tokens || 0);
  check("usage_tokens_recorded", tokenTotal > 0, { token_total: tokenTotal });

  const finalTasks = await listIssueTasks(issue.id);
  const issueWorktreeNeedle = `/${workspace.id}/issues/${issue.id.slice(0, 8)}/repos/`;
  const requiredWorktreeTaskIDs = new Set(["pm", "01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"]
    .flatMap((stage) => evidence.stages.filter((item) => item.stage === stage).map((item) => item.task?.id).filter(Boolean)));
  const workdirRows = finalTasks
    .filter((task) => requiredWorktreeTaskIDs.has(task.id))
    .map((task) => ({
      id: task.id,
      agent_id: task.agent_id,
      status: task.status,
      work_dir: task.work_dir || "",
      issue_worktree: String(task.work_dir || "").includes(issueWorktreeNeedle),
      forbidden_local_checkout: String(task.work_dir || "").startsWith("/data/ida/user-center"),
    }));
  evidence.issue_worktree = {
    expected_path_fragment: issueWorktreeNeedle,
    required_task_ids: [...requiredWorktreeTaskIDs],
    rows: workdirRows,
  };
  check("issue_scoped_worktree_used", workdirRows.length >= requiredWorktreeTaskIDs.size &&
    workdirRows.every((row) => row.issue_worktree && !row.forbidden_local_checkout), evidence.issue_worktree);

  await assertNoActiveTasks("post-run");
  evidence.self_evaluation = selfEvaluate(evidence);
  evidence.ok = evidence.checks.every((item) => item.ok) && evidence.self_evaluation.score >= 9.5;
  evidence.status = evidence.ok ? "passed" : "failed";
  writeEvidence();
  if (!evidence.ok) {
    process.exitCode = 1;
  }
} catch (error) {
  evidence.ok = false;
  evidence.status = "failed";
  evidence.error = error?.stack || error?.message || String(error);
  writeEvidence();
  process.exitCode = 1;
}

async function createStageAgents(runtime) {
  const workerSpecs = {
    "01-clarify": ["01-需求澄清", "澄清密码强度需求边界和验收口径。"],
    "02-design": ["02-方案设计", "设计密码强度校验策略、接口影响和测试方案。"],
    "03-task-split": ["03-任务拆分", "拆分密码强度实现、测试和回归任务。"],
    "04-implement": ["04-开发", "给出实现说明和局部验证证据；本验收不实际改业务仓库。"],
    "05-verify": ["05-验证测试", "独立验证密码强度规则和证据完整性。"],
  };
  const out = {};
  for (const [key, [name, role]] of Object.entries(workerSpecs)) {
    out[key] = await post("/api/agents", {
      workspace_id: workspace.id,
      name: `${name}-${runID}`,
      description: `Password strength SOP E2E ${key}`,
      instructions: buildAgentInstruction(key, role),
      runtime_id: runtime.id,
      scope: "workspace",
      max_concurrent_tasks: 1,
      model,
      custom_args: sopAgentCustomArgs,
    });
    if (!out[key]?.id) fail(`create agent ${key} failed`);
  }

  const routingTable = Object.entries(out)
    .map(([key, agent]) => `${key}: [@${agent.name}](mention://agent/${agent.id})`)
    .join("\n");
  out.pm = await post("/api/agents", {
    workspace_id: workspace.id,
    name: `PM-项目经理-${runID}`,
    description: "Password strength SOP E2E pm",
    instructions: buildAgentInstruction("pm", "读取 issue、评论和人工恢复来源，负责调度/收口。", routingTable),
    runtime_id: runtime.id,
    scope: "workspace",
    max_concurrent_tasks: 1,
    model,
    custom_args: sopAgentCustomArgs,
  });
  if (!out.pm?.id) fail("create agent pm failed");
  return {
    pm: out.pm,
    ...out,
  };
}

function buildAgentInstruction(stageKey, role, routingTable = "") {
  if (stageKey === "pm") {
    return [
      "硬性执行规则：你已经处于 PM 首轮或 PM 收口任务中，不需要也不得向用户确认从哪个阶段开始。",
      "当前任务就是 PM 阶段本身；禁止说“接下来进入 PM 阶段”、禁止询问“是否需要开始”、禁止等待任何人工确认。",
      "首轮最终输出如果不是调度 01 的评论，本次验收会失败。",
      "如果 01-05 还没有阶段完成，必须立刻评论 @01-需求澄清；禁止提问，禁止等待确认。",
      "如果某个阶段刚完成，必须立刻评论 @唯一的下一阶段；禁止提问，禁止等待确认。",
      "只有 05-验证测试已完成、平台已关联 MR 且结论通过时，才允许把 issue 状态改为 in_review 并发表最终收口评论。",
      `你是 PM-项目经理阶段 Agent。${role}`,
      "这是验收运行，但必须走真实 Multica issue/comment/task/attachment 链路。",
      "这是平台链路验收，04/05 必须完成一个受控仓库变更、验证和 MR 提交流程。",
      "每次运行必须先读取 issue、metadata、完整评论历史和 SOP run 状态。",
      "TAPD source-fetch 如果是 fetch_failed，必须从评论中的“人工恢复 TAPD 需求内容”提取真实需求。",
      "PM 只负责协调和收口，不得代替 01/02/03/04/05 写它们的专业阶段产物。",
      "PM 不得读取、搜索、测试或修改业务仓库代码；不得运行 go test、pnpm test、构建命令或启动服务。",
      "PM 每一轮只能做一件事：判断当前已完成到哪个阶段，然后 @mention 唯一的下一阶段 Agent；发出调度评论后必须立刻停止，不得继续推进后续阶段。",
      "CodeBuddy 的 TaskCreate / TaskUpdate / todo 工具只是本地内部待办，不是 Multica 平台任务；本验收严禁使用这些内部待办工具，严禁用它们推进 01-05。",
      "读取 issue 必须通过 Bash 运行 `multica issue get`、`multica issue comment list`、`multica issue status` 等平台 CLI 命令。",
      "触发下一阶段的唯一方式是在 `multica issue comment add` 的评论正文里写一个 `[@阶段名](mention://agent/<id>)` 形式的真实平台 mention 链接；没有 mention://agent 的评论不会触发下一阶段，必须避免。",
      "可用的下一阶段真实 mention 路由表如下，只能原样使用其中一行：",
      routingTable,
      "阶段顺序固定：PM -> 01-需求澄清 -> 02-方案设计 -> 03-任务拆分 -> 04-开发 -> 05-验证测试 -> PM 最终收口。",
      "首轮：只确认 TAPD 人工恢复来源和阶段链，写入 `artifacts/multica/pm-password-strength.md`，然后只 @01-需求澄清。",
      "首轮调度评论必须直接包含 01 路由表里的 mention；不要写“需要确认”“请确认”“希望从哪个阶段开始”等任何询问句。",
      "01 完成后：只 @02-方案设计。02 完成后：只 @03-任务拆分。03 完成后：只 @04-开发。04 完成后：只 @05-验证测试。",
      "05 完成且结论通过后：先运行 `multica issue mr list <issue> --output json` 确认已有平台关联 MR；如果没有 MR，评论说明阻塞并停止；如果已有 MR，写入 `artifacts/multica/pm-final-password-strength.md`，运行 `multica issue status <issue> in_review`，并发表最终收口评论；此时不得 @任何 Agent。",
      "PM 产物必须包含：阶段名、TAPD 标题“增强密码强度”、人工恢复来源说明、密码规则、当前推进判断、下一步或收口结论。",
      "不要把 TAPD 登录页当需求；不要输出密钥；不要创建 child issue；不要一次 @mention 多个阶段。",
      "调度评论必须简短中文，且必须包含且只包含一个下一阶段 Agent mention；最终收口评论不得包含 mention://。",
    ].join("\n");
  }
  const artifactPath = `artifacts/multica/${stageKey}-password-strength.md`;
  return [
    `硬性执行规则：本阶段唯一合法产物路径是 \`${artifactPath}\`，必须用 Write 写到这个路径。`,
    "禁止把阶段产物写到工作目录根目录、artifacts/acceptance、tmp 或其它目录。",
    "完成评论只写阶段结论，禁止使用 `--attachment`；daemon 会自动把 artifacts/multica 下的 markdown 收拢到平台评论附件。",
    "禁止询问用户是否继续；本阶段收到任务后必须直接完成本阶段产物和简短评论。",
    `你是 ${stageKey} 阶段 Agent。${role}`,
    "这是验收运行，但必须走真实 Multica issue/comment/task/attachment 链路。",
    "这是平台链路验收。只有 04-开发 可以做受控仓库变更；只有 05-验证测试 可以创建 MR。",
    stageKey === "04-implement"
      ? [
          `04 必须在当前 git worktree 中创建或更新受控验收文件 \`${gongfengEvidenceFile}\`，内容写明 TAPD 标题、密码规则、run_id 和验证用途。`,
          `04 还必须单独写阶段产物 \`${artifactPath}\`；仓库里的 \`${gongfengEvidenceFile}\` 不是平台阶段产物，不能替代 \`${artifactPath}\`。`,
          `04 必须切到并推送源分支 \`${gongfengSourceBranch}\`，目标分支是 \`${gongfengTargetBranch}\`。`,
          "04 只允许修改这一个 docs 验收文件和本阶段 markdown 产物，不得改业务代码或其它文件。",
          "04 必须运行 `git status --short`、`git diff --stat`、`git log -1 --oneline` 并把结果写入阶段产物。",
        ].join("\n")
      : "非 04 阶段不得修改业务仓库；非 05 阶段不得创建 MR。",
    stageKey === "05-verify"
      ? [
          `05 必须验证源分支 \`${gongfengSourceBranch}\` 已推送且包含 \`${gongfengEvidenceFile}\`。`,
          `05 必须确认没有把本次验收文件写成旧平铺路径 \`${legacyFlatEvidenceFile}\`。`,
          `05 必须单独写阶段产物 \`${artifactPath}\`；MR 描述文件可以使用该阶段产物。`,
          "05 必须运行 `git status --short` 和 `git log -1 --oneline`，并把结果写入阶段产物。",
          `05 验证通过后必须运行平台命令创建 MR：\`multica issue mr create <issue> --provider gongfeng --project-path ${gongfengProjectPath} --source-branch ${gongfengSourceBranch} --target-branch ${gongfengTargetBranch} --title "<identifier>: 增强密码强度 SOP 验收" --description-file artifacts/multica/05-verify-password-strength.md --output json\`。`,
          "05 创建 MR 后必须运行 `multica issue mr list <issue> --output json` 确认平台能读到关联 MR，再将 issue 状态改为 `in_review`。",
          "如果 MR 创建失败，必须把 issue 状态改为 `blocked` 并在评论中说明阻塞原因，不能宣称验收通过。",
        ].join("\n")
      : "非 05 阶段不得创建 MR 或修改 issue 为 in_review。",
    "禁止使用子 Agent 做代码检索；不要读取 artifacts/acceptance 历史证据文件。",
    "CodeBuddy 的 TaskCreate / TaskUpdate / todo 工具只是本地内部待办，不是 Multica 平台任务；本验收严禁使用这些内部待办工具。",
    "读取 issue 必须通过 Bash 运行 `multica issue get`、`multica issue comment list` 等平台 CLI 命令。",
    "只允许读取当前 issue、metadata、评论历史，写入本阶段 markdown 产物，并发表简短阶段评论。",
    "每次运行必须先读取 issue、metadata 和完整评论历史。",
    "TAPD source-fetch 如果是 fetch_failed，必须从评论中的“人工恢复 TAPD 需求内容”提取真实需求。",
    `必须创建 markdown 产物文件，路径固定为 \`${artifactPath}\`。`,
    "只有 artifacts/multica 会被平台自动收拢。",
    `仓库受控验收文件必须放在本次 run 专属目录 \`${gongfengEvidenceDir}\` 下，禁止写成 \`${legacyFlatEvidenceFile}\`。`,
    "markdown 产物必须包含：阶段名、TAPD 标题“增强密码强度”、人工恢复来源说明、密码规则、验收证据、handoff。",
    "不要把 TAPD 登录页当需求；不要输出密钥；不要创建 child issue；不要超出本阶段允许的仓库动作。",
    "阶段评论不得包含 mention://，不得 @任何 Agent、小队或成员；阶段推进交给 PM 和平台调度。",
    "完成时用 `multica issue comment add` 发表简短中文阶段结论；附件由 daemon 自动收拢，不需要也不得手动加 --attachment。",
    "不得 @mention 下一阶段，交给 PM 或平台自动拉起的队长任务推进。",
  ].join("\n");
}

async function createSOPSquad(agents) {
  const profile = {
    profile_key: "password-strength-comment-sop-e2e",
    project: "usercenter",
    mode: "comment_driven_stage_chain",
    repository: {
      provider: "gongfeng",
      project_path: gongfengProjectPath,
      target_branch: gongfengTargetBranch,
      source_branch: gongfengSourceBranch,
      evidence_dir: gongfengEvidenceDir,
      evidence_file: gongfengEvidenceFile,
    },
    steps: [
      { key: "pm", name: agents.pm.name, role_key: "pm" },
      { key: "01-clarify", name: agents["01-clarify"].name, role_key: "01-clarify" },
      { key: "02-design", name: agents["02-design"].name, role_key: "02-design" },
      { key: "03-task-split", name: agents["03-task-split"].name, role_key: "03-task-split" },
      { key: "04-implement", name: agents["04-implement"].name, role_key: "04-implement" },
      { key: "05-verify", name: agents["05-verify"].name, role_key: "05-verify" },
    ],
    acceptance: ["TAPD fetch_failed 可人工恢复", "01-05 阶段完成", "markdown 产物自动进入评论附件", "05 后平台创建并关联 MR", "复盘能审计附件和 token"],
  };
  const squad = await post(`/api/squads?workspace_id=${encodeURIComponent(workspace.id)}`, {
    name: `密码强度 SOP 验收小队 ${runID}`,
    description: "SOP 小队：用于增强密码强度需求的评论式端到端验收。",
    leader_id: agents.pm.id,
    scope: "workspace",
    sop_profile: profile,
  });
  if (!squad?.id) fail("create squad failed");
  return squad;
}

async function addSquadMembers(squad, agents) {
  for (const [key, agent] of Object.entries(agents)) {
    const current = normalizeList(await get(`/api/squads/${squad.id}/members?workspace_id=${encodeURIComponent(workspace.id)}`));
    if (current.some((item) => item.member_id === agent.id)) continue;
    await post(`/api/squads/${squad.id}/members?workspace_id=${encodeURIComponent(workspace.id)}`, {
      member_type: "agent",
      member_id: agent.id,
      role: key === "pm" ? "leader" : key,
    });
  }
}

async function createPasswordIssue(project, squad, currentUser) {
  const created = await post("/api/issues", {
    workspace_id: workspace.id,
    project_id: project.id,
    title: `增强密码强度 SOP E2E ${runID}`,
    description: [
      "根据 TAPD Wiki 文档创建需求。",
      "",
      "这是 ai-studio / Multica 平台链路验收任务，要求验证 SOP 阶段、产物收拢、复盘证据、05 后 MR 创建与平台关联。",
      "04 阶段只允许在目标仓库写入受控 docs 验收文件，不得修改业务代码。",
      "05 阶段必须验证 04 变更并通过平台命令创建 Gongfeng MR，MR 必须自动关联回本 issue。",
      `目标仓库：${gongfengProjectPath}`,
      `目标分支：${gongfengTargetBranch}`,
      `源分支：${gongfengSourceBranch}`,
      `受控验收文件：${gongfengEvidenceFile}`,
      "每个阶段必须读取 issue、metadata、评论历史，生成本阶段 markdown 产物，并发表简短阶段评论。",
      "",
      `TAPD 链接：${tapdURL}`,
      "",
      "预期流程：先记录 TAPD fetch_failed，再由人工评论恢复需求正文，随后按 PM -> 01 -> 02 -> 03 -> 04 -> 05 评论推进，05 后进入 in_review。",
    ].join("\n"),
    status: "backlog",
    priority: "high",
    assignee_type: "member",
    assignee_id: currentUser.id,
    metadata: buildTAPDSourceMetadata(tapdURL),
  });
  if (!created?.id) fail("create password issue failed");
  return created;
}

async function runNaturalSOPFlow(agents, knownTasks) {
  const agentToStage = Object.fromEntries(Object.entries(agents).map(([stage, agent]) => [agent.id, stage]));
  const completedWorkers = new Set();
  let pmRuns = 0;
  for (let i = 0; i < maxSOPTasks; i++) {
    const task = await waitNextTerminalSOPTask(issue.id, agentToStage, knownTasks, `SOP task ${i + 1}`);
    knownTasks.add(task.id);
    const stage = agentToStage[task.agent_id] || "unknown";
    if (task.status !== "completed") {
      if (task.failure_reason === "runtime_recovery") {
        const attemptsExhausted =
          Number(task.max_attempts || 0) > 0 &&
          Number(task.attempt || 0) >= Number(task.max_attempts || 0);
        evidence.pm_route_recoveries.push({
          stage,
          task: pickTask(task),
          reason: attemptsExhausted
            ? "daemon/runtime recovery exhausted task retries"
            : "daemon/runtime recovery; waiting for platform retry",
        });
        if (attemptsExhausted) {
          const messages = await safeGet(`/api/tasks/${task.id}/messages`);
          fail(`${stage} task exhausted runtime recovery retries`, { task, messages });
        }
        continue;
      }
      const messages = await safeGet(`/api/tasks/${task.id}/messages`);
      fail(`${stage} task status=${task.status}: ${task.error || task.failure_reason || ""}`, { task, messages });
    }
    if (stage === "pm") {
      pmRuns++;
      await assertPMRoutedOrClosed(task, agents, completedWorkers);
    } else {
      completedWorkers.add(stage);
    }
    evidence.stages.push({
      stage: stage === "pm" ? (pmRuns === 1 ? "pm" : `pm-${pmRuns}`) : stage,
      trigger_comment: null,
      task: pickTask(task),
    });

    const current = await get(`/api/issues/${issue.id}`);
    if (current.status === "blocked") {
      fail("SOP flow reached blocked before MR/in_review handoff", {
        completed_workers: [...completedWorkers],
        pm_runs: pmRuns,
        issue: pickIssue(current),
      });
    }
    if (current.status === "in_review" && ["01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"].every((key) => completedWorkers.has(key))) {
      return;
    }
  }
  fail(`SOP flow exceeded ${maxSOPTasks} tasks without reaching in_review`, {
    completed_workers: [...completedWorkers],
    pm_runs: pmRuns,
    issue: pickIssue(await get(`/api/issues/${issue.id}`)),
  });
}

async function assertPMRoutedOrClosed(task, agents, completedWorkers) {
  const current = await get(`/api/issues/${issue.id}`);
  if (current.status === "in_review") return;
  const comments = normalizeList(await get(`/api/issues/${issue.id}/comments`));
  const taskComments = comments.filter((comment) => isTaskComment(comment, task));
  const mentionCount = taskComments.reduce((sum, comment) => sum + countMentions(comment.content), 0);
  if (mentionCount === 1) {
    return;
  }
  const next = nextStageAfterCompletedWorkers(agents, completedWorkers);
  if (!next) {
    fail("PM completed without exactly one platform agent mention and no next stage could be inferred", {
      task: pickTask(task),
      issue: pickIssue(current),
      mention_count: mentionCount,
      task_comments: taskComments.map(pickComment),
    });
  }
  const recovery = await postComment([
    `验收脚本检测到 PM 本轮没有发出唯一阶段 mention，作为人工评论恢复推进到 ${next.stage}。`,
    "",
    `[@${next.agent.name}](mention://agent/${next.agent.id})`,
  ].join("\n"));
  const recoveryEvidence = {
    pm_task: pickTask(task),
    recovered_to_stage: next.stage,
    recovered_to_agent: pick(next.agent, ["id", "name"]),
    recovery_comment: pickComment(recovery),
    mention_count: mentionCount,
    task_comments: taskComments.map(pickComment),
  };
  evidence.pm_route_recoveries.push(recoveryEvidence);
  check(`pm_route_recovered_${evidence.pm_route_recoveries.length}`, true, recoveryEvidence);
}

function nextStageAfterCompletedWorkers(agents, completedWorkers) {
  const order = ["01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"];
  const stage = order.find((key) => !completedWorkers.has(key));
  if (!stage || !agents[stage]?.id) return null;
  return { stage, agent: agents[stage] };
}

function isTaskComment(comment, task) {
  if (comment?.source_task_id && comment.source_task_id === task.id) return true;
  if (!comment?.created_at || !task?.started_at) return false;
  const created = new Date(comment.created_at).getTime();
  const started = new Date(task.started_at).getTime() - 1000;
  const completed = new Date(task.completed_at || Date.now()).getTime() + 5000;
  return comment.author_type === "agent" && created >= started && created <= completed;
}

function countMentions(content) {
  return (String(content || "").match(/mention:\/\/agent\//g) || []).length;
}

async function waitNextTerminalSOPTask(issueID, agentToStage, knownIDs, label) {
  return poll(async () => {
    const tasks = sortTasks(await listIssueTasks(issueID));
    const task = tasks.find((item) => agentToStage[item.agent_id] && !knownIDs.has(item.id));
    if (!task || isActiveTask(task)) return null;
    return task;
  }, taskTimeoutMs, `wait ${label}`);
}

async function assertNoActiveTasks(label) {
  const client = new pg.Client({ connectionString: databaseURL });
  await client.connect();
  try {
    const res = await client.query("SELECT status, count(*)::int FROM agent_task_queue WHERE status IN ('queued','dispatched','running','waiting_local_directory') GROUP BY status ORDER BY status");
    check(`${label}_no_active_tasks`, res.rows.length === 0, { active_tasks: res.rows });
    if (res.rows.length > 0) {
      fail(`${label}: active tasks exist; full SOP acceptance must run one task at a time`, { active_tasks: res.rows });
    }
  } finally {
    await client.end();
  }
}

async function ensureProject(currentUser) {
  const projects = normalizeList(await get("/api/projects"));
  const existing = projects.find((item) => item.title === `password-strength-e2e-${runID}`);
  if (existing) return existing;
  return post("/api/projects", {
    workspace_id: workspace.id,
    title: `password-strength-e2e-${runID}`,
    description: "Temporary project for password strength SOP E2E.",
    status: "in_progress",
    lead_type: "member",
    lead_id: currentUser.id,
    resources: [{
      resource_type: "gongfeng_repo",
      label: `${gongfengProjectPath} ${gongfengTargetBranch}`,
      resource_ref: {
        provider: "gongfeng",
        project_path: gongfengProjectPath,
        resource_kind: "commits",
        url: `https://git.code.tencent.com/${gongfengProjectPath}/commits/${gongfengTargetBranch}`,
        ref: gongfengTargetBranch,
      },
    }],
  });
}

async function resolveWorkspace() {
  const workspaces = await get("/api/workspaces");
  const found = normalizeList(workspaces).find((item) => item.slug === workspaceSlug);
  if (!found?.id) fail(`workspace not found: ${workspaceSlug}`);
  return found;
}

async function resolveRuntime() {
  const runtimes = normalizeList(await get(`/api/runtimes?workspace_id=${encodeURIComponent(workspace.id)}`));
  const runtime = runtimes
    .filter((item) => item.provider === provider && item.status === "online" && new Date(item.last_seen_at || 0).getTime() > Date.now() - 180000)
    .sort((a, b) => new Date(b.last_seen_at || 0).getTime() - new Date(a.last_seen_at || 0).getTime())[0];
  if (!runtime?.id) fail(`online runtime not found for provider=${provider}`);
  return runtime;
}

async function listIssueTasks(issueID) {
  const client = new pg.Client({ connectionString: databaseURL });
  await client.connect();
  try {
    const res = await client.query(`
      SELECT
        id::text,
        agent_id::text,
        runtime_id::text,
        issue_id::text,
        status,
        priority,
        dispatched_at,
        started_at,
        completed_at,
        result,
        error,
        failure_reason,
        attempt,
        max_attempts,
        is_leader_task,
        created_at,
        work_dir,
        trigger_comment_id::text,
        trigger_summary
      FROM agent_task_queue
      WHERE issue_id = $1
      ORDER BY created_at DESC
    `, [issueID]);
    return res.rows;
  } finally {
    await client.end();
  }
}

async function updateIssue(issueID, data) {
  return put(`/api/issues/${issueID}`, data);
}

async function postComment(content) {
  return post(`/api/issues/${issue.id}/comments`, { content });
}

async function get(pathname) {
  return request("GET", pathname);
}

async function safeGet(pathname) {
  try {
    return await get(pathname);
  } catch (error) {
    return { error: error.message };
  }
}

async function getText(pathname) {
  for (let attempt = 0; attempt <= httpRetries; attempt++) {
    try {
      const res = await fetch(apiURL + pathname, { headers: headers() });
      if (!res.ok) {
        const body = (await res.text()).slice(0, 500);
        if (res.status >= 500 && attempt < httpRetries) {
          await sleep(backoffMs(attempt));
          continue;
        }
        throw new Error(`GET ${pathname} ${res.status}: ${body}`);
      }
      return res.text();
    } catch (error) {
      if (attempt >= httpRetries) throw error;
      await sleep(backoffMs(attempt));
    }
  }
  throw new Error(`GET ${pathname} failed after retries`);
}

async function post(pathname, body, authToken = token) {
  return request("POST", pathname, body, authToken);
}

async function put(pathname, body) {
  return request("PUT", pathname, body);
}

async function request(method, pathname, body, authToken = token) {
  for (let attempt = 0; attempt <= httpRetries; attempt++) {
    try {
      const res = await fetch(apiURL + pathname, {
        method,
        headers: headers(authToken),
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const text = await res.text();
      if (!res.ok) {
        if (res.status >= 500 && attempt < httpRetries) {
          await sleep(backoffMs(attempt));
          continue;
        }
        throw new Error(`${method} ${pathname} ${res.status}: ${text.slice(0, 1000)}`);
      }
      if (!text) return null;
      return JSON.parse(text);
    } catch (error) {
      if (attempt >= httpRetries) throw error;
      await sleep(backoffMs(attempt));
    }
  }
  throw new Error(`${method} ${pathname} failed after retries`);
}

function backoffMs(attempt) {
  return Math.min(5000, 500 * 2 ** attempt);
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function headers(authToken = token) {
  const h = { "content-type": "application/json" };
  if (authToken) h.authorization = `Bearer ${authToken}`;
  if (workspace?.id) h["x-workspace-id"] = workspace.id;
  return h;
}

function buildTAPDSourceMetadata(rawURL) {
  return {
    source_provider: "tapd",
    source_url: rawURL,
    tapd_workspace_id: "47654106",
    tapd_resource_type: "markdown_wiki",
    tapd_resource_id: "1147654106001004223",
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
  fail(`${label} timed out`, { last });
}

function check(name, ok, detail = {}) {
  evidence.checks.push({ name, ok: Boolean(ok), detail });
  if (!ok) {
    // Keep collecting when possible; fatal checks call fail directly.
  }
}

function fail(message, detail = {}) {
  const err = new Error(message);
  err.detail = detail;
  throw err;
}

function writeEvidence() {
  mkdirSync(outputDir, { recursive: true });
  const stamped = path.join(outputDir, `password-strength-sop-e2e-${new Date().toISOString().replace(/[:.]/g, "-")}.json`);
  const latest = path.join(outputDir, "password-strength-sop-e2e-latest.json");
  const data = JSON.stringify(evidence, null, 2);
  writeFileSync(stamped, data + "\n");
  writeFileSync(latest, data + "\n");
  console.log(JSON.stringify({ ok: evidence.ok, status: evidence.status, latest, stamped }, null, 2));
}

function selfEvaluate(data) {
  const required = [
    "source_fetch_failed_recorded",
    "manual_recovery_comment_created",
    "issue_in_review",
    "platform_mr_linked",
    "repository_acceptance_file_run_scoped",
    "auto_artifact_comments_present",
    "artifact_previews_available",
    "sop_steps_completed",
    "execution_tree_has_artifact_refs",
    "trace_contains_source_fetch",
    "usage_tokens_recorded",
    "issue_scoped_worktree_used",
    "post-run_no_active_tasks",
  ];
  const passed = new Set(data.checks.filter((item) => item.ok).map((item) => item.name));
  const missing = required.filter((name) => !passed.has(name));
  return {
    score: missing.length === 0 ? 9.6 : Math.max(0, 9.6 - missing.length * 0.8),
    passed: missing.length === 0,
    missing,
    note: missing.length === 0 ? "密码强度 SOP 评论推进、人工恢复、附件预览、05 后 MR 关联、复盘证据和 token 统计均有当前运行证据。" : "仍有硬证据缺口，不能标记 goal complete。",
  };
}

function summarizeComments(comments) {
  return {
    count: comments.length,
    agent_comments: comments.filter((item) => item.author_type === "agent").length,
    member_comments: comments.filter((item) => item.author_type === "member").length,
    attachment_comments: comments.filter((item) => Array.isArray(item.attachments) && item.attachments.length > 0).length,
  };
}

function summarizeSOPRun(run) {
  if (!run) return null;
  return {
    id: run.id,
    status: run.status,
    current_step_key: run.current_step_key,
    event_count: Array.isArray(run.events) ? run.events.length : 0,
    completed_steps: (run.events || []).filter((event) => event.event_type === "步骤完成").map((event) => event.step_key),
  };
}

function summarizeExecutionTree(tree) {
  const nodes = collectTreeNodes(tree?.root || tree);
  const artifactCount = nodes.reduce((sum, node) => sum + (Array.isArray(node.artifacts) ? node.artifacts.length : 0), 0);
  const attachmentEvidenceRefs = nodes.reduce((sum, node) => sum + (Array.isArray(node.evidence_refs) ? node.evidence_refs.filter((ref) => ref.type === "attachment").length : 0), 0);
  return {
    node_count: nodes.length,
    task_node_count: nodes.filter((node) => node.node_type === "agent_task").length,
    artifact_count: artifactCount,
    attachment_evidence_refs: attachmentEvidenceRefs,
  };
}

function collectTreeNodes(root) {
  if (!root || typeof root !== "object") return [];
  const out = [];
  const visit = (node) => {
    if (!node || typeof node !== "object") return;
    if (node.node_id || node.node_type || node.artifacts) out.push(node);
    for (const key of ["timeline", "children", "nodes"]) {
      if (Array.isArray(node[key])) node[key].forEach(visit);
    }
  };
  visit(root);
  return out;
}

function pickSourceFetch(metadata) {
  return {
    provider: metadata.source_fetch_provider,
    status: metadata.source_fetch_status,
    error: metadata.source_fetch_error,
    url: metadata.source_fetch_url,
    resource_id: metadata.source_fetch_resource_id,
  };
}

function pickIssue(item) {
  return pick(item, ["id", "identifier", "title", "status", "project_id", "assignee_type", "assignee_id"]);
}

function pickTask(item) {
  return pick(item, ["id", "agent_id", "status", "created_at", "started_at", "completed_at", "failure_reason", "error", "attempt", "max_attempts", "is_leader_task", "work_dir"]);
}

function pickComment(item) {
  return {
    ...pick(item, ["id", "author_type", "source_task_id", "created_at"]),
    content_excerpt: String(item?.content || "").slice(0, 220),
    attachment_count: Array.isArray(item?.attachments) ? item.attachments.length : 0,
  };
}

function isVisibleStageMarkdownComment(comment) {
  const content = String(comment?.content || "");
  if (comment?.author_type !== "agent" || !comment?.source_task_id) return false;
  if (!/^##\s+0[1-5]-/m.test(content) && !/^##\s+0[1-5][\s-]/m.test(content)) return false;
  return content.length >= 300;
}

function normalizePullRequests(value) {
  if (Array.isArray(value)) return value;
  if (Array.isArray(value?.pull_requests)) return value.pull_requests;
  if (Array.isArray(value?.items)) return value.items;
  return [];
}

function pickPullRequest(item) {
  return {
    id: item?.id || "",
    number: Number(item?.number || 0),
    title: item?.title || "",
    state: item?.state || "",
    html_url: item?.html_url || "",
    branch: item?.branch || "",
  };
}

function pick(obj, keys) {
  const out = {};
  for (const key of keys) out[key] = obj?.[key] ?? null;
  return out;
}

function normalizeList(value) {
  if (Array.isArray(value)) return value;
  if (Array.isArray(value?.items)) return value.items;
  if (Array.isArray(value?.members)) return value.members;
  if (Array.isArray(value?.tasks)) return value.tasks;
  if (Array.isArray(value?.comments)) return value.comments;
  return [];
}

function normalizeRuns(value) {
  if (Array.isArray(value)) return value;
  if (Array.isArray(value?.runs)) return value.runs;
  if (Array.isArray(value?.items)) return value.items;
  return [];
}

function sortTasks(tasks) {
  return [...tasks].sort((a, b) => new Date(a.created_at || a.started_at || 0).getTime() - new Date(b.created_at || b.started_at || 0).getTime());
}

function isActiveTask(task) {
  return ["queued", "dispatched", "running", "waiting_local_directory"].includes(task.status);
}

function countItems(items, total) {
  if (typeof total === "number") return total;
  return Array.isArray(items) ? items.length : 0;
}

function trimEnv(name) {
  return String(process.env[name] || "").trim();
}

function readGoalTestDatabaseURLForAPI(apiBase) {
  const environment = String(apiBase || "").includes(":18760") ? "prod" : "int";
  const envFile = path.join(repoRoot, ".run", "env", `goal-test-${environment}.env`);
  if (!existsSync(envFile)) return "";
  return readEnvFile(envFile).DATABASE_URL || "";
}

function readEnvFile(file) {
  const env = {};
  for (const raw of readFileSync(file, "utf8").split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const match = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
    if (match) env[match[1]] = match[2].replace(/^['"]|['"]$/g, "");
  }
  return env;
}
