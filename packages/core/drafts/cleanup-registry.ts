// 本文件实现「草稿清理注册表」：草稿 store 在模块加载时自注册一次，登出/删除
// 工作区时按注册表统一清理持久化与内存态，取代原先手工维护 key 清单的做法。

import type { StorageAdapter } from "../types/storage";
import { abortAll as abortAllUploads } from "./upload-coordinator";

/**
 * 草稿 store 的「自注册」注册表，取代 `platform/storage-cleanup.ts` 里那份
 * 手工维护的 `WORKSPACE_SCOPED_KEYS` 数组。
 *
 * 旧模型要求每个新草稿 store 记得把它的 persist key 追加到另一个文件里的
 * 清单中；这份清单会漂移，导致 `multica_comment_drafts`、`multica_quick_create`、
 * `multica_project_draft` 以及 chat 草稿附件/恢复等
 * key 在登出和删除工作区时没被清掉（持久层泄漏）；而内存态的 Zustand 单例
 * 在纯客户端登出跳转后会保留上一个用户的草稿（内存层泄漏，共享配置下跨用户）。
 *
 * store 在模块加载时通过 `registerDraftCleanup` 注册一次，清理时遍历注册表，
 * 这样新增草稿 store 再也不可能悄悄漏掉清理。
 */
export interface DraftCleanupEntry {
  /**
   * 基础 persist key（拼上工作区 slug 后缀之前）。对工作区维度 store 而言，
   * 真正的 localStorage key 是 `${storageKey}:${slug}`。
   */
  storageKey: string;
  /**
   * 为 true 表示经 `createWorkspaceAwareStorage` 持久化（key 带当前 slug
   * 后缀）；为 false 表示全局命名空间 key。
   */
  workspaceScoped: boolean;
  /**
   * 把该 store 的内存态重置为空。登出时调用，这样纯客户端导航（没有整页
   * 刷新去清模块单例）后，Zustand 单例不会再把上一个用户的草稿亮出来。
   */
  resetInMemory: () => void;
}

const entries = new Map<string, DraftCleanupEntry>();

/**
 * 注册一个草稿 store 供工作区/登出清理使用。按 key 幂等。
 *
 * @param entry - 该草稿 store 的清理条目
 */
export function registerDraftCleanup(entry: DraftCleanupEntry): void {
  entries.set(entry.storageKey, entry);
}

/**
 * 删除某个工作区 slug 下所有已注册草稿的持久化存储。
 *
 * 在工作区删除/离开（按 slug）以及登出（对该用户所属的每个工作区）时调用。
 * 全局命名空间的草稿 key 无论 slug 是什么都会被删——传入任意 slug 即可把它们
 * 各清一次。
 *
 * @param adapter - 存储适配器（浏览器是 localStorage）
 * @param slug - 目标工作区 slug
 */
export function clearRegisteredWorkspaceDrafts(
  adapter: StorageAdapter,
  slug: string,
): void {
  for (const entry of entries.values()) {
    if (entry.workspaceScoped) {
      adapter.removeItem(`${entry.storageKey}:${slug}`);
    } else {
      adapter.removeItem(entry.storageKey);
    }
  }
}

/**
 * 重置所有已注册草稿 store 的内存态。登出时、清掉认证之前调用，确保同标签页
 * 上的下一次登录不会看到上一份残留草稿。
 */
export function resetAllRegisteredDrafts(): void {
  // 先中止在途上传、再清草稿：草稿被清掉之后才落定的上传绝不能把占位符复活，
  // 它的字节也绝不能在下个会话里绑定成附件（MUL-5181）。
  abortAllUploads();
  for (const entry of entries.values()) {
    entry.resetInMemory();
  }
}

/**
 * Persist keys of every registered draft store, split by how they are
 * namespaced. Used to sweep drafts for workspaces whose slugs this process
 * never learned — a cold start that is rejected at the identity probe has no
 * workspace list to enumerate.
 */
export function registeredDraftKeys(): {
  workspaceScoped: string[];
  global: string[];
} {
  const workspaceScoped: string[] = [];
  const global: string[] = [];
  for (const entry of entries.values()) {
    (entry.workspaceScoped ? workspaceScoped : global).push(entry.storageKey);
  }
  return { workspaceScoped, global };
}

/** Test-only: drop all registrations. */
export function __clearDraftCleanupRegistryForTest(): void {
  entries.clear();
}

/** 仅供测试：查看已注册的 key。 */
export function __getRegisteredDraftKeysForTest(): string[] {
  return [...entries.keys()];
}
