"use client";

// 「源回填」提示的每用户「关闭次数」存储：提示每被关掉一次就 +1，用于决定
// 之后还要不要、以及隔多久再弹出。计数只落在浏览器 localStorage，不上服务端。
import { useCallback, useSyncExternalStore } from "react";

/**
 * 为什么用 localStorage 而不是服务端：
 *   - 这纯粹是前端 UX 的事（何时别再打扰），从不进统计或后端；
 *   - 若写数据库就得每次页面访问都写一次，对瞬态信号太浪费。
 *
 * 取舍：清浏览器缓存会清零计数，计数也不跨设备同步。两者都能接受——真正落到
 * 服务端的终点是 Submit / Skip；dismiss 只是「稍后再问」，换个新浏览器再问一次
 * 也无妨。
 */

const STORAGE_PREFIX = "multica.source_backfill.dismiss.";

function storageKey(userId: string): string {
  return `${STORAGE_PREFIX}${userId}`;
}

function readCount(userId: string | null | undefined): number {
  if (!userId) return 0;
  if (typeof window === "undefined") return 0;
  try {
    const raw = window.localStorage.getItem(storageKey(userId));
    if (!raw) return 0;
    const parsed = Number.parseInt(raw, 10);
    return Number.isFinite(parsed) && parsed >= 0 ? parsed : 0;
  } catch {
    return 0;
  }
}

function subscribe(callback: () => void): () => void {
  if (typeof window === "undefined") return () => {};
  // 别的标签页更新了该值时触发；同标签页内的更新走下面的 notify()。
  const handler = (e: StorageEvent) => {
    if (e.key && e.key.startsWith(STORAGE_PREFIX)) callback();
  };
  window.addEventListener("storage", handler);
  return () => window.removeEventListener("storage", handler);
}

// 共享的监听器注册表：让 React 树外调用 bumpDismissCount（罕见但可能）时，
// 也能唤醒订阅了同一 key 的 Hook。
const listeners = new Set<() => void>();
function notify(): void {
  for (const fn of listeners) fn();
}

/**
 * 读取并累加指定用户对「源回填」提示的关闭次数，结果对订阅本 Hook 的所有组件
 * 保持同步（含跨标签页）。
 *
 * @param userId 当前用户 id（为空时恒为 0，也不累加）
 * @returns [关闭次数, 把次数 +1 并通知订阅者的函数]
 */
export function useSourceBackfillDismissCount(
  userId: string | null | undefined,
): readonly [number, () => void] {
  const count = useSyncExternalStore(
    (cb) => {
      listeners.add(cb);
      const off = subscribe(cb);
      return () => {
        listeners.delete(cb);
        off();
      };
    },
    () => readCount(userId),
    () => 0,
  );

  const bump = useCallback(() => {
    if (!userId) return;
    if (typeof window === "undefined") return;
    try {
      const next = readCount(userId) + 1;
      window.localStorage.setItem(storageKey(userId), String(next));
      notify();
    } catch {
      // 隐私模式 Safari 等场景下 localStorage 写入可能失败——当作没发生。
      // 最坏情况是下次登录提示重新出现、不后退避让。
    }
  }, [userId]);

  return [count, bump] as const;
}

/** 仅供测试使用的清理函数——不通过包入口对外导出。 */
export function _resetSourceBackfillDismissCountForTests(): void {
  if (typeof window === "undefined") return;
  for (let i = window.localStorage.length - 1; i >= 0; i--) {
    const k = window.localStorage.key(i);
    if (k && k.startsWith(STORAGE_PREFIX)) window.localStorage.removeItem(k);
  }
  notify();
}
