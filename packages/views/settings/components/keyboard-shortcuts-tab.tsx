// 本文件是「键盘快捷键」设置标签页：列出所有可自定义的快捷键动作，支持点击按钮
// 重新录制、禁用单项、单项还原与一键全部还原；下方另列一组不可修改的固定快捷键。

"use client";

import { useMemo, useState } from "react";
import { Keyboard, RotateCcw, Search, X } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
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
import { cn } from "@multica/ui/lib/utils";
import {
  findShortcutConflict,
  createShortcutChord,
  isReservedShortcut,
  isShortcutAllowedForAction,
  isPlainShortcut,
  resolveShortcut,
  shortcutFromEvent,
  SHORTCUT_ACTIONS,
  useShortcutStore,
  type ShortcutActionDefinition,
  type ShortcutActionId,
  type ShortcutCategory,
  type ShortcutChord,
} from "@multica/core/shortcuts";
import { isImeComposing } from "@multica/core/utils";
import { useT } from "../../i18n";
import { ShortcutKeycaps } from "../../common/shortcut-keycaps";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";

/** 录制快捷键失败时的错误分类：与其它动作冲突 / 撞上系统保留键 / 特定动作不允许 /
 *  一般性不安全 / 无错误。 */
type CaptureError =
  | { kind: "conflict"; actionId: ShortcutActionId }
  | { kind: "reserved" }
  | { kind: "send" }
  | { kind: "unsafe" }
  | null;

/**
 * 「键盘快捷键」标签页。每个可自定义动作一行：中间是当前快捷键按键帽（点击进入
 * 「录制中」状态，直接按想要的新组合），右侧是还原、禁用两个图标按钮。搜索框按
 * 动作名称/描述过滤列表。
 */
export function KeyboardShortcutsTab() {
  const { t } = useT("settings");
  const [query, setQuery] = useState("");
  const [recording, setRecording] = useState<ShortcutActionId | null>(null);
  const [captureError, setCaptureError] = useState<CaptureError>(null);
  const [resetConfirmOpen, setResetConfirmOpen] = useState(false);
  const overrides = useShortcutStore((state) => state.overrides);
  const setShortcut = useShortcutStore((state) => state.setShortcut);
  const resetShortcut = useShortcutStore((state) => state.resetShortcut);
  const resetAll = useShortcutStore((state) => state.resetAll);

  const visibleActions = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    if (!needle) return SHORTCUT_ACTIONS;
    return SHORTCUT_ACTIONS.filter((action) => {
      const label = t(($) => $.shortcuts.actions[action.id].label);
      const description = t(
        ($) => $.shortcuts.actions[action.id].description,
      );
      return `${label} ${description}`
        .toLocaleLowerCase()
        .includes(needle);
    });
  }, [query, t]);

  const groups: readonly ShortcutCategory[] = ["general", "navigation"];

  // 录制核心：把用户按下的组合键套用到指定动作上。按 keydown 事件的顺序先拦下
  // 冒泡与浏览器默认行为，再做一连串校验，全部通过才写入。
  const capture = (actionId: ShortcutActionId, event: React.KeyboardEvent) => {
    event.preventDefault();
    event.stopPropagation();
    // 长按自动重复的 keydown 事件与输入法组字阶段的按键都跳过，避免误录。
    if (event.repeat || isImeComposing(event)) return;
    if (event.key === "Escape") {
      // Esc 结束录制、什么都不改。
      setRecording(null);
      setCaptureError(null);
      return;
    }
    if (event.key === "Backspace" || event.key === "Delete") {
      // 退格/删除视为「禁用该快捷键」（把覆盖设为 null）。
      setShortcut(actionId, null);
      setRecording(null);
      setCaptureError(null);
      return;
    }

    const shortcut = shortcutFromEvent(event.nativeEvent);
    if (!shortcut) return;
    // 系统/浏览器保留键（如单独 Ctrl）不可被占用。
    if (isReservedShortcut(shortcut)) {
      setCaptureError({ kind: "reserved" });
      return;
    }
    // 个别动作对按键有硬性限制（如发送消息不允许吞掉回车），违规直接拒绝。
    if (!isShortcutAllowedForAction(actionId, shortcut)) {
      setCaptureError({ kind: actionId === "send" ? "send" : "unsafe" });
      return;
    }
    // 新组合不能与其它已占用动作冲突；冲突时把对手动作名带进错误提示。
    const conflict = findShortcutConflict(actionId, shortcut);
    if (conflict) {
      setCaptureError({ kind: "conflict", actionId: conflict });
      return;
    }

    setShortcut(actionId, shortcut);
    setRecording(null);
    setCaptureError(null);
  };

  return (
    <SettingsTab
      title={t(($) => $.shortcuts.title)}
      description={t(($) => $.shortcuts.description)}
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="relative min-w-0 flex-1 sm:max-w-sm">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t(($) => $.shortcuts.search_placeholder)}
            aria-label={t(($) => $.shortcuts.search_placeholder)}
            className="pl-8"
          />
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={() => setResetConfirmOpen(true)}
          disabled={Object.keys(overrides).length === 0}
        >
          <RotateCcw className="size-3.5" />
          {t(($) => $.shortcuts.reset_all)}
        </Button>
      </div>

      {groups.map((category) => {
        const actions = visibleActions.filter(
          (action) => action.category === category,
        );
        if (actions.length === 0) return null;
        return (
          <SettingsSection
            key={category}
            title={t(($) => $.shortcuts.categories[category])}
          >
            <SettingsCard>
              {actions.map((action) => (
                <ShortcutRow
                  key={action.id}
                  action={action}
                  shortcut={resolveShortcut(overrides, action.id)}
                  customized={Object.prototype.hasOwnProperty.call(
                    overrides,
                    action.id,
                  )}
                  recording={recording === action.id}
                  error={recording === action.id ? captureError : null}
                  onStartRecording={() => {
                    setRecording(action.id);
                    setCaptureError(null);
                  }}
                  onCancelRecording={() => {
                    setRecording(null);
                    setCaptureError(null);
                  }}
                  onCapture={(event) => capture(action.id, event)}
                  onDisable={() => {
                    setShortcut(action.id, null);
                    setCaptureError(null);
                  }}
                  onReset={() => {
                    resetShortcut(action.id);
                    setCaptureError(null);
                  }}
                />
              ))}
            </SettingsCard>
          </SettingsSection>
        );
      })}

      {visibleActions.length === 0 ? (
        <div className="rounded-lg border border-dashed px-4 py-10 text-center text-body text-muted-foreground">
          {t(($) => $.shortcuts.no_results)}
        </div>
      ) : null}

      <SettingsSection
        title={t(($) => $.shortcuts.fixed.title)}
        description={t(($) => $.shortcuts.fixed.description)}
      >
        <SettingsCard>
          <FixedShortcutRow label={t(($) => $.shortcuts.fixed.open_settings)} shortcut={createShortcutChord(",", { primary: true })} />
          <FixedShortcutRow label={t(($) => $.shortcuts.fixed.close_tab)} shortcut={createShortcutChord("W", { primary: true })} />
          <FixedShortcutRow label={t(($) => $.shortcuts.fixed.zoom_in)} shortcut={createShortcutChord("Plus", { primary: true })} />
          <FixedShortcutRow label={t(($) => $.shortcuts.fixed.zoom_out)} shortcut={createShortcutChord("Minus", { primary: true })} />
          <FixedShortcutRow label={t(($) => $.shortcuts.fixed.reset_zoom)} shortcut={createShortcutChord("0", { primary: true })} />
          <FixedShortcutRow label={t(($) => $.shortcuts.fixed.close_dialog)} shortcut={createShortcutChord("Escape")} />
        </SettingsCard>
      </SettingsSection>

      <AlertDialog open={resetConfirmOpen} onOpenChange={setResetConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.shortcuts.reset_confirm.title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.shortcuts.reset_confirm.description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(($) => $.shortcuts.reset_confirm.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                resetAll();
                setCaptureError(null);
                setRecording(null);
                setResetConfirmOpen(false);
              }}
            >
              {t(($) => $.shortcuts.reset_confirm.confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

/**
 * 单个可自定义快捷键的行：左侧动作名/说明，右侧当前按键帽（可点击进入录制）、
 * 还原与禁用按钮。录制中时把按钮变成「录制中」并监听按键；底部显示错误提示
 * （冲突/保留/受限）或「按新快捷键」的提示。对「发送」动作且当前是纯回车时，
 * 额外给一条品牌色说明（回车保留了特殊含义）。
 */
function ShortcutRow({
  action,
  shortcut,
  customized,
  recording,
  error,
  onStartRecording,
  onCancelRecording,
  onCapture,
  onDisable,
  onReset,
}: {
  action: ShortcutActionDefinition;
  shortcut: ShortcutChord | null;
  customized: boolean;
  recording: boolean;
  error: CaptureError;
  onStartRecording: () => void;
  onCancelRecording: () => void;
  onCapture: (event: React.KeyboardEvent) => void;
  onDisable: () => void;
  onReset: () => void;
}) {
  const { t } = useT("settings");
  const label = t(($) => $.shortcuts.actions[action.id].label);
  const description = t(($) => $.shortcuts.actions[action.id].description);
  const errorText = error?.kind === "reserved"
    ? t(($) => $.shortcuts.reserved_error)
    : error?.kind === "send"
      ? t(($) => $.shortcuts.send_error)
    : error?.kind === "unsafe"
      ? t(($) => $.shortcuts.unsafe_error)
    : error?.kind === "conflict"
      ? t(($) => $.shortcuts.conflict_error, {
          action: t(($) => $.shortcuts.actions[error.actionId].label),
        })
      : null;

  return (
    <SettingsRow
      label={label}
      description={
        action.id === "send" && isPlainShortcut(shortcut, "Enter") ? (
          <>
            {description}
            <span className="mt-1 block text-brand">
              {t(($) => $.shortcuts.send_enter_hint)}
            </span>
          </>
        ) : description
      }
      align="start"
      size="select-wide"
    >
      <div className="flex flex-col items-stretch gap-1.5 sm:items-end">
        <div className="flex items-center justify-end gap-1.5">
          <button
            type="button"
            onClick={onStartRecording}
            onKeyDown={recording ? onCapture : undefined}
            onBlur={onCancelRecording}
            className={cn(
              "inline-flex h-8 min-w-28 items-center justify-center rounded-md border bg-background px-2.5 font-mono text-caption font-medium shadow-xs outline-none transition-colors hover:bg-surface-hover focus-visible:ring-2 focus-visible:ring-ring",
              recording && "border-brand bg-brand/5 text-brand ring-2 ring-brand/20",
              error && "border-destructive text-destructive ring-destructive/20",
            )}
            aria-label={t(($) => $.shortcuts.record_aria, { action: label })}
            aria-pressed={recording}
          >
            {recording ? (
              <span className="inline-flex items-center gap-1.5 font-sans">
                <Keyboard className="size-3.5" />
                {t(($) => $.shortcuts.recording)}
              </span>
            ) : shortcut ? (
              <ShortcutKeycaps shortcut={shortcut} decorative />
            ) : (
              <span className="font-sans font-normal text-muted-foreground">
                {t(($) => $.shortcuts.unassigned)}
              </span>
            )}
          </button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            onClick={onReset}
            disabled={!customized}
            aria-label={t(($) => $.shortcuts.reset_action, { action: label })}
            title={t(($) => $.shortcuts.reset)}
          >
            <RotateCcw className="size-3.5" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            onClick={onDisable}
            disabled={shortcut === null}
            aria-label={t(($) => $.shortcuts.disable_action, { action: label })}
            title={t(($) => $.shortcuts.disable)}
          >
            <X className="size-3.5" />
          </Button>
        </div>
        {errorText ? (
          <span role="alert" className="max-w-72 text-right text-caption text-destructive">
            {errorText}
          </span>
        ) : recording ? (
          <span className="text-right text-micro text-muted-foreground">
            {t(($) => $.shortcuts.record_hint)}
          </span>
        ) : null}
      </div>
    </SettingsRow>
  );
}

/** 固定快捷键的展示行：只读渲染按键帽，不提供自定义。 */
function FixedShortcutRow({ label, shortcut }: { label: string; shortcut: ShortcutChord }) {
  return (
    <SettingsRow label={label}>
      <ShortcutKeycaps shortcut={shortcut} size="md" className="justify-end" />
    </SettingsRow>
  );
}
