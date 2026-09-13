// 议题查询的 query key 工厂与 queryOptions：定义各类议题视图/详情的缓存键和请求配置。
import {
  infiniteQueryOptions,
  keepPreviousData,
  queryOptions,
  type QueryClient,
} from "@tanstack/react-query";
import { api, ApiError } from "../api";
import type {
  Issue,
  IssueAssigneeType,
  IssueStatusCategory,
  IssueTableFacetsRequest,
  IssueTableGroupSpec,
  IssueTableGroupsRequest,
  IssueTableQuerySpec,
  IssueTableRowsRequest,
  ListGroupedIssuesParams,
  ListIssuesParams,
  ListIssuesCache,
} from "../types";
import { ALL_STATUSES } from "./config";

/** 议题列表的排序/窗口参数，编进 query key，保证排序变化时缓存键也随之变化。 */
export interface IssueSortParam {
  sort_by?: ListIssuesParams["sort_by"];
  sort_direction?: ListIssuesParams["sort_direction"];
  date_field?: ListIssuesParams["date_field"];
  date_start?: ListIssuesParams["date_start"];
  date_end?: ListIssuesParams["date_end"];
  /** 服务器端的自定义属性过滤（定义 id → 可接受取值）。
   *  放在排序/窗口这个袋子里，让每个列表视图、query key、加载更多页都自动带上它。 */
  properties?: ListIssuesParams["properties"];
}

/** 议题各视图/详情的缓存键工厂。 */
export const issueKeys = {
  all: (wsId: string) => ["issues", wsId] as const,
  /** 用于失效的前缀——不带排序。 */
  list: (wsId: string) => [...issueKeys.all(wsId), "list"] as const,
  /** 供 queryOptions 使用的完整 key——含排序。 */
  listSorted: (wsId: string, sort?: IssueSortParam) =>
    [...issueKeys.list(wsId), sort ?? {}] as const,
  flatAll: (wsId: string) => [...issueKeys.all(wsId), "flat"] as const,
  flat: (
    wsId: string,
    scope: string,
    filter: IssueFlatFilter,
    sort?: IssueSortParam,
  ) => [...issueKeys.flatAll(wsId), scope, filter, sort ?? {}] as const,
  flatExport: (
    wsId: string,
    scope: string,
    filter: IssueFlatFilter,
    sort?: IssueSortParam,
  ) => [...issueKeys.flatAll(wsId), "export", scope, filter, sort ?? {}] as const,
  tableAll: (wsId: string) => [...issueKeys.all(wsId), "table-query"] as const,
  tableGroups: (
    wsId: string,
    query: IssueTableQuerySpec,
    group: IssueTableGroupsRequest["group"],
  ) => [...issueKeys.tableAll(wsId), "groups", query, group] as const,
  tableFacets: (
    wsId: string,
    request: IssueTableFacetsRequest,
  ) => [...issueKeys.tableAll(wsId), "facets", request] as const,
  tableRows: (
    wsId: string,
    query: IssueTableQuerySpec,
    group: IssueTableGroupSpec,
    groupKey: string | null,
    hierarchy: boolean,
    parentId: string | null,
  ) => [
    ...issueKeys.tableAll(wsId),
    "rows",
    query,
    group,
    groupKey,
    { hierarchy, parentId },
  ] as const,
  assigneeGroupsAll: (wsId: string) =>
    [...issueKeys.all(wsId), "assignee-groups"] as const,
  assigneeGroups: (wsId: string, filter: AssigneeGroupedIssuesFilter) =>
    [...issueKeys.assigneeGroupsAll(wsId), filter] as const,
  /** 所有 “my issues” 查询——用于批量失效。 */
  myAll: (wsId: string) => [...issueKeys.all(wsId), "my"] as const,
  /** 按视图失效的前缀——不带排序。 */
  myList: (wsId: string, scope: string, filter: MyIssuesFilter) =>
    [...issueKeys.myAll(wsId), scope, filter] as const,
  /** 供 queryOptions 使用的完整 key——含排序。 */
  myListSorted: (wsId: string, scope: string, filter: MyIssuesFilter, sort?: IssueSortParam) =>
    [...issueKeys.myList(wsId, scope, filter), sort ?? {}] as const,
  myAssigneeGroupsAll: (wsId: string) =>
    [...issueKeys.myAll(wsId), "assignee-groups"] as const,
  myAssigneeGroups: (
    wsId: string,
    scope: string,
    filter: AssigneeGroupedIssuesFilter,
  ) => [...issueKeys.myAssigneeGroupsAll(wsId), scope, filter] as const,
  /** 所有 Project Gantt 查询——跨项目失效用的前缀匹配 key。 */
  projectGanttAll: (wsId: string) =>
    [...issueKeys.all(wsId), "project-gantt"] as const,
  /**
   * 每个项目的 Gantt 议题列表（仅已排期）。用它自己的缓存键、而不是复用分桶的
   * `myList` 缓存，这样 WS 处理器和缓存辅助函数就不用为 `my` 前缀下的
   * 非分桶形状做特判。
   */
  projectGantt: (
    wsId: string,
    projectId: string,
    assigneeTypes?: IssueAssigneeType[],
  ) =>
    [
      ...issueKeys.projectGanttAll(wsId),
      projectId,
      assigneeTypes ?? null,
    ] as const,
  detail: (wsId: string, id: string) =>
    [...issueKeys.all(wsId), "detail", id] as const,
  /** 把裸议题标识符（如 "MUL-123"）解析成议题。 */
  identifier: (wsId: string, identifier: string) =>
    [...issueKeys.all(wsId), "identifier", identifier] as const,
  /** 工作区内所有“按父议题子议题”查询的前缀。 */
  childrenAll: (wsId: string) =>
    [...issueKeys.all(wsId), "children"] as const,
  children: (wsId: string, id: string) =>
    [...issueKeys.childrenAll(wsId), id] as const,
  /** 失效工作区内所有“批量子议题”查询的前缀。 */
  childrenByParentsAll: (wsId: string) =>
    [...issueKeys.all(wsId), "children-by-parents"] as const,
  /** 完整 key——含已排序的父 id 列表以保证缓存键稳定。 */
  childrenByParents: (wsId: string, parentIds: readonly string[]) =>
    [...issueKeys.childrenByParentsAll(wsId), parentIds] as const,
  childProgress: (wsId: string) =>
    [...issueKeys.all(wsId), "child-progress"] as const,
  /** 失效下面这些“按议题”缓存的前缀匹配 key。这些 key 不带 wsId，所以
   *  `issueKeys.all(wsId)` 覆盖不到它们——WS 重连恢复必须显式失效这些 `*All`
   *  前缀，否则漏掉的事件会让它们在 staleTime: Infinity 默认下永久陈旧（#3953）。 */
  timelineAll: () => ["issues", "timeline"] as const,
  /** 完整议题时间线（单个 TanStack Query，无游标）。 */
  timeline: (issueId: string) =>
    [...issueKeys.timelineAll(), issueId] as const,
  /** 跨所有议题的前缀——WS 任务生命周期事件在这里失效，让打开的 composer 的
   *  触发预览在 agent 队列状态变化时刷新（去重守卫让答案依赖于队列）。 */
  commentTriggerPreviewAll: () => ["issues", "comment-trigger-preview"] as const,
  /** 用于失效的前缀——composer hook 会在其后追加父议题 + 内容签名。 */
  commentTriggerPreview: (issueId: string) =>
    [...issueKeys.commentTriggerPreviewAll(), issueId] as const,
  /** 跨所有议题触发预览（assign/status/create/batch）的前缀。
   *  WS 任务生命周期事件在这里失效，让答案在 agent 队列状态变化时重新校验
   *  （状态源的 pending 去重使其依赖队列，与 commentTriggerPreviewAll 对应）。 */
  issueTriggerPreviewAll: () => ["issues", "issue-trigger-preview"] as const,
  /** 前缀——选择器 hook 会在其后追加一次预期写入的签名。 */
  issueTriggerPreview: (signature: string) =>
    [...issueKeys.issueTriggerPreviewAll(), signature] as const,
  reactionsAll: () => ["issues", "reactions"] as const,
  reactions: (issueId: string) =>
    [...issueKeys.reactionsAll(), issueId] as const,
  subscribersAll: () => ["issues", "subscribers"] as const,
  subscribers: (issueId: string) =>
    [...issueKeys.subscribersAll(), issueId] as const,
  usageAll: () => ["issues", "usage"] as const,
  usage: (issueId: string) => [...issueKeys.usageAll(), issueId] as const,
  attachmentsAll: () => ["issues", "attachments"] as const,
  /** 议题级附件——供描述编辑器使用，让它的行内文件卡/图片 NodeViews
   *  能在点击时重新签名下载 URL。 */
  attachments: (issueId: string) =>
    [...issueKeys.attachmentsAll(), issueId] as const,
  /** 跨所有议题失效任务的前缀匹配 key——用于全局 WS 的 task: 前缀路径，
   *  让任何任务生命周期事件刷新每个按议题列表，而不管当前挂载的是哪个议题。 */
  tasksAll: () => ["issues", "tasks"] as const,
  /** 每个议题的任务列表（议题详情的 Execution log 区块）。 */
  tasks: (issueId: string) => [...issueKeys.tasksAll(), issueId] as const,
  executionTreeAll: () => ["issues", "execution-tree"] as const,
  executionTree: (issueId: string) => [...issueKeys.executionTreeAll(), issueId] as const,
  sourceContextPreview: (wsId: string, anchorCommentId: string) =>
    ["source-context", "preview", wsId, anchorCommentId] as const,
};

/** “来源上下文预览”查询配置：给定某条评论 id 作为锚点，取它关联的子议题摘要。
 * anchorCommentId 为空时不发请求；staleTime: 0 保证每次聚焦都取新值。 */
export function sourceContextPreviewOptions(
  wsId: string,
  anchorCommentId: string | null | undefined,
) {
  return queryOptions({
    queryKey: issueKeys.sourceContextPreview(wsId, anchorCommentId ?? ""),
    queryFn: () => api.getCommentSubIssuePreview(anchorCommentId!),
    enabled: !!anchorCommentId,
    staleTime: 0,
  });
}

/** “我的议题”/按负责人视角的过滤字段子集（从 ListIssuesParams 里挑出的成员类过滤）。
 *  相比 IssueFlatFilter，这是“成员类”视图用的那部分过滤。 */
export type MyIssuesFilter = Pick<
  ListIssuesParams,
  | "assignee_id"
  | "assignee_ids"
  | "assignee_types"
  | "creator_id"
  | "project_id"
  | "involves_user_id"
>;

/** 平铺列表窗口的“服务端过滤契约”。这些过滤维度必须跟着每一页 offset 一起发给
 * 服务端（也因此要编进 query key）：如果在已加载的页面上做客户端后置过滤，当前
 * offset 之后才出现的匹配议题会被静默漏掉。 */
export type IssueFlatFilter = MyIssuesFilter &
  Pick<
    ListIssuesParams,
    | "q"
    | "statuses"
    | "priorities"
    | "assignee_filters"
    | "include_no_assignee"
    | "creator_filters"
    | "project_ids"
    | "include_no_project"
    | "label_ids"
    | "top_level_only"
    | "ids"
  >;

/** “按负责人分桶/分组”查询的过滤字段（去掉分组键、分页参数后剩余的过滤项）。 */
export type AssigneeGroupedIssuesFilter = Omit<
  ListGroupedIssuesParams,
  "group_by" | "limit" | "offset" | "group_assignee_type" | "group_assignee_id"
>;

/** 每个“状态分类列”的单页拉取条数（分页抓取时逐列用这个 limit）。 */
export const ISSUE_PAGE_SIZE = 50;

/**
 * 被拉取并分页进“列表/看板缓存”的状态分类全集——全部 7 个，含 `cancelled`。
 * `cancelled` 是一等公民的默认分类（MUL-4290）：它和其它列一样进缓存、一样渲染，
 * 并不存在单独的“可见看板”子集。这个常量决定“哪些分类会被拉取、进入缓存”。
 *
 * 分桶依据是“状态分类”而不是“单个状态 key”（MUL-6243）。工作区可以自定义任意
 * 数量的状态；若按状态分桶，每新增一个状态，每次加载看板就要多发一个并行的
 * `listIssues` 请求。按分类分桶能把并发请求数恒定钉在 7 个；自定义状态显示在其
 * 所继承分类的那一列里，卡片上自己的徽标才用来标识它具体处于哪个状态。
 */
export const PAGINATED_CATEGORIES: readonly IssueStatusCategory[] = ALL_STATUSES;

/** 把“按状态分桶”的响应摊平成单个 Issue[]，给想要全量列表的消费方用。 */
export function flattenIssueBuckets(data: ListIssuesCache) {
  const out = [];
  for (const status of PAGINATED_CATEGORIES) {
    const bucket = data.byStatus[status];
    if (bucket) out.push(...bucket.issues);
  }
  return out;
}

async function fetchFirstPages(filter: MyIssuesFilter = {}, sort?: IssueSortParam): Promise<ListIssuesCache> {
  const responses = await Promise.all(
    PAGINATED_CATEGORIES.map((category) =>
      api.listIssues({ status_category: category, limit: ISSUE_PAGE_SIZE, offset: 0, ...sort, ...filter }),
    ),
  );
  const byStatus: ListIssuesCache["byStatus"] = {};
  PAGINATED_CATEGORIES.forEach((status: IssueStatusCategory, i: number) => {
    const res = responses[i]!;
    byStatus[status] = { issues: res.issues, total: res.total };
  });
  return { byStatus };
}

/** Table 分组标题页（顶部一级分组名）的无限滚动查询配置。游标来自服务端返回的
 * next_cursor；禁止重试，避免用户在输入过程中被连续失败的请求拖住。 */
export function issueTableGroupsOptions(
  wsId: string,
  query: IssueTableQuerySpec,
  group: IssueTableGroupsRequest["group"],
) {
  return infiniteQueryOptions({
    queryKey: issueKeys.tableGroups(wsId, query, group),
    initialPageParam: null as string | null,
    queryFn: ({ pageParam }) =>
      api.listIssueTableGroups({
        query,
        group,
        page: { limit: 100, cursor: pageParam },
    }),
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

/** 表格里“一个可独立寻址的分支页”的查询配置。
 *
 * Table 让每个游标页都拥有自己独立的 query key：这样头部页刷新后，过期的尾部
 * 游标页能在新响应落地前被脱离/丢弃，不会互相串数据。把请求参数、key 形状、切换
 * 占位（placeholder）和重试策略都收拢在这里——视图层动态拼出的分支图不应重复
 * 实现这套 API 契约。 */
export function issueTableRowPageOptions(
  wsId: string,
  request: IssueTableRowsRequest,
) {
  const cursor = request.page?.cursor ?? null;
  return queryOptions({
    queryKey: [
      ...issueKeys.tableRows(
        wsId,
        request.query,
        request.group,
        request.group_key,
        request.hierarchy.enabled,
        request.parent_id,
      ),
      "page",
      cursor,
    ] as const,
    queryFn: () => api.listIssueTableRows(request),
    placeholderData: keepPreviousData,
    retry: false,
    // 动态 useQueries 的 observer 会随兄弟分支滚入/滚出视口而挂载/卸载。策略是：
    // 出错的页保持“错误”状态，直到用户点 Retry；但“成功过、只是被失效”的页在
    // 重新挂载时仍要允许重取——否则某行页在 observer 处于卸载状态时被 WS 失效，
    // 就会在全局 `staleTime: Infinity` 默认值下永远滞留陈旧数据（总数对、却缺议题）。
    //
    // 两个开关缺一不可：`retryOnMount` 兜住“首次加载就出错”（无数据）的情形；
    // `refetchOnMount` 兜住“页面还留着数据、只是后台重取失败”的情形——TanStack 会
    // 把这种页标成 `isInvalidated`，若只写 `refetchOnMount: true`，它会在每次重新
    // 挂载时重发那个失败的请求，而不是等用户显式点 Retry。
    retryOnMount: false,
    refetchOnMount: (query) => query.state.status !== "error",
  });
}

/** 表格“筛选面”(facet) 的查询配置：给定查询规格，返回各过滤维度可选取值的聚合。 */
export function issueTableFacetsOptions(
  wsId: string,
  request: IssueTableFacetsRequest,
) {
  return queryOptions({
    queryKey: issueKeys.tableFacets(wsId, request),
    queryFn: () => api.listIssueTableFacets(request),
  });
}

/**
 * 缓存形态须知：底层缓存存的是 {@link ListIssuesCache}（按状态分类分桶、每桶
 * `{ issues, total }`），`select` 再把它摊平成 `Issue[]` 交给消费方。因此
 * mutation 与 ws-updaters 必须用 `setQueryData<ListIssuesCache>(...)` 写入，
 * 并保持 byStatus 的分桶形态，不能摊平成数组再写回去。
 *
 * 并行拉取每个会分页的状态分类的第一页。
 */
export function issueListOptions(wsId: string, sort?: IssueSortParam) {
  return queryOptions({
    queryKey: issueKeys.listSorted(wsId, sort),
    queryFn: () => fetchFirstPages({}, sort),
    select: flattenIssueBuckets,
    placeholderData: keepPreviousData,
  });
}

/** 拉取“已排期议题”时用的单页大小。Gantt 视图总是全量拉取已排期议题（不做客户端
 * 分页），所以这个值只是步进服务端 `(limit, offset)` 窗口、直到走完 `total` 时
 * 每一步的块大小。 */
export const PROJECT_GANTT_PAGE_LIMIT = 500;

/** 给 {@link fetchProjectGanttIssues} 循环设的保险上限。真实项目远到不了这个数——
 * 单个项目承载 5 万个已排期议题本身已经是产品层面的问题，轮不到 Gantt 渲染来扛——
 * 但有了这道保险，即使服务端返回的 `total` 出错，循环也不会永远空转下去。 */
export const PROJECT_GANTT_MAX_ISSUES = 10_000;

async function fetchProjectGanttIssues(
  projectId: string,
  assigneeTypes?: IssueAssigneeType[],
) {
  const issues = [];
  let offset = 0;
  while (offset < PROJECT_GANTT_MAX_ISSUES) {
    const res = await api.listIssues({
      project_id: projectId,
      scheduled: true,
      ...(assigneeTypes?.length ? { assignee_types: assigneeTypes } : {}),
      limit: PROJECT_GANTT_PAGE_LIMIT,
      offset,
    });
    issues.push(...res.issues);
    if (res.issues.length < PROJECT_GANTT_PAGE_LIMIT) break;
    if (issues.length >= res.total) break;
    offset += PROJECT_GANTT_PAGE_LIMIT;
  }
  return issues;
}

/** 一次性拉取某项目下所有“已排期”的议题（设了 `start_date` 或 `due_date`）。
 * Project Gantt 视图直接消费它——不做状态分桶、不做客户端分页、也没有“加载全部”
 * 的额外动作——因为已排期的子集足够有界，用少数几个请求就能取完。
 *
 * 背后是 `GET /api/issues?scheduled=true&project_id=…`；SQL 过滤条件与 Gantt
 * 视图在客户端用的 `(start_date IS NOT NULL OR due_date IS NOT NULL)` 判定保持
 * 一致。分页会一直走到 `total` 为止，避免项目过大时第一页之后的时间条被悄悄丢掉。
 */
export function projectGanttIssuesOptions(
  wsId: string,
  projectId: string,
  // 该页面上的“负责人类型”选项卡和别的模式一样收窄 Gantt 范围——同样的 scope、
  // 同样的单一上游映射，所以这里只需要把所选类型原样透传给列表接口。
  assigneeTypes?: IssueAssigneeType[],
) {
  return queryOptions({
    queryKey: issueKeys.projectGantt(wsId, projectId, assigneeTypes),
    queryFn: () => fetchProjectGanttIssues(projectId, assigneeTypes),
  });
}

/** 拉取单条议题详情（`GET /api/issues/{id}`）的查询配置。 */
export function issueDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: issueKeys.detail(wsId, id),
    queryFn: () => api.getIssue(id),
  });
}

/** 把裸议题标识符（如 "MUL-123"）解析成对应议题；找不到则返回 `null`。
 *
 * 支撑 Linear 风格的自动链接。这是“精确查找”，走 `GET /api/issues/{identifier}`——
 * 服务端解析 `前缀-数字`，拿前缀比对工作区自己的 `issue_prefix`，再经唯一的
 * `(workspace_id, number)` 索引读取议题。标识符不存在或前缀不对会得到 404，在这里
 * 映射成 `null`、渲染成纯文本——与旧实现的契约一致。
 *
 * 刻意不用 `/api/issues/search`：那个端点跑的是整个工作区的全文检索（标题/描述/
 * 评论 `LIKE`、相关度排序、摘要子查询、`COUNT(*) OVER()`），比单点读取贵几个数量级；
 * 而自动链接恰恰是它最主要的调用方（MUL-6268）。
 *
 * 服务端状态 → TanStack Query；key 里同时带上 `wsId` 和标识符，全 App 各处解析
 * 同一标识符时就能共享同一个请求。`enabled` 由调用方自行把关（标识符形状 + 工作区
 * 前缀）。
 */
export function issueIdentifierOptions(wsId: string, identifier: string) {
  return queryOptions({
    queryKey: issueKeys.identifier(wsId, identifier),
    queryFn: async ({ signal }) => {
      try {
        return await api.getIssue(identifier, { signal });
      } catch (err) {
        // 标识符不存在或工作区前缀不对（404）→ 当作纯文本渲染，不报错。
        // 其它任何失败（401/5xx/取消）都必须继续向上抛，让查询被重试或取消，
        // 而不是被误缓存成“没有这个议题”。
        if (err instanceof ApiError && err.status === 404) return null;
        throw err;
      }
    },
    // 标识符→议题的映射实际不可变；同一 key 在大量评论/消息里反复渲染时，
    // 设一段保鲜期可以避免无谓的重取抖动。
    staleTime: 5 * 60_000,
  });
}

/** 拉取子议题完成进度汇总，经 select 转成 parent_issue_id → {done, total} 的 Map，
 * 供父议题卡片/泳道上直接展示“子任务完成 x/y”。 */
export function childIssueProgressOptions(wsId: string) {
  return queryOptions({
    queryKey: issueKeys.childProgress(wsId),
    queryFn: () => api.getChildIssueProgress(),
    select: (data) => {
      const map = new Map<string, { done: number; total: number }>();
      for (const entry of data.progress) {
        map.set(entry.parent_issue_id, { done: entry.done, total: entry.total });
      }
      return map;
    },
  });
}

/** 拉取某个父议题的子议题列表（议题详情的子议题区块等处使用）。 */
export function childIssuesOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: issueKeys.children(wsId, id),
    queryFn: () => api.listChildIssues(id).then((r) => r.issues),
    // 子议题的创建可能发生在“当前工作区不是活跃实时订阅”的时候（例如桌面端某个
    // 标签页正显示另一工作区，而一个 agent 在这期间建了子议题）。若不做这个兜底，
    // 全局 staleTime: Infinity 会让再次打开父议题时直接复用一份不完整的子议题
    // 快照，而之后不一定再有事件来纠正它。
    refetchOnMount: "always",
  });
}

/** 拉取议题的执行树（执行记录按父子任务构成的分层结构）。 */
export function issueExecutionTreeOptions(issueId: string) {
  return queryOptions({
    queryKey: issueKeys.executionTree(issueId),
    queryFn: () => api.getIssueExecutionTree(issueId),
  });
}

/** 服务端对单次 `GET /api/issues/children` 请求里 parent_ids 条数的上限——必须与
 * server/internal/handler/issue.go 里的 `listChildrenByParentsLimit` 保持一致。
 * 超限会返回 400，所以客户端要把更大的请求拆成多块。 */
export const CHILDREN_BY_PARENTS_CHUNK_SIZE = 200;

/** {@link childIssuesOptions} 的批量变体：用 `GET /api/issues/children?parent_ids=…`
 * 一次取回所有给定父议题的子议题，每块最多 {@link CHILDREN_BY_PARENTS_CHUNK_SIZE}
 * 个父议题。queryFn 还会把每个父议题各自的 issueKeys.children 缓存顺带填上，这样
 * 其它界面（议题详情的子议题面板、设置父议题弹窗）能命中已预热的缓存，而不用重新
 * 拉取。水合放在 queryFn 里而不是 useEffect 里，是为了避免 setQueryData → 重渲染 →
 * effect 的循环。
 *
 * 供 SwimLaneView 解析父泳道使用，避免 N 个请求的扇出。调用方必须保证 parentIds
 * 已排序并去重，否则缓存 key 不稳定。
 */
async function fetchAndHydrateChildrenByParents(
  qc: QueryClient,
  wsId: string,
  parentIds: readonly string[],
) {
  // 拆块以遵守服务端上限（块之间互相独立，可以并行发）。
  const chunks: string[][] = [];
  for (let i = 0; i < parentIds.length; i += CHILDREN_BY_PARENTS_CHUNK_SIZE) {
    chunks.push([...parentIds.slice(i, i + CHILDREN_BY_PARENTS_CHUNK_SIZE)]);
  }
  const responses = await Promise.all(chunks.map((c) => api.listChildrenByParents(c)));
  const grouped = new Map<string, Issue[]>();
  for (const response of responses) {
    for (const issue of response.issues) {
      if (!issue.parent_issue_id) continue;
      const bucket = grouped.get(issue.parent_issue_id);
      if (bucket) {
        bucket.push(issue);
      } else {
        grouped.set(issue.parent_issue_id, [issue]);
      }
    }
  }
  for (const [parentId, children] of grouped) {
    // 只在“该父议题的缓存为空”时才水合——不覆盖其它查询（如 issue-detail）可能已
    // 写入的更新结果。这依赖 useUpdateIssue.onMutate 是“写进既有的每父缓存”而不是
    // “新建一份空缓存”；若那条契约变了，这里的批量水合就会悄悄停止给新泳道填数据。
    const existing = qc.getQueryData<Issue[]>(issueKeys.children(wsId, parentId));
    if (!existing || existing.length === 0) {
      qc.setQueryData(issueKeys.children(wsId, parentId), children);
    }
  }
  return grouped;
}

/** 按父议题批量拉取子议题的查询配置（实际逻辑见 fetchAndHydrateChildrenByParents）。
 * parentIds 为空时不发请求。 */
export function childrenByParentsOptions(
  wsId: string,
  parentIds: readonly string[],
  qc: QueryClient,
) {
  return queryOptions({
    queryKey: issueKeys.childrenByParents(wsId, parentIds),
    queryFn: () => fetchAndHydrateChildrenByParents(qc, wsId, parentIds),
    enabled: parentIds.length > 0,
  });
}

/** 一次性取回的议题时间线配置。该端点返回一条议题“评论 + 活动”的完整有序集合
 *（服务端有 2000 条的安全上限）。游标分页在 #1929 被移除——就观察到的数据规模
 *（每条议题 p99 约 30 条）而言，它徒增复杂度却没有体验收益，还会在分页边界上
 * 拆散回复线程。 */
export function issueTimelineOptions(issueId: string) {
  return queryOptions({
    queryKey: issueKeys.timeline(issueId),
    queryFn: () => api.listTimeline(issueId),
  });
}

/** 议题的表情回应（reactions）查询配置：回应随议题详情接口一起返回，这里只取那一组。 */
export function issueReactionsOptions(issueId: string) {
  return queryOptions({
    queryKey: issueKeys.reactions(issueId),
    queryFn: async () => {
      const issue = await api.getIssue(issueId);
      return issue.reactions ?? [];
    },
  });
}

/** 议题订阅者（关注该议题的用户）列表的查询配置。 */
export function issueSubscribersOptions(issueId: string) {
  return queryOptions({
    queryKey: issueKeys.subscribers(issueId),
    queryFn: () => api.listIssueSubscribers(issueId),
  });
}

/** 议题被引用/用量（usage）数据的查询配置。 */
export function issueUsageOptions(issueId: string) {
  return queryOptions({
    queryKey: issueKeys.usage(issueId),
    queryFn: () => api.getIssueUsage(issueId),
  });
}

// 支撑描述编辑器的“即时签名下载”流程：NodeViews 拿 markdown 里的 URL 与这份列表
// 比对，从而解析出对应的附件 id。这份列表属于“工作区私有元数据”，缓存生命周期与
// 议题详情表面的其它缓存保持一致。
export function issueAttachmentsOptions(issueId: string) {
  return queryOptions({
    queryKey: issueKeys.attachments(issueId),
    queryFn: () => api.listAttachments(issueId),
  });
}
