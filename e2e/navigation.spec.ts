import { test, expect } from "@playwright/test";
import { loginAsDefault, waitForPageText } from "./helpers";
import { DEFAULT_TRAINING_ROUTE, TRAINING_ROUTES } from "./training-routes";

const ROUTE_CHANGE_TIMEOUT = 30000;

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
    for (const item of TRAINING_ROUTES) {
      await page.getByRole("button", { name: /搜索/ }).click();
      const input = page.getByPlaceholder("输入命令或关键词搜索...");
      await expect(input).toBeVisible({ timeout: 10000 });
      await input.fill(item.query);
      await page
        .getByText(item.command, { exact: true })
        .locator("xpath=ancestor::*[@cmdk-item][1]")
        .click();

      await expect(page).toHaveURL(new RegExp(`/training/${item.path}$`), { timeout: ROUTE_CHANGE_TIMEOUT });
      await waitForPageText(page, item.text);
      await expect(page.locator('[data-active="true"]').filter({ hasText: item.nav }).first()).toBeVisible();
      await expect(page.getByTestId("prompt-library-editor")).toHaveCount(item.showPromptEditor ? 1 : 0);
      if (!item.showPromptEditor) {
        await expect(page.getByTestId("prompt-version-history")).toHaveCount(0);
      }
      await expect(page.getByTestId("prompt-playground-workbench")).toHaveCount(item.showPromptPlayground ? 1 : 0);
      await expect(page.getByTestId("agent-playground-workbench")).toHaveCount(item.showAgentWorkbench ? 1 : 0);
      await expect(page.getByTestId("prompt-template-actions")).toHaveCount(item.path === "prompts" ? 1 : 0);
      await expect(page.getByRole("button", { name: "应用需求澄清模板" })).toHaveCount(item.path === "prompts" ? 1 : 0);
      await expect(page.getByRole("button", { name: "创建 user-center 需求澄清提示词" })).toHaveCount(0);
      await expect(page.getByTestId("training-summary-strip")).toContainText("项目总览", { timeout: 30000 });
    }
  });

  test("sidebar opens every training submodule", async ({ page }) => {
    await page.getByRole("link", { name: "训练与评估" }).click();
    await expect(page).toHaveURL(new RegExp(`/training/${DEFAULT_TRAINING_ROUTE.path}$`), { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, DEFAULT_TRAINING_ROUTE.text);

    for (const item of TRAINING_ROUTES) {
      await page.locator('[data-sidebar="menu-button"]').filter({ hasText: item.nav }).first().click();
      await expect(page).toHaveURL(new RegExp(`/training/${item.path}$`), { timeout: ROUTE_CHANGE_TIMEOUT });
      await waitForPageText(page, item.text);
      await expect(page.locator('[data-active="true"]').filter({ hasText: item.nav }).first()).toBeVisible();
      await expect(page.getByTestId("prompt-library-editor")).toHaveCount(item.showPromptEditor ? 1 : 0);
      if (!item.showPromptEditor) {
        await expect(page.getByTestId("prompt-version-history")).toHaveCount(0);
      }
      await expect(page.getByTestId("prompt-playground-workbench")).toHaveCount(item.showPromptPlayground ? 1 : 0);
      await expect(page.getByTestId("agent-playground-workbench")).toHaveCount(item.showAgentWorkbench ? 1 : 0);
    }
  });

  test("agents page shows agent list", async ({ page }) => {
    await page.getByRole("link", { name: "智能体" }).click();
    await expect(page).toHaveURL(/\/agents/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "智能体");

    await expect(page.locator("text=智能体").first()).toBeVisible();
  });
});
