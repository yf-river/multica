import { test, expect } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { createTestApi, loginAsDefault, waitForPageText } from "./helpers";
import type { TestApiClient } from "./fixtures";

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
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
    test.setTimeout(120_000);
    await api.ensureOnlineCodexRuntime(`${artifactPrefix} Codex Runtime`);
    await refreshExpectedAgentModel();

    await page.getByRole("link", { name: "训练与评估" }).click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/runs$`), { timeout: 30000 });
    await waitForPageText(page, "训练与评估");
    await expect(page.getByTestId("training-demo-dashboard")).toContainText("团队运行看板", { timeout: 10000 });
    await page.getByRole("link", { name: "提示词库", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/prompts$`), { timeout: 30000 });

    await page.getByRole("button", { name: "应用需求澄清模板" }).click();
    await page.getByLabel("名称").fill(`${artifactPrefix} user-center 澄清`);
    await page.getByLabel("提示词内容").fill("请澄清 {{issue_title}}，项目背景：{{project_context}}。");
    await page.getByLabel("调试变量").fill("issue_title=登录失败\nproject_context=user-center");

    await expect(page.getByText("请澄清 登录失败，项目背景：user-center。").last()).toBeVisible();

    await page.getByRole("button", { name: "保存" }).click();
    await expect
      .poll(async () => (await api.listPromptLibraryItems()).some((item) => item.name === `${artifactPrefix} user-center 澄清`), { timeout: 10000 })
      .toBe(true);
    await expect(page.getByTestId("prompt-version-history")).toContainText("手动创建", { timeout: 10000 });
    await expect(page.getByTestId("prompt-version-history")).toContainText("当前版本 1");

    await expect(page.getByLabel("调试变量")).toHaveValue("issue_title=\nproject_context=", { timeout: 10000 });
    await page.getByLabel("调试变量").fill("issue_title=登录失败\nproject_context=user-center");
    await expect(page.getByText("请澄清 登录失败，项目背景：user-center。").last()).toBeVisible({ timeout: 10000 });
    await page.getByRole("button", { name: "运行并记录" }).click();
    await expect(page.getByText("优化运行已记录")).toBeVisible({ timeout: 10000 });

    for (const assetType of ["数据集", "测试套件", "实验"] as const) {
      await page.getByRole("link", { name: assetType, exact: true }).last().click();
      await page.getByRole("button", { name: `新建${assetType}` }).click();
      await expect(page.getByText("资产已创建").last()).toBeVisible({ timeout: 10000 });
    }

    await page.getByRole("link", { name: "智能体调试场", exact: true }).last().click();
    await expect(page.getByText("Codex 在线")).toBeVisible({ timeout: 10000 });
    if (expectedAgentRuntimeName) {
      await expect(page.getByText(expectedAgentRuntimeName)).toBeVisible();
    }
    await page.getByLabel("期望输出").fill("输出需求澄清结论、风险、测试证据和下一步建议。");
    await page.getByRole("button", { name: "保存为实验" }).click();
    await expect(page.getByText("资产已创建").last()).toBeVisible({ timeout: 10000 });
    const createAgentTaskButton = page.getByRole("button", { name: "创建真实智能体任务" });
    await expect(createAgentTaskButton).toBeEnabled({ timeout: 10000 });
    const agentRunResponse = page.waitForResponse(
      (response) => response.request().method() === "POST" && response.url().includes("/agent-run"),
      { timeout: 10000 },
    );
    await createAgentTaskButton.click();
    expect((await agentRunResponse).status()).toBe(202);

    await expect
      .poll(async () => {
        const prompts = await api.listPromptLibraryItems();
        const prompt = prompts.find((item) => item.name === `${artifactPrefix} user-center 澄清`);
        if (!prompt) return null;
        const assets = await api.listPromptEvaluationAssets({
          asset_type: "优化运行",
          prompt_id: prompt.id,
        });
        return assets.find((asset) => asset.name.startsWith(`${artifactPrefix} user-center 澄清 优化运行`)) ?? null;
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
                渲染提示词: "请澄清 登录失败，项目背景：user-center。",
              },
            ],
          },
        },
      });

    await expect
      .poll(async () => {
        const prompts = await api.listPromptLibraryItems();
        const prompt = prompts.find((item) => item.name === `${artifactPrefix} user-center 澄清`);
        if (!prompt) return [];
        const assets = await api.listPromptEvaluationAssets({ prompt_id: prompt.id });
        return assets.map((asset) => asset.asset_type).sort();
      }, { timeout: 15000 })
      .toEqual(["优化运行", "实验", "实验", "实验", "数据集", "测试套件"].sort());

    const prompt = (await api.listPromptLibraryItems()).find((item) => item.name === `${artifactPrefix} user-center 澄清`);
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
    await page.getByRole("link", { name: "数据集", exact: true }).last().click();
    const datasetRow = page.getByTestId(`prompt-evaluation-asset-${dataset!.id}`);
    await expect(datasetRow).toContainText("结构化用例 1 个", { timeout: 10000 });
    await datasetRow.getByPlaceholder("手工用例名称").fill("手工补充登录失败验收");
    await datasetRow.getByPlaceholder("变量：issue_title=登录失败").fill("issue_title=登录失败\nproject_context=user-center");
    await datasetRow.getByPlaceholder("期望包含：验收条件, trace/任务标识").fill("验收条件, trace/任务标识, 可观测证据");
    await datasetRow.getByPlaceholder("标签：user-center, 回归").fill("手工用例, user-center");
    await datasetRow.getByRole("button", { name: "新增用例" }).click();
    await expect(page.getByText("手工评测用例已创建")).toBeVisible({ timeout: 10000 });
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
    await datasetRow.getByPlaceholder("编辑变量：issue_title=登录失败").fill("issue_title=登录失败\nproject_context=user-center\npriority=P0");
    await datasetRow.getByPlaceholder("编辑期望包含").fill("验收条件, trace/任务标识, 领导演示证据");
    await datasetRow.getByPlaceholder("编辑标签").fill("手工用例, user-center, 领导演示");
    await datasetRow.getByRole("button", { name: "保存用例" }).click();
    await expect(page.getByText("手工评测用例已保存")).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: dataset!.id });
        return items.find((item) => item.source === "manual" && item.case_name === "手工补充登录失败验收 v2") ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        variables: expect.objectContaining({
          issue_title: "登录失败",
          project_context: "user-center",
          priority: "P0",
        }),
        expected_contains: expect.arrayContaining(["领导演示证据"]),
        tags: expect.arrayContaining(["领导演示"]),
      });
    await datasetRow.getByRole("button", { name: "删除用例" }).click();
    await expect(page.getByText("手工评测用例已删除")).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: dataset!.id });
        return items.some((item) => item.source === "manual" && item.case_name === "手工补充登录失败验收");
      }, { timeout: 15000 })
      .toBe(false);

    await page.getByRole("link", { name: "测试套件", exact: true }).last().click();
    const testSuiteRow = page.getByTestId(`prompt-evaluation-asset-${testSuite!.id}`);
    await expect(testSuiteRow.getByText("结构化评测用例", { exact: true })).toBeVisible({ timeout: 10000 });
    await expect(testSuiteRow).toContainText("结构化用例 1 个", { timeout: 10000 });
    await testSuiteRow.getByPlaceholder("手工用例名称").fill("手工套件回归用例");
    await testSuiteRow.getByPlaceholder("变量：issue_title=登录失败").fill("issue_title=登录失败\nproject_context=user-center");
    await testSuiteRow.getByPlaceholder("期望包含：验收条件, trace/任务标识").fill("套件结论, 通过率, trace/任务标识");
    await testSuiteRow.getByPlaceholder("标签：user-center, 回归").fill("测试套件, 回归");
    await testSuiteRow.getByRole("button", { name: "新增用例" }).click();
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
    await testSuiteRow.getByPlaceholder("编辑变量：issue_title=登录失败").fill("issue_title=登录失败\nproject_context=user-center\nowner=qa");
    await testSuiteRow.getByPlaceholder("编辑期望包含").fill("套件结论, 通过率, 领导演示证据");
    await testSuiteRow.getByPlaceholder("编辑标签").fill("测试套件, 回归, 领导演示");
    await testSuiteRow.getByRole("button", { name: "保存用例" }).click();
    await expect(page.getByText("手工评测用例已保存")).toBeVisible({ timeout: 10000 });
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
    await expect(page.getByText("手工评测用例已删除")).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: testSuite!.id });
        return items.some((item) => item.source === "manual" && item.case_name.includes("手工套件回归用例"));
      }, { timeout: 15000 })
      .toBe(false);

    await page.getByRole("link", { name: "实验", exact: true }).last().click();
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
    await experimentRow.getByPlaceholder("手工用例名称").fill("手工实验对比用例");
    await experimentRow.getByPlaceholder("变量：issue_title=登录失败").fill("issue_title=登录失败\nproject_context=user-center");
    await experimentRow.getByPlaceholder("期望包含：验收条件, trace/任务标识").fill("实验结论, 中文指标, trace/任务标识");
    await experimentRow.getByPlaceholder("标签：user-center, 回归").fill("实验, 领导演示");
    await experimentRow.getByRole("button", { name: "新增用例" }).click();
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

    await page.getByRole("link", { name: "优化运行", exact: true }).last().click();
    const optimizationRow = page.getByTestId(`prompt-evaluation-asset-${optimizationRun!.id}`);
    await expect(optimizationRow.getByText("结构化评测用例", { exact: true })).toBeVisible({ timeout: 10000 });
    await optimizationRow.getByPlaceholder("手工用例名称").fill("手工优化回归用例");
    await optimizationRow.getByPlaceholder("变量：issue_title=登录失败").fill("issue_title=登录失败\nproject_context=user-center");
    await optimizationRow.getByPlaceholder("期望包含：验收条件, trace/任务标识").fill("优化候选, 失败原因, 人工确认");
    await optimizationRow.getByPlaceholder("标签：user-center, 回归").fill("优化运行, 人工确认");
    await optimizationRow.getByRole("button", { name: "新增用例" }).click();
    await expect(page.getByText("手工评测用例已创建").last()).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: optimizationRun!.id });
        return items.find((item) => item.source === "manual" && item.case_name === "手工优化回归用例") ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        asset_id: optimizationRun!.id,
        expected_contains: expect.arrayContaining(["人工确认"]),
        tags: expect.arrayContaining(["优化运行"]),
      });
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
    const runEvidence = await api.getPromptEvaluationRunEvidence(optimizationRuns[0]!.id);
    expect(runEvidence.trials).toEqual([
      expect.objectContaining({
        case_name: "调试场用例",
        status: "通过",
        rendered_prompt: "请澄清 登录失败，项目背景：user-center。",
      }),
    ]);
    const findQueuedAgentRun = async () => {
        const assets = await api.listPromptEvaluationAssets({ prompt_id: prompt!.id });
        const agentAssetIds = assets
          .filter((asset) => asset.name.startsWith(`${artifactPrefix} user-center 澄清 智能体调试包`))
          .map((asset) => asset.id);
        const runs = await api.listPromptEvaluationRuns({ limit: 20 });
        return runs.find((run) => agentAssetIds.includes(run.asset_id) && run.run_kind === "Agent执行") ?? null;
      };
    await expect
      .poll(findQueuedAgentRun, { timeout: 15000 })
      .toMatchObject({
        run_kind: "Agent执行",
        status: "已入队",
        model: expectedAgentModel,
        runtime_provider: "codex",
        runtime_id: expectedAgentRuntimeId || expect.any(String),
        total_cases: 1,
        passed_cases: 0,
        failed_cases: 0,
        task_id: expect.any(String),
        chat_session_id: expect.any(String),
        conclusion: "等待 智能体执行完成",
      });
    const queuedAgentRun = await findQueuedAgentRun();
    expect(queuedAgentRun).toBeTruthy();
    const queuedAgentAsset = (await api.listPromptEvaluationAssets({ prompt_id: prompt!.id })).find((asset) => asset.id === queuedAgentRun!.asset_id);
    expect(queuedAgentAsset).toBeTruthy();
    const agentEvidence = await api.getPromptEvaluationRunEvidence(queuedAgentRun!.id);
    expect(agentEvidence.run).toMatchObject({
      run_kind: "Agent执行",
      status: "已入队",
      model: expectedAgentModel,
      runtime_provider: "codex",
      runtime_id: expectedAgentRuntimeId || expect.any(String),
      task_id: queuedAgentRun!.task_id,
    });
    expect(agentEvidence.trials).toEqual([
      expect.objectContaining({
        case_name: "智能体调试场用例",
        status: "待执行",
        failure_reason: "等待 智能体执行完成",
      }),
    ]);
    expect(agentEvidence.上下文).toMatchObject({
      提示词名称: prompt!.name,
      评测资产名称: queuedAgentAsset!.name,
      执行Agent名称: "Multica 训练评估 Agent",
      运行时名称: runtime.name,
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
    await page.getByRole("link", { name: "运行历史", exact: true }).last().click();
    await expect(page.getByText("Agent执行 · 已入队")).toBeVisible({ timeout: 10000 });
    await expect(page.getByText(`任务 ${queuedAgentRun!.task_id}`)).toBeVisible();
    let agentRunCard = page.locator("div.grid.gap-2.px-3.py-3").filter({ hasText: "Agent执行 · 已入队" }).first();
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
    await page.goto(`/${workspaceSlug}/training/run-history`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "运行历史", 10000);
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
    const demoDashboard = page.getByTestId("training-demo-dashboard");
    await expect(demoDashboard).toContainText("团队运行看板", { timeout: 10000 });
    await expect(demoDashboard).toContainText("训练评估闭环");
    await expect(demoDashboard).toContainText("SOP 与任务观测");
    await expect(demoDashboard.getByTestId("training-demo-metric-智能体运行数")).toContainText(/[1-9]/);
    await expect(demoDashboard.getByTestId("training-demo-proof-真实智能体 证据")).toContainText("已有任务/trace 运行记录");
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
    expect(exportedEvidence["证据统计"]["task_usage条数"]).toBeGreaterThan(0);
    expect(exportedEvidence["证据统计"]["trace_event条数"]).toBeGreaterThan(0);
    await page.getByRole("link", { name: "运行历史", exact: true }).last().click();
    agentRunCard = page.getByTestId(`prompt-evaluation-run-${queuedAgentRun!.id}`);
    await expect(agentRunCard).toContainText("Agent执行 · 通过", { timeout: 10000 });
    await expect(agentRunCard).toContainText(new RegExp(`模型 ${escapeRegExp(expectedAgentModel)} · 运行时 codex · 通过 1\\/1 · 输入 16 token · 输出 7 token`));
    await agentRunCard.getByRole("button", { name: "查看证据" }).click();
    const agentEvidencePanel = agentRunCard.getByTestId(`run-evidence-${queuedAgentRun!.id}`);
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-模型")).toContainText(expectedAgentModel, { timeout: 10000 });
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-运行时")).toContainText("codex");
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-触发来源")).toContainText("智能体调试场");
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-创建者")).toContainText(/[0-9a-f-]{36}/);
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-智能体标识")).toContainText(queuedAgentRun!.agent_id!);
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-运行时标识")).toContainText(runtime.id);
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-会话标识")).toContainText(queuedAgentRun!.chat_session_id!);
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-输入 token")).toContainText("16");
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-输出 token")).toContainText("7");
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-开始时间")).not.toContainText("未记录");
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-结束时间")).not.toContainText("未完成");
    await expect(agentEvidencePanel.getByTestId("run-evidence-metric-评估结论")).toContainText("Agent 返回结构化逐用例评估");
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText("上下文摘要");
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText(`提示词 ${prompt!.name}`);
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText(`评测资产 ${queuedAgentAsset!.name}`);
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText("Agent Multica 训练评估 Agent");
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText(`运行时 ${runtime.name}`);
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText(`任务 ${queuedAgentRun!.task_id}`);
    await expect(agentEvidencePanel.getByTestId("run-evidence-context")).toContainText("用量证据 1");
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
        }),
      });
    await expect(page.getByText("模板渲染检查 · 通过")).toBeVisible({ timeout: 10000 });
    await expect(page.getByText(/模型 本地模板渲染检查 · 运行时 server · 通过 1\/1/)).toBeVisible();
    const localRunCard = page.locator("div.grid.gap-2.px-3.py-3").filter({ hasText: "模板渲染检查 · 通过" }).first();
    await localRunCard.getByRole("button", { name: "查看证据" }).click();
    await expect(page.getByText("用例明细")).toBeVisible({ timeout: 10000 });
    await expect(localRunCard.getByText("调试场用例", { exact: true })).toBeVisible();
    await expect(localRunCard.getByText("请澄清 登录失败，项目背景：user-center。", { exact: true })).toBeVisible();
    await localRunCard.getByText("完整运行证据 JSON").click();
    await expect(localRunCard.getByText("\"task_usage\"")).toBeVisible();
    await expect(localRunCard.getByText("\"trace_events\"")).toBeVisible();
    await localRunCard.getByRole("button", { name: "收起证据" }).click();
    agentRunCard = page.locator("div.grid.gap-2.px-3.py-3").filter({ hasText: "Agent执行 · 通过" }).first();
    await agentRunCard.getByRole("button", { name: "查看证据" }).click();
    await expect(agentRunCard.getByText("智能体调试场用例", { exact: true })).toBeVisible({ timeout: 10000 });
    await expect(agentRunCard.getByText(/codex\/[^ ]+ · 输入 11 · 输出 7 · 预估成本 \$/)).toBeVisible();
    await expect(agentRunCard.getByText("缓存读 2 · 缓存写 3")).toBeVisible();
    await expect(agentRunCard.getByText("#1 text：Agent 输出：完成训练评估")).toBeVisible();
    await expect(agentRunCard.getByText(/训练评估用量已上报 · completed · codex\/[^ ]+ · 尝试次数 1 · .*输入 16 · 输出 7/)).toBeVisible();
    await expect(page.getByText("失败原因：等待 智能体执行完成")).toHaveCount(0);
    await expect(agentRunCard.getByText("任务用量")).toBeVisible();
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
        const agentAssets = assets.filter((asset) => asset.name.startsWith(`${artifactPrefix} user-center 澄清 智能体调试包`));
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
              期望输出: "输出需求澄清结论、风险、测试证据和下一步建议。",
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
    await expect(page.getByTestId("training-demo-dashboard")).toContainText("团队运行看板", { timeout: 10000 });
    await page.getByRole("link", { name: "提示词库", exact: true }).last().click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/prompts$`), { timeout: 30000 });
    await page.getByRole("button", { name: "应用需求澄清模板" }).click();
    await page.getByLabel("名称").fill(promptName);
    await page.getByLabel("提示词内容").fill(sourceContent);
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
    await failedRunRow.getByRole("button", { name: "Agent 优化任务" }).click();
    expect((await optimizationAgentResponse).status()).toBe(202);
    await expect(page.getByText(/真实智能体 优化任务已入队/)).toBeVisible({ timeout: 10000 });

    let optimizationAgentRun = null as Awaited<ReturnType<typeof api.listPromptEvaluationRuns>>[number] | null;
    await expect
      .poll(async () => {
        const assets = await api.listPromptEvaluationAssets({ prompt_id: prompt.id, asset_type: "优化运行" });
        const agentAsset = assets.find((item) => item.name.startsWith(`${promptName} Agent 优化运行`));
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
        taskType: "Agent 优化运行",
        sourceRun: failedRun.id,
        run_kind: "Agent执行",
        status: "已入队",
        model: expectedAgentModel,
        runtime_provider: "codex",
        runtime_id: expectedAgentRuntimeId || expect.any(String),
        hasTask: true,
      });

    if (!optimizationAgentRun) {
      throw new Error("E2E 未找到 Agent 优化运行记录");
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

  test("旧提示词库路由会跳转到训练与评估提示词视图", async ({ page }) => {
    await page.goto(`/${workspaceSlug}/prompt-library`, { waitUntil: "domcontentloaded" });
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/prompts$`), { timeout: 30000 });
    await waitForPageText(page, "训练与评估");
    await expect(page.getByRole("link", { name: "提示词库", exact: true }).last()).toBeVisible();

    for (const legacyPath of ["evaluation", "eval"]) {
      await page.goto(`/${workspaceSlug}/${legacyPath}`, { waitUntil: "domcontentloaded" });
      await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/runs$`), { timeout: 30000 });
      await expect(page.getByTestId("training-demo-dashboard")).toContainText("团队运行看板", { timeout: 10000 });
    }
  });
});
