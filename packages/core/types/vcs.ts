// 本文件定义「基于 token 的 Git 托管平台」集成类型（Forgejo / Gitea / GitLab）。
// 与 GitHub 不同，这些平台没有 App/安装模型：每个 workspace 存一条指向某个
// 平台实例的 token 连接。从这些平台镜像来的 PR 通过共享的 GitHubPullRequest
// 形状呈现，并带上对应的 `provider` 标记。

/** 受支持的 Git 托管平台类型。 */
export type VCSProvider = "forgejo" | "gitea" | "gitlab";

/** 一条 VCS 连接（workspace 内）。 */
export interface VCSConnection {
  id: string;
  workspace_id: string;
  provider: VCSProvider;
  /** 实例基地址，如 https://forgejo.example.com（末尾不带斜杠）。 */
  instance_url: string;
  /** 存储的 access token 所认证的登录名（用户或组织）。 */
  account_login: string;
  /** 要注册到平台侧的完整 webhook 地址。当服务器没有配置公网地址时为空，
   *  此时 UI 会自行拼上 `webhook_path`。 */
  webhook_url: string;
  webhook_path: string;
  created_at: string;
}

/** 列出 VCS 连接的接口响应。 */
export interface ListVCSConnectionsResponse {
  connections: VCSConnection[];
  /** 本次部署是否提供该集成（仅自托管有；托管云关闭）。为 false 时整个
   *  区块隐藏。旧后端省略该字段，按 true 处理以让既有自托管 UI 仍能渲染
   * （可见性同时由 /api/config 上的 vcs_integration_available 把关，那才是
   *  权威的部署信号）。 */
  available?: boolean;
  /** 部署是否配置了 MULTICA_VCS_SECRET_KEY。为 false 时连接表单禁用。
   *  旧后端省略该字段，按 false 处理。 */
  configured?: boolean;
  /** 调用者能否连接/断开。非管理员为 false。 */
  can_manage?: boolean;
}

/** 新建（连接）一个 VCS 的请求体。 */
export interface ConnectVCSRequest {
  provider: VCSProvider;
  instance_url: string;
  access_token: string;
}

/** 连接 VCS 的响应：VCS 连接信息 + 一次性明文 webhook 密钥。 */
export interface ConnectVCSResponse extends VCSConnection {
  /** 一次性明文 webhook 密钥，用于粘贴到平台侧（Forgejo/Gitea 是 HMAC 密钥，
   *  GitLab 是 X-Gitlab-Token 值）。之后不可再取回（加密存储）；重连会轮换它。 */
  webhook_secret: string;
}
