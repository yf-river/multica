import { test, expect } from "@playwright/test";

import { createTestApi } from "./helpers";
import type { TestApiClient } from "./fixtures";

test.describe("训练与评估 daemon 协议闭环", () => {
  let api: TestApiClient;
  let artifactPrefix: string;

  test.beforeEach(async () => {
    api = await createTestApi();
    artifactPrefix = `E2E daemon 契约 ${Date.now()}`;
    await api.cleanupPromptArtifactsByPrefix(artifactPrefix);
  });

  test.afterEach(async () => {
    if (api) {
      await api.cleanupPromptArtifactsByPrefix(artifactPrefix);
      await api.cleanup();
    }
  });

  test("daemon claim/start/usage/messages/complete 会写回训练评估证据", async () => {
    const { runtime } = await api.registerDaemonCodeBuddyRuntime(`${artifactPrefix} CodeBuddy Runtime`);

    const prompt = await api.createPromptLibraryItem({
      name: `${artifactPrefix} 提示词`,
      prompt_type: "需求澄清",
      content: "请用中文澄清 {{issue_title}}，必须输出验收条件和 trace/任务标识。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
      status: "启用",
    });
    const asset = await api.createPromptEvaluationAsset({
      prompt_id: prompt.id,
      name: `${artifactPrefix} 测试套件`,
      asset_type: "测试套件",
      payload: {
        schema_version: 1,
        schema: "multica.training_evaluation.payload.v1",
        cases: [
          {
            case_name: "daemon 协议用例",
            variables: { issue_title: "登录失败" },
            expected_contains: ["验收条件", "trace/任务标识"],
            tags: ["daemon-contract"],
          },
        ],
      },
      status: "启用",
    });

    const queued = await api.runPromptEvaluationAssetAgent(asset.id);
    expect(queued.runtime_id).toBe(runtime.id);

    const claimed = await api.claimDaemonTask(runtime.id);
    expect(claimed.task?.id).toBe(queued.task_id);

    const output = [
      "Agent 输出：完成 daemon 协议评估。",
      "```json",
      JSON.stringify({
        schema_version: 1,
        schema: "multica.training_evaluation.agent_verdict.v1",
        case_results: [
          {
            case_index: 0,
            status: "通过",
            output: { 摘要: "已输出验收条件和 trace/任务标识" },
            failure_reason: "无",
            conclusion: "daemon 协议用例通过",
            evidence: { 命中: ["验收条件", "trace/任务标识"], 缺失: [] },
          },
        ],
        summary: {
          total_cases: 1,
          passed_cases: 1,
          failed_cases: 0,
          failure_reason: "无",
          conclusion: "daemon 协议完成后自动写回训练评估证据",
          improvement_suggestions: [],
          reproducible_evidence: [queued.task_id],
        },
      }),
      "```",
    ].join("\n");

    await api.startDaemonTask(queued.task_id);
    await api.reportDaemonTaskUsage(queued.task_id, {
      provider: "codebuddy",
      model: queued.model,
      input_tokens: 21,
      output_tokens: 13,
      cache_read_tokens: 2,
      cache_write_tokens: 1,
    });
    await api.reportDaemonTaskMessages(queued.task_id, output);
    await api.completeDaemonTask(queued.task_id, output);

    const synced = await api.syncPromptEvaluationRun(queued.run.id);
    expect(synced).toMatchObject({
      status: "通过",
      passed_cases: 1,
      failed_cases: 0,
      runtime_id: runtime.id,
    });
    expect(synced.input_tokens).toBeGreaterThanOrEqual(21);
    expect(synced.output_tokens).toBeGreaterThanOrEqual(13);

    const evidence = await api.getPromptEvaluationRunEvidence(queued.run.id);
    expect(evidence.task_usage).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          task_id: queued.task_id,
          provider: "codebuddy",
          input_tokens: 21,
          output_tokens: 13,
        }),
      ]),
    );
    expect(evidence.上下文).toMatchObject({
      任务: queued.task_id,
      执行Agent: queued.agent_id,
      执行Agent名称: "Multica 训练评估 Agent",
      运行时标识: runtime.id,
      运行时名称: runtime.name,
      运行时提供方: "codebuddy",
      模型: queued.model,
      触发来源: "智能体调试场",
      提示词名称: prompt.name,
      评测资产名称: asset.name,
    });
    expect(evidence.上下文["证据完整性"]).toMatchObject({
      用例数: 1,
      任务用量条数: 1,
      任务消息条数: 1,
    });
    expect(evidence.trials[0]).toMatchObject({
      case_name: "daemon 协议用例",
      status: "通过",
    });
    expect(JSON.stringify(evidence.trials[0])).toContain("multica.training_evaluation.agent_verdict.v1");
  });
});
