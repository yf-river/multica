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
const EXISTING_ISSUE_E2E_ACCOUNT = process.env.E2E_ACCOUNT ?? "develop";
const EXISTING_ISSUE_E2E_NAME = process.env.E2E_NAME ?? "AI Studio Developer";
const EXISTING_ISSUE_E2E_WORKSPACE = process.env.E2E_WORKSPACE ?? "ai-studio";
const EXISTING_ISSUE_E2E_WORKSPACE_NAME = process.env.E2E_WORKSPACE_NAME ?? "AI Studio 工作区";

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

    await page.goto(`/${workspaceSlug}/evaluation/datasets?issue=${issue.id}&case=${createdCase.id}`, { waitUntil: "domcontentloaded" });
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

test.describe("run review eval draft flow for existing completed issues", () => {
  test.describe.configure({ timeout: 120_000 });
  test.skip(process.env.RUN_REVIEW_EXISTING_ISSUE_E2E !== "1", "Set RUN_REVIEW_EXISTING_ISSUE_E2E=1 to validate against existing completed issue data.");

  let api: TestApiClient;
  let workspaceSlug: string;
  const createdCaseIds: string[] = [];

  test.beforeEach(async ({ page }) => {
    api = new TestApiClient();
    await api.login(EXISTING_ISSUE_E2E_ACCOUNT, EXISTING_ISSUE_E2E_NAME);
    const workspace = await api.ensureWorkspace(EXISTING_ISSUE_E2E_WORKSPACE_NAME, EXISTING_ISSUE_E2E_WORKSPACE);
    await api.markUserOnboarded();
    workspaceSlug = workspace.slug;
    const token = api.getToken();
    if (!token) throw new Error("E2E login did not return an auth token");
    await authenticateBrowserSession(page, token, workspaceSlug);
  });

  test.afterEach(async () => {
    if (!api) return;
    for (const caseId of createdCaseIds) {
      await api.deletePromptEvaluationCase(caseId).catch(() => undefined);
    }
    createdCaseIds.length = 0;
  });

  test("existing completed issue can generate usable eval case", async ({ page }) => {
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

    const { issue, tree } = await findExistingCompletedIssueWithRunReview(api);

    await page.goto(`/${workspaceSlug}/run-reviews?issue=${issue.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByRole("heading", { name: "运行复盘" })).toBeVisible({ timeout: 30_000 });
    await expect(page.getByRole("heading", { name: String(issue.title) })).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId("run-review-horizontal-timeline")).toBeVisible();
    await expect(page.getByTestId("run-review-horizontal-timeline")).not.toContainText("暂无可绘制的真实执行节点。");
    await expect(page.getByText("事件流")).toBeVisible();
    await expect(page.locator('[data-testid^="run-review-event-"]').first()).toBeVisible({ timeout: 30_000 });
    await expect(page.getByText("证据节点")).toHaveCount(0);
    await expect(page.getByRole("columnheader", { name: "证据" })).toHaveCount(0);

    const createDraftButton = page.getByTestId("run-review-create-eval-draft");
    await expect(createDraftButton).toBeEnabled({ timeout: 30_000 });
    await expect(createDraftButton).toContainText("生成评测用例");
    const createResponsePromise = page.waitForResponse((response) =>
      response.url().includes("/api/prompt-evaluation-cases") &&
      response.request().method() === "POST",
    );
    await createDraftButton.click();
    const createResponse = await createResponsePromise;
    expect(createResponse.ok()).toBeTruthy();
    const createdCaseFromResponse = asRecord(await createResponse.json());
    const createdCaseId = stringValue(createdCaseFromResponse.id);
    expect(createdCaseId).not.toBe("");
    createdCaseIds.push(createdCaseId);
    await expect(page.getByTestId("run-review-created-eval-draft")).toBeVisible({ timeout: 30_000 });

    await expect.poll(async () => {
      const assets = await api.listPromptEvaluationAssets({ asset_type: "数据集" });
      const asset = assets.find((item) => item.name === ISSUE_REVIEW_DRAFT_DATASET_NAME);
      if (!asset) return "";
      const cases = await api.listPromptEvaluationCases({ asset_id: asset.id, tag: `issue:${issue.id}`, limit: 20 });
      return cases.find((item) => item.id === createdCaseId)?.id ?? "";
    }, { timeout: 30_000 }).toBe(createdCaseId);
    const assets = await api.listPromptEvaluationAssets({ asset_type: "数据集" });
    const asset = assets.find((item) => item.name === ISSUE_REVIEW_DRAFT_DATASET_NAME);
    if (!asset) throw new Error("run review draft dataset was not found");
    const cases = await api.listPromptEvaluationCases({ asset_id: asset.id, tag: `issue:${issue.id}`, limit: 20 });
    const createdCase = cases.find((item) => item.id === createdCaseId);
    if (!createdCase) throw new Error(`created draft case ${createdCaseId} was not listed`);

    expect(createdCase.status).toBe("draft");
    expect(stringArray(createdCase.tags)).toEqual(expect.arrayContaining([
      "issue-review",
      "run-snapshot",
      "prompt-snapshot",
      "skill-snapshot",
      `issue:${issue.id}`,
    ]));

    const input = asRecord(createdCase.input);
    const runSnapshot = asRecord(input.run_snapshot);
    expect(runSnapshot.schema).toBe("multica.run_review.snapshot.v1");
    expect(asArray(runSnapshot.stages).length).toBeGreaterThan(0);
    expect(asArray(runSnapshot.prompt_skill_snapshots).length).toBeGreaterThan(0);
    expect(Array.isArray(runSnapshot.tool_evidence)).toBeTruthy();
    expect(asArray(runSnapshot.evidence_refs).length).toBeGreaterThan(0);
    expect(runSnapshot.formal_prompt_library_write ?? asRecord(runSnapshot.source_limits).formal_prompt_library_write).not.toBe(true);
    expect(Number(runSnapshot.timeline_node_count ?? 0)).toBeGreaterThan(0);
    expect((tree.timeline_nodes ?? []).length).toBeGreaterThan(0);

    const expected = asRecord(createdCase.expected);
    const assertions = asRecord(expected.assertions);
    expect(stringArray(assertions.required_stages)).toEqual(expect.arrayContaining([
      "PM",
      "01-需求澄清",
      "02-方案设计",
      "03-任务拆分",
      "04-开发",
      "05-测试",
    ]));
    expect(assertions.must_keep_evidence).toBe(true);
    expect(assertions.must_report_blocker_on_failure).toBe(true);
    expect(assertions.require_prompt_skill_snapshot_refs).toBe(true);
    expect(assertions.require_tool_evidence_on_tool_use).toBe(true);
    expect(Object.keys(assertions).length).toBeGreaterThan(createdCase.expected_contains.length);

    await page.goto(`/${workspaceSlug}/evaluation/datasets?issue=${issue.id}&case=${createdCase.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByText(ISSUE_REVIEW_DRAFT_DATASET_NAME)).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId(`prompt-evaluation-case-${createdCase.id}`)).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId(`prompt-evaluation-case-${createdCase.id}`)).toContainText(createdCase.case_name);

    const actionableConsoleErrors = consoleErrors.filter((message) =>
      !message.includes("Failed to load resource: the server responded with a status of 403"),
    );
    expect(actionableConsoleErrors).toEqual([]);
    expect(pageErrors).toEqual([]);
    expect(failedRequests).toEqual([]);
  });
});

async function findExistingCompletedIssueWithRunReview(api: TestApiClient) {
  const configuredIssueId = process.env.RUN_REVIEW_EXISTING_ISSUE_ID;
  if (configuredIssueId) {
    const issue = asRecord(await api.getIssue(configuredIssueId));
    const tree = await api.getIssueExecutionTree(configuredIssueId);
    if (!hasUsableRunReviewTree(tree)) {
      throw new Error(`RUN_REVIEW_EXISTING_ISSUE_ID=${configuredIssueId} does not have usable run review timeline nodes`);
    }
    return { issue, tree };
  }

  const listed = await api.listIssues({
    status: "done",
    limit: 50,
    sort_by: "created_at",
    sort_direction: "desc",
  });
  const checked: string[] = [];
  for (const candidate of listed.issues) {
    const issue = asRecord(candidate);
    const issueId = stringValue(issue.id);
    if (!issueId) continue;
    checked.push(issueId);
    const tree = await api.getIssueExecutionTree(issueId).catch(() => null);
    if (tree && hasUsableRunReviewTree(tree)) return { issue, tree };
  }
  throw new Error(`No completed issue with usable run review data found in latest ${listed.issues.length} done issues. Checked: ${checked.join(", ") || "none"}. Set RUN_REVIEW_EXISTING_ISSUE_ID to a known completed issue id.`);
}

function hasUsableRunReviewTree(tree: Awaited<ReturnType<TestApiClient["getIssueExecutionTree"]>>) {
  const timelineNodes = tree.timeline_nodes ?? [];
  if (timelineNodes.length === 0) return false;
  const summary = tree.issue_summary ?? {};
  const summaryHasEvidence =
    Number(summary.node_count ?? 0) > 0 ||
    Number(summary.message_count ?? 0) > 0 ||
    Number(summary.agent_turn_count ?? 0) > 0 ||
    Number(summary.total_input_tokens ?? 0) + Number(summary.total_output_tokens ?? 0) > 0;
  const timelineHasEvidence = timelineNodes.some((node) =>
    Number(node.message_count ?? 0) > 0 ||
    Number(node.agent_turn_count ?? 0) > 0 ||
    Number(node.trace_event_count ?? 0) > 0 ||
    Number(node.input_tokens ?? 0) + Number(node.output_tokens ?? 0) > 0 ||
    (node.evidence_refs ?? []).length > 0,
  );
  return summaryHasEvidence || timelineHasEvidence;
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function stringArray(value: unknown): string[] {
  return asArray(value).filter((item): item is string => typeof item === "string");
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}
