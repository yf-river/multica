"use client";

import { useId, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { respondTaskInteractionOptions, taskInteractionOptions } from "@multica/core/runtimes/interactions";
import type { TaskInteraction } from "@multica/core/types/task-interaction";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../i18n";

export function TaskInteractionPanel({ taskId, active, showEmpty = false }: { taskId: string; active: boolean; showEmpty?: boolean }) {
  const wsId = useWorkspaceId();
  const { t } = useT("common");
  const query = useQuery(taskInteractionOptions(wsId, taskId, active));
  if (query.isError || query.data?.available === false) {
    return <div role="alert" className="rounded-md border p-3 text-body">
      <p>{t(($) => $.runtime_interaction.load_error)}</p>
      <Button variant="outline" className="mt-2" onClick={() => void query.refetch()}>{t(($) => $.runtime_interaction.retry)}</Button>
    </div>;
  }
  const interactions = query.data?.interactions ?? [];
  if (interactions.length === 0) return showEmpty && !query.isPending ? <p className="text-body text-muted-foreground">{t(($) => $.runtime_interaction.empty)}</p> : null;
  return <div className="min-w-0 space-y-3">
    {interactions.filter((row) => row.status === "pending").map((row) => (
      <InteractionForm key={row.id} interaction={row} active={active} />
    ))}
    {interactions.some((row) => row.status !== "pending") && <details className="rounded-md border p-3 text-body">
      <summary className="cursor-pointer rounded py-1 text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring">{t(($) => $.runtime_interaction.history)}</summary>
      <div className="mt-3 space-y-3">
        {interactions.filter((row) => row.status !== "pending").map((row) => <div key={row.id} className="space-y-1 break-words">
          <p className="whitespace-pre-wrap">{row.prompt.title}</p>
          <p className="text-caption text-muted-foreground">{row.status === "responded" ? t(($) => $.runtime_interaction.saved) : row.status === "expired" ? t(($) => $.runtime_interaction.expired) : t(($) => $.runtime_interaction.unknown)}</p>
          {row.response && <p className="whitespace-pre-wrap">{row.response.decision === "deny" ? t(($) => $.runtime_interaction.declined) : Object.values(row.response.answers ?? {}).flat().join("、") || t(($) => $.runtime_interaction.allowed)}</p>}
        </div>)}
      </div>
    </details>}
  </div>;
}

function InteractionForm({ interaction, active }: { interaction: TaskInteraction; active: boolean }) {
  const { t } = useT("common");
  const wsId = useWorkspaceId();
  const client = useQueryClient();
  const mutation = useMutation(respondTaskInteractionOptions(client, wsId, interaction.taskId));
  const [selections, setSelections] = useState<Record<string, string[]>>({});
  const [texts, setTexts] = useState<Record<string, string>>({});
  const formId = useId();
  const { prompt } = interaction;
  const answers = Object.fromEntries(prompt.questions.map((q) => [q.id,
    texts[q.id]?.trim() ? [texts[q.id]!.trim()] : selections[q.id] ?? [],
  ]));
  const supported = ["question", "approval", "plan"].includes(prompt.kind) && ["codex", "codebuddy"].includes(interaction.provider);
  const disabled = !active || !supported || mutation.isPending;
  const complete = prompt.questions.every((q) => (answers[q.id]?.length ?? 0) > 0);
  return <form className="min-w-0 space-y-3 rounded-md border bg-card p-4" onSubmit={(event) => {
    event.preventDefault();
    if (!disabled && complete) mutation.mutate({ id: interaction.id, answer: { decision: "allow", ...(prompt.kind === "question" ? { answers } : {}) } });
  }}>
    <h3 className="whitespace-pre-wrap break-words text-body font-medium">{prompt.title}</h3>
    {prompt.questions.map((q, index) => <fieldset key={q.id} disabled={disabled} className="min-w-0 space-y-2">
      <legend className="mb-2 whitespace-pre-wrap break-words text-body">{q.question}</legend>
      {q.options.map((option, optionIndex) => <label key={optionIndex} className="flex min-h-11 cursor-pointer items-start gap-2 rounded border px-3 py-2 text-body">
        <input type={q.multiSelect ? "checkbox" : "radio"} name={`${formId}-${index}`} className="mt-1 shrink-0 accent-primary focus-visible:outline-2 focus-visible:outline-ring" checked={(selections[q.id] ?? []).includes(option.label) && !texts[q.id]?.trim()} onChange={(event) => {
          setTexts((value) => ({ ...value, [q.id]: "" }));
          setSelections((value) => ({ ...value, [q.id]: q.multiSelect ? event.target.checked ? [...(value[q.id] ?? []), option.label] : (value[q.id] ?? []).filter((label) => label !== option.label) : [option.label] }));
        }} />
        <span className="min-w-0 break-words"><span>{option.label}</span>{option.description && <span className="mt-1 block text-caption text-muted-foreground">{option.description}</span>}</span>
      </label>)}
      <label htmlFor={`${formId}-${index}-text`} className="block text-caption text-muted-foreground">{q.options.length ? t(($) => $.runtime_interaction.other_answer) : t(($) => $.runtime_interaction.answer)}</label>
      <Textarea autoComplete="off" name={`${formId}-${index}-answer`} id={`${formId}-${index}-text`} value={texts[q.id] ?? ""} maxLength={4096} onChange={(event) => setTexts((value) => ({ ...value, [q.id]: event.target.value }))} />
    </fieldset>)}
    {mutation.isError && <p role="alert" className="text-body text-destructive">{t(($) => $.runtime_interaction.save_error)}</p>}
    {!supported && <p role="alert" className="text-body text-muted-foreground">{t(($) => $.runtime_interaction.unsupported)}</p>}
    <div className="flex flex-wrap gap-2">
      <Button type="submit" disabled={disabled || !complete}>{mutation.isPending ? t(($) => $.runtime_interaction.saving) : prompt.kind === "question" ? t(($) => $.runtime_interaction.submit) : prompt.kind === "plan" ? t(($) => $.runtime_interaction.execute) : t(($) => $.runtime_interaction.allow)}</Button>
      <Button type="button" variant="outline" disabled={disabled} onClick={() => mutation.mutate({ id: interaction.id, answer: { decision: "deny" } })}>{t(($) => $.runtime_interaction.deny)}</Button>
    </div>
  </form>;
}

// 历史记录按需读取，避免打开列表就为每条旧任务请求问答正文。
export function TaskInteractionHistory({ taskId, active }: { taskId: string; active: boolean }) {
  const { t } = useT("common");
  const [expanded, setExpanded] = useState(false);
  return <details className="max-h-[40dvh] overflow-y-auto border-b px-4 py-2 text-body" onToggle={(event) => setExpanded(event.currentTarget.open)}>
    <summary className="cursor-pointer rounded py-1 text-muted-foreground focus-visible:outline-2 focus-visible:outline-ring">{t(($) => $.runtime_interaction.history)}</summary>
    {expanded && <div className="py-2"><TaskInteractionPanel taskId={taskId} active={active} showEmpty /></div>}
  </details>;
}
