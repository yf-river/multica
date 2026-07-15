import { create } from "zustand";
import type { User, StorageAdapter } from "../types";
import { identify as identifyAnalytics, resetAnalytics } from "../analytics";
import type { ApiClient } from "../api/client";
import { setCurrentWorkspace } from "../platform/workspace-storage";

interface AuthStoreOptions {
  api: ApiClient;
  storage: StorageAdapter;
  onLogin?: () => void;
  onLogout?: () => void | Promise<void>;
  /** When true, rely on HttpOnly cookies instead of localStorage for auth tokens. */
  cookieAuth?: boolean;
}

export interface AuthState {
  user: User | null;
  isLoading: boolean;

  login: (account: string, password: string) => Promise<User>;
  loginWithToken: (token: string) => Promise<User>;
  logout: () => Promise<void>;
  setUser: (user: User) => void;
}

export function createAuthStore(options: AuthStoreOptions) {
  const { api, storage, onLogin, onLogout, cookieAuth } = options;

  return create<AuthState>((set) => ({
    user: null,
    isLoading: true,

    login: async (account: string, password: string) => {
      const { token, user } = await api.login(account, password);
      if (!cookieAuth) {
        storage.setItem("multica_token", token);
        api.setToken(token);
      }
      onLogin?.();
      identifyAnalytics(user.id, { account: user.account, name: user.name });
      set({ user });
      return user;
    },

    loginWithToken: async (token: string) => {
      storage.setItem("multica_token", token);
      api.setToken(token);
      const user = await api.getMe();
      onLogin?.();
      identifyAnalytics(user.id, { account: user.account, name: user.name });
      set({ user, isLoading: false });
      return user;
    },

    logout: async () => {
      if (cookieAuth) {
        // The browser cannot clear the HttpOnly session itself. Do not claim
        // logout succeeded or erase recoverable client state until the server
        // has actually expired the cookie.
        await api.logout();
      }
      storage.removeItem("multica_token");
      api.setToken(null);
      setCurrentWorkspace(null, null);
      resetAnalytics();
      await onLogout?.();
      set({ user: null });
    },

    setUser: (user: User) => {
      set({ user });
    },
  }));
}

type AuthStoreInstance = ReturnType<typeof createAuthStore>;

let authStore: AuthStoreInstance | null = null;

export function registerAuthStore(store: AuthStoreInstance) {
  authStore = store;
}

export const useAuthStore: AuthStoreInstance = new Proxy(
  (() => {}) as unknown as AuthStoreInstance,
  {
    apply(_target, _thisArg, args) {
      if (!authStore) {
        throw new Error(
          "Auth store not initialised — call registerAuthStore() first",
        );
      }
      return (authStore as unknown as (...values: unknown[]) => unknown)(
        ...args,
      );
    },
    get(_target, property) {
      return authStore ? Reflect.get(authStore, property) : undefined;
    },
  },
);
