// Chat 模块的公共出口：注册/访问单例 chat store，并导出 store 工厂、状态类型、
// 最近上下文等能力。
export { createChatStore, CHAT_MIN_W, CHAT_MIN_H, CHAT_DEFAULT_W, CHAT_DEFAULT_H, DRAFT_NEW_SESSION } from "./store";
export type { ChatStoreOptions, ChatState, ChatTimelineItem } from "./store";
export { useRecentContextStore, selectRecentContexts } from "./recent-context-store";
export type { RecentContextEntry, RecentContextType } from "./recent-context-store";

import type { createChatStore as CreateChatStoreFn } from "./store";

type ChatStoreInstance = ReturnType<typeof CreateChatStoreFn>;

/** 模块级单例——应用启动时经 `registerChatStore()` 设置一次。 */
let _store: ChatStoreInstance | null = null;

/**
 * 注册应用创建好的 chat store 实例。
 * 必须在应用启动、任何组件渲染之前调用。
 * @param store 应用创建的 chat store 实例。
 */
export function registerChatStore(store: ChatStoreInstance) {
  _store = store;
}

/**
 * 单例访问器——由已注册实例支撑的 Zustand hook。
 * 支持 `useChatStore(selector)` 与 `useChatStore.getState()`。
 */
export const useChatStore: ChatStoreInstance = new Proxy(
  (() => {}) as unknown as ChatStoreInstance,
  {
    apply(_target, _thisArg, args) {
      if (!_store)
        throw new Error(
          "Chat store not initialised — call registerChatStore() first",
        );
      return (_store as unknown as (...a: unknown[]) => unknown)(...args);
    },
    // 代理对 getState / setState 等属性的读取，转发到真实实例。
    get(_target, prop) {
      if (!_store) return undefined;
      return Reflect.get(_store, prop);
    },
  },
);
