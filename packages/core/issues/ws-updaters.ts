// 议题相关的 WebSocket 事件处理器：把服务器推送的创建/更新/删除等事件应用到本地缓存。
import { issueStatusCategory } from "./status-category";
import type { QueryClient } from "@tanstack/react-query";
import { issueKeys } from "./queries";
import { labelKeys } from "../labels/queries";
import { projectKeys } from "../projects/queries";
import {
  applyIssueChange,
  bucketedListEntries,
  flatListEntries,
  invalidateIssueDerivatives,
  invalidateLastActivitySortedIssueLists,
  invalidateStaleListKeys,
  invalidateUpdatedAtSortedIssueLists,
  issueArrayEntries,
  tableRowEntries,
  type IssueFlatCache,
} from "./cache-coordinator";
import {
  addIssueToBuckets,
  findIssueLocation,
  patchIssueInBuckets,
} from "./cache-helpers";
import { cleanupDeletedIssueCaches } from "./delete-cache";
import type {
  Issue,
  IssueLabelsResponse,
  IssueMetadata,
  IssuePropertyValues,
  IssueTableRowsResponse,
  Label,
} from "../types";
import type { ListIssuesCache } from "../types";

// 记录各议题“辅助投影”（标签/元数据/属性）最新 revision 的 WeakMap，用于丢弃乱序到达的旧事件。
const auxiliaryIssueRevisions = new WeakMap<QueryClient, Map<string, number>>();

type AuxiliaryIssueProjection = "generic" | "labels" | "metadata" | "properties";

function auxiliaryIssueRevisionPrefix(wsId: string, issueId: string) {
  return `${wsId}:${issueId}:`;
}

function auxiliaryIssueRevisionKey(
  wsId: string,
  issueId: string,
  projection: AuxiliaryIssueProjection,
) {
  return `${auxiliaryIssueRevisionPrefix(wsId, issueId)}${projection}`;
}

function recordAuxiliaryIssueRevision(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  revision: number,
  projection: AuxiliaryIssueProjection,
) {
  let revisions = auxiliaryIssueRevisions.get(qc);
  if (!revisions) {
    revisions = new Map();
    auxiliaryIssueRevisions.set(qc, revisions);
  }
  const key = auxiliaryIssueRevisionKey(wsId, issueId, projection);
  if ((revisions.get(key) ?? 0) < revision) revisions.set(key, revision);
}

function isOlderThanRecordedAuxiliaryProjection(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  revision: number | undefined,
  projection: AuxiliaryIssueProjection,
) {
  if (revision === undefined) return false;
  const recorded = auxiliaryIssueRevisions
    .get(qc)
    ?.get(auxiliaryIssueRevisionKey(wsId, issueId, projection));
  return recorded !== undefined && revision < recorded;
}

/** 用一份完整快照的 revision 对账：清掉不高于它的辅助投影记录，并对仍更新的投影做失效。 */
export function reconcileIssueFullSnapshotRevision(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  fullRevision: number | undefined,
) {
  if (fullRevision === undefined) return;
  const revisions = auxiliaryIssueRevisions.get(qc);
  if (!revisions) return;
  const prefix = auxiliaryIssueRevisionPrefix(wsId, issueId);
  let newestAuxiliaryRevision: number | undefined;
  for (const [key, revision] of revisions) {
    if (!key.startsWith(prefix)) continue;
    if (fullRevision >= revision) {
      revisions.delete(key);
    } else if (
      newestAuxiliaryRevision === undefined ||
      revision > newestAuxiliaryRevision
    ) {
      newestAuxiliaryRevision = revision;
    }
  }
  if (newestAuxiliaryRevision === undefined) return;
  // setQueryData 会清掉一个非活跃查询的 stale 标记。当这份完整快照仍落后于
  // 我们已经观察到的辅助事件时，把它恢复。
  invalidateStaleIssueOwnerProjections(
    qc,
    wsId,
    issueId,
    newestAuxiliaryRevision,
  );
}

function mergeIssuePatch(
  issue: Issue,
  patch: Partial<Issue>,
  orderRevision?: number,
): Issue {
  if (
    orderRevision !== undefined &&
    issue.revision !== undefined &&
    orderRevision < issue.revision
  ) {
    return issue;
  }
  return { ...issue, ...patch };
}

function patchIssueInFlatCaches(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  patch: Partial<Issue>,
  orderRevision?: number,
) {
  for (const [key, data] of flatListEntries(qc, wsId)) {
    qc.setQueryData<IssueFlatCache>(key, {
      ...data,
      pages: data.pages.map((page) => ({
        ...page,
        issues: page.issues.map((issue) =>
          issue.id === issueId
            ? mergeIssuePatch(issue, patch, orderRevision)
            : issue,
        ),
      })),
    });
  }
}

/** 修补每个已加载的“按父议题”缓存里反规范化的议题快照。 */
function patchIssueInChildrenCaches(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  patch: Partial<Issue>,
  orderRevision?: number,
) {
  for (const [key, data] of issueArrayEntries(qc, issueKeys.childrenAll(wsId))) {
    if (!data.some((child) => child.id === issueId)) continue;
    qc.setQueryData<Issue[]>(
      key,
      data.map((child) =>
        child.id === issueId
          ? mergeIssuePatch(child, patch, orderRevision)
          : child,
      ),
    );
  }
}

function patchIssueInTableCaches(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  patch: Partial<Issue>,
  orderRevision?: number,
) {
  for (const [key, page] of tableRowEntries(qc, wsId)) {
    if (!page.rows.some((row) => row.issue.id === issueId)) continue;
    qc.setQueryData<IssueTableRowsResponse>(key, {
      ...page,
      rows: page.rows.map((row) =>
        row.issue.id === issueId
          ? {
              ...row,
              issue: mergeIssuePatch(row.issue, patch, orderRevision),
            }
          : row,
      ),
    });
  }
}

function patchIssueSnapshot(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  patch: Partial<Issue>,
  orderRevision?: number,
) {
  for (const [key, data] of bucketedListEntries(qc, wsId)) {
    const location = findIssueLocation(data, issueId);
    if (
      !location ||
      mergeIssuePatch(location.issue, patch, orderRevision) === location.issue
    ) {
      continue;
    }
    qc.setQueryData<ListIssuesCache>(key, patchIssueInBuckets(data, issueId, patch));
  }
  patchIssueInFlatCaches(qc, wsId, issueId, patch, orderRevision);
  patchIssueInTableCaches(qc, wsId, issueId, patch, orderRevision);
  patchIssueInChildrenCaches(qc, wsId, issueId, patch, orderRevision);
  qc.setQueryData<Issue>(issueKeys.detail(wsId, issueId), (old) =>
    old ? mergeIssuePatch(old, patch, orderRevision) : old,
  );
  for (const [key, data] of issueArrayEntries(
    qc,
    issueKeys.projectGanttAll(wsId),
  )) {
    qc.setQueryData<Issue[]>(
      key,
      data.map((issue) =>
        issue.id === issueId
          ? mergeIssuePatch(issue, patch, orderRevision)
          : issue,
      ),
    );
  }
}

function freshestCachedIssueRevision(
  qc: QueryClient,
  wsId: string,
  issueId: string,
): number | undefined {
  let freshest = qc.getQueryData<Issue>(issueKeys.detail(wsId, issueId))?.revision;
  const consider = (issue: Issue | undefined) => {
    if (issue?.revision !== undefined && (freshest === undefined || issue.revision > freshest)) {
      freshest = issue.revision;
    }
  };
  for (const [, data] of bucketedListEntries(qc, wsId)) {
    consider(findIssueLocation(data, issueId)?.issue);
  }
  for (const [, data] of flatListEntries(qc, wsId)) {
    for (const page of data.pages) {
      consider(page.issues.find((issue) => issue.id === issueId));
    }
  }
  for (const [, data] of tableRowEntries(qc, wsId)) {
    consider(data.rows.find((row) => row.issue.id === issueId)?.issue);
  }
  for (const prefix of [issueKeys.childrenAll(wsId), issueKeys.projectGanttAll(wsId)]) {
    for (const [, data] of issueArrayEntries(qc, prefix)) {
      consider(data.find((issue) => issue.id === issueId));
    }
  }
  return freshest;
}

function refetchForUnversionedIssueEvent(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  revision: number | undefined,
): boolean {
  if (revision !== undefined) return false;
  if (freshestCachedIssueRevision(qc, wsId, issueId) === undefined) return false;
  // 混合版本上线期：一旦任一投影有了有序 revision，旧服务器的无版本快照就不能再安全应用了。
  qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
  return true;
}

function invalidateIssueOwnerProjectionsWhere(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  shouldInvalidate: (issue: Issue | undefined) => boolean,
) {
  const invalidateExact = (queryKey: readonly unknown[]) => {
    qc.invalidateQueries({ queryKey, exact: true });
  };

  const detailKey = issueKeys.detail(wsId, issueId);
  if (shouldInvalidate(qc.getQueryData<Issue>(detailKey))) invalidateExact(detailKey);

  for (const [key, data] of bucketedListEntries(qc, wsId)) {
    if (shouldInvalidate(findIssueLocation(data, issueId)?.issue)) {
      invalidateExact(key);
    }
  }
  for (const [key, data] of flatListEntries(qc, wsId)) {
    if (data.pages.some((page) => shouldInvalidate(page.issues.find((issue) => issue.id === issueId)))) {
      invalidateExact(key);
    }
  }
  for (const [key, data] of tableRowEntries(qc, wsId)) {
    if (data.rows.some((row) => row.issue.id === issueId && shouldInvalidate(row.issue))) {
      invalidateExact(key);
    }
  }
  for (const prefix of [issueKeys.childrenAll(wsId), issueKeys.projectGanttAll(wsId)]) {
    for (const [key, data] of issueArrayEntries(qc, prefix)) {
      if (data.some((issue) => issue.id === issueId && shouldInvalidate(issue))) {
        invalidateExact(key);
      }
    }
  }
}

function invalidateStaleIssueOwnerProjections(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  revision: number,
) {
  invalidateIssueOwnerProjectionsWhere(
    qc,
    wsId,
    issueId,
    (issue) =>
      issue !== undefined &&
      (issue.revision === undefined || issue.revision < revision),
  );
}

/** 成功但 HTTP 响应带不了 owner revision 的辅助 mutation 的回退方案
 * （典型如 204 的评论删除）。只失效“包含该议题”的已加载投影。 */
export function invalidateIssueOwnerProjections(
  qc: QueryClient,
  wsId: string,
  issueId: string,
) {
  invalidateIssueOwnerProjectionsWhere(
    qc,
    wsId,
    issueId,
    (issue) => issue !== undefined,
  );
}

/**
 * 一个辅助事件证明了存在更新的聚合 owner revision，但它不携带该 revision 的完整
 * Issue 快照。如果在这里推进缓存的 Issue.revision，会让一份“更旧但更新”的完整快照
 * 看起来过期，并可能让 title 之类的字段永久滞留。所以让 Issue.revision 保持为该缓存里
 * 实际存在的完整快照的顺序，只把过期的投影标记为权威重取。
 */
export function onIssueAuxiliaryRevision(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  revision: number | undefined,
  projection: AuxiliaryIssueProjection = "generic",
) {
  if (!revision || revision <= 0) return;
  recordAuxiliaryIssueRevision(qc, wsId, issueId, revision, projection);
  invalidateStaleIssueOwnerProjections(qc, wsId, issueId, revision);
}

function findIssueInFlatCaches(
  qc: QueryClient,
  wsId: string,
  issueId: string,
) {
  for (const [, data] of flatListEntries(qc, wsId)) {
    for (const page of data.pages) {
      const issue = page.issues.find((candidate) => candidate.id === issueId);
      if (issue) return issue;
    }
  }
  return undefined;
}

/** 处理 `issue:created` 事件：把新议题加入列表缓存，并失效各类聚合缓存。 */
export function onIssueCreated(
  qc: QueryClient,
  wsId: string,
  issue: Issue,
) {
  // 客户端无法解析出分类的自定义状态，没有可入的桶。往哪都不插会静默隐藏一个
  // 服务器上确实存在的议题，所以改为失效列表、让重取来摆放它。（MUL-6243）
  const bucketable = issueStatusCategory(issue) !== null;
  for (const [key, data] of qc.getQueriesData<ListIssuesCache>({ queryKey: issueKeys.list(wsId) })) {
    if (!data) continue;
    if (bucketable) {
      qc.setQueryData<ListIssuesCache>(key, addIssueToBuckets(data, issue));
    } else {
      qc.invalidateQueries({ queryKey: key });
    }
  }
  qc.invalidateQueries({ queryKey: issueKeys.myAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.flatAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.assigneeGroupsAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.myAssigneeGroupsAll(wsId) });
  if (issue.project_id) {
    qc.invalidateQueries({ queryKey: projectKeys.all(wsId) });
  }
  // 刷新每个可能正在观察该议题的 Project Gantt 缓存。这里失效整个前缀而不是
  // 议题自己的项目，因为新议题未必已经排期；活跃的 Gantt 页面（如果有）会重取，
  // 符合条件时再把它捡起来。
  qc.invalidateQueries({ queryKey: issueKeys.projectGanttAll(wsId) });
  if (issue.parent_issue_id) {
    qc.invalidateQueries({ queryKey: issueKeys.children(wsId, issue.parent_issue_id) });
    qc.invalidateQueries({ queryKey: issueKeys.childProgress(wsId) });
  }
}

/**
 * 处理 `issue:updated` 事件：把服务器下发的部分议题变更应用到所有相关缓存。
 * @param qc 查询客户端。
 * @param wsId 工作区 id。
 * @param issue 服务器下发的议题补丁（必含 id）。
 * @param meta 服务器 issue:updated 事件自带的变更标记，权威地表示这次写是否动了成员关系维度。
 */
export function onIssueUpdated(
  qc: QueryClient,
  wsId: string,
  issue: Partial<Issue> & { id: string },
  // assigneeChanged / statusChanged / projectChanged 来自服务器的 issue:updated 标记——
  // 是权威的“这次写是否动了成员关系维度”信号。它们喂给协调器的 changed-dims 入参，
  // 让非成员关系变更（标题/位置/优先级/标签）保持每个已加载列表原地不动，而不是去重取。
  meta: {
    assigneeChanged?: boolean;
    statusChanged?: boolean;
    projectChanged?: boolean;
  } = {},
) {
  // 在改动缓存状态之前先查好“旧的父议题 + 已缓存实体”，这样既能保持父议题的
  // 子议题缓存同步（支撑父议题页上的子议题列表），又能用 diff 回退出变更标记。
  const listQueries = qc.getQueriesData<ListIssuesCache>({ queryKey: issueKeys.list(wsId) });
  const firstListData = listQueries[0]?.[1];
  const detailData = qc.getQueryData<Issue>(issueKeys.detail(wsId, issue.id));
  const cachedIssue =
    detailData ??
    (firstListData ? findIssueLocation(firstListData, issue.id)?.issue : undefined) ??
    findIssueInFlatCaches(qc, wsId, issue.id);
  if (refetchForUnversionedIssueEvent(qc, wsId, issue.id, issue.revision)) {
    return;
  }
  if (
    issue.revision !== undefined &&
    cachedIssue?.revision !== undefined &&
    issue.revision < cachedIssue.revision
  ) {
    return;
  }
  const oldParentId =
    detailData?.parent_issue_id ?? cachedIssue?.parent_issue_id ?? null;
  // 当 parent_issue_id 变化时，新父议题来自 WS 载荷
  const newParentId = issue.parent_issue_id ?? null;
  const parentChanged =
    issue.parent_issue_id !== undefined && newParentId !== oldParentId;

  // 优先用服务器的标记（权威、在线上设置）。只在某个标记缺失（旧后端）时才回退到
  // 把载荷与缓存副本做 diff：一旦本地乐观移动覆盖了缓存值，diff 就不可靠了，但它仍能
  // 覆盖远程/agent 的变更，并避免“新前端 + 旧后端”的组合退步（MUL-3669 / #4548）。
  // 本地移动本身由 useUpdateIssue 自己的协调器一轮覆盖，从不依赖这些标记。
  const oldProjectId = detailData?.project_id ?? cachedIssue?.project_id ?? null;
  const changed = {
    assignee:
      meta.assigneeChanged ??
      (cachedIssue !== undefined &&
        ((issue.assignee_id !== undefined &&
          issue.assignee_id !== cachedIssue.assignee_id) ||
          (issue.assignee_type !== undefined &&
            issue.assignee_type !== cachedIssue.assignee_type))),
    project:
      meta.projectChanged ??
      (issue.project_id !== undefined && (issue.project_id ?? null) !== oldProjectId),
    status:
      meta.statusChanged ??
      (cachedIssue !== undefined &&
        issue.status !== undefined &&
        issue.status !== cachedIssue.status),
  };

  // 协调器套用与本地 mutation 相同的规则表：卡片已加载且仍属于该列表时精确修补/换桶，
  // 变更把它挪出某过滤视图时精确移除，以及为补丁修不了的漂移（窗口外进出、成员关系
  // 无法判定、屏外桶计数）产出过期 key。服务器已经提交，所以过期 key 立即冲刷。
  const change = applyIssueChange(qc, wsId, issue.id, issue, {
    changed,
    baseIssue: cachedIssue,
    acceptCurrent: (current) =>
      issue.revision === undefined ||
      current.revision === undefined ||
      issue.revision > current.revision,
  });
  invalidateStaleListKeys(qc, change.staleKeys);
  invalidateIssueDerivatives(qc, wsId, {
    statusOrProjectChanged:
      issue.status !== undefined || issue.project_id !== undefined,
  });
  // 分组计数、分支成员关系和层级都由服务器拥有。绝不要从部分分支猜增量；
  // 重取活跃的 Table 查询即可。
  qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });

  // 失效旧父议题的子议题（议题已从它下面移除）
  if (oldParentId) {
    if (parentChanged) {
      qc.invalidateQueries({ queryKey: issueKeys.children(wsId, oldParentId) });
    } else {
      qc.setQueryData<Issue[]>(issueKeys.children(wsId, oldParentId), (old) =>
        old?.map((c) =>
          c.id === issue.id &&
          (issue.revision === undefined ||
            c.revision === undefined ||
            issue.revision > c.revision)
            ? { ...c, ...issue }
            : c,
        ),
      );
    }
  }
  // 失效新父议题的子议题（议题被加到了它下面）
  if (newParentId && parentChanged) {
    qc.invalidateQueries({ queryKey: issueKeys.children(wsId, newParentId) });
  }
  if (oldParentId || newParentId) {
    if (issue.status !== undefined || issue.parent_issue_id !== undefined) {
      qc.invalidateQueries({ queryKey: issueKeys.childProgress(wsId) });
    }
    qc.invalidateQueries({ queryKey: issueKeys.childrenByParentsAll(wsId) });
  }
  reconcileIssueFullSnapshotRevision(qc, wsId, issue.id, issue.revision);
}

/**
 * 把议题的标签就地修补到列表缓存、my-issues 缓存、详情缓存和每议题标签缓存里。
 * 由 attach/detach 之后的 `issue_labels:changed` WS 事件触发，让列表/看板的标签 chip
 * 和议题详情 Properties 里的 LabelPicker 无需重取即可更新。
 *
 * byIssue 缓存支撑着 `LabelPicker`；不修补它，外部驱动的标签变更（agent、其它标签页）
 * 会让选择器一直陈旧到重新挂载——`staleTime: Infinity` + `refetchOnWindowFocus: false`
 * （见 `query-client.ts`）意味着焦点变化无法恢复它。
 */
export function onIssueLabelsChanged(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  labels: Label[],
  revision?: number,
) {
  if (refetchForUnversionedIssueEvent(qc, wsId, issueId, revision)) {
    qc.invalidateQueries({ queryKey: labelKeys.byIssue(wsId, issueId) });
    return;
  }
  patchIssueLabels(qc, wsId, issueId, labels, revision);
  invalidateIssueLabelDerivatives(qc, wsId);
  invalidateLastActivitySortedIssueLists(qc, wsId);
}

/** 确定性的标签快照补丁，供乐观 mutation 分支使用。 */
export function patchIssueLabels(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  labels: Label[],
  revision?: number,
) {
  if (
    isOlderThanRecordedAuxiliaryProjection(
      qc,
      wsId,
      issueId,
      revision,
      "labels",
    )
  ) {
    return;
  }
  patchIssueSnapshot(qc, wsId, issueId, { labels }, revision);
  qc.setQueryData<IssueLabelsResponse>(labelKeys.byIssue(wsId, issueId), (old) =>
    old &&
    !(
      revision !== undefined &&
      old.issue_revision !== undefined &&
      revision <= old.issue_revision
    )
      ? {
          ...old,
          labels,
          ...(revision !== undefined ? { issue_revision: revision } : {}),
        }
      : old,
  );
  onIssueAuxiliaryRevision(qc, wsId, issueId, revision, "labels");
}

/** 只在写操作提交后，对服务器过滤的标签窗口做对账。 */
export function invalidateIssueLabelDerivatives(qc: QueryClient, wsId: string) {
  // 一次已提交的响应/事件必须取消或取代任何在标签写之前发起、之后可能才落地的按父拉取。
  qc.invalidateQueries({ queryKey: issueKeys.childrenAll(wsId) });
  // 批量子议题缓存是 Map 形状（parentId → Issue[]），patchIssueLabels 无法精确更新——
  // 改为重取，让泳道的子议题行拿到新的标签集合。
  qc.invalidateQueries({ queryKey: issueKeys.childrenByParentsAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.myAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.assigneeGroupsAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.myAssigneeGroupsAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
  qc.invalidateQueries({
    queryKey: issueKeys.flatAll(wsId),
    predicate: (query) =>
      query.queryKey.some((part) => {
        if (!part || typeof part !== "object" || Array.isArray(part)) return false;
        const labelIds = (part as { label_ids?: unknown }).label_ids;
        return Array.isArray(labelIds) && labelIds.length > 0;
      }),
  });
}

/**
 * 把元数据快照应用到议题详情 + 列表 + my-issues 缓存。
 * 服务器在每次单个 key 被设置或删除时都会下发这个事件，所以载荷永远是变更后的
 * 完整 map——我们做的是替换，而不是合并。
 *
 * 用于议题详情里只读的元数据条。即使没有视图挂载时到达的更新，也会让缓存保持准确，
 * 这样下一次渲染无需重取就能显示最新状态。
 */
export function onIssueMetadataChanged(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  metadata: IssueMetadata,
  revision?: number,
) {
  if (refetchForUnversionedIssueEvent(qc, wsId, issueId, revision)) return;
  if (
    isOlderThanRecordedAuxiliaryProjection(
      qc,
      wsId,
      issueId,
      revision,
      "metadata",
    )
  ) {
    return;
  }
  patchIssueSnapshot(qc, wsId, issueId, { metadata }, revision);
  onIssueAuxiliaryRevision(qc, wsId, issueId, revision, "metadata");
  invalidateLastActivitySortedIssueLists(qc, wsId);
  qc.invalidateQueries({ queryKey: issueKeys.myAll(wsId) });
  // 一次元数据写在服务器端会推进 issue.updated_at（SetIssueMetadataKey /
  // DeleteIssueMetadataKey），但上面的补丁让每张卡保持原槽位，所以按“更新日期”排序的
  // 看板/表格会停在旧顺序。这个事件已由服务器提交，因此重取这些 key 来重新排序（MUL-5016）。
  invalidateUpdatedAtSortedIssueLists(qc, wsId);
  // 由服务器支持的 Table 计数、成员关系和游标边界也可能依赖元数据驱动的时间戳，
  // 所以一并刷新它的查询图。
  qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
}

/**
 * 把自定义属性包快照应用到议题详情 + 列表缓存。与 onIssueMetadataChanged 一致：
 * 服务器在每次单 key 写时都会下发变更后的完整属性包，所以我们替换而不是合并。
 * 也被 useSetIssueProperty/useUnsetIssueProperty 的乐观路径直接使用。
 */
export function onIssuePropertiesChanged(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  properties: IssuePropertyValues,
  revision?: number,
) {
  if (refetchForUnversionedIssueEvent(qc, wsId, issueId, revision)) return;
  patchIssueProperties(qc, wsId, issueId, properties, revision);
  invalidateLastActivitySortedIssueLists(qc, wsId);
  // 按父行先做修补以获得即时 UI 反馈，然后把所有子议题投影标记为过期，
  // 避免旧拉取在提交后胜出。
  qc.invalidateQueries({ queryKey: issueKeys.childrenAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.childrenByParentsAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.myAll(wsId) });
  // 普通的按负责人分组缓存从不在原地修补（它们的桶形状不同），否则在
  // staleTime:Infinity 下会永久持有陈旧 chip（无尘评审 F2）。
  qc.invalidateQueries({ queryKey: issueKeys.assigneeGroupsAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.myAssigneeGroupsAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
  invalidatePropertyWindowQueries(qc, wsId);
  // 一次属性写同样会在服务器端推进 issue.updated_at（SetIssuePropertyValue /
  // DeleteIssuePropertyValue）。invalidatePropertyWindowQueries 只重取按属性过滤/排序的
  // 窗口，所以按“更新日期”排序的状态看板或平铺表格（没有属性参数）会停在旧顺序。
  // 这些也要重取。只有已提交的调用方才会走到这里（WS 事件 + mutation onSuccess）；
  // 乐观分支用 patchIssueProperties（MUL-5016）。
  invalidateUpdatedAtSortedIssueLists(qc, wsId);
}

/** 只修补确定性的实体快照。乐观 mutation 分支用这个辅助函数，
 * 这样它们绝不会在服务器提交前就发起属性过滤/排序的重取
 * （那会返回旧属性包、覆盖乐观的这份）。 */
export function patchIssueProperties(
  qc: QueryClient,
  wsId: string,
  issueId: string,
  properties: IssuePropertyValues,
  revision?: number,
) {
  if (
    isOlderThanRecordedAuxiliaryProjection(
      qc,
      wsId,
      issueId,
      revision,
      "properties",
    )
  ) {
    return;
  }
  patchIssueSnapshot(qc, wsId, issueId, { properties }, revision);
  onIssueAuxiliaryRevision(qc, wsId, issueId, revision, "properties");
}

/**
 * 重取每个“服务器端形状依赖属性值”的议题窗口：按 `properties` 过滤、或按
 * `property:<id>` 排序的查询。原地修补在 staleTime:Infinity 下会一直保持陈旧——
 * 一次取值编辑可能改变议题的页成员关系和顺序，而分组缓存永远不会自愈（第 3 轮评审）。
 * 没有属性参数的窗口则保留上面廉价的原地修补。
 */
export function invalidatePropertyWindowQueries(qc: QueryClient, wsId: string) {
  qc.invalidateQueries({
    queryKey: issueKeys.all(wsId),
    predicate: (query) =>
      query.queryKey.some((part) => {
        if (!part || typeof part !== "object" || Array.isArray(part)) return false;
        const rec = part as Record<string, unknown>;
        if (
          rec.properties &&
          typeof rec.properties === "object" &&
          Object.keys(rec.properties as Record<string, unknown>).length > 0
        ) {
          return true;
        }
        return typeof rec.sort_by === "string" && rec.sort_by.startsWith("property:");
      }),
  });
}

/** 处理 `issue:deleted` 事件：清理该议题的所有缓存，并失效相关聚合缓存。 */
export function onIssueDeleted(
  qc: QueryClient,
  wsId: string,
  issueId: string,
) {
  cleanupDeletedIssueCaches(qc, wsId, issueId);
  qc.invalidateQueries({ queryKey: issueKeys.assigneeGroupsAll(wsId) });
  qc.invalidateQueries({ queryKey: issueKeys.myAssigneeGroupsAll(wsId) });
  qc.invalidateQueries({ queryKey: projectKeys.all(wsId) });
}
