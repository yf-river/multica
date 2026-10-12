// 本文件实现全局弹窗的 Zentand store：记录当前打开哪个弹窗、它携带的数据，
// 以及一个独立的「issue 数量上限恢复」提示状态。

"use client";

import { create } from "zustand";

/** 可打开的全局弹窗类型清单。 */
type ModalType =
  | "create-issue"
  | "quick-create-issue"
  | "create-project"
  | "create-squad"
  | "issue-set-parent"
  | "issue-add-child"
  | "issue-delete-confirm"
  | "issue-run-confirm"
  | null;

/** 全局弹窗 store 的状态结构。 */
interface ModalStore {
  /** 当前打开的弹窗类型，null 表示没有弹窗。 */
  modal: ModalType;
  /** 打开弹窗时附带的上下文数据。 */
  data: Record<string, unknown> | null;
  /** 触发「issue 数量上限恢复」提示的工作区 id（无提示时为 null）。 */
  issueLimitRecoveryWorkspaceId: string | null;
  /** 打开某个弹窗（可附带数据）。 */
  open: (modal: NonNullable<ModalType>, data?: Record<string, unknown> | null) => void;
  /** 关闭当前弹窗。 */
  close: () => void;
  /** 在某个工作区触发「issue 数量上限恢复」提示。 */
  showIssueLimitRecovery: (workspaceId: string) => void;
  /** 关掉「issue 数量上限恢复」提示。 */
  dismissIssueLimitRecovery: () => void;
}

/** 全局弹窗 store：一次只能开一个弹窗，`open` 会覆盖旧的。 */
export const useModalStore = create<ModalStore>((set) => ({
  modal: null,
  data: null,
  issueLimitRecoveryWorkspaceId: null,
  open: (modal, data = null) => set({ modal, data }),
  close: () => set({ modal: null, data: null }),
  showIssueLimitRecovery: (workspaceId) =>
    set({ issueLimitRecoveryWorkspaceId: workspaceId }),
  dismissIssueLimitRecovery: () =>
    set({ issueLimitRecoveryWorkspaceId: null }),
}));
