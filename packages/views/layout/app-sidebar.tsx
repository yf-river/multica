"use client";

import { issueStatusCategory } from "@multica/core/issues";
import React, { useCallback, useEffect, useRef, useState } from "react";
import { cn } from "@multica/ui/lib/utils";
import { useScrollFade } from "@multica/ui/hooks/use-scroll-fade";
import { AppLink, useNavigation } from "../navigation";
import {
  DndContext,
  PointerSensor,
  useSensor,
  useSensors,
  closestCenter,
  type DragEndEvent,
} from "@dnd-kit/core";
import { SortableContext, verticalListSortingStrategy, useSortable, arrayMove } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { Layers,
  ChevronDown,
  ChevronRight,
  LogOut,
  Plus,
  Check,
  SquarePen,
  X,
} from "lucide-react";
import { EnvironmentBadge } from "../common/environment-badge";
import { WorkspaceAvatar } from "../workspace/workspace-avatar";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip";
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from "@multica/ui/components/ui/collapsible";
import { CappedNumberFlow } from "@multica/ui/components/ui/number-flow";
import { StatusIcon } from "../issues/components/status-icon";
import { useIssueDraftStore } from "@multica/core/issues/stores/draft-store";
import { openCreateIssueWithPreference } from "@multica/core/issues/stores/create-mode-store";
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  useSidebar,
} from "@multica/ui/components/ui/sidebar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { useAuthStore } from "@multica/core/auth";
import { issueViewDetailOptions } from "@multica/core/issue-views/queries";
import {
  issueViewContainerKey,
  useActiveIssueViewStore,
} from "@multica/core/issue-views/active-view-store";
import { useCurrentWorkspace, useWorkspacePaths, paths } from "@multica/core/paths";
import { workspaceListOptions, myInvitationListOptions, workspaceKeys } from "@multica/core/workspace/queries";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { inboxUnreadSummaryEnabled, useInboxUnreadCount, inboxUnreadSummaryOptions, hasOtherWorkspaceUnread, unreadWorkspaceIds } from "@multica/core/inbox/queries";
import { api, ApiError } from "@multica/core/api";
import { useConfigStore } from "@multica/core/config";
import { pinListOptions } from "@multica/core/pins/queries";
import { useDeletePin, useReorderPins } from "@multica/core/pins/mutations";
import { issueDetailOptions } from "@multica/core/issues/queries";
import { projectDetailOptions } from "@multica/core/projects/queries";
import type { PinnedItem } from "@multica/core/types";
import { useLogout } from "../auth";
import { ProjectIcon } from "../projects/components/project-icon";
import { routeIconForPath } from "./route-icon-components";
import { useT } from "../i18n";
import {
  useShortcut,
} from "@multica/core/shortcuts";
import { ShortcutKeycaps } from "../common/shortcut-keycaps";
import { isWorkspaceMenuVisible } from "@multica/core/workspace";
import { MessageSquare } from "lucide-react";

/**
 * 布局模块：应用主侧边栏。
 * 渲染工作区切换器、新建 issue 按钮、个人/工作区/配置/生活导航、置顶项、
 * 待处理邀请和底部工具条，是整个仪表盘的导航中枢。
 */

// 顶层导航项在子路由下也保持高亮（例如 /:slug/projects/:id 下「项目」
// 仍亮着）。置顶项在其他地方保持严格相等——置顶的项目不应在自己的子页
// 上也高亮。
function isNavActive(pathname: string, href: string): boolean {
  return pathname === href || pathname.startsWith(href + "/");
}

// 为查询默认值提供稳定的空数组。若在 `useQuery` 上用内联 `= []` 默认值，
// 当 `data` 为 undefined（例如查询禁用或加载中）时每次渲染都会产生新的
// 数组引用——这会破坏依赖该值的 `useEffect`/`useMemo`，当 effect 自身调用
// `setState` 时还可能触发无限重渲染循环。
const EMPTY_PINS: PinnedItem[] = [];
const EMPTY_WORKSPACES: Awaited<ReturnType<typeof api.listWorkspaces>> = [];
const EMPTY_INVITATIONS: Awaited<ReturnType<typeof api.listMyInvitations>> = [];
const EMPTY_INBOX_SUMMARY: Awaited<ReturnType<typeof api.getInboxUnreadSummary>> = [];

// 导航项引用 WorkspacePaths 的方法名，以便在渲染时针对当前工作区 slug 解析
// （见 AppSidebar 主体）。只有无参数的路径才是合法的导航目标。
type NavKey =
  | "inbox"
  | "myIssues"
  | "issues"
  | "projects"
  | "autopilots"
  | "agents"
  | "squads"
  | "usage"
  | "runReviews"
  | "knowledge"
  | "runtimes"
  | "mcp"
  | "quickActions"
  | "skills"
  | "settings"
  | "upstream"
  | "life";

// 静态 schema（仅 key）——标签在渲染时通过 useT("layout") 解析，
// 图标通过 routeIconForPath 从目标路径推导。
type NavLabelKey =
  | "inbox"
  | "my_issues"
  | "issues"
  | "projects"
  | "autopilots"
  | "agents"
  | "squads"
  | "usage"
  | "run_reviews"
  | "knowledge"
  | "runtimes"
  | "mcp"
  | "quick_actions"
  | "skills"
  | "settings"
  | "upstream"
  | "life";

// 导航图标不在这里声明：它们在渲染时从每项的目标路径推导，这样每个资源
// 导航界面都一致。见 route-icon-components.tsx。
const personalNav: { key: NavKey; labelKey: NavLabelKey }[] = [
  { key: "inbox", labelKey: "inbox" },
];

const workspaceNav: { key: NavKey; labelKey: NavLabelKey }[] = [
  { key: "issues", labelKey: "issues" },
  { key: "projects", labelKey: "projects" },
  { key: "autopilots", labelKey: "autopilots" },
  { key: "agents", labelKey: "agents" },
  { key: "squads", labelKey: "squads" },
  { key: "usage", labelKey: "usage" },
  { key: "runReviews", labelKey: "run_reviews" },
  { key: "knowledge", labelKey: "knowledge" },
];

const configureNav: { key: NavKey; labelKey: NavLabelKey }[] = [
  { key: "runtimes", labelKey: "runtimes" },
  { key: "skills", labelKey: "skills" },
  { key: "mcp", labelKey: "mcp" },
  { key: "quickActions", labelKey: "quick_actions" },
  { key: "upstream", labelKey: "upstream" },
  { key: "settings", labelKey: "settings" },
];

const lifeNav = [
  { tab: "memory", labelKey: "memory" },
  { tab: "experiment", labelKey: "experiment" },
  { tab: "observers", labelKey: "observers" },
  { tab: "chronicle", labelKey: "chronicle" },
] as const;

const livingNav = ["health", "meals", "goals"] as const;

function DraftDot() {
  const hasDraft = useIssueDraftStore((s) => s.hasDraft());
  if (!hasDraft) return null;
  return <span className="absolute top-0 right-0 size-1.5 rounded-full bg-brand" />;
}

/**
 * 纯展示的置顶行。`label` 和 `iconNode` 由父级 `PinRow` 从缓存的 issue /
 * project 详情查询里算出——让这个组件保持「笨」一些，dnd-kit / 导航的接线
 * 就只住在一个地方，数据流也更明确。
 */
function SortablePinItem({
  pin,
  href,
  pathname,
  onUnpin,
  label,
  iconNode,
  onNavigate,
  isActiveOverride,
}: {
  pin: PinnedItem;
  href: string;
  pathname: string;
  onUnpin: () => void;
  label: string;
  iconNode: React.ReactNode;
  /** 在真实点击（而非拖拽松手）时、导航之前执行。 */
  onNavigate?: () => void;
  /** 覆盖默认的路径比较（view 置顶项携带额外状态）。 */
  isActiveOverride?: boolean;
}) {
  const { t } = useT("layout");
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: pin.id });
  const wasDragged = useRef(false);

  useEffect(() => {
    if (isDragging) wasDragged.current = true;
  }, [isDragging]);

  const style = { transform: CSS.Transform.toString(transform), transition };
  const isActive = isActiveOverride ?? pathname === href;

  return (
    <SidebarMenuItem
      ref={setNodeRef}
      style={style}
      className={cn("group/pin", isDragging && "opacity-30")}
      {...attributes}
      {...listeners}
    >
      <SidebarMenuButton
        size="sm"
        isActive={isActive}
        render={<AppLink href={href} draggable={false} />}
        onClick={(event) => {
          if (wasDragged.current) {
            wasDragged.current = false;
            event.preventDefault();
            return;
          }
          onNavigate?.();
        }}
        className={cn(
          "text-muted-foreground hover:not-data-active:bg-sidebar-accent/70 data-active:bg-sidebar-accent data-active:text-sidebar-accent-foreground",
          isDragging && "pointer-events-none",
        )}
      >
        {iconNode}
        <span
          className="min-w-0 flex-1 overflow-hidden whitespace-nowrap"
          style={{
            maskImage: "linear-gradient(to right, black calc(100% - 12px), transparent)",
            WebkitMaskImage: "linear-gradient(to right, black calc(100% - 12px), transparent)",
          }}
        >{label}</span>
        <Tooltip>
          <TooltipTrigger
            render={<span role="button" />}
            className="hidden size-2.5 shrink-0 items-center justify-center rounded-sm text-muted-foreground group-hover/pin:flex hover:text-foreground"
            onClick={(event) => {
              event.preventDefault();
              event.stopPropagation();
              onUnpin();
            }}
          >
            <X className="size-1" />
          </TooltipTrigger>
          <TooltipContent side="top" sideOffset={4}>{t(($) => $.sidebar.unpin_tooltip)}</TooltipContent>
        </Tooltip>
      </SidebarMenuButton>
    </SidebarMenuItem>
  );
}

/**
 * 智能包装：从 issue / project 详情查询缓存解析置顶项的展示数据（标签 +
 * 状态/图标）。两个查询都无条件声明、用 `enabled` 门控，这样无论
 * `pin.item_type` 如何变化，hook 顺序都保持稳定。
 *
 * 加载中：渲染一个扁平骨架，避免侧边栏高度跳动。
 * 缺失（已删除项 / 404）：渲染空——该行隐藏自身，直到用户手动取消置顶
 * 或服务端级联清理跟上。
 */
function PinRow({
  pin,
  href,
  pathname,
  onUnpin,
  wsId,
}: {
  pin: PinnedItem;
  href: string;
  pathname: string;
  onUnpin: () => void;
  wsId: string;
}) {
  const isIssue = pin.item_type === "issue";
  const isView = pin.item_type === "view";
  const p = useWorkspacePaths();
  const setActiveView = useActiveIssueViewStore((s) => s.setActive);
  const issueQuery = useQuery({
    ...issueDetailOptions(wsId, pin.item_id),
    enabled: isIssue,
  });
  const projectQuery = useQuery({
    ...projectDetailOptions(wsId, pin.item_id),
    enabled: pin.item_type === "project",
  });
  const viewQuery = useQuery({
    ...issueViewDetailOptions(wsId, pin.item_id),
    enabled: isView,
  });

  const triggeredRef = useRef(false);
  useEffect(() => {
    // view 不参与 404 自动取消置顶：已安装的客户端连到没有 view 端点的旧
    // 后端时，每个 view 置顶都会得到 404——自动取消置顶会把它们全部永久删掉。
    // 已删除的 view 行只是隐藏而已。
    if (isView) return;
    const err = isIssue ? issueQuery.error : projectQuery.error;
    if (err instanceof ApiError && err.status === 404 && !triggeredRef.current) {
      triggeredRef.current = true;
      onUnpin();
    }
  }, [isIssue, isView, issueQuery.error, onUnpin, projectQuery.error]);

  const activeViewByContainer = useActiveIssueViewStore((s) => s.active);
  if (isView) {
    if (viewQuery.isPending) return <PinSkeleton />;
    if (viewQuery.isError || !viewQuery.data) return null;
    const view = viewQuery.data;
    // 解析出的 scope 同时驱动路径和容器 key，这样来自更新后端的未知
    // scope_type 也能一致地降级。
    const scopeType: "workspace" | "my" | "project" =
      view.scope_type === "my"
        ? "my"
        : view.scope_type === "project" && view.scope_id
          ? "project"
          : "workspace";
    const viewPath =
      scopeType === "my"
        ? p.issues()
        : scopeType === "project"
          ? p.projectDetail(view.scope_id!)
          : p.issues();
    const containerKey = issueViewContainerKey(wsId, {
      scope_type: scopeType,
      scope_id: scopeType === "project" ? view.scope_id : null,
    });
    return (
      <SortablePinItem
        pin={pin}
        // ?view= 让挂载了 URL 同步 hook 的界面（/issues、/my-issues）在刷新
        // 后仍停留在该 view。项目页还没同步——那里 query 是惰性的，刷新会
        // 回退到普通页面；点击激活在任何地方都仍然有效。
        href={`${viewPath}?view=${view.id}`}
        pathname={pathname}
        onUnpin={onUnpin}
        label={view.name}
        iconNode={<Layers className="!size-3.5 shrink-0" />}
        // 只有当这个精确的 view 在它的界面上打开时才高亮——仅凭路径也会
        // 匹配普通标签页。
        isActiveOverride={
          pathname === viewPath && activeViewByContainer[containerKey] === view.id
        }
        onNavigate={() => setActiveView(containerKey, view.id)}
      />
    );
  }

  if (isIssue) {
    if (issueQuery.isPending) return <PinSkeleton />;
    if (issueQuery.isError || !issueQuery.data) return null;
    const issue = issueQuery.data;
    const label = issue.title;
    const iconNode = (
      /* 覆盖父级的 [&_svg]:size-4——置顶项需要更小的图标来匹配 sm 尺寸 */
      <StatusIcon
        status={issue.status}
        category={issueStatusCategory(issue) ?? undefined}
        className="!size-3.5 shrink-0"
      />
    );
    return (
      <SortablePinItem
        pin={pin}
        href={href}
        pathname={pathname}
        onUnpin={onUnpin}
        label={label}
        iconNode={iconNode}
      />
    );
  }

  if (projectQuery.isPending) return <PinSkeleton />;
  if (projectQuery.isError || !projectQuery.data) return null;
  const project = projectQuery.data;
  const iconNode = <ProjectIcon project={project} size="sm" />;
  return (
    <SortablePinItem
      pin={pin}
      href={href}
      pathname={pathname}
      onUnpin={onUnpin}
      label={project.title}
      iconNode={iconNode}
    />
  );
}

function PinSkeleton() {
  return (
    <SidebarMenuItem>
      <div className="flex h-7 w-full items-center gap-2 px-2">
        <div className="size-3.5 shrink-0 rounded-sm bg-sidebar-accent/40" />
        <div className="h-3 w-24 rounded bg-sidebar-accent/40" />
      </div>
    </SidebarMenuItem>
  );
}

/** 应用侧边栏的属性。 */
interface AppSidebarProps {
  /** 当宿主提供窗口 chrome 间距时，渲染在 SidebarHeader 上方的内容 */
  topSlot?: React.ReactNode;
  /** 渲染在头部工作区切换器和新建 issue 按钮之间（如搜索触发器） */
  searchSlot?: React.ReactNode;
  /** SidebarHeader 的附加 className */
  headerClassName?: string;
  /** SidebarHeader 的附加样式 */
  headerStyle?: React.CSSProperties;
}

/**
 * 应用主侧边栏组件。
 * @param topSlot 侧边栏顶部插槽（窗口 chrome 间距）。
 * @param searchSlot 头部搜索触发器插槽。
 * @param headerClassName 头部附加类名。
 * @param headerStyle 头部附加样式。
 */
export function AppSidebar({ topSlot, searchSlot, headerClassName, headerStyle }: AppSidebarProps = {}) {
  const { t } = useT("layout");
  const { pathname, push } = useNavigation();
  const user = useAuthStore((s) => s.user);
  const userId = useAuthStore((s) => s.user?.id);
  const logout = useLogout();
  const workspace = useCurrentWorkspace();
  const p = useWorkspacePaths();
  const { data: workspaces = EMPTY_WORKSPACES } = useQuery(workspaceListOptions());
  const { data: myInvitations = EMPTY_INVITATIONS } = useQuery(myInvitationListOptions());
  const workspaceCreationDisabled = useConfigStore((s) => s.workspaceCreationDisabled);

  // 手机上侧边栏是盖住页面的 Sheet，所以导航出去时必须把它关闭——否则目标
  // 会渲染在底下，点击看起来像「什么都没发生」。按 `pathname` 关闭（而不是
  // 每个链接的 onClick）能一次性覆盖所有离开这里的路径：导航组、置顶项、
  // 工作区切换器的编程式 push，以及以后加进来的任何东西。`setOpenMobile`
  // 在桌面端是空操作，那里 Sheet 根本不是侧边栏的渲染形态。
  const { setOpenMobile } = useSidebar();
  useEffect(() => {
    setOpenMobile(false);
  }, [pathname, setOpenMobile]);

  const wsId = workspace?.id;
  const unreadCount = useInboxUnreadCount(wsId);
  // 跨工作区未读汇总支撑工作区切换器上的红点。所有工作区共用一个缓存条目；
  // 以「存在活动工作区」为门控，因为该端点要经过工作区成员中间件解析。
  const { data: unreadSummary = EMPTY_INBOX_SUMMARY } = useQuery({
    ...inboxUnreadSummaryOptions(),
    enabled: inboxUnreadSummaryEnabled(wsId),
  });
  const otherWorkspaceUnread = React.useMemo(
    () => hasOtherWorkspaceUnread(unreadSummary, wsId),
    [unreadSummary, wsId],
  );
  // 记录哪些工作区有未读，让切换器下拉能指向具体的那（几）个，
  // 而不只是头像上的汇总红点。
  const unreadWsIds = React.useMemo(() => unreadWorkspaceIds(unreadSummary), [unreadSummary]);
  const { data: pinnedItems = EMPTY_PINS } = useQuery({
    ...pinListOptions(wsId ?? "", userId ?? ""),
    enabled: !!wsId && !!userId,
  });
  const deletePin = useDeletePin();
  const reorderPins = useReorderPins();
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 5 } }));
  const sidebarScrollRef = useRef<HTMLDivElement>(null);
  const sidebarFadeStyle = useScrollFade(sidebarScrollRef, 24);
  const getPinHref = useCallback(
    (pin: PinnedItem) =>
      pin.item_type === "issue"
        ? p.issueDetail(pin.item_id)
        : pin.item_type === "project"
          ? p.projectDetail(pin.item_id)
          // view 只有在详情加载后才知道目标——由行自身解析 href；这个占位符
          // 永远不会作为链接渲染。
          : "",
    [p],
  );

  // pinnedItems 的本地展示副本，用于保证放置动画稳定。静止时跟随 TQ；
  // 拖拽手势期间冻结，这样拖拽中途的缓存写入（我们自己的乐观更新，或
  // WS 重取）不会在 dnd-kit 的放置动画还在插值时重排它下面的 DOM。
  const [localPinned, setLocalPinned] = useState<PinnedItem[]>(pinnedItems);
  const [localPinnedWsId, setLocalPinnedWsId] = useState<string | null>(wsId ?? null);
  const isDraggingRef = useRef(false);
  useEffect(() => {
    if (!isDraggingRef.current) {
      setLocalPinned(pinnedItems);
    }
  }, [pinnedItems]);
  useEffect(() => {
    setLocalPinnedWsId(wsId ?? null);
  }, [wsId]);
  const visiblePinned = localPinnedWsId === (wsId ?? null) ? localPinned : EMPTY_PINS;
  // view 置顶不在这里（它们的 href 是异步解析的）：某个 view 置顶激活时，
  // 它所在界面的普通导航行也会保持高亮。这一点接受——要压制它得把每个
  // view 的详情都提到这里来。
  const isActivePinnedRoute = visiblePinned.some((pin) => pathname === getPinHref(pin));

  const handleDragStart = useCallback(() => {
    isDraggingRef.current = true;
  }, []);
  const handleDragEnd = useCallback(
    (event: DragEndEvent) => {
      isDraggingRef.current = false;
      const { active, over } = event;
      if (!over || active.id === over.id) return;
      const oldIndex = localPinned.findIndex((p) => p.id === active.id);
      const newIndex = localPinned.findIndex((p) => p.id === over.id);
      if (oldIndex === -1 || newIndex === -1) return;
      const reordered = arrayMove(localPinned, oldIndex, newIndex);
      setLocalPinned(reordered);
      reorderPins.mutate(reordered);
    },
    [localPinned, reorderPins],
  );

  const queryClient = useQueryClient();
  const acceptInvitationMut = useMutation({
    mutationFn: (id: string) => api.acceptInvitation(id),
    // 接受邀请后，导航进入刚加入的工作区。否则用户停留在当前工作区，只是
    // 看到下拉里多了个新的——安静又令人困惑（这是 MUL-820）。
    onSuccess: async (_, invitationId) => {
      const invitation = myInvitations.find((i) => i.id === invitationId);
      queryClient.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
      // staleTime: 0 强制真正走一次网络请求——得先把加入的工作区放进列表，
      // 才能解析它的 slug 用于导航。
      const list = await queryClient.fetchQuery({
        ...workspaceListOptions(),
        staleTime: 0,
      });
      const joined = invitation
        ? list.find((w) => w.id === invitation.workspace_id)
        : null;
      if (joined) {
        push(paths.workspace(joined.slug).issues());
      }
    },
    onError: () => {
      // "invitation is not pending" means the invite was concluded from
      // another surface while this row was on screen. Refetch so the stale
      // row drops instead of sticking around until restart — a silent
      // failure here reads as "the button does nothing".
      queryClient.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
    },
  });
  const declineInvitationMut = useMutation({
    mutationFn: (id: string) => api.declineInvitation(id),
    // Either outcome must refresh the list: success drops the declined row,
    // and a failure ("invitation is not pending") means the invite was
    // concluded from another surface — refetch drops the stale row.
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
    },
  });

  const createIssueShortcut = useShortcut("createIssue");

  return (
      <Sidebar variant="inset">
        {topSlot}
        {/* Workspace Switcher */}
        <SidebarHeader className={cn("py-3", headerClassName)} style={headerStyle}>
          <EnvironmentBadge />
          <SidebarMenu>
            <SidebarMenuItem>
              <DropdownMenu>
                <DropdownMenuTrigger
                  render={
                    <SidebarMenuButton>
                      <span className="relative">
                        <WorkspaceAvatar name={workspace?.name ?? "M"} avatarUrl={workspace?.avatar_url} size="sm" />
                        {/* 共享的品牌红点：有待处理邀请，或另一个工作区有未读
                            收件箱。当前工作区自己的未读留在下方 Inbox 导航计数
                            上，所以这里有意把它排除掉。 */}
                        {(myInvitations.length > 0 || otherWorkspaceUnread) && (
                          <span className="absolute -top-0.5 -right-0.5 size-2 rounded-full bg-brand ring-1 ring-sidebar" />
                        )}
                      </span>
                      <span className="flex-1 truncate font-medium">
                        {workspace?.name ?? "Multica"}
                      </span>
                      <ChevronDown className="size-3 text-muted-foreground" />
                    </SidebarMenuButton>
                  }
                />
                <DropdownMenuContent
                  className="w-auto min-w-56"
                  align="start"
                  side="bottom"
                  sideOffset={4}
                >
                  <div className="flex items-center gap-2.5 px-2 py-1.5">
                    <ActorAvatar
                      name={user?.name ?? ""}
                      initials={(user?.name ?? "U").charAt(0).toUpperCase()}
                      avatarUrl={resolvePublicFileUrl(user?.avatar_url)}
                      size="lg"
                    />
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-body font-medium leading-tight">
                        {user?.name}
                      </p>
                      <p className="truncate text-caption text-muted-foreground leading-tight">
                        {user?.email}
                      </p>
                    </div>
                  </div>
                  <DropdownMenuSeparator />
                  <DropdownMenuGroup>
                    <DropdownMenuLabel className="text-caption text-muted-foreground">
                      {t(($) => $.sidebar.workspaces_label)}
                    </DropdownMenuLabel>
                    {workspaces.map((ws) => (
                      <DropdownMenuItem
                        key={ws.id}
                        render={
                          <AppLink href={paths.workspace(ws.slug).issues()} />
                        }
                      >
                        <WorkspaceAvatar name={ws.name} avatarUrl={ws.avatar_url} size="sm" />
                        <span className="flex-1 truncate">{ws.name}</span>
                        {/* 指向具体持有未读收件箱项的工作区。与当前工作区的
                            对勾占据同一右侧边缘槽位；当前工作区被排除（它的未读
                            就是 Inbox 导航计数），所以红点和对勾永远不会在同一行
                            相撞。 */}
                        {ws.id !== workspace?.id && unreadWsIds.has(ws.id) && (
                          <span className="size-2 rounded-full bg-brand" />
                        )}
                        {ws.id === workspace?.id && (
                          <Check className="h-3.5 w-3.5 text-primary" />
                        )}
                      </DropdownMenuItem>
                    ))}
                    {!workspaceCreationDisabled && (
                      <DropdownMenuItem
                        onClick={() => push(paths.newWorkspace())}
                      >
                        <Plus className="h-3.5 w-3.5" />
                        {t(($) => $.sidebar.create_workspace)}
                      </DropdownMenuItem>
                    )}
                  </DropdownMenuGroup>
                  {myInvitations.length > 0 && (
                    <>
                      <DropdownMenuSeparator />
                      <DropdownMenuGroup>
                        <DropdownMenuLabel className="text-caption text-muted-foreground">
                          {t(($) => $.sidebar.pending_invitations_label)}
                        </DropdownMenuLabel>
                        {myInvitations.map((inv) => (
                          <div key={inv.id} className="flex items-center gap-2 px-2 py-1.5">
                            <WorkspaceAvatar name={inv.workspace_name ?? "W"} size="sm" />
                            <span className="flex-1 truncate text-body">{inv.workspace_name ?? t(($) => $.sidebar.invitation_workspace_fallback)}</span>
                            <button
                              type="button"
                              className="text-caption px-2 py-0.5 rounded bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
                              disabled={acceptInvitationMut.isPending}
                              onClick={(e) => {
                                e.stopPropagation();
                                acceptInvitationMut.mutate(inv.id);
                              }}
                            >
                              {t(($) => $.sidebar.invitation_join)}
                            </button>
                            <button
                              type="button"
                              className="text-caption px-2 py-0.5 rounded bg-muted text-muted-foreground hover:bg-muted/80 disabled:opacity-50"
                              disabled={declineInvitationMut.isPending}
                              onClick={(e) => {
                                e.stopPropagation();
                                declineInvitationMut.mutate(inv.id);
                              }}
                            >
                              {t(($) => $.sidebar.invitation_decline)}
                            </button>
                          </div>
                        ))}
                      </DropdownMenuGroup>
                    </>
                  )}
                  <DropdownMenuSeparator />
                  <DropdownMenuGroup>
                    <DropdownMenuItem variant="destructive" onClick={logout}>
                      <LogOut className="h-3.5 w-3.5" />
                      {t(($) => $.sidebar.log_out)}
                    </DropdownMenuItem>
                  </DropdownMenuGroup>
                </DropdownMenuContent>
              </DropdownMenu>
            </SidebarMenuItem>
          </SidebarMenu>
          <SidebarMenu>
            {searchSlot && (
              <SidebarMenuItem>
                {searchSlot}
              </SidebarMenuItem>
            )}
            <SidebarMenuItem>
              <SidebarMenuButton
                className="text-muted-foreground"
                onClick={() => openCreateIssueWithPreference()}
              >
                <span className="relative">
                  <SquarePen />
                  <DraftDot />
                </span>
                <span>{t(($) => $.sidebar.new_issue)}</span>
                {createIssueShortcut ? (
                  <ShortcutKeycaps shortcut={createIssueShortcut} decorative className="pointer-events-none ml-auto" />
                ) : null}
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>

        {/* Navigation */}
        <SidebarContent ref={sidebarScrollRef} style={sidebarFadeStyle}>
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu className="gap-0.5">
                {personalNav.filter((item) => isWorkspaceMenuVisible(workspace?.settings, item.key)).map((item) => {
                  const href = p[item.key]();
                  const Icon = routeIconForPath(href);
                  const isActive = isNavActive(pathname, href);
                  return (
                    <SidebarMenuItem key={item.key}>
                      <SidebarMenuButton
                        isActive={isActive}
                        render={<AppLink href={href} />}
                        className="text-muted-foreground hover:not-data-active:bg-sidebar-accent/70 data-active:bg-sidebar-accent data-active:text-sidebar-accent-foreground"
                      >
                        <Icon />
                        <span>{t(($) => $.nav[item.labelKey])}</span>
                        {item.key === "inbox" && unreadCount > 0 && (
                          <CappedNumberFlow
                            value={unreadCount}
                            animated={false}
                            className="ml-auto text-caption"
                          />
                        )}
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  );
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          {visiblePinned.length > 0 && (
            <Collapsible defaultOpen>
              <SidebarGroup className="group/pinned">
                <SidebarGroupLabel
                  render={<CollapsibleTrigger />}
                  className="group/trigger cursor-pointer hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground"
                >
                  <span>{t(($) => $.sidebar.pinned_label)}</span>
                  <ChevronRight className="!size-3 ml-1 stroke-[2.5] transition-transform duration-200 group-data-[panel-open]/trigger:rotate-90" />
                  <span className="ml-auto text-micro text-muted-foreground opacity-0 transition-opacity group-hover/pinned:opacity-100">{visiblePinned.length}</span>
                </SidebarGroupLabel>
                <CollapsibleContent>
                  <SidebarGroupContent>
                    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragStart={handleDragStart} onDragEnd={handleDragEnd}>
                      <SortableContext items={visiblePinned.map((p) => p.id)} strategy={verticalListSortingStrategy}>
                        <SidebarMenu className="gap-0.5">
                          {visiblePinned.map((pin: PinnedItem) => (
                            <PinRow
                              key={pin.id}
                              pin={pin}
                              href={getPinHref(pin)}
                              pathname={pathname}
                              onUnpin={() => deletePin.mutate({ itemType: pin.item_type, itemId: pin.item_id })}
                              wsId={wsId ?? ""}
                            />
                          ))}
                        </SidebarMenu>
                      </SortableContext>
                    </DndContext>
                  </SidebarGroupContent>
                </CollapsibleContent>
              </SidebarGroup>
            </Collapsible>
          )}

          <SidebarGroup>
            <SidebarGroupLabel>{t(($) => $.sidebar.workspace_group)}</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu className="gap-0.5">
                <SidebarMenuItem>
                  <SidebarMenuButton render={<AppLink href={p.chat()} />} isActive={isNavActive(pathname, p.chat())}>
                    <MessageSquare aria-hidden="true" /><span>{t(($) => $.nav.chat)}</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
                {workspaceNav.filter((item) => isWorkspaceMenuVisible(workspace?.settings, item.key)).map((item) => {
                  const href = p[item.key]();
                  const Icon = routeIconForPath(href);
                  const isActive = !isActivePinnedRoute && isNavActive(pathname, href);
                  return (
                    <SidebarMenuItem key={item.key}>
                      <SidebarMenuButton
                        isActive={isActive}
                        render={<AppLink href={href} />}
                        className="text-muted-foreground hover:not-data-active:bg-sidebar-accent/70 data-active:bg-sidebar-accent data-active:text-sidebar-accent-foreground"
                      >
                        <Icon />
                        <span>{t(($) => $.nav[item.labelKey])}</span>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  );
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          <SidebarGroup>
            <SidebarGroupLabel>{t(($) => $.sidebar.configure_group)}</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu className="gap-0.5">
                {configureNav.filter((item) => isWorkspaceMenuVisible(workspace?.settings, item.key)).map((item) => {
                  const href = p[item.key]();
                  const Icon = routeIconForPath(href);
                  const isActive = isNavActive(pathname, href);
                  return (
                    <SidebarMenuItem key={item.key}>
                      <SidebarMenuButton
                        isActive={isActive}
                        render={<AppLink href={href} />}
                        className="text-muted-foreground hover:not-data-active:bg-sidebar-accent/70 data-active:bg-sidebar-accent data-active:text-sidebar-accent-foreground"
                      >
                        <Icon />
                        <span>{t(($) => $.nav[item.labelKey])}</span>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  );
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          <SidebarGroup>
            <SidebarGroupLabel>{t(($) => $.nav.living)}</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu className="gap-0.5">
                {livingNav.filter((key) => isWorkspaceMenuVisible(workspace?.settings, key)).map((key) => {
                  const href = p[key]();
                  const Icon = routeIconForPath(href);
                  const isActive = isNavActive(pathname, href) ||
                    (key === "meals" && isNavActive(pathname, p.cooking()));
                  return (
                    <SidebarMenuItem key={key}>
                      <SidebarMenuButton isActive={isActive} render={<AppLink href={href} />}>
                        <Icon />
                        <span>{t(($) => $.nav[key])}</span>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  );
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          <SidebarGroup>
            <SidebarGroupLabel>{t(($) => $.nav.life)}</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu className="gap-0.5">
                {lifeNav.filter(() => isWorkspaceMenuVisible(workspace?.settings, "life")).map((item) => {
                  const href = p.lifeTab(item.tab);
                  const Icon = routeIconForPath(href);
                  const isActive = pathname.startsWith(p.life());
                  return (
                    <SidebarMenuItem key={item.tab}>
                      <SidebarMenuButton isActive={isActive && new URLSearchParams(typeof window === "undefined" ? "" : window.location.search).get("tab") === item.tab} render={<AppLink href={href} />} className="text-muted-foreground hover:not-data-active:bg-sidebar-accent/70 data-active:bg-sidebar-accent data-active:text-sidebar-accent-foreground">
                        <Icon /><span>{t(($) => $.nav[item.labelKey])}</span>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  );
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>

        <SidebarRail />
      </Sidebar>
  );
}
