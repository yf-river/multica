import { test, expect, type Page } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { waitForPageText } from "./helpers";

const ACCOUNT_PREFIX = `onboarding_disabled_${Date.now()}`;
const SLUG_PREFIX = `onboarding-disabled-${Date.now()}`;
async function loginInBrowser(page: Page, account: string) {
  const res = await page.request.post("/auth/login", {
    data: { account, password: "e2e-password" },
  });
  expect(res.ok()).toBe(true);
  const data = await res.json();
  await page.goto("/", { waitUntil: "domcontentloaded" });
  await page.evaluate((token) => {
    localStorage.setItem("multica_token", token);
  }, data.token);
}

test("旧 onboarding 链接会直接进入已有工作区", async ({ page }) => {
  const api = new TestApiClient();
  const account = `${ACCOUNT_PREFIX}_with_workspace`;
  await api.login(account, "跳过引导用户");
  const workspace = await api.ensureWorkspace(
    "跳过引导工作区",
    `${SLUG_PREFIX}-workspace`,
  );
  await loginInBrowser(page, account);

  await page.goto("/onboarding", { waitUntil: "domcontentloaded" });
  await waitForPageText(page, "新建 issue");

  await expect(page).toHaveURL(new RegExp(`/${workspace.slug}/issues$`));
  await expect(page.getByText("你是从哪里了解到 Multica 的？")).toHaveCount(0);
  await expect(page.getByText("在 web 端继续")).toHaveCount(0);
});

test("旧 onboarding 链接对无工作区用户进入新建工作区", async ({ page }) => {
  const api = new TestApiClient();
  const account = `${ACCOUNT_PREFIX}_no_workspace`;
  await api.login(account, "无工作区用户");
  await loginInBrowser(page, account);

  await page.goto("/onboarding", { waitUntil: "domcontentloaded" });

  await expect(page).toHaveURL(/\/workspaces\/new$/, { timeout: 15000 });
  await expect(page.getByText("你是从哪里了解到 Multica 的？")).toHaveCount(0);
  await expect(page.getByText("在 web 端继续")).toHaveCount(0);
});
