// 本文件定义用户反馈的类型：反馈种类、携带的上下文与创建反馈的返回结构。

/** 反馈的四种类型：Bug、功能建议、一般问题、表扬。 */
export const FEEDBACK_KINDS = ["bug", "feature", "general", "praise"] as const;

/** 单个反馈类型，是 FEEDBACK_KINDS 元素组成的联合类型。 */
export type FeedbackKind = (typeof FEEDBACK_KINDS)[number];

/** 反馈发生时携带的上下文信息，帮助开发定位问题。 */
export interface FeedbackContext {
  /** 反馈发生时的页面地址。 */
  page?: string;
  /** 相关 issue 的 id（如有）。 */
  issue_id?: string;
  /** 相关任务 id（如有）。 */
  task_id?: string;
  /** 其他任意元数据。 */
  metadata?: Record<string, unknown>;
}

/** 创建反馈成功后服务端返回的结构。 */
export interface CreateFeedbackResponse {
  id: string;
  created_at: string;
}

/** 桌面端路由错误场景下的反馈上下文：附带触发路由与错误详情。 */
export interface DesktopRouteErrorFeedbackContext {
  kind: "desktop_route_error";
  trigger: string;
  error: { name: string; message: string; stack?: string };
}
