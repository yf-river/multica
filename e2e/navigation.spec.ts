import { test, expect } from "@playwright/test";
import { loginAsDefault, waitForPageText } from "./helpers";

const ROUTE_CHANGE_TIMEOUT = 30000;
const TRAINING_VIEWS = [
  { query: "生产看板", command: "打开生产看板", view: "demo-dashboard", nav: "生产看板", text: "团队生产看板" },
  { query: "提示词库", command: "打开提示词库", view: "prompts", nav: "提示词库", text: "提示词库" },
  { query: "提示词调试", command: "打开提示词调试场", view: "prompt-playground", nav: "提示词调试场", text: "提示词调试场" },
  { query: "Agent 调试", command: "打开 Agent 调试场", view: "agent-playground", nav: "Agent 调试场", text: "Agent 调试场" },
  { query: "数据集", command: "打开数据集", view: "datasets", nav: "数据集", text: "数据集" },
  { query: "测试套件", command: "打开测试套件", view: "test-suites", nav: "测试套件", text: "测试套件" },
  { query: "实验", command: "打开实验", view: "experiments", nav: "实验", text: "实验" },
  { query: "优化运行", command: "打开优化运行", view: "optimization-runs", nav: "优化运行", text: "优化运行" },
  { query: "运行历史", command: "打开运行历史", view: "run-history", nav: "运行历史", text: "运行历史" },
] as const;

test.describe("Navigation", () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDefault(page);
    await page.waitForLoadState("networkidle");
  });

  test("sidebar navigation works", async ({ page }) => {
    await page.getByRole("link", { name: "收件箱" }).click();
    await expect(page).toHaveURL(/\/inbox/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "收件箱");

    await page.getByRole("link", { name: "智能体" }).click();
    await expect(page).toHaveURL(/\/agents/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "智能体");

    await page.getByRole("link", { name: "任务", exact: true }).click();
    await expect(page).toHaveURL(/\/issues/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "任务");
  });

  test("settings page loads via sidebar", async ({ page }) => {
    await page.getByRole("link", { name: "设置", exact: true }).click();
    await expect(page).toHaveURL(/\/settings/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "设置");

    await expect(page.getByRole("tab", { name: "通用" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "成员" })).toBeVisible();
  });

  test("command palette opens training submodules", async ({ page }) => {
    for (const item of TRAINING_VIEWS) {
      await page.getByRole("button", { name: /搜索/ }).click();
      const input = page.getByPlaceholder("输入命令或关键词搜索...");
      await expect(input).toBeVisible({ timeout: 10000 });
      await input.fill(item.query);
      await page.getByText(item.command, { exact: true }).click();

      await expect(page).toHaveURL(new RegExp(`/training\\?view=${item.view}`), { timeout: ROUTE_CHANGE_TIMEOUT });
      await waitForPageText(page, item.text);
      await expect(page.getByTestId("training-summary-strip")).toContainText("领导视角摘要", { timeout: 30000 });
    }
  });

  test("sidebar opens every training submodule", async ({ page }) => {
    await page.getByRole("link", { name: "训练与评估" }).click();
    await expect(page).toHaveURL(/\/training(?:\?|$)/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "团队生产看板");

    for (const item of TRAINING_VIEWS) {
      await page.getByRole("link", { name: item.nav, exact: true }).click();
      await expect(page).toHaveURL(new RegExp(`/training\\?view=${item.view}`), { timeout: ROUTE_CHANGE_TIMEOUT });
      await waitForPageText(page, item.text);
    }
  });

  test("agents page shows agent list", async ({ page }) => {
    await page.getByRole("link", { name: "智能体" }).click();
    await expect(page).toHaveURL(/\/agents/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "智能体");

    await expect(page.locator("text=智能体").first()).toBeVisible();
  });
});
