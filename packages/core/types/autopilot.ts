// 本文件定义「Autopilot（自动驾驶/定时 agent）」相关类型：配置、触发器、
// 运行记录、配额、协作者，以及 webhook 投递记录。

/** autopilot 的生命周期状态。 */
export type AutopilotStatus = "active" | "paused" | "archived";

/** 执行模式：创建议题 还是 仅运行。 */
export type AutopilotExecutionMode = "create_issue" | "run_only";

// `assignee_type` 选择支撑 autopilot 的多态执行者："agent" → assignee_id 引用
// agent(id)；"squad" → assignee_id 引用 squad(id)，派发时解析到
// squad.leader_id（MUL-2429，Path A）。旧服务器省略该字段——调用方应默认
// "agent"。
export type AutopilotAssigneeType = "agent" | "squad";

/** 触发器类型：定时 / webhook / API。 */
export type AutopilotTriggerKind = "schedule" | "webhook" | "api";

// `skipped` 由后端飞行前准入检查发出（派发时经办 agent 离线，MUL-1899）。
// 前端必须显式处理它——过去落入通用分支会把该次运行显示成仍在等待，掩盖
// 了空操作。
export type AutopilotRunStatus =
  | "issue_created"
  | "running"
  | "completed"
  | "failed"
  | "skipped";

/** 运行来源。 */
export type AutopilotRunSource = "schedule" | "manual" | "webhook" | "api";

/** 一条 autopilot 配置。 */
export interface Autopilot {
  id: string;
  workspace_id: string;
  title: string;
  description: string | null;
  project_id?: string | null;
  assignee_type: AutopilotAssigneeType;
  assignee_id: string;
  status: AutopilotStatus;
  // 系统暂停的附加机器可读原因。手动暂停与旧服务器为 null。
  pause_reason?: string | null;
  execution_mode: AutopilotExecutionMode;
  issue_title_template: string | null;
  created_by_type: string;
  created_by_id: string;
  last_run_at: string | null;
  created_at: string;
  updated_at: string;
  // 仅列表端点推导的字段；详情/创建/更新响应及旧服务器上缺失。只统计启用
  // 触发器。`trigger_kinds` 与 `last_run_status` 是服务端驱动字符串——未知值
  // 走通用回退渲染，绝不做穷尽 switch。
  trigger_kinds?: string[];
  next_run_at?: string | null;
  last_run_status?: string | null;
  // 列表端点返回 []；只有详情端点填充它。旧服务器上 undefined 视为空。
  subscribers?: AutopilotSubscriber[];
  // 请求用户能否编辑 / 删除 / 触发 / 管理该 autopilot（创建者、workspace
  // owner/admin 或被授予的协作者）。列表与详情响应都带；旧服务器上缺失——
  // undefined 视为「未知」而非「拒绝」（服务器才是把关者）。
  can_write?: boolean;
  // 请求用户能否管理协作者（访问）列表——比 can_write 更窄：仅创建者与
  // workspace owner/admin 持有，被授予的协作者没有。仅详情端点带；旧服务器
  // 上缺失（回退到 can_write）。
  can_manage_access?: boolean;
}

/** webhook 触发器的按事件过滤条件。 */
export interface WebhookEventFilter {
  event: string;
  actions?: string[];
}

/** autopilot 的订阅者（谁接收其结果通知）。 */
export interface AutopilotSubscriber {
  user_type: "member";
  user_id: string;
  created_at: string;
}

// 在隐式「创建者 ∪ owner/admin」集合之上，被显式授予某 autopilot 写权限的
// workspace 成员。目前仅限成员。
export interface AutopilotCollaborator {
  user_type: "member";
  user_id: string;
  granted_by: string;
  created_at: string;
}

/** 协作者列表接口响应。 */
export interface AutopilotCollaboratorsResponse {
  collaborators: AutopilotCollaborator[];
}

/** autopilot 的一个触发器。 */
export interface AutopilotTrigger {
  id: string;
  autopilot_id: string;
  kind: AutopilotTriggerKind;
  enabled: boolean;
  cron_expression: string | null;
  timezone: string | null;
  next_run_at: string | null;
  webhook_token: string | null;
  // webhook_path 由 webhook_token 在服务端推导（恒为
  // "/api/webhooks/autopilots/{token}"）。可选以便优雅兼容旧服务器。
  webhook_path?: string | null;
  // webhook_url 仅当服务端配置了 MULTICA_PUBLIC_URL 时才存在。客户端在该值
  // 缺失时回退为 getBaseUrl/origin + webhook_path 拼装。
  webhook_url?: string | null;
  label: string | null;
  // event_filters 仅对 webhook 触发器存在。null/空表示「接受所有事件」。
  event_filters?: WebhookEventFilter[] | null;
  last_fired_at: string | null;
  created_at: string;
  updated_at: string;
}

/** 一次 autopilot 运行。 */
export interface AutopilotRun {
  id: string;
  autopilot_id: string;
  trigger_id: string | null;
  source: AutopilotRunSource;
  status: AutopilotRunStatus;
  issue_id: string | null;
  task_id: string | null;
  triggered_at: string;
  completed_at: string | null;
  failure_reason: string | null;
  // 非成功运行（skipped/failed）的稳定、可本地化、枚举安全的分类，在服务端
  // 决策源持久化。「立即运行」UI 据此本地化，而不是回显原始英文原因。
  // 旧服务器省略。
  reason_code?: string;
  trigger_payload: unknown;
  result: unknown;
  created_at: string;
}

/** 新增订阅者的输入（不含服务端回填字段）。 */
export interface AutopilotSubscriberInput {
  user_type: "member";
  user_id: string;
}

/** 新建 autopilot 的请求体。 */
export interface CreateAutopilotRequest {
  title: string;
  description?: string;
  project_id?: string | null;
  // 线上可选——省略时服务器默认 "agent"，让旧客户端继续工作。
  assignee_type?: AutopilotAssigneeType;
  assignee_id: string;
  execution_mode: AutopilotExecutionMode;
  issue_title_template?: string;
  subscribers?: AutopilotSubscriberInput[];
}

/** 更新 autopilot 的请求体。 */
export interface UpdateAutopilotRequest {
  title?: string;
  description?: string | null;
  project_id?: string | null;
  // 更换经办人时把 `assignee_type` 与 `assignee_id` 一起传——服务器在做类型
  // 切换时两者都要。
  assignee_type?: AutopilotAssigneeType;
  assignee_id?: string;
  status?: AutopilotStatus;
  execution_mode?: AutopilotExecutionMode;
  issue_title_template?: string | null;
  // 出现时整体替换 autopilot 的订阅者模板；省略则不动。
  subscribers?: AutopilotSubscriberInput[];
}

/** 新建触发器的请求体。 */
export interface CreateAutopilotTriggerRequest {
  kind: AutopilotTriggerKind;
  cron_expression?: string;
  timezone?: string;
  label?: string;
  // event_filters 仅对 webhook 触发器有意义。
  event_filters?: WebhookEventFilter[];
}

/** 更新触发器的请求体。 */
export interface UpdateAutopilotTriggerRequest {
  enabled?: boolean;
  cron_expression?: string;
  timezone?: string;
  label?: string;
  // event_filters 仅对 webhook 触发器有意义。
  event_filters?: WebhookEventFilter[] | null;
}

/** cron 表达式预览接口响应。 */
export interface CronPreviewResponse {
  // 后续触发时间，RFC3339 UTC，升序。空数组表示表达式永不触发；`null` 是
  // 客户端侧「响应读不出来」（schema 漂移）的哨兵，调用方绝不能把它展示成
  // 「永不触发」。
  next_runs: string[] | null;
}

/** autopilot 列表接口响应。 */
export interface ListAutopilotsResponse {
  autopilots: Autopilot[];
  total: number;
}

/** autopilot 详情接口响应：配置 + 触发器 + 协作者。 */
export interface GetAutopilotResponse {
  autopilot: Autopilot;
  triggers: AutopilotTrigger[];
  // 被显式授予写权限的成员。旧服务器上缺失——undefined 视为空列表。
  collaborators?: AutopilotCollaborator[];
}

/** autopilot 运行列表接口响应。 */
export interface ListAutopilotRunsResponse {
  runs: AutopilotRun[];
  total: number;
}

// Webhook 投递枚举以服务器为准。前端对它的任何 switch 都必须 `default` 到
// 通用回退——见 CLAUDE.md 的 API Response Compatibility 规则。PR1 把 `skipped`
// 折叠进了 `dispatched`（跳过状态由运行本身携带）；未来服务器可能新增值。
export type WebhookDeliveryStatus =
  | "queued"
  | "dispatched"
  | "rejected"
  | "ignored"
  | "failed";

/** webhook 请求签名校验结果。 */
export type WebhookSignatureStatus =
  | "not_required"
  | "valid"
  | "invalid"
  | "missing";

/** 一次 webhook 投递记录。 */
export interface WebhookDelivery {
  id: string;
  workspace_id: string;
  autopilot_id: string;
  trigger_id: string;
  provider: string;
  event: string;
  dedupe_key: string | null;
  dedupe_source: string | null;
  signature_status: WebhookSignatureStatus;
  status: WebhookDeliveryStatus;
  attempt_count: number;
  dispatch_attempts: number;
  available_at: string;
  content_type: string | null;
  response_status: number | null;
  autopilot_run_id: string | null;
  replayed_from_delivery_id: string | null;
  error: string | null;
  reason_code: string | null;
  replay_idempotency_key: string | null;
  received_at: string;
  last_attempt_at: string;
  created_at: string;
  // 仅详情端点的字段。列表端点省略它们以控制线上体积（仅 raw_body 每条
  // 投递就可能达 256 KiB）。
  selected_headers?: Record<string, unknown> | null;
  raw_body?: string | null;
  response_body?: string | null;
}

/** webhook 投递列表接口响应。 */
export interface ListWebhookDeliveriesResponse {
  deliveries: WebhookDelivery[];
  total: number;
}
