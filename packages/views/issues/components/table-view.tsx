"use client";

import { workspaceListOptions } from "@multica/core/workspace/queries";

// 本文件：TableView —— 问题列表的"表格视图"。
// 负责渲染可排序/可拖拽调序/可显隐列的表格，支持服务端分组（按状态/负责人/项目/
// 自定义属性）与父子层级、按分支无限分页 + 实时刷新去抖、单元内联编辑（选择器 /
// 标题重命名）、行选择（含 shift 连选）、CSV 导出、虚拟化行渲染。
// 核心技巧：表格单元组件必须是模块级稳定引用（渲染上下文走 table.options.meta），
// 否则任何工作区刷新都会重挂载所有单元格、关闭用户刚打开的选择器（MUL-5108）。

import { useStatusLabel } from "../utils/status-label";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useDndContext,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import { restrictToHorizontalAxis } from "@dnd-kit/modifiers";
import {
  SortableContext,
  horizontalListSortingStrategy,
  sortableKeyboardCoordinates,
  useSortable,
} from "@dnd-kit/sortable";
import {
  getCoreRowModel,
  useReactTable,
  type CellContext,
  type ColumnDef,
  type ColumnSizingState,
  type HeaderContext,
  type OnChangeFn,
  type Table as TanstackTable,
  type TableMeta,
} from "@tanstack/react-table";
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronRight,
  Download,
  EyeOff,
  GripVertical,
  Loader2,
  Pencil,
  Plus,
  Search,
  X,
} from "lucide-react";
import { toast } from "sonner";
import { DataTable } from "@multica/ui/components/ui/data-table";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuCheckboxItem,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import {
  TableCell,
  TableRow,
} from "@multica/ui/components/ui/table";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { ApiError } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { useModalStore } from "@multica/core/modals";
import {
  issueKeys,
  issueTableGroupsOptions,
  issueTableRowPageOptions,
} from "@multica/core/issues/queries";
import {
  TABLE_SYSTEM_COLUMNS,
  propertyIdFromViewKey,
  type SortField,
  type TableColumnKey,
  type TableSystemColumnKey,
} from "@multica/core/issues/stores/view-store";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import { propertyListOptions } from "@multica/core/properties";
import { projectListOptions } from "@multica/core/projects/queries";
import { paths as workspacePaths, useWorkspacePaths } from "@multica/core/paths";
import { buildActorNameResolver, useActorName } from "@multica/core/workspace/hooks";
import {
  agentListOptions,
  memberListOptions,
  squadListOptions,
} from "@multica/core/workspace/queries";
import type {
  Issue,
  IssueProperty,
  IssuePropertyValue,
  IssueTableGroupDescriptor,
  IssueTableGroupSpec,
  IssueTableQuerySpec,
  IssueTableRowsResponse,
  Project,
  UpdateIssueRequest,
} from "@multica/core/types";
import {
  actorRefsFromValue,
  formatActorRef,
  isActorPropertyType,
} from "@multica/core/types";
import {
  useInfiniteQuery,
  useQueries,
  useQuery,
  useQueryClient,
  type UseQueryResult,
} from "@tanstack/react-query";
import { runConfirmIntent } from "../actions/run-confirm-gate";
import { ActorAvatar } from "../../common/actor-avatar";
import { LabelChip } from "../../labels/label-chip";
import { resolveClickIntent, useIntentNavigate } from "../../navigation";
import { ProjectPicker } from "../../projects/components/project-picker";
import { useT } from "../../i18n";
import { useIssueSurfaceActionsOptional } from "../surface/actions-context";
import { useIssueSurfaceSelection } from "../surface/selection-context";
import type { IssueCreateDefaults } from "../surface/types";
import { ProgressRing } from "./progress-ring";
import {
  AssigneePicker,
  DueDatePicker,
  LabelPicker,
  PriorityPicker,
  StartDatePicker,
  StatusPicker,
} from "./pickers";
import { CustomPropertyValueEditor } from "./pickers/custom-property-picker";
import {
  buildIssueTableCsv,
  getIssueTableSelectionRange,
  IssueTableExportIntegrityError,
  refreshFrozenTableRows,
  type IssueTableDisplayRow,
} from "./table-view-model";
import type { ChildProgress } from "./list-row";
import { ListLoadMoreFooter } from "./list-load-more-footer";
import { IssueAgentActivityIndicator } from "./issue-agent-activity-indicator";
import { IssueReviewBadge } from "./issue-review-badge";

// 足够覆盖典型视口的占位行数；虚拟化只挂载放得下的行，多铺几行零成本。
const SKELETON_ROW_COUNT = 12;

const SELECT_COLUMN_ID = "__select";
const ADD_COLUMN_ID = "__add";

type TableViewProps = {
  serverQuery: IssueTableQuerySpec;
  childProgressMap: Map<string, ChildProgress>;
  search: string;
  onSearchChange: (query: string) => void;
  onLoadedIssuesChange: (issues: Issue[]) => void;
  onCreateIssue: (defaults: IssueCreateDefaults) => void;
  exportIssues: () => Promise<Issue[]>;
  resolveExportLookups: (needs: {
    projects: boolean;
    childProgress: boolean;
  }) => Promise<{
    projectMap: Map<string, Project>;
    childProgressMap: Map<string, ChildProgress>;
  }>;
};

type ServerBranch = {
  key: string;
  groupKey: string | null;
  parentId: string | null;
  ancestorIds: string[];
  cursors: Array<string | null>;
};

type ServerBranchState = {
  identity: string;
  structureIdentity: string;
  branches: Map<string, ServerBranch>;
};

type ServerBranchPageTarget = {
  branch: ServerBranch;
  cursor: string | null;
};

type ServerBranchData = {
  rows: IssueTableRowsResponse["rows"];
  total: number;
  nextCursor: string | null;
  headUpdatedAt: number;
  headFetching: boolean;
  loading: boolean;
  error: boolean;
  placeholder: boolean;
};

type LoadedIssueState = {
  membershipIdentity: string;
  issues: Map<string, Issue>;
};

function serverBranchKey(groupKey: string | null, parentId: string | null) {
  return `${groupKey ?? "ungrouped"}::${parentId ?? "root"}`;
}

function sameStringArray(left: string[], right: string[]) {
  return (
    left.length === right.length &&
    left.every((value, index) => value === right[index])
  );
}

/** 查询变化时同步地"重挂"分支图。
 *
 * 筛选/搜索/排序不会改变分组与层级结构，因此保留分支身份、只把所有游标重置回
 * 头部，就能让 React Query 在新请求进行期间继续显示旧行。真正改了结构
 * （分组方式或层级开关）时才丢弃不兼容的分组/父级身份。 */
function rebaseServerBranchState(
  previous: ServerBranchState,
  identity: string,
  structureIdentity: string,
  usesServerGrouping: boolean,
): ServerBranchState {
  if (previous.identity === identity) return previous;

  const branches =
    previous.structureIdentity === structureIdentity
      ? new Map(
          [...previous.branches].map(([key, branch]) => [
            key,
            { ...branch, cursors: [null] },
          ]),
        )
      : new Map<string, ServerBranch>();

  if (!usesServerGrouping) {
    const key = serverBranchKey(null, null);
    if (!branches.has(key)) {
      branches.set(key, {
        key,
        groupKey: null,
        parentId: null,
        ancestorIds: [],
        cursors: [null],
      });
    }
  }

  return { identity, structureIdentity, branches };
}

function tableGroupSpec(grouping: string): IssueTableGroupSpec {
  if (grouping === "status") return { kind: "status" };
  if (grouping === "assignee") return { kind: "assignee" };
  if (grouping === "project") return { kind: "project" };
  const propertyId = propertyIdFromViewKey(grouping);
  if (propertyId) return { kind: "property", property_id: propertyId };
  return { kind: "none" };
}

type ColumnLabelKey =
  | "title"
  | "identifier"
  | "status"
  | "priority"
  | "assignee"
  | "labels"
  | "project"
  | "start_date"
  | "due_date"
  | "created_at"
  | "updated_at"
  | "child_progress"
  | "creator";

const SORTABLE_COLUMNS: Partial<Record<TableSystemColumnKey, SortField>> = {
  title: "title",
  status: "status",
  priority: "priority",
  start_date: "start_date",
  due_date: "due_date",
  created_at: "created_at",
  updated_at: "updated_at",
};

function stopRowNavigation(event: React.SyntheticEvent) {
  event.stopPropagation();
}

function SelectAllCheckbox({
  issueIds,
  label,
}: {
  issueIds: string[];
  label: string;
}) {
  const selection = useIssueSurfaceSelection();
  const ref = useRef<HTMLInputElement>(null);
  const selectedCount = issueIds.filter((id) => selection.selectedIds.has(id)).length;
  const checked = issueIds.length > 0 && selectedCount === issueIds.length;

  useEffect(() => {
    if (ref.current) {
      ref.current.indeterminate = selectedCount > 0 && !checked;
    }
  }, [checked, selectedCount]);

  return (
    <input
      ref={ref}
      type="checkbox"
      aria-label={label}
      checked={checked}
      onChange={() =>
        checked ? selection.deselect(issueIds) : selection.select(issueIds)
      }
      className="size-3.5 cursor-pointer accent-primary"
    />
  );
}

function IssueCheckbox({
  checked,
  label,
  onToggle,
}: {
  checked: boolean;
  label: string;
  onToggle: (shiftKey: boolean) => void;
}) {
  return (
    <input
      type="checkbox"
      aria-label={label}
      checked={checked}
      onClick={(event) => {
        event.stopPropagation();
        onToggle(event.shiftKey);
      }}
      onAuxClick={stopRowNavigation}
      onChange={() => undefined}
      className="size-3.5 cursor-pointer accent-primary"
    />
  );
}

function SortableColumnHeader({
  columnKey,
  label,
  sortField,
  sortBy,
  sortDirection,
  onSort,
  onHide,
  ascendingLabel,
  descendingLabel,
  hideLabel,
  reorderLabel,
}: {
  columnKey: TableColumnKey;
  label: string;
  sortField?: SortField;
  sortBy: SortField;
  sortDirection: "asc" | "desc";
  onSort: (field: SortField, direction: "asc" | "desc") => void;
  onHide?: () => void;
  ascendingLabel: string;
  descendingLabel: string;
  hideLabel: string;
  reorderLabel: string;
}) {
  const sortable = columnKey !== "title";
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } =
    useSortable({ id: columnKey, disabled: !sortable });
  const active = sortField === sortBy;
  // 只要有任何列在拖动（不限于本列）：相邻列会让位腾出缝隙，每列都同样被自己的
  // 单元格裁剪着。
  const isReordering = useDndContext().active != null;
  const nodeRef = useRef<HTMLDivElement | null>(null);

  // 单元格默认裁剪自身内容——这正是拖拽中的列看起来"消失"而非"跟着走"的原因。
  // 这种裁剪只在静止时有意义（限制比列还宽的标签），所以整段重排期间暂时放开，
  // 并让被拖的列浮到相邻列之上。
  //
  // 这里只动 overflow 与层叠。直接给 <th> 加 transform 会把表头整行高度一起带着
  // 跑，而且对表格单元格的 `transform`，各浏览器实现得很随意——Chromium 会把该格
  // 从表格盒模型里抽出来，几何位置便不再与行对应。所以下面这个 wrapper 用负内边距
  // 补足到与单元格同尺寸，由它来充当"被移动"的那一块。
  useLayoutEffect(() => {
    const cell = nodeRef.current?.closest("th");
    if (!cell || !isReordering) return;
    cell.style.overflow = "visible";
    if (isDragging) cell.style.zIndex = "20";
    return () => {
      cell.style.removeProperty("overflow");
      cell.style.removeProperty("z-index");
    };
  }, [isDragging, isReordering]);

  return (
    <div
      ref={(node) => {
        nodeRef.current = node;
        setNodeRef(node);
      }}
      // 只允许水平移动。dnd-kit 的布局动画还会回传 scaleX/scaleY——用"新矩形 ÷
      // 旧矩形"把某个对象补间成它落位槽位的形状。两个等宽标签页之间该比例恒为 1、
      // 永远看不出效果；但两列之间不是，一根 174px 的列与 96px 的列互换时会被拉到
      // 1.8 倍。重排列不会改变任何列宽，因此这里的形状补间无话可说。
      // 移动与落定仍由 `transition` 负责动画。
      style={{
        transform: transform ? `translate3d(${transform.x}px, 0, 0)` : undefined,
        transition,
      }}
      // wrapper 撑满单元格自身的盒子——负外边距抵消 <th> 的内边距、让它"住回"格子
      // 里——所以静止时它渲染得分毫不差，动起来的就是它：一块表头大小的方块，而非
      // 其中的一行文字。高度用派生值而非写死 h-8：一旦算上行边框，这条表头条会比
      // 单元格的名义高度更高。
      className={cn(
        "group/header -mx-4 -my-2 flex h-[calc(100%+1rem)] min-w-0 items-center px-4",
        isDragging && "opacity-60",
      )}
    >
      {sortable && (
        <button
          type="button"
          aria-label={reorderLabel}
          className={cn(
            "-ml-2 mr-0.5 rounded p-0.5 text-muted-foreground opacity-0 hover:bg-accent hover:text-muted-foreground group-hover/header:opacity-100 focus-visible:opacity-100",
            isDragging ? "cursor-grabbing opacity-100" : "cursor-grab",
          )}
          {...attributes}
          {...listeners}
        >
          <GripVertical className="size-3" />
        </button>
      )}
      <DropdownMenu>
        <DropdownMenuTrigger className="flex min-w-0 items-center gap-1 rounded px-1.5 py-1 hover:bg-accent">
          <span className="truncate">{label}</span>
          {active &&
            (sortDirection === "asc" ? (
              <ArrowUp className="size-3 shrink-0" />
            ) : (
              <ArrowDown className="size-3 shrink-0" />
            ))}
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-40">
          {sortField && (
            <>
              <DropdownMenuItem onClick={() => onSort(sortField, "asc")}>
                <ArrowUp />
                {ascendingLabel}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => onSort(sortField, "desc")}>
                <ArrowDown />
                {descendingLabel}
              </DropdownMenuItem>
            </>
          )}
          {sortField && onHide && <DropdownMenuSeparator />}
          {onHide && (
            <DropdownMenuItem onClick={onHide}>
              <EyeOff />
              {hideLabel}
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

/**
 * 表格"列选择器"下拉：可勾选系统列与工作区自定义属性列（标题列固定不可隐藏），
 * 带搜索框过滤。列的显隐写回 view store（`toggleTableColumn`）。
 */
export function TableColumnPicker({
  properties,
  trigger,
}: {
  properties: IssueProperty[];
  trigger: React.ReactElement;
}) {
  const { t } = useT("issues");
  const [search, setSearch] = useState("");
  const tableColumns = useViewStore((state) => state.tableColumns);
  const toggleTableColumn = useViewStore((state) => state.toggleTableColumn);
  const selected = useMemo(
    () => new Set(tableColumns.map((column) => column.key)),
    [tableColumns],
  );
  const query = search.trim().toLocaleLowerCase();
  const systemColumns = TABLE_SYSTEM_COLUMNS.filter((key) =>
    t(($) => $.table.columns[key as ColumnLabelKey])
      .toLocaleLowerCase()
      .includes(query),
  );
  const visibleProperties = properties.filter((property) =>
    property.name.toLocaleLowerCase().includes(query),
  );

  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={trigger} />
      <DropdownMenuContent align="end" className="w-64 p-0">
        <div className="border-b p-2">
          <Input
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            onKeyDown={(event) => {
              if (event.key !== "Escape") event.stopPropagation();
            }}
            placeholder={t(($) => $.table.columns.search_placeholder)}
            className="h-7"
          />
        </div>
        <div className="max-h-80 overflow-y-auto p-1">
          {systemColumns.length > 0 && (
            <DropdownMenuGroup>
              <DropdownMenuLabel>
                {t(($) => $.table.columns.system_section)}
              </DropdownMenuLabel>
              {systemColumns.map((key) => (
                <DropdownMenuCheckboxItem
                  key={key}
                  disabled={key === "title"}
                  checked={selected.has(key)}
                  onCheckedChange={() => toggleTableColumn(key)}
                >
                  {t(($) => $.table.columns[key as ColumnLabelKey])}
                </DropdownMenuCheckboxItem>
              ))}
            </DropdownMenuGroup>
          )}
          {visibleProperties.length > 0 && (
            <>
              {systemColumns.length > 0 && <DropdownMenuSeparator />}
              <DropdownMenuGroup>
                <DropdownMenuLabel>
                  {t(($) => $.table.columns.property_section)}
                </DropdownMenuLabel>
                {visibleProperties.map((property) => {
                  const key = `property:${property.id}` as const;
                  return (
                    <DropdownMenuCheckboxItem
                      key={property.id}
                      checked={selected.has(key)}
                      onCheckedChange={() => toggleTableColumn(key)}
                    >
                      <span className="truncate">{property.name}</span>
                    </DropdownMenuCheckboxItem>
                  );
                })}
              </DropdownMenuGroup>
            </>
          )}
          {systemColumns.length === 0 && visibleProperties.length === 0 && (
            <p className="px-2 py-6 text-center text-caption text-muted-foreground">
              {t(($) => $.table.columns.no_results)}
            </p>
          )}
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * 表格顶部的搜索输入框：带放大镜图标，非空时显示一键清空按钮。
 */
export function TableIssueSearch({
  value,
  onChange,
  placeholder,
  clearLabel,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  clearLabel: string;
}) {
  return (
    <div className="relative w-56 shrink-0">
      <Search
        aria-hidden
        className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
      />
      <Input
        type="text"
        role="searchbox"
        inputMode="search"
        value={value}
        onChange={(event) => onChange(event.target.value)}
        aria-label={placeholder}
        placeholder={placeholder}
        className="h-7 pl-7 pr-7 text-caption"
      />
      {value && (
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label={clearLabel}
          onClick={() => onChange("")}
          className="absolute right-0.5 top-0.5 text-muted-foreground"
        >
          <X className="size-3" />
        </Button>
      )}
    </div>
  );
}

/**
 * 表格标题格的内联渲染：默认显示编号 + 标题（可点击打开 issue），有子任务时带
 * 折叠箭头与缩进；重命名状态下显示可提交的输入框。点击外部"保存并关闭"时需吞掉
 * 那次 click（避免误触发行导航），详见下文 `gestureStartedWhileEditingRef` 注释。
 */
export function InlineTitle({
  row,
  editing,
  onEditingChange,
  onUpdate,
  onOpen,
  onCreateSubIssue,
  onToggleParent,
  toggleLabel,
  renameLabel,
  createSubIssueLabel,
}: {
  row: Extract<IssueTableDisplayRow, { kind: "issue" }>;
  /** 重命名状态由表格统一持有（同一时刻只有一个编辑器在编辑），这样它既能跨越
   *  单元格被虚拟化重挂载，又能驱动表格结构的"冻结"。 */
  editing: boolean;
  onEditingChange: (editing: boolean) => void;
  onUpdate: (updates: Partial<UpdateIssueRequest>) => void;
  /** 进入 issue —— 点击标题是进入详情的主要入口。 */
  onOpen: (event: React.MouseEvent) => void;
  onCreateSubIssue: () => void;
  onToggleParent: () => void;
  toggleLabel: string;
  renameLabel: string;
  createSubIssueLabel: string;
}) {
  const [draft, setDraft] = useState(row.issue.title);
  const editingRef = useRef(editing);
  editingRef.current = editing;
  // 当"某一次手势的 mousedown 到 click 之间"、且该手势开始时重命名输入框正开着时，
  // 此标记为真。onBlur 会在引发 blur 的那次 click 落下之前同步提交并关掉 `editing`，
  // 所以只以当前 `editing` 值为键的守卫到 click 时已经失效，那次"提交式点击"会冒泡
  // 进行的导航（甚至命中标题自己的打开处理器）：点别处想保存重命名，结果把 issue
  // 打开了 (MUL-5108 review R1#2)。
  const gestureStartedWhileEditingRef = useRef(false);

  useEffect(() => {
    // 实时/缓存快照应刷新被动展示的标签，但绝不能覆盖用户正在输入的文本。
    if (!editingRef.current) setDraft(row.issue.title);
  }, [row.issue.title]);

  const commit = () => {
    const title = draft.trim();
    onEditingChange(false);
    if (title && title !== row.issue.title) onUpdate({ title });
    else setDraft(row.issue.title);
  };

  return (
    <div
      className="group/title relative flex min-w-0 items-center gap-1.5"
      style={{ paddingLeft: row.depth * 18 }}
      // 记录手势是否在编辑状态中开始（mousedown 先于触发提交的 blur），然后在捕获
      // 阶段吞掉这次 click——别让它到达行（导航）或标题的打开处理器。
      // 编辑状态外开始的手势原样放行，所以点击空白处依然能打开 issue。
      onMouseDownCapture={() => {
        gestureStartedWhileEditingRef.current = editingRef.current;
      }}
      onClickCapture={(event) => {
        if (editing || gestureStartedWhileEditingRef.current) {
          event.stopPropagation();
        }
        gestureStartedWhileEditingRef.current = false;
      }}
      onAuxClickCapture={(event) => {
        if (editing) event.stopPropagation();
      }}
    >
      {row.hasChildren ? (
        <button
          type="button"
          aria-label={toggleLabel}
          className="rounded p-0.5 text-muted-foreground hover:bg-accent"
          onClick={(event) => {
            event.stopPropagation();
            onToggleParent();
          }}
          onAuxClick={stopRowNavigation}
        >
          {row.collapsed ? (
            <ChevronRight className="size-3.5" />
          ) : (
            <ChevronDown className="size-3.5" />
          )}
        </button>
      ) : (
        <span className="w-4 shrink-0" />
      )}
      <span className="min-w-16 shrink-0 text-caption text-muted-foreground">
        {row.issue.identifier}
      </span>
      <IssueAgentActivityIndicator issueId={row.issue.id} />
      <IssueReviewBadge issue={row.issue} />
      {editing ? (
        <Input
          autoFocus
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          onBlur={commit}
          onKeyDown={(event) => {
            if (event.key === "Enter") commit();
            if (event.key === "Escape") {
              setDraft(row.issue.title);
              onEditingChange(false);
            }
          }}
          className="h-7 min-w-0 flex-1 px-2"
        />
      ) : (
        <>
          <button
            type="button"
            className="min-w-0 flex-1 truncate text-left hover:underline"
            onClick={(event) => {
              event.stopPropagation();
              onOpen(event);
            }}
          >
            {row.issue.title}
          </button>
          {/* 像 SidebarMenuAction 那样把它从 flex 流里提出来。若按行内布局，这两个
            * 按钮会为"悬停才可见"的状态白占标题列约 40px——而标题正是最挤的一列。
            * 渐变让下方文字淡出，而不是让图标叠在文字上；侧栏不需要渐变是因为它的
            * 标签都很短，标题却会一直延伸到格子边缘。focus-within 让它们在不发生
            * 悬停时也能用键盘触达。 */}
          {/* 渐变必须跟随单元格此刻被涂上的颜色；悬停中的行并非静止底色——固定列
            * 在悬停时会切到 muted 混合色，所以渐变要跟着走。 */}
          {/* 以标题格为锚而不是整行：这些按钮作用于标题；从行上任取一点都会把它们
            * 放到"指针正在别处"的位置。渐变色仍跟随整行的悬停色，因为它们出现时
            * 单元格正是被那种颜色涂着的。 */}
          <span className="pointer-events-none absolute inset-y-0 right-0 flex items-center gap-0.5 bg-gradient-to-l from-background from-70% to-transparent pr-1 pl-8 opacity-0 transition-opacity group-hover:from-[color-mix(in_oklab,var(--muted)_50%,var(--background))] group-hover/title:pointer-events-auto group-hover/title:opacity-100 focus-within:pointer-events-auto focus-within:opacity-100">
            <button
              type="button"
              aria-label={createSubIssueLabel}
              className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
              onClick={(event) => {
                event.stopPropagation();
                onCreateSubIssue();
              }}
              onAuxClick={stopRowNavigation}
            >
              <Plus className="size-3" />
            </button>
            <button
              type="button"
              aria-label={renameLabel}
              className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
              onClick={(event) => {
                event.stopPropagation();
                setDraft(row.issue.title);
                onEditingChange(true);
              }}
              onAuxClick={stopRowNavigation}
            >
              <Pencil className="size-3" />
            </button>
          </span>
        </>
      )}
    </div>
  );
}

function LazyLabelCell({
  issue,
  open,
  onOpenChange,
}: {
  issue: Issue;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("issues");
  const labels = issue.labels ?? [];
  if (open) {
    return (
      <div onClick={stopRowNavigation} onAuxClick={stopRowNavigation}>
        <LabelPicker
          issueId={issue.id}
          open
          onOpenChange={(next) => {
            if (!next) onOpenChange(false);
          }}
          triggerRender={<button type="button" className="flex max-w-full gap-1" />}
        />
      </div>
    );
  }
  return (
    <button
      type="button"
      className="flex max-w-full items-center gap-1 overflow-hidden rounded px-1 py-0.5 hover:bg-accent"
      onClick={(event) => {
        event.stopPropagation();
        onOpenChange(true);
      }}
      onAuxClick={stopRowNavigation}
    >
      {labels.length > 0 ? (
        <>
          {labels.slice(0, 2).map((label) => (
            <LabelChip key={label.id} label={label} />
          ))}
          {labels.length > 2 && (
            <span className="text-caption text-muted-foreground">+{labels.length - 2}</span>
          )}
        </>
      ) : (
        <span className="text-muted-foreground">{t(($) => $.table.empty_value)}</span>
      )}
    </button>
  );
}

// 继承 <tr> 的 props，让虚拟化器打在返回行上的测量 ref 与 data-index（DataTable 会
// 把它们克隆到 renderRow 的返回值上）能落到元素上、而不是被这里吞掉。组头比数据行矮，
// 所以它正是这套测量机制为之存在的行型。
type IssueTableGroupRowProps = React.ComponentProps<"tr"> & {
  group: Extract<IssueTableDisplayRow, { kind: "group" }>;
  colSpan: number;
  onToggle: () => void;
};

/**
 * 分组标题行：显示折叠箭头 + 分组名 + 计数，整行可点击切换折叠。
 */
export function IssueTableGroupRow({
  group,
  colSpan,
  onToggle,
  ...rowProps
}: IssueTableGroupRowProps) {
  return (
    <TableRow
      {...rowProps}
      className="bg-muted/40 hover:bg-muted/60"
      onClick={onToggle}
    >
      <TableCell colSpan={colSpan} className="h-9 px-4 py-1.5">
        <button
          type="button"
          className="sticky left-4 flex w-fit items-center gap-2 text-caption font-medium"
        >
          {group.collapsed ? (
            <ChevronRight className="size-3.5" />
          ) : (
            <ChevronDown className="size-3.5" />
          )}
          {group.label}
          <span className="font-normal tabular-nums text-muted-foreground">
            {group.count}
          </span>
        </button>
      </TableCell>
    </TableRow>
  );
}

function propertyDisplayValue(
  property: IssueProperty,
  value: IssuePropertyValue | undefined,
  // actor 值是 "<kind>:<uuid>" 形式的引用；没有解析器它们会以原始 id 导出，
  // 所以能导出 actor 列的调用方必须传一个。
  getActorName?: (type: string, id: string) => string,
) {
  if (value === undefined) return "";
  const options = property.config.options ?? [];
  if (property.type === "select") {
    return options.find((option) => option.id === value)?.name ?? "";
  }
  if (property.type === "multi_select") {
    const ids = Array.isArray(value) ? value : [];
    return options
      .filter((option) => ids.includes(option.id))
      .map((option) => option.name)
      .join(", ");
  }
  if (isActorPropertyType(property.type)) {
    return actorRefsFromValue(value)
      .map((ref) => (getActorName ? getActorName(ref.kind, ref.id) : formatActorRef(ref.kind, ref.id)))
      .join(", ");
  }
  return String(value);
}

/**
 * 供下方模块级单元格/表头组件使用的"渲染期上下文"，挂在 `table.options.meta` 上。
 * 这些渲染器必须是引用稳定的模块级组件：TanStack 的 flexRender 会把函数形态的
 * `cell`/`header` 当作 React 组件来挂载，因此若渲染器是"任何查表数据变化就重建"的
 * 闭包（childProgressMap 随每次实时刷新、propertyById、actor 名称……），每次刷新都
 * 等于换了一种元素类型，React 会重挂载每个单元格——把打开的选择器弹层关掉、
 * 把进行中的草稿丢掉，仅仅因为工作区活动刷新了一下窗口 (MUL-5108)。
 * 数据改从 meta 流过，元素类型因此永不改变。
 */
type TableViewMeta = {
  childProgressMap: Map<string, ChildProgress>;
  propertyById: Map<string, IssueProperty>;
  properties: IssueProperty[];
  visibleIssueIds: string[];
  /** 正在打开编辑弹层/重命名输入框的单元格，记作 `${row.key}:${column.id}`，
   *  没有则为 null。归 TableView 所有，让"打开中的编辑器"能扛过单元格重挂载，
   *  并在它打开期间冻结表格结构。 */
  editingCellKey: string | null;
  setEditingCellKey: (key: string | null) => void;
  /** 收的是 ISSUE 而非它的 id：run-confirm 门槛要读它的状态类别与归属者，
   *  才能判断这次写入是否需要先确认。 */
  updateIssue: (issue: Issue, updates: Partial<UpdateIssueRequest>) => void;
  openIssue: (issue: Issue, event?: React.MouseEvent) => void;
  createSubIssue: (issue: Issue) => void;
  toggleTableParentCollapsed: (issueId: string) => void;
  handleIssueSelection: (issueId: string, shiftKey: boolean) => void;
  getActorName: (actorType: string, actorId: string) => string;
  columnLabel: (key: TableColumnKey) => string;
  sortBy: SortField;
  sortDirection: "asc" | "desc";
  onSort: (field: SortField, direction: "asc" | "desc") => void;
  toggleTableColumn: (key: TableColumnKey) => void;
};

function getTableViewMeta(
  table: TanstackTable<IssueTableDisplayRow>,
): TableViewMeta {
  return table.options.meta as unknown as TableViewMeta;
}

/**
 * 当拥有某个"被提升到表格层"的编辑键的单元格卸载时，释放该键。
 *
 * 行虚拟化（见 data-table.tsx）会把滚出渲染窗口的行连同它的单元格一起卸载。
 * Base UI 在卸载时不会调用 onOpenChange(false)，所以没有这个机制的话，打开中的
 * picker 的键——以及以它为键的整份冻结行结构——会在锚点行离开视口后仍然残留：
 * 表格一直冻结着，把行滚回来又会悄悄重新打开 picker、丢掉正在编辑的重命名草稿
 * (MUL-5108 review R1#3)。仅当这个正在卸载的单元格仍持有该键时清掉它，
 * 结构随之解冻、编辑器随之关闭。
 *
 * 活动值通过 ref 读取，让空依赖的清理函数永远看得到当前键/setter。初次挂载时单元格
 * 绝不可能是活动编辑器（编辑器由更晚的交互打开，而那种交互不会重挂载单元格），所以
 * 这里绝不会误触发——包括 StrictMode 的 mount → unmount → mount 探测：它的首次清理
 * 看到的 `editingCellKey` 仍不等于本单元格的键。
 */
export function useReleaseEditingCellOnUnmount(
  cellKey: string | null,
  editingCellKey: string | null,
  setEditingCellKey: (key: string | null) => void,
) {
  const editingCellKeyRef = useRef(editingCellKey);
  editingCellKeyRef.current = editingCellKey;
  const setEditingCellKeyRef = useRef(setEditingCellKey);
  setEditingCellKeyRef.current = setEditingCellKey;
  useEffect(() => {
    return () => {
      if (cellKey !== null && editingCellKeyRef.current === cellKey) {
        setEditingCellKeyRef.current(null);
      }
    };
  }, [cellKey]);
}

function IssueTableSelectHeader({
  table,
}: HeaderContext<IssueTableDisplayRow, unknown>) {
  const meta = getTableViewMeta(table);
  const { t } = useT("issues");
  return (
    <SelectAllCheckbox
      issueIds={meta.visibleIssueIds}
      label={t(($) => $.table.select_all)}
    />
  );
}

function IssueTableSelectCell({
  row,
  table,
}: CellContext<IssueTableDisplayRow, unknown>) {
  const meta = getTableViewMeta(table);
  const selection = useIssueSurfaceSelection();
  const { t } = useT("issues");
  if (row.original.kind !== "issue") return null;
  const issue = row.original.issue;
  return (
    <IssueCheckbox
      checked={selection.selectedIds.has(issue.id)}
      label={t(($) => $.table.select_issue, { identifier: issue.identifier })}
      onToggle={(shiftKey) => meta.handleIssueSelection(issue.id, shiftKey)}
    />
  );
}

function IssueTableAddColumnHeader({
  table,
}: HeaderContext<IssueTableDisplayRow, unknown>) {
  const meta = getTableViewMeta(table);
  const { t } = useT("issues");
  return (
    <TableColumnPicker
      properties={meta.properties}
      trigger={
        <button
          type="button"
          aria-label={t(($) => $.table.columns.add)}
          className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          <Plus className="size-3.5" />
        </button>
      }
    />
  );
}

function IssueTableEmptyCell() {
  return null;
}

function IssueTableHeaderCell({
  column,
  table,
}: HeaderContext<IssueTableDisplayRow, unknown>) {
  const meta = getTableViewMeta(table);
  const { t } = useT("issues");
  const key = column.id as TableColumnKey;
  const propertyId = propertyIdFromViewKey(key);
  const property = propertyId ? meta.propertyById.get(propertyId) : undefined;
  const staticSort = propertyId
    ? property &&
      !["multi_select", "checkbox", "actor", "multi_actor"].includes(property.type)
      ? (`property:${propertyId}` as SortField)
      : undefined
    : SORTABLE_COLUMNS[key as TableSystemColumnKey];
  const label = meta.columnLabel(key);
  return (
    <SortableColumnHeader
      columnKey={key}
      label={label}
      sortField={staticSort}
      sortBy={meta.sortBy}
      sortDirection={meta.sortDirection}
      onSort={meta.onSort}
      onHide={key === "title" ? undefined : () => meta.toggleTableColumn(key)}
      ascendingLabel={t(($) => $.table.sort_ascending)}
      descendingLabel={t(($) => $.table.sort_descending)}
      hideLabel={t(($) => $.table.columns.hide)}
      reorderLabel={t(($) => $.table.columns.reorder, { column: label })}
    />
  );
}

function IssueTableBodyCell({
  row,
  column,
  table,
}: CellContext<IssueTableDisplayRow, unknown>) {
  const meta = getTableViewMeta(table);
  const { t, i18n } = useT("issues");
  // 计算 cellKey（并注册卸载响应）须在提前 return 之前完成，
  // 保证 issue/group 两种行之间的 hook 顺序稳定。
  const cellKey =
    row.original.kind === "issue" ? `${row.original.key}:${column.id}` : null;
  useReleaseEditingCellOnUnmount(
    cellKey,
    meta.editingCellKey,
    meta.setEditingCellKey,
  );
  // 占位行也走普通单元格渲染器，从而继承真实的列宽、固定与边框——数据还没到，网格就
  // 已经对了，之后换行进来不会引起任何位移。
  if (row.original.kind === "skeleton") {
    return <Skeleton className="h-3.5 w-full" />;
  }
  if (row.original.kind !== "issue") return null;
  const issueRow = row.original;
  const issue = issueRow.issue;
  const key = column.id as TableColumnKey;
  const editorOpen = meta.editingCellKey === cellKey;
  const setEditorOpen = (open: boolean) =>
    meta.setEditingCellKey(open ? cellKey : null);
  const onUpdate = (updates: Partial<UpdateIssueRequest>) =>
    meta.updateIssue(issue, updates);

  const propertyId = propertyIdFromViewKey(key);
  if (propertyId) {
    const property = meta.propertyById.get(propertyId);
    if (!property) return null;
    return (
      <div onClick={stopRowNavigation} onAuxClick={stopRowNavigation}>
        <CustomPropertyValueEditor
          issue={issue}
          property={property}
          open={editorOpen}
          onOpenChange={setEditorOpen}
        />
      </div>
    );
  }
  switch (key) {
    case "title":
      return (
        <InlineTitle
          row={issueRow}
          editing={editorOpen}
          onEditingChange={setEditorOpen}
          onUpdate={onUpdate}
          onOpen={(event) => meta.openIssue(issue, event)}
          onCreateSubIssue={() => meta.createSubIssue(issue)}
          onToggleParent={() => meta.toggleTableParentCollapsed(issue.id)}
          toggleLabel={t(($) => $.table.toggle_sub_issues)}
          renameLabel={t(($) => $.table.rename_title)}
          createSubIssueLabel={t(($) => $.actions.create_sub_issue)}
        />
      );
    case "identifier":
      return (
        <span className="text-caption text-muted-foreground">{issue.identifier}</span>
      );
    case "status":
      return (
        <div onClick={stopRowNavigation} onAuxClick={stopRowNavigation}>
          <StatusPicker
            status={issue.status}
            onUpdate={onUpdate}
            align="start"
            open={editorOpen}
            onOpenChange={setEditorOpen}
          />
        </div>
      );
    case "priority":
      return (
        <div onClick={stopRowNavigation} onAuxClick={stopRowNavigation}>
          <PriorityPicker
            priority={issue.priority}
            onUpdate={onUpdate}
            align="start"
            open={editorOpen}
            onOpenChange={setEditorOpen}
          />
        </div>
      );
    case "assignee":
      return (
        <div onClick={stopRowNavigation} onAuxClick={stopRowNavigation}>
          <AssigneePicker
            assigneeType={issue.assignee_type}
            assigneeId={issue.assignee_id}
            onUpdate={onUpdate}
            align="start"
            open={editorOpen}
            onOpenChange={setEditorOpen}
          />
        </div>
      );
    case "labels":
      return (
        <LazyLabelCell
          issue={issue}
          open={editorOpen}
          onOpenChange={setEditorOpen}
        />
      );
    case "project":
      return (
        <div onClick={stopRowNavigation} onAuxClick={stopRowNavigation}>
          <ProjectPicker
            projectId={issue.project_id}
            onUpdate={onUpdate}
            open={editorOpen}
            onOpenChange={setEditorOpen}
            triggerRender={
              <button
                type="button"
                className="flex max-w-full items-center gap-1.5 rounded px-1 py-0.5 hover:bg-accent"
              />
            }
          />
        </div>
      );
    case "start_date":
      return (
        <div onClick={stopRowNavigation} onAuxClick={stopRowNavigation}>
          <StartDatePicker
            startDate={issue.start_date}
            onUpdate={onUpdate}
            open={editorOpen}
            onOpenChange={setEditorOpen}
          />
        </div>
      );
    case "due_date":
      return (
        <div onClick={stopRowNavigation} onAuxClick={stopRowNavigation}>
          <DueDatePicker
            dueDate={issue.due_date}
            onUpdate={onUpdate}
            open={editorOpen}
            onOpenChange={setEditorOpen}
          />
        </div>
      );
    case "created_at":
    case "updated_at":
      return (
        <span className="text-caption text-muted-foreground">
          {new Intl.DateTimeFormat(i18n.language, {
            month: "short",
            day: "numeric",
            year: "numeric",
          }).format(new Date(issue[key]))}
        </span>
      );
    case "child_progress": {
      const progress = meta.childProgressMap.get(issue.id);
      return progress ? (
        <span className="inline-flex items-center gap-1.5 text-caption text-muted-foreground">
          <ProgressRing done={progress.done} total={progress.total} size={15} />
          {progress.done}/{progress.total}
        </span>
      ) : (
        <span className="text-muted-foreground">{t(($) => $.table.empty_value)}</span>
      );
    }
    case "creator":
      return (
        <span className="flex min-w-0 items-center gap-1.5">
          <ActorAvatar
            actorType={issue.creator_type}
            actorId={issue.creator_id}
            size="sm"
          />
          <span className="truncate">
            {meta.getActorName(issue.creator_type, issue.creator_id)}
          </span>
        </span>
      );
  }
  return null;
}

/**
 * 表格视图主组件：聚合"按需激活的服务端分支"数据，编排成
 * group / issue / load_more / skeleton 等展示行，交给下方受控的 DataTable 渲染。
 * 同时持有列显隐/排序/分组等 view store 状态，并提供编辑、选择、导出等回调。
 */
export function TableView({
  serverQuery,
  childProgressMap,
  search,
  onSearchChange,
  onLoadedIssuesChange,
  onCreateIssue,
  exportIssues,
  resolveExportLookups,
}: TableViewProps) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const resolveStatusLabel = useStatusLabel(wsId);
  const { entryOf } = useIssueStatuses(wsId);
  const openModal = useModalStore((s) => s.open);
  const queryClient = useQueryClient();
  const intentNavigate = useIntentNavigate();
  const paths = useWorkspacePaths();
  const actions = useIssueSurfaceActionsOptional();
  const selection = useIssueSurfaceSelection();
  const { getActorName } = useActorName();
  const {
    data: properties = [],
    isSuccess: propertyCatalogSettled,
  } = useQuery(propertyListOptions(wsId));
  const propertyById = useMemo(
    () => new Map(properties.map((property) => [property.id, property])),
    [properties],
  );
  const activePropertyIds = useMemo(
    () => new Set(properties.map((property) => property.id)),
    [properties],
  );
  const groupablePropertyIds = useMemo(
    () =>
      new Set(
        properties
          .filter((property) => ["select", "checkbox"].includes(property.type))
          .map((property) => property.id),
      ),
    [properties],
  );
  const tableColumns = useViewStore((state) => state.tableColumns);
  const toggleTableColumn = useViewStore((state) => state.toggleTableColumn);
  const reorderTableColumn = useViewStore((state) => state.reorderTableColumn);
  const setTableColumnWidth = useViewStore((state) => state.setTableColumnWidth);
  const tableGrouping = useViewStore((state) => state.tableGrouping);
  const setTableGrouping = useViewStore((state) => state.setTableGrouping);
  const tableCollapsedGroups = useViewStore((state) => state.tableCollapsedGroups);
  const toggleTableGroupCollapsed = useViewStore(
    (state) => state.toggleTableGroupCollapsed,
  );
  const tableCollapsedParents = useViewStore((state) => state.tableCollapsedParents);
  const toggleTableParentCollapsed = useViewStore(
    (state) => state.toggleTableParentCollapsed,
  );
  const tableHierarchy = useViewStore((state) => state.tableHierarchy);
  const sortBy = useViewStore((state) => state.sortBy);
  const setSortBy = useViewStore((state) => state.setSortBy);
  const sortDirection = useViewStore((state) => state.sortDirection);
  const setSortDirection = useViewStore((state) => state.setSortDirection);
  const [exporting, setExporting] = useState<"all" | "selected" | null>(null);
  const selectionAnchorRef = useRef<string | null>(null);
  // 当前打开着编辑弹层/重命名输入框的那一个单元格——见 TableViewMeta.editingCellKey。
  const [editingCellKey, setEditingCellKey] = useState<string | null>(null);

  const groupingPropertyId = propertyIdFromViewKey(tableGrouping);
  const effectiveTableGrouping =
    groupingPropertyId &&
    propertyCatalogSettled &&
    !groupablePropertyIds.has(groupingPropertyId)
      ? "none"
      : tableGrouping;
  useEffect(() => {
    if (
      !groupingPropertyId ||
      !propertyCatalogSettled ||
      groupablePropertyIds.has(groupingPropertyId)
    ) {
      return;
    }
    setTableGrouping("none");
    toast.info(t(($) => $.table.group_property_unavailable));
  }, [
    groupablePropertyIds,
    groupingPropertyId,
    propertyCatalogSettled,
    setTableGrouping,
    t,
  ]);

  const serverGroupSpec = useMemo(
    () => tableGroupSpec(effectiveTableGrouping),
    [effectiveTableGrouping],
  );
  const usesServerGrouping = serverGroupSpec.kind !== "none";
  // 项目分组行只携带项目 id；标题取自 surface 为这个分组预先拉好的共享项目查询。
  //
  // 读取 `data` 而不是在解构里给默认值：尚未落定的查询没有 data，`= []` 会让这份
  // memo 每次渲染都拿到新数组，从而让下方映射的每个消费者跟着一起抖动 (MUL-5477)。
  const groupProjectsQuery = useQuery({
    ...projectListOptions(wsId),
    enabled: serverGroupSpec.kind === "project",
  });
  const groupProjectMap = useMemo(
    () =>
      new Map(
        (groupProjectsQuery.data ?? []).map((project) => [project.id, project]),
      ),
    [groupProjectsQuery.data],
  );
  const serverGroupsRequestGroup =
    serverGroupSpec.kind === "none"
      ? ({ kind: "status" } as const)
      : serverGroupSpec;
  const serverGroupsQuery = useInfiniteQuery({
    ...issueTableGroupsOptions(
      wsId,
      serverQuery,
      serverGroupsRequestGroup,
    ),
    enabled: usesServerGrouping,
  });
  const {
    data: serverGroupsData,
    isPending: serverGroupsPending,
    isError: serverGroupsError,
    hasNextPage: hasNextServerGroupPage,
    isFetchingNextPage: fetchingNextServerGroupPage,
    refetch: refetchServerGroups,
    fetchNextPage: fetchNextServerGroupPage,
  } = serverGroupsQuery;
  useEffect(() => {
    const body =
      serverGroupsQuery.error instanceof ApiError &&
      serverGroupsQuery.error.body &&
      typeof serverGroupsQuery.error.body === "object"
        ? (serverGroupsQuery.error.body as { error?: unknown })
        : null;
    if (
      serverGroupsQuery.error instanceof ApiError &&
      serverGroupsQuery.error.status === 422 &&
      body?.error === "unsupported_group"
    ) {
      setTableGrouping("none");
      toast.info(t(($) => $.table.group_property_unavailable));
    }
  }, [serverGroupsQuery.error, setTableGrouping, t]);
  const serverGroups = useMemo(
    () => serverGroupsData?.pages.flatMap((page) => page.groups) ?? [],
    [serverGroupsData?.pages],
  );
  const serverIdentity = useMemo(
    () => JSON.stringify([serverQuery, serverGroupSpec, tableHierarchy]),
    [serverGroupSpec, serverQuery, tableHierarchy],
  );
  const serverStructureIdentity = useMemo(
    () => JSON.stringify([serverGroupSpec, tableHierarchy]),
    [serverGroupSpec, tableHierarchy],
  );
  const collapsedGroupSet = useMemo(
    () => new Set(tableCollapsedGroups),
    [tableCollapsedGroups],
  );
  const collapsedParentSet = useMemo(
    () => new Set(tableCollapsedParents),
    [tableCollapsedParents],
  );
  const [serverBranchState, setServerBranchState] =
    useState<ServerBranchState>({
      identity: "",
      structureIdentity: "",
      branches: new Map(),
    });

  const rebasedServerBranchState = useMemo(
    () =>
      rebaseServerBranchState(
        serverBranchState,
        serverIdentity,
        serverStructureIdentity,
        usesServerGrouping,
      ),
    [
      serverBranchState,
      serverIdentity,
      serverStructureIdentity,
      usesServerGrouping,
    ],
  );

  // 渲染后再把同步的 rebase 结果提交进 state。消费者立刻使用上方那个派生状态，
  // 所以筛选/搜索/排序切换之间绝不会出现"空一帧"等待这个 effect 追上状态。
  useEffect(() => {
    if (rebasedServerBranchState !== serverBranchState) {
      setServerBranchState(rebasedServerBranchState);
    }
  }, [rebasedServerBranchState, serverBranchState]);

  const activeServerBranches = rebasedServerBranchState.branches;
  const serverBranchPlaceholderRef = useRef(
    new Map<string, IssueTableRowsResponse>(),
  );
  const serverBranchPageTargets = useMemo<ServerBranchPageTarget[]>(
    () =>
      [...activeServerBranches.values()].flatMap((branch) =>
        branch.cursors.map((cursor) => ({ branch, cursor })),
      ),
    [activeServerBranches],
  );
  // useQueries 会在一个 effect 里比较并安装传入的查询列表。保持这个数组引用稳定，
  // 可避免已落定的分支在每次渲染时都被重新安装（否则一旦虚拟化表格开始测量行高，
  // 就可能形成渲染死循环）。
  const serverBranchQueries = useMemo(
    () =>
      serverBranchPageTargets.map(({ branch, cursor }) => {
        const placeholder =
          cursor === null
            ? serverBranchPlaceholderRef.current.get(
                `${serverStructureIdentity}:${branch.key}`,
              )
            : undefined;
        return {
          ...issueTableRowPageOptions(wsId, {
            query: serverQuery,
            group: serverGroupSpec,
            group_key: branch.groupKey,
            hierarchy: { enabled: tableHierarchy },
            parent_id: branch.parentId,
            page: { limit: 50, cursor },
          }),
          // QueriesObserver 按查询 hash 替换 observer，所以单靠 keepPreviousData
          // 无法弥合 useQueries 里 table query 的变化。按结构分支保留最后一个已
          // 落定的头部响应，在新查询挂起期间继续把旧表画出来。
          //
          // 以"值"而非"闭包"传入。QueryObserver 只有在 `options.placeholderData`
          // 与上一次渲染的值按引用相等时，才会复用上一份 placeholder 结果——
          // `() => placeholder` 每次重建该数组都是全新的箭头函数，会迫使 placeholder
          // 被反复计算、结果被反复派生。这里的值来自 ref Map、本就稳定，而闭包形态
          // 又忽略函数形态收到的两个参数，所以两种写法等价 (MUL-5477)。
          ...(placeholder ? { placeholderData: placeholder } : {}),
          enabled:
            (branch.groupKey === null ||
              !collapsedGroupSet.has(branch.groupKey)) &&
            !branch.ancestorIds.some((id) => collapsedParentSet.has(id)),
        };
      }),
    [
      collapsedGroupSet,
      collapsedParentSet,
      serverBranchPageTargets,
      serverGroupSpec,
      serverQuery,
      serverStructureIdentity,
      tableHierarchy,
      wsId,
    ],
  );
  const combineServerBranchQueries = useCallback(
    (results: Array<UseQueryResult<IssueTableRowsResponse, Error>>) => {
      const byBranch: Record<string, ServerBranchData> = {};
      for (let index = 0; index < serverBranchPageTargets.length; index += 1) {
        const target = serverBranchPageTargets[index];
        const result = results[index];
        if (!target || !result) continue;
        const current = byBranch[target.branch.key] ?? {
          rows: [],
          total: 0,
          nextCursor: null,
          headUpdatedAt: 0,
          headFetching: false,
          loading: false,
          error: false,
          placeholder: false,
        };
        const page = result.data;
        if (page) {
          current.rows.push(...page.rows);
          if (target.cursor === null) current.total = page.total;
          current.nextCursor = page.next_cursor;
        }
        if (target.cursor === null) {
          current.headUpdatedAt = result.dataUpdatedAt;
          current.headFetching = result.isFetching;
        }
        current.loading ||= result.isPending || result.isFetching;
        current.error ||= result.isError;
        current.placeholder ||= result.isPlaceholderData;
        byBranch[target.branch.key] = current;
      }
      return byBranch;
    },
    [serverBranchPageTargets],
  );
  // `combine` 会被 React Query 做结构共享。它必须只返回普通对象/数组：Map 实例和
  // 每次新建的 retry 闭包无法共享，会让 useQueries 每次渲染发布不同的快照。
  const serverBranchData = useQueries({
    queries: serverBranchQueries,
    combine: combineServerBranchQueries,
  });

  useEffect(() => {
    const next = new Map<string, IssueTableRowsResponse>();
    for (const branch of activeServerBranches.values()) {
      const key = `${serverStructureIdentity}:${branch.key}`;
      const data = serverBranchData[branch.key];
      if (!data || data.placeholder || data.loading || data.error) {
        const previous = serverBranchPlaceholderRef.current.get(key);
        if (previous) next.set(key, previous);
        continue;
      }
      next.set(key, {
        query_fingerprint: "__table_placeholder__",
        group_key: branch.groupKey,
        parent_id: branch.parentId,
        total: data.total,
        rows: data.rows,
        branch_total: data.rows.length,
        next_cursor: null,
      });
    }
    // 把占位结果限定在当前活跃的结构图上。排序/筛选/搜索切换会复用这些条目；
    // 旧的组/属性配置不会在 Table 组件存活期内无限堆积。
    serverBranchPlaceholderRef.current = next;
  }, [
    activeServerBranches,
    serverBranchData,
    serverStructureIdentity,
  ]);

  // 实时重挂父级后，从当前行图重新推导祖先链。分支键是父级 id，因此一条既有分支可能
  // 被挪到一个新的折叠祖先下——别让视口哨兵把它重新激活。
  useEffect(() => {
    const desiredAncestors = new Map<string, string[]>();
    const visited = new Set<string>();
    const visit = (
      groupKey: string | null,
      parentId: string | null,
      ancestors: string[],
    ) => {
      const key = serverBranchKey(groupKey, parentId);
      if (visited.has(key)) return;
      visited.add(key);
      desiredAncestors.set(key, ancestors);
      for (const row of serverBranchData[key]?.rows ?? []) {
        if (row.direct_child_count > 0) {
          visit(groupKey, row.issue.id, [...ancestors, row.issue.id]);
        }
      }
    };

    if (usesServerGrouping) {
      for (const group of serverGroups) visit(group.key, null, []);
    } else {
      visit(null, null, []);
    }

    setServerBranchState((previous) => {
      if (previous.identity !== serverIdentity) return previous;
      let branches: Map<string, ServerBranch> | null = null;
      for (const [key, ancestors] of desiredAncestors) {
        const branch = previous.branches.get(key);
        if (!branch || sameStringArray(branch.ancestorIds, ancestors)) continue;
        branches ??= new Map(previous.branches);
        branches.set(key, { ...branch, ancestorIds: ancestors });
      }
      return branches ? { ...previous, branches } : previous;
    });
  }, [serverBranchData, serverGroups, serverIdentity, usesServerGrouping]);

  const activateServerBranch = useCallback(
    (
      groupKey: string | null,
      parentId: string | null,
      ancestorIds: string[],
    ) => {
      setServerBranchState((previous) => {
        if (previous.identity !== serverIdentity) return previous;
        const key = serverBranchKey(groupKey, parentId);
        const existing = previous.branches.get(key);
        if (existing && sameStringArray(existing.ancestorIds, ancestorIds)) {
          return previous;
        }
        const branches = new Map(previous.branches);
        branches.set(
          key,
          existing
            ? { ...existing, ancestorIds }
            : {
                key,
                groupKey,
                parentId,
                ancestorIds,
                cursors: [null],
              },
        );
        return { ...previous, branches };
      });
    },
    [serverIdentity],
  );

  // 一次宽泛的 Table 失效会让每个活跃页都重新拉取。头部一开始刷新，之后的所有游标
  // 就都过时了——在它们的并发响应可能造成重复或缺失行之前，先把这些游标丢弃。
  // revision 检查是第二道防线：用于把刷新后的头部直接写回缓存、不暴露 fetching 帧
  // 的那些客户端。
  const branchHeadRevisionRef = useRef<Record<string, number>>({});
  useEffect(() => {
    const previousRevisions = branchHeadRevisionRef.current;
    const nextRevisions: Record<string, number> = {};
    const branchesToTrim = new Set<string>();
    for (const [key, branch] of activeServerBranches) {
      const revision = serverBranchData[key]?.headUpdatedAt ?? 0;
      if (revision === 0) continue;
      nextRevisions[key] = revision;
      const seen = previousRevisions[key];
      if (
        branch.cursors.length > 1 &&
        (serverBranchData[key]?.headFetching ||
          (seen !== undefined && seen !== revision))
      ) {
        branchesToTrim.add(key);
      }
    }
    // 只保留当前查询的活跃分支。这样能把 revision 记账限制在"长期会话 + 大量
    // 筛选/搜索身份"的范围内。
    branchHeadRevisionRef.current = nextRevisions;
    if (branchesToTrim.size === 0) return;

    setServerBranchState((previous) => {
      if (previous.identity !== serverIdentity) return previous;
      let branches: Map<string, ServerBranch> | null = null;
      for (const [key, branch] of previous.branches) {
        if (branchesToTrim.has(key)) {
          branches ??= new Map(previous.branches);
          branches.set(key, { ...branch, cursors: [null] });
        }
      }
      return branches ? { ...previous, branches } : previous;
    });
  }, [activeServerBranches, serverBranchData, serverIdentity]);

  const loadNextServerBranchPage = useCallback(
    (branchKey: string, cursor: string) => {
      setServerBranchState((previous) => {
        if (previous.identity !== serverIdentity) return previous;
        const branch = previous.branches.get(branchKey);
        if (!branch || branch.cursors.includes(cursor)) return previous;
        const branches = new Map(previous.branches);
        branches.set(branchKey, {
          ...branch,
          cursors: [...branch.cursors, cursor],
        });
        return { ...previous, branches };
      });
    },
    [serverIdentity],
  );

  const retryServerBranch = useCallback(
    (branchKey: string) => {
      const branch = activeServerBranches.get(branchKey);
      if (!branch) return;
      void queryClient.refetchQueries({
        queryKey: issueKeys.tableRows(
          wsId,
          serverQuery,
          serverGroupSpec,
          branch.groupKey,
          tableHierarchy,
          branch.parentId,
        ),
        exact: false,
        type: "active",
      });
    },
    [
      activeServerBranches,
      queryClient,
      serverGroupSpec,
      serverQuery,
      tableHierarchy,
      wsId,
    ],
  );

  const serverGroupLabel = useCallback(
    (descriptor: IssueTableGroupDescriptor) => {
      const value = descriptor.value;
      if (value.kind === "status") {
        // 一个分组就是一个状态 KEY，因此显示该状态自己的名字——自定义状态绝不能读成
        // 它的类别名。`resolveStatusLabel` 会回退到原始 key，这同时保证"由更新版本
        // 后端引入的状态"在已装客户端上仍可显示，而不是塌缩成 schema 兜底值或空标签 (MUL-6243)。
        return resolveStatusLabel(value.status);
      }
      if (value.kind === "assignee") {
        return value.actor
          ? getActorName(value.actor.type, value.actor.id)
          : t(($) => $.table.unassigned);
      }
      if (value.kind === "project") {
        if (!value.project_id) return t(($) => $.swimlane.no_project);
        // 查询解析不到的项目（已删除，或该成员不可见）读起来应像"不可用"——
        // 而不是它的原始 id。
        return (
          groupProjectMap.get(value.project_id)?.title ??
          t(($) => $.table.value_unavailable)
        );
      }
      if (value.kind === "parent") {
        if (value.value_state === "unset") {
          return t(($) => $.swimlane.no_parent);
        }
        return value.parent?.title ?? t(($) => $.table.value_unavailable);
      }
      if (value.value_state === "unset") return t(($) => $.table.no_value);
      if (value.value_state === "unavailable") {
        return t(($) => $.table.value_unavailable);
      }
      const property = propertyById.get(value.property_id);
      if (typeof value.value === "boolean") {
        return value.value
          ? t(($) => $.pickers.custom_property.true_label)
          : t(($) => $.pickers.custom_property.false_label);
      }
      return (
        property?.config.options?.find((option) => option.id === value.value)
          ?.name ?? String(value.value ?? "")
      );
    },
    [getActorName, groupProjectMap, propertyById, t],
  );

  const serverDisplayRows = useMemo<IssueTableDisplayRow[]>(() => {
    const result: IssueTableDisplayRow[] = [];
    const seenIssueIds = new Set<string>();
    const appendBranch = (
      groupKey: string | null,
      parentId: string | null,
      depth: number,
      ancestorIds: string[],
    ) => {
      const key = serverBranchKey(groupKey, parentId);
      const data = serverBranchData[key];
      if (!data) {
        const registered = activeServerBranches.has(key);
        result.push({
          kind: "load_more",
          key: `${registered ? "loading" : "activate"}:${key}`,
          state: registered ? "loading" : "has_more",
          total: 0,
          onLoad: registered
            ? undefined
            : () => activateServerBranch(groupKey, parentId, ancestorIds),
        });
        return;
      }
      if (data.rows.length === 0 && data.loading) {
        result.push({
          kind: "load_more",
          key: `loading:${key}`,
          state: "loading",
          total: 0,
        });
      }
      for (const row of data.rows) {
        // 实时移动可能让同一实体短暂同时存在于新旧两个分支的缓存里。只渲染第一个
        // 权威位置；重复 id 会带来重复的 React key 和重复的选中。
        if (seenIssueIds.has(row.issue.id)) continue;
        seenIssueIds.add(row.issue.id);
        const collapsed = collapsedParentSet.has(row.issue.id);
        result.push({
          kind: "issue",
          key: row.issue.id,
          issue: row.issue,
          depth,
          hasChildren: tableHierarchy && row.direct_child_count > 0,
          collapsed,
        });
        if (tableHierarchy && row.direct_child_count > 0 && !collapsed) {
          appendBranch(groupKey, row.issue.id, depth + 1, [
            ...ancestorIds,
            row.issue.id,
          ]);
        }
      }
      if (data.error) {
        result.push({
          kind: "load_more",
          key: `retry:${key}`,
          state: "error",
          total: data.total,
          onLoad: () => retryServerBranch(key),
        });
      } else if (data.nextCursor) {
        const nextCursor = data.nextCursor;
        result.push({
          kind: "load_more",
          key: `more:${key}:${nextCursor}`,
          state: data.loading ? "loading" : "has_more",
          total: data.total,
          onLoad: () => loadNextServerBranchPage(key, nextCursor),
        });
      } else if (data.rows.length > 0) {
        // 只有在"确实分页过"的分支上，抵达末尾才值得标记；footer 负责套用这条规则，
        // 所以这里无条件推进这一行，并带上 total 供它判断。
        result.push({
          kind: "load_more",
          key: `end:${key}`,
          state: "end",
          total: data.total,
        });
      }
    };

    if (usesServerGrouping) {
      for (const descriptor of serverGroups) {
        const collapsed = collapsedGroupSet.has(descriptor.key);
        result.push({
          kind: "group",
          key: descriptor.key,
          label: serverGroupLabel(descriptor),
          count: descriptor.count,
          collapsed,
        });
        if (!collapsed) appendBranch(descriptor.key, null, 0, []);
      }
    } else {
      appendBranch(null, null, 0, []);
    }
    if (
      usesServerGrouping &&
      serverGroups.length === 0 &&
      serverGroupsPending
    ) {
      result.push({
        kind: "load_more",
        key: "loading:groups",
        state: "loading",
        total: 0,
      });
    } else if (usesServerGrouping && serverGroupsError) {
      result.push({
        kind: "load_more",
        key: "retry:groups",
        state: "error",
        total: 0,
        onLoad: () => void refetchServerGroups(),
      });
    } else if (usesServerGrouping && hasNextServerGroupPage) {
      result.push({
        kind: "load_more",
        key: "more:groups",
        state: fetchingNextServerGroupPage ? "loading" : "has_more",
        total: 0,
        onLoad: () => void fetchNextServerGroupPage(),
      });
    }

    // 还没有任何数据落地、且仍有请求在途：用占位行铺满网格，而不是显示一行孤零零的
    // "Loading…"——那读起来更像空表而非加载中。
    const isColdLoad =
      !result.some((row) => row.kind === "issue") &&
      result.some((row) => row.kind === "load_more" && row.state === "loading");
    if (isColdLoad) {
      return Array.from({ length: SKELETON_ROW_COUNT }, (_, index) => ({
        kind: "skeleton" as const,
        key: `skeleton:${index}`,
      }));
    }

    return result;
  }, [
    collapsedGroupSet,
    collapsedParentSet,
    activeServerBranches,
    activateServerBranch,
    loadNextServerBranchPage,
    retryServerBranch,
    serverBranchData,
    serverGroupLabel,
    serverGroups,
    serverGroupsPending,
    serverGroupsError,
    hasNextServerGroupPage,
    fetchingNextServerGroupPage,
    refetchServerGroups,
    fetchNextServerGroupPage,
    tableHierarchy,
    usesServerGrouping,
  ]);

  const tableMembershipIdentity = useMemo(
    () =>
      JSON.stringify([
        serverQuery.scope,
        serverQuery.filters,
        serverQuery.search ?? "",
      ]),
    [serverQuery.filters, serverQuery.scope, serverQuery.search],
  );
  const authoritativeLoadedIssues = useMemo(() => {
    const byId = new Map<string, Issue>();
    for (const branch of Object.values(serverBranchData)) {
      // "上一份数据"占位结果在查询切换期间负责把旧表继续画出来，但它们不属于
      // 新的筛选/搜索窗口。
      if (branch.placeholder) continue;
      for (const row of branch.rows) byId.set(row.issue.id, row.issue);
    }
    return [...byId.values()];
  }, [serverBranchData]);
  const [loadedIssueState, setLoadedIssueState] = useState<LoadedIssueState>({
    membershipIdentity: tableMembershipIdentity,
    issues: new Map(),
  });
  useEffect(() => {
    setLoadedIssueState((previous) => {
      const reset = previous.membershipIdentity !== tableMembershipIdentity;
      const issues = reset
        ? new Map<string, Issue>()
        : new Map(previous.issues);
      let changed = reset;
      for (const issue of authoritativeLoadedIssues) {
        if (issues.get(issue.id) !== issue) {
          issues.set(issue.id, issue);
          changed = true;
        }
      }
      return changed
        ? { membershipIdentity: tableMembershipIdentity, issues }
        : previous;
    });
  }, [authoritativeLoadedIssues, tableMembershipIdentity]);
  const loadedIssues = useMemo(
    () =>
      loadedIssueState.membershipIdentity === tableMembershipIdentity
        ? [...loadedIssueState.issues.values()]
        : [],
    [
      loadedIssueState.issues,
      loadedIssueState.membershipIdentity,
      tableMembershipIdentity,
    ],
  );

  const visibleColumnConfigs = useMemo(
    () =>
      tableColumns.filter((column) => {
        const propertyId = propertyIdFromViewKey(column.key);
        return !propertyId || activePropertyIds.has(propertyId);
      }),
    [activePropertyIds, tableColumns],
  );

  // 单元格编辑器（picker 弹层 / 重命名输入框）打开期间，让行结构保持静止：服务端
  // 分支分页和实时刷新都可能重建或重排行列表，把锚点行移出虚拟化渲染窗口、关掉用户
  // 刚打开的弹层 (MUL-5108)。这份快照只冻结 ORDER；行里的 issue 对象继续追踪实时
  // 服务端查询数据，让打开中的编辑器能反映乐观更新。编辑器一关，实时结构立刻弹回来。
  // ref 写入刻意放在渲染期：快照必须与"打开 `editing` 的那一次渲染"同帧捕获，
  // 且两个分支在 StrictMode 双渲染下都是幂等的。
  const frozenRowsRef = useRef<IssueTableDisplayRow[] | null>(null);
  if (editingCellKey === null) frozenRowsRef.current = null;
  else if (frozenRowsRef.current === null)
    frozenRowsRef.current = serverDisplayRows;
  const frozenRows = frozenRowsRef.current;
  const issueById = useMemo(
    () => new Map(authoritativeLoadedIssues.map((issue) => [issue.id, issue])),
    [authoritativeLoadedIssues],
  );
  const displayRows = useMemo(
    () =>
      frozenRows && frozenRows !== serverDisplayRows
        ? refreshFrozenTableRows(frozenRows, issueById)
        : serverDisplayRows,
    [frozenRows, issueById, serverDisplayRows],
  );
  const visibleIssueIds = useMemo(
    () =>
      displayRows
        .filter((row): row is Extract<IssueTableDisplayRow, { kind: "issue" }> => row.kind === "issue")
        .map((row) => row.issue.id),
    [displayRows],
  );
  useEffect(() => {
    onLoadedIssuesChange(loadedIssues);
  }, [loadedIssues, onLoadedIssuesChange]);
  const selectedIssues = useMemo(
    () => loadedIssues.filter((issue) => selection.selectedIds.has(issue.id)),
    [loadedIssues, selection.selectedIds],
  );
  const handleIssueSelection = useCallback(
    (issueId: string, shiftKey: boolean) => {
      const range = shiftKey
        ? getIssueTableSelectionRange(
            visibleIssueIds,
            selectionAnchorRef.current,
            issueId,
          )
        : null;

      if (range) {
        if (selection.selectedIds.has(issueId)) selection.deselect(range);
        else selection.select(range);
        return;
      }

      selection.toggle(issueId);
      selectionAnchorRef.current = issueId;
    },
    [selection, visibleIssueIds],
  );

  useEffect(() => {
    if (selection.selectedIds.size === 0) selectionAnchorRef.current = null;
  }, [selection.selectedIds]);

  const columnLabel = useCallback(
    (key: TableColumnKey) => {
      const propertyId = propertyIdFromViewKey(key);
      if (propertyId) return propertyById.get(propertyId)?.name ?? t(($) => $.table.no_value);
      return t(($) => $.table.columns[key as ColumnLabelKey]);
    },
    [propertyById, t],
  );

  // 行内编辑和详情页 picker、右键菜单一样是单 issue 写入，所以也走同一个门槛：
  // 把 agent 拥有的 issue 从 backlog 类状态改出（进入运行态）的那次状态变更，
  // 必须先确认而不能单击即发 (MUL-6463)。
  const updateIssue = useCallback(
    (issue: Issue, updates: Partial<UpdateIssueRequest>) => {
      const intent = runConfirmIntent(issue, updates, { entryOf });
      if (intent) {
        openModal("issue-run-confirm", intent);
        return;
      }
      actions?.updateIssue(issue.id, updates);
    },
    [actions, entryOf, openModal],
  );

  const { data: issueWorkspaces = [] } = useQuery(workspaceListOptions());
  const openIssue = useCallback(
    (issue: Issue, event?: React.MouseEvent) => {
      // 标准链接语义：普通点击就地导航；修饰键 / 中键点击开新标签页。
      // 没有 event 的回调（键盘可达性场景）按普通点击处理。
      intentNavigate(
        (issueWorkspaces.find(w => w.id === issue.workspace_id) ? workspacePaths.workspace(issueWorkspaces.find(w => w.id === issue.workspace_id)!.slug) : paths).issueDetail(issue.id),
        event ? resolveClickIntent(event) : "push",
      );
    },
    [intentNavigate, paths, issueWorkspaces],
  );

  const createSubIssue = useCallback(
    (issue: Issue) =>
      onCreateIssue({
        parent_issue_id: issue.id,
        parent_issue_identifier: issue.identifier,
        ...(issue.project_id ? { project_id: issue.project_id } : {}),
      }),
    [onCreateIssue],
  );

  const onSort = useCallback(
    (field: SortField, direction: "asc" | "desc") => {
      setSortBy(field);
      setSortDirection(direction);
    },
    [setSortBy, setSortDirection],
  );

  // 每次渲染都给新对象没问题——单元格在渲染时通过 table.options.meta 读取它。
  // 真正绝不能每次渲染都变的是下方各列定义的组件身份。
  const viewMeta: TableViewMeta = {
    childProgressMap,
    propertyById,
    properties,
    visibleIssueIds,
    editingCellKey,
    setEditingCellKey,
    updateIssue,
    openIssue,
    createSubIssue,
    toggleTableParentCollapsed,
    handleIssueSelection,
    getActorName,
    columnLabel,
    sortBy,
    sortDirection,
    onSort,
    toggleTableColumn,
  };

  const columns = useMemo<ColumnDef<IssueTableDisplayRow>[]>(
    () => [
      {
        id: SELECT_COLUMN_ID,
        size: 44,
        minSize: 44,
        maxSize: 44,
        enableResizing: false,
        header: IssueTableSelectHeader,
        cell: IssueTableSelectCell,
      },
      ...visibleColumnConfigs.map((config): ColumnDef<IssueTableDisplayRow> => {
        const definition: ColumnDef<IssueTableDisplayRow> = {
          id: config.key,
          minSize: config.key === "title" ? 260 : 96,
          maxSize: 640,
          enableResizing: true,
          header: IssueTableHeaderCell,
          cell: IssueTableBodyCell,
        };
        if (config.width !== undefined) definition.size = config.width;
        return definition;
      }),
      {
        id: ADD_COLUMN_ID,
        size: 48,
        minSize: 48,
        maxSize: 48,
        enableResizing: false,
        header: IssueTableAddColumnHeader,
        cell: IssueTableEmptyCell,
      },
    ],
    [visibleColumnConfigs],
  );

  const columnSizing = useMemo<ColumnSizingState>(
    () =>
      Object.fromEntries(
        visibleColumnConfigs
          .filter((column) => column.width !== undefined)
          .map((column) => [column.key, column.width!]),
      ),
    [visibleColumnConfigs],
  );
  const handleColumnSizingChange = useCallback<OnChangeFn<ColumnSizingState>>(
    (updater) => {
      const next = typeof updater === "function" ? updater(columnSizing) : updater;
      for (const column of visibleColumnConfigs) {
        const width = next[column.key];
        if (width !== column.width) setTableColumnWidth(column.key, width);
      }
    },
    [columnSizing, setTableColumnWidth, visibleColumnConfigs],
  );

  const table = useReactTable({
    data: displayRows,
    columns,
    getRowId: (row) => row.key,
    getCoreRowModel: getCoreRowModel(),
    state: {
      columnSizing,
      columnPinning: { left: [SELECT_COLUMN_ID, "title"], right: [] },
    },
    meta: viewMeta as TableMeta<IssueTableDisplayRow>,
    onColumnSizingChange: handleColumnSizingChange,
    columnResizeMode: "onChange",
  });

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  const handleDragEnd = useCallback(
    ({ active, over }: DragEndEvent) => {
      if (!over || active.id === over.id) return;
      reorderTableColumn(active.id as TableColumnKey, over.id as TableColumnKey);
    },
    [reorderTableColumn],
  );

  const handleExport = async (mode: "all" | "selected") => {
    setExporting(mode);
    try {
      // 文件依赖的每份查表数据都必须在这里 AWAIT，而不是读渲染期的 hook 状态——
      // 冷启动或出错都应当让导出失败，而不是悄悄降级：目录未落定时用户的自定义
      // 列会从文件里消失；目录未落定时每个 actor 都会导出成 "Unknown *"（二轮评审
      // P2#4）。fetchQuery 失败会抛错，落到下面的 catch。
      const needsPropertyCatalog = tableColumns.some(
        (column) => propertyIdFromViewKey(column.key) !== null,
      );
      // fetchQuery 会绕过选项里的 `select`，所以这里手动解开响应。
      const exportProperties = needsPropertyCatalog
        ? (await queryClient.fetchQuery(propertyListOptions(wsId))).properties
        : [];
      const exportPropertyById = new Map(
        exportProperties.map((property) => [property.id, property]),
      );
      // 用"已解析的目录"过滤已配置列——只有定义真正消失（归档/删除）的属性列才掉出。
      const csvColumns = tableColumns.filter((column) => {
        const propertyId = propertyIdFromViewKey(column.key);
        return !propertyId || exportPropertyById.has(propertyId);
      });
      const needsActors = csvColumns.some((column) => {
        if (column.key === "assignee" || column.key === "creator") return true;
        const propertyId = propertyIdFromViewKey(column.key);
        const property = propertyId ? exportPropertyById.get(propertyId) : undefined;
        return property ? isActorPropertyType(property.type) : false;
      });
      const [rows, exportLookups, exportActorName] = await Promise.all([
        mode === "all" ? exportIssues() : Promise.resolve(selectedIssues),
        resolveExportLookups({
          projects: csvColumns.some((column) => column.key === "project"),
          childProgress: csvColumns.some(
            (column) => column.key === "child_progress",
          ),
        }),
        needsActors
          ? Promise.all([
              queryClient.fetchQuery(memberListOptions(wsId)),
              queryClient.fetchQuery(agentListOptions(wsId)),
              queryClient.fetchQuery(squadListOptions(wsId)),
            ]).then(([members, agents, squads]) =>
              buildActorNameResolver({ members, agents, squads }),
            )
          : Promise.resolve(getActorName),
      ]);
      const headers = csvColumns.map((column) => {
        const propertyId = propertyIdFromViewKey(column.key);
        if (propertyId) return exportPropertyById.get(propertyId)?.name ?? "";
        return columnLabel(column.key);
      });
      const csvRows = rows.map((issue) =>
        csvColumns.map((column) => {
          const propertyId = propertyIdFromViewKey(column.key);
          if (propertyId) {
            const property = exportPropertyById.get(propertyId);
            return property
              ? propertyDisplayValue(
                  property,
                  issue.properties[propertyId],
                  exportActorName,
                )
              : "";
          }
          switch (column.key) {
            case "title":
              return issue.title;
            case "identifier":
              return issue.identifier;
            case "status":
              return resolveStatusLabel(issue.status);
            case "priority":
              return t(($) => $.priority[issue.priority]);
            case "assignee":
              return issue.assignee_type && issue.assignee_id
                ? exportActorName(issue.assignee_type, issue.assignee_id)
                : "";
            case "labels":
              return issue.labels?.map((label) => label.name).join(", ") ?? "";
            case "project":
              return issue.project_id
                ? exportLookups.projectMap.get(issue.project_id)?.title ?? ""
                : "";
            case "start_date":
            case "due_date":
              return issue[column.key] ?? "";
            case "created_at":
            case "updated_at":
              return issue[column.key];
            case "child_progress": {
              const progress = exportLookups.childProgressMap.get(issue.id);
              return progress ? `${progress.done}/${progress.total}` : "";
            }
            case "creator":
              return exportActorName(issue.creator_type, issue.creator_id);
          }
          return "";
        }),
      );
      const csv = buildIssueTableCsv(headers, csvRows);
      const blob = new Blob(["\uFEFF", csv], { type: "text/csv;charset=utf-8" });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      const filenamePrefix = mode === "all" ? "issues" : "issues-selected";
      anchor.download = `${filenamePrefix}-${new Date().toISOString().slice(0, 10)}.csv`;
      anchor.click();
      URL.revokeObjectURL(url);
      toast.success(t(($) => $.table.export_success, { count: rows.length }));
    } catch (error) {
      toast.error(
        error instanceof Error &&
          !(error instanceof IssueTableExportIntegrityError) &&
          error.message
          ? error.message
          : t(($) => $.table.export_failed),
      );
    } finally {
      setExporting(null);
    }
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b px-3 py-1.5">
        <TableIssueSearch
          value={search}
          onChange={onSearchChange}
          placeholder={t(($) => $.table.search_placeholder)}
          clearLabel={t(($) => $.table.search_clear)}
        />
        <span className="mr-auto" />
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="ghost"
                size="sm"
                className="h-7"
                disabled={exporting !== null}
              >
                {exporting ? (
                  <Loader2 className="size-3.5 animate-spin" />
                ) : (
                  <Download className="size-3.5" />
                )}
                {t(($) => $.table.export)}
                <ChevronDown className="size-3" />
              </Button>
            }
          />
          <DropdownMenuContent align="end" className="w-52">
            <DropdownMenuItem onClick={() => void handleExport("all")}>
              <Download className="size-3.5" />
              {t(($) => $.table.export_all)}
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={selectedIssues.length === 0}
              onClick={() => void handleExport("selected")}
            >
              <Download className="size-3.5" />
              {t(($) => $.table.export_selected, {
                count: selectedIssues.length,
              })}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <DndContext
        sensors={sensors}
        collisionDetection={closestCenter}
        // 列只可能横向互换，表头不应跟着指针飞出自己所在的横条——与桌面标签栏对
        // 标签拖动的约束一致。
        modifiers={[restrictToHorizontalAxis]}
        // Modifier 只约束拖动的位移，不约束它的自动滚动——自动滚动读的是原始指针
        // 坐标：拖表头时手稍微飘几像素纵向，就会让下面的行跟着滚起来。y 归零，去掉
        // 这个手势根本用不到的轴。x 保留，因为比视口还宽的表格要靠它到达远处槽位；
        // 但默认的 0.2 意味着要滑入约五分之一宽的边距才触发，对宽表头来说太迟钝。
        autoScroll={{ threshold: { x: 0.05, y: 0 } }}
        onDragEnd={handleDragEnd}
      >
        <SortableContext
          items={visibleColumnConfigs.map((column) => column.key)}
          strategy={horizontalListSortingStrategy}
        >
          <DataTable
            table={table}
            virtualizeRows
            emptyMessage={t(($) => $.table.empty)}
            onRowClick={(row, event) => {
              if (row.original.kind === "issue") {
                openIssue(row.original.issue, event);
              }
            }}
            renderRow={(row) => {
              if (row.original.kind === "group") {
                return (
                  <IssueTableGroupRow
                    group={row.original}
                    colSpan={table.getVisibleLeafColumns().length}
                    onToggle={() => toggleTableGroupCollapsed(row.original.key)}
                  />
                );
              }
              if (row.original.kind === "load_more") {
                const loadMoreRow = row.original;
                return (
                  <TableRow className="hover:bg-transparent">
                    <TableCell
                      colSpan={table.getVisibleLeafColumns().length}
                      className="p-0"
                    >
                      {/* 与 Board / List / Swimlane 收尾用的同一个 footer。之前在
                        * 表格里手工另写一份，导致表格成了唯一一个"加载失败读起来像
                        * 灰色正文而非错误、分页分支到底了却一声不吭"的视图。这行只
                        * 供给它所在的那个单元格。 */}
                      <div className="sticky left-0 w-full">
                        <ListLoadMoreFooter
                          hasMore={
                            loadMoreRow.state === "loading" ||
                            loadMoreRow.state === "has_more"
                          }
                          isLoading={loadMoreRow.state === "loading"}
                          total={loadMoreRow.total}
                          onLoadMore={() => loadMoreRow.onLoad?.()}
                          isError={loadMoreRow.state === "error"}
                          onRetry={loadMoreRow.onLoad}
                        />
                      </div>
                    </TableCell>
                  </TableRow>
                );
              }
              return null;
            }}
            className="min-h-0 flex-1"
          />
        </SortableContext>
      </DndContext>
    </div>
  );
}
