import { z } from "zod";
import type {
  Agent,
  AgentEnvResponse,
  AgentTemplate,
  AgentTemplateSummary,
  CreateAgentFromTemplateResponse,
  ListIssueSOPRunsResponse,
  ObservabilitySummary,
  Squad,
  SquadSOPRun,
} from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

// Runtime response contracts for agents.
const AgentSkillSummarySchema = z.object({
  id: NonEmptyStringSchema,
  name: z.string().default(""),
  description: z.string().default(""),
}).loose();

const AgentWireSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  runtime_id: NonEmptyStringSchema,
  name: z.string(),
  description: z.string().default(""),
  instructions: z.string().default(""),
  avatar_url: z.string().nullable().optional().transform((value) => value ?? null),
  runtime_mode: z.string().default("local"),
  runtime_config: z.record(z.string(), z.unknown()).default({}),
  custom_args: z.array(z.string()).default([]),
  has_custom_env: z.boolean().optional(),
  custom_env_key_count: z.number().optional(),
  mcp_config: z.unknown().optional(),
  mcp_config_redacted: z.boolean().optional(),
  scope: z.string().default("workspace"),
  status: z.string().default("idle"),
  max_concurrent_tasks: z.number().default(1),
  model: z.string().default(""),
  thinking_level: z.string().optional(),
  owner_id: z.string().nullable().optional().transform((value) => value ?? null),
  skills: z.array(AgentSkillSummarySchema).default([]),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
  archived_at: z.string().nullable().optional().transform((value) => value ?? null),
  archived_by: z.string().nullable().optional().transform((value) => value ?? null),
}).loose();

export const AgentSchema = AgentWireSchema.transform((wire) => {
  const safe: Record<string, unknown> = { ...wire };
  delete safe.custom_env;
  delete safe.custom_env_redacted;
  delete safe.custom_env_redacted_reason;
  return safe;
});

export const AgentListSchema = z.array(AgentSchema);

export const AgentEnvResponseSchema = z.object({
  agent_id: NonEmptyStringSchema,
  custom_env: z.record(z.string(), z.string()),
});

export const AgentTaskCancellationCountSchema = z.object({
  cancelled: z.number(),
}).loose();

export const EMPTY_AGENT: Agent = {
  id: "", workspace_id: "", runtime_id: "", name: "", description: "", instructions: "",
  avatar_url: null, runtime_mode: "local", runtime_config: {}, custom_args: [], scope: "workspace",
  status: "offline", max_concurrent_tasks: 1, model: "", owner_id: null, skills: [],
  created_at: "", updated_at: "", archived_at: null, archived_by: null,
};

export const EMPTY_AGENT_ENV_RESPONSE: AgentEnvResponse = { agent_id: "", custom_env: {} };

// ---------------------------------------------------------------------------
// Agent template catalog — `/api/agent-templates*` and the
// create-from-template response. The desktop app's create-agent picker
// reaches these endpoints, and a future server change to the template shape
// would white-screen older installed builds (#2192 pattern) without these
// parsers. Lenient by the same rules as IssueSchema above: arrays default to
// `[]`, optional fields stay optional, `.loose()` lets unknown fields pass
// through unchanged.
// ---------------------------------------------------------------------------

const AgentTemplateSkillRefSchema = z.object({
  source_url: z.string(),
  cached_name: z.string().default(""),
  cached_description: z.string().default(""),
}).loose();

const AgentTemplateSummarySchemaBase = z.object({
  slug: z.string(),
  name: z.string(),
  description: z.string().default(""),
  category: z.string().optional(),
  icon: z.string().optional(),
  accent: z.string().optional(),
  // skills MUST default to [] — picker code reads `template.skills.length`
  // and `.map(...)`, both of which crash on `undefined`. The most common
  // future drift (field renamed / wrapped) lands here.
  skills: z.array(AgentTemplateSkillRefSchema).default([]),
}).loose();

export const AgentTemplateSummarySchema = AgentTemplateSummarySchemaBase;

// List endpoint historically returns a bare array. Server could legitimately
// migrate to `{templates: [...]}` later — we accept either shape so an old
// desktop survives the upgrade.
export const AgentTemplateSummaryListSchema = z.union([
  z.array(AgentTemplateSummarySchemaBase),
  z.object({ templates: z.array(AgentTemplateSummarySchemaBase).default([]) })
    .loose()
    .transform((v) => v.templates),
]);

export const EMPTY_AGENT_TEMPLATE_SUMMARY_LIST: AgentTemplateSummary[] = [];

export const AgentTemplateSchema = AgentTemplateSummarySchemaBase.extend({
  // Detail-only field. Default "" so a malformed detail still renders the
  // header + skill list; the user just sees an empty Instructions block.
  instructions: z.string().default(""),
}).loose();

// Used as the parse fallback for `GET /api/agent-templates/:slug`. Slug comes
// from the URL, so we round-trip the requested one back into the fallback
// at the call site (see `getAgentTemplate` in client.ts).
export const EMPTY_AGENT_TEMPLATE_DETAIL: AgentTemplate = {
  slug: "",
  name: "",
  description: "",
  skills: [],
  instructions: "",
};

// `agent` is a full Agent record — schematising every field would duplicate
// a 50-field interface and bit-rot fast. We keep it loose and require only
// `id`, the one field the create-from-template flow consumes (used to
// navigate to the new agent's detail page). Downstream code already
// optional-chains the rest.
const MinimalAgentSchema = z.object({
  id: NonEmptyStringSchema,
}).loose();

export const CreateAgentFromTemplateResponseSchema = z.object({
  agent: MinimalAgentSchema,
  imported_skill_ids: z.array(z.string()).default([]),
  reused_skill_ids: z.array(z.string()).default([]),
}).loose();

// Type witness used by strict mutation parsing. It is never returned for an
// invalid response.
export const EMPTY_CREATE_AGENT_FROM_TEMPLATE_RESPONSE: CreateAgentFromTemplateResponse = {
  agent: { id: "" } as Agent,
  imported_skill_ids: [],
  reused_skill_ids: [],
};

// Squad list responses carry lightweight membership previews used by hover
// cards. The preview fields are additive API fields, so older backends default
// cleanly to no preview instead of breaking newer frontends.
const SquadMemberPreviewSchema = z.object({
  member_type: z.string(),
  member_id: z.string(),
  role: z.string().default(""),
}).loose();

export const SquadSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  name: z.string(),
  description: z.string().default(""),
  instructions: z.string().default(""),
  sop_profile: z.record(z.string(), z.unknown()).default({}),
  avatar_url: z.string().nullable().optional().transform((v) => v ?? null),
  scope: z.enum(["workspace", "personal"]).default("workspace"),
  leader_id: z.string(),
  creator_id: z.string(),
  created_at: z.string(),
  updated_at: z.string(),
  archived_at: z.string().nullable().optional().transform((v) => v ?? null),
  archived_by: z.string().nullable().optional().transform((v) => v ?? null),
  member_count: z.number().default(0),
  member_preview: z.array(SquadMemberPreviewSchema).default([]),
}).loose();

export const SquadListSchema = z.array(SquadSchema);
export const EMPTY_SQUAD_LIST: Squad[] = [];
export const EMPTY_SQUAD: Squad = {
  id: "",
  workspace_id: "",
  name: "",
  description: "",
  instructions: "",
  sop_profile: {},
  avatar_url: null,
  scope: "workspace",
  leader_id: "",
  creator_id: "",
  created_at: "",
  updated_at: "",
  archived_at: null,
  archived_by: null,
  member_count: 0,
  member_preview: [],
};

const SOPStepEventSchema = z.object({
  id: z.string(),
  run_id: z.string(),
  workspace_id: z.string(),
  issue_id: z.string(),
  squad_id: z.string(),
  step_key: z.string().default(""),
  step_name: z.string().default(""),
  role_key: z.string().default(""),
  event_type: z.string().default("追加证据"),
  status: z.string().default(""),
  evidence: z.record(z.string(), z.unknown()).default({}),
  reason: z.string().default(""),
  duration_ms: z.number().nullable().optional().transform((v) => v ?? null),
  created_by_type: z.string().default(""),
  created_by_id: z.string().nullable().optional().transform((v) => v ?? null),
  task_id: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string(),
  metrics: z.record(z.string(), z.unknown()).default({}),
}).loose();

export const SquadSOPRunSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  issue_id: z.string(),
  squad_id: z.string(),
  leader_task_id: z.string().nullable().optional().transform((v) => v ?? null),
  profile_key: z.string().default(""),
  profile: z.record(z.string(), z.unknown()).default({}),
  status: z.string().default("进行中"),
  current_step_key: z.string().default(""),
  started_at: z.string(),
  completed_at: z.string().nullable().optional().transform((v) => v ?? null),
  total_duration_ms: z.number().nullable().optional().transform((v) => v ?? null),
  metrics: z.record(z.string(), z.unknown()).default({}),
  events: z.array(SOPStepEventSchema).default([]),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

export const IssueSOPRunsResponseSchema = z.object({
  items: z.array(SquadSOPRunSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_SQUAD_SOP_RUN: SquadSOPRun = {
  id: "",
  workspace_id: "",
  issue_id: "",
  squad_id: "",
  leader_task_id: null,
  profile_key: "",
  profile: {},
  status: "进行中",
  current_step_key: "",
  started_at: "",
  completed_at: null,
  total_duration_ms: null,
  metrics: {},
  events: [],
  created_at: "",
  updated_at: "",
};

export const EMPTY_ISSUE_SOP_RUNS_RESPONSE: ListIssueSOPRunsResponse = {
  items: [],
  total: 0,
};

const ObservabilityUsageBreakdownSchema = z.object({
  "名称": z.string().default(""),
  provider: z.string().default(""),
  model: z.string().default(""),
  runtime: z.string().default(""),
  "输入 token": z.number().default(0),
  "输出 token": z.number().default(0),
  "缓存读 token": z.number().default(0),
  "缓存写 token": z.number().default(0),
  "任务数": z.number().default(0),
  "预估成本": z.number().default(0),
  "价格已知": z.boolean().default(false),
}).loose();

const ObservabilitySOPStageBreakdownSchema = z.object({
  step_key: z.string().default(""),
  step_name: z.string().default(""),
  role_key: z.string().default(""),
  status: z.string().default(""),
  duration_ms: z.number().default(0),
  event_count: z.number().default(0),
  evidence_count: z.number().default(0),
  task_count: z.number().default(0),
  message_count: z.number().default(0),
  agent_turn_count: z.number().default(0),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
}).loose();

export const ObservabilitySummarySchema = z.object({
  指标: z.record(z.string(), z.unknown()).default({}),
  sop_status_counts: z.record(z.string(), z.number()).default({}),
  squad_counts: z.record(z.string(), z.number()).default({}),
  project_counts: z.record(z.string(), z.number()).default({}),
  issue_counts: z.record(z.string(), z.number()).default({}),
  task_trace_total: z.number().default(0),
  sop_run_sample_total: z.number().default(0),
  task_trace_sample_total: z.number().default(0),
  sample_limit: z.number().default(0),
  sop_run_maybe_truncated: z.boolean().default(false),
  task_trace_maybe_truncated: z.boolean().default(false),
  summary_completeness: z.object({
    状态: z.string().default("完整"),
    说明: z.string().default("当前筛选条件下的 SOP 执行和任务观测已按全量汇总。"),
    采样上限: z.number().default(0),
    "SOP 执行样本数": z.number().default(0),
    "任务观测样本数": z.number().default(0),
    "SOP 执行可能截断": z.boolean().default(false),
    "任务观测可能截断": z.boolean().default(false),
  }).loose().default({
    状态: "完整",
    说明: "当前筛选条件下的 SOP 执行和任务观测已按全量汇总。",
    采样上限: 0,
    "SOP 执行样本数": 0,
    "任务观测样本数": 0,
    "SOP 执行可能截断": false,
    "任务观测可能截断": false,
  }),
  model_breakdown: z.array(ObservabilityUsageBreakdownSchema).default([]),
  runtime_breakdown: z.array(ObservabilityUsageBreakdownSchema).default([]),
  sop_stage_breakdown: z.array(ObservabilitySOPStageBreakdownSchema).default([]),
}).loose();

export const EMPTY_OBSERVABILITY_SUMMARY: ObservabilitySummary = {
  指标: {},
  sop_status_counts: {},
  squad_counts: {},
  project_counts: {},
  issue_counts: {},
  task_trace_total: 0,
  sop_run_sample_total: 0,
  task_trace_sample_total: 0,
  sample_limit: 0,
  sop_run_maybe_truncated: false,
  task_trace_maybe_truncated: false,
  summary_completeness: {
    状态: "完整",
    说明: "当前筛选条件下的 SOP 执行和任务观测已按全量汇总。",
    采样上限: 0,
    "SOP 执行样本数": 0,
    "任务观测样本数": 0,
    "SOP 执行可能截断": false,
    "任务观测可能截断": false,
  },
  model_breakdown: [],
  runtime_breakdown: [],
  sop_stage_breakdown: [],
};
