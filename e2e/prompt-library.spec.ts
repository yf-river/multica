import { test, expect, type Locator, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { createTestApi, loginAsDefault, waitForPageText } from "./helpers";
import type { TestApiClient } from "./fixtures";

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

async function clickStableButton(locator: Locator) {
  await expect(locator).toBeEnabled({ timeout: 10000 });
  await locator.evaluate((button: HTMLButtonElement) => {
    button.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, view: window }));
  });
}

async function fillStable(locator: Locator, value: string) {
  await expect(locator).toBeEditable({ timeout: 10000 });
  for (let attempt = 0; attempt < 3; attempt += 1) {
    await locator.fill(value);
    try {
      await expect(locator).toHaveValue(value, { timeout: 2000 });
      return;
    } catch (error) {
      if (attempt === 2) throw error;
    }
  }
}

async function showAcceptanceFixturesIfAvailable(page: Page) {
  await page.getByText(/已隐藏 \d+ 个验收训练证据/).last().waitFor({ timeout: 10000 }).catch(() => undefined);
  const showButtons = page.getByRole("button", { name: "显示验收数据" });
  const count = await showButtons.count();
  for (let index = count - 1; index >= 0; index -= 1) {
    const showButton = showButtons.nth(index);
    if (await showButton.isVisible({ timeout: 500 }).catch(() => false)) {
      await showButton.click({ force: true });
      await expect(page.getByTestId("training-summary-strip")).toContainText("含验收数据", { timeout: 10000 });
      break;
    }
  }
}

test.describe("训练与评估工作台", () => {
	  let api: TestApiClient;
	  let artifactPrefix: string;
	  let workspaceSlug: string;
  let expectedAgentModel = process.env.MULTICA_PROMPT_EVALUATION_AGENT_MODEL || "gpt-5.3-codex-spark";
  let expectedAgentRuntimeId = "";
  let expectedAgentRuntimeName = "";

  async function refreshExpectedAgentModel() {
    const readiness = await api.getPromptEvaluationRuntimeReadiness();
    if (!process.env.MULTICA_PROMPT_EVALUATION_AGENT_MODEL) {
      expectedAgentModel = readiness.model || expectedAgentModel;
    }
    expectedAgentRuntimeId = readiness.runtime?.id || expectedAgentRuntimeId;
    expectedAgentRuntimeName = readiness.runtime?.name || expectedAgentRuntimeName;
  }

  test.beforeEach(async ({ page }) => {
    api = await createTestApi();
    artifactPrefix = `E2E 提示词 ${Date.now()}`;
    await api.cleanupPromptArtifactsByPrefix("E2E 提示词");
	    workspaceSlug = await loginAsDefault(page);
	  });

  test.afterEach(async () => {
    if (api && artifactPrefix) {
      await api.cleanupPromptArtifactsByPrefix(artifactPrefix);
      await api.cleanup();
    }
  });

	  test("可以创建提示词、调试渲染并记录评测资产", async ({ page }) => {
    test.setTimeout(180_000);
    await api.ensureOnlineCodexRuntime(`${artifactPrefix} Codex Runtime`);
    await refreshExpectedAgentModel();

    await page.getByRole("link", { name: "训练与评估" }).click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/runs$`), { timeout: 30000 });
    await waitForPageText(page, "训练与评估");
    await expect(page.getByTestId("training-demo-dashboard")).toContainText("训练运行看板", { timeout: 10000 });
    await page.getByRole("link", { name: "提示词库", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/prompts$`), { timeout: 30000 });

    await page.getByRole("button", { name: "起草需求澄清模板" }).click();
    await page.getByLabel("名称").fill(`${artifactPrefix} 账号系统 澄清`);
    await page.getByLabel("提示词内容").fill("请澄清 {{issue_title}}，项目背景：{{project_context}}。");
    await page.getByLabel("变量", { exact: true }).fill("issue_title=任务标题, project_context=项目背景");
    await page.getByLabel("调试变量").fill("issue_title=登录失败\nproject_context=账号系统");

    await expect(page.getByText("请澄清 登录失败，项目背景：账号系统。").last()).toBeVisible();

    await page.getByRole("button", { name: "保存", exact: true }).click();
    await expect
      .poll(async () => (await api.listPromptLibraryItems()).some((item) => item.name === `${artifactPrefix} 账号系统 澄清`), { timeout: 10000 })
      .toBe(true);
    await expect(page.getByTestId("prompt-version-history")).toContainText("手动创建", { timeout: 10000 });
    await expect(page.getByTestId("prompt-version-history")).toContainText("当前版本 1");

    await page.getByRole("link", { name: "提示词调试场", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/prompt-playground$`), { timeout: 30000 });
    await expect(page.getByTestId("prompt-playground-workbench")).toBeVisible({ timeout: 10000 });
    await page.getByRole("button", { name: new RegExp(escapeRegExp(`${artifactPrefix} 账号系统 澄清`)) }).click();
    await expect(page.getByTestId("prompt-playground-workbench")).toContainText(`${artifactPrefix} 账号系统 澄清`, { timeout: 10000 });
    await expect(page.getByTestId("prompt-library-editor")).toHaveCount(0);
    await expect(page.getByTestId("agent-playground-workbench")).toHaveCount(0);
    await expect(page.getByTestId("prompt-playground-template-lab")).toBeVisible();
    await expect(page.getByTestId("agent-playground-run-console")).toHaveCount(0);
    await expect(page.getByTestId("agent-playground-task-payload")).toHaveCount(0);
    await expect(page.getByTestId("prompt-playground-contract")).toContainText("不启动智能体");
    await expect(page.getByTestId("prompt-playground-purpose-map")).toContainText("不创建任务、不消耗模型");
    await expect(page.getByTestId("prompt-playground-local-pipeline")).toContainText("解析模板源");
    await expect(page.getByTestId("prompt-playground-local-pipeline")).toContainText("保存质检记录");
    await expect(page.getByTestId("prompt-playground-source-panel")).toContainText("模板源");
    await expect(page.getByTestId("prompt-playground-variable-checklist")).toContainText("变量样本");
    await expect(page.getByTestId("prompt-playground-template-source")).toBeVisible();
    await expect(page.getByTestId("agent-playground-task-pipeline")).toHaveCount(0);
    await expect(page.getByTestId("agent-playground-launch-brief")).toHaveCount(0);
    await expect(page.getByLabel("模板变量")).toHaveValue("issue_title=\nproject_context=", { timeout: 10000 });
    await page.getByLabel("模板变量").fill("issue_title=登录失败\nproject_context=账号系统");
    await expect(page.getByTestId("prompt-playground-rendered-output")).toContainText("请澄清 登录失败，项目背景：账号系统。", { timeout: 10000 });
    await page.getByRole("button", { name: "保存本地渲染检查" }).click();
    await expect(page.getByText("优化运行已记录")).toBeVisible({ timeout: 10000 });
    const selectedPrompt = (await api.listPromptLibraryItems()).find((item) => item.name === `${artifactPrefix} 账号系统 澄清`);
    expect(selectedPrompt).toBeTruthy();

    const assetRoutes = {
      数据集: "datasets",
      测试套件: "test-suites",
      实验: "experiments",
    } as const;
    for (const assetType of Object.keys(assetRoutes) as Array<keyof typeof assetRoutes>) {
      const route = assetRoutes[assetType];
      await page.goto(`/${workspaceSlug}/training/${route}?prompt_id=${selectedPrompt!.id}`);
      await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/${route}\\?prompt_id=`), { timeout: 30000 });
      const routeWorkspace = page.getByTestId(`training-route-workspace-${route}`);
      await expect(routeWorkspace).toBeVisible({ timeout: 10000 });
      const createAssetResponse = page.waitForResponse(
        (response) => response.request().method() === "POST" && response.url().includes("/prompt-evaluation-assets"),
        { timeout: 10000 },
      );
      await routeWorkspace.getByRole("button", { name: `新建${assetType}` }).click();
      const createdAssetResponse = await createAssetResponse;
      expect([200, 201]).toContain(createdAssetResponse.status());
      const createdAsset = await createdAssetResponse.json() as PromptEvaluationAsset;
      expect(createdAsset.asset_type).toBe(assetType);
      expect(createdAsset.prompt_id).toBe(selectedPrompt!.id);
      await expect(page.getByText("资产已创建").last()).toBeVisible({ timeout: 10000 });
      await expect
        .poll(async () => {
          return (await api.listPromptEvaluationAssets({ prompt_id: selectedPrompt!.id, asset_type: assetType }))
            .find((asset) => asset.id === createdAsset.id) ?? null;
        }, { timeout: 15000 })
        .toMatchObject({ asset_type: assetType });
    }

    await page.getByRole("link", { name: "智能体调试场", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/agent-playground$`), { timeout: 30000 });
    await expect(page.getByTestId("agent-playground-workbench")).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId("agent-playground-workbench")).toContainText(`${artifactPrefix} 账号系统 澄清`, { timeout: 10000 });
    await expect(page.getByTestId("agent-playground-execution-stage")).toBeVisible();
    await expect(page.getByTestId("agent-playground-selector-summary")).toContainText("执行目标池");
    await expect(page.getByTestId("agent-playground-run-console")).toContainText("真实任务发射台");
    await expect(page.getByTestId("agent-playground-launch-brief")).toContainText("写入真实任务队列");
    await expect(page.getByTestId("agent-playground-evidence-strip")).toContainText("真实运行");
    await expect(page.getByTestId("agent-playground-task-payload")).toBeVisible();
    await expect(page.getByTestId("agent-playground-observability-contract")).toContainText("观测回写契约");
    await expect(page.getByTestId("agent-playground-task-pipeline")).toContainText("创建真实任务");
    await expect(page.getByTestId("agent-playground-execution-bus")).toContainText("Trace");
    await expect(page.getByTestId("agent-playground-execution-bus")).toContainText("用量");
    await expect(page.getByTestId("agent-playground-task-pipeline")).toContainText("回写观测证据");
    await expect(page.getByTestId("prompt-playground-purpose-map")).toHaveCount(0);
    await expect(page.getByTestId("prompt-playground-local-pipeline")).toHaveCount(0);
    await expect(page.getByTestId("prompt-playground-template-lab")).toHaveCount(0);
    await expect(page.getByText("Codex 在线")).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId("agent-playground-runtime")).toContainText("运行时：", { timeout: 10000 });
    const agentExpectedOutput = "输出需求澄清结论、风险、测试证据和下一步建议。";
    const agentExpectedOutputInput = page.getByTestId("agent-playground-panel").getByLabel("期望输出");
    await agentExpectedOutputInput.fill(agentExpectedOutput);
    await expect(agentExpectedOutputInput).toHaveValue(agentExpectedOutput);
    await page.getByRole("button", { name: "保存为实验" }).click();
    await expect(page.getByText("资产已创建").last()).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const assets = await api.listPromptEvaluationAssets({ asset_type: "实验" });
        const asset = assets.find((item) => item.name.startsWith(`${artifactPrefix} 账号系统 澄清 智能体调试包`));
        return (asset?.payload as Record<string, any> | undefined)?.调试包?.期望输出 ?? null;
      }, { timeout: 15000 })
      .toBe(agentExpectedOutput);
    const createAgentTaskButton = page.getByRole("button", { name: "创建真实智能体任务" });
    await expect(createAgentTaskButton).toBeEnabled({ timeout: 10000 });
    const agentRunResponse = page.waitForResponse(
      (response) => response.request().method() === "POST" && response.url().includes("/agent-run"),
      { timeout: 10000 },
    );
    await createAgentTaskButton.click();
    const createdAgentRunResponse = await agentRunResponse;
    expect(createdAgentRunResponse.status()).toBe(202);
    const createdAgentRun = await createdAgentRunResponse.json() as {
      model?: string;
      runtime_id?: string;
      run?: { model?: string; runtime_id?: string };
    };
    const actualAgentModel = createdAgentRun.model || createdAgentRun.run?.model || expectedAgentModel;
    const actualAgentRuntimeId = createdAgentRun.runtime_id || createdAgentRun.run?.runtime_id || expectedAgentRuntimeId;

    await expect
      .poll(async () => {
        const prompts = await api.listPromptLibraryItems();
        const prompt = prompts.find((item) => item.name === `${artifactPrefix} 账号系统 澄清`);
        if (!prompt) return null;
        const assets = await api.listPromptEvaluationAssets({
          asset_type: "优化运行",
          prompt_id: prompt.id,
        });
        return assets.find((asset) => asset.name.startsWith(`${artifactPrefix} 账号系统 澄清 优化运行`)) ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        asset_type: "优化运行",
        payload: {
          最近运行: {
            总用例数: 1,
            通过用例数: 1,
            缺失变量数: 0,
            用例结果: [
              {
                状态: "通过",
                渲染提示词: "请澄清 登录失败，项目背景：账号系统。",
              },
            ],
          },
        },
      });

    await expect
      .poll(async () => {
        const prompts = await api.listPromptLibraryItems();
        const prompt = prompts.find((item) => item.name === `${artifactPrefix} 账号系统 澄清`);
        if (!prompt) return [];
        const assets = await api.listPromptEvaluationAssets({ prompt_id: prompt.id });
        return assets.map((asset) => asset.asset_type).sort();
      }, { timeout: 15000 })
      .toEqual(["优化运行", "实验", "实验", "实验", "数据集", "测试套件"].sort());

    const prompt = (await api.listPromptLibraryItems()).find((item) => item.name === `${artifactPrefix} 账号系统 澄清`);
    expect(prompt).toBeTruthy();
    const promptAssets = await api.listPromptEvaluationAssets({ prompt_id: prompt!.id });
    const optimizationRun = promptAssets.find((asset) => asset.asset_type === "优化运行");
    const dataset = promptAssets.find((asset) => asset.asset_type === "数据集");
    const testSuite = promptAssets.find((asset) => asset.asset_type === "测试套件");
    const experiment = promptAssets.find((asset) => asset.asset_type === "实验" && asset.name.includes(" 实验 "));
    expect(optimizationRun).toBeTruthy();
    expect(dataset).toBeTruthy();
    expect(testSuite).toBeTruthy();
    expect(experiment).toBeTruthy();
    await expect(api.listPromptEvaluationCases({ asset_id: dataset!.id })).resolves.toEqual([
      expect.objectContaining({
        asset_id: dataset!.id,
        case_name: expect.stringContaining("基准用例"),
        status: "启用",
      }),
    ]);
    await page.goto(`/${workspaceSlug}/training/datasets?prompt_id=${selectedPrompt!.id}`);
    await showAcceptanceFixturesIfAvailable(page);
    const datasetRow = page.getByTestId(`prompt-evaluation-asset-${dataset!.id}`);
    await expect(datasetRow).toContainText("结构化用例 1 个", { timeout: 10000 });
    const importTraceButton = datasetRow.getByRole("button", { name: "从 trace 导入样本" });
    await expect(importTraceButton).toBeVisible();
    const importTraceResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes(`/api/prompt-evaluation-assets/${dataset!.id}/dataset-from-traces`),
      { timeout: 15000 },
    );
    await importTraceButton.click();
    expect((await importTraceResponse).status()).toBe(201);
    await expect(page.getByText(/已从 trace 导入 \d+ 条数据集样本/).last()).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: dataset!.id });
        return items.filter((item) => item.source === "trace").length;
      }, { timeout: 15000 })
      .toBeGreaterThan(0);
    await expect(datasetRow).toContainText("trace导入", { timeout: 10000 });
    const manualCaseNameInput = datasetRow.getByPlaceholder("手工用例名称");
    const manualCaseVariablesInput = datasetRow.getByPlaceholder("变量：任务标题=登录失败");
    const manualCaseExpectedInput = datasetRow.getByPlaceholder("期望包含：验收条件, trace/任务标识");
    const manualCaseTagsInput = datasetRow.getByPlaceholder("标签：账号系统, 回归");
    await fillStable(manualCaseNameInput, "手工补充登录失败验收");
    await fillStable(manualCaseVariablesInput, "issue_title=登录失败\nproject_context=账号系统");
    await fillStable(manualCaseExpectedInput, "验收条件, trace/任务标识, 可观测证据");
    await fillStable(manualCaseTagsInput, "手工用例, 账号系统");
    const addManualCaseButton = datasetRow.getByRole("button", { name: "新增用例" });
    await clickStableButton(addManualCaseButton);
    await expect(page.getByText("手工评测用例已创建").last()).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: dataset!.id });
        return items.find((item) => item.source === "manual" && item.case_name === "手工补充登录失败验收") ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        asset_id: dataset!.id,
        case_name: "手工补充登录失败验收",
        source: "manual",
        status: "启用",
      });
    await expect(datasetRow).toContainText("手工 1", { timeout: 10000 });
    await datasetRow.getByRole("button", { name: "编辑用例" }).click();
    await datasetRow.getByPlaceholder("编辑用例名称").fill("手工补充登录失败验收 v2");
    await datasetRow.getByPlaceholder("编辑变量：任务标题=登录失败").fill("issue_title=登录失败\nproject_context=账号系统\npriority=P0");
    await datasetRow.getByPlaceholder("编辑期望包含").fill("验收条件, trace/任务标识, 领导演示证据");
    await datasetRow.getByPlaceholder("编辑标签").fill("手工用例, 账号系统, 领导演示");
    await datasetRow.getByRole("button", { name: "保存用例" }).click();
    await expect(page.getByText("手工评测用例已保存").last()).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: dataset!.id });
        return items.find((item) => item.source === "manual" && item.case_name === "手工补充登录失败验收 v2") ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        variables: expect.objectContaining({
          issue_title: "登录失败",
          project_context: "账号系统",
          priority: "P0",
        }),
        expected_contains: expect.arrayContaining(["领导演示证据"]),
        tags: expect.arrayContaining(["领导演示"]),
      });
    await datasetRow.getByRole("button", { name: "删除用例" }).click();
    await expect(page.getByText("手工评测用例已删除").last()).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: dataset!.id });
        return items.some((item) => item.source === "manual" && item.case_name === "手工补充登录失败验收");
      }, { timeout: 15000 })
      .toBe(false);

    await page.goto(`/${workspaceSlug}/training/test-suites?prompt_id=${selectedPrompt!.id}`);
    await showAcceptanceFixturesIfAvailable(page);
    const testSuiteRow = page.getByTestId(`prompt-evaluation-asset-${testSuite!.id}`);
    await expect(testSuiteRow.getByText("结构化评测用例", { exact: true })).toBeVisible({ timeout: 10000 });
    await expect(testSuiteRow).toContainText("结构化用例 1 个", { timeout: 10000 });
    await fillStable(testSuiteRow.getByPlaceholder("手工用例名称"), "手工套件回归用例");
    await fillStable(testSuiteRow.getByPlaceholder("变量：任务标题=登录失败"), "issue_title=登录失败\nproject_context=账号系统");
    await fillStable(testSuiteRow.getByPlaceholder("期望包含：验收条件, trace/任务标识"), "套件结论, 通过率, trace/任务标识");
    await fillStable(testSuiteRow.getByPlaceholder("标签：账号系统, 回归"), "测试套件, 回归");
    await clickStableButton(testSuiteRow.getByRole("button", { name: "新增用例" }));
    await expect(page.getByText("手工评测用例已创建").last()).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: testSuite!.id });
        return items.find((item) => item.source === "manual" && item.case_name === "手工套件回归用例") ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        asset_id: testSuite!.id,
        expected_contains: expect.arrayContaining(["通过率"]),
        tags: expect.arrayContaining(["测试套件"]),
      });
    await expect(testSuiteRow).toContainText("手工 1", { timeout: 10000 });
    await testSuiteRow.getByRole("button", { name: "编辑用例" }).click();
    await testSuiteRow.getByPlaceholder("编辑用例名称").fill("手工套件回归用例 v2");
    await testSuiteRow.getByPlaceholder("编辑变量：任务标题=登录失败").fill("issue_title=登录失败\nproject_context=账号系统\nowner=qa");
    await testSuiteRow.getByPlaceholder("编辑期望包含").fill("套件结论, 通过率, 领导演示证据");
    await testSuiteRow.getByPlaceholder("编辑标签").fill("测试套件, 回归, 领导演示");
    await testSuiteRow.getByRole("button", { name: "保存用例" }).click();
    await expect(page.getByText("手工评测用例已保存").last()).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: testSuite!.id });
        return items.find((item) => item.source === "manual" && item.case_name === "手工套件回归用例 v2") ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        variables: expect.objectContaining({
          owner: "qa",
        }),
        expected_contains: expect.arrayContaining(["领导演示证据"]),
        tags: expect.arrayContaining(["领导演示"]),
      });
    await testSuiteRow.getByRole("button", { name: "删除用例" }).click();
    await expect(page.getByText("手工评测用例已删除").last()).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: testSuite!.id });
        return items.some((item) => item.source === "manual" && item.case_name.includes("手工套件回归用例"));
      }, { timeout: 15000 })
      .toBe(false);

    await page.goto(`/${workspaceSlug}/training/experiments?prompt_id=${selectedPrompt!.id}`);
    await showAcceptanceFixturesIfAvailable(page);
    const experimentRow = page.getByTestId(`prompt-evaluation-asset-${experiment!.id}`);
    await expect(experimentRow.getByText("结构化评测用例", { exact: true })).toBeVisible({ timeout: 10000 });
    await expect(experimentRow.getByText("实验维度事实", { exact: true })).toBeVisible({ timeout: 10000 });
    await expect(experimentRow.getByTestId(`prompt-evaluation-experiment-dimensions-${experiment!.id}`)).toContainText("命中率");
    await expect
      .poll(async () => {
        const dimensions = await api.listPromptEvaluationExperimentDimensions({ asset_id: experiment!.id });
        return {
          total: dimensions.total,
          names: dimensions.items.map((item) => item.dimension_name).sort(),
        };
      }, { timeout: 15000 })
      .toEqual({
        total: 3,
        names: ["中文一致性", "命中率", "缺失变量"].sort(),
      });
    await fillStable(experimentRow.getByPlaceholder("手工用例名称"), "手工实验对比用例");
    await fillStable(experimentRow.getByPlaceholder("变量：任务标题=登录失败"), "issue_title=登录失败\nproject_context=账号系统");
    await fillStable(experimentRow.getByPlaceholder("期望包含：验收条件, trace/任务标识"), "实验结论, 中文指标, trace/任务标识");
    await fillStable(experimentRow.getByPlaceholder("标签：账号系统, 回归"), "实验, 领导演示");
    await clickStableButton(experimentRow.getByRole("button", { name: "新增用例" }));
    await expect(page.getByText("手工评测用例已创建").last()).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: experiment!.id });
        return items.find((item) => item.source === "manual" && item.case_name === "手工实验对比用例") ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        asset_id: experiment!.id,
        expected_contains: expect.arrayContaining(["中文指标"]),
        tags: expect.arrayContaining(["领导演示"]),
      });

    await page.goto(`/${workspaceSlug}/training/optimization-runs?prompt_id=${selectedPrompt!.id}`);
    await showAcceptanceFixturesIfAvailable(page);
    const optimizationRow = page.getByTestId(`prompt-evaluation-asset-${optimizationRun!.id}`);
    await expect(optimizationRow.getByText("结构化评测用例", { exact: true })).toBeVisible({ timeout: 10000 });
    await expect(optimizationRow).toContainText("优化运行", { timeout: 10000 });
    const optimizationRuns = await api.listPromptEvaluationRuns({ asset_id: optimizationRun!.id });
    await expect(Promise.resolve(optimizationRuns)).resolves.toEqual([
      expect.objectContaining({
        asset_id: optimizationRun!.id,
        run_kind: "模板渲染检查",
        status: "通过",
        total_cases: 1,
        passed_cases: 1,
        failed_cases: 0,
      }),
    ]);
    const localRenderRun = optimizationRuns[0]!;
    const runEvidence = await api.getPromptEvaluationRunEvidence(localRenderRun.id);
    expect(runEvidence.trials).toEqual([
      expect.objectContaining({
        case_name: "调试场用例",
        status: "通过",
        rendered_prompt: "请澄清 登录失败，项目背景：账号系统。",
      }),
    ]);
    const archiveAssetEvidenceButton = optimizationRow.getByTestId(`archive-asset-evidence-${optimizationRun!.id}`);
    await expect(archiveAssetEvidenceButton).toBeVisible({ timeout: 10000 });
    const archiveAssetEvidenceResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes(`/api/prompt-evaluation-assets/${optimizationRun!.id}/evidence-snapshots`),
      { timeout: 15000 },
    );
    await archiveAssetEvidenceButton.click();
    const archiveAssetEvidence = await archiveAssetEvidenceResponse;
    expect(archiveAssetEvidence.status()).toBe(201);
    const archiveAssetEvidenceJson = await archiveAssetEvidence.json() as { created_count: number; total_runs: number; items: Array<{ run_id: string }> };
    expect(archiveAssetEvidenceJson).toMatchObject({
      created_count: 1,
      total_runs: expect.any(Number),
      items: [expect.objectContaining({ run_id: localRenderRun.id })],
    });
    await expect(page.getByText(/已归档 1 条运行证据/).last()).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => (await api.listPromptEvaluationEvidenceSnapshots(localRenderRun.id))[0] ?? null, { timeout: 10000 })
      .toMatchObject({
        run_id: localRenderRun.id,
        snapshot_type: "验收归档",
      });
    const downloadAssetEvidencePackageButton = optimizationRow.getByTestId(`download-asset-evidence-package-${optimizationRun!.id}`);
    await expect(downloadAssetEvidencePackageButton).toBeVisible({ timeout: 10000 });
    const assetEvidencePackageResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "GET" &&
        response.url().includes(`/api/prompt-evaluation-assets/${optimizationRun!.id}/evidence-snapshots/export`),
      { timeout: 15000 },
    );
    const assetEvidencePackageDownload = page.waitForEvent("download");
    await downloadAssetEvidencePackageButton.click();
    const [assetEvidencePackage, downloadedAssetEvidencePackage] = await Promise.all([assetEvidencePackageResponse, assetEvidencePackageDownload]);
    expect(assetEvidencePackage.status()).toBe(200);
    expect(downloadedAssetEvidencePackage.suggestedFilename()).toMatch(/^multica-training-asset-evidence-.*\.json$/);
    const downloadedAssetEvidencePackagePath = await downloadedAssetEvidencePackage.path();
    expect(downloadedAssetEvidencePackagePath).toBeTruthy();
    const assetEvidenceArchive = JSON.parse(await readFile(downloadedAssetEvidencePackagePath!, "utf8")) as Record<string, any>;
    expect(assetEvidenceArchive.schema_version).toBe("multica.prompt_evaluation.asset_evidence_archive.v1");
    expect(assetEvidenceArchive.asset_id).toBe(optimizationRun!.id);
    expect(assetEvidenceArchive.snapshot_type).toBe("验收归档");
    expect(assetEvidenceArchive.archived_run_count).toBeGreaterThanOrEqual(1);
    expect(assetEvidenceArchive.items).toEqual([
      expect.objectContaining({
        run: expect.objectContaining({ id: localRenderRun.id }),
        snapshots: [
          expect.objectContaining({
            run_id: localRenderRun.id,
            evidence: expect.objectContaining({
              服务端解释快照: expect.objectContaining({
                语义版本: "multica.prompt_evaluation.evidence_snapshot.insight.v1",
              }),
            }),
          }),
        ],
      }),
    ]);
    const findQueuedAgentRun = async () => {
        const assets = await api.listPromptEvaluationAssets({ prompt_id: prompt!.id });
        const agentAssetIds = assets
          .filter((asset) => asset.name.startsWith(`${artifactPrefix} 账号系统 澄清 智能体调试包`))
          .map((asset) => asset.id);
        const runs = await api.listPromptEvaluationRuns({ limit: 20 });
        return runs.find((run) => agentAssetIds.includes(run.asset_id) && run.run_kind === "Agent执行") ?? null;
      };
    await expect
      .poll(findQueuedAgentRun, { timeout: 15000 })
      .toMatchObject({
        run_kind: "Agent执行",
        status: "已入队",
        model: actualAgentModel,
        runtime_provider: "codex",
        runtime_id: actualAgentRuntimeId || expect.any(String),
        total_cases: 1,
        passed_cases: 0,
        failed_cases: 0,
        task_id: expect.any(String),
        chat_session_id: expect.any(String),
        conclusion: "等待智能体执行完成",
      });
    const queuedAgentRun = await findQueuedAgentRun();
    expect(queuedAgentRun).toBeTruthy();
    const queuedAgentAsset = (await api.listPromptEvaluationAssets({ prompt_id: prompt!.id })).find((asset) => asset.id === queuedAgentRun!.asset_id);
    expect(queuedAgentAsset).toBeTruthy();
    const agentEvidence = await api.getPromptEvaluationRunEvidence(queuedAgentRun!.id);
    expect(agentEvidence.run).toMatchObject({
      run_kind: "Agent执行",
      status: "已入队",
      model: actualAgentModel,
      runtime_provider: "codex",
      runtime_id: actualAgentRuntimeId || expect.any(String),
      task_id: queuedAgentRun!.task_id,
    });
    expect(agentEvidence.trials).toEqual([
      expect.objectContaining({
        case_name: "智能体调试场用例",
        status: "待执行",
        failure_reason: "等待智能体执行完成",
      }),
    ]);
    const actualAgentRuntimeName = String(agentEvidence.上下文.运行时名称 || expectedAgentRuntimeName);
    expect(agentEvidence.上下文).toMatchObject({
      提示词名称: prompt!.name,
      评测资产名称: queuedAgentAsset!.name,
      执行Agent名称: "Multica 训练评估智能体",
      运行时名称: actualAgentRuntimeName || expect.any(String),
      运行时提供方: "codex",
    });
    await api.completePromptEvaluationAgentTask(queuedAgentRun!);
    await expect
      .poll(async () => {
        const summary = await api.getPromptEvaluationSummary();
        return {
          hasRuns: summary.运行状态["运行总数"] >= 1,
          hasPassedCase: summary.指标["通过数"] >= 1,
          passRate: summary.指标["通过率"],
          hasAssets: summary.资产统计["资产总数"] >= 1,
        };
      }, { timeout: 15000 })
      .toEqual({
        hasRuns: true,
        hasPassedCase: true,
        passRate: expect.any(Number),
        hasAssets: true,
      });
    const futureSince = new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString();
    const futureSummary = await api.getPromptEvaluationSummary({ since: futureSince });
    expect(futureSummary.运行状态["运行总数"]).toBe(0);
    expect(futureSummary.指标["输入token"]).toBe(0);
    expect(futureSummary.资产统计["资产总数"]).toBeGreaterThan(0);
    const futureRuns = await api.listPromptEvaluationRuns({ since: futureSince, limit: 20 });
    expect(futureRuns).toHaveLength(0);
    await page.goto(`/${workspaceSlug}/training/run-history`, { waitUntil: "domcontentloaded" });
    await expect(page.getByText("智能体执行 · 已入队")).toBeVisible({ timeout: 10000 });
    await expect(page.getByText(`任务 ${queuedAgentRun!.task_id}`)).toBeVisible();
    let agentRunCard = page.locator("div.grid.gap-2.px-3.py-3").filter({ hasText: "智能体执行 · 已入队" }).first();
    const syncResponse = page.waitForResponse(
      (response) => response.request().method() === "POST" && response.url().includes(`/prompt-evaluation-runs/${queuedAgentRun!.id}/sync`),
      { timeout: 10000 },
    );
    await agentRunCard.getByRole("button", { name: "同步任务" }).click();
    expect((await syncResponse).status()).toBe(200);
    await expect(page.getByText("运行记录已同步")).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => (await api.listPromptEvaluationRuns({ asset_id: queuedAgentRun!.asset_id })).find((run) => run.id === queuedAgentRun!.id) ?? null, {
        timeout: 15000,
      })
      .toMatchObject({
        status: "通过",
        passed_cases: 1,
        failed_cases: 0,
      });
    const syncedAgentDimensionEvidence = await api.getPromptEvaluationRunEvidence(queuedAgentRun!.id);
    const syncedDimensionScores = Array.isArray(syncedAgentDimensionEvidence.run.metrics["实验维度评分"]) ? syncedAgentDimensionEvidence.run.metrics["实验维度评分"] as Array<Record<string, unknown>> : [];
    expect(syncedDimensionScores).toEqual(expect.arrayContaining([
      expect.objectContaining({
        维度名称: "上下文完整性",
        状态: "已评分",
        通过用例数: 1,
        评分规则: "逐用例沿用 Agent 结构化通过状态",
      }),
      expect.objectContaining({
        维度名称: "期望输出覆盖",
        状态: "已评分",
      }),
      expect.objectContaining({
        维度名称: "中文语义一致性",
        状态: "已评分",
      }),
    ]));
    expect(syncedAgentDimensionEvidence.evidence["实验维度评分"]).toEqual(expect.arrayContaining([
      expect.objectContaining({ 维度名称: "上下文完整性", 状态: "已评分" }),
    ]));
    await page.goto(`/${workspaceSlug}/training/run-history`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "运行历史", 10000);
    await showAcceptanceFixturesIfAvailable(page);
    await expect(page).toHaveURL(/training_data=acceptance/);
    const summaryStrip = page.getByTestId("training-summary-strip");
    await expect(summaryStrip).toContainText("项目总览", { timeout: 10000 });
    await expect(page.getByTestId("training-summary-运行总数")).toContainText(/[1-9]/);
    await expect(page.getByTestId("training-summary-通过率")).toContainText("%");
    await expect(page.getByTestId("training-summary-智能体运行数")).toContainText(/[1-9]/);
    await expect(page.getByTestId("training-summary-需人工复核")).toContainText(/\d/);
    await expect(page.getByTestId("training-summary-输入 token")).toContainText(/[1-9]/);
    await expect(page.getByTestId("training-summary-预估成本")).toContainText("$");
    await expect(page.getByTestId("training-summary-待确认优化候选")).toContainText(/\d/);
    await page.getByRole("link", { name: "运行看板", exact: true }).last().click();
    await expect(page).toHaveURL(/\/training\/runs\?training_data=acceptance/);
    await showAcceptanceFixturesIfAvailable(page);
    const demoDashboard = page.getByTestId("training-demo-dashboard");
    await expect(demoDashboard).toContainText("训练运行看板", { timeout: 10000 });
    await expect(demoDashboard).toContainText("训练评估闭环");
    await expect(demoDashboard).toContainText("SOP 与任务观测");
    await expect(demoDashboard.getByTestId("training-demo-metric-智能体运行数")).toContainText(/[1-9]/);
    await expect(demoDashboard.getByTestId("training-demo-proof-真实智能体证据")).toContainText("已有任务/trace 运行记录");
    await expect(demoDashboard.getByText("Codex 运行时可创建真实智能体任务")).toBeVisible();
    await expect(demoDashboard).toContainText("最近7天");
    await demoDashboard.getByRole("button", { name: "最近24小时" }).click();
    await expect(demoDashboard).toContainText("最近24小时");
    const evidenceDownload = page.waitForEvent("download");
    await demoDashboard.getByRole("button", { name: "导出证据 JSON" }).click();
    const downloadedEvidence = await evidenceDownload;
    expect(downloadedEvidence.suggestedFilename()).toMatch(/^multica-production-evidence-.*\.json$/);
    const downloadedEvidencePath = await downloadedEvidence.path();
    expect(downloadedEvidencePath).toBeTruthy();
    const exportedEvidence = JSON.parse(await readFile(downloadedEvidencePath!, "utf8")) as Record<string, any>;
    expect(exportedEvidence["语义版本"]).toBe("multica.production_demo_evidence.v1");
    const exportedRunEvidence = (exportedEvidence["最近运行证据"] as any[]).find((item) => item.run?.id === queuedAgentRun!.id);
    expect(exportedRunEvidence).toMatchObject({
      采集状态: "已采集",
      run: {
        id: queuedAgentRun!.id,
        task_id: queuedAgentRun!.task_id,
      },
    });
    expect(exportedRunEvidence.task_usage).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          task_id: queuedAgentRun!.task_id,
          provider: "codex",
          output_tokens: 7,
        }),
      ]),
    );
    expect(exportedRunEvidence.task_usage[0].input_tokens).toBeGreaterThan(0);
    expect(exportedRunEvidence.task_usage[0].model).toEqual(expect.any(String));
    expect(exportedRunEvidence.trace_events.length).toBeGreaterThan(0);
    expect(exportedRunEvidence.execution_spans.length).toBeGreaterThan(0);
    expect(exportedRunEvidence.tool_call_chains).toEqual(expect.any(Array));
    expect(exportedRunEvidence.tool_call_summary).toEqual(expect.any(Array));
    expect(exportedRunEvidence.execution_summary["span总数"]).toBeGreaterThan(0);
    expect(exportedEvidence["训练维度评分摘要"]).toEqual(expect.arrayContaining([
      expect.objectContaining({
        asset_id: queuedAgentRun!.asset_id,
        dimension_name: "上下文完整性",
      }),
    ]));
    expect(exportedEvidence["训练维度评分趋势"]).toEqual(expect.arrayContaining([
      expect.objectContaining({
        asset_id: queuedAgentRun!.asset_id,
        dimension_name: "上下文完整性",
        run_count: expect.any(Number),
      }),
    ]));
    expect(exportedEvidence["优化候选证据"]).toEqual(expect.any(Array));
    expect(exportedEvidence["证据统计"]["task_usage条数"]).toBeGreaterThan(0);
    expect(exportedEvidence["证据统计"]["trace_event条数"]).toBeGreaterThan(0);
    expect(exportedEvidence["证据统计"]["execution_span条数"]).toBeGreaterThan(0);
    expect(exportedEvidence["证据统计"]["tool_call_chain条数"]).toEqual(expect.any(Number));
    expect(exportedEvidence["证据统计"]["tool_call_summary条数"]).toEqual(expect.any(Number));
    expect(exportedEvidence["资产统计"]["维度评分摘要数"]).toBeGreaterThan(0);
    expect(exportedEvidence["资产统计"]["维度评分趋势数"]).toBeGreaterThan(0);
    await page.goto(`/${workspaceSlug}/training/run-history`, { waitUntil: "domcontentloaded" });
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/run-history$`), { timeout: 30000 });
    await expect(page.getByTestId("training-route-panel-run-history")).toBeVisible({ timeout: 10000 });
    agentRunCard = page.getByTestId(`prompt-evaluation-run-${queuedAgentRun!.id}`);
    await expect(agentRunCard).toContainText("智能体执行 · 通过", { timeout: 10000 });
    await expect(agentRunCard).toContainText(new RegExp(`模型 ${escapeRegExp(actualAgentModel)} · 运行时 codex · 通过 1\\/1 · 输入 16 token · 输出 7 token`));
    await clickStableButton(agentRunCard.getByRole("button", { name: "查看证据" }));
    const agentEvidencePanel = agentRunCard.getByTestId(`run-evidence-${queuedAgentRun!.id}`);
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-模型")).toContainText(actualAgentModel, { timeout: 10000 });
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-运行时")).toContainText("codex");
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-触发来源")).toContainText("智能体调试场");
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-创建者")).toContainText(/[0-9a-f-]{36}/);
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-智能体标识")).toContainText(queuedAgentRun!.agent_id!);
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-运行时标识")).toContainText(queuedAgentRun!.runtime_id!);
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-会话标识")).toContainText(queuedAgentRun!.chat_session_id!);
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-输入 token")).toContainText("16");
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-输出 token")).toContainText("7");
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-开始时间")).not.toContainText("未记录");
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-结束时间")).not.toContainText("未完成");
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-评估结论")).toContainText("Agent 返回结构化逐用例评估");
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText("上下文摘要");
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText(`提示词 ${prompt!.name}`);
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText(`评测资产 ${queuedAgentAsset!.name}`);
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText("智能体 Multica 训练评估智能体");
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText(`运行时 ${actualAgentRuntimeName}`);
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText(`任务 ${queuedAgentRun!.task_id}`);
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText("用量证据 1");
    const executionTree = agentEvidencePanel.getByTestId("run-evidence-execution-spans");
    await expect(executionTree).toContainText("执行观测树");
    await expect(executionTree).toContainText(`根任务 ${queuedAgentRun!.task_id}`);
    await expect(executionTree).toContainText("模型用量");
    await expect(executionTree).toContainText("训练评估用量已上报");
    await expect(executionTree.getByTestId("run-evidence-tool-call-chains")).toContainText(/工具调用链|暂无工具调用链/);
    const traceTree = agentEvidencePanel.getByTestId("run-evidence-trace-tree");
    await expect(traceTree).toContainText("任务事件树");
    await expect(traceTree).toContainText(`根任务 ${queuedAgentRun!.task_id}`);
    await expect(traceTree).toContainText("模型用量");
    await expect(traceTree).toContainText("训练评估用量已上报");
    await expect(traceTree).toContainText(/token \d+/);
    const snapshotBar = agentEvidencePanel.getByTestId("run-evidence-snapshots");
    await expect(snapshotBar).toContainText("暂无服务端归档", { timeout: 10000 });
    const snapshotResponse = page.waitForResponse(
      (response) => response.request().method() === "POST" && response.url().includes(`/prompt-evaluation-runs/${queuedAgentRun!.id}/evidence-snapshots`),
      { timeout: 10000 },
    );
    await snapshotBar.getByRole("button", { name: "归档快照" }).click();
    expect((await snapshotResponse).status()).toBe(201);
    await expect(page.getByText("服务端证据快照已归档")).toBeVisible({ timeout: 10000 });
    await expect(snapshotBar).toContainText("验收归档", { timeout: 10000 });
    await expect(snapshotBar).toContainText("1 条快照");
    await expect
      .poll(async () => (await api.listPromptEvaluationEvidenceSnapshots(queuedAgentRun!.id))[0] ?? null, { timeout: 10000 })
      .toMatchObject({
        run_id: queuedAgentRun!.id,
        snapshot_type: "验收归档",
        summary: expect.objectContaining({
          运行状态: "通过",
          "trace/task id": queuedAgentRun!.task_id,
          服务端解释: expect.objectContaining({
            质量判断: "质量稳定",
            维度摘要数: expect.any(Number),
            维度趋势数: expect.any(Number),
          }),
        }),
      });
    const snapshotList = await api.listPromptEvaluationEvidenceSnapshots(queuedAgentRun!.id);
    const snapshotDetail = await api.getPromptEvaluationEvidenceSnapshot(queuedAgentRun!.id, snapshotList[0]!.id);
    expect(snapshotDetail.evidence?.["服务端解释快照"]).toMatchObject({
      语义版本: "multica.prompt_evaluation.evidence_snapshot.insight.v1",
      质量判断: "质量稳定",
      建议动作: expect.any(String),
    });
    expect((snapshotDetail.evidence?.["服务端解释快照"] as Record<string, unknown>)["维度评分摘要"]).toEqual(expect.any(Array));
    expect((snapshotDetail.evidence?.["服务端解释快照"] as Record<string, unknown>)["维度评分趋势"]).toEqual(expect.any(Array));
    await page.goto(`/${workspaceSlug}/training/run-history`, { waitUntil: "domcontentloaded" });
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/run-history$`), { timeout: 30000 });
    await expect(page.getByTestId("training-route-panel-run-history")).toBeVisible({ timeout: 10000 });
    const localRunCard = page.getByTestId(`prompt-evaluation-run-${localRenderRun.id}`);
    await expect(localRunCard.getByText("模板渲染检查 · 通过")).toBeVisible({ timeout: 10000 });
    await expect(localRunCard.getByText(/模型 本地模板渲染检查 · 运行时 server · 通过 1\/1/)).toBeVisible();
    await clickStableButton(localRunCard.getByRole("button", { name: "查看证据" }));
    const localEvidencePanel = localRunCard.getByTestId(`run-evidence-${localRenderRun.id}`);
    await expect(localEvidencePanel.getByText("用例明细")).toBeVisible({ timeout: 10000 });
    await expect(localEvidencePanel.getByText("调试场用例", { exact: true })).toBeVisible();
    await expect(localRunCard.getByText("请澄清 登录失败，项目背景：账号系统。", { exact: true })).toBeVisible();
    await localEvidencePanel.getByText("完整运行证据 JSON").click();
    await expect(localEvidencePanel.getByText("\"task_usage\"")).toBeVisible();
    await expect(localEvidencePanel.getByText("\"trace_events\"")).toBeVisible();
    await localRunCard.getByRole("button", { name: "收起证据" }).click();
    agentRunCard = page.getByTestId(`prompt-evaluation-run-${queuedAgentRun!.id}`);
    await clickStableButton(agentRunCard.getByRole("button", { name: "查看证据" }));
    const syncedAgentEvidencePanel = agentRunCard.getByTestId(`run-evidence-${queuedAgentRun!.id}`);
    await expect(syncedAgentEvidencePanel).toContainText("智能体调试场用例", { timeout: 10000 });
    await expect(syncedAgentEvidencePanel).toContainText(/codex\/[^ ]+ · 输入 11 · 输出 7 · 预估成本 \$/);
    await expect(syncedAgentEvidencePanel).toContainText("缓存读 2 · 缓存写 3");
    await expect(syncedAgentEvidencePanel).toContainText("#1 text：Agent 输出：完成训练评估");
    await expect(syncedAgentEvidencePanel).toContainText(/训练评估用量已上报 · completed · codex\/[^ ]+ · 尝试次数 1 · .*输入 16 · 输出 7/);
    await expect(page.getByText("失败原因：等待智能体执行完成")).toHaveCount(0);
    await expect(syncedAgentEvidencePanel).toContainText("任务用量");
    const syncedAgentEvidence = await api.getPromptEvaluationRunEvidence(queuedAgentRun!.id);
    expect(syncedAgentEvidence.run).toMatchObject({
      status: "通过",
      passed_cases: 1,
      failed_cases: 0,
      input_tokens: 16,
      output_tokens: 7,
      conclusion: "Agent 返回结构化逐用例评估，全部用例通过",
    });
    expect(syncedAgentEvidence.trials).toEqual([
      expect.objectContaining({
        case_name: "智能体调试场用例",
        status: "通过",
        input_tokens: 16,
        output_tokens: 7,
        failure_reason: "无",
      }),
    ]);
    expect(syncedAgentEvidence.task_usage).toEqual([
      expect.objectContaining({
        provider: "codex",
        model: expect.any(String),
        input_tokens: 11,
        output_tokens: 7,
        cache_read_tokens: 2,
        cache_write_tokens: 3,
      }),
    ]);
    expect(syncedAgentEvidence.trace_events).toEqual([
      expect.objectContaining({
        event_name: "训练评估用量已上报",
        status: "completed",
        input_tokens: 16,
        output_tokens: 7,
      }),
    ]);
    expect(syncedAgentEvidence.execution_spans).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          span_kind: "任务根节点",
          span_name: "评估任务执行",
        }),
        expect.objectContaining({
          span_kind: "模型用量",
          span_name: "训练评估用量已上报",
        }),
      ]),
    );
    expect(syncedAgentEvidence.execution_summary).toMatchObject({
      "生命周期span数": expect.any(Number),
      "用量span数": expect.any(Number),
      "工具调用链数": expect.any(Number),
      "span总数": expect.any(Number),
    });

    await expect(api.updatePromptEvaluationAsset(dataset!.id, { status: "归档" })).resolves.toMatchObject({
      id: dataset!.id,
      status: "归档",
    });
    await api.deletePromptEvaluationAsset(testSuite!.id);
    await expect
      .poll(async () => {
        const assets = await api.listPromptEvaluationAssets({ prompt_id: prompt!.id });
        return {
          datasetStatus: assets.find((asset) => asset.id === dataset!.id)?.status ?? null,
          hasDeletedTestSuite: assets.some((asset) => asset.id === testSuite!.id),
        };
      }, { timeout: 15000 })
      .toEqual({ datasetStatus: "归档", hasDeletedTestSuite: false });

    await expect
      .poll(async () => {
        const assets = await api.listPromptEvaluationAssets({ asset_type: "实验" });
        const agentAssets = assets.filter((asset) => asset.name.startsWith(`${artifactPrefix} 账号系统 澄清 智能体调试包`));
        return {
          count: agentAssets.length,
          hasSnapshot: agentAssets.some((asset) => {
            const payload = asset.payload as Record<string, any>;
            return String(payload.调试包?.执行方式 ?? "").includes("Codex 运行时已在线");
          }),
          queuedRun: agentAssets.find((asset) => {
            const payload = asset.payload as Record<string, any>;
            return payload.最近Agent运行?.["trace/task id"] === queuedAgentRun!.task_id;
          }) ?? null,
        };
      }, { timeout: 15000 })
      .toMatchObject({
        count: 2,
        hasSnapshot: true,
        queuedRun: {
          asset_type: "实验",
          payload: {
            调试包: {
              期望输出: agentExpectedOutput,
            },
            最近Agent运行: {
              状态: "通过",
              模型: expectedAgentModel,
              runtime: "codex",
              runtime_id: expectedAgentRuntimeId || expect.any(String),
              "trace/task id": queuedAgentRun!.task_id,
              总用例数: 1,
              通过数: 1,
              失败数: 0,
              输入token: 16,
              输出token: 7,
              评估结论: "Agent 返回结构化逐用例评估，全部用例通过",
            },
          },
        },
      });
  });

  test("失败运行可以生成优化候选并人工发布新版本", async ({ page }) => {
    const promptName = `${artifactPrefix} 失败优化闭环`;
    const sourceContent = "请澄清 {{issue_title}}，输出必须使用中文。";
    await api.ensureOnlineCodexRuntime(`${artifactPrefix} 优化 Codex Runtime`);
    await refreshExpectedAgentModel();

    await page.getByRole("link", { name: "训练与评估" }).click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/runs$`), { timeout: 30000 });
    await expect(page.getByTestId("training-demo-dashboard")).toContainText("训练运行看板", { timeout: 10000 });
    await page.getByRole("link", { name: "提示词库", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/prompts$`), { timeout: 30000 });
    await page.getByRole("button", { name: "起草需求澄清模板" }).click();
    await page.getByLabel("名称").fill(promptName);
    await page.getByLabel("提示词内容").fill(sourceContent);
    await page.getByLabel("变量").fill("issue_title=任务标题");
    await page.getByLabel("调试变量").fill("issue_title=登录失败");
    await page.getByRole("button", { name: "保存" }).click();
    await expect(page.getByText("提示词已创建")).toBeVisible({ timeout: 10000 });

    const prompt = await expect
      .poll(async () => (await api.listPromptLibraryItems()).find((item) => item.name === promptName) ?? null, { timeout: 15000 })
      .not.toBeNull()
      .then(async () => (await api.listPromptLibraryItems()).find((item) => item.name === promptName)!);

    const asset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${promptName} 失败优化运行`,
      asset_type: "优化运行",
      payload: {
        cases: [
          {
            名称: "缺少验收与 trace",
            变量: { issue_title: "登录失败" },
            期望包含: ["验收条件", "trace/任务标识"],
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
      }, { timeout: 15000 })
      .not.toBeNull()
      .then(async () => (await api.listPromptEvaluationRuns({ asset_id: asset.id })).find((run) => run.status === "未通过")!);

    await page.goto(`/${workspaceSlug}/training/run-history`, { waitUntil: "domcontentloaded" });
    const failedRunRow = page.getByTestId(`prompt-evaluation-run-${failedRun.id}`);
    await failedRunRow.scrollIntoViewIfNeeded();
    await expect(failedRunRow).toContainText("模板渲染检查 · 未通过", { timeout: 10000 });
    const optimizationAgentResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes(`/prompt-evaluation-runs/${failedRun.id}/optimization-agent-run`),
      { timeout: 10000 },
    );
    await failedRunRow.getByRole("button", { name: "智能体优化任务" }).click();
    expect((await optimizationAgentResponse).status()).toBe(202);
    await expect(page.getByText(/真实智能体优化任务已入队/)).toBeVisible({ timeout: 10000 });

    let optimizationAgentRun = null as Awaited<ReturnType<typeof api.listPromptEvaluationRuns>>[number] | null;
    await expect
      .poll(async () => {
        const assets = await api.listPromptEvaluationAssets({ prompt_id: prompt.id, asset_type: "优化运行" });
        const agentAsset = assets.find((item) => item.name.startsWith(`${promptName} 智能体优化运行`));
        if (!agentAsset) return null;
        const runs = await api.listPromptEvaluationRuns({ asset_id: agentAsset.id });
        const agentRun = runs.find((run) => run.run_kind === "Agent执行") ?? null;
        optimizationAgentRun = agentRun;
        return agentRun
          ? {
              asset_type: agentAsset.asset_type,
              taskType: (agentAsset.payload as Record<string, any>).任务类型,
              sourceRun: (agentAsset.payload as Record<string, any>).来源运行,
              run_kind: agentRun.run_kind,
              status: agentRun.status,
              model: agentRun.model,
              runtime_provider: agentRun.runtime_provider,
              runtime_id: agentRun.runtime_id,
              hasTask: Boolean(agentRun.task_id),
            }
          : null;
      }, { timeout: 15000 })
      .toMatchObject({
        asset_type: "优化运行",
        taskType: "智能体优化运行",
        sourceRun: failedRun.id,
        run_kind: "Agent执行",
        status: "已入队",
        model: expectedAgentModel,
        runtime_provider: "codex",
        runtime_id: expectedAgentRuntimeId || expect.any(String),
        hasTask: true,
      });

    if (!optimizationAgentRun) {
      throw new Error("E2E 未找到智能体优化运行记录");
    }
    await api.completePromptEvaluationOptimizationAgentTask(optimizationAgentRun);
    await page.goto(`/${workspaceSlug}/training/run-history`, { waitUntil: "domcontentloaded" });
    const optimizationRunRow = page.getByTestId(`prompt-evaluation-run-${optimizationAgentRun.id}`);
    await optimizationRunRow.scrollIntoViewIfNeeded();
    const optimizationSyncResponse = page.waitForResponse(
      (response) => response.request().method() === "POST" && response.url().includes(`/prompt-evaluation-runs/${optimizationAgentRun!.id}/sync`),
      { timeout: 10000 },
    );
    await optimizationRunRow.getByRole("button", { name: "同步任务" }).click();
    expect((await optimizationSyncResponse).status()).toBe(200);
    await expect(page.getByText("运行记录已同步")).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const candidates = await api.listPromptEvaluationOptimizationCandidates({ run_id: failedRun.id });
        return candidates[0] ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        status: "待确认",
        failed_case_count: 1,
        prompt_id: prompt.id,
      });
    const generatedCandidate = (await api.listPromptEvaluationOptimizationCandidates({ run_id: failedRun.id }))[0];
    expect(generatedCandidate).toBeTruthy();
    const editedCandidateContent = `${generatedCandidate!.candidate_content}\n\n【E2E人工复核】候选发布前已补充验收条件和 trace/任务标识保留要求。`;

    await page.getByRole("link", { name: "优化运行", exact: true }).last().click();
    const candidateRow = page.getByTestId(`prompt-evaluation-candidate-${generatedCandidate!.id}`);
    await expect(candidateRow.getByText(/待确认 · 失败 1/)).toBeVisible({ timeout: 10000 });
    await candidateRow.getByRole("button", { name: "编辑候选" }).click();
    await candidateRow.getByLabel("候选提示词正文").fill(editedCandidateContent);
    await candidateRow.getByLabel("优化依据").fill("E2E 人工复核：发布前确认候选正文保留中文验收口径。");
    await candidateRow.getByRole("button", { name: "保存候选" }).click();
    await expect(page.getByText(/优化候选已保存/)).toBeVisible({ timeout: 10000 });
    await expect(candidateRow).toContainText("已人工编辑");
    await candidateRow.getByRole("button", { name: "发布新版本" }).click();
    await expect(page.getByText(/已发布新提示词版本/)).toBeVisible({ timeout: 10000 });
    await page.getByRole("link", { name: "提示词库", exact: true }).last().click();
    await expect(page.getByTestId("prompt-version-history")).toContainText("优化候选发布", { timeout: 10000 });
    await expect(page.getByTestId("prompt-version-history")).toContainText(generatedCandidate!.id);

    await expect
      .poll(async () => {
        const candidates = await api.listPromptEvaluationOptimizationCandidates({ run_id: failedRun.id });
        return candidates[0] ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        status: "已发布",
        failed_case_count: 1,
        prompt_id: prompt.id,
      });

    const prompts = await api.listPromptLibraryItems();
    const original = prompts.find((item) => item.id === prompt.id);
    const published = prompts.find((item) => item.name.startsWith(`${promptName} 优化发布`));
    expect(original).toMatchObject({ id: prompt.id, content: sourceContent, version: 1 });
    expect(published).toMatchObject({
      prompt_type: "需求澄清",
      version: 2,
    });
    expect(published?.content).toContain("优化候选");
    expect(published?.content).toContain("人工发布要求");
    expect(published?.content).toContain("E2E人工复核");
    const originalVersions = await api.listPromptLibraryVersions(prompt.id);
    expect(originalVersions).toEqual([
      expect.objectContaining({ version: 1, source: "手动创建", content: sourceContent }),
    ]);
    const publishedVersions = await api.listPromptLibraryVersions(published!.id);
    expect(publishedVersions).toEqual([
      expect.objectContaining({
        version: 2,
        source: "优化候选发布",
        source_candidate_id: generatedCandidate!.id,
      }),
    ]);

    await api.runPromptEvaluationAsset(asset.id);
    const rejectRun = await expect
      .poll(async () => {
        const runs = await api.listPromptEvaluationRuns({ asset_id: asset.id });
        return runs.find((run) => run.status === "未通过" && run.id !== failedRun.id) ?? null;
      }, { timeout: 15000 })
      .not.toBeNull()
      .then(async () => (await api.listPromptEvaluationRuns({ asset_id: asset.id })).find((run) => run.status === "未通过" && run.id !== failedRun.id)!);

    await page.goto(`/${workspaceSlug}/training/run-history`, { waitUntil: "domcontentloaded" });
    const rejectRunRow = page.getByTestId(`prompt-evaluation-run-${rejectRun.id}`);
    await rejectRunRow.scrollIntoViewIfNeeded();
    await rejectRunRow.getByRole("button", { name: "生成优化候选" }).click();
    await expect(page.getByText("优化候选已生成，等待人工确认")).toBeVisible({ timeout: 10000 });
    const rejectCandidate = await expect
      .poll(async () => {
        const candidates = await api.listPromptEvaluationOptimizationCandidates({ run_id: rejectRun.id });
        return candidates[0] ?? null;
      }, { timeout: 15000 })
      .not.toBeNull()
      .then(async () => (await api.listPromptEvaluationOptimizationCandidates({ run_id: rejectRun.id }))[0]!);

    await page.getByRole("link", { name: "优化运行", exact: true }).last().click();
    const rejectCandidateRow = page.getByTestId(`prompt-evaluation-candidate-${rejectCandidate.id}`);
    await rejectCandidateRow.getByRole("button", { name: "暂不采纳" }).click();
    await rejectCandidateRow.getByLabel("暂不采纳原因").fill("E2E 拒绝原因：候选没有覆盖全部验收口径。");
    await rejectCandidateRow.getByRole("button", { name: "确认暂不采纳" }).click();
    await expect(page.getByText(/已暂不采纳优化候选/)).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const candidates = await api.listPromptEvaluationOptimizationCandidates({ run_id: rejectRun.id });
        return candidates[0] ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        status: "已拒绝",
        metrics: {
          人工处理: {
            处理结果: "已拒绝",
            拒绝原因: "E2E 拒绝原因：候选没有覆盖全部验收口径。",
          },
        },
      });
  });

  test("提示词调试场和智能体调试场首屏职责不同", async ({ page }) => {
    await page.goto(`/${workspaceSlug}/training/prompt-playground`, { waitUntil: "domcontentloaded" });
    await expect(page.getByTestId("prompt-playground-page-shell")).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId("playground-page-contract")).toContainText("本地渲染 · 不启动智能体");
    await expect(page.getByTestId("prompt-playground-selector-summary")).toContainText("本地模板目录");
    await expect(page.getByTestId("prompt-playground-selector-summary")).toContainText("质检工作单");
    await expect(page.getByTestId("prompt-playground-inspection-board")).toBeVisible();
    await expect(page.getByTestId("prompt-playground-local-pipeline")).toContainText("保存质检记录");
    await expect(page.getByTestId("prompt-playground-quality-gate")).toContainText("质检结论");
    await expect(page.getByTestId("prompt-playground-template-lab")).toBeVisible();
    await expect(page.getByTestId("prompt-playground-source-panel")).toContainText("模板源");
    await expect(page.getByTestId("prompt-playground-variable-checklist")).toContainText("变量样本");
    await expect(page.getByTestId("prompt-playground-template-source")).toBeVisible();
    const sourcePanelBox = await page.getByTestId("prompt-playground-source-panel").boundingBox();
    const variablePanelBox = await page.getByTestId("prompt-playground-variable-checklist").boundingBox();
    const renderedOutputBox = await page.getByTestId("prompt-playground-rendered-output").boundingBox();
    const viewport = page.viewportSize();
    for (const box of [sourcePanelBox, variablePanelBox, renderedOutputBox]) {
      expect(box?.width ?? 0).toBeGreaterThan(160);
      expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual((viewport?.width ?? 0) + 1);
    }
    await expect(page.getByTestId("agent-playground-run-console")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "创建真实智能体任务" })).toHaveCount(0);

    await page.goto(`/${workspaceSlug}/training/agent-playground`, { waitUntil: "domcontentloaded" });
    await expect(page.getByTestId("agent-playground-page-shell")).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId("playground-page-contract")).toContainText("真实任务 · 写回观测证据");
    await expect(page.getByTestId("agent-playground-selector-summary")).toContainText("执行目标池");
    await expect(page.getByTestId("agent-playground-target-queue")).toBeVisible();
    await expect(page.getByTestId("agent-playground-target-queue-item").first()).toContainText("入队目标");
    await expect(page.getByTestId("agent-playground-run-console")).toContainText("真实任务发射台");
    await expect(page.getByTestId("agent-playground-agent-selector")).toContainText("执行智能体");
    await expect(page.getByTestId("agent-playground-agent-selector")).toContainText("自动选择训练评估智能体");
    await expect(page.getByTestId("agent-playground-task-pipeline")).toContainText("创建真实任务");
    await expect(page.getByTestId("agent-playground-execution-bus")).toContainText("执行节点");
    await expect(page.getByTestId("agent-playground-execution-bus")).toContainText("Trace");
    await expect(page.getByTestId("agent-playground-execution-bus")).toContainText("用量");
    await expect(page.getByTestId("agent-playground-observability-contract")).toContainText("观测回写契约");
    await expect(page.getByTestId("agent-playground-config-comparison")).toContainText("执行配置对比");
    await expect(page.getByTestId("agent-playground-config-comparison")).toContainText("当前待执行配置");
    await expect(page.getByTestId("agent-playground-config-comparison")).toContainText("执行智能体");
    await expect(page.getByTestId("agent-playground-config-comparison")).toContainText("模型");
    await expect(page.getByTestId("agent-playground-run-comparison")).toContainText("最近运行横向对比");
    await expect(page.getByTestId("agent-playground-run-comparison")).toContainText("耗时");
    await expect(page.getByTestId("agent-playground-run-comparison")).toContainText("token");
    await expect(page.getByTestId("agent-playground-model-matrix")).toContainText("模型参数矩阵");
    await expect(page.getByTestId("agent-playground-model-matrix")).toContainText("当前待执行");
    await expect(page.getByTestId("agent-playground-model-matrix")).toContainText("思考等级");
    await expect(page.getByTestId("agent-playground-model-matrix")).toContainText("参数");
    await expect(page.getByTestId("agent-playground-tool-env-diff")).toContainText("工具与环境差异");
    await expect(page.getByTestId("agent-playground-tool-env-diff")).toContainText("密钥只显示数量");
    await expect(page.getByTestId("agent-playground-tool-env-diff")).toContainText("环境变量");
    await expect(page.getByTestId("agent-playground-tool-env-diff")).toContainText("MCP");
    await expect(page.getByRole("button", { name: "创建真实智能体任务" })).toBeVisible();
    await expect(page.getByTestId("prompt-playground-template-lab")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "保存本地渲染检查" })).toHaveCount(0);
  });

  test("智能体调试场横向对比可以跳到运行证据", async ({ page }) => {
    test.setTimeout(90_000);
    await api.ensureOnlineCodexRuntime(`${artifactPrefix} 证据跳转 Runtime`);
    const prompt = await api.createPromptForE2E(artifactPrefix, {
      name: `${artifactPrefix} 智能体证据跳转`,
      content: "请评估 {{issue_title}}，输出中文结论和证据链接。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
    });
    const asset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} 智能体证据跳转实验`,
      description: "E2E 验证智能体调试场横向对比可以跳转运行证据",
      asset_type: "实验",
      payload: {
        cases: [
          {
            名称: "证据跳转用例",
            变量: { issue_title: "智能体调试场证据跳转" },
            期望包含: ["中文结论", "证据链接"],
          },
        ],
      },
      status: "启用",
    });
    const agentRun = await api.runPromptEvaluationAssetAgent(asset.id);

    await page.goto(`/${workspaceSlug}/training/agent-playground`, { waitUntil: "domcontentloaded" });
    await expect(page.getByTestId("agent-playground-page-shell")).toBeVisible({ timeout: 10000 });
    await page.getByPlaceholder("搜索执行目标").fill(prompt.name);
    await page.getByRole("button", { name: new RegExp(escapeRegExp(prompt.name)) }).click();
    const evidenceLink = page.getByTestId(`agent-playground-run-evidence-link-${agentRun.run.id}`);
    await expect(evidenceLink).toBeVisible({ timeout: 10000 });
    await evidenceLink.click();

    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/run-history\\?run=${escapeRegExp(agentRun.run.id)}`), { timeout: 10000 });
    const runRow = page.getByTestId(`prompt-evaluation-run-${agentRun.run.id}`);
    await expect(runRow).toBeVisible({ timeout: 10000 });
    await expect(runRow).toContainText(`任务 ${agentRun.task_id}`);
    await expect(runRow.getByRole("button", { name: "收起证据" })).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId(`run-evidence-${agentRun.run.id}`)).toBeVisible({ timeout: 15000 });
  });

  test("智能体调试场失败运行可以直接生成优化候选", async ({ page }) => {
    test.setTimeout(90_000);
    await api.ensureOnlineCodexRuntime(`${artifactPrefix} 调试场失败回灌 Runtime`);
    const prompt = await api.createPromptForE2E(artifactPrefix, {
      name: `${artifactPrefix} 智能体失败回灌`,
      content: "请评估 {{issue_title}}，输出中文结论、验收条件和 trace/task id。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
    });
    const asset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} 智能体失败回灌实验`,
      description: "E2E 验证智能体调试场失败运行可以直接生成优化候选",
      asset_type: "实验",
      payload: {
        cases: [
          {
            名称: "失败回灌用例",
            变量: { issue_title: "智能体调试场失败回灌" },
            期望包含: ["验收条件", "trace/task id"],
          },
        ],
      },
      status: "启用",
    });
    const agentRun = await api.runPromptEvaluationAssetAgent(asset.id);
    if (!agentRun.task_id || !agentRun.runtime_id) {
      throw new Error(`智能体调试场失败回灌缺少 task/runtime：${JSON.stringify(agentRun)}`);
    }
    const claimed = await api.claimDaemonTask(agentRun.runtime_id);
    expect(claimed.task?.id).toBe(agentRun.task_id);
    await api.startDaemonTask(agentRun.task_id);
    await api.reportDaemonTaskUsage(agentRun.task_id, {
      model: agentRun.model,
      input_tokens: 42,
      output_tokens: 8,
      cache_read_tokens: 3,
      cache_write_tokens: 2,
    });
    await api.reportDaemonTaskMessages(agentRun.task_id, [
      { seq: 1, type: "text", content: "Agent 输出：未能生成验收条件和 trace/task id。" },
      {
        seq: 2,
        type: "tool_result",
        tool: "prompt-evaluation",
        content: "断言未命中",
        output: "缺失验收条件、trace/task id",
      },
    ]);
    await api.failDaemonTask(agentRun.task_id, {
      error: "Agent 输出缺失验收证据",
      failure_reason: "assertion_mismatch",
    });
    const failedRun = await api.syncPromptEvaluationRun(agentRun.run.id);
    expect(failedRun).toMatchObject({
      id: agentRun.run.id,
      status: "失败",
    });
    expect(failedRun.failure_reason).toContain("未能生成验收条件");

    await page.goto(`/${workspaceSlug}/training/agent-playground`, { waitUntil: "domcontentloaded" });
    await expect(page.getByTestId("agent-playground-page-shell")).toBeVisible({ timeout: 10000 });
    await page.getByPlaceholder("搜索执行目标").fill(prompt.name);
    await page.getByRole("button", { name: new RegExp(escapeRegExp(prompt.name)) }).click();
    const runRow = page.getByTestId(`agent-playground-run-comparison-row-${agentRun.run.id}`);
    await expect(runRow).toBeVisible({ timeout: 10000 });
    await expect(runRow).toContainText("失败用例 1");
    await expect(runRow.getByTestId(`agent-playground-run-evidence-link-${agentRun.run.id}`)).toBeVisible();

    const candidateResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes(`/api/prompt-evaluation-runs/${agentRun.run.id}/optimization-candidates`),
      { timeout: 10000 },
    );
    await runRow.getByTestId(`agent-playground-run-generate-candidate-${agentRun.run.id}`).click();
    expect((await candidateResponse).status()).toBe(201);
    await expect(page.getByText(/优化候选已生成/)).toBeVisible({ timeout: 10000 });

    await expect
      .poll(async () => {
        const candidates = await api.listPromptEvaluationOptimizationCandidates({ run_id: agentRun.run.id });
        return candidates[0] ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        run_id: agentRun.run.id,
        prompt_id: prompt.id,
        status: "待确认",
      });
    const candidates = await api.listPromptEvaluationOptimizationCandidates({ run_id: agentRun.run.id });
    const candidate = candidates[0]!;
    expect(candidate.metrics).toMatchObject({
      候选优先级: "高",
      候选优先级依据: expect.stringContaining("实验维度评分摘要"),
    });
    expect(candidate.metrics["失败维度"]).toEqual(expect.arrayContaining([
      expect.objectContaining({ 维度名称: "命中率", 优先级: "高" }),
    ]));
    await expect(runRow.getByTestId(`agent-playground-run-generate-candidate-${agentRun.run.id}`)).toContainText("已有候选", { timeout: 10000 });

    await page.goto(`/${workspaceSlug}/training/optimization-runs?training_data=acceptance`, { waitUntil: "domcontentloaded" });
    const candidateRow = page.getByTestId(`prompt-evaluation-candidate-${candidate.id}`);
    await expect(candidateRow).toBeVisible({ timeout: 15000 });
    await expect(candidateRow).toContainText("优先级 高");
    await expect(candidateRow.getByTestId(`optimization-candidate-weak-dimensions-${candidate.id}`)).toContainText("命中率");
  });

  test("数据集版本可以对比并恢复为新的可追溯版本", async ({ page }) => {
    test.setTimeout(90_000);
    const prompt = await api.createPromptForE2E(artifactPrefix, {
      name: `${artifactPrefix} 数据集版本提示词`,
      content: "请处理 {{issue_title}}，输出中文验收结论。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
    });
    const dataset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} 数据集版本治理`,
      description: "E2E 公开 API 和 UI 验证数据集版本对比恢复",
      asset_type: "数据集",
      payload: {
        cases: [
          {
            名称: "版本一登录用例",
            变量: { issue_title: "登录失败" },
            期望包含: ["中文验收结论"],
            标签: ["版本一"],
          },
        ],
      },
      status: "启用",
    });
    const v1 = await api.createPromptEvaluationDatasetVersion(dataset.id, { version_label: "E2E v1" });
    await api.createPromptEvaluationCase({
      asset_id: dataset.id,
      prompt_id: prompt.id,
      case_index: 1,
      case_name: "版本二新增用例",
      variables: { issue_title: "新增 API" },
      expected_contains: ["新增 API 验收"],
      tags: ["版本二"],
      status: "启用",
    });
    const v2 = await api.createPromptEvaluationDatasetVersion(dataset.id, { version_label: "E2E v2" });
    const apiDiff = await api.diffPromptEvaluationDatasetVersion(dataset.id, v1.id, v2.id);
    expect(apiDiff.summary["新增"]).toBe(1);
    expect(apiDiff.summary["未变更"]).toBe(1);

    await page.goto(`/${workspaceSlug}/training/datasets`, { waitUntil: "domcontentloaded" });
    await page.getByRole("button", { name: "显示验收数据" }).first().click();
    const datasetRow = page.getByTestId(`prompt-evaluation-asset-${dataset.id}`);
    await expect(datasetRow).toBeVisible({ timeout: 15000 });
    const governance = datasetRow.getByTestId(`dataset-case-governance-${dataset.id}`);
    await expect(governance).toContainText("数据集用例治理", { timeout: 15000 });
    await expect(governance.getByTestId(`dataset-case-filter-count-${dataset.id}`)).toContainText("命中 2 / 2");
    await governance.getByLabel("筛选数据集用例标签").selectOption("版本二");
    await expect(governance.getByTestId(`dataset-case-filter-count-${dataset.id}`)).toContainText("命中 1 / 2");
    await expect(governance.getByTestId(`dataset-case-sampling-preview-${dataset.id}`)).toContainText("版本二新增用例");
    await governance.getByLabel("筛选数据集用例标签").selectOption("全部");
    await governance.getByRole("button", { name: "手工" }).click();
    await expect(governance.getByTestId(`dataset-case-filter-count-${dataset.id}`)).toContainText("命中 1 / 2");
    await expect(governance.getByTestId(`dataset-case-sampling-preview-${dataset.id}`)).toContainText("版本二新增用例");
    await governance.getByRole("button", { name: "资产载荷" }).click();
    await expect(governance.getByTestId(`dataset-case-filter-count-${dataset.id}`)).toContainText("命中 1 / 2");
    await expect(governance.getByTestId(`dataset-case-sampling-preview-${dataset.id}`)).toContainText("版本一登录用例");
    await datasetRow.getByRole("button", { name: "编辑标签" }).click();
    await datasetRow.getByLabel("编辑数据集标签").fill("版本一, 资产载荷, 领导演示");
    const tagUpdateResponse = page.waitForResponse(
      (response) => response.request().method() === "PUT" && response.url().includes("/api/prompt-evaluation-cases/"),
      { timeout: 15000 },
    );
    await datasetRow.getByRole("button", { name: "保存标签" }).click();
    expect((await tagUpdateResponse).status()).toBe(200);
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: dataset.id });
        return items.find((item) => item.case_name === "版本一登录用例") ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        source: "payload",
        tags: expect.arrayContaining(["资产载荷", "领导演示"]),
      });
    await governance.getByLabel("筛选数据集用例标签").selectOption("领导演示");
    await expect(governance.getByTestId(`dataset-case-filter-count-${dataset.id}`)).toContainText("命中 1 / 2");
    await expect(governance.getByTestId(`dataset-case-tag-stats-${dataset.id}`)).toContainText("领导演示 1");
    await governance.getByLabel("筛选数据集用例关键词").fill("登录");
    await expect(governance.getByTestId(`dataset-case-filter-count-${dataset.id}`)).toContainText("命中 1 / 2");
    await expect(governance.getByTestId(`dataset-case-sampling-preview-${dataset.id}`)).toContainText("版本一登录用例");
    await expect
      .poll(async () => api.listPromptEvaluationCases({ asset_id: dataset.id, source: "payload", tag: "领导演示", keyword: "登录", limit: 20 }), { timeout: 15000 })
      .toEqual([
        expect.objectContaining({
          case_name: "版本一登录用例",
          source: "payload",
          tags: expect.arrayContaining(["领导演示"]),
        }),
      ]);
    const serverSearchResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "GET" &&
        response.url().includes("/api/prompt-evaluation-cases") &&
        response.url().includes("source=payload") &&
        response.url().includes("tag=%E9%A2%86%E5%AF%BC%E6%BC%94%E7%A4%BA") &&
        response.url().includes("keyword=%E7%99%BB%E5%BD%95"),
      { timeout: 15000 },
    );
    await governance.getByTestId(`dataset-case-server-search-button-${dataset.id}`).click();
    expect((await serverSearchResponse).status()).toBe(200);
    await expect(governance.getByTestId(`dataset-case-server-search-result-${dataset.id}`)).toContainText("服务端返回 1 条", { timeout: 15000 });
    await expect(governance.getByTestId(`dataset-case-server-search-result-${dataset.id}`)).toContainText("版本一登录用例");
    await governance.getByLabel("数据集筛选方案名称").fill("登录领导样本");
    const saveFilterResponse = page.waitForResponse(
      (response) => response.request().method() === "PUT" && response.url().includes(`/api/prompt-evaluation-assets/${dataset.id}`),
      { timeout: 15000 },
    );
    await governance.getByTestId(`dataset-case-save-filter-${dataset.id}`).click();
    expect((await saveFilterResponse).status()).toBe(200);
    await expect(governance.getByTestId(`dataset-case-saved-filters-${dataset.id}`)).toContainText("登录领导样本", { timeout: 15000 });
    await expect
      .poll(async () => {
        const assets = await api.listPromptEvaluationAssets({ asset_type: "数据集" });
        const current = assets.find((item) => item.id === dataset.id);
        return (current?.payload as Record<string, any> | undefined)?.["数据集筛选方案"]?.[0] ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        name: "登录领导样本",
        source_filter: "资产载荷",
        tag_filter: "领导演示",
        keyword_filter: "登录",
      });
    await governance.getByLabel("筛选数据集用例关键词").fill("");
    await governance.getByRole("button", { name: "全部" }).click();
    await governance.getByLabel("筛选数据集用例标签").selectOption("全部");
    await expect(governance.getByTestId(`dataset-case-filter-count-${dataset.id}`)).toContainText("命中 2 / 2");
    await governance.locator(`[data-testid^="dataset-case-apply-filter-${dataset.id}-"]`).first().click();
    await expect(governance.getByTestId(`dataset-case-filter-count-${dataset.id}`)).toContainText("命中 1 / 2");
    await expect(governance.getByLabel("筛选数据集用例关键词")).toHaveValue("登录");
    await governance.getByLabel("批量处理数据集用例标签").fill("批量验收");
    const bulkAddResponse = page.waitForResponse(
      (response) => response.request().method() === "POST" && response.url().includes("/api/prompt-evaluation-cases/bulk-tags"),
      { timeout: 15000 },
    );
    await governance.getByTestId(`dataset-case-bulk-add-tags-${dataset.id}`).click();
    expect((await bulkAddResponse).status()).toBe(200);
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: dataset.id });
        return items.find((item) => item.case_name === "版本一登录用例")?.tags.includes("批量验收") ?? false;
      }, { timeout: 15000 })
      .toBe(true);
    await expect(governance.getByTestId(`dataset-case-tag-stats-${dataset.id}`)).toContainText("批量验收 1");
    await expect(governance.getByTestId(`dataset-case-operation-audit-${dataset.id}`)).toContainText("批量追加标签", { timeout: 15000 });
    await expect(governance.getByTestId(`dataset-case-operation-audit-${dataset.id}`)).toContainText("变更 1");
    await expect
      .poll(async () => {
        const operations = await api.listPromptEvaluationCaseOperations(dataset.id, { limit: 5 });
        return operations.items[0] ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        operation_type: "批量追加标签",
        changed_count: 1,
      });
    const operationHistoryResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "GET" &&
        response.url().includes(`/api/prompt-evaluation-assets/${dataset.id}/case-operations`),
      { timeout: 15000 },
    );
    await governance.getByTestId(`dataset-case-load-operation-audit-${dataset.id}`).click();
    expect((await operationHistoryResponse).status()).toBe(200);
    await expect(governance.getByTestId(`dataset-case-operation-audit-${dataset.id}`)).toContainText("批量追加标签", { timeout: 15000 });
    await expect
      .poll(async () => {
        const assets = await api.listPromptEvaluationAssets({ asset_type: "数据集" });
        const current = assets.find((item) => item.id === dataset.id);
        const payloadCase = ((current?.payload as Record<string, any> | undefined)?.cases as Array<Record<string, any>> | undefined)?.[0];
        return Array.isArray(payloadCase?.tags) ? payloadCase.tags : payloadCase?.["标签"] ?? [];
      }, { timeout: 15000 })
      .toEqual(expect.arrayContaining(["批量验收"]));
    const datasetSampleDownload = page.waitForEvent("download");
    await governance.getByTestId(`download-dataset-sample-${dataset.id}`).click();
    const downloadedDatasetSample = await datasetSampleDownload;
    expect(downloadedDatasetSample.suggestedFilename()).toMatch(/^multica-dataset-sample-.*\.json$/);
    const downloadedDatasetSamplePath = await downloadedDatasetSample.path();
    expect(downloadedDatasetSamplePath).toBeTruthy();
    const exportedDatasetSample = JSON.parse(await readFile(downloadedDatasetSamplePath!, "utf8")) as Record<string, any>;
    expect(exportedDatasetSample.schema_version).toBe("multica.prompt_evaluation.dataset_sample_export.v1");
    expect(exportedDatasetSample["数据集"]).toMatchObject({ id: dataset.id, 名称: dataset.name });
    expect(exportedDatasetSample["筛选条件"]).toEqual({ 来源: "资产载荷", 标签: "领导演示", 关键词: "登录" });
    expect(exportedDatasetSample["统计"]).toMatchObject({ 总用例数: 2, 命中用例数: 1, 采样预览数: 1 });
    expect(exportedDatasetSample["命中用例"]).toEqual([
      expect.objectContaining({
        名称: "版本一登录用例",
        来源: "资产载荷",
        标签: expect.arrayContaining(["领导演示", "批量验收"]),
      }),
    ]);
    const bulkRemoveResponse = page.waitForResponse(
      (response) => response.request().method() === "POST" && response.url().includes("/api/prompt-evaluation-cases/bulk-tags"),
      { timeout: 15000 },
    );
    await governance.getByTestId(`dataset-case-bulk-remove-tags-${dataset.id}`).click();
    expect((await bulkRemoveResponse).status()).toBe(200);
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: dataset.id });
        return items.find((item) => item.case_name === "版本一登录用例")?.tags.includes("批量验收") ?? true;
      }, { timeout: 15000 })
      .toBe(false);
    await expect(governance.getByTestId(`dataset-case-operation-audit-${dataset.id}`)).toContainText("批量移除标签", { timeout: 15000 });
    await governance.getByLabel("选择要整理的数据集标签").selectOption("领导演示");
    await governance.getByLabel("输入整理后的数据集标签").fill("领导展示");
    const renameTagResponse = page.waitForResponse(
      (response) => response.request().method() === "POST" && response.url().includes("/api/prompt-evaluation-cases/bulk-tags"),
      { timeout: 15000 },
    );
    await governance.getByTestId(`dataset-case-rename-tag-${dataset.id}`).click();
    expect((await renameTagResponse).status()).toBe(200);
    await expect(governance.getByTestId(`dataset-case-operation-audit-${dataset.id}`)).toContainText("批量重命名/合并标签", { timeout: 15000 });
    await expect
      .poll(async () => {
        const operations = await api.listPromptEvaluationCaseOperations(dataset.id, { limit: 5 });
        return operations.items[0] ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        operation_type: "批量重命名/合并标签",
        changed_count: 1,
      });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: dataset.id });
        const current = items.find((item) => item.case_name === "版本一登录用例");
        return current ? { hasNewTag: current.tags.includes("领导展示"), hasOldTag: current.tags.includes("领导演示") } : null;
      }, { timeout: 15000 })
      .toEqual({ hasNewTag: true, hasOldTag: false });
    await expect
      .poll(async () => {
        const assets = await api.listPromptEvaluationAssets({ asset_type: "数据集" });
        const current = assets.find((item) => item.id === dataset.id);
        const payloadCase = ((current?.payload as Record<string, any> | undefined)?.cases as Array<Record<string, any>> | undefined)?.[0];
        return Array.isArray(payloadCase?.tags) ? payloadCase.tags : payloadCase?.["标签"] ?? [];
      }, { timeout: 15000 })
      .toEqual(expect.arrayContaining(["领导展示"]));
    await expect(governance.getByTestId(`dataset-case-tag-stats-${dataset.id}`)).toContainText("领导展示 1");
    await expect(governance.getByTestId(`dataset-case-tag-stats-${dataset.id}`)).not.toContainText("领导演示 1");
    await governance.getByLabel("筛选数据集用例关键词").fill("");
    await governance.getByRole("button", { name: "全部" }).click();
    await governance.getByLabel("筛选数据集用例标签").selectOption("全部");
    await datasetRow.getByTestId(`load-dataset-versions-${dataset.id}`).click();
    await expect(datasetRow.getByTestId(`dataset-version-controls-${dataset.id}`)).toContainText("最新 v2", { timeout: 15000 });
    await expect(datasetRow.getByTestId(`dataset-version-chain-${dataset.id}`)).toContainText("版本链回放");
    await expect(datasetRow.getByTestId(`dataset-version-chain-${dataset.id}`)).toContainText("已加载最近 2 个快照");
    const versionRowsResponse = page.waitForResponse(
      (response) => response.request().method() === "GET" && response.url().includes(`/api/prompt-evaluation-assets/${dataset.id}/dataset-versions/${v2.id}/rows`),
      { timeout: 15000 },
    );
    await datasetRow.getByTestId(`show-dataset-version-rows-${dataset.id}-2`).click();
    expect((await versionRowsResponse).status()).toBe(200);
    await expect(datasetRow.getByTestId(`dataset-version-rows-${dataset.id}`)).toContainText("行级快照 v2", { timeout: 15000 });
    await expect(datasetRow.getByTestId(`dataset-version-rows-${dataset.id}`)).toContainText("已加载 2 / 2 行");
    await expect(datasetRow.getByTestId(`dataset-version-rows-${dataset.id}`)).toContainText("版本一登录用例");
    await expect(datasetRow.getByTestId(`dataset-version-rows-${dataset.id}`)).toContainText("版本二新增用例");
    await datasetRow.getByTestId(`diff-dataset-version-${dataset.id}`).click();
    await expect(datasetRow.getByTestId(`dataset-version-diff-${dataset.id}`)).toContainText("新增 1", { timeout: 15000 });

    await datasetRow.getByTestId(`restore-dataset-version-${dataset.id}-1`).click();
    await expect
      .poll(async () => (await api.listPromptEvaluationDatasetVersions(dataset.id))[0]?.version ?? 0, { timeout: 15000 })
      .toBe(3);
    await expect(datasetRow.getByTestId(`dataset-version-controls-${dataset.id}`)).toContainText("最新 v3", { timeout: 15000 });
    const latest = (await api.listPromptEvaluationDatasetVersions(dataset.id))[0]!;
    const restoredRows = await api.listPromptEvaluationDatasetVersionRows(dataset.id, latest.id);
    expect(restoredRows).toHaveLength(1);
    expect(restoredRows[0]).toMatchObject({
      row_index: 0,
      row_name: "版本一登录用例",
    });
    expect(restoredRows.map((row) => row.row_name)).not.toContain("版本二新增用例");
  });

  test("实验运行会绑定资产声明的明确数据集版本", async ({ page }) => {
    test.setTimeout(120_000);
    const prompt = await api.createPromptForE2E(artifactPrefix, {
      name: `${artifactPrefix} 实验版本绑定提示词`,
      content: "请分析 {{issue_title}}，输出目标和验收条件。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
    });
    const promptV2 = await api.updatePromptLibraryItem(prompt.id, {
      content: "请分析 {{issue_title}}，输出目标、验收条件和中文风险说明。",
      tags: ["E2E", "实验", "提示词版本对比"],
      status: "启用",
    });
    expect(promptV2.version).toBe(2);
    const dataset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} 实验基准数据集`,
      description: "E2E 验证实验资产绑定明确数据集版本",
      asset_type: "数据集",
      payload: {
        cases: [
          {
            名称: "实验基准样本",
            变量: { issue_title: "user-center 登录失败" },
            期望包含: ["目标", "验收条件"],
            标签: ["实验基准"],
          },
        ],
      },
      status: "启用",
    });
    const datasetVersion = await api.createPromptEvaluationDatasetVersion(dataset.id, { version_label: "实验基准 v1" });
    const experiment = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} 明确版本实验`,
      description: "E2E 验证实验运行 evidence 不再偷偷读取最新版本",
      asset_type: "实验",
      payload: {
        schema: "multica.training_evaluation.payload.v1",
        schema_version: 1,
        语义版本: "multica.training_evaluation.v1",
        实验对象: prompt.name,
        对比维度: ["命中率", "缺失变量", "中文一致性"],
        linked_dataset_ids: [dataset.id],
        linked_dataset_versions: [
          {
            dataset_id: dataset.id,
            dataset_name: dataset.name,
            dataset_version_id: datasetVersion.id,
            version: datasetVersion.version,
            row_fingerprint: datasetVersion.row_fingerprint,
          },
        ],
        cases: [
          {
            case_name: "实验版本绑定样本",
            variables: { issue_title: "user-center 登录失败" },
            expected_contains: ["目标", "验收条件"],
            tags: ["实验", "数据集版本"],
          },
        ],
      },
      status: "启用",
    });

    await api.runPromptEvaluationAsset(experiment.id);
    await expect
      .poll(async () => (await api.listPromptEvaluationRuns({ asset_id: experiment.id, limit: 5 }))[0]?.id ?? "", { timeout: 15000 })
      .not.toBe("");
    const run = (await api.listPromptEvaluationRuns({ asset_id: experiment.id, limit: 5 }))[0]!;
    expect(run.metrics).toMatchObject({ 提示词版本: 2 });
    const metricDimensionScores = Array.isArray(run.metrics["实验维度评分"]) ? run.metrics["实验维度评分"] as Array<Record<string, unknown>> : [];
    expect(metricDimensionScores).toEqual(expect.arrayContaining([
      expect.objectContaining({
        维度名称: "命中率",
        状态: "已评分",
        评分规则: "逐用例检查期望内容全部命中",
      }),
      expect.objectContaining({
        维度名称: "缺失变量",
        状态: "已评分",
      }),
      expect.objectContaining({
        维度名称: "中文一致性",
        状态: "已评分",
      }),
    ]));
    const factDimensionScores = await api.listPromptEvaluationDimensionScores({ run_id: run.id });
    expect(factDimensionScores.total).toBe(3);
    expect(factDimensionScores.items).toEqual(expect.arrayContaining([
      expect.objectContaining({
        asset_id: experiment.id,
        run_id: run.id,
        dimension_name: "命中率",
        status: "已评分",
        source: "local_run",
        rule: "逐用例检查期望内容全部命中",
      }),
      expect.objectContaining({
        asset_id: experiment.id,
        run_id: run.id,
        dimension_name: "缺失变量",
        status: "已评分",
        source: "local_run",
      }),
      expect.objectContaining({
        asset_id: experiment.id,
        run_id: run.id,
        dimension_name: "中文一致性",
        status: "已评分",
        source: "local_run",
      }),
    ]));
    const factDimensionScoreSummaries = await api.listPromptEvaluationDimensionScoreSummaries({ asset_id: experiment.id });
    expect(factDimensionScoreSummaries.total).toBe(3);
    expect(factDimensionScoreSummaries.items).toEqual(expect.arrayContaining([
      expect.objectContaining({
        asset_id: experiment.id,
        dimension_name: "命中率",
        run_count: 1,
        scored_run_count: 1,
        passed_cases: 1,
        total_cases: 1,
        score: 1,
        latest_status: "已评分",
        latest_source: "local_run",
      }),
    ]));
    const factDimensionScoreTrends = await api.listPromptEvaluationDimensionScoreTrends({ asset_id: experiment.id });
    expect(factDimensionScoreTrends.total).toBe(3);
    expect(factDimensionScoreTrends.items).toEqual(expect.arrayContaining([
      expect.objectContaining({
        asset_id: experiment.id,
        dimension_name: "命中率",
        prompt_version: 2,
        run_count: 1,
        scored_run_count: 1,
        passed_cases: 1,
        total_cases: 1,
        score: 1,
        latest_source: "local_run",
      }),
    ]));
    expect(factDimensionScoreTrends.items[0]?.period).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    const evidence = await api.getPromptEvaluationRunEvidence(run.id);
    expect(evidence.evidence).toMatchObject({ 提示词版本: 2 });
    expect(evidence.evidence["实验维度评分"]).toEqual(expect.arrayContaining([
      expect.objectContaining({ 维度名称: "命中率", 状态: "已评分" }),
    ]));
    const versions = Array.isArray(evidence.evidence["数据集版本"]) ? evidence.evidence["数据集版本"] as Array<Record<string, unknown>> : [];
    expect(versions).toEqual(expect.arrayContaining([
      expect.objectContaining({
        dataset_asset_id: dataset.id,
        dataset_version_id: datasetVersion.id,
        version: datasetVersion.version,
        绑定方式: "资产声明的明确数据集版本",
      }),
    ]));

    const dimensionScoreSummariesResponsePromise = page.waitForResponse(
      (response) =>
        response.request().method() === "GET" &&
        response.url().includes("/api/prompt-evaluation-dimension-score-summaries") &&
        response.status() === 200,
    );
    const dimensionScoreTrendsResponsePromise = page.waitForResponse(
      (response) =>
        response.request().method() === "GET" &&
        response.url().includes("/api/prompt-evaluation-dimension-score-trends") &&
        response.status() === 200,
    );
    await page.goto(`/${workspaceSlug}/training/experiments`, { waitUntil: "domcontentloaded" });
    const dimensionScoreSummariesResponse = await dimensionScoreSummariesResponsePromise;
    const dimensionScoreSummariesPayload = await dimensionScoreSummariesResponse.json() as { items?: Array<Record<string, unknown>>; total?: number };
    expect(dimensionScoreSummariesPayload.items ?? []).toEqual(expect.arrayContaining([
      expect.objectContaining({
        asset_id: experiment.id,
        dimension_name: "命中率",
        latest_status: "已评分",
        run_count: 1,
      }),
    ]));
    const dimensionScoreTrendsResponse = await dimensionScoreTrendsResponsePromise;
    const dimensionScoreTrendsPayload = await dimensionScoreTrendsResponse.json() as { items?: Array<Record<string, unknown>>; total?: number };
    expect(dimensionScoreTrendsPayload.items ?? []).toEqual(expect.arrayContaining([
      expect.objectContaining({
        asset_id: experiment.id,
        dimension_name: "命中率",
        prompt_version: 2,
        run_count: 1,
      }),
    ]));
    await page.getByRole("button", { name: "显示验收数据" }).first().click();
    await expect(page.getByTestId("experiment-comparison-panel")).toContainText("实验对比排行", { timeout: 15000 });
    const comparisonRow = page.getByTestId(`experiment-comparison-row-${experiment.id}`);
    await expect(comparisonRow).toContainText(experiment.name, { timeout: 15000 });
    await expect(comparisonRow).toContainText("通过率");
    await expect(comparisonRow).toContainText("预估成本");
    await expect(comparisonRow).toContainText(`v${datasetVersion.version}`);
    const qualityExplanation = comparisonRow.getByTestId(`experiment-quality-explanation-${experiment.id}`);
    await expect(qualityExplanation).toContainText("成本质量解释");
    await expect(qualityExplanation).toContainText("单位通过成本");
    await expect(qualityExplanation).toContainText("成本判断");
    await expect(qualityExplanation).toContainText("失败主因");
    await expect(qualityExplanation).toContainText("建议动作");
    const dimensionScoreComparison = comparisonRow.getByTestId(`experiment-dimension-score-comparison-${experiment.id}`);
    await expect(dimensionScoreComparison).toContainText("实验维度评分");
    await expect(dimensionScoreComparison).toContainText("命中率");
    await expect(dimensionScoreComparison).toContainText("缺失变量");
    await expect(dimensionScoreComparison).toContainText("中文一致性");
    await expect(dimensionScoreComparison).toContainText("逐用例检查期望内容全部命中");
    const dimensionScoreTrend = comparisonRow.getByTestId(`experiment-dimension-score-trend-${experiment.id}`);
    await expect(dimensionScoreTrend).toContainText("维度趋势");
    await expect(dimensionScoreTrend).toContainText("v2");
    await expect(dimensionScoreTrend).toContainText("命中率");
    const promptVersionComparison = comparisonRow.getByTestId(`experiment-prompt-version-comparison-${experiment.id}`);
    await expect(promptVersionComparison).toContainText("提示词版本对比");
    await expect(promptVersionComparison).toContainText("v2");
    await expect(promptVersionComparison).toContainText("v1");
    await expect(promptVersionComparison).toContainText("版本运行");
    await expect(promptVersionComparison).toContainText("手动更新");
    const experimentRow = page.getByTestId(`prompt-evaluation-asset-${experiment.id}`);
    await expect(experimentRow).toBeVisible({ timeout: 15000 });
    await expect(experimentRow.getByTestId(`linked-dataset-version-summary-${experiment.id}`)).toContainText("绑定数据集版本");
    await expect(experimentRow.getByTestId(`linked-dataset-version-summary-${experiment.id}`)).toContainText(`v${datasetVersion.version}`);
  });

  test("智能体调试场公开 API 可以指定执行智能体", async () => {
    test.setTimeout(90_000);
    const runtime = await api.ensureOnlineCodexRuntime(`${artifactPrefix} 指定执行智能体 Runtime`);
    const agent = await api.createAgent({
      name: `${artifactPrefix} 指定执行智能体`,
      runtime_id: runtime.id,
      model: "gpt-5.4-mini",
      visibility: "workspace",
      max_concurrent_tasks: 1,
      instructions: "你是 E2E 指定执行智能体，只输出中文结论和可验收证据。",
    });
    const prompt = await api.createPromptForE2E(artifactPrefix, {
      name: `${artifactPrefix} 指定执行智能体提示词`,
      content: "请评估 {{issue_title}}，输出中文结论和验收证据。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
    });
    const asset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} 指定执行智能体实验`,
      description: "E2E 通过公开 API 指定智能体调试场执行者",
      asset_type: "实验",
      payload: {
        执行智能体: {
          agent_id: agent.id,
          名称: agent.name,
        },
        调试包: {
          执行智能体: {
            agent_id: agent.id,
            名称: agent.name,
          },
        },
        cases: [
          {
            名称: "指定执行智能体用例",
            变量: { issue_title: "指定执行智能体" },
            期望包含: ["中文结论", "验收证据"],
          },
        ],
      },
      status: "启用",
    });

    const agentRun = await api.runPromptEvaluationAssetAgent(asset.id);
    expect(agentRun.agent_id).toBe(agent.id);
    expect(agentRun.runtime_id).toBe(runtime.id);
    expect(agentRun.model).toBe("gpt-5.4-mini");
    expect(agentRun.run).toMatchObject({
      asset_id: asset.id,
      agent_id: agent.id,
      runtime_id: runtime.id,
      model: "gpt-5.4-mini",
      run_kind: "Agent执行",
      status: "已入队",
    });
    expect(agentRun.asset.payload).toMatchObject({
      最近Agent运行: {
        agent_id: agent.id,
        runtime_id: runtime.id,
        模型: "gpt-5.4-mini",
        状态: "已入队",
      },
    });
  });

  test("运行历史可以取消已入队的真实智能体运行", async ({ page }) => {
    test.setTimeout(90_000);
    await api.ensureOnlineCodexRuntime(`${artifactPrefix} 取消运行 Runtime`);
    const prompt = await api.createPromptForE2E(artifactPrefix, {
      name: `${artifactPrefix} 取消运行提示词`,
      content: "请处理 {{issue_title}}，输出中文结论和验收证据。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
    });
    const asset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} 取消运行实验`,
      description: "E2E 公开 UI 取消训练评估运行",
      asset_type: "实验",
      payload: {
        cases: [
          {
            名称: "取消运行用例",
            变量: { issue_title: "取消运行" },
            期望包含: ["中文结论", "验收证据"],
          },
        ],
      },
      status: "启用",
    });
    const agentRun = await api.runPromptEvaluationAssetAgent(asset.id);

    await page.goto(`/${workspaceSlug}/training/run-history`, { waitUntil: "domcontentloaded" });
    const runRow = page.getByTestId(`prompt-evaluation-run-${agentRun.run.id}`);
    await expect(runRow).toContainText("智能体执行 · 已入队", { timeout: 10000 });
    await expect(runRow).toContainText(`任务 ${agentRun.task_id}`);
    const cancelResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes(`/api/prompt-evaluation-runs/${agentRun.run.id}/cancel`),
      { timeout: 10000 },
    );
    await runRow.getByRole("button", { name: "取消运行" }).click();
    expect((await cancelResponse).status()).toBe(200);
    await expect(page.getByText("训练评估运行已取消")).toBeVisible({ timeout: 10000 });
    await expect(runRow).toContainText("智能体执行 · 已取消", { timeout: 10000 });
    await expect(runRow.getByRole("button", { name: "取消运行" })).toHaveCount(0);
    await expect
      .poll(async () => {
        const runs = await api.listPromptEvaluationRuns({ asset_id: asset.id });
        const cancelled = runs.find((run) => run.id === agentRun.run.id);
        return cancelled ? { status: cancelled.status, task_id: cancelled.task_id } : null;
      }, { timeout: 15000 })
      .toEqual({ status: "已取消", task_id: agentRun.task_id });
    const evidence = await api.getPromptEvaluationRunEvidence(agentRun.run.id);
    expect(evidence.run.status).toBe("已取消");
    expect(evidence.trials[0]?.status).toBe("已跳过");
  });

  test("优化运行作业台汇总资产运行候选并支持取消", async ({ page }) => {
    test.setTimeout(90_000);
    await api.ensureOnlineCodexRuntime(`${artifactPrefix} 优化作业台 Runtime`);
    const prompt = await api.createPromptForE2E(artifactPrefix, {
      name: `${artifactPrefix} 优化作业台提示词`,
      content: "请优化 {{issue_title}}，输出中文候选、依据和验收条件。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
    });
    const asset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} 优化作业台资产`,
      description: "E2E 优化运行作业台资产",
      asset_type: "优化运行",
      payload: {
        优化目标: "提升失败用例的中文验收质量",
        cases: [
          {
            名称: "优化作业台用例",
            变量: { issue_title: "优化运行作业台" },
            期望包含: ["候选", "验收条件"],
          },
        ],
      },
      status: "启用",
    });
    const agentRun = await api.runPromptEvaluationAssetAgent(asset.id);

    await page.goto(`/${workspaceSlug}/training/optimization-runs`, { waitUntil: "domcontentloaded" });
    const studio = page.getByTestId("optimization-studio-panel");
    await expect(studio).toContainText("优化运行作业台", { timeout: 10000 });
    await expect(studio).toContainText("活动 1");
    const job = page.getByTestId(`optimization-studio-job-${asset.id}`);
    await expect(job).toContainText(`${artifactPrefix} 优化作业台资产`);
    await expect(job).toContainText("配置摘要");
    await expect(job.getByTestId(`optimization-studio-rounds-${asset.id}`)).toContainText("优化轮次");
    await expect(job.getByTestId(`optimization-studio-rounds-${asset.id}`)).toContainText("multica.training_evaluation.optimization_run.v2");
    await expect(job.getByTestId(`optimization-studio-rounds-${asset.id}`)).toContainText("轮次 1");
    await expect(job.getByTestId(`optimization-studio-rounds-${asset.id}`)).toContainText("重试 0");
    await expect(job.getByTestId(`optimization-studio-log-stream-${asset.id}`)).toContainText("日志流");
    await expect(job.getByTestId(`optimization-studio-log-stream-${asset.id}`)).toContainText("创建优化运行");
    const run = page.getByTestId(`optimization-studio-run-${agentRun.run.id}`);
    await expect(run).toContainText("智能体执行 · 已入队");
    const retryResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes(`/api/prompt-evaluation-assets/${asset.id}/agent-run`),
      { timeout: 10000 },
    );
    await job.getByTestId(`retry-optimization-run-${asset.id}`).click();
    expect((await retryResponse).status()).toBe(202);
    await expect(page.getByText(/优化运行重试已入队/)).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const assets = await api.listPromptEvaluationAssets({ prompt_id: prompt.id, asset_type: "优化运行" });
        const reloaded = assets.find((item) => item.id === asset.id);
        const payload = reloaded?.payload as Record<string, any> | undefined;
        return {
          roundCount: Array.isArray(payload?.优化轮次) ? payload!.优化轮次.length : 0,
          latestRound: Array.isArray(payload?.优化轮次) ? payload!.优化轮次[0] : null,
          logCount: Array.isArray(payload?.日志流) ? payload!.日志流.length : 0,
          retryCount: payload?.重试策略?.当前重试次数 ?? -1,
        };
      }, { timeout: 15000 })
      .toMatchObject({
        roundCount: 2,
        latestRound: {
          轮次: 2,
          重试序号: 1,
          状态: "已入队",
        },
        logCount: 2,
        retryCount: 1,
      });
    await expect(job.getByTestId(`optimization-studio-rounds-${asset.id}`)).toContainText("轮次 2", { timeout: 10000 });
    await expect(job.getByTestId(`optimization-studio-log-stream-${asset.id}`)).toContainText("重试优化运行", { timeout: 10000 });
    await run.getByRole("button", { name: "查看证据" }).click();
    const evidencePanel = run.getByTestId(`run-evidence-${agentRun.run.id}`);
    await expect(evidencePanel).toContainText(`任务 ${agentRun.task_id}`, { timeout: 10000 });
    await expect(evidencePanel).toContainText("证据快照");
    const cancelResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes(`/api/prompt-evaluation-runs/${agentRun.run.id}/cancel`),
      { timeout: 10000 },
    );
    await run.getByRole("button", { name: "取消运行" }).click();
    expect((await cancelResponse).status()).toBe(200);
    await expect(page.getByText("训练评估运行已取消")).toBeVisible({ timeout: 10000 });
    await expect(run).toContainText("智能体执行 · 已取消", { timeout: 10000 });
    await expect(studio).toContainText("活动 1", { timeout: 10000 });
  });

  test("运行看板可以进入人工复核队列", async ({ page }) => {
    test.setTimeout(90_000);
    await api.ensureOnlineCodexRuntime(`${artifactPrefix} 人工复核 Runtime`);
    const prompt = await api.createPromptForE2E(artifactPrefix, {
      name: `${artifactPrefix} 人工复核提示词`,
      content: "请评估 {{issue_title}}，必须返回结构化 JSON。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
    });
    const asset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} 人工复核实验`,
      description: "E2E 人工复核队列入口",
      asset_type: "实验",
      payload: {
        cases: [
          {
            名称: "人工复核用例",
            变量: { issue_title: "缺少结构化输出" },
            期望包含: ["结构化 JSON"],
          },
        ],
      },
      status: "启用",
    });
    const agentRun = await api.runPromptEvaluationAssetAgent(asset.id);
    const claimed = await api.claimDaemonTask(agentRun.runtime_id);
    expect(claimed.task?.id).toBe(agentRun.task_id);
    await api.startDaemonTask(agentRun.task_id);
    await api.reportDaemonTaskMessages(agentRun.task_id, [
      {
        seq: 1,
        type: "text",
        content: "Agent 输出：只给了自然语言结论，没有返回结构化 JSON，需要人工复核。",
      },
      {
        seq: 2,
        type: "tool_use",
        tool: "playwright-inspect",
        input: { tool_call_id: "manual-review-tool-1", url: "/training/runs" },
      },
      {
        seq: 3,
        type: "tool_result",
        tool: "playwright-inspect",
        output: "页面可打开，但输出没有结构化 JSON。",
      },
      {
        seq: 4,
        type: "tool_use",
        tool: "curl-check",
        input: { tool_call_id: "manual-review-tool-2", url: "/api/prompt-evaluation-runs" },
      },
      {
        seq: 5,
        type: "tool_result",
        tool: "curl-check",
        output: "Error: HTTP 500 from upstream",
      },
    ]);
    await api.completeDaemonTask(agentRun.task_id, "Agent 输出：只给了自然语言结论，没有返回结构化 JSON，需要人工复核。");
    await expect
      .poll(async () => {
        const runs = await api.listPromptEvaluationRuns({ asset_id: asset.id });
        return runs.find((run) => run.id === agentRun.run.id)?.status ?? null;
      }, { timeout: 15000 })
      .toBe("需人工复核");

    const evidence = await api.getPromptEvaluationRunEvidence(agentRun.run.id);
    const playwrightToolChain = evidence.tool_call_chains.find((chain) => chain.tool === "playwright-inspect");
    expect(playwrightToolChain?.id).toBeTruthy();
    expect(evidence.trials[0]?.id).toBeTruthy();
    expect(evidence.execution_spans[0]?.seq).toBeDefined();
    const firstTraceEventId = evidence.trace_events[0]?.id;
    expect(firstTraceEventId).toBeTruthy();

    await page.goto(`/${workspaceSlug}/training/run-history?run=${agentRun.run.id}&trace=1`, { waitUntil: "domcontentloaded" });
    const traceDeepLinkRun = page.getByTestId(`prompt-evaluation-run-${agentRun.run.id}`);
    const traceDeepLinkEvidence = traceDeepLinkRun.getByTestId(`run-evidence-${agentRun.run.id}`);
    await expect(traceDeepLinkEvidence.getByTestId("run-evidence-anchor-summary")).toContainText("当前定位 trace=1", { timeout: 10000 });
    await expect(traceDeepLinkEvidence.getByTestId("run-evidence-trace-node-1")).toHaveClass(/ring-2/);

    await page.goto(`/${workspaceSlug}/training/run-history?run=${agentRun.run.id}&trace=${firstTraceEventId}`, { waitUntil: "domcontentloaded" });
    const traceIdDeepLinkRun = page.getByTestId(`prompt-evaluation-run-${agentRun.run.id}`);
    const traceIdDeepLinkEvidence = traceIdDeepLinkRun.getByTestId(`run-evidence-${agentRun.run.id}`);
    await expect(traceIdDeepLinkEvidence.getByTestId("run-evidence-anchor-summary")).toContainText(`当前定位 trace=${firstTraceEventId}`, { timeout: 10000 });
    await expect(traceIdDeepLinkEvidence.getByTestId("run-evidence-trace-node-1")).toHaveClass(/ring-2/);

    await page.goto(`/${workspaceSlug}/training/run-history?run=${agentRun.run.id}&tool=${playwrightToolChain!.id}`, { waitUntil: "domcontentloaded" });
    const toolDeepLinkRun = page.getByTestId(`prompt-evaluation-run-${agentRun.run.id}`);
    const toolDeepLinkEvidence = toolDeepLinkRun.getByTestId(`run-evidence-${agentRun.run.id}`);
    await expect(toolDeepLinkEvidence.getByTestId("run-evidence-anchor-summary")).toContainText(`当前定位 tool=${playwrightToolChain!.id}`, { timeout: 10000 });
    await expect(toolDeepLinkEvidence.getByTestId(`run-evidence-tool-call-chain-${playwrightToolChain!.id}`)).toHaveClass(/ring-2/);

    await page.goto(`/${workspaceSlug}/training/run-history?run=${agentRun.run.id}&trial=${evidence.trials[0]!.id}`, { waitUntil: "domcontentloaded" });
    const trialDeepLinkRun = page.getByTestId(`prompt-evaluation-run-${agentRun.run.id}`);
    const trialDeepLinkEvidence = trialDeepLinkRun.getByTestId(`run-evidence-${agentRun.run.id}`);
    await expect(trialDeepLinkEvidence.getByTestId("run-evidence-anchor-summary")).toContainText(`当前定位 trial=${evidence.trials[0]!.id}`, { timeout: 10000 });
    await expect(trialDeepLinkEvidence.getByTestId(`run-evidence-trial-${evidence.trials[0]!.id}`)).toHaveClass(/ring-2/);

    await page.goto(`/${workspaceSlug}/training/run-history?run=${agentRun.run.id}&assertion=${evidence.trials[0]!.id}:1`, { waitUntil: "domcontentloaded" });
    const assertionDeepLinkRun = page.getByTestId(`prompt-evaluation-run-${agentRun.run.id}`);
    const assertionDeepLinkEvidence = assertionDeepLinkRun.getByTestId(`run-evidence-${agentRun.run.id}`);
    await expect(assertionDeepLinkEvidence.getByTestId("run-evidence-anchor-summary")).toContainText(`当前定位 assertion=${evidence.trials[0]!.id}:1`, { timeout: 10000 });
    await expect(assertionDeepLinkEvidence.getByTestId(`run-evidence-assertion-${evidence.trials[0]!.id}-1`)).toHaveClass(/ring-2/);

    await page.goto(`/${workspaceSlug}/training/run-history?run=${agentRun.run.id}&message=2`, { waitUntil: "domcontentloaded" });
    const messageDeepLinkRun = page.getByTestId(`prompt-evaluation-run-${agentRun.run.id}`);
    const messageDeepLinkEvidence = messageDeepLinkRun.getByTestId(`run-evidence-${agentRun.run.id}`);
    await expect(messageDeepLinkEvidence.getByTestId("run-evidence-anchor-summary")).toContainText("当前定位 message=2", { timeout: 10000 });
    await expect(messageDeepLinkEvidence.locator('[data-evidence-anchor="message:2"]')).toHaveClass(/ring-2/);

    const spanSeq = evidence.execution_spans[0]!.seq;
    await page.goto(`/${workspaceSlug}/training/run-history?run=${agentRun.run.id}&span=${spanSeq}`, { waitUntil: "domcontentloaded" });
    const spanDeepLinkRun = page.getByTestId(`prompt-evaluation-run-${agentRun.run.id}`);
    const spanDeepLinkEvidence = spanDeepLinkRun.getByTestId(`run-evidence-${agentRun.run.id}`);
    await expect(spanDeepLinkEvidence.getByTestId("run-evidence-anchor-summary")).toContainText(`当前定位 span=${spanSeq}`, { timeout: 10000 });
    await expect(spanDeepLinkEvidence.getByTestId(`run-evidence-execution-span-${spanSeq}`)).toHaveClass(/ring-2/);

    await page.goto(`/${workspaceSlug}/training/run-history?run=${agentRun.run.id}&failure=tool`, { waitUntil: "domcontentloaded" });
    const failureDeepLinkRun = page.getByTestId(`prompt-evaluation-run-${agentRun.run.id}`);
    const failureDeepLinkEvidence = failureDeepLinkRun.getByTestId(`run-evidence-${agentRun.run.id}`);
    await expect(failureDeepLinkEvidence.getByTestId("run-evidence-anchor-summary")).toContainText("当前定位 failure=tool", { timeout: 10000 });
    await expect(failureDeepLinkEvidence.getByTestId("run-evidence-failure-review")).toContainText("失败复盘入口");
    await expect(failureDeepLinkEvidence.getByTestId("run-evidence-failure-tool").first()).toHaveClass(/ring-2/);
    await expect(failureDeepLinkEvidence.getByTestId("run-evidence-failure-review-actions")).toContainText("生成优化候选");
    const reportDownloadPromise = page.waitForEvent("download");
    await failureDeepLinkEvidence.getByTestId("run-evidence-failure-download-report").click();
    const reportDownload = await reportDownloadPromise;
    expect(reportDownload.suggestedFilename()).toMatch(/^multica-failure-review-.*\.md$/);
    const reportPath = await reportDownload.path();
    expect(reportPath).toBeTruthy();
    const reportMarkdown = await readFile(reportPath!, "utf8");
    expect(reportMarkdown).toContain("# Multica 失败复盘报告");
    expect(reportMarkdown).toContain(`运行 ID：${agentRun.run.id}`);
    expect(reportMarkdown).toContain(`trace=${firstTraceEventId}`);
    expect(reportMarkdown).toContain("工具异常");
    expect(reportMarkdown).toContain("生成优化候选");
    const candidateFromFailurePanelResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes(`/api/prompt-evaluation-runs/${agentRun.run.id}/optimization-candidate`),
      { timeout: 10000 },
    );
    await failureDeepLinkEvidence.getByTestId("run-evidence-failure-generate-candidate").click();
    expect((await candidateFromFailurePanelResponse).status()).toBe(201);
    await expect(page.getByText("优化候选已生成，等待人工确认")).toBeVisible({ timeout: 10000 });

    const optimizationAgentFromFailurePanelResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes(`/api/prompt-evaluation-runs/${agentRun.run.id}/optimization-agent-run`),
      { timeout: 10000 },
    );
    await failureDeepLinkEvidence.getByTestId("run-evidence-failure-run-optimization-agent").click();
    const optimizationAgentResponse = await optimizationAgentFromFailurePanelResponse;
    expect(optimizationAgentResponse.status()).toBe(202);
    const optimizationAgentPayload = (await optimizationAgentResponse.json()) as {
      run: {
        id: string;
        asset_id: string;
        task_id: string;
        runtime_id: string;
        run_kind: string;
        status: string;
      };
      task_id: string;
      runtime_id: string;
      model: string;
    };
    await expect(page.getByText(/真实智能体优化任务已入队/)).toBeVisible({ timeout: 10000 });
    expect(optimizationAgentPayload.run).toMatchObject({
      run_kind: "Agent执行",
      status: "已入队",
      task_id: optimizationAgentPayload.task_id,
      runtime_id: optimizationAgentPayload.runtime_id,
    });
    expect(optimizationAgentPayload.model).toBe(expectedAgentModel);

    const optimizationClaim = await api.claimDaemonTask(optimizationAgentPayload.runtime_id);
    expect(optimizationClaim.task?.id).toBe(optimizationAgentPayload.task_id);
    const optimizationOutput = [
      "智能体优化输出：已基于失败复盘入口的工具异常线索生成候选。",
      "```json",
      JSON.stringify({
        用例结果: [
          {
            case_index: 0,
            status: "通过",
            output: "已生成优化候选提示词正文。",
            failure_reason: "无",
            evidence: {
              命中: ["优化候选", "验收条件", "trace/task id"],
              trace_task_id: optimizationAgentPayload.task_id,
            },
          },
        ],
        评估结论: "智能体优化任务已完成，候选需要人工确认后发布",
        优化候选名称: "失败复盘入口智能体优化候选",
        候选提示词正文:
          "请评估 {{issue_title}}，必须返回结构化 JSON；若工具异常，必须输出失败原因、验收条件、trace/task id 和下一步人工确认点。",
        逐条修改依据: "把工具异常、结构化 JSON、验收条件和 trace/task id 固定为输出约束，方便运行证据复盘。",
        可能影响的通过用例: "需要回归人工复核用例，确认没有降低中文输出质量。",
        人工验收清单: ["确认中文输出", "确认包含 trace/task id", "确认原提示词未被自动替换"],
      }),
      "```",
    ].join("\n");
    await api.startDaemonTask(optimizationAgentPayload.task_id);
    await api.reportDaemonTaskUsage(optimizationAgentPayload.task_id, {
      provider: "codex",
      model: optimizationAgentPayload.model,
      input_tokens: 34,
      output_tokens: 21,
      cache_read_tokens: 5,
      cache_write_tokens: 3,
    });
    await api.reportDaemonTaskMessages(optimizationAgentPayload.task_id, [
      {
        seq: 1,
        type: "text",
        content: optimizationOutput,
      },
      {
        seq: 2,
        type: "tool_use",
        tool: "failure-review-optimizer",
        input: { tool_call_id: "failure-review-optimizer-1", run_id: agentRun.run.id },
      },
      {
        seq: 3,
        type: "tool_result",
        tool: "failure-review-optimizer",
        output: "已生成智能体优化候选草案，等待人工确认。",
      },
    ]);
    await api.completeDaemonTask(optimizationAgentPayload.task_id, optimizationOutput);
    const syncedOptimizationRun = await api.syncPromptEvaluationRun(optimizationAgentPayload.run.id);
    expect(syncedOptimizationRun).toMatchObject({
      id: optimizationAgentPayload.run.id,
      run_kind: "Agent执行",
      status: "通过",
      model: optimizationAgentPayload.model,
      runtime_provider: "codex",
      task_id: optimizationAgentPayload.task_id,
    });
    const optimizationEvidence = await api.getPromptEvaluationRunEvidence(optimizationAgentPayload.run.id);
    expect(optimizationEvidence.task_messages).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          task_id: optimizationAgentPayload.task_id,
          tool: "failure-review-optimizer",
        }),
      ]),
    );
    await expect
      .poll(async () => {
        const candidates = await api.listPromptEvaluationOptimizationCandidates({ run_id: agentRun.run.id });
        return candidates.filter((candidate) => candidate.status === "待确认").length;
      }, { timeout: 10000 })
      .toBeGreaterThanOrEqual(2);

    await page.goto(`/${workspaceSlug}/training/runs`, { waitUntil: "domcontentloaded" });
    await expect(page.getByTestId("training-summary-需人工复核")).toContainText(/\d+/, { timeout: 10000 });
    const queueResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "GET" &&
        response.url().includes("/api/prompt-evaluation-runs") &&
        decodeURIComponent(response.url()).includes("status=需人工复核"),
      { timeout: 10000 },
    );
    await page.getByTestId("training-summary-需人工复核").click();
    expect((await queueResponse).status()).toBe(200);
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/run-history$`), { timeout: 30000 });
    await expect(page.getByTestId("run-status-filter-bar")).toContainText("人工复核队列");
    const runRow = page.getByTestId(`prompt-evaluation-run-${agentRun.run.id}`);
    await expect(runRow).toContainText("智能体执行 · 需人工复核", { timeout: 10000 });
    await runRow.getByRole("button", { name: "查看证据" }).click();
    const evidencePanel = runRow.getByTestId(`run-evidence-${agentRun.run.id}`);
    await expect(evidencePanel).toContainText("需要人工复核", { timeout: 10000 });
    const toolChains = evidencePanel.getByTestId("run-evidence-tool-call-chains");
    await expect(toolChains).toContainText("工具调用链", { timeout: 10000 });
    await expect(toolChains).toContainText("playwright-inspect");
    await expect(toolChains).toContainText("已配对");
    await expect(toolChains).toContainText("已返回");
    const toolSummary = evidencePanel.getByTestId("run-evidence-tool-call-summary");
    await expect(toolSummary).toContainText("工具调用摘要");
    await expect(toolSummary).toContainText("playwright-inspect");
    await expect(toolSummary).toContainText("结果正常");
    await expect(toolSummary).toContainText("平均耗时");
    await expect(toolSummary).toContainText("结果分类");
    await expect(toolSummary).toContainText("curl-check");
    await expect(toolSummary).toContainText("需要关注");
    await expect(toolSummary).toContainText("异常线索 1");
    await expect(toolChains.getByTestId("run-evidence-tool-call-chain-filters")).toBeVisible();
    await toolChains.getByLabel("搜索工具调用链").fill("playwright-inspect");
    await expect(toolChains).toContainText("1/2 条");
    await toolChains.getByLabel("筛选工具调用链状态").selectOption("已配对");
    await expect(toolChains).toContainText("耗时");
    await toolChains.getByLabel("搜索工具调用链").fill("curl-check");
    await expect(toolChains).toContainText("异常原因");
    await expect(toolChains).toContainText("工具结果包含 HTTP 状态码 500");

    page.once("dialog", async (dialog) => {
      expect(dialog.message()).toContain("通过说明");
      await dialog.accept("人工复核确认该输出可作为通过样例");
    });
    const reviewResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes(`/api/prompt-evaluation-runs/${agentRun.run.id}/review`),
      { timeout: 10000 },
    );
    await runRow.getByRole("button", { name: "人工通过" }).click();
    expect((await reviewResponse).status()).toBe(200);
    await expect(page.getByText("人工复核已处理：通过")).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const runs = await api.listPromptEvaluationRuns({ asset_id: asset.id });
        const reviewed = runs.find((run) => run.id === agentRun.run.id);
        return `${reviewed?.status ?? ""}|${reviewed?.review_decision ?? ""}|${reviewed?.review_note ?? ""}`;
      }, { timeout: 15000 })
      .toContain("通过|通过|人工复核确认该输出可作为通过样例");
  });

  test("旧提示词库路由会跳转到训练与评估提示词视图", async ({ page }) => {
    await page.goto(`/${workspaceSlug}/prompt-library`, { waitUntil: "domcontentloaded" });
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/prompts$`), { timeout: 30000 });
    await waitForPageText(page, "训练与评估");
    await expect(page.getByRole("link", { name: "提示词库", exact: true }).last()).toBeVisible();

    for (const legacyPath of ["evaluation", "eval"]) {
      await page.goto(`/${workspaceSlug}/${legacyPath}`, { waitUntil: "domcontentloaded" });
      await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/runs$`), { timeout: 30000 });
      await expect(page.getByTestId("training-demo-dashboard")).toContainText("训练运行看板", { timeout: 10000 });
    }
  });
});
