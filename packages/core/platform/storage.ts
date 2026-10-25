// 本文件提供浏览器/webview 客户端可用的默认 localStorage 适配器（SSR 安全）。

import type { StorageAdapter } from "../types/storage";

/** SSR 安全的 localStorage 适配器：浏览器里用 localStorage，服务端一律返回空。 */
export const defaultStorage: StorageAdapter = {
  getItem: (k) =>
    typeof window !== "undefined" ? localStorage.getItem(k) : null,
  setItem: (k, v) => {
    if (typeof window !== "undefined") localStorage.setItem(k, v);
  },
  removeItem: (k) => {
    if (typeof window !== "undefined") localStorage.removeItem(k);
  },
  keys: () => {
    if (typeof window === "undefined") return [];
    return Object.keys(localStorage);
  },
};
