import { test, expect } from "@playwright/test";
import pg from "pg";
import { loginAsDefault, createTestApi, reloadAppPage } from "./helpers";
import type { TestApiClient } from "./fixtures";

const DATABASE_URL =
  process.env.DATABASE_URL ?? "postgres://multica:multica@localhost:5432/multica?sslmode=disable";

async function setIssueTimestamps(
  issueId: string,
  timestamps: { createdAt: Date; updatedAt?: Date },
) {
  const client = new pg.Client(DATABASE_URL);
  await client.connect();
  try {
    await client.query(
      `
        UPDATE issue
        SET created_at = $2, updated_at = $3
        WHERE id = $1
      `,
      [
        issueId,
        timestamps.createdAt.toISOString(),
        (timestamps.updatedAt ?? timestamps.createdAt).toISOString(),
      ],
    );
  } finally {
    await client.end();
  }
}

test.describe("Issues", () => {
  let api: TestApiClient;
  let workspaceSlug: string;

  test.beforeEach(async ({ page }) => {
    api = await createTestApi();
    workspaceSlug = await loginAsDefault(page);
  });

  test.afterEach(async () => {
    if (api) {
      await api.cleanup();
    }
  });

  test("issues page loads with board view", async ({ page }) => {
    await api.createIssue("E2E Board View " + Date.now());
    await reloadAppPage(page);

    // Board columns should be visible.
    await expect(page.getByText("待规划").first()).toBeVisible();
    await expect(page.getByText("待办").first()).toBeVisible();
    await expect(page.getByText("进行中").first()).toBeVisible();
  });

  test("can switch from board to list view", async ({ page }) => {
    const title = "E2E List Switch " + Date.now();
    await api.createIssue(title);
    await reloadAppPage(page);
    await expect(page.getByText("待规划").first()).toBeVisible();

    // Switch to list view
    await page.getByRole("button", { name: "看板" }).click();
    await page.getByRole("menuitemradio", { name: "列表" }).click();
    await expect(page.getByText(title)).toBeVisible();
  });

  test("can filter issues by created and updated dates", async ({ page }) => {
    const suffix = Date.now();
    const todayTitle = `E2E Date Today ${suffix}`;
    const oldTitle = `E2E Date Old ${suffix}`;
    const updatedTodayTitle = `E2E Date Updated Today ${suffix}`;
    await api.createIssue(todayTitle);
    const oldIssue = await api.createIssue(oldTitle);
    const updatedTodayIssue = await api.createIssue(updatedTodayTitle);
    const oldDate = new Date();
    oldDate.setDate(oldDate.getDate() - 8);
    await setIssueTimestamps(oldIssue.id, { createdAt: oldDate });
    await setIssueTimestamps(updatedTodayIssue.id, {
      createdAt: oldDate,
      updatedAt: new Date(),
    });

    await reloadAppPage(page);
    await expect(page.getByText(todayTitle)).toBeVisible();
    await expect(page.getByText(oldTitle)).toBeVisible();
    await expect(page.getByText(updatedTodayTitle)).toBeVisible();

    await page.getByRole("button", { name: "筛选" }).click();
    await page.getByRole("menuitem", { name: "日期" }).hover();
    await page.getByRole("menuitem", { name: "今天" }).click();

    await expect(page.getByRole("button", { name: /1 个筛选/ })).toBeVisible();
    await expect(page.getByText(todayTitle)).toBeVisible();
    await expect(page.getByText(oldTitle)).toBeHidden({ timeout: 10000 });
    await expect(page.getByText(updatedTodayTitle)).toBeHidden({ timeout: 10000 });

    await page.getByRole("button", { name: /1 个筛选/ }).click();
    const dateFilterItem = page.getByRole("menuitem", { name: "日期" });
    await dateFilterItem.focus();
    await page.keyboard.press("ArrowRight");
    const updatedDateField = page.getByRole("menuitemradio", { name: "更新时间" });
    await expect(updatedDateField).toBeVisible();
    await updatedDateField.press("Enter");
    await expect(page.getByText(todayTitle)).toBeVisible();
    await expect(page.getByText(updatedTodayTitle)).toBeVisible();
    await expect(page.getByText(oldTitle)).toBeHidden({ timeout: 10000 });
  });

  test("can filter issues by custom created date", async ({ page }) => {
    const suffix = Date.now();
    const todayTitle = `E2E Date Custom Today ${suffix}`;
    const oldTitle = `E2E Date Custom Old ${suffix}`;
    await api.createIssue(todayTitle);
    const oldIssue = await api.createIssue(oldTitle);
    const oldDate = new Date();
    oldDate.setDate(oldDate.getDate() - 8);
    await setIssueTimestamps(oldIssue.id, { createdAt: oldDate });

    await reloadAppPage(page);
    await expect(page.getByText(todayTitle)).toBeVisible();
    await expect(page.getByText(oldTitle)).toBeVisible();

    await page.getByRole("button", { name: "筛选" }).click();
    await page.getByRole("menuitem", { name: "日期" }).hover();
    const customDateButton = page.getByRole("button", { name: "自定义日期或范围" });
    await expect(customDateButton).toBeVisible();
    await customDateButton.click();
    const todayDataDay = await page.evaluate(() => new Date().toLocaleDateString());
    await page.locator(`[data-day="${todayDataDay}"]`).click();
    await page.getByRole("button", { name: "应用" }).click();
    await expect(page.getByText(todayTitle)).toBeVisible();
    await expect(page.getByText(oldTitle)).toBeHidden({ timeout: 10000 });
  });

  test("opens the current agent issue creation flow", async ({ page }) => {
    const newIssueButton = page.getByRole("button", { name: "新建任务" });
    await expect(newIssueButton).toBeVisible();
    await newIssueButton.click();

    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("通过智能体创建")).toBeVisible();
    await expect(
      dialog.getByPlaceholder(
        '告诉智能体要做什么，例如："让 Bohan 修一下 Web 项目里收件箱加载慢的问题"',
      ),
    ).toBeVisible();
  });

  test("can navigate to issue detail page", async ({ page }) => {
    // Create a known issue via API so the test controls its own fixture
    const issue = await api.createIssue("E2E Detail Test " + Date.now());

    // Reload to see the new issue
    await reloadAppPage(page);

    // Navigate to the issue detail. Use a suffix match so the selector works
    // whether the href is legacy `/issues/{id}` or URL-refactored
    // `/{slug}/issues/{id}`.
    const issueLink = page.locator(`a[href$="/issues/${issue.id}"]`);
    await expect(issueLink).toBeVisible({ timeout: 5000 });
    await issueLink.click();

    await page.waitForURL(/\/issues\/[\w-]+/);

    // Should show properties panel.
    await expect(page.getByText("属性")).toBeVisible();
    // Should show the issue leaf and avoid English fallback crumbs.
    await expect(page.getByText(issue.identifier)).toBeVisible();
    await expect(page.getByText("Issues", { exact: true })).toHaveCount(0);
  });

  test("seeds parent and project when creating a child issue", async ({ page }) => {
    const suffix = Date.now();
    const usercenterProject = await api.createProject(`E2E usercenter ${suffix}`);
    const gatewayProject = await api.createProject(`E2E gateway ${suffix}`);
    await api.createProject(`E2E config ${suffix}`);
    const parent = await api.createIssue(`E2E usercenter 父任务 ${suffix}`, {
      project_id: usercenterProject.id,
      status: "in_progress",
    });
    await page.goto(`/${workspaceSlug}/issues/${parent.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByText(parent.identifier)).toBeVisible({ timeout: 15000 });
    await expect(page.getByRole("link", { name: new RegExp(parent.title) })).toBeVisible({ timeout: 15000 });

    await page.getByRole("button", { name: "添加子任务" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText(`作为 ${parent.identifier} 的子任务`)).toBeVisible({ timeout: 10000 });
    await expect(dialog.getByRole("button", { name: usercenterProject.title })).toBeVisible({ timeout: 10000 });

    await dialog.getByRole("button", { name: usercenterProject.title }).click();
    await page.getByRole("menuitem", { name: gatewayProject.title }).click();
    await expect(dialog.getByRole("button", { name: gatewayProject.title })).toBeVisible({ timeout: 10000 });
  });

  test("can dismiss issue creation", async ({ page }) => {
    await page.getByRole("button", { name: "新建任务" }).click();

    const promptInput = page.getByPlaceholder(
      '告诉智能体要做什么，例如："让 Bohan 修一下 Web 项目里收件箱加载慢的问题"',
    );
    await expect(promptInput).toBeVisible();

    await page.keyboard.press("Escape");

    await expect(promptInput).not.toBeVisible();
    await expect(page.getByRole("button", { name: "新建任务" })).toBeVisible();
  });
});
