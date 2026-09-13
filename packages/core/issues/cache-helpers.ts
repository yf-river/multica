// 提供对议题分桶缓存（按状态分列）的读写与就地增删改工具，是缓存协调器的底层操作集。
import type {
  Issue,
  IssueStatusCategory,
  IssueStatusBucket,
  ListIssuesCache,
} from "../types";
import { PAGINATED_CATEGORIES } from "./queries";
import { issueStatusCategory, normalizeStatusPatch } from "./status-category";

const EMPTY_BUCKET: IssueStatusBucket = { issues: [], total: 0 };

/** 读取某个状态分类对应的分桶；该分类没有数据时返回一个空桶。 */
export function getBucket(
  resp: ListIssuesCache,
  status: IssueStatusCategory,
): IssueStatusBucket {
  return resp.byStatus[status] ?? EMPTY_BUCKET;
}

/** 覆盖写入某个状态分类的分桶，返回一份新的缓存对象（不改动入参）。 */
export function setBucket(
  resp: ListIssuesCache,
  status: IssueStatusCategory,
  bucket: IssueStatusBucket,
): ListIssuesCache {
  return { ...resp, byStatus: { ...resp.byStatus, [status]: bucket } };
}

/** 定位某个议题 id 当前落在哪个状态分桶里，找不到返回 `null`。 */
export function findIssueLocation(
  resp: ListIssuesCache,
  id: string,
): { status: IssueStatusCategory; issue: Issue } | null {
  for (const status of PAGINATED_CATEGORIES) {
    const bucket = resp.byStatus[status];
    const found = bucket?.issues.find((i: Issue) => i.id === id);
    if (found) return { status, issue: found };
  }
  return null;
}

/** 把一个议题加进它所属的状态分桶（已存在则什么都不做）。 */
export function addIssueToBuckets(
  resp: ListIssuesCache,
  issue: Issue,
): ListIssuesCache {
  // 无法解析的自定义状态没有对应的列可以放。这种情况应该由调用方去失效重取，
  // 而不是静默丢弃——否则看板上会少一行服务器上确实存在的议题。
  // 调用方应在调用前用 `issueStatusCategory(issue) === null` 先判断。
  const category = issueStatusCategory(issue);
  if (!category) return resp;
  const bucket = getBucket(resp, category);
  if (bucket.issues.some((i: Issue) => i.id === issue.id)) return resp;
  return setBucket(resp, category, {
    issues: [...bucket.issues, issue],
    total: bucket.total + 1,
  });
}

/** 从包含该议题的分桶里把它移除（不知道在哪一列也能用）。 */
export function removeIssueFromBuckets(
  resp: ListIssuesCache,
  id: string,
): ListIssuesCache {
  const loc = findIssueLocation(resp, id);
  if (!loc) return resp;
  const bucket = getBucket(resp, loc.status);
  return setBucket(resp, loc.status, {
    issues: bucket.issues.filter((i) => i.id !== id),
    total: Math.max(0, bucket.total - 1),
  });
}

/**
 * 只改数量的对账，用于“已加载窗口之外”的议题：它的状态在服务器端变了，
 * 所以 `total` 从一个桶挪 1 到另一个桶，而已加载的数组不动。
 * `hasMore`（已加载 < total）自然保持一致。
 */
export function moveBucketTotal(
  resp: ListIssuesCache,
  from: IssueStatusCategory,
  to: IssueStatusCategory,
): ListIssuesCache {
  if (from === to) return resp;
  const fromBucket = getBucket(resp, from);
  const toBucket = getBucket(resp, to);
  let next = setBucket(resp, from, {
    ...fromBucket,
    total: Math.max(0, fromBucket.total - 1),
  });
  next = setBucket(next, to, { ...toBucket, total: toBucket.total + 1 });
  return next;
}

/**
 * 只改数量的对账，用于“已加载窗口之外”且已离开本列表的议题
 * （被重新分配 / 挪到别的项目）：它原来所在的分桶数量减 1。
 */
export function decrementBucketTotal(
  resp: ListIssuesCache,
  status: IssueStatusCategory,
): ListIssuesCache {
  const bucket = getBucket(resp, status);
  return setBucket(resp, status, {
    ...bucket,
    total: Math.max(0, bucket.total - 1),
  });
}

/**
 * 按 `position` 升序把 `issue` 插到 `issues` 中正确的位置——与看板渲染顺序一致
 * （对应服务器 `ORDER BY position ASC`）。返回新数组，不改动入参。
 *
 * 插到正确槽位（而不是追加到末尾）是为了让乐观移动不跳变：卡片落在服务器确认后
 * 它应有的位置，之后缓存刷新时不会被“瞬移”到列尾。
 */
export function insertByPosition(issues: Issue[], issue: Issue): Issue[] {
  const idx = issues.findIndex((i) => i.position > issue.position);
  if (idx === -1) return [...issues, issue];
  return [...issues.slice(0, idx), issue, ...issues.slice(idx)];
}

/**
 * 把 `patch` 合并到 id 对应的议题上。若 `patch.status` 与当前所在桶不同，
 * 该议题会移动到新桶，两个桶的 total 也随之调整。被移动的卡片（以及同一列里
 * position 变化的卡片）会重新插到按 `position` 排序的槽位，而不是追加到末尾，
 * 从而保证缓存顺序与看板渲染一致。纯字段更新（状态/位置都没变）则保持原位。
 */
export function patchIssueInBuckets(
  resp: ListIssuesCache,
  id: string,
  rawPatch: Partial<Issue>,
): ListIssuesCache {
  const loc = findIssueLocation(resp, id);
  if (!loc) return resp;
  // 先做归一化，让合并后的实体 status_category 和它最终落到的桶一致；
  // 直接裸合并会保留旧的 category。（MUL-6243）
  const patch = normalizeStatusPatch(rawPatch);
  const merged: Issue = { ...loc.issue, ...patch };

  // 在比较之前先解析出“目标分类”。`loc.status` 是“分类”（桶的 key），
  // 而 `patch.status` 是“状态 key”，直接比较会把同分类下的 key 变更——
  // 比如 `in_review` 换成自定义的 `human_review`——误判成跨桶移动。
  // 那会先删再插到同一个桶（用的是删除前的快照），导致卡片重复、total 偏大。
  //
  // 只从 `patch` 解析，绝不用 `merged`：merged 继承的是“旧议题”的 status_category，
  // 真实移动后会被静默留在旧列。
  const nextCategory =
    patch.status === undefined
      ? loc.status
      : issueStatusCategory({ status: patch.status, status_category: patch.status_category });

  // 无法解析的自定义状态：没有桶可放。这里不动缓存，交给调用方去失效——
  // 见 patchNeedsInvalidation。
  if (!nextCategory) return resp;

  if (nextCategory === loc.status) {
    const bucket = getBucket(resp, loc.status);
    const positionChanged =
      patch.position !== undefined && patch.position !== loc.issue.position;
    if (!positionChanged) {
      // 纯字段更新（标签、元数据、标题、同分类的状态 key 变更……）：保持原槽位，
      // 让远程编辑不会打乱一个本就没变的列，也不去改 total。
      return setBucket(resp, loc.status, {
        ...bucket,
        issues: bucket.issues.map((i: Issue) => (i.id === id ? merged : i)),
      });
    }
    // 同列内重排：先把卡片拎出来，再插回它的新槽位。
    return setBucket(resp, loc.status, {
      ...bucket,
      issues: insertByPosition(
        bucket.issues.filter((i: Issue) => i.id !== id),
        merged,
      ),
    });
  }

  const fromBucket = getBucket(resp, loc.status);
  const toBucket = getBucket(resp, nextCategory);
  let next = setBucket(resp, loc.status, {
    issues: fromBucket.issues.filter((i: Issue) => i.id !== id),
    total: Math.max(0, fromBucket.total - 1),
  });
  next = setBucket(next, nextCategory, {
    issues: insertByPosition(toBucket.issues, merged),
    total: toBucket.total + 1,
  });
  return next;
}

/**
 * 当状态补丁指向一个客户端无法解析出分类的状态时返回 true，
 * 这时 `patchIssueInBuckets` 会什么都不做。调用方必须选择失效重取，
 * 而不是当作“无事发生”——这行在服务器上已经移动了，缓存不动会让列和它的
 * total 永久偏离。
 */
export function patchNeedsInvalidation(patch: Partial<Issue>): boolean {
  if (patch.status === undefined) return false;
  return (
    issueStatusCategory({ status: patch.status, status_category: patch.status_category }) === null
  );
}
