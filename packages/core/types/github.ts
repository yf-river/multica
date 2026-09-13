// 本文件定义 GitHub 集成相关类型：安装、PR 卡片数据及其 CI/合并状态。

/** PR 的总体状态。 */
export type GitHubPullRequestState = "open" | "closed" | "merged" | "draft";

/** 某个 PR 当前 head SHA 的聚合 CI 结论，由服务器从每个 app 的最新
 * check_suite 计算。尚未见到已完成的 suite 时为 `null`（如 PR 刚开、或仓库
 * 没有配置 CI）。
 *
 * 为兼容后端漂移保留的旧字段；当前 PR 卡片改从 `checks_rollup` + 计数派生
 * CI 状态。 */
export type GitHubPullRequestChecksConclusion = "passed" | "failed" | "pending";

/** GitHub 旧 `mergeable_state` 的原始镜像。已被 `mergeable` + 
 * `merge_state_status` 快照对取代；仅为后端漂移保留为可选。 */
export type GitHubMergeableState = string;

/** GitHub 的 `mergeable` 判定——只回答「有没有冲突」。`unknown` 是正常的
 * 瞬时值（GitHub 惰性计算它）；渲染时既不能显示成「冲突」也不能显示成
 * 「可合并」。 */
export type GitHubPullRequestMergeable = "mergeable" | "conflicting" | "unknown";

/** GitHub 的 `mergeStateStatus`。「可以合并」只在 `clean` 时成立（它把必需
 * 检查 + 分支保护都折进来了）；其它值如实展示，绝不推断成「已就绪/可合并」。 */
export type GitHubPullRequestMergeStateStatus =
  | "clean"
  | "dirty"
  | "blocked"
  | "behind"
  | "unstable"
  | "draft"
  | "has_hooks"
  | "unknown";

/** GitHub 的整体 CI 汇总判定（statusCheckRollup.state）。`null`/缺失表示
 * 还没有任何检查上报——绝不能渲染成已通过。 */
export type GitHubPullRequestChecksRollup =
  | "success"
  | "failure"
  | "pending"
  | "error"
  | "expected";

/** 一个 GitHub App 安装。 */
export interface GitHubInstallation {
  id: string;
  workspace_id: string;
  /** GitHub 的数字安装 id——连接/断开流程用的管理句柄。调用者无权管理集成
   * 时省略（见 ListGitHubInstallationsResponse.can_manage）。 */
  installation_id?: number;
  account_login: string;
  account_type: "User" | "Organization";
  account_avatar_url: string | null;
  created_at: string;
  /** 连接该安装的 workspace 成员的显示名。可选：旧后端与最小可见性部署
   *  可能省略；UI 仅在存在时渲染「由谁连接」一行。 */
  connected_by?: string;
}

/** 一张 PR 卡片的数据（含快照期的 CI/合并状态）。 */
export interface GitHubPullRequest {
  id: string;
  /** 来源平台。仅支持 GitHub 的旧后端省略。 */
  provider?: "github" | "forgejo" | "gitea" | "gitlab";
  workspace_id: string;
  repo_owner: string;
  repo_name: string;
  number: number;
  title: string;
  state: GitHubPullRequestState;
  html_url: string;
  branch: string | null;
  author_login: string | null;
  author_avatar_url: string | null;
  merged_at: string | null;
  closed_at: string | null;
  pr_created_at: string;
  pr_updated_at: string;
  /** GitHub API 快照里的冲突判定。只回答「有没有冲突」；旧后端省略。 */
  mergeable?: GitHubPullRequestMergeable | null;
  /** 快照中的 GitHub `mergeStateStatus`。「可以合并」的依据（仅 `clean`
   *  时成立）；旧后端省略。 */
  merge_state_status?: GitHubPullRequestMergeStateStatus | null;
  /** 快照中的 GitHub 整体 CI 汇总判定。仅当 `snapshot_available === true`
   *  时 `null` 才表示「没有检查」；单独缺失并不能当作「无检查」结论。 */
  checks_rollup?: GitHubPullRequestChecksRollup | null;
  /** 仅当 GitHub API 快照功能启用、且存储的快照属于该 PR 当前 head 时为
   *  true。false 表示必须隐藏 CI/合并快照区；省略保持旧平台输出。 */
  snapshot_available?: boolean;
  /** 快照中的检查计数。旧后端省略这些字段，视为 0。没有检查上报时
   *  `checks_total` 为 0。 */
  checks_total?: number;
  checks_passed?: number;
  checks_failed?: number;
  checks_running?: number;
  /** 当前失败检查的名称，供「…失败 · a, b」摘要用。旧后端省略，视为空列表。 */
  failed_check_names?: string[];
  /** 展示的快照是否过期（GitHub 故障 / 密钥被吊销）。此时卡片把两个状态
   *  元素置灰并显示快照年龄。 */
  snapshot_stale?: boolean;
  /** 快照抓取时刻（RFC3339），供「过期」提示显示。 */
  snapshot_fetched_at?: string | null;
  /** GitHub `mergeable_state` 的旧镜像。可选；已被 `mergeable` +
   *  `merge_state_status` 取代。 */
  mergeable_state?: GitHubMergeableState | null;
  /** 旧的聚合 CI 结论。可选；已被 `checks_rollup` 取代。 */
  checks_conclusion?: GitHubPullRequestChecksConclusion | null;
  /** 旧的挂起 suite 计数。可选；已被 `checks_running` 取代。 */
  checks_pending?: number;
  /** GitHub `pull_request` 载荷里的 diff 统计。旧后端省略这些字段；把
   *  0/0/0 视为「未知」并隐藏统计行。 */
  additions?: number;
  deletions?: number;
  changed_files?: number;
}

/** 列出 GitHub 安装的接口响应。 */
export interface ListGitHubInstallationsResponse {
  installations: GitHubInstallation[];
  /** 部署是否配置了 GitHub App 凭证。为 false 时隐藏/禁用连接按钮。 */
  configured: boolean;
  /** 服务器能否铸造安装 token 来浏览仓库。旧后端省略该字段；调用方须把
   *  缺失当作 false。 */
  repository_browse_configured?: boolean;
  /** 调用者能否连接/断开安装。非管理员成员得到 `false`，且其安装会省略
   *  `installation_id`。早于 MUL-2413 的后端省略该字段；为只读安全把缺失
   *  当作 `false`。 */
  can_manage?: boolean;
}

/** GitHub 连接 URL 的响应。 */
export interface GitHubConnectResponse {
  /** 浏览器应打开的 GitHub App 安装 URL。`configured` 为 false 时为空。 */
  url?: string;
  configured: boolean;
}

/** 一次 GitHub 安装下可访问的一个仓库。 */
export interface GitHubRepository {
  id: number;
  full_name: string;
  html_url: string;
  clone_url: string;
  description: string | null;
  private: boolean;
  archived: boolean;
  default_branch: string;
}

/** 列出 GitHub 仓库的分页响应。 */
export interface ListGitHubRepositoriesResponse {
  repositories: GitHubRepository[];
  total_count: number;
  next_page: number | null;
}
