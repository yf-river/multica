import { type RefObject, type CSSProperties, useEffect, useState, useCallback } from "react";

// 本文件提供「滚动渐隐」hook：当容器内容溢出并可滚动时，返回一个 maskImage
// 渐变，让内容在接近滚动边界处淡出，暗示「还能继续滚」。

/** 滚动渐隐的方向 */
export type ScrollFadeAxis = "vertical" | "horizontal";

/**
 * 根据滚动位置返回动态的 maskImage 样式：
 * - 停在开头 → 只淡出结尾
 * - 停在结尾 → 只淡出开头
 * - 停在中间 → 两端都淡出
 * - 没有溢出 → 返回 undefined（不加遮罩）
 *
 * @param ref 要监听的滚动容器 ref
 * @param fadeSize 渐隐带宽度/高度（px），默认 32
 * @param axis 滚动方向：'vertical'（默认）/ 'horizontal'
 * @returns 应用到容器上的 mask 样式；无需遮罩时返回 undefined
 */
export function useScrollFade(
  ref: RefObject<HTMLElement | null>,
  fadeSize = 32,
  axis: ScrollFadeAxis = "vertical",
): CSSProperties | undefined {
  const [fade, setFade] = useState<"none" | "start" | "end" | "both">("none");

  const update = useCallback(() => {
    const el = ref.current;
    if (!el) return;

    const position = axis === "horizontal" ? el.scrollLeft : el.scrollTop;
    const scrollSize = axis === "horizontal" ? el.scrollWidth : el.scrollHeight;
    const clientSize = axis === "horizontal" ? el.clientWidth : el.clientHeight;
    const scrollable = scrollSize - clientSize;

    // 没有溢出就不需要遮罩
    if (scrollable <= 0) {
      setFade((current) => current === "none" ? current : "none");
      return;
    }

    const atStart = position <= 1;
    const atEnd = position >= scrollable - 1;

    const next = atStart && atEnd ? "none" : atStart ? "end" : atEnd ? "start" : "both";
    setFade((current) => current === next ? current : next);
  }, [axis, ref]);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;

    // 下一帧先同步一次，避免初次进入时遮罩缺位
    let frame: number | null = null;
    const schedule = () => {
      if (frame !== null) return;
      frame = requestAnimationFrame(() => { frame = null; update(); });
    };
    schedule();

    el.addEventListener("scroll", update, { passive: true });
    const ro = new ResizeObserver(schedule);
    ro.observe(el);
    // ResizeObserver 只在容器自身尺寸变化时触发。当子内容在 flex / 自适应
    // 高度的父级里变高时，滚动范围会变大但视口不变——遮罩会一直是 "none"
    // 直到用户滚动一下。MutationObserver 监听 childList 可以捕获这类内容
    // 插入。
    const mo = new MutationObserver(schedule);
    mo.observe(el, { childList: true, subtree: true });

    return () => {
      if (frame !== null) cancelAnimationFrame(frame);
      el.removeEventListener("scroll", update);
      ro.disconnect();
      mo.disconnect();
    };
  }, [ref, update]);

  if (fade === "none") return undefined;

  const start =
    fade === "start" || fade === "both"
      ? `transparent 0%, black ${fadeSize}px`
      : "black 0%";
  const end =
    fade === "end" || fade === "both"
      ? `black calc(100% - ${fadeSize}px), transparent 100%`
      : "black 100%";

  const direction = axis === "horizontal" ? "right" : "bottom";
  const gradient = `linear-gradient(to ${direction}, ${start}, ${end})`;

  return {
    maskImage: gradient,
    WebkitMaskImage: gradient,
  };
}
