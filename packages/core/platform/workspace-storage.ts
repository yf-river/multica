// 本文件实现「工作区感知」的持久化命名空间：记录当前工作区、按工作区 slug
// 给存储 key 加命名空间、订阅 slug 变化，并在切换工作区时让各持久化 store
// 重新水合。

import type { StateStorage } from "zustand/middleware";
import type { StorageAdapter } from "../types/storage";

// 成对出现的模块级变量——由工作区 layout 总是同时设置/清除。
// _currentSlug 是主标识（与 URL 段一致）。
// _currentWsId 是派生值（来自 React Query 工作区列表），在需要 UUID 的
// query key 与路径内嵌 API 调用里使用。
let _currentSlug: string | null = null;
let _currentWsId: string | null = null;

const _rehydrateFns: Array<() => void> = [];
const _slugSubscribers = new Set<(slug: string | null) => void>();
let _pendingNotify = false;
let _pendingRehydrate = false;

/**
 * 更新当前工作区身份。这是「哪个工作区处于激活」的唯一真源；下游一切
 * （WS 连接、持久化命名空间、缓存 key 推导）都由此而来。
 *
 * 若 slug 真的变了，会触发两个副作用：
 *   1. 通知订阅者（例如 WSProvider 重新连接）。
 *   2. 所有已注册的持久化 store 从新 slug 的命名空间重新水合。
 *
 * 两个副作用在「slug 相等」时幂等：同一 slug 的重复调用是纯空操作。这在
 * 桌面端很重要——那里 N 个标签页各自挂载自己的 WorkspaceRouteLayout、各自
 * 天真地尝试同步；只有对给定 slug 的第一次调用做真活。
 *
 * 两个副作用都推迟到微任务执行：zustand persist 的水合与订阅通知最终都会调
 * setState()，而 React 19 禁止「渲染期间跨组件更新」。
 */
export function setCurrentWorkspace(slug: string | null, wsId: string | null) {
  if (_currentSlug === slug) {
    // slug 没变：没什么要水合的、没什么要通知的。接受（可能更新过的）wsId，
    // 供读取 UUID 镜像的消费方使用。
    _currentWsId = wsId;
    return;
  }
  _currentSlug = slug;
  _currentWsId = wsId;

  if (!_pendingNotify) {
    _pendingNotify = true;
    queueMicrotask(() => {
      _pendingNotify = false;
      const current = _currentSlug;
      for (const fn of _slugSubscribers) {
        fn(current);
      }
    });
  }

  if (!_pendingRehydrate) {
    _pendingRehydrate = true;
    queueMicrotask(() => {
      _pendingRehydrate = false;
      for (const fn of _rehydrateFns) {
        fn();
      }
    });
  }
}

/** 当前工作区 slug（来自 URL）。 */
export function getCurrentSlug(): string | null {
  return _currentSlug;
}

/** 当前工作区 UUID（由 slug + 工作区列表缓存推导）。 */
export function getCurrentWsId(): string | null {
  return _currentWsId;
}

/**
 * 订阅当前工作区 slug 的变化。返回退订函数。为 React 的 `useSyncExternalStore`
 * 设计（WSProvider 重连）。
 */
export function subscribeToCurrentSlug(
  fn: (slug: string | null) => void,
): () => void {
  _slugSubscribers.add(fn);
  return () => {
    _slugSubscribers.delete(fn);
  };
}

/** 注册某持久化 store 的水合函数，供切换工作区时调用。 */
export function registerForWorkspaceRehydration(fn: () => void) {
  _rehydrateFns.push(fn);
}

/**
 * 会自动用当前工作区 slug 给 key 加命名空间的存储。读取/写入时实时取
 * `_currentSlug`，因此会动态跟随工作区切换。
 *
 * 没有工作区激活时（例如 WorkspaceRouteLayout 调 setCurrentWorkspace 之前
 * zustand persist 的首次水合，或子组件 mount-effect 里的 setter 早于父 layout
 * effect 运行），读取返回 null、写入被丢弃——刻意不落到「无命名空间的裸 key」，
 * 那会把工作区范围数据跨工作区泄漏。持久化 store 在 setCurrentWorkspace 触发
 * 它们注册的水合函数后会读到真数据。
 */
export function createWorkspaceAwareStorage(adapter: StorageAdapter): StateStorage {
  return {
    getItem: (key) =>
      _currentSlug ? adapter.getItem(`${key}:${_currentSlug}`) : null,
    setItem: (key, value) => {
      if (_currentSlug) adapter.setItem(`${key}:${_currentSlug}`, value);
    },
    removeItem: (key) => {
      if (_currentSlug) adapter.removeItem(`${key}:${_currentSlug}`);
    },
  };
}
