"use client";

/* eslint-disable i18next/no-literal-string */

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, Check, ChevronRight, Eye, History, Loader2, Pause, Play, Plus, RotateCcw, Save, Search, ShieldCheck, Sparkles, Trash2, X } from "lucide-react";
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
import type { LifeChronicleEntry, LifeExperiment, LifeExperimentRound, LifeIdentityVersion, LifeMemory, LifeObservationSeatResponse } from "@multica/core/types";
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
  const actionLabel = activeTab === "memory" ? "记录一条" : activeTab === "experiment" ? "新建实验" : activeTab === "observers" ? "管理观察席" : "生成缺失周期";

  useEffect(() => { setExpanded(false); setOpen(false); }, [setExpanded, setOpen]);

  return (
    <div data-testid="life-shell" className="flex h-full min-h-0 flex-col bg-background">
      <PageHeader className="h-16 shrink-0 justify-between border-b border-border/60 bg-background px-4 md:px-8">
        <div className="flex min-w-0 items-center gap-2 text-sm">
          <span className="text-muted-foreground">{t(($) => $.page.title)}</span><ChevronRight className="size-3 text-muted-foreground/50" aria-hidden="true" /><h1 className="shrink-0 font-medium">{tabCopy[activeTab].label}</h1>
        </div>
        <div className="hidden items-center gap-3 text-xs text-muted-foreground md:flex"><Button size="sm" variant="ghost" className="font-normal" onClick={() => (document.getElementById(`life-${activeTab}-details`) ?? document.querySelector(`[data-testid=life-${activeTab}-panel]`))?.scrollIntoView({ behavior: "smooth" })}>+ {actionLabel}</Button><span className="rounded-md border border-border/60 px-2 py-1">长期工作台</span><Button size="icon-sm" variant="ghost" aria-label="搜索"><Search className="size-4" /></Button><span className="size-7 rounded-full bg-brand/10 text-center leading-7 text-brand">我</span></div>
      </PageHeader>
      <main className="min-h-0 flex-1 overflow-y-auto bg-background"><h1 className="sr-only">{t(($) => $.page.title)}</h1>
        <div className="mx-auto w-full max-w-[1150px] px-4 py-8 md:px-8 md:py-10">
          <header className="mb-8 max-w-2xl space-y-2"><p className="text-xs font-medium tracking-wide text-brand">{t(($) => $.page.title)}</p><h2 className="text-base font-medium tracking-tight">{tabCopy[activeTab].label}</h2><p className="text-sm leading-6 text-muted-foreground">{tabCopy[activeTab].description}</p></header>
          <div className="min-w-0">
            {activeTab === "memory" && <QuickMaterialRecorder />}
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

function QuickMaterialRecorder() {
  const { t } = useT("life");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const [material, setMaterial] = useState("");
  const add = useMutation({ mutationFn: () => api.createLifeMaterial(material), onSuccess: () => setMaterial(""), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.materials(wsId) }) });
  return <section data-testid="memory-quick-record" className="mb-5 rounded-xl border border-border/70 bg-background p-4"><div className="mb-3 text-sm font-medium">{t(($) => $.complete.material_title)}</div><div className="flex flex-wrap items-center gap-3"><Textarea className="min-h-10 flex-1" value={material} onChange={(event) => setMaterial(event.target.value)} placeholder={t(($) => $.complete.material_placeholder)} /><Button size="sm" disabled={!material.trim() || add.isPending} onClick={() => add.mutate()}><Plus />{t(($) => $.complete.record)}</Button></div></section>;
}

function QueryLoading() {
  return <div className="flex items-center justify-center py-10 text-muted-foreground"><Loader2 className="size-4 animate-spin" /></div>;
}

function MemoryPanel() {
  const { t } = useT("life");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const query = useQuery(lifeMemoryListOptions(wsId));
  const [material, setMaterial] = useState("");
  const addMaterial = useMutation({ mutationFn: () => api.createLifeMaterial(material), onSuccess: () => setMaterial(""), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.materials(wsId) }) });
  const identities = useQuery(lifeIdentityListOptions(wsId));
  const relationships = useQuery(lifeRelationshipListOptions(wsId));
  const topics = useQuery(lifeTopicListOptions(wsId));
  const commitments = useQuery(lifeCommitmentListOptions(wsId));
  const materials = useQuery(lifeMaterialListOptions(wsId));
  const thoughts = useQuery(lifeInternalThoughtListOptions(wsId));
  const updateCommitment = useMutation({ mutationFn: ({ id, status }: { id: string; status: string }) => api.updateLifeCommitment(id, { status }), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.commitments(wsId) }) });
  const resolveEvent = useMutation({ mutationFn: (id: string) => api.resolveLifeRelationshipEvent(id, "resolved", "已由用户确认处理"), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.relationships(wsId) }) });
  const updateTopic = useMutation({ mutationFn: ({ id, status }: { id: string; status: string }) => api.updateLifeTopic(id, status), onSettled: () => qc.invalidateQueries({ queryKey: lifeKeys.topics(wsId) }) });
  if ([query, identities, relationships, topics, commitments, materials, thoughts].some((item) => item.isLoading)) return <QueryLoading />;
  const memories = query.data?.memories ?? [];
  const activeIdentity = identities.data?.versions.find((version) => version.status === "active");
  const active = memories.filter((item) => item.status !== "archived");
  const focus = [...active].sort((a, b) => Number(b.status === "confirmed") - Number(a.status === "confirmed") || b.confidence - a.confidence || new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())[0];
  const others = focus ? active.filter((item) => item.id !== focus.id) : [];
  const changes = [...active].filter((item) => item.id !== focus?.id).sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()).slice(0, 4);
  return <div id="life-memory-details" data-testid="life-memory-panel" className="space-y-8">
    <div className="grid gap-5 lg:grid-cols-[1.58fr_.82fr]">
      <section data-testid="memory-focus-card" className="rounded-xl border border-brand/20 bg-brand/[0.04] p-5 md:p-7"><div className="flex items-center justify-between"><div className="flex items-center gap-2 text-xs font-medium text-brand"><Sparkles className="size-4" />长期理解</div>{focus && <span className="rounded-full bg-background/80 px-2 py-1 text-xs text-muted-foreground">{focus.status === "confirmed" ? t(($) => $.memory.confirmed) : t(($) => $.memory.candidate)}</span>}</div>{focus ? <><h2 className="mt-5 text-base font-medium leading-7">{focus.content}</h2><div className="mt-5 flex flex-wrap gap-2">{focus.evidence.slice(0, 4).map((evidence) => <span key={`${evidence.source_type}:${evidence.source_id}`} className="rounded-full border border-border/60 bg-background px-2.5 py-1 text-xs text-muted-foreground">{evidence.source_type}</span>)}<span className="rounded-full border border-border/60 bg-background px-2.5 py-1 text-xs text-muted-foreground">可信度 {Math.round(focus.confidence * 100)}%</span></div>{focus.uncertainty && <p className="mt-4 text-xs leading-5 text-muted-foreground">不确定：{focus.uncertainty}</p>}<div className="mt-6 flex flex-wrap gap-2"><MemoryRow memory={focus} compact /></div></> : <div className="mt-5 space-y-2"><p className="text-sm text-muted-foreground">{t(($) => $.memory.empty)}</p><p className="text-xs text-muted-foreground">先在下方记录一条材料，搭子会和你一起形成理解。</p></div>}</section>
      <aside className="space-y-5"><section data-testid="memory-changes" className="rounded-xl border border-border/70 bg-background p-5"><div className="flex items-center gap-2 text-sm font-medium"><History className="size-4 text-brand" />最近发生的变化</div>{changes.length === 0 ? <p className="mt-4 text-xs text-muted-foreground">还没有可回看的变化。</p> : <div className="mt-5 space-y-4">{changes.map((item) => <div key={item.id} className="relative border-l border-border pl-4"><span className="absolute -left-1.5 top-1 size-2.5 rounded-full bg-brand" /><p className="text-xs text-muted-foreground">{new Date(item.updated_at).toLocaleDateString()}</p><p className="mt-1 line-clamp-2 text-sm">{item.content}</p></div>)}</div>}</section><section data-testid="memory-governance" className="rounded-xl border border-border/70 bg-muted/20 p-5"><div className="flex items-center gap-2 text-sm font-medium"><ShieldCheck className="size-4 text-brand" />记忆治理</div><p className="mt-3 text-xs leading-5 text-muted-foreground">记忆由你确认、纠正、降级或永久删除。搭子只把仍然有效的内容当作事实。</p></section></aside>
    </div>
    <section className="space-y-3"><div className="flex items-center justify-between"><h2 className="text-sm font-medium">其他正在使用的理解</h2><span className="text-xs text-muted-foreground">{others.length} 条</span></div>{others.length === 0 ? <p className="rounded-xl border border-dashed border-border p-6 text-sm text-muted-foreground">暂无其他理解。</p> : <div className="grid gap-3 md:grid-cols-2">{others.map((memory) => <MemoryRow key={memory.id} memory={memory} compact />)}</div>}</section>
    <section className="rounded-xl border border-border/70"><details><summary className="cursor-pointer list-none p-5 text-sm font-medium">更多上下文与治理</summary><div className="grid gap-5 border-t border-border/60 p-5 md:grid-cols-2"><section className="space-y-3"><h3 className="text-sm font-medium">{t(($) => $.complete.identity_title)}</h3>{activeIdentity ? <IdentitySummary identity={activeIdentity} /> : <p className="text-sm text-muted-foreground">{t(($) => $.complete.no_identity)}</p>}{(identities.data?.versions ?? []).filter((version) => version.status === "draft").map((version) => <IdentityDraftRow key={version.id} identity={version} />)}<IdentityEditor identity={activeIdentity} /></section><section className="space-y-3"><h3 className="text-sm font-medium">{t(($) => $.complete.material_title)}</h3><Textarea value={material} onChange={(event) => setMaterial(event.target.value)} placeholder="补充材料（治理区）" /><Button size="sm" disabled={!material.trim() || addMaterial.isPending} onClick={() => addMaterial.mutate()}><Plus />{t(($) => $.complete.record)}</Button><p className="text-xs text-muted-foreground">{t(($) => $.complete.material_count, { count: materials.data?.materials.length ?? 0 })}</p></section><section className="space-y-3"><h3 className="text-sm font-medium">{t(($) => $.complete.thought_title)}</h3>{(thoughts.data?.thoughts ?? []).length === 0 ? <p className="text-sm text-muted-foreground">{t(($) => $.complete.no_thoughts)}</p> : thoughts.data?.thoughts.slice(0, 4).map((thought) => <article key={thought.id} className="rounded-lg border p-3"><p className="text-sm">{thought.title}</p><p className="mt-1 text-xs text-muted-foreground">{thought.content}</p></article>)}</section><section className="space-y-3"><h3 className="text-sm font-medium">{t(($) => $.complete.topic_title)}</h3>{(topics.data?.topics ?? []).length === 0 ? <p className="text-sm text-muted-foreground">{t(($) => $.complete.no_topics)}</p> : topics.data?.topics.map((topic) => <article key={topic.id} className="rounded-lg border p-3"><div className="flex justify-between text-xs"><span>{topic.title}</span><span className="text-muted-foreground">{Math.round(topic.confidence * 100)}%</span></div><p className="mt-1 text-xs text-muted-foreground">{topic.summary}</p>{topic.status === "candidate" && <Button className="mt-2" size="sm" onClick={() => updateTopic.mutate({ id: topic.id, status: "active" })}>{t(($) => $.complete.confirm)}</Button>}</article>)}</section><section className="space-y-3 md:col-span-2"><h3 className="text-sm font-medium">{t(($) => $.complete.commitment_title)}</h3>{(commitments.data?.commitments ?? []).length === 0 ? <p className="text-sm text-muted-foreground">{t(($) => $.complete.no_commitments)}</p> : commitments.data?.commitments.map((item) => <article key={item.id} className="flex items-center gap-3 rounded-lg border p-3"><span className="min-w-0 flex-1 text-sm">{item.content}</span>{item.status === "candidate" && <Button size="sm" onClick={() => updateCommitment.mutate({ id: item.id, status: "confirmed" })}>{t(($) => $.complete.confirm)}</Button>}{item.status === "confirmed" && <Button size="sm" variant="outline" onClick={() => updateCommitment.mutate({ id: item.id, status: "completed" })}>{t(($) => $.complete.complete)}</Button>}</article>)}</section>{(relationships.data?.events ?? []).filter((event) => event.status !== "resolved").map((event) => <article key={event.id} className="rounded-lg border p-3 md:col-span-2"><div className="text-xs font-medium">{relationshipEventLabel(event.event_type, t)} · {relationshipStatusLabel(event.status, t)}</div><p className="mt-2 text-sm">{event.context}</p><Button className="mt-3" size="sm" variant="outline" onClick={() => resolveEvent.mutate(event.id)}>{t(($) => $.complete.finish_review)}</Button></article>)}</div></details></section>
    <section className="rounded-xl border border-border/70"><details><summary className="cursor-pointer list-none p-5 text-sm font-medium">完整记忆与修订历史</summary><div className="space-y-3 border-t border-border/60 p-5">{memories.length === 0 ? <p className="text-sm text-muted-foreground">{t(($) => $.memory.empty)}</p> : memories.filter((memory) => memory.id !== focus?.id).map((memory) => <MemoryRow key={memory.id} memory={memory} />)}</div></details></section>
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

function MemoryRow({ memory, compact = false }: { memory: LifeMemory; compact?: boolean }) {
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
  if (compact) return editing ? <div className="w-full space-y-2"><Textarea value={content} onChange={(event) => setContent(event.target.value)} /><div className="flex gap-2"><Button size="sm" disabled={busy || !content.trim()} onClick={() => update.mutate({ id: memory.id, data: { kind: memory.kind, content, uncertainty, confidence: memory.confidence, urgency: memory.urgency } }, { onSuccess: () => setEditing(false) })}><Save />{t(($) => $.memory.save)}</Button><Button size="sm" variant="ghost" onClick={() => setEditing(false)}>{t(($) => $.memory.cancel)}</Button></div></div> : <div className="flex flex-wrap gap-2"><span className="text-xs text-muted-foreground">{Math.round(memory.confidence * 100)}%</span>{memory.status === "candidate" && <Button size="sm" disabled={busy} onClick={() => confirm.mutate(memory.id)}><Check />{t(($) => $.memory.confirm)}</Button>}<Button size="sm" variant="outline" disabled={busy} onClick={() => setEditing(true)}>{t(($) => $.memory.correct)}</Button>{memory.status !== "archived" && <Button size="sm" variant="ghost" disabled={busy} onClick={() => archive.mutate(memory.id)}><Archive />{t(($) => $.memory.archive)}</Button>}{!confirmingDelete ? <Button size="sm" variant="ghost" className="text-destructive" disabled={busy} onClick={() => setConfirmingDelete(true)}><Trash2 />{t(($) => $.memory.delete)}</Button> : <><span className="text-xs text-destructive">{t(($) => $.memory.delete_question)}</span><Button size="sm" variant="destructive" disabled={busy} onClick={() => remove.mutate(memory.id)}>{t(($) => $.memory.delete_confirm)}</Button></>}</div>;
  const statusLabel = memory.status === "candidate"
    ? t(($) => $.memory.candidate)
    : memory.status === "confirmed"
      ? t(($) => $.memory.confirmed)
      : t(($) => $.memory.archived);
  return (
    <article data-testid="memory-row" className={`space-y-3 rounded-xl border border-border/70 p-4 ${compact ? "bg-background" : ""}`}>
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
  const firstExperiment = experiments[0];
  const current = experiments.map((experiment) => ({ experiment, rounds: rounds.filter((round) => round.experiment_id === experiment.id) })).find(({ rounds: items }) => items.some((round) => ["running", "awaiting_review"].includes(round.status))) ?? (firstExperiment ? { experiment: firstExperiment, rounds: rounds.filter((round) => round.experiment_id === firstExperiment.id) } : undefined);
  const currentRound = current?.rounds.at(-1);
  return <div id="life-experiment-details" data-testid="life-experiment-panel" className="space-y-8"><div className="grid gap-5 lg:grid-cols-[1.58fr_.82fr]">
    <section data-testid="experiment-hero" className="rounded-xl border border-brand/20 bg-brand/[0.04] p-5 md:p-7"><div className="flex items-center justify-between"><span className="text-xs font-medium text-brand">实验 · {currentRound ? `${current?.rounds.indexOf(currentRound) ?? 0 + 1} / ${current?.rounds.length ?? 0} 轮` : "准备开始"}</span>{currentRound && <span className="rounded-full bg-background px-2 py-1 text-xs text-muted-foreground">{currentRound.status}</span>}</div>{current?.experiment ? <><h2 className="mt-5 text-base font-medium">{current.experiment.title}</h2><dl className="mt-5 space-y-3 text-sm"><div><dt className="text-xs text-muted-foreground">想回答的问题</dt><dd className="mt-1">{current.experiment.problem}</dd></div><div><dt className="text-xs text-muted-foreground">当前假设</dt><dd className="mt-1">{current.experiment.hypothesis}</dd></div></dl><div className="mt-6 border-t border-border/60 pt-4"><p className="text-xs font-medium">低负担采集</p>{currentRound?.observations.length ? <ul className="mt-3 space-y-2 text-sm">{currentRound.observations.slice(0, 5).map((item) => <li key={item.id} className="flex gap-2"><Check className="mt-0.5 size-4 text-brand" />{item.content}</li>)}</ul> : <p className="mt-2 text-xs text-muted-foreground">本轮还没有过程观察。</p>}</div></> : <p className="mt-5 text-sm text-muted-foreground">{t(($) => $.experiment.none)}</p>}</section>
    <section data-testid="experiment-review" className="rounded-xl border border-border/70 bg-background p-5 md:p-7"><div className="flex items-center gap-2 text-sm font-medium"><History className="size-4 text-brand" />本轮复盘</div>{currentRound?.review ? <div className="mt-5 space-y-4 text-sm"><p>{currentRound.review.outcome}</p><p className="text-xs text-muted-foreground">{currentRound.review.feelings}</p><p className="text-xs text-muted-foreground">负担：{currentRound.review.burden}</p></div> : <p className="mt-5 text-sm leading-6 text-muted-foreground">完成这一轮后，在下面的轮次管理中留下你的感受、实际结果和下一步。你可以随时停止，也可以以后再来。</p>}</section>
  </div><section className="space-y-3"><div className="flex items-center justify-between"><h2 className="text-sm font-medium">全部轮次与治理</h2><span className="text-xs text-muted-foreground">{experiments.length} 个实验 · {rounds.length} 轮</span></div><div className="space-y-4">{pending.map((proposal) => <article key={proposal.id} className="space-y-2 rounded-xl border border-dashed border-brand/40 p-4"><div className="text-xs text-muted-foreground">等待你确认 · {proposal.proposal_type}</div><div className="text-sm font-medium">{proposal.title}</div><p className="text-sm text-muted-foreground">{proposal.summary}</p><div className="flex gap-2"><Button size="sm" disabled={confirm.isPending} onClick={() => confirm.mutate(proposal.id)}><Check />{t(($) => $.complete.confirm_execute)}</Button><Button size="sm" variant="outline" disabled={reject.isPending} onClick={() => reject.mutate(proposal.id)}><X />{t(($) => $.complete.reject)}</Button></div></article>)}{experiments.map((experiment) => <ExperimentRows key={experiment.id} experiment={experiment} rounds={rounds.filter((round) => round.experiment_id === experiment.id)} />)}{modules.length > 0 && <details className="rounded-xl border border-border/70"><summary className="cursor-pointer list-none p-4 text-sm font-medium">{t(($) => $.complete.modules_title)}</summary><div className="space-y-2 border-t border-border/60 p-4">{modules.map((module) => <article key={module.id} className="flex items-center gap-3 rounded-lg border p-3"><div className="min-w-0 flex-1"><div className="text-sm">{module.name}</div><div className="text-xs text-muted-foreground">{t(($) => $.complete.module_status, { status: module.status, version: module.current_version })}</div></div>{module.status === "active" ? <Button size="sm" variant="outline" onClick={() => updateModule.mutate({ id: module.id, status: "paused" })}><Pause />{t(($) => $.complete.pause)}</Button> : <Button size="sm" onClick={() => updateModule.mutate({ id: module.id, status: "active" })}><Play />{t(($) => $.complete.enable)}</Button>}</article>)}</div></details>}</div></section></div>;
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
  const latestByObserver = new Map<string, LifeObservationSeatResponse["judgements"][number]>();
  for (const judgement of [...(seatQuery.data?.judgements ?? [])].filter((item) => item.status === "published").sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())) if (!latestByObserver.has(judgement.observer_id)) latestByObserver.set(judgement.observer_id, judgement);
  return <div id="life-observers-details" data-testid="life-observers-panel" className="space-y-8">
    {jobs.some((job) => job.status === "cancelled") && <section className="rounded-xl border border-destructive/30 bg-destructive/5 p-4"><p className="text-sm font-medium text-destructive">连续处理失败，这部分内容还没有进入长期理解。</p><p className="mt-1 text-xs text-muted-foreground">展开下方治理区域可重新处理失败任务。</p></section>}
    <section className="rounded-xl border border-brand/20 bg-brand/[0.04] p-5 md:p-6"><p className="text-sm font-medium">多个视角，保留分歧</p><p className="mt-2 max-w-2xl text-sm leading-6 text-muted-foreground">他们可以看见人生系统中的全部信息，但各自独立判断。你不需要接受每一个结论，分歧本身也是值得保留的线索。</p></section>
    <section data-testid="observer-grid" className="grid gap-4 md:grid-cols-2">{observers.slice(0, 4).map((observer) => { const judgement = latestByObserver.get(observer.id); return <article key={observer.id} className="space-y-4 rounded-xl border border-border/70 bg-background p-5"><div className="flex items-start gap-3"><div className="flex size-10 items-center justify-center rounded-full bg-brand/10 text-sm font-medium text-brand">{observer.name.slice(0, 1)}</div><div className="min-w-0 flex-1"><h2 className="text-sm font-medium">{observer.name}</h2><p className="mt-1 text-xs text-muted-foreground">{observer.basis_type} · {observer.status}</p></div></div>{judgement ? <><p className="text-sm font-medium leading-6">{judgement.title}</p><p className="text-sm leading-6 text-muted-foreground">{judgement.content}</p><div className="flex items-center justify-between text-xs text-muted-foreground"><span>依据 {judgement.evidence.length} 条 · 可信度 {Math.round(judgement.confidence * 100)}%</span><details><summary className="cursor-pointer text-brand">展开详情</summary><div className="mt-2 max-w-xs text-right">{judgement.uncertainty || "没有额外不确定性说明。"}</div></details></div></> : <p className="rounded-lg bg-muted/30 p-4 text-sm text-muted-foreground">还没有形成判断，等下一次观察。</p>}</article>; })}{observers.length === 0 && <p className="rounded-xl border border-dashed p-6 text-sm text-muted-foreground">{t(($) => $.observers.empty)}</p>}</section>
    <section data-testid="observer-disagreement" className="rounded-xl border border-border/70 p-5 md:p-6"><div className="flex items-center gap-2 text-sm font-medium"><ShieldCheck className="size-4 text-brand" />一处真实分歧</div>{latestByObserver.size < 2 ? <p className="mt-3 text-sm text-muted-foreground">有两种以上判断后，这里会并列展示它们的差异。</p> : <div className="mt-4 grid gap-4 md:grid-cols-2">{Array.from(latestByObserver.values()).slice(0, 2).map((judgement) => <blockquote key={judgement.id} className="border-l-2 border-brand/40 pl-4 text-sm leading-6"><p className="text-xs text-muted-foreground">{judgement.observer_name}</p>{judgement.content}</blockquote>)}</div>}</section>
    <section className="rounded-xl border border-border/70"><details><summary className="cursor-pointer list-none p-5 text-sm font-medium">管理观察席与更多治理</summary><div className="space-y-6 border-t border-border/60 p-5"><div className="space-y-3"><h3 className="text-sm font-medium">{t(($) => $.complete.proactive_title)}</h3><div className="flex flex-wrap items-end gap-3"><label className="space-y-1 text-xs"><span>{t(($) => $.complete.minimum_interval)}</span><Input className="w-28" type="number" min={1} value={intervalHours} onChange={(event) => setIntervalHours(Number(event.target.value))} /></label><Button size="sm" variant={policyQuery.data?.enabled ? "outline" : "default"} onClick={() => updatePolicy.mutate(!policyQuery.data?.enabled)}>{policyQuery.data?.enabled ? t(($) => $.complete.pause_proactive) : t(($) => $.complete.enable_proactive)}</Button><Button size="sm" variant="outline" onClick={() => updatePolicy.mutate(policyQuery.data?.enabled ?? true)}>{t(($) => $.complete.save_policy)}</Button></div><Textarea placeholder={t(($) => $.complete.quiet_hours)} value={quietHours} onChange={(event) => setQuietHours(event.target.value)} /></div><div className="grid gap-3 md:grid-cols-2">{observers.map((observer) => <article key={observer.id} className="rounded-lg border p-4"><div className="flex items-center justify-between gap-3"><div><div className="text-sm font-medium">{observer.name}</div><div className="text-xs text-muted-foreground">资料 {observer.knowledge.length} 条</div></div><div className="flex gap-2"><Button size="sm" variant="outline" onClick={() => runObserver.mutate(observer.id)}><Eye />{t(($) => $.complete.run_now)}</Button><Button size="sm" variant="ghost" onClick={() => updateObserver.mutate({ id: observer.id, status: observer.status === "active" ? "paused" : "active" })}>{observer.status === "active" ? t(($) => $.complete.pause) : t(($) => $.complete.enable)}</Button></div></div><details className="mt-3"><summary className="cursor-pointer text-xs text-muted-foreground">{t(($) => $.complete.identity_and_perspective)}</summary><pre className="mt-2 overflow-auto whitespace-pre-wrap text-xs">{JSON.stringify({ personality: observer.personality, perspective: observer.perspective }, null, 2)}</pre></details><div className="mt-3 flex gap-2"><Button size="sm" variant="ghost" onClick={() => setKnowledgeDraft({ observer_id: observer.id, title: "", content: "" })}><Plus />{t(($) => $.complete.add_knowledge)}</Button><Button size="sm" variant="ghost" onClick={() => setObserverVersionDraft({ observer_id: observer.id, personality: JSON.stringify(observer.personality, null, 2), perspective: JSON.stringify(observer.perspective, null, 2), expression_profile: JSON.stringify(observer.expression_profile, null, 2), change_reason: "" })}>{t(($) => $.complete.edit_identity)}</Button></div></article>)}</div>{knowledgeDraft.observer_id && <div className="grid gap-2 rounded-lg border p-4"><Input placeholder={t(($) => $.complete.knowledge_title)} value={knowledgeDraft.title} onChange={(event) => setKnowledgeDraft({ ...knowledgeDraft, title: event.target.value })}/><Textarea placeholder={t(($) => $.complete.knowledge_content)} value={knowledgeDraft.content} onChange={(event) => setKnowledgeDraft({ ...knowledgeDraft, content: event.target.value })}/><Button size="sm" onClick={() => addKnowledge.mutate()}>{t(($) => $.complete.save_knowledge)}</Button></div>}{observerVersionDraft.observer_id && <div className="grid gap-2 rounded-lg border p-4"><Input placeholder={t(($) => $.complete.change_reason)} value={observerVersionDraft.change_reason} onChange={(event) => setObserverVersionDraft({ ...observerVersionDraft, change_reason: event.target.value })}/><Textarea value={observerVersionDraft.personality} onChange={(event) => setObserverVersionDraft({ ...observerVersionDraft, personality: event.target.value })}/><Button size="sm" onClick={() => createObserverVersion.mutate()}>{t(($) => $.complete.activate)}</Button></div>}<details className="rounded-lg border p-4"><summary className="cursor-pointer text-sm">{t(($) => $.complete.create_observer)}</summary><div className="mt-3 grid gap-2"><select className="h-9 rounded-md border bg-background px-3 text-sm" value={observerDraft.agent_id} onChange={(event) => setObserverDraft({ ...observerDraft, agent_id: event.target.value })}><option value="">{t(($) => $.complete.choose_agent)}</option>{(agentsQuery.data ?? []).filter((agent) => !agent.archived_at && agent.id !== companionQuery.data?.profile?.agent_id).map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select><Input placeholder={t(($) => $.complete.observer_name)} value={observerDraft.name} onChange={(event) => setObserverDraft({ ...observerDraft, name: event.target.value })}/><Button size="sm" disabled={!observerDraft.agent_id || !observerDraft.name.trim()} onClick={() => createObserver.mutate()}><Plus />{t(($) => $.complete.create)}</Button></div></details><details className="rounded-lg border p-4"><summary className="cursor-pointer text-sm">{t(($) => $.complete.background_title)} · {checks.length} 条主动记录 · {upgradesQuery.data?.evaluations.length ?? 0} 次升级评估</summary><div className="mt-3 space-y-2">{jobs.length === 0 ? <p className="text-xs text-muted-foreground">{t(($) => $.complete.none)}</p> : jobs.slice(0, 8).map((job) => <article key={job.id} className="flex items-center gap-3 rounded border p-3"><span className="min-w-0 flex-1 text-xs">{job.job_type} · {job.status}</span>{job.status === "cancelled" && <Button size="sm" variant="outline" onClick={() => retryJob.mutate(job.id)}><RotateCcw />{t(($) => $.complete.retry_job)}</Button>}</article>)}</div></details></div></details></section>
  </div>;
}

function ChroniclePanel() {
  const { t } = useT("life");
  const wsId = useWorkspaceId();
  const query = useQuery(lifeChronicleListOptions(wsId));
  const [selected, setSelected] = useState("");
  if (query.isLoading) return <QueryLoading />;
  const entries = query.data?.entries ?? [];
  if (entries.length === 0) return <p className="rounded-xl border border-dashed p-6 text-sm text-muted-foreground">{t(($) => $.chronicle.empty)}</p>;
  const grouped = entries.reduce<Record<string, LifeChronicleEntry[]>>((result, entry) => { const year = new Date(entry.period_start).getFullYear().toString(); (result[year] ??= []).push(entry); return result; }, {});
  const years = Object.keys(grouped).sort((a, b) => Number(b) - Number(a));
  const activeYear = years.includes(selected) ? selected : (years[0] ?? "");
  const yearEntries = grouped[activeYear] ?? [];
  const selectedEntry = yearEntries[0] ?? entries[0];
  if (!selectedEntry) return <p className="rounded-xl border border-dashed p-6 text-sm text-muted-foreground">{t(($) => $.chronicle.empty)}</p>;
  // @ts-expect-error grouped keys are derived from the same years array.
  return <div data-testid="life-chronicle-panel" className="grid gap-5 lg:grid-cols-[.65fr_1.35fr]"><aside className="rounded-xl border border-border/70 p-4"><h2 className="text-sm font-medium">时间线</h2><div className="mt-4 space-y-1">{years.map((year) => <button key={year} type="button" className={`flex w-full items-center justify-between rounded-lg px-3 py-2 text-left text-sm ${activeYear === year ? "bg-brand/10 text-brand" : "text-muted-foreground"}`} onClick={() => setSelected(year)}><span>{year}</span><span className="text-xs">{grouped[year].length}</span></button>)}</div></aside><section data-testid="chronicle-detail" className="rounded-xl border border-border/70 p-5 md:p-7"><div className="flex items-center justify-between"><div><p className="text-xs text-muted-foreground">{selectedEntry.period_kind} · {new Date(selectedEntry.period_start).toLocaleDateString()} — {new Date(selectedEntry.period_end).toLocaleDateString()}</p><h2 className="mt-2 text-base font-medium">{activeYear} 年的人生片段</h2></div><span className="text-xs text-muted-foreground">v{selectedEntry.revision}</span></div><ChronicleRow entry={selectedEntry} /><div className="mt-5 space-y-2">{yearEntries.slice(1).map((entry) => <ChronicleRow key={entry.id} entry={entry} />)}</div></section></div>;
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
