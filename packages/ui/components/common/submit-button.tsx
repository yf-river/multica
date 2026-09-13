"use client";
// 本文件是「发送/停止」按钮：消息输入框旁的圆形按钮。空闲时是发送箭头；
// 正在运行（AI 作答中）时切换成方形「停止」按钮；loading/busy 时显示旋转圈。
// 按钮按下时会阻止默认事件，避免点按钮让安卓软键盘收起。

import type { ReactNode, SyntheticEvent } from "react";
import { ArrowUp, Loader2, Square } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";

/**
 * SubmitButton 组件的 props 定义。
 */
interface SubmitButtonProps {
  /** 点击发送时的回调 */
  onClick: () => void;
  /** 禁用发送按钮（前置条件不满足） */
  disabled?: boolean;
  /** 是否正在发送（转圈） */
  loading?: boolean;
  /**
   * 是否处于「后台忙」状态——用户已发起的后台工作还没完成（例如附件仍在
   * 上传），而不是前置条件缺失。此时按钮禁用并标记 `aria-busy`，屏幕阅读器
   * 用虚拟光标读到时是「忙碌」而不是一个莫名其妙的死控件。注意 busy 时按钮
   * 不可 Tab 聚焦，所以调用方还应把原因写进 `ariaLabel` / `tooltip`，
   * 以及（如果有）可见的标签文本里。
   */
  busy?: boolean;
  /** 是否正在运行（AI 作答中）——true 时显示「停止」按钮 */
  running?: boolean;
  /** 点击「停止」按钮时的回调 */
  onStop?: () => void;
  /**
   * 空闲（可发送）时悬浮在发送按钮上的提示。可传字符串或节点（如 `Send · ⌘↵`）。
   * 不传则不渲染 tooltip。
   * 快捷键提示由调用方自己拼接，让本组件不依赖 `@multica/core`（平台检测）
   * 与 i18n。
   */
  tooltip?: ReactNode;
  /** 纯图标发送按钮的可访问名称 */
  ariaLabel?: string;
  /** 运行中悬浮在「停止」按钮上的提示 */
  stopTooltip?: ReactNode;
  /** 纯图标「停止」按钮的可访问名称 */
  stopAriaLabel?: string;
}

/**
 * 让输入区保持焦点：阻止 pointerdown / mousedown 的默认行为，
 * 这样点发送/停止不会让安卓软键盘收起。
 */
function keepFocusInComposer(event: SyntheticEvent<HTMLButtonElement>) {
  event.preventDefault();
}

/**
 * SubmitButton：发送 ⇄ 停止的二态圆形按钮。
 *
 * - running：显示「停止」方块按钮，点击触发 onStop
 * - loading / busy：显示旋转圈；loading 是发送中转圈，busy 是后台忙时也转圈
 *   （见 busy 的 prop 说明）
 * - 其余：显示发送箭头
 *
 * @param props 见 SubmitButtonProps 定义
 * @returns 发送/停止按钮（可选包一层 tooltip）
 */
function SubmitButton({
  onClick,
  disabled,
  loading,
  busy,
  running,
  onStop,
  tooltip,
  ariaLabel,
  stopTooltip,
  stopAriaLabel,
}: SubmitButtonProps) {
  if (running) {
    const stopButton = (
      <Button
        size="icon-sm"
        className="rounded-full"
        onPointerDown={keepFocusInComposer}
        onMouseDown={keepFocusInComposer}
        onClick={onStop}
        aria-label={stopAriaLabel}
      >
        <Square className="fill-current" aria-hidden="true" />
      </Button>
    );
    return stopTooltip ? (
      <Tooltip>
        <TooltipTrigger render={stopButton} />
        <TooltipContent side="top">{stopTooltip}</TooltipContent>
      </Tooltip>
    ) : stopButton;
  }

  const submitButton = (
    <Button
      size="icon-sm"
      className="rounded-full"
      disabled={disabled || loading || busy}
      aria-disabled={busy || undefined}
      aria-busy={loading || busy || undefined}
      onPointerDown={keepFocusInComposer}
      onMouseDown={keepFocusInComposer}
      onClick={onClick}
      aria-label={ariaLabel}
    >
      {loading || busy ? (
        // busy 也要转圈、不能只是置灰：上传可能在后台跑，而屏幕上没有别的
        // 东西解释为什么按钮是禁用的（比如重新打开输入框还在重建占位内容，
        // 或者用户把它删了）。转圈在不用悬浮的情况下就能表达「在等什么」，
        // tooltip 负责说明具体原因。
        <Loader2 className="animate-spin" aria-hidden="true" />
      ) : (
        <ArrowUp aria-hidden="true" />
      )}
    </Button>
  );
  const submitControl = !tooltip
    ? submitButton
    : (
        <Tooltip>
          <TooltipTrigger render={submitButton} />
          <TooltipContent side="top">{tooltip}</TooltipContent>
        </Tooltip>
      );
  return submitControl;
}

export { SubmitButton, type SubmitButtonProps };
