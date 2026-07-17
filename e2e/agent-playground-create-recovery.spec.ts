import { expect, test } from "@playwright/test";
import {
  createTestApi,
  expectCommittedResponseRecovery,
  interceptCommittedResponseLoss,
  loginAsDefault,
  waitForPageText,
} from "./helpers";

test("agent playground create and run recover exactly after response loss", async ({ page }) => {
  const api = await createTestApi();
  const workspaceSlug = await loginAsDefault(page);
  const suffix = Date.now().toString(36);
  const experimentName = `Recovered Playground ${suffix}`;
  const agentName = `Playground Agent ${suffix}`;
  const assetName = `Playground Dataset ${suffix}`;
  const runtime = await api.ensureOnlineRuntime("codebuddy", `Playground Runtime ${suffix}`);
  const agent = await api.createAgent({ name: agentName, runtime_id: runtime.id, scope: "workspace" });
  const asset = await api.createPromptEvaluationAsset({
    name: assetName,
    description: "Agent Playground response recovery fixture",
    asset_type: "数据集",
    payload: {
      schema: "multica.training_evaluation.payload.v1",
      schema_version: 1,
      cases: [{
        case_name: "登录失败",
        variables: { issue_title: "user-center 登录失败" },
        expected_contains: ["验收条件"],
        tags: ["E2E"],
      }],
    },
    status: "启用",
  });
  let versions = await api.listPromptEvaluationDatasetVersions(asset.id);
  if (versions.length === 0) {
    await api.createPromptEvaluationDatasetVersion(asset.id, { version_label: "Playground E2E" });
    versions = await api.listPromptEvaluationDatasetVersions(asset.id);
  }
  const version = versions[0];
  if (!version) throw new Error("Agent Playground fixture has no dataset version");
  let experimentId: string | undefined;
  const createRecovery = await interceptCommittedResponseLoss(page, "**/api/agent-playground-experiments", 201);

  try {
    await page.goto(`/${workspaceSlug}/debug/agent-playground`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "Agent 调试场");
    await page.getByLabel("名称", { exact: true }).fill(experimentName);
    await page.getByRole("combobox", { name: "用例库", exact: true }).selectOption(asset.id);
    await page.getByRole("combobox", { name: "快照", exact: true }).selectOption(version.id);
    await page.getByRole("button", { name: "搜索并选择执行 Agent" }).click();
    await page.getByRole("button", { name: agentName, exact: true }).click();
    await page.getByRole("button", { name: "创建实验", exact: true }).click();

    await expect(page.getByText(experimentName).first()).toBeVisible({ timeout: 15_000 });
    expectCommittedResponseRecovery(createRecovery);
    const experiments = (await api.listAgentPlaygroundExperiments())
      .filter((item) => item.name === experimentName);
    expect(experiments).toHaveLength(1);
    experimentId = experiments[0]!.id;
    expect(experimentId).toBe(createRecovery.requestKeys[0]);
    expect(experiments[0]).toMatchObject({ input_count: 1, agent_count: 1 });

    const runRecovery = await interceptCommittedResponseLoss(page, `**/api/agent-playground-experiments/${experimentId}/run`, 202);
    await page.getByRole("button", { name: "运行", exact: true }).click();
    await expect.poll(() => runRecovery.calls, { timeout: 15_000 }).toBe(2);
    await expect.poll(async () => {
      const detail = await api.getAgentPlaygroundExperiment(experimentId!);
      return detail.results.filter((result) => result.task_id && result.chat_session_id).length;
    }).toBe(1);
  } finally {
    if (!experimentId) {
      experimentId = (await api.listAgentPlaygroundExperiments().catch(() => []))
        .find((item) => item.name === experimentName)?.id;
    }
    await api.cleanupAgentPlaygroundFixture({
      experimentId,
      requestKey: createRecovery.requestKeys[0],
      agentId: agent.id,
      assetId: asset.id,
    }).catch(() => undefined);
    await api.cleanup();
  }
});
