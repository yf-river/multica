// ============================================================================
// Runtimes 页面（RuntimesPage）——工作区里所有运行时间 / 机器的总览页
//
// 角色定位：Web 端 runtimes 集合页。上半部分由 Header 条 + 可选「Mika 设置」
// 卡组成；主体把 runtime 快照按机器（本机 daemon / 云 worker）分组为机器
// 列表，没有机器时列出「未分配的自定义 profile」。空态提供「连接远程机器」
// 入口；Header 的 Cloud Runtime 入口（仅 SaaS）打开云 runtime 对话框。
//
// 数据流：同时挂载 runtimes / runtime profiles / agents / 任务快照 / 聊天会话
// 等查询，用 buildRuntimeMachines 按机器分组、buildWorkloadIndex 聚负载；监听
// `daemon:register` 事件使 runtime 缓存失效（daemon 上线即刷新列表）。
//
// 入口：runtimeRow 详情 / 机器详情 由 MachineRow 跳转；「连接远程」与
// 「Cloud Runtime」分别打开 ConnectRemoteDialog / CloudRuntimeDialog。
// ============================================================================
"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ChevronRight,
  Cloud,
  Loader2,
  Monitor,
  Plus,
  Server,
} from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberNeedsMikaSetup, useBootstrapMika } from "@multica/core/onboarding";
import { MIKA_PLACEHOLDER_EMOJI } from "../../onboarding/components/mika-intro";
import { useRequiredWorkspaceSlug, useWorkspacePaths } from "@multica/core/paths";
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import { chatSessionsOptions } from "@multica/core/chat/queries";
import { runtimeProfileListOptions } from "@multica/core/runtimes";
import { runtimeListOptions, runtimeKeys } from "@multica/core/runtimes/queries";
import { useWSEvent } from "@multica/core/realtime";
import { agentListOptions } from "@multica/core/workspace/queries";
import type { AgentRuntime } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  MikaRuntimeChoice,
  type MikaRuntimeSelection,
} from "./mika-runtime-choice";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "../../layout/collection-page";
import { PageHeader } from "../../layout/page-header";
import { AppLink, useNavigation } from "../../navigation";
import { getMikaOnboarding } from "../../onboarding/templates";
import { ConnectRemoteDialog } from "./connect-remote-dialog";
import { CloudRuntimeDialog } from "./cloud-runtime-dialog";
import { ProviderLogo } from "./provider-logo";
import { buildWorkloadIndex, RuntimeList } from "./runtime-list";
import { pendingRuntimeFromProfile } from "./pending-runtime";
import { buildRuntimeMachines, type RuntimeMachine } from "./runtime-machines";
import { HealthDot, HealthIcon, useHealthLabel } from "./shared";
import { useT, useTimeAgo } from "../../i18n";
import { daemonRuntimesDocsHref } from "./runtime-docs";

export interface RuntimesPageProps {
  /** Web SaaS 专属的 Cloud Runtime 入口开关。 */
  cloudRuntimeEnabled?: boolean;
}

// 每 30 秒心跳：让派生出的「机器健康」能跨过按时间划分的阈值（如
// recently_lost → offline）推进，即使没有新查询数据到达。
function useNowTick(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

/**
 * Runtimes 集合页。
 * @param cloudRuntimeEnabled - 是否展示 Cloud Runtime（SaaS）创建入口。
 */
export function RuntimesPage({
  cloudRuntimeEnabled = false,
}: RuntimesPageProps = {}) {
  const isAuthLoading = useAuthStore((state) => state.isLoading);
  const currentUserId = useAuthStore((state) => state.user?.id);
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const [showConnectDialog, setShowConnectDialog] = useState(false);
  const [showCloudRuntimeDialog, setShowCloudRuntimeDialog] = useState(false);

  const { data: runtimes = [], isLoading: runtimesLoading } = useQuery(
    runtimeListOptions(wsId),
  );
  const { data: runtimeProfiles = [], isLoading: profilesLoading } = useQuery(
    runtimeProfileListOptions(wsId),
  );
  const { data: agents = [], isLoading: agentsLoading } = useQuery(
    agentListOptions(wsId),
  );
  const { data: snapshot = [] } = useQuery(agentTaskSnapshotOptions(wsId));
  // Mika 的「待设置」判定是按成员、而非按工作区的：仅有 agent 并不能说明
  // 「*这个*成员的会话是否打开并跑起来过」。见 memberNeedsMikaSetup。
  const { data: chatSessions = [], isLoading: chatSessionsLoading } = useQuery(
    chatSessionsOptions(wsId),
  );

  const handleDaemonEvent = useCallback(() => {
    qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
  }, [qc, wsId]);
  useWSEvent("daemon:register", handleDaemonEvent);

  const workloadIndex = useMemo(
    () => buildWorkloadIndex(agents, snapshot),
    [agents, snapshot],
  );
  const now = useNowTick();
  const machines = useMemo(
    () =>
      buildRuntimeMachines(runtimes, {
        now,
        workloadByRuntimeId: workloadIndex,
      }),
    [runtimes, now, workloadIndex],
  );
  const orphanProfileRuntimes = useMemo(() => {
    if (machines.some((machine) => machine.mode === "local")) return [];
    return runtimeProfiles.map((profile) => {
      const createdAt = Date.parse(profile.created_at);
      return pendingRuntimeFromProfile({
        profile,
        createdAt: Number.isFinite(createdAt) ? createdAt : 0,
        fallbackMachineName: "Unassigned",
      });
    });
  }, [machines, runtimeProfiles]);

  if (isAuthLoading || runtimesLoading || profilesLoading) {
    return <RuntimesPageSkeleton />;
  }

  const showEmpty =
    machines.length === 0 &&
    orphanProfileRuntimes.length === 0;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeaderBar
        totalCount={machines.length}
        onConnectRemote={() => setShowConnectDialog(true)}
        cloudRuntimeEnabled={cloudRuntimeEnabled}
        onOpenCloudRuntime={() => setShowCloudRuntimeDialog(true)}
      />

      {showEmpty ? (
        <div className="flex flex-1 items-center justify-center p-6">
          <EmptyState onConnectRemote={() => setShowConnectDialog(true)} />
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto flex w-full max-w-[1440px] flex-col p-4 sm:p-6">
            {!agentsLoading &&
              !chatSessionsLoading &&
              memberNeedsMikaSetup(agents, chatSessions) &&
              runtimes.length > 0 && (
              <MikaSetupCard
                workspaceId={wsId}
                runtimes={runtimes}
                runtimesLoading={runtimesLoading}
                currentUserId={currentUserId ?? null}
              />
            )}
            {machines.length > 0 && <MachineList machines={machines} />}
            {orphanProfileRuntimes.length > 0 && (
              <OrphanRuntimeProfiles
                runtimes={orphanProfileRuntimes}
                now={now}
                hasMachines={machines.length > 0}
              />
            )}
          </div>
        </div>
      )}

      {showConnectDialog && (
        <ConnectRemoteDialog onClose={() => setShowConnectDialog(false)} />
      )}
      {cloudRuntimeEnabled && showCloudRuntimeDialog && (
        <CloudRuntimeDialog onClose={() => setShowCloudRuntimeDialog(false)} />
      )}
    </div>
  );
}

/**
 * 一旦有 runtime 存在，创建 Mika（首席参谋 agent）的入口卡。
 *
 * 这个操作打开一个选择器而不是直接开建。它以前会取 `runtimes.find(online) ??
 * runtimes[0]` 并静默地在那台上创建 Mika——但一台机器通常会暴露它装好的
 * 全部 agent CLI（在报告此问题的机器上是九个），所以「第一台在线的」是随意
 * 的，很可能是一台成员根本没打算在上面跑自己参谋官的 CLI。Onboarding 已经
 * 把这事做成显式选择；这里是从另一个入口走到同一个决定，所以用同样的方式
 * 询问，并复用同一套控件。
 */
function MikaSetupCard({
  workspaceId,
  runtimes,
  runtimesLoading,
  currentUserId,
}: {
  workspaceId: string;
  runtimes: AgentRuntime[];
  runtimesLoading?: boolean;
  currentUserId: string | null;
}) {
  const { t } = useT("runtimes");
  const navigation = useNavigation();
  const paths = useWorkspacePaths();
  const wsSlug = useRequiredWorkspaceSlug();
  const bootstrapMika = useBootstrapMika(workspaceId);

  const [open, setOpen] = useState(false);
  // 用旧的启发式给默认值「种」上一个合理选择；关键在于它是可见、可改的，
  // 而不是留空。
  const defaultRuntimeId =
    runtimes.find((runtime) => runtime.status === "online")?.id ??
    runtimes[0]?.id ??
    "";
  const [choice, setChoice] = useState<MikaRuntimeSelection | null>(null);

  const value: MikaRuntimeSelection = choice ?? {
    runtimeId: defaultRuntimeId,
    model: "",
  };
  const runtimeId = value.runtimeId;

  const handleStart = async () => {
    if (!runtimeId || bootstrapMika.isPending) return;
    try {
      const result = await bootstrapMika.mutateAsync({
        workspaceSlug: wsSlug,
        runtimeId,
        model: value.model || undefined,
        ...getMikaOnboarding(),
      });
      setOpen(false);
      navigation.push(paths.chatSession(result.chatSession.id));
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t(($) => $.mika_setup.failed),
      );
    }
  };

  return (
    <>
      <div className="mb-6 flex flex-col gap-4 rounded-xl border bg-card p-5 sm:flex-row sm:items-center">
        <span
          role="img"
          aria-label={t(($) => $.mika_setup.title)}
          className="flex size-10 shrink-0 select-none items-center justify-center rounded-full bg-muted text-title-lg leading-none"
        >
          {MIKA_PLACEHOLDER_EMOJI}
        </span>
        <div className="min-w-0 flex-1">
          <h2 className="text-body font-semibold">
            {t(($) => $.mika_setup.title)}
          </h2>
          <p className="mt-1 text-body leading-relaxed text-muted-foreground">
            {t(($) => $.mika_setup.description)}
          </p>
        </div>
        <Button className="shrink-0" onClick={() => setOpen(true)}>
          {t(($) => $.mika_setup.action)}
        </Button>
      </div>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-[480px]">
          <DialogHeader>
            <DialogTitle>{t(($) => $.mika_setup.dialog_title)}</DialogTitle>
            <DialogDescription>
              {t(($) => $.mika_setup.dialog_description)}
            </DialogDescription>
          </DialogHeader>

          <MikaRuntimeChoice
            runtimes={runtimes}
            runtimesLoading={runtimesLoading}
            currentUserId={currentUserId}
            value={value}
            onChange={setChoice}
            disabled={bootstrapMika.isPending}
          />

          <DialogFooter>
            <Button
              variant="ghost"
              onClick={() => setOpen(false)}
              disabled={bootstrapMika.isPending}
            >
              {t(($) => $.mika_setup.cancel)}
            </Button>
            <Button
              onClick={handleStart}
              disabled={!runtimeId || bootstrapMika.isPending}
            >
              {bootstrapMika.isPending && (
                <Loader2 aria-hidden className="size-4 animate-spin" />
              )}
              {t(($) => $.mika_setup.action)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

function OrphanRuntimeProfiles({
  runtimes,
  now,
  hasMachines,
}: {
  runtimes: ReturnType<typeof pendingRuntimeFromProfile>[];
  now: number;
  hasMachines: boolean;
}) {
  const { t } = useT("runtimes");
  return (
    <section className={hasMachines ? "mt-6" : undefined}>
      <div className="mb-3">
        <h2 className="text-body font-semibold">
          {t(($) => $.profiles.unassigned_title)}
        </h2>
        <p className="mt-1 text-caption text-muted-foreground">
          {t(($) => $.profiles.unassigned_description)}
        </p>
      </div>
      <div className="overflow-hidden rounded-lg border bg-card">
        <RuntimeList runtimes={runtimes} now={now} />
      </div>
    </section>
  );
}

function PageHeaderBar({
  totalCount,
  onConnectRemote,
  cloudRuntimeEnabled,
  onOpenCloudRuntime,
}: {
  totalCount: number;
  onConnectRemote: () => void;
  cloudRuntimeEnabled: boolean;
  onOpenCloudRuntime: () => void;
}) {
  const { t } = useT("runtimes");
  return (
    <CollectionPageHeader
      icon={Server}
      title={t(($) => $.page.title)}
      count={totalCount}
      description={t(($) => $.page.tagline)}
      learnMore={{
        href: daemonRuntimesDocsHref(),
        label: t(($) => $.page.learn_more),
      }}
      actions={
        <>
          {cloudRuntimeEnabled && (
            <CollectionPageHeaderAction
              icon={Cloud}
              label={t(($) => $.cloud_runtime.action)}
              onClick={onOpenCloudRuntime}
            />
          )}
          <CollectionPageHeaderAction
            icon={Plus}
            label={t(($) => $.page.connect_remote)}
            onClick={onConnectRemote}
          />
        </>
      }
    />
  );
}

function MachineList({
  machines,
}: {
  machines: RuntimeMachine[];
}) {
  const { t } = useT("runtimes");
  if (machines.length === 0) {
    return (
      <CollectionPageState
        icon={Server}
        title={t(($) => $.page.empty.title)}
        description={t(($) => $.page.empty.hint)}
      />
    );
  }

  return (
    <div className="overflow-hidden rounded-lg border bg-card">
      <div className="divide-y">
        {machines.map((machine) => (
          <MachineRow key={machine.id} machine={machine} />
        ))}
      </div>
    </div>
  );
}

function MachineRow({ machine }: { machine: RuntimeMachine }) {
  const { t } = useT("runtimes");
  const healthLabel = useHealthLabel();
  const timeAgo = useTimeAgo();
  const paths = useWorkspacePaths();
  const Icon = machine.section === "cloud" ? Cloud : Monitor;
  const locator = machine.id;
  const busyCount = machine.runningCount + machine.queuedCount;
  const body = (
    <>
      <span className="relative flex h-10 w-10 shrink-0 items-center justify-center rounded-lg border bg-background">
        <Icon aria-hidden="true" className="h-4 w-4 text-muted-foreground" />
        <HealthDot
          health={machine.health}
          className="absolute -bottom-0.5 -right-0.5 ring-2 ring-background"
        />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-body font-medium">
          {machine.title}
        </span>
        <span className="mt-1 flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
          <span className="truncate">
            {machine.subtitle ??
              (machine.section === "cloud"
                ? t(($) => $.machine.metrics.cloud_worker)
                : t(($) => $.machine.metrics.local_daemon))}
          </span>
        </span>
      </span>

      <span className="hidden w-36 shrink-0 items-center gap-1.5 text-caption md:flex">
        <HealthIcon health={machine.health} />
        <span>{healthLabel(machine.health)}</span>
      </span>
      <span className="hidden w-40 shrink-0 flex-col gap-1 lg:flex">
        <span className="text-caption text-muted-foreground">
          {t(($) => $.machine.runtime_count, {
            count: machine.runtimes.length,
          })}
        </span>
        <ProviderIconStack providers={machine.providerNames} />
      </span>
      <span className="hidden w-36 shrink-0 text-caption text-muted-foreground xl:block">
        {busyCount > 0
          ? t(($) => $.machine.metrics.workload_hint, {
              running: machine.runningCount,
              queued: machine.queuedCount,
            })
          : t(($) => $.machine.metrics.workload_idle)}
      </span>
      <span className="hidden w-28 shrink-0 text-right text-caption text-muted-foreground lg:block">
        {machine.lastSeenAt ? timeAgo(machine.lastSeenAt) : "—"}
      </span>
      {locator && (
        <ChevronRight
          aria-hidden="true"
          className="h-4 w-4 shrink-0 text-faint-foreground transition-transform group-hover:translate-x-0.5 group-hover:text-muted-foreground"
        />
      )}
    </>
  );

  return (
    <AppLink
      href={paths.runtimeDetail(locator)}
      className="group flex min-w-0 items-center gap-3 px-4 py-3.5 transition-colors hover:bg-accent/40 focus-visible:bg-accent/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
    >
      {body}
    </AppLink>
  );
}

function ProviderIconStack({ providers }: { providers: string[] }) {
  const visible = providers.slice(0, 4);
  const extra = providers.length - visible.length;
  if (visible.length === 0) return null;
  return (
    <span className="flex min-w-0 items-center -space-x-1">
      {visible.map((provider) => (
        <span
          key={provider}
          className="inline-flex h-5 w-5 items-center justify-center rounded bg-background ring-1 ring-border"
        >
          <ProviderLogo provider={provider} className="h-3.5 w-3.5" />
        </span>
      ))}
      {extra > 0 && (
        <span className="inline-flex h-5 min-w-5 items-center justify-center rounded bg-muted px-1 text-micro font-medium text-muted-foreground ring-1 ring-border">
          +{extra}
        </span>
      )}
    </span>
  );
}

function EmptyState({ onConnectRemote }: { onConnectRemote: () => void }) {
  const { t } = useT("runtimes");
  return (
    <CollectionPageState
      icon={Server}
      title={t(($) => $.page.empty.title)}
      description={t(($) => $.page.empty.hint)}
      actions={
        <Button type="button" size="sm" onClick={onConnectRemote}>
          <Plus aria-hidden="true" className="size-3" />
          {t(($) => $.page.connect_remote)}
        </Button>
      }
    />
  );
}

function RuntimesPageSkeleton() {
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader>
        <Skeleton className="h-4 w-24" />
      </PageHeader>
      <div className="mx-auto w-full max-w-[1440px] p-6">
        <div className="overflow-hidden rounded-lg border">
          {Array.from({ length: 5 }).map((_, index) => (
            <div key={index} className="flex h-[76px] items-center gap-3 border-b px-4 last:border-b-0">
              <Skeleton className="h-10 w-10 rounded-lg" />
              <div className="flex-1">
                <Skeleton className="h-4 w-44" />
                <Skeleton className="mt-2 h-3 w-28" />
              </div>
              <Skeleton className="hidden h-4 w-24 md:block" />
              <Skeleton className="hidden h-4 w-28 lg:block" />
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

export default RuntimesPage;
