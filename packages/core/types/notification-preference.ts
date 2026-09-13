// 本文件定义通知偏好相关的类型：把通知按「分组」归类，并为每个分组配置
// 收不接收通知。

/** 通知分组键：按通知来源/语义划分的类别名。 */
export type NotificationGroupKey =
  | "assignments"
  | "status_changes"
  | "comments"
  | "mentions"
  | "updates"
  | "agent_activity"
  | "system_notifications";

/** 某一通知分组的值：全部接收 or 静默（不接收）。 */
export type NotificationGroupValue = "all" | "muted";

/** 通知偏好：以分组键为索引的部分配置（没配置的分组用默认策略）。 */
export type NotificationPreferences = Partial<Record<NotificationGroupKey, NotificationGroupValue>>;

/** 通知偏好接口响应：属于哪个 workspace + 该 workspace 的偏好配置。 */
export interface NotificationPreferenceResponse {
  workspace_id: string;
  preferences: NotificationPreferences;
}
