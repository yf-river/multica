"use client";

import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import type { TextAnchor } from "./text-anchor";

/**
 * useLazyEditor 需要被包裹编辑器暴露的最小命令式接口。
 * ContentEditorRef 与 TitleEditorRef 结构上都满足它。
 */
export interface LazyEditorHandle {
  focus: () => void;
  focusAtCoords?: (coords: { x: number; y: number }) => void;
  focusAtAnchor?: (anchor: TextAnchor) => void;
  uploadFile?: (file: File) => void;
}

/**
 * 切换后光标应落的位置。`anchor`（逻辑的「第 N 块、第 M 个字符」，
 * 与布局无关）优先于原始点击坐标；坐标保留为「点不出文本锚点」时的兜底
 * （图片、表格外框、留白），也服务标题这类单行宿主。
 */
export interface LazyFocusTarget {
  x: number;
  y: number;
  anchor?: TextAnchor;
}

export interface UseLazyEditorOptions {
  /**
   * 立即挂载真实编辑器而不是静态占位。唯一合理的场景是未发送的草稿：
   * 它的存在证明编辑意图，把它水合成可见编辑器与 lazy 之前的旧行为完全
   * 一致。
   */
  initialActive?: boolean;
  /** 宿主自己的编辑器 ref——用于切换后的聚焦与排队上传。 */
  editorRef: RefObject<LazyEditorHandle | null>;
  /**
   * 该值变化时，hook 会在**同一次渲染中**（渲染期 setState，React 的派生
   * 状态模式）回到静态占位，而不是等一个 effect。effect 式重置慢一次
   * commit：承载新 key 的那次渲染仍会看到 `active/ready === true`，给**新**
   * 主体挂一个带 key 的编辑器、拼装完整 Tiptap 实例，然后才把它扔掉——
   * 白付了本 hook 想省掉的同款主线程成本，还会让该区域空一下。每个主体都
   * 用 `key` 重挂的宿主不需要这个。
   */
  resetKey?: unknown;
}

/**
 * 「只读优先」编辑器（issue 标题 / 评论 / 回复）的共享控制器。
 * Tiptap 实例化在主线程要花约 70-460ms/个，每个常驻编辑器都会让打开 issue
 * 更卡（实测：每次打开挂 2-4 个编辑器会冻结点击 0.4-1.3s）。模式：
 *
 *   1. `!active` 时渲染廉价静态占位。
 *   2. 点击（或排队上传、或占位上按 Enter）调 `activate`——挂载真实编辑器
 *      但**隐藏**；占位保持可见，Tiptap 组装期间内容绝不空白。
 *   3. 编辑器 `onReady` 翻转 `ready`；宿主在那一 commit 切换可见性，下面的
 *      effect（跑在那次 commit 之后，编辑器已布局）把光标落到触发点击的
 *      焦点目标上——优先文本锚点、原始坐标兜底——并冲刷排在被占位上的
 *      上传队列。
 *
 * 宿主渲染契约：
 *   {lazy.active && <div className={lazy.ready ? undefined : "hidden"}>
 *     <Editor ref={editorRef} onReady={lazy.onReady} ... /></div>}
 *   {!lazy.ready && <StaticStandIn onClick={e => lazy.activate({x: e.clientX, y: e.clientY})} />}
 *
 * 主体切换时不重挂的宿主（web issue 路由）必须传 `resetKey`；带 key 的
 * 宿主靠重挂免费获得重置。
 */
export function useLazyEditor({
  initialActive = false,
  editorRef,
  resetKey,
}: UseLazyEditorOptions) {
  const [active, setActive] = useState(initialActive);
  const [ready, setReady] = useState(false);
  const focusTargetRef = useRef<LazyFocusTarget | null>(null);
  const focusPendingRef = useRef(false);
  const pendingFilesRef = useRef<File[]>([]);

  // 主体变化时的渲染期重置——见 `resetKey` 选项说明。
  const [prevResetKey, setPrevResetKey] = useState(resetKey);
  if (resetKey !== prevResetKey) {
    setPrevResetKey(resetKey);
    setActive(initialActive);
    setReady(false);
    focusTargetRef.current = null;
    focusPendingRef.current = false;
    pendingFilesRef.current = [];
  }

  /** 把光标落到目标上：优先文本锚点，其次坐标，最后普通 focus。 */
  const focusAtTarget = useCallback(
    (target: LazyFocusTarget | null) => {
      const handle = editorRef.current;
      if (!handle) return;
      if (target?.anchor && handle.focusAtAnchor) handle.focusAtAnchor(target.anchor);
      else if (target && handle.focusAtCoords) handle.focusAtCoords(target);
      else handle.focus();
    },
    [editorRef],
  );

  const activate = useCallback(
    (target?: LazyFocusTarget) => {
      focusTargetRef.current = target ?? null;
      focusPendingRef.current = true;
      setActive(true);
      // 已经切换进来（如 blur 后键盘再次激活）：立刻聚焦，后面不会再
      // 来 ready effect。
      if (ready) {
        focusPendingRef.current = false;
        const latched = focusTargetRef.current;
        focusTargetRef.current = null;
        focusAtTarget(latched);
      }
    },
    [ready, focusAtTarget],
  );

  const onReady = useCallback(() => setReady(true), []);

  // 切换后的收尾——跑在「露出 ready 编辑器」的 commit 之后，因此焦点目标
  // 能对着已布局、已活着的文档解析，上传也能插进它。
  useEffect(() => {
    if (!ready) return;
    if (focusPendingRef.current) {
      focusPendingRef.current = false;
      const target = focusTargetRef.current;
      focusTargetRef.current = null;
      focusAtTarget(target);
    }
    const pending = pendingFilesRef.current;
    pendingFilesRef.current = [];
    for (const file of pending) editorRef.current?.uploadFile?.(file);
  }, [ready, editorRef, focusAtTarget]);

  /** 编辑器已活就立刻上传；否则排队并召唤编辑器。 */
  const uploadOrQueue = useCallback(
    (files: File[]) => {
      if (ready) {
        for (const file of files) editorRef.current?.uploadFile?.(file);
        return;
      }
      pendingFilesRef.current.push(...files);
      setActive(true);
    },
    [ready, editorRef],
  );

  return { active, ready, activate, onReady, uploadOrQueue };
}
