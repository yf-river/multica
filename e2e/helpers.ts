import { expect, type Page } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import {
  DEFAULT_E2E_ACCOUNT,
  DEFAULT_E2E_NAME,
  DEFAULT_E2E_PASSWORD,
  DEFAULT_E2E_WORKSPACE,
  DEFAULT_E2E_WORKSPACE_NAME,
} from "./test-identity";

async function waitForIssuesPage(page: Page) {
  await waitForPageText(page, "新建任务", 60000);
  await expect(page.getByRole("button", { name: "新建任务" })).toBeVisible({
    timeout: 30000,
  });
}

export async function waitForPageText(page: Page, text: string, timeout = 30000) {
  await page.waitForFunction(
    (expected) => document.body?.innerText.includes(expected),
    text,
    { timeout },
  );
}

export async function authenticateBrowserSession(page: Page, token: string, workspaceSlug?: string) {
  await page.addInitScript((value) => {
    localStorage.setItem("multica_token", value);
    localStorage.setItem("multica:chat:isOpen", "false");
  }, token);

  const baseURL =
    process.env.PLAYWRIGHT_BASE_URL ??
    process.env.FRONTEND_ORIGIN ??
    "http://localhost:3000";
  const cookies = [
    {
      name: "multica_logged_in",
      value: "1",
      url: baseURL,
      sameSite: "Lax" as const,
    },
  ];
  if (workspaceSlug) {
    cookies.push({
      name: "last_workspace_slug",
      value: workspaceSlug,
      url: baseURL,
      sameSite: "Lax" as const,
    });
  }
  await page.context().addCookies(cookies);
}

export async function reloadAppPage(page: Page) {
  await page.reload({ waitUntil: "domcontentloaded" });
  await waitForPageText(page, "新建任务");
}

/**
 * Log in as the default E2E user and ensure the workspace exists first.
 * Authenticates via API, then injects the token into localStorage so the
 * browser session is authenticated.
 *
 * Returns the E2E workspace slug so callers can build workspace-scoped URLs.
 */
export async function loginAsDefault(page: Page): Promise<string> {
  const api = new TestApiClient();
  await api.login(DEFAULT_E2E_ACCOUNT, DEFAULT_E2E_NAME, DEFAULT_E2E_PASSWORD);
  const workspace = await api.ensureWorkspace(
    DEFAULT_E2E_WORKSPACE_NAME,
    DEFAULT_E2E_WORKSPACE,
  );
  const token = api.getToken();
  if (!token) {
    throw new Error("E2E login did not return an auth token");
  }

  const browserLogin = await page.request.post("/auth/login", {
    data: { account: DEFAULT_E2E_ACCOUNT, password: DEFAULT_E2E_PASSWORD },
  });
  if (!browserLogin.ok()) {
    throw new Error(`E2E browser login failed: ${browserLogin.status()}`);
  }
  await authenticateBrowserSession(page, token, workspace.slug);
  await page.goto(`/${workspace.slug}/issues`, { waitUntil: "domcontentloaded" });
  await waitForIssuesPage(page);
  return workspace.slug;
}

/**
 * Create a TestApiClient logged in as the default E2E user.
 * Call api.cleanup() in afterEach to remove test data created during the test.
 */
export async function createTestApi(): Promise<TestApiClient> {
  const api = new TestApiClient();
  await api.login(DEFAULT_E2E_ACCOUNT, DEFAULT_E2E_NAME, DEFAULT_E2E_PASSWORD);
  await api.ensureWorkspace(DEFAULT_E2E_WORKSPACE_NAME, DEFAULT_E2E_WORKSPACE);
  return api;
}

export async function openWorkspaceMenu(page: Page) {
  // Click the workspace switcher button (has ChevronDown icon)
  const workspaceButton = page.getByRole("button", { name: new RegExp(DEFAULT_E2E_WORKSPACE_NAME) }).first();
  await expect(workspaceButton).toBeVisible({ timeout: 15000 });
  await workspaceButton.click();
  // Wait for dropdown to appear
  await expect(page.locator('[class*="popover"]')).toBeVisible();
}
