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
    // Each destination renames the browser tab after itself (MUL-6222).
    await expect(page).toHaveTitle("收件箱 | Multica");

    await page.getByRole("link", { name: "智能体" }).click();
    await expect(page).toHaveURL(/\/agents/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "智能体");
    await expect(page).toHaveTitle("智能体 | Multica");

    await page.getByRole("link", { name: "任务", exact: true }).click();
    await expect(page).toHaveURL(/\/issues/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "任务");
    await expect(page).toHaveTitle("任务 | Multica");
  });

  test("settings page loads via sidebar", async ({ page }) => {
    await page.getByRole("link", { name: "设置", exact: true }).click();
    await expect(page).toHaveURL(/\/settings/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "设置");

    await expect(page.getByRole("tab", { name: "常规" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "成员" })).toBeVisible();
  });

  test("agents page shows agent list", async ({ page }) => {
    await page.getByRole("link", { name: "智能体" }).click();
    await expect(page).toHaveURL(/\/agents/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "智能体");

    await expect(
      page.getByRole("heading", { name: "智能体", exact: true }),
    ).toBeVisible();
  });
});
