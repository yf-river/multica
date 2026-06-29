import { describe, expect, it } from "vitest";
import {
  SKILL_SCENARIO_EVALUATION_SCHEMA,
  buildDefaultSkillScenarioPayload,
  buildSkillScenarioAssetRequest,
  isSkillScenarioPayload,
  summarizeSkillScenarioTarget,
} from "./skill-scenarios";
import type { PromptEvaluationAsset } from "../types";

describe("skill scenario evaluation payload", () => {
  it("builds a repo-local skill scenario payload with runnable case variables", () => {
    const payload = buildDefaultSkillScenarioPayload();

    expect(payload.schema).toBe(SKILL_SCENARIO_EVALUATION_SCHEMA);
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
      payload: buildDefaultSkillScenarioPayload(),
    } as unknown as PromptEvaluationAsset;
    const normal = { payload: { cases: [] } } as unknown as PromptEvaluationAsset;

    expect(summarizeSkillScenarioTarget(asset)).toBe(
      "user-center/add-api · operation · .codebuddy/skills/add-api/SKILL.md",
    );
    expect(summarizeSkillScenarioTarget(normal)).toBeNull();
  });
});
