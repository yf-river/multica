"use client";

import { useEffect, useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import {
  claimPendingChatOperation,
  releasePendingChatOperation,
  replayPendingChatOperation,
  usePendingChatOperationStore,
} from "@multica/core/chat";
import { chatKeys } from "@multica/core/chat/queries";
import { useAuthStore } from "@multica/core/auth";
import { getCurrentWsId } from "@multica/core/platform";
import { createLogger } from "@multica/core/logger";
import { isOutcomeUnknownMutationError } from "./chat-send-failure";

const logger = createLogger("chat.recovery");

/** Retry durable chat intents after reload or an outcome-unknown response. */
export function useChatOperationRecovery(workspaceId: string | null) {
  const accountId = useAuthStore((state) => state.user?.id ?? null);
  const operationMap = usePendingChatOperationStore((state) => state.operations);
  const operations = useMemo(() => {
    if (!accountId || !workspaceId) return [];
    return Object.values(operationMap)
      .filter((operation) =>
        operation.accountId === accountId && operation.workspaceId === workspaceId,
      )
      .sort((a, b) => a.createdAt - b.createdAt);
  }, [accountId, operationMap, workspaceId]);
  const queryClient = useQueryClient();

  useEffect(() => {
    if (!accountId || !workspaceId) return;

    for (const operation of operations) {
      if (!claimPendingChatOperation(operation.id)) continue;

      void (async () => {
        try {
          const { sessionId, response: result } = await replayPendingChatOperation(
            operation,
            api,
            (createdSessionId) => {
              if (!usePendingChatOperationStore.getState().operations[operation.id]) {
                throw new Error("pending chat operation was cleared during recovery");
              }
              usePendingChatOperationStore.getState().update(operation.id, {
                sessionId: createdSessionId,
                stage: "sending-message",
              });
            },
          );
          const current = usePendingChatOperationStore.getState().operations[operation.id];
          if (!current) return;

          if (current.cancelRequested) {
            await api.cancelTaskById(result.task_id);
          }
          usePendingChatOperationStore.getState().remove(operation.id);

          // Never write view state for a workspace/account the user left
          // while recovery was awaiting the network.
          if (
            useAuthStore.getState().user?.id === accountId &&
            getCurrentWsId() === workspaceId
          ) {
            queryClient.invalidateQueries({ queryKey: chatKeys.sessions(workspaceId) });
            queryClient.invalidateQueries({ queryKey: chatKeys.messagesPage(sessionId) });
            queryClient.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) });
            queryClient.invalidateQueries({ queryKey: chatKeys.pendingTasks(workspaceId) });
          }
          logger.info("pending chat operation recovered", {
            operationId: operation.id,
            sessionId,
            taskId: result.task_id,
          });
        } catch (error) {
          if (!isOutcomeUnknownMutationError(error)) {
            // A 4xx proves this exact payload was rejected. Transport errors,
            // malformed success responses and 5xx remain retryable with the
            // same key because an intermediary may have lost the stored 201.
            usePendingChatOperationStore.getState().remove(operation.id);
          }
          logger.warn("pending chat operation recovery deferred", {
            operationId: operation.id,
            stage: operation.stage,
            outcomeUnknown: isOutcomeUnknownMutationError(error),
          });
        } finally {
          releasePendingChatOperation(operation.id);
        }
      })();
    }
  }, [accountId, operations, queryClient, workspaceId]);
}
