import { expect, test } from "@playwright/test";
import { loginAsDefault, waitForPageText } from "./helpers";

test.describe("Life", () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDefault(page);
  });

  test("life navigation and material flow survive refresh", async ({ page }) => {
    await page.getByRole("link", { name: "人生", exact: true }).click();
    await expect(page).toHaveURL(/\/life$/);
    await expect(page.getByRole("tab", { name: "记忆" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "实验" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "观察席" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "编年史" })).toBeVisible();

    const before = await page.getByText(/最近已接入 \d+ 条材料/).textContent();
    const responsePromise = page.waitForResponse(
      (response) => response.url().includes("/api/life/materials") && response.request().method() === "POST",
    );
    await page
      .getByPlaceholder("今天发生了什么，或者你现在在想什么？")
      .fill(`E2E 人生材料 ${Date.now()}：今天看到一棵很好看的树。`);
    await page.getByRole("button", { name: "记下来" }).click();
    expect((await responsePromise).status()).toBe(201);
    await expect(page.getByText(/最近已接入 \d+ 条材料/)).not.toHaveText(before ?? "");

    for (const tab of ["实验", "观察席", "编年史", "记忆"]) {
      await page.getByRole("tab", { name: tab }).click();
      await expect(page.getByRole("tab", { name: tab })).toHaveAttribute("data-state", "active");
    }

    await page.reload();
    await waitForPageText(page, "随手留下材料");
    await expect(page.getByRole("tab", { name: "记忆" })).toHaveAttribute("data-state", "active");
  });

  test("companion and life remain usable in a narrow window", async ({ page }) => {
    await page.setViewportSize({ width: 768, height: 900 });
    await page.getByRole("link", { name: "搭子", exact: true }).click();
    await expect(page).toHaveURL(/\/companion$/);
    await waitForPageText(page, "搭子");

    await page.goto((await page.url()).replace(/\/companion$/, "/life"));
    await waitForPageText(page, "人格与关系");
    await expect(page.getByRole("tab", { name: "编年史" })).toBeVisible();
  });
});
