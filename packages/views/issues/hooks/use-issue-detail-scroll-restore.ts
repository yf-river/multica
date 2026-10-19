// 问题详情滚动位置恢复 hook：离开时记住滚动位置，返回时恢复到原处（含虚拟化时间线初始化后的重试）。
import { useLayoutEffect, useRef } from "react";

type UseIssueDetailScrollRestoreArgs = {
  restoreKey: string;
  scrollContainerEl: HTMLElement | null;
  ready: boolean;
  disabled?: boolean;
  /**
   * 来自平台标签备忘（tab memento）的权威恢复目标，当平台正在提供它时使用（MUL-4741）。
   * 它优先于本 hook 的模块级 Map：备忘是在离开视图那一刻从实时 DOM 抓取的，
   * 而 Map 只能听到滚动*事件*——内容驱动的位移（滚动锚定、流式块折叠）会在没有事件的情况下
   * 移动 scrollTop，导致 Map 里留着的是更早一次访问的位置。没有这个字段，两处恢复会互相竞争，
   * 下面的重试循环会用过期值覆盖备忘里更新的偏移量。
   */
  overrideTop?: number;
  startAtBottom?: boolean;
};

const scrollPositions = new Map<string, number>();
const SCROLL_POSITION_CACHE_MAX_SIZE = 100;

/**
 * 记住并恢复问题详情页的滚动位置。
 * @param options.restoreKey - 唯一标识这次访问的 key。
 * @param options.scrollContainerEl - 滚动容器元素。
 * @param options.ready - 内容是否已就绪，就绪后才开始保存/恢复。
 * @param options.disabled - 为 true 时跳过恢复。
 * @param options.overrideTop - 平台提供的权威恢复目标（优先于本地缓存）。
 */
export function useIssueDetailScrollRestore({
  restoreKey,
  scrollContainerEl,
  ready,
  disabled = false,
  overrideTop,
  startAtBottom = false,
}: UseIssueDetailScrollRestoreArgs) {
  const restoredKeyRef = useRef<string | null>(null);

  useLayoutEffect(() => {
    restoredKeyRef.current = null;
  }, [restoreKey]);

  useLayoutEffect(() => {
    if (!scrollContainerEl || disabled || !ready) return;

    const save = () => {
      saveScrollPosition(restoreKey, scrollContainerEl.scrollTop);
    };

    scrollContainerEl.addEventListener("scroll", save, { passive: true });

    return () => {
      save();
      scrollContainerEl.removeEventListener("scroll", save);
    };
  }, [scrollContainerEl, restoreKey, ready, disabled]);

  useLayoutEffect(() => {
    if (!scrollContainerEl || !ready) return;
    if (disabled) {
      restoredKeyRef.current = restoreKey;
      return;
    }
    if (restoredKeyRef.current === restoreKey) return;

    restoredKeyRef.current = restoreKey;

    if (startAtBottom) {
      // Follow initial asynchronous layout only; user input immediately takes control.
      const jump = () => { scrollContainerEl.scrollTop = scrollContainerEl.scrollHeight; };
      jump();
      const observer = new ResizeObserver(jump);
      for (const child of scrollContainerEl.children) observer.observe(child);
      const stop = () => observer.disconnect();
      scrollContainerEl.addEventListener("wheel", stop, { passive: true, once: true });
      scrollContainerEl.addEventListener("touchstart", stop, { passive: true, once: true });
      scrollContainerEl.addEventListener("keydown", stop, { once: true });
      scrollContainerEl.addEventListener("pointerdown", stop, { once: true });
      const timer = window.setTimeout(stop, 1500);
      return () => {
        stop();
        window.clearTimeout(timer);
        scrollContainerEl.removeEventListener("wheel", stop);
        scrollContainerEl.removeEventListener("touchstart", stop);
        scrollContainerEl.removeEventListener("keydown", stop);
        scrollContainerEl.removeEventListener("pointerdown", stop);
      };
    }

    const target = overrideTop ?? scrollPositions.get(restoreKey) ?? 0;
    if (target <= 1) {
      scrollContainerEl.scrollTop = target;
      return;
    }

    return restoreScrollTopWithRetry(scrollContainerEl, target);
  }, [scrollContainerEl, restoreKey, ready, disabled, overrideTop, startAtBottom]);
}

function saveScrollPosition(restoreKey: string, scrollTop: number) {
  if (scrollPositions.has(restoreKey)) {
    scrollPositions.delete(restoreKey);
  } else if (scrollPositions.size >= SCROLL_POSITION_CACHE_MAX_SIZE) {
    const oldestKey = scrollPositions.keys().next().value;
    if (oldestKey !== undefined) scrollPositions.delete(oldestKey);
  }

  scrollPositions.set(restoreKey, scrollTop);
}

function restoreScrollTopWithRetry(el: HTMLElement, target: number) {
  let cancelled = false;
  let attempts = 0;
  let stableFrames = 0;
  const maxAttempts = 30;
  const requiredStableFrames = 2;

  el.scrollTop = target;

  let frameId: number;

  const tick = () => {
    if (cancelled || !el.isConnected) return;

    attempts += 1;

    if (Math.abs(el.scrollTop - target) <= 1) {
      stableFrames += 1;
    } else {
      stableFrames = 0;
      el.scrollTop = target;
    }

    // 虚拟化时间线在父级 layout effect 之后才初始化，可能会重置同步的滚动写入。
    // 要求跨帧稳定，能让恢复多撑一会儿、熬过那次初始化。
    if (stableFrames >= requiredStableFrames || attempts >= maxAttempts) {
      return;
    }

    frameId = requestAnimationFrame(tick);
  };

  frameId = requestAnimationFrame(tick);

  return () => {
    cancelled = true;
    cancelAnimationFrame(frameId);
  };
}
