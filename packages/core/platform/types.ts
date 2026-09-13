// 本文件定义 CoreProvider 的 props 与客户端身份（ClientIdentity）等平台层类型。

import type { LocaleResources, SupportedLocale } from "../i18n";
import type { StorageAdapter } from "../types/storage";

/** 向服务端标识「调用方客户端」。贯穿 ApiClient 与 WSClient，让本应用实例的
 *  所有 HTTP 请求与 WS 连接都带上平台、版本与 OS 标记。 */
export interface ClientIdentity {
  /** 逻辑客户端种类："web" | "cli" | "daemon"。 */
  platform?: string;
  /** 客户端/应用版本字符串（例如 "0.1.0"）。 */
  version?: string;
  /** 粗略的操作系统桶（例如 "macos"、"windows"、"linux"）。 */
  os?: string;
}

/** CoreProvider 的 props。 */
export interface CoreProviderProps {
  children: React.ReactNode;
  /** API 基地址。默认 ""（同源）。 */
  apiBaseUrl?: string;
  /** WebSocket 地址。默认 "ws://localhost:8080/ws"。 */
  wsUrl?: string;
  /** 存储适配器。默认 SSR 安全的 localStorage 包装。 */
  storage?: StorageAdapter;
  /** 用 HttpOnly cookie 而不是 localStorage token 做认证。默认 false。 */
  cookieAuth?: boolean;
  /** 登录成功后回调（例如为 Next.js 中间件种 cookie）。 */
  onLogin?: () => void;
  /** 登出后回调（例如清 cookie）。 */
  onLogout?: () => void;
  /** 向服务端标识调用方客户端（web/CLI/daemon + 版本 + OS）。 */
  identity?: ClientIdentity;
  /** 当前语言，由宿主应用决定。 */
  locale: SupportedLocale;
  /** i18next 资源，宿主已为当前语言服务端预加载。 */
  resources: Record<string, LocaleResources>;
}
