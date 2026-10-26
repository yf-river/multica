// 本文件定义各 API 端点响应的 zod 校验 schema，以及解析失败时使用的空值兜底常量（EMPTY_*）。
import { z } from "zod";
import type {
  AgentBuilderRuntimeSwitch,
  AgentBuilderSession,
  AgentBuilderSessionSummary,
  Attachment,
  AutopilotRun,
  CancelTaskResponse,
  ChatMessage,
  ChatDraftRestoresResponse,
  ChatPendingTask,
  ChatSession,
  PrioritizeQueuedChatTaskResponse,
  SendChatMessageResponse,
  Comment,
  CronPreviewResponse,
  DingTalkInstallation,
  ListDingTalkInstallationsResponse,
  ListDingTalkGroupsResponse,
  RedeemDingTalkBindingTokenResponse,
  WecomInstallation,
  ListWecomInstallationsResponse,
  RedeemWecomBindingTokenResponse,
  TelegramInstallation,
  ListTelegramInstallationsResponse,
  RedeemTelegramBindingTokenResponse,
  GroupedIssuesResponse,
  GitHubConnectResponse,
  GitHubPullRequest,
  InboxItem,
  InboxWorkspaceUnread,
  Label,
  MemberWithUser,
  IssueProperty,
  ListPropertiesResponse,
  QuickAction,
  ListQuickActionsResponse,
  IssuePropertiesResponse,
  IssueTableGroupDescriptor,
  IssueTableFacetsResponse,
  IssueTableGroupsResponse,
  IssueTableRowsResponse,
  ListIssuesResponse,
  ListGitHubInstallationsResponse,
  ListGitHubRepositoriesResponse,
  ListLabelsResponse,
  ListWebhookDeliveriesResponse,
  IssueStatusEntry,
  ListIssueStatusesResponse,
  NotificationPreferenceResponse,
  PluginInstallation,
  PluginInstallationListResponse,
  PluginPackage,
  PluginPackageListResponse,
  PluginPreview,
  PluginSurfaceLaunch,
  ResourceLabelsResponse,
  RuntimeModelListRequest,
  SearchIssuesResponse,
  SearchProjectsResponse,
  ShareLink,
  ShareLinkInfo,
  Skill,
  SkillImportResult,
  Squad,
  TimelineEntry,
  User,
  WebhookDelivery,
  WorkspaceRepo,
  WorkspaceRepoProbeResponse,
  WorkspaceMcpServer,
} from "../types";
import type { CloudRuntimeNode } from "../runtimes/cloud-runtime";

/** 校验插件配置字段定义（key / type / label / 是否必填 / 枚举选项等）。 */
export const PluginConfigFieldSchema = z.object({
  key: z.string(),
  type: z.string().default("string"),
  label: z.string().default(""),
  description: z.string().optional(),
  required: z.boolean().default(false),
  options: z.array(z.string()).default([]),
  placeholder: z.string().optional(),
  multiline: z.boolean().default(false),
}).loose();

/** 校验插件界面（Surface）定义：key / type / name / entry / 支持的平台。 */
export const PluginSurfaceSchema = z.object({
  key: z.string(),
  type: z.string().default(""),
  name: z.string().default(""),
  entry: z.string().default(""),
  platforms: z.array(z.string()).default([]),
}).loose();

/** 校验插件钩子（Hook）定义：key / name / 触发类型 / 事件 / 调度 / 传输方式。 */
export const PluginHookSchema = z.object({
  key: z.string(),
  name: z.string().default(""),
  description: z.string().default(""),
  triggers: z.array(z.string()).default([]),
  events: z.array(z.string()).default([]),
  schedule: z.object({
    cron: z.string().default(""),
    timezone: z.string().default(""),
    next_run_at: z.string().optional(),
  }).loose().optional(),
  transport: z.string().default(""),
}).loose();

/** 校验插件静态资源（Resource）定义：type / key / entry。 */
export const PluginResourceSchema = z.object({
  type: z.string().default(""),
  key: z.string(),
  entry: z.string().default(""),
}).loose();

/** 校验一个插件安装实例：标识、版本、启用状态、授权 scope、配置、界面、钩子与资源。 */
export const PluginInstallationSchema = z.object({
  id: z.string(),
  plugin_key: z.string().default(""),
  name: z.string().default(""),
  description: z.string().optional(),
  version: z.string().default(""),
  package_version_id: z.string().default(""),
  enabled: z.boolean().default(false),
  granted_scopes: z.array(z.string()).default([]),
  config_schema: z.array(PluginConfigFieldSchema).default([]),
  config: z.record(z.string(), z.unknown()).default({}),
  configured_secrets: z.array(z.string()).default([]),
  surfaces: z.array(PluginSurfaceSchema).default([]),
  hooks: z.array(PluginHookSchema).default([]),
  resources: z.array(PluginResourceSchema).default([]),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

/** 插件安装实例的空值兜底（校验失败时返回）。 */
export const EMPTY_PLUGIN_INSTALLATION: PluginInstallation = {
  id: "",
  plugin_key: "",
  name: "",
  version: "",
  package_version_id: "",
  enabled: false,
  granted_scopes: [],
  config_schema: [],
  config: {},
  configured_secrets: [],
  surfaces: [],
  hooks: [],
  resources: [],
  created_at: "",
  updated_at: "",
};

/** 校验插件安装列表响应。 */
export const PluginInstallationListResponseSchema = z.object({
  plugins: z.array(PluginInstallationSchema).default([]),
}).loose();

/** 插件安装列表的空值兜底。 */
export const EMPTY_PLUGIN_INSTALLATION_LIST: PluginInstallationListResponse = {
  plugins: [],
};

/**
 * 校验一次已完成的钩子调用结果。`status` 是宿主（host）的分类，不是端点的：
 * "refused" 表示我们根本没发出这次调用，这对阅读者来说是和「端点应答失败」完全不同的问题。
 */
export const PluginHookResultSchema = z.object({
  status: z.string().default("ok"),
  output: z.unknown().optional(),
  error: z.string().optional(),
  latency_ms: z.number().default(0),
  hook_key: z.string().default(""),
  trigger: z.string().default(""),
  attempts: z.number().default(1),
}).loose();

/** 校验一条已记录的钩子调用记录（运维遥测，不含请求/响应正文）。 */
export const PluginInvocationSchema = z.object({
  id: z.string().default(""),
  hook_key: z.string().default(""),
  trigger: z.string().default(""),
  status: z.string().default(""),
  event_type: z.string().optional(),
  attempt: z.number().default(1),
  latency_ms: z.number().default(0),
  error: z.string().optional(),
  delivery_id: z.string().optional(),
  planned_at: z.string().optional(),
  created_at: z.string().default(""),
}).loose();

/** 校验钩子调用记录列表响应。 */
export const PluginInvocationListSchema = z.object({
  invocations: z.array(PluginInvocationSchema).default([]),
}).loose();

/**
 * 校验插件 token 签发结果。只在签发它的那次请求中返回一次；两个值都没有读取端点，
 * 所以客户端一旦丢弃就无法找回。
 */
export const PluginTokenIssueSchema = z.object({
  token: z.string().default(""),
  signing_secret: z.string().default(""),
}).loose();

/**
 * 校验某个 `mcp` 传输钩子当前暴露的工具。
 *
 * 这里的默认值方向很关键：无法解析的响应会得到空列表且无任何已批准项，
 * 这样漂移的后端不会让 UI 把一个工具渲染成「已批准」。
 */
export const PluginMCPToolSchema = z.object({
  name: z.string().default(""),
  description: z.string().default(""),
  schema_digest: z.string().default(""),
  approved: z.boolean().default(false),
  drifted: z.boolean().default(false),
}).loose();

/** 校验 MCP 工具列表响应。 */
export const PluginMCPToolListSchema = z.object({
  tools: z.array(PluginMCPToolSchema).default([]),
}).loose();

/** 校验插件 manifest 摘要（key / name / version / 作者 / 贡献的钩子）。 */
export const PluginManifestSummarySchema = z.object({
  key: z.string().default(""),
  name: z.string().default(""),
  description: z.string().optional(),
  version: z.string().default(""),
  author: z.object({
    name: z.string().default(""),
    url: z.string().optional(),
  }).loose().default({ name: "" }),
  contributes: z.object({
    hooks: z.array(z.object({
      key: z.string().default(""),
      name: z.string().default(""),
      triggers: z.array(z.string()).default([]),
      schedule: z.object({
        cron: z.string().default(""),
        timezone: z.string().default(""),
      }).loose().optional(),
    }).loose()).default([]),
  }).loose().optional(),
}).loose();

/** 校验插件安装预览（同意界面展示的内容：manifest、scope、配置 schema、版本摘要）。 */
export const PluginPreviewSchema = z.object({
  manifest: PluginManifestSummarySchema,
  scopes: z.array(z.string()).default([]),
  config_schema: z.array(PluginConfigFieldSchema).default([]),
  version_id: z.string().default(""),
  version: z.string().default(""),
  digest: z.string().default(""),
  installed: z.boolean().default(false),
  installed_version: z.string().optional(),
  added_scopes: z.array(z.string()).default([]),
}).loose();

/** 插件预览的空值兜底。 */
export const EMPTY_PLUGIN_PREVIEW: PluginPreview = {
  manifest: { key: "", name: "", version: "", author: { name: "" } },
  scopes: [],
  config_schema: [],
  version_id: "",
  version: "",
  digest: "",
  installed: false,
  added_scopes: [],
};

/**
 * 校验一个已发布版本。`installed` 是设置页用来回答「我当前用的是哪个」的标记；
 * 默认 false，这样畸形响应永远不会声称某个版本正在运行而实际并非如此。
 */
export const PluginPackageVersionSchema = z.object({
  id: z.string().default(""),
  version: z.string().default(""),
  digest: z.string().default(""),
  size_bytes: z.number().default(0),
  published_at: z.string().default(""),
  installed: z.boolean().default(false),
}).loose();

/** 校验一个已发布进工作区的插件包（含其所有版本）。 */
export const PluginPackageSchema = z.object({
  id: z.string().default(""),
  plugin_key: z.string().default(""),
  name: z.string().default(""),
  versions: z.array(PluginPackageVersionSchema).default([]),
  created_at: z.string().default(""),
}).loose();

/** 校验插件包列表响应。 */
export const PluginPackageListResponseSchema = z.object({
  packages: z.array(PluginPackageSchema).default([]),
}).loose();

/** 插件包列表的空值兜底。 */
export const EMPTY_PLUGIN_PACKAGE_LIST: PluginPackageListResponse = {
  packages: [],
};

/** 单个插件包的空值兜底。 */
export const EMPTY_PLUGIN_PACKAGE: PluginPackage = {
  id: "",
  plugin_key: "",
  name: "",
  versions: [],
  created_at: "",
};

/** 校验一次托管界面启动：畸形启动降级为「不可用」，绝不成为「半可信」的框架。 */
export const PluginSurfaceLaunchSchema = z.object({
  url: z.string().default(""),
  bridge_token: z.string().default(""),
  version: z.string().default(""),
  digest: z.string().default(""),
}).loose();

/** 插件界面启动的空值兜底。 */
export const EMPTY_PLUGIN_SURFACE_LAUNCH: PluginSurfaceLaunch = {
  url: "",
  bridge_token: "",
  version: "",
  digest: "",
};

/** 校验一个 GitHub 安装实例（账号登录名、类型、头像、安装时间等）。 */
export const GitHubInstallationSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  installation_id: z.number().optional(),
  account_login: z.string(),
  account_type: z.string(),
  account_avatar_url: z.string().nullable(),
  created_at: z.string(),
  connected_by: z.string().optional(),
}).loose();

/** 校验 GitHub 安装列表响应（含 configured / repository_browse_configured / can_manage 三个能力标记）。 */
export const ListGitHubInstallationsResponseSchema = z.object({
  installations: z.array(GitHubInstallationSchema).default([]),
  configured: z.boolean().optional().default(false),
  repository_browse_configured: z.boolean().optional().default(false),
  can_manage: z.boolean().optional().default(false),
}).loose();

/** GitHub 安装列表的空值兜底。 */
export const EMPTY_LIST_GITHUB_INSTALLATIONS_RESPONSE: ListGitHubInstallationsResponse = {
  installations: [],
  configured: false,
  repository_browse_configured: false,
  can_manage: false,
};

/** 校验 GitHub 连接响应（安装跳转 URL 与是否已配置）。 */
export const GitHubConnectResponseSchema = z.object({
  url: z.string().optional(),
  configured: z.boolean().optional().default(false),
}).loose();

/** GitHub 连接响应的空值兜底。 */
export const EMPTY_GITHUB_CONNECT_RESPONSE: GitHubConnectResponse = {
  configured: false,
};

/** 校验一个 GitHub 仓库（全名、克隆地址、描述、私有/归档标记、默认分支）。 */
export const GitHubRepositorySchema = z.object({
  id: z.number(),
  full_name: z.string(),
  html_url: z.string(),
  clone_url: z.string(),
  description: z.string().nullable(),
  private: z.boolean(),
  archived: z.boolean(),
  default_branch: z.string(),
}).loose();

/** 校验 GitHub 仓库列表响应（含总数与下一页）。 */
export const ListGitHubRepositoriesResponseSchema = z.object({
  repositories: z.array(GitHubRepositorySchema).default([]),
  total_count: z.number().optional().default(0),
  next_page: z.number().nullable().optional().default(null),
}).loose();

/** GitHub 仓库列表的空值兜底。 */
export const EMPTY_LIST_GITHUB_REPOSITORIES_RESPONSE: ListGitHubRepositoriesResponse = {
  repositories: [],
  total_count: 0,
  next_page: null,
};

/** 校验一个 GitHub PR（仓库、分支、作者、CI 汇总、合并状态、diff 统计等）。 */
export const GitHubPullRequestSchema = z.object({
  id: z.string(),
  provider: z.string().optional().default("github"),
  workspace_id: z.string(),
  repo_owner: z.string(),
  repo_name: z.string(),
  number: z.number(),
  title: z.string(),
  state: z.string(),
  html_url: z.string(),
  branch: z.string().nullable(),
  author_login: z.string().nullable(),
  author_avatar_url: z.string().nullable(),
  merged_at: z.string().nullable(),
  closed_at: z.string().nullable(),
  pr_created_at: z.string(),
  pr_updated_at: z.string(),
  mergeable: z.string().nullable().optional(),
  merge_state_status: z.string().nullable().optional(),
  snapshot_available: z.boolean().optional(),
  checks_rollup: z.string().nullable().optional(),
  checks_conclusion: z.string().nullable().optional(),
  checks_total: z.number().optional().default(0),
  checks_passed: z.number().optional().default(0),
  checks_failed: z.number().optional().default(0),
  checks_running: z.number().optional().default(0),
  checks_pending: z.number().optional().default(0),
  failed_check_names: z.array(z.string()).optional().default([]),
  snapshot_stale: z.boolean().optional().default(false),
  snapshot_fetched_at: z.string().nullable().optional(),
  mergeable_state: z.string().nullable().optional(),
  additions: z.number().optional().default(0),
  deletions: z.number().optional().default(0),
  changed_files: z.number().optional().default(0),
}).loose();

/** 校验某 issue 关联的 PR 列表响应。 */
export const IssuePullRequestsResponseSchema = z.object({
  pull_requests: z.array(GitHubPullRequestSchema).default([]),
}).loose();

/** issue 关联 PR 列表的空值兜底。 */
export const EMPTY_ISSUE_PULL_REQUESTS_RESPONSE: { pull_requests: GitHubPullRequest[] } = {
  pull_requests: [],
};

// 标签响应用于设置表格和资源选择器。resource_type 保持宽松，让新版服务器的 scope
// 不会破坏旧客户端；同时给那些早于「作用域标签目录」的字段提供默认值。
/** 校验一个标签（资源类型、名称、描述、颜色、使用次数等）。 */
export const LabelSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  resource_type: z.string().optional().default("issue"),
  name: z.string(),
  description: z.string().optional().default(""),
  color: z.string(),
  usage_count: z.number().optional().default(0),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

/** 标签的空值兜底。 */
export const EMPTY_LABEL: Label = {
  id: "",
  workspace_id: "",
  resource_type: "issue",
  name: "",
  description: "",
  color: "#6b7280",
  usage_count: 0,
  created_at: "",
  updated_at: "",
};

/** 校验标签列表响应（含总数）。 */
export const ListLabelsResponseSchema = z.object({
  labels: z.array(LabelSchema).default([]),
  total: z.number().default(0),
}).loose();

/** 标签列表的空值兜底。 */
export const EMPTY_LIST_LABELS_RESPONSE: ListLabelsResponse = {
  labels: [],
  total: 0,
};

/**
 * 校验 issue 状态目录（MUL-6243）中的一条。`category` 故意按普通字符串解析
 * 而不是枚举：新服务器可能返回本构建不认识的新分类，让整份目录解析失败会导致
 * UI 一个状态都渲染不出来。遇到不认识的分实时，消费方退回到按 `color`/`name` 渲染。
 */
export const IssueStatusEntrySchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  key: z.string(),
  name: z.string(),
  description: z.string().optional().default(""),
  category: z.string(),
  color: z.string().optional().default("#6b7280"),
  is_system: z.boolean().optional().default(false),
  position: z.number().optional().default(0),
  archived_at: z.string().nullable().optional().default(null),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

/** 单条 issue 状态条目的空值兜底。 */
export const EMPTY_ISSUE_STATUS_ENTRY: IssueStatusEntry = {
  id: "",
  workspace_id: "",
  key: "",
  name: "",
  description: "",
  category: "backlog",
  color: "#6b7280",
  is_system: false,
  position: 0,
  archived_at: null,
  created_at: "",
  updated_at: "",
};

/** 校验 issue 状态目录列表响应：状态条目、分类集合与总数。 */
export const ListIssueStatusesResponseSchema = z.object({
  statuses: z.array(IssueStatusEntrySchema).default([]),
  categories: z.array(z.string()).default([]),
  total: z.number().default(0),
}).loose();

// 兜底值把 7 个内置状态的 key 当作 categories，这样即使服务器旧到还没有这个端点，
// 客户端也能拿到权威的内置分类列表。
export const EMPTY_LIST_ISSUE_STATUSES_RESPONSE: ListIssueStatusesResponse = {
  statuses: [],
  categories: ["backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled"],
  total: 0,
};

/** 校验某资源的标签列表响应：标签数组 + 可选的 issue 修订号。 */
export const ResourceLabelsResponseSchema = z.object({
  labels: z.array(LabelSchema).default([]),
  issue_revision: z.number().int().positive().optional(),
}).loose();

/** 资源标签列表的空值兜底。 */
export const EMPTY_RESOURCE_LABELS_RESPONSE: ResourceLabelsResponse = {
  labels: [],
};

/**
 * 校验一个已保存的 issue 视图（MUL-4796）。`query`/`display` 是不透明的定义块，
 * 按 `definition_version` 在客户端解释——保持为宽松记录，新服务器可以自由加字段。
 * `scope_type`/`visibility` 保持宽松字符串；下游代码用显式 `===` 比较和 default
 * 分支处理（见 API 兼容规则）。
 */
export const IssueViewSchema = z.object({
  id: z.string(),
  workspace_id: z.string().default(""),
  owner_id: z.string().default(""),
  name: z.string().default(""),
  scope_type: z.string().default("workspace"),
  scope_id: z.string().nullish(),
  scope_variant: z.string().nullish(),
  visibility: z.string().default("private"),
  definition_version: z.number().default(1),
  query: z.record(z.string(), z.unknown()).default({}),
  display: z.record(z.string(), z.unknown()).default({}),
  revision: z.number().default(1),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

/** 一个已保存 issue 视图的类型（由 schema 推导）。 */
export type IssueView = z.infer<typeof IssueViewSchema>;

/** 校验已保存视图列表（直接就是一个视图数组）。 */
export const IssueViewListSchema = z.array(IssueViewSchema);

/** 校验视图偏好：作用域 + 该作用域下隐藏/排序的视图 id 列表。 */
export const IssueViewPreferenceSchema = z.object({
  scope_type: z.string().default("workspace"),
  scope_id: z.string().nullish(),
  prefs: z.object({
    hidden: z.array(z.string()).default([]),
    order: z.array(z.string()).default([]),
  }).loose().default({ hidden: [], order: [] }),
  updated_at: z.string().default(""),
}).loose();

/** 视图偏好的类型（由 schema 推导）。 */
export type IssueViewPreference = z.infer<typeof IssueViewPreferenceSchema>;

/** 视图偏好的空值兜底：无隐藏、无自定义顺序。 */
export const EMPTY_ISSUE_VIEW_PREFERENCE: IssueViewPreference = {
  scope_type: "workspace",
  scope_id: null,
  prefs: { hidden: [], order: [] },
  updated_at: "",
};

/** 创建/保存视图的请求体类型。 */
export interface CreateIssueViewRequest {
  name: string;
  scope_type: "workspace" | "my" | "project";
  scope_id?: string | null;
  scope_variant?: "assigned" | "created" | "involved" | "any" | "members" | "agents" | null;
  visibility: "private" | "workspace";
  definition_version: number;
  query: Record<string, unknown>;
  display: Record<string, unknown>;
}

/**
 * 校验自定义属性定义。`type` 保持宽松字符串：新服务器增加的类型不会弄坏已安装
 * 的客户端，UI 用 isKnownPropertyType 做收窄。
 */
export const IssuePropertySchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  name: z.string(),
  type: z.string(),
  description: z.string().optional().default(""),
  icon: z.string().optional().default(""),
  config: z.object({
    options: z.array(z.object({
      id: z.string(),
      name: z.string(),
      color: z.string().optional().default("#6b7280"),
    }).loose()).optional(),
  }).loose().default({}),
  position: z.number().optional().default(0),
  archived: z.boolean().optional().default(false),
  archived_at: z.string().nullable().optional(),
  usage_count: z.number().optional().default(0),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

/** 自定义属性的空值兜底。 */
export const EMPTY_ISSUE_PROPERTY: IssueProperty = {
  id: "",
  workspace_id: "",
  name: "",
  type: "text",
  description: "",
  icon: "",
  config: {},
  position: 0,
  archived: false,
  usage_count: 0,
  created_at: "",
  updated_at: "",
};

/**
 * 校验一条快捷指令（Quick action，MUL-5465）。`visibility` 与 `status` 用
 * z.string() 而不是 z.enum：它们由服务器驱动，新服务器加了新取值时应退到 UI
 * 的 default 分支渲染，而不是让整个列表变空白。
 */
export const QuickActionSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  name: z.string(),
  description: z.string().optional().default(""),
  assignee_type: z.string(),
  assignee_id: z.string(),
  prompt: z.string().optional().default(""),
  visibility: z.string().optional().default("public"),
  status: z.string().optional().default("active"),
  last_used_at: z.string().nullable().optional().default(null),
  use_count: z.number().optional().default(0),
  created_by_id: z.string().optional().default(""),
  created_at: z.string(),
  updated_at: z.string(),
  target_name: z.string().optional(),
  // 两个字段在旧服务器上都缺省为悲观的解读：「不知道它公开」和「不知道它缺失」，
  // 让设置页保持诚实，而不是声称服务器从未发过的状态。
  target_public: z.boolean().optional().default(false),
  target_missing: z.boolean().optional().default(false),
}).loose();

/** 快捷指令的空值兜底。 */
export const EMPTY_QUICK_ACTION: QuickAction = {
  id: "",
  workspace_id: "",
  name: "",
  description: "",
  assignee_type: "agent",
  assignee_id: "",
  prompt: "",
  visibility: "public",
  status: "active",
  last_used_at: null,
  use_count: 0,
  created_by_id: "",
  created_at: "",
  updated_at: "",
  target_public: false,
  target_missing: true,
};

/** 校验快捷指令列表响应。 */
export const ListQuickActionsResponseSchema = z.object({
  quick_actions: z.array(QuickActionSchema).default([]),
}).loose();

/** 快捷指令列表的空值兜底。 */
export const EMPTY_LIST_QUICK_ACTIONS_RESPONSE: ListQuickActionsResponse = {
  quick_actions: [],
};

/** 校验快捷指令渲染预览响应（渲染出的文本内容）。 */
export const QuickActionRenderSchema = z.object({
  content: z.string().default(""),
}).loose();

/** 校验自定义属性列表响应（含总数）。 */
export const ListPropertiesResponseSchema = z.object({
  properties: z.array(IssuePropertySchema).default([]),
  total: z.number().default(0),
}).loose();

/** 自定义属性列表的空值兜底。 */
export const EMPTY_LIST_PROPERTIES_RESPONSE: ListPropertiesResponse = {
  properties: [],
  total: 0,
};

/**
 * 校验某 issue 的属性值袋：以属性定义 UUID 为键，值为基本类型或字符串数组
 * （multi_select）。预处理步骤在真正校验前先丢弃形状未知的条目——新服务器若返回
 * 对象形状的值（未来的 actor/relation 类型），应当退化成「缺了那一个属性」，
 * 绝不能让整个 IssueSchema 校验失败、经 parseWithFallback 把列表刷成空白。
 */
export const IssuePropertyValuesSchema = z.preprocess(
  (raw) => {
    if (typeof raw !== "object" || raw === null || Array.isArray(raw)) return {};
    const out: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(raw)) {
      const ok =
        typeof value === "string" ||
        typeof value === "number" ||
        typeof value === "boolean" ||
        (Array.isArray(value) && value.every((item) => typeof item === "string"));
      if (ok) out[key] = value;
    }
    return out;
  },
  z.record(z.string(), z.union([z.string(), z.number(), z.boolean(), z.array(z.string())])).default({}),
);

/** 校验 issue 属性值响应：值袋 + 可选的 issue 修订号。 */
export const IssuePropertiesResponseSchema = z.object({
  properties: IssuePropertyValuesSchema,
  issue_revision: z.number().int().positive().optional(),
}).loose();

/** issue 属性值的空值兜底。 */
export const EMPTY_ISSUE_PROPERTIES_RESPONSE: IssuePropertiesResponse = {
  properties: {},
};

/** 应用级配置（CDN、登录、遥测、daemon 地址、功能开关、能力信号等）。 */
export interface AppConfigResponse {
  cdn_domain: string;
  attachment_public_url?: string;
  // True when the CDN domain serves private content via time-bounded signed
  // URLs (CloudFront signing) — raw storage URLs on that domain are NOT
  // publicly fetchable and must not be used as native media sources
  // (MUL-3254). Older servers omit the field; treat that as false.
  cdn_signed?: boolean;
  allow_signup: boolean;
  google_client_id?: string;
  posthog_key?: string;
  posthog_host?: string;
  analytics_environment?: string;
  daemon_server_url?: string;
  daemon_app_url?: string;
  workspace_creation_disabled?: boolean;
  /** 本次部署是否提供自托管 Git 提供方集成（仅自托管版；托管云上关闭）。
   * 缺失/false 会隐藏整个「设置 → 集成 → Git providers」区块。 */
  vcs_integration_available?: boolean;
  feature_flags?: Record<string, boolean>;
  /** 服务器是否理解 local_directory `execution_mode`、并在保存时把关 worktree 模式。
   * 早于该能力信号的服务器都没有此字段，其中包括那些静默丢弃未知 execution_mode
   * 却仍回 201 的版本——资源于是在本地原处运行，而用户以为获得了隔离（#7113）。
   * 处于修复与信号之间的服务器虽然会校验却无法声明，按「不支持」处理：客户端无法
   * 区分二者，而两种答案中只有一种安全。 */
  local_worktree_supported?: boolean;
  /** 服务器是否在创建/更新 agent 时持久化 `conversation_starters`。旧服务器会静默
   * 忽略未知字段，所以字段缺失必须当作 false。 */
  agent_conversation_starters_supported?: boolean;
  issue_create_properties_supported?: boolean;
  /** Whether deleting a comment keeps its replies and the server routes
   * DELETE /api/comments/{id}/keep-replies. Older servers deleted the replies
   * too, so absent must be treated as false (#8296). */
  comment_delete_keep_replies_supported?: boolean;
  server_version?: string;
  environment_name?: string;
  environment_id?: string;
}

// ---------------------------------------------------------------------------
// 最高风险 API 端点的 schema——这些响应对应 issue 详情页（timeline / comments /
// subscribers）和 issue 列表。它们就是 #2143 / #2147 / #2192 里白屏的那批页面。
//
// 这些 schema 有意设计得「宽松」：
//   - 字符串枚举用 `z.string()` 而不是 `z.enum([...])`。服务器新增的枚举值应该在
//     UI 里渲染成通用兜底，而不是让 `safeParse` 崩溃。
//   - 可选字段与 `null` 做联合，并在既有 UI 代码已做兜底的地方给出默认值。
//   - 数组默认 `[]`，这样缺失的 `reactions` / `attachments` / `entries` 字段
//     不会把整个页面搞挂。
//   - 每个对象 schema 都以 `.loose()` 结尾，让未知的服务端字段原样透传。zod 4 的
//     `.object()` 默认是 STRIP（剥离），会静默删掉 schema 未显式列出的字段——在 TS
//     类型还没声明它们时没问题，但一旦将来某个 PR 加了 TS 字段却没同步 schema，
//     那个 `as T` 断言就会说谎，运行时字段变成 `undefined`。`.loose()` 消除了这个
//     同步隐患。
//
// 这些 schema 故意不标注成 `z.ZodType<TimelineEntry>` / `z.ZodType<Issue>` 等——
// 严格的 TS 类型会把字符串字段收窄成字面量联合，反而破坏了上面的宽松性。
// `parseWithFallback` 会把解析结果 cast 成调用方提供的 `T`，所以严格类型仍然在
// 调用处流出；schema 只负责校验形状。
// ---------------------------------------------------------------------------

const ReactionSchema = z.object({
  id: z.string(),
  comment_id: z.string(),
  actor_type: z.string(),
  actor_id: z.string(),
  emoji: z.string(),
  created_at: z.string(),
});

// 内嵌在 timeline/comment 响应里的附件刻意保持宽松：单个畸形附件不能把整个
// timeline 打进兜底的 `[]`。
const AttachmentSchema = z.object({
  id: z.string(),
}).loose();

// 聊天消息附带的快捷指令：展示文案 + 填充用的 prompt，primary 标记是否主推。
const ChatQuickActionSchema = z.object({
  label: z.string(),
  prompt: z.string(),
  primary: z.boolean().optional(),
}).loose();

/** 校验一条聊天消息：角色、内容、关联任务、附件、失败原因等。 */
export const ChatMessageSchema = z.object({
  id: z.string(),
  chat_session_id: z.string(),
  role: z.enum(["user", "assistant"]).catch("assistant"),
  content: z.string().default(""),
  task_id: z.string().nullable().default(null),
  created_at: z.string().default(""),
  attachments: z.array(AttachmentSchema).optional(),
  failure_reason: z.string().nullable().optional(),
  elapsed_ms: z.number().nullable().optional(),
  message_kind: z
    .enum(["message", "no_response", "onboarding_kickoff", "onboarding_opening"])
    .catch("message")
    .optional(),
  // 可选的附加数据独立降级：一条畸形建议绝不能把包含它的助手回复也一起藏掉。
  quick_actions: z.array(ChatQuickActionSchema).catch([]).optional().default([]),
}).loose();

/** 校验聊天消息列表（直接是消息数组）。 */
export const ChatMessageListSchema = z.array(ChatMessageSchema).default([]);
/** 聊天消息列表的空值兜底。 */
export const EMPTY_CHAT_MESSAGE_LIST: ChatMessage[] = [];

/** 校验聊天消息分页响应：消息数组 + limit + has_more + 下一页游标。 */
export const ChatMessagesPageSchema = z.object({
  messages: z.array(ChatMessageSchema).default([]),
  limit: z.number().default(50),
  has_more: z.boolean().default(false),
  next_cursor: z.object({
    created_at: z.string(),
    id: z.string(),
  }).loose().nullable().optional(),
}).loose();

/**
 * 校验独立附件查询（GET /api/attachments/{id}）。这是点击时下载 URL 的权威来源；
 * 下载流程会在新标签页打开的 `download_url` 与 `url` 必须是非空字符串，否则会
 * 变成 `window.open(undefined)`。`filename` 决定 toast/标题，同样强校验——
 * 缺失时会落到下面的空记录兜底。
 *
 * `markdown_url` 宽松解析：早于 MUL-3192 的服务器不返回该字段，schema 用 "" 兜底。
 * 需要把 URL 持久化进 markdown 的调用方应走 `useFileUpload`（它在 `markdown_url`
 * 为空时退回旧 `attachmentDownloadPath` 形状），从而空字符串默认值不会悄悄破坏
 * 任何持久化路径。
 */
export const AttachmentResponseSchema = z.object({
  id: z.string(),
  url: z.string(),
  download_url: z.string(),
  // 「强制附件」（下载按钮）URL——无需凭据，且不同于 `download_url`，它在各种存储
  // 模式下都带 Content-Disposition: attachment。可选：早于该字段的服务器不返回，
  // 调用方退回 `download_url` / 稳定端点。不持久化（短生命周期）。
  attachment_download_url: z.string().optional(),
  markdown_url: z.string().optional().default(""),
  filename: z.string(),
  chat_session_id: z.string().nullable().optional(),
  chat_message_id: z.string().nullable().optional(),
}).loose();

/** 单个附件的空值兜底。 */
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

// 所有对象 schema 都以 `.loose()` 结尾，让未知的服务端字段原样透传。zod 4 的
// `.object()` 默认是 STRIP（剥离），会静默删掉 schema 未显式列出的字段——在 TS
// 类型还没声明它们时没问题，但一旦将来某个 PR 加了 TS 字段却没同步 schema，
// 那个 `as T` 断言就会说谎，运行时字段变成 undefined。`.loose()` 消除了这个
// 同步隐患——schema 校验它认识的形状，剩下的原样保留。
const TimelineEntrySchema = z.object({
  type: z.string(),
  id: z.string(),
  actor_type: z.string(),
  actor_id: z.string(),
  created_at: z.string(),
  actor_name: z.string().optional(),
  actor_avatar_url: z.string().optional(),
  action: z.string().optional(),
  details: z.record(z.string(), z.unknown()).optional(),
  content: z.string().optional(),
  parent_id: z.string().nullable().optional(),
  updated_at: z.string().optional(),
  revision: z.number().int().positive().optional(),
  comment_type: z.string().optional(),
  reactions: z.array(ReactionSchema).optional(),
  attachments: z.array(AttachmentSchema).optional(),
  source_task_id: z.string().nullable().optional(),
  supplement_task_id: z.string().optional().catch(undefined),
  supplement_status: z.enum(["pending", "delivering", "delivered", "failed"]).optional().catch(undefined),
  supplement_failure_reason: z.string().optional().catch(undefined),
  supplement_delivered_at: z.string().optional().catch(undefined),
  // Tombstone marker (#8296). Lenient: a malformed value reads as a live
  // comment instead of failing the whole timeline.
  deleted_at: z.string().nullable().optional().catch(undefined),
  coalesced_count: z.number().optional(),
}).loose();

// /timeline 返回扁平的 TimelineEntry 数组，旧的在前。之前游标分页的包装结构已被
// 移除（#1929）——按观测到的数据量（单 issue 约 p99 30 条），分页只制造麻烦。
/** 校验 issue timeline 条目列表（直接是条目数组）。 */
export const TimelineEntriesSchema = z.array(TimelineEntrySchema);

/** timeline 条目的空值兜底。 */
export const EMPTY_TIMELINE_ENTRIES: TimelineEntry[] = [];

// 可选字符串 helper：非字符串值（null/缺省）归一成 undefined。
const OptionalStringSchema = z.preprocess(
  (value) => (typeof value === "string" ? value : undefined),
  z.string().optional(),
);

// 布尔 helper：非布尔值按缺省值兜底，用于兼容旧服务器省略字段的情况。
const BooleanWithDefaultSchema = (fallback: boolean) =>
  z.preprocess(
    (value) => (typeof value === "boolean" ? value : undefined),
    z.boolean().default(fallback),
  );

// 特性开关集合：整体须是对象，逐个值缺省为 false。
const FeatureFlagsSchema = z.preprocess(
  (value) =>
    value && typeof value === "object" && !Array.isArray(value)
      ? value
      : undefined,
  z.record(z.string(), BooleanWithDefaultSchema(false)).default({}),
);

/**
 * POST /api/auth/refresh — sliding session renewal (MUL-7436).
 *
 * `token` is present only for clients that carry the session as a string
 * (Desktop, mobile). A cookie-authenticated browser gets the renewed session
 * as a Set-Cookie and must never be handed a readable JWT, so the field is
 * absent there rather than empty.
 *
 * `renewed: false` is the normal answer to asking early, not an error —
 * clients poll on a cadence and most calls land outside the renewal window.
 * `check_again_in_seconds` is that cadence: the server derives it from the
 * deployment's configured TTL, so no client hardcodes one.
 */
export interface RefreshSessionResponse {
  token?: string;
  expires_at: string;
  renewed: boolean;
  check_again_in_seconds: number;
}

export const RefreshSessionResponseSchema = z.object({
  token: OptionalStringSchema,
  expires_at: OptionalStringSchema,
  renewed: BooleanWithDefaultSchema(false),
  check_again_in_seconds: z.number().int().nonnegative().default(0),
}).loose();

// Fail closed: an unreadable response means "nothing was renewed", so the
// client keeps the session it already has and retries later. A fallback that
// claimed renewal would drop a working session on the floor.
export const EMPTY_REFRESH_SESSION_RESPONSE: RefreshSessionResponse = {
  expires_at: "",
  renewed: false,
  check_again_in_seconds: 0,
};

export const AppConfigSchema = z.object({
  cdn_domain: z.string().default(""),
  attachment_public_url: OptionalStringSchema,
  cdn_signed: BooleanWithDefaultSchema(false),
  allow_signup: BooleanWithDefaultSchema(true),
  google_client_id: OptionalStringSchema,
  posthog_key: OptionalStringSchema,
  posthog_host: OptionalStringSchema,
  analytics_environment: OptionalStringSchema,
  daemon_server_url: OptionalStringSchema,
  daemon_app_url: OptionalStringSchema,
  workspace_creation_disabled: BooleanWithDefaultSchema(false).optional(),
  vcs_integration_available: BooleanWithDefaultSchema(false).optional(),
  feature_flags: FeatureFlagsSchema,
  local_worktree_supported: BooleanWithDefaultSchema(false),
  agent_conversation_starters_supported: BooleanWithDefaultSchema(false),
  issue_create_properties_supported: BooleanWithDefaultSchema(false),
  comment_delete_keep_replies_supported: BooleanWithDefaultSchema(false),
  server_version: OptionalStringSchema,
  environment_name: OptionalStringSchema,
  environment_id: OptionalStringSchema,
}).loose();

/** 应用级配置的空值兜底。 */
export const EMPTY_APP_CONFIG: AppConfigResponse = {
  cdn_domain: "",
  attachment_public_url: "",
  cdn_signed: false,
  allow_signup: true,
  google_client_id: "",
  daemon_server_url: "",
  daemon_app_url: "",
  workspace_creation_disabled: false,
  vcs_integration_available: false,
  // 失败时闭锁（fail closed）：读不到的配置不能伪装成「服务器会校验 execution_mode」。
  local_worktree_supported: false,
  // 失败时闭锁：旧服务器当年是丢弃该字段却返回成功。
  agent_conversation_starters_supported: false,
  issue_create_properties_supported: false,
  // Fail closed: old servers delete a comment's replies with it.
  comment_delete_keep_replies_supported: false,
  feature_flags: {},
};

// 通知偏好键会随时间增长，所以键名和取值都保持向前兼容，但拒绝非字符串的持久化值。
/** 校验通知偏好响应：workspace id + 偏好的键值对。 */
export const NotificationPreferenceResponseSchema = z.object({
  workspace_id: z.string(),
  preferences: z.record(z.string(), z.string()).default({}),
}).loose();

/** 通知偏好响应的空值兜底。 */
export const EMPTY_NOTIFICATION_PREFERENCE_RESPONSE: NotificationPreferenceResponse = {
  workspace_id: "",
  preferences: {},
};

/** 校验一条评论：作者、正文、类型、父评论、回复/附件/修订号等。 */
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
  revision: z.number().int().positive().optional(),
  source_task_id: z.string().nullable().optional(),
  supplement_task_id: z.string().optional().catch(undefined),
  supplement_status: z.enum(["pending", "delivering", "delivered", "failed"]).optional().catch(undefined),
  supplement_failure_reason: z.string().optional().catch(undefined),
  supplement_delivered_at: z.string().optional().catch(undefined),
  // Set only on comments a quick action produced (MUL-5465). Server-only.
  quick_action_id: z.string().nullable().optional(),
  deleted_at: z.string().nullable().optional().catch(undefined),
}).loose();

/** 校验评论列表（直接是评论数组）。 */
export const CommentsListSchema = z.array(CommentSchema);

// 评论响应校验失败时的降级占位。空 id 是调用方的信号：没有可用内容回来——
// run UI 把它当作「读不到结果」而不是「一次成功的运行」。
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

const CommentTriggerPreviewAgentSchema = z.object({
  id: z.string(),
  name: z.string().default(""),
  account: z.string().default(""),
  avatar_url: z.string().optional(),
  source: z.string().default(""),
  reason: z.string().default(""),
}).loose();

// 显式 @agent / @squad 提及时每个目标的结果（MUL-4525 §2）。target_id 必须与
// 客户端渲染的提及对应；畸形条目（缺 id）会被丢弃而不是让整个负载校验失败。
export const CommentTriggerOutcomeSchema = z.object({
  target_type: z.string().default(""),
  target_id: z.string(),
  status: z.string().default(""),
  reason_code: z.string().default(""),
}).loose();

// 回复托管审查员时的转交结果（GONG-63）：谁实际接收这条回复、以及发生/将发生什么。
// 增量字段：旧服务器不返回它，校验时整体可缺省。
export const CommentReviewReplySchema = z.object({
  recipient_type: z.string().default(""),
  recipient_id: z.string().default(""),
  recipient_name: z.string().default(""),
  status: z.string().default(""),
  reason_code: z.string().default(""),
}).loose();

/** 校验评论触发预览响应：可触达的 agents + 被拦截的提及结果。 */
export const CommentTriggerPreviewSchema = z.object({
  agents: z.array(CommentTriggerPreviewAgentSchema).default([]),
  // 被拦截的条目逐个丢弃（MUL-4525）：单个坏数据不能把整组有效提及清掉。非数组
  // 退化成 []；每条有效条目保留，每条畸形条目丢弃。
  blocked: z
    .array(z.unknown())
    .catch([])
    .default([])
    .transform((items) =>
      items.flatMap((item) => {
        const parsed = CommentTriggerOutcomeSchema.safeParse(item);
        return parsed.success ? [parsed.data] : [];
      }),
    ),
  // 回复审查员时这条草稿会通知谁（GONG-63）。缺省/畸形都退化成 null：预览提示是
  // 增强信息，不能因为它把整组有效触发清掉。
  review_reply: CommentReviewReplySchema.nullish().catch(null).default(null),
}).loose();

// 单条触发预览项：目标 issue、候选 agent、来源、是否支持交接。
const IssueTriggerPreviewItemSchema = z.object({
  issue_id: z.string(),
  agent_id: z.string().default(""),
  source: z.string().default(""),
  handoff_supported: z.boolean().default(false),
}).loose();

/** 校验 issue 触发预览响应：可能的触发项 + 总数。 */
export const IssueTriggerPreviewSchema = z.object({
  triggers: z.array(IssueTriggerPreviewItemSchema).default([]),
  total_count: z.number().default(0),
}).loose();

// 元数据按 API/DB 契约只含基本类型。形状保持宽松：未知键对调用方以 `unknown`
// 落地，但字段本身默认 {}，消费者无需对 `issue.metadata` 做空值防护。
const IssueMetadataSchema = z.record(z.string(), z.union([z.string(), z.number(), z.boolean()])).default({});

const SourceContextAttachmentSchema = z.object({
  id: z.string(),
  source_attachment_id: z.string().optional(),
  owner_type: z.string(),
  owner_id: z.string(),
  filename: z.string(),
  content_type: z.string(),
  size_bytes: z.number(),
  created_at: z.string(),
}).loose();

// 早期的 source-context 服务器会把空 Go slice 编码成 JSON null。保持已安装客户端的
// 兼容，同时把消费者统一归一化到当前服务器发出的标准数组形状。
const SourceContextAttachmentsSchema = z.array(SourceContextAttachmentSchema)
  .nullish()
  .transform((attachments) => attachments ?? []);

const SourceContextAuthorSchema = z.object({
  type: z.string(),
  id: z.string(),
  name: z.string(),
}).loose();

const SourceContextIssueSnapshotSchema = z.object({
  id: z.string(),
  identifier: z.string(),
  number: z.number(),
  title: z.string(),
  description: z.string().nullable(),
  created_at: z.string(),
  updated_at: z.string(),
  revision: z.number(),
  attachments: SourceContextAttachmentsSchema,
}).loose();

const SourceContextCommentSnapshotSchema = z.object({
  id: z.string(),
  parent_id: z.string().nullable(),
  type: z.string(),
  content: z.string(),
  author: SourceContextAuthorSchema,
  created_at: z.string(),
  updated_at: z.string(),
  revision: z.number(),
  attachments: SourceContextAttachmentsSchema,
  deleted: z.boolean().optional().catch(undefined),
}).loose();

/** 校验「来源上下文」快照：被引用 issue/评论线程的只读副本 + 锚点。 */
export const SourceContextSnapshotSchema = z.object({
  version: z.number().optional(),
  captured_by_user_id: z.string().optional(),
  captured_at: z.string().optional(),
  source_issue: SourceContextIssueSnapshotSchema,
  comment_thread: z.array(SourceContextCommentSnapshotSchema),
  anchor_comment_id: z.string(),
}).loose();

/** 校验「来源上下文」预览：快照基础上追加一次性捕获令牌与体量上限。 */
export const SourceContextPreviewSchema = SourceContextSnapshotSchema.extend({
  capture_token: z.string().min(1),
  limits: z.object({
    comment_count: z.number(),
    text_bytes: z.number(),
    attachment_count: z.number(),
    attachment_bytes: z.number(),
  }).loose(),
}).loose();

const IssueSourceContextSchema = z.object({
  id: z.string(),
  version: z.number(),
  usage: z.string(),
  captured_at: z.string(),
  display_state: z.string(),
  source_issue_state: z.string(),
  comment_thread_state: z.string(),
  anchor_comment_state: z.string(),
  can_open_current_source: z.boolean(),
  change_reasons: z.array(z.string()).optional(),
  change_details: z.object({
    changed_comment_ids: z.array(z.string()),
    added_comments: z.array(SourceContextCommentSnapshotSchema).optional(),
    removed_comment_ids: z.array(z.string()).optional(),
    description_attachment_changes: z.array(z.object({
      kind: z.string(),
      attachment_id: z.string(),
      filename: z.string(),
      previous_filename: z.string().optional(),
    }).loose()),
  }).loose().optional(),
  current_source: z.object({
    issue_id: z.string(),
    identifier: z.string(),
    anchor_comment_id: z.string(),
  }).loose().optional(),
  source_author_state: z.array(z.object({
    type: z.string(),
    id: z.string(),
    captured_name: z.string(),
    current_name: z.string().optional(),
    state: z.string(),
  }).loose()).optional(),
  snapshot: SourceContextSnapshotSchema,
}).loose();

/** 校验「评论转子任务」响应的形状：任务 id 非空。 */
export const CommentSubIssueTaskResponseSchema = z.object({
  task_id: z.string().min(1),
}).loose();

/**
 * 校验一个 issue 对象。字段策略见文件头大注释：枚举保持宽松、可空字段并上 null、
 * 未知字段由 `.loose()` 透传。
 */
export const IssueSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  number: z.number(),
  identifier: z.string(),
  title: z.string(),
  description: z.string().nullable(),
  status: z.string(),
  // `status` 承载的平台行为对应的权威分类：7 个内置状态取自身，自定义状态取继承的
  // 分类。可选是因为只有会解析它的端点才返回，消费方必须退回用 `status`，而不是把
  // "" 当成一个分类。(MUL-6243)
  status_category: z.string().optional(),
  // 自定义状态的显示名；内置状态为 ""（客户端从 key 本地化）。可选，这样早于该字段
  // 的服务器响应也能通过校验。
  //
  // 用 .catch(undefined) 是因为这是「附加的展示数据」，一旦它坏掉损失不成比例：
  // IssueSchema 里任何一处解析失败都会让整个响应经 parseWithFallback 落到
  // EMPTY_LIST_ISSUES_RESPONSE——混合版本部署里一条畸形名称就会清空整张 issue 列表。
  // 与上方 source_context、labels 同样处理。没有代码读它做决策（useStatusLabel 从
  // 目录解析标签），所以丢弃它只是退回用 key。(MUL-6749)
  status_name: z.string().optional().catch(undefined),
  priority: z.enum(["important_urgent", "important_not_urgent", "not_important_urgent", "not_important_not_urgent"]),
  assignee_type: z.string().nullable(),
  assignee_id: z.string().nullable(),
  creator_type: z.string(),
  creator_id: z.string(),
  parent_issue_id: z.string().nullable(),
  project_id: z.string().nullable(),
  position: z.number(),
  // 旧后端早于 `stage` 字段；缺省为 null，让缺失字段干净地解析进非可选的
  // Issue.stage (number | null)。
  stage: z.number().nullable().default(null),
  start_date: z.string().nullable(),
  due_date: z.string().nullable(),
  metadata: IssueMetadataSchema,
  // 旧后端早于自定义属性；缺省 {}，消费者无需对 issue.properties 做空值防护。
  properties: IssuePropertyValuesSchema,
  reactions: z.array(z.unknown()).optional(),
  labels: z.array(z.unknown()).optional(),
  created_at: z.string(),
  updated_at: z.string(),
  revision: z.number().int().positive().optional(),
  // 为兼容旧的自托管后端而可选；现在的后端会一直发 null，直到历史回填追平该 issue。
  last_activity_at: z.string().nullable().optional(),
  // 仅详情端点返回且可能很大。畸形的附加字段不能抹掉混合版本服务器返回的、本可用的
  // issue。
  source_context: IssueSourceContextSchema.optional().catch(undefined),
}).loose();

/** 校验 issue 列表响应：issue 数组 + 总数。 */
export const ListIssuesResponseSchema = z.object({
  issues: z.array(IssueSchema).default([]),
  total: z.number().default(0),
}).loose();

// POST /api/issues 的响应 schema。相对 IssueSchema 有两处收紧：
//
//   - `id` 必须非空。创建成功的 issue 必然带真实 id，所以 id 空/缺失即代表创建实际
//     失败。createIssue 会把 schema 失败转成 rejection（而不是伪造成功），这里收紧
//     id 就把「没有 id 的响应体」导进同一条失败路径。
//   - `labels` 是创建弹窗读取的后端兼容信号，用来判断后端是在创建事务里就挂好标签
//     （有该字段）还是早于该特性（缺失 → 退回逐标签附加）。把它严格校验成 Label[]，
//     畸形值降级为 `undefined`——与缺失同义——这样错误形状（null、对象、垃圾数组）
//     永远无法伪装成「已处理」而压掉兜底逻辑。不同于宽松的 IssueSchema.labels
//     （z.array(z.unknown())），这里的元素会被完整校验。见 packages/views/modals/create-issue.tsx。
export const CreateIssueResponseSchema = IssueSchema.extend({
  id: z.string().min(1),
  labels: z.array(LabelSchema).optional().catch(undefined),
}).loose();

/** issue 列表响应的空值兜底。 */
export const EMPTY_LIST_ISSUES_RESPONSE: ListIssuesResponse = {
  issues: [],
  total: 0,
};

// 搜索结果项：issue 快照 + 命中的来源与片段。
const SearchIssueResultSchema = IssueSchema.extend({
  match_source: z.string(),
  matched_snippet: z.string().optional(),
  matched_description_snippet: z.string().optional(),
  matched_comment_snippet: z.string().optional(),
}).loose();

/** 校验 issue 搜索响应：结果数组 + 总数。 */
export const SearchIssuesResponseSchema = z.object({
  issues: z.array(SearchIssueResultSchema).default([]),
  total: z.number().default(0),
}).loose();

/** issue 搜索响应的空值兜底。 */
export const EMPTY_SEARCH_ISSUES_RESPONSE: SearchIssuesResponse = {
  issues: [],
  total: 0,
};

// 单个项目（project）。
const ProjectSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  title: z.string(),
  description: z.string().nullable(),
  icon: z.string().nullable(),
  status: z.string(),
  priority: z.string(),
  lead_type: z.string().nullable(),
  lead_id: z.string().nullable(),
  // .default(null) 保证旧后端（前端先部署）省略这些键时能解析成 null 而不是让整个
  // 对象失败——后者会把一整个搜索/列表批次降级成空兜底。
  start_date: z.string().nullable().default(null),
  due_date: z.string().nullable().default(null),
  created_at: z.string(),
  updated_at: z.string(),
  issue_count: z.number().default(0),
  done_count: z.number().default(0),
  resource_count: z.number().default(0),
}).loose();

// 项目搜索结果：项目快照 + 命中的来源与片段。
const SearchProjectResultSchema = ProjectSchema.extend({
  match_source: z.string(),
  matched_snippet: z.string().optional(),
}).loose();

/** 校验项目搜索响应：结果数组 + 总数。 */
export const SearchProjectsResponseSchema = z.object({
  projects: z.array(SearchProjectResultSchema).default([]),
  total: z.number().default(0),
}).loose();

/** 项目搜索响应的空值兜底。 */
export const EMPTY_SEARCH_PROJECTS_RESPONSE: SearchProjectsResponse = {
  projects: [],
  total: 0,
};

// 分组：按负责人/项目等聚成一组，内含该组的 issue。
const IssueAssigneeGroupSchema = z.object({
  id: z.string(),
  assignee_type: z.string().nullable(),
  assignee_id: z.string().nullable(),
  issues: z.array(IssueSchema).default([]),
  total: z.number().default(0),
}).loose();

/** 校验「按负责人分组」的 issue 列表响应。 */
export const GroupedIssuesResponseSchema = z.object({
  groups: z.array(IssueAssigneeGroupSchema).default([]),
}).loose();

/** 分组 issue 列表的空值兜底。 */
export const EMPTY_GROUPED_ISSUES_RESPONSE: GroupedIssuesResponse = {
  groups: [],
};

// actor 引用：服务端驱动的枚举保持开放，已安装的桌面客户端才能扛住后端新增角色种类。
const IssueTableActorRefSchema = z.object({
  type: z.string(),
  id: z.string(),
}).loose();

// 父级 issue 引用（层级视图行用）。
const IssueTableParentRefSchema = z.object({
  id: z.string(),
  number: z.number(),
  identifier: z.string(),
  title: z.string(),
  status: z.string(),
}).loose();

// 表格行的分组值：按 kind 区分状态/负责人/项目/父级/自定义属性五种分组来源。
const IssueTableGroupValueSchema = z.discriminatedUnion("kind", [
  z.object({
    kind: z.literal("status"),
    status: z.string(),
  }).loose(),
  z.object({
    kind: z.literal("assignee"),
    actor: IssueTableActorRefSchema.nullable(),
  }).loose(),
  z.object({
    kind: z.literal("project"),
    project_id: z.string().nullable().optional().default(null),
  }).loose(),
  z.object({
    kind: z.literal("parent"),
    parent_id: z.string().nullable().optional().default(null),
    parent: IssueTableParentRefSchema.nullable().optional().default(null),
    value_state: z.enum(["value", "unavailable", "unset"]),
  }).loose(),
  z.object({
    kind: z.literal("property"),
    property_id: z.string(),
    value: z.union([z.string(), z.boolean(), z.null()]).optional(),
    value_state: z.enum(["value", "unavailable", "unset"]),
  }).loose(),
]);

// 递归的分组描述符：key + 分组值 + 计数 + 可选的次级分组（用 lazy 支持自引用）。
const IssueTableGroupDescriptorSchema: z.ZodType<IssueTableGroupDescriptor> = z.lazy(() => z.object({
  key: z.string(),
  value: IssueTableGroupValueSchema,
  count: z.number(),
  secondary_groups: z.array(IssueTableGroupDescriptorSchema).optional(),
}).loose());

/** 校验 issue 表格分组响应：查询指纹、总数、分组树与下一页游标。 */
export const IssueTableGroupsResponseSchema = z.object({
  query_fingerprint: z.string(),
  total: z.number(),
  groups: z.array(IssueTableGroupDescriptorSchema).default([]),
  next_cursor: z.string().nullable().default(null),
}).loose();

/** issue 表格分组响应的空值兜底。 */
export const EMPTY_ISSUE_TABLE_GROUPS_RESPONSE: IssueTableGroupsResponse = {
  query_fingerprint: "",
  total: 0,
  groups: [],
  next_cursor: null,
};

// 表格行：issue + 直接子级计数。
const IssueTableRowSchema = z.object({
  issue: IssueSchema,
  direct_child_count: z.number().default(0),
}).loose();

/** 校验 issue 表格行响应：查询指纹、分组/父级定位、行数据与游标。 */
export const IssueTableRowsResponseSchema = z.object({
  query_fingerprint: z.string(),
  group_key: z.string().nullable().default(null),
  parent_id: z.string().nullable().default(null),
  total: z.number(),
  rows: z.array(IssueTableRowSchema).default([]),
  branch_total: z.number(),
  next_cursor: z.string().nullable().default(null),
}).loose();

/** issue 表格行响应的空值兜底。 */
export const EMPTY_ISSUE_TABLE_ROWS_RESPONSE: IssueTableRowsResponse = {
  query_fingerprint: "",
  group_key: null,
  parent_id: null,
  total: 0,
  rows: [],
  branch_total: 0,
  next_cursor: null,
};

// 单个筛选面的一个取值及计数。
const IssueTableFacetValueSchema = z.object({
  key: z.string(),
  count: z.number(),
}).loose();

// 单个筛选面：kind + 可选 property_id + 取值列表。
const IssueTableFacetSchema = z.object({
  kind: z.enum(["status", "priority", "assignee", "creator", "project", "label", "property", "working_agents"]),
  property_id: z.string().optional(),
  values: z.array(IssueTableFacetValueSchema).default([]),
}).loose();

/** 校验 issue 表格筛选面响应：查询指纹、总数与各筛选面。 */
export const IssueTableFacetsResponseSchema = z.object({
  query_fingerprint: z.string(),
  total: z.number(),
  facets: z.array(IssueTableFacetSchema).default([]),
}).loose();

/** issue 表格筛选面的空值兜底。 */
export const EMPTY_ISSUE_TABLE_FACETS_RESPONSE: IssueTableFacetsResponse = {
  query_fingerprint: "",
  total: 0,
  facets: [],
};

// 一个 issue 订阅者。
const SubscriberSchema = z.object({
  issue_id: z.string(),
  user_type: z.string(),
  user_id: z.string(),
  reason: z.string(),
  created_at: z.string(),
}).loose();

/** 校验 issue 订阅者列表（直接是数组）。 */
export const SubscribersListSchema = z.array(SubscriberSchema);

/** 校验某 issue 的子 issue 列表响应。 */
export const ChildIssuesResponseSchema = z.object({
  issues: z.array(IssueSchema).default([]),
}).loose();

/** 校验子 issue 进度汇总响应：每个父 issue 的总数/已完成数。 */
export const ChildIssueProgressResponseSchema = z.object({
  progress: z
    .array(
      z
        .object({
          parent_issue_id: z.string(),
          total: z.number(),
          done: z.number(),
        })
        .loose(),
    )
    .default([]),
}).loose();


/** 校验单个云运行时节点（实例元数据快照）。 */
export const CloudRuntimeNodeSchema = z.object({
  id: z.string(),
  owner_id: z.string(),
  instance_id: z.string(),
  region: z.string(),
  instance_type: z.string(),
  image_id: z.string(),
  subnet_id: z.string(),
  name: z.string(),
  status: z.string(),
  tags: z.record(z.string(), z.string()).default({}),
  metadata: z.record(z.string(), z.unknown()).default({}),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

/** 校验云运行时节点列表（直接是数组）。 */
export const CloudRuntimeNodeListSchema = z.array(CloudRuntimeNodeSchema);

/** 云运行时节点列表的空值兜底。 */
export const EMPTY_CLOUD_RUNTIME_NODE_LIST: CloudRuntimeNode[] = [];

/** 单个云运行时节点的空值兜底。 */
export const EMPTY_CLOUD_RUNTIME_NODE: CloudRuntimeNode = {
  id: "",
  owner_id: "",
  instance_id: "",
  region: "",
  instance_type: "",
  image_id: "",
  subnet_id: "",
  name: "",
  status: "",
  tags: {},
  metadata: {},
  created_at: "",
  updated_at: "",
};

// ---------------------------------------------------------------------------
// Workspace 仪表盘 schema
//
// 仪表盘打三个相互独立的汇总端点。每个都返回扁平数组，所有字段都喂给图表 / KPI
// 计算——缺失的数字下游会静默退化成 NaN，所以这里把缺失数字归成 0。字符串字段默认
// ""（不做枚举收窄），以扛住未来 model / agent id 漂移；同时 tz 感知的 SQL 分桶偶尔
// 吐出的单个 null 只会让那一行失败，而不是把整个数组都丢到 `[]` 兜底。
// ---------------------------------------------------------------------------

// 每条用量行都携带的成本拆分。`cost_usd_ticks` 是该聚合背后各行的供应商实际计费
// （1e-10 美元）；`uncosted_*` 计数是供应商没有定价的行的 token——只有这些是客户端
// 应该按自己的费率表估算的部分。
//
// `uncosted_*` 字段特意用 `.optional()` 而不是 `.default(0)`：早于它们的后端不发送，
// 一旦默认成「0 token 待估算」就会静默抹掉这些行的成本。`undefined` 表示「后端不拆分」，
// 消费方退回用完整 token 计数——即旧行为本身。来自当前后端的真实 0 表示「这里全部
// 已计价」，是另一回事，必须保持可区分。
const CostSplitShape = {
  cost_usd_ticks: z.number().optional(),
  uncosted_input_tokens: z.number().optional(),
  uncosted_output_tokens: z.number().optional(),
  uncosted_cache_read_tokens: z.number().optional(),
  uncosted_cache_write_tokens: z.number().optional(),
};

// 按日聚合的用量行。
const DashboardUsageDailySchema = z.object({
  date: z.string().default(""),
  provider: z.string().default(""),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  ...CostSplitShape,
  task_count: z.number().default(0),
}).loose();

/** 校验「按日用量」列表（直接是数组）。 */
export const DashboardUsageDailyListSchema = z.array(DashboardUsageDailySchema);

// 按 agent 聚合的用量行。
const DashboardUsageByAgentSchema = z.object({
  agent_id: z.string().default(""),
  provider: z.string().default(""),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  ...CostSplitShape,
  task_count: z.number().default(0),
}).loose();

/** 校验「按 agent 用量」列表（直接是数组）。 */
export const DashboardUsageByAgentListSchema = z.array(DashboardUsageByAgentSchema);

// `cancelled_count` 默认 0：指向旧后端的已安装客户端仍能渲染——那些行只是没有
// cancelled 段，而这正是该后端当时测得的真实情况。
const DashboardAgentRunTimeSchema = z.object({
  agent_id: z.string().default(""),
  total_seconds: z.number().default(0),
  task_count: z.number().default(0),
  metered_task_count: z.number().optional().catch(undefined),
  failed_count: z.number().default(0),
  cancelled_count: z.number().default(0),
}).loose();

/** 校验「按 agent 运行时长」列表（直接是数组）。 */
export const DashboardAgentRunTimeListSchema = z.array(DashboardAgentRunTimeSchema);

// 按日聚合的运行时长行。
const DashboardRunTimeDailySchema = z.object({
  date: z.string().default(""),
  total_seconds: z.number().default(0),
  task_count: z.number().default(0),
  failed_count: z.number().default(0),
  cancelled_count: z.number().default(0),
}).loose();

/** 校验「按日运行时长」列表（直接是数组）。 */
export const DashboardRunTimeDailyListSchema = z.array(DashboardRunTimeDailySchema);

// 失败聚合。`failure_reason` 刻意保持开放字符串——它携带后端权威分类，而该分类会随新
// 分类规则（server/pkg/taskfailure）不断增长。若把它钉死成 z.enum，已安装的桌面客户端
// 会因一个它构建时还不存在的 reason 丢掉整行；客户端把不认识的 reason 折叠进
// "other" 展示类。空字符串是成功桶，所以 `.default("")` 只有对已丢失 reason 的行才有
// 意义——这样的行落入分母，而不是凭空造出一个从未发生的失败。
const DashboardFailureDailySchema = z.object({
  date: z.string().default(""),
  failure_reason: z.string().default(""),
  task_count: z.number().default(0),
}).loose();

/** 校验「按日失败」列表（直接是数组）。 */
export const DashboardFailureDailyListSchema = z.array(DashboardFailureDailySchema);

// 按 agent 聚合的失败行。
const DashboardFailureByAgentSchema = z.object({
  agent_id: z.string().default(""),
  failure_reason: z.string().default(""),
  task_count: z.number().default(0),
}).loose();

/** 校验「按 agent 失败」列表（直接是数组）。 */
export const DashboardFailureByAgentListSchema = z.array(
  DashboardFailureByAgentSchema,
);

// ---------------------------------------------------------------------------
// Runtime usage schema —— runtime 详情页的四个用量端点（`/api/runtimes/:id/usage*`）。
// 与上方仪表盘 schema 相同的宽松规则：数字默认 0、字符串默认 ""、未知字段由
// `.loose()` 透传。
// ---------------------------------------------------------------------------

// 一条按日聚合的 runtime 用量行。
const RuntimeUsageSchema = z.object({
  runtime_id: z.string().default(""),
  date: z.string().default(""),
  provider: z.string().default(""),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  ...CostSplitShape,
}).loose();

/** 校验 runtime 用量列表（直接是数组）。 */
export const RuntimeUsageListSchema = z.array(RuntimeUsageSchema);

// 一小时的活跃数（按小时分桶）。
const RuntimeHourlyActivitySchema = z.object({
  hour: z.number().default(0),
  count: z.number().default(0),
}).loose();

/** 校验「按小时活跃」列表（直接是数组）。 */
export const RuntimeHourlyActivityListSchema = z.array(RuntimeHourlyActivitySchema);

// 一条按 agent 聚合的 runtime 用量行。
const RuntimeUsageByAgentSchema = z.object({
  agent_id: z.string().default(""),
  provider: z.string().default(""),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  ...CostSplitShape,
  task_count: z.number().default(0),
}).loose();

/** 校验「runtime 按 agent 用量」列表（直接是数组）。 */
export const RuntimeUsageByAgentListSchema = z.array(RuntimeUsageByAgentSchema);

// 一条按小时聚合的 runtime 用量行。
const RuntimeUsageByHourSchema = z.object({
  hour: z.number().default(0),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  ...CostSplitShape,
  task_count: z.number().default(0),
}).loose();

/** 校验「runtime 按小时用量」列表（直接是数组）。 */
export const RuntimeUsageByHourListSchema = z.array(RuntimeUsageByHourSchema);

// ---------------------------------------------------------------------------
// Agent 任务响应。基础对象保持宽松，daemon / runtime 的字段可以漂移，而任务列表
// 的消费方仍能校验它们渲染的字段。
// ---------------------------------------------------------------------------

// 人工归因（MUL-4302 §9）：一次 agent 运行向谁负责、那个人是如何被解析出来的。
// 每个字段都有防御：已离开的成员、自动驾驶运行（无发起人）、或旧后端都降级为
// 部分对象，而不是解析失败。
const AttributionUserSchema = z.object({
  id: z.string().default(""),
  name: z.string().optional(),
  email: z.string().optional(),
  avatar_url: z.string().optional(),
}).loose();

// 任务证据引用：kind + 引用的实体 id。
const TaskEvidenceSchema = z.object({
  kind: z.string().default(""),
  ref_id: z.string().default(""),
}).loose();

// 任务归因：来源、是否精确、发起者、发起人、证据、规则版本，以及委托/重试/重跑关系。
const TaskAttributionSchema = z.object({
  source: z.string().default("unattributed"),
  precise: z.boolean().default(false),
  initiator: AttributionUserSchema.optional(),
  originator: AttributionUserSchema.optional(),
  evidence: TaskEvidenceSchema.optional(),
  rule_version_id: z.string().optional(),
  delegated_from_task_id: z.string().optional(),
  retry_of_task_id: z.string().optional(),
  rerun_of_task_id: z.string().optional(),
}).loose();

// 可选字符串数组 helper：非「全字符串数组」的值归一成 undefined。
const OptionalStringArraySchema = z.preprocess(
  (value) =>
    Array.isArray(value) && value.every((item) => typeof item === "string")
      ? value
      : undefined,
  z.array(z.string()).optional(),
);

// 一次运行用量中 (provider, model) 的一个切片。token 计数默认 0 而不是让整行失败：
// 缺一个计数器的切片仍值得为已有的计数器计价，而「完全没有用量」这一情况由字段缺失
// 表达，而不是一条归零的记录。
const TaskUsageSchema = z.object({
  provider: z.string().optional(),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  cost_usd_ticks: z.number().optional(),
}).loose();

/**
 * 校验一个 agent 任务：状态、调度时间线、结果、归因、用量与投递覆盖等。
 * 基础对象宽松，daemon/runtime 字段漂移时任务列表仍可校验它渲染的字段。
 */
export const AgentTaskSchema = z.object({
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
  // 覆盖是附加的展示元数据。混合版本或部分升级的服务器不能因为一个畸形可选字段就
  // 抹掉整条执行日志，所以让该字段独立降级为「缺失」。
  coalesced_comment_ids: OptionalStringArraySchema,
  delivered_comment_ids: OptionalStringArraySchema,
  supplement_capability: z.string().optional().catch(undefined),
  supplement_comment_ids: OptionalStringArraySchema,
  can_supplement: z.boolean().optional().catch(undefined),
  trigger_summary: z.string().optional(),
  handoff_note: z.string().optional(),
  kind: z.string().optional(),
  work_dir: z.string().optional().catch(undefined),
  relative_work_dir: z.string().optional().catch(undefined),
  durable_work_dir: z.string().optional().catch(undefined),
  relative_durable_work_dir: z.string().optional().catch(undefined),
  branch_name: z.string().optional().catch(undefined),
  attribution: TaskAttributionSchema.optional(),
  // 单次运行的 token 用量。与上方 coverage 数组相同的独立降级规则：用量是附加的展示
  // 元数据，一条畸形记录只赔上该行的用量数字，不会抹掉整条执行日志。
  // `.catch(undefined)` 把坏数组折叠成「无用量记录」，UI 已用破折号渲染这种情形。
  usage: z.array(TaskUsageSchema).optional().catch(undefined),
}).loose();

// Outcome counts are required: every backend that serves this endpoint
// reports them, so a response missing them is a contract drift, not an
// older peer. `parseWithFallback` then degrades the whole list to `[]`
// rather than letting a partial window masquerade as measured outcomes.
export const AgentActivityBucketListSchema = z.array(z.object({
  agent_id: z.string(),
  bucket_at: z.string(),
  task_count: z.number().int().nonnegative(),
  failed_count: z.number().int().nonnegative(),
  completed_count: z.number().int().nonnegative(),
  cancelled_count: z.number().int().nonnegative(),
  duration_ms: z.number().nonnegative().optional().catch(undefined),
  duration_count: z.number().int().nonnegative().optional().catch(undefined),
}).loose());

export const AgentTaskListSchema = z.array(AgentTaskSchema);

export const AgentTaskPageSchema = z.object({
  tasks: AgentTaskListSchema,
  nextCursor: z.string().min(1).nullable(),
});

// One row of a run transcript. `output_truncated` gates a completeness claim
// the UI makes about a tool's output, so it stays `.optional()` with no
// default: a server that does not send it means "unknown", and defaulting it
// to false would turn a missing field into an assertion that the record is
// complete. `.catch(undefined)` keeps a malformed value that far too — without
// it a single bad boolean fails its row, the array fails with it, and
// `parseWithFallback` hands the viewer an empty transcript. Degrading one
// field to "unknown" is the correct loss; deleting the run is not. Every other
// field keeps a default for the same reason.
export const TaskMessagePayloadSchema = z.object({
  id: z.string().optional(),
  output_chars: z.number().optional(),
  output_truncated: z.boolean().optional(),
  task_id: z.string(),
  issue_id: z.string().optional().default(""),
  call_id: z.string().optional().catch(undefined),
  chat_session_id: z.string().optional(),
  seq: z.number(),
  type: z.enum(["text", "thinking", "tool_use", "tool_result", "error"]),
  tool: z.string().optional(),
  content: z.string().optional(),
  input: z.record(z.string(), z.unknown()).optional(),
  output: z.string().optional(),
  created_at: z.string().optional(),
}).loose();

// 任务执行链上的一个 trace 事件。
const TaskTraceEventSchema = z.object({
  id: z.string(),
  task_id: z.string(),
  agent_id: z.string(),
  runtime_id: z.string().nullable(),
  source: z.string(),
  event_type: z.string(),
  event_name: z.string(),
  status: z.string(),
  duration_ms: z.number().nullable().optional(),
  queue_wait_ms: z.number().nullable().optional(),
  run_ms: z.number().nullable().optional(),
  total_ms: z.number().nullable().optional(),
  provider: z.string(),
  model: z.string(),
  input_tokens: z.number(),
  output_tokens: z.number(),
  cache_read_tokens: z.number(),
  cache_write_tokens: z.number(),
  failure_reason: z.string(),
  error_type: z.string(),
  metadata: z.record(z.string(), z.unknown()),
  created_at: z.string(),
}).loose();

// 一条工具调用链环节。
const ToolCallChainSchema = z.object({
  id: z.string(),
  task_id: z.string().optional(),
  tool: z.string().optional(),
  status: z.string(),
  use_seq: z.number().optional(),
  result_seq: z.number().optional(),
  input: z.record(z.string(), z.unknown()).optional(),
  output: z.string().optional(),
  duration_ms: z.number().optional(),
  failure_signal: z.boolean(),
  failure_reason: z.string().optional(),
  created_at: z.string().optional(),
  completed_at: z.string().optional(),
}).loose();

// 任务产出的一个附件。
const AgentTaskArtifactSchema = z.object({
  id: z.string(),
  filename: z.string(),
  title: z.string(),
  kind: z.string(),
  download_url: z.string(),
  markdown_url: z.string(),
  created_at: z.string(),
}).loose();

// 时间线上一个节点（任务/工具调用/审批/人工确认等）。
const IssueTimelineNodeSchema = z.object({
  root_task_id: z.string().optional(),
  node_id: z.string(),
  parent_node_id: z.string().optional(),
  node_type: z.enum([
    "agent_task",
    "squad_step",
    "tool_call",
    "evidence",
    "approval",
    "human_confirmation",
    "dispatch_wait",
    "child_issue_ref",
    "source_fetch",
    "status_change",
  ]),
  agent_id: z.string().optional(),
  agent_name: z.string().optional(),
  status: z.string(),
  started_at: z.string().optional(),
  actual_started_at: z.string().optional(),
  completed_at: z.string().optional(),
  duration_ms: z.number(),
  input_tokens: z.number(),
  output_tokens: z.number(),
  cache_read_tokens: z.number(),
  cache_write_tokens: z.number(),
  message_count: z.number(),
  agent_turn_count: z.number(),
  trace_event_count: z.number(),
  usage_unavailable_trace: z.boolean(),
  summary: z.string(),
  evidence_refs: z.array(z.record(z.string(), z.unknown())),
  artifacts: z.array(AgentTaskArtifactSchema).optional(),
}).loose();

// issue 执行树的一个节点（lazy 支持自引用递归）。
const IssueExecutionNodeSchema: z.ZodType<unknown> = z.lazy(() =>
  z.object({
    issue: IssueSchema,
    tasks: z.array(AgentTaskSchema),
    task_messages: z.array(TaskMessagePayloadSchema),
    trace_events: z.array(TaskTraceEventSchema),
    tool_call_chains: z.array(ToolCallChainSchema),
    children: z.array(IssueExecutionNodeSchema),
  }).loose(),
);

/** 校验 issue 执行树响应：根节点 + 汇总 + 可选的时间线/issue 汇总。 */
export const IssueExecutionTreeResponseSchema = z.object({
  root: IssueExecutionNodeSchema,
  summary: z.record(z.string(), z.number()),
  timeline_nodes: z.array(IssueTimelineNodeSchema).optional(),
  issue_summary: z.object({
    total_duration_ms: z.number(),
    agent_execution_duration_ms: z.number().optional(),
    human_confirmation_duration_ms: z.number().nullable().optional(),
    child_issue_wait_duration_ms: z.number().nullable().optional(),
    total_input_tokens: z.number(),
    total_output_tokens: z.number(),
    total_cache_read_tokens: z.number(),
    total_cache_write_tokens: z.number(),
    agent_turn_count: z.number(),
    failure_summary: z.string().optional(),
    acceptance_status: z.string(),
  }).loose().optional(),
}).loose();

/** 校验 issue trace 事件列表响应。 */
export const IssueTraceEventsResponseSchema = z.object({
  events: z.array(TaskTraceEventSchema),
}).loose();

/** 校验一个外部凭据档案（provider、密钥绑定方式、状态）。 */
export const ExternalCredentialProfileSchema = z.object({
  id: z.string(),
  provider: z.string(),
  name: z.string(),
  secret_binding: z.object({
    configured: z.boolean(),
    mode: z.string(),
    hint: z.string().optional(),
  }).loose(),
  status: z.string(),
  last_error: z.string().optional(),
}).loose();

/** 校验外部凭据档案列表响应。 */
export const ExternalCredentialProfileListSchema = z.object({
  profiles: z.array(ExternalCredentialProfileSchema).default([]),
}).loose();

/** 校验外部凭据连通性测试响应：状态与可能的错误。 */
export const TestExternalCredentialProfileResponseSchema = z.object({
  status: z.string(),
  last_error: z.string().optional(),
}).loose();

/** issue 执行树响应的空值兜底。 */
export const EMPTY_ISSUE_EXECUTION_TREE_RESPONSE = {
  root: {
    issue: {
      id: "",
      workspace_id: "",
      number: 0,
      identifier: "",
      title: "",
      description: null,
      status: "cancelled",
      priority: "not_important_not_urgent",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "",
      parent_issue_id: null,
      project_id: null,
      position: 0,
      stage: null,
      start_date: null,
      due_date: null,
      metadata: {},
      properties: {},
      created_at: "",
      updated_at: "",
    },
    tasks: [],
    task_messages: [],
    trace_events: [],
    tool_call_chains: [],
    children: [],
  },
  summary: {},
};

// 任务取消（POST /api/tasks/:id/cancel）直接服务于聊天恢复。其可选的消息负载必须先
// 形状完好，UI 才会把消息从缓存删除或把文本恢复到输入框。
const CancelledChatMessageSchema = z.object({
  chat_session_id: z.string(),
  message_id: z.string(),
  content: z.string(),
  restore_to_input: z.boolean().default(false),
  // 从被删除消息上摘下的附件，供恢复的草稿在重发时重新绑定。早于该字段的服务器不返回。
  attachments: z.array(AttachmentSchema).optional(),
}).loose();

/** 校验任务取消响应：任务本体 + 可选的已取消聊天消息。 */
export const CancelTaskResponseSchema = AgentTaskSchema.extend({
  cancelled_chat_message: CancelledChatMessageSchema.nullish()
    .transform((value) => value ?? undefined),
}).loose();

// 会话最后一条消息的摘要。
const ChatLastMessageSchema = z.object({
  content: z.string().default(""),
  role: z.enum(["user", "assistant"]).catch("assistant"),
  created_at: z.string().default(""),
  failure_reason: z.string().nullable().optional(),
  message_kind: z.enum([
    "message",
    "no_response",
    "onboarding_kickoff",
    "onboarding_opening",
  ]).optional().catch(undefined),
}).loose();

// 会话来源渠道（IM 集成等），用于路由展示。
const ChatChannelSourceSchema = z.object({
  channel_type: z.string().default(""),
  installation_id: z.string().default(""),
  route_revision: z.number().default(0),
}).loose();

/** 校验一个聊天会话：agent、标题、状态、未读、最后一条消息与来源渠道。 */
export const ChatSessionSchema: z.ZodType<ChatSession> = z.object({
  id: z.string(),
  workspace_id: z.string().default(""),
  agent_id: z.string().default(""),
  creator_id: z.string().default(""),
  project_id: z.string().nullable().optional(),
  title: z.string().default(""),
  status: z.enum(["active", "archived"]).catch("active"),
  has_unread: z.boolean().default(false),
  unread_count: z.number().optional(),
  last_message: ChatLastMessageSchema.nullable().optional().catch(undefined),
  pinned: z.boolean().optional(),
  channel_source: ChatChannelSourceSchema.optional().catch(undefined),
  is_current_channel_route: z.boolean().optional().catch(undefined),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

/** 聊天会话的空值兜底。 */
export const EMPTY_CHAT_SESSION: ChatSession = {
  id: "",
  workspace_id: "",
  agent_id: "",
  creator_id: "",
  title: "",
  status: "active",
  has_unread: false,
  created_at: "",
  updated_at: "",
};

/** 校验聊天会话列表：坏条目用空会话占位后过滤掉，再给默认空数组。 */
export const ChatSessionListSchema = z
  .array(ChatSessionSchema.catch(EMPTY_CHAT_SESSION))
  .transform((sessions) => sessions.filter((session) => session.id !== ""))
  .default([]);

/** 聊天会话列表的空值兜底。 */
export const EMPTY_CHAT_SESSION_LIST: ChatSession[] = [];

// 延迟取消的草稿恢复（GET /api/chat/sessions/{id}/draft-restores，#5219）直接喂给
// 输入框：`content` 成为草稿文本，`attachments` 在重发时重新绑定，`id` 是消费键。
// 畸形响应退回空列表——持久化行仍在服务端待处理，跳过这次拉取不会丢任何东西。
const ChatDraftRestoreSchema = z.object({
  id: z.string(),
  chat_session_id: z.string(),
  task_id: z.string().optional(),
  content: z.string().default(""),
  attachments: z.array(AttachmentSchema).optional(),
  created_at: z.string().optional(),
}).loose();

/** 校验草稿恢复响应：restores 数组。 */
export const ChatDraftRestoresResponseSchema = z.object({
  restores: z.array(ChatDraftRestoreSchema).default([]),
}).loose();

// 排队中的一个聊天任务。
const ChatQueuedTaskSchema = z.object({
  task_id: z.string(),
  status: z.string().default("queued"),
  created_at: z.string().default(""),
  message_id: z.string().optional(),
  content: z.string().optional(),
}).loose();

// 排队任务列表：逐个校验，坏行丢弃。
const ChatQueuedTasksSchema = z.array(z.unknown()).transform((tasks) =>
  tasks.flatMap((task) => {
    const parsed = ChatQueuedTaskSchema.safeParse(task);
    return parsed.success ? [parsed.data] : [];
  }),
);

// 根字段保留旧的单任务响应形状。附加字段保持可选，让调用方能区分「旧服务器」与
// 「空队列」。畸形的队列行被忽略，不会把合法的队头一起丢掉。
export const ChatPendingTaskSchema: z.ZodType<ChatPendingTask> = z.object({
  task_id: z.string().optional(),
  status: z.string().optional(),
  created_at: z.string().optional(),
  supports_queue: z.boolean().optional(),
  queued_tasks: ChatQueuedTasksSchema.optional(),
}).loose();

/** 待处理聊天任务的空值兜底。 */
export const EMPTY_CHAT_PENDING_TASK: ChatPendingTask = {};

/** 校验发送聊天消息的响应：消息 id 与任务 id 必须非空。 */
export const SendChatMessageResponseSchema: z.ZodType<SendChatMessageResponse> = z.object({
  message_id: z.string().min(1),
  task_id: z.string().min(1),
  supports_queue: z.boolean().optional(),
  queued: z.boolean().optional().catch(undefined),
  created_at: z.string().min(1),
  attachment_ids: z.array(z.string()).nullish().transform((ids) => ids ?? undefined),
}).loose();

/** 校验「把排队聊天任务提前」的响应。 */
export const PrioritizeQueuedChatTaskResponseSchema:
  z.ZodType<PrioritizeQueuedChatTaskResponse> = z.object({
    task_id: z.string(),
    active_task_id: z.string().optional(),
  }).loose();

/** 提前任务的空值兜底。 */
export const EMPTY_PRIORITIZE_QUEUED_CHAT_TASK_RESPONSE:
  PrioritizeQueuedChatTaskResponse = { task_id: "" };

/** 草稿恢复响应的空值兜底。 */
export const EMPTY_CHAT_DRAFT_RESTORES: ChatDraftRestoresResponse = {
  restores: [],
};

/** 取消任务响应的空值兜底。 */
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

/** 校验 Agent Builder 会话的建立响应：三个 id 均非空。 */
export const AgentBuilderSessionSchema = z.object({
  session_id: z.string(),
  builder_agent_id: z.string(),
  runtime_id: z.string(),
}).loose();

/** Agent Builder 会话的空值兜底。 */
export const EMPTY_AGENT_BUILDER_SESSION: AgentBuilderSession = {
  session_id: "",
  builder_agent_id: "",
  runtime_id: "",
};

/**
 * 校验创建会话中已填写的配置。每个字段各自独立回退为空值：较新构建写出的草稿
 * （或在传输中被截断的草稿）也必须能恢复它认识的字段，而不是把用户的创作整个丢弃。
 */
export const StoredAgentDraftSchema = z.object({
  name: z.string().catch(""),
  description: z.string().catch(""),
  instructions: z.string().catch(""),
  conversation_starters: z
    .array(
      z.object({
        label: z.string().catch(""),
        prompt: z.string().catch(""),
      }),
    )
    .catch([]),
  avatar_url: z.string().nullable().catch(null),
  model: z.string().catch(""),
  thinking_level: z.string().catch(""),
  service_tier: z.string().catch(""),
  skill_ids: z.array(z.string()).catch([]),
  permission_scope: z
    .enum(["private", "workspace", "members"])
    .catch("private"),
  member_ids: z.array(z.string()).catch([]),
  team_ids: z.array(z.string()).catch([]),
  applied_message_id: z.string().nullable().catch(null),
}).loose();

/**
 * 校验一个未完成的创建草稿（会话摘要）。除 id 外每个字段都有安全默认：旧服务器省略
 * `runtime_id` 时应退化成「让用户自己选」，而不是丢掉整行、丢失对话。
 */
export const AgentBuilderSessionSummarySchema = z.object({
  session_id: z.string(),
  title: z.string().catch(""),
  runtime_id: z.string().catch(""),
  created_at: z.string().catch(""),
  updated_at: z.string().catch(""),
  last_message_content: z.string().catch(""),
  last_message_role: z.string().catch(""),
  last_message_at: z.string().catch(""),
  // 用户从未手改过的会话没有此字段；此时客户端重放最近的 <agent_draft> 块而不是
  // 恢复存储的副本。
  draft: StoredAgentDraftSchema.nullish().catch(null),
}).loose();

/** 校验 Agent Builder 会话列表响应。 */
export const AgentBuilderSessionListSchema = z.object({
  sessions: z.array(AgentBuilderSessionSummarySchema).catch([]),
}).loose();

/** Agent Builder 会话列表的空值兜底。 */
export const EMPTY_AGENT_BUILDER_SESSION_LIST: {
  sessions: AgentBuilderSessionSummary[];
} = { sessions: [] };

/** 校验「切换 Builder 运行时」响应的形状：返回目标 runtime id。 */
export const AgentBuilderRuntimeSwitchSchema = z.object({
  runtime_id: z.string(),
}).loose();

// 该端点只有在载体已绑定到调用方要求的 runtime 后才返回 2xx；其余都是抛错、无提交。
// 所以无法解析的 SUCCESS 体的安全兜底是「请求的 id」而不是空 id：绑定确实发生了，
// 若报告「未知」会让选择器停在已不再执行的 runtime 上——而这个端点正是为了消除这个
// 落差而存在的。
export const agentBuilderRuntimeSwitchFallback = (
  requestedRuntimeID: string,
): AgentBuilderRuntimeSwitch => ({ runtime_id: requestedRuntimeID });

// Squad 列表响应携带轻量的成员预览，供悬浮卡片使用。预览字段是附加的 API 字段，
// 旧后端能干净地默认成无预览，而不是弄坏较新的前端。
const SquadMemberPreviewSchema = z.object({
  member_type: z.string(),
  member_id: z.string(),
  role: z.string().default(""),
}).loose();

/** 校验一个 Squad（战队）：描述、指令、负责人、成员数、成员预览等。 */
export const SquadSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  name: z.string(),
  description: z.string().default(""),
  instructions: z.string().default(""),
  system_key: z.string().optional(),
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

/** 校验 Squad 列表（直接是数组）。 */
export const SquadListSchema = z.array(SquadSchema);

/** Squad 列表的空值兜底。 */
export const EMPTY_SQUAD_LIST: Squad[] = [];

/** 单个 Squad 的空值兜底。 */
export const EMPTY_SQUAD: Squad = {
  id: "",
  workspace_id: "",
  name: "",
  description: "",
  instructions: "",
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

// Squad 成员状态——支撑 Squad 详情页的 Members 页签。status 是 `string | null`
// （不是窄化的 `SquadMemberStatusValue` 联合），新服务端状态不会让解析失败；UI 对
// 未知值默认渲染中性的胶囊徽章。
const SquadActiveIssueBriefSchema = z.object({
  issue_id: z.string(),
  identifier: z.string(),
  title: z.string(),
  issue_status: z.string(),
}).loose();

// 成员当前状态行：在线状态 + 正在处理的 issue 简报。
const SquadMemberStatusSchema = z.object({
  member_type: z.string(),
  member_id: z.string(),
  status: z.string().nullable().optional().transform((v) => v ?? null),
  active_issues: z.array(SquadActiveIssueBriefSchema).default([]),
  last_active_at: z.string().nullable().optional().transform((v) => v ?? null),
}).loose();

/** 校验 Squad 成员状态列表响应。 */
export const SquadMemberStatusListResponseSchema = z.object({
  members: z.array(SquadMemberStatusSchema).default([]),
}).loose();

/** Squad 成员状态列表的空值兜底。 */
export const EMPTY_SQUAD_MEMBER_STATUS_LIST = { members: [] };

// ---------------------------------------------------------------------------
// 结构化错误体 —— POST /api/workspaces/:wsId/issues 的 409 冲突。
//
// 当服务器在相同 workspace 检测到同标题的活跃 issue 时，返回
// `{ code: "active_duplicate_issue", error, issue }` 而不是放行创建。UI 用内嵌的
// issue 引用提供「查看已有」入口，而不是把用户丢进泛泛的「创建失败」toast。
//
// 严格保证：
//   - `code` 是字面量，未来服务器改名（如 `duplicate_issue`）会让解析失败并退回
//     普通错误 toast——契约漂移永远不能以「坏掉的重复卡片 UI」的形式上线。
//   - `issue` 必填；没有 id/identifier/title 时「查看已有」按钮没有可指向的对象，
//     所以宁可退回也不能猜。
//   - `issue.status` 有意省略：重复 toast 不渲染 StatusIcon（它对未知枚举值没有
//     兜底），未来服务端重命名 `status` 也不能把这个分支打掉。`.loose()` 让该字段
//     对其他消费方原样透传。
// ---------------------------------------------------------------------------

/** 校验「创建 issue 撞到活跃重复项」的错误体。 */
export const DuplicateIssueErrorBodySchema = z.object({
  code: z.literal("active_duplicate_issue"),
  error: z.string().optional(),
  issue: z.object({
    id: z.string(),
    identifier: z.string(),
    title: z.string(),
  }).loose(),
}).loose();

// A destructive confirmation must fail closed on an incomplete plan. Do not
// default missing arrays to empty or silently discard malformed entries.
export const RuntimeAccessRevocationPlanSchema = z.object({
  code: z.enum(["runtime_has_nonowner_dependents", "runtime_access_revocation_plan_changed"]),
  nonowner_agents: z.array(z.object({ id: z.string().uuid(), name: z.string() })),
  active_task_ids: z.array(z.string().uuid()),
}).refine((body) =>
  new Set(body.nonowner_agents.map((agent) => agent.id)).size === body.nonowner_agents.length &&
  new Set(body.active_task_ids).size === body.active_task_ids.length,
).transform((body) => ({ agents: body.nonowner_agents, activeTaskIds: body.active_task_ids }));

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
// Webhook 投递记录 schema —— 支撑 Autopilot 投递区。枚举（`status`、
// `signature_status`、`provider`）保持 `z.string()`：未来的服务端取值（例如
// Stripe provider、新的去重状态）退化成通用 UI 兜底，而不是把整个列表塌成空数组。
// `.loose()` 让未知字段透传，与文件内其他端点规则一致。
// ---------------------------------------------------------------------------

// 一条 webhook 投递记录。
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
  // 旧服务器早于「持久化派发队列」。默认值保证兼容，同时 UI 与新 worker 一起逐步上线。
  dispatch_attempts: z.number().default(0),
  available_at: z.string().default(""),
  content_type: z.string().nullable(),
  response_status: z.number().nullable(),
  autopilot_run_id: z.string().nullable(),
  replayed_from_delivery_id: z.string().nullable(),
  error: z.string().nullable(),
  reason_code: z.string().nullable().default(null),
  replay_idempotency_key: z.string().nullable().default(null),
  received_at: z.string(),
  last_attempt_at: z.string(),
  created_at: z.string(),
  // 仅详情端点返回的字段：列表端点省略，详情端点填充 raw_body / selected_headers /
  // response_body。
  selected_headers: z.record(z.string(), z.unknown()).nullable().optional(),
  raw_body: z.string().nullable().optional(),
  response_body: z.string().nullable().optional(),
}).loose();

/** 校验 webhook 投递列表响应：投递记录 + 总数。 */
export const ListWebhookDeliveriesResponseSchema = z.object({
  deliveries: z.array(WebhookDeliverySchema).default([]),
  total: z.number().default(0),
}).loose();

/** 校验单条 webhook 投递详情（与列表行同形状）。 */
export const WebhookDeliveryResponseSchema = WebhookDeliverySchema;

/** webhook 投递列表的空值兜底。 */
export const EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE: ListWebhookDeliveriesResponse = {
  deliveries: [],
  total: 0,
};

// ---------------------------------------------------------------------------
// Autopilot 列表 schema。枚举（`status`、`execution_mode`、`trigger_kinds`、
// `last_run_status`）保持 `z.string()`，未来的服务端取值退化为通用 UI 兜底。
// 三个派生字段（trigger_kinds / next_run_at / last_run_status）仅列表端点返回，
// 旧服务器没有它们——按契约可选，列表没有时渲染 "—"。
// ---------------------------------------------------------------------------

// Autopilot 列表行。
const AutopilotListItemSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  title: z.string(),
  description: z.string().nullable().optional(),
  project_id: z.string().nullable().optional(),
  // 旧服务器（pre-MUL-2429）省略 assignee_type；文档化默认值是 "agent"。
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
  // 按调用方而异的写权限；旧服务器缺失（按未知处理）。
  can_write: z.boolean().optional(),
  // 更窄的按调用方访问管理能力（仅详情端点）。
  can_manage_access: z.boolean().optional(),
}).loose();

/** 校验 Autopilot 列表响应。 */
export const ListAutopilotsResponseSchema = z.object({
  autopilots: z.array(AutopilotListItemSchema).default([]),
  total: z.number().default(0),
}).loose();

/** Autopilot 列表的空值兜底。 */
export const EMPTY_LIST_AUTOPILOTS_RESPONSE = {
  autopilots: [],
  total: 0,
};

// Autopilot run（POST /trigger、GET /runs）。被「立即运行」流程消费，它按 `status`
// 分支以避免假成功 toast（MUL-4525），所以响应必须经 schema 解析。`reason_code` 是
// 附加的稳定分类，供 UI 本地化非成功运行；旧服务器省略它。默认值保守：读不懂的运行
// 降级为非成功状态，UI 永不展示它无法确认的成功。.loose() 容忍新字段。
export const AutopilotRunSchema = z.object({
  id: z.string().default(""),
  autopilot_id: z.string().default(""),
  trigger_id: z.string().nullable().default(null),
  source: z.string().default("manual"),
  status: z.string().default("failed"),
  issue_id: z.string().nullable().default(null),
  task_id: z.string().nullable().default(null),
  triggered_at: z.string().default(""),
  completed_at: z.string().nullable().default(null),
  failure_reason: z.string().nullable().default(null),
  reason_code: z.string().optional(),
  trigger_payload: z.unknown().default(null),
  result: z.unknown().default(null),
  created_at: z.string().default(""),
}).loose();

/** Autopilot run 的空值兜底。 */
export const FALLBACK_AUTOPILOT_RUN: AutopilotRun = {
  id: "",
  autopilot_id: "",
  trigger_id: null,
  source: "manual",
  status: "failed",
  issue_id: null,
  task_id: null,
  triggered_at: "",
  completed_at: null,
  failure_reason: null,
  trigger_payload: null,
  result: null,
  created_at: "",
};

// Cron 预览：服务器是下一次执行的权威来源。这里不用 `.default([])`——缺失或被改形的
// 字段必须让校验失败，从而退到 `next_runs: null` 兜底（「预览不可读」），而不是伪装成
// 合法的空列表（「这个表达式永不触发」）。
export const CronPreviewResponseSchema = z.object({
  next_runs: z.array(z.string()),
}).loose();

/** Cron 预览不可读时的空值兜底。 */
export const UNREADABLE_CRON_PREVIEW_RESPONSE: CronPreviewResponse = {
  next_runs: null,
};

/** 单条 webhook 投递记录的空值兜底。 */
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
  dispatch_attempts: 0,
  available_at: "",
  content_type: null,
  response_status: null,
  autopilot_run_id: null,
  replayed_from_delivery_id: null,
  error: null,
  reason_code: null,
  replay_idempotency_key: null,
  received_at: "",
  last_attempt_at: "",
  created_at: "",
};

// ---------------------------------------------------------------------------
// User（/api/me GET + PATCH）。auth store 和「设置 → 账号」都信任此形状——这里漂移
// 会同时打掉两个界面。按 IssueSchema 同样的规则保持宽松：枚举保持 `z.string()`、
// 可空字段并上 `null`、未知服务端字段由 `.loose()` 透传。`profile_description` 是
// MUL-2406 新增的字段；服务器未设置时发出 `""`（NOT NULL DEFAULT ''），schema 也
// 默认 `""`——类型保持紧凑，同时不弄坏还没返回该列的旧后端。
// ---------------------------------------------------------------------------

/** 校验用户档案（/api/me）。 */
export const UserSchema = z.object({
  id: z.string(),
  name: z.string().default(""),
  email: z.string().default(""),
  avatar_url: z.string().nullable().default(null),
  onboarded_at: z.string().nullable().default(null),
  onboarding_questionnaire: z.record(z.string(), z.unknown()).default({}),
  starter_content_state: z.string().nullable().default(null),
  profile_description: z.string().default(""),
  timezone: z.string().nullable().default(null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

/** 用户档案的空值兜底。 */
export const EMPTY_USER: User = {
  id: "",
  name: "",
  account: "",
  email: "",
  avatar_url: null,
  onboarded_at: null,
  onboarding_questionnaire: {},
  starter_content_state: null,
  profile_description: "",
  timezone: null,
  created_at: "",
  updated_at: "",
};

// ---------------------------------------------------------------------------
// 跨 workspace 的未读收件箱汇总（/api/inbox/unread-summary GET）。用户所属的每个有
// 未读项的 workspace 一条；侧边栏由此推导 workspace 切换器的小圆点。按通常规则宽松，
// 未来的字段追加不会弄丢圆点——畸形 JSON 时 parseWithFallback 返回空列表，只是隐藏圆点。
// ---------------------------------------------------------------------------

/** 校验跨 workspace 未读汇总（直接是数组）。 */
export const InboxUnreadSummarySchema = z.array(
  z
    .object({
      workspace_id: z.string(),
      count: z.number(),
    })
    .loose(),
);

/** 跨 workspace 未读汇总的空值兜底。 */
export const EMPTY_INBOX_UNREAD_SUMMARY: InboxWorkspaceUnread[] = [];

// ---------------------------------------------------------------------------
// 收件箱项（/api/inbox 与 /api/inbox/archived GET）。
// 按通常规则宽松：`severity` / `type` / `recipient_type` 保持 `z.string()`，本客户端
// 还不认识的通知类型仍能解析并渲染（UI 的类型标签查找已容忍未知类型）。可空的可选字段
// 也声明为可选，旧行可能整体省略它们。畸形 JSON 时 parseWithFallback 返回空列表——
// 受影响视图显示为空而不是让收件箱白屏。两个端点共用这个边界：它们返回同样的行形状，
// 都喂给状态/优先级筛选 UI。
// ---------------------------------------------------------------------------

/** 校验收件箱项列表（直接是数组）。 */
export const InboxItemListSchema = z.array(
  z
    .object({
      id: z.string(),
      workspace_id: z.string(),
      recipient_type: z.string(),
      recipient_id: z.string(),
      type: z.string(),
      severity: z.string(),
      issue_id: z.string().nullish(),
      title: z.string(),
      body: z.string().nullish(),
      issue_status: z.string().nullish(),
      issue_priority: z.string().nullish(),
      read: z.boolean(),
      archived: z.boolean(),
      created_at: z.string(),
    })
    .loose(),
);

export const ArchivedInboxPageSchema = z.object({
  items: InboxItemListSchema,
  next_cursor: z.string().min(1).nullable(),
  has_more: z.boolean(),
}).refine((page) => page.has_more === (page.next_cursor !== null) &&
  (!page.has_more || page.items.length > 0))
  .transform((page) => ({ items: page.items, nextCursor: page.next_cursor, hasMore: page.has_more }));

export const ArchivedInboxFacetsSchema = z.object({
  statuses: z.record(z.string(), z.number().int().nonnegative()),
  priorities: z.record(z.string(), z.number().int().nonnegative()),
  actors: z.record(z.string(), z.number().int().nonnegative()),
  unread_count: z.number().int().nonnegative(),
}).transform((facets) => ({
  statuses: facets.statuses, priorities: facets.priorities,
  actors: facets.actors, unreadCount: facets.unread_count,
}));

export const EMPTY_INBOX_ITEMS: InboxItem[] = [];

// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Runtime 模型发现（POST /api/runtimes/:id/models、
// GET /api/runtimes/:id/models/:requestId）。两个端点返回同一份请求记录，UI 按
// `status` 驱动状态机，所以决定行为的两个字段被钉死：`status` 门控轮询循环，
// `supported` 决定选择器是否可用。其余按文件顶部规则保持宽松。
//
// `status` 刻意保持 `z.string()`（新服务器可能加状态）；`resolveRuntimeModels`
// 把任何它不认识的值当作显式失败，而不是「已完成但目录为空」。`supported` 默认 true，
// 旧到省略该字段的服务器也让选择器保持可用，而不是因为 `undefined` 渲染成
// "managed by runtime"。
//
// `cached` / `cached_at` 是「快照来自服务端目录缓存」的附加标记（MUL-5444）；
// 旧后端省略它们。
// ---------------------------------------------------------------------------

// 思考等级选项。
const RuntimeModelThinkingLevelSchema = z.object({
  value: z.string(),
  label: z.string().default(""),
  description: z.string().optional(),
}).loose();

// 思考能力：支持的等级 + 默认等级。
const RuntimeModelThinkingSchema = z.object({
  supported_levels: z.array(RuntimeModelThinkingLevelSchema).default([]),
  default_level: z.string().optional(),
}).loose();

// 服务档位。
const RuntimeModelServiceTierSchema = z.object({
  id: z.string(),
  name: z.string().default(""),
  description: z.string().optional(),
}).loose();

// 没有 `id` 的模型条目不可选——`onChange(m.id)` 会持久化一个空模型——所以 `id` 必填，
// 畸形条目把整个响应丢到兜底，而不是渲染一行死数据。
const RuntimeModelSchema = z.object({
  id: z.string(),
  label: z.string().default(""),
  provider: z.string().optional(),
  default: z.boolean().optional(),
  thinking: RuntimeModelThinkingSchema.nullable().optional()
    .transform((v) => v ?? undefined),
  service_tiers: z.array(RuntimeModelServiceTierSchema).optional(),
  supports_explicit_standard_service_tier: z.boolean().optional(),
}).loose();

// A row the runtime named but will not run (MUL-6961). Parsed from its own
// top-level list, never from `models`, so nothing here can become a selectable
// value. `id` is required for the same reason it is on RuntimeModelSchema — a
// row without one cannot even be keyed in a list.
const RuntimeUnavailableModelSchema = z.object({
  id: z.string(),
  label: z.string().default(""),
  reason: z.string().optional(),
}).loose();

export const RuntimeModelListRequestSchema = z.object({
  id: z.string().default(""),
  runtime_id: z.string().default(""),
  status: z.string(),
  models: z.array(RuntimeModelSchema).optional(),
  // Absent on any daemon or server older than the field, which simply means
  // the picker shows no unavailable section.
  unavailable_models: z.array(RuntimeUnavailableModelSchema).optional(),
  supported: z.boolean().default(true),
  error: z.string().optional(),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
  cached: z.boolean().optional(),
  cached_at: z.string().optional(),
}).loose();

// 模型发现响应不可解析时的兜底。`failed` 是唯一诚实的选择：`completed` 会伪造一个空
// 目录（`supported` 被读成 false 时还会静默清掉已保存的模型），而 `pending` 会让选择器
// 空转到客户端轮询超时。`failed` 立即亮出「发现失败」，同时保留可手动输入的自定义入口，
// 与真实发现失败的降级一致。
export const MALFORMED_RUNTIME_MODEL_LIST_REQUEST: RuntimeModelListRequest = {
  id: "",
  runtime_id: "",
  status: "failed",
  supported: true,
  error: "invalid model discovery response",
  created_at: "",
  updated_at: "",
};

/** 校验一个钉钉集成安装实例（绑定 agent、状态、绑定成员）。 */
export const DingTalkInstallationSchema = z.object({
  id: z.string(),
  workspace_id: z.string().default(""),
  agent_id: z.string().default(""),
  installer_user_id: z.string().default(""),
  status: z.string().default("revoked"),
  installed_at: z.string().default(""),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
  agent_available: z.boolean().optional(),
  bound_dingtalk_user_ids: z.array(z.string()).catch([]).default([]),
}).loose();

/** 钉钉安装实例的空值兜底。 */
export const EMPTY_DINGTALK_INSTALLATION: DingTalkInstallation = {
  id: "",
  workspace_id: "",
  agent_id: "",
  installer_user_id: "",
  status: "revoked",
  installed_at: "",
  created_at: "",
  updated_at: "",
  bound_dingtalk_user_ids: [],
};

/** 校验钉钉安装列表响应：安装实例 + 配置/安装能力标记。 */
export const ListDingTalkInstallationsResponseSchema = z.object({
  installations: z.array(DingTalkInstallationSchema).default([]),
  configured: z.boolean().default(false),
  install_supported: z.boolean().optional(),
}).loose();

/** 钉钉安装列表的空值兜底。 */
export const EMPTY_LIST_DINGTALK_INSTALLATIONS_RESPONSE: ListDingTalkInstallationsResponse = {
  installations: [],
  configured: false,
};

/** 校验群内某个钉钉机器人的状态。 */
export const DingTalkGroupBotSchema = z.object({
  installation_id: z.string().default(""),
  agent_id: z.string().default(""),
  bot_name: z.string().default(""),
  bot_identity_issue: z.string().default(""),
  last_active_at: z.string().optional(),
  mention_count: z.number().int().nonnegative().optional(),
}).loose();

/** 校验一个钉钉群：会话 id、标题与其内机器人。 */
export const DingTalkGroupSchema = z.object({
  conversation_id: z.string(),
  conversation_title: z.string().default(""),
  bots: z.array(DingTalkGroupBotSchema).catch([]).default([]),
}).loose();

/** 校验钉钉群列表响应。 */
export const ListDingTalkGroupsResponseSchema = z.object({
  groups: z.array(DingTalkGroupSchema).default([]),
  group_discovery_supported: z.boolean().default(false),
  inactive_group_counts: z.record(z.string(), z.number().int().nonnegative()).optional(),
  bot_identities: z.record(z.string(), DingTalkGroupBotSchema).optional(),
  next_offset: z.number().int().nonnegative().optional(),
}).loose();

/** 钉钉群列表的空值兜底。 */
export const EMPTY_LIST_DINGTALK_GROUPS_RESPONSE: ListDingTalkGroupsResponse = {
  groups: [],
  group_discovery_supported: false,
};

/** 校验钉钉绑定 token 兑换响应（返回 workspace/安装/用户 id）。 */
export const RedeemDingTalkBindingTokenResponseSchema = z.object({
  workspace_id: z.string().default(""),
  installation_id: z.string().default(""),
  dingtalk_user_id: z.string().default(""),
}).loose();

/** 钉钉绑定兑换响应的空值兜底。 */
export const EMPTY_REDEEM_DINGTALK_BINDING_TOKEN_RESPONSE: RedeemDingTalkBindingTokenResponse = {
  workspace_id: "",
  installation_id: "",
  dingtalk_user_id: "",
};

// 企业微信智能机器人（aibot）安装响应。`.loose()` 让新后端字段不会在旧桌面构建上让解析
// 失败（见 CLAUDE.md → API Compatibility）。默认值保证畸形响应安全降级：`configured`
// 默认 false（渲染「找你的管理员」状态，而不是一个提交必然失败的连接对话框），缺失的
// `status` 默认 "revoked" 而非 "active"，坏掉的读取绝不会把未必在线的机器人显示成已连接。
export const WecomInstallationSchema = z.object({
  id: z.string(),
  workspace_id: z.string().default(""),
  agent_id: z.string().default(""),
  bot_id: z.string().default(""),
  installer_user_id: z.string().default(""),
  status: z.string().default("revoked"),
}).loose();

/** 企业微信安装实例的空值兜底。 */
export const EMPTY_WECOM_INSTALLATION: WecomInstallation = {
  id: "",
  workspace_id: "",
  agent_id: "",
  bot_id: "",
  installer_user_id: "",
  status: "revoked",
};

/** 校验企业微信安装列表响应。 */
export const ListWecomInstallationsResponseSchema = z.object({
  installations: z.array(WecomInstallationSchema).default([]),
  configured: z.boolean().default(false),
  install_supported: z.boolean().optional(),
}).loose();

/** 企业微信安装列表的空值兜底。 */
export const EMPTY_LIST_WECOM_INSTALLATIONS_RESPONSE: ListWecomInstallationsResponse = {
  installations: [],
  configured: false,
};

/** 校验企业微信绑定 token 兑换响应。 */
export const RedeemWecomBindingTokenResponseSchema = z.object({
  workspace_id: z.string().default(""),
  installation_id: z.string().default(""),
  wecom_user_id: z.string().default(""),
}).loose();

/** 企业微信绑定兑换响应的空值兜底。 */
export const EMPTY_REDEEM_WECOM_BINDING_TOKEN_RESPONSE: RedeemWecomBindingTokenResponse = {
  workspace_id: "",
  installation_id: "",
  wecom_user_id: "",
};

/** 校验一个 Telegram 集成安装实例。 */
export const TelegramInstallationSchema = z.object({
  id: z.string(),
  workspace_id: z.string().default(""),
  agent_id: z.string().default(""),
  bot_id: z.string().default(""),
  bot_username: z.string().default(""),
  installer_user_id: z.string().default(""),
  status: z.string().default("revoked"),
  installed_at: z.string().default(""),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

/** Telegram 安装实例的空值兜底。 */
export const EMPTY_TELEGRAM_INSTALLATION: TelegramInstallation = {
  id: "",
  workspace_id: "",
  agent_id: "",
  bot_id: "",
  bot_username: "",
  installer_user_id: "",
  status: "revoked",
  installed_at: "",
  created_at: "",
  updated_at: "",
};

/** 校验 Telegram 安装列表响应。 */
export const ListTelegramInstallationsResponseSchema = z.object({
  installations: z.array(TelegramInstallationSchema).default([]),
  configured: z.boolean().default(false),
  install_supported: z.boolean().optional(),
}).loose();

/** Telegram 安装列表的空值兜底。 */
export const EMPTY_LIST_TELEGRAM_INSTALLATIONS_RESPONSE: ListTelegramInstallationsResponse = {
  installations: [],
  configured: false,
};

/** 校验 Telegram 绑定 token 兑换响应。 */
export const RedeemTelegramBindingTokenResponseSchema = z.object({
  workspace_id: z.string().default(""),
  installation_id: z.string().default(""),
  telegram_user_id: z.string().default(""),
}).loose();

/** Telegram 绑定兑换响应的空值兜底。 */
export const EMPTY_REDEEM_TELEGRAM_BINDING_TOKEN_RESPONSE: RedeemTelegramBindingTokenResponse = {
  workspace_id: "",
  installation_id: "",
  telegram_user_id: "",
};

// Skills。为 POST /api/skills/:id/refresh 引入（从导入源更新 skill）。`config` 保持
// 宽松记录：服务器拥有 `origin` 来源形状的所有权，可能自由扩展。
/** 校验 skill 的一个源文件。 */
export const SkillFileSchema = z.object({
 kind: z.enum(["text", "binary"]).catch("text").default("text"),
 content_type: z.string().catch("application/octet-stream").default("text/plain"),
 size_bytes: z.number().nonnegative().catch(0).default(0),
 sha256: z.string().catch("").default(""),
 mode: z.number().catch(420).default(420),
  id: z.string(),
  skill_id: z.string(),
  path: z.string(),
  content: z.string().optional().default(""),
  created_at: z.string().optional().default(""),
  updated_at: z.string().optional().default(""),
}).loose();

/** 校验一个 skill：名称、描述、正文、配置与源文件列表。 */
export const SkillSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  name: z.string(),
  description: z.string().optional().default(""),
  content: z.string().optional().default(""),
  config: z.record(z.string(), z.unknown()).optional().default({}),
  created_by: z.string().nullable().optional().default(null),
  created_at: z.string().optional().default(""),
  updated_at: z.string().optional().default(""),
  files: z.array(SkillFileSchema).optional().default([]),
}).loose();

/** skill 的空值兜底。 */
export const EMPTY_SKILL: Skill = {
  id: "",
  workspace_id: "",
  name: "",
  description: "",
  content: "",
  config: {},
  created_by: null,
  created_at: "",
  updated_at: "",
  files: [],
};

/** 校验导入时探测到的同名既有 skill：能否覆盖等。 */
export const SkillImportExistingSkillSchema = z.object({
  id: z.string(),
  name: z.string(),
  created_by: z.string().optional(),
  can_overwrite: z.boolean().optional(),
}).loose();

/**
 * POST /api/skills/import 响应的信封。
 *
 * `status` 保持普通字符串（而非枚举）：新后端增加的状态仍能解析、其 `reason` 能
 * 送达用户。这里若用 `z.enum`，未知值会让整个信封校验失败、丢掉服务器原因，只留下
 * 泛泛的「导入失败」——服务端该字段是裸 `string`，可以自由增长。`skillFromImportResult`
 * 有默认分支：created/updated 之外的任何值都按失败处理。
 */
export const SkillImportResultSchema = z.object({
  status: z.string().default("failed"),
  reason: z.string().optional().default(""),
  skill: SkillSchema.optional(),
  existing_skill: SkillImportExistingSkillSchema.optional(),
}).loose();

/** skill 导入结果的空值兜底。 */
export const EMPTY_SKILL_IMPORT_RESULT: SkillImportResult = {
  status: "failed",
  reason: "",
};

/**
 * 校验一个 workspace MCP 服务器的读取形状。
 *
 * 这是本文件中唯一**不能**用 `.loose()` 的 schema。别处保留未知字段是为了向前兼容；
 * 在这里它会成为「只写边界」的漏洞——服务器若回归到回传存储条目（或 summary 上的
 * `url`/`headers`），这些内容会落进解析对象和查询缓存。zod 默认剥离未知键，客户端
 * 于是只持有安全的摘要。
 *
 * `transport` 保持普通字符串（而非枚举），新后端的未知值仍能解析——UI 有默认分支。
 */
export const WorkspaceRepoSchema: z.ZodType<WorkspaceRepo> = z.object({
  id:z.string().optional(), revision:z.number().optional(),default_for_tasks:z.boolean().optional(),
  url: z.string(),
  description: z.string().optional(),
  provider: z.string().optional(),
  project_path: z.string().optional(),
  default_branch: z.string().optional(),
  ref: z.string().optional(),
  head_commit: z.string().optional(),
  connection_status: z.string().optional(),
  sync_status: z.string().optional(),
  test_status: z.string().optional(),
  last_tested_at: z.string().optional(),
  last_synced_at: z.string().optional(),
}).loose();

/** 校验仓库连通性探测响应的形状。 */
export const WorkspaceRepoProbeResponseSchema: z.ZodType<WorkspaceRepoProbeResponse> = z.object({
  url: z.string(),
  provider: z.string().default("gongfeng"),
  project_path: z.string(),
  default_branch: z.string().default(""),
  branches: z.array(z.string()).default([]),
  connection_status: z.string().default(""),
  test_status: z.string().default(""),
}).loose();

/** 校验 workspace 的单个 MCP 服务器摘要。 */
export const WorkspaceMcpServerSchema = z.object({
  description: z.string().default(""),
  configured: z.boolean().default(false),
  id: z.string().default(""),
  workspace_id: z.string().default(""),
  name: z.string().default(""),
  transport: z.string().default("unknown"),
  enabled: z.boolean().optional(),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
});

/** 校验 workspace MCP 服务器列表（直接是数组）。 */
export const WorkspaceMcpServerListSchema = z.array(WorkspaceMcpServerSchema);

/** workspace MCP 服务器的空值兜底。 */
export const EMPTY_WORKSPACE_MCP_SERVER: WorkspaceMcpServer = {
  id: "",
  workspace_id: "",
  name: "",
  transport: "unknown",
  created_at: "",
  updated_at: "",
};

// 分享链接。随 workspace 分享邀请流程引入；schema 镜像 API 响应，畸形负载退到安全默认。
/** 校验一个分享邀请链接。 */
export const ShareLinkSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  code: z.string(),
  created_by: z.string(),
  role: z.string(),
  expires_at: z.string().nullable().optional().default(null),
  max_uses: z.number().nullable().optional().default(null),
  use_count: z.number().optional().default(0),
  is_active: z.boolean().optional().default(true),
  created_at: z.string().optional().default(""),
  creator_name: z.string().optional().default(""),
  creator_email: z.string().optional().default(""),
}).loose();

/** 分享链接的空值兜底。 */
export const EMPTY_SHARE_LINK: ShareLink = {
  id: "",
  workspace_id: "",
  code: "",
  created_by: "",
  role: "member",
  expires_at: null,
  max_uses: null,
  use_count: 0,
  is_active: false,
  created_at: "",
};

/** 校验分享链接列表响应（直接是数组）。 */
export const ShareLinkListResponseSchema = z.array(ShareLinkSchema).default([]);

/** 校验分享链接落地页信息。 */
export const ShareLinkInfoSchema = z.object({
  workspace_name: z.string().optional().default(""),
  workspace_slug: z.string().optional().default(""),
  creator_name: z.string().optional().default(""),
  role: z.string().optional().default("member"),
}).loose();

/** 分享链接落地页信息的空值兜底。 */
export const EMPTY_SHARE_LINK_INFO: ShareLinkInfo = {
  workspace_name: "",
  workspace_slug: "",
  role: "member",
};

/** 校验成员（含用户摘要）的读取形状。 */
export const MemberWithUserSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  user_id: z.string(),
  role: z.string(),
  created_at: z.string().optional().default(""),
  name: z.string().optional().default(""),
  email: z.string().optional().default(""),
  avatar_url: z.string().nullable().optional().default(null),
}).loose();

/** 校验「通过分享链接加入 workspace」的响应。 */
export const JoinShareLinkResponseSchema = z.object({
  member: MemberWithUserSchema,
  workspace_id: z.string(),
  workspace_slug: z.string().optional().default(""),
}).loose();

/** 加入分享链接响应的空值兜底。 */
export const EMPTY_JOIN_SHARE_LINK_RESPONSE: {
  member: MemberWithUser;
  workspace_id: string;
  workspace_slug: string;
} = {
  member: {
    id: "",
    workspace_id: "",
    user_id: "",
    role: "member",
    created_at: "",
    name: "",
    email: "",
    avatar_url: null,
  },
  workspace_id: "",
  workspace_slug: "",
};

export * from "./schemas-life";

export const RepositoryLocationSchema = z.object({id:z.string(),workspace_id:z.string(),repository_id:z.string(),daemon_id:z.string(),local_path:z.string(),execution_mode:z.enum(["worktree","in_place"])});
export const WorkspaceRepositoryCenterSchema = z.object({
 configured_locations:z.array(RepositoryLocationSchema).default([]),
 repos: z.array(WorkspaceRepoSchema),
 projects: z.array(z.object({id:z.string().min(1),title:z.string(),resources:z.array(z.object({
  id:z.string(), project_id:z.string(), workspace_id:z.string(), resource_type:z.string(), resource_ref:z.record(z.string(),z.unknown()),
  label:z.string().nullable(), position:z.number(), created_at:z.string(), created_by:z.string().nullable(),
 }))})),
 locations: z.array(z.object({
 project_id:z.string(),task_id:z.string(),runtime_id:z.string(),daemon_id:z.string(),runtime_name:z.string(),provider:z.string(),
 work_dir:z.string(),branch:z.string(),status:z.string(),started_at:z.string().nullable(),completed_at:z.string().nullable(),
 })),
});

export const WorkspaceMcpCheckSchema = z.object({
 id:z.string(), runtime_id:z.string(), status:z.string(), tool_count:z.number(), error_code:z.string(),
 created_at:z.string(), completed_at:z.string().nullable(), stale:z.boolean().default(false),
}).transform((v)=>({id:v.id,runtimeId:v.runtime_id,status:v.status,toolCount:v.tool_count,errorCode:v.error_code,createdAt:v.created_at,completedAt:v.completed_at,stale:v.stale}));
export const WorkspaceMcpChecksSchema = z.array(WorkspaceMcpCheckSchema);

export const TaskOutputPageSchema = z.object({
  output: z.string(), offset: z.number(), next_offset: z.number(),
  total_chars: z.number(), has_more: z.boolean(), found: z.boolean(),
}).transform((page) => ({
  output: page.output, offset: page.offset, nextOffset: page.next_offset,
  totalChars: page.total_chars, hasMore: page.has_more, found: page.found,
}));

export type TaskOutputPage = z.output<typeof TaskOutputPageSchema>;

// ─── 上游同步 ────────────────────────────────────────────────────────────
export const UpstreamConfigSchema = z.object({
  upstream_url: z.string(), upstream_branch: z.string(), local_ref: z.string(),
  runtime_id: z.string().default(""),
  agent_id: z.string().default(""), autopilot_id: z.string().default(""),
  batch_size: z.number(), schedule_hour: z.number().default(9),
  sync_state: z.string(),
  bootstrap_base_sha: z.string(), cursor_sha: z.string(), head_sha: z.string(),
  last_run_at: z.string().nullable(), error: z.string().default(""),
  model: z.string().default(""), thinking_level: z.string().default(""),
  instructions: z.string().default(""),
});
export const UpstreamRunSchema = z.object({
  id: z.string(), mode: z.string(), trigger_source: z.string(), status: z.string(),
  base_sha: z.string(), observed_head_sha: z.string(), cursor_sha: z.string(),
  scanned_commits: z.number(), absorbed_commits: z.number(), item_count: z.number(),
  has_more: z.boolean(), error: z.string(),
  started_at: z.string().nullable(), completed_at: z.string().nullable(), created_at: z.string(),
});
export const UpstreamSnapshotSchema = z.object({
  config: UpstreamConfigSchema, pending: z.number(), adopted: z.number(), ignored: z.number().default(0),
  active_run_id: z.string().default(""), can_write: z.boolean().default(false),
  next_run_at: z.string().nullable().default(null),
  runtime_online: z.boolean().default(false),
  // none 未绑定运行时；unbound 执行体失去运行时；bound 已绑定。
  executor_state: z.string().default("none"),
  last_run: UpstreamRunSchema.nullable().default(null),
});
export const UpstreamItemSchema = z.object({
  id: z.string(), run_id: z.string(), cluster_key: z.string(), title: z.string(),
  summary: z.string(), rationale: z.string(), verdict: z.string(), confidence: z.string(),
  presence: z.string(), paths: z.array(z.string()).default([]), commit_count: z.number(),
  commit_shas: z.array(z.string()).default([]),
  first_commit_at: z.string().nullable(), last_commit_at: z.string().nullable(),
  status: z.string(), adopted_at: z.string().nullable(), created_at: z.string(),
  absorption_issue_id: z.string().default(""), absorption_status: z.string().default("none"),
  has_new_commits: z.boolean().default(false),
});
export const UpstreamItemsSchema = z.object({ items: z.array(UpstreamItemSchema), total: z.number().default(0) });
export const UpstreamRunsSchema = z.object({ runs: z.array(UpstreamRunSchema), total: z.number().default(0) });
export type UpstreamSnapshot = z.output<typeof UpstreamSnapshotSchema>;
export type UpstreamItem = z.output<typeof UpstreamItemSchema>;
export type UpstreamRun = z.output<typeof UpstreamRunSchema>;

export const TestEnvironmentControlSchema = z.object({
 name:z.string(), revision:z.number(),
 desired:z.object({policy:z.object({provider:z.enum(["codebuddy","codex"]),model:z.string(),thinking_level:z.string(),service_tier:z.string()}),overrides:z.record(z.string(),z.object({provider:z.enum(["codebuddy","codex"]),model:z.string(),thinking_level:z.string(),service_tier:z.string()}))}),
 observed:z.object({status:z.string().optional(),revision:z.number().optional(),error:z.string().optional(),agents:z.array(z.object({id:z.string(),name:z.string(),model:z.string().nullable(),runtime_id:z.string().nullable(),status:z.string(),archived_at:z.string().nullable()})).optional(),runtimes:z.array(z.object({id:z.string(),provider:z.string()})).optional()}).passthrough(),
 observed_at:z.string().nullable(),
});

export const TestAgentControlCapabilitySchema = z.object({ available: z.boolean() });

/** 运行时上下文事件：只要求 kind，其余字段按 kind 解释。 */
export const TaskContextEventSchema = z.object({ kind: z.string() }).passthrough();
export const TaskContextResponseSchema = z.object({
  events: z.array(TaskContextEventSchema),
  next_cursor: z.string().optional(),
  has_more: z.boolean().optional(),
}).loose();

/** 工作区知识库状态（失败时也返回 200，healthy=false）。 */
export const KnowledgeStatusSchema = z.object({
  enabled: z.boolean(),
  healthy: z.boolean(),
  base_url: z.string().optional(),
  error: z.string().optional(),
  health: z.unknown().optional(),
}).loose();

export const KnowledgeResourcesResponseSchema = z.object({
  uri: z.string(),
  result: z.unknown(),
}).loose();

export const KnowledgeSearchResponseSchema = z.object({
  query: z.string(),
  target_uri: z.string(),
  result: z.unknown(),
}).loose();

export const KnowledgeUploadResponseSchema = z.object({
  filename: z.string(),
  result: z.unknown(),
}).loose();
