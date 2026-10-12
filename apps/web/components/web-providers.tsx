"use client";

/**
 * Web 应用的全局 Provider 装配层：把 core 的能力（API/WS/认证/i18n/身份版本）
 * 与 Web 专属的导航、滚动恢复 Provider 组合成整棵树，供根布局引用。
 */
import { EnvironmentContext } from "@/platform/environment";
import { useMemo } from "react";
import { CoreProvider } from "@multica/core/platform";
import type { LocaleResources, SupportedLocale } from "@multica/core/i18n";
import { useWelcomeStore } from "@multica/core/onboarding";
import packageJson from "../package.json";
import { EnvironmentDocumentTitle } from "@/platform/environment-document-title";
import { WebNavigationProvider } from "@/platform/navigation";
import { WebScrollRestorationProvider } from "@/platform/scroll-restoration";
import {
  setLoggedInCookie,
  clearLoggedInCookie,
} from "@/features/auth/auth-cookie";
import { detectWebOS } from "@/platform/client-os";

// 读取 localStorage 里的旧版 token → 让本次会话维持 token 模式，使「cookie 认证迁移」
// 之前登录的用户不被登出。他们会在下次登出/登录循环中迁移到 cookie 模式
// （登出会清掉 multica_token）。
// 日落计划：等遥测显示仍携带 multica_token 的会话占比 <1% 后，删除此分支并
// 硬编码 `cookieAuth`——localStorage token 暴露于 XSS，正是 cookie 迁移要消灭的东西。
function hasLegacyToken(): boolean {
  if (typeof window === "undefined") return false;
  try {
    return Boolean(window.localStorage.getItem("multica_token"));
  } catch {
    return false;
  }
}

// 从页面 origin 推导 WebSocket 地址，让自托管 / 局域网部署无需显式配置 wsUrl。
// 部署保持同源时，Next.js 运行时 proxy 负责把 /ws 转发到后端。
function deriveWsUrl(): string | undefined {
  if (typeof window === "undefined") return undefined;
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}/ws`;
}

// 优先用构建期版本号（CI 会把 NEXT_PUBLIC_APP_VERSION 设为 git tag/sha，便于在服务端
// 日志区分不同部署）；退回 package.json 版本，保证本地开发也上报点有用的信息。
const WEB_VERSION =
  process.env.NEXT_PUBLIC_APP_VERSION || packageJson.version || "dev";

/**
 * 全局 Provider 组件。
 *
 * @param props.children - 应用子树。
 * @param props.locale / props.resources - 语言与对应文案资源（i18n）。
 * @param props.apiBaseUrl - 可选的 API 基地址（默认走同源/运行时代理）。
 * @param props.wsUrl - 可选的 WebSocket 地址（缺省按页面 origin 推导）。
 * @returns 包裹了 core 与导航能力 Provider 的子树。
 */
export function WebProviders({
  children,
  locale,
  resources,
  apiBaseUrl,
  environmentId = "",
  wsUrl,
}: {
  children: React.ReactNode;
  locale: SupportedLocale;
  resources: Record<string, LocaleResources>;
  apiBaseUrl?: string;
  environmentId?: string;
  wsUrl?: string;
}) {
  const cookieAuth = !hasLegacyToken();
  // 稳定的身份引用：下游依赖它做 key 的 effect 不会因为父组件每次渲染产生新对象而误触发。
  const identity = useMemo(
    () => ({ platform: "web", version: WEB_VERSION, os: detectWebOS() }),
    [],
  );
  return (
    <EnvironmentContext.Provider value={environmentId}>
      <CoreProvider
      apiBaseUrl={apiBaseUrl}
      environmentId={environmentId}
      wsUrl={wsUrl || deriveWsUrl()}
      cookieAuth={cookieAuth}
      onLogin={() => setLoggedInCookie(environmentId)}
      onLogout={() => {
        // welcome-store 里暂存着 onboarding 结束后的瞬态信号。登出时必须清掉，
        // 否则用同一浏览器登录的用户 B 会继承用户 A 的信号，导致
        // <WelcomeAfterOnboarding /> 对 B 根本不属于的 workspace 去
        // listAgents / createIssue。store 自身 docstring 承诺会重置；这里就是接线处。
        useWelcomeStore.getState().reset();
        clearLoggedInCookie(environmentId);
      }}
      identity={identity}
      locale={locale}
      resources={resources}
    >
      <WebNavigationProvider>
        <EnvironmentDocumentTitle />
        <WebScrollRestorationProvider>{children}</WebScrollRestorationProvider>
      </WebNavigationProvider>
      </CoreProvider>
    </EnvironmentContext.Provider>
  );
}
