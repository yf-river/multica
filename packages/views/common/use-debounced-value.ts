// 防抖 Hook：返回"停止变化满 delayMs 后"的最终值，供高频输入驱动昂贵操作时踩刹车。
// 本文件只做防抖这一个纯函数式的事，不关心具体业务，任何调用方都可用。

"use client";

import { useEffect, useState } from "react";

/** The last settled value; disabling cancels pending updates and retains it. */
export function useDebouncedValue<T>(value: T, delayMs: number, enabled = true): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    if (!enabled) return;
    const id = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(id);
  }, [value, delayMs, enabled]);
  return debounced;
}
