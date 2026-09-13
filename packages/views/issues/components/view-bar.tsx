"use client";

// 本文件：ViewBar —— 视图栏。
// 把内置页签与已保存视图拼成一行扁平、可按用户顺序横向拖拽重排的页签行。
// 放不下的条目折叠进"更多"下拉（ViewListPanel，可拖序/置顶/隐藏/编辑/删除）；
// 当被打开的那个视图溢出时，它会把触发器顶替为自己的名字，保证"正在看的视图
// 永远可见"。栏尾 [⧉] 菜单提供"新建视图/管理视图"；删除确认与拖拽重排分别
// 走 DeleteViewConfirm 与 ManageViewsDialog。行宽用隐藏镜像逐帧测量（useSingleRowFit）。

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  DndContext,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  horizontalListSortingStrategy,
  useSortable,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { restrictToHorizontalAxis } from "@dnd-kit/modifiers";
import {
  ChevronDown,
  Layers,
  MoreHorizontal,
  Pencil,
  Pin,
  PinOff,
  Plus,
  Settings2,
  Trash2,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from "@multica/ui/components/ui/context-menu";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import type { IssueView } from "@multica/core/api/schemas";
import {
  canManageIssueView,
  type IssueViewScope,
} from "@multica/core/issue-views/queries";
import {
  applyViewBarPrefs,
  issueViewPreferenceOptions,
  useUpdateIssueViewPreference,
  EMPTY_VIEW_BAR_PREFS,
} from "@multica/core/issue-views/preferences";
import { useDeleteIssueView } from "@multica/core/issue-views/mutations";
import { useAuthStore } from "@multica/core/auth";
import { memberListOptions } from "@multica/core/workspace/queries";
import { pinListOptions } from "@multica/core/pins/queries";
import { useCreatePin, useDeletePin } from "@multica/core/pins/mutations";
import { useSingleRowFit } from "../../common/single-row-fit";
import { ManageViewsDialog } from "./manage-views-dialog";
import {
  DeleteViewConfirm,
  ViewListPanel,
  type ViewBarItem,
} from "./view-bar-popover";
import { useT } from "../../i18n";

/** 内置页签的宿主侧描述（如"全部/成员/代理"这类固定页签）。 */
export interface ViewBarBuiltin {
  key: string;
  label: string;
  description?: string;
  active: boolean;
  onSelect: () => void;
}

/** 每个页签共享的最大宽度：能放下短名称、超长的截断。 */
const TAB_MAX_W = "max-w-40";
/** 页签旁边按实际渲染内容预留的像素：行尾的 [⧉] 菜单常驻；"更多"触发器只在
 *  有溢出时出现；被顶替的活动页签是最宽的形态。 */
const RESERVE_MENU = 36;
const RESERVE_MORE = RESERVE_MENU + 32;
const RESERVE_PROMOTED = RESERVE_MENU + 200;

/** 一个栏上页签：可原位拖拽；并吞掉一次拖拽结束时落到它身上的点击。 */
function SortableBarTab({
  id,
  children,
}: {
  id: string;
  children: React.ReactNode;
}) {
  const { listeners, setNodeRef, transform, transition, isDragging } =
    useSortable({ id });
  const wasDragged = useRef(false);
  useEffect(() => {
    if (isDragging) {
      wasDragged.current = true;
      return;
    }
    if (!wasDragged.current) return;
    // 松手会以一次 click 收尾（在下方被吞掉）；被取消的拖拽（按 Escape）不会
    // 产生 click——这里推迟一个 tick 再清标志，避免下一次真正的点击被一次
    // 从未落下的拖拽误吞。
    const timer = window.setTimeout(() => {
      wasDragged.current = false;
    }, 0);
    return () => window.clearTimeout(timer);
  }, [isDragging]);
  return (
    // 只挂 listeners——dnd-kit 的 aria 属性会让这个 span 变成第二个可聚焦的
    // role=button，反而包住了真正的页签按钮。
    <span
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      className={cn("inline-flex", isDragging && "z-10 opacity-70")}
      {...listeners}
      onClickCapture={(event) => {
        if (wasDragged.current) {
          wasDragged.current = false;
          event.preventDefault();
          event.stopPropagation();
        }
      }}
    >
      {children}
    </span>
  );
}

/** 页签通用外观——真实行与测量镜像共用，保证两者宽度完全一致。 */
function BarTabButton({
  label,
  active,
  onClick,
}: {
  label: string;
  active?: boolean;
  onClick?: () => void;
}) {
  return (
    <Button
      variant="outline"
      size="sm"
      className={cn(
        TAB_MAX_W,
        active
          ? "bg-accent text-accent-foreground hover:bg-accent/80"
          : "text-muted-foreground",
      )}
      onClick={onClick}
      tabIndex={onClick ? undefined : -1}
    >
      <span className="truncate">{label}</span>
    </Button>
  );
}

/**
 * 视图栏：内置页签与已保存视图拼成一行扁平、按用户排序、可横向拖拽的页签行。
 * 放得下的全部以页签呈现（可拖拽重排）；放不下的收进一个"更多"弹层，
 * 该弹层仅在确有溢出时才出现。当前打开的视图永远可见——它溢出时会把
 * "更多"触发器顶替成自己的名字。行尾的 [⧉] 菜单（新建视图 / 管理视图）不变。
 */
export function ViewBar({
  wsId,
  scope,
  builtins,
  views,
  viewsReady,
  activeView,
  onSelectView,
  onNewView,
  onEditView,
}: {
  wsId: string;
  scope: IssueViewScope;
  builtins: ViewBarBuiltin[];
  views: IssueView[];
  /** 视图列表真正加载完成后才会置 true——见 savePrefs 的说明。 */
  viewsReady: boolean;
  activeView: IssueView | null;
  onSelectView: (view: IssueView | null) => void;
  onNewView: () => void;
  /** 打开以视图自身定义为种子的编辑对话框。 */
  onEditView: (view: IssueView) => void;
}) {
  const { t } = useT("issues");
  const currentUserId = useAuthStore((s) => s.user?.id ?? null);
  const { data: preference } = useQuery(issueViewPreferenceOptions(wsId, scope));
  const prefs = preference?.prefs ?? EMPTY_VIEW_BAR_PREFS;
  const updatePreference = useUpdateIssueViewPreference(wsId, scope);
  const deleteView = useDeleteIssueView(wsId);
  const [manageOpen, setManageOpen] = useState(false);
  const [moreOpen, setMoreOpen] = useState(false);
  const [deleting, setDeleting] = useState<IssueView | null>(null);

  // 与管理规则一致：所有者，或共享视图的工作区 owner/admin 才能管理。
  // 成员用一条查询从缓存加载。
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const myRole = useMemo(
    () => members.find((m) => m.user_id === currentUserId)?.role ?? null,
    [members, currentUserId],
  );

  const { data: pins = [] } = useQuery({
    ...pinListOptions(wsId, currentUserId ?? ""),
    enabled: !!currentUserId,
  });
  const pinnedViewIds = useMemo(
    () =>
      new Set(
        pins.filter((p) => p.item_type === "view").map((p) => p.item_id),
      ),
    [pins],
  );
  const createPin = useCreatePin();
  const deletePin = useDeletePin();

  const anchorId = builtins.length > 0 ? `builtin:${builtins[0]!.key}` : "";

  const items = useMemo<ViewBarItem[]>(
    () => [
      ...builtins.map((b) => ({
        barItemId: `builtin:${b.key}`,
        label: b.label,
        kind: "builtin" as const,
      })),
      ...views.map((view) => ({
        barItemId: `view:${view.id}`,
        label: view.name,
        kind: "view" as const,
        view,
        canManage: canManageIssueView(view, currentUserId, myRole),
      })),
    ],
    [builtins, views, currentUserId, myRole],
  );

  // 刚刚落下的顺序的同步镜像。偏好 mutation 是乐观的，但它的 onMutate 是异步的——
  // 没有这个镜像，落下的页签会在缓存生效前、先在自己的旧位置渲染一帧，看起来像
  // 弹了一下。偏好数据追上来后立即清空镜像。
  const [localOrder, setLocalOrder] = useState<string[] | null>(null);
  useEffect(() => {
    setLocalOrder(null);
  }, [preference]);
  const displayPrefs = useMemo(
    () => (localOrder ? { ...prefs, order: localOrder } : prefs),
    [prefs, localOrder],
  );

  const { visible, hiddenSet, ordered } = useMemo(
    () => applyViewBarPrefs(items, displayPrefs, anchorId),
    [items, displayPrefs, anchorId],
  );

  const builtinByKey = useMemo(
    () => new Map(builtins.map((b) => [`builtin:${b.key}`, b])),
    [builtins],
  );

  // --- 单行容纳度计算 ------------------------------------------------
  // 预留宽度会反哺回容纳度，但它只在有条目被挤出去时才会变大（"更多"触发器出现、
  // 随后被顶替的活动页签又把它加宽），这会让容纳度进一步缩小、把条目继续挡在外面——
  // 收敛过程在两个方向上都单调，不会来回抖动。
  const activeBarId = activeView ? `view:${activeView.id}` : null;
  const [reserveTier, setReserveTier] = useState<"menu" | "more" | "promoted">(
    "menu",
  );
  const { containerRef, measureRef, fitCount } = useSingleRowFit({
    count: visible.length,
    gap: 4,
    reserve:
      reserveTier === "promoted"
        ? RESERVE_PROMOTED
        : reserveTier === "more"
          ? RESERVE_MORE
          : RESERVE_MENU,
  });
  const fitting = visible.slice(0, fitCount);
  const overflowed = visible.slice(fitCount);
  const activeOverflowed =
    !!activeBarId && overflowed.some((item) => item.barItemId === activeBarId);
  useEffect(() => {
    setReserveTier(
      activeOverflowed ? "promoted" : overflowed.length > 0 ? "more" : "menu",
    );
  }, [activeOverflowed, overflowed.length]);
  const promoted = reserveTier === "promoted" && activeOverflowed;

  const savePrefs = (next: { hidden: string[]; order: string[] }) => {
    // 拖拽可能落在视图列表加载完成之前；拿一份进行中的（空）列表去做修剪，
    // 会把"所有保存视图都被删了"误写持久化，抹掉用户的顺序与隐藏集。
    // 这里干脆放弃这次写入——手势稍后即可重做。
    if (!viewsReady) return;
    // 修剪掉已无法解析的 id，避免被删除的视图在偏好里无限累积。
    const known = new Set(items.map((item) => item.barItemId));
    updatePreference.mutate({
      hidden: next.hidden.filter((id) => known.has(id)),
      order: next.order.filter((id) => known.has(id)),
    });
  };

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
  );
  // 立即应用这次移动（镜像）并持久化——两条拖拽通路都经过这里，
  // 让落下与视觉更新发生在同一帧。
  const applyMove = (activeId: string, overId: string) => {
    const ids = ordered.map((item) => item.barItemId);
    const from = ids.indexOf(activeId);
    const to = ids.indexOf(overId);
    if (from < 0 || to < 0) return;
    const next = arrayMove(ids, from, to);
    if (viewsReady) setLocalOrder(next);
    savePrefs({ hidden: [...hiddenSet], order: next });
  };

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    applyMove(String(active.id), String(over.id));
  };

  const confirmDelete = async (view: IssueView) => {
    await deleteView.mutateAsync(view.id);
    if (activeView?.id === view.id) onSelectView(null);
  };

  const toggleHidden = (barItemId: string, hidden: boolean) => {
    const nextHidden = new Set(hiddenSet);
    if (hidden) nextHidden.add(barItemId);
    else nextHidden.delete(barItemId);
    savePrefs({
      hidden: [...nextHidden],
      order: ordered.map((item) => item.barItemId),
    });
    // 隐藏正在打开的视图等于退出它——一个看不见的活动视图会让 surface
    // 处于没有对应页签的"悬空"状态。
    if (hidden && activeView && barItemId === `view:${activeView.id}`) {
      onSelectView(null);
    }
  };

  const togglePin = (view: IssueView, pinned: boolean) =>
    pinned
      ? deletePin.mutate({ itemType: "view", itemId: view.id })
      : createPin.mutate({ item_type: "view", item_id: view.id });

  const selectItem = (item: ViewBarItem) => {
    if (item.kind === "builtin") {
      builtinByKey.get(item.barItemId)?.onSelect();
    } else if (item.view) {
      onSelectView(item.view);
    }
    setMoreOpen(false);
  };

  const renderViewTab = (item: ViewBarItem) => {
    const view = item.view!;
    const active = activeView?.id === view.id;
    const pinned = pinnedViewIds.has(view.id);
    return (
      <ContextMenu>
        <ContextMenuTrigger
          render={
            <Button
              variant="outline"
              size="sm"
              className={cn(
                TAB_MAX_W,
                active
                  ? "bg-accent text-accent-foreground hover:bg-accent/80"
                  : "text-muted-foreground",
              )}
              onClick={() => onSelectView(active ? null : view)}
            />
          }
        >
          <span className="truncate">{view.name}</span>
        </ContextMenuTrigger>
        <ContextMenuContent className="w-44">
          {/* 无权限时"编辑"保持可见但置灰——那行灰色本身就是在说"你不能"；
              删除则整个隐藏——一条毁灭性的死路只会引出注定失败的确认流程。 */}
          <ContextMenuItem
            disabled={!item.canManage}
            onClick={() => onEditView(view)}
          >
            <Pencil className="size-3.5" />
            {t(($) => $.view_bar.context_edit)}
          </ContextMenuItem>
          {item.canManage && (
            <ContextMenuItem
              variant="destructive"
              onClick={() => setDeleting(view)}
            >
              <Trash2 className="size-3.5" />
              {t(($) => $.view_bar.delete)}
            </ContextMenuItem>
          )}
          <ContextMenuSeparator />
          <ContextMenuItem onClick={() => togglePin(view, pinned)}>
            {pinned ? <PinOff className="size-3.5" /> : <Pin className="size-3.5" />}
            {pinned
              ? t(($) => $.view_bar.context_unpin)
              : t(($) => $.view_bar.context_pin)}
          </ContextMenuItem>
        </ContextMenuContent>
      </ContextMenu>
    );
  };

  return (
    <div
      ref={containerRef}
      className="relative flex min-w-0 flex-1 flex-nowrap items-center gap-1 overflow-hidden"
    >
      {/* 测量镜像：让每个可见候选按自然宽度渲染一次。 */}
      <div
        ref={measureRef}
        aria-hidden
        className="pointer-events-none invisible absolute left-0 top-0 flex flex-nowrap items-center gap-1"
      >
        {visible.map((item) => (
          <span key={item.barItemId} className="inline-flex">
            <BarTabButton label={item.label} />
          </span>
        ))}
      </div>

      <DndContext
        sensors={sensors}
        collisionDetection={closestCenter}
        // 与表格表头列重排同一套手势契约：页签只沿行滑动。
        // 溢出的条目通过管理对话框重排。
        modifiers={[restrictToHorizontalAxis]}
        onDragEnd={handleDragEnd}
      >
        <SortableContext
          items={fitting.map((item) => item.barItemId)}
          strategy={horizontalListSortingStrategy}
        >
          {fitting.map((item) => {
            if (item.kind === "builtin") {
              const b = builtinByKey.get(item.barItemId);
              if (!b) return null;
              const button = (
                <BarTabButton label={b.label} active={b.active} onClick={b.onSelect} />
              );
              return (
                <SortableBarTab key={item.barItemId} id={item.barItemId}>
                  {b.description ? (
                    <Tooltip>
                      <TooltipTrigger render={button} />
                      <TooltipContent side="bottom">{b.description}</TooltipContent>
                    </Tooltip>
                  ) : (
                    button
                  )}
                </SortableBarTab>
              );
            }
            return (
              <SortableBarTab key={item.barItemId} id={item.barItemId}>
                {renderViewTab(item)}
              </SortableBarTab>
            );
          })}
        </SortableContext>
      </DndContext>

      {/* "更多"是溢出指示器：只在确有折叠内容时渲染。面板里重排决定栏上放什么；
          打开的视图溢出时顶替触发器，因此它永远不会消失。没有溢出时，
          重排发生在栏上、管理走 [⧉] 菜单。 */}
      {overflowed.length > 0 && (
      <Popover open={moreOpen} onOpenChange={setMoreOpen}>
        <PopoverTrigger
          render={
            promoted && activeView ? (
              <Button
                variant="outline"
                size="sm"
                className={cn(
                  TAB_MAX_W,
                  "shrink-0 gap-1 bg-accent text-accent-foreground hover:bg-accent/80",
                )}
              >
                <span className="truncate">{activeView.name}</span>
                <ChevronDown className="size-3 shrink-0" />
              </Button>
            ) : (
              <Button
                variant="outline"
                size="sm"
                aria-label={t(($) => $.view_bar.more_label)}
                className="shrink-0 px-1.5 text-muted-foreground"
              >
                <MoreHorizontal className="size-3.5" />
              </Button>
            )
          }
        />
        <PopoverContent align="end" className="w-64 p-0">
          <ViewListPanel
            items={visible}
            fitCount={fitCount}
            activeViewId={activeView?.id ?? null}
            pinnedViewIds={pinnedViewIds}
            onMoveItem={applyMove}
            onSelectItem={selectItem}
            onEditView={(view) => {
              setMoreOpen(false);
              onEditView(view);
            }}
            onDeleteView={(view) => {
              setMoreOpen(false);
              setDeleting(view);
            }}
            onTogglePin={togglePin}
            onHideItem={(barItemId) => {
              setMoreOpen(false);
              toggleHidden(barItemId, true);
            }}
          />
        </PopoverContent>
      </Popover>
      )}

      <DropdownMenu>
        <Tooltip>
          <DropdownMenuTrigger
            render={
              <TooltipTrigger
                render={
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t(($) => $.view_bar.menu_label)}
                    className="shrink-0 text-muted-foreground"
                  >
                    <Layers className="size-3.5" />
                  </Button>
                }
              />
            }
          />
          <TooltipContent side="bottom">{t(($) => $.view_bar.menu_label)}</TooltipContent>
        </Tooltip>
        <DropdownMenuContent align="start" className="w-44">
          <DropdownMenuItem onClick={onNewView}>
            <Plus className="size-3.5" />
            {t(($) => $.view_bar.menu_new)}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => setManageOpen(true)}>
            <Settings2 className="size-3.5" />
            {t(($) => $.view_bar.menu_manage)}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <ManageViewsDialog
        open={manageOpen}
        onOpenChange={setManageOpen}
        items={ordered}
        hiddenSet={hiddenSet}
        anchorId={anchorId}
        onReorder={(orderedIds) => savePrefs({ hidden: [...hiddenSet], order: orderedIds })}
        onToggleHidden={toggleHidden}
        onEditView={(view) => {
          setManageOpen(false);
          onEditView(view);
        }}
        onDeleteView={confirmDelete}
      />

      {/* 右键菜单与溢出行的删除共用同一个确认框。 */}
      <DeleteViewConfirm
        view={deleting}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        onConfirm={confirmDelete}
      />
    </div>
  );
}
