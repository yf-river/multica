// 客户端问题筛选工具：定义筛选模型（IssueFilters / IssueFilterState）以及统一的应用
// 逻辑 applyIssueFilters——按正向选择模型过滤（空数组 = 不过滤）。与自定义属性筛选、
// 服务端算子（contains/gt/…）保持一致。纯函数，供各类问题界面与历史遗留调用复用。
import type { Issue, IssueStatus, IssuePriority, IssueAssigneeGroup, PropertyFilterValue, PropertyOperatorFilter } from "@multica/core/types";
import type { ActorFilterValue } from "@multica/core/issues/stores/view-store";
import type { IssueActivityState } from "../surface/activity";

/** 面向调用方的筛选描述（供本地过滤使用）。 */
export interface IssueFilters {
  statusFilters: IssueStatus[];
  priorityFilters: IssuePriority[];
  assigneeFilters: ActorFilterValue[];
  includeNoAssignee: boolean;
  /** 把「显式激活的指派人筛选」与常规的「空数组 = 无筛选」状态区分开。当它为 true、
   *  未选任何指派人且 includeNoAssignee=false 时，筛选有意地一条都不匹配。 */
  assigneeFilterActive?: boolean;
  creatorFilters: ActorFilterValue[];
  projectFilters: string[];
  includeNoProject: boolean;
  labelFilters: string[];
  /** 自定义属性筛选：定义 id → 选中的值（同一定义内 OR，定义之间 AND；checkbox 用
   *  "true"/"false"）。 */
  propertyFilters?: Record<string, PropertyFilterValue[]>;
  // 当 `agentRunningFilter` 为 true 时，只保留 id 在 `runningIssueIds` 里的问题。
  // surface 从独立的 `/api/working-agents` 投影推导这个集合，让 filter.ts 保持零拉取。
  agentRunningFilter?: boolean;
  runningIssueIds?: ReadonlySet<string>;
  // 「显示子问题」显示开关。显式 `false` 时隐藏有父问题的问题、只留顶层。
  // undefined / true 保持显示一切的默认行为，所以省略它（以及移动端的位置变体）的
  // 既有调用方不受影响。
  showSubIssues?: boolean;
}

/** 传入 applyIssueFilters 的完整筛选状态（已把 showSubIssues 规整为显式形态）。 */
export interface IssueFilterState {
  statusFilters: IssueStatus[];
  priorityFilters: IssuePriority[];
  assigneeFilters: ActorFilterValue[];
  includeNoAssignee: boolean;
  /** 见 IssueFilters.assigneeFilterActive。 */
  assigneeFilterActive?: boolean;
  creatorFilters: ActorFilterValue[];
  projectFilters: string[];
  includeNoProject: boolean;
  labelFilters: string[];
  propertyFilters?: Record<string, PropertyFilterValue[]>;
  workingOnly: boolean;
  /** 见 IssueFilters.showSubIssues——只有显式 `false` 才隐藏。 */
  showSubIssues?: boolean;
}

/** 过滤器在判定「是否正在工作中」时需要的上下文。 */
export interface IssueFilterContext {
  activityByIssueId?: ReadonlyMap<string, IssueActivityState>;
  runningIssueIds?: ReadonlySet<string>;
}

/**
 * 选中「自定义属性无值」的哨兵值。镜像后端 `parsePropertiesFilterParam` 里的哨兵
 * （`server/internal/handler/property.go`）；不会与真实选项 id 冲突（select 选项是
 * UUID，checkbox 用 "true"/"false"）。
 */
export const NO_PROPERTY_VALUE = "__none__";

/**
 * 判断一个存下来的属性值是否命中某个操作符筛选成员。镜像服务端
 * `parsePropertiesFilterParam`（`server/internal/handler/property.go`）里的操作符谓词：
 *
 * - `contains` 只对存下来的字符串做大小写不敏感的子串测试（服务端受守卫的
 *   `ILIKE '%…%'`），所以 "Foo" 与 "foo" 一致。
 * - `gt`/`gte`/`lt`/`lte` 只匹配数值类型的存值——服务端用
 *   `jsonb_typeof(...) = 'number'` 守卫，文本值在这里同样是未命中。
 * - `before`/`after` 只匹配字符串值并按字典序比较；对服务端存和筛的 "YYYY-MM-DD"
 *   纯日期字符串，字典序恰好是时间序。
 *
 * 缺失的 key 永不命中操作符（服务端 `->>` 得到 NULL，任何操作符谓词都不满足）——
 * 由本函数运行前的调用方处理。
 */
export function issueValueMatchesOperator(
  value: NonNullable<Issue["properties"]>[string],
  filter: PropertyOperatorFilter,
): boolean {
  switch (filter.op) {
    case "contains": {
      const needle = filter.value.toLowerCase();
      // 空 needle 会让任何值都子串命中（"".includes("") 为 true）；服务端直接拒绝
      // 空的算子值，所以这里的匹配也必须拒绝它，而不是对手工改过的已保存视图 blob
      // 全量放行。
      if (needle === "") return false;
      if (typeof value === "string") return value.toLowerCase().includes(needle);
      // 数字、布尔和数组永不命中。虽然 jsonb ->> 能序列化它们，但 contains 在服务端
      // 和客户端都刻意只作文本/URL 算子。
      return false;
    }
    case "gt":
    case "gte":
    case "lt":
    case "lte": {
      if (typeof value !== "number") return false;
      const bound = Number(filter.value);
      if (!Number.isFinite(bound)) return false;
      if (filter.op === "gt") return value > bound;
      if (filter.op === "gte") return value >= bound;
      if (filter.op === "lt") return value < bound;
      return value <= bound;
    }
    case "before":
    case "after": {
      if (typeof value !== "string") return false;
      return filter.op === "before" ? value < filter.value : value > filter.value;
    }
    default:
      return false;
  }
}

/**
 * 判断一个问题是否命中属性筛选。select 值是单个选项 id 字符串，multi_select 值是
 * 选项 id 数组，checkbox 值是布尔（与 "true"/"false" 伪选项比较），标量定义除普通
 * 等值字符串外还接受操作符成员（`PropertyOperatorFilter`）。同一定义内任一成员命中
 * 即可（OR）；每个定义都必须命中（AND）。一个对被筛定义没有值的问题，只在选中
 * 「无值」（NO_PROPERTY_VALUE）伪选项时命中。
 */
export function issueMatchesPropertyFilters(
  issue: Issue,
  propertyFilters: Record<string, PropertyFilterValue[]> | undefined,
): boolean {
  if (!propertyFilters) return true;
  for (const [propertyId, selected] of Object.entries(propertyFilters)) {
    if (selected.length === 0) continue;
    const value = issue.properties?.[propertyId];
    if (value === undefined) {
      if (selected.includes(NO_PROPERTY_VALUE)) continue;
      return false;
    }
    const matched = selected.some((member) => {
      if (member === NO_PROPERTY_VALUE) {
        // 哨兵永不命中一个「有值」的值：字面的 "__none__" 文本是真实的值
        // （服务端的 key 缺失谓词会把它排除在「无值」筛选之外），这条路径必须与
        // 服务端一致。
        return false;
      }
      if (typeof member === "string") {
        if (typeof value === "string") return member === value;
        // 按数值比较，让 "3.50" 和 3.5 与服务端的 jsonb 数字包含一致。
        if (typeof value === "number") return Number(member) === value;
        if (Array.isArray(value)) return value.includes(member);
        if (typeof value === "boolean") return member === String(value);
        return false;
      }
      return issueValueMatchesOperator(value, member);
    });
    if (!matched) return false;
  }
  return true;
}

function issueIsWorking(issueId: string, context: IssueFilterContext) {
  if (context.activityByIssueId) {
    return context.activityByIssueId.get(issueId)?.isWorking === true;
  }
  return context.runningIssueIds?.has(issueId) === true;
}

/**
 * 用正向选择模型过滤问题。空数组 = 不过滤（全显示）；非空 = 只显示匹配的。
 *
 * 指派人有个特殊的「无指派人」开关（includeNoAssignee）：
 * - 只有 includeNoAssignee 为 true → 只显示未指派的问题
 * - assigneeFilters 里有值 → 只显示这些指派人的问题
 * - 两者都开 → 显示匹配的指派人 + 未指派
 */
export function applyIssueFilters(
  issues: Issue[],
  filters: IssueFilterState,
  context: IssueFilterContext = {},
): Issue[] {
  const { statusFilters, priorityFilters, assigneeFilters, includeNoAssignee, creatorFilters, projectFilters, includeNoProject, labelFilters, workingOnly } = filters;
  const hasAssigneeFilter =
    filters.assigneeFilterActive === true ||
    assigneeFilters.length > 0 ||
    includeNoAssignee;
  const hasProjectFilter = projectFilters.length > 0 || includeNoProject;
  // 未开 `agentRunningFilter` 时空集合是空操作。筛选开着但集合缺失/为空时全隐藏——
  // 用户选了「只看运行中」而眼下没有东西在运行。
  const applyWorkingOnly = workingOnly === true;
  const hideSubIssues = filters.showSubIssues === false;

  return issues.filter((issue) => {
    if (applyWorkingOnly && !issueIsWorking(issue.id, context))
      return false;

    if (hideSubIssues && issue.parent_issue_id) return false;

    if (statusFilters.length > 0 && !statusFilters.includes(issue.status))
      return false;

    if (priorityFilters.length > 0 && !priorityFilters.includes(issue.priority))
      return false;

    if (hasAssigneeFilter) {
      if (!issue.assignee_id) {
        // 未指派的问题——只有勾了「无指派人」才显示
        if (!includeNoAssignee) return false;
      } else if (assigneeFilters.length > 0) {
        // 已指派的问题——只有指派人落在筛选列表里才显示
        if (!assigneeFilters.some(
          (f) => f.type === issue.assignee_type && f.id === issue.assignee_id,
        )) return false;
      } else {
        // 只勾了「无指派人」、没选具体指派人 → 隐藏已指派的问题
        return false;
      }
    }

    if (
      creatorFilters.length > 0 &&
      !creatorFilters.some(
        (f) => f.type === issue.creator_type && f.id === issue.creator_id,
      )
    ) {
      return false;
    }

    if (hasProjectFilter) {
      if (!issue.project_id) {
        if (!includeNoProject) return false;
      } else if (projectFilters.length > 0) {
        if (!projectFilters.includes(issue.project_id)) return false;
      } else {
        // 只勾了「无项目」→ 隐藏有项目的问题
        return false;
      }
    }

    if (labelFilters.length > 0) {
      // 标签筛选内是 OR 语义：保留携带任一选中标签的问题。与既有的优先级/项目多选一致。
      const issueLabels = issue.labels;
      if (!issueLabels || issueLabels.length === 0) return false;
      if (!issueLabels.some((l) => labelFilters.includes(l.id))) return false;
    }

    if (!issueMatchesPropertyFilters(issue, filters.propertyFilters)) return false;

    return true;
  });
}

/** 以调用方友好的 IssueFilters 为入参的过滤便捷封装（内部规整成 IssueFilterState）。 */
export function filterIssues(issues: Issue[], filters: IssueFilters): Issue[] {
  return applyIssueFilters(
    issues,
    {
      statusFilters: filters.statusFilters,
      priorityFilters: filters.priorityFilters,
      assigneeFilters: filters.assigneeFilters,
      includeNoAssignee: filters.includeNoAssignee,
      assigneeFilterActive: filters.assigneeFilterActive,
      creatorFilters: filters.creatorFilters,
      projectFilters: filters.projectFilters,
      includeNoProject: filters.includeNoProject,
      labelFilters: filters.labelFilters,
      propertyFilters: filters.propertyFilters,
      workingOnly: filters.agentRunningFilter === true,
      showSubIssues: filters.showSubIssues,
    },
    { runningIssueIds: filters.runningIssueIds },
  );
}

/**
 * 把仅客户端生效的显示筛选重新套用到「服务端已分组」的响应上。
 * 按指派人分组的看板直接渲染 `groups`、绕过了平铺的 `applyIssueFilters` 输出，
 * 所以「显示子问题」开关和 agents-working 快捷筛选必须在这里逐组应用。
 * 会重算每组的总数并丢弃被清空的组。没有客户端筛选激活时按引用返回原输入。
 */
export function filterAssigneeGroups(
  groups: IssueAssigneeGroup[] | undefined,
  filters: {
    showSubIssues?: boolean;
    agentRunningFilter?: boolean;
    runningIssueIds?: ReadonlySet<string>;
    propertyFilters?: Record<string, PropertyFilterValue[]>;
  },
): IssueAssigneeGroup[] | undefined {
  const applyRunning = filters.agentRunningFilter === true;
  const hideSubIssues = filters.showSubIssues === false;
  const hasPropertyFilters = Object.values(filters.propertyFilters ?? {}).some(
    (selected) => selected.length > 0,
  );
  if (!groups || (!applyRunning && !hideSubIssues && !hasPropertyFilters)) return groups;

  const { runningIssueIds } = filters;
  return groups
    .map((group) => {
      const issues = group.issues.filter((issue) => {
        if (applyRunning && !(runningIssueIds?.has(issue.id) ?? false))
          return false;
        if (hideSubIssues && issue.parent_issue_id) return false;
        if (hasPropertyFilters && !issueMatchesPropertyFilters(issue, filters.propertyFilters))
          return false;
        return true;
      });
      return { ...group, issues, total: issues.length };
    })
    .filter((group) => group.issues.length > 0);
}
