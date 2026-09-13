// 聊天悬浮按钮（FAB）：右下角圆形入口，点击后打开/关闭悬浮聊天窗口。
"use client";

import { MessageCircle } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { cn } from "@multica/ui/lib/utils";
import { useChatStore } from "@multica/core/chat";
import {
  hasPendingChatTasksOptions,
} from "@multica/core/chat/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { createLogger } from "@multica/core/logger";
import { useShortcut } from "@multica/core/shortcuts";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from "@multica/ui/components/ui/tooltip";
import { ShortcutKeycaps } from "../../common/shortcut-keycaps";
import { useT } from "../../i18n";

const logger = createLogger("chat.ui");

/**
 * 聊天悬浮按钮（FAB）：悬浮在右下角，点击切换聊天窗口的开合。
 */
export function ChatFab() {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const isOpen = useChatStore((s) => s.isOpen);
  const toggle = useChatStore((s) => s.toggle);
  // 这个按钮的键盘入口只有在可被发现时才有用，所以 tooltip 带上当前的按键绑定
  // （设置 → 快捷键可以重新绑定或清除它，因此存在 null 的情况）。
  const shortcut = useShortcut("toggleChat");
  // FAB 只需要一个布尔值“是否有任务在运行”，而且只在窗口关闭时才需要
  // （打开时，由 ChatWindow 负责详细的 pending 查询）。用 `enabled: !isOpen`
  // 把关，让最小化的按钮完全不进入逐消息聚合的热路径（MUL-4159）。
  const { data: hasPending } = useQuery({
    ...hasPendingChatTasksOptions(wsId),
    enabled: !isOpen,
  });

  if (isOpen) return null;

  const isRunning = hasPending?.has_pending ?? false;

  const handleClick = () => {
    logger.info("fab.click (open chat)", { isRunning });
    toggle();
  };

  // Tooltip 文案在悬停时展示运行/未读状态；FAB 本身不再显示未读数角标
  // （它和聊天标签页的角标重复了，MUL-4374）。
  const tooltip = isRunning
    ? t(($) => $.fab.running)
    : t(($) => $.fab.default);

  return (
    <Tooltip>
      <TooltipTrigger
        onClick={handleClick}
        aria-label={tooltip}
        className={cn(
          // 几何尺寸来自共享 token，这样各页面为这个角落预留的间距都源自同一组数值。
          "absolute bottom-[var(--chat-launcher-inset)] right-[var(--chat-launcher-inset)] z-50 flex size-[var(--chat-launcher-size)] touch-manipulation items-center justify-center rounded-full bg-surface-raised text-muted-foreground shadow-[var(--floating-shadow)] ring-1 ring-surface-border transition-[background-color,color,box-shadow] hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/70 focus-visible:ring-offset-2 focus-visible:ring-offset-page-canvas active:bg-surface-hover",
          // 聊天任务运行时让按钮本身脉冲——不加外圈，保持平静。
          isRunning && "animate-chat-impulse",
        )}
      >
        <MessageCircle className="size-5" />
      </TooltipTrigger>
      <TooltipContent side="top" sideOffset={10}>
        {tooltip}
        {shortcut ? <ShortcutKeycaps shortcut={shortcut} className="ml-1.5" /> : null}
      </TooltipContent>
    </Tooltip>
  );
}
