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
import {
  createSessionRenewal,
  watchSessionActivity,
  type SessionRenewal,
} from "./session-renewal";
import type { CoreProviderProps, ClientIdentity } from "./types";
import type { StorageAdapter } from "../types/storage";
import { ClientUsageReporter } from "../client-usage";
import { configureShortcutPlatform } from "../shortcuts/platform";

// 模块级单例——首次渲染时创建一次，绝不重建。
// Vite HMR 保留模块级状态，所以它们能扛过热重载。
let initialized = false;
let authStore: ReturnType<typeof createAuthStore>;
let chatStore: ReturnType<typeof createChatStore>;
// Token mode only. Cookie-mode browsers have their session re-issued by the
// server on any authenticated request, so there is nothing for a client-side
// renewer to do there (MUL-7436).
let sessionRenewal: SessionRenewal | null = null;
// Named rather than positional: onLogin / onLogout / onSessionExpired are
// three adjacent `() => void`, and nothing but the argument order would tell
// them apart at the call site.
interface InitCoreOptions {
  apiBaseUrl: string;
  storage: StorageAdapter;
  onLogin?: () => void;
  onLogout?: () => void;
  onSessionExpired?: () => void;
  cookieAuth?: boolean;
  identity?: ClientIdentity;
  environmentId?: string;
}

function initCore({
  apiBaseUrl,
  storage,
  onLogin,
  onLogout,
  onSessionExpired,
  cookieAuth,
  identity,
  environmentId = "",
}: InitCoreOptions) {
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
    // A 401 mid-session has to end the session, not just drop the token.
    // Dropping it alone left the shell mounted with `user` still set, so no
    // shell ever showed the login page and every following request went out
    // unauthenticated — the user got a wall of "missing authorization"
    // toasts with no way forward (MUL-7028). The store action is idempotent,
    // so a screenful of parallel 401s is still one expiry.
    //
    // `authStore` is assigned a few lines below, synchronously, and this
    // callback can only run from a request — never before boot finishes.
    onUnauthorized: () => {
      authStore.getState().sessionExpired();
    },
    // Token mode only. Desktop runs one ApiClient per window over one shared
    // localStorage, so the credential has to be read through to storage rather
    // than cached per instance — otherwise a session renewed in one window
    // leaves the others sending a token that is on its way out (MUL-7436).
    getToken: cookieAuth
      ? undefined
      : () => storage.getItem("multica_token"),
    identity,
    environmentId,
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

  authStore = createAuthStore({
    api,
    storage,
    onLogin,
    onLogout,
    onSessionExpired,
    cookieAuth,
  });
  registerAuthStore(authStore);

  chatStore = createChatStore({ storage });
  registerChatStore(chatStore);

  if (!cookieAuth) {
    sessionRenewal = createSessionRenewal({
      api,
      storage,
      isAuthenticated: () => authStore.getState().status === "authenticated",
      logger: createLogger("auth"),
    });
  }

  initialized = true;
}

/** CoreProvider：初始化单例并组装 core 的 Provider 树。 */
export function CoreProvider({
  children,
  apiBaseUrl = "",
  environmentId = "",
  wsUrl = "ws://localhost:8080/ws",
  storage = defaultStorage,
  cookieAuth,
  onLogin,
  onLogout,
  onSessionExpired,
  identity,
  locale,
  resources,
}: CoreProviderProps) {
  // 只在首次渲染时初始化单例。依赖只读一次：apiBaseUrl、storage 与回调在应用
  // 启动时设定，运行期不变。
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useMemo(
    () =>
      initCore({
        apiBaseUrl,
        storage,
        onLogin,
        onLogout,
        onSessionExpired,
        cookieAuth,
        identity,
        environmentId,
      }),
    [],
  );

  // 仅客户端运行的卡死看门狗。服务端空操作、幂等。
  useEffect(() => {
    installFreezeWatchdog();
  }, []);

  // Sliding session renewal, driven by use rather than by a clock. The store
  // subscription covers the launch check: at mount the boot identity probe is
  // still running, so the first attempt that finds a live session is the one
  // that fires — and `maybeRenew` declines cheaply for every store update
  // after that until the server-supplied interval has elapsed.
  useEffect(() => {
    const renewal = sessionRenewal;
    if (!renewal) return;
    const unsubscribe = authStore.subscribe(() => renewal.maybeRenew());
    const stopWatching = watchSessionActivity(renewal);
    renewal.maybeRenew();
    return () => {
      unsubscribe();
      stopWatching();
    };
  }, []);

  // I18nProvider wraps everything else: server and client must use the same
  // (locale, resources) to avoid hydration mismatch. Language switching goes
  // through window.location.reload(), never client-side changeLanguage.
  const tree = (
    <QueryProvider>
      <AuthInitializer
        onLogin={onLogin}
        storage={storage}
        cookieAuth={cookieAuth}
        identity={identity}
        environmentId={environmentId}
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
