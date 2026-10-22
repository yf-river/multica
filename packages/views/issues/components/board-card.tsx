"use client";

// 本文件：board-card —— 看板视图的卡片与拖拽包装。
// BoardCardContent 渲染问题卡内容（状态/优先级/负责人/日期/子进度等，各行
// 依显示选项按需出现）；DraggableBoardCard 用 dnd-kit 让它可拖拽排序、
// 可点进详情、可右键弹出动作菜单。优先级/负责人/日期在可编辑面上直接以
// picker 形式内联编辑。

import { useCallback, memo } from "react";
import { AppLink } from "../../navigation";
import { useSortable, defaultAnimateLayoutChanges } from "@dnd-kit/sortable";
import type { AnimateLayoutChanges } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import type { Issue, IssueProperty, Project, UpdateIssueRequest } from "@multica/core/types";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { propertyListOptions } from "@multica/core/properties";
import { CustomPropertyValueDisplay } from "./pickers/custom-property-picker";
import { descriptionPreview } from "./description-preview";
import { formatDateOnly, isPastDateOnly } from "@multica/core/issues/date";
import { CalendarClock, CalendarDays } from "lucide-react";
import { ActorAvatar } from "../../common/actor-avatar";
import { PropertyIcon } from "../../common/property-icon";
import { useIssuePaths } from "@multica/core/paths";
import { useActorName } from "@multica/core/workspace/hooks";
import { useLocale, useT, useTimeAgo } from "../../i18n";
import { ProjectIcon } from "../../projects/components/project-icon";
import { PriorityIcon } from "./priority-icon";
import { PriorityPicker, AssigneePicker, StartDatePicker, DueDatePicker } from "./pickers";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import { ProgressRing } from "./progress-ring";
import type { ChildProgress } from "./list-row";
import { IssueActionsContextMenu } from "../actions";
import { LabelChip } from "../../labels/label-chip";
import { IssueAgentActivityIndicator } from "./issue-agent-activity-indicator";
import { IssueReviewBadge } from "./issue-review-badge";
import { TAPDSourceBadge } from "./tapd-source-badge";
import { CustomStatusChip, useIsCustomStatus } from "./custom-status-chip";
import { useIssueSurfaceActionsOptional } from "../surface/actions-context";
function formatDate(date: string, locale: string): string {
  return formatDateOnly(date, { month: "short", day: "numeric" }, locale);
}

/** 阻止事件冒泡到链接/拖拽处理器（picker 内部点击不该触发卡片导航或拖拽）。 */
function PickerWrapper({ children, className }: { children: React.ReactNode; className?: string }) {
  const stop = (e: React.SyntheticEvent) => {
    e.stopPropagation();
    e.preventDefault();
  };
  return (
    <div onClick={stop} onMouseDown={stop} onPointerDown={stop} className={className}>
      {children}
    </div>
  );
}

/**
 * 看板问题卡的内容面（职责见文件头）。
 *
 * `issue` 是要渲染的问题；`editable` 打开内联编辑（各 picker 走 surface
 * actions 更新）；`childProgress` / `project` 有值时按显示选项补对应行。
 */
export const BoardCardContent = memo(function BoardCardContent({
  issue,
  editable = false,
  childProgress,
  project,
}: {
  issue: Issue;
  editable?: boolean;
  childProgress?: ChildProgress;
  project?: Project;
}) {
  const { t } = useT("issues");
  const locale = useLocale();
  const timeAgo = useTimeAgo();
  const storeProperties = useViewStore((s) => s.cardProperties);
  const cardPropertyIds = useViewStore((s) => s.cardPropertyIds);
  const cardWsId = useWorkspaceId();
  const { data: workspaceProperties = [] } = useQuery(propertyListOptions(cardWsId));
  // 显示选项里打开的自定义属性，按开关顺序排列，且只保留本问题确实带值的。
  const cardCustomProperties = cardPropertyIds
    .map((id) => workspaceProperties.find((p) => p.id === id))
    .filter((p): p is IssueProperty => !!p && issue.properties?.[p.id] !== undefined);
  const labels = issue.labels ?? [];

  const surfaceActions = useIssueSurfaceActionsOptional();
  const handleUpdate = useCallback(
    (updates: Partial<UpdateIssueRequest>) => {
      surfaceActions?.updateIssue(issue.id, updates, {
        errorMessage: t(($) => $.card.update_failed),
      });
    },
    [issue.id, surfaceActions, t],
  );
  const canEdit = editable && !!surfaceActions;

  const showPriority = storeProperties.priority;
  const showDescription = storeProperties.description && issue.description;
  const showAssigneeSection = storeProperties.assignee;
  const hasAssignee = !!issue.assignee_type && !!issue.assignee_id;
  const showStartDate = storeProperties.startDate && issue.start_date;
  const showDueDate = storeProperties.dueDate && issue.due_date;
  const showProject = storeProperties.project && project;
  const showChildProgress = storeProperties.childProgress && childProgress;
  const showLabels = storeProperties.labels && labels.length > 0;
  // 状态 chip 行只有状态 chip 一个成员、而它又决定不渲染时，这里避免渲染出
  // 一个空的 flex 容器。
  const showCustomStatus = useIsCustomStatus(issue.status);

  const showAssigneeName = showAssigneeSection && hasAssignee && !showStartDate && !showDueDate;
  const showUpdatedHint = showAssigneeName && !showChildProgress;
  const { getActorName } = useActorName();
  const assigneeName =
    showAssigneeName && issue.assignee_type && issue.assignee_id
      ? getActorName(issue.assignee_type, issue.assignee_id)
      : null;

  const priorityLabel = t(($) => $.priority[issue.priority]);
  const priorityIconNode = showPriority ? (
    canEdit ? (
      <PickerWrapper className="flex">
        <PriorityPicker
          priority={issue.priority}
          onUpdate={handleUpdate}
          triggerRender={
            <button
              type="button"
              aria-label={priorityLabel}
              className="inline-flex size-5 shrink-0 items-center justify-center rounded hover:bg-muted/60"
            >
              <PriorityIcon priority={issue.priority} />
            </button>
          }
        />
      </PickerWrapper>
    ) : (
      <span
        aria-label={priorityLabel}
        className="inline-flex size-5 shrink-0 items-center justify-center"
      >
        <PriorityIcon priority={issue.priority} />
      </span>
    )
  ) : null;

  // 父行给这个容器留下剩余空间；min-w-0 与 max-w-full 让内嵌 picker 触发器
  // 遵守该上限。
  const assigneeContainerClass = assigneeName
    ? "flex min-w-0 max-w-full items-center"
    : "inline-flex items-center";

  const assigneeInner = hasAssignee ? (
    <span className="flex min-w-0 max-w-full items-center gap-1.5">
      <ActorAvatar
        actorType={issue.assignee_type!}
        actorId={issue.assignee_id!}
        size="sm"
        enableHoverCard
        profileLink={false}
        className="shrink-0"
      />
      {assigneeName && (
        <span className="min-w-0 truncate text-caption text-foreground">{assigneeName}</span>
      )}
    </span>
  ) : (
    <span className="text-caption text-muted-foreground">{t(($) => $.pickers.assignee.trigger_unassigned)}</span>
  );

  const assigneeNode = showAssigneeSection ? (
    canEdit ? (
      <PickerWrapper className={assigneeContainerClass}>
        <AssigneePicker
          assigneeType={issue.assignee_type}
          assigneeId={issue.assignee_id}
          onUpdate={handleUpdate}
          trigger={assigneeInner}
        />
      </PickerWrapper>
    ) : (
      <span className={assigneeContainerClass}>{assigneeInner}</span>
    )
  ) : null;

  const showMetaRow = showAssigneeSection || showStartDate || showDueDate || showChildProgress;
  const showRightMeta = !!showStartDate || !!showDueDate || !!showChildProgress || showUpdatedHint;

  return (
    <div className="rounded-lg border-[0.5px] border-surface-border bg-surface py-3 px-2.5 shadow-[var(--surface-shadow)] transition-colors group-hover/card:border-foreground/15 group-hover/card:bg-surface-hover group-data-[popup-open]/card:border-foreground/15 group-data-[popup-open]/card:bg-surface-hover">
      {/* 第 1 行：优先级 + 标识符（左），agent 活动 + 负责人（右） */}
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 min-w-0">
          {priorityIconNode}
          <p className="text-caption text-muted-foreground truncate">{issue.identifier}</p>
        </div>
        <span className="flex shrink-0 items-center gap-1.5">
          <IssueReviewBadge issue={issue} />
          <IssueAgentActivityIndicator issueId={issue.id} />
        </span>
      </div>

      {/* 第 2 行：标题 */}
      <p className="mt-1 text-body font-medium leading-snug line-clamp-2">
        {issue.title}
        <TAPDSourceBadge issue={issue} variant="inline" />
      </p>

      {showDescription && (() => {
        const preview = descriptionPreview(issue.description!);
        if (!preview) return null;
        return (
          <p className="mt-1 text-caption text-muted-foreground line-clamp-1">
            {preview}
          </p>
        );
      })()}

      {/* Chip 行：状态 + 项目 + 标签 + 自定义属性值。
          状态 chip 只为 CUSTOM 状态渲染——列头已经点明了状态类别。（MUL-6243） */}
      {(showCustomStatus || showProject || showLabels || cardCustomProperties.length > 0) && (
        <div className="mt-1.5 flex items-center gap-1.5 flex-wrap">
          <CustomStatusChip status={issue.status} />
          {showProject && (
            <span className="inline-flex items-center gap-1 rounded-full bg-muted/60 px-1.5 py-0.5 text-micro text-muted-foreground max-w-[160px]">
              <ProjectIcon project={project} size="sm" />
              <span className="truncate">{project!.title}</span>
            </span>
          )}
          {showLabels && labels.map((label) => (
            <LabelChip key={label.id} label={label} />
          ))}
          {cardCustomProperties.map((property) => (
            <span
              key={property.id}
              className="inline-flex max-w-[160px] items-center gap-1 rounded-full bg-muted/60 px-1.5 py-0.5 text-micro text-muted-foreground"
            >
              <PropertyIcon property={property} className="size-3 text-micro" />
              <CustomPropertyValueDisplay property={property} value={issue.properties?.[property.id]} />
            </span>
          ))}
        </div>
      )}

      {/* Meta 行：负责人（左），开始日期、截止日期、子进度（右） */}
      {showMetaRow && (
        <div className="mt-2 flex items-center justify-between gap-2">
          {showAssigneeSection && (
            <div className="min-w-0 flex-1">
              {assigneeNode}
            </div>
          )}
          {showRightMeta && (
            <div className="ml-auto flex shrink-0 items-center gap-2">
              {showStartDate && (
                canEdit ? (
                  <PickerWrapper className="flex shrink-0">
                    <StartDatePicker
                      startDate={issue.start_date}
                      onUpdate={handleUpdate}
                      trigger={
                        <span className="flex items-center gap-1 text-caption text-muted-foreground">
                          <CalendarClock className="size-3" />
                          {formatDate(issue.start_date!, locale)}
                        </span>
                      }
                    />
                  </PickerWrapper>
                ) : (
                  <span className="flex shrink-0 items-center gap-1 text-caption text-muted-foreground">
                    <CalendarClock className="size-3" />
                    {formatDate(issue.start_date!, locale)}
                  </span>
                )
              )}
              {showDueDate && (
                canEdit ? (
                  <PickerWrapper className="flex shrink-0">
                    <DueDatePicker
                      dueDate={issue.due_date}
                      onUpdate={handleUpdate}
                      trigger={
                        <span
                          className={`flex items-center gap-1 text-caption ${
                            isPastDateOnly(issue.due_date)
                              ? "text-destructive"
                              : "text-muted-foreground"
                          }`}
                        >
                          <CalendarDays className="size-3" />
                          {formatDate(issue.due_date!, locale)}
                        </span>
                      }
                    />
                  </PickerWrapper>
                ) : (
                  <span
                    className={`flex shrink-0 items-center gap-1 text-caption ${
                      isPastDateOnly(issue.due_date)
                        ? "text-destructive"
                        : "text-muted-foreground"
                    }`}
                  >
                    <CalendarDays className="size-3" />
                    {formatDate(issue.due_date!, locale)}
                  </span>
                )
              )}
              {showChildProgress && (
                <div className="inline-flex shrink-0 items-center gap-1">
                  <ProgressRing done={childProgress!.done} total={childProgress!.total} size={14} />
                  <span className="text-micro text-muted-foreground tabular-nums font-medium">
                    {childProgress!.done}/{childProgress!.total}
                  </span>
                </div>
              )}
              {showUpdatedHint && (
                <span className="shrink-0 text-caption text-muted-foreground">
                  {t(($) => $.card.updated_ago, { time: timeAgo(issue.updated_at) })}
                </span>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
});

// 拖拽时关掉 dnd-kit 的默认布局动画：排序/拖动中不做位移动画，
// 只在放下后做常规布局过渡（避免把"正在拖"和"已放下"搅在一起）。
const animateLayoutChanges: AnimateLayoutChanges = (args) => {
  const { isSorting, wasDragging } = args;
  if (isSorting || wasDragging) return false;
  return defaultAnimateLayoutChanges(args);
};

/**
 * 可拖拽看板卡（职责见文件头）。
 *
 * `disableSorting` 为 true 时禁掉排序落点（仍可拖、不可与别的卡换位）；
 * 其余 props 原样透传给 BoardCardContent。
 */
export const DraggableBoardCard = memo(function DraggableBoardCard({
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
  const p = useIssuePaths(issue);
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
    <IssueActionsContextMenu issue={issue}>
      <div
        ref={setNodeRef}
        style={style}
        data-board-card=""
        {...attributes}
        {...listeners}
        className={`group/card ${isDragging ? "opacity-30" : ""}`}
      >
        <AppLink
          href={p.issueDetail(issue.id)}
          className={`group block transition-colors ${isDragging ? "pointer-events-none" : ""}`}
        >
          <BoardCardContent
            issue={issue}
            editable
            childProgress={childProgress}
            project={project}
          />
        </AppLink>
      </div>
    </IssueActionsContextMenu>
  );
});
