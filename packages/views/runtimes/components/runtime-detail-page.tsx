// ============================================================================
// Runtime 详情页（RuntimeDetailPage）——单台「机器」维度的详情路由
//
// 角色定位：路由 /runtimes/[runtimeId] 对应的页面，展示一台机器（本机 daemon
// 或云端 worker）的总览：头部是机器信息（名称 / 健康 / 负载 / CLI 操作 + 重命名），
// 主体是这台机器上的 runtime 列表。列表行点击后进入该 runtime 的设置页。
//
// 地址兼容性：路由参数既可能是新链路的「机器 id（daemon id）」，也可能是旧版
// 遗留的「runtime id」——findMachine 负责把两种 id 都定位到所在机器。
//
// 数据流：同时挂载 runtimes / agents / 任务快照 / members / runtime profiles
// 等查询，经 buildRuntimeMachines 聚成机器；本机 daemon 会再把「已绑定机器的
// profile」并进 runtime 列表（pendingRuntimesForProfiles）。监听 daemon:register
// 事件让缓存失效（daemon 上线即刷新）。
// ============================================================================
"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { AlertCircle, Cloud, Monitor, Pencil, Plus, Server } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import { runtimeProfileListOptions } from "@multica/core/runtimes";
import { runtimeKeys, runtimeListOptions } from "@multica/core/runtimes/queries";
import { useWSEvent } from "@multica/core/realtime";
import {
  agentListOptions,
  memberListOptions,
} from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { AppLink } from "../../navigation";
import { buildWorkloadIndex, RuntimeList } from "./runtime-list";
import {
  buildRuntimeMachines,
  sharedCustomName,
} from "./runtime-machines";
import { RenameMachineDialog } from "./rename-machine-dialog";
import { RuntimeProfilesDialog } from "./runtime-profiles-dialog";
import { pendingRuntimesForProfiles } from "./pending-runtime";
import { MachineCliSection } from "./machine-cli-section";
import { HealthIcon, useHealthLabel } from "./shared";
import { useT, useTimeAgo } from "../../i18n";

export interface RuntimeDetailPageProps {
  /** 机器 id（daemon id）；也兼容旧的 runtime id，会先定位到其所在机器。 */
  runtimeId: string;
}

// 每 30 秒跳一次的心跳时钟：让派生出的机器健康能跨过按时间划分的阈值
// （如 recently_lost → offline），即使没有新查询数据到达也能推进。
function useNowTick(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

// daemon/machine 的 id 可能含 URL 特殊字符，路由进来时被编码过；先解码，
// 解码失败（畸形编码串）就原样返回，避免抛异常。
function decodeRouteParam(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

// 在机器列表里定位目标：路由参数既可能是机器 id，也可能是这台机器上某个
// runtime 的 id（旧链接遗留形态）。两样都查不到才认为机器不存在。
function findMachine(
  machines: ReturnType<typeof buildRuntimeMachines>,
  locator: string,
) {
  return (
    machines.find(
      (candidate) =>
        candidate.id === locator ||
        candidate.runtimes.some((runtime) => runtime.id === locator),
    ) ?? null
  );
}

/**
 * 机器级详情页：展示一台机器（本机 daemon / 云 worker）及其上的 runtime 列表。
 *
 * @param runtimeId - 机器 id（daemon 级）。新链接都用机器 id；旧的 runtime id
 *   链接仍然有效，会先展开定位到其所在机器，再展示下属 runtime 的设置入口。
 *
 * 页面行为与权限有关：管理员（owner/admin）对本机 daemon（有 daemonId）可添加
 * 自定义 profile runtime、可对机器下任意 runtime 发起重命名；普通成员只能对
 * 自己拥有的 runtime 重命名。
 */
export function RuntimeDetailPage({
  runtimeId,
}: RuntimeDetailPageProps) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const qc = useQueryClient();
  const healthLabel = useHealthLabel();
  const timeAgo = useTimeAgo();
  const currentUserId = useAuthStore((state) => state.user?.id);
  const { data: runtimes = [], isLoading } = useQuery(runtimeListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: tasks = [] } = useQuery(agentTaskSnapshotOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: runtimeProfiles = [] } = useQuery(
    runtimeProfileListOptions(wsId),
  );
  const now = useNowTick();
  const machineLocator = decodeRouteParam(runtimeId);
  const [renameOpen, setRenameOpen] = useState(false);
  const [createProfileOpen, setCreateProfileOpen] = useState(false);
  const workloadIndex = useMemo(
    () => buildWorkloadIndex(agents, tasks),
    [agents, tasks],
  );
  const baseMachines = useMemo(
    () =>
      buildRuntimeMachines(runtimes, {
        now,
        workloadByRuntimeId: workloadIndex,
      }),
    [runtimes, now, workloadIndex],
  );
  const baseMachine = findMachine(baseMachines, machineLocator);
  const profileRows = useMemo(
    () =>
      runtimeProfiles.map((profile) => {
        const createdAt = Date.parse(profile.created_at);
        return {
          profile,
          createdAt: Number.isFinite(createdAt) ? createdAt : 0,
        };
      }),
    [runtimeProfiles],
  );
  const machineRuntimes = useMemo(() => {
    if (!baseMachine) return [];
    if (baseMachine.mode !== "local") return baseMachine.runtimes;
    return pendingRuntimesForProfiles({
      pendingProfiles: profileRows,
      runtimes: baseMachine.runtimes,
      localDaemonId: baseMachine.daemonId,
      localMachineName: baseMachine.title,
      fallbackMachineName: baseMachine.title,
    });
  }, [baseMachine, profileRows]);
  const machine = baseMachine;
  const handleDaemonEvent = useCallback(() => {
    qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
  }, [qc, wsId]);
  useWSEvent("daemon:register", handleDaemonEvent);

  const currentMember = currentUserId
    ? members.find((member) => member.user_id === currentUserId)
    : null;
  const isAdmin =
    currentMember?.role === "owner" || currentMember?.role === "admin";
  // 「添加自定义 profile runtime」只在管理员且目标为本机 daemon（带 daemonId）
  // 时开放；云 worker 与普通成员都不满足。
  const canAddRuntime =
    isAdmin && machine?.mode === "local" && !!machine.daemonId;
  // 重命名入口基于机器上的某条 runtime 发起请求：管理员取第一条即可，普通成员
  // 取自己拥有的那条；两者都拿不到（无 runtime / 非本人）就不显示重命名按钮。
  const renameTarget = useMemo(() => {
    if (!machine || machine.runtimes.length === 0) return null;
    const editable = isAdmin
      ? machine.runtimes[0]
      : machine.runtimes.find((runtime) => runtime.owner_id === currentUserId);
    if (!editable) return null;
    return {
      runtimeId: editable.id,
      currentName: sharedCustomName(machine.runtimes) ?? "",
    };
  }, [machine, isAdmin, currentUserId]);

  if (isLoading) return <MachineDetailSkeleton />;

  if (!machine) {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 py-16 text-center">
          <AlertCircle className="h-8 w-8 text-destructive" />
          <div>
            <p className="text-body font-medium">
              {t(($) => $.machine.not_found_title)}
            </p>
            <p className="mt-1 text-caption text-muted-foreground">
              {t(($) => $.machine.not_found_hint)}
            </p>
          </div>
          <Button
            size="sm"
            render={<AppLink href={paths.runtimes()} />}
            nativeButton={false}
          >
            {t(($) => $.detail.all_runtimes)}
          </Button>
        </div>
      </div>
    );
  }

  const Icon = machine.section === "cloud" ? Cloud : Monitor;
  const busyCount = machine.runningCount + machine.queuedCount;
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="shrink-0 border-b bg-background px-4 pb-5 pt-3 sm:px-6">
        <div className="mx-auto max-w-[1440px]">
          <div className="flex min-w-0 items-center gap-1.5 text-caption text-muted-foreground">
            <AppLink
              href={paths.runtimes()}
              className="rounded-sm transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {t(($) => $.page.title)}
            </AppLink>
            <span aria-hidden="true">/</span>
            <span className="truncate text-foreground">{machine.title}</span>
          </div>

          <div className="mt-4 flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
            <div className="flex min-w-0 items-start gap-4">
              <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl border bg-card sm:h-14 sm:w-14">
                <Icon
                  aria-hidden="true"
                  className="h-5 w-5 text-muted-foreground sm:h-6 sm:w-6"
                />
              </div>
              <div className="min-w-0 pt-0.5">
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                  <h1 className="min-w-0 text-balance text-title-lg font-semibold tracking-tight sm:text-display-sm">
                    {machine.title}
                  </h1>
                  <span className="inline-flex items-center gap-1.5 text-caption">
                    <HealthIcon health={machine.health} />
                    {healthLabel(machine.health)}
                  </span>
                </div>
                {machine.subtitle && (
                  <p className="mt-1 text-body text-muted-foreground">
                    {machine.subtitle}
                  </p>
                )}
                <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-caption text-muted-foreground">
                  <span>
                    {t(($) => $.machine.runtime_count, {
                      count: machineRuntimes.length,
                    })}
                  </span>
                  <span>
                    {busyCount > 0
                      ? t(($) => $.machine.metrics.workload_hint, {
                          running: machine.runningCount,
                          queued: machine.queuedCount,
                        })
                      : t(($) => $.machine.metrics.workload_idle)}
                  </span>
                  <MachineCliSection
                    machine={machine}
                    currentUserId={currentUserId}
                    canManagePublicRuntimes={isAdmin}
                  />
                  {machine.lastSeenAt && (
                    <span>{timeAgo(machine.lastSeenAt)}</span>
                  )}
                </div>
              </div>
            </div>

            <div className="flex shrink-0 items-center gap-2 self-end lg:self-start">
              {renameTarget && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => setRenameOpen(true)}
                >
                  <Pencil aria-hidden="true" className="h-3.5 w-3.5" />
                  {t(($) => $.machine.rename)}
                </Button>
              )}
            </div>
          </div>
        </div>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto bg-background">
        <div className="mx-auto w-full max-w-[1440px] p-4 sm:p-6">
          <div className="mb-4 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
            <div className="min-w-0">
              <h2 className="text-body font-semibold">
                {t(($) => $.machine.metrics.runtimes)}
              </h2>
              <p className="mt-1 text-caption text-muted-foreground">
                {t(($) => $.machine.select_runtime)}
              </p>
            </div>
            {canAddRuntime && (
              <Button
                type="button"
                size="sm"
                onClick={() => setCreateProfileOpen(true)}
              >
                <Plus aria-hidden="true" className="h-3.5 w-3.5" />
                {t(($) => $.profiles.add_custom)}
              </Button>
            )}
          </div>
          {machineRuntimes.length > 0 ? (
            <div className="overflow-hidden rounded-lg border bg-card">
              <RuntimeList
                runtimes={machineRuntimes}
                now={now}
                machineTitle={machine.title}
                runtimeHref={(childRuntimeId) =>
                  paths.runtimeSettings(machine.id, childRuntimeId)
                }
              />
            </div>
          ) : (
            <div className="flex min-h-48 flex-col items-center justify-center rounded-lg border border-dashed px-6 text-center">
              <Server
                aria-hidden="true"
                className="h-7 w-7 text-faint-foreground"
              />
              <p className="mt-3 text-body font-medium">
                {t(($) => $.machine.no_runtimes_title)}
              </p>
              <p className="mt-1 max-w-sm text-caption text-muted-foreground">
                {t(($) => $.machine.no_runtimes_hint)}
              </p>
            </div>
          )}
        </div>
      </div>

      {renameTarget && (
        <RenameMachineDialog
          open={renameOpen}
          onOpenChange={setRenameOpen}
          wsId={wsId}
          runtimeId={renameTarget.runtimeId}
          currentName={renameTarget.currentName}
        />
      )}
      {canAddRuntime && createProfileOpen && (
        <RuntimeProfilesDialog
          wsId={wsId}
          intent="create"
          machineName={machine.title}
          onClose={() => setCreateProfileOpen(false)}
        />
      )}
    </div>
  );
}

// 机器详情骨架屏：数据加载完成前占位，布局与真实页面对齐，减少跳动感。
function MachineDetailSkeleton() {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="border-b px-6 pb-5 pt-3">
        <Skeleton className="h-3 w-36" />
        <div className="mt-4 flex items-start gap-4">
          <Skeleton className="h-14 w-14 rounded-xl" />
          <div className="flex-1">
            <Skeleton className="h-7 w-64" />
            <Skeleton className="mt-2 h-4 w-40" />
            <Skeleton className="mt-3 h-3 w-72" />
          </div>
        </div>
      </div>
      <div className="mx-auto w-full max-w-[1440px] p-6">
        <Skeleton className="h-4 w-24" />
        <Skeleton className="mt-2 h-3 w-72" />
        <div className="mt-4 overflow-hidden rounded-lg border">
          {Array.from({ length: 4 }).map((_, index) => (
            <Skeleton key={index} className="h-14 w-full rounded-none border-b last:border-b-0" />
          ))}
        </div>
      </div>
    </div>
  );
}
