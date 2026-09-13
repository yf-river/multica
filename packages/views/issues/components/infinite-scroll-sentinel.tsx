"use client";

// 本文件：InfiniteScrollSentinel —— 无限滚动哨兵。
// 用于"触底加载下一页"：在列表底部放一个占位元素并用 IntersectionObserver 盯梢，
// 一旦它滚进视口就回调 onVisible 请求下一页；loading 为 true 时在哨兵处展示
// 转圈与提示文案。

import { useEffect, useRef, type ReactNode } from "react";
import { Loader2 } from "lucide-react";

/** 每次"从不可见变为进入视口"触发一次回调的哨兵。回调/loading 变化都通过
 * ref/props 读取、不重建 observer，因此当一页加载完成、哨兵仍保持可见时，
 * 不会因为回调引用变化而重复触发。 */
export function InfiniteScrollSentinel({
  onVisible,
  loading,
  label,
  rootMargin = "100px",
  className = "flex items-center justify-center gap-1.5 py-2",
}: {
  onVisible: () => void;
  loading: boolean;
  /** loading 时显示在 spinner 旁边的文案（如"加载中…"）。给光秃秃的转圈一点
   * 上下文，慢分页时不至于让用户以为"卡死了"。 */
  label?: ReactNode;
  rootMargin?: string;
  className?: string;
}) {
  const sentinelRef = useRef<HTMLDivElement>(null);
  const onVisibleRef = useRef(onVisible);
  onVisibleRef.current = onVisible;

  useEffect(() => {
    const node = sentinelRef.current;
    if (!node) return;
    const observer = new IntersectionObserver(
      ([entry]) => { if (entry?.isIntersecting) onVisibleRef.current(); },
      { rootMargin },
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, [rootMargin]);

  return (
    <div ref={sentinelRef} className={className} aria-hidden>
      {loading && (
        <>
          <Loader2 className="size-3 animate-spin text-muted-foreground" />
          {label && (
            <span className="text-caption text-muted-foreground">{label}</span>
          )}
        </>
      )}
    </div>
  );
}
