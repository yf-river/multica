"use client";

/**
 * ZoomCanvas + ZoomControls——Mermaid 查看器与图片附件预览共享的
 * 平移/缩放视口。
 *
 * 拆成 hook（`useZoomCanvas`，全部行为）与两个表现组件，是因为工具条和画布
 * 住在不同的父节点里：每个界面有自己的头部，于是它调 hook、把
 * `<ZoomControls>` 放进自己的 chrome 位置、再把 `<ZoomCanvas>` 渲染为主体。
 * 内容以 children 传入——本模块只知道内容的原始尺寸，对它一无所知。
 */

import { useCallback, type ReactNode, type RefObject } from "react";
import { Frame, Maximize, Minus, Plus } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { createShortcutChord, type ShortcutChord } from "@multica/core/shortcuts";
import { ShortcutKeycaps } from "../common/shortcut-keycaps";
import { useT } from "../i18n";
import type { ZoomCanvasApi } from "./hooks/use-zoom-canvas";
import type { Size } from "./utils/zoom-transform";
import "./styles/zoom-canvas.css";

const ZOOM_IN_SHORTCUT = createShortcutChord("Plus");
const ZOOM_OUT_SHORTCUT = createShortcutChord("Minus");
const FIT_SHORTCUT = createShortcutChord("0");

/** 带快捷键 tooltip 的工具栏按钮。 */
function ToolbarButton({
  onClick,
  disabled,
  label,
  shortcut,
  children,
}: {
  onClick: () => void;
  disabled?: boolean;
  label: string;
  shortcut?: ShortcutChord;
  children: ReactNode;
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            onClick={onClick}
            disabled={disabled}
            aria-label={label}
            className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
          />
        }
      >
        {children}
      </TooltipTrigger>
      <TooltipContent side="top" sideOffset={8}>
        {label}
        {shortcut && <ShortcutKeycaps shortcut={shortcut} className="ml-1.5" />}
      </TooltipContent>
    </Tooltip>
  );
}

/**
 * ZoomControls——缩放工具条（缩小/百分比/放大/适配/100%）。
 */
export function ZoomControls({
  canvas,
  disabled,
  className,
}: {
  canvas: ZoomCanvasApi;
  /** 界面正显示非画布内容（如源码视图）时置位。 */
  disabled?: boolean;
  className?: string;
}) {
  const { t } = useT("editor");

  return (
    <TooltipProvider delay={300}>
      <div className={cn("flex items-center gap-0.5", className)}>
        <ToolbarButton
          onClick={canvas.zoomOut}
          disabled={!canvas.canZoomOut || disabled}
          label={t(($) => $.canvas.zoom_out)}
          shortcut={ZOOM_OUT_SHORTCUT}
        >
          <Minus className="size-4" />
        </ToolbarButton>
        {/* 固定宽度，数字变化时工具条不会抖动。 */}
        <span
          className="w-12 select-none text-center text-caption tabular-nums text-muted-foreground"
          aria-live="polite"
        >
          {canvas.zoomPercent}%
        </span>
        <ToolbarButton
          onClick={canvas.zoomIn}
          disabled={!canvas.canZoomIn || disabled}
          label={t(($) => $.canvas.zoom_in)}
          shortcut={ZOOM_IN_SHORTCUT}
        >
          <Plus className="size-4" />
        </ToolbarButton>
        <ToolbarButton
          onClick={canvas.fit}
          disabled={disabled}
          label={t(($) => $.canvas.zoom_fit)}
          shortcut={FIT_SHORTCUT}
        >
          <Frame className="size-4" />
        </ToolbarButton>
        <ToolbarButton
          onClick={canvas.zoomToActualSize}
          disabled={disabled}
          label={t(($) => $.canvas.zoom_actual)}
        >
          <Maximize className="size-4" />
        </ToolbarButton>
      </div>
    </TooltipProvider>
  );
}

/**
 * ZoomCanvas——可平移/缩放的视口本体，内容以 children 传入。
 */
export function ZoomCanvas({
  canvas,
  content,
  label,
  className,
  canvasRef,
  autoFocus,
  children,
}: {
  canvas: ZoomCanvasApi;
  /** children 的原始尺寸，未知时为 null。 */
  content: Size | null;
  /** 无障碍名——各界面描述自己的内容。 */
  label: string;
  className?: string;
  canvasRef?: RefObject<HTMLDivElement | null>;
  /**
   * 挂载即聚焦画布。没有焦点管理 Dialog 的界面需要它：键盘控制是画布作用域
   * 的，不聚焦的话 +/-/0 与方向键在用户先点一下之前毫无作用。
   */
  autoFocus?: boolean;
  children: ReactNode;
}) {
  // 稳定身份：行内 ref 回调每次渲染都是新函数，React 会在每次渲染上 detach
  //（setViewportNode(null)）又重挂。
  const setCanvasNode = useCallback(
    (node: HTMLDivElement | null) => {
      if (canvasRef) canvasRef.current = node;
      canvas.setViewportNode(node);
      if (node && autoFocus) node.focus({ preventScroll: true });
    },
    [canvasRef, canvas.setViewportNode, autoFocus],
  );

  return (
    <div
      ref={setCanvasNode}
      className={cn("zoom-canvas", canvas.isPanning && "is-panning", className)}
      // `application` 告诉屏幕阅读器把方向键透传给画布，而不是拿去做自己
      // 的导航。
      role="application"
      aria-label={label}
      tabIndex={0}
      onPointerDown={canvas.handlePointerDown}
      onPointerMove={canvas.handlePointerMove}
      onPointerUp={canvas.handlePointerUp}
      onPointerCancel={canvas.handlePointerUp}
      onDoubleClick={canvas.handleDoubleClick}
      onKeyDown={canvas.handleKeyDown}
    >
      <div
        className={cn(
          content ? "zoom-canvas-content" : "zoom-canvas-fit",
          content && canvas.isAnimated && "is-animated",
        )}
        style={
          content
            ? {
                width: `${content.width}px`,
                height: `${content.height}px`,
                transform: `translate(${canvas.transform.x}px, ${canvas.transform.y}px) scale(${canvas.transform.scale})`,
              }
            : undefined
        }
      >
        {children}
      </div>
    </div>
  );
}
