// 本文件定义飞书 / Lark 集成相关类型：机器人安装、设备码扫码绑定的安装
// 流程及其状态轮询。

/** 绑定到单个 Multica agent 上的一个 Lark 机器人安装。
 *
 * 线上结构对应 `server/internal/handler/lark.go` 里的 `LarkInstallationResponse`。
 * 后端以后新增的字段必须默认可选，让旧版桌面端仍能解析响应——见
 * CLAUDE.md → API Response Compatibility。 */
export interface LarkInstallation {
  id: string;
  workspace_id: string;
  agent_id: string;
  app_id: string;
  tenant_key?: string | null;
  bot_open_id: string;
  installer_user_id: string;
  status: "active" | "revoked" | string;
  /** 机器人所在 Lark 云环境："feishu"（国内版）或 "lark"（国际版），在安装
   *  时自动探测。可选，让旧桌面解析新服务器、或新构建打到尚未提供该字段的
   *  服务器时，在 UI 上默认飞书（见 CLAUDE.md → API Response Compatibility）。 */
  region?: "feishu" | "lark" | string;
  installed_at: string;
  created_at: string;
  updated_at: string;
}

/** 列出 Lark 安装的接口响应。 */
export interface ListLarkInstallationsResponse {
  installations: LarkInstallation[];
  /** 部署是否配置了静态加密密钥。为 false 时必须禁用「绑定」按钮，面板
   *  渲染空态 / 「请联系运维开启 Lark」状态。 */
  configured: boolean;
  /** 新的安装能否通过设备码「扫码绑定」路径端到端完成——即设备码
   *  RegistrationService 已接线且真实 Lark HTTP APIClient（非 no-op 桩）就位。
   *  为 false 时隐藏安装入口，面板显示「即将推出」。可选字段，收到不返回
   *  该字段的服务器的旧桌面构建默认为 undefined，按「不支持」处理。 */
  install_supported?: boolean;
}

/** 设备码安装的前半段：服务器已针对 accounts.feishu.cn 开启一个注册会话并
 * 返回二维码 URL。前端把 `qr_code_url` 渲染成二维码（并提供可点击链接兜底），
 * 然后按给定节奏轮询 `/install/{session_id}/status`，直到成功或终态失败。 */
export interface BeginLarkInstallResponse {
  session_id: string;
  qr_code_url: string;
  expires_in_seconds: number;
  poll_interval_seconds: number;
}

/** 安装状态轮询结果。`status` 是判别字段。 */
export interface LarkInstallStatusResponse {
  status: "pending" | "success" | "error" | string;
  /** status === "success" 时填充。前端使安装缓存失效，让新行出现在
   *  Settings 页签。 */
  installation_id?: string;
  /** 出错时的稳定错误码——用它（而非 error_message）来选择文案。
   *  常见值："expired"、"access_denied"、"lark_protocol_error"、
   *  "bot_info_failed"、"installation_conflict"、"installer_bind_failed"、
   *  "internal_error"。 */
  error_reason?: string;
  /** 供调试用的人读错误尾巴；生产 UI 应根据 error_reason 显示文案，此字段
   *  只用作诊断提示。 */
  error_message?: string;
}

/** 兑换绑定 token 的响应：token 携带的 Lark open_id 现已绑定当前用户。 */
export interface RedeemLarkBindingTokenResponse {
  workspace_id: string;
  installation_id: string;
  lark_open_id: string;
}
