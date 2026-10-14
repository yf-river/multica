// 本文件定义「聊天（Chat）」相关类型：会话、消息、快捷回复建议、挂起/排队
// 任务、草稿恢复等，供与 agent 对话的 IM 风格界面使用。

import type { AgentTask } from "./agent";

/** 用户置顶的「快捷 agent」（聊天列表顶栏用）。 */
export interface ChatPinnedAgent {
  agent_id: string;
  position: number;
}

/**
 * 聊天消息的种类。增量式（MUL-4351）：服务器始终发送具体值，但缺失/未知值
 * 一律按 "message" 处理，让旧服务器或未来种类都不破坏渲染。
 * - "message"     —— 普通用户/助手消息。
 * - "no_response" —— 一轮结束的直接对话，但没产生文本回复。
 * - "onboarding_kickoff" —— 产品撰写的开场输入，发给 Mika 但从不作为成员
 *   消息渲染。
 * - "onboarding_opening" —— Mika 对 kickoff 的回复；聊天界面在它下面渲染
 *   onboarding 起步卡片而不是快捷动作 chips。
 */
export type ChatMessageKind =
  | "message"
  | "no_response"
  | "onboarding_kickoff"
  | "onboarding_opening";

/**
 * 助手回复顺带的一条简明后续建议。UI 渲染 `label`，`prompt` 是发回给 agent
 * 的完整文本。至多一个动作应标记为 primary；即便旧/未来服务器多发几个，
 * 客户端仍能安全渲染。
 */
export interface ChatQuickAction {
  label: string;
  prompt: string;
  primary?: boolean;
}

/**
 * 仅客户端标记（永不持久化、永不拉取）：标明某轮次的快捷动作补充仍在途
 * ——在 chat:done 与 chat:quick_actions 之间驱动 pill 骨架。
 */
export interface ChatQuickActionsPendingState {
  message_id: string;
  task_id: string;
  /**
   * 绝对 epoch-ms 截止时间，之后客户端放弃等待 chat:quick_actions 补充并清除
   * 该标记。存在标记上（而非组件定时器），使切换聊天界面（会卸载/重挂超时
   * hook）能继续同一截止时间，而不是每次重新武装新窗口（MUL-5149 review）。
   */
  expires_at: number;
}

/**
 * 仅客户端信号（永不持久化、永不拉取）：一次显式刷新「重新生成」失败——
 * daemon 的建议轮从未完成，因此该轮次的 pills 未变。由实时层在失败的
 * chat:quick_actions 事件上设置，视图消费一次以提示「无法刷新」，然后自清。
 * `at` 是每事件 nonce，即使 message_id 重复，第二次失败也能重新触发
 *（MUL-5149 review）。
 */
export interface ChatQuickActionsFailureState {
  message_id: string;
  at: number;
}

/** 会话最近一条消息的预览（IM 式列表用）。 */
export interface ChatLastMessage {
  content: string;
  role: "user" | "assistant";
  created_at: string;
  /** 最近一条是失败助手回复时存在。 */
  failure_reason?: string | null;
  /** "message"（默认）或 "no_response"。旧服务器可选。 */
  message_kind?: ChatMessageKind;
}

/** 外部渠道（Channel）建会话的来源信息。 */
export interface ChatChannelSource {
  channel_type: string;
  installation_id: string;
  route_revision: number;
}

/** 一个聊天会话。 */
export interface ChatSession {
  id: string;
  workspace_id: string;
  agent_id: string;
  creator_id: string;
  /** 该会话每一轮的持久项目上下文。对话只使用 workspace 上下文时为 null；
   *  旧服务器可选。 */
  project_id?: string | null;
  title: string;
  status: "active" | "archived";
  /** 会话是否有未读的助手回复。仅列表。等价便捷 `unread_count > 0`。 */
  has_unread: boolean;
  /** 未读助手消息数（读游标之后）。仅列表；可选让旧客户端 / 非列表载荷保持
   *  有效。 */
  unread_count?: number;
  /** 会话最新消息；空会话时为 null。仅列表。 */
  last_message?: ChatLastMessage | null;
  /** 用户是否把该会话置顶到列表顶部。可选让旧客户端 / 非列表载荷保持有效。 */
  pinned?: boolean;
  /** 由外部渠道创建的聊天会带此字段。历史聊天即使在新一代路由成为当前后，
   *  也保留来源。 */
  channel_source?: ChatChannelSource;
  /** 一手（first-party）聊天没有此字段。 */
  is_current_channel_route?: boolean;
  created_at: string;
  updated_at: string;
}

/** 一个排队中的聊天任务项。 */
export interface PendingChatTaskItem {
  task_id: string;
  status: string;
  chat_session_id: string;
}

/** 排队聊天任务列表接口响应。 */
export interface PendingChatTasksResponse {
  tasks: PendingChatTaskItem[];
}

/**
 * FAB「运行中」指示器的布尔快速通道载荷——GET /api/chat/pending-tasks/has-any
 * 返回。FAB 只需知道是否存在任何在途聊天任务，因此不用拉取完整任务列表。
 */
export interface HasPendingChatTasksResponse {
  has_pending: boolean;
}

/** 一条聊天消息。 */
export interface ChatMessage {
  id: string;
  chat_session_id: string;
  role: "user" | "assistant";
  content: string;
  task_id: string | null;
  created_at: string;
  /**
   * 通过附件表的 chat_message_id 外键关联到本条消息的附件。由 ListChatMessages
   * 填充。UI 在气泡里把它们渲染成文件/图片卡片；`content` 里的 markdown URL
   * 可能带过期签名，而这里的附件元数据稳定，是点击时下载的事实来源。
   */
  attachments?: import("./attachment").Attachment[];
  /**
   * 设置后，这是服务端 FailTask 兜底合成的助手消息（对应议题路径的失败系统
   * 评论）。`content` 携带 daemon 报告的原始 errMsg；前端把 `failure_reason`
   *（枚举，如 "agent_error" / "connection_error" / "timeout"）映射到用户可见
   * 文案并渲染成错误气泡。成功消息与用户消息上为 null。
   */
  failure_reason?: string | null;
  /**
   * 从 `task.created_at`（用户点发送）到终态（completed/failed）的墙钟时长。
   * 服务器在 CompleteTask/FailTask 合成的助手消息上设置。UI 在气泡下渲染成
   * "38s 内回复" / "12s 后失败"。用户消息与早于迁移 063 的旧助手消息上为
   * null。
   */
  elapsed_ms?: number | null;
  /**
   * "message"（默认）或 "no_response"——一轮完成的直接对话但无文本回复
   *（MUL-4351）。可选/增量：旧服务器与用户消息上缺失；缺失按 "message" 处理。
   */
  message_kind?: ChatMessageKind;
  /** 随该回复生成的至多三条经服务器校验的后续建议。 */
  quick_actions?: ChatQuickAction[];
}

/** 消息翻页游标（按 created_at + id 定位）。 */
export interface ChatMessagesCursor {
  created_at: string;
  id: string;
}

/** 分页消息接口响应。 */
export interface ChatMessagesPage {
  messages: ChatMessage[];
  limit: number;
  has_more: boolean;
  next_cursor?: ChatMessagesCursor | null;
}

/** 发送聊天消息的接口响应。 */
export interface SendChatMessageResponse {
  message_id: string;
  task_id: string;
  /** 服务器是否支持排队跟进发送。 */
  supports_queue?: boolean;
  /**
   * 仅当该任务被接受排到同一会话另一在途任务之后时才为 true。为兼容旧服务器
   * 设为可选。
   */
  queued?: boolean;
  /**
   * 服务器权威的任务创建时间。乐观 StatusPill 种子用它当锚点，让计时从真实
   * 的 `0s` 起步——没有它，前端回退到本地时钟，之后 WS 事件更新缓存时计时器
   * 会「往回跳」。
   */
  created_at: string;
  /**
   * 服务器实际绑定到该消息的附件 id。客户端用它与自己请求的 id 做差集，以在
   * 附件静默绑定失败时给出告警——无需额外请求。为兼容早于该字段的服务器设为
   * 可选。
   */
  attachment_ids?: string[];
}

/** 一条被取消的聊天消息（供草稿恢复）。 */
export interface CancelledChatMessage {
  chat_session_id: string;
  message_id: string;
  content: string;
  restore_to_input: boolean;
  /**
   * 从被删除消息解绑的附件，供恢复的草稿重发时重新绑定。早于该字段的服务器
   * 上缺失。
   */
  attachments?: import("./attachment").Attachment[];
}

/** 取消任务的接口响应（可带被取消聊天消息）。 */
export interface CancelTaskResponse extends AgentTask {
  cancelled_chat_message?: CancelledChatMessage;
}

/**
 * GET /api/chat/sessions/{id}/draft-restores 返回的一条持久化草稿恢复（#5219）：
 * 一次延迟取消在取消 HTTP 响应返回之后才以「空记录」结算，因此被删除的
 * prompt 在服务端留存，直到创建方的客户端把它应用到编辑器并消费掉
 *（DELETE，幂等）。chat:cancel_finalized 事件只是本次拉取的失效提示。
 */
export interface ChatDraftRestore {
  /** 被删除的用户聊天消息 id——稳定的去重与消费键。 */
  id: string;
  chat_session_id: string;
  task_id?: string;
  content: string;
  /**
   * 从被删除消息解绑的附件，供恢复的草稿重发时重新绑定。
   */
  attachments?: import("./attachment").Attachment[];
  created_at?: string;
}

/** 草稿恢复列表接口响应。 */
export interface ChatDraftRestoresResponse {
  restores: ChatDraftRestore[];
}

/**
 * GET /api/chat/sessions/{id}/pending-task 的响应。
 * 会话没有在途任务时所有字段都缺失。
 *
 * `created_at` 是聊天 StatusPill 计时器的服务器权威锚点——chat-window.tsx 里
 * 的乐观种子只填 task_id/status，此查询随后补上真实 created_at，让计时器在
 * 刷新/重开后不「重置回 0s」。
 */
export interface ChatQueuedTask {
  task_id: string;
  status: string;
  created_at: string;
  message_id?: string;
  content?: string;
}

/** 把排队任务提前的接口响应。 */
export interface PrioritizeQueuedChatTaskResponse {
  task_id: string;
  /** 所选任务被提前后、应停掉的服务器权威当前活跃任务。 */
  active_task_id?: string;
}

/** 会话当前挂起/排队任务的汇总。 */
export interface ChatPendingTask {
  task_id?: string;
  status?: string;
  created_at?: string;
  /**
   * 为何一个 `waiting_local_directory` 任务被停放：它需要的目录，以及（可知时）
   * 占用该目录的任务短 id。仅在该状态处于当前时设置——见 promotePendingChatTask，
   * 它会在其它每个转移上清除它，因此陈旧的占用绝不会被读成活跃占用。早于该
   * 字段的服务器上缺失，渲染为朴素的等待标签。
   */
  wait_reason?: string;
  /** 显式能力门控；早于跟进队列的服务器上缺失。 */
  supports_queue?: boolean;
  /**
   * 根任务之后排队的跟进任务。根任务在认领前数据库状态可能仍是 `queued`，
   * 但绝不会在此重复出现。
   */
  queued_tasks?: ChatQueuedTask[];
}
