"use client";

import { useCallback, useMemo, useState, type MouseEvent } from "react";
import {
  ArrowDown,
  ArrowUp,
  Ban,
  ChevronDown,
  CheckCircle2,
  Filter,
  FolderGit,
  FolderKanban,
  Info,
  LayoutGrid,
  MoreHorizontal,
  Pin,
  PinOff,
  Plus,
  Power,
  RefreshCw,
  Rows3,
  Search,
  Trash2,
  X,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useQueries } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  projectListOptions,
  projectResourcesOptions,
  useUpdateProject,
  useDeleteProject,
  useDeleteProjectResource,
  useDisableProjectResource,
  useEnableProjectResource,
  useSyncProjectResource,
  useTestProjectResource,
  useProjectViewStore,
  type ProjectColumnKey,
  type ProjectListFilters,
  type ProjectSortField,
} from "@multica/core/projects";
import {
  pinListOptions,
  useCreatePin,
  useDeletePin,
} from "@multica/core/pins";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useAuthStore } from "@multica/core/auth";
import { useActorName } from "@multica/core/workspace/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useModalStore } from "@multica/core/modals";
import { AppLink, useRowLink } from "../../navigation";
import { ActorAvatar } from "../../common/actor-avatar";
import { FILTER_ITEM_CLASS, HoverCheck } from "../../common/hover-check";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
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
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import type {
  MemberWithUser,
  GongfengRepoResourceRef,
  Project,
  ProjectPriority,
  ProjectResource,
  ProjectStatus,
  UpdateProjectRequest,
} from "@multica/core/types";
import { PageHeader } from "../../layout/page-header";
import { ProjectIcon } from "./project-icon";
import { useT } from "../../i18n";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { useFormatRelativeDate } from "./labels";
import { ProjectStatusBadge, ProjectPriorityBadge } from "./project-badge";
import { ProjectLeadPicker } from "./project-lead-picker";

// Sort order maps for the enum columns (header sort needs a total order).
const PRIORITY_ORDER: Record<ProjectPriority, number> = {
  urgent: 4,
  high: 3,
  medium: 2,
  low: 1,
  none: 0,
};
const STATUS_ORDER: Record<ProjectStatus, number> = {
  planned: 0,
  in_progress: 1,
  paused: 2,
  completed: 3,
  cancelled: 4,
};

const progressOf = (p: Project) =>
  p.issue_count > 0 ? p.done_count / p.issue_count : -1;

// Composite "type:id" lead value so the string[] filter holds member/agent
// refs alike.
function leadFilterValue(p: Project): string | null {
  return p.lead_type && p.lead_id ? `${p.lead_type}:${p.lead_id}` : null;
}

// ---------------------------------------------------------------------------
// Table (compact) view — ListGrid. Name + status are the core columns;
// priority/progress/lead/issues/created collapse below @2xl, with min-width
// + the wrapper's overflow as the escape valve. Rows use whole-row mouse
// navigation; inline controls stop propagation so edit/menu clicks stay local.
// ---------------------------------------------------------------------------

const COLUMN_WIDTHS: Record<ProjectColumnKey, number> = {
  priority: 116,
  progress: 88,
  lead: 132,
  issues: 80,
  created: 104,
};

// Fixed tracks: edges 12+12, checkbox 16, name min 200, status 116,
// kebab 28 = 384, plus the 10 gap-x-3 gaps between the wide template's
// 11 tracks.
const FIXED_TRACKS_WIDTH = 384 + 10 * 12;

// Render/track order: checkbox, name, status (core, fixed 116px), priority,
// progress, lead, issues, created, kebab. MUST be a literal string —
// Tailwind can't see interpolated `grid-cols-[...]` arbitrary values, so an
// interpolated width silently drops the whole template and the grid
// collapses to one column.
const GRID_COLS =
  "grid-cols-[0.75rem_1rem_minmax(120px,1fr)_116px_1.75rem_0.75rem] " +
  "@2xl:grid-cols-[0.75rem_1rem_minmax(200px,1fr)_116px_var(--pjc-priority)_var(--pjc-progress)_var(--pjc-lead)_var(--pjc-issues)_var(--pjc-created)_1.75rem_0.75rem]";

const stopRowNavigation = (e: MouseEvent) => e.stopPropagation();

function columnTrackVars(
  isVisible: (key: ProjectColumnKey) => boolean,
): React.CSSProperties {
  const width = (key: ProjectColumnKey) =>
    isVisible(key) ? `${COLUMN_WIDTHS[key]}px` : "0px";
  const minWidth =
    FIXED_TRACKS_WIDTH +
    (Object.keys(COLUMN_WIDTHS) as ProjectColumnKey[]).reduce(
      (sum, key) => sum + (isVisible(key) ? COLUMN_WIDTHS[key] : 0),
      0,
    );
  return {
    "--pjc-priority": width("priority"),
    "--pjc-progress": width("progress"),
    "--pjc-lead": width("lead"),
    "--pjc-issues": width("issues"),
    "--pjc-created": width("created"),
    "--pjc-minw": `${minWidth}px`,
  } as React.CSSProperties;
}

function ProgressRing({ project }: { project: Project }) {
  if (project.issue_count === 0) {
    return <span className="text-xs text-muted-foreground/40">—</span>;
  }
  const pct = Math.round((project.done_count / project.issue_count) * 100);
  return (
    <span className="flex items-center gap-1.5">
      <span className="relative h-3.5 w-3.5">
        <svg className="h-3.5 w-3.5 -rotate-90" viewBox="0 0 16 16">
          <circle className="text-muted" strokeWidth="2" stroke="currentColor" fill="none" r="6" cx="8" cy="8" />
          <circle
            className="text-emerald-500"
            strokeWidth="2"
            stroke="currentColor"
            fill="none"
            r="6"
            cx="8"
            cy="8"
            strokeDasharray={`${pct * 0.377} 37.7`}
            strokeLinecap="round"
          />
        </svg>
      </span>
      <span className="text-xs tabular-nums text-muted-foreground">
        {project.done_count}/{project.issue_count}
      </span>
    </span>
  );
}

function isGongfengResource(resource: ProjectResource): resource is ProjectResource & {
  resource_ref: GongfengRepoResourceRef;
} {
  return resource.resource_type === "gongfeng_repo";
}

type GongfengResourceRow = ProjectResource & {
  resource_ref: GongfengRepoResourceRef;
  project: Project;
};

function GongfengRepositoryInventory({ projects }: { projects: Project[] }) {
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const queries = useQueries({
    queries: projects.map((project) => projectResourcesOptions(wsId, project.id)),
  });
  const rows = useMemo<GongfengResourceRow[]>(() => {
    return queries.flatMap((query, idx) => {
      const project = projects[idx];
      if (!project) return [];
      return (query.data ?? [])
        .filter(isGongfengResource)
        .map((resource) => ({ ...resource, project }));
    });
  }, [projects, queries]);

  if (rows.length === 0) return null;

  return (
    <section className="border-y bg-muted/20 px-5 py-2" data-testid="gongfeng-repository-inventory">
      <div className="mb-2 flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          <FolderGit className="size-3.5 text-muted-foreground" />
          <h2 className="text-xs font-medium">工蜂仓库</h2>
          <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
            {rows.length}
          </span>
        </div>
      </div>
      <div className="overflow-x-auto">
        <div className="min-w-[760px] divide-y rounded-md border bg-background">
          {rows.map((row) => (
            <GongfengRepositoryInventoryRow
              key={row.id}
              row={row}
              projectHref={wsPaths.projectDetail(row.project.id)}
            />
          ))}
        </div>
      </div>
    </section>
  );
}

function GongfengRepositoryInventoryRow({
  row,
  projectHref,
}: {
  row: GongfengResourceRow;
  projectHref: string;
}) {
  const wsId = useWorkspaceId();
  const deleteResource = useDeleteProjectResource(wsId, row.project.id);
  const testResource = useTestProjectResource(wsId, row.project.id);
  const syncResource = useSyncProjectResource(wsId, row.project.id);
  const disableResource = useDisableProjectResource(wsId, row.project.id);
  const enableResource = useEnableProjectResource(wsId, row.project.id);
  const [detailOpen, setDetailOpen] = useState(false);
  const ref = row.resource_ref;
  const branch = ref.branch || ref.ref || "";
  const commit = ref.commit_sha || ref.head_commit || "";
  const title = row.label || ref.title || ref.project_path || ref.url;
  const statusItems = [
    { label: "连接", value: ref.connection_status },
    { label: "同步", value: ref.sync_status },
    { label: "测试", value: ref.test_status },
  ].filter((item): item is { label: string; value: string } => Boolean(item.value));

  return (
    <>
      <div
        className="grid grid-cols-[minmax(220px,1.3fr)_minmax(120px,0.8fr)_minmax(180px,1fr)_minmax(120px,0.7fr)_10rem] items-center gap-3 px-3 py-2 text-xs"
        data-testid="gongfeng-inventory-row"
      >
        <div className="min-w-0">
          <a
            href={ref.url}
            target="_blank"
            rel="noopener noreferrer"
            className="block truncate font-medium hover:underline"
            data-testid="gongfeng-inventory-repo"
          >
            {title}
          </a>
          <div className="mt-0.5 truncate font-mono text-[11px] text-muted-foreground">
            {ref.project_path}
          </div>
        </div>
        <AppLink href={projectHref} className="min-w-0 truncate text-muted-foreground hover:text-foreground hover:underline">
          {row.project.title}
        </AppLink>
        <div className="flex min-w-0 flex-wrap gap-1">
          {branch && <span className="rounded border px-1 font-mono text-[11px]">{branch}</span>}
          {commit && <span className="rounded border px-1 font-mono text-[11px]">{shortCommit(commit)}</span>}
          <span className="rounded border px-1 text-[11px]">{ref.resource_kind}</span>
        </div>
        <div className="flex min-w-0 flex-wrap gap-1">
          {statusItems.length === 0 ? (
            <span className="text-[11px] text-muted-foreground">状态未采集</span>
          ) : (
            statusItems.map((item) => (
              <span key={item.label} className="rounded border px-1 text-[11px]" data-testid="gongfeng-inventory-status">
                {item.label}: {statusLabel(item.value)}
              </span>
            ))
          )}
        </div>
        <div className="flex items-center justify-end gap-1">
          <button
            type="button"
            className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
            aria-label="查看仓库详情"
            title="查看仓库详情"
            onClick={() => setDetailOpen(true)}
          >
            <Info className="size-3.5" />
          </button>
          <button
            type="button"
            className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-40"
            aria-label="测试连接"
            title="测试连接"
            disabled={testResource.isPending || Boolean(ref.disabled)}
            onClick={() => {
              testResource.mutate(row.id, {
                onSuccess: () => toast.success("工蜂仓库连接测试已更新"),
                onError: (err) => toast.error(err instanceof Error ? err.message : "连接测试失败"),
              });
            }}
          >
            <CheckCircle2 className="size-3.5" />
          </button>
          <button
            type="button"
            className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-40"
            aria-label="刷新同步状态"
            title="刷新同步状态"
            disabled={syncResource.isPending || Boolean(ref.disabled)}
            onClick={() => {
              syncResource.mutate(row.id, {
                onSuccess: () => toast.success("工蜂仓库同步状态已更新"),
                onError: (err) => toast.error(err instanceof Error ? err.message : "同步失败"),
              });
            }}
          >
            <RefreshCw className="size-3.5" />
          </button>
          {ref.disabled ? (
            <button
              type="button"
              className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-40"
              aria-label="启用仓库"
              title="启用仓库"
              disabled={enableResource.isPending}
              onClick={() => {
                enableResource.mutate(row.id, {
                  onSuccess: () => toast.success("已启用工蜂仓库"),
                  onError: (err) => toast.error(err instanceof Error ? err.message : "启用失败"),
                });
              }}
            >
              <Power className="size-3.5" />
            </button>
          ) : (
            <button
              type="button"
              className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground disabled:opacity-40"
              aria-label="禁用仓库"
              title="禁用仓库"
              disabled={disableResource.isPending}
              onClick={() => {
                disableResource.mutate(row.id, {
                  onSuccess: () => toast.success("已禁用工蜂仓库"),
                  onError: (err) => toast.error(err instanceof Error ? err.message : "禁用失败"),
                });
              }}
            >
              <Ban className="size-3.5" />
            </button>
          )}
          <button
            type="button"
            className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-destructive"
            aria-label="删除仓库关联"
            title="删除仓库关联"
            onClick={() => {
              deleteResource.mutate(row.id, {
                onSuccess: () => toast.success("已删除工蜂仓库关联"),
                onError: (err) => toast.error(err instanceof Error ? err.message : "删除失败"),
              });
            }}
          >
            <Trash2 className="size-3.5" />
          </button>
        </div>
      </div>
      <Dialog open={detailOpen} onOpenChange={setDetailOpen}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>工蜂仓库详情</DialogTitle>
            <DialogDescription>
              {title}
            </DialogDescription>
          </DialogHeader>
          <GongfengRepositoryDetail row={row} />
        </DialogContent>
      </Dialog>
    </>
  );
}

function GongfengRepositoryDetail({ row }: { row: GongfengResourceRow }) {
  const ref = row.resource_ref;
  const items = [
    ["Provider", ref.provider],
    ["Project", row.project.title],
    ["Project path", ref.project_path],
    ["URL", ref.url],
    ["Kind", ref.resource_kind],
    ["Branch/ref", ref.branch || ref.ref || "未设置"],
    ["Commit", ref.commit_sha || ref.head_commit || "未采集"],
    ["Connection", statusLabel(ref.connection_status || "pending_verification")],
    ["Sync", statusLabel(ref.sync_status || "pending_verification")],
    ["Test", statusLabel(ref.test_status || "pending_verification")],
    ["Last tested", ref.last_tested_at || "未测试"],
    ["Last synced", ref.last_synced_at || "未同步"],
    ["Training", ref.disabled ? "不可选" : "可选"],
  ];
  return (
    <div className="space-y-2 text-xs" data-testid="gongfeng-repository-detail">
      {items.map(([label, value]) => (
        <div key={label} className="grid grid-cols-[7rem_minmax(0,1fr)] gap-3 border-b py-1.5 last:border-b-0">
          <div className="text-muted-foreground">{label}</div>
          <div className="min-w-0 break-words font-mono">{value}</div>
        </div>
      ))}
    </div>
  );
}

function shortCommit(value: string): string {
  return value.length > 12 ? value.slice(0, 12) : value;
}

function statusLabel(value: string): string {
  switch (value) {
    case "ok":
    case "passed":
    case "connected":
    case "synced":
      return "通过";
    case "reachable":
      return "可达";
    case "auth_required":
      return "需凭据";
    case "unreachable":
      return "不可达";
    case "invalid_url":
      return "地址无效";
    case "failed":
    case "error":
      return "失败";
    case "disabled":
      return "已停用";
    case "pending_verification":
      return "待验证";
    case "seeded_for_remediation":
      return "已建档";
    case "requires_real_click_acceptance":
      return "待 UI 验收";
    default:
      return value.replace(/_/g, " ");
  }
}

// Compact rows own whole-row navigation; callers stop propagation around this
// menu so action clicks do not bubble into the rowLink handler.
function ProjectRowActions({
  project,
  pinned,
  canDelete,
}: {
  project: Project;
  pinned: boolean;
  canDelete: boolean;
}) {
  const { t } = useT("projects");
  const createPin = useCreatePin();
  const deletePin = useDeletePin();
  const deleteProject = useDeleteProject();
  const [deleteOpen, setDeleteOpen] = useState(false);

  const togglePin = () => {
    if (pinned) deletePin.mutate({ itemType: "project", itemId: project.id });
    else createPin.mutate({ item_type: "project", item_id: project.id });
  };

  return (
    <>
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
        <DropdownMenuContent align="end" className="w-44">
          <DropdownMenuItem onClick={togglePin}>
            {pinned ? (
              <PinOff className="size-3.5" />
            ) : (
              <Pin className="size-3.5" />
            )}
            {pinned ? t(($) => $.page.unpin) : t(($) => $.page.pin)}
          </DropdownMenuItem>
          {canDelete && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive"
                onClick={() => setDeleteOpen(true)}
              >
                <Trash2 className="size-3.5" />
                {t(($) => $.page.delete)}
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t(($) => $.delete_dialog.title)}</DialogTitle>
            <DialogDescription>
              {t(($) => $.delete_dialog.description)}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => setDeleteOpen(false)}
            >
              {t(($) => $.delete_dialog.cancel)}
            </Button>
            <Button
              type="button"
              variant="destructive"
              size="sm"
              onClick={() => {
                deleteProject.mutate(project.id, {
                  onError: (err) =>
                    toast.error(
                      err instanceof Error ? err.message : String(err),
                    ),
                });
                setDeleteOpen(false);
              }}
            >
              {t(($) => $.delete_dialog.confirm)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

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
          stopRowNavigation(e);
          onToggle();
        }}
        onAuxClick={stopRowNavigation}
        className={`-m-1.5 flex items-center p-1.5 ${
          checked ? "" : "opacity-0 transition-opacity group-hover/row:opacity-100"
        }`}
      >
        <Checkbox checked={checked} tabIndex={-1} className="pointer-events-none" />
      </button>
    </ListGridCell>
  );
}

function ProjectTableRow({
  project,
  pinned,
  canDelete,
  isColVisible,
  selected,
  onToggleSelect,
  rowHref,
  rowLink,
}: {
  project: Project;
  pinned: boolean;
  canDelete: boolean;
  isColVisible: (key: ProjectColumnKey) => boolean;
  selected: boolean;
  onToggleSelect: () => void;
  rowHref: string;
  rowLink: ReturnType<typeof useRowLink>;
}) {
  const formatRelativeDate = useFormatRelativeDate();
  const updateProject = useUpdateProject();
  const handleUpdate = useCallback(
    (data: UpdateProjectRequest) => updateProject.mutate({ id: project.id, ...data }),
    [project.id, updateProject],
  );

  return (
    <ListGridRow
      className={`h-11 cursor-pointer ${selected ? "bg-accent/30" : ""}`}
      {...rowLink(rowHref)}
    >
      <CheckboxCell checked={selected} onToggle={onToggleSelect} />
      <ListGridCell className="gap-2">
        <ProjectIcon project={project} size="sm" />
        <span className="min-w-0 truncate text-sm font-medium">
          {project.title}
        </span>
      </ListGridCell>

      {/* status — core column, always visible */}
      <ListGridCell onClick={stopRowNavigation} onAuxClick={stopRowNavigation}>
        <ProjectStatusBadge project={project} handleUpdate={handleUpdate} align="start" />
      </ListGridCell>

      {isColVisible("priority") ? (
        <ListGridCell className="hidden @2xl:flex" onClick={stopRowNavigation} onAuxClick={stopRowNavigation}>
          <ProjectPriorityBadge project={project} handleUpdate={handleUpdate} align="start" />
        </ListGridCell>
      ) : (
        <ListGridCell className="hidden px-0 @2xl:flex" />
      )}

      {isColVisible("progress") ? (
        <ListGridCell className="hidden @2xl:flex">
          <ProgressRing project={project} />
        </ListGridCell>
      ) : (
        <ListGridCell className="hidden px-0 @2xl:flex" />
      )}

      {isColVisible("lead") ? (
        <ListGridCell className="hidden @2xl:flex" onClick={stopRowNavigation} onAuxClick={stopRowNavigation}>
          <ProjectLeadPicker
            project={project}
            handleUpdate={handleUpdate}
            align="start"
            renderTrigger={(leadName) => (
              <button
                type="button"
                className="flex min-w-0 items-center gap-1.5 rounded px-1 py-0.5 transition-colors hover:bg-accent/60"
              >
                {project.lead_type && project.lead_id ? (
                  <ActorAvatar actorType={project.lead_type} actorId={project.lead_id} size={18} enableHoverCard />
                ) : (
                  <span className="inline-flex h-[18px] w-[18px] rounded-full border border-dashed border-muted-foreground/30" />
                )}
                <span className="min-w-0 truncate text-xs text-muted-foreground">
                  {leadName ?? "—"}
                </span>
              </button>
            )}
          />
        </ListGridCell>
      ) : (
        <ListGridCell className="hidden px-0 @2xl:flex" />
      )}

      {isColVisible("issues") ? (
        <ListGridCell className="hidden justify-end font-mono text-xs tabular-nums text-muted-foreground @2xl:flex">
          {project.issue_count}
        </ListGridCell>
      ) : (
        <ListGridCell className="hidden px-0 @2xl:flex" />
      )}

      {isColVisible("created") ? (
        <ListGridCell className="hidden whitespace-nowrap text-xs tabular-nums text-muted-foreground @2xl:flex">
          {formatRelativeDate(project.created_at)}
        </ListGridCell>
      ) : (
        <ListGridCell className="hidden px-0 @2xl:flex" />
      )}

      <ListGridCell className="justify-end px-0">
        <span onClick={stopRowNavigation} onAuxClick={stopRowNavigation} className="flex items-center">
          <ProjectRowActions project={project} pinned={pinned} canDelete={canDelete} />
        </span>
      </ListGridCell>
    </ListGridRow>
  );
}

function ProjectTableHeader({
  sortField,
  sortDirection,
  onSort,
  isColVisible,
  allSelected,
  someSelected,
  onToggleAll,
}: {
  sortField: ProjectSortField;
  sortDirection: ListGridSortDirection;
  onSort: (field: ProjectSortField) => void;
  isColVisible: (key: ProjectColumnKey) => boolean;
  allSelected: boolean;
  someSelected: boolean;
  onToggleAll: () => void;
}) {
  const { t } = useT("projects");
  const sorted = (field: ProjectSortField) =>
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
            anySelected ? "" : "opacity-0 transition-opacity group-hover/header:opacity-100"
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
      <ListGridHeaderCell sorted={sorted("status")} onSort={() => onSort("status")}>
        {t(($) => $.table.status)}
      </ListGridHeaderCell>
      {isColVisible("priority") ? (
        <ListGridHeaderCell
          className="hidden @2xl:flex"
          sorted={sorted("priority")}
          onSort={() => onSort("priority")}
        >
          {t(($) => $.table.priority)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("progress") ? (
        <ListGridHeaderCell
          className="hidden @2xl:flex"
          sorted={sorted("progress")}
          onSort={() => onSort("progress")}
        >
          {t(($) => $.table.progress)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("lead") ? (
        <ListGridHeaderCell className="hidden @2xl:flex">
          {t(($) => $.table.lead)}
        </ListGridHeaderCell>
      ) : (
        <ListGridHeaderCell className="hidden px-0 @2xl:flex" />
      )}
      {isColVisible("issues") ? (
        <ListGridHeaderCell className="hidden justify-end @2xl:flex" align="right">
          {t(($) => $.table.issues)}
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

// ---------------------------------------------------------------------------
// Card (comfortable) view — kept from the prior page.
// ---------------------------------------------------------------------------

function ProjectCard({
  project,
  pinned,
  canDelete,
}: {
  project: Project;
  pinned: boolean;
  canDelete: boolean;
}) {
  const { t } = useT("projects");
  const wsPaths = useWorkspacePaths();
  const formatRelativeDate = useFormatRelativeDate();
  const updateProject = useUpdateProject();
  const handleUpdate = useCallback(
    (data: UpdateProjectRequest) => updateProject.mutate({ id: project.id, ...data }),
    [project.id, updateProject],
  );
  const progressPercent =
    project.issue_count > 0
      ? Math.round((project.done_count / project.issue_count) * 100)
      : 0;

  return (
    <div className="group/card group/row flex flex-col rounded-md border bg-card transition-colors hover:border-primary/50">
      <div className="p-3 pb-2">
        <div className="flex items-center gap-2">
          <AppLink
            href={wsPaths.projectDetail(project.id)}
            className="flex min-w-0 flex-1 items-center gap-2"
          >
            <ProjectIcon project={project} size="sm" />
            <h3 className="truncate text-sm font-medium">{project.title}</h3>
          </AppLink>
          <ProjectRowActions project={project} pinned={pinned} canDelete={canDelete} />
          <ProjectStatusBadge project={project} handleUpdate={handleUpdate} triggerClassName="shrink-0" />
        </div>

        {project.issue_count > 0 ? (
          <div className="flex items-center justify-end gap-1.5 pt-2">
            <div className="relative h-4 w-4">
              <svg className="h-4 w-4 -rotate-90" viewBox="0 0 16 16">
                <circle className="text-muted" strokeWidth="2" stroke="currentColor" fill="none" r="6" cx="8" cy="8" />
                <circle
                  className="text-emerald-500"
                  strokeWidth="2"
                  stroke="currentColor"
                  fill="none"
                  r="6"
                  cx="8"
                  cy="8"
                  strokeDasharray={`${progressPercent * 0.377} 37.7`}
                  strokeLinecap="round"
                />
              </svg>
            </div>
            <span className="text-[10px] tabular-nums text-muted-foreground">
              {project.done_count}/{project.issue_count}
            </span>
          </div>
        ) : (
          <span className="flex justify-end pt-2 text-[10px] text-muted-foreground">
            {t(($) => $.detail.no_issues_yet)}
          </span>
        )}
      </div>

      <div className="mt-0 flex items-center justify-between border-t px-3 pb-3 pt-2">
        <ProjectLeadPicker
          project={project}
          handleUpdate={handleUpdate}
          renderTrigger={(leadName) => (
            <button type="button" className="-mx-1.5 flex items-center gap-1.5 rounded px-1.5 py-0.5 transition-colors hover:bg-accent/60">
              {project.lead_type && project.lead_id ? (
                <ActorAvatar actorType={project.lead_type} actorId={project.lead_id} size={20} enableHoverCard />
              ) : (
                <span className="inline-flex h-5 w-5 rounded-full border border-dashed border-muted-foreground/30" />
              )}
              <span className="max-w-[60px] truncate text-[10px] text-muted-foreground">
                {leadName ?? t(($) => $.lead.no_lead)}
              </span>
            </button>
          )}
        />
        <div className="flex items-center gap-2">
          <ProjectPriorityBadge project={project} handleUpdate={handleUpdate} align="start" />
          <span className="text-[10px] text-muted-foreground">
            {formatRelativeDate(project.created_at)}
          </span>
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Toolbar — search + result count + filter + display (compact only) + view
// toggle.
// ---------------------------------------------------------------------------

const STATUS_VALUES: ProjectStatus[] = [
  "planned",
  "in_progress",
  "paused",
  "completed",
  "cancelled",
];
const PRIORITY_VALUES: ProjectPriority[] = ["urgent", "high", "medium", "low", "none"];
const COLUMN_KEYS: ProjectColumnKey[] = ["priority", "progress", "lead", "issues", "created"];
const SORT_FIELDS: ProjectSortField[] = ["name", "priority", "status", "progress", "created"];

function countActiveFilters(f: ProjectListFilters): number {
  let c = 0;
  if (f.statuses.length) c++;
  if (f.priorities.length) c++;
  if (f.leads.length) c++;
  return c;
}

// Batch toolbar — page-anchored (not viewport). Pin all selected (any
// member) + Delete (workspace admin). Mirrors the other lists.
function ProjectBatchToolbar({
  rows,
  pinnedIds,
  canDelete,
  onClear,
}: {
  rows: Project[];
  pinnedIds: Set<string>;
  canDelete: boolean;
  onClear: () => void;
}) {
  const { t } = useT("projects");
  const createPin = useCreatePin();
  const deleteProject = useDeleteProject();
  const [confirmDelete, setConfirmDelete] = useState(false);

  if (rows.length === 0) return null;
  const anyUnpinned = rows.some((p) => !pinnedIds.has(p.id));

  return (
    <>
      <div className="absolute bottom-6 left-1/2 z-50 flex -translate-x-1/2 items-center gap-1 rounded-lg border bg-background px-2 py-1.5 shadow-lg">
        <div className="mr-1 flex items-center gap-1.5 border-r pl-1 pr-2">
          <span className="text-sm font-medium">
            {t(($) => $.page.selected, { count: rows.length })}
          </span>
          <button
            type="button"
            aria-label={t(($) => $.page.clear_selection)}
            onClick={onClear}
            className="rounded p-0.5 transition-colors hover:bg-accent"
          >
            <X className="size-3.5 text-muted-foreground" />
          </button>
        </div>
        {anyUnpinned && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              for (const p of rows) {
                if (!pinnedIds.has(p.id)) {
                  createPin.mutate({ item_type: "project", item_id: p.id });
                }
              }
              onClear();
            }}
          >
            <Pin className="mr-1 size-3.5" />
            {t(($) => $.page.pin)}
          </Button>
        )}
        {canDelete && (
          <Button
            variant="ghost"
            size="sm"
            className="text-destructive hover:text-destructive"
            onClick={() => setConfirmDelete(true)}
          >
            <Trash2 className="mr-1 size-3.5" />
            {t(($) => $.page.delete)}
          </Button>
        )}
      </div>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t(($) => $.delete_dialog.title)}</DialogTitle>
            <DialogDescription>{t(($) => $.delete_dialog.description)}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button type="button" variant="outline" size="sm" onClick={() => setConfirmDelete(false)}>
              {t(($) => $.delete_dialog.cancel)}
            </Button>
            <Button
              type="button"
              variant="destructive"
              size="sm"
              onClick={() => {
                for (const p of rows) deleteProject.mutate(p.id);
                setConfirmDelete(false);
                onClear();
              }}
            >
              {t(($) => $.delete_dialog.confirm)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function ProjectsPage() {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const rowLink = useRowLink();
  const currentUser = useAuthStore((s) => s.user);
  const { getActorName } = useActorName();

  const viewMode = useProjectViewStore((s) => s.viewMode);
  const setViewMode = useProjectViewStore((s) => s.setViewMode);
  const sortField = useProjectViewStore((s) => s.sortField);
  const sortDirection = useProjectViewStore((s) => s.sortDirection);
  const hiddenColumns = useProjectViewStore((s) => s.hiddenColumns);
  const filters = useProjectViewStore((s) => s.filters);
  const toggleSort = useProjectViewStore((s) => s.toggleSort);
  const setSortField = useProjectViewStore((s) => s.setSortField);
  const setSortDirection = useProjectViewStore((s) => s.setSortDirection);
  const toggleColumn = useProjectViewStore((s) => s.toggleColumn);
  const toggleFilter = useProjectViewStore((s) => s.toggleFilter);
  const clearFilters = useProjectViewStore((s) => s.clearFilters);
  const isCompact = viewMode === "compact";
  const isColVisible = (key: ProjectColumnKey) => !hiddenColumns.includes(key);

  const { data: projects = [], isLoading } = useQuery(projectListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: pins = [] } = useQuery({
    ...pinListOptions(wsId, currentUser?.id ?? ""),
    enabled: !!wsId && !!currentUser?.id,
  });
  const openCreateProject = () => useModalStore.getState().open("create-project");

  const isWorkspaceAdmin = useMemo(() => {
    if (!currentUser) return false;
    const me = members.find((m: MemberWithUser) => m.user_id === currentUser.id);
    return me?.role === "owner" || me?.role === "admin";
  }, [members, currentUser]);

  const pinnedProjectIds = useMemo(() => {
    const s = new Set<string>();
    for (const pin of pins) if (pin.item_type === "project") s.add(pin.item_id);
    return s;
  }, [pins]);

  const [search, setSearch] = useState("");
  const [selectedIds, setSelectedIds] = useState<ReadonlySet<string>>(new Set());
  const toggleSelected = (id: string) =>
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const activeFilterCount = countActiveFilters(filters);
  const hasActiveFilters = activeFilterCount > 0;
  const displayProjects = projects;

  // Filter option counts derive from the full set so toggling one dimension
  // doesn't make the others vanish.
  const leadOptions = useMemo(() => {
    const m = new Map<string, { type: string; id: string; count: number }>();
    for (const p of displayProjects) {
      const v = leadFilterValue(p);
      if (!v || !p.lead_type || !p.lead_id) continue;
      const e = m.get(v);
      if (e) e.count += 1;
      else m.set(v, { type: p.lead_type, id: p.lead_id, count: 1 });
    }
    return m;
  }, [displayProjects]);

  const visible = useMemo(() => {
    const q = search.trim().toLowerCase();
    const filtered = displayProjects.filter((p) => {
      if (q && !p.title.toLowerCase().includes(q) && !matchesPinyin(p.title, q)) {
        return false;
      }
      if (filters.statuses.length && !filters.statuses.includes(p.status)) return false;
      if (filters.priorities.length && !filters.priorities.includes(p.priority)) {
        return false;
      }
      if (filters.leads.length) {
        const v = leadFilterValue(p);
        if (!v || !filters.leads.includes(v)) return false;
      }
      return true;
    });
    const dir = sortDirection === "asc" ? 1 : -1;
    const sorted = [...filtered];
    sorted.sort((a, b) => {
      if (sortField === "name") return a.title.localeCompare(b.title) * dir;
      if (sortField === "priority") {
        return (
          (PRIORITY_ORDER[a.priority] - PRIORITY_ORDER[b.priority]) * dir ||
          a.title.localeCompare(b.title)
        );
      }
      if (sortField === "status") {
        return (
          (STATUS_ORDER[a.status] - STATUS_ORDER[b.status]) * dir ||
          a.title.localeCompare(b.title)
        );
      }
      if (sortField === "progress") {
        return (progressOf(a) - progressOf(b)) * dir || a.title.localeCompare(b.title);
      }
      return (Date.parse(a.created_at) - Date.parse(b.created_at)) * dir;
    });
    return sorted;
  }, [displayProjects, search, filters, sortField, sortDirection]);

  const selectedProjects = visible.filter((p) => selectedIds.has(p.id));
  const allSelected = visible.length > 0 && selectedProjects.length === visible.length;
  const someSelected = selectedProjects.length > 0 && !allSelected;
  const handleToggleAll = () =>
    setSelectedIds(allSelected ? new Set() : new Set(visible.map((p) => p.id)));

  const sortLabel = (f: ProjectSortField) =>
    f === "name"
      ? t(($) => $.table.name)
      : f === "priority"
        ? t(($) => $.table.priority)
        : f === "status"
          ? t(($) => $.table.status)
          : f === "progress"
            ? t(($) => $.table.progress)
            : t(($) => $.table.created);
  const columnLabel = (k: ProjectColumnKey) =>
    k === "priority"
      ? t(($) => $.table.priority)
      : k === "progress"
        ? t(($) => $.table.progress)
        : k === "lead"
          ? t(($) => $.table.lead)
          : k === "issues"
            ? t(($) => $.table.issues)
            : t(($) => $.table.created);

  const showEmpty = !isLoading && displayProjects.length === 0 && projects.length === 0;
  const countBadge = (n: number) => (
    <span className="ml-auto pl-3 text-xs text-muted-foreground">{n}</span>
  );

  return (
    // relative: positioning anchor for the page-centered batch toolbar.
    <div className="relative flex flex-1 min-h-0 flex-col">
      <PageHeader className="justify-between px-5">
        <div className="flex items-center gap-2">
          <FolderKanban className="h-4 w-4 text-muted-foreground" />
          <h1 className="text-sm font-medium">{t(($) => $.page.title)}</h1>
          {displayProjects.length > 0 && (
            <span className="font-mono text-xs tabular-nums text-muted-foreground/70">
              {displayProjects.length}
            </span>
          )}
        </div>
        <Button
          size="sm"
          variant="outline"
          className="h-8 w-8 gap-1 px-0 md:w-auto md:px-2.5"
          aria-label={t(($) => $.page.new_project)}
          onClick={openCreateProject}
        >
          <Plus className="h-3.5 w-3.5" />
          <span className="hidden md:inline">{t(($) => $.page.new_project)}</span>
        </Button>
      </PageHeader>

      {showEmpty ? (
        <div className="flex flex-1 flex-col items-center justify-center py-24 text-muted-foreground">
          <FolderKanban className="mb-3 h-10 w-10 opacity-30" />
          <p className="text-sm">{t(($) => $.page.empty)}</p>
          <Button size="sm" variant="outline" className="mt-3" onClick={openCreateProject}>
            {t(($) => $.page.create_first)}
          </Button>
        </div>
      ) : (
        <>
          {/* Toolbar */}
          <div className="flex h-12 shrink-0 items-center justify-between gap-2 px-5">
            <div className="flex min-w-0 items-center gap-2">
              <div className="relative hidden md:block">
                <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  placeholder={t(($) => $.page.search_placeholder)}
                  className="h-8 w-56 pl-8 text-sm"
                />
              </div>
              {(hasActiveFilters || search.trim().length > 0) && (
                <span
                  title={t(($) => $.toolbar.result_count_title)}
                  className="hidden shrink-0 text-xs tabular-nums text-muted-foreground md:inline"
                >
                  {visible.length} / {displayProjects.length}
                </span>
              )}
            </div>

            <div className="flex shrink-0 items-center gap-1">
              {/* Filter */}
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
                            clearFilters();
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
                      <span className="flex-1">{t(($) => $.toolbar.section_status)}</span>
                      {filters.statuses.length > 0 && (
                        <span className="text-xs font-medium text-primary">{filters.statuses.length}</span>
                      )}
                    </DropdownMenuSubTrigger>
                    <DropdownMenuSubContent className="w-auto min-w-44">
                      {STATUS_VALUES.map((s) => (
                        <DropdownMenuCheckboxItem
                          key={s}
                          checked={filters.statuses.includes(s)}
                          onCheckedChange={() => toggleFilter("statuses", s)}
                          className={FILTER_ITEM_CLASS}
                        >
                          <HoverCheck checked={filters.statuses.includes(s)} />
                          {t(($) => $.status[s])}
                        </DropdownMenuCheckboxItem>
                      ))}
                    </DropdownMenuSubContent>
                  </DropdownMenuSub>
                  <DropdownMenuSub>
                    <DropdownMenuSubTrigger>
                      <span className="flex-1">{t(($) => $.toolbar.section_priority)}</span>
                      {filters.priorities.length > 0 && (
                        <span className="text-xs font-medium text-primary">{filters.priorities.length}</span>
                      )}
                    </DropdownMenuSubTrigger>
                    <DropdownMenuSubContent className="w-auto min-w-44">
                      {PRIORITY_VALUES.map((pr) => (
                        <DropdownMenuCheckboxItem
                          key={pr}
                          checked={filters.priorities.includes(pr)}
                          onCheckedChange={() => toggleFilter("priorities", pr)}
                          className={FILTER_ITEM_CLASS}
                        >
                          <HoverCheck checked={filters.priorities.includes(pr)} />
                          {t(($) => $.priority[pr])}
                        </DropdownMenuCheckboxItem>
                      ))}
                    </DropdownMenuSubContent>
                  </DropdownMenuSub>
                  <DropdownMenuSub>
                    <DropdownMenuSubTrigger>
                      <span className="flex-1">{t(($) => $.toolbar.section_lead)}</span>
                      {filters.leads.length > 0 && (
                        <span className="text-xs font-medium text-primary">{filters.leads.length}</span>
                      )}
                    </DropdownMenuSubTrigger>
                    <DropdownMenuSubContent className="max-h-72 w-auto min-w-48 overflow-y-auto">
                      {[...leadOptions.entries()].map(([value, { type, id, count }]) => (
                        <DropdownMenuCheckboxItem
                          key={value}
                          checked={filters.leads.includes(value)}
                          onCheckedChange={() => toggleFilter("leads", value)}
                          className={FILTER_ITEM_CLASS}
                        >
                          <HoverCheck checked={filters.leads.includes(value)} />
                          <ActorAvatar actorType={type} actorId={id} size={16} />
                          <span className="min-w-0 truncate">{getActorName(type, id)}</span>
                          {countBadge(count)}
                        </DropdownMenuCheckboxItem>
                      ))}
                    </DropdownMenuSubContent>
                  </DropdownMenuSub>
                </DropdownMenuContent>
              </DropdownMenu>

              {/* Display (sort + columns). Always present — view mode is a
                  pure presentation choice and must not reshape the toolbar.
                  Sort applies to both views; the columns section is shown
                  only in the table view (cards have no columns). */}
              <Popover>
                  <Tooltip>
                    <PopoverTrigger
                      render={
                        <TooltipTrigger
                          render={
                            <Button variant="outline" size="sm" className="h-8 w-8 gap-1 px-0 text-muted-foreground md:w-auto md:px-2.5">
                              {sortDirection === "asc" ? <ArrowUp className="size-3.5" /> : <ArrowDown className="size-3.5" />}
                              <span className="hidden md:inline">{sortLabel(sortField)}</span>
                            </Button>
                          }
                        />
                      }
                    />
                    <TooltipContent side="bottom">{t(($) => $.toolbar.display)}</TooltipContent>
                  </Tooltip>
                  <PopoverContent align="end" className="w-64 p-0">
                    <div className="border-b px-3 py-2.5">
                      <span className="text-xs font-medium text-muted-foreground">{t(($) => $.toolbar.sort_by)}</span>
                      <div className="mt-2 flex items-center gap-1.5">
                        <DropdownMenu>
                          <DropdownMenuTrigger
                            render={
                              <Button variant="outline" size="sm" className="flex-1 justify-between text-xs">
                                {sortLabel(sortField)}
                                <ChevronDown className="size-3 text-muted-foreground" />
                              </Button>
                            }
                          />
                          <DropdownMenuContent align="start" className="w-auto">
                            <DropdownMenuRadioGroup
                              value={sortField}
                              onValueChange={(v) => setSortField(v as ProjectSortField)}
                            >
                              {SORT_FIELDS.map((f) => (
                                <DropdownMenuRadioItem key={f} value={f}>
                                  {sortLabel(f)}
                                </DropdownMenuRadioItem>
                              ))}
                            </DropdownMenuRadioGroup>
                          </DropdownMenuContent>
                        </DropdownMenu>
                        <Button
                          variant="outline"
                          size="icon-sm"
                          onClick={() => setSortDirection(sortDirection === "asc" ? "desc" : "asc")}
                          title={sortDirection === "asc" ? t(($) => $.toolbar.direction_asc) : t(($) => $.toolbar.direction_desc)}
                        >
                          {sortDirection === "asc" ? <ArrowUp className="size-3.5" /> : <ArrowDown className="size-3.5" />}
                        </Button>
                      </div>
                    </div>
                    {isCompact && (
                      <div className="px-3 py-2.5">
                        <span className="text-xs font-medium text-muted-foreground">{t(($) => $.toolbar.section_columns)}</span>
                        <div className="mt-2 space-y-2">
                          {COLUMN_KEYS.map((key) => (
                            <label key={key} className="flex cursor-pointer items-center justify-between">
                              <span className="text-sm">{columnLabel(key)}</span>
                              <Switch size="sm" checked={!hiddenColumns.includes(key)} onCheckedChange={() => toggleColumn(key)} />
                            </label>
                          ))}
                        </div>
                      </div>
                    )}
                  </PopoverContent>
                </Popover>

              {/* View toggle — a single button that flips table ⇄ cards.
                  Pure presentation; coupled to nothing else. */}
              <Tooltip>
                <TooltipTrigger
                  render={
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-8 w-8 gap-1 px-0 text-muted-foreground md:w-auto md:px-2.5"
                      onClick={() => setViewMode(isCompact ? "comfortable" : "compact")}
                    >
                      {isCompact ? (
                        <Rows3 className="size-3.5" />
                      ) : (
                        <LayoutGrid className="size-3.5" />
                      )}
                      <span className="hidden md:inline">
                        {isCompact ? t(($) => $.page.view_table) : t(($) => $.page.view_cards)}
                      </span>
                    </Button>
                  }
                />
                <TooltipContent side="bottom">
                  {isCompact ? t(($) => $.page.view_cards) : t(($) => $.page.view_table)}
                </TooltipContent>
              </Tooltip>
            </div>
          </div>

          <GongfengRepositoryInventory projects={displayProjects} />

          {/* Body */}
          {isLoading ? (
            <LoadingState isCompact={isCompact} />
          ) : visible.length === 0 ? (
            <div className="flex flex-1 flex-col items-center justify-center py-24 text-muted-foreground">
              <Search className="mb-3 h-10 w-10 opacity-30" />
              <p className="text-sm">{t(($) => $.page.no_matches)}</p>
            </div>
          ) : isCompact ? (
            <div className="min-h-0 flex-1 overflow-auto @container">
              <ListGrid
                className={`${GRID_COLS} @2xl:min-w-[var(--pjc-minw)]`}
                style={{
                  ...columnTrackVars(isColVisible),
                  paddingBottom: LIST_GRID_BOTTOM_CLEARANCE,
                }}
              >
                <ProjectTableHeader
                  sortField={sortField}
                  sortDirection={sortDirection}
                  onSort={toggleSort}
                  isColVisible={isColVisible}
                  allSelected={allSelected}
                  someSelected={someSelected}
                  onToggleAll={handleToggleAll}
                />
                {visible.map((project) => (
                  <ProjectTableRow
                    key={project.id}
                    project={project}
                    pinned={pinnedProjectIds.has(project.id)}
                    canDelete={isWorkspaceAdmin}
                    isColVisible={isColVisible}
                    selected={selectedIds.has(project.id)}
                    onToggleSelect={() => toggleSelected(project.id)}
                    rowHref={wsPaths.projectDetail(project.id)}
                    rowLink={rowLink}
                  />
                ))}
              </ListGrid>
            </div>
          ) : (
            <div className="min-h-0 flex-1 overflow-y-auto px-5 pt-4">
              <div
                className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4"
                style={{ paddingBottom: LIST_GRID_BOTTOM_CLEARANCE }}
              >
                {visible.map((project) => (
                  <ProjectCard
                    key={project.id}
                    project={project}
                    pinned={pinnedProjectIds.has(project.id)}
                    canDelete={isWorkspaceAdmin}
                  />
                ))}
              </div>
            </div>
          )}

          <ProjectBatchToolbar
            rows={selectedProjects}
            pinnedIds={pinnedProjectIds}
            canDelete={isWorkspaceAdmin}
            onClear={() => setSelectedIds(new Set())}
          />
        </>
      )}
    </div>
  );
}

function LoadingState({ isCompact }: { isCompact: boolean }) {
  if (isCompact) {
    return (
      <div className="min-h-0 flex-1 overflow-auto px-5 pt-4">
        <div className="space-y-2">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-11 w-full rounded-md" />
          ))}
        </div>
      </div>
    );
  }
  return (
    <div className="grid grid-cols-1 gap-3 px-5 pt-4 sm:grid-cols-2 lg:grid-cols-4">
      {Array.from({ length: 8 }).map((_, i) => (
        <div key={i} className="flex flex-col gap-2 rounded-md border p-3">
          <div className="flex items-center gap-2">
            <Skeleton className="h-8 w-8 rounded" />
            <Skeleton className="h-4 w-3/4" />
          </div>
          <div className="flex gap-1.5">
            <Skeleton className="h-5 w-16 rounded" />
            <Skeleton className="h-5 w-20 rounded" />
          </div>
        </div>
      ))}
    </div>
  );
}
