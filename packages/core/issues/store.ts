"use client";

// 议题的客户端视图状态：记录当前激活（正在查看）的议题 id。
import { create } from "zustand";

/** 议题相关的客户端视图状态结构。 */
interface IssueClientState {
  /** 当前激活的议题 id，没有则 null。 */
  activeIssueId: string | null;
  /** 设置当前激活的议题 id。 */
  setActiveIssue: (id: string | null) => void;
}

/** 议题客户端状态的 Zustand store，持有当前激活议题 id。 */
export const useIssueStore = create<IssueClientState>((set) => ({
  activeIssueId: null,
  setActiveIssue: (id) => set({ activeIssueId: id }),
}));
