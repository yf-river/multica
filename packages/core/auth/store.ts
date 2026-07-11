import { create } from "zustand";
import type { User, StorageAdapter } from "../types";
import { identify as identifyAnalytics, resetAnalytics } from "../analytics";
import { ApiError, type ApiClient } from "../api/client";
import { setCurrentWorkspace } from "../platform/workspace-storage";

export interface AuthStoreOptions {
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

  initialize: () => Promise<void>;
  login: (account: string, password: string) => Promise<User>;
  loginWithToken: (token: string) => Promise<User>;
  logout: () => Promise<void>;
  setUser: (user: User) => void;
  refreshMe: () => Promise<void>;
}

export function createAuthStore(options: AuthStoreOptions) {
  const { api, storage, onLogin, onLogout, cookieAuth } = options;

  return create<AuthState>((set) => ({
    user: null,
    isLoading: true,

    initialize: async () => {
      if (cookieAuth) {
        // In cookie mode, the HttpOnly cookie is sent automatically.
        // Try to fetch the current user — if the cookie exists the server will accept it.
        try {
          const user = await api.getMe();
          set({ user, isLoading: false });
        } catch {
          set({ user: null, isLoading: false });
        }
        return;
      }

      // Desktop token mode reads its session from localStorage.
      const token = storage.getItem("multica_token");
      if (!token) {
        set({ isLoading: false });
        return;
      }

      api.setToken(token);

      try {
        const user = await api.getMe();
        set({ user, isLoading: false });
      } catch (err) {
        // Only clear the stored token on a genuine auth failure (401). For
        // transient errors — network blips, backend rolling restarts, 5xx,
        // aborted fetches — keep the token so the next initialize() (next
        // page load or focus-refresh) can retry. The 401 path's token
        // cleanup is handled upstream by ApiClient.handleUnauthorized via
        // the onUnauthorized callback; we only need to reset the in-memory
        // user + workspace state here.
        if (err instanceof ApiError && err.status === 401) {
          setCurrentWorkspace(null, null);
        }
        set({ user: null, isLoading: false });
      }
    },

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

    refreshMe: async () => {
      const user = await api.getMe();
      set({ user });
    },
  }));
}
