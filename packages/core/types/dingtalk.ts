// 本文件定义钉钉（DingTalk）集成相关类型：机器人安装、群组发现、群组内
// 观察到的机器人状态，以及自带应用（BYO）安装的请求/响应。

/** 绑定到单个 Multica agent 上的一个钉钉机器人安装。
 *
 * 线上结构对应 `server/internal/handler/dingtalk.go` 里的
 * `DingTalkInstallationResponse`。后端以后新增的字段必须默认可选，让旧版
 * 桌面端仍能解析响应——见 CLAUDE.md → API Compatibility。 */
export interface DingTalkInstallation {
  id: string;
  workspace_id: string;
  agent_id: string;
  installer_user_id: string;
  status: "active" | "revoked" | string;
  installed_at: string;
  created_at: string;
  updated_at: string;
  /** 仅当 workspace 管理员查看一条 agent 已不存在的孤儿安装时才为 false。
   *  旧后端可选。 */
  agent_available?: boolean;
  /** 当前已鉴权 Multica 用户为该机器人关联的钉钉员工 id。按成员隔离，
   *  因此对成员可见的安装端点不会泄露其它成员的钉钉身份。 */
  bound_dingtalk_user_ids?: string[];
}

/** 列出钉钉安装的接口响应。 */
export interface ListDingTalkInstallationsResponse {
  installations: DingTalkInstallation[];
  /** 部署是否配置了静态加密密钥。为 false 时隐藏连接入口，面板展示
   *  「请联系运维开启钉钉」状态。 */
  configured: boolean;
  /** 安装通道是否可用（只要钉钉已配置即为 true，即静态密钥已设置——
   *  自带应用安装无需托管凭证）。单独拆出开关以便前后兼容；可选字段，
   *  早于它的旧桌面构建按「关」处理。 */
  install_supported?: boolean;
}

/** 在某钉钉群里观察到一个已连接的多米卡机器人。 */
export interface DingTalkGroupBot {
  installation_id: string;
  agent_id: string;
  /** 可读的钉钉机器人名。在旧后端、瞬时查询失败或应用尚未获得
   *  qyapi_chat_manage 权限时为空。 */
  bot_name: string;
  /** 可读身份不可用的机器可读原因。 */
  bot_identity_issue: string;
  /** 该群最近一次被接受的 @提及，RFC 3339 格式。与旧后端兼容设为可选；
   *  没有留存消息时为空。 */
  last_active_at?: string;
  /** 该机器人在此群被接受的去重消息数。 */
  mention_count?: number;
}

/** 在收到一次校验通过的 @bot 回调后观察到的钉钉群。 */
export interface DingTalkGroup {
  conversation_id: string;
  conversation_title: string;
  bots: DingTalkGroupBot[];
}

/** 列出钉钉群的接口响应。 */
export interface ListDingTalkGroupsResponse {
  groups: DingTalkGroup[];
  /** 当已安装客户端连到早于群发现特性的后端时为 false。调用方据此避免轮询
   *  一个不存在的端点。 */
  group_discovery_supported: boolean;
  /** 90 天活跃窗口之外的历史观察，按机器人安装为键。为兼容旧服务器而设的
   *  增量字段。 */
  inactive_group_counts?: Record<string, number>;
  /** 按安装为键的应用级身份，即使某机器人的所有群都在活跃窗口外也可用。 */
  bot_identities?: Record<string, DingTalkGroupBot>;
  /** 下一页不活跃数据的偏移量。页面已完整时缺失。 */
  next_offset?: number;
}

/** 钉钉群搜索的查询参数。 */
export interface ListDingTalkGroupsParams {
  activity?: "inactive";
  installationId?: string;
  offset?: number;
  limit?: number;
}

/** 自带应用（BYO）安装的请求体：agent 拥有者或 workspace 管理员从其创建的
 * 钉钉 Stream 模式机器人粘贴 AppKey 与 AppSecret。后端校验两者后才持久化，
 * 随后返回新建的 DingTalkInstallation。 */
export interface RegisterDingTalkBYORequest {
  client_id: string;
  client_secret: string;
}

/** 兑换后的回显：token 携带的钉钉用户 id 现在已在该 workspace/安装下绑定到
 * 当前登录的 Multica 用户。 */
export interface RedeemDingTalkBindingTokenResponse {
  workspace_id: string;
  installation_id: string;
  dingtalk_user_id: string;
}
