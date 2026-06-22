import { test, expect } from "@playwright/test";

const workspaceSlug = process.env.ACCEPTANCE_WORKSPACE_SLUG
  || process.env.REAL_AGENT_E2E_WORKSPACE
  || "goal-test-daemon";
const account = process.env.ACCEPTANCE_DEMO_ACCOUNT
  || process.env.REAL_AGENT_E2E_ACCOUNT
  || "goal-test-daemon";
const password = process.env.ACCEPTANCE_DEMO_PASSWORD
  || process.env.REAL_AGENT_E2E_PASSWORD
  || "e2e-password";
const frontendURL = process.env.ACCEPTANCE_FRONTEND_URL
  || process.env.PLAYWRIGHT_BASE_URL
  || process.env.FRONTEND_ORIGIN
  || "http://localhost:3000";

test.describe("生产部署验收", () => {
  test("领导演示账号可以看到训练评估生产看板和服务端证据快照", async ({ page }) => {
    test.setTimeout(120_000);
    const next = `/${workspaceSlug}/training?view=demo-dashboard`;
    await page.addInitScript(() => {
      localStorage.setItem("multica:chat:isOpen", "false");
    });
    await page.goto(`${frontendURL}/login?next=${encodeURIComponent(next)}`, { waitUntil: "domcontentloaded" });
    await page.waitForLoadState("networkidle").catch(() => {});
    const accountInput = page.locator("#login-account");
    const passwordInput = page.locator("#login-password");
    await expect(accountInput).toBeEditable({ timeout: 10000 });
    await accountInput.fill(account);
    await expect(accountInput).toHaveValue(account);
    await passwordInput.fill(password);
    await expect(passwordInput).toHaveValue(password);
    const continueButton = page.getByRole("button", { name: "继续" });
    await expect(continueButton).toBeEnabled({ timeout: 10000 });
    const loginResponse = page.waitForResponse(
      (response) => response.url().endsWith("/auth/login") && response.request().method() === "POST",
      { timeout: 30000 },
    );
    await continueButton.click();
    await expect((await loginResponse).ok()).toBeTruthy();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training\\?view=demo-dashboard`), { timeout: 30000 });

    await expect(page.getByTestId("training-demo-dashboard")).toContainText("团队生产看板", { timeout: 30000 });
    await expect(page.getByTestId("training-demo-proof-真实 Agent 证据")).toContainText("已有任务/trace 运行记录");
    await expect(page.getByTestId("training-demo-proof-数据集行")).toContainText(/[1-9]/);
    await expect(page.getByTestId("training-demo-proof-测试套件用例")).toContainText(/[1-9]/);
    await expect(page.getByTestId("training-demo-proof-实验维度事实")).toContainText(/[1-9]/);
    await expect(page.getByTestId("training-demo-proof-服务端证据快照")).toContainText(/验收归档 [1-9]/);
    await expect(page.getByText("运行证据已服务端归档")).toBeVisible();
    await expect(page.getByText("你是从哪里了解到 Multica 的？")).toHaveCount(0);

    await page.getByRole("button", { name: "运行历史", exact: true }).click();
    const firstRun = page.locator("[data-testid^='prompt-evaluation-run-']").first();
    await expect(firstRun).toContainText(/Agent执行|模板渲染检查/, { timeout: 30000 });
    await firstRun.getByRole("button", { name: "查看证据" }).click();
    await expect(firstRun.getByTestId("run-evidence-snapshots")).toContainText("服务端证据快照", { timeout: 10000 });
  });
});
