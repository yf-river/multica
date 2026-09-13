// 本文件是 api 模块的对外出口：统一 re-export 各子模块，并提供全局 ApiClient 单例。
export {
  ApiClient,
  ApiError,
  clientErrorMessage,
  dispatchReasonCode,
  errorCode,
  PreviewTooLargeError,
  PreviewUnsupportedError,
} from "./client";
export type {
  ApiClientOptions,
  ClientUsageRequest,
} from "./client";
export { parseWithFallback, setSchemaLogger } from "./schema";
export type { ParseOptions } from "./schema";
export { DuplicateIssueErrorBodySchema } from "./schemas";
export type { DuplicateIssueErrorBody } from "./schemas";
export { WSClient } from "./ws-client";

import type { ApiClient as ApiClientType } from "./client";

/** 模块级单例——应用启动时通过 setApiInstance() 设置一次。 */
let _api: ApiClientType | null = null;

/** 设置全局共享的 ApiClient 单例（应用启动时调用一次）。 */
export function setApiInstance(instance: ApiClientType) {
  _api = instance;
}

/** 返回共享的 ApiClient 单例；尚未初始化时抛错。 */
export function getApi(): ApiClientType {
  if (!_api) throw new Error("ApiClient not initialised — call setApiInstance() first");
  return _api;
}

/**
 * 便捷 re-export：一个把每次属性访问都转发给单例的 Proxy，
 * 让既有调用点（`api.listIssues(...)`）无需改动即可继续工作。
 */
export const api = new Proxy({} as ApiClientType, {
  get(_target, prop, receiver) {
    // 允许初始化前做属性探测（HMR / React Refresh）
    if (!_api) return undefined;
    const value = Reflect.get(_api, prop, receiver);
    return typeof value === "function" ? value.bind(_api) : value;
  },
});
