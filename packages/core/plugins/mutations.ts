// 本文件封装插件相关的各类变更：预览、发布包（远端 zip / 本地目录）、删除包、
// 安装/卸载、配置、启停、MCP 工具审批、hook 调用、token 轮换/吊销等。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { PluginConfigRequest, PluginInstallRequest, PluginPreviewRequest } from "../types";
import { pluginKeys } from "./queries";

/** 失效某工作区全部插件查询的公共逻辑。 */
function useInvalidatePlugins(wsId: string) {
  const queryClient = useQueryClient();
  return () => queryClient.invalidateQueries({ queryKey: pluginKeys.all(wsId) });
}

/**
 * 预览刻意做成 mutation 而不是 query：它是管理员发起的「同意流程」的前半段，
 * 必须在一次显式动作里运行，而不是被某次缓存重拉重放。
 */
export function usePreviewPlugin(wsId: string) {
  return useMutation({
    mutationFn: (request: PluginPreviewRequest) => api.previewPlugin(wsId, request),
  });
}

/**
 * 发布一份插件构建包。已发布的版本不可变，所以这里永远只是「新增一个」——
 * 不可能改变某个已安装工作区正在运行的东西。
 */
export function usePublishPluginPackage(wsId: string) {
  const invalidate = useInvalidatePlugins(wsId);
  return useMutation({
    mutationFn: (bundle: File) => api.publishPluginPackage(wsId, bundle),
    onSettled: invalidate,
  });
}

/** 开发通道：从 MULTICA_PLUGIN_DIR 发布而不是传 zip。 */
export function usePublishLocalPluginPackage(wsId: string) {
  const invalidate = useInvalidatePlugins(wsId);
  return useMutation({
    mutationFn: (name: string) => api.publishLocalPluginPackage(wsId, name),
    onSettled: invalidate,
  });
}

/** 删除一个插件包。 */
export function useDeletePluginPackage(wsId: string) {
  const invalidate = useInvalidatePlugins(wsId);
  return useMutation({
    mutationFn: (packageId: string) => api.deletePluginPackage(wsId, packageId),
    onSettled: invalidate,
  });
}

/** 安装一个插件。 */
export function useInstallPlugin(wsId: string) {
  const invalidate = useInvalidatePlugins(wsId);
  return useMutation({
    mutationFn: (request: PluginInstallRequest) => api.installPlugin(wsId, request),
    onSettled: invalidate,
  });
}

/** 配置某个插件安装（把安装 id 单独拆出，其余请求体透传）。 */
export function useConfigurePlugin(wsId: string) {
  const invalidate = useInvalidatePlugins(wsId);
  return useMutation({
    mutationFn: ({ installationId, ...request }: PluginConfigRequest & { installationId: string }) =>
      api.configurePlugin(wsId, installationId, request),
    onSettled: invalidate,
  });
}

/** 启用 / 停用某个插件安装。 */
export function useSetPluginEnabled(wsId: string) {
  const invalidate = useInvalidatePlugins(wsId);
  return useMutation({
    mutationFn: ({ installationId, enabled }: { installationId: string; enabled: boolean }) =>
      api.setPluginEnabled(wsId, installationId, enabled),
    onSettled: invalidate,
  });
}

/**
 * 锁定某个 `mcp` hook 获准的工具集。
 *
 * `tools` 是管理员想要批准的「完整集合」，绝不是增量——取消勾选一个再保存，
 * 正是撤销它。空数组表示撤回该 hook，之后的任务认领会停止把它提供给 agent。
 */
export function useApprovePluginMCPTools(wsId: string, installationId: string, hookKey: string) {
  const invalidate = useInvalidatePlugins(wsId);
  return useMutation({
    mutationFn: (tools: string[]) => api.approvePluginMCPTools(wsId, installationId, hookKey, tools),
    onSettled: invalidate,
  });
}

/** 卸载某个插件安装。 */
export function useUninstallPlugin(wsId: string) {
  const invalidate = useInvalidatePlugins(wsId);
  return useMutation({
    mutationFn: (installationId: string) => api.uninstallPlugin(wsId, installationId),
    onSettled: invalidate,
  });
}

/**
 * 调用用户主动发起的某个 hook。
 *
 * 用 mutation 而非 query，理由同 preview：它会对第三方服务器发起一次出站调用，
 * 缓存重拉绝不可以重放它。hook 在远端做的事，不该因为组件重挂而重复一遍。
 *
 * 刻意不在 settle 时失效：hook 可能什么都没改，也可能通过 Action API 以自己的
 * 身份写穿了——调用方才知道到底哪个没动、哪个动了，由它去失效自己真正预期
 * 会变的那部分。
 */
export function useInvokePluginHook() {
  return useMutation({
    mutationFn: ({ installationId, hookKey, ...request }: {
      installationId: string;
      hookKey: string;
      trigger: "ui" | "manual";
      issueId?: string;
      input?: unknown;
    }) => api.invokePluginHook(installationId, hookKey, request),
  });
}

/** 轮换某个插件安装的 webhook token。 */
export function useRotatePluginToken(wsId: string) {
  const invalidate = useInvalidatePlugins(wsId);
  return useMutation({
    mutationFn: (installationId: string) => api.rotatePluginToken(wsId, installationId),
    onSettled: invalidate,
  });
}

/** 吊销某个插件安装的 webhook token。 */
export function useRevokePluginToken(wsId: string) {
  const invalidate = useInvalidatePlugins(wsId);
  return useMutation({
    mutationFn: (installationId: string) => api.revokePluginToken(wsId, installationId),
    onSettled: invalidate,
  });
}
