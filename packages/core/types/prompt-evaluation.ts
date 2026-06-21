export type PromptEvaluationAssetType = "数据集" | "测试套件" | "实验" | "优化运行";
export type PromptEvaluationAssetStatus = "启用" | "归档";

export interface PromptEvaluationCase {
  名称: string;
  变量: Record<string, string>;
  期望包含: string[];
}

export interface PromptEvaluationMetricSummary {
  总用例数: number;
  通过数: number;
  失败数: number;
  通过率: number;
  总耗时毫秒: number;
  平均耗时毫秒: number;
  输入token: number;
  输出token: number;
  预估成本: number;
  执行Agent: string;
  模型: string;
  runtime: string;
  "trace/task id": string;
  失败原因: string;
  评估结论: string;
}

export interface PromptEvaluationStructuredPayload extends Record<string, unknown> {
  schema_version: 1;
  语义版本: "multica.training_evaluation.v1";
  cases: PromptEvaluationCase[];
  指标口径: string[];
  最近运行?: PromptEvaluationMetricSummary;
  运行记录?: PromptEvaluationMetricSummary[];
}

export interface PromptEvaluationAsset {
  id: string;
  workspace_id: string;
  prompt_id: string | null;
  name: string;
  description: string;
  asset_type: PromptEvaluationAssetType;
  payload: Record<string, unknown> | PromptEvaluationStructuredPayload;
  status: PromptEvaluationAssetStatus;
  created_by: string | null;
  created_at: string;
  updated_at: string;
}

export interface PromptEvaluationRun {
  id: string;
  workspace_id: string;
  asset_id: string;
  prompt_id: string | null;
  run_kind: "本地渲染" | "Agent执行";
  status: "已入队" | "运行中" | "通过" | "未通过" | "失败" | "已取消";
  trigger_source: string;
  agent_id: string | null;
  runtime_id: string | null;
  task_id: string | null;
  chat_session_id: string | null;
  model: string;
  runtime_provider: string;
  total_cases: number;
  passed_cases: number;
  failed_cases: number;
  pass_rate: number;
  total_duration_ms: number;
  average_duration_ms: number;
  input_tokens: number;
  output_tokens: number;
  estimated_cost: number;
  failure_reason: string;
  conclusion: string;
  metrics: Record<string, unknown>;
  evidence: Record<string, unknown>;
  started_at: string;
  completed_at: string;
  created_by: string | null;
  created_at: string;
  updated_at: string;
}

export interface PromptEvaluationTrial {
  id: string;
  run_id: string;
  workspace_id: string;
  asset_id: string;
  case_index: number;
  case_name: string;
  status: "待执行" | "通过" | "未通过" | "失败" | "已跳过";
  input: Record<string, unknown>;
  expected: Record<string, unknown>;
  output: Record<string, unknown>;
  rendered_prompt: string;
  input_tokens: number;
  output_tokens: number;
  duration_ms: number;
  failure_reason: string;
  evidence: Record<string, unknown>;
  created_at: string;
}

export interface PromptEvaluationAgentRunResponse {
  asset: PromptEvaluationAsset;
  run: PromptEvaluationRun;
  task_id: string;
  chat_session_id: string;
  agent_id: string;
  runtime_id: string;
  model: string;
  status: string;
  message: string;
}

export interface ListPromptEvaluationAssetsResponse {
  items: PromptEvaluationAsset[];
  total: number;
}

export interface ListPromptEvaluationRunsResponse {
  items: PromptEvaluationRun[];
  total: number;
}

export interface ListPromptEvaluationTrialsResponse {
  items: PromptEvaluationTrial[];
  total: number;
}

export interface ListPromptEvaluationAssetsParams {
  prompt_id?: string;
  asset_type?: PromptEvaluationAssetType;
  status?: PromptEvaluationAssetStatus;
}

export interface ListPromptEvaluationRunsParams {
  asset_id?: string;
  status?: PromptEvaluationRun["status"];
  limit?: number;
}

export interface CreatePromptEvaluationAssetRequest {
  prompt_id?: string | null;
  name: string;
  description?: string;
  asset_type: PromptEvaluationAssetType;
  payload?: Record<string, unknown>;
  status?: PromptEvaluationAssetStatus;
}

export interface UpdatePromptEvaluationAssetRequest {
  prompt_id?: string | null;
  name?: string;
  description?: string;
  asset_type?: PromptEvaluationAssetType;
  payload?: Record<string, unknown>;
  status?: PromptEvaluationAssetStatus;
}
