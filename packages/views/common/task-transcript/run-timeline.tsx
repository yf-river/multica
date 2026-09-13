// 运行时间线图：把一次运行的时间花在哪，画在真实墙钟轴上。模型泳道（思考/汇报）
// 与工具泳道各占一行，条块可点击跳转到对应步骤，支持 1×/2×/4×/8× 缩放并保持
// 视口中点不动。时间线数据由 build-steps 产出。

"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Minus, Plus } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import {
  laneSegmentPosition,
  timelineTicks,
  type LaneSegment,
  type TraceLanes,
  type ToolKindTotals,
} from "./build-steps";
import { useT } from "../../i18n";

// 为什么是两条泳道而不是一条：智能体循环里模型回合与工具调用绝不重叠，塞在同一行
// 只会互相遮挡。拆开后每条泳道是独立的梳齿、带自己的合计——"是模型慢还是命令慢"
// 不用悬停就能看出来。
//
// 为什么不是四条：工具泳道按行为拆分看似可行，但真实运行里某一种工具会以数量级
// 优势占掉绝大多数时间（命令一跑几分钟，读文件/编辑几毫秒就完），按工具分就会画
// 出三条近乎空白的轨道。那个细分放到工具泳道的悬停 tooltip 里呈现。
//
// 颜色只表状态、不表分类：token 色板只有一条蓝色明度渐变、没有分类调色板，所以
// 块在哪个泳道说明它是哪类工作，颜色留给成功（绿）与失败（红）。

const SEGMENT_TONE: Record<LaneSegment["kind"], string> = {
  tool: "bg-chart-2",
  think: "bg-chart-3",
  report: "bg-success",
  error: "bg-destructive",
};

/** 适配宽度后按 2 的幂放大。超过 8 倍时一次 30 分钟的运行要滚 20 屏，那就不是
 *  导航而是折磨了。 */
const ZOOM_STEPS = [1, 2, 4, 8] as const;
const LABEL_WIDTH = "5.75rem";

// 刻度文本格式化：自动按量级切到 秒 / 分:秒 / 时:分。刻度只在整数值处出现。
function formatTick(ms: number): string {
  const totalSeconds = Math.round(ms / 1000);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) return seconds === 0 ? `${minutes}m` : `${minutes}m ${seconds}s`;
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return rest === 0 ? `${hours}h` : `${hours}h ${rest}m`;
}

/**
 * 泳道合计文本格式化：给每个泳道末尾的耗时合计用，分钟/秒数补零对齐
 * （"3m 05s"），方便同一列上下两行对齐阅读。
 * @param ms 时长（毫秒）。
 * @returns 形如 "45s" / "3m 05s" / "2h 03m" 的文本。
 */
export function formatLaneTotal(ms: number): string {
  const totalSeconds = Math.round(ms / 1000);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) return `${minutes}m ${String(seconds).padStart(2, "0")}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${String(minutes % 60).padStart(2, "0")}m`;
}

// 泳道左侧的固定标签行：名称 + 右侧对齐的合计。
function LaneLabel({ label, total, title }: { label: string; total: string; title?: string }) {
  return (
    <div
      title={title}
      className="flex h-[15px] items-baseline gap-1.5 pr-2.5 text-micro text-muted-foreground"
    >
      <span className="truncate">{label}</span>
      <span className="ml-auto shrink-0 tabular-nums text-faint-foreground">{total}</span>
    </div>
  );
}

// 泳道主体：一条灰色底轨上按时间比例排布各段色块，每块可点击跳到对应步骤。
function LaneTrack({
  label,
  segments,
  totalMs,
  onSeek,
}: {
  label: string;
  segments: LaneSegment[];
  totalMs: number;
  onSeek: (ms: number) => void;
}) {
  return (
    <div className="relative h-[15px] rounded-sm bg-muted/55">
      {segments.map((segment) => (
        <button
          key={`${segment.kind}:${segment.startMs}`}
          type="button"
          aria-label={`${label} ${formatLaneTotal(segment.durationMs)}`}
          title={`${label} · ${formatLaneTotal(segment.durationMs)}`}
          onClick={() => onSeek(segment.startMs)}
          className={cn(
            "absolute inset-y-0 rounded-[3px] transition-opacity hover:opacity-80",
            SEGMENT_TONE[segment.kind],
          )}
          style={laneSegmentPosition(segment, totalMs)}
        />
      ))}
    </div>
  );
}

/**
 * 运行时间线。两条泳道（模型/工具）画在真实时间轴上，刻度随缩放保持屏上密度
 * 恒定；条块点击触发 `onSeek` 跳到对应步骤。可选地把选中步骤画成跨两条泳道的
 * 播放头。
 * @param lanes 分泳道的时间线段数据。
 * @param toolKinds 工具泳道合计背后的细分（命令/写入/读取/其他），悬停展示。
 * @param selectedOffsetMs 选中步骤的起始偏移，画成播放头。
 * @param onSeek 点击某时间段的跳转回调。
 */
export function RunTimeline({
  lanes,
  toolKinds,
  selectedOffsetMs,
  onSeek,
}: {
  lanes: TraceLanes;
  /** 工具泳道合计的细分构成，悬停显示。 */
  toolKinds?: ToolKindTotals;
  /** 选中步骤的时间偏移，画成贯穿两条泳道的播放头。 */
  selectedOffsetMs?: number;
  onSeek: (ms: number) => void;
}) {
  const { t } = useT("agents");
  const [zoom, setZoom] = useState(1);
  const scrollerRef = useRef<HTMLDivElement>(null);

  // 屏更宽意味着标签更有地方放：刻度间隔随 zoom 放大，保证它们在屏上的密度恒定，
  // 而不是放大后越变越稀。
  const ticks = useMemo(() => timelineTicks(lanes.totalMs, 6 * zoom), [lanes.totalMs, zoom]);

  const setZoomAround = useCallback(
    (next: number) => {
      const el = scrollerRef.current;
      const previous = zoom;
      setZoom(next);
      if (!el) return;
      // 放大后仍让视口中点下面的内容留在中点，缩放就不会把读者瞬移到别处。
      const centerRatio = (el.scrollLeft + el.clientWidth / 2) / (el.clientWidth * previous);
      requestAnimationFrame(() => {
        el.scrollLeft = centerRatio * el.clientWidth * next - el.clientWidth / 2;
      });
    },
    [zoom],
  );

  // 选中的步骤如果滚出了屏外，播放头就看不见了——把它拉回视口。
  useEffect(() => {
    const el = scrollerRef.current;
    if (!el || selectedOffsetMs === undefined || zoom === 1) return;
    const x = (selectedOffsetMs / lanes.totalMs) * el.clientWidth * zoom;
    if (x < el.scrollLeft || x > el.scrollLeft + el.clientWidth) {
      el.scrollLeft = Math.max(0, x - el.clientWidth / 2);
    }
  }, [selectedOffsetMs, lanes.totalMs, zoom]);

  const zoomIndex = ZOOM_STEPS.indexOf(zoom as (typeof ZOOM_STEPS)[number]);
  const canZoomIn = zoomIndex >= 0 && zoomIndex < ZOOM_STEPS.length - 1;
  const canZoomOut = zoomIndex > 0;

  const toolTitle = toolKinds
    ? [
        [t(($) => $.transcript.tool_kind_command), toolKinds.command],
        [t(($) => $.transcript.tool_kind_write), toolKinds.write],
        [t(($) => $.transcript.tool_kind_read), toolKinds.read],
        [t(($) => $.transcript.tool_kind_misc), toolKinds.other],
      ]
        .filter(([, ms]) => (ms as number) > 0)
        .map(([name, ms]) => `${name} ${formatLaneTotal(ms as number)}`)
        .join(" · ")
    : undefined;

  return (
    <div className="group/timeline relative shrink-0 border-b px-4 pb-2.5 pt-2">
      <div className="flex items-start">
        {/* 标签固定不动、轨道滚动——合计跟着滚走的话，就对着不属于它的条块读了。 */}
        <div className="shrink-0" style={{ width: LABEL_WIDTH }}>
          <div className="h-3" />
          <div className="mt-1 space-y-1">
            <LaneLabel
              label={t(($) => $.transcript.lane_model)}
              total={formatLaneTotal(lanes.modelMs)}
            />
            <LaneLabel
              label={t(($) => $.transcript.lane_tool)}
              total={formatLaneTotal(lanes.toolMs)}
              title={toolTitle}
            />
          </div>
        </div>

        {/* 缩放时这条轨道横向滚动，永远不需要竖向滚动。必须显式写
            overflow-y-hidden：单独 `overflow-x: auto` 会把 overflow-y 也算成 auto，
            于是泳道下沿多出一个像素的绘制就能买到竖向滚动条，而滚动条会挤窄轨道、
            反过来又影响要不要横向滚动条——两条 15px 高的泳道没有可收敛的固定点，
            轨道内任何 `:hover` 都会每帧重跑一次判定，时间线在指针停在一块条上期间
            一直抖（#6278）。 */}
        <div
          ref={scrollerRef}
          className="min-w-0 flex-1 overflow-x-auto overflow-y-hidden overscroll-x-contain"
        >
          <div className="relative" style={{ width: `${zoom * 100}%` }}>
            <div className="relative h-3">
              {ticks.map((tick, index) => (
                <span
                  key={tick}
                  className={cn(
                    "absolute top-0 whitespace-nowrap text-micro tabular-nums text-faint-foreground",
                    index === 0
                      ? ""
                      : index === ticks.length - 1
                        ? "-translate-x-full"
                        : "-translate-x-1/2",
                  )}
                  style={{ left: `${(tick / lanes.totalMs) * 100}%` }}
                >
                  {formatTick(tick)}
                </span>
              ))}
            </div>
            <div className="mt-1 space-y-1">
              <LaneTrack
                label={t(($) => $.transcript.lane_model)}
                segments={lanes.model}
                totalMs={lanes.totalMs}
                onSeek={onSeek}
              />
              <LaneTrack
                label={t(($) => $.transcript.lane_tool)}
                segments={lanes.tool}
                totalMs={lanes.totalMs}
                onSeek={onSeek}
              />
            </div>
            {selectedOffsetMs !== undefined && (
              <div
                aria-hidden
                // 播放头两端落在泳道上而不是伸出轨道下方：轨道现在会裁切自己的
                // 竖直溢出，伸出去的部分只会被切掉。
                className="pointer-events-none absolute bottom-0 top-2.5 w-0.5 rounded-full bg-brand"
                style={{
                  left: `${
                    (Math.min(Math.max(selectedOffsetMs, 0), lanes.totalMs) / lanes.totalMs) * 100
                  }%`,
                }}
              />
            )}
          </div>
        </div>
      </div>

      {/* 缩放是对付"长运行把条块压成细线"的答案。静止状态保持干净：控件悬停才
          出现；一旦放大过就常驻可见，回去的路永远能找到。 */}
      <div
        className={cn(
          "absolute right-3 top-1 flex items-center gap-0.5 rounded-md border bg-surface/95 p-0.5 shadow-sm transition-opacity",
          zoom > 1 ? "opacity-100" : "opacity-0 focus-within:opacity-100 group-hover/timeline:opacity-100",
        )}
      >
        <button
          type="button"
          disabled={!canZoomOut}
          onClick={() => setZoomAround(ZOOM_STEPS[zoomIndex - 1] ?? 1)}
          aria-label={t(($) => $.transcript.timeline_zoom_out)}
          className="flex h-5 w-5 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent disabled:opacity-40"
        >
          <Minus className="h-3 w-3" />
        </button>
        <span className="min-w-[2rem] text-center text-micro tabular-nums text-muted-foreground">
          {zoom}×
        </span>
        <button
          type="button"
          disabled={!canZoomIn}
          onClick={() => setZoomAround(ZOOM_STEPS[zoomIndex + 1] ?? zoom)}
          aria-label={t(($) => $.transcript.timeline_zoom_in)}
          className="flex h-5 w-5 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent disabled:opacity-40"
        >
          <Plus className="h-3 w-3" />
        </button>
      </div>
    </div>
  );
}
