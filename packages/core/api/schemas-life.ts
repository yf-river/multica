// 本文件定义「生活伴侣（Life companion）」相关接口响应的 zod 校验 schema 及空值兜底常量。
import { z } from "zod";
import type {
  CompanionProfileEnvelope,
  LifeChronicleListResponse,
  LifeExperimentListResponse,
  LifeMemoryListResponse,
  LifeProposalListResponse,
  LifeProactiveCheckListResponse,
	LifeIdentityListResponse, LifeRelationshipListResponse, LifeMaterialListResponse, LifeInternalThoughtListResponse, LifeTopicListResponse,
	LifeCommitmentListResponse, LifeObserverListResponse, LifeObservationSeatResponse, LifeModuleListResponse,
	LifeCognitionJobListResponse, LifeUpgradeEvaluationListResponse,
} from "../types";
const NonEmptyStringSchema = z.string().min(1);

/** 校验伴侣（Companion）档案：workspace、用户、代理三个 id 都非空。 */
export const CompanionProfileSchema = z.object({
  workspace_id: NonEmptyStringSchema,
  user_id: NonEmptyStringSchema,
  agent_id: NonEmptyStringSchema,
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

/** 校验伴侣档案信封响应：内层 profile 允许为 null。 */
export const CompanionProfileEnvelopeSchema = z.object({
  profile: CompanionProfileSchema.nullable(),
}).loose();

// 单条记忆证据：来源类型、来源 id、摘录、立场与观察时间。
const LifeMemoryEvidenceSchema = z.object({
  source_type: z.string(),
  source_id: NonEmptyStringSchema,
  excerpt: z.string().default(""),
	stance: z.enum(["supports", "contradicts", "context"]).default("supports"),
  observed_at: z.string().default(""),
}).loose();

/** 校验一条生活记忆（Life memory）：含置信度、紧迫度、有效期、证据列表等字段。 */
export const LifeMemorySchema = z.object({
  id: NonEmptyStringSchema,
  kind: z.string(),
  status: z.string(),
  content: z.string(),
  confidence: z.number(),
  urgency: z.number(),
  uncertainty: z.string().default(""),
  valid_from: z.string().nullable().default(null),
  valid_to: z.string().nullable().default(null),
  confirmed_at: z.string().nullable().default(null),
  created_by_type: z.string(),
  created_by_id: NonEmptyStringSchema,
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
  evidence: z.array(LifeMemoryEvidenceSchema).default([]),
}).loose();

/** 校验记忆列表响应：包一层 memories 数组。 */
export const LifeMemoryListSchema = z.object({ memories: z.array(LifeMemorySchema).default([]) }).loose();

// 提案（Proposal）的负载：实验 id、问题、假设、计划、起止时间、关联记忆 id 等。
const LifeProposalPayloadSchema = z.object({
  experiment_id: z.string().optional(),
  previous_round_id: z.string().optional(),
  problem: z.string().default(""),
  hypothesis: z.string().default(""),
  method: z.record(z.string(), z.unknown()).default({}),
  plan: z.record(z.string(), z.unknown()).default({}),
  starts_at: z.string().default(""),
  ends_at: z.string().default(""),
  memory_ids: z.array(z.string()).default([]),
  issue_title: z.string().default(""),
  issue_description: z.string().optional(),
	action_title: z.string().optional(), action_instructions: z.string().optional(),
	module_name: z.string().optional(), module_id: z.string().optional(), module_definition: z.record(z.string(), z.unknown()).optional(), source_experiment_id: z.string().optional(),
	memory_id: z.string().optional(), memory_action: z.string().optional(), memory_kind: z.string().optional(), memory_content: z.string().optional(), memory_confidence: z.number().optional(), memory_urgency: z.number().optional(), memory_uncertainty: z.string().optional(),
	project_title: z.string().optional(), project_description: z.string().optional(), stable_core: z.record(z.string(), z.unknown()).optional(), relationship_contract: z.record(z.string(), z.unknown()).optional(), growth_profile: z.record(z.string(), z.unknown()).optional(), expression_profile: z.record(z.string(), z.unknown()).optional(), interests: z.array(z.string()).optional(), change_reason: z.string().optional(),
}).loose();

/** 校验一条生活提案：含类型、状态、标题、摘要、负载与各阶段时间戳。 */
export const LifeProposalSchema = z.object({
  id: NonEmptyStringSchema,
  proposal_type: z.string(), status: z.string(), title: z.string(), summary: z.string().default(""),
  payload: LifeProposalPayloadSchema,
  expires_at: z.string().nullable().default(null), confirmed_at: z.string().nullable().default(null),
  executed_at: z.string().nullable().default(null), created_at: z.string().default(""), updated_at: z.string().default(""),
	execution_receipt: z.record(z.string(), z.unknown()).optional(),
}).loose();
/** 校验提案列表响应。 */
export const LifeProposalListSchema = z.object({ proposals: z.array(LifeProposalSchema).default([]) }).loose();

// 实验（Experiment）定义：标题、问题、假设、方法。
const LifeExperimentSchema = z.object({
  id: NonEmptyStringSchema, title: z.string(), problem: z.string(), hypothesis: z.string(),
  method: z.record(z.string(), z.unknown()).default({}), created_at: z.string().default(""),
}).loose();
/** 校验实验轮次（Round）：含实验 id、轮次状态、计划、起止/停止时间与复盘内容。 */
export const LifeExperimentRoundSchema = z.object({
  id: NonEmptyStringSchema, experiment_id: NonEmptyStringSchema,
  previous_round_id: z.string().nullable().default(null), proposal_id: z.string().nullable().default(null),
  issue_id: z.string().nullable().default(null), status: z.string(),
  plan: z.record(z.string(), z.unknown()).default({}), starts_at: z.string().nullable().default(null),
  ends_at: z.string().nullable().default(null), stopped_at: z.string().nullable().default(null),
  stop_reason: z.string().default(""), review: z.record(z.string(), z.string()).optional(),
	review_draft: z.object({ outcome: z.string().optional(), feelings: z.string().optional(), burden: z.string().optional(), companion_correction: z.string().optional(), module_proposal: z.record(z.string(), z.unknown()).optional() }).optional(),
	observations: z.array(z.object({ id: NonEmptyStringSchema, observation_type: z.string(), content: z.string(), captured_by: z.string(), observed_at: z.string() }).loose()).default([]),
  reviewed_at: z.string().nullable().default(null), created_at: z.string().default(""),
}).loose();
/** 校验实验列表响应：同时包含 experiments 与 rounds 两个数组。 */
export const LifeExperimentListSchema = z.object({
  experiments: z.array(LifeExperimentSchema).default([]), rounds: z.array(LifeExperimentRoundSchema).default([]),
}).loose();
/** 校验一条编年史（Chronicle）条目：记录某时间段的客观事实、感受、当时与后来的理解、行动等。 */
export const LifeChronicleEntrySchema = z.object({
  id: NonEmptyStringSchema, period_start: z.string(), period_end: z.string(), facts: z.string(), feelings: z.string().default(""),
  understanding_then: z.string().default(""), understanding_later: z.string().default(""),
  actions: z.string().default(""),
  evidence: z.array(z.object({ source_type: z.string(), source_id: NonEmptyStringSchema }).loose()).default([]),
  created_at: z.string().default(""), updated_at: z.string().default(""),
	period_kind: z.string().default("event"), generated_by: z.string().default("user"), revision: z.number().default(1),
}).loose();
/** 校验编年史列表响应。 */
export const LifeChronicleListSchema = z.object({ entries: z.array(LifeChronicleEntrySchema).default([]) }).loose();

/** 校验主动检查（Proactive check）列表：每条检查含状态、触发来源、原因、上下文快照等。 */
export const LifeProactiveCheckListSchema = z.object({
  checks: z.array(z.object({
    id: NonEmptyStringSchema, status: z.string(), trigger_source: z.string(), reason: z.string(),
    context_snapshot: z.record(z.string(), z.unknown()).default({}), checked_at: z.string().default(""),
		message: z.string().optional(),
		user_responded_at: z.string().nullable().default(null), value_assessment: z.string().default(""),
  }).loose()).default([]),
}).loose();

// 以下空值常量作为 parseWithFallback 的兜底，确保校验失败时 UI 仍有安全的空结构可渲染。
export const EMPTY_COMPANION_PROFILE: CompanionProfileEnvelope = { profile: null };
export const EMPTY_LIFE_MEMORIES: LifeMemoryListResponse = { memories: [] };
export const EMPTY_LIFE_PROPOSALS: LifeProposalListResponse = { proposals: [] };
export const EMPTY_LIFE_EXPERIMENTS: LifeExperimentListResponse = { experiments: [], rounds: [] };
export const EMPTY_LIFE_CHRONICLE: LifeChronicleListResponse = { entries: [] };
export const EMPTY_LIFE_PROACTIVE_CHECKS: LifeProactiveCheckListResponse = { checks: [] };

// 可空时间：字符串或 null，缺省为 null。
const NullableTime = z.string().nullable().default(null);
/** 校验身份版本（Identity version）列表响应：核心稳定块、关系契约、成长画像、表达画像等。 */
export const LifeIdentityListSchema = z.object({ versions: z.array(z.object({ id: NonEmptyStringSchema, version: z.number(), status: z.string(), stable_core: z.record(z.string(), z.unknown()).default({}), relationship_contract: z.record(z.string(), z.unknown()).default({}), growth_profile: z.record(z.string(), z.unknown()).default({}), expression_profile: z.record(z.string(), z.unknown()).default({}), interests: z.array(z.unknown()).default([]), change_reason: z.string().default(""), confirmed_at: NullableTime, created_at: z.string().default("") }).loose()).default([]) }).loose();
/** 校验关系事件（Relationship event）列表响应。 */
export const LifeRelationshipListSchema = z.object({ events: z.array(z.object({ id: NonEmptyStringSchema, event_type: z.string(), status: z.string(), user_position: z.string().default(""), companion_position: z.string().default(""), context: z.string().default(""), revisit_after: NullableTime, resolution: z.string().default(""), created_at: z.string().default("") }).loose()).default([]) }).loose();
/** 校验素材（Material）列表响应：来源类型、来源键、正文、元数据与发生时间。 */
export const LifeMaterialListSchema = z.object({ materials: z.array(z.object({ id: NonEmptyStringSchema, source_type: z.string(), source_key: z.string(), content: z.string(), metadata: z.record(z.string(), z.unknown()).default({}), occurred_at: z.string() }).loose()).default([]) }).loose();
// 内部思考（Internal thought）条目。
const LifeInternalThoughtSchema = z.object({ id: NonEmptyStringSchema, thought_type: z.string(), title: z.string(), content: z.string(), status: z.string(), metadata: z.record(z.string(), z.unknown()).default({}), last_developed_at: z.string(), created_at: z.string(), updated_at: z.string() }).loose();
/** 校验内部思考列表响应。 */
export const LifeInternalThoughtListSchema = z.object({ thoughts: z.array(LifeInternalThoughtSchema).default([]) }).loose();
/** 校验主题（Topic）列表响应。 */
export const LifeTopicListSchema = z.object({ topics: z.array(z.object({ id: NonEmptyStringSchema, title: z.string(), summary: z.string().default(""), status: z.string(), confidence: z.number(), uncertainty: z.string().default(""), last_observed_at: z.string().default("") }).loose()).default([]) }).loose();
/** 校验承诺（Commitment）列表响应。 */
export const LifeCommitmentListSchema = z.object({ commitments: z.array(z.object({ id: NonEmptyStringSchema, content: z.string(), status: z.string(), due_at: NullableTime, revisit_after: NullableTime, outcome: z.string().default("") }).loose()).default([]) }).loose();
/** 校验主动策略（Proactive policy）：开关、时区、静默时段、最小检查间隔等。 */
export const LifeProactivePolicySchema = z.object({ enabled: z.boolean(), timezone: z.string(), quiet_hours: z.record(z.string(), z.unknown()).default({}), minimum_interval_hours: z.number(), next_check_at: z.string(), unanswered_count: z.number() }).loose();
// 观察者知识条目。
const LifeObserverKnowledgeSchema = z.object({ id: NonEmptyStringSchema, title: z.string(), content: z.string(), source: z.string().default("") }).loose();
/** 校验观察者（Observer）列表响应。 */
export const LifeObserverListSchema = z.object({ observers: z.array(z.object({ id: NonEmptyStringSchema, agent_id: NonEmptyStringSchema, name: z.string(), basis_type: z.string(), status: z.string(), current_version: z.number(), next_run_at: z.string(), last_run_at: NullableTime, personality: z.record(z.string(), z.unknown()).default({}), perspective: z.record(z.string(), z.unknown()).default({}), expression_profile: z.record(z.string(), z.unknown()).default({}), knowledge: z.array(LifeObserverKnowledgeSchema).default([]) }).loose()).default([]) }).loose();
/** 校验观察席（Observation seat）响应：判断列表 + 主题列表。 */
export const LifeObservationSeatSchema = z.object({ judgements: z.array(z.object({ id: NonEmptyStringSchema, observer_id: NonEmptyStringSchema, observer_name: z.string(), status: z.string(), title: z.string(), content: z.string(), evidence: z.array(z.unknown()).default([]), confidence: z.number(), uncertainty: z.string().default(""), published_at: NullableTime, created_at: z.string() }).loose()).default([]), topics: z.array(z.object({ id: NonEmptyStringSchema, title: z.string(), summary: z.string().default(""), status: z.string(), companion_response: z.string().default(""), surfaced_at: NullableTime, created_at: z.string() }).loose()).default([]) }).loose();
/** 校验模块（Module）列表响应。 */
export const LifeModuleListSchema = z.object({ modules: z.array(z.object({ id: NonEmptyStringSchema, name: z.string(), status: z.string(), current_version: z.number(), enabled_at: NullableTime, disabled_at: NullableTime }).loose()).default([]) }).loose();
/** 校验认知任务（Cognition job）列表响应：任务类型、状态、输入输出、重试次数等。 */
export const LifeCognitionJobListSchema = z.object({ jobs: z.array(z.object({ id: NonEmptyStringSchema, job_type: z.string(), status: z.string(), input: z.record(z.string(), z.unknown()).default({}), output: z.record(z.string(), z.unknown()).nullable().default(null), scheduled_at: z.string(), completed_at: NullableTime, error: z.string().default(""), attempt: z.number().default(0), max_attempts: z.number().default(3) }).loose()).default([]) }).loose();
/** 校验升级评估（Upgrade evaluation）列表响应。 */
export const LifeUpgradeEvaluationListSchema = z.object({ evaluations: z.array(z.object({ id: NonEmptyStringSchema, candidate_label: z.string(), baseline_label: z.string(), scenarios: z.array(z.unknown()).default([]), result: z.record(z.string(), z.unknown()).nullable().default(null), status: z.string(), rollback_recommended: z.boolean(), created_at: z.string() }).loose()).default([]) }).loose();

// 以下空值常量作为 parseWithFallback 的兜底。
export const EMPTY_LIFE_IDENTITIES: LifeIdentityListResponse = { versions: [] };
export const EMPTY_LIFE_RELATIONSHIPS: LifeRelationshipListResponse = { events: [] };
export const EMPTY_LIFE_MATERIALS: LifeMaterialListResponse = { materials: [] };
export const EMPTY_LIFE_INTERNAL_THOUGHTS: LifeInternalThoughtListResponse = { thoughts: [] };
export const EMPTY_LIFE_TOPICS: LifeTopicListResponse = { topics: [] };
export const EMPTY_LIFE_COMMITMENTS: LifeCommitmentListResponse = { commitments: [] };
export const EMPTY_LIFE_OBSERVERS: LifeObserverListResponse = { observers: [] };
export const EMPTY_LIFE_OBSERVATION_SEAT: LifeObservationSeatResponse = { judgements: [], topics: [] };
export const EMPTY_LIFE_MODULES: LifeModuleListResponse = { modules: [] };
export const EMPTY_LIFE_JOBS: LifeCognitionJobListResponse = { jobs: [] };
export const EMPTY_LIFE_UPGRADES: LifeUpgradeEvaluationListResponse = { evaluations: [] };
