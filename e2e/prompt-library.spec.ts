import { test, expect } from "@playwright/test";
import { createTestApi, loginAsDefault, waitForPageText } from "./helpers";
import type { TestApiClient } from "./fixtures";

test.describe("提示词工作台", () => {
  let api: TestApiClient;
  let artifactPrefix: string;

  test.beforeEach(async ({ page }) => {
    api = await createTestApi();
    artifactPrefix = `E2E 提示词 ${Date.now()}`;
    await api.cleanupPromptArtifactsByPrefix("E2E 提示词");
    await loginAsDefault(page);
  });

  test.afterEach(async () => {
    if (api && artifactPrefix) {
      await api.cleanupPromptArtifactsByPrefix(artifactPrefix);
    }
  });

  test("可以创建提示词、调试渲染并记录评测资产", async ({ page }) => {
    await page.getByRole("link", { name: "提示词库" }).click();
    await expect(page).toHaveURL(/\/prompt-library/, { timeout: 30000 });
    await waitForPageText(page, "提示词工作台");

    await page.getByRole("button", { name: /user-center 模板/ }).click();
    await page.getByLabel("名称").fill(`${artifactPrefix} user-center 澄清`);
    await page.getByLabel("提示词内容").fill("请澄清 {{issue_title}}，项目背景：{{project_context}}。");
    await page.getByLabel("调试变量").fill("issue_title=登录失败\nproject_context=user-center");

    await expect(page.getByText("请澄清 登录失败，项目背景：user-center。")).toBeVisible();

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
    await page.getByLabel("期望输出").fill("输出需求澄清结论、风险、测试证据和下一步建议。");
    await page.getByRole("button", { name: "保存为实验" }).click();
    await expect(page.getByText("资产已创建").last()).toBeVisible({ timeout: 10000 });

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
      .toEqual(["优化运行", "实验", "实验", "数据集", "测试套件"].sort());

    const prompt = (await api.listPromptLibraryItems()).find((item) => item.name === `${artifactPrefix} user-center 澄清`);
    expect(prompt).toBeTruthy();
    const promptAssets = await api.listPromptEvaluationAssets({ prompt_id: prompt!.id });
    const dataset = promptAssets.find((asset) => asset.asset_type === "数据集");
    const testSuite = promptAssets.find((asset) => asset.asset_type === "测试套件");
    expect(dataset).toBeTruthy();
    expect(testSuite).toBeTruthy();

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
        return assets.find((asset) => asset.name.startsWith(`${artifactPrefix} user-center 澄清 Agent 调试包`)) ?? null;
      }, { timeout: 15000 })
      .toMatchObject({
        asset_type: "实验",
        payload: {
          调试包: {
            执行方式: "不真实执行 agent",
            期望输出: "输出需求澄清结论、风险、测试证据和下一步建议。",
          },
        },
      });
  });
});
