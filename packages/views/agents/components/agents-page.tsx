"use client";

// 本文件是 Agent 列表主页：顶部页头 + 工具栏（搜索/范围/筛选/排序/列显隐）+ 虚拟滚动
// 列表 + 批量操作浮动栏。行数据统一在此组装（运行环境/在场/活动/计数/拥有者都打好包），
// 单元格只读不自己拉数据；可隐藏列、排序、筛选状态都放 core 的 view store。

import { useCallback, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  Bot,
  Lock,
  Plus,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import type {
  Agent,
  AgentRuntime,
  MemberWithUser,
} from "@multica/core/types";
import {
  type AgentActivity,
  agentRunCounts30dOptions,
  effectiveAccessScope,
  isAgentRuntimeBound,
  useWorkspaceActivityMap,
  useWorkspacePresenceMap,
  VISIBILITY_TOOLTIP,
  type AgentPresenceDetail,
} from "@multica/core/agents";
import {
  type AgentListFilters,
  useAgentsViewStore,
  AGENT_DEFAULT_HIDDEN_COLUMNS,
  AGENT_SCOPES,
  type AgentColumnKey,
  type AgentsScope,
  type AgentSortField,
} from "@multica/core/agents/stores";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  agentListOptions,
  memberListOptions,
} from "@multica/core/workspace/queries";
import { runtimeDisplayLabel, runtimeListOptions } from "@multica/core/runtimes";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  LIST_GRID_BOTTOM_CLEARANCE,
  ListGrid,
  ListGridBody,
  ListGridCell,
  ListGridHeader,
  ListGridHeaderCell,
  ListGridRow,
  type ListGridSortDirection,
} from "@multica/ui/components/ui/list-grid";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { useNavigation, useRowLink } from "../../navigation";
import { ActorAvatar } from "../../common/actor-avatar";
import { ProviderLogo } from "../../runtimes/components/provider-logo";
import {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "../../layout/collection-page";
import { availabilityConfig } from "../presence";
import { AgentRowActions } from "./agent-row-actions";
import {
  AgentListToolbar,
  countActiveFilterDimensions,
} from "./agent-list-toolbar";
import { useLocale, useT } from "../../i18n";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";

// 列模板——表头、行、骨架屏的唯一事实源。与 skills/autopilots 列表同约定
//（见 list-grid.tsx）：确定宽度的可变轨道、两档响应（≥@2xl 所见即所得，
// 带 min-width + 横向滚动逃生阀；<@2xl 只显示静态核心集 = 名字 + 状态，
// 列显隐开关不生效）。
//
// Agent 属于“身份型”实体（数量少、头像 + 人格），所以行用两行式：
// 左侧头像、右侧名字 + 描述，高 64px —— 这是对“单行管理列表”规则的明文例外。
const GRID_COLS =
  "grid-cols-[0.75rem_minmax(120px,1fr)_var(--agc-status-mobile)_1.75rem_0.75rem] " +
  "@2xl:grid-cols-[0.75rem_1rem_minmax(200px,1fr)_var(--agc-status-desktop)_var(--agc-owner)_var(--agc-access)_var(--agc-runtime)_var(--agc-lastactive)_var(--agc-runs)_var(--agc-model)_var(--agc-created)_1.75rem_0.75rem]";

// 两行式行高；虚拟列表“定高”契约就靠它。
const ROW_HEIGHT = 64;

// 可隐藏列宽的唯一来源：轨道变量和 grid 的 min-width 都由同一组数推导。
const COLUMN_WIDTHS: Record<AgentColumnKey, number> = {
  // 按最坏情况 “Online · 2 tasks”（含 padding 约 140px）定的宽度；
  // 空闲行只显示圆点 + 标签，轨道里会留一点余量。
  status: 144,
  owner: 144,
  // 能放下最长的标签 “Specific people”（含 padding 约 120px）。
  access: 132,
  runtime: 144,
  lastActive: 120,
  runs: 88,
  model: 120,
  created: 104,
};

// 固定轨道宽（左右 12+12、勾选框 16、名字最小 200、kebab 按钮 28），
// 加上宽模板 12 条轨道之间 11 个 gap-x-3 的间隔（零宽轨道也照样带 gap）。
const FIXED_TRACKS_WIDTH = 268 + 11 * 12;

/** 计算每列宽度的 CSS 变量（可见列给实宽，隐藏列给 0px），并汇总出 grid 的最小宽度。 */
function columnTrackVars(
  isVisible: (key: AgentColumnKey) => boolean,
): React.CSSProperties {
  const width = (key: AgentColumnKey) =>
    isVisible(key) ? `${COLUMN_WIDTHS[key]}px` : "0px";
  const minWidth =
    FIXED_TRACKS_WIDTH +
    (Object.keys(COLUMN_WIDTHS) as AgentColumnKey[]).reduce(
      (sum, key) => sum + (isVisible(key) ? COLUMN_WIDTHS[key] : 0),
      0,
    );
  return {
    "--agc-status-mobile": isVisible("status") ? "96px" : "0px",
    "--agc-status-desktop": width("status"),
    "--agc-owner": width("owner"),
    "--agc-access": width("access"),
    "--agc-runtime": width("runtime"),
    "--agc-lastactive": width("lastActive"),
    "--agc-runs": width("runs"),
    "--agc-model": width("model"),
    "--agc-created": width("created"),
    "--agc-minw": `${minWidth}px`,
  } as React.CSSProperties;
}

export interface AgentListRow {
  agent: Agent;
  runtime: AgentRuntime | null;
  presence: AgentPresenceDetail | null;
  activity: AgentActivity | null;
  runCount: number;
  /** 距最近一次有任务执行的时间桶的天数；null = 30 天窗口内无任务。 */
  lastActiveDays: number | null;
  owner: MemberWithUser | null;
  isOwnedByMe: boolean;
  canManage: boolean;
}

// 最近一个“有任务执行”的活动桶距今多少天（0 = 今天）。
// 刻意按天粒度——直接复用详情页图表同源的 30 天桶数据，不再多发请求。
function lastActiveDaysAgo(activity: AgentActivity | null): number | null {
  if (!activity) return null;
  for (let i = activity.buckets.length - 1; i >= 0; i--) {
    const bucket = activity.buckets[i];
    if (bucket && bucket.total > 0) return activity.buckets.length - 1 - i;
  }
  return null;
}

/** 行搜索匹配：名字 / 描述按子串 + 拼音匹配。空查询直接放行。 */
function matchesAgentSearch(row: AgentListRow, query: string): boolean {
  if (!query) return true;
  const { agent } = row;
  return (
    agent.name.toLowerCase().includes(query) ||
    matchesPinyin(agent.name, query) ||
    (agent.description?.toLowerCase().includes(query) ?? false) ||
    (agent.description ? matchesPinyin(agent.description, query) : false)
  );
}

/**
 * 纯行过滤谓词：行命中所有激活的筛选维度才返回 true。空的筛选数组视为“未激活”
 *（行通过）。`access` 维度用 `effectiveAccessScope` 推导 key，保证列与筛选取同一
 * 口径。导出供测试使用——页面在 useMemo 里调用它。
 */
export function rowMatchesFilters(
  row: AgentListRow,
  filters: AgentListFilters,
  query: string,
): boolean {
  if (!matchesAgentSearch(row, query.trim().toLowerCase())) return false;
  if (
    filters.availability.length > 0 &&
    (!row.presence || !filters.availability.includes(row.presence.availability))
  ) {
    return false;
  }
  if (
    filters.runtimes.length > 0 &&
    !filters.runtimes.includes(row.agent.runtime_id)
  ) {
    return false;
  }
  if (
    filters.owners.length > 0 &&
    (!row.agent.owner_id || !filters.owners.includes(row.agent.owner_id))
  ) {
    return false;
  }
  if (
    filters.models.length > 0 &&
    !filters.models.includes(row.agent.model)
  ) {
    return false;
  }
  if (
    filters.access.length > 0 &&
    !filters.access.includes(
      effectiveAccessScope(
        row.agent.permission_mode,
        row.agent.invocation_targets,
      ),
    )
  ) {
    return false;
  }
  return true;
}

/**
 * 批量访问弹窗“确认”按钮的可用性判定集中放在 `@multica/core/agents` 的
 * `isAccessChangeReady`（MUL-3963）。弹窗消费它；选择器内部的 Save 按钮也
 * 用同一谓词把关（批量流程里它自己的 Save 按钮通过 `hideFooter` 隐藏）。
 */
import { isAccessChangeReady } from "@multica/core/agents";
import { AgentBatchToolbar } from "./agent-batch-toolbar";
export { isAccessChangeReady };

// ---------------------------------------------------------------------------
// Page header
// ---------------------------------------------------------------------------

/** 页面顶部集合头：标题 + 计数 + 简介 + “新建 Agent”按钮。 */
function PageHeaderBar({
  totalCount,
  onCreate,
}: {
  totalCount: number;
  onCreate: () => void;
}) {
  const { t } = useT("agents");
  return (
    <CollectionPageHeader
      icon={Bot}
      title={t(($) => $.page.title)}
      count={totalCount}
      description={t(($) => $.page.tagline)}
      learnMore={{
        href: "https://multica.ai/docs/agents",
        label: t(($) => $.page.learn_more),
      }}
      actions={
        <CollectionPageHeaderAction
          icon={Plus}
          label={t(($) => $.page.new_agent)}
          onClick={onCreate}
        />
      }
    />
  );
}

/** 列表加载失败的整页状态（红色告警 + 重试）。 */
function ListError({
  onCreate,
  listError,
  onRetry,
}: {
  onCreate: () => void;
  listError: unknown;
  onRetry: () => void;
}) {
  const { t } = useT("agents");
  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeaderBar totalCount={0} onCreate={onCreate} />
      <CollectionPageState
        role="alert"
        tone="destructive"
        icon={AlertCircle}
        title={t(($) => $.page.list_load_failed)}
        description={
          listError instanceof Error
            ? listError.message
            : t(($) => $.page.list_load_failed_default)
        }
        actions={
          <Button type="button" variant="outline" size="sm" onClick={onRetry}>
            {t(($) => $.page.try_again)}
          </Button>
        }
      />
    </div>
  );
}

/** 工作区还没有任何 Agent 时的空态。 */
function EmptyState({ onCreate }: { onCreate: () => void }) {
  const { t } = useT("agents");
  return (
    <CollectionPageState
      icon={Bot}
      title={t(($) => $.empty.title)}
      description={t(($) => $.empty.description)}
      actions={
        <Button type="button" onClick={onCreate} size="sm">
          <Plus aria-hidden="true" className="size-3" />
          {t(($) => $.page.new_agent)}
        </Button>
      }
    />
  );
}

// ---------------------------------------------------------------------------
// Cells
// ---------------------------------------------------------------------------

/** 行首的多选框单元格。用透明占位 + 悬停显现（选中后常显）。 */
function CheckboxCell({
  checked,
  onToggle,
}: {
  checked: boolean;
  onToggle: () => void;
}) {
  return (
    <ListGridCell className="hidden justify-center px-0 @2xl:flex">
      <button
        type="button"
        aria-pressed={checked}
        onClick={(e) => {
          e.stopPropagation();
          onToggle();
        }}
        className={`-m-1.5 flex items-center p-1.5 ${
          checked ? "" : "opacity-0 transition-opacity group-hover/row:opacity-100"
        }`}
      >
        <Checkbox
          checked={checked}
          tabIndex={-1}
          className="pointer-events-none"
        />
      </button>
    </ListGridCell>
  );
}

// 两行式身份单元格：左侧头像、右侧名字 + 描述。这是“单行规则”的明文例外——
// Agent 数量少且身份信息丰富，所以采用“团队花名册”形态（GitHub 组织成员、
// Slack 成员列表同款）。
function NameCell({ row }: { row: AgentListRow }) {
  const { t } = useT("agents");
  const { agent, isOwnedByMe } = row;
  const isArchived = !!agent.archived_at;
  const isPrivate = agent.visibility === "private";
  return (
    <ListGridCell className="gap-3">
      <ActorAvatar
        actorType="agent"
        actorId={agent.id}
        size="lg"
        className={`shrink-0 ${isArchived ? "opacity-50 grayscale" : ""}`}
        showStatusDot
      />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span
            className={`min-w-0 truncate text-body font-medium ${
              isArchived ? "text-muted-foreground" : ""
            }`}
          >
            {agent.name}
          </span>
          {isPrivate && !isArchived && (
            <Tooltip>
              <TooltipTrigger
                render={
                  <Lock className="h-3 w-3 shrink-0 text-faint-foreground" />
                }
              />
              <TooltipContent>{VISIBILITY_TOOLTIP.private}</TooltipContent>
            </Tooltip>
          )}
          {isOwnedByMe && (
            <span className="shrink-0 rounded bg-muted px-1 text-micro font-medium text-muted-foreground">
              {t(($) => $.row.you)}
            </span>
          )}
        </div>
        {agent.description ? (
          <div className="mt-0.5 truncate text-caption text-muted-foreground">
            {agent.description}
          </div>
        ) : null}
      </div>
    </ListGridCell>
  );
}

// 可用状态圆点 + 标签，把负载折成后缀一起显示（“在线 · 2 个任务”）——
// 0-2 的整数不值得单独开一列。
function StatusCell({ row }: { row: AgentListRow }) {
  const { t } = useT("agents");
  const { agent, presence } = row;
  if (agent.archived_at) {
    return (
      <ListGridCell>
        <span className="text-caption text-muted-foreground">
          {t(($) => $.row.archived)}
        </span>
      </ListGridCell>
    );
  }
  if (!isAgentRuntimeBound(agent)) {
    return (
      <ListGridCell className="gap-1.5">
        <AlertCircle className="size-3.5 shrink-0 text-amber-500" />
        <span className="truncate text-caption text-amber-600 dark:text-amber-400">
          {t(($) => $.row.needs_runtime)}
        </span>
      </ListGridCell>
    );
  }
  if (!presence) {
    return (
      <ListGridCell>
        <span className="text-caption text-faint-foreground">—</span>
      </ListGridCell>
    );
  }
  const visual = availabilityConfig[presence.availability];
  const active = presence.runningCount + presence.queuedCount;
  return (
    <ListGridCell className="gap-1.5">
      <span className={`size-1.5 shrink-0 rounded-full ${visual.dotClass}`} />
      <span className={`truncate text-caption ${visual.textClass}`}>
        {t(($) => $.availability[presence.availability])}
        {active > 0 && (
          <span className="text-muted-foreground">
            {" · "}
            {t(($) => $.row.task_count, { count: active })}
          </span>
        )}
      </span>
    </ListGridCell>
  );
}

// 拥有者 = Agent 的 owner_id（创建时定为创建者、从不转移，所以拥有者 ≡ 创建者）。
// 拥有者带管理权，所以这列叫 “Owner” 而不是 “Created by”。
function OwnerCell({ row }: { row: AgentListRow }) {
  const { agent, owner } = row;
  if (!agent.owner_id) {
    return (
      <ListGridCell className="hidden @2xl:flex">
        <span className="text-caption text-faint-foreground">—</span>
      </ListGridCell>
    );
  }
  return (
    <ListGridCell className="hidden gap-1.5 @2xl:flex">
      <ActorAvatar actorType="member" actorId={agent.owner_id} size="sm" />
      <span className="min-w-0 truncate text-caption text-muted-foreground">
        {owner?.name ?? agent.owner_id.slice(0, 8)}
      </span>
    </ListGridCell>
  );
}

// 从 permission_mode + invocation_targets 推导“有效访问范围”（而非有损的
// `visibility` 派生值）。用文字标签而不是纯图标，让屏幕阅读器能读出范围。
export function AccessCell({ row }: { row: AgentListRow }) {
  const { t } = useT("agents");
  const scope = useMemo(
    () =>
      effectiveAccessScope(
        row.agent.permission_mode,
        row.agent.invocation_targets,
      ),
    [row.agent.permission_mode, row.agent.invocation_targets],
  );
  const label = t(($) =>
    scope === "workspace"
      ? $.access.scope_labels.workspace
      : scope === "specific-people"
        ? $.access.scope_labels.specific_people
        : $.access.scope_labels.owner_only,
  );
  return (
    <ListGridCell className="hidden @2xl:flex">
      <span className="min-w-0 truncate text-caption text-muted-foreground">
        {label}
      </span>
    </ListGridCell>
  );
}

function RuntimeCell({ row }: { row: AgentListRow }) {
  const { t } = useT("agents");
  if (!isAgentRuntimeBound(row.agent)) {
    return (
      <ListGridCell className="hidden @2xl:flex">
        <span className="truncate text-caption text-amber-600 dark:text-amber-400">
          {t(($) => $.row.needs_runtime)}
        </span>
      </ListGridCell>
    );
  }
  const runtime = row.runtime;
  return (
    <ListGridCell className="hidden @2xl:flex">
      {runtime ? (
        // 图标放在标签前面：扫这一列找“哪些跑在 Codex 上”靠形状一眼识别，而不是读字。
        <span className="inline-flex min-w-0 items-center gap-1.5">
          <ProviderLogo
            provider={runtime.provider}
            className="h-3.5 w-3.5 shrink-0"
          />
          <span className="min-w-0 truncate text-caption text-muted-foreground">
            {runtimeDisplayLabel(runtime)}
          </span>
        </span>
      ) : (
        <span className="text-caption text-faint-foreground">—</span>
      )}
    </ListGridCell>
  );
}

/** “最近活跃”列：显示今天/N天前/从不；已归档显示横杠。 */
function LastActiveCell({ row }: { row: AgentListRow }) {
  const { t } = useT("agents");
  const days = row.lastActiveDays;
  return (
    <ListGridCell className="hidden @2xl:flex">
      {days === null ? (
        <span className="truncate text-caption text-muted-foreground">
          {row.agent.archived_at ? "—" : t(($) => $.last_active.none)}
        </span>
      ) : (
        <span className="whitespace-nowrap text-caption tabular-nums text-muted-foreground">
          {days === 0
            ? t(($) => $.last_active.today)
            : t(($) => $.last_active.days_ago, { count: days })}
        </span>
      )}
    </ListGridCell>
  );
}

// ---------------------------------------------------------------------------
// Header row + skeleton
// ---------------------------------------------------------------------------

/** 表头行：全选/半选勾选框 + 各列（可隐藏列给占位格子保持轨道对齐）。 */
function AgentListHeader({
  sortField,
  sortDirection,
  onSort,
  allSelected,
  someSelected,
  onToggleAll,
  isColVisible,
}: {
  sortField: AgentSortField;
  sortDirection: ListGridSortDirection;
  onSort: (field: AgentSortField) => void;
  allSelected: boolean;
  someSelected: boolean;
  onToggleAll: () => void;
  isColVisible: (key: AgentColumnKey) => boolean;
}) {
  const { t } = useT("agents");
  const sorted = (field: AgentSortField) =>
    sortField === field ? sortDirection : false;
  const anySelected = allSelected || someSelected;
  return (
    <ListGridHeader>
      <div className="hidden items-center justify-center @2xl:flex">
        <button
          type="button"
          aria-pressed={allSelected}
          onClick={onToggleAll}
          className={`-m-1.5 flex items-center p-1.5 ${
            anySelected
              ? ""
              : "opacity-0 transition-opacity group-hover/header:opacity-100"
          }`}
        >
          <Checkbox
            checked={allSelected}
            indeterminate={someSelected && !allSelected}
            tabIndex={-1}
            className="pointer-events-none"
          />
        </button>
      </div>
      <ListGridHeaderCell sorted={sorted("name")} onSort={() => onSort("name")}>
        {t(($) => $.columns.agent)}
      </ListGridHeaderCell>
      {isColVisible("status") ? (
        <ListGridHeaderCell>{t(($) => $.columns.status)}</ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="px-0" />
      )}
      {isColVisible("owner") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.columns.owner)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("access") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.columns.access)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("runtime") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.columns.runtime)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("lastActive") ? (
        <ListGridHeaderCell
          className="hidden @2xl:flex"
          sorted={sorted("lastActive")}
          onSort={() => onSort("lastActive")}
        >
          {t(($) => $.columns.last_active)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("runs") ? (
        <ListGridHeaderCell
          className="hidden @2xl:flex"
          align="right"
          sorted={sorted("runs")}
          onSort={() => onSort("runs")}
        >
          {t(($) => $.columns.runs)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("model") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.columns.model)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("created") ? (
        <ListGridHeaderCell
          className="hidden @2xl:flex"
          sorted={sorted("created")}
          onSort={() => onSort("created")}
        >
          {t(($) => $.columns.created)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      <span aria-hidden="true" />
    </ListGridHeader>
  );
}

/** 列表初次加载/等待辅助查询时的骨架屏（列结构与真网格一致）。 */
function LoadingSkeleton() {
  return (
    <ListGrid
      className={GRID_COLS}
      style={columnTrackVars(
        (key) => !AGENT_DEFAULT_HIDDEN_COLUMNS.includes(key),
      )}
    >
      <ListGridHeader>
        <span aria-hidden="true" className="hidden @2xl:inline" />
        <ListGridHeaderCell>
          <Skeleton className="h-3 w-12" />
        </ListGridHeaderCell>
        <ListGridHeaderCell>
          <Skeleton className="h-3 w-12" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-14" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-14" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-14" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-14" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-10" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
        <span aria-hidden="true" />
      </ListGridHeader>
      {Array.from({ length: 5 }).map((_, i) => (
        <ListGridRow key={i} className="h-16 hover:bg-transparent">
          <span aria-hidden="true" className="hidden @2xl:inline" />
          <ListGridCell className="gap-3">
            <Skeleton className="size-8 rounded-full" />
            <div className="min-w-0 flex-1 space-y-1.5">
              <Skeleton className="h-3.5 w-32 max-w-full" />
              <Skeleton className="h-3 w-48 max-w-full" />
            </div>
          </ListGridCell>
          <ListGridCell>
            <Skeleton className="h-3 w-16" />
          </ListGridCell>
          <ListGridCell className="hidden gap-1.5 @2xl:flex">
            <Skeleton className="size-5 rounded-full" />
            <Skeleton className="h-3 w-12" />
          </ListGridCell>
          <ListGridCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-14" />
          </ListGridCell>
          <ListGridCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-16" />
          </ListGridCell>
          <ListGridCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-12" />
          </ListGridCell>
          <ListGridCell className="hidden justify-end @2xl:flex">
            <Skeleton className="h-3 w-8" />
          </ListGridCell>
          <ListGridCell className="hidden px-0 @2xl:flex" />
          <ListGridCell className="hidden px-0 @2xl:flex" />
          <span aria-hidden="true" />
        </ListGridRow>
      ))}
    </ListGrid>
  );
}

// ---------------------------------------------------------------------------
// Batch toolbar — archive (with confirm; archiving cancels active tasks) and
// Page
// ---------------------------------------------------------------------------

/**
 * Agent 列表主页。职责：拉取并把行数据装配成 AgentListRow（运行环境/在场/活动/计数/
 * 拥有者都在这打包，单元格不再自己拉数据），按范围/搜索/筛选/排序出可见行并虚拟滚动
 * 渲染，还托管工具栏、行操作、批量操作栏与首屏就绪门槛。
 */
export function AgentsPage() {
  const { t } = useT("agents");
  const locale = useLocale();
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const rowLink = useRowLink();
  const currentUser = useAuthStore((s) => s.user);

  const {
    data: agents = [],
    isLoading,
    error: listError,
    refetch: refetchList,
  } = useQuery(agentListOptions(wsId));
  const { data: runtimes = [] } = useQuery(
    runtimeListOptions(wsId),
  );
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: runCountsRaw = [], isPending: runCountsPending } = useQuery(
    agentRunCounts30dOptions(wsId),
  );
  const { byAgent: presenceMap, loading: presenceLoading } =
    useWorkspacePresenceMap(wsId);
  const { byAgent: activityMap, loading: activityLoading } =
    useWorkspaceActivityMap(wsId);

  const [selectedIds, setSelectedIds] = useState<ReadonlySet<string>>(
    new Set(),
  );
  const [search, setSearch] = useState("");

  const rawScope = useAgentsViewStore((s) => s.scope);
  const scope = AGENT_SCOPES.includes(rawScope) ? rawScope : "mine";
  const setScope = useAgentsViewStore((s) => s.setScope);
  const sortField = useAgentsViewStore((s) => s.sortField);
  const sortDirection = useAgentsViewStore((s) => s.sortDirection);
  const hiddenColumns = useAgentsViewStore((s) => s.hiddenColumns);
  const filters = useAgentsViewStore((s) => s.filters);
  const handleSort = useAgentsViewStore((s) => s.toggleSort);
  const handleSortFieldSelect = useAgentsViewStore((s) => s.setSortField);
  const setSortDirection = useAgentsViewStore((s) => s.setSortDirection);
  const toggleColumn = useAgentsViewStore((s) => s.toggleColumn);
  const toggleFilter = useAgentsViewStore((s) => s.toggleFilter);
  const clearFilters = useAgentsViewStore((s) => s.clearFilters);

  const isColVisible = (key: AgentColumnKey) => !hiddenColumns.includes(key);

  const toggleSelected = (id: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const runtimesById = useMemo(() => {
    const m = new Map<string, AgentRuntime>();
    for (const r of runtimes) m.set(r.id, r);
    return m;
  }, [runtimes]);

  const runCountsById = useMemo(() => {
    const m = new Map<string, number>();
    for (const r of runCountsRaw) m.set(r.agent_id, r.run_count);
    return m;
  }, [runCountsRaw]);

  const membersById = useMemo(() => {
    const m = new Map<string, MemberWithUser>();
    for (const mem of members) m.set(mem.user_id, mem);
    return m;
  }, [members]);

  const isWorkspaceAdmin = useMemo(() => {
    if (!currentUser) return false;
    const me = members.find((m) => m.user_id === currentUser.id);
    return me?.role === "owner" || me?.role === "admin";
  }, [members, currentUser]);

  // 范围计数来自“全集”（筛选不影响它）。已归档忽略归属视角（见 view store 注释）。
  const scopeCounts = useMemo<Record<AgentsScope, number>>(() => {
    let mine = 0;
    let all = 0;
    let archived = 0;
    for (const a of agents) {
      if (a.archived_at) {
        archived++;
        continue;
      }
      all++;
      if (currentUser && a.owner_id === currentUser.id) mine++;
    }
    return { mine, all, archived };
  }, [agents, currentUser]);

  // 当前范围内的行（未过滤）、完整装配好——工具栏的选项列表和“n / total”分母
  // 都从它推导；各单元格从不自己发查询。
  const scopeRows = useMemo<AgentListRow[]>(() => {
    const inScope = agents.filter((a) => {
      if (scope === "archived") return !!a.archived_at;
      if (a.archived_at) return false;
      if (scope === "mine") {
        return !!currentUser && a.owner_id === currentUser.id;
      }
      return true;
    });
    return inScope.map((agent) => {
      const isOwner = !!currentUser?.id && agent.owner_id === currentUser.id;
      const activity = activityMap.get(agent.id) ?? null;
      return {
        agent,
        runtime: runtimesById.get(agent.runtime_id) ?? null,
        presence: presenceMap.get(agent.id) ?? null,
        activity,
        runCount: runCountsById.get(agent.id) ?? 0,
        lastActiveDays: lastActiveDaysAgo(activity),
        owner: agent.owner_id ? membersById.get(agent.owner_id) ?? null : null,
        isOwnedByMe: isOwner,
        canManage: isWorkspaceAdmin || isOwner,
      };
    });
  }, [
    agents,
    scope,
    currentUser,
    runtimesById,
    membersById,
    presenceMap,
    activityMap,
    runCountsById,
    isWorkspaceAdmin,
  ]);

  // 可见行：本地搜索 + 筛选，再排序。
  const rows = useMemo<AgentListRow[]>(() => {
    const filtered = scopeRows.filter((row) => rowMatchesFilters(row, filters, search));

    const dir = sortDirection === "asc" ? 1 : -1;
    filtered.sort((a, b) => {
      if (sortField === "name") {
        return a.agent.name.localeCompare(b.agent.name) * dir;
      }
      if (sortField === "runs") {
        return (a.runCount - b.runCount) * dir ||
          a.agent.name.localeCompare(b.agent.name);
      }
      if (sortField === "created") {
        return (
          (Date.parse(a.agent.created_at) - Date.parse(b.agent.created_at)) *
          dir
        );
      }
      // lastActive：daysAgo 越小越新。“desc”（默认）= 最近活跃的排最前；
      // 从没活跃过的两种方向都排最后。同天数用运行次数破平局。
      const av = a.lastActiveDays ?? Number.POSITIVE_INFINITY;
      const bv = b.lastActiveDays ?? Number.POSITIVE_INFINITY;
      const byDays = sortDirection === "desc" ? av - bv : bv - av;
      return (
        byDays || b.runCount - a.runCount ||
        a.agent.name.localeCompare(b.agent.name)
      );
    });
    return filtered;
  }, [scopeRows, search, filters, sortField, sortDirection]);

  const noMatchText = useMemo(() => {
    const query = search.trim();
    if (query) {
      if (scope === "archived") {
        return t(($) => $.no_matches.search_archived, { query });
      }
      if (countActiveFilterDimensions(filters) > 0) {
        return t(($) => $.no_matches.search_active_filtered, { query });
      }
      return t(($) => $.no_matches.search_active, { query });
    }
    if (scope === "archived") return t(($) => $.no_matches.no_archived);
    if (countActiveFilterDimensions(filters) > 0) {
      return t(($) => $.no_matches.no_filter_match);
    }
    return t(($) => $.no_matches.title);
  }, [filters, scope, search, t]);

  // 行虚拟化——无头数学计算，偏移量做成行容器的 padding，行高固定。滚动元素是
  // “单个”外层滚动器（双向滚）：若把横向滚动（外层容器）和纵向滚动（内层元素）
  // 拆开、再用 h-full 百分比桥接，会陷入不收敛的布局循环（双滚动条闪烁），
  // 还会在横滚动条下面裁掉最后一行。sticky 表头钉在这个滚动器内；纵向滚动条
  // 贯通整个面板高度（Linear 的结构）。
  const listScrollRef = useRef<HTMLDivElement | null>(null);
  const rowVirtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => listScrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 10,
  });

  // 直接进手动表单：复制出来的 Agent 每个字段都已定好，再走“选创建方式”那一步
  // 只会让用户面对一个无可选择的选择页。
  const duplicateHref = useCallback(
    (agent: Agent) =>
      `${paths.newAgentManual()}?duplicate=${encodeURIComponent(agent.id)}`,
    [paths],
  );

  const selectedRows = rows.filter((row) => selectedIds.has(row.agent.id));
  const allSelected = rows.length > 0 && selectedRows.length === rows.length;
  const someSelected = selectedRows.length > 0 && !allSelected;
  const handleToggleAll = () => {
    setSelectedIds(
      allSelected ? new Set() : new Set(rows.map((r) => r.agent.id)),
    );
  };

  const virtualItems = rowVirtualizer.getVirtualItems();
  const firstVirtual = virtualItems[0];
  const lastVirtual = virtualItems[virtualItems.length - 1];
  const virtualPadding = {
    top: firstVirtual ? firstVirtual.start : 0,
    bottom: lastVirtual
      ? rowVirtualizer.getTotalSize() - lastVirtual.end
      : 0,
  };

  if (listError) {
    return (
      <ListError
        onCreate={() => navigation.push(paths.newAgent())}
        listError={listError}
        onRetry={() => refetchList()}
      />
    );
  }

  const totalCount = agents.filter((a) => !a.archived_at).length;
  const showEmpty = !isLoading && agents.length === 0;

  // 当前激活的排序列 / 可用性筛选读到的列，来自主列表以外的独立查询（活动 →
  // lastActiveDays、运行计数 → runCount、在场 → availability）。若这些数据没到就渲染
  // 真实行，会按占位值排序（lastActiveDays null→∞、runCount 0），等每个查询返回后
  // 列表又会肉眼可见地重排。所以把“首次真实渲染”的门槛精确设成当前排序/筛选所依赖
  // 的那几个辅助查询——name/created 什么都不等；runs 等运行计数；默认 lastActive
  // 等活动 + 运行计数（它的破平局项）；激活了可用性筛选就再等在场的。这些查询本来
  // 并行跑，所以首屏最多多等一个往返（显示骨架屏），绝不会串行化。空工作区
  // （showEmpty）跳过门槛，让空态永不因辅助查询而卡住。
  const needsRunCounts = sortField === "lastActive" || sortField === "runs";
  const needsActivity = sortField === "lastActive";
  const needsPresence = filters.availability.length > 0;
  const listReady =
    (!needsActivity || !activityLoading) &&
    (!needsRunCounts || !runCountsPending) &&
    (!needsPresence || !presenceLoading);

  return (
    // relative：作为批量工具栏的定位锚点（相对页面居中，而非视口居中）。
    <div className="relative flex flex-1 min-h-0 flex-col">
      <PageHeaderBar
        totalCount={totalCount}
        onCreate={() => navigation.push(paths.newAgent())}
      />

      {isLoading || (!showEmpty && !listReady) ? (
        <div className="flex-1 overflow-y-auto @container">
          <LoadingSkeleton />
        </div>
      ) : showEmpty ? (
        <div className="flex flex-1 items-center justify-center">
          <EmptyState onCreate={() => navigation.push(paths.newAgent())} />
        </div>
      ) : (
        <>
          <AgentListToolbar
            scope={scope}
            onScopeChange={setScope}
            scopeCounts={scopeCounts}
            search={search}
            onSearchChange={setSearch}
            filters={filters}
            onToggleFilter={toggleFilter}
            onClearFilters={clearFilters}
            sortField={sortField}
            sortDirection={sortDirection}
            onSortFieldChange={handleSortFieldSelect}
            onSortDirectionChange={setSortDirection}
            hiddenColumns={hiddenColumns}
            onToggleColumn={toggleColumn}
            allRows={scopeRows}
            members={members}
            visibleCount={rows.length}
          />
          <div
            ref={listScrollRef}
            className="min-h-0 flex-1 overflow-auto @container"
          >
            <ListGrid
              className={`${GRID_COLS} @2xl:min-w-[var(--agc-minw)]`}
              style={columnTrackVars(isColVisible)}
            >
              <AgentListHeader
                sortField={sortField}
                sortDirection={sortDirection}
                onSort={handleSort}
                allSelected={allSelected}
                someSelected={someSelected}
                onToggleAll={handleToggleAll}
                isColVisible={isColVisible}
              />
              <ListGridBody
                style={{
                  paddingTop: virtualPadding.top,
                  paddingBottom:
                    virtualPadding.bottom + LIST_GRID_BOTTOM_CLEARANCE,
                }}
              >
                {rows.length === 0 && (
                  <div className="col-span-full py-16 text-center text-body text-muted-foreground">
                    {noMatchText}
                  </div>
                )}
                {virtualItems.map((vi) => {
                  const row = rows[vi.index];
                  if (!row) return null;
                  return (
                    <ListGridRow
                      key={row.agent.id}
                      className={`h-16 cursor-pointer ${
                        selectedIds.has(row.agent.id) ? "bg-accent/30" : ""
                      }`}
                      {...rowLink(paths.agentDetail(row.agent.id))}
                    >
                      <CheckboxCell
                        checked={selectedIds.has(row.agent.id)}
                        onToggle={() => toggleSelected(row.agent.id)}
                      />
                      <NameCell row={row} />
                      {isColVisible("status") ? (
                        <StatusCell row={row} />
                      ) : (
                        <ListGridCell className="px-0" />
                      )}
                      {isColVisible("owner") ? (
                        <OwnerCell row={row} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("access") ? (
                        <AccessCell row={row} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("runtime") ? (
                        <RuntimeCell row={row} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("lastActive") ? (
                        <LastActiveCell row={row} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("runs") ? (
                        <ListGridCell className="hidden justify-end font-mono text-caption tabular-nums text-muted-foreground @2xl:flex">
                          {row.runCount.toLocaleString(locale)}
                        </ListGridCell>
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("model") ? (
                        <ListGridCell className="hidden @2xl:flex">
                          <span className="min-w-0 truncate text-caption text-muted-foreground">
                            {row.agent.model || "—"}
                          </span>
                        </ListGridCell>
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("created") ? (
                        <ListGridCell className="hidden whitespace-nowrap text-caption tabular-nums text-muted-foreground @2xl:flex">
                          {new Date(
                            row.agent.created_at,
                          ).toLocaleDateString(locale)}
                        </ListGridCell>
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      <ListGridCell className="justify-end px-0">
                        <span
                          onClick={(e) => e.stopPropagation()}
                          className="flex items-center"
                        >
                          <AgentRowActions
                            agent={row.agent}
                            presence={row.presence}
                            canManage={row.canManage}
                            duplicateHref={duplicateHref(row.agent)}
                          />
                        </span>
                      </ListGridCell>
                    </ListGridRow>
                  );
                })}
              </ListGridBody>
            </ListGrid>
          </div>
        </>
      )}

      <AgentBatchToolbar
        key={wsId}
        runtimes={runtimes}
        rows={selectedRows}
        members={members}
        currentUserId={currentUser?.id ?? null}
        onClear={() => setSelectedIds(new Set())}
      />

    </div>
  );
}
