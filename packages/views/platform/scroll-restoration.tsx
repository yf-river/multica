"use client";

import {
  createContext,
  useCallback,
  useContext,
  type ReactNode,
} from "react";

/**
 * platform 模块：基于「拉取」的滚动恢复通道（MUL-4741 状态恢复协议）。
 *
 * 平台（桌面标签宿主）在视图即将离开时捕获滚动偏移，并在视图再次挂载时经
 * 本 context 把它送回来。恢复是视图的「输入」，而不是外部对它的事后 DOM
 * 篡改：
 *
 *   - 虚拟化列表把偏移喂给 `initialScrollTop`，于是列表首次渲染就已经物化出
 *     保存偏移附近的那些行——没有「先顶部再跳」，也不用跟列表自己的高度模型
 *     打架；
 *   - 普通滚动容器在挂载的 ref 回调里（绘制前）直接赋偏移。
 *
 * 容器 key 与捕获侧扫描的 `data-tab-scroll-root` 属性值一一对应；平台按
 * 「路由 + 标签」为它们限定作用域。Web 端（无 provider）每次查找都返回
 * undefined，视图行为与从前一致。
 */

/** 一条被捕获的滚动记录。 */
export interface ScrollRestorationEntry {
  top: number;
  height: number;
  contentKey?: string;
}

/** 外部滚动容器（例如附件 iframe）向桌面标签页提供的快照接口。 */
export interface ExternalScrollSource {
  capture(): ScrollRestorationEntry | null;
}

/** 滚动/视图状态恢复适配器的接口。 */
export interface ScrollRestorationAdapter {
  /**
   * 当前路由上某个容器 key 的已保存记录；没有可恢复内容时为 undefined。
   */
  get(containerKey: string): ScrollRestorationEntry | undefined;
  /**
   * 可恢复视图状态记录——这是 memento 的第二类货，针对那些必须跨视图卸载
   * 存活、却不是滚动偏移的状态（例如「本路由的评论高亮深链已经落地」）。
   * 与滚动不同，它们不是靠扫描 DOM 捕获的：视图在状态变化的那一刻用
   * `setViewState` 写入、挂载时读回。与滚动记录一样按「路由 + 标签」限定
   * 作用域。
   *
   * 保持可选，让只做滚动的适配器依然合法；各 hook 把缺失实现当作「什么都没
   * 存 / 写被忽略」。
   */
  getViewState?(entryKey: string): string | undefined;
  /** 为当前路由写入（字符串）或清除（undefined）一条记录。 */
  setViewState?(entryKey: string, value: string | undefined): void;
  /** 注册/注销一个外部滚动来源。 */
  registerExternalSource?(containerKey: string, source: ExternalScrollSource | null): void;
}

const ScrollRestorationContext = createContext<ScrollRestorationAdapter | null>(
  null,
);

/** 向子树提供滚动恢复适配器。 */
export function ScrollRestorationProvider({
  adapter,
  children,
}: {
  adapter: ScrollRestorationAdapter;
  children: ReactNode;
}) {
  return (
    <ScrollRestorationContext.Provider value={adapter}>
      {children}
    </ScrollRestorationContext.Provider>
  );
}

/**
 * 当前生效的适配器；没有挂 provider 时为 null。当视图只需要一种恢复操作时，
 * 优先用下面更聚焦的 hook。
 */
export function useScrollRestorationAdapter(): ScrollRestorationAdapter | null {
  return useContext(ScrollRestorationContext);
}

/**
 * 当前路由上某个容器 key 的完整已保存记录（含 `contentKey`）——当视图必须
 * 判断保存的偏移对当前渲染的内容是否仍适用时用它（multica-ai#6405）。
 */
export function useRestoredScrollEntry(
  containerKey: string,
): ScrollRestorationEntry | undefined {
  const adapter = useContext(ScrollRestorationContext);
  return adapter?.get(containerKey);
}

/**
 * 当前路由上某个容器 key 的已保存滚动偏移，或 undefined。挂载时读一次
 * （例如喂给 Virtuoso 的 `initialScrollTop`）——它反映的是本路由上次离开时
 * 捕获的状态。
 */
export function useRestoredScrollOffset(containerKey: string): number | undefined {
  return useRestoredScrollEntry(containerKey)?.top;
}

/**
 * 给普通（非虚拟化）滚动容器用的 ref 回调：元素挂载时（commit 阶段、绘制前）
 * 赋上保存的偏移，所以用户看到的第一帧就已经停在恢复的位置。需要时可与其它
 * ref 合并成一个回调。
 */
export function useRestoredScrollRef(
  containerKey: string,
): (el: HTMLElement | null) => void {
  const adapter = useContext(ScrollRestorationContext);
  return useCallback(
    (el: HTMLElement | null) => {
      if (!el) return;
      const saved = adapter?.get(containerKey);
      if (!saved || saved.top <= 0) return;
      el.scrollTop = saved.top;
    },
    [adapter, containerKey],
  );
}

/**
 * 当前路由上某个 key 的已保存视图状态记录，或 undefined。像滚动偏移一样在
 * 挂载时读取——它反映的是本视图上次卸载前记录下来的状态。
 */
export function useRestoredViewState(entryKey: string): string | undefined {
  const adapter = useContext(ScrollRestorationContext);
  return adapter?.getViewState?.(entryKey);
}

/**
 * 视图状态记录的命令式写入器：在可恢复状态变化时调用（传 undefined 清除）。
 * 没有 provider、或适配器只跟踪滚动时为空操作。
 */
export function useViewStateWriter(): (
  entryKey: string,
  value: string | undefined,
) => void {
  const adapter = useContext(ScrollRestorationContext);
  return useCallback(
    (entryKey: string, value: string | undefined) => {
      adapter?.setViewState?.(entryKey, value);
    },
    [adapter],
  );
}
