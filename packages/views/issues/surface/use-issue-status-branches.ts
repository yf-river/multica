"use client";

// 状态分支数据 hook：给列表视图和按状态分组的看板提供服务端权威的分列数据。
// 每列的分页用的是与 Table 相同的 keyset 分页 `/table/rows` 约定；列的总数来自
// 析取式的状态 facet，所以隐藏/空的列从不依赖已加载的卡片窗口。
// 这里的「状态」都是看板列，而列是「分类」。
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  useQueries,
  useQueryClient,
  type UseQueryResult,
} from "@tanstack/react-query";
import { ALL_STATUSES } from "@multica/core/issues/config";
import { issueColumnCategory } from "@multica/core/issues";
import type { IssueStatusCatalog } from "@multica/core/issue-statuses";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import {
  issueKeys,
  issueTableRowPageOptions,
} from "@multica/core/issues/queries";
import type {
  Issue,
  IssueStatusCategory,
  IssueTableFacetsResponse,
  IssueTableQuerySpec,
  IssueTableRowsResponse,
} from "@multica/core/types";

/** 单列（单个状态分类）的分页状态与控制方法。 */
export interface IssueStatusPageState {
  total: number;
  loaded: number;
  hasMore: boolean;
  isLoading: boolean;
  isFetching: boolean;
  isError: boolean;
  loadMore: () => void;
  retry: () => void;
}

/** 按状态分类组织的分页状态表。 */
export type IssueStatusPagination = Record<
  IssueStatusCategory,
  IssueStatusPageState
>;

interface StatusCursorState {
  identity: string;
  cursors: Record<IssueStatusCategory, Array<string | null>>;
}

interface StatusPageTarget {
  status: IssueStatusCategory;
  cursor: string | null;
}

interface StatusBranchData {
  rows: Issue[];
  nextCursor: string | null;
  isLoading: boolean;
  isFetching: boolean;
  isError: boolean;
  headUpdatedAt: number;
  headFetching: boolean;
  /** 只有头部页（cursor === null）还在加载、且没有任何东西可显示时为 true。
   *  与 `isLoading` 不同——后者还会累积尾部页，让每列自己的「加载更多」哨兵能转圈。 */
  headPending: boolean;
}

// 本 hook 里的每个「状态」都是看板「列」，而列是分类：工作区自定义状态住在它所属
// 分类的列里，而不是自成一列。（MUL-6243）
function statusGroupKey(status: IssueStatusCategory, byCategory: boolean) {
  return byCategory ? `status_category:${status}` : `status:${status}`;
}

/**
 * 这个 hook 分页所用的分组约定。
 *
 * `status_category` 是本特性引入的服务端约定，所以只有在「确认工作区有自定义状态」后
 * 才发送——参见 `IssueStatusCatalog.hasCustomStatuses` 为什么它既是滚动发布的守卫、
 * 又是冷加载的守卫。其他人都保留过去发起的那个请求原样。（MUL-6243）
 */
const CATEGORY_GROUP = { kind: "status_category" } as const;
const STATUS_GROUP = { kind: "status" } as const;

function initialCursorState(
  identity: string,
  statuses: readonly IssueStatusCategory[],
): StatusCursorState {
  const cursors = Object.fromEntries(
    ALL_STATUSES.map((status) => [
      status,
      statuses.includes(status) ? [null] : [],
    ]),
  ) as StatusCursorState["cursors"];
  return { identity, cursors };
}

function rebaseCursorState(
  state: StatusCursorState,
  identity: string,
  statuses: readonly IssueStatusCategory[],
) {
  const current =
    state.identity === identity ? state : initialCursorState(identity, statuses);
  let cursors: StatusCursorState["cursors"] | null = null;
  for (const status of statuses) {
    if (current.cursors[status].length > 0) continue;
    cursors ??= { ...current.cursors };
    cursors[status] = [null];
  }
  return cursors ? { ...current, cursors } : current;
}

/**
 * 从服务端的状态 facet 算出每列的精确总数。
 *
 * facet 按具体状态 KEY 组织，而列是分类，所以自定义 key 要被折进它的分类而不是丢弃
 * ——丢弃它们会让列的总数和它渲染的卡片对不上。
 *
 * 已加载目录里都不认识的 key 会被丢弃，而不是猜进 `todo`：`categoryOf` 区分不了
 * 「几秒前在别处刚建的自定义状态」和「根本不是状态」，而把一个任意列的计数虚高，
 * 比总数短暂少一张卡片更糟。目录加载前没有东西可折，只数内置状态——这正是本特性
 * 出现前的行为。（MUL-6243）
 *
 * 状态 facet 是「析取式」的——服务端回答它时会丢掉状态筛选，好让筛选菜单展示每个
 * 选项会选中什么。列标题是相反的问题，所以激活筛选时要把折并范围收窄到它选中的那些
 * key。否则按某个自定义状态筛选，会把全部 in_review 问题（220 个）都算进 In Review
 * 列的标题，而实际匹配的只有 5 张卡。（MUL-6409）
 */
function statusCountsFromFacets(
  facets: IssueTableFacetsResponse | undefined,
  catalog: Pick<IssueStatusCatalog, "entryOf" | "isLoaded">,
  selectedStatuses: readonly string[] | undefined,
) {
  const counts = new Map<IssueStatusCategory, number>();
  const selected =
    selectedStatuses && selectedStatuses.length > 0
      ? new Set(selectedStatuses)
      : null;
  const statusFacet = facets?.facets.find((facet) => facet.kind === "status");
  for (const value of statusFacet?.values ?? []) {
    if (selected && !selected.has(value.key)) continue;
    const builtIn = ALL_STATUSES.find((category) => category === value.key);
    const category = builtIn ?? (catalog.isLoaded ? catalog.entryOf(value.key)?.category : undefined);
    if (!category || !ALL_STATUSES.includes(category)) continue;
    counts.set(category, (counts.get(category) ?? 0) + value.count);
  }
  return counts;
}

/** useIssueStatusBranches 返回的状态分支数据。 */
export interface IssueStatusBranches {
  enabled: boolean;
  issues: Issue[];
  pagination: IssueStatusPagination;
  total: number;
  isTotalKnown: boolean;
  isLoading: boolean;
  isRefreshing: boolean;
}

/**
 * 列表视图与按状态分组的看板共享的服务端权威状态分支数据 hook。每个分支的分页
 * 用的是与 Table 相同的 keyset 分页 `/table/rows` 约定；状态总数来自析取式的状态
 * facet，所以隐藏/空列从不依赖已加载的卡片窗口。
 * @param options.wsId - 工作区 id。
 * @param options.query - 表格查询规格。
 * @param options.statuses - 要加载哪些状态分类（看板列）。
 * @param options.facets - 服务端 facet 响应（状态总数来源）。
 * @param options.facetsPending - facets 是否还在加载。
 * @param options.facetsFetching - facets 是否正在重新拉取。
 * @param options.enabled - 是否启用查询。
 * @returns 展开的问题列表与每列分页状态。
 */
export function useIssueStatusBranches({
  wsId,
  query,
  statuses,
  facets,
  facetsPending,
  facetsFetching,
  enabled,
}: {
  wsId: string;
  query: IssueTableQuerySpec;
  statuses: readonly IssueStatusCategory[];
  facets: IssueTableFacetsResponse | undefined;
  facetsPending: boolean;
  facetsFetching: boolean;
  enabled: boolean;
}): IssueStatusBranches {
  const queryClient = useQueryClient();
  const catalog = useIssueStatuses(wsId);
  const hasCustomStatuses = catalog.hasCustomStatuses || query.scope.kind === "my";
  const group = hasCustomStatuses ? CATEGORY_GROUP : STATUS_GROUP;
  // 分组约定是游标身份的一部分，而不只是查询的一部分：目录一落定它就由 `status`
  // 翻成 `status_category`，而按旧约定铸出的游标对新约定毫无意义。没有这条，目录到达
  // 会把过期的 `status:` 游标带进 `status_category:` 请求。（MUL-6243）
  const identity = useMemo(
    () => JSON.stringify({ query, group: group.kind }),
    [group.kind, query],
  );
  const [cursorState, setCursorState] = useState<StatusCursorState>(() =>
    initialCursorState(identity, statuses),
  );
  const activeCursorState = rebaseCursorState(
    cursorState,
    identity,
    statuses,
  );

  useEffect(() => {
    if (cursorState !== activeCursorState) {
      setCursorState(activeCursorState);
    }
  }, [activeCursorState, cursorState]);

  const pageTargets = useMemo<StatusPageTarget[]>(
    () =>
      enabled
        ? statuses.flatMap((status) =>
            activeCursorState.cursors[status].map((cursor) => ({
              status,
              cursor,
            })),
          )
        : [],
    [activeCursorState.cursors, enabled, statuses],
  );
  const headPlaceholderRef = useRef(
    new Map<IssueStatusCategory, IssueTableRowsResponse>(),
  );
  const pageQueries = useMemo(
    () =>
      pageTargets.map(({ status, cursor }) => {
        const placeholder =
          cursor === null ? headPlaceholderRef.current.get(status) : undefined;
        return {
          ...issueTableRowPageOptions(wsId, {
            query,
            group,
            group_key: statusGroupKey(status, hasCustomStatuses),
            hierarchy: { enabled: false },
            parent_id: null,
            page: { limit: 50, cursor },
          }),
          // useQueries 在查询哈希变化时会替换 observer，所以它内置的 keepPreviousData
          // 撑不过筛选/排序的切换。这里只为每个固定的状态分支保留最后一份落定的
          // HEAD 数据。尾部页刻意断开；精确 facet 仍归服务端所有。
          ...(placeholder ? { placeholderData: () => placeholder } : {}),
          enabled,
        };
      }),
    [enabled, group, hasCustomStatuses, pageTargets, query, wsId],
  );
  const pageResults = useQueries({ queries: pageQueries }) as Array<
    UseQueryResult<IssueTableRowsResponse, Error>
  >;
  useEffect(() => {
    const next = new Map(headPlaceholderRef.current);
    for (let index = 0; index < pageTargets.length; index += 1) {
      const target = pageTargets[index];
      const result = pageResults[index];
      if (
        target?.cursor !== null ||
        !result?.data ||
        result.isPlaceholderData ||
        result.isError
      ) {
        continue;
      }
      next.set(target.status, result.data);
    }
    headPlaceholderRef.current = next;
  }, [pageResults, pageTargets]);

  const branchData = useMemo(() => {
    const result = new Map<IssueStatusCategory, StatusBranchData>();
    for (const status of statuses) {
      result.set(status, {
        rows: [],
        nextCursor: null,
        isLoading: false,
        isFetching: false,
        isError: false,
        headUpdatedAt: 0,
        headFetching: false,
        headPending: false,
      });
    }

    const headFetching = new Set<IssueStatusCategory>();
    for (let index = 0; index < pageTargets.length; index += 1) {
      const target = pageTargets[index];
      const queryResult = pageResults[index];
      if (
        target?.cursor === null &&
        queryResult?.isFetching &&
        activeCursorState.cursors[target.status].length > 1
      ) {
        headFetching.add(target.status);
      }
    }

    const seenByStatus = new Map<IssueStatusCategory, Set<string>>();
    for (let index = 0; index < pageTargets.length; index += 1) {
      const target = pageTargets[index];
      const queryResult = pageResults[index];
      if (!target || !queryResult) continue;
      // 大范围失效会让 HEAD 一开始刷新、所有游标就立刻过期。立即藏起已断开的尾部页；
      // 下面的 effect 会在它们的响应重新进入前就把游标 observer 修剪掉。
      if (target.cursor !== null && headFetching.has(target.status)) continue;

      const current = result.get(target.status);
      if (!current) continue;
      const page = queryResult.data;
      if (page) {
        const seen = seenByStatus.get(target.status) ?? new Set<string>();
        for (const row of page.rows) {
          // 实时事件可能在宽范围失效把它挪到别的分支缓存之前，先修补了问题状态。
          // 绝不要把一张已修补的卡片渲染在它已不属于的列下。比较对象是「分类」，
          // 所以自定义状态留在自己的列里，而不是因为不等于列 key 被丢弃。
          if (issueColumnCategory(row.issue) !== target.status) continue;
          if (seen.has(row.issue.id)) continue;
          seen.add(row.issue.id);
          current.rows.push(row.issue);
        }
        seenByStatus.set(target.status, seen);
        current.nextCursor = page.next_cursor;
      }
      current.isLoading ||= queryResult.isPending;
      current.isFetching ||= queryResult.isFetching;
      current.isError ||= queryResult.isError;
      if (target.cursor === null) {
        current.headUpdatedAt = queryResult.dataUpdatedAt;
        current.headFetching = queryResult.isFetching;
        current.headPending = queryResult.isPending;
      }
    }
    return result;
  }, [
    activeCursorState.cursors,
    pageResults,
    pageTargets,
    statuses,
  ]);

  // 一旦 HEAD 页刷新过，它旧的游标链就不再属于当前快照。跟 Table 的分支行为对齐：
  // 丢掉所有尾部页。
  const headRevisionRef = useRef<{
    identity: string;
    revisions: Partial<Record<IssueStatusCategory, number>>;
  }>({ identity, revisions: {} });
  useEffect(() => {
    const previous =
      headRevisionRef.current.identity === identity
        ? headRevisionRef.current.revisions
        : {};
    const next: Partial<Record<IssueStatusCategory, number>> = {};
    const trim = new Set<IssueStatusCategory>();
    for (const status of statuses) {
      const branch = branchData.get(status);
      if (!branch || branch.headUpdatedAt === 0) continue;
      next[status] = branch.headUpdatedAt;
      const seen = previous[status];
      if (
        activeCursorState.cursors[status].length > 1 &&
        (branch.headFetching ||
          (seen !== undefined && seen !== branch.headUpdatedAt))
      ) {
        trim.add(status);
      }
    }
    headRevisionRef.current = { identity, revisions: next };
    if (trim.size === 0) return;
    setCursorState((previousState) => {
      if (previousState.identity !== identity) return previousState;
      const cursors = { ...previousState.cursors };
      for (const status of trim) cursors[status] = [null];
      return { ...previousState, cursors };
    });
  }, [
    activeCursorState.cursors,
    branchData,
    identity,
    statuses,
  ]);

  const counts = useMemo(
    () => statusCountsFromFacets(facets, catalog, query.filters.statuses),
    [facets, catalog, query.filters.statuses],
  );
  const loadMore = useCallback(
    (status: IssueStatusCategory) => {
      const cursor = branchData.get(status)?.nextCursor;
      if (!cursor) return;
      setCursorState((previous) => {
        if (previous.identity !== identity) return previous;
        const current = previous.cursors[status];
        if (current.includes(cursor)) return previous;
        return {
          ...previous,
          cursors: {
            ...previous.cursors,
            [status]: [...current, cursor],
          },
        };
      });
    },
    [branchData, identity],
  );
  const retry = useCallback(
    (status: IssueStatusCategory) => {
      void queryClient.refetchQueries({
        queryKey: issueKeys.tableRows(
          wsId,
          query,
          group,
          statusGroupKey(status, hasCustomStatuses),
          false,
          null,
        ),
        exact: false,
        type: "active",
      });
    },
    [group, hasCustomStatuses, query, queryClient, wsId],
  );

  const pagination = useMemo<IssueStatusPagination>(() => {
    return Object.fromEntries(
      ALL_STATUSES.map((status) => {
        const branch = branchData.get(status);
        const loaded = branch?.rows.length ?? 0;
        const total = counts.get(status) ?? loaded;
        return [
          status,
          {
            total,
            loaded,
            hasMore: enabled && !!branch?.nextCursor,
            isLoading: enabled && (branch?.isLoading ?? false),
            isFetching: enabled && (branch?.isFetching ?? false),
            isError: enabled && (branch?.isError ?? false),
            loadMore: () => loadMore(status),
            retry: () => retry(status),
          },
        ];
      }),
    ) as IssueStatusPagination;
  }, [branchData, counts, enabled, loadMore, retry]);

  const issues = useMemo(
    () => statuses.flatMap((status) => branchData.get(status)?.rows ?? []),
    [branchData, statuses],
  );
  const isTotalKnown = facets !== undefined;
  const total = facets?.total ?? issues.length;
  // surface 级的加载只反映每个分支的 HEAD 页。尾部页（加载更多）pending/fetching 时，
  // 已渲染的行还留在屏幕上，所以它绝不能再次触发整个 surface 的骨架屏或全局刷新
  // 指示——「加载更多」由每分支自己的哨兵负责。
  const headsPending = statuses.some(
    (status) => branchData.get(status)?.headPending,
  );
  const headsFetching = statuses.some(
    (status) => branchData.get(status)?.headFetching,
  );

  return {
    enabled,
    issues,
    pagination,
    total,
    isTotalKnown,
    isLoading: enabled && (facetsPending || headsPending),
    isRefreshing:
      enabled &&
      !facetsPending &&
      !headsPending &&
      (facetsFetching || headsFetching),
  };
}
