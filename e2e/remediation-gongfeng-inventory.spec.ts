import { expect, test } from "@playwright/test";
import { loginAsDefault } from "./helpers";

test("settings repositories page shows the added Gongfeng repository inventory", async ({ page }) => {
  const consoleErrors: string[] = [];
  const pageErrors: string[] = [];
  const failedBusinessRequests: string[] = [];

  page.on("console", (message) => {
    if (message.type() === "error") consoleErrors.push(message.text());
  });
  page.on("pageerror", (error) => pageErrors.push(error.message));
  page.on("response", (response) => {
    const url = response.url();
    if (response.status() >= 400 && (url.includes("/api/") || url.includes("/auth/"))) {
      failedBusinessRequests.push(`${response.status()} ${url}`);
    }
  });

  const workspaceSlug = await loginAsDefault(page);
  await page.goto(`/${workspaceSlug}/settings?tab=repositories`, { waitUntil: "domcontentloaded" });

  const inventory = page.getByTestId("settings-gongfeng-repository-inventory");
  await expect(inventory).toBeVisible({ timeout: 30000 });
  const requiredRepos = [
    { project: "usercenter", repo: "ChainWeaver/ida/user-center" },
    { project: "gateway", repo: "ChainWeaver/ida/gateway" },
    { project: "ida-deployment", repo: "ChainWeaver/ida/ida-deployment" },
  ];
  await expect.poll(async () => inventory.getByTestId("settings-gongfeng-repository-row").count(), { timeout: 5000 }).toBeGreaterThanOrEqual(requiredRepos.length);
  for (const { project, repo } of requiredRepos) {
    const matchingRows = inventory.getByTestId("settings-gongfeng-repository-row").filter({ hasText: repo });
    await expect.poll(async () => matchingRows.count(), { timeout: 5000 }).toBeGreaterThanOrEqual(1);
    const projectRows = matchingRows.filter({ has: page.getByRole("link", { name: project, exact: true }) });
    await expect.poll(async () => projectRows.count(), { timeout: 5000 }).toBeGreaterThanOrEqual(1);
    const row = projectRows.first();
    await expect(row.getByText(repo)).toBeVisible();
    await expect(row.getByRole("link", { name: project, exact: true })).toBeVisible();
    await expect(row.getByRole("button", { name: "查看仓库详情" })).toHaveCount(1);
    await expect(row.getByRole("button", { name: "测试连接" })).toHaveCount(1);
    await expect(row.getByRole("button", { name: "刷新同步状态" })).toHaveCount(1);
    await expect(row.getByRole("button", { name: "删除仓库关联" })).toHaveCount(1);
  }
  const usercenterRow = inventory
    .getByTestId("settings-gongfeng-repository-row")
    .filter({ hasText: "ChainWeaver/ida/user-center" })
    .filter({ has: page.getByRole("link", { name: "usercenter", exact: true }) })
    .first();
  await expect(usercenterRow).toBeVisible();
  if ((await usercenterRow.getByRole("button", { name: "启用仓库" }).count()) > 0) {
    await usercenterRow.getByRole("button", { name: "启用仓库" }).click();
    await expect(usercenterRow.getByText("连接: 待验证")).toBeVisible({
      timeout: 15000,
    });
  }
  await expect(usercenterRow.getByRole("button", { name: "禁用仓库" })).toHaveCount(1);

  await usercenterRow.getByRole("button", { name: "查看仓库详情" }).click();
  const detail = page.getByTestId("settings-gongfeng-repository-detail");
  await expect(detail).toBeVisible();
  await expect(detail.getByText("Provider")).toBeVisible();
  await expect(detail.getByText("Project path")).toBeVisible();
  await expect(detail.getByText("Training")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(detail).toBeHidden();

  await usercenterRow.getByRole("button", { name: "测试连接" }).click();
  await expect(usercenterRow.getByText("连接: 需凭据").or(usercenterRow.getByText("连接: 可达"))).toBeVisible({
    timeout: 15000,
  });
  await expect(usercenterRow.getByText("测试: 通过")).toBeVisible();

  await usercenterRow.getByRole("button", { name: "刷新同步状态" }).click();
  await expect(usercenterRow.getByText("同步: 通过")).toBeVisible({
    timeout: 15000,
  });

  await usercenterRow.getByRole("button", { name: "禁用仓库" }).click();
  await expect(usercenterRow.getByText("连接: 已停用")).toBeVisible({
    timeout: 15000,
  });
  await expect(usercenterRow.getByRole("button", { name: "启用仓库" })).toHaveCount(1);

  await usercenterRow.getByRole("button", { name: "启用仓库" }).click();
  await expect(usercenterRow.getByText("连接: 待验证")).toBeVisible({
    timeout: 15000,
  });
  await expect(usercenterRow.getByRole("button", { name: "禁用仓库" })).toHaveCount(1);
  await usercenterRow.getByRole("button", { name: "测试连接" }).click();
  await expect(usercenterRow.getByText("连接: 需凭据").or(usercenterRow.getByText("连接: 可达"))).toBeVisible({
    timeout: 15000,
  });
  await expect(usercenterRow.getByText("测试: 通过")).toBeVisible();
  await usercenterRow.getByRole("button", { name: "刷新同步状态" }).click();
  await expect(usercenterRow.getByText("同步: 通过")).toBeVisible({
    timeout: 15000,
  });

  await page.goto(`/${workspaceSlug}/projects`, { waitUntil: "domcontentloaded" });
  await expect(page.getByTestId("gongfeng-repository-inventory")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "项目" })).toBeVisible();

  expect(consoleErrors).toEqual([]);
  expect(pageErrors).toEqual([]);
  expect(failedBusinessRequests).toEqual([]);
});
