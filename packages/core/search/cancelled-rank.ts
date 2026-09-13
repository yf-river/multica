// 本文件实现「跨类型、取消项沉底」的聚合搜索结果显示逻辑（MUL-5824）。
//
// 搜索 API 已经会把取消的工作排到活跃工作之下——但只在「单个结果集内」。
// 每个把 issue 与 project 一起渲染的客户端，聚合的是两次相互独立的响应，所以
// 一个响应里的取消行仍会压在另一个响应里的活跃行上方：命令面板与移动端搜索页
// 都先渲染整个 Projects 列表再渲染整个 Issues 列表，于是单条已取消的 project
// 就能排到每一条活跃 issue 之前。
//
// 修复必须落在「聚合点」，而且必须是横跨两种类型的「稳定」分区：两侧各自内部
// 的相对顺序保持不变，服务端排序仍决定除「活跃在取消之前」以外的所有次序。
// 在任何截断之前先应用它，能让取消行让出的是它们的「名额」而不只是位置。
//
// 直接命中（精确 identifier、精确裸数字、或精确标题）豁免，与服务端一致：
// 那意味着用户正瞄准那一条记录，把它的沉底反而会藏起用户明确要的东西。

import type { SearchIssueResult, SearchProjectResult } from "../types/api";
import { issueBehavesAs } from "../issues/status-category";

/**
 * 镜像服务端的标识符模式（server/internal/handler/issue.go 里的
 * parseQueryNumber）：「MUL-123」或裸「123」。
 */
const IDENTIFIER_NUMBER_RE = /^[a-z]+-(\d+)$/i;

/**
 * 提取查询里瞄准的 issue 编号；没瞄准任何编号时返回 null。
 *
 * @param query - 搜索词
 * @returns issue 编号，或 null
 */
export function parseSearchQueryNumber(query: string): number | null {
  const q = query.trim();
  const m = IDENTIFIER_NUMBER_RE.exec(q);
  if (m) {
    const n = Number.parseInt(m[1]!, 10);
    if (Number.isInteger(n) && n > 0) return n;
  }
  if (/^\d+$/.test(q)) {
    const n = Number.parseInt(q, 10);
    if (Number.isInteger(n) && n > 0) return n;
  }
  return null;
}

/** 标题（去首尾空白、忽略大小写）是否与查询完全一致。 */
function isExactTitle(title: string | null | undefined, query: string): boolean {
  if (!title) return false;
  const q = query.trim();
  if (!q) return false;
  return title.trim().toLowerCase() === q.toLowerCase();
}

/**
 * 某查询是否瞄准了这个具体的 issue：精确 identifier / 裸编号 / 或完整标题。
 *
 * 每个字段都可选，让只拿着行的一部分的调用方也能共用同一条规则——比如
 * @mention 选择器只带着 identifier 和 title，没有 `number`，此时 number 从
 * identifier 里推导。
 *
 * 注意：服务端把标题和一个经 escapeLike 处理的参数比较，所以含 `_` 或 `%`
 * 的标题在服务端永远不算直接命中（一个早已存在的 tier-1 转义怪癖，见
 * buildSearchQuery）。客户端这边比较原始字符串，只会把这样的 issue 留在活跃
 * 分区里——绝不会把服务端保在上面的东西沉下去——所以两侧保持兼容。
 */
export function isIssueDirectHit(
  issue: {
    title?: string | null;
    number?: number | null;
    identifier?: string | null;
  },
  query: string,
): boolean {
  const targetNumber = parseSearchQueryNumber(query);
  if (targetNumber !== null) {
    const issueNumber =
      issue.number ??
      (issue.identifier ? parseSearchQueryNumber(issue.identifier) : null);
    if (issueNumber === targetNumber) return true;
  }
  if (
    issue.identifier &&
    issue.identifier.trim().toLowerCase() === query.trim().toLowerCase()
  ) {
    return true;
  }
  return isExactTitle(issue.title, query);
}

/** 某查询是否瞄准了这个具体的 project：它的完整标题。 */
export function isProjectDirectHit(
  project: { title?: string | null },
  query: string,
): boolean {
  return isExactTitle(project.title, query);
}

/** 一次稳定分区的结果：活跃项与沉底的取消项。 */
export interface CancelledPartition<T> {
  /** 所有未被沉底的项，保持原相对顺序。 */
  live: T[];
  /** 被沉底的取消项，保持原相对顺序。 */
  cancelled: T[];
}

/**
 * 在任意谓词上做稳定分区。拆出来让调用方决定对自己的行形状而言什么算
 * 「已取消且不豁免」，而「保序」的保证集中在一个地方。
 */
export function partitionStable<T>(
  items: readonly T[],
  demote: (item: T) => boolean,
): CancelledPartition<T> {
  const live: T[] = [];
  const cancelled: T[] = [];
  for (const item of items) {
    if (demote(item)) cancelled.push(item);
    else live.push(item);
  }
  return { live, cancelled };
}

/** 聚合搜索结果的渲染分区。 */
export interface AggregatedSearchPartition {
  /** 先渲染的活跃 projects（已去掉被沉底的）。 */
  liveProjects: SearchProjectResult[];
  /** 在活跃 projects 之后渲染的活跃 issues（已去掉被沉底的）。 */
  liveIssues: SearchIssueResult[];
  /**
   * 沉底的取消 projects，再接取消 issues——一个收尾桶，这样任何一种类型的
   * 取消行都不会排到另一种类型的活跃行之前。
   */
  cancelledProjects: SearchProjectResult[];
  cancelledIssues: SearchIssueResult[];
  /** 有任何项被沉底时为 true，让调用方可以跳过一个空区块。 */
  hasCancelled: boolean;
}

/**
 * 把一份全局搜索的结果对（issues + projects）分区供渲染。
 *
 * 渲染顺序必须是：活跃 projects → 活跃 issues → 取消（先 projects 再
 * issues）。要做截断的调用方必须按这个顺序截断拼接结果，被丢掉的正是取消
 * 尾巴。
 */
export function partitionAggregatedSearchResults({
  issues,
  projects,
  query,
}: {
  issues: readonly SearchIssueResult[];
  projects: readonly SearchProjectResult[];
  query: string;
}): AggregatedSearchPartition {
  const issueParts = partitionStable(
    issues,
    // 按「类别」判断：落在取消类别里的自定义状态同样是取消工作，必须以同样
    // 的方式沉底。(MUL-6243)
    (issue) => issueBehavesAs(issue, "cancelled") && !isIssueDirectHit(issue, query),
  );
  const projectParts = partitionStable(
    projects,
    (project) =>
      project.status === "cancelled" && !isProjectDirectHit(project, query),
  );

  return {
    liveProjects: projectParts.live,
    liveIssues: issueParts.live,
    cancelledProjects: projectParts.cancelled,
    cancelledIssues: issueParts.cancelled,
    hasCancelled:
      projectParts.cancelled.length > 0 || issueParts.cancelled.length > 0,
  };
}
