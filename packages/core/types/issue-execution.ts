// 本文件定义「议题执行详情」相关类型：单个议题从任务、消息、trace 事件、
// 工具调用链到执行树/时间线的完整展开结构。

import type { AgentTask } from "./agent";
import type { TaskMessagePayload } from "./events";
import type { Issue } from "./issue";

/** 任务执行过程中的一条 trace 事件（某次模型调用的阶段记录）。 */
export interface TaskTraceEvent {
  id: string;
  task_id: string;
  agent_id: string;
  runtime_id: string | null;
  source: string;
  event_type: string;
  event_name: string;
  status: string;
  duration_ms?: number | null;
  queue_wait_ms?: number | null;
  run_ms?: number | null;
  total_ms?: number | null;
  provider: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  failure_reason: string;
  error_type: string;
  metadata: Record<string, unknown>;
  created_at: string;
}

/** 一次 agent 工具调用的请求/响应链。 */
export interface ToolCallChain {
  id: string;
  task_id?: string;
  tool?: string;
  status: string;
  use_seq?: number;
  result_seq?: number;
  input?: Record<string, unknown>;
  output?: string;
  duration_ms?: number;
  failure_signal: boolean;
  failure_reason?: string;
  created_at?: string;
  completed_at?: string;
}

/** 任务产出的一份文件（artifact）。 */
export interface AgentTaskArtifact {
  id: string;
  filename: string;
  title: string;
  kind: string;
  download_url: string;
  markdown_url: string;
  created_at: string;
}

/** 执行树里的一个节点：一个议题 + 其任务与全部执行细节，外加子议题递归。 */
export interface IssueExecutionNode {
  issue: Issue;
  tasks: AgentTask[];
  task_messages: TaskMessagePayload[];
  trace_events: TaskTraceEvent[];
  tool_call_chains: ToolCallChain[];
  children: IssueExecutionNode[];
}

/** 议题时间线上的节点类型。 */
type IssueTimelineNodeType =
  | "agent_task"
  | "squad_step"
  | "tool_call"
  | "evidence"
  | "approval"
  | "human_confirmation"
  | "dispatch_wait"
  | "child_issue_ref"
  | "source_fetch"
  | "status_change";

/** 议题时间线上的单个节点（按执行阶段折叠成的展示单元）。 */
export interface IssueTimelineNode {
  root_task_id?: string;
  node_id: string;
  parent_node_id?: string;
  node_type: IssueTimelineNodeType;
  agent_id?: string;
  agent_name?: string;
  status: string;
  started_at?: string;
  actual_started_at?: string;
  completed_at?: string;
  duration_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  message_count: number;
  agent_turn_count: number;
  trace_event_count: number;
  usage_unavailable_trace: boolean;
  summary: string;
  evidence_refs: Record<string, unknown>[];
  artifacts?: AgentTaskArtifact[];
}

/** 议题整次执行的时间线汇总指标。 */
export interface IssueTimelineSummary {
  total_duration_ms: number;
  agent_execution_duration_ms?: number;
  human_confirmation_duration_ms?: number | null;
  child_issue_wait_duration_ms?: number | null;
  total_input_tokens: number;
  total_output_tokens: number;
  total_cache_read_tokens: number;
  total_cache_write_tokens: number;
  agent_turn_count: number;
  failure_summary?: string;
  acceptance_status: string;
}

/** 议题执行树接口响应：根节点 + 汇总 + 可选时间线。 */
export interface IssueExecutionTreeResponse {
  root: IssueExecutionNode;
  summary: Record<string, number>;
  timeline_nodes?: IssueTimelineNode[];
  issue_summary?: IssueTimelineSummary;
}

/** 运行时上下文事件（DSH context/compaction 帧）。字段随 kind 变化，保持宽松。 */
export interface TaskContextEvent {
  kind: string;
  request_id?: string;
  session_id?: string;
  seq?: number;
  at?: string;
  provider?: string;
  model?: string;
  duration_ms?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  context_window?: number;
  pressure_tokens?: number;
  projected_tokens?: number;
  breakdown?: {
    system_tokens?: number;
    tools_tokens?: number;
    message_tokens?: number;
  };
  phase?: string;
  before_tokens?: number;
  after_tokens?: number;
  reason?: string;
  [key: string]: unknown;
}

/** 单次运行的上下文遥测响应。 */
export interface TaskContextResponse {
  events: TaskContextEvent[];
  next_cursor?: string;
  has_more?: boolean;
}
