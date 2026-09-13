// 本文件是「删除工作区」的确认对话框：要求用户逐字输入工作区名来解锁删除按钮。
// 工作区一旦删除会级联清掉其下所有 issue、agent、技能与运行记录，且服务端没有
// 回收站，所以确认门槛刻意设得很高。

"use client";

import { useEffect, useState } from "react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Button } from "@multica/ui/components/ui/button";
import { isImeComposing } from "@multica/core/utils";
import { useT } from "../../i18n";

/**
 * 删除工作区的「输入确认」对话框——GitHub 删除仓库时那种要求逐字输入的防误删弹窗。
 * 在用户输入与工作区名完全一致之前，红色删除按钮始终禁用。
 *
 * 比对规则与 GitHub 一致、刻意严格：区分大小写（能拦住「只记得大概拼写、记不清
 * 大小写」的误操作）；不去除首尾空格（带多余空格说明是手误，悄悄接受会削弱这道
 * 闸门的意义）。
 *
 * 输入内容在对话框关闭时清空：防止下次打开（可能换了个工作区）还残留上一次的输入。
 * @param props.workspaceName - 要删除的工作区名，作为比对基准与占位提示。
 * @param props.loading - 删除请求进行中；期间禁用输入和按钮。
 * @param props.open - 对话框是否打开。
 * @param props.onOpenChange - 外部控制开合的回调。
 * @param props.onConfirm - 输入校验通过后点击删除时触发。
 */
export function DeleteWorkspaceDialog({
  workspaceName,
  loading = false,
  open,
  onOpenChange,
  onConfirm,
}: {
  workspaceName: string;
  loading?: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
}) {
  const { t } = useT("settings");
  const [typed, setTyped] = useState("");
  const matched = typed === workspaceName;

  // 在「对话框关闭」和「工作区名变化」两种情况下都清空输入：前者避免再次打开时
  // 泄漏上一次尝试（可能针对的是另一个工作区）；后者是因为对话框打开期间若别的
  // 负责人改动了工作区名，已输入的内容会静默失配，且没有任何反馈说明为什么。
  useEffect(() => {
    setTyped("");
  }, [open, workspaceName]);

  const submit = () => {
    if (!matched || loading) return;
    onConfirm();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(($) => $.delete_workspace_dialog.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.delete_workspace_dialog.description)}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          <Label htmlFor="delete-workspace-confirm" className="text-caption">
            {t(($) => $.delete_workspace_dialog.type_to_confirm_prefix)}{" "}
            <code className="rounded bg-muted px-1 py-0.5 font-mono text-caption">
              {workspaceName}
            </code>{" "}
            {t(($) => $.delete_workspace_dialog.type_to_confirm_suffix)}
          </Label>
          <Input
            id="delete-workspace-confirm"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            onKeyDown={(e) => {
              if (isImeComposing(e)) return;
              if (e.key === "Enter") {
                e.preventDefault();
                submit();
              }
            }}
            placeholder={workspaceName}
            autoFocus
            disabled={loading}
            autoComplete="off"
            autoCorrect="off"
            autoCapitalize="off"
            spellCheck={false}
          />
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={loading}
          >
            {t(($) => $.delete_workspace_dialog.cancel)}
          </Button>
          <Button
            type="button"
            variant="destructive"
            onClick={submit}
            disabled={!matched || loading}
          >
            {loading ? t(($) => $.delete_workspace_dialog.deleting) : t(($) => $.delete_workspace_dialog.confirm)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
