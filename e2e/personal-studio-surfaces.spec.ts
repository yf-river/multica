import { expect, test } from "@playwright/test";
import { createTestApi, loginAsDefault, waitForPageText } from "./helpers";

test.describe("个人工作室入口", () => {
  test("运行复盘展示真实任务并提供 Trace 检索与导出入口", async ({ page }) => {
    const api = await createTestApi();
    const issue = await api.createIssue(`E2E 运行复盘 ${Date.now()}`);
    const workspaceSlug = await loginAsDefault(page);

    try {
      await page.goto(`/${workspaceSlug}/run-reviews?issue=${issue.id}`, {
        waitUntil: "domcontentloaded",
      });
      await waitForPageText(page, issue.title);
      await expect(
        page.getByRole("heading", { name: "运行复盘", exact: true }),
      ).toBeVisible();
      await expect(page.getByText(issue.title, { exact: false }).first()).toBeVisible();
      await expect(
        page.getByPlaceholder("搜索事件、智能体、工具、结果或任务"),
      ).toBeVisible();
      await expect(
        page.getByRole("button", { name: "导出原始交互信息" }),
      ).toBeVisible();
    } finally {
      await page.close();
      await api.cleanup();
    }
  });

  test("设置页提供 TAPD、工蜂账号和工蜂仓库入口", async ({ page }) => {
    const workspaceSlug = await loginAsDefault(page);

    await page.goto(`/${workspaceSlug}/settings?tab=tokens`, {
      waitUntil: "domcontentloaded",
    });
    await waitForPageText(page, "外部研发账号");
    await expect(page.getByText("TAPD 账号", { exact: true })).toBeVisible();
    await expect(page.getByText("工蜂账号", { exact: true })).toBeVisible();

    await page.goto(`/${workspaceSlug}/settings?tab=repositories`, {
      waitUntil: "domcontentloaded",
    });
    await waitForPageText(page, "从工蜂添加");
    await expect(
      page.getByRole("button", { name: "从工蜂添加" }),
    ).toBeVisible();
  });
});
