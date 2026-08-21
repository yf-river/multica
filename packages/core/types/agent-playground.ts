export interface AgentPlaygroundExperiment {
  id: string;
  workspace_id: string;
  name: string;
  description: string;
  dataset_asset_id: string | null;
  dataset_version_id: string | null;
  judge_agent_id: string | null;
  status: string;
  created_by: string | null;
  created_at: string;
  updated_at: string;
  input_count: number;
  agent_count: number;
}

export interface AgentPlaygroundInput {
  id: string;
  row_index: number;
  name: string;
  input: string;
  variables: Record<string, unknown>;
  expected: string;
  dataset_row_id: string | null;
  created_at: string;
}

export interface AgentPlaygroundAgent {
  id: string;
  agent_id: string;
  agent_name: string;
  agent_model: string | null;
  display_order: number;
}

export interface AgentPlaygroundResult {
  id: string;
  input_id: string;
  experiment_agent_id: string;
  agent_id: string;
  chat_session_id: string | null;
  task_id: string | null;
  rendered_input: string;
  status: string;
  output: string;
  error: string;
  started_at: string | null;
  completed_at: string | null;
  updated_at: string;
}

export interface AgentPlaygroundJudgement {
  id: string;
  input_id: string;
  judge_agent_id: string;
  chat_session_id: string | null;
  task_id: string | null;
  status: string;
  output: string;
  updated_at: string;
}

export interface AgentPlaygroundDetail {
  experiment: AgentPlaygroundExperiment;
  inputs: AgentPlaygroundInput[];
  agents: AgentPlaygroundAgent[];
  results: AgentPlaygroundResult[];
  judgements: AgentPlaygroundJudgement[];
}

export interface ListAgentPlaygroundExperimentsResponse {
  items: AgentPlaygroundExperiment[];
  total: number;
}

export interface CreateAgentPlaygroundExperimentRequest {
  name: string;
  description?: string;
  dataset_asset_id: string;
  dataset_version_id: string;
  judge_agent_id?: string;
  agent_ids: string[];
}

export interface JudgeAgentPlaygroundExperimentRequest {
  judge_agent_id?: string;
}
