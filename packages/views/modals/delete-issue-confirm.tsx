// 本文件职责：「删除 issue」二次确认弹窗——用 AlertDialog 强确认，删除成功后返回来源列表页。
"use client";

import { useState } from "react";
import { toast } from "sonner";
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
import { useDeleteIssue } from "@multica/core/issues/mutations";
import { useBackOrReplace } from "../navigation";
import { useT } from "../i18n";

/**
 * 「删除 issue」确认弹窗。
 *
 * 打开即弹强确认（AlertDialog），确认后调用删除接口，成功 toast 提示并关闭；
 * 同时尽量「返回来源列表页」。`fallbackPath` 只在没有站内历史可回退时兜底生效。
 *
 * @param onClose 关闭弹窗的回调
 * @param data    弹窗上下文，需要 `issueId`；可选 `onDeletedFallbackPath`
 *                作为删除成功后的回退跳转地址
 */
export function DeleteIssueConfirmModal({
  onClose,
  data,
}: {
  onClose: () => void;
  data: Record<string, unknown> | null;
}) {
  const { t } = useT("modals");
  const issueId = (data?.issueId as string) || "";
  // 仅当调用方正渲染「将要被删的这个 issue」时（即详情页）才设置它；列表页调用方
  // 不传此值，删除后留在原列表即可。
  const fallbackPath = (data?.onDeletedFallbackPath as string | undefined) || undefined;
  const [deleting, setDeleting] = useState(false);
  const deleteIssue = useDeleteIssue();
  const backOrReplace = useBackOrReplace();

  const handleDelete = async () => {
    if (!issueId) return;
    setDeleting(true);
    try {
      await deleteIssue.mutateAsync(issueId);
      toast.success(t(($) => $.delete_issue.toast_deleted));
      onClose();
      // 返回用户打开该 issue 时所在的列表页；`fallbackPath` 只在应用内没有
      // 可回退的历史时才兜底跳转。
      if (fallbackPath) backOrReplace(fallbackPath);
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.delete_issue.toast_delete_failed),
      );
      setDeleting(false);
    }
  };

  return (
    <AlertDialog open onOpenChange={(v) => { if (!v && !deleting) onClose(); }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(($) => $.delete_issue.title)}</AlertDialogTitle>
          <AlertDialogDescription>
            {t(($) => $.delete_issue.description)}
            <span className="mt-2 block text-caption text-muted-foreground">
              {t(($) => $.delete_issue.hint)}
            </span>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={deleting}>{t(($) => $.delete_issue.cancel)}</AlertDialogCancel>
          <AlertDialogAction
            onClick={handleDelete}
            disabled={deleting}
            className="bg-destructive text-white hover:bg-destructive/90"
          >
            {deleting ? t(($) => $.delete_issue.deleting) : t(($) => $.delete_issue.confirm)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
