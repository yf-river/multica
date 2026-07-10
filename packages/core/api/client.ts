import type {
  Issue,
  CreateIssueRequest,
  UpdateIssueRequest,
  GroupedIssuesResponse,
  IssueStatus,
  ListIssueBucketsResponse,
  ListIssuesResponse,
  SearchIssuesResponse,
  SearchProjectsResponse,
  UpdateMeRequest,
  CreateMemberRequest,
  UpdateMemberRequest,
  ListIssuesParams,
  ListGroupedIssuesParams,
  Agent,
  CreateAgentRequest,
  AgentTemplate,
  AgentTemplateSummary,
  CreateAgentFromTemplateRequest,
  CreateAgentFromTemplateResponse,
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
  WorkspaceRepo,
  WorkspaceRepoProbeResponse,
  MemberWithUser,
  User,
  Skill,
  SkillSummary,
  CreateSkillRequest,
  UpdateSkillRequest,
  SetAgentSkillsRequest,
  PersonalAccessToken,
  CreatePersonalAccessTokenRequest,
  CreatePersonalAccessTokenResponse,
  RuntimeUsage,
  IssueUsageSummary,
  IssueTaskTraceResponse,
  IssueExecutionTreeResponse,
  ListIssueSOPRunsResponse,
  CreateSOPRunRequest,
  SquadSOPRun,
  ObservabilitySummary,
  RuntimeHourlyActivity,
  RuntimeUsageByAgent,
  RuntimeUsageByTask,
  RuntimeUsageByHour,
  DashboardUsageDaily,
  DashboardUsageByAgent,
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
  ListProjectsResponse,
  ProjectResource,
  CreateProjectResourceRequest,
  UpdateProjectResourceRequest,
  ListProjectResourcesResponse,
  Label,
  CreateLabelRequest,
  UpdateLabelRequest,
  ListLabelsResponse,
  IssueLabelsResponse,
  PinnedItem,
  CreatePinRequest,
  PinnedItemType,
  ReorderPinsRequest,
  Autopilot,
  AutopilotTrigger,
  AutopilotRun,
  CreateAutopilotRequest,
  UpdateAutopilotRequest,
  CreateAutopilotTriggerRequest,
  UpdateAutopilotTriggerRequest,
  ListAutopilotsResponse,
  GetAutopilotResponse,
  ListAutopilotRunsResponse,
  ListWebhookDeliveriesResponse,
  WebhookDelivery,
  NotificationPreferenceResponse,
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
  SquadMemberStatusListResponse,
  InternalSquadTemplateKey,
  EnsureInternalSquadTemplateRequest,
  InternalSquadTemplateResponse,
  CreateSquadRequest,
  UpdateSquadRequest,
  PromptLibraryItem,
  PromptLibraryTrial,
  PromptEvaluationAsset,
  PromptEvaluationRun,
  PromptEvaluationRunEvidence,
  PromptEvaluationAssetEvidenceArchivePackage,
  PromptEvaluationAssetEvidenceSnapshotResponse,
  PromptEvaluationEvidenceSnapshot,
  PromptEvaluationEvidenceSnapshotType,
  PromptEvaluationStructuredCase,
  PromptEvaluationAgentRunResponse,
  CreatePromptEvaluationDatasetFromTracesRequest,
  PromptEvaluationDatasetExportResponse,
  ImportPromptEvaluationDatasetRequest,
  ImportPromptEvaluationDatasetResponse,
  CreatePromptEvaluationDatasetVersionRequest,
  PromptEvaluationDatasetFromTracesResponse,
  PromptEvaluationDatasetVersion,
  PromptEvaluationDatasetVersionDiff,
  RestorePromptEvaluationDatasetVersionRequest,
  RestorePromptEvaluationDatasetVersionResponse,
  PromptEvaluationOptimizationCandidate,
  UpdatePromptEvaluationOptimizationCandidateRequest,
  PublishPromptEvaluationOptimizationCandidateResponse,
  ApplyPromptEvaluationSkillCandidateRequest,
  CheckPromptEvaluationSkillFreshnessRequest,
  CreatePromptEvaluationSkillCaseDraftsRequest,
  CreatePromptEvaluationSkillInventoryRequest,
  CreatePromptEvaluationSkillSnapshotRequest,
  PreparePromptEvaluationSkillReEvalRequest,
  RunPromptEvaluationSkillReEvalRequest,
  PromptEvaluationSkillApplyCandidateResponse,
  PromptEvaluationSkillCaseDraftsResult,
  PromptEvaluationSkillFreshnessResult,
  PromptEvaluationSkillInventoryResponse,
  PromptEvaluationSkillReEvalAssetResponse,
  PromptEvaluationSkillReEvalRunResponse,
  PromptEvaluationSkillSnapshotResult,
  ListPromptEvaluationAssetsParams,
  ListPromptEvaluationRunsParams,
  ListPromptEvaluationCasesParams,
  ListPromptEvaluationCaseTagSummariesParams,
  ListPromptEvaluationCaseTagDatasetSummariesParams,
  ListPromptEvaluationCaseOperationsParams,
  ListPromptEvaluationDatasetVersionTagTrendsParams,
  ListPromptEvaluationDimensionScoresParams,
  ListPromptEvaluationDimensionScoreSummariesParams,
  ListPromptEvaluationDimensionScoreTrendsParams,
  ListPromptEvaluationOptimizationCandidatesParams,
  ListPromptEvaluationAssetsResponse,
  ListPromptEvaluationDatasetVersionRowsResponse,
  ListPromptEvaluationDatasetVersionTagTrendsResponse,
  ListPromptEvaluationDatasetVersionsResponse,
  ListPromptEvaluationRunsResponse,
  ListPromptEvaluationTrialsResponse,
  ListPromptEvaluationEvidenceSnapshotsResponse,
  ListPromptEvaluationCasesResponse,
  ListPromptEvaluationCaseTagSummariesResponse,
  ListPromptEvaluationCaseTagDatasetSummariesResponse,
  ListPromptEvaluationCaseOperationsResponse,
  ListPromptEvaluationDimensionScoresResponse,
  ListPromptEvaluationDimensionScoreSummariesResponse,
  ListPromptEvaluationDimensionScoreTrendsResponse,
  ListPromptEvaluationOptimizationCandidatesResponse,
  CreatePromptEvaluationAssetRequest,
  UpdatePromptEvaluationAssetRequest,
  ReviewPromptEvaluationRunRequest,
  CreatePromptEvaluationCaseRequest,
  UpdatePromptEvaluationCaseRequest,
  BulkUpdatePromptEvaluationCaseTagsRequest,
  BulkUpdatePromptEvaluationCaseTagsResponse,
  ListPromptLibraryItemsParams,
  ListPromptLibraryItemsResponse,
  ListPromptLibraryTrialsResponse,
  ListPromptLibraryVersionsResponse,
  CreatePromptLibraryItemRequest,
  CreatePromptLibraryVersionRequest,
  CreatePromptLibraryVersionResponse,
  CreatePromptLibraryTrialRequest,
  UpdatePromptLibraryItemRequest,
  AgentPlaygroundDetail,
  ListAgentPlaygroundExperimentsResponse,
  CreateAgentPlaygroundExperimentRequest,
  JudgeAgentPlaygroundExperimentRequest,
  ExternalCredentialProvider,
  ExternalCredentialProfile,
  ListExternalCredentialProfilesResponse,
  CreateExternalCredentialProfileRequest,
  UpdateExternalCredentialProfileRequest,
  TestExternalCredentialProfileRequest,
  TestExternalCredentialProfileResponse,
} from "../types";
import { type Logger, noopLogger } from "../logger";
import { createRequestId } from "../utils";
import { getCurrentSlug } from "../platform/workspace-storage";
import { ApiResponseValidationError, parseOrThrow, parseWithFallback } from "./schema";
import {
  AgentTemplateSchema,
  AgentSchema,
  AgentListSchema,
  AgentEnvResponseSchema,
  AgentTaskCancellationCountSchema,
  AgentTemplateSummaryListSchema,
  AttachmentResponseSchema,
  CancelTaskResponseSchema,
  ChildIssuesResponseSchema,
  CommentsListSchema,
  CommentTriggerPreviewSchema,
  CreateAgentFromTemplateResponseSchema,
  DashboardAgentRunTimeListSchema,
  DashboardRunTimeDailyListSchema,
  DashboardUsageByAgentListSchema,
  DashboardUsageDailyListSchema,
  EMPTY_AGENT_TEMPLATE_DETAIL,
  EMPTY_AGENT,
  EMPTY_AGENT_ENV_RESPONSE,
  EMPTY_AGENT_TEMPLATE_SUMMARY_LIST,
  EMPTY_APP_CONFIG,
  EMPTY_ATTACHMENT,
  EMPTY_CREATE_AGENT_FROM_TEMPLATE_RESPONSE,
  EMPTY_GROUPED_ISSUES_RESPONSE,
  EMPTY_LIST_ISSUE_BUCKETS_RESPONSE,
  EMPTY_LIST_ISSUES_RESPONSE,
  EMPTY_ISSUE,
  EMPTY_COMMENT,
  EMPTY_REACTION,
  EMPTY_ISSUE_REACTION,
  EMPTY_SQUAD,
  EMPTY_SQUAD_LIST,
  EMPTY_SQUAD_MEMBER_STATUS_LIST,
  EMPTY_SQUAD_SOP_RUN,
  EMPTY_ISSUE_SOP_RUNS_RESPONSE,
  EMPTY_OBSERVABILITY_SUMMARY,
  EMPTY_TIMELINE_ENTRIES,
  EMPTY_USER,
  EMPTY_CREATE_PERSONAL_ACCESS_TOKEN_RESPONSE,
  EMPTY_WORKSPACE,
  EMPTY_WORKSPACE_REPO,
  EMPTY_WORKSPACE_REPO_PROBE_RESPONSE,
  EMPTY_MEMBER_WITH_USER,
  EMPTY_INBOX_ITEM,
  EMPTY_NOTIFICATION_PREFERENCE_RESPONSE,
  EMPTY_CHAT_SESSION,
  EMPTY_CHAT_MESSAGES_PAGE,
  EMPTY_SEND_CHAT_MESSAGE_RESPONSE,
  EMPTY_CHAT_PENDING_TASK,
  EMPTY_PENDING_CHAT_TASKS_RESPONSE,
  EMPTY_PROJECT_RESOURCE,
  EMPTY_PROJECT_RESOURCE_LIST_RESPONSE,
  EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE,
  EMPTY_PROMPT_LIBRARY_ITEM,
  EMPTY_PROMPT_LIBRARY_LIST_RESPONSE,
  EMPTY_PROMPT_LIBRARY_TRIAL,
  EMPTY_PROMPT_LIBRARY_TRIAL_LIST_RESPONSE,
  EMPTY_PROMPT_LIBRARY_VERSION,
  EMPTY_PROMPT_LIBRARY_VERSION_LIST_RESPONSE,
  EMPTY_AGENT_PLAYGROUND_DETAIL,
  EMPTY_AGENT_PLAYGROUND_EXPERIMENT_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_ASSET,
  EMPTY_PROMPT_EVALUATION_ASSET_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_DATASET_VERSION_DIFF,
  EMPTY_PROMPT_EVALUATION_DATASET_VERSION_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_DATASET_VERSION_ROW_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_DATASET_VERSION_TAG_TREND_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_RUN,
  EMPTY_PROMPT_EVALUATION_RUN_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_TRIAL_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_RUN_EVIDENCE,
  EMPTY_PROMPT_EVALUATION_ASSET_EVIDENCE_ARCHIVE_PACKAGE,
  EMPTY_PROMPT_EVALUATION_ASSET_EVIDENCE_SNAPSHOT_RESPONSE,
  EMPTY_PROMPT_EVALUATION_EVIDENCE_SNAPSHOT,
  EMPTY_PROMPT_EVALUATION_EVIDENCE_SNAPSHOT_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_CASE,
  EMPTY_PROMPT_EVALUATION_CASE_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_CASE_TAG_SUMMARY_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_CASE_TAG_DATASET_SUMMARY_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_CASE_OPERATION_LIST_RESPONSE,
  EMPTY_BULK_PROMPT_EVALUATION_CASE_TAGS_RESPONSE,
  EMPTY_PROMPT_EVALUATION_DIMENSION_SCORE_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_DIMENSION_SCORE_SUMMARY_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_DIMENSION_SCORE_TREND_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE,
  EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_SKILL_APPLY_CANDIDATE_RESPONSE,
  EMPTY_PROMPT_EVALUATION_SKILL_CASE_DRAFTS_RESULT,
  EMPTY_PROMPT_EVALUATION_SKILL_FRESHNESS_RESULT,
  EMPTY_PROMPT_EVALUATION_SKILL_INVENTORY_RESPONSE,
  EMPTY_PROMPT_EVALUATION_SKILL_RE_EVAL_ASSET_RESPONSE,
  EMPTY_PROMPT_EVALUATION_SKILL_RE_EVAL_RUN_RESPONSE,
  EMPTY_PROMPT_EVALUATION_SKILL_SNAPSHOT_RESULT,
  EMPTY_PUBLISH_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_RESPONSE,
  EMPTY_RESTORE_PROMPT_EVALUATION_DATASET_VERSION_RESPONSE,
  EMPTY_RUNTIME_PROFILE,
  EMPTY_RUNTIME_PROFILE_LIST_RESPONSE,
  EMPTY_WEBHOOK_DELIVERY,
  AppConfigSchema,
  type AppConfigResponse,
  GroupedIssuesResponseSchema,
  ListIssueBucketsResponseSchema,
  ListAutopilotsResponseSchema,
  EMPTY_LIST_AUTOPILOTS_RESPONSE,
  ListIssuesResponseSchema,
  IssueSchema,
  CommentSchema,
  ReactionSchema,
  IssueReactionSchema,
  ListWebhookDeliveriesResponseSchema,
  RuntimeHourlyActivityListSchema,
  RuntimeUsageByAgentListSchema,
  RuntimeUsageByTaskListSchema,
  RuntimeUsageByHourListSchema,
  RuntimeUsageListSchema,
  RuntimeProfileListResponseSchema,
  RuntimeProfileSchema,
  PromptEvaluationAssetSchema,
  PromptEvaluationAssetListResponseSchema,
  PromptEvaluationDatasetExportResponseSchema,
  ImportPromptEvaluationDatasetResponseSchema,
  PromptEvaluationDatasetFromTracesResponseSchema,
  PromptEvaluationDatasetVersionDiffSchema,
  PromptEvaluationDatasetVersionListResponseSchema,
  PromptEvaluationDatasetVersionRowListResponseSchema,
  PromptEvaluationDatasetVersionTagTrendListResponseSchema,
  PromptEvaluationDatasetVersionSchema,
  RestorePromptEvaluationDatasetVersionResponseSchema,
  PromptEvaluationRunListResponseSchema,
  PromptEvaluationRunSchema,
  PromptEvaluationAgentRunResponseSchema,
  PromptEvaluationTrialListResponseSchema,
  PromptEvaluationRunEvidenceSchema,
  PromptEvaluationAssetEvidenceArchivePackageSchema,
  PromptEvaluationAssetEvidenceSnapshotResponseSchema,
  PromptEvaluationEvidenceSnapshotSchema,
  PromptEvaluationEvidenceSnapshotListResponseSchema,
  PromptEvaluationCaseSchema,
  PromptEvaluationCaseListResponseSchema,
  PromptEvaluationCaseTagSummaryListResponseSchema,
  PromptEvaluationCaseTagDatasetSummaryListResponseSchema,
  PromptEvaluationCaseOperationListResponseSchema,
  BulkUpdatePromptEvaluationCaseTagsResponseSchema,
  PromptEvaluationDimensionScoreListResponseSchema,
  PromptEvaluationDimensionScoreSummaryListResponseSchema,
  PromptEvaluationDimensionScoreTrendListResponseSchema,
  PromptEvaluationOptimizationCandidateSchema,
  PromptEvaluationOptimizationCandidateListResponseSchema,
  PromptEvaluationSkillApplyCandidateResponseSchema,
  PromptEvaluationSkillCaseDraftsResultSchema,
  PromptEvaluationSkillFreshnessResultSchema,
  PromptEvaluationSkillInventoryResponseSchema,
  PromptEvaluationSkillReEvalAssetResponseSchema,
  PromptEvaluationSkillReEvalRunResponseSchema,
  PromptEvaluationSkillSnapshotResultSchema,
  PublishPromptEvaluationOptimizationCandidateResponseSchema,
  PromptLibraryItemSchema,
  PromptLibraryItemListResponseSchema,
  PromptLibraryTrialListResponseSchema,
  PromptLibraryTrialSchema,
  PromptLibraryVersionListResponseSchema,
  CreatePromptLibraryVersionResponseSchema,
  AgentPlaygroundDetailSchema,
  AgentPlaygroundExperimentListResponseSchema,
  IssueSOPRunsResponseSchema,
  ObservabilitySummarySchema,
  SquadSchema,
  SquadSOPRunSchema,
  SquadListSchema,
  SquadMemberStatusListResponseSchema,
  SubscribersListSchema,
  TimelineEntriesSchema,
  UserSchema,
  LoginResponseSchema,
  CliTokenResponseSchema,
  PersonalAccessTokenListSchema,
  CreatePersonalAccessTokenResponseSchema,
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
  ProjectResourceListResponseSchema,
  WebhookDeliveryResponseSchema,
  EMPTY_CANCEL_TASK_RESPONSE,
} from "./schemas";

/** Identifies the calling client to the server.
 *  Sent on every HTTP request as X-Client-Platform / X-Client-Version /
 *  X-Client-OS so the backend can log, gate, or split metrics by client.
 *  See server/internal/middleware/client.go for the receiving end. */
export interface ApiClientIdentity {
  /** Logical client kind. Server expects: "web" | "desktop" | "cli" | "daemon". */
  platform?: string;
  /** Client/app version string (e.g. "0.1.0", git tag, commit). */
  version?: string;
  /** Operating system the client is running on: "macos" | "windows" | "linux". */
  os?: string;
}

export interface ApiClientOptions {
  logger?: Logger;
  onUnauthorized?: () => void;
  /** Identifies the client to the server. Sent as X-Client-* headers. */
  identity?: ApiClientIdentity;
}

type JsonRequestInit = RequestInit & {
  responseMayHaveCommitted?: boolean;
};

export interface LoginResponse {
  token: string;
  user: User;
}

export class ApiError extends Error {
  readonly status: number;
  readonly statusText: string;
  // Raw decoded JSON body (when the server returned one). Carries structured
  // error fields like `code` so callers can branch on machine-readable
  // identifiers instead of pattern-matching the human-readable message.
  readonly body?: unknown;

  constructor(message: string, status: number, statusText: string, body?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.statusText = statusText;
    this.body = body;
  }
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
// rejects the content type. Normally the client's isPreviewable() guard
// catches this earlier, but the two whitelists can drift — surfacing the
// 415 as a typed error makes the drift visible.
export class PreviewUnsupportedError extends Error {
  constructor() {
    super("attachment type not supported for inline preview");
    this.name = "PreviewUnsupportedError";
  }
}

function issueSearchParams(params?: ListIssuesParams) {
  const search = new URLSearchParams();
  if (params?.limit) search.set("limit", String(params.limit));
  if (params?.offset) search.set("offset", String(params.offset));
  if (params?.workspace_id) search.set("workspace_id", params.workspace_id);
  if (params?.status) search.set("status", params.status);
  if (params?.priority) search.set("priority", params.priority);
  if (params?.assignee_id) search.set("assignee_id", params.assignee_id);
  if (params?.assignee_ids?.length) search.set("assignee_ids", params.assignee_ids.join(","));
  if (params?.creator_id) search.set("creator_id", params.creator_id);
  if (params?.project_id) search.set("project_id", params.project_id);
  if (params?.involves_user_id) search.set("involves_user_id", params.involves_user_id);
  if (params?.metadata && Object.keys(params.metadata).length > 0) {
    search.set("metadata", JSON.stringify(params.metadata));
  }
  if (params?.open_only) search.set("open_only", "true");
  if (params?.scheduled) search.set("scheduled", "true");
  if (params?.date_field) search.set("date_field", params.date_field);
  if (params?.date_start) search.set("date_start", params.date_start);
  if (params?.date_end) search.set("date_end", params.date_end);
  if (params?.sort_by) search.set("sort", params.sort_by);
  if (params?.sort_direction) search.set("direction", params.sort_direction);
  return search;
}

export class ApiClient {
  private baseUrl: string;
  private token: string | null = null;
  private logger: Logger;
  private options: ApiClientOptions;

  constructor(baseUrl: string, options?: ApiClientOptions) {
    this.baseUrl = baseUrl;
    this.options = options ?? {};
    this.logger = options?.logger ?? noopLogger;
  }

  getBaseUrl(): string {
    return this.baseUrl;
  }

  setToken(token: string | null) {
    this.token = token;
  }

  private readCsrfToken(): string | null {
    if (typeof document === "undefined") return null;
    const match = document.cookie
      .split("; ")
      .find((c) => c.startsWith("multica_csrf="));
    return match ? match.split("=")[1] ?? null : null;
  }

  private authHeaders(): Record<string, string> {
    const headers: Record<string, string> = {};
    if (this.token) headers["Authorization"] = `Bearer ${this.token}`;
    const slug = getCurrentSlug();
    if (slug) headers["X-Workspace-Slug"] = slug;
    const csrf = this.readCsrfToken();
    if (csrf) headers["X-CSRF-Token"] = csrf;
    const id = this.options.identity;
    if (id?.platform) headers["X-Client-Platform"] = id.platform;
    if (id?.version) headers["X-Client-Version"] = id.version;
    if (id?.os) headers["X-Client-OS"] = id.os;
    return headers;
  }

  private handleUnauthorized() {
    this.token = null;
    // Workspace id is owned by the URL-driven workspace-storage singleton
    // (set by [workspaceSlug]/layout.tsx). On 401, the auth flow navigates
    // to /login which leaves the workspace route, and the next workspace
    // entry will overwrite the id. No clear needed here.
    this.options.onUnauthorized?.();
  }

  private async parseErrorMessage(res: Response, fallback: string): Promise<string> {
    try {
      const data = await res.json() as { error?: string };
      if (typeof data.error === "string" && data.error) return data.error;
    } catch {
      // Ignore non-JSON error bodies.
    }
    return fallback;
  }

  // Reads the response body once for both human-readable error message and
  // structured fields. The Response stream can only be consumed once, so
  // both pieces have to come from a single read.
  private async parseErrorBody(res: Response, fallback: string): Promise<{ message: string; body: unknown }> {
    try {
      const data = await res.json() as { error?: string };
      const message = typeof data.error === "string" && data.error ? data.error : fallback;
      return { message, body: data };
    } catch {
      return { message: fallback, body: undefined };
    }
  }

  // Sends the request with the standard headers (auth, CSRF, request id,
  // client identity) and runs the shared error path (401 → handleUnauthorized,
  // structured ApiError, status-aware log level). Returns the raw Response so
  // callers can decide how to decode the body — JSON for the typed `fetch<T>`
  // path, plain text for the attachment-preview proxy, etc.
  private async fetchRaw(
    path: string,
    init?: RequestInit & { extraHeaders?: Record<string, string> },
  ): Promise<Response> {
    const rid = createRequestId();
    const start = Date.now();
    const method = init?.method ?? "GET";

    const headers: Record<string, string> = {
      "X-Request-ID": rid,
      ...this.authHeaders(),
      ...(init?.extraHeaders ?? {}),
      ...((init?.headers as Record<string, string>) ?? {}),
    };

    this.logger.info(`→ ${method} ${path}`, { rid });

    const res = await fetch(`${this.baseUrl}${path}`, {
      ...init,
      headers,
      credentials: "include",
    });

    if (!res.ok) {
      if (res.status === 401) this.handleUnauthorized();
      const { message, body } = await this.parseErrorBody(res, `API error: ${res.status} ${res.statusText}`);
      const logLevel = res.status >= 500 ? "error" : "warn";
      this.logger[logLevel](`← ${res.status} ${path}`, { rid, duration: `${Date.now() - start}ms`, error: message });
      throw new ApiError(message, res.status, res.statusText, body);
    }

    this.logger.info(`← ${res.status} ${path}`, { rid, duration: `${Date.now() - start}ms` });
    return res;
  }

  private async parseSuccessJson<T>(
    res: Response,
    endpoint: string,
    mayHaveCommitted: boolean,
  ): Promise<T> {
    try {
      return await res.json() as T;
    } catch {
      this.logger.warn("API response body is not valid JSON", {
        endpoint,
        status: res.status,
      });
      throw new ApiResponseValidationError(endpoint, mayHaveCommitted);
    }
  }

  private async fetch<T>(path: string, init?: JsonRequestInit): Promise<T> {
    const { responseMayHaveCommitted, ...requestInit } = init ?? {};
    const method = (requestInit.method ?? "GET").toUpperCase();
    const res = await this.fetchRaw(path, {
      ...requestInit,
      extraHeaders: { "Content-Type": "application/json" },
    });
    // Handle 204 No Content
    if (res.status === 204) {
      return undefined as T;
    }
    const mayHaveCommitted = responseMayHaveCommitted
      ?? !["GET", "HEAD", "OPTIONS"].includes(method);
    return this.parseSuccessJson<T>(res, `${method} ${path}`, mayHaveCommitted);
  }

  // Auth
  async login(account: string, password: string): Promise<LoginResponse> {
    const raw = await this.fetch<unknown>("/auth/login", {
      method: "POST",
      body: JSON.stringify({ account, password }),
    });
    return parseOrThrow(raw, LoginResponseSchema, { token: "", user: EMPTY_USER }, {
      endpoint: "POST /auth/login",
    });
  }

  async logout(): Promise<void> {
    await this.fetch("/auth/logout", { method: "POST" });
  }

  async issueCliToken(): Promise<{ token: string }> {
    const raw = await this.fetch<unknown>("/api/cli-token", { method: "POST" });
    return parseOrThrow(raw, CliTokenResponseSchema, { token: "" }, {
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
    return parseOrThrow(raw, UserSchema, EMPTY_USER, {
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
    const search = new URLSearchParams({ group_by: params.group_by });
    if (params.limit) search.set("limit", String(params.limit));
    if (params.offset) search.set("offset", String(params.offset));
    if (params.workspace_id) search.set("workspace_id", params.workspace_id);
    if (params.statuses?.length) search.set("statuses", params.statuses.join(","));
    if (params.priorities?.length) search.set("priorities", params.priorities.join(","));
    if (params.assignee_types?.length) search.set("assignee_types", params.assignee_types.join(","));
    if (params.assignee_id) search.set("assignee_id", params.assignee_id);
    if (params.assignee_ids?.length) search.set("assignee_ids", params.assignee_ids.join(","));
    if (params.creator_id) search.set("creator_id", params.creator_id);
    if (params.project_id) search.set("project_id", params.project_id);
    if (params.involves_user_id) search.set("involves_user_id", params.involves_user_id);
    if (params.metadata && Object.keys(params.metadata).length > 0) {
      search.set("metadata", JSON.stringify(params.metadata));
    }
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
    if (params.date_field) search.set("date_field", params.date_field);
    if (params.date_start) search.set("date_start", params.date_start);
    if (params.date_end) search.set("date_end", params.date_end);
    if (params.sort_by) search.set("sort", params.sort_by);
    if (params.sort_direction) search.set("direction", params.sort_direction);
    const raw = await this.fetch<unknown>(`/api/issues/grouped?${search}`);
    return parseWithFallback(raw, GroupedIssuesResponseSchema, EMPTY_GROUPED_ISSUES_RESPONSE, {
      endpoint: "GET /api/issues/grouped",
    });
  }

  async searchIssues(params: { q: string; limit?: number; offset?: number; include_closed?: boolean; signal?: AbortSignal }): Promise<SearchIssuesResponse> {
    const search = new URLSearchParams({ q: params.q });
    if (params.limit !== undefined) search.set("limit", String(params.limit));
    if (params.offset !== undefined) search.set("offset", String(params.offset));
    if (params.include_closed) search.set("include_closed", "true");
    return this.fetch(`/api/issues/search?${search}`, params.signal ? { signal: params.signal } : undefined);
  }

  async searchProjects(params: { q: string; limit?: number; offset?: number; include_closed?: boolean; signal?: AbortSignal }): Promise<SearchProjectsResponse> {
    const search = new URLSearchParams({ q: params.q });
    if (params.limit !== undefined) search.set("limit", String(params.limit));
    if (params.offset !== undefined) search.set("offset", String(params.offset));
    if (params.include_closed) search.set("include_closed", "true");
    return this.fetch(`/api/projects/search?${search}`, params.signal ? { signal: params.signal } : undefined);
  }

  async getIssue(id: string): Promise<Issue> {
    const raw = await this.fetch<unknown>(`/api/issues/${id}`);
    return parseWithFallback(raw, IssueSchema, EMPTY_ISSUE, {
      endpoint: "GET /api/issues/:id",
    });
  }

  async createIssue(data: CreateIssueRequest): Promise<Issue> {
    const raw = await this.fetch<unknown>("/api/issues", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, IssueSchema, EMPTY_ISSUE, {
      endpoint: "POST /api/issues",
    });
  }

  async quickCreateIssue(data: {
    agent_id?: string;
    squad_id?: string;
    prompt: string;
    project_id?: string | null;
    parent_issue_id?: string | null;
    status?: string;
    priority?: string;
    start_date?: string | null;
    due_date?: string | null;
    attachment_ids?: string[];
  }): Promise<{
    task_id?: string;
    issue_id?: string;
    identifier?: string;
    source_fetch_status?: string;
  }> {
    return this.fetch("/api/issues/quick-create", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async createFeedback(data: {
    message: string;
    url?: string;
    workspace_id?: string;
  }): Promise<{ id: string; created_at: string }> {
    return this.fetch("/api/feedback", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateIssue(id: string, data: UpdateIssueRequest): Promise<Issue> {
    const raw = await this.fetch<unknown>(`/api/issues/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, IssueSchema, EMPTY_ISSUE, {
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

  async getChildIssueProgress(): Promise<{ progress: { parent_issue_id: string; total: number; done: number }[] }> {
    return this.fetch("/api/issues/child-progress");
  }

  async deleteIssue(id: string): Promise<void> {
    await this.fetch(`/api/issues/${id}`, { method: "DELETE" });
  }

  async batchUpdateIssues(issueIds: string[], updates: UpdateIssueRequest): Promise<{ updated: number }> {
    return this.fetch("/api/issues/batch-update", {
      method: "POST",
      body: JSON.stringify({ issue_ids: issueIds, updates }),
    });
  }

  async batchDeleteIssues(issueIds: string[]): Promise<{ deleted: number }> {
    return this.fetch("/api/issues/batch-delete", {
      method: "POST",
      body: JSON.stringify({ issue_ids: issueIds }),
    });
  }

  // Comments
  async listComments(issueId: string): Promise<Comment[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/comments`);
    return parseWithFallback(raw, CommentsListSchema, [], {
      endpoint: "GET /api/issues/:id/comments",
    });
  }

  async createComment(
    issueId: string,
    content: string,
    type?: string,
    parentId?: string,
    attachmentIds?: string[],
    suppressAgentIds?: string[],
  ): Promise<Comment> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/comments`, {
      method: "POST",
      body: JSON.stringify({
        content,
        type: type ?? "comment",
        ...(parentId ? { parent_id: parentId } : {}),
        ...(attachmentIds?.length ? { attachment_ids: attachmentIds } : {}),
        ...(suppressAgentIds?.length ? { suppress_agent_ids: suppressAgentIds } : {}),
      }),
    });
    return parseOrThrow(raw, CommentSchema, EMPTY_COMMENT, {
      endpoint: "POST /api/issues/:id/comments",
    });
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
    return this.fetch("/api/assignee-frequency");
  }

  async updateComment(commentId: string, content: string, attachmentIds?: string[], suppressAgentIds?: string[]): Promise<Comment> {
    const raw = await this.fetch<unknown>(`/api/comments/${commentId}`, {
      method: "PUT",
      body: JSON.stringify({
        content,
        attachment_ids: attachmentIds,
        ...(suppressAgentIds?.length ? { suppress_agent_ids: suppressAgentIds } : {}),
      }),
    });
    return parseOrThrow(raw, CommentSchema, EMPTY_COMMENT, {
      endpoint: "PUT /api/comments/:id",
    });
  }

  async deleteComment(commentId: string): Promise<void> {
    await this.fetch(`/api/comments/${commentId}`, { method: "DELETE" });
  }

  async resolveComment(commentId: string): Promise<Comment> {
    const raw = await this.fetch<unknown>(`/api/comments/${commentId}/resolve`, { method: "POST" });
    return parseOrThrow(raw, CommentSchema, EMPTY_COMMENT, {
      endpoint: "POST /api/comments/:id/resolve",
    });
  }

  async unresolveComment(commentId: string): Promise<Comment> {
    const raw = await this.fetch<unknown>(`/api/comments/${commentId}/resolve`, { method: "DELETE" });
    return parseOrThrow(raw, CommentSchema, EMPTY_COMMENT, {
      endpoint: "DELETE /api/comments/:id/resolve",
    });
  }

  async addReaction(commentId: string, emoji: string): Promise<Reaction> {
    const raw = await this.fetch<unknown>(`/api/comments/${commentId}/reactions`, {
      method: "POST",
      body: JSON.stringify({ emoji }),
    });
    return parseOrThrow(raw, ReactionSchema, EMPTY_REACTION, {
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
    return parseOrThrow(raw, IssueReactionSchema, EMPTY_ISSUE_REACTION, {
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
    const body: Record<string, string> = {};
    if (userId) body.user_id = userId;
    if (userType) body.user_type = userType;
    await this.fetch(`/api/issues/${issueId}/subscribe`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  async unsubscribeFromIssue(issueId: string, userId?: string, userType?: string): Promise<void> {
    const body: Record<string, string> = {};
    if (userId) body.user_id = userId;
    if (userType) body.user_type = userType;
    await this.fetch(`/api/issues/${issueId}/unsubscribe`, {
      method: "POST",
      body: JSON.stringify(body),
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

  async createAgent(data: CreateAgentRequest): Promise<Agent> {
    const raw = await this.fetch<unknown>("/api/agents", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, AgentSchema, EMPTY_AGENT, { endpoint: "POST /api/agents" });
  }

  async listAgentTemplates(): Promise<AgentTemplateSummary[]> {
    const raw = await this.fetch<unknown>("/api/agent-templates");
    return parseWithFallback(
      raw,
      AgentTemplateSummaryListSchema,
      EMPTY_AGENT_TEMPLATE_SUMMARY_LIST,
      { endpoint: "GET /api/agent-templates" },
    );
  }

  async getAgentTemplate(slug: string): Promise<AgentTemplate> {
    const raw = await this.fetch<unknown>(
      `/api/agent-templates/${encodeURIComponent(slug)}`,
    );
    // Round-trip the requested slug into the fallback so a malformed
    // detail response still produces a navigable record matching the URL
    // the user clicked.
    return parseWithFallback(
      raw,
      AgentTemplateSchema,
      { ...EMPTY_AGENT_TEMPLATE_DETAIL, slug },
      { endpoint: "GET /api/agent-templates/:slug" },
    );
  }

  /** Creates an agent from a curated template. The server fetches every
   *  referenced skill URL in parallel, materializes them into the workspace
   *  (find-or-create by name), and writes the agent + skill bindings in a
   *  single transaction. On any upstream fetch failure, the entire write is
   *  rolled back and the API returns 422 with `failed_urls`. */
  async createAgentFromTemplate(
    data: CreateAgentFromTemplateRequest,
  ): Promise<CreateAgentFromTemplateResponse> {
    const raw = await this.fetch<unknown>("/api/agents/from-template", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(
      raw,
      CreateAgentFromTemplateResponseSchema,
      EMPTY_CREATE_AGENT_FROM_TEMPLATE_RESPONSE,
      { endpoint: "POST /api/agents/from-template" },
    );
  }

  async updateAgent(id: string, data: UpdateAgentRequest): Promise<Agent> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, AgentSchema, EMPTY_AGENT, { endpoint: "PUT /api/agents/:id" });
  }

  async archiveAgent(id: string): Promise<Agent> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/archive`, { method: "POST" });
    return parseOrThrow(raw, AgentSchema, EMPTY_AGENT, { endpoint: "POST /api/agents/:id/archive" });
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
      EMPTY_AGENT_ENV_RESPONSE,
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
      EMPTY_AGENT_ENV_RESPONSE,
      { endpoint: "PUT /api/agents/:id/env" },
    );
  }

  async restoreAgent(id: string): Promise<Agent> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/restore`, { method: "POST" });
    return parseOrThrow(raw, AgentSchema, EMPTY_AGENT, { endpoint: "POST /api/agents/:id/restore" });
  }

  // Bulk-cancel every active task (queued/dispatched/running) for the agent.
  // Permission: agent owner or workspace admin/owner. Server returns the
  // count of cancelled rows; broadcasts task:cancelled for each so other
  // surfaces can clear their live cards.
  async cancelAgentTasks(id: string): Promise<{ cancelled: number }> {
    const raw = await this.fetch<unknown>(`/api/agents/${id}/cancel-tasks`, { method: "POST" });
    return parseOrThrow(raw, AgentTaskCancellationCountSchema, { cancelled: 0 }, { endpoint: "POST /api/agents/:id/cancel-tasks" });
  }

  async listRuntimes(params?: { workspace_id?: string; owner?: "me" }): Promise<AgentRuntime[]> {
    const search = new URLSearchParams();
    if (params?.workspace_id) search.set("workspace_id", params.workspace_id);
    if (params?.owner) search.set("owner", params.owner);
    return this.fetch(`/api/runtimes?${search}`);
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
    return this.fetch(`/api/runtimes/${runtimeId}/archive-agents-and-delete`, {
      method: "POST",
      body: JSON.stringify({ expected_active_agent_ids: expectedActiveAgentIds }),
    });
  }

  async updateRuntime(
    runtimeId: string,
    patch: { scope?: "personal" | "workspace" },
  ): Promise<AgentRuntime> {
    return this.fetch(`/api/runtimes/${runtimeId}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
  }

  // ---------------------------------------------------------------------
  // Custom runtime profiles (MUL-3284). All workspace-scoped: the caller
  // passes the workspace id the same way the runtimes list resolves it.
  // ---------------------------------------------------------------------

  async listRuntimeProfiles(workspaceId: string): Promise<RuntimeProfile[]> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/runtime-profiles`,
    );
    return parseWithFallback(
      raw,
      RuntimeProfileListResponseSchema,
      EMPTY_RUNTIME_PROFILE_LIST_RESPONSE,
      { endpoint: "GET /api/workspaces/:workspaceId/runtime-profiles" },
    ).runtime_profiles;
  }

  async getRuntimeProfile(
    workspaceId: string,
    profileId: string,
  ): Promise<RuntimeProfile> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/runtime-profiles/${profileId}`,
    );
    return parseWithFallback(raw, RuntimeProfileSchema, EMPTY_RUNTIME_PROFILE, {
      endpoint: "GET /api/workspaces/:workspaceId/runtime-profiles/:profileId",
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
    return parseOrThrow(raw, RuntimeProfileSchema, EMPTY_RUNTIME_PROFILE, {
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
    return parseOrThrow(raw, RuntimeProfileSchema, EMPTY_RUNTIME_PROFILE, {
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
    const search = new URLSearchParams();
    if (params?.days) search.set("days", String(params.days));
    // `tz` drives the calendar-day boundary for the trend chart (Viewing
    // layer). Caller-supplied; the backend falls back to user.timezone /
    // UTC if omitted.
    if (params?.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/usage?${search}`,
    );
    return parseWithFallback<RuntimeUsage[]>(raw, RuntimeUsageListSchema, [], {
      endpoint: "GET /api/runtimes/:id/usage",
    });
  }

  async getRuntimeTaskActivity(
    runtimeId: string,
    params?: { tz?: string },
  ): Promise<RuntimeHourlyActivity[]> {
    // Hour-of-day heatmap follows the viewer's tz, like the other reports on
    // this page. Pass the viewer's IANA zone so the server buckets correctly.
    const search = new URLSearchParams();
    if (params?.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/activity?${search}`,
    );
    return parseWithFallback<RuntimeHourlyActivity[]>(
      raw,
      RuntimeHourlyActivityListSchema,
      [],
      { endpoint: "GET /api/runtimes/:id/activity" },
    );
  }

  async getRuntimeUsageByAgent(
    runtimeId: string,
    params?: { days?: number; tz?: string },
  ): Promise<RuntimeUsageByAgent[]> {
    const search = new URLSearchParams();
    if (params?.days) search.set("days", String(params.days));
    if (params?.tz) search.set("tz", params.tz);
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
    const search = new URLSearchParams();
    if (params?.days) search.set("days", String(params.days));
    if (params?.tz) search.set("tz", params.tz);
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

  async getRuntimeUsageByHour(
    runtimeId: string,
    params?: { days?: number; tz?: string },
  ): Promise<RuntimeUsageByHour[]> {
    const search = new URLSearchParams();
    if (params?.days) search.set("days", String(params.days));
    if (params?.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/usage/by-hour?${search}`,
    );
    return parseWithFallback<RuntimeUsageByHour[]>(
      raw,
      RuntimeUsageByHourListSchema,
      [],
      { endpoint: "GET /api/runtimes/:id/usage/by-hour" },
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
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    if (params.tz) search.set("tz", params.tz);
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
  ): Promise<DashboardUsageByAgent[]> {
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    if (params.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(`/api/dashboard/usage/by-agent?${search}`);
    return parseWithFallback<DashboardUsageByAgent[]>(
      raw,
      DashboardUsageByAgentListSchema,
      [],
      { endpoint: "GET /api/dashboard/usage/by-agent" },
    );
  }

  async getDashboardAgentRunTime(
    params: { days?: number; project_id?: string | null; tz?: string },
  ): Promise<DashboardAgentRunTime[]> {
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    // `tz` aligns the "last N days" cutoff with the viewer's calendar,
    // matching the per-agent token card.
    if (params.tz) search.set("tz", params.tz);
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
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    // `tz` cuts the day buckets in the viewer's calendar so Time / Tasks
    // align with the Cost / Tokens charts.
    if (params.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(`/api/dashboard/runtime/daily?${search}`);
    return parseWithFallback<DashboardRunTimeDaily[]>(
      raw,
      DashboardRunTimeDailyListSchema,
      [],
      { endpoint: "GET /api/dashboard/runtime/daily" },
    );
  }

  async initiateListModels(runtimeId: string): Promise<RuntimeModelListRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/models`, { method: "POST" });
  }

  async getListModelsResult(
    runtimeId: string,
    requestId: string,
  ): Promise<RuntimeModelListRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/models/${requestId}`);
  }

  async initiateListLocalSkills(
    runtimeId: string,
  ): Promise<RuntimeLocalSkillListRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/local-skills`, {
      method: "POST",
    });
  }

  async getListLocalSkillsResult(
    runtimeId: string,
    requestId: string,
  ): Promise<RuntimeLocalSkillListRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/local-skills/${requestId}`);
  }

  async initiateImportLocalSkill(
    runtimeId: string,
    data: CreateRuntimeLocalSkillImportRequest,
  ): Promise<RuntimeLocalSkillImportRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/local-skills/import`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async getImportLocalSkillResult(
    runtimeId: string,
    requestId: string,
  ): Promise<RuntimeLocalSkillImportRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/local-skills/import/${requestId}`);
  }

  async listAgentTasks(agentId: string): Promise<AgentTask[]> {
    return this.fetch(`/api/agents/${agentId}/tasks`);
  }

  // Workspace-scoped agent task snapshot: every active task
  // (queued/dispatched/running) plus each agent's most recent terminal task.
  // Powers the front-end's "active wins, else latest terminal" presence
  // derivation; one fetch backs every per-agent presence read in the app.
  // Workspace is resolved server-side from the X-Workspace-Slug header.
  async getAgentTaskSnapshot(): Promise<AgentTask[]> {
    return this.fetch(`/api/agent-task-snapshot`);
  }

  // Per-agent daily activity for the last 30 days, anchored on
  // completed_at. One workspace-wide fetch backs both the Agents-list
  // sparkline (uses trailing 7 buckets) and the agent detail "Last 30
  // days" panel (uses all 30).
  async getWorkspaceAgentActivity30d(): Promise<AgentActivityBucket[]> {
    return this.fetch(`/api/agent-activity-30d`);
  }

  // Per-agent 30-day total run count for the Agents-list RUNS column.
  async getWorkspaceAgentRunCounts(): Promise<AgentRunCount[]> {
    return this.fetch(`/api/agent-run-counts`);
  }

  async listTaskMessages(taskId: string): Promise<TaskMessagePayload[]> {
    return this.fetch(`/api/tasks/${taskId}/messages`);
  }

  async listTasksByIssue(issueId: string): Promise<AgentTask[]> {
    return this.fetch(`/api/issues/${issueId}/task-runs`);
  }

  async getIssueUsage(issueId: string): Promise<IssueUsageSummary> {
    return this.fetch(`/api/issues/${issueId}/usage`);
  }

  async listIssueTaskTraceEvents(issueId: string): Promise<IssueTaskTraceResponse> {
    return this.fetch(`/api/issues/${issueId}/trace`);
  }

  async getIssueExecutionTree(issueId: string): Promise<IssueExecutionTreeResponse> {
    return this.fetch(`/api/issues/${issueId}/execution-tree`);
  }

  async listIssueSOPRuns(issueId: string): Promise<ListIssueSOPRunsResponse> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/sop-runs`);
    return parseWithFallback(raw, IssueSOPRunsResponseSchema, EMPTY_ISSUE_SOP_RUNS_RESPONSE, {
      endpoint: "GET /api/issues/:id/sop-runs",
    }) as ListIssueSOPRunsResponse;
  }

  async createIssueSOPRun(issueId: string, data: CreateSOPRunRequest = {}): Promise<SquadSOPRun> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/sop-runs`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, SquadSOPRunSchema, EMPTY_SQUAD_SOP_RUN, {
      endpoint: "POST /api/issues/:id/sop-runs",
    }) as SquadSOPRun;
  }

  async cancelTask(issueId: string, taskId: string): Promise<AgentTask> {
    return this.fetch(`/api/issues/${issueId}/tasks/${taskId}/cancel`, {
      method: "POST",
    });
  }

  async rerunIssue(issueId: string, taskId?: string): Promise<AgentTask> {
    return this.fetch(`/api/issues/${issueId}/rerun`, {
      method: "POST",
      body: JSON.stringify(taskId ? { task_id: taskId } : {}),
    });
  }

  // Inbox
  async listInbox(): Promise<InboxItem[]> {
    const raw = await this.fetch<unknown>("/api/inbox");
    return parseWithFallback(raw, InboxListSchema, [], { endpoint: "GET /api/inbox" });
  }

  async markInboxRead(id: string): Promise<InboxItem> {
    const raw = await this.fetch<unknown>(`/api/inbox/${id}/read`, { method: "POST" });
    return parseOrThrow(raw, InboxItemSchema, EMPTY_INBOX_ITEM, { endpoint: "POST /api/inbox/:id/read" });
  }

  async archiveInbox(id: string): Promise<InboxItem> {
    const raw = await this.fetch<unknown>(`/api/inbox/${id}/archive`, { method: "POST" });
    return parseOrThrow(raw, InboxItemSchema, EMPTY_INBOX_ITEM, { endpoint: "POST /api/inbox/:id/archive" });
  }

  async markAllInboxRead(): Promise<{ count: number }> {
    const raw = await this.fetch<unknown>("/api/inbox/mark-all-read", { method: "POST" });
    return parseOrThrow(raw, InboxCountResponseSchema, { count: 0 }, { endpoint: "POST /api/inbox/mark-all-read" });
  }

  async archiveAllInbox(): Promise<{ count: number }> {
    const raw = await this.fetch<unknown>("/api/inbox/archive-all", { method: "POST" });
    return parseOrThrow(raw, InboxCountResponseSchema, { count: 0 }, { endpoint: "POST /api/inbox/archive-all" });
  }

  async archiveAllReadInbox(): Promise<{ count: number }> {
    const raw = await this.fetch<unknown>("/api/inbox/archive-all-read", { method: "POST" });
    return parseOrThrow(raw, InboxCountResponseSchema, { count: 0 }, { endpoint: "POST /api/inbox/archive-all-read" });
  }

  async archiveCompletedInbox(): Promise<{ count: number }> {
    const raw = await this.fetch<unknown>("/api/inbox/archive-completed", { method: "POST" });
    return parseOrThrow(raw, InboxCountResponseSchema, { count: 0 }, { endpoint: "POST /api/inbox/archive-completed" });
  }

  // Notification preferences
  //
  // `workspaceSlug` overrides the default `X-Workspace-Slug` header (which
  // follows the active workspace) so a caller can read a SPECIFIC workspace's
  // preferences — e.g. honoring the mute setting of the workspace an inbox
  // notification came from while the user is viewing a different one (#3766).
  async getNotificationPreferences(workspaceSlug?: string): Promise<NotificationPreferenceResponse> {
    const raw = await this.fetch<unknown>(
      "/api/notification-preferences",
      workspaceSlug ? { headers: { "X-Workspace-Slug": workspaceSlug } } : undefined,
    );
    return parseWithFallback(raw, NotificationPreferenceResponseSchema, EMPTY_NOTIFICATION_PREFERENCE_RESPONSE, {
      endpoint: "GET /api/notification-preferences",
    });
  }

  async updateNotificationPreferences(preferences: NotificationPreferences): Promise<NotificationPreferenceResponse> {
    const raw = await this.fetch<unknown>("/api/notification-preferences", {
      method: "PUT",
      body: JSON.stringify({ preferences }),
    });
    return parseOrThrow(raw, NotificationPreferenceResponseSchema, EMPTY_NOTIFICATION_PREFERENCE_RESPONSE, {
      endpoint: "PUT /api/notification-preferences",
    });
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

  async getWorkspace(id: string): Promise<Workspace> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${id}`);
    return parseWithFallback(raw, WorkspaceSchema, EMPTY_WORKSPACE, {
      endpoint: "GET /api/workspaces/:id",
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

  async createWorkspace(data: { name: string; slug: string; description?: string; context?: string }): Promise<Workspace> {
    const raw = await this.fetch<unknown>("/api/workspaces", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, WorkspaceSchema, EMPTY_WORKSPACE, {
      endpoint: "POST /api/workspaces",
    });
  }

  async updateWorkspace(id: string, data: { name?: string; description?: string; context?: string; settings?: Record<string, unknown>; repos?: WorkspaceRepo[]; issue_prefix?: string; avatar_url?: string }): Promise<Workspace> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, WorkspaceSchema, EMPTY_WORKSPACE, {
      endpoint: "PATCH /api/workspaces/:id",
    });
  }

  async resolveWorkspaceRepo(workspaceId: string, data: { url: string; default_branch?: string }): Promise<WorkspaceRepo> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/repos/resolve`, {
      method: "POST",
      responseMayHaveCommitted: false,
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, WorkspaceRepoSchema, EMPTY_WORKSPACE_REPO, {
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
      EMPTY_WORKSPACE_REPO_PROBE_RESPONSE,
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

  async createMember(workspaceId: string, data: CreateMemberRequest): Promise<MemberWithUser> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/members`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, MemberWithUserSchema, EMPTY_MEMBER_WITH_USER, {
      endpoint: "POST /api/workspaces/:workspaceId/members",
    });
  }

  async updateMember(workspaceId: string, memberId: string, data: UpdateMemberRequest): Promise<MemberWithUser> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/members/${memberId}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, MemberWithUserSchema, EMPTY_MEMBER_WITH_USER, {
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
    return this.fetch("/api/skills");
  }

  async getSkill(id: string): Promise<Skill> {
    return this.fetch(`/api/skills/${id}`);
  }

  async createSkill(data: CreateSkillRequest): Promise<Skill> {
    return this.fetch("/api/skills", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateSkill(id: string, data: UpdateSkillRequest): Promise<Skill> {
    return this.fetch(`/api/skills/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  async deleteSkill(id: string): Promise<void> {
    await this.fetch(`/api/skills/${id}`, { method: "DELETE" });
  }

  async importSkill(data: { url: string }): Promise<Skill> {
    return this.fetch("/api/skills/import", {
      method: "POST",
      body: JSON.stringify(data),
    });
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

  // Personal Access Tokens
  async listPersonalAccessTokens(): Promise<PersonalAccessToken[]> {
    const raw = await this.fetch<unknown>("/api/tokens");
    return parseWithFallback(raw, PersonalAccessTokenListSchema, [], {
      endpoint: "GET /api/tokens",
    });
  }

  async createPersonalAccessToken(data: CreatePersonalAccessTokenRequest): Promise<CreatePersonalAccessTokenResponse> {
    const raw = await this.fetch<unknown>("/api/tokens", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(
      raw,
      CreatePersonalAccessTokenResponseSchema,
      EMPTY_CREATE_PERSONAL_ACCESS_TOKEN_RESPONSE,
      { endpoint: "POST /api/tokens" },
    );
  }

  async revokePersonalAccessToken(id: string): Promise<void> {
    await this.fetch(`/api/tokens/${id}`, { method: "DELETE" });
  }

  // File Upload & Attachments
  async uploadFile(
    file: File,
    opts?: { issueId?: string; commentId?: string; chatSessionId?: string },
  ): Promise<Attachment> {
    const formData = new FormData();
    formData.append("file", file);
    if (opts?.issueId) formData.append("issue_id", opts.issueId);
    if (opts?.commentId) formData.append("comment_id", opts.commentId);
    if (opts?.chatSessionId) formData.append("chat_session_id", opts.chatSessionId);

    const rid = createRequestId();
    const start = Date.now();
    this.logger.info("→ POST /api/upload-file", { rid });

    const res = await fetch(`${this.baseUrl}/api/upload-file`, {
      method: "POST",
      headers: this.authHeaders(),
      body: formData,
      credentials: "include",
    });

    if (!res.ok) {
      if (res.status === 401) this.handleUnauthorized();
      const message = await this.parseErrorMessage(res, `Upload failed: ${res.status}`);
      this.logger.error(`← ${res.status} /api/upload-file`, { rid, duration: `${Date.now() - start}ms`, error: message });
      throw new Error(message);
    }

    this.logger.info(`← ${res.status} /api/upload-file`, { rid, duration: `${Date.now() - start}ms` });
    const raw = await this.parseSuccessJson<unknown>(res, "POST /api/upload-file", true);
    return parseOrThrow(raw, AttachmentResponseSchema, EMPTY_ATTACHMENT, {
      endpoint: "POST /api/upload-file",
    });
  }

  // Chat Sessions
  async listChatSessions(params?: { status?: string }): Promise<ChatSession[]> {
    const query = params?.status ? `?status=${params.status}` : "";
    const raw = await this.fetch<unknown>(`/api/chat/sessions${query}`);
    return parseWithFallback(raw, ChatSessionListSchema, [], { endpoint: "GET /api/chat/sessions" });
  }

  async getChatSession(id: string): Promise<ChatSession> {
    const raw = await this.fetch<unknown>(`/api/chat/sessions/${id}`);
    return parseWithFallback(raw, ChatSessionSchema, EMPTY_CHAT_SESSION, { endpoint: "GET /api/chat/sessions/:id" });
  }

  async createChatSession(data: { agent_id: string; title?: string }): Promise<ChatSession> {
    const raw = await this.fetch<unknown>("/api/chat/sessions", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, ChatSessionSchema, EMPTY_CHAT_SESSION, { endpoint: "POST /api/chat/sessions" });
  }

  async deleteChatSession(id: string): Promise<void> {
    await this.fetch(`/api/chat/sessions/${id}`, { method: "DELETE" });
  }

  async updateChatSession(id: string, data: { title: string }): Promise<ChatSession> {
    const raw = await this.fetch<unknown>(`/api/chat/sessions/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, ChatSessionSchema, EMPTY_CHAT_SESSION, { endpoint: "PATCH /api/chat/sessions/:id" });
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
    return parseWithFallback(raw, ChatMessagesPageSchema, { ...EMPTY_CHAT_MESSAGES_PAGE, limit }, {
      endpoint: "GET /api/chat/sessions/:id/messages/page",
    });
  }

  async sendChatMessage(
    sessionId: string,
    content: string,
    attachmentIds?: string[],
  ): Promise<SendChatMessageResponse> {
    const body: { content: string; attachment_ids?: string[] } = { content };
    if (attachmentIds && attachmentIds.length > 0) {
      body.attachment_ids = attachmentIds;
    }
    const raw = await this.fetch<unknown>(`/api/chat/sessions/${sessionId}/messages`, {
      method: "POST",
      body: JSON.stringify(body),
    });
    return parseOrThrow(raw, SendChatMessageResponseSchema, EMPTY_SEND_CHAT_MESSAGE_RESPONSE, {
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
    return parseOrThrow(raw, CancelTaskResponseSchema, EMPTY_CANCEL_TASK_RESPONSE, {
      endpoint: "POST /api/tasks/{taskId}/cancel",
    });
  }

  async listAttachments(issueId: string): Promise<Attachment[]> {
    return this.fetch(`/api/issues/${issueId}/attachments`);
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

  async deleteAttachment(id: string): Promise<void> {
    await this.fetch(`/api/attachments/${id}`, { method: "DELETE" });
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
  async listProjects(params?: { status?: string }): Promise<ListProjectsResponse> {
    const search = new URLSearchParams();
    if (params?.status) search.set("status", params.status);
    return this.fetch(`/api/projects?${search}`);
  }

  async getProject(id: string): Promise<Project> {
    return this.fetch(`/api/projects/${id}`);
  }

  async createProject(data: CreateProjectRequest): Promise<Project> {
    return this.fetch("/api/projects", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateProject(id: string, data: UpdateProjectRequest): Promise<Project> {
    return this.fetch(`/api/projects/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  async deleteProject(id: string): Promise<void> {
    await this.fetch(`/api/projects/${id}`, { method: "DELETE" });
  }

  // Prompt library
  async listPromptLibraryItems(params?: ListPromptLibraryItemsParams): Promise<ListPromptLibraryItemsResponse> {
    const search = new URLSearchParams();
    if (params?.project_id) search.set("project_id", params.project_id);
    if (params?.prompt_type) search.set("prompt_type", params.prompt_type);
    if (params?.status) search.set("status", params.status);
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-library${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptLibraryItemListResponseSchema, EMPTY_PROMPT_LIBRARY_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-library",
    }) as ListPromptLibraryItemsResponse;
  }

  async getPromptLibraryItem(id: string): Promise<PromptLibraryItem> {
    const raw = await this.fetch<unknown>(`/api/prompt-library/${id}`);
    return parseWithFallback(raw, PromptLibraryItemSchema, EMPTY_PROMPT_LIBRARY_ITEM, {
      endpoint: "GET /api/prompt-library/:id",
    }) as PromptLibraryItem;
  }

  async listPromptLibraryVersions(id: string): Promise<ListPromptLibraryVersionsResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-library/${id}/versions`);
    return parseWithFallback(raw, PromptLibraryVersionListResponseSchema, EMPTY_PROMPT_LIBRARY_VERSION_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-library/:id/versions",
    }) as ListPromptLibraryVersionsResponse;
  }

  async listPromptLibraryTrials(id: string): Promise<ListPromptLibraryTrialsResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-library/${id}/trials`);
    return parseWithFallback(raw, PromptLibraryTrialListResponseSchema, EMPTY_PROMPT_LIBRARY_TRIAL_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-library/:id/trials",
    }) as ListPromptLibraryTrialsResponse;
  }

  async createPromptLibraryItem(data: CreatePromptLibraryItemRequest): Promise<PromptLibraryItem> {
    const raw = await this.fetch<unknown>("/api/prompt-library", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptLibraryItemSchema, EMPTY_PROMPT_LIBRARY_ITEM, {
      endpoint: "POST /api/prompt-library",
    }) as PromptLibraryItem;
  }

  async createPromptLibraryVersion(id: string, data: CreatePromptLibraryVersionRequest): Promise<CreatePromptLibraryVersionResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-library/${id}/versions`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(
      raw,
      CreatePromptLibraryVersionResponseSchema,
      { item: EMPTY_PROMPT_LIBRARY_ITEM, version: EMPTY_PROMPT_LIBRARY_VERSION },
      { endpoint: "POST /api/prompt-library/:id/versions" },
    ) as CreatePromptLibraryVersionResponse;
  }

  async createPromptLibraryTrial(id: string, versionId: string, data: CreatePromptLibraryTrialRequest): Promise<PromptLibraryTrial> {
    const raw = await this.fetch<unknown>(`/api/prompt-library/${id}/versions/${versionId}/trials`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptLibraryTrialSchema, EMPTY_PROMPT_LIBRARY_TRIAL, {
      endpoint: "POST /api/prompt-library/:id/versions/:versionId/trials",
    }) as PromptLibraryTrial;
  }

  async updatePromptLibraryItem(id: string, data: UpdatePromptLibraryItemRequest): Promise<PromptLibraryItem> {
    const raw = await this.fetch<unknown>(`/api/prompt-library/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptLibraryItemSchema, EMPTY_PROMPT_LIBRARY_ITEM, {
      endpoint: "PUT /api/prompt-library/:id",
    }) as PromptLibraryItem;
  }

  async deletePromptLibraryItem(id: string): Promise<void> {
    await this.fetch(`/api/prompt-library/${id}`, { method: "DELETE" });
  }

  // Agent playground
  async listAgentPlaygroundExperiments(): Promise<ListAgentPlaygroundExperimentsResponse> {
    const raw = await this.fetch<unknown>("/api/agent-playground-experiments");
    return parseWithFallback(raw, AgentPlaygroundExperimentListResponseSchema, EMPTY_AGENT_PLAYGROUND_EXPERIMENT_LIST_RESPONSE, {
      endpoint: "GET /api/agent-playground-experiments",
    }) as ListAgentPlaygroundExperimentsResponse;
  }

  async getAgentPlaygroundExperiment(id: string): Promise<AgentPlaygroundDetail> {
    const raw = await this.fetch<unknown>(`/api/agent-playground-experiments/${id}`);
    return parseWithFallback(raw, AgentPlaygroundDetailSchema, EMPTY_AGENT_PLAYGROUND_DETAIL, {
      endpoint: "GET /api/agent-playground-experiments/:id",
    }) as AgentPlaygroundDetail;
  }

  async createAgentPlaygroundExperiment(data: CreateAgentPlaygroundExperimentRequest): Promise<AgentPlaygroundDetail> {
    const raw = await this.fetch<unknown>("/api/agent-playground-experiments", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, AgentPlaygroundDetailSchema, EMPTY_AGENT_PLAYGROUND_DETAIL, {
      endpoint: "POST /api/agent-playground-experiments",
    }) as AgentPlaygroundDetail;
  }

  async runAgentPlaygroundExperiment(id: string): Promise<AgentPlaygroundDetail> {
    const raw = await this.fetch<unknown>(`/api/agent-playground-experiments/${id}/run`, { method: "POST" });
    return parseOrThrow(raw, AgentPlaygroundDetailSchema, EMPTY_AGENT_PLAYGROUND_DETAIL, {
      endpoint: "POST /api/agent-playground-experiments/:id/run",
    }) as AgentPlaygroundDetail;
  }

  async syncAgentPlaygroundExperiment(id: string): Promise<AgentPlaygroundDetail> {
    const raw = await this.fetch<unknown>(`/api/agent-playground-experiments/${id}/sync`, { method: "POST" });
    return parseOrThrow(raw, AgentPlaygroundDetailSchema, EMPTY_AGENT_PLAYGROUND_DETAIL, {
      endpoint: "POST /api/agent-playground-experiments/:id/sync",
    }) as AgentPlaygroundDetail;
  }

  async judgeAgentPlaygroundExperiment(id: string, data?: JudgeAgentPlaygroundExperimentRequest): Promise<AgentPlaygroundDetail> {
    const raw = await this.fetch<unknown>(`/api/agent-playground-experiments/${id}/judge`, {
      method: "POST",
      body: JSON.stringify(data ?? {}),
    });
    return parseOrThrow(raw, AgentPlaygroundDetailSchema, EMPTY_AGENT_PLAYGROUND_DETAIL, {
      endpoint: "POST /api/agent-playground-experiments/:id/judge",
    }) as AgentPlaygroundDetail;
  }

  // Prompt evaluation assets
  async listPromptEvaluationAssets(params?: ListPromptEvaluationAssetsParams): Promise<ListPromptEvaluationAssetsResponse> {
    const search = new URLSearchParams();
    if (params?.prompt_id) search.set("prompt_id", params.prompt_id);
    if (params?.asset_type) search.set("asset_type", params.asset_type);
    if (params?.status) search.set("status", params.status);
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationAssetListResponseSchema, EMPTY_PROMPT_EVALUATION_ASSET_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-assets",
    }) as ListPromptEvaluationAssetsResponse;
  }

  async getPromptEvaluationAsset(id: string): Promise<PromptEvaluationAsset> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}`);
    return parseWithFallback(raw, PromptEvaluationAssetSchema, EMPTY_PROMPT_EVALUATION_ASSET, {
      endpoint: "GET /api/prompt-evaluation-assets/:id",
    }) as PromptEvaluationAsset;
  }

  async createPromptEvaluationAsset(data: CreatePromptEvaluationAssetRequest): Promise<PromptEvaluationAsset> {
    const raw = await this.fetch<unknown>("/api/prompt-evaluation-assets", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationAssetSchema, EMPTY_PROMPT_EVALUATION_ASSET, {
      endpoint: "POST /api/prompt-evaluation-assets",
    }) as PromptEvaluationAsset;
  }

  async updatePromptEvaluationAsset(id: string, data: UpdatePromptEvaluationAssetRequest): Promise<PromptEvaluationAsset> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationAssetSchema, EMPTY_PROMPT_EVALUATION_ASSET, {
      endpoint: "PUT /api/prompt-evaluation-assets/:id",
    }) as PromptEvaluationAsset;
  }

  async deletePromptEvaluationAsset(id: string): Promise<void> {
    await this.fetch(`/api/prompt-evaluation-assets/${id}`, { method: "DELETE" });
  }

  async createPromptEvaluationSkillInventory(
    id: string,
    data: CreatePromptEvaluationSkillInventoryRequest,
  ): Promise<PromptEvaluationSkillInventoryResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/skill-inventory`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationSkillInventoryResponseSchema, EMPTY_PROMPT_EVALUATION_SKILL_INVENTORY_RESPONSE, {
      endpoint: "POST /api/prompt-evaluation-assets/:id/skill-inventory",
    }) as PromptEvaluationSkillInventoryResponse;
  }

  async createPromptEvaluationSkillSnapshot(
    id: string,
    data: CreatePromptEvaluationSkillSnapshotRequest,
  ): Promise<PromptEvaluationSkillSnapshotResult> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/skill-snapshot`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationSkillSnapshotResultSchema, EMPTY_PROMPT_EVALUATION_SKILL_SNAPSHOT_RESULT, {
      endpoint: "POST /api/prompt-evaluation-assets/:id/skill-snapshot",
    }) as PromptEvaluationSkillSnapshotResult;
  }

  async createPromptEvaluationSkillCaseDrafts(
    id: string,
    data: CreatePromptEvaluationSkillCaseDraftsRequest,
  ): Promise<PromptEvaluationSkillCaseDraftsResult> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/skill-case-drafts`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationSkillCaseDraftsResultSchema, EMPTY_PROMPT_EVALUATION_SKILL_CASE_DRAFTS_RESULT, {
      endpoint: "POST /api/prompt-evaluation-assets/:id/skill-case-drafts",
    }) as PromptEvaluationSkillCaseDraftsResult;
  }

  async exportPromptEvaluationDataset(id: string): Promise<PromptEvaluationDatasetExportResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/dataset-export`);
    return parseWithFallback(raw, PromptEvaluationDatasetExportResponseSchema, {
      schema: "multica.prompt_evaluation.dataset_export.v1",
      exported_at: "",
      source_asset_id: id,
      asset: EMPTY_PROMPT_EVALUATION_ASSET,
      case_count: 0,
      cases: [],
      payload: {},
    }, {
      endpoint: "GET /api/prompt-evaluation-assets/:id/dataset-export",
    }) as PromptEvaluationDatasetExportResponse;
  }

  async importPromptEvaluationDataset(data: ImportPromptEvaluationDatasetRequest): Promise<ImportPromptEvaluationDatasetResponse> {
    const raw = await this.fetch<unknown>("/api/prompt-evaluation-assets/dataset-import", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, ImportPromptEvaluationDatasetResponseSchema, {
      asset: EMPTY_PROMPT_EVALUATION_ASSET,
      source_asset_id: data.export.source_asset_id,
      case_count: 0,
      cases: [],
    }, {
      endpoint: "POST /api/prompt-evaluation-assets/dataset-import",
    }) as ImportPromptEvaluationDatasetResponse;
  }

  async createPromptEvaluationDatasetFromTraces(
    id: string,
    data: CreatePromptEvaluationDatasetFromTracesRequest = {},
  ): Promise<PromptEvaluationDatasetFromTracesResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/dataset-from-traces`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationDatasetFromTracesResponseSchema, {
      asset: EMPTY_PROMPT_EVALUATION_ASSET,
      cases: [],
      trace_events: [],
      created_count: 0,
      skipped_count: 0,
      source: "trace",
    }, {
      endpoint: "POST /api/prompt-evaluation-assets/:id/dataset-from-traces",
    }) as PromptEvaluationDatasetFromTracesResponse;
  }

  async listPromptEvaluationDatasetVersions(id: string, limit?: number): Promise<ListPromptEvaluationDatasetVersionsResponse> {
    const search = new URLSearchParams();
    if (limit) search.set("limit", String(limit));
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/dataset-versions${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationDatasetVersionListResponseSchema, EMPTY_PROMPT_EVALUATION_DATASET_VERSION_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-assets/:id/dataset-versions",
    }) as ListPromptEvaluationDatasetVersionsResponse;
  }

  async listPromptEvaluationDatasetVersionTagTrends(id: string, params?: ListPromptEvaluationDatasetVersionTagTrendsParams): Promise<ListPromptEvaluationDatasetVersionTagTrendsResponse> {
    const search = new URLSearchParams();
    if (params?.version_limit) search.set("version_limit", String(params.version_limit));
    if (params?.limit) search.set("limit", String(params.limit));
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/dataset-versions/tag-trends${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationDatasetVersionTagTrendListResponseSchema, EMPTY_PROMPT_EVALUATION_DATASET_VERSION_TAG_TREND_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-assets/:id/dataset-versions/tag-trends",
    }) as ListPromptEvaluationDatasetVersionTagTrendsResponse;
  }

  async createPromptEvaluationDatasetVersion(
    id: string,
    data: CreatePromptEvaluationDatasetVersionRequest = {},
  ): Promise<PromptEvaluationDatasetVersion> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/dataset-versions`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationDatasetVersionSchema, {
      id: "",
      workspace_id: "",
      dataset_asset_id: id,
      version: 0,
      version_label: "",
      row_count: 0,
      row_fingerprint: "",
      metadata: {},
      created_by: null,
      created_at: "",
    }, {
      endpoint: "POST /api/prompt-evaluation-assets/:id/dataset-versions",
    }) as PromptEvaluationDatasetVersion;
  }

  async listPromptEvaluationDatasetVersionRows(id: string, versionId: string): Promise<ListPromptEvaluationDatasetVersionRowsResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/dataset-versions/${versionId}/rows`);
    return parseWithFallback(raw, PromptEvaluationDatasetVersionRowListResponseSchema, EMPTY_PROMPT_EVALUATION_DATASET_VERSION_ROW_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-assets/:id/dataset-versions/:versionId/rows",
    }) as ListPromptEvaluationDatasetVersionRowsResponse;
  }

  async diffPromptEvaluationDatasetVersion(
    id: string,
    baseVersionId: string,
    targetVersionId: string,
  ): Promise<PromptEvaluationDatasetVersionDiff> {
    const search = new URLSearchParams({ target_version_id: targetVersionId });
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/dataset-versions/${baseVersionId}/diff?${search}`);
    return parseWithFallback(raw, PromptEvaluationDatasetVersionDiffSchema, EMPTY_PROMPT_EVALUATION_DATASET_VERSION_DIFF, {
      endpoint: "GET /api/prompt-evaluation-assets/:id/dataset-versions/:versionId/diff",
    }) as PromptEvaluationDatasetVersionDiff;
  }

  async restorePromptEvaluationDatasetVersion(
    id: string,
    versionId: string,
    data: RestorePromptEvaluationDatasetVersionRequest = {},
  ): Promise<RestorePromptEvaluationDatasetVersionResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/dataset-versions/${versionId}/restore`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, RestorePromptEvaluationDatasetVersionResponseSchema, EMPTY_RESTORE_PROMPT_EVALUATION_DATASET_VERSION_RESPONSE, {
      endpoint: "POST /api/prompt-evaluation-assets/:id/dataset-versions/:versionId/restore",
    }) as RestorePromptEvaluationDatasetVersionResponse;
  }

  async listPromptEvaluationCases(params?: ListPromptEvaluationCasesParams): Promise<ListPromptEvaluationCasesResponse> {
    const search = new URLSearchParams();
    if (params?.asset_id) search.set("asset_id", params.asset_id);
    if (params?.status) search.set("status", params.status);
    if (params?.source) search.set("source", params.source);
    if (params?.tag) search.set("tag", params.tag);
    if (params?.keyword) search.set("keyword", params.keyword);
    if (params?.limit) search.set("limit", String(params.limit));
    if (params?.cursor) search.set("cursor", params.cursor);
    if (params?.sort_by) search.set("sort_by", params.sort_by);
    if (params?.sort_direction) search.set("sort_direction", params.sort_direction);
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-cases${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationCaseListResponseSchema, EMPTY_PROMPT_EVALUATION_CASE_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-cases",
    }) as ListPromptEvaluationCasesResponse;
  }

  async listPromptEvaluationCaseTagSummaries(params?: ListPromptEvaluationCaseTagSummariesParams): Promise<ListPromptEvaluationCaseTagSummariesResponse> {
    const search = new URLSearchParams();
    if (params?.asset_id) search.set("asset_id", params.asset_id);
    if (params?.status) search.set("status", params.status);
    if (params?.source) search.set("source", params.source);
    if (params?.keyword) search.set("keyword", params.keyword);
    if (params?.limit) search.set("limit", String(params.limit));
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-cases/tag-summaries${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationCaseTagSummaryListResponseSchema, EMPTY_PROMPT_EVALUATION_CASE_TAG_SUMMARY_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-cases/tag-summaries",
    }) as ListPromptEvaluationCaseTagSummariesResponse;
  }

  async listPromptEvaluationCaseTagDatasetSummaries(params?: ListPromptEvaluationCaseTagDatasetSummariesParams): Promise<ListPromptEvaluationCaseTagDatasetSummariesResponse> {
    const search = new URLSearchParams();
    if (params?.status) search.set("status", params.status);
    if (params?.source) search.set("source", params.source);
    if (params?.keyword) search.set("keyword", params.keyword);
    if (params?.limit) search.set("limit", String(params.limit));
    if (params?.top_dataset_limit) search.set("top_dataset_limit", String(params.top_dataset_limit));
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-cases/tag-dataset-summaries${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationCaseTagDatasetSummaryListResponseSchema, EMPTY_PROMPT_EVALUATION_CASE_TAG_DATASET_SUMMARY_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-cases/tag-dataset-summaries",
    }) as ListPromptEvaluationCaseTagDatasetSummariesResponse;
  }

  async listPromptEvaluationCaseOperations(id: string, params?: ListPromptEvaluationCaseOperationsParams): Promise<ListPromptEvaluationCaseOperationsResponse> {
    const search = new URLSearchParams();
    if (params?.limit) search.set("limit", String(params.limit));
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/case-operations${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationCaseOperationListResponseSchema, EMPTY_PROMPT_EVALUATION_CASE_OPERATION_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-assets/:id/case-operations",
    }) as ListPromptEvaluationCaseOperationsResponse;
  }

  async createPromptEvaluationCase(data: CreatePromptEvaluationCaseRequest): Promise<PromptEvaluationStructuredCase> {
    const raw = await this.fetch<unknown>("/api/prompt-evaluation-cases", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationCaseSchema, EMPTY_PROMPT_EVALUATION_CASE, {
      endpoint: "POST /api/prompt-evaluation-cases",
    }) as PromptEvaluationStructuredCase;
  }

  async bulkUpdatePromptEvaluationCaseTags(data: BulkUpdatePromptEvaluationCaseTagsRequest): Promise<BulkUpdatePromptEvaluationCaseTagsResponse> {
    const raw = await this.fetch<unknown>("/api/prompt-evaluation-cases/bulk-tags", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, BulkUpdatePromptEvaluationCaseTagsResponseSchema, EMPTY_BULK_PROMPT_EVALUATION_CASE_TAGS_RESPONSE, {
      endpoint: "POST /api/prompt-evaluation-cases/bulk-tags",
    }) as BulkUpdatePromptEvaluationCaseTagsResponse;
  }

  async updatePromptEvaluationCase(id: string, data: UpdatePromptEvaluationCaseRequest): Promise<PromptEvaluationStructuredCase> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-cases/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationCaseSchema, EMPTY_PROMPT_EVALUATION_CASE, {
      endpoint: "PUT /api/prompt-evaluation-cases/:id",
    }) as PromptEvaluationStructuredCase;
  }

  async deletePromptEvaluationCase(id: string): Promise<void> {
    await this.fetch(`/api/prompt-evaluation-cases/${id}`, { method: "DELETE" });
  }

  async listPromptEvaluationDimensionScores(params?: ListPromptEvaluationDimensionScoresParams): Promise<ListPromptEvaluationDimensionScoresResponse> {
    const search = new URLSearchParams();
    if (params?.run_id) search.set("run_id", params.run_id);
    if (params?.asset_id) search.set("asset_id", params.asset_id);
    if (params?.prompt_id) search.set("prompt_id", params.prompt_id);
    if (params?.status) search.set("status", params.status);
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-dimension-scores${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationDimensionScoreListResponseSchema, EMPTY_PROMPT_EVALUATION_DIMENSION_SCORE_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-dimension-scores",
    }) as ListPromptEvaluationDimensionScoresResponse;
  }

  async listPromptEvaluationDimensionScoreSummaries(params?: ListPromptEvaluationDimensionScoreSummariesParams): Promise<ListPromptEvaluationDimensionScoreSummariesResponse> {
    const search = new URLSearchParams();
    if (params?.asset_id) search.set("asset_id", params.asset_id);
    if (params?.prompt_id) search.set("prompt_id", params.prompt_id);
    if (params?.status) search.set("status", params.status);
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-dimension-score-summaries${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationDimensionScoreSummaryListResponseSchema, EMPTY_PROMPT_EVALUATION_DIMENSION_SCORE_SUMMARY_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-dimension-score-summaries",
    }) as ListPromptEvaluationDimensionScoreSummariesResponse;
  }

  async listPromptEvaluationDimensionScoreTrends(params?: ListPromptEvaluationDimensionScoreTrendsParams): Promise<ListPromptEvaluationDimensionScoreTrendsResponse> {
    const search = new URLSearchParams();
    if (params?.asset_id) search.set("asset_id", params.asset_id);
    if (params?.prompt_id) search.set("prompt_id", params.prompt_id);
    if (params?.status) search.set("status", params.status);
    if (params?.since) search.set("since", params.since);
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-dimension-score-trends${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationDimensionScoreTrendListResponseSchema, EMPTY_PROMPT_EVALUATION_DIMENSION_SCORE_TREND_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-dimension-score-trends",
    }) as ListPromptEvaluationDimensionScoreTrendsResponse;
  }

  async runPromptEvaluationAsset(id: string): Promise<PromptEvaluationAsset> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/run`, { method: "POST" });
    return parseOrThrow(raw, PromptEvaluationAssetSchema, EMPTY_PROMPT_EVALUATION_ASSET, {
      endpoint: "POST /api/prompt-evaluation-assets/:id/run",
    }) as PromptEvaluationAsset;
  }

  async runPromptEvaluationAssetAgent(id: string): Promise<PromptEvaluationAgentRunResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${id}/agent-run`, { method: "POST" });
    return parseOrThrow(raw, PromptEvaluationAgentRunResponseSchema, {
      asset: EMPTY_PROMPT_EVALUATION_ASSET,
      run: EMPTY_PROMPT_EVALUATION_RUN,
      task_id: "",
      chat_session_id: "",
      agent_id: "",
      runtime_id: "",
      model: "",
      status: "",
      message: "",
    }, {
      endpoint: "POST /api/prompt-evaluation-assets/:id/agent-run",
    }) as PromptEvaluationAgentRunResponse;
  }

  async listPromptEvaluationRuns(params?: ListPromptEvaluationRunsParams): Promise<ListPromptEvaluationRunsResponse> {
    const search = new URLSearchParams();
    if (params?.asset_id) search.set("asset_id", params.asset_id);
    if (params?.status) search.set("status", params.status);
    if (params?.since) search.set("since", params.since);
    if (params?.limit) search.set("limit", String(params.limit));
    if (params?.offset) search.set("offset", String(params.offset));
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-runs${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationRunListResponseSchema, EMPTY_PROMPT_EVALUATION_RUN_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-runs",
    }) as ListPromptEvaluationRunsResponse;
  }

  async listPromptEvaluationRunTrials(runId: string): Promise<ListPromptEvaluationTrialsResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-runs/${runId}/trials`);
    return parseWithFallback(raw, PromptEvaluationTrialListResponseSchema, EMPTY_PROMPT_EVALUATION_TRIAL_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-runs/:id/trials",
    }) as ListPromptEvaluationTrialsResponse;
  }

  async getPromptEvaluationRunEvidence(runId: string): Promise<PromptEvaluationRunEvidence> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-runs/${runId}/evidence`);
    return parseWithFallback(raw, PromptEvaluationRunEvidenceSchema, EMPTY_PROMPT_EVALUATION_RUN_EVIDENCE, {
      endpoint: "GET /api/prompt-evaluation-runs/:id/evidence",
    }) as PromptEvaluationRunEvidence;
  }

  async listPromptEvaluationEvidenceSnapshots(runId: string, limit?: number): Promise<ListPromptEvaluationEvidenceSnapshotsResponse> {
    const search = new URLSearchParams();
    if (limit) search.set("limit", String(limit));
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-runs/${runId}/evidence-snapshots${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationEvidenceSnapshotListResponseSchema, EMPTY_PROMPT_EVALUATION_EVIDENCE_SNAPSHOT_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-runs/:id/evidence-snapshots",
    }) as ListPromptEvaluationEvidenceSnapshotsResponse;
  }

  async createPromptEvaluationEvidenceSnapshot(runId: string, snapshotType: PromptEvaluationEvidenceSnapshotType = "手动归档"): Promise<PromptEvaluationEvidenceSnapshot> {
    const search = new URLSearchParams({ snapshot_type: snapshotType });
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-runs/${runId}/evidence-snapshots?${search.toString()}`, { method: "POST" });
    return parseOrThrow(raw, PromptEvaluationEvidenceSnapshotSchema, EMPTY_PROMPT_EVALUATION_EVIDENCE_SNAPSHOT, {
      endpoint: "POST /api/prompt-evaluation-runs/:id/evidence-snapshots",
    }) as PromptEvaluationEvidenceSnapshot;
  }

  async createPromptEvaluationAssetEvidenceSnapshots(assetId: string, snapshotType: PromptEvaluationEvidenceSnapshotType = "验收归档", limit = 20): Promise<PromptEvaluationAssetEvidenceSnapshotResponse> {
    const search = new URLSearchParams({ snapshot_type: snapshotType, limit: String(limit) });
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${assetId}/evidence-snapshots?${search.toString()}`, { method: "POST" });
    return parseOrThrow(raw, PromptEvaluationAssetEvidenceSnapshotResponseSchema, EMPTY_PROMPT_EVALUATION_ASSET_EVIDENCE_SNAPSHOT_RESPONSE, {
      endpoint: "POST /api/prompt-evaluation-assets/:id/evidence-snapshots",
    }) as PromptEvaluationAssetEvidenceSnapshotResponse;
  }

  async getPromptEvaluationAssetEvidenceArchivePackage(assetId: string, snapshotType: PromptEvaluationEvidenceSnapshotType = "验收归档", limit = 20): Promise<PromptEvaluationAssetEvidenceArchivePackage> {
    const search = new URLSearchParams({ snapshot_type: snapshotType, limit: String(limit) });
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-assets/${assetId}/evidence-snapshots/export?${search.toString()}`);
    return parseWithFallback(raw, PromptEvaluationAssetEvidenceArchivePackageSchema, EMPTY_PROMPT_EVALUATION_ASSET_EVIDENCE_ARCHIVE_PACKAGE, {
      endpoint: "GET /api/prompt-evaluation-assets/:id/evidence-snapshots/export",
    }) as PromptEvaluationAssetEvidenceArchivePackage;
  }

  async getPromptEvaluationEvidenceSnapshot(runId: string, snapshotId: string): Promise<PromptEvaluationEvidenceSnapshot> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-runs/${runId}/evidence-snapshots/${snapshotId}`);
    return parseWithFallback(raw, PromptEvaluationEvidenceSnapshotSchema, EMPTY_PROMPT_EVALUATION_EVIDENCE_SNAPSHOT, {
      endpoint: "GET /api/prompt-evaluation-runs/:id/evidence-snapshots/:snapshotId",
    }) as PromptEvaluationEvidenceSnapshot;
  }

  async syncPromptEvaluationRun(runId: string): Promise<PromptEvaluationRun> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-runs/${runId}/sync`, { method: "POST" });
    return parseOrThrow(raw, PromptEvaluationRunSchema, EMPTY_PROMPT_EVALUATION_RUN, {
      endpoint: "POST /api/prompt-evaluation-runs/:id/sync",
    }) as PromptEvaluationRun;
  }

  async cancelPromptEvaluationRun(runId: string): Promise<PromptEvaluationRun> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-runs/${runId}/cancel`, { method: "POST" });
    return parseOrThrow(raw, PromptEvaluationRunSchema, EMPTY_PROMPT_EVALUATION_RUN, {
      endpoint: "POST /api/prompt-evaluation-runs/:id/cancel",
    }) as PromptEvaluationRun;
  }

  async reviewPromptEvaluationRun(runId: string, data: ReviewPromptEvaluationRunRequest): Promise<PromptEvaluationRun> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-runs/${runId}/review`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationRunSchema, EMPTY_PROMPT_EVALUATION_RUN, {
      endpoint: "POST /api/prompt-evaluation-runs/:id/review",
    }) as PromptEvaluationRun;
  }

  async listPromptEvaluationOptimizationCandidates(params?: ListPromptEvaluationOptimizationCandidatesParams): Promise<ListPromptEvaluationOptimizationCandidatesResponse> {
    const search = new URLSearchParams();
    if (params?.run_id) search.set("run_id", params.run_id);
    if (params?.prompt_id) search.set("prompt_id", params.prompt_id);
    if (params?.status) search.set("status", params.status);
    if (params?.limit) search.set("limit", String(params.limit));
    const query = search.toString();
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-optimization-candidates${query ? `?${query}` : ""}`);
    return parseWithFallback(raw, PromptEvaluationOptimizationCandidateListResponseSchema, EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_LIST_RESPONSE, {
      endpoint: "GET /api/prompt-evaluation-optimization-candidates",
    }) as ListPromptEvaluationOptimizationCandidatesResponse;
  }

  async createPromptEvaluationOptimizationCandidate(runId: string): Promise<PromptEvaluationOptimizationCandidate> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-runs/${runId}/optimization-candidates`, { method: "POST" });
    return parseOrThrow(raw, PromptEvaluationOptimizationCandidateSchema, EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE, {
      endpoint: "POST /api/prompt-evaluation-runs/:id/optimization-candidates",
    }) as PromptEvaluationOptimizationCandidate;
  }

  async updatePromptEvaluationOptimizationCandidate(candidateId: string, data: UpdatePromptEvaluationOptimizationCandidateRequest): Promise<PromptEvaluationOptimizationCandidate> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-optimization-candidates/${candidateId}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationOptimizationCandidateSchema, EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE, {
      endpoint: "PUT /api/prompt-evaluation-optimization-candidates/:id",
    }) as PromptEvaluationOptimizationCandidate;
  }

  async publishPromptEvaluationOptimizationCandidate(candidateId: string): Promise<PublishPromptEvaluationOptimizationCandidateResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-optimization-candidates/${candidateId}/publish`, { method: "POST" });
    return parseOrThrow(raw, PublishPromptEvaluationOptimizationCandidateResponseSchema, EMPTY_PUBLISH_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_RESPONSE, {
      endpoint: "POST /api/prompt-evaluation-optimization-candidates/:id/publish",
    }) as PublishPromptEvaluationOptimizationCandidateResponse;
  }

  async checkPromptEvaluationSkillCandidateFreshness(
    candidateId: string,
    data: CheckPromptEvaluationSkillFreshnessRequest = {},
  ): Promise<PromptEvaluationSkillFreshnessResult> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-optimization-candidates/${candidateId}/skill-freshness`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationSkillFreshnessResultSchema, EMPTY_PROMPT_EVALUATION_SKILL_FRESHNESS_RESULT, {
      endpoint: "POST /api/prompt-evaluation-optimization-candidates/:id/skill-freshness",
    }) as PromptEvaluationSkillFreshnessResult;
  }

  async applyPromptEvaluationSkillCandidate(
    candidateId: string,
    data: ApplyPromptEvaluationSkillCandidateRequest,
  ): Promise<PromptEvaluationSkillApplyCandidateResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-optimization-candidates/${candidateId}/skill-apply`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationSkillApplyCandidateResponseSchema, EMPTY_PROMPT_EVALUATION_SKILL_APPLY_CANDIDATE_RESPONSE, {
      endpoint: "POST /api/prompt-evaluation-optimization-candidates/:id/skill-apply",
    }) as PromptEvaluationSkillApplyCandidateResponse;
  }

  async preparePromptEvaluationSkillReEvalAsset(
    candidateId: string,
    data: PreparePromptEvaluationSkillReEvalRequest = {},
  ): Promise<PromptEvaluationSkillReEvalAssetResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-optimization-candidates/${candidateId}/skill-re-eval-asset`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationSkillReEvalAssetResponseSchema, EMPTY_PROMPT_EVALUATION_SKILL_RE_EVAL_ASSET_RESPONSE, {
      endpoint: "POST /api/prompt-evaluation-optimization-candidates/:id/skill-re-eval-asset",
    }) as PromptEvaluationSkillReEvalAssetResponse;
  }

  async runPromptEvaluationSkillReEval(
    candidateId: string,
    data: RunPromptEvaluationSkillReEvalRequest = {},
  ): Promise<PromptEvaluationSkillReEvalRunResponse> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-optimization-candidates/${candidateId}/skill-re-eval-run`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, PromptEvaluationSkillReEvalRunResponseSchema, EMPTY_PROMPT_EVALUATION_SKILL_RE_EVAL_RUN_RESPONSE, {
      endpoint: "POST /api/prompt-evaluation-optimization-candidates/:id/skill-re-eval-run",
    }) as PromptEvaluationSkillReEvalRunResponse;
  }

  async rejectPromptEvaluationOptimizationCandidate(candidateId: string, reason?: string): Promise<PromptEvaluationOptimizationCandidate> {
    const raw = await this.fetch<unknown>(`/api/prompt-evaluation-optimization-candidates/${candidateId}/reject`, {
      method: "POST",
      body: JSON.stringify({ reason: reason ?? "" }),
    });
    return parseOrThrow(raw, PromptEvaluationOptimizationCandidateSchema, EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE, {
      endpoint: "POST /api/prompt-evaluation-optimization-candidates/:id/reject",
    }) as PromptEvaluationOptimizationCandidate;
  }

  // Project resources
  async listProjectResources(
    projectId: string,
  ): Promise<ListProjectResourcesResponse> {
    const raw = await this.fetch<unknown>(`/api/projects/${projectId}/resources`);
    return parseWithFallback(
      raw,
      ProjectResourceListResponseSchema,
      EMPTY_PROJECT_RESOURCE_LIST_RESPONSE,
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
    return parseOrThrow(raw, ProjectResourceSchema, EMPTY_PROJECT_RESOURCE, {
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
    return parseOrThrow(raw, ProjectResourceSchema, EMPTY_PROJECT_RESOURCE, {
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
    return parseOrThrow(raw, ProjectResourceSchema, EMPTY_PROJECT_RESOURCE, {
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
  ): Promise<ListExternalCredentialProfilesResponse> {
    const search = new URLSearchParams();
    if (provider) search.set("provider", provider);
    const query = search.toString();
    return this.fetch(`/api/external-credential-profiles${query ? `?${query}` : ""}`);
  }

  async createExternalCredentialProfile(
    data: CreateExternalCredentialProfileRequest,
  ): Promise<ExternalCredentialProfile> {
    return this.fetch("/api/external-credential-profiles", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateExternalCredentialProfile(
    id: string,
    data: UpdateExternalCredentialProfileRequest,
  ): Promise<ExternalCredentialProfile> {
    return this.fetch(`/api/external-credential-profiles/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  async testExternalCredentialProfile(
    data: TestExternalCredentialProfileRequest,
  ): Promise<TestExternalCredentialProfileResponse> {
    return this.fetch("/api/external-credential-profiles/test", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async deleteExternalCredentialProfile(id: string): Promise<void> {
    await this.fetch(`/api/external-credential-profiles/${id}`, {
      method: "DELETE",
    });
  }

  // Labels
  async listLabels(): Promise<ListLabelsResponse> {
    return this.fetch(`/api/labels`);
  }

  async createLabel(data: CreateLabelRequest): Promise<Label> {
    return this.fetch(`/api/labels`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateLabel(id: string, data: UpdateLabelRequest): Promise<Label> {
    return this.fetch(`/api/labels/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  async deleteLabel(id: string): Promise<void> {
    await this.fetch(`/api/labels/${id}`, { method: "DELETE" });
  }

  async listLabelsForIssue(issueId: string): Promise<IssueLabelsResponse> {
    return this.fetch(`/api/issues/${issueId}/labels`);
  }

  async attachLabel(issueId: string, labelId: string): Promise<IssueLabelsResponse> {
    return this.fetch(`/api/issues/${issueId}/labels`, {
      method: "POST",
      body: JSON.stringify({ label_id: labelId }),
    });
  }

  async detachLabel(issueId: string, labelId: string): Promise<IssueLabelsResponse> {
    return this.fetch(`/api/issues/${issueId}/labels/${labelId}`, {
      method: "DELETE",
    });
  }

  // Pins
  async listPins(): Promise<PinnedItem[]> {
    return this.fetch("/api/pins");
  }

  async createPin(data: CreatePinRequest): Promise<PinnedItem> {
    return this.fetch("/api/pins", {
      method: "POST",
      body: JSON.stringify(data),
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

  async createSquad(data: CreateSquadRequest): Promise<Squad> {
    const raw = await this.fetch<unknown>("/api/squads", { method: "POST", body: JSON.stringify(data) });
    return parseOrThrow(raw, SquadSchema, EMPTY_SQUAD, {
      endpoint: "POST /api/squads",
    }) as Squad;
  }

  async ensureInternalSquadTemplate(template: InternalSquadTemplateKey | EnsureInternalSquadTemplateRequest): Promise<InternalSquadTemplateResponse> {
    const body =
      typeof template === "string" ? { template_key: template } : template;
    return this.fetch("/api/squads/internal-template", {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  async updateSquad(id: string, data: UpdateSquadRequest): Promise<Squad> {
    const raw = await this.fetch<unknown>(`/api/squads/${id}`, { method: "PUT", body: JSON.stringify(data) });
    return parseOrThrow(raw, SquadSchema, EMPTY_SQUAD, {
      endpoint: "PUT /api/squads/:id",
    }) as Squad;
  }

  async deleteSquad(id: string): Promise<void> {
    await this.fetch(`/api/squads/${id}`, { method: "DELETE" });
  }

  async restoreSquad(id: string): Promise<Squad> {
    const raw = await this.fetch<unknown>(`/api/squads/${id}/restore`, { method: "POST" });
    return parseOrThrow(raw, SquadSchema, EMPTY_SQUAD, {
      endpoint: "POST /api/squads/:id/restore",
    }) as Squad;
  }

  async listSquadMembers(squadId: string): Promise<SquadMember[]> {
    return this.fetch(`/api/squads/${squadId}/members`);
  }

  async addSquadMember(squadId: string, data: { member_type: string; member_id: string; role?: string }): Promise<SquadMember> {
    return this.fetch(`/api/squads/${squadId}/members`, { method: "POST", body: JSON.stringify(data) });
  }

  async removeSquadMember(squadId: string, data: { member_type: string; member_id: string }): Promise<void> {
    await this.fetch(`/api/squads/${squadId}/members`, { method: "DELETE", body: JSON.stringify(data) });
  }

  async updateSquadMemberRole(squadId: string, data: { member_type: string; member_id: string; role: string }): Promise<SquadMember> {
    return this.fetch(`/api/squads/${squadId}/members/role`, { method: "PATCH", body: JSON.stringify(data) });
  }

  // Per-squad members status snapshot: one row per member with derived
  // working/idle/offline/unstable plus the issues each agent is currently
  // running. Parsed with a lenient schema so a new server-side status
  // value or extra field can't white-screen the Squad page (#2143).
  async getSquadMemberStatus(squadId: string): Promise<SquadMemberStatusListResponse> {
    const raw = await this.fetch<unknown>(`/api/squads/${squadId}/members/status`);
    return parseWithFallback(raw, SquadMemberStatusListResponseSchema, EMPTY_SQUAD_MEMBER_STATUS_LIST, {
      endpoint: "GET /api/squads/:id/members/status",
    }) as SquadMemberStatusListResponse;
  }

  // Autopilots
  async listAutopilots(params?: { status?: string }): Promise<ListAutopilotsResponse> {
    const search = new URLSearchParams();
    if (params?.status) search.set("status", params.status);
    const raw = await this.fetch<unknown>(`/api/autopilots?${search}`);
    return parseWithFallback(
      raw,
      ListAutopilotsResponseSchema,
      EMPTY_LIST_AUTOPILOTS_RESPONSE as ListAutopilotsResponse,
      { endpoint: "GET /api/autopilots" },
    );
  }

  async getAutopilot(id: string): Promise<GetAutopilotResponse> {
    return this.fetch(`/api/autopilots/${id}`);
  }

  async createAutopilot(data: CreateAutopilotRequest): Promise<Autopilot> {
    return this.fetch("/api/autopilots", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateAutopilot(id: string, data: UpdateAutopilotRequest): Promise<Autopilot> {
    return this.fetch(`/api/autopilots/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  }

  async deleteAutopilot(id: string): Promise<void> {
    await this.fetch(`/api/autopilots/${id}`, { method: "DELETE" });
  }

  async triggerAutopilot(id: string): Promise<AutopilotRun> {
    return this.fetch(`/api/autopilots/${id}/trigger`, { method: "POST" });
  }

  async listAutopilotRuns(id: string, params?: { limit?: number; offset?: number }): Promise<ListAutopilotRunsResponse> {
    const search = new URLSearchParams();
    if (params?.limit) search.set("limit", params.limit.toString());
    if (params?.offset) search.set("offset", params.offset.toString());
    return this.fetch(`/api/autopilots/${id}/runs?${search}`);
  }

  // Returns a single run including its full trigger_payload. List responses
  // omit trigger_payload to keep them small (a webhook envelope can be
  // up to 256 KiB × limit rows), so the detail view fetches via this route.
  async getAutopilotRun(autopilotId: string, runId: string): Promise<AutopilotRun> {
    return this.fetch(`/api/autopilots/${autopilotId}/runs/${runId}`);
  }

  async createAutopilotTrigger(autopilotId: string, data: CreateAutopilotTriggerRequest): Promise<AutopilotTrigger> {
    return this.fetch(`/api/autopilots/${autopilotId}/triggers`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  async updateAutopilotTrigger(autopilotId: string, triggerId: string, data: UpdateAutopilotTriggerRequest): Promise<AutopilotTrigger> {
    return this.fetch(`/api/autopilots/${autopilotId}/triggers/${triggerId}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  }

  async deleteAutopilotTrigger(autopilotId: string, triggerId: string): Promise<void> {
    await this.fetch(`/api/autopilots/${autopilotId}/triggers/${triggerId}`, { method: "DELETE" });
  }

  async rotateAutopilotTriggerWebhookToken(
    autopilotId: string,
    triggerId: string,
  ): Promise<AutopilotTrigger> {
    return this.fetch(
      `/api/autopilots/${autopilotId}/triggers/${triggerId}/rotate-webhook-token`,
      { method: "POST" },
    );
  }

  // Webhook deliveries — list is slim (no raw_body / selected_headers /
  // response_body); detail returns the full row. Both responses are parsed
  // through a lenient schema so an unknown server-side `status` /
  // `signature_status` value degrades to a generic row instead of dropping
  // the whole list.
  async listAutopilotDeliveries(
    autopilotId: string,
    params?: { limit?: number; offset?: number },
  ): Promise<ListWebhookDeliveriesResponse> {
    const search = new URLSearchParams();
    if (params?.limit) search.set("limit", params.limit.toString());
    if (params?.offset) search.set("offset", params.offset.toString());
    const raw = await this.fetch<unknown>(
      `/api/autopilots/${autopilotId}/deliveries?${search}`,
    );
    return parseWithFallback(
      raw,
      ListWebhookDeliveriesResponseSchema,
      EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE,
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
      { ...EMPTY_WEBHOOK_DELIVERY, id: deliveryId, autopilot_id: autopilotId },
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
  ): Promise<WebhookDelivery> {
    const raw = await this.fetch<unknown>(
      `/api/autopilots/${autopilotId}/deliveries/${deliveryId}/replay`,
      { method: "POST" },
    );
    return parseOrThrow(
      raw,
      WebhookDeliveryResponseSchema,
      { ...EMPTY_WEBHOOK_DELIVERY, autopilot_id: autopilotId },
      { endpoint: "POST /api/autopilots/:id/deliveries/:deliveryId/replay" },
    );
  }

  // GitHub integration
  async getGitHubConnectURL(workspaceId: string): Promise<GitHubConnectResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/github/connect`);
  }

  async listGitHubInstallations(workspaceId: string): Promise<ListGitHubInstallationsResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/github/installations`);
  }

  async deleteGitHubInstallation(workspaceId: string, installationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/github/installations/${installationId}`, {
      method: "DELETE",
    });
  }

  async listIssuePullRequests(issueId: string): Promise<{ pull_requests: GitHubPullRequest[] }> {
    return this.fetch(`/api/issues/${issueId}/pull-requests`);
  }

  // Lark integration
  async listLarkInstallations(workspaceId: string): Promise<ListLarkInstallationsResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/lark/installations`);
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
    // cloud up front. Empty / omitted region still resolves to Feishu
    // server-side (RegionOrDefault) — we surface region as a required
    // arg here so every call site is forced to make a deliberate
    // choice rather than silently defaulting to mainland.
    const search = new URLSearchParams({ agent_id: agentId, region });
    return this.fetch(`/api/workspaces/${workspaceId}/lark/install/begin?${search.toString()}`, {
      method: "POST",
    });
  }

  async getLarkInstallStatus(workspaceId: string, sessionId: string): Promise<LarkInstallStatusResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/lark/install/${sessionId}/status`);
  }

  async deleteLarkInstallation(workspaceId: string, installationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/lark/installations/${installationId}`, {
      method: "DELETE",
    });
  }

  async redeemLarkBindingToken(token: string): Promise<RedeemLarkBindingTokenResponse> {
    return this.fetch(`/api/lark/binding/redeem`, {
      method: "POST",
      body: JSON.stringify({ token }),
    });
  }
}
