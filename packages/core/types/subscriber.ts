// 本文件定义「议题订阅者」类型：谁在订阅一个议题，以及为什么订阅。

/**
 * 某人订阅一个议题的原因。`delegated` 表示 agent 代表该成员创建了议题
 *（MUL-5483）——该成员从未亲自处理过它，所以 UI 需要向用户解释这条订阅的
 * 来历，并且其消息触达范围比直接原因更窄。
 *
 * 服务端驱动且故意开放式：API 把 `reason` 当作纯字符串解析，所以对不认识的
 * 值应按「直接订阅」处理，而不是把这一行丢掉。
 */
export type IssueSubscriberReason =
  | "creator"
  | "assignee"
  | "commenter"
  | "mentioned"
  | "manual"
  | "autopilot"
  | "delegated";

/** 议题订阅者记录：谁以什么身份、因什么原因订阅了哪个议题。 */
export interface IssueSubscriber {
  issue_id: string;
  user_type: "member" | "agent";
  user_id: string;
  reason: IssueSubscriberReason;
  created_at: string;
}
