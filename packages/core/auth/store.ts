// 本文件实现认证状态管理：登录/登出、用户信息维护，以及与埋点、工作区存储的联动。

import { create } from "zustand";
import type { User, StorageAdapter } from "../types";
import { identify as identifyAnalytics, resetAnalytics } from "../analytics";
import type { ApiClient } from "../api/client";
import { setCurrentWorkspace } from "../platform/workspace-storage";

/** 创建认证 store 所需的依赖配置。 */
export interface AuthStoreOptions {
  /** API 客户端，用于调用登录/登出/获取用户接口。 */
  api: ApiClient;
  /** 存储适配器，用于持久化 token 等。 */
  storage: StorageAdapter;
  /** 登录成功后的回调（可选）。 */
  onLogin?: () => void;
  /** 登出后的回调（可选）。 */
  onLogout?: () => void;
  /**
   * Cleanup for a session the server ended, as opposed to one the user did.
   * Defaults to `onLogout` — a shell only needs its own handler when some of
   * its logout teardown is too destructive for an expiry it did not ask for.
   */
  onSessionExpired?: () => void;
  /** When true, rely on HttpOnly cookies instead of localStorage for auth tokens. */
  cookieAuth?: boolean;
}

/** 认证状态机所处的阶段。 */
export type AuthStatus =
  | "authenticating"
  | "authenticated"
  | "unauthenticated"
  | "recovering";

/** 认证 store 的状态与动作。 */
export interface AuthState {
  /** 当前登录用户，未登录为 null。 */
  user: User | null;
  /** 是否正在加载用户信息。 */
  isLoading: boolean;
  /** 当前认证状态。 */
  status: AuthStatus;
  /** 重试代数，递增触发重新认证。 */
  retryGeneration: number;
  /**
   * The last transition to `unauthenticated` was the server rejecting our
   * credential, not the user asking to leave. Purely presentational — the
   * login page uses it to say why the session ended. Cleared by any
   * successful login and by an explicit logout.
   */
  expired: boolean;

  retryAuthentication: () => void;
  login: (account: string, password: string) => Promise<User>;
  loginWithGoogle: (code: string, redirectUri: string) => Promise<User>;
  loginWithToken: (token: string) => Promise<User>;
  logout: () => void;
  sessionExpired: () => void;
  setUser: (user: User) => void;
  refreshMe: () => Promise<void>;
}

/**
 * 创建认证 store。
 *
 * @param options - 认证依赖配置
 * @returns Zustand 认证 store
 */
export function createAuthStore(options: AuthStoreOptions) {
  const { api, storage, onLogin, onLogout, onSessionExpired, cookieAuth } =
    options;

  return create<AuthState>((set, get) => ({
    user: null,
    isLoading: true,
    status: "authenticating",
    retryGeneration: 0,
    expired: false,

    retryAuthentication: () => {
      set((state) => ({
        isLoading: true,
        status: "authenticating",
        retryGeneration: state.retryGeneration + 1,
      }));
    },

    login: async (account: string, password: string) => {
      const { token, user } = await api.login(account, password);
      if (!cookieAuth) { storage.setItem("multica_token", token); api.setToken(token); }
      onLogin?.();
      identifyAnalytics(user.id, { account: user.account, name: user.name });
      set({ user, isLoading: false, status: "authenticated", expired: false });
      return user;
    },

    loginWithGoogle: async (code: string, redirectUri: string) => {
      const { token, user } = await api.googleLogin(code, redirectUri);
      if (!cookieAuth) {
        storage.setItem("multica_token", token);
        api.setToken(token);
      }
      onLogin?.();
      identifyAnalytics(user.id, { email: user.email, name: user.name });
      set({ user, isLoading: false, status: "authenticated", expired: false });
      return user;
    },

    loginWithToken: async (token: string) => {
      storage.setItem("multica_token", token);
      api.setToken(token);
      const user = await api.getMe();
      onLogin?.();
      identifyAnalytics(user.id, { email: user.email, name: user.name });
      set({ user, isLoading: false, status: "authenticated", expired: false });
      return user;
    },

    logout: () => {
      if (cookieAuth) {
        // 清除服务端的 HttpOnly cookie。
        api.logout().catch(() => {});
      }
      storage.removeItem("multica_token");
      api.setToken(null);
      setCurrentWorkspace(null, null);
      resetAnalytics();
      onLogout?.();
      set({
        user: null,
        isLoading: false,
        status: "unauthenticated",
        expired: false,
      });
    },

    /**
     * The server rejected our credential (401). Tears the session down to
     * exactly the state a cold boot with a dead token lands in, so the shell
     * unmounts and the app shows the login page instead of staying up while
     * every request fails with an auth error the user cannot act on
     * (MUL-7028).
     *
     * No server round-trip: the credential is already dead, and `/auth/logout`
     * would be one more request to answer a 401 with. Idempotent, because a
     * session dies once but a screen full of in-flight requests all learn
     * about it separately.
     */
    sessionExpired: () => {
      // "Expired" is a claim about the user's own history, so only make it
      // when this client really did present a credential the server then
      // rejected: a live session, or a stored token left by an earlier one.
      // A first visit to /login 401s on the identity probe too, and telling
      // that person their session expired would be a lie. Read before the
      // teardown below removes the evidence.
      const hadCredential =
        get().status === "authenticated" ||
        storage.getItem("multica_token") !== null;

      // Dropping the rejected credential happens before the idempotence
      // guard, and unconditionally. A login attempt that 401s never leaves
      // `unauthenticated` — Desktop's deep link writes the token, calls
      // getMe, and gets rejected — so a guard placed first would return with
      // that invalid token still sitting in storage, to be replayed at the
      // next launch. Nothing below this point is safe to repeat; this is.
      storage.removeItem("multica_token");
      api.setToken(null);

      // Past here we are ending a session, which happens once no matter how
      // many in-flight requests learn the credential is dead — and does not
      // happen at all when there was no session to end.
      if (get().status === "unauthenticated") return;

      // Cookie mode leaves the workspace singleton alone: there the URL owns
      // workspace identity and the login route overwrites it on the next
      // entry. Mirrors AuthInitializer's boot-time rejection.
      if (!cookieAuth) setCurrentWorkspace(null, null);
      resetAnalytics();
      (onSessionExpired ?? onLogout)?.();
      set({
        user: null,
        isLoading: false,
        status: "unauthenticated",
        expired: hadCredential,
      });
    },

    setUser: (user: User) => {
      set({ user, isLoading: false, status: "authenticated", expired: false });
    },

    refreshMe: async () => {
      const user = await api.getMe();
      set({ user, isLoading: false, status: "authenticated", expired: false });
    },
  }));
}
