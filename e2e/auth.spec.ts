import { test, expect } from "@playwright/test";
import {
  authenticateBrowserSession,
  createTestApi,
  loginAsDefault,
  openWorkspaceMenu,
  waitForPageText,
} from "./helpers";
import { TestApiClient } from "./fixtures";
import {
  DEFAULT_E2E_ACCOUNT,
  E2E_FIXTURE_PASSWORD,
} from "./test-identity";

test.describe("Authentication", () => {
  test("login page renders correctly", async ({ page }) => {
    await page.goto("/login", { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "登录 Multica");

    await expect(page.getByText("登录 Multica")).toBeVisible();
    await expect(page.getByRole("textbox", { name: "账号" })).toBeVisible();
    await expect(page.getByPlaceholder("alice")).toBeVisible();
    await expect(page.getByRole("button", { name: "继续" })).toBeDisabled();
  });

  test("login and redirect to /issues", async ({ page }) => {
    const workspaceSlug = await loginAsDefault(page);

    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/issues$`));
    await expect(page.getByRole("button", { name: "新建任务" })).toBeVisible();
  });

  test("login without a workspace opens the current workspace creation flow", async ({ page }) => {
    const api = new TestApiClient();
    const account = `${DEFAULT_E2E_ACCOUNT.slice(0, 40)}_no_workspace`;
    await api.login(account, "无工作区用户", E2E_FIXTURE_PASSWORD);
    for (const workspace of await api.getWorkspaces()) {
      await api.deleteWorkspace(workspace.id);
    }
    const token = api.getToken();
    if (!token) throw new Error("zero-workspace fixture login returned no token");
    await authenticateBrowserSession(page, token);

    await page.goto("/login", { waitUntil: "domcontentloaded" });

    await expect(page).toHaveURL(/\/workspaces\/new$/, { timeout: 15000 });
    await expect(page.getByRole("heading", { name: "欢迎使用 Multica" })).toBeVisible();
    await expect(page.getByRole("button", { name: "创建工作区" })).toBeDisabled();
  });

  test("unauthenticated user is redirected to /login", async ({ page }) => {
    const api = await createTestApi();
    const [workspace] = await api.getWorkspaces();
    if (!workspace) {
      throw new Error("E2E workspace was not created");
    }

    await page.goto(`/${workspace.slug}/issues`, { waitUntil: "domcontentloaded" });
    await page.waitForURL("**/login", { timeout: 10000, waitUntil: "domcontentloaded" });
    await waitForPageText(page, "登录 Multica");
  });

  test("logout redirects to /login", async ({ page }) => {
    await loginAsDefault(page);

    // Open the workspace dropdown menu
    await openWorkspaceMenu(page);

    await page.getByRole("menuitem", { name: "退出登录" }).click();

    await page.waitForURL("**/login", { timeout: 10000, waitUntil: "domcontentloaded" });
    await waitForPageText(page, "登录 Multica");
    await expect(page).toHaveURL(/\/login/);
  });
});
