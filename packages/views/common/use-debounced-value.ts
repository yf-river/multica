// 防抖 Hook：返回"停止变化满 delayMs 后"的最终值，供高频输入驱动昂贵操作时踩刹车。
// 本文件只做防抖这一个纯函数式的事，不关心具体业务，任何调用方都可用。

"use client";

import { useEffect, useState } from "react";

/**
 * 防抖值 Hook：返回的值是 `value` 在"连续 `delayMs` 毫秒不再变化后"的最终快照。
 *
 * 用途：给昂贵的操作（如定时器编辑器里跟着表达式变化的服务器端预览查询）踩刹车——
 * 用户在输入框里连续打字时，每次都触发请求会把接口打爆；只有停下来超过延迟时间，
 * 才真正把最新值送出去。
 *
 * 实现原理：每次 `value` 或 `delayMs` 变化就重设一个定时器，之前的定时器被清理掉，
 * 所以只要输入还在持续，定时器就永远不会触发；一旦安静下来，`delayMs` 后定时器
 * 到点，把最新值写进 state 触发重渲染。
 * @param value 需要防抖的原始值（泛型，任何类型均可）。
 * @param delayMs 判定"停止变化"所需的静默时长（毫秒）。
 * @returns 停止变化后的值；在静默期结束前返回的是上一次的旧值。
 */
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(id);
  }, [value, delayMs]);
  return debounced;
}
