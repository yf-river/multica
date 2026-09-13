"use client";

import { useMemo, useState } from "react";
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ExternalLink,
  Filter,
  Loader2,
  MoreHorizontal,
  Plus,
  Trash2,
  Users,
  X,
} from "lucide-react";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { toast } from "sonner";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import {
  agentListOptions,
  memberListOptions,
  squadListOptions,
  workspaceKeys,
} from "@multica/core/workspace/queries";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { useAuthStore } from "@multica/core/auth";
import { api } from "@multica/core/api";
import { useModalStore } from "@multica/core/modals";
import {
  useSquadsViewStore,
  SQUAD_SCOPES,
  SQUAD_DEFAULT_HIDDEN_COLUMNS,
  type SquadColumnKey,
  type SquadListFilters,
  type SquadsScope,
  type SquadSortField,
} from "@multica/core/squads/stores";
import type { Agent, MemberWithUser, Squad } from "@multica/core/types";
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
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  ListGrid,
  ListGridCell,
  ListGridHeader,
  ListGridHeaderCell,
  ListGridRow,
  LIST_GRID_BOTTOM_CLEARANCE,
  type ListGridSortDirection,
} from "@multica/ui/components/ui/list-grid";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { ActorAvatar } from "../../common/actor-avatar";
import { FILTER_ITEM_CLASS, HoverCheck } from "../../common/hover-check";
import { useIntentNavigate, useRowLink } from "../../navigation";
import {
  CollectionPageHeader,
  CollectionPageHeaderAction,
  CollectionPageState,
} from "../../layout/collection-page";
import { useLocale, useT } from "../../i18n";
import { PAGE_TOOLBAR } from "../../layout/page-header";

/**
 * squads 模块：小队列表页。
 * 以表格形式列出当前工作区的小队，支持范围切换（我的/全部）、按创建者与
 * 负责人筛选、排序、显示列开关，以及对小队的归档（删除）操作。
 */

// 列模板——ListGrid 家族里最简单的一员（小队是实体里最少的，通常只有 1-5
// 行）：子网格模板 + var 轨道 + 两档响应式 + 单一滚动容器，但无需虚拟滚动、
// 复选框或批量操作。身份采用两行式（头像 + 名称 + 简介，64px），与 agents
// 列表一致。名称 + 负责人是核心列（<@2xl 也显示）；成员 / 创建者 / 创建时间
// 在 @2xl 才显示。当查看者无法管理任何小队（仅工作区管理员可管理）时，
// kebab 操作列塌缩隐藏。
const GRID_COLS =
  "grid-cols-[0.75rem_minmax(120px,1fr)_var(--sqc-leader)_var(--sqc-kebab)_0.75rem] " +
  "@2xl:grid-cols-[0.75rem_minmax(200px,1fr)_var(--sqc-leader)_var(--sqc-members)_var(--sqc-creator)_var(--sqc-created)_var(--sqc-kebab)_0.75rem]";

const LEADER_WIDTH = 160;
const COLUMN_WIDTHS: Record<SquadColumnKey, number> = {
  members: 120,
  creator: 144,
  created: 104,
};

// 固定轨道宽度：两侧留白（12+12）+ 名称列最小 200 + 负责人列 160，再加上
// 宽模板 8 条轨道之间的 7 个 gap-x-3 间隙（零宽轨道仍占用 gap）。
const FIXED_TRACKS_WIDTH = 224 + LEADER_WIDTH + 7 * 12;

function columnTrackVars(
  isVisible: (key: SquadColumnKey) => boolean,
  showActions: boolean,
): React.CSSProperties {
  const width = (key: SquadColumnKey) =>
    isVisible(key) ? `${COLUMN_WIDTHS[key]}px` : "0px";
  const minWidth =
    FIXED_TRACKS_WIDTH +
    (Object.keys(COLUMN_WIDTHS) as SquadColumnKey[]).reduce(
      (sum, key) => sum + (isVisible(key) ? COLUMN_WIDTHS[key] : 0),
      0,
    ) +
    (showActions ? 28 : 0);
  return {
    "--sqc-leader": `${LEADER_WIDTH}px`,
    "--sqc-members": width("members"),
    "--sqc-creator": width("creator"),
    "--sqc-created": width("created"),
    "--sqc-kebab": showActions ? "1.75rem" : "0px",
    "--sqc-minw": `${minWidth}px`,
  } as React.CSSProperties;
}

// ---------------------------------------------------------------------------
// 单元格（每列的数据单元）
// ---------------------------------------------------------------------------

/** 小队头像：有自定义头像就显示图片，否则按名称缩写渲染团队占位头像。 */
function SquadAvatar({ squad }: { squad: Squad }) {
  const initials = squad.name
    .split(" ")
    .map((w) => w[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);
  if (squad.avatar_url) {
    return (
      <ActorAvatarBase
        name={squad.name}
        initials={initials}
        avatarUrl={resolvePublicFileUrl(squad.avatar_url)}
        size="lg"
        className="shrink-0"
      />
    );
  }
  return (
    <ActorAvatarBase
      name={squad.name}
      initials={initials}
      isSquad
      size="lg"
      className="shrink-0"
    />
  );
}

// 两行式身份单元格——与 agents 列表形式相同。
function NameCell({ squad }: { squad: Squad }) {
  return (
    <ListGridCell className="gap-3">
      <SquadAvatar squad={squad} />
      <div className="min-w-0 flex-1">
        <span className="block min-w-0 truncate text-body font-medium">
          {squad.name}
        </span>
        {squad.description ? (
          <span className="block min-w-0 truncate text-caption text-muted-foreground">
            {squad.description}
          </span>
        ) : null}
      </div>
    </ListGridCell>
  );
}

/** 负责人单元格：展示小队 leader（始终是 agent）的头像与名称。 */
function LeaderCell({
  leaderId,
  leader,
}: {
  leaderId: string;
  leader: Agent | undefined;
}) {
  return (
    <ListGridCell className="gap-1.5">
      <ActorAvatar actorType="agent" actorId={leaderId} size="sm" />
      <span className="min-w-0 truncate text-caption text-muted-foreground">
        {leader?.name ?? leaderId.slice(0, 8)}
      </span>
    </ListGridCell>
  );
}

// 多类型成员头像叠放（agent + 人类成员），由列表载荷的 member_preview /
// member_count 驱动。不用 AgentAvatarStack——那个只支持 agent。
function MembersCell({ squad }: { squad: Squad }) {
  const preview = squad.member_preview ?? [];
  const count = squad.member_count ?? preview.length;
  if (count === 0) {
    return (
      <ListGridCell className="hidden @2xl:flex">
        <span className="text-caption text-faint-foreground">—</span>
      </ListGridCell>
    );
  }
  const visible = preview.slice(0, 3);
  const overflow = count - visible.length;
  return (
    <ListGridCell className="hidden @2xl:flex">
      <div className="flex items-center -space-x-1.5">
        {visible.map((m) => (
          <span
            key={`${m.member_type}-${m.member_id}`}
            className="inline-flex rounded-full ring-2 ring-background"
          >
            <ActorAvatar
              actorType={m.member_type}
              actorId={m.member_id}
              size="md"
              enableHoverCard={m.member_type === "agent"}
            />
          </span>
        ))}
        {overflow > 0 && (
          <span className="inline-flex h-6 w-6 items-center justify-center rounded-full bg-muted text-caption font-medium text-muted-foreground ring-2 ring-background">
            +{overflow}
          </span>
        )}
      </div>
    </ListGridCell>
  );
}

// ---------------------------------------------------------------------------
// 归档（= 删除）弹窗——复用现有 archive_dialog 文案。
// 仅工作区 owner/admin 可用（后端同样拦截）。没有恢复接口，一旦归档，
// 小队就从 UI 里消失了。
// ---------------------------------------------------------------------------

/** 归档小队确认弹窗：确认后调用删除接口并失效小队列表缓存。 */
function ArchiveSquadDialog({
  squad,
  open,
  onOpenChange,
}: {
  squad: Squad;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("squads");
  const wsId = useCurrentWorkspace()?.id ?? "";
  const qc = useQueryClient();
  const archive = useMutation({
    mutationFn: () => api.deleteSquad(squad.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) });
      onOpenChange(false);
      toast.success(t(($) => $.archive_dialog.success));
    },
    onError: (err) =>
      toast.error(err instanceof Error ? err.message : String(err)),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.archive_dialog.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.archive_dialog.description, { name: squad.name })}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={archive.isPending}
            onClick={() => onOpenChange(false)}
          >
            {t(($) => $.archive_dialog.cancel)}
          </Button>
          <Button
            type="button"
            variant="destructive"
            size="sm"
            disabled={archive.isPending}
            onClick={() => archive.mutate()}
          >
            {archive.isPending ? (
              <>
                <Loader2 className="mr-1 size-3.5 animate-spin" />
                {t(($) => $.archive_dialog.archiving)}
              </>
            ) : (
              t(($) => $.archive_dialog.confirm)
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** 行操作菜单：在新标签打开详情、归档小队。 */
function SquadRowActions({ squad }: { squad: Squad }) {
  const { t } = useT("squads");
  const { t: tCommon } = useT("common");
  const p = useWorkspacePaths();
  const intentNavigate = useIntentNavigate();
  const [archiveOpen, setArchiveOpen] = useState(false);
  return (
    <span
      onClick={(e) => e.stopPropagation()}
      className="flex items-center"
    >
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <button
              type="button"
              aria-label={t(($) => $.page.row_menu)}
              className="flex size-7 items-center justify-center rounded-md text-muted-foreground opacity-0 transition-opacity hover:bg-accent hover:text-accent-foreground group-hover/row:opacity-100 data-popup-open:bg-accent data-popup-open:opacity-100 data-popup-open:text-accent-foreground"
            >
              <MoreHorizontal className="size-4" />
            </button>
          }
        />
        <DropdownMenuContent align="end" className="w-40">
          <DropdownMenuItem
            onClick={() =>
              intentNavigate(
                p.squadDetail(squad.id),
                "foreground-tab",
              )
            }
          >
            <ExternalLink className="size-3.5" />
            {tCommon(($) => $.navigation.open_in_new_tab)}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            onClick={() => setArchiveOpen(true)}
          >
            <Trash2 className="size-3.5" />
            {t(($) => $.page.archive_action)}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ArchiveSquadDialog
        squad={squad}
        open={archiveOpen}
        onOpenChange={setArchiveOpen}
      />
    </span>
  );
}

// ---------------------------------------------------------------------------
// 页头与工具栏
// ---------------------------------------------------------------------------

/** 小队列表的表头：可点击列头排序，并随隐藏列状态省略对应列。 */
function SquadListHeader({
  sortField,
  sortDirection,
  onSort,
  isColVisible,
}: {
  sortField: SquadSortField;
  sortDirection: ListGridSortDirection;
  onSort: (field: SquadSortField) => void;
  isColVisible: (key: SquadColumnKey) => boolean;
}) {
  const { t } = useT("squads");
  const sorted = (field: SquadSortField) =>
    sortField === field ? sortDirection : false;
  return (
    <ListGridHeader>
      <ListGridHeaderCell sorted={sorted("name")} onSort={() => onSort("name")}>
        {t(($) => $.page.table.name)}
      </ListGridHeaderCell>
      <ListGridHeaderCell>{t(($) => $.page.table.leader)}</ListGridHeaderCell>
      {isColVisible("members") ? (
        <ListGridHeaderCell
          className="hidden @2xl:flex"
          sorted={sorted("members")}
          onSort={() => onSort("members")}
        >
          {t(($) => $.page.table.members)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("creator") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.page.table.creator)}
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
      {/* kebab 操作列占位（无操作时该轨道宽度塌缩为 0） */}
      <span aria-hidden="true" />
    </ListGridHeader>
  );
}

const COLUMN_KEYS: SquadColumnKey[] = ["members", "creator", "created"];
const SORT_FIELDS: SquadSortField[] = ["name", "members", "created"];

/** 筛选下拉里可选的 actor（leader/creator）选项。 */
interface ActorOption {
  id: string;
  name: string;
  count: number;
}

/** 小队列表顶部的工具栏：范围切换、筛选、排序与显示列设置。 */
function SquadListToolbar({
  scope,
  onScopeChange,
  scopeCounts,
  filters,
  onToggleFilter,
  onClearFilters,
  leaderOptions,
  creatorOptions,
  visibleCount,
  totalCount,
  sortField,
  sortDirection,
  onSortFieldChange,
  onSortDirectionChange,
  hiddenColumns,
  onToggleColumn,
}: {
  scope: SquadsScope;
  onScopeChange: (scope: SquadsScope) => void;
  scopeCounts: Record<SquadsScope, number>;
  filters: SquadListFilters;
  onToggleFilter: (key: keyof SquadListFilters, value: string) => void;
  onClearFilters: () => void;
  leaderOptions: ActorOption[];
  creatorOptions: ActorOption[];
  visibleCount: number;
  totalCount: number;
  sortField: SquadSortField;
  sortDirection: ListGridSortDirection;
  onSortFieldChange: (field: SquadSortField) => void;
  onSortDirectionChange: (direction: ListGridSortDirection) => void;
  hiddenColumns: SquadColumnKey[];
  onToggleColumn: (key: SquadColumnKey) => void;
}) {
  const { t } = useT("squads");
  const activeFilterCount =
    (filters.leaders.length > 0 ? 1 : 0) +
    (filters.creators.length > 0 ? 1 : 0);
  const hasActiveFilters = activeFilterCount > 0;
  const countBadge = (n: number) => (
    <span className="ml-auto pl-3 text-caption text-muted-foreground">{n}</span>
  );
  const SCOPE_LABELS: Record<SquadsScope, string> = {
    mine: t(($) => $.scope.mine),
    all: t(($) => $.scope.all),
  };
  const SORT_LABELS: Record<SquadSortField, string> = {
    name: t(($) => $.page.table.name),
    members: t(($) => $.page.table.members),
    created: t(($) => $.page.table.created),
  };
  const COLUMN_LABELS: Record<SquadColumnKey, string> = {
    members: t(($) => $.page.table.members),
    creator: t(($) => $.page.table.creator),
    created: t(($) => $.page.table.created),
  };
  const sortLabel = SORT_LABELS[sortField];

  return (
    <div className={PAGE_TOOLBAR}>
      <div className="flex min-w-0 items-center gap-2">
        <div className="hidden shrink-0 items-center gap-1 md:flex">
          {SQUAD_SCOPES.map((s) => (
            <Button
              key={s}
              variant="outline"
              size="sm"
              className={
                scope === s
                  ? "gap-1.5 bg-accent text-accent-foreground hover:bg-accent/80"
                  : "gap-1.5 text-muted-foreground"
              }
              onClick={() => onScopeChange(s)}
            >
              {SCOPE_LABELS[s]}
              <span className="tabular-nums text-caption text-muted-foreground">
                {scopeCounts[s]}
              </span>
            </Button>
          ))}
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="outline"
                size="sm"
                className="shrink-0 gap-1 text-muted-foreground md:hidden"
              >
                <span className="truncate">{SCOPE_LABELS[scope]}</span>
                <ChevronDown className="size-3 text-muted-foreground" />
              </Button>
            }
          />
          <DropdownMenuContent align="start" className="w-auto">
            <DropdownMenuRadioGroup
              value={scope}
              onValueChange={(value) => onScopeChange(value as SquadsScope)}
            >
              {SQUAD_SCOPES.map((s) => (
                <DropdownMenuRadioItem key={s} value={s}>
                  {SCOPE_LABELS[s]}
                  <span className="ml-2 tabular-nums text-caption text-muted-foreground">
                    {scopeCounts[s]}
                  </span>
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>

        {hasActiveFilters && (
          <span
            title={t(($) => $.toolbar.result_count_title)}
            className="hidden shrink-0 text-caption tabular-nums text-muted-foreground md:inline"
          >
            {visibleCount} / {totalCount}
          </span>
        )}
      </div>

      <div className="flex shrink-0 items-center gap-1">
      {/* 筛选 */}
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant={hasActiveFilters ? "default" : "outline"}
              size="sm"
              className={
                hasActiveFilters
                  ? "h-8 w-8 gap-1 bg-brand px-0 text-white hover:bg-brand/90 md:w-auto md:px-2.5"
                  : "h-8 w-8 gap-1 px-0 text-muted-foreground md:w-auto md:px-2.5"
              }
            >
              <Filter className="size-3.5" />
              {hasActiveFilters ? (
                <>
                  <span className="hidden md:inline">
                    {t(($) => $.toolbar.filter_active_count, { count: activeFilterCount })}
                  </span>
                  <span className="tabular-nums md:hidden">{activeFilterCount}</span>
                </>
              ) : (
                <span className="hidden md:inline">{t(($) => $.toolbar.filter_label)}</span>
              )}
              {hasActiveFilters && (
                <span
                  role="button"
                  tabIndex={-1}
                  aria-label={t(($) => $.toolbar.clear_filters)}
                  className="-mr-1 ml-0.5 hidden rounded-sm p-0.5 hover:bg-white/20 md:inline-flex"
                  onClick={(e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    onClearFilters();
                  }}
                  onPointerDown={(e) => e.stopPropagation()}
                >
                  <X className="size-3" />
                </span>
              )}
            </Button>
          }
        />
        <DropdownMenuContent align="end" className="w-auto">
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <span className="flex-1">{t(($) => $.page.table.leader)}</span>
              {filters.leaders.length > 0 && (
                <span className="text-caption font-medium text-primary">{filters.leaders.length}</span>
              )}
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent className="max-h-72 w-auto min-w-48 overflow-y-auto">
              {leaderOptions.map((o) => (
                <DropdownMenuCheckboxItem
                  key={o.id}
                  checked={filters.leaders.includes(o.id)}
                  onCheckedChange={() => onToggleFilter("leaders", o.id)}
                  className={FILTER_ITEM_CLASS}
                >
                  <HoverCheck checked={filters.leaders.includes(o.id)} />
                  <ActorAvatar actorType="agent" actorId={o.id} size="sm" />
                  <span className="min-w-0 truncate">{o.name}</span>
                  {countBadge(o.count)}
                </DropdownMenuCheckboxItem>
              ))}
            </DropdownMenuSubContent>
          </DropdownMenuSub>
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <span className="flex-1">{t(($) => $.page.table.creator)}</span>
              {filters.creators.length > 0 && (
                <span className="text-caption font-medium text-primary">{filters.creators.length}</span>
              )}
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent className="max-h-72 w-auto min-w-48 overflow-y-auto">
              {creatorOptions.map((o) => (
                <DropdownMenuCheckboxItem
                  key={o.id}
                  checked={filters.creators.includes(o.id)}
                  onCheckedChange={() => onToggleFilter("creators", o.id)}
                  className={FILTER_ITEM_CLASS}
                >
                  <HoverCheck checked={filters.creators.includes(o.id)} />
                  <ActorAvatar actorType="member" actorId={o.id} size="sm" />
                  <span className="min-w-0 truncate">{o.name}</span>
                  {countBadge(o.count)}
                </DropdownMenuCheckboxItem>
              ))}
            </DropdownMenuSubContent>
          </DropdownMenuSub>
        </DropdownMenuContent>
      </DropdownMenu>

      {/* 显示设置（排序与显示列） */}
      <Popover>
        <Tooltip>
          <PopoverTrigger
            render={
              <TooltipTrigger
                render={
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-8 w-8 gap-1 px-0 text-muted-foreground md:w-auto md:px-2.5"
                  >
                    {sortDirection === "asc" ? (
                      <ArrowUp className="size-3.5" />
                    ) : (
                      <ArrowDown className="size-3.5" />
                    )}
                    <span className="hidden md:inline">{sortLabel}</span>
                  </Button>
                }
              />
            }
          />
          <TooltipContent side="bottom">
            {t(($) => $.toolbar.display)}
          </TooltipContent>
        </Tooltip>
        <PopoverContent align="end" className="w-64 p-0">
          <div className="border-b px-3 py-2.5">
            <span className="text-caption font-medium text-muted-foreground">
              {t(($) => $.toolbar.sort_by)}
            </span>
            <div className="mt-2 flex items-center gap-1.5">
              <DropdownMenu>
                <DropdownMenuTrigger
                  render={
                    <Button
                      variant="outline"
                      size="sm"
                      className="flex-1 justify-between text-caption"
                    >
                      {sortLabel}
                      <ChevronDown className="size-3 text-muted-foreground" />
                    </Button>
                  }
                />
                <DropdownMenuContent align="start" className="w-auto">
                  <DropdownMenuRadioGroup
                    value={sortField}
                    onValueChange={(v) =>
                      onSortFieldChange(v as SquadSortField)
                    }
                  >
                    {SORT_FIELDS.map((field) => (
                      <DropdownMenuRadioItem key={field} value={field}>
                        {SORT_LABELS[field]}
                      </DropdownMenuRadioItem>
                    ))}
                  </DropdownMenuRadioGroup>
                </DropdownMenuContent>
              </DropdownMenu>
              <Button
                variant="outline"
                size="icon-sm"
                onClick={() =>
                  onSortDirectionChange(
                    sortDirection === "asc" ? "desc" : "asc",
                  )
                }
                title={
                  sortDirection === "asc"
                    ? t(($) => $.toolbar.direction_asc)
                    : t(($) => $.toolbar.direction_desc)
                }
              >
                {sortDirection === "asc" ? (
                  <ArrowUp className="size-3.5" />
                ) : (
                  <ArrowDown className="size-3.5" />
                )}
              </Button>
            </div>
          </div>
          <div className="px-3 py-2.5">
            <span className="text-caption font-medium text-muted-foreground">
              {t(($) => $.toolbar.section_columns)}
            </span>
            <div className="mt-2 space-y-2">
              {COLUMN_KEYS.map((key) => (
                <label
                  key={key}
                  className="flex cursor-pointer items-center justify-between"
                >
                  <span className="text-body">{COLUMN_LABELS[key]}</span>
                  <Switch
                    size="sm"
                    checked={!hiddenColumns.includes(key)}
                    onCheckedChange={() => onToggleColumn(key)}
                  />
                </label>
              ))}
            </div>
          </div>
        </PopoverContent>
      </Popover>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 页面主体
// ---------------------------------------------------------------------------

/**
 * 小队列表页主组件。
 * 汇总小队、agent 与成员查询，把范围筛选、排序、过滤后的行渲染成网格；
 * 空/加载状态分别展示对应占位。
 */
export function SquadsPage() {
  const { t } = useT("squads");
  const locale = useLocale();
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const p = useWorkspacePaths();
  const rowLink = useRowLink();
  const currentUser = useAuthStore((s) => s.user);

  const { data: squads = [], isLoading } = useQuery({
    ...squadListOptions(wsId),
    enabled: !!wsId,
  });
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));

  const agentsById = useMemo(() => {
    const m = new Map<string, Agent>();
    for (const a of agents) m.set(a.id, a);
    return m;
  }, [agents]);

  const membersById = useMemo(() => {
    const m = new Map<string, MemberWithUser>();
    for (const mem of members) m.set(mem.user_id, mem);
    return m;
  }, [members]);

  const isWorkspaceAdmin = useMemo(() => {
    if (!currentUser) return false;
    const me = members.find((mem: MemberWithUser) => mem.user_id === currentUser.id);
    return me?.role === "owner" || me?.role === "admin";
  }, [members, currentUser]);

  const scope = useSquadsViewStore((s) => s.scope);
  const setScope = useSquadsViewStore((s) => s.setScope);
  const sortField = useSquadsViewStore((s) => s.sortField);
  const sortDirection = useSquadsViewStore((s) => s.sortDirection);
  const hiddenColumns = useSquadsViewStore((s) => s.hiddenColumns);
  const handleSort = useSquadsViewStore((s) => s.toggleSort);
  const handleSortFieldSelect = useSquadsViewStore((s) => s.setSortField);
  const setSortDirection = useSquadsViewStore((s) => s.setSortDirection);
  const toggleColumn = useSquadsViewStore((s) => s.toggleColumn);
  const filters = useSquadsViewStore((s) => s.filters);
  const toggleFilter = useSquadsViewStore((s) => s.toggleFilter);
  const clearFilters = useSquadsViewStore((s) => s.clearFilters);

  const isColVisible = (key: SquadColumnKey) => !hiddenColumns.includes(key);

  const scopeCounts = useMemo<Record<SquadsScope, number>>(() => {
    let mine = 0;
    if (currentUser) {
      for (const s of squads) if (s.creator_id === currentUser.id) mine++;
    }
    return { mine, all: squads.length };
  }, [squads, currentUser]);

  // 当前范围内、未加过滤的行——筛选项列表与「n / 总数」的分母都由它推导。
  const scopeRows = useMemo<Squad[]>(() => {
    return squads.filter((s) => {
      if (scope === "mine") {
        return !!currentUser && s.creator_id === currentUser.id;
      }
      return true;
    });
  }, [squads, scope, currentUser]);

  const leaderOptions = useMemo(() => {
    const m = new Map<string, { id: string; name: string; count: number }>();
    for (const s of scopeRows) {
      const e = m.get(s.leader_id);
      if (e) e.count += 1;
      else
        m.set(s.leader_id, {
          id: s.leader_id,
          name: agentsById.get(s.leader_id)?.name ?? s.leader_id.slice(0, 8),
          count: 1,
        });
    }
    return [...m.values()];
  }, [scopeRows, agentsById]);

  const creatorOptions = useMemo(() => {
    const m = new Map<string, { id: string; name: string; count: number }>();
    for (const s of scopeRows) {
      const e = m.get(s.creator_id);
      if (e) e.count += 1;
      else
        m.set(s.creator_id, {
          id: s.creator_id,
          name: membersById.get(s.creator_id)?.name ?? s.creator_id.slice(0, 8),
          count: 1,
        });
    }
    return [...m.values()];
  }, [scopeRows, membersById]);

  const rows = useMemo<Squad[]>(() => {
    const inScope = scopeRows.filter((s) => {
      if (filters.leaders.length > 0 && !filters.leaders.includes(s.leader_id)) {
        return false;
      }
      if (
        filters.creators.length > 0 &&
        !filters.creators.includes(s.creator_id)
      ) {
        return false;
      }
      return true;
    });
    const dir = sortDirection === "asc" ? 1 : -1;
    const sorted = [...inScope];
    sorted.sort((a, b) => {
      if (sortField === "members") {
        const av = a.member_count ?? a.member_preview?.length ?? 0;
        const bv = b.member_count ?? b.member_preview?.length ?? 0;
        return (av - bv) * dir || a.name.localeCompare(b.name);
      }
      if (sortField === "created") {
        return (
          (Date.parse(a.created_at) - Date.parse(b.created_at)) * dir
        );
      }
      return a.name.localeCompare(b.name) * dir;
    });
    return sorted;
  }, [scopeRows, filters, sortField, sortDirection]);

  // 当当前用户能管理至少一个小队时保留行操作（kebab）轨道。工作区管理员
  // 能管理全部小队；普通成员只能管理自己创建的小队（MUL-4223）。
  const canManageAnyRow = useMemo(
    () =>
      isWorkspaceAdmin ||
      (!!currentUser && rows.some((s) => s.creator_id === currentUser.id)),
    [isWorkspaceAdmin, rows, currentUser],
  );

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <CollectionPageHeader
        icon={Users}
        title={t(($) => $.page.title)}
        count={squads.length}
        actions={
          <CollectionPageHeaderAction
            icon={Plus}
            label={t(($) => $.page.new_button)}
            onClick={() => useModalStore.getState().open("create-squad")}
          />
        }
      />

      {isLoading ? (
        <LoadingSkeleton />
      ) : squads.length === 0 ? (
        <CollectionPageState
          icon={Users}
          title={t(($) => $.page.empty_no_squads)}
          actions={
            <Button
              size="sm"
              onClick={() => useModalStore.getState().open("create-squad")}
            >
              <Plus aria-hidden="true" className="size-3.5" />
              {t(($) => $.page.new_button)}
            </Button>
          }
        />
      ) : (
        <>
          <SquadListToolbar
            scope={scope}
            onScopeChange={setScope}
            scopeCounts={scopeCounts}
            filters={filters}
            onToggleFilter={toggleFilter}
            onClearFilters={clearFilters}
            leaderOptions={leaderOptions}
            creatorOptions={creatorOptions}
            visibleCount={rows.length}
            totalCount={scopeRows.length}
            sortField={sortField}
            sortDirection={sortDirection}
            onSortFieldChange={handleSortFieldSelect}
            onSortDirectionChange={setSortDirection}
            hiddenColumns={hiddenColumns}
            onToggleColumn={toggleColumn}
          />
          <div className="min-h-0 flex-1 overflow-auto @container">
            <ListGrid
              className={`${GRID_COLS} @2xl:min-w-[var(--sqc-minw)]`}
              style={{
                ...columnTrackVars(isColVisible, canManageAnyRow),
                paddingBottom: LIST_GRID_BOTTOM_CLEARANCE,
              }}
            >
              <SquadListHeader
                sortField={sortField}
                sortDirection={sortDirection}
                onSort={handleSort}
                isColVisible={isColVisible}
              />
              {rows.length === 0 ? (
                <div className="col-span-full py-16 text-center text-body text-muted-foreground">
                  {t(($) => $.page.no_matches)}
                </div>
              ) : (
                rows.map((squad) => (
                  <ListGridRow
                    key={squad.id}
                    className="cursor-pointer"
                    {...rowLink(p.squadDetail(squad.id))}
                  >
                    <NameCell squad={squad} />
                    <LeaderCell
                      leaderId={squad.leader_id}
                      leader={agentsById.get(squad.leader_id)}
                    />
                    {isColVisible("members") ? (
                      <MembersCell squad={squad} />
                    ) : (
                      <ListGridCell className="hidden px-0 @2xl:flex" />
                    )}
                    {isColVisible("creator") ? (
                      <ListGridCell className="hidden gap-1.5 @2xl:flex">
                        <ActorAvatar
                          actorType="member"
                          actorId={squad.creator_id}
                          size="sm"
                        />
                        <span className="min-w-0 truncate text-caption text-muted-foreground">
                          {membersById.get(squad.creator_id)?.name ??
                            squad.creator_id.slice(0, 8)}
                        </span>
                      </ListGridCell>
                    ) : (
                      <ListGridCell className="hidden px-0 @2xl:flex" />
                    )}
                    {isColVisible("created") ? (
                      <ListGridCell className="hidden whitespace-nowrap text-caption tabular-nums text-muted-foreground @2xl:flex">
                        {new Date(squad.created_at).toLocaleDateString(locale)}
                      </ListGridCell>
                    ) : (
                      <ListGridCell className="hidden px-0 @2xl:flex" />
                    )}
                    <ListGridCell className="justify-end px-0">
                      {isWorkspaceAdmin ||
                      (!!currentUser && squad.creator_id === currentUser.id) ? (
                        <SquadRowActions squad={squad} />
                      ) : null}
                    </ListGridCell>
                  </ListGridRow>
                ))
              )}
            </ListGrid>
          </div>
        </>
      )}
    </div>
  );
}

/** 加载中的骨架屏：先按默认列渲染几行灰色占位。 */
function LoadingSkeleton() {
  return (
    <div className="min-h-0 flex-1 overflow-auto @container">
      <ListGrid
        className={GRID_COLS}
        style={columnTrackVars(
          (key) => !SQUAD_DEFAULT_HIDDEN_COLUMNS.includes(key),
          true,
        )}
      >
        <ListGridHeader>
          <ListGridHeaderCell>
            <Skeleton className="h-3 w-12" />
          </ListGridHeaderCell>
          <ListGridHeaderCell>
            <Skeleton className="h-3 w-12" />
          </ListGridHeaderCell>
          <ListGridHeaderCell className="hidden @2xl:flex">
            <Skeleton className="h-3 w-12" />
          </ListGridHeaderCell>
          <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
          <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
          <span aria-hidden="true" />
        </ListGridHeader>
        {Array.from({ length: 4 }).map((_, i) => (
          <ListGridRow key={i} className="h-16 hover:bg-transparent">
            <ListGridCell className="gap-3">
              <Skeleton className="size-8 rounded-full" />
              <div className="min-w-0 flex-1 space-y-1.5">
                <Skeleton className="h-3.5 w-32 max-w-full" />
                <Skeleton className="h-3 w-48 max-w-full" />
              </div>
            </ListGridCell>
            <ListGridCell className="gap-1.5">
              <Skeleton className="size-5 rounded-full" />
              <Skeleton className="h-3 w-16" />
            </ListGridCell>
            <ListGridCell className="hidden @2xl:flex">
              <Skeleton className="h-5 w-16" />
            </ListGridCell>
            <ListGridCell className="hidden px-0 @2xl:flex" />
            <ListGridCell className="hidden px-0 @2xl:flex" />
            <span aria-hidden="true" />
          </ListGridRow>
        ))}
      </ListGrid>
    </div>
  );
}
