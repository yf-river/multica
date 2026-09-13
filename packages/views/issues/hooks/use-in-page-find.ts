"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { getShortcut, shortcutMatchesEvent } from "@multica/core/shortcuts";
import { isImeComposing } from "@multica/core/utils";

// ---------------------------------------------------------------------------
// 问题详情页的「页内查找」（Cmd/Ctrl+F）。
//
// 评论时间线是虚拟化的，原生浏览器查找只能看到视口里挂载的那几条评论。
// 这个 hook 驱动一个真正的页内查找栏：查找打开时宿主强制把时间线平铺渲染（让每条评论都进 DOM），
// 匹配结果用 CSS Custom Highlight API 上色——只操作 Range、绝不改动 DOM，
// 所以既能作用于 React 渲染的 markdown，也能作用于 contenteditable 的标题/描述编辑器，
// 而不用和 ProseMirror 较劲。
// ---------------------------------------------------------------------------

const HIGHLIGHT_NAME = "multica-find";
const ACTIVE_HIGHLIGHT_NAME = "multica-find-active";

// 特性检测，在每个调用点惰性求值。不支持 CSS Custom Highlight API 的浏览器上，
// 查找栏依然能打开和导航，只是不上色。还要为 SSR（没有 window）兜底 `CSS`/`Highlight`。
function highlightApiSupported(): boolean {
  return (
    typeof CSS !== "undefined" &&
    "highlights" in CSS &&
    typeof Highlight !== "undefined"
  );
}

// 永不搜索的元素文本。
const SKIP_TAGS = new Set(["SCRIPT", "STYLE", "NOSCRIPT"]);

/** 一条文本匹配：所在的文本节点以及起止下标。 */
export interface TextMatch {
  node: Text;
  start: number;
  end: number;
}

// 遍历 `root` 下每个可见文本节点，按文档顺序返回 `query` 的每处大小写不敏感匹配。
// 匹配限定在单个文本节点内——跨元素边界的查询（比如跨一段加粗文本）不会被找到，
// 这是轻量级查找的标准取舍。
//
// 纯函数、只依赖 DOM（不用 React / CSS API），因此可以在 jsdom 里做单元测试。
/**
 * 收集根节点下所有可见文本中出现的查询字符串。
 * @param root - 被搜索的根元素。
 * @param query - 要查找的字符串。
 * @returns 按文档顺序排列的匹配列表。
 */
export function collectTextMatches(root: HTMLElement, query: string): TextMatch[] {
  const matches: TextMatch[] = [];
  const needle = query.toLowerCase();
  if (!needle) return matches;

  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      const parent = node.parentElement;
      if (!parent) return NodeFilter.FILTER_REJECT;
      if (SKIP_TAGS.has(parent.tagName)) return NodeFilter.FILTER_REJECT;
      if (parent.closest("[data-find-ignore]")) return NodeFilter.FILTER_REJECT;
      const value = node.nodeValue;
      if (!value || value.trim().length === 0) return NodeFilter.FILTER_REJECT;
      return NodeFilter.FILTER_ACCEPT;
    },
  });

  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const textNode = node as Text;
    const haystack = (textNode.nodeValue ?? "").toLowerCase();
    let index = haystack.indexOf(needle);
    while (index !== -1) {
      matches.push({ node: textNode, start: index, end: index + needle.length });
      index = haystack.indexOf(needle, index + needle.length);
    }
  }

  return matches;
}

function isElementVisible(el: HTMLElement | null): boolean {
  return !!el && el.getClientRects().length > 0;
}

// 直接驱动滚动容器的 scrollTop 把 `range` 滚进视野。绝不用原生的 scrollIntoView：
// 在本页它会把滚动传播到桌面壳的包装层，把整个视图顶出屏幕（#3929）。
// 只有匹配确实处在舒适视口带之外时才滚动。
function scrollRangeIntoView(container: HTMLElement | null, range: Range): void {
  if (!container) return;
  const rects = range.getClientRects();
  const rect = rects.length > 0 ? rects[0]! : range.getBoundingClientRect();
  if (!rect || (rect.width === 0 && rect.height === 0)) return;

  const containerRect = container.getBoundingClientRect();
  const pad = 80;
  const above = rect.top < containerRect.top + pad;
  const below = rect.bottom > containerRect.bottom - pad;
  if (!above && !below) return;

  const offsetWithin = rect.top - containerRect.top + container.scrollTop;
  const target = offsetWithin - container.clientHeight / 2 + rect.height / 2;
  container.scrollTop = Math.max(0, target);
}

/** useInPageFind 返回的页内查找状态与操作。 */
export interface UseInPageFindResult {
  /** 查找栏是否打开。 */
  open: boolean;
  /** 当前查询字符串。 */
  query: string;
  /** 当前查询的匹配总数。 */
  matchCount: number;
  /** 当前活动匹配的 0 基下标，无匹配时为 -1。 */
  activeIndex: number;
  /** 当前浏览器是否支持 CSS Custom Highlight API。 */
  supported: boolean;
  /** 查找输入框的 ref。 */
  inputRef: React.RefObject<HTMLInputElement | null>;
  /** 设置查询字符串。 */
  setQuery: (value: string) => void;
  /** 打开查找栏。 */
  openFind: () => void;
  /** 关闭查找栏。 */
  closeFind: () => void;
  /** 跳到下一个匹配。 */
  goNext: () => void;
  /** 跳到上一个匹配。 */
  goPrev: () => void;
}

/**
 * 页内查找 hook：提供 Cmd/Ctrl+F 查找栏的状态、匹配计数、上下导航和匹配高亮。
 * @param options.container - 被搜索的滚动容器。
 * @param options.contentKey - 任何可搜索内容变化时都会变化的值（如时间线长度），
 *   在 DOM 稳定后触发匹配重算。
 * @param options.enabled - 为 false 时 Cmd/Ctrl+F 快捷键失效（如问题仍在加载）。
 * @returns 页内查找的状态与操作集合。
 */
export function useInPageFind(options: {
  /** 被搜索的滚动容器。 */
  container: HTMLElement | null;
  /**
   * 任何可搜索内容变化时都会变化的值（如时间线长度）。
   * 在 DOM 稳定后触发匹配重算。
   */
  contentKey: unknown;
  /** 为 false 时 Cmd/Ctrl+F 快捷键失效（如问题仍在加载）。 */
  enabled?: boolean;
}): UseInPageFindResult {
  const { container, contentKey, enabled = true } = options;

  const [open, setOpen] = useState(false);
  const [query, setQueryState] = useState("");
  const [matchCount, setMatchCount] = useState(0);
  const [activeIndex, setActiveIndex] = useState(-1);

  const inputRef = useRef<HTMLInputElement | null>(null);
  const containerRef = useRef<HTMLElement | null>(container);
  containerRef.current = container;
  const rangesRef = useRef<Range[]>([]);
  const activeIndexRef = useRef(activeIndex);
  activeIndexRef.current = activeIndex;
  const supported = highlightApiSupported();

  const clearHighlights = useCallback(() => {
    if (!supported) return;
    CSS.highlights.delete(HIGHLIGHT_NAME);
    CSS.highlights.delete(ACTIVE_HIGHLIGHT_NAME);
  }, [supported]);

  // 把活动匹配画在所有匹配的底色之上（优先级更高），需要时再滚进视野。
  // 用户导航时和重算之后都会调用，保证活动底色始终跟随实时 Range——
  // 即使匹配总数没变，一个已经过期的 Range（异步重新高亮替换了文本节点）也会被替换掉。
  const applyActive = useCallback(
    (ranges: Range[], index: number, scroll: boolean) => {
      const range = index >= 0 ? ranges[index] : undefined;
      // 画活动底色是唯一只依赖 highlight API 的步骤。活动跟踪和滚动到匹配
      // 必须在没有 CSS Custom Highlight API 的浏览器上照常工作，
      // 否则上一个/下一个只会打开查找栏却永远不移动视图。
      if (supported) {
        if (range) {
          const active = new Highlight(range);
          active.priority = 1;
          CSS.highlights.set(ACTIVE_HIGHLIGHT_NAME, active);
        } else {
          CSS.highlights.delete(ACTIVE_HIGHLIGHT_NAME);
        }
      }
      if (range && scroll) scrollRangeIntoView(containerRef.current, range);
    },
    [supported],
  );

  // 从实时 DOM 重建匹配集合。`resetActive` 表示导航从头开始（查询变了）；
  // 否则内容在下方变化（流式评论、重新高亮）时保留当前活动下标。
  const recompute = useCallback(
    (resetActive: boolean) => {
      const root = containerRef.current;
      if (!open || !root || query.trim().length === 0) {
        rangesRef.current = [];
        clearHighlights();
        setMatchCount(0);
        setActiveIndex(-1);
        return;
      }

      const matches = collectTextMatches(root, query);
      const ranges = matches.map((m) => {
        const range = new Range();
        range.setStart(m.node, m.start);
        range.setEnd(m.node, m.end);
        return range;
      });
      rangesRef.current = ranges;

      if (ranges.length === 0) {
        clearHighlights();
        setMatchCount(0);
        setActiveIndex(-1);
        return;
      }

      if (supported) {
        CSS.highlights.set(HIGHLIGHT_NAME, new Highlight(...ranges));
      }
      setMatchCount(ranges.length);
      const prev = activeIndexRef.current;
      const nextActive =
        resetActive || prev < 0 ? 0 : Math.min(prev, ranges.length - 1);
      setActiveIndex(nextActive);
      // 只在查询变化时（resetActive）才滚动——绝不在内容于固定活动匹配下方变化时猛拉视图。
      applyActive(ranges, nextActive, resetActive);
    },
    [open, query, supported, clearHighlights, applyActive],
  );

  const recomputeRef = useRef(recompute);
  recomputeRef.current = recompute;

  // 查询变化或查找栏开/关时重算 → 从第一个匹配重新开始。
  // 延迟一帧，让宿主在打开时切换到的平铺（非虚拟化）渲染先提交，再遍历 DOM。
  useEffect(() => {
    const raf = requestAnimationFrame(() => recomputeRef.current(true));
    return () => cancelAnimationFrame(raf);
  }, [open, query, container]);

  // 可搜索内容变化时重算，并保留当前活动匹配。
  useEffect(() => {
    if (!open) return;
    const raf = requestAnimationFrame(() => recomputeRef.current(false));
    return () => cancelAnimationFrame(raf);
  }, [contentKey, open]);

  // 异步的 DOM 变动（markdown/代码高亮稳定下来、流式回复）会让 Range 失效；
  // 重新推导它们，把成批的变动合并到一帧里。无论是否支持高亮都会运行，
  // 让降级场景下的计数/导航始终和实时 DOM 保持同步。
  useEffect(() => {
    if (!open || !container) return;
    let raf = 0;
    const observer = new MutationObserver(() => {
      cancelAnimationFrame(raf);
      raf = requestAnimationFrame(() => recomputeRef.current(false));
    });
    observer.observe(container, {
      subtree: true,
      childList: true,
      characterData: true,
    });
    return () => {
      observer.disconnect();
      cancelAnimationFrame(raf);
    };
  }, [open, container]);

  // 用户在匹配之间步进时移动活动底色并滚动。
  // （内容驱动变化时的活动底色由 recompute 处理。）
  useEffect(() => {
    if (!open) return;
    applyActive(rangesRef.current, activeIndex, true);
  }, [activeIndex, open, applyActive]);

  // 全局 Cmd/Ctrl+F。监听器只作用在当前挂载的问题详情上；桌面端 <Activity>
  // 会随标签可见性循环挂载/卸载这个 effect，所以只有可见标签拦截按键。
  // 可见性守卫是防御性的。
  useEffect(() => {
    if (!enabled) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.repeat || isImeComposing(e)) return;
      if (!shortcutMatchesEvent(getShortcut("findInIssue"), e)) return;
      if (!isElementVisible(containerRef.current)) return;
      e.preventDefault();
      setOpen(true);
      requestAnimationFrame(() => {
        const input = inputRef.current;
        if (input) {
          input.focus();
          input.select();
        }
      });
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [enabled]);

  // 卸载时清除高亮，避免导航后残留过期的底色。
  useEffect(() => clearHighlights, [clearHighlights]);

  const setQuery = useCallback((value: string) => setQueryState(value), []);
  const openFind = useCallback(() => setOpen(true), []);
  const closeFind = useCallback(() => setOpen(false), []);

  const goNext = useCallback(() => {
    setActiveIndex((prev) => {
      const n = rangesRef.current.length;
      if (n === 0) return -1;
      return prev < 0 ? 0 : (prev + 1) % n;
    });
  }, []);

  const goPrev = useCallback(() => {
    setActiveIndex((prev) => {
      const n = rangesRef.current.length;
      if (n === 0) return -1;
      return prev < 0 ? n - 1 : (prev - 1 + n) % n;
    });
  }, []);

  return {
    open,
    query,
    matchCount,
    activeIndex,
    supported,
    inputRef,
    setQuery,
    openFind,
    closeFind,
    goNext,
    goPrev,
  };
}
