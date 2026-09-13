"use client";

/**
 * 富文本模块：LazyRichBlock——近视口、尺寸稳定的富块挂载闸门
 * （MUL-4922 性能契约）。
 *
 * 一段很长的会话或评论串可能包含几十个 Mermaid 图和沙箱 HTML iframe。
 * 一次性全部实例化，每个块都要付出一次 Mermaid 渲染（异步、布局开销大）
 * 和一个 iframe（自带文档 + 脚本执行），无论用户最终会不会滚到它们。
 * 这个外壳把每个富叶子推迟到它接近视口时才挂载。
 *
 * 两个特性让它在 Virtuoso 虚拟列表里是安全的：
 *
 * 1. 尺寸稳定。外壳在挂载前就预留块的期望高度，之后把它作为 `min-height`
 *    保留。没有它，块在离屏时会量出 0px，挂载时再跳到真实高度——这正是
 *    让虚拟列表误估项尺寸、丢失滚动位置的测量抖动。这个预留不是本文件里
 *    的猜测：它来自叶子组件自身（有会话缓存的真实图高度就用它，否则用
 *    它们记录的骨架/iframe 高度），所以缓存命中时挂载零位移。
 *
 * 2. 只挂载一次的闩锁。一旦挂载，块就再也不会被卸载，哪怕滚得很远。卸载
 *    会在每一趟都重跑 Mermaid、重建 iframe，还会丢掉查看者的平移/缩放
 *    状态——把一次性成本换成了重复成本。内存被「用户实际滚过的块」所限制，
 *    而不是整个记录。
 */

import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useRichContentScrollRoot } from "./scroll-root";
import {
  hasBlockMounted,
  markBlockMounted,
  mountedBlockKey,
} from "./mounted-block-registry";

// 恢复一个已挂载的块必须在绘制前完成，否则被回收的 Virtuoso 行滚回视野时
// 会先闪一下占位符。useLayoutEffect 不在服务端运行（在那里调用还会告警），
// 所以服务端走普通 effect 分支——两者在 SSR 期间都不运行，首帧不受影响。
const useIsomorphicLayoutEffect =
  typeof window !== "undefined" ? useLayoutEffect : useEffect;

/**
 * 块在视口外多远时开始挂载。取值覆盖 Virtuoso 自身的 overscan（chat 列表
 * 里 `increaseViewportBy` 是顶部 400px / 底部 600px），这样块在滚进视野时
 * 就绪，而不必急切地构建整个记录。
 */
const NEAR_VIEWPORT_ROOT_MARGIN = "800px 0px";

function supportsIntersectionObserver(): boolean {
  return typeof window !== "undefined" && typeof window.IntersectionObserver === "function";
}

/**
 * 近视口、尺寸稳定的富块挂载闸门。
 * @param reservedHeightPx 挂载后的块期望高度，挂载前后都据此预留。
 * @param sourceKey 该块内容的稳定标识，用于记住它已挂载过。
 * @param children 富块叶子内容。
 */
export function LazyRichBlock({
  reservedHeightPx,
  sourceKey,
  children,
}: {
  /** 挂载后的块期望高度；挂载前后都预留。 */
  reservedHeightPx: number;
  /**
   * 该块内容的稳定标识。用于记住它已经挂载过，这样被回收、之后又重建的
   * 虚拟行不会再付一次挂载成本。
   */
  sourceKey?: string;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const scrollRoot = useRichContentScrollRoot();
  const registryKey = sourceKey == null ? null : mountedBlockKey("rich", sourceKey);
  // 首帧在服务端和客户端都始终为 false。
  //
  // 若用特性检测（`typeof window`、IntersectionObserver 是否存在）推导它，
  // 首帧就会依赖环境：服务端没有 `window`，会渲染完整的 Mermaid/HTML 子树，
  // 而浏览器 hydration 阶段渲染占位符——标记不一致，且 SSR 悄悄绕过了它
  // 本应遵守的懒加载闸门。`"use client"` 并不会让组件退出 Next 的服务端
  // 渲染，所以唯一安全的初始状态是两个环境都能达成一致的那个。
  //
  // 所有依赖环境的事都在下面的 effect 里做，它永远不会在服务端运行。
  const [mounted, setMounted] = useState(false);

  // 恢复本会话已经构建过的块。不放进初始状态的原因和这里其他东西一样：
  // 注册表在服务端是空的，从它播种会让首帧在服务端和客户端不一致。
  useIsomorphicLayoutEffect(() => {
    if (mounted || registryKey == null) return;
    if (hasBlockMounted(registryKey)) setMounted(true);
  }, [mounted, registryKey]);

  useEffect(() => {
    if (mounted) return;

    // 没有 IntersectionObserver（jsdom、较老的 webview）：退回急切挂载。
    // 放在 effect 里而不是初始状态，是为了让首个提交帧仍与服务端一致。
    if (!supportsIntersectionObserver()) {
      setMounted(true);
      return;
    }

    const el = ref.current;
    if (!el) return;

    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          // 闩锁：停止观察，让块在滚走后也不会被卸载。
          setMounted(true);
          observer.disconnect();
        }
      },
      {
        // 有界面自己的滚动容器时就相对它裁剪。用默认（视口）root 时，
        // rootMargin 会扩大错误的框，Chat 块只有已经可见了才会加载。
        root: scrollRoot,
        rootMargin: NEAR_VIEWPORT_ROOT_MARGIN,
      },
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, [mounted, scrollRoot]);

  // 记录这次挂载，让被回收的行能直接跳到已构建好的块。
  useEffect(() => {
    if (mounted && registryKey != null) markBlockMounted(registryKey);
  }, [mounted, registryKey]);

  return (
    <div
      ref={ref}
      data-rich-block-shell=""
      data-mounted={mounted ? "" : undefined}
      // 预留会在挂载后继续保留，让外壳永远不会缩回去并再次触发一次测量。
      // 真实图和固定高度的 HTML 预览通常会超过它，所以它很少增加可见空白。
      style={{ minHeight: reservedHeightPx }}
    >
      {mounted ? children : <RichBlockPlaceholder />}
    </div>
  );
}

/**
 * 挂载前的占位符。刻意惰性、且不标记为「加载中」：它只是填充预留空间
 * 而不是宣告有工作在进行，因为此刻还没开始加载——只是块还没被滚到附近。
 */
function RichBlockPlaceholder() {
  return (
    <div
      className="my-3 h-full w-full rounded-md border border-border/50 bg-muted/20"
      aria-hidden="true"
    />
  );
}
