// ===========================================================================
// 文件职责：types 目录的"总出口"（barrel）。本文件不做任何定义，只把各
// 领域文件的类型与少量常量/纯函数统一 re-export，让外部（packages/core 的
// 其它模块、apps/web、packages/views 等）可以一条 import 全取：
//
//   import type { Issue, Agent, ChatSession } from "@multica/core/types";
//
// 分组说明（与源文件一一对应）：
//   - issue-execution：issue 的执行树 / 时间线节点
//   - issue / issue-status：议题本体与状态目录
//   - agent：Agent 实体、调用权限、运行时、任务、用量、技能（最大的一块）
//   - workspace / project / squad / label / property / pin：组织与元数据
//   - comment / activity / subscriber / inbox：协作与通知
//   - chat / autopilot / quick-action：对话与自动化
//   - events / api：WS 实时事件与后端接口签名
//   - attachment / attachment-url：附件及下载地址工具函数
//   - plugin / github / vcs / lark / slack / dingtalk / wecom / telegram /
//     composio / external-credential：插件与外部集成
//   - storage：存储适配器接口；life：生活助手
//
// 维护约定：每新增一个源文件，记得在这里补一行 re-export，并保持导出名
// 大写字母开头（type）与常量函数的区分；纯类型用 `export type`，运行时可
// 用的值/函数用 `export`。
// ===========================================================================

export type {
  AgentTaskArtifact,
  IssueExecutionNode,
  IssueExecutionTreeResponse,
  IssueTimelineNode,
  IssueTimelineSummary,
  TaskTraceEvent,
  ToolCallChain,
} from "./issue-execution";
export type { Issue, IssueStatus, IssuePriority, IssueAssigneeType, IssueMetadata, IssueMetadataValue, IssueReaction, SourceContextAttachment, SourceContextAuthor, SourceContextIssueSnapshot, SourceContextCommentSnapshot, SourceContextSnapshot, SourceContextLimitUsage, SourceContextPreview, SourceContextAuthorState, IssueSourceContext } from "./issue";
export type {
  IssueStatusCategory,
  IssueStatusEntry,
  ListIssueStatusesResponse,
  CreateIssueStatusRequest,
  UpdateIssueStatusRequest,
} from "./issue-status";
export type {
  Agent,
  AgentConversationStarter,
  AgentStatus,
  AgentRuntimeMode,
  AgentVisibility,
  AgentPermissionMode,
  AgentInvocationTarget,
  AgentInvocationTargetInput,
  AgentTask,
  TaskUsage,
  TaskAttribution,
  AttributionUser,
  TaskEvidence,
  AgentActivityBucket,
  AgentRunCount,
  WorkspaceWorkingAgent,
  WorkspaceWorkingAgentType,
  WorkspaceWorkingAgentMineRelation,
  TaskFailureReason,
  AgentRuntime,
  RuntimeDevice,
  RuntimeProfile,
  RuntimeProtocolFamily,
  RuntimeProfileVisibility,
  CreateRuntimeProfileRequest,
  UpdateRuntimeProfileRequest,
  CreateAgentRequest,
  AgentBuilderRuntimeSwitch,
  AgentBuilderSession,
  AgentBuilderSessionSummary,
  AgentPermissionScope,
  StoredAgentDraft,
  UpdateAgentRequest,
  AgentEnvResponse,
  UpdateAgentEnvRequest,
  Skill,
  SkillSummary,
  AgentSkillSummary,
  DisabledRuntimeSkill,
  SetAgentRuntimeSkillEnabledRequest,
  SkillFile,
  CreateSkillRequest,
  SkillImportResult,
  UpdateSkillRequest,
  SetAgentSkillsRequest,
  RuntimeUsage,
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
  RuntimeUpdateStatus,
  RuntimeModel,
  RuntimeModelServiceTier,
  RuntimeModelThinking,
  RuntimeModelThinkingLevel,
  RuntimeModelListRequest,
  RuntimeModelListStatus,
  RuntimeModelsResult,
  RuntimeLocalSkillStatus,
  RuntimeLocalSkillImportAction,
  RuntimeLocalSkillImportConflict,
  RuntimeLocalSkillSummary,
	RuntimeLocalMcpServerSummary,
  RuntimeLocalSkillListRequest,
  CreateRuntimeLocalSkillImportRequest,
  RuntimeLocalSkillImportRequest,
  RuntimeLocalSkillsResult,
  RuntimeLocalSkillImportResult,
  IssueUsageSummary,
  MikaBootstrapResponse,
} from "./agent";
export { RUNTIME_PROFILE_PROTOCOL_FAMILIES } from "./agent";
export { normalizeWorkspaceRepoProvider } from "./workspace";
export type { Workspace, WorkspaceRepo, WorkspaceRepoProvider, WorkspaceRepoProbeResponse, WorkspaceMcpServer, Member, MemberRole, User, MemberWithUser, Invitation, ShareLink, ShareLinkInfo } from "./workspace";
export type {
  PluginInstallation,
  PluginConfigField,
  PluginConfigFieldType,
  PluginSurface,
  PluginSurfaceType,
  PluginHook,
  PluginHookTrigger,
  PluginResource,
  PluginInstallationListResponse,
  PluginManifestSummary,
  PluginPackage,
  PluginPackageVersion,
  PluginPackageListResponse,
  PluginSurfaceLaunch,
  PluginPreview,
  PluginPreviewRequest,
  PluginInstallRequest,
  PluginConfigRequest,
  PluginHookResult,
  PluginInvocation,
  PluginMCPTool,
  PluginTokenIssue,
} from "./plugin";
export type { InboxItem, InboxSeverity, InboxItemType, InboxWorkspaceUnread } from "./inbox";
export type { NotificationGroupKey, NotificationGroupValue, NotificationPreferences, NotificationPreferenceResponse } from "./notification-preference";
export type { Comment, CommentType, CommentAuthorType, CommentTriggerPreview, CommentTriggerPreviewAgent, CommentTriggerSource, CommentTriggerOutcome, CommentTriggerStatus, Reaction } from "./comment";
export type { Label, LabelResourceType, CreateLabelRequest, UpdateLabelRequest, ListLabelsResponse, IssueLabelsResponse, ResourceLabelsResponse } from "./label";
export type { IssueProperty, IssuePropertyType, ScalarIssuePropertyType, IssuePropertyOption, IssuePropertyConfig, IssuePropertyValue, IssuePropertyValues, CreatePropertyRequest, UpdatePropertyRequest, ListPropertiesResponse, IssuePropertiesResponse, IssuePropertyActorKind, IssuePropertyActorRef, PropertyFilterOp, PropertyOperatorFilter, PropertyFilterValue } from "./property";
export { ISSUE_PROPERTY_TYPES, isKnownPropertyType, ISSUE_PROPERTY_ACTOR_KINDS, MAX_ISSUE_PROPERTY_ACTOR_VALUES, isActorPropertyType, isFilterablePropertyType, isScalarPropertyType, formatActorRef, parseActorRef, actorRefsFromValue, actorRefValuesFromValue, hasUnknownActorRef, isPropertyOperatorFilter, isKnownPropertyFilterOp, propertyFilterValueKey, PROPERTY_FILTER_OP_SYMBOLS, PROPERTY_FILTER_OPS_BY_TYPE } from "./property";
export type {
  QuickAction,
  QuickActionVisibility,
  QuickActionAssigneeType,
  QuickActionStatus,
  CreateQuickActionRequest,
  UpdateQuickActionRequest,
  ListQuickActionsResponse,
} from "./quick-action";
export {
  QUICK_ACTION_SIDEBAR_LIMIT,
  QUICK_ACTION_TEMPLATE_TOKEN_RE,
  findQuickActionTemplateToken,
} from "./quick-action";
export type {
  TimelineEntry,
  AssigneeFrequencyEntry,
} from "./activity";
export type { IssueSubscriber } from "./subscriber";
export type * from "./events";
export type * from "./api";
export type { Attachment } from "./attachment";
export {
  attachmentDownloadPath,
  attachmentIdFromDownloadURL,
  contentReferencesAttachment,
  stripChannelMediaMarkers,
} from "./attachment-url";
export type {
  ChatSession,
  ChatLastMessage,
  ChatPinnedAgent,
  ChatMessage,
  ChatQuickAction,
  ChatQuickActionsPendingState,
  ChatQuickActionsFailureState,
  ChatMessagesPage,
  ChatPendingTask,
  ChatQueuedTask,
  PrioritizeQueuedChatTaskResponse,
  PendingChatTaskItem,
  PendingChatTasksResponse,
  HasPendingChatTasksResponse,
  SendChatMessageResponse,
  StartMikaOnboardingResponse,
  CancelledChatMessage,
  CancelTaskResponse,
  ChatDraftRestore,
  ChatDraftRestoresResponse,
} from "./chat";
export type { StorageAdapter } from "./storage";
export type * from "./life";
export type {
  Project,
  ProjectStatus,
  ProjectPriority,
  CreateProjectRequest,
  UpdateProjectRequest,
  ListProjectsResponse,
  ProjectResource,
  GongfengRepoResourceRef,
  ProjectResourceType,
  ProjectResourceRef,
  GithubRepoResourceRef,
  LocalDirectoryResourceRef,
  LocalDirectoryExecutionMode,
  CreateProjectResourceRequest,
  UpdateProjectResourceRequest,
  ListProjectResourcesResponse,
} from "./project";
export type { PinnedItem, PinnedItemType, CreatePinRequest, ReorderPinsRequest } from "./pin";
export type {
  GitHubInstallation,
  GitHubMergeableState,
  GitHubPullRequest,
  GitHubPullRequestChecksConclusion,
  GitHubPullRequestChecksRollup,
  GitHubPullRequestMergeable,
  GitHubPullRequestMergeStateStatus,
  GitHubPullRequestState,
  ListGitHubInstallationsResponse,
  GitHubRepository,
  ListGitHubRepositoriesResponse,
  GitHubConnectResponse,
} from "./github";
export type {
  VCSProvider,
  VCSConnection,
  ListVCSConnectionsResponse,
  ConnectVCSRequest,
  ConnectVCSResponse,
} from "./vcs";
export type {
  LarkInstallation,
  ListLarkInstallationsResponse,
  BeginLarkInstallResponse,
  LarkInstallStatusResponse,
  RedeemLarkBindingTokenResponse,
} from "./lark";
export type {
  ComposioToolkit,
  ComposioConnection,
  ComposioConnectInitResponse,
} from "./composio";
export type {
  SlackInstallation,
  ListSlackInstallationsResponse,
  RegisterSlackBYORequest,
  RedeemSlackBindingTokenResponse,
} from "./slack";
export type {
  DingTalkInstallation,
  ListDingTalkInstallationsResponse,
  DingTalkGroupBot,
  DingTalkGroup,
  ListDingTalkGroupsResponse,
  ListDingTalkGroupsParams,
  RegisterDingTalkBYORequest,
  RedeemDingTalkBindingTokenResponse,
} from "./dingtalk";
export type {
  WecomInstallation,
  ListWecomInstallationsResponse,
  RegisterWecomBYORequest,
  RedeemWecomBindingTokenResponse,
} from "./wecom";
export type {
  TelegramInstallation,
  ListTelegramInstallationsResponse,
  RegisterTelegramRequest,
  RedeemTelegramBindingTokenResponse,
} from "./telegram";
export type {
  Autopilot,
  AutopilotStatus,
  AutopilotExecutionMode,
  AutopilotAssigneeType,
  AutopilotSubscriber,
  AutopilotSubscriberInput,
  AutopilotCollaborator,
  AutopilotCollaboratorsResponse,
  AutopilotTrigger,
  AutopilotTriggerKind,
  AutopilotRun,
  AutopilotRunStatus,
  AutopilotRunSource,
  WebhookEventFilter,
  CreateAutopilotRequest,
  UpdateAutopilotRequest,
  CreateAutopilotTriggerRequest,
  UpdateAutopilotTriggerRequest,
  ListAutopilotsResponse,
  CronPreviewResponse,
  GetAutopilotResponse,
  ListAutopilotRunsResponse,
  WebhookDelivery,
  WebhookDeliveryStatus,
  WebhookSignatureStatus,
  ListWebhookDeliveriesResponse,
} from "./autopilot";
export type {
  Squad,
  SquadMember,
  SquadMemberType,
  SquadMemberPreview,
  SquadActivityLog,
  SquadActivityOutcome,
  CreateSquadRequest,
  UpdateSquadRequest,
  AddSquadMemberRequest,
  RemoveSquadMemberRequest,
  UpdateSquadMemberRoleRequest,
  CreateSquadActivityLogRequest,
  SquadMemberStatusValue,
  SquadActiveIssueBrief,
  SquadMemberStatus,
  SquadMemberStatusListResponse,
} from "./squad";
export type { ExternalCredentialProvider, ExternalCredentialProfile, CreateExternalCredentialProfileRequest, UpdateExternalCredentialProfileRequest, TestExternalCredentialProfileRequest, TestExternalCredentialProfileResponse } from "./external-credential";
