"use client";

import { useEffect, useId, useState } from "react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { createRuntimeInputOptions, isNativeRuntimeCommand, runtimeInputCapabilitiesOptions, runtimeInputHistoryOptions } from "@multica/core/runtimes/inputs";
import { useRuntimeInputDraftStore } from "@multica/core/runtimes/input-draft-store";
import type { RuntimeInput } from "@multica/core/api/runtime-input-schema";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../i18n";

export function RuntimeInputPanel({ taskId, active, showControls = false }: { taskId: string; active: boolean; showControls?: boolean }) {
  const wsId = useWorkspaceId();
  const { t } = useT("common");
  const [expanded, setExpanded] = useState(false);
  const capabilities = useQuery(runtimeInputCapabilitiesOptions(wsId, taskId, active));
  const supported = capabilities.data?.available === true && capabilities.data.capabilities.length > 0;
  const history = useInfiniteQuery(runtimeInputHistoryOptions(wsId, taskId, active, (active && supported) || expanded));
  const inputs = history.data?.pages.flatMap((page) => page.inputs) ?? [];
  const latest = inputs[0];
  if (active && !expanded && !latest && (!showControls || !supported)) return null;
  return <details className="min-w-0 rounded-md border px-3 py-2 text-body" onToggle={(event) => setExpanded(event.currentTarget.open)}>
    <summary className="cursor-pointer rounded py-1 text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring">{t(($) => $.runtime_input.history)}
      {!expanded && latest && <span className="ml-2 text-caption">{inputStatus(latest, t)}</span>}
    </summary>
    {expanded && <div className="mt-3 max-h-[40dvh] space-y-3 overflow-y-auto">
      {showControls && active && <NativeCommandForm taskId={taskId} />}
      {history.isError && <div role="alert"><p>{t(($) => $.runtime_input.load_error)}</p><Button variant="outline" onClick={() => void history.refetch()}>{t(($) => $.runtime_interaction.retry)}</Button></div>}
      {!history.isPending && !history.isError && inputs.length === 0 && <p className="text-muted-foreground">{t(($) => $.runtime_input.empty)}</p>}
      {inputs.map((input) => <div key={input.id} className="min-w-0 space-y-1 border-t pt-3 first:border-t-0 first:pt-0">
        <p className="whitespace-pre-wrap break-words">{input.content}</p>
        <p className="text-caption text-muted-foreground" role={input.status === "failed" ? "status" : undefined}>{inputStatus(input, t)}</p>
        {input.output && <p className="whitespace-pre-wrap break-words text-muted-foreground">{input.output}</p>}
      </div>)}
      {history.hasNextPage && <Button variant="outline" disabled={history.isFetchingNextPage} onClick={() => void history.fetchNextPage()}>{t(($) => $.runtime_input.load_older)}</Button>}
    </div>}
  </details>;
}

function inputStatus(input: RuntimeInput, t: ReturnType<typeof useT<"common">>["t"]) {
  switch (input.status) {
    case "pending": return t(($) => $.runtime_input.pending);
    case "delivering": return t(($) => $.runtime_input.delivering);
    case "delivered": return t(($) => $.runtime_input.delivered);
    case "failed": return input.failureReason === "delivery_unknown" ? t(($) => $.runtime_input.uncertain) : input.failureReason === "turn_ended" ? t(($) => $.runtime_input.ended) : t(($) => $.runtime_input.failed);
    default: return t(($) => $.runtime_input.unknown);
  }
}

function NativeCommandForm({ taskId }: { taskId: string }) {
  const wsId = useWorkspaceId();
  const { t } = useT("common");
  const id = useId();
  const [content, setContent] = useState("");
  const [edited, setEdited] = useState(false);
  const saved = useRuntimeInputDraftStore((state) => state.drafts[taskId]);
  useEffect(() => {
    if (!edited && saved?.kind === "command") setContent(saved.content);
  }, [edited, saved]);
  const capabilities = useQuery(runtimeInputCapabilitiesOptions(wsId, taskId, true));
  const client = useQueryClient();
  const mutation = useMutation(createRuntimeInputOptions(client, wsId, taskId));
  const valid = isNativeRuntimeCommand(content);
  if (capabilities.data?.available !== true || !capabilities.data.capabilities.includes("command")) return null;
  return <form className="space-y-2" onSubmit={async (event) => {
    event.preventDefault();
    if (!valid || mutation.isPending) return;
    if (!useRuntimeInputDraftStore.persist.hasHydrated()) await useRuntimeInputDraftStore.persist.rehydrate();
    const store = useRuntimeInputDraftStore.getState();
    const input = store.reserve(taskId, "command", content);
    try {
      await mutation.mutateAsync(input);
      store.clear(taskId, input.id);
      setContent("");
    } catch {
      // 原文本和逻辑请求身份用于人工重试，命令不会自动重发。
    }
  }}>
    <label htmlFor={id} className="text-caption">{t(($) => $.runtime_input.command_label)}</label>
    <Textarea id={id} name="native-command" placeholder={t(($) => $.runtime_input.command_placeholder)} value={content} maxLength={4000} disabled={mutation.isPending} onChange={(event) => { setEdited(true); setContent(event.target.value); }} />
    {content.trim() && !valid && <p className="text-caption text-muted-foreground">{t(($) => $.runtime_input.invalid_command)}</p>}
    {mutation.isError && <p role="alert" className="text-destructive">{t(($) => $.runtime_input.save_error)}</p>}
    <Button type="submit" size="sm" disabled={!valid || mutation.isPending}>{t(($) => $.runtime_input.send_command)}</Button>
  </form>;
}
