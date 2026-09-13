"use client";

import { create } from "zustand";

/**
 * search 模块：全局搜索面板开合状态的 store。
 * 搜索面板的开关不依赖路由，任何地方（侧边栏按钮、快捷键）都能打开它，
 * 所以用模块级 zustand 单例而不是组件内 state。
 */
interface SearchStore {
  /** 搜索面板是否打开。 */
  open: boolean;
  /** 打开或关闭搜索面板。 */
  setOpen: (open: boolean) => void;
  /** 反转当前开合状态。 */
  toggle: () => void;
}

/** 全局搜索面板开合状态的 zustand store 单例。 */
export const useSearchStore = create<SearchStore>((set) => ({
  open: false,
  setOpen: (open) => set({ open }),
  toggle: () => set((s) => ({ open: !s.open })),
}));
