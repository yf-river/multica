// 聊天悬浮窗口的缩放抓手：三条不可见的热区（左边 / 上边 / 左上角），
// 分别支持拖拽调宽、调高、同时调宽高。
"use client";

import React from "react";

/** 拖拽方向：left=向左调宽，top=向上调高，corner=往左上同时调宽高。 */
type DragDir = "left" | "top" | "corner";

/** ChatResizeHandles 组件的 props。 */
interface ChatResizeHandlesProps {
  /** 按下某个抓手并开始拖动时的回调，携带拖拽方向。 */
  onDragStart: (e: React.PointerEvent, dir: DragDir) => void;
}

/**
 * 悬浮聊天窗口三个角的缩放抓手热区。每个热区把指针事件交给调用方的
 * `onDragStart`，由调用方执行真正的尺寸调整逻辑。
 */
export function ChatResizeHandles({ onDragStart }: ChatResizeHandlesProps) {
  return (
    <>
      {/* 左边热区——向左拖会加宽窗口 */}
      <div
        aria-hidden
        onPointerDown={(e) => onDragStart(e, "left")}
        className="absolute left-0 top-4 bottom-0 w-1 z-10 cursor-col-resize"
      />
      {/* 上边热区——向上拖会加高窗口 */}
      <div
        aria-hidden
        onPointerDown={(e) => onDragStart(e, "top")}
        className="absolute top-0 left-4 right-0 h-1 z-10 cursor-row-resize"
      />
      {/* 左上角热区——同时加宽加高 */}
      <div
        aria-hidden
        onPointerDown={(e) => onDragStart(e, "corner")}
        className="absolute top-0 left-0 size-4 z-20 cursor-nw-resize"
      />
    </>
  );
}
