// 议题详情页评论输入框的展示偏好 store：是否把底部评论栏"钉"在滚动视口上。
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../../platform/storage";

/**
 * 议题详情评论输入框的展示偏好。
 *
 * `sticky`（钉住）会把底部评论栏固定到滚动视口，浏览长时间线时始终可达。
 * 这是个人阅读习惯偏好（像主题色一样），所以用 `defaultStorage` 全局持久化，
 * 而不是按工作区分开存。
 */
interface CommentComposerStore {
  /** 评论栏是否钉在滚动视口底部。 */
  sticky: boolean;
  /** 切换钉住与否。 */
  toggleSticky: () => void;
}

export const useCommentComposerStore = create<CommentComposerStore>()(
  persist(
    (set) => ({
      sticky: true,
      toggleSticky: () => set((s) => ({ sticky: !s.sticky })),
    }),
    {
      name: "multica_comment_composer",
      storage: createJSONStorage(() => defaultStorage),
    },
  ),
);
