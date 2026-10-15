// 本文件定义 workspace 与用户/成员/邀请/分享链接等核心组织实体的类型。

/** 成员在 workspace 里的角色。 */
export type MemberRole = "owner" | "admin" | "member";

/** 远程仓库的已知来源；服务端新增来源时前端按 unknown 展示。 */
export type WorkspaceRepoProvider = "github" | "gongfeng" | "unknown";

export function normalizeWorkspaceRepoProvider(provider?: string): WorkspaceRepoProvider {
  const value = provider?.trim().toLowerCase();
  if (value === "github" || value === "gongfeng") return value;
  return "unknown";
}

/** workspace 关联的一个 git 仓库的元信息。 */
export interface WorkspaceRepo {
  id?: string;
  revision?: number;
  default_for_tasks?: boolean;
  url: string;
  description?: string;
  /** API 可能返回未来值，读取时用 normalizeWorkspaceRepoProvider 收敛。 */
  provider?: WorkspaceRepoProvider | string;
  project_path?: string;
  default_branch?: string;
  ref?: string;
  head_commit?: string;
  connection_status?: string;
  sync_status?: string;
  test_status?: string;
  last_tested_at?: string;
  last_synced_at?: string;
}

/** 探测仓库 URL 的响应：判定平台、默认分支、可达性等。 */
export interface WorkspaceRepoProbeResponse {
  url: string;
  provider: string;
  project_path: string;
  default_branch: string;
  branches: string[];
  connection_status: string;
  test_status: string;
}

/** 一个 workspace（工作空间）。 */
export interface Workspace {
  id: string;
  name: string;
  slug: string;
  description: string | null;
  context: string | null;
  settings: Record<string, unknown>;
  repos: WorkspaceRepo[];
  issue_prefix: string;
  avatar_url: string | null;
  created_at: string;
  updated_at: string;
}

/**
 * workspace 库中的一台 MCP 服务器。
 *
 * 这是完整的「读」形状：存储的配置是只写的，所以 urls、commands、headers、
 * env 对任何角色都绝不会离开服务器。需要修改条目的 UI 会发送一份替换内容，
 * 而不是编辑读回来的东西。
 *
 * `enabled` 只出现在某 agent 的分配列表上，在那里它是按 agent 的开关；
 * workspace 库列表没有绑定关系可报告。
 */
export interface WorkspaceMcpServer {
  description?: string;
  configured?: boolean;
  id: string;
  workspace_id: string;
  name: string;
  transport: string;
  enabled?: boolean;
  created_at: string;
  updated_at: string;
}

/** 成员关系记录：用户在 workspace 里担任的角色。 */
export interface Member {
  id: string;
  workspace_id: string;
  user_id: string;
  role: MemberRole;
  created_at: string;
}

/** 用户账号（个人资料）。 */
export interface User {
  id: string;
  name: string;
  account: string;
  email: string;
  avatar_url: string | null;
  onboarded_at: string | null;
  /**
   * 来自服务器的 JSONB 数据。这里类型定为 `unknown`，让本模块不依赖问卷的
   * 具体形状——onboarding 视图读取时再强转为 `Partial<QuestionnaireAnswers>`。
   * 服务器始终返回对象（缺省为 `{}`），绝不会是 null。
   */
  onboarding_questionnaire: Record<string, unknown>;
  /**
   * 来自已移除的「starter 内容」对话框的遗留列。该列仍会写入（新账号在
   * mark-onboarded 路径跑完后恒为 'imported'），这样旧桌面构建——在 NULL
   * 时仍会渲染那个对话框——不会把它展示给在更新服务器上创建的用户。
   * 保持 `string | null` 以便向前兼容。
   */
  starter_content_state: string | null;
  /**
   * 自由形式的自我介绍（角色、技术栈、偏好）。会被注入 agent 简报，让编码
   * agent 对「谁在请求这份工作」有廉价、耐用的上下文。服务器始终返回字符串
   * ——列级为 NOT NULL DEFAULT ''，未设置时为空串。
   */
  profile_description: string;
  /** 固定的 IANA 时区；null 表示「渲染时用浏览器探测的时区」。 */
  timezone: string | null;
  created_at: string;
  updated_at: string;
}

/** 成员及其（冗余的）用户展示字段，便于列表直接渲染。 */
export interface MemberWithUser {
  id: string;
  workspace_id: string;
  user_id: string;
  role: MemberRole;
  created_at: string;
  name: string;
  email: string;
  avatar_url: string | null;
}

/** 一条邀请记录。 */
export interface Invitation {
  id: string;
  workspace_id: string;
  inviter_id: string;
  invitee_email: string;
  invitee_user_id: string | null;
  role: MemberRole;
  status: "pending" | "accepted" | "declined" | "expired";
  created_at: string;
  updated_at: string;
  expires_at: string;
  inviter_name?: string;
  inviter_email?: string;
  workspace_name?: string;
}

/** 一条分享链接（用邀请码加入 workspace）。 */
export interface ShareLink {
  id: string;
  workspace_id: string;
  code: string;
  created_by: string;
  role: MemberRole;
  expires_at: string | null;
  max_uses: number | null;
  use_count: number;
  is_active: boolean;
  created_at: string;
  creator_name?: string;
  creator_email?: string;
}

/** 通过分享码加入前，链接对应的 workspace 概要信息。 */
export interface ShareLinkInfo {
  workspace_name: string;
  workspace_slug: string;
  creator_name?: string;
  role: MemberRole;
}

/** 工作区仓库中心读取已确认的项目资源和运行目录。 */
export interface RepositoryLocation {
 id: string; workspace_id: string; repository_id: string; daemon_id: string;
 local_path: string; execution_mode: "worktree" | "in_place";
}

export interface WorkspaceRepositoryCenter {
  configured_locations?: RepositoryLocation[];
  repos: WorkspaceRepo[];
  projects: Array<{ id: string; title: string; resources: import('./project').ProjectResource[] }>;
  locations: Array<{
    project_id: string; task_id: string; runtime_id: string; daemon_id: string;
    runtime_name: string; provider: string; work_dir: string; branch: string; status: string;
    started_at: string | null; completed_at: string | null;
  }>;
}
