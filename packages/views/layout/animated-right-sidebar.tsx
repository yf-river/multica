"use client";

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";
import { motion } from "motion/react";
import type { Layout, PanelSize } from "react-resizable-panels";
import {
  isEditableShortcutTarget,
  shortcutMatchesEvent,
  useShortcut,
} from "@multica/core/shortcuts";
import { isImeComposing } from "@multica/core/utils";
import { cn } from "@multica/ui/lib/utils";

/**
 * 布局模块：带动画的右侧边栏。
 * 提供右侧详情面板的开合状态管理、开关动画，以及快捷键切换的封装。
 */

/** 右侧边栏面板的统一 motion 属性，供各详情面板复用。 */
export const rightSidebarPanelMotionProps = {
  "data-right-sidebar-panel": "true",
  style: { overflowX: "hidden" },
} as const;

const RIGHT_SIDEBAR_PANEL_TRANSITION_MS = 220;
const RIGHT_SIDEBAR_PANEL_SETTLE_MS = RIGHT_SIDEBAR_PANEL_TRANSITION_MS + 80;

const rightSidebarTransition = {
  type: "spring",
  stiffness: 420,
  damping: 38,
  mass: 0.8,
} as const;

/**
 * 计算右侧边栏初始是否展开。
 * 优先读取布局里恢复的尺寸，取不到再退回默认值。
 * @param defaultOpen 默认是否展开。
 * @param defaultLayout 已恢复的面板布局，用于读取持久化的尺寸。
 * @param panelId 面板 id，默认 "sidebar"。
 * @returns 初始是否展开。
 */
export function getAnimatedRightSidebarInitialOpen(
  defaultOpen: boolean,
  defaultLayout: Layout | undefined,
  panelId = "sidebar",
) {
  const restoredSize = defaultLayout?.[panelId];
  return typeof restoredSize === "number" ? restoredSize > 0 : defaultOpen;
}

/** 判断右侧边栏面板是否处于展开状态（按百分比或像素大于 0）。 */
function isRightSidebarPanelOpen(size: PanelSize) {
  return size.asPercentage > 0 || size.inPixels > 0;
}

/**
 * 管理右侧边栏的开合状态与动画。
 * 区分「实际展开」与「视觉展开」：切换后先立即更新两者触发动画，
 * 动画结束后再关闭 motion 开关，避免隐藏时仍持续播放补间。
 * @param initialOpen 初始是否展开。
 */
export function useAnimatedRightSidebarState(initialOpen: boolean) {
  const [open, setOpen] = useState(initialOpen);
  const [visualOpen, setVisualOpen] = useState(initialOpen);
  const [motionEnabled, setMotionEnabled] = useState(false);
  const toggleTargetRef = useRef<boolean | null>(null);
  const settleTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const beginToggle = useCallback((nextOpen: boolean) => {
    toggleTargetRef.current = nextOpen;
    setMotionEnabled(true);
    setOpen(nextOpen);
    setVisualOpen(nextOpen);

    if (settleTimeoutRef.current) {
      clearTimeout(settleTimeoutRef.current);
    }

    settleTimeoutRef.current = setTimeout(() => {
      toggleTargetRef.current = null;
      settleTimeoutRef.current = null;
      setMotionEnabled(false);
    }, RIGHT_SIDEBAR_PANEL_SETTLE_MS);
  }, []);

  const handleResize = useCallback((size: PanelSize) => {
    const nextOpen = isRightSidebarPanelOpen(size);
    const toggleTarget = toggleTargetRef.current;

    if (toggleTarget === null) {
      setOpen(nextOpen);
      setVisualOpen(nextOpen);
      return;
    }

    setOpen(toggleTarget);
  }, []);

  useEffect(() => {
    return () => {
      if (settleTimeoutRef.current) {
        clearTimeout(settleTimeoutRef.current);
      }
    };
  }, []);

  return { open, visualOpen, motionEnabled, beginToggle, handleResize };
}

/**
 * 只为当前可见、且拥有右侧边栏的详情页注册切换快捷键。
 * 只有目标元素真实存在（getClientRects 非空）时才响应，避免隐藏页面抢快捷键。
 * @param targetRef 拥有右侧边栏的目标元素引用。
 * @param onToggle 触发切换时的回调。
 */
export function useRightSidebarShortcut(
  targetRef: RefObject<HTMLElement | null>,
  onToggle: () => void,
) {
  const shortcut = useShortcut("toggleRightSidebar");

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat || isImeComposing(event)) return;
      if (isEditableShortcutTarget(event.target)) return;
      if (!shortcutMatchesEvent(shortcut, event)) return;

      const target = targetRef.current;
      if (!target || target.getClientRects().length === 0) return;

      event.preventDefault();
      onToggle();
    };

    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [onToggle, shortcut, targetRef]);
}

/**
 * 带动画的右侧边栏容器。
 * @param open 是否展开，决定透明度和水平位移。
 * @param motionEnabled 是否启用过渡动画；关闭时动画时长为 0，立即到位。
 * @param children 面板内容。
 * @param className 附加样式类名。
 */
export function AnimatedRightSidebar({
  open,
  motionEnabled,
  children,
  className,
}: {
  open: boolean;
  motionEnabled?: boolean;
  children: ReactNode;
  className?: string;
}) {
  return (
    <motion.div
      animate={{ opacity: open ? 1 : 0, x: open ? 0 : 12 }}
      // `relative` makes this scroller the containing block of absolutely
      // positioned descendants (`sr-only` labels in the execution log, etc.).
      // Without it they resolve against an ancestor outside the scroller:
      // they escape its clip and stretch the enclosing panel's scroll height,
      // so wheeling past the end of the comments scrolled the whole issue
      // detail up and left a blank band at the bottom (MUL-8100).
      className={cn(
        "relative h-full overflow-x-hidden overflow-y-auto border-l",
        !open && "pointer-events-none",
        className,
      )}
      initial={false}
      transition={motionEnabled ? rightSidebarTransition : { duration: 0 }}
    >
      <div className="p-4">{children}</div>
    </motion.div>
  );
}
