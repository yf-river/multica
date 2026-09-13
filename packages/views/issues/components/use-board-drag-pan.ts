// 本文件：useBoardDragPan —— 看板空白区的"按住左键拖拽平移"手势（Trello/Linear 模式）。
// 在横向可滚动的看板上：按住空白区域拖拽即左右平移看板，页面跟随光标。
// 拖卡片仍走 dnd-kit；本 hook 在手势起始于卡片或任何交互元素时根本不会激活，
// 因此两者互不干扰。只处理鼠标左键，触屏/手写笔留给浏览器原生滚动与缩放。
// 返回一组要展开到滚动容器上的 props。

import { useCallback, useEffect, useRef } from "react";

/** 区分"点击"与"拖拽平移"的阈值（px）。与看板 dnd-kit PointerSensor 的
 * `activationConstraint: { distance: 5 }` 对齐，让空白区按压与卡片按压在
 * 被识别为拖拽之前的体感完全一致。 */
const PAN_ACTIVATION_DISTANCE = 5;

// 按压起点在这些元素内的手势绝不能触发看板平移——它们有自己的指针语义
//（dnd-kit 卡片、链接、表单控件、菜单）。在这些元素内部开始的手势原样放行。
// 故意放宽范围（P2-4）：除了明显的控件，还覆盖 label/checkbox/radio、
// ARIA link/menu/option 角色，以及用 `data-no-board-pan` 主动退出者。
const INTERACTIVE_SELECTOR = [
  "[data-board-card]",
  "[data-no-board-pan]",
  "a",
  "button",
  "input",
  "textarea",
  "select",
  "option",
  "label",
  "summary",
  "[role='button']",
  "[role='link']",
  "[role='menuitem']",
  "[role='option']",
  "[role='checkbox']",
  "[role='radio']",
  "[role='tab']",
  "[role='switch']",
  "[contenteditable='true']",
].join(", ");

/**
 * 横向滚动看板的"空白区左键拖拽平移"（#6700，Trello/Linear 模式）：
 * 在空白看板背景上按住鼠标左键并拖动，看板就横向平移——页面跟随光标。
 * 拖卡片依旧移动卡片（dnd-kit）；本 hook 不掺和，因为它从不把手势起始于
 * 卡片或任何交互元素的情况激活。触屏与手写笔刻意留给浏览器；此 PR 只接管
 * 鼠标左键这个手势。
 *
 * ### 与 dnd-kit 共存的恒常约束（P2-5 —— 不可破坏）
 * 这些指针处理器挂在同一个承载 `DndContext` 的滚动容器上。它们与卡片拖拽
 * 纯粹靠"起点"互斥：`onPointerDown` 只要发现按压起点落在 `INTERACTIVE_SELECTOR`
 * 内就退出；而每个可拖拽卡片的根节点都带 `data-board-card`
 *（见 `board-card.tsx` 的 `DraggableBoardCard`）。若未来某次改动移除了卡片根上
 * 的这个属性、或把平移容器嵌到卡片监听器之下，卡片拖拽与看板平移就会互相打架。
 * 请始终让 `data-board-card` 留在卡片根上、并让这里的排除规则与之同步。
 *
 * ### 设计说明
 *   - 使用 Pointer Events，并在 `pointerdown`（而非延迟到首次移动）时就抢占
 *     pointer capture，让后续每个 `pointermove` 都持续派发到容器——即使指针
 *     已离开容器甚至离开窗口。"延迟捕获"曾是"指针一移出看板就滚回去"的 bug
 *     来源：不提前捕获，浏览器会跑一次原生选区拖拽，其自动滚动与 `scrollLeft`
 *     打架；而指针重新进入容器时又会产生一次跳变的位移。
 *   - 从 `pointerdown` 起就抑制文本选区（P1-3）：容器上 `user-select: none`
 *     外加 `selectstart` / `dragstart` 阻止器，随后在 `reset` 里恢复。在 down
 *     时（而非越过 5px 阈值后）就做，堵住了 Safari/Firefox 开始一次选区、
 *     其自动滚动把看板拽回去的窗口期。因为选区已经无法开始，`pointermove`
 *     也就不再需要逐帧 `removeAllRanges`（P3-7）。
 *   - 仅鼠标左键（`event.pointerType === "mouse"` 且 `event.button === 0`）。
 *     触屏/手写笔不动，让原生触摸滚动与双指缩放继续归浏览器。
 *   - 激活以约 5px 的移动为前提，普通点击不会被吞掉。
 *   - 平移用被捕获指针的 `clientX` 增量——绝不用 hover 目标或
 *     `elementFromPoint`——离开容器也不会跟丢。
 *   - 只动横轴：`deltaY` 从不读取，`scrollTop` 从不写入。`scrollLeft` 被夹在
 *     `[0, scrollWidth - clientWidth]` 之间，顶到边缘就干脆停住而不是回弹。
 *   - 在 `pointerup` / `pointercancel` / `lostpointercapture` 与窗口 `blur`
 *     上清理，并在 move 里检查 `buttons`，让"丢失的一次释放"不会把看板卡在
 *     平移状态。
 *
 * 返回要展开到滚动容器上的一组 props。
 */
export function useBoardDragPan<T extends HTMLElement>() {
  const ref = useRef<T | null>(null);
  // 当前活动/待定手势的 pointer id；空闲时为 null。
  const pointerIdRef = useRef<number | null>(null);
  // 越过约 5px 阈值、真正进入平移后才置 true。
  const activeRef = useRef(false);
  const startXRef = useRef(0);
  const lastXRef = useRef(0);

  const beginSelectionSuppression = useCallback((el: T) => {
    el.style.userSelect = "none";
    el.style.setProperty("-webkit-user-select", "none");
  }, []);

  const reset = useCallback(() => {
    const el = ref.current;
    if (el && pointerIdRef.current !== null) {
      // releasePointerCapture 在捕获已丢失时会抛错——忽略它。
      try {
        el.releasePointerCapture(pointerIdRef.current);
      } catch {
        /* 捕获已经释放 */
      }
    }
    pointerIdRef.current = null;
    activeRef.current = false;
    if (el) {
      el.style.removeProperty("cursor");
      // 手势结束后恢复文本选择。
      el.style.removeProperty("user-select");
      el.style.removeProperty("-webkit-user-select");
    }
  }, []);

  const onPointerDown = useCallback((event: React.PointerEvent<T>) => {
    // 仅鼠标左键。主触屏/手写笔触点同样上报 button === 0，但此看板手势
    // 刻意限定在鼠标输入。
    if (event.pointerType !== "mouse" || event.button !== 0) return;
    const el = ref.current;
    if (!el) return;
    // 忽略从卡片或交互控件上开始的手势——那些属于 dnd-kit / 链接 / 表单
    //（见上面的共存恒常约束）。
    const target = event.target as Element | null;
    if (target && target.closest(INTERACTIVE_SELECTOR)) return;

    pointerIdRef.current = event.pointerId;
    activeRef.current = false;
    startXRef.current = event.clientX;
    lastXRef.current = event.clientX;

    // 从第一个事件起就抑制文本选区（P1-3）——在 5px 阈值之前，
    // 让任何选区都无法开始、无从自动滚动容器。
    beginSelectionSuppression(el);

    // 立即抢占指针，让后续移动即使光标离开看板也继续派发到本元素。
    try {
      el.setPointerCapture(event.pointerId);
    } catch {
      // 指针捕获不受支持/被拒绝（老引擎、合成事件）。
      // 手势仍可用：按住期间指针事件会持续流向容器，
      // 窗口 `blur` 处理器外加下面每次 move 的 `buttons` 检查兜底清理，
      // 即使漏了一次释放也能收尾。
    }
    event.preventDefault();
  }, [beginSelectionSuppression]);

  const onPointerMove = useCallback((event: React.PointerEvent<T>) => {
    if (pointerIdRef.current === null || event.pointerId !== pointerIdRef.current) return;
    const el = ref.current;
    if (!el) return;

    // 主键在我们没收到消息的地方被松开了（窗口 blur、拖出文档）。
    // buttons 的第 0 位就是左键。
    if ((event.buttons & 1) === 0) {
      reset();
      return;
    }

    if (!activeRef.current) {
      if (Math.abs(event.clientX - startXRef.current) < PAN_ACTIVATION_DISTANCE) return;
      // 越过阈值：开始平移。指针在 pointerdown 时已被捕获、选区也已抑制，
      // 这里只需要给出可抓取的视觉反馈。
      activeRef.current = true;
      el.style.cursor = "grabbing";
    }

    // 只动横轴，以被捕获指针的 clientX 增量为准。夹在可滚动范围内，
    // 顶到边缘就停住。（选区在 pointerdown 已被抑制，因此无需逐帧清选区。）
    const delta = event.clientX - lastXRef.current;
    lastXRef.current = event.clientX;
    const maxScroll = el.scrollWidth - el.clientWidth;
    const next = Math.min(Math.max(el.scrollLeft - delta, 0), Math.max(maxScroll, 0));
    el.scrollLeft = next;
    event.preventDefault();
  }, [reset]);

  const onPointerUp = useCallback((event: React.PointerEvent<T>) => {
    if (event.pointerId !== pointerIdRef.current) return;
    reset();
  }, [reset]);

  // 手势待定/活动期间的"双保险"选区/原生拖拽拦截器（P1-3）：`user-select`
  // 覆盖大多数引擎，但 Safari 与 Firefox 仍可能开始一次选区或图片/文本拖拽，
  // 因此直到手势结束都直接否决这些事件。以原生监听器挂载，
  // 因为 React 没有 `selectstart` 的合成事件。
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const veto = (event: Event) => {
      if (pointerIdRef.current !== null) event.preventDefault();
    };
    el.addEventListener("selectstart", veto);
    el.addEventListener("dragstart", veto);
    return () => {
      el.removeEventListener("selectstart", veto);
      el.removeEventListener("dragstart", veto);
    };
  }, []);

  useEffect(() => {
    const handleBlur = () => reset();
    window.addEventListener("blur", handleBlur);
    return () => window.removeEventListener("blur", handleBlur);
  }, [reset]);

  return {
    ref,
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerCancel: onPointerUp,
    onLostPointerCapture: onPointerUp,
  };
}
