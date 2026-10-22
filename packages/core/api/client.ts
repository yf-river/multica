import { supervisionSnapshotSchema, supervisionListSchema, supervisionDetailSchema, supervisionFeedbackSchema, supervisionEvidencePageSchema, supervisionInstructionsSchema, type SupervisionEvidencePage, type SupervisionSnapshot, type SupervisionList, type SupervisionDetail, type SupervisionFeedback, type SupervisionInstructions } from "../supervision/schema";
import { TaskOutputPageSchema, TaskMessagePayloadSchema, type TaskOutputPage } from "./schemas";
import { MealsSchema, mealWire, type MealsData, type MealCommand } from "../meals/schema";
import { CookingListSchema, CookingDetailSchema, CookingCategoriesSchema, type CookingList, type CookingDetail, type CookingCategory } from "../cooking/schema";
import { HealthOverviewSchema, HealthReportsSchema, HealthRevisionsSchema, HealthReportSchema, HealthReceiptSchema, EMPTY_HEALTH, observationWire, type HealthOverview, type HealthReport, type HealthProfile, type HealthObservation, type HealthRevision } from "../health/schema";
import { HealthReportHistorySchema, type HealthExtraction } from "../health/schema";
import { environmentCookieName } from "../platform/environment";
// 本文件是核心 HTTP API 客户端：封装一个针对后端的 fetch 层。
// 统一处理鉴权头（token / workspace / CSRF / 客户端身份）、请求 ID、401 未授权回调、
// 结构化错误（ApiError）解析与日志；业务方法拿到响应后大多用 zod schema 校验，
// 校验失败时降级为空值/兜底数据而不是直接抛错，让 UI 不至于白屏。
import { configStore } from "../config";
import type {
  Issue,
  IssuePriority,
  CreateIssueRequest,
  MoveIssueRequest,
  UpdateIssueRequest,
  GroupedIssuesResponse,
  ListIssuesResponse,
  SearchIssuesResponse,
  SearchProjectsResponse,
  UpdateMeRequest,
  CreateMemberRequest,
  UpdateMemberRequest,
  ListIssuesParams,
  ListGroupedIssuesParams,
  IssueTableFacetsRequest,
  IssueTableFacetsResponse,
  IssueTableGroupsRequest,
  IssueTableGroupsResponse,
  IssueTableRowsRequest,
  IssueTableRowsResponse,
  Agent,
  MikaBootstrapResponse,
  CreateAgentRequest,
  AgentBuilderRuntimeSwitch,
  AgentBuilderSession,
  AgentBuilderSessionSummary,
  StoredAgentDraft,
  UpdateAgentRequest,
  AgentEnvResponse,
  UpdateAgentEnvRequest,
  AgentTask,
  AgentActivityBucket,
  AgentRunCount,
  WorkspaceWorkingAgent,
  WorkspaceWorkingAgentMineRelation,
  WorkspaceWorkingAgentType,
  AgentRuntime,
  RuntimeProfile,
  CreateRuntimeProfileRequest,
  UpdateRuntimeProfileRequest,
  InboxItem,
  InboxWorkspaceUnread,
  IssueSubscriber,
  Comment,
  CommentTriggerPreview,
  IssueTriggerPreview,
  IssueTriggerPreviewParams,
  Reaction,
  IssueReaction,
  Workspace,
  WorkspaceRepo,
  WorkspaceRepoProbeResponse,
  WorkspaceMcpServer,
  MemberWithUser,
  User,
  Skill,
  SkillSummary,
  CreateSkillRequest,
  UpdateSkillRequest,
  SetAgentSkillsRequest,
  SetAgentRuntimeSkillEnabledRequest,
  PersonalAccessToken,
  CreatePersonalAccessTokenRequest,
  CreatePersonalAccessTokenResponse,
  RuntimeUsage,
  IssueUsageSummary,
  RuntimeHourlyActivity,
  RuntimeUsageByAgent,
  RuntimeUsageByHour,
  DashboardUsageDaily,
  DashboardUsageByAgent,
  DashboardAgentRunTime,
  DashboardRunTimeDaily,
  DashboardFailureDaily,
  DashboardFailureByAgent,
  RuntimeUpdate,
  RuntimeModelListRequest,
  RuntimeLocalSkillListRequest,
  CreateRuntimeLocalSkillImportRequest,
  RuntimeLocalSkillImportRequest,
  TimelineEntry,
  AssigneeFrequencyEntry,
  TaskMessagePayload,
  Attachment,
  ChatSession,
  ChatPinnedAgent,
  ChatMessage,
  ChatMessagesPage,
  ChatDraftRestoresResponse,
  ChatPendingTask,
  PrioritizeQueuedChatTaskResponse,
  PendingChatTasksResponse,
  HasPendingChatTasksResponse,
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
  IssueProperty,
  IssuePropertyValue,
  CreatePropertyRequest,
  QuickAction,
  CreateQuickActionRequest,
  UpdateQuickActionRequest,
  ListQuickActionsResponse,
  UpdatePropertyRequest,
  ListPropertiesResponse,
  IssuePropertiesResponse,
  CreateLabelRequest,
  UpdateLabelRequest,
  ListLabelsResponse,
  ListIssueStatusesResponse,
  IssueStatusCategory,
  IssueStatusEntry,
  CreateIssueStatusRequest,
  UpdateIssueStatusRequest,
  IssueLabelsResponse,
  LabelResourceType,
  ResourceLabelsResponse,
  PinnedItem,
  CreatePinRequest,
  PinnedItemType,
  ReorderPinsRequest,
  Invitation,
  ShareLink,
  ShareLinkInfo,
  Autopilot,
  AutopilotTrigger,
  AutopilotRun,
  CreateAutopilotRequest,
  UpdateAutopilotRequest,
  CreateAutopilotTriggerRequest,
  UpdateAutopilotTriggerRequest,
  ListAutopilotsResponse,
  CronPreviewResponse,
  GetAutopilotResponse,
  AutopilotCollaboratorsResponse,
  ListAutopilotRunsResponse,
  ListWebhookDeliveriesResponse,
  WebhookDelivery,
  NotificationPreferenceResponse,
  NotificationPreferences,
  PluginHookResult,
  PluginInstallation,
  PluginInstallationListResponse,
  PluginPackage,
  PluginPackageListResponse,
  PluginSurfaceLaunch,
  PluginInvocation,
  PluginMCPTool,
  PluginPreview,
  PluginTokenIssue,
  PluginPreviewRequest,
  PluginInstallRequest,
  PluginConfigRequest,
  GitHubPullRequest,
  ListGitHubInstallationsResponse,
  ListGitHubRepositoriesResponse,
  GitHubConnectResponse,
  ListVCSConnectionsResponse,
  ConnectVCSRequest,
  ConnectVCSResponse,
  ListLarkInstallationsResponse,
  BeginLarkInstallResponse,
  LarkInstallStatusResponse,
  RedeemLarkBindingTokenResponse,
  ComposioToolkit,
  ComposioConnection,
  ComposioConnectInitResponse,
  SlackInstallation,
  ListSlackInstallationsResponse,
  RegisterSlackBYORequest,
  RedeemSlackBindingTokenResponse,
  DingTalkInstallation,
  ListDingTalkInstallationsResponse,
  ListDingTalkGroupsResponse,
  ListDingTalkGroupsParams,
  RegisterDingTalkBYORequest,
  RedeemDingTalkBindingTokenResponse,
  WecomInstallation,
  ListWecomInstallationsResponse,
  RegisterWecomBYORequest,
  RedeemWecomBindingTokenResponse,
  TelegramInstallation,
  ListTelegramInstallationsResponse,
  RegisterTelegramRequest,
  RedeemTelegramBindingTokenResponse,
  Squad,
  SquadMember,
  SquadMemberStatusListResponse,
  SourceContextPreview,
  CreateCommentSubIssueManualRequest,
  CreateCommentSubIssueAgentRequest,
  CreateCommentSubIssueRequest,
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
  LifeIdentityListResponse,
  LifeRelationshipListResponse,
  LifeMaterialListResponse,
  LifeInternalThoughtListResponse,
  LifeTopicListResponse,
  LifeCommitmentListResponse,
  LifeProactivePolicy,
  LifeObserverListResponse,
  LifeObservationSeatResponse,
  LifeModuleListResponse,
  LifeCognitionJobListResponse,
  LifeUpgradeEvaluationListResponse,
} from "../types";
import type { OnboardingCompletionPath } from "../onboarding/types";
import type {
  CloudRuntimeNode,
  CreateCloudRuntimeNodeRequest,
  ListCloudRuntimeNodesParams,
} from "../runtimes/cloud-runtime";
import { type Logger, noopLogger } from "../logger";
import { createRequestId, createSafeId } from "../utils";
import { getCurrentSlug } from "../platform/workspace-storage";
import { parseOrThrow, parseWithFallback } from "./schema";
import type { CreateExternalCredentialProfileRequest, ExternalCredentialProfile, ExternalCredentialProvider, TestExternalCredentialProfileRequest, TestExternalCredentialProfileResponse, UpdateExternalCredentialProfileRequest } from "../types/external-credential";
import type { IssueExecutionTreeResponse } from "../types/issue-execution";
import {
  AgentTaskListSchema,
  EMPTY_ISSUE_EXECUTION_TREE_RESPONSE,
  IssueExecutionTreeResponseSchema,
  IssueTraceEventsResponseSchema,
  ExternalCredentialProfileSchema,
  ExternalCredentialProfileListSchema,
  TestExternalCredentialProfileResponseSchema,
  AttachmentResponseSchema,
  CancelTaskResponseSchema,
  ChatDraftRestoresResponseSchema,
  ChatMessageListSchema,
  ChatMessagesPageSchema,
  ChatPendingTaskSchema,
  ChatSessionListSchema,
  ChatSessionSchema,
  PrioritizeQueuedChatTaskResponseSchema,
  SendChatMessageResponseSchema,
  ChildIssuesResponseSchema,
  ChildIssueProgressResponseSchema,
  CommentsListSchema,
  CommentTriggerPreviewSchema,
  IssueTriggerPreviewSchema,
  CloudRuntimeNodeListSchema,
  CloudRuntimeNodeSchema,
  AgentBuilderRuntimeSwitchSchema,
  AgentBuilderSessionSchema,
  AgentBuilderSessionListSchema,
  EMPTY_AGENT_BUILDER_SESSION_LIST,
  agentBuilderRuntimeSwitchFallback,
  DashboardAgentRunTimeListSchema,
  DashboardRunTimeDailyListSchema,
  DashboardFailureDailyListSchema,
  DashboardFailureByAgentListSchema,
  DashboardUsageByAgentListSchema,
  DashboardUsageDailyListSchema,
  EMPTY_APP_CONFIG,
  EMPTY_ATTACHMENT,
  EMPTY_CHAT_MESSAGE_LIST,
  EMPTY_CHAT_PENDING_TASK,
  EMPTY_CHAT_SESSION,
  EMPTY_CHAT_SESSION_LIST,
  EMPTY_PRIORITIZE_QUEUED_CHAT_TASK_RESPONSE,
  EMPTY_CLOUD_RUNTIME_NODE,
  EMPTY_CLOUD_RUNTIME_NODE_LIST,
  EMPTY_AGENT_BUILDER_SESSION,
  EMPTY_GROUPED_ISSUES_RESPONSE,
  EMPTY_ISSUE_TABLE_FACETS_RESPONSE,
  EMPTY_ISSUE_TABLE_GROUPS_RESPONSE,
  EMPTY_ISSUE_TABLE_ROWS_RESPONSE,
  EMPTY_LIST_ISSUES_RESPONSE,
  EMPTY_SEARCH_ISSUES_RESPONSE,
  EMPTY_SEARCH_PROJECTS_RESPONSE,
  EMPTY_SQUAD,
  EMPTY_SQUAD_LIST,
  EMPTY_SQUAD_MEMBER_STATUS_LIST,
  EMPTY_TIMELINE_ENTRIES,
  EMPTY_USER,
  EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE,
  EMPTY_WEBHOOK_DELIVERY,
  AppConfigSchema,
  type AppConfigResponse,
  GroupedIssuesResponseSchema,
  IssueTableFacetsResponseSchema,
  IssueTableGroupsResponseSchema,
  IssueTableRowsResponseSchema,
  ListAutopilotsResponseSchema,
  EMPTY_LIST_AUTOPILOTS_RESPONSE,
  AutopilotRunSchema,
  FALLBACK_AUTOPILOT_RUN,
  CronPreviewResponseSchema,
  UNREADABLE_CRON_PREVIEW_RESPONSE,
  ListIssuesResponseSchema,
  CreateIssueResponseSchema,
  IssueSchema,
  AgentTaskSchema,
  SourceContextPreviewSchema,
  CommentSubIssueTaskResponseSchema,
  ListWebhookDeliveriesResponseSchema,
  RuntimeHourlyActivityListSchema,
  RuntimeUsageByAgentListSchema,
  RuntimeUsageByHourListSchema,
  RuntimeUsageListSchema,
  SearchIssuesResponseSchema,
  SearchProjectsResponseSchema,
  SquadSchema,
  SquadListSchema,
  SquadMemberStatusListResponseSchema,
  SubscribersListSchema,
  TimelineEntriesSchema,
  UserSchema,
  WebhookDeliveryResponseSchema,
  DingTalkInstallationSchema,
  ListDingTalkInstallationsResponseSchema,
  ListDingTalkGroupsResponseSchema,
  RedeemDingTalkBindingTokenResponseSchema,
  EMPTY_DINGTALK_INSTALLATION,
  EMPTY_LIST_DINGTALK_INSTALLATIONS_RESPONSE,
  EMPTY_LIST_DINGTALK_GROUPS_RESPONSE,
  EMPTY_REDEEM_DINGTALK_BINDING_TOKEN_RESPONSE,
  WecomInstallationSchema,
  ListWecomInstallationsResponseSchema,
  RedeemWecomBindingTokenResponseSchema,
  EMPTY_WECOM_INSTALLATION,
  EMPTY_LIST_WECOM_INSTALLATIONS_RESPONSE,
  EMPTY_REDEEM_WECOM_BINDING_TOKEN_RESPONSE,
  TelegramInstallationSchema,
  ListTelegramInstallationsResponseSchema,
  RedeemTelegramBindingTokenResponseSchema,
  EMPTY_TELEGRAM_INSTALLATION,
  EMPTY_LIST_TELEGRAM_INSTALLATIONS_RESPONSE,
  EMPTY_REDEEM_TELEGRAM_BINDING_TOKEN_RESPONSE,
  EMPTY_CANCEL_TASK_RESPONSE,
  EMPTY_CHAT_DRAFT_RESTORES,
  InboxUnreadSummarySchema,
  EMPTY_INBOX_UNREAD_SUMMARY,
  InboxItemListSchema,
  EMPTY_INBOX_ITEMS,
  NotificationPreferenceResponseSchema,
  EMPTY_NOTIFICATION_PREFERENCE_RESPONSE,
  LabelSchema,
  ListLabelsResponseSchema,
  ListIssueStatusesResponseSchema,
  IssueStatusEntrySchema,
  IssuePropertySchema,
  ListPropertiesResponseSchema,
  IssuePropertiesResponseSchema,
  QuickActionSchema,
  ListQuickActionsResponseSchema,
  QuickActionRenderSchema,
  EMPTY_QUICK_ACTION,
  EMPTY_LIST_QUICK_ACTIONS_RESPONSE,
  CommentSchema,
  EMPTY_COMMENT,
  EMPTY_ISSUE_PROPERTY,
  EMPTY_LIST_PROPERTIES_RESPONSE,
  EMPTY_ISSUE_PROPERTIES_RESPONSE,
  EMPTY_ISSUE_PULL_REQUESTS_RESPONSE,
  IssuePullRequestsResponseSchema,
  ResourceLabelsResponseSchema,
  EMPTY_LABEL,
  EMPTY_LIST_LABELS_RESPONSE,
  EMPTY_LIST_ISSUE_STATUSES_RESPONSE,
  EMPTY_ISSUE_STATUS_ENTRY,
  EMPTY_RESOURCE_LABELS_RESPONSE,
  GitHubConnectResponseSchema,
  ListGitHubInstallationsResponseSchema,
  ListGitHubRepositoriesResponseSchema,
  EMPTY_GITHUB_CONNECT_RESPONSE,
  EMPTY_LIST_GITHUB_INSTALLATIONS_RESPONSE,
  EMPTY_LIST_GITHUB_REPOSITORIES_RESPONSE,
  RuntimeModelListRequestSchema,
  MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
  SkillSchema,
  SkillFileSchema,
  EMPTY_SKILL,
  SkillImportResultSchema,
  EMPTY_SKILL_IMPORT_RESULT,
  IssueViewSchema,
  IssueViewListSchema,
  IssueViewPreferenceSchema,
  EMPTY_ISSUE_VIEW_PREFERENCE,
  EMPTY_PLUGIN_INSTALLATION,
  EMPTY_WORKSPACE_MCP_SERVER,
  EMPTY_PLUGIN_INSTALLATION_LIST,
  EMPTY_PLUGIN_PREVIEW,
  EMPTY_PLUGIN_PACKAGE,
  EMPTY_PLUGIN_PACKAGE_LIST,
  EMPTY_PLUGIN_SURFACE_LAUNCH,
  PluginHookResultSchema,
  PluginInstallationListResponseSchema,
  PluginPackageListResponseSchema,
  PluginPackageSchema,
  PluginSurfaceLaunchSchema,
  PluginInvocationListSchema,
  PluginMCPToolListSchema,
  PluginTokenIssueSchema,
  PluginInstallationSchema,
  PluginPreviewSchema,
  WorkspaceMcpServerListSchema,
  WorkspaceMcpServerSchema,
  WorkspaceMcpChecksSchema,
  WorkspaceRepoSchema,
  RepositoryLocationSchema,
  WorkspaceRepositoryCenterSchema,
  WorkspaceRepoProbeResponseSchema,
  ShareLinkSchema,
  ShareLinkListResponseSchema,
  ShareLinkInfoSchema,
  JoinShareLinkResponseSchema,
  EMPTY_SHARE_LINK,
  EMPTY_SHARE_LINK_INFO,
  EMPTY_JOIN_SHARE_LINK_RESPONSE,
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
  LifeIdentityListSchema,
  LifeRelationshipListSchema,
  LifeMaterialListSchema,
  LifeInternalThoughtListSchema,
  LifeTopicListSchema,
  LifeCommitmentListSchema,
  LifeProactivePolicySchema,
  LifeObserverListSchema,
  LifeObservationSeatSchema,
  LifeModuleListSchema,
  LifeCognitionJobListSchema,
  LifeUpgradeEvaluationListSchema,
  EMPTY_LIFE_IDENTITIES,
  EMPTY_LIFE_RELATIONSHIPS,
  EMPTY_LIFE_MATERIALS,
  EMPTY_LIFE_INTERNAL_THOUGHTS,
  EMPTY_LIFE_TOPICS,
  EMPTY_LIFE_COMMITMENTS,
  EMPTY_LIFE_OBSERVERS,
  EMPTY_LIFE_OBSERVATION_SEAT,
  EMPTY_LIFE_MODULES,
  EMPTY_LIFE_JOBS,
  EMPTY_LIFE_UPGRADES,
  type IssueView,
  type IssueViewPreference,
  type CreateIssueViewRequest,
} from "./schemas";

/** 向服务器标识调用方客户端的身份信息。
 *  随每个 HTTP 请求以 X-Client-Platform / X-Client-Version /
 *  X-Client-OS 头发送，供后端按客户端做日志记录、门控或指标拆分。
 *  对应接收端见 server/internal/middleware/client.go。 */
export interface ApiClientIdentity {
  /** 客户端类型逻辑名。服务器期望取值："web" | "desktop" | "cli" | "daemon"。 */
  platform?: string;
  /** 客户端/应用版本号（如 "0.1.0"、git tag、commit）。 */
  version?: string;
  /** 粗略的操作系统分类（如 "macos"、"windows" 或 "linux"）。 */
  os?: string;
}

/** 创建 ApiClient 的可选配置。 */
export interface ApiClientOptions {
  environmentId?: string;
  logger?: Logger;
  onUnauthorized?: () => void;
  /** 标识客户端身份，会作为 X-Client-* 请求头发送。 */
  identity?: ApiClientIdentity;
}

/** 客户端运行环境快照：记录调用者机器上探测到的 runtime 汇总信息。 */
export interface ClientRuntimeSnapshot {
  probe_result: "success" | "error";
  runtime_count?: number;
  provider_summary?: Record<string, number>;
  online_count?: number;
  offline_count?: number;
}

/** 客户端使用量上报请求体：安装 ID + 可选的运行环境快照。 */
export interface ClientUsageRequest {
  install_id: string;
  runtime?: ClientRuntimeSnapshot;
}

/** 登录成功后的响应：token 与当前用户信息。 */
export interface LoginResponse {
  token: string;
  user: User;
}

/**
 * 后端返回非 2xx 时抛出的结构化错误。
 * status / statusText 保留 HTTP 状态，body 保留已解析的 JSON 响应体
 * （如果服务器返回了），其中通常带 `code` 等字段，方便调用方用机器可读
 * 的错误码分支处理，而不是去匹配给人看的消息文案。
 */
export class ApiError extends Error {
  readonly status: number;
  readonly statusText: string;
  // 原始解码的 JSON 响应体（当服务器返回了 JSON 时）。它承载结构化的
  // 错误字段如 `code`，调用方可以按机器可读标识符分支，而不用匹配人读消息。
  readonly body?: unknown;

  constructor(message: string, status: number, statusText: string, body?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.statusText = statusText;
    this.body = body;
  }
}

function assertAgentConversationStartersWriteSupported(data: {
  conversation_starters?: unknown;
}): void {
  if (
    Object.prototype.hasOwnProperty.call(data, "conversation_starters") &&
    !configStore.getState().agentConversationStartersSupported
  ) {
    throw new Error(
      "This server version does not support agent conversation starters. Update the server before saving them.",
    );
  }
}

// errorCode 从错误的响应体里提取处理器挂在失败上的稳定 `code`
// （服务端 writeErrorCode 写入），调用方据此渲染自己本地化的提示句，
// 而不是直接弹服务端的英文文案。对非 ApiError 或没带 code 的旧服务器返回
// undefined，调用方会退回到 err.message——那是目前尚未采用此机制的端点
// 产生的错误信息。
export function errorCode(err: unknown): string | undefined {
  if (err instanceof ApiError && err.body && typeof err.body === "object") {
    const code = (err.body as { code?: unknown }).code;
    if (typeof code === "string" && code.length > 0) return code;
  }
  return undefined;
}

// dispatchReasonCode 从「触发器被拦截」错误的结构化响应体里提取稳定的、
// 机器可读的准入拒绝原因（MUL-4525）。UI 调用方据此本地化被拦截/部分拦截的
// 触发器提示，而不是去匹配给人读的消息文案。对非 ApiError 或响应体里没有
// reason_code（旧服务器）的情况返回 undefined，调用方退回到通用失败提示。
export function dispatchReasonCode(err: unknown): string | undefined {
  if (err instanceof ApiError && err.body && typeof err.body === "object") {
    const code = (err.body as { reason_code?: unknown }).reason_code;
    if (typeof code === "string" && code.length > 0) return code;
  }
  return undefined;
}

// clientErrorMessage 只在错误是「客户端错误（4xx）」时返回服务器消息。
// 处理器为面向用户写的这类文案——「autopilot is not active」、「Idempotency-Key
// is too long」——值得展示给用户。5xx 消息属于内部细节（Go error chain、
// pgx 表/约束名、内部 id），绝不能进 toast（MUL-6472）；非 ApiError 是传输层
// 失败，其消息（如 "Failed to fetch"）对用户没有可操作信息。两种情况都返回
// undefined，调用方回退到自己的本地化文案。
export function clientErrorMessage(err: unknown): string | undefined {
  if (err instanceof ApiError && err.status >= 400 && err.status < 500) {
    return err.message || undefined;
  }
  return undefined;
}

// 当服务器因文件超过 2 MB 上限拒绝内联展示时，由 getAttachmentTextContent 抛出。
// UI 将其映射为「文件太大请下载」的提示，仍保留下载按钮可用。
export class PreviewTooLargeError extends Error {
  constructor() {
    super("attachment too large for inline preview");
    this.name = "PreviewTooLargeError";
  }
}

// 当服务器文本白名单拒绝了该内容类型时，由 getAttachmentTextContent 抛出。
// 通常客户端的 isPreviewable() 守卫会提前拦截，但两侧白名单可能漂移——
// 把 415 转成带类型的错误，让这种漂移暴露出来。
export class PreviewUnsupportedError extends Error {
  constructor() {
    super("attachment type not supported for inline preview");
    this.name = "PreviewUnsupportedError";
  }
}

function remapSkillImportError(err: unknown): unknown {
  if (!(err instanceof ApiError) || !err.body || typeof err.body !== "object") {
    return err;
  }
  const body = err.body as { reason?: unknown; error?: unknown };
  const reason = typeof body.reason === "string" && body.reason ? body.reason : "";
  const error = typeof body.error === "string" && body.error ? body.error : "";
  const message = reason || error;
  if (!message || message === err.message) return err;
  return new ApiError(message, err.status, err.statusText, err.body);
}

function skillFromImportResult(raw: unknown, endpoint: string): Skill {
  const result = parseWithFallback(
    raw,
    SkillImportResultSchema,
    EMPTY_SKILL_IMPORT_RESULT,
    { endpoint },
  );
  if (
    (result.status === "created" || result.status === "updated") &&
    result.skill
  ) {
    const skill = parseWithFallback(result.skill, SkillSchema, EMPTY_SKILL, {
      endpoint,
    });
    if (skill.id) return skill;
  }
  throw new Error(result.reason || "Import failed");
}

/**
 * 在 X-Client-Capabilities 中宣告的能力名：告知服务器本客户端可以从
 * 持久化的草稿恢复行里救回一条被取消的 prompt（#5219）。
 * 必须与协议常量 protocol.AppCapabilityChatDraftRestoreV1 保持一致。
 */
export const CHAT_DRAFT_RESTORE_CAPABILITY = "chat-draft-restore-v1";

/**
 * 两个取消订阅端点共用的请求体构造：省略 target 即表示「当前调用者」，
 * 服务器从请求发起者（actor）解析。
 */
function subscriberTarget(
  userId?: string,
  userType?: string,
): Record<string, string> {
  const body: Record<string, string> = {};
  if (userId) body.user_id = userId;
  if (userType) body.user_type = userType;
  return body;
}

/**
 * 单次调用覆盖本次请求目标 workspace 的辅助函数。
 *
 * authHeaders() 平时从全局「当前 workspace」单例里打 X-Workspace-Slug，
 * 服务器在读取任何 `workspace_id` 查询参数之前先从该请求头解析 workspace。
 * 任何作用于「用户当前不在其中」的 workspace 的操作——典型如创建 workspace
 * 的流程，它要为尚未跳转过去的 workspace 做初始化——必须显式声明目标，
 * 否则该单例被并发写入时，写入会被静默重定向到错误的 workspace。
 */
function workspaceHeader(
  slug?: string,
): Record<string, string> | undefined {
  return slug ? { "X-Workspace-Slug": slug } : undefined;
}

/** 把钉钉群搜索参数拼成查询字符串；无参数时返回空串。 */
function dingTalkGroupSearch(params: ListDingTalkGroupsParams): string {
  const search = new URLSearchParams();
  if (params.activity) search.set("activity", params.activity);
  if (params.installationId) search.set("installation_id", params.installationId);
  if (params.offset !== undefined) search.set("offset", String(params.offset));
  if (params.limit !== undefined) search.set("limit", String(params.limit));
  const encoded = search.toString();
  return encoded ? `?${encoded}` : "";
}

/**
 * 业务 API 客户端。
 *
 * 职责：基于一个 baseUrl 发起各类 HTTP 请求，自动带上鉴权头与请求 ID，
 * 统一处理 401 跳转、结构化 ApiError、响应日志；多数业务方法会用 zod schema
 * 校验响应并在解析失败时降级为空数据。通常不直接 new，而是通过
 * index.ts 暴露的全局单例使用。
 */
export class ApiClient {
  private baseUrl: string;
  private token: string | null = null;
  private logger: Logger;
  private options: ApiClientOptions;

  /**
   * @param baseUrl 服务端地址（不含路径前缀）
   * @param options 可选：日志、401 未授权回调、客户端身份标识
   */
  constructor(baseUrl: string, options?: ApiClientOptions) {
    this.baseUrl = baseUrl;
    this.options = options ?? {};
    this.logger = options?.logger ?? noopLogger;
  }

  /** 返回构造时传入的服务端地址。 */
  getBaseUrl(): string {
    return this.baseUrl;
  }

  /** 设置/清除登录 token；为 null 表示登出。 */
  setToken(token: string | null) {
    this.token = token;
  }

  // 从浏览器 cookie 中读取 CSRF token（名 multica_csrf）；非浏览器环境返回 null。
  private readCsrfToken(): string | null {
    if (typeof document === "undefined") return null;
    const match = document.cookie
      .split("; ")
      .find((c) => c.startsWith(`${environmentCookieName("multica_csrf", this.options.environmentId)}=`));
    return match ? match.split("=")[1] ?? null : null;
  }

  // 组装标准请求头：鉴权 token、当前 workspace slug、CSRF、客户端身份标识。
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

  // 401 处理：清空本地 token，然后交给调用方配置的 onUnauthorized 回调
  //（通常是跳转登录页）。
  private handleUnauthorized() {
    this.token = null;
    // Workspace id 归 URL 驱动的 workspace-storage 单例管
    //（由 [workspaceSlug]/layout.tsx 写入）。收到 401 后，鉴权流程会跳到
    // /login 从而离开 workspace 路由，下一次进入 workspace 时会重新覆盖
    // 该 id，所以这里无需主动清理。
    this.options.onUnauthorized?.();
  }

  async getSupervision() {
    return parseOrThrow<SupervisionSnapshot>(await this.fetch<unknown>("/api/supervision/"), supervisionSnapshotSchema, { endpoint: "GET /api/supervision", sensitive: true });
  }
  async listSupervision(params: { search: string; filter: string; offset: number; project: string }) {
    const q = new URLSearchParams({ ...params, offset: String(params.offset) });
    return parseOrThrow<SupervisionList>(await this.fetch<unknown>(`/api/supervision/subjects?${q}`), supervisionListSchema, { endpoint: "GET /api/supervision/subjects", sensitive: true });
  }
  async getSupervisionEvidence(kind: string, id: string, offset: number, batch?: string) {
    const query = new URLSearchParams({ offset: String(offset) });
    if (batch) query.set("batch", batch);
    return parseOrThrow<SupervisionEvidencePage>(await this.fetch<unknown>(`/api/supervision/evidence/${encodeURIComponent(kind)}/${encodeURIComponent(id)}?${query}`), supervisionEvidencePageSchema, { endpoint: "GET /api/supervision/evidence/:kind/:id", sensitive: true });
  }
  async getSupervisionSubject(id: string) {
    return parseOrThrow<SupervisionDetail>(await this.fetch<unknown>(`/api/supervision/subjects/${encodeURIComponent(id)}`), supervisionDetailSchema, { endpoint: "GET /api/supervision/subjects/:id", sensitive: true });
  }
  async getSupervisionBatchTask(id: string): Promise<AgentTask> {
    return parseOrThrow<AgentTask>(await this.fetch<unknown>(`/api/supervision/batches/${encodeURIComponent(id)}/task`), AgentTaskSchema, { endpoint: "GET /api/supervision/batches/:id/task", sensitive: true });
  }
  async getSupervisionInstructions() {
    return parseOrThrow<SupervisionInstructions>(await this.fetch<unknown>("/api/supervision/instructions"), supervisionInstructionsSchema, { endpoint: "GET /api/supervision/instructions", sensitive: true });
  }
  async updateSupervision(config: { enabled: boolean; interval_seconds: number; investigation_enabled?: boolean; runtime_id?: string | null; model?: string | null; thinking_level?: string | null; instructions?: string | null }) {
    return parseOrThrow<SupervisionSnapshot>(await this.fetch<unknown>("/api/supervision/", { method: "PUT", body: JSON.stringify(config) }), supervisionSnapshotSchema, { endpoint: "PUT /api/supervision", sensitive: true, mayHaveCommitted: true });
  }
  async checkSupervision(body: { issue_id?: string; full?: boolean; stop?: boolean; retry_failed?: boolean }) {
    return this.fetch<{ scheduled?: boolean; stopped?: boolean }>("/api/supervision/check", { method: "POST", body: JSON.stringify(body) });
  }
  async sendSupervisionFeedback(id: string, body: { content: string; operation_id: string }) {
    return parseOrThrow<SupervisionFeedback>(await this.fetch<unknown>(`/api/supervision/findings/${encodeURIComponent(id)}/feedback`, { method: "POST", body: JSON.stringify(body) }), supervisionFeedbackSchema, { endpoint: "POST /api/supervision/findings/:id/feedback", sensitive: true, mayHaveCommitted: true });
  }
  async updateSupervisionFinding(id: string, body: { status: string; reason: string; operation_id: string; snoozed_until?: string }) {
    return this.fetch<{ saved: boolean }>(`/api/supervision/findings/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(body) });
  }

  private async parseErrorMessage(res: Response, fallback: string): Promise<string> {
    try {
      const data = await res.json() as { error?: string };
      if (typeof data.error === "string" && data.error) return data.error;
    } catch {
      // 忽略非 JSON 的错误响应体。
    }
    return fallback;
  }

  // 一次性读取响应体：既取给人看的人读错误消息，也取结构化字段。
  // Response 流只能消费一次，所以两者必须来自同一次读取。
  private async parseErrorBody(res: Response, fallback: string): Promise<{ message: string; body: unknown }> {
    try {
      const data = await res.json() as { error?: string };
      const message = typeof data.error === "string" && data.error ? data.error : fallback;
      return { message, body: data };
    } catch {
      return { message: fallback, body: undefined };
    }
  }

  // 发送请求并统一走标准请求头（鉴权、CSRF、请求 ID、客户端身份）和共享
  // 错误路径（401 → handleUnauthorized、结构化 ApiError、按状态区分的日志级别）。
  // 返回原始 Response，让调用方自行决定如何解码响应体——JSON 给类型化的
  // fetch<T> 路径，纯文本给附件预览代理等场景。
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
      const logLevel = res.status === 404 ? "warn" : "error";
      this.logger[logLevel](`← ${res.status} ${path}`, { rid, duration: `${Date.now() - start}ms`, error: message });
      throw new ApiError(message, res.status, res.statusText, body);
    }

    this.logger.info(`← ${res.status} ${path}`, { rid, duration: `${Date.now() - start}ms` });
    return res;
  }

  // 类型化 GET/POST 便捷方法：默认带 JSON Content-Type；204 时返回 undefined。
  async meals(): Promise<MealsData> {
    const endpoint = "/api/meals";
    const value = parseWithFallback(await this.fetch<unknown>(endpoint), MealsSchema, null as MealsData | null, { endpoint, sensitive: true });
    if (!value) throw new Error("饮食响应无效，请重试");
    return value;
  }
  async mealAction(command: MealCommand): Promise<MealsData> {
    const endpoint = "/api/meals/actions";
    const value = parseWithFallback(await this.fetch<unknown>(endpoint, { method: "POST", body: JSON.stringify(mealWire(command)) }), MealsSchema, null as MealsData | null, { endpoint, sensitive: true });
    if (!value) throw new Error("饮食保存回执无效，请刷新后检查");
    return value;
  }
  async cookingList(section: string, query: string): Promise<CookingList | null> {
    const endpoint = `/api/cooking/${section}?${query}`;
    return parseWithFallback(await this.fetch<unknown>(endpoint), CookingListSchema, null as CookingList | null, { endpoint });
  }
  async cookingDetail(kind: string, id: string): Promise<CookingDetail | null> {
    const endpoint = `/api/cooking/${kind}/${encodeURIComponent(id)}`;
    return parseWithFallback(await this.fetch<unknown>(endpoint), CookingDetailSchema, null as CookingDetail | null, { endpoint });
  }
  async cookingCategories(): Promise<CookingCategory[]> {
    const endpoint = "/api/cooking/categories";
    return parseWithFallback(await this.fetch<unknown>(endpoint), CookingCategoriesSchema, [] as CookingCategory[], { endpoint });
  }
  async cookingFavorite(id: string, favorite: boolean): Promise<void> {
    await this.fetch<unknown>(`/api/cooking/favorites/${encodeURIComponent(id)}`, { method: favorite ? "PUT" : "DELETE" });
  }

  private async fetch<T>(path: string, init?: RequestInit): Promise<T> {
    const res = await this.fetchRaw(path, {
      ...init,
      extraHeaders: { "Content-Type": "application/json" },
    });
    // 处理 204 No Content
    if (res.status === 204) {
      return undefined as T;
    }
    return res.json() as Promise<T>;
  }

  // Auth（账号鉴权区）
  /** 账号密码登录，成功后返回 token 与用户信息。 */
  async login(account: string, password: string): Promise<LoginResponse> {
    return this.fetch("/auth/login", { method: "POST", body: JSON.stringify({ account, password }) });
  }

  /** Google OAuth 登录：用授权码换 token 与用户信息。 */
  async googleLogin(code: string, redirectUri: string): Promise<LoginResponse> {
    return this.fetch("/auth/google", {
      method: "POST",
      body: JSON.stringify({ code, redirect_uri: redirectUri }),
    });
  }

  /** 退出登录。 */
  async logout(): Promise<void> {
    await this.fetch("/auth/logout", { method: "POST" });
  }

  /** 为 CLI 签发一个临时访问 token。 */
  async issueCliToken(): Promise<{ token: string }> {
    return this.fetch("/api/cli-token", { method: "POST" });
  }

  /** 获取当前登录用户信息（schema 校验 + EMPTY_USER 兜底）。 */
  async getMe(): Promise<User> {
    const raw = await this.fetch<unknown>("/api/me");
    return parseWithFallback(raw, UserSchema, EMPTY_USER, {
      endpoint: "GET /api/me",
    });
  }

  /** 标记 onboarding（新手引导）完成；可带上完成路径与所在 workspace。 */
  async markOnboardingComplete(payload?: {
    completion_path?: OnboardingCompletionPath;
    workspace_id?: string;
  }): Promise<User> {
    const raw = await this.fetch<unknown>("/api/me/onboarding/complete", {
      method: "POST",
      body: payload ? JSON.stringify(payload) : undefined,
    });
    return parseWithFallback(raw, UserSchema, EMPTY_USER, {
      endpoint: "POST /api/me/onboarding/complete",
    });
  }

  /** 加入云版 waitlist（等待名单）。 */
  async joinCloudWaitlist(payload: {
    email: string;
    reason?: string;
  }): Promise<User> {
    const raw = await this.fetch<unknown>("/api/me/onboarding/cloud-waitlist", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    return parseWithFallback(raw, UserSchema, EMPTY_USER, {
      endpoint: "POST /api/me/onboarding/cloud-waitlist",
    });
  }

  /** 局部更新 onboarding 问卷等资料。 */
  async patchOnboarding(payload: {
    questionnaire?: Record<string, unknown>;
  }): Promise<User> {
    const raw = await this.fetch<unknown>("/api/me/onboarding", {
      method: "PATCH",
      body: JSON.stringify(payload),
    });
    return parseWithFallback(raw, UserSchema, EMPTY_USER, {
      endpoint: "PATCH /api/me/onboarding",
    });
  }

  /** 更新当前用户资料。 */
  async updateMe(data: UpdateMeRequest): Promise<User> {
    const raw = await this.fetch<unknown>("/api/me", {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, UserSchema, EMPTY_USER, {
      endpoint: "PATCH /api/me",
    });
  }

  // Issues（议题区）
  /**
   * 查询议题列表，支持筛选、分页、搜索与排序。
   * @param params 筛选条件（状态、优先级、经办人、标签、日期范围等）
   * @returns 分页后的议题列表（schema 校验 + 空列表兜底）
   */
  async listIssues(params?: ListIssuesParams): Promise<ListIssuesResponse> {
    const search = new URLSearchParams();
    if (params?.limit) search.set("limit", String(params.limit));
    if (params?.offset) search.set("offset", String(params.offset));
    if (params?.workspace_id) search.set("workspace_id", params.workspace_id);
    if (params?.q?.trim()) search.set("q", params.q.trim());
    if (params?.status) search.set("status", params.status);
    if (params?.statuses?.length) search.set("statuses", params.statuses.join(","));
    if (params?.priority) search.set("priority", params.priority);
    if (params?.priorities?.length) search.set("priorities", params.priorities.join(","));
    if (params?.assignee_id) search.set("assignee_id", params.assignee_id);
    if (params?.assignee_ids?.length) search.set("assignee_ids", params.assignee_ids.join(","));
    if (params?.assignee_types?.length) search.set("assignee_types", params.assignee_types.join(","));
    if (params?.creator_id) search.set("creator_id", params.creator_id);
    if (params?.project_id) search.set("project_id", params.project_id);
    if (params?.assignee_filters?.length) {
      search.set("assignee_filters", params.assignee_filters.map((f) => `${f.type}:${f.id}`).join(","));
    }
    if (params?.include_no_assignee) search.set("include_no_assignee", "true");
    if (params?.creator_filters?.length) {
      search.set("creator_filters", params.creator_filters.map((f) => `${f.type}:${f.id}`).join(","));
    }
    if (params?.project_ids?.length) search.set("project_ids", params.project_ids.join(","));
    if (params?.include_no_project) search.set("include_no_project", "true");
    if (params?.label_ids?.length) search.set("label_ids", params.label_ids.join(","));
    if (params?.top_level_only) search.set("top_level_only", "true");
    // 故意不加 `.length` 守卫：空的 ids 数组也必须发 `ids=`——服务器把「存在但
    // 为空」的列表解释为「空窗口」（什么都没有在跑），而缺少该参数表示不过滤。
    if (params?.ids) search.set("ids", params.ids.join(","));
    if (params?.involves_user_id) search.set("involves_user_id", params.involves_user_id);
    if (params?.metadata && Object.keys(params.metadata).length > 0) {
      search.set("metadata", JSON.stringify(params.metadata));
    }
    if (params?.properties && Object.keys(params.properties).length > 0) {
      search.set("properties", JSON.stringify(params.properties));
    }
    if (params?.open_only) search.set("open_only", "true");
    if (params?.scheduled) search.set("scheduled", "true");
    if (params?.date_field) search.set("date_field", params.date_field);
    if (params?.date_start) search.set("date_start", params.date_start);
    if (params?.date_end) search.set("date_end", params.date_end);
    if (params?.sort_by) search.set("sort", params.sort_by);
    if (params?.sort_direction) search.set("direction", params.sort_direction);
    // ids 维度的筛选项可能携带几百个 UUID（如「agent 正在工作」筛选），足够
    // 撑爆常见反向代理约 8 KB 的请求行上限。这类窗口改走它的 POST 孪生端点，
    // 用 JSON body 携带完全相同的键值对。
    if (params?.ids) {
      const raw = await this.fetch<unknown>("/api/issues/query", {
        method: "POST",
        body: JSON.stringify(Object.fromEntries(search)),
      });
      return parseWithFallback(raw, ListIssuesResponseSchema, EMPTY_LIST_ISSUES_RESPONSE, {
        endpoint: "POST /api/issues/query",
      });
    }
    const path = `/api/issues?${search}`;
    const raw = await this.fetch<unknown>(path);
    return parseWithFallback(raw, ListIssuesResponseSchema, EMPTY_LIST_ISSUES_RESPONSE, {
      endpoint: "GET /api/issues",
    });
  }

  /**
   * 查询按某字段（如状态/经办人）分组统计的议题列表。
   * @param params 分组字段与过滤条件
   * @returns 分组结果（schema 校验 + 空响应兜底）
   */
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
    if (params.properties && Object.keys(params.properties).length > 0) {
      search.set("properties", JSON.stringify(params.properties));
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

  /** 查询议题表格的分组数据（拖表头分组视图）。 */
  async listIssueTableGroups(params: IssueTableGroupsRequest): Promise<IssueTableGroupsResponse> {
    const raw = await this.fetch<unknown>("/api/issues/table/groups", {
      method: "POST",
      body: JSON.stringify(params),
    });
    return parseWithFallback(
      raw,
      IssueTableGroupsResponseSchema,
      EMPTY_ISSUE_TABLE_GROUPS_RESPONSE,
      { endpoint: "POST /api/issues/table/groups" },
    );
  }

  /** 查询议题表格当前页的行数据（带展开/滚动游标等状态）。 */
  async listIssueTableRows(params: IssueTableRowsRequest): Promise<IssueTableRowsResponse> {
    const raw = await this.fetch<unknown>("/api/issues/table/rows", {
      method: "POST",
      body: JSON.stringify(params),
    });
    return parseWithFallback(
      raw,
      IssueTableRowsResponseSchema,
      EMPTY_ISSUE_TABLE_ROWS_RESPONSE,
      { endpoint: "POST /api/issues/table/rows" },
    );
  }

  /** 查询议题表格的筛选项/维度（facet）汇总。 */
  async listIssueTableFacets(params: IssueTableFacetsRequest): Promise<IssueTableFacetsResponse> {
    const raw = await this.fetch<unknown>("/api/issues/table/facets", {
      method: "POST",
      body: JSON.stringify(params),
    });
    return parseWithFallback(
      raw,
      IssueTableFacetsResponseSchema,
      EMPTY_ISSUE_TABLE_FACETS_RESPONSE,
      { endpoint: "POST /api/issues/table/facets" },
    );
  }

  /**
   * 按关键词搜索议题（轻量全局搜索下拉）。
   * @param params.q 搜索词；signal 用于组件卸载时取消在途请求
   */
  async searchIssues(params: { q: string; limit?: number; offset?: number; include_closed?: boolean; signal?: AbortSignal }): Promise<SearchIssuesResponse> {
    const search = new URLSearchParams({ q: params.q });
    if (params.limit !== undefined) search.set("limit", String(params.limit));
    if (params.offset !== undefined) search.set("offset", String(params.offset));
    if (params.include_closed) search.set("include_closed", "true");
    const raw = await this.fetch<unknown>(
      `/api/issues/search?${search}`,
      params.signal ? { signal: params.signal } : undefined,
    );
    return parseWithFallback(raw, SearchIssuesResponseSchema, EMPTY_SEARCH_ISSUES_RESPONSE, {
      endpoint: "GET /api/issues/search",
    });
  }

  /** 按关键词搜索项目（供链接输入/选择器使用）。 */
  async searchProjects(params: { q: string; limit?: number; offset?: number; include_closed?: boolean; signal?: AbortSignal }): Promise<SearchProjectsResponse> {
    const search = new URLSearchParams({ q: params.q });
    if (params.limit !== undefined) search.set("limit", String(params.limit));
    if (params.offset !== undefined) search.set("offset", String(params.offset));
    if (params.include_closed) search.set("include_closed", "true");
    const raw = await this.fetch<unknown>(
      `/api/projects/search?${search}`,
      params.signal ? { signal: params.signal } : undefined,
    );
    return parseWithFallback(raw, SearchProjectsResponseSchema, EMPTY_SEARCH_PROJECTS_RESPONSE, {
      endpoint: "GET /api/projects/search",
    });
  }

  /**
   * 按 UUID **或** 裸标识（如 "MUL-123"）获取单个议题：服务器会按
   * 该 workspace 自己的前缀、通过唯一索引 `(workspace_id, number)` 解析
   * `PREFIX-NUMBER`，前缀错误或编号不存在时返回 404。
   *
   * `signal` 可选，供「取消即卸载」的调用方（标识符自动链接解析）像搜索那样
   * 中止在途查询。
   *
   * 2xx 响应体是校验而非强转。单个议题不是列表：没有可降级的「安全空」形状，
   * 且标识符自动链接的调用方会把结果缓存 5 分钟，字段缺失或类型漂移的 200
   * 绝不能变成一个 id 为 undefined 的「真议题」。与 createIssue 相同，不可用的
   * 响应体会让调用失败——且以普通 Error 失败，绝不伪装成 ApiError 404，
   * 这样 issueIdentifierOptions 会把它当作「不是没有这个议题」往上抛而非缓存。
   */
  async getIssue(id: string, options?: { signal?: AbortSignal }): Promise<Issue> {
    const raw = await this.fetch<unknown>(
      `/api/issues/${encodeURIComponent(id)}`,
      options?.signal ? { signal: options.signal } : undefined,
    );
    const issue = parseWithFallback<Issue | null>(raw, IssueSchema, null, {
      endpoint: "GET /api/issues/:id",
    });
    if (!issue) {
      // parseWithFallback 已记录 zod 报错与原始响应体。
      throw new Error("GET /api/issues/:id returned a malformed issue");
    }
    return issue;
  }

  /**
   * 新建议题。
   * @param data 创建议题请求（标题、优先级、经办人、标签等）
   * @returns 创建成功的议题；响应体不可用时会抛错，让弹窗保留草稿并提示失败
   */
  async createIssue(data: CreateIssueRequest): Promise<Issue> {
    // 经 schema 校验而非裸强转：创建弹窗根据 `labels` 是缺失还是已校验的
    // Label[] 来决定标签附加的兼容回退逻辑，未校验的错误形状不能溜进来。
    // 与列表接口不同，创建返回不可用响应体是「失败的写操作」而非「安全空读」：
    // 回退 null 并抛错，让弹窗保留草稿并展示失败 toast，而不是显示一张指向
    // 空 issue id 的空白「已创建」卡片。parseWithFallback 已记录 schema 报错和
    // 原始响应体；空 message 让弹窗渲染本地化的「创建失败」提示。
    const raw = await this.fetch<unknown>("/api/issues", {
      method: "POST",
      body: JSON.stringify(data),
    });
    const issue = parseWithFallback<Issue | null>(raw, CreateIssueResponseSchema, null, {
      endpoint: "POST /api/issues",
    });
    if (!issue) {
      throw new Error();
    }
    return issue;
  }

  /** 快捷创建议题（用 prompt 走 agent 生成），返回后台任务 id。 */
  async quickCreateIssue(data: {
    agent_id?: string;
    squad_id?: string;
    prompt: string;
    priority?: IssuePriority;
    due_date?: string;
    project_id?: string | null;
    parent_issue_id?: string | null;
    attachment_ids?: string[];
  }): Promise<{ task_id: string }> {
    return this.fetch("/api/issues/quick-create", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 从评论上下文预览「可创建的子议题」信息。 */
  async getCommentSubIssuePreview(anchorCommentId: string): Promise<SourceContextPreview> {
    const raw = await this.fetch<unknown>(`/api/comments/${anchorCommentId}/sub-issue-preview`);
    const preview = parseWithFallback<SourceContextPreview | null>(
      raw,
      SourceContextPreviewSchema,
      null,
      { endpoint: "GET /api/comments/:id/sub-issue-preview" },
    );
    if (!preview) throw new Error("Invalid source context preview response");
    return preview;
  }

  /**
   * 以一条评论为「锚点」，从评论上下文创建其子议题。
   * 手动模式返回创建好的 Issue；agent 模式返回一个后台任务 id。
   * 服务器太旧（无此能力）时会抛一个带 source_context_server_unsupported
   * 错误码的 ApiError，调用方可据此提示升级。
   */
  async createCommentSubIssue(
    anchorCommentId: string,
    data: CreateCommentSubIssueManualRequest,
  ): Promise<Issue>;
  async createCommentSubIssue(
    anchorCommentId: string,
    data: CreateCommentSubIssueAgentRequest,
  ): Promise<{ task_id: string }>;
  async createCommentSubIssue(
    anchorCommentId: string,
    data: CreateCommentSubIssueRequest,
  ): Promise<Issue | { task_id: string }> {
    try {
      const raw = await this.fetch<unknown>(`/api/comments/${anchorCommentId}/sub-issues`, {
        method: "POST",
        body: JSON.stringify(data),
      });
      if (data.mode === "manual") {
        const issue = parseWithFallback<Issue | null>(raw, CreateIssueResponseSchema, null, {
          endpoint: "POST /api/comments/:id/sub-issues (manual)",
        });
        if (!issue) throw new Error("Invalid sub-issue response");
        return issue;
      }
      const task = parseWithFallback<{ task_id: string } | null>(
        raw,
        CommentSubIssueTaskResponseSchema,
        null,
        { endpoint: "POST /api/comments/:id/sub-issues (agent)" },
      );
      if (!task) throw new Error("Invalid quick-create response");
      return task;
    } catch (error) {
      if (error instanceof ApiError && (error.status === 404 || error.status === 405)) {
        throw new ApiError(
          "Source-context sub-issues require a newer server",
          error.status,
          error.statusText,
          { code: "source_context_server_unsupported" },
        );
      }
      throw error;
    }
  }

  /** 上报一次客户端使用情况（安装 id + 运行环境快照）。 */
  async upsertClientUsage(data: ClientUsageRequest): Promise<void> {
    await this.fetch("/api/client-usage", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 整体更新议题字段。 */
  async updateIssue(id: string, data: UpdateIssueRequest, workspaceSlug?: string): Promise<Issue> {
    return this.fetch(`/api/issues/${id}`, {
      headers: workspaceHeader(workspaceSlug),
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  /** 移动议题（改状态/列）。 */
  async moveIssue(id: string, data: MoveIssueRequest, workspaceSlug?: string): Promise<Issue> {
    return this.fetch(`/api/issues/${id}/move`, {
      headers: workspaceHeader(workspaceSlug),
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 获取某议题的直接子议题列表。 */
  async listChildIssues(id: string): Promise<{ issues: Issue[] }> {
    const raw = await this.fetch<unknown>(`/api/issues/${id}/children`);
    return parseWithFallback(raw, ChildIssuesResponseSchema, { issues: [] }, {
      endpoint: "GET /api/issues/:id/children",
    });
  }

  /** 批量版——一次请求返回多个父议题各自的子议题。
   *  避免泳道视图对每个可见父泳道各发一次请求的 N 次扇出。
   *  parentIds 必须非空；请传入已排序去重列表，让 React Query 缓存键稳定。 */
  async listChildrenByParents(parentIds: string[]): Promise<{ issues: Issue[] }> {
    const raw = await this.fetch<unknown>(
      `/api/issues/children?parent_ids=${parentIds.join(",")}`,
    );
    return parseWithFallback(raw, ChildIssuesResponseSchema, { issues: [] }, {
      endpoint: "GET /api/issues/children",
    });
  }

  /** 批量返回各父议题的子议题完成进度（用于泳道顶部进度条）。 */
  async getChildIssueProgress(): Promise<{
    progress: { parent_issue_id: string; total: number; done: number }[];
  }> {
    const raw = await this.fetch<unknown>("/api/issues/child-progress");
    return parseWithFallback(
      raw,
      ChildIssueProgressResponseSchema,
      { progress: [] },
      { endpoint: "GET /api/issues/child-progress" },
    );
  }

  /** 删除单个议题。 */
  async deleteIssue(id: string, workspaceSlug?: string): Promise<void> {
    await this.fetch(`/api/issues/${id}`, { method: "DELETE", headers: workspaceHeader(workspaceSlug) });
  }

  /** 批量更新多个议题（同一组字段变更）。 */
  async batchUpdateIssues(issueIds: string[], updates: UpdateIssueRequest, workspaceSlug?: string): Promise<{ updated: number }> {
    return this.fetch("/api/issues/batch-update", {
      headers: workspaceHeader(workspaceSlug),
      method: "POST",
      body: JSON.stringify({ issue_ids: issueIds, updates }),
    });
  }

  /** 批量删除多个议题。 */
  async batchDeleteIssues(issueIds: string[], workspaceSlug?: string): Promise<{ deleted: number }> {
    return this.fetch("/api/issues/batch-delete", {
      headers: workspaceHeader(workspaceSlug),
      method: "POST",
      body: JSON.stringify({ issue_ids: issueIds }),
    });
  }

  // Comments（评论区）
  /** 获取某议题的全部评论。 */
  async listComments(issueId: string): Promise<Comment[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/comments`);
    return parseWithFallback(raw, CommentsListSchema, [], {
      endpoint: "GET /api/issues/:id/comments",
    });
  }

  /**
   * 在议题下发表评论。
   * @param issueId 议题 id
   * @param content 评论正文
   * @param type 评论类型（默认 "comment"）
   * @param parentId 回复哪条评论（可选）
   * @param attachmentIds 附带附件 id 列表（可选）
   * @param suppressAgentIds 需要抑制触发/回应的 agent id 列表（可选）
   */
  async createComment(
    issueId: string,
    content: string,
    type?: string,
    parentId?: string,
    attachmentIds?: string[],
    suppressAgentIds?: string[],
  ): Promise<Comment> {
    return this.fetch(`/api/issues/${issueId}/comments`, {
      method: "POST",
      body: JSON.stringify({
        content,
        type: type ?? "comment",
        ...(parentId ? { parent_id: parentId } : {}),
        ...(attachmentIds?.length ? { attachment_ids: attachmentIds } : {}),
        ...(suppressAgentIds?.length ? { suppress_agent_ids: suppressAgentIds } : {}),
      }),
    });
  }

  /** 预演：模拟输入一条评论会触发哪些 agent 运行（供 UI 展示「将触发 N 个 agent」）。 */
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

  /** 干跑一次「统一入队判定」：对一次拟进行的议题写入（新建 / 单人指派 /
   *  单状态变更 / 批量）预演会启动哪些 agent 运行，无副作用。四个入口都
   *  调用此接口而不是各自重复实现规则（MUL-3375）。 */
  async previewIssueTrigger(params: IssueTriggerPreviewParams): Promise<IssueTriggerPreview> {
    const raw = await this.fetch<unknown>("/api/issues/preview-trigger", {
      method: "POST",
      body: JSON.stringify({
        ...(params.issueIds?.length ? { issue_ids: params.issueIds } : {}),
        ...(params.isCreate ? { is_create: true } : {}),
        ...(params.assigneeType ? { assignee_type: params.assigneeType } : {}),
        ...(params.assigneeId ? { assignee_id: params.assigneeId } : {}),
        ...(params.status ? { status: params.status } : {}),
      }),
    });
    return parseWithFallback(raw, IssueTriggerPreviewSchema, { triggers: [], total_count: 0 }, {
      endpoint: "POST /api/issues/preview-trigger",
    });
  }

  /** 获取某议题的操作时间线（活动记录）。 */
  async listTimeline(issueId: string): Promise<TimelineEntry[]> {
    const raw = await this.fetch<unknown>(
      `/api/issues/${issueId}/timeline`,
    );
    return parseWithFallback(raw, TimelineEntriesSchema, EMPTY_TIMELINE_ENTRIES, {
      endpoint: "GET /api/issues/:id/timeline",
    });
  }

  /** 获取「分配频率」统计条目（常用于展示高频协作者）。 */
  async getAssigneeFrequency(): Promise<AssigneeFrequencyEntry[]> {
    return this.fetch("/api/assignee-frequency");
  }

  /** 编辑评论内容；可带上附件变更与基于修订号的并发保护。 */
  async updateComment(commentId: string, content: string, attachmentIds?: string[], suppressAgentIds?: string[], contentBase?: string, expectedRevision?: number): Promise<Comment> {
    return this.fetch(`/api/comments/${commentId}`, {
      method: "PUT",
      body: JSON.stringify({
        content,
        attachment_ids: attachmentIds,
        ...(suppressAgentIds?.length ? { suppress_agent_ids: suppressAgentIds } : {}),
        ...(contentBase !== undefined ? { content_base: contentBase } : {}),
        ...(expectedRevision !== undefined ? { expected_revision: expectedRevision } : {}),
      }),
    });
  }

  /** 删除评论。 */
  async deleteComment(commentId: string): Promise<void> {
    await this.fetch(`/api/comments/${commentId}`, { method: "DELETE" });
  }

  /** 把评论标记为已解决。 */
  async resolveComment(commentId: string): Promise<Comment> {
    return this.fetch(`/api/comments/${commentId}/resolve`, { method: "POST" });
  }

  /** 取消评论的「已解决」标记。 */
  async unresolveComment(commentId: string): Promise<Comment> {
    return this.fetch(`/api/comments/${commentId}/resolve`, { method: "DELETE" });
  }

  /** 在评论上添加一个表情回应。 */
  async addReaction(commentId: string, emoji: string): Promise<Reaction> {
    return this.fetch(`/api/comments/${commentId}/reactions`, {
      method: "POST",
      body: JSON.stringify({ emoji }),
    });
  }

  /** 移除评论上的一个表情回应。 */
  async removeReaction(commentId: string, emoji: string): Promise<void> {
    await this.fetch(`/api/comments/${commentId}/reactions`, {
      method: "DELETE",
      body: JSON.stringify({ emoji }),
    });
  }

  /** 在议题上添加一个表情回应。 */
  async addIssueReaction(issueId: string, emoji: string): Promise<IssueReaction> {
    return this.fetch(`/api/issues/${issueId}/reactions`, {
      method: "POST",
      body: JSON.stringify({ emoji }),
    });
  }

  /** 移除议题上的一个表情回应。 */
  async removeIssueReaction(issueId: string, emoji: string): Promise<void> {
    await this.fetch(`/api/issues/${issueId}/reactions`, {
      method: "DELETE",
      body: JSON.stringify({ emoji }),
    });
  }

  // Subscribers（订阅区）
  /** 列出订阅了某议题的用户/agent。 */
  async listIssueSubscribers(issueId: string): Promise<IssueSubscriber[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/subscribers`);
    return parseWithFallback(raw, SubscribersListSchema, [], {
      endpoint: "GET /api/issues/:id/subscribers",
    });
  }

  /** 让当前用户（或指定用户）订阅该议题，接收后续变更通知。 */
  async subscribeToIssue(issueId: string, userId?: string, userType?: string): Promise<void> {
    const body: Record<string, string> = {};
    if (userId) body.user_id = userId;
    if (userType) body.user_type = userType;
    await this.fetch(`/api/issues/${issueId}/subscribe`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  /** 取消订阅某议题。 */
  async unsubscribeFromIssue(
    issueId: string,
    userId?: string,
    userType?: string,
  ): Promise<void> {
    await this.fetch(`/api/issues/${issueId}/unsubscribe`, {
      method: "POST",
      body: JSON.stringify(subscriberTarget(userId, userType)),
    });
  }

  /**
   * 退出该议题及其所有后代议题的订阅，并让这棵议题树未来的新子议题
   * 也不再自动订阅当前用户——这是针对 agent 生成且不断长大的树的逃生门
   *（MUL-5483）。
   *
   * 特意做成独立端点，而不是在 unsubscribeFromIssue 上加一个 `subtree`
   * 标志。Web/desktop 随合并立即发版而后端是手工部署，所以本客户端经常跑在
   * 比服务器新的版本上；早于该特性的旧后端会忽略未知请求体字段，只退订根
   * 节点却仍回 200。独立路径在旧后端上会 404，以「失败变更」的形式暴露
   * 出来，而不是悄悄撒谎成功。
   */
  async unsubscribeFromIssueSubtree(
    issueId: string,
    userId?: string,
    userType?: string,
  ): Promise<void> {
    await this.fetch(`/api/issues/${issueId}/unsubscribe/subtree`, {
      method: "POST",
      body: JSON.stringify(subscriberTarget(userId, userType)),
    });
  }

  // Agents（agent 管理区）
  /** 列出 workspace 的 agent，可选是否包含已归档。 */
  async listAgents(params?: { workspace_id?: string; include_archived?: boolean }): Promise<Agent[]> {
    const search = new URLSearchParams();
    if (params?.workspace_id) search.set("workspace_id", params.workspace_id);
    if (params?.include_archived) search.set("include_archived", "true");
    return this.fetch(`/api/agents?${search}`);
  }

  /** 获取单个 agent 详情。 */
  async getAgent(id: string): Promise<Agent> {
    return this.fetch(`/api/agents/${id}`);
  }

  /** 新建 agent（写入前会检查当前服务器是否支持会话开场白字段）。 */
  async createAgent(data: CreateAgentRequest): Promise<Agent> {
    assertAgentConversationStartersWriteSupported(data);
    return this.fetch("/api/agents", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /**
   * 创建 workspace 内置的「首席幕僚 Mika」agent；已存在时直接返回现有实例。
   *
   * 请求只携带 runtime 选择与会话标题：名称、描述、头像、权限与系统指令层
   * 都是服务器常量，客户端无法自行铸造一个声称具备这些能力的 agent。
   * 服务器还是幂等边界——调用两次得到的是同一个 agent。
   */
  async createMikaAgent(
    data: {
      runtime_id: string;
      /** 空值表示「用 runtime 的默认值」。 */
      model?: string;
      /** 供 onboarding 对话使用的会话标题；仅当本次调用真正创建了会话时才
       *  生效。会话身份是「该成员 + Mika」，与这个本地化标题无关。 */
      session_title?: string;
    },
    workspaceSlug?: string,
  ): Promise<MikaBootstrapResponse> {
    return this.fetch("/api/agents/mika", {
      method: "POST",
      headers: workspaceHeader(workspaceSlug),
      body: JSON.stringify(data),
    });
  }

  /** 开启一个「用对话创建 agent」的 builder 会话。 */
  async createAgentBuilderSession(data: {
    runtime_id: string;
    model?: string;
  }): Promise<AgentBuilderSession> {
    const raw = await this.fetch<unknown>("/api/agent-builder/sessions", {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(
      raw,
      AgentBuilderSessionSchema,
      EMPTY_AGENT_BUILDER_SESSION,
      { endpoint: "POST /api/agent-builder/sessions" },
    );
  }

  /**
   * 取回调用方所有未完成的「创建 agent」对话。
   *
   * builder 会话被所有聊天列表隐藏（载体 agent 的 `kind = 'system'`），
   * 因此这是找回它们的唯一途径。404 表示后端早于该端点：降级为「无草稿」
   * 而不是让 Agents 页报错，处理方式与 listChatDraftRestores 一致。
   */
  async listAgentBuilderSessions(): Promise<AgentBuilderSessionSummary[]> {
    let raw: unknown;
    try {
      raw = await this.fetch<unknown>("/api/agent-builder/sessions");
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) return [];
      throw err;
    }
    return parseWithFallback(
      raw,
      AgentBuilderSessionListSchema,
      EMPTY_AGENT_BUILDER_SESSION_LIST,
      { endpoint: "GET /api/agent-builder/sessions" },
    ).sessions;
  }

  /**
   * 保存创建对话已形成的配置，包括用户输入但尚未发送的编辑。整对象
   * 后写覆盖：一个会话只有一个编辑者，字段级合并只能还原出一个谁都没
   * 见过的中间态。之后通过 `listAgentBuilderSessions` 读回。
   */
  async saveAgentBuilderDraft(
    sessionId: string,
    draft: StoredAgentDraft,
  ): Promise<void> {
    await this.fetch(`/api/agent-builder/sessions/${sessionId}/draft`, {
      method: "PUT",
      body: JSON.stringify({ draft }),
    });
  }

  /** 把一个进行中的 builder 会话改绑到另一个 runtime。调用方在本次请求
   *  成功前不能把新 runtime 显示为已选中——重点就是让 UI 的 runtime 与
   *  实际执行的 runtime 保持一致。
   *
   *  非 2xx 会在走到这里之前抛错，此时什么都没提交。能走到 parse 说明服务器
   *  已绑定 `data.runtime_id`，因此解析失败时用它兜底——见
   *  agentBuilderRuntimeSwitchFallback。 */
  async switchAgentBuilderRuntime(
    sessionId: string,
    data: { runtime_id: string },
  ): Promise<AgentBuilderRuntimeSwitch> {
    const raw = await this.fetch<unknown>(
      `/api/agent-builder/sessions/${sessionId}/runtime`,
      { method: "PATCH", body: JSON.stringify(data) },
    );
    return parseWithFallback(
      raw,
      AgentBuilderRuntimeSwitchSchema,
      agentBuilderRuntimeSwitchFallback(data.runtime_id),
      { endpoint: "PATCH /api/agent-builder/sessions/{id}/runtime" },
    );
  }

  /** 更新 agent 信息（写入前会检查服务器是否支持会话开场白字段）。 */
  async updateAgent(id: string, data: UpdateAgentRequest): Promise<Agent> {
    assertAgentConversationStartersWriteSupported(data);
    return this.fetch(`/api/agents/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  /** 归档（停用）agent。 */
  async archiveAgent(id: string): Promise<Agent> {
    return this.fetch(`/api/agents/${id}/archive`, { method: "POST" });
  }

  /**
   * 读取 agent 的明文 `custom_env` 映射。仅限 agent 拥有者或 workspace
   * owner/admin（MUL-5438）；agent 身份的会话调用会得到 403。每次成功调用
   * 都会在服务端写一条 `agent_env_revealed` activity_log。MUL-2600。
   */
  async getAgentEnv(id: string): Promise<AgentEnvResponse> {
    return this.fetch(`/api/agents/${id}/env`);
  }

  /**
   * 整体替换 agent 的 `custom_env`。值为 `"****"` 的项在服务端会被保留
   *（**** 守卫），这样 UI 的部分编辑不会用脱敏占位符覆盖真实密钥。仅限
   * agent 拥有者或 workspace owner/admin（MUL-5438）；agent 身份调用返回 403。
   * 每次成功调用写一条 `agent_env_updated` activity_log。MUL-2600。
   */
  async updateAgentEnv(id: string, data: UpdateAgentEnvRequest): Promise<AgentEnvResponse> {
    return this.fetch(`/api/agents/${id}/env`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  /** 恢复（取消归档）agent。 */
  async restoreAgent(id: string): Promise<Agent> {
    return this.fetch(`/api/agents/${id}/restore`, { method: "POST" });
  }

  // 批量取消该 agent 的所有进行中任务（排队/已派发/运行中）。
  // 权限：agent 拥有者或 workspace admin/owner。服务器返回被取消的行数，
  // 并逐个广播 task:cancelled 让其它界面清掉各自的实时卡片。
  async cancelAgentTasks(id: string): Promise<{ cancelled: number }> {
    return this.fetch(`/api/agents/${id}/cancel-tasks`, { method: "POST" });
  }

  /** 列出 runtime；owner="me" 只返回当前用户拥有的。workspaceSlug 用于
   *  覆盖请求头，访问「不是当前所在」的 workspace 时必须传。 */
  async listRuntimes(
    params?: { workspace_id?: string; owner?: "me" },
    workspaceSlug?: string,
  ): Promise<AgentRuntime[]> {
    const search = new URLSearchParams();
    if (params?.workspace_id) search.set("workspace_id", params.workspace_id);
    if (params?.owner) search.set("owner", params.owner);
    // 光有 workspace_id 不够：服务器优先从 slug 请求头解析 workspace，
    // 因此要列出别的 workspace 的 runtime 时，请求头也必须一起覆盖。
    return this.fetch(`/api/runtimes?${search}`, {
      headers: workspaceHeader(workspaceSlug),
    });
  }

  /** 分页列出云端 runtime 节点。 */
  async listCloudRuntimeNodes(
    params?: ListCloudRuntimeNodesParams,
  ): Promise<CloudRuntimeNode[]> {
    const search = new URLSearchParams();
    if (params?.limit !== undefined) search.set("limit", String(params.limit));
    if (params?.offset !== undefined) search.set("offset", String(params.offset));
    const query = search.toString();
    const raw = await this.fetch<unknown>(
      `/api/cloud-runtime/nodes${query ? `?${query}` : ""}`,
    );
    return parseWithFallback(
      raw,
      CloudRuntimeNodeListSchema,
      EMPTY_CLOUD_RUNTIME_NODE_LIST,
      { endpoint: "GET /api/cloud-runtime/nodes" },
    );
  }

  /** 创建一个云端 runtime 节点（异步启动）。 */
  async createCloudRuntimeNode(
    data: CreateCloudRuntimeNodeRequest,
  ): Promise<CloudRuntimeNode> {
    const res = await this.fetchRaw("/api/cloud-runtime/nodes", {
      method: "POST",
      body: JSON.stringify(data),
      extraHeaders: { "Content-Type": "application/json" },
    });
    const raw = await res.json() as unknown;
    return parseWithFallback(
      raw,
      CloudRuntimeNodeSchema,
      EMPTY_CLOUD_RUNTIME_NODE,
      { endpoint: "POST /api/cloud-runtime/nodes" },
    );
  }

  /** 删除（销毁）一个云端 runtime 节点。 */
  async deleteCloudRuntimeNode(instanceId: string): Promise<void> {
    await this.fetchRaw("/api/cloud-runtime/nodes", {
      method: "DELETE",
      body: JSON.stringify({ instance_id: instanceId }),
      extraHeaders: { "Content-Type": "application/json" },
    });
  }

  /** 直接删除 runtime；若上面还绑着活跃 agent，服务器会返回结构化 409。 */
  async deleteRuntime(runtimeId: string): Promise<void> {
    await this.fetch(`/api/runtimes/${runtimeId}`, { method: "DELETE" });
  }

  // deleteRuntime 的「已确认」变体。严格的 DELETE 在还有活跃 agent 绑定时
  // 会以结构化 409 拒绝（`code: "runtime_has_active_agents"`，body 携带阻塞的
  // agent）；前端于是弹确认框，把用户确认的活跃 agent 集合提交到这里。
  // 服务器会在事务内把快照与实时集合比较，不一致则以
  // `code: "runtime_delete_plan_changed"` 拒绝（同形状、含最新 `active_agents`）
  // ——调用方应重新渲染 agent 列表并让用户再次确认。
  //
  // 这些 agent 是被「解绑」，而非归档或删除（MUL-5559）：它们保留自己的
  // 配置、聊天与任务历史，只是需要一个新 runtime 才能再次运行。
  // `agents_archived` 是服务器对 `agents_unbound` 的废弃镜像字段，仅为兼容
  // 旧客户端而保留；请优先使用 `agents_unbound`。
  async unbindAgentsAndDeleteRuntime(
    runtimeId: string,
    expectedActiveAgentIds: string[],
  ): Promise<{
    status: string;
    agents_unbound?: number;
    agents_archived?: number;
    tasks_cancelled: number;
    autopilots_paused?: number;
  }> {
    return this.fetch(`/api/runtimes/${runtimeId}/unbind-agents-and-delete`, {
      method: "POST",
      body: JSON.stringify({ expected_active_agent_ids: expectedActiveAgentIds }),
    });
  }

  /** 局部更新 runtime（可见性、自定义显示名等）。 */
  async updateRuntime(
    runtimeId: string,
    patch: {
      visibility?: "private" | "public";
      /**
       * 自定义显示名。传空字符串清除（服务器恢复为默认名）；不传表示不变——
       * JSON 里的 `null` 被视为「不变」而非「清除」。见 MUL-4217。
       */
      custom_name?: string;
      /** 是否把 custom_name 应用到同一台机器上的每个 runtime。 */
      apply_to_machine?: boolean;
    },
  ): Promise<AgentRuntime> {
    return this.fetch(`/api/runtimes/${runtimeId}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
  }

  // ---------------------------------------------------------------------
  // 自定义 runtime 配置档（MUL-3284）。全部按 workspace 隔离：调用方传
  // workspace id 的方式与 runtimes 列表解析方式一致。
  // ---------------------------------------------------------------------

  /** 列出某 workspace 的 runtime 配置档。 */
  async listRuntimeProfiles(workspaceId: string): Promise<RuntimeProfile[]> {
    const res = await this.fetch<{ runtime_profiles?: RuntimeProfile[] }>(
      `/api/workspaces/${workspaceId}/runtime-profiles`,
    );
    return res.runtime_profiles ?? [];
  }

  /** 获取单个 runtime 配置档详情。 */
  async getRuntimeProfile(
    workspaceId: string,
    profileId: string,
  ): Promise<RuntimeProfile> {
    return this.fetch(
      `/api/workspaces/${workspaceId}/runtime-profiles/${profileId}`,
    );
  }

  /** 新建一个 runtime 配置档。 */
  async createRuntimeProfile(
    workspaceId: string,
    body: CreateRuntimeProfileRequest,
  ): Promise<RuntimeProfile> {
    return this.fetch(`/api/workspaces/${workspaceId}/runtime-profiles`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  /** 更新 runtime 配置档。 */
  async updateRuntimeProfile(
    workspaceId: string,
    profileId: string,
    patch: UpdateRuntimeProfileRequest,
  ): Promise<RuntimeProfile> {
    return this.fetch(
      `/api/workspaces/${workspaceId}/runtime-profiles/${profileId}`,
      {
        method: "PATCH",
        body: JSON.stringify(patch),
      },
    );
  }

  /** 删除 runtime 配置档。 */
  async deleteRuntimeProfile(
    workspaceId: string,
    profileId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/runtime-profiles/${profileId}`,
      { method: "DELETE" },
    );
  }

  /** 查询某 runtime 近 N 天用量（token 数等）；tz 影响「按天」的切分边界。 */
  async getRuntimeUsage(
    runtimeId: string,
    params?: { days?: number; tz?: string },
  ): Promise<RuntimeUsage[]> {
    const search = new URLSearchParams();
    if (params?.days) search.set("days", String(params.days));
    // `tz` 决定趋势图里「按天」日历边界的切分（展示层）。由调用方传入；
    // 后端在缺省时回退到 user.timezone / UTC。
    if (params?.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/usage?${search}`,
    );
    return parseWithFallback<RuntimeUsage[]>(raw, RuntimeUsageListSchema, [], {
      endpoint: "GET /api/runtimes/:id/usage",
    });
  }

  /** 查询某 runtime 的按小时任务活跃度（小时热力图数据）。 */
  async getRuntimeTaskActivity(
    runtimeId: string,
    params?: { tz?: string },
  ): Promise<RuntimeHourlyActivity[]> {
    // 小时热力图跟随查看者的时区，与同页其它报表一致。传查看者的 IANA
    // 时区，让服务器按正确的时区分桶。
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

  /** 查询某 runtime 近 N 天按 agent 拆分的用量。 */
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

  /** 查询某 runtime 近 N 天按小时拆分的用量。 */
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
  // Workspace 仪表盘——`/{slug}/dashboard` 的三组相互独立的汇总数据。
  // 每组都接受可选 `project_id` 把范围收窄到单个项目。
  // 成本在客户端按模型定价表计算（与上面按 runtime 的端点同契约）。
  // ---------------------------------------------------------------------------

  /** 仪表盘：每日用量汇总。 */
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

  /** 仪表盘：按 agent 拆分的用量汇总。 */
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

  /** 仪表盘：agent 运行时长统计。 */
  async getDashboardAgentRunTime(
    params: { days?: number; project_id?: string | null; tz?: string },
  ): Promise<DashboardAgentRunTime[]> {
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    // `tz` 让「最近 N 天」的截止点与查看者的日历对齐，与按 agent 的
    // token 卡片保持一致。
    if (params.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(`/api/dashboard/agent-runtime?${search}`);
    return parseWithFallback<DashboardAgentRunTime[]>(
      raw,
      DashboardAgentRunTimeListSchema,
      [],
      { endpoint: "GET /api/dashboard/agent-runtime" },
    );
  }

  /** 仪表盘：每日运行时长（runtime/daily）汇总。 */
  async getDashboardRunTimeDaily(
    params: { days?: number; project_id?: string | null; tz?: string },
  ): Promise<DashboardRunTimeDaily[]> {
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    // `tz` 按查看者日历切分天桶，让 Time / Tasks 与 Cost / Tokens 图对齐。
    if (params.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(`/api/dashboard/runtime/daily?${search}`);
    return parseWithFallback<DashboardRunTimeDaily[]>(
      raw,
      DashboardRunTimeDailyListSchema,
      [],
      { endpoint: "GET /api/dashboard/runtime/daily" },
    );
  }

  /** 仪表盘：每日失败次数汇总。 */
  async getDashboardFailuresDaily(
    params: { days?: number; project_id?: string | null; tz?: string },
  ): Promise<DashboardFailureDaily[]> {
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    // `tz` 按查看者日历切分天桶，让 Errors 图与另外四个指标的 x 轴一致。
    if (params.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(`/api/dashboard/failures/daily?${search}`);
    return parseWithFallback<DashboardFailureDaily[]>(
      raw,
      DashboardFailureDailyListSchema,
      [],
      { endpoint: "GET /api/dashboard/failures/daily" },
    );
  }

  /** 仪表盘：按 agent 拆分的失败次数汇总。 */
  async getDashboardFailuresByAgent(
    params: { days?: number; project_id?: string | null; tz?: string },
  ): Promise<DashboardFailureByAgent[]> {
    const search = new URLSearchParams();
    if (params.days) search.set("days", String(params.days));
    if (params.project_id) search.set("project_id", params.project_id);
    if (params.tz) search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(`/api/dashboard/failures/by-agent?${search}`);
    return parseWithFallback<DashboardFailureByAgent[]>(
      raw,
      DashboardFailureByAgentListSchema,
      [],
      { endpoint: "GET /api/dashboard/failures/by-agent" },
    );
  }

  /** 发起 runtime 升级到指定版本（异步执行，结果另行查询）。 */
  async initiateUpdate(
    runtimeId: string,
    targetVersion: string,
  ): Promise<RuntimeUpdate> {
    return this.fetch(`/api/runtimes/${runtimeId}/update`, {
      method: "POST",
      body: JSON.stringify({ target_version: targetVersion }),
    });
  }

  /** 查询一次 runtime 升级任务的结果。 */
  async getUpdateResult(
    runtimeId: string,
    updateId: string,
  ): Promise<RuntimeUpdate> {
    return this.fetch(`/api/runtimes/${runtimeId}/update/${updateId}`);
  }

  // 两个「发现类」端点都喂给同一个 UI 状态机（pending/running 时轮询，
  // 然后渲染或失败），所以响应要校验而非强转：解析失败的响应体降级为一条
  // 显式的 "failed" 记录——它展示发现错误、保留手动录入模型的入口，
  // 而不是一个凭空捏造的空目录或无限转圈（MUL-5444）。
  /** 发起一次「拉取 runtime 支持的模型列表」的异步任务（结果另行查询）。 */
  async initiateListModels(runtimeId: string): Promise<RuntimeModelListRequest> {
    const raw = await this.fetch<unknown>(`/api/runtimes/${runtimeId}/models`, {
      method: "POST",
    });
    return parseWithFallback<RuntimeModelListRequest>(
      raw,
      RuntimeModelListRequestSchema,
      { ...MALFORMED_RUNTIME_MODEL_LIST_REQUEST, runtime_id: runtimeId },
      { endpoint: "POST /api/runtimes/{id}/models" },
    );
  }

  /** 查询一次「拉取模型列表」任务的结果。 */
  async getListModelsResult(
    runtimeId: string,
    requestId: string,
  ): Promise<RuntimeModelListRequest> {
    const raw = await this.fetch<unknown>(
      `/api/runtimes/${runtimeId}/models/${requestId}`,
    );
    return parseWithFallback<RuntimeModelListRequest>(
      raw,
      RuntimeModelListRequestSchema,
      {
        ...MALFORMED_RUNTIME_MODEL_LIST_REQUEST,
        id: requestId,
        runtime_id: runtimeId,
      },
      { endpoint: "GET /api/runtimes/{id}/models/{requestId}" },
    );
  }

  /** 发起一次「列出 runtime 本地技能」的异步任务。 */
  async initiateListLocalSkills(
    runtimeId: string,
  ): Promise<RuntimeLocalSkillListRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/local-skills`, {
      method: "POST",
    });
  }

  /** 查询「列出本地技能」任务的结果。 */
  async getListLocalSkillsResult(
    runtimeId: string,
    requestId: string,
  ): Promise<RuntimeLocalSkillListRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/local-skills/${requestId}`);
  }

  /** 发起一次「导入本地技能」的异步任务。 */
  async initiateImportLocalSkill(
    runtimeId: string,
    data: CreateRuntimeLocalSkillImportRequest,
  ): Promise<RuntimeLocalSkillImportRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/local-skills/import`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 查询「导入本地技能」任务的结果。 */
  async getImportLocalSkillResult(
    runtimeId: string,
    requestId: string,
  ): Promise<RuntimeLocalSkillImportRequest> {
    return this.fetch(`/api/runtimes/${runtimeId}/local-skills/import/${requestId}`);
  }

  /** 列出某 agent 的全部历史任务。 */
  async listAgentTasks(agentId: string): Promise<AgentTask[]> {
    return this.fetch(`/api/agents/${agentId}/tasks`);
  }

  // workspace 级 agent 任务快照：每个进行中任务（排队/已派发/运行中）加上
  // 每个 agent 最近一个终态任务。支撑前端「活跃优先，否则取最近终态」的
  // 在线状态推导；一次拉取就覆盖了应用里所有按 agent 的在线读取。
  // workspace 由服务器按 X-Workspace-Slug 请求头解析。
  async getAgentTaskSnapshot(): Promise<AgentTask[]> {
    return this.fetch(`/api/agent-task-snapshot`);
  }

  // 独立的 workspace 级投影。与任务快照不同，它已对运行中的 agent 去重，
  // 只返回消费方需要的展示字段。调用方可按任务来源收窄投影；议题相关
  // 场景还可按登录成员的 My Issues 关系过滤。
  // `parentIssueId` 把投影收窄到某议题的直接子议题——议题详情的子议题表头
  // 正是借此与议题列表表头读到同一数据源。服务器拒绝把该参数与 `scope`
  // 一起用，所以调用方二选一。
  async getWorkspaceWorkingAgents(
    type?: WorkspaceWorkingAgentType,
    mineRelation?: WorkspaceWorkingAgentMineRelation,
    parentIssueId?: string,
  ): Promise<WorkspaceWorkingAgent[]> {
    const search = new URLSearchParams();
    if (type) search.set("type", type);
    if (mineRelation) {
      search.set("scope", "mine");
      search.set("relation", mineRelation);
    } else if (parentIssueId) {
      search.set("parent", parentIssueId);
    }
    const query = search.toString();
    return this.fetch(`/api/working-agents${query ? `?${query}` : ""}`);
  }

  // 每个 agent 最近 30 天、锚定 completed_at 的日活跃数据。一次 workspace 级
  // 拉取同时支撑：Agent 列表迷你折线图（用最近 7 个桶）和 agent 详情页的
  //「最近 30 天」面板（用全部 30 个桶）。
  async getWorkspaceAgentActivity30d(): Promise<AgentActivityBucket[]> {
    return this.fetch(`/api/agent-activity-30d`);
  }

  // 每个 agent 的 30 天运行总次数，供 Agent 列表的 RUNS 列使用。
  async getWorkspaceAgentRunCounts(): Promise<AgentRunCount[]> {
    return this.fetch(`/api/agent-run-counts`);
  }

  /** 获取某议题当前的活跃任务。 */
  async getActiveTasksForIssue(issueId: string): Promise<{ tasks: AgentTask[] }> {
    return this.fetch(`/api/issues/${issueId}/active-task`);
  }

  /** 列出某任务的消息记录。 */
  async listTaskMessages(taskId: string): Promise<TaskMessagePayload[]> {
    const raw = await this.fetch<unknown>(`/api/tasks/${taskId}/messages`);
    return parseWithFallback<TaskMessagePayload[]>(raw, TaskMessagePayloadSchema.array(), [], { endpoint: "GET /api/tasks/:id/messages" });
  }

  async readTaskOutput(taskId: string, messageId: string, offset = 0, query = "") {
    const params = new URLSearchParams({ output_id: messageId, offset: String(offset), q: query });
    const raw = await this.fetch<unknown>(`/api/tasks/${taskId}/messages?${params}`);
    return parseWithFallback<TaskOutputPage | null>(raw, TaskOutputPageSchema, null, { endpoint: "GET /api/tasks/:id/messages?output_id" });
  }

  /** 列出与某议题关联的全部任务运行记录。 */
  async listTasksByIssue(issueId: string): Promise<AgentTask[]> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/task-runs`);
    return parseWithFallback<AgentTask[]>(raw, AgentTaskListSchema, [], {
      endpoint: "GET /api/issues/:id/task-runs",
    });
  }

  /** 列出某议题任务执行轨迹（trace）事件，用于耗时/失败分析。 */
  async listIssueTaskTraceEvents(issueId: string): Promise<{ events: Array<{
    id: string; task_id: string; event_type: string; event_name: string; status: string;
    provider: string; model: string; failure_reason: string; duration_ms?: number;
    input_tokens: number; output_tokens: number; created_at: string;
  }> }> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/trace`);
    return parseWithFallback(raw, IssueTraceEventsResponseSchema, { events: [] }, {
      endpoint: "GET /api/issues/:id/trace",
    });
  }

  /** 获取某议题的执行树（子任务分解结构）。 */
  async getIssueExecutionTree(issueId: string): Promise<IssueExecutionTreeResponse> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/execution-tree`);
    return parseWithFallback<IssueExecutionTreeResponse>(
      raw,
      IssueExecutionTreeResponseSchema,
      EMPTY_ISSUE_EXECUTION_TREE_RESPONSE as IssueExecutionTreeResponse,
      { endpoint: "GET /api/issues/:id/execution-tree" },
    );
  }

  /** 列出外部凭证配置；可按 provider 过滤。 */
  async listExternalCredentialProfiles(provider?: ExternalCredentialProvider): Promise<ExternalCredentialProfile[]> {
    const query = provider ? `?provider=${encodeURIComponent(provider)}` : "";
    const raw = await this.fetch<unknown>(`/api/external-credential-profiles${query}`);
    return parseWithFallback(raw, ExternalCredentialProfileListSchema, { profiles: [] }, {
      endpoint: "GET /api/external-credential-profiles",
    }).profiles;
  }
  /** 获取单个外部凭证配置。 */
  async getExternalCredentialProfile(id: string): Promise<ExternalCredentialProfile> {
    const raw = await this.fetch<unknown>(`/api/external-credential-profiles/${id}`);
    return parseOrThrow(raw, ExternalCredentialProfileSchema, { endpoint: "GET /api/external-credential-profiles/:id" });
  }
  /** 新建外部凭证配置（自带幂等键，防重复提交）。 */
  async createExternalCredentialProfile(data: CreateExternalCredentialProfileRequest, idempotencyKey = createSafeId()): Promise<ExternalCredentialProfile> {
    const raw = await this.fetch<unknown>(`/api/external-credential-profiles`, { method: "POST", body: JSON.stringify(data), headers: { "Idempotency-Key": idempotencyKey } });
    return parseOrThrow(raw, ExternalCredentialProfileSchema, { endpoint: "POST /api/external-credential-profiles", mayHaveCommitted: true });
  }
  /** 更新外部凭证配置。 */
  async updateExternalCredentialProfile(id: string, data: UpdateExternalCredentialProfileRequest): Promise<ExternalCredentialProfile> {
    const raw = await this.fetch<unknown>(`/api/external-credential-profiles/${id}`, { method: "PUT", body: JSON.stringify(data) });
    return parseOrThrow(raw, ExternalCredentialProfileSchema, { endpoint: "PUT /api/external-credential-profiles/:id", mayHaveCommitted: true });
  }
  /** 测试外部凭证是否可用；响应无法解析时兜底为「测试失败」。 */
  async testExternalCredentialProfile(data: TestExternalCredentialProfileRequest): Promise<TestExternalCredentialProfileResponse> {
    const raw = await this.fetch<unknown>(`/api/external-credential-profiles/test`, { method: "POST", body: JSON.stringify(data) });
    return parseWithFallback(raw, TestExternalCredentialProfileResponseSchema, { status: "failed", last_error: "账号测试响应格式无效" }, { endpoint: "POST /api/external-credential-profiles/test" });
  }
  /** 删除外部凭证配置。 */
  async deleteExternalCredentialProfile(id: string): Promise<void> { await this.fetch(`/api/external-credential-profiles/${id}`, { method: "DELETE" }); }

  /** 查询单议题的用量汇总。 */
  async getIssueUsage(issueId: string): Promise<IssueUsageSummary> {
    return this.fetch(`/api/issues/${issueId}/usage`);
  }

  /** 取消议题下的某个任务。 */
  async cancelTask(issueId: string, taskId: string): Promise<AgentTask> {
    return this.fetch(`/api/issues/${issueId}/tasks/${taskId}/cancel`, {
      method: "POST",
    });
  }

  /** 重新运行整个议题（或只重跑指定任务）。 */
  async rerunIssue(issueId: string, taskId?: string): Promise<AgentTask> {
    return this.fetch(`/api/issues/${issueId}/rerun`, {
      method: "POST",
      body: JSON.stringify(taskId ? { task_id: taskId } : {}),
    });
  }

  /** 重试一次「源上下文快速创建」失败的任务。 */
  async retrySourceContextQuickCreate(taskId: string): Promise<AgentTask> {
    const raw = await this.fetch<unknown>(`/api/tasks/${taskId}/retry-source-context`, {
      method: "POST",
    });
    const task = parseWithFallback<AgentTask | null>(raw, AgentTaskSchema, null, {
      endpoint: "POST /api/tasks/:id/retry-source-context",
    });
    if (!task) throw new Error("Invalid source-context retry response");
    return task;
  }

  // Inbox（收件箱区）
  /** 列出收件箱消息。 */
  async listInbox(): Promise<InboxItem[]> {
    const raw = await this.fetch<unknown>("/api/inbox");
    return parseWithFallback(raw, InboxItemListSchema, EMPTY_INBOX_ITEMS, {
      endpoint: "GET /api/inbox",
    });
  }

  /** 把某条收件箱消息标为已读。 */
  async markInboxRead(id: string): Promise<InboxItem> {
    return this.fetch(`/api/inbox/${id}/read`, { method: "POST" });
  }

  /** 把某条收件箱消息标为未读。 */
  async markInboxUnread(id: string): Promise<InboxItem> {
    return this.fetch(`/api/inbox/${id}/unread`, { method: "POST" });
  }

  /** 归档某条收件箱消息。 */
  async archiveInbox(id: string): Promise<InboxItem> {
    return this.fetch(`/api/inbox/${id}/archive`, { method: "POST" });
  }

  // 已归档通知，支撑收件箱的「已归档」子视图。服务端限制条数（v1 无分页）。
  // 有 schema 守卫：契约漂移时渲染为空归档，而不是拖垮整个收件箱。
  async listArchivedInbox(): Promise<InboxItem[]> {
    const raw = await this.fetch<unknown>("/api/inbox/archived");
    return parseWithFallback(raw, InboxItemListSchema, EMPTY_INBOX_ITEMS, {
      endpoint: "GET /api/inbox/archived",
    });
  }

  /** 把某条消息从归档中恢复。 */
  async unarchiveInbox(id: string): Promise<InboxItem> {
    return this.fetch(`/api/inbox/${id}/unarchive`, { method: "POST" });
  }

  /** 查询当前收件箱的未读数。 */
  async getUnreadInboxCount(): Promise<{ count: number }> {
    return this.fetch("/api/inbox/unread-count");
  }

  // 跨 workspace 的未读汇总：用户所属且有未读收件箱消息的每个 workspace
  // 一条记录。支撑 workspace 切换器里「其它 workspace」的小圆点。带 schema
  // 守卫：契约漂移时隐藏圆点，而不是弄崩侧边栏。
  async getInboxUnreadSummary(): Promise<InboxWorkspaceUnread[]> {
    const raw = await this.fetch<unknown>("/api/inbox/unread-summary");
    return parseWithFallback(raw, InboxUnreadSummarySchema, EMPTY_INBOX_UNREAD_SUMMARY, {
      endpoint: "GET /api/inbox/unread-summary",
    });
  }

  /** 把所有收件箱消息标为已读。 */
  async markAllInboxRead(): Promise<{ count: number }> {
    return this.fetch("/api/inbox/mark-all-read", { method: "POST" });
  }

  /** 归档全部收件箱消息。 */
  async archiveAllInbox(): Promise<{ count: number }> {
    return this.fetch("/api/inbox/archive-all", { method: "POST" });
  }

  /** 归档全部已读的收件箱消息。 */
  async archiveAllReadInbox(): Promise<{ count: number }> {
    return this.fetch("/api/inbox/archive-all-read", { method: "POST" });
  }

  /** 归档所有「已完成」的收件箱消息。 */
  async archiveCompletedInbox(): Promise<{ count: number }> {
    return this.fetch("/api/inbox/archive-completed", { method: "POST" });
  }

  // Notification preferences（通知偏好区）
  //
  // `workspaceSlug` 会覆盖默认的 X-Workspace-Slug 请求头（默认跟随当前活跃
  // workspace），从而让调用方能读取「指定 workspace」的偏好——例如用户正在
  // 查看 A workspace，却要遵守 B workspace（那条收件箱通知来源）的静默设置
  //（#3766）。
  /** 读取当前（或指定）workspace 的通知偏好。 */
  async getNotificationPreferences(workspaceSlug?: string): Promise<NotificationPreferenceResponse> {
    const raw = await this.fetch<unknown>(
      "/api/notification-preferences",
      workspaceSlug ? { headers: { "X-Workspace-Slug": workspaceSlug } } : undefined,
    );
    return parseWithFallback(
      raw,
      NotificationPreferenceResponseSchema,
      EMPTY_NOTIFICATION_PREFERENCE_RESPONSE,
      { endpoint: "GET /api/notification-preferences" },
    );
  }

  /** 更新当前（或指定）workspace 的通知偏好。 */
  async updateNotificationPreferences(
    preferences: NotificationPreferences,
    workspaceSlug?: string,
  ): Promise<NotificationPreferenceResponse> {
    const raw = await this.fetch<unknown>("/api/notification-preferences", {
      method: "PATCH",
      headers: workspaceHeader(workspaceSlug),
      body: JSON.stringify({ preferences }),
    });
    return parseWithFallback(
      raw,
      NotificationPreferenceResponseSchema,
      EMPTY_NOTIFICATION_PREFERENCE_RESPONSE,
      { endpoint: "PATCH /api/notification-preferences" },
    );
  }

  // App Config（应用配置区）
  /** 读取全局应用配置（功能开关等）。 */
  async getConfig(): Promise<AppConfigResponse> {
    const raw = await this.fetch<unknown>("/api/config");
    return parseWithFallback<AppConfigResponse>(raw, AppConfigSchema, EMPTY_APP_CONFIG, {
      endpoint: "GET /api/config",
    });
  }

  // Workspaces（workspace 管理区）
  /** 列出当前用户可访问的全部 workspace。 */
  async listWorkspaces(): Promise<Workspace[]> {
    return this.fetch("/api/workspaces");
  }

  /** 获取单个 workspace 详情。 */
  async getWorkspace(id: string): Promise<Workspace> {
    return this.fetch(`/api/workspaces/${id}`);
  }

  /** 新建 workspace（需提供名称、slug 与可选描述/上下文/议题前缀）。 */
  async createWorkspace(data: { name: string; slug: string; description?: string; context?: string; issue_prefix?: string }): Promise<Workspace> {
    return this.fetch("/api/workspaces", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 更新 workspace 资料与设置。 */
  async updateWorkspace(id: string, data: { name?: string; description?: string; context?: string; settings?: Record<string, unknown>; issue_prefix?: string; avatar_url?: string }): Promise<Workspace> {
    return this.fetch(`/api/workspaces/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  }

  async createRepository(workspaceId: string, data: WorkspaceRepo): Promise<WorkspaceRepo> {
    const raw=await this.fetch<unknown>(`/api/workspaces/${workspaceId}/repos`,{method:"POST",body:JSON.stringify(data)});
    return parseOrThrow(raw,WorkspaceRepoSchema,{endpoint:"POST /api/workspaces/:id/repos"});
  }

  async updateRepository(workspaceId:string,id:string,data:Partial<WorkspaceRepo>):Promise<WorkspaceRepo>{
    const raw=await this.fetch<unknown>(`/api/workspaces/${workspaceId}/repos/${id}`,{method:"PATCH",body:JSON.stringify(data)});
    return parseOrThrow(raw,WorkspaceRepoSchema,{endpoint:"PATCH /api/workspaces/:id/repos/:repositoryId"});
  }

  async deleteRepository(workspaceId:string,id:string):Promise<void>{
    await this.fetch(`/api/workspaces/${workspaceId}/repos/${id}`,{method:"DELETE"});
  }

  async putRepositoryLocation(workspaceId:string,id:string,data:{daemon_id:string;local_path:string;execution_mode:"worktree"|"in_place"}):Promise<import('../types/workspace').RepositoryLocation>{
    const raw=await this.fetch<unknown>(`/api/workspaces/${workspaceId}/repos/${id}/locations`,{method:"PUT",body:JSON.stringify(data)});
    return parseOrThrow(raw,RepositoryLocationSchema,{endpoint:"PUT /api/workspaces/:id/repos/:repositoryId/locations"});
  }

  async deleteRepositoryLocation(workspaceId:string,id:string,locationId:string):Promise<void>{
    await this.fetch(`/api/workspaces/${workspaceId}/repos/${id}/locations/${locationId}`,{method:"DELETE"});
  }

  async getWorkspaceRepositoryCenter(workspaceId: string): Promise<import('../types/workspace').WorkspaceRepositoryCenter | null> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/repos/details`);
    return parseWithFallback(raw, WorkspaceRepositoryCenterSchema, null, {endpoint:"GET /api/workspaces/:id/repos/details"});
  }

  /** 探测一个 git 仓库 URL：检查可达性、协议等，返回探测结果。 */
  async probeWorkspaceRepo(
    workspaceId: string,
    data: { url: string },
  ): Promise<WorkspaceRepoProbeResponse> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/repos/probe`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, WorkspaceRepoProbeResponseSchema, {
      endpoint: "POST /api/workspaces/:id/repos/probe",
    });
  }

  /** 解析仓库 URL 为规范化的仓库信息（可指定默认分支）。 */
  async resolveWorkspaceRepo(
    workspaceId: string,
    data: { url: string; default_branch?: string },
  ): Promise<WorkspaceRepo> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/repos/resolve`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseOrThrow(raw, WorkspaceRepoSchema, {
      endpoint: "POST /api/workspaces/:id/repos/resolve",
    });
  }

  /** 列出 workspace 下已安装的插件；旧服务器 404 时返回空列表。 */
  async listPluginInstallations(workspaceId: string): Promise<PluginInstallationListResponse> {
    let raw: unknown;
    try {
      raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/plugins`);
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) return EMPTY_PLUGIN_INSTALLATION_LIST;
      throw error;
    }
    return parseWithFallback(raw, PluginInstallationListResponseSchema, EMPTY_PLUGIN_INSTALLATION_LIST, {
      endpoint: "GET /api/workspaces/{id}/plugins",
    });
  }

  /** workspace 的 MCP 服务器库。响应只带身份与传输方式——服务端存的是
   *  只写条目，所以这里没有需要脱敏的内容。 */
  async listWorkspaceMcpServers(workspaceId: string): Promise<WorkspaceMcpServer[]> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/mcp-servers`);
    return parseWithFallback(raw, WorkspaceMcpServerListSchema, [] as WorkspaceMcpServer[], {
      endpoint: "GET /api/workspaces/{id}/mcp-servers",
    });
  }

  /** 把一台服务器加入库中。它不会自动分配给任何 agent——agent 只能通过
   *  addAgentMcpServer 显式获得它。 */
  async createWorkspaceMcpServer(
    workspaceId: string,
    name: string,
    config: Record<string, unknown>,
    description?: string,
  ): Promise<WorkspaceMcpServer> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/mcp-servers`, {
      method: "POST",
      body: JSON.stringify({ name, config, ...(description !== undefined ? {description} : {}) }),
    });
    return parseWithFallback(raw, WorkspaceMcpServerSchema, EMPTY_WORKSPACE_MCP_SERVER, {
      endpoint: "POST /api/workspaces/{id}/mcp-servers",
    });
  }

  /** 重命名库条目 / 替换配置，或两者都做。agent 的分配按 id 关联，
   *  因此重命名不会影响已有分配。 */
  async updateWorkspaceMcpServer(
    workspaceId: string,
    serverId: string,
    update: { name?: string; description?: string; config?: Record<string, unknown> },
  ): Promise<WorkspaceMcpServer> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/mcp-servers/${encodeURIComponent(serverId)}`,
      { method: "PUT", body: JSON.stringify(update) },
    );
    return parseWithFallback(raw, WorkspaceMcpServerSchema, EMPTY_WORKSPACE_MCP_SERVER, {
      endpoint: "PUT /api/workspaces/{id}/mcp-servers/{serverId}",
    });
  }

  /** 从库中删除一台服务器，同时删除所有指向它的 agent 分配。 */
  async updateWorkspaceMcpMetadata(workspaceId:string, serverId:string, name:string, description:string) {
    const endpoint=`/api/workspaces/${workspaceId}/mcp-servers/${encodeURIComponent(serverId)}/metadata`;
    const raw=await this.fetch<unknown>(endpoint,{method:"PATCH",body:JSON.stringify({name,description})});
    return parseWithFallback(raw,WorkspaceMcpServerSchema,EMPTY_WORKSPACE_MCP_SERVER,{endpoint});
  }
  async workspaceMcpChecks(workspaceId:string,serverId:string,runtimeId?:string) {
    const endpoint=`/api/workspaces/${workspaceId}/mcp-servers/${encodeURIComponent(serverId)}/checks`;
    const raw=await this.fetch<unknown>(endpoint,runtimeId?{method:"POST",body:JSON.stringify({runtime_id:runtimeId})}:undefined);
    return parseWithFallback(raw,WorkspaceMcpChecksSchema,[] as {id:string;runtimeId:string;status:string;toolCount:number;errorCode:string;createdAt:string;completedAt:string|null;stale:boolean}[],{endpoint});
  }

  async deleteWorkspaceMcpServer(workspaceId: string, serverId: string): Promise<void> {
    await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/mcp-servers/${encodeURIComponent(serverId)}`,
      { method: "DELETE" },
    );
  }

  /** 列出分配给该 agent 的 workspace MCP 服务器及其开关状态。 */
  async listAgentMcpServers(agentId: string): Promise<WorkspaceMcpServer[]> {
    const raw = await this.fetch<unknown>(`/api/agents/${agentId}/mcp-servers`);
    return parseWithFallback(raw, WorkspaceMcpServerListSchema, [] as WorkspaceMcpServer[], {
      endpoint: "GET /api/agents/{id}/mcp-servers",
    });
  }

  /** 把一台 workspace 服务器分配给这个 agent。每次写入都返回分配后的完整
   *  列表，客户端无需猜测当前状态。 */
  async addAgentMcpServer(agentId: string, serverId: string): Promise<WorkspaceMcpServer[]> {
    const raw = await this.fetch<unknown>(`/api/agents/${agentId}/mcp-servers`, {
      method: "POST",
      body: JSON.stringify({ server_id: serverId }),
    });
    return parseWithFallback(raw, WorkspaceMcpServerListSchema, [] as WorkspaceMcpServer[], {
      endpoint: "POST /api/agents/{id}/mcp-servers",
    });
  }

  /** 启停某个 agent 对某台 MCP 服务器的使用。 */
  async setAgentMcpServerEnabled(
    agentId: string,
    serverId: string,
    enabled: boolean,
  ): Promise<WorkspaceMcpServer[]> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${agentId}/mcp-servers/${encodeURIComponent(serverId)}/enabled`,
      { method: "PUT", body: JSON.stringify({ enabled }) },
    );
    return parseWithFallback(raw, WorkspaceMcpServerListSchema, [] as WorkspaceMcpServer[], {
      endpoint: "PUT /api/agents/{id}/mcp-servers/{serverId}/enabled",
    });
  }

  /** 移除该 agent 与某台 MCP 服务器的分配。 */
  async removeAgentMcpServer(agentId: string, serverId: string): Promise<WorkspaceMcpServer[]> {
    const raw = await this.fetch<unknown>(
      `/api/agents/${agentId}/mcp-servers/${encodeURIComponent(serverId)}`,
      { method: "DELETE" },
    );
    return parseWithFallback(raw, WorkspaceMcpServerListSchema, [] as WorkspaceMcpServer[], {
      endpoint: "DELETE /api/agents/{id}/mcp-servers/{serverId}",
    });
  }

  /** 列出该 workspace 发布过的全部插件包及各自版本。 */
  async listPluginPackages(workspaceId: string): Promise<PluginPackageListResponse> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/plugins/packages`);
    return parseWithFallback(raw, PluginPackageListResponseSchema, EMPTY_PLUGIN_PACKAGE_LIST, {
      endpoint: "GET /api/workspaces/{id}/plugins/packages",
    });
  }

  /** 发布一个插件包：zip 内含 manifest 与它引用的所有文件。这是完整的发布
   *  通道——插件作者无需自建服务器，且已发布版本之后不可修改。
   *
   *  不走 this.fetch，原因与 uploadFile 相同：浏览器必须自己设置 multipart
   *  边界。 */
  async publishPluginPackage(workspaceId: string, bundle: File): Promise<PluginPackage> {
    const formData = new FormData();
    formData.append("bundle", bundle);

    const res = await fetch(`${this.baseUrl}/api/workspaces/${workspaceId}/plugins/packages`, {
      method: "POST",
      headers: this.authHeaders(),
      body: formData,
      credentials: "include",
    });
    if (!res.ok) {
      if (res.status === 401) this.handleUnauthorized();
      throw new Error(await this.parseErrorMessage(res, `Publishing failed: ${res.status}`));
    }
    const raw = (await res.json()) as unknown;
    return parseWithFallback(raw, PluginPackageSchema, EMPTY_PLUGIN_PACKAGE, {
      endpoint: "POST /api/workspaces/{id}/plugins/packages",
    });
  }

  /** 从运维托管的目录（MULTICA_PLUGIN_DIR）发布——开发通道，让迭代 surface
   *  不必每次改动都压缩再上传。产出仍是不可变版本。 */
  async publishLocalPluginPackage(workspaceId: string, name: string): Promise<PluginPackage> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/plugins/packages/local`, {
      method: "POST",
      body: JSON.stringify({ name }),
    });
    return parseWithFallback(raw, PluginPackageSchema, EMPTY_PLUGIN_PACKAGE, {
      endpoint: "POST /api/workspaces/{id}/plugins/packages/local",
    });
  }

  /** 删除一个插件包（连同其全部版本）。 */
  async deletePluginPackage(workspaceId: string, packageId: string): Promise<void> {
    await this.fetch<void>(`/api/workspaces/${workspaceId}/plugins/packages/${packageId}`, { method: "DELETE" });
  }

  /** 铸造一个托管文档 URL 及其一次性桥接凭证，供 plugin surface 启动。 */
  async getPluginSurfaceLaunch(workspaceId: string, installationId: string, surfaceKey: string): Promise<PluginSurfaceLaunch> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/plugins/${installationId}/surfaces/${encodeURIComponent(surfaceKey)}/launch`,
    );
    return parseWithFallback(raw, PluginSurfaceLaunchSchema, EMPTY_PLUGIN_SURFACE_LAUNCH, {
      endpoint: "GET /api/workspaces/{id}/plugins/{installationId}/surfaces/{surfaceKey}/launch",
    });
  }

  /** 两步安装的第一步。不产生任何写入：返回的是安装存在之前、授权确认屏
   *  需要展示的内容。 */
  async previewPlugin(workspaceId: string, request: PluginPreviewRequest): Promise<PluginPreview> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/plugins/preview`, {
      method: "POST",
      body: JSON.stringify(request),
    });
    return parseWithFallback(raw, PluginPreviewSchema, EMPTY_PLUGIN_PREVIEW, {
      endpoint: "POST /api/workspaces/{id}/plugins/preview",
    });
  }

  /** 正式安装插件（生成安装记录）。 */
  async installPlugin(workspaceId: string, request: PluginInstallRequest): Promise<PluginInstallation> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/plugins`, {
      method: "POST",
      body: JSON.stringify(request),
    });
    return parseWithFallback(raw, PluginInstallationSchema, EMPTY_PLUGIN_INSTALLATION, {
      endpoint: "POST /api/workspaces/{id}/plugins",
    });
  }

  /** 更新某安装的插件配置。 */
  async configurePlugin(workspaceId: string, installationId: string, request: PluginConfigRequest): Promise<PluginInstallation> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/plugins/${installationId}/config`, {
      method: "PUT",
      body: JSON.stringify(request),
    });
    return parseWithFallback(raw, PluginInstallationSchema, EMPTY_PLUGIN_INSTALLATION, {
      endpoint: "PUT /api/workspaces/{id}/plugins/{installationId}/config",
    });
  }

  /** 启停某个插件安装。 */
  async setPluginEnabled(workspaceId: string, installationId: string, enabled: boolean): Promise<PluginInstallation> {
    const action = enabled ? "enable" : "disable";
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/plugins/${installationId}/${action}`, {
      method: "POST",
    });
    return parseWithFallback(raw, PluginInstallationSchema, EMPTY_PLUGIN_INSTALLATION, {
      endpoint: `POST /api/workspaces/{id}/plugins/{installationId}/${action}`,
    });
  }

  /** 代替某个 plugin surface 执行一次 Action API 调用。
   *
   *  surface 自身没有凭证——它通过 postMessage 向宿主申请，宿主以当前登录
   *  用户的会话重新发起调用。安装标识放在 iframe 自己无法伪造的请求头里；
   *  服务器据此推导 workspace，而不是信任客户端发送的任何东西。
   */
  async callPluginAction(
    installationId: string,
    request: { method: "GET" | "POST" | "PATCH" | "PUT" | "DELETE"; path: string; body?: unknown; issueId?: string },
  ): Promise<unknown> {
    const query = request.path === "/context" && request.issueId
      ? `?issue_id=${encodeURIComponent(request.issueId)}`
      : "";
    return this.fetch<unknown>(`/api/plugin-bridge/v1${request.path}${query}`, {
      method: request.method,
      headers: { "X-Multica-Plugin-Installation": installationId },
      body: request.body === undefined ? undefined : JSON.stringify(request.body),
    });
  }

  /** 代替用户调用一次 plugin hook。
   *
   *  `ui` 来自 surface 内的按钮，`manual` 来自议题操作菜单或命令面板。两者
   *  都会阻塞本次（且仅本次）请求：有人正在等答案。`event` 触发器永远不会
   *  走到这里——它由宿主调度，任何客户端都不能申请一个并继承「本应属于
   *  插件」的身份。 */
  async invokePluginHook(
    installationId: string,
    hookKey: string,
    request: { trigger: "ui" | "manual"; issueId?: string; input?: unknown },
  ): Promise<PluginHookResult> {
    const raw = await this.fetch<unknown>(`/api/plugin-bridge/v1/hooks/${encodeURIComponent(hookKey)}`, {
      method: "POST",
      headers: { "X-Multica-Plugin-Installation": installationId },
      body: JSON.stringify({ trigger: request.trigger, issue_id: request.issueId, input: request.input }),
    });
    return parseWithFallback(raw, PluginHookResultSchema, {
      status: "ok",
      hook_key: hookKey,
      trigger: request.trigger,
      latency_ms: 0,
      attempts: 1,
    }, { endpoint: "POST /api/plugin-bridge/v1/hooks/{key}" });
  }

  /** 列出某安装的插件调用历史。 */
  async listPluginInvocations(workspaceId: string, installationId: string): Promise<PluginInvocation[]> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/plugins/${installationId}/invocations`);
    return parseWithFallback(raw, PluginInvocationListSchema, { invocations: [] }, {
      endpoint: "GET /api/workspaces/{id}/plugins/{installationId}/invocations",
    }).invocations;
  }

  /** 轮换（重新签发）插件 token 与签名密钥。 */
  async rotatePluginToken(workspaceId: string, installationId: string): Promise<PluginTokenIssue> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/plugins/${installationId}/token`, {
      method: "POST",
    });
    return parseWithFallback(raw, PluginTokenIssueSchema, { token: "", signing_secret: "" }, {
      endpoint: "POST /api/workspaces/{id}/plugins/{installationId}/token",
    });
  }

  /** 吊销（作废）插件的当前 token 与签名密钥。 */
  async revokePluginToken(workspaceId: string, installationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/plugins/${installationId}/token`, { method: "DELETE" });
  }

  /** 列出某 `mcp` 传输插件 hook 的服务器当前提供的工具。
   *
   *  只读：发现一个工具并不等于采纳它。approvePluginMCPTools 才是授权动作；
   *  这与 `http` 型 hook 不同——后者在管理员已审阅过的 manifest 里声明单个
   *  端点，而 MCP 服务器在运行时自行决定工具列表且之后可以变更。 */
  async listPluginMCPTools(workspaceId: string, installationId: string, hookKey: string): Promise<PluginMCPTool[]> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/plugins/${installationId}/mcp/${encodeURIComponent(hookKey)}/tools`,
    );
    return parseWithFallback(raw, PluginMCPToolListSchema, { tools: [] }, {
      endpoint: "GET /api/workspaces/{id}/plugins/{installationId}/mcp/{hookKey}/tools",
    }).tools;
  }

  /** 固定（授权）某个 hook 的已批准工具集。
   *
   *  `tools` 是完整集合而非增量差异——移除一个工具与新增一个工具的请求形状
   *  相同，因此不存在「你以为撤销了但它还在」的情况。空数组表示整体撤销
   *  该 hook。 */
  async approvePluginMCPTools(
    workspaceId: string,
    installationId: string,
    hookKey: string,
    tools: string[],
  ): Promise<PluginInstallation> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/plugins/${installationId}/mcp/${encodeURIComponent(hookKey)}/tools`,
      { method: "PUT", body: JSON.stringify({ tools }) },
    );
    return parseWithFallback(raw, PluginInstallationSchema, EMPTY_PLUGIN_INSTALLATION, {
      endpoint: "PUT /api/workspaces/{id}/plugins/{installationId}/mcp/{hookKey}/tools",
    });
  }

  /** 卸载插件（删除安装记录及其全部配置）。 */
  async uninstallPlugin(workspaceId: string, installationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/plugins/${installationId}`, {
      method: "DELETE",
    });
  }

  // Members（成员管理区）
  /** 列出 workspace 成员。 */
  async listMembers(workspaceId: string): Promise<MemberWithUser[]> {
    return this.fetch(`/api/workspaces/${workspaceId}/members`);
  }

  /** 邀请（创建）一位 workspace 成员，返回邀请记录。 */
  async createMember(workspaceId: string, data: CreateMemberRequest): Promise<Invitation> {
    return this.fetch(`/api/workspaces/${workspaceId}/members`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 更新成员的角色等信息。 */
  async updateMember(workspaceId: string, memberId: string, data: UpdateMemberRequest): Promise<MemberWithUser> {
    return this.fetch(`/api/workspaces/${workspaceId}/members/${memberId}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  }

  /** 移除 workspace 成员。 */
  async deleteMember(workspaceId: string, memberId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/members/${memberId}`, {
      method: "DELETE",
    });
  }

  /** 退出 workspace。 */
  async leaveWorkspace(workspaceId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/leave`, {
      method: "POST",
    });
  }

  // Invitations（邀请管理区）
  /** 列出 workspace 已发出的邀请。 */
  async listWorkspaceInvitations(workspaceId: string): Promise<Invitation[]> {
    return this.fetch(`/api/workspaces/${workspaceId}/invitations`);
  }

  /** 撤销一条邀请。 */
  async revokeInvitation(workspaceId: string, invitationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/invitations/${invitationId}`, {
      method: "DELETE",
    });
  }

  /** 列出发给我的（待处理）邀请。 */
  async listMyInvitations(): Promise<Invitation[]> {
    return this.fetch("/api/invitations");
  }

  /** 获取一条邀请的详情。 */
  async getInvitation(invitationId: string): Promise<Invitation> {
    return this.fetch(`/api/invitations/${invitationId}`);
  }

  /** 接受邀请，成为该 workspace 成员。 */
  async acceptInvitation(invitationId: string): Promise<MemberWithUser> {
    return this.fetch(`/api/invitations/${invitationId}/accept`, {
      method: "POST",
    });
  }

  /** 拒绝邀请。 */
  async declineInvitation(invitationId: string): Promise<void> {
    await this.fetch(`/api/invitations/${invitationId}/decline`, {
      method: "POST",
    });
  }

  /** 创建一条分享链接（可设角色、过期时间、最大使用次数）。 */
  async createShareLink(workspaceId: string, data: { role?: string; expires_in?: number; max_uses?: number }): Promise<ShareLink> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/share-links`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, ShareLinkSchema, EMPTY_SHARE_LINK, {
      endpoint: "POST /api/workspaces/{id}/share-links",
    });
  }

  /** 列出 workspace 的分享链接。 */
  async listShareLinks(workspaceId: string): Promise<ShareLink[]> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/share-links`);
    return parseWithFallback(raw, ShareLinkListResponseSchema, [], {
      endpoint: "GET /api/workspaces/{id}/share-links",
    });
  }

  /** 撤销一条分享链接。 */
  async revokeShareLink(workspaceId: string, linkId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/share-links/${linkId}`, {
      method: "DELETE",
    });
  }

  /** 通过分享码加入对应 workspace。 */
  async joinByShareLink(code: string): Promise<{ member: MemberWithUser; workspace_id: string; workspace_slug: string }> {
    const raw = await this.fetch<unknown>("/api/share-links/join", {
      method: "POST",
      body: JSON.stringify({ code }),
    });
    return parseWithFallback(raw, JoinShareLinkResponseSchema, EMPTY_JOIN_SHARE_LINK_RESPONSE, {
      endpoint: "POST /api/share-links/join",
    });
  }

  /** 查询分享码对应的 workspace 概要（加入前展示用）。 */
  async getShareLinkInfo(code: string): Promise<ShareLinkInfo> {
    const raw = await this.fetch<unknown>(`/api/share-links/${encodeURIComponent(code)}`);
    return parseWithFallback(raw, ShareLinkInfoSchema, EMPTY_SHARE_LINK_INFO, {
      endpoint: "GET /api/share-links/{code}",
    });
  }

  /** 删除整个 workspace。 */
  async deleteWorkspace(workspaceId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}`, {
      method: "DELETE",
    });
  }

  // Skills（技能库区）
  /** 列出全部技能（概要）。 */
  async listSkills(): Promise<SkillSummary[]> {
    return this.fetch("/api/skills");
  }

  /** 获取技能详情。 */
  async getSkill(id: string): Promise<Skill> {
    return parseWithFallback(await this.fetch(`/api/skills/${id}`), SkillSchema, EMPTY_SKILL, { endpoint: `/api/skills/${id}` });
  }

  /** 新建技能。 */
  async createSkill(data: CreateSkillRequest): Promise<Skill> {
    return this.fetch("/api/skills", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 更新技能内容。 */
  async updateSkill(id: string, data: UpdateSkillRequest): Promise<Skill> {
    return this.fetch(`/api/skills/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  async uploadSkillFile(id: string, file: File, path: string) {
    const body = new FormData(); body.append("file", file); body.append("path", path);
    const response = await this.fetchRaw(`/api/skills/${id}/files/upload`, {method:"POST", body});
    if (!response.ok) throw new Error(await response.text());
    return parseWithFallback(await response.json(), SkillFileSchema, {id:"",skill_id:id,path,content:"",created_at:"",updated_at:"",kind:"text",content_type:"",size_bytes:0,sha256:"",mode:420}, {endpoint:"skill file upload"});
  }

  async getSkillFileBlob(id: string, fileId: string): Promise<Blob> {
    const response = await this.fetchRaw(`/api/skills/${id}/files/${fileId}/content`);
    if (!response.ok) throw new Error(await response.text());
    return response.blob();
  }

  /** 删除技能。 */
  async deleteSkill(id: string): Promise<void> {
    await this.fetch(`/api/skills/${id}`, { method: "DELETE" });
  }

  /** 从远程 URL 导入技能。 */
  async importSkill(data: { url: string }): Promise<Skill> {
    return this.fetch("/api/skills/import", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 从本地 .skill / .zip 压缩包导入技能。不走 this.fetch：浏览器必须自己
   *  设置 multipart 边界。
   *
   *  压缩包路径始终返回结构化的 `{ status, skill, reason }` 响应体。
   *  created/updated 响应返回技能本身；其它状态抛出携带服务器 reason（或
   *  `error`）的错误，让导入对话框能直接展示。 */
  async importSkillArchive(
    file: File,
    onConflict?: "fail" | "overwrite" | "rename" | "skip",
  ): Promise<Skill> {
    const formData = new FormData();
    formData.append("file", file, file.name || "skill.zip");
    if (onConflict) formData.append("on_conflict", onConflict);

    let res: Response;
    try {
      res = await this.fetchRaw("/api/skills/import", {
        method: "POST",
        body: formData,
      });
    } catch (err) {
      throw remapSkillImportError(err);
    }

    const raw = (await res.json()) as unknown;
    return skillFromImportResult(raw, "POST /api/skills/import");
  }

  // 从技能记录的 config.origin 来源重新下载技能，就地更新名称/描述/内容/文件，
  // 但保留技能 id 与它的 agent 绑定关系。
  async refreshSkill(id: string): Promise<Skill> {
    const raw = await this.fetch<unknown>(`/api/skills/${id}/refresh`, {
      method: "POST",
    });
    return parseWithFallback(raw, SkillSchema, EMPTY_SKILL, {
      endpoint: "POST /api/skills/:id/refresh",
    });
  }

  /** 列出某 agent 绑定的技能。 */
  async listAgentSkills(agentId: string): Promise<SkillSummary[]> {
    return this.fetch(`/api/agents/${agentId}/skills`);
  }

  /** 整体设置某 agent 绑定的技能集合。 */
  async setAgentSkills(agentId: string, data: SetAgentSkillsRequest): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/skills`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  // 增量附加：POST /skills/add 只插入给定的技能 id（服务器用 ON CONFLICT DO
  // NOTHING 幂等 upsert），调用方无需先读取 agent 当前的技能集合。
  async addAgentSkills(agentId: string, data: SetAgentSkillsRequest): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/skills/add`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

	/** 启停某 agent 对某个技能的可用状态。 */
	async setAgentSkillEnabled(agentId: string, skillId: string, enabled: boolean): Promise<void> {
		await this.fetch(`/api/agents/${agentId}/skills/${skillId}/enabled`, {
			method: "PUT",
			body: JSON.stringify({ enabled }),
		});
	}

  /** 启停某 agent 对某个「运行时技能」的可用状态。 */
  async setAgentRuntimeSkillEnabled(
    agentId: string,
    data: SetAgentRuntimeSkillEnabledRequest,
  ): Promise<void> {
    await this.fetch(`/api/agents/${agentId}/runtime-skills/enabled`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

	/** 解除某 agent 与某个技能的绑定。 */
	async removeAgentSkill(agentId: string, skillId: string): Promise<void> {
		await this.fetch(`/api/agents/${agentId}/skills/${skillId}`, {
			method: "DELETE",
		});
	}

  // Personal Access Tokens（个人访问令牌区）
  /** 列出个人访问令牌。 */
  async listPersonalAccessTokens(): Promise<PersonalAccessToken[]> {
    return this.fetch("/api/tokens");
  }

  /** 创建个人访问令牌。 */
  async createPersonalAccessToken(data: CreatePersonalAccessTokenRequest): Promise<CreatePersonalAccessTokenResponse> {
    return this.fetch("/api/tokens", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 吊销个人访问令牌。 */
  async revokePersonalAccessToken(id: string): Promise<void> {
    await this.fetch(`/api/tokens/${id}`, { method: "DELETE" });
  }

  // File Upload & Attachments（文件上传与附件区）
  /**
   * 上传一个文件并返回附件记录。
   * @param file 待上传的文件
   * @param opts 可选：把附件关联到某议题 / 评论 / 聊天会话
   * @param signal 可选中止信号，供模块级上传协调器（MUL-5181）在登出时取消
   *  在途上传。被中止时 `fetch` 以 AbortError 拒绝，协调器通过
   *  `signal.aborted` / `err.name === "AbortError"` 与真实失败区分。
   */
  async uploadFile(
    file: File,
    opts?: { issueId?: string; commentId?: string; chatSessionId?: string },
    signal?: AbortSignal,
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
      signal,
    });

    if (!res.ok) {
      if (res.status === 401) this.handleUnauthorized();
      const message = await this.parseErrorMessage(res, `Upload failed: ${res.status}`);
      this.logger.error(`← ${res.status} /api/upload-file`, { rid, duration: `${Date.now() - start}ms`, error: message });
      throw new Error(message);
    }

    this.logger.info(`← ${res.status} /api/upload-file`, { rid, duration: `${Date.now() - start}ms` });
    const raw = (await res.json()) as unknown;
    return parseWithFallback(raw, AttachmentResponseSchema, EMPTY_ATTACHMENT, {
      endpoint: "POST /api/upload-file",
    });
  }

  // Life companion（生活伴侣区）
  async healthOverview(): Promise<HealthOverview> {
    const endpoint = "/api/health/overview";
    return parseWithFallback(await this.fetch<unknown>(endpoint), HealthOverviewSchema, EMPTY_HEALTH, { endpoint, sensitive: true });
  }
  async healthReports(): Promise<{ items: HealthReport[] }> {
    const endpoint = "/api/health/reports";
    const parsed = parseWithFallback(await this.fetch<unknown>(endpoint), HealthReportsSchema, null as { items: HealthReport[] } | null, { endpoint, sensitive: true });
    if (!parsed) throw new Error("体检报告响应无效");
    return parsed;
  }
  async healthRevisions(): Promise<{ items: HealthRevision[] }> {
    const endpoint = "/api/health/revisions";
    const parsed = parseWithFallback(await this.fetch<unknown>(endpoint), HealthRevisionsSchema, null as { items: HealthRevision[] } | null, { endpoint, sensitive: true });
    if (!parsed) throw new Error("历史版本响应无效");
    return parsed;
  }
  async healthReportHistory(id: string): Promise<{ items: HealthExtraction[] }> {
    const endpoint = `/api/health/reports/${encodeURIComponent(id)}/results`;
    const result = parseWithFallback(await this.fetch<unknown>(endpoint), HealthReportHistorySchema, null as { items: HealthExtraction[] } | null, { endpoint, sensitive: true });
    if (!result) throw new Error("报告历史响应无效"); return result;
  }
  private async healthReceipt(endpoint: string, body: unknown, method = "POST") {
    const parsed = parseWithFallback(await this.fetch<unknown>(endpoint, { method, body: JSON.stringify(body) }), HealthReceiptSchema, null as { revision: number } | null, { endpoint, sensitive: true });
    if (!parsed) throw new Error("身体数据保存回执无效");
    return parsed;
  }
  saveHealthProfile(profile: HealthProfile, revision: number) { return this.healthReceipt("/api/health/profile", { profile: { name: profile.name, sex: profile.sex, birth_date: profile.birthDate }, revision }, "PATCH"); }
  saveHealthObservation(value: Partial<HealthObservation>, revision: number) { return this.healthReceipt("/api/health/observations", { observation: observationWire(value), revision }); }
  saveHealthObservations(values: Partial<HealthObservation>[], revision: number) { return this.healthReceipt("/api/health/observations", { observations: values.map(observationWire), revision }); }
  restoreHealthRevision(target: number, revision: number) { return this.healthReceipt("/api/health/revisions/restore", { target, revision }); }
  applyHealthReport(id: string, revision: number) { return this.healthReceipt(`/api/health/reports/${encodeURIComponent(id)}/apply`, { revision }); }
  async uploadHealthReport(file: File): Promise<HealthReport> {
    const endpoint = "/api/health/reports";
    const form = new FormData(); form.append("file", file);
    const response = await this.fetchRaw(endpoint, { method: "POST", body: form });
    const parsed = parseWithFallback(await response.json(), HealthReportSchema, null as HealthReport | null, { endpoint, sensitive: true });
    if (!parsed?.id) throw new Error("体检报告上传回执无效");
    return parsed;
  }
  async processHealthReport(id: string, agentId: string, reprocess: boolean): Promise<HealthReport> {
    const endpoint = `/api/health/reports/${encodeURIComponent(id)}/process`;
    const parsed = parseWithFallback(await this.fetch<unknown>(endpoint, { method: "POST", body: JSON.stringify({ agent_id: agentId, reprocess }) }), HealthReportSchema, null as HealthReport | null, { endpoint, sensitive: true });
    if (!parsed?.id) throw new Error("报告任务回执无效");
    return parsed;
  }
  /** 读取当前用户的伴侣（companion）档案。 */
  async getCompanionProfile(): Promise<CompanionProfileEnvelope> {
    const raw = await this.fetch<unknown>("/api/life/companion");
    return parseWithFallback(raw, CompanionProfileEnvelopeSchema, EMPTY_COMPANION_PROFILE, { endpoint: "GET /api/life/companion" });
  }

  /** 把某个 agent 绑定为当前用户的伴侣。 */
  async setCompanionProfile(agentId: string): Promise<CompanionProfile> {
    const raw = await this.fetch<unknown>("/api/life/companion", { method: "PUT", body: JSON.stringify({ agent_id: agentId }) });
    const envelope = parseOrThrow<{ profile: CompanionProfile }>(raw, CompanionProfileEnvelopeSchema, { endpoint: "PUT /api/life/companion" });
    return parseOrThrow(envelope.profile, CompanionProfileSchema, { endpoint: "PUT /api/life/companion profile", mayHaveCommitted: true });
  }

  /** 按状态列出生活记忆。 */
  async listLifeMemories(status?: string): Promise<LifeMemoryListResponse> {
    const query = status ? `?status=${encodeURIComponent(status)}` : "";
    return parseWithFallback(await this.fetch<unknown>(`/api/life/memories${query}`), LifeMemoryListSchema, EMPTY_LIFE_MEMORIES, { endpoint: "GET /api/life/memories" });
  }
  /** 更新一条生活记忆。 */
  async updateLifeMemory(id: string, data: UpdateLifeMemoryRequest): Promise<LifeMemory> { return parseOrThrow(await this.fetch(`/api/life/memories/${id}`, { method: "PATCH", body: JSON.stringify(data) }), LifeMemorySchema, { endpoint: "PATCH /api/life/memories/:id" }); }
  /** 确认一条生活记忆为真实经历。 */
  async confirmLifeMemory(id: string): Promise<LifeMemory> { return parseOrThrow(await this.fetch(`/api/life/memories/${id}/confirm`, { method: "POST" }), LifeMemorySchema, { endpoint: "POST /api/life/memories/:id/confirm" }); }
  /** 把记忆降级为指定（低置信度）类型。 */
  async downgradeLifeMemory(id: string, kind: LifeMemoryKind): Promise<LifeMemory> { return parseOrThrow(await this.fetch(`/api/life/memories/${id}/downgrade`, { method: "POST", body: JSON.stringify({ kind }) }), LifeMemorySchema, { endpoint: "POST /api/life/memories/:id/downgrade" }); }
  /** 归档一条生活记忆。 */
  async archiveLifeMemory(id: string): Promise<LifeMemory> { return parseOrThrow(await this.fetch(`/api/life/memories/${id}/archive`, { method: "POST" }), LifeMemorySchema, { endpoint: "POST /api/life/memories/:id/archive" }); }
  /** 删除一条生活记忆。 */
  async deleteLifeMemory(id: string): Promise<void> { await this.fetch(`/api/life/memories/${id}`, { method: "DELETE" }); }
  /** 列出某条记忆的历史修订。 */
  async listLifeMemoryRevisions(id: string): Promise<{ revisions: Array<Record<string, unknown>> }> { return this.fetch(`/api/life/memories/${id}/revisions`); }
  /** 列出生活建议（proposal）。 */
  async listLifeProposals(): Promise<LifeProposalListResponse> { return parseWithFallback(await this.fetch("/api/life/proposals"), LifeProposalListSchema, EMPTY_LIFE_PROPOSALS, { endpoint: "GET /api/life/proposals" }); }
  /** 新建一条生活建议。 */
  async createLifeProposal(data: Omit<LifeProposal, "id" | "status" | "expires_at" | "confirmed_at" | "executed_at" | "created_at" | "updated_at">): Promise<LifeProposal> { return parseOrThrow(await this.fetch("/api/life/proposals", { method: "POST", body: JSON.stringify(data) }), LifeProposalSchema, { endpoint: "POST /api/life/proposals" }); }
  /** 确认执行某条生活建议。 */
  async confirmLifeProposal(id: string): Promise<unknown> { return this.fetch(`/api/life/proposals/${id}/confirm`, { method: "POST" }); }
  /** 拒绝某条生活建议。 */
  async rejectLifeProposal(id: string): Promise<LifeProposal> { return parseOrThrow(await this.fetch(`/api/life/proposals/${id}/reject`, { method: "POST" }), LifeProposalSchema, { endpoint: "POST /api/life/proposals/:id/reject" }); }
  /** 列出生活实验。 */
  async listLifeExperiments(): Promise<LifeExperimentListResponse> { return parseWithFallback(await this.fetch("/api/life/experiments"), LifeExperimentListSchema, EMPTY_LIFE_EXPERIMENTS, { endpoint: "GET /api/life/experiments" }); }
  /** 提前终止一轮生活实验。 */
  async stopLifeExperimentRound(id: string, reason: string): Promise<LifeExperimentRound> { return parseOrThrow(await this.fetch(`/api/life/experiment-rounds/${id}/stop`, { method: "POST", body: JSON.stringify({ reason }) }), LifeExperimentRoundSchema, { endpoint: "POST /api/life/experiment-rounds/:id/stop" }); }
  /** 提交一轮生活实验的复盘（感受/负担等）。 */
  async reviewLifeExperimentRound(id: string, data: { outcome: string; feelings: string; burden: string; companion_correction: string }): Promise<LifeExperimentRound> { return parseOrThrow(await this.fetch(`/api/life/experiment-rounds/${id}/review`, { method: "POST", body: JSON.stringify(data) }), LifeExperimentRoundSchema, { endpoint: "POST /api/life/experiment-rounds/:id/review" }); }
  /** 列出生活编年史条目。 */
  async listLifeChronicle(): Promise<LifeChronicleListResponse> { return parseWithFallback(await this.fetch("/api/life/chronicle"), LifeChronicleListSchema, EMPTY_LIFE_CHRONICLE, { endpoint: "GET /api/life/chronicle" }); }
  /** 列出「主动关怀」检查结果。 */
  async listLifeProactiveChecks(): Promise<LifeProactiveCheckListResponse> { return parseWithFallback(await this.fetch("/api/life/proactive-checks"), LifeProactiveCheckListSchema, EMPTY_LIFE_PROACTIVE_CHECKS, { endpoint: "GET /api/life/proactive-checks" }); }
  /** 为某条编年史补充「后来的理解」。 */
  async updateLifeChronicleLaterUnderstanding(id: string, understanding: string): Promise<LifeChronicleEntry> { return parseOrThrow(await this.fetch(`/api/life/chronicle/${id}/later-understanding`, { method: "PATCH", body: JSON.stringify({ understanding_later: understanding }) }), LifeChronicleEntrySchema, { endpoint: "PATCH /api/life/chronicle/:id/later-understanding" }); }
  /** 列出「人生身份」的历史版本。 */
  async listLifeIdentityVersions(): Promise<LifeIdentityListResponse> { return parseWithFallback(await this.fetch("/api/life/identity/versions"), LifeIdentityListSchema, EMPTY_LIFE_IDENTITIES, { endpoint: "GET /api/life/identity/versions" }); }
  /** 新建一个「人生身份」版本。 */
  async createLifeIdentityVersion(data: Record<string, unknown>): Promise<unknown> { return this.fetch("/api/life/identity/versions", { method: "POST", body: JSON.stringify(data) }); }
  /** 激活某个「人生身份」版本。 */
  async activateLifeIdentityVersion(id: string): Promise<unknown> { return this.fetch(`/api/life/identity/versions/${id}/activate`, { method: "POST" }); }
  /** 列出人际关系事件。 */
  async listLifeRelationshipEvents(): Promise<LifeRelationshipListResponse> { return parseWithFallback(await this.fetch("/api/life/relationship-events"), LifeRelationshipListSchema, EMPTY_LIFE_RELATIONSHIPS, { endpoint: "GET /api/life/relationship-events" }); }
  /** 解决一条人际关系事件。 */
  async resolveLifeRelationshipEvent(id: string, status: string, resolution: string): Promise<unknown> { return this.fetch(`/api/life/relationship-events/${id}/resolve`, { method: "POST", body: JSON.stringify({ status, resolution }) }); }
  /** 列出生活素材（material）。 */
  async listLifeMaterials(): Promise<LifeMaterialListResponse> { return parseWithFallback(await this.fetch("/api/life/materials"), LifeMaterialListSchema, EMPTY_LIFE_MATERIALS, { endpoint: "GET /api/life/materials" }); }
  /** 列出内部想法（internal thoughts）。 */
  async listLifeInternalThoughts(): Promise<LifeInternalThoughtListResponse> { return parseWithFallback(await this.fetch("/api/life/internal-thoughts"), LifeInternalThoughtListSchema, EMPTY_LIFE_INTERNAL_THOUGHTS, { endpoint: "GET /api/life/internal-thoughts" }); }
  /** 新建一条生活素材。 */
  async createLifeMaterial(content: string): Promise<unknown> { return this.fetch("/api/life/materials", { method: "POST", body: JSON.stringify({ content, metadata: {} }) }); }
  /** 列出生活主题。 */
  async listLifeTopics(): Promise<LifeTopicListResponse> { return parseWithFallback(await this.fetch("/api/life/topics"), LifeTopicListSchema, EMPTY_LIFE_TOPICS, { endpoint: "GET /api/life/topics" }); }
  /** 更新某生活主题的状态。 */
  async updateLifeTopic(id: string, status: string): Promise<unknown> { return this.fetch(`/api/life/topics/${id}`, { method: "PATCH", body: JSON.stringify({ status }) }); }
  /** 列出生活承诺（commitment）。 */
  async listLifeCommitments(): Promise<LifeCommitmentListResponse> { return parseWithFallback(await this.fetch("/api/life/commitments"), LifeCommitmentListSchema, EMPTY_LIFE_COMMITMENTS, { endpoint: "GET /api/life/commitments" }); }
  /** 更新一条生活承诺。 */
  async updateLifeCommitment(id: string, data: Record<string, unknown>): Promise<unknown> { return this.fetch(`/api/life/commitments/${id}`, { method: "PATCH", body: JSON.stringify(data) }); }
  /** 读取「主动关怀」策略。 */
  async getLifeProactivePolicy(): Promise<LifeProactivePolicy> { return parseOrThrow(await this.fetch("/api/life/proactive-policy"), LifeProactivePolicySchema, { endpoint: "GET /api/life/proactive-policy" }); }
  /** 更新「主动关怀」策略。 */
  async updateLifeProactivePolicy(data: LifeProactivePolicy | Record<string, unknown>): Promise<unknown> { return this.fetch("/api/life/proactive-policy", { method: "PUT", body: JSON.stringify(data) }); }
  /** 列出生活观察者（observer）。 */
  async listLifeObservers(): Promise<LifeObserverListResponse> { return parseWithFallback(await this.fetch("/api/life/observers"), LifeObserverListSchema, EMPTY_LIFE_OBSERVERS, { endpoint: "GET /api/life/observers" }); }
  /** 新建一个生活观察者。 */
  async createLifeObserver(data: Record<string, unknown>): Promise<unknown> { return this.fetch("/api/life/observers", { method: "POST", body: JSON.stringify(data) }); }
  /** 更新某生活观察者的状态。 */
  async updateLifeObserver(id: string, status: string): Promise<unknown> { return this.fetch(`/api/life/observers/${id}`, { method: "PATCH", body: JSON.stringify({ status }) }); }
  /** 为观察者添加一条知识。 */
  async addLifeObserverKnowledge(id: string, data: { title: string; content: string; source?: string }): Promise<unknown> { return this.fetch(`/api/life/observers/${id}/knowledge`, { method: "POST", body: JSON.stringify(data) }); }
  /** 创建观察者的一个新版本。 */
  async createLifeObserverVersion(id: string, data: Record<string, unknown>): Promise<unknown> { return this.fetch(`/api/life/observers/${id}/versions`, { method: "POST", body: JSON.stringify(data) }); }
  /** 手动触发一次观察者运行。 */
  async runLifeObserver(id: string): Promise<unknown> { return this.fetch(`/api/life/observers/${id}/run`, { method: "POST" }); }
  /** 列出「观察席」占用情况。 */
  async listLifeObservationSeat(): Promise<LifeObservationSeatResponse> { return parseWithFallback(await this.fetch("/api/life/observation-seat"), LifeObservationSeatSchema, EMPTY_LIFE_OBSERVATION_SEAT, { endpoint: "GET /api/life/observation-seat" }); }
  /** 列出生活模块。 */
  async listLifeModules(): Promise<LifeModuleListResponse> { return parseWithFallback(await this.fetch("/api/life/modules"), LifeModuleListSchema, EMPTY_LIFE_MODULES, { endpoint: "GET /api/life/modules" }); }
  /** 更新某生活模块的状态。 */
  async updateLifeModule(id: string, status: string): Promise<unknown> { return this.fetch(`/api/life/modules/${id}`, { method: "PATCH", body: JSON.stringify({ status }) }); }
  /** 列出认知任务（cognition jobs）。 */
  async listLifeCognitionJobs(): Promise<LifeCognitionJobListResponse> { return parseWithFallback(await this.fetch("/api/life/cognition-jobs"), LifeCognitionJobListSchema, EMPTY_LIFE_JOBS, { endpoint: "GET /api/life/cognition-jobs" }); }
  /** 重试某个失败的认知任务。 */
  async retryLifeCognitionJob(id: string): Promise<unknown> { return this.fetch(`/api/life/cognition-jobs/${id}/retry`, { method: "POST" }); }
  /** 列出「升级评估」记录。 */
  async listLifeUpgradeEvaluations(): Promise<LifeUpgradeEvaluationListResponse> { return parseWithFallback(await this.fetch("/api/life/upgrade-evaluations"), LifeUpgradeEvaluationListSchema, EMPTY_LIFE_UPGRADES, { endpoint: "GET /api/life/upgrade-evaluations" }); }

  // Chat Sessions（聊天会话区）
  /** 列出聊天会话；可按状态过滤，workspaceSlug 可覆盖目标 workspace。 */
  async listChatSessions(
    params?: { status?: string },
    workspaceSlug?: string,
  ): Promise<ChatSession[]> {
    const query = params?.status ? `?status=${params.status}` : "";
    const raw: unknown = await this.fetch(`/api/chat/sessions${query}`, {
      headers: workspaceHeader(workspaceSlug),
    });
    return parseWithFallback(raw, ChatSessionListSchema, EMPTY_CHAT_SESSION_LIST, {
      endpoint: "GET /api/chat/sessions",
    });
  }

  /** 获取单个聊天会话。 */
  async getChatSession(id: string): Promise<ChatSession> {
    const raw: unknown = await this.fetch(`/api/chat/sessions/${id}`);
    return parseWithFallback(raw, ChatSessionSchema, EMPTY_CHAT_SESSION, {
      endpoint: "GET /api/chat/sessions/:id",
    });
  }

  /** 新建一个与指定 agent 的聊天会话。 */
  async createChatSession(
    data: {
      agent_id: string;
      title?: string;
      project_id?: string | null;
      reuse_existing?: boolean;
    },
    workspaceSlug?: string,
  ): Promise<ChatSession> {
    const raw: unknown = await this.fetch("/api/chat/sessions", {
      method: "POST",
      headers: workspaceHeader(workspaceSlug),
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, ChatSessionSchema, EMPTY_CHAT_SESSION, {
      endpoint: "POST /api/chat/sessions",
    });
  }

  /** 删除聊天会话。 */
  async deleteChatSession(id: string): Promise<void> {
    await this.fetch(`/api/chat/sessions/${id}`, { method: "DELETE" });
  }

  // 为某会话最新一条 assistant 轮次刷新快捷动作建议。发后即忘：服务器入队一次
  // 后台重生成，刷新好的 pill 通过 chat:quick_actions 实时事件送达，所以调用方
  // 把「处理中」占位符锚定在自己已知的那一轮上，而不是本响应上。
  // 携带调用方正在刷新的消息 id，让服务器能原子地确认它仍是该会话的最新一轮
  //（否则 409）——即使出现 WS 先于 HTTP 到达的竞态，也能让客户端的 pending
  // 标记与 chat:quick_actions 最终解析的那一轮保持一致，无需再做响应对账
  //（MUL-5149）。
  async regenerateChatQuickActions(
    sessionId: string,
    messageId: string,
  ): Promise<void> {
    await this.fetch(`/api/chat/sessions/${sessionId}/quick-actions/regenerate`, {
      method: "POST",
      body: JSON.stringify({ message_id: messageId }),
    });
  }

  /** 修改会话标题或所属项目。 */
  async updateChatSession(
    id: string,
    data: { title: string } | { project_id: string | null },
  ): Promise<ChatSession> {
    return this.fetch(`/api/chat/sessions/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  }

  /** 置顶/取消置顶会话。 */
  async setChatSessionPinned(id: string, pinned: boolean): Promise<ChatSession> {
    return this.fetch(`/api/chat/sessions/${id}/pin`, {
      method: "PATCH",
      body: JSON.stringify({ pinned }),
    });
  }

  /** 归档/取消归档会话。 */
  async setChatSessionArchived(id: string, archived: boolean): Promise<ChatSession> {
    return this.fetch(`/api/chat/sessions/${id}/archive`, {
      method: "PATCH",
      body: JSON.stringify({ archived }),
    });
  }

  // Quick-agent bar：每个用户的置顶 agent 列表。
  /** 列出当前用户置顶的 agent（快捷 agent 栏）。 */
  async listChatPinnedAgents(): Promise<ChatPinnedAgent[]> {
    return this.fetch("/api/chat/pinned-agents");
  }

  /** 置顶一个 agent。 */
  async pinChatAgent(agentId: string): Promise<ChatPinnedAgent> {
    return this.fetch("/api/chat/pinned-agents", {
      method: "POST",
      body: JSON.stringify({ agent_id: agentId }),
    });
  }

  /** 取消置顶 agent。 */
  async unpinChatAgent(agentId: string): Promise<void> {
    await this.fetch(`/api/chat/pinned-agents/${agentId}`, { method: "DELETE" });
  }

  /** 列出会话的全部消息（一次性全量）。 */
  async listChatMessages(sessionId: string): Promise<ChatMessage[]> {
    const raw: unknown = await this.fetch(`/api/chat/sessions/${sessionId}/messages`);
    return parseWithFallback(raw, ChatMessageListSchema, EMPTY_CHAT_MESSAGE_LIST, {
      endpoint: "GET /api/chat/sessions/:id/messages",
    });
  }

  /** 分页拉取会话消息（支持向上翻页游标）。 */
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
    try {
      const raw: unknown = await this.fetch(
        `/api/chat/sessions/${sessionId}/messages/page?${query.toString()}`,
      );
      return parseWithFallback(
        raw,
        ChatMessagesPageSchema,
        { messages: [], limit, has_more: false, next_cursor: null },
        {
          endpoint: "GET /api/chat/sessions/:id/messages/page",
        },
      );
    } catch (err) {
      // 部署顺序兼容：在本端点出现之前部署的后端会对未知路由返回 404。
      // 回退到旧的整表端点，这样无论服务器还是客户端先发布，聊天都不会白屏。
      // 只有初始（无游标）页会回退——旧端点一次返回全部消息，回退页因此报告
      // has_more: false，也不存在后续请求需要翻译。游标请求上的 404 是意外状态，
      // 直接向上抛而不是把整个列表再复制一份。
      if (err instanceof ApiError && err.status === 404 && !params.before) {
        const messages = await this.listChatMessages(sessionId);
        return { messages, limit, has_more: false, next_cursor: null };
      }
      throw err;
    }
  }

  /** 向会话发送一条用户消息（可带附件）。 */
  async sendChatMessage(
    sessionId: string,
    content: string,
    attachmentIds?: string[],
    healthReportIds?: string[],
  ): Promise<SendChatMessageResponse> {
    const body: {
      content: string;
      attachment_ids?: string[];
      health_report_ids?: string[];
    } = { content };
    if (healthReportIds?.length) body.health_report_ids = healthReportIds;
    if (attachmentIds && attachmentIds.length > 0) {
      body.attachment_ids = attachmentIds;
    }
    const raw = await this.fetch<unknown>(`/api/chat/sessions/${sessionId}/messages`, {
      method: "POST",
      body: JSON.stringify(body),
    });
    const response = parseWithFallback<SendChatMessageResponse | null>(
      raw,
      SendChatMessageResponseSchema,
      null,
      { endpoint: "POST /api/chat/sessions/:id/messages" },
    );
    if (!response) throw new Error("invalid send chat message response");
    return response;
  }

  /** 查询会话当前挂起的任务（等待中的 agent 回复）。 */
  async getPendingChatTask(sessionId: string): Promise<ChatPendingTask> {
    const raw = await this.fetch<unknown>(`/api/chat/sessions/${sessionId}/pending-task`);
    return parseWithFallback(raw, ChatPendingTaskSchema, EMPTY_CHAT_PENDING_TASK, {
      endpoint: "GET /api/chat/sessions/:id/pending-task",
    });
  }

  /** 把某会话的一条排队任务提前（优先处理）。 */
  async prioritizeQueuedChatTask(
    sessionId: string,
    taskId: string,
  ): Promise<PrioritizeQueuedChatTaskResponse> {
    const raw = await this.fetch<unknown>(
      `/api/chat/sessions/${sessionId}/queued-tasks/${taskId}/prioritize`,
      { method: "POST" },
    );
    return parseWithFallback(
      raw,
      PrioritizeQueuedChatTaskResponseSchema,
      EMPTY_PRIORITIZE_QUEUED_CHAT_TASK_RESPONSE,
      { endpoint: "POST /api/chat/sessions/:id/queued-tasks/:taskId/prioritize" },
    );
  }

  /** 清空某会话的排队任务（删除排队状态）。 */
  async clearQueuedChatTasks(sessionId: string): Promise<void> {
    await this.fetch(`/api/chat/sessions/${sessionId}/queued-tasks`, {
      method: "DELETE",
    });
  }

  /** 会话待处理的「延迟取消后的草稿恢复」列表（#5219）。
   *  404 表示后端早于该端点——当作「无待恢复项」，旧服务器不会让编辑器报错。 */
  async listChatDraftRestores(sessionId: string): Promise<ChatDraftRestoresResponse> {
    let raw: unknown;
    try {
      raw = await this.fetch<unknown>(`/api/chat/sessions/${sessionId}/draft-restores`);
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        return { restores: [] };
      }
      throw err;
    }
    return parseWithFallback(raw, ChatDraftRestoresResponseSchema, EMPTY_CHAT_DRAFT_RESTORES, {
      endpoint: "GET /api/chat/sessions/{id}/draft-restores",
    });
  }

  /** 幂等消费一条草稿恢复——删除一条已消费的恢复是 204 空操作。 */
  async consumeChatDraftRestore(sessionId: string, restoreId: string): Promise<void> {
    await this.fetch(`/api/chat/sessions/${sessionId}/draft-restores/${restoreId}`, {
      method: "DELETE",
    });
  }

  /** 列出所有会话的排队任务（跨会话）。 */
  async listPendingChatTasks(): Promise<PendingChatTasksResponse> {
    return this.fetch(`/api/chat/pending-tasks`);
  }

  /** 是否还有任何排队的聊天任务（供 UI 做提醒角标等）。 */
  async hasAnyPendingChatTasks(): Promise<HasPendingChatTasksResponse> {
    return this.fetch(`/api/chat/pending-tasks/has-any`);
  }

  /** 将会话标记为已读。 */
  async markChatSessionRead(sessionId: string): Promise<void> {
    await this.fetch(`/api/chat/sessions/${sessionId}/read`, { method: "POST" });
  }

  // 在请求头宣告「持久草稿恢复」能力（#5219）。只有发送该头的客户端，服务器
  // 才会延迟「空记录」判定——也因此才会把同步恢复从响应里扣下；不带此头会被
  // 当作 #5219 前的旧客户端，得到旧行为。
  async cancelTaskById(
    taskId: string,
    options?: { queuedAction?: "edit" | "remove"; sessionId?: string },
  ): Promise<CancelTaskResponse> {
    const params = new URLSearchParams();
    if (options?.queuedAction) {
      if (!options.sessionId) throw new Error("sessionId is required for queued-only cancellation");
      params.set("expected_status", "queued");
      params.set("chat_session_id", options.sessionId);
      params.set("queue_action", options.queuedAction);
    }
    const query = params.size > 0 ? `?${params}` : "";
    const raw = await this.fetch<unknown>(`/api/tasks/${taskId}/cancel${query}`, {
      method: "POST",
      headers: { "X-Client-Capabilities": CHAT_DRAFT_RESTORE_CAPABILITY },
    });
    return parseWithFallback(raw, CancelTaskResponseSchema, EMPTY_CANCEL_TASK_RESPONSE, {
      endpoint: "POST /api/tasks/{taskId}/cancel",
    });
  }

  /** 列出议题的附件列表。 */
  async listAttachments(issueId: string): Promise<Attachment[]> {
    return this.fetch(`/api/issues/${issueId}/attachments`);
  }

  // 重新拉取一份新的附件元数据记录。服务器每次调用都会重新签名
  // `download_url`（30 分钟有效），因此点击下载的流程走这里，避免把 TanStack
  // Query 里缓存的过期签名 URL 交给用户。
  async getAttachment(id: string): Promise<Attachment> {
    const raw = await this.fetch<unknown>(`/api/attachments/${id}`);
    return parseWithFallback(raw, AttachmentResponseSchema, EMPTY_ATTACHMENT, {
      endpoint: "GET /api/attachments/{id}",
    });
  }

  /** 删除附件。 */
  async deleteAttachment(id: string): Promise<void> {
    await this.fetch(`/api/attachments/${id}`, { method: "DELETE" });
  }

  // 取回一个「可文本预览」附件的原始字节。
  //
  // 该端点绕开 CloudFront CORS（CDN 未配置）以及对 `text/*` 家族的
  // Content-Disposition: attachment（两者都会阻止渲染器拿到响应体）。服务器
  // 一律以 `text/plain; charset=utf-8` 响应以保证安全；原始 MIME 通过
  // `X-Original-Content-Type` 请求头回传，预览分发器据此在 markdown / html /
  // 纯代码之间做选择。
  //
  // 走 fetchRaw 以便继承标准鉴权头、401 → handleUnauthorized 恢复、请求 id
  // 日志与 ApiError 形状。413 / 415 被转成带类型的 Preview*Error，让弹窗能
  // 渲染具体回退而非通用失败。
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

  // 通过统一下载端点取回附件的原始字节。
  //
  // 这是那些服务器无法提供「可原生加载 URL」部署环境的兜底内联媒体通道。
  // `GET /api/attachments/{id}` 只在 CloudFront 签名或预签名模式下才把
  // `download_url` 升级为签名存储 URL；在 **proxy** 模式（自托管 MinIO 或任何
  // 内部主机上的存储端点，默认的 `auto` 模式会把这类归为 proxy）下，它再次返回
  // 受鉴权的 API 路径。那些无法在原生 `<img>` 资源请求上携带会话 cookie 的
  // 客户端——Desktop 的 file:// 渲染器、移动 webview、跨域部署的 web——在这里
  // 拿到字节，再以 object URL 渲染。
  //
  // 走 fetchRaw 以继承标准鉴权头、401 → handleUnauthorized 恢复、请求 id 日志
  // 与 ApiError 形状。调用方必须在元数据刷新已表明「没有签名 URL」之后才走此路：
  // 其它模式下该端点会 302 到存储，而存储上未为 JS fetch 配置 CORS。
  async getAttachmentBlob(id: string): Promise<Blob> {
    const res = await this.fetchRaw(`/api/attachments/${id}/download`);
    return res.blob();
  }

  // Projects（项目区）
  /** 列出项目（可按状态过滤）。 */
  async listProjects(params?: { status?: string }): Promise<ListProjectsResponse> {
    const search = new URLSearchParams();
    if (params?.status) search.set("status", params.status);
    return this.fetch(`/api/projects?${search}`);
  }

  /** 获取项目详情。 */
  async getProject(id: string): Promise<Project> {
    return this.fetch(`/api/projects/${id}`);
  }

  /** 新建项目。 */
  async createProject(data: CreateProjectRequest): Promise<Project> {
    return this.fetch("/api/projects", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 更新项目。 */
  async updateProject(id: string, data: UpdateProjectRequest): Promise<Project> {
    return this.fetch(`/api/projects/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  /** 删除项目。 */
  async deleteProject(id: string): Promise<void> {
    await this.fetch(`/api/projects/${id}`, { method: "DELETE" });
  }

  // Project resources（项目资源区）
  /** 列出项目的资源。 */
  async listProjectResources(
    projectId: string,
  ): Promise<ListProjectResourcesResponse> {
    return this.fetch(`/api/projects/${projectId}/resources`);
  }

  /** 给项目添加一个资源。 */
  async createProjectResource(
    projectId: string,
    data: CreateProjectResourceRequest,
  ): Promise<ProjectResource> {
    return this.fetch(`/api/projects/${projectId}/resources`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 更新项目资源。 */
  async updateProjectResource(
    projectId: string,
    resourceId: string,
    data: UpdateProjectResourceRequest,
  ): Promise<ProjectResource> {
    return this.fetch(`/api/projects/${projectId}/resources/${resourceId}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  /** 删除项目资源。 */
  async deleteProjectResource(
    projectId: string,
    resourceId: string,
  ): Promise<void> {
    await this.fetch(`/api/projects/${projectId}/resources/${resourceId}`, {
      method: "DELETE",
    });
  }

  // Labels（标签区）
  /** 列出某类资源（默认 issue）的标签。 */
  async listLabels(resourceType: LabelResourceType = "issue"): Promise<ListLabelsResponse> {
    const raw = await this.fetch<unknown>(`/api/labels?resource_type=${resourceType}`);
    return parseWithFallback(raw, ListLabelsResponseSchema, EMPTY_LIST_LABELS_RESPONSE, {
      endpoint: "GET /api/labels",
    });
  }

  /** 获取单个标签。 */
  async getLabel(id: string): Promise<Label> {
    const raw = await this.fetch<unknown>(`/api/labels/${id}`);
    return parseWithFallback(raw, LabelSchema, EMPTY_LABEL, {
      endpoint: "GET /api/labels/{id}",
    });
  }

  /** 新建标签。 */
  async createLabel(data: CreateLabelRequest): Promise<Label> {
    const raw = await this.fetch<unknown>(`/api/labels`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, LabelSchema, EMPTY_LABEL, {
      endpoint: "POST /api/labels",
    });
  }

  /** 更新标签。 */
  async updateLabel(id: string, data: UpdateLabelRequest): Promise<Label> {
    const raw = await this.fetch<unknown>(`/api/labels/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, LabelSchema, EMPTY_LABEL, {
      endpoint: "PUT /api/labels/{id}",
    });
  }

  /** 删除标签。 */
  async deleteLabel(id: string): Promise<void> {
    await this.fetch(`/api/labels/${id}`, { method: "DELETE" });
  }

  // 议题状态目录（MUL-6243）。读操作对所有 workspace 成员开放；下面的写操作
  // 仅限 owner/admin，否则返回 403。
  /** 列出议题状态目录（可按需包含已归档）。 */
  async listIssueStatuses(includeArchived = false): Promise<ListIssueStatusesResponse> {
    const query = includeArchived ? "?include_archived=true" : "";
    const raw = await this.fetch<unknown>(`/api/issue-statuses${query}`);
    return parseWithFallback(raw, ListIssueStatusesResponseSchema, EMPTY_LIST_ISSUE_STATUSES_RESPONSE, {
      endpoint: "GET /api/issue-statuses",
    });
  }

  /** 新建一个自定义议题状态。 */
  async createIssueStatus(data: CreateIssueStatusRequest): Promise<IssueStatusEntry> {
    const raw = await this.fetch<unknown>(`/api/issue-statuses`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, IssueStatusEntrySchema, EMPTY_ISSUE_STATUS_ENTRY, {
      endpoint: "POST /api/issue-statuses",
    });
  }

  /** 更新议题状态（名称、类别、颜色等）。 */
  async updateIssueStatus(id: string, data: UpdateIssueStatusRequest): Promise<IssueStatusEntry> {
    const raw = await this.fetch<unknown>(`/api/issue-statuses/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, IssueStatusEntrySchema, EMPTY_ISSUE_STATUS_ENTRY, {
      endpoint: "PATCH /api/issue-statuses/{id}",
    });
  }

  /** 在单条服务端语句里重写某个类别下自定义状态的排列顺序。无法用一串
   *  updateIssueStatus 表达：若中途某行被拒绝，前面的行已经重排好了而调用方
   *  只看到一个失败。（MUL-6243） */
  async reorderIssueStatuses(
    category: IssueStatusCategory,
    ids: string[],
  ): Promise<ListIssueStatusesResponse> {
    const raw = await this.fetch<unknown>(`/api/issue-statuses/reorder`, {
      method: "PATCH",
      body: JSON.stringify({ category, ids }),
    });
    return parseWithFallback(raw, ListIssueStatusesResponseSchema, EMPTY_LIST_ISSUE_STATUSES_RESPONSE, {
      endpoint: "PATCH /api/issue-statuses/reorder",
    });
  }

  /** 归档一个自定义状态，使其不再用于新指派。已在用该状态的议题保留它并按
   *  其所属类别的规则继续运作；只有新的指派会被拒绝。内建状态返回 403。 */
  async archiveIssueStatus(id: string): Promise<IssueStatusEntry> {
    const raw = await this.fetch<unknown>(`/api/issue-statuses/${id}`, { method: "DELETE" });
    return parseWithFallback(raw, IssueStatusEntrySchema, EMPTY_ISSUE_STATUS_ENTRY, {
      endpoint: "DELETE /api/issue-statuses/{id}",
    });
  }

  // Custom issue properties（自定义议题属性区）
  /** 列出自定义属性目录（可按需包含已归档）。 */
  async listProperties(includeArchived = false): Promise<ListPropertiesResponse> {
    const suffix = includeArchived ? "?include_archived=true" : "";
    let raw: unknown;
    try {
      raw = await this.fetch<unknown>(`/api/properties${suffix}`);
    } catch (error) {
      // 早于自定义属性的后端会在此 404（例如只回滚了服务器的场景）。把它当作
      // 空目录处理：属性相关的 UI 区块消失，活跃目录对账会剥离持久化的属性
      // 排序/筛选，因此不会把属性参数发给旧服务器。其它错误保持常规的
      // 查询错误语义。
      if (error instanceof Error && "status" in error && (error as { status?: number }).status === 404) {
        return EMPTY_LIST_PROPERTIES_RESPONSE;
      }
      throw error;
    }
    return parseWithFallback(raw, ListPropertiesResponseSchema, EMPTY_LIST_PROPERTIES_RESPONSE, {
      endpoint: "GET /api/properties",
    });
  }

  /** 快捷动作目录——对所有调用方用同一份投影。
   *
   *  服务器除 `private` 归属外不隐藏任何东西；调用方能否 RUN 某个动作由
   *  runQuickAction 回答，而不是这里。刻意不做「仅可运行」模式：按权限过滤
   *  侧边栏会让看同一议题的两个人看到不同的侧边栏且无法解释。
   *
   *  早于快捷动作的后端会在此 404；当作空目录处理，让侧边栏区块和设置页
   *  直接不渲染。 */
  async listQuickActions(opts?: { includeArchived?: boolean }): Promise<ListQuickActionsResponse> {
    const suffix = opts?.includeArchived === true ? "?include_archived=true" : "";
    let raw: unknown;
    try {
      raw = await this.fetch<unknown>(`/api/quick-actions${suffix}`);
    } catch (error) {
      if (error instanceof Error && "status" in error && (error as { status?: number }).status === 404) {
        return EMPTY_LIST_QUICK_ACTIONS_RESPONSE;
      }
      throw error;
    }
    return parseWithFallback(raw, ListQuickActionsResponseSchema, EMPTY_LIST_QUICK_ACTIONS_RESPONSE, {
      endpoint: "GET /api/quick-actions",
    });
  }

  /** 新建一条快捷动作。 */
  async createQuickAction(data: CreateQuickActionRequest): Promise<QuickAction> {
    const raw = await this.fetch<unknown>(`/api/quick-actions`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, QuickActionSchema, EMPTY_QUICK_ACTION, {
      endpoint: "POST /api/quick-actions",
    });
  }

  /** 更新一条快捷动作。 */
  async updateQuickAction(id: string, data: UpdateQuickActionRequest): Promise<QuickAction> {
    const raw = await this.fetch<unknown>(`/api/quick-actions/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, QuickActionSchema, EMPTY_QUICK_ACTION, {
      endpoint: "PATCH /api/quick-actions/{id}",
    });
  }

  /** 删除一条快捷动作。 */
  async deleteQuickAction(id: string): Promise<void> {
    await this.fetch<void>(`/api/quick-actions/${id}`, { method: "DELETE" });
  }

  /** 对某个议题执行一条快捷动作。响应是一个携带 `trigger_outcomes` 的 Comment
   *  ——与 POST /comments 返回的形状相同——因此调用方复用同一套结果处理器，
   *  天然支持 `queued` / `coalesced` / `deferred` / `blocked`，而不必维护一套
   *  会漂移的平行词汇。 */
  async runQuickAction(issueId: string, quickActionId: string): Promise<Comment> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/quick-actions/${quickActionId}/run`, {
      method: "POST",
    });
    return parseWithFallback(raw, CommentSchema, EMPTY_COMMENT, {
      endpoint: "POST /api/issues/{id}/quick-actions/{quickActionId}/run",
    });
  }

  /** 渲染一条快捷动作「将要发布的内容」而不真正发布。支撑编辑器交接（⌥-点击
   *  与 `/` 菜单），让用户在发送前可以修改。响应读不到时返回 ""——调用方
   *  必须把空串当作「不插入任何内容」，而不是清空编辑器。 */
  async renderQuickAction(issueId: string, quickActionId: string): Promise<string> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/quick-actions/${quickActionId}/render`, {
      method: "POST",
    });
    const parsed = parseWithFallback(raw, QuickActionRenderSchema, { content: "" }, {
      endpoint: "POST /api/issues/{id}/quick-actions/{quickActionId}/render",
    });
    return parsed.content;
  }

  /** 新建一个自定义议题属性。 */
  async createProperty(data: CreatePropertyRequest): Promise<IssueProperty> {
    const raw = await this.fetch<unknown>(`/api/properties`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, IssuePropertySchema, EMPTY_ISSUE_PROPERTY, {
      endpoint: "POST /api/properties",
    });
  }

  /** 更新自定义属性定义。 */
  async updateProperty(id: string, data: UpdatePropertyRequest): Promise<IssueProperty> {
    const raw = await this.fetch<unknown>(`/api/properties/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, IssuePropertySchema, EMPTY_ISSUE_PROPERTY, {
      endpoint: "PATCH /api/properties/{id}",
    });
  }

  /** 给某议题设置自定义属性值。 */
  async setIssueProperty(issueId: string, propertyId: string, value: IssuePropertyValue): Promise<IssuePropertiesResponse> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/properties/${propertyId}`, {
      method: "PUT",
      body: JSON.stringify({ value }),
    });
    return parseWithFallback(raw, IssuePropertiesResponseSchema, EMPTY_ISSUE_PROPERTIES_RESPONSE, {
      endpoint: "PUT /api/issues/{id}/properties/{propertyId}",
    });
  }

  /** 移除某议题的自定义属性值。 */
  async unsetIssueProperty(issueId: string, propertyId: string): Promise<IssuePropertiesResponse> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/properties/${propertyId}`, {
      method: "DELETE",
    });
    return parseWithFallback(raw, IssuePropertiesResponseSchema, EMPTY_ISSUE_PROPERTIES_RESPONSE, {
      endpoint: "DELETE /api/issues/{id}/properties/{propertyId}",
    });
  }

  /** 列出某议题当前的标签。 */
  async listLabelsForIssue(issueId: string): Promise<IssueLabelsResponse> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/labels`);
    return parseWithFallback(raw, ResourceLabelsResponseSchema, EMPTY_RESOURCE_LABELS_RESPONSE, {
      endpoint: "GET /api/issues/{id}/labels",
    });
  }

  /** 给议题打上标签。 */
  async attachLabel(issueId: string, labelId: string): Promise<IssueLabelsResponse> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/labels`, {
      method: "POST",
      body: JSON.stringify({ label_id: labelId }),
    });
    return parseWithFallback(raw, ResourceLabelsResponseSchema, EMPTY_RESOURCE_LABELS_RESPONSE, {
      endpoint: "POST /api/issues/{id}/labels",
    });
  }

  /** 移除议题上的某个标签。 */
  async detachLabel(issueId: string, labelId: string): Promise<IssueLabelsResponse> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/labels/${labelId}`, {
      method: "DELETE",
    });
    return parseWithFallback(raw, ResourceLabelsResponseSchema, EMPTY_RESOURCE_LABELS_RESPONSE, {
      endpoint: "DELETE /api/issues/{id}/labels/{labelId}",
    });
  }

  /** 列出某 agent/skill 资源上的标签。 */
  async listLabelsForResource(
    resourceType: "agent" | "skill",
    resourceId: string,
  ): Promise<ResourceLabelsResponse> {
    const raw = await this.fetch<unknown>(`/api/${resourceType === "agent" ? "agents" : "skills"}/${resourceId}/labels`);
    return parseWithFallback(raw, ResourceLabelsResponseSchema, EMPTY_RESOURCE_LABELS_RESPONSE, {
      endpoint: `GET /api/${resourceType === "agent" ? "agents" : "skills"}/{id}/labels`,
    });
  }

  /** 给某 agent/skill 资源打上标签。 */
  async attachLabelToResource(
    resourceType: "agent" | "skill",
    resourceId: string,
    labelId: string,
  ): Promise<ResourceLabelsResponse> {
    const raw = await this.fetch<unknown>(`/api/${resourceType === "agent" ? "agents" : "skills"}/${resourceId}/labels`, {
      method: "POST",
      body: JSON.stringify({ label_id: labelId }),
    });
    return parseWithFallback(raw, ResourceLabelsResponseSchema, EMPTY_RESOURCE_LABELS_RESPONSE, {
      endpoint: `POST /api/${resourceType === "agent" ? "agents" : "skills"}/{id}/labels`,
    });
  }

  /** 移除某 agent/skill 资源上的标签。 */
  async detachLabelFromResource(
    resourceType: "agent" | "skill",
    resourceId: string,
    labelId: string,
  ): Promise<ResourceLabelsResponse> {
    const raw = await this.fetch<unknown>(`/api/${resourceType === "agent" ? "agents" : "skills"}/${resourceId}/labels/${labelId}`, {
      method: "DELETE",
    });
    return parseWithFallback(raw, ResourceLabelsResponseSchema, EMPTY_RESOURCE_LABELS_RESPONSE, {
      endpoint: `DELETE /api/${resourceType === "agent" ? "agents" : "skills"}/{id}/labels/{labelId}`,
    });
  }

  // Saved issue views（MUL-4796）。响应经过 zod 校验，让已安装的桌面端构建在
  // 后端演进时仍可运行；格式异常的列表降级为 []（选择器只显示内建视图）
  // 而不是让整页白屏。
  /** 列出某作用域（scope）下的已保存议题视图。 */
  async listIssueViews(params: {
    scope_type: string;
    scope_id?: string | null;
  }): Promise<IssueView[]> {
    const qs = new URLSearchParams({ scope_type: params.scope_type });
    if (params.scope_id) qs.set("scope_id", params.scope_id);
    const raw = await this.fetch<unknown>(`/api/issue-views?${qs.toString()}`);
    return parseWithFallback(raw, IssueViewListSchema, [], {
      endpoint: "GET /api/issue-views",
    });
  }

  /** 新建一个已保存议题视图。 */
  async createIssueView(data: CreateIssueViewRequest): Promise<IssueView | null> {
    const raw = await this.fetch<unknown>("/api/issue-views", {
      method: "POST",
      body: JSON.stringify(data),
    });
    // null 兜底：创建本身在服务端已成功；解析不了的响应不能让弹窗崩溃——
    // 调用方重新拉取列表即可。
    return parseWithFallback(raw, IssueViewSchema.nullable(), null, {
      endpoint: "POST /api/issue-views",
    });
  }

  /** 更新已保存视图（带 expected_revision 并发保护）。 */
  async updateIssueView(
    id: string,
    data: {
      name?: string;
      visibility?: "private" | "workspace";
      scope_variant?: string | null;
      query?: Record<string, unknown>;
      display?: Record<string, unknown>;
      expected_revision: number;
    },
  ): Promise<IssueView | null> {
    const raw = await this.fetch<unknown>(`/api/issue-views/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, IssueViewSchema.nullable(), null, {
      endpoint: "PATCH /api/issue-views/{id}",
    });
  }

  /** 获取单个已保存视图。 */
  async getIssueView(id: string): Promise<IssueView | null> {
    const raw = await this.fetch<unknown>(`/api/issue-views/${id}`);
    return parseWithFallback(raw, IssueViewSchema.nullable(), null, {
      endpoint: "GET /api/issue-views/{id}",
    });
  }

  /** 删除已保存视图。 */
  async deleteIssueView(id: string): Promise<void> {
    await this.fetch(`/api/issue-views/${id}`, { method: "DELETE" });
  }

  /** 读取某作用域的视图偏好（隐藏了哪些视图、排列顺序等）。 */
  async getIssueViewPreference(params: {
    scope_type: string;
    scope_id?: string | null;
  }): Promise<IssueViewPreference> {
    const qs = new URLSearchParams({ scope_type: params.scope_type });
    if (params.scope_id) qs.set("scope_id", params.scope_id);
    const raw = await this.fetch<unknown>(`/api/issue-view-preferences?${qs.toString()}`);
    return parseWithFallback(raw, IssueViewPreferenceSchema, EMPTY_ISSUE_VIEW_PREFERENCE, {
      endpoint: "GET /api/issue-view-preferences",
    });
  }

  /** 保存某作用域的视图偏好（整体覆盖）。 */
  async putIssueViewPreference(data: {
    scope_type: string;
    scope_id?: string | null;
    prefs: { hidden: string[]; order: string[] };
  }): Promise<IssueViewPreference> {
    const raw = await this.fetch<unknown>("/api/issue-view-preferences", {
      method: "PUT",
      body: JSON.stringify(data),
    });
    return parseWithFallback(raw, IssueViewPreferenceSchema, EMPTY_ISSUE_VIEW_PREFERENCE, {
      endpoint: "PUT /api/issue-view-preferences",
    });
  }

  // Pins（置顶区）
  /** 列出置顶项。
   *  include=view 是能力声明：服务器不会把「视图置顶」返回给未声明支持它的
   *  客户端（旧构建把所有非议题置顶当作项目置顶，并在 404 时自动删除）。 */
  async listPins(): Promise<PinnedItem[]> {
    return this.fetch("/api/pins?include=view");
  }

  /** 新增一个置顶项。 */
  async createPin(data: CreatePinRequest): Promise<PinnedItem> {
    return this.fetch("/api/pins", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 删除指定类型与 id 的置顶项。 */
  async deletePin(itemType: PinnedItemType, itemId: string): Promise<void> {
    await this.fetch(`/api/pins/${itemType}/${itemId}`, { method: "DELETE" });
  }

  /** 重新排列置顶顺序。 */
  async reorderPins(data: ReorderPinsRequest): Promise<void> {
    await this.fetch("/api/pins/reorder", {
      method: "PUT",
      body: JSON.stringify(data),
    });
  }

  // Squads（战队/小组区）
  /** 列出全部 squad。 */
  async listSquads(): Promise<Squad[]> {
    const raw = await this.fetch<unknown>(`/api/squads`);
    return parseWithFallback(raw, SquadListSchema, EMPTY_SQUAD_LIST, {
      endpoint: "GET /api/squads",
    }) as Squad[];
  }

  /** 获取单个 squad。 */
  async getSquad(id: string): Promise<Squad> {
    const raw = await this.fetch<unknown>(`/api/squads/${id}`);
    return parseWithFallback(raw, SquadSchema, EMPTY_SQUAD, {
      endpoint: "GET /api/squads/:id",
    }) as Squad;
  }

  /** 新建 squad（需指定队长 leader_id）。 */
  async createSquad(data: { name: string; description?: string; leader_id: string; avatar_url?: string }): Promise<Squad> {
    const raw = await this.fetch<unknown>("/api/squads", { method: "POST", body: JSON.stringify(data) });
    return parseWithFallback(raw, SquadSchema, EMPTY_SQUAD, {
      endpoint: "POST /api/squads",
    }) as Squad;
  }

  /** 更新 squad 信息。 */
  async updateSquad(id: string, data: { name?: string; description?: string; instructions?: string; leader_id?: string; avatar_url?: string }): Promise<Squad> {
    const raw = await this.fetch<unknown>(`/api/squads/${id}`, { method: "PUT", body: JSON.stringify(data) });
    return parseWithFallback(raw, SquadSchema, EMPTY_SQUAD, {
      endpoint: "PUT /api/squads/:id",
    }) as Squad;
  }

  /** 删除 squad。 */
  async deleteSquad(id: string): Promise<void> {
    await this.fetch(`/api/squads/${id}`, { method: "DELETE" });
  }

  /** 列出 squad 成员。 */
  async listSquadMembers(squadId: string): Promise<SquadMember[]> {
    return this.fetch(`/api/squads/${squadId}/members`);
  }

  /** 添加 squad 成员。 */
  async addSquadMember(squadId: string, data: { member_type: string; member_id: string; role?: string }): Promise<SquadMember> {
    return this.fetch(`/api/squads/${squadId}/members`, { method: "POST", body: JSON.stringify(data) });
  }

  /** 移除 squad 成员。 */
  async removeSquadMember(squadId: string, data: { member_type: string; member_id: string }): Promise<void> {
    await this.fetch(`/api/squads/${squadId}/members`, { method: "DELETE", body: JSON.stringify(data) });
  }

  /** 修改 squad 成员的角色。 */
  async updateSquadMemberRole(squadId: string, data: { member_type: string; member_id: string; role: string }): Promise<SquadMember> {
    return this.fetch(`/api/squads/${squadId}/members/role`, { method: "PATCH", body: JSON.stringify(data) });
  }

  // 每个 squad 的成员状态快照：每名成员一行，含推导出的
  // working/idle/offline/unstable 状态与每个 agent 当前正在跑的议题。
  // 用宽松 schema 解析，使新的服务端状态值或额外字段不会把 Squad 页弄白屏
  //（#2143）。
  async getSquadMemberStatus(squadId: string): Promise<SquadMemberStatusListResponse> {
    const raw = await this.fetch<unknown>(`/api/squads/${squadId}/members/status`);
    return parseWithFallback(raw, SquadMemberStatusListResponseSchema, EMPTY_SQUAD_MEMBER_STATUS_LIST, {
      endpoint: "GET /api/squads/:id/members/status",
    }) as SquadMemberStatusListResponse;
  }

  // Autopilots（自动驾驶/自动化 agent 区）
  /** 列出 autopilot（可按状态过滤）。 */
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

  /** 获取单个 autopilot（含其配置明细）。 */
  async getAutopilot(id: string): Promise<GetAutopilotResponse> {
    return this.fetch(`/api/autopilots/${id}`);
  }

  /** 新建 autopilot。 */
  async createAutopilot(data: CreateAutopilotRequest): Promise<Autopilot> {
    return this.fetch("/api/autopilots", {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 更新 autopilot 配置。 */
  async updateAutopilot(id: string, data: UpdateAutopilotRequest): Promise<Autopilot> {
    return this.fetch(`/api/autopilots/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  }

  /** 删除 autopilot。 */
  async deleteAutopilot(id: string): Promise<void> {
    await this.fetch(`/api/autopilots/${id}`, { method: "DELETE" });
  }

  // 授予某 workspace 成员对该 autopilot 的显式写权限。授予与撤销都返回完整
  // 的更新后协作者列表，调用方无需二次请求即可刷新。
  async grantAutopilotAccess(id: string, userId: string): Promise<AutopilotCollaboratorsResponse> {
    return this.fetch(`/api/autopilots/${id}/collaborators`, {
      method: "POST",
      body: JSON.stringify({ user_id: userId }),
    });
  }

  /** 撤销某成员对该 autopilot 的访问权限。 */
  async revokeAutopilotAccess(id: string, userId: string): Promise<AutopilotCollaboratorsResponse> {
    return this.fetch(`/api/autopilots/${id}/collaborators/${userId}`, {
      method: "DELETE",
    });
  }

  /** 手动立即触发一次 autopilot 运行。
   *  手动「立即运行」通常返回一条运行记录（含下游准入失败）。配额被拒是
   *  例外，返回 429。UI 对两条路径都做处理，不展示「假成功」的 toast。 */
  async triggerAutopilot(id: string): Promise<AutopilotRun> {
    const raw = await this.fetch<unknown>(`/api/autopilots/${id}/trigger`, {
      method: "POST",
      headers: { "Idempotency-Key": createSafeId() },
    });
    return parseWithFallback(raw, AutopilotRunSchema, FALLBACK_AUTOPILOT_RUN, {
      endpoint: "POST /api/autopilots/:id/trigger",
    });
  }

  async listAutopilotRuns(id: string, params?: { limit?: number; offset?: number }): Promise<ListAutopilotRunsResponse> {
    const search = new URLSearchParams();
    if (params?.limit) search.set("limit", params.limit.toString());
    if (params?.offset) search.set("offset", params.offset.toString());
    return this.fetch(`/api/autopilots/${id}/runs?${search}`);
  }

  // 返回单次运行并含完整 trigger_payload。列表响应为保持体积省略了
  // trigger_payload（一个 webhook 信封可达 256 KiB × 行数上限），因此详情视图
  // 走此路由。
  async getAutopilotRun(autopilotId: string, runId: string): Promise<AutopilotRun> {
    return this.fetch(`/api/autopilots/${autopilotId}/runs/${runId}`);
  }

  /** 为 autopilot 新建一个触发器（定时/webhook 等）。 */
  async createAutopilotTrigger(autopilotId: string, data: CreateAutopilotTriggerRequest): Promise<AutopilotTrigger> {
    return this.fetch(`/api/autopilots/${autopilotId}/triggers`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  }

  /** 更新 autopilot 触发器。 */
  async updateAutopilotTrigger(autopilotId: string, triggerId: string, data: UpdateAutopilotTriggerRequest): Promise<AutopilotTrigger> {
    return this.fetch(`/api/autopilots/${autopilotId}/triggers/${triggerId}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  }

  /** 删除 autopilot 触发器。 */

  /** 删除 autopilot 触发器。 */
  async deleteAutopilotTrigger(autopilotId: string, triggerId: string): Promise<void> {
    await this.fetch(`/api/autopilots/${autopilotId}/triggers/${triggerId}`, { method: "DELETE" });
  }

  /** 预览一条 cron 表达式在指定时区下的下次触发时间。 */
  async cronPreview(params: { expr: string; tz: string }): Promise<CronPreviewResponse> {
    const search = new URLSearchParams();
    search.set("expr", params.expr);
    search.set("tz", params.tz);
    const raw = await this.fetch<unknown>(`/api/autopilots/cron-preview?${search}`);
    return parseWithFallback(
      raw,
      CronPreviewResponseSchema,
      UNREADABLE_CRON_PREVIEW_RESPONSE,
      { endpoint: "GET /api/autopilots/cron-preview" },
    );
  }

  /** 轮换 autopilot webhook 触发器的签名 token。 */
  async rotateAutopilotTriggerWebhookToken(
    autopilotId: string,
    triggerId: string,
  ): Promise<AutopilotTrigger> {
    return this.fetch(
      `/api/autopilots/${autopilotId}/triggers/${triggerId}/rotate-webhook-token`,
      { method: "POST" },
    );
  }

  // Webhook 投递记录——列表为精简字段（不含 raw_body / selected_headers /
  // response_body），详情返回完整行。两类响应都用宽松 schema 解析：未知的
  // 服务端 `status` / `signature_status` 值会降级为通用行，而不会丢弃整份列表。
  /** 分页列出某 autopilot 的 webhook 投递记录（精简字段）。 */
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

  /** 获取单条 webhook 投递记录（完整字段）。 */
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

  // 重放会创建一条新的投递记录，通过 `replayed_from_delivery_id` 指向原记录。
  // 服务器用 400 拒绝「签名无效 / 已被拒」投递的重放——UI 对这些行保持按钮
  // 禁用，但服务器才是权威。
  async replayAutopilotDelivery(
    autopilotId: string,
    deliveryId: string,
  ): Promise<WebhookDelivery> {
    const raw = await this.fetch<unknown>(
      `/api/autopilots/${autopilotId}/deliveries/${deliveryId}/replay`,
      { method: "POST", headers: { "Idempotency-Key": createSafeId() } },
    );
    return parseWithFallback(
      raw,
      WebhookDeliveryResponseSchema,
      { ...EMPTY_WEBHOOK_DELIVERY, autopilot_id: autopilotId },
      { endpoint: "POST /api/autopilots/:id/deliveries/:deliveryId/replay" },
    );
  }

  // GitHub integration（GitHub 集成区）
  /** 获取 GitHub OAuth 连接 URL（可指定连接后跳回 github / repositories 页）。 */
  async getGitHubConnectURL(
    workspaceId: string,
    returnTo?: "github" | "repositories",
  ): Promise<GitHubConnectResponse> {
    const search = new URLSearchParams();
    if (returnTo) search.set("return_to", returnTo);
    const suffix = search.size > 0 ? `?${search.toString()}` : "";
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/github/connect${suffix}`,
    );
    return parseWithFallback(
      raw,
      GitHubConnectResponseSchema,
      EMPTY_GITHUB_CONNECT_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/github/connect" },
    );
  }

  /** 列出 workspace 已安装的 GitHub App 安装。 */
  async listGitHubInstallations(workspaceId: string): Promise<ListGitHubInstallationsResponse> {
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/github/installations`,
    );
    return parseWithFallback(
      raw,
      ListGitHubInstallationsResponseSchema,
      EMPTY_LIST_GITHUB_INSTALLATIONS_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/github/installations" },
    );
  }

  /** 列出某 GitHub 安装下可访问的仓库（分页）。 */
  async listGitHubInstallationRepositories(
    workspaceId: string,
    installationId: string,
    params: { page?: number; per_page?: number } = {},
  ): Promise<ListGitHubRepositoriesResponse> {
    const search = new URLSearchParams();
    if (params.page !== undefined) search.set("page", String(params.page));
    if (params.per_page !== undefined) search.set("per_page", String(params.per_page));
    const suffix = search.size > 0 ? `?${search.toString()}` : "";
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/github/installations/${installationId}/repositories${suffix}`,
    );
    return parseWithFallback(
      raw,
      ListGitHubRepositoriesResponseSchema,
      EMPTY_LIST_GITHUB_REPOSITORIES_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/github/installations/:installationId/repositories" },
    );
  }

  /** 删除（卸载）一个 GitHub 安装。 */
  async deleteGitHubInstallation(workspaceId: string, installationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/github/installations/${installationId}`, {
      method: "DELETE",
    });
  }

  /** 列出某议题关联的 GitHub PR。 */
  async listIssuePullRequests(issueId: string): Promise<{ pull_requests: GitHubPullRequest[] }> {
    const raw = await this.fetch<unknown>(`/api/issues/${issueId}/pull-requests`);
    return parseWithFallback(
      raw,
      IssuePullRequestsResponseSchema,
      EMPTY_ISSUE_PULL_REQUESTS_RESPONSE,
      { endpoint: "GET /api/issues/:id/pull-requests" },
    );
  }

  // VCS integration（Forgejo / Gitea / GitLab 集成区）
  /** 列出 workspace 已配置的 VCS 连接。 */
  async listVCSConnections(workspaceId: string): Promise<ListVCSConnectionsResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/vcs/connections`);
  }

  /** 新建（连接）一个 VCS 连接。 */
  async connectVCS(
    workspaceId: string,
    body: ConnectVCSRequest,
  ): Promise<ConnectVCSResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/vcs/connections`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  /** 删除 VCS 连接。 */
  async deleteVCSConnection(workspaceId: string, connectionId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/vcs/connections/${connectionId}`, {
      method: "DELETE",
    });
  }

  /** 轮换 VCS 连接的 webhook 密钥。 */
  async rotateVCSWebhook(
    workspaceId: string,
    connectionId: string,
  ): Promise<ConnectVCSResponse> {
    return this.fetch(
      `/api/workspaces/${workspaceId}/vcs/connections/${connectionId}/rotate-webhook`,
      { method: "POST" },
    );
  }

  // Lark / 飞书集成区
  /** 列出 workspace 的 Lark/飞书安装。 */
  async listLarkInstallations(workspaceId: string): Promise<ListLarkInstallationsResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/lark/installations`);
  }

  /** 发起一次 Lark/飞书绑定安装（设备码流程的 begin 步，用于展示二维码）。 */
  async beginLarkInstall(
    workspaceId: string,
    agentId: string,
    region: "feishu" | "lark",
  ): Promise<BeginLarkInstallResponse> {
    // 用户在 UI 里显式选择云环境（「绑定到飞书」还是「绑定到 Lark」），后端会
    // 对相应的账号主机（accounts.feishu.cn 还是 accounts.larksuite.com）发起
    // 设备码流程的 `begin`，让二维码一开始就渲染在正确的云环境上。region 为空/
    // 省略时服务端仍默认飞书（RegionOrDefault）——这里把 region 设为必填参数，
    // 强制每个调用点做出明确选择，而不是悄悄默认到国内版。
    const search = new URLSearchParams({ agent_id: agentId, region });
    return this.fetch(`/api/workspaces/${workspaceId}/lark/install/begin?${search.toString()}`, {
      method: "POST",
    });
  }

  /** 查询一次 Lark/飞书绑定安装会话的进行状态。 */
  async getLarkInstallStatus(workspaceId: string, sessionId: string): Promise<LarkInstallStatusResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/lark/install/${sessionId}/status`);
  }

  /** 解绑（删除）一个 Lark/飞书安装。 */
  async deleteLarkInstallation(workspaceId: string, installationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/lark/installations/${installationId}`, {
      method: "DELETE",
    });
  }

  /** 用绑定 token 兑换当前登录用户与 Lark/飞书账号的绑定关系。 */
  async redeemLarkBindingToken(token: string): Promise<RedeemLarkBindingTokenResponse> {
    return this.fetch(`/api/lark/binding/redeem`, {
      method: "POST",
      body: JSON.stringify({ token }),
    });
  }

  // Composio 集成（MUL-3720）。所有路由按用户隔离（连接属于用户而非
  // workspace），因此都不接收 workspaceId。

  /** 项目可连接的 Composio 工具包（启用过鉴权配置的那些）。自 MUL-4009 起
   *  后端会过滤掉不可连接的 toolkit，因此每个条目都是 `connectable: true`。
   *  解析/上游失败返回 502 而非空列表。 */
  async listComposioToolkits(): Promise<ComposioToolkit[]> {
    return this.fetch(`/api/integrations/composio/toolkits`);
  }

  /** 当前调用者活跃的 Composio 连接列表。 */
  async listComposioConnections(): Promise<ComposioConnection[]> {
    return this.fetch(`/api/integrations/composio/connections`);
  }

  /** 为某个 toolkit 启动托管式 Composio 连接流程，返回浏览器应跳转的
   *  重定向 URL。 */
  async beginComposioConnect(toolkitSlug: string): Promise<ComposioConnectInitResponse> {
    return this.fetch(`/api/integrations/composio/connect/init`, {
      method: "POST",
      body: JSON.stringify({ toolkit_slug: toolkitSlug }),
    });
  }

  /** 断开调用者拥有的一个 Composio 连接。 */
  async deleteComposioConnection(connectionId: string): Promise<void> {
    await this.fetch(`/api/integrations/composio/connections/${connectionId}`, {
      method: "DELETE",
    });
  }

  // Slack 集成（MUL-3666）
  /** 列出 workspace 的 Slack 安装。 */
  async listSlackInstallations(workspaceId: string): Promise<ListSlackInstallationsResponse> {
    return this.fetch(`/api/workspaces/${workspaceId}/slack/installations`);
  }

  // registerSlackBYO 执行「自带应用」安装：管理员粘贴其创建的 Slack 应用的
  // bot token（xoxb-）与应用级 token（xapp-），后端校验并持久化，返回新安装。
  async registerSlackBYO(
    workspaceId: string,
    agentId: string,
    body: RegisterSlackBYORequest,
  ): Promise<SlackInstallation> {
    const search = new URLSearchParams({ agent_id: agentId });
    return this.fetch(`/api/workspaces/${workspaceId}/slack/install/byo?${search.toString()}`, {
      method: "POST",
      body: JSON.stringify(body),
    });
  }

  /** 删除 Slack 安装（解绑）。 */
  async deleteSlackInstallation(workspaceId: string, installationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/slack/installations/${installationId}`, {
      method: "DELETE",
    });
  }

  /** 用绑定 token 把当前登录用户与 Slack 账号绑定起来。 */
  async redeemSlackBindingToken(token: string): Promise<RedeemSlackBindingTokenResponse> {
    return this.fetch(`/api/slack/binding/redeem`, {
      method: "POST",
      body: JSON.stringify({ token }),
    });
  }

  // DingTalk / 钉钉集成区
  /** 列出 workspace 的钉钉安装。 */
  async listDingTalkInstallations(
    workspaceId: string,
  ): Promise<ListDingTalkInstallationsResponse> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/dingtalk/installations`);
    return parseWithFallback(
      raw,
      ListDingTalkInstallationsResponseSchema,
      EMPTY_LIST_DINGTALK_INSTALLATIONS_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/dingtalk/installations" },
    );
  }

  /** 列出 workspace 钉钉可用的群（供机器人加入/转发）。 */
  async listDingTalkGroups(
    workspaceId: string,
    params: ListDingTalkGroupsParams = {},
  ): Promise<ListDingTalkGroupsResponse> {
    let raw: unknown;
    try {
      const search = dingTalkGroupSearch(params);
      raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/dingtalk/groups${search}`);
    } catch (error) {
      // 已安装的客户端可能比它们重新连上的服务器新。早于群发现特性的后端会
      // 对该增量端点返回 404；最初仅管理员的版本对成员返回 403。两种情况都
      // 表示本客户端用不了该 workspace 的群清单，而真实的故障仍然保持可见。
      if (
        error instanceof ApiError &&
        (error.status === 404 || error.status === 403)
      ) {
        return EMPTY_LIST_DINGTALK_GROUPS_RESPONSE;
      }
      throw error;
    }
    return parseWithFallback(
      raw,
      ListDingTalkGroupsResponseSchema,
      EMPTY_LIST_DINGTALK_GROUPS_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/dingtalk/groups" },
    );
  }

  /** 列出某 agent 可用的钉钉群。 */
  async listAgentDingTalkGroups(
    agentId: string,
    params: ListDingTalkGroupsParams = {},
  ): Promise<ListDingTalkGroupsResponse> {
    let raw: unknown;
    try {
      const search = dingTalkGroupSearch(params);
      raw = await this.fetch<unknown>(`/api/agents/${agentId}/dingtalk/groups${search}`);
    } catch (error) {
      // 保持已安装客户端与早于「按 agent 的群发现」特性的服务器兼容。
      // 授权失败仍须保持可见。
      if (error instanceof ApiError && error.status === 404) {
        return EMPTY_LIST_DINGTALK_GROUPS_RESPONSE;
      }
      throw error;
    }
    return parseWithFallback(
      raw,
      ListDingTalkGroupsResponseSchema,
      EMPTY_LIST_DINGTALK_GROUPS_RESPONSE,
      { endpoint: "GET /api/agents/:id/dingtalk/groups" },
    );
  }

  /** 让某钉钉安装「遗忘」一个群（解除关联）。 */
  async forgetDingTalkGroup(
    workspaceId: string,
    installationId: string,
    conversationId: string,
  ): Promise<void> {
    await this.fetch(
      `/api/workspaces/${workspaceId}/dingtalk/installations/${installationId}/groups/${encodeURIComponent(conversationId)}`,
      { method: "DELETE" },
    );
  }

  // registerDingTalkBYO 执行「自带应用」安装：agent 拥有者或 workspace
  // owner/admin 粘贴其创建的钉钉 Stream 模式机器人的 AppKey（client id）+
  // AppSecret（client secret），后端校验并持久化，返回新安装。
  async registerDingTalkBYO(
    workspaceId: string,
    agentId: string,
    body: RegisterDingTalkBYORequest,
  ): Promise<DingTalkInstallation> {
    const search = new URLSearchParams({ agent_id: agentId });
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/dingtalk/install/byo?${search.toString()}`,
      {
        method: "POST",
        body: JSON.stringify(body),
      },
    );
    return parseWithFallback(raw, DingTalkInstallationSchema, EMPTY_DINGTALK_INSTALLATION, {
      endpoint: "POST /api/workspaces/:id/dingtalk/install/byo",
    });
  }

  /** 删除钉钉安装（解绑）。 */
  async deleteDingTalkInstallation(workspaceId: string, installationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/dingtalk/installations/${installationId}`, {
      method: "DELETE",
    });
  }

  /** 用绑定 token 把当前登录用户与钉钉账号绑定。 */
  async redeemDingTalkBindingToken(
    token: string,
  ): Promise<RedeemDingTalkBindingTokenResponse> {
    const raw = await this.fetch<unknown>(`/api/dingtalk/binding/redeem`, {
      method: "POST",
      body: JSON.stringify({ token }),
    });
    return parseWithFallback(
      raw,
      RedeemDingTalkBindingTokenResponseSchema,
      EMPTY_REDEEM_DINGTALK_BINDING_TOKEN_RESPONSE,
      { endpoint: "POST /api/dingtalk/binding/redeem" },
    );
  }
  // 企业微信智能机器人（aibot）集成。机器人拨一条 WebSocket 长连接到
  // wss://openws.work.weixin.qq.com 并以 (bot_id, secret) 保持鉴权，无需公开
  // 回调地址。下面这几个方法只驱动设置页的 BYO 连接对话框 + 列表 + 断开——
  // 入站 WebSocket 循环完全运行在服务端。
  /** 列出 workspace 的企业微信安装。 */
  async listWecomInstallations(workspaceId: string): Promise<ListWecomInstallationsResponse> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/wecom/installations`);
    return parseWithFallback(
      raw,
      ListWecomInstallationsResponseSchema,
      EMPTY_LIST_WECOM_INSTALLATIONS_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/wecom/installations" },
    );
  }

  // registerWecomBYO 执行「自带应用」安装：管理员粘贴企业微信管理后台的
  // 机器人 id 与长连接密钥，后端在持久化前用 MULTICA_WECOM_SECRET_KEY 封存
  // 密钥，返回新安装。
  /** 自带应用方式注册企业微信机器人。 */
  async registerWecomBYO(
    workspaceId: string,
    agentId: string,
    body: RegisterWecomBYORequest,
  ): Promise<WecomInstallation> {
    const search = new URLSearchParams({ agent_id: agentId });
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/wecom/install/byo?${search.toString()}`,
      {
        method: "POST",
        body: JSON.stringify(body),
      },
    );
    return parseWithFallback(raw, WecomInstallationSchema, EMPTY_WECOM_INSTALLATION, {
      endpoint: "POST /api/workspaces/:id/wecom/install/byo",
    });
  }

  /** 删除企业微信安装（解绑）。 */
  async deleteWecomInstallation(workspaceId: string, installationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/wecom/installations/${installationId}`, {
      method: "DELETE",
    });
  }

  // redeemWecomBindingToken 把 token 携带的企业微信机器人 userid 绑定到当前
  // 登录的 Multica 用户。由 /wecom/bind 兑换页在用户点过机器人在企业微信里
  // 发来的「关联你的 Multica 账号」提示后调用。状态码：
  //   410 Gone      → 无效 / 过期 / 已消费
  //   409 Conflict  → 该企业微信用户已绑定到别的用户
  //   403 Forbidden → 当前登录用户不是 workspace 成员
  async redeemWecomBindingToken(token: string): Promise<RedeemWecomBindingTokenResponse> {
    const raw = await this.fetch<unknown>(`/api/wecom/binding/redeem`, {
      method: "POST",
      body: JSON.stringify({ token }),
    });
    return parseWithFallback(
      raw,
      RedeemWecomBindingTokenResponseSchema,
      EMPTY_REDEEM_WECOM_BINDING_TOKEN_RESPONSE,
      { endpoint: "POST /api/wecom/binding/redeem" },
    );
  }

  // Telegram 集成区
  /** 列出 workspace 的 Telegram 安装。 */
  async listTelegramInstallations(
    workspaceId: string,
  ): Promise<ListTelegramInstallationsResponse> {
    const raw = await this.fetch<unknown>(`/api/workspaces/${workspaceId}/telegram/installations`);
    return parseWithFallback(
      raw,
      ListTelegramInstallationsResponseSchema,
      EMPTY_LIST_TELEGRAM_INSTALLATIONS_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/telegram/installations" },
    );
  }

  /** 注册（安装）一个 Telegram 机器人绑定到 agent。 */
  async registerTelegramBot(
    workspaceId: string,
    agentId: string,
    body: RegisterTelegramRequest,
  ): Promise<TelegramInstallation> {
    const search = new URLSearchParams({ agent_id: agentId });
    const raw = await this.fetch<unknown>(
      `/api/workspaces/${workspaceId}/telegram/install?${search.toString()}`,
      {
        method: "POST",
        body: JSON.stringify(body),
      },
    );
    return parseWithFallback(raw, TelegramInstallationSchema, EMPTY_TELEGRAM_INSTALLATION, {
      endpoint: "POST /api/workspaces/:id/telegram/install",
    });
  }

  /** 删除 Telegram 安装（解绑）。 */
  async deleteTelegramInstallation(workspaceId: string, installationId: string): Promise<void> {
    await this.fetch(`/api/workspaces/${workspaceId}/telegram/installations/${installationId}`, {
      method: "DELETE",
    });
  }

  /** 用绑定 token 把当前登录用户与 Telegram 账号绑定。 */
  async redeemTelegramBindingToken(token: string): Promise<RedeemTelegramBindingTokenResponse> {
    const raw = await this.fetch<unknown>(`/api/telegram/binding/redeem`, {
      method: "POST",
      body: JSON.stringify({ token }),
    });
    return parseWithFallback(
      raw,
      RedeemTelegramBindingTokenResponseSchema,
      EMPTY_REDEEM_TELEGRAM_BINDING_TOKEN_RESPONSE,
      { endpoint: "POST /api/telegram/binding/redeem" },
    );
  }
}
