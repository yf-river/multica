"use client";

import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronsUpDown, Loader2, Play, RefreshCw, Scale, Plus, Search, X } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useWSEvent, useWSReconnect } from "@multica/core/realtime";
import type { Agent, AgentPlaygroundDetail, CreateAgentPlaygroundExperimentRequest, PromptEvaluationAsset, PromptEvaluationDatasetVersion } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Badge } from "@multica/ui/components/ui/badge";
import { NativeSelect } from "@multica/ui/components/ui/native-select";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import { cn } from "@multica/ui/lib/utils";
import { PageHeader } from "../../layout/page-header";
import { useT } from "../../i18n";

const keys = {
  experiments: (workspaceId: string) => ["agent-playground", workspaceId, "experiments"] as const,
  detail: (workspaceId: string, id: string | null) => ["agent-playground", workspaceId, "detail", id ?? ""] as const,
  agents: (workspaceId: string) => ["agent-playground", workspaceId, "agents"] as const,
  datasets: (workspaceId: string) => ["agent-playground", workspaceId, "datasets"] as const,
  versions: (workspaceId: string, datasetId: string) => ["agent-playground", workspaceId, "dataset-versions", datasetId] as const,
};

const AGENT_PLAYGROUND_SYNC_INTERVAL_MS = 2000;
const TERMINAL_AGENT_PLAYGROUND_STATUSES = new Set(["completed", "failed", "cancelled"]);

function isTerminalAgentPlaygroundStatus(status: string): boolean {
  return TERMINAL_AGENT_PLAYGROUND_STATUSES.has(status);
}

function hasActiveAgentPlaygroundWork(detail: AgentPlaygroundDetail | null): boolean {
  if (!detail) return false;
  return (
    detail.results.some((result) => result.task_id && !isTerminalAgentPlaygroundStatus(result.status)) ||
    detail.judgements.some((judgement) => judgement.task_id && !isTerminalAgentPlaygroundStatus(judgement.status))
  );
}

export function AgentPlaygroundPage() {
  const { t } = useT("agent-playground");
  const workspaceId = useWorkspaceId();
  const workspacePaths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [name, setName] = useState(() => t(($) => $.create.default_name));
  const [description, setDescription] = useState("");
  const [datasetId, setDatasetId] = useState("");
  const [datasetVersionId, setDatasetVersionId] = useState("");
  const [judgeAgentId, setJudgeAgentId] = useState("");
  const [selectedAgentIds, setSelectedAgentIds] = useState<string[]>([]);

  const experimentsQuery = useQuery({
    queryKey: keys.experiments(workspaceId),
    queryFn: () => api.listAgentPlaygroundExperiments(),
  });
  const selectedExperimentId = selectedId ?? experimentsQuery.data?.items[0]?.id ?? null;
  const detailQuery = useQuery({
    queryKey: keys.detail(workspaceId, selectedExperimentId),
    queryFn: () => api.getAgentPlaygroundExperiment(selectedExperimentId!),
    enabled: Boolean(selectedExperimentId),
  });
  const agentsQuery = useQuery({
    queryKey: keys.agents(workspaceId),
    queryFn: () => api.listAgents({ include_archived: false }),
  });
  const datasetsQuery = useQuery({
    queryKey: keys.datasets(workspaceId),
    queryFn: () => api.listPromptEvaluationAssets({ asset_type: "数据集" }),
  });
  const datasetVersionsQuery = useQuery({
    queryKey: keys.versions(workspaceId, datasetId),
    queryFn: () => api.listPromptEvaluationDatasetVersions(datasetId, 20),
    enabled: Boolean(datasetId),
  });
  const agents = agentsQuery.data ?? [];
  const datasets = datasetsQuery.data?.items ?? [];
  const datasetVersions = useMemo(() => datasetVersionsQuery.data?.items ?? [], [datasetVersionsQuery.data?.items]);
  const detail = detailQuery.data ?? null;
  const selectedDatasetVersion = datasetVersions.find((version) => version.id === datasetVersionId);
  const plannedTaskCount = (selectedDatasetVersion?.row_count ?? 0) * selectedAgentIds.length;
  const autoSyncActive = hasActiveAgentPlaygroundWork(detail);

  const createMutation = useMutation({
    mutationFn: (data: CreateAgentPlaygroundExperimentRequest) => api.createAgentPlaygroundExperiment(data),
    onSuccess: (created) => {
      toast.success(t(($) => $.toast.created));
      setSelectedId(created.experiment.id);
      queryClient.invalidateQueries({ queryKey: keys.experiments(workspaceId) });
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : t(($) => $.toast.create_failed)),
  });
  const runMutation = useMutation({
    mutationFn: (id: string) => api.runAgentPlaygroundExperiment(id),
    onSuccess: (updated) => {
      queryClient.setQueryData(keys.detail(workspaceId, updated.experiment.id), updated);
      queryClient.invalidateQueries({ queryKey: keys.experiments(workspaceId) });
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : t(($) => $.toast.run_failed)),
  });
  const syncMutation = useMutation({
    mutationFn: (id: string) => api.syncAgentPlaygroundExperiment(id),
    onSuccess: (updated) => {
      queryClient.setQueryData(keys.detail(workspaceId, updated.experiment.id), updated);
      queryClient.invalidateQueries({ queryKey: keys.experiments(workspaceId) });
    },
  });
  const { isPending: syncPending, mutate: syncExperiment } = syncMutation;
  const judgeMutation = useMutation({
    mutationFn: (id: string) => api.judgeAgentPlaygroundExperiment(id, judgeAgentId ? { judge_agent_id: judgeAgentId } : undefined),
    onSuccess: (updated) => queryClient.setQueryData(keys.detail(workspaceId, updated.experiment.id), updated),
    onError: (error) => toast.error(error instanceof Error ? error.message : t(($) => $.toast.judge_failed)),
  });

  const canCreate = Boolean(name.trim() && selectedAgentIds.length > 0 && datasetId && datasetVersionId);
  const createMissingReasons = useMemo(() => {
    const reasons: string[] = [];
    if (!name.trim()) reasons.push(t(($) => $.create.missing_name));
    if (!datasetId) {
      reasons.push(t(($) => $.create.missing_dataset));
    } else if (!datasetVersionId) {
      if (datasetVersionsQuery.isFetching) {
        reasons.push(t(($) => $.create.missing_snapshot_loading));
      } else if (datasetVersions.length === 0) {
        reasons.push(t(($) => $.create.missing_snapshot_empty));
      } else {
        reasons.push(t(($) => $.create.missing_snapshot));
      }
    }
    if (selectedAgentIds.length === 0) reasons.push(t(($) => $.create.missing_agents));
    return reasons;
  }, [datasetId, datasetVersionId, datasetVersions.length, datasetVersionsQuery.isFetching, name, selectedAgentIds.length, t]);
  const createHint = canCreate
    ? ""
    : t(($) => $.create.missing_hint, { reasons: createMissingReasons.join("、") });
  const resultsByCell = useMemo(() => {
    const map = new Map<string, AgentPlaygroundDetail["results"][number]>();
    for (const result of detail?.results ?? []) {
      map.set(`${result.input_id}:${result.experiment_agent_id}`, result);
    }
    return map;
  }, [detail?.results]);
  const judgementByInput = useMemo(() => {
    const map = new Map<string, AgentPlaygroundDetail["judgements"][number]>();
    for (const judgement of detail?.judgements ?? []) {
      map.set(judgement.input_id, judgement);
    }
    return map;
  }, [detail?.judgements]);

  useEffect(() => {
    if (!datasetId) return;
    if (datasetVersions.length === 0) {
      if (datasetVersionId) setDatasetVersionId("");
      return;
    }
    if (!datasetVersionId || !datasetVersions.some((version) => version.id === datasetVersionId)) {
      setDatasetVersionId(datasetVersions[0]!.id);
    }
  }, [datasetId, datasetVersionId, datasetVersions]);

  const syncCurrentExperiment = useCallback((force = false) => {
    if (!selectedExperimentId || syncPending) return;
    if (!force && !hasActiveAgentPlaygroundWork(detail)) return;
    syncExperiment(selectedExperimentId);
  }, [detail, selectedExperimentId, syncExperiment, syncPending]);

  useEffect(() => {
    if (!autoSyncActive) return;
    const interval = window.setInterval(() => syncCurrentExperiment(), AGENT_PLAYGROUND_SYNC_INTERVAL_MS);
    return () => window.clearInterval(interval);
  }, [autoSyncActive, syncCurrentExperiment]);

  const handleTaskLifecycleEvent = useCallback(() => {
    syncCurrentExperiment();
  }, [syncCurrentExperiment]);

  const handleWSReconnect = useCallback(() => {
    syncCurrentExperiment(true);
  }, [syncCurrentExperiment]);

  useWSReconnect(handleWSReconnect);
  useWSEvent("task:completed", handleTaskLifecycleEvent);
  useWSEvent("task:failed", handleTaskLifecycleEvent);
  useWSEvent("task:cancelled", handleTaskLifecycleEvent);
  useWSEvent("chat:done", handleTaskLifecycleEvent);

  function createExperiment() {
    if (!canCreate) return;
    createMutation.mutate({
      name: name.trim(),
      description: description.trim(),
      dataset_asset_id: datasetId,
      dataset_version_id: datasetVersionId,
      judge_agent_id: judgeAgentId || undefined,
      agent_ids: selectedAgentIds,
    });
  }

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <PageHeader className="h-auto min-h-12 flex-col items-start justify-center gap-0.5 py-2">
        <div className="text-sm font-semibold">{t(($) => $.page_title)}</div>
        <div className="text-xs text-muted-foreground">{t(($) => $.page_description)}</div>
      </PageHeader>
      <div className="grid min-h-0 flex-1 grid-cols-[320px_minmax(0,1fr)] border-t">
        <aside className="min-h-0 border-r bg-muted/20">
          <div className="border-b p-3">
            <div className="text-sm font-medium">{t(($) => $.experiments)}</div>
            <div className="text-xs text-muted-foreground">
              {t(($) => $.experiment_count, { count: experimentsQuery.data?.total ?? 0 })}
            </div>
          </div>
          <div className="max-h-full overflow-y-auto">
            {(experimentsQuery.data?.items ?? []).map((item) => (
              <button
                key={item.id}
                type="button"
                onClick={() => setSelectedId(item.id)}
                className={`w-full border-b px-3 py-3 text-left hover:bg-muted/60 ${selectedExperimentId === item.id ? "bg-muted" : ""}`}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate text-sm font-medium">{item.name}</span>
                  <PlaygroundStatusBadge status={item.status} />
                </div>
                <div className="mt-1 text-xs text-muted-foreground">
                  {t(($) => $.experiment_summary, {
                    inputCount: item.input_count,
                    agentCount: item.agent_count,
                  })}
                </div>
              </button>
            ))}
          </div>
        </aside>
        <main className="min-h-0 overflow-y-auto p-5">
          <section className="mb-5 border-b pb-5">
            <div className="mb-3 flex items-center justify-between">
              <div>
                <h2 className="text-base font-semibold">{t(($) => $.create.title)}</h2>
                <p className="text-xs text-muted-foreground">{t(($) => $.create.description)}</p>
              </div>
              <div className="flex flex-col items-end gap-1">
                <Button onClick={createExperiment} disabled={!canCreate || createMutation.isPending}>
                  {createMutation.isPending ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Plus className="mr-2 h-4 w-4" />}
                  {t(($) => $.create.button)}
                </Button>
                {createHint ? <div className="text-xs text-muted-foreground">{createHint}</div> : null}
              </div>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <Labeled label={t(($) => $.create.name)}><Input value={name} onChange={(event) => setName(event.target.value)} /></Labeled>
              <Labeled label={t(($) => $.create.description_label)}><Input value={description} onChange={(event) => setDescription(event.target.value)} /></Labeled>
              <Labeled label={t(($) => $.create.dataset)}>
                <NativeSelect value={datasetId} onChange={(event) => { setDatasetId(event.target.value); setDatasetVersionId(""); }}>
                  <option value="">{t(($) => $.create.dataset_placeholder)}</option>
                  {datasets.map((dataset: PromptEvaluationAsset) => <option key={dataset.id} value={dataset.id}>{dataset.name}</option>)}
                </NativeSelect>
              </Labeled>
              <Labeled label={t(($) => $.create.snapshot)}>
                <NativeSelect value={datasetVersionId} onChange={(event) => setDatasetVersionId(event.target.value)} disabled={!datasetId}>
                  <option value="">{t(($) => $.create.snapshot_placeholder)}</option>
                  {datasetVersions.map((version: PromptEvaluationDatasetVersion) => (
                    <option key={version.id} value={version.id}>
                      {version.version_label
                        ? t(($) => $.create.snapshot_option_labeled, {
                          version: version.version,
                          label: version.version_label,
                          count: version.row_count,
                        })
                        : t(($) => $.create.snapshot_option, {
                          version: version.version,
                          count: version.row_count,
                        })}
                    </option>
                  ))}
                </NativeSelect>
              </Labeled>
              <Labeled label={t(($) => $.create.judge_agent)}>
                <NativeSelect value={judgeAgentId} onChange={(event) => setJudgeAgentId(event.target.value)}>
                  <option value="">{t(($) => $.create.judge_later)}</option>
                  {agents.map((agent: Agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}
                </NativeSelect>
              </Labeled>
            </div>
            <div className="mt-3">
              <AgentMultiSelect
                agents={agents}
                selectedIds={selectedAgentIds}
                loading={agentsQuery.isFetching}
                agentsHref={workspacePaths.agents()}
                onChange={setSelectedAgentIds}
                onOpen={() => { void agentsQuery.refetch(); }}
              />
              <div className="mt-2 text-xs text-muted-foreground">
                {selectedDatasetVersion && selectedAgentIds.length > 0
                  ? t(($) => $.create.planned_tasks, {
                    caseCount: selectedDatasetVersion.row_count,
                    agentCount: selectedAgentIds.length,
                    taskCount: plannedTaskCount,
                  })
                  : selectedAgentIds.length > 0
                    ? t(($) => $.create.selected_agents_hint, { count: selectedAgentIds.length })
                    : t(($) => $.create.select_agent_hint)}
              </div>
            </div>
          </section>

          {detail ? (
            <ExperimentDetail
              detail={detail}
              resultsByCell={resultsByCell}
              judgementByInput={judgementByInput}
              onRun={() => runMutation.mutate(detail.experiment.id)}
              onSync={() => syncMutation.mutate(detail.experiment.id)}
              onJudge={() => judgeMutation.mutate(detail.experiment.id)}
              autoSyncing={autoSyncActive}
              running={runMutation.isPending}
              syncing={syncMutation.isPending}
              judging={judgeMutation.isPending}
              loading={detailQuery.isFetching}
            />
          ) : (
            <div className="rounded-md border border-dashed p-8 text-sm text-muted-foreground">
              {t(($) => $.empty_experiments)}
            </div>
          )}
        </main>
      </div>
    </div>
  );
}

function AgentMultiSelect({
  agents,
  selectedIds,
  loading,
  agentsHref,
  onChange,
  onOpen,
}: {
  agents: Agent[];
  selectedIds: string[];
  loading: boolean;
  agentsHref: string;
  onChange: (ids: string[]) => void;
  onOpen: () => void;
}) {
  const { t } = useT("agent-playground");
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const selectedIdSet = useMemo(() => new Set(selectedIds), [selectedIds]);
  const selectedAgents = agents.filter((agent) => selectedIdSet.has(agent.id));
  const normalizedQuery = query.trim().toLowerCase();
  const filteredAgents = normalizedQuery
    ? agents.filter((agent) => agent.name.toLowerCase().includes(normalizedQuery))
    : agents;

  function handleOpenChange(nextOpen: boolean) {
    setOpen(nextOpen);
    if (nextOpen) {
      onOpen();
    } else {
      setQuery("");
    }
  }

  function toggleAgent(agentId: string) {
    if (selectedIdSet.has(agentId)) {
      onChange(selectedIds.filter((id) => id !== agentId));
      return;
    }
    onChange([...selectedIds, agentId]);
  }

  function removeAgent(agentId: string) {
    onChange(selectedIds.filter((id) => id !== agentId));
  }

  return (
    <div>
      <div className="mb-2 text-xs font-medium text-muted-foreground">
        {t(($) => $.agent_picker.label)}
      </div>
      <Popover open={open} onOpenChange={handleOpenChange}>
        <PopoverTrigger
          render={
            <button
              type="button"
              className="flex h-9 w-full items-center justify-between rounded-md border bg-background px-3 text-left text-sm transition-colors hover:bg-muted/50"
            >
              <span className={selectedIds.length > 0 ? "text-foreground" : "text-muted-foreground"}>
                {selectedIds.length > 0
                  ? t(($) => $.agent_picker.selected, { count: selectedIds.length })
                  : t(($) => $.agent_picker.trigger)}
              </span>
              <ChevronsUpDown className="h-4 w-4 text-muted-foreground" />
            </button>
          }
        />
        <PopoverContent align="start" className="w-[420px] p-0">
          <div className="flex items-center gap-2 border-b px-3 py-2">
            <Search className="h-4 w-4 text-muted-foreground" />
            <input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t(($) => $.agent_picker.search_placeholder)}
              className="h-8 min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
            />
            {loading ? <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" /> : null}
          </div>
          <div className="max-h-72 overflow-y-auto p-1">
            {agents.length === 0 ? (
              <div className="p-4 text-sm text-muted-foreground">
                <div>{t(($) => $.agent_picker.empty)}</div>
                <a href={agentsHref} className="mt-2 inline-flex text-foreground underline-offset-4 hover:underline">
                  {t(($) => $.agent_picker.create_agent)}
                </a>
              </div>
            ) : filteredAgents.length === 0 ? (
              <div className="p-4 text-sm text-muted-foreground">
                {t(($) => $.agent_picker.no_matches)}
              </div>
            ) : (
              filteredAgents.map((agent) => {
                const selected = selectedIdSet.has(agent.id);
                return (
                  <button
                    key={agent.id}
                    type="button"
                    onClick={() => toggleAgent(agent.id)}
                    className={cn(
                      "flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-sm hover:bg-muted",
                      selected && "bg-muted"
                    )}
                  >
                    <span className="min-w-0 flex-1 truncate">{agent.name}</span>
                    <Check className={cn("h-4 w-4", selected ? "opacity-100" : "opacity-0")} />
                  </button>
                );
              })
            )}
          </div>
        </PopoverContent>
      </Popover>
      {selectedAgents.length > 0 ? (
        <div className="mt-2 flex flex-wrap gap-2">
          {selectedAgents.map((agent) => (
            <span key={agent.id} className="inline-flex h-7 items-center gap-1 rounded-md border bg-muted px-2 text-xs">
              <span className="max-w-48 truncate">{agent.name}</span>
              <button
                type="button"
                onClick={() => removeAgent(agent.id)}
                className="rounded-sm text-muted-foreground hover:text-foreground"
                aria-label={t(($) => $.agent_picker.remove_aria, { name: agent.name })}
              >
                <X className="h-3 w-3" />
              </button>
            </span>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function ExperimentDetail({
  detail,
  resultsByCell,
  judgementByInput,
  onRun,
  onSync,
  onJudge,
  autoSyncing,
  running,
  syncing,
  judging,
  loading,
}: {
  detail: AgentPlaygroundDetail;
  resultsByCell: Map<string, AgentPlaygroundDetail["results"][number]>;
  judgementByInput: Map<string, AgentPlaygroundDetail["judgements"][number]>;
  onRun: () => void;
  onSync: () => void;
  onJudge: () => void;
  autoSyncing: boolean;
  running: boolean;
  syncing: boolean;
  judging: boolean;
  loading: boolean;
}) {
  const { t } = useT("agent-playground");
  const busy = running || syncing || judging || loading;
  return (
    <section>
      <div className="mb-3 flex items-center justify-between">
        <div>
          <h2 className="text-base font-semibold">{detail.experiment.name}</h2>
          <p className="text-xs text-muted-foreground">
            {t(($) => $.detail.summary, {
              inputCount: detail.inputs.length,
              agentCount: detail.agents.length,
            })}
          </p>
        </div>
        <div className="flex items-center gap-3">
          {autoSyncing ? (
            <div className="inline-flex items-center gap-1 text-xs text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              {t(($) => $.detail.auto_syncing)}
            </div>
          ) : null}
          <div className="flex gap-2">
            <Button variant="outline" onClick={onSync} disabled={busy}>
              <RefreshCw className={`mr-2 h-4 w-4 ${syncing ? "animate-spin" : ""}`} />
              {t(($) => $.detail.sync)}
            </Button>
            <Button variant="outline" onClick={onJudge} disabled={busy}>
              {judging ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Scale className="mr-2 h-4 w-4" />}
              {t(($) => $.detail.judge)}
            </Button>
            <Button onClick={onRun} disabled={busy}>
              {running ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Play className="mr-2 h-4 w-4" />}
              {t(($) => $.detail.run)}
            </Button>
          </div>
        </div>
      </div>
      <div className="overflow-x-auto rounded-md border">
        <table className="w-full min-w-[900px] border-collapse text-sm">
          <thead className="bg-muted/50">
            <tr>
              <th className="w-64 border-b border-r p-3 text-left font-medium">
                {t(($) => $.detail.input)}
              </th>
              {detail.agents.map((agent) => <th key={agent.id} className="border-b border-r p-3 text-left font-medium">{agent.agent_name}</th>)}
              <th className="border-b p-3 text-left font-medium">
                {t(($) => $.detail.judge_column)}
              </th>
            </tr>
          </thead>
          <tbody>
            {detail.inputs.map((input) => {
              const judgement = judgementByInput.get(input.id);
              return (
                <tr key={input.id} className="align-top">
                  <td className="border-r border-t p-3">
                    <div className="font-medium">
                      {input.name || t(($) => $.detail.case_name, { count: input.row_index + 1 })}
                    </div>
                    <div className="mt-1 line-clamp-4 text-xs text-muted-foreground">{input.input}</div>
                  </td>
                  {detail.agents.map((agent) => {
                    const result = resultsByCell.get(`${input.id}:${agent.id}`);
                    return (
                      <td key={agent.id} className="border-r border-t p-3">
                        {result
                          ? <ResultCell status={result.status} output={result.output} error={result.error} />
                          : <span className="text-xs text-muted-foreground">{t(($) => $.detail.not_run)}</span>}
                      </td>
                    );
                  })}
                  <td className="border-t p-3">
                    {judgement
                      ? <ResultCell status={judgement.status} output={judgement.output} error="" />
                      : <span className="text-xs text-muted-foreground">{t(($) => $.detail.not_judged)}</span>}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function ResultCell({ status, output, error }: { status: string; output: string; error: string }) {
  const { t } = useT("agent-playground");
  return (
    <div>
      <PlaygroundStatusBadge status={status} />
      <div className="mt-2 whitespace-pre-wrap text-xs leading-5">
        {output || error || t(($) => $.detail.no_output)}
      </div>
    </div>
  );
}

function PlaygroundStatusBadge({ status }: { status: string }) {
  const { t } = useT("agent-playground");
  const label = (() => {
    switch (status) {
      case "draft": return t(($) => $.status.draft);
      case "queued": return t(($) => $.status.queued);
      case "running": return t(($) => $.status.running);
      case "completed": return t(($) => $.status.completed);
      case "failed": return t(($) => $.status.failed);
      case "cancelled": return t(($) => $.status.cancelled);
      default: return status;
    }
  })();
  return (
    <Badge variant={status === "completed" ? "default" : status === "failed" ? "destructive" : "secondary"}>
      {label}
    </Badge>
  );
}

function Labeled({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="grid gap-1">
      <span className="text-xs font-medium text-muted-foreground">{label}</span>
      {children}
    </label>
  );
}
