// ============================================================================
// 删除自定义 runtime profile 的确认对话框（DeleteRuntimeProfileDialog）
//
// 角色定位：profile 管理弹窗详情面板里「删除」按钮打开的确认框。删除前用户看到
// 一条破坏性确认；若仍有关联（bound）该 profile 的 agent，服务端会用 409 拒绝，
// 本对话框把拒绝原因内联展示在按钮上方并保持打开，而不是弹一条原始错误 toast——
// 让管理员读明白原因后能体面地放弃删除。
// ============================================================================
"use client";

import { useEffect, useState } from "react";
import { AlertTriangle } from "lucide-react";
import { toast } from "sonner";
import type { RuntimeProfile } from "@multica/core/types";
import {
  parseRuntimeProfileBoundConflict,
  useDeleteRuntimeProfile,
} from "@multica/core/runtimes";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

/**
 * 删除自定义 runtime profile 的确认对话框。
 *
 * @param open - 是否展示。
 * @param onOpenChange - 开关回调（提交中被忽略）。
 * @param profile - 要删除的 profile。
 * @param wsId - 工作区 id。
 * @param onDeleted - 删除成功后的回调（父组件负责刷新列表 / 关闭）。
 *
 * 冲突处理：删除时若仍有 agent 绑定该 profile，服务端返回结构化 409——
 * parseRuntimeProfileBoundConflict 解析出后，把原因（优先服务端原文，否则本地
 * 兜底文案）内联展示并让用户自行决定是否继续/放弃。
 */
export function DeleteRuntimeProfileDialog({
  open,
  onOpenChange,
  profile,
  wsId,
  onDeleted,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  profile: RuntimeProfile;
  wsId: string;
  onDeleted: () => void;
}) {
  const { t } = useT("runtimes");
  const deleteProfile = useDeleteRuntimeProfile(wsId);
  const [submitting, setSubmitting] = useState(false);
  // 服务端下发的「仍有 agent 绑定」提示，内联展示在按钮区上方；每次重新打开
  // 对话框时重置，避免上次的旧消息残留。
  const [boundMessage, setBoundMessage] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setSubmitting(false);
      setBoundMessage(null);
    }
  }, [open]);

  const handleOpenChange = (next: boolean) => {
    if (submitting) return;
    onOpenChange(next);
  };

  const handleConfirm = async () => {
    setSubmitting(true);
    setBoundMessage(null);
    try {
      await deleteProfile.mutateAsync(profile.id);
      toast.success(t(($) => $.profiles.delete_dialog.toast_deleted));
      onDeleted();
    } catch (err) {
      const conflict = parseRuntimeProfileBoundConflict(err);
      if (conflict) {
        // 优先用服务端给出的具体措辞；body 里没带消息时，退回本地通用的
        // 「仍有绑定」文案。
        setBoundMessage(
          conflict.message ||
            t(($) => $.profiles.delete_dialog.error_bound),
        );
        return;
      }
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.profiles.delete_dialog.error_generic),
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={handleOpenChange}>
      <AlertDialogContent
        className="w-[calc(100vw-2rem)] !max-w-[440px] gap-0 overflow-hidden rounded-lg p-0"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="px-5 pb-4 pt-5">
          <AlertDialogTitle className="font-sans text-title-sm font-semibold">
            {t(($) => $.profiles.delete_dialog.title)}
          </AlertDialogTitle>
          <AlertDialogDescription className="mt-1 text-left text-body leading-5 text-pretty">
            {t(($) => $.profiles.delete_dialog.description, {
              name: profile.display_name,
            })}
          </AlertDialogDescription>
          {boundMessage && (
            <div
              role="alert"
              className="mt-3 flex items-start gap-2 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-caption"
            >
              <AlertTriangle
                aria-hidden="true"
                className="mt-0.5 size-3.5 shrink-0 text-warning"
              />
              <span className="text-foreground">{boundMessage}</span>
            </div>
          )}
        </div>
        <div className="border-t bg-muted/25 px-5 py-3">
          <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
            <Button
              type="button"
              variant="outline"
              className="w-full sm:w-auto"
              onClick={() => handleOpenChange(false)}
              disabled={submitting}
            >
              {t(($) => $.profiles.delete_dialog.cancel)}
            </Button>
            <Button
              type="button"
              variant="destructive"
              className="w-full sm:w-auto"
              onClick={handleConfirm}
              disabled={submitting}
            >
              {submitting
                ? t(($) => $.profiles.delete_dialog.deleting)
                : t(($) => $.profiles.delete_dialog.confirm)}
            </Button>
          </div>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  );
}
