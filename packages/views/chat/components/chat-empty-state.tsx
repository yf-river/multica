// 聊天空状态：在用户发送第一条消息前显示的占位界面，含 agent 头像、描述和开场建议。
"use client";

import { Settings2 } from "lucide-react";
import { selectConversationStarters } from "@multica/core/agents";
import type { Agent } from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";
import {
  ConversationStarterList,
  useFallbackConversationStarters,
} from "./conversation-starter-list";

/**
 * 空状态占位：在用户发送第一条消息前显示。
 * @param agent - 当前对话对应的 agent（可能为 null）。
 * @param hasSessions - 该用户是否已有其他会话，用于区分首次使用提示。
 * @param onPickPrompt - 选中某条开场建议后触发的回调。
 * @param customizeHref - “自定义”入口的跳转地址，为 `null` 时隐藏该入口。
 */
export function EmptyState({
  agent,
  hasSessions = true,
  onPickPrompt,
  customizeHref = null,
}: {
  agent: Agent | null;
  hasSessions?: boolean;
  onPickPrompt: (prompt: string) => void;
  /**
   * “自定义”入口要把用户送去哪里，`null` 表示隐藏这个入口。
   * 由容器来决定：只有能在一个会持久化开场建议的后端上编辑“这个”agent 的人
   * 才会拿到链接。把它作为 prop，让这个组件保持纯展示——并且对读者来说，
   * 链接完全不出现在 DOM 里，而不是渲染一个被禁用的“诱饵”。
   */
  customizeHref?: string | null;
}) {
  const { t } = useT("chat");
  const description = agent?.description?.trim();
  const fallbackStarters = useFallbackConversationStarters();
  const { starters } = selectConversationStarters(
    agent?.conversation_starters,
    fallbackStarters,
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col items-center justify-center-safe gap-5 overflow-y-auto px-6 py-8">
      {agent && (
        <ActorAvatar
          actorType="agent"
          actorId={agent.id}
          size="2xl"
          className="ring-1 ring-inset ring-border"
        />
      )}
      <div className="max-w-sm space-y-1 text-center">
        <h3 className="text-title-sm font-semibold">
          {agent
            ? t(($) => $.empty_state.chat_with_named, { name: agent.name })
            : t(($) => $.empty_state.first_time_title)}
        </h3>
        {description && (
          <p className="text-body text-muted-foreground">{description}</p>
        )}
        {!hasSessions && (
          <p className="text-body text-muted-foreground">
            {t(($) => $.empty_state.first_time_actions)}
          </p>
        )}
      </div>
      {agent ? (
        <div
          className="w-full max-w-sm space-y-2"
          aria-label={t(($) => $.conversation_starters.aria_label)}
        >
          <ConversationStarterList starters={starters} onPick={onPickPrompt} />
          {customizeHref ? (
            <div className="flex justify-center pt-1">
              <AppLink
                href={customizeHref}
                className="inline-flex items-center gap-1.5 text-caption text-muted-foreground transition-colors hover:text-foreground"
              >
                <Settings2 className="size-3.5" aria-hidden="true" />
                {t(($) => $.conversation_starters.customize)}
              </AppLink>
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
