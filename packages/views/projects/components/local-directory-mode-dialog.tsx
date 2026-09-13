"use client";

// 本地目录资源的「执行模式」选择弹窗：在「原地执行（改动直接出现在工作副本）」
// 与「worktree 分支执行（改动进分支、由你审）」之间二选一。
import { useEffect, useState } from "react";
import { GitBranch, Pencil, TriangleAlert } from "lucide-react";
import type { LocalDirectoryExecutionMode } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useT } from "../../i18n/use-t";

/**
 * worktree 选项为什么不可用。
 *
 * 只有两种原因，而且都不是对机器的猜测。`not_git` 由客户端自己确定——这个文件夹
 * 有没有可建分支的仓库，桌面端选择器已经查过了。`server_outdated` 是服务端对
 * 自己的描述：它到底认不认识 `execution_mode` 这个字段。
 *
 * 「机器到底能不能跑这种模式」刻意不在这里判断。那是服务端的问题，每次保存都会
 * 问一遍；被拒会以 `errorMessage` 形式回来，而不是前端先猜一个禁用的选项
 * （#7113）。但把判断交给服务端只有在服务端明确表态「我会查」之后才安全：老版本
 * 服务端会丢弃该字段并直接回 201，任务就会去改用户本来想隔离的目录。
 *
 * `undefined` 表示可用。
 */
export type WorktreeUnavailableReason = "not_git" | "server_outdated";

interface LocalDirectoryModeDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 正在配置的绝对路径，展示出来让用户确认自己选的是哪。 */
  path: string;
  /** 预选的模式——编辑时是当前模式，新增时是 in_place。 */
  value: LocalDirectoryExecutionMode;
  /** 设置了说明 worktree 不能选；对应选项会禁用并给出原因。 */
  unavailableReason?: WorktreeUnavailableReason;
  /** 要内联展示的服务端拒绝原因（例如只有 API 才能发现的 422）。 */
  errorMessage?: string;
  saving?: boolean;
  /** 确认按钮文案在「新增资源」与「编辑资源」时不同。 */
  confirmLabel: string;
  onConfirm: (mode: LocalDirectoryExecutionMode) => void;
}

/**
 * 本地目录资源的模式选择器。
 *
 * 刻意不把原始的 `in_place` / `worktree` 标识当主文案。用户真正做的选择是
 * 「结果怎么拿回来」——改动直接出现在工作副本里，还是进一条分支等他们审——
 * 所以选项以此为主题，标识符只是给想对 CLI 或文档的读者看的次要提示。
 *
 * @param open 是否打开
 * @param onOpenChange 打开状态变化回调
 * @param path 被配置的目录绝对路径
 * @param value 当前/预选模式
 * @param unavailableReason worktree 不可用的原因（可选）
 * @param errorMessage 内联展示的服务端错误（可选）
 * @param saving 保存中标记（禁用操作按钮）
 * @param confirmLabel 确认按钮文案
 * @param onConfirm 确认时的回调（携带所选模式）
 */
export function LocalDirectoryModeDialog({
  open,
  onOpenChange,
  path,
  value,
  unavailableReason,
  errorMessage,
  saving = false,
  confirmLabel,
  onConfirm,
}: LocalDirectoryModeDialogProps) {
  const { t } = useT("projects");
  const [selected, setSelected] = useState<LocalDirectoryExecutionMode>(value);

  // 弹窗为不同资源重新打开时重新同步一次选中值，否则上一行的模式会预选到这一行。
  useEffect(() => {
    if (open) setSelected(value);
  }, [open, value]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t(($) => $.resources.mode_dialog_title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.resources.mode_dialog_description)}
          </DialogDescription>
        </DialogHeader>

        <div className="rounded-md bg-muted px-2.5 py-1.5 font-mono text-micro text-muted-foreground break-all">
          {path}
        </div>

        <LocalDirectoryModeOptions
          value={selected}
          onChange={setSelected}
          unavailableReason={unavailableReason}
        />

        {errorMessage && (
          <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-caption text-destructive">
            <TriangleAlert className="size-3.5 mt-0.5 shrink-0" />
            <span>{errorMessage}</span>
          </div>
        )}

        <DialogFooter>
          <Button
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            {t(($) => $.resources.mode_cancel)}
          </Button>
          <Button onClick={() => onConfirm(selected)} disabled={saving}>
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

interface LocalDirectoryModeOptionsProps {
  value: LocalDirectoryExecutionMode;
  onChange: (mode: LocalDirectoryExecutionMode) => void;
  unavailableReason?: WorktreeUnavailableReason;
}

/**
 * 二选一的选项本体，不包任何外围壳。
 *
 * 拆出来共享，是为了让弹窗（编辑已有资源）和创建项目弹窗里的紧凑选择器提供
 * 完全相同的选项、文案和禁用态——决策逻辑相同，只是容器不同。
 */
export function LocalDirectoryModeOptions({
  value,
  onChange,
  unavailableReason,
}: LocalDirectoryModeOptionsProps) {
  const { t } = useT("projects");
  const worktreeDisabled = unavailableReason !== undefined;

  return (
    <div className="flex flex-col gap-2">
      <ModeOption
        icon={<Pencil className="size-4" />}
        title={t(($) => $.resources.mode_in_place_title)}
        description={t(($) => $.resources.mode_in_place_description)}
        identifier="in_place"
        selected={value === "in_place"}
        onSelect={() => onChange("in_place")}
      />
      <ModeOption
        icon={<GitBranch className="size-4" />}
        title={t(($) => $.resources.mode_worktree_title)}
        description={t(($) => $.resources.mode_worktree_description)}
        identifier="worktree"
        selected={value === "worktree"}
        disabled={worktreeDisabled}
        disabledReason={
          unavailableReason === "not_git"
            ? t(($) => $.resources.mode_worktree_needs_git)
            : unavailableReason === "server_outdated"
              ? t(($) => $.resources.mode_worktree_needs_server_upgrade)
              : undefined
        }
        onSelect={() => onChange("worktree")}
      />
    </div>
  );
}

interface ModeOptionProps {
  icon: React.ReactNode;
  title: string;
  description: string;
  identifier: string;
  selected: boolean;
  disabled?: boolean;
  disabledReason?: string;
  onSelect: () => void;
}

function ModeOption({
  icon,
  title,
  description,
  identifier,
  selected,
  disabled = false,
  disabledReason,
  onSelect,
}: ModeOptionProps) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      disabled={disabled}
      onClick={onSelect}
      // 选中态用边框 + 外圈来表达，而不是背景色块：这样鼠标悬停时也仍然清晰——
      // 悬停只会挪动背景。
      className={`flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-colors ${
        selected
          ? "border-primary ring-1 ring-primary"
          : "border-border hover:bg-muted/50"
      } ${disabled ? "cursor-not-allowed opacity-60" : ""}`}
    >
      <span
        className={`mt-0.5 shrink-0 ${
          selected ? "text-primary" : "text-muted-foreground"
        }`}
      >
        {icon}
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-2">
          <span className="text-body font-medium">{title}</span>
          <span className="font-mono text-micro text-muted-foreground">
            {identifier}
          </span>
        </span>
        <span className="mt-0.5 block text-caption text-muted-foreground">
          {description}
        </span>
        {disabled && disabledReason && (
          <span className="mt-1.5 flex items-start gap-1.5 text-caption text-warning">
            <TriangleAlert className="size-3 mt-0.5 shrink-0" />
            <span>{disabledReason}</span>
          </span>
        )}
      </span>
    </button>
  );
}
