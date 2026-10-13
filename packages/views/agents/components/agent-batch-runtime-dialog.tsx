"use client";

import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { useBatchAgentRuntime, type AgentRuntimeResult, type AgentRuntimeSettings } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { isRuntimeUsableForUser, runtimeModelsOptions } from "@multica/core/runtimes";
import type { AgentRuntime, MemberWithUser } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { SettingsCard, SettingsRow } from "../../settings/components/settings-layout";
import { useT } from "../../i18n";
import { RuntimePicker } from "./inspector/runtime-picker";
import { ModelPicker } from "./inspector/model-picker";
import { buildModelChangeUpdate } from "./inspector/model-change-cleanup";
import { findModelCapabilityEntry } from "./inspector/model-capability";
import type { AgentListRow } from "./agents-page";

export function AgentBatchRuntimeDialog({ rows, runtimes, members, currentUserId, onClose, onSuccess }: {
  rows: AgentListRow[];
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId: string | null;
  onClose: () => void;
  onSuccess: () => void;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  // 本次目标由打开弹窗时的选择固定，列表刷新不会改变提交名单。
  const [targets] = useState(rows);
  const common = (field: "runtime_id" | "model" | "thinking_level" | "service_tier") => {
    const first = targets[0]?.agent[field] ?? "";
    return targets.every(({ agent }) => (agent[field] ?? "") === first) ? first : null;
  };
  const [runtimeId, setRuntimeId] = useState(() => common("runtime_id") ?? "");
  const [model, setModel] = useState<string | null>(() => common("runtime_id") ? common("model") : null);
  const [thinking, setThinking] = useState(() => common("thinking_level") ?? "");
  const [tier, setTier] = useState(() => common("service_tier") ?? "");
  const [results, setResults] = useState<AgentRuntimeResult[] | null>(null);
  const [progress, setProgress] = useState({ completed: 0, total: 0 });
  const submitting = useRef(false);
  const submittedSettings = useRef<AgentRuntimeSettings | null>(null);
  const mutation = useBatchAgentRuntime(wsId);
  const runtime = runtimes.find((item) => item.id === runtimeId);
  const online = runtime?.status === "online" && isRuntimeUsableForUser(runtime, currentUserId);
  const catalog = useQuery(runtimeModelsOptions(online ? runtimeId : null));
  const models = catalog.data?.models ?? [];
  const selectedModel = catalog.data?.supported === false ? "" : model;
  const entry = selectedModel ? findModelCapabilityEntry(models, selectedModel, runtime?.provider ?? "") : undefined;
  const levels = entry?.thinking?.supported_levels ?? [];
  const tiers = entry?.service_tiers ?? [];
  const standard = models.some((item) => item.supports_explicit_standard_service_tier === true);
  const effectiveThinking = levels.some((item) => item.value === thinking) ? thinking : "";
  const effectiveTier = tiers.some((item) => item.id === tier) || (tier === "default" && standard) ? tier : "";
  const blocked = targets.filter((row) => !row.canManage || row.agent.archived_at);
  const failures = results?.filter((result) => result.error !== undefined) ?? [];
  const frozen = mutation.isPending || results !== null;
  const ready = blocked.length === 0 && online && catalog.isSuccess && !catalog.isFetching && selectedModel !== null && (!selectedModel || !!entry);

  const submit = async () => {
    if (submitting.current || (!results && !ready)) return;
    submitting.current = true;
    const ids = results ? failures.map((result) => result.id) : targets.map((row) => row.agent.id);
    const settings = submittedSettings.current ?? { runtime_id: runtimeId, model: selectedModel ?? "", thinking_level: effectiveThinking, service_tier: effectiveTier };
    submittedSettings.current = settings;
    setProgress({ completed: 0, total: ids.length });
    try {
      const next = await mutation.mutateAsync({ ids, settings, onProgress: (completed) => setProgress({ completed, total: ids.length }) });
      setResults(next);
      if (next.every((result) => result.error === undefined)) onSuccess();
    } finally {
      submitting.current = false;
    }
  };

  const defaultLabel = t(($) => $.batch_runtime.follow_default);
  const selectClass = "min-h-11 w-full min-w-0 rounded-lg border border-input bg-background px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60";
  return <Dialog open onOpenChange={(open) => { if (!open && !submitting.current) onClose(); }}>
    <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>{t(($) => $.batch_runtime.title)}</DialogTitle>
        <DialogDescription>{t(($) => $.batch_runtime.description, { count: targets.length })}</DialogDescription>
      </DialogHeader>
      <details className="min-w-0 text-caption text-muted-foreground">
        <summary className="cursor-pointer rounded focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">{t(($) => $.batch_runtime.targets, { count: targets.length })}</summary>
        <ul className="mt-2 max-h-24 overflow-y-auto">{targets.map(({ agent }) => <li key={agent.id} className="break-words">{agent.name}</li>)}</ul>
      </details>
      <SettingsCard>
        <SettingsRow label={t(($) => $.inspector.prop_runtime)} size="select-wide">
          <RuntimePicker variant="field" showLabel={false} value={runtimeId} runtimes={runtimes} members={members} currentUserId={currentUserId} canEdit={!frozen} onChange={(id) => { setRuntimeId(id); setModel(""); setThinking(""); setTier(""); }} />
        </SettingsRow>
        <SettingsRow label={t(($) => $.inspector.prop_model)} size="select-wide">
          <ModelPicker allowCustom={false} variant="field" showLabel={false} runtimeId={runtimeId || null} runtimeOnline={!!online} value={model} canEdit={!frozen && !!online && catalog.isSuccess} onChange={(value) => {
            const change = buildModelChangeUpdate({ provider: runtime?.provider ?? "", model: value, thinkingLevel: effectiveThinking, serviceTier: effectiveTier, catalog: models });
            setModel(value);
            setThinking(value ? change.thinking_level ?? effectiveThinking : "");
            setTier(value ? change.service_tier ?? effectiveTier : "");
          }} />
        </SettingsRow>
        <SettingsRow label={t(($) => $.inspector.prop_thinking)} size="select-wide">
          <select aria-label={t(($) => $.inspector.prop_thinking)} className={selectClass} value={effectiveThinking} disabled={frozen || !levels.length} onChange={(event) => setThinking(event.target.value)}>
            <option value="">{!model ? defaultLabel : !levels.length ? t(($) => $.batch_runtime.unsupported) : defaultLabel}</option>
            {levels.map((level) => <option key={level.value} value={level.value}>{level.label}</option>)}
          </select>
        </SettingsRow>
        <SettingsRow label={t(($) => $.batch_runtime.speed)} size="select-wide">
          <select aria-label={t(($) => $.batch_runtime.speed)} className={selectClass} value={effectiveTier} disabled={frozen || (!tiers.length && !standard)} onChange={(event) => setTier(event.target.value)}>
            <option value="">{!model ? defaultLabel : !tiers.length && !standard ? t(($) => $.batch_runtime.unsupported) : defaultLabel}</option>
            {standard && <option value="default">{t(($) => $.pickers.service_tier_standard)}</option>}
            {tiers.filter((item) => !standard || item.id !== "default").map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
          </select>
        </SettingsRow>
      </SettingsCard>
      {!results && runtimeId && (!online || catalog.isError) && <div role="alert" className="text-caption text-destructive">
        {t(($) => $.batch_runtime.catalog_error)}
        {online && <Button variant="ghost" size="sm" onClick={() => void catalog.refetch()}>{t(($) => $.batch_runtime.retry)}</Button>}
      </div>}
      {!results && online && catalog.isPending && <p role="status" className="text-caption text-muted-foreground">{t(($) => $.batch_runtime.loading)}</p>}
      {!results && selectedModel && catalog.isSuccess && !entry && <p role="alert" className="text-caption text-destructive">{t(($) => $.batch_runtime.choose_model)}</p>}
      {blocked.length > 0 && <p role="alert" className="break-words text-caption text-destructive">{t(($) => $.batch_runtime.blocked, { names: blocked.map((row) => row.agent.name).join("、") })}</p>}
      {failures.length > 0 && <div role="alert" className="text-caption">
        <p>{t(($) => $.batch_runtime.partial, { succeeded: targets.length - failures.length, failed: failures.length })}</p>
        <ul className="mt-2 max-h-32 overflow-y-auto text-destructive">{failures.map((result) => <li key={result.id} className="break-words">{targets.find((row) => row.agent.id === result.id)?.agent.name}: {result.error}</li>)}</ul>
      </div>}
      <p className="text-caption text-muted-foreground">{t(($) => $.batch_runtime.running_hint)}</p>
      <DialogFooter>
        <Button variant="outline" disabled={mutation.isPending} onClick={onClose}>{t(($) => $.batch_runtime.cancel)}</Button>
        <Button disabled={mutation.isPending || (results ? failures.length === 0 : !ready)} onClick={() => void submit()}>
          {mutation.isPending && <Loader2 className="size-4 animate-spin" aria-hidden="true" />}
          <span aria-live="polite">{mutation.isPending ? t(($) => $.batch_runtime.progress, progress) : results ? t(($) => $.batch_runtime.retry_failed) : t(($) => $.batch_runtime.apply, { count: targets.length })}</span>
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>;
}
