export const FEEDBACK_KINDS = ["bug", "feature", "general", "praise"] as const;

export type FeedbackKind = (typeof FEEDBACK_KINDS)[number];

export interface FeedbackContext {
  page?: string;
  issue_id?: string;
  task_id?: string;
  metadata?: Record<string, unknown>;
}

export interface CreateFeedbackResponse {
  id: string;
  created_at: string;
}

export interface DesktopRouteErrorFeedbackContext {
  kind: "desktop_route_error";
  trigger: string;
  error: { name: string; message: string; stack?: string };
}
