"use client";

// 问题界面的「多选」上下文：为每个 surface 实例提供独立的选中集合（勾选/全选/取消/清空），
// 供批量操作栏消费。没挂宿主时回退到全局 selection store，保证表格行外也能选中。
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { useIssueSelectionStore } from "@multica/core/issues/stores/selection-store";

/** 问题界面的选中集合及其操作方法。 */
export interface IssueSurfaceSelection {
  selectedIds: Set<string>;
  /** 切换单个问题 id 的选中状态。 */
  toggle: (id: string) => void;
  /** 把给定的一批 id 加入选中。 */
  select: (ids: string[]) => void;
  /** 把给定的一批 id 移出选中。 */
  deselect: (ids: string[]) => void;
  /** 清空全部选中。 */
  clear: () => void;
}

const IssueSurfaceSelectionContext =
  createContext<IssueSurfaceSelection | null>(null);

/**
 * 为一个 surface 实例创建独立的选中状态。
 *
 * `resetKey` 变化时（会员窗口变了，如筛选/搜索变化）会自动清空选中。
 * @param surfaceKey - surface 的标识（不同 surface 互不共享选中）。
 * @param resetKey - 会员窗口的标识；变化即清空选中。默认与 surfaceKey 相同。
 * @returns 选中集合及其操作方法。
 */
export function useCreateIssueSurfaceSelection(
  surfaceKey: string,
  resetKey = surfaceKey,
): IssueSurfaceSelection {
  const [selectedIds, setSelectedIds] = useState(() => new Set<string>());
  const [committedResetKey, setCommittedResetKey] = useState(resetKey);

  // 在渲染阶段重置（React 的「props 变化时调整 state」模式），而不是用 effect：
  // effect 在提交之后才运行，中间会有一帧带着「新会员（筛选/搜索）」和「旧选中」
  // 上屏——批量操作的消费方可能在这帧里作用到窗口已经不再包含的行上。渲染期间设置
  // state 会在提交前同步地重新渲染，因此永远不会有那样一帧可见。size 守卫让常见的
  // 「没选中任何东西时 key 变化」避免新建 Set 和重渲染整个 surface。
  if (committedResetKey !== resetKey) {
    setCommittedResetKey(resetKey);
    if (selectedIds.size > 0) setSelectedIds(new Set());
  }

  const toggle = useCallback((id: string) => {
    setSelectedIds((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const select = useCallback((ids: string[]) => {
    setSelectedIds((current) => {
      const next = new Set(current);
      for (const id of ids) next.add(id);
      return next;
    });
  }, []);

  const deselect = useCallback((ids: string[]) => {
    setSelectedIds((current) => {
      const next = new Set(current);
      for (const id of ids) next.delete(id);
      return next;
    });
  }, []);

  const clear = useCallback(() => {
    setSelectedIds(new Set());
  }, []);

  return useMemo(
    () => ({ selectedIds, toggle, select, deselect, clear }),
    [clear, deselect, select, selectedIds, toggle],
  );
}

/**
 * 提供问题界面的选中上下文。
 * @param props.selection - 该 surface 的选中集合与操作方法。
 * @param props.children - 需要消费选中的子内容。
 */
export function IssueSurfaceSelectionProvider({
  selection,
  children,
}: {
  selection: IssueSurfaceSelection;
  children: ReactNode;
}) {
  return (
    <IssueSurfaceSelectionContext.Provider value={selection}>
      {children}
    </IssueSurfaceSelectionContext.Provider>
  );
}

/** 读取 surface 级选中上下文；没有宿主时返回 null。 */
export function useIssueSurfaceSelectionOptional() {
  return useContext(IssueSurfaceSelectionContext);
}

/**
 * 读取问题界面的选中状态：优先用 surface 级上下文；未包裹时回退到全局 selection store
 * （例如表格行/详情侧栏等不在 surface 上下文里的场景）。
 */
export function useIssueSurfaceSelection(): IssueSurfaceSelection {
  const surfaceSelection = useIssueSurfaceSelectionOptional();
  const globalSelectedIds = useIssueSelectionStore((s) => s.selectedIds);
  const globalToggle = useIssueSelectionStore((s) => s.toggle);
  const globalSelect = useIssueSelectionStore((s) => s.select);
  const globalDeselect = useIssueSelectionStore((s) => s.deselect);
  const globalClear = useIssueSelectionStore((s) => s.clear);

  return (
    surfaceSelection ?? {
      selectedIds: globalSelectedIds,
      toggle: globalToggle,
      select: globalSelect,
      deselect: globalDeselect,
      clear: globalClear,
    }
  );
}
