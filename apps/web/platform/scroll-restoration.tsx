"use client";

/**
 * MUL-4741 滚动恢复协议的 Web 半边（桌面端半边在标签页协调器里）。浏览器只会恢复
 * 窗口自身的滚动位置——应用的滚动容器（`data-tab-scroll-root`）是内部 div 和虚拟化
 * 列表，浏览器毫不知情；没有这套机制，按「后退」回列表时每个列表都会停在顶部。
 */
import { useEffect, useMemo, type ReactNode } from "react";
import {
  ScrollRestorationProvider,
  type ScrollRestorationAdapter,
} from "@multica/views/platform";

/**
 * Web 半边实现（桌面端在标签页协调器）。
 *
 * 浏览器只会在前进/后退时恢复「窗口自己」的滚动位置——应用的可滚动容器
 * （`data-tab-scroll-root`）是它不了解的内部 div 与虚拟化列表，所以不做处理的话，
 * 后退到任何列表都会停在顶部。
 *
 * 采集是持续性的而非「导航前一刻」：App Router 没有可靠的「即将离开」钩子，
 * 因此一个捕获阶段的滚动监听器在用户滚动时持续记录每个标记容器的偏移量，
 * 以 `pathname::containerKey` 为键。回放与桌面端一样是拉取式 adapter——
 * 视图在挂载时通过 useRestoredScrollOffset / useRestoredScrollRef 读回偏移。
 *
 * 以 pathname（而非 history 条目）为键与桌面端 memento 语义一致：回到某个路由——
 * 无论是后退还是点新链接——都会恢复你上次在它上面的位置。这份 map 是模块级状态，
 * 按浏览器标签页隔离（Web 标签页就是真标签页），整页刷新时清空，交由浏览器自身的
 * 恢复机制接管。
 */
const savedOffsets = new Map<string, { top: number; height: number }>();

/**
 * 通用视图状态条目（桌面 memento 的第二类负载——例如「本路由的评论高亮深链已落地」）。
 * 视图在状态变化时用 setViewState 写入、挂载时读回。生命周期与上面的偏移量相同：
 * 模块级状态、按浏览器标签页隔离、整页刷新即重置。
 */
const savedViewState = new Map<string, string>();

/**
 * 刚通过 `adapter.get` 送出的键会在一小段窗口内抑制写入：恢复中的容器在挂载那一刻
 * 会被赋 scrollTop，若内容高度此时还没定型，浏览器会截断赋值并触发一次 scroll 事件——
 * 不抑制的话，这个被截断的值（或被截成 0）就会覆盖正要恢复的 memento。
 * 代价是这段窗口内一次真实的用户滚动会被丢弃，这是取舍里便宜的一端。
 */
const suppressedUntil = new Map<string, number>();
const RESTORE_SUPPRESS_MS = 1000;

/** 拼接内存键：`pathname::容器键`。 */
function mementoKey(pathname: string, containerKey: string): string {
  return `${pathname}::${containerKey}`;
}

/**
 * Web 滚动恢复 Provider（导出组件）：捕获滚动、提供读写 adapter。
 *
 * @param props.children - 子树。
 * @returns 提供滚动恢复能力上下文。
 */
export function WebScrollRestorationProvider({
  children,
}: {
  children: ReactNode;
}) {
  useEffect(() => {
    const onScroll = (e: Event) => {
      const el = e.target;
      if (!(el instanceof HTMLElement)) return;
      const raw = el.getAttribute("data-tab-scroll-root");
      if (raw === null) return;
      const key = mementoKey(window.location.pathname, raw || "main");
      const suppressed = suppressedUntil.get(key);
      if (suppressed !== undefined) {
        if (performance.now() < suppressed) return;
        suppressedUntil.delete(key);
      }
      if (el.scrollTop <= 0) {
        // 回到顶部就是默认状态——过期的偏移若不清掉，会在下次访问时「复活」。
        savedOffsets.delete(key);
      } else {
        savedOffsets.set(key, { top: el.scrollTop, height: el.scrollHeight });
      }
    };
    // scroll 事件不冒泡，但在捕获阶段会一路传播——所以窗口上一个监听器就能
    // 观察到所有容器的滚动。
    window.addEventListener("scroll", onScroll, {
      capture: true,
      passive: true,
    });
    return () =>
      window.removeEventListener("scroll", onScroll, { capture: true });
  }, []);

  const adapter = useMemo<ScrollRestorationAdapter>(
    () => ({
      get(containerKey) {
        const key = mementoKey(window.location.pathname, containerKey);
        const saved = savedOffsets.get(key);
        if (saved) {
          // 调用方即将恢复这个偏移量——先把 memento 保护起来，
          // 免于被恢复动作自身触发的「被截断的」scroll 事件覆盖。
          suppressedUntil.set(key, performance.now() + RESTORE_SUPPRESS_MS);
        }
        return saved;
      },
      getViewState(entryKey) {
        return savedViewState.get(
          mementoKey(window.location.pathname, entryKey),
        );
      },
      setViewState(entryKey, value) {
        const key = mementoKey(window.location.pathname, entryKey);
        if (value === undefined) savedViewState.delete(key);
        else savedViewState.set(key, value);
      },
    }),
    [],
  );

  return (
    <ScrollRestorationProvider adapter={adapter}>
      {children}
    </ScrollRestorationProvider>
  );
}
