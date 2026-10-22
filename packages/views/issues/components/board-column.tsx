"use client";

// 本文件：BoardColumn —— 看板视图的一列（状态/项目/负责人/自定义属性分组列）。
// 列的滚动容器同时兼作 dnd-kit 投放区与 Virtuoso 的自定义滚动父级，卡片跨列拖拽
// 因此不受虚拟化影响。小列整列直出、大列走 Virtuoso 虚拟化（阈值与种子数量等见
// 下方常量），列尾页脚挂在 Virtuoso 的 Footer 槽位上以支持触底加载。
// 列表头含隐藏状态列菜单与"新建问题"按钮。

import { memo, useCallback, useMemo, useState, type ReactNode } from "react";
import { Virtuoso } from "react-virtuoso";
import { EyeOff, FolderMinus, MoreHorizontal, Plus, ScanEye, UserMinus } from "lucide-react";
import { useDroppable } from "@dnd-kit/core";
import { SortableContext, verticalListSortingStrategy } from "@dnd-kit/sortable";
import type {
  Issue,
  IssueAssigneeType,
  IssuePriority,
  IssueStatusCategory,
  Project,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from "@multica/ui/components/ui/dropdown-menu";
import { STATUS_CONFIG } from "@multica/core/issues/config";
import { useViewStoreApi } from "@multica/core/issues/stores/view-store-context";
import { useViewBaseline } from "../surface/view-baseline-context";
import { StatusHeading } from "./status-heading";
import { DraggableBoardCard } from "./board-card";
import { ReviewSettingsSheet } from "./review-settings-sheet";
import type { ChildProgress } from "./list-row";
import { useT } from "../../i18n";
import { ActorAvatar } from "../../common/actor-avatar";
import { ProjectIcon } from "../../projects/components/project-icon";
import { PriorityIcon } from "./priority-icon";
import { useRestoredScrollOffset, useRestoredScrollRef } from "../../platform";
import { DeferredPopup } from "../../common/deferred-popup";
import { DeferredTooltip } from "../../common/deferred-tooltip";
import { VirtuosoSeed } from "../../common/virtuoso-seed";
import type { IssueCreateDefaults } from "../surface/types";
import { PRIORITY_DISPLAY_ORDER } from "@multica/core/issues/config";
import { quadrantGroupId } from "../utils/drag-utils";

// 刻意不做"插入位置预测"。服务端 ORDER BY 用的是 PostgreSQL 的
// en_US.utf8 排序规则（glibc），JavaScript（ICU/V8）无法忠实复刻。
// 展示一个不准确的落点指示器，还不如不展示。

export const BOARD_COL_WIDTH = 280;
export const BOARD_CARD_WIDTH = BOARD_COL_WIDTH - 16 - 8; // 列(280) - 列内边距 p-2(16) - 投放区 p-1(8)

// 看板卡片高约 90-140px，约 10 张就能填满一列视口——不像通用
// VIRTUOSO_SEED_COUNT（30，按 36px 的列表行高算）。该种子会在每次 surface
// 重挂载时按列同步挂载，把种子调得过大，成本会直接乘进"切换页签"的耗时里
//（列数 × 种子条数 × 每张卡片的挂载开销）。
const BOARD_SEED_COUNT = 10;

// 卡片高度的中位数（含 8px pt-2 间距），只用于"预测测量"阶段的滚动高度：
// 种子的尾部占位块与 Virtuoso 的 defaultItemHeight 共享这个值，让从种子到
// Virtuoso 交接期间的总滚动高度（以及滚动条滑块）保持稳定，而不是在未种子化
// 的行突然被拉开间距时跳动。真实测量随后会修正它。
const BOARD_CARD_ESTIMATED_HEIGHT = 110;

// 等于或低于此数量时，列内每张卡片都直出渲染——没有 Virtuoso、没有种子交接、
// 没有预估高度。此时滚动高度永远是浏览器实测的真值，因此列滚动条在挂载、
// 路由返回或页签恢复时绝不会跳。虚拟化只在足够大的列上才划算（Linear 也是
// 按列拆分：小列 `data-virtual-cluster="false"`，大列用 padding-spacer 虚拟化）。
// ~30 ≈ 三屏卡片；这么大的列整列直出也不贵，因为每张卡片的浮层是惰性挂载的。
// 已知边界：一次"加载更多"把总数推过阈值时会把列切换成虚拟化路径，
// 滚动条可能调整一次——可接受，因为它只发生在列底部的滚动中途。
const BOARD_VIRTUALIZE_THRESHOLD = 30;

// 列没有页脚时传给 <Virtuoso components> 的对象。必须是稳定的对象，
// 绝不能是 `undefined`：显式的 `undefined` prop 会覆盖 react-virtuoso 内部
// 的 `{}` 默认值，其启动时对 `EmptyPlaceholder`/`Footer` 的解构会抛错（MUL-4474）。
const EMPTY_VIRTUOSO_COMPONENTS = {};

export interface BoardColumnGroup {
  id: string;
  title: string;
  /** 看板列是"状态类别"，绝不是原始状态 key。（MUL-6243） */
  status?: IssueStatusCategory;
  assigneeType?: IssueAssigneeType | null;
  assigneeId?: string | null;
  /** 该列对应的项目 id；null = "无项目"列。仅在看板按项目分组时设置。 */
  projectId?: string | null;
  /** 仅供列头图标显示。在"无项目"列、以及 projects 查询解析不到的项目上为 null。 */
  project?: Pick<Project, "icon"> | null;
  /** 看板按 select 型自定义属性分组时设置。 */
  propertyId?: string;
  /** 该列对应的选项 id；null = "无值"列。 */
  propertyOptionId?: string | null;
  propertyOptionColor?: string;
  totalCount?: number;
  createData?: IssueCreateDefaults;
}

export const BoardColumn = memo(function BoardColumn({
  group,
  issueIds,
  issueMap,
  childProgressMap,
  projectMap,
  totalCount,
  footer,
  projectId,
  onCreateIssue,
  sortLabel,
  quadrants = false,
}: {
  group: BoardColumnGroup;
  issueIds: string[];
  issueMap: Map<string, Issue>;
  childProgressMap?: Map<string, ChildProgress>;
  projectMap?: Map<string, Project>;
  totalCount?: number;
  footer?: ReactNode;
  /** 设置后，列上的"+"会在新建表单里预填该项目。 */
  projectId?: string;
  onCreateIssue?: (defaults: IssueCreateDefaults) => void;
  sortLabel?: string | null;
  quadrants?: boolean;
}) {
  const status = group.status;
  const cfg = status ? STATUS_CONFIG[status] : null;
  const [reviewSettingsOpen, setReviewSettingsOpen] = useState(false);
  const { setNodeRef, isOver } = useDroppable({ id: group.id });
  const viewStoreApi = useViewStoreApi();
  // 被已打开保存视图固定的状态不能在看板里被隐藏——那等于悄悄抽掉视图自身的
  // 一个条件。
  const viewBaseline = useViewBaseline();
  const statusFixedByView = !!status && viewBaseline?.status.has(status) === true;
  const { t } = useT("issues");

  // 把 id 解析成 Issue 对象，保留父组件给定的顺序。
  const resolvedIssues = useMemo(
    () =>
      issueIds.flatMap((id) => {
        const issue = issueMap.get(id);
        return issue ? [issue] : [];
      }),
    [issueIds, issueMap],
  );

  // 列的滚动容器同时兼任 dnd-kit 的投放区与 Virtuoso 的 customScrollParent，
  // 所以用一个合并回调 ref 把元素同时喂给两者。useDroppable 的 setNodeRef
  // 跨渲染稳定。把投放区放在常驻的滚动容器上（而不是每张卡片上），
  // 是跨列投放能扛住虚拟化的关键——只有容器内的卡片会进出 DOM。
  const [scrollEl, setScrollEl] = useState<HTMLDivElement | null>(null);
  // 基于"拉取"的滚动恢复（MUL-4741）：在 ref 挂载那一刻写入已保存的偏移
  //（种子 + 预估间隔块在首次提交时就给了列一个真实高度，赋值能赶在绘制前生效），
  // 并把同一偏移作为 Virtuoso 的初始位置喂进去。
  const scrollMementoKey = `board:${group.id}`;
  const restoredScrollTop = useRestoredScrollOffset(scrollMementoKey);
  const restoreScrollRef = useRestoredScrollRef(scrollMementoKey);
  const mergedRef = useCallback(
    (el: HTMLDivElement | null) => {
      setNodeRef(el);
      setScrollEl(el);
      restoreScrollRef(el);
    },
    [setNodeRef, restoreScrollRef],
  );
  // 无限滚动哨兵挂在 Virtuoso 的 Footer 槽位上，因此它位于虚拟化列表真正的末尾，
  // 滚到底时其 IntersectionObserver 依然会触发 loadMore。
  const footerComponents = useMemo(
    () => (footer ? { Footer: () => <>{footer}</> } : EMPTY_VIRTUOSO_COMPONENTS),
    [footer],
  );

  const computeItemKey = (_index: number, issue: Issue) => issue.id;
  const itemContent = (index: number, issue: Issue) => (
    // 除第一张外每张卡片都加 pt-2，复刻原来的 `space-y-2` 间距；
    // 用 padding（而非 margin）是因为它在 Virtuoso 的测量条目盒内部，
    // 高度计算才能保持正确。
    <div className={index === 0 ? undefined : "pt-2"}>
      <DraggableBoardCard
        issue={issue}
        childProgress={childProgressMap?.get(issue.id)}
        project={
          issue.project_id ? projectMap?.get(issue.project_id) : undefined
        }
        disableSorting={!!sortLabel}
      />
    </div>
  );

  return (
    <div style={{ width: BOARD_COL_WIDTH }} className={`flex shrink-0 flex-col rounded-xl ${cfg?.columnBg ?? "bg-muted/40"} p-2`}>
      <div className="mb-2 flex items-center justify-between px-1.5">
        <BoardGroupHeading group={group} count={totalCount ?? issueIds.length} />

        {/* 右侧：添加 + 菜单 */}
        <div className="flex items-center gap-1">
          {/* 列表头的浮层惰性挂载：看板/泳道每列都渲染一个表头，而这些菜单/提示
              几乎从不会被打开——急切挂载它们会主导 surface 的挂载成本
              （DeferredPopup / DeferredTooltip）。 */}
          {status && (
            <DeferredPopup
              ariaHasPopup="menu"
              triggerRender={
                <Button variant="ghost" size="icon-sm" className="rounded-full text-muted-foreground">
                  <MoreHorizontal className="size-3.5" />
                </Button>
              }
            >
              {(open, onOpenChange) => (
                <DropdownMenu open={open} onOpenChange={onOpenChange}>
                  <DropdownMenuTrigger
                    render={
                      <Button variant="ghost" size="icon-sm" className="rounded-full text-muted-foreground">
                        <MoreHorizontal className="size-3.5" />
                      </Button>
                    }
                  />
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem
                      disabled={statusFixedByView}
                      title={statusFixedByView ? t(($) => $.filters.in_view) : undefined}
                      onClick={() => viewStoreApi.getState().hideStatus(status)}
                    >
                      <EyeOff className="size-3.5" />
                      {t(($) => $.board.hide_column)}
                    </DropdownMenuItem>
                    {/* 审查配置只有一份，挂在「审核中」类别上；自定义的审核类列也走这里。 */}
                    {status === "in_review" && (
                      <DropdownMenuItem onClick={() => setReviewSettingsOpen(true)}>
                        <ScanEye className="size-3.5" />
                        {t(($) => $.review.settings)}
                      </DropdownMenuItem>
                    )}
                  </DropdownMenuContent>
                </DropdownMenu>
              )}
            </DeferredPopup>
          )}
          {onCreateIssue && (
            <DeferredTooltip
              content={t(($) => $.board.add_issue_tooltip)}
              trigger={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="rounded-full text-muted-foreground"
                  onClick={() => {
                    const data = {
                      ...(group.createData ?? {}),
                      ...(projectId ? { project_id: projectId } : {}),
                    };
                    onCreateIssue(data);
                  }}
                >
                  <Plus className="size-3.5" />
                </Button>
              }
            />
          )}
        </div>
      </div>
      <div className="relative min-h-[200px] flex-1 rounded-lg">
        {isOver && sortLabel && (
          <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-lg bg-background/40">
            <span className="rounded-md bg-popover px-2.5 py-1 text-caption font-medium text-popover-foreground shadow-sm border border-border">
              {sortLabel}
            </span>
          </div>
        )}
        <div
          ref={mergedRef}
          // 每列独立向"页签会话记忆"登记滚动位置（MUL-4741）：
          // 以 group id 作为稳定的记忆 key，因此每列滚动偏移在
          // 页签切换/重载后各自独立地恢复。
          data-tab-scroll-root={scrollMementoKey}
          className={`absolute inset-0 overflow-y-auto rounded-lg p-1 transition-colors ${
            isOver && sortLabel
              ? "ring-2 ring-brand/25 bg-accent/15"
              : isOver
                ? "bg-accent/60"
                : ""
          }`}
        >
          {quadrants ? (
            <div className="space-y-3">
              {PRIORITY_DISPLAY_ORDER.map((priority) => (
                <QuadrantSection
                  key={priority}
                  groupId={group.id}
                  priority={priority}
                  issues={resolvedIssues.filter((issue) => issue.priority === priority)}
                  childProgressMap={childProgressMap}
                  projectMap={projectMap}
                />
              ))}
              {footer}
            </div>
          ) : resolvedIssues.length > 0 ? (
            <SortableContext items={issueIds} strategy={verticalListSortingStrategy}>
              {resolvedIssues.length <= BOARD_VIRTUALIZE_THRESHOLD ? (
                /* 小列：整列直出（复用同一个 itemContent，因此与虚拟化行
                   逐字节相同）。无交接、无预估——见 BOARD_VIRTUALIZE_THRESHOLD。
                   页脚（无限滚动哨兵）渲染在流的真正末尾，其 IntersectionObserver
                   的运作方式与在 Virtuoso 的 Footer 槽位里一致。 */
                <>
                  <VirtuosoSeed
                    data={resolvedIssues}
                    itemContent={itemContent}
                    computeItemKey={computeItemKey}
                    count={resolvedIssues.length}
                  />
                  {footer}
                </>
              ) : scrollEl ? (
                <Virtuoso
                  customScrollParent={scrollEl}
                  data={resolvedIssues}
                  computeItemKey={computeItemKey}
                  initialScrollTop={restoredScrollTop}
                  initialItemCount={Math.min(resolvedIssues.length, BOARD_SEED_COUNT)}
                  defaultItemHeight={BOARD_CARD_ESTIMATED_HEIGHT}
                  increaseViewportBy={{ top: 300, bottom: 300 }}
                  components={footerComponents}
                  itemContent={itemContent}
                />
              ) : (
                /* 大列，重挂载后合并滚动 ref 尚未就位：先种子化一截真实卡片，
                   让列绝不以空白呈现；等 ref 落地后再挂载 Virtuoso，
                   并配以一致的 `initialItemCount` 以熬过测量帧（MUL-4750）。 */
                <VirtuosoSeed
                  data={resolvedIssues}
                  itemContent={itemContent}
                  computeItemKey={computeItemKey}
                  count={BOARD_SEED_COUNT}
                  estimatedItemHeight={BOARD_CARD_ESTIMATED_HEIGHT}
                />
              )}
            </SortableContext>
          ) : (
            <>
              {issueIds.length === 0 && (
                <p className="py-8 text-center text-caption text-muted-foreground">
                  {t(($) => $.board.empty_column)}
                </p>
              )}
              {footer}
            </>
          )}
        </div>
      </div>
      {status === "in_review" && (
        <ReviewSettingsSheet open={reviewSettingsOpen} onClose={() => setReviewSettingsOpen(false)} />
      )}
    </div>
  );
});

function QuadrantSection({
  groupId,
  priority,
  issues,
  childProgressMap,
  projectMap,
}: {
  groupId: string;
  priority: IssuePriority;
  issues: Issue[];
  childProgressMap?: Map<string, ChildProgress>;
  projectMap?: Map<string, Project>;
}) {
  const { t } = useT("issues");
  const { setNodeRef, isOver } = useDroppable({ id: quadrantGroupId(groupId, priority) });
  return (
    <section ref={setNodeRef} className={`rounded-md border p-2 ${isOver ? "bg-accent/60 ring-2 ring-brand/25" : "bg-background/40"}`}>
      <div className="mb-1 flex items-center justify-between gap-2">
        <span className="flex items-center gap-1.5 text-caption font-medium"><PriorityIcon priority={priority} />{t(($) => $.priority[priority])}</span>
        <span className="text-micro text-muted-foreground">{issues.length}</span>
      </div>
      {issues.length > 0 ? (
        <SortableContext items={issues.map((issue) => issue.id)} strategy={verticalListSortingStrategy}>
          <div className="space-y-2">
            {issues.map((issue) => (
              <DraggableBoardCard
                key={issue.id}
                issue={issue}
                childProgress={childProgressMap?.get(issue.id)}
                project={issue.project_id ? projectMap?.get(issue.project_id) : undefined}
              />
            ))}
          </div>
        </SortableContext>
      ) : (
        <p className="py-3 text-center text-micro text-muted-foreground">{t(($) => $.board.empty_column)}</p>
      )}
    </section>
  );
}

function BoardGroupHeading({
  group,
  count,
}: {
  group: BoardColumnGroup;
  count: number;
}) {
  if (group.status) {
    return <StatusHeading status={group.status} count={count} />;
  }

  if (group.propertyId !== undefined) {
    return (
      <div className="flex min-w-0 items-center gap-2">
        <span
          className="size-2.5 shrink-0 rounded-full bg-muted-foreground/30"
          style={group.propertyOptionColor ? { backgroundColor: group.propertyOptionColor } : undefined}
        />
        <span className="truncate text-body font-medium" title={group.title}>
          {group.title}
        </span>
        <span className="shrink-0 rounded-full bg-background px-1.5 py-0.5 text-micro font-medium tabular-nums text-muted-foreground">
          {count}
        </span>
      </div>
    );
  }

  if (group.projectId !== undefined) {
    return (
      <div className="flex min-w-0 items-center gap-2">
        {group.project ? (
          <ProjectIcon project={group.project} size="sm" />
        ) : (
          <span className="flex size-[18px] shrink-0 items-center justify-center rounded-full bg-background text-muted-foreground">
            <FolderMinus className="size-3.5" />
          </span>
        )}
        <span className="truncate text-body font-medium" title={group.title}>
          {group.title}
        </span>
        <span className="shrink-0 rounded-full bg-background px-1.5 py-0.5 text-micro font-medium tabular-nums text-muted-foreground">
          {count}
        </span>
      </div>
    );
  }

  const actorIcon =
    group.assigneeType && group.assigneeId ? (
      <ActorAvatar
        actorType={group.assigneeType}
        actorId={group.assigneeId}
        size="sm"
        showStatusDot={group.assigneeType === "agent"}
      />
    ) : (
      <span className="flex size-[18px] shrink-0 items-center justify-center rounded-full bg-background text-muted-foreground">
        <UserMinus className="size-3.5" />
      </span>
    );

  return (
    <div className="flex min-w-0 items-center gap-2">
      {actorIcon}
      <span className="truncate text-body font-medium" title={group.title}>
        {group.title}
      </span>
      <span className="shrink-0 rounded-full bg-background px-1.5 py-0.5 text-micro font-medium tabular-nums text-muted-foreground">
        {count}
      </span>
    </div>
  );
}
