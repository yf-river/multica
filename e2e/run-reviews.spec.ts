import { test, expect, type ConsoleMessage } from "@playwright/test";
import { authenticateBrowserSession } from "./helpers";
import { TestApiClient } from "./fixtures";

const E2E_WORKER = process.env.TEST_PARALLEL_INDEX ?? process.env.TEST_WORKER_INDEX ?? "0";
const E2E_RUN_ID = process.env.E2E_RUN_ID ?? `${Date.now().toString(36)}-${process.pid.toString(36)}`;
const E2E_ACCOUNT = process.env.E2E_ACCOUNT ?? `e2e-run-review-${E2E_WORKER}-${E2E_RUN_ID}`;
const E2E_NAME = process.env.E2E_NAME ?? "Run Review E2E User";
const E2E_WORKSPACE = process.env.E2E_WORKSPACE ?? `e2e-run-review-workspace-${E2E_WORKER}-${E2E_RUN_ID}`;
const E2E_WORKSPACE_NAME = process.env.E2E_WORKSPACE_NAME ?? `Run Review Workspace ${E2E_WORKER}`;
const ISSUE_REVIEW_DRAFT_DATASET_NAME = "Issue 复盘评测 Draft";

test.describe("run review eval draft flow", () => {
  test.describe.configure({ timeout: 120_000 });

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
    if (!api) return;
    const assets = await api.listPromptEvaluationAssets({ asset_type: "数据集" }).catch(() => []);
    for (const asset of assets) {
      if (asset.name === ISSUE_REVIEW_DRAFT_DATASET_NAME) {
        await api.deletePromptEvaluationAsset(asset.id).catch(() => undefined);
      }
    }
    await api.cleanup();
  });

  test("creates an eval draft from run review, then approves and activates it", async ({ page }) => {
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

    const issue = await api.createIssue(`Run review draft issue ${Date.now()}`, {
      status: "todo",
      description: "E2E issue used to verify run review -> eval draft -> approved -> active.",
    });

    await page.goto(`/${workspaceSlug}/run-reviews?issue=${issue.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByRole("heading", { name: "运行复盘" })).toBeVisible({ timeout: 30_000 });
    await expect(page.getByRole("heading", { name: issue.title })).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId("run-review-horizontal-timeline")).toBeVisible();
    await expect(page.getByTestId("run-review-horizontal-timeline")).toContainText("暂无可绘制的真实执行节点。");
    await expect(page.getByText("缺失诊断")).toHaveCount(0);
    await expect(page.getByText("未关联的跨项目子任务")).toHaveCount(0);
    await expect(page.getByText("节点表")).toBeVisible();
    await expect(page.getByText("暂无真实 SOP 节点。").first()).toBeVisible();

    const createDraftButton = page.getByTestId("run-review-create-eval-draft");
    await expect(createDraftButton).toBeEnabled({ timeout: 30_000 });
    await Promise.all([
      page.waitForResponse((response) => response.url().includes("/api/prompt-evaluation-cases") && response.request().method() === "POST"),
      createDraftButton.click(),
    ]);
    await expect(page.getByTestId("run-review-created-eval-draft")).toContainText("draft case", { timeout: 30_000 });

    await expect.poll(async () => {
      const assets = await api.listPromptEvaluationAssets({ asset_type: "数据集" });
      const asset = assets.find((item) => item.name === ISSUE_REVIEW_DRAFT_DATASET_NAME);
      if (!asset) return null;
      const cases = await api.listPromptEvaluationCases({ asset_id: asset.id, tag: `issue:${issue.id}`, limit: 10 });
      return cases.find((item) => item.status === "draft")?.id ?? null;
    }, { timeout: 30_000 }).not.toBeNull();
    const assets = await api.listPromptEvaluationAssets({ asset_type: "数据集" });
    const draftAsset = assets.find((item) => item.name === ISSUE_REVIEW_DRAFT_DATASET_NAME);
    if (!draftAsset) throw new Error("run review draft dataset was not created");
    const draftCases = await api.listPromptEvaluationCases({ asset_id: draftAsset.id, tag: `issue:${issue.id}`, limit: 10 });
    const createdCase = draftCases.find((item) => item.status === "draft");
    if (!createdCase) throw new Error("run review draft case was not created");

    await page.goto(`/${workspaceSlug}/training/datasets?issue=${issue.id}&case=${createdCase.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByText(ISSUE_REVIEW_DRAFT_DATASET_NAME)).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId(`prompt-evaluation-cases-${createdCase.asset_id}`)).toContainText(createdCase.case_name);
    await expect(page.getByTestId(`prompt-evaluation-cases-${createdCase.asset_id}`)).toContainText("待确认");
    const createdCaseRow = page.getByTestId(`prompt-evaluation-case-${createdCase.id}`);
    await expect(createdCaseRow).toBeVisible({ timeout: 30_000 });
    await expect(createdCaseRow.getByTestId(`prompt-evaluation-case-source-${createdCase.id}`)).toContainText("来源 issue");
    await expect(createdCaseRow.getByRole("link", { name: "查看运行复盘" })).toHaveAttribute("href", new RegExp(`/run-reviews\\?issue=${issue.id}$`));

    await page.getByTestId(`approve-eval-case-${createdCase.id}`).click();
    await expect.poll(async () => {
      const cases = await api.listPromptEvaluationCases({ asset_id: createdCase.asset_id, tag: `issue:${issue.id}`, limit: 10 });
      return cases.find((item) => item.id === createdCase.id)?.status;
    }, { timeout: 30_000 }).toBe("approved");
    await expect(page.getByTestId(`prompt-evaluation-cases-${createdCase.asset_id}`)).toContainText("已批准", { timeout: 30_000 });

    await page.getByTestId(`activate-eval-case-${createdCase.id}`).click();
    await expect.poll(async () => {
      const cases = await api.listPromptEvaluationCases({ asset_id: createdCase.asset_id, tag: `issue:${issue.id}`, limit: 10 });
      return cases.find((item) => item.id === createdCase.id)?.status;
    }, { timeout: 30_000 }).toBe("active");
    await expect(page.getByTestId(`prompt-evaluation-cases-${createdCase.asset_id}`)).toContainText("已激活", { timeout: 30_000 });

    const actionableConsoleErrors = consoleErrors.filter((message) =>
      !message.includes("Failed to load resource: the server responded with a status of 403"),
    );
    expect(actionableConsoleErrors).toEqual([]);
    expect(pageErrors).toEqual([]);
    expect(failedRequests).toEqual([]);
  });
});
