"use client";
// 本文件是内联盲文加载动画：直接渲染 unicode-animations 提供的逐帧字符，
// 不依赖 SVG/gif。用等宽字体 + 固定最小宽度保证换帧时周围文字不抖动。

import { useEffect, useState } from "react";
import spinners, { type BrailleSpinnerName } from "unicode-animations";

/**
 * UnicodeSpinner 组件的 props 定义。
 */
interface Props {
  /** 动画样式名，来自 unicode-animations 支持的盲文样式，默认 'braille' */
  name?: BrailleSpinnerName;
  /** 额外的 class 名 */
  className?: string;
  /** 暂停换帧但不卸载组件（例如外层状态被冻结时） */
  paused?: boolean;
}

// 内联渲染的盲文加载动画：每一帧是 `unicode-animations` 包里的一个 unicode 字符串。
// 按该动画自带的 interval 逐帧切换，渲染在等宽 monospace + 最小宽度 1ch 的 span 里，
// 这样不同宽度的帧不会让相邻文本来回抖动——宽度抖动正是这个组件存在的首要原因，
// 否则直接把原始字符串丢给 Tailwind 样式里就好了。
export function UnicodeSpinner({ name = "braille", className, paused }: Props) {
  const spec = spinners[name];
  const [frame, setFrame] = useState(0);

  useEffect(() => {
    if (paused) return;
    // 从第 0 帧重新开始，保证恢复时从头播放而不是接着停住的位置
    setFrame(0);
    const timer = setInterval(
      () => setFrame((f) => (f + 1) % spec.frames.length),
      spec.interval,
    );
    return () => clearInterval(timer);
  }, [name, paused, spec]);

  return (
    <span
      aria-hidden="true"
      className={className}
      style={{
        fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
        display: "inline-block",
        minWidth: "1ch",
        textAlign: "center",
        fontVariantNumeric: "tabular-nums",
      }}
    >
      {spec.frames[frame]}
    </span>
  );
}
