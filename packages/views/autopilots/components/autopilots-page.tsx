// autopilot 列表页（"/autopilots" 路由）：拉取当前工作区的全部 autopilot，先按
// 范围（全部/运行中/已暂停）裁出 scopeRows，再叠加执行者/触发方式/模式/创建者过滤
// 与排序得到可见行，交给虚拟化表格渲染。表格支持勾选行做批量暂停/恢复/删除，整行
// 可点击进详情页；空态页面提供六个内置模板一键创建。视图状态（范围/排序/列显隐/
// 过滤）全部存于 useAutopilotsViewStore（Zustand），分页与创建弹窗只在本文件内使用。
// 创建/编辑弹窗经 lazy 懒加载，减小列表页首屏包体。

"use client";

import { lazy, Suspense, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  BarChart3,
  Bug,
  Clock,
  Code,
  FileSearch,
  GitPullRequest,
  Newspaper,
  Pause,
  Plus,
  Shield,
  Webhook,
  Zap,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { autopilotListOptions } from "@multica/core/autopilots/queries";
import {
  useAutopilotsViewStore,
  AUTOPILOT_DEFAULT_HIDDEN_COLUMNS,
  AUTOPILOT_SCOPES,
  type AutopilotColumnKey,
  type AutopilotScope,
  type AutopilotSortField,
} from "@multica/core/autopilots/stores";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useActorName } from "@multica/core/workspace/hooks";
import type { Autopilot } from "@multica/core/types";
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
import { useRowLink } from "../../navigation";
import { ActorAvatar } from "../../common/actor-avatar";
import { formatInTimeZone } from "../../common/format-in-time-zone";
import {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "../../layout/collection-page";
import { AutopilotListToolbar, actorFilterValue } from "./autopilot-list-toolbar";
import {
  AutopilotBatchToolbar,
  AutopilotRowActions,
} from "./autopilot-list-actions";
import type { ScheduleConfig } from "./schedule-editor/model";
import { useLocale, useT, useTimeAgo } from "../../i18n";

// 列模板——表头、行与骨架的唯一真源。沿用技能列表的约定（见 list-grid.tsx 与其中
// 注释）：确定性可变宽轨道（行是虚拟化的），外加两区响应式：
// - 容器 ≥ @2xl（672px）：所见即所得——每个用户启用的列都渲染；网格携带
//   min-width = Σ(启用的轨道 + 间隙)，启用集合超过容器宽度时外层横向滚动。
//   已启用的列绝不能悄然消失（"死开关" bug）。
// - 容器 < @2xl：静态核心集（名称 + 执行者），无横向滚动，列开关不生效。
const GRID_COLS =
  "grid-cols-[0.75rem_1rem_minmax(120px,1fr)_var(--apc-assignee)_1.75rem_0.75rem] " +
  "@2xl:grid-cols-[0.75rem_1rem_minmax(200px,1fr)_var(--apc-assignee)_var(--apc-trigger)_var(--apc-lastrun)_var(--apc-nextrun)_var(--apc-mode)_var(--apc-creator)_var(--apc-created)_1.75rem_0.75rem]";

// h-12 行高；虚拟器定长契约。
const ROW_HEIGHT = 48;

// 创建/编辑弹窗按需懒加载，减小首屏包体。
const AutopilotDialog = lazy(() =>
  import("./autopilot-dialog").then((module) => ({ default: module.AutopilotDialog })),
);

// 可隐藏列宽度的唯一真源：轨道 CSS 变量与网格 min-width 由同一组数字推出。
const COLUMN_WIDTHS: Record<AutopilotColumnKey, number> = {
  assignee: 144,
  trigger: 144,
  lastRun: 120,
  nextRun: 104,
  mode: 104,
  creator: 144,
  created: 104,
};

// 固定轨道（两边边缘 12+12、复选框 16、名称 min 200、kebab 28）加上宽模板 12 条
// 轨道之间的 11 个 gap-x-3 间隙（零宽轨道仍带间隙）。
const FIXED_TRACKS_WIDTH = 268 + 11 * 12;

// 依据列可见性生成 CSS 变量：隐藏列轨道宽度 0px，min-width = 固定轨 + 可见列宽和。
function columnTrackVars(
  isVisible: (key: AutopilotColumnKey) => boolean,
): React.CSSProperties {
  const width = (key: AutopilotColumnKey) =>
    isVisible(key) ? `${COLUMN_WIDTHS[key]}px` : "0px";
  const minWidth =
    FIXED_TRACKS_WIDTH +
    (Object.keys(COLUMN_WIDTHS) as AutopilotColumnKey[]).reduce(
      (sum, key) => sum + (isVisible(key) ? COLUMN_WIDTHS[key] : 0),
      0,
    );
  return {
    "--apc-assignee": width("assignee"),
    "--apc-trigger": width("trigger"),
    "--apc-lastrun": width("lastRun"),
    "--apc-nextrun": width("nextRun"),
    "--apc-mode": width("mode"),
    "--apc-creator": width("creator"),
    "--apc-created": width("created"),
    "--apc-minw": `${minWidth}px`,
  } as React.CSSProperties;
}

// ---------------------------------------------------------------------------
// 空状态模板（与旧页面版本一致）。prompt 保持原始英文：它们直接注入智能体的
// 任务输入。
// ---------------------------------------------------------------------------

type TemplateId =
  | "daily_news"
  | "pr_review"
  | "bug_triage"
  | "weekly_progress"
  | "dependency_audit"
  | "documentation_check";

// 一个"一键创建"模板：ID、注入任务的提示词正文、图标、预填的调度。
interface AutopilotTemplate {
  id: TemplateId;
  prompt: string;
  icon: typeof Zap;
  schedule: Pick<ScheduleConfig, "time" | "days">;
}

const WEEKDAYS: ScheduleConfig["days"] = { kind: "weekly", daysOfWeek: [1, 2, 3, 4, 5] };
const MONDAY: ScheduleConfig["days"] = { kind: "weekly", daysOfWeek: [1] };

// 六个内置模板清单。prompt 会整段注入智能体任务，因此刻意不翻译。
const TEMPLATES: AutopilotTemplate[] = [
  {
    id: "daily_news",
    prompt: `1. Search the web for news and announcements published today only (strictly today's date)
2. Filter for topics relevant to our team and industry
3. For each item, write a short summary including: title, source, key takeaways
4. Compile everything into a single digest post
5. Post the digest as a comment on this issue and @mention all workspace members`,
    icon: Newspaper,
    schedule: { time: { kind: "at", time: "09:00" }, days: { kind: "every" } },
  },
  {
    id: "pr_review",
    prompt: `1. List all open pull requests in the repository
2. Identify PRs that have been open for more than 24 hours without a review
3. For each stale PR, note the author, age, and a one-line summary of the change
4. Post a comment on this issue listing all stale PRs with links
5. @mention the team to remind them to review`,
    icon: GitPullRequest,
    schedule: { time: { kind: "at", time: "10:00" }, days: WEEKDAYS },
  },
  {
    id: "bug_triage",
    prompt: `1. List all backlog issues that have not been prioritized
2. For each issue, read the description and any attached logs or screenshots
3. Assess severity (critical / high / medium / low) based on user impact and scope
4. Set the priority field on the issue accordingly
5. Add a comment explaining your assessment and suggested next steps`,
    icon: Bug,
    schedule: { time: { kind: "at", time: "09:00" }, days: WEEKDAYS },
  },
  {
    id: "weekly_progress",
    prompt: `1. Gather all issues completed (status "done") in the past 7 days
2. Gather all issues currently in progress
3. Identify any blocked issues and their blockers
4. Calculate key metrics: issues closed, issues opened, net change
5. Write a structured weekly report with sections: Completed, In Progress, Blocked, Metrics
6. Post the report as a comment on this issue`,
    icon: BarChart3,
    schedule: { time: { kind: "at", time: "17:00" }, days: MONDAY },
  },
  {
    id: "dependency_audit",
    prompt: `1. Run dependency audit tools on the project (npm audit, go vuln check, etc.)
2. Identify any packages with known security vulnerabilities
3. List outdated packages that are more than 2 major versions behind
4. For each finding, note the severity, affected package, and recommended fix
5. Post a summary report as a comment with actionable items`,
    icon: Shield,
    schedule: { time: { kind: "at", time: "08:00" }, days: MONDAY },
  },
  {
    id: "documentation_check",
    prompt: `1. List all code changes merged in the past 7 days (via git log)
2. For each significant change, check if related documentation was updated
3. Identify any new APIs, config options, or features missing documentation
4. Create a list of documentation gaps with file paths and suggested content
5. Post the findings as a comment on this issue`,
    icon: FileSearch,
    schedule: { time: { kind: "at", time: "14:00" }, days: MONDAY },
  },
];

// ---------------------------------------------------------------------------
// 单元格
// ---------------------------------------------------------------------------

// 行首复选框列：整行点击会导航，所以这里阻止冒泡后单独切换。
function CheckboxCell({
  checked,
  onToggle,
}: {
  checked: boolean;
  onToggle: () => void;
}) {
  return (
    <ListGridCell className="justify-center px-0">
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

// 名称单元格：标题 + 暂停标记。
function NameCell({ autopilot }: { autopilot: Autopilot }) {
  const { t } = useT("autopilots");
  return (
    <ListGridCell className="gap-1.5">
      <span className="min-w-0 truncate text-body font-medium">
        {autopilot.title}
      </span>
      {/* 暂停标记：在"全部"范围里运行中与暂停的行混排，暂停的自动化需要一个行内
          信号。 */}
      {autopilot.status === "paused" && (
        <span
          title={
            autopilot.pause_reason === "agent_runtime_required"
              ? t(($) => $.status.paused_runtime_required)
              : t(($) => $.status.paused)
          }
          className="flex shrink-0 items-center text-amber-500"
        >
          <Pause className="size-3" />
        </span>
      )}
    </ListGridCell>
  );
}

// 执行者单元格：头像 + 名称。
function AssigneeCell({ autopilot }: { autopilot: Autopilot }) {
  const { getActorName } = useActorName();
  return (
    <ListGridCell className="gap-1.5">
      <ActorAvatar
        actorType={autopilot.assignee_type}
        actorId={autopilot.assignee_id}
        size="sm"
        enableHoverCard={autopilot.assignee_type === "agent"}
        showStatusDot={autopilot.assignee_type === "agent"}
      />
      <span className="min-w-0 truncate text-caption text-muted-foreground">
        {getActorName(autopilot.assignee_type, autopilot.assignee_id)}
      </span>
    </ListGridCell>
  );
}

// 触发方式 → 图标映射。
const TRIGGER_ICONS: Record<string, typeof Zap> = {
  schedule: Clock,
  webhook: Webhook,
  api: Code,
};

// 触发方式单元格：每行列出该 autopilot 的全部触发方式（带图标）。
function TriggerCell({ autopilot }: { autopilot: Autopilot }) {
  const { t } = useT("autopilots");
  const kinds = autopilot.trigger_kinds ?? [];
  if (kinds.length === 0) {
    return (
      <ListGridCell className="hidden @2xl:flex">
        <span className="text-caption text-faint-foreground">—</span>
      </ListGridCell>
    );
  }
  return (
    <ListGridCell className="hidden gap-2 @2xl:flex">
      {kinds.map((kind) => {
        // 服务器驱动的枚举：未知种类用通用图标 + 原始标签。
        const Icon = TRIGGER_ICONS[kind] ?? Zap;
        const label =
          kind === "schedule" || kind === "webhook" || kind === "api"
            ? t(($) => $.trigger_kind[kind])
            : kind;
        return (
          <span
            key={kind}
            className="flex min-w-0 items-center gap-1 text-caption text-muted-foreground"
          >
            <Icon className="size-3 shrink-0" />
            <span className="truncate">{label}</span>
          </span>
        );
      })}
    </ListGridCell>
  );
}

// 最近一次运行结果的圆点颜色。服务器驱动的枚举——未知值落到中性圆点，绝不崩溃
//（API 兼容规则）。
function runStatusDotClass(status: string | null | undefined): string {
  switch (status) {
    case "completed":
    case "issue_created":
      return "bg-emerald-500";
    case "failed":
      return "bg-red-500";
    case "skipped":
      return "bg-amber-500";
    case "running":
      return "bg-blue-500";
    default:
      return "bg-muted-foreground/40";
  }
}

// 最近运行列：状态圆点 + 相对时间。
function LastRunCell({ autopilot }: { autopilot: Autopilot }) {
  const { t } = useT("autopilots");
  const timeAgo = useTimeAgo();
  if (!autopilot.last_run_at) {
    return (
      <ListGridCell className="hidden @2xl:flex">
        <span className="text-caption text-faint-foreground">—</span>
      </ListGridCell>
    );
  }
  const status = autopilot.last_run_status;
  const knownStatus =
    status === "issue_created" ||
    status === "running" ||
    status === "completed" ||
    status === "failed" ||
    status === "skipped"
      ? status
      : null;
  return (
    <ListGridCell className="hidden gap-1.5 @2xl:flex">
      <span
        title={knownStatus ? t(($) => $.run_status[knownStatus]) : status ?? undefined}
        className={`size-1.5 shrink-0 rounded-full ${runStatusDotClass(status)}`}
      />
      <span className="whitespace-nowrap text-caption tabular-nums text-muted-foreground">
        {timeAgo(autopilot.last_run_at)}
      </span>
    </ListGridCell>
  );
}

// 下次运行列：按读者时区格式化时刻；未调度则破折号。
function NextRunCell({ autopilot }: { autopilot: Autopilot }) {
  const { i18n } = useT("autopilots");
  const next = autopilot.next_run_at;
  return (
    <ListGridCell className="hidden @2xl:flex">
      {next ? (
        <span className="whitespace-nowrap text-caption tabular-nums text-muted-foreground">
          {formatInTimeZone(next, undefined, i18n.language)}
        </span>
      ) : (
        <span className="text-caption text-faint-foreground">—</span>
      )}
    </ListGridCell>
  );
}

// 运行模式列。
function ModeCell({ autopilot }: { autopilot: Autopilot }) {
  const { t } = useT("autopilots");
  const mode = autopilot.execution_mode;
  const label =
    mode === "create_issue" || mode === "run_only"
      ? t(($) => $.execution_mode[mode])
      : mode;
  return (
    <ListGridCell className="hidden @2xl:flex">
      <span className="truncate text-caption text-muted-foreground">{label}</span>
    </ListGridCell>
  );
}

// 创建者列：头像 + 名称。
function CreatorCell({ autopilot }: { autopilot: Autopilot }) {
  const { getActorName } = useActorName();
  return (
    <ListGridCell className="hidden gap-1.5 @2xl:flex">
      <ActorAvatar
        actorType={autopilot.created_by_type}
        actorId={autopilot.created_by_id}
        size="sm"
      />
      <span className="min-w-0 truncate text-caption text-muted-foreground">
        {getActorName(autopilot.created_by_type, autopilot.created_by_id)}
      </span>
    </ListGridCell>
  );
}

// ---------------------------------------------------------------------------
// 表头行
// ---------------------------------------------------------------------------

// 表头：全选复选框 + 各排序列表头。名称/最近运行/下次运行/创建支持排序。
function AutopilotListHeader({
  sortField,
  sortDirection,
  onSort,
  allSelected,
  someSelected,
  onToggleAll,
  isColVisible,
}: {
  sortField: AutopilotSortField;
  sortDirection: ListGridSortDirection;
  onSort: (field: AutopilotSortField) => void;
  allSelected: boolean;
  someSelected: boolean;
  onToggleAll: () => void;
  isColVisible: (key: AutopilotColumnKey) => boolean;
}) {
  const { t } = useT("autopilots");
  const sorted = (field: AutopilotSortField) =>
    sortField === field ? sortDirection : false;
  const anySelected = allSelected || someSelected;
  return (
    <ListGridHeader>
      <div className="flex items-center justify-center">
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
        {t(($) => $.page.table.name)}
      </ListGridHeaderCell>
      {isColVisible("assignee") ? (
        <ListGridHeaderCell>
          {t(($) => $.page.table.assignee)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="px-0" />
      )}
      {isColVisible("trigger") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.page.table.trigger)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("lastRun") ? (
        <ListGridHeaderCell
          className="hidden @2xl:flex"
          sorted={sorted("lastRun")}
          onSort={() => onSort("lastRun")}
        >
          {t(($) => $.page.table.last_run)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("nextRun") ? (
        <ListGridHeaderCell
          className="hidden @2xl:flex"
          sorted={sorted("nextRun")}
          onSort={() => onSort("nextRun")}
        >
          {t(($) => $.page.table.next_run)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("mode") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.page.table.mode)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("creator") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.page.table.created_by)}
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
          {t(($) => $.page.table.created)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      <span aria-hidden="true" />
    </ListGridHeader>
  );
}

// 加载骨架屏：与真实网格同轨道布局，列切换用默认隐藏集。
function LoadingSkeleton() {
  return (
    <ListGrid
      className={GRID_COLS}
      style={columnTrackVars(
        (key) => !AUTOPILOT_DEFAULT_HIDDEN_COLUMNS.includes(key),
      )}
    >
      <ListGridHeader>
        <span aria-hidden="true" />
        <ListGridHeaderCell>
          <Skeleton className="h-3 w-12" />
        </ListGridHeaderCell>
        <ListGridHeaderCell>
          <Skeleton className="h-3 w-14" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-12" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-12" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-12" />
        </ListGridHeaderCell>
        {/* mode/creator/created 默认隐藏——保持轨道映射为空的占位。 */}
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
        <span aria-hidden="true" />
      </ListGridHeader>
      {Array.from({ length: 5 }).map((_, i) => (
        <ListGridRow key={i} className="hover:bg-transparent">
          <span aria-hidden="true" />
          <ListGridCell>
            <Skeleton className="h-3.5 w-40 max-w-full" />
          </ListGridCell>
          <ListGridCell className="gap-1.5">
            <Skeleton className="size-5 rounded-full" />
            <Skeleton className="h-3 w-12" />
          </ListGridCell>
          <ListGridCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-16" />
          </ListGridCell>
          <ListGridCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-14" />
          </ListGridCell>
          <ListGridCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-14" />
          </ListGridCell>
          <ListGridCell className="hidden px-0 @2xl:flex" />
          <ListGridCell className="hidden px-0 @2xl:flex" />
          <ListGridCell className="hidden px-0 @2xl:flex" />
          <span aria-hidden="true" />
        </ListGridRow>
      ))}
    </ListGrid>
  );
}

// ---------------------------------------------------------------------------
// 页面
// ---------------------------------------------------------------------------

/**
 * autopilot 列表页。加载 autopilot 全集，按范围（全部/运行中/已暂停）、过滤
 * （执行者/触发方式/模式/创建者）与排序（名称/最近/下次运行/创建时间）出表；
 * 行支持勾选与批量暂停/恢复/删除，点行跳详情，右上新建打开创建弹窗。空态提供
 * 六个内置模板一键创建。
 */
export function AutopilotsPage() {
  const { t } = useT("autopilots");
  const locale = useLocale();
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const rowLink = useRowLink();
  const {
    data: autopilots = [],
    isLoading,
    error: listError,
    refetch: refetchList,
  } = useQuery(autopilotListOptions(wsId));

  const [createOpen, setCreateOpen] = useState(false);
  const [selectedTemplate, setSelectedTemplate] =
    useState<AutopilotTemplate | null>(null);
  const [selectedIds, setSelectedIds] = useState<ReadonlySet<string>>(
    new Set(),
  );
  // 持久化的范围可能持有已退役的值（如旧构建的 "archived"）——回退到 "all"，
  // 别把用户困在够不到的范围上。
  const rawScope = useAutopilotsViewStore((s) => s.scope);
  const scope = AUTOPILOT_SCOPES.includes(rawScope) ? rawScope : "all";
  const setScope = useAutopilotsViewStore((s) => s.setScope);
  const sortField = useAutopilotsViewStore((s) => s.sortField);
  const sortDirection = useAutopilotsViewStore((s) => s.sortDirection);
  const hiddenColumns = useAutopilotsViewStore((s) => s.hiddenColumns);
  const filters = useAutopilotsViewStore((s) => s.filters);
  const handleSort = useAutopilotsViewStore((s) => s.toggleSort);
  const handleSortFieldSelect = useAutopilotsViewStore((s) => s.setSortField);
  const setSortDirection = useAutopilotsViewStore((s) => s.setSortDirection);
  const toggleColumn = useAutopilotsViewStore((s) => s.toggleColumn);
  const toggleFilter = useAutopilotsViewStore((s) => s.toggleFilter);
  const clearFilters = useAutopilotsViewStore((s) => s.clearFilters);

  const isColVisible = (key: AutopilotColumnKey) =>
    !hiddenColumns.includes(key);

  const toggleSelected = (id: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  // 各范围计数来自全集（过滤从不影响它们——它们是阶段清单，不是结果计数）。
  // API 归档的行（没有任何 UI 流程会创建）在所有位置排除。
  const scopeCounts = useMemo<Record<AutopilotScope, number>>(() => {
    let active = 0;
    let paused = 0;
    for (const a of autopilots) {
      if (a.status === "archived") continue;
      if (a.status === "paused") paused++;
      else active++;
    }
    return { all: active + paused, active, paused };
  }, [autopilots]);

  // 当前范围内的行（未过滤）——工具栏选项列表与 "n / total" 分母由此推出。
  const scopeRows = useMemo<Autopilot[]>(() => {
    if (scope === "all") {
      return autopilots.filter((a) => a.status !== "archived");
    }
    return autopilots.filter((a) => a.status === scope);
  }, [autopilots, scope]);

  // 可见行：先过滤，再排序。
  const rows = useMemo<Autopilot[]>(() => {
    const filtered = scopeRows.filter((a) => {
      if (
        filters.assignees.length > 0 &&
        !filters.assignees.includes(
          actorFilterValue(a.assignee_type, a.assignee_id),
        )
      ) {
        return false;
      }
      if (filters.modes.length > 0 && !filters.modes.includes(a.execution_mode)) {
        return false;
      }
      if (
        filters.triggerKinds.length > 0 &&
        !(a.trigger_kinds ?? []).some((k) => filters.triggerKinds.includes(k))
      ) {
        return false;
      }
      if (
        filters.creators.length > 0 &&
        !filters.creators.includes(
          actorFilterValue(a.created_by_type, a.created_by_id),
        )
      ) {
        return false;
      }
      return true;
    });

    const dir = sortDirection === "asc" ? 1 : -1;
    filtered.sort((a, b) => {
      if (sortField === "name") {
        return a.title.localeCompare(b.title) * dir;
      }
      if (sortField === "nextRun") {
        // 没有下次运行的行无论方向都排最后。
        const av = a.next_run_at ? Date.parse(a.next_run_at) : null;
        const bv = b.next_run_at ? Date.parse(b.next_run_at) : null;
        if (av === null && bv === null) return a.title.localeCompare(b.title);
        if (av === null) return 1;
        if (bv === null) return -1;
        return (av - bv) * dir;
      }
      if (sortField === "created") {
        return (Date.parse(a.created_at) - Date.parse(b.created_at)) * dir;
      }
      // lastRun：从未跑过的行排最旧（0 起点）。
      const av = a.last_run_at ? Date.parse(a.last_run_at) : 0;
      const bv = b.last_run_at ? Date.parse(b.last_run_at) : 0;
      return (av - bv) * dir || a.title.localeCompare(b.title);
    });
    return filtered;
  }, [scopeRows, filters, sortField, sortDirection]);

  // 行虚拟化——与技能列表同款接线：headless 数学、偏移量作 body 内边距、定高行。
  const listScrollRef = useRef<HTMLDivElement | null>(null);
  const rowVirtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => listScrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 10,
  });

  const openCreate = (template?: AutopilotTemplate) => {
    setSelectedTemplate(template ?? null);
    setCreateOpen(true);
  };

  const selectedRows = rows.filter((a) => selectedIds.has(a.id));
  const allSelected = rows.length > 0 && selectedRows.length === rows.length;
  const someSelected = selectedRows.length > 0 && !allSelected;
  const handleToggleAll = () => {
    setSelectedIds(allSelected ? new Set() : new Set(rows.map((a) => a.id)));
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

  const totalCount = autopilots.length;
  const showEmpty = !isLoading && !listError && totalCount === 0;

  return (
    // relative：作为批量工具栏的定位锚点（页面居中，而非视口居中）。
    <div className="relative flex flex-1 min-h-0 flex-col">
      {/* 页头 */}
      <CollectionPageHeader
        icon={Zap}
        title={t(($) => $.page.title)}
        count={totalCount}
        actions={
          <CollectionPageHeaderAction
            icon={Plus}
            label={t(($) => $.page.new_autopilot)}
            onClick={() => openCreate()}
          />
        }
      />

      {listError ? (
        <CollectionPageState
          role="alert"
          tone="destructive"
          icon={AlertCircle}
          title={
            listError instanceof Error ? listError.message : String(listError)
          }
          actions={
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => refetchList()}
            >
              {t(($) => $.page.retry)}
            </Button>
          }
        />
      ) : isLoading ? (
        <div className="flex-1 overflow-y-auto @container">
          <LoadingSkeleton />
        </div>
      ) : showEmpty ? (
        <div className="flex flex-col items-center px-5 py-16">
          <Zap className="mb-3 h-10 w-10 text-faint-foreground" />
          <p className="text-body text-muted-foreground">
            {t(($) => $.page.empty.title)}
          </p>
          <p className="mb-6 mt-1 text-caption text-muted-foreground">
            {t(($) => $.page.empty.hint)}
          </p>
          <div className="grid w-full max-w-3xl grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {TEMPLATES.map((tpl) => {
              const Icon = tpl.icon;
              return (
                <button
                  key={tpl.id}
                  type="button"
                  className="flex items-start gap-3 rounded-lg border p-3 text-left transition-colors hover:bg-accent/40"
                  onClick={() => openCreate(tpl)}
                >
                  <Icon className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground" />
                  <div className="min-w-0">
                    <div className="text-body font-medium">
                      {t(($) => $.templates[tpl.id].title)}
                    </div>
                    <div className="mt-0.5 line-clamp-2 text-caption text-muted-foreground">
                      {t(($) => $.templates[tpl.id].summary)}
                    </div>
                  </div>
                </button>
              );
            })}
          </div>
          <Button
            size="sm"
            variant="outline"
            className="mt-4"
            onClick={() => openCreate()}
          >
            <Plus className="mr-1 h-3.5 w-3.5" />
            {t(($) => $.page.start_blank)}
          </Button>
        </div>
      ) : (
        <>
          <AutopilotListToolbar
            scope={scope}
            onScopeChange={setScope}
            scopeCounts={scopeCounts}
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
            visibleCount={rows.length}
          />
          <div
            ref={listScrollRef}
            className="min-h-0 flex-1 overflow-auto @container"
          >
            <ListGrid
              className={`${GRID_COLS} @2xl:min-w-[var(--apc-minw)]`}
              style={columnTrackVars(isColVisible)}
            >
              <AutopilotListHeader
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
                    {t(($) => $.page.no_matches)}
                  </div>
                )}
                {virtualItems.map((vi) => {
                  const autopilot = rows[vi.index];
                  if (!autopilot) return null;
                  return (
                    <ListGridRow
                      key={autopilot.id}
                      className={`cursor-pointer ${
                        selectedIds.has(autopilot.id) ? "bg-accent/30" : ""
                      }`}
                      {...rowLink(wsPaths.autopilotDetail(autopilot.id))}
                    >
                      <CheckboxCell
                        checked={selectedIds.has(autopilot.id)}
                        onToggle={() => toggleSelected(autopilot.id)}
                      />
                      <NameCell autopilot={autopilot} />
                      {isColVisible("assignee") ? (
                        <AssigneeCell autopilot={autopilot} />
                      ) : (
                        <ListGridCell className="px-0" />
                      )}
                      {isColVisible("trigger") ? (
                        <TriggerCell autopilot={autopilot} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("lastRun") ? (
                        <LastRunCell autopilot={autopilot} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("nextRun") ? (
                        <NextRunCell autopilot={autopilot} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("mode") ? (
                        <ModeCell autopilot={autopilot} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("creator") ? (
                        <CreatorCell autopilot={autopilot} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("created") ? (
                        <ListGridCell className="hidden whitespace-nowrap text-caption tabular-nums text-muted-foreground @2xl:flex">
                          {new Date(autopilot.created_at).toLocaleDateString(locale)}
                        </ListGridCell>
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      <ListGridCell className="justify-end px-0">
                        <AutopilotRowActions row={autopilot} />
                      </ListGridCell>
                    </ListGridRow>
                  );
                })}
              </ListGridBody>
            </ListGrid>
          </div>
        </>
      )}

      <AutopilotBatchToolbar
        rows={selectedRows}
        onClear={() => setSelectedIds(new Set())}
      />

      {createOpen && (
        <Suspense fallback={null}>
          <AutopilotDialog
            mode="create"
            open={createOpen}
            onOpenChange={setCreateOpen}
            initial={
              selectedTemplate
                ? {
                    // 模板标题走 i18n，用户看到的默认名与其语言一致；prompt 正文保持
                    // 原始英文，因为它会整段注入智能体任务。
                    title: t(($) => $.templates[selectedTemplate.id].title),
                    description: selectedTemplate.prompt,
                  }
                : undefined
            }
            initialSchedule={selectedTemplate ? selectedTemplate.schedule : undefined}
          />
        </Suspense>
      )}
    </div>
  );
}
