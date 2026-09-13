"use client";

// 记录用户上次使用的"创建议题"模式（手动创建 / Agent 创建），
// 并据此决定全局 `c` 快捷键与各入口该打开哪个弹窗。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../../platform/storage";
import { useModalStore } from "../../modals";

/**
 * 用户上次停留的"创建议题"模式。驱动全局 `c` 快捷键和弹窗内的模式切换——
 * 按 `c` 打开的是用户上次用的那个弹窗；任一弹窗里的切换按钮会更新这里，
 * 让偏好被记住。
 *
 * 刻意不做工作区隔离：用户对"我该用哪种方式提交议题"的心理偏好不会随工作区变化，
 * 所以它存在普通 localStorage，而不是像 quick-create-store / draft-store 那样
 * 用按工作区隔离的 StateStorage。
 */
export type CreateMode = "agent" | "manual";

interface CreateModeState {
  lastMode: CreateMode;
  setLastMode: (mode: CreateMode) => void;
}

export const useCreateModeStore = create<CreateModeState>()(
  persist(
    (set) => ({
      lastMode: "agent",
      setLastMode: (mode) => set({ lastMode: mode }),
    }),
    {
      name: "multica_create_mode",
      storage: createJSONStorage(() => defaultStorage),
    },
  ),
);

/**
 * 以"用户上次用过的模式"打开创建议题流程。通用入口（侧边栏按钮、命令面板、
 * `c` 快捷键）都走这里，让持久化的偏好真正生效；而那些需要预填仅手工表单字段
 * （状态、父议题 id）的入口仍直接打开"创建议题"，因为 Agent 模式无法承载这些预填。
 * @param data 传给创建弹窗的初始数据。
 */
export function openCreateIssueWithPreference(
  data?: Record<string, unknown> | null,
) {
  const lastMode = useCreateModeStore.getState().lastMode;
  const modal = lastMode === "manual" ? "create-issue" : "quick-create-issue";
  useModalStore.getState().open(modal, data ?? null);
}
