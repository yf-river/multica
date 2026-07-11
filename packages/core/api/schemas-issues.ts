import { z } from "zod";
import type {
  Attachment,
  AssigneeFrequencyEntry,
  BatchDeleteIssuesResponse,
  BatchUpdateIssuesResponse,
  ChildIssueProgressResponse,
  Comment,
  FeedbackResponse,
  GroupedIssuesResponse,
  Issue,
  IssueReaction,
  ListIssueBucketsResponse,
  ListIssuesResponse,
  Reaction,
  QuickCreateIssueResponse,
  SearchIssuesResponse,
  TimelineEntry,
} from "../types";
import { EmbeddedAttachmentSchema, NonEmptyStringSchema } from "./schemas-internal";

// Runtime response contracts for issues.
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

export const ReactionSchema = z.object({
  id: NonEmptyStringSchema,
  comment_id: NonEmptyStringSchema,
  actor_type: z.string(),
  actor_id: z.string(),
  emoji: z.string(),
  created_at: z.string(),
}).loose();

export const IssueReactionSchema = z.object({
  id: NonEmptyStringSchema,
  issue_id: NonEmptyStringSchema,
  actor_type: z.string(),
  actor_id: z.string(),
  emoji: z.string(),
  created_at: z.string(),
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
  id: NonEmptyStringSchema,
  url: NonEmptyStringSchema,
  download_url: NonEmptyStringSchema,
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
  attachments: z.array(EmbeddedAttachmentSchema).optional(),
  source_task_id: z.string().nullable().optional(),
  coalesced_count: z.number().optional(),
}).loose();

// /timeline returns a flat array of TimelineEntry, oldest first. The
// previously cursor-paginated wrapper was removed (#1929) — at observed data
// sizes (p99 ~30 entries per issue) paged delivery only created bugs.
export const TimelineEntriesSchema = z.array(TimelineEntrySchema);

export const EMPTY_TIMELINE_ENTRIES: TimelineEntry[] = [];


export const CommentSchema = z.object({
  id: NonEmptyStringSchema,
  issue_id: NonEmptyStringSchema,
  author_type: z.string(),
  author_id: z.string(),
  content: z.string(),
  type: z.string(),
  parent_id: z.string().nullable(),
  reactions: z.array(ReactionSchema).default([]),
  attachments: z.array(EmbeddedAttachmentSchema).default([]),
  created_at: z.string(),
  updated_at: z.string(),
  source_task_id: z.string().nullable().optional(),
}).loose();

export const CommentsListSchema = z.array(CommentSchema);

export const EMPTY_COMMENT: Comment = {
  id: "",
  issue_id: "",
  author_type: "member",
  author_id: "",
  content: "",
  type: "comment",
  parent_id: null,
  reactions: [],
  attachments: [],
  created_at: "",
  updated_at: "",
  resolved_at: null,
  resolved_by_type: null,
  resolved_by_id: null,
};

export const EMPTY_REACTION: Reaction = {
  id: "",
  comment_id: "",
  actor_type: "",
  actor_id: "",
  emoji: "",
  created_at: "",
};

export const EMPTY_ISSUE_REACTION: IssueReaction = {
  id: "",
  issue_id: "",
  actor_type: "",
  actor_id: "",
  emoji: "",
  created_at: "",
};

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
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
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
  work_started_at: z.string().nullable().optional(),
  work_completed_at: z.string().nullable().optional(),
}).loose();

export const EMPTY_ISSUE: Issue = {
  id: "",
  workspace_id: "",
  number: 0,
  identifier: "",
  title: "",
  description: null,
  status: "todo",
  priority: "none",
  assignee_type: null,
  assignee_id: null,
  creator_type: "member",
  creator_id: "",
  parent_issue_id: null,
  project_id: null,
  position: 0,
  start_date: null,
  due_date: null,
  metadata: {},
  reactions: [],
  labels: [],
  created_at: "",
  updated_at: "",
};

export const SearchIssueSchema = IssueSchema.extend({
  match_source: z.string(),
  matched_description_snippet: z.string().optional(),
  matched_comment_snippet: z.string().optional(),
}).loose();

export const SearchIssuesResponseSchema = z.object({
  issues: z.array(SearchIssueSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_SEARCH_ISSUES_RESPONSE: SearchIssuesResponse = { issues: [], total: 0 };

export const QuickCreateIssueResponseSchema = z.object({
  task_id: NonEmptyStringSchema.optional(),
  issue_id: NonEmptyStringSchema.optional(),
  identifier: z.string().optional(),
  source_fetch_status: z.string().optional(),
}).loose().refine((response) => Boolean(response.task_id || response.issue_id), {
  message: "quick create response must identify a task or issue",
});

export const EMPTY_QUICK_CREATE_ISSUE_RESPONSE: QuickCreateIssueResponse = {};

export const FeedbackResponseSchema = z.object({
  id: NonEmptyStringSchema,
  created_at: z.string(),
}).loose();

export const EMPTY_FEEDBACK_RESPONSE: FeedbackResponse = { id: "", created_at: "" };

export const ChildIssueProgressResponseSchema = z.object({
  progress: z.array(z.object({
    parent_issue_id: NonEmptyStringSchema,
    total: z.number(),
    done: z.number(),
  }).loose()).default([]),
}).loose();

export const EMPTY_CHILD_ISSUE_PROGRESS_RESPONSE: ChildIssueProgressResponse = { progress: [] };

export const BatchUpdateIssuesResponseSchema = z.object({
  updated: z.number(),
  blocked: z.array(z.object({
    issue_id: z.string(),
    identifier: z.string(),
    title: z.string(),
    incomplete_children: z.array(z.unknown()),
  })).optional(),
  blocked_reason: z.string().optional(),
  failed: z.array(z.object({
    issue_id: z.string(),
    code: z.enum([
      "invalid_id",
      "not_found",
      "lookup_failed",
      "invalid_assignee",
      "invalid_start_date",
      "invalid_due_date",
      "invalid_parent",
      "invalid_project",
      "child_check_failed",
      "transaction_failed",
      "update_failed",
      "event_failed",
    ]),
  })).optional(),
}).loose();
export const BatchDeleteIssuesResponseSchema = z.object({
  deleted: z.number(),
  failed: z.array(z.object({
    issue_id: z.string(),
    code: z.enum(["invalid_id", "not_found", "lookup_failed", "delete_failed"]),
  })).optional(),
}).loose();
export const EMPTY_BATCH_UPDATE_ISSUES_RESPONSE: BatchUpdateIssuesResponse = { updated: 0 };
export const EMPTY_BATCH_DELETE_ISSUES_RESPONSE: BatchDeleteIssuesResponse = { deleted: 0 };

export const AssigneeFrequencyListSchema = z.array(z.object({
  assignee_type: z.string(),
  assignee_id: NonEmptyStringSchema,
  frequency: z.number(),
}).loose());
export const EMPTY_ASSIGNEE_FREQUENCY: AssigneeFrequencyEntry[] = [];

export const AttachmentListSchema = z.array(AttachmentResponseSchema);
export const EMPTY_ATTACHMENTS: Attachment[] = [];

export const ListIssuesResponseSchema = z.object({
  issues: z.array(IssueSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_LIST_ISSUES_RESPONSE: ListIssuesResponse = {
  issues: [],
  total: 0,
};

const IssueStatusBucketSchema = z.object({
  issues: z.array(IssueSchema).default([]),
  total: z.number().default(0),
}).loose();

export const ListIssueBucketsResponseSchema = z.object({
  by_status: z.record(z.string(), IssueStatusBucketSchema).default({}),
}).loose();

export const EMPTY_LIST_ISSUE_BUCKETS_RESPONSE: ListIssueBucketsResponse = {
  by_status: {},
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
