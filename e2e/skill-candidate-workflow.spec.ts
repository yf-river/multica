import { test, expect, type ConsoleMessage } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { authenticateBrowserSession, waitForPageText } from "./helpers";
import { TestApiClient } from "./fixtures";
import {
  DEFAULT_E2E_ACCOUNT,
  DEFAULT_E2E_NAME,
  DEFAULT_E2E_PASSWORD,
  DEFAULT_E2E_WORKSPACE,
  DEFAULT_E2E_WORKSPACE_NAME,
} from "./test-identity";

test.describe("Skill candidate workflow", () => {
  let api: TestApiClient;
  let artifactPrefix: string;
  let workspaceSlug: string;

  test.beforeEach(async () => {
    api = new TestApiClient();
    await api.login(DEFAULT_E2E_ACCOUNT, DEFAULT_E2E_NAME, DEFAULT_E2E_PASSWORD);
    const workspace = await api.ensureWorkspace(DEFAULT_E2E_WORKSPACE_NAME, DEFAULT_E2E_WORKSPACE);
    workspaceSlug = workspace.slug;
    artifactPrefix = `Skill Candidate UI ${Date.now()}`;
    await api.cleanupPromptArtifactsByPrefix("Skill Candidate UI");
  });

  test.afterEach(async () => {
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
      if (message.type() === "error") consoleErrors.push(message.text());
    });
    page.on("pageerror", (error) => pageErrors.push(error.message));

    const token = api.getToken();
    if (!token) throw new Error("E2E login did not return an auth token");
    await authenticateBrowserSession(page, token, workspaceSlug);
    await page.goto(`/${workspaceSlug}/issues`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "新建任务", 60_000);

    const prompt = await api.createPromptForE2E(artifactPrefix, {
      name: `${artifactPrefix} prompt`,
      content: "请处理 {{issue_title}}，输出中文结论。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
    });
    const asset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} failing optimization asset`,
      description: "Current Skill candidate workflow fixture",
      asset_type: "测试套件",
      payload: {
        cases: [{
          case_name: "缺少 Skill 发布证据",
          variables: { issue_title: "登录失败" },
          expected_contains: ["skill snapshot hash", "re-eval run id"],
        }],
      },
      status: "启用",
    });
    const localRunRequestId = crypto.randomUUID();
    const localRun = await api.runPromptEvaluationAsset(asset.id, localRunRequestId);
    expect(await api.runPromptEvaluationAsset(asset.id, localRunRequestId)).toEqual(localRun);
    const failedRun = await expect
      .poll(async () => {
        const runs = await api.listPromptEvaluationRuns({ asset_id: asset.id });
        return runs.find((run) => run.status === "未通过") ?? null;
      }, { timeout: 15_000 })
      .not.toBeNull()
      .then(async () => (await api.listPromptEvaluationRuns({ asset_id: asset.id })).find((run) => run.status === "未通过")!);
    const candidateRequestId = crypto.randomUUID();
    const candidate = await api.createPromptEvaluationOptimizationCandidate(failedRun.id, candidateRequestId);
    expect(await api.createPromptEvaluationOptimizationCandidate(failedRun.id, candidateRequestId)).toEqual(candidate);

    await page.goto(`/${workspaceSlug}/evaluation/runs?run=${failedRun.id}`, { waitUntil: "domcontentloaded" });
    const runRow = page.getByTestId(`prompt-evaluation-run-${failedRun.id}`);
    await expect(runRow).toBeVisible({ timeout: 15_000 });
    const skillWorkflow = runRow.getByTestId(`skill-candidate-workflow-${candidate.id}`);
    await expect(skillWorkflow).toBeVisible({ timeout: 15_000 });
    for (const text of ["Skill 发布链路", "本地工蜂 checkout", "目标分支", "Skill 路径", "CHANGELOG 路径", "Re-eval 资产"]) {
      await expect(skillWorkflow).toContainText(text);
    }
    await expect(skillWorkflow.getByRole("button", { name: "Freshness" })).toBeEnabled();
    await expect(skillWorkflow.getByRole("button", { name: "Apply + CHANGELOG" })).toBeEnabled();
    await expect(skillWorkflow.getByRole("button", { name: "Prepare re-eval" })).toBeEnabled();
    await expect(skillWorkflow.getByRole("button", { name: "Run re-eval" })).toBeDisabled();

    const artifactDir = path.resolve(process.cwd(), "artifacts/acceptance");
    await mkdir(artifactDir, { recursive: true });
    await skillWorkflow.screenshot({
      path: path.join(artifactDir, `skill-candidate-workflow-${Date.now()}.png`),
    });

    const decisionActions = runRow.getByTestId("candidate-decision-actions");
    await expect(decisionActions.getByRole("button", { name: "发布为新提示词" })).toBeEnabled();
    await expect(decisionActions.getByRole("button", { name: "拒绝候选" })).toBeEnabled();
    let publishRequests = 0;
    await page.route("**/api/prompt-evaluation-optimization-candidates/*/publish", async (route) => {
      publishRequests += 1;
      if (publishRequests === 1) {
        await route.fetch();
        await route.abort("connectionfailed");
        return;
      }
      await route.continue();
    });
    await decisionActions.getByRole("button", { name: "发布为新提示词" }).click();
    const publishDialog = page.getByRole("alertdialog");
    await expect(publishDialog).toContainText("确认发布这个优化候选");
    await publishDialog.getByRole("button", { name: "发布为新提示词" }).click();
    await expect(runRow.getByTestId("run-evidence-candidate")).toContainText("已发布", { timeout: 15_000 });
    await expect(runRow.getByTestId("candidate-decision-actions")).toHaveCount(0);
    expect(publishRequests).toBe(2);
    expect(pageErrors).toEqual([]);
    expect(consoleErrors.filter((message) => !message.includes("ERR_CONNECTION_FAILED"))).toEqual([]);
  });
});
