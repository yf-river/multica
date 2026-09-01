"use client";

import { useEffect, useState } from "react";
import { MessageCircle } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { cn } from "@multica/ui/lib/utils";
import { useChatStore } from "@multica/core/chat";
import { chatSessionsOptions, pendingChatTasksOptions } from "@multica/core/chat/queries";
import { useWorkspaceId } from "@multica/core/paths";
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
        data-testid="chat-fab"
        aria-label={tooltip}
        className={cn(
          "fixed bottom-5 right-5 z-50 flex h-12 cursor-pointer items-center gap-2 rounded-full border border-brand/20 bg-background px-2.5 pr-4 text-sm font-medium text-foreground shadow-sm transition-colors hover:bg-brand/5",
          // Impulse the button itself while a chat task is running — no
          // outer ring to keep things calm.
          isRunning && "animate-chat-impulse",
        )}
      >
        <span className="relative flex size-8 items-center justify-center rounded-full bg-brand text-brand-foreground"><MessageCircle className="size-4" /><span className="absolute -right-0.5 -bottom-0.5 size-2.5 rounded-full border-2 border-background bg-success" /></span><span>{t(($) => $.fab.companion)}</span>
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
