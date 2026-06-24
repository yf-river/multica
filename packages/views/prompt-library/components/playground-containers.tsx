"use client";

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpenText, TerminalSquare } from "lucide-react";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { TRAINING_WORKBENCH_VIEW_BY_TAB, trainingWorkbenchPath } from "@multica/core/training";
import type {
  Agent,
  PromptEvaluationRuntimeReadiness,
  PromptLibraryItem,
} from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { PageHeader } from "../../layout/page-header";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { AgentPlaygroundPromptList, PromptPlaygroundPromptList } from "./playground-prompt-lists";
import { AgentPlaygroundWorkbench, PromptPlaygroundWorkbench } from "./playground-workbenches";
import { DEFAULT_AGENT_MODEL, itemToDraft, valuesToDebugText } from "./prompt-library-request-builders";
import { legacyTrainingSelectedPromptStorageKeys, trainingSelectedPromptStorageKey } from "./prompt-selection-storage";
import { useAgentPlaygroundActions, usePromptPlaygroundActions } from "./use-prompt-playground-actions";

const DEFAULT_AGENT_RUNTIME_READINESS: PromptEvaluationRuntimeReadiness = {
  status: "缺失",
  label: "Codex 检查中",
  detail: "正在检查当前工作区的 Codex 运行时就绪状态。",
  fix: "等待检查完成；如果持续缺失，请安装并配置 Codex，启动 Multica 守护进程。",
  model: DEFAULT_AGENT_MODEL,
  runtime: null,
  last_seen_age_seconds: -1,
  checked_at: "",
};

const promptPlaygroundKeys = {
  list: (workspaceId: string) => ["prompt-playground", workspaceId, "prompt-list"] as const,
  assets: (workspaceId: string) => ["prompt-playground", workspaceId, "evaluation-assets"] as const,
  runs: (workspaceId: string) => ["prompt-playground", workspaceId, "evaluation-runs"] as const,
};

const agentPlaygroundKeys = {
  list: (workspaceId: string) => ["agent-playground", workspaceId, "prompt-list"] as const,
  assets: (workspaceId: string) => ["agent-playground", workspaceId, "evaluation-assets"] as const,
  cases: (workspaceId: string) => ["agent-playground", workspaceId, "evaluation-cases"] as const,
  runs: (workspaceId: string) => ["agent-playground", workspaceId, "evaluation-runs"] as const,
  runtimeReadiness: (workspaceId: string) => ["agent-playground", workspaceId, "runtime-readiness"] as const,
  agents: (workspaceId: string) => ["agent-playground", workspaceId, "agents"] as const,
};

export function PromptPlaygroundContainer() {
  const workspaceId = useWorkspaceId();
  const queryClient = useQueryClient();
  const [query, setQuery] = useState("");
  const selection = usePlaygroundPromptSelection("prompt-playground", workspaceId);

  useEffect(() => {
    document.title = "训练与评估 · 提示词调试场";
  }, []);

  const listQuery = useQuery({
    queryKey: promptPlaygroundKeys.list(workspaceId ?? ""),
    queryFn: () => api.listPromptLibraryItems(),
    enabled: !!workspaceId,
  });
  const assetQuery = useQuery({
    queryKey: promptPlaygroundKeys.assets(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationAssets(),
    enabled: !!workspaceId,
  });
  const runQuery = useQuery({
    queryKey: promptPlaygroundKeys.runs(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationRuns({ limit: 100 }),
    enabled: !!workspaceId,
  });

  const items = listQuery.data?.items ?? [];
  const filteredItems = useFilteredPromptItems(items, query);
  const selected = selection.resolve(items);
  const draft = useMemo(() => selected ? itemToDraft(selected) : emptyPlaygroundDraft(), [selected]);
  const actions = usePromptPlaygroundActions({
    draft,
    selected,
    items,
    selectedPromptStorageKey: selection.storageKey,
    onAssetsChanged: () => queryClient.invalidateQueries({ queryKey: promptPlaygroundKeys.assets(workspaceId ?? "") }),
    onCasesChanged: () => undefined,
    onExperimentDimensionsChanged: () => undefined,
    onRunsChanged: () => queryClient.invalidateQueries({ queryKey: promptPlaygroundKeys.runs(workspaceId ?? "") }),
    onSummaryChanged: () => undefined,
  });

  useEffect(() => {
    if (!selected) return;
    actions.setDebugValuesText(valuesToDebugText(selected.variables));
  }, [actions.setDebugValuesText, selected]);

  return (
    <PlaygroundPageShell
      testId="prompt-playground-page-shell"
      icon="prompt"
      title="提示词调试场"
      subtitle="本地模板渲染、变量检查和调试记录，不创建智能体任务。"
      count={items.length}
      countLabel="提示词"
      contract="本地渲染 · 不启动智能体"
      contractVariant="outline"
    >
      <div className="flex min-h-0 flex-1 flex-col md:grid md:grid-cols-[280px_minmax(0,1fr)]" data-testid="prompt-playground-workbench">
        <PromptPlaygroundPromptList
          query={query}
          onQueryChange={setQuery}
          loading={listQuery.isLoading}
          items={filteredItems}
          selectedId={selected?.id ?? null}
          onSelect={selection.select}
        />
        <main className="min-h-0 overflow-y-auto p-4 md:p-6">
          <PromptPlaygroundWorkbench
            selected={selected}
            debugValuesText={actions.debugValuesText}
            onDebugValuesTextChange={actions.setDebugValuesText}
            debugResult={actions.debugResult}
            runningDebug={actions.runningDebug}
            runs={runQuery.data?.items ?? []}
            assets={assetQuery.data?.items ?? []}
            loading={assetQuery.isLoading || runQuery.isLoading}
            onRunDebug={actions.runDebug}
          />
        </main>
      </div>
    </PlaygroundPageShell>
  );
}

export function AgentPlaygroundContainer() {
  const workspaceId = useWorkspaceId();
  const workspacePaths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const [query, setQuery] = useState("");
  const [selectedExecutionAgentId, setSelectedExecutionAgentId] = useState("__auto__");
  const selection = usePlaygroundPromptSelection("agent-playground", workspaceId);
  const runHistoryPath = trainingWorkbenchPath(workspacePaths.training(), TRAINING_WORKBENCH_VIEW_BY_TAB["运行历史"]);

  useEffect(() => {
    document.title = "训练与评估 · 智能体调试场";
  }, []);

  const listQuery = useQuery({
    queryKey: agentPlaygroundKeys.list(workspaceId ?? ""),
    queryFn: () => api.listPromptLibraryItems(),
    enabled: !!workspaceId,
  });
  const assetQuery = useQuery({
    queryKey: agentPlaygroundKeys.assets(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationAssets(),
    enabled: !!workspaceId,
  });
  const caseQuery = useQuery({
    queryKey: agentPlaygroundKeys.cases(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationCases(),
    enabled: !!workspaceId,
  });
  const runQuery = useQuery({
    queryKey: agentPlaygroundKeys.runs(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationRuns({ limit: 100 }),
    enabled: !!workspaceId,
  });
  const runtimeReadinessQuery = useQuery({
    queryKey: agentPlaygroundKeys.runtimeReadiness(workspaceId ?? ""),
    queryFn: () => api.getPromptEvaluationRuntimeReadiness(),
    enabled: !!workspaceId,
  });
  const agentQuery = useQuery<Agent[]>({
    queryKey: agentPlaygroundKeys.agents(workspaceId ?? ""),
    queryFn: () => api.listAgents({ workspace_id: workspaceId ?? undefined }),
    enabled: !!workspaceId,
  });
  const runtimeQuery = useQuery({ ...runtimeListOptions(workspaceId ?? ""), enabled: !!workspaceId });

  const items = listQuery.data?.items ?? [];
  const cases = caseQuery.data?.items ?? [];
  const runs = runQuery.data?.items ?? [];
  const assets = assetQuery.data?.items ?? [];
  const filteredItems = useFilteredPromptItems(items, query);
  const selected = selection.resolve(items);
  const runtimeReadiness = runtimeReadinessQuery.data ?? DEFAULT_AGENT_RUNTIME_READINESS;
  const agents = agentQuery.data ?? [];
  const selectedExecutionAgent = selectedExecutionAgentId === "__auto__"
    ? null
    : agents.find((agent) => agent.id === selectedExecutionAgentId && !agent.archived_at) ?? null;
  const draft = useMemo(() => selected ? itemToDraft(selected) : emptyPlaygroundDraft(), [selected]);
  const actions = useAgentPlaygroundActions({
    draft,
    selected,
    agentRuntimeReadiness: runtimeReadiness,
    selectedExecutionAgent,
    onAssetsChanged: () => queryClient.invalidateQueries({ queryKey: agentPlaygroundKeys.assets(workspaceId ?? "") }),
    onCasesChanged: () => queryClient.invalidateQueries({ queryKey: agentPlaygroundKeys.cases(workspaceId ?? "") }),
    onExperimentDimensionsChanged: () => undefined,
    onRunsChanged: () => queryClient.invalidateQueries({ queryKey: agentPlaygroundKeys.runs(workspaceId ?? "") }),
    onSummaryChanged: () => undefined,
  });

  useEffect(() => {
    if (!selected) return;
    actions.setDebugValuesText(valuesToDebugText(selected.variables));
  }, [actions.setDebugValuesText, selected]);

  return (
    <PlaygroundPageShell
      testId="agent-playground-page-shell"
      icon="agent"
      title="智能体调试场"
      subtitle="选择执行智能体和运行时，创建真实任务并回写链路追踪、消息、用量和耗时。"
      count={items.length}
      countLabel="执行目标"
      contract="真实任务 · 写回观测证据"
      contractVariant="secondary"
    >
      <div className="flex min-h-0 flex-1 flex-col xl:grid xl:grid-cols-[minmax(0,1fr)_360px]" data-testid="agent-playground-workbench">
        <main className="min-h-0 overflow-y-auto p-4 md:p-6" data-testid="agent-playground-execution-stage">
          <AgentPlaygroundWorkbench
            selected={selected}
            debugValuesText={actions.debugValuesText}
            onDebugValuesTextChange={actions.setDebugValuesText}
            debugResult={actions.debugResult}
            agentExpectedText={actions.agentExpectedText}
            onAgentExpectedTextChange={actions.setAgentExpectedText}
            runtimeReadiness={runtimeReadiness}
            runtimeLoading={runtimeReadinessQuery.isLoading}
            agents={agents}
            runtimes={runtimeQuery.data ?? []}
            executionCatalogLoading={agentQuery.isLoading || runtimeQuery.isLoading}
            selectedExecutionAgentId={selectedExecutionAgentId}
            onSelectedExecutionAgentIdChange={setSelectedExecutionAgentId}
            saving={actions.creatingAgentPackage}
            runningAgent={actions.runningAgent}
            runs={runs}
            assets={assets}
            cases={cases}
            loading={assetQuery.isLoading || caseQuery.isLoading || runQuery.isLoading}
            onSaveAgentDebugPackage={actions.saveAgentDebugPackage}
            onRunAgentDebugPackage={actions.runAgentDebugPackage}
            runHistoryHrefForRun={(runId) => `${runHistoryPath}?run=${encodeURIComponent(runId)}`}
          />
        </main>
        <AgentPlaygroundPromptList
          query={query}
          onQueryChange={setQuery}
          loading={listQuery.isLoading}
          items={filteredItems}
          selectedId={selected?.id ?? null}
          onSelect={selection.select}
          cases={cases}
          runs={runs}
          runtimeReadiness={runtimeReadiness}
          runtimeLoading={runtimeReadinessQuery.isLoading}
        />
      </div>
    </PlaygroundPageShell>
  );
}

function PlaygroundPageShell({
  testId,
  icon,
  title,
  subtitle,
  count,
  countLabel,
  contract,
  contractVariant,
  children,
}: {
  testId: string;
  icon: "prompt" | "agent";
  title: string;
  subtitle: string;
  count: number;
  countLabel: string;
  contract: string;
  contractVariant: "outline" | "secondary";
  children: ReactNode;
}) {
  const Icon = icon === "agent" ? TerminalSquare : BookOpenText;
  return (
    <div className="flex h-full min-h-0 flex-col bg-background" data-testid={testId}>
      <PageHeader>
        <div className="flex min-w-0 flex-1 items-center gap-3">
          <div className={`flex size-8 shrink-0 items-center justify-center rounded-md border ${
            icon === "agent" ? "bg-emerald-500/10 text-emerald-700" : "bg-sky-500/10 text-sky-700"
          }`}>
            <Icon className="size-4" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex min-w-0 items-center gap-2">
              <h1 className="truncate text-sm font-semibold">{title}</h1>
              <Badge variant={contractVariant} data-testid="playground-page-contract" className="hidden shrink-0 text-[11px] sm:inline-flex">
                {contract}
              </Badge>
            </div>
            <div className="mt-0.5 truncate text-xs text-muted-foreground" data-testid="playground-page-subtitle">
              {subtitle}
            </div>
          </div>
          <div className="hidden shrink-0 rounded-md border bg-muted/30 px-2.5 py-1 text-right sm:block" data-testid="playground-page-count">
            <div className="font-mono text-sm font-semibold leading-4">{count}</div>
            <div className="text-[10px] leading-4 text-muted-foreground">{countLabel}</div>
          </div>
        </div>
      </PageHeader>
      {children}
    </div>
  );
}

function usePlaygroundPromptSelection(surface: "prompt-playground" | "agent-playground", workspaceId: string | null) {
  const storageKey = trainingSelectedPromptStorageKey(workspaceId);
  const legacyStorageKeys = useMemo(() => {
    if (!workspaceId) return [];
    const surfaceKey = `multica:training:${surface}:selected-prompt:${workspaceId}`;
    return [surfaceKey, ...legacyTrainingSelectedPromptStorageKeys(workspaceId).filter((key) => key !== surfaceKey)];
  }, [surface, workspaceId]);
  const [selectedId, setSelectedId] = useState<string | null>(null);

  useEffect(() => {
    if (!storageKey || selectedId) return;
    try {
      const storedId = [storageKey, ...legacyStorageKeys]
        .map((key) => window.localStorage.getItem(key))
        .find(Boolean);
      if (storedId) setSelectedId(storedId);
    } catch {
      // localStorage is best-effort; route usability must not depend on it.
    }
  }, [legacyStorageKeys, selectedId, storageKey]);

  useEffect(() => {
    if (!storageKey || !selectedId) return;
    try {
      window.localStorage.setItem(storageKey, selectedId);
      for (const key of legacyStorageKeys) {
        window.localStorage.setItem(key, selectedId);
      }
    } catch {
      // Ignore storage failures in private or restricted browser contexts.
    }
  }, [legacyStorageKeys, selectedId, storageKey]);

  const select = (promptId: string | null) => {
    setSelectedId(promptId);
    if (!storageKey) return;
    try {
      for (const key of [storageKey, ...legacyStorageKeys]) {
        if (promptId) {
          window.localStorage.setItem(key, promptId);
        } else {
          window.localStorage.removeItem(key);
        }
      }
    } catch {
      // localStorage persistence is best-effort; in-memory selection is still updated.
    }
  };

  return {
    storageKey,
    select,
    resolve(items: PromptLibraryItem[]) {
      const selected = selectedId ? items.find((item) => item.id === selectedId) ?? null : null;
      return selected ?? items[0] ?? null;
    },
  };
}

function useFilteredPromptItems(items: PromptLibraryItem[], query: string) {
  return useMemo(() => {
    const q = query.trim().toLowerCase();
    return items.filter((item) => {
      if (!q) return true;
      const haystack = [item.name, item.description, item.prompt_type, item.content, ...item.tags].join(" ");
      return haystack.toLowerCase().includes(q) || matchesPinyin(haystack, q);
    });
  }, [items, query]);
}

function emptyPlaygroundDraft() {
  return {
    name: "",
    description: "",
    prompt_type: "通用",
    content: "",
    variablesText: "",
    tagsText: "",
    status: "启用" as const,
  };
}
