import { test, expect } from "@playwright/test";
import { readFile } from "node:fs/promises";

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
  test("验收账号可以看到训练评估运行看板和服务端证据快照", async ({ page }) => {
    test.setTimeout(120_000);
    const next = `/${workspaceSlug}/training/runs`;
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
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/runs$`), { timeout: 30000 });

    await expect(page.getByTestId("training-demo-dashboard")).toContainText("团队运行看板", { timeout: 30000 });
    await expect(page.getByTestId("training-demo-proof-真实 Agent 证据")).toContainText("已有任务/trace 运行记录");
    await expect(page.getByTestId("training-demo-proof-数据集行")).toContainText(/[1-9]/);
    await expect(page.getByTestId("training-demo-proof-测试套件用例")).toContainText(/[1-9]/);
    await expect(page.getByTestId("training-demo-proof-实验维度事实")).toContainText(/[1-9]/);
    await expect(page.getByTestId("training-demo-proof-服务端证据快照")).toContainText(/验收归档 [1-9]/);
    await expect(page.getByText("运行证据已服务端归档")).toBeVisible();
    await expect(page.getByText("你是从哪里了解到 Multica 的？")).toHaveCount(0);
    const downloadPromise = page.waitForEvent("download");
    await page.getByRole("button", { name: "导出证据 JSON" }).click();
    const download = await downloadPromise;
    const downloadPath = await download.path();
    expect(downloadPath).toBeTruthy();
    const exported = JSON.parse(await readFile(downloadPath!, "utf8"));
    expect(exported["语义版本"]).toBe("multica.production_demo_evidence.v1");
    expect(exported.workspace_id).toBeTruthy();
    expect(exported["证据统计"]["运行数"]).toBeGreaterThan(0);

    await page.getByRole("button", { name: "提示词库", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/prompts$`));
    await expect(page.getByTestId("prompt-version-history")).toContainText("版本历史", { timeout: 15000 });
    await expect(page.getByRole("button", { name: "需求澄清", exact: true })).toBeVisible({ timeout: 15000 });

    await page.getByRole("button", { name: "提示词调试场", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/prompt-playground$`));
    await expect(page.getByText("调试变量")).toBeVisible({ timeout: 15000 });
    await expect(page.getByText("调试输出")).toBeVisible();
    await expect(page.getByRole("button", { name: "运行并记录" })).toBeVisible();

    await page.getByRole("button", { name: "Agent 调试场", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/agent-playground$`));
    await expect(page.getByText("真实执行准备度")).toBeVisible({ timeout: 15000 });
    await expect(page.getByRole("button", { name: "创建真实 Agent 任务" })).toBeVisible();

    await page.getByRole("button", { name: "数据集", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/datasets$`));
    await expect(page.locator("[data-testid^='prompt-evaluation-asset-']").filter({ hasText: "数据集" }).first()).toContainText("数据集", { timeout: 15000 });
    await expect(page.locator("[data-testid^='prompt-evaluation-cases-']").first()).toContainText("结构化评测用例", { timeout: 15000 });

    await page.getByRole("button", { name: "测试套件", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/test-suites$`));
    await expect(page.locator("[data-testid^='prompt-evaluation-asset-']").filter({ hasText: "测试套件" }).first()).toContainText("测试套件", { timeout: 15000 });

    await page.getByRole("button", { name: "实验", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/experiments$`));
    await expect(page.getByText(/实验对比摘要：[1-9]/)).toBeVisible({ timeout: 15000 });
    await expect(page.locator("[data-testid^='prompt-evaluation-experiment-dimensions-']").first()).toContainText("实验维度事实", { timeout: 15000 });

    await page.getByRole("button", { name: "优化运行", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/optimization-runs$`));
    await expect(page.locator("[data-testid^='prompt-evaluation-asset-']").filter({ hasText: "优化运行" }).first()).toContainText("优化运行", { timeout: 15000 });
    await expect(page.locator("[data-testid^='prompt-evaluation-candidate-']").first()).toContainText(/待确认|已发布|已拒绝/, { timeout: 15000 });

    await page.getByRole("button", { name: "运行历史", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/training/run-history$`));
    const firstRun = page.locator("[data-testid^='prompt-evaluation-run-']").first();
    await expect(firstRun).toContainText(/Agent执行|模板渲染检查/, { timeout: 30000 });
    await firstRun.getByRole("button", { name: "查看证据" }).click();
    await expect(firstRun.getByTestId("run-evidence-snapshots")).toContainText("服务端证据快照", { timeout: 10000 });
  });
});
