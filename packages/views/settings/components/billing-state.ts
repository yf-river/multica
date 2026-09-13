// 本文件负责把订阅配额数据整理成计费标签页所需的展示结构，并判断工作区席位容量状态。

import type {
  AutopilotQuotaUsage,
  WorkspaceSubscriptionEntitlements,
  WorkspaceSubscriptionSummary,
} from "@multica/core/types";

/**
 * Autopilot 用量的展示形态，用可辨识联合把三种状态区分开：
 * - unlimited：不限量
 * - unavailable：拿不到用量数据（失败或未开启）
 * - metered：按量计费，携带已用/预留/总量/上限/进度等信息
 */
export type AutopilotUsageView =
  | { kind: "unlimited" }
  | { kind: "unavailable" }
  | {
      kind: "metered";
      used: number;
      reserved: number;
      total: number;
      limit: number;
      progress: number;
      reached: boolean;
      resetAt: string;
    };

/**
 * 把服务端返回的配额数据解析成计费页可直接渲染的展示结构。
 * 配额计量同时算「已完成」和「已预留」的运行数，让进度条和服务端对受限工作区的拦截判断保持一致；
 * 上限模式以及是否已达上限是服务端的事实，本函数只负责把它们换算成展示所需的值。
 * @param entitlements - 工作区订阅权益，用来判断是否不限量。
 * @param usage - 服务端返回的 Autopilot 用量（可能为空）。
 * @param failed - 用量查询是否失败。
 * @returns 解析后的展示结构，可能是不限量、不可用或按量计费三种之一。
 */
export function resolveAutopilotUsage(
  entitlements: WorkspaceSubscriptionEntitlements,
  usage: AutopilotQuotaUsage | undefined,
  failed: boolean,
): AutopilotUsageView {
  // 不限量模式直接返回，无需任何计量数据。
  if (entitlements.limits.autopilotRuns.mode === "unlimited") {
    return { kind: "unlimited" };
  }

  // 只有查询成功、数据存在且配额开关未关闭时，才尝试解析具体的计量数字。
  if (!failed && usage !== undefined && usage.action !== "off") {
    const { used, reserved, total, limit, reached, reset_at: resetAt } = usage;
    if (
      used !== null &&
      reserved !== null &&
      total !== null &&
      limit !== null &&
      reached !== null &&
      resetAt !== null &&
      used >= 0 &&
      reserved >= 0 &&
      limit >= 0 &&
      Number.isFinite(used) &&
      Number.isFinite(reserved) &&
      Number.isFinite(total) &&
      Number.isFinite(limit)
    ) {
      // 上限为 0 时视为已用满；否则把总量占比换算成 0~100 的进度百分比。
      const progress =
        limit === 0
          ? 100
          : Math.min(100, Math.max(0, (total / limit) * 100));

      return {
        kind: "metered",
        used,
        reserved,
        total,
        limit,
        progress,
        reached,
        resetAt,
      };
    }
  }

  return { kind: "unavailable" };
}

/**
 * 判断工作区是否具备「席位容量」信息。
 * 只有当订阅摘要里真的带了 seatCapacity 字段时才返回 true，用于决定是否展示席位相关 UI。
 * @param summary - 工作区订阅摘要，可能为空。
 * @returns 是否具备席位容量信息。
 */
export function hasActiveWorkspaceSeatCapacity(
  summary: WorkspaceSubscriptionSummary | null | undefined,
): boolean {
  return summary?.seatCapacity != null;
}
