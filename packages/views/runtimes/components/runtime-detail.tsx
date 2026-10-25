// ============================================================================
// Runtime 详情页（RuntimeDetail）
//
// 角色定位：单台 runtime 的详情视图。顶部面包屑（可含机器层级）；主体左侧
// 是 Hero 卡（标识行 + 归属/设备/运行时事实 + 可折叠的技术 ID）与用量区块
// （UsageSection）；右侧栏是「服务中的 agent」列表与诊断卡（可见性
// public/private 切换 + 删除入口）。
//
// 数据流：父组件传入 runtime 快照；本组件再拉取工作区成员/agent/profile 与
// 在线状态，本地算出健康、归属、可编辑/删除/读取权限后渲染。删除分为两种：
// 自定义 profile runtime 删「配置」，其余删 runtime 本体。
//
// 权限约定：归属者或工作区 owner/admin 可改名/删除；只有 runtime 归属者可
// 编辑可见性（MUL-6126，工作区 admin 也看不到编辑开关）。
//
// 关键 UI 决定：Hero 卡放在滚动容器内而非钉在顶栏下，让滚动条从页面边界
// 开始而不是从内容中部开始；顶栏因属于「导航」而独立吸顶。
// ============================================================================
"use client";

import { useEffect, useState } from "react";
import {
  Trash2,
  ChevronRight,
  Cpu,
  Globe,
  Lock,
} from "lucide-react";
import { toast } from "sonner";
import { useQuery } from "@tanstack/react-query";
import type {
  AgentRuntime,
  Agent,
  MemberWithUser,
  RuntimeProfile,
} from "@multica/core/types";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions, agentListOptions } from "@multica/core/workspace/queries";
import { useUpdateRuntime } from "@multica/core/runtimes/mutations";
import {
  deriveRuntimeHealth,
  isRuntimeUsableForUser,
  runtimeDisplayName,
  runtimeProfileListOptions,
} from "@multica/core/runtimes";
import {
  type AgentPresenceDetail,
  useWorkspacePresenceMap,
} from "@multica/core/agents";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { ActorAvatar } from "../../common/actor-avatar";
import { BreadcrumbHeader } from "../../layout/breadcrumb-header";
import { AppLink, useNavigation } from "../../navigation";
import { availabilityConfig, workloadConfig } from "../../agents/presence";
import { HealthBadge } from "./shared";
import { ProviderLogo } from "./provider-logo";
import { UsageSection } from "./usage-section";
import { DeleteRuntimeDialog } from "./delete-runtime-dialog";
import {
  RevokeRuntimeAccessDialog,
  parseRuntimeAccessRevocationPlan,
  type RuntimeAccessRevocationPlan,
} from "./revoke-runtime-access-dialog";
import { DeleteRuntimeProfileDialog } from "./delete-runtime-profile-dialog";
import { runtimeRowLabel } from "./runtime-machines";
import { useT, useTimeAgo } from "../../i18n";

// 从 metadata 里取 daemon CLI 版本号（仅本机 runtime 上报；云 runtime 无）。
function getCliVersion(metadata: Record<string, unknown>): string | null {
  if (
    metadata &&
    typeof metadata.cli_version === "string" &&
    metadata.cli_version
  ) {
    return metadata.cli_version;
  }
  return null;
}

// 把完整 daemon id 缩短成可展示的短码（如 ab12cd··ef），太长时取头 6 位与
// 尾 2 位。
function shortDaemonId(id: string | null): string | null {
  if (!id) return null;
  if (id.length <= 10) return id;
  return `${id.slice(0, 6)}··${id.slice(-2)}`;
}

// 每 30 秒一次心跳，驱动健康状态随时间窗口（recently_lost → offline →
// long_offline）推进——即便没有任何新查询数据到达。agent 在线状态已经没有
// 时间窗口，本不需要它；但 useWorkspacePresenceMap 是此页面已挂载的依赖，
// 它绑定的是查询数据而不是 `now`。
function useNowTick(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

/**
 * Runtime 详情页。
 *
 * @param runtime - 要展示的 runtime。
 * @param machineHref/machineLabel - 当页面挂在机器详情下时，面包屑中的机器
 *   层级链接与名称。
 * @param afterDeleteHref - 删除完成后的跳转地址；缺省回退到 runtimes 列表。
 */
export function RuntimeDetail({
  runtime,
  machineHref,
  machineLabel,
  afterDeleteHref,
}: {
  runtime: AgentRuntime;
  machineHref?: string;
  machineLabel?: string;
  afterDeleteHref?: string;
}) {
  const { t } = useT("runtimes");
  const timeAgo = useTimeAgo();
  const cliVersion =
    runtime.runtime_mode === "local" ? getCliVersion(runtime.metadata) : null;
  const user = useAuthStore((s) => s.user);
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: profiles = [] } = useQuery(runtimeProfileListOptions(wsId));
  const { byAgent: presenceMap } = useWorkspacePresenceMap(wsId);
  const now = useNowTick();

  const [deleteOpen, setDeleteOpen] = useState(false);

  const health = deriveRuntimeHealth(runtime, now);
  const ownerMember = runtime.owner_id
    ? members.find((m) => m.user_id === runtime.owner_id) ?? null
    : null;

  const currentMember = user
    ? members.find((m) => m.user_id === user.id)
    : null;
  const isAdmin = currentMember
    ? currentMember.role === "owner" || currentMember.role === "admin"
    : false;
  const isRuntimeOwner = user && runtime.owner_id === user.id;
  const canEditRuntime = isAdmin || isRuntimeOwner;
  const canReadRuntime = isRuntimeUsableForUser(runtime, user?.id ?? null);
  const runtimeProfile: RuntimeProfile | null = runtime.profile_id
    ? profiles.find((p) => p.id === runtime.profile_id) ?? null
    : null;
  const isCustomRuntime = !!runtime.profile_id;
  const canDelete = isCustomRuntime
    ? isAdmin && !!runtimeProfile
    : canEditRuntime;

  const servingAgents = agents.filter(
    (a) => a.runtime_id === runtime.id && !a.archived_at,
  );

  const deleteDestination = afterDeleteHref ?? paths.runtimes();

  const handleRuntimeDeleted = () => {
    setDeleteOpen(false);
    navigation.replace(deleteDestination);
    toast.success(t(($) => $.detail.toast_deleted));
  };

  const handleProfileDeleted = () => {
    setDeleteOpen(false);
    navigation.replace(deleteDestination);
  };

  const daemonShort = shortDaemonId(runtime.daemon_id);
  const lastSeen = runtime.last_seen_at
    ? timeAgo(runtime.last_seen_at)
    : t(($) => $.detail.never_seen);
  const runtimeName = machineLabel
    ? runtimeRowLabel(runtime, machineLabel)
    : runtimeDisplayName(runtime);

  return (
    <div className="flex h-full flex-col">
      <BreadcrumbHeader
        segments={[
          { href: paths.runtimes(), label: t(($) => $.page.title) },
          ...(machineHref && machineLabel
            ? [{ href: machineHref, label: machineLabel }]
            : []),
        ]}
        leaf={
          <span className="truncate font-mono text-caption text-foreground">
            {runtimeName}
          </span>
        }
        actions={
          !canEditRuntime ? (
            <span className="inline-flex items-center gap-1 text-caption text-muted-foreground">
              <Lock className="h-3 w-3" />
              {t(($) => $.detail.read_only)}
            </span>
          ) : null
        }
      />

      {/* 主体——单个滚动容器同时承载 Hero 卡与下方的分析区块。把 Hero 放进
          滚动区（而不是钉在顶栏下）能让滚动条从页面边界开始、而不是从内容
          中部开始；顶栏本身吸顶是因为它属于导航而非数据。 */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="grid grid-cols-1 gap-4 p-6 lg:grid-cols-[minmax(0,1fr)_320px]">
          <div className="min-w-0 space-y-5">
            <HeroCard
              runtime={runtime}
              runtimeName={runtimeName}
              health={health}
              lastSeen={lastSeen}
              ownerMember={ownerMember}
              cliVersion={cliVersion}
              daemonShort={daemonShort}
            />
            {canReadRuntime && <UsageSection runtime={runtime} />}
          </div>

          {/* 右侧栏：服务中的 agent + 诊断 */}
          <div className="space-y-4">
            <ServingAgentsCard
              agents={servingAgents}
              presenceMap={presenceMap}
              agentHref={(id) => paths.agentDetail(id)}
            />
            <DiagnosticsCard
              runtime={runtime}
              canEditVisibility={!!isRuntimeOwner}
              canDelete={!!canDelete}
              onDelete={() => setDeleteOpen(true)}
            />
          </div>
        </div>
      </div>

      {isCustomRuntime && runtimeProfile ? (
        <DeleteRuntimeProfileDialog
          open={deleteOpen}
          onOpenChange={setDeleteOpen}
          profile={runtimeProfile}
          wsId={wsId}
          onDeleted={handleProfileDeleted}
        />
      ) : (
        <DeleteRuntimeDialog
          open={deleteOpen}
          onOpenChange={setDeleteOpen}
          runtime={runtime}
          wsId={wsId}
          onDeleted={handleRuntimeDeleted}
        />
      )}
    </div>
  );
}

// `device_info` 以 daemon 拼装的一条复合字符串到达
// （如 "host.local · 2.1.121 (Claude Code)"）。按第一个 " · " 拆开得到
// 「主机名一半 + 运行时版本一半」，Hero 卡里就能分别打标签。只上报主机名的
// 老 runtime 也能正常工作——此时 `runtime` 为空。
function parseDeviceInfo(raw: string): { hostname: string; runtime?: string } {
  const idx = raw.indexOf(" · ");
  if (idx < 0) return { hostname: raw };
  return {
    hostname: raw.slice(0, idx),
    runtime: raw.slice(idx + 3),
  };
}

// Hero 卡：名称行（提供商 logo、名称、健康徽标、最后在线时间）+ 归属/设备/
// 运行时三条事实 + 折叠的「技术详情」（daemon CLI 版本、daemon UUID 短码）。
function HeroCard({
  runtime,
  runtimeName,
  health,
  lastSeen,
  ownerMember,
  cliVersion,
  daemonShort,
}: {
  runtime: AgentRuntime;
  runtimeName: string;
  health: ReturnType<typeof deriveRuntimeHealth>;
  lastSeen: string;
  ownerMember: MemberWithUser | null;
  cliVersion: string | null;
  daemonShort: string | null;
}) {
  const { t } = useT("runtimes");
  const [showDetails, setShowDetails] = useState(false);
  const device = runtime.device_info ? parseDeviceInfo(runtime.device_info) : null;
  const hasTechDetails = !!cliVersion || !!daemonShort;

  return (
    <div className="rounded-lg border bg-card">
      {/* 标识行——提供商 logo、名称、状态徽标、最后在线时间。 */}
      <div className="flex items-start gap-3 border-b p-4">
        <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border bg-card">
          <ProviderLogo provider={runtime.provider} className="h-5 w-5" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <h2 className="truncate text-title-sm font-semibold tracking-tight">
              {runtimeName}
            </h2>
            <HealthBadge health={health} />
            <span className="text-caption text-muted-foreground">
              {t(($) => $.detail.last_seen, { when: lastSeen })}
            </span>
          </div>
        </div>
      </div>

      {/* 面向用户的事实——归属 / 设备 / 运行时，各带标签。取代了旧的 `·`
          分隔的密集元信息条（它把一切——包括仅供开发的 ID——都混在同一
          视觉层级上）。 */}
      <dl className="grid grid-cols-1 divide-y sm:grid-cols-3 sm:divide-x sm:divide-y-0">
        <Fact label={t(($) => $.detail.fact_owner)}>
          {ownerMember ? (
            <span className="inline-flex min-w-0 items-center gap-1.5">
              <ActorAvatar
                actorType="member"
                actorId={ownerMember.user_id}
                size="sm"
                enableHoverCard
              />
              <span className="cursor-pointer truncate text-body">{ownerMember.name}</span>
            </span>
          ) : (
            <span className="text-body text-muted-foreground">—</span>
          )}
        </Fact>
        <Fact label={t(($) => $.detail.fact_device)}>
          {device?.hostname ? (
            <Tooltip>
              <TooltipTrigger
                render={
                  <span className="block truncate font-mono text-caption">
                    {device.hostname}
                  </span>
                }
              />
              <TooltipContent>{device.hostname}</TooltipContent>
            </Tooltip>
          ) : (
            <span className="text-body text-muted-foreground">—</span>
          )}
        </Fact>
        <Fact label={t(($) => $.detail.fact_runtime)}>
          <span className="block truncate text-body">
            {device?.runtime ?? (
              <span className="capitalize">{runtime.provider}</span>
            )}
          </span>
        </Fact>
      </dl>

      {/* 诊断 ID——multica CLI 的 git 哈希 + 截断的 daemon UUID。只在提 bug
          或读日志时有用；默认折叠，免得跟上面的用户可见事实抢注意力。 */}
      {hasTechDetails && (
        <div className="border-t">
          <button
            type="button"
            onClick={() => setShowDetails((v) => !v)}
            className="flex w-full items-center gap-1 px-4 py-2 text-caption text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
          >
            <ChevronRight
              className={`h-3 w-3 transition-transform ${
                showDetails ? "rotate-90" : ""
              }`}
            />
            {t(($) => $.detail.technical_details)}
          </button>
          {showDetails && (
            <dl className="grid grid-cols-1 gap-y-2 border-t bg-muted/30 px-4 py-3 sm:grid-cols-2">
              {cliVersion && (
                <Fact label={t(($) => $.detail.fact_daemon_cli)} mono compact>
                  {cliVersion}
                </Fact>
              )}
              {daemonShort && (
                <Fact label={t(($) => $.detail.fact_daemon_id)} mono compact>
                  {daemonShort}
                </Fact>
              )}
            </dl>
          )}
        </div>
      )}
    </div>
  );
}

function Fact({
  label,
  children,
  mono,
  compact,
}: {
  label: string;
  children: React.ReactNode;
  mono?: boolean;
  compact?: boolean;
}) {
  return (
    <div className={`min-w-0 ${compact ? "" : "px-4 py-3"}`}>
      <dt className="text-micro uppercase tracking-wider text-muted-foreground">
        {label}
      </dt>
      <dd className={`mt-1 ${mono ? "font-mono text-caption" : ""}`}>{children}</dd>
    </div>
  );
}

// 服务中的 agent 卡：列出绑定在本 runtime 上、未归档的 agent，每行显示
// 在线状态 + 工作负载 + 运行/排队计数。行本身可点，跳 agent 详情。
function ServingAgentsCard({
  agents,
  presenceMap,
  agentHref,
}: {
  agents: Agent[];
  presenceMap: Map<string, AgentPresenceDetail>;
  agentHref: (agentId: string) => string;
}) {
  const { t } = useT("runtimes");
  const { t: tAgents } = useT("agents");
  return (
    <div className="rounded-lg border">
      <div className="flex items-center justify-between border-b px-4 py-2.5">
        <span className="text-caption font-semibold">{t(($) => $.detail.serving_title)}</span>
        <span className="text-caption text-muted-foreground">
          {t(($) => $.detail.serving_count, { count: agents.length })}
        </span>
      </div>
      {agents.length === 0 ? (
        <div className="flex flex-col items-center px-4 py-6 text-center">
          <Cpu className="h-5 w-5 text-faint-foreground" />
          <p className="mt-2 text-caption text-muted-foreground">
            {t(($) => $.detail.no_agents)}
          </p>
        </div>
      ) : (
        <div className="divide-y">
          {agents.map((agent) => {
            const detail = presenceMap.get(agent.id);
            const av = detail
              ? availabilityConfig[detail.availability]
              : availabilityConfig.offline;
            const avLabel = tAgents(($) => $.availability[detail?.availability ?? "offline"]);
            const wl = detail ? workloadConfig[detail.workload] : null;
            const running = detail?.runningCount ?? 0;
            const queued = detail?.queuedCount ?? 0;
            return (
              <AppLink
                key={agent.id}
                href={agentHref(agent.id)}
                className="group flex items-center gap-2 px-4 py-2 transition-colors hover:bg-accent/40 focus-visible:bg-accent/40 focus-visible:outline-none"
              >
                <ActorAvatar actorType="agent" actorId={agent.id} size="sm" enableHoverCard showStatusDot />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-caption font-medium">
                    {agent.name}
                  </div>
                  <div className="mt-0.5 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-caption">
                    <span className="inline-flex items-center gap-1.5">
                      <span className={`h-1.5 w-1.5 rounded-full ${av.dotClass}`} />
                      <span className={av.textClass}>{avLabel}</span>
                    </span>
                    {wl && detail && detail.workload !== "idle" && (
                      <span className={`inline-flex items-center gap-1 ${wl.textClass}`}>
                        <span className="text-muted-foreground">·</span>
                        <wl.icon
                          className={`h-3 w-3 ${detail.workload === "working" ? "animate-spin" : ""}`}
                        />
                        {tAgents(($) => $.workload[detail.workload])}
                        {running > 0 && (
                          <span className="text-muted-foreground">{t(($) => $.detail.running_chip, { count: running })}</span>
                        )}
                        {queued > 0 && (
                          <span className="text-muted-foreground">{t(($) => $.detail.queued_chip, { count: queued })}</span>
                        )}
                      </span>
                    )}
                  </div>
                </div>
                <ChevronRight className="h-3.5 w-3.5 shrink-0 text-faint-foreground transition-colors group-hover:text-muted-foreground" />
              </AppLink>
            );
          })}
        </div>
      )}
    </div>
  );
}

// 诊断卡：可见性（归属者可编辑，其他人只读）+ 删除按钮（有权限才显示）。
function DiagnosticsCard({
  runtime,
  canEditVisibility,
  canDelete,
  onDelete,
}: {
  runtime: AgentRuntime;
  /**
   * 仅 runtime 归属者为 true——刻意比卡片上其它操作更窄（MUL-6126）。
   * 是否把机器共享给工作区是归属者的决定，所以工作区 admin 在这里看到的是
   * 只读小标，尽管他们仍可能重命名或删除该 runtime。
   */
  canEditVisibility: boolean;
  canDelete: boolean;
  onDelete: () => void;
}) {
  const { t } = useT("runtimes");
  return (
    <div className="rounded-lg border">
      <div className="border-b px-4 py-2.5">
        <span className="text-caption font-semibold">{t(($) => $.detail.diagnostics_title)}</span>
      </div>
      <div className="space-y-3 p-4">
        <div>
          <div className="mb-1.5 text-micro uppercase tracking-wide text-muted-foreground">
            {t(($) => $.detail.diagnostics_visibility)}
          </div>
          {canEditVisibility ? (
            <VisibilityEditor runtime={runtime} />
          ) : (
            <VisibilityReadout runtime={runtime} />
          )}
        </div>
        {canDelete && (
          // 即使 runtime 是仍在线的本机 daemon（会自愈），按钮也保持可点。
          // 归属者明确要求如此（MUL-3352）——之前在这里禁用按钮，等于让他们
          // 看着一个有权限点却点不了的按钮。对话框会弹出自愈横幅，让用户在
          // 确认前看到取舍。
          <div className="border-t pt-3">
            <Button
              variant="ghost"
              size="sm"
              className="h-8 w-full justify-start gap-2 text-destructive hover:bg-destructive/10 hover:text-destructive"
              onClick={onDelete}
            >
              <Trash2 className="h-3.5 w-3.5" />
              {t(($) => $.detail.delete_button)}
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

// VisibilityReadout 为所有非归属者渲染一个静态的「Private / Public」小标
// （工作区 admin 也包含在内，MUL-6126）。因此它的悬浮提示用第三人称措辞；
// 编辑器自己的提示保持第二人称。描述原先放在小标下方，现在移到悬停提示里，
// 让诊断列保持紧凑并与周边区块一致。不返回该字段的旧后端按严格的默认值
// 渲染成「Private」。
function VisibilityReadout({ runtime }: { runtime: AgentRuntime }) {
  const { t } = useT("runtimes");
  const visibility = runtime.visibility === "public" ? "public" : "private";
  const Icon = visibility === "public" ? Globe : Lock;
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span className="inline-flex items-center gap-1.5 rounded-md border bg-muted/30 px-2 py-1.5 text-caption">
            <Icon className="h-3 w-3 text-muted-foreground" />
            <span className="font-medium">
              {t(($) => $.detail.visibility_label[visibility])}
            </span>
          </span>
        }
      />
      <TooltipContent>
        {t(($) => $.detail.visibility_hint_readonly[visibility])}
      </TooltipContent>
    </Tooltip>
  );
}

// VisibilityEditor 让 runtime 归属者在 public↔private 间切换。仅归属者可
// 用——PATCH 端点会拒绝工作区 admin 的此类修改（canSetRuntimeVisibility）；
// 这只是 UI 门槛，不是安全边界。每个选项的说明文字放在悬停提示里，让两个
// 按钮保持紧凑的图标+标签组合，而不是以前那种与周边卡片抢眼的两行大块。
function VisibilityEditor({ runtime }: { runtime: AgentRuntime }) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const updateRuntime = useUpdateRuntime(wsId);
  const current = runtime.visibility === "public" ? "public" : "private";
  const [revokePlan, setRevokePlan] = useState<RuntimeAccessRevocationPlan | null>(null);
  const [revokeOpen, setRevokeOpen] = useState(false);

  const flip = (next: "private" | "public") => {
    if (next === current) return;
    updateRuntime.mutate(
      { runtimeId: runtime.id, patch: { visibility: next } },
      {
        onSuccess: () =>
          toast.success(
            t(($) => $.detail.visibility_toast_updated, {
              visibility: t(($) => $.detail.visibility_label[next]),
            }),
          ),
        onError: (err) => {
          const plan = parseRuntimeAccessRevocationPlan(err);
          if (plan) {
            setRevokePlan(plan);
            setRevokeOpen(true);
            return;
          }
          toast.error(
            err instanceof Error && err.message
              ? err.message
              : t(($) => $.detail.visibility_toast_failed),
          );
        },
      },
    );
  };

  return (
    <>
      <div className="inline-flex items-center gap-0.5 rounded-md bg-muted p-0.5">
        <VisibilityChoice
          active={current === "private"}
          icon={<Lock className="h-3 w-3" />}
          label={t(($) => $.detail.visibility_label.private)}
          tooltip={t(($) => $.detail.visibility_hint.private)}
          disabled={updateRuntime.isPending}
          onClick={() => flip("private")}
        />
        <VisibilityChoice
          active={current === "public"}
          icon={<Globe className="h-3 w-3" />}
          label={t(($) => $.detail.visibility_label.public)}
          tooltip={t(($) => $.detail.visibility_hint.public)}
          disabled={updateRuntime.isPending}
          onClick={() => flip("public")}
        />
      </div>
      <RevokeRuntimeAccessDialog
        open={revokeOpen}
        onOpenChange={setRevokeOpen}
        runtime={runtime}
        wsId={wsId}
        initialPlan={revokePlan}
      />
    </>
  );
}

function VisibilityChoice({
  active,
  icon,
  label,
  tooltip,
  disabled,
  onClick,
}: {
  active: boolean;
  icon: React.ReactNode;
  label: string;
  tooltip: string;
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            onClick={onClick}
            disabled={disabled}
            className={`inline-flex items-center gap-1.5 rounded px-2 py-1 text-caption font-medium transition-colors ${
              active
                ? "bg-background text-foreground shadow-sm"
                : "text-muted-foreground hover:text-foreground"
            } ${disabled ? "cursor-not-allowed opacity-60" : ""}`}
          >
            <span className="shrink-0">{icon}</span>
            <span>{label}</span>
          </button>
        }
      />
      <TooltipContent>{tooltip}</TooltipContent>
    </Tooltip>
  );
}
