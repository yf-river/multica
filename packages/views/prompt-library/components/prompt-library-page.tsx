"use client";

import { useEffect, useMemo, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, BookOpenText, Loader2, Play, Plus, Save, Search, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { renderPromptTemplate } from "@multica/core/prompt-library";
import type {
  CreatePromptLibraryItemRequest,
  CreatePromptEvaluationAssetRequest,
  PromptEvaluationAsset,
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
import { matchesPinyin } from "../../editor/extensions/pinyin-match";

const promptLibraryKeys = {
  list: (workspaceId: string) => ["prompt-library", workspaceId, "list"] as const,
  assets: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-assets"] as const,
};

const PROMPT_TYPES = ["全部", "需求澄清", "系统提示词", "评测提示词", "小队 SOP", "通用"];
const WORKBENCH_TABS = ["提示词库", "提示词调试场", "Agent 调试场", "数据集", "测试套件", "实验", "优化运行", "运行历史"] as const;
type WorkbenchTab = typeof WORKBENCH_TABS[number];

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
  const queryClient = useQueryClient();
  const [query, setQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState("全部");
  const [statusFilter, setStatusFilter] = useState<"全部" | PromptLibraryStatus>("全部");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [draft, setDraft] = useState<PromptDraft>(emptyDraft);
  const [debugValuesText, setDebugValuesText] = useState("");
  const [activeTab, setActiveTab] = useState<WorkbenchTab>("提示词库");
  const [agentExpectedText, setAgentExpectedText] = useState("");

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

  const items = listQuery.data?.items ?? [];
  const assets = assetQuery.data?.items ?? [];
  const selected = selectedId ? items.find((item) => item.id === selectedId) ?? null : null;

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
      toast.success("优化运行已记录");
    },
  });

  const createAssetMut = useMutation({
    mutationFn: (data: CreatePromptEvaluationAssetRequest) => api.createPromptEvaluationAsset(data),
    onSuccess: () => {
      invalidateAssets();
      toast.success("资产已创建");
    },
  });

  const updateAssetMut = useMutation({
    mutationFn: ({ id, data }: { id: string; data: UpdatePromptEvaluationAssetRequest }) => api.updatePromptEvaluationAsset(id, data),
    onSuccess: () => {
      invalidateAssets();
      toast.success("资产已更新");
    },
  });

  const deleteAssetMut = useMutation({
    mutationFn: (id: string) => api.deletePromptEvaluationAsset(id),
    onSuccess: () => {
      invalidateAssets();
      toast.success("资产已删除");
    },
  });

  const saving = createMut.isPending || updateMut.isPending;
  const deleting = deleteMut.isPending;
  const runningDebug = runDebugMut.isPending;
  const savingAsset = createAssetMut.isPending || updateAssetMut.isPending || deleteAssetMut.isPending;
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
    const values = parseDebugValues(debugValuesText);
    createAssetMut.mutate({
      prompt_id: prompt.id,
      name: `${prompt.name} Agent 调试包 ${new Date().toLocaleString("zh-CN")}`,
      description: "Agent 调试场 v1 记录：不真实执行 agent，只保存调试包和期望输出。",
      asset_type: "实验",
      payload: {
        调试包: {
          提示词: prompt.name,
          变量: values,
          上下文: debugResult.rendered,
          期望输出: agentExpectedText,
          执行方式: "不真实执行 agent",
        },
        对比维度: ["上下文完整性", "期望输出覆盖", "中文语义一致性"],
      },
      status: "启用",
    });
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
          <h1 className="truncate text-sm font-semibold">提示词工作台</h1>
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
          <FilterButton key={tab} active={activeTab === tab} onClick={() => setActiveTab(tab)}>
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
              loading={assetQuery.isLoading}
              saving={savingAsset}
              agentExpectedText={agentExpectedText}
              onAgentExpectedTextChange={setAgentExpectedText}
              onCreateAsset={createWorkbenchAsset}
              onSaveAgentDebugPackage={saveAgentDebugPackage}
              onToggleAssetStatus={toggleAssetStatus}
              onDeleteAsset={deleteAsset}
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
  loading,
  saving,
  agentExpectedText,
  onAgentExpectedTextChange,
  onCreateAsset,
  onSaveAgentDebugPackage,
  onToggleAssetStatus,
  onDeleteAsset,
}: {
  activeTab: WorkbenchTab;
  selected: PromptLibraryItem | null;
  assets: PromptEvaluationAsset[];
  loading: boolean;
  saving: boolean;
  agentExpectedText: string;
  onAgentExpectedTextChange: (value: string) => void;
  onCreateAsset: (assetType: PromptEvaluationAssetType) => void;
  onSaveAgentDebugPackage: () => void;
  onToggleAssetStatus: (asset: PromptEvaluationAsset) => void;
  onDeleteAsset: (asset: PromptEvaluationAsset) => void;
}) {
  const tabAssetType = tabToAssetType(activeTab);
  const visibleAssets = tabAssetType
    ? assets.filter((asset) => asset.asset_type === tabAssetType)
    : activeTab === "运行历史"
      ? assets.filter((asset) => Boolean(asset.payload?.["运行结果"] ?? asset.payload?.["调试输出"] ?? asset.payload?.["调试包"]))
      : assets;
  const experiments = assets.filter((asset) => asset.asset_type === "实验");

  if (activeTab === "提示词库" || activeTab === "提示词调试场") {
    return null;
  }

  if (activeTab === "Agent 调试场") {
    return (
      <section className="grid gap-3 border-t pt-4">
        <div className="flex items-center justify-between gap-2">
          <div>
            <h3 className="text-sm font-semibold">Agent 调试场</h3>
            <p className="mt-1 text-xs text-muted-foreground">v1 不真实执行 agent，只生成调试包、保存变量、上下文和期望输出。</p>
          </div>
          <Button size="sm" onClick={onSaveAgentDebugPackage} disabled={!selected || saving}>
            {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
            保存为实验
          </Button>
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
            {activeTab === "运行历史" ? "按运行结果、调试输出和调试包汇总历史记录。" : "复用提示词评测资产，全部语义按中文记录。"}
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

      {loading ? (
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
                  更新于 {asset.updated_at} · {summarizeAssetPayload(asset)}
                </div>
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
      )}
    </section>
  );
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
  if (assetType === "数据集") {
    return {
      数据集: [casePayload],
      字段说明: ["名称", "变量", "期望包含"],
      中文语义: "用于提示词调试和实验复现的基准样本。",
    };
  }
  if (assetType === "测试套件") {
    return {
      cases: [casePayload],
      通过标准: ["变量完整", "渲染内容包含期望关键词", "输出保持中文"],
    };
  }
  if (assetType === "实验") {
    return {
      实验对象: prompt.name,
      对比维度: ["命中率", "缺失变量", "中文一致性"],
      基线输出: rendered,
      cases: [casePayload],
    };
  }
  return {
    cases: [casePayload],
    调试输出: rendered,
    运行结果: {
      状态: "已记录",
      运行时间: new Date().toISOString(),
    },
  };
}

function summarizeAssetPayload(asset: PromptEvaluationAsset): string {
  const payload = asset.payload ?? {};
  const cases = Array.isArray(payload.cases) ? payload.cases.length : Array.isArray(payload["数据集"]) ? payload["数据集"].length : 0;
  if (payload["调试包"]) return "包含 Agent 调试包";
  if (payload["运行结果"]) return "包含运行结果";
  if (asset.asset_type === "实验") return `实验维度 ${Array.isArray(payload["对比维度"]) ? payload["对比维度"].length : 0} 个`;
  return cases > 0 ? `${cases} 个用例` : "未记录用例";
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
