import type { AgentTask, TaskTraceEvent } from "./agent";
import type { Issue } from "./issue";
import type { PromptEvaluationToolCallSummary } from "./prompt-evaluation";
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

export interface IssueExecutionNode {
  issue: Issue;
  tasks: AgentTask[];
  sop_runs: SquadSOPRun[];
  trace_events: TaskTraceEvent[];
  tool_call_summary: PromptEvaluationToolCallSummary[];
  wakeup_comments: IssueWakeupCommentBrief[];
  children: IssueExecutionNode[];
}

export interface IssueExecutionTreeResponse {
  root: IssueExecutionNode;
  summary: Record<string, number>;
}
