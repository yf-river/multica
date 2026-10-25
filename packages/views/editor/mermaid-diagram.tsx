"use client";

/**
 * MermaidDiagram——沙箱化的 Mermaid 图渲染器（行内展示）。
 *
 * 从 `readonly-content.tsx` 抽出，让 Tiptap 代码块 NodeView
 * （`code-block-view.tsx`）在代码块语言为 `mermaid` 时渲染同一组件。此前
 * Mermaid 只在只读 markdown 界面（评论卡片）工作——issue 描述始终留在
 * Tiptap 编辑器里，从不渲染图。
 *
 * 主题变量从宿主的 CSS 自定义属性读取，让图的颜色匹配亮/暗模式。SVG 渲染
 * 在沙箱 iframe 里，避免 Mermaid 的运行时样式表漏进页面。
 *
 * 本组件负责行内呈现；全屏体验在 `MermaidViewer`。两者读同一个渲染出的
 * SVG，所以导出或放大看到的永远就是你正在看的。
 */

import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import { Check, Copy, Maximize2 } from "lucide-react";
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../i18n";
import { useDragToScroll } from "./hooks/use-drag-to-scroll";
import { MermaidViewer } from "./mermaid-viewer";
import type { Size } from "./utils/zoom-transform";

type MermaidAPI = typeof import("mermaid").default;

let mermaidPromise: Promise<MermaidAPI> | null = null;

/** 惰性加载 mermaid（模块级缓存，整个会话只载一次）。 */
function getMermaid(): Promise<MermaidAPI> {
  mermaidPromise ??= import("mermaid").then(({ default: mermaid }) => mermaid);

  return mermaidPromise;
}

/** 把任意 CSS 颜色换算成 Mermaid 能吃的 legacy 语法。 */
function toLegacyColor(color: string, fallback: string, ownerDocument: Document): string {
  const canvas = ownerDocument.createElement("canvas");
  canvas.width = 1;
  canvas.height = 1;
  const context = canvas.getContext("2d", { willReadFrequently: true });
  if (!context) return fallback;

  // Mermaid 的颜色解析器只支持 legacy 语法。canvas 能解析 oklch() 等现代
  // CSS Color 4 值，getImageData 随后给出 Mermaid 可安全消费的 8 位 sRGB
  // 字节。
  context.fillStyle = "#000";
  context.fillStyle = color || fallback;
  context.fillRect(0, 0, 1, 1);
  const [red, green, blue] = context.getImageData(0, 0, 1, 1).data;

  return `rgb(${red}, ${green}, ${blue})`;
}

/** 解析宿主某个 CSS 自定义属性（如 --muted）的实际颜色值。 */
function resolveCssColor(
  host: HTMLElement,
  variableName: string,
  fallback: string,
): string {
  const probe = host.ownerDocument.createElement("span");
  probe.style.color = `var(${variableName})`;
  probe.style.display = "none";
  host.appendChild(probe);
  const color = getComputedStyle(probe).color;
  probe.remove();

  return toLegacyColor(color || fallback, fallback, host.ownerDocument);
}

const FALLBACK_BACKGROUND = "rgb(255, 255, 255)";

/** 按宿主主题组装 mermaid 主题变量；无宿主时用亮色默认。 */
function getMermaidThemeVariables(host: HTMLElement | null) {
  if (!host) {
    return {
      primaryColor: "rgb(245, 245, 245)",
      primaryBorderColor: "rgb(59, 130, 246)",
      primaryTextColor: "rgb(17, 24, 39)",
      lineColor: "rgb(107, 114, 128)",
      fontFamily: "inherit",
    };
  }

  return {
    primaryColor: resolveCssColor(host, "--muted", "rgb(245, 245, 245)"),
    primaryBorderColor: resolveCssColor(host, "--primary", "rgb(59, 130, 246)"),
    primaryTextColor: resolveCssColor(host, "--foreground", "rgb(17, 24, 39)"),
    lineColor: resolveCssColor(host, "--muted-foreground", "rgb(107, 114, 128)"),
    fontFamily: "inherit",
  };
}

function getSandboxCssVariables(host: HTMLElement | null): string {
  const styles = host ? getComputedStyle(host) : null;
  return ["--muted", "--primary", "--foreground", "--muted-foreground"]
    .map((name) => `${name}: ${styles?.getPropertyValue(name).trim() || "initial"};`)
    .join(" ");
}

/**
 * 供导出的具体颜色。导出的文件没有宿主页面可继承，`var(--muted)` /
 * `font-family: inherit` 都会解析成空。
 */
function getExportStyle(host: HTMLElement | null): {
  background: string;
  fontFamily: string;
} {
  if (!host) {
    return { background: FALLBACK_BACKGROUND, fontFamily: "sans-serif" };
  }

  return {
    background: resolveCssColor(host, "--muted", FALLBACK_BACKGROUND),
    fontFamily: getComputedStyle(host).fontFamily || "sans-serif",
  };
}

/** 从渲染出的 SVG 解析出原始尺寸；viewBox 缺失或无效时返回 null。 */
function getMermaidLayout(svg: string): Size | null {
  const viewBoxMatch = svg.match(
    /viewBox=["']\s*([\d.-]+)\s+([\d.-]+)\s+([\d.-]+)\s+([\d.-]+)\s*["']/i,
  );
  const [, , , widthValue, heightValue] = viewBoxMatch ?? [];
  const width = widthValue ? Number.parseFloat(widthValue) : undefined;
  const height = heightValue ? Number.parseFloat(heightValue) : undefined;

  if (width && height && width > 0 && height > 0) {
    return {
      width: Math.ceil(width),
      height: Math.ceil(height),
    };
  }

  return null;
}

// Mermaid 首次加载 + 渲染期间使用的默认骨架高度。取值意图是容纳 issue
// 详情里的大多数图又不留过多空白；web.dev 的 CLS 指南建议提前预留此类空间，
// 免得异步内容到来时把周围布局推来推去。
export const MERMAID_SKELETON_HEIGHT_PX = 280;
const MERMAID_LAYOUT_CACHE_PREFIX = "multica:mermaid:layout:";

// DJB2——小巧、快速、够 sessionStorage 缓存 key 用。图文本本身当 key 太笨重
// （长度、特殊字符），而密码级哈希又得是异步的。
function hashChart(chart: string): string {
  let hash = 5381;
  for (let i = 0; i < chart.length; i++) {
    hash = ((hash << 5) + hash) ^ chart.charCodeAt(i);
  }
  return (hash >>> 0).toString(36);
}

/**
 * 尚未渲染的图要预留的高度：本会话已渲染过这同一张图时取真实高度，否则用
 * 骨架默认值。导出给近视口惰性外壳（rich-content/lazy-rich-block.tsx），让
 * 它预留与本组件相同的高度，而不是另行猜测尺寸。
 *
 * 渲染期间不可调用：它读 sessionStorage，服务端不存在，因此缓存温热时
 * 服务端帧与浏览器水合帧的值会不一致。调用方首帧必须用骨架默认值，并从
 * effect 里调用它（见 rich-content/rich-code-block.tsx 的
 * useReservedMermaidHeightPx）。
 */
export function reservedMermaidHeightPx(chart: string): number {
  return readCachedLayout(chart)?.height ?? MERMAID_SKELETON_HEIGHT_PX;
}

function readCachedLayout(chart: string): Size | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.sessionStorage.getItem(
      MERMAID_LAYOUT_CACHE_PREFIX + hashChart(chart),
    );
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    if (
      typeof parsed?.width === "number" &&
      typeof parsed?.height === "number" &&
      parsed.width > 0 &&
      parsed.height > 0
    ) {
      return { width: parsed.width, height: parsed.height };
    }
    return null;
  } catch {
    return null;
  }
}

function writeCachedLayout(chart: string, layout: Size | null): void {
  if (typeof window === "undefined") return;
  if (!layout) return;
  try {
    window.sessionStorage.setItem(
      MERMAID_LAYOUT_CACHE_PREFIX + hashChart(chart),
      JSON.stringify({ width: layout.width, height: layout.height }),
    );
  } catch {
    // 配额超限或存储被禁用——静默降级；仍能正确渲染，只是没有零位移
    // 优化。
  }
}

/** 组出一份「按宿主 CSS 变量配色的独立 HTML 文档」字符串。 */
function buildSandboxedMermaidDocument(svg: string, host: HTMLElement | null): string {
  const cssVariables = getSandboxCssVariables(host);

  return `<!doctype html><html><head><style>:root { ${cssVariables} } body { margin: 0; display: flex; justify-content: center; background: transparent; } svg { max-width: 100%; height: auto; }</style></head><body>${svg}</body></html>`;
}

/**
 * 查看器文档：图按原始尺寸绘制，宿主把缩放作为包裹层上的 CSS transform 施加。
 * 这里若像行内文档那样钳到 `max-width: 100%`，会与 transform 打架，把缩放
 * 上限卡在 iframe 恰好量到的宽度上。
 */
function buildViewerMermaidDocument(
  svg: string,
  host: HTMLElement | null,
  layout: Size,
): string {
  const cssVariables = getSandboxCssVariables(host);

  return `<!doctype html><html><head><style>:root { ${cssVariables} } html, body { margin: 0; width: 100%; height: 100%; overflow: hidden; background: transparent; } svg { display: block; width: ${layout.width}px; height: ${layout.height}px; max-width: none; }</style></head><body>${svg}</body></html>`;
}

/** 对比真正影响图表的主题值，弹窗滚动锁的 style 变化不触发图表重绘。 */
function readThemeSignature(): string {
  const root = document.documentElement;
  const styles = getComputedStyle(root);
  return [root.className, root.getAttribute("data-theme"), document.body?.className,
    document.body?.getAttribute("data-theme"), styles.colorScheme, styles.fontFamily,
    getSandboxCssVariables(root)].join("|");
}

function useThemeVersion() {
  const [themeVersion, setThemeVersion] = useState(0);

  useEffect(() => {
    let signature = readThemeSignature();
    const bumpThemeVersion = () => {
      const next = readThemeSignature();
      if (next === signature) return;
      signature = next;
      setThemeVersion((version) => version + 1);
    };
    const observer = new MutationObserver(bumpThemeVersion);
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["class", "style", "data-theme"],
    });
    if (document.body) {
      observer.observe(document.body, {
        attributes: true,
        attributeFilter: ["class", "style", "data-theme"],
      });
    }

    const mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
    mediaQuery.addEventListener("change", bumpThemeVersion);

    return () => {
      observer.disconnect();
      mediaQuery.removeEventListener("change", bumpThemeVersion);
    };
  }, []);

  return themeVersion;
}

/**
 * 跟踪滚动容器哪个水平边缘后面还有内容，让 CSS 淡出那些边缘，提示读者
 * 图在屏幕之外继续延伸。
 */
function useHorizontalOverflow(ref: React.RefObject<HTMLElement | null>, deps: unknown[]) {
  const [edges, setEdges] = useState<{ start: boolean; end: boolean }>({
    start: false,
    end: false,
  });

  useEffect(() => {
    const element = ref.current;
    if (!element) return;

    const measure = () => {
      const { scrollLeft, scrollWidth, clientWidth } = element;
      const maxScroll = scrollWidth - clientWidth;
      setEdges((previous) => {
        // 1px 容差：否则分数级布局宽度会在静止时留一片永久淡出。
        const start = scrollLeft > 1;
        const end = scrollLeft < maxScroll - 1;
        return previous.start === start && previous.end === end
          ? previous
          : { start, end };
      });
    };

    measure();
    element.addEventListener("scroll", measure, { passive: true });

    const observer =
      typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(element);

    return () => {
      element.removeEventListener("scroll", measure);
      observer?.disconnect();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ref, ...deps]);

  return edges;
}

// SVG 没有可用 viewBox 时查看器退回的尺寸。Mermaid 总会给出 viewBox，但
// 查看器的变换数学需要一个具体的 content 尺寸——什么都不渲染比一个近似
// 值更糟。
const VIEWER_FALLBACK_LAYOUT: Size = { width: 800, height: MERMAID_SKELETON_HEIGHT_PX };

/** 一次成功的渲染产物：SVG、两份文档、尺寸与导出配色。 */
interface RenderedDiagram {
  svg: string;
  inlineDocument: string;
  viewerDocument: string;
  /** 原始尺寸；viewBox 读不到时为 null。决定行内 iframe 的尺寸。 */
  layout: Size | null;
  /** 恒为具体值——查看器无法对着未知尺寸布局。 */
  viewerLayout: Size;
  exportBackground: string;
  exportFontFamily: string;
}

/**
 * MermaidDiagram——行内 Mermaid 图：渲染、错误态、复制源码、打开查看器。
 * 宽图可横向拖动；点按打开全屏查看器（拖拽不触发）。
 */
export function MermaidDiagram({ chart }: { chart: string }) {
  const { t } = useT("editor");
  const reactId = useId();
  const containerRef = useRef<HTMLDivElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const expandButtonRef = useRef<HTMLButtonElement>(null);
  const diagramId = useMemo(
    () => `mermaid-${reactId.replace(/[^a-zA-Z0-9_-]/g, "")}`,
    [reactId],
  );
  const themeVersion = useThemeVersion();
  // 用单个对象而非并行状态：半应用的重渲染（新 SVG、旧 layout）会让查看器
  // 的 iframe 对着错误的图定尺寸。
  const [rendered, setRendered] = useState<RenderedDiagram | null>(null);
  // 惰性初值：本会话已渲染过这同一张图时，缓存的 layout 让首帧就能预留
  // 正确空间——消除 0px → 真实高度的跳动，那会破坏深链滚动定位与阅读
  // 时的位置感。
  const [skeletonLayout, setSkeletonLayout] = useState<Size | null>(() =>
    readCachedLayout(chart),
  );
  const [error, setError] = useState<string | null>(null);
  const [viewerOpen, setViewerOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  const copiedResetTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function renderDiagram() {
      try {
        setError(null);
        // 刻意不清 `rendered`。主题切换会重跑本 effect，落回 null 会卸载
        // 已打开的查看器——连同用户的缩放与位置一起关掉。让旧图留在屏上
        // 直到新图就绪，也省去每次切换主题都闪一下加载骨架。
        setSkeletonLayout(readCachedLayout(chart));
        const mermaid = await getMermaid();
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: "strict",
          theme: "base",
          // 用 SVG <text> 渲染标签，而不是 Mermaid 默认的 HTML-in-
          // <foreignObject>。浏览器经 <img> 画 SVG 时不会栅格化
          // foreignObject——在 Chromium 里验证过，标签零像素绘制、还会污染
          // canvas，PNG 导出结果空空如也。SVG text 让导出自足。
          htmlLabels: false,
          themeVariables: getMermaidThemeVariables(containerRef.current),
          // 语法无效时让 render() 抛错，而不是把 Mermaid 内建错误图形画进
          // DOM。下面的 catch 于是展示我们自己的紧凑错误态——没有游离的
          // 错误 SVG，合法图也不会多跑一遍解析。
          suppressErrorRendering: true,
        });
        const { svg: renderedSvg } = await mermaid.render(diagramId, chart);
        if (cancelled) return;

        const measured = getMermaidLayout(renderedSvg);
        const viewerLayout = measured ?? VIEWER_FALLBACK_LAYOUT;
        const exportStyle = getExportStyle(containerRef.current);
        writeCachedLayout(chart, measured);
        setSkeletonLayout(measured);
        setRendered({
          svg: renderedSvg,
          inlineDocument: buildSandboxedMermaidDocument(renderedSvg, containerRef.current),
          // 与查看器布局用同一个尺寸，画出的 SVG 与 transform 对「图有多大」
          // 永不分歧。
          viewerDocument: buildViewerMermaidDocument(
            renderedSvg,
            containerRef.current,
            viewerLayout,
          ),
          layout: measured,
          viewerLayout,
          exportBackground: exportStyle.background,
          exportFontFamily: exportStyle.fontFamily,
        });
      } catch (err) {
        if (!cancelled) {
          setRendered(null);
          setError(err instanceof Error ? err.message : "Failed to render Mermaid diagram");
        }
      }
    }

    void renderDiagram();

    return () => {
      cancelled = true;
    };
  }, [chart, diagramId, themeVersion]);

  const overflow = useHorizontalOverflow(scrollRef, [rendered?.inlineDocument]);
  const openViewer = useCallback(() => setViewerOpen(true), []);
  const dragToScroll = useDragToScroll({ onTap: openViewer });

  const handleCopySource = useCallback(async () => {
    if (await copyText(chart)) {
      setCopied(true);
      if (copiedResetTimerRef.current) clearTimeout(copiedResetTimerRef.current);
      copiedResetTimerRef.current = setTimeout(() => {
        copiedResetTimerRef.current = null;
        setCopied(false);
      }, 2000);
    }
  }, [chart]);

  useEffect(
    () => () => {
      if (copiedResetTimerRef.current) clearTimeout(copiedResetTimerRef.current);
    },
    [],
  );

  if (error) {
    return (
      <div ref={containerRef} className="mermaid-diagram mermaid-diagram-error">
        <div className="mermaid-diagram-error-head">
          <p>{t(($) => $.mermaid.render_error)}</p>
          <button
            type="button"
            onClick={handleCopySource}
            title={t(($) => $.mermaid.copy_source)}
            aria-label={t(($) => $.mermaid.copy_source)}
          >
            {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
          </button>
        </div>
        {/* 解析器消息是判断哪行出错的唯一线索；没有它，兜底只是一段说不清
            原因的代码块。 */}
        <p className="mermaid-diagram-error-detail">{error}</p>
        <pre>
          <code>{chart}</code>
        </pre>
      </div>
    );
  }

  // iframe 尚未就绪时，让容器保持骨架高度（有缓存用缓存真实高度，否则用
  // 默认）。iframe 渲染后去掉 min-height——由 iframe 自身高度驱动布局。
  // 缓存若是对的，这次过渡就是零位移。
  const containerStyle: CSSProperties | undefined = rendered
    ? undefined
    : { minHeight: skeletonLayout?.height ?? MERMAID_SKELETON_HEIGHT_PX };

  return (
    <div
      ref={containerRef}
      className="mermaid-diagram"
      aria-label={t(($) => $.mermaid.diagram_label)}
      style={containerStyle}
      data-overflow-start={overflow.start ? "" : undefined}
      data-overflow-end={overflow.end ? "" : undefined}
    >
      {rendered ? (
        <>
          {/* 滚动容器是工具条的兄弟而不是父节点：作为滚动器的绝对定位子节点，
              旧版工具条在宽图上会随图一起滚出视野。 */}
          {/* 点按打开查看器，但拖拽不行——见 useDragToScroll。这里用手势驱动
              而非 `onClick`，因为拖拽结束时也会触发 click，会替只想看看宽图
              剩余部分的人重新打开查看器。 */}
          <div
            ref={scrollRef}
            className="mermaid-diagram-scroll"
            {...dragToScroll}
          >
            <iframe
              className="mermaid-diagram-frame"
              sandbox=""
              srcDoc={rendered.inlineDocument}
              style={{
                height: rendered.layout ? `${rendered.layout.height}px` : undefined,
                width: rendered.layout ? `${rendered.layout.width}px` : undefined,
              }}
              title={t(($) => $.mermaid.diagram_label)}
            />
          </div>
          <div className="mermaid-diagram-toolbar">
            <button
              type="button"
              onClick={handleCopySource}
              title={t(($) => $.mermaid.copy_source)}
              aria-label={t(($) => $.mermaid.copy_source)}
            >
              {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
            </button>
            <button
              ref={expandButtonRef}
              type="button"
              onClick={() => setViewerOpen(true)}
              title={t(($) => $.mermaid.open_viewer)}
              aria-label={t(($) => $.mermaid.open_viewer)}
            >
              <Maximize2 className="size-3.5" />
            </button>
          </div>
          <MermaidViewer
            open={viewerOpen}
            onOpenChange={setViewerOpen}
            chart={chart}
            svg={rendered.svg}
            viewerDocument={rendered.viewerDocument}
            layout={rendered.viewerLayout}
            exportBackground={rendered.exportBackground}
            exportFontFamily={rendered.exportFontFamily}
            finalFocusRef={expandButtonRef}
          />
        </>
      ) : (
        <div className="mermaid-diagram-loading">{t(($) => $.mermaid.rendering)}</div>
      )}
    </div>
  );
}
