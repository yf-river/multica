"use client";

import { useMemo } from "react";
import { useOptionalNavigation } from "./context";

/**
 * navigation 模块：返回本次部署公共应用 URL 的 origin，平台说不出时（服务端
 * 渲染、桌面端运行时配置就绪前、或组件挂在 NavigationProvider 之外）为 null。
 *
 * 从适配器的 `getShareableUrl` 推导而非单独存一个适配器字段：「本应用的公共
 * URL」在各平台已经各自实现（web：当前 origin；桌面端：所连环境的 app URL），
 * 同一事实再抄一份就会多一份可以漂移的副本。
 *
 * 用于把「写成绝对 URL 的应用内目的地」（`https://<app-host>/acme/issues/1`）
 * 与真正的外部链接区分开。返回 `null` 时退化成旧行为——所有绝对 URL 都当
 * 外部链接——这正是缺 provider 时被容忍而不是抛错的原因。
 */
export function useAppOrigin(): string | null {
  const navigation = useOptionalNavigation();
  const getShareableUrl = navigation?.getShareableUrl;
  return useMemo(() => {
    if (!getShareableUrl) return null;
    try {
      return new URL(getShareableUrl("/")).origin;
    } catch {
      return null;
    }
  }, [getShareableUrl]);
}
