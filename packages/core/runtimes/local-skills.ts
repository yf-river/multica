// Runtime 本地技能（local skills）与 MCP 清单的发现与导入：
// 发起异步请求后轮询 daemon，直到结果返回或超时。
import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type {
  CreateRuntimeLocalSkillImportRequest,
  RuntimeLocalSkillImportResult,
  RuntimeLocalSkillsResult,
} from "../types";

/** 本地技能查询的缓存 key 工厂。 */
export const runtimeLocalSkillsKeys = {
  all: () => ["runtimes", "local-skills"] as const,
  forRuntime: (runtimeId: string) =>
    [...runtimeLocalSkillsKeys.all(), runtimeId] as const,
};

/** 发现轮询间隔。 */
const POLL_INTERVAL_MS = 500;
/** 发现轮询总超时。 */
const POLL_TIMEOUT_MS = 30_000;
// 导入超时比发现长，因为旧 daemon（batch 之前）每心跳周期只弹一个导入（约 15s）。
// 排 10 个导入时，第 10 个最多可在 pending 等 150s 才被认领，加上 daemon 真正执行
// 导入最多 60s。
//
// 超时不变式：IMPORT_POLL_TIMEOUT_MS 必须超过
// runtimeLocalSkillPendingTimeout + runtimeLocalSkillRunningTimeout
// （server/internal/handler/runtime_local_skills.go）。
// 另见 packages/views/.../runtime-local-skill-import-panel.tsx 的 IMPORT_CONCURRENCY
// 与 server/internal/handler/daemon.go 的 maxLocalSkillImportBatch。
const IMPORT_POLL_TIMEOUT_MS = 4 * 60_000; // 4 分钟

/**
 * 向 daemon 发起一次本地技能发现并轮询到结果。
 * @param runtimeId 目标 runtime id。
 * @returns 技能列表 + 是否支持 + MCP 清单 + MCP 是否可用。
 * @throws 轮询超时或发现失败/超时状态时抛错。
 */
export async function resolveRuntimeLocalSkills(
  runtimeId: string,
): Promise<RuntimeLocalSkillsResult> {
  const initial = await api.initiateListLocalSkills(runtimeId);
  const start = Date.now();
  let current = initial;

  while (current.status === "pending" || current.status === "running") {
    if (Date.now() - start > POLL_TIMEOUT_MS) {
      throw new Error("runtime local skill discovery timed out");
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
    current = await api.getListLocalSkillsResult(runtimeId, initial.id);
  }

  if (current.status === "failed" || current.status === "timeout") {
    throw new Error(current.error || "runtime local skill discovery failed");
  }

  return {
    skills: current.skills ?? [],
    supported: current.supported,
	mcpServers: current.mcp_servers ?? [],
	mcpSupported: current.mcp_supported === true,
  };
}

/**
 * 把本地技能导入发起请求并轮询到终态。
 * @param runtimeId 目标 runtime id。
 * @param payload 导入请求参数。
 * @returns 导入结果：created/updated/conflict。
 * @throws 轮询超时、失败/超时态，或成功却没带回 skill 时抛错。
 */
export async function resolveRuntimeLocalSkillImport(
  runtimeId: string,
  payload: CreateRuntimeLocalSkillImportRequest,
): Promise<RuntimeLocalSkillImportResult> {
  const initial = await api.initiateImportLocalSkill(runtimeId, payload);
  const start = Date.now();
  let current = initial;

  while (current.status === "pending" || current.status === "running") {
    if (Date.now() - start > IMPORT_POLL_TIMEOUT_MS) {
      throw new Error("runtime local skill import timed out");
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
    current = await api.getImportLocalSkillResult(runtimeId, initial.id);
  }

  // 冲突：把细节透传给调用方（确认弹窗展示）。
  if (current.status === "conflict") {
    if (!current.conflict) {
      throw new Error("runtime local skill import conflict missing details");
    }
    return {
      status: "conflict",
      conflict: current.conflict,
    };
  }

  if (current.status === "failed" || current.status === "timeout") {
    throw new Error(current.error || "runtime local skill import failed");
  }
  if (!current.skill) {
    throw new Error("runtime local skill import did not return a skill");
  }

  // 服务端用 action 区分覆盖写入与新建，客户端归一化成 updated/created。
  return {
    status: current.action === "overwrite" ? "updated" : "created",
    skill: current.skill,
  };
}

/** 某 runtime 的本地技能列表查询配置（发起异步发现并轮询）。 */
export function runtimeLocalSkillsOptions(runtimeId: string | null | undefined) {
  return queryOptions({
    queryKey: runtimeId
      ? runtimeLocalSkillsKeys.forRuntime(runtimeId)
      : runtimeLocalSkillsKeys.all(),
    queryFn: () => resolveRuntimeLocalSkills(runtimeId as string),
    enabled: Boolean(runtimeId),
    staleTime: 30_000,
    retry: false,
  });
}

// daemon 在同一次往返里返回本地技能和脱敏后的 MCP 清单。导入流程继续用原来的导出，
// Agent 详情界面用这个面向"能力"的别名。
export const runtimeCapabilitiesOptions = runtimeLocalSkillsOptions;
