// 任务执行实录（transcript）主弹窗：把一次智能体运行完整地展现在一个可滚动的
// 四层界面上——发生了什么（头部）、产出了什么（结果行）、时间花在哪（时间线）、
// 以及证据（步骤列表）。任意时刻只有一层被展开：步骤检查器按需打开，而不是在读者
// 还没问任何问题之前就把界面劈成两半。支持实时任务追尾、倒序/正序、按工具过滤、
// 站内搜索、步骤详情与图片结果展示。

"use client";

import { RunContextDialog } from "../../issues/components/run-context-dialog";
import { TaskInteractionHistory } from "../task-interaction-panel";
import { TaskOutput } from "./task-output";
import { useState, useRef, useCallback, useEffect, useLayoutEffect, useMemo, forwardRef } from "react";
import { Virtuoso, type VirtuosoHandle, type Components } from "react-virtuoso";
import {
  Bot,
  Brain,
  CircleAlert,
  CheckCircle2,
  XCircle,
  X,
  Loader2,
  Clock,
  Copy,
  Check,
  ChevronRight,
  Filter,
  FilePen,
  FileText,
  Search,
  Terminal,
  Wrench,
  MoreHorizontal,
  ArrowDownNarrowWide,
  ArrowUpNarrowWide,
  Info,
  Coins,
  GitBranch,
} from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuSeparator,
  DropdownMenuCheckboxItem,
  DropdownMenuItem,
} from "@multica/ui/components/ui/dropdown-menu";
import { ActorAvatar } from "../actor-avatar";
import { AttributionBadge } from "../../issues/components/attribution-badge";
import { cancelReasonLabel, failureReasonLabel } from "../../agents/components/tabs/task-failure";
import { RichContent } from "../../rich-content";
import { api } from "@multica/core/api";
import {
  useTranscriptViewStore,
  type TranscriptFilterKey,
  type TranscriptSortDirection,
} from "@multica/core/agents/stores";
import type { AgentTask, Agent, AgentRuntime } from "@multica/core/types/agent";
import { resolveWorkdirCopyTarget } from "@multica/core/issues";
import { runtimeDisplayName, providerDisplayName } from "@multica/core/runtimes";
import { useCustomPricingStore } from "@multica/core/runtimes/custom-pricing-store";
import { redactSecrets } from "./redact";
import {
  createLiveEndFollow,
  FOLLOW_EDGE_THRESHOLD,
  LINE_SCROLL_PX,
} from "./transcript-follow";
import type { TimelineItem } from "./build-timeline";
import {
  buildLanes,
  buildSteps,
  groupSteps,
  isCallStep,
  isGroupRow,
  rowCalls,
  shouldShowTimeline,
  toolKindTotals,
  type TraceCallStep,
  type TraceGroupRow,
  type TraceMessageStep,
  type TraceRow,
  type TraceStep,
} from "./build-steps";
import { buildRunOutcome } from "./run-outcome";
import { RunTimeline } from "./run-timeline";
import {
  base64ByteLength,
  readImageResult,
  traceEventCopyText,
  traceEventDetail,
  traceEventLabel,
  traceEventSummary,
  traceToolArgSummary,
} from "./trace-event-presenter";
import type { TraceSummaryLabels } from "./trace-event-presenter";
import {
  DiffDetailSurface,
  FileWriteSurface,
  PatchDetailSurface,
  ToolDetailSurface,
} from "./detail-surfaces";
import { languageForPath } from "./diff-highlight";
import { useLocale, useT } from "../../i18n";
import {
  formatTokens,
  formatUsd,
  summarizeTaskUsage,
} from "../../runtimes/utils";
import "../../editor/styles/code.css";
import "./task-transcript.css";

// 运行界面按四层阅读：发生了什么（头部）、产出了什么（结果）、时间花在哪
// （时间线）、证据（步骤）。同一时刻只展开一层——检查器按需打开，而不是在读者
// 还没问任何问题前就把界面劈成两半。

interface AgentTranscriptDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  task: AgentTask;
  items: TimelineItem[];
  agentName: string;
  isLive?: boolean;
  /**
   * 渲染在头部 chip 与事件列表之间的可选内容。autopilot 运行行用来把入站的
   * webhook 触发载荷摆出来，让它在智能体是否复述都可见。弹窗保持通用——
   * 插槽内容由调用方负责。
   */
  headerSlot?: React.ReactNode;
}

// ─── 辅助函数 ──────────────────────────────────────────────────────────────

// 两个 ISO 时间之间的时长，格式化成人读文本（头部与行上列用）。
function formatDuration(start: string, end: string): string {
  const ms = new Date(end).getTime() - new Date(start).getTime();
  return formatElapsedMs(ms);
}

// 毫秒数格式化成 "Xs" / "Xm Ys" 的人读文本。
function formatElapsedMs(ms: number): string {
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const secs = seconds % 60;
  return `${minutes}m ${secs}s`;
}

/** 单步自身的时长，用右列那种紧凑格式显示（秒带一位小数，分带补零秒）。 */
function formatStepDuration(ms: number): string {
  if (ms <= 0) return "—";
  if (ms < 100) return "<0.1s";
  if (ms < 1000) return `${(ms / 1000).toFixed(1)}s`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)}s`;
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1000);
  return `${minutes}m ${String(seconds).padStart(2, "0")}s`;
}

/** 距运行起点的偏移，即一个步骤"位置"的含义。 */
function formatOffset(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) return `+${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
  const hours = Math.floor(minutes / 60);
  return `+${hours}:${String(minutes % 60).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
}

function formatRunTime(iso: string, locale: string): string {
  return new Date(iso).toLocaleString(locale, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function timeMs(iso: string | undefined): number | undefined {
  if (!iso) return undefined;
  const ms = new Date(iso).getTime();
  return Number.isFinite(ms) ? ms : undefined;
}

// 单步的过滤键：调用按其工具名（带 "tool:" 前缀以区分同名不同类），其余按其
// kind。
function stepFilterKey(step: TraceStep): TranscriptFilterKey {
  return step.kind === "call" ? `tool:${step.tool}` : step.kind;
}

/** 站内搜索用的小写 haystack。输出被裁剪：命令输出 4KB 之后的内容不可能是
 *  任何人想搜的目标。 */
function stepHaystack(step: TraceStep): string {
  if (step.kind !== "call") return (step.item.content ?? "").toLowerCase();
  const input = step.call?.input ? JSON.stringify(step.call.input) : "";
  return `${step.tool} ${input} ${(step.result?.output ?? "").slice(0, 4000)}`.toLowerCase();
}

// 按步骤类型挑图标：思考用大脑、错误用告警三角、文本用机器人。
function StepIcon({ step, className }: { step: TraceStep; className?: string }) {
  if (!isCallStep(step)) {
    if (step.kind === "thinking") return <Brain className={className} />;
    if (step.kind === "error") return <CircleAlert className={className} />;
    return <Bot className={className} />;
  }

  // 工具名因 provider 而异，所以图标由入参的形状决定：命令→终端，写文件→笔，
  // 查询/正则→放大镜，路径→文件，其余→扳手。
  const input = step.call?.input;
  if (input) {
    if (typeof input.command === "string") return <Terminal className={className} />;
    if (typeof input.old_string === "string" || typeof input.content === "string" || input.changes)
      return <FilePen className={className} />;
    if (typeof input.query === "string" || typeof input.pattern === "string")
      return <Search className={className} />;
    if (typeof input.file_path === "string" || typeof input.path === "string")
      return <FileText className={className} />;
  }
  return <Wrench className={className} />;
}

// ─── 运行详情行（ⓘ 弹层） ─────────────────────────────────────────────────
// 诊断弹层里的一行带标签事实。给 `onCopy` 时整行都是复制按钮（工作目录路径用）。
function RunDetailRow({
  label,
  value,
  mono,
  onCopy,
  copied,
  copyTitle,
}: {
  label: string;
  value: string;
  mono?: boolean;
  onCopy?: () => void;
  copied?: boolean;
  copyTitle?: string;
}) {
  const valueClass = cn("min-w-0 select-text break-all text-foreground", mono && "font-mono");
  if (onCopy) {
    return (
      <button
        type="button"
        onClick={onCopy}
        title={copyTitle}
        className="group -mx-1 grid w-[calc(100%+0.5rem)] grid-cols-[4.5rem_minmax(0,1fr)] items-start gap-3 rounded px-1 py-0.5 text-left transition-colors hover:bg-accent/60"
      >
        <span className="text-muted-foreground">{label}</span>
        <span className="flex min-w-0 items-start gap-1.5">
          <span className={cn(valueClass, "flex-1")}>{value}</span>
          {copied ? (
            <Check className="mt-0.5 h-3 w-3 shrink-0 text-success" />
          ) : (
            <Copy className="mt-0.5 h-3 w-3 shrink-0 opacity-0 transition-opacity group-hover:opacity-100" />
          )}
        </span>
      </button>
    );
  }
  return (
    <div className="grid grid-cols-[4.5rem_minmax(0,1fr)] items-start gap-3">
      <span className="text-muted-foreground">{label}</span>
      <span className={valueClass}>{value}</span>
    </div>
  );
}

// ─── 主弹窗 ────────────────────────────────────────────────────────────────

// Virtuoso 把行作为其 List 元素的直接子元素挂载；这里由我们自己提供分隔线样式
// （原先是列表容器带的）。定义在模块作用域——若把 `components` 对象写成内联的，
// 每次渲染都会重挂整个列表。
const VirtuosoList = forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(
  function VirtuosoList(props, ref) {
    return <div ref={ref} {...props} className="divide-y" />;
  },
);
const LIST_COMPONENTS: Components<TraceRow> = { List: VirtuosoList };
const COPY_FEEDBACK_DURATION_MS = 2000;

// 复制反馈 Hook：复制动作后把"已复制 ✓"状态点亮约 2 秒再熄灭。卸载后不再动
// state，避免在异步回调里对已卸载组件 setState。
function useCopyFeedback() {
  const [copied, setCopied] = useState(false);
  const resetTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      if (resetTimerRef.current !== null) {
        clearTimeout(resetTimerRef.current);
        resetTimerRef.current = null;
      }
    };
  }, []);

  const showCopied = useCallback(() => {
    if (!mountedRef.current) return;
    setCopied(true);
    if (resetTimerRef.current !== null) clearTimeout(resetTimerRef.current);
    resetTimerRef.current = setTimeout(() => {
      resetTimerRef.current = null;
      if (mountedRef.current) setCopied(false);
    }, COPY_FEEDBACK_DURATION_MS);
  }, []);

  return [copied, showCopied] as const;
}

/**
 * 任务执行实录弹窗：展示一次运行的完整过程。头部给出结果状态/身份/耗时/成本；
 * 结果行概括"它碰了哪些文件、跑了几条命令"；可选时间线画出时间去向；主区是按
 * 时间排列的步骤列表，支持工具过滤、站内搜索、倒序/正序，点行在右侧检查器展开
 * 该步的输入与结果。实时任务会随新事件增长（最新在前时自动钉在最新行）。
 * @param open 是否打开。
 * @param onOpenChange 开关回调。
 * @param task 智能体任务。
 * @param items 时间线条目。
 * @param agentName 智能体名称。
 * @param isLive 任务是否实时进行中。
 * @param headerSlot 事件列表上方的可选插槽内容。
 */
export function AgentTranscriptDialog({
  open,
  onOpenChange,
  task,
  items,
  agentName,
  isLive = false,
  headerSlot,
}: AgentTranscriptDialogProps) {
  const { t } = useT("agents");
  const locale = useLocale();
  const [runtimeContextOpen, setRuntimeContextOpen] = useState(false);
  useEffect(() => { setRuntimeContextOpen(false); }, [open, task.id]);
  const [selectedSeq, setSelectedSeq] = useState<number | null>(null);
  const [expandedGroups, setExpandedGroups] = useState<Set<number>>(() => new Set());
  const [query, setQuery] = useState("");
  const [elapsed, setElapsed] = useState("");
  const [copied, showCopied] = useCopyFeedback();
  const [copiedWorkdir, showCopiedWorkdir] = useCopyFeedback();
  const [copiedBranch, showCopiedBranch] = useCopyFeedback();
  const [agentInfo, setAgentInfo] = useState<Agent | null>(null);
  const [runtimeInfo, setRuntimeInfo] = useState<AgentRuntime | null>(null);
  const workdirCopyTarget = useMemo(
    () => resolveWorkdirCopyTarget([task]),
    [task],
  );
  const sortDirection = useTranscriptViewStore((s) => s.sortDirection);
  const setSortDirection = useTranscriptViewStore((s) => s.setSortDirection);
  // 过滤条件总是跨打开保留——运行没有的面就空操作（见 activeFilterKeys），
  // 所以没必要把它做成可开关的"持久化"选项。
  const selectedFilterKeys = useTranscriptViewStore((s) => s.selectedFilterKeys);
  const toggleFilterKey = useTranscriptViewStore((s) => s.toggleFilterKey);
  const clearFilters = useTranscriptViewStore((s) => s.clearFilterKeys);
  const virtuosoRef = useRef<VirtuosoHandle>(null);
  // 最新在前实时跟随（#5921）：所有锁存决策都在纯控制器里（模型见
  // transcript-follow.ts）；本组件只负责把 DOM 事件接给它。实例稳定，
  // 绝不被滚动流量重新渲染。
  const followCtl = useMemo(() => createLiveEndFollow(), []);
  const detachScrollerRef = useRef<(() => void) | null>(null);

  // Virtuoso 回调 scrollerRef 时，把跟随状态机接到滚动元素的各类事件上：
  // 滚轮/触屏/键盘是"输入"，滚动事件本身做"归因与钉回判定"。返回清理函数。
  const handleScrollerRef = useCallback(
    (el: HTMLElement | Window | null) => {
      detachScrollerRef.current?.();
      detachScrollerRef.current = null;
      if (!(el instanceof HTMLElement)) return;
      // 输入先暂存给状态机，下一帧再清掉没被滚动确认的部分（见 transcript-follow
      // 的 input/endInputFrame 契约）。
      let inputFrame: number | null = null;
      const stageInput = (delta: number) => {
        followCtl.input(delta);
        if (inputFrame !== null) cancelAnimationFrame(inputFrame);
        inputFrame = requestAnimationFrame(() => {
          inputFrame = null;
          followCtl.endInputFrame();
        });
      };
      // 滚轮事件：把 deltaMode 统一换算成像素（行→行高基准，页→滚动区高度）。
      const onWheel = (e: WheelEvent) => {
        const scale =
          e.deltaMode === 1 ? LINE_SCROLL_PX : e.deltaMode === 2 ? el.clientHeight : 1;
        stageInput(e.deltaY * scale);
      };
      // 触屏：跟踪单指竖直位移作为滚动输入。多指手势只认第一根手指。
      let touchId: number | null = null;
      let lastTouchY: number | null = null;
      const trackedTouch = (touches: TouchList) =>
        Array.from(touches).find((touch) => touch.identifier === touchId);
      const onTouchStart = (e: TouchEvent) => {
        if (touchId !== null) return;
        const touch = e.changedTouches[0] ?? e.touches[0];
        if (!touch) return;
        touchId = touch.identifier;
        lastTouchY = touch.clientY;
        followCtl.touchStart();
      };
      const onTouchMove = (e: TouchEvent) => {
        const touch = trackedTouch(e.touches);
        if (!touch) return;
        // 手指上滑让内容向下滚（远离实时末端），传给状态机的是反向的 Y 位移。
        if (lastTouchY !== null) stageInput(lastTouchY - touch.clientY);
        lastTouchY = touch.clientY;
      };
      const onTouchEnd = (e: TouchEvent) => {
        if (touchId === null || trackedTouch(e.touches)) return;
        touchId = null;
        lastTouchY = null;
        followCtl.touchEnd();
      };
      // 键盘滚动：只有焦点就在滚动元素本身时才算滚动意图；行内控件冒泡上来的
      // Space/方向键不算。
      const onKeyDown = (e: KeyboardEvent) => {
        if (e.target !== el) return;
        if (e.key === "ArrowDown") stageInput(LINE_SCROLL_PX);
        else if (e.key === "ArrowUp") stageInput(-LINE_SCROLL_PX);
        else if (e.key === "PageDown") stageInput(el.clientHeight);
        // Shift+Space 向上翻页——朝本列表的实时末端方向。
        else if (e.key === " ") stageInput(e.shiftKey ? -el.clientHeight : el.clientHeight);
        else if (e.key === "PageUp") stageInput(-el.clientHeight);
        else if (e.key === "End") followCtl.disengage();
      };
      // 滚动条拖动会落在滚动元素自身；点行内容则落在子元素（也要跟踪——按住期间
      // 的强制回钉不能和选中文本的自动滚动打架）。
      const onPointerDown = (e: MouseEvent) => {
        followCtl.pointerDown(e.target === el);
      };
      const onPointerUp = () => {
        followCtl.pointerUp();
      };
      const onScroll = () => {
        // 就在滚动事件上执行跟随：Virtuoso 的前置补偿落在 React effects 之后，
        // 因此只在 effect 里钉会慢一拍。注意：这段直接写 scrollTop 要抢在
        // Virtuoso 自身状态之前，依赖 react-virtuoso 在调用 scrollerRef 之后才
        // 注册滚动监听（4.18.7 如此）；若顺序反转，需改走 virtuosoRef.scrollTo。
        if (followCtl.onScroll(el.scrollTop)) el.scrollTop = 0;
      };
      el.addEventListener("wheel", onWheel, { passive: true });
      el.addEventListener("touchstart", onTouchStart, { passive: true });
      el.addEventListener("touchmove", onTouchMove, { passive: true });
      el.addEventListener("touchend", onTouchEnd, { passive: true });
      el.addEventListener("touchcancel", onTouchEnd, { passive: true });
      el.addEventListener("keydown", onKeyDown);
      el.addEventListener("mousedown", onPointerDown);
      window.addEventListener("mouseup", onPointerUp, { capture: true });
      el.addEventListener("scroll", onScroll, { passive: true });
      detachScrollerRef.current = () => {
        if (inputFrame !== null) cancelAnimationFrame(inputFrame);
        followCtl.endInputFrame();
        el.removeEventListener("wheel", onWheel);
        el.removeEventListener("touchstart", onTouchStart);
        el.removeEventListener("touchmove", onTouchMove);
        el.removeEventListener("touchend", onTouchEnd);
        el.removeEventListener("touchcancel", onTouchEnd);
        el.removeEventListener("keydown", onKeyDown);
        el.removeEventListener("mousedown", onPointerDown);
        window.removeEventListener("mouseup", onPointerUp, { capture: true });
        el.removeEventListener("scroll", onScroll);
        // 滚动区可能在拖动中途脱离（listEpoch 重挂载）；卡住的"按住鼠标"标记
        // 会永久压制强制回钉，所以清理时补发松开。
        followCtl.pointerUp();
        followCtl.touchEnd();
      };
    },
    [followCtl],
  );

  useEffect(() => () => detachScrollerRef.current?.(), []);

  // 换了运行就换了阅读位置：清掉选中的步骤与读者打开过的任何组。
  useEffect(() => {
    setSelectedSeq(null);
    setExpandedGroups(new Set());
  }, [task.id]);

  // ── 派生模型 ────────────────────────────────────────────────────────────

  // 每次工具调用一个步骤，其结果折叠在步内——为何按位置配对见 build-steps.ts。
  const steps = useMemo(() => buildSteps(items), [items]);

  // 一个筛选面读起来就像它的行：行带的字形与行印的名字。每种类型的第一步骤
  // 充当字形。"tool:" 前缀留在键里（它是被持久化的内容），但绝不进入菜单——
  // 行上写的是 `Bash`，筛选面也就写 `Bash`。
  const filterOptions = useMemo(() => {
    const options = new Map<string, { label: string; step: TraceStep }>();
    for (const step of steps) {
      const key = stepFilterKey(step);
      if (options.has(key)) continue;
      options.set(key, {
        label:
          step.kind === "call"
            ? step.tool || t(($) => $.transcript.kind_tool)
            : traceEventLabel({ type: step.item.type, tool: step.item.tool }),
        step,
      });
    }
    return Array.from(options.entries()).sort((a, b) => a[1].label.localeCompare(b[1].label));
  }, [steps, t]);

  const filterOptionKeys = useMemo(
    () => new Set(filterOptions.map(([value]) => value)),
    [filterOptions],
  );

  const activeFilterKeys = useMemo(
    () => selectedFilterKeys.filter((key) => filterOptionKeys.has(key)),
    [selectedFilterKeys, filterOptionKeys],
  );

  const activeFilterSet = useMemo(() => new Set(activeFilterKeys), [activeFilterKeys]);

  const trimmedQuery = query.trim().toLowerCase();

  const filteredSteps = useMemo(() => {
    if (activeFilterSet.size === 0 && trimmedQuery.length === 0) return steps;
    return steps.filter((step) => {
      if (activeFilterSet.size > 0 && !activeFilterSet.has(stepFilterKey(step))) return false;
      if (trimmedQuery.length > 0 && !stepHaystack(step).includes(trimmedQuery)) return false;
      return true;
    });
  }, [steps, activeFilterSet, trimmedQuery]);

  // 分组建立在读者正在看的东西上：过滤会打断相邻关系，横跨被藏步骤的组等于在
  // 撒谎说"这些是连着跑的"。
  const rows = useMemo(() => groupSteps(filteredSteps), [filteredSteps]);

  // 应用用户选择的排序方向。倒序是纯粹的展示问题——底层步骤（及它们的 seq）不被
  // 改动。
  const displayRows = useMemo(
    () => (sortDirection === "newest_first" ? [...rows].reverse() : rows),
    [rows, sortDirection],
  );

  const runStart = task.started_at ?? task.dispatched_at ?? steps[0]?.startedAt;
  const runStartMs = timeMs(runStart);
  // 运行的结束时刻：完成时间优先，否则取最后一个带时间戳的步骤。
  const lastStamp = useMemo(() => {
    for (let i = steps.length - 1; i >= 0; i--) {
      const step = steps[i]!;
      const stamp = step.kind === "call" ? (step.endedAt ?? step.startedAt) : step.startedAt;
      if (stamp) return stamp;
    }
    return undefined;
  }, [steps]);
  const runEnd = task.completed_at ?? lastStamp;

  const lanes = useMemo(() => buildLanes(steps, runStart, runEnd), [steps, runStart, runEnd]);
  // 短运行的时间线信息量不如每行已写着的时长，所以干脆不渲染。
  const showTimeline = shouldShowTimeline(steps, lanes);
  const toolKinds = useMemo(() => toolKindTotals(steps), [steps]);
  const outcome = useMemo(() => buildRunOutcome(steps), [steps]);

  const selectedStep = useMemo(() => {
    if (selectedSeq === null) return null;
    return steps.find((step) => step.seq === selectedSeq) ?? null;
  }, [steps, selectedSeq]);

  const selectedOffsetMs =
    selectedStep && runStartMs !== undefined
      ? Math.max(0, (timeMs(selectedStep.startedAt) ?? runStartMs) - runStartMs)
      : undefined;

  // 以"运行本身什么都没产出"为键，而不是"过滤后的视图为空"——过滤把步骤全藏掉
  // 不是运行能力的限制。
  const isAntigravityLiveEmpty =
    isLive && steps.length === 0 && runtimeInfo?.provider === "antigravity";

  // 最新在前视图把实时事件作为 PREPEND（插到顶部），而 Virtuoso 条目退出了原生
  // 滚动锚定（overflow-anchor: none），所以没有补偿的话每 500ms 一次刷盘都会挪动
  // 阅读位置。Virtuoso 的契约：firstItemIndex 减少 N 会让视口跨过 N 条前置插入，
  // 而该值在同一实例内绝不能增大——从一个大基数往下数满足"列表只增不减"；排序、
  // 过滤或换任务会让列表变短，所以用 `listEpoch` 重挂实例（重新从顶部开始），
  // 而不是让 firstItemIndex 回升。
  const firstItemIndex =
    sortDirection === "newest_first" ? 1_000_000 - displayRows.length : 0;
  const listEpoch = `${task.id}:${sortDirection}:${activeFilterKeys.join(",")}:${trimmedQuery}`;

  const handleSortDirectionChange = useCallback(
    (dir: TranscriptSortDirection) => {
      if (dir === sortDirection) return;
      setSortDirection(dir);
      virtuosoRef.current?.scrollTo({ top: 0 });
    },
    [sortDirection, setSortDirection],
  );

  useLayoutEffect(() => {
    followCtl.setActive(isLive && sortDirection === "newest_first");
  }, [followCtl, isLive, sortDirection]);

  // 新列表实例（换任务/排序/过滤）在其实时末端挂载并带着跟随状态——与首次打开
  // 的契约一致。
  useLayoutEffect(() => {
    followCtl.reset();
  }, [followCtl, listEpoch]);

  // 最新在前的实时跟随（#5921）：锁存保持接合时，每次刷盘都钉回最新行——否则
  // 前置锚定会把视口钉在之前的首行上。瞬时而非平滑：平滑动画跨多次刷盘，其飞行
  // 中的位置会被读成用户的位移。handleScrollerRef 里的滚动事件强制才是权威回钉；
  // 本 effect 只是缩短首绘的空窗。
  const displayCount = displayRows.length;
  useLayoutEffect(() => {
    if (!followCtl.isFollowing()) return;
    virtuosoRef.current?.scrollToIndex({ index: 0, align: "start", behavior: "auto" });
  }, [followCtl, displayCount]);

  // 弹窗打开时拉取智能体与运行时元数据。
  useEffect(() => {
    if (!open) return;
    let cancelled = false;

    if (task.agent_id) {
      api.getAgent(task.agent_id).then((agent) => {
        if (!cancelled) setAgentInfo(agent);
      }).catch(() => {});
    }

    if (task.runtime_id) {
      api.listRuntimes().then((runtimes) => {
        if (cancelled) return;
        const rt = runtimes.find((r) => r.id === task.runtime_id);
        if (rt) setRuntimeInfo(rt);
      }).catch(() => {});
    }

    return () => { cancelled = true; };
  }, [open, task.agent_id, task.runtime_id]);

  // 实时任务的已耗时：每秒刷新一次展示文本。
  useEffect(() => {
    if (!isLive || (!task.started_at && !task.dispatched_at)) return;
    const startRef = task.started_at ?? task.dispatched_at!;
    const update = () => setElapsed(formatElapsedMs(Date.now() - new Date(startRef).getTime()));
    update();
    const interval = setInterval(update, 1000);
    return () => clearInterval(interval);
  }, [isLive, task.started_at, task.dispatched_at]);

  const scrollToStep = useCallback(
    (seq: number) => {
      const index = displayRows.findIndex((row) => row.seq === seq || rowCalls(row).some((c) => c.seq === seq));
      if (index < 0) return;
      // 显式跳到别处会解除最新在前跟随——否则滚动事件强制会把视口直接钉回顶部。
      // 滚回来时照常重新接合。
      if (index > 0) followCtl.disengage();
      virtuosoRef.current?.scrollToIndex({ index, align: "center", behavior: "smooth" });
    },
    [displayRows, followCtl],
  );

  // 点时间线落到该偏移处的第一个步骤（含起点的步骤）。
  const handleSeek = useCallback(
    (offsetMs: number) => {
      if (runStartMs === undefined) return;
      const target = runStartMs + offsetMs;
      let best: TraceStep | null = null;
      for (const step of steps) {
        const at = timeMs(step.startedAt);
        if (at === undefined || at < target) continue;
        if (!best || at < (timeMs(best.startedAt) ?? Infinity)) best = step;
      }
      if (!best) return;
      setSelectedSeq(best.seq);
      scrollToStep(best.seq);
    },
    [steps, runStartMs, scrollToStep],
  );

  const handleCopyWorkdir = useCallback(() => {
    if (!workdirCopyTarget) return;
    void copyText(workdirCopyTarget.path).then((ok) => {
      if (!ok) return;
      showCopiedWorkdir();
    });
  }, [workdirCopyTarget, showCopiedWorkdir]);

  // 工作树模式下的运行交付的是分支而非工作副本里的改动，所以复制分支名是通向
  // `git diff <branch>` 的最快路径。
  const handleCopyBranch = useCallback(() => {
    if (!task.branch_name) return;
    void copyText(task.branch_name).then((ok) => {
      if (!ok) return;
      showCopiedBranch();
    });
  }, [task.branch_name, showCopiedBranch]);

  const handleCopyAll = useCallback(() => {
    // 复制每个事件的完整正文（而非截断的行摘要），并施加详情视图同款的敏感信息
    // 脱敏。一个步骤按其调用+结果的顺序复制，也就是它实际发生的顺序。
    const events: TimelineItem[] = [];
    for (const row of displayRows) {
      if (row.kind === "group") {
        for (const call of row.steps) {
          if (call.call) events.push(call.call);
          if (call.result) events.push(call.result);
        }
        continue;
      }
      if (row.kind === "call") {
        if (row.call) events.push(row.call);
        if (row.result) events.push(row.result);
        continue;
      }
      events.push(row.item);
    }
    const text = events.map((event) => redactSecrets(traceEventCopyText(event))).join("\n\n");
    void copyText(text).then((ok) => {
      if (!ok) return;
      showCopied();
    });
  }, [displayRows, showCopied]);

  const handleToggleGroup = useCallback((seq: number) => {
    setExpandedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(seq)) next.delete(seq);
      else next.add(seq);
      return next;
    });
  }, []);

  const duration =
    task.started_at && task.completed_at
      ? formatDuration(task.started_at, task.completed_at)
      : isLive
        ? elapsed
        : null;

  const copyTranscriptLabel = copied
    ? t(($) => $.transcript.copied)
    : activeFilterKeys.length > 0 || trimmedQuery.length > 0
      ? t(($) => $.transcript.copy_filtered)
      : t(($) => $.transcript.copy_all);

  // 状态徽章——完整状态机，让 queued/dispatched/cancelled 显示成正经文案而不是
  // 裸枚举文本。
  const effectiveStatus = isLive ? "running" : task.status;
  const statusBadge = (() => {
    const base = "inline-flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-caption font-medium";
    switch (effectiveStatus) {
      case "running":
        return (
          <span className={cn(base, "bg-info/15 text-info")}>
            <Loader2 className="h-3 w-3 animate-spin" />
            {t(($) => $.transcript.status_running)}
          </span>
        );
      case "completed":
        return (
          <span className={cn(base, "bg-success/15 text-success")}>
            <CheckCircle2 className="h-3 w-3" />
            {t(($) => $.transcript.status_completed)}
          </span>
        );
      case "failed":
        return (
          <span className={cn(base, "bg-destructive/15 text-destructive")}>
            <XCircle className="h-3 w-3" />
            {t(($) => $.transcript.status_failed)}
          </span>
        );
      case "cancelled": {
        // 被服务器取消的运行（工作树认领闸、保留工作交付）带有用户必须行动的持久
        // 原因；把它放在徽章上而不是只写个"已取消"。用户主动取消没有原因、保持
        // 简洁文案。徽章不带 `title`：背后的原始 `task.error` 是未翻译的运维 prose
        // （#7411），属于运行详情，不该悬停在状态药丸上。
        const cancelReason = cancelReasonLabel(task, t);
        return (
          <span className={cn(base, "bg-muted text-muted-foreground")}>
            <XCircle className="h-3 w-3" />
            {cancelReason
              ? `${t(($) => $.transcript.status_cancelled)} · ${cancelReason}`
              : t(($) => $.transcript.status_cancelled)}
          </span>
        );
      }
      case "queued":
        return (
          <span className={cn(base, "bg-muted text-muted-foreground")}>
            {t(($) => $.transcript.status_queued)}
          </span>
        );
      case "dispatched":
        return (
          <span className={cn(base, "bg-info/15 text-info")}>
            {t(($) => $.transcript.status_dispatched)}
          </span>
        );
      case "waiting_local_directory":
        return (
          <span className={cn(base, "bg-muted text-muted-foreground")}>
            {t(($) => $.transcript.status_waiting)}
          </span>
        );
      default:
        return (
          <span className={cn(base, "bg-muted text-muted-foreground capitalize")}>
            {task.status}
          </span>
        );
    }
  })();

  // 触发来源：一个词回答"为什么会有这次运行"——比运行时/provider 的诊断更有用，
  // 那些放在 ⓘ 弹层里。
  const triggerLabel = task.parent_task_id
    ? t(($) => $.transcript.trigger_retry)
    : task.kind === "comment" || task.trigger_comment_id
      ? t(($) => $.transcript.trigger_comment)
      : task.kind === "autopilot" || task.autopilot_run_id
        ? t(($) => $.transcript.trigger_autopilot)
        : task.kind === "chat" || task.chat_session_id
          ? t(($) => $.transcript.trigger_chat)
          : task.kind === "quick_create"
            ? t(($) => $.transcript.trigger_quick_create)
            : task.kind === "direct" || task.handoff_note
              ? t(($) => $.transcript.trigger_direct)
              : t(($) => $.transcript.trigger_initial);

  // ⓘ 弹层的诊断详情：只在调试这一具体运行时才需要的一切，不放进常显界面。
  const providerLabel = runtimeInfo?.provider ? transcriptProviderLabel(runtimeInfo.provider) : null;
  const createdLabel = task.created_at ? formatRunTime(task.created_at, locale) : null;
  const startedLabel = task.started_at ? formatRunTime(task.started_at, locale) : null;
  const completedLabel = task.completed_at ? formatRunTime(task.completed_at, locale) : null;
  const hasTriggeredBy = !!task.attribution?.initiator;
  // 本次运行自己的花费。从 issue 执行日志打开的转录（那个端点会填充 usage）才有；
  // 别处没有时，下面的 chip 与用量行自然不渲染。
  //
  // `summarizeTaskUsage` 通过自定义费率 store 计价，它是命令式读取的——这里订阅
  // 一下正是为了让保存过的费率变化能传导到这个数字，跟其他用量界面一样。
  useCustomPricingStore((s) => s.pricings);
  const usage = summarizeTaskUsage(task.usage);
  // 两件刻意不拼成一个字符串的事（#7411）：
  //   • `reasonLabel` —— 本地化的原因，由稳定的 `failure_reason` 枚举推导。
  //      这是面向用户的解释。
  //   • `task.error` —— 服务器/daemon 持久化的原始诊断（英文），供归类与日志。
  //      保持可读（它是找到"我的保留工作在哪棵 worktree"与"哪台机器要升级"的
  //      途径），但标注为技术细节而非当作原因呈现，绝不混入本地化文本。
  const reasonLabel =
    task.status === "failed"
      ? failureReasonLabel(task.failure_reason, t)
      : cancelReasonLabel(task, t);
  const hasRunDetails =
    !!runtimeInfo ||
    !!workdirCopyTarget?.relativePath ||
    !!task.branch_name ||
    !!reasonLabel ||
    !!task.error ||
    !!createdLabel ||
    !!startedLabel ||
    !!completedLabel ||
    !!usage;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="!max-w-5xl !w-[calc(100vw-4rem)] !max-h-[calc(100vh-4rem)] !h-[calc(100vh-4rem)] flex flex-col !p-0 !gap-0 overflow-hidden"
        showCloseButton={false}
      >
        <DialogTitle className="sr-only">{t(($) => $.transcript.dialog_title)}</DialogTitle>

        {/* ── 头部：结果、身份、花费 ────────────────────────────────
            读者开读前就要的一切：它怎么结束的、谁跑的、为什么存在、花了多久、
            成本多少。诊断留在 ⓘ 弹层。 */}
        <div className="border-b px-4 py-3 shrink-0">
          <div className="flex min-w-0 items-center gap-3">
            {statusBadge}
            {/* 主身份：跑这次运行的智能体。它是唯一的前景实体——头像 + 中等字重。 */}
            <div className="flex min-w-0 items-center gap-2">
              {task.agent_id ? (
                <ActorAvatar actorType="agent" actorId={task.agent_id} size="sm" enableHoverCard />
              ) : (
                <div className="flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-info/10 text-info">
                  <Bot className="h-3 w-3" />
                </div>
              )}
              <span className="truncate font-medium text-body">
                {agentName || agentInfo?.name || ""}
              </span>
            </div>
            {/* 出处，一个与智能体拉开距离的弱化次要单元：谁触发的、以何种方式——
                读作 "<人> · <方式>"，而不是三个平级实体。这里略去人的头像，避免
                两张同尺寸的脸被读成两个智能体。 */}
            <div className="flex min-w-0 flex-1 items-center gap-x-1.5 overflow-hidden text-caption text-muted-foreground">
              {hasTriggeredBy && (
                <>
                  <AttributionBadge
                    attribution={task.attribution}
                    variant="inline"
                    hideAvatar
                    className="min-w-0"
                  />
                  <FactDot />
                </>
              )}
              <span className="shrink-0">{triggerLabel}</span>
              {duration && (
                <>
                  <FactDot />
                  <span className="shrink-0 tabular-nums">
                    {t(($) => $.transcript.fact_took, { duration })}
                  </span>
                </>
              )}
            </div>

            {/* 这次运行花了多少钱，就放在你正在读的这次运行的头部——"为什么这
                次这么贵"不用回到列表就能回答。明细拆分在 ⓘ 弹层里点一下就到。 */}
            {usage && (
              <span
                className="flex shrink-0 items-center gap-1 rounded-full border px-2 py-0.5 text-micro tabular-nums"
                title={t(($) => $.transcript.usage_chip_title)}
              >
                <Coins aria-hidden="true" className="h-3 w-3 text-muted-foreground" />
                <span className="font-medium">{formatTokens(usage.tokens)}</span>
                <span className="text-faint-foreground">·</span>
                <span className="text-muted-foreground">{formatUsd(usage.cost)}</span>
              </span>
            )}

            <div className="flex shrink-0 items-center gap-0.5">
              {hasRunDetails && (
                <Popover>
                  <PopoverTrigger
                    render={
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={t(($) => $.transcript.run_info)}
                        title={t(($) => $.transcript.run_info)}
                        className="text-muted-foreground"
                      />
                    }
                  >
                    <Info className="h-3.5 w-3.5" />
                  </PopoverTrigger>
                  <PopoverContent align="end" className="w-80 max-w-[calc(100vw-2rem)] p-3">
                    <div className="mb-2 text-caption font-medium text-foreground">
                      {t(($) => $.transcript.run_info)}
                    </div>
                    <div className="space-y-1 text-caption">
                      {runtimeInfo && (
                        <RunDetailRow
                          label={t(($) => $.transcript.details_runtime)}
                          value={runtimeDisplayName(runtimeInfo)}
                        />
                      )}
                      {providerLabel && (
                        <RunDetailRow label={t(($) => $.transcript.details_provider)} value={providerLabel} />
                      )}
                      {runtimeInfo && (
                        <RunDetailRow label={t(($) => $.transcript.details_mode)} value={runtimeInfo.runtime_mode} />
                      )}
                      {workdirCopyTarget?.relativePath && (
                        <RunDetailRow
                          label={
                            workdirCopyTarget.source ===
                            "durable_project_directory"
                              ? t(
                                  ($) =>
                                    $.transcript.details_project_directory,
                                )
                              : t(($) => $.transcript.details_workdir)
                          }
                          value={workdirCopyTarget.relativePath}
                          mono
                          onCopy={handleCopyWorkdir}
                          copied={copiedWorkdir}
                          copyTitle={
                            workdirCopyTarget.source ===
                            "durable_project_directory"
                              ? t(
                                  ($) =>
                                    $.transcript.copy_project_directory,
                                )
                              : t(($) => $.transcript.copy_workdir)
                          }
                        />
                      )}
                      {task.branch_name && (
                        <RunDetailRow
                          label={t(($) => $.transcript.details_branch)}
                          value={task.branch_name}
                          mono
                          onCopy={handleCopyBranch}
                          copied={copiedBranch}
                          copyTitle={t(($) => $.transcript.copy_branch)}
                        />
                      )}
                      {/* 本地化的原因，来自稳定的 `failure_reason` 枚举——
                          这才是解释，且以用户的语言呈现。 */}
                      {reasonLabel && (
                        <RunDetailRow
                          label={t(($) => $.transcript.details_reason)}
                          value={reasonLabel}
                        />
                      )}
                      {createdLabel && (
                        <RunDetailRow label={t(($) => $.transcript.details_created)} value={createdLabel} />
                      )}
                      {startedLabel && (
                        <RunDetailRow label={t(($) => $.transcript.details_started)} value={startedLabel} />
                      )}
                      {completedLabel && (
                        <RunDetailRow label={t(($) => $.transcript.details_completed)} value={completedLabel} />
                      )}
                      {/* 原始持久化诊断，放最后、带自己的分隔线。它是服务器与 daemon
                          写给日志与归类用的英文 prose，所以标为"技术细节"——翻译过的
                          标题盖着未翻译的内容——而不是当作运行原因展示（#7411）。
                          这里仍是"我的保留工作在哪棵 worktree"可以读出的地方。 */}
                      {task.error && (
                        <>
                          <div className="my-2 h-px bg-border" />
                          <RunDetailRow
                            label={t(($) => $.transcript.details_diagnostics)}
                            value={task.error}
                            mono
                          />
                        </>
                      )}
                      {usage && (
                        <>
                          <div className="my-2 h-px bg-border" />
                          <RunDetailRow
                            label={t(($) => $.transcript.details_input)}
                            value={formatTokens(usage.input)}
                          />
                          <RunDetailRow
                            label={t(($) => $.transcript.details_output)}
                            value={formatTokens(usage.output)}
                          />
                          {usage.cacheRead > 0 && (
                            <RunDetailRow
                              label={t(($) => $.transcript.details_cache_read)}
                              value={formatTokens(usage.cacheRead)}
                            />
                          )}
                          {usage.cacheWrite > 0 && (
                            <RunDetailRow
                              label={t(($) => $.transcript.details_cache_write)}
                              value={formatTokens(usage.cacheWrite)}
                            />
                          )}
                          <RunDetailRow
                            label={t(($) => $.transcript.details_cost)}
                            value={formatUsd(usage.cost)}
                          />
                        </>
                      )}
                    </div>
                  </PopoverContent>
                </Popover>
              )}
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => onOpenChange(false)}
                aria-label={t(($) => $.transcript.close)}
                className="text-muted-foreground"
              >
                <X className="h-4 w-4" />
              </Button>
            </div>
          </div>
        </div>

        {/* ── 运行产出了什么 ──────────────────────────────────────────── */}
        <RunOutcomeRow outcome={outcome} branch={task.branch_name} />

        {/* ── 时间花在哪 ──────────────────────────────────────────────── */}
        {showTimeline && lanes && (
          <RunTimeline
            lanes={lanes}
            toolKinds={toolKinds}
            selectedOffsetMs={selectedOffsetMs}
            onSeek={handleSeek}
          />
        )}

        {/* ── 可选头部插槽（如 webhook 载荷预览） ── */}
        {headerSlot && (
          <div className="border-b px-4 py-3 shrink-0 bg-muted/20">
            {headerSlot}
          </div>
        )}

        {open && <TaskInteractionHistory taskId={task.id} active={isLive} />}
        {open && <div className="shrink-0 border-b px-4 py-2">
          <Button variant="outline" size="sm" onClick={() => setRuntimeContextOpen(true)}>{t(($) => $.transcript.runtime_context)}</Button>
          {runtimeContextOpen && <RunContextDialog taskId={task.id} open onOpenChange={setRuntimeContextOpen} />}
        </div>}

        {/* ── 列表工具栏：左搜索，右侧两个菜单 ── */}
        <div className="flex items-center gap-2 border-b px-4 py-1.5 shrink-0">
          <label className="flex h-7 min-w-0 max-w-xs flex-1 items-center gap-1.5 rounded-md bg-muted px-2 text-caption">
            <Search aria-hidden className="h-3 w-3 shrink-0 text-faint-foreground" />
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t(($) => $.transcript.search_placeholder)}
              aria-label={t(($) => $.transcript.search_placeholder)}
              className="min-w-0 flex-1 bg-transparent text-caption outline-none placeholder:text-faint-foreground"
            />
          </label>
          <span className="ml-auto shrink-0 whitespace-nowrap text-caption text-muted-foreground">
            {activeFilterKeys.length > 0 || trimmedQuery.length > 0
              ? t(($) => $.transcript.steps_filtered, {
                  shown: filteredSteps.length,
                  total: steps.length,
                })
              : t(($) => $.transcript.steps_count, { count: steps.length })}
          </span>
          {filterOptions.length > 0 && (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    variant={activeFilterKeys.length > 0 ? "brand" : "ghost"}
                    size="sm"
                    aria-label={t(($) => $.transcript.filter)}
                    className={activeFilterKeys.length > 0 ? undefined : "text-muted-foreground"}
                  />
                }
              >
                <Filter className="h-3 w-3" />
                <span className="hidden sm:inline">{t(($) => $.transcript.filter)}</span>
                {activeFilterKeys.length > 0 && (
                  <span className="ml-0.5 rounded-full bg-brand-foreground/20 px-1.5 py-0 text-micro font-medium tabular-nums">
                    {activeFilterKeys.length}
                  </span>
                )}
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-auto">
                {filterOptions.map(([value, option]) => (
                  <DropdownMenuCheckboxItem
                    key={value}
                    checked={selectedFilterKeys.includes(value)}
                    onCheckedChange={() => toggleFilterKey(value)}
                  >
                    <span className="flex items-center gap-1.5">
                      <StepIcon
                        step={option.step}
                        className="h-3 w-3 shrink-0 text-muted-foreground"
                      />
                      {option.label}
                    </span>
                  </DropdownMenuCheckboxItem>
                ))}
                {selectedFilterKeys.length > 0 && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem onClick={clearFilters} className="text-muted-foreground">
                      {t(($) => $.transcript.clear_filters)}
                    </DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          {/* 排序与复制是进入阅读时谁都不需要的运行级动作，所以收进一个溢出控件
              而不是两个常驻按钮。 */}
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t(($) => $.transcript.more_actions)}
                  className="text-muted-foreground"
                />
              }
            >
              <MoreHorizontal className="h-3.5 w-3.5" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              <DropdownMenuItem
                onClick={() =>
                  handleSortDirectionChange(
                    sortDirection === "chronological" ? "newest_first" : "chronological",
                  )
                }
              >
                {sortDirection === "chronological" ? (
                  <ArrowUpNarrowWide className="h-3.5 w-3.5" />
                ) : (
                  <ArrowDownNarrowWide className="h-3.5 w-3.5" />
                )}
                {sortDirection === "chronological"
                  ? t(($) => $.transcript.sort_newest_first)
                  : t(($) => $.transcript.sort_chronological)}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={handleCopyAll}>
                {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
                {copyTranscriptLabel}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>

        {/* ── 步骤列表，选中某步时的检查器 ── */}
        <div className="flex min-h-0 flex-1">
          <div className="flex min-w-0 flex-1 flex-col">
            {displayRows.length === 0 ? (
              <div className="flex h-full items-center justify-center text-body text-muted-foreground">
                {isAntigravityLiveEmpty ? (
                  <div className="flex max-w-md items-center gap-2 px-4 text-center">
                    <Clock className="h-4 w-4 shrink-0" />
                    {t(($) => $.transcript.antigravity_live_unavailable)}
                  </div>
                ) : isLive && steps.length === 0 ? (
                  <div className="flex items-center gap-2">
                    <Loader2 className="h-4 w-4 animate-spin" />
                    {t(($) => $.transcript.waiting_events)}
                  </div>
                ) : steps.length === 0 ? (
                  t(($) => $.transcript.no_data)
                ) : (
                  t(($) => $.transcript.no_matches)
                )}
              </div>
            ) : (
              // 虚拟化：数千步的运行也只挂载有限数量的 DOM 行（#5733）。
              <Virtuoso
                key={listEpoch}
                ref={virtuosoRef}
                style={{ height: "100%" }}
                data={displayRows}
                firstItemIndex={firstItemIndex}
                // 打开实时正序转录并钉在最新一步（#5921）；每次 listEpoch 重挂载后
                // 在任务/排序/过滤变化时重新应用本设置。
                initialTopMostItemIndex={
                  isLive && sortDirection !== "newest_first"
                    ? { index: "LAST", align: "end" }
                    : 0
                }
                followOutput={(atBottom) =>
                  isLive && sortDirection !== "newest_first" && atBottom ? "auto" : false
                }
                atBottomThreshold={FOLLOW_EDGE_THRESHOLD}
                atTopThreshold={FOLLOW_EDGE_THRESHOLD}
                atTopStateChange={(atTop) => followCtl.onAtEdgeChange(atTop)}
                scrollerRef={handleScrollerRef}
                computeItemKey={(_, row) => row.seq}
                components={LIST_COMPONENTS}
                itemContent={(_, row) => (
                  <TranscriptRow
                    row={row}
                    runStartMs={runStartMs}
                    isLive={isLive}
                    selectedSeq={selectedSeq}
                    expanded={row.kind === "group" && expandedGroups.has(row.seq)}
                    onToggleGroup={handleToggleGroup}
                    onSelect={setSelectedSeq}
                  />
                )}
              />
            )}
          </div>
          {selectedStep && (
            <StepInspector
              step={selectedStep}
              runStartMs={runStartMs}
              onClose={() => setSelectedSeq(null)}
            />
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

// 本视图对 provider 的叫法与运行时列表不同。daemon 对 Claude 没有显示名覆盖，
// 共享格式化器只会答 "Claude"——但运行的诊断一向管这个工具叫 "Claude Code"，
// 且 `claude-code` 是旧任务上仍然存在的历史 provider 值（光做首字母大写会读成
// "Claude-code"）。其他 provider 一律交给共享格式化器，让这一行再也不会像从前
// 那样与运行时列表漂移（#5260）。
const TRANSCRIPT_PROVIDER_LABELS: Record<string, string> = {
  claude: "Claude Code",
  "claude-code": "Claude Code",
};

// 把 provider 名转成转录视图的显示名。
function transcriptProviderLabel(provider: string): string {
  return TRANSCRIPT_PROVIDER_LABELS[provider.toLowerCase()] ?? providerDisplayName(provider);
}

// ─── 事实行分隔符 ─────────────────────────────────────────────────────────

function FactDot() {
  return (
    <span aria-hidden className="text-faint-foreground">
      ·
    </span>
  );
}

// ─── 结果行 ────────────────────────────────────────────────────────────────

/**
 * 运行产出了什么，放在证据之上。对多数读者这就是全部答案——"它碰了这些文件、
 * 跑了这些命令"足以判定"能信吗"，一个步骤都不用打开。
 *
 * 运行没有可命名的产出时不渲染任何东西；一行全零读起来像"它没干活"，而不是
 * "我们无可总结"。
 */
function RunOutcomeRow({
  outcome,
  branch,
}: {
  outcome: ReturnType<typeof buildRunOutcome>;
  branch?: string | null;
}) {
  const { t } = useT("agents");
  if (!outcome) return null;

  const chip = "inline-flex shrink-0 items-center gap-1.5 rounded-md border bg-surface px-2 py-1 text-caption";
  return (
    <div className="flex shrink-0 items-center gap-2 overflow-hidden border-b bg-muted/40 px-4 py-1.5">
      <span className="shrink-0 text-micro text-faint-foreground">
        {t(($) => $.transcript.outcome_label)}
      </span>
      {outcome.paths.length > 0 && (
        <span className={chip} title={outcome.paths.join("\n")}>
          <FilePen aria-hidden className="h-3 w-3 text-muted-foreground" />
          <span className="font-medium">
            {t(($) => $.transcript.outcome_files, { count: outcome.paths.length })}
          </span>
          {outcome.addedLines > 0 && (
            <span className="tabular-nums text-success">+{outcome.addedLines}</span>
          )}
          {outcome.removedLines > 0 && (
            <span className="tabular-nums text-destructive">−{outcome.removedLines}</span>
          )}
        </span>
      )}
      {outcome.commandCount > 0 && (
        <span className={chip}>
          <Terminal aria-hidden className="h-3 w-3 text-muted-foreground" />
          <span className="font-medium">
            {t(($) => $.transcript.outcome_commands, { count: outcome.commandCount })}
          </span>
        </span>
      )}
      {branch && (
        <span className={cn(chip, "min-w-0")}>
          <GitBranch aria-hidden className="h-3 w-3 shrink-0 text-muted-foreground" />
          <span className="truncate font-mono text-micro">{branch}</span>
        </span>
      )}
    </div>
  );
}

// ─── 行 ───────────────────────────────────────────────────────────────────

interface TranscriptRowProps {
  row: TraceRow;
  runStartMs?: number;
  isLive: boolean;
  selectedSeq: number | null;
  expanded: boolean;
  onToggleGroup: (seq: number) => void;
  onSelect: (seq: number) => void;
}

// 按行类型分流渲染：组行 → GroupRow；文本（智能体叙述）→ ProseRow；其余
// （调用/思考/错误）→ StepRow。
function TranscriptRow(props: TranscriptRowProps) {
  const { row } = props;
  if (isGroupRow(row)) return <GroupRow {...props} row={row} />;
  if (!isCallStep(row) && row.kind === "text") return <ProseRow {...props} row={row} />;
  return <StepRow {...props} row={row} />;
}

/** 偏移列：这一步发生在运行的哪个时刻。 */
function OffsetCell({ startedAt, runStartMs }: { startedAt?: string; runStartMs?: number }) {
  const locale = useLocale();
  const at = timeMs(startedAt);
  const label = at !== undefined && runStartMs !== undefined ? formatOffset(at - runStartMs) : "";
  return (
    <span
      className="w-11 shrink-0 pt-0.5 text-right font-mono text-micro tabular-nums text-faint-foreground"
      title={startedAt ? new Date(startedAt).toLocaleString(locale) : undefined}
    >
      {label}
    </span>
  );
}

// 右列的时长单元格：调用仍无结果（实时进行中）时转圈；无时长数据时占位空位。
function DurationCell({ ms, pending }: { ms?: number; pending?: boolean }) {
  const { t } = useT("agents");
  if (pending) {
    return (
      <span className="shrink-0 pt-0.5 text-micro text-info" title={t(($) => $.transcript.step_running)}>
        <Loader2 aria-hidden className="h-3 w-3 animate-spin" />
        <span className="sr-only">{t(($) => $.transcript.step_running)}</span>
      </span>
    );
  }
  if (ms === undefined) return <span className="w-12 shrink-0" />;
  return (
    <StepDuration
      ms={ms}
      unknownLabel={t(($) => $.transcript.step_duration_unknown)}
      className="w-12 shrink-0 pt-0.5 text-right font-mono text-micro tabular-nums text-faint-foreground"
    />
  );
}

function StepDuration({
  ms,
  unknownLabel,
  className,
}: {
  ms: number;
  unknownLabel: string;
  className?: string;
}) {
  const unknown = ms <= 0;
  return (
    <span className={className} title={unknown ? unknownLabel : undefined}>
      <span aria-hidden={unknown || undefined}>{formatStepDuration(ms)}</span>
      {unknown && <span className="sr-only">{unknownLabel}</span>}
    </span>
  );
}

/**
 * 智能体自己的话，用正文字号与阅读行长呈现。
 *
 * 这是改版所依赖的层级反转：报告过去长得跟一次 `Read` 一模一样，唯一值得读的
 * 东西反而最难找。叙述是内容、保持展开；工具调用是证据、折叠待查。
 *
 * 这里刻意不显示身份。一份转录是一个智能体跑的一次运行——`TraceMessageStep` 不带
 * 每步的行动者，所以在行上放头像与名字等于每一行都重复两个相同的值，而头部已经
 * 声明过了。重复它们要在 12px 正文上方多占一行半粗 12px，在一串短步骤的运行上，
 * 会让行里最重的元素成为信息量最低的那个。
 *
 * 行保留的是它的"种类"：与其他行相同的 `StepIcon` 列，让筛选里的 "Agent" 有个
 * 可指的对象。种类而非身份——字形说的是"智能体自己的话"，这正是该筛选面要选的，
 * 而且它用 12px 说出来、不重复字符串。
 */
function ProseRow({ row, runStartMs }: TranscriptRowProps & { row: TraceMessageStep }) {
  return (
    <div className="group flex items-start gap-2 px-4 py-2.5">
      <OffsetCell startedAt={row.startedAt} runStartMs={runStartMs} />
      <span aria-hidden className="mt-1 w-0.5 self-stretch rounded-full bg-success" />
      <StepIcon step={row} className="mt-1 h-3 w-3 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1 pr-2">
        <RichContent
          content={row.item.content ?? ""}
          density="compact"
          className="transcript-prose max-w-[62rem]"
        />
      </div>
    </div>
  );
}

/** 一次调用、一个思考块或一个错误：一行，详情在检查器里点一下就到。 */
function StepRow({
  row,
  runStartMs,
  isLive,
  selectedSeq,
  onSelect,
}: TranscriptRowProps & { row: TraceStep }) {
  const { t } = useT("agents");
  const summaryLabels = useMemo<TraceSummaryLabels>(
    () => ({
      morePaths: (path, extraCount) =>
        t(($) => $.transcript.patch_summary_more, { path, extra: extraCount }),
    }),
    [t],
  );

  const call = isCallStep(row) ? row : null;
  const label = call
    ? call.tool || t(($) => $.transcript.kind_tool)
    : row.kind === "thinking"
      ? t(($) => $.transcript.kind_thinking)
      : t(($) => $.transcript.kind_error);
  const summary = call
    ? callSummary(call, summaryLabels)
    : firstLineOf((row as TraceMessageStep).item.content);
  const pending = call !== null && isLive && !call.result;
  const selected = selectedSeq === row.seq;

  return (
    <button
      type="button"
      onClick={() => onSelect(row.seq)}
      aria-current={selected ? "true" : undefined}
      className={cn(
        "group flex w-full items-start gap-2 px-4 py-1.5 text-left transition-colors",
        selected ? "bg-brand/8" : "hover:bg-accent/40",
        row.kind === "error" && "bg-destructive/5",
      )}
    >
      <OffsetCell startedAt={row.startedAt} runStartMs={runStartMs} />
      <span
        aria-hidden
        className={cn(
          "mt-0.5 w-0.5 self-stretch rounded-full",
          selected ? "bg-brand" : row.kind === "error" ? "bg-destructive" : "bg-border",
        )}
      />
      <StepIcon
        step={row}
        className={cn(
          "mt-1 h-3 w-3 shrink-0",
          row.kind === "error" ? "text-destructive" : "text-muted-foreground",
        )}
      />
      <span className="flex min-w-0 flex-1 items-baseline gap-1.5">
        <span
          className={cn(
            "shrink-0 text-caption font-medium",
            row.kind === "error" ? "text-destructive" : "text-foreground",
          )}
        >
          {label}
        </span>
        <span
          className={cn(
            "truncate text-micro text-muted-foreground",
            call && "font-mono",
          )}
        >
          {summary}
        </span>
      </span>
      <DurationCell ms={call?.durationMs} pending={pending} />
    </button>
  );
}

/** 连续同工具调用在展开前被折叠成一行。 */
function GroupRow({
  row,
  runStartMs,
  selectedSeq,
  expanded,
  onToggleGroup,
  onSelect,
}: TranscriptRowProps & { row: TraceGroupRow }) {
  const { t } = useT("agents");
  const summaryLabels = useMemo<TraceSummaryLabels>(
    () => ({
      morePaths: (path, extraCount) =>
        t(($) => $.transcript.patch_summary_more, { path, extra: extraCount }),
    }),
    [t],
  );

  return (
    <div className={cn(selectedSeq !== null && row.steps.some((s) => s.seq === selectedSeq) && "bg-brand/5")}>
      <button
        type="button"
        onClick={() => onToggleGroup(row.seq)}
        aria-expanded={expanded}
        className="group flex w-full items-start gap-2 px-4 py-1.5 text-left transition-colors hover:bg-accent/40"
      >
        <OffsetCell startedAt={row.startedAt} runStartMs={runStartMs} />
        <span aria-hidden className="mt-0.5 w-0.5 self-stretch rounded-full bg-border" />
        <ChevronRight
          aria-hidden
          className={cn(
            "mt-1 h-3 w-3 shrink-0 text-faint-foreground transition-transform",
            expanded && "rotate-90",
          )}
        />
        <span className="flex min-w-0 flex-1 items-baseline gap-1.5">
          <span className="shrink-0 text-caption font-medium">{row.tool}</span>
          {/* 说它第一个碰了什么，而不是只报个数："Read · 4 次调用"恰恰藏起了让
              折叠组可扫读的那一个细节。 */}
          <span className="truncate font-mono text-micro text-muted-foreground">
            {callSummary(row.steps[0]!, summaryLabels)}
          </span>
          <span className="shrink-0 text-micro text-faint-foreground">
            {t(($) => $.transcript.group_calls, { count: row.steps.length })}
          </span>
        </span>
        <DurationCell ms={row.durationMs} />
      </button>
      {expanded && (
        <div className="ml-[4.4rem] border-l pl-3">
          {row.steps.map((step) => (
            <button
              key={step.seq}
              type="button"
              onClick={() => onSelect(step.seq)}
              className={cn(
                "flex w-full items-baseline gap-2 rounded px-2 py-1 text-left text-micro transition-colors",
                selectedSeq === step.seq ? "bg-brand/10" : "hover:bg-accent/40",
              )}
            >
              <span className="truncate font-mono text-muted-foreground">
                {callSummary(step, summaryLabels) || step.tool}
              </span>
              {step.durationMs === undefined ? (
                <span className="ml-auto shrink-0" />
              ) : (
                <StepDuration
                  ms={step.durationMs}
                  unknownLabel={t(($) => $.transcript.step_duration_unknown)}
                  className="ml-auto shrink-0 font-mono tabular-nums text-faint-foreground"
                />
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * 一次调用在列表里值一行字：它被要求做什么。
 *
 * 刻意取调用的入参而非结果——结果行正是 base64 截图与 8KB 命令转储过去堆在列表
 * 中间的位置。只有从未见过的调用才回退到它的输出。
 */
function callSummary(step: TraceCallStep, labels: TraceSummaryLabels): string {
  if (step.call) {
    const summary = traceToolArgSummary(step.call.input, labels);
    if (summary) return summary;
  }
  if (!step.result) return "";
  if (readImageResult(step.result.output)) return "";
  return traceEventSummary({ type: "tool_result", output: step.result.output });
}

function firstLineOf(value: string | undefined): string {
  return value?.split("\n").find((line) => line.trim().length > 0) ?? "";
}

// ─── 检查器 ──────────────────────────────────────────────────────────────

/**
 * 单个步骤的证据，按需打开。
 *
 * 输入与结果上下堆叠而非分页签：这个宽度下两者都放得下，分页签则要把一半证据
 * 藏在一记点击后面，却什么也没省。
 */
function StepInspector({
  step,
  runStartMs,
  onClose,
}: {
  step: TraceStep;
  runStartMs?: number;
  onClose: () => void;
}) {
  const { t } = useT("agents");
  const [copied, showCopied] = useCopyFeedback();

  const at = timeMs(step.startedAt);
  const offset = at !== undefined && runStartMs !== undefined ? formatOffset(at - runStartMs) : null;

  const call = isCallStep(step) ? step : null;
  const message = call ? null : (step as TraceMessageStep);

  const handleCopy = useCallback(() => {
    const parts: string[] = [];
    if (call) {
      if (call.call) parts.push(traceEventCopyText(call.call));
      if (call.result) parts.push(traceEventCopyText(call.result));
    } else if (message) {
      parts.push(traceEventCopyText(message.item));
    }
    void copyText(redactSecrets(parts.join("\n\n"))).then((ok) => {
      if (!ok) return;
      showCopied();
    });
  }, [call, message, showCopied]);

  const title =
    call
      ? call.tool || t(($) => $.transcript.kind_tool)
      : step.kind === "thinking"
        ? t(($) => $.transcript.kind_thinking)
        : step.kind === "text"
          ? t(($) => $.transcript.kind_text)
          : t(($) => $.transcript.kind_error);

  return (
    <aside className="flex w-[26rem] shrink-0 flex-col border-l bg-muted/25">
      <div className="flex items-center gap-2 border-b px-3 py-2">
        <StepIcon step={step} className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className="shrink-0 text-label font-semibold">{title}</span>
        <span className="flex min-w-0 flex-1 items-center gap-1.5 text-micro text-muted-foreground">
          {offset && <span className="font-mono tabular-nums">{offset}</span>}
          {call?.durationMs !== undefined && (
            <>
              <FactDot />
              <StepDuration
                ms={call.durationMs}
                unknownLabel={t(($) => $.transcript.step_duration_unknown)}
                className="font-mono tabular-nums"
              />
            </>
          )}
        </span>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={handleCopy}
          aria-label={t(($) => call?.result?.outputTruncated ? $.transcript.copy_step_preview : $.transcript.copy_step)}
          title={t(($) => call?.result?.outputTruncated ? $.transcript.copy_step_preview : $.transcript.copy_step)}
          className="shrink-0 text-muted-foreground"
        >
          {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={onClose}
          aria-label={t(($) => $.transcript.close_step)}
          className="shrink-0 text-muted-foreground"
        >
          <X className="h-3.5 w-3.5" />
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {call ? (
          <>
            {call.call && (
              <InspectorSection label={t(($) => $.transcript.step_input)}>
                <StepBody item={call.call} />
              </InspectorSection>
            )}
            {call.result && (
              <InspectorSection label={t(($) => $.transcript.step_result)}>
                <StepBody item={call.result} />
              </InspectorSection>
            )}
          </>
        ) : (
          <InspectorSection label={title}>
            <ToolDetailSurface text={redactSecrets(message?.item.content ?? "")} />
          </InspectorSection>
        )}
      </div>
    </aside>
  );
}

// 检查器里的一个小节：小标题 + 内容。
function InspectorSection({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <section className="border-b last:border-b-0">
      <h3 className="px-3 pt-2 text-micro font-medium uppercase tracking-wide text-faint-foreground">
        {label}
      </h3>
      <div className="px-1 pb-2">{children}</div>
    </section>
  );
}

/** 一份载荷，按它本来的样子渲染：图片显示成图，diff/patch/整文件写入各用各的
 *  展示面，其余按文本代码块处理（超长截断并标注）。 */
function StepBody({ item }: { item: TimelineItem }) {
  const { t } = useT("agents");
  const detail = useMemo(() => traceEventDetail(item), [item]);
  const image = useMemo(() => readImageResult(item.output), [item.output]);

  if ((item.outputTruncated || (item.output?.length ?? 0) > 8000) && item.id && item.taskId) return <TaskOutput key={item.id} item={item} />;

  // 截图是图片，不是塞在 <pre> 里的一串 200KB base64。
  if (image) {
    return (
      <figure className="px-2 py-1">
        <img
          src={`data:${image.mediaType};base64,${image.base64}`}
          alt={t(($) => $.transcript.image_result)}
          className="max-h-80 w-full rounded-md border object-contain"
        />
        <figcaption className="pt-1 text-micro text-faint-foreground">
          {t(($) => $.transcript.image_result)} · {formatBytes(base64ByteLength(image.base64))}
        </figcaption>
      </figure>
    );
  }

  switch (detail.kind) {
    case "diff":
      return <DiffDetailSurface lines={detail.lines} path={detail.path} />;
    case "patch":
      return <PatchDetailSurface files={detail.files} truncated={detail.truncated} />;
    case "file":
      return (
        <FileWriteSurface text={detail.text} lineCount={detail.lineCount} path={detail.path} />
      );
    default: {
      const text = detail.text;
      const clipped =
        text.length > 8000 ? `${redactSecrets(text.slice(0, 8000))}\n... (truncated)` : redactSecrets(text);
      const path = item.type === "tool_use" ? readPathFromInput(item.input) : undefined;
      return <ToolDetailSurface text={clipped} language={path ? languageForPath(path) : undefined} />;
    }
  }
}

function readPathFromInput(input: Record<string, unknown> | undefined): string | undefined {
  if (!input) return undefined;
  const path = input.file_path ?? input.path;
  return typeof path === "string" ? path : undefined;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
