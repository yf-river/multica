import { create } from "zustand";
import type { User } from "@multica/core/types";
import { api, ApiError } from "./api";
import { clearToken, getToken, setToken } from "./secure-storage";
import { useWorkspaceStore } from "./workspace-store";

interface AuthState {
  user: User | null;
  isLoading: boolean;
  initialize: () => Promise<void>;
  login: (account: string, password: string) => Promise<User>;
  logout: () => Promise<void>;
  setUser: (user: User) => void;
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  isLoading: true,
  initialize: async () => {
    await useWorkspaceStore.getState().restoreSlug();
    const token = await getToken();
    if (!token) { set({ isLoading: false }); return; }
    api.setToken(token);
    try { set({ user: await api.getMe(), isLoading: false }); }
    catch (err) { if (err instanceof ApiError && err.status === 401) { await clearToken(); api.setToken(null); } set({ user: null, isLoading: false }); }
  },
  login: async (account, password) => { const { token, user } = await api.login(account, password); await setToken(token); api.setToken(token); set({ user }); return user; },
  logout: async () => { await clearToken(); api.setToken(null); set({ user: null }); },
  setUser: (user) => set({ user }),
}));
