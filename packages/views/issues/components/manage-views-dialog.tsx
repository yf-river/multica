"use client";

// 本文件：ManageViewsDialog —— 视图栏管理对话框。
// 集中管理某个 surface 视图栏上的所有条目：拖拽排序、显隐开关、以及可管理
// 保存视图的编辑/删除。排序与显隐乐观更新"每用户偏好文档"；编辑/删除则委托
// 宿主处理（需要与 surface 状态接线的保存对话框/删除 mutation）。

import { useEffect, useMemo, useState } from "react";
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
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { restrictToVerticalAxis } from "@dnd-kit/modifiers";
import { GripVertical, Layers, Pencil, Trash2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";
import type { IssueView } from "@multica/core/api/schemas";
import {
  DeleteViewConfirm,
  type ViewBarItem,
} from "./view-bar-popover";
import { useT } from "../../i18n";

/** 管理器里的一行：内置页签或一个已保存视图。 */

/** 纵向锁定的行拖拽在指针下没有 DragOverlay 可供抓取，因此在拖拽期间把
 *  "抓取"光标提升到整个文档（见 ui/styles/base.css 里的
 *  `data-dnd-dragging` 约定）。 */
function setDndCursor(on: boolean) {
  if (on) document.documentElement.dataset.dndDragging = "true";
  else delete document.documentElement.dataset.dndDragging;
}

function SortableRow({
  item,
  hidden,
  anchor,
  onToggleHidden,
  onEdit,
  onDelete,
}: {
  item: ViewBarItem;
  hidden: boolean;
  /** 锚点内置页签不可隐藏——它的开关被禁用。 */
  anchor: boolean;
  onToggleHidden: () => void;
  onEdit?: () => void;
  onDelete?: () => void;
}) {
  const { t } = useT("issues");
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } =
    useSortable({ id: item.barItemId });

  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      className={cn(
        "flex min-w-0 items-center gap-2 rounded-md px-2 py-1.5",
        isDragging && "z-10 bg-accent opacity-80",
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
      <span className="min-w-0 flex-1 truncate text-body">
        {item.label}
        {item.kind === "builtin" && (
          <span className="ml-1.5 text-caption text-muted-foreground">
            {t(($) => $.view_bar.builtin_tag)}
          </span>
        )}
      </span>
      {item.kind === "view" && item.canManage && (
        <span className="flex items-center gap-0.5">
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={t(($) => $.view_selector.edit)}
            onClick={onEdit}
          >
            <Pencil className="size-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={t(($) => $.view_bar.delete)}
            onClick={onDelete}
            className="text-muted-foreground hover:text-destructive"
          >
            <Trash2 className="size-3.5" />
          </Button>
        </span>
      )}
      <Switch
        size="sm"
        checked={!hidden}
        disabled={anchor}
        aria-label={t(($) => $.view_bar.visible_toggle)}
        onCheckedChange={onToggleHidden}
      />
    </div>
  );
}

/**
 * 单个 surface 视图栏的集中管理器：顺序（拖拽）、显隐（开关）、以及可管理
 * 保存视图的编辑/删除。顺序与显隐会乐观写入"每用户偏好文档"；编辑/删除
 * 委托给宿主（它们需要接到 surface 状态的保存对话框/删除 mutation）。
 */
export function ManageViewsDialog({
  open,
  onOpenChange,
  items,
  hiddenSet,
  anchorId,
  onReorder,
  onToggleHidden,
  onEditView,
  onDeleteView,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 完整的有序列表（内置 + 已保存视图），由宿主预先组装。 */
  items: ViewBarItem[];
  hiddenSet: Set<string>;
  anchorId: string;
  onReorder: (orderedIds: string[]) => void;
  onToggleHidden: (barItemId: string, hidden: boolean) => void;
  onEditView: (view: IssueView) => void;
  onDeleteView: (view: IssueView) => Promise<void>;
}) {
  const { t } = useT("issues");
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
  );
  // 拖拽期间维护一份本地顺序，让行能跟随指针移动而不必每个像素都打一次服务端；
  // 松手时统一提交（一次 PUT）。
  const [localOrder, setLocalOrder] = useState<string[] | null>(null);
  // 若对话框在拖拽中途卸载（按 Escape 关闭、视图被删等），dnd-kit 不会触发
  // cancel 事件——这里自己负责清掉文档级光标。
  useEffect(() => () => setDndCursor(false), []);
  useEffect(() => {
    if (!open) setLocalOrder(null);
  }, [open]);

  const orderedItems = useMemo(() => {
    if (!localOrder) return items;
    const byId = new Map(items.map((item) => [item.barItemId, item]));
    const next: ViewBarItem[] = [];
    for (const id of localOrder) {
      const item = byId.get(id);
      if (item) {
        next.push(item);
        byId.delete(id);
      }
    }
    return [...next, ...byId.values()];
  }, [items, localOrder]);

  const [deleting, setDeleting] = useState<IssueView | null>(null);

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const ids = orderedItems.map((item) => item.barItemId);
    const from = ids.indexOf(String(active.id));
    const to = ids.indexOf(String(over.id));
    if (from < 0 || to < 0) return;
    const next = arrayMove(ids, from, to);
    setLocalOrder(next);
    onReorder(next);
  };

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-1.5">
              <Layers className="size-4" />
              {t(($) => $.view_bar.manage_title)}
            </DialogTitle>
            <DialogDescription>{t(($) => $.view_bar.manage_hint)}</DialogDescription>
          </DialogHeader>
          <div className="-mx-2 min-w-0 overflow-hidden">
            <DndContext
              sensors={sensors}
              collisionDetection={closestCenter}
              // 行与行只做纵向交换——与表格表头给列重排加的限制一致，只是转了 90°。
              // 同理，横向自动滚动也不是这个手势能作用到的轴。
              modifiers={[restrictToVerticalAxis]}
              autoScroll={{ threshold: { x: 0, y: 0.15 } }}
              onDragStart={() => setDndCursor(true)}
              onDragCancel={() => setDndCursor(false)}
              onDragEnd={(event) => {
                setDndCursor(false);
                handleDragEnd(event);
              }}
            >
              <SortableContext
                items={orderedItems.map((item) => item.barItemId)}
                strategy={verticalListSortingStrategy}
              >
                {orderedItems.map((item) => (
                  <SortableRow
                    key={item.barItemId}
                    item={item}
                    hidden={hiddenSet.has(item.barItemId)}
                    anchor={item.barItemId === anchorId}
                    onToggleHidden={() =>
                      onToggleHidden(item.barItemId, !hiddenSet.has(item.barItemId))
                    }
                    onEdit={item.view ? () => onEditView(item.view!) : undefined}
                    onDelete={item.view ? () => setDeleting(item.view!) : undefined}
                  />
                ))}
              </SortableContext>
            </DndContext>
          </div>
        </DialogContent>
      </Dialog>

      <DeleteViewConfirm
        view={deleting}
        onOpenChange={(open) => {
          if (!open) setDeleting(null);
        }}
        onConfirm={onDeleteView}
      />
    </>
  );
}

