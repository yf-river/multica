// 延迟 Tooltip 包装器：只在首次悬停时才挂载 Tooltip 机制，先用原触发器元素占位，避免密集网格里大量从未悬停的 tooltip 白白挂载。
"use client";

import {
  cloneElement,
  useEffect,
  useRef,
  useState,
  type ReactElement,
  type ReactNode,
} from "react";
import { Tooltip, TooltipContent } from "@multica/ui/components/ui/tooltip";

// 与 ui 的 TooltipProvider 默认延迟保持一致，让延迟后的首次打开感觉和原生 tooltip 一样。
const TOOLTIP_OPEN_DELAY = 200;

/**
 * 只在首次悬停时才挂载机制的 Tooltip。
 *
 * 密集网格（看板/泳道列头）会挂载成百上千个几乎从不被悬停的 tooltip 根——
 * 这是可测量的界面挂载开销。本包装器在指针首次进入前只渲染触发器元素，
 * 之后才挂载一个锚定到同一元素上的受控 Tooltip。
 *
 * 与"替换触发器"方案不同，触发器元素由本组件在预热前后渲染在同一树位置——
 * 它的 DOM 节点被保留，所以手势中途的事件（悬停本身带来的点击）绝不会落在
 * 已分离的节点上。悬停开/关在这里驱动（进入定时、离开关闭）；弹层通过锚点
 * ref 而非 Base UI 触发器包装来相对触发器定位。
 * @param trigger 可见的触发器元素（如图标按钮），克隆时带上悬停处理器和锚点
 *   ref，其自身 props 被保留。
 * @param content 提示内容。
 * @param side 弹层方向。
 */
export function DeferredTooltip({
  trigger,
  content,
  side,
}: {
  /** 可见的触发器元素（如图标按钮），克隆时带上悬停处理器和锚点 ref，
   *  其自身 props 被保留。 */
  trigger: ReactElement<Record<string, unknown>>;
  /** 提示内容。 */
  content: ReactNode;
  /** 弹层方向。 */
  side?: "top" | "bottom" | "left" | "right";
}) {
  const anchorRef = useRef<HTMLElement | null>(null);
  const [warm, setWarm] = useState(false);
  const [open, setOpen] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const clearTimer = () => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  };
  useEffect(() => clearTimer, []);

  const element = cloneElement(trigger, {
    ref: (el: HTMLElement | null) => {
      anchorRef.current = el;
    },
    onPointerEnter: (e: React.PointerEvent) => {
      (trigger.props.onPointerEnter as ((e: React.PointerEvent) => void) | undefined)?.(e);
      setWarm(true);
      clearTimer();
      timerRef.current = setTimeout(() => setOpen(true), TOOLTIP_OPEN_DELAY);
    },
    onPointerLeave: (e: React.PointerEvent) => {
      (trigger.props.onPointerLeave as ((e: React.PointerEvent) => void) | undefined)?.(e);
      clearTimer();
      setOpen(false);
    },
  });

  return (
    <>
      {element}
      {warm && (
        <Tooltip open={open} onOpenChange={setOpen}>
          <TooltipContent side={side} anchor={anchorRef}>
            {content}
          </TooltipContent>
        </Tooltip>
      )}
    </>
  );
}
