"use client";

import { useEffect, useMemo, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, BookOpenText, Loader2, Play, Plus, Save, Search, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { renderPromptTemplate } from "@multica/core/prompt-library";
import type {
  AgentRuntime,
  CreatePromptLibraryItemRequest,
  CreatePromptEvaluationAssetRequest,
  PromptEvaluationAsset,
  PromptEvaluationStructuredCase,
  PromptEvaluationRun,
  PromptEvaluationAssetType,
  PromptLibraryItem,
  PromptLibraryStatus,
  PromptLibraryVariable,
  UpdatePromptEvaluationAssetRequest,
  UpdatePromptLibraryItemRequest,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Badge } from "@multica/ui/components/ui/badge";
import { PageHeader } from "../../layout/page-header";
import { useNavigation } from "../../navigation";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";

const promptLibraryKeys = {
  list: (workspaceId: string) => ["prompt-library", workspaceId, "list"] as const,
  assets: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-assets"] as const,
  cases: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-cases"] as const,
  runs: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-runs"] as const,
};

const PROMPT_TYPES = ["全部", "需求澄清", "系统提示词", "评测提示词", "小队 SOP", "通用"];
const WORKBENCH_TABS = ["提示词库", "提示词调试场", "Agent 调试场", "数据集", "测试套件", "实验", "优化运行", "运行历史"] as const;
type WorkbenchTab = typeof WORKBENCH_TABS[number];
const DEFAULT_AGENT_MODEL = "minimax-m2.7-ioa";

const TAB_TO_VIEW: Record<WorkbenchTab, string> = {
  提示词库: "prompts",
  提示词调试场: "prompt-playground",
  "Agent 调试场": "agent-playground",
  数据集: "datasets",
  测试套件: "test-suites",
  实验: "experiments",
  优化运行: "optimization-runs",
  运行历史: "run-history",
};

const VIEW_TO_TAB = Object.fromEntries(
  Object.entries(TAB_TO_VIEW).map(([tab, view]) => [view, tab]),
) as Record<string, WorkbenchTab>;

function tabFromView(view: string | null): WorkbenchTab {
  return view ? (VIEW_TO_TAB[view] ?? "提示词库") : "提示词库";
}

const USER_CENTER_TEMPLATE: CreatePromptLibraryItemRequest = {
  name: "user-center 需求澄清提示词",
  description: "user-center 小队队长使用",
  prompt_type: "需求澄清",
  content: "请先澄清目标、边界、验收条件、风险、影响范围和可观测指标。输出必须使用中文，并列出需要团队确认的问题。",
  variables: [
    { name: "issue_title", label: "issue 标题", required: true },
    { name: "project_context", label: "项目背景" },
  ],
  tags: ["user-center", "小队", "需求澄清"],
  status: "启用",
};

type PromptDraft = {
  name: string;
  description: string;
  prompt_type: string;
  content: string;
  variablesText: string;
  tagsText: string;
  status: PromptLibraryStatus;
};

type AgentRuntimeReadiness = {
  status: "就绪" | "离线" | "缺失";
  label: string;
  detail: string;
  fix: string;
  runtime: AgentRuntime | null;
};

const emptyDraft = (): PromptDraft => ({
  name: "",
  description: "",
  prompt_type: "通用",
  content: "",
  variablesText: "",
  tagsText: "",
  status: "启用",
});

export function PromptLibraryPage() {
  const workspaceId = useWorkspaceId();
  const workspacePaths = useWorkspacePaths();
  const navigation = useNavigation();
  const queryClient = useQueryClient();
  const [query, setQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState("全部");
  const [statusFilter, setStatusFilter] = useState<"全部" | PromptLibraryStatus>("全部");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [draft, setDraft] = useState<PromptDraft>(emptyDraft);
  const [debugValuesText, setDebugValuesText] = useState("");
  const viewParam = navigation.searchParams.get("view");
  const [activeTab, setActiveTab] = useState<WorkbenchTab>(() => tabFromView(viewParam));
  const [agentExpectedText, setAgentExpectedText] = useState("");

  useEffect(() => {
    setActiveTab(tabFromView(viewParam));
  }, [viewParam]);

  const listQuery = useQuery({
    queryKey: promptLibraryKeys.list(workspaceId ?? ""),
    queryFn: () => api.listPromptLibraryItems(),
    enabled: !!workspaceId,
  });

  const assetQuery = useQuery({
    queryKey: promptLibraryKeys.assets(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationAssets(),
    enabled: !!workspaceId,
  });
  const caseQuery = useQuery({
    queryKey: promptLibraryKeys.cases(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationCases(),
    enabled: !!workspaceId,
  });
  const runQuery = useQuery({
    queryKey: promptLibraryKeys.runs(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationRuns({ limit: 100 }),
    enabled: !!workspaceId,
  });

  const runtimeQuery = useQuery({
    queryKey: ["training-evaluation", workspaceId ?? "", "runtimes"],
    queryFn: () => api.listRuntimes({ workspace_id: workspaceId ?? undefined }),
    enabled: !!workspaceId,
  });

  const items = listQuery.data?.items ?? [];
  const assets = assetQuery.data?.items ?? [];
  const cases = caseQuery.data?.items ?? [];
  const runs = runQuery.data?.items ?? [];
  const selected = selectedId ? items.find((item) => item.id === selectedId) ?? null : null;
  const agentRuntimeReadiness = useMemo(
    () => evaluateCodeBuddyReadiness(runtimeQuery.data ?? []),
    [runtimeQuery.data],
  );

  useEffect(() => {
    if (!selected && selectedId && items.length > 0) {
      setSelectedId(items[0]?.id ?? null);
    }
  }, [items, selected, selectedId]);

  useEffect(() => {
    if (!selected) return;
    setDraft(itemToDraft(selected));
    setDebugValuesText(valuesToDebugText(selected.variables));
  }, [selected]);

  const filteredItems = useMemo(() => {
    const q = query.trim().toLowerCase();
    return items.filter((item) => {
      if (typeFilter !== "全部" && item.prompt_type !== typeFilter) return false;
      if (statusFilter !== "全部" && item.status !== statusFilter) return false;
      if (!q) return true;
      const haystack = [item.name, item.description, item.prompt_type, item.content, ...item.tags].join(" ");
      return haystack.toLowerCase().includes(q) || matchesPinyin(haystack, q);
    });
  }, [items, query, statusFilter, typeFilter]);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.list(workspaceId ?? "") });
  const invalidateAssets = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.assets(workspaceId ?? "") });
  const invalidateCases = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.cases(workspaceId ?? "") });
  const invalidateRuns = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runs(workspaceId ?? "") });

  const createMut = useMutation({
    mutationFn: (data: CreatePromptLibraryItemRequest) => api.createPromptLibraryItem(data),
    onSuccess: (item) => {
      invalidate();
      setSelectedId(item.id);
      toast.success("提示词已创建");
    },
  });

  const updateMut = useMutation({
    mutationFn: ({ id, data }: { id: string; data: UpdatePromptLibraryItemRequest }) => api.updatePromptLibraryItem(id, data),
    onSuccess: (item) => {
      invalidate();
      setSelectedId(item.id);
      toast.success("提示词已保存");
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.deletePromptLibraryItem(id),
    onSuccess: () => {
      invalidate();
      setSelectedId(null);
      setDraft(emptyDraft());
      toast.success("提示词已删除");
    },
  });

  const runDebugMut = useMutation({
    mutationFn: async (data: CreatePromptEvaluationAssetRequest) => {
      const asset = await api.createPromptEvaluationAsset(data);
      return api.runPromptEvaluationAsset(asset.id);
    },
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      toast.success("优化运行已记录");
    },
  });

    const createAssetMut = useMutation({
      mutationFn: (data: CreatePromptEvaluationAssetRequest) => api.createPromptEvaluationAsset(data),
      onSuccess: () => {
        invalidateAssets();
        invalidateCases();
        toast.success("资产已创建");
      },
    });

    const runAgentMut = useMutation({
      mutationFn: async (data: CreatePromptEvaluationAssetRequest) => {
        const asset = await api.createPromptEvaluationAsset(data);
        return api.runPromptEvaluationAssetAgent(asset.id);
      },
      onSuccess: (result) => {
        invalidateAssets();
        invalidateCases();
        invalidateRuns();
        toast.success(`真实 Agent 任务已入队：${result.task_id}`);
      },
    });

  const updateAssetMut = useMutation({
    mutationFn: ({ id, data }: { id: string; data: UpdatePromptEvaluationAssetRequest }) => api.updatePromptEvaluationAsset(id, data),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      toast.success("资产已更新");
    },
  });

  const deleteAssetMut = useMutation({
    mutationFn: (id: string) => api.deletePromptEvaluationAsset(id),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      toast.success("资产已删除");
    },
  });

  const syncRunMut = useMutation({
    mutationFn: (runId: string) => api.syncPromptEvaluationRun(runId),
    onSuccess: () => {
      invalidateRuns();
      toast.success("运行记录已同步");
    },
  });

  const saving = createMut.isPending || updateMut.isPending;
  const deleting = deleteMut.isPending;
  const runningDebug = runDebugMut.isPending;
    const savingAsset = createAssetMut.isPending || updateAssetMut.isPending || deleteAssetMut.isPending;
    const runningAgent = runAgentMut.isPending;
  const debugResult = useMemo(
    () => renderPromptTemplate({
      content: draft.content,
      variables: parseVariables(draft.variablesText),
      values: parseDebugValues(debugValuesText),
    }),
    [debugValuesText, draft.content, draft.variablesText],
  );

  const startNew = () => {
    setSelectedId(null);
    setDraft(emptyDraft());
    setDebugValuesText("");
  };

  const applyUserCenterTemplate = () => {
    setSelectedId(null);
    setDraft(requestToDraft(USER_CENTER_TEMPLATE));
    setDebugValuesText(valuesToDebugText(USER_CENTER_TEMPLATE.variables ?? []));
  };

  const selectWorkbenchTab = (tab: WorkbenchTab) => {
    setActiveTab(tab);
    navigation.push(workspacePaths.trainingView(TAB_TO_VIEW[tab]));
  };

  const saveDraft = () => {
    const payload = draftToRequest(draft);
    if (!payload.name.trim()) {
      toast.error("请输入名称");
      return;
    }
    if (!payload.content.trim()) {
      toast.error("请输入提示词内容");
      return;
    }
    if (selected) {
      updateMut.mutate({ id: selected.id, data: payload });
    } else {
      createMut.mutate(payload);
    }
  };

  const toggleArchive = () => {
    if (!selected) return;
    updateMut.mutate({
      id: selected.id,
      data: { status: selected.status === "启用" ? "归档" : "启用" },
    });
  };

  const deleteSelected = () => {
    if (!selected) return;
    if (!window.confirm(`删除提示词「${selected.name}」？`)) return;
    deleteMut.mutate(selected.id);
  };

  const runDebug = () => {
    if (!selected) {
      toast.error("请先保存提示词");
      return;
    }
    const values = parseDebugValues(debugValuesText);
    runDebugMut.mutate({
      prompt_id: selected.id,
      name: `${selected.name} 优化运行 ${new Date().toLocaleString("zh-CN")}`,
      description: "从提示词调试场记录",
      asset_type: "优化运行",
      payload: {
        cases: [
          {
            名称: "调试场用例",
            变量: values,
            期望包含: debugResult.missingVariables.length === 0 ? debugResult.usedVariables.map((key) => values[key]).filter(Boolean) : [],
          },
        ],
        调试输出: debugResult.rendered,
      },
      status: "启用",
    });
  };

  const createWorkbenchAsset = (assetType: PromptEvaluationAssetType) => {
    const prompt = selected;
    if (!prompt) {
      toast.error("请先保存提示词");
      return;
    }
    const values = parseDebugValues(debugValuesText);
    const now = new Date().toLocaleString("zh-CN");
    createAssetMut.mutate({
      prompt_id: prompt.id,
      name: `${prompt.name} ${assetType} ${now}`,
      description: `从中文工作台创建的${assetType}`,
      asset_type: assetType,
      payload: buildAssetPayload(assetType, prompt, values, debugResult.rendered),
      status: "启用",
    });
  };

    const saveAgentDebugPackage = () => {
      const prompt = selected;
      if (!prompt) {
        toast.error("请先保存提示词");
        return;
      }
      createAssetMut.mutate(buildAgentDebugPackageRequest(prompt, parseDebugValues(debugValuesText), debugResult.rendered, agentExpectedText, agentRuntimeReadiness));
    };

    const runAgentDebugPackage = () => {
      const prompt = selected;
      if (!prompt) {
        toast.error("请先保存提示词");
        return;
      }
      if (agentRuntimeReadiness.status !== "就绪") {
        toast.error(agentRuntimeReadiness.fix);
        return;
      }
      const values = parseDebugValues(debugValuesText);
      runAgentMut.mutate(buildAgentDebugPackageRequest(prompt, values, debugResult.rendered, agentExpectedText, agentRuntimeReadiness));
    };

  const toggleAssetStatus = (asset: PromptEvaluationAsset) => {
    updateAssetMut.mutate({
      id: asset.id,
      data: { status: asset.status === "启用" ? "归档" : "启用" },
    });
  };

  const deleteAsset = (asset: PromptEvaluationAsset) => {
    if (!window.confirm(`删除资产「${asset.name}」？`)) return;
    deleteAssetMut.mutate(asset.id);
  };

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <PageHeader>
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <BookOpenText className="size-4 shrink-0 text-muted-foreground" />
          <h1 className="truncate text-sm font-semibold">训练与评估</h1>
          <span className="text-xs text-muted-foreground">{items.length}</span>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="secondary" onClick={applyUserCenterTemplate}>
            <BookOpenText className="size-3.5" />
            user-center 模板
          </Button>
          <Button size="sm" onClick={startNew}>
            <Plus className="size-3.5" />
            新建
          </Button>
        </div>
      </PageHeader>

      <div className="flex shrink-0 gap-1 overflow-x-auto border-b px-3 py-2">
        {WORKBENCH_TABS.map((tab) => (
          <FilterButton key={tab} active={activeTab === tab} onClick={() => selectWorkbenchTab(tab)}>
            {tab}
          </FilterButton>
        ))}
      </div>

      <div className="flex min-h-0 flex-1 flex-col md:grid md:grid-cols-[360px_minmax(0,1fr)]">
        <aside className="flex min-h-0 flex-col border-b md:border-b-0 md:border-r">
          <div className="space-y-3 border-b p-3">
            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="搜索名称、标签、内容"
                className="h-8 pl-8 text-sm"
              />
            </div>
            <div className="flex flex-wrap gap-1.5">
              {PROMPT_TYPES.map((type) => (
                <FilterButton key={type} active={typeFilter === type} onClick={() => setTypeFilter(type)}>
                  {type}
                </FilterButton>
              ))}
            </div>
            <div className="flex flex-wrap gap-1.5">
              {(["全部", "启用", "归档"] as const).map((status) => (
                <FilterButton key={status} active={statusFilter === status} onClick={() => setStatusFilter(status)}>
                  {status}
                </FilterButton>
              ))}
            </div>
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto">
            {listQuery.isLoading ? (
              <div className="space-y-2 p-3">
                {Array.from({ length: 5 }).map((_, index) => (
                  <div key={index} className="h-16 rounded-md bg-muted/60" />
                ))}
              </div>
            ) : filteredItems.length === 0 ? (
              <div className="p-6 text-sm text-muted-foreground">暂无提示词</div>
            ) : (
              <div className="divide-y">
                {filteredItems.map((item) => (
                  <button
                    key={item.id}
                    type="button"
                    onClick={() => setSelectedId(item.id)}
                    className={`flex w-full flex-col gap-2 px-3 py-3 text-left transition-colors hover:bg-muted/60 ${
                      selectedId === item.id ? "bg-muted" : ""
                    }`}
                  >
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="min-w-0 flex-1 truncate text-sm font-medium">{item.name}</span>
                      <Badge variant={item.status === "启用" ? "secondary" : "outline"} className="shrink-0">
                        {item.status}
                      </Badge>
                    </div>
                    <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
                      <span className="shrink-0">{item.prompt_type}</span>
                      <span className="truncate">{item.description || "无描述"}</span>
                    </div>
                    {item.tags.length > 0 && (
                      <div className="flex flex-wrap gap-1">
                        {item.tags.slice(0, 4).map((tag) => (
                          <span key={tag} className="rounded bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
                            {tag}
                          </span>
                        ))}
                      </div>
                    )}
                  </button>
                ))}
              </div>
            )}
          </div>
        </aside>

        <main className="min-h-0 overflow-y-auto p-4 md:p-6">
          <div className="mx-auto flex max-w-5xl flex-col gap-4">
            <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
              <div className="min-w-0">
                <h2 className="truncate text-base font-semibold">{selected ? selected.name : "新建提示词"}</h2>
                <div className="mt-1 text-xs text-muted-foreground">
                  {selected ? `版本 ${selected.version} · ${selected.updated_at}` : "未保存"}
                </div>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                {selected && (
                  <>
                    <Button size="sm" variant="secondary" onClick={toggleArchive} disabled={saving}>
                      <Archive className="size-3.5" />
                      {selected.status === "启用" ? "归档" : "启用"}
                    </Button>
                    <Button size="sm" variant="destructive" onClick={deleteSelected} disabled={deleting}>
                      <Trash2 className="size-3.5" />
                      删除
                    </Button>
                  </>
                )}
                <Button size="sm" onClick={saveDraft} disabled={saving}>
                  {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                  保存
                </Button>
              </div>
            </div>

            <div className="grid gap-4 md:grid-cols-2">
              <Field label="名称">
                <Input value={draft.name} onChange={(event) => setDraftField(setDraft, "name", event.target.value)} />
              </Field>
              <Field label="类型">
                <Input value={draft.prompt_type} onChange={(event) => setDraftField(setDraft, "prompt_type", event.target.value)} />
              </Field>
              <Field label="描述">
                <Input value={draft.description} onChange={(event) => setDraftField(setDraft, "description", event.target.value)} />
              </Field>
              <Field label="状态">
                <div className="flex h-10 items-center gap-2">
                  {(["启用", "归档"] as const).map((status) => (
                    <FilterButton key={status} active={draft.status === status} onClick={() => setDraftField(setDraft, "status", status)}>
                      {status}
                    </FilterButton>
                  ))}
                </div>
              </Field>
              <Field label="变量">
                <Input
                  value={draft.variablesText}
                  onChange={(event) => setDraftField(setDraft, "variablesText", event.target.value)}
                  placeholder="issue_title=issue 标题, project_context=项目背景"
                />
              </Field>
              <Field label="标签">
                <Input
                  value={draft.tagsText}
                  onChange={(event) => setDraftField(setDraft, "tagsText", event.target.value)}
                  placeholder="user-center, 小队, 需求澄清"
                />
              </Field>
            </div>

            <Field label="提示词内容">
              <Textarea
                value={draft.content}
                onChange={(event) => setDraftField(setDraft, "content", event.target.value)}
                className="min-h-[360px] resize-y font-mono text-sm leading-6"
              />
            </Field>

            <section className="grid gap-4 md:grid-cols-[320px_minmax(0,1fr)]">
              <Field label="调试变量">
                <Textarea
                  value={debugValuesText}
                  onChange={(event) => setDebugValuesText(event.target.value)}
                  className="min-h-[180px] resize-y font-mono text-sm leading-6"
                  placeholder="issue_title=登录失败&#10;project_context=user-center"
                />
              </Field>
              <div className="grid gap-1.5 text-sm">
                <div className="flex min-h-5 items-center gap-2">
                  <span className="text-xs font-medium text-muted-foreground">调试输出</span>
                  {debugResult.missingVariables.length > 0 && (
                    <Badge variant="outline" className="text-[11px]">
                      缺失 {debugResult.missingVariables.join("、")}
                    </Badge>
                  )}
                  <Button size="sm" variant="secondary" className="ml-auto h-7" onClick={runDebug} disabled={runningDebug}>
                    {runningDebug ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
                    运行并记录
                  </Button>
                </div>
                <pre className="min-h-[180px] overflow-auto whitespace-pre-wrap rounded-md border bg-muted/20 p-3 font-mono text-sm leading-6">
                  {debugResult.rendered || "暂无输出"}
                </pre>
              </div>
            </section>

            <WorkbenchPanel
              activeTab={activeTab}
              selected={selected}
              assets={assets}
              cases={cases}
              runs={runs}
              loading={assetQuery.isLoading || caseQuery.isLoading || runQuery.isLoading}
                saving={savingAsset}
                runningAgent={runningAgent}
                runtimeReadiness={agentRuntimeReadiness}
                runtimeLoading={runtimeQuery.isLoading}
              agentExpectedText={agentExpectedText}
              onAgentExpectedTextChange={setAgentExpectedText}
              onCreateAsset={createWorkbenchAsset}
                onSaveAgentDebugPackage={saveAgentDebugPackage}
              onRunAgentDebugPackage={runAgentDebugPackage}
              onToggleAssetStatus={toggleAssetStatus}
              onDeleteAsset={deleteAsset}
              onSyncRun={(runId) => syncRunMut.mutate(runId)}
              syncingRunId={syncRunMut.isPending ? syncRunMut.variables ?? null : null}
            />
          </div>
        </main>
      </div>
    </div>
  );
}

function WorkbenchPanel({
  activeTab,
  selected,
  assets,
  cases,
  runs,
  loading,
    saving,
    runningAgent,
    agentExpectedText,
    runtimeReadiness,
    runtimeLoading,
  onAgentExpectedTextChange,
  onCreateAsset,
    onSaveAgentDebugPackage,
  onRunAgentDebugPackage,
    onToggleAssetStatus,
  onDeleteAsset,
  onSyncRun,
  syncingRunId,
}: {
  activeTab: WorkbenchTab;
  selected: PromptLibraryItem | null;
  assets: PromptEvaluationAsset[];
  cases: PromptEvaluationStructuredCase[];
  runs: PromptEvaluationRun[];
    loading: boolean;
    saving: boolean;
    runningAgent: boolean;
    runtimeReadiness: AgentRuntimeReadiness;
    runtimeLoading: boolean;
    agentExpectedText: string;
    onAgentExpectedTextChange: (value: string) => void;
    onCreateAsset: (assetType: PromptEvaluationAssetType) => void;
    onSaveAgentDebugPackage: () => void;
    onRunAgentDebugPackage: () => void;
    onToggleAssetStatus: (asset: PromptEvaluationAsset) => void;
  onDeleteAsset: (asset: PromptEvaluationAsset) => void;
  onSyncRun: (runId: string) => void;
  syncingRunId: string | null;
}) {
  const tabAssetType = tabToAssetType(activeTab);
  const visibleAssets = tabAssetType ? assets.filter((asset) => asset.asset_type === tabAssetType) : assets;
  const experiments = assets.filter((asset) => asset.asset_type === "实验");
  const caseCounts = useMemo(() => buildCaseCounts(cases), [cases]);

  if (activeTab === "提示词库" || activeTab === "提示词调试场") {
    return null;
  }

	  if (activeTab === "Agent 调试场") {
	    return (
	      <section className="grid gap-3 border-t pt-4">
	        <div className="flex items-center justify-between gap-2">
	          <div>
	            <h3 className="text-sm font-semibold">Agent 调试场</h3>
	            <p className="mt-1 text-xs text-muted-foreground">当前仅保存调试包；未创建真实任务时会明确写入环境状态和修复路径。</p>
	          </div>
	          <div className="flex shrink-0 items-center gap-2">
	            <Button size="sm" variant="secondary" onClick={onSaveAgentDebugPackage} disabled={!selected || saving}>
	              {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
	              保存为实验
	            </Button>
	            <Button size="sm" onClick={onRunAgentDebugPackage} disabled={!selected || saving || runningAgent || runtimeReadiness.status !== "就绪"}>
	              {runningAgent ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
	              创建真实 Agent 任务
	            </Button>
	          </div>
	        </div>
	        <div className="grid gap-2 rounded-md border bg-muted/20 p-3 text-xs">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-foreground">真实执行准备度</span>
            <Badge variant={runtimeReadiness.status === "就绪" ? "secondary" : "outline"}>
              {runtimeLoading ? "检查中" : runtimeReadiness.label}
            </Badge>
            <span className="text-muted-foreground">目标模型 {DEFAULT_AGENT_MODEL}</span>
          </div>
          <div className="text-muted-foreground">{runtimeLoading ? "正在检查当前 workspace 的运行时列表。" : runtimeReadiness.detail}</div>
          {runtimeReadiness.status !== "就绪" && !runtimeLoading && (
            <div className="text-muted-foreground">修复路径：{runtimeReadiness.fix}</div>
          )}
        </div>
        <Field label="期望输出">
          <Textarea
            value={agentExpectedText}
            onChange={(event) => onAgentExpectedTextChange(event.target.value)}
            className="min-h-[140px] resize-y text-sm leading-6"
            placeholder="写下希望 Agent 最终交付的结构、证据和中文口径。"
          />
        </Field>
      </section>
    );
  }

  return (
    <section className="grid gap-3 border-t pt-4">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h3 className="text-sm font-semibold">{activeTab}</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            {activeTab === "运行历史" ? "按结构化运行记录展示 run、task、模型、耗时和评估结论。" : "复用提示词评测资产，全部语义按中文记录。"}
          </p>
        </div>
        {tabAssetType && (
          <Button size="sm" onClick={() => onCreateAsset(tabAssetType)} disabled={!selected || saving}>
            {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
            新建{tabAssetType}
          </Button>
        )}
      </div>

      {activeTab === "实验" && (
        <div className="rounded-md border border-border/70 bg-muted/20 px-3 py-2 text-xs text-muted-foreground">
          实验对比摘要：{experiments.length} 个实验，启用 {experiments.filter((asset) => asset.status === "启用").length} 个，归档 {experiments.filter((asset) => asset.status === "归档").length} 个。
        </div>
      )}

      {activeTab === "运行历史" && (
        loading ? (
          <div className="h-20 rounded-md bg-muted/60" />
        ) : runs.length === 0 ? (
          <div className="rounded-md border border-dashed px-3 py-6 text-center text-sm text-muted-foreground">暂无结构化运行记录</div>
        ) : (
          <div className="divide-y rounded-md border">
            {runs.map((run) => (
              <div key={run.id} className="grid gap-2 px-3 py-3 md:grid-cols-[minmax(0,1fr)_auto]">
                <div className="min-w-0">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-sm font-medium">{run.run_kind} · {run.status}</span>
                    <Badge variant={run.status === "通过" ? "secondary" : run.status === "已入队" || run.status === "运行中" ? "outline" : "destructive"} className="shrink-0">
                      {run.total_cases} 用例 · 通过率 {Math.round(run.pass_rate * 100)}%
                    </Badge>
                  </div>
                  <div className="mt-1 text-xs text-muted-foreground">{summarizeStructuredRun(run)}</div>
                  <div className="mt-1 break-all text-[11px] text-muted-foreground">
                    run {run.id}{run.task_id ? ` · task ${run.task_id}` : ""}
                  </div>
                </div>
                <div className="flex items-center justify-end gap-2 text-right text-[11px] text-muted-foreground">
                  <div>
                    <div>{run.created_at}</div>
                    <div>{run.total_duration_ms} ms</div>
                  </div>
                  {run.task_id && (
                    <Button size="sm" variant="secondary" onClick={() => onSyncRun(run.id)} disabled={syncingRunId === run.id}>
                      {syncingRunId === run.id ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
                      同步任务
                    </Button>
                  )}
                </div>
              </div>
            ))}
          </div>
        )
      )}

      {activeTab !== "运行历史" && (
        loading ? (
          <div className="h-20 rounded-md bg-muted/60" />
        ) : visibleAssets.length === 0 ? (
          <div className="rounded-md border border-dashed px-3 py-6 text-center text-sm text-muted-foreground">暂无资产</div>
        ) : (
          <div className="divide-y rounded-md border">
            {visibleAssets.map((asset) => (
              <div key={asset.id} className="grid gap-2 px-3 py-3 md:grid-cols-[minmax(0,1fr)_auto]">
                <div className="min-w-0">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-sm font-medium">{asset.name}</span>
                    <Badge variant={asset.status === "启用" ? "secondary" : "outline"} className="shrink-0">
                      {asset.asset_type} · {asset.status}
                    </Badge>
                  </div>
                  <div className="mt-1 truncate text-xs text-muted-foreground">{asset.description || "无描述"}</div>
                  <div className="mt-1 text-[11px] text-muted-foreground">
                    更新于 {asset.updated_at} · {summarizeAssetPayload(asset, caseCounts.get(asset.id) ?? 0)}
                  </div>
                  {summarizeAgentRun(asset) && (
                    <div className="mt-1 text-[11px] text-muted-foreground">
                      {summarizeAgentRun(asset)}
                    </div>
                  )}
                </div>
                <div className="flex items-center gap-2">
                  <Button size="sm" variant="secondary" onClick={() => onToggleAssetStatus(asset)} disabled={saving}>
                    {asset.status === "启用" ? "归档" : "启用"}
                  </Button>
                  <Button size="sm" variant="destructive" onClick={() => onDeleteAsset(asset)} disabled={saving}>
                    <Trash2 className="size-3.5" />
                    删除
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )
      )}
    </section>
  );
}

function evaluateCodeBuddyReadiness(runtimes: AgentRuntime[]): AgentRuntimeReadiness {
  const codeBuddyRuntimes = runtimes.filter((runtime) => runtime.provider.toLowerCase() === "codebuddy");
  const onlineRuntime = codeBuddyRuntimes.find((runtime) => runtime.status === "online") ?? null;
  if (onlineRuntime) {
    return {
      status: "就绪",
      label: "CodeBuddy 在线",
      detail: `已发现在线 CodeBuddy runtime「${onlineRuntime.name}」，可以作为 ${DEFAULT_AGENT_MODEL} 的真实执行目标。`,
      fix: "无需修复；下一步应创建真实 Agent 任务并采集 trace、token、成本和输出。",
      runtime: onlineRuntime,
    };
  }
  const offlineRuntime = codeBuddyRuntimes[0] ?? null;
  if (offlineRuntime) {
    return {
      status: "离线",
      label: "CodeBuddy 离线",
      detail: `已注册 CodeBuddy runtime「${offlineRuntime.name}」，但当前状态是离线，不能创建真实 Agent 任务。`,
      fix: "启动 multica daemon，并确认 codebuddy 可执行文件在 PATH 中，或设置 MULTICA_CODEBUDDY_PATH 后重启 daemon。",
      runtime: offlineRuntime,
    };
  }
  return {
    status: "缺失",
    label: "CodeBuddy 缺失",
    detail: "当前 workspace 未发现 CodeBuddy runtime，Agent 调试场不能执行 minimax-m2.7-ioa。",
    fix: "安装并配置 codebuddy，启动 multica daemon，等待 /api/runtimes 出现 provider=codebuddy 且 status=online 的 runtime。",
    runtime: null,
  };
}

function buildAgentExecutionStatus(readiness: AgentRuntimeReadiness): string {
  if (readiness.status === "就绪") {
    return `CodeBuddy runtime 已在线，目标模型 ${DEFAULT_AGENT_MODEL}；当前记录仅保存调试包，尚未创建真实 Agent 任务`;
  }
  return `${readiness.label}，目标模型 ${DEFAULT_AGENT_MODEL}；未创建真实 Agent 任务`;
}

function FilterButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`inline-flex h-7 items-center rounded-md border px-2.5 text-xs transition-colors ${
        active ? "border-foreground bg-foreground text-background" : "border-border bg-background text-muted-foreground hover:text-foreground"
      }`}
    >
      {children}
    </button>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="grid gap-1.5 text-sm">
      <span className="text-xs font-medium text-muted-foreground">{label}</span>
      {children}
    </label>
  );
}

function setDraftField<K extends keyof PromptDraft>(
  setDraft: Dispatch<SetStateAction<PromptDraft>>,
  key: K,
  value: PromptDraft[K],
) {
  setDraft((current) => ({ ...current, [key]: value }));
}

function itemToDraft(item: PromptLibraryItem): PromptDraft {
  return {
    name: item.name,
    description: item.description,
    prompt_type: item.prompt_type,
    content: item.content,
    variablesText: variablesToText(item.variables),
    tagsText: item.tags.join(", "),
    status: item.status,
  };
}

function requestToDraft(req: CreatePromptLibraryItemRequest): PromptDraft {
  return {
    name: req.name,
    description: req.description ?? "",
    prompt_type: req.prompt_type ?? "通用",
    content: req.content,
    variablesText: variablesToText(req.variables ?? []),
    tagsText: (req.tags ?? []).join(", "),
    status: req.status ?? "启用",
  };
}

function draftToRequest(draft: PromptDraft): CreatePromptLibraryItemRequest {
  return {
    name: draft.name.trim(),
    description: draft.description.trim(),
    prompt_type: draft.prompt_type.trim() || "通用",
    content: draft.content,
    variables: parseVariables(draft.variablesText),
    tags: splitList(draft.tagsText),
    status: draft.status,
  };
}

function tabToAssetType(tab: WorkbenchTab): PromptEvaluationAssetType | null {
  if (tab === "数据集" || tab === "测试套件" || tab === "实验" || tab === "优化运行") return tab;
  return null;
}

function buildAssetPayload(
  assetType: PromptEvaluationAssetType,
  prompt: PromptLibraryItem,
  values: Record<string, string>,
  rendered: string,
): Record<string, unknown> {
  const casePayload = {
    名称: `${prompt.name} 基准用例`,
    变量: values,
    期望包含: Object.values(values).filter(Boolean),
  };
  const basePayload = {
    schema_version: 1,
    语义版本: "multica.training_evaluation.v1",
    cases: [casePayload],
    指标口径: [
      "总用例数",
      "通过数",
      "失败数",
      "通过率",
      "总耗时",
      "平均耗时",
      "输入token",
      "输出token",
      "预估成本",
      "执行Agent",
      "模型",
      "runtime",
      "trace/task id",
      "失败原因",
      "评估结论",
    ],
  };
  if (assetType === "数据集") {
    return {
      ...basePayload,
      数据集: [casePayload],
      字段说明: ["名称", "变量", "期望包含"],
      中文语义: "用于提示词调试和实验复现的基准样本。",
    };
  }
  if (assetType === "测试套件") {
    return {
      ...basePayload,
      通过标准: ["变量完整", "渲染内容包含期望关键词", "输出保持中文"],
    };
  }
  if (assetType === "实验") {
    return {
      ...basePayload,
      实验对象: prompt.name,
      对比维度: ["命中率", "缺失变量", "中文一致性"],
      基线输出: rendered,
    };
  }
	  return {
	    ...basePayload,
	    调试输出: rendered,
	    运行结果: {
	      状态: "已记录",
	      运行时间: new Date().toISOString(),
	    },
	  };
}

function buildAgentDebugPackageRequest(
  prompt: PromptLibraryItem,
  values: Record<string, string>,
  rendered: string,
  expectedOutput: string,
  readiness: AgentRuntimeReadiness,
): CreatePromptEvaluationAssetRequest {
  return {
    prompt_id: prompt.id,
    name: `${prompt.name} Agent 调试包 ${new Date().toLocaleString("zh-CN")}`,
    description: `Agent 调试场记录：${buildAgentExecutionStatus(readiness)}`,
    asset_type: "实验",
    payload: {
      schema_version: 1,
      语义版本: "multica.training_evaluation.v1",
      调试包: {
        提示词: prompt.name,
        变量: values,
        上下文: rendered,
        期望输出: expectedOutput,
        执行方式: buildAgentExecutionStatus(readiness),
      },
      运行环境: {
        目标Runtime: "CodeBuddy",
        目标模型: DEFAULT_AGENT_MODEL,
        状态: readiness.status,
        说明: readiness.detail,
        修复路径: readiness.fix,
        runtime_id: readiness.runtime?.id ?? null,
      },
      对比维度: ["上下文完整性", "期望输出覆盖", "中文语义一致性"],
    },
    status: "启用",
  };
}

function buildCaseCounts(cases: PromptEvaluationStructuredCase[]): Map<string, number> {
  const counts = new Map<string, number>();
  for (const item of cases) {
    counts.set(item.asset_id, (counts.get(item.asset_id) ?? 0) + 1);
  }
  return counts;
}

function summarizeAssetPayload(asset: PromptEvaluationAsset, structuredCaseCount = 0): string {
  const payload = asset.payload ?? {};
  const cases = Array.isArray(payload.cases) ? payload.cases.length : Array.isArray(payload["数据集"]) ? payload["数据集"].length : 0;
  if (structuredCaseCount > 0) return `结构化用例 ${structuredCaseCount} 个`;
  if (payload["最近Agent运行"]) return "包含真实 Agent 运行";
  if (payload["调试包"]) return "包含 Agent 调试包";
  if (payload["运行结果"]) return "包含运行结果";
  if (asset.asset_type === "实验") return `实验维度 ${Array.isArray(payload["对比维度"]) ? payload["对比维度"].length : 0} 个`;
  return cases > 0 ? `${cases} 个用例` : "未记录用例";
}

function summarizeAgentRun(asset: PromptEvaluationAsset): string | null {
  const payload = asset.payload ?? {};
  const run = payload["最近Agent运行"];
  if (!run || typeof run !== "object" || Array.isArray(run)) return null;
  const record = run as Record<string, unknown>;
  const status = stringFromRecord(record, "状态") || "未知状态";
  const taskId = stringFromRecord(record, "trace/task id");
  const agent = stringFromRecord(record, "执行Agent");
  const model = stringFromRecord(record, "模型");
  return `Agent 任务：${status}${taskId ? ` · task ${taskId}` : ""}${agent ? ` · ${agent}` : ""}${model ? ` · ${model}` : ""}`;
}

function summarizeStructuredRun(run: PromptEvaluationRun): string {
  const pieces = [
    `模型 ${run.model || "未记录"}`,
    `runtime ${run.runtime_provider || "未记录"}`,
    `通过 ${run.passed_cases}/${run.total_cases}`,
    `输入 ${run.input_tokens} token`,
    `输出 ${run.output_tokens} token`,
  ];
  if (run.failure_reason) pieces.push(`失败原因：${run.failure_reason}`);
  if (run.conclusion) pieces.push(`结论：${run.conclusion}`);
  return pieces.join(" · ");
}

function stringFromRecord(record: Record<string, unknown>, key: string): string {
  const value = record[key];
  return typeof value === "string" ? value : "";
}

function variablesToText(variables: PromptLibraryVariable[]): string {
  return variables.map((variable) => `${variable.name}${variable.label ? `=${variable.label}` : ""}`).join(", ");
}

function valuesToDebugText(variables: PromptLibraryVariable[]): string {
  return variables.map((variable) => `${variable.name}=${variable.default_value ?? ""}`).join("\n");
}

function parseVariables(value: string): PromptLibraryVariable[] {
  return splitList(value).map((part) => {
    const [name, ...labelParts] = part.split("=");
    const label = labelParts.join("=").trim();
    const variableName = (name ?? "").trim();
    return {
      name: variableName,
      ...(label ? { label } : {}),
    };
  }).filter((variable) => variable.name.length > 0);
}

function splitList(value: string): string[] {
  return value.split(/[,，]/).map((part) => part.trim()).filter(Boolean);
}

function parseDebugValues(value: string): Record<string, string> {
  const result: Record<string, string> = {};
  for (const line of value.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const [name, ...valueParts] = trimmed.split("=");
    const key = (name ?? "").trim();
    if (!key) continue;
    result[key] = valueParts.join("=").trim();
  }
  return result;
}
