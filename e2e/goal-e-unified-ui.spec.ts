import { test, expect, type ConsoleMessage, type Page } from "@playwright/test";
import { execFile } from "node:child_process";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { authenticateBrowserSession, waitForPageText } from "./helpers";
import { TestApiClient } from "./fixtures";

const execFileAsync = promisify(execFile);
const E2E_WORKER = process.env.TEST_PARALLEL_INDEX ?? process.env.TEST_WORKER_INDEX ?? "0";
const E2E_RUN_ID = process.env.E2E_RUN_ID ?? `${Date.now().toString(36)}-${process.pid.toString(36)}`;
const E2E_ACCOUNT = process.env.E2E_ACCOUNT ?? `e2e-goal-e-${E2E_WORKER}-${E2E_RUN_ID}`;
const E2E_NAME = process.env.E2E_NAME ?? "Goal E UI User";
const E2E_WORKSPACE = process.env.E2E_WORKSPACE ?? `e2e-goal-e-workspace-${E2E_WORKER}-${E2E_RUN_ID}`;
const E2E_WORKSPACE_NAME = process.env.E2E_WORKSPACE_NAME ?? `Goal E Workspace ${E2E_WORKER}`;
const GONGFENG_URL = "https://git.code.tencent.com/ChainWeaver/ida/user-center";

test.describe("Goal E unified UI acceptance", () => {
  test.describe.configure({ timeout: 240_000 });

  let api: TestApiClient;
  let workspaceSlug: string;
  let workspaceId: string;
  let artifactPrefix: string;
  let tempRepoPath: string | null = null;
  let tempRepoPaths: string[] = [];

  test.beforeEach(async ({ page }) => {
    api = new TestApiClient();
    await api.login(E2E_ACCOUNT, E2E_NAME);
    const workspace = await api.ensureWorkspace(E2E_WORKSPACE_NAME, E2E_WORKSPACE);
    await api.markUserOnboarded();
    workspaceSlug = workspace.slug;
    workspaceId = workspace.id;
    artifactPrefix = `GoalE Unified UI ${Date.now()}`;
    await api.cleanupPromptArtifactsByPrefix("GoalE Unified UI");
    const token = api.getToken();
    if (!token) throw new Error("E2E login did not return an auth token");
    await authenticateBrowserSession(page, token, workspaceSlug);
    await page.goto(`/${workspaceSlug}/issues`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "新建任务", 60_000);
  });

  test.afterEach(async () => {
    for (const repoPath of tempRepoPaths) {
      await rm(repoPath, { recursive: true, force: true }).catch(() => undefined);
    }
    tempRepoPaths = [];
    if (tempRepoPath) {
      await rm(tempRepoPath, { recursive: true, force: true });
      tempRepoPath = null;
    }
    if (api && artifactPrefix) {
      if (workspaceId) {
        await api.updateWorkspace(workspaceId, { repos: [] }).catch(() => undefined);
      }
      await api.cleanupPromptArtifactsByPrefix(artifactPrefix);
      await api.cleanup();
    }
  });

  test("covers settings, project issue, run evidence, optimizer, and skill changelog path", async ({ page }) => {
    test.setTimeout(240_000);
    const consoleErrors: string[] = [];
    const pageErrors: string[] = [];
    const failedRequests: string[] = [];
    const ignoredRequestFailures: string[] = [];
    const pageTimings: Record<string, number> = {};
    page.on("console", (message: ConsoleMessage) => {
      if (message.type() === "error") consoleErrors.push(message.text());
    });
    page.on("pageerror", (error) => pageErrors.push(error.message));
    page.on("requestfailed", (request) => {
      const failureText = request.failure()?.errorText ?? "";
      const row = `${request.method()} ${request.url()} ${failureText}`;
      if (failureText === "net::ERR_ABORTED") {
        ignoredRequestFailures.push(row);
        return;
      }
      failedRequests.push(row);
    });
    page.on("response", (response) => {
      if (response.status() >= 500) failedRequests.push(`${response.status()} ${response.url()}`);
    });
    await page.route("https://api.github.com/repos/multica-ai/multica/releases/latest", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ tag_name: "v0.0.0-e2e", html_url: "https://git.code.tencent.com/ChainWeaver/ida/user-center" }),
      });
    });

    const artifactDir = path.resolve(process.cwd(), "artifacts/acceptance");
    await mkdir(artifactDir, { recursive: true });
    const screenshotPaths: Record<string, string> = {};
    const coverage = {
      settings: false,
      project_issue: false,
      training_run_detail: false,
      dashboard_ia: false,
      eval_optimizer: false,
      skill_candidate_changelog: false,
      console_pageerror_checked: false,
      api_errors_checked: false,
    };

    await api.updateWorkspace(workspaceId, {
      repos: [{ url: GONGFENG_URL, description: "Goal E Gongfeng user-center fixture" }],
    });

    const skillPath = ".codebuddy/skills/05-verify/SKILL.md";
    const changelogPath = ".codebuddy/skills/05-verify/CHANGELOG.md";
    const repoFixture = await createSkillRepoFixture(skillPath, changelogPath);
    tempRepoPath = repoFixture.repoPath;
    const linkedRepoFixture = await createSkillRepoFixture(skillPath, changelogPath);
    tempRepoPaths.push(linkedRepoFixture.repoPath);

    const project = await api.createProject(`${artifactPrefix} project`);
    const gongfengResource = await api.createProjectResource(project.id, {
      resource_type: "gongfeng_repo",
      label: `${artifactPrefix} Gongfeng user-center`,
      resource_ref: {
        provider: "gongfeng",
        url: GONGFENG_URL,
        project_path: "ChainWeaver/ida/user-center",
        ref: "v5.0.0_dev_sop",
        resource_kind: "branch",
        title: "user-center",
      },
    });
    const localResource = await api.createProjectResource(project.id, {
      resource_type: "local_directory",
      label: `${artifactPrefix} local Gongfeng checkout`,
      resource_ref: {
        local_path: tempRepoPath,
        daemon_id: `goal-e-unified-${Date.now()}`,
      },
    });
    const linkedLocalResource = await api.createProjectResource(project.id, {
      resource_type: "local_directory",
      label: `${artifactPrefix} linked candidate checkout`,
      resource_ref: {
        local_path: linkedRepoFixture.repoPath,
        daemon_id: `goal-e-linked-${Date.now()}`,
      },
    });

    const timelineRuntime = await api.ensureOnlineCodexRuntime(`${artifactPrefix} issue timeline runtime`);
    const timelineAgent = await api.createAgent({
      name: `${artifactPrefix} issue timeline agent`,
      runtime_id: timelineRuntime.id,
      model: "gpt-5.4-mini",
      instructions: "请用中文完成任务，并记录可验收的运行时间流、token 和证据锚点。",
    });
    const issue = await api.createIssue(`${artifactPrefix} issue`, {
      project_id: project.id,
      status: "todo",
      assignee_type: "agent",
      assignee_id: timelineAgent.id,
      description: "Goal E unified UI issue fixture for project and run evidence coverage.",
    });
    let timelineTask: { id: string; runtime_id: string; status: string } | null = null;
    await expect.poll(async () => {
      const claimed = await api.claimDaemonTask(timelineRuntime.id);
      if (claimed.task?.id) {
        timelineTask = claimed.task;
        return claimed.task.id;
      }
      return null;
    }, { timeout: 15_000 }).not.toBeNull();
    if (!timelineTask) throw new Error("Goal E issue timeline task was not claimed");
    await api.startDaemonTask(timelineTask.id);
    await api.reportDaemonTaskUsage(timelineTask.id, {
      provider: "codex",
      model: "gpt-5.4-mini",
      input_tokens: 128,
      output_tokens: 64,
      cache_read_tokens: 16,
      cache_write_tokens: 8,
    });
    await api.reportDaemonTaskMessages(timelineTask.id, [
      {
        seq: 1,
        type: "text",
        content: `已读取 Issue ${issue.id} 的目标，并准备生成评测用例与优化候选证据。`,
      },
      {
        seq: 2,
        type: "tool_use",
        tool: "gongfeng.issue.timeline.inspect",
        content: "检查工蜂资源、运行摘要和评测入口。",
        input: { issue_id: issue.id, project_id: project.id, source_resource_id: gongfengResource.id },
        output: null,
      },
      {
        seq: 3,
        type: "tool_result",
        tool: "gongfeng.issue.timeline.inspect",
        content: "工蜂资源、运行摘要和评测入口检查完成。",
        input: {},
        output: "已形成 issue timeline 证据锚点。",
      },
    ]);
    await new Promise((resolve) => setTimeout(resolve, 25));
    await api.completeDaemonTask(timelineTask.id, "Issue timeline fixture completed with usage, messages, and tool evidence.");
    const issueExecutionTreeApi = await api.getIssueExecutionTree(issue.id);
    expect(issueExecutionTreeApi.issue_summary?.node_count ?? 0).toBeGreaterThanOrEqual(1);
    expect(issueExecutionTreeApi.issue_summary?.total_input_tokens ?? 0).toBeGreaterThan(0);
    expect(issueExecutionTreeApi.issue_summary?.total_output_tokens ?? 0).toBeGreaterThan(0);
    expect(issueExecutionTreeApi.issue_summary?.agent_turn_count ?? 0).toBeGreaterThan(0);
    expect(issueExecutionTreeApi.issue_summary?.full_analysis_deep_link || "").toContain(issue.id);
    expect(issueExecutionTreeApi.timeline_nodes?.some((node) => node.node_type === "agent_task")).toBe(true);

    await timedGoto(page, `/${workspaceSlug}/settings?tab=repositories`, pageTimings, "settings");
    await expect(page.getByRole("tab", { name: "代码仓库" })).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText(GONGFENG_URL)).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole("tab", { name: /^GitHub$/ })).toHaveCount(0);
    screenshotPaths.settings = await screenshot(page, artifactDir, "settings");
    coverage.settings = true;

    await timedGoto(page, `/${workspaceSlug}/projects/${project.id}`, pageTimings, "project");
    await expect(page.getByText(`${artifactPrefix} project`).first()).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole("button", { name: "资源", exact: true })).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText(`${artifactPrefix} Gongfeng user-center`)).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText(/Gongfeng user-center/)).toBeVisible({ timeout: 15_000 });
    screenshotPaths.project = await screenshot(page, artifactDir, "project");

    await timedGoto(page, `/${workspaceSlug}/issues/${issue.id}`, pageTimings, "issue");
    await expect(page.getByText(`${artifactPrefix} issue`).first()).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole("button", { name: "Merge Request" })).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole("button", { name: "详情" })).toBeVisible({ timeout: 15_000 });
    await expect(page.getByTestId("issue-timeline-summary")).toBeVisible({ timeout: 20_000 });
    await expect(page.getByTestId("issue-collaboration-execution-tree")).toBeVisible({ timeout: 15_000 });
    screenshotPaths.issue = await screenshot(page, artifactDir, "issue");
    coverage.project_issue = true;

    const prompt = await api.createPromptForE2E(artifactPrefix, {
      name: `${artifactPrefix} prompt`,
      content: "{{expected_behavior}}\n{{verification}}\n{{evidence_source}}\n{{issue_title}}\n{{issue_id}}",
      variables: [
        { name: "expected_behavior", label: "Expected behavior", required: false },
        { name: "verification", label: "Verification", required: false },
        { name: "evidence_source", label: "Evidence source", required: false },
        { name: "issue_title", label: "Issue title", required: false },
        { name: "issue_id", label: "Issue ID", required: false },
      ],
    });
    const asset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} evaluation asset`,
      description: "Goal E unified run detail, optimizer, and skill chain fixture",
      asset_type: "优化运行",
      payload: {
        cases: [{
          名称: "force unified optimizer candidate",
          变量: { issue_id: issue.id, issue_title: issue.title ?? `${artifactPrefix} issue` },
          期望包含: ["__missing_goal_e_marker__"],
        }],
      },
      status: "启用",
    });
    const inventory = await api.createPromptEvaluationSkillInventory(asset.id, {
      source_resource_id: localResource.id,
      skill_root: ".codebuddy/skills",
    });
    const snapshotResult = await api.createPromptEvaluationSkillSnapshot(asset.id, {
      source_resource_id: localResource.id,
      skill_path: skillPath,
    });
    const snapshot = snapshotResult.snapshot;
    const linkedSnapshotResult = await api.createPromptEvaluationSkillSnapshot(asset.id, {
      source_resource_id: linkedLocalResource.id,
      skill_path: skillPath,
    });
    const linkedSnapshot = linkedSnapshotResult.snapshot;
    const draftCasesResult = await api.createPromptEvaluationSkillCaseDrafts(asset.id, {
      repo_path: tempRepoPath,
      skill_path: skillPath,
      limit: 5,
      auto_approve: false,
    });
    const approvedCasesResult = await api.createPromptEvaluationSkillCaseDrafts(asset.id, {
      repo_path: tempRepoPath,
      skill_path: skillPath,
      limit: 5,
      auto_approve: true,
    });
    const historyCases = buildHistoryCaseEvidence(draftCasesResult.drafts, approvedCasesResult.drafts);
    await api.updatePromptEvaluationAsset(asset.id, {
      payload: {
        ...approvedCasesResult.asset.payload,
        skill_case_draft_contract: "multica.skill.case_draft.v1",
        skill_case_drafts: historyCases,
      },
    });
    await api.updatePromptLibraryItem(prompt.id, {
      content: [
        `Issue ID: ${issue.id}`,
        ...historyCases
        .map((draft: { expected_behavior: string; verification: string; evidence_source: string }) => [
          draft.expected_behavior,
          draft.verification,
          draft.evidence_source,
        ].join("\n")),
      ].join("\n\n"),
      tags: ["E2E", "Goal E", "unified UI"],
      status: "启用",
    });
    await api.runPromptEvaluationAsset(asset.id);
    const failedRun = await expect
      .poll(async () => {
        const runs = await api.listPromptEvaluationRuns({ asset_id: asset.id });
        return runs.find((run) => run.status === "未通过") ?? null;
      }, { timeout: 15_000 })
      .not.toBeNull()
      .then(async () => (await api.listPromptEvaluationRuns({ asset_id: asset.id })).find((run) => run.status === "未通过")!);
    const candidate = await api.createPromptEvaluationOptimizationCandidate(failedRun.id);
    const issueRead = await api.getIssue(issue.id);
    const listedRuns = await api.listPromptEvaluationRuns({ asset_id: asset.id });
    const runEvidenceApi = await api.getPromptEvaluationRunEvidence(failedRun.id);
    const transitionedIssue = await api.updateIssue(issue.id, { status: "in_progress" });
    const evidenceSnapshot = await api.createPromptEvaluationEvidenceSnapshot(failedRun.id);
    const evidenceSnapshotRead = await api.getPromptEvaluationEvidenceSnapshot(failedRun.id, evidenceSnapshot.id);
    const assetSnapshotResult = await api.createPromptEvaluationAssetEvidenceSnapshots(asset.id);
    const assetEvidenceArchive = await api.getPromptEvaluationAssetEvidenceArchivePackage(asset.id);
    const updatedCandidate = await api.updatePromptEvaluationOptimizationCandidate(candidate.id, {
      candidate_name: `${artifactPrefix} skill patch candidate`,
      candidate_content: "Skill patch candidate stored in skill_patch.patch.",
      rationale: "Patch adds Goal E unified acceptance evidence discipline to the verify skill.",
      edit_note: "Goal E unified UI fixture promotes the generated candidate to a skill patch.",
      skill_patch: {
        patch: repoFixture.candidatePatch,
        source_snapshot: snapshot,
        source_resource_id: localResource.id,
        repo_path: tempRepoPath,
        target_branch: snapshot.branch,
        skill_path: skillPath,
        changelog_path: changelogPath,
        expected_improvement: "Improve final verification evidence discipline.",
        risk: "Low-risk skill instruction addition in a temporary local checkout fixture.",
        verification_plan: "Run Goal E unified UI Playwright fixture after apply.",
        publication_status: "draft",
      },
    });
    const freshness = await api.checkPromptEvaluationSkillCandidateFreshness(candidate.id, {
      source_resource_id: localResource.id,
    });
    const apply = await api.applyPromptEvaluationSkillCandidate(candidate.id, {
      source_resource_id: localResource.id,
      rollback_plan: "Reverse the candidate patch and remove the generated CHANGELOG entry.",
    });
    const reEvalAsset = await api.preparePromptEvaluationSkillReEvalAsset(candidate.id, {
      source_resource_id: localResource.id,
      snapshot,
      include_draft: false,
    });
    const reEvalRun = await api.runPromptEvaluationSkillReEval(candidate.id, {
      asset_id: reEvalAsset.asset.id,
    });
    expect(reEvalRun.run.status, JSON.stringify(reEvalRun.run, null, 2)).toBe("通过");

    await timedGoto(page, `/${workspaceSlug}/training/run-history?run=${failedRun.id}`, pageTimings, "run_history");
    const runRow = page.getByTestId(`prompt-evaluation-run-${failedRun.id}`);
    const runEvidence = runRow.getByTestId(`run-evidence-${failedRun.id}`);
    await expect(runEvidence).toBeVisible({ timeout: 20_000 });
    await expect(runEvidence.getByTestId("run-evidence-anchor-summary")).toBeVisible({ timeout: 15_000 });
    await expect(runEvidence.getByTestId("run-evidence-issue-review")).toBeVisible({ timeout: 15_000 });
    await expect(runEvidence.getByTestId("run-evidence-issue-review")).toContainText(issue.id, { timeout: 15_000 });
    await expect(runEvidence.getByTestId("run-evidence-issue-review-nodes")).toBeVisible({ timeout: 15_000 });
    await expect(runEvidence.getByTestId("run-evidence-eval-link")).toBeVisible({ timeout: 15_000 });
    const createEvalCaseResponse = page.waitForResponse((response) =>
      response.request().method() === "POST" &&
      response.url().includes("/api/prompt-evaluation-cases") &&
      response.status() < 400,
    );
    await runEvidence.getByTestId("run-evidence-create-eval-case").click();
    const createdEvalCase = await (await createEvalCaseResponse).json();
    await expect(runEvidence.getByTestId("run-evidence-created-eval-case")).toContainText(createdEvalCase.id, { timeout: 15_000 });
    const linkedEvalCases = await api.listPromptEvaluationCases({ asset_id: asset.id, tag: "issue-review", limit: 20 });
    const linkedEvalCase = linkedEvalCases.find((item) => item.id === createdEvalCase.id);
    expect(linkedEvalCase, JSON.stringify(linkedEvalCases, null, 2)).toBeTruthy();
    expect(JSON.stringify(linkedEvalCase?.input ?? {})).toContain(issue.id);
    expect(JSON.stringify(linkedEvalCase?.expected ?? {})).toContain(failedRun.id);
    const runIdsBeforeLinkedEval = new Set((await api.listPromptEvaluationRuns({ asset_id: asset.id, limit: 20 })).map((run) => run.id));
    const runLinkedEvalResponse = page.waitForResponse((response) =>
      response.request().method() === "POST" &&
      response.url().includes(`/api/prompt-evaluation-assets/${asset.id}/run`) &&
      response.status() < 400,
    );
    await runEvidence.getByTestId("run-evidence-run-eval").click();
    await runLinkedEvalResponse;
    const linkedEvalRun = await expect
      .poll(async () => {
        const runs = await api.listPromptEvaluationRuns({ asset_id: asset.id, limit: 20 });
        return runs.find((run) => !runIdsBeforeLinkedEval.has(run.id)) ?? null;
      }, { timeout: 15_000 })
      .not.toBeNull()
      .then(async () => {
        const runs = await api.listPromptEvaluationRuns({ asset_id: asset.id, limit: 20 });
        return runs.find((run) => !runIdsBeforeLinkedEval.has(run.id))!;
      });
    await expect(runEvidence.getByTestId("run-evidence-created-eval-run")).toContainText(linkedEvalRun.id, { timeout: 15_000 });
    await expect(runEvidence.getByTestId("run-evidence-eval-run-review")).toBeVisible({ timeout: 15_000 });
    await expect(runEvidence.getByTestId("run-evidence-eval-run-review")).toContainText("评分", { timeout: 15_000 });
    const linkedEvalRunEvidence = await api.getPromptEvaluationRunEvidence(linkedEvalRun.id);
    expect(linkedEvalRunEvidence.trials.length).toBeGreaterThan(0);
    expect(linkedEvalRunEvidence.trials.some((trial) => trial.case_name === createdEvalCase.case_name)).toBeTruthy();
    await expect(runEvidence.getByTestId("run-evidence-eval-trials")).toContainText(createdEvalCase.case_name, { timeout: 15_000 });
    const generateLinkedCandidateResponse = page.waitForResponse((response) =>
      response.request().method() === "POST" &&
      response.url().includes(`/api/prompt-evaluation-runs/${linkedEvalRun.id}/optimization-candidates`) &&
      response.status() < 400,
    );
    await runEvidence.getByTestId("run-evidence-generate-candidate").click();
    const linkedEvalCandidate = await (await generateLinkedCandidateResponse).json();
    await expect(runEvidence.getByTestId("run-evidence-generated-candidate")).toContainText(linkedEvalCandidate.id, { timeout: 15_000 });
    await expect(runEvidence.getByTestId("run-evidence-generated-candidate")).toContainText(linkedEvalCandidate.status, { timeout: 15_000 });
    expect(linkedEvalCandidate.run_id).toBe(linkedEvalRun.id);
    const linkedSkillCandidate = await api.updatePromptEvaluationOptimizationCandidate(linkedEvalCandidate.id, {
      candidate_name: `${artifactPrefix} linked eval skill patch candidate`,
      candidate_content: "Linked eval skill patch candidate stored in skill_patch.patch.",
      rationale: "Patch is promoted from the linked failed eval run so source_run_id remains bound to the visible run evidence path.",
      edit_note: "Goal E unified UI fixture promotes the linked eval candidate to a skill patch.",
      skill_patch: {
        patch: linkedRepoFixture.candidatePatch,
        source_snapshot: linkedSnapshot,
        source_resource_id: linkedLocalResource.id,
        repo_path: linkedRepoFixture.repoPath,
        target_branch: linkedSnapshot.branch,
        skill_path: skillPath,
        changelog_path: changelogPath,
        expected_improvement: "Improve linked run evidence verification discipline.",
        risk: "Low-risk linked eval skill instruction addition in a temporary local checkout fixture.",
        verification_plan: "Run freshness, apply, CHANGELOG write, prepare re-eval, and run re-eval from the same run evidence panel.",
        publication_status: "draft",
      },
    });
    await runEvidence.getByTestId("run-evidence-refresh-candidate").click();
    const linkedWorkflow = runEvidence.getByTestId(`skill-candidate-workflow-${linkedEvalCandidate.id}`);
    await expect(linkedWorkflow).toBeVisible({ timeout: 15_000 });
    await expect(linkedWorkflow).toContainText("Skill 发布链路", { timeout: 15_000 });
    await expect(linkedWorkflow).toContainText("Apply + CHANGELOG", { timeout: 15_000 });
    await expect(runEvidence.getByTestId(`skill-candidate-diff-risk-${linkedEvalCandidate.id}`)).toContainText("Low-risk linked eval", { timeout: 15_000 });
    await expect(runEvidence.getByTestId(`skill-candidate-diff-risk-${linkedEvalCandidate.id}`)).toContainText("Run freshness, apply", { timeout: 15_000 });
    const linkedFreshnessResponse = page.waitForResponse((response) =>
      response.request().method() === "POST" &&
      response.url().includes(`/api/prompt-evaluation-optimization-candidates/${linkedEvalCandidate.id}/skill-freshness`) &&
      response.status() < 400,
    );
    await linkedWorkflow.getByRole("button", { name: "Freshness" }).click();
    const linkedFreshness = await (await linkedFreshnessResponse).json();
    await expect(linkedWorkflow).toContainText(linkedFreshness.status, { timeout: 15_000 });
    const linkedApplyResponse = page.waitForResponse((response) =>
      response.request().method() === "POST" &&
      response.url().includes(`/api/prompt-evaluation-optimization-candidates/${linkedEvalCandidate.id}/skill-apply`) &&
      response.status() < 400,
    );
    await linkedWorkflow.getByRole("button", { name: "Apply + CHANGELOG" }).click();
    const linkedApply = await (await linkedApplyResponse).json();
    await expect(linkedWorkflow).toContainText(linkedApply.apply.status, { timeout: 15_000 });
    const linkedReEvalAssetResponse = page.waitForResponse((response) =>
      response.request().method() === "POST" &&
      response.url().includes(`/api/prompt-evaluation-optimization-candidates/${linkedEvalCandidate.id}/skill-re-eval-asset`) &&
      response.status() < 400,
    );
    await linkedWorkflow.getByRole("button", { name: "Prepare re-eval" }).click();
    const linkedReEvalAsset = await (await linkedReEvalAssetResponse).json();
    await expect(linkedWorkflow).toContainText(linkedReEvalAsset.asset.id.slice(0, 10), { timeout: 15_000 });
    const linkedRunReEvalResponse = page.waitForResponse((response) =>
      response.request().method() === "POST" &&
      response.url().includes(`/api/prompt-evaluation-optimization-candidates/${linkedEvalCandidate.id}/skill-re-eval-run`) &&
      response.status() < 400,
    );
    await expect(linkedWorkflow.getByRole("button", { name: "Run re-eval" })).toBeEnabled({ timeout: 15_000 });
    await linkedWorkflow.getByRole("button", { name: "Run re-eval" }).click();
    const linkedReEvalRun = await (await linkedRunReEvalResponse).json();
    expect(linkedReEvalRun.run.status, JSON.stringify(linkedReEvalRun.run, null, 2)).toBe("通过");
    await expect(linkedWorkflow).toContainText(linkedReEvalRun.run.id.slice(0, 10), { timeout: 15_000 });
    await expect(runEvidence.getByTestId("run-evidence-execution-spans")).toBeVisible({ timeout: 15_000 });
    await expect(runEvidence.getByText(issue.id).first()).toBeVisible({ timeout: 15_000 });
    screenshotPaths.run_history = await screenshot(page, artifactDir, "run-history");
    coverage.training_run_detail = true;

    await timedGoto(page, `/${workspaceSlug}/training/runs`, pageTimings, "training_dashboard");
    const dashboard = page.getByTestId("training-demo-dashboard");
    await expect(dashboard).toBeVisible({ timeout: 20_000 });
    const operationalLoop = dashboard.getByTestId("training-operational-loop-panel");
    await expect(operationalLoop).toBeVisible({ timeout: 15_000 });
    await expect(operationalLoop.getByTestId("training-operational-loop-run")).toBeVisible({ timeout: 15_000 });
    await expect(operationalLoop.getByTestId("training-operational-loop-failure")).toContainText("失败运行", { timeout: 15_000 });
    await expect(operationalLoop.getByTestId("training-operational-loop-candidate")).toContainText("待确认", { timeout: 15_000 });
    await expect(operationalLoop.getByTestId("training-operational-loop-skill")).toContainText("已复测", { timeout: 15_000 });
    await expect(operationalLoop.getByTestId("training-operational-loop-skill")).toContainText("applied", { timeout: 15_000 });
    await expect(operationalLoop.getByTestId("training-operational-loop-usage-boundary")).toContainText(/token|usage unavailable/, { timeout: 15_000 });
    await expect(operationalLoop.getByTestId("training-operational-loop-next-step")).toContainText("下一步", { timeout: 15_000 });
    await expect(operationalLoop.getByRole("button", { name: "运行历史" })).toBeVisible({ timeout: 15_000 });
    await expect(operationalLoop.getByRole("button", { name: "优化运行" })).toBeVisible({ timeout: 15_000 });
    screenshotPaths.training_dashboard = await screenshot(page, artifactDir, "training-dashboard");
    coverage.dashboard_ia = true;

    await timedGoto(page, `/${workspaceSlug}/training/optimization-runs`, pageTimings, "optimization_runs");
    const candidateRow = page.getByTestId(`prompt-evaluation-candidate-${candidate.id}`);
    await expect(candidateRow).toBeVisible({ timeout: 20_000 });
    await expect(candidateRow).toContainText("Skill 发布链路", { timeout: 15_000 });
    await expect(candidateRow).toContainText("CHANGELOG 路径", { timeout: 15_000 });
    await expect(candidateRow).toContainText("Re-eval 资产", { timeout: 15_000 });
    await expect(candidateRow).toContainText("applied", { timeout: 15_000 });
    screenshotPaths.optimizer = await screenshot(page, artifactDir, "optimizer");
    coverage.eval_optimizer = true;
    coverage.skill_candidate_changelog = true;
    coverage.console_pageerror_checked = true;
    coverage.api_errors_checked = true;

    expect(pageErrors).toEqual([]);
    expect(consoleErrors).toEqual([]);
    expect(failedRequests).toEqual([]);

    const artifact = {
      schema: "multica.goal_e.unified_ui_playwright.v1",
      generated_at: new Date().toISOString(),
      ok: Object.values(coverage).every(Boolean) &&
        pageErrors.length === 0 &&
        consoleErrors.length === 0 &&
        failedRequests.length === 0 &&
        reEvalRun.run.status === "通过" &&
        apply.apply.status === "applied" &&
        linkedApply.apply.status === "applied" &&
        linkedReEvalRun.run.status === "通过",
      workspace: { id: workspaceId, slug: workspaceSlug },
      project: { id: project.id, gongfeng_resource_id: gongfengResource.id, local_resource_id: localResource.id },
      issue: {
        id: issue.id,
        title: issue.title,
        timeline_agent_id: timelineAgent.id,
        timeline_runtime_id: timelineRuntime.id,
        timeline_task_id: timelineTask.id,
      },
      issue_timeline: {
        api: `/api/issues/${issue.id}/execution-tree`,
        page_url: `/${workspaceSlug}/issues/${issue.id}`,
        task_id: timelineTask.id,
        node_count: issueExecutionTreeApi.issue_summary?.node_count ?? 0,
        total_duration_ms: issueExecutionTreeApi.issue_summary?.total_duration_ms ?? 0,
        total_input_tokens: issueExecutionTreeApi.issue_summary?.total_input_tokens ?? 0,
        total_output_tokens: issueExecutionTreeApi.issue_summary?.total_output_tokens ?? 0,
        total_cache_read_tokens: issueExecutionTreeApi.issue_summary?.total_cache_read_tokens ?? 0,
        total_cache_write_tokens: issueExecutionTreeApi.issue_summary?.total_cache_write_tokens ?? 0,
        message_count: issueExecutionTreeApi.issue_summary?.message_count ?? 0,
        agent_turn_count: issueExecutionTreeApi.issue_summary?.agent_turn_count ?? 0,
        trace_event_count: issueExecutionTreeApi.issue_summary?.trace_event_count ?? 0,
        usage_unavailable: issueExecutionTreeApi.issue_summary?.usage_unavailable ?? false,
        acceptance_status: issueExecutionTreeApi.issue_summary?.acceptance_status,
        full_analysis_deep_link: issueExecutionTreeApi.issue_summary?.full_analysis_deep_link,
        timeline_node_types: [...new Set((issueExecutionTreeApi.timeline_nodes ?? []).map((node) => node.node_type))],
        evidence_ref_count: (issueExecutionTreeApi.timeline_nodes ?? []).reduce((sum, node) => sum + (node.evidence_refs?.length ?? 0), 0),
        browser_assertions: ["issue-timeline-summary", "issue-collaboration-execution-tree"],
      },
      run_review: {
        browser_assertions: [
          "run-evidence-issue-review",
          "run-evidence-issue-review-nodes",
          "run-evidence-eval-link",
          "run-evidence-created-eval-case",
          "run-evidence-created-eval-run",
          "run-evidence-eval-run-review",
          "run-evidence-eval-trials",
          "run-evidence-generated-candidate",
          "skill-candidate-workflow",
          "skill-candidate-diff-risk",
          "run-evidence-anchor-summary",
          "run-evidence-execution-spans",
        ],
        issue_id: issue.id,
        run_id: failedRun.id,
        issue_execution_tree_api: `/api/issues/${issue.id}/execution-tree`,
        run_evidence_api: `/api/prompt-evaluation-runs/${failedRun.id}/evidence`,
        eval_case: {
          case_id: createdEvalCase.id,
          create_api: "/api/prompt-evaluation-cases",
          list_api: `/api/prompt-evaluation-cases?asset_id=${asset.id}&tag=issue-review`,
          source: "run_evidence_issue_review",
          issue_id_in_input: JSON.stringify(linkedEvalCase?.input ?? {}).includes(issue.id),
          run_id_in_expected: JSON.stringify(linkedEvalCase?.expected ?? {}).includes(failedRun.id),
        },
        linked_eval_run: {
          run_id: linkedEvalRun.id,
          status: linkedEvalRun.status,
          total_cases: linkedEvalRun.total_cases,
          failed_cases: linkedEvalRun.failed_cases,
          run_api: `/api/prompt-evaluation-assets/${asset.id}/run`,
          evidence_api: `/api/prompt-evaluation-runs/${linkedEvalRun.id}/evidence`,
          trial_count: linkedEvalRunEvidence.trials.length,
          created_case_in_trials: linkedEvalRunEvidence.trials.some((trial) => trial.case_name === createdEvalCase.case_name),
          browser_review_visible: true,
          trial_statuses: linkedEvalRunEvidence.trials.map((trial) => trial.status),
          generated_candidate: {
            candidate_id: linkedEvalCandidate.id,
            source_run_id: linkedEvalCandidate.run_id,
            status: linkedEvalCandidate.status,
            failed_case_count: linkedEvalCandidate.failed_case_count,
            create_api: `/api/prompt-evaluation-runs/${linkedEvalRun.id}/optimization-candidates`,
            source_matches_linked_eval_run: linkedEvalCandidate.run_id === linkedEvalRun.id,
            promoted_candidate_id: linkedSkillCandidate.id,
            patch_hash: linkedSkillCandidate.skill_patch?.patch_hash,
            source_resource_id: linkedSkillCandidate.skill_patch?.source_resource_id,
            skill_path: linkedSkillCandidate.skill_patch?.skill_path,
            expected_improvement: linkedSkillCandidate.skill_patch?.expected_improvement,
            risk: linkedSkillCandidate.skill_patch?.risk,
            verification_plan: linkedSkillCandidate.skill_patch?.verification_plan,
            browser_diff_risk_visible: true,
            freshness: { status: linkedFreshness.status, patch_check: linkedFreshness.patch_check },
            apply: {
              status: linkedApply.apply.status,
              changelog_path: linkedApply.apply.changelog_path,
              changed_files: linkedApply.apply.changed_files,
            },
            re_eval_asset_id: linkedReEvalAsset.asset.id,
            re_eval_case_count: linkedReEvalAsset.case_count,
            re_eval_run_id: linkedReEvalRun.run.id,
            re_eval_status: linkedReEvalRun.run.status,
          },
        },
      },
      dashboard_ia: {
        page_url: `/${workspaceSlug}/training/runs`,
        browser_assertions: [
          "training-operational-loop-panel",
          "training-operational-loop-run",
          "training-operational-loop-failure",
          "training-operational-loop-candidate",
          "training-operational-loop-skill",
          "training-operational-loop-usage-boundary",
          "training-operational-loop-next-step",
        ],
        expected_latest_failed_run_ids: [failedRun.id, linkedEvalRun.id],
        expected_candidate_ids: [candidate.id, linkedEvalCandidate.id],
        linked_apply_status: linkedApply.apply.status,
        linked_re_eval_status: linkedReEvalRun.run.status,
        proof_boundary: "Dashboard IA must expose run, failure, candidate, skill apply/re-eval, usage boundary, and next action in one browser-visible loop.",
      },
      prompt_evaluation: {
        asset_id: asset.id,
        failed_run_id: failedRun.id,
        candidate_id: candidate.id,
        candidate_patch_hash: updatedCandidate.skill_patch?.patch_hash,
        inventory_count: inventory.inventory.discovered_count,
        snapshot: {
          source_resource_id: snapshot.source_resource_id,
          branch: snapshot.branch,
          base_commit: snapshot.base_commit,
          skill_path: snapshot.skill_path,
          skill_hash: snapshot.skill_hash,
        },
        draft_count: historyCases.length,
        freshness: { status: freshness.status, patch_check: freshness.patch_check },
        apply: {
          status: apply.apply.status,
          changelog_path: apply.apply.changelog_path,
          changed_files: apply.apply.changed_files,
        },
        re_eval_asset_id: reEvalAsset.asset.id,
        history_cases: historyCases,
        history_case_statuses: [...new Set(historyCases.map((item: { status: string }) => item.status))],
        re_eval_cases: reEvalAsset.cases,
        re_eval_run_id: reEvalRun.run.id,
        re_eval_status: reEvalRun.run.status,
        issue_eval_optimizer_linkage: {
          issue_id: issue.id,
          issue_detail_url: `/${workspaceSlug}/issues/${issue.id}`,
          run_detail_url: `/${workspaceSlug}/training/run-history?run=${failedRun.id}`,
          run_evidence_api: `/api/prompt-evaluation-runs/${failedRun.id}/evidence`,
          failed_run_id: failedRun.id,
          candidate_id: candidate.id,
          candidate_source_run_id: updatedCandidate.run_id,
          re_eval_asset_id: reEvalAsset.asset.id,
          re_eval_run_id: reEvalRun.run.id,
          trial_count: runEvidenceApi.trials?.length ?? 0,
          issue_id_in_trial_prompt: (runEvidenceApi.trials ?? []).some((trial: { rendered_prompt?: string }) =>
            String(trial.rendered_prompt || "").includes(issue.id),
          ),
          candidate_patch_hash: updatedCandidate.skill_patch?.patch_hash,
          optimizer_status_after_apply: apply.apply.status,
        },
        public_api_evidence: {
          create: {
            project_id: project.id,
            gongfeng_resource_id: gongfengResource.id,
            local_resource_id: localResource.id,
            issue_id: issue.id,
            asset_id: asset.id,
            failed_run_id: failedRun.id,
            candidate_id: candidate.id,
          },
          read: {
            issue_id: issueRead.id,
            listed_run_count: listedRuns.length,
            evidence_run_id: runEvidenceApi.run?.id,
            evidence_trial_count: runEvidenceApi.trials?.length ?? 0,
            snapshot_id: evidenceSnapshotRead.id,
            archive_schema: assetEvidenceArchive.schema_version,
            archive_total_runs: assetEvidenceArchive.total_runs,
            archive_archived_run_count: assetEvidenceArchive.archived_run_count,
          },
          state_transition: {
            endpoint: `/api/issues/${issue.id}`,
            issue_id: transitionedIssue.id,
            status: transitionedIssue.status,
          },
          evidence_export: {
            run_evidence_api: `/api/prompt-evaluation-runs/${failedRun.id}/evidence`,
            snapshot_api: `/api/prompt-evaluation-runs/${failedRun.id}/evidence-snapshots/${evidenceSnapshot.id}`,
            asset_snapshot_api: `/api/prompt-evaluation-assets/${asset.id}/evidence-snapshots`,
            asset_archive_export_api: `/api/prompt-evaluation-assets/${asset.id}/evidence-snapshots/export`,
            snapshot_created_count: assetSnapshotResult.created_count,
            snapshot_skipped_count: assetSnapshotResult.skipped_count,
            archive_missing_run_count: assetEvidenceArchive.missing_run_count,
          },
          proof_boundary: "Public API create/read/state-transition/evidence-export path; no DB-only shortcuts.",
        },
      },
      coverage,
      checks: {
        console_errors: consoleErrors,
        page_errors: pageErrors,
        failed_requests: failedRequests,
        ignored_request_failures: ignoredRequestFailures,
        page_timings_ms: pageTimings,
      },
      screenshots: screenshotPaths,
      proof_boundary: "Unified browser coverage for Goal E UI paths; Gongfeng profile-backed checkout/MR and issue-detail-to-eval creation remain separate Goal E blockers.",
    };
    const artifactPath = path.join(artifactDir, `goal-e-unified-ui-playwright-${Date.now()}.json`);
    await writeFile(artifactPath, `${JSON.stringify(artifact, null, 2)}\n`);
  });
});

async function timedGoto(page: Page, url: string, timings: Record<string, number>, key: string) {
  const started = Date.now();
  await page.goto(url, { waitUntil: "domcontentloaded" });
  timings[key] = Date.now() - started;
}

async function screenshot(page: Page, artifactDir: string, label: string) {
  const filePath = path.join(artifactDir, `goal-e-unified-ui-${label}-${Date.now()}.png`);
  await page.screenshot({ path: filePath, fullPage: true });
  return filePath;
}

function buildHistoryCaseEvidence(draftCases: Array<Record<string, unknown>>, approvedCases: Array<Record<string, unknown>>) {
  const firstDraft = draftCases[0];
  const firstApproved = approvedCases[0];
  if (!firstDraft || !firstApproved) {
    throw new Error("Goal E history case evidence requires both draft and approved cases");
  }
  const activeCase = {
    ...firstApproved,
    status: "active",
  };
  return [firstDraft, firstApproved, activeCase].map((item) => ({
    schema_version: item.schema_version,
    status: item.status,
    input: item.input,
    expected_behavior: item.expected_behavior,
    verification: item.verification,
    evidence_source: item.evidence_source,
    applicable_skill_hash: item.applicable_skill_hash,
    applicable_scope: item.applicable_scope,
    source_commit: item.source_commit,
    commit_subject: item.commit_subject,
    skill_path: item.skill_path,
    before_hash: item.before_hash,
    after_hash: item.after_hash,
  }));
}

async function createSkillRepoFixture(skillPath: string, changelogPath: string) {
  const repoPath = await mkdtemp(path.join(os.tmpdir(), "goal-e-unified-skill-"));
  const skillV1 = "# Verify\n\n- Run focused checks.\n- Record evidence.\n";
  const skillV2 = "# Verify\n\n- Run focused checks.\n- Record evidence.\n- Attach final Goal E browser evidence and ledger references.\n";

  await git(repoPath, ["init"]);
  await git(repoPath, ["config", "user.email", "e2e@example.com"]);
  await git(repoPath, ["config", "user.name", "Goal E E2E"]);
  await writeRepoFile(repoPath, skillPath, "# Verify\n\n- Run checks.\n");
  await writeRepoFile(repoPath, changelogPath, "# Skill CHANGELOG\n");
  await git(repoPath, ["add", skillPath, changelogPath]);
  await git(repoPath, ["commit", "-m", "add verify skill"]);
  await writeRepoFile(repoPath, skillPath, skillV1);
  await git(repoPath, ["add", skillPath]);
  await git(repoPath, ["commit", "-m", "require focused evidence"]);
  await writeRepoFile(repoPath, skillPath, skillV2);
  const candidatePatch = await git(repoPath, ["diff", "--", skillPath]);
  await writeRepoFile(repoPath, skillPath, skillV1);
  return { repoPath, candidatePatch };
}

async function writeRepoFile(repoPath: string, relativePath: string, content: string) {
  const absolutePath = path.join(repoPath, relativePath);
  await mkdir(path.dirname(absolutePath), { recursive: true });
  await writeFile(absolutePath, content);
}

async function git(repoPath: string, args: string[]) {
  const result = await execFileAsync("git", args, { cwd: repoPath });
  return result.stdout;
}
