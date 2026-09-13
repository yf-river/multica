import { useState, useEffect } from "react";
import { cn } from "../../lib/utils";

// 本文件是 Multica 品牌图标：用纯 CSS 的 clip-path 多边形画出一个 8 角星，
// 与 Multica logo 一致。颜色跟随 currentColor，自动适配明暗主题。

/**
 * MulticaIcon 组件的 props 定义。
 * 继承 span 元素的所有原生属性。
 */
interface MulticaIconProps extends React.ComponentProps<"span"> {
  /** 为 true 时播放入场旋转动画（只播一次） */
  animate?: boolean;
  /** 为 true 时关闭悬停旋转动画 */
  noSpin?: boolean;
  /** 为 true 时给图标加一个边框底框 */
  bordered?: boolean;
  /** 带边框时的图标尺寸档位：'sm'（默认）/ 'md' / 'lg' */
  size?: "sm" | "md" | "lg";
}

/** 带边框图标各尺寸对应的内边距与图标大小 */
const borderedSizes = {
  sm: { wrapper: "p-1.5", icon: "size-3.5" },
  md: { wrapper: "p-2", icon: "size-4" },
  lg: { wrapper: "p-2.5", icon: "size-5" },
};

/**
 * MulticaIcon：纯 CSS 实现的 Multica logo 八角星图标。
 * 颜色用 currentColor，因此会自动适配明暗主题。
 * clip-path 多边形坐标是从原始 SVG 路径坐标描出来的。
 *
 * @param props 见 MulticaIconProps 定义（其余 span 属性会透传到元素上）
 * @returns 图标 span 元素
 */
export function MulticaIcon({
  className,
  animate = false,
  noSpin = false,
  bordered = false,
  size = "sm",
  ...props
}: MulticaIconProps) {
  // 入场动画只播一次：播完（600ms）后标记 entranceDone，之后不再播入场动画
  const [entranceDone, setEntranceDone] = useState(!animate);

  useEffect(() => {
    if (!animate) return;
    const timer = setTimeout(() => setEntranceDone(true), 600);
    return () => clearTimeout(timer);
  }, [animate]);

  // 八角星的裁切多边形：12 个点的坐标轨迹，从官方 SVG 里描出来的
  const clipPath = `polygon(
    45% 62.1%, 45% 100%, 55% 100%, 55% 62.1%,
    81.8% 88.9%, 88.9% 81.8%, 62.1% 55%, 100% 55%,
    100% 45%, 62.1% 45%, 88.9% 18.2%, 81.8% 11.1%,
    55% 37.9%, 55% 0%, 45% 0%, 45% 37.9%,
    18.2% 11.1%, 11.1% 18.2%, 37.9% 45%, 0% 45%,
    0% 55%, 37.9% 55%, 11.1% 81.8%, 18.2% 88.9%
  )`;

  if (bordered) {
    const sizeConfig = borderedSizes[size];
    return (
      <span
        className={cn(
          "inline-flex items-center justify-center border border-border rounded-md",
          sizeConfig.wrapper,
          className
        )}
        aria-hidden="true"
        {...props}
      >
        <span
          className={cn(
            "block",
            sizeConfig.icon,
            !entranceDone && "animate-entrance-spin",
            entranceDone && !noSpin && "hover:animate-spin"
          )}
        >
          <span
            className="block size-full bg-current"
            style={{ clipPath }}
          />
        </span>
      </span>
    );
  }

  return (
    <span
      className={cn(
        "inline-block size-[1em]",
        !entranceDone && "animate-entrance-spin",
        entranceDone && !noSpin && "hover:animate-spin",
        className
      )}
      aria-hidden="true"
      {...props}
    >
      <span
        className="block size-full bg-current"
        style={{ clipPath }}
      />
    </span>
  );
}
