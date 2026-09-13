// 判断某个议题是否还属于某个带过滤的列表缓存（看板/列表/泳道等视图）。
// 这是"确定 → 精确修补、不确定 → 失效重取"规则在列表成员关系上的落点：
// 能确定的就同步缓存，客户端判断不了的（谓词在服务端）就退回到失效。
import type { Issue, UpdateIssueRequest } from "../../types";
import type { MyIssuesFilter } from "../queries";

/**
 * 议题是否属于某个带过滤的列表缓存的判定结果。
 *
 * `"unknown"`（不确定）意味着光靠实体本身客户端无法下结论——过滤条件里有一部分
 * 谓词活在服务端（`involves_user_id` 背后的 agent 归属关系图、my:all 的关系并集），
 * 或者实体缺少过滤所依据的字段。调用方在 `true` 时精确修补、`false` 时移除，
 * `"unknown"` 时修补 + 失效重取。
 */
export type IssueMembership = true | false | "unknown";

/**
 * 一次写操作能触碰、且会让议题进出某个过滤列表（assignee / project）
 * 或改变各状态桶计数（status）的字段分组。创建者（creator）不在其中：
 * 议题创建后 creator 不可变。
 */
export interface IssueChangedDims {
  assignee: boolean;
  project: boolean;
  status: boolean;
}

/**
 * 从变更补丁里推导"变了哪些维度"。`base`（写入前最新缓存的实体，通常是详情缓存）
 * 让结论更精确：给议题写入一个它本来就是这个值的字段等于没变。没有 `base` 时，
 * 任何被写入的成员字段都算作变化——偏保守，最坏情况只是结算时多一次列表重取。
 * @param patch 待应用的变更补丁。
 * @param base 写入前的基线实体（可选），用于判断值是否真的变了。
 * @returns 各维度是否发生变化。
 */
export function issueChangedDims(
  patch: Partial<Issue> | UpdateIssueRequest,
  base?: Issue,
): IssueChangedDims {
  const has = (field: string) =>
    Object.prototype.hasOwnProperty.call(patch, field);
  const p = patch as Partial<Issue>;
  return {
    assignee:
      (has("assignee_id") && (!base || base.assignee_id !== p.assignee_id)) ||
      (has("assignee_type") && (!base || base.assignee_type !== p.assignee_type)),
    project: has("project_id") && (!base || base.project_id !== p.project_id),
    status: has("status") && p.status !== undefined && (!base || base.status !== p.status),
  };
}

/**
 * 这个列表的服务端契约是否依赖被改动的那些维度？
 * `scope` 是 query key 里 myList 的作用域段（不带过滤的工作区列表是 `undefined`）。
 * 返回 false 说明这次写入不可能把议题挪进/挪出该列表，此时做纯字段修补就是完整对账。
 * @param scope myList 作用域段，`undefined` 表示工作区全量列表。
 * @param filter 该视图当前启用的过滤条件。
 * @param changed 本次变更涉及的维度。
 * @returns true 表示该列表可能受这次写入影响。
 */
export function listFilterDependsOn(
  scope: string | undefined,
  filter: MyIssuesFilter,
  changed: IssueChangedDims,
): boolean {
  // my:all 是 assigned / created / involved 的并集——其中 assigned 和
  // involved 两条腿都按负责人（assignee）过滤，所以负责人一变列表就会动。
  if (scope === "all") return changed.assignee;
  if (
    changed.assignee &&
    (filter.assignee_id !== undefined ||
      filter.assignee_ids !== undefined ||
      filter.assignee_types !== undefined ||
      filter.involves_user_id !== undefined)
  ) {
    return true;
  }
  if (changed.project && filter.project_id !== undefined) return true;
  // creator_id 过滤永不响应更新——creator 创建后不可变。
  return false;
}

/**
 * 拿一个议题去对照某列表的服务端契约做成员判定。各过滤字段之间是 AND 语义：
 * 任一字段明确不匹配即返回 `false`；客户端无法求值的谓词（或部分实体缺字段）
 * 会把答案降级成 `"unknown"`，而不是瞎猜。
 * @param issue 待判定的议题（可以是部分字段）。
 * @param scope myList 作用域段，`undefined` 表示工作区全量列表。
 * @param filter 该视图当前启用的过滤条件。
 * @returns true=在列表里 / false=不在 / "unknown"=无法判定。
 */
export function issueMatchesListFilter(
  issue: Partial<Issue>,
  scope: string | undefined,
  filter: MyIssuesFilter,
): IssueMembership {
  // my:all 是多个关系上的并集；其中 involved 这条腿依赖服务端的 agent 归属图，
  // 所以成员关系永远无法在客户端独立判定。
  if (scope === "all") return "unknown";

  let unknown = false;

  if (filter.assignee_id !== undefined) {
    if (issue.assignee_id === undefined) unknown = true;
    else if (issue.assignee_id !== filter.assignee_id) return false;
  }
  if (filter.assignee_ids !== undefined) {
    if (issue.assignee_id === undefined) unknown = true;
    else if (
      issue.assignee_id === null ||
      !filter.assignee_ids.includes(issue.assignee_id)
    ) {
      return false;
    }
  }
  if (filter.assignee_types !== undefined) {
    if (issue.assignee_type === undefined) unknown = true;
    else if (
      issue.assignee_type === null ||
      !filter.assignee_types.includes(issue.assignee_type)
    ) {
      return false;
    }
  }
  if (filter.creator_id !== undefined) {
    if (issue.creator_id === undefined) unknown = true;
    else if (issue.creator_id !== filter.creator_id) return false;
  }
  if (filter.project_id !== undefined) {
    if (issue.project_id === undefined) unknown = true;
    else if (issue.project_id !== filter.project_id) return false;
  }
  if (filter.involves_user_id !== undefined) {
    // 间接负责人谓词（议题归属的 agents / squads）——只有服务端能算。
    unknown = true;
  }

  return unknown ? "unknown" : true;
}
