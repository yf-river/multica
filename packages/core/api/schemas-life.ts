import { z } from "zod";
import type {
  CompanionProfileEnvelope,
  LifeChronicleListResponse,
  LifeExperimentListResponse,
  LifeMemoryListResponse,
  LifeProposalListResponse,
  LifeProactiveCheckListResponse,
	LifeIdentityListResponse, LifeRelationshipListResponse, LifeMaterialListResponse, LifeInternalThought, LifeInternalThoughtListResponse, LifeTopicListResponse,
	LifeCommitmentListResponse, LifeObserverListResponse, LifeObservationSeatResponse, LifeModuleListResponse,
	LifeCognitionJobListResponse, LifeUpgradeEvaluationListResponse,
} from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

export const CompanionProfileSchema = z.object({
  workspace_id: NonEmptyStringSchema,
  user_id: NonEmptyStringSchema,
  agent_id: NonEmptyStringSchema,
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const CompanionProfileEnvelopeSchema = z.object({
  profile: CompanionProfileSchema.nullable(),
}).loose();

const LifeMemoryEvidenceSchema = z.object({
  source_type: z.string(),
  source_id: NonEmptyStringSchema,
  excerpt: z.string().default(""),
	stance: z.enum(["supports", "contradicts", "context"]).default("supports"),
  observed_at: z.string().default(""),
}).loose();

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

export const LifeMemoryListSchema = z.object({ memories: z.array(LifeMemorySchema).default([]) }).loose();

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

export const LifeProposalSchema = z.object({
  id: NonEmptyStringSchema,
  proposal_type: z.string(), status: z.string(), title: z.string(), summary: z.string().default(""),
  payload: LifeProposalPayloadSchema,
  expires_at: z.string().nullable().default(null), confirmed_at: z.string().nullable().default(null),
  executed_at: z.string().nullable().default(null), created_at: z.string().default(""), updated_at: z.string().default(""),
	execution_receipt: z.record(z.string(), z.unknown()).optional(),
}).loose();
export const LifeProposalListSchema = z.object({ proposals: z.array(LifeProposalSchema).default([]) }).loose();

const LifeExperimentSchema = z.object({
  id: NonEmptyStringSchema, title: z.string(), problem: z.string(), hypothesis: z.string(),
  method: z.record(z.string(), z.unknown()).default({}), created_at: z.string().default(""),
}).loose();
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
export const LifeExperimentListSchema = z.object({
  experiments: z.array(LifeExperimentSchema).default([]), rounds: z.array(LifeExperimentRoundSchema).default([]),
}).loose();
export const LifeChronicleEntrySchema = z.object({
  id: NonEmptyStringSchema, period_start: z.string(), period_end: z.string(), facts: z.string(), feelings: z.string().default(""),
  understanding_then: z.string().default(""), understanding_later: z.string().default(""),
  actions: z.string().default(""),
  evidence: z.array(z.object({ source_type: z.string(), source_id: NonEmptyStringSchema }).loose()).default([]),
  created_at: z.string().default(""), updated_at: z.string().default(""),
	period_kind: z.string().default("event"), generated_by: z.string().default("user"), revision: z.number().default(1),
}).loose();
export const LifeChronicleListSchema = z.object({ entries: z.array(LifeChronicleEntrySchema).default([]) }).loose();

export const LifeProactiveCheckListSchema = z.object({
  checks: z.array(z.object({
    id: NonEmptyStringSchema, status: z.string(), trigger_source: z.string(), reason: z.string(),
    context_snapshot: z.record(z.string(), z.unknown()).default({}), checked_at: z.string().default(""),
		message: z.string().optional(),
		user_responded_at: z.string().nullable().default(null), value_assessment: z.string().default(""),
  }).loose()).default([]),
}).loose();

export const EMPTY_COMPANION_PROFILE: CompanionProfileEnvelope = { profile: null };
export const EMPTY_LIFE_MEMORIES: LifeMemoryListResponse = { memories: [] };
export const EMPTY_LIFE_PROPOSALS: LifeProposalListResponse = { proposals: [] };
export const EMPTY_LIFE_EXPERIMENTS: LifeExperimentListResponse = { experiments: [], rounds: [] };
export const EMPTY_LIFE_CHRONICLE: LifeChronicleListResponse = { entries: [] };
export const EMPTY_LIFE_PROACTIVE_CHECKS: LifeProactiveCheckListResponse = { checks: [] };

const NullableTime = z.string().nullable().default(null);
export const LifeIdentityListSchema = z.object({ versions: z.array(z.object({ id: NonEmptyStringSchema, version: z.number(), status: z.string(), stable_core: z.record(z.string(), z.unknown()).default({}), relationship_contract: z.record(z.string(), z.unknown()).default({}), growth_profile: z.record(z.string(), z.unknown()).default({}), expression_profile: z.record(z.string(), z.unknown()).default({}), interests: z.array(z.unknown()).default([]), change_reason: z.string().default(""), confirmed_at: NullableTime, created_at: z.string().default("") }).loose()).default([]) }).loose();
export const LifeRelationshipListSchema = z.object({ events: z.array(z.object({ id: NonEmptyStringSchema, event_type: z.string(), status: z.string(), user_position: z.string().default(""), companion_position: z.string().default(""), context: z.string().default(""), revisit_after: NullableTime, resolution: z.string().default(""), created_at: z.string().default("") }).loose()).default([]) }).loose();
export const LifeMaterialListSchema = z.object({ materials: z.array(z.object({ id: NonEmptyStringSchema, source_type: z.string(), source_key: z.string(), content: z.string(), metadata: z.record(z.string(), z.unknown()).default({}), occurred_at: z.string() }).loose()).default([]) }).loose();
const LifeInternalThoughtSchema: z.ZodType<LifeInternalThought> = z.object({ id: NonEmptyStringSchema, thought_type: z.string(), title: z.string(), content: z.string(), status: z.string(), metadata: z.record(z.string(), z.unknown()).default({}), last_developed_at: z.string(), created_at: z.string(), updated_at: z.string() }).loose();
export const LifeInternalThoughtListSchema = z.object({ thoughts: z.array(LifeInternalThoughtSchema).default([]) }).loose();
export const LifeTopicListSchema = z.object({ topics: z.array(z.object({ id: NonEmptyStringSchema, title: z.string(), summary: z.string().default(""), status: z.string(), confidence: z.number(), uncertainty: z.string().default(""), last_observed_at: z.string().default("") }).loose()).default([]) }).loose();
export const LifeCommitmentListSchema = z.object({ commitments: z.array(z.object({ id: NonEmptyStringSchema, content: z.string(), status: z.string(), due_at: NullableTime, revisit_after: NullableTime, outcome: z.string().default("") }).loose()).default([]) }).loose();
export const LifeProactivePolicySchema = z.object({ enabled: z.boolean(), timezone: z.string(), quiet_hours: z.record(z.string(), z.unknown()).default({}), minimum_interval_hours: z.number(), next_check_at: z.string(), unanswered_count: z.number() }).loose();
const LifeObserverKnowledgeSchema = z.object({ id: NonEmptyStringSchema, title: z.string(), content: z.string(), source: z.string().default("") }).loose();
export const LifeObserverListSchema = z.object({ observers: z.array(z.object({ id: NonEmptyStringSchema, agent_id: NonEmptyStringSchema, name: z.string(), basis_type: z.string(), status: z.string(), current_version: z.number(), next_run_at: z.string(), last_run_at: NullableTime, personality: z.record(z.string(), z.unknown()).default({}), perspective: z.record(z.string(), z.unknown()).default({}), expression_profile: z.record(z.string(), z.unknown()).default({}), knowledge: z.array(LifeObserverKnowledgeSchema).default([]) }).loose()).default([]) }).loose();
export const LifeObservationSeatSchema = z.object({ judgements: z.array(z.object({ id: NonEmptyStringSchema, observer_id: NonEmptyStringSchema, observer_name: z.string(), status: z.string(), title: z.string(), content: z.string(), evidence: z.array(z.unknown()).default([]), confidence: z.number(), uncertainty: z.string().default(""), published_at: NullableTime, created_at: z.string() }).loose()).default([]), topics: z.array(z.object({ id: NonEmptyStringSchema, title: z.string(), summary: z.string().default(""), status: z.string(), companion_response: z.string().default(""), surfaced_at: NullableTime, created_at: z.string() }).loose()).default([]) }).loose();
export const LifeModuleListSchema = z.object({ modules: z.array(z.object({ id: NonEmptyStringSchema, name: z.string(), status: z.string(), current_version: z.number(), enabled_at: NullableTime, disabled_at: NullableTime }).loose()).default([]) }).loose();
export const LifeCognitionJobListSchema = z.object({ jobs: z.array(z.object({ id: NonEmptyStringSchema, job_type: z.string(), status: z.string(), input: z.record(z.string(), z.unknown()).default({}), output: z.record(z.string(), z.unknown()).nullable().default(null), scheduled_at: z.string(), completed_at: NullableTime, error: z.string().default("") }).loose()).default([]) }).loose();
export const LifeUpgradeEvaluationListSchema = z.object({ evaluations: z.array(z.object({ id: NonEmptyStringSchema, candidate_label: z.string(), baseline_label: z.string(), scenarios: z.array(z.unknown()).default([]), result: z.record(z.string(), z.unknown()).nullable().default(null), status: z.string(), rollback_recommended: z.boolean(), created_at: z.string() }).loose()).default([]) }).loose();

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
