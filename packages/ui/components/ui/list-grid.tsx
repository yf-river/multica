"use client";

import { ArrowDown, ArrowUp } from "lucide-react";

import { cn } from "../../lib/utils";

// 本文件是「Linear 风格列表网格」组件族：用 CSS grid 把表头与每一行都对齐到
// 同一套列轨道上，实现真正的表格对齐与吸附表头。声明即文档——容器类名里把
// 全部列轨道写死一次（含响应式变体）；表头和行用 `grid-cols-subgrid` 继承整个
// 列模板，列宽只有单一事实来源，表头/行/骨架屏之间永不漂移。
//
// 容器类名必须遵守的约定：
// - 首尾两列是留白列（例如 1.25rem）：让行悬停背景可以横向满幅，而内容仍与
//   页面边缘对齐。ListGridHeader / ListGridRow 会渲染对应的占位格子。
// - 响应式是「两档」的、且由容器查询驱动（把 ListGrid 包在一个 `@container`
//   元素里，用 `@<断点>:` 变体，绝不使用视口 `sm:`/`lg:`——这样才能算上侧栏
//   和分栏面板的宽度）：
//   - ≥ @2xl：所见即所得——用户开启的每一列都渲染。网格自带
//     `@2xl:min-w-[var(--…-minw)]`（由页面的列宽常量算出的各轨道 + 间距之和），
//     外层容器带 `overflow-x-auto`，所以列设多了是横向滚动而不是被裁掉。
//     已开启的列绝不允许在某档宽度下被悄悄隐藏——那种「开关开了却没反应」
//     的 bug 已经翻过两次车。
//   - < @2xl：退到静态核心模板（名称列 + 一个关键列），不横向滚动，
//     列开关不生效。非核心单元格带 `hidden @2xl:flex`；
//     display:none 的单元格会从 subgrid 自动排布中消失，剩下的单元格
//     正好填进正确的轨道。
// - 类名必须在页面源码里保持字面量字符串，Tailwind 才能扫到它。

/** 列表排序方向 */
export type ListGridSortDirection = "asc" | "desc";

/**
 * ListGrid：列表网格容器。必须由调用方在 className 里声明整张列模板，
 * 见文件头注释的约定。
 */
function ListGrid({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      role="table"
      className={cn("grid w-full min-w-0 content-start gap-x-3", className)}
      {...props}
    />
  );
}

/**
 * ListGridHeader：吸顶表头行。首尾会自动补占位格，与行悬停背景的满幅留白对齐；
 * 底部带一道向下渐隐，让内容滚过表头下方时有层次。
 */
function ListGridHeader({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      role="row"
      className={cn(
        "group/header sticky top-0 z-10 col-span-full grid h-9 grid-cols-subgrid items-center bg-background after:pointer-events-none after:absolute after:inset-x-0 after:top-full after:h-3 after:bg-gradient-to-b after:from-background after:to-transparent",
        className,
      )}
      {...props}
    >
      <span aria-hidden="true" />
      {children}
      <span aria-hidden="true" />
    </div>
  );
}

/** ListGridHeaderCell 的 props 定义 */
interface ListGridHeaderCellProps
  extends React.HTMLAttributes<HTMLDivElement> {
  /** 该列当前的排序状态；非当前排序列传 false */
  sorted?: ListGridSortDirection | false;
  /** 提供时表头渲染成可点击的排序按钮 */
  onSort?: () => void;
  /** 对齐方向：'left' / 'right' */
  align?: "left" | "right";
}

/**
 * ListGridHeaderCell：单个列头。无 onSort 时是静态文本；有 onSort 时是
 * 排序按钮，按当前排序方向显示 ↑/↓ 箭头（未激活列悬停时才出现箭头）。
 */
function ListGridHeaderCell({
  sorted = false,
  onSort,
  align = "left",
  className,
  children,
  ...props
}: ListGridHeaderCellProps) {
  if (!onSort) {
    return (
      <div
        role="columnheader"
        className={cn(
          "flex min-w-0 items-center px-2 text-caption text-muted-foreground",
          align === "right" && "justify-end",
          className,
        )}
        {...props}
      >
        {children}
      </div>
    );
  }
  const Arrow = sorted === "asc" ? ArrowUp : ArrowDown;
  return (
    <div
      role="columnheader"
      aria-sort={
        sorted === "asc"
          ? "ascending"
          : sorted === "desc"
            ? "descending"
            : undefined
      }
      className={cn(
        "flex min-w-0 items-center px-2",
        align === "right" && "justify-end",
        className,
      )}
      {...props}
    >
      <button
        type="button"
        onClick={onSort}
        className={cn(
          "group/sort flex h-6 items-center gap-0.5 rounded-md text-caption transition-colors",
          // 激活的排序列：靠字重与前景全色来强调——不加背景，
          // 让表头这一行保持安静。
          sorted
            ? "font-medium text-foreground"
            : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
          align === "right" ? "-mr-1.5 flex-row-reverse pl-1 pr-1.5" : "-ml-1.5 pl-1.5 pr-1",
        )}
      >
        {children}
        <Arrow
          className={cn(
            "size-3 shrink-0",
            sorted ? "opacity-100" : "opacity-0 group-hover/sort:opacity-50",
          )}
        />
      </button>
    </div>
  );
}

// 行区——一个纯 subgrid 透传容器，负责把各数据行归拢并承载虚拟滚动的上下
// 内边距。它自己不滚动：横向、纵向两个滚动轴都在外层唯一的容器上
//（`overflow-auto @container`），吸附表头就固定在那个滚动器内部。当初把横向
// 滚动（外层）和纵向滚动（本元素）拆开、再用 h-full 百分比桥接，结果导致
// 布局无法收敛（双滚动条来回闪）、最后一行被横向滚动条盖住，还会让竖向
// 滚动条把行轨道与表头错位。
function ListGridBody({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      role="rowgroup"
      className={cn(
        "col-span-full grid grid-cols-subgrid content-start",
        className,
      )}
      {...props}
    />
  );
}

// 行区底部要追加的这一段留白：让最后一行能滚出钉在面板底边的悬浮 UI 的
// 遮挡（右下角的聊天 FAB 约占 48px，批量操作栏约 62px）。多加一行高度，
// 让两者都碰不到最后一行的「···」菜单。
export const LIST_GRID_BOTTOM_CLEARANCE = 64;

// 一行是普通的 <div>，而不是 <a>——这样可交互的单元格（复选框、···菜单、
// 行内编辑器）都是合法的兄弟节点，而不是嵌在锚点里的可交互内容（那是非法
// HTML，会让原生导航在点击每个子元素时都触发一次）。整行跳转只是给鼠标的
// 便捷层，靠 onClick/onAuxClick 实现（见 views 的 useRowLink）；键盘和屏幕
// 阅读器能访问的链接、以及右键「在新标签页打开」，都在名称格里的真正
// <AppLink> 上。可交互单元格会调用 stopPropagation，点它们不会触发整行导航。
type ListGridRowProps = React.HTMLAttributes<HTMLDivElement>;

/** ListGridRow：一条数据行。首尾自动补占位格对齐留白列，行悬停整行高亮 */
function ListGridRow({ className, children, ...props }: ListGridRowProps) {
  return (
    <div
      role="row"
      className={cn(
        "group/row col-span-full grid h-12 grid-cols-subgrid items-center transition-colors hover:bg-accent/40",
        className,
      )}
      {...props}
    >
      <span aria-hidden="true" />
      {children}
      <span aria-hidden="true" />
    </div>
  );
}

// 单元格与表头格共用同一份默认水平内边距，表头永远不会与行内容错位。
// 结构性列（复选框、···菜单）用 px-0 取消这份内边距。
/** ListGridCell：单个数据单元格 */
function ListGridCell({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      role="cell"
      className={cn("flex min-w-0 items-center px-2", className)}
      {...props}
    />
  );
}

export {
  ListGrid,
  ListGridBody,
  ListGridHeader,
  ListGridHeaderCell,
  ListGridRow,
  ListGridCell,
};
