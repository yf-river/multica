"use client";

// 本文件：PropertyPicker —— 各属性选择器的通用 Popover 外壳。
// 内置可选的搜索框、键盘上下移动高亮、Enter 确认；子组件只需提供
// PickerItem / PickerSection / PickerEmpty 等列表内容即可。
//
// 键盘语义统一约定（所有具体选择器共用）：
//   - "空值行"（如"无项目/未分配"）固定钉在首行，但不属于搜索结果——
//     输入查询后按 Enter 应提交首个真实匹配，绝不把字段清空。
//   - 高亮跟随键盘移动；选中类样式直接写 DOM（避免整树重渲染）。
//   - 弹出层在 open->closed 转换时清空搜索状态（见下方 effect 说明）。

import { useState, useCallback, useRef, useEffect } from "react";
import { Check } from "lucide-react";
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from "@multica/ui/components/ui/popover";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { isImeComposing } from "@multica/core/utils";
import { useT } from "../../../i18n";

const HIGHLIGHT_CLASS = "bg-accent";
const ITEM_SELECTOR = "button[data-picker-item]:not(:disabled)";
/**
 * 标记"固定的空值行"（"无项目"、"未分配"……）。该行虽然钉在列表首行，
 * 但它不是搜索结果——输入查询后按 Enter 必须提交首个真实匹配，绝不回到清空字段。
 */
const EMPTY_ITEM_ATTR = "data-picker-empty";

const isEmptyItem = (el: HTMLButtonElement | undefined) =>
  el?.hasAttribute(EMPTY_ITEM_ATTR) === true;

/**
 * 选择器 Popover 触发器的默认样式类。与 `DeferredPopup` 调用点里那个
 * 延迟预挂载的仿触发器共享，保证首次交互时的替换在像素级一致。
 */
export const PICKER_TRIGGER_CLASS =
  "flex items-center gap-1.5 cursor-pointer rounded px-1 -mx-1 hover:bg-accent/30 transition-colors overflow-hidden";

// ---------------------------------------------------------------------------
// PropertyPicker —— 可选搜索的通用 Popover 外壳
// ---------------------------------------------------------------------------

/**
 * 通用属性选择器外壳：渲染 trigger + 弹层，弹层内可选搜索框，
 * 并统一负责方向键高亮、Enter 提交、IME 输入法放行与状态复位。
 */
export function PropertyPicker({
  open,
  onOpenChange,
  trigger,
  triggerRender,
  width = "w-48",
  align = "end",
  side = "bottom",
  searchable = false,
  searchPlaceholder,
  onSearchChange,
  header,
  tooltip,
  children,
  footer,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  trigger: React.ReactNode;
  triggerRender?: React.ReactElement;
  width?: string;
  align?: "start" | "center" | "end";
  side?: React.ComponentProps<typeof PopoverContent>["side"];
  searchable?: boolean;
  searchPlaceholder?: string | undefined;
  onSearchChange?: (query: string) => void;
  /** 自定义吸顶页头，渲染在可滚动列表上方。用于过滤开关、搜索输入框或任何
   *  需要在列表滚动时保持可见的 UI。同时提供 `searchable` 时，内置搜索框会
   *  渲染在该页头正上方。 */
  header?: React.ReactNode;
  /** 弹层关闭时悬停触发器展示的设计系统 Tooltip。弹层打开时自动抑制
   *  （否则 Tooltip 与 Popover 会叠在同一个锚点上）。 */
  tooltip?: React.ReactNode;
  children: React.ReactNode;
  /**
   * 渲染在 listbox 下方的可选页脚。与作为 children 的条目不同，页脚
   * *不参与*方向键导航——适合放"新建…/管理…"这类不应被当作可选列表项的动作。
   */
  footer?: React.ReactNode;
}) {
  const { t } = useT("issues");
  const placeholder = searchPlaceholder ?? t(($) => $.filters.placeholder);
  const filterAria = t(($) => $.pickers.filter_options_aria);
  const [query, setQuery] = useState("");
  const [highlightedIndex, setHighlightedIndex] = useState(-1);
  const [tooltipHover, setTooltipHover] = useState(false);
  const listRef = useRef<HTMLDivElement>(null);
  // 仅当触发器被悬停且弹层关闭时才展示 Tooltip——避免弹层刚因点击打开时，
  // Tooltip 还飘在它旁边/上面的尴尬状态。
  const tooltipOpen = !!tooltip && tooltipHover && !open;

  const getItems = useCallback(() => {
    if (!listRef.current) return [];
    return Array.from(
      listRef.current.querySelectorAll<HTMLButtonElement>(ITEM_SELECTOR),
    );
  }, []);

  // 打字会重新高亮首个*真实*匹配项。索引不能在输入框 onChange 里即时算出——
  // 此时过滤后的列表尚未渲染——因此按键只立起一个标志位，由本 effect 对着
  // 新鲜 DOM 求解。靠标志位守卫意味着方向键仍可自由走回空值行：只有按键
  // 才会把高亮从它身上移开。
  const pendingSearchHighlight = useRef(false);
  useEffect(() => {
    if (!pendingSearchHighlight.current) return;
    pendingSearchHighlight.current = false;
    const items = getItems();
    // 查询无匹配时（列表只剩空值行）结果是 -1，Enter 因而保持无操作，
    // 而不会把字段清空。
    setHighlightedIndex(items.findIndex((item) => !isEmptyItem(item)));
  }, [children, getItems]);

  // 高亮索引变化时，直接改 DOM 上的类来应用/移除高亮
  useEffect(() => {
    const items = getItems();
    for (const item of items) {
      item.classList.remove(HIGHLIGHT_CLASS);
    }
    if (highlightedIndex >= 0 && highlightedIndex < items.length) {
      items[highlightedIndex]?.classList.add(HIGHLIGHT_CLASS);
    }
  }, [highlightedIndex, getItems, children]); // children 变化（过滤列表刷新）时重跑

  // 在 open->closed 转换时复位搜索状态，而不是放进 open-change 处理器里。
  // 每个选择器在选择后都调用自己的 `setOpen(false)` 来关闭——那会直接翻转
  // 本 `open` prop，从不经过 Popover 的 `onOpenChange`——所以只靠处理器复位
  // 会让旧的查询词（以及过滤后的列表）残留到下次打开时。
  const wasOpen = useRef(open);
  useEffect(() => {
    if (wasOpen.current && !open) {
      setQuery("");
      setHighlightedIndex(-1);
      onSearchChange?.("");
    }
    wasOpen.current = open;
  }, [open, onSearchChange]);

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      // IME 正在组词——Enter/方向键属于输入法（Enter 确认候选、方向键切换
      // 候选）。不要劫持它们。
      if (isImeComposing(e)) return;
      const items = getItems();
      if (items.length === 0) return;

      if (e.key === "ArrowDown") {
        e.preventDefault();
        setHighlightedIndex((prev) => {
          const next = prev < items.length - 1 ? prev + 1 : 0;
          items[next]?.scrollIntoView({ block: "nearest" });
          return next;
        });
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        setHighlightedIndex((prev) => {
          const next = prev > 0 ? prev - 1 : items.length - 1;
          items[next]?.scrollIntoView({ block: "nearest" });
          return next;
        });
      } else if (e.key === "Enter") {
        e.preventDefault();
        if (highlightedIndex >= 0 && highlightedIndex < items.length) {
          items[highlightedIndex]?.click();
        } else if (items.length === 1 && !isEmptyItem(items[0])) {
          // 唯一结果时自动选中。空值行被排除：查询无匹配时它会是唯一一项，
          // 此时若允许 Enter 会清空用户正在搜索的字段。
          items[0]?.click();
        }
      }
    },
    [getItems, highlightedIndex],
  );

  const popoverTrigger = (
    <PopoverTrigger
      className={triggerRender ? undefined : PICKER_TRIGGER_CLASS}
      render={triggerRender}
    >
      {trigger}
    </PopoverTrigger>
  );

  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      {tooltip ? (
        <Tooltip open={tooltipOpen} onOpenChange={setTooltipHover}>
          <TooltipTrigger render={popoverTrigger} />
          <TooltipContent side="top">{tooltip}</TooltipContent>
        </Tooltip>
      ) : (
        popoverTrigger
      )}
      <PopoverContent align={align} side={side} className={`${width} gap-0 p-0`}>
        {searchable && (
          <div className="px-2 py-1.5 border-b">
            <input
              type="text"
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                pendingSearchHighlight.current = true;
                onSearchChange?.(e.target.value);
              }}
              onKeyDown={handleKeyDown}
              placeholder={placeholder}
              aria-label={filterAria}
              className="w-full bg-transparent text-body placeholder:text-muted-foreground outline-none"
            />
          </div>
        )}
        {header && <div className="border-b">{header}</div>}
        <div ref={listRef} className="p-1 max-h-72 overflow-y-auto">{children}</div>
        {footer && <div className="border-t p-1">{footer}</div>}
      </PopoverContent>
    </Popover>
  );
}

// ---------------------------------------------------------------------------
// PickerItem —— 单条可选行
// ---------------------------------------------------------------------------

/**
 * 单条可选行。渲染一个真实 <button>，具备选中对勾、可选禁用、可选 Tooltip。
 */
export function PickerItem({
  selected,
  disabled,
  onClick,
  hoverClassName,
  tooltip,
  emptyValue = false,
  children,
}: {
  selected: boolean;
  disabled?: boolean;
  onClick: () => void;
  hoverClassName?: string;
  /**
   * 把本行标记为字段的固定空值（"无项目"、"未分配"、"无阶段"）。这类行会
   * 钉在首行供一眼定位，但不参与"搜索结果"的键盘默认项——这样输入查询后按
   * Enter 提交的是首个真实匹配而不是把字段清空。方向键仍可到达它。请只在
   * 写入 null/undefined 的行上设置；不要把恰好读作"无"的真实枚举成员
   * （如 issue 优先级）算进来，那是一个与其他值平等的值。
   */
  emptyValue?: boolean;
  /** 行级设计系统 Tooltip——适合内容被截断需展示完整串，或行上带有放不进
   *  单行的元数据时。用真正的 Tooltip 组件包裹（200ms 延迟、带样式），
   *  而非原生 `title` 属性。 */
  tooltip?: React.ReactNode;
  children: React.ReactNode;
}) {
  const button = (
    <button
      type="button"
      data-picker-item
      {...(emptyValue ? { [EMPTY_ITEM_ATTR]: "" } : {})}
      disabled={disabled}
      onClick={onClick}
      className={`flex w-full items-center gap-3 rounded-md px-2 py-1.5 text-left text-body ${disabled ? "opacity-50 cursor-not-allowed" : hoverClassName ?? "hover:bg-accent"} transition-colors`}
    >
      {/* min-w-0 让超长子内容（如被截断的标签名）在 flex 行内可收缩，
          而不会把右侧选中对勾挤出屏幕。对勾列始终预留其 14px 槽位
          （选中可见、未选中不可见），保证未选中行与选中行对齐，
          眼睛不必追着抖动的右边缘跑。 */}
      <span className="flex min-w-0 flex-1 items-center gap-2">{children}</span>
      <Check
        className={`h-3.5 w-3.5 shrink-0 text-muted-foreground ${
          selected ? "" : "invisible"
        }`}
      />
    </button>
  );

  if (!tooltip) return button;

  return (
    <Tooltip>
      <TooltipTrigger render={button} />
      <TooltipContent side="top">{tooltip}</TooltipContent>
    </Tooltip>
  );
}

// ---------------------------------------------------------------------------
// PickerSection —— 分组标题
// ---------------------------------------------------------------------------

/**
 * 分组标题 + 组内条目。用于把成员/Agent/小队等类别在列表里分节展示。
 */
export function PickerSection({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <div className="px-2 pt-2 pb-1 text-caption font-medium text-muted-foreground uppercase tracking-wider">
        {label}
      </div>
      {children}
    </div>
  );
}

// ---------------------------------------------------------------------------
// PickerEmpty —— 无结果占位
// ---------------------------------------------------------------------------

/**
 * "无匹配结果"的空态占位。
 */
export function PickerEmpty() {
  const { t } = useT("issues");
  return (
    <div className="px-2 py-3 text-center text-body text-muted-foreground">
      {t(($) => $.pickers.no_results)}
    </div>
  );
}
