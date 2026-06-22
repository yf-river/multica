export type SOPRunStatus = "待开始" | "进行中" | "已完成" | "已失败" | "已阻塞";

export type SOPStepEventType =
  | "步骤开始"
  | "步骤完成"
  | "步骤失败"
  | "追加证据"
  | "人工确认"
  | "测试结果"
  | "优化运行";

export interface SquadSOPStepEvent {
  id: string;
  run_id: string;
  workspace_id: string;
  issue_id: string;
  squad_id: string;
  step_key: string;
  step_name: string;
  role_key: string;
  event_type: SOPStepEventType | string;
  status: SOPRunStatus | string;
  evidence: Record<string, unknown>;
  reason: string;
  duration_ms: number | null;
  created_by_type: string;
  created_by_id: string | null;
  task_id: string | null;
  created_at: string;
  metrics: Record<string, unknown>;
}

export interface SquadSOPRun {
  id: string;
  workspace_id: string;
  issue_id: string;
  squad_id: string;
  leader_task_id: string | null;
  profile_key: string;
  profile: Record<string, unknown>;
  status: SOPRunStatus | string;
  current_step_key: string;
  started_at: string;
  completed_at: string | null;
  total_duration_ms: number | null;
  metrics: Record<string, unknown>;
  events: SquadSOPStepEvent[];
  created_at: string;
  updated_at: string;
}

export interface ListIssueSOPRunsResponse {
  items: SquadSOPRun[];
  total: number;
}

export interface CreateSOPRunRequest {
  status?: SOPRunStatus;
  current_step_key?: string;
  profile?: Record<string, unknown>;
}

export interface CreateSOPStepEventRequest {
  event_type?: SOPStepEventType;
  status?: SOPRunStatus;
  step_name?: string;
  role_key?: string;
  evidence?: Record<string, unknown>;
  reason?: string;
  duration_ms?: number;
  created_by_type?: string;
  created_by_id?: string;
  task_id?: string;
  update_run?: boolean;
}

export interface ObservabilitySummary {
  指标: {
    "SOP 执行数"?: number;
    "SOP 事件数"?: number;
    "阶段耗时"?: number | null;
    "队列等待"?: number;
    "执行耗时"?: number;
    "总耗时"?: number;
    "输入 token"?: number;
    "输出 token"?: number;
    "缓存读 token"?: number;
    "缓存写 token"?: number;
    "预估成本"?: number;
    "失败原因"?: Array<Record<string, unknown>>;
    "缺少模型价格"?: Array<Record<string, unknown>>;
    "重试次数"?: number;
    "证据数"?: number;
    "采样上限"?: number;
    "SOP 执行样本数"?: number;
    "任务观测样本数"?: number;
    "汇总完整性"?: string;
    [key: string]: unknown;
  };
  sop_status_counts: Record<string, number>;
  squad_counts: Record<string, number>;
  project_counts: Record<string, number>;
  issue_counts: Record<string, number>;
  task_trace_total: number;
  sop_run_sample_total: number;
  task_trace_sample_total: number;
  sample_limit: number;
  sop_run_maybe_truncated: boolean;
  task_trace_maybe_truncated: boolean;
  summary_completeness: {
    "状态": string;
    "说明": string;
    "采样上限": number;
    "SOP 执行样本数": number;
    "任务观测样本数": number;
    "SOP 执行可能截断": boolean;
    "任务观测可能截断": boolean;
    [key: string]: unknown;
  };
  model_breakdown: ObservabilityUsageBreakdown[];
  runtime_breakdown: ObservabilityUsageBreakdown[];
}

export interface ObservabilityUsageBreakdown {
  "名称": string;
  provider: string;
  model: string;
  runtime: string;
  "输入 token": number;
  "输出 token": number;
  "缓存读 token": number;
  "缓存写 token": number;
  "任务数": number;
  "预估成本": number;
  "价格已知": boolean;
}
