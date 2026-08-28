import type { ZodType } from "zod";
import type {
  Issue,
  CreateIssueRequest,
  CreateCommentRequest,
  UpdateIssueRequest,
  GroupedIssuesResponse,
  IssueStatus,
  ListIssueBucketsResponse,
  ListIssuesResponse,
  SearchIssueResult,
  SearchProjectResult,
  QuickCreateIssueRequest,
  ChildIssueProgressResponse,
  BatchUpdateIssuesResponse,
  BatchDeleteIssuesResponse,
  UpdateMeRequest,
  CreateMemberRequest,
  UpdateMemberRequest,
  ListIssuesParams,
  ListGroupedIssuesParams,
  Agent,
  CreateAgentRequest,
  UpdateAgentRequest,
  AgentEnvResponse,
  UpdateAgentEnvRequest,
  AgentTask,
  AgentActivityBucket,
  AgentRunCount,
  AgentRuntime,
  RuntimeProfile,
  CreateRuntimeProfileRequest,
  UpdateRuntimeProfileRequest,
  InboxItem,
  IssueSubscriber,
  Comment,
  CommentTriggerPreview,
  Reaction,
  IssueReaction,
  Workspace,
  WorkspaceSettings,
  WorkspaceRepo,
  WorkspaceRepoProbeResponse,
  MemberWithUser,
  User,
  Skill,
  SkillSummary,
  CreateSkillRequest,
  UpdateSkillRequest,
  SetAgentSkillsRequest,
  RuntimeUsage,
  IssueTaskTraceResponse,
  IssueExecutionTreeResponse,
  SquadSOPRun,
  ObservabilitySummary,
  RuntimeUsageByAgent,
  RuntimeUsageByTask,
  DashboardUsageDaily,
  DashboardAgentRunTime,
  DashboardRunTimeDaily,
  RuntimeModelListRequest,
  RuntimeLocalSkillListRequest,
  CreateRuntimeLocalSkillImportRequest,
  RuntimeLocalSkillImportRequest,
  TimelineEntry,
  AssigneeFrequencyEntry,
  TaskMessagePayload,
  Attachment,
  ChatSession,
  ChatMessagesPage,
  ChatPendingTask,
  PendingChatTasksResponse,
  SendChatMessageResponse,
  CancelTaskResponse,
  Project,
  CreateProjectRequest,
  UpdateProjectRequest,
  ProjectResource,
  CreateProjectResourceRequest,
  UpdateProjectResourceRequest,
  Label,
  CreateLabelRequest,
  UpdateLabelRequest,
  PinnedItem,
  CreatePinRequest,
  PinnedItemType,
  ReorderPinsRequest,
  Autopilot,
  AutopilotTrigger,
  AutopilotRun,
  CreateAutopilotRequest,
  CreateAutopilotResponse,
  UpdateAutopilotRequest,
  CreateAutopilotTriggerRequest,
  UpdateAutopilotTriggerRequest,
  GetAutopilotResponse,
  WebhookDelivery,
  NotificationPreferences,
  GitHubPullRequest,
  ListGitHubInstallationsResponse,
  GitHubConnectResponse,
  ListLarkInstallationsResponse,
  BeginLarkInstallResponse,
  LarkInstallStatusResponse,
  RedeemLarkBindingTokenResponse,
  Squad,
  SquadMember,
  SquadMemberStatus,
  InternalSquadTemplateKey,
  EnsureInternalSquadTemplateRequest,
  InternalSquadTemplateResponse,
  CreateSquadRequest,
  UpdateSquadRequest,
  ExternalCredentialProvider,
  ExternalCredentialProfile,
  CreateExternalCredentialProfileRequest,
  UpdateExternalCredentialProfileRequest,
  TestExternalCredentialProfileRequest,
  TestExternalCredentialProfileResponse,
  CompanionProfileEnvelope,
  CompanionProfile,
  LifeMemory,
  LifeMemoryKind,
  LifeMemoryListResponse,
  UpdateLifeMemoryRequest,
  LifeProposal,
  LifeProposalListResponse,
  LifeExperimentListResponse,
  LifeExperimentRound,
  LifeChronicleEntry,
  LifeChronicleListResponse,
  LifeProactiveCheckListResponse,
	LifeIdentityListResponse, LifeRelationshipListResponse, LifeMaterialListResponse, LifeInternalThoughtListResponse, LifeTopicListResponse,
	LifeCommitmentListResponse, LifeProactivePolicy, LifeObserverListResponse, LifeObservationSeatResponse,
	LifeModuleListResponse, LifeCognitionJobListResponse, LifeUpgradeEvaluationListResponse,
} from "../types";
import { generateUUID } from "../utils";
import { parseOrThrow, parseWithFallback } from "./schema";
import {
  ApiError,
  ApiTransport,
} from "./transport";
export {
  ApiError,
  ApiTransportError,
  type ApiClientOptions,
} from "./transport";
import {
  AgentSchema,
  AgentListSchema,
  AgentEnvResponseSchema,
  AgentTaskCancellationCountSchema,
  AgentTaskListSchema,
  AgentTaskSchema,
  AgentActivityBucketListSchema,
  AgentRunCountListSchema,
  TaskMessageListSchema,
  IssueTaskTraceResponseSchema,
  IssueExecutionTreeResponseSchema,
  SearchIssuesSchema,
  QuickCreateIssueResponseSchema,
  FeedbackResponseSchema,
  ChildIssueProgressResponseSchema,
  BatchUpdateIssuesResponseSchema,
  BatchDeleteIssuesResponseSchema,
  AssigneeFrequencyListSchema,
  AttachmentListSchema,
  AttachmentResponseSchema,
  CancelTaskResponseSchema,
  ChildIssuesResponseSchema,
  CommentTriggerPreviewSchema,
  DashboardAgentRunTimeListSchema,
  DashboardRunTimeDailyListSchema,
  DashboardUsageByAgentListSchema,
  DashboardUsageDailyListSchema,
  EMPTY_AGENT,
  EMPTY_AGENT_ACTIVITY_BUCKETS,
  EMPTY_AGENT_RUN_COUNTS,
  EMPTY_TASK_MESSAGES,
  EMPTY_ISSUE_TASK_TRACE_RESPONSE,
  EMPTY_ISSUE_EXECUTION_TREE,
  EMPTY_SEARCH_ISSUES,
  EMPTY_CHILD_ISSUE_PROGRESS_RESPONSE,
  EMPTY_ASSIGNEE_FREQUENCY,
  EMPTY_ATTACHMENTS,
  EMPTY_APP_CONFIG,
  EMPTY_ATTACHMENT,
  EMPTY_GROUPED_ISSUES_RESPONSE,
  EMPTY_LIST_ISSUE_BUCKETS_RESPONSE,
  EMPTY_LIST_ISSUES_RESPONSE,
  EMPTY_ISSUE,
  EMPTY_SQUAD,
  EMPTY_SQUAD_LIST,
  EMPTY_OBSERVABILITY_SUMMARY,
  EMPTY_TIMELINE_ENTRIES,
  EMPTY_USER,
  EMPTY_NOTIFICATION_PREFERENCES,
  EMPTY_CHAT_MESSAGES_PAGE,
  EMPTY_CHAT_PENDING_TASK,
  EMPTY_PENDING_CHAT_TASKS_RESPONSE,
  EMPTY_PROJECT_RESOURCES,
  EMPTY_LARK_INSTALLATION_LIST_RESPONSE,
  EMPTY_GITHUB_CONNECT_RESPONSE,
  EMPTY_GITHUB_INSTALLATION_LIST_RESPONSE,
  EMPTY_WEBHOOK_DELIVERIES,
  EMPTY_WEBHOOK_DELIVERY,
  AppConfigSchema,
  type AppConfigResponse,
  GroupedIssuesResponseSchema,
  ListIssueBucketsResponseSchema,
  AutopilotListSchema,
  EMPTY_AUTOPILOTS,
  AutopilotSchema,
  CreateAutopilotResponseSchema,
  AutopilotTriggerSchema,
  AutopilotRunSchema,
  GetAutopilotResponseSchema,
  AutopilotRunListSchema,
  EMPTY_AUTOPILOT_RUN,
  EMPTY_GET_AUTOPILOT_RESPONSE,
  EMPTY_AUTOPILOT_RUNS,
  ListIssuesResponseSchema,
  IssueSchema,
  CommentSchema,
  ReactionSchema,
  IssueReactionSchema,
  WebhookDeliveryListSchema,
  RuntimeUsageByAgentListSchema,
  RuntimeUsageByTaskListSchema,
  RuntimeUsageListSchema,
  RuntimeProfileListResponseSchema,
  RuntimeProfileSchema,
  RuntimeDeviceListSchema,
  RuntimeDeviceSchema,
  RuntimeCascadeDeleteResponseSchema,
  RuntimeModelListRequestSchema,
  RuntimeLocalSkillListRequestSchema,
  RuntimeLocalSkillImportRequestSchema,
  IssueSOPRunsResponseSchema,
  ObservabilitySummarySchema,
  SquadSchema,
  SquadListSchema,
  SquadMemberStatusListResponseSchema,
  SubscribersListSchema,
  TimelineEntriesSchema,
  UserSchema,
  LoginResponseSchema,
  CliTokenResponseSchema,
  WorkspaceListSchema,
  WorkspaceSchema,
  WorkspaceRepoSchema,
  WorkspaceRepoProbeResponseSchema,
  MemberWithUserListSchema,
  MemberWithUserSchema,
  InboxListSchema,
  InboxItemSchema,
  InboxCountResponseSchema,
  NotificationPreferenceResponseSchema,
  ChatSessionListSchema,
  ChatSessionSchema,
  ChatMessagesPageSchema,
  SendChatMessageResponseSchema,
  ChatPendingTaskSchema,
  PendingChatTasksResponseSchema,
  ProjectResourceSchema,
  ProjectResourceListSchema,
  ProjectSchema,
  ProjectListSchema,
  SearchProjectListSchema,
  SkillSchema,
  SkillSummaryListSchema,
  LabelSchema,
  LabelListSchema,
  IssueLabelListSchema,
  PinnedItemSchema,
  PinnedItemListSchema,
  SquadMemberSchema,
  SquadMemberListSchema,
  InternalSquadTemplateResponseSchema,
  EMPTY_PROJECT,
  EMPTY_PROJECTS,
  EMPTY_SEARCH_PROJECTS,
  EMPTY_SKILL,
  EMPTY_SKILL_SUMMARIES,
  EMPTY_LABELS,
  EMPTY_PINNED_ITEM_LIST,
  EMPTY_SQUAD_MEMBERS,
  ExternalCredentialProfileSchema,
  ExternalCredentialProfileListResponseSchema,
  TestExternalCredentialProfileResponseSchema,
  LarkInstallationListResponseSchema,
  BeginLarkInstallResponseSchema,
  LarkInstallStatusResponseSchema,
  RedeemLarkBindingTokenResponseSchema,
  GitHubConnectResponseSchema,
  GitHubInstallationListResponseSchema,
  GitHubPullRequestListResponseSchema,
  WebhookDeliveryResponseSchema,
  CompanionProfileEnvelopeSchema,
  CompanionProfileSchema,
  LifeMemorySchema,
  LifeMemoryListSchema,
  LifeProposalSchema,
  LifeProposalListSchema,
  LifeExperimentListSchema,
  LifeExperimentRoundSchema,
  LifeChronicleEntrySchema,
  LifeChronicleListSchema,
  EMPTY_COMPANION_PROFILE,
  EMPTY_LIFE_MEMORIES,
  EMPTY_LIFE_PROPOSALS,
  EMPTY_LIFE_EXPERIMENTS,
  EMPTY_LIFE_CHRONICLE,
  LifeProactiveCheckListSchema,
  EMPTY_LIFE_PROACTIVE_CHECKS,
	LifeIdentityListSchema, LifeRelationshipListSchema, LifeMaterialListSchema, LifeInternalThoughtListSchema, LifeTopicListSchema,
	LifeCommitmentListSchema, LifeProactivePolicySchema, LifeObserverListSchema, LifeObservationSeatSchema,
	LifeModuleListSchema, LifeCognitionJobListSchema, LifeUpgradeEvaluationListSchema,
	EMPTY_LIFE_IDENTITIES, EMPTY_LIFE_RELATIONSHIPS, EMPTY_LIFE_MATERIALS, EMPTY_LIFE_INTERNAL_THOUGHTS, EMPTY_LIFE_TOPICS,
	EMPTY_LIFE_COMMITMENTS, EMPTY_LIFE_OBSERVERS, EMPTY_LIFE_OBSERVATION_SEAT, EMPTY_LIFE_MODULES,
	EMPTY_LIFE_JOBS, EMPTY_LIFE_UPGRADES,
} from "./schemas";

export interface LoginResponse {
  token: string;
  user: User;
}

// Thrown by getAttachmentTextContent when the server refuses to inline a
// file because it exceeds the 2 MB cap. UI maps to a "too large, please
// download" affordance with the Download CTA still available.
export class PreviewTooLargeError extends Error {
  constructor() {
    super("attachment too large for inline preview");
    this.name = "PreviewTooLargeError";
  }
}

// Thrown by getAttachmentTextContent when the server's text whitelist
// rejects the content type. Normally the client's preview-kind guard
// catches this earlier, but the two whitelists can drift — surfacing the
// 415 as a typed error makes the drift visible.
export class PreviewUnsupportedError extends Error {
  constructor() {
    super("attachment type not supported for inline preview");
    this.name = "PreviewUnsupportedError";
  }
}

type UsageQueryParams = { days?: number; project_id?: string | null; tz?: string };

function usageSearchParams(params?: UsageQueryParams) {
  const search = new URLSearchParams();
  if (params?.days) search.set("days", String(params.days));
  if (params?.project_id) search.set("project_id", params.project_id);
  if (params?.tz) search.set("tz", params.tz);
  return search;
}

function parseRuntimeRequest<T extends { id: string; runtime_id: string }>(
  data: unknown,
  schema: ZodType<T>,
  runtimeId: string,
  requestId: string | undefined,
  endpoint: string,
  mayHaveCommitted: boolean,
): T {
  const matchesIdentity = (request: T) =>
    request.runtime_id === runtimeId && (requestId === undefined || request.id === requestId);
  const validatedSchema = requestId === undefined
    ? schema.refine(matchesIdentity, {
        path: ["runtime_id"],
        message: "runtime request identity does not match request",
      })
    : schema.refine(matchesIdentity, {
        message: "runtime request identity does not match request",
      });
  return parseOrThrow(data, validatedSchema, {
    endpoint,
    mayHaveCommitted,
  });
}

function issueSubscriptionBody(userId?: string, userType?: string) {
  return JSON.stringify({ user_id: userId || undefined, user_type: userType || undefined });
}

function issueSearchParams(params?: ListIssuesParams) {
  const search = new URLSearchParams();
  if (params?.limit) search.set("limit", String(params.limit));
  if (params?.offset) search.set("offset", String(params.offset));
  if (params?.workspace_id) search.set("workspace_id", params.workspace_id);
  if (params?.status) search.set("status", params.status);
  if (params?.priority) search.set("priority", params.priority);
  if (params?.assignee_id) search.set("assignee_id", params.assignee_id);
  if (params?.creator_id) search.set("creator_id", params.creator_id);
  if (params?.project_id) search.set("project_id", params.project_id);
  if (params?.involves_user_id) search.set("involves_user_id", params.involves_user_id);
  if (params?.metadata && Object.keys(params.metadata).length > 0) {
    search.set("metadata", JSON.stringify(params.metadata));
  }
  if (params?.scheduled) search.set("scheduled", "true");
  if (params?.date_field) search.set("date_field", params.date_field);
  if (params?.date_start) search.set("date_start", params.date_start);
  if (params?.date_end) search.set("date_end", params.date_end);
  if (params?.sort_by) search.set("sort", params.sort_by);
  if (params?.sort_direction) search.set("direction", params.sort_direction);
  return search;
}

// Flat endpoint registry. Transport/auth/error semantics live in transport.ts;
// docs/architecture/domain-flows.md records the ownership boundary.
export class ApiClient extends ApiTransport {
  // Auth
  async login(account: string, password: string): Promise<LoginResponse> {
    const raw = await this.fetch<unknown>("/auth/login", {
      method: "POST",
      body: JSON.stringify({ account, password }),
    });
    return parseOrThrow(raw, LoginResponseSchema, {
      endpoint: "POST /auth/login",
    });
  }

  async logout(): Promise<void> {
    await this.fetch("/auth/logout", { method: "POST" });
  }

  async issueCliToken(): Promise<{ token: string }> {
    const raw = await this.fetch<unknown>("/api/cli-token", { method: "POST" });
    return parseOrThrow(raw, CliTokenResponseSchema, {
      endpoint: "POST /api/cli-token",
    });
  }

  async getMe(): Promise<User> {
    const raw = await this.fetch<unknown>("/api/me");
    return parseWithFallback(raw, UserSchema, EMPTY_USER, {
      endpoint: "GET /api/me",
    });
  }

  async updateMe(data: UpdateMeRequest): Promise<User> {
    const raw = await this.fetch<unknown>("/api/me", {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, UserSchema, {
      endpoint: "PATCH /api/me",
    });
  }

  // Issues
  async listIssues(params?: ListIssuesParams): Promise<ListIssuesResponse> {
    const search = issueSearchParams(params);
    const path = `/api/issues?${search}`;
    const raw = await this.fetch<unknown>(path);
    return parseWithFallback(raw, ListIssuesResponseSchema, EMPTY_LIST_ISSUES_RESPONSE, {
      endpoint: "GET /api/issues",
    });
  }

  async listIssueBuckets(params?: ListIssuesParams & { statuses?: IssueStatus[] }): Promise<ListIssueBucketsResponse> {
    const search = issueSearchParams(params);
    if (params?.statuses?.length) search.set("statuses", params.statuses.join(","));
    const path = `/api/issues/buckets?${search}`;
    const raw = await this.fetch<unknown>(path);
    return parseWithFallback(raw, ListIssueBucketsResponseSchema, EMPTY_LIST_ISSUE_BUCKETS_RESPONSE, {
      endpoint: "GET /api/issues/buckets",
    });
  }

  async listGroupedIssues(params: ListGroupedIssuesParams): Promise<GroupedIssuesResponse> {
    const search = issueSearchParams(params);
    search.set("group_by", params.group_by);
    if (params.statuses?.length) search.set("statuses", params.statuses.join(","));
    if (params.priorities?.length) search.set("priorities", params.priorities.join(","));
    if (params.assignee_types?.length) search.set("assignee_types", params.assignee_types.join(","));
    if (params.assignee_filters?.length) {
      search.set("assignee_filters", params.assignee_filters.map((f) => `${f.type}:${f.id}`).join(","));
    }
    if (params.include_no_assignee) search.set("include_no_assignee", "true");
    if (params.creator_filters?.length) {
      search.set("creator_filters", params.creator_filters.map((f) => `${f.type}:${f.id}`).join(","));
    }
    if (params.project_ids?.length) search.set("project_ids", params.project_ids.join(","));
    if (params.include_no_project) search.set("include_no_project", "true");
    if (params.label_ids?.length) search.set("label_ids", params.label_ids.join(","));
    if (params.group_assignee_type) search.set("group_assignee_type", params.group_assignee_type);
    if (params.group_assignee_id) search.set("group_assignee_id", params.group_assignee_id);
    const raw = await this.fetch<unknown>(`/api/issues/grouped?${search}`);
    return parseWithFallback(raw, GroupedIssuesResponseSchema, EMPTY_GROUPED_ISSUES_RESPONSE, {
      endpoint: "GET /api/issues/grouped",
    });
  }

  async searchIssues(params: { q: string; limit?: number; offset?: number; include_closed?: boolean; signal?: AbortSignal }): Promise<SearchIssueResult[]> {
    const search = new URLSearchParams({ q: params.q });
    if (params.limit !== undefined) search.set("limit", String(params.limit));
    if (params.offset !== undefined) search.set("offset", String(params.offset));
    if (params.include_closed) search.set("include_closed", "true");
    const raw = await this.fetch<unknown>(
      `/api/issues/search?${search}`,
      params.signal ? { signal: params.signal } : undefined,
    );
    return parseWithFallback(raw, SearchIssuesSchema, EMPTY_SEARCH_ISSUES, {
      endpoint: "GET /api/issues/search",
    });
  }

  async searchProjects(params: { q: string; limit?: number; offset?: number; include_closed?: boolean; signal?: AbortSignal }): Promise<SearchProjectResult[]> {
    const search = new URLSearchParams({ q: params.q });
    if (params.limit !== undefined) search.set("limit", String(params.limit));
    if (params.offset !== undefined) search.set("offset", String(params.offset));
    if (params.include_closed) search.set("include_closed", "true");
    const raw = await this.fetch<unknown>(
      `/api/projects/search?${search}`,
      params.signal ? { signal: params.signal } : undefined,
    );
    return parseWithFallback(
      raw,
      SearchProjectListSchema,
      EMPTY_SEARCH_PROJECTS,
      { endpoint: "GET /api/projects/search" },
    );
  }

  async getIssue(id: string): Promise<Issue> {
    const raw = await this.fetch<unknown>(`/api/issues/${id}`);
    return parseWithFallback(raw, IssueSchema, EMPTY_ISSUE, {
      endpoint: "GET /api/issues/:id",
    });
  }

  async createIssue(
    data: CreateIssueRequest,
    idempotencyKey = generateUUID(),
  ): Promise<Issue> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>("/api/issues", {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<Issue>(raw, IssueSchema, {
        endpoint: "POST /api/issues",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async quickCreateIssue(
    data: QuickCreateIssueRequest,
    idempotencyKey = generateUUID(),
  ): Promise<void> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>("/api/issues/quick-create", {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      parseOrThrow(
        raw,
        QuickCreateIssueResponseSchema,
        { endpoint: "POST /api/issues/quick-create", mayHaveCommitted: true },
      );
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async createFeedback(data: {
    message: string;
    kind: "bug" | "feature" | "general" | "praise";
    url?: string;
    workspace_id?: string;
  }, idempotencyKey = generateUUID()): Promise<void> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>("/api/feedback", {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      parseOrThrow(raw, FeedbackResponseSchema, {
        endpoint: "POST /api/feedback",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async updateIssue(id: string, data: UpdateIssueRequest): Promise<Issue> {
    const raw = await this.fetch<unknown>(`/api/issues/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, IssueSchema, {
      endpoint: "PUT /api/issues/:id",
    });
  }

  async listChildIssues(id: string): Promise<{ issues: Issue[] }> {
    const raw = await this.fetch<unknown>(`/api/issues/${id}/children`);
    return parseWithFallback(raw, ChildIssuesResponseSchema, { issues: [] }, {
      endpoint: "GET /api/issues/:id/children",
    });
  }

  /** Batched variant — returns children for multiple parents in one request.
   *  Avoids an N-request fan-out in Swimlane (one per visible parent lane).
   *  parentIds must be non-empty; pass a sorted, deduplicated list so the
   *  React Query cache key is stable across renders. */
  async listChildrenByParents(parentIds: string[]): Promise<{ issues: Issue[] }> {
    const raw = await this.fetch<unknown>(
      `/api/issues/children?parent_ids=${parentIds.join(",")}`,
    );
    return parseWithFallback(raw, ChildIssuesResponseSchema, { issues: [] }, {
      endpoint: "GET /api/issues/children",
    });
  }

  async getChildIssueProgress(): Promise<ChildIssueProgressResponse> {
    const raw = await this.fetch<unknown>("/api/issues/child-progress");
    return parseWithFallback(
      raw,
      ChildIssueProgressResponseSchema,
      EMPTY_CHILD_ISSUE_PROGRESS_RESPONSE,
      { endpoint: "GET /api/issues/child-progress" },
    );
  }

  async deleteIssue(id: string): Promise<void> {
    await this.fetch(`/api/issues/${id}`, { method: "DELETE" });
  }

  async batchUpdateIssues(issueIds: string[], updates: UpdateIssueRequest): Promise<BatchUpdateIssuesResponse> {
    const raw = await this.fetch<unknown>("/api/issues/batch-update", {
      method: "POST",
      body: JSON.stringify({ issue_ids: issueIds, updates }),
    });
    return parseOrThrow(raw, BatchUpdateIssuesResponseSchema, {
      endpoint: "POST /api/issues/batch-update",
      mayHaveCommitted: true,
    });
  }

  async batchDeleteIssues(issueIds: string[]): Promise<BatchDeleteIssuesResponse> {
    const raw = await this.fetch<unknown>("/api/issues/batch-delete", {
      method: "POST",
      body: JSON.stringify({ issue_ids: issueIds }),
    });
    return parseOrThrow(raw, BatchDeleteIssuesResponseSchema, {
      endpoint: "POST /api/issues/batch-delete",
      mayHaveCommitted: true,
    });
  }

  // Comments
  async createComment(
    issueId: string,
    data: CreateCommentRequest,
    idempotencyKey = generateUUID(),
  ): Promise<Comment> {
    const request = { ...data, type: data.type ?? "comment" };
    const attempt = async () => {
      const raw = await this.fetch<unknown>(`/api/issues/${issueId}/comments`, {
        method: "POST",
        body: JSON.stringify(request),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<Comment>(raw, CommentSchema, {
        endpoint: "POST /api/issues/:id/comments",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async previewCommentTriggers(issueId: string, content: string, parentId?: string, editingCommentId?: string): Promise<CommentTriggerPreview> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/comments/trigger-preview`, {
      method: "POST",
      body: JSON.stringify({
        content,
        ...(parentId ? { parent_id: parentId } : {}),
        ...(editingCommentId ? { editing_comment_id: editingCommentId } : {}),
      }),
    });
    return parseWithFallback(raw, CommentTriggerPreviewSchema, { agents: [] }, {
      endpoint: "POST /api/issues/:id/comments/trigger-preview",
    });
  }

  async listTimeline(issueId: string): Promise<TimelineEntry[]> {
    const raw = await this.fetch<unknown>(
      `/api/issues/${issueId}/timeline`,
    );
    return parseWithFallback(raw, TimelineEntriesSchema, EMPTY_TIMELINE_ENTRIES, {
      endpoint: "GET /api/issues/:id/timeline",
    });
  }

  async getAssigneeFrequency(): Promise<AssigneeFrequencyEntry[]> {
    const raw = await this.fetch<unknown>("/api/assignee-frequency");
    return parseWithFallback(raw, AssigneeFrequencyListSchema, EMPTY_ASSIGNEE_FREQUENCY, {
      endpoint: "GET /api/assignee-frequency",
    });
  }

  async updateComment(commentId: string, content: string, attachmentIds: string[], suppressAgentIds?: string[]): Promise<Comment> {
    const raw = await this.fetch<unknown>(`/api/comments/${commentId}`, {
      method: "PUT",
      body: JSON.stringify({
        content,
        attachment_ids: attachmentIds,
        ...(suppressAgentIds?.length ? { suppress_agent_ids: suppressAgentIds } : {}),
      }),
    });
    return parseOrThrow(raw, CommentSchema, {
      endpoint: "PUT /api/comments/:id",
    });
  }

  async deleteComment(commentId: string): Promise<void> {
    await this.fetch(`/api/comments/${commentId}`, { method: "DELETE" });
  }

  async resolveComment(commentId: string): Promise<Comment> {
    const raw = await this.fetch<unknown>(`/api/comments/${commentId}/resolve`, { method: "POST" });
    return parseOrThrow(raw, CommentSchema, {
      endpoint: "POST /api/comments/:id/resolve",
    });
  }

  async unresolveComment(commentId: string): Promise<Comment> {
    const raw = await this.fetch<unknown>(`/api/comments/${commentId}/resolve`, { method: "DELETE" });
    return parseOrThrow(raw, CommentSchema, {
      endpoint: "DELETE /api/comments/:id/resolve",
    });
  }

  async addReaction(commentId: string, emoji: string): Promise<Reaction> {
    const raw = await this.fetch<unknown>(`/api/comments/${commentId}/reactions`, {
      method: "POST",
      body: JSON.stringify({ emoji }),
    });
    return parseOrThrow(raw, ReactionSchema, {
      endpoint: "POST /api/comments/:id/reactions",
    });
  }

  async removeReaction(commentId: string, emoji: string): Promise<void> {
    await this.fetch(`/api/comments/${commentId}/reactions`, {
      method: "DELETE",
      body: JSON.stringify({ emoji }),
    });
  }

  async addIssueReaction(issueId: string, emoji: string): Promise<IssueReaction> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/reactions`, {
      method: "POST",
      body: JSON.stringify({ emoji }),
    });
    return parseOrThrow(raw, IssueReactionSchema, {
      endpoint: "POST /api/issues/:id/reactions",
    });
  }

  async removeIssueReaction(issueId: string, emoji: string): Promise<void> {
    await this.fetch(`/api/issues/${issueId}/reactions`, {
      method: "DELETE",
      body: JSON.stringify({ emoji }),
    });
  }

  // Subscribers
  async listIssueSubscribers(issueId: string): Promise<IssueSubscriber[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/subscribers`);
    return parseWithFallback(raw, SubscribersListSchema, [], {
      endpoint: "GET /api/issues/:id/subscribers",
    });
  }

  async subscribeToIssue(issueId: string, userId?: string, userType?: string): Promise<void> {
    await this.fetch(`/api/issues/${issueId}/subscribe`, {
      method: "POST",
      body: issueSubscriptionBody(userId, userType),
    });
  }

  async unsubscribeFromIssue(issueId: string, userId?: string, userType?: string): Promise<void> {
    await this.fetch(`/api/issues/${issueId}/unsubscribe`, {
      method: "POST",
      body: issueSubscriptionBody(userId, userType),
    });
  }

  // Agents
  async listAgents(params?: { workspace_id?: string; include_archived?: boolean }): Promise<Agent[]> {
    const search = new URLSearchParams();
    if (params?.workspace_id) search.set("workspace_id", params.workspace_id);
    if (params?.include_archived) search.set("include_archived", "true");
    const raw = await this.fetch<unknown>(`/api/agents?${search}`);
    return parseWithFallback(raw, AgentListSchema, [], { endpoint: "GET /api/agents" });
  }

  async getAgent(id: string): Promise<Agent> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}`);
    return parseWithFallback(raw, AgentSchema, EMPTY_AGENT, { endpoint: "GET /api/agents/:id" });
  }

  async createAgent(
    data: CreateAgentRequest,
    idempotencyKey = generateUUID(),
  ): Promise<Agent> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>("/api/agents", {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<Agent>(raw, AgentSchema, {
        endpoint: "POST /api/agents",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async updateAgent(id: string, data: UpdateAgentRequest): Promise<Agent> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, AgentSchema, { endpoint: "PUT /api/agents/:id" });
  }

  async archiveAgent(id: string): Promise<Agent> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/archive`, { method: "POST" });
    return parseOrThrow(raw, AgentSchema, { endpoint: "POST /api/agents/:id/archive" });
  }

  /**
   * Returns the plaintext `custom_env` map for an agent. Owner/admin
   * only; calls from agent-actor sessions get a 403. Every successful
   * call writes an `agent_env_revealed` activity_log row server-side.
   * MUL-2600.
   */
  async getAgentEnv(id: string): Promise<AgentEnvResponse> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/env`);
    return parseOrThrow(
      raw,
      AgentEnvResponseSchema.refine((response) => response.agent_id === id, {
        path: ["agent_id"],
        message: "agent id does not match request",
      }),
      {
        endpoint: "GET /api/agents/:id/env",
        mayHaveCommitted: false,
      },
    );
  }

  /**
   * Replaces an agent's `custom_env` wholesale. Values equal to
   * `"****"` are preserved server-side (the **** guard) so a partial
   * UI edit doesn't overwrite real secrets with the masked
   * placeholder. Owner/admin only; agent actors get a 403. Every
   * successful call writes an `agent_env_updated` activity_log row.
   * MUL-2600.
   */
  async updateAgentEnv(id: string, data: UpdateAgentEnvRequest): Promise<AgentEnvResponse> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/env`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(
      raw,
      AgentEnvResponseSchema.refine((response) => response.agent_id === id, {
        path: ["agent_id"],
        message: "agent id does not match request",
      }),
      { endpoint: "PUT /api/agents/:id/env" },
    );
  }

  async restoreAgent(id: string): Promise<Agent> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/restore`, { method: "POST" });
    return parseOrThrow(raw, AgentSchema, { endpoint: "POST /api/agents/:id/restore" });
  }

  // Bulk-cancel every active task (queued/dispatched/running) for the agent.
  // Permission: agent owner or workspace admin/owner. Server returns the
  // count of cancelled rows; broadcasts task:cancelled for each so other
  // surfaces can clear their live cards.
  async cancelAgentTasks(id: string): Promise<{ cancelled: number }> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/cancel-tasks`, { method: "POST" });
    return parseOrThrow(raw, AgentTaskCancellationCountSchema, { endpoint: "POST /api/agents/:id/cancel-tasks" });
  }

  async listRuntimes(params?: { workspace_id?: string; owner?: "me" }): Promise<AgentRuntime[]> {
    const search = new URLSearchParams();
    if (params?.workspace_id) search.set("workspace_id", params.workspace_id);
    if (params?.owner) search.set("owner", params.owner);
    const raw = await this.fetch<unknown>(`/api/runtimes?${search}`);
    return parseWithFallback(raw, RuntimeDeviceListSchema, [], {
      endpoint: "GET /api/runtimes",
    }) as AgentRuntime[];
  }

  async deleteRuntime(runtimeId: string): Promise<void> {
    await this.fetch(`/api/runtimes/${runtimeId}`, { method: "DELETE" });
  }

  // Cascade variant of deleteRuntime. The strict DELETE refuses with
  // structured 409 (`code: "runtime_has_active_agents"`, body carries the
  // blocking agents) when active agents are bound; the front-end then opens
  // the cascade-mode confirmation dialog and submits the user-confirmed
  // active agent set here. Server compares the snapshot to the live set
  // inside the transaction and refuses with `code: "runtime_delete_plan_changed"`
  // (same shape, fresh `active_agents`) if they don't match — caller should
  // re-render the agent list and force the user to re-confirm.
  async archiveAgentsAndDeleteRuntime(
    runtimeId: string,
    expectedActiveAgentIds: string[],
  ): Promise<{ status: string; agents_archived: number; tasks_cancelled: number }> {
    const raw = await this.fetch<unknown>(`/api/runtimes/${runtimeId}/archive-agents-and-delete`, {
      method: "POST",
      body: JSON.stringify({ expected_active_agent_ids: expectedActiveAgentIds }),
    });
    return parseOrThrow(
      raw,
      RuntimeCascadeDeleteResponseSchema,
      { endpoint: "POST /api/runtimes/:id/archive-agents-and-delete" },
    );
  }

  async updateRuntime(
    runtimeId: string,
    patch: { scope?: "personal" | "workspace" },
  ): Promise<AgentRuntime> {
    const raw = await this.fetch<unknown>(`/api/runtimes/${runtimeId}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
    return parseOrThrow(
      raw,
      RuntimeDeviceSchema.refine((runtime) => runtime.id === runtimeId, {
        path: ["id"],
        message: "runtime id does not match request",
      }),
      { endpoint: "PATCH /api/runtimes/:id" },
    ) as AgentRuntime;
  }

  // ---------------------------------------------------------------------
  // Custom runtime profiles (MUL-3284). All workspace-scoped: the caller
  // passes the workspace id the same way the runtimes list resolves it.
  // ---------------------------------------------------------------------

  async listRuntimeProfiles(workspaceId: string): Promise<RuntimeProfile[]> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/runtime-profiles`,
    );
    return parseWithFallback(raw, RuntimeProfileListResponseSchema, [], {
      endpoint: "GET /api/workspaces/:workspaceId/runtime-profiles",
    });
  }

  async createRuntimeProfile(
    workspaceId: string,
    body: CreateRuntimeProfileRequest,
  ): Promise<RuntimeProfile> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/runtime-profiles`, {
      method: "POST",
      body: JSON.stringify(body),
    });
    return parseOrThrow(raw, RuntimeProfileSchema, {
      endpoint: "POST /api/workspaces/:workspaceId/runtime-profiles",
    });
  }

  async updateRuntimeProfile(
    workspaceId: string,
    profileId: string,
    patch: UpdateRuntimeProfileRequest,
  ): Promise<RuntimeProfile> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/runtime-profiles/${profileId}`,
      {
        method: "PATCH",
        body: JSON.stringify(patch),
      },
    );
    return parseOrThrow(raw, RuntimeProfileSchema, {
      endpoint: "PATCH /api/workspaces/:workspaceId/runtime-profiles/:profileId",
    });
  }

  async deleteRuntimeProfile(
    workspaceId: string,
    profileId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/runtime-profiles/${profileId}`,
      { method: "DELETE" },
    );
  }

  async getRuntimeUsage(
    runtimeId: string,
    params?: { days?: number; tz?: string },
  ): Promise<RuntimeUsage[]> {
    const search = usageSearchParams(params);
    // `tz` drives the calendar-day boundary for the trend chart (Viewing
    // layer). Caller-supplied; the backend falls back to user.timezone /
    // UTC if omitted.
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/usage?${search}`,
    );
    return parseWithFallback<RuntimeUsage[]>(raw, RuntimeUsageListSchema, [], {
      endpoint: "GET /api/runtimes/:id/usage",
    });
  }

  async getRuntimeUsageByAgent(
    runtimeId: string,
    params?: { days?: number; tz?: string },
  ): Promise<RuntimeUsageByAgent[]> {
    const search = usageSearchParams(params);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/usage/by-agent?${search}`,
    );
    return parseWithFallback<RuntimeUsageByAgent[]>(
      raw,
      RuntimeUsageByAgentListSchema,
      [],
      { endpoint: "GET /api/runtimes/:id/usage/by-agent" },
    );
  }

  async getRuntimeUsageByTask(
    runtimeId: string,
    params?: { days?: number; tz?: string },
  ): Promise<RuntimeUsageByTask[]> {
    const search = usageSearchParams(params);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/usage/by-task?${search}`,
    );
    return parseWithFallback<RuntimeUsageByTask[]>(
      raw,
      RuntimeUsageByTaskListSchema,
      [],
      { endpoint: "GET /api/runtimes/:id/usage/by-task" },
    );
  }

  // ---------------------------------------------------------------------------
  // Workspace dashboard — three independent rollups for `/{slug}/dashboard`.
  // Each accepts an optional `project_id` to narrow the scope to one project.
  // Cost fields are computed server-side from the maintained pricing catalog
  // (same contract as the per-runtime endpoints above).
  // ---------------------------------------------------------------------------

  async getDashboardUsageDaily(
    params: { days?: number; project_id?: string | null; tz?: string },
  ): Promise<DashboardUsageDaily[]> {
    const search = usageSearchParams(params);
    const raw = await this.fetch<unknown>(`/api/dashboard/usage/daily?${search}`);
    return parseWithFallback<DashboardUsageDaily[]>(
      raw,
      DashboardUsageDailyListSchema,
      [],
      { endpoint: "GET /api/dashboard/usage/daily" },
    );
  }

  async getDashboardUsageByAgent(
    params: { days?: number; project_id?: string | null; tz?: string },
  ): Promise<RuntimeUsageByAgent[]> {
    const search = usageSearchParams(params);
    const raw = await this.fetch<unknown>(`/api/dashboard/usage/by-agent?${search}`);
    return parseWithFallback<RuntimeUsageByAgent[]>(
      raw,
      DashboardUsageByAgentListSchema,
      [],
      { endpoint: "GET /api/dashboard/usage/by-agent" },
    );
  }

  async getDashboardAgentRunTime(
    params: { days?: number; project_id?: string | null; tz?: string },
  ): Promise<DashboardAgentRunTime[]> {
    const search = usageSearchParams(params);
    // `tz` aligns the "last N days" cutoff with the viewer's calendar,
    // matching the per-agent token card.
    const raw = await this.fetch<unknown>(`/api/dashboard/agent-runtime?${search}`);
    return parseWithFallback<DashboardAgentRunTime[]>(
      raw,
      DashboardAgentRunTimeListSchema,
      [],
      { endpoint: "GET /api/dashboard/agent-runtime" },
    );
  }

  async getDashboardRunTimeDaily(
    params: { days?: number; project_id?: string | null; tz?: string },
  ): Promise<DashboardRunTimeDaily[]> {
    const search = usageSearchParams(params);
    // `tz` cuts the day buckets in the viewer's calendar so Time / Tasks
    // align with the Cost / Tokens charts.
    const raw = await this.fetch<unknown>(`/api/dashboard/runtime/daily?${search}`);
    return parseWithFallback<DashboardRunTimeDaily[]>(
      raw,
      DashboardRunTimeDailyListSchema,
      [],
      { endpoint: "GET /api/dashboard/runtime/daily" },
    );
  }

  async initiateListModels(runtimeId: string, idempotencyKey = generateUUID()): Promise<RuntimeModelListRequest> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>(`/api/runtimes/${runtimeId}/models`, {
        method: "POST", extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseRuntimeRequest(
        raw,
        RuntimeModelListRequestSchema,
        runtimeId,
        undefined,
        "POST /api/runtimes/:id/models",
        true,
      );
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async getListModelsResult(
    runtimeId: string,
    requestId: string,
  ): Promise<RuntimeModelListRequest> {
    const raw = await this.fetch<unknown>(`/api/runtimes/${runtimeId}/models/${requestId}`);
    return parseRuntimeRequest(
      raw,
      RuntimeModelListRequestSchema,
      runtimeId,
      requestId,
      "GET /api/runtimes/:id/models/:requestId",
      false,
    );
  }

  async initiateListLocalSkills(
    runtimeId: string,
    idempotencyKey = generateUUID(),
  ): Promise<RuntimeLocalSkillListRequest> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>(`/api/runtimes/${runtimeId}/local-skills`, {
        method: "POST", extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseRuntimeRequest(
        raw,
        RuntimeLocalSkillListRequestSchema,
        runtimeId,
        undefined,
        "POST /api/runtimes/:id/local-skills",
        true,
      );
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async getListLocalSkillsResult(
    runtimeId: string,
    requestId: string,
  ): Promise<RuntimeLocalSkillListRequest> {
    const raw = await this.fetch<unknown>(`/api/runtimes/${runtimeId}/local-skills/${requestId}`);
    return parseRuntimeRequest(
      raw,
      RuntimeLocalSkillListRequestSchema,
      runtimeId,
      requestId,
      "GET /api/runtimes/:id/local-skills/:requestId",
      false,
    );
  }

  async initiateImportLocalSkill(
    runtimeId: string,
    data: CreateRuntimeLocalSkillImportRequest,
    idempotencyKey = generateUUID(),
  ): Promise<RuntimeLocalSkillImportRequest> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>(`/api/runtimes/${runtimeId}/local-skills/import`, {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseRuntimeRequest(
        raw,
        RuntimeLocalSkillImportRequestSchema,
        runtimeId,
        undefined,
        "POST /api/runtimes/:id/local-skills/import",
        true,
      );
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async getImportLocalSkillResult(
    runtimeId: string,
    requestId: string,
  ): Promise<RuntimeLocalSkillImportRequest> {
    const raw = await this.fetch<unknown>(`/api/runtimes/${runtimeId}/local-skills/import/${requestId}`);
    return parseRuntimeRequest(
      raw,
      RuntimeLocalSkillImportRequestSchema,
      runtimeId,
      requestId,
      "GET /api/runtimes/:id/local-skills/import/:requestId",
      false,
    );
  }

  async listAgentTasks(agentId: string): Promise<AgentTask[]> {
    const raw = await this.fetch<unknown>(`/api/agents/${agentId}/tasks`);
    return parseWithFallback(raw, AgentTaskListSchema, [], {
      endpoint: "GET /api/agents/:id/tasks",
    }) as AgentTask[];
  }

  // Workspace-scoped agent task snapshot: every active task
  // (queued/dispatched/running) plus each agent's most recent terminal task.
  // Powers the front-end's "active wins, else latest terminal" presence
  // derivation; one fetch backs every per-agent presence read in the app.
  // Workspace is resolved server-side from the X-Workspace-Slug header.
  async getAgentTaskSnapshot(): Promise<AgentTask[]> {
    const raw = await this.fetch<unknown>(`/api/agent-task-snapshot`);
    return parseWithFallback(raw, AgentTaskListSchema, [], {
      endpoint: "GET /api/agent-task-snapshot",
    }) as AgentTask[];
  }

  // Per-agent daily activity for the last 30 days, anchored on
  // completed_at. One workspace-wide fetch backs both the Agents-list
  // sparkline (uses trailing 7 buckets) and the agent detail "Last 30
  // days" panel (uses all 30).
  async getWorkspaceAgentActivity30d(): Promise<AgentActivityBucket[]> {
    const raw = await this.fetch<unknown>(`/api/agent-activity-30d`);
    return parseWithFallback(raw, AgentActivityBucketListSchema, EMPTY_AGENT_ACTIVITY_BUCKETS, {
      endpoint: "GET /api/agent-activity-30d",
    });
  }

  // Per-agent 30-day total run count for the Agents-list RUNS column.
  async getWorkspaceAgentRunCounts(): Promise<AgentRunCount[]> {
    const raw = await this.fetch<unknown>(`/api/agent-run-counts`);
    return parseWithFallback(raw, AgentRunCountListSchema, EMPTY_AGENT_RUN_COUNTS, {
      endpoint: "GET /api/agent-run-counts",
    });
  }

  async listTaskMessages(taskId: string): Promise<TaskMessagePayload[]> {
    const raw = await this.fetch<unknown>(`/api/tasks/${taskId}/messages`);
    return parseWithFallback(raw, TaskMessageListSchema, EMPTY_TASK_MESSAGES, {
      endpoint: "GET /api/tasks/:id/messages",
    });
  }

  async listTasksByIssue(issueId: string): Promise<AgentTask[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/task-runs`);
    return parseWithFallback(raw, AgentTaskListSchema, [], {
      endpoint: "GET /api/issues/:id/task-runs",
    }) as AgentTask[];
  }

  async listIssueTaskTraceEvents(issueId: string): Promise<IssueTaskTraceResponse> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/trace`);
    return parseWithFallback(
      raw,
      IssueTaskTraceResponseSchema,
      EMPTY_ISSUE_TASK_TRACE_RESPONSE,
      { endpoint: "GET /api/issues/:id/trace" },
    );
  }

  async getIssueExecutionTree(issueId: string): Promise<IssueExecutionTreeResponse> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/execution-tree`);
    return parseWithFallback(
      raw,
      IssueExecutionTreeResponseSchema,
      EMPTY_ISSUE_EXECUTION_TREE,
      { endpoint: "GET /api/issues/:id/execution-tree" },
    );
  }

  async listIssueSOPRuns(issueId: string): Promise<SquadSOPRun[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/sop-runs`);
    return parseWithFallback(raw, IssueSOPRunsResponseSchema, [], {
      endpoint: "GET /api/issues/:id/sop-runs",
    });
  }

  async cancelTask(issueId: string, taskId: string): Promise<AgentTask> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/tasks/${taskId}/cancel`, {
      method: "POST",
    });
    return parseOrThrow(raw, AgentTaskSchema, {
      endpoint: "POST /api/issues/:id/tasks/:taskId/cancel",
      mayHaveCommitted: true,
    });
  }

  async rerunIssue(issueId: string, taskId: string, idempotencyKey = generateUUID()): Promise<AgentTask> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>(`/api/issues/${issueId}/rerun`, {
        method: "POST",
        body: JSON.stringify({ task_id: taskId }),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<AgentTask>(raw, AgentTaskSchema, {
        endpoint: "POST /api/issues/:id/rerun",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  // Inbox
  async listInbox(): Promise<InboxItem[]> {
    const raw = await this.fetch<unknown>("/api/inbox");
    return parseWithFallback(raw, InboxListSchema, [], { endpoint: "GET /api/inbox" });
  }

  async markInboxRead(id: string): Promise<InboxItem> {
    const raw = await this.fetch<unknown>(`/api/inbox/${id}/read`, { method: "POST" });
    return parseOrThrow(raw, InboxItemSchema, { endpoint: "POST /api/inbox/:id/read" });
  }

  async archiveInbox(id: string): Promise<InboxItem> {
    const raw = await this.fetch<unknown>(`/api/inbox/${id}/archive`, { method: "POST" });
    return parseOrThrow(raw, InboxItemSchema, { endpoint: "POST /api/inbox/:id/archive" });
  }

  async markAllInboxRead(): Promise<{ count: number }> {
    const raw = await this.fetch<unknown>("/api/inbox/mark-all-read", { method: "POST" });
    return parseOrThrow(raw, InboxCountResponseSchema, { endpoint: "POST /api/inbox/mark-all-read" });
  }

  async archiveAllInbox(): Promise<{ count: number }> {
    const raw = await this.fetch<unknown>("/api/inbox/archive-all", { method: "POST" });
    return parseOrThrow(raw, InboxCountResponseSchema, { endpoint: "POST /api/inbox/archive-all" });
  }

  async archiveAllReadInbox(): Promise<{ count: number }> {
    const raw = await this.fetch<unknown>("/api/inbox/archive-all-read", { method: "POST" });
    return parseOrThrow(raw, InboxCountResponseSchema, { endpoint: "POST /api/inbox/archive-all-read" });
  }

  async archiveCompletedInbox(): Promise<{ count: number }> {
    const raw = await this.fetch<unknown>("/api/inbox/archive-completed", { method: "POST" });
    return parseOrThrow(raw, InboxCountResponseSchema, { endpoint: "POST /api/inbox/archive-completed" });
  }

  // Notification preferences
  //
  // `workspaceSlug` overrides the default `X-Workspace-Slug` header (which
  // follows the active workspace) so a caller can read a SPECIFIC workspace's
  // preferences — e.g. honoring the mute setting of the workspace an inbox
  // notification came from while the user is viewing a different one (#3766).
  async getNotificationPreferences(workspaceSlug?: string): Promise<NotificationPreferences> {
    const raw = await this.fetch<unknown>(
      "/api/notification-preferences",
      workspaceSlug ? { headers: { "X-Workspace-Slug": workspaceSlug } } : undefined,
    );
    return parseWithFallback(
      raw,
      NotificationPreferenceResponseSchema,
      { preferences: EMPTY_NOTIFICATION_PREFERENCES },
      { endpoint: "GET /api/notification-preferences" },
    ).preferences;
  }

  async updateNotificationPreferences(preferences: NotificationPreferences): Promise<NotificationPreferences> {
    const raw = await this.fetch<unknown>("/api/notification-preferences", {
      method: "PUT",
      body: JSON.stringify({ preferences }),
    });
    return parseOrThrow<{ preferences: NotificationPreferences }>(
      raw,
      NotificationPreferenceResponseSchema,
      { endpoint: "PUT /api/notification-preferences" },
    ).preferences;
  }

  // App Config
  async getConfig(): Promise<AppConfigResponse> {
    const raw = await this.fetch<unknown>("/api/config");
    return parseWithFallback<AppConfigResponse>(raw, AppConfigSchema, EMPTY_APP_CONFIG, {
      endpoint: "GET /api/config",
    });
  }

  // Workspaces
  async listWorkspaces(): Promise<Workspace[]> {
    const raw = await this.fetch<unknown>("/api/workspaces");
    return parseWithFallback(raw, WorkspaceListSchema, [], {
      endpoint: "GET /api/workspaces",
    });
  }

  async getWorkspaceObservabilitySummary(id: string, paramsInput?: { since?: string; squad_id?: string; project_id?: string; agent_id?: string }): Promise<ObservabilitySummary> {
    const params = new URLSearchParams();
    if (paramsInput?.since) params.set("since", paramsInput.since);
    if (paramsInput?.squad_id) params.set("squad_id", paramsInput.squad_id);
    if (paramsInput?.project_id) params.set("project_id", paramsInput.project_id);
    if (paramsInput?.agent_id) params.set("agent_id", paramsInput.agent_id);
    const raw = await this.fetch<unknown>(`/api/workspaces/${id}/observability/summary${params.toString() ? `?${params}` : ""}`);
    return parseWithFallback(raw, ObservabilitySummarySchema, EMPTY_OBSERVABILITY_SUMMARY, {
      endpoint: "GET /api/workspaces/:id/observability/summary",
    }) as ObservabilitySummary;
  }

  async createWorkspace(data: { name: string; slug: string; description?: string; context?: string }, idempotencyKey = generateUUID()): Promise<Workspace> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>("/api/workspaces", {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<Workspace>(raw, WorkspaceSchema, {
        endpoint: "POST /api/workspaces",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async updateWorkspace(id: string, data: { name?: string; description?: string; context?: string; settings?: WorkspaceSettings; repos?: WorkspaceRepo[]; issue_prefix?: string; avatar_url?: string }): Promise<Workspace> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, WorkspaceSchema, {
      endpoint: "PATCH /api/workspaces/:id",
    });
  }

  async resolveWorkspaceRepo(workspaceId: string, data: { url: string; default_branch?: string }): Promise<WorkspaceRepo> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/repos/resolve`, {
      method: "POST",
      responseMayHaveCommitted: false,
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, WorkspaceRepoSchema, {
      endpoint: "POST /api/workspaces/:workspaceId/repos/resolve",
      mayHaveCommitted: false,
    });
  }

  async probeWorkspaceRepo(workspaceId: string, data: { url: string }): Promise<WorkspaceRepoProbeResponse> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/repos/probe`, {
      method: "POST",
      responseMayHaveCommitted: false,
      body: JSON.stringify(data),
    });
    return parseOrThrow(
      raw,
      WorkspaceRepoProbeResponseSchema,
      {
        endpoint: "POST /api/workspaces/:workspaceId/repos/probe",
        mayHaveCommitted: false,
      },
    );
  }

  // Members
  async listMembers(workspaceId: string): Promise<MemberWithUser[]> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/members`);
    return parseWithFallback(raw, MemberWithUserListSchema, [], {
      endpoint: "GET /api/workspaces/:workspaceId/members",
    });
  }

  async createMember(
    workspaceId: string,
    data: CreateMemberRequest,
    idempotencyKey = generateUUID(),
  ): Promise<MemberWithUser> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/members`, {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<MemberWithUser>(raw, MemberWithUserSchema, {
        endpoint: "POST /api/workspaces/:workspaceId/members",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async updateMember(workspaceId: string, memberId: string, data: UpdateMemberRequest): Promise<MemberWithUser> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/members/${memberId}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, MemberWithUserSchema, {
      endpoint: "PATCH /api/workspaces/:workspaceId/members/:memberId",
    });
  }

  async deleteMember(workspaceId: string, memberId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/members/${memberId}`, {
      method: "DELETE",
    });
  }

  async leaveWorkspace(workspaceId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/leave`, {
      method: "POST",
    });
  }

  async deleteWorkspace(workspaceId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}`, {
      method: "DELETE",
    });
  }

  // Skills
  async listSkills(): Promise<SkillSummary[]> {
    const raw = await this.fetch<unknown>("/api/skills");
    return parseWithFallback(raw, SkillSummaryListSchema, EMPTY_SKILL_SUMMARIES, {
      endpoint: "GET /api/skills",
    });
  }

  async getSkill(id: string): Promise<Skill> {
    const raw = await this.fetch<unknown>(`/api/skills/${id}`);
    return parseWithFallback(raw, SkillSchema, EMPTY_SKILL, {
      endpoint: "GET /api/skills/:id",
    });
  }

  async createSkill(
    data: CreateSkillRequest,
    idempotencyKey = generateUUID(),
  ): Promise<Skill> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>("/api/skills", {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<Skill>(raw, SkillSchema, {
        endpoint: "POST /api/skills",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async updateSkill(id: string, data: UpdateSkillRequest): Promise<Skill> {
    const raw = await this.fetch<unknown>(`/api/skills/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, SkillSchema, {
      endpoint: "PUT /api/skills/:id",
      mayHaveCommitted: true,
    });
  }

  async deleteSkill(id: string): Promise<void> {
    await this.fetch(`/api/skills/${id}`, { method: "DELETE" });
  }

  async importSkill(data: { url: string }, idempotencyKey = generateUUID()): Promise<Skill> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>("/api/skills/import", {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<Skill>(raw, SkillSchema, {
        endpoint: "POST /api/skills/import",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async setAgentSkills(agentId: string, data: SetAgentSkillsRequest): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/skills`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  // Incremental attach: POST /skills/add only inserts the given ids (the
  // server upserts with ON CONFLICT DO NOTHING), so callers don't need to
  // read the agent's current skill set first.
  async addAgentSkills(agentId: string, data: SetAgentSkillsRequest): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/skills/add`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  // File Upload & Attachments
  async uploadFile(
    file: File,
    opts?: { issueId?: string; commentId?: string; chatSessionId?: string },
    idempotencyKey = generateUUID(),
  ): Promise<Attachment> {
    const attempt = async () => {
      // Rebuild FormData for every attempt. Browsers may consume a request
      // body stream even when the response is lost; the File itself remains
      // reusable and the server fingerprints its bytes under one request key.
      const formData = new FormData();
      formData.append("file", file);
      if (opts?.issueId) formData.append("issue_id", opts.issueId);
      if (opts?.commentId) formData.append("comment_id", opts.commentId);
      if (opts?.chatSessionId) formData.append("chat_session_id", opts.chatSessionId);

      const response = await this.fetchRaw("/api/upload-file", {
        method: "POST",
        body: formData,
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      const raw = await this.parseSuccessJson<unknown>(response, "POST /api/upload-file", true);
      return parseOrThrow<Attachment>(raw, AttachmentResponseSchema, {
        endpoint: "POST /api/upload-file",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  // Life companion
  async getCompanionProfile(): Promise<CompanionProfileEnvelope> {
    const raw = await this.fetch<unknown>("/api/life/companion");
    return parseWithFallback(raw, CompanionProfileEnvelopeSchema, EMPTY_COMPANION_PROFILE, {
      endpoint: "GET /api/life/companion",
    });
  }

  async setCompanionProfile(agentId: string): Promise<CompanionProfile> {
    const raw = await this.fetch<unknown>("/api/life/companion", {
      method: "PUT",
      body: JSON.stringify({ agent_id: agentId }),
    });
    const envelope = parseOrThrow<{ profile: CompanionProfile }>(raw, CompanionProfileEnvelopeSchema, {
      endpoint: "PUT /api/life/companion",
    });
    return parseOrThrow(envelope.profile, CompanionProfileSchema, {
      endpoint: "PUT /api/life/companion profile",
      mayHaveCommitted: true,
    });
  }

  async listLifeMemories(status?: string): Promise<LifeMemoryListResponse> {
    const query = status ? `?status=${encodeURIComponent(status)}` : "";
    const raw = await this.fetch<unknown>(`/api/life/memories${query}`);
    return parseWithFallback(raw, LifeMemoryListSchema, EMPTY_LIFE_MEMORIES, {
      endpoint: "GET /api/life/memories",
    });
  }

  async updateLifeMemory(id: string, data: UpdateLifeMemoryRequest): Promise<LifeMemory> {
    const raw = await this.fetch<unknown>(`/api/life/memories/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, LifeMemorySchema, { endpoint: "PATCH /api/life/memories/:id" });
  }

  async confirmLifeMemory(id: string): Promise<LifeMemory> {
    const raw = await this.fetch<unknown>(`/api/life/memories/${id}/confirm`, { method: "POST" });
    return parseOrThrow(raw, LifeMemorySchema, { endpoint: "POST /api/life/memories/:id/confirm" });
  }

  async downgradeLifeMemory(id: string, kind: LifeMemoryKind): Promise<LifeMemory> {
    const raw = await this.fetch<unknown>(`/api/life/memories/${id}/downgrade`, {
      method: "POST", body: JSON.stringify({ kind }),
    });
    return parseOrThrow(raw, LifeMemorySchema, { endpoint: "POST /api/life/memories/:id/downgrade" });
  }

  async archiveLifeMemory(id: string): Promise<LifeMemory> {
    const raw = await this.fetch<unknown>(`/api/life/memories/${id}/archive`, { method: "POST" });
    return parseOrThrow(raw, LifeMemorySchema, { endpoint: "POST /api/life/memories/:id/archive" });
  }

  async deleteLifeMemory(id: string): Promise<void> {
    await this.fetch(`/api/life/memories/${id}`, { method: "DELETE" });
  }

	async listLifeMemoryRevisions(id: string): Promise<{ revisions: Array<Record<string, unknown>> }> { return this.fetch(`/api/life/memories/${id}/revisions`); }

  async listLifeProposals(): Promise<LifeProposalListResponse> {
    const raw = await this.fetch<unknown>("/api/life/proposals");
    return parseWithFallback(raw, LifeProposalListSchema, EMPTY_LIFE_PROPOSALS, {
      endpoint: "GET /api/life/proposals",
    });
  }

  async createLifeProposal(data: Omit<LifeProposal, "id" | "status" | "expires_at" | "confirmed_at" | "executed_at" | "created_at" | "updated_at">): Promise<LifeProposal> {
    const raw = await this.fetch<unknown>("/api/life/proposals", {
      method: "POST", body: JSON.stringify(data),
    });
    return parseOrThrow(raw, LifeProposalSchema, { endpoint: "POST /api/life/proposals" });
  }

  async confirmLifeProposal(id: string): Promise<unknown> {
    return this.fetch<unknown>(`/api/life/proposals/${id}/confirm`, { method: "POST" });
  }

	async rejectLifeProposal(id: string): Promise<LifeProposal> {
		const raw = await this.fetch<unknown>(`/api/life/proposals/${id}/reject`, { method: "POST" });
		return parseOrThrow(raw, LifeProposalSchema, { endpoint: "POST /api/life/proposals/:id/reject" });
	}

  async listLifeExperiments(): Promise<LifeExperimentListResponse> {
    const raw = await this.fetch<unknown>("/api/life/experiments");
    return parseWithFallback(raw, LifeExperimentListSchema, EMPTY_LIFE_EXPERIMENTS, {
      endpoint: "GET /api/life/experiments",
    });
  }

  async stopLifeExperimentRound(id: string, reason: string): Promise<LifeExperimentRound> {
    const raw = await this.fetch<unknown>(`/api/life/experiment-rounds/${id}/stop`, {
      method: "POST", body: JSON.stringify({ reason }),
    });
    return parseOrThrow(raw, LifeExperimentRoundSchema, { endpoint: "POST /api/life/experiment-rounds/:id/stop" });
  }

  async reviewLifeExperimentRound(id: string, data: { outcome: string; feelings: string; burden: string; companion_correction: string }): Promise<LifeExperimentRound> {
    const raw = await this.fetch<unknown>(`/api/life/experiment-rounds/${id}/review`, {
      method: "POST", body: JSON.stringify(data),
    });
    return parseOrThrow(raw, LifeExperimentRoundSchema, { endpoint: "POST /api/life/experiment-rounds/:id/review" });
  }

  async listLifeChronicle(): Promise<LifeChronicleListResponse> {
    const raw = await this.fetch<unknown>("/api/life/chronicle");
    return parseWithFallback(raw, LifeChronicleListSchema, EMPTY_LIFE_CHRONICLE, {
      endpoint: "GET /api/life/chronicle",
    });
  }

  async listLifeProactiveChecks(): Promise<LifeProactiveCheckListResponse> {
    const raw = await this.fetch<unknown>("/api/life/proactive-checks");
    return parseWithFallback(raw, LifeProactiveCheckListSchema, EMPTY_LIFE_PROACTIVE_CHECKS, {
      endpoint: "GET /api/life/proactive-checks",
    });
  }

  async updateLifeChronicleLaterUnderstanding(id: string, understanding: string): Promise<LifeChronicleEntry> {
    const raw = await this.fetch<unknown>(`/api/life/chronicle/${id}/later-understanding`, {
      method: "PATCH", body: JSON.stringify({ understanding_later: understanding }),
    });
    return parseOrThrow(raw, LifeChronicleEntrySchema, { endpoint: "PATCH /api/life/chronicle/:id/later-understanding" });
  }

	async listLifeIdentityVersions(): Promise<LifeIdentityListResponse> {
		const raw = await this.fetch<unknown>("/api/life/identity/versions");
		return parseWithFallback(raw, LifeIdentityListSchema, EMPTY_LIFE_IDENTITIES, { endpoint: "GET /api/life/identity/versions" });
	}

	async createLifeIdentityVersion(data: Record<string, unknown>): Promise<unknown> { return this.fetch("/api/life/identity/versions", { method: "POST", body: JSON.stringify(data) }); }
	async activateLifeIdentityVersion(id: string): Promise<unknown> { return this.fetch(`/api/life/identity/versions/${id}/activate`, { method: "POST" }); }
	async listLifeRelationshipEvents(): Promise<LifeRelationshipListResponse> { const raw = await this.fetch<unknown>("/api/life/relationship-events"); return parseWithFallback(raw, LifeRelationshipListSchema, EMPTY_LIFE_RELATIONSHIPS, { endpoint: "GET /api/life/relationship-events" }); }
	async resolveLifeRelationshipEvent(id: string, status: string, resolution: string): Promise<unknown> { return this.fetch(`/api/life/relationship-events/${id}/resolve`, { method: "POST", body: JSON.stringify({ status, resolution }) }); }
	async listLifeMaterials(): Promise<LifeMaterialListResponse> { const raw = await this.fetch<unknown>("/api/life/materials"); return parseWithFallback(raw, LifeMaterialListSchema, EMPTY_LIFE_MATERIALS, { endpoint: "GET /api/life/materials" }); }
	async listLifeInternalThoughts(): Promise<LifeInternalThoughtListResponse> { const raw = await this.fetch<unknown>("/api/life/internal-thoughts"); return parseWithFallback(raw, LifeInternalThoughtListSchema, EMPTY_LIFE_INTERNAL_THOUGHTS, { endpoint: "GET /api/life/internal-thoughts" }); }
	async createLifeMaterial(content: string): Promise<unknown> { return this.fetch("/api/life/materials", { method: "POST", body: JSON.stringify({ content, metadata: {} }) }); }
	async listLifeTopics(): Promise<LifeTopicListResponse> { const raw = await this.fetch<unknown>("/api/life/topics"); return parseWithFallback(raw, LifeTopicListSchema, EMPTY_LIFE_TOPICS, { endpoint: "GET /api/life/topics" }); }
	async updateLifeTopic(id: string, status: string): Promise<unknown> { return this.fetch(`/api/life/topics/${id}`, { method: "PATCH", body: JSON.stringify({ status }) }); }
	async listLifeCommitments(): Promise<LifeCommitmentListResponse> { const raw = await this.fetch<unknown>("/api/life/commitments"); return parseWithFallback(raw, LifeCommitmentListSchema, EMPTY_LIFE_COMMITMENTS, { endpoint: "GET /api/life/commitments" }); }
	async updateLifeCommitment(id: string, data: Record<string, unknown>): Promise<unknown> { return this.fetch(`/api/life/commitments/${id}`, { method: "PATCH", body: JSON.stringify(data) }); }
	async getLifeProactivePolicy(): Promise<LifeProactivePolicy> { const raw = await this.fetch<unknown>("/api/life/proactive-policy"); return parseOrThrow(raw, LifeProactivePolicySchema, { endpoint: "GET /api/life/proactive-policy" }); }
	async updateLifeProactivePolicy(data: LifeProactivePolicy | Record<string, unknown>): Promise<unknown> { return this.fetch("/api/life/proactive-policy", { method: "PUT", body: JSON.stringify(data) }); }
	async listLifeObservers(): Promise<LifeObserverListResponse> { const raw = await this.fetch<unknown>("/api/life/observers"); return parseWithFallback(raw, LifeObserverListSchema, EMPTY_LIFE_OBSERVERS, { endpoint: "GET /api/life/observers" }); }
	async createLifeObserver(data: Record<string, unknown>): Promise<unknown> { return this.fetch("/api/life/observers", { method: "POST", body: JSON.stringify(data) }); }
	async updateLifeObserver(id: string, status: string): Promise<unknown> { return this.fetch(`/api/life/observers/${id}`, { method: "PATCH", body: JSON.stringify({ status }) }); }
	async addLifeObserverKnowledge(id: string, data: { title: string; content: string; source?: string }): Promise<unknown> { return this.fetch(`/api/life/observers/${id}/knowledge`, { method: "POST", body: JSON.stringify(data) }); }
	async createLifeObserverVersion(id: string, data: Record<string, unknown>): Promise<unknown> { return this.fetch(`/api/life/observers/${id}/versions`, { method: "POST", body: JSON.stringify(data) }); }
	async runLifeObserver(id: string): Promise<unknown> { return this.fetch(`/api/life/observers/${id}/run`, { method: "POST" }); }
	async listLifeObservationSeat(): Promise<LifeObservationSeatResponse> { const raw = await this.fetch<unknown>("/api/life/observation-seat"); return parseWithFallback(raw, LifeObservationSeatSchema, EMPTY_LIFE_OBSERVATION_SEAT, { endpoint: "GET /api/life/observation-seat" }); }
	async updateLifeObservationTopic(id: string, data: { status: string; companion_response?: string }): Promise<unknown> { return this.fetch(`/api/life/observation-seat/topics/${id}`, { method: "PATCH", body: JSON.stringify(data) }); }
	async listLifeModules(): Promise<LifeModuleListResponse> { const raw = await this.fetch<unknown>("/api/life/modules"); return parseWithFallback(raw, LifeModuleListSchema, EMPTY_LIFE_MODULES, { endpoint: "GET /api/life/modules" }); }
	async updateLifeModule(id: string, status: string): Promise<unknown> { return this.fetch(`/api/life/modules/${id}`, { method: "PATCH", body: JSON.stringify({ status }) }); }
	async listLifeCognitionJobs(): Promise<LifeCognitionJobListResponse> { const raw = await this.fetch<unknown>("/api/life/cognition-jobs"); return parseWithFallback(raw, LifeCognitionJobListSchema, EMPTY_LIFE_JOBS, { endpoint: "GET /api/life/cognition-jobs" }); }
	async listLifeUpgradeEvaluations(): Promise<LifeUpgradeEvaluationListResponse> { const raw = await this.fetch<unknown>("/api/life/upgrade-evaluations"); return parseWithFallback(raw, LifeUpgradeEvaluationListSchema, EMPTY_LIFE_UPGRADES, { endpoint: "GET /api/life/upgrade-evaluations" }); }
	async createLifeUpgradeEvaluation(data: Record<string, unknown>): Promise<unknown> { return this.fetch("/api/life/upgrade-evaluations", { method: "POST", body: JSON.stringify(data) }); }

  // Chat Sessions
  async listChatSessions(): Promise<ChatSession[]> {
    const raw = await this.fetch<unknown>("/api/chat/sessions");
    return parseWithFallback(raw, ChatSessionListSchema, [], { endpoint: "GET /api/chat/sessions" });
  }

  async createChatSession(
    data: { agent_id: string; title?: string },
    idempotencyKey: string,
  ): Promise<ChatSession> {
    const raw = await this.fetch<unknown>("/api/chat/sessions", {
      method: "POST",
      extraHeaders: { "Idempotency-Key": idempotencyKey },
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, ChatSessionSchema, { endpoint: "POST /api/chat/sessions" });
  }

  async deleteChatSession(id: string): Promise<void> {
    await this.fetch(`/api/chat/sessions/${id}`, { method: "DELETE" });
  }

  async updateChatSession(id: string, data: { title: string }): Promise<ChatSession> {
    const raw = await this.fetch<unknown>(`/api/chat/sessions/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, ChatSessionSchema, { endpoint: "PATCH /api/chat/sessions/:id" });
  }

  async listChatMessagesPage(
    sessionId: string,
    params: { before?: { created_at: string; id: string } | null; limit?: number } = {},
  ): Promise<ChatMessagesPage> {
    const limit = params.limit ?? 50;
    const query = new URLSearchParams({ limit: String(limit) });
    if (params.before) {
      query.set("before_created_at", params.before.created_at);
      query.set("before_id", params.before.id);
    }
    const raw = await this.fetch<unknown>(
      `/api/chat/sessions/${sessionId}/messages/page?${query.toString()}`,
    );
    return parseWithFallback(raw, ChatMessagesPageSchema, EMPTY_CHAT_MESSAGES_PAGE, {
      endpoint: "GET /api/chat/sessions/:id/messages/page",
    });
  }

  async sendChatMessage(
    sessionId: string,
    content: string,
    idempotencyKey: string,
    attachmentIds?: string[],
  ): Promise<SendChatMessageResponse> {
    const body: { content: string; attachment_ids?: string[] } = { content };
    if (attachmentIds && attachmentIds.length > 0) {
      body.attachment_ids = attachmentIds;
    }
    const raw = await this.fetch<unknown>(`/api/chat/sessions/${sessionId}/messages`, {
      method: "POST",
      extraHeaders: { "Idempotency-Key": idempotencyKey },
      body: JSON.stringify(body),
    });
    return parseOrThrow(raw, SendChatMessageResponseSchema, {
      endpoint: "POST /api/chat/sessions/:id/messages",
    });
  }

  async getPendingChatTask(sessionId: string): Promise<ChatPendingTask> {
    const raw = await this.fetch<unknown>(`/api/chat/sessions/${sessionId}/pending-task`);
    return parseWithFallback(raw, ChatPendingTaskSchema, EMPTY_CHAT_PENDING_TASK, { endpoint: "GET /api/chat/sessions/:id/pending-task" });
  }

  async listPendingChatTasks(): Promise<PendingChatTasksResponse> {
    const raw = await this.fetch<unknown>(`/api/chat/pending-tasks`);
    return parseWithFallback(raw, PendingChatTasksResponseSchema, EMPTY_PENDING_CHAT_TASKS_RESPONSE, { endpoint: "GET /api/chat/pending-tasks" });
  }

  async markChatSessionRead(sessionId: string): Promise<void> {
    await this.fetch(`/api/chat/sessions/${sessionId}/read`, { method: "POST" });
  }

  async cancelTaskById(taskId: string): Promise<CancelTaskResponse> {
    const raw = await this.fetch<unknown>(`/api/tasks/${taskId}/cancel`, { method: "POST" });
    return parseOrThrow(raw, CancelTaskResponseSchema, {
      endpoint: "POST /api/tasks/{taskId}/cancel",
    });
  }

  async listAttachments(issueId: string): Promise<Attachment[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/attachments`);
    return parseWithFallback(raw, AttachmentListSchema, EMPTY_ATTACHMENTS, {
      endpoint: "GET /api/issues/:id/attachments",
    });
  }

  // Fetches a fresh attachment metadata record. The server re-signs
  // `download_url` on every call (30 min expiry), so the click-time
  // download flow uses this endpoint to avoid handing the user a stale
  // signed URL cached in TanStack Query.
  async getAttachment(id: string): Promise<Attachment> {
    const raw = await this.fetch<unknown>(`/api/attachments/${id}`);
    return parseWithFallback(raw, AttachmentResponseSchema, EMPTY_ATTACHMENT, {
      endpoint: "GET /api/attachments/{id}",
    });
  }

  // Fetches the raw bytes of a text-previewable attachment.
  //
  // The endpoint sidesteps CloudFront CORS (not configured on the CDN) and
  // bypasses Content-Disposition: attachment for the `text/*` family, both
  // of which would otherwise prevent the renderer from getting the body.
  // The server always replies with `text/plain; charset=utf-8` for safety;
  // the original MIME ships back in the `X-Original-Content-Type` header so
  // the preview dispatcher can choose between markdown / html / plain code.
  //
  // Routes through `fetchRaw` so it inherits the standard auth headers,
  // 401 → handleUnauthorized recovery, request-id logging, and ApiError
  // shape. 413 / 415 are translated to typed `Preview*Error` instances so
  // the modal can render specific fallbacks instead of generic failure.
  async getAttachmentTextContent(
    id: string,
  ): Promise<{ text: string; originalContentType: string }> {
    let res: Response;
    try {
      res = await this.fetchRaw(`/api/attachments/${id}/content`);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 413) throw new PreviewTooLargeError();
        if (err.status === 415) throw new PreviewUnsupportedError();
      }
      throw err;
    }
    return {
      text: await res.text(),
      originalContentType: res.headers.get("X-Original-Content-Type") ?? "",
    };
  }

  // Projects
  async listProjects(params?: { status?: string }): Promise<Project[]> {
    const search = new URLSearchParams();
    if (params?.status) search.set("status", params.status);
    const raw = await this.fetch<unknown>(`/api/projects?${search}`);
    return parseWithFallback(raw, ProjectListSchema, EMPTY_PROJECTS, {
      endpoint: "GET /api/projects",
    });
  }

  async getProject(id: string): Promise<Project> {
    const raw = await this.fetch<unknown>(`/api/projects/${id}`);
    return parseWithFallback(raw, ProjectSchema, EMPTY_PROJECT, {
      endpoint: "GET /api/projects/:id",
    });
  }

  async createProject(
    data: CreateProjectRequest,
    idempotencyKey = generateUUID(),
  ): Promise<Project> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>("/api/projects", {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<Project>(raw, ProjectSchema, {
        endpoint: "POST /api/projects",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async updateProject(id: string, data: UpdateProjectRequest): Promise<Project> {
    const raw = await this.fetch<unknown>(`/api/projects/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, ProjectSchema, {
      endpoint: "PUT /api/projects/:id",
      mayHaveCommitted: true,
    });
  }

  async deleteProject(id: string): Promise<void> {
    await this.fetch(`/api/projects/${id}`, { method: "DELETE" });
  }

  // Project resources
  async listProjectResources(
    projectId: string,
  ): Promise<ProjectResource[]> {
    const raw = await this.fetch<unknown>(`/api/projects/${projectId}/resources`);
    return parseWithFallback(
      raw,
      ProjectResourceListSchema,
      EMPTY_PROJECT_RESOURCES,
      { endpoint: "GET /api/projects/:id/resources" },
    );
  }

  async createProjectResource(
    projectId: string,
    data: CreateProjectResourceRequest,
  ): Promise<ProjectResource> {
    const raw = await this.fetch<unknown>(`/api/projects/${projectId}/resources`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, ProjectResourceSchema, {
      endpoint: "POST /api/projects/:id/resources",
    });
  }

  async updateProjectResource(
    projectId: string,
    resourceId: string,
    data: UpdateProjectResourceRequest,
  ): Promise<ProjectResource> {
    const raw = await this.fetch<unknown>(`/api/projects/${projectId}/resources/${resourceId}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, ProjectResourceSchema, {
      endpoint: "PUT /api/projects/:id/resources/:resourceId",
    });
  }

  async syncProjectResource(
    projectId: string,
    resourceId: string,
  ): Promise<ProjectResource> {
    const raw = await this.fetch<unknown>(`/api/projects/${projectId}/resources/${resourceId}/sync`, {
      method: "POST",
    });
    return parseOrThrow(raw, ProjectResourceSchema, {
      endpoint: "POST /api/projects/:id/resources/:resourceId/sync",
    });
  }

  async deleteProjectResource(
    projectId: string,
    resourceId: string,
  ): Promise<void> {
    await this.fetch(`/api/projects/${projectId}/resources/${resourceId}`, {
      method: "DELETE",
    });
  }

  async listExternalCredentialProfiles(
    provider?: ExternalCredentialProvider,
  ): Promise<ExternalCredentialProfile[]> {
    const search = new URLSearchParams();
    if (provider) search.set("provider", provider);
    const query = search.toString();
    const raw = await this.fetch<unknown>(
      `/api/external-credential-profiles${query ? `?${query}` : ""}`,
    );
    return parseWithFallback(raw, ExternalCredentialProfileListResponseSchema, [], {
      endpoint: "GET /api/external-credential-profiles",
    });
  }

  async getExternalCredentialProfile(id: string): Promise<ExternalCredentialProfile> {
    const raw = await this.fetch<unknown>(`/api/external-credential-profiles/${id}`);
    return parseOrThrow(raw, ExternalCredentialProfileSchema, {
      endpoint: "GET /api/external-credential-profiles/:id",
    });
  }

  async createExternalCredentialProfile(
    data: CreateExternalCredentialProfileRequest,
    idempotencyKey = generateUUID(),
  ): Promise<ExternalCredentialProfile> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>("/api/external-credential-profiles", {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<ExternalCredentialProfile>(raw, ExternalCredentialProfileSchema, {
        endpoint: "POST /api/external-credential-profiles",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async updateExternalCredentialProfile(
    id: string,
    data: UpdateExternalCredentialProfileRequest,
  ): Promise<ExternalCredentialProfile> {
    const raw = await this.fetch<unknown>(`/api/external-credential-profiles/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, ExternalCredentialProfileSchema, {
      endpoint: "PUT /api/external-credential-profiles/:id",
    });
  }

  async testExternalCredentialProfile(
    data: TestExternalCredentialProfileRequest,
  ): Promise<TestExternalCredentialProfileResponse> {
    const raw = await this.fetch<unknown>("/api/external-credential-profiles/test", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(
      raw,
      TestExternalCredentialProfileResponseSchema,
      { endpoint: "POST /api/external-credential-profiles/test" },
    );
  }

  async deleteExternalCredentialProfile(id: string): Promise<void> {
    await this.fetch(`/api/external-credential-profiles/${id}`, {
      method: "DELETE",
    });
  }

  // Labels
  async listLabels(): Promise<Label[]> {
    const raw = await this.fetch<unknown>(`/api/labels`);
    return parseWithFallback(raw, LabelListSchema, EMPTY_LABELS, {
      endpoint: "GET /api/labels",
    });
  }

  async createLabel(data: CreateLabelRequest): Promise<Label> {
    const raw = await this.fetch<unknown>(`/api/labels`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, LabelSchema, {
      endpoint: "POST /api/labels",
      mayHaveCommitted: true,
    });
  }

  async updateLabel(id: string, data: UpdateLabelRequest): Promise<Label> {
    const raw = await this.fetch<unknown>(`/api/labels/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, LabelSchema, {
      endpoint: "PUT /api/labels/:id",
      mayHaveCommitted: true,
    });
  }

  async deleteLabel(id: string): Promise<void> {
    await this.fetch(`/api/labels/${id}`, { method: "DELETE" });
  }

  async listLabelsForIssue(issueId: string): Promise<Label[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/labels`);
    return parseWithFallback(raw, IssueLabelListSchema, EMPTY_LABELS, {
      endpoint: "GET /api/issues/:id/labels",
    });
  }

  async attachLabel(issueId: string, labelId: string): Promise<Label[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/labels`, {
      method: "POST",
      body: JSON.stringify({ label_id: labelId }),
    });
    return parseOrThrow(raw, IssueLabelListSchema, {
      endpoint: "POST /api/issues/:id/labels",
      mayHaveCommitted: true,
    });
  }

  async detachLabel(issueId: string, labelId: string): Promise<Label[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/labels/${labelId}`, {
      method: "DELETE",
    });
    return parseOrThrow(raw, IssueLabelListSchema, {
      endpoint: "DELETE /api/issues/:id/labels/:labelId",
      mayHaveCommitted: true,
    });
  }

  // Pins
  async listPins(): Promise<PinnedItem[]> {
    const raw = await this.fetch<unknown>("/api/pins");
    return parseWithFallback(raw, PinnedItemListSchema, EMPTY_PINNED_ITEM_LIST, {
      endpoint: "GET /api/pins",
    });
  }

  async createPin(data: CreatePinRequest): Promise<PinnedItem> {
    const raw = await this.fetch<unknown>("/api/pins", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PinnedItemSchema, {
      endpoint: "POST /api/pins",
      mayHaveCommitted: true,
    });
  }

  async deletePin(itemType: PinnedItemType, itemId: string): Promise<void> {
    await this.fetch(`/api/pins/${itemType}/${itemId}`, { method: "DELETE" });
  }

  async reorderPins(data: ReorderPinsRequest): Promise<void> {
    await this.fetch("/api/pins/reorder", {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  // Squads
  async listSquads(paramsInput?: { include_archived?: boolean }): Promise<Squad[]> {
    const params = new URLSearchParams();
    if (paramsInput?.include_archived) params.set("include_archived", "true");
    const qs = params.toString();
    const raw = await this.fetch<unknown>(`/api/squads${qs ? `?${qs}` : ""}`);
    return parseWithFallback(raw, SquadListSchema, EMPTY_SQUAD_LIST, {
      endpoint: "GET /api/squads",
    }) as Squad[];
  }

  async getSquad(id: string): Promise<Squad> {
    const raw = await this.fetch<unknown>(`/api/squads/${id}`);
    return parseWithFallback(raw, SquadSchema, EMPTY_SQUAD, {
      endpoint: "GET /api/squads/:id",
    }) as Squad;
  }

  async createSquad(
    data: CreateSquadRequest,
    idempotencyKey = generateUUID(),
  ): Promise<Squad> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>("/api/squads", {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow(raw, SquadSchema, {
        endpoint: "POST /api/squads",
        mayHaveCommitted: true,
      }) as Squad;
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async ensureInternalSquadTemplate(template: InternalSquadTemplateKey | EnsureInternalSquadTemplateRequest): Promise<InternalSquadTemplateResponse> {
    const body =
      typeof template === "string" ? { template_key: template } : template;
    const raw = await this.fetch<unknown>("/api/squads/internal-template", {
      method: "POST",
      body: JSON.stringify(body),
    });
    return parseOrThrow(
      raw,
      InternalSquadTemplateResponseSchema,
      { endpoint: "POST /api/squads/internal-template", mayHaveCommitted: true },
    );
  }

  async updateSquad(id: string, data: UpdateSquadRequest): Promise<Squad> {
    const raw = await this.fetch<unknown>(`/api/squads/${id}`, { method: "PUT", body: JSON.stringify(data) });
    return parseOrThrow(raw, SquadSchema, {
      endpoint: "PUT /api/squads/:id",
    }) as Squad;
  }

  async deleteSquad(id: string): Promise<void> {
    await this.fetch(`/api/squads/${id}`, { method: "DELETE" });
  }

  async restoreSquad(id: string): Promise<Squad> {
    const raw = await this.fetch<unknown>(`/api/squads/${id}/restore`, { method: "POST" });
    return parseOrThrow(raw, SquadSchema, {
      endpoint: "POST /api/squads/:id/restore",
    }) as Squad;
  }

  async listSquadMembers(squadId: string): Promise<SquadMember[]> {
    const raw = await this.fetch<unknown>(`/api/squads/${squadId}/members`);
    return parseWithFallback(raw, SquadMemberListSchema, EMPTY_SQUAD_MEMBERS, {
      endpoint: "GET /api/squads/:id/members",
    });
  }

  async addSquadMember(squadId: string, data: { member_type: string; member_id: string; role?: string }): Promise<SquadMember> {
    const raw = await this.fetch<unknown>(`/api/squads/${squadId}/members`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, SquadMemberSchema, {
      endpoint: "POST /api/squads/:id/members",
      mayHaveCommitted: true,
    });
  }

  async removeSquadMember(squadId: string, data: { member_type: string; member_id: string }): Promise<void> {
    await this.fetch(`/api/squads/${squadId}/members`, { method: "DELETE", body: JSON.stringify(data) });
  }

  async updateSquadMemberRole(squadId: string, data: { member_type: string; member_id: string; role: string }): Promise<SquadMember> {
    const raw = await this.fetch<unknown>(`/api/squads/${squadId}/members/role`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, SquadMemberSchema, {
      endpoint: "PATCH /api/squads/:id/members/role",
      mayHaveCommitted: true,
    });
  }

  // Per-squad members status snapshot: one row per member with derived
  // working/idle/offline/unstable plus the issues each agent is currently
  // running. Parsed with a lenient schema so a new server-side status
  // value or extra field can't white-screen the Squad page (#2143).
  async getSquadMemberStatus(squadId: string): Promise<SquadMemberStatus[]> {
    const raw = await this.fetch<unknown>(`/api/squads/${squadId}/members/status`);
    return parseWithFallback(raw, SquadMemberStatusListResponseSchema, [], {
      endpoint: "GET /api/squads/:id/members/status",
    });
  }

  // Autopilots
  async listAutopilots(params?: { status?: string }): Promise<Autopilot[]> {
    const search = new URLSearchParams();
    if (params?.status) search.set("status", params.status);
    const raw = await this.fetch<unknown>(`/api/autopilots?${search}`);
    return parseWithFallback(
      raw,
      AutopilotListSchema,
      EMPTY_AUTOPILOTS,
      { endpoint: "GET /api/autopilots" },
    );
  }

  async getAutopilot(id: string): Promise<GetAutopilotResponse> {
    const raw = await this.fetch<unknown>(`/api/autopilots/${id}`);
    return parseWithFallback(
      raw,
      GetAutopilotResponseSchema,
      EMPTY_GET_AUTOPILOT_RESPONSE,
      { endpoint: "GET /api/autopilots/:id" },
    );
  }

  async createAutopilot(
    data: CreateAutopilotRequest,
    idempotencyKey = generateUUID(),
  ): Promise<CreateAutopilotResponse> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>("/api/autopilots", {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<CreateAutopilotResponse>(
        raw,
        CreateAutopilotResponseSchema,
        {
          endpoint: "POST /api/autopilots",
          mayHaveCommitted: true,
        },
      );
    };

    return this.retryUnknownMutationOnce(attempt);
  }

  async updateAutopilot(id: string, data: UpdateAutopilotRequest): Promise<Autopilot> {
    const raw = await this.fetch<unknown>(`/api/autopilots/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, AutopilotSchema, {
      endpoint: "PATCH /api/autopilots/:id",
      mayHaveCommitted: true,
    });
  }

  async deleteAutopilot(id: string): Promise<void> {
    await this.fetch(`/api/autopilots/${id}`, { method: "DELETE" });
  }

  async triggerAutopilot(
    id: string,
    idempotencyKey = generateUUID(),
  ): Promise<AutopilotRun> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>(`/api/autopilots/${id}/trigger`, {
        method: "POST",
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<AutopilotRun>(raw, AutopilotRunSchema, {
        endpoint: "POST /api/autopilots/:id/trigger",
        mayHaveCommitted: true,
      });
    };

    return this.retryUnknownMutationOnce(attempt);
  }

  async listAutopilotRuns(id: string, params?: { limit?: number; offset?: number }): Promise<AutopilotRun[]> {
    const search = new URLSearchParams();
    if (params?.limit) search.set("limit", params.limit.toString());
    if (params?.offset) search.set("offset", params.offset.toString());
    const raw = await this.fetch<unknown>(`/api/autopilots/${id}/runs?${search}`);
    return parseWithFallback(
      raw,
      AutopilotRunListSchema,
      EMPTY_AUTOPILOT_RUNS,
      { endpoint: "GET /api/autopilots/:id/runs" },
    );
  }

  // Returns a single run including its full trigger_payload. List responses
  // omit trigger_payload to keep them small (a webhook envelope can be
  // up to 256 KiB × limit rows), so the detail view fetches via this route.
  async getAutopilotRun(autopilotId: string, runId: string): Promise<AutopilotRun> {
    const raw = await this.fetch<unknown>(`/api/autopilots/${autopilotId}/runs/${runId}`);
    return parseWithFallback(raw, AutopilotRunSchema, EMPTY_AUTOPILOT_RUN, {
      endpoint: "GET /api/autopilots/:id/runs/:runId",
    });
  }

  async createAutopilotTrigger(autopilotId: string, data: CreateAutopilotTriggerRequest, idempotencyKey = generateUUID()): Promise<AutopilotTrigger> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>(`/api/autopilots/${autopilotId}/triggers`, {
        method: "POST",
        body: JSON.stringify(data),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<AutopilotTrigger>(raw, AutopilotTriggerSchema, {
        endpoint: "POST /api/autopilots/:id/triggers",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async updateAutopilotTrigger(autopilotId: string, triggerId: string, data: UpdateAutopilotTriggerRequest): Promise<AutopilotTrigger> {
    const raw = await this.fetch<unknown>(`/api/autopilots/${autopilotId}/triggers/${triggerId}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, AutopilotTriggerSchema, {
      endpoint: "PATCH /api/autopilots/:id/triggers/:triggerId",
      mayHaveCommitted: true,
    });
  }

  async deleteAutopilotTrigger(autopilotId: string, triggerId: string): Promise<void> {
    await this.fetch(`/api/autopilots/${autopilotId}/triggers/${triggerId}`, { method: "DELETE" });
  }

  async rotateAutopilotTriggerWebhookToken(
    autopilotId: string,
    triggerId: string,
    idempotencyKey = generateUUID(),
  ): Promise<AutopilotTrigger> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>(
        `/api/autopilots/${autopilotId}/triggers/${triggerId}/rotate-webhook-token`,
        { method: "POST", extraHeaders: { "Idempotency-Key": idempotencyKey } },
      );
      return parseOrThrow<AutopilotTrigger>(raw, AutopilotTriggerSchema, {
        endpoint: "POST /api/autopilots/:id/triggers/:triggerId/rotate-webhook-token",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  // Webhook deliveries — list is slim (no raw_body / selected_headers /
  // response_body); detail returns the full row. Both responses are parsed
  // through a lenient schema so an unknown server-side `status` /
  // `signature_status` value degrades to a generic row instead of dropping
  // the whole list.
  async listAutopilotDeliveries(
    autopilotId: string,
    params?: { limit?: number; offset?: number },
  ): Promise<WebhookDelivery[]> {
    const search = new URLSearchParams();
    if (params?.limit) search.set("limit", params.limit.toString());
    if (params?.offset) search.set("offset", params.offset.toString());
    const raw = await this.fetch<unknown>(
      `/api/autopilots/${autopilotId}/deliveries?${search}`,
    );
    return parseWithFallback(
      raw,
      WebhookDeliveryListSchema,
      EMPTY_WEBHOOK_DELIVERIES,
      { endpoint: "GET /api/autopilots/:id/deliveries" },
    );
  }

  async getAutopilotDelivery(
    autopilotId: string,
    deliveryId: string,
  ): Promise<WebhookDelivery> {
    const raw = await this.fetch<unknown>(
      `/api/autopilots/${autopilotId}/deliveries/${deliveryId}`,
    );
    return parseWithFallback(
      raw,
      WebhookDeliveryResponseSchema,
      { ...EMPTY_WEBHOOK_DELIVERY, id: deliveryId },
      { endpoint: "GET /api/autopilots/:id/deliveries/:deliveryId" },
    );
  }

  // Replay creates a NEW delivery row referencing the original via
  // `replayed_from_delivery_id`. Server rejects replays of
  // signature-invalid / rejected deliveries with 400 — the UI keeps the
  // button disabled for those rows, but the server is the source of truth.
  async replayAutopilotDelivery(
    autopilotId: string,
    deliveryId: string,
    idempotencyKey = generateUUID(),
  ): Promise<WebhookDelivery> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>(
        `/api/autopilots/${autopilotId}/deliveries/${deliveryId}/replay`,
        { method: "POST", extraHeaders: { "Idempotency-Key": idempotencyKey } },
      );
      return parseOrThrow<WebhookDelivery>(
        raw,
        WebhookDeliveryResponseSchema,
        { endpoint: "POST /api/autopilots/:id/deliveries/:deliveryId/replay", mayHaveCommitted: true },
      );
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  // GitHub integration
  async getGitHubConnectURL(workspaceId: string): Promise<GitHubConnectResponse> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/github/connect`);
    return parseWithFallback(raw, GitHubConnectResponseSchema, EMPTY_GITHUB_CONNECT_RESPONSE, {
      endpoint: "GET /api/workspaces/:id/github/connect",
    });
  }

  async listGitHubInstallations(workspaceId: string): Promise<ListGitHubInstallationsResponse> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/github/installations`);
    return parseWithFallback(
      raw,
      GitHubInstallationListResponseSchema,
      EMPTY_GITHUB_INSTALLATION_LIST_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/github/installations" },
    );
  }

  async deleteGitHubInstallation(workspaceId: string, installationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/github/installations/${installationId}`, {
      method: "DELETE",
    });
  }

  async listIssuePullRequests(issueId: string): Promise<GitHubPullRequest[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/pull-requests`);
    return parseWithFallback(
      raw,
      GitHubPullRequestListResponseSchema,
      [],
      { endpoint: "GET /api/issues/:id/pull-requests" },
    );
  }

  // Lark integration
  async listLarkInstallations(workspaceId: string): Promise<ListLarkInstallationsResponse> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/lark/installations`);
    return parseWithFallback(
      raw,
      LarkInstallationListResponseSchema,
      EMPTY_LARK_INSTALLATION_LIST_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/lark/installations" },
    );
  }

  async beginLarkInstall(
    workspaceId: string,
    agentId: string,
    region: "feishu" | "lark",
  ): Promise<BeginLarkInstallResponse> {
    // The user picks the cloud explicitly in the UI ("Bind to Feishu"
    // vs "Bind to Lark"), and the backend POSTs the device-flow `begin`
    // against the corresponding accounts host (accounts.feishu.cn vs
    // accounts.larksuite.com) so the QR renders against the right
    // cloud up front. Region is required across the full request chain so
    // every call site makes a deliberate choice.
    const search = new URLSearchParams({ agent_id: agentId, region });
    const attempt = async () => {
      const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/lark/install/begin?${search.toString()}`, {
        method: "POST",
      });
      return parseOrThrow<BeginLarkInstallResponse>(raw, BeginLarkInstallResponseSchema, {
        endpoint: "POST /api/workspaces/:id/lark/install/begin",
        mayHaveCommitted: true,
      });
    };
    return this.retryUnknownMutationOnce(attempt);
  }

  async getLarkInstallStatus(workspaceId: string, sessionId: string): Promise<LarkInstallStatusResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/lark/install/${sessionId}/status`,
    );
    return parseOrThrow(raw, LarkInstallStatusResponseSchema, {
      endpoint: "GET /api/workspaces/:id/lark/install/:sessionId/status",
      mayHaveCommitted: false,
    });
  }

  async deleteLarkInstallation(workspaceId: string, installationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/lark/installations/${installationId}`, {
      method: "DELETE",
    });
  }

  async redeemLarkBindingToken(
    token: string,
    idempotencyKey = generateUUID(),
  ): Promise<RedeemLarkBindingTokenResponse> {
    const attempt = async () => {
      const raw = await this.fetch<unknown>(`/api/lark/binding/redeem`, {
        method: "POST",
        body: JSON.stringify({ token }),
        extraHeaders: { "Idempotency-Key": idempotencyKey },
      });
      return parseOrThrow<RedeemLarkBindingTokenResponse>(
        raw,
        RedeemLarkBindingTokenResponseSchema,
        { endpoint: "POST /api/lark/binding/redeem", mayHaveCommitted: true },
      );
    };
    return this.retryUnknownMutationOnce(attempt);
  }
}
