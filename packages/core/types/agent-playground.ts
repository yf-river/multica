export interface AgentPlaygroundExperiment {
  id: string;
  name: string;
  status: string;
  input_count: number;
  agent_count: number;
}

export interface AgentPlaygroundInput {
  id: string;
  row_index: number;
  name: string;
  input: string;
}

export interface AgentPlaygroundAgent {
  id: string;
  agent_name: string;
}

export interface AgentPlaygroundResult {
  input_id: string;
  experiment_agent_id: string;
  task_id: string | null;
  status: string;
  output: string;
  error: string;
}

export interface AgentPlaygroundJudgement {
  input_id: string;
  task_id: string | null;
  status: string;
  output: string;
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
