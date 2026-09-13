// 本文件定义 Slack 集成相关类型（MUL-3666）。

/** 绑定到单个 Multica agent 上的一个 Slack 机器人安装（MUL-3666）。
 *
 * 线上结构对应 `server/internal/handler/slack.go` 里的 `SlackInstallationResponse`。
 * 后端以后新增的字段必须默认可选，让旧版桌面端仍能解析响应——见
 * CLAUDE.md → API Compatibility。 */
export interface SlackInstallation {
  id: string;
  workspace_id: string;
  agent_id: string;
  /** 该机器人所安装到的 Slack workspace（team）id。 */
  team_id: string;
  /** 已安装机器人在 Slack 上的用户 id。 */
  bot_user_id: string;
  installer_user_id: string;
  status: "active" | "revoked" | string;
  installed_at: string;
  created_at: string;
  updated_at: string;
}

/** 列出 Slack 安装的接口响应。 */
export interface ListSlackInstallationsResponse {
  installations: SlackInstallation[];
  /** 部署是否配置了静态加密密钥。为 false 时隐藏连接入口，面板展示
   *  「请联系运维开启 Slack」状态。 */
  configured: boolean;
  /** 安装通道是否可用（只要 Slack 已配置即为 true，即静态密钥已设置——
   *  自带应用安装无需托管 OAuth 凭证）。单独拆出一个开关以便前后兼容；
   *  可选字段，早于它的旧桌面构建按「关」处理。 */
  install_supported?: boolean;
}

/** 自带应用（BYO）安装的请求体：管理员从其创建的 Slack 应用粘贴两个 token。
 * 后端先校验两者属于同一个 Slack 应用（且 app token 有效）再持久化，随后
 * 返回新建的 SlackInstallation。 */
export interface RegisterSlackBYORequest {
  bot_token: string;
  app_token: string;
}

/** 兑换后的回显：token 携带的 Slack 用户 id 现在已在该 workspace/安装下
 * 绑定到当前登录的 Multica 用户。 */
export interface RedeemSlackBindingTokenResponse {
  workspace_id: string;
  installation_id: string;
  slack_user_id: string;
}
