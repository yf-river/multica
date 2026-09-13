// 本文件定义「生活伴侣（Life companion）」模块的类型：记忆（memory）、
// 建议（proposal）、实验（experiment）、编年史（chronicle）、主动关怀、
// 身份版本、关系事件、观察者、模块与认知任务等。

/** 生活记忆的种类。 */
export type LifeMemoryKind =
  | "current_expression"
  | "weak_signal"
  | "understanding"
  | "fact"
  | "plan"
  | "commitment";

/** 生活记忆的生命周期状态。 */
type LifeMemoryStatus = "candidate" | "confirmed" | "archived";

/** 伴侣（companion）档案：某个用户绑定了一个 agent 作为生活伴侣。 */
export interface CompanionProfile {
  workspace_id: string;
  user_id: string;
  agent_id: string;
  created_at: string;
  updated_at: string;
}

/** 伴侣档案的信封响应：profile 可为 null（还没绑定）。 */
export interface CompanionProfileEnvelope {
  profile: CompanionProfile | null;
}

/** 单条记忆的证据：来源、摘录、立场与观察时间。 */
interface LifeMemoryEvidence {
  source_type: string;
  source_id: string;
  excerpt: string;
  stance: "supports" | "contradicts" | "context";
  observed_at: string;
}

/** 一条生活记忆。 */
export interface LifeMemory {
  id: string;
  kind: LifeMemoryKind;
  status: LifeMemoryStatus;
  content: string;
  confidence: number;
  urgency: number;
  uncertainty: string;
  valid_from: string | null;
  valid_to: string | null;
  confirmed_at: string | null;
  created_by_type: string;
  created_by_id: string;
  created_at: string;
  updated_at: string;
  evidence: LifeMemoryEvidence[];
}

/** 生活记忆列表接口响应。 */
export interface LifeMemoryListResponse {
  memories: LifeMemory[];
}

/** 更新一条生活记忆的请求体。 */
export interface UpdateLifeMemoryRequest {
  kind: LifeMemoryKind;
  content: string;
  confidence: number;
  urgency: number;
  uncertainty: string;
  valid_from?: string;
  valid_to?: string;
}

/** 生活建议的载荷：不同 proposal_type 使用不同的可选字段子集。 */
interface LifeProposalPayload {
  experiment_id?: string;
  previous_round_id?: string;
  problem: string;
  hypothesis: string;
  method: Record<string, unknown>;
  plan: Record<string, unknown>;
  starts_at: string;
  ends_at: string;
  memory_ids: string[];
  issue_title: string;
  issue_description?: string;
  action_title?: string;
  action_instructions?: string;
  module_name?: string;
  module_id?: string;
  module_definition?: Record<string, unknown>;
  source_experiment_id?: string;
  memory_id?: string;
  memory_action?: string;
  memory_kind?: string;
  memory_content?: string;
  memory_confidence?: number;
  memory_urgency?: number;
  memory_uncertainty?: string;
  project_title?: string;
  project_description?: string;
  stable_core?: Record<string, unknown>;
  relationship_contract?: Record<string, unknown>;
  growth_profile?: Record<string, unknown>;
  expression_profile?: Record<string, unknown>;
  interests?: string[];
  change_reason?: string;
}

/** 一条生活建议（提案给用户执行的具体行动）。 */
export interface LifeProposal {
  id: string;
  proposal_type: string;
  status: string;
  title: string;
  summary: string;
  payload: LifeProposalPayload;
  expires_at: string | null;
  confirmed_at: string | null;
  executed_at: string | null;
  execution_receipt?: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

/** 生活建议列表接口响应。 */
export interface LifeProposalListResponse {
  proposals: LifeProposal[];
}

/** 一个生活实验（为验证假设而设计的行动方案）。 */
export interface LifeExperiment {
  id: string;
  title: string;
  problem: string;
  hypothesis: string;
  method: Record<string, unknown>;
  created_at: string;
}

/** 实验的一轮执行（含复盘 review）。 */
export interface LifeExperimentRound {
  id: string;
  experiment_id: string;
  previous_round_id: string | null;
  proposal_id: string | null;
  issue_id: string | null;
  status: string;
  plan: Record<string, unknown>;
  starts_at: string | null;
  ends_at: string | null;
  stopped_at: string | null;
  stop_reason: string;
  review?: {
    outcome: string;
    feelings: string;
    burden: string;
    companion_correction: string;
  };
  review_draft?: {
    outcome?: string;
    feelings?: string;
    burden?: string;
    companion_correction?: string;
    module_proposal?: Record<string, unknown>;
  };
  observations: Array<{ id: string; observation_type: string; content: string; captured_by: string; observed_at: string }>;
  reviewed_at: string | null;
  created_at: string;
}

/** 生活实验列表接口响应（含全部轮次）。 */
export interface LifeExperimentListResponse {
  experiments: LifeExperiment[];
  rounds: LifeExperimentRound[];
}

/** 编年史条目引用的一条证据。 */
interface LifeChronicleEvidence {
  source_type: string;
  source_id: string;
}

/** 一条生活编年史条目（按周期总结事实、感受与后来的理解）。 */
export interface LifeChronicleEntry {
  id: string;
  period_start: string;
  period_end: string;
  facts: string;
  feelings: string;
  understanding_then: string;
  understanding_later: string;
  actions: string;
  evidence: LifeChronicleEvidence[];
  created_at: string;
  updated_at: string;
  period_kind: string;
  generated_by: string;
  revision: number;
}

/** 编年史列表接口响应。 */
export interface LifeChronicleListResponse {
  entries: LifeChronicleEntry[];
}

/** 一次主动关怀检查。 */
interface LifeProactiveCheck {
  id: string;
  status: string;
  trigger_source: string;
  reason: string;
  context_snapshot: Record<string, unknown>;
  checked_at: string;
  message?: string;
  user_responded_at: string | null;
  value_assessment: string;
}

/** 「人生身份」的一个版本。 */
export interface LifeIdentityVersion {
  id: string; version: number; status: string; stable_core: Record<string, unknown>;
  relationship_contract: Record<string, unknown>; growth_profile: Record<string, unknown>;
  expression_profile: Record<string, unknown>; interests: unknown[]; change_reason: string;
  confirmed_at: string | null; created_at: string;
}
/** 身份版本列表接口响应。 */
export interface LifeIdentityListResponse { versions: LifeIdentityVersion[] }

/** 一条人际关系事件。 */
interface LifeRelationshipEvent { id: string; event_type: string; status: string; user_position: string; companion_position: string; context: string; revisit_after: string | null; resolution: string; created_at: string }
/** 关系事件列表接口响应。 */
export interface LifeRelationshipListResponse { events: LifeRelationshipEvent[] }

/** 一条生活素材（原始内容）。 */
interface LifeMaterial { id: string; source_type: string; source_key: string; content: string; metadata: Record<string, unknown>; occurred_at: string }
/** 素材列表接口响应。 */
export interface LifeMaterialListResponse { materials: LifeMaterial[] }
/** 一条内部想法。 */
export interface LifeInternalThought { id: string; thought_type: string; title: string; content: string; status: string; metadata: Record<string, unknown>; last_developed_at: string; created_at: string; updated_at: string }
/** 内部想法列表接口响应。 */
export interface LifeInternalThoughtListResponse { thoughts: LifeInternalThought[] }

/** 一个生活主题。 */
interface LifeTopic { id: string; title: string; summary: string; status: string; confidence: number; uncertainty: string; last_observed_at: string }
/** 主题列表接口响应。 */
export interface LifeTopicListResponse { topics: LifeTopic[] }

/** 一条生活承诺。 */
interface LifeCommitment { id: string; content: string; status: string; due_at: string | null; revisit_after: string | null; outcome: string }
/** 承诺列表接口响应。 */
export interface LifeCommitmentListResponse { commitments: LifeCommitment[] }

/** 「主动关怀」策略配置。 */
export interface LifeProactivePolicy { enabled: boolean; timezone: string; quiet_hours: Record<string, unknown>; minimum_interval_hours: number; next_check_at: string; unanswered_count: number }

/** 观察者掌握的一条知识。 */
interface LifeObserverKnowledge { id: string; title: string; content: string; source: string }
/** 一个生活观察者（观察生活并产出判断的 agent 视角）。 */
interface LifeObserver { id: string; agent_id: string; name: string; basis_type: string; status: string; current_version: number; next_run_at: string; last_run_at: string | null; personality: Record<string, unknown>; perspective: Record<string, unknown>; expression_profile: Record<string, unknown>; knowledge: LifeObserverKnowledge[] }
/** 观察者列表接口响应。 */
export interface LifeObserverListResponse { observers: LifeObserver[] }

/** 观察者产出的判断。 */
interface LifeObserverJudgement { id: string; observer_id: string; observer_name: string; status: string; title: string; content: string; evidence: unknown[]; confidence: number; uncertainty: string; published_at: string | null; created_at: string }
/** 观察主题。 */
interface LifeObservationTopic { id: string; title: string; summary: string; status: string; companion_response: string; surfaced_at: string | null; created_at: string }
/** 「观察席」占用情况响应。 */
export interface LifeObservationSeatResponse { judgements: LifeObserverJudgement[]; topics: LifeObservationTopic[] }

/** 一个生活模块（开/关状态）。 */
interface LifeModule { id: string; name: string; status: string; current_version: number; enabled_at: string | null; disabled_at: string | null }
/** 模块列表接口响应。 */
export interface LifeModuleListResponse { modules: LifeModule[] }

/** 一个认知任务。 */
interface LifeCognitionJob { id: string; job_type: string; status: string; input: Record<string, unknown>; output: Record<string, unknown> | null; scheduled_at: string; completed_at: string | null; error: string; attempt: number; max_attempts: number }
/** 认知任务列表接口响应。 */
export interface LifeCognitionJobListResponse { jobs: LifeCognitionJob[] }

/** 一次升级评估。 */
interface LifeUpgradeEvaluation { id: string; candidate_label: string; baseline_label: string; scenarios: unknown[]; result: Record<string, unknown> | null; status: string; rollback_recommended: boolean; created_at: string }
/** 升级评估列表接口响应。 */
export interface LifeUpgradeEvaluationListResponse { evaluations: LifeUpgradeEvaluation[] }

/** 主动关怀检查列表接口响应。 */
export interface LifeProactiveCheckListResponse {
  checks: LifeProactiveCheck[];
}
