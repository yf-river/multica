"use client";

// 本文件：BatchActionToolbar —— 批量选择多行问题后浮现的工具条。
// 支持批量状态 / 优先级 / 负责人 / 删除；删除前有确认弹窗。
// placement 决定它是视口底部悬浮条还是内联在列表流里。

import { useEffect, useMemo, useState } from "react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { X, Trash2 } from "lucide-react";
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
import type { Issue, UpdateIssueRequest } from "@multica/core/types";
import { commonIssueFields } from "@multica/core/issues/batch";
import { issueBehavesAs } from "@multica/core/issues";
import { useBatchUpdateIssues, useBatchDeleteIssues } from "@multica/core/issues/mutations";
import { useModalStore } from "@multica/core/modals";
import { StatusPicker, PriorityPicker, AssigneePicker } from "./pickers";
import { useT } from "../../i18n";
import { cn } from "@multica/ui/lib/utils";
import {
  UI_EASE_OUT,
  UI_MOTION_DURATION,
} from "@multica/ui/lib/motion";
import { useIssueSurfaceActionsOptional } from "../surface/actions-context";
import { useIssueSurfaceSelection } from "../surface/selection-context";

/**
 * 批量操作工具条。
 */
export function BatchActionToolbar({
  issues,
  placement = "fixed-bottom",
  clearSelectionOnApply = false,
}: {
  /**
   * 本调用点的可选中问题全集（与渲染行同一份列表）。工具条按当前表面
   * 的活动选择对其过滤，好反映选中问题的真实共同状态/优先级/负责人，
   * 与技能列表按 `selectedIds` 过滤行是一个道理。
   */
  issues: Issue[];
  /**
   * "fixed-bottom" —— 悬浮在视口底部（默认；全屏问题列表用）。
   * "inline" —— 正常文档流渲染，调用方可把它放在选中行旁
   *（可滚动区段如子问题内用）。
   */
  placement?: "fixed-bottom" | "inline";
  /**
   * Drop the selection once a status / priority / assignee change fully
   * applies. Off for full-screen lists, where the selection stays so several
   * edits can be chained; on for sub-issues, where a batch edit is a one-shot
   * dispatch and a lingering inline toolbar reads as unfinished. A partial or
   * failed write keeps the selection so the user can retry.
   */
  clearSelectionOnApply?: boolean;
}) {
  const { t } = useT("issues");
  const selection = useIssueSurfaceSelection();
  const selectedIds = selection.selectedIds;
  const clear = selection.clear;
  const shouldReduceMotion = useReducedMotion() ?? false;

  // 权威选择集 = selectedIds ∩ 可见全集。当其它消费方（导出选中、公共字段）
  // 都做交集时，若这里对裸 selectedIds 动手，同一个"N 已选"可能指不同集合
  // ——例如实时更新把某选中行移出窗口，批量仍改了它而导出漏掉了它。
  // 计数、pickers 与下面每个动作共用这一份集合。
  const selectedIssues = useMemo(
    () => issues.filter((i) => selectedIds.has(i.id)),
    [issues, selectedIds],
  );
  const count = selectedIssues.length;

  // 让每个 picker 反映选中问题的真实共同值；选择为混合时回退到空态
  // （无对勾），而不是断言一个写死的默认值。
  const common = useMemo(
    () => commonIssueFields(selectedIssues),
    [selectedIssues],
  );

  const [statusOpen, setStatusOpen] = useState(false);
  const [priorityOpen, setPriorityOpen] = useState(false);
  const [assigneeOpen, setAssigneeOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const surfaceActions = useIssueSurfaceActionsOptional();
  const batchUpdate = useBatchUpdateIssues();
  const batchDelete = useBatchDeleteIssues();
  const openModal = useModalStore((s) => s.open);
  const loading =
    surfaceActions?.isPending ?? (batchUpdate.isPending || batchDelete.isPending);
  const ids = selectedIssues.map((issue) => issue.id);

  useEffect(() => {
    if (count > 0) return;
    setStatusOpen(false);
    setPriorityOpen(false);
    setAssigneeOpen(false);
    setDeleteOpen(false);
  }, [count]);

  const handleBatchUpdate = async (updates: Partial<UpdateIssueRequest>) => {
    try {
      const result = surfaceActions
        ? await surfaceActions.batchUpdate(ids, updates)
        : await batchUpdate.mutateAsync({ ids, updates });
      if (result && result.updated < count) {
        toast.warning(t(($) => $.batch.update_partial, { updated: result.updated, count }));
        return;
      }
      if (clearSelectionOnApply) clear();
      toast.success(t(($) => $.batch.update_success, { count }));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.batch.update_failed),
      );
    }
  };

  // 批量状态变更直接应用——不走"运行确认"弹窗（MUL-4155）。
  // done/cancelled 永远不会启动运行；backlog → active 的晋升现在与单问题
  // 改状态或 CLI 一样直接开工，不再多一道确认（MUL-4155 的产品决定）。
  // 过去状态变更要路由过 pre-trigger 弹窗，而在常见的 done/cancelled
  // 情形它只渲染一个误导性的"现在开始处理？→ 不会开始处理"框。
  // Agent/小队分配仍走 handleBatchAssignee 确认——那是唯一该预览
  // 运行扇出的批量动作。
  const handleBatchStatus = (updates: Partial<UpdateIssueRequest>) => {
    if (!updates.status) return;
    void handleBatchUpdate(updates);
  };

  const handleBatchAssignee = (updates: Partial<UpdateIssueRequest>) => {
    if ((updates.assignee_type === "agent" || updates.assignee_type === "squad") && updates.assignee_id) {
      // Backlog 在分配时从不启动运行（停车场），所以如果每个选中问题都在
      // backlog，确认弹窗只会渲染一个空的"不会开始"框——直接应用，
      // 与 handleBatchStatus 的 backlog 短路一致。混合选择仍走弹窗：
      // 非 backlog 的问题会触发运行、需要确认。
      // 用类别而非 key 判断：backlog 类别里的自定义状态同样是停车场，
      // 分配进去也永不启动运行。（MUL-6243）
      const allBacklog = selectedIssues.every((i) => issueBehavesAs(i, "backlog"));
      if (!allBacklog) {
        openModal("issue-run-confirm", {
          issueIds: ids,
          mode: "assign",
          assigneeType: updates.assignee_type,
          assigneeId: updates.assignee_id,
          onApplied: clearSelectionOnApply ? clear : undefined,
        });
        return;
      }
    }
    void handleBatchUpdate(updates);
  };

  const handleBatchDelete = async () => {
    try {
      if (surfaceActions) {
        await surfaceActions.batchDelete(ids);
      } else {
        await batchDelete.mutateAsync(ids);
      }
      clear();
      toast.success(t(($) => $.batch.delete_success, { count }));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.batch.delete_failed),
      );
    } finally {
      setDeleteOpen(false);
    }
  };

  return (
    <>
      <AnimatePresence initial={false}>
        {count > 0 && (
          <div
            key="issue-batch-toolbar"
            className={cn(
              "z-50",
              placement === "fixed-bottom"
                ? "fixed bottom-6 left-1/2 -translate-x-1/2 max-md:above-chat-launcher"
                : "mb-2 w-fit",
            )}
          >
            <motion.div
              className="flex items-center gap-1 rounded-lg border bg-background px-2 py-1.5 shadow-lg"
              initial={{
                opacity: 0,
                transform: shouldReduceMotion
                  ? "translateY(0)"
                  : "translateY(8px)",
              }}
              animate={{
                opacity: 1,
                transform: "translateY(0)",
                transition: {
                  duration: UI_MOTION_DURATION.fast,
                  ease: UI_EASE_OUT,
                },
              }}
              exit={{
                opacity: 0,
                transform: shouldReduceMotion
                  ? "translateY(0)"
                  : "translateY(8px)",
                transition: {
                  duration: shouldReduceMotion
                    ? UI_MOTION_DURATION.fast
                    : UI_MOTION_DURATION.micro,
                  ease: UI_EASE_OUT,
                },
              }}
            >
        <div className="flex items-center gap-1.5 pl-1 pr-2 border-r mr-1">
          <span className="text-body font-medium">{t(($) => $.batch.selected, { count })}</span>
          <button
            type="button"
            onClick={clear}
            className="rounded p-0.5 hover:bg-accent transition-colors"
          >
            <X className="size-3.5 text-muted-foreground" />
          </button>
        </div>

        {/* 状态 */}
        <StatusPicker
          status={common.status}
          onUpdate={handleBatchStatus}
          open={statusOpen}
          onOpenChange={setStatusOpen}
          triggerRender={<Button variant="ghost" size="sm" disabled={loading} />}
          trigger={t(($) => $.batch.status)}
          align="center"
        />

        {/* 优先级 */}
        <PriorityPicker
          priority={common.priority}
          onUpdate={handleBatchUpdate}
          open={priorityOpen}
          onOpenChange={setPriorityOpen}
          triggerRender={<Button variant="ghost" size="sm" disabled={loading} />}
          trigger={t(($) => $.batch.priority)}
          align="center"
        />

        {/* 负责人 */}
        <AssigneePicker
          assigneeType={common.assignee?.type ?? null}
          assigneeId={common.assignee?.id ?? null}
          mixed={common.assignee === null}
          onUpdate={handleBatchAssignee}
          open={assigneeOpen}
          onOpenChange={setAssigneeOpen}
          triggerRender={<Button variant="ghost" size="sm" disabled={loading} />}
          trigger={t(($) => $.batch.assignee)}
          align="center"
        />

        {/* 删除 */}
        <Button
          variant="ghost"
          size="sm"
          disabled={loading}
          onClick={() => setDeleteOpen(true)}
          className="text-destructive hover:text-destructive"
        >
          <Trash2 className="size-3.5 mr-1" />
          {t(($) => $.batch.delete)}
        </Button>
            </motion.div>
          </div>
        )}
      </AnimatePresence>

      {count > 0 && <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.batch.delete_dialog_title, { count })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.batch.delete_dialog_desc, { count })}
              <span className="mt-2 block text-caption text-muted-foreground">
                {t(($) => $.batch.delete_dialog_warning)}
              </span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.batch.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleBatchDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {t(($) => $.batch.delete)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>}
    </>
  );
}
