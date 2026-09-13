// 本文件封装插件相关的 React Query 查询：已安装插件、工作区发布过的插件包、
// 某安装的 surface 启动、hook 调用记录与 MCP 工具列表。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** 插件相关查询的 query key 命名空间。 */
export const pluginKeys = {
  all: (wsId: string) => ["workspaces", wsId, "plugins"] as const,
  installed: (wsId: string) => [...pluginKeys.all(wsId), "installed"] as const,
  packages: (wsId: string) => [...pluginKeys.all(wsId), "packages"] as const,
};

/** 查询某个工作区已安装的插件列表。 */
export function pluginInstallationsOptions(wsId: string) {
  return queryOptions({
    queryKey: pluginKeys.installed(wsId),
    queryFn: () => api.listPluginInstallations(wsId),
    enabled: wsId.length > 0,
  });
}

/** 查询该工作区发布过哪些插件包（带各自版本）。 */
export function pluginPackagesOptions(wsId: string) {
  return queryOptions({
    queryKey: pluginKeys.packages(wsId),
    queryFn: () => api.listPluginPackages(wsId),
    enabled: wsId.length > 0,
  });
}

/**
 * 为某个已挂载的插件 surface 面板发起一次「不可缓存」的启动。
 */
export function pluginSurfaceLaunchOptions(
  wsId: string,
  installationId: string,
  surfaceKey: string,
  packageVersionId: string,
  launchInstance: string,
  issueId?: string,
) {
  return queryOptions({
    // 一次启动里带着一次性 bridge token。两个已挂载的面板不能仅仅因为
    // React Query 合并了请求就共享同一份启动。把同一个挂载面板挪到另一个
    // issue 上也需要一次全新启动，因为面向 issue 的 bridge 会被替换掉。
    queryKey: [...pluginKeys.all(wsId), installationId, "surface-launch", surfaceKey, packageVersionId, launchInstance, issueId ?? ""] as const,
    queryFn: () => api.getPluginSurfaceLaunch(wsId, installationId, surfaceKey),
    enabled: wsId.length > 0 && installationId.length > 0 && surfaceKey.length > 0,
    staleTime: 0,
    gcTime: 0,
    refetchOnMount: "always",
    retry: false,
  });
}

/**
 * 某个 hook 最近的调用记录——给对着一个失败端点发呆的作者看。
 *
 * 缓存寿命很短：打开它的意义就是看「刚才发生了什么」，在这里一份陈旧的列表
 * 比一份稍慢的列表更糟。
 */
export function pluginInvocationsOptions(wsId: string, installationId: string) {
  return queryOptions({
    queryKey: [...pluginKeys.all(wsId), installationId, "invocations"] as const,
    queryFn: () => api.listPluginInvocations(wsId, installationId),
    enabled: wsId.length > 0 && installationId.length > 0,
    staleTime: 5_000,
  });
}

/**
 * 某个 `mcp` 传输型 hook 的服务端当前提供哪些工具。
 *
 * 每次读取都会真实连到插件作者的 MCP 服务器，所以任何地方都不预取：它在管理
 * 员打开审批面板并询问时才运行。`staleTime` 很短，因为打开它的目的就是看当前
 * 的工具列表，而「漂移」恰恰是它存在所要暴露的。
 */
export function pluginMCPToolsOptions(wsId: string, installationId: string, hookKey: string) {
  return queryOptions({
    queryKey: [...pluginKeys.all(wsId), installationId, "mcp", hookKey] as const,
    queryFn: () => api.listPluginMCPTools(wsId, installationId, hookKey),
    enabled: wsId.length > 0 && installationId.length > 0 && hookKey.length > 0,
    staleTime: 5_000,
    retry: false,
  });
}
