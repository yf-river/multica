// 本文件定义 Telegram 集成相关类型。

/** 绑定到单个 Multica agent 上的一个 Telegram 机器人安装。
 *
 * 线上结构对应 `server/internal/handler/telegram.go` 里的
 * `TelegramInstallationResponse`。后端以后新增的字段必须默认可选，让旧版
 * 桌面端仍能解析响应——见 CLAUDE.md → API Compatibility。 */
export interface TelegramInstallation {
  id: string;
  workspace_id: string;
  agent_id: string;
  /** 机器人的数字 Telegram id（token 前缀），以字符串形式保存。 */
  bot_id: string;
  /** 机器人的 Telegram 用户名（不含 @）。 */
  bot_username: string;
  installer_user_id: string;
  status: "active" | "revoked" | string;
  installed_at: string;
  created_at: string;
  updated_at: string;
}

/** 列出 Telegram 安装的接口响应。 */
export interface ListTelegramInstallationsResponse {
  installations: TelegramInstallation[];
  /** 部署是否配置了静态加密密钥。为 false 时隐藏连接入口，面板展示
   *  「请联系运维开启 Telegram」状态。 */
  configured: boolean;
  /** 安装通道是否可用（只要 Telegram 已配置即为 true——粘贴 BotFather token
   *  无需托管凭证）。可选字段，早于它的旧桌面构建按「关」处理。 */
  install_supported?: boolean;
}

/** 机器人安装的请求体：管理员从 @BotFather 粘贴的 token。后端先在线校验
 *（getMe）再持久化。 */
export interface RegisterTelegramRequest {
  bot_token: string;
}

/** 兑换后的回显：token 携带的 Telegram 用户 id 现在已在该 workspace/安装下
 * 绑定到当前登录的 Multica 用户。 */
export interface RedeemTelegramBindingTokenResponse {
  workspace_id: string;
  installation_id: string;
  telegram_user_id: string;
}
