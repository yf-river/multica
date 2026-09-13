import type { Column, RowData } from "@tanstack/react-table";
import type * as React from "react";

// 本文件是 DataTable 组件的配套样式/尺寸工具：
// 扩展 ColumnMeta 提供 `grow` 列标记，以及生成列宽 CSS 变量与
// 单元格的粘性定位（固定列）样式、滚动容器的渐隐遮罩。

// 用声明合并给 TanStack Table 的 ColumnMeta 扩展一个 `grow` 标志。TanStack
// 会给每个 columnDef 塞一个默认 `size: 150`，所以没法靠检查 columnDef.size
// 来判断「没显式设宽度」（它永远是个数字）。设 `meta: { grow: true }` 才是
// 官方扩展点：DataTable 对这种列不写行内宽度，直到用户显式拖过宽，之后
// 拖出的宽度生效。
declare module "@tanstack/react-table" {
  interface ColumnMeta<TData extends RowData, TValue> {
    grow?: boolean;
  }
}

// 列宽对应的 CSS 自定义属性名。列 id 由应用定义，可能含 CSS 标识符不允许
// 的字符（`property:<uuid>` 就是活例子），所以标识符集之外的字符一律折叠成
// `_`。实际用到的 id 远在此前就各不相同，折叠不会产生碰撞。
export function columnSizeVar(columnId: string) {
  return `--col-${columnId.replace(/[^a-zA-Z0-9_-]/g, "_")}-size`;
}

// 给某个 <th>/<td> 合成「宽度 + 固定列」样式。列带 `meta.grow` 时不输出宽度
//（它们靠 fixed 布局的剩余空间分配）。固定列要 sticky 定位——见下面注释。
//
// 宽度写成对自定义属性的引用（该属性在 <table> 上只发布一次），而不是每个
// 单元格读一个数字。拖宽期间浏览器从「一处声明变化」套用所有新宽度，无需让
// 成百上千个单元格各自重读 column.getSize()。
//
// 背景色刻意不写在行内——上游 Dice UI 在这里写 `background: var(--background)`，
// 那种写法无法响应 `:hover`。消费方用 Tailwind 类搭配 `group-hover:` 自己上色。
export function getCellStyle<TData>(
  column: Column<TData>,
  options?: { hasExplicitSize?: boolean },
): React.CSSProperties {
  const grow = column.columnDef.meta?.grow;
  const width =
    grow && !options?.hasExplicitSize
      ? undefined
      : `var(${columnSizeVar(column.id)})`;

  const isPinned = column.getIsPinned();
  if (!isPinned) {
    return width !== undefined ? { width } : {};
  }

  // 这里不画边界标记：固定列结束的位置由滚动容器的遮罩来画——固定格内的
  // 阴影只能压暗固定列自己的边，而遮罩淡出的是真正从下面滑过的内容，
  // 描述的才是那件事。
  return {
    width,
    position: "sticky",
    left: isPinned === "left" ? `${column.getStart("left")}px` : undefined,
    right: isPinned === "right" ? `${column.getAfter("right")}px` : undefined,
    zIndex: 1,
  };
}

// 滚动容器的遮罩：从固定列底下冒出来的内容在 `fade` 像素内淡入，
// 而不是在一条硬边上直接出现。
//
// 渐变停止点锚定在固定块的宽度上，绝不锚定滚动偏移——滚动容器上的遮罩是相对
// 它的 padding box 定位的，而 padding box 不滚动，所以淡出带始终焊在边界上。
// 锚定宽度也让固定列自己保持全不透明：那种通用的「把滚动条两端都淡掉」写法，
// 会恰好溶解掉那些必须保持实心的固定列。
export function pinnedScrollMask(
  pinnedWidth: number,
  fade = 24,
): React.CSSProperties | undefined {
  if (pinnedWidth <= 0) return undefined;
  const gradient =
    `linear-gradient(to right, black 0, black ${pinnedWidth}px, ` +
    `transparent ${pinnedWidth}px, black ${pinnedWidth + fade}px, black 100%)`;
  return { maskImage: gradient, WebkitMaskImage: gradient };
}
