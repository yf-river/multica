// ============================================================================
// Runtime 列表（RuntimeList）——一台机器名下的运行时间（runtime）表格
//
// 角色定位：机器详情页里展示运行时间的共享 ListGrid 表格。每行一台 runtime，
// 列包含：名称（+ 自定义/内置徽标、待注册徽标、可见性徽标）、健康状态（含
// 负载后缀）、归属者、agent 头像堆、7 天费用 + 环比、CLI 工具版本、行操作
// 菜单（编辑自定义配置 / 删除）。
//
// 布局范式：这里的 ListGrid「按需取用」——子网格模板 + 可变宽轨道 + 两档
// 响应式（详情窗格会被机器列表挤压，容器驱动的核心列收缩比全宽页面更关键），
// 但没有虚拟滚动/排序/过滤/列开关/批量勾选——一台机器只托管 1~5 个
// runtime，那些都是死重；批量删除 runtime（级联确认重操作）也刻意不提供。
//
// 数据流：父组件传入 runtimes 快照；本组件拉取工作区 agent / 任务快照 /
// 成员 / 自定义 profile 等缓存，本地聚合出 workload（绑定 agent 与运行中/排队
// 任务数）、归属者与可删除性，逐行渲染。
//
// 关键约定：费用列只取 14 天用量算「近 7 天 + 对比上一周」，避免每行都去拉
// 180 天窗口、把列表页压成对 task_usage 的 N×180 天内联聚合。
// ============================================================================
"use client";

import { useMemo, useState } from "react";
import {
  AlertTriangle,
  ExternalLink,
  Globe,
  Loader2,
  MoreHorizontal,
  Pencil,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { useQuery } from "@tanstack/react-query";
import { CurrencyNumberFlow } from "@multica/ui/components/ui/number-flow";
import type {
  Agent,
  AgentRuntime,
  AgentTask,
  MemberWithUser,
  RuntimeProfile,
} from "@multica/core/types";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  agentListOptions,
  memberListOptions,
} from "@multica/core/workspace/queries";
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import {
  deriveRuntimeHealth,
  isRuntimeUsableForUser,
  runtimeProfileListOptions,
  runtimeUsageOptions,
} from "@multica/core/runtimes";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  ListGrid,
  ListGridCell,
  ListGridHeader,
  ListGridHeaderCell,
  ListGridRow,
} from "@multica/ui/components/ui/list-grid";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { useIntentNavigate, useRowLink } from "../../navigation";
import { ActorAvatar } from "../../common/actor-avatar";
import { useViewingTimezone } from "../../common/use-viewing-timezone";
import { ProviderLogo } from "./provider-logo";
import { HealthIcon, useHealthLabel } from "./shared";
import { DeleteRuntimeDialog } from "./delete-runtime-dialog";
import { DeleteRuntimeProfileDialog } from "./delete-runtime-profile-dialog";
import { RuntimeProfilesDialog } from "./runtime-profiles-dialog";
import {
  computeCostInWindow,
  pctChange,
} from "../utils";
import { runtimeRowLabel } from "./runtime-machines";
import {
  customRuntimeRegistrationFailure,
  isDisabledCustomRuntime,
  isPendingCustomRuntime,
  isPendingCustomRuntimeWarning,
  pendingRuntimeCommandName,
} from "./pending-runtime";
import { useT, useTimeAgo } from "../../i18n";

// 表格的列轨道定义见文件头导读（ListGrid「按需取用」范式）。下面两行是
// 窄（核心列）/宽（@2xl 全列）两套网格模板。
const GRID_COLS =
  "grid-cols-[0.75rem_minmax(120px,1fr)_var(--rtc-health)_var(--rtc-kebab)_0.75rem] " +
  "@2xl:grid-cols-[0.75rem_minmax(140px,1fr)_var(--rtc-health)_var(--rtc-owner)_var(--rtc-agents)_var(--rtc-cost)_var(--rtc-cli)_var(--rtc-kebab)_0.75rem]";

const COLUMN_WIDTHS = {
  // 健康列把负载折进来作后缀（"Healthy · 2 running"）——与 agent 列表的
  // 状态单元格同一种合并法。
  health: 176,
  owner: 96,
  agents: 92,
  cost: 96,
  cli: 112,
} as const;

// 固定轨道（两侧边距 12+12、名称列最小 140）加上宽模板 9 条轨道间的
// 8 个 gap-x-3 间距（零宽轨道仍会携带间距）。
const FIXED_TRACKS_WIDTH = 164 + 8 * 12;

// 操作（kebab）轨道与归属者列一样是条件性的：当列表里没有任何行拥有删除
// 权限时，每行的唯一操作都被隐藏——若无条件预留 28px 的操作轨道，最后一列
// 后面就会挂着一片永久的死区。
function columnTrackVars(
  showOwner: boolean,
  showActions: boolean,
): React.CSSProperties {
  const minWidth =
    FIXED_TRACKS_WIDTH +
    COLUMN_WIDTHS.health +
    (showOwner ? COLUMN_WIDTHS.owner : 0) +
    COLUMN_WIDTHS.agents +
    COLUMN_WIDTHS.cost +
    COLUMN_WIDTHS.cli +
    (showActions ? 28 : 0);
  return {
    "--rtc-health": `${COLUMN_WIDTHS.health}px`,
    "--rtc-owner": showOwner ? `${COLUMN_WIDTHS.owner}px` : "0px",
    "--rtc-agents": `${COLUMN_WIDTHS.agents}px`,
    "--rtc-cost": `${COLUMN_WIDTHS.cost}px`,
    "--rtc-cli": `${COLUMN_WIDTHS.cli}px`,
    "--rtc-kebab": showActions ? "1.75rem" : "0px",
    "--rtc-minw": `${minWidth}px`,
  } as React.CSSProperties;
}

// 每台 runtime 的负载快照——为该 runtime 服务的 agent（.length 兼作 agent
// 数；驱动头像堆）以及按状态拆分的任务数。每次渲染基于工作区级 agent /
// 任务快照缓存构建一次，本地过滤——零额外请求。
interface RuntimeWorkload {
  agentIds: string[];
  runningCount: number;
  queuedCount: number;
}

const EMPTY_WORKLOAD: RuntimeWorkload = {
  agentIds: [],
  runningCount: 0,
  queuedCount: 0,
};

// 列表每一行的数据：runtime 本体 + 其关联的自定义 profile（若有）+ 归属者
// 成员 + 负载 + 当前用户是否可删除。
export interface RuntimeRow {
  runtime: AgentRuntime;
  profile: RuntimeProfile | null;
  ownerMember: MemberWithUser | null;
  workload: RuntimeWorkload;
  canDelete: boolean;
}

/**
 * 遍历工作区 agents 与任务快照，构建「runtime_id → 负载」索引。
 * agent 必须先绑定到 runtime 且未归档；任务按状态计入 running / queued
 * （dispatched 视作排队中）。
 */
export function buildWorkloadIndex(
  agents: Agent[],
  tasks: AgentTask[],
): Map<string, RuntimeWorkload> {
  const result = new Map<string, RuntimeWorkload>();
  const agentToRuntime = new Map<string, string>();

  for (const a of agents) {
    if (!a.runtime_id || a.archived_at) continue;
    agentToRuntime.set(a.id, a.runtime_id);
    const entry =
      result.get(a.runtime_id) ?? {
        agentIds: [],
        runningCount: 0,
        queuedCount: 0,
      };
    entry.agentIds.push(a.id);
    result.set(a.runtime_id, entry);
  }
  for (const t of tasks) {
    const rid = agentToRuntime.get(t.agent_id);
    if (!rid) continue;
    const entry = result.get(rid);
    if (!entry) continue;
    if (t.status === "running") entry.runningCount += 1;
    else if (t.status === "queued" || t.status === "dispatched")
      entry.queuedCount += 1;
  }
  return result;
}

// ---------------------------------------------------------------------------
// 单元格
// ---------------------------------------------------------------------------

function RuntimeNameCell({
  runtime,
  machineTitle,
}: {
  runtime: AgentRuntime;
  /**
   * 所在机器的标题。作用是让「per-runtime 别名」在此展示，而「机器级重命名」
   * （daemon 上所有 runtime 共享）会折叠回厂商基名，避免每行重复（MUL-5248）。
   * 当该行没有机器上下文（孤儿自定义 profile）时省略——此时任何别名原样显示。
   */
  machineTitle?: string;
}) {
  const label = runtimeRowLabel(runtime, machineTitle ?? "");
  return (
    <ListGridCell className="gap-2">
      <div className="flex h-8 w-8 shrink-0 items-center justify-center">
        <ProviderLogo provider={runtime.provider} className="h-5 w-5" />
      </div>
      <div className="flex min-w-0 flex-1 items-center gap-1.5">
        <span className="block min-w-0 shrink truncate text-body font-medium">
          {label}
        </span>
        <RuntimeKindBadge runtime={runtime} />
        <PendingRuntimeBadge runtime={runtime} />
        <VisibilityBadge runtime={runtime} />
      </div>
    </ListGridCell>
  );
}

// 区分「内置协议族 runtime」与「从自定义 profile 启动的 runtime」。
// `profile_id` 是判别依据：非空即表示由自定义 profile 启动。旧后端不返回该
// 字段——按内置处理。
function RuntimeKindBadge({ runtime }: { runtime: AgentRuntime }) {
  const { t } = useT("runtimes");
  const isCustom = !!runtime.profile_id;
  return (
    <span
      className={
        isCustom
          ? "inline-flex shrink-0 items-center rounded bg-info/10 px-1 text-micro font-medium text-info"
          : "inline-flex shrink-0 items-center rounded bg-muted px-1 text-micro font-medium text-muted-foreground"
      }
    >
      {isCustom
        ? t(($) => $.list.badge_custom)
        : t(($) => $.list.badge_builtin)}
    </span>
  );
}

function PendingRuntimeBadge({ runtime }: { runtime: AgentRuntime }) {
  const { t } = useT("runtimes");
  if (!isPendingCustomRuntime(runtime)) return null;
  if (isDisabledCustomRuntime(runtime)) {
    return (
      <span className="inline-flex shrink-0 items-center rounded bg-muted px-1 text-micro font-medium text-muted-foreground">
        {t(($) => $.list.badge_disabled)}
      </span>
    );
  }
  return (
    <span className="inline-flex shrink-0 items-center rounded bg-warning/10 px-1 text-micro font-medium text-warning">
      {t(($) => $.list.badge_registering)}
    </span>
  );
}

// 只有 public 值得显示徽标——private 是默认值，若每行都画一个 `🔒 Private`
// 小标，整列都会变成噪音。
function VisibilityBadge({ runtime }: { runtime: AgentRuntime }) {
  const { t } = useT("runtimes");
  if (runtime.visibility !== "public") return null;
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span className="inline-flex shrink-0 items-center gap-0.5 rounded bg-info/10 px-1 text-micro font-medium text-info">
            <Globe className="h-2.5 w-2.5" />
            {t(($) => $.detail.visibility_label.public)}
          </span>
        }
      />
      <TooltipContent>
        {t(($) => $.detail.visibility_hint.public)}
      </TooltipContent>
    </Tooltip>
  );
}

// 健康状态并把负载折进来作 "· N tasks" 后缀——与 agent 列表状态单元格的
// 形式完全一致，让两处 UI 说同一种语言。后缀是带单位的计数（运行中 + 排队）；
// 离线类的行省略（健康文案已说明一切）、空闲行省略（空闲是常态默认值）。
// 若「在排队但没有任务运行」将来成为值得展示的信号，它应属于 HEALTH 层
// （新的 deriveRuntimeHealth 状态），而不是这里的措辞提示。
function HealthCell({
  runtime,
  workload,
  now,
}: {
  runtime: AgentRuntime;
  workload: RuntimeWorkload;
  now: number;
}) {
  const { t } = useT("runtimes");
  const { t: tAgents } = useT("agents");
  const labelOf = useHealthLabel();
  const timeAgo = useTimeAgo();
  if (isDisabledCustomRuntime(runtime)) {
    return (
      <ListGridCell>
        <span className="text-caption text-muted-foreground">
          {t(($) => $.list.pending_health_disabled)}
        </span>
      </ListGridCell>
    );
  }
  const registrationFailure = customRuntimeRegistrationFailure(runtime);
  if (registrationFailure) {
    return (
      <ListGridCell className="gap-1.5">
        <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-destructive" />
        <span
          className="block min-w-0 truncate text-caption text-destructive"
          title={registrationFailure}
        >
          {t(($) => $.list.pending_health_error)}
        </span>
      </ListGridCell>
    );
  }
  if (isPendingCustomRuntime(runtime)) {
    const warning = isPendingCustomRuntimeWarning(runtime, now);
    return (
      <ListGridCell className="gap-1.5">
        {warning ? (
          <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-warning" />
        ) : (
          <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-info" />
        )}
        <span className="block min-w-0 truncate text-caption">
          {warning
            ? t(($) => $.list.pending_health_warning)
            : t(($) => $.list.pending_health)}
        </span>
      </ListGridCell>
    );
  }

  const health = deriveRuntimeHealth(runtime, now);
  const offline = health === "offline" || health === "long_offline";
  const lastSeen = runtime.last_seen_at ? timeAgo(runtime.last_seen_at) : null;
  const active = workload.runningCount + workload.queuedCount;

  return (
    <ListGridCell className="gap-1.5">
      <HealthIcon health={health} />
      <span className="block min-w-0 truncate text-caption">
        {labelOf(health)}
        {health !== "online" && lastSeen && (
          <span className="text-muted-foreground"> · {lastSeen}</span>
        )}
        {!offline && active > 0 && (
          <span className="text-muted-foreground">
            {" · "}
            {tAgents(($) => $.row.task_count, { count: active })}
          </span>
        )}
      </span>
    </ListGridCell>
  );
}

// 每行费用——只渲染「近 7 天总额 + 与再前 7 天的环比」，因此只需要 14 天
// 用量。以前也拉 180 天窗口、为了与 runtime 详情页共享缓存键，但那样会把
// 列表页变成对 task_usage 的 N×180 天内联聚合（每行都算一遍），此视图的
// DB 负载占大头。详情页仍会在导航时自拉 180 天窗口；对详情来说冷加载只多
// 一次请求，而列表页的稳态节省是巨大的。
const COST_CELL_DAYS = 14;

/**
 * 当前用户能否读取该 runtime 的用量（费用列显示与否的判断）。
 */
export function canReadRuntimeUsage(
  runtime: AgentRuntime,
  currentUserId: string | null,
): boolean {
  return isRuntimeUsableForUser(runtime, currentUserId);
}

/**
 * 单行费用单元格：近 7 天总额 + 与上一周对比的涨跌。
 *
 * @param runtimeId - runtime id。
 * @param enabled - 是否启用查询（无读取权限的行不请求）。
 */
export function CostCell({
  runtimeId,
  enabled,
}: {
  runtimeId: string;
  enabled: boolean;
}) {
  const { t, i18n } = useT("runtimes");
  const tz = useViewingTimezone();
  const locales = i18n.resolvedLanguage ?? i18n.language;
  const { data: usage = [] } = useQuery({
    ...runtimeUsageOptions(runtimeId, COST_CELL_DAYS, tz),
    enabled,
  });
  const cost7d = useMemo(() => computeCostInWindow(usage, 7, tz), [usage, tz]);
  const costPrev7d = useMemo(
    () => computeCostInWindow(usage, 7, tz, 7),
    [usage, tz],
  );
  const delta = pctChange(cost7d, costPrev7d);

  if (usage.length === 0) {
    return (
      <div className="w-full text-right">
        <span className="text-caption text-faint-foreground">—</span>
      </div>
    );
  }
  const fmt = cost7d >= 100 ? `$${cost7d.toFixed(0)}` : `$${cost7d.toFixed(2)}`;
  const deltaTone =
    delta == null
      ? "text-muted-foreground"
      : delta > 0
        ? "text-warning"
        : delta < 0
          ? "text-success"
          : "text-muted-foreground";
  const deltaLabel =
    delta == null
      ? null
      : delta === 0
        ? t(($) => $.list.cost_delta_flat)
        : `${delta > 0 ? "↑" : "↓"}${Math.abs(delta)}%`;
  return (
    <div className="flex w-full flex-col items-end leading-tight">
      <CurrencyNumberFlow
        value={cost7d}
        locales={locales}
        aria-label={fmt}
        className="text-body font-medium"
      />
      {deltaLabel && (
        <span className={`text-micro tabular-nums ${deltaTone}`}>
          {deltaLabel}
        </span>
      )}
    </div>
  );
}

/**
 * CLI 版本列：显示该 agent 底层 CLI 工具自身的版本。
 */
export function CliCell({ runtime }: { runtime: AgentRuntime }) {
  const { t } = useT("runtimes");
  const failure = customRuntimeRegistrationFailure(runtime);
  if (failure) {
    const command = pendingRuntimeCommandName(runtime);
    return (
      <div className="flex min-w-0 flex-col text-caption">
        {command && (
          <span
            className="truncate font-mono text-muted-foreground"
            title={command}
          >
            {command}
          </span>
        )}
        <span className="truncate text-destructive" title={failure}>
          {failure}
        </span>
      </div>
    );
  }
  if (isPendingCustomRuntime(runtime)) {
    const command = pendingRuntimeCommandName(runtime);
    if (!command) {
      return (
        <span className="text-caption text-muted-foreground">
          {t(($) => $.list.pending_cli_unknown)}
        </span>
      );
    }
    return (
      <div className="flex min-w-0 items-center text-caption">
        <span
          className="truncate font-mono text-muted-foreground"
          title={command}
        >
          {command}
        </span>
      </div>
    );
  }

  if (runtime.runtime_mode === "cloud") {
    return <span className="text-caption text-faint-foreground">—</span>;
  }
  const meta = runtime.metadata as Record<string, unknown> | null;
  // `version` 是 agent 底层 CLI 工具自身的版本——随厂商不同而不同
  // （如 "2.1.5 (Claude Code)"、"codex-cli 0.118.0"、"0.42.0"）。
  // 另一个 `cli_version` 是共享的 multica daemon CLI，同一台机器上所有
  // runtime 都相同；把它显示在这里会让所有 agent 显示同一个数字 (#3838)。
  // daemon CLI 版本及其升级提示属于「机器」，它们应在机器头部，而不是
  // 每个 agent 行上。
  const version =
    meta && typeof meta.version === "string" ? meta.version : null;

  if (!version) {
    return <span className="text-caption text-faint-foreground">—</span>;
  }

  return (
    <div className="flex min-w-0 items-center text-caption">
      <span className="truncate font-mono text-muted-foreground">
        {version}
      </span>
    </div>
  );
}

// 最多叠 3 个头像，还有更多 agent 绑定时追加一个 "+N" 圆点。每个头像都用
// 包裹后的 ActorAvatar，悬停自动弹出 AgentProfileCard。
function AgentStack({ agentIds }: { agentIds: string[] }) {
  if (agentIds.length === 0) {
    return <span className="text-caption text-faint-foreground">—</span>;
  }
  const visible = agentIds.slice(0, 3);
  const extra = agentIds.length - visible.length;
  return (
    <div className="flex items-center -space-x-1.5">
      {visible.map((id) => (
        <span
          key={id}
          className="inline-flex rounded-full ring-2 ring-background"
        >
          <ActorAvatar
            actorType="agent"
            actorId={id}
            size="md"
            enableHoverCard
          />
        </span>
      ))}
      {extra > 0 && (
        <span className="inline-flex h-6 w-6 items-center justify-center rounded-full bg-muted text-caption font-medium text-muted-foreground ring-2 ring-background">
          +{extra}
        </span>
      )}
    </div>
  );
}

/**
 * 行操作菜单：在新页签打开详情 / 编辑自定义 profile / 删除。
 *
 * @param canDelete - 当前用户是否有删除权限（没有权限时整列都不渲染菜单）。
 * @param detailHref - 行详情跳转目标；待注册的自定义 runtime 不可导航时省略，
 *   与列表本身的行链接行为一致。
 */
export function RuntimeRowMenu({
  runtime,
  profile,
  wsId,
  canDelete,
  detailHref,
}: {
  runtime: AgentRuntime;
  profile: RuntimeProfile | null;
  wsId: string;
  canDelete: boolean;
  /**
   * 行的详情目标。当行没有详情页时省略——待注册的自定义 runtime 不可导航，
   * 与列表自身的行链接行为保持一致。
   */
  detailHref?: string;
}) {
  const { t } = useT("runtimes");
  const { t: tCommon } = useT("common");
  const intentNavigate = useIntentNavigate();
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const isCustomRuntime = !!runtime.profile_id;
  // 删除是该行唯一的管理动作；若该行不能执行，就干脆不画 kebab 按钮，
  // 免得这列弹出一个近乎空白的菜单。以前我们也为自愈 runtime（在线本机
  // daemon 几秒内会重新注册）隐藏它，但 MUL-3352 表明：归属者把「菜单消失」
  // 读成「我丢了权限」而不是「daemon 会撤销这次删除」。现在删除对话框自己
  // 带着自愈警告，决定权交给用户。

  if (!canDelete) {
    return <span aria-hidden />;
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <button
              type="button"
              aria-label={t(($) => $.list.row_actions_aria)}
              className="flex size-7 items-center justify-center rounded-md text-muted-foreground opacity-0 transition-opacity hover:bg-accent hover:text-accent-foreground focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring group-hover/row:opacity-100 data-popup-open:bg-accent data-popup-open:opacity-100 data-popup-open:text-accent-foreground"
            >
              <MoreHorizontal className="size-4" />
            </button>
          }
        />
        <DropdownMenuContent align="end" className="w-40">
          {detailHref && (
            <>
              <DropdownMenuItem
                onClick={() => intentNavigate(detailHref, "foreground-tab")}
              >
                <ExternalLink aria-hidden="true" className="h-3.5 w-3.5" />
                {tCommon(($) => $.navigation.open_in_new_tab)}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
            </>
          )}
          {isCustomRuntime && profile && (
            <DropdownMenuItem onClick={() => setEditOpen(true)}>
              <Pencil aria-hidden="true" className="h-3.5 w-3.5" />
              {t(($) => $.list.edit_action)}
            </DropdownMenuItem>
          )}
          <DropdownMenuItem
            variant="destructive"
            onClick={() => setDeleteOpen(true)}
            title={t(($) => $.list.delete_permission_hint)}
          >
            <Trash2 aria-hidden="true" className="h-3.5 w-3.5" />
            {isCustomRuntime
              ? t(($) => $.list.delete_profile_action)
              : t(($) => $.list.delete_action)}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {isCustomRuntime && profile && editOpen && (
        <RuntimeProfilesDialog
          wsId={wsId}
          intent="edit"
          initialProfile={profile}
          onClose={() => setEditOpen(false)}
        />
      )}
      {isCustomRuntime && profile ? (
        <DeleteRuntimeProfileDialog
          open={deleteOpen}
          onOpenChange={setDeleteOpen}
          profile={profile}
          wsId={wsId}
          onDeleted={() => setDeleteOpen(false)}
        />
      ) : (
        <DeleteRuntimeDialog
          open={deleteOpen}
          onOpenChange={setDeleteOpen}
          runtime={runtime}
          wsId={wsId}
          onDeleted={() => {
            setDeleteOpen(false);
            toast.success(t(($) => $.detail.toast_deleted));
          }}
        />
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// 列表主体
// ---------------------------------------------------------------------------

/**
 * 机器详情页的 runtime 表格。以 runtime 快照为输入，汇总工作区数据后按行
 * 渲染出名称 / 健康 / 归属 / agent / 费用 / CLI / 操作等列。
 *
 * @param runtimes - 要展示的 runtime 列表（通常是一台机器名下的）。
 * @param now - 当前时间戳（健康判定用，父组件统一传入保持渲染一致）。
 * @param runtimeHref - 生成行详情链接；机器详情页把 runtime 设置嵌套在
 *   机器下时用此回调。缺省时回退到工作区默认详情路径。
 * @param machineTitle - 所在机器标题（机器别名去重逻辑，见 RuntimeNameCell）。
 */
export function RuntimeList({
  runtimes,
  now,
  runtimeHref,
  machineTitle,
}: {
  runtimes: AgentRuntime[];
  now: number;
  /** 详情页保持 runtime 设置嵌套在机器下的跳转回调。 */
  runtimeHref?: (runtimeId: string) => string;
  /**
   * 所在机器的标题，当本列表渲染的是单台机器下的 runtimes 时使用。用于：
   * 机器级别名不在每行重复出现，而 per-runtime 别名仍然展示（MUL-5248）。
   */
  machineTitle?: string;
}) {
  const { t } = useT("runtimes");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const rowLink = useRowLink();
  const user = useAuthStore((s) => s.user);

  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: snapshot = [] } = useQuery(agentTaskSnapshotOptions(wsId));
  const { data: profiles = [] } = useQuery(runtimeProfileListOptions(wsId));

  const currentMember = user
    ? members.find((m) => m.user_id === user.id)
    : null;
  const isAdmin = currentMember
    ? currentMember.role === "owner" || currentMember.role === "admin"
    : false;

  const workloadIndex = useMemo(
    () => buildWorkloadIndex(agents, snapshot),
    [agents, snapshot],
  );

  const memberById = useMemo(() => {
    const map = new Map<string, MemberWithUser>();
    for (const m of members) map.set(m.user_id, m);
    return map;
  }, [members]);

  const profileById = useMemo(() => {
    const map = new Map<string, RuntimeProfile>();
    for (const p of profiles) map.set(p.id, p);
    return map;
  }, [profiles]);

  // 只有页面里真的存在多个不同归属者时，归属列才配得上占一列——否则它只是
  // 一列相同的头像。
  const showOwner = useMemo(() => {
    const owners = new Set<string>();
    for (const r of runtimes) {
      if (r.owner_id) owners.add(r.owner_id);
    }
    return owners.size > 1;
  }, [runtimes]);

  const rows = useMemo<RuntimeRow[]>(() => {
    return runtimes.map((runtime) => {
      const profile = runtime.profile_id
        ? profileById.get(runtime.profile_id) ?? null
        : null;
      const isCustomRuntime = !!runtime.profile_id;
      return {
        runtime,
        profile,
        ownerMember: runtime.owner_id
          ? memberById.get(runtime.owner_id) ?? null
          : null,
        workload: workloadIndex.get(runtime.id) ?? EMPTY_WORKLOAD,
        canDelete: isCustomRuntime
          ? isAdmin && !!profile
          : !isPendingCustomRuntime(runtime) &&
            (isAdmin || (!!user && runtime.owner_id === user.id)),
      };
    });
  }, [runtimes, profileById, memberById, workloadIndex, isAdmin, user]);

  // 与 RuntimeRowMenu 的渲染守卫保持一致：至少一行真的会显示菜单时，kebab
  // 轨道才配占它的宽度。
  const showActions = rows.some((row) => row.canDelete);

  return (
    <div className="overflow-x-auto overflow-y-hidden @container">
      <ListGrid
        className={`${GRID_COLS} @2xl:min-w-[var(--rtc-minw)]`}
        style={columnTrackVars(showOwner, showActions)}
      >
        <ListGridHeader>
          <ListGridHeaderCell>
            {t(($) => $.list.col_runtime)}
          </ListGridHeaderCell>
          <ListGridHeaderCell>{t(($) => $.list.col_health)}</ListGridHeaderCell>
          {showOwner ? (
            <ListGridHeaderCell className="hidden @2xl:flex">
              {t(($) => $.list.col_owner)}
            </ListGridHeaderCell>
          ) : (
            <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
          )}
          <ListGridHeaderCell className="hidden @2xl:flex">
            {t(($) => $.list.col_agents)}
          </ListGridHeaderCell>
          <ListGridHeaderCell className="hidden @2xl:flex" align="right">
            {t(($) => $.list.col_cost)}
          </ListGridHeaderCell>
          <ListGridHeaderCell className="hidden @2xl:flex">
            {t(($) => $.list.col_cli)}
          </ListGridHeaderCell>
          <span aria-hidden="true" />
        </ListGridHeader>
        {rows.map((row) => {
          const pending = isPendingCustomRuntime(row.runtime);
          const detailHref = pending
            ? undefined
            : runtimeHref?.(row.runtime.id) ??
              wsPaths.runtimeDetail(row.runtime.id);
          return (
            <ListGridRow
              key={row.runtime.id}
              className={pending ? "cursor-default" : "cursor-pointer"}
              {...(detailHref ? rowLink(detailHref) : {})}
            >
              <RuntimeNameCell runtime={row.runtime} machineTitle={machineTitle} />
              <HealthCell
                runtime={row.runtime}
                workload={row.workload}
                now={now}
              />
              {showOwner ? (
                <ListGridCell className="hidden gap-1.5 @2xl:flex">
                  {row.ownerMember ? (
                    <>
                      <ActorAvatar
                        actorType="member"
                        actorId={row.ownerMember.user_id}
                        size="sm"
                      />
                      <span className="min-w-0 truncate text-caption text-muted-foreground">
                        {row.ownerMember.name}
                      </span>
                    </>
                  ) : (
                    <span className="text-caption text-faint-foreground">—</span>
                  )}
                </ListGridCell>
              ) : (
                <ListGridCell className="hidden px-0 @2xl:flex" />
              )}
              <ListGridCell className="hidden @2xl:flex">
                <AgentStack agentIds={row.workload.agentIds} />
              </ListGridCell>
              <ListGridCell className="hidden @2xl:flex">
                {pending ? (
                  <div className="w-full text-right">
                    <span className="text-caption text-faint-foreground">—</span>
                  </div>
                ) : (
                  <CostCell
                    runtimeId={row.runtime.id}
                    enabled={canReadRuntimeUsage(row.runtime, user?.id ?? null)}
                  />
                )}
              </ListGridCell>
              <ListGridCell className="hidden @2xl:flex">
                <CliCell runtime={row.runtime} />
              </ListGridCell>
              <ListGridCell className="justify-end px-0">
                <span
                  onClick={(e) => e.stopPropagation()}
                  className="flex items-center"
                >
                  <RuntimeRowMenu
                    runtime={row.runtime}
                    profile={row.profile}
                    wsId={wsId}
                    canDelete={row.canDelete}
                    detailHref={detailHref}
                  />
                </span>
              </ListGridCell>
            </ListGridRow>
          );
        })}
      </ListGrid>
    </div>
  );
}
