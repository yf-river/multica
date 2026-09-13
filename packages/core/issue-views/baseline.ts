// 本文件实现「当前打开视图的基线」：把已保存视图的查询解析成过滤器菜单能
// 用的「视图固定值」集合与可直接重置回退的原始快照。

import type { ActorFilterValue, FilterSnapshot } from "../issues/stores/view-store";
import type { IssuePriority, IssueStatus, PropertyFilterValue } from "../types";
import { isKnownPropertyFilterOp, isPropertyOperatorFilter, propertyFilterValueKey } from "../types";
import { PRIORITY_DISPLAY_ORDER } from "../issues/config";

/**
 * 当前打开的已保存视图的查询，被规范化后服务两个用途：
 * - 归属判断（`has`）：过滤器菜单把视图固定的值渲染成「选中且不可取消」，
 *   其余选项保持可选；
 * - 重置（`raw`）：移除一条用户加的 chip 时，该维度回到视图自己的值，
 *   而不是回到空。
 *
 * 视图自己的条件从不作为 chip 出现——视图名已经承载了它们；chip 只展示用户
 * 在其上叠加的东西。
 */
export interface IssueViewBaseline {
  status: Set<string>;
  priority: Set<string>;
  /** 经办人 key，形态为 `${type}:${id}`。 */
  assignee: Set<string>;
  includeNoAssignee: boolean;
  creator: Set<string>;
  project: Set<string>;
  includeNoProject: boolean;
  label: Set<string>;
  /** 属性定义 id → 固定成员 key（`propertyFilterValueKey`）。 */
  property: Map<string, Set<string>>;
  /** 做过枚举清洗的快照，可以直接交给 `resetFiltersTo`。 */
  raw: FilterSnapshot;
}

/** 把「类型 + id」的经办人值折叠成稳定 key。 */
export function actorFilterKey(actor: ActorFilterValue): string {
  return `${actor.type}:${actor.id}`;
}

/** 从未知值里取出字符串数组（非数组返回空）。 */
function stringArray(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

/**
 * 清洗某个属性过滤的成员列表。字符串原样放行（相等、"true"/"false"、
 * "__none__"）；运算符对象必须带已知的 op 与字符串值。其余一律丢弃——
 * 手改的 blob 或未来客户端新增的运算符，store 无法表达的值绝不能进快照
 * （与上面枚举过滤同一规则）。
 */
function propertyFilterValueArray(v: unknown): PropertyFilterValue[] {
  if (!Array.isArray(v)) return [];
  return v.filter(
    (x): x is PropertyFilterValue =>
      typeof x === "string" ||
      (isPropertyOperatorFilter(x) && isKnownPropertyFilterOp(x.op)),
  );
}

/** 从未知值里取出「有 id 的经办人」数组。 */
function actorArray(v: unknown): ActorFilterValue[] {
  if (!Array.isArray(v)) return [];
  return v.filter(
    (x): x is ActorFilterValue =>
      !!x && typeof x === "object" && typeof (x as ActorFilterValue).id === "string",
  );
}

/**
 * 从已保存视图的查询参数里构建基线。
 *
 * @param query - 已保存视图的 query 对象（各维度 key 与列表接口一致）
 * @returns 解析好的视图基线
 */
export function baselineFromQuery(query: Record<string, unknown>): IssueViewBaseline {
  // 未知的枚举成员（更新的服务端、手改的 blob）一律丢弃——store 无法表示的
  // 值绝不能进快照。
  //
  // 状态是例外：MUL-6243 之后，状态过滤存的是状态 KEY，而工作区的自定义 key
  // 无法用常量枚举。这里若按 ALL_STATUSES 过滤，会在这份已保存视图被重新打开
  // 的瞬间悄悄删掉所有自定义状态过滤——视图回来时显示的内容比保存时更多。
  // 任何非空字符串都是一个可表示的状态 key。
  const statusFilters = stringArray(query.statusFilters).filter(
    (s): s is IssueStatus => s.length > 0,
  );
  const priorityFilters = stringArray(query.priorityFilters).filter(
    (p): p is IssuePriority => (PRIORITY_DISPLAY_ORDER as readonly string[]).includes(p),
  );
  const assigneeFilters = actorArray(query.assigneeFilters);
  const creatorFilters = actorArray(query.creatorFilters);
  const projectFilters = stringArray(query.projectFilters);
  const labelFilters = stringArray(query.labelFilters);
  const includeNoAssignee = query.includeNoAssignee === true;
  const includeNoProject = query.includeNoProject === true;

  const propertyFilters: Record<string, PropertyFilterValue[]> = {};
  const property = new Map<string, Set<string>>();
  if (query.propertyFilters && typeof query.propertyFilters === "object") {
    for (const [id, selected] of Object.entries(
      query.propertyFilters as Record<string, unknown>,
    )) {
      const values = propertyFilterValueArray(selected);
      if (values.length > 0) {
        propertyFilters[id] = values;
        property.set(id, new Set(values.map(propertyFilterValueKey)));
      }
    }
  }

  return {
    status: new Set(statusFilters),
    priority: new Set(priorityFilters),
    assignee: new Set(assigneeFilters.map(actorFilterKey)),
    includeNoAssignee,
    creator: new Set(creatorFilters.map(actorFilterKey)),
    project: new Set(projectFilters),
    includeNoProject,
    label: new Set(labelFilters),
    property,
    raw: {
      statusFilters,
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      projectFilters,
      includeNoProject,
      labelFilters,
      propertyFilters,
    },
  };
}
