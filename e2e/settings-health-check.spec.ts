import { test, expect, type ConsoleMessage } from "@playwright/test";
import { authenticateBrowserSession } from "./helpers";
import { TestApiClient } from "./fixtures";

const E2E_WORKER = process.env.TEST_PARALLEL_INDEX ?? process.env.TEST_WORKER_INDEX ?? "0";
const E2E_RUN_ID = process.env.E2E_RUN_ID ?? `${Date.now().toString(36)}-${process.pid.toString(36)}`;
const E2E_ACCOUNT = process.env.E2E_ACCOUNT ?? `e2e-settings-health-${E2E_WORKER}-${E2E_RUN_ID}`;
const E2E_NAME = process.env.E2E_NAME ?? "Settings Health E2E User";
const E2E_WORKSPACE = process.env.E2E_WORKSPACE ?? `e2e-settings-health-workspace-${E2E_WORKER}-${E2E_RUN_ID}`;
const E2E_WORKSPACE_NAME = process.env.E2E_WORKSPACE_NAME ?? `Settings Health Workspace ${E2E_WORKER}`;

test.describe("settings integration health check", () => {
  test.describe.configure({ timeout: 90_000 });

  let api: TestApiClient;
  let workspaceSlug: string;

  test.beforeEach(async ({ page }) => {
    api = new TestApiClient();
    await api.login(E2E_ACCOUNT, E2E_NAME);
    const workspace = await api.ensureWorkspace(E2E_WORKSPACE_NAME, E2E_WORKSPACE);
    await api.markUserOnboarded();
    workspaceSlug = workspace.slug;
    const token = api.getToken();
    if (!token) throw new Error("E2E login did not return an auth token");
    await authenticateBrowserSession(page, token, workspaceSlug);
  });

  test.afterEach(async () => {
    await api?.cleanup();
  });

  test("shows TAPD, Gongfeng, MCP, daemon, and runtime health in settings", async ({ page }) => {
    const consoleErrors: string[] = [];
    const pageErrors: string[] = [];
    const failedRequests: string[] = [];
    page.on("console", (message: ConsoleMessage) => {
      if (message.type() === "error") consoleErrors.push(message.text());
    });
    page.on("pageerror", (error) => pageErrors.push(error.message));
    page.on("response", (response) => {
      if (response.status() >= 500) failedRequests.push(`${response.status()} ${response.url()}`);
    });

    await page.goto(`/${workspaceSlug}/settings?tab=integrations`, { waitUntil: "domcontentloaded" });
    await expect(page.getByTestId("settings-integration-health-check")).toBeVisible({ timeout: 30_000 });
    await expect(page.getByRole("heading", { name: "接入健康检查" })).toBeVisible();

    for (const key of [
      "tapd-readable",
      "gongfeng-token",
      "repo-usercenter",
      "repo-gateway",
      "repo-ida-deployment",
      "mcp-profile",
      "daemon-online",
      "runtime-version",
    ]) {
      await expect(page.getByTestId(`settings-health-${key}`)).toBeVisible({ timeout: 30_000 });
    }

    await expect(page.getByTestId("settings-health-tapd-readable")).toContainText("TAPD 可读");
    await expect(page.getByTestId("settings-health-gongfeng-token")).toContainText("工蜂 token 可用");
    await expect(page.getByTestId("settings-health-mcp-profile")).toContainText("MCP profile 可用");
    await expect(page.getByTestId("settings-health-daemon-online")).toContainText("daemon 在线");
    await expect(page.getByTestId("settings-health-runtime-version")).toContainText("runtime 版本满足");

    expect(consoleErrors).toEqual([]);
    expect(pageErrors).toEqual([]);
    expect(failedRequests).toEqual([]);
  });
});
