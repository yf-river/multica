import { test, expect } from "@playwright/test";
import { loginAsDefault, waitForPageText } from "./helpers";

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
    await page.getByRole("button", { name: /搜索/ }).click();
    const input = page.getByPlaceholder("输入命令或关键词搜索...");
    await expect(input).toBeVisible({ timeout: 10000 });
    await input.fill("运行历史");
    await page.getByText("打开运行历史", { exact: true }).click();

    await expect(page).toHaveURL(/\/training\?view=run-history/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await expect(page.getByTestId("training-summary-strip")).toContainText("领导视角摘要", { timeout: 30000 });
    await waitForPageText(page, "运行历史");
  });

  test("agents page shows agent list", async ({ page }) => {
    await page.getByRole("link", { name: "智能体" }).click();
    await expect(page).toHaveURL(/\/agents/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "智能体");

    await expect(page.locator("text=智能体").first()).toBeVisible();
  });
});
