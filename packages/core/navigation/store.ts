// 本文件实现「导航记忆」store：记住用户最后访问的工作区内部路径，供登录/
// 切换回来后跳转回原处。

"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerForWorkspaceRehydration,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";

// 这些前缀下的路径不该被记成「最后访问」：
//  - 认证流程（/login、/signup、/logout）
//  - 工作区之前的路由（/workspaces/new、/auth/、/invite/）
//  - 配对流程（/pair/）
const EXCLUDED_PREFIXES = [
  "/login",
  "/signup",
  "/logout",
  "/workspaces/",
  "/auth/",
  "/invite/",
  "/pair/",
];

/** 导航记忆 store 的状态结构。 */
interface NavigationState {
  /** 最后访问的工作区内部路径（初始为 null）。 */
  lastPath: string | null;
  /** 路径变化时调用：非排除前缀的路径会被记为 lastPath。 */
  onPathChange: (path: string) => void;
}

/** 导航记忆 store：只持久化 lastPath，按工作区 slug 分桶。 */
export const useNavigationStore = create<NavigationState>()(
  persist(
    (set) => ({
      lastPath: null,
      onPathChange: (path: string) => {
        if (!EXCLUDED_PREFIXES.some((prefix) => path.startsWith(prefix))) {
          set({ lastPath: path });
        }
      },
    }),
    {
      name: "multica_navigation",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
      partialize: (state) => ({ lastPath: state.lastPath }),
    },
  ),
);

// 工作区感知：当前工作区切换时重新读取 lastPath。
registerForWorkspaceRehydration(() => useNavigationStore.persist.rehydrate());
