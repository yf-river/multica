// 本文件实现收件箱的「过滤状态」：按工作区隔离的 Zustand store、各维度的
// 开关切换，以及把过滤应用到条目列表的纯函数（含「优先级过滤是否被老后端
// 支持」的能力探测）。

import { create } from "zustand";
import type { InboxItem, IssuePriority, IssueStatus } from "../types";

/** 收件箱的过滤条件：状态、优先级、操作者三个维度各自多选，外加「只看未读」。 */
export interface InboxFilters {
  readonly statuses: readonly IssueStatus[];
  readonly priorities: readonly IssuePriority[];
  /** 操作者 key——含义见 `inboxActorKey`。 */
  readonly actors: readonly string[];
  readonly unreadOnly: boolean;
  /** Only issues no agent is running or queued on. Active inbox only. */
  readonly idleOnly: boolean;
}

/**
 * The dimensions the server-filtered archive understands. It has no idle
 * dimension, so `idleOnly` never reaches its endpoints or query keys.
 */
export type ArchivedInboxFilters = Omit<InboxFilters, "idleOnly">;

/**
 * Stable identity for "who this notification came from", used as the value of
 * the `actors` dimension.
 *
 * @param item - 收件箱条目
 * @returns 操作者 key；无法归属时返回 null
 */
export function inboxActorKey(item: InboxItem): string | null {
  const type = item.actor_type;
  if (!type) return null;
  if (type === "system") return "system";
  return item.actor_id ? `${type}:${item.actor_id}` : null;
}

/**
 * `inboxActorKey` 的逆操作：把选中项解析回「类型 + id」，用于反查目录/成员。
 *
 * @param key - 操作者 key
 * @returns 拆出的类型与 id（无分隔符时 id 为空串）
 */
export function inboxActorKeyParts(key: string): { type: string; id: string } {
  const separator = key.indexOf(":");
  if (separator === -1) return { type: key, id: "" };
  return { type: key.slice(0, separator), id: key.slice(separator + 1) };
}

/** 优先级过滤的「支持度」：未知 / 支持 / 不支持（老后端没有该字段）。 */
export type InboxPriorityFilterSupport =
  | "unknown"
  | "supported"
  | "unsupported";

/** 全部维度的空过滤条件（冻结常量，可安全共享）。 */
export const EMPTY_INBOX_FILTERS: InboxFilters = Object.freeze({
  statuses: Object.freeze([]),
  priorities: Object.freeze([]),
  actors: Object.freeze([]),
  unreadOnly: false,
  idleOnly: false,
});

/** 所有维度都没选中任何东西时返回 true。 */
export function isEmptyInboxFilters(filters: InboxFilters): boolean {
  return inboxFilterCount(filters) === 0;
}

/** store 内部状态：每个工作区一份过滤条件，外加各维度的切换动作。 */
interface InboxFilterState {
  filtersByWorkspace: Record<string, InboxFilters>;
  toggleStatusFilter: (wsId: string, status: IssueStatus) => void;
  togglePriorityFilter: (wsId: string, priority: IssuePriority) => void;
  toggleActorFilter: (wsId: string, actor: string) => void;
  toggleUnreadOnly: (wsId: string) => void;
  toggleIdleOnly: (wsId: string) => void;
  clearPriorityFilters: (wsId: string) => void;
  clearFilters: (wsId: string) => void;
}

/** 在只读数组上做「有则删、无则加」的切换，返回新数组。 */
function toggleValue<T extends string>(values: readonly T[], value: T): T[] {
  return values.includes(value)
    ? values.filter((candidate) => candidate !== value)
    : [...values, value];
}

/**
 * 收件箱过滤 Zustand store。`filtersByWorkspace` 让不同工作区的过滤条件互不
 * 干扰；切换动作都是不可变更新。
 */
export const useInboxFilterStore = create<InboxFilterState>()((set) => ({
  filtersByWorkspace: {},
  toggleStatusFilter: (wsId, status) =>
    set((state) => {
      const current = state.filtersByWorkspace[wsId] ?? EMPTY_INBOX_FILTERS;
      return {
        filtersByWorkspace: {
          ...state.filtersByWorkspace,
          [wsId]: {
            ...current,
            statuses: toggleValue(current.statuses, status),
          },
        },
      };
    }),
  togglePriorityFilter: (wsId, priority) =>
    set((state) => {
      const current = state.filtersByWorkspace[wsId] ?? EMPTY_INBOX_FILTERS;
      return {
        filtersByWorkspace: {
          ...state.filtersByWorkspace,
          [wsId]: {
            ...current,
            priorities: toggleValue(current.priorities, priority),
          },
        },
      };
    }),
  toggleActorFilter: (wsId, actor) =>
    set((state) => {
      const current = state.filtersByWorkspace[wsId] ?? EMPTY_INBOX_FILTERS;
      return {
        filtersByWorkspace: {
          ...state.filtersByWorkspace,
          [wsId]: { ...current, actors: toggleValue(current.actors, actor) },
        },
      };
    }),
  toggleUnreadOnly: (wsId) =>
    set((state) => {
      const current = state.filtersByWorkspace[wsId] ?? EMPTY_INBOX_FILTERS;
      return {
        filtersByWorkspace: {
          ...state.filtersByWorkspace,
          [wsId]: { ...current, unreadOnly: !current.unreadOnly },
        },
      };
    }),
  toggleIdleOnly: (wsId) =>
    set((state) => {
      const current = state.filtersByWorkspace[wsId] ?? EMPTY_INBOX_FILTERS;
      return {
        filtersByWorkspace: {
          ...state.filtersByWorkspace,
          [wsId]: { ...current, idleOnly: !current.idleOnly },
        },
      };
    }),
  clearPriorityFilters: (wsId) =>
    set((state) => {
      const current = state.filtersByWorkspace[wsId];
      if (!current || current.priorities.length === 0) return state;
      const next = { ...current, priorities: [] };
      // 「是否为空」要问所有维度，而不只是状态：若把条目删掉而其中还留着
      // 操作者/只看未读等选择，就等于清掉了用户设的、后端从没过问过的过滤。
      if (isEmptyInboxFilters(next)) {
        const { [wsId]: _removed, ...filtersByWorkspace } =
          state.filtersByWorkspace;
        return { filtersByWorkspace };
      }
      return {
        filtersByWorkspace: { ...state.filtersByWorkspace, [wsId]: next },
      };
    }),
  clearFilters: (wsId) =>
    set((state) => {
      if (!state.filtersByWorkspace[wsId]) return state;
      const { [wsId]: _removed, ...filtersByWorkspace } =
        state.filtersByWorkspace;
      return { filtersByWorkspace };
    }),
}));

/** 读取某个工作区的过滤条件，未设置时回退到稳定的空过滤常量。 */
export function useInboxFilters(wsId: string): InboxFilters {
  return (
    useInboxFilterStore((state) => state.filtersByWorkspace[wsId]) ??
    EMPTY_INBOX_FILTERS
  );
}

/**
 * 从解析到的响应里推断「优先级过滤是否受支持」，而不是看版本字符串。
 *
 * 新后端会对每一行 Inbox 都序列化 `issue_priority`（没有 issue 的通知则是
 * `null`）；老后端直接省略它。要求「每行返回都带已定义的值」，还能防止一次
 * 滚动部署的混合响应被优先级缓存补丁误判成「完全支持」。
 *
 * @param items - 已解析的收件箱条目
 * @returns 支持度三态
 */
export function inboxPriorityFilterSupport(
  items: readonly InboxItem[],
): InboxPriorityFilterSupport {
  if (items.length === 0) return "unknown";
  return items.every((item) => item.issue_priority !== undefined)
    ? "supported"
    : "unsupported";
}

/**
 * 在后端能力被证实之前，忽略过期的优先级选择。
 *
 * @param filters - 当前过滤条件
 * @param support - 后端对优先级过滤的支持度
 * @returns 需要清空优先级选择时返回新条件，否则原样返回
 */
export function inboxFiltersForPrioritySupport(
  filters: InboxFilters,
  support: InboxPriorityFilterSupport,
): InboxFilters {
  if (support === "supported" || filters.priorities.length === 0) {
    return filters;
  }
  return { ...filters, priorities: [] };
}

const EMPTY_ISSUE_IDS: ReadonlySet<string> = new Set();

/** Page state `filterInboxItems` needs beyond the selected filters. */
export interface InboxFilterContext {
  /** Issues an agent is running or queued on. Read only by `idleOnly`. */
  readonly activeIssueIds?: ReadonlySet<string>;
  /**
   * Group key (`issue_id ?? id`) of the open notification, pinned against the
   * unread dimension only. Set by the page after opening marks the row read.
   */
  readonly pinnedUnreadKey?: string | null;
  /**
   * Group key of the open notification, pinned against the idle dimension
   * only. Set by the page once the row was opened while idle.
   */
  readonly pinnedIdleKey?: string | null;
}

/**
 * The archive is filtered server-side and the backend has no idle dimension,
 * so a selection kept from the active inbox stays dormant there instead of
 * narrowing whichever archived pages happen to be loaded.
 */
export function inboxFiltersForArchive(filters: InboxFilters): InboxFilters {
  return filters.idleOnly ? { ...filters, idleOnly: false } : filters;
}

/**
 * 把过滤应用到条目列表。维度内是「或」，维度之间是「与」。
 *
 * 每个维度都读「列表实际渲染的那一行」。状态和优先级是白捡的——它们是 issue
 * 的属性，组内每条通知都相同。操作者则是有意为之：去重只保留一组里最新的
 * 通知，那一行显示的是「它自己的」操作者头像和名字。若去匹配埋在同一个组里
 * 更老行中的操作者，回答的问题（「Alice 碰过的所有 issue」）就和这一行随后
 * 展示的内容不一致了——选了 Alice，列表里却会塞满 Bob。按什么过滤，看到什么。
 *
 * `unreadOnly` 同样读渲染行的 `read`，正是未读角标在数的那个值
 * （`useInboxUnreadCount`）——所以「只看未读」和 Inbox 边上的数字永远不会打架。
 *
 * @param items - 待过滤条目
 * @param filters - 过滤条件
 * @returns 过滤后的条目
 */
export function filterInboxItems(
  items: InboxItem[],
  filters: InboxFilters,
  {
    activeIssueIds = EMPTY_ISSUE_IDS,
    pinnedUnreadKey = null,
    pinnedIdleKey = null,
  }: InboxFilterContext = {},
): InboxItem[] {
  if (isEmptyInboxFilters(filters)) return items;

  const statuses = new Set(filters.statuses);
  const priorities = new Set(filters.priorities);
  const actors = new Set(filters.actors);
  return items.filter((item) => {
    const actor = inboxActorKey(item);
    const matchesOtherDimensions =
      (statuses.size === 0 ||
        (item.issue_status != null && statuses.has(item.issue_status))) &&
      (priorities.size === 0 ||
        (item.issue_priority != null && priorities.has(item.issue_priority))) &&
      (actors.size === 0 || (actor != null && actors.has(actor)));
    if (!matchesOtherDimensions) return false;
    const key = item.issue_id ?? item.id;
    const matchesUnread =
      !filters.unreadOnly ||
      item.read !== true ||
      (pinnedUnreadKey != null && key === pinnedUnreadKey);
    const matchesIdle =
      !filters.idleOnly ||
      (item.issue_id != null && !activeIssueIds.has(item.issue_id)) ||
      (pinnedIdleKey != null && key === pinnedIdleKey);
    return matchesUnread && matchesIdle;
  });
}

/** 统计当前选中的过滤条件个数（每个维度的选中项相加，只看未读计 1）。 */
export function inboxFilterCount(filters: InboxFilters): number {
  return (
    filters.statuses.length +
    filters.priorities.length +
    filters.actors.length +
    (filters.unreadOnly ? 1 : 0) +
    (filters.idleOnly ? 1 : 0)
  );
}
