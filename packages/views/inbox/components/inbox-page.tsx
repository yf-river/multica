"use client";

// 收件箱页：左侧通知列表（活跃/归档两视图，可筛选），右侧任务详情或通知详情
// （窄屏为整屏切换）。列表与选中都持久化到 URL；选中条目自动标已读，并支持
// 全部已读/归档等批量操作。
import {
  useState,
  useEffect,
  useCallback,
  useDeferredValue,
  useMemo,
  useRef,
  lazy,
  Suspense,
} from "react";
import { useDefaultLayout } from "react-resizable-panels";
import { useQuery } from "@tanstack/react-query";
import { workspaceWorkingAgentsOptions } from "@multica/core/agents/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { ApiError, errorCode } from "@multica/core/api";
import { useWorkspacePaths } from "@multica/core/paths";
import { useModalStore } from "@multica/core/modals";
import {
  getShortcut,
  isEditableShortcutTarget,
  isPortalLayerShortcutTarget,
  shortcutMatchesEvent,
} from "@multica/core/shortcuts";
import { isImeComposing } from "@multica/core/utils";
import { useIssueDraftStore } from "@multica/core/issues/stores/draft-store";
import {
  inboxListOptions,
  archivedInboxListOptions,
  deduplicateInboxItems,
  deduplicateArchivedInboxItems,
  useInboxUnreadCount,
} from "@multica/core/inbox/queries";
import {
  useMarkInboxRead,
  useMarkInboxUnread,
  useArchiveInbox,
  useUnarchiveInbox,
  useMarkAllInboxRead,
  useArchiveAllInbox,
  useArchiveAllReadInbox,
  useArchiveCompletedInbox,
  useRetrySourceContextQuickCreate,
} from "@multica/core/inbox/mutations";
import {
  filterInboxItems,
  inboxFiltersForArchive,
  inboxFiltersForPrioritySupport,
  inboxFilterCount,
  inboxPriorityFilterSupport,
  useInboxFilters,
  useInboxFilterStore,
} from "@multica/core/inbox/filter-store";

import { issueHighlightMementoKey } from "../../issues/components/issue-highlight-memento";
import { useViewStateWriter } from "../../platform";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";
import { useNavigation, useReportNavigating } from "../../navigation";
import { toast } from "sonner";
import {
  MoreHorizontal,
  Inbox,
  CheckCheck,
  Archive,
  ArchiveRestore,
  BookCheck,
  ChevronLeft,
  ListChecks,
  ArrowLeft,
} from "lucide-react";
import type { InboxItem } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  ResizablePanelGroup,
  ResizablePanel,
  ResizableHandle,
} from "@multica/ui/components/ui/resizable";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { NumberFlow } from "@multica/ui/components/ui/number-flow";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@multica/ui/components/ui/dropdown-menu";
import { useIsCompact } from "@multica/ui/hooks/use-mobile";
import { cn } from "@multica/ui/lib/utils";
import { PAGE_GUTTER, PageHeader } from "../../layout/page-header";
import { useTimeAgo } from "./inbox-list-item";
import { InboxList } from "./inbox-list";
import { InboxFilterMenu } from "./inbox-filter-menu";
import { InboxContextMenuProvider } from "./inbox-context-menu";
import { ARCHIVED_VIEW_PARAM, type InboxView } from "./inbox-view";
import { useTypeLabels } from "./inbox-detail-label";
import {
  getInboxDisplayTitle,
  isQuickCreateOutcome,
  resolveDetailItem,
} from "./inbox-display";
import { useT } from "../../i18n";

const IssueDetail = lazy(() =>
  import("../../issues/components/issue-detail").then((module) => ({ default: module.IssueDetail })),
);

/**
 * 收件箱页：左侧是去重后的通知列表（收件箱 / 归档两个视图，支持筛选与
 * 批量操作），右侧渲染任务详情（懒加载）或纯通知详情；列表与选中状态
 * 与 URL 参数 `issue` / `view` 双向同步。
 */
export function InboxPage() {
  const { t } = useT("inbox");
  const { searchParams, replace } = useNavigation();
  const urlIssue = searchParams.get("issue") ?? "";
  const urlView: InboxView =
    searchParams.get("view") === ARCHIVED_VIEW_PARAM ? "archived" : "inbox";
  const wsPaths = useWorkspacePaths();

  const [selectedKey, setSelectedKeyState] = useState(() => urlIssue);
  const pinnedUnreadKeyRef = useRef<string | null>(null);
  const pinnedIdleKeyRef = useRef<string | null>(null);
  const [view, setViewState] = useState<InboxView>(() => urlView);

  // URL 变化（例如导航）时把选中同步回 state。
  useEffect(() => {
    setSelectedKeyState(urlIssue);
  }, [urlIssue]);
  useEffect(() => {
    setViewState(urlView);
  }, [urlView]);

  const wsId = useWorkspaceId();
  const { data: rawItems = [], isLoading: loading } = useQuery(inboxListOptions(wsId));
  const items = useMemo(() => deduplicateInboxItems(rawItems), [rawItems]);

  // 归档列表在两个视图都拉取，而不只在归档视图：主列表的归档入口要用这个计数
  // 配数字，所以必须等用户进去之前就已知。
  const {
    data: rawArchivedItems = [],
    isLoading: archivedLoading,
    isError: archivedError,
  } = useQuery(archivedInboxListOptions(wsId));
  const archivedItems = useMemo(
    () => deduplicateArchivedInboxItems(rawArchivedItems),
    [rawArchivedItems],
  );

  const isArchivedView = view === "archived";
  const viewItems = isArchivedView ? archivedItems : items;
  const filters = useInboxFilters(wsId);
  const clearFilters = useInboxFilterStore((state) => state.clearFilters);
  // 活跃与归档两个接口返回同一份行契约，且本页两者都已加载。要求「合并响应」才
  // 暴露能力，能防止滚动发布里一个 pod 宣称的能力另一个 pod 还没有。
  const priorityFilterSupport = useMemo(
    () => inboxPriorityFilterSupport([...rawItems, ...rawArchivedItems]),
    [rawItems, rawArchivedItems],
  );
  const effectiveFilters = useMemo(() => {
    const supported = inboxFiltersForPrioritySupport(filters, priorityFilterSupport);
    return isArchivedView ? inboxFiltersForArchive(supported) : supported;
  }, [filters, priorityFilterSupport, isArchivedView]);
  const { data: workingAgents } = useQuery({
    ...workspaceWorkingAgentsOptions(wsId, "issue"),
    enabled: !isArchivedView && effectiveFilters.idleOnly,
  });
  const activeIssueIds = useMemo(() => new Set(workingAgents?.flatMap((agent) => agent.issue_ids) ?? []), [workingAgents]);
  const pinnedUnreadKey = effectiveFilters.unreadOnly && pinnedUnreadKeyRef.current === selectedKey ? selectedKey : null;
  const pinnedIdleKey = effectiveFilters.idleOnly && pinnedIdleKeyRef.current === selectedKey ? selectedKey : null;
  const filterContext = useMemo(() => ({activeIssueIds, pinnedUnreadKey, pinnedIdleKey}), [activeIssueIds, pinnedUnreadKey, pinnedIdleKey]);
  const visibleItems = useMemo(
    () => filterInboxItems(viewItems, effectiveFilters, filterContext),
    [viewItems, effectiveFilters, filterContext],
  );
  const hasActiveFilters = inboxFilterCount(effectiveFilters) > 0;

  const selected =
    visibleItems.find((i) => (i.issue_id ?? i.id) === selectedKey) ?? null;
  useEffect(() => {
    if (pinnedUnreadKeyRef.current !== selectedKey || !effectiveFilters.unreadOnly) pinnedUnreadKeyRef.current = null;
    if (pinnedIdleKeyRef.current !== selectedKey || !effectiveFilters.idleOnly) pinnedIdleKeyRef.current = null;
    if (effectiveFilters.idleOnly && selected && workingAgents) pinnedIdleKeyRef.current = selectedKey;
  }, [selectedKey, selected, workingAgents, effectiveFilters.unreadOnly, effectiveFilters.idleOnly]);

  const selectedInView =
    viewItems.find((i) => (i.issue_id ?? i.id) === selectedKey) ?? null;
  const selectionFilteredOut =
    selectedKey.length > 0 && selectedInView !== null && selected === null;

  // 详情面板展示什么，比点击晚一个 React transition。
  //
  // 挂载 `IssueDetail` 是换行里最贵的半边；如果直接跟 `selectedKey` 走，它会以
  // urgent 更新运行：主线程从点击那一刻一直卡到新详情就绪，也就是桌面端曾经的
  // 「点击、冻结、跳变」（MUL-6404）。延迟后，点击立刻提交行高亮和 URL，React
  // 以 transition 优先级渲染新详情——可中断，外壳持续绘制——旧任务在就绪前一直
  // 留在屏幕上。
  const detailKey = useDeferredValue(selectedKey);
  const detailSwapping = detailKey !== selectedKey;
  const detailItem = resolveDetailItem(visibleItems, selectedKey, detailKey);

  // 上面的延迟对外壳导航适配器不可见——收件箱始终在同一路由，只改写 `?issue=`——
  // 所以这里显式上报，外壳的进度条在两种平台上都能盖住这次切换。
  useReportNavigating(detailSwapping);

  // 记住「最后一次真的在收件箱列表里解析成功」的 key。这让兜底 effect 能区分
  // 「分享链接指向不在收件箱里的通知」（从未解析 → 跳任务页）与「条目原本在
  // 收件箱、刚被移除」（曾解析 → 留在 /inbox）。
  const lastResolvedKeyRef = useRef<string>("");
  useEffect(() => {
    if (selected) lastResolvedKeyRef.current = selectedKey;
  }, [selected, selectedKey]);

  // 视图与选中都活在 URL 里，所以每次写入都要带上另一个——裸写 `?issue=` 会在
  // 下一次选中时把用户悄悄甩出归档视图。
  const buildInboxUrl = useCallback(
    (nextView: InboxView, key: string) => {
      const params = new URLSearchParams();
      if (nextView === "archived") params.set("view", ARCHIVED_VIEW_PARAM);
      if (key) params.set("issue", key);
      const query = params.toString();
      const inboxPath = wsPaths.inbox();
      return query ? `${inboxPath}?${query}` : inboxPath;
    },
    [wsPaths],
  );

  const setSelectedKey = useCallback((key: string) => {
    setSelectedKeyState(key);
    replace(buildInboxUrl(view, key));
  }, [replace, buildInboxUrl, view]);

  // 切换视图总是清空选中：两个列表互斥，跨视图带过去的 key 永远解析不到，
  // 下面的兜底 effect 会把用户弹到任务页去。
  const setView = useCallback((nextView: InboxView) => {
    setViewState(nextView);
    setSelectedKeyState("");
    replace(buildInboxUrl(nextView, ""));
  }, [replace, buildInboxUrl]);

  // 稳定身份：InboxList 对归档入口做了 memo；这里如果写行内箭头，每次渲染都会
  // 重建（并重挂载）这个入口。
  const openArchived = useCallback(() => setView("archived"), [setView]);

  // 应用筛选可能把当前打开的行筛出列表。这里清掉本地选中，而不是当成坏掉的
  // 深链跳去任务页：通知仍然存在，只是被过滤了而已。
  useEffect(() => {
    if (selectionFilteredOut) setSelectedKey("");
  }, [selectionFilteredOut, setSelectedKey]);

  // 当前屏幕上的列表是否已完成首次加载。下面两个兜底 effect 都以它为准；
  // 在归档视图里搞错它，等于对一个「只是还没到货」的空列表做动作。
  const viewLoading = isArchivedView ? archivedLoading : loading;

  // 共享的收件箱链接（?issue=<id>）可能指向不在该用户收件箱里的通知（已归档，
  // 或从没收到过）。退回任务页，让 URL 仍解析到有意义的地方。但如果这个 key
  // 之前能解析（例如该任务刚在别的标签页被删、`onInboxIssueDeleted` 已清掉
  // 缓存），任务详情也会 404——那就清空选中、留在 /inbox。
  useEffect(() => {
    if (viewLoading) return;
    if (!selectedKey) return;
    if (selected) return;
    if (selectionFilteredOut) return;
    if (lastResolvedKeyRef.current === selectedKey) {
      setSelectedKey("");
      return;
    }
    replace(wsPaths.issueDetail(selectedKey));
  }, [
    viewLoading,
    selectedKey,
    selected,
    selectionFilteredOut,
    replace,
    wsPaths,
    setSelectedKey,
  ]);

  // 别把用户困在空归档里：当最后一条归档任务被还原（或新通知把它带回主收件箱）
  // 时，退回主列表。与聊天归档视图同样的兜底。以加载完成作为门控，避免冷启动
  // `?view=archived` 在数据落地前就被弹走。
  useEffect(() => {
    if (!isArchivedView) return;
    if (archivedLoading) return;
    // 拉取失败也等于「没有条目」——在这弹走会把错误信息换成主列表，用户连失败
    // 都不知道。
    if (archivedError) return;
    // 先让上面的兜底 effect 处理掉解析不到的选中。像 `?view=archived&issue=X`
    // 深链进空归档时，两个 effect 会在同一提交同时触发，后者的 replace() 会
    // 最后落地、把去 X 的重定向吞掉。
    if (selectedKey) return;
    if (archivedItems.length > 0) return;
    setView("inbox");
  }, [
    isArchivedView,
    archivedLoading,
    archivedError,
    archivedItems.length,
    selectedKey,
    setView,
  ]);

  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_inbox_layout",
  });

  const isCompact = useIsCompact();
  const unreadCount = useInboxUnreadCount(wsId);

  const markReadMutation = useMarkInboxRead();
  const markUnreadMutation = useMarkInboxUnread();
  const archiveMutation = useArchiveInbox();
  const unarchiveMutation = useUnarchiveInbox();
  const markAllReadMutation = useMarkAllInboxRead();
  const archiveAllMutation = useArchiveAllInbox();
  const archiveAllReadMutation = useArchiveAllReadInbox();
  const archiveCompletedMutation = useArchiveCompletedInbox();
  const retrySourceContextMutation = useRetrySourceContextQuickCreate();
  const timeAgo = useTimeAgo();
  const typeLabels = useTypeLabels();


  // 对「当前打开的行」显式标为未读，必须扛过下面那个自动已读 effect——否则它在
  // 下一个提交就会触发，把用户的操作悄悄撤掉。这里只短暂记录该条目的 id，且只在
  // 它保持选中期间有效：选中移走即释放守卫，之后重新打开这行会和其它行一样被标读。
  const manualUnreadIdRef = useRef<string | null>(null);

  // 只要选中了一条未读条目就自动标读——覆盖点击选中和 URL 参数选中（如桌面端点
  // OS 通知）两种情况。mutation 乐观地把 `read: true` 翻掉，所以这个 effect 一遍
  // 就收敛、不会死循环。放在 `useEffect` 而不是 handleSelect 里，是为了 URL 驱动的
  // 选中也能触发它。
  const markReadMutate = markReadMutation.mutate;
  const selectedId = selected?.id;
  const selectedRead = selected?.read;
  useEffect(() => {
    if (!selectedId || selectedRead) return;
    if (manualUnreadIdRef.current === selectedId) return;
    pinnedUnreadKeyRef.current = selectedKey;
    markReadMutate(selectedId, {
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.errors.mark_read_failed),
        ),
    });
  }, [selectedId, selectedKey, selectedRead, markReadMutate, t]);

  // 选中一离开被「留住」的行，就释放守卫。
  useEffect(() => {
    if (manualUnreadIdRef.current && manualUnreadIdRef.current !== selectedId) {
      manualUnreadIdRef.current = null;
    }
  }, [selectedId]);

  const writeViewState = useViewStateWriter();
  // 已打开的通知行被再次点击时递增；穿给 IssueDetail，让它不用重挂载就能重放
  // 评论高亮的落地。选中变化不需要它——选中会按任务 key 重挂载详情，新挂载带
  // 着已清空的 memento 项，自己就能落地。
  const [highlightRequestToken, setHighlightRequestToken] = useState(0);
  const handleSelect = (item: InboxItem) => {
    const nextKey = item.issue_id ?? item.id;
    // 每次点通知行都是一次全新的深链意图：清掉「高亮已落地」的 memento 项，
    // 让评论跳转即使在这个任务详情以前开过（且落地被消费过）也会再次执行。
    // 重挂载时从 URL 还原的选中走上面的 effect、不走这里，所以切标签页回来会
    // 保留该项、不再跳一次。
    if (item.issue_id) {
      writeViewState(issueHighlightMementoKey(item.issue_id), undefined);
      if (nextKey === selectedKey) {
        setHighlightRequestToken((t) => t + 1);
      }
    }
    setSelectedKey(nextKey);
  };

  const handleMarkRead = (id: string) => {
    // 显式标读会取消同一行早先的「未读留住」。
    if (manualUnreadIdRef.current === id) manualUnreadIdRef.current = null;
    markReadMutation.mutate(id, {
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.errors.mark_read_failed),
        ),
    });
  };

  const handleMarkUnread = (id: string) => {
    // 只有打开的那行需要守卫——自动标读从不碰其它行，给后台行装上守卫反而会
    // 压制它之后第一次被打开时的标读。
    if (selected?.id === id) manualUnreadIdRef.current = id;
    markUnreadMutation.mutate(id, {
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.errors.mark_unread_failed),
        ),
    });
  };

  // 归档和取消归档都会把被操作的行从它所在的列表里移除，所以两者都要先把选中
  // 移走。`list` 是行来自的那个列表——归档用主列表，取消归档用归档列表。
  const advanceSelectionPast = (id: string, list: InboxItem[]) => {
    const idx = list.findIndex((i) => i.id === id);
    const target = idx >= 0 ? list[idx] : null;
    const wasSelected = !!target && (target.issue_id ?? target.id) === selectedKey;
    if (!wasSelected) return;
    // 列表按最新在前排序；优先下一个（更旧）的，在底部操作时退回上一个（更新）的，
    // 只剩自己时才清空选中。
    const next = list[idx + 1] ?? list[idx - 1] ?? null;
    setSelectedKey(next ? (next.issue_id ?? next.id) : "");
  };

  // toast 放在这些共享 handler 里，让所有归档入口的确认文案一致。
  const handleArchive = (id: string) => {
    advanceSelectionPast(id, visibleItems);
    archiveMutation.mutate(id, {
      onSuccess: () => toast.success(t(($) => $.toasts.archived)),
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.errors.archive_failed),
        ),
    });
  };

  const handleUnarchive = (id: string) => {
    advanceSelectionPast(id, visibleItems);
    unarchiveMutation.mutate(id, {
      onSuccess: () => toast.success(t(($) => $.toasts.unarchived)),
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.errors.unarchive_failed),
        ),
    });
  };

  // 监听器保持稳定，同时总用最新的「对选中项的操作」。
  const actionOnSelectedRef = useRef<(() => void) | null>(null);
  useEffect(() => {
    actionOnSelectedRef.current = selected
      ? () => (isArchivedView ? handleUnarchive(selected.id) : handleArchive(selected.id))
      : null;
  });

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat || isImeComposing(event)) return;
      if (isEditableShortcutTarget(event.target)) return;
      if (isPortalLayerShortcutTarget(event.target)) return;
      if (useModalStore.getState().modal) return;
      if (!shortcutMatchesEvent(getShortcut("archiveInboxItem"), event)) return;
      const run = actionOnSelectedRef.current;
      if (!run) return;
      event.preventDefault();
      run();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, []);

  // 批量操作（只出现在主视图）
  const handleMarkAllRead = () => {
    markAllReadMutation.mutate(undefined, {
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.errors.mark_all_read_failed),
        ),
    });
  };

  const handleArchiveAll = () => {
    setSelectedKey("");
    archiveAllMutation.mutate(undefined, {
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.errors.archive_all_failed),
        ),
    });
  };

  const handleArchiveAllRead = () => {
    const readKeys = items.filter((i) => i.read).map((i) => i.issue_id ?? i.id);
    if (readKeys.includes(selectedKey)) setSelectedKey("");
    archiveAllReadMutation.mutate(undefined, {
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.errors.archive_all_read_failed),
        ),
    });
  };

  const handleArchiveCompleted = () => {
    setSelectedKey("");
    archiveCompletedMutation.mutate(undefined, {
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.errors.archive_completed_failed),
        ),
    });
  };

  // -- 共享子组件 ------------------------------------------------------------

  const listHeader = (
    <PageHeader>
      <div className="flex flex-1 items-center gap-2">
        <h1 className="text-body font-semibold">{t(($) => $.page.title)}</h1>
        {unreadCount > 0 && (
          <NumberFlow
            value={unreadCount}
            animated={false}
            format={{ maximumFractionDigits: 0 }}
            aria-label={String(unreadCount)}
            className="text-caption text-muted-foreground"
          />
        )}
      </div>
      <InboxFilterMenu
        wsId={wsId}
        items={viewItems}
        priorityFilterSupport={priorityFilterSupport}
        archived={isArchivedView}
      />
      {/* 批量操作只出现在主视图。每条入口都是「从主收件箱归档」——在归档列表
          上提供它们会被读成「把这些全部归档」，做出来的事正好和字面相反。 */}
      {!isArchivedView && (
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              className="text-muted-foreground"
            />
          }
        >
          <MoreHorizontal className="h-4 w-4" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-auto">
          <DropdownMenuItem onClick={handleMarkAllRead}>
            <CheckCheck className="h-4 w-4" />
            {t(($) => $.menu.mark_all_read)}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={handleArchiveAll}>
            <Archive className="h-4 w-4" />
            {t(($) => $.menu.archive_all)}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={handleArchiveAllRead}>
            <BookCheck className="h-4 w-4" />
            {t(($) => $.menu.archive_all_read)}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={handleArchiveCompleted}>
            <ListChecks className="h-4 w-4" />
            {t(($) => $.menu.archive_completed)}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      )}
    </PageHeader>
  );

  // 从归档返回。放在列表面板里而不是替换 PageHeader：用户仍在收件箱，页面标题
  // 保持不动，这里读起来像子视图——与聊天归档视图同款结构。
  const archivedBackRow = (
    <button
      type="button"
      onClick={() => setView("inbox")}
      className="flex w-full shrink-0 items-center gap-1.5 border-b px-3 py-2 text-left text-caption font-medium text-muted-foreground outline-none transition-colors hover:bg-accent/50 hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
    >
      <ChevronLeft className="size-4 shrink-0" />
      <span className="truncate">{t(($) => $.list.archived_title)}</span>
      <span className="ml-auto shrink-0 tabular-nums text-muted-foreground">
        {archivedItems.length}
      </span>
    </button>
  );

  const list = archivedError && isArchivedView ? (
    <div className="flex-1 min-h-0 overflow-y-auto">
      <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
        <Archive className="mb-3 h-8 w-8 text-faint-foreground" />
        <p className="text-body">{t(($) => $.errors.archived_load_failed)}</p>
      </div>
    </div>
  ) : (
    <InboxContextMenuProvider
      view={view}
      actions={{
        onMarkRead: handleMarkRead,
        onMarkUnread: handleMarkUnread,
        onAction: isArchivedView ? handleUnarchive : handleArchive,
      }}
    >
      <InboxList
        items={visibleItems}
        view={view}
        selectedKey={selectedKey}
        archivedCount={archivedItems.length}
        onSelect={handleSelect}
        onAction={isArchivedView ? handleUnarchive : handleArchive}
        onOpenArchived={openArchived}
        emptyLabel={
          hasActiveFilters && viewItems.length > 0 && visibleItems.length === 0
            ? t(($) => $.filters.empty)
            : undefined
        }
        emptyAction={
          hasActiveFilters && viewItems.length > 0 && visibleItems.length === 0 ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => clearFilters(wsId)}
            >
              {t(($) => $.filters.clear)}
            </Button>
          ) : undefined
        }
      />
    </InboxContextMenuProvider>
  );

  const listPanel = (
    <>
      {listHeader}
      {isArchivedView && archivedBackRow}
      {list}
    </>
  );

  // 仅紧凑宽度：详情在那里占满整屏，所以回到列表的控件必须活在详情内部。桌面端
  // 列表仍在左面板里可见，根本不需要返回控件。
  //
  // 详情在四种状态里拥有整块紧凑屏幕——已加载、加载中、未找到、崩溃——所以下面
  // 四个状态都要穿进这个控件。漏掉一个，那个状态就会把读者困在没有出口的地方。
  const compactBackAction = isCompact ? (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => setSelectedKey("")}
      className="-ml-2 shrink-0 gap-1.5 text-muted-foreground"
    >
      <ArrowLeft className="h-4 w-4" />
      {/* 返回要去的是用户「来时的」那个列表，所以文案要说出它的名字——这里写
          「收件箱」就是对目的地的撒谎。 */}
      {isArchivedView ? t(($) => $.list.archived_title) : t(($) => $.page.back)}
    </Button>
  ) : undefined;

  const compactBackBar = compactBackAction ? (
    <div className={cn("flex h-12 shrink-0 items-center gap-2 border-b", PAGE_GUTTER)}>
      {compactBackAction}
    </div>
  ) : null;

  const detailContent = detailItem?.issue_id ? (
    // 按 issue_id（而不是收件箱条目 id）作 key：新评论/新反应会给同一任务生成新的
    // 收件箱通知，去重助手会挑最新一条——用它的 id 作 key 会让每条事件都重挂载
    // IssueDetail，把评论草稿和滚动位置全清掉。
    <ErrorBoundary
      resetKeys={[detailItem.issue_id]}
      // 默认 fallback 只是一张干巴巴的错误卡。在手机上它是屏幕上唯一的东西，所以
      // 必须带上回去的路——重点是那条返回栏，文案只是错误边界抓到的东西。
      fallback={compactBackAction ? ({ error }) => (
        <div className="flex flex-1 min-h-0 flex-col">
          {compactBackBar}
          <div className="flex flex-1 min-h-0 items-center justify-center px-4 text-center text-body text-muted-foreground">
            {error.message}
          </div>
        </div>
      ) : undefined}
    >
      <Suspense fallback={<Skeleton className="h-full w-full" />}>
        <IssueDetail
          key={detailItem.issue_id}
          issueId={detailItem.issue_id}
          defaultSidebarOpen={false}
          layoutId="multica_inbox_issue_detail_layout"
          highlightCommentId={detailItem.details?.comment_id ?? undefined}
          highlightRequestToken={highlightRequestToken}
          leadingAction={compactBackAction}
          onDelete={() => {
            // 删除任务会在服务端级联删掉收件箱条目，`issue:deleted` 的 WS 事件会
            // 把它从收件箱缓存里清掉。这里只需清空选中——调 archive 会对一条已
            // 不存在的行返回 404。
            setSelectedKey("");
          }}
          onDone={() => {
            handleArchive(detailItem.id);
          }}
        />
      </Suspense>
    </ErrorBoundary>
  ) : detailItem ? (
    <div className="p-6">
      <h2 className="text-title font-semibold">{getInboxDisplayTitle(detailItem)}</h2>
      <p className="mt-1 text-body text-muted-foreground">
        {typeLabels[detailItem.type]} · {timeAgo(detailItem.created_at)}
      </p>
      {detailItem.body && (
        <div className="mt-4 whitespace-pre-wrap text-body leading-relaxed text-foreground">
          {detailItem.body}
        </div>
      )}
      {isQuickCreateOutcome(detailItem.type) && detailItem.details?.original_prompt && (
        <div className="mt-4 rounded-md border bg-muted/40 p-3">
          <p className="text-caption font-medium text-muted-foreground">
            {t(($) => $.detail.original_input)}
          </p>
          <p className="mt-1 whitespace-pre-wrap text-body">{detailItem.details.original_prompt}</p>
        </div>
      )}
      <div className="mt-4 flex gap-2">
        {detailItem.type === "quick_create_failed" &&
          detailItem.details?.source_context_id &&
          detailItem.details?.task_id && (
            <Button
              size="sm"
              data-testid="retry-source-context"
              disabled={retrySourceContextMutation.isPending}
              onClick={async () => {
                try {
                  await retrySourceContextMutation.mutateAsync(detailItem.details!.task_id!);
                  toast.success(t(($) => $.toasts.source_context_retry_started));
                } catch (error) {
                  toast.error(
                    error instanceof ApiError &&
                      errorCode(error) === "source_context_retry_unavailable"
                      ? t(($) => $.errors.source_context_retry_unavailable)
                      : t(($) => $.errors.source_context_retry_failed),
                  );
                }
              }}
            >
              {t(($) => $.detail.retry_with_context)}
            </Button>
          )}
        {isQuickCreateOutcome(detailItem.type) && (
          <Button
            size="sm"
            onClick={() => {
              // 用原始提示词预填旧的进阶表单，让用户能在完整编辑器里找回输入，
              // 而不是重新打一遍。Agent 选择提示会变成「负责人」候选（仍可改）。
              const prompt = detailItem.details?.original_prompt ?? "";
              const agentId = detailItem.details?.agent_id;
              useIssueDraftStore.getState().setManual({
                description: prompt,
                ...(agentId
                  ? { assigneeType: "agent" as const, assigneeId: agentId }
                  : {}),
              });
              useModalStore.getState().open("create-issue");
            }}
          >
            {t(($) => $.detail.edit_advanced)}
          </Button>
        )}
        {/* 与行的动作互相对应：按钮永远做「正在读这条的视图」的反向操作。 */}
        {isArchivedView ? (
          <Button
            variant="outline"
            size="sm"
            onClick={() => handleUnarchive(detailItem.id)}
          >
            <ArchiveRestore className="mr-1.5 h-3.5 w-3.5" />
            {t(($) => $.detail.unarchive)}
          </Button>
        ) : (
          <Button
            variant="outline"
            size="sm"
            onClick={() => handleArchive(detailItem.id)}
          >
            <Archive className="mr-1.5 h-3.5 w-3.5" />
            {t(($) => $.detail.archive)}
          </Button>
        )}
      </div>
    </div>
  ) : null;

  // -- 紧凑布局：列表 / 详情切换 -----------------------------------------------

  if (isCompact) {
    if (viewLoading) {
      return (
        <div className="flex flex-1 flex-col min-h-0">
          <div className={cn("flex h-12 shrink-0 items-center border-b", PAGE_GUTTER)}>
            <Skeleton className="h-5 w-16" />
          </div>
          <div className="flex-1 min-h-0 overflow-y-auto space-y-1 p-2">
            {Array.from({ length: 5 }).map((_, i) => (
              <div key={i} className="flex items-center gap-3 px-2 py-2.5">
                <Skeleton className="h-7 w-7 shrink-0 rounded-full" />
                <div className="flex-1 space-y-2">
                  <Skeleton className="h-4 w-3/4" />
                  <Skeleton className="h-3 w-1/2" />
                </div>
              </div>
            ))}
          </div>
        </div>
      );
    }

    // 紧凑布局：选中条目时整屏展示详情。两种选中的 chrome 来自不同地方，渲染
    // 也不同——`InboxItem.issue_id` 可为空，为空是纯通知（如一次失败的快速创建），
    // 不是任务。
    if (detailItem?.issue_id) {
      // 不包自己的滚动容器也不包返回栏：`IssueDetail` 两者都自带，返回走
      // `leadingAction`。以前包一层 `overflow-y-auto` 会把它的内层滚动器压成内容
      // 高度——头部（连同 done/pin/more/sidebar 操作）离开钉住的位置、内部
      // `position: sticky` 失效，滚动还原与时间线虚拟化还指向一个从不滚动的元素。
      // 这个包装只负责给详情一个确定的高度去填满。
      return <div className="flex flex-1 flex-col min-h-0">{detailContent}</div>;
    }

    if (detailItem) {
      // 通知正文是一块纯文本，没有可承载 leading 插槽的头部、也没有自己的滚动器，
      // 所以这个分支继续提供两者。
      return (
        <div className="flex flex-1 flex-col min-h-0">
          {compactBackBar}
          <div className="flex-1 min-h-0 overflow-y-auto">{detailContent}</div>
        </div>
      );
    }

    // 紧凑布局：整屏列表
    return <div className="flex flex-1 flex-col min-h-0">{listPanel}</div>;
  }

  // -- 桌面布局：可调宽度的左右两栏 -------------------------------------------

  if (viewLoading) {
    return (
      <ResizablePanelGroup orientation="horizontal" className="flex-1 min-h-0" defaultLayout={defaultLayout} onLayoutChanged={onLayoutChanged}>
        <ResizablePanel id="list" defaultSize={320} minSize={240} maxSize={480} groupResizeBehavior="preserve-pixel-size">
          <div className="flex flex-col border-r h-full">
            <div className={cn("flex h-12 shrink-0 items-center border-b", PAGE_GUTTER)}>
              <Skeleton className="h-5 w-16" />
            </div>
            <div className="flex-1 min-h-0 overflow-y-auto space-y-1 p-2">
              {Array.from({ length: 5 }).map((_, i) => (
                <div key={i} className="flex items-center gap-3 px-2 py-2.5">
                  <Skeleton className="h-7 w-7 shrink-0 rounded-full" />
                  <div className="flex-1 space-y-2">
                    <Skeleton className="h-4 w-3/4" />
                    <Skeleton className="h-3 w-1/2" />
                  </div>
                </div>
              ))}
            </div>
          </div>
        </ResizablePanel>
        <ResizableHandle />
        <ResizablePanel id="detail" minSize="40%">
          <div className="p-6">
            <Skeleton className="h-6 w-48" />
            <Skeleton className="mt-4 h-4 w-32" />
          </div>
        </ResizablePanel>
      </ResizablePanelGroup>
    );
  }

  return (
    <ResizablePanelGroup orientation="horizontal" className="flex-1 min-h-0" defaultLayout={defaultLayout} onLayoutChanged={onLayoutChanged}>
      <ResizablePanel id="list" defaultSize={320} minSize={240} maxSize={480} groupResizeBehavior="preserve-pixel-size">
      <div className="flex flex-col border-r h-full">
        {listPanel}
      </div>
      </ResizablePanel>
      <ResizableHandle />
      <ResizablePanel id="detail" minSize="40%">
      <div className="flex flex-col min-h-0 h-full">
        {detailContent ?? (
          <div className="flex h-full flex-col items-center justify-center text-muted-foreground">
            <Inbox className="mb-3 h-10 w-10 text-faint-foreground" />
            <p className="text-body">
              {visibleItems.length === 0
                ? t(($) => $.detail.empty)
                : t(($) => $.detail.select_prompt)}
            </p>
          </div>
        )}
      </div>
      </ResizablePanel>
    </ResizablePanelGroup>
  );
}