import { test, expect, type Page } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { waitForPageText } from "./helpers";
import {
  DEFAULT_E2E_ACCOUNT,
  DEFAULT_E2E_WORKSPACE,
  E2E_FIXTURE_PASSWORD,
} from "./test-identity";

const ACCOUNT_PREFIX = `${DEFAULT_E2E_ACCOUNT.slice(0, 40)}_onboarding`;
const SLUG_PREFIX = `${DEFAULT_E2E_WORKSPACE.slice(0, 40)}-onboarding`;
async function loginInBrowser(page: Page, account: string) {
  const res = await page.request.post("/auth/login", {
    data: { account, password: E2E_FIXTURE_PASSWORD },
  });
  expect(res.ok()).toBe(true);
  const data = await res.json();
  const baseURL =
    process.env.PLAYWRIGHT_BASE_URL ??
    process.env.FRONTEND_ORIGIN ??
    "http://localhost:3000";
  await page.context().addCookies([
    {
      name: "multica_logged_in",
      value: "1",
      url: baseURL,
      sameSite: "Lax",
    },
  ]);
  await page.addInitScript((token) => {
    localStorage.setItem("multica_token", token);
    localStorage.setItem("multica:chat:isOpen", "false");
  }, data.token);
}

test("旧 onboarding 链接会直接进入已有工作区", async ({ page }) => {
  const api = new TestApiClient();
  const account = `${ACCOUNT_PREFIX}_with_workspace`;
  await api.login(account, "跳过引导用户", E2E_FIXTURE_PASSWORD);
  const workspace = await api.ensureWorkspace(
    "跳过引导工作区",
    `${SLUG_PREFIX}-workspace`,
  );
  await loginInBrowser(page, account);

  await page.goto("/onboarding", { waitUntil: "domcontentloaded" });
  await waitForPageText(page, "新建任务");

  await expect(page).toHaveURL(new RegExp(`/${workspace.slug}/issues$`));
  await expect(page.getByText("你是从哪里了解到 Multica 的？")).toHaveCount(0);
  await expect(page.getByText("在 web 端继续")).toHaveCount(0);
});

test("旧 onboarding 链接对无工作区用户进入新建工作区", async ({ page }) => {
  const api = new TestApiClient();
  const account = `${ACCOUNT_PREFIX}_no_workspace`;
  await api.login(account, "无工作区用户", E2E_FIXTURE_PASSWORD);
  for (const workspace of await api.getWorkspaces()) {
    await api.deleteWorkspace(workspace.id);
  }
  await loginInBrowser(page, account);

  await page.goto("/onboarding", { waitUntil: "domcontentloaded" });

  await expect(page).toHaveURL(/\/workspaces\/new$/, { timeout: 15000 });
  await expect(page.getByText("你是从哪里了解到 Multica 的？")).toHaveCount(0);
  await expect(page.getByText("在 web 端继续")).toHaveCount(0);
});
