// 记录议题下哪些评论被折叠的 store：按议题 id 分组存"被折叠的评论 id"。
// 只存折叠的——默认（未记录）就是展开态。持久化到按工作区隔离的存储里。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../../platform/workspace-storage";
import { defaultStorage } from "../../platform/storage";

/**
 * 各议题评论的折叠状态。
 * 只存"已折叠"的评论 id——展开是默认态，没记录即未折叠。
 */
interface CommentCollapseStore {
  /** 每个议题当前被折叠的评论 id 列表。 */
  collapsedByIssue: Record<string, string[]>;
  /** 该议题下的这条评论当前是否被折叠。 */
  isCollapsed: (issueId: string, commentId: string) => boolean;
  /** 切换该议题下这条评论的折叠状态。 */
  toggle: (issueId: string, commentId: string) => void;
  /** 把该议题的折叠集合整体替换为 `commentIds`（全部折叠）。 */
  collapseAll: (issueId: string, commentIds: readonly string[]) => void;
  /** 清空该议题的折叠集合（全部展开）。 */
  expandAll: (issueId: string) => void;
}

export const useCommentCollapseStore = create<CommentCollapseStore>()(
  persist(
    (set, get) => ({
      collapsedByIssue: {},
      isCollapsed: (issueId, commentId) => {
        const ids = get().collapsedByIssue[issueId];
        return ids ? ids.includes(commentId) : false;
      },
      toggle: (issueId, commentId) =>
        set((s) => {
          const current = s.collapsedByIssue[issueId] ?? [];
          const isCurrentlyCollapsed = current.includes(commentId);
          if (isCurrentlyCollapsed) {
            const next = current.filter((id) => id !== commentId);
            if (next.length === 0) {
              // 折叠列表清空后干脆删掉这条议题的键，避免留空数组占位。
              const { [issueId]: _, ...rest } = s.collapsedByIssue;
              return { collapsedByIssue: rest };
            }
            return { collapsedByIssue: { ...s.collapsedByIssue, [issueId]: next } };
          }
          return { collapsedByIssue: { ...s.collapsedByIssue, [issueId]: [...current, commentId] } };
        }),
      collapseAll: (issueId, commentIds) =>
        set((s) => {
          // 没有可折叠的评论：展开态即默认态，直接清掉该议题的记录。
          if (commentIds.length === 0) {
            if (!(issueId in s.collapsedByIssue)) return s;
            const { [issueId]: _, ...rest } = s.collapsedByIssue;
            return { collapsedByIssue: rest };
          }
          // 用 Set 去重，防止评论 id 重复传入。
          return { collapsedByIssue: { ...s.collapsedByIssue, [issueId]: [...new Set(commentIds)] } };
        }),
      expandAll: (issueId) =>
        set((s) => {
          if (!(issueId in s.collapsedByIssue)) return s;
          const { [issueId]: _, ...rest } = s.collapsedByIssue;
          return { collapsedByIssue: rest };
        }),
    }),
    {
      name: "multica_comment_collapse",
      storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
    },
  ),
);

// 切换工作区后重新水合该工作区的折叠状态。
registerForWorkspaceRehydration(() => useCommentCollapseStore.persist.rehydrate());
