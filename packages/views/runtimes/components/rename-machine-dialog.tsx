// ============================================================================
// 机器重命名对话框（RenameMachineDialog）——给「整台机器」改名或清除名字
//
// 角色定位：机器详情页「重命名」按钮打开的 AlertDialog。一台 daemon 机器上每个
// 厂商各跑一条 runtime，所以「改机器名」实际是把名字盖到这台 daemon 的全部
// runtime 上（apply_to_machine），而不是只改其中一条——这正是第一版实现最让
// 人困惑的地方，现在统一成机器级语义。
//
// 语义约定：
//  - 输入留空 = 清除自定义名、回到设备默认名（成功 toast 文案与「改名」不同）。
//  - 发请求用的 runtimeId 是「当前用户有权编辑的任意一条 runtime」；服务端负责
//    把名字平铺到整台 daemon（云 worker 没有 daemon_id，就只改它自己——它自己
//    就是一台机器）。
// ============================================================================
"use client";

import { useEffect, useState } from "react";
import { toast } from "sonner";
import { useUpdateRuntime } from "@multica/core/runtimes/mutations";
import {
  AlertDialog,
  AlertDialogContent,
} from "@multica/ui/components/ui/alert-dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";

export interface RenameMachineDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  wsId: string;
  runtimeId: string;
  /** 机器当前的自定义名；还没设过（用的是默认名）时为空串。 */
  currentName: string;
}

/**
 * 机器重命名对话框：编辑整台机器在列表 / 详情里的显示名。
 *
 * @param open - 是否展示。
 * @param onOpenChange - 开关回调（提交中会被忽略，防止中途关掉）。
 * @param wsId - 工作区 id。
 * @param runtimeId - 这台机器上任一当前用户可编辑的 runtime id（改名的载体）。
 * @param currentName - 当前的机器自定义名，作为输入框初值。
 *
 * 提交即把 custom_name 以 apply_to_machine=true 写上去（机器级铺开）；输入清空
 * 则清除自定义名、回到默认。
 */
export function RenameMachineDialog({
  open,
  onOpenChange,
  wsId,
  runtimeId,
  currentName,
}: RenameMachineDialogProps) {
  const { t } = useT("runtimes");
  const updateRuntime = useUpdateRuntime(wsId);

  const [value, setValue] = useState(currentName);
  const submitting = updateRuntime.isPending;

  // 每次打开都重置成当前名字：取消编辑后残留的半截输入不会漏进下一次。
  useEffect(() => {
    if (open) setValue(currentName);
  }, [open, currentName]);

  const handleOpenChange = (next: boolean) => {
    if (submitting) return;
    onOpenChange(next);
  };

  const handleSave = () => {
    const trimmed = value.trim();
    updateRuntime.mutate(
      {
        runtimeId,
        // 空串 = 清除名字（回到默认）；apply_to_machine 恒为 true——这个对话框
        // 只会「改机器」，不存在只改单条 runtime 的路径。
        patch: { custom_name: trimmed, apply_to_machine: true },
      },
      {
        onSuccess: () => {
          toast.success(
            trimmed
              ? t(($) => $.machine.rename_dialog.toast_saved)
              : t(($) => $.machine.rename_dialog.toast_cleared),
          );
          onOpenChange(false);
        },
        onError: (err) =>
          toast.error(
            err instanceof Error && err.message
              ? err.message
              : t(($) => $.machine.rename_dialog.toast_failed),
          ),
      },
    );
  };

  return (
    <AlertDialog open={open} onOpenChange={handleOpenChange}>
      <AlertDialogContent
        className="w-[calc(100vw-2rem)] !max-w-[440px] gap-0 overflow-hidden rounded-lg p-0"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="px-5 pb-4 pt-5">
          <h2 className="text-title-sm font-semibold">
            {t(($) => $.machine.rename_dialog.title)}
          </h2>
          <p className="mt-1 text-body leading-5 text-muted-foreground">
            {t(($) => $.machine.rename_dialog.description)}
          </p>

          <Input
            className="mt-3"
            autoFocus
            value={value}
            maxLength={100}
            placeholder={t(($) => $.machine.rename_dialog.placeholder)}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !submitting) {
                e.preventDefault();
                handleSave();
              }
            }}
          />
          <p className="mt-1.5 text-caption text-muted-foreground">
            {t(($) => $.machine.rename_dialog.hint)}
          </p>
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
              {t(($) => $.machine.rename_dialog.cancel)}
            </Button>
            <Button
              type="button"
              className="w-full sm:w-auto"
              onClick={handleSave}
              disabled={submitting}
            >
              {submitting
                ? t(($) => $.machine.rename_dialog.saving)
                : t(($) => $.machine.rename_dialog.save)}
            </Button>
          </div>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  );
}
