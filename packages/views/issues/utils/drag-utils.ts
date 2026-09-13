// 看板拖拽的工具函数：碰撞检测、分组 id 与拖拽列 id 的互相换算、拖拽落点位置计算
// （before/after 定位）、把拖拽结果换算成一次写操作的更新字段等。纯函数、不碰 React。
import {
  pointerWithin,
  closestCenter,
  type CollisionDetection,
} from "@dnd-kit/core";
import type { Issue, IssueAssigneeType, IssueStatus, UpdateIssueRequest } from "@multica/core/types";
import type { IssueGrouping } from "@multica/core/issues/stores/view-store";
import { propertyIdFromViewKey } from "@multica/core/issues/stores/view-store";
import { issueColumnCategory } from "@multica/core/issues";
import type { BoardColumnGroup } from "../components/board-column";

/** 拖拽移动目标列时要写的问题字段子集。 */
export type DragMoveTargetUpdates = Pick<
  UpdateIssueRequest,
  "status" | "assignee_type" | "assignee_id" | "project_id" | "position"
>;

/** 一次拖拽移动的完整更新：目标字段 + 相对谁插入（before_id / after_id）。 */
export type DragMoveUpdates = DragMoveTargetUpdates & {
  before_id: string | null;
  after_id: string | null;
};

const UNASSIGNED_GROUP_ID = "assignee:unassigned";

/**
 * 看板专用碰撞检测：优先按指针所在位置判定；命中结果里如果只有分组（列）本身、
 * 没有真正的卡片，就退回这些分组，否则按最近中心点判定。
 * @param groupIds - 被视为「列」的拖拽 id 集合（卡片不与列相撞）。
 */
export function makeKanbanCollision(groupIds: Set<string>): CollisionDetection {
  return (args) => {
    const pointer = pointerWithin(args);
    if (pointer.length > 0) {
      const items = pointer.filter((c) => !groupIds.has(c.id as string));
      if (items.length > 0) return items;
      return pointer;
    }
    return closestCenter(args);
  };
}

/** 状态分组 id：`status:<状态key>`。 */
export function statusGroupId(status: IssueStatus): string {
  return `status:${status}`;
}

/** 自定义属性分组 id：`property:<属性id>:<选项id>`，无选项用 "none"。 */
export function propertyGroupId(propertyId: string, optionId: string | null): string {
  return `property:${propertyId}:${optionId ?? "none"}`;
}

/** 指派人分组 id：`assignee:<类型>:<id>`；未指派一律归 `assignee:unassigned`。 */
export function assigneeGroupId(
  type: IssueAssigneeType | null,
  id: string | null,
): string {
  return type && id ? `assignee:${type}:${id}` : UNASSIGNED_GROUP_ID;
}

/** 镜像服务端的项目分组 key（`project:<id>` / `project:none`），
 *  让「由描述符建的列」和「由卡片归的列」对上号。 */
export function projectGroupId(projectId: string | null): string {
  return `project:${projectId ?? "none"}`;
}

/**
 * 计算一个问题应归入哪个分组 id。
 * @param issue - 要分组的问题。
 * @param grouping - 当前分组方式。
 * @param knownOptionIds - 当前仍生效的自定义属性选项 id 集合（可选）。
 */
export function getIssueGroupId(
  issue: Issue,
  grouping: IssueGrouping,
  knownOptionIds?: ReadonlySet<string>,
): string {
  // 状态列是「分类」，所以卡片按它「表现为」的分类入桶。按原始 key 入桶会让自定义
  // 状态得到一个不存在的列 id，卡片便整个从看板/列表中消失（MUL-6409）。
  if (grouping === "status") return statusGroupId(issueColumnCategory(issue));
  if (grouping === "project") return projectGroupId(issue.project_id ?? null);
  const propertyId = propertyIdFromViewKey(grouping);
  if (propertyId) {
    const value = issue.properties?.[propertyId];
    let optionId = typeof value === "string" ? value : null;
    // 值指向一个定义里已不存在的选项（在「在用守卫」出现前删掉的，或被更新版本的
    // 服务端删掉的），必须归进「无值」列——一个对不上的列 id 会悄悄把问题从看板丢掉。
    if (optionId !== null && knownOptionIds && !knownOptionIds.has(optionId)) {
      optionId = null;
    }
    return propertyGroupId(propertyId, optionId);
  }
  return assigneeGroupId(issue.assignee_type, issue.assignee_id);
}

/**
 * 把问题按当前分组方式摊进各组：返回一个「列 id → 问题 id 列表」的映射。
 * @param issues - 全部问题。
 * @param groups - 已知的分组（列）。
 * @param grouping - 当前分组方式。
 * @param knownOptionIds - 仍生效的自定义属性选项 id 集合（可选）。
 */
export function buildColumns(
  issues: Issue[],
  groups: BoardColumnGroup[],
  grouping: IssueGrouping,
  knownOptionIds?: ReadonlySet<string>,
): Record<string, string[]> {
  const cols: Record<string, string[]> = {};
  for (const group of groups) cols[group.id] = [];
  for (const issue of issues) {
    const gid = getIssueGroupId(issue, grouping, knownOptionIds);
    if (cols[gid]) cols[gid].push(issue.id);
  }
  return cols;
}

/**
 * 为被拖动的卡片算一个不与邻居冲突的 position 值：取前后两张卡 position 的中点，
 * 拖到开头则比第二张小 1、拖到末尾则比最后一张大 1。问题个数为 1 或找不到时
 * 返回原 position 或 0。
 * @param ids - 目标列当前按序排列的问题 id。
 * @param activeId - 被拖动的卡片 id。
 * @param issueMap - id → 问题 的映射（读取 position）。
 */
export function computePosition(ids: string[], activeId: string, issueMap: Map<string, Issue>): number {
  const idx = ids.indexOf(activeId);
  if (idx === -1) return 0;
  const getPos = (id: string) => issueMap.get(id)?.position ?? 0;
  if (ids.length === 1) return issueMap.get(activeId)?.position ?? 0;
  if (idx === 0) return getPos(ids[1]!) - 1;
  if (idx === ids.length - 1) return getPos(ids[idx - 1]!) + 1;
  return (getPos(ids[idx - 1]!) + getPos(ids[idx + 1]!)) / 2;
}

/** 由当前相邻顺序推导拖放的 before_id / after_id 锚点。 */
export function getMoveAnchors(
  ids: readonly string[],
  activeId: string,
): Pick<DragMoveUpdates, "before_id" | "after_id"> {
  const index = ids.indexOf(activeId);
  return {
    before_id: index > 0 ? ids[index - 1]! : null,
    after_id: index >= 0 && index < ids.length - 1 ? ids[index + 1]! : null,
  };
}

/**
 * 把 `id` 按「position 升序所暗示的槽位」插入 `ids`，每张卡的 position 从 `issueMap`
 * 读取。镜像 `@multica/core/issues/cache-helpers` 里的 `insertByPosition`，
 * 让看板放下时的乐观排位与最终 reconcile 重建的缓存一致——否则卡片会先落在一个槽位，
 * 随后本地列从 TQ 重新推导时又跳一次。
 */
export function insertIdByPosition(
  ids: string[],
  id: string,
  position: number,
  issueMap: Map<string, Issue>,
): string[] {
  const idx = ids.findIndex((existing) => {
    const p = issueMap.get(existing)?.position;
    return p !== undefined && p > position;
  });
  if (idx === -1) return [...ids, id];
  return [...ids.slice(0, idx), id, ...ids.slice(idx)];
}

/**
 * 找出一个 id 当前所在的列：优先按「本身是列 id」判断（拖进空列），否则在列映射里
 * 查哪一列包含它。
 */
export function findColumn(
  columns: Record<string, string[]>,
  id: string,
  columnIds: Set<string>,
): string | null {
  if (columnIds.has(id)) return id;
  for (const [columnId, ids] of Object.entries(columns)) {
    if (ids.includes(id)) return columnId;
  }
  return null;
}

/** 判断某问题是否已经属于某个分组（列）。 */
export function issueMatchesGroup(issue: Issue, group: BoardColumnGroup): boolean {
  // 「这张卡是不是已经在这列里？」——和列本身一样是分类问题。拿原始 key 比较会让
  // 每个自定义状态都答「否」，于是一次什么都没改变的拖放也会触发一次状态写入（MUL-6409）。
  if (group.status) return issueColumnCategory(issue) === group.status;
  if (group.propertyId !== undefined) {
    const value = issue.properties?.[group.propertyId];
    const optionId = typeof value === "string" ? value : null;
    return optionId === (group.propertyOptionId ?? null);
  }
  if (group.projectId !== undefined) {
    return (issue.project_id ?? null) === group.projectId;
  }
  return (
    (issue.assignee_type ?? null) === (group.assigneeType ?? null) &&
    (issue.assignee_id ?? null) === (group.assigneeId ?? null)
  );
}

/**
 * 把一次拖放换算成要写的问题字段更新。
 * @param group - 拖放目标所在的列。
 * @param position - 拖放后算出的 position。
 * @param issue - 正在被拖的卡片（调用方有的话）。状态列命名的是「分类」，而一张
 *   处于自定义状态上的卡片已经在列里了、只是带着不同的 key——所以照抄列的规范 key
 *   会把 `awaiting_response` 悄悄改写成 `in_review`，而一次只改了行序的拖拽会因此
 *   启动一次智能体运行（MUL-6409）。
 * @returns 需要写的问题字段；列内纯排序时可能只含 position。
 */
export function getMoveUpdates(
  group: BoardColumnGroup,
  position: number,
  /** 正在被拖的那张卡（调用方有的话）。 */
  issue?: Pick<Issue, "status" | "status_category">,
): DragMoveTargetUpdates {
  if (group.status) {
    const keepsStatus =
      issue !== undefined &&
      issue.status !== group.status &&
      issueColumnCategory(issue) === group.status;
    if (keepsStatus) return { position };
    return { status: group.status, position };
  }
  // 属性列：值的变化不属于 UpdateIssueRequest——看板在 position 移动之后通过
  // useSetIssueProperty 应用它。
  if (group.propertyId !== undefined) return { position };
  if (group.projectId !== undefined) {
    return { project_id: group.projectId, position };
  }
  return {
    assignee_type: group.assigneeType ?? null,
    assignee_id: group.assigneeId ?? null,
    position,
  };
}
