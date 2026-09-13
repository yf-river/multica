"use client";

// 收件箱行的右键菜单：整张列表共享一个菜单（而不是每行一个 ContextMenu 根），
// 行只把「点了哪行 + 光标位置」上报上来，由这个单例在光标处弹出菜单；同时
// 把同一组操作回传给行内的紧凑菜单。
import {
  Fragment,
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type MouseEvent,
  type ReactNode,
} from "react";
import type { InboxItem } from "@multica/core/types";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
} from "@multica/ui/components/ui/context-menu";
import type { InboxView } from "./inbox-view";
import { useInboxItemActions, type InboxRowActions } from "./inbox-item-actions";

/**
 * 收件箱行的右键操作菜单。
 *
 * 整张列表共用一个菜单，而不是每行都挂一个 Base UI ContextMenu 根——这与
 * `IssueContextMenuProvider` 的形状一致，理由也一样。任何时候只有一个菜单会打开，
 * N 个根只是 N 倍的机制开销；而且列表是虚拟化的，行滚出视口时整行会被卸载，
 * 每行一个根会连它打开的菜单一起被拆掉。
 *
 * 行是委派的：行只把 (item, 光标位置) 上报到这个单例，单例用虚拟锚点元素把
 * 菜单定位在光标处。
 *
 * Provider 还会把同一组操作经 `useInboxRowActions` 传回去，让行自己的紧凑菜单
 * ——右键与悬停的触屏替代品——正好提供这些操作。
 */

interface ActiveMenu {
  item: InboxItem;
  position: { x: number; y: number };
}

type OpenInboxContextMenu = (item: InboxItem, event: MouseEvent) => void;

interface InboxContextMenuValue {
  open: OpenInboxContextMenu;
  actions: InboxRowActions;
}

const InboxContextMenuContext = createContext<InboxContextMenuValue | null>(null);

/**
 * 收件箱行的共享右键菜单 Provider：把「打开菜单」的函数和行操作集放进 context，
 * 并挂载唯一的菜单单例。
 *
 * @param view 当前列表视图
 * @param actions 行级操作集
 * @param children 行列表等子内容
 */
export function InboxContextMenuProvider({
  view,
  actions,
  children,
}: {
  view: InboxView;
  actions: InboxRowActions;
  children: ReactNode;
}) {
  const [active, setActive] = useState<ActiveMenu | null>(null);
  const [open, setOpen] = useState(false);
  // 用户右键的那行。行用 `data-popup-open` 来表达「菜单正为我开着」；这里用
  // 命令式设置，避免每次开关都重渲染每一行。
  const triggerElRef = useRef<HTMLElement | null>(null);

  const openMenu = useCallback<OpenInboxContextMenu>((item, event) => {
    event.preventDefault();
    triggerElRef.current?.removeAttribute("data-popup-open");
    const el = event.currentTarget as HTMLElement;
    el.setAttribute("data-popup-open", "");
    triggerElRef.current = el;
    setActive({ item, position: { x: event.clientX, y: event.clientY } });
    setOpen(true);
  }, []);

  const handleOpenChange = useCallback((v: boolean) => {
    if (!v) {
      triggerElRef.current?.removeAttribute("data-popup-open");
      triggerElRef.current = null;
    }
    setOpen(v);
  }, []);

  const value = useMemo<InboxContextMenuValue>(
    () => ({ open: openMenu, actions }),
    [openMenu, actions],
  );

  return (
    <InboxContextMenuContext.Provider value={value}>
      {children}
      {/* 首次使用时才挂载，之后保持挂载——弹层本体在关闭时是自己卸载的。 */}
      {active && (
        <InboxContextMenuSingleton
          item={active.item}
          position={active.position}
          view={view}
          actions={actions}
          open={open}
          onOpenChange={handleOpenChange}
        />
      )}
    </InboxContextMenuContext.Provider>
  );
}

/**
 * 为某行打开列表共享的右键菜单。上面没有 Provider 时返回 `null`——该行因此
 * 只是没有右键菜单、而不是崩溃，这让 `InboxListItem` 可以单独渲染。
 */
export function useInboxContextMenu(): OpenInboxContextMenu | null {
  return useContext(InboxContextMenuContext)?.open ?? null;
}

/**
 * 列表的行操作集，供行自己的紧凑菜单使用。没有 Provider 时为 `null`，
 * 与 `useInboxContextMenu` 同样优雅降级。
 */
export function useInboxRowActions(): InboxRowActions | null {
  return useContext(InboxContextMenuContext)?.actions ?? null;
}

function InboxContextMenuSingleton({
  item,
  position,
  view,
  actions,
  open,
  onOpenChange,
}: ActiveMenu & {
  view: InboxView;
  actions: InboxRowActions;
  open: boolean;
  onOpenChange: (v: boolean) => void;
}) {
  const groups = useInboxItemActions(item, view, actions);

  // 一个「零尺寸」的虚拟锚点，放在右键位置，让菜单开在光标处而不是行的左上角。
  const anchor = useMemo(
    () => ({
      getBoundingClientRect: () =>
        DOMRect.fromRect({ x: position.x, y: position.y, width: 0, height: 0 }),
    }),
    [position.x, position.y],
  );

  return (
    <ContextMenu open={open} onOpenChange={onOpenChange}>
      <ContextMenuContent anchor={anchor}>
        {groups.map((group, index) => (
          <Fragment key={group[0]?.key ?? index}>
            {index > 0 && <ContextMenuSeparator />}
            {group.map((action) => (
              <ContextMenuItem key={action.key} onClick={action.onSelect}>
                {action.icon}
                {action.label}
              </ContextMenuItem>
            ))}
          </Fragment>
        ))}
      </ContextMenuContent>
    </ContextMenu>
  );
}
