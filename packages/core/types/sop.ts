export interface SquadSOPRun {
  current_step_key: string;
  started_at: string;
  completed_at: string | null;
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
  task_trace_total: number;
  task_trace_sample_total: number;
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

interface ObservabilityUsageBreakdown {
  "名称": string;
  model: string;
  runtime: string;
  "输入 token": number;
  "输出 token": number;
  "缓存读 token": number;
  "缓存写 token": number;
  "预估成本": number;
  "价格已知": boolean;
}
