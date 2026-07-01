import type { AgentTask, TaskTraceEvent } from "./agent";
import type { TaskMessagePayload } from "./events";
import type { Issue } from "./issue";
import type { PromptEvaluationToolCallChain, PromptEvaluationToolCallSummary } from "./prompt-evaluation";
import type { SquadSOPRun } from "./sop";

export interface IssueWakeupCommentBrief {
  id: string;
  issue_id: string;
  author_type: string;
  type: string;
  content: string;
  parent_id: string | null;
  created_at: string;
}

export interface AgentTaskArtifact {
  id: string;
  task_id: string;
  comment_id: string;
  issue_id: string;
  filename: string;
  title: string;
  kind: string;
  content_type: string;
  size_bytes: number;
  download_url: string;
  markdown_url: string;
  created_at: string;
}

export interface IssueExecutionNode {
  issue: Issue;
  tasks: AgentTask[];
  sop_runs: SquadSOPRun[];
  task_messages: TaskMessagePayload[];
  trace_events: TaskTraceEvent[];
  tool_call_chains: PromptEvaluationToolCallChain[];
  tool_call_summary: PromptEvaluationToolCallSummary[];
  artifacts?: AgentTaskArtifact[];
  wakeup_comments: IssueWakeupCommentBrief[];
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
  | "child_issue_ref"
  | "source_fetch"
  | "status_change";

export interface IssueTimelineNode {
  issue_id: string;
  root_task_id?: string;
  node_id: string;
  parent_node_id?: string;
  node_type: IssueTimelineNodeType;
  agent_id?: string;
  agent_name?: string;
  squad_id?: string;
  project_id?: string;
  child_issue_id?: string;
  status: string;
  started_at?: string;
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
  issue_id: string;
  node_count: number;
  total_duration_ms: number;
  total_input_tokens: number;
  total_output_tokens: number;
  total_cache_read_tokens: number;
  total_cache_write_tokens: number;
  message_count: number;
  agent_turn_count: number;
  trace_event_count: number;
  usage_unavailable: boolean;
  failure_summary?: string;
  acceptance_status: string;
  full_analysis_deep_link: string;
}

export interface IssueExecutionTreeResponse {
  root: IssueExecutionNode;
  summary: Record<string, number>;
  timeline_nodes?: IssueTimelineNode[];
  issue_summary?: IssueTimelineSummary;
}
