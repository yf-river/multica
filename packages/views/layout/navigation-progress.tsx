"use client";

import { useEffect, useState } from "react";

import { useIsNavigating } from "../navigation";

/**
 * 布局模块：页面顶部 2px 的路由切换进度条。
 * 在 transition 包裹的 push/replace 进行中显示。刻意做成不确定进度——
 * 因为我们不知道下一条路由何时提交，只知道它快来了。
 *
 * 容器始终保持挂载，好让它用 200ms 淡出而不是一帧消失。内部的扫动条
 * 只在导航期间（加上淡出尾巴）挂载；若隐藏时仍让它的 `infinite` 关键帧
 * 动画继续跑，会在每个仪表盘视图上白白烧掉绘制。
 */
export function NavigationProgress() {
  const isNavigating = useIsNavigating();
  const [renderSweep, setRenderSweep] = useState(false);

  useEffect(() => {
    if (isNavigating) setRenderSweep(true);
  }, [isNavigating]);

  return (
    <div
      aria-hidden
      data-visible={isNavigating ? "true" : "false"}
      onTransitionEnd={(event) => {
        if (event.propertyName === "opacity" && !isNavigating) {
          setRenderSweep(false);
        }
      }}
      className="pointer-events-none absolute inset-x-0 top-0 z-50 h-0.5 overflow-hidden opacity-0 transition-opacity duration-200 data-[visible=true]:opacity-100"
    >
      {renderSweep && (
        <div
          className="h-full w-1/3 animate-nav-progress-sweep bg-brand"
          style={{
            boxShadow:
              "0 0 8px color-mix(in oklab, var(--brand) 60%, transparent), 0 0 2px color-mix(in oklab, var(--brand) 80%, transparent)",
          }}
        />
      )}
    </div>
  );
}
