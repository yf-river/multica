// 本文件定义「插件（Plugin）」相关类型：插件与 Multica 的关系恰好有三类——
// Action（插件调用 Multica）、Hook（Multica 调用插件）、Resource（静态贡献，
// 无调用）。这些类型镜像服务器解析的 manifest 契约；客户端从不从其它来源
// 重新推导它们。

/** 插件配置字段的值类型。 */
export type PluginConfigFieldType = "string" | "number" | "bool" | "enum" | "secret";

/** 配置 schema 里的一个字段定义。 */
export interface PluginConfigField {
  key: string;
  type: PluginConfigFieldType | string;
  label: string;
  description?: string;
  required: boolean;
  options?: string[];
  placeholder?: string;
  /** 仅字符串字段：渲染 textarea 而非单行输入。 */
  multiline?: boolean;
}

/** 插件 surface（界面）类型：议题面板 / 侧栏面板 / 弹窗。 */
export type PluginSurfaceType = "issue_panel" | "sidebar_panel" | "modal";

/** 一个插件 surface（在 Multica 里展示的扩展界面）。 */
export interface PluginSurface {
  key: string;
  type: PluginSurfaceType | string;
  name: string;
  entry: string;
  platforms?: string[];
}

/** hook 触发方式：ui 按钮 / 手动菜单 / agent / 事件 / 定时。 */
export type PluginHookTrigger = "ui" | "manual" | "agent" | "event" | "schedule";

/** hook 的定时调度配置。 */
export interface PluginHookSchedule {
  cron: string;
  timezone: string;
  /** 仅展示用途的投影。运行时正确性绝不依赖该值。 */
  next_run_at?: string;
}

/** 一个插件 hook。 */
export interface PluginHook {
  key: string;
  name: string;
  description: string;
  triggers: (PluginHookTrigger | string)[];
  events?: string[];
  schedule?: PluginHookSchedule;
  transport: string;
}

/** 一个插件静态资源。 */
export interface PluginResource {
  type: string;
  key: string;
  entry: string;
}

/** 一个已安装的插件。 */
export interface PluginInstallation {
  id: string;
  plugin_key: string;
  name: string;
  description?: string;
  version: string;
  /**
   * 该安装绑定到的已发布版本。发布新版本不会动它——升级是第二次显式同意，
   * 这才是「管理员批准过这份代码」成立而非空谈的关键。
   */
  package_version_id: string;
  enabled: boolean;
  granted_scopes: string[];
  config_schema: PluginConfigField[];
  /** 仅非秘密值。任何端点都不会返回秘密本身。 */
  config: Record<string, unknown>;
  /** 已配置了值的秘密字段名——绝不是它们的值。 */
  configured_secrets: string[];
  surfaces: PluginSurface[];
  hooks: PluginHook[];
  resources: PluginResource[];
  created_at: string;
  updated_at: string;
}

/** 插件安装列表接口响应。 */
export interface PluginInstallationListResponse {
  plugins: PluginInstallation[];
}

/** manifest 概要（供确认屏展示）。 */
export interface PluginManifestSummary {
  key: string;
  name: string;
  description?: string;
  version: string;
  author: { name: string; url?: string };
  contributes?: {
    hooks?: Array<{
      key: string;
      name: string;
      triggers: (PluginHookTrigger | string)[];
      schedule?: PluginHookSchedule;
    }>;
  };
}

/**
 * 授权确认屏渲染的内容。该模型里没有签名、没有信任分级：管理员阅读作用域
 * 列表本身就是信任决策。
 *
 * 它描述某一个已发布版本，而安装也正是指定这个版本。
 */
export interface PluginPreview {
  manifest: PluginManifestSummary;
  scopes: string[];
  config_schema: PluginConfigField[];
  version_id: string;
  version: string;
  digest: string;
  installed: boolean;
  installed_version?: string;
  /** 本次安装会在已授予作用域之上新增的作用域。 */
  added_scopes?: string[];
}

/** 预览插件的请求体（指定版本）。 */
export interface PluginPreviewRequest {
  version_id: string;
}

/** 安装插件的请求体（指定版本与授予作用域）。 */
export interface PluginInstallRequest {
  version_id: string;
  granted_scopes: string[];
}

/** 插件包的某一个不可变已发布版本。 */
export interface PluginPackageVersion {
  id: string;
  version: string;
  /** 产物内容的 sha256，两个人可以据此确认内容一致。 */
  digest: string;
  size_bytes: number;
  published_at: string;
  /** 是否为该 workspace 当前运行的版本（若有）。 */
  installed: boolean;
}

/**
 * 发布进本 workspace 的一个插件包。发布是 workspace 私有的：公开目录需要
 * 评审、报告与下架流程，那是与产物存放位置不同的一码事。
 */
export interface PluginPackage {
  id: string;
  plugin_key: string;
  name: string;
  /** 新版本在前。 */
  versions: PluginPackageVersion[];
  created_at: string;
}

/** 插件包列表接口响应。 */
export interface PluginPackageListResponse {
  packages: PluginPackage[];
}

/** 一次托管 surface 的短时、绑定单次安装的启动。 */
export interface PluginSurfaceLaunch {
  /** Multica 的无 cookie 内容 URL，绝不是插件作者自己的服务器。 */
  url: string;
  /** 生成的文档必须向本 frame 出示的一次性凭证。 */
  bridge_token: string;
  version: string;
  digest: string;
}

/** 配置插件的请求体。 */
export interface PluginConfigRequest {
  values: Record<string, unknown>;
}

/** 一次已完成的 hook 调用（返回给调用方）。 */
export interface PluginHookResult {
  /**
   * 宿主（Multica）自己的分类，而非端点返回的。所谓 "refused" 表示调用从未
   * 发出——作用域不足、被限流或安装被停用——对读者来说是不同于「端点应答
   * 不佳」的另一类问题。
   */
  status: string;
  output?: unknown;
  error?: string;
  latency_ms: number;
  hook_key: string;
  trigger: string;
  attempts: number;
}

/**
 * 一次已记录的 hook 调用。属运维遥测：TTL 清理、刻意不带任何请求/响应体——
 * 若保留每个载荷，这张表就成了 workspace 内容的第二份拷贝，且没有任何一条
 * 删除路径。
 */
export interface PluginInvocation {
  id: string;
  hook_key: string;
  trigger: string;
  status: string;
  event_type?: string;
  attempt: number;
  latency_ms: number;
  error?: string;
  delivery_id?: string;
  planned_at?: string;
  created_at: string;
}

/** 铸造出来的插件 token 与签名密钥，两者只在铸造请求里各展示一次。 */
export interface PluginTokenIssue {
  token: string;
  signing_secret: string;
}

/**
 * `mcp` 传输 hook 的服务器当前提供的工具之一。
 *
 * `approved` 是管理员的固定授权。`drifted` 表示该工具确实被批准，但其 schema
 * 已与批准时不符——需要暴露出来而不是被静默重新批准，因为管理员批准的是
 * 一个特定形状，而形状变了就是一次新决策。
 */
export interface PluginMCPTool {
  name: string;
  description: string;
  schema_digest: string;
  approved: boolean;
  drifted: boolean;
}
