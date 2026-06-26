import { test, expect, type ConsoleMessage } from "@playwright/test";
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
const E2E_ACCOUNT = process.env.E2E_ACCOUNT ?? `e2e-${E2E_WORKER}-${E2E_RUN_ID}`;
const E2E_NAME = process.env.E2E_NAME ?? "E2E User";
const E2E_WORKSPACE = process.env.E2E_WORKSPACE ?? `e2e-workspace-${E2E_WORKER}-${E2E_RUN_ID}`;
const E2E_WORKSPACE_NAME = process.env.E2E_WORKSPACE_NAME ?? `E2E Workspace ${E2E_WORKER}`;

test.describe("Skill candidate workflow", () => {
  let api: TestApiClient;
  let artifactPrefix: string;
  let workspaceSlug: string;
  let tempRepoPath: string | null = null;

  test.beforeEach(async ({ page }) => {
    api = new TestApiClient();
    await api.login(E2E_ACCOUNT, E2E_NAME);
    const workspace = await api.ensureWorkspace(E2E_WORKSPACE_NAME, E2E_WORKSPACE);
    await api.markUserOnboarded();
    workspaceSlug = workspace.slug;
    artifactPrefix = `GoalD Skill UI ${Date.now()}`;
    await api.cleanupPromptArtifactsByPrefix("GoalD Skill UI");
    const token = api.getToken();
    if (!token) {
      throw new Error("E2E login did not return an auth token");
    }
    await authenticateBrowserSession(page, token, workspaceSlug);
    await page.goto(`/${workspaceSlug}/issues`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "新建任务", 60_000);
  });

  test.afterEach(async () => {
    if (tempRepoPath) {
      await rm(tempRepoPath, { recursive: true, force: true });
      tempRepoPath = null;
    }
    if (api && artifactPrefix) {
      await api.cleanupPromptArtifactsByPrefix(artifactPrefix);
      await api.cleanup();
    }
  });

  test("renders guarded publish and re-eval controls for optimization candidates", async ({ page }) => {
    test.setTimeout(90_000);
    const consoleErrors: string[] = [];
    const pageErrors: string[] = [];
    page.on("console", (message: ConsoleMessage) => {
      if (message.type() === "error") {
        consoleErrors.push(message.text());
      }
    });
    page.on("pageerror", (error) => {
      pageErrors.push(error.message);
    });

    const prompt = await api.createPromptForE2E(artifactPrefix, {
      name: `${artifactPrefix} prompt`,
      content: "请处理 {{issue_title}}，输出中文结论。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
    });
    const asset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} failing optimization asset`,
      description: "Goal D Skill workflow Playwright fixture",
      asset_type: "优化运行",
      payload: {
        cases: [
          {
            名称: "缺少 skill 发布证据",
            变量: { issue_title: "登录失败" },
            期望包含: ["skill snapshot hash", "re-eval run id"],
          },
        ],
      },
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

    await page.goto(`/${workspaceSlug}/training/optimization-runs`, { waitUntil: "domcontentloaded" });
    const candidateRow = page.getByTestId(`prompt-evaluation-candidate-${candidate.id}`);
    await expect(candidateRow).toBeVisible({ timeout: 15_000 });
    await expect(candidateRow).toContainText("待确认", { timeout: 15_000 });

    const skillWorkflow = page.getByTestId(`skill-candidate-workflow-${candidate.id}`);
    await expect(skillWorkflow).toBeVisible({ timeout: 15_000 });
    await expect(skillWorkflow).toContainText("Skill 发布链路");
    await expect(skillWorkflow).toContainText("本地工蜂 checkout");
    await expect(skillWorkflow).toContainText("目标分支");
    await expect(skillWorkflow).toContainText("Skill 路径");
    await expect(skillWorkflow).toContainText("CHANGELOG 路径");
    await expect(skillWorkflow).toContainText("Re-eval 资产");
    await expect(skillWorkflow.getByRole("button", { name: "Freshness" })).toBeEnabled();
    await expect(skillWorkflow.getByRole("button", { name: "Apply + CHANGELOG" })).toBeEnabled();
    await expect(skillWorkflow.getByRole("button", { name: "Prepare re-eval" })).toBeEnabled();
    await expect(skillWorkflow.getByRole("button", { name: "Run re-eval" })).toBeDisabled();

    const artifactDir = path.resolve(process.cwd(), "artifacts/acceptance");
    await mkdir(artifactDir, { recursive: true });
    await skillWorkflow.screenshot({
      path: path.join(artifactDir, `goal-d-skill-ui-workflow-playwright-${Date.now()}.png`),
    });

    expect(pageErrors).toEqual([]);
    expect(consoleErrors).toEqual([]);
  });

  test("runs the local checkout skill API chain through apply changelog and re-eval", async () => {
    test.setTimeout(120_000);
    tempRepoPath = await mkdtemp(path.join(os.tmpdir(), "goal-d-skill-e2e-"));
    const skillPath = ".codebuddy/skills/05-verify/SKILL.md";
    const changelogPath = ".codebuddy/skills/05-verify/CHANGELOG.md";
    const skillV1 = "# Verify\n\n- Run focused checks.\n- Record evidence.\n";
    const skillV2 = "# Verify\n\n- Run focused checks.\n- Record evidence.\n- Attach ledger references in acceptance artifacts.\n";

    await git(tempRepoPath, ["init"]);
    await git(tempRepoPath, ["config", "user.email", "e2e@example.com"]);
    await git(tempRepoPath, ["config", "user.name", "Goal D E2E"]);
    await writeRepoFile(tempRepoPath, skillPath, "# Verify\n\n- Run checks.\n");
    await writeRepoFile(tempRepoPath, changelogPath, "# Skill CHANGELOG\n");
    await git(tempRepoPath, ["add", skillPath, changelogPath]);
    await git(tempRepoPath, ["commit", "-m", "add verify skill"]);
    await writeRepoFile(tempRepoPath, skillPath, skillV1);
    await git(tempRepoPath, ["add", skillPath]);
    await git(tempRepoPath, ["commit", "-m", "require focused evidence"]);
    await writeRepoFile(tempRepoPath, skillPath, skillV2);
    const candidatePatch = await git(tempRepoPath, ["diff", "--", skillPath]);
    await writeRepoFile(tempRepoPath, skillPath, skillV1);

    const prompt = await api.createPromptForE2E(artifactPrefix, {
      name: `${artifactPrefix} skill echo prompt`,
      content: "{{expected_behavior}}\n{{verification}}\n{{evidence_source}}\n{{issue_title}}",
      variables: [
        { name: "expected_behavior", label: "Expected behavior", required: false },
        { name: "verification", label: "Verification", required: false },
        { name: "evidence_source", label: "Evidence source", required: false },
        { name: "issue_title", label: "Issue title", required: false },
      ],
    });
    const asset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} full skill chain asset`,
      description: "Goal D full local checkout skill chain fixture",
      asset_type: "优化运行",
      payload: {
        cases: [
          {
            名称: "force optimizer candidate",
            变量: { issue_title: "登录失败" },
            期望包含: ["__missing_goal_d_marker__"],
          },
        ],
      },
      status: "启用",
    });
    const project = await api.createProject(`${artifactPrefix} project`);
    const resource = await api.createProjectResource(project.id, {
      resource_type: "local_directory",
      label: `${artifactPrefix} local skill checkout`,
      resource_ref: {
        local_path: tempRepoPath,
        daemon_id: `goal-d-skill-e2e-${Date.now()}`,
      },
    });

    const inventory = await api.createPromptEvaluationSkillInventory(asset.id, {
      source_resource_id: resource.id,
      skill_root: ".codebuddy/skills",
    });
    expect(inventory.inventory.discovered_count).toBeGreaterThanOrEqual(1);
    expect(inventory.inventory.items.some((item: { skill_path: string }) => item.skill_path === skillPath)).toBeTruthy();

    const snapshotResult = await api.createPromptEvaluationSkillSnapshot(asset.id, {
      source_resource_id: resource.id,
      skill_path: skillPath,
    });
    const snapshot = snapshotResult.snapshot;
    expect(snapshot.source_resource_id).toBe(resource.id);

    const draftsResult = await api.createPromptEvaluationSkillCaseDrafts(asset.id, {
      repo_path: tempRepoPath,
      skill_path: skillPath,
      limit: 5,
      auto_approve: true,
    });
    expect(draftsResult.created_count).toBeGreaterThanOrEqual(1);
    expect(draftsResult.drafts.every((draft: { status: string }) => draft.status === "approved")).toBeTruthy();
    await api.updatePromptLibraryItem(prompt.id, {
      content: draftsResult.drafts
        .map((draft: { expected_behavior: string; verification: string; evidence_source: string }) => [
          draft.expected_behavior,
          draft.verification,
          draft.evidence_source,
        ].join("\n"))
        .join("\n\n"),
      tags: ["E2E", "Goal D", "skill re-eval"],
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
    const updatedCandidate = await api.updatePromptEvaluationOptimizationCandidate(candidate.id, {
      candidate_name: `${artifactPrefix} skill patch candidate`,
      candidate_content: "Skill patch candidate stored in skill_patch.patch.",
      rationale: "Patch adds ledger-reference evidence discipline to the verify skill.",
      edit_note: "E2E fixture replaces generated prompt candidate with a skill patch.",
      skill_patch: {
        patch: candidatePatch,
        source_snapshot: snapshot,
        source_resource_id: resource.id,
        repo_path: tempRepoPath,
        target_branch: snapshot.branch,
        skill_path: skillPath,
        changelog_path: changelogPath,
        expected_improvement: "Improve verification evidence discipline.",
        risk: "Low-risk skill instruction addition in a temporary local checkout fixture.",
        verification_plan: "Run Goal D full local checkout E2E re-eval after apply.",
        publication_status: "draft",
      },
    });
    expect(updatedCandidate.skill_patch?.patch).toContain("Attach ledger references");
    expect(updatedCandidate.skill_patch?.source_snapshot?.skill_hash).toBe(snapshot.skill_hash);

    const freshness = await api.checkPromptEvaluationSkillCandidateFreshness(candidate.id, {
      source_resource_id: resource.id,
    });
    expect(freshness.status).toBe("fresh");
    expect(freshness.patch_check).toBe("not_needed");

    const apply = await api.applyPromptEvaluationSkillCandidate(candidate.id, {
      source_resource_id: resource.id,
      rollback_plan: "Reverse the candidate patch and remove the generated CHANGELOG entry.",
    });
    expect(apply.apply.status, JSON.stringify(apply.apply, null, 2)).toBe("applied");
    expect(apply.apply.skill_hash_after).not.toBe(snapshot.skill_hash);
    expect(apply.apply.changed_files.some((item: string) => item.includes(skillPath))).toBeTruthy();
    expect(apply.apply.changed_files.some((item: string) => item.includes(changelogPath))).toBeTruthy();

    const reEvalAsset = await api.preparePromptEvaluationSkillReEvalAsset(candidate.id, {
      source_resource_id: resource.id,
      snapshot,
      include_draft: false,
    });
    expect(reEvalAsset.case_count).toBeGreaterThanOrEqual(1);
    expect(reEvalAsset.re_eval_snapshot.skill_hash).toBe(apply.apply.skill_hash_after);

    const reEvalRun = await api.runPromptEvaluationSkillReEval(candidate.id, {
      asset_id: reEvalAsset.asset.id,
    });
    expect(reEvalRun.run.status, JSON.stringify(reEvalRun.run, null, 2)).toBe("通过");
    expect(reEvalRun.re_eval_run.proof_scope).toBe("local_prompt_evaluation_run");
    expect(reEvalRun.re_eval_snapshot.skill_hash).toBe(apply.apply.skill_hash_after);

    const artifactDir = path.resolve(process.cwd(), "artifacts/acceptance");
    await mkdir(artifactDir, { recursive: true });
    const artifactPath = path.join(artifactDir, `goal-d-skill-full-local-e2e-${Date.now()}.json`);
    await writeFile(artifactPath, `${JSON.stringify({
      repo_path: tempRepoPath,
      project_id: project.id,
      source_resource_id: resource.id,
      asset_id: asset.id,
      run_id: failedRun.id,
      candidate_id: candidate.id,
      skill_patch: {
        schema_version: updatedCandidate.skill_patch?.schema_version,
        patch_hash: updatedCandidate.skill_patch?.patch_hash,
        patch_bytes: updatedCandidate.skill_patch?.patch_bytes,
        source_snapshot_hash: updatedCandidate.skill_patch?.source_snapshot?.skill_hash,
        default_patch_source: "candidate.skill_patch.patch",
      },
      inventory_count: inventory.inventory.discovered_count,
      snapshot,
      draft_count: draftsResult.created_count,
      freshness: {
        status: freshness.status,
        patch_check: freshness.patch_check,
        head_commit: freshness.head_commit,
      },
      apply: {
        status: apply.apply.status,
        skill_hash_after: apply.apply.skill_hash_after,
        changed_files: apply.apply.changed_files,
      },
      re_eval_asset_id: reEvalAsset.asset.id,
      re_eval_run_id: reEvalRun.run.id,
      re_eval_status: reEvalRun.run.status,
      proof_scope: reEvalRun.re_eval_run.proof_scope,
    }, null, 2)}\n`);
  });
});

async function writeRepoFile(repoPath: string, relativePath: string, content: string) {
  const absolutePath = path.join(repoPath, relativePath);
  await mkdir(path.dirname(absolutePath), { recursive: true });
  await writeFile(absolutePath, content);
}

async function git(repoPath: string, args: string[]) {
  const result = await execFileAsync("git", args, { cwd: repoPath });
  return result.stdout;
}
