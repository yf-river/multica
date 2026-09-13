// 本文件定义企业微信（WeCom）智能机器人集成相关类型。

/** 绑定到单个 Multica agent 上的企业微信智能机器人（aibot）安装。
 * 线上结构对应 `server/internal/handler/wecom_web.go` 里的
 * `WecomInstallationResponse`。后端以后新增的任何字段必须默认可选，让旧版
 * 桌面端仍能解析响应——见 CLAUDE.md → API Compatibility。
 */
export interface WecomInstallation {
  id: string;
  workspace_id: string;
  agent_id: string;
  /** 企业微信管理后台分配的智能机器人标识。 */
  bot_id: string;
  installer_user_id: string;
  status: "active" | "revoked" | string;
}

/** 列出企业微信安装的接口响应。 */
export interface ListWecomInstallationsResponse {
  installations: WecomInstallation[];
  /** 本次部署是否设置了 MULTICA_WECOM_SECRET_KEY。为 false 时隐藏 BYO
   * Connect 按钮，面板渲染「请联系运维」状态。 */
  configured: boolean;
  /** 安装通道是否可用（只要已配置即为 true）。与 Slack / Lark 保持一致的
   *  独立开关；可选字段，早于它的桌面构建按「关」处理。 */
  install_supported?: boolean;
}

/** Web UI 的 BYO Connect 对话框的请求体。前两个字段从企业微信管理后台的
 * 智能机器人页面复制：机器人的稳定标识与长连接密钥。后端在写入前用部署的
 * MULTICA_WECOM_SECRET_KEY 封存密钥，明文不会落库。 */
export interface RegisterWecomBYORequest {
  bot_id: string;
  secret: string;
  /** 机器人在聊天里显示的名称。可选，只为一个目的服务：在群里识别机器人
   * 自己的 @提及。企业微信把提及当纯文本投递、没有结构化的提及列表，所以
   * 名称若含空格（如 "Multica Bot"）会吞掉其后输入的斜杠命令。对同一机器人
   * 重新安装时省略它，可保留已存的名称。 */
  bot_name?: string;
}

/** 兑换后的回显：token 携带的企业微信 aibot userid 现在已在该 workspace/
 * 安装下绑定到当前登录的 Multica 用户。 */
export interface RedeemWecomBindingTokenResponse {
  workspace_id: string;
  installation_id: string;
  wecom_user_id: string;
}
