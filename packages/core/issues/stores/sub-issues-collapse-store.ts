// 议题详情页"子议题区"是否被折叠的 store：按议题 id 分组，只存被折叠的。
import { create } from "zustand";

/**
 * 某议题详情页的"子议题区"是否折叠，按议题 id 分组。
 * 只存"已折叠"的议题 id——默认展开。
 *
 * 刻意不做持久化（与 resolved-expand-store 同一约定）：状态放在 store 而非议题详情
 * 组件自己的 state 里，是为了离开议题再返回时能找回原状（MUL-4741 的返回恢复），
 * 而刷新页面则回到默认展开。
 */
interface SubIssuesCollapseStore {
  /** 被折叠的议题 id 集合。 */
  collapsedIssueIds: ReadonlySet<string>;
  /** 设置某议题的子议题区是否折叠。 */
  setCollapsed: (issueId: string, collapsed: boolean) => void;
}

export const useSubIssuesCollapseStore = create<SubIssuesCollapseStore>()(
  (set) => ({
    collapsedIssueIds: new Set<string>(),
    setCollapsed: (issueId, collapsed) =>
      set((s) => {
        // 状态没变化就返回原引用，避免多余通知。
        if (s.collapsedIssueIds.has(issueId) === collapsed) return s;
        const next = new Set(s.collapsedIssueIds);
        if (collapsed) next.add(issueId);
        else next.delete(issueId);
        return { collapsedIssueIds: next };
      }),
  }),
);
