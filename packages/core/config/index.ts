// 本文件维护一份全局「运行时配置」的 Zustand store：CDN、登录注册、daemon 地址、
// 特性开关、API 服务端版本等，这些配置由宿主应用在启动时从服务端拉取后写入，
// 组件通过 Hook 读取使用。

import { createStore } from "zustand/vanilla";
import { useStore } from "zustand";

/**
 * 全局运行时配置的状态结构。
 *
 * 各字段的默认值都按「服务端没下发就当没开/为空」设计，保证老服务端或
 * 未知字段不会让前端表现出危险或错误的行为。
 */
interface ConfigState {
  cdnDomain: string;
  // 为 true 表示 cdnDomain 上的内容是带时限的签名 URL（服务端开启了
  // CloudFront 签名）。渲染器不能把该域名下的裸存储地址当成可直接加载的
  // 媒体源（MUL-3254）。
  cdnSigned: boolean;
  allowSignup: boolean;
  googleClientId: string;
  daemonServerUrl: string;
  daemonAppUrl: string;
  // 自托管开关（#3433）：为 true 时必须隐藏所有「创建工作区」入口。
  // 默认 false，让未知/老服务端保持托管云的行为。
  workspaceCreationDisabled: boolean;
  // 仅自托管下控制 Git 提供方集成（Forgejo / Gitea / GitLab）的开关。
  // 为 false 时整块「设置 → 集成 → Git providers」都隐藏。默认 false，
  // 这样未知/老服务端以及不带该字段的托管云都会隐藏它。
  vcsIntegrationAvailable: boolean;
  featureFlags: Record<string, boolean>;
  // 正在运行的 API 构建版本，展示在「帮助」弹层里，方便自托管运维确认
  // 部署内容。开发构建或不支持该字段的老服务端为空字符串。
  serverVersion: string;
  environmentName: string;
  // 连上的服务端是否会校验 local_directory 执行模式。默认 false，任何未
  // 声明的服务端都保持 false：危险的那类服务端只接受 worktree 模式、丢弃
  // 该字段、仍悄悄在用户工作副本里跑任务（#7113）。会校验但比该信号老的
  // 服务端也会被同一个网兜住——这里无法区分，而两个猜测里只有一个是安全的。
  localWorktreeSupported: boolean;
  // 服务端是否会在创建/更新 agent 时持久化 conversation_starters。
  // 老处理器会接受这个未知字段并返回成功却把它丢掉，所以字段缺失必须按
  // 「不支持」处理（fail closed）。
  agentConversationStartersSupported: boolean;
  setCdnConfig: (config: { cdnDomain: string; cdnSigned?: boolean }) => void;
  setAuthConfig: (config: {
    allowSignup: boolean;
    googleClientId?: string;
    workspaceCreationDisabled?: boolean;
    vcsIntegrationAvailable?: boolean;
  }) => void;
  setDaemonConfig: (config: {
    daemonServerUrl?: string;
    daemonAppUrl?: string;
  }) => void;
  setFeatureFlags: (flags?: Record<string, boolean>) => void;
  setServerVersion: (version?: string) => void;
  setEnvironmentName: (name?: string) => void;
  setLocalWorktreeSupported: (supported?: boolean) => void;
  setAgentConversationStartersSupported: (supported?: boolean) => void;
}

/** 底层的原生 Zustand store，宿主的启动代码往这里写入运行时配置。 */
export const configStore = createStore<ConfigState>((set) => ({
  cdnDomain: "",
  cdnSigned: false,
  allowSignup: true,
  googleClientId: "",
  daemonServerUrl: "",
  daemonAppUrl: "",
  workspaceCreationDisabled: false,
  vcsIntegrationAvailable: false,
  featureFlags: {},
  serverVersion: "",
  environmentName: "",
  localWorktreeSupported: false,
  agentConversationStartersSupported: false,
  setCdnConfig: ({ cdnDomain, cdnSigned = false }) => set({ cdnDomain, cdnSigned }),
  setAuthConfig: ({
    allowSignup,
    googleClientId = "",
    workspaceCreationDisabled = false,
    vcsIntegrationAvailable = false,
  }) => set({ allowSignup, googleClientId, workspaceCreationDisabled, vcsIntegrationAvailable }),
  setDaemonConfig: ({ daemonServerUrl = "", daemonAppUrl = "" }) =>
    set({ daemonServerUrl, daemonAppUrl }),
  setFeatureFlags: (flags = {}) => set({ featureFlags: { ...flags } }),
  setServerVersion: (version = "") => set({ serverVersion: version }),
  setEnvironmentName: (name = "") => set({ environmentName: name }),
  setLocalWorktreeSupported: (supported = false) =>
    set({ localWorktreeSupported: supported === true }),
  setAgentConversationStartersSupported: (supported = false) =>
    set({ agentConversationStartersSupported: supported === true }),
}));

/** React 侧读取配置 store 的 Hook，可传 selector 只订阅需要的片段。 */
export function useConfigStore(): ConfigState;
export function useConfigStore<T>(selector: (state: ConfigState) => T): T;
export function useConfigStore<T>(selector?: (state: ConfigState) => T) {
  return useStore(configStore, selector as (state: ConfigState) => T);
}

/**
 * 从一组布尔特性开关里取某个 key 是否开启。
 *
 * @param flags - 特性开关映射，可能为 undefined
 * @param key - 要查询的开关名
 * @param defaultValue - 开关不存在时的兜底值（默认关）
 * @returns 开关是否开启
 */
export function featureFlagEnabled(
  flags: Readonly<Record<string, boolean>> | undefined,
  key: string,
  defaultValue = false,
): boolean {
  return flags?.[key] ?? defaultValue;
}

/**
 * 响应式读取某个特性开关当前是否开启。
 *
 * @param key - 要查询的开关名
 * @param defaultValue - 开关不存在时的兜底值（默认关）
 * @returns 开关是否开启
 */
export function useFeatureEnabled(key: string, defaultValue = false): boolean {
  return useConfigStore((state) =>
    featureFlagEnabled(state.featureFlags, key, defaultValue),
  );
}

export { BuildInfoContext, type BuildInfo } from "./build-info";
