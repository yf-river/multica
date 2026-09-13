"use client";

/**
 * MermaidViewer——全屏图查看器。
 *
 * 基于共享 `Dialog`（Base UI）而非定制的 portal，因此继承了旧 Mermaid
 * 灯箱缺失的行为：真正的背景遮罩、焦点陷阱、背景滚动锁定、焦点还原、
 * Esc 关闭。
 *
 * 图仍然待在 `sandbox=""` iframe 里——隔离边界不会为了可交互而放宽。改为
 * 给 iframe 设 `pointer-events: none`，由本文档把平移/缩放作为包裹层上的
 * transform 施加（见 `useDiagramCanvas`）。有个值得保留的副作用：因为
 * iframe 永不拿焦点，键盘事件不会困在对话框听不到的文档里——这正是过去
 * 点击后 Esc 失效的原因。
 */

import { useCallback, useMemo, useRef, useState } from "react";
import {
  Check,
  Code as CodeIcon,
  Copy,
  Download,
  Image as ImageIcon,
  X,
} from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { copyText } from "@multica/ui/lib/clipboard";
import { Dialog, DialogContent, DialogTitle } from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { useT } from "../i18n";
import { CodeBlockStatic } from "./code-block-static";
import { useZoomCanvas } from "./hooks/use-zoom-canvas";
import { ZoomCanvas, ZoomControls } from "./zoom-canvas";
import {
  buildExportSvg,
  diagramFilenameStem,
  downloadBlob,
  renderSvgToPngBlob,
} from "./utils/mermaid-export";
import type { Size } from "./utils/zoom-transform";

const COPY_FEEDBACK_MS = 2000;

interface MermaidViewerContentProps {
  /** Mermaid 源码，供源码视图与 `.mmd` 导出。 */
  chart: string;
  /** 渲染出的 SVG 标记，供导出。 */
  svg: string | null;
  /** 按原始尺寸画图的沙箱文档。 */
  viewerDocument: string | null;
  /** 图的原始尺寸。 */
  layout: Size;
  /** 从宿主主题解析出的颜色，导出内容与屏上所见一致。 */
  exportBackground: string;
  exportFontFamily: string;
}

export interface MermaidViewerProps extends MermaidViewerContentProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 关闭后焦点回到这里——打开查看器的那个按钮。 */
  finalFocusRef?: React.RefObject<HTMLElement | null>;
}

// 查看器专属 chrome（源码切换、复制、关闭）。缩放按钮来自共享的
// <ZoomControls>。
function ToolbarButton({
  onClick,
  disabled,
  label,
  children,
}: {
  onClick: () => void;
  disabled?: boolean;
  label: string;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={label}
      aria-label={label}
      className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
    >
      {children}
    </button>
  );
}

/**
 * MermaidViewer——全屏查看器：Dialog + 可平移缩放的画布 + 导出菜单。
 */
export function MermaidViewer({
  open,
  onOpenChange,
  finalFocusRef,
  ...content
}: MermaidViewerProps) {
  const { t } = useT("editor");
  const canvasRef = useRef<HTMLDivElement>(null);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        initialFocus={canvasRef}
        finalFocus={finalFocusRef}
        // 动态视口单位：移动端 Safari 等窄 webview 里 `vh` 会算进收缩的
        // 浏览器 chrome，把工具条推出屏——这正是本查看器想消灭的
        // 「找不到关闭按钮」问题。
        className="!max-w-[calc(100vw-2rem)] !h-[min(90dvh,calc(100dvh-2rem))] !w-[calc(100vw-2rem)] flex flex-col gap-0 overflow-hidden p-0 xl:!max-w-[80rem]"
        aria-label={t(($) => $.mermaid.viewer_title)}
      >
        {/* 正文状态（缩放、平移、源码切换）刻意活在 portal 边界之下，关闭即
            销毁。每次打开都是一次重新适配的全新读取，而不是恢复上次打开这张
            图时的过期缩放。 */}
        <MermaidViewerContent
          {...content}
          canvasRef={canvasRef}
          onClose={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  );
}

/** 查看器主体：画布/源码切换 + 缩放 + 导出。 */
function MermaidViewerContent({
  chart,
  svg,
  viewerDocument,
  layout,
  exportBackground,
  exportFontFamily,
  canvasRef,
  onClose,
}: MermaidViewerContentProps & {
  canvasRef: React.RefObject<HTMLDivElement | null>;
  onClose: () => void;
}) {
  const { t } = useT("editor");
  const [showSource, setShowSource] = useState(false);
  const [copied, setCopied] = useState(false);
  const canvas = useZoomCanvas({ content: layout });

  const handleCopySource = useCallback(async () => {
    if (await copyText(chart)) {
      setCopied(true);
      setTimeout(() => setCopied(false), COPY_FEEDBACK_MS);
    }
  }, [chart]);

  const standaloneSvg = useMemo(() => {
    if (!svg) return null;
    return buildExportSvg(svg, {
      background: exportBackground,
      fontFamily: exportFontFamily,
      width: layout.width,
      height: layout.height,
    });
  }, [svg, layout, exportBackground, exportFontFamily]);

  const filenameStem = useMemo(() => diagramFilenameStem(chart), [chart]);

  const handleDownloadSource = useCallback(() => {
    downloadBlob(
      new Blob([chart], { type: "text/vnd.mermaid;charset=utf-8" }),
      `${filenameStem}.mmd`,
    );
  }, [chart, filenameStem]);

  const handleDownloadSvg = useCallback(() => {
    if (!standaloneSvg) return;
    downloadBlob(
      new Blob([standaloneSvg], { type: "image/svg+xml;charset=utf-8" }),
      `${filenameStem}.svg`,
    );
  }, [standaloneSvg, filenameStem]);

  const handleDownloadPng = useCallback(async () => {
    if (!standaloneSvg) return;
    try {
      const blob = await renderSvgToPngBlob(standaloneSvg, layout);
      downloadBlob(blob, `${filenameStem}.png`);
    } catch {
      // 栅格化是尽力而为：`.svg` 与 `.mmd` 仍可用，这里响亮报错会在读者
      // 正看的图上叠一个错误弹窗。
    }
  }, [standaloneSvg, layout, filenameStem]);

  const canExportImage = standaloneSvg !== null;

  return (
    <>
      {/* 与滚动画布平级、绝不放进画布里：无论图平移/缩放到多远，工具条都
          必须原地不动。 */}
      <header className="flex shrink-0 items-center gap-1 border-b border-border bg-muted/30 px-3 py-2">
          <DialogTitle className="mr-1 truncate text-body font-medium">
            {t(($) => $.mermaid.viewer_title)}
          </DialogTitle>

          <ZoomControls canvas={canvas} disabled={showSource} />

          <div className="ml-auto flex items-center gap-0.5">
            <ToolbarButton
              onClick={() => setShowSource((value) => !value)}
              label={
                showSource
                  ? t(($) => $.mermaid.show_diagram)
                  : t(($) => $.mermaid.show_source)
              }
            >
              <CodeIcon className={cn("size-4", showSource && "text-foreground")} />
            </ToolbarButton>
            <ToolbarButton
              onClick={handleCopySource}
              label={t(($) => $.mermaid.copy_source)}
            >
              {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
            </ToolbarButton>
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <button
                    type="button"
                    title={t(($) => $.mermaid.export)}
                    aria-label={t(($) => $.mermaid.export)}
                    className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                  >
                    <Download className="size-4" />
                  </button>
                }
              />
              <DropdownMenuContent align="end">
                <DropdownMenuItem onClick={handleDownloadPng} disabled={!canExportImage}>
                  <ImageIcon className="size-4" />
                  {t(($) => $.mermaid.export_png)}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={handleDownloadSvg} disabled={!canExportImage}>
                  <ImageIcon className="size-4" />
                  {t(($) => $.mermaid.export_svg)}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={handleDownloadSource}>
                  <CodeIcon className="size-4" />
                  {t(($) => $.mermaid.export_source)}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
            <ToolbarButton
              onClick={onClose}
              label={t(($) => $.mermaid.close_viewer)}
            >
              <X className="size-4" />
            </ToolbarButton>
          </div>
        </header>

        {showSource ? (
          <div className="min-h-0 flex-1 overflow-auto bg-background p-3">
            <CodeBlockStatic language="mermaid" body={chart} />
          </div>
        ) : (
          <ZoomCanvas
            canvas={canvas}
            content={layout}
            label={t(($) => $.mermaid.canvas_label)}
            className="bg-muted"
            canvasRef={canvasRef}
          >
            {viewerDocument && (
              <iframe
                className="mermaid-viewer-frame"
                sandbox=""
                srcDoc={viewerDocument}
                title={t(($) => $.mermaid.viewer_title)}
                style={{ width: `${layout.width}px`, height: `${layout.height}px` }}
              />
            )}
          </ZoomCanvas>
        )}
    </>
  );
}
