"use client";

/**
 * 可横向滚动元素上的「点按 vs 拖拽」手势判定。
 *
 * 解决一个具体冲突：内联 Mermaid 图被点击时应打开查看器，但宽图本身也
 * 允许拖拽滚动看剩余部分。如果不区分用户意图，每次拖拽结束都会冒泡出
 * 一次 click，查看器就会盖到用户头上，看起来像图表在跟用户对着干。
 *
 * 规则（产品决策，MUL-4908）：指针一旦移动超过很小的阈值，该手势就永远
 * 被认定为拖拽，松手时绝不能当作点按——即使元素已经没有滚动空间也一样，
 * 否则一张滚不动的图会在用户松手时意外打开查看器。
 *
 * 触摸是唯一一类浏览器会对 `overflow-x: auto` 元素原生做拖拽滚动、且其
 * 竖向手势属于整页滚动的指针类型，所以这里不接管触摸，只记录意图；浏览器
 * 接管后会通过 `pointercancel` 通知我们。其余指针类型（鼠标、笔）没有原生
 * 拖拽滚动能力，由本 Hook 负责平移。
 */

import { useCallback, useRef, type PointerEvent as ReactPointerEvent } from "react";

// Kim 拍板的范围是 4–6px。取 5 在中间：既大于真实点击的抖动（含触控板
// 手抖的轻点），又明显小于一次有意的拖拽。
export const DRAG_THRESHOLD_PX = 5;

interface GestureState {
  pointerId: number;
  pointerType: string;
  startX: number;
  startY: number;
  startScrollLeft: number;
  dragged: boolean;
}

function isTouch(gesture: GestureState): boolean {
  return gesture.pointerType === "touch";
}

/**
 * 返回需要绑定到目标元素上的四个 pointer 事件处理器。
 */
export interface DragToScrollHandlers {
  onPointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerMove: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerUp: (event: ReactPointerEvent<HTMLElement>) => void;
  onPointerCancel: (event: ReactPointerEvent<HTMLElement>) => void;
}

/**
 * 给目标元素提供「点按 vs 拖拽」的手势判定。
 *
 * @param onTap 手势从未变成拖拽时，在松手那一刻触发。
 * @returns 一组要绑定到元素上的 pointer 事件处理器。
 */
export function useDragToScroll({
  onTap,
}: {
  onTap: () => void;
}): DragToScrollHandlers {
  // 用 ref 而不是 state：拖拽过程中每个 pointermove 都要更新，若触发渲染，
  // 会以这个频率重绘图表（及其 iframe），导致正在进行的平移卡顿。
  const gestureRef = useRef<GestureState | null>(null);

  const onPointerDown = useCallback((event: ReactPointerEvent<HTMLElement>) => {
    // 鼠标右键走的是上下文菜单，不是点按也不是拖拽。
    if (event.pointerType === "mouse" && event.button !== 0) return;

    gestureRef.current = {
      pointerId: event.pointerId,
      pointerType: event.pointerType,
      startX: event.clientX,
      startY: event.clientY,
      startScrollLeft: event.currentTarget.scrollLeft,
      dragged: false,
    };
  }, []);

  const onPointerMove = useCallback((event: ReactPointerEvent<HTMLElement>) => {
    const gesture = gestureRef.current;
    if (!gesture || gesture.pointerId !== event.pointerId) return;

    const deltaX = event.clientX - gesture.startX;
    const deltaY = event.clientY - gesture.startY;

    if (!gesture.dragged) {
      if (Math.hypot(deltaX, deltaY) <= DRAG_THRESHOLD_PX) return;
      gesture.dragged = true;
      // 确认是拖拽后才捕获指针，并且触摸永不捕获：pointerdown 就捕获会
      // 吞掉普通点击；捕获触摸则会干扰这里依赖的原生滚动。
      if (!isTouch(gesture)) {
        event.currentTarget.setPointerCapture?.(event.pointerId);
      }
    }

    // 触摸本身就会滚动；这里再手动改 scrollLeft 会让速度翻倍。
    if (isTouch(gesture)) return;
    // 滚到任一端的越界值会被浏览器自动钳制，所以一张滚不动的图只会原地
    // 不动——但手势仍然是拖拽，仍然会按下面逻辑抑制点按。
    event.currentTarget.scrollLeft = gesture.startScrollLeft - deltaX;
  }, []);

  const onPointerUp = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      const gesture = gestureRef.current;
      gestureRef.current = null;
      if (!gesture || gesture.pointerId !== event.pointerId) return;

      if (event.currentTarget.hasPointerCapture?.(event.pointerId)) {
        event.currentTarget.releasePointerCapture?.(event.pointerId);
      }

      if (!gesture.dragged) onTap();
    },
    [onTap],
  );

  // 浏览器接管了该手势（原生触摸滚动，或指针因故丢失）——这本身就是拖拽，
  // 因此绝不能再触发点按。
  const onPointerCancel = useCallback((event: ReactPointerEvent<HTMLElement>) => {
    const gesture = gestureRef.current;
    if (!gesture || gesture.pointerId !== event.pointerId) return;
    gestureRef.current = null;
  }, []);

  return { onPointerDown, onPointerMove, onPointerUp, onPointerCancel };
}
