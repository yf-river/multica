"use client";

// 本文件：视图栏（ViewBar）的下拉面板及删除确认。
// ViewBar 在窄屏/溢出时把放不下的页签收进"更多"列表：这里提供那个可拖拽排序的
// 面板 ViewListPanel（拖序即调整栏上可见哪些视图）、每行的行内操作菜单，
// 以及各入口共用的"删除视图"二次确认对话框 DeleteViewConfirm。

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
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { restrictToVerticalAxis } from "@dnd-kit/modifiers";
import {
  EyeOff,
  GripVertical,
  MoreHorizontal,
  Pencil,
  Pin,
  PinOff,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { cn } from "@multica/ui/lib/utils";
import type { IssueView } from "@multica/core/api/schemas";
import { useT } from "../../i18n";

/** 栏上的一个条目：内置页签或一个已保存视图。 */
export interface ViewBarItem {
  barItemId: string;
  label: string;
  kind: "builtin" | "view";
  view?: IssueView;
  canManage?: boolean;
}

/** 纵向锁定的行拖拽在指针下没有 DragOverlay 可供抓取，因此在拖拽期间把
 *  "抓取"光标提升到整个文档（见 ui/styles/base.css 里的
 *  `data-dnd-dragging` 约定）。 */
function setDndCursor(on: boolean) {
  if (on) document.documentElement.dataset.dndDragging = "true";
  else delete document.documentElement.dataset.dndDragging;
}

/**
 * 唯一的"删除视图"确认框，由管理对话框、栏上右键菜单与面板行三处共用，
 * 保证任何入口的文案一致、并共同遵守"只删视图、绝不删问题"的约定。
 */
export function DeleteViewConfirm({
  view,
  onOpenChange,
  onConfirm,
}: {
  view: IssueView | null;
  onOpenChange: (open: boolean) => void;
  onConfirm: (view: IssueView) => Promise<void>;
}) {
  const { t } = useT("issues");
  return (
    <AlertDialog open={!!view} onOpenChange={(v) => !v && onOpenChange(false)}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(($) => $.view_bar.delete_title)}</AlertDialogTitle>
          <AlertDialogDescription className="break-words">
            {t(($) => $.view_bar.delete_description, { name: view?.name ?? "" })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t(($) => $.save_view.cancel)}</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => {
              const target = view;
              onOpenChange(false);
              if (!target) return;
              void onConfirm(target).then(
                () => toast.success(t(($) => $.view_bar.toast_deleted)),
                () => toast.error(t(($) => $.save_view.toast_failed)),
              );
            }}
          >
            {t(($) => $.view_bar.delete)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function SortablePanelRow({
  item,
  active,
  pinned,
  onSelect,
  onEdit,
  onDelete,
  onTogglePin,
  onHide,
}: {
  item: ViewBarItem;
  active: boolean;
  pinned: boolean;
  onSelect: () => void;
  onEdit?: () => void;
  onDelete?: () => void;
  onTogglePin?: () => void;
  onHide?: () => void;
}) {
  const { t } = useT("issues");
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } =
    useSortable({ id: item.barItemId });
  const isView = item.kind === "view";

  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      className={cn(
        "group/view-row flex items-center gap-1.5 rounded-md px-1.5 py-1",
        isDragging ? "z-10 bg-accent opacity-80" : "hover:bg-muted/60",
      )}
    >
      <button
        type="button"
        {...attributes}
        {...listeners}
        aria-label={t(($) => $.view_bar.drag_handle)}
        className={cn(
          "cursor-grab active:cursor-grabbing text-faint-foreground hover:text-muted-foreground",
          isDragging && "cursor-grabbing",
        )}
      >
        <GripVertical className="size-3.5" />
      </button>
      <button
        type="button"
        onClick={onSelect}
        className={cn(
          "flex min-w-0 flex-1 items-center gap-1.5 text-left text-body",
          active && "font-medium",
        )}
      >
        <span className="truncate">{item.label}</span>
        {item.kind === "builtin" && (
          <span className="shrink-0 text-caption text-muted-foreground">
            {t(($) => $.view_bar.builtin_tag)}
          </span>
        )}
      </button>
      {isView && item.view && (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t(($) => $.view_bar.row_menu)}
                className="text-muted-foreground"
              >
                <MoreHorizontal className="size-3.5" />
              </Button>
            }
          />
          <DropdownMenuContent align="end" className="w-44">
            {/* 与页签右键菜单同一套权限呈现：无权限时"编辑"置灰、删除直接隐藏。 */}
            <DropdownMenuItem disabled={!item.canManage} onClick={onEdit}>
              <Pencil className="size-3.5" />
              {t(($) => $.view_bar.context_edit)}
            </DropdownMenuItem>
            {item.canManage && (
              <DropdownMenuItem variant="destructive" onClick={onDelete}>
                <Trash2 className="size-3.5" />
                {t(($) => $.view_bar.delete)}
              </DropdownMenuItem>
            )}
            <DropdownMenuItem onClick={onTogglePin}>
              {pinned ? <PinOff className="size-3.5" /> : <Pin className="size-3.5" />}
              {pinned
                ? t(($) => $.view_bar.context_unpin)
                : t(($) => $.view_bar.context_pin)}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={onHide}>
              <EyeOff className="size-3.5" />
              {t(($) => $.view_bar.row_hide)}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </div>
  );
}

/**
 * 下拉列表：栏上的全部页签，按栏上顺序排列，可拖拽重排——它与栏上的内联
 * 拖拽写的是同一份偏好。因为栏只展示"能放得下"的最长前缀，这里的重排
 * 就是控制哪些视图留在栏上的手段：往上拖让它上栏，往下拖收起来。
 * 分隔线标出当前折叠位置。
 */
export function ViewListPanel({
  items,
  fitCount,
  activeViewId,
  pinnedViewIds,
  onMoveItem,
  onSelectItem,
  onEditView,
  onDeleteView,
  onTogglePin,
  onHideItem,
}: {
  /** 按顺序排列的全部候选条目（被隐藏的已排除，与栏上一致）。 */
  items: ViewBarItem[];
  /** 当前栏上能放下的数量——即折叠线位置。 */
  fitCount: number;
  activeViewId: string | null;
  pinnedViewIds: ReadonlySet<string>;
  /** 重排请求：把 `activeId` 那个条目移到 `overId` 旁边。 */
  onMoveItem: (activeId: string, overId: string) => void;
  onSelectItem: (item: ViewBarItem) => void;
  onEditView: (view: IssueView) => void;
  onDeleteView: (view: IssueView) => void;
  onTogglePin: (view: IssueView, pinned: boolean) => void;
  onHideItem: (barItemId: string) => void;
}) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
  );

  const handleDragEnd = (event: DragEndEvent) => {
    setDndCursor(false);
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    onMoveItem(String(active.id), String(over.id));
  };

  return (
    <div className="max-h-96 overflow-y-auto p-1">
      <DndContext
        sensors={sensors}
        collisionDetection={closestCenter}
        modifiers={[restrictToVerticalAxis]}
        autoScroll={{ threshold: { x: 0, y: 0.15 } }}
        onDragStart={() => setDndCursor(true)}
        onDragCancel={() => setDndCursor(false)}
        onDragEnd={handleDragEnd}
      >
        <SortableContext
          items={items.map((item) => item.barItemId)}
          strategy={verticalListSortingStrategy}
        >
          {items.map((item, index) => (
            <div key={item.barItemId}>
              {/* 折叠线：它之上的条目此刻就在栏上。 */}
              {index === fitCount && index > 0 && (
                <div className="mx-1.5 my-1 border-t border-border" />
              )}
              <SortablePanelRow
                item={item}
                active={
                  item.kind === "view" && item.view
                    ? item.view.id === activeViewId
                    : false
                }
                pinned={!!item.view && pinnedViewIds.has(item.view.id)}
                onSelect={() => onSelectItem(item)}
                onEdit={item.view ? () => onEditView(item.view!) : undefined}
                onDelete={item.view ? () => onDeleteView(item.view!) : undefined}
                onTogglePin={
                  item.view
                    ? () => onTogglePin(item.view!, pinnedViewIds.has(item.view!.id))
                    : undefined
                }
                onHide={() => onHideItem(item.barItemId)}
              />
            </div>
          ))}
        </SortableContext>
      </DndContext>
    </div>
  );
}
