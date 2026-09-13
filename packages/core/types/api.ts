import type { Issue, IssueMetadata, IssueStatus, IssueStatusCategory, IssuePriority, IssueAssigneeType } from "./issue";
import type { PropertyFilterValue } from "./property";
import type { MemberRole } from "./workspace";
import type { Project } from "./project";

// ===========================================================================
// 文件职责：各类后端 HTTP 接口（API）的请求/响应数据结构，纯类型声明。
//
// 内容分组：
//   1. Issue 读写（Create/Update/Move/触发预览/列表/分组），含编辑器防冲突
//      的 base 字段与"是否顺带启动 agent 运行"的控制字段。
//   2. 议题表格协议（IssueTable*）：server 端权威的范围/筛选/分组/分页契约，
//      浏览器只持有折叠等视图状态。范围 scope、过滤器 filters、分组 group
//      三类规格都在这份文件里。
//   3. 全局搜索（issue / project）与用户/成员（UpdateMe、CreateMember…）。
//   4. 个人访问令牌（Personal Access Token）与通用分页参数。
//
// 通用约定：字段命名与后端 JSON 一一对应（snake_case）；`*_base` 字段是
// 编辑器在生成这次更新前实际采用的基线文本，供服务器做三方合并；`*Request`
// 为请求体，`*Response` 为响应体，`*Params` 为查询字符串参数。
// ===========================================================================

// 议题创建/读写类 API
/** 创建一条议题的请求体（POST /api/issues）。 */
export interface CreateIssueRequest {
  title: string;
  description?: string;
  status?: IssueStatus;
  priority?: IssuePriority;
  assignee_type?: IssueAssigneeType;
  assignee_id?: string;
  /** 父议题 id（把本议题创建为某父议题的子议题）。 */
  parent_issue_id?: string;
  project_id?: string;
  /** 有序的关卡编号（>= 1），把这条子议题归到父议题下的某个"关卡"分组。 */
  stage?: number;
  /** 计划开始日期（纯日期 "YYYY-MM-DD"）。 */
  start_date?: string;
  /** 截止日期（纯日期 "YYYY-MM-DD"）。 */
  due_date?: string;
  /** 创建时一并绑定到议题的附件 id。 */
  attachment_ids?: string[];
  /** 创建时同事务挂上的议题级标签 id；未知或非议题的 id 会被服务器 400 拒绝。 */
  label_ids?: string[];
}

/** 请求体：从评论以"手动模式"创建子议题——用户自己填一份议题表单。 */
export interface CreateCommentSubIssueManualRequest {
  mode: "manual";
  /** 一次性捕获令牌（标识来源上下文快照，防止过期/重复使用）。 */
  capture_token: string;
  issue: CreateIssueRequest;
}

/** 请求体：从评论以"agent 模式"创建子议题——交给 agent/squad 快速去办。 */
export interface CreateCommentSubIssueAgentRequest {
  mode: "agent";
  capture_token: string;
  quick_create: {
    /** 承接的 agent id（agent_id 与 squad_id 二选一）。 */
    agent_id?: string;
    /** 承接的 squad id。 */
    squad_id?: string;
    /** 要执行的任务描述。 */
    prompt: string;
    priority?: IssuePriority;
    due_date?: string;
    project_id?: string | null;
    attachment_ids?: string[];
  };
}

/** 从评论创建子议题的请求体 = 手动模式 | agent 模式（用 mode 判别）。 */
export type CreateCommentSubIssueRequest =
  | CreateCommentSubIssueManualRequest
  | CreateCommentSubIssueAgentRequest;

/** 更新一条议题的请求体（PATCH/PUT /api/issues/:id）：字段全部可选，传哪个改哪个。 */
export interface UpdateIssueRequest {
  /** 旧式整体"比较并替换"令牌。新文本编辑器改用字段级基线，避免无关的议题
   *  活动（比如别人改标题）把你正在编辑的字段也判定为冲突。 */
  expected_revision?: number;
  title?: string;
  /** 编辑器在生成这次更新前实际采用的权威标题基线（服务端据此三方合并）。 */
  title_base?: string;
  description?: string;
  /** 编辑器在生成这次更新前实际采用的权威描述基线。服务端用它与期间新落地的
   *  渠道媒体做合并。 */
  description_base?: string;
  status?: IssueStatus;
  priority?: IssuePriority;
  assignee_type?: IssueAssigneeType | null;
  assignee_id?: string | null;
  /** 排序位置（看板列内拖拽的结果）。 */
  position?: number;
  start_date?: string | null;
  due_date?: string | null;
  parent_issue_id?: string | null;
  project_id?: string | null;
  /** 有序的关卡编号（>= 1）；null 表示清除（取消分关）。 */
  stage?: number | null;
  /** 描述更新时一并绑定到本议题的附件 id。描述编辑器用它登记新上传的文件，
   *  让它们出现在 `issueAttachments` 里，刷新后预览仍保持已读状态。 */
  attachment_ids?: string[];
  /** 抑制本次写入本会触发的 agent 运行（"暂时不启动"，MUL-3375）——负责人/
   *  状态变更照常生效。控制字段：乐观缓存补丁里要剥掉，绝不写进 Issue。 */
  suppress_run?: boolean;
  /** 注入到这次启动的运行开场上下文中的自由文本交接嘱咐（MUL-3375）。只在
   *  确实启动了运行时才被消费。控制字段——从乐观缓存补丁里剥掉。 */
  handoff_note?: string;
}

/**
 * 拖拽意图（位置语义由服务器权威决定）。客户端乐观动画可以先用临时 position，
 * 但最终位置由 `POST /api/issues/:id/move` 依据这些工作区范围内相邻议题推导。
 */
export interface MoveIssueRequest
  extends Pick<
    UpdateIssueRequest,
    | "status"
    | "assignee_type"
    | "assignee_id"
    | "parent_issue_id"
    | "project_id"
  > {
  /** 拖放目标前面的兄弟议题 id（移到最前为 null）。 */
  before_id: string | null;
  /** 拖放目标后面的兄弟议题 id（移到最后为 null）。 */
  after_id: string | null;
}

/** `POST /api/issues/preview-trigger` 的入参：某项预期字段传 null 表示"保持不变"；
 *  `isCreate` 用于预览一条尚未落库的议题。 */
export interface IssueTriggerPreviewParams {
  issueIds?: string[];
  isCreate?: boolean;
  assigneeType?: IssueAssigneeType | null;
  assigneeId?: string | null;
  status?: IssueStatus;
}

/** 在这份预期写入下「会启动一次运行」的议题。`agent_id` 是真正能跑的 agent
 *  （squad 取其 leader）。`handoff_supported` 是软闸信号：false 说明目标
 *  runtime 太老、无法展示交接嘱咐（备注框置灰，但分配照常生效）。 */
export interface IssueTriggerPreviewItem {
  issue_id: string;
  agent_id: string;
  /** 触发来源说明（为何本次写入会触发运行）。 */
  source: string;
  handoff_supported: boolean;
}

/** 触发预览结果：预期触发的运行清单 + 总数。 */
export interface IssueTriggerPreview {
  triggers: IssueTriggerPreviewItem[];
  total_count: number;
}

/** `GET /api/issues` 的查询参数：列表窗口（范围/筛选/排序/分页）。 */
export interface ListIssuesParams {
  /** 每页条数（分页）。 */
  limit?: number;
  /** 跳过条数（分页）。 */
  offset?: number;
  workspace_id?: string;
  /** 扁平表格快速搜索：匹配标题里的词或精确的议题编号。 */
  q?: string;
  status?: IssueStatus;
  /** 多值表格筛面。字段内取 OR。 */
  statuses?: IssueStatus[];
  /**
   * 按状态「类别」（category）筛而不是精确 key——这样一个桶能同时装下该类别
   * 的规范状态与所有继承它的自定义状态。这正是看板扇出始终固定为 7 个请求、
   * 与工作区定义了多少自定义状态无关的原因。(MUL-6243)
   */
  status_category?: IssueStatusCategory;
  /** `status_category` 的多值形式。字段内取 OR。 */
  status_categories?: IssueStatusCategory[];
  priority?: IssuePriority;
  /** 多值表格筛面。字段内取 OR。 */
  priorities?: IssuePriority[];
  assignee_id?: string;
  assignee_ids?: string[];
  /**
   * 收窄到分派给给定执行者种类（member / agent / squad）的议题。语义与
   * `ListGroupedIssuesParams.assignee_types` 相同——服务端支撑工作区的
   * Members/Agents 页签。
   */
  assignee_types?: IssueAssigneeType[];
  creator_id?: string;
  project_id?: string;
  /** 感知执行者类型的表格筛面。每个字段内取 OR。 */
  assignee_filters?: IssueActorRef[];
  /** 无负责人议题也纳入筛面。 */
  include_no_assignee?: boolean;
  creator_filters?: IssueActorRef[];
  project_ids?: string[];
  /** 无项目议题也纳入筛面。 */
  include_no_project?: boolean;
  label_ids?: string[];
  /** 把窗口限制到根议题（而非对加载出的页再做过滤）。 */
  top_level_only?: boolean;
  /**
   * 把窗口硬性限制到给定议题 id 集（表格的 "agents-working" 筛面就把当前
   * 在跑的议题集传进来）。空数组是有意义的——得到空窗口；想表达"不加限制"
   * 则整体省略该字段。
   */
  ids?: string[];
  /**
   * 把负责人筛面放宽到"用户是*间接*负责人"的议题：负责人是用户拥有的某个
   * agent，或某个涉及用户的 squad（真人成员 / 经自有 agent 担任 leader /
   * 用户拥有的 agent 作为成员）。刻意排除直接成员指派：`involves_user_id`
   * 与 `assignee_id=<user>`（"指派给我"页签）结果集因此天然不相交。
   */
  involves_user_id?: string;
  /** `issue.metadata` 的 JSONB 包含过滤。跨键取 AND。 */
  metadata?: IssueMetadata;
  /** 自定义属性过滤：属性定义 id → 可接受的值集（选项 id，勾选框为 "true"/
   *  "false"；裸字符串表示精确相等，操作符对象进一步收窄）。同一属性内取
   *  OR，跨属性取 AND。 */
  properties?: Record<string, PropertyFilterValue[]>;
  /** 只看未关闭（open）议题。 */
  open_only?: boolean;
  /**
   * 只返回 `start_date` / `due_date` 至少设置了一个的议题。项目甘特视图靠它
   * 避免在客户端翻遍整个项目再丢弃没排期的大多数议题。
   */
  scheduled?: boolean;
  /** 按哪个时间字段做区间筛选。 */
  date_field?: "created_at" | "updated_at";
  /** 区间起点（含）。 */
  date_start?: string;
  /** 区间终点（含）。 */
  date_end?: string;
  /** 排序字段（`property:<id>` 表示按某自定义属性值排）。 */
  sort_by?:
    | "position"
    | "status"
    | "priority"
    | "title"
    | "created_at"
    | "updated_at"
    | "last_activity"
    | "start_date"
    | "due_date"
    | `property:${string}`;
  sort_direction?: "asc" | "desc";
}

/** 执行者引用：类型（member/agent/squad）+ id。 */
export interface IssueActorRef {
  type: IssueAssigneeType;
  id: string;
}

/** `GET /api/issues/grouped`（按负责人分组）的查询参数。 */
export interface ListGroupedIssuesParams {
  /** 目前仅支持按负责人分组。 */
  group_by: "assignee";
  limit?: number;
  offset?: number;
  workspace_id?: string;
  statuses?: IssueStatus[];
  priorities?: IssuePriority[];
  assignee_types?: IssueAssigneeType[];
  assignee_id?: string;
  assignee_ids?: string[];
  creator_id?: string;
  project_id?: string;
  /** 语义同 `ListIssuesParams.involves_user_id`。 */
  involves_user_id?: string;
  /** `issue.metadata` 的 JSONB 包含过滤。跨键取 AND。 */
  metadata?: IssueMetadata;
  /** 自定义属性过滤，语义同 `ListIssuesParams.properties`。 */
  properties?: Record<string, PropertyFilterValue[]>;
  assignee_filters?: IssueActorRef[];
  include_no_assignee?: boolean;
  creator_filters?: IssueActorRef[];
  project_ids?: string[];
  include_no_project?: boolean;
  label_ids?: string[];
  /** 只取某一负责人类型的组（"none" = 无负责人组）。 */
  group_assignee_type?: IssueAssigneeType | "none";
  /** 要取的那个组的负责人 id。 */
  group_assignee_id?: string;
  date_field?: "created_at" | "updated_at";
  date_start?: string;
  date_end?: string;
  sort_by?:
    | "position"
    | "status"
    | "priority"
    | "title"
    | "created_at"
    | "updated_at"
    | "last_activity"
    | "start_date"
    | "due_date"
    | `property:${string}`;
  sort_direction?: "asc" | "desc";
}

/** `GET /api/issues` 的原始后端响应形态。 */
export interface ListIssuesResponse {
  issues: Issue[];
  /** 满足筛选的总条数（而非本页条数）。 */
  total: number;
}

/** 按负责人分组结果里的一个组桶。 */
export interface IssueAssigneeGroup {
  id: string;
  assignee_type: IssueAssigneeType | null;
  assignee_id: string | null;
  /** 该桶内（当前页）的议题。 */
  issues: Issue[];
  /** 该桶的议题总数（跨页）。 */
  total: number;
}

/** `GET /api/issues/grouped?group_by=assignee` 的原始后端响应形态。 */
export interface GroupedIssuesResponse {
  groups: IssueAssigneeGroup[];
}

// 议题表格协议：范围/筛选/分组/分页契约（服务端权威）。成员关系、分组与
// 计数都对完整结果集求值；浏览器只持有折叠的组/父等视图状态。
export type IssueTableScope =
  /** 整个工作区。 */
  | { kind: "workspace"; assignee_types?: IssueAssigneeType[] }
  /** 某个项目内。 */
  | { kind: "project"; project_id: string; assignee_types?: IssueAssigneeType[] }
  /** 某负责人名下。 */
  | { kind: "assignee"; actor: IssueActorRef }
  /** 某创建者名下。 */
  | { kind: "creator"; actor: IssueActorRef }
  /** "与我相关"（我负责的/我创建的/我参与的/任意）。 */
  | { kind: "my"; relation: "assigned" | "created" | "involved" | "any" };

/** 议题表格的筛面集合（字段内 OR、跨字段 AND）。 */
export interface IssueTableFilters {
  statuses?: IssueStatus[];
  priorities?: IssuePriority[];
  assignees?: IssueActorRef[];
  include_no_assignee?: boolean;
  creators?: IssueActorRef[];
  project_ids?: string[];
  include_no_project?: boolean;
  label_ids?: string[];
  /** 形状同 `ListIssuesParams.properties`：裸字符串为精确相等/"无值"，
   *  操作符对象收窄标量匹配。 */
  properties?: Record<string, PropertyFilterValue[]>;
  /** 按创建/更新时间区间筛。 */
  date?: {
    field: "created_at" | "updated_at";
    start: string;
    end: string;
  };
  /** 只看有 agent 正在干活的议题。 */
  working_only?: boolean;
  /** 匹配 `/api/working-agents` 返回的"在跑任务议题"投影。显式空列表表示
   *  什么都不匹配。 */
  working_issue_ids?: string[];
  /** 是否包含子议题。 */
  include_sub_issues?: boolean;
}

/** 表格可排序字段（`property:<id>` 表示按自定义属性值排序）。 */
export type IssueTableSortField =
  | "position"
  | "status"
  | "priority"
  | "title"
  | "created_at"
  | "updated_at"
  | "last_activity"
  | "start_date"
  | "due_date"
  | `property:${string}`;

/** 一次表格查询的完整规格：范围 + 筛选 + 搜索词 + 排序。 */
export interface IssueTableQuerySpec {
  scope: IssueTableScope;
  filters: IssueTableFilters;
  search?: string;
  sort: {
    field: IssueTableSortField;
    direction: "asc" | "desc";
  };
}

/** 表格的分组规格（看板/列表如何分桶）。 */
export type IssueTableGroupSpec =
  | { kind: "none" }
  | { kind: "status" }
  /**
   * 按状态所归属的「类别」（category）分组，而不是按状态 key。
   *
   * 看板列、列表分区与泳道格子都是类别，因此自定义状态会并进它所表现的那个
   * 列，而不是各自成列——这正是页面的扇出固定为 7、与工作区定义多少状态无关
   * 的原因。描述符仍上报 `value.kind === "status"`，因为类别的值就是它的规范
   * 状态 key；是 group KEY 区分了这两种契约。(MUL-6243)
   */
  | { kind: "status_category" }
  | { kind: "assignee" }
  | { kind: "project" }
  | { kind: "parent" }
  | {
      kind: "compound";
      primary: "assignee" | "project" | "parent";
      /** 次级维度用类别，让自定义状态并入其类别的格子。 */
      secondary: "status" | "status_category";
      /** 可选的可见次级桶白名单。给出时，服务器只对包含至少一张匹配卡片的
       *  主组做分页，并对这个完整可见结果集返回 `total`。 */
      secondary_values?: IssueStatus[] | IssueStatusCategory[];
    }
  | { kind: "property"; property_id: string; include_empty?: boolean };

/** 响应侧的执行者引用。保持开放以向前兼容未来新执行者种类。 */
export interface IssueTableActorRef {
  type: string;
  id: string;
}

/** 父议题引用（分组单元格展示用）。 */
export interface IssueTableParentRef {
  id: string;
  number: number;
  identifier: string;
  title: string;
  status: string;
}

/** 分组值：一个分组的"键"，按 kind 判别。 */
export type IssueTableGroupValue =
  | { kind: "status"; status: string }
  | { kind: "assignee"; actor: IssueTableActorRef | null }
  | { kind: "project"; project_id: string | null }
  | {
      kind: "parent";
      parent_id: string | null;
      parent: IssueTableParentRef | null;
      /** 父议题取值状态：正常有值 / 不可用 / 未设置。 */
      value_state: "value" | "unavailable" | "unset";
    }
  | {
      kind: "property";
      property_id: string;
      value?: string | boolean | null;
      value_state: "value" | "unavailable" | "unset";
    };

/** 一个分组桶的描述符。 */
export interface IssueTableGroupDescriptor {
  key: string;
  value: IssueTableGroupValue;
  /** 该组内的议题条数。 */
  count: number;
  /** 复合分组的次级桶。这些不透明 key 可直接回传给 `/table/rows`；客户端
   *  不得自行拼装。 */
  secondary_groups?: IssueTableGroupDescriptor[];
}

/** 表格分页请求。 */
export interface IssueTablePageRequest {
  /** 每页条数。 */
  limit?: number;
  /** 分页游标（首页为 null）。 */
  cursor?: string | null;
}

/** 拉取分组桶的请求：查询规格 + 分组规格 + 分页。 */
export interface IssueTableGroupsRequest {
  query: IssueTableQuerySpec;
  group: Exclude<IssueTableGroupSpec, { kind: "none" }>;
  page?: IssueTablePageRequest;
}

/** 分组桶列表的响应。 */
export interface IssueTableGroupsResponse {
  /** 查询指纹（标识这份规格，便于客户端缓存校验）。 */
  query_fingerprint: string;
  total: number;
  groups: IssueTableGroupDescriptor[];
  next_cursor: string | null;
}

/** 拉取行（议题卡片）的请求。 */
export interface IssueTableRowsRequest {
  query: IssueTableQuerySpec;
  group: IssueTableGroupSpec;
  /** 目标分组桶的 key（复合/父子结构用）。 */
  group_key: string | null;
  /** 是否展开父子层级。 */
  hierarchy: { enabled: boolean };
  /** 要取的父议题（顶层为 null）。 */
  parent_id: string | null;
  page?: IssueTablePageRequest;
}

/** 表格中的一行：议题 + 它的直接子议题数。 */
export interface IssueTableRow {
  issue: Issue;
  direct_child_count: number;
}

/** 行列表的响应。 */
export interface IssueTableRowsResponse {
  query_fingerprint: string;
  group_key: string | null;
  parent_id: string | null;
  total: number;
  rows: IssueTableRow[];
  /** 该分支下的总数（含折叠的深层子级）。 */
  branch_total: number;
  next_cursor: string | null;
}

/** 表格侧栏筛面规格。 */
export type IssueTableFacetSpec =
  | { kind: "status" }
  | { kind: "priority" }
  | { kind: "assignee" }
  | { kind: "creator" }
  | { kind: "project" }
  | { kind: "label" }
  | { kind: "property"; property_id: string }
  /** 在该页面内正在给议题干活的 agent。`key` 是 agent id，`count` 是它在跑
   *  任务数。按页面自身的范围与筛面求值，表头芯片数到的行和列表显示的
   *  同一批。 */
  | { kind: "working_agents" };

/** 侧栏筛面的请求。 */
export interface IssueTableFacetsRequest {
  query: IssueTableQuerySpec;
  facets: IssueTableFacetSpec[];
  /** 老调用方默认传 true。只计数的 UI 可跳过额外扫描。 */
  include_total?: boolean;
}

/** 筛面里一个值项（key + 命中条数）。 */
export interface IssueTableFacetValue {
  key: string;
  count: number;
}

/** 一个筛面的结果。 */
export interface IssueTableFacet {
  kind: IssueTableFacetSpec["kind"];
  property_id?: string;
  values: IssueTableFacetValue[];
}

/** 侧栏筛面结果的响应。 */
export interface IssueTableFacetsResponse {
  query_fingerprint: string;
  total: number;
  facets: IssueTableFacet[];
}

/** 在某个议题页面里正给议题干活的单个 agent。从 `working_agents` 筛面投影
 *  而来，计数已经过该页面的范围与全部激活筛面收窄。名字/头像从工作区 agent
 *  目录解析，不随行携带。 */
export interface WorkingAgentSummary {
  id: string;
  running_task_count: number;
}

/** 分页议题缓存里"按状态"的一个桶。`total` 是服务器计数（全部页），不是
 *  `issues` 数组的长度。 */
export interface IssueStatusBucket {
  issues: Issue[];
  total: number;
}

/**
 * 议题列表的前端缓存形态：按状态分桶，让每列能独立分页。由 issues/queries.ts
 * 里的查询函数把各状态的 `api.listIssues` 响应组装而成。
 */
export interface ListIssuesCache {
  /** 按状态「类别」分桶——见 PAGINATED_CATEGORIES。(MUL-6243) */
  byStatus: Partial<Record<IssueStatusCategory, IssueStatusBucket>>;
}

/** 一条搜索命中的议题（在 Issue 基础上标注命中位置与片段）。 */
export interface SearchIssueResult extends Issue {
  /** 命中来源：标题 / 描述 / 评论。 */
  match_source: "title" | "description" | "comment";
  /** 命中的标题/描述片段。 */
  matched_snippet?: string;
  matched_description_snippet?: string;
  matched_comment_snippet?: string;
}

/** 议题搜索的响应。 */
export interface SearchIssuesResponse {
  issues: SearchIssueResult[];
  total: number;
}

/** 一条搜索命中的项目。 */
export interface SearchProjectResult extends Project {
  match_source: "title" | "description";
  matched_snippet?: string;
}

/** 项目搜索的响应。 */
export interface SearchProjectsResponse {
  projects: SearchProjectResult[];
  total: number;
}

/** 更新"我自己"个人资料/设置的请求体。 */
export interface UpdateMeRequest {
  name?: string;
  avatar_url?: string;
  language?: string;
  /** 自由文本自我介绍（最长 2000 字符）。传 "" 清空。 */
  profile_description?: string;
  /** 想固定的 IANA 时区；"" 清空退回浏览器时区；undefined 表示不改动。 */
  timezone?: string;
}

/** 向工作区添加成员的请求体。 */
export interface CreateMemberRequest {
  /** 邀请邮箱。 */
  email: string;
  role?: MemberRole;
}

/** 更新工作区某成员角色的请求体。 */
export interface UpdateMemberRequest {
  role: MemberRole;
}

// 个人访问令牌（PAT，供外部程序/CLI 以你的身份调 API）
export interface PersonalAccessToken {
  id: string;
  /** 令牌名称（给自己看）。 */
  name: string;
  /** 令牌明文前缀（只存前缀，便于识别是哪把钥匙）。 */
  token_prefix: string;
  /** 过期时间；永不失效为 null。 */
  expires_at: string | null;
  /** 最近使用时间；从未用过的为 null。 */
  last_used_at: string | null;
  created_at: string;
}

/** 创建 PAT 的请求体。 */
export interface CreatePersonalAccessTokenRequest {
  name: string;
  /** 有效期天数。 */
  expires_in_days?: number;
}

/** 创建 PAT 的响应：除元数据外含一次性的完整令牌明文。 */
export interface CreatePersonalAccessTokenResponse extends PersonalAccessToken {
  /** 完整令牌明文——只在创建响应里出现一次，之后不可再取。 */
  token: string;
}

// 通用分页参数
export interface PaginationParams {
  /** 每页条数。 */
  limit?: number;
  /** 跳过条数。 */
  offset?: number;
}
