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
    [key: string]: unknown;
  };
  task_trace_total: number;
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
