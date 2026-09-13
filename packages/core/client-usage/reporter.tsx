// 本文件实现客户端用量上报组件：在 Web 端每天上报一次安装 ID，尽力而为、绝不阻塞启动。

"use client";

import { useCallback, useEffect, useRef } from "react";
import { getApi } from "../api";
import { useAuthStore } from "../auth";
import type { StorageAdapter } from "../types/storage";
import type { ClientIdentity } from "../platform/types";
import { getOrCreateInstallId } from "./install-id";

const LAST_REPORTED_PREFIX = "multica_client_usage_last_reported";

/**
 * 把日期归一化为 UTC 日（`YYYY-MM-DD`），用于「每天上报一次」的去重判断。
 *
 * @param date - 日期对象（默认当前时间）
 * @returns UTC 日期字符串
 */
export function utcDay(date = new Date()): string {
  return date.toISOString().slice(0, 10);
}

/**
 * 客户端用量上报组件。挂在应用根部，检测到已登录的 Web 用户后，每天最多上报
 * 一次安装 ID；上报是尽力而为的，任何失败都静默吞掉，绝不打断应用启动。
 *
 * 不渲染任何 DOM（返回 null）。
 *
 * @param storage - 存储适配器，用于读写「最近上报日期」标记
 * @param identity - 客户端身份（平台等）
 */
export function ClientUsageReporter({
  storage,
  identity,
}: {
  storage: StorageAdapter;
  identity?: ClientIdentity;
}) {
  const userID = useAuthStore((state) => state.user?.id ?? null);
  const userIDRef = useRef(userID);
  userIDRef.current = userID;
  const inFlight = useRef(false);
  const rerun = useRef(false);

  const reportIfNeeded = useCallback(async () => {
    const platform = identity?.platform;
    const activeUserID = userIDRef.current;
    if (!activeUserID || platform !== "web") return;
    if (inFlight.current) {
      rerun.current = true;
      return;
    }

    const day = utcDay();
    try {
      const installID = getOrCreateInstallId(storage);
      const reportedKey = `${LAST_REPORTED_PREFIX}:${activeUserID}:${platform}:${installID}`;
      if (storage.getItem(reportedKey) === day) return;
      inFlight.current = true;
      await getApi().upsertClientUsage({ install_id: installID });
      storage.setItem(reportedKey, day);
    } catch {
      // 用量上报是尽力而为的，绝不能打断应用启动。
    } finally {
      inFlight.current = false;
      if (rerun.current) {
        rerun.current = false;
        void reportIfNeeded();
      }
    }
  }, [identity?.platform, storage]);

  useEffect(() => {
    void reportIfNeeded();
  }, [reportIfNeeded, userID]);

  useEffect(() => {
    const onFocus = () => void reportIfNeeded();
    const onVisibility = () => {
      if (document.visibilityState === "visible") void reportIfNeeded();
    };
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [reportIfNeeded]);

  return null;
}
