import { describe, expect, it } from "vitest";
import {
  buildSkillScenarioAssetRequest,
  buildWritingModelBenchmarkAssetRequest,
  isSkillScenarioPayload,
  summarizeSkillScenarioTarget,
  summarizeWritingModelBenchmark,
} from "./skill-scenarios";
import type { PromptEvaluationAsset } from "../types";

describe("skill scenario evaluation payload", () => {
  it("builds a repo-local skill scenario payload with runnable case variables", () => {
    const request = buildSkillScenarioAssetRequest();
    expect(isSkillScenarioPayload(request.payload)).toBe(true);
    if (!isSkillScenarioPayload(request.payload)) throw new Error("expected skill scenario payload");
    const payload = request.payload;

    expect(payload.schema).toBe("multica.skill_scenario_eval.v1");
    expect(payload.target).toMatchObject({
      kind: "repo_skill",
      repo_path: "/data/ida/user-center",
      skill_path: ".codebuddy/skills/add-api/SKILL.md",
      skill_role: "operation",
    });
    expect(payload.cases[0]?.variables).toMatchObject({
      project: "user-center",
      task_type: "add-api",
      skill_path: ".codebuddy/skills/add-api/SKILL.md",
    });
    expect(payload.rubric.map((item) => item.key)).toEqual([
      "context",
      "boundary",
      "artifacts",
      "verification",
      "evidence",
    ]);
  });

  it("creates dataset or test-suite assets without requiring a prompt", () => {
    const dataset = buildSkillScenarioAssetRequest("数据集");
    const suite = buildSkillScenarioAssetRequest("测试套件");

    expect(dataset.prompt_id).toBeNull();
    expect(dataset.asset_type).toBe("数据集");
    expect(suite.asset_type).toBe("测试套件");
    expect(isSkillScenarioPayload(dataset.payload)).toBe(true);
  });

  it("summarizes only skill scenario assets", () => {
    const asset = {
      payload: buildSkillScenarioAssetRequest().payload,
    } as unknown as PromptEvaluationAsset;
    const normal = { payload: { cases: [] } } as unknown as PromptEvaluationAsset;

    expect(summarizeSkillScenarioTarget(asset)).toBe(
      "user-center/add-api · operation · .codebuddy/skills/add-api/SKILL.md",
    );
    expect(summarizeSkillScenarioTarget(normal)).toBeNull();
  });

  it("builds a multi-model writing benchmark test suite", () => {
    const request = buildWritingModelBenchmarkAssetRequest();
    const payload = request.payload as {
      schema: string;
      target_models: string[];
      cases: Array<{ scenario_key: string }>;
      rubric: Array<{ key: string }>;
    };
    const asset = { payload: request.payload } as unknown as PromptEvaluationAsset;

    expect(payload.schema).toBe("multica.writing_model_benchmark.v1");
    expect(payload.target_models).toContain("codebuddy/kimi-k2.6-ioa");
    expect(payload.target_models).toContain("codebuddy/deepseek-v4-pro");
    expect(payload.cases.map((item) => item.scenario_key)).toEqual([
      "emotional_support",
      "daily_communication",
      "creative_writing",
    ]);
    expect(payload.rubric.map((item) => item.key)).toEqual([
      "empathy",
      "naturalness",
      "style_control",
      "usefulness",
      "safety",
    ]);
    expect(request.asset_type).toBe("测试套件");
    expect(request.prompt_id).toBeNull();
    expect(summarizeWritingModelBenchmark(asset)).toBe("3 个模型 · 3 个写作用例 · 5 个评分维度");
  });
});
