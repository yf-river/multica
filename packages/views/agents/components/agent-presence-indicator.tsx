"use client";

// 本文件提供 Agent 的“在场状态”指示器，用圆点 + 工作负载标签展示可用性与负载两个维度。

import { Skeleton } from "@multica/ui/components/ui/skeleton";
import type { AgentPresenceDetail } from "@multica/core/agents";
import { availabilityConfig, workloadConfig } from "../presence";
import { useT } from "../../i18n";

interface PresenceIndicatorProps {
  // null/undefined 表示仍在加载。调用方传入在页面层（或单 Agent 视图下通过
  // useAgentPresenceDetail hook）算好的详情。把它做成 prop 是为了避免在长列表里
  // 每一行都各自订阅 hook。
  detail: AgentPresenceDetail | null | undefined;
  // 紧凑模式 = 只显示圆点，无标签、无负载小标签。用于密集的行。
  compact?: boolean;
}

/**
 * 渲染一个 Agent 的两维在场状态：可用性圆点 + 可选的工作负载小标签。
 * 圆点颜色只取决于可用性这一个维度（共 3 种颜色），所以一个运行正常、
 * 但上一个任务失败的 Agent 依然显示绿色圆点——工作负载不再承载任何历史状态。
 *
 * 紧凑模式只保留圆点，用于完整小标签会挤爆一行的密集场景。
 *
 * 纯展示——把已经算好的 detail 对象作为 prop 接收。数据来源由页面级组件负责
 * （单 Agent 用 `useAgentPresenceDetail`，列表用 `useWorkspacePresenceMap`）。
 */
export function AgentPresenceIndicator({
  detail,
  compact,
}: PresenceIndicatorProps) {
  const { t } = useT("agents");
  if (!detail) {
    return compact ? (
      <Skeleton className="h-1.5 w-1.5 rounded-full" />
    ) : (
      <Skeleton className="h-3 w-24 rounded" />
    );
  }

  const av = availabilityConfig[detail.availability];
  const wl = workloadConfig[detail.workload];
  const availabilityLabel = t(($) => $.availability[detail.availability]);
  const workloadLabel = t(($) => $.workload[detail.workload]);
  const isWorking = detail.workload === "working";
  const isQueued = detail.workload === "queued";
  const showQueueBadge = isWorking && detail.queuedCount > 0;
  // queued 的琥珀色来自 workloadConfig，是作为“严重”色调用的——对应
  // “卡在离线运行时上”这一主因。但在健康的运行时上，queued 只是“入队与
  // daemon 认领之间”的短暂竞态，此时用琥珀色会读作并不存在的告警。
  // 所以这里和 availability 组合：online ⇒ 用 muted（瞬态），否则 ⇒
  // 保留琥珀色（真正的卡住信号）。
  const queuedTone =
    detail.availability === "online" ? "text-muted-foreground" : wl.textClass;

  if (compact) {
    return (
      <span
        className="inline-flex items-center"
        title={`${availabilityLabel}${detail.workload !== "idle" ? ` · ${workloadLabel}` : ""}`}
      >
        <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${av.dotClass}`} />
      </span>
    );
  }

  return (
    <span className="inline-flex flex-wrap items-center gap-x-1.5 gap-y-0.5">
      {/* 可用性——圆点 + 标签。单一维度、单一颜色。 */}
      <span className="inline-flex items-center gap-1.5">
        <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${av.dotClass}`} />
        <span className={`text-caption ${av.textClass}`}>{availabilityLabel}</span>
      </span>

      {/* 工作负载——分隔符 + 标签，working/queued 时再带上计数。
          三种负载状态在这里对称渲染：idle 也有自己的 "Idle" 标签，
          这样“没有在场数据”（根本没有小标签）和“Agent 空闲”（明确的 Idle 标签）
          之间的差别才能被看到。已归档的 Agent 完全跳过负载小标签——
          "Archived" 本身已说明一切；"Archived · Idle" 只会是噪音。 */}
      {detail.availability !== "archived" && (
      <span className="inline-flex items-center gap-1">
        <span className="text-caption text-muted-foreground">·</span>
        <span
          className={`text-caption ${
            isQueued ? queuedTone : wl.textClass
          }`}
        >
          {workloadLabel}
        </span>
        {isWorking && (
          <span className="font-mono text-caption tabular-nums text-muted-foreground">
            {detail.runningCount} / {detail.capacity}
          </span>
        )}
        {showQueueBadge && (
          <span className="rounded-md bg-muted px-1 py-0 text-caption font-medium text-muted-foreground">
            {t(($) => $.presence.queue_badge, { count: detail.queuedCount })}
          </span>
        )}
        {/* Queued（无 running）——直接显示排队数，因为没有 running/capacity
            比值可依托。这样能如实暴露离线运行时上的“卡住”状态。 */}
        {isQueued && (
          <span className="font-mono text-caption tabular-nums text-muted-foreground">
            {detail.queuedCount}
          </span>
        )}
      </span>
      )}
    </span>
  );
}
