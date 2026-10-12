"use client";

// 本文件：GanttView —— 问题列表的"甘特图视图"。
// 以时间作横轴、每个 issue 一行，用彩色条形展示 start_date ~ due_date 的跨度；
// 只有单边日期时画成菱形标记。支持 日/周/月 三档缩放与"显示已完成"开关，
// 行上可右键打开 issue 操作菜单。数据范围由调用方（surface）预过滤好——
// 本视图只负责排序与绘制，不决定哪些行存在。整个时间轴按 UTC 自然日对齐，
// 避免查看者时区让条形错位到别的日期上。

import { useEffect, useMemo, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssuePaths } from "@multica/core/paths";
import { useViewStore, useViewStoreApi } from "@multica/core/issues/stores/view-store-context";
import type { GanttZoom } from "@multica/core/issues/stores/view-store";
import { projectListOptions } from "@multica/core/projects/queries";
import type { Issue, IssueStatusCategory } from "@multica/core/types";
import { issueStatusCategory } from "@multica/core/issues";
import { dateOnlyToUTCDate } from "@multica/core/issues/date";
import { cn } from "@multica/ui/lib/utils";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from "@multica/ui/components/ui/tooltip";
import { Button } from "@multica/ui/components/ui/button";
import { AppLink } from "../../navigation";
import { ActorAvatar } from "../../common/actor-avatar";
import { ProjectIcon } from "../../projects/components/project-icon";
import { StatusIcon } from "./status-icon";
import { PriorityIcon } from "./priority-icon";
import { IssueActionsContextMenu } from "../actions";
import { sortIssues } from "../utils/sort";
import { useLocale, useT } from "../../i18n";

// ---------------------------------------------------------------------------
// 日期工具 —— 所有计算都以 UTC 自然日为对齐单位，这样任何一处产生的
// `due_date` ISO 字符串都能精确映射到时间轴上的某一列。
// ---------------------------------------------------------------------------

const MS_PER_DAY = 24 * 60 * 60 * 1000;

function startOfDayUTC(d: Date): Date {
  return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate()));
}

function addDays(d: Date, days: number): Date {
  return new Date(d.getTime() + days * MS_PER_DAY);
}

function daysBetween(a: Date, b: Date): number {
  return Math.round((b.getTime() - a.getTime()) / MS_PER_DAY);
}

// issue 的日期到达时是仅含日期的 "YYYY-MM-DD" 字符串（日历日）。把它们各自
// 锚定到对应的 UTC 零点，条形就能精确落在该日历日上，与查看者所在时区无关。
// 实现见 @multica/core/issues/date。
function parseDay(iso: string | null): Date | null {
  return dateOnlyToUTCDate(iso);
}

function isWeekendUTC(d: Date): boolean {
  const wd = d.getUTCDay();
  return wd === 0 || wd === 6;
}

function isMonthStartUTC(d: Date): boolean {
  return d.getUTCDate() === 1;
}

function isWeekStartUTC(d: Date): boolean {
  return d.getUTCDay() === 1; // 周一（一周的第一天）
}

// ---------------------------------------------------------------------------
// 几何常量
// ---------------------------------------------------------------------------

const ROW_HEIGHT = 36;
const HEADER_HEIGHT = 56;
const LEFT_COL_WIDTH = 320;

const DAY_PX_BY_ZOOM: Record<GanttZoom, number> = {
  day: 36,
  week: 14,
  month: 6,
};

interface Range {
  start: Date;
  end: Date;
}

function computeRange(issues: Issue[], today: Date, zoom: GanttZoom): Range {
  const defaultPad: Record<GanttZoom, number> = {
    day: 21,
    week: 60,
    month: 180,
  };
  let minTs = today.getTime() - defaultPad[zoom] * MS_PER_DAY;
  let maxTs = today.getTime() + defaultPad[zoom] * MS_PER_DAY;
  for (const i of issues) {
    const s = parseDay(i.start_date);
    const e = parseDay(i.due_date);
    if (s && s.getTime() < minTs) minTs = s.getTime();
    if (e && e.getTime() > maxTs) maxTs = e.getTime();
    if (s && s.getTime() > maxTs) maxTs = s.getTime();
    if (e && e.getTime() < minTs) minTs = e.getTime();
  }
  const pad = Math.max(2, Math.round(defaultPad[zoom] / 6));
  return {
    start: addDays(startOfDayUTC(new Date(minTs)), -pad),
    end: addDays(startOfDayUTC(new Date(maxTs)), pad + 1),
  };
}

// ---------------------------------------------------------------------------
// 顶部时间轴 —— 纵向滚动时吸顶。上面一行月份、下面一行日/周刻度。
// ---------------------------------------------------------------------------

function GanttAxis({
  range,
  dayPx,
  zoom,
  todayOffsetDays,
  width,
}: {
  range: Range;
  dayPx: number;
  zoom: GanttZoom;
  todayOffsetDays: number;
  width: number;
}) {
  const locale = useLocale();
  const totalDays = daysBetween(range.start, range.end);

  const monthBlocks = useMemo(() => {
    const out: { label: string; left: number; width: number }[] = [];
    let cursor = startOfDayUTC(range.start);
    while (cursor.getTime() < range.end.getTime()) {
      const monthEnd = new Date(
        Date.UTC(cursor.getUTCFullYear(), cursor.getUTCMonth() + 1, 1),
      );
      const blockEnd = monthEnd.getTime() > range.end.getTime() ? range.end : monthEnd;
      const startDays = daysBetween(range.start, cursor);
      const widthDays = daysBetween(cursor, blockEnd);
      out.push({
        label: cursor.toLocaleDateString(locale, {
          month: "short",
          year: "numeric",
          timeZone: "UTC",
        }),
        left: startDays * dayPx,
        width: widthDays * dayPx,
      });
      cursor = monthEnd;
    }
    return out;
  }, [range, dayPx, locale]);

  return (
    <div
      className="relative shrink-0 border-b bg-background"
      style={{ height: HEADER_HEIGHT, width }}
    >
      {/* 月份行 */}
      <div className="relative h-7 border-b">
        {monthBlocks.map((b, i) => (
          <div
            key={i}
            className="absolute top-0 bottom-0 flex items-center px-2 text-caption font-medium text-foreground"
            style={{ left: b.left, width: b.width }}
          >
            {b.width > 40 && <span className="truncate">{b.label}</span>}
          </div>
        ))}
      </div>
      {/* 日 / 周刻度行 */}
      <div className="relative h-7">
        {Array.from({ length: totalDays }, (_, i) => {
          const date = addDays(range.start, i);
          const isMonth = isMonthStartUTC(date);
          const isWeek = isWeekStartUTC(date);
          const showLabel =
            zoom === "day" ||
            (zoom === "week" && isWeek) ||
            (zoom === "month" && isMonth);
          return (
            <div
              key={i}
              className={cn(
                "absolute top-0 bottom-0 flex items-center justify-center text-micro text-muted-foreground border-l",
                isMonth
                  ? "border-foreground/15"
                  : isWeek
                  ? "border-foreground/10"
                  : "border-foreground/5",
              )}
              style={{ left: i * dayPx, width: dayPx }}
            >
              {showLabel && (
                <div className="flex flex-col items-center leading-tight">
                  {zoom === "day" && (
                    <>
                      <span className="tabular-nums">{date.getUTCDate()}</span>
                      <span className="text-micro">
                        {date.toLocaleDateString(locale, {
                          weekday: "short",
                          timeZone: "UTC",
                        })}
                      </span>
                    </>
                  )}
                  {zoom === "week" && (
                    <span className="tabular-nums">{date.getUTCDate()}</span>
                  )}
                  {zoom === "month" && (
                    <span className="tabular-nums whitespace-nowrap">
                      {date.toLocaleDateString(locale, {
                        month: "short",
                        day: "numeric",
                        timeZone: "UTC",
                      })}
                    </span>
                  )}
                </div>
              )}
            </div>
          );
        })}
        {todayOffsetDays >= 0 && todayOffsetDays <= totalDays && (
          <div
            className="absolute top-0 bottom-0 w-px bg-brand"
            style={{ left: todayOffsetDays * dayPx }}
          />
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 背景层 —— 周末底色、周/月网格线与今日线。只铺一次，贯穿整条时间轨道的高度、
// 垫在所有条形图后面。
// ---------------------------------------------------------------------------

function BackgroundLayer({
  range,
  dayPx,
  height,
  todayOffsetDays,
}: {
  range: Range;
  dayPx: number;
  height: number;
  todayOffsetDays: number;
}) {
  const totalDays = daysBetween(range.start, range.end);
  return (
    <div
      className="pointer-events-none absolute inset-0"
      style={{ height, width: totalDays * dayPx }}
    >
      {Array.from({ length: totalDays }, (_, i) => {
        const date = addDays(range.start, i);
        const weekend = isWeekendUTC(date);
        const isMonth = isMonthStartUTC(date);
        const isWeek = isWeekStartUTC(date);
        return (
          <div
            key={i}
            className="absolute top-0 bottom-0"
            style={{ left: i * dayPx, width: dayPx }}
          >
            {weekend && <div className="absolute inset-0 bg-muted/40" />}
            {(isMonth || isWeek) && (
              <div
                className={cn(
                  "absolute top-0 bottom-0 left-0 w-px",
                  isMonth ? "bg-foreground/10" : "bg-foreground/5",
                )}
              />
            )}
          </div>
        );
      })}
      {todayOffsetDays >= 0 && todayOffsetDays <= totalDays && (
        <div
          className="absolute top-0 bottom-0 w-px bg-brand/70"
          style={{ left: todayOffsetDays * dayPx }}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// 条形图按状态取色（用 Tailwind 语义 token，而非写死的色值）。
// ---------------------------------------------------------------------------

// 以"类别"（CATEGORY）为键，而不是状态键：处于自定义状态的 issue，其条形
// 按它归属的类别着色。当初以 IssueStatus 为键时，每个自定义状态键查出来的
// 都是 undefined，条形图直接整个丢色 (MUL-6243)。
const STATUS_BAR_BG: Record<IssueStatusCategory, string> = {
  backlog: "bg-muted-foreground/60",
  todo: "bg-muted-foreground/70",
  in_progress: "bg-warning",
  in_review: "bg-success",
  done: "bg-info",
  blocked: "bg-destructive",
  cancelled: "bg-muted-foreground/40",
};

// ---------------------------------------------------------------------------
// 单行 —— 左侧吸顶的标签格 + 右侧时间轨道（条形绝对定位在其上）。
// ---------------------------------------------------------------------------

function ScheduledRow({
  issue,
  range,
  dayPx,
  totalDays,
}: {
  issue: Issue;
  range: Range;
  dayPx: number;
  totalDays: number;
}) {
  const { t } = useT("issues");
  const locale = useLocale();
  const p = useIssuePaths(issue);
  const wsId = useWorkspaceId();
  const { data: projects = [] } = useQuery({
    ...projectListOptions(wsId),
    enabled: !!issue.project_id,
  });
  const project = issue.project_id ? projects.find((pr) => pr.id === issue.project_id) : undefined;

  const start = parseDay(issue.start_date);
  const due = parseDay(issue.due_date);

  // start > due 属于异常数据（后端只校验 RFC3339 格式，不校验先后顺序）。
  // 这里按 min/max 归一化，让这一行至少还能画出条形；同时给它加红圈警示，
  // 让用户察觉异常，而不是看着一行静默空着。
  const inverted =
    start !== null && due !== null && start.getTime() > due.getTime();
  const rangeStart = start && due ? (inverted ? due : start) : (start ?? due);
  const rangeEnd = start && due ? (inverted ? start : due) : (start ?? due);

  let bar: { left: number; width: number; isMarker: boolean } | null = null;
  if (rangeStart && rangeEnd) {
    const s = Math.max(daysBetween(range.start, rangeStart), 0);
    const e = Math.min(daysBetween(range.start, rangeEnd) + 1, totalDays);
    if (e > s) {
      const isSingle = !start || !due;
      if (isSingle) {
        bar = { left: s * dayPx, width: Math.max(dayPx, 12), isMarker: true };
      } else {
        bar = { left: s * dayPx, width: (e - s) * dayPx, isMarker: false };
      }
    }
  }

  const fmt = (d: Date) =>
    d.toLocaleDateString(locale, {
      month: "short",
      day: "numeric",
      year: "numeric",
      timeZone: "UTC",
    });

  return (
    <IssueActionsContextMenu issue={issue}>
      <div
        className="flex border-b border-foreground/5 hover:bg-accent/30 transition-colors"
        style={{ height: ROW_HEIGHT }}
      >
        {/* 左侧吸顶的标签格 */}
        <AppLink
          href={p.issueDetail(issue.id)}
          className="sticky left-0 z-[1] flex shrink-0 items-center gap-2 border-r bg-background px-3 text-body min-w-0"
          style={{ width: LEFT_COL_WIDTH }}
        >
          <StatusIcon
            status={issue.status}
            category={issueStatusCategory(issue) ?? undefined}
            className="h-3.5 w-3.5"
          />
          <PriorityIcon priority={issue.priority} />
          <span className="w-14 shrink-0 text-caption text-muted-foreground tabular-nums truncate">
            {issue.identifier}
          </span>
          <span className="truncate flex-1">{issue.title}</span>
          {project && <ProjectIcon project={project} size="sm" />}
          {issue.assignee_type && issue.assignee_id && (
            <ActorAvatar
              actorType={issue.assignee_type}
              actorId={issue.assignee_id}
              size="sm"
              enableHoverCard
            />
          )}
        </AppLink>
        {/* 时间轨道 */}
        <div
          className="relative shrink-0"
          style={{ width: totalDays * dayPx }}
        >
          {bar && (
            <Tooltip>
              <TooltipTrigger
                render={
                  <AppLink
                    href={p.issueDetail(issue.id)}
                    className={cn(
                      "absolute top-1/2 -translate-y-1/2 transition-opacity hover:opacity-90",
                      bar.isMarker
                        ? "h-3 w-3 rotate-45 rounded-[2px]"
                        : "h-5 rounded-md",
                      STATUS_BAR_BG[issueStatusCategory(issue) ?? "todo"],
                      inverted && "ring-2 ring-destructive ring-offset-1 ring-offset-background",
                    )}
                    style={{ left: bar.left, width: bar.width }}
                  >
                    {!bar.isMarker && bar.width > 60 && (
                      <span className="block truncate px-2 py-[2px] text-micro leading-4 text-white">
                        {issue.title}
                      </span>
                    )}
                  </AppLink>
                }
              />
              <TooltipContent side="top">
                <div className="flex flex-col gap-0.5 text-caption">
                  <span className="font-medium">{issue.title}</span>
                  <span className="text-muted-foreground">
                    {start ? fmt(start) : "—"} → {due ? fmt(due) : "—"}
                  </span>
                  {inverted && (
                    <span className="text-destructive">
                      {t(($) => $.gantt.inverted_dates_warning)}
                    </span>
                  )}
                </div>
              </TooltipContent>
            </Tooltip>
          )}
        </div>
      </div>
    </IssueActionsContextMenu>
  );
}

// ---------------------------------------------------------------------------
// GanttView —— 对外导出的甘特图组件
// ---------------------------------------------------------------------------

/**
 * 甘特图视图组件：把传入的 issue 列表按时间跨度渲染成横向甘特图（行 = issue，
 * 横轴 = 时间，彩色条形 = start_date ~ due_date 的跨度）。
 * @param issues 待绘制的 issue 列表——调用方（surface）已套过共享过滤、剔除
 *   无日期的行并处理了"是否显示已完成"，因此本组件只负责排序与绘制。
 */
export function GanttView({ issues }: { issues: Issue[] }) {
  const { t } = useT("issues");
  const zoom = useViewStore((s) => s.ganttZoom);
  const showCompleted = useViewStore((s) => s.ganttShowCompleted);
  const sortBy = useViewStore((s) => s.sortBy);
  const sortDirection = useViewStore((s) => s.sortDirection);
  const act = useViewStoreApi().getState();

  const today = useMemo(() => startOfDayUTC(new Date()), []);
  const dayPx = DAY_PX_BY_ZOOM[zoom];

  // `issues` 拿到的已是"该画哪些行"的最终集合：surface 在传进来之前就套好了
  // 共享过滤、剔除了没有日期的行、并按 `ganttShowCompleted` 做过取舍（见
  // use-issue-surface-data.ts 的 `ganttCanvasRows`）。这些规则原先写在本组件里，
  // 结果头部 chip 统计的行数与画布实际画出来的对不上 (MUL-4884)。
  // 因此请让本视图保持"纯渲染器"：它只负责给行排序，不决定哪些行存在。
  const scheduled = useMemo(() => {
    // 甘特图上"手动排序 position"没有意义——当用户没选更具体的排序字段时，
    // 默认按开始日期升序。
    const sortField = sortBy === "position" ? "start_date" : sortBy;
    return sortIssues(issues, sortField, sortDirection);
  }, [issues, sortBy, sortDirection]);

  const range = useMemo(
    () => computeRange(scheduled, today, zoom),
    [scheduled, today, zoom],
  );
  const totalDays = daysBetween(range.start, range.end);
  const timelineWidth = totalDays * dayPx;
  const todayOffsetDays = daysBetween(range.start, today);

  const scrollRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const target = Math.max(0, LEFT_COL_WIDTH + todayOffsetDays * dayPx - 240);
    el.scrollLeft = target;
  }, [todayOffsetDays, dayPx]);

  if (scheduled.length === 0) {
    return (
      <div className="flex-1 min-h-0 flex items-center justify-center text-body text-muted-foreground">
        {t(($) => $.gantt.empty)}
      </div>
    );
  }

  return (
    <div className="flex flex-col flex-1 min-h-0">
      {/* 工具栏 */}
      <div className="flex h-9 shrink-0 items-center gap-2 border-b px-3">
        <div className="inline-flex items-center rounded-md border border-foreground/10 p-0.5">
          {([
            { value: "day", label: t(($) => $.gantt.zoom_day) },
            { value: "week", label: t(($) => $.gantt.zoom_week) },
            { value: "month", label: t(($) => $.gantt.zoom_month) },
          ] as const).map((opt) => (
            <Button
              key={opt.value}
              size="sm"
              variant={zoom === opt.value ? "secondary" : "ghost"}
              className={cn(
                "h-6 px-2 text-caption",
                zoom !== opt.value && "text-muted-foreground",
              )}
              onClick={() => act.setGanttZoom(opt.value)}
            >
              {opt.label}
            </Button>
          ))}
        </div>
        <div className="flex-1" />
        <Button
          size="sm"
          variant={showCompleted ? "secondary" : "outline"}
          className={cn(
            "h-7 text-caption",
            !showCompleted && "text-muted-foreground",
          )}
          onClick={act.toggleGanttShowCompleted}
        >
          {t(($) => $.gantt.show_completed)}
        </Button>
      </div>

      {/* 主体 —— 单一滚动容器同时驱动纵向滚动（跨行）与横向滚动（时间轴） */}
      <div ref={scrollRef} className="flex-1 min-h-0 overflow-auto">
        <div style={{ minWidth: LEFT_COL_WIDTH + timelineWidth }}>
          {/* 吸顶的表头行 */}
          <div className="sticky top-0 z-20 flex">
            <div
              className="sticky left-0 z-30 shrink-0 border-b border-r bg-background"
              style={{ width: LEFT_COL_WIDTH, height: HEADER_HEIGHT }}
            >
              <div className="flex h-full items-end px-3 pb-1.5 text-micro font-medium text-muted-foreground">
                {t(($) => $.gantt.header_issue)}
              </div>
            </div>
            <GanttAxis
              range={range}
              dayPx={dayPx}
              zoom={zoom}
              todayOffsetDays={todayOffsetDays}
              width={timelineWidth}
            />
          </div>

          {/* 计划中的各行 + 背景覆盖层 */}
          <div className="relative">
            {/* 背景网格线与今日线纵向贯穿所有行。起点让出左侧标签列。 */}
            <div
              className="pointer-events-none absolute top-0"
              style={{ left: LEFT_COL_WIDTH, width: timelineWidth }}
            >
              <BackgroundLayer
                range={range}
                dayPx={dayPx}
                height={scheduled.length * ROW_HEIGHT}
                todayOffsetDays={todayOffsetDays}
              />
            </div>
            {scheduled.map((issue) => (
              <ScheduledRow
                key={issue.id}
                issue={issue}
                range={range}
                dayPx={dayPx}
                totalDays={totalDays}
              />
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
