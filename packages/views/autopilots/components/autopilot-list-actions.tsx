// autopilot 列表的行操作与批量操作：行级 kebab（新标签打开/暂停/恢复/删除）、
// 批量工具栏（暂停/恢复/删除多个）、以及两者共用的删除确认对话框。

"use client";

import { useState } from "react";
import {
  ExternalLink,
  Loader2,
  MoreHorizontal,
  Pause,
  Play,
  Trash2,
  X,
} from "lucide-react";
import { toast } from "sonner";
import type { Autopilot } from "@multica/core/types";
import {
  useDeleteAutopilot,
  useUpdateAutopilot,
} from "@multica/core/autopilots";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { useT } from "../../i18n";
import { useIntentNavigate } from "../../navigation";

// ---------------------------------------------------------------------------
// 删除对话框——单行（kebab）与批量共用一份实现。
// ---------------------------------------------------------------------------

/**
 * 删除 autopilot 的确认对话框。单行删除与批量删除共用：传入 `rows` 列表逐条删除。
 * @param rows 要删除的 autopilot 列表。
 * @param open 是否打开。
 * @param onOpenChange 开关回调。
 * @param onDeleted 全部删除成功后回调（批量工具栏用来清空选择）。
 */
export function DeleteAutopilotsDialog({
  rows,
  open,
  onOpenChange,
  onDeleted,
}: {
  rows: Autopilot[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDeleted?: () => void;
}) {
  const { t } = useT("autopilots");
  const deleteAutopilot = useDeleteAutopilot();
  const [deleting, setDeleting] = useState(false);

  // 逐条删除；任一条失败即中断并 toast 报错，避免"部分删除"被当成功。
  const handleDelete = async () => {
    setDeleting(true);
    try {
      for (const row of rows) {
        await deleteAutopilot.mutateAsync(row.id);
      }
      onOpenChange(false);
      onDeleted?.();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : String(err));
    } finally {
      setDeleting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            {t(($) => $.actions.delete_dialog.title)}
          </DialogTitle>
          <DialogDescription>
            {t(($) => $.actions.delete_dialog.description, {
              count: rows.length,
              name: rows[0]?.title ?? "",
            })}
          </DialogDescription>
        </DialogHeader>
        <p className="text-caption text-muted-foreground">
          {t(($) => $.actions.delete_dialog.warning)}
        </p>
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={deleting}
            onClick={() => onOpenChange(false)}
          >
            {t(($) => $.actions.delete_dialog.cancel)}
          </Button>
          <Button
            type="button"
            variant="destructive"
            size="sm"
            disabled={deleting}
            onClick={handleDelete}
          >
            {deleting ? (
              <>
                <Loader2 className="mr-1 size-3.5 animate-spin" />
                {t(($) => $.actions.delete_dialog.deleting)}
              </>
            ) : (
              t(($) => $.actions.delete_dialog.confirm)
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// kebab 与批量工具栏共用的状态助手。
// ---------------------------------------------------------------------------

// 批量设置状态（active/paused）：跳过已是目标状态的行，其余逐个更新。
function useSetStatus() {
  const updateAutopilot = useUpdateAutopilot();
  return async (rows: Autopilot[], status: "active" | "paused") => {
    try {
      for (const row of rows) {
        if (row.status === status) continue;
        await updateAutopilot.mutateAsync({ id: row.id, status });
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : String(err));
    }
  };
}

// ---------------------------------------------------------------------------
// 行 kebab——行是普通 `<div>`，整行导航是鼠标 `onClick`（见 `useRowLink`），
// 不是祖先 `<a>`。外层 span 阻止点击冒泡，使打开本菜单绝不会导航到该行——只做
// stopPropagation，不做 preventDefault（与技能列表同款模式）。
// ---------------------------------------------------------------------------

/**
 * 单行 kebab 菜单。除"新标签打开"外只有写操作（暂停/恢复/删除），所以对无写权限
 * 的成员整体隐藏——他们仍可通过"带修饰键的行点击"到达新标签。can_write 缺失
 * （旧服务器）时保持菜单可见、让后端继续把关。
 * @param row 该行对应的 autopilot。
 */
export function AutopilotRowActions({ row }: { row: Autopilot }) {
  const { t } = useT("autopilots");
  const { t: tCommon } = useT("common");
  const wsPaths = useWorkspacePaths();
  const intentNavigate = useIntentNavigate();
  const [deleteOpen, setDeleteOpen] = useState(false);
  const setStatus = useSetStatus();

  // 除导航入口外 kebab 只放写操作（暂停/恢复/删除），所以对没有写访问的成员整
  // 个隐藏——他们仍能通过修饰键行点击到达新标签页。can_write 缺失（旧服务器）时
  // 保持菜单可见、让后端继续把关。
  if (row.can_write === false) return null;

  return (
    <span
      onClick={(e) => e.stopPropagation()}
      className="flex items-center"
    >
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <button
              type="button"
              aria-label={t(($) => $.actions.row_menu)}
              className="flex size-7 items-center justify-center rounded-md text-muted-foreground opacity-0 transition-opacity hover:bg-accent hover:text-accent-foreground group-hover/row:opacity-100 data-popup-open:bg-accent data-popup-open:opacity-100 data-popup-open:text-accent-foreground"
            >
              <MoreHorizontal className="size-4" />
            </button>
          }
        />
        <DropdownMenuContent align="end" className="w-48">
          <DropdownMenuItem
            onClick={() =>
              intentNavigate(
                wsPaths.autopilotDetail(row.id),
                "foreground-tab",
              )
            }
          >
            <ExternalLink className="size-3.5" />
            {tCommon(($) => $.navigation.open_in_new_tab)}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          {row.status === "active" && (
            <DropdownMenuItem onClick={() => setStatus([row], "paused")}>
              <Pause className="size-3.5" />
              {t(($) => $.actions.pause)}
            </DropdownMenuItem>
          )}
          {row.status === "paused" && (
            <DropdownMenuItem onClick={() => setStatus([row], "active")}>
              <Play className="size-3.5" />
              {t(($) => $.actions.resume)}
            </DropdownMenuItem>
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            onClick={() => setDeleteOpen(true)}
          >
            <Trash2 className="size-3.5" />
            {t(($) => $.actions.delete)}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <DeleteAutopilotsDialog
        rows={[row]}
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
      />
    </span>
  );
}

// ---------------------------------------------------------------------------
// 批量工具栏
// ---------------------------------------------------------------------------

/**
 * 批量操作工具栏：勾选了多行时浮在页面底部居中，提供暂停/恢复/删除与清除选择。
 * 无选中行时不渲染。删除成功后自动清空选择。
 * @param rows 已选中的 autopilot 列表。
 * @param onClear 清除选择回调。
 */
export function AutopilotBatchToolbar({
  rows,
  onClear,
}: {
  rows: Autopilot[];
  onClear: () => void;
}) {
  const { t } = useT("autopilots");
  const [deleteOpen, setDeleteOpen] = useState(false);
  const setStatus = useSetStatus();

  if (rows.length === 0) return null;

  const anyActive = rows.some((r) => r.status === "active");
  const anyPaused = rows.some((r) => r.status === "paused");

  return (
    <>
      {/* 锚定到页面根（relative），不是视口——理由见技能批量工具栏。 */}
      <div className="absolute bottom-6 left-1/2 z-50 flex -translate-x-1/2 items-center gap-1 rounded-lg border bg-background px-2 py-1.5 shadow-lg max-md:above-chat-launcher">
        <div className="mr-1 flex items-center gap-1.5 border-r pl-1 pr-2">
          <span className="text-body font-medium">
            {t(($) => $.actions.selected, { count: rows.length })}
          </span>
          <button
            type="button"
            aria-label={t(($) => $.actions.clear_selection)}
            onClick={onClear}
            className="rounded p-0.5 transition-colors hover:bg-accent"
          >
            <X className="size-3.5 text-muted-foreground" />
          </button>
        </div>

        {anyActive && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setStatus(rows, "paused")}
          >
            <Pause className="mr-1 size-3.5" />
            {t(($) => $.actions.pause)}
          </Button>
        )}
        {anyPaused && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setStatus(rows, "active")}
          >
            <Play className="mr-1 size-3.5" />
            {t(($) => $.actions.resume)}
          </Button>
        )}
        <Button
          variant="ghost"
          size="sm"
          className="text-destructive hover:text-destructive"
          onClick={() => setDeleteOpen(true)}
        >
          <Trash2 className="mr-1 size-3.5" />
          {t(($) => $.actions.delete)}
        </Button>
      </div>

      <DeleteAutopilotsDialog
        rows={rows}
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        onDeleted={onClear}
      />
    </>
  );
}
