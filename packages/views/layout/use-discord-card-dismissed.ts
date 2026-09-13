"use client";

import { useCallback, useSyncExternalStore } from "react";

/**
 * 布局模块：记录「是否已关闭侧边栏的 Discord 卡片」的 Hook。
 * 每个用户独立记录，用于隐藏不再展示的加入 Discord 推广卡片。
 *
 * 这个标志存在 `localStorage` 而不是服务器，因为它纯粹是前端 UX 偏好
 * （不再展示这个推广），不会上报分析或后端。按用户 id 区分，避免共用
 * 同一个浏览器账号的两个人互相继承对方的关闭状态。
 *
 * 取舍：清除浏览器存储后卡片会重新出现，且该标志不会跨设备同步。
 * 对一个低价值的推广来说，这两点都可接受。
 */

const STORAGE_PREFIX = "multica.discord_card.dismissed.";

/** 拼接某个用户在 localStorage 里的存储键名。 */
function storageKey(userId: string): string {
  return `${STORAGE_PREFIX}${userId}`;
}

function readDismissed(userId: string | null | undefined): boolean {
  if (!userId) return false;
  if (typeof window === "undefined") return false;
  try {
    return window.localStorage.getItem(storageKey(userId)) === "1";
  } catch {
    return false;
  }
}

// 共享的监听器注册表，让同标签页里的写入能唤醒所有订阅的 hook。
const listeners = new Set<() => void>();
function notify(): void {
  for (const fn of listeners) fn();
}

function subscribe(callback: () => void): () => void {
  listeners.add(callback);
  // 跨标签页的更新通过 storage 事件到达。
  const handler =
    typeof window === "undefined"
      ? undefined
      : (e: StorageEvent) => {
          if (e.key && e.key.startsWith(STORAGE_PREFIX)) callback();
        };
  if (handler) window.addEventListener("storage", handler);
  return () => {
    listeners.delete(callback);
    if (handler) window.removeEventListener("storage", handler);
  };
}

/**
 * 返回 `[dismissed, dismiss]` 元组：当前是否已关闭，以及用于关闭的函数。
 * 没有用户 id 时，`dismiss` 是空操作（不产生任何效果）。
 * @param userId 当前用户 id，用于按用户区分关闭状态。
 */
export function useDiscordCardDismissed(
  userId: string | null | undefined,
): readonly [boolean, () => void] {
  const dismissed = useSyncExternalStore(
    subscribe,
    () => readDismissed(userId),
    () => false,
  );

  const dismiss = useCallback(() => {
    if (!userId) return;
    if (typeof window === "undefined") return;
    try {
      window.localStorage.setItem(storageKey(userId), "1");
      notify();
    } catch {
      // Safari 隐私模式等场景下 localStorage 可能抛异常——当作空操作处理。
      // 最坏情况只是下次挂载时卡片重新出现。
    }
  }, [userId]);

  return [dismissed, dismiss] as const;
}
