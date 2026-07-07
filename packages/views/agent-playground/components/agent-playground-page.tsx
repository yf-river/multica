"use client";

import { useMemo, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronsUpDown, Loader2, Play, RefreshCw, Scale, Plus, Search, X } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Agent, AgentPlaygroundDetail, CreateAgentPlaygroundExperimentRequest, PromptEvaluationAsset, PromptEvaluationDatasetVersion } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Badge } from "@multica/ui/components/ui/badge";
import { NativeSelect } from "@multica/ui/components/ui/native-select";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import { cn } from "@multica/ui/lib/utils";
import { PageHeader } from "../../layout/page-header";

const keys = {
  experiments: (workspaceId: string) => ["agent-playground", workspaceId, "experiments"] as const,
  detail: (workspaceId: string, id: string | null) => ["agent-playground", workspaceId, "detail", id ?? ""] as const,
  agents: (workspaceId: string) => ["agent-playground", workspaceId, "agents"] as const,
  datasets: (workspaceId: string) => ["agent-playground", workspaceId, "datasets"] as const,
  versions: (workspaceId: string, datasetId: string) => ["agent-playground", workspaceId, "dataset-versions", datasetId] as const,
};

export function AgentPlaygroundPage() {
  const workspaceId = useWorkspaceId();
  const workspacePaths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [name, setName] = useState("Agent 对比实验");
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
  const datasetVersions = datasetVersionsQuery.data?.items ?? [];
  const detail = detailQuery.data ?? null;
  const selectedDatasetVersion = datasetVersions.find((version) => version.id === datasetVersionId);
  const plannedTaskCount = (selectedDatasetVersion?.row_count ?? 0) * selectedAgentIds.length;

  const createMutation = useMutation({
    mutationFn: (data: CreateAgentPlaygroundExperimentRequest) => api.createAgentPlaygroundExperiment(data),
    onSuccess: (created) => {
      toast.success("已创建 Agent 调试实验");
      setSelectedId(created.experiment.id);
      queryClient.invalidateQueries({ queryKey: keys.experiments(workspaceId) });
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : "创建失败"),
  });
  const runMutation = useMutation({
    mutationFn: (id: string) => api.runAgentPlaygroundExperiment(id),
    onSuccess: (updated) => {
      queryClient.setQueryData(keys.detail(workspaceId, updated.experiment.id), updated);
      queryClient.invalidateQueries({ queryKey: keys.experiments(workspaceId) });
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : "运行失败"),
  });
  const syncMutation = useMutation({
    mutationFn: (id: string) => api.syncAgentPlaygroundExperiment(id),
    onSuccess: (updated) => queryClient.setQueryData(keys.detail(workspaceId, updated.experiment.id), updated),
  });
  const judgeMutation = useMutation({
    mutationFn: (id: string) => api.judgeAgentPlaygroundExperiment(id, judgeAgentId ? { judge_agent_id: judgeAgentId } : undefined),
    onSuccess: (updated) => queryClient.setQueryData(keys.detail(workspaceId, updated.experiment.id), updated),
    onError: (error) => toast.error(error instanceof Error ? error.message : "裁判失败"),
  });

  const canCreate = Boolean(name.trim() && selectedAgentIds.length > 0 && datasetId && datasetVersionId);
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
        <div className="text-sm font-semibold">Agent 调试场</div>
        <div className="text-xs text-muted-foreground">用同一批输入对比多个 Agent，手动同步结果，再交给裁判 Agent 评价。</div>
      </PageHeader>
      <div className="grid min-h-0 flex-1 grid-cols-[320px_minmax(0,1fr)] border-t">
        <aside className="min-h-0 border-r bg-muted/20">
          <div className="border-b p-3">
            <div className="text-sm font-medium">实验</div>
            <div className="text-xs text-muted-foreground">{experimentsQuery.data?.total ?? 0} 个实验</div>
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
                  <Badge variant="secondary">{item.status}</Badge>
                </div>
                <div className="mt-1 text-xs text-muted-foreground">{item.input_count} 用例 · {item.agent_count} Agent</div>
              </button>
            ))}
          </div>
        </aside>
        <main className="min-h-0 overflow-y-auto p-5">
          <section className="mb-5 border-b pb-5">
            <div className="mb-3 flex items-center justify-between">
              <div>
                <h2 className="text-base font-semibold">新建调试实验</h2>
                <p className="text-xs text-muted-foreground">选择一个用例库快照，批量对比多个 Agent 的真实执行结果。</p>
              </div>
              <Button onClick={createExperiment} disabled={!canCreate || createMutation.isPending}>
                {createMutation.isPending ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : <Plus className="mr-2 h-4 w-4" />}
                创建实验
              </Button>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <Labeled label="名称"><Input value={name} onChange={(event) => setName(event.target.value)} /></Labeled>
              <Labeled label="描述"><Input value={description} onChange={(event) => setDescription(event.target.value)} /></Labeled>
              <Labeled label="用例库">
                <NativeSelect value={datasetId} onChange={(event) => { setDatasetId(event.target.value); setDatasetVersionId(""); }}>
                  <option value="">选择用例库</option>
                  {datasets.map((dataset: PromptEvaluationAsset) => <option key={dataset.id} value={dataset.id}>{dataset.name}</option>)}
                </NativeSelect>
              </Labeled>
              <Labeled label="快照">
                <NativeSelect value={datasetVersionId} onChange={(event) => setDatasetVersionId(event.target.value)} disabled={!datasetId}>
                  <option value="">选择快照</option>
                  {datasetVersions.map((version: PromptEvaluationDatasetVersion) => (
                    <option key={version.id} value={version.id}>v{version.version} {version.version_label || ""} · {version.row_count} 条</option>
                  ))}
                </NativeSelect>
              </Labeled>
              <Labeled label="裁判 Agent">
                <NativeSelect value={judgeAgentId} onChange={(event) => setJudgeAgentId(event.target.value)}>
                  <option value="">稍后选择</option>
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
                  ? `本次将创建 ${selectedDatasetVersion.row_count} 条用例 × ${selectedAgentIds.length} 个 Agent = ${plannedTaskCount} 个执行任务。`
                  : selectedAgentIds.length > 0
                    ? `已选择 ${selectedAgentIds.length} 个 Agent。选择快照后会显示本次执行任务数。`
                    : "请选择至少 1 个 Agent。"}
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
              busy={runMutation.isPending || syncMutation.isPending || judgeMutation.isPending || detailQuery.isFetching}
            />
          ) : (
            <div className="rounded-md border border-dashed p-8 text-sm text-muted-foreground">暂无实验。创建一个实验后会在这里看到结果矩阵。</div>
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
      <div className="mb-2 text-xs font-medium text-muted-foreground">执行 Agent</div>
      <Popover open={open} onOpenChange={handleOpenChange}>
        <PopoverTrigger
          render={
            <button
              type="button"
              className="flex h-9 w-full items-center justify-between rounded-md border bg-background px-3 text-left text-sm transition-colors hover:bg-muted/50"
            >
              <span className={selectedIds.length > 0 ? "text-foreground" : "text-muted-foreground"}>
                {selectedIds.length > 0 ? `已选择 ${selectedIds.length} 个 Agent` : "搜索并选择执行 Agent"}
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
              placeholder="搜索 Agent"
              className="h-8 min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
            />
            {loading ? <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" /> : null}
          </div>
          <div className="max-h-72 overflow-y-auto p-1">
            {agents.length === 0 ? (
              <div className="p-4 text-sm text-muted-foreground">
                <div>暂无可执行 Agent。</div>
                <a href={agentsHref} className="mt-2 inline-flex text-foreground underline-offset-4 hover:underline">
                  去智能体页面创建
                </a>
              </div>
            ) : filteredAgents.length === 0 ? (
              <div className="p-4 text-sm text-muted-foreground">没有匹配的 Agent</div>
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
                aria-label={`移除 ${agent.name}`}
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
  busy,
}: {
  detail: AgentPlaygroundDetail;
  resultsByCell: Map<string, AgentPlaygroundDetail["results"][number]>;
  judgementByInput: Map<string, AgentPlaygroundDetail["judgements"][number]>;
  onRun: () => void;
  onSync: () => void;
  onJudge: () => void;
  busy: boolean;
}) {
  return (
    <section>
      <div className="mb-3 flex items-center justify-between">
        <div>
          <h2 className="text-base font-semibold">{detail.experiment.name}</h2>
          <p className="text-xs text-muted-foreground">{detail.inputs.length} 条输入 · {detail.agents.length} 个 Agent</p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={onSync} disabled={busy}><RefreshCw className="mr-2 h-4 w-4" />同步</Button>
          <Button variant="outline" onClick={onJudge} disabled={busy}><Scale className="mr-2 h-4 w-4" />裁判</Button>
          <Button onClick={onRun} disabled={busy}><Play className="mr-2 h-4 w-4" />运行</Button>
        </div>
      </div>
      <div className="overflow-x-auto rounded-md border">
        <table className="w-full min-w-[900px] border-collapse text-sm">
          <thead className="bg-muted/50">
            <tr>
              <th className="w-64 border-b border-r p-3 text-left font-medium">输入</th>
              {detail.agents.map((agent) => <th key={agent.id} className="border-b border-r p-3 text-left font-medium">{agent.agent_name}</th>)}
              <th className="border-b p-3 text-left font-medium">裁判</th>
            </tr>
          </thead>
          <tbody>
            {detail.inputs.map((input) => {
              const judgement = judgementByInput.get(input.id);
              return (
                <tr key={input.id} className="align-top">
                  <td className="border-r border-t p-3">
                    <div className="font-medium">{input.name || `用例 ${input.row_index + 1}`}</div>
                    <div className="mt-1 line-clamp-4 text-xs text-muted-foreground">{input.input}</div>
                  </td>
                  {detail.agents.map((agent) => {
                    const result = resultsByCell.get(`${input.id}:${agent.id}`);
                    return (
                      <td key={agent.id} className="border-r border-t p-3">
                        {result ? <ResultCell status={result.status} output={result.output} error={result.error} /> : <span className="text-xs text-muted-foreground">未运行</span>}
                      </td>
                    );
                  })}
                  <td className="border-t p-3">
                    {judgement ? <ResultCell status={judgement.status} output={judgement.output} error="" /> : <span className="text-xs text-muted-foreground">未裁判</span>}
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
  return (
    <div>
      <Badge variant={status === "completed" ? "default" : status === "failed" ? "destructive" : "secondary"}>{status}</Badge>
      <div className="mt-2 whitespace-pre-wrap text-xs leading-5">{output || error || "暂无输出"}</div>
    </div>
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
