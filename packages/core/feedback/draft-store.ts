// 本文件创建「反馈编辑器草稿」的本地持久化 store：内容在刷新/切换后仍能找回。

import { createDraftStore } from "../drafts/create-draft-store";

/** 反馈草稿的内容结构。 */
interface FeedbackDraft {
  message: string;
}

/** 反馈草稿 store：写入 localStorage（key 为 multica_feedback_draft），空内容不保存。 */
export const useFeedbackDraftStore = createDraftStore<FeedbackDraft>({
  storageKey: "multica_feedback_draft",
  emptyData: { message: "" },
  hasMeaningful: (d) => !!d.message,
});
