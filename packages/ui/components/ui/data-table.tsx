"use client";

import {
  flexRender,
  type ColumnSizingState,
  type Header as TanstackHeader,
  type Row,
  type Table as TanstackTable,
} from "@tanstack/react-table";
import { useVirtualizer, type VirtualItem } from "@tanstack/react-virtual";
import * as React from "react";
import { createPortal } from "react-dom";

// 我们刻意用 shadcn 低层原语（TableHeader/TableBody/TableRow/TableHead/TableCell）
// 而不是外层 <Table>。shadcn 的 <Table> 会把 <table> 嵌进一个
// `overflow-x-auto` 的 div 里，这会让它和我们自己的外层滚动容器抢活，
// 并把横向滚动条钉在表格底部而不是视口上。
import {
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@multica/ui/components/ui/table";
import { columnSizeVar, getCellStyle } from "@multica/ui/lib/data-table";
import { cn } from "@multica/ui/lib/utils";

// 指针移动超过该距离，才把「按下拖拽柄」判定为一次拖拽。与列排序拖拽
// 传感器的激活距离一致，让同一表头上两种手势手感一致；同时保证单纯点一下
// 不会真的提交一列宽度。
const RESIZE_DRAG_THRESHOLD = 4;

/** DataTable 组件的 props 定义 */
interface DataTableProps<TData> extends React.ComponentProps<"div"> {
  /** TanStack Table 实例（由调用方 useReactTable 创建并传进来） */
  table: TanstackTable<TData>;
  // 选中 ≥1 行时显示在表格下方的操作条。目前产品还没启用行选中——
  // 保留在 API 里是为了和 Dice UI 原组件对齐，未来要做行选择功能时直接用。
  actionBar?: React.ReactNode;
  /** 空数据时表格里展示的文案覆盖 */
  emptyMessage?: React.ReactNode;
  // 用户点击某行时触发（点可交互子元素不算——单元格里的按钮/下拉要在
  // 自己的事件里 stopPropagation）。用整行点击跳详情页，而不在 <tr> 里嵌
  // <a>（那是非法 HTML）。回调会带上鼠标事件，调用方可以处理修饰键；
  // 中键点击也走同一个回调（调用方自己读 event.button 区分）。
  onRowClick?: (row: Row<TData>, event: React.MouseEvent) => void;
  // 语义行的逃生舱：比如可折叠的分组表头。返回 null/undefined 时用
  // 标准数据行渲染。
  renderRow?: (row: Row<TData>) => React.ReactNode;
  /** 调用方自带的 <tfoot>（合计行 / 快捷新建行等） */
  footer?: React.ReactNode;
  // 大表只渲染可见的那一段行窗口。行高稳定时调用方应开启；footer 仍属于
  // 同一滚动面，在虚拟行空间之后仍可滚到。
  virtualizeRows?: boolean;
  /** 行高估计值（虚拟滚动用，会被实际测量纠正） */
  virtualRowHeight?: number;
  /** 可视窗口上下额外渲染的行数（滚动缓冲） */
  virtualOverscan?: number;
}

// 无头（headless）数据表格壳，改编自 Dice UI 的 data-table
//（https://diceui.com/r/data-table）：拿到一个 TanStack Table 实例，
// 用 shadcn/ui 的表格原语把它渲染出来。
//
// 布局行为：
//   - `w-full` + `table-fixed` 让表格始终是视口宽，且每列的宽度来自首行
//     <th> 的行内宽。对设置了宽度的列，column.size 是权威值。
//   - 标了 `meta.grow: true` 的列不写行内宽，fixed 布局会把剩余空间分给它；
//     等用户手动拖过之后才应用明确宽度。
//   - 表格的 min-width = 所有列 TanStack size 之和（table.getTotalSize()）。
//     这给 grow 列一个真实下限——fixed 模式忽略单元格级 min-width，但会
//     尊重表格自身的 min-width。容器比 min-width 宽时表格跟着撑满；更窄时
//     表格钉在 min-width，外层 overflow-auto 负责滚动。
export function DataTable<TData>({
  table,
  actionBar,
  emptyMessage = "No results.",
  onRowClick,
  renderRow,
  footer,
  virtualizeRows = false,
  virtualRowHeight = 41,
  virtualOverscan = 10,
  className,
  ...props
}: DataTableProps<TData>) {
  const [resizingColumnId, setResizingColumnId] = React.useState<string | null>(
    null,
  );

  const columnSizing = table.getState().columnSizing;
  const hasExplicitSize = React.useCallback(
    (columnId: string) =>
      Object.prototype.hasOwnProperty.call(columnSizing, columnId),
    [columnSizing],
  );

  const clampColumnWidth = React.useCallback(
    (columnId: string, width: number) => {
      const columnDef = table.getColumn(columnId)?.columnDef;
      const minSize = columnDef?.minSize ?? 48;
      const maxSize = columnDef?.maxSize ?? Number.MAX_SAFE_INTEGER;
      return Math.min(maxSize, Math.max(minSize, Math.round(width)));
    },
    [table],
  );

  const setColumnWidth = React.useCallback(
    (header: TanstackHeader<TData, unknown>, width: number) => {
      table.setColumnSizing((old) => ({
        ...old,
        [header.column.id]: clampColumnWidth(header.column.id, width),
      }));
    },
    [clampColumnWidth, table],
  );

  // fixed 表格布局会把「配置宽度总和小余表格宽度」的那部分空间摊给所有列，
  // 于是每列实际渲染都比配置宽。提交某一列的宽度会改变剩余空间并连带缩放
  // 其它列，造成「拖拽跑在指针前面」。先把每一列都钉到它当前实际渲染的宽度，
  // 消除剩余空间，此后拖拽就能与指针移动 1:1 对应。
  const freezeRenderedWidths = React.useCallback(
    (headerRow: Element | null): ColumnSizingState => {
      const frozen: ColumnSizingState = {};
      const cells =
        headerRow?.querySelectorAll<HTMLElement>("th[data-column-id]") ?? [];
      for (const cell of cells) {
        const columnId = cell.dataset.columnId;
        if (!columnId) continue;
        frozen[columnId] = clampColumnWidth(
          columnId,
          cell.getBoundingClientRect().width,
        );
      }
      return frozen;
    },
    [clampColumnWidth],
  );

  // 双击拖拽柄让该列自适应到内容宽度——这是所有表格/网格软件共享的约定。
  // 用它替代 column.resetSize：resetSize 会清掉已存宽度让 TanStack 退回它的
  // 通用默认 150，这个数和本表任何设计宽度都无关，于是「重置」反而有的列变宽
  // 有的列塌掉。
  //
  // fixed 布局不理会内容宽度，单元格又会截断自身文本，所以屏幕上没有任何东西
  // 报出内容真正想要的宽度。这里的测量做法是：临时解除这两条约束（tableLayout
  // 改 auto、单元格放开宽度），量完立刻在同一次任务里全部还原——浏览器只在
  // 还原之后统一绘制一次，中间那个临时布局永远不会被看到。
  const autoFitColumn = React.useCallback(
    (header: TanstackHeader<TData, unknown>) => {
      const container = scrollRef.current;
      const tableElement = container?.querySelector("table");
      if (!container || !tableElement) return;
      const cells = container.querySelectorAll<HTMLElement>(
        `[data-column-id="${CSS.escape(header.column.id)}"]`,
      );
      if (!cells.length) return;

      const previousLayout = tableElement.style.tableLayout;
      const previous = Array.from(cells, (cell) => ({
        cell,
        width: cell.style.width,
        maxWidth: cell.style.maxWidth,
        overflow: cell.style.overflow,
      }));

      tableElement.style.tableLayout = "auto";
      for (const cell of cells) {
        cell.style.width = "max-content";
        cell.style.maxWidth = "none";
        cell.style.overflow = "visible";
      }

      let widest = 0;
      for (const cell of cells) {
        widest = Math.max(widest, cell.getBoundingClientRect().width);
      }

      for (const entry of previous) {
        entry.cell.style.width = entry.width;
        entry.cell.style.maxWidth = entry.maxWidth;
        entry.cell.style.overflow = entry.overflow;
      }
      tableElement.style.tableLayout = previousLayout;

      if (widest > 0) setColumnWidth(header, widest);
    },
    [setColumnWidth],
  );

  /** 拖拽开始时在表头上绑「拖拽线宽度」，双击、右键等均由此进入 */
  const beginColumnResize = React.useCallback(
    (
      header: TanstackHeader<TData, unknown>,
      event: React.PointerEvent<HTMLDivElement>,
    ) => {
      if (!header.column.getCanResize()) return;

      event.preventDefault();
      event.stopPropagation();

      const startX = event.clientX;
      const handleEl = event.currentTarget;
      const { pointerId } = event;
      const headerCell = handleEl.closest("th");
      // 取「实际渲染宽」而不是 column.getSize()：拖拽必须从用户抓到的那条边
      // 继续走，而那条边正好在表格被撑开后实际所在的位置。
      const startWidth =
        headerCell?.getBoundingClientRect().width ?? header.column.getSize();

      setResizingColumnId(header.column.id);

      let frozen: ColumnSizingState | null = null;

      const handlePointerMove = (pointerEvent: PointerEvent) => {
        const delta = pointerEvent.clientX - startX;
        // 位移小于阈值时不提交任何宽度：单独按一下（不拖动）就不会把列钉在
        // 它被撑出来的宽度上。
        if (!frozen && Math.abs(delta) < RESIZE_DRAG_THRESHOLD) return;
        // 基线只在拖动开始的那一帧量一次，之后每帧都复用，保证下面宽度变化
        // 时基线不会跟着漂。
        frozen ??= freezeRenderedWidths(handleEl.closest("tr"));

        table.setColumnSizing((old) => ({
          ...old,
          ...frozen,
          [header.column.id]: clampColumnWidth(
            header.column.id,
            startWidth + delta,
          ),
        }));
      };

      const stopResize = () => {
        window.removeEventListener("pointermove", handlePointerMove);
        window.removeEventListener("pointerup", stopResize);
        window.removeEventListener("pointercancel", stopResize);
        window.removeEventListener("blur", stopResize);
        // 要在释放指针捕获前先解绑——释放本身会再次触发这个事件。
        handleEl.removeEventListener("lostpointercapture", stopResize);
        if (handleEl.hasPointerCapture?.(pointerId)) {
          handleEl.releasePointerCapture?.(pointerId);
        }
        setResizingColumnId(null);
      };

      window.addEventListener("pointermove", handlePointerMove);
      window.addEventListener("pointerup", stopResize);
      window.addEventListener("pointercancel", stopResize);
      // 拖出窗口外或拖拽中途切走应用时永远不会派发 pointerup。不加这两个
      // 兜底，手势会一直保持「武装」状态，等用户回来列还会继续追着指针跑。
      window.addEventListener("blur", stopResize);
      handleEl.addEventListener("lostpointercapture", stopResize);
      // 可选调用：jsdom 没有指针捕获。
      handleEl.setPointerCapture?.(pointerId);
    },
    [clampColumnWidth, freezeRenderedWidths, table],
  );

  /** 键盘微调列宽：左右方向键 ±8px，按住 Shift ±20px */
  const handleResizeKeyDown = React.useCallback(
    (
      header: TanstackHeader<TData, unknown>,
      event: React.KeyboardEvent<HTMLDivElement>,
    ) => {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;

      event.preventDefault();
      event.stopPropagation();

      const headerCell = event.currentTarget.closest("th");
      // 有显式宽度就读配置值；否则读实际渲染宽（fixed 布局下更接近视觉）
      const currentWidth = hasExplicitSize(header.column.id)
        ? header.column.getSize()
        : (headerCell?.getBoundingClientRect().width ??
          header.column.getSize());
      const direction = event.key === "ArrowRight" ? 1 : -1;
      const step = event.shiftKey ? 20 : 8;

      setColumnWidth(header, currentWidth + direction * step);
    },
    [hasExplicitSize, setColumnWidth],
  );

  // 把每一列的宽度一次性发布到 <table> 上。单元格引用这些 CSS 变量而不是
  // 各自调 column.getSize()，于是调整一列宽只更新一个元素的一条样式，
  // 而不是每个单元格各来一次。
  const leafColumns = table.getVisibleLeafColumns();
  const columnSizeVars = React.useMemo(() => {
    const vars: Record<string, string> = {};
    for (const column of leafColumns) {
      vars[columnSizeVar(column.id)] = `${column.getSize()}px`;
    }
    return vars;
    // column.getSize() 内部读的是 columnSizing，但本规则透过调用看不到这层
    // 依赖；getVisibleLeafColumns 只在列可见性与顺序变化时重算，若不把
    // columnSizing 列为依赖，宽度会冻结在挂载时。
    // eslint-disable-next-line react-hooks/exhaustive-deps -- getSize() reads columnSizing
  }, [columnSizing, leafColumns]);

  const scrollRef = React.useRef<HTMLDivElement | null>(null);

  // 驱动「固定列右侧拖影」的显示：只有内容滚到固定列底下时才有意义，所以
  // 静止时不亮。状态用布尔而不是偏移量：一次滚动往返只翻转两次，而不是每个
  // 滚动事件翻一次，重复时 React 会自动跳过重渲染。
  const [isScrolledHorizontally, setIsScrolledHorizontally] =
    React.useState(false);

  // 固定块右边界在滚动容器自己坐标系里的位置（拖影就钉在那里）
  const [pinnedEdge, setPinnedEdge] = React.useState<number | null>(null);

  React.useEffect(() => {
    const element = scrollRef.current;
    if (!element) return;
    const sync = () => {
      setIsScrolledHorizontally(element.scrollLeft > 0);
      const edge = element.querySelector("thead th[data-pinned-edge]");
      setPinnedEdge(
        edge
          ? edge.getBoundingClientRect().right -
              element.getBoundingClientRect().left
          : null,
      );
    };
    sync();
    element.addEventListener("scroll", sync, { passive: true });
    return () => element.removeEventListener("scroll", sync);
    // 列尺寸变化时也要重测，不能只等滚动事件：在表面已横向滚动的情况下调整
    // 固定列的宽度会挪动拖影所钉的边界，而之后不会有滚动事件来纠正它。
  }, [columnSizing]);

  const rows = table.getRowModel().rows;
  const getVirtualRowKey = React.useCallback(
    (index: number) => rows[index]?.id ?? index,
    [rows],
  );
  const rowVirtualizer = useVirtualizer({
    count: virtualizeRows ? rows.length : 0,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => virtualRowHeight,
    getItemKey: getVirtualRowKey,
    overscan: virtualOverscan,
    // 数据行并不等高：调用方用 renderRow 渲染的分组表头、列尾合计行都比数据行
    // 矮，固定估高会随这些行累积而偏离真实布局——可视窗口会落在它该显示的行
    // 之外，滚动条也会冲过底部。因此每个已挂载的行都上报自己的实际高度，
    // 估计值只覆盖从未上过屏的行。
    measureElement: (element) => element.getBoundingClientRect().height,
  });
  const virtualItems = rowVirtualizer.getVirtualItems();
  const firstVirtualItem = virtualItems[0];
  const lastVirtualItem = virtualItems[virtualItems.length - 1];
  const virtualPaddingTop = firstVirtualItem?.start ?? 0;
  const virtualPaddingBottom = lastVirtualItem
    ? rowVirtualizer.getTotalSize() - lastVirtualItem.end
    : 0;

  const bodyProps: DataTableBodyProps<TData> = {
    table,
    rows,
    emptyMessage,
    onRowClick,
    renderRow,
    hasExplicitSize,
    measureRow: rowVirtualizer.measureElement,
    virtualizeRows,
    virtualItems,
    virtualPaddingTop,
    virtualPaddingBottom,
  };

  return (
    <div
      className={cn("flex min-h-0 flex-1 flex-col", className)}
      {...props}
    >
      <div className="relative flex min-h-0 flex-1 flex-col">
      <div
        ref={scrollRef}
        className="flex min-h-0 flex-1 flex-col overflow-auto bg-background"
      >
        <table
          className="w-full table-fixed caption-bottom text-body"
          style={{
            minWidth: `${table.getTotalSize()}px`,
            ...columnSizeVars,
          }}
        >
          {/* 用不透明色而不是半透明 + 模糊：在 sticky 元素上做 backdrop-filter
            * 且底下有内容在滚，是已知的 Chromium 合成层故障（chromium#339841685）——
            * 每帧都要重算模糊层底下的内容，而虚拟化还在不断增删行，结果那条
            * 条带会闪黑。这里的 color-mix 合成结果与 bg-muted/30 是一致的，
            * 而且滚过的内容也没必要透过表头显示出来。 */}
          <TableHeader className="sticky top-0 z-10 bg-[color-mix(in_oklab,var(--muted)_30%,var(--background))]">
            {table.getHeaderGroups().map((headerGroup) => (
              <TableRow key={headerGroup.id} className="hover:bg-transparent">
                {headerGroup.headers.map((header) => {
                  const isPinned = header.column.getIsPinned();
                  const columnHasExplicitSize = hasExplicitSize(
                    header.column.id,
                  );
                  const headerLabel =
                    typeof header.column.columnDef.header === "string"
                      ? header.column.columnDef.header
                      : header.column.id;
                  return (
                    <TableHead
                      key={header.id}
                      colSpan={header.colSpan}
                      // 让拖拽能按 id 量每一列，而不是按位置把 <th> 和列配对。
                      data-column-id={header.column.id}
                      // 标记固定块右边界，供滚动拖影测量用。fixed 布局下实际
                      // 渲染宽与配置宽不同，所以边界必须从 DOM 读，不能相加。
                      data-pinned-edge={
                        isPinned === "left" &&
                        header.column.getIsLastColumn("left")
                          ? ""
                          : undefined
                      }
                      // 表头的「表格样式」覆写：更小、全大写、更宽字距、弱化色。
                      // shadcn 的 <TableHead> 默认是 text-body + text-foreground +
                      // font-medium，在这里显得太重。h-8（32px）比默认 h-10（40px）
                      // 更紧凑。
                      // overflow-hidden 兜住任何超出 column.size 的单元格内容；
                      // tooltip / 下拉 / 悬停卡都是传送到 body 的，不受影响。
                      // 固定列必须不透明：半透明背景会透出底下横向滚动的内容。
                      // 用 muted 与 background 的 color-mix 保持 muted/30 的视觉，
                      // 同时去掉 alpha。
                      className={cn(
                        "relative h-8 overflow-hidden border-r px-4 py-2 text-caption uppercase tracking-wider text-muted-foreground last:border-r-0",
                        isPinned &&
                          "bg-[color-mix(in_oklab,var(--muted)_30%,var(--background))]",
                      )}
                      style={getCellStyle(header.column, {
                        hasExplicitSize: columnHasExplicitSize,
                      })}
                    >
                      {header.isPlaceholder
                        ? null
                        : flexRender(
                            header.column.columnDef.header,
                            header.getContext(),
                          )}
                      {!header.isPlaceholder &&
                        header.column.getCanResize() && (
                          <div
                            role="separator"
                            aria-label={`Resize ${headerLabel} column`}
                            aria-orientation="vertical"
                            tabIndex={0}
                            className={cn(
                              // 高亮线精确落在该列自己的边框上（所以是
                              // -right-px：right-0 会落在边框内侧，看起来像
                              // 双重线）并给它重新着色。一条发丝线上要区分
                              // 三种状态：静止时是淡淡的网格线，悬停变半透明
                              // 品牌色，整个拖拽过程保持全品牌色——是变亮而
                              // 不是变淡，这样即使指针已经离开拖拽柄，正在被
                              // 拖动的那条边仍是屏幕上最明确的东西。
                              // 用 right-0 而不是负偏移：<th> 是 overflow-hidden
                              // 的，内容裁到内边距边缘，凡是挪到边框上的部分
                              // 根本不会被绘制。于是高亮条落在边框内侧，2px 宽
                              // 在细线旁仍清晰可辨。
                              "absolute top-0 right-0 h-full w-2 cursor-col-resize touch-none select-none outline-none",
                              "after:absolute after:inset-y-0 after:right-0 after:w-0.5 after:bg-transparent after:transition-colors after:duration-100",
                              "hover:after:bg-brand/60 focus-visible:after:bg-brand/60",
                              resizingColumnId === header.column.id &&
                                "after:bg-brand after:transition-none",
                            )}
                            onPointerDown={(event) =>
                              beginColumnResize(header, event)
                            }
                            onDoubleClick={(event) => {
                              event.preventDefault();
                              event.stopPropagation();
                              autoFitColumn(header);
                            }}
                            onKeyDown={(event) =>
                              handleResizeKeyDown(header, event)
                            }
                          />
                        )}
                    </TableHead>
                  );
                })}
              </TableRow>
            ))}
          </TableHeader>
          {/* 宽度通过上面那些自定义属性到达 body，所以拖列期间 body 完全不用
            * 重渲染：换用记忆化的那份拷贝，把调宽全交给浏览器去做。 */}
          {resizingColumnId !== null ? (
            <MemoizedDataTableBody {...bodyProps} />
          ) : (
            <DataTableBody {...bodyProps} />
          )}
          {footer}
        </table>
      </div>
      {/* 拖影画在固定块外侧、盖住滑动中的内容，而不是画在固定块内部。固定列
        * 与其余列之间的那条分隔边框是常驻的，说明边界在哪；这个拖影说的是
        * 「此刻有东西正从下面滑过」，所以只在滚动时才出现。给固定格加内嵌
        * box-shadow 只能加深那一格自己的边——立体感应该落在从底下滑过的内容上。 */}
      {isScrolledHorizontally && pinnedEdge !== null && (
        <div
          aria-hidden
          data-slot="data-table-pinned-shadow"
          className="pointer-events-none absolute inset-y-0 w-3"
          style={{
            left: `${pinnedEdge}px`,
            background:
              "linear-gradient(to right, color-mix(in oklab, var(--foreground) 7%, transparent), transparent)",
          }}
        />
      )}
      </div>
      {/* 只在列拖拽期间存在的一个铺满视口的层。它持有光标：这样后代那些自带
        * 光标的元素（行是 cursor-pointer、单元格有文本光标）都压不过它；它还让
        * 底下的文字在拖拽期间不可选中。直接设在 document.body 上两头都赢不了，
        * 因为后代自己的光标会胜过继承来的。指针事件仍能到达手势处理：监听在
        * window，而这层的冒泡事件照样能冒上去。传送到 body，以免某个带
        * transform / filter / contain 的祖先把 fixed 变成「相对那个祖先」。 */}
      {resizingColumnId !== null &&
        typeof document !== "undefined" &&
        createPortal(
          <div
            aria-hidden
            data-slot="data-table-resize-overlay"
            className="fixed inset-0 z-50 cursor-col-resize touch-none select-none"
          />,
          document.body,
        )}
      {actionBar &&
        table.getFilteredSelectedRowModel().rows.length > 0 &&
        actionBar}
    </div>
  );
}

/** DataTableBody 的 props 定义（由 DataTable 组好传入） */
interface DataTableBodyProps<TData> {
  table: TanstackTable<TData>;
  rows: Row<TData>[];
  emptyMessage: React.ReactNode;
  onRowClick?: (row: Row<TData>, event: React.MouseEvent) => void;
  renderRow?: (row: Row<TData>) => React.ReactNode;
  hasExplicitSize: (columnId: string) => boolean;
  // 虚拟滚动器的测量 ref；数据行通过它上报自己的真实高度。
  measureRow: (element: HTMLElement | null) => void;
  virtualizeRows: boolean;
  virtualItems: VirtualItem[];
  virtualPaddingTop: number;
  virtualPaddingBottom: number;
}

/** 表格的 <tbody>：渲染数据行（或虚拟行窗口），空表时渲染占位文案 */
function DataTableBody<TData>({
  table,
  rows,
  emptyMessage,
  onRowClick,
  renderRow,
  hasExplicitSize,
  measureRow,
  virtualizeRows,
  virtualItems,
  virtualPaddingTop,
  virtualPaddingBottom,
}: DataTableBodyProps<TData>) {
  const renderDataRow = (row: Row<TData>, index?: number) => {
    // 虚拟滚动器靠 `data-index` 读元素自身高度，所以行要打上标记并拿到测量
    // ref。renderRow 返回调用方自建的 <tr>；克隆它来接入这两样，调用方就不用
    // 自己去穿线。
    const measured =
      index === undefined
        ? {}
        : { "data-index": index, ref: measureRow };

    const customRow = renderRow?.(row);
    if (customRow != null) {
      return React.isValidElement(customRow) && index !== undefined
        ? React.cloneElement(
            customRow as React.ReactElement<Record<string, unknown>>,
            { key: row.id, ...measured },
          )
        : <React.Fragment key={row.id}>{customRow}</React.Fragment>;
    }
    return (
      <TableRow
        key={row.id}
        {...measured}
        data-state={row.getIsSelected() && "selected"}
        onClick={
          onRowClick
            ? (e) => {
                if (e.defaultPrevented) return;
                onRowClick(row, e);
              }
            : undefined
        }
        // 中键点击永远不产生 `click` 事件，这里把它透传进同一个回调（只放行
        // button === 1；button 2 是右键菜单，不管）。
        onAuxClick={
          onRowClick
            ? (e) => {
                if (e.defaultPrevented || e.button !== 1) return;
                e.preventDefault();
                onRowClick(row, e);
              }
            : undefined
        }
        // `group` 让固定格能通过 group-hover 跟随行的悬停态（固定格自己的底色
        // 写在 className 而非行上，才能足够不透明地盖住底下滚动的内容）。
        className={cn("group", onRowClick && "cursor-pointer")}
      >
        {row.getVisibleCells().map((cell) => {
          const isPinned = cell.column.getIsPinned();
          const columnHasExplicitSize = hasExplicitSize(cell.column.id);
          return (
            <TableCell
              key={cell.id}
              data-column-id={cell.column.id}
              // 统一 px-4，让单元格内容与外面工具栏的 px-4 对齐。窄的尾部列
              // （箭头/操作）声明的宽度要足够容纳图标加内边距。
              // 固定格需要不透明底色 + group-hover，才能盖住底下滚动的内容
              // 并跟随行的悬停态。
              className={cn(
                "overflow-hidden border-r px-4 py-2 last:border-r-0",
                isPinned &&
                  "bg-background group-hover:bg-[color-mix(in_oklab,var(--muted)_50%,var(--background))]",
              )}
              style={getCellStyle(cell.column, {
                hasExplicitSize: columnHasExplicitSize,
              })}
            >
              {flexRender(cell.column.columnDef.cell, cell.getContext())}
            </TableCell>
          );
        })}
      </TableRow>
    );
  };

  const renderVirtualSpacer = (position: "top" | "bottom", height: number) =>
    height > 0 ? (
      <TableRow
        key={`virtual-spacer-${position}`}
        aria-hidden
        className="pointer-events-none border-0 hover:bg-transparent"
      >
        <TableCell
          colSpan={table.getVisibleLeafColumns().length}
          className="p-0"
          style={{ height: `${height}px` }}
        />
      </TableRow>
    ) : null;

  return (
    <TableBody>
      {rows.length ? (
        virtualizeRows ? (
          <>
            {renderVirtualSpacer("top", virtualPaddingTop)}
            {virtualItems.map((virtualItem) => {
              const row = rows[virtualItem.index];
              return row ? renderDataRow(row, virtualItem.index) : null;
            })}
            {renderVirtualSpacer("bottom", virtualPaddingBottom)}
          </>
        ) : (
          rows.map(renderDataRow)
        )
      ) : (
        <TableRow>
          <TableCell
            colSpan={table.getAllColumns().length}
            className="h-24 text-center text-muted-foreground"
          >
            {emptyMessage}
          </TableCell>
        </TableRow>
      )}
    </TableBody>
  );
}

// 在列拖拽期间用 MemoizedDataTableBody 顶替 DataTableBody。比较时只看行数据：
// 拖拽期间宽度是以自定义属性传播的，body 里其它内容不可能变化，所以整段跳过
// 渲染能让每帧几百个单元格不再占用主线程。React.memo 会抹掉泛型签名，
// 这个 as 断言把它还原成原本的类型。
const MemoizedDataTableBody = React.memo(
  DataTableBody,
  (prev, next) => prev.table.options.data === next.table.options.data,
) as typeof DataTableBody;
