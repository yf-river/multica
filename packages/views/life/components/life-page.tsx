"use client";

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, Check, Eye, Loader2, Pause, Play, Plus, RotateCcw, Save, Trash2, X } from "lucide-react";
import { api } from "@multica/core/api";
import { useChatStore } from "@multica/core/chat";
import {
  companionProfileOptions,
  lifeChronicleListOptions,
	lifeCognitionJobListOptions,
	lifeCommitmentListOptions,
  lifeExperimentListOptions,
	lifeIdentityListOptions,
  lifeKeys,
	lifeMaterialListOptions,
	lifeInternalThoughtListOptions,
  lifeMemoryListOptions,
	lifeModuleListOptions,
	lifeObservationSeatOptions,
	lifeObserverListOptions,
	lifeProactivePolicyOptions,
  lifeProactiveCheckListOptions,
  lifeProposalListOptions,
	lifeRelationshipListOptions,
	lifeTopicListOptions,
	lifeUpgradeEvaluationListOptions,
  useArchiveLifeMemory,
  useConfirmLifeMemory,
  useConfirmLifeProposal,
  useDeleteLifeMemory,
  useDowngradeLifeMemory,
  useReviewLifeExperimentRound,
  useStopLifeExperimentRound,
  useUpdateLifeMemory,
} from "@multica/core/life";
import { LIFE_TABS, useWorkspaceId, type LifeTab } from "@multica/core/paths";
import { agentListOptions } from "@multica/core/workspace/queries";
import type { LifeChronicleEntry, LifeExperiment, LifeExperimentRound, LifeIdentityVersion, LifeMemory } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { PageHeader } from "../../layout/page-header";
import { useT } from "../../i18n";
import { useNavigation } from "../../navigation";

function parseLifeTab(value: string | null): LifeTab {
  return LIFE_TABS.includes(value as LifeTab) ? (value as LifeTab) : "memory";
}

export function LifePage() {
  const { t } = useT("life");
  const navigation = useNavigation();
  const setOpen = useChatStore((state) => state.setOpen);
  const setExpanded = useChatStore((state) => state.setExpanded);
  const activeTab = parseLifeTab(navigation.searchParams.get("tab"));
  const tabCopy: Record<LifeTab, { label: string; description: string }> = {
    memory: {
      label: t(($) => $.page.memory),
      description: t(($) => $.page.memory_description),
    },
    experiment: {
      label: t(($) => $.page.experiment),
      description: t(($) => $.page.experiment_description),
    },
    observers: {
      label: t(($) => $.page.observers),
      description: t(($) => $.page.observers_description),
    },
    chronicle: {
      label: t(($) => $.page.chronicle),
      description: t(($) => $.page.chronicle_description),
    },
  };

  useEffect(() => {
    setExpanded(false);
    setOpen(false);
  }, [setExpanded, setOpen]);

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <PageHeader>
        <div className="flex min-w-0 items-center gap-2 text-sm">
          <h1 className="shrink-0 font-medium">{t(($) => $.page.title)}</h1>
          <span className="text-muted-foreground/50" aria-hidden="true">/</span>
          <span className="truncate text-muted-foreground">{tabCopy[activeTab].label}</span>
        </div>
      </PageHeader>
      <main className="min-h-0 flex-1 overflow-y-auto bg-muted/20">
        <div className="mx-auto w-full max-w-6xl px-4 py-6 md:px-8 md:py-8">
          <header className="mb-6 space-y-2">
            <p className="text-xs font-medium uppercase tracking-wide text-brand">{t(($) => $.page.title)}</p>
            <h2 className="text-base font-medium tracking-tight">{tabCopy[activeTab].label}</h2>
            <p className="max-w-2xl text-sm leading-6 text-muted-foreground">{tabCopy[activeTab].description}</p>
          </header>
          <div className="min-w-0">
            {activeTab === "memory" && <MemoryPanel />}
            {activeTab === "experiment" && <ExperimentPanel />}
            {activeTab === "observers" && <ObserversPanel />}
            {activeTab === "chronicle" && <ChroniclePanel />}
          </div>
        </div>
      </main>
    </div>
  );
}

function QueryLoading() {
  return <div className="flex items-center justify-center py-10 text-muted-foreground"><Loader2 className="size-4 animate-spin" /></div>;
}

function MemoryPanel() {
  const { t } = useT("life");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const query = useQuery(lifeMemoryListOptions(wsId));
  const identities = useQuery(lifeIdentityListOptions(wsId));
  const relationships = useQuery(lifeRelationshipListOptions(wsId));
  const topics = useQuery(lifeTopicListOptions(wsId));
  const commitments = useQuery(lifeCommitmentListOptions(wsId));
  const materials = useQuery(lifeMaterialListOptions(wsId));
  const thoughts = useQuery(lifeInternalThoughtListOptions(wsId));
  const [material, setMaterial] = useState("");
  const addMaterial = useMutation({ mutationFn: () => api.createLifeMaterial(material), onSuccess: () => setMaterial(""), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.materials(wsId) }) });
  const updateCommitment = useMutation({ mutationFn: ({ id, status }: { id: string; status: string }) => api.updateLifeCommitment(id, { status }), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.commitments(wsId) }) });
  const resolveEvent = useMutation({ mutationFn: (id: string) => api.resolveLifeRelationshipEvent(id, "resolved", "已由用户确认处理"), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.relationships(wsId) }) });
  const updateTopic = useMutation({ mutationFn: ({ id, status }: { id: string; status: string }) => api.updateLifeTopic(id, status), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.topics(wsId) }) });
  if ([query, identities, relationships, topics, commitments, materials, thoughts].some((item) => item.isLoading)) return <QueryLoading />;
  const memories = query.data?.memories ?? [];
  const activeIdentity = identities.data?.versions.find((version) => version.status === "active");
  return <div className="w-full max-w-5xl space-y-6 lg:grid lg:grid-cols-2 lg:gap-6 lg:space-y-0">
    <section className="space-y-3 rounded-lg border border-border/80 bg-background p-4 md:p-5 lg:col-span-2"><div><h2 className="text-sm font-medium">{t(($) => $.complete.identity_title)}</h2><p className="mt-1 text-xs text-muted-foreground">{t(($) => $.complete.identity_description)}</p></div>
      {activeIdentity ? <IdentitySummary identity={activeIdentity} /> : <p className="text-sm text-muted-foreground">{t(($) => $.complete.no_identity)}</p>}
      {(identities.data?.versions ?? []).filter((version) => version.status === "draft").map((version) => <IdentityDraftRow key={version.id} identity={version} />)}
      <IdentityEditor identity={activeIdentity} />
      {(relationships.data?.events ?? []).filter((event) => event.status !== "resolved").map((event) => <article key={event.id} className="rounded-lg border p-4"><div className="text-xs font-medium">{relationshipEventLabel(event.event_type, t)} · {relationshipStatusLabel(event.status, t)}</div><p className="mt-2 text-sm">{event.context}</p><div className="mt-3 grid gap-1 text-xs text-muted-foreground"><span>{t(($) => $.complete.your_position, { value: event.user_position || t(($) => $.complete.missing) })}</span><span>{t(($) => $.complete.companion_position, { value: event.companion_position || t(($) => $.complete.missing) })}</span></div><Button className="mt-3" size="sm" variant="outline" onClick={() => resolveEvent.mutate(event.id)}>{t(($) => $.complete.finish_review)}</Button></article>)}
    </section>
    <section className="space-y-3 rounded-lg border border-border/80 bg-background p-4 md:p-5"><div><h2 className="text-sm font-medium">{t(($) => $.complete.material_title)}</h2><p className="mt-1 text-xs text-muted-foreground">{t(($) => $.complete.material_description)}</p></div><Textarea value={material} onChange={(event) => setMaterial(event.target.value)} placeholder={t(($) => $.complete.material_placeholder)}/><Button size="sm" disabled={!material.trim() || addMaterial.isPending} onClick={() => addMaterial.mutate()}><Plus />{t(($) => $.complete.record)}</Button><p className="text-xs text-muted-foreground">{t(($) => $.complete.material_count, { count: materials.data?.materials.length ?? 0 })}</p></section>
    <section className="space-y-3 rounded-lg border border-border/80 bg-background p-4 md:p-5"><div><h2 className="text-sm font-medium">{t(($) => $.complete.thought_title)}</h2><p className="mt-1 text-xs text-muted-foreground">{t(($) => $.complete.thought_description)}</p></div>{(thoughts.data?.thoughts ?? []).length === 0 ? <p className="text-sm text-muted-foreground">{t(($) => $.complete.no_thoughts)}</p> : thoughts.data?.thoughts.map((thought) => <article key={thought.id} className="rounded-lg border p-4"><div className="flex items-center justify-between gap-3"><span className="text-sm font-medium">{thought.title}</span><span className="text-xs text-muted-foreground">{thought.thought_type}</span></div><p className="mt-2 text-sm text-muted-foreground">{thought.content}</p><p className="mt-2 text-xs text-muted-foreground">{t(($) => $.complete.last_developed, { time: new Date(thought.last_developed_at).toLocaleString() })}</p></article>)}</section>
    <section className="space-y-3 rounded-lg border border-border/80 bg-background p-4 md:p-5"><h2 className="text-sm font-medium">{t(($) => $.complete.topic_title)}</h2>{(topics.data?.topics ?? []).length === 0 ? <p className="text-sm text-muted-foreground">{t(($) => $.complete.no_topics)}</p> : topics.data?.topics.map((topic) => <article key={topic.id} className="rounded-lg border p-4"><div className="flex justify-between text-xs"><span className="font-medium">{topic.title}</span><span className="text-muted-foreground">{Math.round(topic.confidence * 100)}%</span></div><p className="mt-2 text-sm text-muted-foreground">{topic.summary}</p>{topic.status === "candidate" && <Button className="mt-3" size="sm" onClick={() => updateTopic.mutate({ id: topic.id, status: "active" })}>{t(($) => $.complete.confirm)}</Button>}{topic.status === "active" && <Button className="mt-3" size="sm" variant="outline" onClick={() => updateTopic.mutate({ id: topic.id, status: "resolved" })}>{t(($) => $.complete.complete)}</Button>}</article>)}</section>
    <section className="space-y-3 rounded-lg border border-border/80 bg-background p-4 md:p-5"><h2 className="text-sm font-medium">{t(($) => $.complete.commitment_title)}</h2>{(commitments.data?.commitments ?? []).length === 0 ? <p className="text-sm text-muted-foreground">{t(($) => $.complete.no_commitments)}</p> : commitments.data?.commitments.map((item) => <article key={item.id} className="flex items-start gap-3 rounded-lg border p-4"><div className="min-w-0 flex-1"><div className="text-sm">{item.content}</div><div className="mt-1 text-xs text-muted-foreground">{item.status}{item.due_at ? ` · ${new Date(item.due_at).toLocaleString()}` : ""}</div></div>{item.status === "candidate" && <Button size="sm" onClick={() => updateCommitment.mutate({ id: item.id, status: "confirmed" })}>{t(($) => $.complete.confirm)}</Button>}{item.status === "confirmed" && <Button size="sm" variant="outline" onClick={() => updateCommitment.mutate({ id: item.id, status: "completed" })}>{t(($) => $.complete.complete)}</Button>}</article>)}</section>
    <section className="space-y-3 rounded-lg border border-border/80 bg-background p-4 md:p-5 lg:col-span-2"><h2 className="text-sm font-medium">{t(($) => $.complete.memory_title)}</h2>{memories.length === 0 ? <p className="text-sm text-muted-foreground">{t(($) => $.memory.empty)}</p> : memories.map((memory) => <MemoryRow key={memory.id} memory={memory} />)}</section>
  </div>;
}

function IdentitySummary({ identity }: { identity: LifeIdentityVersion }) {
  const { t } = useT("life");
  const labels: Record<string, string> = {
    traits: t(($) => $.complete.identity_traits),
    position: t(($) => $.complete.identity_position),
    support: t(($) => $.complete.identity_support),
    conflict: t(($) => $.complete.identity_conflict),
    shared_change: t(($) => $.complete.identity_shared_change),
    reunion: t(($) => $.complete.identity_reunion),
    follow_up: t(($) => $.complete.identity_follow_up),
    memory: t(($) => $.complete.identity_memory),
    support_without_control: t(($) => $.complete.identity_support),
  };
  const sections = [
    [t(($) => $.complete.stable_core), identity.stable_core],
    [t(($) => $.complete.relationship_contract), identity.relationship_contract],
  ] as const;
  return <article className="rounded-lg border p-4"><div className="text-xs text-muted-foreground">{t(($) => $.complete.current_identity, { version: identity.version })}</div><div className="mt-3 grid gap-3">{sections.map(([title, values]) => <div key={title}><div className="text-xs font-medium">{title}</div><dl className="mt-1 grid gap-1 text-sm">{Object.entries(values).map(([key, value]) => <div key={key} className="grid grid-cols-[7rem_1fr] gap-2"><dt className="text-muted-foreground">{labels[key] ?? key.replaceAll("_", " ")}</dt><dd>{formatIdentityValue(value, t)}</dd></div>)}</dl></div>)}</div></article>;
}

function formatIdentityValue(value: unknown, t: ReturnType<typeof useT<"life">>["t"]): string {
  if (Array.isArray(value)) return value.map((item) => formatIdentityValue(item, t)).join("、");
  if (value && typeof value === "object") return Object.values(value).map((item) => formatIdentityValue(item, t)).join("；");
  if (typeof value === "boolean") return value ? t(($) => $.complete.yes) : t(($) => $.complete.no);
  return String(value ?? "");
}

function relationshipEventLabel(value: string, t: ReturnType<typeof useT<"life">>["t"]): string {
  const labels: Record<string, string> = {
    conflict: t(($) => $.complete.relationship_conflict),
    agreement: t(($) => $.complete.relationship_agreement),
    boundary: t(($) => $.complete.relationship_boundary),
    reunion: t(($) => $.complete.relationship_reunion),
  };
  return labels[value] ?? value;
}

function relationshipStatusLabel(value: string, t: ReturnType<typeof useT<"life">>["t"]): string {
  const labels: Record<string, string> = {
    open: t(($) => $.complete.relationship_open),
    waiting: t(($) => $.complete.relationship_waiting),
    retained_difference: t(($) => $.complete.relationship_retained_difference),
  };
  return labels[value] ?? value;
}

function IdentityDraftRow({ identity }: { identity: LifeIdentityVersion }) {
  const { t } = useT("life");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const activate = useMutation({ mutationFn: () => api.activateLifeIdentityVersion(identity.id), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.identity(wsId) }) });
  return <article className="flex items-center gap-3 rounded-lg border border-dashed p-4"><div className="min-w-0 flex-1"><div className="text-sm font-medium">{t(($) => $.complete.identity_draft, { version: identity.version })}</div><div className="truncate text-xs text-muted-foreground">{identity.change_reason}</div></div><Button size="sm" disabled={activate.isPending} onClick={() => activate.mutate()}><Check />{t(($) => $.complete.activate)}</Button></article>;
}

function IdentityEditor({ identity }: { identity?: LifeIdentityVersion }) {
  const { t } = useT("life");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [stableCore, setStableCore] = useState(() => JSON.stringify(identity?.stable_core ?? {}, null, 2));
  const [contract, setContract] = useState(() => JSON.stringify(identity?.relationship_contract ?? {}, null, 2));
  const [growth, setGrowth] = useState(() => JSON.stringify(identity?.growth_profile ?? {}, null, 2));
  const [expression, setExpression] = useState(() => JSON.stringify(identity?.expression_profile ?? {}, null, 2));
  const [interests, setInterests] = useState(() => JSON.stringify(identity?.interests ?? [], null, 2));
  const create = useMutation({ mutationFn: () => api.createLifeIdentityVersion({ stable_core: JSON.parse(stableCore), relationship_contract: JSON.parse(contract), growth_profile: JSON.parse(growth), expression_profile: JSON.parse(expression), interests: JSON.parse(interests), change_reason: reason }), onSuccess: () => { setOpen(false); setReason(""); }, onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.identity(wsId) }) });
  const fields: Array<[string, string, (value: string) => void]> = [[t(($) => $.complete.stable_core), stableCore, setStableCore], [t(($) => $.complete.relationship_contract), contract, setContract], [t(($) => $.complete.growth_profile), growth, setGrowth], [t(($) => $.complete.expression_profile), expression, setExpression], [t(($) => $.complete.interests), interests, setInterests]];
  return <details open={open} onToggle={(event) => setOpen(event.currentTarget.open)} className="rounded-lg border p-4"><summary className="cursor-pointer text-sm font-medium">{t(($) => $.complete.edit_identity)}</summary><div className="mt-4 grid gap-2"><p className="text-xs text-muted-foreground">{t(($) => $.complete.identity_draft_note)}</p><Input placeholder={t(($) => $.complete.change_reason)} value={reason} onChange={(event) => setReason(event.target.value)}/>{fields.map(([label, value, setter]) => <label key={label} className="space-y-1 text-xs"><span>{label}</span><Textarea value={value} onChange={(event) => setter(event.target.value)} /></label>)}<Button size="sm" disabled={!reason.trim() || create.isPending} onClick={() => create.mutate()}>{t(($) => $.complete.save_identity_draft)}</Button></div></details>;
}

function MemoryRow({ memory }: { memory: LifeMemory }) {
  const { t } = useT("life");
  const [editing, setEditing] = useState(false);
  const [content, setContent] = useState(memory.content);
  const [uncertainty, setUncertainty] = useState(memory.uncertainty);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [showHistory, setShowHistory] = useState(false);
  const history = useQuery({ queryKey: ["life-memory-revisions", memory.id], queryFn: () => api.listLifeMemoryRevisions(memory.id), enabled: showHistory });
  const confirm = useConfirmLifeMemory();
  const update = useUpdateLifeMemory();
  const downgrade = useDowngradeLifeMemory();
  const archive = useArchiveLifeMemory();
  const remove = useDeleteLifeMemory();
  const busy = confirm.isPending || update.isPending || downgrade.isPending || archive.isPending || remove.isPending;
  const statusLabel = memory.status === "candidate"
    ? t(($) => $.memory.candidate)
    : memory.status === "confirmed"
      ? t(($) => $.memory.confirmed)
      : t(($) => $.memory.archived);
  return (
    <article className="space-y-3 rounded-lg border p-4">
      <div className="flex items-center justify-between gap-3">
        <span className="text-xs font-medium text-muted-foreground">{statusLabel} · {memory.kind}</span>
        <span className="text-xs text-muted-foreground">{t(($) => $.memory.confidence, { value: Math.round(memory.confidence * 100) })}</span>
      </div>
      {editing ? (
        <div className="space-y-2">
          <Textarea value={content} onChange={(event) => setContent(event.target.value)} />
          <Input value={uncertainty} onChange={(event) => setUncertainty(event.target.value)} />
        </div>
      ) : (
        <p className="text-sm leading-6">{memory.content}</p>
      )}
      {memory.uncertainty && !editing && <p className="text-xs text-muted-foreground">{t(($) => $.memory.uncertainty, { value: memory.uncertainty })}</p>}
      {memory.evidence.length > 0 && (
        <details className="text-xs text-muted-foreground">
          <summary className="cursor-pointer">{t(($) => $.memory.evidence, { count: memory.evidence.length })}</summary>
          <div className="mt-2 space-y-1">{memory.evidence.map((evidence) => <p key={`${evidence.source_type}:${evidence.source_id}`}>{evidence.excerpt}</p>)}</div>
        </details>
      )}
      <details open={showHistory} onToggle={(event) => setShowHistory(event.currentTarget.open)} className="text-xs text-muted-foreground"><summary className="cursor-pointer">{history.data ? t(($) => $.memory.history_count, { count: history.data.revisions.length }) : t(($) => $.memory.history)}</summary>{history.data && <div className="mt-2 space-y-2">{history.data.revisions.map((revision) => <div key={String(revision.id)}>{String(revision.change_type)} · {String(revision.change_reason)} · {String(revision.content)}</div>)}</div>}</details>
      <div className="flex flex-wrap items-center gap-2">
        {memory.status === "candidate" && <Button size="sm" disabled={busy} onClick={() => confirm.mutate(memory.id)}><Check />{t(($) => $.memory.confirm)}</Button>}
        {editing ? (
          <>
            <Button size="sm" disabled={busy || !content.trim()} onClick={() => update.mutate({ id: memory.id, data: { kind: memory.kind, content, uncertainty, confidence: memory.confidence, urgency: memory.urgency, valid_from: memory.valid_from ?? undefined, valid_to: memory.valid_to ?? undefined } }, { onSuccess: () => setEditing(false) })}><Save />{t(($) => $.memory.save)}</Button>
            <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>{t(($) => $.memory.cancel)}</Button>
          </>
        ) : <Button size="sm" variant="outline" disabled={busy} onClick={() => setEditing(true)}>{t(($) => $.memory.correct)}</Button>}
        {memory.kind !== "current_expression" && <Button size="sm" variant="ghost" disabled={busy} onClick={() => downgrade.mutate({ id: memory.id, kind: "current_expression" })}>{t(($) => $.memory.downgrade)}</Button>}
        {memory.status !== "archived" && <Button size="sm" variant="ghost" disabled={busy} onClick={() => archive.mutate(memory.id)}><Archive />{t(($) => $.memory.archive)}</Button>}
        {!confirmingDelete ? (
          <Button size="sm" variant="ghost" className="text-destructive" disabled={busy} onClick={() => setConfirmingDelete(true)}><Trash2 />{t(($) => $.memory.delete)}</Button>
        ) : (
          <div className="flex flex-wrap items-center gap-2 text-xs text-destructive">
            <span>{t(($) => $.memory.delete_question)}</span>
            <Button size="sm" variant="destructive" disabled={busy} onClick={() => remove.mutate(memory.id)}>{t(($) => $.memory.delete_confirm)}</Button>
            <Button size="sm" variant="ghost" onClick={() => setConfirmingDelete(false)}>{t(($) => $.memory.cancel)}</Button>
          </div>
        )}
      </div>
    </article>
  );
}

function ExperimentPanel() {
  const { t } = useT("life");
  const wsId = useWorkspaceId();
  const proposalsQuery = useQuery(lifeProposalListOptions(wsId));
  const experimentsQuery = useQuery(lifeExperimentListOptions(wsId));
  const modulesQuery = useQuery(lifeModuleListOptions(wsId));
  const qc = useQueryClient();
  const confirm = useConfirmLifeProposal();
  const reject = useMutation({ mutationFn: (id: string) => api.rejectLifeProposal(id), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.proposals(wsId) }) });
  const updateModule = useMutation({ mutationFn: ({ id, status }: { id: string; status: string }) => api.updateLifeModule(id, status), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.modules(wsId) }) });
  if (proposalsQuery.isLoading || experimentsQuery.isLoading || modulesQuery.isLoading) return <QueryLoading />;
  const pending = (proposalsQuery.data?.proposals ?? []).filter((proposal) => proposal.status === "pending_confirmation");
  const experiments = experimentsQuery.data?.experiments ?? [];
  const rounds = experimentsQuery.data?.rounds ?? [];
  const modules = modulesQuery.data?.modules ?? [];
  if (pending.length === 0 && experiments.length === 0 && modules.length === 0) return <p className="text-sm text-muted-foreground">{t(($) => $.experiment.none)}</p>;
  return (
    <div className="w-full max-w-5xl space-y-6">
      {pending.length > 0 && <section className="space-y-3">
        <h2 className="text-sm font-medium">{t(($) => $.experiment.pending)}</h2>
        {pending.map((proposal) => <article key={proposal.id} className="space-y-2 rounded-lg border p-4"><div className="text-xs text-muted-foreground">{proposal.proposal_type}</div><div className="text-sm font-medium">{proposal.title}</div><p className="text-sm text-muted-foreground">{proposal.summary}</p>{proposal.payload.hypothesis && <p className="text-xs text-muted-foreground">{proposal.payload.hypothesis}</p>}<div className="flex gap-2"><Button size="sm" disabled={confirm.isPending} onClick={() => confirm.mutate(proposal.id)}><Check />{t(($) => $.complete.confirm_execute)}</Button><Button size="sm" variant="outline" disabled={reject.isPending} onClick={() => reject.mutate(proposal.id)}><X />{t(($) => $.complete.reject)}</Button></div></article>)}
      </section>}
      {experiments.map((experiment) => <ExperimentRows key={experiment.id} experiment={experiment} rounds={rounds.filter((round) => round.experiment_id === experiment.id)} />)}
      {modules.length > 0 && <section className="space-y-3"><h2 className="text-sm font-medium">{t(($) => $.complete.modules_title)}</h2>{modules.map((module) => <article key={module.id} className="flex items-center gap-3 rounded-lg border p-4"><div className="min-w-0 flex-1"><div className="text-sm font-medium">{module.name}</div><div className="text-xs text-muted-foreground">{t(($) => $.complete.module_status, { status: module.status, version: module.current_version })}</div></div>{module.status === "active" ? <Button size="sm" variant="outline" onClick={() => updateModule.mutate({ id: module.id, status: "paused" })}><Pause />{t(($) => $.complete.pause)}</Button> : <Button size="sm" onClick={() => updateModule.mutate({ id: module.id, status: "active" })}><Play />{t(($) => $.complete.enable)}</Button>}</article>)}</section>}
    </div>
  );
}

function ExperimentRows({ experiment, rounds }: { experiment: LifeExperiment; rounds: LifeExperimentRound[] }) {
  return <section className="space-y-3"><div><h2 className="text-sm font-medium">{experiment.title}</h2><p className="mt-1 text-xs text-muted-foreground">{experiment.hypothesis}</p></div>{rounds.map((round) => <ExperimentRoundRow key={round.id} experiment={experiment} round={round} />)}</section>;
}

function ExperimentRoundRow({ experiment, round }: { experiment: LifeExperiment; round: LifeExperimentRound }) {
  const { t } = useT("life");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const stop = useStopLifeExperimentRound();
  const review = useReviewLifeExperimentRound();
  const [showReview, setShowReview] = useState(false);
  const [draft, setDraft] = useState({ outcome: round.review_draft?.outcome ?? "", feelings: round.review_draft?.feelings ?? "", burden: round.review_draft?.burden ?? "", companion_correction: round.review_draft?.companion_correction ?? "" });
  const rerun = useMutation({
    mutationFn: () => {
      const startsAt = new Date(Date.now() + 60_000);
      const priorDuration = round.starts_at && round.ends_at ? Math.max(new Date(round.ends_at).getTime() - new Date(round.starts_at).getTime(), 86_400_000) : 7 * 86_400_000;
      return api.createLifeProposal({
        proposal_type: "experiment_extend", title: `${experiment.title}（新一轮）`, summary: "基于上一轮复盘准备的新一轮，确认后才会启动。",
        payload: { experiment_id: experiment.id, previous_round_id: round.id, problem: experiment.problem, hypothesis: experiment.hypothesis, method: experiment.method, plan: round.plan, starts_at: startsAt.toISOString(), ends_at: new Date(startsAt.getTime() + priorDuration).toISOString(), memory_ids: [], issue_title: `再次执行：${experiment.title}` },
      });
    },
    onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.proposals(wsId) }),
  });
  const statusLabel = round.status === "running" ? t(($) => $.experiment.running) : round.status === "awaiting_review" ? t(($) => $.experiment.awaiting_review) : round.status === "reviewed" ? t(($) => $.experiment.reviewed) : round.status;
  return (
    <article className="space-y-3 rounded-lg border p-4">
      <div className="flex items-center justify-between"><span className="text-xs font-medium">{statusLabel}</span><span className="text-xs text-muted-foreground">{round.ends_at ? new Date(round.ends_at).toLocaleString() : ""}</span></div>
      {round.review && <div className="space-y-1 text-sm"><p>{round.review.outcome}</p><p className="text-xs text-muted-foreground">{round.review.feelings} · {round.review.burden}</p><p className="text-xs text-muted-foreground">{round.review.companion_correction}</p></div>}
      {round.observations.length > 0 && <details className="text-xs text-muted-foreground"><summary className="cursor-pointer">{t(($) => $.complete.observations, { count: round.observations.length })}</summary><div className="mt-2 space-y-2">{round.observations.map((item) => <div key={item.id}><span className="font-medium">{item.observation_type}</span> · {item.content}</div>)}</div></details>}
      {round.review_draft && !round.review && <p className="rounded-md bg-muted p-3 text-xs text-muted-foreground">{t(($) => $.complete.review_draft_ready)}</p>}
      {showReview && <div className="grid gap-2"><Textarea placeholder={t(($) => $.experiment.outcome)} value={draft.outcome} onChange={(e) => setDraft({ ...draft, outcome: e.target.value })} /><Textarea placeholder={t(($) => $.experiment.feelings)} value={draft.feelings} onChange={(e) => setDraft({ ...draft, feelings: e.target.value })} /><Textarea placeholder={t(($) => $.experiment.burden)} value={draft.burden} onChange={(e) => setDraft({ ...draft, burden: e.target.value })} /><Textarea placeholder={t(($) => $.experiment.correction)} value={draft.companion_correction} onChange={(e) => setDraft({ ...draft, companion_correction: e.target.value })} /><Button size="sm" disabled={review.isPending || Object.values(draft).some((value) => !value.trim())} onClick={() => review.mutate({ id: round.id, ...draft }, { onSuccess: () => setShowReview(false) })}>{t(($) => $.experiment.review)}</Button></div>}
      <div className="flex flex-wrap gap-2">
        {round.status === "running" && <Button size="sm" variant="outline" disabled={stop.isPending} onClick={() => stop.mutate({ id: round.id, reason: "stopped_by_user" })}>{t(($) => $.experiment.stop)}</Button>}
        {round.status === "awaiting_review" && !showReview && <Button size="sm" onClick={() => setShowReview(true)}>{t(($) => $.experiment.review)}</Button>}
        {(round.status === "reviewed" || round.status === "awaiting_review") && <Button size="sm" variant="outline" disabled={rerun.isPending} onClick={() => rerun.mutate()}><RotateCcw />{t(($) => $.experiment.rerun)}</Button>}
      </div>
    </article>
  );
}

function ObserversPanel() {
  const { t } = useT("life");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const checksQuery = useQuery(lifeProactiveCheckListOptions(wsId));
  const policyQuery = useQuery(lifeProactivePolicyOptions(wsId));
  const observersQuery = useQuery(lifeObserverListOptions(wsId));
  const companionQuery = useQuery(companionProfileOptions(wsId));
  const seatQuery = useQuery(lifeObservationSeatOptions(wsId));
  const agentsQuery = useQuery(agentListOptions(wsId));
  const jobsQuery = useQuery(lifeCognitionJobListOptions(wsId));
  const upgradesQuery = useQuery(lifeUpgradeEvaluationListOptions(wsId));
  const [observerDraft, setObserverDraft] = useState({ agent_id: "", name: "", basis_type: "virtual", personality: "{}", perspective: "{}", expression_profile: "{}" });
  const [knowledgeDraft, setKnowledgeDraft] = useState({ observer_id: "", title: "", content: "" });
  const [observerVersionDraft, setObserverVersionDraft] = useState({ observer_id: "", personality: "{}", perspective: "{}", expression_profile: "{}", change_reason: "" });
  const [intervalHours, setIntervalHours] = useState(12);
  const [quietHours, setQuietHours] = useState("{}");
  const [upgradeDraft, setUpgradeDraft] = useState({ candidate_label: "", baseline_label: "当前版本", scenarios: "[]" });
  const createObserver = useMutation({ mutationFn: () => api.createLifeObserver({ ...observerDraft, personality: JSON.parse(observerDraft.personality), perspective: JSON.parse(observerDraft.perspective), expression_profile: JSON.parse(observerDraft.expression_profile) }), onSuccess: () => setObserverDraft({ agent_id: "", name: "", basis_type: "virtual", personality: "{}", perspective: "{}", expression_profile: "{}" }), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.observers(wsId) }) });
  const updateObserver = useMutation({ mutationFn: ({ id, status }: { id: string; status: string }) => api.updateLifeObserver(id, status), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.observers(wsId) }) });
  const runObserver = useMutation({ mutationFn: (id: string) => api.runLifeObserver(id), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.jobs(wsId) }) });
  const addKnowledge = useMutation({ mutationFn: () => api.addLifeObserverKnowledge(knowledgeDraft.observer_id, { title: knowledgeDraft.title, content: knowledgeDraft.content }), onSuccess: () => setKnowledgeDraft({ observer_id: "", title: "", content: "" }), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.observers(wsId) }) });
  const createObserverVersion = useMutation({ mutationFn: () => api.createLifeObserverVersion(observerVersionDraft.observer_id, { personality: JSON.parse(observerVersionDraft.personality), perspective: JSON.parse(observerVersionDraft.perspective), expression_profile: JSON.parse(observerVersionDraft.expression_profile), change_reason: observerVersionDraft.change_reason }), onSuccess: () => setObserverVersionDraft({ observer_id: "", personality: "{}", perspective: "{}", expression_profile: "{}", change_reason: "" }), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.observers(wsId) }) });
  useEffect(() => { if (policyQuery.data) { setIntervalHours(policyQuery.data.minimum_interval_hours); setQuietHours(JSON.stringify(policyQuery.data.quiet_hours, null, 2)); } }, [policyQuery.data]);
  const updatePolicy = useMutation({ mutationFn: (enabled: boolean) => api.updateLifeProactivePolicy({ enabled, timezone: policyQuery.data?.timezone ?? "Asia/Shanghai", quiet_hours: JSON.parse(quietHours), minimum_interval_hours: intervalHours }), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.policy(wsId) }) });
  const createUpgrade = useMutation({ mutationFn: () => api.createLifeUpgradeEvaluation({ candidate_label: upgradeDraft.candidate_label, baseline_label: upgradeDraft.baseline_label, scenarios: JSON.parse(upgradeDraft.scenarios) }), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.upgrades(wsId) }) });
  const retryJob = useMutation({ mutationFn: (id: string) => api.retryLifeCognitionJob(id), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.jobs(wsId) }) });
  const resolveObservationTopic = useMutation({ mutationFn: (id: string) => api.updateLifeObservationTopic(id, { status: "resolved" }), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.observationSeat(wsId) }) });
  if ([checksQuery, policyQuery, observersQuery, companionQuery, seatQuery, agentsQuery, jobsQuery, upgradesQuery].some((query) => query.isLoading)) return <QueryLoading />;
  const checks = checksQuery.data?.checks ?? [];
  const observers = observersQuery.data?.observers ?? [];
  const jobs = jobsQuery.data?.jobs ?? [];
  return <div className="w-full max-w-5xl space-y-6">
    <section className="space-y-3"><div><h2 className="text-sm font-medium">{t(($) => $.complete.proactive_title)}</h2><p className="mt-1 text-xs text-muted-foreground">{t(($) => $.complete.proactive_description)}</p></div><div className="grid gap-3 rounded-lg border p-4"><div className="flex flex-wrap items-end gap-3"><label className="space-y-1 text-xs"><span>{t(($) => $.complete.minimum_interval)}</span><Input className="w-28" type="number" min={1} value={intervalHours} onChange={(event) => setIntervalHours(Number(event.target.value))}/></label><Button size="sm" variant={policyQuery.data?.enabled ? "outline" : "default"} onClick={() => updatePolicy.mutate(!policyQuery.data?.enabled)}>{policyQuery.data?.enabled ? t(($) => $.complete.pause_proactive) : t(($) => $.complete.enable_proactive)}</Button><span className="text-xs text-muted-foreground">{t(($) => $.complete.next_check, { value: policyQuery.data?.next_check_at ? new Date(policyQuery.data.next_check_at).toLocaleString() : "—" })}</span></div><Textarea placeholder={t(($) => $.complete.quiet_hours)} value={quietHours} onChange={(event) => setQuietHours(event.target.value)}/><Button className="w-fit" size="sm" variant="outline" onClick={() => updatePolicy.mutate(policyQuery.data?.enabled ?? true)}>{t(($) => $.complete.save_policy)}</Button></div></section>
    <section className="space-y-3"><div><h2 className="text-sm font-medium">{t(($) => $.complete.perspectives_title)}</h2><p className="mt-1 text-xs text-muted-foreground">{t(($) => $.complete.perspectives_description)}</p></div>
      {observers.map((observer) => <article key={observer.id} className="space-y-3 rounded-lg border p-4"><div className="flex items-center gap-3"><div className="min-w-0 flex-1"><div className="text-sm font-medium">{observer.name}</div><div className="text-xs text-muted-foreground">{t(($) => $.complete.observer_meta, { basis: observer.basis_type, status: observer.status, count: observer.knowledge.length })}</div></div><Button size="sm" variant="outline" onClick={() => runObserver.mutate(observer.id)}><Eye />{t(($) => $.complete.run_now)}</Button><Button size="sm" variant="ghost" onClick={() => updateObserver.mutate({ id: observer.id, status: observer.status === "active" ? "paused" : "active" })}>{observer.status === "active" ? t(($) => $.complete.pause) : t(($) => $.complete.enable)}</Button></div><details><summary className="cursor-pointer text-xs text-muted-foreground">{t(($) => $.complete.identity_and_perspective)}</summary><pre className="mt-2 overflow-auto whitespace-pre-wrap text-xs">{JSON.stringify({ personality: observer.personality, perspective: observer.perspective }, null, 2)}</pre></details><div className="flex gap-2"><Button size="sm" variant="ghost" onClick={() => setKnowledgeDraft({ observer_id: observer.id, title: "", content: "" })}><Plus />{t(($) => $.complete.add_knowledge)}</Button><Button size="sm" variant="ghost" onClick={() => setObserverVersionDraft({ observer_id: observer.id, personality: JSON.stringify(observer.personality, null, 2), perspective: JSON.stringify(observer.perspective, null, 2), expression_profile: JSON.stringify(observer.expression_profile, null, 2), change_reason: "" })}>{t(($) => $.complete.edit_identity)}</Button></div></article>)}
      {knowledgeDraft.observer_id && <div className="space-y-2 rounded-lg border p-4"><Input placeholder={t(($) => $.complete.knowledge_title)} value={knowledgeDraft.title} onChange={(event) => setKnowledgeDraft({ ...knowledgeDraft, title: event.target.value })}/><Textarea placeholder={t(($) => $.complete.knowledge_content)} value={knowledgeDraft.content} onChange={(event) => setKnowledgeDraft({ ...knowledgeDraft, content: event.target.value })}/><Button size="sm" disabled={!knowledgeDraft.title.trim() || !knowledgeDraft.content.trim()} onClick={() => addKnowledge.mutate()}>{t(($) => $.complete.save_knowledge)}</Button></div>}
      {observerVersionDraft.observer_id && <div className="grid gap-2 rounded-lg border p-4"><Input placeholder={t(($) => $.complete.change_reason)} value={observerVersionDraft.change_reason} onChange={(event) => setObserverVersionDraft({ ...observerVersionDraft, change_reason: event.target.value })}/><Textarea placeholder={t(($) => $.complete.personality_json)} value={observerVersionDraft.personality} onChange={(event) => setObserverVersionDraft({ ...observerVersionDraft, personality: event.target.value })}/><Textarea placeholder={t(($) => $.complete.perspective_json)} value={observerVersionDraft.perspective} onChange={(event) => setObserverVersionDraft({ ...observerVersionDraft, perspective: event.target.value })}/><Textarea placeholder={t(($) => $.complete.expression_json)} value={observerVersionDraft.expression_profile} onChange={(event) => setObserverVersionDraft({ ...observerVersionDraft, expression_profile: event.target.value })}/><Button size="sm" disabled={!observerVersionDraft.change_reason.trim() || createObserverVersion.isPending} onClick={() => createObserverVersion.mutate()}>{t(($) => $.complete.activate)}</Button></div>}
      <details className="rounded-lg border p-4"><summary className="cursor-pointer text-sm font-medium">{t(($) => $.complete.create_observer)}</summary><div className="mt-4 grid gap-2"><select className="h-9 rounded-md border bg-background px-3 text-sm" value={observerDraft.agent_id} onChange={(event) => setObserverDraft({ ...observerDraft, agent_id: event.target.value })}><option value="">{t(($) => $.complete.choose_agent)}</option>{(agentsQuery.data ?? []).filter((agent) => !agent.archived_at && agent.id !== companionQuery.data?.profile?.agent_id).map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select><Input placeholder={t(($) => $.complete.observer_name)} value={observerDraft.name} onChange={(event) => setObserverDraft({ ...observerDraft, name: event.target.value })}/><select className="h-9 rounded-md border bg-background px-3 text-sm" value={observerDraft.basis_type} onChange={(event) => setObserverDraft({ ...observerDraft, basis_type: event.target.value })}><option value="real_person">{t(($) => $.complete.real_person)}</option><option value="reconstructed">{t(($) => $.complete.reconstructed)}</option><option value="virtual">{t(($) => $.complete.virtual)}</option></select><Textarea placeholder={t(($) => $.complete.personality_json)} value={observerDraft.personality} onChange={(event) => setObserverDraft({ ...observerDraft, personality: event.target.value })}/><Textarea placeholder={t(($) => $.complete.perspective_json)} value={observerDraft.perspective} onChange={(event) => setObserverDraft({ ...observerDraft, perspective: event.target.value })}/><Textarea placeholder={t(($) => $.complete.expression_json)} value={observerDraft.expression_profile} onChange={(event) => setObserverDraft({ ...observerDraft, expression_profile: event.target.value })}/><Button size="sm" disabled={!observerDraft.agent_id || !observerDraft.name.trim() || createObserver.isPending} onClick={() => createObserver.mutate()}><Plus />{t(($) => $.complete.create)}</Button></div></details>
    </section>
    <section className="space-y-3"><h2 className="text-sm font-medium">{t(($) => $.complete.observation_topics)}</h2>{(seatQuery.data?.topics ?? []).map((topic) => <article key={topic.id} className="rounded-lg border p-4"><div className="text-xs font-medium">{topic.title} · {topic.status}</div><p className="mt-2 text-sm">{topic.summary}</p>{topic.status !== "resolved" && <Button className="mt-3" size="sm" variant="outline" onClick={() => resolveObservationTopic.mutate(topic.id)}>{t(($) => $.complete.complete)}</Button>}</article>)}{(seatQuery.data?.judgements ?? []).map((judgement) => <article key={judgement.id} className="rounded-lg border p-4"><div className="text-xs text-muted-foreground">{judgement.observer_name} · {judgement.status} · {Math.round(judgement.confidence * 100)}%</div><div className="mt-2 text-sm font-medium">{judgement.title}</div><p className="mt-1 text-sm text-muted-foreground">{judgement.content}</p></article>)}{(seatQuery.data?.topics.length ?? 0) === 0 && (seatQuery.data?.judgements.length ?? 0) === 0 && <p className="text-sm text-muted-foreground">{t(($) => $.observers.empty)}</p>}</section>
    <section className="space-y-3"><h2 className="text-sm font-medium">{t(($) => $.observers.checks)}</h2>{checks.slice(0, 20).map((check) => <article key={check.id} className="rounded-lg border p-4"><div className="text-xs font-medium">{check.status === "silent" ? t(($) => $.observers.silent) : t(($) => $.observers.spoke)}</div><p className="mt-2 text-sm text-muted-foreground">{check.reason}</p>{check.message && <p className="mt-2 text-sm">{check.message}</p>}{check.value_assessment && <p className="mt-2 text-xs text-muted-foreground">{check.value_assessment}</p>}</article>)}</section>
    <section className="space-y-3"><h2 className="text-sm font-medium">{t(($) => $.complete.background_title)}</h2>{jobs.length === 0 && <p className="text-xs text-muted-foreground">{t(($) => $.complete.none)}</p>}{jobs.slice(0, 8).map((job) => <article key={job.id} className="flex items-start gap-3 rounded-lg border p-3"><div className="min-w-0 flex-1"><div className="text-xs font-medium">{job.job_type} · {job.status}</div><p className="mt-1 text-xs text-muted-foreground">{t(($) => $.complete.job_attempts, { attempt: job.attempt, max: job.max_attempts })}</p>{job.status === "cancelled" && <p className="mt-1 text-xs text-destructive">{t(($) => $.complete.job_exhausted)}</p>}</div>{job.status === "cancelled" && <Button size="sm" variant="outline" disabled={retryJob.isPending} onClick={() => retryJob.mutate(job.id)}><RotateCcw />{t(($) => $.complete.retry_job)}</Button>}</article>)}{(upgradesQuery.data?.evaluations ?? []).map((item) => <article key={item.id} className="rounded-lg border p-4 text-sm"><div>{t(($) => $.complete.comparison, { candidate: item.candidate_label, baseline: item.baseline_label })}</div><div className="mt-1 text-xs text-muted-foreground">{item.status}{item.rollback_recommended ? ` · ${t(($) => $.complete.rollback)}` : ""}</div></article>)}<details className="rounded-lg border p-4"><summary className="cursor-pointer text-sm">{t(($) => $.complete.upgrade_title)}</summary><div className="mt-3 grid gap-2"><Input placeholder={t(($) => $.complete.candidate_label)} value={upgradeDraft.candidate_label} onChange={(event) => setUpgradeDraft({ ...upgradeDraft, candidate_label: event.target.value })}/><Input placeholder={t(($) => $.complete.baseline_label)} value={upgradeDraft.baseline_label} onChange={(event) => setUpgradeDraft({ ...upgradeDraft, baseline_label: event.target.value })}/><Textarea placeholder={t(($) => $.complete.scenarios_json)} value={upgradeDraft.scenarios} onChange={(event) => setUpgradeDraft({ ...upgradeDraft, scenarios: event.target.value })}/><Button size="sm" disabled={!upgradeDraft.candidate_label.trim() || createUpgrade.isPending} onClick={() => createUpgrade.mutate()}>{t(($) => $.complete.start_upgrade)}</Button></div></details></section>
  </div>;
}

function ChroniclePanel() {
  const { t } = useT("life");
  const wsId = useWorkspaceId();
  const query = useQuery(lifeChronicleListOptions(wsId));
  if (query.isLoading) return <QueryLoading />;
  const entries = query.data?.entries ?? [];
  if (entries.length === 0) return <p className="text-sm text-muted-foreground">{t(($) => $.chronicle.empty)}</p>;
  return <div className="w-full max-w-4xl space-y-4">{entries.map((entry) => <ChronicleRow key={entry.id} entry={entry} />)}</div>;
}

function ChronicleRow({ entry }: { entry: LifeChronicleEntry }) {
  const { t } = useT("life");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const [later, setLater] = useState(entry.understanding_later);
  const [editing, setEditing] = useState(!entry.understanding_later);
  const update = useMutation({ mutationFn: () => api.updateLifeChronicleLaterUnderstanding(entry.id, later), onSuccess: () => setEditing(false), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.chronicle(wsId) }) });
  const rows = useMemo(() => [[t(($) => $.chronicle.facts), entry.facts], [t(($) => $.chronicle.feelings), entry.feelings], [t(($) => $.chronicle.then), entry.understanding_then], [t(($) => $.complete.actions), entry.actions]], [entry, t]);
  return <article className="space-y-4 rounded-lg border p-4"><div className="text-xs text-muted-foreground">{t(($) => $.complete.chronicle_meta, { kind: entry.period_kind, start: new Date(entry.period_start).toLocaleDateString(), end: new Date(entry.period_end).toLocaleDateString(), generatedBy: entry.generated_by, revision: entry.revision })}</div>{rows.map(([label, value]) => value && <div key={label}><div className="text-xs font-medium text-muted-foreground">{label}</div><p className="mt-1 text-sm leading-6">{value}</p></div>)}<div><div className="text-xs font-medium text-muted-foreground">{t(($) => $.chronicle.later)}</div>{editing ? <div className="mt-2 space-y-2"><Textarea value={later} onChange={(event) => setLater(event.target.value)} placeholder={t(($) => $.chronicle.add_later)} /><Button size="sm" disabled={update.isPending || !later.trim()} onClick={() => update.mutate()}>{t(($) => $.chronicle.save)}</Button></div> : <button type="button" className="mt-1 text-left text-sm hover:underline" onClick={() => setEditing(true)}>{entry.understanding_later}</button>}</div><details className="text-xs text-muted-foreground"><summary className="cursor-pointer">{t(($) => $.chronicle.evidence, { count: entry.evidence.length })}</summary><div className="mt-1">{entry.evidence.map((item) => <div key={`${item.source_type}:${item.source_id}`}>{item.source_type} · {item.source_id}</div>)}</div></details></article>;
}
