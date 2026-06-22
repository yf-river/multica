import { test, expect } from "@playwright/test";
import { TestApiClient } from "./fixtures";

const RUN_REAL_AGENT_E2E = process.env.RUN_REAL_AGENT_E2E === "1";
const REAL_AGENT_ACCOUNT = process.env.REAL_AGENT_E2E_ACCOUNT || "goal-test-daemon";
const REAL_AGENT_WORKSPACE = process.env.REAL_AGENT_E2E_WORKSPACE || "goal-test-daemon";
const EXPECTED_AGENT_MODEL = process.env.MULTICA_PROMPT_EVALUATION_AGENT_MODEL || "minimax-m2.7-ioa";

test.describe("训练与评估真实 Agent 闭环", () => {
  test.skip(!RUN_REAL_AGENT_E2E, "设置 RUN_REAL_AGENT_E2E=1 后才运行真实 daemon/CodeBuddy 验收");

  test("CodeBuddy daemon 可以真实执行测试套件并写回运行证据", async () => {
    test.setTimeout(240_000);

    const api = new TestApiClient();
    const prefix = `真实Agent验收 ${Date.now()}`;
    await api.login(REAL_AGENT_ACCOUNT, "Goal Test Daemon");
    await api.ensureWorkspace("Goal Test Daemon", REAL_AGENT_WORKSPACE);
    await api.markUserOnboarded();
    await api.cleanupPromptArtifactsByPrefix(prefix);

    try {
      const readiness = await api.getPromptEvaluationRuntimeReadiness();
      expect(readiness).toMatchObject({
        status: "就绪",
        model: EXPECTED_AGENT_MODEL,
      });
      expect(readiness.runtime).toMatchObject({
        provider: "codebuddy",
        status: "online",
      });

      const prompt = await api.createPromptLibraryItem({
        name: `${prefix} 提示词`,
        description: "真实 daemon E2E：创建提示词后由 CodeBuddy 执行评估。",
        prompt_type: "需求澄清",
        content: "请用中文澄清 {{issue_title}}，必须输出验收条件、风险、trace/task id 和下一步建议。",
        variables: [{ name: "issue_title", label: "Issue 标题", required: true }],
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
              期望包含: ["验收条件", "trace/task id", "下一步"],
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
          runtime_provider: "codebuddy",
          total_cases: 1,
        })
        .then(async () => {
          const runs = await api.listPromptEvaluationRuns({ asset_id: asset.id, limit: 10 });
          return runs.find((run) => run.id === queued.run.id)!;
        });

      expect(["通过", "未通过", "需人工复核"]).toContain(terminalRun.status);
      expect(terminalRun.task_id).toBe(queued.task_id);

      const evidence = await api.getPromptEvaluationRunEvidence(queued.run.id);
      expect(evidence.run).toMatchObject({
        id: queued.run.id,
        run_kind: "Agent执行",
        model: EXPECTED_AGENT_MODEL,
        runtime_provider: "codebuddy",
        task_id: queued.task_id,
      });
      expect(evidence.trials.length).toBeGreaterThan(0);
      expect(evidence.task_messages.length).toBeGreaterThan(0);
      expect(evidence.trace_events.length).toBeGreaterThan(0);
    } finally {
      await api.cleanupPromptArtifactsByPrefix(prefix);
      await api.cleanup();
    }
  });
});
