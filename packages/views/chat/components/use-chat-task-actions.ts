// 聊天任务操作 hook：封装"停止/取消任务""编辑或移除排队任务""清空队列"
// "把某条排队任务立即发送（抢占队头）"这些操作，负责乐观更新缓存、
// 失败回滚、以及取消后把草稿恢复进输入框。
"use client";

import { useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api, ApiError } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { chatKeys } from "@multica/core/chat/queries";
import {
  prioritizePendingChatTask,
  removePendingChatTask,
} from "@multica/core/chat/pending";
import { removeChatMessageFromCaches } from "@multica/core/realtime";
import { createLogger } from "@multica/core/logger";
import type {
  Attachment,
  CancelTaskResponse,
  ChatPendingTask,
} from "@multica/core/types";
import { useT } from "../../i18n";

const apiLogger = createLogger("chat.api");

type EnqueueLocalRestore = (restore: {
  id: string;
  content: string;
  attachments?: Attachment[];
  sessionId: string;
}) => void;

type CancelOptions = {
  /** 是否把被取消任务的内容恢复进输入框（本地草稿队列）。 */
  restoreDraftToInput: boolean;
  /** 打日志用的操作来源标签。 */
  source: string;
  /** 目标是一个排队任务时的具体动作：编辑（把内容取回输入框）或移除。 */
  queuedAction?: "edit" | "remove";
};

/**
 * 当前会话的任务操作集：取消正在跑/排队中的任务（可顺带把内容取回输入框）、
 * 编辑 / 移除 / 清空排队任务、以及"立即发送"某个排队任务（会把正在跑的队头任务
 * 取消掉再抢占）。全部带乐观更新与失败恢复。
 */
export function useChatTaskActions(
  activeSessionId: string | null,
  enqueueLocalRestore: EnqueueLocalRestore,
) {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();

  /**
   * 取消（停止）一个任务。先从缓存里乐观移除它；服务端返回被取消的聊天消息时，
   * 按需把它恢复进输入框。若服务端报了 409 且目标本是一个我们已缓存成 queued 的
   * 任务，则说明 daemon 已抢先开始跑它——改以纯"停止"语义再取消一次。
   */
  const cancelChatTask = useCallback(
    async (
      taskId: string,
      sessionId: string,
      options: CancelOptions,
    ): Promise<CancelTaskResponse | null> => {
      apiLogger.info("cancelTask.start", {
        taskId,
        sessionId,
        source: options.source,
      });
      const pendingKey = chatKeys.pendingTask(sessionId);
      await qc.cancelQueries({ queryKey: pendingKey });
      const pendingSnapshot = qc.getQueryData<ChatPendingTask>(pendingKey);
      const derivedQueuedAction = !options.queuedAction &&
        (options.restoreDraftToInput &&
            pendingSnapshot?.task_id === taskId &&
            pendingSnapshot.status === "queued" &&
            pendingSnapshot.supports_queue === true);
      let queuedAction = options.queuedAction ??
        (derivedQueuedAction ? "edit" : undefined);
      qc.setQueryData<ChatPendingTask>(
        pendingKey,
        (old) => removePendingChatTask(old, taskId),
      );

      try {
        let result: CancelTaskResponse;
        try {
          result = await api.cancelTaskById(
            taskId,
            queuedAction ? { queuedAction, sessionId } : undefined,
          );
        } catch (err) {
          if (!(derivedQueuedAction && err instanceof ApiError && err.status === 409)) {
            throw err;
          }
          // daemon 在我们缓存的 queued 快照之后把任务领走了。
          // 用取消它新的活动状态来保留"停止"的语义。
          queuedAction = undefined;
          result = await api.cancelTaskById(taskId);
        }
        const restored = result.cancelled_chat_message;
        if (restored) {
          removeChatMessageFromCaches(qc, restored.chat_session_id, restored.message_id);
          if (
            restored.restore_to_input &&
            !queuedAction &&
            options.restoreDraftToInput &&
            restored.chat_session_id === sessionId
          ) {
            enqueueLocalRestore({
              id: restored.message_id,
              content: restored.content,
              attachments: restored.attachments,
              sessionId: restored.chat_session_id,
            });
          }
        }
        qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
        qc.invalidateQueries({ queryKey: chatKeys.messagesPage(sessionId) });
        apiLogger.info("cancelTask.success", {
          taskId,
          sessionId,
          restoredToInput: !!restored?.restore_to_input && options.restoreDraftToInput,
        });
        return result;
      } catch (err) {
        qc.setQueryData(pendingKey, pendingSnapshot);
        apiLogger.warn("cancelTask.error (task may have already finished)", {
          taskId,
          sessionId,
          err,
        });
        qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
        qc.invalidateQueries({ queryKey: chatKeys.messagesPage(sessionId) });
        return null;
      } finally {
        qc.invalidateQueries({ queryKey: pendingKey });
        if (queuedAction === "edit") {
          qc.invalidateQueries({ queryKey: chatKeys.draftRestores(sessionId) });
        }
        if (wsId) qc.invalidateQueries({ queryKey: chatKeys.pendingTasks(wsId) });
      }
    },
    [qc, enqueueLocalRestore, wsId],
  );

  /** "编辑"排队任务：把它的内容取回输入框（作为可编辑草稿），并取消它。 */
  const handleEditQueuedTask = useCallback(
    async (taskId: string) => {
      if (!activeSessionId) return;
      const result = await cancelChatTask(taskId, activeSessionId, {
        restoreDraftToInput: true,
        source: "queued-message-edit",
        queuedAction: "edit",
      });
      if (!result) toast.error(t(($) => $.queue.action_failed_toast));
    },
    [activeSessionId, cancelChatTask, t],
  );

  /** "移除"排队任务：直接删除，不把内容取回输入框。 */
  const handleRemoveQueuedTask = useCallback(
    async (taskId: string) => {
      if (!activeSessionId) return;
      const result = await cancelChatTask(taskId, activeSessionId, {
        restoreDraftToInput: false,
        source: "queued-message-remove",
        queuedAction: "remove",
      });
      if (!result) toast.error(t(($) => $.queue.action_failed_toast));
    },
    [activeSessionId, cancelChatTask, t],
  );

  /** "清空队列"：取消并移除当前会话所有排队任务，同时清掉它们对应的消息气泡。 */
  const handleClearQueuedTasks = useCallback(async () => {
    if (!activeSessionId) return;
    const pendingKey = chatKeys.pendingTask(activeSessionId);
    await qc.cancelQueries({ queryKey: pendingKey });
    try {
      await api.clearQueuedChatTasks(activeSessionId);
      const pending = qc.getQueryData<ChatPendingTask>(pendingKey);
      for (const task of pending?.queued_tasks ?? []) {
        if (task.message_id) {
          removeChatMessageFromCaches(qc, activeSessionId, task.message_id);
        }
      }
      qc.setQueryData<ChatPendingTask>(pendingKey, (old) =>
        (old?.queued_tasks ?? []).reduce<ChatPendingTask | undefined>(
          (current, task) => removePendingChatTask(current, task.task_id),
          old,
        )
      );
    } catch (err) {
      apiLogger.warn("clearQueuedTasks.error", {
        sessionId: activeSessionId,
        err,
      });
      toast.error(t(($) => $.queue.action_failed_toast));
    } finally {
      qc.invalidateQueries({ queryKey: pendingKey });
      qc.invalidateQueries({ queryKey: chatKeys.messages(activeSessionId) });
      qc.invalidateQueries({ queryKey: chatKeys.messagesPage(activeSessionId) });
      if (wsId) qc.invalidateQueries({ queryKey: chatKeys.pendingTasks(wsId) });
    }
  }, [activeSessionId, qc, t, wsId]);

  /**
   * "立即发送"某条排队任务：先乐观把它提到队头，通知服务端去抢占；
   * 若服务端回应有活动任务（active_task_id），则把当前正在跑的任务取消掉
   * （其内容一并取回输入框）。
   */
  const handleSendQueuedTaskNow = useCallback(
    async (taskId: string) => {
      if (!activeSessionId) return;
      const pendingKey = chatKeys.pendingTask(activeSessionId);
      await qc.cancelQueries({ queryKey: pendingKey });
      const pendingSnapshot = qc.getQueryData<ChatPendingTask>(pendingKey);
      qc.setQueryData<ChatPendingTask>(
        pendingKey,
        (old) => prioritizePendingChatTask(old, taskId),
      );
      let prioritizeSucceeded = false;
      try {
        const result = await api.prioritizeQueuedChatTask(activeSessionId, taskId);
        if (result.task_id !== taskId) throw new Error("invalid prioritize response");
        prioritizeSucceeded = true;
        if (result.active_task_id) {
          const cancelled = await cancelChatTask(result.active_task_id, activeSessionId, {
            restoreDraftToInput: true,
            source: "queued-message-send-now",
          });
          if (!cancelled) throw new Error("current task could not be stopped");
        }
      } catch (err) {
        if (!prioritizeSucceeded) qc.setQueryData(pendingKey, pendingSnapshot);
        apiLogger.warn("sendQueuedTaskNow.error", {
          taskId,
          sessionId: activeSessionId,
          err,
        });
        toast.error(
          err instanceof ApiError && err.status === 409
            ? t(($) => $.queue.steer_unavailable_toast)
            : t(($) => $.queue.action_failed_toast),
        );
      } finally {
        qc.invalidateQueries({ queryKey: pendingKey });
      }
    },
    [activeSessionId, cancelChatTask, qc, t],
  );

  return {
    cancelChatTask,
    handleEditQueuedTask,
    handleRemoveQueuedTask,
    handleClearQueuedTasks,
    handleSendQueuedTaskNow,
  };
}
