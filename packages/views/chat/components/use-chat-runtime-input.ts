"use client";

import { useCallback } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createRuntimeInputOptions, isNativeRuntimeCommand, runtimeInputCapabilitiesOptions } from "@multica/core/runtimes/inputs";
import { useRuntimeInputDraftStore } from "@multica/core/runtimes/input-draft-store";
import { toast } from "sonner";
import { useT } from "../../i18n";

export type ChatDelivery = "queue" | "current";

// 聊天页和悬浮窗口共用投递选择、幂等身份和失败反馈。
export function useChatRuntimeInput(wsId: string, taskId: string, active: boolean) {
  const { t } = useT("chat");
  const client = useQueryClient();
  const query = useQuery(runtimeInputCapabilitiesOptions(wsId, taskId, active));
  const mutation = useMutation(createRuntimeInputOptions(client, wsId, taskId));
  const textAvailable = active && query.data?.available === true && query.data.capabilities.includes("text");
  const commandsAvailable = active && query.data?.available === true && query.data.capabilities.includes("command");
  const sendCurrent = mutation.mutateAsync;
  const trySend = useCallback(async (content: string, hasAttachments: boolean, delivery: ChatDelivery = "queue"): Promise<boolean | undefined> => {
    const command = commandsAvailable && isNativeRuntimeCommand(content);
    if (delivery !== "current" && !command) return undefined;
    if (!taskId || (command ? !commandsAvailable : !textAvailable)) {
      toast.error(t(($) => $.input.current_unavailable));
      return false;
    }
    if (hasAttachments) {
      toast.error(t(($) => $.input.current_text_only));
      return false;
    }
    if (!useRuntimeInputDraftStore.persist.hasHydrated()) await useRuntimeInputDraftStore.persist.rehydrate();
    const store = useRuntimeInputDraftStore.getState();
    const input = store.reserve(taskId, command ? "command" : "text", content);
    try {
      await sendCurrent(input);
      store.clear(taskId, input.id);
      return true;
    } catch {
      toast.error(t(($) => $.input.current_send_failed));
      return false;
    }
  }, [taskId, textAvailable, commandsAvailable, sendCurrent, t]);
  return { textAvailable, commandsAvailable, trySend };
}
