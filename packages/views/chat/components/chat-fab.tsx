"use client";

import { useEffect, useState } from "react";
import { MessageCircle } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { cn } from "@multica/ui/lib/utils";
import { useChatStore } from "@multica/core/chat";
import { chatSessionsOptions, pendingChatTasksOptions } from "@multica/core/chat/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { createLogger } from "@multica/core/logger";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from "@multica/ui/components/ui/tooltip";
import { useT } from "../../i18n";

const logger = createLogger("chat.ui");
const CHAT_FAB_STATUS_DELAY_MS = 2_000;

function useDeferredFabStatus(isOpen: boolean, wsId: string) {
  const [enabled, setEnabled] = useState(false);

  useEffect(() => {
    setEnabled(false);
    if (isOpen || !wsId) return;
    const timer = window.setTimeout(() => setEnabled(true), CHAT_FAB_STATUS_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [isOpen, wsId]);

  return enabled;
}

export function ChatFab() {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const isOpen = useChatStore((s) => s.isOpen);
  const toggle = useChatStore((s) => s.toggle);
  const statusEnabled = useDeferredFabStatus(isOpen, wsId);
  const { data: sessions = [] } = useQuery({
    ...chatSessionsOptions(wsId),
    enabled: isOpen,
  });
  const { data: pending } = useQuery({
    ...pendingChatTasksOptions(wsId),
    enabled: statusEnabled,
  });

  if (isOpen) return null;

  const unreadSessionCount = sessions.filter((s) => s.has_unread).length;
  const isRunning = (pending?.tasks ?? []).length > 0;

  const handleClick = () => {
    logger.info("fab.click (open chat)", { unreadSessionCount, isRunning });
    toggle();
  };

  // Tooltip text communicates the state that isn't carried by the icon/badge.
  const tooltip = isRunning
    ? t(($) => $.fab.running)
    : unreadSessionCount > 0
      ? t(($) => $.fab.unread, { count: unreadSessionCount })
      : t(($) => $.fab.default);

  return (
    <Tooltip>
      <TooltipTrigger
        onClick={handleClick}
        className={cn(
          "absolute bottom-2 right-2 z-50 flex size-10 cursor-pointer items-center justify-center rounded-full ring-1 ring-foreground/10 bg-card text-muted-foreground shadow-sm transition-transform hover:scale-110 hover:text-accent-foreground active:scale-95",
          // Impulse the button itself while a chat task is running — no
          // outer ring to keep things calm.
          isRunning && "animate-chat-impulse",
        )}
      >
        <MessageCircle className="size-5" />
        {unreadSessionCount > 0 && (
          <span className="pointer-events-none absolute -top-0.5 -right-0.5 flex min-w-4 h-4 items-center justify-center rounded-full bg-brand px-1 text-xs font-semibold leading-none text-background">
            {unreadSessionCount > 9 ? "9+" : unreadSessionCount}
          </span>
        )}
      </TooltipTrigger>
      <TooltipContent side="top" sideOffset={10}>{tooltip}</TooltipContent>
    </Tooltip>
  );
}
