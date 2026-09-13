// 单行工具栏的"能放下几个"计算 Hook：尾部收进溢出触发器，测量用隐藏镜像行，避免测量与渲染互相反馈。
"use client";

import { useCallback, useLayoutEffect, useRef, useState } from "react";

/**
 * 单行工具栏的适配计算器，把尾部折叠进一个尾部触发器（视图栏；可复用于任何
 * "N 个 chip + 溢出菜单"的条带）。
 *
 * 调用方把每个候选渲染两次：一次真实渲染（切到 `fitCount`），一次渲染在挂在
 * `measureRef` 上的隐藏镜像行里（绝对定位、`visibility: hidden`、相同类名）。
 * 自然宽度从镜像行读取，所以适配结果从不依赖当前真实挂载的内容——测量与渲染
 * 之间没有反馈环。
 *
 * 规则：能与 `reserve` px 尾部外框并排放下的最长前缀获胜。`reserve` 必须覆盖
 * 所有始终在条目之后渲染的内容（触发器、菜单）——即使所有条目都放得下也会被
 * 减去，否则一行刚好放下全部条目时会把尾部外框挤出可视范围。
 *
 * 在容器 resize（ResizeObserver）和每次提交（标签/条目变化影响镜像行）后重测；
 * 状态只在答案真正变化时才更新，所以额外的测量趟数不产生额外开销。
 * @param count 候选条目数（镜像子元素必须匹配）。
 * @param gap 条目间水平间距（px，即容器的 `gap`）。
 * @param reserve 为溢出触发器预留的宽度（px）。
 * @returns containerRef、measureRef 和能放下的条目数 fitCount。
 */
export function useSingleRowFit({
  count,
  gap,
  reserve,
}: {
  /** 候选条目数（镜像子元素必须匹配）。 */
  count: number;
  /** 条目间水平间距（px，即容器的 `gap`）。 */
  gap: number;
  /** 为溢出触发器预留的宽度（px）。 */
  reserve: number;
}) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const measureRef = useRef<HTMLDivElement | null>(null);
  const [fitCount, setFitCount] = useState(count);

  const recompute = useCallback(() => {
    const container = containerRef.current;
    const mirror = measureRef.current;
    if (!container || !mirror) return;
    const available = container.clientWidth;
    const widths = Array.from(mirror.children).map(
      (child) => (child as HTMLElement).offsetWidth,
    );

    let used = 0;
    let next = 0;
    for (let i = 0; i < widths.length; i++) {
      const withItem = used + (i > 0 ? gap : 0) + widths[i]!;
      if (withItem + gap + reserve > available) break;
      used = withItem;
      next = i + 1;
    }
    setFitCount((current) => (current === next ? current : next));
  }, [gap, reserve]);

  // 每次提交后：镜像行刚用当前标签重渲染过。
  // eslint-disable-next-line react-hooks/exhaustive-deps -- 刻意在每次提交都测量；内部的 setState 有变化守卫
  useLayoutEffect(() => {
    recompute();
  });

  useLayoutEffect(() => {
    const container = containerRef.current;
    if (!container || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => recompute());
    observer.observe(container);
    return () => observer.disconnect();
  }, [recompute]);

  // 绝不多报（条目可能在两次渲染间减少）。
  return { containerRef, measureRef, fitCount: Math.min(fitCount, count) };
}
