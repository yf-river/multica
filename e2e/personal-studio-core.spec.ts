import { expect, test } from "@playwright/test";
import { createTestApi, loginAsDefault, preferManualCreateMode, waitForPageText } from "./helpers";

test("Personal Studio core Web flow", async ({ page }) => {
  const api = await createTestApi();
  const workspaceSlug = await loginAsDefault(page);
  const originalMemory = `E2E 待纠正记忆 ${Date.now()}`;
  const correctedMemory = `${originalMemory}，已经确认的新理解`;
  const material = `E2E 随手记录 ${Date.now()}`;
  let issueId: string | null = null;

  try {
    const companion = page.getByRole("button", { name: /搭子|Multica 正在工作|未读对话/ });
    await expect(companion).toBeVisible();
    await companion.click();
    await expect(companion).toBeHidden();

    await preferManualCreateMode(page);
    await page.getByRole("button", { name: "新建任务" }).click();
    const issueTitle = `E2E 核心流程 ${Date.now()}`;
    await page.getByRole("textbox", { name: "任务标题" }).fill(issueTitle);
    await page.getByRole("button", { name: "创建任务" }).click();
    await expect(page.getByText("已创建任务")).toBeVisible({ timeout: 10000 });
    await page.getByRole("button", { name: "查看任务" }).click();
    await page.waitForURL(/\/issues\/[\w-]+/);
    issueId = page.url().match(/\/issues\/([^/?#]+)/)?.[1] ?? null;
    expect(issueId).not.toBeNull();

    await page.goto(`/${workspaceSlug}/run-reviews?issue=${issueId}`, {
      waitUntil: "domcontentloaded",
    });
    await waitForPageText(page, issueTitle);
    await expect(page.getByRole("heading", { name: "运行复盘", exact: true })).toBeVisible();
    await expect(page.getByText(issueTitle, { exact: false }).first()).toBeVisible();

    await api.seedLifeMemory(originalMemory);
    await page.goto(`/${workspaceSlug}/life?tab=memory`, {
      waitUntil: "domcontentloaded",
    });
    await page.getByPlaceholder("今天发生了什么，或者你现在在想什么？").fill(material);
    const recordResponse = page.waitForResponse(
      (response) =>
        response.url().includes("/api/life/materials") &&
        response.request().method() === "POST",
    );
    await page.getByRole("button", { name: "记下来" }).click();
    expect((await recordResponse).status()).toBe(201);

    const memoryCard = page.getByTestId("memory-focus-card");
    await expect(memoryCard).toContainText(originalMemory);
    await memoryCard.getByRole("button", { name: "纠正" }).click();
    await memoryCard.getByRole("textbox").fill(correctedMemory);
    await memoryCard.getByRole("button", { name: "保存纠正" }).click();
    await expect(memoryCard).toContainText(correctedMemory);

    await memoryCard.getByRole("button", { name: "永久删除" }).click();
    await memoryCard.getByRole("button", { name: "确认永久删除" }).click();
    await expect(memoryCard).not.toContainText(correctedMemory);
  } finally {
    await page.close();
    if (issueId) {
      await api.deleteIssue(issueId).catch(() => undefined);
    }
    await api.cleanup();
  }
});
