import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { CheckCircle2 } from "lucide-react";
import type { TimelineEntry } from "@multica/core/types";
import { isDeletedComment } from "@multica/core/issues/comment-deletion";
import { useActorName } from "@multica/core/workspace/hooks";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

// ---------------------------------------------------------------------------
// 本文件：ThreadMinimap —— Linear 风格的评论线程快捷跳转轨道
// ---------------------------------------------------------------------------
//
// 垂直一列刻度线，叠在问题详情滚动区右缘（贴滚动条内侧），每个顶层评论线程
// 一颗刻度（折叠的已解决评论条也算——它们同样是跳转目标）。线程当前位于
// 滚动视口内的刻度渲染得更深，让轨道兼任"你在哪"的缩略图。悬停会以
// Dock 式波浪放大刻度（悬停的刻度最高、邻居渐弱），并显示一张预览卡
// （加粗首行 + 弱化的正文摘要；线程带解决标记时还会加一枚"已解决"徽章——
// 折叠的已解决线程在别处与打开线程无法区分，这里能）。点击即跳到该线程。
//
// 特意骑在滚动条一侧：它看起来、用起来都像滚动类控件，而此类先例
// （滚动条本身、编辑器缩略图、浮动的章节大纲）全都待在右侧——指针本就在那，
// 从滚动到跳转的路程很短（MUL-4522）。
//
// 预览卡是轨道自持的唯一一张卡，不是每颗刻度的 popover：悬停意图延迟在
// 指针进入轨道时只付一次，之后指针在刻度间滑过时卡片即时换内容、滑向被
// 悬停刻度。若按刻度各挂 popover，每跨一颗都要重付打开延迟和出入场动画，
// 连续扫轨道时读起来就是卡顿。
//
// 轨道刻意跳过活动分组：它们是时间线上的噪音，不是导航目的地。

/** 线程数达到这个下限轨道才配占这些像素。 */
const MIN_THREADS = 2;

/** 卡片首次出现的意图延迟；之后的滑过是即时的。 */
const PREVIEW_OPEN_DELAY_MS = 150;
/** 离开时的宽限期——足够从轨道走到卡片上。 */
const PREVIEW_CLOSE_DELAY_MS = 150;

// ---------------------------------------------------------------------------
// 悬停波浪 —— Dock 式邻近放大
// ---------------------------------------------------------------------------
//
// 指针沿轨道移动时，每颗刻度按与指针距离的余弦衰减缩放：悬停的刻度登顶、
// 邻居像波浪一样渐弱。每次 pointermove 用直接样式写入驱动（不触发 React
// 重渲染），在一次 rAF 内批量"先读后写"，作用于对合成器友好的原生 `scale`
// 属性；刻度上 100ms 的 ease-out 过渡既平滑了指针采样间的跳变，也在离开时
// 收拢回落。只有被悬停的刻度变深——邻居放大但保持自己的颜色。

/** 刻度在此距离（px）外不再感受到波浪——约 4 个刻度间距。 */
const WAVE_RADIUS_PX = 56;
/** 被悬停刻度的峰值水平缩放（12px 基础 → 约 20px）。 */
const WAVE_MAX_SCALE = 1.7;

/**
 * 圆心与指针相距 `distancePx` 时某刻度的水平缩放。
 * 余弦平方钟形：在峰值与半径边缘都平滑（无折点）。
 */
export function waveScale(distancePx: number): number {
  const d = Math.abs(distancePx);
  if (d >= WAVE_RADIUS_PX) return 1;
  const t = Math.cos(((d / WAVE_RADIUS_PX) * Math.PI) / 2);
  return 1 + (WAVE_MAX_SCALE - 1) * t * t;
}

/**
 * `commentPreview` 应用的截断上限。预览卡视觉上有截断（`truncate` /
 * `line-clamp-3`），但 agent 评论动辄几十 KB 的 markdown——在这里封顶，
 * 让扁平化后的字符串（以及由此派生的 aria-label）保持小巧，
 * 而不是把整篇评论塞进 DOM。
 */
const PREVIEW_TITLE_MAX = 200;
const PREVIEW_BODY_MAX = 300;

/**
 * 把评论 markdown 扁平化为纯文本预览：`title` 是首个非空行（卡片中加粗），
 * `body` 是其余行合并成的一段弱化摘要。镜像聊天列表 `toPreview` 的扁平化
 * （丢弃代码围栏、剥掉 md 记号），但保留 minimap 卡片要渲染的首行/正文拆分。
 */
export function commentPreview(markdown: string): { title: string; body: string } {
  const lines = markdown
    .replace(/```[\s\S]*?```/g, " ")
    .split(/\r?\n/)
    .map((line) =>
      line
        .replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
        .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
        .replace(/^\s*(?:[-+*]|\d+[.)])\s+/, "")
        .replace(/[#*`>~]/g, "")
        .replace(/\s+/g, " ")
        .trim(),
    )
    .filter(Boolean);
  return {
    title: (lines[0] ?? "").slice(0, PREVIEW_TITLE_MAX),
    body: lines.slice(1).join(" ").slice(0, PREVIEW_BODY_MAX),
  };
}

export interface ThreadMinimapThread {
  /** 根评论 id —— 也是渲染行的 `comment-${id}` DOM 锚点。 */
  id: string;
  /** 线程的根评论条目（预览文本 + 作者兜底）。 */
  entry: TimelineEntry;
  /**
   * 线程是否带解决标记——由调用方用 `deriveThreadResolution` 派生，
   * 因此同时覆盖"解决线程"（根评论）与"带评论解决线程"（回复），
   * 并且在用户展开某条已折叠已解决线程期间仍保持 true。
   */
  resolved: boolean;
}

interface ThreadMinimapProps {
  threads: ThreadMinimapThread[];
  /** 问题详情滚动容器；在其回调 ref 填充前为 null。 */
  scrollContainerEl: HTMLElement | null;
  onJump: (threadId: string) => void;
  /**
   * 头部面板指针此刻停留的线程。轨道会把该刻度点亮为品牌色，让两个导航器
   * 读起来是同一套坐标，而不是两套竞争列表（MUL-5755）。
   */
  highlightedThreadId?: string | null;
  /** 在页面内的定位（如 `absolute right-3 top-12 bottom-0`）——由调用方持有，同 FindBar。 */
  className?: string;
}

// ---------------------------------------------------------------------------
// useVisibleThreadIds —— "当前有哪些评论线程在屏幕上"
// ---------------------------------------------------------------------------
//
// 哪些线程与滚动视口相交，好让轨道加深它们的刻度。这刻意只归轨道所有：
// "在屏幕上"是一个集合而不是一个点，只有一列刻度能诚实地展示区段——
// 头部面板曾试着把同一集合渲染成列表行，结果读起来像坏掉的多选。
//
// 在 scroll/resize 时用 DOM rect 计算而不是 IntersectionObserver：
// Virtuoso 滚动时会挂载/卸载行——观察器会丢掉自己的目标。被卸载的行按定义
// 就在（过扫描的）视口之外，所以"没有元素"正确地计为不可见。

function sameIdSet(a: Set<string>, b: Set<string>): boolean {
  if (a.size !== b.size) return false;
  for (const v of a) if (!b.has(v)) return false;
  return true;
}

function useVisibleThreadIds(
  threadIds: readonly string[],
  scrollContainerEl: HTMLElement | null,
): Set<string> {
  const [visibleIds, setVisibleIds] = useState<Set<string>>(() => new Set());

  useEffect(() => {
    const container = scrollContainerEl;
    if (!container) return;

    let raf = 0;
    const compute = () => {
      raf = 0;
      const rect = container.getBoundingClientRect();
      const next = new Set<string>();
      for (const id of threadIds) {
        const el = document.getElementById(`comment-${id}`);
        if (!el) continue;
        const r = el.getBoundingClientRect();
        if (r.bottom > rect.top && r.top < rect.bottom) next.add(id);
      }
      setVisibleIds((prev) => (sameIdSet(prev, next) ? prev : next));
    };
    const schedule = () => {
      if (!raf) raf = requestAnimationFrame(compute);
    };

    compute();
    container.addEventListener("scroll", schedule, { passive: true });
    // 内容高度在没有滚动事件时也会变：Virtuoso 首帧后挂载行、
    // agent 流式回复变长、窗口缩放。
    const ro = new ResizeObserver(schedule);
    ro.observe(container);
    if (container.firstElementChild) ro.observe(container.firstElementChild);
    return () => {
      container.removeEventListener("scroll", schedule);
      ro.disconnect();
      if (raf) cancelAnimationFrame(raf);
    };
  }, [threadIds, scrollContainerEl]);

  return visibleIds;
}

/** 轨道自持预览卡的目标：哪颗刻度、锚在相对 shim 的哪个 Y。 */
interface PreviewAnchor {
  index: number;
  y: number;
}

/**
 * 单颗刻度按钮。
 */
function MinimapTick({
  label,
  inViewport,
  isPreviewOpen,
  isHighlighted,
  onClick,
}: {
  label: string;
  inViewport: boolean;
  /** 这颗刻度的预览就是当前打开的卡——即使指针在卡上，也保持放大态。 */
  isPreviewOpen: boolean;
  /** 头部面板正在悬停本线程的行。 */
  isHighlighted: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      onClick={onClick}
      // 宽 20px，刻度齐右端：配合轨道内侧 12px 内边距（见调用方 className），
      // 细条从面板边缘起跨 12–32px——一侧清出经典滚动条约 11px 的槽，
      // 另一侧正好停在正文列 32px 内边距前——于是无论滚动条哪种形态，
      // 它都不压滚动条、也不压正文。
      className="group/tick flex min-h-[5px] w-5 flex-[0_1_0.875rem] cursor-pointer items-center justify-end focus-visible:outline-none"
    >
      <span
        className={cn(
          // 放大是右锚定的 `scale`（对合成器友好，也正是 JS 波浪行内写入的
          // 属性），所以刻度向内生长、远离滚动条。100ms ease-out 同时充当
          // 指针采样间的平滑与离开时的回落。
          "h-0.5 w-3 origin-right rounded-full transition-[scale,background-color] duration-100 ease-out",
          inViewport ? "bg-foreground/70" : "bg-muted-foreground/30",
          "group-hover/tick:bg-foreground",
          // 无行内波浪值时的 CSS 地板态：指针停在卡上时开卡的刻度保持放大、
          // 键盘焦点无指针也能放大、reduced-motion 把波浪换成朴素的悬停放大。
          isPreviewOpen && "scale-x-[1.7] bg-foreground",
          // 面板驱动的点亮。用品牌色而非前景色：它表达"面板正指向这里"，
          // 与轨道自身的悬停/视口灰是不同的陈述，必须与两者都能区分。
          // 胜过 `inViewport`，因为它是更具体、用户驱动的状态。
          isHighlighted && "scale-x-[1.7] bg-brand",
          "group-focus-visible/tick:scale-x-[1.7] group-focus-visible/tick:bg-foreground",
          "motion-reduce:group-hover/tick:scale-x-[1.7]",
        )}
      />
    </button>
  );
}

/**
 * 线程缩略轨道（职责见文件头）。
 */
export function ThreadMinimap({
  threads,
  scrollContainerEl,
  onJump,
  highlightedThreadId,
  className,
}: ThreadMinimapProps) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();
  const threadIds = useMemo(() => threads.map((th) => th.id), [threads]);
  const visibleIds = useVisibleThreadIds(threadIds, scrollContainerEl);

  // 扁平化预览按内容逐线程缓存，让无关的时间线更新（加了表情、别处有新
  // 回复）不至于把每条评论重新扁平化一遍。
  const prevPreviewsRef = useRef<Map<string, { content: string | undefined; preview: { title: string; body: string } }>>(new Map());
  const previews = useMemo(() => {
    const next = new Map<string, { content: string | undefined; preview: { title: string; body: string } }>();
    const arr = threads.map((th) => {
      const cached = prevPreviewsRef.current.get(th.id);
      const preview =
        cached && cached.content === th.entry.content
          ? cached.preview
          : commentPreview(th.entry.content ?? "");
      next.set(th.id, { content: th.entry.content, preview });
      return preview;
    });
    prevPreviewsRef.current = next;
    return arr;
  }, [threads]);

  const shimRef = useRef<HTMLDivElement | null>(null);
  const navRef = useRef<HTMLElement | null>(null);
  const cardRef = useRef<HTMLDivElement | null>(null);

  // 悬停波浪 + 预览卡定位。指针位置存在 ref、刻度用直接样式写入缩放，
  // 让 pointermove 从不触发组件重渲染；rAF 守卫把脉冲合流为每帧一次
  // "先读后写"的批量操作。同一趟 rect 计算也派生卡片应锚定哪颗刻度，
  // 于是卡片与波浪对"被悬停的刻度"永不失和。
  const waveRafRef = useRef(0);
  const pointerYRef = useRef<number | null>(null);
  const reducedMotionRef = useRef(false);

  const [preview, setPreview] = useState<PreviewAnchor | null>(null);
  const previewRef = useRef<PreviewAnchor | null>(null);
  const pendingAnchorRef = useRef<PreviewAnchor | null>(null);
  const openTimerRef = useRef<number | null>(null);
  const closeTimerRef = useRef<number | null>(null);

  const showPreview = useCallback((anchor: PreviewAnchor | null) => {
    previewRef.current = anchor;
    setPreview((prev) =>
      prev?.index === anchor?.index && prev?.y === anchor?.y ? prev : anchor,
    );
  }, []);

  useEffect(() => {
    reducedMotionRef.current = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    return () => {
      if (waveRafRef.current) cancelAnimationFrame(waveRafRef.current);
      if (openTimerRef.current !== null) window.clearTimeout(openTimerRef.current);
      if (closeTimerRef.current !== null) window.clearTimeout(closeTimerRef.current);
    };
  }, []);

  const cancelClose = useCallback(() => {
    if (closeTimerRef.current !== null) {
      window.clearTimeout(closeTimerRef.current);
      closeTimerRef.current = null;
    }
  }, []);
  const scheduleClose = useCallback(() => {
    cancelClose();
    if (openTimerRef.current !== null) {
      window.clearTimeout(openTimerRef.current);
      openTimerRef.current = null;
    }
    closeTimerRef.current = window.setTimeout(() => {
      closeTimerRef.current = null;
      showPreview(null);
    }, PREVIEW_CLOSE_DELAY_MS);
  }, [cancelClose, showPreview]);

  const runWave = useCallback(() => {
    waveRafRef.current = 0;
    const nav = navRef.current;
    const shim = shimRef.current;
    if (!nav || !shim) return;
    const y = pointerYRef.current;
    const buttons = nav.querySelectorAll<HTMLButtonElement>("button");
    // 先读后写，绝不交错——至多一次 reflow。
    const shimTop = shim.getBoundingClientRect().top;
    const shimHeight = shim.clientHeight;
    const cardHalf = (cardRef.current?.offsetHeight ?? 96) / 2;
    const scales: string[] = [];
    let nearest: { index: number; centerY: number; dist: number } | null = null;
    buttons.forEach((b, i) => {
      if (y === null) {
        scales.push("");
        return;
      }
      const r = b.getBoundingClientRect();
      const centerY = r.top + r.height / 2;
      const dist = Math.abs(y - centerY);
      const s = reducedMotionRef.current ? 1 : waveScale(y - centerY);
      scales.push(s > 1.001 ? `${s.toFixed(3)} 1` : "");
      if (!nearest || dist < nearest.dist) nearest = { index: i, centerY, dist };
    });
    buttons.forEach((b, i) => {
      const tick = b.firstElementChild as HTMLElement | null;
      if (!tick) return;
      const s = scales[i]!;
      // 清掉行内值 = 把控制权交还给 CSS 地板态
      //（开卡的刻度 / focus-visible / reduced-motion 悬停）。
      if (s) tick.style.setProperty("scale", s);
      else tick.style.removeProperty("scale");
    });

    // 预览卡定位复用同一趟计算。钳制锚点，让卡片在两端的极限处
    // 也不会探出轨道那一列。
    if (y === null || !nearest) return;
    const { index, centerY } = nearest as { index: number; centerY: number };
    const anchor: PreviewAnchor = {
      index,
      y: Math.min(Math.max(centerY - shimTop, cardHalf + 6), shimHeight - cardHalf - 6),
    };
    pendingAnchorRef.current = anchor;
    if (previewRef.current) {
      // 已开着：滑过时卡片即时重定向——不再重计延迟。
      showPreview(anchor);
    } else if (openTimerRef.current === null) {
      openTimerRef.current = window.setTimeout(() => {
        openTimerRef.current = null;
        if (pointerYRef.current !== null) showPreview(pendingAnchorRef.current);
      }, PREVIEW_OPEN_DELAY_MS);
    }
  }, [showPreview]);
  const scheduleWave = useCallback(() => {
    if (!waveRafRef.current) waveRafRef.current = requestAnimationFrame(runWave);
  }, [runWave]);
  const handleWaveMove = useCallback(
    (e: React.PointerEvent) => {
      cancelClose();
      pointerYRef.current = e.clientY;
      scheduleWave();
    },
    [cancelClose, scheduleWave],
  );
  const handleWaveLeave = useCallback(() => {
    pointerYRef.current = null;
    scheduleWave();
    scheduleClose();
  }, [scheduleWave, scheduleClose]);

  // 键盘对齐：聚焦某颗刻度时立即把卡片锚过去——没有指针，
  // 也就没有需要防抖的"误悬停"。
  const handleFocus = useCallback(
    (e: React.FocusEvent) => {
      const nav = navRef.current;
      const shim = shimRef.current;
      const btn = (e.target as HTMLElement).closest("button");
      if (!nav || !shim || !btn) return;
      cancelClose();
      const buttons = [...nav.querySelectorAll<HTMLButtonElement>("button")];
      const index = buttons.indexOf(btn as HTMLButtonElement);
      if (index < 0) return;
      const r = btn.getBoundingClientRect();
      const cardHalf = (cardRef.current?.offsetHeight ?? 96) / 2;
      const y = r.top + r.height / 2 - shim.getBoundingClientRect().top;
      showPreview({
        index,
        y: Math.min(Math.max(y, cardHalf + 6), shim.clientHeight - cardHalf - 6),
      });
    },
    [cancelClose, showPreview],
  );

  if (threads.length < MIN_THREADS) return null;

  const activeThread = preview ? threads[preview.index] : undefined;
  const activePreview = preview ? previews[preview.index] : undefined;
  const activeTitle = activeThread && activePreview
    ? isDeletedComment(activeThread.entry) ? t(($) => $.comment.deleted_placeholder) : activePreview.title ||
      getActorName(activeThread.entry.actor_type, activeThread.entry.actor_id)
    : undefined;

  return (
    // 定位垫片；只有 nav 和卡片接收指针事件，细条永不挡内容点击。
    <div ref={shimRef} className={cn("pointer-events-none z-10 flex flex-col justify-center py-6", className)}>
      <nav
        ref={navRef}
        aria-label={t(($) => $.detail.thread_nav_label)}
        onPointerMove={handleWaveMove}
        onPointerLeave={handleWaveLeave}
        onFocusCapture={handleFocus}
        onBlurCapture={scheduleClose}
        // 高度受限 + 刻度可收缩：线程数超过轨道高度时，flex 压缩间距
        //（直到 min-h）而不是溢出。
        className="pointer-events-auto flex max-h-full flex-col overflow-hidden"
      >
        {threads.map((thread, i) => {
          const title = isDeletedComment(thread.entry) ? t(($) => $.comment.deleted_placeholder) :
            previews[i]!.title ||
            getActorName(thread.entry.actor_type, thread.entry.actor_id);
          return (
            <MinimapTick
              key={thread.id}
              // 卡片是已解决状态的可视通道，但读屏器看不到它——刻度自身的名字
              // 是聚焦时唯一会被播报的东西，所以状态也由它承载。
              label={
                thread.resolved
                  ? t(($) => $.detail.thread_nav_resolved_label, { title })
                  : title
              }
              inViewport={visibleIds.has(thread.id)}
              isPreviewOpen={preview?.index === i}
              isHighlighted={highlightedThreadId === thread.id}
              onClick={() => onJump(thread.id)}
            />
          );
        })}
      </nav>

      {/* 轨道唯一的预览卡。挂载时不带入场动画（悬停意图延迟已挡住误触闪现；
          用户既然等了，立刻出内容才是响应性的选择），刻度间用短 transform
          过渡滑动。悬停卡片保持其打开，好让卡片文字可被选中。它向内
          （向左、朝内容方向）打开——这是滚动条旁边唯一有空间的方向。
          已解决徽章前置，让状态先于内容被读到，配色沿用 CommentCard
          的 Resolution 徽章同款 `text-success`。 */}
      {preview && activeThread && activePreview && (
        <div
          ref={cardRef}
          onPointerEnter={cancelClose}
          onPointerLeave={scheduleClose}
          className="pointer-events-auto absolute right-8 top-0 w-72 rounded-lg bg-popover p-2.5 text-body text-popover-foreground shadow-md ring-1 ring-foreground/10 transition-transform duration-150 ease-out motion-reduce:transition-none"
          style={{ transform: `translateY(${preview.y}px) translateY(-50%)` }}
        >
          {activeThread.resolved && (
            <p className="mb-1 flex items-center gap-1.5 text-caption font-medium text-success">
              <CheckCircle2 className="h-3.5 w-3.5 shrink-0" />
              {t(($) => $.comment.resolve.thread_resolved_badge)}
            </p>
          )}
          <p className="truncate text-body font-semibold text-foreground">{activeTitle}</p>
          {activePreview.body && (
            <p className="mt-1 line-clamp-3 text-body text-muted-foreground">{activePreview.body}</p>
          )}
        </div>
      )}
    </div>
  );
}
