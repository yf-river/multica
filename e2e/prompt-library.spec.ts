import { test, expect } from "@playwright/test";
import { createTestApi, loginAsDefault, waitForPageText } from "./helpers";
import type { TestApiClient } from "./fixtures";

test.describe("训练与评估工作台", () => {
	  let api: TestApiClient;
	  let artifactPrefix: string;
	  let workspaceSlug: string;

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
    const runtime = await api.ensureOnlineCodeBuddyRuntime(`${artifactPrefix} CodeBuddy Runtime`);

    await page.getByRole("link", { name: "训练与评估" }).click();
    await expect(page).toHaveURL(/\/training(?:\?|$)/, { timeout: 30000 });
    await waitForPageText(page, "训练与评估");

    await page.getByRole("button", { name: /user-center 模板/ }).click();
    await page.getByLabel("名称").fill(`${artifactPrefix} user-center 澄清`);
    await page.getByLabel("提示词内容").fill("请澄清 {{issue_title}}，项目背景：{{project_context}}。");
    await page.getByLabel("调试变量").fill("issue_title=登录失败\nproject_context=user-center");

    await expect(page.getByText("请澄清 登录失败，项目背景：user-center。").last()).toBeVisible();

    await page.getByRole("button", { name: "保存" }).click();
    await expect(page.getByText("提示词已创建")).toBeVisible({ timeout: 10000 });

    await page.getByLabel("调试变量").fill("issue_title=登录失败\nproject_context=user-center");
    await page.getByRole("button", { name: "运行并记录" }).click();
    await expect(page.getByText("优化运行已记录")).toBeVisible({ timeout: 10000 });

    for (const assetType of ["数据集", "测试套件", "实验"] as const) {
      await page.getByRole("button", { name: assetType, exact: true }).click();
      await page.getByRole("button", { name: `新建${assetType}` }).click();
      await expect(page.getByText("资产已创建").last()).toBeVisible({ timeout: 10000 });
    }

    await page.getByRole("button", { name: "Agent 调试场", exact: true }).click();
    await expect(page.getByText("CodeBuddy 在线")).toBeVisible({ timeout: 10000 });
    await expect(page.getByText(runtime.name)).toBeVisible();
    await page.getByLabel("期望输出").fill("输出需求澄清结论、风险、测试证据和下一步建议。");
    await page.getByRole("button", { name: "保存为实验" }).click();
    await expect(page.getByText("资产已创建").last()).toBeVisible({ timeout: 10000 });
    const createAgentTaskButton = page.getByRole("button", { name: "创建真实 Agent 任务" });
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
    expect(optimizationRun).toBeTruthy();
    expect(dataset).toBeTruthy();
    expect(testSuite).toBeTruthy();
    await expect(api.listPromptEvaluationCases({ asset_id: dataset!.id })).resolves.toEqual([
      expect.objectContaining({
        asset_id: dataset!.id,
        case_name: expect.stringContaining("基准用例"),
        status: "启用",
      }),
    ]);
    await page.getByRole("button", { name: "数据集", exact: true }).click();
    const datasetRow = page.getByTestId(`prompt-evaluation-asset-${dataset!.id}`);
    await expect(datasetRow).toContainText("结构化用例 1 个", { timeout: 10000 });
    await datasetRow.getByPlaceholder("手工用例名称").fill("手工补充登录失败验收");
    await datasetRow.getByPlaceholder("变量：issue_title=登录失败").fill("issue_title=登录失败\nproject_context=user-center");
    await datasetRow.getByPlaceholder("期望包含：验收条件, trace/task id").fill("验收条件, trace/task id, 可观测证据");
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
    await datasetRow.getByRole("button", { name: "删除用例" }).click();
    await expect(page.getByText("手工评测用例已删除")).toBeVisible({ timeout: 10000 });
    await expect
      .poll(async () => {
        const items = await api.listPromptEvaluationCases({ asset_id: dataset!.id });
        return items.some((item) => item.source === "manual" && item.case_name === "手工补充登录失败验收");
      }, { timeout: 15000 })
      .toBe(false);
    const optimizationRuns = await api.listPromptEvaluationRuns({ asset_id: optimizationRun!.id });
    await expect(Promise.resolve(optimizationRuns)).resolves.toEqual([
      expect.objectContaining({
        asset_id: optimizationRun!.id,
        run_kind: "本地渲染",
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
          .filter((asset) => asset.name.startsWith(`${artifactPrefix} user-center 澄清 Agent 调试包`))
          .map((asset) => asset.id);
        const runs = await api.listPromptEvaluationRuns({ limit: 20 });
        return runs.find((run) => agentAssetIds.includes(run.asset_id) && run.run_kind === "Agent执行") ?? null;
      };
    await expect
      .poll(findQueuedAgentRun, { timeout: 15000 })
      .toMatchObject({
        run_kind: "Agent执行",
        status: "已入队",
        model: "minimax-m2.7-ioa",
        runtime_provider: "codebuddy",
        runtime_id: runtime.id,
        total_cases: 1,
        passed_cases: 0,
        failed_cases: 0,
        task_id: expect.any(String),
        chat_session_id: expect.any(String),
        conclusion: "等待 Agent 执行完成",
      });
    const queuedAgentRun = await findQueuedAgentRun();
    expect(queuedAgentRun).toBeTruthy();
    const agentEvidence = await api.getPromptEvaluationRunEvidence(queuedAgentRun!.id);
    expect(agentEvidence.run).toMatchObject({
      run_kind: "Agent执行",
      status: "已入队",
      model: "minimax-m2.7-ioa",
      runtime_provider: "codebuddy",
      runtime_id: runtime.id,
      task_id: queuedAgentRun!.task_id,
    });
    expect(agentEvidence.trials).toEqual([
      expect.objectContaining({
        case_name: "Agent 调试场用例",
        status: "待执行",
        failure_reason: "等待 Agent 执行完成",
      }),
    ]);
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
    await expect(page.getByText("领导视角摘要")).toBeVisible({ timeout: 10000 });
    await expect(page.getByText("运行总数")).toBeVisible();
    await expect(page.getByText("通过率")).toBeVisible();
    await expect(page.getByText("待确认优化候选")).toBeVisible();
    await page.getByRole("button", { name: "运行历史", exact: true }).click();
    await expect(page.getByText("Agent执行 · 已入队")).toBeVisible({ timeout: 10000 });
    await expect(page.getByText(`task ${queuedAgentRun!.task_id}`)).toBeVisible();
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
    await page.goto(`/${workspaceSlug}/training?view=run-history`, { waitUntil: "domcontentloaded" });
    agentRunCard = page.getByTestId(`prompt-evaluation-run-${queuedAgentRun!.id}`);
    await expect(agentRunCard).toContainText("Agent执行 · 通过", { timeout: 10000 });
    await expect(agentRunCard).toContainText(/模型 minimax-m2\.7-ioa · runtime codebuddy · 通过 1\/1 · 输入 16 token · 输出 7 token/);
    await expect(page.getByText("本地渲染 · 通过")).toBeVisible({ timeout: 10000 });
    await expect(page.getByText(/模型 本地模板渲染 · runtime server · 通过 1\/1/)).toBeVisible();
    const localRunCard = page.locator("div.grid.gap-2.px-3.py-3").filter({ hasText: "本地渲染 · 通过" }).first();
    await localRunCard.getByRole("button", { name: "查看证据" }).click();
    await expect(page.getByText("用例明细")).toBeVisible({ timeout: 10000 });
    await expect(page.getByText("调试场用例")).toBeVisible();
    await expect(page.getByText("请澄清 登录失败，项目背景：user-center。").last()).toBeVisible();
    await expect(page.getByText("原始 evidence JSON")).toBeVisible();
    await localRunCard.getByRole("button", { name: "收起证据" }).click();
    agentRunCard = page.locator("div.grid.gap-2.px-3.py-3").filter({ hasText: "Agent执行 · 通过" }).first();
    await agentRunCard.getByRole("button", { name: "查看证据" }).click();
    await expect(page.getByText("Agent 调试场用例")).toBeVisible({ timeout: 10000 });
    await expect(page.getByText(/codebuddy\/minimax-m2\.7-ioa · 输入 11 · 输出 7 · 预估成本/)).toBeVisible();
    await expect(page.getByText("#1 text：Agent 输出：完成训练评估")).toBeVisible();
    await expect(page.getByText(/训练评估用量已上报 · completed · 输入 16 · 输出 7/)).toBeVisible();
    await expect(page.getByText("失败原因：等待 Agent 执行完成")).toHaveCount(0);
    await expect(page.getByText("task 用量")).toBeVisible();
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
        case_name: "Agent 调试场用例",
        status: "通过",
        input_tokens: 16,
        output_tokens: 7,
        failure_reason: "无",
      }),
    ]);
    expect(syncedAgentEvidence.task_usage).toEqual([
      expect.objectContaining({
        provider: "codebuddy",
        model: "minimax-m2.7-ioa",
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
        const agentAssets = assets.filter((asset) => asset.name.startsWith(`${artifactPrefix} user-center 澄清 Agent 调试包`));
        return {
          count: agentAssets.length,
          hasSnapshot: agentAssets.some((asset) => {
            const payload = asset.payload as Record<string, any>;
            return String(payload.调试包?.执行方式 ?? "").includes("CodeBuddy runtime 已在线");
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
              模型: "minimax-m2.7-ioa",
              runtime: "codebuddy",
              runtime_id: runtime.id,
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
    const runtime = await api.ensureOnlineCodeBuddyRuntime(`${artifactPrefix} 优化 CodeBuddy Runtime`);

    await page.getByRole("link", { name: "训练与评估" }).click();
    await expect(page).toHaveURL(/\/training(?:\?|$)/, { timeout: 30000 });
    await page.getByRole("button", { name: /user-center 模板/ }).click();
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
            期望包含: ["验收条件", "trace/task id"],
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

    await page.goto(`/${workspaceSlug}/training?view=run-history`, { waitUntil: "domcontentloaded" });
    const failedRunRow = page.getByTestId(`prompt-evaluation-run-${failedRun.id}`);
    await failedRunRow.scrollIntoViewIfNeeded();
    await expect(failedRunRow).toContainText("本地渲染 · 未通过", { timeout: 10000 });
    const optimizationAgentResponse = page.waitForResponse(
      (response) =>
        response.request().method() === "POST" &&
        response.url().includes(`/prompt-evaluation-runs/${failedRun.id}/optimization-agent-run`),
      { timeout: 10000 },
    );
    await failedRunRow.getByRole("button", { name: "Agent 优化任务" }).click();
    expect((await optimizationAgentResponse).status()).toBe(202);
    await expect(page.getByText(/真实 Agent 优化任务已入队/)).toBeVisible({ timeout: 10000 });

    await expect
      .poll(async () => {
        const assets = await api.listPromptEvaluationAssets({ prompt_id: prompt.id, asset_type: "优化运行" });
        const agentAsset = assets.find((item) => item.name.startsWith(`${promptName} Agent 优化运行`));
        if (!agentAsset) return null;
        const runs = await api.listPromptEvaluationRuns({ asset_id: agentAsset.id });
        const agentRun = runs.find((run) => run.run_kind === "Agent执行") ?? null;
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
        model: "minimax-m2.7-ioa",
        runtime_provider: "codebuddy",
        runtime_id: runtime.id,
        hasTask: true,
      });

    await failedRunRow.getByRole("button", { name: "生成优化候选" }).click();
    await expect(page.getByText("优化候选已生成，等待人工确认")).toBeVisible({ timeout: 10000 });

    await page.getByRole("button", { name: "优化运行", exact: true }).click();
    await expect(page.getByText(/待确认 · 失败 1/)).toBeVisible({ timeout: 10000 });
    await page.getByRole("button", { name: "发布新版本" }).click();
    await expect(page.getByText(/已发布新提示词版本/)).toBeVisible({ timeout: 10000 });

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
  });

  test("旧提示词库路由会跳转到训练与评估提示词视图", async ({ page }) => {
    await page.goto(`/${workspaceSlug}/prompt-library`, { waitUntil: "domcontentloaded" });
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training\\?view=prompts`), { timeout: 30000 });
    await waitForPageText(page, "训练与评估");
    await expect(page.getByRole("button", { name: "提示词库", exact: true })).toBeVisible();
  });
});
