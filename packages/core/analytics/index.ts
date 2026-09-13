// 本文件是前端埋点的粘合层，对 posthog-js 做薄封装。

// 事件目录的唯一事实来源是 `server/internal/analytics/events.go`。本模块只处理
// 后端无法完成的两件事：首次匿名页面浏览时的归因采集，以及登录时的人员身份合并。
// 所有漏斗事件（signup、workspace_created、runtime_registered、issue_executed、
// invite_sent、invite_accepted）都在服务端发出——见 `server/internal/analytics`。
//
// 配置来自后端 `/api/config` 的响应（由服务端的 POSTHOG_API_KEY 填充），
// 而不是 NEXT_PUBLIC_* 环境变量。这样自托管的 Docker 镜像就不会泄露我们的项目 key——
// 它们的后端返回空 key，本模块就保持惰性、什么也不做。

import posthog from "posthog-js";
import { redactExceptionProperties } from "./redact-exception";
import { shouldDropException } from "./exception-dedupe";
import { isBenignException } from "./benign-exceptions";

/** 事件结构版本号，随事件 schema 变化递增。 */
export const EVENT_SCHEMA_VERSION = 2;

const SIGNUP_SOURCE_COOKIE = "multica_signup_source";
// 给每个值设上限，避免过长的 utm_content 把额度耗尽。如果 JSON 仍超过总上限，
// 就整个丢弃 cookie——残缺的 JSON 比没有归因更糟，因为 PostHog 根本解析不了。
const SIGNUP_SOURCE_VALUE_MAX_LEN = 96;
const SIGNUP_SOURCE_MAX_LEN = 512;
const UTM_KEYS = [
  "utm_source",
  "utm_medium",
  "utm_campaign",
  "utm_content",
  "utm_term",
] as const;

let initialized = false;
// auth-initializer 会并行请求 /api/config 和 /api/me——在 config 较慢的路径下，
// identify() 可能先于 initAnalytics() 触发。把最近一次待处理的 identify 缓冲起来
// （只有一条要紧，因为它是每会话一次），在 initAnalytics 里统一冲刷。
let pendingIdentify: { userId: string; props?: Record<string, unknown> } | null = null;
let currentUserId: string | null = null;
let analyticsEnvironment = "dev";
// 前端发出的事件（captureEvent）和人员属性更新（setPersonProperties）也可能在
// 初始化之前到达——和 identify/pageview 一样的 config 竞态。初始化成功后按序重放。
// 它们只携带已识别用户的用户触发信号，所以缓冲区保持得很小（大约一次步骤切换的量）。
type PendingOp =
  | {
      kind: "event";
      name: string;
      props?: Record<string, unknown>;
      options?: CaptureEventOptions;
    }
  | { kind: "set"; props: Record<string, unknown> }
  | { kind: "exception"; error: unknown; props?: Record<string, unknown> };
const pendingOps: PendingOp[] = [];
// 缓存超级属性，让 resetAnalytics() 能在 posthog.reset() 清掉持久化集合后重新注册它们。
// 否则登出 / 切换账号会在下次完整刷新前，静默丢失后续所有事件的 client_type + app_version。
let superProperties: Record<string, unknown> = {};

/** 埋点配置，来自后端 /api/config 的响应。 */
export interface AnalyticsConfig {
  /** PostHog 项目 key。为空时埋点不启用。 */
  key: string;
  /** PostHog API 主机地址。 */
  host: string;
  /**
   * 客户端应用版本——作为 `app_version` 超级属性附加到每个事件上。
   * Web 应用会注入构建时的 tag / sha。可选，因为本地开发可能没有可用版本。
   */
  appVersion?: string;
  /** 环境标识，如 production / staging / dev。 */
  environment?: string;
}

export type ClientType = "web";

/** 目前只有浏览器客户端会发出前端埋点。 */
export function detectClientType(): ClientType {
  return "web";
}

/**
 * 如果存在 key，则初始化 posthog-js。可安全地多次调用；
 * 后续使用相同配置的调用是无副作用的空操作。
 *
 * 返回 `true` 表示埋点真正在运行；`false` 表示被禁用
 * （无 key、SSR 环境，或已用冲突的 key 初始化过——按「沿用现有实例」处理）。
 *
 * @param config - 埋点配置（可为空）
 * @returns 埋点是否已生效
 */
export function initAnalytics(config: AnalyticsConfig | null | undefined): boolean {
  if (typeof window === "undefined") return false;
  if (!config?.key) return false;
  if (initialized) return true;

  posthog.init(config.key, {
    api_host: config.host || "https://us.i.posthog.com",
    // person_profiles=identified_only 让匿名的路过流量在真正 identify 之前不计入
    // 计费事件，这和我们漏斗的设置一致：signup 是第一个真正的漏斗步骤。
    person_profiles: "identified_only",
    // 关掉所有默认开启的自动采集面。我们的漏斗很窄、且是显式的（server/internal/analytics/events.go
    // 里的事件 + 手动 $pageview）。自动采集会用匿名的「点击按钮」「点击链接」噪音淹没
    // Activity 视图，烧掉计费事件额度，还可能在输入框值里采集到用户输入内容。
    // 如果以后想要这些能力，再逐个显式打开。
    capture_pageview: false,
    autocapture: false,
    capture_heatmaps: false,
    capture_dead_clicks: false,
    // 异常自动采集是开启的：posthog-js 挂载 window.onerror + unhandledrejection
    // 处理器，并带着错误堆栈发送 `$exception` 事件。和上面的点击/热力图自动采集不同，
    // 这是显式的失败信号（不是行为噪音），也是 PostHog 原生处理 JS 抛错的那一面——
    // 见 packages/core/diagnostics 的 failure-tier 划分。（生产构建是压缩的；把 source map
    // 上传到 PostHog 才能还原堆栈。）
    //
    // 错误消息可能插值了用户输入（带输入值的校验错误、带 token 的 URL），所以
    // `before_send` 会在事件离开客户端前擦洗消息和 `$exception_list[].value`。
    // 栈帧（代码位置）被保留。见 redact-exception.ts。
    //
    // 擦洗之后，一个会话级保险丝会丢弃相同错误的重复上报，避免渲染循环或反复抛错的
    // 轮询请求在单会话里产生 100+ 条完全相同的 `$exception` 事件（MUL-3331）。
    // 指纹只由已脱敏字段构建，因此 PII 不会进入存储。顺序很重要：先脱敏，再对脱敏后的形状做指纹。
    capture_exceptions: true,
    before_send: (event) => {
      if (event && event.event === "$exception") {
        // 在脱敏前基于原始消息，整体丢弃已知无害的浏览器噪音（如 ResizeObserver loop）。
        // 它们占据主流且没有信号，所以既跳过脱敏也跳过去重保险丝。见 benign-exceptions.ts。
        if (isBenignException(event.properties)) return null;
        redactExceptionProperties(event.properties);
        if (shouldDropException(event.properties)) return null;
      }
      return event;
    },
    disable_session_recording: true,
    disable_surveys: true,
  });
  analyticsEnvironment = normalizeEnvironment(config.environment);
  // 注册超级属性——附加到本客户端发出的每个事件上。`client_type` 是为了埋点 schema 稳定。
  // `app_version` 可选，这样自托管或本地开发中没有版本的构建不会污染该属性。
  // 我们缓存这个集合，让 resetAnalytics() 能在 posthog.reset() 之后重新应用——
  // 因为 reset() 会清掉持久化的超级属性。
  superProperties = {
    client_type: detectClientType(),
    event_schema_version: EVENT_SCHEMA_VERSION,
    environment: analyticsEnvironment,
    is_demo: false,
  };
  if (config.appVersion) superProperties.app_version = config.appVersion;
  posthog.register(superProperties);
  initialized = true;

  // 冲刷在 init 完成前到达的任何 identify()。
  if (pendingIdentify) {
    currentUserId = pendingIdentify.userId;
    posthog.identify(pendingIdentify.userId, pendingIdentify.props);
    pendingIdentify = null;
  }
  // 按原始顺序重放缓冲的事件 / 人员属性更新——漏斗正确性依赖顺序
  // （例如用户在同一个 config 竞态窗口内提交了问卷、又完成了 onboarding）。
  while (pendingOps.length > 0) {
    const op = pendingOps.shift()!;
    if (op.kind === "event") {
      captureNow(op.name, op.props, op.options);
    } else if (op.kind === "exception") {
      posthog.captureException(op.error, withClientEventProperties(op.props));
    } else {
      capturePersonSet(op.props);
    }
  }
  return true;
}

/**
 * 把当前匿名会话合并到已登录用户。每次身份切换（登录 / 会话恢复）必须且只能调用一次。
 * 在 identify 时把归因属性拉进 person_properties，是为了把 UTM / referrer 保留在
 * 用户画像上，而不是每个事件重复发一遍。
 *
 * 在 initAnalytics() 之前的调用会被缓冲——auth-initializer 并行请求 config 和 user，
 * 所以 identify 可能先到达。
 *
 * @param userId - 用户 ID
 * @param userProperties - 用户属性（可选）
 */
export function identify(userId: string, userProperties?: Record<string, unknown>): void {
  currentUserId = userId;
  if (!initialized) {
    pendingIdentify = { userId, props: userProperties };
    return;
  }
  posthog.identify(userId, userProperties);
}

/**
 * 登出时清除客户端身份，让下一次登录干净地合并，不会把上一个用户的事件泄漏进新会话。
 */
export function resetAnalytics(): void {
  currentUserId = null;
  pendingIdentify = null;
  pendingOps.length = 0;
  if (!initialized) return;
  posthog.reset();
  // reset() 也会清掉持久化的超级属性，所以重新注册 init 时设置的那些。
  // 否则登出 / 切换账号之后的每个事件都会缺失 client_type + app_version，直到完整刷新。
  if (Object.keys(superProperties).length > 0) {
    posthog.register(superProperties);
  }
}

/** 捕获前端事件的额外选项。 */
export interface CaptureEventOptions {
  /**
   * 绕过 posthog-js 的批处理定时器，立即把事件放到网络上。
   * 批处理是在同一个即将被冻结或杀死的线程里的 JS 定时器，所以正常排队上报的
   * 失败报告恰恰在最要紧的时刻可能丢失（MUL-4115：三次确定性挂起，零事件送达）。
   * 只为失败遥测保留此选项——常规事件应正常批处理。
   */
  sendInstantly?: boolean;
  /**
   * 事件被交给 posthog.capture 之后触发一次。
   *
   * 这是「交接」而非「送达」：请求可能仍在传输中，posthog-js 也没有提供可等待的
   * 送达回调。绝不要把它当作可以丢弃某物唯一副本的许可——在这里删除持久化状态的
   * 调用方，会在交接与上送之间应用死亡时丢失数据（见 freeze-flush.ts，它改用等待
   * 一个宽限窗口）。
   *
   * 在埋点被禁用的构建上它也永远不会触发，所以任何等待它的事物都需要自己的过期路径。
   */
  onCaptured?: () => void;
}

/**
 * 捕获一个前端发出的事件。大多数漏斗事件在服务端发出（见 `server/internal/analytics`）；
 * 这个封装保留给后端看不到的少数信号——主要是 Web 上 Step 3 的平台分叉选择，
 * 那里的用户点击根本不会往返到处理器。
 *
 * 在 initAnalytics() 之前的调用会按序缓冲，避免迟到的 config 静默吞掉一次步骤切换。
 *
 * @param name - 事件名称
 * @param props - 事件属性（可选）
 * @param options - 捕获选项（可选）
 */
export function captureEvent(
  name: string,
  props?: Record<string, unknown>,
  options?: CaptureEventOptions,
): void {
  if (!initialized) {
    pendingOps.push({ kind: "event", name, props, options });
    return;
  }
  captureNow(name, props, options);
}

function captureNow(
  name: string,
  props?: Record<string, unknown>,
  options?: CaptureEventOptions,
): void {
  posthog.capture(
    name,
    withClientEventProperties(props),
    options?.sendInstantly ? { send_instantly: true } : undefined,
  );
  options?.onCaptured?.();
}

/**
 * 上报一个从未到达 `window.onerror` 的已捕获异常——比如被错误边界吞掉的 React
 * 渲染阶段错误。全局未捕获错误和未处理的 rejection 已由 posthog-js 自动捕获
 * （`capture_exceptions: true`）；这个封装用于那些处理器看不到的边界场景。
 *
 * 目前由 Web 路由级的 `global-error` 调用。区块级的 `@multica/ui` ErrorBoundary
 * 可以在调用点通过传 `onError={captureException}` 选择启用；它并没有全局接线
 * （那些失败已用 fallback UI 优雅降级）。
 *
 * 在 initAnalytics() 之前的调用会按序缓冲，同 captureEvent。
 *
 * @param error - 异常对象
 * @param props - 附加属性（可选）
 */
export function captureException(
  error: unknown,
  props?: Record<string, unknown>,
): void {
  if (!initialized) {
    pendingOps.push({ kind: "exception", error, props });
    return;
  }
  posthog.captureException(error, withClientEventProperties(props));
}

/**
 * 设置（覆盖）当前已识别用户的人员属性。镜像后端的 `Event.Set` 路径——保持两者一致，
 * 这样无论哪一侧最后发出，同一组画像信号（role、use_case、platform_preference）都可查询。
 * 用于可变信号；对于绝不能被覆盖的归因字段，用 `identify(userId, { $set_once: {...} })` 风格。
 *
 * @param props - 人员属性
 */
export function setPersonProperties(props: Record<string, unknown>): void {
  if (!initialized) {
    pendingOps.push({ kind: "set", props });
    return;
  }
  capturePersonSet(props);
}

// `$set` 的公开线上协议是携带 `$set` 属性的空操作事件。在这里封装（而不是直接调用
// `posthog.setPersonProperties`）让我们与版本解耦——旧版 posthog-js 通过
// `posthog.people.set` 暴露同样的协议，而 capture 形式在两种版本下都一致可用。
function capturePersonSet(props: Record<string, unknown>): void {
  posthog.capture("$set", { $set: props });
}

function withClientEventProperties(
  props?: Record<string, unknown>,
): Record<string, unknown> {
  const next: Record<string, unknown> = { ...(props ?? {}) };
  if (currentUserId && next.user_id === undefined) {
    next.user_id = currentUserId;
  }
  if (next.event_schema_version === undefined) {
    next.event_schema_version = EVENT_SCHEMA_VERSION;
  }
  if (next.environment === undefined) {
    next.environment = analyticsEnvironment;
  }
  if (next.is_demo === undefined) {
    next.is_demo = false;
  }
  return next;
}

function normalizeEnvironment(value: string | undefined): string {
  switch ((value || "").trim().toLowerCase()) {
    case "production":
    case "prod":
      return "production";
    case "staging":
    case "stage":
      return "staging";
    case "development":
    case "dev":
    case "test":
    case "local":
      return "dev";
    default:
      return "dev";
  }
}

/**
 * 在浏览器会话的第一次匿名页面加载时，读取 UTM + referrer 并存进 cookie，
 * 供后端在注册时读取。
 *
 * 绝不要用原始 `document.referrer` 做归因——它可能泄露带 `code` / `state` 查询参数的
 * OAuth 回调 URL。我们只保留 referrer 的 origin（scheme + host），这才是漏斗真正需要的。
 *
 * 这个 cookie 就是后端注册事件里 `signup_source` 的读取来源；两个字段刻意用不透明的
 * JSON，这样 schema 演进时无需后端发版。
 */
export function captureSignupSource(): void {
  if (typeof window === "undefined" || typeof document === "undefined") return;
  if (readCookie(SIGNUP_SOURCE_COOKIE)) return;

  const source: Record<string, string> = {};
  const cap = (v: string) =>
    v.length > SIGNUP_SOURCE_VALUE_MAX_LEN ? v.slice(0, SIGNUP_SOURCE_VALUE_MAX_LEN) : v;

  try {
    const params = new URLSearchParams(window.location.search);
    for (const key of UTM_KEYS) {
      const v = params.get(key);
      if (v) source[key] = cap(v);
    }
  } catch {
    // URL API 不可用——静默跳过。
  }

  const refOrigin = safeReferrerOrigin(document.referrer);
  if (refOrigin) source.referrer_origin = cap(refOrigin);

  if (Object.keys(source).length === 0) return;

  const payload = JSON.stringify(source);
  // 直接丢弃而非截断到一半——半截字符串后端解析不了，归因会比缺失更糟。
  if (payload.length > SIGNUP_SOURCE_MAX_LEN) return;

  // 30 天有效期覆盖典型的注册考虑窗口。Lax 是正确的默认——cookie 只被同源认证消费。
  const maxAge = 60 * 60 * 24 * 30;
  document.cookie = `${SIGNUP_SOURCE_COOKIE}=${encodeURIComponent(payload)}; path=/; max-age=${maxAge}; samesite=lax`;
}

function safeReferrerOrigin(referrer: string): string {
  if (!referrer) return "";
  try {
    const url = new URL(referrer);
    if (url.origin === window.location.origin) return "";
    return url.origin;
  } catch {
    return "";
  }
}

function readCookie(name: string): string {
  if (typeof document === "undefined") return "";
  const prefix = `${name}=`;
  const parts = document.cookie ? document.cookie.split("; ") : [];
  for (const part of parts) {
    if (part.startsWith(prefix)) return decodeURIComponent(part.slice(prefix.length));
  }
  return "";
}
