// 本文件是 auth 模块的对外出口，并对外提供认证 store 的单例注册与访问。

export { createAuthStore } from "./store";
export type { AuthStoreOptions, AuthState, AuthStatus } from "./store";
export { sanitizeNextUrl } from "./utils";

import type { createAuthStore as CreateAuthStoreFn } from "./store";

type AuthStoreInstance = ReturnType<typeof CreateAuthStoreFn>;

/** 模块级单例——应用启动时通过 `registerAuthStore()` 设置一次。 */
let _store: AuthStoreInstance | null = null;

/**
 * 注册由应用创建的认证 store 实例。
 * 必须在启动时、任何组件渲染之前调用。
 *
 * @param store - 认证 store 实例
 */
export function registerAuthStore(store: AuthStoreInstance) {
  _store = store;
}

/**
 * 单例访问器——一个由已注册实例支撑的 Zustand hook。
 * 支持 `useAuthStore(selector)` 和 `useAuthStore.getState()` 两种用法。
 */
export const useAuthStore: AuthStoreInstance = new Proxy(
  (() => {}) as unknown as AuthStoreInstance,
  {
    apply(_target, _thisArg, args) {
      if (!_store)
        throw new Error(
          "Auth store not initialised — call registerAuthStore() first",
        );
      return (_store as unknown as (...a: unknown[]) => unknown)(...args);
    },
    get(_target, prop) {
      // 在注册前也允许属性访问（HMR/React Refresh 场景）。
      if (!_store) return undefined;
      return Reflect.get(_store, prop);
    },
  },
);
