// 记录哪些"已解决的线程"被用户临时展开，按议题 id 分组。
// 只存被展开的线程根 id——默认折叠成一条摘要条。
import { create } from "zustand";

/**
 * 哪些已解决线程被临时展开，按议题 id 分组的临时状态。
 * 只存"已展开"的线程根 id——默认收成摘要条。
 *
 * 刻意不做持久化（与 Linear 行为一致）：刷新后每条已解决线程都重新收成摘要条。
 * 放进 store 而不是议题详情组件自己的 state，是为了让命令面板的"全部折叠/全部展开
 * 评论"命令能从议题页之外驱动它。
 */
interface ResolvedExpandStore {
  expandedByIssue: Record<string, ReadonlySet<string>>;
  /** 设置该议题下某线程是否展开。 */
  setExpanded: (issueId: string, commentId: string, expand: boolean) => void;
  /** 一次性展开 `commentIds` 里的每个线程根（全部展开）。 */
  expandAll: (issueId: string, commentIds: readonly string[]) => void;
  /** 把所有展开的线程重新收成摘要条（全部折叠）。 */
  collapseAll: (issueId: string) => void;
}

/** 共享空集合：没有展开记录时返回它，避免读者因无关 store 写入而重渲染。 */
const EMPTY_EXPANDED: ReadonlySet<string> = new Set();

/** 删除某议题的展开记录并返回新对象（不修改入参）。 */
function withoutIssue(
  expandedByIssue: Record<string, ReadonlySet<string>>,
  issueId: string,
) {
  const { [issueId]: _, ...rest } = expandedByIssue;
  return rest;
}

export const useResolvedExpandStore = create<ResolvedExpandStore>()((set) => ({
  expandedByIssue: {},
  setExpanded: (issueId, commentId, expand) =>
    set((s) => {
      const current = s.expandedByIssue[issueId] ?? EMPTY_EXPANDED;
      if (current.has(commentId) === expand) return s;
      const next = new Set(current);
      if (expand) next.add(commentId);
      else next.delete(commentId);
      // 展开集合空了就把议题的记录整个删掉，不留空集合占位。
      if (next.size === 0) {
        return { expandedByIssue: withoutIssue(s.expandedByIssue, issueId) };
      }
      return { expandedByIssue: { ...s.expandedByIssue, [issueId]: next } };
    }),
  expandAll: (issueId, commentIds) =>
    set((s) => {
      // 没有可展开的线程则原样返回（保持引用稳定）。
      if (commentIds.length === 0) return s;
      const next = new Set(s.expandedByIssue[issueId] ?? EMPTY_EXPANDED);
      for (const id of commentIds) next.add(id);
      return { expandedByIssue: { ...s.expandedByIssue, [issueId]: next } };
    }),
  collapseAll: (issueId) =>
    set((s) => {
      if (!(issueId in s.expandedByIssue)) return s;
      return { expandedByIssue: withoutIssue(s.expandedByIssue, issueId) };
    }),
}));

/**
 * 读取某议题展开集合的稳定引用选择器（无记录时回退到共享空集合，
 * 这样无关的 store 写入不会让读者重渲染）。
 */
export function selectExpandedResolved(issueId: string) {
  return (s: ResolvedExpandStore): ReadonlySet<string> =>
    s.expandedByIssue[issueId] ?? EMPTY_EXPANDED;
}
