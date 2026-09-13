// 本文件提供 zustand persist 中间件与我们 StorageAdapter 依赖注入系统之间的
// 桥接。

import type { StateStorage } from "zustand/middleware";
import type { StorageAdapter } from "../types/storage";

/**
 * 把我们的 StorageAdapter 包装成 zustand persist 需要的 StateStorage。
 * 工作区范围（按 slug 分桶）的 store 请改用 createWorkspaceAwareStorage。
 *
 * @param adapter - 底层存储适配器
 * @returns zustand 可用的存储接口
 */
export function createPersistStorage(adapter: StorageAdapter): StateStorage {
  return {
    getItem: (key) => adapter.getItem(key),
    setItem: (key, value) => adapter.setItem(key, value),
    removeItem: (key) => adapter.removeItem(key),
  };
}
