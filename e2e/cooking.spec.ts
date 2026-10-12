import { expect, test } from "@playwright/test";
import { TestApiClient } from "./fixtures";

// API ownership, persistence and content hashes are checked separately from browser behavior.
test("cooking search, direct comparison and mobile layout", async ({ page }) => {
  const api = new TestApiClient();
  const account = process.env.E2E_ACCOUNT ?? "develop";
  const password = process.env.E2E_PASSWORD ?? "develop123";
  await api.login(account, "", password);
  const workspace = await api.ensureWorkspace(
    "Cooking E2E",
    `e2e-cooking-${Date.now()}`,
  );
  await api.markUserOnboarded();
  const login = await page.request.post("/auth/login", {
    data: { account, password },
  });
  expect(login.ok()).toBe(true);
  const slug = workspace.slug;
  try {
    const externalImages: string[] = [];
    page.on("request", (request) => {
      if (
        request.resourceType() === "image" &&
        /githubusercontent|github\.io|aiursoft/.test(request.url())
      )
        externalImages.push(request.url());
    });
    await page.goto(`/${slug}/cooking`);
    await expect(
      page.getByRole("heading", { name: "烹饪指南" }),
    ).toBeVisible();
    await expect(page.locator("article")).toHaveCount(24);
    await expect(page.getByRole("checkbox", { name: "只看有图解" })).toHaveCount(0);
    await expect(page.getByRole("combobox", { name: "排序", exact: true })).toHaveCount(0);
    await page
      .getByRole("textbox", { name: "搜索菜名、食材或做法" })
      .fill("西红柿炒鸡蛋");
    await page
      .getByRole("textbox", { name: "搜索菜名、食材或做法" })
      .press("Enter");
    await expect(page).toHaveURL(/q=/);
    await page
      .getByRole("link", { name: "西红柿炒鸡蛋", exact: true })
      .last()
      .click();
    await expect(
      page.getByRole("heading", { name: "西红柿炒鸡蛋", exact: true }),
    ).toBeVisible();
    for (const mode of ["图文对照", "只看图解", "只看文字"]) {
      await expect(page.getByRole("button", { name: mode, exact: true })).toHaveCount(0);
    }
    await expect(page.getByText("正文目录", { exact: false })).toHaveCount(0);
    await expect(page.getByText("菜谱原文来源", { exact: false })).toHaveCount(0);
    await expect(page.getByText("图解来源", { exact: false })).toHaveCount(0);
    await expect(page.getByText("内容版本", { exact: false })).toHaveCount(0);
    await expect(
      page.getByRole("heading", { name: "计算", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "放大查看", exact: true }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await page.getByRole("button", { name: "返回菜谱", exact: true }).click();
    await expect(
      page.getByRole("textbox", { name: "搜索菜名、食材或做法" }),
    ).toHaveValue("西红柿炒鸡蛋");
    await page.setViewportSize({ width: 390, height: 844 });
    await page
      .getByRole("link", { name: "西红柿炒鸡蛋", exact: true })
      .last()
      .click();
    await expect(
      page.getByRole("button", { name: "只看文字", exact: true }),
    ).toHaveCount(0);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    expect(externalImages).toEqual([]);
  } finally {
    await api.cleanup();
    const cleanup = await page.request.delete(
      `/api/workspaces/${workspace.id}`,
      {
        headers: {
          Authorization: `Bearer ${api.getToken()}`,
          "X-Workspace-ID": workspace.id,
        },
      },
    );
    expect(cleanup.ok()).toBe(true);
  }
});
