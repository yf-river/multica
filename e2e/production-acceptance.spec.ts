import { test, expect } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { TRAINING_ROUTES } from "./training-routes";
import { TestApiClient } from "./fixtures";

const workspaceSlug = process.env.ACCEPTANCE_WORKSPACE_SLUG
  || process.env.REAL_AGENT_E2E_WORKSPACE
  || "goal-test-daemon";
const account = process.env.ACCEPTANCE_DEMO_ACCOUNT
  || process.env.REAL_AGENT_E2E_ACCOUNT
  || "goal-test-daemon";
const password = process.env.ACCEPTANCE_DEMO_PASSWORD
  || process.env.REAL_AGENT_E2E_PASSWORD
  || "e2e-password";
const frontendURL = process.env.ACCEPTANCE_FRONTEND_URL
  || process.env.PLAYWRIGHT_BASE_URL
  || process.env.FRONTEND_ORIGIN
  || "http://localhost:3000";
const workspaceName = process.env.E2E_WORKSPACE_NAME || "goal-test 联调工作区";
const evidencePrefix = "生产验收训练证据";
const ROUTE_INTRO_TITLES: Record<string, string> = {
  datasets: "数据集题库",
  "test-suites": "测试套件回归",
  experiments: "实验对比",
  "optimization-runs": "优化运行作业台",
  "run-history": "运行历史与证据",
};
const ROUTE_OPERATING_TEXT: Record<string, string> = {
  datasets: "样本入库、版本快照、下游复用",
  "test-suites": "固定试卷、断言回归、失败定位",
  experiments: "变量矩阵、版本绑定、横向排行",
  "optimization-runs": "失败样本、候选生成、人工发布",
  "run-history": "运行检索、证据展开、人工复核",
};

async function prepareTrainingDashboardEvidence() {
  const api = new TestApiClient();
  await api.login(account, "goal-test 验收账号");
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
  const experiment = await api.createPromptEvaluationAsset({
    prompt_id: prompt.id,
    name: `${evidencePrefix} 实验 ${suffix}`,
    description: "生产验收实验。",
    asset_type: "实验",
    payload: {
      schema: "multica.training_evaluation.payload.v1",
      schema_version: 1,
      语义版本: "multica.training_evaluation.v1",
      实验对象: prompt.name,
      对比维度: ["命中率", "缺失变量", "中文一致性"],
      cases: [{
        case_name: "实验对比样本",
        variables: { issue_title: "user-center 登录失败" },
        expected_contains: ["目标", "验收条件"],
        tags: ["生产验收", "实验"],
      }],
    },
    status: "启用",
  });
  const optimizationAsset = await api.createPromptEvaluationAsset({
    prompt_id: prompt.id,
    name: `${evidencePrefix} 优化运行 ${suffix}`,
    description: "生产验收优化运行。",
    asset_type: "优化运行",
    payload: {
      schema: "multica.training_evaluation.payload.v1",
      schema_version: 1,
      语义版本: "multica.training_evaluation.v1",
      cases: [{
        case_name: "缺失 trace 的优化样本",
        variables: { issue_title: "user-center 登录失败" },
        expected_contains: ["这个断言用于制造失败候选", "trace/task id"],
        tags: ["生产验收", "优化运行"],
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
    experiment,
    optimizationAsset,
    syncedRun,
    snapshot,
    failedRun,
    candidate,
  };
}

async function expectTrainingRouteShell(page, route: (typeof TRAINING_ROUTES)[number]) {
  const isPromptPlayground = route.path === "prompt-playground";
  const isAgentPlayground = route.path === "agent-playground";
  const routeIntroTitle = ROUTE_INTRO_TITLES[route.path];
  const hasRouteIntro = Boolean(routeIntroTitle);
  await expect(page.getByTestId("prompt-playground-page-shell")).toHaveCount(isPromptPlayground ? 1 : 0);
  await expect(page.getByTestId("agent-playground-page-shell")).toHaveCount(isAgentPlayground ? 1 : 0);
  await expect(page.getByTestId("training-page-shell")).toHaveCount(isPromptPlayground || isAgentPlayground ? 0 : 1);
  await expect(page.getByTestId("training-tab-strip")).toHaveCount(0);
  if (!isPromptPlayground && !isAgentPlayground) {
    await expect(page.getByTestId(`training-route-${route.path}`)).toHaveCount(1);
  }
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
    test.setTimeout(120_000);
    const evidence = await prepareTrainingDashboardEvidence();
    expect(evidence.snapshot.id).toBeTruthy();
    expect(evidence.syncedRun.task_id).toBeTruthy();
    const next = `/${workspaceSlug}/training/runs`;
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
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/runs$`), { timeout: 30000 });

    await expect(page.getByTestId("training-demo-dashboard")).toContainText("团队运行看板", { timeout: 30000 });
    await expect(page.getByTestId("training-demo-proof-真实智能体证据")).toContainText("已有任务/trace 运行记录");
    await expect(page.getByTestId("training-demo-proof-数据集行")).toContainText(/[1-9]/);
    await expect(page.getByTestId("training-demo-proof-测试套件用例")).toContainText(/[1-9]/);
    await expect(page.getByTestId("training-demo-proof-实验维度事实")).toContainText(/[1-9]/);
    await expect(page.getByTestId("training-demo-proof-服务端证据快照")).toContainText(/验收归档 [1-9]/);
    await expect(page.getByText("运行证据已服务端归档")).toBeVisible();
    await expect(page.getByText("你是从哪里了解到 Multica 的？")).toHaveCount(0);
    const downloadPromise = page.waitForEvent("download");
    await page.getByRole("button", { name: "导出证据 JSON" }).click();
    const download = await downloadPromise;
    const downloadPath = await download.path();
    expect(downloadPath).toBeTruthy();
    const exported = JSON.parse(await readFile(downloadPath!, "utf8"));
    expect(exported["语义版本"]).toBe("multica.production_demo_evidence.v1");
    expect(exported.workspace_id).toBeTruthy();
    expect(exported["证据统计"]["运行数"]).toBeGreaterThan(0);

    await page.getByRole("link", { name: "提示词库", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/prompts$`));
    await expectTrainingRouteShell(page, TRAINING_ROUTES[1]!);
    await expect(page.getByTestId("prompt-version-history")).toContainText("版本历史", { timeout: 15000 });
    await expect(page.getByRole("button", { name: "需求澄清", exact: true })).toBeVisible({ timeout: 15000 });
    await expectTrainingRouteSurvivesReload(page, TRAINING_ROUTES[1]!);

    await page.getByRole("link", { name: "提示词调试场", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/prompt-playground$`));
    await expectTrainingRouteShell(page, TRAINING_ROUTES[2]!);
    await expect(page.getByTestId("playground-page-subtitle")).toContainText("本地模板渲染");
    await expect(page.getByTestId("playground-page-subtitle")).toContainText("不创建智能体任务");
    await expect(page.getByTestId("playground-page-count")).toContainText("提示词");
    await expect(page.getByTestId("prompt-playground-workbench")).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId("prompt-playground-selector-summary")).toContainText("本地模板目录");
    await expect(page.getByTestId("prompt-playground-selector-summary")).toContainText("质检工作单");
    await expect(page.getByLabel("模板变量")).toBeVisible();
    await expect(page.getByTestId("prompt-playground-rendered-output")).toBeVisible();
    await expect(page.getByTestId("prompt-playground-template-lab")).toBeVisible();
    await expect(page.getByTestId("prompt-playground-inspection-board")).toBeVisible();
    await expect(page.getByTestId("prompt-playground-source-panel")).toContainText("模板源");
    await expect(page.getByTestId("prompt-playground-variable-checklist")).toContainText("变量样本");
    await expect(page.getByTestId("prompt-playground-purpose-map")).toContainText("不创建任务、不消耗模型");
    await expect(page.getByText("调试边界")).toBeVisible();
    await expect(page.getByTestId("prompt-playground-contract")).toContainText("不启动智能体");
    await expect(page.getByTestId("prompt-playground-local-pipeline")).toContainText("保存质检记录");
    await expect(page.getByTestId("prompt-playground-quality-gate")).toContainText("质检结论");
    await expect(page.getByTestId("agent-playground-task-pipeline")).toHaveCount(0);
    await expect(page.getByTestId("agent-playground-launch-brief")).toHaveCount(0);
    await expect(page.getByTestId("agent-playground-run-console")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "保存本地渲染检查" })).toBeVisible();
    await expect(page.getByRole("button", { name: "创建真实智能体任务" })).toHaveCount(0);
    await expect(page.getByText("真实执行准备度")).toHaveCount(0);
    await expectTrainingRouteSurvivesReload(page, TRAINING_ROUTES[2]!);
    await expect(page.getByTestId("prompt-playground-template-lab")).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId("agent-playground-run-console")).toHaveCount(0);

    await page.getByRole("link", { name: "智能体调试场", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/agent-playground$`));
    await expectTrainingRouteShell(page, TRAINING_ROUTES[3]!);
    await expect(page.getByTestId("playground-page-subtitle")).toContainText("创建真实任务");
    await expect(page.getByTestId("playground-page-subtitle")).toContainText("链路追踪");
    await expect(page.getByTestId("playground-page-count")).toContainText("执行目标");
    await expect(page.getByTestId("agent-playground-run-console")).toContainText("真实任务发射台", { timeout: 15000 });
    await expect(page.getByTestId("agent-playground-launch-brief")).toContainText("写入真实任务队列");
    await expect(page.getByTestId("agent-playground-execution-stage")).toBeVisible();
    await expect(page.getByTestId("agent-playground-selector-summary")).toContainText("执行目标池");
    await expect(page.getByTestId("agent-playground-selector-summary")).toContainText("链路追踪");
    await expect(page.getByTestId("agent-playground-task-payload")).toBeVisible();
    await expect(page.getByTestId("agent-playground-observability-contract")).toContainText("观测回写契约");
    await expect(page.getByTestId("agent-playground-evidence-strip")).toContainText("真实运行");
    await expect(page.getByText("真实执行准备度")).toBeVisible({ timeout: 15000 });
    await expect(page.getByText("将入队的任务正文")).toBeVisible();
    await expect(page.getByText("最近智能体运行")).toBeVisible();
    await expect(page.getByTestId("agent-playground-task-pipeline")).toContainText("创建真实任务");
    await expect(page.getByTestId("agent-playground-readiness-lane")).toHaveCount(3);
    await expect(page.getByTestId("agent-playground-execution-bus")).toContainText("Trace");
    await expect(page.getByTestId("agent-playground-execution-bus")).toContainText("用量");
    await expect(page.getByTestId("agent-playground-task-pipeline")).toContainText("回写观测证据");
    await expect(page.getByTestId("prompt-playground-local-pipeline")).toHaveCount(0);
    await expect(page.getByTestId("prompt-playground-purpose-map")).toHaveCount(0);
    await expect(page.getByTestId("prompt-playground-source-panel")).toHaveCount(0);
    await expect(page.getByTestId("prompt-playground-variable-checklist")).toHaveCount(0);
    await expect(page.getByTestId("prompt-playground-template-lab")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "创建真实智能体任务" })).toBeVisible();
    await expect(page.getByTestId("prompt-playground-workbench")).toHaveCount(0);
    await expect(page.getByText("不启动智能体")).toHaveCount(0);
    await expectTrainingRouteSurvivesReload(page, TRAINING_ROUTES[3]!);
    await expect(page.getByTestId("agent-playground-run-console")).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId("prompt-playground-template-lab")).toHaveCount(0);

    await page.getByRole("link", { name: "数据集", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/datasets$`), { timeout: 30000 });
    await expectTrainingRouteShell(page, TRAINING_ROUTES[4]!);
    const datasetRow = page.getByTestId(`prompt-evaluation-asset-${evidence.dataset.id}`);
    await expect(datasetRow).toContainText(evidence.dataset.name, { timeout: 15000 });
    await expect(datasetRow).toContainText("数据集");
    await expect(page.getByTestId(`prompt-evaluation-cases-${evidence.dataset.id}`)).toContainText("登录失败澄清", { timeout: 15000 });
    await expectTrainingRouteSurvivesReload(page, TRAINING_ROUTES[4]!);
    await expect(page.getByTestId(`prompt-evaluation-asset-${evidence.dataset.id}`)).toContainText(evidence.dataset.name, { timeout: 15000 });

    await page.getByRole("link", { name: "测试套件", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/test-suites$`), { timeout: 30000 });
    await expectTrainingRouteShell(page, TRAINING_ROUTES[5]!);
    const suiteRow = page.getByTestId(`prompt-evaluation-asset-${evidence.suite.id}`);
    await expect(suiteRow).toContainText(evidence.suite.name, { timeout: 15000 });
    await expect(suiteRow).toContainText("测试套件");
    await expect(page.getByTestId(`prompt-evaluation-cases-${evidence.suite.id}`)).toContainText("真实智能体证据样本", { timeout: 15000 });
    await expectTrainingRouteSurvivesReload(page, TRAINING_ROUTES[5]!);
    await expect(page.getByTestId(`prompt-evaluation-asset-${evidence.suite.id}`)).toContainText(evidence.suite.name, { timeout: 15000 });

    await page.getByRole("link", { name: "实验", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/experiments$`), { timeout: 30000 });
    await expectTrainingRouteShell(page, TRAINING_ROUTES[6]!);
    await expect(page.getByTestId("experiment-comparison-panel")).toContainText("实验对比排行", { timeout: 15000 });
    const experimentRow = page.getByTestId(`prompt-evaluation-asset-${evidence.experiment.id}`);
    await expect(page.getByTestId(`experiment-comparison-row-${evidence.experiment.id}`)).toContainText("通过率", { timeout: 15000 });
    await expect(page.getByTestId(`experiment-comparison-row-${evidence.experiment.id}`)).toContainText("预估成本");
    await expect(experimentRow).toContainText(evidence.experiment.name, { timeout: 15000 });
    await expect(experimentRow).toContainText("实验");
    const experimentDimensions = page.getByTestId(`prompt-evaluation-experiment-dimensions-${evidence.experiment.id}`);
    await expect(experimentDimensions).toContainText("实验维度事实", { timeout: 15000 });
    await expect(experimentDimensions).toContainText("命中率");
    await expect(experimentDimensions).toContainText("缺失变量");
    await expect(experimentDimensions).toContainText("中文一致性");
    await expectTrainingRouteSurvivesReload(page, TRAINING_ROUTES[6]!);
    await expect(page.getByTestId(`prompt-evaluation-asset-${evidence.experiment.id}`)).toContainText(evidence.experiment.name, { timeout: 15000 });

    await page.getByRole("link", { name: "优化运行", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/optimization-runs$`), { timeout: 30000 });
    await expectTrainingRouteShell(page, TRAINING_ROUTES[7]!);
    const optimizationRow = page.getByTestId(`prompt-evaluation-asset-${evidence.optimizationAsset.id}`);
    await expect(optimizationRow).toContainText(evidence.optimizationAsset.name, { timeout: 15000 });
    await expect(optimizationRow).toContainText("优化运行");
    await expect(page.getByTestId(`prompt-evaluation-candidate-${evidence.candidate.id}`)).toContainText("待确认", { timeout: 15000 });
    await expectTrainingRouteSurvivesReload(page, TRAINING_ROUTES[7]!);
    await expect(page.getByTestId(`prompt-evaluation-candidate-${evidence.candidate.id}`)).toContainText("待确认", { timeout: 15000 });

    await page.getByRole("link", { name: "运行历史", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/run-history$`), { timeout: 30000 });
    await expectTrainingRouteShell(page, TRAINING_ROUTES[8]!);
    const syncedRunRow = page.getByTestId(`prompt-evaluation-run-${evidence.syncedRun.id}`);
    await syncedRunRow.scrollIntoViewIfNeeded();
    await expect(syncedRunRow).toContainText("智能体执行", { timeout: 30000 });
    await expect(syncedRunRow).toContainText(evidence.syncedRun.id);
    await syncedRunRow.locator("button").filter({ hasText: "查看证据" }).first().click();
    await expect(syncedRunRow.getByTestId("run-evidence-snapshots")).toContainText("服务端证据快照", { timeout: 10000 });
    await expect(syncedRunRow.getByTestId("run-evidence-snapshots")).toContainText(evidence.syncedRun.task_id!, { timeout: 10000 });
    await expectTrainingRouteSurvivesReload(page, TRAINING_ROUTES[8]!);
    await expect(page.getByTestId(`prompt-evaluation-run-${evidence.syncedRun.id}`)).toContainText(evidence.syncedRun.id, { timeout: 30000 });
  });
});
