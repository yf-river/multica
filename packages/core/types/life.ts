export type LifeMemoryKind =
  | "current_expression"
  | "weak_signal"
  | "understanding"
  | "fact"
  | "plan"
  | "commitment";

type LifeMemoryStatus = "candidate" | "confirmed" | "archived";

export interface CompanionProfile {
  workspace_id: string;
  user_id: string;
  agent_id: string;
  created_at: string;
  updated_at: string;
}

export interface CompanionProfileEnvelope {
  profile: CompanionProfile | null;
}

interface LifeMemoryEvidence {
  source_type: string;
  source_id: string;
  excerpt: string;
	stance: "supports" | "contradicts" | "context";
  observed_at: string;
}

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

export interface LifeMemoryListResponse {
  memories: LifeMemory[];
}

export interface UpdateLifeMemoryRequest {
  kind: LifeMemoryKind;
  content: string;
  confidence: number;
  urgency: number;
  uncertainty: string;
  valid_from?: string;
  valid_to?: string;
}

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

export interface LifeProposalListResponse {
  proposals: LifeProposal[];
}

export interface LifeExperiment {
  id: string;
  title: string;
  problem: string;
  hypothesis: string;
  method: Record<string, unknown>;
  created_at: string;
}

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

export interface LifeExperimentListResponse {
  experiments: LifeExperiment[];
  rounds: LifeExperimentRound[];
}

interface LifeChronicleEvidence {
  source_type: string;
  source_id: string;
}

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

export interface LifeChronicleListResponse {
  entries: LifeChronicleEntry[];
}

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

export interface LifeIdentityVersion {
	id: string; version: number; status: string; stable_core: Record<string, unknown>;
	relationship_contract: Record<string, unknown>; growth_profile: Record<string, unknown>;
	expression_profile: Record<string, unknown>; interests: unknown[]; change_reason: string;
	confirmed_at: string | null; created_at: string;
}
export interface LifeIdentityListResponse { versions: LifeIdentityVersion[] }

interface LifeRelationshipEvent { id: string; event_type: string; status: string; user_position: string; companion_position: string; context: string; revisit_after: string | null; resolution: string; created_at: string }
export interface LifeRelationshipListResponse { events: LifeRelationshipEvent[] }

interface LifeMaterial { id: string; source_type: string; source_key: string; content: string; metadata: Record<string, unknown>; occurred_at: string }
export interface LifeMaterialListResponse { materials: LifeMaterial[] }
export interface LifeInternalThought { id: string; thought_type: string; title: string; content: string; status: string; metadata: Record<string, unknown>; last_developed_at: string; created_at: string; updated_at: string }
export interface LifeInternalThoughtListResponse { thoughts: LifeInternalThought[] }

interface LifeTopic { id: string; title: string; summary: string; status: string; confidence: number; uncertainty: string; last_observed_at: string }
export interface LifeTopicListResponse { topics: LifeTopic[] }

interface LifeCommitment { id: string; content: string; status: string; due_at: string | null; revisit_after: string | null; outcome: string }
export interface LifeCommitmentListResponse { commitments: LifeCommitment[] }

export interface LifeProactivePolicy { enabled: boolean; timezone: string; quiet_hours: Record<string, unknown>; minimum_interval_hours: number; next_check_at: string; unanswered_count: number }

interface LifeObserverKnowledge { id: string; title: string; content: string; source: string }
interface LifeObserver { id: string; agent_id: string; name: string; basis_type: string; status: string; current_version: number; next_run_at: string; last_run_at: string | null; personality: Record<string, unknown>; perspective: Record<string, unknown>; expression_profile: Record<string, unknown>; knowledge: LifeObserverKnowledge[] }
export interface LifeObserverListResponse { observers: LifeObserver[] }

interface LifeObserverJudgement { id: string; observer_id: string; observer_name: string; status: string; title: string; content: string; evidence: unknown[]; confidence: number; uncertainty: string; published_at: string | null; created_at: string }
interface LifeObservationTopic { id: string; title: string; summary: string; status: string; companion_response: string; surfaced_at: string | null; created_at: string }
export interface LifeObservationSeatResponse { judgements: LifeObserverJudgement[]; topics: LifeObservationTopic[] }

interface LifeModule { id: string; name: string; status: string; current_version: number; enabled_at: string | null; disabled_at: string | null }
export interface LifeModuleListResponse { modules: LifeModule[] }

interface LifeCognitionJob { id: string; job_type: string; status: string; input: Record<string, unknown>; output: Record<string, unknown> | null; scheduled_at: string; completed_at: string | null; error: string }
export interface LifeCognitionJobListResponse { jobs: LifeCognitionJob[] }

interface LifeUpgradeEvaluation { id: string; candidate_label: string; baseline_label: string; scenarios: unknown[]; result: Record<string, unknown> | null; status: string; rollback_recommended: boolean; created_at: string }
export interface LifeUpgradeEvaluationListResponse { evaluations: LifeUpgradeEvaluation[] }

export interface LifeProactiveCheckListResponse {
  checks: LifeProactiveCheck[];
}
