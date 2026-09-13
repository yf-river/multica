"use client";

/**
 * 给无法自我缩放的内容提供「平移/缩放」控制器——空沙箱 iframe 里的
 * Mermaid 图，或附件预览里的图片。
 *
 * 所有手势都捕捉在宿主视口元素上，并以 CSS transform 作用在内容的包裹层。
 * 对 Mermaid 场景，这种反转让 Escape 键仍然可用：iframe 设了
 * `pointer-events: none` 所以永远拿不到焦点，键盘事件始终停留在宿主文档，
 * 由 Dialog 正常监听到。
 *
 * 变换数学都在 `../utils/zoom-transform`；本 Hook 只负责事件与状态。
 */

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
} from "react";
import {
  MAX_SCALE,
  PAN_STEP_PX,
  ZOOM_STEP,
  centerTransform,
  clampTransform,
  computeFitScale,
  computeFitTransform,
  computeMinScale,
  distanceBetween,
  midpointOf,
  panBy,
  wheelZoomFactor,
  zoomByAtCenter,
  zoomToAt,
  type Point,
  type Size,
  type ZoomTransform,
} from "../utils/zoom-transform";

interface UseZoomCanvasOptions {
  /** 内容未缩放时的原始尺寸；未知时为 null。 */
  content: Size | null;
  /**
   * 是否允许 ←/→ 键平移画布。当外层查看器自己绑定了这两个键时设为 false
   * （如图片序列的上一张/下一张，MUL-5752），避免一次按键两个处理器都触发。
   * 无论该值如何，↑/↓ 键始终平移，拖拽和滚轮也始终能横向平移。
   */
  horizontalArrowPan?: boolean;
}

/**
 * useZoomCanvas 暴露的整套画布控制 API。
 */
export interface ZoomCanvasApi {
  /**
   * 视口元素的回调 ref。刻意不用 RefObject：画布被 Dialog 以 portal 方式挂载，
   * 挂载发生在本 Hook 第一个 layout effect 之后，如果 effect 依赖 `[]` 就找不到
   * 元素、跳过测量、永远观察不到尺寸变化——画布会一直停留在未适配状态且毫无
   * 响应。
   */
  setViewportNode: (node: HTMLDivElement | null) => void;
  transform: ZoomTransform;
  /** 整数百分比，供工具栏读数显示。 */
  zoomPercent: number;
  canZoomIn: boolean;
  canZoomOut: boolean;
  isPanning: boolean;
  /**
   * 为 true 表示最近一次变化来自离散控件（按钮/键盘）而非直接操作，此时
   * transform 可以做缓动动画。拖拽与滚轮/捏合必须 1:1 跟随输入，永远不带动画。
   */
  isAnimated: boolean;
  zoomIn: () => void;
  zoomOut: () => void;
  zoomToActualSize: () => void;
  fit: () => void;
  handlePointerDown: (event: ReactPointerEvent<HTMLElement>) => void;
  handlePointerMove: (event: ReactPointerEvent<HTMLElement>) => void;
  handlePointerUp: (event: ReactPointerEvent<HTMLElement>) => void;
  handleKeyDown: (event: ReactKeyboardEvent<HTMLElement>) => void;
  /** 双击/双指双击在「默认适配」与「100%」之间切换。 */
  handleDoubleClick: (event: ReactMouseEvent<HTMLElement>) => void;
}

const IDENTITY: ZoomTransform = { scale: 1, x: 0, y: 0 };
const EMPTY_SIZE: Size = { width: 0, height: 0 };

function nearlyEqual(a: number, b: number): boolean {
  return Math.abs(a - b) < 0.5;
}

/**
 * 画布平移/缩放控制 Hook：负责收集手势输入并驱动 transform 状态。
 *
 * @param options 见 UseZoomCanvasOptions。
 * @returns 见 ZoomCanvasApi——把事件处理器绑定到视口元素上即可工作。
 */
export function useZoomCanvas({
  content,
  horizontalArrowPan = true,
}: UseZoomCanvasOptions): ZoomCanvasApi {
  const [viewportNode, setViewportNode] = useState<HTMLDivElement | null>(null);
  const [viewport, setViewport] = useState<Size>(EMPTY_SIZE);
  const [transform, setTransform] = useState<ZoomTransform>(IDENTITY);
  const [isPanning, setIsPanning] = useState(false);
  const [isAnimated, setIsAnimated] = useState(false);

  // 适配被推迟到两个尺寸都已知时：在此之前算出的任何 transform 都是对着
  // 0x0 视口算的，必然要扔掉重来。
  const hasFittedRef = useRef(false);
  const activePointersRef = useRef(new Map<number, Point>());
  const panOriginRef = useRef<{ pointer: Point; transform: ZoomTransform } | null>(null);
  const pinchOriginRef = useRef<{ distance: number; scale: number } | null>(null);

  const contentSize = content ?? EMPTY_SIZE;
  const hasContent = contentSize.width > 0 && contentSize.height > 0;
  const hasViewport = viewport.width > 0 && viewport.height > 0;
  const ready = hasContent && hasViewport;

  // 最新值 ref：下面的原生滚轮监听只注册一次（`{ passive: false }`），
  // 若不借助 ref 会闭包住过期的 state。
  const stateRef = useRef({ transform, contentSize, viewport, ready });
  stateRef.current = { transform, contentSize, viewport, ready };

  useLayoutEffect(() => {
    const element = viewportNode;
    if (!element) return;

    // 用 offsetWidth/offsetHeight 而不用 getBoundingClientRect：两个查看器都
    // 打开在会缩小进入的容器里（共享 Dialog 的 `zoom-in-95`、附件弹窗动画的
    // scale(0.95)）。client rect 是带 transform 缩放的，若在动画中途测量，
    // 就会拿小了百分之几的视口去适配内容——而且 ResizeObserver 上报的是未
    // 变换的布局盒，动画结束时它不会触发，这个错位的适配就会一直留在屏幕上。
    const measure = () => {
      const width = element.offsetWidth;
      const height = element.offsetHeight;
      setViewport((previous) =>
        nearlyEqual(previous.width, width) && nearlyEqual(previous.height, height)
          ? previous
          : { width, height },
      );
    };

    measure();

    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);

    return () => observer.disconnect();
  }, [viewportNode]);

  // 首次打开时只适配一次——用 layout effect 保证第一帧画面就已适配好。
  // 刻意不在后续视口变化时重新适配：主题切换会让内容以同样尺寸重渲染，
  // 若默默把用户的缩放拉回适配会丢失他们的阅读位置。
  useLayoutEffect(() => {
    if (!ready || hasFittedRef.current) return;
    hasFittedRef.current = true;
    setTransform(computeFitTransform(contentSize, viewport));
  }, [ready, contentSize, viewport]);

  // 内容确实变了（换了原始尺寸）就从全新状态开始。同样是 layout effect，且
  // 从不做缓动：切到另一张图（序列导航）属于内容替换，不是同一内容上的镜头
  // 移动。新帧必须一画出来就是适配好的——普通 effect 会先用上一张图的
  // 平移/缩放把新图画一帧，而残留的 `isAnimated` 还可能把纠正过程做成动画。
  const contentKey = `${contentSize.width}x${contentSize.height}`;
  const previousContentKeyRef = useRef(contentKey);
  useLayoutEffect(() => {
    if (previousContentKeyRef.current === contentKey) return;
    previousContentKeyRef.current = contentKey;
    if (!ready) return;
    setIsAnimated(false);
    setTransform(computeFitTransform(contentSize, viewport));
  }, [contentKey, ready, contentSize, viewport]);

  // 窗口/面板在内容下方被改变大小时，把内容钳回视野内。
  useEffect(() => {
    if (!ready) return;
    setTransform((current) => clampTransform(current, contentSize, viewport));
  }, [ready, contentSize, viewport]);

  const fit = useCallback(() => {
    const { contentSize: c, viewport: v, ready: r } = stateRef.current;
    if (!r) return;
    setIsAnimated(true);
    setTransform(computeFitTransform(c, v));
  }, []);

  const zoomToActualSize = useCallback(() => {
    const { contentSize: c, viewport: v, ready: r } = stateRef.current;
    if (!r) return;
    setIsAnimated(true);
    setTransform(clampTransform(centerTransform(c, v, 1), c, v));
  }, []);

  const zoomBy = useCallback((factor: number) => {
    const { transform: t, contentSize: c, viewport: v, ready: r } = stateRef.current;
    if (!r) return;
    setIsAnimated(true);
    setTransform(zoomByAtCenter(t, factor, c, v));
  }, []);

  const zoomIn = useCallback(() => zoomBy(ZOOM_STEP), [zoomBy]);
  const zoomOut = useCallback(() => zoomBy(1 / ZOOM_STEP), [zoomBy]);

  // 滚轮必须是带 `{ passive: false }` 的原生监听：React 的 onWheel 经由一个
  // passive 根监听器分发，那里的 preventDefault 会被忽略，页面会在对话框
  // 后面滚动，而不是图在缩放。
  useEffect(() => {
    const element = viewportNode;
    if (!element) return;

    const onWheel = (event: WheelEvent) => {
      const { transform: t, contentSize: c, viewport: v, ready: r } = stateRef.current;
      if (!r) return;
      event.preventDefault();
      setIsAnimated(false);

      const rect = element.getBoundingClientRect();
      const anchor: Point = {
        x: event.clientX - rect.left,
        y: event.clientY - rect.top,
      };
      const factor = wheelZoomFactor(event.deltaY, event.deltaMode);
      setTransform(zoomToAt(t, t.scale * factor, anchor, c, v));
    };

    element.addEventListener("wheel", onWheel, { passive: false });
    return () => element.removeEventListener("wheel", onWheel);
  }, [viewportNode]);

  const localPoint = useCallback(
    (event: { clientX: number; clientY: number }): Point => {
      const rect = viewportNode?.getBoundingClientRect();
      return {
        x: event.clientX - (rect?.left ?? 0),
        y: event.clientY - (rect?.top ?? 0),
      };
    },
    [viewportNode],
  );

  const handlePointerDown = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      if (!stateRef.current.ready) return;
      // 忽略鼠标非主键，避免右键误触发平移。
      if (event.pointerType === "mouse" && event.button !== 0) return;

      setIsAnimated(false);
      const point = localPoint(event);
      activePointersRef.current.set(event.pointerId, point);
      event.currentTarget.setPointerCapture?.(event.pointerId);

      const pointers = [...activePointersRef.current.values()];
      if (pointers.length === 1) {
        panOriginRef.current = { pointer: point, transform: stateRef.current.transform };
        pinchOriginRef.current = null;
        setIsPanning(true);
        return;
      }
      if (pointers.length === 2) {
        // 第二根手指落下：从平移切换为双指捏合。
        panOriginRef.current = null;
        pinchOriginRef.current = {
          distance: distanceBetween(pointers[0]!, pointers[1]!),
          scale: stateRef.current.transform.scale,
        };
        setIsPanning(false);
      }
    },
    [localPoint],
  );

  const handlePointerMove = useCallback(
    (event: ReactPointerEvent<HTMLElement>) => {
      if (!activePointersRef.current.has(event.pointerId)) return;
      const { contentSize: c, viewport: v, ready: r } = stateRef.current;
      if (!r) return;

      const point = localPoint(event);
      activePointersRef.current.set(event.pointerId, point);
      const pointers = [...activePointersRef.current.values()];

      if (pointers.length >= 2) {
        const pinchOrigin = pinchOriginRef.current;
        if (!pinchOrigin || pinchOrigin.distance <= 0) return;
        const [first, second] = pointers as [Point, Point];
        const nextScale =
          pinchOrigin.scale * (distanceBetween(first, second) / pinchOrigin.distance);
        setTransform((current) =>
          zoomToAt(current, nextScale, midpointOf(first, second), c, v),
        );
        return;
      }

      const panOrigin = panOriginRef.current;
      if (!panOrigin) return;
      setTransform(
        clampTransform(
          {
            scale: panOrigin.transform.scale,
            x: panOrigin.transform.x + (point.x - panOrigin.pointer.x),
            y: panOrigin.transform.y + (point.y - panOrigin.pointer.y),
          },
          c,
          v,
        ),
      );
    },
    [localPoint],
  );

  const handlePointerUp = useCallback((event: ReactPointerEvent<HTMLElement>) => {
    activePointersRef.current.delete(event.pointerId);
    event.currentTarget.releasePointerCapture?.(event.pointerId);

    const pointers = [...activePointersRef.current.values()];
    if (pointers.length === 1) {
      // 从捏合中抬起一根手指：用剩下的那根接着平移，而不是冻结到用户
      // 抬起再重新按下。
      panOriginRef.current = {
        pointer: pointers[0]!,
        transform: stateRef.current.transform,
      };
      pinchOriginRef.current = null;
      setIsPanning(true);
      return;
    }
    if (pointers.length === 0) {
      panOriginRef.current = null;
      pinchOriginRef.current = null;
      setIsPanning(false);
    }
  }, []);

  const handleKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLElement>) => {
      const { contentSize: c, viewport: v, ready: r } = stateRef.current;
      if (!r) return;

      // Escape 归 Dialog 管，这里绝不能吞掉。
      switch (event.key) {
        case "+":
        case "=":
          event.preventDefault();
          zoomIn();
          return;
        case "-":
        case "_":
          event.preventDefault();
          zoomOut();
          return;
        case "0":
          event.preventDefault();
          fit();
          return;
        case "ArrowLeft":
        case "ArrowRight":
          // 外层查看器拥有这些键（如图片序列的上一张/下一张）时不处理。
          // 不调 preventDefault，让事件继续冒泡到它的处理器。
          if (!horizontalArrowPan) return;
        // 有意不加 break：←/→ 在有水平平移权限时与 ↑/↓ 走同一套平移逻辑。
        case "ArrowUp":
        case "ArrowDown": {
          event.preventDefault();
          setIsAnimated(true);
          const step = event.shiftKey ? PAN_STEP_PX * 3 : PAN_STEP_PX;
          const deltaX =
            event.key === "ArrowLeft" ? step : event.key === "ArrowRight" ? -step : 0;
          const deltaY =
            event.key === "ArrowUp" ? step : event.key === "ArrowDown" ? -step : 0;
          setTransform((current) => panBy(current, deltaX, deltaY, c, v));
          return;
        }
        default:
      }
    },
    [zoomIn, zoomOut, fit, horizontalArrowPan],
  );

  // 双击在「适配」与「100%」之间切换，放大过程锚定在光标下方。这是图片
  // 查看器的通用手势（macOS 预览、各类浏览器）：一个手势就能在「看全貌」
  // 和「看细节」之间来回。
  const handleDoubleClick = useCallback(
    (event: ReactMouseEvent<HTMLElement>) => {
      const { transform: t, contentSize: c, viewport: v, ready: r } = stateRef.current;
      if (!r) return;

      setIsAnimated(true);
      const fitScale = computeFitScale(c, v);
      if (Math.abs(t.scale - fitScale) >= 0.001) {
        setTransform(computeFitTransform(c, v));
        return;
      }
      // 当前已是适配态。跳到 100%——如果适配本身就是 100%（小图），就跳到
      // 200%，让图仍然有放大空间而不是忽略这次手势。
      setTransform(zoomToAt(t, fitScale < 0.999 ? 1 : 2, localPoint(event), c, v));
    },
    [localPoint],
  );

  return {
    setViewportNode,
    transform,
    zoomPercent: Math.round(transform.scale * 100),
    canZoomIn: transform.scale < MAX_SCALE - 0.001,
    // 下限看动态最小值而非 MIN_SCALE：对只能在 25% 以下适配的内容，缩小
    // 按钮必须一直可用到适配缩放为止。
    canZoomOut: transform.scale > computeMinScale(contentSize, viewport) + 0.001,
    isPanning,
    isAnimated,
    zoomIn,
    zoomOut,
    zoomToActualSize,
    fit,
    handlePointerDown,
    handlePointerMove,
    handlePointerUp,
    handleKeyDown,
    handleDoubleClick,
  };
}

export type { Size, ZoomTransform };
export { MAX_SCALE };
