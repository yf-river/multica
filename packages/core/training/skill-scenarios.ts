import type { CreatePromptEvaluationAssetRequest, PromptEvaluationAsset, PromptEvaluationAssetType } from "../types";

export const SKILL_SCENARIO_EVALUATION_SCHEMA = "multica.skill_scenario_eval.v1";

export type SkillScenarioRole = "sop" | "operation";

export type SkillScenarioTarget = {
  kind: "repo_skill";
  repo_path: string;
  branch: string;
  skill_path: string;
  skill_role: SkillScenarioRole;
};

export type SkillScenarioPayload = {
  schema_version: 1;
  schema: typeof SKILL_SCENARIO_EVALUATION_SCHEMA;
  target: SkillScenarioTarget;
  scenario: {
    project: string;
    task_type: string;
    task_input: string;
    allowed_scope: string[];
    forbidden_scope: string[];
    expected_artifacts: string[];
    verification_commands: string[];
  };
  rubric: Array<{
    key: string;
    label: string;
    pass: string;
  }>;
  cases: Array<{
    case_name: string;
    variables: Record<string, unknown>;
    expected_contains: string[];
    tags: string[];
  }>;
};

export const DEFAULT_SKILL_SCENARIO_RUBRIC = [
  { key: "context", label: "上下文读取", pass: "只读取项目要求的 AGENTS、harness、阶段产物和 skill 文件。" },
  { key: "boundary", label: "边界遵守", pass: "不越权修改其他项目，不读取禁读材料。" },
  { key: "artifacts", label: "产物完整", pass: "按 skill 约定生成阶段产物、operation 记录或代码变更摘要。" },
  { key: "verification", label: "验证执行", pass: "执行或明确记录必要验证命令，失败时说明 blocker。" },
  { key: "evidence", label: "证据留存", pass: "留下文件、命令、trace、handoff 或 re-eval 证据。" },
] as const;

type SkillScenarioPayloadOverrides = {
  target?: Partial<SkillScenarioTarget>;
  scenario?: Partial<SkillScenarioPayload["scenario"]>;
  rubric?: SkillScenarioPayload["rubric"];
  cases?: SkillScenarioPayload["cases"];
};

export function buildDefaultSkillScenarioPayload(overrides: SkillScenarioPayloadOverrides = {}): SkillScenarioPayload {
  const target = {
    kind: "repo_skill" as const,
    repo_path: "/data/ida/user-center",
    branch: "current-checkout",
    skill_path: ".codebuddy/skills/add-api/SKILL.md",
    skill_role: "operation" as const,
    ...overrides.target,
  };
  const scenario = {
    project: "user-center",
    task_type: "add-api",
    task_input: "新增或修改 user-center API，并按项目 harness 完成实现、测试和证据记录。",
    allowed_scope: [
      "proto/user_center.proto",
      "internal/server/",
      "usercenter/",
      "internal/logic/",
      "internal/dao/",
      "internal/models/",
      "migrations/",
      "pb/usercenterpb/",
      "openapi.yaml",
      "同包 *_test.go",
    ],
    forbidden_scope: [
      "/root/go/pkg/mod/",
      "benchmark evaluator 文件",
      "历史 run",
      "expected.md / eval.md / task.yaml",
      "gateway、ida-deployment 等其他项目源码",
    ],
    expected_artifacts: [
      "operation-contract 或等价契约记录",
      "实现改动摘要",
      "验证命令和结果",
      "阻塞项或 handoff 状态",
    ],
    verification_commands: ["go test ./...", "按 harness/testing.md 执行 V0/V1/V2/V3 中适用的验证"],
    ...overrides.scenario,
  };
  const cases = overrides.cases ?? [
    {
      case_name: "user-center add-api 场景基线",
      variables: {
        project: scenario.project,
        task_type: scenario.task_type,
        repo_path: target.repo_path,
        branch: target.branch,
        skill_path: target.skill_path,
        skill_role: target.skill_role,
        task_input: scenario.task_input,
        allowed_scope: scenario.allowed_scope,
        forbidden_scope: scenario.forbidden_scope,
        expected_artifacts: scenario.expected_artifacts,
        verification_commands: scenario.verification_commands,
      },
      expected_contains: ["AGENTS.md", "harness", "验证", "证据", "边界"],
      tags: ["skill-scenario", scenario.project, scenario.task_type, target.skill_role],
    },
  ];
  return {
    schema_version: 1,
    schema: SKILL_SCENARIO_EVALUATION_SCHEMA,
    target,
    scenario,
    rubric: overrides.rubric ?? [...DEFAULT_SKILL_SCENARIO_RUBRIC],
    cases,
  };
}

export function buildSkillScenarioAssetRequest(
  assetType: Extract<PromptEvaluationAssetType, "数据集" | "测试套件"> = "数据集",
): CreatePromptEvaluationAssetRequest {
  const payload = buildDefaultSkillScenarioPayload();
  const suffix = new Date().toLocaleString("zh-CN");
  return {
    prompt_id: null,
    name: `${payload.scenario.project} ${payload.scenario.task_type} Skill 场景 ${assetType} ${suffix}`,
    description: `场景化评估 ${payload.target.skill_path} 在 ${payload.scenario.project}/${payload.scenario.task_type} 任务中的表现。`,
    asset_type: assetType,
    payload,
    status: "启用",
  };
}

export function isSkillScenarioPayload(payload: unknown): payload is SkillScenarioPayload {
  return Boolean(
    payload &&
      typeof payload === "object" &&
      (payload as { schema?: unknown }).schema === SKILL_SCENARIO_EVALUATION_SCHEMA,
  );
}

export function isSkillScenarioAsset(asset: PromptEvaluationAsset): boolean {
  return isSkillScenarioPayload(asset.payload);
}

export function summarizeSkillScenarioTarget(asset: PromptEvaluationAsset): string | null {
  if (!isSkillScenarioPayload(asset.payload)) return null;
  const { target, scenario } = asset.payload;
  return `${scenario.project}/${scenario.task_type} · ${target.skill_role} · ${target.skill_path}`;
}
