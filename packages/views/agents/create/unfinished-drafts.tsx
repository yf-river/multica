"use client";

// 本文件是“AI 创建方式”选择页顶部的一条横幅/弹层：在用户本会悄悄开出第三条会话
// 的这块屏上，把 TA 开过又丢下的会话摆出来。一条未完成草稿时直接打开它（单选器
// 列一项等于问一个已有答案的问题）；多条时弹对话框列出各自内容让用户挑。

import { useState } from "react";
import { ChevronRight, MessageSquare } from "lucide-react";
import { decodeBuilderInput, stripBuilderDraft } from "@multica/core/agents";
import type { AgentBuilderSessionSummary } from "@multica/core/types";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";
import { useT, useTimeAgo } from "../../i18n";

/**
 * 在这块原本会悄悄再开第三条会话的屏上，把用户开过又丢下的会话亮出来。
 *
 * 只有一条未完成草稿时直接打开——一个只列一项的选择器是“只有一个答案的问题”。
 * 多条则弹对话框，因为在它们之间挑选需要展示每条大概在聊什么，横幅放不下。
 */
export function UnfinishedDraftsBanner({
  sessions,
  onResume,
}: {
  sessions: AgentBuilderSessionSummary[];
  onResume: (sessionId: string) => void;
}) {
  const { t } = useT("agents");
  const [picking, setPicking] = useState(false);

  if (sessions.length === 0) return null;

  const openOrPick = () => {
    const only = sessions[0];
    if (sessions.length === 1 && only) {
      onResume(only.session_id);
      return;
    }
    setPicking(true);
  };

  return (
    <>
      <button
        type="button"
        onClick={openOrPick}
        className={cn(
          "mb-5 flex w-full items-center gap-3 rounded-lg border bg-muted/40 px-4 py-3 text-left transition-colors",
          "hover:border-primary/40 hover:bg-accent/30",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        )}
      >
        <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-background text-muted-foreground">
          <MessageSquare className="size-4" aria-hidden="true" />
        </span>
        <span className="min-w-0 flex-1 text-body">
          {t(($) => $.creation_studio.drafts.banner, {
            count: sessions.length,
          })}
        </span>
        <ChevronRight
          className="size-4 shrink-0 text-muted-foreground"
          aria-hidden="true"
        />
      </button>

      <Dialog open={picking} onOpenChange={setPicking}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t(($) => $.creation_studio.drafts.title)}</DialogTitle>
            <DialogDescription>
              {t(($) => $.creation_studio.drafts.pick_hint)}
            </DialogDescription>
          </DialogHeader>
          <div className="max-h-96 space-y-2 overflow-y-auto">
            {sessions.map((session) => (
              <button
                key={session.session_id}
                type="button"
                onClick={() => {
                  setPicking(false);
                  onResume(session.session_id);
                }}
                className={cn(
                  "flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-colors",
                  "hover:border-primary/40 hover:bg-accent/30",
                  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                )}
              >
                <span className="min-w-0 flex-1">
                  <span className="flex items-baseline gap-2">
                    <span className="truncate text-body font-medium">
                      {draftTitle(session) ||
                        t(($) => $.creation_studio.drafts.untitled)}
                    </span>
                    {session.last_message_at ? (
                      <DraftTimestamp at={session.last_message_at} />
                    ) : null}
                  </span>
                  {/* 两行：够认出是哪条会话，又短到长回复不至于把下一行挤出弹窗。 */}
                  <span className="mt-1 line-clamp-2 block text-caption leading-5 text-muted-foreground">
                    {draftPreview(session)}
                  </span>
                </span>
              </button>
            ))}
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}

/** 相对时间的小时间戳。 */
function DraftTimestamp({ at }: { at: string }) {
  const timeAgo = useTimeAgo();
  return (
    <span className="ml-auto shrink-0 text-micro text-muted-foreground">
      {timeAgo(at)}
    </span>
  );
}

/**
 * 用户会怎么称呼这条草稿。唯一候选是已存配置里的名字——服务端给的标题在每行上
 * 都是同一个字符串，会让列表没法看。
 */
function draftTitle(
  session: Pick<AgentBuilderSessionSummary, "draft">,
): string {
  return session.draft?.name?.trim() ?? "";
}

/**
 * 存的消息仍是 builder 的线上格式——用户侧是携带整份草稿的 JSON 信封，助手侧
 * 以 `<agent_draft>` 块结尾。原样展示就是一堵 JSON 墙，所以两侧都用会话本身
 * 用的同一套 helper 解码。
 */
export function draftPreview(
  session: Pick<
    AgentBuilderSessionSummary,
    "last_message_content" | "last_message_role"
  >,
): string {
  const content = session.last_message_content;
  if (!content) return "";
  return session.last_message_role === "user"
    ? decodeBuilderInput(content)
    : stripBuilderDraft(content);
}
