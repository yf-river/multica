"use client";

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpenText, TerminalSquare } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { TRAINING_WORKBENCH_VIEW_BY_TAB, buildDefaultSkillScenarioPayload, trainingWorkbenchPath } from "@multica/core/training";
import type {
  Agent,
  PromptEvaluationOptimizationCandidate,
  PromptEvaluationRuntimeReadiness,
  PromptLibraryItem,
} from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { PageHeader } from "../../layout/page-header";
import { AppLink } from "../../navigation";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { AgentPlaygroundPromptList, PromptPlaygroundPromptList } from "./playground-prompt-lists";
import { AgentPlaygroundWorkbench, PromptPlaygroundWorkbench } from "./playground-workbenches";
import { DEFAULT_AGENT_MODEL, itemToDraft, valuesToDebugText } from "./prompt-library-request-builders";
import { trainingPlaygroundSelectedPromptStorageKey } from "./prompt-selection-storage";
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

type DebugRunMode = "prompt" | "agent" | "skill";

export function buildRunEvidenceHref(runRecordsPath: string, runId: string): string {
  return `${runRecordsPath}?run=${encodeURIComponent(runId)}`;
}

export function DebugRunsContainer() {
  const [mode, setMode] = useState<DebugRunMode>("prompt");

  useEffect(() => {
    document.title = "训练与评估 · 调试运行";
  }, []);

  return (
    <div className="flex h-full min-h-0 flex-col bg-background" data-testid="debug-runs-page-shell">
      <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        <div className="min-w-0">
          <h1 className="truncate text-sm font-semibold">调试运行</h1>
          <div className="mt-0.5 truncate text-xs text-muted-foreground">本地提示词渲染和真实智能体执行共用一个调试入口。</div>
        </div>
        <div className="flex rounded-md border bg-muted/20 p-0.5" role="tablist" aria-label="调试运行模式">
          <Button
            type="button"
            size="sm"
            variant={mode === "prompt" ? "secondary" : "ghost"}
            className="h-7 px-2 text-xs"
            onClick={() => setMode("prompt")}
            role="tab"
            aria-selected={mode === "prompt"}
          >
            提示词调试
          </Button>
          <Button
            type="button"
            size="sm"
            variant={mode === "agent" ? "secondary" : "ghost"}
            className="h-7 px-2 text-xs"
            onClick={() => setMode("agent")}
            role="tab"
            aria-selected={mode === "agent"}
          >
            智能体调试
          </Button>
          <Button
            type="button"
            size="sm"
            variant={mode === "skill" ? "secondary" : "ghost"}
            className="h-7 px-2 text-xs"
            onClick={() => setMode("skill")}
            role="tab"
            aria-selected={mode === "skill"}
          >
            Skill 场景
          </Button>
        </div>
      </div>
      <div className="min-h-0 flex-1">
        {mode === "prompt" ? (
          <PromptPlaygroundContainer embedded />
        ) : mode === "agent" ? (
          <AgentPlaygroundContainer embedded />
        ) : (
          <SkillScenarioDebugContainer />
        )}
      </div>
    </div>
  );
}

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
  candidates: (workspaceId: string) => ["agent-playground", workspaceId, "optimization-candidates"] as const,
  runtimeReadiness: (workspaceId: string) => ["agent-playground", workspaceId, "runtime-readiness"] as const,
  agents: (workspaceId: string) => ["agent-playground", workspaceId, "agents"] as const,
};

function SkillScenarioDebugContainer() {
  const workspacePaths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const runRecordsPath = trainingWorkbenchPath(workspacePaths.training(), TRAINING_WORKBENCH_VIEW_BY_TAB["评测记录"]);
  const [project, setProject] = useState("user-center");
  const [taskType, setTaskType] = useState("add-api");
  const [repoPath, setRepoPath] = useState("/data/ida/user-center");
  const [branch, setBranch] = useState("current-checkout");
  const [skillPath, setSkillPath] = useState(".codebuddy/skills/add-api/SKILL.md");
  const [skillRole, setSkillRole] = useState<"sop" | "operation">("operation");
  const [taskInput, setTaskInput] = useState("新增或修改 user-center API，并按项目 harness 完成实现、测试和证据记录。");
  const [latestEvidenceHref, setLatestEvidenceHref] = useState<string | null>(null);

  const runMut = useMutation({
    mutationFn: async () => {
      const payload = buildDefaultSkillScenarioPayload({
        target: {
          kind: "repo_skill",
          repo_path: repoPath,
          branch,
          skill_path: skillPath,
          skill_role: skillRole,
        },
        scenario: {
          project,
          task_type: taskType,
          task_input: taskInput,
        },
      });
      const asset = await api.createPromptEvaluationAsset({
        prompt_id: null,
        name: `${project} ${taskType} Skill 场景调试 ${new Date().toLocaleString("zh-CN")} #${Date.now()}`,
        description: `调试 ${skillPath} 在 ${project}/${taskType} 场景里的执行表现。`,
        asset_type: "测试套件",
        payload,
        status: "启用",
      });
      return api.runPromptEvaluationAssetAgent(asset.id);
    },
    onSuccess: (result) => {
      queryClient.invalidateQueries({ queryKey: ["prompt-library"] });
      setLatestEvidenceHref(buildRunEvidenceHref(runRecordsPath, result.run.id));
      toast.success(`Skill 场景运行已入队：${result.task_id}`);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Skill 场景运行失败");
    },
  });

  return (
    <div className="min-h-0 overflow-y-auto p-4 md:p-6" data-testid="skill-scenario-debug-panel">
      <section className="mx-auto grid max-w-5xl gap-4">
        <div className="grid gap-3 rounded-md border bg-background p-4">
          <div className="flex flex-col gap-1">
            <h2 className="text-sm font-semibold">Skill 场景调试</h2>
            <p className="text-xs text-muted-foreground">按项目 checkout、skill 路径和任务场景创建测试套件，并发起真实智能体评测。</p>
          </div>
          <div className="grid gap-3 md:grid-cols-2">
            <label className="grid gap-1 text-xs">
              <span className="text-muted-foreground">项目</span>
              <Input value={project} onChange={(event) => setProject(event.target.value)} className="h-8 text-xs" />
            </label>
            <label className="grid gap-1 text-xs">
              <span className="text-muted-foreground">任务类型</span>
              <Input value={taskType} onChange={(event) => setTaskType(event.target.value)} className="h-8 text-xs" />
            </label>
            <label className="grid gap-1 text-xs">
              <span className="text-muted-foreground">本地 checkout</span>
              <Input value={repoPath} onChange={(event) => setRepoPath(event.target.value)} className="h-8 text-xs" />
            </label>
            <label className="grid gap-1 text-xs">
              <span className="text-muted-foreground">分支</span>
              <Input value={branch} onChange={(event) => setBranch(event.target.value)} className="h-8 text-xs" />
            </label>
            <label className="grid gap-1 text-xs">
              <span className="text-muted-foreground">Skill 路径</span>
              <Input value={skillPath} onChange={(event) => setSkillPath(event.target.value)} className="h-8 text-xs" />
            </label>
            <label className="grid gap-1 text-xs">
              <span className="text-muted-foreground">Skill 类型</span>
              <select
                value={skillRole}
                onChange={(event) => setSkillRole(event.target.value === "sop" ? "sop" : "operation")}
                className="h-8 rounded-md border border-input bg-background px-2 text-xs"
              >
                <option value="operation">operation</option>
                <option value="sop">sop</option>
              </select>
            </label>
          </div>
          <label className="grid gap-1 text-xs">
            <span className="text-muted-foreground">任务输入</span>
            <Textarea value={taskInput} onChange={(event) => setTaskInput(event.target.value)} className="min-h-24 text-xs" />
          </label>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              onClick={() => runMut.mutate()}
              disabled={runMut.isPending || !project.trim() || !repoPath.trim() || !skillPath.trim() || !taskInput.trim()}
              data-testid="run-skill-scenario-debug"
            >
              {runMut.isPending ? "运行中..." : "运行 Skill 场景"}
            </Button>
            {latestEvidenceHref && (
              <Button size="sm" variant="secondary" render={<AppLink href={latestEvidenceHref} />}>
                查看评测记录
              </Button>
            )}
          </div>
        </div>
      </section>
    </div>
  );
}

export function PromptPlaygroundContainer({ embedded = false }: { embedded?: boolean } = {}) {
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
      embedded={embedded}
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

export function AgentPlaygroundContainer({ embedded = false }: { embedded?: boolean } = {}) {
  const workspaceId = useWorkspaceId();
  const workspacePaths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const [query, setQuery] = useState("");
  const [selectedExecutionAgentId, setSelectedExecutionAgentId] = useState("__auto__");
  const selection = usePlaygroundPromptSelection("agent-playground", workspaceId);
  const runHistoryPath = trainingWorkbenchPath(workspacePaths.training(), TRAINING_WORKBENCH_VIEW_BY_TAB["评测记录"]);

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
  const candidateQuery = useQuery({
    queryKey: agentPlaygroundKeys.candidates(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationOptimizationCandidates({ limit: 100 }),
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
  const candidates = candidateQuery.data?.items ?? [];
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

  const createCandidateMut = useMutation({
    mutationFn: (runId: string) => api.createPromptEvaluationOptimizationCandidate(runId),
    onSuccess: (candidate: PromptEvaluationOptimizationCandidate) => {
      queryClient.invalidateQueries({ queryKey: agentPlaygroundKeys.candidates(workspaceId ?? "") });
      toast.success(`优化候选已生成：${candidate.candidate_name}`);
    },
  });

  const runOptimizationAgentMut = useMutation({
    mutationFn: (runId: string) => api.runPromptEvaluationOptimizationAgent(runId),
    onSuccess: (result) => {
      queryClient.invalidateQueries({ queryKey: agentPlaygroundKeys.assets(workspaceId ?? "") });
      queryClient.invalidateQueries({ queryKey: agentPlaygroundKeys.cases(workspaceId ?? "") });
      queryClient.invalidateQueries({ queryKey: agentPlaygroundKeys.runs(workspaceId ?? "") });
      toast.success(`真实智能体优化任务已入队：${result.task_id}`);
    },
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
      embedded={embedded}
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
            candidates={candidates}
            loading={assetQuery.isLoading || caseQuery.isLoading || runQuery.isLoading}
            onSaveAgentDebugPackage={actions.saveAgentDebugPackage}
            onRunAgentDebugPackage={actions.runAgentDebugPackage}
            onGenerateCandidate={(runId) => createCandidateMut.mutate(runId)}
            generatingCandidateRunId={createCandidateMut.isPending ? createCandidateMut.variables ?? null : null}
            onRunOptimizationAgent={(runId) => runOptimizationAgentMut.mutate(runId)}
            runningOptimizationAgentRunId={runOptimizationAgentMut.isPending ? runOptimizationAgentMut.variables ?? null : null}
            runHistoryHrefForRun={(runId) => buildRunEvidenceHref(runHistoryPath, runId)}
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
  embedded,
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
  embedded?: boolean;
  children: ReactNode;
}) {
  const Icon = icon === "agent" ? TerminalSquare : BookOpenText;
  return (
    <div className="flex h-full min-h-0 flex-col bg-background" data-testid={testId}>
      {!embedded && (
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
      )}
      {children}
    </div>
  );
}

function usePlaygroundPromptSelection(surface: "prompt-playground" | "agent-playground", workspaceId: string | null) {
  const storageKey = trainingPlaygroundSelectedPromptStorageKey(surface, workspaceId);
  const [selectedId, setSelectedId] = useState<string | null>(null);

  useEffect(() => {
    if (!storageKey || selectedId) return;
    try {
      const storedId = window.localStorage.getItem(storageKey);
      if (storedId) setSelectedId(storedId);
    } catch {
      // localStorage is best-effort; route usability must not depend on it.
    }
  }, [selectedId, storageKey]);

  useEffect(() => {
    if (!storageKey || !selectedId) return;
    try {
      window.localStorage.setItem(storageKey, selectedId);
    } catch {
      // Ignore storage failures in private or restricted browser contexts.
    }
  }, [selectedId, storageKey]);

  const select = (promptId: string | null) => {
    setSelectedId(promptId);
    if (!storageKey) return;
    try {
      if (promptId) {
        window.localStorage.setItem(storageKey, promptId);
      } else {
        window.localStorage.removeItem(storageKey);
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
