import { test, expect } from "@playwright/test";

const apiURL = process.env.ACCEPTANCE_API_URL
  || process.env.NEXT_PUBLIC_API_URL
  || `http://127.0.0.1:${process.env.PORT || "8080"}`;
const workspaceSlug = process.env.ACCEPTANCE_WORKSPACE_SLUG
  || process.env.REAL_AGENT_E2E_WORKSPACE
  || "goal-test-daemon";
const account = process.env.ACCEPTANCE_DEMO_ACCOUNT
  || process.env.REAL_AGENT_E2E_ACCOUNT
  || "goal-test-daemon";
const password = process.env.ACCEPTANCE_DEMO_PASSWORD
  || process.env.REAL_AGENT_E2E_PASSWORD
  || "e2e-password";

test.describe("生产部署验收", () => {
  test("领导演示账号可以看到训练评估生产看板和服务端证据快照", async ({ page }) => {
    const login = await page.request.post(`${apiURL}/auth/login`, {
      data: { account, password },
    });
    expect(login.ok(), `登录失败：${login.status()}`).toBeTruthy();
    const loginData = await login.json();
    expect(loginData.token).toEqual(expect.any(String));

    await page.addInitScript(({ token, slug }) => {
      localStorage.setItem("multica_token", token);
      localStorage.setItem("multica:chat:isOpen", "false");
      document.cookie = `last_workspace_slug=${encodeURIComponent(slug)}; path=/; max-age=31536000; SameSite=Lax`;
    }, { token: loginData.token, slug: workspaceSlug });

    await page.goto(`/${workspaceSlug}/training?view=demo-dashboard`, { waitUntil: "domcontentloaded" });
    await expect(page.getByTestId("training-demo-dashboard")).toContainText("团队生产看板", { timeout: 30000 });
    await expect(page.getByTestId("training-demo-proof-真实 Agent 证据")).toContainText("已有任务/trace 运行记录");
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
