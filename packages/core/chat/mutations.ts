import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { createLogger } from "../logger";
import { useWorkspaceId } from "../paths";
import { chatKeys } from "./queries";
import type { ChatSession } from "../types";

const logger = createLogger("chat.mut");

function useInvalidatingSessionMutation<Variables, Result>(
  operation: string,
  mutationFn: (variables: Variables) => Promise<Result>,
) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn,
    onError: (error) => logger.error(`${operation}.error`, error),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
    },
  });
}

function useOptimisticSessionMutation<Variables, Result>(
  operation: string,
  mutationFn: (variables: Variables) => Promise<Result>,
  update: (sessions: ChatSession[], variables: Variables) => ChatSession[],
) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const queryKey = chatKeys.sessions(wsId);

  return useMutation<Result, Error, Variables, ChatSession[] | undefined>({
    mutationFn,
    onMutate: async (variables) => {
      await qc.cancelQueries({ queryKey });
      const previous = qc.getQueryData<ChatSession[]>(queryKey);
      qc.setQueryData<ChatSession[]>(queryKey, (sessions) =>
        sessions ? update(sessions, variables) : sessions,
      );
      return previous;
    },
    onError: (error, _variables, previous) => {
      logger.error(`${operation}.error.rollback`, error);
      if (previous !== undefined) qc.setQueryData(queryKey, previous);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey });
    },
  });
}

export function useCreateChatSession() {
  return useInvalidatingSessionMutation(
    "createChatSession",
    (data: { agent_id: string; title?: string; idempotencyKey: string }) => {
      const { idempotencyKey, ...request } = data;
      return api.createChatSession(request, idempotencyKey);
    },
  );
}

/**
 * Clears the session's unread state server-side. Optimistically flips
 * has_unread to false in the cached list so the FAB badge drops
 * immediately. The server broadcasts chat:session_read so other devices
 * also sync.
 */
export function useMarkChatSessionRead() {
  return useOptimisticSessionMutation(
    "markChatSessionRead",
    (sessionId: string) => api.markChatSessionRead(sessionId),
    (sessions, sessionId) =>
      sessions.map((session) =>
        session.id === sessionId ? { ...session, has_unread: false } : session,
      ),
  );
}

/**
 * Renames a chat session. Optimistically swaps the title in the cached
 * list so the dropdown reflects the new label immediately; rolls back on
 * error. The matching `chat:session_updated` WS event keeps other
 * tabs/devices in sync — see use-realtime-sync.ts.
 */
export function useUpdateChatSession() {
  return useOptimisticSessionMutation(
    "updateChatSession",
    (data: { sessionId: string; title: string }) =>
      api.updateChatSession(data.sessionId, { title: data.title }),
    (sessions, { sessionId, title }) =>
      sessions.map((session) =>
        session.id === sessionId ? { ...session, title } : session,
      ),
  );
}

/**
 * Hard-deletes a chat session. Optimistically removes the row from the
 * sessions list so the dropdown updates instantly; rolls back on error.
 * The matching `chat:session_deleted` WS event keeps other tabs/devices
 * in sync — see use-realtime-sync.ts.
 */
export function useDeleteChatSession() {
  return useOptimisticSessionMutation(
    "deleteChatSession",
    (sessionId: string) => api.deleteChatSession(sessionId),
    (sessions, sessionId) =>
      sessions.filter((session) => session.id !== sessionId),
  );
}
