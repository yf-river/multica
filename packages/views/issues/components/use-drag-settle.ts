// 本文件：useDragSettle —— 问题看板三处拖拽面（看板/列表/泳道）共用的
// "拖拽-落定"状态机。核心思路：两次拖拽之间，本地列状态镜像 TanStack Query
// 缓存；拖拽中或落定中（移动 mutation 在途）冻结镜像，防止乐观移动被中途落地的
// 缓存更新冲掉；落定后解锁并 bump settleVersion，强制按已对账的缓存重新同步一次。
// 无内容的 setColumns 写入（等值判断）会被跳过，避免触发冷加载更新死循环。

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";

/**
 * 列映射（`columnId -> 有序 issue id 数组`）的内容等值比较：两图具有相同的
 * 列键、且每列的 id 列表逐元素相等即视为相等。用于跳过无效果的 `setColumns` 写入。
 */
function columnsEqual(
  a: Record<string, string[]>,
  b: Record<string, string[]>,
): boolean {
  if (a === b) return true;
  const aKeys = Object.keys(a);
  const bKeys = Object.keys(b);
  if (aKeys.length !== bKeys.length) return false;
  for (const key of aKeys) {
    const av = a[key];
    const bv = b[key];
    if (av === bv) continue;
    if (!av || !bv || av.length !== bv.length) return false;
    for (let i = 0; i < av.length; i++) {
      if (av[i] !== bv[i]) return false;
    }
  }
  return true;
}

/**
 * 问题看板（board-view / list-view）共用的拖拽/落定状态机。
 *
 * 三处拖拽面（看板、列表、泳道）遵循同一契约：
 *
 *   - 本地列状态在两次拖拽之间镜像 TanStack Query 缓存。
 *   - 拖拽中、或一次落下仍在"落定"（移动 mutation 在途）时，镜像被冻结，
 *     避免乐观移动被恰好中途落地的缓存变化冲掉。
 *   - 落定后解锁并 bump `settleVersion`，触发一次按已对账缓存的重新同步。
 *
 * 本 hook 独享这份原语，防止各视图逐渐跑偏（list-view 曾悄悄丢掉乐观移动那半）。
 * 触发同步的 `useEffect` 仍留在各调用方，因为其依赖列表随数据源而异
 *（工作区看板 vs 纯状态列表），但它从这里读 `settleVersion` 与各 ref。
 *
 * `initialColumns` 只在初始化时读一次（useState initializer）；
 * 调用方通过自己的 resync effect + `setColumns` 驱动后续更新。
 */
export function useDragSettle(
  initialColumns: () => Record<string, string[]>,
) {
  const isDraggingRef = useRef(false);
  const isSettlingRef = useRef(false);
  // 限流 onDragOver：本地移动后置 true，一帧之后清除。
  const recentlyMovedRef = useRef(false);
  const [settleVersion, setSettleVersion] = useState(0);

  const [columns, setColumnsState] = useState<Record<string, string[]>>(
    initialColumns,
  );
  const columnsRef = useRef(columns);
  columnsRef.current = columns;

  // 带等值保护的列 setter。当一次重同步（或一次空拖拽）算出的列映射内容与当前
  // 相同，就返回同一个引用，让 React 跳过这次重渲染。这是对"冷加载更新循环"
  // 的第二道防线（MUL-4985）：即使某个 `buildColumns` 输入恢复了每次渲染
  // 都不稳定的身份，内容相等的重建也不会再产生新状态对象，无从驱动 resync
  // effect 空转。它从不改变最终值——只是跳过冗余写入——因此拖拽/落定语义不变。
  const setColumns = useCallback<
    Dispatch<SetStateAction<Record<string, string[]>>>
  >((update) => {
    setColumnsState((prev) => {
      const next =
        typeof update === "function"
          ? (update as (p: Record<string, string[]>) => Record<string, string[]>)(
              prev,
            )
          : update;
      return columnsEqual(prev, next) ? prev : next;
    });
  }, []);

  useEffect(() => {
    const id = requestAnimationFrame(() => {
      recentlyMovedRef.current = false;
    });
    return () => cancelAnimationFrame(id);
  }, [columns]);

  /**
   * 锁上"落定中"并返回要交给移动 mutation 的 `onSettled` 回调。
   * 该回调负责解锁并触发一次重同步。
   */
  const beginSettle = useCallback(() => {
    isSettlingRef.current = true;
    return () => {
      isSettlingRef.current = false;
      setSettleVersion((v) => v + 1);
    };
  }, []);

  return {
    columns,
    setColumns,
    columnsRef,
    isDraggingRef,
    isSettlingRef,
    recentlyMovedRef,
    settleVersion,
    beginSettle,
  };
}
