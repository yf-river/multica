import { z } from "zod";
import type {
  AgentActivityBucket,
  AgentRunCount,
  AgentTask,
  CancelTaskResponse,
  IssueExecutionTreeResponse,
  IssueTaskTraceResponse,
  IssueUsageSummary,
  TaskMessagePayload,
} from "../types";
import {
  EmbeddedAttachmentSchema,
  NonEmptyStringSchema,
  TaskTraceEventSchema,
} from "./schemas-internal";
import { EMPTY_ISSUE, IssueSchema } from "./schemas-issues";

// Task and execution responses drive presence, retries, usage totals and the
// review timeline. Identity fields are required; additive daemon/runtime data
// remains loose so Desktop clients tolerate newer servers.
export const AgentTaskSchema = z.object({
  id: NonEmptyStringSchema,
  agent_id: NonEmptyStringSchema,
  runtime_id: z.string().default(""),
  issue_id: z.string().default(""),
  status: z.string(),
  priority: z.number().default(0),
  dispatched_at: z.string().nullable().default(null),
  started_at: z.string().nullable().default(null),
  completed_at: z.string().nullable().default(null),
  result: z.unknown().default(null),
  error: z.string().nullable().default(null),
  failure_reason: z.string().optional(),
  created_at: z.string().default(""),
  chat_session_id: z.string().optional(),
  autopilot_run_id: z.string().optional(),
  parent_task_id: z.string().optional(),
  is_leader_task: z.boolean().optional(),
  attempt: z.number().optional(),
  trigger_comment_id: z.string().optional(),
  trigger_summary: z.string().optional(),
  kind: z.string().optional(),
  work_dir: z.string().optional(),
  relative_work_dir: z.string().optional(),
}).loose();

export const AgentTaskListSchema = z.array(AgentTaskSchema);

export const EMPTY_AGENT_TASK: AgentTask = {
  id: "",
  agent_id: "",
  runtime_id: "",
  issue_id: "",
  status: "cancelled",
  priority: 0,
  dispatched_at: null,
  started_at: null,
  completed_at: null,
  result: null,
  error: null,
  created_at: "",
};

const CancelledChatMessageSchema = z.object({
  chat_session_id: NonEmptyStringSchema,
  message_id: NonEmptyStringSchema,
  content: z.string(),
  restore_to_input: z.boolean().default(false),
  attachments: z.array(EmbeddedAttachmentSchema).optional(),
}).loose();

export const CancelTaskResponseSchema = AgentTaskSchema.extend({
  cancelled_chat_message: CancelledChatMessageSchema.nullish()
    .transform((value) => value ?? undefined),
}).loose();

export const EMPTY_CANCEL_TASK_RESPONSE: CancelTaskResponse = EMPTY_AGENT_TASK;

export const AgentActivityBucketListSchema = z.array(z.object({
  agent_id: NonEmptyStringSchema,
  bucket_at: z.string(),
  task_count: z.number(),
  failed_count: z.number(),
}).loose());

export const EMPTY_AGENT_ACTIVITY_BUCKETS: AgentActivityBucket[] = [];

export const AgentRunCountListSchema = z.array(z.object({
  agent_id: NonEmptyStringSchema,
  run_count: z.number(),
}).loose());

export const EMPTY_AGENT_RUN_COUNTS: AgentRunCount[] = [];

export const TaskMessageListSchema = z.array(z.object({
  task_id: NonEmptyStringSchema,
  issue_id: z.string().default(""),
  chat_session_id: z.string().optional(),
  seq: z.number(),
  type: z.string(),
  tool: z.string().optional(),
  content: z.string().optional(),
  input: z.record(z.string(), z.unknown()).optional(),
  output: z.string().optional(),
  created_at: z.string().optional(),
}).loose());

export const EMPTY_TASK_MESSAGES: TaskMessagePayload[] = [];

export const IssueUsageSummarySchema = z.object({
  total_input_tokens: z.number().default(0),
  total_output_tokens: z.number().default(0),
  total_cache_read_tokens: z.number().default(0),
  total_cache_write_tokens: z.number().default(0),
  task_count: z.number().default(0),
}).loose();

export const EMPTY_ISSUE_USAGE_SUMMARY: IssueUsageSummary = {
  total_input_tokens: 0,
  total_output_tokens: 0,
  total_cache_read_tokens: 0,
  total_cache_write_tokens: 0,
  task_count: 0,
};

export const IssueTaskTraceResponseSchema = z.object({
  events: z.array(TaskTraceEventSchema).default([]),
}).loose();

export const EMPTY_ISSUE_TASK_TRACE_RESPONSE: IssueTaskTraceResponse = { events: [] };

const ArtifactSchema = z.object({
  id: NonEmptyStringSchema,
  task_id: NonEmptyStringSchema,
  comment_id: z.string(),
  issue_id: NonEmptyStringSchema,
  filename: z.string(),
  title: z.string(),
  kind: z.string(),
  content_type: z.string(),
  size_bytes: z.number(),
  download_url: z.string(),
  markdown_url: z.string(),
  created_at: z.string(),
}).loose();

const TimelineNodeSchema = z.object({
  issue_id: NonEmptyStringSchema,
  node_id: NonEmptyStringSchema,
  node_type: z.string(),
  status: z.string(),
  duration_ms: z.number().default(0),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  message_count: z.number().default(0),
  agent_turn_count: z.number().default(0),
  trace_event_count: z.number().default(0),
  usage_unavailable_trace: z.boolean().default(false),
  summary: z.string().default(""),
  evidence_refs: z.array(z.object({
    type: z.string(),
    id: z.string(),
    href: z.string().optional(),
  }).loose()).default([]),
  artifacts: z.array(ArtifactSchema).default([]),
  metadata: z.record(z.string(), z.unknown()).optional(),
}).loose();

const IssueTimelineSummarySchema = z.object({
  issue_id: NonEmptyStringSchema,
  node_count: z.number().default(0),
  total_duration_ms: z.number().default(0),
  wall_clock_duration_ms: z.number().nullable().optional(),
  agent_execution_duration_ms: z.number().default(0),
  human_confirmation_duration_ms: z.number().nullable().optional(),
  child_issue_wait_duration_ms: z.number().nullable().optional(),
  total_input_tokens: z.number().default(0),
  total_output_tokens: z.number().default(0),
  total_cache_read_tokens: z.number().default(0),
  total_cache_write_tokens: z.number().default(0),
  message_count: z.number().default(0),
  agent_turn_count: z.number().default(0),
  trace_event_count: z.number().default(0),
  usage_unavailable: z.boolean().default(false),
  acceptance_status: z.string().default(""),
  full_analysis_deep_link: z.string().default(""),
}).loose();

const IssueExecutionNodeSchema: z.ZodTypeAny = z.lazy(() => z.object({
  issue: IssueSchema,
  tasks: AgentTaskListSchema.default([]),
  sop_runs: z.array(z.unknown()).default([]),
  task_messages: TaskMessageListSchema.default([]),
  trace_events: z.array(TaskTraceEventSchema).default([]),
  tool_call_chains: z.array(z.unknown()).default([]),
  tool_call_summary: z.array(z.unknown()).default([]),
  artifacts: z.array(ArtifactSchema).default([]),
  wakeup_comments: z.array(z.unknown()).default([]),
  children: z.array(IssueExecutionNodeSchema).default([]),
}).loose());

export const IssueExecutionTreeResponseSchema = z.object({
  root: IssueExecutionNodeSchema,
  summary: z.record(z.string(), z.number()).default({}),
  timeline_nodes: z.array(TimelineNodeSchema).default([]),
  issue_summary: IssueTimelineSummarySchema,
}).loose();

export const EMPTY_ISSUE_EXECUTION_TREE: IssueExecutionTreeResponse = {
  root: {
    issue: EMPTY_ISSUE,
    tasks: [],
    sop_runs: [],
    task_messages: [],
    trace_events: [],
    tool_call_chains: [],
    tool_call_summary: [],
    artifacts: [],
    wakeup_comments: [],
    children: [],
  },
  summary: {},
  timeline_nodes: [],
  issue_summary: {
    issue_id: "",
    node_count: 0,
    total_duration_ms: 0,
    wall_clock_duration_ms: null,
    agent_execution_duration_ms: 0,
    human_confirmation_duration_ms: null,
    child_issue_wait_duration_ms: null,
    total_input_tokens: 0,
    total_output_tokens: 0,
    total_cache_read_tokens: 0,
    total_cache_write_tokens: 0,
    message_count: 0,
    agent_turn_count: 0,
    trace_event_count: 0,
    usage_unavailable: false,
    acceptance_status: "",
    full_analysis_deep_link: "",
  },
};
