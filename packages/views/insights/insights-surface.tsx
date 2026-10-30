"use client";
import type { ReactNode } from "react";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { useNavigation } from "../navigation";
import { useLocale } from "../i18n";
import { InsightsPage } from "./insights-page";
import { InsightsRealtimeBar } from "./realtime-bar";

export function InsightsSurface({
  children,
  issueId,
  chatSessionId,
  title,
}: {
  children: ReactNode;
  issueId?: string;
  chatSessionId?: string;
  title?: string;
}) {
  const wsId = useWorkspaceId();
  const nav = useNavigation();
  const zh = useLocale().startsWith("zh");
  const insights = nav.searchParams.get("view") === "insights";
  const change = (open: boolean) => {
    const q = new URLSearchParams(nav.searchParams);
    if (open) q.set("view", "insights");
    else {
      q.delete("view");
      q.delete("insight");
      q.delete("run");
    }
    nav.replace(`${nav.pathname}${q.size ? "?" + q : ""}${nav.hash}`);
  };
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b px-3 py-1">
        {title ? (
          <span className="mr-auto min-w-0 truncate text-body font-medium">
            {title}
          </span>
        ) : null}
        <Button
          size="sm"
          variant={insights ? "ghost" : "secondary"}
          onClick={() => change(false)}
        >
          {zh ? "会话" : "Conversation"}
        </Button>
        <Button
          size="sm"
          variant={insights ? "secondary" : "ghost"}
          onClick={() => change(true)}
        >
          {zh ? "上下文洞察" : "Context Insights"}
        </Button>
      </div>
      <div
        className={`${insights ? "hidden" : "flex"} min-h-0 flex-1 flex-col`}
      >
        {children}
      </div>
      {insights ? (
        <div className="min-h-0 flex-1">
          <InsightsPage
            workspaceId={wsId}
            issueId={issueId}
            chatSessionId={chatSessionId}
            taskId={nav.searchParams.get("run") ?? undefined}
            sessionKey={nav.searchParams.get("insight") ?? undefined}
          />
        </div>
      ) : (
        <InsightsRealtimeBar
          workspaceId={wsId}
          issueId={issueId}
          chatSessionId={chatSessionId}
          onOpen={() => change(true)}
        />
      )}
    </div>
  );
}
