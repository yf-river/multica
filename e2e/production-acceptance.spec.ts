import { test, expect } from "@playwright/test";
import { TRAINING_ROUTES } from "./training-routes";
import { TestApiClient } from "./fixtures";

const workspaceSlug = process.env.ACCEPTANCE_WORKSPACE_SLUG
  || process.env.REAL_AGENT_E2E_WORKSPACE
  || "ai-studio";
const account = process.env.ACCEPTANCE_DEMO_ACCOUNT
  || process.env.REAL_AGENT_E2E_ACCOUNT
  || "develop";
const password = process.env.ACCEPTANCE_DEMO_PASSWORD
  || process.env.REAL_AGENT_E2E_PASSWORD
  || "develop123";
const frontendURL = process.env.ACCEPTANCE_FRONTEND_URL
  || process.env.PLAYWRIGHT_BASE_URL
  || process.env.FRONTEND_ORIGIN
  || "http://localhost:3000";
const workspaceName = process.env.E2E_WORKSPACE_NAME || "AI Studio 工作区";
const evidencePrefix = "生产验收训练证据";
const ROUTE_INTRO_TITLES: Record<string, string> = {
  datasets: "数据集题库",
  "test-suites": "测试套件回归",
  "evaluation-runs": "评测记录",
};
const ROUTE_OPERATING_TEXT: Record<string, string> = {
  datasets: "样本入库、版本快照、下游复用",
  "test-suites": "固定试卷、断言回归、失败定位",
  "evaluation-runs": "运行检索、证据展开、人工复核",
};
const routeByPath = (path: (typeof TRAINING_ROUTES)[number]["path"]) => {
  const route = TRAINING_ROUTES.find((item) => item.path === path);
  if (!route) throw new Error(`missing training route fixture: ${path}`);
  return route;
};
const PROMPTS_ROUTE = routeByPath("prompts");
const DATASETS_ROUTE = routeByPath("datasets");
const TEST_SUITES_ROUTE = routeByPath("test-suites");

async function prepareTrainingDashboardEvidence() {
  const api = new TestApiClient();
  await api.login(account, "AI Studio 开发账号");
  await api.ensureWorkspace(workspaceName, workspaceSlug);
  await api.markUserOnboarded();
  await api.cleanupPromptArtifactsByPrefix(evidencePrefix);
  await api.ensureOnlineCodexRuntime(`${evidencePrefix} Codex Runtime`);

  const suffix = Date.now();
  const prompt = await api.createPromptLibraryItem({
    name: `${evidencePrefix} ${suffix}`,
    description: "生产部署验收前通过公开 API 准备的训练评估证据。",
    prompt_type: "需求澄清",
    content: "请用中文澄清 {{issue_title}}，输出目标、边界、验收条件和风险。",
    variables: [{ name: "issue_title", label: "任务标题", required: true }],
    tags: ["生产验收", "训练与评估"],
    status: "启用",
  });
  const dataset = await api.createPromptEvaluationAsset({
    prompt_id: prompt.id,
    name: `${evidencePrefix} 数据集 ${suffix}`,
    description: "生产验收数据集。",
    asset_type: "数据集",
    payload: {
      schema: "multica.training_evaluation.payload.v1",
      schema_version: 1,
      语义版本: "multica.training_evaluation.v1",
      cases: [{
        case_name: "登录失败澄清",
        variables: { issue_title: "user-center 登录失败" },
        expected_contains: ["验收条件", "trace/task id"],
        tags: ["生产验收", "数据集"],
      }],
    },
    status: "启用",
  });
  const suite = await api.createPromptEvaluationAsset({
    prompt_id: prompt.id,
    name: `${evidencePrefix} 测试套件 ${suffix}`,
    description: "生产验收测试套件。",
    asset_type: "测试套件",
    payload: {
      schema: "multica.training_evaluation.payload.v1",
      schema_version: 1,
      语义版本: "multica.training_evaluation.v1",
      linked_dataset_ids: [dataset.id],
      cases: [{
        case_name: "真实智能体证据样本",
        variables: { issue_title: "user-center 登录失败" },
        expected_contains: ["需求澄清结论", "风险", "测试证据", "下一步建议"],
        tags: ["生产验收", "智能体"],
      }],
    },
    status: "启用",
  });
  const optimizationAsset = await api.createPromptEvaluationAsset({
    prompt_id: prompt.id,
    name: `${evidencePrefix} 候选生成套件 ${suffix}`,
    description: "生产验收失败测试套件，用于生成优化候选。",
    asset_type: "测试套件",
    payload: {
      schema: "multica.training_evaluation.payload.v1",
      schema_version: 1,
      语义版本: "multica.training_evaluation.v1",
      cases: [{
        case_name: "缺失 trace 的优化样本",
        variables: { issue_title: "user-center 登录失败" },
        expected_contains: ["这个断言用于制造失败候选", "trace/task id"],
        tags: ["生产验收", "优化候选"],
      }],
    },
    status: "启用",
  });

  const agentRun = await api.runPromptEvaluationAssetAgent(suite.id);
  await api.completePromptEvaluationAgentTask(agentRun.run);
  const syncedRun = await api.syncPromptEvaluationRun(agentRun.run.id);
  const snapshot = await api.createPromptEvaluationEvidenceSnapshot(syncedRun.id);
  await api.runPromptEvaluationAsset(optimizationAsset.id);
  const failedRun = await expect
    .poll(async () => {
      const runs = await api.listPromptEvaluationRuns({ asset_id: optimizationAsset.id });
      return runs.find((run) => run.status === "未通过") ?? null;
    }, { timeout: 15000 })
    .not.toBeNull()
    .then(async () => (await api.listPromptEvaluationRuns({ asset_id: optimizationAsset.id })).find((run) => run.status === "未通过")!);
  const candidate = await api.createPromptEvaluationOptimizationCandidate(failedRun.id);

  return {
    prompt,
    dataset,
    suite,
    optimizationAsset,
    syncedRun,
    snapshot,
    failedRun,
    candidate,
  };
}

async function expectTrainingRouteShell(page, route: (typeof TRAINING_ROUTES)[number]) {
  const routeIntroTitle = ROUTE_INTRO_TITLES[route.path];
  const hasRouteIntro = Boolean(routeIntroTitle);
  await expect(page.getByTestId("training-page-shell")).toHaveCount(1);
  await expect(page.getByTestId("training-tab-strip")).toHaveCount(0);
  await expect(page.getByTestId(`training-route-${route.path}`)).toHaveCount(1);
  await expect(page.getByTestId(`training-route-intro-${route.path}`)).toHaveCount(hasRouteIntro ? 1 : 0);
  await expect(page.getByTestId(`training-route-panel-${route.path}`)).toHaveCount(hasRouteIntro ? 1 : 0);
  await expect(page.getByTestId(`training-route-operating-model-${route.path}`)).toHaveCount(hasRouteIntro ? 1 : 0);
  if (routeIntroTitle) {
    await expect(page.getByTestId(`training-route-intro-${route.path}`)).toContainText(routeIntroTitle);
    await expect(page.getByTestId(`training-route-operating-model-${route.path}`)).toContainText(ROUTE_OPERATING_TEXT[route.path]!);
    await expect(page.getByTestId(`training-route-operating-step-${route.path}-1`)).toBeVisible();
    await expect(page.getByTestId(`training-route-operating-step-${route.path}-2`)).toBeVisible();
    await expect(page.getByTestId(`training-route-operating-step-${route.path}-3`)).toBeVisible();
  }
  await expect(page.getByTestId("prompt-library-editor")).toHaveCount(route.showPromptEditor ? 1 : 0);
  await expect(page.getByTestId("prompt-version-history")).toHaveCount(route.showPromptEditor ? 1 : 0);
  await expect(page.getByTestId("prompt-playground-workbench")).toHaveCount(route.showPromptPlayground ? 1 : 0);
  await expect(page.getByTestId("agent-playground-workbench")).toHaveCount(route.showAgentWorkbench ? 1 : 0);
  await expect(page.getByTestId("prompt-template-actions")).toHaveCount(route.path === "prompts" ? 1 : 0);
  await expect(page.getByRole("button", { name: "起草需求澄清模板" })).toHaveCount(route.path === "prompts" ? 1 : 0);
  await expect(page.getByRole("button", { name: "创建 user-center 需求澄清提示词" })).toHaveCount(0);
}

async function expectTrainingRouteSurvivesReload(page, route: (typeof TRAINING_ROUTES)[number]) {
  await page.reload({ waitUntil: "domcontentloaded" });
  await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/${route.path}$`), { timeout: 30000 });
  await expect(page.getByText(route.text).first()).toBeVisible({ timeout: 15000 });
  await expectTrainingRouteShell(page, route);
}

test.describe("生产部署验收", () => {
  test("验收账号可以看到训练评估运行看板和服务端证据快照", async ({ page }) => {
    test.setTimeout(180_000);
    const evidence = await prepareTrainingDashboardEvidence();
    expect(evidence.snapshot.id).toBeTruthy();
    expect(evidence.syncedRun.task_id).toBeTruthy();
    const next = `/${workspaceSlug}/run-reviews`;
    await page.addInitScript(() => {
      localStorage.setItem("multica:chat:isOpen", "false");
    });
    await page.goto(`${frontendURL}/login?next=${encodeURIComponent(next)}`, { waitUntil: "domcontentloaded" });
    await page.waitForLoadState("networkidle").catch(() => {});
    const accountInput = page.locator("#login-account");
    const passwordInput = page.locator("#login-password");
    await expect(accountInput).toBeEditable({ timeout: 10000 });
    await accountInput.fill(account);
    await expect(accountInput).toHaveValue(account);
    await passwordInput.fill(password);
    await expect(passwordInput).toHaveValue(password);
    const continueButton = page.getByRole("button", { name: "继续" });
    await expect(continueButton).toBeEnabled({ timeout: 10000 });
    const loginResponse = page.waitForResponse(
      (response) => response.url().endsWith("/auth/login") && response.request().method() === "POST",
      { timeout: 30000 },
    );
    await continueButton.click();
    await expect((await loginResponse).ok()).toBeTruthy();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/run-reviews$`), { timeout: 30000 });

    await expect(page.getByRole("heading", { name: "运行复盘" })).toBeVisible({ timeout: 30000 });
    await expect(page.getByText("你是从哪里了解到 Multica 的？")).toHaveCount(0);

    await page.goto(`${frontendURL}/${workspaceSlug}/training/prompts`, { waitUntil: "domcontentloaded" });
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/prompts$`));
    await expectTrainingRouteShell(page, PROMPTS_ROUTE);
    await expect(page.getByTestId("prompt-version-history")).toContainText("版本历史", { timeout: 15000 });
    await expect(page.getByRole("button", { name: "需求澄清", exact: true })).toBeVisible({ timeout: 15000 });
    await expectTrainingRouteSurvivesReload(page, PROMPTS_ROUTE);

    await page.goto(`${frontendURL}/${workspaceSlug}/training/datasets`, { waitUntil: "domcontentloaded" });
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/datasets$`), { timeout: 30000 });
    await expectTrainingRouteShell(page, DATASETS_ROUTE);
    const datasetRow = page.getByTestId(`prompt-evaluation-asset-${evidence.dataset.id}`);
    await expect(datasetRow).toContainText(evidence.dataset.name, { timeout: 15000 });
    await expect(datasetRow).toContainText("数据集");
    await expect(page.getByTestId(`prompt-evaluation-cases-${evidence.dataset.id}`)).toContainText("登录失败澄清", { timeout: 15000 });
    await expectTrainingRouteSurvivesReload(page, DATASETS_ROUTE);
    await expect(page.getByTestId(`prompt-evaluation-asset-${evidence.dataset.id}`)).toContainText(evidence.dataset.name, { timeout: 15000 });

    await page.goto(`${frontendURL}/${workspaceSlug}/training/test-suites`, { waitUntil: "domcontentloaded" });
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/test-suites$`), { timeout: 30000 });
    await expectTrainingRouteShell(page, TEST_SUITES_ROUTE);
    const suiteRow = page.getByTestId(`prompt-evaluation-asset-${evidence.suite.id}`);
    await expect(suiteRow).toContainText(evidence.suite.name, { timeout: 15000 });
    await expect(suiteRow).toContainText("测试套件");
    await expect(page.getByTestId(`prompt-evaluation-cases-${evidence.suite.id}`)).toContainText("真实智能体证据样本", { timeout: 15000 });
    await expectTrainingRouteSurvivesReload(page, TEST_SUITES_ROUTE);
    await expect(page.getByTestId(`prompt-evaluation-asset-${evidence.suite.id}`)).toContainText(evidence.suite.name, { timeout: 15000 });

    await page.goto(`${frontendURL}/${workspaceSlug}/training/evaluation-runs`, { waitUntil: "domcontentloaded" });
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/evaluation-runs$`), { timeout: 30000 });
    const syncedRunRow = page.getByTestId(`prompt-evaluation-run-${evidence.syncedRun.id}`);
    await syncedRunRow.scrollIntoViewIfNeeded();
    await expect(syncedRunRow).toContainText("智能体执行", { timeout: 30000 });
    await expect(syncedRunRow).toContainText(evidence.syncedRun.id);
    await syncedRunRow.locator("button").filter({ hasText: "查看证据" }).first().click();
    await expect(syncedRunRow.getByTestId("run-evidence-snapshots")).toContainText("服务端证据快照", { timeout: 10000 });
    await expect(syncedRunRow.getByTestId("run-evidence-snapshots")).toContainText(evidence.syncedRun.task_id!, { timeout: 10000 });
    await page.reload({ waitUntil: "domcontentloaded" });
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/evaluation-runs$`), { timeout: 30000 });
    await expect(page.getByTestId(`prompt-evaluation-run-${evidence.syncedRun.id}`)).toContainText(evidence.syncedRun.id, { timeout: 30000 });
  });
});
