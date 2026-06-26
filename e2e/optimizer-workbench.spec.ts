import { test, expect, type ConsoleMessage } from "@playwright/test";
import { authenticateBrowserSession } from "./helpers";
import { TestApiClient } from "./fixtures";

const E2E_WORKER = process.env.TEST_PARALLEL_INDEX ?? process.env.TEST_WORKER_INDEX ?? "0";
const E2E_RUN_ID = process.env.E2E_RUN_ID ?? `${Date.now().toString(36)}-${process.pid.toString(36)}`;
const E2E_ACCOUNT = process.env.E2E_ACCOUNT ?? `e2e-optimizer-workbench-${E2E_WORKER}-${E2E_RUN_ID}`;
const E2E_NAME = process.env.E2E_NAME ?? "Optimizer Workbench E2E User";
const E2E_WORKSPACE = process.env.E2E_WORKSPACE ?? `e2e-optimizer-workbench-workspace-${E2E_WORKER}-${E2E_RUN_ID}`;
const E2E_WORKSPACE_NAME = process.env.E2E_WORKSPACE_NAME ?? `Optimizer Workbench Workspace ${E2E_WORKER}`;
const ARTIFACT_PREFIX = "D192 Optimizer Workbench";

test.describe("optimizer case-centered workbench", () => {
  test.describe.configure({ timeout: 120_000 });

  let api: TestApiClient;
  let workspaceSlug: string;

  test.beforeEach(async ({ page }) => {
    api = new TestApiClient();
    await api.login(E2E_ACCOUNT, E2E_NAME);
    const workspace = await api.ensureWorkspace(E2E_WORKSPACE_NAME, E2E_WORKSPACE);
    await api.markUserOnboarded();
    workspaceSlug = workspace.slug;
    const token = api.getToken();
    if (!token) throw new Error("E2E login did not return an auth token");
    await authenticateBrowserSession(page, token, workspaceSlug);
  });

  test.afterEach(async () => {
    await api?.cleanupPromptArtifactsByPrefix(ARTIFACT_PREFIX);
    await api?.cleanup();
  });

  test("shows source case, failure, target skill, patch, freshness, apply, re-eval, and evidence", async ({ page }) => {
    const consoleErrors: string[] = [];
    const pageErrors: string[] = [];
    const failedRequests: string[] = [];
    page.on("console", (message: ConsoleMessage) => {
      if (message.type() === "error") consoleErrors.push(message.text());
    });
    page.on("pageerror", (error) => pageErrors.push(error.message));
    page.on("response", (response) => {
      if (response.status() >= 500) failedRequests.push(`${response.status()} ${response.url()}`);
    });

    const suffix = Date.now();
    const prompt = await api.createPromptForE2E(`${ARTIFACT_PREFIX} Prompt ${suffix}`, {
      content: "请处理 {{issue_title}}，输出必须包含真实验收证据。",
    });
    const dataset = await api.createPromptEvaluationAsset({
      name: `${ARTIFACT_PREFIX} Dataset ${suffix}`,
      description: "Optimizer workbench source dataset.",
      asset_type: "数据集",
      prompt_id: prompt.id,
      status: "启用",
      payload: {},
    });
    await api.createPromptEvaluationCase({
      asset_id: dataset.id,
      prompt_id: prompt.id,
      case_name: `${ARTIFACT_PREFIX} GOA-456 source case`,
      variables: { issue_title: "GOA-456 跨项目拆分失败复盘" },
      expected_contains: [`NEVER_MATCH_D192_${suffix}`],
      input: {
        issue: { id: "GOA-456", title: "跨项目拆分失败复盘" },
        run_review: { timeline_node_count: 6 },
      },
      expected: {
        expected_behavior: "识别 gateway 和 ida-deployment 子任务。",
        validation: "检查 DAG、子任务和 evidence refs。",
      },
      tags: ["GOA-456", "issue:GOA-456", "optimizer-workbench"],
      status: "active",
    });

    await api.runPromptEvaluationAsset(dataset.id);
    const runs = await api.listPromptEvaluationRuns({ asset_id: dataset.id, limit: 10 });
    const failedRun = runs.find((run) => run.status !== "通过");
    if (!failedRun) throw new Error("Expected the source evaluation run to fail");
    const candidate = await api.createPromptEvaluationOptimizationCandidate(failedRun.id);
    await api.updatePromptEvaluationOptimizationCandidate(candidate.id, {
      candidate_name: `${ARTIFACT_PREFIX} Skill Patch ${suffix}`,
      candidate_content: "增加跨服务依赖识别、子任务模板和 evidence refs 检查。",
      rationale: "失败原因：02-design 没有稳定拆出 gateway/ida-deployment 子任务。",
      skill_patch: {
        patch: `--- a/.codebuddy/skills/sop.eval/SKILL.md\n+++ b/.codebuddy/skills/sop.eval/SKILL.md\n@@\n+- D192 optimizer workbench marker ${suffix}\n`,
        source_snapshot: {
          schema_version: "multica.skill.snapshot.v1",
          provider: "gongfeng",
          repo: "ChainWeaver/ida/user-center",
          repo_path: "/tmp/d192-optimizer-workbench",
          branch: "v5.0.0_dev_sop",
          base_commit: "abc123def456",
          skill_path: ".codebuddy/skills/sop.eval/SKILL.md",
          skill_hash: "sha256:source-skill-hash",
          snapshot_time: new Date().toISOString(),
        },
        target_branch: "v5.0.0_dev_sop",
        skill_path: ".codebuddy/skills/sop.eval/SKILL.md",
        expected_improvement: "跨项目拆分准确率提升，gateway/ida-deployment 子任务不丢失。",
        risk: "可能让 02-design 输出更长，需要回归 token 和可读性。",
        verification_plan: "运行 freshness、apply、CHANGELOG、re-eval，并检查 evidence package。",
        publication_status: "draft",
      },
    });

    await page.goto(`/${workspaceSlug}/training/optimization-runs`, { waitUntil: "domcontentloaded" });
    const workbench = page.getByTestId("optimizer-case-workbench");
    await expect(workbench).toBeVisible({ timeout: 30_000 });
    await expect(workbench.getByText("优化 Skill 工作台")).toBeVisible();
    await expect(page.getByTestId("optimizer-workbench-source-case")).toContainText("GOA-456 source case", { timeout: 30_000 });
    await expect(page.getByTestId("optimizer-workbench-failure")).toContainText("02-design");
    await expect(page.getByTestId("optimizer-workbench-target-skill")).toContainText(".codebuddy/skills/sop.eval/SKILL.md");
    await expect(page.getByTestId("optimizer-workbench-candidate-patch")).toContainText("D192 optimizer workbench marker");
    await expect(page.getByTestId("optimizer-workbench-freshness")).toBeVisible();
    await expect(page.getByTestId("optimizer-workbench-apply")).toBeVisible();
    await expect(page.getByTestId("optimizer-workbench-re-eval")).toBeVisible();
    await expect(page.getByTestId("optimizer-workbench-evidence")).toContainText(candidate.id);

    expect(consoleErrors).toEqual([]);
    expect(pageErrors).toEqual([]);
    expect(failedRequests).toEqual([]);
  });
});
