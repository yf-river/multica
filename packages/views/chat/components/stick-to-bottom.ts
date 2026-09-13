// 聊天列表“吸底”逻辑：在用户跟随最新消息时，让列表自动钉在底部，并判断是否已到达最新消息。
"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  createLiveEndFollow,
  FOLLOW_EDGE_THRESHOLD,
  LINE_SCROLL_PX,
  type LiveEndFollow,
} from "../../common/task-transcript/transcript-follow";

// 聊天列表的“吸底”逻辑。
//
// Virtuoso 的 `followOutput` 只在条目数量变化时触发。一条流式输出的助手回复是“一行”
// 在持续变长，而变大的输入框又会缩小列表的视口——两者都不改变数量，所以当读者
// 位于最新消息处时，列表必须自己重新钉住。
//
// 读者意图来自共享的 live-end 闩锁（transcript-follow.ts）：仅靠滚动位置和方向
// 无法区分“读者离开最新消息”和“浏览器自己移动视口”（输入框折叠后的 scrollTop 钳制、
// 滚动锚定），所以闩锁根据累积的输入增量来判断意图，并且只有在越过
// FOLLOW_EDGE_THRESHOLD 之后才释放——这与列表通过 `atBottomThreshold`
// 授予 Virtuoso 的宽容度相同。
//
// 输入是“暂存”的，只有列表自身的滚动才会把它“转正”：行里包含嵌套的滚动器
// （带 `overflow-auto` 上限的代码块），它们的 wheel/touch 事件会冒泡到这里
// 却不会移动列表，而短于视口的对话根本不会滚动。未消费的输入绝不能释放跟随。
// 同样的规则对按键反向成立：容器里任何位置的滚动键都会暂存意图，如果浏览器
// 通过滚动列表来响应它（焦点在某行的控件上会翻页滚动最近的滚动祖先），
// 该滚动就确认了它——读者永远不会被钉回自己按键之上的位置。

/** 描述一个滚动容器的滚动几何信息。 */
export interface ScrollMetrics {
  scrollTop: number;
  scrollHeight: number;
  clientHeight: number;
}

/**
 * 计算容器底部与当前滚动位置之间的距离（即距离底部的像素数）。
 * @param m - 滚动几何信息。
 * @returns 距离底部的像素数（不小于 0）。
 */
export function distanceFromBottom(m: ScrollMetrics): number {
  return Math.max(0, m.scrollHeight - m.scrollTop - m.clientHeight);
}

/**
 * 判断容器当前是否处于“最新消息”位置（即已滚动到底部附近）。
 * @param m - 滚动几何信息。
 * @returns 是否位于最新消息处。
 */
export function isAtLiveEnd(m: ScrollMetrics): boolean {
  return distanceFromBottom(m) <= FOLLOW_EDGE_THRESHOLD;
}

/**
 * 用于标记最新一行的属性，让列表能区分“读者正在看最新消息”和
 * “视口正好在预测该消息应在的位置”。见 `useStickToBottom` 的 `hasReachedLiveEnd`。
 */
export const LIVE_END_ROW_ATTR = "data-chat-live-end";

/**
 * 判断最新一行的盒子是否已经到达视口底部。
 *
 * 是“测量”出来的，而不是预测：滚动几何源自 `scrollHeight`，它是对未渲染行的估计，
 * 而在这个估计还不准确时，它会报告视口已经位于最新消息处。行的 rect 来自布局，
 * 所以只有它下面的行都被真正测量过之后，它才会对齐。
 * @param scrollEl - 滚动容器元素。
 * @returns 最新行是否已显示在视口底部。
 */
export function isShowingLiveEndRow(scrollEl: HTMLElement): boolean {
  const row = scrollEl.querySelector(`[${LIVE_END_ROW_ATTR}]`);
  if (!row) return false;
  const viewport = scrollEl.getBoundingClientRect();
  const rect = row.getBoundingClientRect();
  // 它的底部在屏幕上——位于折叠线处或之上（footer 的内边距让它略高于边缘），
  // 且没有被滚出顶部。
  return rect.bottom <= viewport.bottom + 1 && rect.bottom > viewport.top;
}

/**
 * 最新消息需要在折叠线处停留多久（且滚动范围不变），列表才会被显示出来。
 *
 * 仅仅到达最新消息一次是不够的：行在首次绘制后还会继续“安定”（图片解码、
 * 代码块高亮、字体替换），每一次都会让钉在底部的读者下面的内容移动。
 * 这就是列表等待它们停下来的时间窗——调大它能在较慢内容下更稳，
 * 代价是对话显示得更晚；调小它则更早绘制，让迟到的内容去移动视图。
 */
const LIVE_END_SETTLE_MS = 120;

// 内容永远停不下来的列表不能一直隐藏。超过这个时间就展示 Virtuoso 已有的内容：
// 跳一下总比一片空白的聊天强。
const REVEAL_DEADLINE_MS = 1000;

/** `useStickToBottom` 返回的控制接口。 */
export interface StickToBottom {
  /** 用于 `followOutput`：读者是否仍在跟随最新消息。 */
  isFollowing(): boolean;
  /** 接到 Virtuoso 的 `totalListHeightChanged`：内容大小变了。 */
  onContentHeightChanged(): void;
  /**
   * 在最新消息真正到达视口底部之前为 false。在此之前保持列表隐藏：
   * Virtuoso 打开列表时会滚动到它“预测”该消息所在的位置，在那里绘制，
   * 然后才测量并纠正——所以第一帧绘制的是对话的错误部分，下一帧会跳一下（MUL-6879）。
   */
  hasReachedLiveEnd: boolean;
}

/**
 * 让 `scrollEl` 在读者跟随最新消息期间保持钉在底部。视口尺寸变化（如输入框伸缩）
 * 在这里被观察到；内容尺寸变化（流式输出）必须通过 `onContentHeightChanged`
 * 上报——因为对容器本身做 ResizeObserver 永远看不到它的滚动范围。
 *
 * `pinToLiveEnd` 负责应用纠正，而且必须通过 Virtuoso 滚动
 * （`scrollToIndex` 到最后一行），绝不能在这里直接写 `scrollTop`。在虚拟化列表里，
 * `scrollHeight` 对未渲染的行用的是"估算值"，所以 `scrollHeight - clientHeight`
 * 并不是真正的底部。直接跳到那里会让 Virtuoso 掉出它的渲染窗口；
 * 它会去测量落点上的行，估算值随之移动，"底部"也跟着移动，下一次高度变化又钉一次——
 * 一个永远不收敛的纠正。更糟的是，每次这样的跳动都会以"没有输入能解释的位移"
 * 到达闩锁，于是读者自己的滚轮滚动被拒绝记账、被钉走：跟随永远不释放，
 * 列表根本无法滚动（MUL-6879）。基于索引滚动到最后一行正是 Virtuoso 自带的
 * `followOutput` 的做法，它会收敛，因为 Virtuoso 在决定停在哪之前先测量了那行。
 */
export function useStickToBottom(
  scrollEl: HTMLElement | null,
  pinToLiveEnd: () => void,
): StickToBottom {
  const followRef = useRef<LiveEndFollow | null>(null);
  if (followRef.current === null) {
    followRef.current = createLiveEndFollow();
    // 和 transcript 不同，聊天列表永远是"实时"的，所以在创建时就激活，
    // 而不是放到 effect 里：`followOutput` 在第一次渲染时就要读这个闩锁。
    followRef.current.setActive(true);
  }
  const follow = followRef.current;

  const pinRef = useRef(pinToLiveEnd);
  pinRef.current = pinToLiveEnd;
  const pin = useCallback(() => {
    pinRef.current();
  }, []);

  // 用轮询而不是 Virtuoso 的回调来驱动：`atBottomStateChange` 在行还不存在时
  // 就会一直读成 true，`itemsRendered` 上报的行可能马上又被一轮测量卸载掉，
  // 而且两者都不会在纠正性滚动落地的那个帧触发。列表一停稳就立刻停下的帧循环，
  // 在打开时只需付出几次 rect 读取的代价。
  const [hasReachedLiveEnd, setHasReachedLiveEnd] = useState(false);
  useEffect(() => {
    if (!scrollEl || hasReachedLiveEnd) return;
    let frame = 0;
    // 滚动范围是"某行尺寸变了"最便宜的代理信号：
    // 每个会移动视图的迟到内容也都会移动它。
    let lastHeight = -1;
    let steadySince: number | null = null;
    const poll = (now: number) => {
      const height = scrollEl.scrollHeight;
      const steady = height === lastHeight && isShowingLiveEndRow(scrollEl);
      lastHeight = height;
      if (!steady) steadySince = null;
      else if (steadySince === null) steadySince = now;
      else if (now - steadySince >= LIVE_END_SETTLE_MS) {
        setHasReachedLiveEnd(true);
        return;
      }
      frame = requestAnimationFrame(poll);
    };
    frame = requestAnimationFrame(poll);
    const deadline = setTimeout(() => setHasReachedLiveEnd(true), REVEAL_DEADLINE_MS);
    return () => {
      cancelAnimationFrame(frame);
      clearTimeout(deadline);
    };
  }, [scrollEl, hasReachedLiveEnd]);

  // 内容长高了或视口尺寸变了——这是没有滚动事件的位移，
  // 所以它永远不会把暂存的读者输入"转正"。
  const onResize = useCallback(() => {
    if (!scrollEl) return;
    if (follow.onResize(distanceFromBottom(scrollEl))) pin();
  }, [scrollEl, follow, pin]);

  useEffect(() => {
    if (!scrollEl) return;

    // transcript 对话框接线的镜像，只是实时端在 BOTTOM（底部）：
    // 离开它是向上滚，所以每个输入的方向符号都要翻转。
    let inputFrame: number | null = null;
    const stageInput = (delta: number) => {
      follow.input(delta);
      if (inputFrame !== null) cancelAnimationFrame(inputFrame);
      inputFrame = requestAnimationFrame(() => {
        inputFrame = null;
        follow.endInputFrame();
      });
    };
    const onWheel = (e: WheelEvent) => {
      const scale =
        e.deltaMode === 1 ? LINE_SCROLL_PX : e.deltaMode === 2 ? scrollEl.clientHeight : 1;
      stageInput(-e.deltaY * scale);
    };
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
      follow.touchStart();
    };
    const onTouchMove = (e: TouchEvent) => {
      const touch = trackedTouch(e.touches);
      if (!touch) return;
      // 手指向下滑动 = 内容向上滚（离开实时端）。
      if (lastTouchY !== null) stageInput(touch.clientY - lastTouchY);
      lastTouchY = touch.clientY;
    };
    const onTouchEnd = (e: TouchEvent) => {
      if (touchId === null || trackedTouch(e.touches)) return;
      touchId = null;
      lastTouchY = null;
      follow.touchEnd();
    };
    // 不需要目标守卫：这个容器不可聚焦，所以滚动键总是来自某个获得焦点的行内控件，
    // 浏览器会通过滚动最近的可滚动祖先——也就是这个列表——来回应它们。暂存机制在
    // 反方向同样安全：被控件消费掉的按键（Space 触发按钮、Home 在文本框里跳行）
    // 永远不会滚动列表，所以暂存的意图永远不会被转正。
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "ArrowUp") stageInput(LINE_SCROLL_PX);
      else if (e.key === "ArrowDown") stageInput(-LINE_SCROLL_PX);
      else if (e.key === "PageUp") stageInput(scrollEl.clientHeight);
      else if (e.key === "PageDown") stageInput(-scrollEl.clientHeight);
      // Shift+Space 向上翻页——远离实时端。
      else if (e.key === " ")
        stageInput(e.shiftKey ? scrollEl.clientHeight : -scrollEl.clientHeight);
      else if (e.key === "Home") stageInput(scrollEl.scrollHeight);
      else if (e.key === "End") stageInput(-scrollEl.scrollHeight);
    };
    const onPointerDown = (e: MouseEvent) => {
      follow.pointerDown(e.target === scrollEl);
    };
    const onPointerUp = () => {
      follow.pointerUp();
    };
    let atEdge = isAtLiveEnd(scrollEl);
    const onScroll = () => {
      const nowAtEdge = isAtLiveEnd(scrollEl);
      if (nowAtEdge !== atEdge) {
        atEdge = nowAtEdge;
        follow.onAtEdgeChange(nowAtEdge);
      }
      // 列表自己移动了：这正是把暂存输入"转正"的事件。
      if (follow.onScroll(distanceFromBottom(scrollEl))) pin();
    };

    // 输入框长高（或横幅出现）会压缩容器的盒子，但不会产生任何滚动事件；
    // 内容长高则单独通过 `onContentHeightChanged` 到达。
    const observer = new ResizeObserver(onResize);
    observer.observe(scrollEl);
    scrollEl.addEventListener("scroll", onScroll, { passive: true });
    scrollEl.addEventListener("wheel", onWheel, { passive: true });
    scrollEl.addEventListener("touchstart", onTouchStart, { passive: true });
    scrollEl.addEventListener("touchmove", onTouchMove, { passive: true });
    scrollEl.addEventListener("touchend", onTouchEnd, { passive: true });
    scrollEl.addEventListener("touchcancel", onTouchEnd, { passive: true });
    scrollEl.addEventListener("keydown", onKeyDown);
    scrollEl.addEventListener("mousedown", onPointerDown);
    window.addEventListener("mouseup", onPointerUp, { capture: true });

    return () => {
      if (inputFrame !== null) cancelAnimationFrame(inputFrame);
      follow.endInputFrame();
      observer.disconnect();
      scrollEl.removeEventListener("scroll", onScroll);
      scrollEl.removeEventListener("wheel", onWheel);
      scrollEl.removeEventListener("touchstart", onTouchStart);
      scrollEl.removeEventListener("touchmove", onTouchMove);
      scrollEl.removeEventListener("touchend", onTouchEnd);
      scrollEl.removeEventListener("touchcancel", onTouchEnd);
      scrollEl.removeEventListener("keydown", onKeyDown);
      scrollEl.removeEventListener("mousedown", onPointerDown);
      window.removeEventListener("mouseup", onPointerUp, { capture: true });
      // 滚动容器可能在拖拽中途脱离；一个卡死的"按住鼠标"标记
      // 会永远抑制钉底。
      follow.pointerUp();
      follow.touchEnd();
    };
  }, [scrollEl, follow, pin, onResize]);

  return useMemo(
    () => ({
      isFollowing: () => follow.isFollowing(),
      onContentHeightChanged: onResize,
      hasReachedLiveEnd,
    }),
    [follow, onResize, hasReachedLiveEnd],
  );
}
