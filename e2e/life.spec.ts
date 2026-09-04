import { expect, test } from "@playwright/test";
import { loginAsDefault, waitForPageText } from "./helpers";

const lifeTabs = [
  { label: "记忆", value: "memory" },
  { label: "实验", value: "experiment" },
  { label: "观察席", value: "observers" },
  { label: "编年史", value: "chronicle" },
] as const;

test.describe("Life", () => {
  let workspaceSlug: string;

  test.beforeEach(async ({ page }) => {
    workspaceSlug = await loginAsDefault(page);
  });

  test("life navigation and material flow survive refresh", async ({ page }) => {
    await page.goto(`/${workspaceSlug}/life?tab=memory`, {
      waitUntil: "domcontentloaded",
    });
    await expect(page).toHaveURL(/\/life\?tab=memory$/);
    await expect(page.getByRole("heading", { name: "人生", exact: true })).toBeVisible();
    await expect(page.getByRole("tab")).toHaveCount(0);

    const content = `E2E 人生材料 ${Date.now()}：今天看到一棵很好看的树。`;
    const responsePromise = page.waitForResponse(
      (response) => response.url().includes("/api/life/materials") && response.request().method() === "POST",
    );
    const readbackPromise = page.waitForResponse(
      (response) => response.url().includes("/api/life/materials") && response.request().method() === "GET",
    );
    await page
      .getByPlaceholder("今天发生了什么，或者你现在在想什么？")
      .fill(content);
    await page.getByRole("button", { name: "记下来" }).click();
    expect((await responsePromise).status()).toBe(201);
    const readback = (await readbackPromise).json() as Promise<{ materials: Array<{ content: string }> }>;
    expect((await readback).materials.some((item) => item.content === content)).toBe(true);

    for (const tab of lifeTabs.slice(1)) {
      await page.locator(`a[href$="/life?tab=${tab.value}"]`).click();
      await expect(page).toHaveURL(new RegExp(`\\/life\\?tab=${tab.value}$`));
      await expect(page.getByRole("heading", { name: tab.label, exact: true }).first()).toBeVisible();
    }

    await page.locator('a[href$="/life?tab=memory"]').click();
    await expect(page).toHaveURL(/\/life\?tab=memory$/);
    await page.reload();
    await waitForPageText(page, "随手留下材料");
    await expect(page.getByRole("heading", { name: "人生", exact: true })).toBeVisible();
    await expect(page.getByRole("tab")).toHaveCount(0);
  });

  test("life links and companion launcher remain usable in narrow windows", async ({ page }) => {
    await page.setViewportSize({ width: 767, height: 900 });
    const sidebarTrigger = page.locator('[data-sidebar="trigger"]');
    await expect(sidebarTrigger).toBeVisible();
    await sidebarTrigger.click();
    await expect(page.locator('a[href$="/life?tab=memory"]')).toBeVisible();
    await expect(page.getByRole("link", { name: "聊天", exact: true })).toHaveCount(0);
    await page.locator('a[href$="/life?tab=observers"]').click();
    await expect(page).toHaveURL(/\/life\?tab=observers$/);
    await expect(page.getByRole("heading", { name: "观察席", exact: true })).toBeVisible();

    await expect(page.getByRole("button", { name: "搭子" })).toBeVisible();
  });
});
