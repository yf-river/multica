// 虚拟列表首帧填充组件：在 Virtuoso 拿到滚动容器之前，先用少量真实行填满首屏，
// 避免路由回来那一刻列表区域白板闪烁。只处理"第一个渲染帧"的过渡，随后就交接给
// 真正的 <Virtuoso>。

"use client";

import { Fragment, type Key, type ReactNode } from "react";

/**
 * 首帧种子行数的上限：既作为 {@link VirtuosoSeed} 的默认填充数量，也作为虚拟化的
 * issue/inbox 列表 `initialItemCount` 的值。
 *
 * 为什么是"一屏"：路由返回重挂载时，一屏行数足以填满视口；真正的 Virtuoso 会在
 * 下一帧按测量到的窗口裁剪。设上限是为了让超大列表永远不用付出一次全量同步挂载
 * 的代价——那是虚拟化改造前真实桌面端遇到的崩溃点（MUL-4750）。
 */
export const VIRTUOSO_SEED_COUNT = 30;

/**
 * Virtuoso 的 `customScrollParent` 就绪前的非虚拟化兜底渲染。
 *
 * 背景：issue/inbox 列表通过回调 ref 把滚动元素交给 Virtuoso，ref 落在 state 里，
 * 所以重挂载后的首帧 `scrollParent === null`，Virtuoso 此时无法挂载。如果这里渲染
 * 空，组/列头在、行却空白，直到 ref 落定并完成测量——这正是 MUL-4474 虚拟化改造
 * 被回退的闪烁。用真实行的有界切片填住首帧，滚动容器就绪后再切到带
 * `initialItemCount` 的 `<Virtuoso>`，交接在视觉上连续无闪。
 *
 * 种子行直接复用调用方自己的 `itemContent`/`computeItemKey`，因此和虚拟化渲染的
 * 行逐字节一致，不存在第二条会漂移的渲染路径。
 *
 * 提供 `estimatedItemHeight` 时，超出种子数量的行用一个"剩余行数 × 估算高度"的
 * 尾部占位撑起滚动高度（滚动条拇指从首帧起就接近全量列表）；不传的话首帧滚动高度
 * 只到种子行，交接给铺满全量的 Virtuoso 时滚动条会明显重画。传入的估算值应与
 * Virtuoso 的 `defaultItemHeight` 一致，让两个阶段在真实测量落地前保持一致。
 * @param data 全部行数据。
 * @param itemContent 渲染单行内容的函数（索引 + 行数据 → React 节点）。
 * @param computeItemKey 计算每行 key 的函数，用于 React 复用。
 * @param count 首帧渲染的行数上限，默认 {@link VIRTUOSO_SEED_COUNT}。
 * @param estimatedItemHeight 行高估算（px，含行间距），用于给超出 `count` 的行预留滚动高度。
 */
export function VirtuosoSeed<T>({
  data,
  itemContent,
  computeItemKey,
  count = VIRTUOSO_SEED_COUNT,
  estimatedItemHeight,
}: {
  data: T[];
  itemContent: (index: number, item: T) => ReactNode;
  computeItemKey: (index: number, item: T) => Key;
  count?: number;
  /** 行高估算（px，含每行间隙），用来为 `count` 之外的行预留滚动高度。 */
  estimatedItemHeight?: number;
}) {
  const seeded = data.slice(0, count);
  const remaining = data.length - seeded.length;
  return (
    <>
      {seeded.map((item, index) => (
        <Fragment key={computeItemKey(index, item)}>
          {itemContent(index, item)}
        </Fragment>
      ))}
      {estimatedItemHeight !== undefined && remaining > 0 && (
        // 剩余行的滚动高度占位。纯撑高度，无内容，对辅助技术隐藏。
        <div aria-hidden style={{ height: remaining * estimatedItemHeight }} />
      )}
    </>
  );
}
