// 回复托管审查员（reviewer）时的接收人文案（GONG-63）。审查员只接受系统派发的审查
// 任务，任何给它的回复都转给任务当前负责人，因此输入框和发送后提示必须说清「这条回复
// 会交给谁」——尤其是负责人是人类成员或根本未设置时，那里不会有 Agent 启动。
//
// 预览（发送前）与回执（保存后）共用这里：同一个接收对象在两步里用同一套措辞，
// 不让用户在发送前后看到两种说法。
import type { CommentReviewReply } from "@multica/core/types";
import { blockedReasonLabel, blockedShortReasonLabel } from "./blocked-trigger-copy";
import type { useT } from "../i18n";

type IssuesT = ReturnType<typeof useT<"issues">>["t"];

/** 接收人名字缺失时的中性称谓，让插值句子始终成立。 */
function recipientName(reviewReply: CommentReviewReply, t: IssuesT): string {
  return (
    reviewReply.recipient_name?.trim() || t(($) => $.comment.review_reply_recipient_fallback)
  );
}

/**
 * 输入框预览用的接收人提示：这条草稿会通知谁、或者为什么通知不了。
 * @param reviewReply - 服务端预览返回的转交结果。
 * @param t - issues 命名空间的翻译函数。
 * @returns 一整句提示；没有可说的内容时返回 null。
 */
export function reviewReplyRecipientLabel(reviewReply: CommentReviewReply, t: IssuesT): string {
  if (reviewReply.status === "blocked") {
    return t(($) => $.comment.review_reply_blocked, {
      reason: blockedShortReasonLabel(reviewReply.reason_code ?? "", t),
    });
  }
  const name = recipientName(reviewReply, t);
  switch (reviewReply.recipient_type) {
    case "agent":
      return t(($) => $.comment.review_reply_to_assignee, { name });
    case "squad":
      return t(($) => $.comment.review_reply_to_squad, { name });
    case "member":
      return reviewReply.recipient_name?.trim()
        ? t(($) => $.comment.review_reply_to_member, { name })
        : t(($) => $.comment.review_reply_to_member_unnamed);
    default:
      return t(($) => $.comment.review_reply_no_assignee);
  }
}

/**
 * 发送后的回执：这条回复实际被谁接收、运行是否真的开始。
 * @param reviewReply - 创建/编辑评论响应里的转交结果。
 * @param t - issues 命名空间的翻译函数。
 * @returns 语气与文案；`status` 是预览态或无法判定时返回 null（不弹提示）。
 */
export function reviewReplyToast(
  reviewReply: CommentReviewReply,
  t: IssuesT,
): { tone: "info" | "warning"; text: string } | null {
  const name = recipientName(reviewReply, t);
  switch (reviewReply.status) {
    case "queued":
      return { tone: "info", text: t(($) => $.comment.review_reply_queued, { name }) };
    case "coalesced":
      return { tone: "info", text: t(($) => $.comment.review_reply_coalesced, { name }) };
    case "deferred":
      return { tone: "info", text: t(($) => $.comment.review_reply_deferred, { name }) };
    case "blocked":
      // 保存成功不等于通知成功：负责人不可调用或运行时不可用时如实说明。
      return {
        tone: "warning",
        text: t(($) => $.comment.review_reply_dispatch_failed, {
          reason: blockedReasonLabel(reviewReply.reason_code ?? "", t),
        }),
      };
    case "human_assignee":
      return {
        tone: "info",
        text: reviewReply.recipient_name?.trim()
          ? t(($) => $.comment.review_reply_human_saved, { name })
          : t(($) => $.comment.review_reply_human_saved_unnamed),
      };
    case "no_assignee":
      return { tone: "info", text: t(($) => $.comment.review_reply_no_assignee_saved) };
    case "skipped":
      return { tone: "info", text: t(($) => $.comment.review_reply_skipped_saved) };
    default:
      return null;
  }
}
