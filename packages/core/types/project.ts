// 本文件定义「项目（Project）」与「项目资源」相关类型：项目分组议题，
// 并可把外部资源（git 仓库 / 本机目录）挂到项目上供 agent 使用。

/** 项目状态。 */
export type ProjectStatus = "planned" | "in_progress" | "paused" | "completed" | "cancelled";

/** 项目优先级。 */
export type ProjectPriority = "urgent" | "high" | "medium" | "low" | "none";

/** 一个项目。 */
export interface Project {
  id: string;
  workspace_id: string;
  title: string;
  description: string | null;
  icon: string | null;
  status: ProjectStatus;
  priority: ProjectPriority;
  lead_type: "member" | "agent" | null;
  lead_id: string | null;
  // 日历日期（"YYYY-MM-DD"），不带时刻与时区——与 issue.start_date /
  // issue.due_date 同一契约。
  start_date: string | null;
  due_date: string | null;
  created_at: string;
  updated_at: string;
  issue_count: number;
  done_count: number;
  resource_count: number;
}

/** 新建项目的请求体。 */
export interface CreateProjectRequest {
  title: string;
  description?: string;
  icon?: string;
  status?: ProjectStatus;
  priority?: ProjectPriority;
  lead_type?: "member" | "agent";
  lead_id?: string;
  start_date?: string;
  due_date?: string;
  // 要在建项目的同一事务里附带的资源。任一资源无效或重复，服务器返回 4xx
  //（并回滚）。
  resources?: CreateProjectResourceRequest[];
}

/** 更新项目的请求体。 */
export interface UpdateProjectRequest {
  title?: string;
  description?: string | null;
  icon?: string | null;
  status?: ProjectStatus;
  priority?: ProjectPriority;
  lead_type?: "member" | "agent" | null;
  lead_id?: string | null;
  // 省略该键表示日期不动；传 null（或 ""）表示清除。
  start_date?: string | null;
  due_date?: string | null;
}

/** 项目列表接口响应。 */
export interface ListProjectsResponse {
  projects: Project[];
  total: number;
}

// ProjectResource 是项目指向外部资源的类型化指针。resource_ref 的具体形状
// 取决于 resource_type。新增类型时，在服务端 validateAndNormalizeResourceRef
// 加一个分支、在 UI 加一个渲染器。
//
// 已知类型（UI 必须对未知的服务端新增类型走 default 分支）：
//   - github_repo: 云端 git 检出，ref = { url, ref?, default_branch_hint? }
//   - local_directory: 在特定 daemon 上执行的 agent，
//     ref = { local_path, daemon_id, label?, execution_mode? }
export type ProjectResourceType = "github_repo" | "gongfeng_repo" | "local_directory";

/** github_repo 类型的资源引用。 */
export interface GithubRepoResourceRef {
  url: string;
  ref?: string;
  default_branch_hint?: string;
}

/** gongfeng_repo 类型的资源引用。 */
export interface GongfengRepoResourceRef {
  url: string;
  project_path?: string;
  resource_kind?: string;
  ref?: string;
}

/**
 * 共享同一个本地目录的任务如何执行。
 *
 * - `in_place`: agent 直接在该用户目录里工作，任务一个一个跑——第二个任务
 *   会在 `waiting_local_directory` 状态里等待。改动落在用户的工作副本上。
 * - `worktree`: 每个任务在 runtime 的 workspace 里为那个仓库建自己的 git
 *   worktree，任务可并发执行，并以分支形式交付成果而不是改动工作副本。
 *   同一会话的每个任务共享那条分支——`agent/<agent>/<issue>`——使后续
 *   任务接续上一轮的工作；没有会话背景的任务得到 `agent/<agent>/<task>`。
 *   是否接续由仓库里的一条归属记录决定，而不是看分支名，因此用户自己建的
 *   同名分支绝不会被采纳。
 *
 * 缺失表示 `in_place`：在模式出现前创建的资源保持原有行为，所以该字段是
 * 可选而非服务端默认。
 */
export type LocalDirectoryExecutionMode = "in_place" | "worktree";

/** local_directory 类型的资源引用。 */
export interface LocalDirectoryResourceRef {
  local_path: string;
  daemon_id: string;
  label?: string;
  execution_mode?: LocalDirectoryExecutionMode;
}

/** 资源引用的联合形状（按 resource_type 对应具体形态）。 */
export type ProjectResourceRef =
  | GithubRepoResourceRef
  | GongfengRepoResourceRef
  | LocalDirectoryResourceRef
  | Record<string, unknown>;

/** 一条项目资源记录。 */
export interface ProjectResource {
  id: string;
  project_id: string;
  workspace_id: string;
  resource_type: ProjectResourceType;
  resource_ref: ProjectResourceRef;
  label: string | null;
  position: number;
  created_at: string;
  created_by: string | null;
}

/** 新建项目资源的请求体。 */
export interface CreateProjectResourceRequest {
  resource_type: ProjectResourceType;
  resource_ref: ProjectResourceRef;
  label?: string;
  position?: number;
}

// resource_type 在服务端不可变；局部更新载荷与之对应。只发送要改的字段即可
// ——服务器会把请求体与现有行合并，包括 resource_ref 的各种快捷形态。
export interface UpdateProjectResourceRequest {
  resource_ref?: ProjectResourceRef;
  label?: string | null;
  position?: number;
}

/** 项目资源列表接口响应。 */
export interface ListProjectResourcesResponse {
  resources: ProjectResource[];
  total: number;
}
