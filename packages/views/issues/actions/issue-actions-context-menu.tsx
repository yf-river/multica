"use client";

// 问题右键菜单：用「一个视图一个菜单」的单例方案替代每个卡片/行各挂一个菜单，避免大量菜单根节点拖慢标签切换。
import {
  cloneElement,
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type ReactElement,
  type ReactNode,
} from "react";
import type { Issue } from "@multica/core/types";
import {
  ContextMenu,
  ContextMenuContent,
} from "@multica/ui/components/ui/context-menu";
import { useIssueActions } from "./use-issue-actions";
import {
  IssueActionsMenuItems,
  contextPrimitives,
} from "./issue-actions-menu-items";
import { AssigneePicker } from "../components/pickers";

/**
 * 整个视图共享一个右键菜单，而不是给每张卡片/每行各挂一个 Base UI 的 ContextMenu 根节点。
 * 旧实现里看板一旦重新挂载，就会创建「N × (菜单根 + 触发器)」——这是切换标签页卡顿的最大来源，
 * 但实际同一时刻最多只会打开一个菜单。
 *
 * 菜单项改为「委托」模式：每个菜单项的外层包装只挂一个 `onContextMenu`，把 (issue, 鼠标位置)
 * 上报给 Provider 的单例，由单例用一个虚拟锚点元素把菜单定位到光标处。
 *
 * 已知欠账：Base UI 的 ContextMenuTrigger 在触屏长按也能打开；这里委托的 `contextmenu` 事件
 * 能覆盖右键（和 Android 长按），但覆盖不了 iOS Safari 的长按。
 */

interface ActiveMenu {
  issue: Issue;
  position: { x: number; y: number };
}

type OpenIssueContextMenu = (issue: Issue, event: React.MouseEvent) => void;

const IssueContextMenuContext = createContext<OpenIssueContextMenu | null>(
  null,
);

/**
 * 右键菜单的共享 Provider：把「打开某个问题的右键菜单」的能力注入到上下文，
 * 并挂载唯一一份菜单实例。
 * @param props.children - 需要被包裹、能够触发右键菜单的子内容（通常是一整块视图）。
 */
export function IssueContextMenuProvider({
  children,
}: {
  children: ReactNode;
}) {
  const [active, setActive] = useState<ActiveMenu | null>(null);
  const [open, setOpen] = useState(false);
  // 被右键点击的那个元素。Base UI 原来会给每个菜单项的触发器打上 `data-popup-open`，
  // 行/卡片用这个属性来表现「我的菜单正开着」的样式；但 React 不会渲染这个属性，
  // 所以在这里手动维护它，既保留了视觉反馈，又不用在开/关菜单时重渲染每一行。
  const triggerElRef = useRef<HTMLElement | null>(null);

  const openMenu = useCallback<OpenIssueContextMenu>((issue, event) => {
    event.preventDefault();
    triggerElRef.current?.removeAttribute("data-popup-open");
    const el = event.currentTarget as HTMLElement;
    el.setAttribute("data-popup-open", "");
    triggerElRef.current = el;
    setActive({ issue, position: { x: event.clientX, y: event.clientY } });
    setOpen(true);
  }, []);

  const handleOpenChange = useCallback((v: boolean) => {
    if (!v) {
      triggerElRef.current?.removeAttribute("data-popup-open");
      triggerElRef.current = null;
    }
    setOpen(v);
  }, []);

  return (
    <IssueContextMenuContext.Provider value={openMenu}>
      {children}
      {/* 首次使用时才挂载，之后保持挂载（每个视图只保留一个关闭状态的菜单根节点，
          弹层本身在关闭时会卸载）。 */}
      {active && (
        <IssueContextMenuSingleton
          issue={active.issue}
          position={active.position}
          open={open}
          onOpenChange={handleOpenChange}
        />
      )}
    </IssueContextMenuContext.Provider>
  );
}

function IssueContextMenuSingleton({
  issue,
  position,
  open,
  onOpenChange,
}: ActiveMenu & {
  open: boolean;
  onOpenChange: (v: boolean) => void;
}) {
  const actions = useIssueActions(issue);
  const [assigneeOpen, setAssigneeOpen] = useState(false);

  // 在右键位置放一个「点大小」的虚拟锚点，替代 Base UI 自己的触发器本来会注册的光标锚点。
  const anchor = useMemo(
    () => ({
      getBoundingClientRect: () =>
        DOMRect.fromRect({
          x: position.x,
          y: position.y,
          width: 0,
          height: 0,
        }),
    }),
    [position.x, position.y],
  );

  return (
    <>
      <ContextMenu open={open} onOpenChange={(v) => onOpenChange(v)}>
        <ContextMenuContent anchor={anchor}>
          <IssueActionsMenuItems
            issue={issue}
            actions={actions}
            primitives={contextPrimitives}
            onOpenAssignee={() => setAssigneeOpen(true)}
          />
        </ContextMenuContent>
      </ContextMenu>
      {/* 只有用户真的打开时才挂载指派人选择器，并锚定在右键位置，
          让它出现在刚才菜单所在处，而不是跳到行的左上角。 */}
      {assigneeOpen && (
        <AssigneePicker
          assigneeType={issue.assignee_type}
          assigneeId={issue.assignee_id}
          onUpdate={actions.updateField}
          open={assigneeOpen}
          onOpenChange={setAssigneeOpen}
          triggerRender={
            <span
              aria-hidden
              className="pointer-events-none fixed"
              style={{
                left: position.x,
                top: position.y,
                width: 0,
                height: 0,
              }}
            />
          }
          trigger={<span />}
          align="start"
        />
      )}
    </>
  );
}

interface IssueActionsContextMenuProps {
  issue: Issue;
  /** 被嫁接菜单触发行为的单个 React 元素。 */
  children: ReactElement<{
    onContextMenu?: (e: React.MouseEvent) => void;
    className?: string;
  }>;
}

/**
 * 单个菜单项的外层包装：克隆子元素并给它挂上 `onContextMenu`，
 * 用它打开当前视图的单例右键菜单。只消耗一次 context 读取，不携带任何菜单机制。
 * 必须处于 {@link IssueContextMenuProvider} 之下（由 IssueSurface 挂载）。
 * @param props.issue - 该菜单项对应的问题。
 * @param props.children - 被包裹的单个 React 元素。
 */
export function IssueActionsContextMenu({
  issue,
  children,
}: IssueActionsContextMenuProps) {
  const openMenu = useContext(IssueContextMenuContext);
  if (!openMenu) {
    throw new Error(
      "IssueActionsContextMenu requires an IssueContextMenuProvider ancestor",
    );
  }
  const childOnContextMenu = children.props.onContextMenu;
  const childClassName = children.props.className;
  return cloneElement(children, {
    // Base UI 的触发器会把 `select-none` 合并进菜单项；这里保留它，
    // 避免右键目标变成可被文本选中的。
    className: childClassName ? `${childClassName} select-none` : "select-none",
    onContextMenu: (e: React.MouseEvent) => {
      childOnContextMenu?.(e);
      openMenu(issue, e);
    },
  });
}
