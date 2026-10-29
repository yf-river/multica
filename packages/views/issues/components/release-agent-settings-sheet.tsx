"use client";

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/paths";
import { projectListOptions } from "@multica/core/projects/queries";
import { releaseAgentKeys, releaseAgentSettingsOptions } from "@multica/core/release-agent/queries";
import { runtimeDisplayLabel, runtimeListOptions, runtimeModelsOptions } from "@multica/core/runtimes";
import { Button } from "@multica/ui/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@multica/ui/components/ui/sheet";
import { findModelCapabilityEntry } from "../../agents/components/inspector/model-capability";
import { useT } from "../../i18n";

const selectClass = "h-10 w-full rounded-md border bg-background px-3 text-body";

export function ReleaseAgentSettingsSheet({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const cache = useQueryClient();
  const settings = useQuery(releaseAgentSettingsOptions(wsId, open));
  const projects = useQuery({ ...projectListOptions(wsId), enabled: open });
  const runtimes = useQuery({ ...runtimeListOptions(wsId), enabled: open });
  const [enabled, setEnabled] = useState(false);
  const [projectId, setProjectId] = useState("");
  const [runtimeId, setRuntimeId] = useState("");
  const [model, setModel] = useState("");
  const [thinking, setThinking] = useState("");
  const [instructions, setInstructions] = useState("");

  useEffect(() => {
    const data = settings.data;
    if (!data) return;
    setEnabled(data.enabled);
    setProjectId(data.project_id);
    setRuntimeId(data.runtime_id);
    setModel(data.model);
    setThinking(data.thinking_level);
    setInstructions(data.instructions);
  }, [settings.data]);

  const usable = useMemo(() => (runtimes.data ?? []).filter((runtime) => runtime.provider === "codex" || runtime.provider === "codebuddy"), [runtimes.data]);
  const runtime = usable.find((item) => item.id === runtimeId);
  const catalog = useQuery({ ...runtimeModelsOptions(runtimeId || null), enabled: open && Boolean(runtimeId) });
  const models = catalog.data?.models ?? [];
  const levels = runtime && model ? findModelCapabilityEntry(models, model, runtime.provider)?.thinking?.supported_levels ?? [] : [];
  const canManage = settings.data?.can_manage ?? false;
  const save = useMutation({
    mutationFn: () => api.updateReleaseAgentSettings(!enabled && (!projectId || !runtimeId || !runtime)
      ? { enabled: false }
      : { enabled, project_id: projectId, runtime_id: runtimeId, model, thinking_level: thinking, instructions }),
    onSuccess: (value) => {
      cache.setQueryData(releaseAgentKeys.settings(wsId), value);
      toast.success(t(($) => $.release_agent.saved));
      onClose();
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : String(error)),
  });

  return (
    <Sheet open={open} onOpenChange={(value) => { if (!value) onClose(); }}>
      <SheetContent className="!w-full overflow-y-auto sm:!max-w-lg">
        <SheetHeader>
          <SheetTitle>{t(($) => $.release_agent.settings)}</SheetTitle>
          <SheetDescription>{t(($) => $.release_agent.description)}</SheetDescription>
        </SheetHeader>
        <div className="space-y-5 p-4">
          {settings.isPending && <p className="text-body text-muted-foreground">{t(($) => $.review.loading)}</p>}
          {settings.isError && <p role="alert" className="text-body text-destructive">{String(settings.error)}</p>}
          <label className="flex items-start gap-3 rounded-lg border p-3 text-body">
            <input type="checkbox" className="mt-1 size-4 shrink-0" checked={enabled} disabled={!canManage} onChange={(event) => setEnabled(event.target.checked)} />
            <span>{t(($) => $.release_agent.enable)}<span className="mt-1 block text-caption text-muted-foreground">{t(($) => $.release_agent.enable_hint)}</span></span>
          </label>
          <label className="block space-y-2 text-body" htmlFor="release-project">
            <span>{t(($) => $.release_agent.project)}</span>
            <select id="release-project" className={selectClass} value={projectId} disabled={!canManage} onChange={(event) => setProjectId(event.target.value)}>
              <option value="">{t(($) => $.release_agent.project_placeholder)}</option>
              {(projects.data ?? []).map((project) => <option key={project.id} value={project.id}>{project.title}</option>)}
            </select>
          </label>
          <label className="block space-y-2 text-body" htmlFor="release-runtime">
            <span>{t(($) => $.review.runtime)}</span>
            <select id="release-runtime" className={selectClass} value={runtimeId} disabled={!canManage} onChange={(event) => { setRuntimeId(event.target.value); setModel(""); setThinking(""); }}>
              <option value="">{t(($) => $.review.runtime_placeholder)}</option>
              {usable.map((item) => <option key={item.id} value={item.id}>{runtimeDisplayLabel(item)}</option>)}
            </select>
          </label>
          <label className="block space-y-2 text-body" htmlFor="release-model">
            <span>{t(($) => $.review.model)}</span>
            <select id="release-model" className={selectClass} value={model} disabled={!canManage || !runtimeId} onChange={(event) => { setModel(event.target.value); setThinking(""); }}>
              <option value="">{t(($) => $.review.model_default)}</option>
              {models.map((entry) => <option key={entry.id} value={entry.id}>{entry.label || entry.id}</option>)}
            </select>
          </label>
          {levels.length > 0 && <label className="block space-y-2 text-body" htmlFor="release-thinking">
            <span>{t(($) => $.review.thinking)}</span>
            <select id="release-thinking" className={selectClass} value={thinking} disabled={!canManage} onChange={(event) => setThinking(event.target.value)}>
              <option value="">{t(($) => $.review.thinking_default)}</option>
              {levels.map((level) => <option key={level.value} value={level.value}>{level.label}</option>)}
            </select>
          </label>}
          <div className="space-y-2 text-body">
            <div className="flex items-center justify-between gap-2">
              <label htmlFor="release-instructions">{t(($) => $.review.instructions)}</label>
              <Button variant="ghost" size="sm" disabled={!canManage} onClick={() => setInstructions(settings.data?.default_instructions ?? "")}>{t(($) => $.review.restore_default)}</Button>
            </div>
            <textarea id="release-instructions" className="min-h-48 w-full rounded-md border bg-background p-3 text-caption leading-relaxed" value={instructions} disabled={!canManage} onChange={(event) => setInstructions(event.target.value)} />
          </div>
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={onClose}>{t(($) => $.review.cancel)}</Button>
            <Button disabled={!canManage || save.isPending || (enabled && (!projectId || !runtimeId))} onClick={() => save.mutate()}>{save.isPending ? t(($) => $.review.saving) : t(($) => $.review.save)}</Button>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}
