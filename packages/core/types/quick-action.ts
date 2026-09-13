// 本文件定义「议题快捷动作（Quick Actions）」类型（MUL-5465）——workspace
// 级预设，说明对某个既有议题「呼叫谁、说什么」。
//
// 执行它并不是独立的派发通道：服务器渲染 prompt、发布一条携带目标提及标记
// 的 `quick_action` 评论，之后常规的 评论 → 提及 → 任务触发 链路接管。所以
// 运行结果的响应是一个带 `trigger_outcomes` 的 Comment，而不是专用形状。

/**
 * 作者在创建时声明的意图。
 *
 * - `public`  —— 面向团队。服务器要求目标必须每个成员都能调用，因此它天然
 *   对所有人可运行。
 * - `private` —— 只面向其创建者；允许任意目标，列表端点不把它返回给任何人。
 *
 * 这只是意图，不是授权决定——运行端点始终会重新校验调用权限。服务端驱动的
 * 枚举：用 `default` 分支兜底。
 */
export type QuickActionVisibility = "private" | "public";

/** 快捷动作的目标类型：agent 或 squad。 */
export type QuickActionAssigneeType = "agent" | "squad";

/** 快捷动作的生命周期状态。 */
export type QuickActionStatus = "active" | "archived";

/** 一条快捷动作。 */
export interface QuickAction {
  id: string;
  workspace_id: string;
  name: string;
  description: string;
  assignee_type: QuickActionAssigneeType | string;
  assignee_id: string;
  /** 逐字发送的 prompt——没有插值步骤。 */
  prompt: string;
  visibility: QuickActionVisibility | string;
  status: QuickActionStatus | string;
  last_used_at: string | null;
  use_count: number;
  created_by_id: string;
  created_at: string;
  updated_at: string;
  /** 绑定的 agent/squad 的显示名。目标已无法解析时缺失。 */
  target_name?: string;
  /**
   * 绑定的目标当前是否可被每个 workspace 成员调用。只是普通元数据而非
   * 判定——设置页把它显示在绑定旁，让「指向现已私有 agent 的 public 动作」
   * 一眼就能看出不对。
   */
  target_public: boolean;
  /** 绑定的 agent 或 squad 已被归档或删除。 */
  target_missing: boolean;
}

/** 新建快捷动作的请求体。 */
export interface CreateQuickActionRequest {
  name: string;
  description?: string;
  assignee_type: QuickActionAssigneeType;
  assignee_id: string;
  prompt: string;
  visibility?: QuickActionVisibility;
}

/** 更新快捷动作的请求体。 */
export interface UpdateQuickActionRequest {
  name?: string;
  description?: string;
  assignee_type?: QuickActionAssigneeType;
  assignee_id?: string;
  prompt?: string;
  visibility?: QuickActionVisibility;
  status?: QuickActionStatus;
}

/** 快捷动作列表接口响应。 */
export interface ListQuickActionsResponse {
  quick_actions: QuickAction[];
}

/**
 * 议题侧边栏在其余动作折叠进「更多」之前最多显示几个动作。这里的稀缺是
 * 结构性的：一个把全部 30 条都渲染出来的列表就不再是精选清单，而会变成
 * 没人读的菜单。
 */
export const QUICK_ACTION_SIDEBAR_LIMIT = 5;

/**
 * 匹配任意 `{{...}}`，与服务端写时的拒绝规则保持一致。
 *
 * 不支持模板化：每个变量都被认为可以命名为 agent 已从议题上下文得到的东西。
 * 该检查随功能保留下来，让习惯性的 token 不会逐字落到 agent 指令里——表单
 * 内联展示它，而不是等一个 400。
 */
export const QUICK_ACTION_TEMPLATE_TOKEN_RE = /\{\{[^}]*\}\}/;

/** 返回 prompt 里的第一个模板 token；没有时返回 null。 */
export function findQuickActionTemplateToken(prompt: string): string | null {
  return prompt.match(QUICK_ACTION_TEMPLATE_TOKEN_RE)?.[0] ?? null;
}
