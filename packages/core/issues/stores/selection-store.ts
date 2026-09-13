"use client";

// 议题多选状态的 store：在看板/列表/表格里选中一组议题，供批量操作使用。
import { create } from "zustand";

interface IssueSelectionState {
  /** 当前选中的议题 id 集合。 */
  selectedIds: Set<string>;
  /** 切换单个议题的选中状态。 */
  toggle: (id: string) => void;
  /** 把给定议题加入选中（保留已有选中）。 */
  select: (ids: string[]) => void;
  /** 把给定议题移出选中（其它选中保留）。 */
  deselect: (ids: string[]) => void;
  /** 清空全部选中。 */
  clear: () => void;
}

export const useIssueSelectionStore = create<IssueSelectionState>()((set) => ({
  selectedIds: new Set<string>(),
  toggle: (id) =>
    set((state) => {
      const next = new Set(state.selectedIds);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return { selectedIds: next };
    }),
  select: (ids) =>
    set((state) => {
      const next = new Set(state.selectedIds);
      for (const id of ids) next.add(id);
      return { selectedIds: next };
    }),
  deselect: (ids) =>
    set((state) => {
      const next = new Set(state.selectedIds);
      for (const id of ids) next.delete(id);
      return { selectedIds: next };
    }),
  clear: () => set({ selectedIds: new Set() }),
}));
