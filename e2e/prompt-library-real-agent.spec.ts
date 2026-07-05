import { test, expect } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { authenticateBrowserSession, waitForPageText } from "./helpers";

const RUN_REAL_AGENT_E2E = process.env.RUN_REAL_AGENT_E2E === "1";
const REAL_AGENT_ACCOUNT = process.env.REAL_AGENT_E2E_ACCOUNT || "develop";
const REAL_AGENT_WORKSPACE = process.env.REAL_AGENT_E2E_WORKSPACE || "ai-studio";
const EXPECTED_AGENT_PROVIDER = process.env.MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER || "codebuddy";
const EXPECTED_AGENT_MODEL = process.env.MULTICA_PROMPT_EVALUATION_AGENT_MODEL || "deepseek-v4-pro-ioa";

test.describe("训练与评估真实 Agent 闭环", () => {
  test.skip(!RUN_REAL_AGENT_E2E, "设置 RUN_REAL_AGENT_E2E=1 后才运行真实 daemon/CodeBuddy 验收");

  test("CodeBuddy daemon 可以真实执行测试套件并写回运行证据", async ({ page }) => {
    test.setTimeout(240_000);

    const api = new TestApiClient();
    const prefix = `真实Agent验收 ${Date.now()}`;
    await api.login(REAL_AGENT_ACCOUNT, "胡云飞");
    const workspace = await api.ensureWorkspace("AI Studio 工作区", REAL_AGENT_WORKSPACE);
    await api.markUserOnboarded();
    await api.cleanupPromptArtifactsByPrefix(prefix);

    try {
      const readiness = await api.getPromptEvaluationRuntimeReadiness();
      expect(readiness).toMatchObject({
        status: "就绪",
        model: EXPECTED_AGENT_MODEL,
      });
      expect(readiness.runtime).toMatchObject({
        provider: EXPECTED_AGENT_PROVIDER,
        status: "online",
      });

      const prompt = await api.createPromptLibraryItem({
        name: `${prefix} 提示词`,
        description: "真实 daemon E2E：创建提示词后由 Codex 执行评估。",
        prompt_type: "需求澄清",
        content: "请用中文澄清 {{issue_title}}，必须输出验收条件、风险、trace/任务标识和下一步建议。",
        variables: [{ name: "issue_title", label: "任务标题", required: true }],
        tags: ["真实Agent", "E2E", "训练与评估"],
        status: "启用",
      });

      const asset = await api.createPromptEvaluationAsset({
        prompt_id: prompt.id,
        name: `${prefix} 测试套件`,
        asset_type: "测试套件",
        payload: {
          cases: [
            {
              名称: "真实 Agent 中文证据用例",
              变量: { issue_title: "登录失败" },
              期望包含: ["验收条件", "trace/任务标识", "下一步"],
            },
          ],
        },
        status: "启用",
      });

      const queued = await api.runPromptEvaluationAssetAgent(asset.id);
      expect(queued).toMatchObject({
        model: EXPECTED_AGENT_MODEL,
        status: "已入队",
      });
      expect(queued.task_id).toBeTruthy();
      expect(queued.runtime_id).toBe(readiness.runtime?.id);

      const terminalRun = await expect
        .poll(async () => {
          await api.syncPromptEvaluationRun(queued.run.id).catch(() => null);
          const runs = await api.listPromptEvaluationRuns({ asset_id: asset.id, limit: 10 });
          const run = runs.find((item) => item.id === queued.run.id) ?? null;
          if (!run || run.status === "已入队" || run.status === "运行中") return null;
          return run;
        }, { timeout: 180_000, intervals: [3_000, 5_000, 10_000] })
        .toMatchObject({
          run_kind: "Agent执行",
          model: EXPECTED_AGENT_MODEL,
          runtime_provider: EXPECTED_AGENT_PROVIDER,
          total_cases: 1,
        })
        .then(async () => {
          const runs = await api.listPromptEvaluationRuns({ asset_id: asset.id, limit: 10 });
          return runs.find((run) => run.id === queued.run.id)!;
        });

      expect(["通过", "未通过", "需人工复核", "失败"]).toContain(terminalRun.status);
      expect(terminalRun.task_id).toBe(queued.task_id);

      const syncedRun = await api.syncPromptEvaluationRun(queued.run.id);
      const evidence = await api.getPromptEvaluationRunEvidence(queued.run.id);
      expect(evidence.run).toMatchObject({
        id: queued.run.id,
        run_kind: "Agent执行",
        model: EXPECTED_AGENT_MODEL,
        runtime_provider: EXPECTED_AGENT_PROVIDER,
        task_id: queued.task_id,
      });
      expect(evidence.trials.length).toBeGreaterThan(0);
      expect(evidence.task_messages.length).toBeGreaterThan(0);
      expect(evidence.trace_events.length).toBeGreaterThan(0);
      if (syncedRun.status === "失败" && syncedRun.failure_reason.includes("模型额度不足")) {
        expect(evidence.task_usage).toHaveLength(0);
        expect(JSON.stringify(evidence.task_messages)).toContain("无可用Token额度");
        expect(JSON.stringify(evidence.trace_events)).toContain("任务已失败");
        const token = api.getToken();
        expect(token).toBeTruthy();
        await authenticateBrowserSession(page, token!, workspace.slug);
        await page.goto(`/${workspace.slug}/training/evaluation-runs?run=${queued.run.id}`, { waitUntil: "domcontentloaded" });
        await waitForPageText(page, "评测记录", 15000);
        const runCard = page.getByTestId(`prompt-evaluation-run-${queued.run.id}`);
        await expect(runCard).toContainText("智能体执行 · 失败", { timeout: 15000 });
        await expect(runCard).toContainText("模型额度不足");
        const evidencePanel = runCard.getByTestId(`run-evidence-${queued.run.id}`);
        await expect(evidencePanel.getByTestId("run-evidence-external-failure")).toContainText("外部依赖失败：模型额度不足", { timeout: 15000 });
        await expect(evidencePanel.getByTestId("run-evidence-metric-失败原因")).toContainText("模型额度不足");
        await expect(evidencePanel.getByText("暂无 token 用量")).toBeVisible();
      } else {
        expect(["通过", "未通过", "需人工复核"]).toContain(syncedRun.status);
        const token = api.getToken();
        expect(token).toBeTruthy();
        await authenticateBrowserSession(page, token!, workspace.slug);
        await page.goto(`/${workspace.slug}/training/evaluation-runs?run=${queued.run.id}`, { waitUntil: "domcontentloaded" });
        await waitForPageText(page, "评测记录", 15000);
        const runCard = page.getByTestId(`prompt-evaluation-run-${queued.run.id}`);
        await expect(runCard).toContainText("智能体执行", { timeout: 15000 });
        const evidencePanel = runCard.getByTestId(`run-evidence-${queued.run.id}`);
        await expect(evidencePanel).toContainText(queued.task_id, { timeout: 15000 });

        const usage = evidence.task_usage.find((item) => item.task_id === queued.task_id && item.model === EXPECTED_AGENT_MODEL);
        if (usage) {
          expect(usage).toMatchObject({
            provider: EXPECTED_AGENT_PROVIDER,
            model: EXPECTED_AGENT_MODEL,
            priced: true,
          });
          expect(usage.input_tokens).toBeGreaterThan(0);
          expect(usage.output_tokens).toBeGreaterThan(0);
          expect(usage.estimated_cost).toBeGreaterThan(0);
          await expect(evidencePanel).toContainText(EXPECTED_AGENT_MODEL);
        } else {
          expect(evidence.task_usage).toHaveLength(0);
          expect(JSON.stringify(evidence.trace_events)).toContain("模型用量未返回");
          await expect(evidencePanel.getByText("暂无 token 用量")).toBeVisible();
          await expect(evidencePanel).toContainText("模型用量未返回");
        }
      }
    } finally {
      await api.cleanupPromptArtifactsByPrefix(prefix);
      await api.cleanup();
    }
  });
});
