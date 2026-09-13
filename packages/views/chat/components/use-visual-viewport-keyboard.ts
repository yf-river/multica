// 移动端软键盘几何信息 hook：当软键盘弹出时上报 visual viewport 的遮挡高度与可见高度，
// 让贴底的覆盖层能精确落在可视区域的底边上（处理 iOS Safari 键盘打开时的视口收缩与平移）。
"use client";

import { useEffect, useState } from "react";

/** 软键盘弹起时 visual viewport 的几何信息。 */
export interface VisualViewportKeyboard {
  /**
   * 从 LAYOUT viewport 底边向上到 visual viewport 底边的距离（px）。
   * 贴底的覆盖层往上偏移这个量，就能恰好压在可见底边上——无论浏览器
   * 把页面平移到了哪里。
   */
  occludedBottom: number;
  /** 可见高度（px）——覆盖层最高能到多高而不超出屏幕。 */
  viewportHeight: number;
}

/**
 * iOS Safari 的浏览器工具栏伸缩也会让 visual viewport 收缩几十个 px；
 * 只有至少这么高的缺口才被当作"键盘弹起"。任何移动键盘都远超这个值。
 */
const KEYBOARD_MIN_PX = 100;

/**
 * 上报软键盘打开期间的 visual viewport 几何信息；其余情况返回 null
 * （桌面端、键盘收起、捏合缩放、或浏览器不支持该 API）。
 *
 * 移动浏览器在键盘弹起时并不会收缩 layout viewport——它们收缩的是 *visual*
 * viewport，并且在 iOS 上还会平移页面把光标保持在可视范围内。因此贴底的覆盖层
 * 两个数都需要：平移让"到可视底边的距离"不等于键盘高度；收缩后的高度
 * 则限制了覆盖层最多能长多高。
 *
 * layout 高度基准必须是 documentElement.clientHeight，而不是 window.innerHeight：
 * iOS Safari 在键盘弹起时会让 innerHeight 和 visual viewport 一起收缩
 * （在 iOS 17.5 上实测 547 → 385），若用 innerHeight 作基准，恰好在键盘弹起时
 * 测到的键盘高度会趋近于零。
 */
export function useVisualViewportKeyboard(): VisualViewportKeyboard | null {
  const [keyboard, setKeyboard] = useState<VisualViewportKeyboard | null>(null);

  useEffect(() => {
    const vv = window.visualViewport;
    if (!vv) return;

    const update = () => {
      const zoomed = Math.abs(vv.scale - 1) > 0.01;
      const layoutHeight = document.documentElement.clientHeight;
      const keyboardHeight = layoutHeight - vv.height;
      const next: VisualViewportKeyboard | null =
        zoomed || keyboardHeight < KEYBOARD_MIN_PX
          ? null
          : {
              occludedBottom: Math.max(
                0,
                Math.round(layoutHeight - vv.height - vv.offsetTop),
              ),
              viewportHeight: Math.round(vv.height),
            };
      setKeyboard((prev) =>
        prev?.occludedBottom === next?.occludedBottom &&
        prev?.viewportHeight === next?.viewportHeight
          ? prev
          : next,
      );
    };

    update();
    vv.addEventListener("resize", update);
    vv.addEventListener("scroll", update);
    return () => {
      vv.removeEventListener("resize", update);
      vv.removeEventListener("scroll", update);
    };
  }, []);

  return keyboard;
}
