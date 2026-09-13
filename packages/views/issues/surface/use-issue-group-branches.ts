"use client";

// 分组分支数据 hook：给非状态分组的看板（按指派人/项目/父问题/自定义属性）以及复合
// 泳道提供服务端游标分页数据。`/groups` 拥有完整的分组目录和精确计数；每个分组
// （或 泳道×状态 复合格）拥有各自独立的 `/rows` 游标链。
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  useInfiniteQuery,
  useQueries,
  useQueryClient,
  type UseQueryResult,
} from "@tanstack/react-query";
import {
  issueKeys,
  issueTableGroupsOptions,
  issueTableRowPageOptions,
} from "@multica/core/issues/queries";
import { issueStatusCategory } from "@multica/core/issues";
import type {
  Issue,
  IssueTableGroupDescriptor,
  IssueTableGroupsRequest,
  IssueTableQuerySpec,
  IssueTableRowsResponse,
} from "@multica/core/types";

/** 单个分组分支的分页状态与控制方法。 */
export interface IssueGroupPageState {
  total: number;
  loaded: number;
  hasMore: boolean;
  isLoading: boolean;
  isFetching: boolean;
  isError: boolean;
  loadMore: () => void;
  retry: () => void;
}

/** useIssueGroupBranches 返回的分组分支数据。 */
export interface IssueGroupBranches {
  enabled: boolean;
  descriptors: IssueTableGroupDescriptor[];
  issues: Issue[];
  pagination: Record<string, IssueGroupPageState>;
  total: number;
  isLoading: boolean;
  isRefreshing: boolean;
  isError: boolean;
  hasMoreGroups: boolean;
  isLoadingMoreGroups: boolean;
  loadMoreGroups: () => void;
  retryGroups: () => void;
}

interface CursorState {
  identity: string;
  cursors: Record<string, Array<string | null>>;
}

interface PageTarget {
  key: string;
  cursor: string | null;
}

interface BranchData {
  rows: Issue[];
  nextCursor: string | null;
  isLoading: boolean;
  isFetching: boolean;
  isError: boolean;
  headUpdatedAt: number;
  headFetching: boolean;
}

function branchDescriptors(
  descriptors: readonly IssueTableGroupDescriptor[],
  secondaryValues?: readonly string[],
): IssueTableGroupDescriptor[] {
  const allowed = secondaryValues ? new Set(secondaryValues) : null;
  return descriptors.flatMap((descriptor) =>
    descriptor.secondary_groups?.length
      ? descriptor.secondary_groups.filter((secondary) => {
          if (!allowed || secondary.value.kind !== "status") return true;
          return allowed.has(secondary.value.status);
        })
      : [descriptor],
  );
}

function issueMatchesDescriptor(
  issue: Issue,
  descriptor: IssueTableGroupDescriptor,
  primary: IssueTableGroupDescriptor | undefined,
  /**
   * 次轴是否是「状态表现为的分类」而不是状态 key 本身。一个分类格子装着落在它名下
   * 的所有自定义状态，所以拿原始 key 去比，会恰恰丢掉分类约定要交付的那些卡片——
   * `qa !== "in_review"` 会把服务端正确返回的 QA 卡片给扔了。（MUL-6243）
   */
  secondaryIsCategory: boolean,
) {
  const value = descriptor.value;
  if (value.kind === "status") {
    const resolved = secondaryIsCategory
      ? issueStatusCategory(issue)
      : issue.status;
    if (resolved !== value.status) return false;
  }
  const owner = primary?.value ?? value;
  switch (owner.kind) {
    case "assignee":
      return owner.actor
        ? issue.assignee_type === owner.actor.type &&
            issue.assignee_id === owner.actor.id
        : issue.assignee_type === null && issue.assignee_id === null;
    case "project":
      return issue.project_id === owner.project_id;
    case "parent":
      return issue.parent_issue_id === owner.parent_id;
    case "property": {
      const propertyValue = issue.properties?.[owner.property_id];
      if (owner.value_state === "unset") return propertyValue === undefined;
      if (owner.value_state === "value") return propertyValue === owner.value;
      return owner.value !== undefined
        ? propertyValue === owner.value
        : propertyValue !== undefined;
    }
    case "status":
      return issue.status === owner.status;
  }
}

/**
 * 非状态分组的看板与泳道共享的游标分页数据 hook。`/groups` 拥有完整的分组目录和
 * 精确计数；每个分组（或 泳道×状态 复合格）拥有各自独立的 `/rows` 游标链。
 * @param options.wsId - 工作区 id。
 * @param options.query - 表格查询规格。
 * @param options.group - 分组类型。
 * @param options.secondaryValues - 复合格分组：保留精确的泳道描述/计数，但只为当前
 *   可见的次级分桶观察行。
 * @param options.observeEmptyBranches - 复合格与「含空值」属性响应会携带零计数的 key。
 *   当它们的挂载哨兵变得可见时激活这些分支，让拖拽目标有活的首屏数据。
 * @param options.enabled - 是否启用查询。
 * @returns 分组目录、展开的问题列表与各分组分页状态。
 */
export function useIssueGroupBranches({
  wsId,
  query,
  group,
  secondaryValues,
  observeEmptyBranches = false,
  enabled,
}: {
  wsId: string;
  query: IssueTableQuerySpec;
  group: IssueTableGroupsRequest["group"];
  /** 复合格分组：保留精确的泳道描述/计数，但只为当前可见的次级分桶观察行。 */
  secondaryValues?: readonly string[];
  /** 复合格与「含空值」属性响应会携带零计数的 key。
   *  当它们的挂载哨兵变得可见时激活这些分支，让拖拽目标有活的首屏数据。 */
  observeEmptyBranches?: boolean;
  enabled: boolean;
}): IssueGroupBranches {
  const queryClient = useQueryClient();
  const secondaryIsCategory =
    group.kind === "compound" && group.secondary === "status_category";
  const identity = useMemo(
    () => JSON.stringify({ query, group }),
    [group, query],
  );
  const groupsQuery = useInfiniteQuery({
    ...issueTableGroupsOptions(wsId, query, group),
    enabled,
  });

  const descriptors = useMemo(
    () => groupsQuery.data?.pages.flatMap((page) => page.groups) ?? [],
    [groupsQuery.data?.pages],
  );
  const branches = useMemo(
    () => branchDescriptors(descriptors, secondaryValues),
    [descriptors, secondaryValues],
  );
  const branchKeys = useMemo(
    () => branches.map((descriptor) => descriptor.key),
    [branches],
  );
  const [cursorState, setCursorState] = useState<CursorState>({
    identity,
    cursors: {},
  });
  const activeCursorState = useMemo<CursorState>(() => {
    return cursorState.identity === identity
      ? cursorState
      : { identity, cursors: {} };
  }, [cursorState, identity]);
  useEffect(() => {
    if (activeCursorState !== cursorState) {
      setCursorState(activeCursorState);
    }
  }, [activeCursorState, cursorState]);

  const pageTargets = useMemo<PageTarget[]>(
    () =>
      enabled
        ? branchKeys.flatMap((key) =>
            (activeCursorState.cursors[key] ?? []).map((cursor) => ({
              key,
              cursor,
            })),
          )
        : [],
    [activeCursorState.cursors, branchKeys, enabled],
  );
  const headPlaceholderRef = useRef(
    new Map<string, IssueTableRowsResponse>(),
  );
  const pageQueries = useMemo(
    () =>
      pageTargets.map(({ key, cursor }) => {
        const placeholder =
          cursor === null ? headPlaceholderRef.current.get(key) : undefined;
        return {
          ...issueTableRowPageOptions(wsId, {
            query,
            group,
            group_key: key,
            hierarchy: { enabled: false },
            parent_id: null,
            page: { limit: 50, cursor },
          }),
          ...(placeholder ? { placeholderData: () => placeholder } : {}),
          enabled,
        };
      }),
    [enabled, group, pageTargets, query, wsId],
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
      next.set(target.key, result.data);
    }
    headPlaceholderRef.current = next;
  }, [pageResults, pageTargets]);

  const primaryByBranch = useMemo(() => {
    const map = new Map<string, IssueTableGroupDescriptor>();
    for (const descriptor of descriptors) {
      for (const secondary of descriptor.secondary_groups ?? []) {
        map.set(secondary.key, descriptor);
      }
    }
    return map;
  }, [descriptors]);
  const descriptorByKey = useMemo(
    () => new Map(branches.map((descriptor) => [descriptor.key, descriptor])),
    [branches],
  );
  const branchData = useMemo(() => {
    const result = new Map<string, BranchData>();
    for (const key of branchKeys) {
      result.set(key, {
        rows: [],
        nextCursor: null,
        isLoading: false,
        isFetching: false,
        isError: false,
        headUpdatedAt: 0,
        headFetching: false,
      });
    }
    const headFetching = new Set<string>();
    for (let index = 0; index < pageTargets.length; index += 1) {
      const target = pageTargets[index];
      const page = pageResults[index];
      if (
        target?.cursor === null &&
        page?.isFetching &&
        (activeCursorState.cursors[target.key]?.length ?? 0) > 1
      ) {
        headFetching.add(target.key);
      }
    }
    const seenByKey = new Map<string, Set<string>>();
    for (let index = 0; index < pageTargets.length; index += 1) {
      const target = pageTargets[index];
      const pageResult = pageResults[index];
      if (!target || !pageResult) continue;
      if (target.cursor !== null && headFetching.has(target.key)) continue;
      const current = result.get(target.key);
      const descriptor = descriptorByKey.get(target.key);
      if (!current || !descriptor) continue;
      if (pageResult.data) {
        const seen = seenByKey.get(target.key) ?? new Set<string>();
        for (const row of pageResult.data.rows) {
          if (
            !issueMatchesDescriptor(
              row.issue,
              descriptor,
              primaryByBranch.get(target.key),
              secondaryIsCategory,
            ) ||
            seen.has(row.issue.id)
          ) {
            continue;
          }
          seen.add(row.issue.id);
          current.rows.push(row.issue);
        }
        seenByKey.set(target.key, seen);
        current.nextCursor = pageResult.data.next_cursor;
      }
      current.isLoading ||= pageResult.isPending;
      current.isFetching ||= pageResult.isFetching;
      current.isError ||= pageResult.isError;
      if (target.cursor === null) {
        current.headUpdatedAt = pageResult.dataUpdatedAt;
        current.headFetching = pageResult.isFetching;
      }
    }
    return result;
  }, [
    activeCursorState.cursors,
    branchKeys,
    descriptorByKey,
    pageResults,
    pageTargets,
    primaryByBranch,
  ]);

  const headRevisionRef = useRef<{
    identity: string;
    revisions: Record<string, number>;
  }>({ identity, revisions: {} });
  useEffect(() => {
    const previous =
      headRevisionRef.current.identity === identity
        ? headRevisionRef.current.revisions
        : {};
    const next: Record<string, number> = {};
    const trim = new Set<string>();
    for (const key of branchKeys) {
      const branch = branchData.get(key);
      if (!branch || branch.headUpdatedAt === 0) continue;
      next[key] = branch.headUpdatedAt;
      const seen = previous[key];
      if (
        (activeCursorState.cursors[key]?.length ?? 0) > 1 &&
        (branch.headFetching ||
          (seen !== undefined && seen !== branch.headUpdatedAt))
      ) {
        trim.add(key);
      }
    }
    headRevisionRef.current = { identity, revisions: next };
    if (trim.size === 0) return;
    setCursorState((previousState) => {
      if (previousState.identity !== identity) return previousState;
      const cursors = { ...previousState.cursors };
      for (const key of trim) cursors[key] = [null];
      return { ...previousState, cursors };
    });
  }, [
    activeCursorState.cursors,
    branchData,
    branchKeys,
    identity,
  ]);

  const loadMore = useCallback(
    (key: string) => {
      const cursor = branchData.get(key)?.nextCursor;
      setCursorState((previous) => {
        if (previous.identity !== identity) return previous;
        const current = previous.cursors[key] ?? [];
        // 一个描述符不会自己装行观察器。第一个可见哨兵激活头部，后续调用追加服务端
        // 游标。这让泳道的首屏限定在已挂载的泳道上，而不是整个目录的 泳道×状态。
        if (current.length === 0) {
          return {
            ...previous,
            cursors: { ...previous.cursors, [key]: [null] },
          };
        }
        if (!cursor) return previous;
        if (current.includes(cursor)) return previous;
        return {
          ...previous,
          cursors: { ...previous.cursors, [key]: [...current, cursor] },
        };
      });
    },
    [branchData, identity],
  );
  const retry = useCallback(
    (key: string) => {
      void queryClient.refetchQueries({
        queryKey: issueKeys.tableRows(
          wsId,
          query,
          group,
          key,
          false,
          null,
        ),
        exact: false,
        type: "active",
      });
    },
    [group, query, queryClient, wsId],
  );
  const pagination = useMemo(
    () =>
      Object.fromEntries(
        branches.map((descriptor) => {
          const branch = branchData.get(descriptor.key);
          const active =
            (activeCursorState.cursors[descriptor.key]?.length ?? 0) > 0;
          return [
            descriptor.key,
            {
              total: descriptor.count,
              loaded: branch?.rows.length ?? 0,
              hasMore:
                enabled &&
                (active
                  ? !!branch?.nextCursor
                  : descriptor.count > 0 || observeEmptyBranches),
              isLoading: enabled && (branch?.isLoading ?? false),
              isFetching: enabled && (branch?.isFetching ?? false),
              isError: enabled && (branch?.isError ?? false),
              loadMore: () => loadMore(descriptor.key),
              retry: () => retry(descriptor.key),
            },
          ];
        }),
      ) as Record<string, IssueGroupPageState>,
    [
      activeCursorState.cursors,
      branchData,
      branches,
      enabled,
      loadMore,
      observeEmptyBranches,
      retry,
    ],
  );
  const issues = useMemo(
    () => branchKeys.flatMap((key) => branchData.get(key)?.rows ?? []),
    [branchData, branchKeys],
  );
  const rowsPending = branchKeys.some(
    (key) => branchData.get(key)?.isLoading,
  );
  const rowsFetching = branchKeys.some(
    (key) => branchData.get(key)?.isFetching,
  );
  const fetchNextGroupPage = groupsQuery.fetchNextPage;
  const refetchGroups = groupsQuery.refetch;
  const hasNextGroupPage = groupsQuery.hasNextPage;
  const isFetchingNextGroupPage = groupsQuery.isFetchingNextPage;
  const loadMoreGroups = useCallback(() => {
    if (hasNextGroupPage && !isFetchingNextGroupPage) {
      void fetchNextGroupPage();
    }
  }, [
    fetchNextGroupPage,
    hasNextGroupPage,
    isFetchingNextGroupPage,
  ]);
  const retryGroups = useCallback(() => {
    void refetchGroups();
  }, [refetchGroups]);

  return {
    enabled,
    descriptors,
    issues,
    pagination,
    // `/groups` 拥有跨查询的精确可见总数。从已加载的描述符推导总数，会让一个
    // 「只含隐藏内容」的首个分组页在后面的页明明有可见卡片时，被看成全局为空。
    total: groupsQuery.data?.pages[0]?.total ?? issues.length,
    isLoading: enabled && groupsQuery.isPending,
    isRefreshing:
      enabled &&
      !groupsQuery.isPending &&
      (groupsQuery.isFetching || rowsPending || rowsFetching),
    isError:
      enabled &&
      (groupsQuery.isError ||
        branchKeys.some((key) => branchData.get(key)?.isError)),
    hasMoreGroups: enabled && !!hasNextGroupPage,
    isLoadingMoreGroups: enabled && isFetchingNextGroupPage,
    loadMoreGroups,
    retryGroups,
  };
}
