import { expect, type Page } from "@playwright/test";
import { createHmac, randomBytes } from "node:crypto";
import { TestApiClient } from "./fixtures";
import {
  DEFAULT_E2E_ACCOUNT,
  DEFAULT_E2E_NAME,
  DEFAULT_E2E_PASSWORD,
  DEFAULT_E2E_WORKSPACE,
  DEFAULT_E2E_WORKSPACE_NAME,
} from "./test-identity";

interface DefaultE2ESession {
  token: string;
  account: string;
  userId: string | null;
}

export const REAL_AGENT_E2E = {
  enabled: process.env.RUN_REAL_AGENT_E2E === "1",
  account: process.env.REAL_AGENT_E2E_ACCOUNT || "develop",
  password: process.env.REAL_AGENT_E2E_PASSWORD || process.env.E2E_PASSWORD || "develop123",
  workspace: process.env.REAL_AGENT_E2E_WORKSPACE || "ai-studio",
  provider: process.env.MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER || "codebuddy",
  model: process.env.MULTICA_PROMPT_EVALUATION_AGENT_MODEL || "deepseek-v4-pro-ioa",
} as const;

export interface CommittedResponseLossProbe {
  calls: number;
  requestKeys: string[];
}

let defaultSessionPromise: Promise<DefaultE2ESession> | null = null;

async function defaultE2ESession(): Promise<DefaultE2ESession> {
  defaultSessionPromise ??= (async () => {
    const api = new TestApiClient();
    await api.login(DEFAULT_E2E_ACCOUNT, DEFAULT_E2E_NAME, DEFAULT_E2E_PASSWORD);
    const token = api.getToken();
    if (!token) throw new Error("E2E login did not return an auth token");
    return { token, account: DEFAULT_E2E_ACCOUNT, userId: api.getUserId() };
  })();
  return defaultSessionPromise;
}

async function authenticatedDefaultClient(): Promise<TestApiClient> {
  const api = new TestApiClient();
  api.useAuthenticatedSession(await defaultE2ESession());
  return api;
}

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

export async function interceptCommittedResponseLoss(
  page: Page,
  url: string,
  committedStatus: number,
): Promise<CommittedResponseLossProbe> {
  const probe: CommittedResponseLossProbe = { calls: 0, requestKeys: [] };
  await page.route(url, async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    probe.calls += 1;
    probe.requestKeys.push(route.request().headers()["idempotency-key"] ?? "");
    if (probe.calls === 1) {
      const committed = await route.fetch();
      expect(committed.status()).toBe(committedStatus);
      await route.abort("connectionfailed");
      return;
    }
    await route.continue();
  });
  return probe;
}

export function expectCommittedResponseRecovery(probe: CommittedResponseLossProbe) {
  expect(probe.calls).toBe(2);
  expect(probe.requestKeys[0]).toMatch(/^[0-9a-f-]{36}$/);
  expect(probe.requestKeys[1]).toBe(probe.requestKeys[0]);
}

export async function waitForSquadLeaderTask(
  api: TestApiClient,
  issueId: string,
  leaderAgentId: string,
  options: { timeout?: number; message: string; excludeTaskId?: string },
) {
  await expect.poll(async () => {
    const task = await api.findLeaderTask(issueId, leaderAgentId);
    return task?.id && task.id !== options.excludeTaskId ? task.id : "";
  }, { timeout: options.timeout ?? 15_000, message: options.message }).not.toBe("");
  const task = await api.findLeaderTask(issueId, leaderAgentId);
  if (!task || task.id === options.excludeTaskId) throw new Error(options.message);
  return task;
}

export async function waitForIssueSOPRun(
  api: TestApiClient,
  issueId: string,
  profileKey: string,
  options: { timeout?: number; message: string },
) {
  await expect.poll(async () => {
    const runs = await api.listIssueSOPRuns(issueId);
    return runs.items.find((item) => item.profile_key === profileKey)?.id ?? "";
  }, { timeout: options.timeout ?? 15_000, message: options.message }).not.toBe("");
  const runs = await api.listIssueSOPRuns(issueId);
  const run = runs.items.find((item) => item.profile_key === profileKey);
  if (!run) throw new Error(options.message);
  return run;
}

export async function authenticateBrowserSession(
  page: Page,
  token: string,
  workspaceSlug?: string,
) {
  await page.addInitScript(() => {
    localStorage.setItem("multica:chat:isOpen", "false");
  });

  const baseURL =
    process.env.PLAYWRIGHT_BASE_URL ??
    process.env.FRONTEND_ORIGIN ??
    "http://localhost:3000";
  const csrfNonce = randomBytes(16);
  const csrfToken = `${csrfNonce.toString("hex")}.${createHmac("sha256", token).update(csrfNonce).digest("hex")}`;
  const cookies = [
    {
      name: "multica_auth",
      value: token,
      url: baseURL,
      httpOnly: true,
      sameSite: "Strict" as const,
    },
    {
      name: "multica_csrf",
      value: csrfToken,
      url: baseURL,
      sameSite: "Strict" as const,
    },
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
  const api = await authenticatedDefaultClient();
  const workspace = await api.ensureWorkspace(
    DEFAULT_E2E_WORKSPACE_NAME,
    DEFAULT_E2E_WORKSPACE,
  );
  const token = api.getToken();
  if (!token) throw new Error("cached E2E session did not contain an auth token");
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
  const api = await authenticatedDefaultClient();
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
