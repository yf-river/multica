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

export interface ListPromptEvaluationAssetsResponse {
  items: PromptEvaluationAsset[];
  total: number;
}

export interface ListPromptEvaluationAssetsParams {
  prompt_id?: string;
  asset_type?: PromptEvaluationAssetType;
  status?: PromptEvaluationAssetStatus;
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
