import { type InfiniteData, type QueryClient } from "@tanstack/react-query";
import type { Agent, ChatMessage, ChatMessagesPage, MemberRole } from "@multica/core/types";
import { chatKeys } from "@multica/core/chat/queries";
import { canAssignAgentToIssue } from "@multica/core/permissions";

export function getVisibleChatAgents(
  agents: Agent[],
  userId: string | undefined,
  memberRole: MemberRole | undefined,
): Agent[] {
  return agents.filter(
    (agent) =>
      !agent.archived_at &&
      canAssignAgentToIssue(agent, {
        userId: userId ?? null,
        role: memberRole ?? null,
      }).allowed,
  );
}
export function appendChatMessageToLatestPageCache(
  qc: QueryClient,
  sessionId: string,
  message: ChatMessage,
) {
  qc.setQueryData<InfiniteData<ChatMessagesPage>>(
    chatKeys.messagesPage(sessionId),
    (old) => {
      if (!old) {
        return {
          pages: [{
            messages: [message],
            limit: 50,
            has_more: false,
            next_cursor: null,
          }],
          pageParams: [null],
        };
      }
      if (old.pages.some((page) => page.messages.some((m) => m.id === message.id))) {
        return old;
      }
      return {
        ...old,
        pages: old.pages.map((page, index) =>
          index === 0 ? { ...page, messages: [...page.messages, message] } : page,
        ),
      };
    },
  );
}

function removeChatMessageFromPageCache(
  qc: QueryClient,
  sessionId: string,
  messageId: string,
) {
  qc.setQueryData<InfiniteData<ChatMessagesPage> | undefined>(
    chatKeys.messagesPage(sessionId),
    (old) => {
      if (!old) return old;
      return {
        ...old,
        pages: old.pages.map((page) => ({
          ...page,
          messages: page.messages.filter((m) => m.id !== messageId),
        })),
      };
    },
  );
}

export function removeChatMessageFromCaches(
  qc: QueryClient,
  sessionId: string,
  messageId: string,
) {
  removeChatMessageFromPageCache(qc, sessionId, messageId);
}

export function replaceOptimisticChatMessageId(
  qc: QueryClient,
  sessionId: string,
  optimisticId: string,
  messageId: string,
  taskId: string,
) {
  const replace = (messages: ChatMessage[] | undefined) => {
    if (!messages) return messages;
    if (messages.some((m) => m.id === messageId)) {
      return messages.filter((m) => m.id !== optimisticId);
    }
    return messages.map((m) =>
      m.id === optimisticId ? { ...m, id: messageId, task_id: taskId } : m,
    );
  };

  qc.setQueryData<InfiniteData<ChatMessagesPage> | undefined>(
    chatKeys.messagesPage(sessionId),
    (old) => {
      if (!old) return old;
      return {
        ...old,
        pages: old.pages.map((page) => ({
          ...page,
          messages: replace(page.messages) ?? page.messages,
        })),
      };
    },
  );
}
