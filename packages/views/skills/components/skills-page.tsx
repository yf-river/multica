// 本文件职责：技能（Skills）列表主页面。负责加载当前工作区的全部技能并渲染成
// 一张可搜索/筛选/排序/勾选的虚拟滚动表格；每行聚合展示该技能被哪些 agent 使用、
// 来源、创建者、更新时间等信息，并提供「新建技能」入口、列显隐工具栏与底部批量
// 操作栏。排序/筛选/列显隐等偏好持久化在 core 的视图 store 里。
"use client";

import { useMemo, useRef, useState } from "react";
import {
  ChevronRight,
  AlertCircle,
  AlertTriangle,
  Download,
  HardDrive,
  Lock,
  Pencil,
  Plus,
} from "lucide-react";
import { SkillIcon } from "../lib/skill-icon";
import type {
  Agent,
  AgentRuntime,
  MemberWithUser,
  Skill,
  SkillSummary,
} from "@multica/core/types";
import { resourceLabelsOptions } from "@multica/core/labels/queries";
import { useQueries, useQuery } from "@tanstack/react-query";
import { useVirtualizer } from "@tanstack/react-virtual";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  agentListOptions,
  memberListOptions,
  selectSkillAssignments,
  skillListOptions,
} from "@multica/core/workspace/queries";
import {
  runtimeDisplayLabel,
  runtimeListOptions,
} from "@multica/core/runtimes";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
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
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import {
  rowLinkInteractiveProps,
  useNavigation,
  useRowLink,
} from "../../navigation";
import {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "../../layout/collection-page";
import { canEditSkill } from "../hooks/use-can-edit-skill";
import { originSourceUrl, readOrigin, type OriginInfo } from "../lib/origin";
import { CreateSkillDialog } from "./create-skill-dialog";
import {
  useSkillsViewStore,
  DEFAULT_HIDDEN_COLUMNS,
  type SkillColumnKey,
  type SkillSortField,
} from "@multica/core/skills/stores";
import { SkillListToolbar } from "./skill-list-toolbar";
import {
  SkillBatchToolbar,
  SkillRowActions,
  type SkillActionsContext,
} from "./skill-list-actions";
import { useT, useTimeAgo } from "../../i18n";

// 列模板：表头、数据行、骨架屏三处共用的唯一事实来源。
// 轨道结构：[边距 0.75rem][复选框 1rem][名称，唯一 fr 弹性轨道][description][usedBy][source]
// [creator][updated][created][操作 1.75rem][边距 0.75rem]。
// 内容单元格默认带 list-grid.tsx 的 px-2 内边距（结构列用 px-0 取消），窄边距轨道
// 加上单元格内边距后，内容正好距容器边缘 20px。非核心列带 `hidden @2xl:flex`；
// 断点查的是容器宽度（页面外层是 `@container`）而非视口，所以侧栏/分栏造成的容器
// 变窄也能正确触发换档。
// 可隐藏列一律用 CSS 变量定死宽度（不用 max-content）：行是虚拟化的，若轨道宽度
// 随内容浮动，行滚入/滚出时会不断改版式。截断也从「每格 max-w」挪到了轨道自身。
// 用户隐藏某列时 columnTrackVars 把对应变量清零、轨道坍缩成 0 宽，效果等同旧的
// max-content 占位；空的占位格仍会渲染，保证 subgrid 自动排布不乱。
//
// 两档响应式（取代早前按档位拆断点的做法）：
// - 容器 ≥ @2xl（672px）：所见即所得——用户勾选的列全部渲染。网格带
//   min-width = Σ(启用轨道+间距)，列总宽超出容器时外层横向滚动兜底；绝不让某个
//   启用的列静默消失（「勾了白勾」这个 bug 曾出过两次）。
// - 容器 < @2xl（手机、窄分栏）：固定核心两列（名称 + usedBy），不横向滚动，
//   描述按列偏好显示在名称下方。
const GRID_COLS =
  "grid-cols-[0.75rem_1rem_minmax(120px,1fr)_var(--lgc-usedby)_1.75rem_0.75rem] " +
  "@2xl:grid-cols-[0.75rem_1rem_var(--lgc-name)_var(--lgc-description)_var(--lgc-usedby)_var(--lgc-source)_var(--lgc-creator)_var(--lgc-updated)_var(--lgc-created)_1.75rem_0.75rem]";

// 行高 48px（h-12）。虚拟滚动要求每行固定这个高度：行高恒定，虚拟器才能
// 跳过逐行实测、直接按固定尺寸推算渲染区间与偏移。
const ROW_HEIGHT = 48;

// 可隐藏列宽的唯一事实来源：列轨道变量与网格 min-width 都从这组数推导。
const COLUMN_WIDTHS: Record<SkillColumnKey, number> = {
  description: 320,
  usedBy: 144,
  source: 152,
  creator: 144,
  updated: 104,
  created: 104,
};

// 固定轨道宽（左右边距 12+12、复选框 16、名称最小 200、操作 28）加上宽模板
// 11 条轨道之间 10 个 gap-x-3 的间距（宽度为 0 的隐藏轨道同样占一个间距）。
const FIXED_TRACKS_WIDTH = 268 + 10 * 12;

/**
 * 把各列显隐状态折算成 CSS 变量：可见列写入真实宽度，隐藏列写 0px 让轨道
 * 坍缩；同时累加出启用轨道的最小总宽 `--lgc-minw`，供宽容器横向滚动兜底。
 */
function columnTrackVars(
  isVisible: (key: SkillColumnKey) => boolean,
): React.CSSProperties {
  const width = (key: SkillColumnKey) =>
    isVisible(key) ? `${COLUMN_WIDTHS[key]}px` : "0px";
  const minWidth =
    FIXED_TRACKS_WIDTH +
    (Object.keys(COLUMN_WIDTHS) as SkillColumnKey[]).reduce(
      (sum, key) => sum + (isVisible(key) ? COLUMN_WIDTHS[key] : 0),
      0,
    );
  return {
    "--lgc-name": isVisible("description") ? "240px" : "minmax(200px,1fr)",
    "--lgc-description": isVisible("description") ? "minmax(280px,1fr)" : "0px",
    "--lgc-usedby": width("usedBy"),
    "--lgc-source": width("source"),
    "--lgc-creator": width("creator"),
    "--lgc-updated": width("updated"),
    "--lgc-created": width("created"),
    "--lgc-minw": `${minWidth}px`,
  } as React.CSSProperties;
}

/**
 * 排序字段的类型。排序/筛选/列显隐的类型与默认值定义在 core 的视图 store
 * （@multica/core/skills/stores/view-store）里，持久化状态与 UI 共用同一份定义；
 * 这里只是为工具栏引用方便而重新导出。
 */
export type SortField = SkillSortField;

/**
 * 表格中的一行数据：一个技能及其聚合信息，列表单元格、批量操作栏共用。
 * - `skill`：技能摘要本身；
 * - `agents`：正在使用该技能的所有 agent；
 * - `creator`：创建者（成员表里查得到才非空，否则为 null）；
 * - `runtime`：来源为本地 runtime 时对应的运行时实体，否则为 null；
 * - `originType`：技能来源类型（手动/本地 runtime/ClawHub/Skills.sh/GitHub）；
 * - `canEdit`：当前用户能否编辑/删除该技能。
 */
export interface SkillRow {
  skill: SkillSummary;
  agents: Agent[];
  creator: MemberWithUser | null;
  runtime: AgentRuntime | null;
  originType: OriginInfo["type"];
  canEdit: boolean;
}

// ---------------------------------------------------------------------------
// 页头栏：复用公共的 CollectionPageHeader，让移动端侧栏开关与 h-12 顶栏
// 和其他仪表盘列表页保持一致。
// ---------------------------------------------------------------------------

/**
 * 页头组件：展示技能图标、标题、总数、副标题与「新建技能」按钮。
 */
function PageHeaderBar({
  totalCount,
  onCreate,
}: {
  totalCount: number;
  onCreate: () => void;
}) {
  const { t } = useT("skills");
  return (
    <CollectionPageHeader
      icon={SkillIcon}
      title={t(($) => $.page.title)}
      count={totalCount}
      description={t(($) => $.page.tagline)}
      learnMore={{
        href: "https://multica.ai/docs/skills",
        label: t(($) => $.page.learn_more),
      }}
      actions={
        <CollectionPageHeaderAction
          icon={Plus}
          label={t(($) => $.page.new_skill)}
          onClick={onCreate}
        />
      }
    />
  );
}

// ---------------------------------------------------------------------------
// 各列单元格
// ---------------------------------------------------------------------------

// 悬停时才浮现的多选复选框。与 SkillPickerList 同一套做法：shadcn 的 Checkbox
// 纯展示（`pointer-events-none`，永远点不到它），外包的 <button> 才负责开关。
// 点击时 stopPropagation 阻止冒泡到整行的跳转逻辑（见 useRowLink）——行本身是
// <div> 而非 <a>，所以不需要 preventDefault。
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
          checked
            ? ""
            : "opacity-0 transition-opacity group-hover/row:opacity-100"
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

function NameCell({
  row,
  showDescription,
}: {
  row: SkillRow;
  showDescription: boolean;
}) {
  const { t } = useT("skills");
  const { skill, canEdit } = row;
  return (
    <ListGridCell className="gap-1.5">
      <div className="min-w-0 flex-1">
        <div className="truncate text-body font-medium">{skill.name}</div>
        {showDescription && (
          <div
            className="truncate text-caption text-muted-foreground @2xl:hidden"
            title={skill.description}
          >
            {skill.description || "—"}
          </div>
        )}
      </div>
      {!canEdit && (
        <Tooltip>
          <TooltipTrigger
            render={<Lock className="h-3 w-3 shrink-0 text-faint-foreground" />}
          />
          <TooltipContent>{t(($) => $.table.lock_tooltip)}</TooltipContent>
        </Tooltip>
      )}
    </ListGridCell>
  );
}

function UsedByCell({ agents }: { agents: Agent[] }) {
  const { t } = useT("skills");
  if (agents.length === 0) {
    return (
      <ListGridCell>
        <span className="text-caption text-muted-foreground">
          {t(($) => $.table.unused)}
        </span>
      </ListGridCell>
    );
  }
  const soleAgent = agents.length === 1 ? agents[0] : undefined;
  if (soleAgent) {
    const agent = soleAgent;
    return (
      <ListGridCell className="gap-1.5">
        <ActorAvatar
          name={agent.name}
          initials={agent.name.slice(0, 2).toUpperCase()}
          avatarUrl={resolvePublicFileUrl(agent.avatar_url)}
          isAgent
          size="md"
        />
        <span className="min-w-0 truncate text-caption text-muted-foreground">
          {agent.name}
        </span>
      </ListGridCell>
    );
  }
  const visible = agents.slice(0, 3);
  const extra = agents.length - visible.length;
  return (
    <ListGridCell>
      <div className="flex items-center -space-x-1.5">
        {visible.map((a) => (
          <Tooltip key={a.id}>
            <TooltipTrigger
              render={
                <span className="inline-flex rounded-full ring-2 ring-background">
                  <ActorAvatar
                    name={a.name}
                    initials={a.name.slice(0, 2).toUpperCase()}
                    avatarUrl={resolvePublicFileUrl(a.avatar_url)}
                    isAgent
                    size="md"
                  />
                </span>
              }
            />
            <TooltipContent>{a.name}</TooltipContent>
          </Tooltip>
        ))}
        {extra > 0 && (
          <span className="inline-flex h-6 w-6 items-center justify-center rounded-full bg-muted text-caption font-medium text-muted-foreground ring-2 ring-background">
            +{extra}
          </span>
        )}
      </div>
    </ListGridCell>
  );
}

function SourceCell({
  skill,
  runtime,
}: {
  skill: SkillSummary;
  runtime: AgentRuntime | null;
}) {
  const { t } = useT("skills");
  const origin = readOrigin(skill);

  let icon = <Pencil className="h-3 w-3 shrink-0" />;
  let label: string = t(($) => $.table.source_manual);
  if (origin.type === "runtime_local") {
    icon = <HardDrive className="h-3 w-3 shrink-0" />;
    label = runtime
      ? t(($) => $.table.source_runtime_named, {
          name: runtimeDisplayLabel(runtime),
        })
      : origin.provider
        ? t(($) => $.table.source_runtime_provider, {
            provider: origin.provider,
          })
        : t(($) => $.table.source_runtime_unknown);
  } else if (origin.type === "clawhub") {
    icon = <Download className="h-3 w-3 shrink-0" />;
    label = t(($) => $.table.source_clawhub);
  } else if (origin.type === "skills_sh") {
    icon = <Download className="h-3 w-3 shrink-0" />;
    label = t(($) => $.table.source_skills_sh);
  } else if (origin.type === "github") {
    icon = <Download className="h-3 w-3 shrink-0" />;
    label = t(($) => $.table.source_github);
  }

  // 从外部导入的技能要把 label 变成可点开上游页面的链接。行本身整行可点，
  // 所以给这个 <a> 展开 rowLinkInteractiveProps：它同时 stopPropagation 掉
  // click 与 auxclick（中键），点击既不会误触整行跳转，锚点的原生打开行为
  // 也保持原样。
  const sourceUrl = originSourceUrl(origin);

  return (
    <ListGridCell className="hidden gap-1.5 text-caption text-muted-foreground @2xl:flex">
      {icon}
      {sourceUrl ? (
        <Tooltip>
          <TooltipTrigger
            render={
              <a
                href={sourceUrl}
                target="_blank"
                rel="noopener noreferrer"
                {...rowLinkInteractiveProps}
                className="min-w-0 truncate hover:underline"
              >
                {label}
              </a>
            }
          />
          <TooltipContent side="top">{sourceUrl}</TooltipContent>
        </Tooltip>
      ) : (
        <span className="min-w-0 truncate">{label}</span>
      )}
    </ListGridCell>
  );
}

function CreatorCell({ creator }: { creator: MemberWithUser | null }) {
  return (
    <ListGridCell className="hidden gap-1.5 @2xl:flex">
      {creator && (
        <>
          <ActorAvatar
            name={creator.name}
            initials={creator.name.slice(0, 2).toUpperCase()}
            avatarUrl={resolvePublicFileUrl(creator.avatar_url)}
            size="md"
          />
          <span className="min-w-0 truncate text-caption text-muted-foreground">
            {creator.name}
          </span>
        </>
      )}
    </ListGridCell>
  );
}

// ---------------------------------------------------------------------------
// 空状态：工作区里一个技能都没有时，整页居中展示引导文案与新建按钮
// ---------------------------------------------------------------------------

function EmptyState({ onCreate }: { onCreate: () => void }) {
  const { t } = useT("skills");
  return (
    <CollectionPageState
      icon={SkillIcon}
      title={t(($) => $.page.empty.title)}
      description={t(($) => $.page.empty.description)}
      actions={
        <Button type="button" onClick={onCreate} size="sm">
          <Plus aria-hidden="true" className="size-3" />
          {t(($) => $.page.new_skill)}
        </Button>
      }
    />
  );
}

// ---------------------------------------------------------------------------
// 列表主体（表头 + 行）
// ---------------------------------------------------------------------------

/**
 * 表格表头：渲染各列表头文字；支持点击排序（可排序列带升降序指示），
 * 并在复选框轨道放一个三态全选按钮。
 */
function SkillListHeader({
  sortField,
  sortDirection,
  onSort,
  allSelected,
  someSelected,
  onToggleAll,
  isColVisible,
}: {
  sortField: SortField;
  sortDirection: ListGridSortDirection;
  onSort: (field: SortField) => void;
  allSelected: boolean;
  someSelected: boolean;
  onToggleAll: () => void;
  isColVisible: (key: SkillColumnKey) => boolean;
}) {
  const { t } = useT("skills");
  const sorted = (field: SortField) =>
    sortField === field ? sortDirection : false;
  const anySelected = allSelected || someSelected;
  return (
    <ListGridHeader>
      {/* 复选框轨道的三态全选按钮（全选/半选/未选）。与行内单元格同样的
          「展示用 Checkbox + 交互外壳按钮」模式；表头悬停或已有任意选中时
          才会浮现。 */}
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
        {t(($) => $.table.name)}
      </ListGridHeaderCell>
      <ListGridHeaderCell
        className={
          isColVisible("description")
            ? "hidden @2xl:flex"
            : "hidden px-0 @2xl:flex"
        }
      >
        {isColVisible("description") ? t(($) => $.table.description) : null}
      </ListGridHeaderCell>
      {isColVisible("usedBy") ? (
        <ListGridHeaderCell
          sorted={sorted("usedBy")}
          onSort={() => onSort("usedBy")}
        >
          {t(($) => $.table.used_by)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="px-0" />
      )}
      {isColVisible("source") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.table.source)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("creator") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.table.created_by)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("updated") ? (
        <ListGridHeaderCell
          className="hidden @2xl:flex"
          sorted={sorted("updated")}
          onSort={() => onSort("updated")}
        >
          {t(($) => $.table.updated)}
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
          {t(($) => $.table.created)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      <span aria-hidden="true" />
    </ListGridHeader>
  );
}

function LoadingSkeleton() {
  return (
    <ListGrid
      className={GRID_COLS}
      style={columnTrackVars((key) => !DEFAULT_HIDDEN_COLUMNS.includes(key))}
    >
      <ListGridHeader>
        <span aria-hidden="true" />
        <ListGridHeaderCell>
          <Skeleton className="h-3 w-12" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-40" />
        </ListGridHeaderCell>
        <ListGridHeaderCell>
          <Skeleton className="h-3 w-14" />
        </ListGridHeaderCell>
        {/* source 与 created 默认隐藏——仍给它们铺上空占位格，让骨架屏的
            轨道排布与默认布局一致。 */}
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-10" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden @2xl:flex">
          <Skeleton className="h-3 w-12" />
        </ListGridHeaderCell>
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
        <span aria-hidden="true" />
      </ListGridHeader>
      {Array.from({ length: 5 }).map((_, i) => (
        <ListGridRow key={i} className="hover:bg-transparent">
          <span aria-hidden="true" />
          <ListGridCell>
            <Skeleton className="h-3.5 w-40 max-w-full" />
          </ListGridCell>
          <ListGridCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-60" />
          </ListGridCell>
          <ListGridCell>
            <Skeleton className="h-5 w-14" />
          </ListGridCell>
          <ListGridCell className="hidden px-0 @2xl:flex" />
          <ListGridCell className="hidden gap-1.5 @2xl:flex">
            <Skeleton className="size-5 rounded-full" />
            <Skeleton className="h-3 w-12" />
          </ListGridCell>
          <ListGridCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-10" />
          </ListGridCell>
          <ListGridCell className="hidden px-0 @2xl:flex" />
          <span aria-hidden="true" />
        </ListGridRow>
      ))}
    </ListGrid>
  );
}

// ---------------------------------------------------------------------------
// 页面主组件
// ---------------------------------------------------------------------------

/**
 * 技能列表页（默认导出）。加载技能/agent/成员/runtime 四路数据，把它们组装成
 * 行模型后做筛选排序与虚拟滚动渲染；同时管理「新建技能」对话框、行多选与底部
 * 批量操作栏。props 无（工作区 id 从上下文取）。
 */
export default function SkillsPage() {
  const { t } = useT("skills");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const rowLink = useRowLink();
  const timeAgo = useTimeAgo();
  const currentUserId = useAuthStore((s) => s.user?.id ?? null);

  const {
    data: skills = [],
    isLoading,
    error: listError,
    refetch: refetchList,
  } = useQuery(skillListOptions(wsId));
  const { data: agents = [], error: agentsError } = useQuery(
    agentListOptions(wsId),
  );
  const { data: members = [], error: membersError } = useQuery(
    memberListOptions(wsId),
  );
  const { data: runtimes = [], error: runtimesError } = useQuery(
    runtimeListOptions(wsId),
  );

  const labelQueries = useQueries({
    queries: skills.map((skill) =>
      resourceLabelsOptions(wsId, "skill", skill.id),
    ),
  });
  const collapsedGroups = useSkillsViewStore((s) => s.collapsedGroups);
  const setCollapsedGroups = useSkillsViewStore((s) => s.setCollapsedGroups);
  const [createOpen, setCreateOpen] = useState(false);
  const [selectedIds, setSelectedIds] = useState<ReadonlySet<string>>(
    new Set(),
  );
  const [search, setSearch] = useState("");

  // 已持久化的视图偏好（按工作区、按用户/设备分开保存）。表头排序按钮与工具栏
  // 的显示面板写的是同一个 store，两处永远一致；搜索词与勾选则刻意只存在于
  // 当前会话，不落盘。
  const sortField = useSkillsViewStore((s) => s.sortField);
  const sortDirection = useSkillsViewStore((s) => s.sortDirection);
  const hiddenColumns = useSkillsViewStore((s) => s.hiddenColumns);
  const filters = useSkillsViewStore((s) => s.filters);
  const handleSort = useSkillsViewStore((s) => s.toggleSort);
  const handleSortFieldSelect = useSkillsViewStore((s) => s.setSortField);
  const setSortDirection = useSkillsViewStore((s) => s.setSortDirection);
  const toggleColumn = useSkillsViewStore((s) => s.toggleColumn);
  const toggleFilter = useSkillsViewStore((s) => s.toggleFilter);
  const clearFilters = useSkillsViewStore((s) => s.clearFilters);

  const isColVisible = (key: SkillColumnKey) => !hiddenColumns.includes(key);

  const toggleSelected = (id: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const assignments = useMemo(() => selectSkillAssignments(agents), [agents]);

  const membersById = useMemo(() => {
    const map = new Map<string, MemberWithUser>();
    for (const m of members) map.set(m.user_id, m);
    return map;
  }, [members]);

  const runtimesById = useMemo(() => {
    const map = new Map<string, AgentRuntime>();
    for (const r of runtimes) map.set(r.id, r);
    return map;
  }, [runtimes]);

  const myRole =
    members.find((m: MemberWithUser) => m.user_id === currentUserId)?.role ??
    null;
  const isAdmin = myRole === "owner" || myRole === "admin";

  const actionsCtx: SkillActionsContext = {
    wsId,
    agents,
    currentUserId,
    isAdmin,
  };

  // 全量组装好的行集合（不过滤、不排序）——工具栏的下拉选项与计数都从它派生。
  const allRows = useMemo<SkillRow[]>(() => {
    return skills.map((skill) => {
      const origin = readOrigin(skill);
      const runtime =
        origin.type === "runtime_local" && origin.runtime_id
          ? (runtimesById.get(origin.runtime_id) ?? null)
          : null;
      return {
        skill,
        agents: assignments.get(skill.id) ?? [],
        creator: skill.created_by
          ? (membersById.get(skill.created_by) ?? null)
          : null,
        runtime,
        originType: origin.type,
        canEdit: canEditSkill(skill, { userId: currentUserId, role: myRole }),
      };
    });
  }, [skills, assignments, membersById, runtimesById, currentUserId, myRole]);

  // 最终可见行：先按名称和描述搜索词 + 三类筛选（使用情况/来源/agent/创建者）过滤，
  // 再按当前排序字段排序；usedBy 排序相同时退回按名称排，保证顺序稳定。
  const rows = useMemo<SkillRow[]>(() => {
    const q = search.trim().toLowerCase();
    const filtered = allRows.filter((row) => {
      if (
        q &&
        ![row.skill.name, row.skill.description].some((value) =>
          value.toLowerCase().includes(q),
        )
      )
        return false;
      if (filters.usage.length > 0) {
        const usage = row.agents.length > 0 ? "used" : "unused";
        if (!filters.usage.includes(usage)) return false;
      }
      if (
        filters.origins.length > 0 &&
        !filters.origins.includes(row.originType)
      ) {
        return false;
      }
      if (
        filters.agents.length > 0 &&
        !row.agents.some((a) => filters.agents.includes(a.id))
      ) {
        return false;
      }
      if (
        filters.creators.length > 0 &&
        (!row.skill.created_by ||
          !filters.creators.includes(row.skill.created_by))
      ) {
        return false;
      }
      return true;
    });

    const dir = sortDirection === "asc" ? 1 : -1;
    filtered.sort((a, b) => {
      if (sortField === "name") {
        return a.skill.name.localeCompare(b.skill.name) * dir;
      }
      if (sortField === "usedBy") {
        return (
          (a.agents.length - b.agents.length) * dir ||
          a.skill.name.localeCompare(b.skill.name)
        );
      }
      if (sortField === "created") {
        return (
          (Date.parse(a.skill.created_at) - Date.parse(b.skill.created_at)) *
          dir
        );
      }
      return (
        (Date.parse(a.skill.updated_at) - Date.parse(b.skill.updated_at)) * dir
      );
    });
    return filtered;
  }, [allRows, search, filters, sortField, sortDirection]);

  const grouped = new Map<
    string,
    { id: string; name: string; rows: SkillRow[] }
  >();
  const labelsById = new Map(
    skills.map((skill, i) => [skill.id, labelQueries[i]?.data ?? []]),
  );
  for (const row of rows) {
    const labels = labelsById.get(row.skill.id) ?? [];
    for (const label of labels.length
      ? labels
      : [{ id: "unclassified", name: t(($) => $.groups.unclassified) }]) {
      if (!grouped.has(label.id))
        grouped.set(label.id, { id: label.id, name: label.name, rows: [] });
      grouped.get(label.id)!.rows.push(row);
    }
  }
  const groups = [...grouped.values()].sort((a, b) =>
    a.id === "unclassified"
      ? 1
      : b.id === "unclassified"
        ? -1
        : a.name.localeCompare(b.name, "zh-CN"),
  );
  const isCollapsed = (id: string) =>
    !search.trim() && collapsedGroups.includes(id);
  const groupedRows = groups.flatMap((group) => [
    { key: group.id, group, row: null as SkillRow | null },
    ...(isCollapsed(group.id)
      ? []
      : group.rows.map((row) => ({
          key: `${group.id}:${row.skill.id}`,
          group: null,
          row,
        }))),
  ]);

  // 行虚拟化——Linear 风格：虚拟器只负责算（可见行区间 + 偏移量），DOM 仍由
  // 我们掌控。偏移量转成行容器的 padding，于是挂载中的行始终是 subgrid 的直接
  // 子元素，列对齐不受影响。行高固定为 ROW_HEIGHT，因此无需逐行测量。滚动容器
  // 是唯一的外层滚动条（横纵共用）；为何弃用分体滚动结构见 ListGridBody 注释。
  const listScrollRef = useRef<HTMLDivElement | null>(null);
  const rowVirtualizer = useVirtualizer({
    count: groupedRows.length,
    getItemKey: (index) => groupedRows[index]!.key,
    getScrollElement: () => listScrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 10,
  });

  const handleCreated = (skill: Skill) => {
    navigation.push(paths.skillDetail(skill.id));
  };

  const selectedRows = rows.filter((row) => selectedIds.has(row.skill.id));
  const allSelected = rows.length > 0 && selectedRows.length === rows.length;
  const someSelected = selectedRows.length > 0 && !allSelected;
  const handleToggleAll = () => {
    setSelectedIds(
      allSelected ? new Set() : new Set(rows.map((r) => r.skill.id)),
    );
  };

  // --- 列表请求失败：整页降级为错误态，可重试 ---
  if (listError || labelQueries.some((query) => query.isError)) {
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <PageHeaderBar totalCount={0} onCreate={() => setCreateOpen(true)} />
        <CollectionPageState
          role="alert"
          tone="destructive"
          icon={AlertCircle}
          title={t(($) => $.page.list_error.title)}
          description={
            listError instanceof Error
              ? listError.message
              : t(($) => $.page.list_error.fallback)
          }
          actions={
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                void refetchList();
                labelQueries.forEach((query) => void query.refetch());
              }}
            >
              {t(($) => $.page.list_error.retry)}
            </Button>
          }
        />
      </div>
    );
  }

  const totalCount = skills.length;
  const showEmpty = !isLoading && totalCount === 0;
  const supportingQueryDown =
    !!agentsError || !!membersError || !!runtimesError;

  // 可视区上/下没有挂载的行折成滚动主体的上下 padding，等价于 Linear 的
  // --x-paddingTop/Bottom 偏移做法。
  const virtualItems = rowVirtualizer.getVirtualItems();
  const firstVirtual = virtualItems[0];
  const lastVirtual = virtualItems[virtualItems.length - 1];
  const virtualPadding = {
    top: firstVirtual ? firstVirtual.start : 0,
    bottom: lastVirtual ? rowVirtualizer.getTotalSize() - lastVirtual.end : 0,
  };

  return (
    // relative：给批量操作栏当定位锚点——它相对页面（而非视口）水平居中。
    <div className="relative flex flex-1 min-h-0 flex-col">
      <PageHeaderBar
        totalCount={totalCount}
        onCreate={() => setCreateOpen(true)}
      />

      {supportingQueryDown && (
        <div
          role="status"
          className="flex shrink-0 items-start gap-2 border-b bg-warning/10 px-6 py-2 text-caption text-muted-foreground"
        >
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warning" />
          <span>{t(($) => $.page.supporting_data_warning)}</span>
        </div>
      )}

      {isLoading || labelQueries.some((query) => query.isPending) ? (
        <div className="flex-1 overflow-y-auto @container">
          <LoadingSkeleton />
        </div>
      ) : showEmpty ? (
        <div className="flex flex-1 items-center justify-center">
          <EmptyState onCreate={() => setCreateOpen(true)} />
        </div>
      ) : (
        <>
          <SkillListToolbar
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
            allRows={allRows}
            visibleCount={rows.length}
          />
          <div className="flex justify-end gap-2 px-4 pb-2">
            <Button
              variant="ghost"
              size="sm"
              disabled={Boolean(search.trim())}
              onClick={() => setCollapsedGroups([])}
            >
              {t(($) => $.groups.expand_all)}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              disabled={Boolean(search.trim())}
              onClick={() => setCollapsedGroups(groups.map((g) => g.id))}
            >
              {t(($) => $.groups.collapse_all)}
            </Button>
          </div>
          <div
            ref={listScrollRef}
            className="min-h-0 flex-1 overflow-auto @container"
          >
            <ListGrid
              className={`${GRID_COLS} @2xl:min-w-[var(--lgc-minw)]`}
              style={columnTrackVars(isColVisible)}
            >
              <SkillListHeader
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
                    {t(($) => $.page.no_matches.title)}
                  </div>
                )}
                {virtualItems.map((vi) => {
                  const item = groupedRows[vi.index];
                  if (item?.group) {
                    const group = item.group;
                    return (
                      <div
                        key={item.key}
                        className="col-span-full flex h-12 items-center border-t border-surface-border bg-muted/30 px-4"
                      >
                        <button
                          type="button"
                          className="flex min-h-10 items-center gap-2 rounded px-2 text-body font-medium focus-visible:outline-2 focus-visible:outline-ring"
                          aria-expanded={!isCollapsed(group.id)}
                          onClick={() =>
                            setCollapsedGroups(
                              isCollapsed(group.id)
                                ? collapsedGroups.filter(
                                    (id) => id !== group.id,
                                  )
                                : [...collapsedGroups, group.id],
                            )
                          }
                        >
                          <ChevronRight
                            className={`size-4 ${isCollapsed(group.id) ? "" : "rotate-90"}`}
                          />
                          {group.name}
                          <span className="text-caption tabular-nums text-muted-foreground">
                            {group.rows.length}
                          </span>
                        </button>
                      </div>
                    );
                  }
                  const row = item?.row;
                  if (!row) return null;
                  return (
                    <ListGridRow
                      key={item!.key}
                      className={`cursor-pointer ${
                        selectedIds.has(row.skill.id) ? "bg-accent/30" : ""
                      }`}
                      {...rowLink(paths.skillDetail(row.skill.id))}
                    >
                      <CheckboxCell
                        checked={selectedIds.has(row.skill.id)}
                        onToggle={() => toggleSelected(row.skill.id)}
                      />
                      <NameCell
                        row={row}
                        showDescription={isColVisible("description")}
                      />
                      <ListGridCell
                        className={
                          isColVisible("description")
                            ? "hidden text-body text-muted-foreground @2xl:flex"
                            : "hidden px-0 @2xl:flex"
                        }
                      >
                        {isColVisible("description") && (
                          <Tooltip>
                            <TooltipTrigger
                              render={
                                <span
                                  tabIndex={0}
                                  className="min-w-0 truncate rounded-sm focus-visible:outline-2 focus-visible:outline-ring"
                                />
                              }
                            >
                              {row.skill.description || "—"}
                            </TooltipTrigger>
                            <TooltipContent className="max-w-sm break-words">
                              {row.skill.description || "—"}
                            </TooltipContent>
                          </Tooltip>
                        )}
                      </ListGridCell>
                      {isColVisible("usedBy") ? (
                        <UsedByCell agents={row.agents} />
                      ) : (
                        <ListGridCell className="px-0" />
                      )}
                      {isColVisible("source") ? (
                        <SourceCell skill={row.skill} runtime={row.runtime} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("creator") ? (
                        <CreatorCell creator={row.creator} />
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("updated") ? (
                        <ListGridCell className="hidden whitespace-nowrap text-caption tabular-nums text-muted-foreground @2xl:flex">
                          {timeAgo(row.skill.updated_at)}
                        </ListGridCell>
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      {isColVisible("created") ? (
                        <ListGridCell className="hidden whitespace-nowrap text-caption tabular-nums text-muted-foreground @2xl:flex">
                          {timeAgo(row.skill.created_at)}
                        </ListGridCell>
                      ) : (
                        <ListGridCell className="hidden px-0 @2xl:flex" />
                      )}
                      <ListGridCell className="justify-end px-0">
                        <SkillRowActions row={row} ctx={actionsCtx} />
                      </ListGridCell>
                    </ListGridRow>
                  );
                })}
              </ListGridBody>
            </ListGrid>
          </div>
        </>
      )}

      <SkillBatchToolbar
        rows={selectedRows}
        ctx={actionsCtx}
        onClear={() => setSelectedIds(new Set())}
      />

      {createOpen && (
        <CreateSkillDialog
          onClose={() => setCreateOpen(false)}
          onCreated={handleCreated}
        />
      )}
    </div>
  );
}
