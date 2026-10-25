"use client";

// WebSocket 的 React Provider：负责建立/断开与当前工作区绑定的 WS 连接，
// 把连接实例通过 context 提供给 subscribe/onReconnect hooks，
// 并把 WS 事件统一同步进 React Query 缓存（useRealtimeSync）。
import {
  createContext,
  use,
  useEffect,
  useState,
  useCallback,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { WSClient } from "../api/ws-client";
import type { WSEventType, StorageAdapter, WSMessage } from "../types";
import type { ClientIdentity } from "../platform/types";
import type { StoreApi, UseBoundStore } from "zustand";
import type { AuthState } from "../auth/store";
import {
  getCurrentSlug,
  subscribeToCurrentSlug,
} from "../platform/workspace-storage";
import { createLogger } from "../logger";
import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

type EventHandler = (payload: unknown, actorId?: string, actorType?: string) => void;

interface WSContextValue {
  subscribeWorkspace: (slug: string, handler: (message: WSMessage) => void) => () => void;
  subscribe: (event: WSEventType, handler: EventHandler) => () => void;
  onReconnect: (callback: () => void) => () => void;
}

const WSContext = createContext<WSContextValue | null>(null);

export interface WSProviderProps {
  children: ReactNode;
  /** WebSocket 服务器地址（如 "ws://localhost:8080/ws"）。 */
  wsUrl: string;
  /** 平台创建的 auth store 实例。 */
  authStore: UseBoundStore<StoreApi<AuthState>>;
  /** 平台提供的存储适配器，用于读取认证 token。 */
  storage: StorageAdapter;
  /** 为 true 时用 HttpOnly cookie（而非 token query 参数）做 WS 认证。 */
  cookieAuth?: boolean;
  /** 向服务器标识本 WS 客户端（以升级 URL 上的 query 参数发送）。 */
  identity?: ClientIdentity;
  /** 显示 toast 消息的回调（平台相关，如 sonner）。 */
  onToast?: (message: string, type?: "info" | "error") => void;
}

export function WSProvider({
  children,
  wsUrl,
  authStore,
  storage,
  cookieAuth,
  identity,
  onToast,
}: WSProviderProps) {
  const user = authStore((s) => s.user);
  // 对当前工作区 slug 做响应式读取（packages/core/platform/workspace-storage.ts 里的
  // URL 驱动单例）。工作区切换时，下面 useEffect 会拆掉旧 WS 连接、打开一条绑定到
  // 新 slug 的新连接。SSR 快照为 `null`，因为这个 Provider 只在 CoreProvider 下的
  // 客户端渲染。
  const wsSlug = useSyncExternalStore(
    subscribeToCurrentSlug,
    getCurrentSlug,
    () => null,
  );
  const [wsClient, setWsClient] = useState<WSClient | null>(null);

  // 依赖 identity 的原语而非对象引用：父组件重渲染传一个新的
  // `{ platform, version, os }` 字面量时，只要 identity 本身没真变，就不拆线重连。
  const identityPlatform = identity?.platform;
  const identityVersion = identity?.version;
  const identityOS = identity?.os;

  useEffect(() => {
    if (!user || !wsSlug) return;

    // token 模式需要从存储取 token；cookie 模式的 HttpOnly cookie 会随 WS 升级
    // 请求自动带上。
    const token = cookieAuth ? null : storage.getItem("multica_token");
    if (!cookieAuth && !token) return;

    const ws = new WSClient(wsUrl, {
      logger: createLogger("ws"),
      cookieAuth,
      // Re-read on every (re)connect instead of pinning the token captured
      // below: a session renewed since this effect ran would otherwise keep
      // reconnecting with a credential that is on its way to expiring.
      getToken: cookieAuth ? undefined : () => storage.getItem("multica_token"),
      identity:
        identityPlatform || identityVersion || identityOS
          ? {
              platform: identityPlatform,
              version: identityVersion,
              os: identityOS,
            }
          : undefined,
    });
    ws.setAuth(token, wsSlug);
    setWsClient(ws);
    ws.connect();

    return () => {
      ws.disconnect();
      setWsClient(null);
    };
  }, [
    user,
    wsSlug,
    wsUrl,
    storage,
    cookieAuth,
    identityPlatform,
    identityVersion,
    identityOS,
  ]);

  const stores: RealtimeSyncStores = { authStore };

  // 集中的 WS → store 同步（用 hook state，WS 变化时自动重新订阅）。
  useRealtimeSync(wsClient, stores, onToast);

  const subscribe = useCallback(
    (event: WSEventType, handler: EventHandler) => {
      if (!wsClient) return () => {};
      return wsClient.on(event, handler);
    },
    [wsClient],
  );

  const onReconnectCb = useCallback(
    (callback: () => void) => {
      if (!wsClient) return () => {};
      return wsClient.onReconnect(callback);
    },
    [wsClient],
  );

  const subscribeWorkspace = useCallback((slug: string, handler: (message: WSMessage) => void) => {
    const token = cookieAuth ? null : storage.getItem("multica_token");
    if (!user || (!cookieAuth && !token)) return () => {};
    const source = new WSClient(wsUrl, { cookieAuth });
    source.setAuth(token, slug);
    source.onAny(handler);
    source.onReconnect(() => handler({ type: "issue:updated", payload: {} }));
    source.connect();
    return () => source.disconnect();
  }, [cookieAuth, storage, user, wsUrl]);

  return (
    <WSContext.Provider value={{ subscribe, subscribeWorkspace, onReconnect: onReconnectCb }}>
      {children}
    </WSContext.Provider>
  );
}

/** 在 WSProvider 内拿到订阅/重连接口；在 Provider 外使用会报错。 */
export function useWS() {
  const ctx = use(WSContext);
  if (!ctx) throw new Error("useWS must be used within WSProvider");
  return ctx;
}
