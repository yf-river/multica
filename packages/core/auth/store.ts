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
  /** 为 true 时，认证 token 依赖 HttpOnly cookie 而非 localStorage。 */
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

  retryAuthentication: () => void;
  login: (account: string, password: string) => Promise<User>;
  loginWithGoogle: (code: string, redirectUri: string) => Promise<User>;
  loginWithToken: (token: string) => Promise<User>;
  logout: () => void;
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
  const { api, storage, onLogin, onLogout, cookieAuth } = options;

  return create<AuthState>((set) => ({
    user: null,
    isLoading: true,
    status: "authenticating",
    retryGeneration: 0,

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
      set({ user, isLoading: false, status: "authenticated" });
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
      set({ user, isLoading: false, status: "authenticated" });
      return user;
    },

    loginWithToken: async (token: string) => {
      storage.setItem("multica_token", token);
      api.setToken(token);
      const user = await api.getMe();
      onLogin?.();
      identifyAnalytics(user.id, { email: user.email, name: user.name });
      set({ user, isLoading: false, status: "authenticated" });
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
      set({ user: null, isLoading: false, status: "unauthenticated" });
    },

    setUser: (user: User) => {
      set({ user, isLoading: false, status: "authenticated" });
    },

    refreshMe: async () => {
      const user = await api.getMe();
      set({ user, isLoading: false, status: "authenticated" });
    },
  }));
}
