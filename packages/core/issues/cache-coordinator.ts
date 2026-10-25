// 议题缓存的“单一规则表”：统一描述一次议题变更如何在 React Query 缓存里传播。
import { issueStatusCategory, normalizeStatusPatch } from "./status-category";
import {
  hashKey,
  type InfiniteData,
  type QueryClient,
  type QueryKey,
} from "@tanstack/react-query";
import {
  issueKeys,
  type IssueFlatFilter,
  type IssueSortParam,
  type MyIssuesFilter,
} from "./queries";
import { inboxKeys, type ArchivedInboxCache } from "../inbox/queries";
import { patchInboxIssueProjection } from "../inbox/ws-updaters";
import { projectKeys } from "../projects/queries";
import {
  decrementBucketTotal,
  findIssueLocation,
  moveBucketTotal,
  patchIssueInBuckets,
  patchNeedsInvalidation,
  removeIssueFromBuckets,
} from "./cache-helpers";
import {
  issueMatchesListFilter,
  listFilterDependsOn,
  type IssueChangedDims,
} from "./surface/membership";
import type {
  InboxItem,
  Issue,
  IssueTableRowsResponse,
  ListIssuesCache,
  ListIssuesResponse,
} from "../types";

export type IssueFlatCache = InfiniteData<ListIssuesResponse, number>;
export type IssueTableRowCache = IssueTableRowsResponse;

/**
 * IssueCacheCoordinator —— 一张“规则表”，统一描述一次议题变更如何在查询缓存中传播。
 *
 * 所有写路径都汇聚到这里：`useUpdateIssue`（onMutate 乐观更新 + onSuccess 服务端对账）、
 * `useBatchUpdateIssues`，以及 WS 的 `issue:updated` 处理器，都会调用 {@link applyIssueChange}。
 * 因此“我改了它”和“别人改了它”从构造上就遵循同一套规则。
 *
 * 对每个已加载的分桶列表（工作区看板 + 每个 myList 视图——My Issues、Project、
 * actor 面板、工作区成员/Agent 标签页），规则如下：
 *
 *   卡片在、且该变更不影响该列表的过滤条件 → 精确修补（状态变化时重新分桶、
 *     position 变化时按槽位插入；绝不整表重取——重取可见列表正是拖拽闪烁的根源）
 *   卡片在、但已不再匹配该列表的过滤条件 → 精确移除（桶 total 减一）——
 *     这是“议题离开了这个视图”的场景，盲目的字段修补会把它漏掉（MUL-3669）
 *   卡片在、但客户端无法判断成员关系（涉及 / my:all）→ 修补 + 把 key 标记为过期
 *   卡片不在、且该变更不可能影响此列表 → 跳过
 *   卡片不在、仍是成员 + 状态变了 → 立即在两个桶之间挪动 1 个服务器 total，
 *     然后把 key 标记为过期：目标桶已加载窗口里的行槽位是服务器知识，而在
 *     staleTime: Infinity 下，显式失效是唯一能补上它的通道
 *   卡片不在、已离开列表（被重新分配 / 重新投影）→ 原状态桶 total 减一
 *   卡片不在、可能“进入”了，或任何无法确定的情况（没有基础实体、成员关系未知）
 *     → 标记 key 过期（绝不硬插入：在该列表排序+过滤下正确的页/槽位是服务器知识）
 *
 * 过期的 key 不在这里失效——时机是调用方的职责：mutation 会推迟到 onSettled
 * 再失效（在飞行中失效会重取未提交的状态、覆盖乐观补丁），而 WS 路径立即失效
 * （服务器已提交）。
 *
 * 详情缓存和 Inbox 的议题投影在同一轮里一并修补。无法从单个实体重新计算的聚合投影
 * （按负责人分组的看板、Gantt、项目指标）走 {@link invalidateIssueDerivatives}。
 */

export interface IssueCacheChangeResult {
  /** 本次变更触碰过的每个缓存的“变更前快照”——onError 时回传给 {@link rollbackIssueChange}。 */
  prevLists: [QueryKey, ListIssuesCache][];
  prevFlatLists: [QueryKey, IssueFlatCache][];
  prevTableRows: [QueryKey, IssueTableRowCache][];
  prevDetail: Issue | undefined;
  prevInboxList: InboxItem[] | undefined;
  prevArchivedInboxCaches: [QueryKey, ArchivedInboxCache | undefined][] | undefined;
  /** Loaded list keys whose server result may have drifted (membership
   *  unknown, possible enter/leave beyond the loaded window, bucket-count
   *  drift). Invalidate on settle (mutation) or immediately (WS). */
  staleKeys: QueryKey[];
  /** 对账过程中找到的最新“变更前”议题副本——调用方用它做父子关系记账，无需重新扫描。 */
  prevIssue: Issue | undefined;
}

/** 从分桶列表的 query key 里解析出服务器契约。`myListSorted` 类型的 key 形如
 *  `["issues", wsId, "my", scope, filter, sort]`；工作区列表是 `["issues", wsId, "list", sort]`
 *  且不带过滤条件。上游的 `byStatus` 形状检查会把同前缀下的分组/平铺缓存挡在这条路径之外。 */
function listContractFromKey(key: QueryKey): {
  scope: string | undefined;
  filter: MyIssuesFilter;
  sort: IssueSortParam;
} {
  if (key[2] === "my") {
    return {
      scope: typeof key[3] === "string" ? key[3] : undefined,
      filter: (key[4] ?? {}) as MyIssuesFilter,
      sort: (key[5] ?? {}) as IssueSortParam,
    };
  }
  return {
    scope: undefined,
    filter: {},
    sort: (key[3] ?? {}) as IssueSortParam,
  };
}

/**
 * 带“形状过滤”的缓存扫描。
 *
 * `getQueriesData` 按 key 的“前缀”匹配，而每个议题视图前缀也会覆盖到形状不同的兄弟查询：
 * `myAll` 会覆盖到按负责人分组的缓存，`tableAll` 会覆盖到行分页旁边的分组（infinite）和
 * 分面缓存，`flatAll` 会覆盖到导出窗口。从这些兄弟缓存上读 `data.rows` / `data.pages` /
 * `data.byStatus` 会抛错（“Cannot read properties of undefined”），而在 mutation 的
 * onSuccess 里抛错，表现就是“服务器已接受、但本地写失败”（MUL-6394）。
 * 所有扫描都走这些辅助函数，保证新调用点不会忘记做形状检查。
 */
export function bucketedListEntries(
  qc: QueryClient,
  wsId: string,
): [QueryKey, ListIssuesCache][] {
  return [
    ...qc.getQueriesData<ListIssuesCache>({ queryKey: issueKeys.list(wsId) }),
    ...qc.getQueriesData<ListIssuesCache>({ queryKey: issueKeys.myAll(wsId) }),
  ].filter(
    (entry): entry is [QueryKey, ListIssuesCache] => !!entry[1]?.byStatus,
  );
}

export function flatListEntries(
  qc: QueryClient,
  wsId: string,
): [QueryKey, IssueFlatCache][] {
  return qc
    .getQueriesData<IssueFlatCache>({ queryKey: issueKeys.flatAll(wsId) })
    .filter(
      (entry): entry is [QueryKey, IssueFlatCache] =>
        !!entry[1] && Array.isArray(entry[1].pages),
    );
}

export function tableRowEntries(
  qc: QueryClient,
  wsId: string,
): [QueryKey, IssueTableRowCache][] {
  return qc
    .getQueriesData<unknown>({ queryKey: issueKeys.tableAll(wsId) })
    .filter(
      (entry): entry is [QueryKey, IssueTableRowCache] =>
        !!entry[1] &&
        typeof entry[1] === "object" &&
        Array.isArray((entry[1] as IssueTableRowCache).rows),
    );
}

/** `prefix` 下、内容为纯 `Issue[]` 的缓存——即每个父议题的子议题列表和项目 Gantt 列表。 */
export function issueArrayEntries(
  qc: QueryClient,
  prefix: readonly unknown[],
): [QueryKey, Issue[]][] {
  return qc
    .getQueriesData<Issue[]>({ queryKey: prefix })
    .filter((entry): entry is [QueryKey, Issue[]] => Array.isArray(entry[1]));
}

function flatContractFromKey(key: QueryKey): {
  scope: string | undefined;
  filter: IssueFlatFilter;
  sort: IssueSortParam;
} {
  return {
    scope: typeof key[3] === "string" ? key[3] : undefined,
    filter: (key[4] ?? {}) as IssueFlatFilter,
    sort: (key[5] ?? {}) as IssueSortParam,
  };
}

function patchFieldChanged<K extends keyof Issue>(
  patch: Partial<Issue>,
  base: Issue | undefined,
  field: K,
) {
  if (!Object.prototype.hasOwnProperty.call(patch, field)) return false;
  return !base || !Object.is(patch[field], base[field]);
}

/** 相对 `base`，该补丁是否至少改了一个议题字段。
 *  每次持久化的编辑服务器端都会推进 `updated_at`（尽管乐观请求体不带该时间戳），
 *  所以这个函数对按 `updated_at` 排序的视图，等价于“updated_at 是否前进了”。 */
function patchChangesAnyIssueField(
  patch: Partial<Issue>,
  base: Issue | undefined,
): boolean {
  return Object.keys(patch).some((field) =>
    patchFieldChanged(patch, base, field as keyof Issue),
  );
}

// 直接改动这些字段才算议题的“语义活动”。纯位置移动有意排除在外：那只是排版编辑，
// 不算用户可见的活动。完整的服务器快照会带 last_activity_at，所以优先用那个权威时钟；
// 这个字段列表是混合版本 / 乐观补丁在还没带该时间戳时的回退方案。
const issueActivityFields = [
  "title",
  "description",
  "status",
  "priority",
  "assignee_type",
  "assignee_id",
  "start_date",
  "due_date",
  "parent_issue_id",
  "project_id",
  "stage",
] as const satisfies readonly (keyof Issue)[];

function patchChangesIssueActivity(
  patch: Partial<Issue>,
  base: Issue | undefined,
): boolean {
  if (Object.prototype.hasOwnProperty.call(patch, "last_activity_at")) {
    return patchFieldChanged(patch, base, "last_activity_at");
  }
  return issueActivityFields.some((field) =>
    patchFieldChanged(patch, base, field),
  );
}

/** 判断一个补丁是否会改变某平铺窗口的成员关系或排序。
 *  已加载的行总是被乐观修补；只有当窗口的服务器契约依赖被改的字段时，才需要后续重取。 */
function flatWindowNeedsReconcile(
  key: QueryKey,
  patch: Partial<Issue>,
  base: Issue | undefined,
  changed: IssueChangedDims,
) {
  const { scope, filter, sort } = flatContractFromKey(key);
  const anyIssueFieldChanged = patchChangesAnyIssueField(patch, base);

  if (listFilterDependsOn(scope, filter, changed)) return true;
  if (filter.q && patchFieldChanged(patch, base, "title")) return true;
  if (changed.status && (filter.statuses?.length ?? 0) > 0) return true;
  if (
    patchFieldChanged(patch, base, "priority") &&
    (filter.priorities?.length ?? 0) > 0
  ) {
    return true;
  }
  if (
    changed.assignee &&
    ((filter.assignee_filters?.length ?? 0) > 0 || filter.include_no_assignee)
  ) {
    return true;
  }
  if (changed.project && ((filter.project_ids?.length ?? 0) > 0 || filter.include_no_project)) {
    return true;
  }
  if (patchFieldChanged(patch, base, "parent_issue_id") && filter.top_level_only) {
    return true;
  }
  if (sort.date_field === "updated_at" && anyIssueFieldChanged) return true;
  if (
    sort.date_field === "created_at" &&
    patchFieldChanged(patch, base, "created_at")
  ) {
    return true;
  }

  switch (sort.sort_by ?? "position") {
    case "title":
      return patchFieldChanged(patch, base, "title");
    case "status":
      return changed.status;
    case "priority":
      return patchFieldChanged(patch, base, "priority");
    case "created_at":
      return patchFieldChanged(patch, base, "created_at");
    case "updated_at":
      // 服务端会给每次落库的议题编辑刷新 updated_at，即使乐观请求的载荷里并没有
      // 携带新的服务端时间戳——所以只要“任一业务字段被改”，按 updated_at 排序的
      // 列表就要重排/重取，不能因为补丁里没带时间戳就误判为“没动过”。
      return anyIssueFieldChanged;
    case "last_activity":
      return patchChangesIssueActivity(patch, base);
    case "start_date":
      return patchFieldChanged(patch, base, "start_date");
    case "due_date":
      return patchFieldChanged(patch, base, "due_date");
    case "position":
      return patchFieldChanged(patch, base, "position");
    default:
      // 按自定义属性值排序的列表不在这里处理：那种排序由专属的属性 mutation/WS
      // 管线对账（它持有完整的属性包快照）。落到 default 意味着本次补丁不会牵动
      // 该排序依据，直接返回 false 即可。
      return false;
  }
}

/**
 * 把一次议题变更应用到 React Query 缓存的所有相关视图上——乐观更新的核心入口。
 * @param qc 查询客户端。
 * @param wsId 工作区 id。
 * @param id 被改议题的 id。
 * @param rawPatch 要合并进议题的字段补丁（通常只有被改动的字段）。
 * @param opts 附加选项。
 * @returns 本次变更触碰过的所有缓存的“变更前快照”，供 onError 回滚。
 */
export function applyIssueChange(
  qc: QueryClient,
  wsId: string,
  id: string,
  rawPatch: Partial<Issue>,
  opts: {
    /** 本次变更实际动了哪些“成员关系维度”——mutation 用 `issueChangedDims` 计算，
     *  WS 路径用服务器下发的标记。 */
    changed: IssueChangedDims;
    /** 变更前最新的完整实体，用于判断“卡片未加载”的列表的成员关系。
     *  省略它会让这些判断退化成“未知”→ 推迟重取，但绝不会补错。 */
    baseIssue?: Issue;
    /** 可选的逐缓存准入守卫。Realtime 用它拒绝一个非递增的 revision 写入新缓存，
     *  同时仍然修复另一个持有同一议题旧 revision 的已加载投影。 */
    acceptCurrent?: (current: Issue) => boolean;
  },
): IssueCacheChangeResult {
  const { changed, baseIssue, acceptCurrent = () => true } = opts;
  // 只在入口处归一化一次。下面每次写入都是 `{...entity, ...patch}` 的展开，
  // 一个乐观的 `{status}` 补丁若不归一化，会让实体在卡片换桶时还带着过期的
  // status_category（状态分类）。（MUL-6243）
  const patch = normalizeStatusPatch(rawPatch);
  const prevLists: [QueryKey, ListIssuesCache][] = [];
  const prevFlatLists: [QueryKey, IssueFlatCache][] = [];
  const prevTableRows: [QueryKey, IssueTableRowCache][] = [];
  const staleKeys: QueryKey[] = [];
  let prevIssue: Issue | undefined = baseIssue;

  for (const [key, data] of bucketedListEntries(qc, wsId)) {
    const { scope, filter, sort } = listContractFromKey(key);
    const loc = findIssueLocation(data, id);
    if (loc && !acceptCurrent(loc.issue)) continue;
    const filterTouched = listFilterDependsOn(scope, filter, changed);

    // “按更新日期”排序：每次持久化编辑都会推进 updated_at，但乐观补丁不带服务器时间戳，
    // 已加载的卡片又留在原槽位，于是看板顺序已经漂移了。正确的槽位是服务器知识——
    // 把 key 标记为过期，让重取重新排序。这与 flatWindowNeedsReconcile 里的 updated_at
    // 分支一致，同样不判断该列表的成员关系（窗口外的成员也应该浮上来）。
    if (
      sort.sort_by === "updated_at" &&
      patchChangesAnyIssueField(patch, loc?.issue ?? baseIssue)
    ) {
      staleKeys.push(key);
    }
    if (
      sort.sort_by === "last_activity" &&
      patchChangesIssueActivity(patch, loc?.issue ?? baseIssue)
    ) {
      staleKeys.push(key);
    }

    if (loc) {
      if (!prevIssue) prevIssue = loc.issue;
      // 客户端无法解析出分类的状态会让 patchIssueInBuckets 变成空操作。
      // 但这行在服务器上确实移动了，若当作“无事发生”，卡片会永远卡在旧列——
      // 所以要强制重取。（MUL-6243）
      if (patchNeedsInvalidation(patch)) staleKeys.push(key);
      let next: ListIssuesCache;
      if (filterTouched) {
        const membership = issueMatchesListFilter(
          { ...loc.issue, ...patch },
          scope,
          filter,
        );
        if (membership === false) {
          next = removeIssueFromBuckets(data, id);
        } else {
          next = patchIssueInBuckets(data, id, patch);
          if (membership === "unknown") staleKeys.push(key);
        }
      } else {
        next = patchIssueInBuckets(data, id, patch);
      }
      if (next !== data) {
        prevLists.push([key, data]);
        qc.setQueryData<ListIssuesCache>(key, next);
      }
      continue;
    }

    // 这里没加载这张卡片。只有“可能让议题跨过该列表过滤条件”的变更——
    // 或者让已加载窗口之外的议题的某个状态计数变化——才需要对账；其余都是空操作。
    if (!filterTouched && !changed.status) continue;
    const wasMember = baseIssue
      ? issueMatchesListFilter(baseIssue, scope, filter)
      : "unknown";
    const isMember = issueMatchesListFilter(
      { ...baseIssue, ...patch },
      scope,
      filter,
    );
    // 变更前后该议题都不属于这个列表——它的分页和计数都不用动。
    if (wasMember === false && isMember === false) continue;

    // 确定性的计数运算——按成员关系的“结果”分支，而不是按改了哪个字段分支，
    // 这样 status / assignee / project（以及将来的 team）都走同样的两种情况。
    // wasMember === true 意味着 baseIssue 存在，所以旧状态是已知的。
    if (wasMember === true && baseIssue) {
      if (isMember === true) {
        // 仍是成员。只有状态变化才会在桶之间挪计数；其它情况
        // （例如成员→成员的重新分配）不会动这个列表的分页和计数。
        if (!changed.status || patch.status === undefined) continue;
        // 桶的 total 按分类计（MUL-6243）。
        // 服务器发来 patch.status_category 时以它为准；只有 key 的回退方案覆盖内置状态。
        const fromCategory = issueStatusCategory(baseIssue);
        const toCategory = issueStatusCategory({
          status: patch.status,
          status_category: patch.status_category,
        });
        if (!fromCategory || !toCategory) {
          // 无法解析的自定义状态绝不能静默空操作：这行在服务器上已经移动了，
          // 缓存不动会让窗口外的 total 永久偏离。改为强制重取。
          staleKeys.push(key);
          continue;
        }
        const next = moveBucketTotal(data, fromCategory, toCategory);
        if (next !== data) {
          prevLists.push([key, data]);
          qc.setQueryData<ListIssuesCache>(key, next);
          // 计数动了，但这行没法在客户端插入（它在列表排序下的页/槽位是服务器知识）。
          // 保留这个对账触发：在 staleTime: Infinity 下，没有别的东西能让目标桶的窗口
          // 跟它的 total 对齐。
          staleKeys.push(key);
        }
        continue;
      }
      if (isMember === false) {
        // 彻底离开了这个列表——它原来被计入的那个桶减一。
        const leavingCategory = issueStatusCategory(baseIssue);
        if (!leavingCategory) {
          staleKeys.push(key);
          continue;
        }
        const next = decrementBucketTotal(data, leavingCategory);
        if (next !== data) {
          prevLists.push([key, data]);
          qc.setQueryData<ListIssuesCache>(key, next);
        }
        continue;
      }
    }
    // “进入”了（它在列表排序下的页/槽位是服务器知识），或任何不确定的情况
    // （没有 base、成员关系未知）→ 与其猜，不如重取。
    staleKeys.push(key);
  }

  // 立即修补已加载的平铺行。只有其编码的服务器过滤/排序确实依赖本次变更的窗口
  // 才需要重取；例如在一个按 position 排序、无过滤的表格里改标题，补丁本身就完全对账了。
  for (const [key, data] of flatListEntries(qc, wsId)) {
    let found: Issue | undefined;
    const pages = data.pages.map((page) => ({
      ...page,
      issues: page.issues.map((issue) => {
        if (issue.id !== id) return issue;
        if (!acceptCurrent(issue)) return issue;
        found = issue;
        return { ...issue, ...patch };
      }),
    }));
    if (found) {
      if (!prevIssue) prevIssue = found;
      prevFlatLists.push([key, data]);
      qc.setQueryData<IssueFlatCache>(key, { ...data, pages });
    }
    if (flatWindowNeedsReconcile(key, patch, found ?? baseIssue, changed)) {
      staleKeys.push(key);
    }
  }

  // Table 分支页是“服务器部分拥有的投影”，所以成员关系、计数和排序仍通过 settle 时
  // 已有的 tableAll 失效来对账。但每一行里的实体快照是确定的：立即修补它，
  // 让行内编辑在权威分支重取期间不会闪现旧的标题/状态/负责人。
  for (const [key, data] of tableRowEntries(qc, wsId)) {
    let found: Issue | undefined;
    const rows = data.rows.map((row) => {
      if (row.issue.id !== id) return row;
      if (!acceptCurrent(row.issue)) return row;
      found = row.issue;
      return { ...row, issue: { ...row.issue, ...patch } };
    });
    if (!found) continue;
    if (!prevIssue) prevIssue = found;
    prevTableRows.push([key, data]);
    qc.setQueryData<IssueTableRowCache>(key, { ...data, rows });
  }

  const prevDetail = qc.getQueryData<Issue>(issueKeys.detail(wsId, id));
  if (prevDetail && acceptCurrent(prevDetail)) {
    qc.setQueryData<Issue>(issueKeys.detail(wsId, id), {
      ...prevDetail,
      ...patch,
    });
    if (!prevIssue) prevIssue = prevDetail;
  }

  // Inbox 行带着用于展示和过滤的议题 status/priority 快照。议题才是真实状态，
  // 所以两个投影都要立即跟随每一次写入。
  let prevInboxList: InboxItem[] | undefined;
  let prevArchivedInboxCaches: [QueryKey, ArchivedInboxCache | undefined][] | undefined;
  if (patch.status !== undefined || patch.priority !== undefined) {
    prevInboxList = qc.getQueryData<InboxItem[]>(inboxKeys.list(wsId));
    prevArchivedInboxCaches = qc.getQueriesData<ArchivedInboxCache>({ queryKey: inboxKeys.archived(wsId) });
    // Membership and facets are server-owned; callers refresh after commit.
    // Full issue events also carry unchanged status/priority on title edits.
    const archiveProjectionChanged =
      (patch.status !== undefined && (changed.status || !prevIssue || prevIssue.status !== patch.status)) ||
      (patch.priority !== undefined && (!prevIssue || prevIssue.priority !== patch.priority));
    if (archiveProjectionChanged) {
      staleKeys.push(...qc.getQueryCache().findAll({ queryKey: inboxKeys.archived(wsId) })
        .filter((query) => query.queryKey.length > inboxKeys.archived(wsId).length)
        .map((query) => query.queryKey));
      staleKeys.push(...qc.getQueryCache().findAll({ queryKey: inboxKeys.facets(wsId) }).map((query) => query.queryKey));
    }
    patchInboxIssueProjection(qc, wsId, id, {
      status: patch.status,
      priority: patch.priority,
    });
  }

  return {
    prevLists,
    prevFlatLists,
    prevTableRows,
    prevDetail,
    prevInboxList,
    prevArchivedInboxCaches,
    staleKeys,
    prevIssue,
  };
}

/** 恢复 {@link applyIssueChange} 捕获的所有快照——乐观更新生命周期的 onError 回滚分支。 */
export function rollbackIssueChange(
  qc: QueryClient,
  wsId: string,
  id: string,
  result: Pick<
    IssueCacheChangeResult,
    | "prevLists"
    | "prevFlatLists"
    | "prevTableRows"
    | "prevDetail"
    | "prevInboxList"
    | "prevArchivedInboxCaches"
  >,
) {
  for (const [key, snapshot] of result.prevLists) {
    qc.setQueryData(key, snapshot);
  }
  for (const [key, snapshot] of result.prevFlatLists) {
    qc.setQueryData(key, snapshot);
  }
  for (const [key, snapshot] of result.prevTableRows) {
    qc.setQueryData(key, snapshot);
  }
  if (result.prevDetail !== undefined) {
    qc.setQueryData(issueKeys.detail(wsId, id), result.prevDetail);
  }
  if (result.prevInboxList !== undefined) {
    qc.setQueryData(inboxKeys.list(wsId), result.prevInboxList);
  }
  for (const [key, snapshot] of result.prevArchivedInboxCaches ?? []) {
    qc.setQueryData(key, snapshot);
  }
}

/**
 * 刷新那些单实体补丁无法重新计算的聚合投影：按负责人分组的看板（重新分组是服务器逻辑）、
 * 每个项目 Gantt（排期成员关系 + 行镜像），以及当变更可能改变各项目计数时的项目指标。
 */
export function invalidateIssueDerivatives(
  qc: QueryClient,
  wsId: string,
  opts: { statusOrProjectChanged: boolean },
) {
  qc.invalidateQueries({ queryKey: issueKeys.assigneeGroupsAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.myAssigneeGroupsAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.projectGanttAll(wsId) });
  if (opts.statusOrProjectChanged) {
    qc.invalidateQueries({ queryKey: projectKeys.all(wsId) });
  }
}

/** 当 query key 的某个对象片段编码了指定的排序方式时返回 true。
 *  分桶、平铺和分组视图用 `sort_by`；服务器 Table 查询用嵌套的 `sort.field` 契约。 */
function queryKeyHasSort(key: QueryKey, field: string): boolean {
  return key.some(
    (part) => {
      if (!part || typeof part !== "object" || Array.isArray(part)) return false;
      const record = part as Record<string, unknown>;
      if (record.sort_by === field) return true;
      const sort = record.sort;
      return (
        !!sort &&
        typeof sort === "object" &&
        !Array.isArray(sort) &&
        (sort as Record<string, unknown>).field === field
      );
    },
  );
}

/**
 * 重取所有按“更新日期”排序的已加载议题列表/看板，让刚刚推进了 `updated_at` 的卡片
 * 重新排到它真正的位置。用于那些会推进 `updated_at`、却不带新时间戳或字段补丁的事件：
 * `comment:created`（MUL-5009）以及属性/元数据的 WS 事件——它们都在服务器端推进了议题的
 * `updated_at`，却绕过了协调器的字段 diff 路径。覆盖状态看板、平铺表格，以及按负责人分组的
 * 看板（工作区 + My Issues）；只触碰按 `updated_at` 排序的 key。这次重取是权威的
 * （服务器排序 + 打破平局的规则），也能把坐在加载窗口之外被触碰的卡片浮出来。
 */
export function invalidateUpdatedAtSortedIssueLists(
  qc: QueryClient,
  wsId: string,
): void {
  qc.invalidateQueries({
    queryKey: issueKeys.all(wsId),
    predicate: (query) => queryKeyHasSort(query.queryKey, "updated_at"),
  });
}

/** 只重取按“语义活动”排序的议题视图。辅助类 mutation 不带完整 Issue 快照、
 *  也没有可排序的时间戳，所以权威重取是恢复其顺序的唯一安全手段。 */
export function invalidateLastActivitySortedIssueLists(
  qc: QueryClient,
  wsId: string,
): void {
  qc.invalidateQueries({
    queryKey: issueKeys.all(wsId),
    predicate: (query) => queryKeyHasSort(query.queryKey, "last_activity"),
  });
}

/** 去重后失效 {@link applyIssueChange} 上报的过期 key——对 N 个议题的批量操作
 *  可能把同一个 key 上报 N 次。 */
export function invalidateStaleListKeys(qc: QueryClient, staleKeys: QueryKey[]) {
  const seen = new Set<string>();
  for (const key of staleKeys) {
    const hash = hashKey(key);
    if (seen.has(hash)) continue;
    seen.add(hash);
    if (key[0] === "inbox") {
      // A first archive page/facet request may predate this committed issue
      // change. Cancel before invalidation even when it has no cached data.
      void qc.cancelQueries({ queryKey: key, exact: true }).then(() =>
        qc.invalidateQueries({ queryKey: key, exact: true }));
    } else {
      qc.invalidateQueries({ queryKey: key, exact: true });
    }
  }
}
