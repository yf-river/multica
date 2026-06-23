import { test, expect, type Page, type Request } from "@playwright/test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { createTestApi, loginAsDefault, waitForPageText } from "./helpers";
import { DEFAULT_TRAINING_ROUTE, TRAINING_ROUTES, trainingRoutePath } from "./training-routes";

const ROUTE_CHANGE_TIMEOUT = 30000;

async function expectTrainingPageShell(page, item: (typeof TRAINING_ROUTES)[number]) {
  const isPromptPlayground = item.path === "prompt-playground";
  const isAgentPlayground = item.path === "agent-playground";
  await expect(page.getByTestId("prompt-playground-page-shell")).toHaveCount(isPromptPlayground ? 1 : 0);
  await expect(page.getByTestId("agent-playground-page-shell")).toHaveCount(isAgentPlayground ? 1 : 0);
  await expect(page.getByTestId("training-page-shell")).toHaveCount(isPromptPlayground || isAgentPlayground ? 0 : 1);
  await expect(page.getByTestId("training-tab-strip")).toHaveCount(0);
  if (!isPromptPlayground && !isAgentPlayground) {
    await expect(page.getByTestId(`training-route-${item.path}`)).toHaveCount(1);
  }
}

async function expectTrainingNavigationMarker(page, item: (typeof TRAINING_ROUTES)[number]) {
  if (item.path === "prompt-playground" || item.path === "agent-playground") {
    await expect(page.getByRole("link", { name: item.nav, exact: true }).first()).toBeVisible();
    return;
  }
  await expect(page.locator('[data-active="true"]').filter({ hasText: item.nav }).first()).toBeVisible();
}

function collectPromptEvaluationRequests(page: Page) {
  const paths: string[] = [];
  const listener = (request: Request) => {
    const url = new URL(request.url());
    if (url.pathname.startsWith("/api/prompt-evaluation")) {
      paths.push(url.pathname);
    }
  };
  page.on("request", listener);
  return {
    paths,
    stop: () => page.off("request", listener),
  };
}

function currentWorkspaceSlug(page: Page) {
  return new URL(page.url()).pathname.split("/").filter(Boolean)[0] ?? "";
}

test("training playground route modules stay dedicated", async () => {
  const repoRoot = process.cwd();
  const webPromptRoute = readFileSync(join(repoRoot, "apps/web/app/[workspaceSlug]/(dashboard)/training/prompt-playground/page.tsx"), "utf8");
  const webAgentRoute = readFileSync(join(repoRoot, "apps/web/app/[workspaceSlug]/(dashboard)/training/agent-playground/page.tsx"), "utf8");
  const webDynamicRoute = readFileSync(join(repoRoot, "apps/web/app/[workspaceSlug]/(dashboard)/training/[trainingView]/page.tsx"), "utf8");
  const desktopRoutes = readFileSync(join(repoRoot, "apps/desktop/src/renderer/src/routes.tsx"), "utf8");

  expect(webPromptRoute).toContain("PromptPlaygroundPage");
  expect(webPromptRoute).not.toContain("PromptLibraryPage");
  expect(webAgentRoute).toContain("AgentPlaygroundPage");
  expect(webAgentRoute).not.toContain("PromptLibraryPage");
  expect(webDynamicRoute).toContain('if (view === "prompt-playground") return <PromptPlaygroundPage />;');
  expect(webDynamicRoute).toContain('if (view === "agent-playground") return <AgentPlaygroundPage />;');
  expect(desktopRoutes).toContain('path: "prompt-playground", element: <PromptPlaygroundPage />');
  expect(desktopRoutes).toContain('path: "agent-playground", element: <AgentPlaygroundPage />');
});

test.describe("Navigation", () => {
  test.beforeEach(async ({ page }) => {
    await loginAsDefault(page);
    await page.waitForLoadState("networkidle");
  });

  test("sidebar navigation works", async ({ page }) => {
    await page.getByRole("link", { name: "收件箱" }).click();
    await expect(page).toHaveURL(/\/inbox/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "收件箱");

    await page.getByRole("link", { name: "智能体" }).click();
    await expect(page).toHaveURL(/\/agents/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "智能体");

    await page.getByRole("link", { name: "任务", exact: true }).click();
    await expect(page).toHaveURL(/\/issues/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "任务");
  });

  test("settings page loads via sidebar", async ({ page }) => {
    await page.getByRole("link", { name: "设置", exact: true }).click();
    await expect(page).toHaveURL(/\/settings/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "设置");

    await expect(page.getByRole("tab", { name: "通用" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "成员" })).toBeVisible();
  });

  test("command palette opens training submodules", async ({ page }) => {
    for (const item of TRAINING_ROUTES) {
      await page.getByRole("button", { name: /搜索/ }).click();
      const input = page.getByPlaceholder("输入命令或关键词搜索...");
      await expect(input).toBeVisible({ timeout: 10000 });
      await input.fill(item.query);
      await page
        .getByText(item.command, { exact: true })
        .locator("xpath=ancestor::*[@cmdk-item][1]")
        .click();

      await expect(page).toHaveURL(new RegExp(`/training/${item.path}$`), { timeout: ROUTE_CHANGE_TIMEOUT });
      await waitForPageText(page, item.text);
      await expectTrainingPageShell(page, item);
      await expectTrainingNavigationMarker(page, item);
      await expect(page.getByTestId("prompt-library-editor")).toHaveCount(item.showPromptEditor ? 1 : 0);
      if (!item.showPromptEditor) {
        await expect(page.getByTestId("prompt-version-history")).toHaveCount(0);
      }
      await expect(page.getByTestId("prompt-playground-workbench")).toHaveCount(item.showPromptPlayground ? 1 : 0);
      await expect(page.getByTestId("agent-playground-workbench")).toHaveCount(item.showAgentWorkbench ? 1 : 0);
      await expect(page.getByTestId("prompt-playground-selector-summary")).toHaveCount(item.showPromptPlayground ? 1 : 0);
      await expect(page.getByTestId("agent-playground-selector-summary")).toHaveCount(item.showAgentWorkbench ? 1 : 0);
      await expect(page.getByTestId("prompt-template-actions")).toHaveCount(item.path === "prompts" ? 1 : 0);
      await expect(page.getByRole("button", { name: "应用需求澄清模板" })).toHaveCount(item.path === "prompts" ? 1 : 0);
      await expect(page.getByRole("button", { name: "创建 user-center 需求澄清提示词" })).toHaveCount(0);
      await expect(page.getByTestId("training-summary-strip")).toHaveCount(item.showPromptPlayground || item.showAgentWorkbench ? 0 : 1);
    }
  });

  test("sidebar opens every training submodule", async ({ page }) => {
    await page.getByRole("link", { name: "训练与评估" }).click();
    await expect(page).toHaveURL(new RegExp(`/training/${DEFAULT_TRAINING_ROUTE.path}$`), { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, DEFAULT_TRAINING_ROUTE.text);

    for (const item of TRAINING_ROUTES) {
      await page.locator('[data-sidebar="menu-button"]').filter({ hasText: item.nav }).first().click();
      await expect(page).toHaveURL(new RegExp(`/training/${item.path}$`), { timeout: ROUTE_CHANGE_TIMEOUT });
      await waitForPageText(page, item.text);
      await expectTrainingPageShell(page, item);
      await expectTrainingNavigationMarker(page, item);
      await expect(page.getByTestId("prompt-library-editor")).toHaveCount(item.showPromptEditor ? 1 : 0);
      if (!item.showPromptEditor) {
        await expect(page.getByTestId("prompt-version-history")).toHaveCount(0);
      }
      await expect(page.getByTestId("prompt-playground-workbench")).toHaveCount(item.showPromptPlayground ? 1 : 0);
      await expect(page.getByTestId("agent-playground-workbench")).toHaveCount(item.showAgentWorkbench ? 1 : 0);
      await expect(page.getByTestId("prompt-playground-selector-summary")).toHaveCount(item.showPromptPlayground ? 1 : 0);
      await expect(page.getByTestId("agent-playground-selector-summary")).toHaveCount(item.showAgentWorkbench ? 1 : 0);
    }
  });

  test("training playgrounds keep distinct request boundaries", async ({ page }) => {
    const workspaceSlug = currentWorkspaceSlug(page);

    const promptRequests = collectPromptEvaluationRequests(page);
    await page.goto(trainingRoutePath(workspaceSlug, "prompt-playground"), { waitUntil: "domcontentloaded" });
    await expect(page.getByTestId("prompt-playground-page-shell")).toBeVisible({ timeout: 30000 });
    await expect(page.getByTestId("training-summary-strip")).toHaveCount(0);
    await expect(page.getByTestId("playground-page-contract")).toContainText("本地渲染 · 不启动智能体");
    await expect(page.getByTestId("prompt-playground-selector-summary")).toContainText("本地模板实验室");
    await expect(page.getByTestId("prompt-playground-purpose-map")).toContainText("不创建任务、不消耗模型");
    await expect(page.getByTestId("prompt-playground-template-boundary")).toContainText("模板选择只显示版本和变量");
    await expect(page.getByTestId("prompt-playground-contract")).toContainText("不启动智能体");
    await expect(page.getByTestId("agent-playground-run-console")).toHaveCount(0);
    await expect(page.getByTestId("agent-playground-launch-brief")).toHaveCount(0);
    await expect(page.getByTestId("agent-playground-observability-contract")).toHaveCount(0);
    await expect(page.getByTestId("agent-playground-execution-boundary")).toHaveCount(0);
    await page.waitForLoadState("networkidle").catch(() => {});
    promptRequests.stop();

    expect(promptRequests.paths.some((path) => path.includes("summary"))).toBe(false);
    expect(promptRequests.paths.some((path) => path.includes("runtime-readiness"))).toBe(false);
    expect(promptRequests.paths.some((path) => path.includes("cases"))).toBe(false);
    expect(promptRequests.paths.some((path) => path.includes("assets"))).toBe(true);
    expect(promptRequests.paths.some((path) => path.includes("runs"))).toBe(true);

    const agentRequests = collectPromptEvaluationRequests(page);
    await page.goto(trainingRoutePath(workspaceSlug, "agent-playground"), { waitUntil: "domcontentloaded" });
    await expect(page.getByTestId("agent-playground-page-shell")).toBeVisible({ timeout: 30000 });
    await expect(page.getByTestId("training-summary-strip")).toHaveCount(0);
    await expect(page.getByTestId("playground-page-contract")).toContainText("真实任务 · 写回观测证据");
    await expect(page.getByTestId("agent-playground-execution-stage")).toBeVisible();
    await expect(page.getByTestId("agent-playground-selector-summary")).toContainText("执行目标池");
    await expect(page.getByTestId("agent-playground-execution-boundary")).toContainText("任务变量、期望输出、运行时准备度");
    await expect(page.getByTestId("agent-playground-run-console")).toContainText("真实任务发射台");
    await expect(page.getByTestId("agent-playground-launch-brief")).toContainText("写入真实任务队列");
    await expect(page.getByTestId("agent-playground-evidence-strip")).toContainText("真实运行");
    await expect(page.getByTestId("agent-playground-observability-contract")).toContainText("观测回写契约");
    await expect(page.getByTestId("prompt-playground-contract")).toHaveCount(0);
    await expect(page.getByTestId("prompt-playground-purpose-map")).toHaveCount(0);
    await expect(page.getByTestId("prompt-playground-local-pipeline")).toHaveCount(0);
    await expect(page.getByTestId("prompt-playground-template-boundary")).toHaveCount(0);
    await page.waitForLoadState("networkidle").catch(() => {});
    agentRequests.stop();

    expect(agentRequests.paths.some((path) => path.includes("summary"))).toBe(false);
    expect(agentRequests.paths.some((path) => path.includes("runtime-readiness"))).toBe(true);
    expect(agentRequests.paths.some((path) => path.includes("cases"))).toBe(true);
  });

  test("training playgrounds keep selected prompt storage isolated by surface", async ({ page }) => {
    const workspaceSlug = currentWorkspaceSlug(page);
    const api = await createTestApi();
    const artifactPrefix = `E2E 导航调试场 ${Date.now()}`;
    const promptName = `${artifactPrefix} 中文提示词`;
    const prompt = await api.createPromptLibraryItem({
      name: promptName,
      description: "验证两个调试场的选中提示词持久化互不串用",
      prompt_type: "需求澄清",
      content: "请处理 {{issue_title}}，并输出中文验收证据。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
      tags: ["导航验收", "调试场"],
      status: "启用",
    });
    await page.evaluate(() => {
      for (const key of Object.keys(localStorage)) {
        if (key.startsWith("multica:training:") && key.includes(":selected-prompt:")) {
          localStorage.removeItem(key);
        }
      }
    });

    try {
      await page.goto(trainingRoutePath(workspaceSlug, "prompt-playground"), { waitUntil: "domcontentloaded" });
      await expect(page.getByTestId("prompt-playground-page-shell")).toBeVisible({ timeout: 30000 });
      await page.getByTestId("prompt-playground-prompt-list").getByRole("button", { name: new RegExp(promptName) }).click();
      await expect(page.getByTestId("prompt-playground-rendered-output")).toContainText("请处理");
      await expect
        .poll(async () =>
          page.evaluate(
            (promptId) =>
              Object.entries(localStorage).some(([key, value]) => key.startsWith("multica:training:prompt-playground:selected-prompt:") && value === promptId),
            prompt.id,
          ),
        )
        .toBe(true);

      await page.goto(trainingRoutePath(workspaceSlug, "agent-playground"), { waitUntil: "domcontentloaded" });
      await expect(page.getByTestId("agent-playground-page-shell")).toBeVisible({ timeout: 30000 });
      await page.getByTestId("agent-playground-prompt-list").getByRole("button", { name: new RegExp(promptName) }).click();
      await expect(page.getByTestId("agent-playground-task-payload")).toContainText(promptName);
      await expect
        .poll(async () =>
          page.evaluate(
            (promptId) =>
              Object.entries(localStorage).some(([key, value]) => key.startsWith("multica:training:agent-playground:selected-prompt:") && value === promptId),
            prompt.id,
          ),
        )
        .toBe(true);

      const selectedPromptKeys = await page.evaluate(() =>
        Object.keys(localStorage).filter((key) => key.startsWith("multica:training:") && key.includes(":selected-prompt:")).sort(),
      );
      expect(selectedPromptKeys.some((key) => key.startsWith("multica:training:prompt-playground:selected-prompt:"))).toBe(true);
      expect(selectedPromptKeys.some((key) => key.startsWith("multica:training:agent-playground:selected-prompt:"))).toBe(true);
      expect(selectedPromptKeys.some((key) => /^multica:training:selected-prompt:/.test(key))).toBe(false);
    } finally {
      await api.cleanupPromptArtifactsByPrefix(artifactPrefix);
      await api.cleanup();
    }
  });

  test("agents page shows agent list", async ({ page }) => {
    await page.getByRole("link", { name: "智能体" }).click();
    await expect(page).toHaveURL(/\/agents/, { timeout: ROUTE_CHANGE_TIMEOUT });
    await waitForPageText(page, "智能体");

    await expect(page.locator("text=智能体").first()).toBeVisible();
  });
});
