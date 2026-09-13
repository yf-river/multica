// Runtime 配置档案（profile）的查询与变更 + 删除冲突解析。
// 注意：档案目录与已注册 runtime *实例*（runtimeKeys）是两种资源，失效事件不同；
// 但删除档案会连带移除其注册实例并可能解绑它们的 Agent，所以也要失效实例列表——
// 下面的 mutation 两类缓存都碰。
import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { ApiError } from "../api";
import type {
  CreateRuntimeProfileRequest,
  RuntimeProfile,
  UpdateRuntimeProfileRequest,
} from "../types/agent";
import { runtimeKeys } from "./queries";

// 工作区级自定义 runtime 档案目录的 query key。与 `runtimeKeys`（给已注册 runtime
// *实例*做 key）分开，因为两种资源在不同事件上失效——但档案删除会移除它的注册实例、
// 还可能解绑它们的 Agent，所以也必须失效实例列表；下面 mutation 两者都碰。
export const runtimeProfileKeys = {
  all: (wsId: string) => ["runtime-profiles", wsId] as const,
  list: (wsId: string) => [...runtimeProfileKeys.all(wsId), "list"] as const,
  detail: (wsId: string, profileId: string) =>
    [...runtimeProfileKeys.all(wsId), "detail", profileId] as const,
};

/** 某工作区 runtime 档案列表查询配置。 */
export function runtimeProfileListOptions(wsId: string) {
  return queryOptions({
    queryKey: runtimeProfileKeys.list(wsId),
    queryFn: () => api.listRuntimeProfiles(wsId),
  });
}

/** 创建 runtime 档案的 mutation hook（成功后失效档案目录缓存）。 */
export function useCreateRuntimeProfile(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateRuntimeProfileRequest) =>
      api.createRuntimeProfile(wsId, body),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeProfileKeys.all(wsId) });
    },
  });
}

/** 更新 runtime 档案的 mutation hook（改名/可见性变化会影响 runtime 列表的标签与可用性）。 */
export function useUpdateRuntimeProfile(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      profileId,
      patch,
    }: {
      profileId: string;
      patch: UpdateRuntimeProfileRequest;
    }) => api.updateRuntimeProfile(wsId, profileId, patch),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeProfileKeys.all(wsId) });
      // 改名/可见性变化可能影响 runtime 列表如何标注已绑定实例；那边也一起刷新。
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}

/** 删除 runtime 档案的 mutation hook（成功后档案与实例列表都失效）。 */
export function useDeleteRuntimeProfile(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (profileId: string) => api.deleteRuntimeProfile(wsId, profileId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: runtimeProfileKeys.all(wsId) });
      // 严格 DELETE 在仍有 Agent 绑定时拒绝（409），但一旦成功，已绑定实例的图景
      // 就可能变了——让 runtime 列表保持同步。
      qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    },
  });
}

// 删除因"仍有活跃 Agent 绑定该档案"被拒时，服务器返回带机器可读码的 409。
// 我们原样透出服务器的人类可读文案，让确认弹窗能解释被拒原因而不必自己重推。
// 非 409 与无关的码塌缩成 `null`，让调用方落入通用错误路径。
export interface RuntimeProfileBoundConflict {
  message: string;
}

/** 从错误里解析"档案仍被 Agent 绑定"的 409 冲突；不是该冲突返回 null。 */
export function parseRuntimeProfileBoundConflict(
  err: unknown,
): RuntimeProfileBoundConflict | null {
  if (!(err instanceof ApiError)) return null;
  if (err.status !== 409) return null;
  const body = err.body;
  const fallback = err.message;
  if (body && typeof body === "object") {
    const record = body as Record<string, unknown>;
    const message =
      typeof record.message === "string" && record.message.trim()
        ? record.message
        : typeof record.error === "string" && record.error.trim()
          ? record.error
          : fallback;
    return { message };
  }
  return { message: fallback };
}

export type { RuntimeProfile };
