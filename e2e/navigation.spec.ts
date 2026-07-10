import { test, expect, type Page } from "@playwright/test";
import { loginAsDefault, waitForPageText } from "./helpers";
import { TRAINING_ROUTES, trainingRouteURLPath } from "./training-routes";

const ROUTE_CHANGE_TIMEOUT = 30000;

async function expectTrainingPageShell(page: Page, item: (typeof TRAINING_ROUTES)[number]) {
  if (item.pageKind === "agent-playground") {
    await expect(page.getByTestId("training-page-shell")).toHaveCount(0);
    await expect(page.getByText("Agent 调试场", { exact: true }).first()).toBeVisible();
    return;
  }
  const hasRouteIntro = item.introRoute !== null;
  const introRoute = item.introRoute ?? item.path;
  const panelRoute = item.panelRoute ?? item.path;
  await expect(page.getByTestId("training-page-shell")).toHaveCount(1);
  await expect(page.getByTestId("training-tab-strip")).toHaveCount(0);
  await expect(page.getByTestId(`training-route-${item.path}`)).toHaveCount(1);
  await expect(page.getByTestId(`training-route-intro-${introRoute}`)).toHaveCount(hasRouteIntro ? 1 : 0);
  await expect(page.getByTestId(`training-route-panel-${panelRoute}`)).toHaveCount(hasRouteIntro ? 1 : 0);
  await expect(page.getByTestId(`training-route-operating-model-${introRoute}`)).toHaveCount(hasRouteIntro ? 1 : 0);
  if (item.introTitle && item.operatingText) {
    await expect(page.getByTestId(`training-route-intro-${introRoute}`)).toContainText(item.introTitle);
    await expect(page.getByTestId(`training-route-operating-model-${introRoute}`)).toContainText(item.operatingText);
    await expect(page.getByTestId(`training-route-operating-step-${introRoute}-1`)).toBeVisible();
    await expect(page.getByTestId(`training-route-operating-step-${introRoute}-2`)).toBeVisible();
    await expect(page.getByTestId(`training-route-operating-step-${introRoute}-3`)).toBeVisible();
  }
}

async function expectTrainingNavigationMarker(page: Page, item: (typeof TRAINING_ROUTES)[number]) {
  const link = page.getByRole("link", { name: item.nav, exact: true }).first();
  await expect(link).toBeVisible();
  await expect(link).toHaveAttribute("href", new RegExp(`/${trainingRouteURLPath(item.path)}$`));
}

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
      await page.getByRole("button", { name: /搜索/ }).first().click();
      const input = page.getByPlaceholder("输入命令或关键词搜索...");
      await expect(input).toBeVisible({ timeout: 10000 });
      await input.fill(item.query);
      await page
        .getByText(item.command, { exact: true })
        .locator("xpath=ancestor::*[@cmdk-item][1]")
        .click();

      await expect(page).toHaveURL(new RegExp(`/${trainingRouteURLPath(item.path)}$`), { timeout: ROUTE_CHANGE_TIMEOUT });
      await waitForPageText(page, item.text);
      await expectTrainingPageShell(page, item);
      await expectTrainingNavigationMarker(page, item);
      await expect(page.getByTestId("prompt-library-editor")).toHaveCount(item.showPromptEditor ? 1 : 0);
      await expect(page.getByTestId("case-library-editor")).toHaveCount(item.showCaseLibrary ? 1 : 0);
      if (!item.showPromptEditor) {
        await expect(page.getByTestId("prompt-version-history")).toHaveCount(0);
      }
    }
  });

  test("sidebar opens every training submodule", async ({ page }) => {
    for (const item of TRAINING_ROUTES) {
      await page.getByRole("link", { name: item.section, exact: true }).click();
      await page.locator('[data-sidebar="menu-button"]').filter({ hasText: item.nav }).first().click();
      await expect(page).toHaveURL(new RegExp(`/${trainingRouteURLPath(item.path)}$`), { timeout: ROUTE_CHANGE_TIMEOUT });
      await waitForPageText(page, item.text);
      await expectTrainingPageShell(page, item);
      await expectTrainingNavigationMarker(page, item);
      await expect(page.getByTestId("prompt-library-editor")).toHaveCount(item.showPromptEditor ? 1 : 0);
      await expect(page.getByTestId("case-library-editor")).toHaveCount(item.showCaseLibrary ? 1 : 0);
      if (!item.showPromptEditor) {
        await expect(page.getByTestId("prompt-version-history")).toHaveCount(0);
      }
    }
  });

  test("agents page shows agent list", async ({ page }) => {
    await page.getByRole("link", { name: "智能体", exact: true }).first().click();
    await expect(page).toHaveURL(/\/agents/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "智能体");

    await expect(page.locator("text=智能体").first()).toBeVisible();
  });
});
