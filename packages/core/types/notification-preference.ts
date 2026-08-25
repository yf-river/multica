export type NotificationGroupKey =
  | "assignments"
  | "status_changes"
  | "comments"
  | "updates"
  | "agent_activity"
  | "system_notifications";

export type NotificationPreferences = Partial<Record<NotificationGroupKey, "all" | "muted">>;
