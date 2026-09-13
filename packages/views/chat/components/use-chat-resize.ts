// 悬浮聊天窗口的尺寸/展开逻辑：负责监听容器边界、计算渲染尺寸、
// 处理"最大化 ↔ 还原"切换与三个方向的拖拽调大小。
"use client";

import React, { useRef, useCallback, useState, useEffect } from "react";
import { CHAT_MIN_W, CHAT_MIN_H, useChatStore } from "@multica/core/chat";

type DragDir = "left" | "top" | "corner";

const MAX_RATIO = 0.9;
const FALLBACK_MAX_W = 800;
const FALLBACK_MAX_H = 700;

function clamp(v: number, min: number, max: number) {
  return Math.max(min, Math.min(max, v));
}

/**
 * 悬浮聊天窗口的尺寸管理 hook：窗口可被拖拽调宽高（左右边缘、上边缘、左上角），
 * 也可一键在"最大化"与"最小尺寸"间切换。最大尺寸始终按容器的 90% 计算，
 * 边界测量前先用一组兜底值。
 */
export function useChatResize(
  windowRef: React.RefObject<HTMLDivElement | null>,
) {
  const chatWidth = useChatStore((s) => s.chatWidth);
  const chatHeight = useChatStore((s) => s.chatHeight);
  const isExpanded = useChatStore((s) => s.isExpanded);
  const setChatSize = useChatStore((s) => s.setChatSize);
  const setExpanded = useChatStore((s) => s.setExpanded);

  // ── 通过 ResizeObserver 测量容器边界 ─────────────────────────────────
  const boundsRef = useRef({ maxW: FALLBACK_MAX_W, maxH: FALLBACK_MAX_H });
  const [boundsReady, setBoundsReady] = useState(false);
  const [isDragging, setIsDragging] = useState(false);
  const [, setRevision] = useState(0);

  useEffect(() => {
    const el = windowRef.current;
    const parent = el?.parentElement;
    if (!parent) return;

    const update = () => {
      const maxW = Math.floor(parent.clientWidth * MAX_RATIO);
      const maxH = Math.floor(parent.clientHeight * MAX_RATIO);
      setBoundsReady(true); // 一旦置 true 就幂等
      // 边界真的变了才触发重渲染。没有这道守卫，任何多余的 ResizeObserver 通知
      // （包括挂载期间的亚像素抖动）都会调度 setState，再反馈给 observer，
      // 造成 "Maximum update depth exceeded"。
      const prev = boundsRef.current;
      if (prev.maxW === maxW && prev.maxH === maxH) return;
      boundsRef.current = { maxW, maxH };
      setRevision((r) => r + 1);
    };

    // 立即测一次（此刻 parent 已在 DOM 里）
    update();

    const ro = new ResizeObserver(update);
    ro.observe(parent);
    return () => ro.disconnect();
  }, [windowRef]);

  // ── 推导实际渲染的尺寸 ────────────────────────────────────────────────
  const { maxW, maxH } = boundsRef.current;

  const renderWidth = isExpanded ? maxW : clamp(chatWidth, CHAT_MIN_W, maxW);
  const renderHeight = isExpanded ? maxH : clamp(chatHeight, CHAT_MIN_H, maxH);

  // ── 最大化 / 还原 ─────────────────────────────────────────────────────
  const isAtMax = renderWidth >= maxW && renderHeight >= maxH;

  const toggleExpand = useCallback(() => {
    if (isExpanded || isAtMax) {
      setChatSize(CHAT_MIN_W, CHAT_MIN_H);
    } else {
      setExpanded(true);
    }
  }, [isExpanded, isAtMax, setChatSize, setExpanded]);

  // ── 拖拽调大小 ────────────────────────────────────────────────────────
  const dragRef = useRef<{
    startX: number;
    startY: number;
    startW: number;
    startH: number;
    dir: DragDir;
  } | null>(null);

  const startDrag = useCallback(
    (e: React.PointerEvent, dir: DragDir) => {
      e.preventDefault();
      (e.target as HTMLElement).setPointerCapture(e.pointerId);

      dragRef.current = {
        startX: e.clientX,
        startY: e.clientY,
        startW: renderWidth,
        startH: renderHeight,
        dir,
      };
      setIsDragging(true);

      const onPointerMove = (ev: PointerEvent) => {
        const d = dragRef.current;
        if (!d) return;

        const { maxW: mw, maxH: mh } = boundsRef.current;

        const rawW =
          dir === "left" || dir === "corner"
            ? d.startW - (ev.clientX - d.startX)
            : d.startW;
        const rawH =
          dir === "top" || dir === "corner"
            ? d.startH - (ev.clientY - d.startY)
            : d.startH;

        setChatSize(clamp(rawW, CHAT_MIN_W, mw), clamp(rawH, CHAT_MIN_H, mh));
      };

      const onPointerUp = () => {
        dragRef.current = null;
        setIsDragging(false);
        document.removeEventListener("pointermove", onPointerMove);
        document.removeEventListener("pointerup", onPointerUp);
        document.body.style.cursor = "";
        document.body.style.userSelect = "";
      };

      document.addEventListener("pointermove", onPointerMove);
      document.addEventListener("pointerup", onPointerUp);

      const cursorMap: Record<DragDir, string> = {
        left: "col-resize",
        top: "row-resize",
        corner: "nw-resize",
      };
      document.body.style.cursor = cursorMap[dir];
      document.body.style.userSelect = "none";
    },
    [renderWidth, renderHeight, setChatSize],
  );

  return { renderWidth, renderHeight, isAtMax, boundsReady, isDragging, toggleExpand, startDrag };
}
