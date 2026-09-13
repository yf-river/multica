// ============================================================================
// Runtime 设置页（RuntimeSettingsPage）——「机器详情下嵌套 runtime 设置」的
// 路由落地页
//
// 角色定位：承接形如「runtimes/<machineId>/<runtimeId>/settings」的 URL，把它
// 解析成具体的某台 runtime，然后转交给共享的 RuntimeDetail 渲染。URL 里的
// machineId 允许指向「一台机器」或「机器名下的某个 runtime」两种对象（二者
// 都能归位到同一台 machine），以满足不同入口（机器列表 / runtime 列表）拼出
// 来的两种地址形态。
//
// 数据流：读 runtimes 全量列表缓存（runtimeListOptions）→ decodeRouteParam
// 解码两个路径参数 → resolveRuntimeSettingsTarget 定位 machine + runtime →
// 未命中展示 not-found 空态，命中则把 runtime 及其所在机器上下文交给
// RuntimeDetail（面包屑显示机器层级、机器级别名不再逐行重复）。
// ============================================================================
"use client";

import { Server } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type { AgentRuntime } from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { RuntimeDetail } from "./runtime-detail";
import { buildRuntimeMachines } from "./runtime-machines";
import { useT } from "../../i18n";

// Runtime 设置页：按 URL 中的 machineId / runtimeId 定位到目标 runtime，再
// 交给 RuntimeDetail 渲染。参数可能在 URL 里被编码过（含中文别名等），先
// 解码再做匹配；解码失败就退回原字符串，让「查不到」的空态自然承接。
export function RuntimeSettingsPage({
  machineId,
  runtimeId,
}: {
  machineId: string;
  runtimeId: string;
}) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const { data: runtimes = [], isLoading } = useQuery(runtimeListOptions(wsId));
  const decodedMachineId = decodeRouteParam(machineId);
  const decodedRuntimeId = decodeRouteParam(runtimeId);

  if (isLoading) {
    return (
      <div className="flex h-full flex-col p-6">
        <Skeleton className="h-12 w-1/2" />
        <div className="mt-6 grid grid-cols-3 gap-3">
          <Skeleton className="h-20 rounded-lg" />
          <Skeleton className="h-20 rounded-lg" />
          <Skeleton className="h-20 rounded-lg" />
        </div>
        <Skeleton className="mt-4 h-48 w-full rounded-lg" />
      </div>
    );
  }

  const target = resolveRuntimeSettingsTarget(
    runtimes,
    decodedMachineId,
    decodedRuntimeId,
  );
  if (!target) {
    return (
      <div className="flex h-full flex-col items-center justify-center text-muted-foreground">
        <Server
          aria-hidden="true"
          className="h-10 w-10 text-faint-foreground"
        />
        <p className="mt-3 text-body">{t(($) => $.detail_page.not_found_title)}</p>
        <p className="mt-1 text-caption text-muted-foreground">
          {t(($) => $.detail_page.not_found_hint)}
        </p>
      </div>
    );
  }
  const { machine, runtime } = target;

  return (
    <RuntimeDetail
      runtime={runtime}
      machineHref={paths.runtimeDetail(decodedMachineId)}
      machineLabel={machine.title}
      afterDeleteHref={
        machine.runtimes.length > 1
          ? paths.runtimeDetail(decodedMachineId)
          : paths.runtimes()
      }
    />
  );
}

// 在 runtimes 全量列表里定位 (machineId, runtimeId) 对应的机器与 runtime。
//
// machineId 有两种合法形态：它可能确实是某台 machine 的 id，也可能是那台
// machine 名下某个 runtime 的 id——两种入口拼出的 URL 都应当落到同一台机器。
// 只有 machine 与 runtime 都找到才算命中；返回 null 时调用方渲染 not-found。
export function resolveRuntimeSettingsTarget(
  runtimes: AgentRuntime[],
  machineId: string,
  runtimeId: string,
) {
  const machine = buildRuntimeMachines(runtimes, { now: Date.now() }).find(
    (candidate) =>
      candidate.id === machineId ||
      candidate.runtimes.some((item) => item.id === machineId),
  );
  const runtime = machine?.runtimes.find((candidate) => candidate.id === runtimeId);
  return machine && runtime ? { machine, runtime } : null;
}

// 解码 URL 路径参数。路径里可能出现 URL 编码的字符（如中文机器名/别名），
// decodeURIComponent 会抛错的畸形序列就原样返回，交给下游「找不到」逻辑处理。
function decodeRouteParam(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}
