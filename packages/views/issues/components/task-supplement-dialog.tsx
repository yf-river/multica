"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { MessageSquarePlus } from "lucide-react";
import { api, errorCode } from "@multica/core/api";
import { createSafeId } from "@multica/core/utils";
import type { AgentTask, TimelineEntry } from "@multica/core/types";
import { issueKeys } from "@multica/core/issues/queries";
import { useCreateTaskSupplement, useRetryTaskSupplement } from "@multica/core/issues/mutations";
import { useTaskSupplementDraftStore } from "@multica/core/issues/stores/task-supplement-draft-store";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../../i18n";

function canSupplement(task: AgentTask) {
  return task.status === "running" && task.can_supplement === true && task.supplement_capability === "task-supplement-v1";
}

export function TaskSupplementButton({ task, issueId }: { task: AgentTask; issueId: string }) {
  const { t } = useT("issues");
  const open = useTaskSupplementDraftStore((state) => state.open);
  if (!canSupplement(task)) return null;
  return <button type="button" className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground" title={t(($) => $.execution_log.supplement_action)} aria-label={t(($) => $.execution_log.supplement_action)} onClick={() => open(task.id, issueId)}><MessageSquarePlus className="size-3.5" /></button>;
}

// 独立于活跃行挂载：运行结束或侧栏折叠后，输入仍留在当前任务的弹窗中。
export function TaskSupplementDialogs({ tasks, issueId }: { tasks: AgentTask[]; issueId: string }) {
  const drafts = useTaskSupplementDraftStore((state) => state.drafts);
  return <>{tasks.filter((task) => drafts[task.id]?.open && drafts[task.id]?.issueId === issueId).map((task) => <TaskSupplementDialog key={task.id} task={task} issueId={issueId} />)}</>;
}

function TaskSupplementDialog({ task, issueId }: { task: AgentTask; issueId: string }) {
  const { t } = useT("issues");
  const draft = useTaskSupplementDraftStore((state) => state.drafts[task.id]);
  const setContent = useTaskSupplementDraftStore((state) => state.setContent);
  const close = useTaskSupplementDraftStore((state) => state.close);
  const clear = useTaskSupplementDraftStore((state) => state.clear);
  const markEnded = useTaskSupplementDraftStore((state) => state.markEnded);
  const setRequestId = useTaskSupplementDraftStore((state) => state.setRequestId);
  const mutation = useCreateTaskSupplement(issueId);
  const [failed, setFailed] = useState(false);
  const allowed = canSupplement(task) && !draft?.ended;
  useEffect(() => {
    if (["completed", "cancelled", "failed"].includes(task.status)) markEnded(task.id);
  }, [markEnded, task.id, task.status]);
  if (!draft) return null;
  const send = async () => {
    if (!allowed || !draft.content.trim() || mutation.isPending) return;
    const requestId = draft.clientRequestId ?? createSafeId();
    setRequestId(task.id, requestId);
    setFailed(false);
    try { await mutation.mutateAsync({ taskId: task.id, content: draft.content, clientRequestId: requestId }); }
    catch (error) {
      if (errorCode(error) === "task_supplement_turn_ended") markEnded(task.id);
      setFailed(true);
    }
  };
  return <Dialog open={draft.open} onOpenChange={(open) => { if (!open) close(task.id); }}>
    <DialogContent className="sm:max-w-lg">
      <DialogHeader><DialogTitle>{t(($) => $.execution_log.supplement_action)}</DialogTitle><DialogDescription>{t(($) => $.execution_log.supplement_notice)}</DialogDescription></DialogHeader>
      <Textarea autoFocus rows={5} maxLength={16000} value={draft.content} disabled={mutation.isPending} aria-label={t(($) => $.execution_log.supplement_placeholder)} placeholder={t(($) => $.execution_log.supplement_placeholder)} onChange={(event) => setContent(task.id, issueId, event.target.value)} />
      {!allowed && <p role="status" className="text-caption text-muted-foreground">{t(($) => $.execution_log.supplement_ended)}</p>}
      {failed && <p role="alert" className="text-caption text-destructive">{t(($) => $.execution_log.supplement_failed)}</p>}
      <DialogFooter>
        {!allowed && <><Button variant="outline" onClick={() => void copyText(draft.content)}>{t(($) => $.execution_log.supplement_copy)}</Button><Button variant="ghost" onClick={() => clear(task.id)}>{t(($) => $.execution_log.supplement_discard)}</Button></>}
        <Button disabled={!allowed || !draft.content.trim() || mutation.isPending} onClick={() => void send()}>{t(($) => $.execution_log.supplement_send)}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>;
}

export function TaskSupplementReceipt({ entry, issueId }: { entry: TimelineEntry; issueId: string }) {
  const { t } = useT("issues");
  const retry = useRetryTaskSupplement(issueId);
  const { data: tasks = [] } = useQuery({ queryKey: issueKeys.tasks(issueId), queryFn: () => api.listTasksByIssue(issueId), staleTime: 30000 });
  const [failed, setFailed] = useState(false);
  if (!entry.supplement_task_id || !entry.supplement_status) return null;
  const task = tasks.find((candidate) => candidate.id === entry.supplement_task_id);
  let label: string;
  if (entry.supplement_status === "delivered") label = t(($) => $.execution_log.supplement_delivered);
  else if (entry.supplement_status === "failed") {
    const reason = entry.supplement_failure_reason;
    const message = reason === "turn_ended" ? t(($) => $.execution_log.supplement_failure_turn_ended)
      : reason === "timeout" ? t(($) => $.execution_log.supplement_failure_timeout)
      : reason === "turn_not_started" ? t(($) => $.execution_log.supplement_failure_turn_not_started)
      : reason === "provider_rejected" ? t(($) => $.execution_log.supplement_failure_provider_rejected)
      : t(($) => $.execution_log.supplement_failure_unknown);
    label = t(($) => $.execution_log.supplement_not_delivered, { reason: message });
  } else label = t(($) => $.execution_log.supplement_waiting_delivery);
  return <div className="mt-1 flex items-center gap-2 text-micro text-muted-foreground" role="status">
    <span>{label}</span>
    {entry.supplement_status === "failed" && task && canSupplement(task) && <button type="button" className="underline underline-offset-2" disabled={retry.isPending} onClick={() => { setFailed(false); retry.mutate({ taskId: task.id, commentId: entry.id }, { onError: () => setFailed(true) }); }}>{t(($) => $.execution_log.supplement_retry)}</button>}
    {failed && <span role="alert">{t(($) => $.execution_log.supplement_retry_failed)}</span>}
  </div>;
}
