"use client";

// 本文件：ListRow / DraggableListRow —— 列表视图的一行。
// ListRow 是纯展示行（不可拖拽），DraggableListRow 是 dnd-kit 可拖拽行，
// 两者共享同一份 ListRowContent：标题/自定义状态 chip/子任务进度/标签/卡片
// 自定义属性/项目/日期/负责人等字段是否展示，由视图的"卡片属性"开关控制。
// 行左端是复选框选区（hover 出现），整行包裹在 IssueActionsContextMenu 中，
// 右键可呼出常用动作菜单。

import { memo, type Ref } from "react";
import { useSortable, defaultAnimateLayoutChanges } from "@dnd-kit/sortable";
import type { AnimateLayoutChanges } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { AppLink } from "../../navigation";
import type { Issue, Project,
  IssueProperty,
} from "@multica/core/types";
import { formatDateOnly } from "@multica/core/issues/date";
import { ActorAvatar } from "../../common/actor-avatar";
import { PropertyIcon } from "../../common/property-icon";
import { useIssuePaths } from "@multica/core/paths";
import { useQuery } from "@tanstack/react-query";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import { useWorkspaceId } from "@multica/core/hooks";
import { propertyListOptions } from "@multica/core/properties";
import { CustomPropertyValueDisplay } from "./pickers/custom-property-picker";
import { ProjectIcon } from "../../projects/components/project-icon";
import { PriorityIcon } from "./priority-icon";
import { ProgressRing } from "./progress-ring";
import { IssueActionsContextMenu } from "../actions";
import { LabelChip } from "../../labels/label-chip";
import { CustomStatusChip } from "./custom-status-chip";
import { IssueAgentActivityIndicator } from "./issue-agent-activity-indicator";
import { IssueReviewBadge } from "./issue-review-badge";
import { TAPDSourceBadge } from "./tapd-source-badge";
import { useIssueSurfaceSelection } from "../surface/selection-context";
import { useLocale } from "../../i18n";

/** 子任务完成进度概要：已完成数与总数。用于行/卡片上的小进度环。 */
export interface ChildProgress {
  done: number;
  total: number;
}

function formatDate(date: string, locale: string): string {
  return formatDateOnly(date, { month: "short", day: "numeric" }, locale);
}

function ListRowContent({
  issue,
  childProgress,
  project,
  isDragging,
  containerRef,
  containerStyle,
  containerProps,
  checkboxProps,
}: {
  issue: Issue;
  childProgress?: ChildProgress;
  project?: Project;
  isDragging?: boolean;
  containerRef?: Ref<HTMLDivElement>;
  containerStyle?: React.CSSProperties;
  containerProps?: Record<string, unknown>;
  checkboxProps?: Pick<React.HTMLAttributes<HTMLDivElement>, "onClick" | "onMouseDown" | "onPointerDown">;
}) {
  const locale = useLocale();
  const selection = useIssueSurfaceSelection();
  const selected = selection.selectedIds.has(issue.id);
  const toggle = selection.toggle;
  const p = useIssuePaths(issue);
  const storeProperties = useViewStore((s) => s.cardProperties);
  const cardPropertyIds = useViewStore((s) => s.cardPropertyIds);
  const rowWsId = useWorkspaceId();
  const { data: workspaceProperties = [] } = useQuery(propertyListOptions(rowWsId));
  const cardCustomProperties = cardPropertyIds
    .map((id) => workspaceProperties.find((p) => p.id === id))
    .filter((p): p is IssueProperty => !!p && issue.properties?.[p.id] !== undefined);
  const labels = issue.labels ?? [];

  const showProject = storeProperties.project && project;
  const showChildProgress = storeProperties.childProgress && childProgress;
  const showAssignee = storeProperties.assignee && issue.assignee_type && issue.assignee_id;
  const showStartDate = storeProperties.startDate && issue.start_date;
  const showDueDate = storeProperties.dueDate && issue.due_date;
  const showLabels = storeProperties.labels && labels.length > 0;

  return (
    <IssueActionsContextMenu issue={issue}>
      <div
        ref={containerRef}
        style={containerStyle}
        {...containerProps}
        className={`group/row flex h-9 items-center gap-2 px-4 text-body transition-colors ${
          selected
            ? "bg-surface-selected hover:not-data-[popup-open]:bg-surface-selected data-[popup-open]:bg-surface-selected"
            : "hover:not-data-[popup-open]:bg-surface-hover data-[popup-open]:bg-surface-hover"
        } ${isDragging ? "opacity-30" : ""}`}
      >
        <div
          className="relative flex shrink-0 items-center justify-center w-4 h-4"
          {...checkboxProps}
        >
          <PriorityIcon
            priority={issue.priority}
            className={selected ? "hidden" : "group-hover/row:hidden"}
          />
          <input
            type="checkbox"
            checked={selected}
            onChange={() => toggle(issue.id)}
            className={`absolute inset-0 cursor-pointer accent-primary ${
              selected ? "" : "hidden group-hover/row:block"
            }`}
          />
        </div>
        <AppLink
          href={p.issueDetail(issue.id)}
          className={`flex flex-1 items-center gap-2 min-w-0 ${isDragging ? "pointer-events-none" : ""}`}
        >
          <span className="w-16 shrink-0 text-caption text-muted-foreground">
            {issue.identifier}
          </span>
          <IssueAgentActivityIndicator issueId={issue.id} />
          <IssueReviewBadge issue={issue} />

          <span className="flex min-w-0 flex-1 items-center gap-1.5">
            <span className="truncate">{issue.title}</span>
            <TAPDSourceBadge issue={issue} variant="inline" />
            {/* 列表的分区是"状态类别"，因此自定义状态需要在行上自报名号；
                内置状态保持静默。（MUL-6243） */}
            <CustomStatusChip status={issue.status} className="shrink-0" />
            {showChildProgress && (
              <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-muted/60 px-1.5 py-0.5">
                <ProgressRing done={childProgress!.done} total={childProgress!.total} size={14} />
                <span className="text-micro text-muted-foreground tabular-nums font-medium">
                  {childProgress!.done}/{childProgress!.total}
                </span>
              </span>
            )}
            {showLabels && (
              <span className="ml-1.5 hidden md:inline-flex shrink-0 items-center gap-1 max-w-[260px] overflow-hidden">
                {labels.slice(0, 3).map((label) => (
                  <LabelChip key={label.id} label={label} />
                ))}
                {labels.length > 3 && (
                  <span className="text-micro text-muted-foreground">
                    +{labels.length - 3}
                  </span>
                )}
              </span>
            )}
            {cardCustomProperties.length > 0 && (
              <span className="ml-1.5 hidden md:inline-flex shrink-0 items-center gap-1 max-w-[260px] overflow-hidden">
                {cardCustomProperties.slice(0, 3).map((property) => (
                  <span
                    key={property.id}
                    className="inline-flex max-w-[120px] items-center gap-1 rounded-full bg-muted/60 px-1.5 py-0.5 text-micro text-muted-foreground"
                  >
                    <PropertyIcon property={property} className="size-3 text-micro" />
                    <CustomPropertyValueDisplay property={property} value={issue.properties?.[property.id]} />
                  </span>
                ))}
              </span>
            )}
          </span>
          {showProject && (
            <span className="inline-flex shrink-0 items-center gap-1 text-caption text-muted-foreground max-w-[140px]">
              <ProjectIcon project={project} size="sm" />
              <span className="truncate">{project!.title}</span>
            </span>
          )}
          {showStartDate && (
            <span className="shrink-0 text-caption text-muted-foreground">
              {formatDate(issue.start_date!, locale)}
            </span>
          )}
          {showDueDate && (
            <span className="shrink-0 text-caption text-muted-foreground">
              {formatDate(issue.due_date!, locale)}
            </span>
          )}
          {showAssignee && (
            <ActorAvatar
              actorType={issue.assignee_type!}
              actorId={issue.assignee_id!}
              size="sm"
              enableHoverCard
            />
          )}
        </AppLink>
      </div>
    </IssueActionsContextMenu>
  );
}

/** 静态列表行（不可拖拽）。字段渲染由 `ListRowContent` 统一负责，
 *  本组件只做 memo 化的薄封装。 */
export const ListRow = memo(function ListRow({
  issue,
  childProgress,
  project,
}: {
  issue: Issue;
  childProgress?: ChildProgress;
  project?: Project;
}) {
  return (
    <ListRowContent
      issue={issue}
      childProgress={childProgress}
      project={project}
    />
  );
});

const animateLayoutChanges: AnimateLayoutChanges = (args) => {
  const { isSorting, wasDragging } = args;
  if (isSorting || wasDragging) return false;
  return defaultAnimateLayoutChanges(args);
};

const stopDrag = (e: React.SyntheticEvent) => {
  e.stopPropagation();
};

/** dnd-kit 可拖拽列表行：把拖拽的 transform/transition/监听器/aria 属性
 *  通过 container* 槽位交给 `ListRowContent`；`disableSorting` 时仅禁用
 *  droppable（行仍可被拖走、只是自己不是投放目标，用于跨状态列表场景）。
 *  复选框上的事件一律 stopPropagation，避免点选框时误触发拖拽。 */
export const DraggableListRow = memo(function DraggableListRow({
  issue,
  childProgress,
  project,
  disableSorting,
}: {
  issue: Issue;
  childProgress?: ChildProgress;
  project?: Project;
  disableSorting?: boolean;
}) {
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({
    id: issue.id,
    data: { status: issue.status },
    animateLayoutChanges,
    disabled: disableSorting ? { droppable: true } : undefined,
  });

  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
  };

  return (
    <ListRowContent
      issue={issue}
      childProgress={childProgress}
      project={project}
      isDragging={isDragging}
      containerRef={setNodeRef}
      containerStyle={style}
      containerProps={{ ...attributes, ...listeners }}
      checkboxProps={{ onClick: stopDrag, onMouseDown: stopDrag, onPointerDown: stopDrag }}
    />
  );
});
