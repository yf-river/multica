// 本文件实现 CoreProvider：把 core 的各类单例（API、auth、chat、持久化存储、
// 实时 WS）在首帧初始化，并组装 Provider 树（Query → Auth → 用量上报 → WS →
// i18n）。

"use client";

import { useEffect, useMemo } from "react";
import { ApiClient } from "../api/client";
import { installFreezeWatchdog } from "../diagnostics/freeze-watchdog";
import { setApiInstance, setSchemaLogger } from "../api";
import { createAuthStore, registerAuthStore } from "../auth";
import { createChatStore, registerChatStore } from "../chat";
import { I18nProvider } from "../i18n/react";
import { WSProvider } from "../realtime";
import { QueryProvider } from "../provider";
import { createLogger } from "../logger";
import { defaultStorage } from "./storage";
import { AuthInitializer } from "./auth-initializer";
import type { CoreProviderProps, ClientIdentity } from "./types";
import type { StorageAdapter } from "../types/storage";
import { ClientUsageReporter } from "../client-usage";
import { configureShortcutPlatform } from "../shortcuts/platform";

// 模块级单例——首次渲染时创建一次，绝不重建。
// Vite HMR 保留模块级状态，所以它们能扛过热重载。
let initialized = false;
let authStore: ReturnType<typeof createAuthStore>;
let chatStore: ReturnType<typeof createChatStore>;

/** 只执行一次的 core 单例初始化。 */
function initCore(
  apiBaseUrl: string,
  storage: StorageAdapter,
  onLogin?: () => void,
  onLogout?: () => void,
  cookieAuth?: boolean,
  identity?: ClientIdentity,
) {
  if (initialized) return;

  configureShortcutPlatform(
    identity?.os === "macos" ||
      identity?.os === "windows" ||
      identity?.os === "linux" ||
      identity?.os === "unknown"
      ? identity.os
      : null,
  );
  const api = new ApiClient(apiBaseUrl, {
    logger: createLogger("api"),
    onUnauthorized: () => {
      storage.removeItem("multica_token");
    },
    identity,
  });
  setApiInstance(api);
  setSchemaLogger(createLogger("api-schema"));

  // token 模式下从存储里水合 token。
  if (!cookieAuth) {
    const token = storage.getItem("multica_token");
    if (token) api.setToken(token);
  }
  // 工作区身份由 URL 驱动：[workspaceSlug] layout 解析 slug，并在挂载时调
  // setCurrentWorkspace(slug, wsId)。API 客户端从那个单例读 slug 以生成
  // X-Workspace-Slug 头。无需启动时从存储水合。

  authStore = createAuthStore({ api, storage, onLogin, onLogout, cookieAuth });
  registerAuthStore(authStore);

  chatStore = createChatStore({ storage });
  registerChatStore(chatStore);

  initialized = true;
}

/** CoreProvider：初始化单例并组装 core 的 Provider 树。 */
export function CoreProvider({
  children,
  apiBaseUrl = "",
  wsUrl = "ws://localhost:8080/ws",
  storage = defaultStorage,
  cookieAuth,
  onLogin,
  onLogout,
  identity,
  locale,
  resources,
}: CoreProviderProps) {
  // 只在首次渲染时初始化单例。依赖只读一次：apiBaseUrl、storage 与回调在应用
  // 启动时设定，运行期不变。
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useMemo(() => initCore(apiBaseUrl, storage, onLogin, onLogout, cookieAuth, identity), []);

  // 仅客户端运行的卡死看门狗。服务端空操作、幂等。
  useEffect(() => {
    installFreezeWatchdog();
  }, []);

  // I18nProvider 包住其余所有：服务端与客户端必须用同一份中文资源，避免
  // hydration 不一致。
  const tree = (
    <QueryProvider>
      <AuthInitializer
        onLogin={onLogin}
        onLogout={onLogout}
        storage={storage}
        cookieAuth={cookieAuth}
        identity={identity}
      >
        <ClientUsageReporter storage={storage} identity={identity} />
        <WSProvider
          wsUrl={wsUrl}
          authStore={authStore}
          storage={storage}
          cookieAuth={cookieAuth}
          identity={identity}
        >
          {children}
        </WSProvider>
      </AuthInitializer>
    </QueryProvider>
  );

  return (
    <I18nProvider locale={locale} resources={resources}>
      {tree}
    </I18nProvider>
  );
}
