import type { AgentTask, TaskTraceEvent } from "./agent";
import type { TaskMessagePayload } from "./events";
import type { Issue } from "./issue";
import type { PromptEvaluationToolCallChain } from "./prompt-evaluation";

export interface AgentTaskArtifact {
  id: string;
  filename: string;
  title: string;
  kind: string;
  download_url: string;
  markdown_url: string;
  created_at: string;
}

export interface IssueExecutionNode {
  issue: Issue;
  tasks: AgentTask[];
  task_messages: TaskMessagePayload[];
  trace_events: TaskTraceEvent[];
  tool_call_chains: PromptEvaluationToolCallChain[];
  children: IssueExecutionNode[];
}

export interface IssueTimelineEvidenceRef {
  type: string;
  id: string;
  href?: string;
}

export type IssueTimelineNodeType =
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

export interface IssueTimelineNode {
  root_task_id?: string;
  node_id: string;
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
  evidence_refs: IssueTimelineEvidenceRef[];
  artifacts?: AgentTaskArtifact[];
}

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

export interface IssueExecutionTreeResponse {
  root: IssueExecutionNode;
  summary: Record<string, number>;
  timeline_nodes?: IssueTimelineNode[];
  issue_summary?: IssueTimelineSummary;
}
