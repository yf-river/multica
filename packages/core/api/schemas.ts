import { z } from "zod";
import type {
  Agent,
  AgentTemplate,
  AgentTemplateSummary,
  Attachment,
  CancelTaskResponse,
  CreateAgentFromTemplateResponse,
  GroupedIssuesResponse,
  ListIssuesResponse,
  ListPromptEvaluationAssetsResponse,
  ListPromptEvaluationOptimizationCandidatesResponse,
  ListPromptLibraryItemsResponse,
  ListIssueSOPRunsResponse,
  ObservabilitySummary,
  ListWebhookDeliveriesResponse,
  PromptEvaluationAsset,
  PromptEvaluationEvidenceSnapshot,
  PromptEvaluationRunEvidence,
  PromptEvaluationRuntimeReadiness,
  PromptEvaluationSummary,
  PromptEvaluationStructuredCase,
  PromptEvaluationOptimizationCandidate,
  ListPromptEvaluationEvidenceSnapshotsResponse,
  PromptLibraryItem,
  PublishPromptEvaluationOptimizationCandidateResponse,
  Squad,
  SquadSOPRun,
  TimelineEntry,
  User,
  WebhookDelivery,
} from "../types";

export interface AppConfigResponse {
  cdn_domain: string;
  // True when the CDN domain serves private content via time-bounded signed
  // URLs (CloudFront signing) — raw storage URLs on that domain are NOT
  // publicly fetchable and must not be used as native media sources
  // (MUL-3254). Older servers omit the field; treat that as false.
  cdn_signed?: boolean;
  allow_signup: boolean;
  posthog_key?: string;
  posthog_host?: string;
  analytics_environment?: string;
  daemon_server_url?: string;
  daemon_app_url?: string;
  workspace_creation_disabled?: boolean;
}

// ---------------------------------------------------------------------------
// Schemas for the highest-risk API endpoints — those whose responses drive
// the issue detail page (timeline, comments, subscribers) and the issues
// list. These are the surfaces that white-screened in #2143 / #2147 / #2192.
//
// These schemas are intentionally LENIENT:
//   - String enums are stored as `z.string()` rather than `z.enum([...])`.
//     A new server-side enum value should render as a generic fallback in
//     the UI, never crash a `safeParse`.
//   - Optional fields are unioned with `null` and given fallbacks where
//     existing UI code already coerces them.
//   - Arrays default to `[]` so a missing `reactions` / `attachments` /
//     `entries` field doesn't take the page down.
//   - Every object schema ends with `.loose()` so unknown server-side
//     fields pass through unchanged. zod 4's `.object()` defaults to STRIP,
//     which would silently delete fields the schema didn't explicitly list
//     — fine while the TS type doesn't claim them, but the moment a future
//     PR adds a TS field without updating the schema, the cast `as T` lies
//     and the field shows up as `undefined` at runtime. `.loose()` removes
//     that synchronisation hazard.
//
// These schemas are deliberately not typed as `z.ZodType<TimelineEntry>` /
// `z.ZodType<Issue>` etc. — the strict TS types narrow string fields to
// literal unions, which would defeat the leniency above. `parseWithFallback`
// returns the parsed value cast to the caller-supplied `T`, so the strict
// type still flows out at the call site; the schema only guards shape.
// ---------------------------------------------------------------------------

const ReactionSchema = z.object({
  id: z.string(),
  comment_id: z.string(),
  actor_type: z.string(),
  actor_id: z.string(),
  emoji: z.string(),
  created_at: z.string(),
});

// Nested attachments embedded in timeline/comment responses stay lenient on
// purpose: a single malformed attachment must not knock the whole timeline
// into the fallback `[]`.
const AttachmentSchema = z.object({
  id: z.string(),
}).loose();

// Standalone attachment lookup (`GET /api/attachments/{id}`) is the source of
// truth for click-time download URLs. The two fields the download flow opens
// in a new tab — `download_url` and `url` — must be strings, otherwise we'd
// happily `window.open(undefined)`. `filename` gates the toast/title and is
// also enforced so a missing value falls back to the empty record below.
//
// `markdown_url` is parsed lenient: a server old enough to predate
// MUL-3192 omits the field, in which case the schema defaults it to "".
// Callers that need to persist a URL into markdown should go through the
// `useFileUpload` helper (which falls back to the legacy
// `attachmentDownloadPath` shape when `markdown_url` is empty), so the
// empty-string default does not silently break any persistence path.
export const AttachmentResponseSchema = z.object({
  id: z.string(),
  url: z.string(),
  download_url: z.string(),
  markdown_url: z.string().optional().default(""),
  filename: z.string(),
  chat_session_id: z.string().nullable().optional(),
  chat_message_id: z.string().nullable().optional(),
}).loose();

export const EMPTY_ATTACHMENT: Attachment = {
  id: "",
  workspace_id: "",
  issue_id: null,
  comment_id: null,
  chat_session_id: null,
  chat_message_id: null,
  uploader_type: "",
  uploader_id: "",
  filename: "",
  url: "",
  download_url: "",
  markdown_url: "",
  content_type: "",
  size_bytes: 0,
  created_at: "",
};

// All object schemas use `.loose()` so unknown server-side fields pass
// through unchanged. zod 4's `.object()` defaults to STRIP, which would
// silently drop new fields and surface as a "field neither showed up in
// the UI" mystery the next time the TS type adopted them but the schema
// wasn't updated in lock-step. `.loose()` removes that synchronisation
// hazard — the schema validates the shape it knows about and leaves the
// rest alone.
const TimelineEntrySchema = z.object({
  type: z.string(),
  id: z.string(),
  actor_type: z.string(),
  actor_id: z.string(),
  created_at: z.string(),
  action: z.string().optional(),
  details: z.record(z.string(), z.unknown()).optional(),
  content: z.string().optional(),
  parent_id: z.string().nullable().optional(),
  updated_at: z.string().optional(),
  comment_type: z.string().optional(),
  reactions: z.array(ReactionSchema).optional(),
  attachments: z.array(AttachmentSchema).optional(),
  source_task_id: z.string().nullable().optional(),
  coalesced_count: z.number().optional(),
}).loose();

// /timeline returns a flat array of TimelineEntry, oldest first. The
// previously cursor-paginated wrapper was removed (#1929) — at observed data
// sizes (p99 ~30 entries per issue) paged delivery only created bugs.
export const TimelineEntriesSchema = z.array(TimelineEntrySchema);

export const EMPTY_TIMELINE_ENTRIES: TimelineEntry[] = [];

const OptionalStringSchema = z.preprocess(
  (value) => (typeof value === "string" ? value : undefined),
  z.string().optional(),
);

const BooleanWithDefaultSchema = (fallback: boolean) =>
  z.preprocess(
    (value) => (typeof value === "boolean" ? value : undefined),
    z.boolean().default(fallback),
  );

export const AppConfigSchema = z.object({
  cdn_domain: z.string().default(""),
  cdn_signed: BooleanWithDefaultSchema(false),
  allow_signup: BooleanWithDefaultSchema(true),
  posthog_key: OptionalStringSchema,
  posthog_host: OptionalStringSchema,
  analytics_environment: OptionalStringSchema,
  daemon_server_url: OptionalStringSchema,
  daemon_app_url: OptionalStringSchema,
  workspace_creation_disabled: BooleanWithDefaultSchema(false).optional(),
}).loose();

export const EMPTY_APP_CONFIG: AppConfigResponse = {
  cdn_domain: "",
  cdn_signed: false,
  allow_signup: true,
  daemon_server_url: "",
  daemon_app_url: "",
  workspace_creation_disabled: false,
};

export const CommentSchema = z.object({
  id: z.string(),
  issue_id: z.string(),
  author_type: z.string(),
  author_id: z.string(),
  content: z.string(),
  type: z.string(),
  parent_id: z.string().nullable(),
  reactions: z.array(ReactionSchema).default([]),
  attachments: z.array(AttachmentSchema).default([]),
  created_at: z.string(),
  updated_at: z.string(),
  source_task_id: z.string().nullable().optional(),
}).loose();

export const CommentsListSchema = z.array(CommentSchema);

const CommentTriggerPreviewAgentSchema = z.object({
  id: z.string(),
  name: z.string().default(""),
  avatar_url: z.string().optional(),
  source: z.string().default(""),
  reason: z.string().default(""),
}).loose();

export const CommentTriggerPreviewSchema = z.object({
  agents: z.array(CommentTriggerPreviewAgentSchema).default([]),
}).loose();

// Metadata is primitive-only by API/DB contract. Stay lenient on shape:
// unknown keys land as `unknown` to a caller, but the field itself defaults
// to {} so consumers never need to nil-guard `issue.metadata`.
const IssueMetadataSchema = z.record(z.string(), z.union([z.string(), z.number(), z.boolean()])).default({});

export const IssueSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  number: z.number(),
  identifier: z.string(),
  title: z.string(),
  description: z.string().nullable(),
  status: z.string(),
  priority: z.string(),
  assignee_type: z.string().nullable(),
  assignee_id: z.string().nullable(),
  creator_type: z.string(),
  creator_id: z.string(),
  parent_issue_id: z.string().nullable(),
  project_id: z.string().nullable(),
  position: z.number(),
  start_date: z.string().nullable(),
  due_date: z.string().nullable(),
  metadata: IssueMetadataSchema,
  reactions: z.array(z.unknown()).optional(),
  labels: z.array(z.unknown()).optional(),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

export const ListIssuesResponseSchema = z.object({
  issues: z.array(IssueSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_LIST_ISSUES_RESPONSE: ListIssuesResponse = {
  issues: [],
  total: 0,
};

const IssueAssigneeGroupSchema = z.object({
  id: z.string(),
  assignee_type: z.string().nullable(),
  assignee_id: z.string().nullable(),
  issues: z.array(IssueSchema).default([]),
  total: z.number().default(0),
}).loose();

export const GroupedIssuesResponseSchema = z.object({
  groups: z.array(IssueAssigneeGroupSchema).default([]),
}).loose();

export const EMPTY_GROUPED_ISSUES_RESPONSE: GroupedIssuesResponse = {
  groups: [],
};

const SubscriberSchema = z.object({
  issue_id: z.string(),
  user_type: z.string(),
  user_id: z.string(),
  reason: z.string(),
  created_at: z.string(),
}).loose();

export const SubscribersListSchema = z.array(SubscriberSchema);

export const ChildIssuesResponseSchema = z.object({
  issues: z.array(IssueSchema).default([]),
}).loose();

// ---------------------------------------------------------------------------
// Workspace dashboard schemas
//
// The dashboard hits three independent rollup endpoints. Each returns a flat
// array, and every field is consumed by chart / KPI math — a missing number
// silently degrades to NaN downstream, so we coerce missing numbers to 0.
// String fields default to "" (no enum narrowing) to survive future model /
// agent ID drift, and so a single null from tz-aware SQL bucketing fails
// only that row instead of dropping the whole array to the `[]` fallback.
// ---------------------------------------------------------------------------

const DashboardUsageDailySchema = z.object({
  date: z.string().default(""),
  provider: z.string().default(""),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  task_count: z.number().default(0),
}).loose();

export const DashboardUsageDailyListSchema = z.array(DashboardUsageDailySchema);

const DashboardUsageByAgentSchema = z.object({
  agent_id: z.string().default(""),
  provider: z.string().default(""),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  task_count: z.number().default(0),
}).loose();

export const DashboardUsageByAgentListSchema = z.array(DashboardUsageByAgentSchema);

const DashboardAgentRunTimeSchema = z.object({
  agent_id: z.string().default(""),
  total_seconds: z.number().default(0),
  task_count: z.number().default(0),
  failed_count: z.number().default(0),
}).loose();

export const DashboardAgentRunTimeListSchema = z.array(DashboardAgentRunTimeSchema);

const DashboardRunTimeDailySchema = z.object({
  date: z.string().default(""),
  total_seconds: z.number().default(0),
  task_count: z.number().default(0),
  failed_count: z.number().default(0),
}).loose();

export const DashboardRunTimeDailyListSchema = z.array(DashboardRunTimeDailySchema);

// ---------------------------------------------------------------------------
// Runtime usage schemas — the runtime-detail page's four usage endpoints
// (`/api/runtimes/:id/usage*`). Same leniency rules as the dashboard
// schemas above: numbers default to 0, strings to "", `.loose()` passes
// unknown fields.
// ---------------------------------------------------------------------------

const RuntimeUsageSchema = z.object({
  runtime_id: z.string().default(""),
  date: z.string().default(""),
  provider: z.string().default(""),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
}).loose();

export const RuntimeUsageListSchema = z.array(RuntimeUsageSchema);

const RuntimeHourlyActivitySchema = z.object({
  hour: z.number().default(0),
  count: z.number().default(0),
}).loose();

export const RuntimeHourlyActivityListSchema = z.array(RuntimeHourlyActivitySchema);

const RuntimeUsageByAgentSchema = z.object({
  agent_id: z.string().default(""),
  provider: z.string().default(""),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  task_count: z.number().default(0),
}).loose();

export const RuntimeUsageByAgentListSchema = z.array(RuntimeUsageByAgentSchema);

const RuntimeUsageByHourSchema = z.object({
  hour: z.number().default(0),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  task_count: z.number().default(0),
}).loose();

export const RuntimeUsageByHourListSchema = z.array(RuntimeUsageByHourSchema);

// ---------------------------------------------------------------------------
// Task cancellation (`POST /api/tasks/:id/cancel`)
//
// This response is consumed directly by chat recovery. The embedded task
// object stays loose so daemon/runtime fields can drift, but the optional
// `cancelled_chat_message` payload must be well-formed before the UI deletes
// a message from cache or restores text into the input.
// ---------------------------------------------------------------------------

const AgentTaskResponseSchema = z.object({
  id: z.string(),
  agent_id: z.string().default(""),
  runtime_id: z.string().default(""),
  issue_id: z.string().default(""),
  status: z.string().default("cancelled"),
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
  attempt: z.number().optional(),
  trigger_comment_id: z.string().optional(),
  trigger_summary: z.string().optional(),
  kind: z.string().optional(),
  work_dir: z.string().optional(),
  relative_work_dir: z.string().optional(),
}).loose();

const CancelledChatMessageSchema = z.object({
  chat_session_id: z.string(),
  message_id: z.string(),
  content: z.string(),
  restore_to_input: z.boolean().default(false),
  // Attachments detached from the deleted message so a restored draft can
  // re-bind them on re-send. Absent on servers that predate the field.
  attachments: z.array(AttachmentSchema).optional(),
}).loose();

export const CancelTaskResponseSchema = AgentTaskResponseSchema.extend({
  cancelled_chat_message: CancelledChatMessageSchema.nullish()
    .transform((value) => value ?? undefined),
}).loose();

export const EMPTY_CANCEL_TASK_RESPONSE: CancelTaskResponse = {
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

// ---------------------------------------------------------------------------
// Agent template catalog — `/api/agent-templates*` and the
// create-from-template response. The desktop app's create-agent picker
// reaches these endpoints, and a future server change to the template shape
// would white-screen older installed builds (#2192 pattern) without these
// parsers. Lenient by the same rules as IssueSchema above: arrays default to
// `[]`, optional fields stay optional, `.loose()` lets unknown fields pass
// through unchanged.
// ---------------------------------------------------------------------------

const AgentTemplateSkillRefSchema = z.object({
  source_url: z.string(),
  cached_name: z.string().default(""),
  cached_description: z.string().default(""),
}).loose();

const AgentTemplateSummarySchemaBase = z.object({
  slug: z.string(),
  name: z.string(),
  description: z.string().default(""),
  category: z.string().optional(),
  icon: z.string().optional(),
  accent: z.string().optional(),
  // skills MUST default to [] — picker code reads `template.skills.length`
  // and `.map(...)`, both of which crash on `undefined`. The most common
  // future drift (field renamed / wrapped) lands here.
  skills: z.array(AgentTemplateSkillRefSchema).default([]),
}).loose();

export const AgentTemplateSummarySchema = AgentTemplateSummarySchemaBase;

// List endpoint historically returns a bare array. Server could legitimately
// migrate to `{templates: [...]}` later — we accept either shape so an old
// desktop survives the upgrade.
export const AgentTemplateSummaryListSchema = z.union([
  z.array(AgentTemplateSummarySchemaBase),
  z.object({ templates: z.array(AgentTemplateSummarySchemaBase).default([]) })
    .loose()
    .transform((v) => v.templates),
]);

export const EMPTY_AGENT_TEMPLATE_SUMMARY_LIST: AgentTemplateSummary[] = [];

export const AgentTemplateSchema = AgentTemplateSummarySchemaBase.extend({
  // Detail-only field. Default "" so a malformed detail still renders the
  // header + skill list; the user just sees an empty Instructions block.
  instructions: z.string().default(""),
}).loose();

// Used as the parse fallback for `GET /api/agent-templates/:slug`. Slug comes
// from the URL, so we round-trip the requested one back into the fallback
// at the call site (see `getAgentTemplate` in client.ts).
export const EMPTY_AGENT_TEMPLATE_DETAIL: AgentTemplate = {
  slug: "",
  name: "",
  description: "",
  skills: [],
  instructions: "",
};

// `agent` is a full Agent record — schematising every field would duplicate
// a 50-field interface and bit-rot fast. We keep it loose and require only
// `id`, the one field the create-from-template flow consumes (used to
// navigate to the new agent's detail page). Downstream code already
// optional-chains the rest.
const MinimalAgentSchema = z.object({
  id: z.string(),
}).loose();

export const CreateAgentFromTemplateResponseSchema = z.object({
  agent: MinimalAgentSchema,
  imported_skill_ids: z.array(z.string()).default([]),
  reused_skill_ids: z.array(z.string()).default([]),
}).loose();

// Fallback when the success response fails to parse. The agent server-side
// has likely been created already, so we can't pretend nothing happened —
// the caller (`create-agent-dialog.tsx`) is responsible for noticing
// `agent.id === ""` and skipping navigation while keeping the list
// invalidation, so the user finds their new agent in the list.
export const EMPTY_CREATE_AGENT_FROM_TEMPLATE_RESPONSE: CreateAgentFromTemplateResponse = {
  agent: { id: "" } as Agent,
  imported_skill_ids: [],
  reused_skill_ids: [],
};

// Squad list responses carry lightweight membership previews used by hover
// cards. The preview fields are additive API fields, so older backends default
// cleanly to no preview instead of breaking newer frontends.
const SquadMemberPreviewSchema = z.object({
  member_type: z.string(),
  member_id: z.string(),
  role: z.string().default(""),
}).loose();

export const SquadSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  name: z.string(),
  description: z.string().default(""),
  instructions: z.string().default(""),
  sop_profile: z.record(z.string(), z.unknown()).default({}),
  avatar_url: z.string().nullable().optional().transform((v) => v ?? null),
  leader_id: z.string(),
  creator_id: z.string(),
  created_at: z.string(),
  updated_at: z.string(),
  archived_at: z.string().nullable().optional().transform((v) => v ?? null),
  archived_by: z.string().nullable().optional().transform((v) => v ?? null),
  member_count: z.number().default(0),
  member_preview: z.array(SquadMemberPreviewSchema).default([]),
}).loose();

export const SquadListSchema = z.array(SquadSchema);
export const EMPTY_SQUAD_LIST: Squad[] = [];
export const EMPTY_SQUAD: Squad = {
  id: "",
  workspace_id: "",
  name: "",
  description: "",
  instructions: "",
  sop_profile: {},
  avatar_url: null,
  leader_id: "",
  creator_id: "",
  created_at: "",
  updated_at: "",
  archived_at: null,
  archived_by: null,
  member_count: 0,
  member_preview: [],
};

const SOPStepEventSchema = z.object({
  id: z.string(),
  run_id: z.string(),
  workspace_id: z.string(),
  issue_id: z.string(),
  squad_id: z.string(),
  step_key: z.string().default(""),
  step_name: z.string().default(""),
  role_key: z.string().default(""),
  event_type: z.string().default("追加证据"),
  status: z.string().default(""),
  evidence: z.record(z.string(), z.unknown()).default({}),
  reason: z.string().default(""),
  duration_ms: z.number().nullable().optional().transform((v) => v ?? null),
  created_by_type: z.string().default(""),
  created_by_id: z.string().nullable().optional().transform((v) => v ?? null),
  task_id: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string(),
  metrics: z.record(z.string(), z.unknown()).default({}),
}).loose();

export const SquadSOPRunSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  issue_id: z.string(),
  squad_id: z.string(),
  leader_task_id: z.string().nullable().optional().transform((v) => v ?? null),
  profile_key: z.string().default(""),
  profile: z.record(z.string(), z.unknown()).default({}),
  status: z.string().default("进行中"),
  current_step_key: z.string().default(""),
  started_at: z.string(),
  completed_at: z.string().nullable().optional().transform((v) => v ?? null),
  total_duration_ms: z.number().nullable().optional().transform((v) => v ?? null),
  metrics: z.record(z.string(), z.unknown()).default({}),
  events: z.array(SOPStepEventSchema).default([]),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

export const IssueSOPRunsResponseSchema = z.object({
  items: z.array(SquadSOPRunSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_SQUAD_SOP_RUN: SquadSOPRun = {
  id: "",
  workspace_id: "",
  issue_id: "",
  squad_id: "",
  leader_task_id: null,
  profile_key: "",
  profile: {},
  status: "进行中",
  current_step_key: "",
  started_at: "",
  completed_at: null,
  total_duration_ms: null,
  metrics: {},
  events: [],
  created_at: "",
  updated_at: "",
};

export const EMPTY_ISSUE_SOP_RUNS_RESPONSE: ListIssueSOPRunsResponse = {
  items: [],
  total: 0,
};

const ObservabilityUsageBreakdownSchema = z.object({
  "名称": z.string().default(""),
  provider: z.string().default(""),
  model: z.string().default(""),
  runtime: z.string().default(""),
  "输入 token": z.number().default(0),
  "输出 token": z.number().default(0),
  "缓存读 token": z.number().default(0),
  "缓存写 token": z.number().default(0),
  "任务数": z.number().default(0),
  "预估成本": z.number().default(0),
  "价格已知": z.boolean().default(false),
}).loose();

export const ObservabilitySummarySchema = z.object({
  指标: z.record(z.string(), z.unknown()).default({}),
  sop_status_counts: z.record(z.string(), z.number()).default({}),
  squad_counts: z.record(z.string(), z.number()).default({}),
  project_counts: z.record(z.string(), z.number()).default({}),
  issue_counts: z.record(z.string(), z.number()).default({}),
  task_trace_total: z.number().default(0),
  sop_run_sample_total: z.number().default(0),
  task_trace_sample_total: z.number().default(0),
  sample_limit: z.number().default(500),
  sop_run_maybe_truncated: z.boolean().default(false),
  task_trace_maybe_truncated: z.boolean().default(false),
  summary_completeness: z.object({
    状态: z.string().default("完整"),
    说明: z.string().default("当前筛选条件下的 SOP 执行和任务观测未达到采样上限。"),
    采样上限: z.number().default(500),
    "SOP 执行样本数": z.number().default(0),
    "任务观测样本数": z.number().default(0),
    "SOP 执行可能截断": z.boolean().default(false),
    "任务观测可能截断": z.boolean().default(false),
  }).loose().default({
    状态: "完整",
    说明: "当前筛选条件下的 SOP 执行和任务观测未达到采样上限。",
    采样上限: 500,
    "SOP 执行样本数": 0,
    "任务观测样本数": 0,
    "SOP 执行可能截断": false,
    "任务观测可能截断": false,
  }),
  model_breakdown: z.array(ObservabilityUsageBreakdownSchema).default([]),
  runtime_breakdown: z.array(ObservabilityUsageBreakdownSchema).default([]),
}).loose();

export const EMPTY_OBSERVABILITY_SUMMARY: ObservabilitySummary = {
  指标: {},
  sop_status_counts: {},
  squad_counts: {},
  project_counts: {},
  issue_counts: {},
  task_trace_total: 0,
  sop_run_sample_total: 0,
  task_trace_sample_total: 0,
  sample_limit: 500,
  sop_run_maybe_truncated: false,
  task_trace_maybe_truncated: false,
  summary_completeness: {
    状态: "完整",
    说明: "当前筛选条件下的 SOP 执行和任务观测未达到采样上限。",
    采样上限: 500,
    "SOP 执行样本数": 0,
    "任务观测样本数": 0,
    "SOP 执行可能截断": false,
    "任务观测可能截断": false,
  },
  model_breakdown: [],
  runtime_breakdown: [],
};

const PromptLibraryVariableSchema = z.object({
  name: z.string(),
  label: z.string().optional(),
  required: z.boolean().optional(),
  description: z.string().optional(),
  default_value: z.string().optional(),
}).loose();

export const PromptLibraryItemSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  project_id: z.string().nullable().optional().transform((v) => v ?? null),
  name: z.string(),
  description: z.string().default(""),
  prompt_type: z.string().default("通用"),
  content: z.string(),
  variables: z.array(PromptLibraryVariableSchema).default([]),
  tags: z.array(z.string()).default([]),
  status: z.enum(["启用", "归档"]).default("启用"),
  version: z.number().default(1),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

export const PromptLibraryItemListResponseSchema = z.object({
  items: z.array(PromptLibraryItemSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_PROMPT_LIBRARY_ITEM: PromptLibraryItem = {
  id: "",
  workspace_id: "",
  project_id: null,
  name: "",
  description: "",
  prompt_type: "通用",
  content: "",
  variables: [],
  tags: [],
  status: "启用",
  version: 1,
  created_by: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_PROMPT_LIBRARY_LIST_RESPONSE: ListPromptLibraryItemsResponse = {
  items: [],
  total: 0,
};

export const PromptEvaluationPayloadCaseSchema = z.object({
  case_name: z.string().min(1),
  variables: z.record(z.string(), z.unknown()).default({}),
  expected_contains: z.array(z.string()).default([]),
  tags: z.array(z.string()).default([]),
}).loose();

export const PromptEvaluationStrictPayloadSchema = z.object({
  schema_version: z.literal(1),
  schema: z.literal("multica.training_evaluation.payload.v1"),
  语义版本: z.literal("multica.training_evaluation.v1").optional(),
  cases: z.array(PromptEvaluationPayloadCaseSchema).default([]),
  payload_contract: z.record(z.string(), z.unknown()).optional(),
  metric_contract: z.array(z.string()).optional(),
}).loose();

export const PromptEvaluationPayloadSchema = z.record(z.string(), z.unknown()).default({}).superRefine((payload, ctx) => {
  if (payload.schema !== "multica.training_evaluation.payload.v1") return;
  const parsed = PromptEvaluationStrictPayloadSchema.safeParse(payload);
  if (!parsed.success) {
    ctx.addIssue({
      code: "custom",
      message: `invalid training evaluation payload: ${parsed.error.issues.map((issue) => issue.message).join("; ")}`,
    });
  }
});

export const PromptEvaluationAssetSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  prompt_id: z.string().nullable().optional().transform((v) => v ?? null),
  name: z.string(),
  description: z.string().default(""),
  asset_type: z.enum(["数据集", "测试套件", "实验", "优化运行"]),
  payload: PromptEvaluationPayloadSchema,
  status: z.enum(["启用", "归档"]).default("启用"),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string(),
  updated_at: z.string(),
  structure_schema: z.string().default("multica.training_evaluation.asset_profile.v1"),
  structured_case_count: z.number().default(0),
  structured_variable_count: z.number().default(0),
  structured_assertion_count: z.number().default(0),
  linked_dataset_count: z.number().default(0),
  linked_prompt_count: z.number().default(0),
  evaluation_dimension_count: z.number().default(0),
}).loose();

export const PromptEvaluationAssetListResponseSchema = z.object({
  items: z.array(PromptEvaluationAssetSchema).default([]),
  total: z.number().default(0),
}).loose();

export const PromptEvaluationRunSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  asset_id: z.string(),
  prompt_id: z.string().nullable().optional().transform((v) => v ?? null),
  run_kind: z.enum(["本地渲染", "模板渲染检查", "Agent执行"]).transform((value) => (value === "本地渲染" ? "模板渲染检查" : value)),
  status: z.enum(["已入队", "运行中", "通过", "未通过", "失败", "已取消", "需人工复核"]),
  trigger_source: z.string().default("手动"),
  agent_id: z.string().nullable().optional().transform((v) => v ?? null),
  runtime_id: z.string().nullable().optional().transform((v) => v ?? null),
  task_id: z.string().nullable().optional().transform((v) => v ?? null),
  chat_session_id: z.string().nullable().optional().transform((v) => v ?? null),
  model: z.string().default(""),
  runtime_provider: z.string().default(""),
  total_cases: z.number().default(0),
  passed_cases: z.number().default(0),
  failed_cases: z.number().default(0),
  pass_rate: z.number().default(0),
  total_duration_ms: z.number().default(0),
  average_duration_ms: z.number().default(0),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  estimated_cost: z.number().default(0),
  failure_reason: z.string().default(""),
  conclusion: z.string().default(""),
  metrics: z.record(z.string(), z.unknown()).default({}),
  evidence: z.record(z.string(), z.unknown()).default({}),
  started_at: z.string().default(""),
  completed_at: z.string().default(""),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const PromptEvaluationTrialSchema = z.object({
  id: z.string(),
  run_id: z.string(),
  workspace_id: z.string(),
  asset_id: z.string(),
  case_index: z.number().default(0),
  case_name: z.string().default(""),
  status: z.enum(["待执行", "通过", "未通过", "失败", "已跳过", "需人工复核"]),
  input: z.record(z.string(), z.unknown()).default({}),
  expected: z.record(z.string(), z.unknown()).default({}),
  output: z.unknown().default({}),
  rendered_prompt: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  duration_ms: z.number().default(0),
  failure_reason: z.string().default(""),
  evidence: z.record(z.string(), z.unknown()).default({}),
  created_at: z.string().default(""),
}).loose();

const PromptEvaluationTaskUsageSchema = z.object({
  id: z.string(),
  task_id: z.string(),
  provider: z.string().default(""),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  estimated_cost: z.number().optional(),
  priced: z.boolean().optional(),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

const PromptEvaluationTaskMessageSchema = z.object({
  task_id: z.string(),
  issue_id: z.string().default(""),
  chat_session_id: z.string().optional(),
  seq: z.number().default(0),
  type: z.enum(["text", "thinking", "tool_use", "tool_result", "error"]),
  tool: z.string().optional(),
  content: z.string().optional(),
  input: z.record(z.string(), z.unknown()).optional(),
  output: z.string().optional(),
  created_at: z.string().optional(),
}).loose();

const PromptEvaluationTaskTraceEventSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  task_id: z.string(),
  issue_id: z.string().nullable().optional().transform((v) => v ?? null),
  agent_id: z.string(),
  runtime_id: z.string().nullable().optional().transform((v) => v ?? null),
  squad_id: z.string().nullable().optional().transform((v) => v ?? null),
  project_id: z.string().nullable().optional().transform((v) => v ?? null),
  source: z.string().default(""),
  event_type: z.string().default(""),
  event_name: z.string().default(""),
  status: z.string().default(""),
  attempt: z.number().default(0),
  duration_ms: z.number().nullable().optional(),
  queue_wait_ms: z.number().nullable().optional(),
  run_ms: z.number().nullable().optional(),
  total_ms: z.number().nullable().optional(),
  provider: z.string().default(""),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  failure_reason: z.string().default(""),
  error_type: z.string().default(""),
  trigger_comment_id: z.string().nullable().optional().transform((v) => v ?? null),
  autopilot_run_id: z.string().nullable().optional().transform((v) => v ?? null),
  chat_session_id: z.string().nullable().optional().transform((v) => v ?? null),
  metadata: z.record(z.string(), z.unknown()).default({}),
  created_at: z.string().default(""),
}).loose();

export const PromptEvaluationRunEvidenceSchema = z.object({
  run: PromptEvaluationRunSchema,
  trials: z.array(PromptEvaluationTrialSchema).default([]),
  task_usage: z.array(PromptEvaluationTaskUsageSchema).default([]),
  task_messages: z.array(PromptEvaluationTaskMessageSchema).default([]),
  trace_events: z.array(PromptEvaluationTaskTraceEventSchema).default([]),
  evidence: z.record(z.string(), z.unknown()).default({}),
  上下文: z.record(z.string(), z.unknown()).default({}),
}).loose();

export const PromptEvaluationEvidenceSnapshotSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  run_id: z.string(),
  snapshot_type: z.enum(["手动归档", "验收归档", "自动归档"]).default("手动归档"),
  schema_version: z.string().default("multica.prompt_evaluation.evidence_snapshot.v1"),
  summary: z.record(z.string(), z.unknown()).default({}),
  evidence: z.record(z.string(), z.unknown()).optional(),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
}).loose();

export const PromptEvaluationEvidenceSnapshotListResponseSchema = z.object({
  items: z.array(PromptEvaluationEvidenceSnapshotSchema).default([]),
  total: z.number().default(0),
}).loose();

export const PromptEvaluationSummarySchema = z.object({
  workspace_id: z.string().default(""),
  generated_at: z.string().default(""),
  last_run_at: z.string().default(""),
  指标: z.record(z.string(), z.number()).default({}),
  资产统计: z.record(z.string(), z.number()).default({}),
  运行状态: z.record(z.string(), z.number()).default({}),
  优化候选: z.record(z.string(), z.number()).default({}),
}).loose();

const PromptEvaluationRuntimeSchema = z.object({
  id: z.string().default(""),
  workspace_id: z.string().default(""),
  daemon_id: z.string().nullable().optional().transform((v) => v ?? null),
  name: z.string().default(""),
  runtime_mode: z.enum(["local", "cloud"]).default("local"),
  provider: z.string().default(""),
  launch_header: z.string().default(""),
  status: z.enum(["online", "offline"]).default("offline"),
  device_info: z.string().default(""),
  metadata: z.record(z.string(), z.unknown()).default({}),
  owner_id: z.string().nullable().optional().transform((v) => v ?? null),
  visibility: z.enum(["private", "public"]).default("private"),
  profile_id: z.string().nullable().optional().transform((v) => v ?? null),
  last_seen_at: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const PromptEvaluationRuntimeReadinessSchema = z.object({
  status: z.enum(["就绪", "离线", "过期", "缺失", "无权限", "容量受限"]).default("缺失"),
  label: z.string().default("CodeBuddy 缺失"),
  detail: z.string().default("当前 workspace 未发现 CodeBuddy runtime。"),
  fix: z.string().default("安装并配置 codebuddy，启动 multica daemon。"),
  model: z.string().default("minimax-m2.7-ioa"),
  runtime: PromptEvaluationRuntimeSchema.nullable().default(null),
  last_seen_age_seconds: z.number().default(-1),
  checked_at: z.string().default(""),
}).loose();

export const PromptEvaluationRunListResponseSchema = z.object({
  items: z.array(PromptEvaluationRunSchema).default([]),
  total: z.number().default(0),
}).loose();

export const PromptEvaluationTrialListResponseSchema = z.object({
  items: z.array(PromptEvaluationTrialSchema).default([]),
  total: z.number().default(0),
}).loose();

export const PromptEvaluationCaseSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  asset_id: z.string(),
  prompt_id: z.string().nullable().optional().transform((v) => v ?? null),
  case_index: z.number().default(0),
  case_name: z.string().default(""),
  variables: z.record(z.string(), z.unknown()).default({}),
  expected_contains: z.array(z.unknown()).default([]),
  input: z.record(z.string(), z.unknown()).default({}),
  expected: z.record(z.string(), z.unknown()).default({}),
  tags: z.array(z.unknown()).default([]),
  status: z.enum(["启用", "归档"]).default("启用"),
  source: z.string().default("payload"),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const PromptEvaluationCaseListResponseSchema = z.object({
  items: z.array(PromptEvaluationCaseSchema).default([]),
  total: z.number().default(0),
}).loose();

export const PromptEvaluationOptimizationCandidateSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  asset_id: z.string(),
  run_id: z.string(),
  prompt_id: z.string(),
  candidate_name: z.string(),
  candidate_content: z.string(),
  rationale: z.string().default(""),
  failed_case_count: z.number().default(0),
  source_failure_summary: z.record(z.string(), z.unknown()).default({}),
  source_prompt_snapshot: z.record(z.string(), z.unknown()).default({}),
  metrics: z.record(z.string(), z.unknown()).default({}),
  status: z.enum(["待确认", "已发布", "已拒绝"]).default("待确认"),
  published_prompt_id: z.string().nullable().optional().transform((v) => v ?? null),
  published_at: z.string().default(""),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const PromptEvaluationOptimizationCandidateListResponseSchema = z.object({
  items: z.array(PromptEvaluationOptimizationCandidateSchema).default([]),
  total: z.number().default(0),
}).loose();

export const PublishPromptEvaluationOptimizationCandidateResponseSchema = z.object({
  candidate: PromptEvaluationOptimizationCandidateSchema,
  prompt: PromptLibraryItemSchema,
}).loose();

export const EMPTY_PROMPT_EVALUATION_ASSET: PromptEvaluationAsset = {
  id: "",
  workspace_id: "",
  prompt_id: null,
  name: "",
  description: "",
  asset_type: "数据集",
  payload: {},
  status: "启用",
  created_by: null,
  created_at: "",
  updated_at: "",
  structure_schema: "multica.training_evaluation.asset_profile.v1",
  structured_case_count: 0,
  structured_variable_count: 0,
  structured_assertion_count: 0,
  linked_dataset_count: 0,
  linked_prompt_count: 0,
  evaluation_dimension_count: 0,
};

export const EMPTY_PROMPT_EVALUATION_ASSET_LIST_RESPONSE: ListPromptEvaluationAssetsResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_RUN = PromptEvaluationRunSchema.parse({
  id: "",
  workspace_id: "",
  asset_id: "",
  run_kind: "模板渲染检查",
  status: "已入队",
});

export const EMPTY_PROMPT_EVALUATION_RUN_LIST_RESPONSE = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_TRIAL_LIST_RESPONSE = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_RUN_EVIDENCE: PromptEvaluationRunEvidence = {
  run: EMPTY_PROMPT_EVALUATION_RUN,
  trials: [],
  task_usage: [],
  task_messages: [],
  trace_events: [],
  evidence: {},
  上下文: {},
};

export const EMPTY_PROMPT_EVALUATION_EVIDENCE_SNAPSHOT: PromptEvaluationEvidenceSnapshot = {
  id: "",
  workspace_id: "",
  run_id: "",
  snapshot_type: "手动归档",
  schema_version: "multica.prompt_evaluation.evidence_snapshot.v1",
  summary: {},
  evidence: {},
  created_by: null,
  created_at: "",
};

export const EMPTY_PROMPT_EVALUATION_EVIDENCE_SNAPSHOT_LIST_RESPONSE: ListPromptEvaluationEvidenceSnapshotsResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_SUMMARY: PromptEvaluationSummary = {
  workspace_id: "",
  generated_at: "",
  last_run_at: "",
  指标: {},
  资产统计: {},
  运行状态: {},
  优化候选: {},
};

export const EMPTY_PROMPT_EVALUATION_RUNTIME_READINESS: PromptEvaluationRuntimeReadiness = {
  status: "缺失",
  label: "CodeBuddy 缺失",
  detail: "当前 workspace 未发现 CodeBuddy runtime。",
  fix: "安装并配置 codebuddy，启动 multica daemon。",
  model: "minimax-m2.7-ioa",
  runtime: null,
  last_seen_age_seconds: -1,
  checked_at: "",
};

export const EMPTY_PROMPT_EVALUATION_CASE_LIST_RESPONSE = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_EVALUATION_CASE: PromptEvaluationStructuredCase = {
  id: "",
  workspace_id: "",
  asset_id: "",
  prompt_id: null,
  case_index: 0,
  case_name: "",
  variables: {},
  expected_contains: [],
  input: {},
  expected: {},
  tags: [],
  status: "启用",
  source: "",
  created_by: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE: PromptEvaluationOptimizationCandidate = {
  id: "",
  workspace_id: "",
  asset_id: "",
  run_id: "",
  prompt_id: "",
  candidate_name: "",
  candidate_content: "",
  rationale: "",
  failed_case_count: 0,
  source_failure_summary: {},
  source_prompt_snapshot: {},
  metrics: {},
  status: "待确认",
  published_prompt_id: null,
  published_at: "",
  created_by: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_LIST_RESPONSE: ListPromptEvaluationOptimizationCandidatesResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PUBLISH_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_RESPONSE: PublishPromptEvaluationOptimizationCandidateResponse = {
  candidate: EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE,
  prompt: EMPTY_PROMPT_LIBRARY_ITEM,
};

// Squad member status — backs the Squad detail page's Members tab. status
// is `string | null` (not the narrow `SquadMemberStatusValue` union) so a
// new server-side status doesn't fail the parse; the UI defaults to a
// neutral pill for unknown values.
const SquadActiveIssueBriefSchema = z.object({
  issue_id: z.string(),
  identifier: z.string(),
  title: z.string(),
  issue_status: z.string(),
}).loose();

const SquadMemberStatusSchema = z.object({
  member_type: z.string(),
  member_id: z.string(),
  status: z.string().nullable().optional().transform((v) => v ?? null),
  active_issues: z.array(SquadActiveIssueBriefSchema).default([]),
  last_active_at: z.string().nullable().optional().transform((v) => v ?? null),
}).loose();

export const SquadMemberStatusListResponseSchema = z.object({
  members: z.array(SquadMemberStatusSchema).default([]),
}).loose();

export const EMPTY_SQUAD_MEMBER_STATUS_LIST = { members: [] };

// ---------------------------------------------------------------------------
// Structured error body — POST /api/workspaces/:wsId/issues 409 conflict.
//
// When the server detects an active issue with the same title in the same
// workspace, it returns `{ code: "active_duplicate_issue", error, issue }`
// instead of letting the create through. The UI uses the embedded issue ref
// to offer "view existing" rather than dropping the user into a generic
// "create failed" toast.
//
// Strict guarantees:
//   - `code` is a literal so a future server rename (e.g. `duplicate_issue`)
//     fails the parse and falls back to a normal error toast — drift never
//     ships as a broken duplicate UI.
//   - `issue` is required; without an id/identifier/title the "view existing"
//     button has nothing to point at, so we'd rather fall back than guess.
//   - `issue.status` is intentionally OMITTED: the duplicate toast doesn't
//     render a StatusIcon (which has no fallback for unknown enum values),
//     so a future server-side rename of `status` must not knock this branch
//     out. `.loose()` lets the field pass through unchanged for any other
//     consumer.
// ---------------------------------------------------------------------------

export const DuplicateIssueErrorBodySchema = z.object({
  code: z.literal("active_duplicate_issue"),
  error: z.string().optional(),
  issue: z.object({
    id: z.string(),
    identifier: z.string(),
    title: z.string(),
  }).loose(),
}).loose();

export interface DuplicateIssueErrorBody {
  code: "active_duplicate_issue";
  error?: string;
  issue: {
    id: string;
    identifier: string;
    title: string;
  };
}

// ---------------------------------------------------------------------------
// Webhook delivery schemas — backing the Autopilot Deliveries section. Enums
// (`status`, `signature_status`, `provider`) are kept as `z.string()` so a
// future server-side value (e.g. a Stripe provider, a new dedupe state)
// degrades to a generic UI fallback rather than collapsing the list into
// the empty array. `.loose()` lets unknown fields pass through, matching
// the rule used by every other endpoint here.
// ---------------------------------------------------------------------------

const WebhookDeliverySchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  autopilot_id: z.string(),
  trigger_id: z.string(),
  provider: z.string(),
  event: z.string(),
  dedupe_key: z.string().nullable(),
  dedupe_source: z.string().nullable(),
  signature_status: z.string(),
  status: z.string(),
  attempt_count: z.number().default(0),
  content_type: z.string().nullable(),
  response_status: z.number().nullable(),
  autopilot_run_id: z.string().nullable(),
  replayed_from_delivery_id: z.string().nullable(),
  error: z.string().nullable(),
  received_at: z.string(),
  last_attempt_at: z.string(),
  created_at: z.string(),
  // Detail-only fields. The list endpoint omits them; the detail endpoint
  // populates raw_body / selected_headers / response_body.
  selected_headers: z.record(z.string(), z.unknown()).nullable().optional(),
  raw_body: z.string().nullable().optional(),
  response_body: z.string().nullable().optional(),
}).loose();

export const ListWebhookDeliveriesResponseSchema = z.object({
  deliveries: z.array(WebhookDeliverySchema).default([]),
  total: z.number().default(0),
}).loose();

export const WebhookDeliveryResponseSchema = WebhookDeliverySchema;

export const EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE: ListWebhookDeliveriesResponse = {
  deliveries: [],
  total: 0,
};

// ---------------------------------------------------------------------------
// Autopilot list schema. Enums (`status`, `execution_mode`, `trigger_kinds`,
// `last_run_status`) stay `z.string()` so future server-side values degrade
// to a generic UI fallback. The three derived fields (trigger_kinds /
// next_run_at / last_run_status) are list-endpoint-only and absent on older
// servers — optional by contract, the list renders "—" without them.
// ---------------------------------------------------------------------------

const AutopilotListItemSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  title: z.string(),
  description: z.string().nullable().optional(),
  project_id: z.string().nullable().optional(),
  // Older servers (pre-MUL-2429) omit assignee_type; "agent" is the
  // documented default.
  assignee_type: z.string().default("agent"),
  assignee_id: z.string(),
  status: z.string(),
  execution_mode: z.string(),
  issue_title_template: z.string().nullable().optional(),
  created_by_type: z.string(),
  created_by_id: z.string(),
  last_run_at: z.string().nullable().optional(),
  created_at: z.string(),
  updated_at: z.string(),
  trigger_kinds: z.array(z.string()).optional(),
  next_run_at: z.string().nullable().optional(),
  last_run_status: z.string().nullable().optional(),
}).loose();

export const ListAutopilotsResponseSchema = z.object({
  autopilots: z.array(AutopilotListItemSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_LIST_AUTOPILOTS_RESPONSE = {
  autopilots: [],
  total: 0,
};

export const EMPTY_WEBHOOK_DELIVERY: WebhookDelivery = {
  id: "",
  workspace_id: "",
  autopilot_id: "",
  trigger_id: "",
  provider: "",
  event: "",
  dedupe_key: null,
  dedupe_source: null,
  signature_status: "not_required",
  status: "queued",
  attempt_count: 0,
  content_type: null,
  response_status: null,
  autopilot_run_id: null,
  replayed_from_delivery_id: null,
  error: null,
  received_at: "",
  last_attempt_at: "",
  created_at: "",
};

// ---------------------------------------------------------------------------
// User (`/api/me` GET + PATCH). The auth store and Settings → Account both
// trust this shape — a drift here would knock both surfaces out. Kept
// lenient by the same rules as IssueSchema: enums stay `z.string()`,
// nullable fields are unioned with `null`, unknown server fields pass
// through via `.loose()`. `profile_description` is the field added in
// MUL-2406; the server emits `""` when unset (NOT NULL DEFAULT ''), so
// the schema defaults to `""` too — keeps the type tight without
// breaking older backends that don't return the column yet.
// ---------------------------------------------------------------------------

export const UserSchema = z.object({
  id: z.string(),
  name: z.string().default(""),
  account: z.string().default(""),
  avatar_url: z.string().nullable().default(null),
  onboarded_at: z.string().nullable().default(null),
  onboarding_questionnaire: z.record(z.string(), z.unknown()).default({}),
  starter_content_state: z.string().nullable().default(null),
  profile_description: z.string().default(""),
  timezone: z.string().nullable().default(null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const EMPTY_USER: User = {
  id: "",
  name: "",
  account: "",
  avatar_url: null,
  onboarded_at: null,
  onboarding_questionnaire: {},
  starter_content_state: null,
  profile_description: "",
  timezone: null,
  created_at: "",
  updated_at: "",
};
