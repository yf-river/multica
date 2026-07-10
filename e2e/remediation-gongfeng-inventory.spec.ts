import { expect, test } from "@playwright/test";
import { loginAsDefault } from "./helpers";

const RUN_PRESEEDED_GONGFENG_ACCEPTANCE = process.env.RUN_PRESEEDED_GONGFENG_ACCEPTANCE === "1";

test("settings repositories page opens the current Gongfeng inventory flow", async ({ page }) => {
  const workspaceSlug = await loginAsDefault(page);
  await page.goto(`/${workspaceSlug}/settings?tab=repositories`, { waitUntil: "domcontentloaded" });

  const inventory = page.getByTestId("settings-gongfeng-repository-inventory");
  await expect(inventory).toBeVisible({ timeout: 30000 });
  await expect(page.getByRole("heading", { name: "代码仓库" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "工蜂仓库资源库" })).toBeVisible();
  await inventory.getByRole("button", { name: "添加仓库" }).click();
  await expect(page.getByRole("dialog", { name: "添加工蜂仓库" })).toBeVisible();
  await expect(page.getByLabel("默认分支")).toBeVisible();
});

test("preseeded environment shows the expected Gongfeng repository inventory", async ({ page }) => {
  test.skip(
    !RUN_PRESEEDED_GONGFENG_ACCEPTANCE,
    "Set RUN_PRESEEDED_GONGFENG_ACCEPTANCE=1 only when the workspace has the required Gongfeng repositories and credential.",
  );
  const consoleErrors: string[] = [];
  const pageErrors: string[] = [];
  const failedBusinessRequests: string[] = [];

  page.on("console", (message) => {
    if (message.type() !== "error") return;
    const text = message.text();
    if (text.includes("Failed to load resource: net::ERR_CONNECTION_CLOSED")) return;
    if (text.includes("Failed to load resource: the server responded with a status of 403")) return;
    consoleErrors.push(text);
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
  await expect(page.getByRole("heading", { name: "代码仓库" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "工作区 Git 仓库" })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "工蜂仓库资源库" })).toBeVisible();
  await expect(inventory.getByRole("button", { name: "添加仓库" })).toBeVisible();
  await inventory.getByRole("button", { name: "添加仓库" }).click();
  await expect(page.getByRole("dialog", { name: "添加工蜂仓库" })).toBeVisible();
  await expect(page.getByRole("combobox")).toHaveCount(0);
  await expect(page.getByLabel("默认分支")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(inventory.getByTestId("settings-gongfeng-credential-panel")).toHaveCount(0);
  await expect(inventory.getByRole("button", { name: "保存凭据" })).toHaveCount(0);
  await expect(inventory.getByText(/这里维护工作区可选的 Gongfeng 仓库/)).toBeVisible();
  const requiredRepos = [
    {
      project: "user-center",
      repo: "ChainWeaver/ida/user-center",
      href: "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
    },
    {
      project: "gateway",
      repo: "ChainWeaver/ida/gateway",
      href: "https://git.code.tencent.com/ChainWeaver/ida/gateway/commits/v5.0.0_dev",
    },
    {
      project: "ida-deployment",
      repo: "ChainWeaver/ida/ida-deployment",
      href: "https://git.code.tencent.com/ChainWeaver/ida/ida-deployment/commits/v5.0.0_dev",
    },
  ];
  await expect.poll(async () => inventory.getByTestId("settings-gongfeng-repository-row").count(), { timeout: 5000 }).toBeGreaterThanOrEqual(requiredRepos.length);
  await expect(inventory).not.toContainText("curl usercenter");
  await expect(inventory).not.toContainText("curl gateway");
  await expect(inventory).not.toContainText("curl ida-deployment");
  await expect.poll(async () => inventory.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
  for (const { project, repo, href } of requiredRepos) {
    const matchingRows = inventory.getByTestId("settings-gongfeng-repository-row").filter({ hasText: repo });
    await expect.poll(async () => matchingRows.count(), { timeout: 5000 }).toBe(1);
    const projectRows = matchingRows.filter({ has: page.getByRole("link", { name: project, exact: true }) });
    await expect.poll(async () => projectRows.count(), { timeout: 5000 }).toBe(1);
    const row = projectRows.first();
    await expect(row.getByText(repo)).toBeVisible();
    await expect(row.getByText(/默认分支：v5\.0\.0_dev/)).toBeVisible();
    await expect(row.getByRole("link", { name: project, exact: true })).toHaveAttribute("href", href);
    await expect(row.getByRole("button", { name: /测试并同步工蜂仓库|工蜂仓库已停用/ })).toHaveCount(1);
    await expect(row.getByRole("button", { name: "删除" })).toBeVisible();
    await expect(row.getByRole("button", { name: "测试连接" })).toHaveCount(0);
    await expect(row.getByRole("button", { name: "刷新同步状态" })).toHaveCount(0);
    await expect(row.getByRole("button", { name: "删除仓库关联" })).toHaveCount(0);
  }
  const libraryRows = inventory.getByTestId("settings-gongfeng-repository-row").filter({ hasText: "资源库" });
  await expect.poll(async () => libraryRows.count(), { timeout: 5000 }).toBeGreaterThanOrEqual(1);
  await expect(libraryRows.first().getByRole("button", { name: /测试并同步工蜂仓库|工蜂仓库已停用/ })).toHaveCount(1);
  const usercenterRow = inventory
    .getByTestId("settings-gongfeng-repository-row")
    .filter({ hasText: "ChainWeaver/ida/user-center" })
    .filter({ has: page.getByRole("link", { name: "user-center", exact: true }) })
    .first();
  await expect(usercenterRow).toBeVisible();
  await usercenterRow.getByRole("button", { name: "详情" }).click();
  const detailsDialog = page.getByRole("dialog", { name: "工蜂仓库详情" });
  await expect(detailsDialog).toBeVisible();
  await expect(detailsDialog.getByRole("combobox")).toHaveCount(0);
  await expect(detailsDialog.getByRole("button", { name: "绑定项目" })).toHaveCount(0);
  await expect(detailsDialog.getByRole("button", { name: "删除项目关联" })).toBeVisible();
  await expect(detailsDialog.getByRole("button", { name: "测试并同步项目关联" })).toHaveCount(0);
  await expect(detailsDialog.getByRole("button", { name: "停用项目关联" })).toHaveCount(0);
  await page.keyboard.press("Escape");

  await page.goto(`/${workspaceSlug}/settings?tab=tokens`, { waitUntil: "domcontentloaded" });
  await expect(page.getByRole("tab", { name: "外部凭据" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Multica 访问令牌" })).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "外部服务凭据" })).toHaveCount(0);
  const credentialPanel = page.getByTestId("settings-gongfeng-credential-panel");
  await expect(credentialPanel.getByRole("heading", { name: "工蜂访问凭据" })).toBeVisible();
  await expect(credentialPanel.getByRole("button", { name: "更换凭据" })).toBeVisible();
  await credentialPanel.getByRole("button", { name: "更换凭据" }).click();
  await expect(credentialPanel.getByRole("button", { name: "保存凭据" })).toBeVisible();
  await expect.poll(async () => credentialPanel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);

  await page.goto(`/${workspaceSlug}/projects`, { waitUntil: "domcontentloaded" });
  await expect(page.getByTestId("gongfeng-repository-inventory")).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "项目" })).toBeVisible();

  expect(consoleErrors).toEqual([]);
  expect(pageErrors).toEqual([]);
  expect(failedBusinessRequests).toEqual([]);
});
