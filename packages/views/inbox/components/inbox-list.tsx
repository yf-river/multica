"use client";

// 可滚动、虚拟化的收件箱通知列表（react-virtuoso）。同时服务桌面端与移动端布局，
// 滚动容器只有一个；支持方向键选择、归档入口与滚动位置还原。
import {
  useCallback,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";
import { Archive, ChevronRight, Inbox } from "lucide-react";
import { isEditableShortcutTarget } from "@multica/core/shortcuts";
import { isImeComposing } from "@multica/core/utils";
import type { InboxItem } from "@multica/core/types";
import type { InboxView } from "./inbox-view";
import { InboxListItem } from "./inbox-list-item";
import { VirtuosoSeed, VIRTUOSO_SEED_COUNT } from "../../common/virtuoso-seed";
import { useRestoredScrollOffset, useRestoredScrollRef } from "../../platform";
import { useT } from "../../i18n";

// 只用于测量（类似看板的卡片估算）：seed 的尾部占位与 Virtuoso 的
// defaultItemHeight 共用这个值，保证滚动容器首帧高度就是真实高度、还原的偏移
// 能停住。一行是两行文本（正文 + caption）再加 py-2.5。
const INBOX_ROW_ESTIMATED_HEIGHT = 58;

/**
 * 可滚动、虚拟化的收件箱通知列表。
 *
 * 滚动容器归本组件所有，因此移动端与桌面端布局渲染的是同一个滚动手感。行用
 * react-virtuoso 虚拟化，只挂载可视窗口（加一点 overscan）——通知列表可能很长，
 * 每行还带着头像 + 悬停卡，全量挂载会让切标签页的那次提交明显膨胀（MUL-4474）。
 *
 * 虚拟化只改变一件事：屏幕外的行在不在 DOM 里。选中、悬停、归档、滚动语义都
 * 不变——行组件与回调和非虚拟化列表完全一致。`customScrollParent` 让 Virtuoso
 * 读写既有的 `overflow-y-auto` 元素（与任务详情时间线同款做法），滚动位置行为
 * 与之前一致。
 *
 * 已知的虚拟化取舍：键盘 Tab 只能到达当前已挂载的行；纯键盘用户需要滚动才能把
 * 屏外行带进 Tab 序列。下面的方向键导航覆盖屏外行，因为它走的是数据而不是 DOM。
 *
 * @param items 收件箱条目（已去重/已过滤）
 * @param view 当前列表视图
 * @param selectedKey 当前选中的条目 key
 * @param archivedCount 归档入口要展示的去重条数（主视图读）
 * @param onSelect 选中条目回调
 * @param onAction 归档/取消归档回调
 * @param onOpenArchived 点归档入口的回调
 * @param emptyLabel 空列表文案
 * @param emptyAction 空列表时的可选操作
 */
export function InboxList({
  items,
  view,
  selectedKey,
  archivedCount,
  onSelect,
  onAction,
  onOpenArchived,
  emptyLabel,
  emptyAction,
}: {
  items: InboxItem[];
  view: InboxView;
  selectedKey: string;
  // 去重后的归档条目数。只有主视图读它，用来给「进归档」的入口配数字；
  // 数字为零时入口隐藏。
  archivedCount: number;
  onSelect: (item: InboxItem) => void;
  onAction: (id: string) => void;
  onOpenArchived: () => void;
  emptyLabel?: string;
  emptyAction?: ReactNode;
}) {
  const { t } = useT("inbox");
  // Virtuoso 的 `customScrollParent` 要的是真实 HTMLElement，不是 ref 对象。
  // 用回调 ref 写进 state：元素挂载后交过来、触发重渲染，让 Virtuoso 能挂上去。
  const [scrollEl, setScrollEl] = useState<HTMLDivElement | null>(null);
  // 拉取式滚动还原（MUL-4741）：ref 一挂载就把存的偏移赋上去（seed 的估算占位
  // 让容器首帧高度真实，赋值能在绘制前落定），同时把这个偏移喂给 Virtuoso 当
  // 初始位置。
  const restoredScrollTop = useRestoredScrollOffset("list");
  const restoreScrollRef = useRestoredScrollRef("list");
  const attachScrollEl = useCallback(
    (el: HTMLDivElement | null) => {
      setScrollEl(el);
      restoreScrollRef(el);
    },
    [restoreScrollRef],
  );
  const virtuosoRef = useRef<VirtuosoHandle>(null);
  const isArchivedView = view === "archived";

  // 列表的键盘焦点放在滚动容器上而不是行上：虚拟化会在用户点的那行滚出屏幕的
  // 瞬间卸载它，焦点回落到 <body> 会静默吞掉下一次方向键。`preventScroll` 是因为
  // 聚焦一个盒子会把所有可滚动祖先都滚到能露出它，桌面端这样会把外壳顶得到处跑
  // (#3929)。
  const focusList = useCallback(() => {
    scrollEl?.focus({ preventScroll: true });
  }, [scrollEl]);

  const selectItem = useCallback(
    (item: InboxItem) => {
      // Safari 不会聚焦被点击的行控件，所以必须显式聚焦容器，否则点完行方向键
      // 会一直是死的。
      focusList();
      onSelect(item);
    },
    [focusList, onSelect],
  );

  // 方向键用来移动选中而不是滚动容器——所有邮件风列表都这样（MUL-5622）。
  // 绑定在滚动容器而非 document 上，保证只有焦点在列表内时才触发：在任务详情
  // 里按 Down 不应该把读者脚下的行悄悄换掉。
  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    // 已经消费过方向键的菜单/编辑器/文本框拥有它；IME 候选列表组词中也是。
    if (event.defaultPrevented || isImeComposing(event)) return;
    if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return;
    if (isEditableShortcutTarget(event.target)) return;

    // 即使停在列表两端也要吞掉这个键：落到原生滚动会把视口从选中行移走。
    event.preventDefault();
    focusList();

    const current = items.findIndex(
      (item) => (item.issue_id ?? item.id) === selectedKey,
    );
    const step = event.key === "ArrowDown" ? 1 : -1;
    // 还没选中：Down 从顶部进入列表，Up 从底部进入。
    const nextIndex =
      current < 0
        ? step === 1
          ? 0
          : items.length - 1
        : Math.min(Math.max(current + step, 0), items.length - 1);
    if (nextIndex === current) return;
    const nextItem = items[nextIndex];
    if (!nextItem) return;

    // 用 Virtuoso 自己的 scrollIntoView，绝不用 DOM 元素的：目标行可能根本没挂载，
    // 而原生调用还会连滚动祖先一起滚。目标行已完全可见时它是 no-op，所以在视口
    // 内移动选中不会滚动列表。
    virtuosoRef.current?.scrollIntoView({ index: nextIndex });
    onSelect(nextItem);
  };

  // 进归档的入口放在最后一行下面、随列表一起滚（和聊天的入口同一位置）。
  // Virtuoso 通过 `components.Footer` 挂载它，并在该 prop 身份变化时替换组件——
  // 所以元素和包它的 Footer 都要 memo。否则父组件每次渲染都会重挂载入口，
  // 点击中途 hover/focus 会掉。
  const archivedEntry = useMemo(
    () =>
      !isArchivedView && archivedCount > 0 ? (
        <button
          type="button"
          onClick={onOpenArchived}
          className="mt-1 flex h-10 w-full items-center gap-2 rounded-md px-2 text-left text-caption text-muted-foreground outline-none transition-colors hover:bg-accent/50 hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring"
        >
          <span className="flex size-7 shrink-0 items-center justify-center">
            <Archive className="size-4" />
          </span>
          <span className="min-w-0 flex-1 truncate font-medium">
            {t(($) => $.list.archived_title)}
          </span>
          <span className="shrink-0 tabular-nums text-muted-foreground">
            {archivedCount}
          </span>
          <ChevronRight className="size-4 shrink-0 text-faint-foreground" />
        </button>
      ) : null,
    [isArchivedView, archivedCount, onOpenArchived, t],
  );

  const Footer = useCallback(() => archivedEntry, [archivedEntry]);

  if (items.length === 0) {
    return (
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
          <Inbox className="mb-3 h-8 w-8 text-faint-foreground" />
          <p className="text-body">
            {emptyLabel ??
              (isArchivedView
                ? t(($) => $.list.archived_empty)
                : t(($) => $.list.empty))}
          </p>
          {emptyAction && <div className="mt-3">{emptyAction}</div>}
        </div>
        {/* 主列表为空时仍给归档入口——人恰恰是在这时候来找自己收起来的东西。 */}
        {archivedEntry && <div className="px-2">{archivedEntry}</div>}
      </div>
    );
  }

  const computeItemKey = (_index: number, item: InboxItem) => item.id;
  const itemContent = (_index: number, item: InboxItem) => (
    <InboxListItem
      item={item}
      view={view}
      isSelected={(item.issue_id ?? item.id) === selectedKey}
      onClick={() => selectItem(item)}
      onAction={() => onAction(item.id)}
    />
  );

  // 回调 ref 还没把滚动元素交过来时（重挂载后的首帧），先用种子行渲染一段有界
  // 的真实行，避免列表白屏；一旦就绪就挂 Virtuoso 并配一致的 `initialItemCount`，
  // 让测量帧保住这些行（MUL-4750）。
  return (
    <div
      ref={attachScrollEl}
      data-tab-scroll-root="list"
      // 只允许程序化聚焦：行自己已经带 Tab 停靠点，容器再可 Tab 就是多一个冗余。
      tabIndex={-1}
      onKeyDown={handleKeyDown}
      className="flex-1 min-h-0 overflow-y-auto outline-none"
    >
      <div className="px-2 py-1">
        {scrollEl ? (
          <Virtuoso
            ref={virtuosoRef}
            customScrollParent={scrollEl}
            data={items}
            computeItemKey={computeItemKey}
            initialScrollTop={restoredScrollTop}
            initialItemCount={Math.min(items.length, VIRTUOSO_SEED_COUNT)}
            defaultItemHeight={INBOX_ROW_ESTIMATED_HEIGHT}
            increaseViewportBy={{ top: 400, bottom: 400 }}
            itemContent={itemContent}
            components={{ Footer }}
          />
        ) : (
          <>
            <VirtuosoSeed
              data={items}
              itemContent={itemContent}
              computeItemKey={computeItemKey}
              estimatedItemHeight={INBOX_ROW_ESTIMATED_HEIGHT}
            />
            {/* 种子帧只渲染有界的一段，所以归档入口会落到列表中间而不是最后一行
                之后——只有种子已经是整份列表时才展示它。 */}
            {items.length <= VIRTUOSO_SEED_COUNT && archivedEntry}
          </>
        )}
      </div>
    </div>
  );
}
