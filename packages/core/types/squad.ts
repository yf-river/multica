// 本文件定义「Squad（小队）」相关类型：把 agent 与成员组织成队、按队长
// 协作处理议题，以及成员在线状态快照。

/** squad 成员类型：agent 或人类成员。 */
export type SquadMemberType = "agent" | "member";

/** squad 一次协作活动的结果：采取了动作 / 无需动作 / 失败。 */
export type SquadActivityOutcome = "action" | "no_action" | "failed";

/** 成员概览条目（用于列表里的小头像/角色展示）。 */
export interface SquadMemberPreview {
  member_type: SquadMemberType;
  member_id: string;
  role: string;
}

/** 一个 squad。 */
export interface Squad {
  id: string;
  workspace_id: string;
  name: string;
  description: string;
  instructions: string;
  avatar_url: string | null;
  leader_id: string;
  creator_id: string;
  created_at: string;
  updated_at: string;
  archived_at: string | null;
  archived_by: string | null;
  member_count?: number;
  member_preview?: SquadMemberPreview[];
}

/** squad 成员关系记录。 */
export interface SquadMember {
  id: string;
  squad_id: string;
  member_type: SquadMemberType;
  member_id: string;
  role: string;
  created_at: string;
}

/** 一条 squad 协作活动日志。 */
export interface SquadActivityLog {
  id: string;
  squad_id: string;
  issue_id: string;
  trigger_comment_id: string | null;
  leader_id: string;
  outcome: SquadActivityOutcome;
  details: unknown;
  created_at: string;
}

/** 新建 squad 的请求体。 */
export interface CreateSquadRequest {
  name: string;
  description?: string;
  leader_id: string;
  avatar_url?: string;
}

/** 更新 squad 的请求体。 */
export interface UpdateSquadRequest {
  name?: string;
  description?: string;
  instructions?: string;
  leader_id?: string;
  avatar_url?: string;
}

/** 添加 squad 成员的请求体。 */
export interface AddSquadMemberRequest {
  member_type: SquadMemberType;
  member_id: string;
  role?: string;
}

/** 移除 squad 成员的请求体。 */
export interface RemoveSquadMemberRequest {
  member_type: SquadMemberType;
  member_id: string;
}

/** 修改 squad 成员角色的请求体。 */
export interface UpdateSquadMemberRoleRequest {
  member_type: SquadMemberType;
  member_id: string;
  role: string;
}

/** 记录一条 squad 活动日志的请求体。 */
export interface CreateSquadActivityLogRequest {
  squad_id: string;
  issue_id: string;
  trigger_comment_id?: string;
  outcome: SquadActivityOutcome;
  details?: unknown;
}

// SquadMemberStatus 对应后端在 handler/squad.go::deriveSquadMemberStatus 里
// 推导出的五向分桶。这里保持字符串联合（而非从快照数据重新推导），让 squad
// 页能直接渲染服务端最新的判定，无需重取 agent 快照 / runtime 列表。
// `archived` 优先于一切 runtime/task 信号。
export type SquadMemberStatusValue =
  | "working"
  | "idle"
  | "offline"
  | "unstable"
  | "archived";

/** 某 agent 正在处理的议题简报。 */
export interface SquadActiveIssueBrief {
  issue_id: string;
  identifier: string;
  title: string;
  issue_status: string;
}

/** 单个 squad 成员的在线状态。 */
export interface SquadMemberStatus {
  member_type: SquadMemberType;
  member_id: string;
  // 人类成员返回的 status 为 null，UI 可在同一列表渲染他们但不显示状态
  // 胶囊（v1 没有人类的在线信号）。
  status: SquadMemberStatusValue | null;
  active_issues: SquadActiveIssueBrief[];
  last_active_at: string | null;
}

/** 成员状态快照的接口响应。 */
export interface SquadMemberStatusListResponse {
  members: SquadMemberStatus[];
}
