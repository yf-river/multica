"use client";

import { useEffect, useMemo, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, BookOpenText, Download, Loader2, Play, Plus, Save, Search, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { renderPromptTemplate } from "@multica/core/prompt-library";
import {
  TRAINING_WORKBENCH_TABS,
  TRAINING_WORKBENCH_VIEW_BY_TAB,
  trainingWorkbenchTabFromView,
  trainingWorkbenchTitleFromView,
  type TrainingWorkbenchTab,
} from "@multica/core/training";
import type {
  CreatePromptLibraryItemRequest,
  CreatePromptEvaluationAssetRequest,
  CreatePromptEvaluationCaseRequest,
  UpdatePromptEvaluationCaseRequest,
  PromptEvaluationAsset,
  PromptEvaluationOptimizationCandidate,
  PromptEvaluationStructuredCase,
  PromptEvaluationRun,
  PromptEvaluationRunEvidence,
  PromptEvaluationRuntimeReadiness,
  PromptEvaluationSummary,
  PromptEvaluationAssetType,
  ObservabilitySummary,
  PromptLibraryItem,
  PromptLibraryStatus,
  PromptLibraryVariable,
  UpdatePromptEvaluationAssetRequest,
  UpdatePromptEvaluationOptimizationCandidateRequest,
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
  runEvidence: (workspaceId: string, runId: string | null) => ["prompt-library", workspaceId, "run-evidence", runId ?? ""] as const,
  candidates: (workspaceId: string) => ["prompt-library", workspaceId, "optimization-candidates"] as const,
  summary: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-summary"] as const,
};

const PROMPT_TYPES = ["全部", "需求澄清", "系统提示词", "评测提示词", "小队 SOP", "通用"];
type WorkbenchTab = TrainingWorkbenchTab;
type DemoTimeRange = "24h" | "7d" | "30d" | "all";

const DEMO_TIME_RANGES: Array<{ value: DemoTimeRange; label: string; sinceMs: number | null }> = [
  { value: "24h", label: "最近24小时", sinceMs: 24 * 60 * 60 * 1000 },
  { value: "7d", label: "最近7天", sinceMs: 7 * 24 * 60 * 60 * 1000 },
  { value: "30d", label: "最近30天", sinceMs: 30 * 24 * 60 * 60 * 1000 },
  { value: "all", label: "全部", sinceMs: null },
];
const DEFAULT_DEMO_TIME_RANGE = DEMO_TIME_RANGES[1]!;

const DEFAULT_AGENT_MODEL = "minimax-m2.7-ioa";
const DEFAULT_AGENT_RUNTIME_READINESS: PromptEvaluationRuntimeReadiness = {
  status: "缺失",
  label: "CodeBuddy 检查中",
  detail: "正在检查当前工作区的 CodeBuddy 运行时就绪状态。",
  fix: "等待检查完成；如果持续缺失，请安装并配置 CodeBuddy，启动 Multica 守护进程。",
  model: DEFAULT_AGENT_MODEL,
  runtime: null,
  last_seen_age_seconds: -1,
  checked_at: "",
};

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
  const [activeTab, setActiveTab] = useState<WorkbenchTab>(() => trainingWorkbenchTabFromView(viewParam));
  const [agentExpectedText, setAgentExpectedText] = useState("");
  const [demoTimeRange, setDemoTimeRange] = useState<DemoTimeRange>("7d");
  const [exportingDemoEvidence, setExportingDemoEvidence] = useState(false);
  const demoSince = useMemo(() => {
    const option = DEMO_TIME_RANGES.find((item) => item.value === demoTimeRange);
    if (!option?.sinceMs) return null;
    return new Date(Date.now() - option.sinceMs).toISOString();
  }, [demoTimeRange]);

  useEffect(() => {
    setActiveTab(trainingWorkbenchTabFromView(viewParam));
  }, [viewParam]);

  useEffect(() => {
    document.title = trainingWorkbenchTitleFromView(viewParam);
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
    queryKey: [...promptLibraryKeys.runs(workspaceId ?? ""), demoSince ?? "all"] as const,
    queryFn: () => api.listPromptEvaluationRuns({ limit: 100, since: demoSince }),
    enabled: !!workspaceId,
  });
  const candidateQuery = useQuery({
    queryKey: promptLibraryKeys.candidates(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationOptimizationCandidates({ limit: 100 }),
    enabled: !!workspaceId,
  });
  const summaryQuery = useQuery({
    queryKey: [...promptLibraryKeys.summary(workspaceId ?? ""), demoSince ?? "all"] as const,
    queryFn: () => api.getPromptEvaluationSummary(demoSince ? { since: demoSince } : undefined),
    enabled: !!workspaceId,
  });

  const runtimeReadinessQuery = useQuery({
    queryKey: ["training-evaluation", workspaceId ?? "", "runtime-readiness"],
    queryFn: () => api.getPromptEvaluationRuntimeReadiness(),
    enabled: !!workspaceId,
  });
  const observabilitySummaryQuery = useQuery({
    queryKey: ["training-evaluation", workspaceId ?? "", "workspace-observability-summary", demoSince ?? "all"],
    queryFn: () => api.getWorkspaceObservabilitySummary(workspaceId ?? "", demoSince ? { since: demoSince } : undefined),
    enabled: !!workspaceId,
    staleTime: 30_000,
  });

  const items = listQuery.data?.items ?? [];
  const assets = assetQuery.data?.items ?? [];
  const cases = caseQuery.data?.items ?? [];
  const runs = runQuery.data?.items ?? [];
  const candidates = candidateQuery.data?.items ?? [];
  const summary = summaryQuery.data ?? null;
  const selected = selectedId ? items.find((item) => item.id === selectedId) ?? null : null;
  const agentRuntimeReadiness = runtimeReadinessQuery.data ?? DEFAULT_AGENT_RUNTIME_READINESS;

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
  const invalidateCandidates = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.candidates(workspaceId ?? "") });
  const invalidateSummary = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.summary(workspaceId ?? "") });

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
      invalidateSummary();
      toast.success("优化运行已记录");
    },
  });

    const createAssetMut = useMutation({
      mutationFn: (data: CreatePromptEvaluationAssetRequest) => api.createPromptEvaluationAsset(data),
      onSuccess: () => {
        invalidateAssets();
        invalidateCases();
        invalidateSummary();
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
        invalidateSummary();
        toast.success(`真实 Agent 任务已入队：${result.task_id}`);
      },
    });

  const updateAssetMut = useMutation({
    mutationFn: ({ id, data }: { id: string; data: UpdatePromptEvaluationAssetRequest }) => api.updatePromptEvaluationAsset(id, data),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      invalidateSummary();
      toast.success("资产已更新");
    },
  });

  const deleteAssetMut = useMutation({
    mutationFn: (id: string) => api.deletePromptEvaluationAsset(id),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      invalidateSummary();
      toast.success("资产已删除");
    },
  });

  const createCaseMut = useMutation({
    mutationFn: (data: CreatePromptEvaluationCaseRequest) => api.createPromptEvaluationCase(data),
    onSuccess: () => {
      invalidateCases();
      invalidateSummary();
      toast.success("手工评测用例已创建");
    },
  });

  const updateCaseMut = useMutation({
    mutationFn: ({ caseId, data }: { caseId: string; data: UpdatePromptEvaluationCaseRequest }) => api.updatePromptEvaluationCase(caseId, data),
    onSuccess: () => {
      invalidateCases();
      invalidateSummary();
      toast.success("手工评测用例已保存");
    },
  });

  const deleteCaseMut = useMutation({
    mutationFn: (id: string) => api.deletePromptEvaluationCase(id),
    onSuccess: () => {
      invalidateCases();
      invalidateSummary();
      toast.success("手工评测用例已删除");
    },
  });

  const syncRunMut = useMutation({
    mutationFn: (runId: string) => api.syncPromptEvaluationRun(runId),
    onSuccess: (_run, runId) => {
      invalidateRuns();
      invalidateCandidates();
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidence(workspaceId ?? "", runId) });
      invalidateSummary();
      toast.success("运行记录已同步");
    },
  });

  const createCandidateMut = useMutation({
    mutationFn: (runId: string) => api.createPromptEvaluationOptimizationCandidate(runId),
    onSuccess: () => {
      invalidateCandidates();
      invalidateSummary();
      toast.success("优化候选已生成，等待人工确认");
    },
  });

  const runOptimizationAgentMut = useMutation({
    mutationFn: (runId: string) => api.runPromptEvaluationOptimizationAgent(runId),
    onSuccess: (result) => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      invalidateSummary();
      toast.success(`真实 Agent 优化任务已入队：${result.task_id}`);
      setActiveTab("运行历史");
      navigation.push(workspacePaths.trainingView(TRAINING_WORKBENCH_VIEW_BY_TAB["运行历史"]));
    },
  });

  const publishCandidateMut = useMutation({
    mutationFn: (candidateId: string) => api.publishPromptEvaluationOptimizationCandidate(candidateId),
    onSuccess: (result) => {
      invalidate();
      invalidateCandidates();
      invalidateSummary();
      setSelectedId(result.prompt.id);
      toast.success(`已发布新提示词版本：${result.prompt.name}`);
    },
  });

  const updateCandidateMut = useMutation({
    mutationFn: ({ candidateId, data }: { candidateId: string; data: UpdatePromptEvaluationOptimizationCandidateRequest }) =>
      api.updatePromptEvaluationOptimizationCandidate(candidateId, data),
    onSuccess: (candidate) => {
      invalidateCandidates();
      invalidateSummary();
      toast.success(`优化候选已保存：${candidate.candidate_name}`);
    },
  });

  const rejectCandidateMut = useMutation({
    mutationFn: ({ candidateId, reason }: { candidateId: string; reason: string }) =>
      api.rejectPromptEvaluationOptimizationCandidate(candidateId, reason),
    onSuccess: (candidate) => {
      invalidateCandidates();
      invalidateSummary();
      toast.success(`已暂不采纳优化候选：${candidate.candidate_name}`);
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
    navigation.push(workspacePaths.trainingView(TRAINING_WORKBENCH_VIEW_BY_TAB[tab]));
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

  const exportDemoEvidence = async () => {
    const range = DEMO_TIME_RANGES.find((item) => item.value === demoTimeRange);
    setExportingDemoEvidence(true);
    try {
      const scopedRunResponse = await api.listPromptEvaluationRuns({ limit: 50, since: demoSince });
      const recentRuns = scopedRunResponse.items;
      const recentRunEvidence = await Promise.all(
        recentRuns.map(async (run) => {
          try {
            const evidence = await api.getPromptEvaluationRunEvidence(run.id);
            return {
              采集状态: "已采集",
              ...evidence,
            };
          } catch (error) {
            return {
              采集状态: "采集失败",
              run,
              trials: [],
              task_usage: [],
              task_messages: [],
              trace_events: [],
              evidence: {},
              错误: error instanceof Error ? error.message : "未知错误",
            };
          }
        }),
      );
      const evidenceStats = recentRunEvidence.reduce(
        (acc, evidence) => {
          acc.运行数 += 1;
          if (evidence.采集状态 === "已采集") acc.已采集 += 1;
          if (evidence.采集状态 !== "已采集") acc.采集失败 += 1;
          acc.trial条数 += evidence.trials.length;
          acc.task_usage条数 += evidence.task_usage.length;
          acc.task_message条数 += evidence.task_messages.length;
          acc.trace_event条数 += evidence.trace_events.length;
          return acc;
        },
        {
          运行数: 0,
          已采集: 0,
          采集失败: 0,
          trial条数: 0,
          task_usage条数: 0,
          task_message条数: 0,
          trace_event条数: 0,
        },
      );
      const payload = {
        语义版本: "multica.production_demo_evidence.v1",
        导出时间: new Date().toISOString(),
        观测范围: {
          标签: range?.label ?? demoTimeRange,
          since: demoSince,
          说明: "训练评估摘要、最近运行、运行证据与 SOP/任务观测摘要使用同一 since 时间窗口；资产统计保留当前工作区库存。",
        },
        workspace_id: workspaceId,
        训练评估摘要: summary,
        观测摘要: observabilitySummaryQuery.data ?? null,
        真实执行准备度: agentRuntimeReadiness,
        最近运行: recentRuns,
        最近运行证据: recentRunEvidence,
        证据统计: evidenceStats,
        资产统计: {
          提示词数: items.length,
          评测资产数: assets.length,
          结构化用例数: cases.length,
          优化候选数: candidates.length,
        },
      };
      const blob = new Blob([JSON.stringify(payload, null, 2)], { type: "application/json;charset=utf-8" });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = `multica-production-evidence-${new Date().toISOString().replace(/[:.]/g, "-")}.json`;
      document.body.append(anchor);
      anchor.click();
      anchor.remove();
      URL.revokeObjectURL(url);
      toast.success("演示证据 JSON 已导出");
    } finally {
      setExportingDemoEvidence(false);
    }
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
        {TRAINING_WORKBENCH_TABS.map((tab) => (
          <FilterButton key={tab} active={activeTab === tab} onClick={() => selectWorkbenchTab(tab)}>
            {tab}
          </FilterButton>
        ))}
      </div>

      <TrainingSummaryStrip summary={summary} loading={summaryQuery.isLoading} />

      {activeTab === "生产看板" ? (
        <main className="min-h-0 flex-1 overflow-y-auto p-4 md:p-6">
          <DemoDashboardPanel
            trainingSummary={summary}
            trainingLoading={summaryQuery.isLoading}
            observabilitySummary={observabilitySummaryQuery.data ?? null}
            observabilityLoading={observabilitySummaryQuery.isLoading}
            timeRange={demoTimeRange}
            onTimeRangeChange={setDemoTimeRange}
            onExportEvidence={exportDemoEvidence}
            exportingEvidence={exportingDemoEvidence}
            runtimeReadiness={agentRuntimeReadiness}
            runtimeLoading={runtimeReadinessQuery.isLoading}
            runs={runs}
            assets={assets}
            cases={cases}
            candidates={candidates}
          />
        </main>
      ) : (
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
              workspaceId={workspaceId ?? ""}
              selected={selected}
              assets={assets}
              cases={cases}
              runs={runs}
              candidates={candidates}
              loading={assetQuery.isLoading || caseQuery.isLoading || runQuery.isLoading || candidateQuery.isLoading}
                saving={savingAsset}
                runningAgent={runningAgent}
                runtimeReadiness={agentRuntimeReadiness}
                runtimeLoading={runtimeReadinessQuery.isLoading}
              agentExpectedText={agentExpectedText}
              onAgentExpectedTextChange={setAgentExpectedText}
              onCreateAsset={createWorkbenchAsset}
                onSaveAgentDebugPackage={saveAgentDebugPackage}
              onRunAgentDebugPackage={runAgentDebugPackage}
              onToggleAssetStatus={toggleAssetStatus}
              onDeleteAsset={deleteAsset}
              onCreateCase={(data) => createCaseMut.mutate(data)}
              creatingCaseAssetId={createCaseMut.isPending ? createCaseMut.variables?.asset_id ?? null : null}
              onUpdateCase={(caseId, data) => updateCaseMut.mutate({ caseId, data })}
              updatingCaseId={updateCaseMut.isPending ? updateCaseMut.variables?.caseId ?? null : null}
              onDeleteCase={(caseId) => deleteCaseMut.mutate(caseId)}
              deletingCaseId={deleteCaseMut.isPending ? deleteCaseMut.variables ?? null : null}
              onSyncRun={(runId) => syncRunMut.mutate(runId)}
              syncingRunId={syncRunMut.isPending ? syncRunMut.variables ?? null : null}
              onGenerateCandidate={(runId) => createCandidateMut.mutate(runId)}
              generatingCandidateRunId={createCandidateMut.isPending ? createCandidateMut.variables ?? null : null}
              onRunOptimizationAgent={(runId) => runOptimizationAgentMut.mutate(runId)}
              runningOptimizationAgentRunId={runOptimizationAgentMut.isPending ? runOptimizationAgentMut.variables ?? null : null}
              onUpdateCandidate={(candidateId, data) => updateCandidateMut.mutate({ candidateId, data })}
              updatingCandidateId={updateCandidateMut.isPending ? updateCandidateMut.variables?.candidateId ?? null : null}
              onPublishCandidate={(candidateId) => publishCandidateMut.mutate(candidateId)}
              publishingCandidateId={publishCandidateMut.isPending ? publishCandidateMut.variables ?? null : null}
              onRejectCandidate={(candidateId, reason) => rejectCandidateMut.mutate({ candidateId, reason })}
              rejectingCandidateId={rejectCandidateMut.isPending ? rejectCandidateMut.variables?.candidateId ?? null : null}
            />
          </div>
        </main>
      </div>
      )}
    </div>
  );
}

function DemoDashboardPanel({
  trainingSummary,
  trainingLoading,
  observabilitySummary,
  observabilityLoading,
  timeRange,
  onTimeRangeChange,
  onExportEvidence,
  exportingEvidence,
  runtimeReadiness,
  runtimeLoading,
  runs,
  assets,
  cases,
  candidates,
}: {
  trainingSummary: PromptEvaluationSummary | null;
  trainingLoading: boolean;
  observabilitySummary: ObservabilitySummary | null;
  observabilityLoading: boolean;
  timeRange: DemoTimeRange;
  onTimeRangeChange: (value: DemoTimeRange) => void;
  onExportEvidence: () => void | Promise<void>;
  exportingEvidence: boolean;
  runtimeReadiness: PromptEvaluationRuntimeReadiness;
  runtimeLoading: boolean;
  runs: PromptEvaluationRun[];
  assets: PromptEvaluationAsset[];
  cases: PromptEvaluationStructuredCase[];
  candidates: PromptEvaluationOptimizationCandidate[];
}) {
  const trainingMetrics = trainingSummary?.指标 ?? {};
  const trainingAssets = trainingSummary?.资产统计 ?? {};
  const runStatus = trainingSummary?.运行状态 ?? {};
  const observabilityMetrics = observabilitySummary?.指标 ?? {};
  const completeness = observabilitySummary?.summary_completeness;
  const completenessStatus = String(completeness?.["状态"] ?? observabilityMetrics["汇总完整性"] ?? "完整");
  const maybeTruncated = Boolean(
    observabilitySummary?.sop_run_maybe_truncated ||
      observabilitySummary?.task_trace_maybe_truncated ||
      completenessStatus === "可能截断",
  );
  const latestRun = runs[0] ?? null;
  const pendingCandidates = candidates.filter((candidate) => candidate.status === "待确认").length;
  const publishedCandidates = candidates.filter((candidate) => candidate.status === "已发布").length;
  const rejectedCandidates = candidates.filter((candidate) => candidate.status === "已拒绝").length;
  const hasAgentEvidence = runs.some((run) => run.run_kind === "Agent执行" && Boolean(run.task_id));
  const hasOptimizationLoop = publishedCandidates > 0 || pendingCandidates > 0 || rejectedCandidates > 0;
  const readinessLabel = runtimeLoading ? "检查中" : runtimeReadiness.label;
  const activeRange = DEMO_TIME_RANGES.find((item) => item.value === timeRange) ?? DEFAULT_DEMO_TIME_RANGE;

  const trainingItems: Array<[string, string]> = [
    ["运行总数", formatNumber(runStatus["运行总数"])],
    ["通过率", formatPercent(trainingMetrics["通过率"])],
    ["失败数", formatNumber(trainingMetrics["失败数"])],
    ["Agent运行数", formatNumber(trainingMetrics["Agent运行数"])],
    ["需人工复核", formatNumber(trainingMetrics["需人工复核"])],
    ["输入 token", formatNumber(trainingMetrics["输入token"])],
    ["输出 token", formatNumber(trainingMetrics["输出token"])],
    ["预估成本", formatMoney(trainingMetrics["预估成本"])],
  ];
  const observabilityItems: Array<[string, string]> = [
    ["SOP 执行数", formatNumber(observabilityMetrics["SOP 执行数"])],
    ["SOP 事件数", formatNumber(observabilityMetrics["SOP 事件数"])],
    ["任务观测", formatNumber(observabilitySummary?.task_trace_sample_total ?? observabilitySummary?.task_trace_total)],
    ["队列等待", formatDuration(observabilityMetrics["队列等待"])],
    ["执行耗时", formatDuration(observabilityMetrics["执行耗时"])],
    ["总耗时", formatDuration(observabilityMetrics["总耗时"])],
    ["观测输入 token", formatNumber(observabilityMetrics["输入 token"])],
    ["观测预估成本", formatMoney(observabilityMetrics["预估成本"])],
  ];
  const proofItems: Array<[string, string]> = [
    ["提示词库", formatNumber(assets.length)],
    ["资产总数", formatNumber(trainingAssets["资产总数"] ?? assets.length)],
    ["结构化用例", formatNumber(trainingAssets["结构化用例"] ?? cases.length)],
    ["优化候选", `${pendingCandidates} 待确认 · ${publishedCandidates} 已发布 · ${rejectedCandidates} 已拒绝`],
    ["真实 Agent 证据", hasAgentEvidence ? "已有任务/trace 运行记录" : "暂无真实 Agent 运行记录"],
    ["最近运行", latestRun ? summarizeLatestRunForDemo(latestRun) : "暂无运行"],
  ];

  return (
    <section className="mx-auto flex max-w-7xl flex-col gap-4" data-testid="training-demo-dashboard">
      <div className="flex flex-col gap-2 border-b pb-3 md:flex-row md:items-end md:justify-between">
        <div>
          <h2 className="text-base font-semibold">团队生产看板</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            汇总训练评估、真实 Agent、SOP 观测和验收证据，当前观测范围：{activeRange.label}。
          </p>
        </div>
        <div className="flex flex-col gap-2 md:items-end">
          <div className="flex flex-wrap gap-1.5">
            {DEMO_TIME_RANGES.map((range) => (
              <FilterButton key={range.value} active={timeRange === range.value} onClick={() => onTimeRangeChange(range.value)}>
                {range.label}
              </FilterButton>
            ))}
          </div>
          <div className="flex flex-wrap gap-2 text-xs">
            <Badge variant={runtimeReadiness.status === "就绪" ? "secondary" : "outline"}>真实 Agent：{readinessLabel}</Badge>
            <Badge variant={maybeTruncated ? "outline" : "secondary"}>观测完整性：{completenessStatus}</Badge>
            <Badge variant={hasOptimizationLoop ? "secondary" : "outline"}>优化闭环：{hasOptimizationLoop ? "已有证据" : "待补齐"}</Badge>
            <Button size="sm" variant="secondary" className="h-7 text-xs" onClick={onExportEvidence} disabled={exportingEvidence}>
              {exportingEvidence ? <Loader2 className="size-3.5 animate-spin" /> : <Download className="size-3.5" />}
              {exportingEvidence ? "导出中" : "导出证据 JSON"}
            </Button>
          </div>
        </div>
      </div>

      {maybeTruncated && (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-800 dark:text-amber-200">
          {String(completeness?.["说明"] ?? "当前观测摘要可能达到采样上限；用于汇报前请缩小时间、项目、小队或 Agent 范围。")}
        </div>
      )}

      <div className="grid gap-3 lg:grid-cols-2">
        <DemoMetricSection
          title="训练评估闭环"
          subtitle={trainingLoading ? "正在刷新训练评估摘要" : trainingSummary?.last_run_at ? `最近运行 ${trainingSummary.last_run_at}` : "暂无运行记录"}
          items={trainingItems}
        />
        <DemoMetricSection
          title="SOP 与任务观测"
          subtitle={
            observabilityLoading
              ? `正在刷新${activeRange.label}观测摘要`
              : `${activeRange.label} · ${String(completeness?.["说明"] ?? "当前筛选条件下的 SOP 执行和任务观测未达到采样上限。")}`
          }
          items={observabilityItems}
        />
      </div>

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_360px]">
        <section className="rounded-md border border-border/70 bg-muted/10 p-3">
          <div className="mb-3 flex items-center justify-between gap-2">
            <div>
              <h3 className="text-sm font-semibold">验收证据</h3>
              <p className="mt-1 text-xs text-muted-foreground">这些数据来自后端摘要、运行记录、任务 trace 和结构化评测用例。</p>
            </div>
            <Badge variant="outline">证据链</Badge>
          </div>
          <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
            {proofItems.map(([label, value]) => (
              <div key={label} className="min-w-0 rounded-md border bg-background px-3 py-2" data-testid={`training-demo-proof-${label}`}>
                <div className="truncate text-[11px] text-muted-foreground">{label}</div>
                <div className="mt-1 truncate text-sm font-semibold">{value}</div>
              </div>
            ))}
          </div>
        </section>

        <section className="rounded-md border border-border/70 bg-muted/10 p-3">
          <h3 className="text-sm font-semibold">演示状态</h3>
          <div className="mt-3 grid gap-2 text-xs">
            <DemoChecklistItem ok={runtimeReadiness.status === "就绪"} label="CodeBuddy 运行时可创建真实 Agent 任务" detail={runtimeReadiness.detail} />
            <DemoChecklistItem ok={hasAgentEvidence} label="运行历史已有任务/trace 证据" detail={latestRun?.task_id ? `最近任务 ${latestRun.task_id}` : "需要执行一次真实 Agent 评估"} />
            <DemoChecklistItem ok={cases.length > 0} label="数据集/测试套件已有结构化用例" detail={`${cases.length} 条结构化用例`} />
            <DemoChecklistItem ok={hasOptimizationLoop} label="失败用例可进入优化候选人工确认" detail={`${pendingCandidates} 待确认，${publishedCandidates} 已发布`} />
            <DemoChecklistItem ok={!maybeTruncated} label="观测摘要可直接用于汇报" detail={String(completeness?.["说明"] ?? "当前摘要完整")} />
          </div>
        </section>
      </div>

      <div className="grid gap-3 lg:grid-cols-2">
        <UsageList title="模型用量明细" rows={observabilitySummary?.model_breakdown ?? []} />
        <UsageList title="运行时用量明细" rows={observabilitySummary?.runtime_breakdown ?? []} />
      </div>
    </section>
  );
}

function DemoMetricSection({ title, subtitle, items }: { title: string; subtitle: string; items: Array<[string, string]> }) {
  return (
    <section className="rounded-md border border-border/70 bg-muted/10 p-3">
      <div className="mb-3">
        <h3 className="text-sm font-semibold">{title}</h3>
        <p className="mt-1 text-xs text-muted-foreground">{subtitle}</p>
      </div>
      <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
        {items.map(([label, value]) => (
          <div key={label} className="min-w-0 rounded-md border bg-background px-3 py-2" data-testid={`training-demo-metric-${label}`}>
            <div className="truncate text-[11px] text-muted-foreground">{label}</div>
            <div className="mt-1 truncate text-sm font-semibold">{value}</div>
          </div>
        ))}
      </div>
    </section>
  );
}

function DemoChecklistItem({ ok, label, detail }: { ok: boolean; label: string; detail: string }) {
  return (
    <div className="rounded-md border bg-background px-3 py-2">
      <div className="flex items-center justify-between gap-2">
        <span className="font-medium text-foreground">{label}</span>
        <Badge variant={ok ? "secondary" : "outline"}>{ok ? "已具备" : "待补齐"}</Badge>
      </div>
      <div className="mt-1 text-[11px] text-muted-foreground">{detail}</div>
    </div>
  );
}

function UsageList({ title, rows }: { title: string; rows: ObservabilitySummary["model_breakdown"] }) {
  return (
    <section className="rounded-md border border-border/70 bg-muted/10 p-3">
      <h3 className="text-sm font-semibold">{title}</h3>
      {rows.length === 0 ? (
        <div className="mt-3 rounded-md border border-dashed px-3 py-4 text-sm text-muted-foreground">暂无用量数据</div>
      ) : (
        <div className="mt-3 divide-y rounded-md border bg-background">
          {rows.slice(0, 5).map((row) => {
            const name = String(row["名称"] || row.model || row.runtime || "未记录");
            const tokenTotal =
              Number(row["输入 token"] ?? 0) +
              Number(row["输出 token"] ?? 0) +
              Number(row["缓存读 token"] ?? 0) +
              Number(row["缓存写 token"] ?? 0);
            return (
              <div key={`${title}-${name}`} className="grid gap-1 px-3 py-2 text-xs md:grid-cols-[minmax(0,1fr)_auto]">
                <div className="min-w-0">
                  <div className="truncate font-medium text-foreground">{name}</div>
                  <div className="mt-1 text-[11px] text-muted-foreground">{row.provider || "未记录提供方"} · {row.runtime || row.model || "未记录运行时/模型"}</div>
                </div>
                <div className="text-muted-foreground md:text-right">
                  <div>{tokenTotal.toLocaleString("zh-CN")} token</div>
                  <div>{formatMoney(row["预估成本"])}{row["价格已知"] ? "" : " · 缺少价格"}</div>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

function TrainingSummaryStrip({ summary, loading }: { summary: PromptEvaluationSummary | null; loading: boolean }) {
  const metrics = summary?.指标 ?? {};
  const assets = summary?.资产统计 ?? {};
  const runStatus = summary?.运行状态 ?? {};
  const candidates = summary?.优化候选 ?? {};
  const items = [
    { label: "运行总数", value: formatNumber(runStatus["运行总数"]) },
    { label: "通过率", value: formatPercent(metrics["通过率"]) },
    { label: "失败数", value: formatNumber(metrics["失败数"]) },
    { label: "Agent运行数", value: formatNumber(metrics["Agent运行数"]) },
    { label: "需人工复核", value: formatNumber(metrics["需人工复核"]) },
    { label: "输入 token", value: formatNumber(metrics["输入token"]) },
    { label: "输出 token", value: formatNumber(metrics["输出token"]) },
    { label: "预估成本", value: formatMoney(metrics["预估成本"]) },
    { label: "待确认优化候选", value: formatNumber(candidates["待确认"]) },
    { label: "已发布优化候选", value: formatNumber(candidates["已发布"]) },
    { label: "资产总数", value: formatNumber(assets["资产总数"]) },
    { label: "结构化用例", value: formatNumber(assets["结构化用例"]) },
  ];

  return (
    <section className="shrink-0 border-b bg-muted/20 px-3 py-3" data-testid="training-summary-strip">
      <div className="mb-2 flex min-w-0 items-center justify-between gap-3">
        <div className="min-w-0">
          <h2 className="truncate text-sm font-semibold">领导视角摘要</h2>
          <p className="mt-0.5 truncate text-xs text-muted-foreground">
            {summary?.last_run_at ? `最近运行 ${summary.last_run_at}` : "暂无运行记录"}
          </p>
        </div>
        <Badge variant="outline" className="shrink-0">
          {loading ? "刷新中" : "训练评估"}
        </Badge>
      </div>
      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
        {items.map((item) => (
          <div key={item.label} className="min-w-0 rounded-md border bg-background px-3 py-2" data-testid={`training-summary-${item.label}`}>
            <div className="truncate text-[11px] text-muted-foreground">{item.label}</div>
            <div className="mt-1 truncate text-sm font-semibold">{item.value}</div>
          </div>
        ))}
      </div>
    </section>
  );
}

function WorkbenchPanel({
  activeTab,
  workspaceId,
  selected,
  assets,
  cases,
  runs,
  candidates,
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
  onCreateCase,
  creatingCaseAssetId,
  onUpdateCase,
  updatingCaseId,
  onDeleteCase,
  deletingCaseId,
  onSyncRun,
  syncingRunId,
  onGenerateCandidate,
  generatingCandidateRunId,
  onRunOptimizationAgent,
  runningOptimizationAgentRunId,
  onUpdateCandidate,
  updatingCandidateId,
  onPublishCandidate,
  publishingCandidateId,
  onRejectCandidate,
  rejectingCandidateId,
}: {
  activeTab: WorkbenchTab;
  workspaceId: string;
  selected: PromptLibraryItem | null;
  assets: PromptEvaluationAsset[];
  cases: PromptEvaluationStructuredCase[];
  runs: PromptEvaluationRun[];
  candidates: PromptEvaluationOptimizationCandidate[];
  loading: boolean;
    saving: boolean;
    runningAgent: boolean;
    runtimeReadiness: PromptEvaluationRuntimeReadiness;
    runtimeLoading: boolean;
    agentExpectedText: string;
    onAgentExpectedTextChange: (value: string) => void;
    onCreateAsset: (assetType: PromptEvaluationAssetType) => void;
    onSaveAgentDebugPackage: () => void;
    onRunAgentDebugPackage: () => void;
    onToggleAssetStatus: (asset: PromptEvaluationAsset) => void;
  onDeleteAsset: (asset: PromptEvaluationAsset) => void;
  onCreateCase: (data: CreatePromptEvaluationCaseRequest) => void;
  creatingCaseAssetId: string | null;
  onUpdateCase: (caseId: string, data: UpdatePromptEvaluationCaseRequest) => void;
  updatingCaseId: string | null;
  onDeleteCase: (caseId: string) => void;
  deletingCaseId: string | null;
  onSyncRun: (runId: string) => void;
  syncingRunId: string | null;
  onGenerateCandidate: (runId: string) => void;
  generatingCandidateRunId: string | null;
  onRunOptimizationAgent: (runId: string) => void;
  runningOptimizationAgentRunId: string | null;
  onUpdateCandidate: (candidateId: string, data: UpdatePromptEvaluationOptimizationCandidateRequest) => void;
  updatingCandidateId: string | null;
  onPublishCandidate: (candidateId: string) => void;
  publishingCandidateId: string | null;
  onRejectCandidate: (candidateId: string, reason: string) => void;
  rejectingCandidateId: string | null;
}) {
  const tabAssetType = tabToAssetType(activeTab);
  const visibleAssets = tabAssetType ? assets.filter((asset) => asset.asset_type === tabAssetType) : assets;
  const experiments = assets.filter((asset) => asset.asset_type === "实验");
  const caseSummaries = useMemo(() => buildCaseSummaries(cases), [cases]);
  const casesByAsset = useMemo(() => buildCasesByAsset(cases), [cases]);
  const [caseDrafts, setCaseDrafts] = useState<Record<string, ManualCaseDraft>>({});
  const candidatesByRun = useMemo(() => buildCandidatesByRun(candidates), [candidates]);
  const [expandedRunId, setExpandedRunId] = useState<string | null>(null);
  const evidenceQuery = useQuery({
    queryKey: promptLibraryKeys.runEvidence(workspaceId, expandedRunId),
    queryFn: () => api.getPromptEvaluationRunEvidence(expandedRunId ?? ""),
    enabled: !!expandedRunId,
  });

  if (activeTab === "提示词库" || activeTab === "提示词调试场") {
    return null;
  }

	if (activeTab === "Agent 调试场") {
	    return (
	      <section className="grid gap-3 border-t pt-4">
	        <div className="flex items-center justify-between gap-2">
	          <div>
	            <h3 className="text-sm font-semibold">Agent 调试场</h3>
	            <p className="mt-1 text-xs text-muted-foreground">可先保存实验包，也可在 CodeBuddy 就绪后创建真实 Agent 任务并写入运行历史。</p>
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
            <span className="text-muted-foreground">目标模型 {runtimeReadiness.model || DEFAULT_AGENT_MODEL}</span>
          </div>
          <div className="text-muted-foreground">{runtimeLoading ? "正在检查当前工作区的运行时列表。" : runtimeReadiness.detail}</div>
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
            {activeTab === "运行历史" ? "按结构化运行记录展示运行、任务、模型、耗时和评估结论。" : "复用提示词评测资产，全部语义按中文记录。"}
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
              <div key={run.id} data-testid={`prompt-evaluation-run-${run.id}`} className="grid gap-2 px-3 py-3 md:grid-cols-[minmax(0,1fr)_auto]">
                <div className="min-w-0">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-sm font-medium">{run.run_kind} · {run.status}</span>
                    <Badge variant={run.status === "通过" ? "secondary" : run.status === "已入队" || run.status === "运行中" ? "outline" : "destructive"} className="shrink-0">
                      {run.total_cases} 用例 · 通过率 {Math.round(run.pass_rate * 100)}%
                    </Badge>
                  </div>
                  <div className="mt-1 text-xs text-muted-foreground">{summarizeStructuredRun(run)}</div>
                  <div className="mt-1 break-all text-[11px] text-muted-foreground">
                    运行 {run.id}{run.task_id ? ` · 任务 ${run.task_id}` : ""}
                  </div>
                </div>
                <div className="flex items-center justify-end gap-2 text-right text-[11px] text-muted-foreground">
                  <div>
                    <div>{run.created_at}</div>
                    <div>{run.total_duration_ms} ms</div>
                  </div>
                  <Button size="sm" variant="secondary" onClick={() => setExpandedRunId(expandedRunId === run.id ? null : run.id)}>
                    {expandedRunId === run.id ? "收起证据" : "查看证据"}
                  </Button>
                  {run.task_id && (
                    <Button size="sm" variant="secondary" onClick={() => onSyncRun(run.id)} disabled={syncingRunId === run.id}>
                      {syncingRunId === run.id ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
                      同步任务
                    </Button>
                  )}
                  {canGenerateOptimizationCandidate(run) && (
                    <>
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => onRunOptimizationAgent(run.id)}
                        disabled={runningOptimizationAgentRunId === run.id || run.status === "已入队" || run.status === "运行中"}
                      >
                        {runningOptimizationAgentRunId === run.id ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
                        Agent 优化任务
                      </Button>
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => onGenerateCandidate(run.id)}
                        disabled={generatingCandidateRunId === run.id || (candidatesByRun.get(run.id)?.some((candidate) => candidate.status === "待确认") ?? false)}
                      >
                        {generatingCandidateRunId === run.id ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
                        {candidatesByRun.get(run.id)?.some((candidate) => candidate.status === "待确认") ? "已有候选" : "生成优化候选"}
                      </Button>
                    </>
                  )}
                </div>
                {expandedRunId === run.id && (
                  <RunEvidencePanel
                    evidence={evidenceQuery.data ?? null}
                    loading={evidenceQuery.isLoading || evidenceQuery.isFetching}
                    error={evidenceQuery.isError}
                  />
                )}
              </div>
            ))}
          </div>
        )
      )}

      {activeTab !== "运行历史" && (
        <>
          {activeTab === "优化运行" && (
            <OptimizationCandidateList
              candidates={candidates}
              onUpdateCandidate={onUpdateCandidate}
              updatingCandidateId={updatingCandidateId}
              onPublishCandidate={onPublishCandidate}
              publishingCandidateId={publishingCandidateId}
              onRejectCandidate={onRejectCandidate}
              rejectingCandidateId={rejectingCandidateId}
            />
          )}
          {loading ? (
          <div className="h-20 rounded-md bg-muted/60" />
        ) : visibleAssets.length === 0 ? (
          <div className="rounded-md border border-dashed px-3 py-6 text-center text-sm text-muted-foreground">暂无资产</div>
        ) : (
          <div className="divide-y rounded-md border">
            {visibleAssets.map((asset) => (
              <div key={asset.id} data-testid={`prompt-evaluation-asset-${asset.id}`} className="grid gap-2 px-3 py-3 md:grid-cols-[minmax(0,1fr)_auto]">
                <div className="min-w-0">
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-sm font-medium">{asset.name}</span>
                    <Badge variant={asset.status === "启用" ? "secondary" : "outline"} className="shrink-0">
                      {asset.asset_type} · {asset.status}
                    </Badge>
                  </div>
                  <div className="mt-1 truncate text-xs text-muted-foreground">{asset.description || "无描述"}</div>
                  <div className="mt-1 text-[11px] text-muted-foreground">
                    更新于 {asset.updated_at} · {summarizeAssetPayload(asset, caseSummaries.get(asset.id))}
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
                {canManageStructuredCases(asset) && (
                  <ManualCasePanel
                    asset={asset}
                    cases={casesByAsset.get(asset.id) ?? []}
                    draft={caseDrafts[asset.id] ?? emptyManualCaseDraft()}
                    onDraftChange={(draft) => setCaseDrafts((prev) => ({ ...prev, [asset.id]: draft }))}
                    onCreateCase={() => {
                      const draft = caseDrafts[asset.id] ?? emptyManualCaseDraft();
                      onCreateCase(buildManualCaseRequest(asset, draft, casesByAsset.get(asset.id)?.length ?? 0));
                      setCaseDrafts((prev) => ({ ...prev, [asset.id]: emptyManualCaseDraft() }));
                    }}
                    creating={creatingCaseAssetId === asset.id}
                    onUpdateCase={onUpdateCase}
                    updatingCaseId={updatingCaseId}
                    onDeleteCase={onDeleteCase}
                    deletingCaseId={deletingCaseId}
                  />
                )}
              </div>
            ))}
          </div>
        )}
        </>
      )}
    </section>
  );
}

function OptimizationCandidateList({
  candidates,
  onUpdateCandidate,
  updatingCandidateId,
  onPublishCandidate,
  publishingCandidateId,
  onRejectCandidate,
  rejectingCandidateId,
}: {
  candidates: PromptEvaluationOptimizationCandidate[];
  onUpdateCandidate: (candidateId: string, data: UpdatePromptEvaluationOptimizationCandidateRequest) => void;
  updatingCandidateId: string | null;
  onPublishCandidate: (candidateId: string) => void;
  publishingCandidateId: string | null;
  onRejectCandidate: (candidateId: string, reason: string) => void;
  rejectingCandidateId: string | null;
}) {
  const [editingCandidateId, setEditingCandidateId] = useState<string | null>(null);
  const [rejectingDraftCandidateId, setRejectingDraftCandidateId] = useState<string | null>(null);
  const [drafts, setDrafts] = useState<Record<string, UpdatePromptEvaluationOptimizationCandidateRequest>>({});
  const [rejectReasons, setRejectReasons] = useState<Record<string, string>>({});
  if (candidates.length === 0) {
    return (
      <div className="rounded-md border border-dashed px-3 py-5 text-center text-sm text-muted-foreground">
        暂无优化候选。先在运行历史里对失败运行生成候选。
      </div>
    );
  }
  return (
    <div className="divide-y rounded-md border">
      {candidates.map((candidate) => {
        const editing = editingCandidateId === candidate.id;
        const writingRejectReason = rejectingDraftCandidateId === candidate.id;
        const draft = drafts[candidate.id] ?? candidateToDraft(candidate);
        const rejectReason = rejectReasons[candidate.id] ?? "候选未覆盖验收要求，暂不采纳。";
        const canHandle = candidate.status === "待确认";
        const hasManualEdit = Boolean((candidate.metrics as Record<string, unknown>)["人工编辑"]);
        return (
          <div key={candidate.id} data-testid={`prompt-evaluation-candidate-${candidate.id}`} className="grid gap-2 px-3 py-3 md:grid-cols-[minmax(0,1fr)_auto]">
            <div className="min-w-0">
              <div className="flex min-w-0 items-center gap-2">
                <span className="truncate text-sm font-medium">{candidate.candidate_name}</span>
                <Badge variant={canHandle ? "secondary" : "outline"} className="shrink-0">
                  {candidate.status} · 失败 {candidate.failed_case_count}
                </Badge>
                {hasManualEdit && <Badge variant="outline" className="shrink-0">已人工编辑</Badge>}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">{candidate.rationale || "基于失败用例生成，等待人工确认。"}</div>
              <div className="mt-2 max-h-28 overflow-auto whitespace-pre-wrap rounded-sm border bg-muted/30 px-2 py-1.5 text-[11px] text-foreground">
                {candidate.candidate_content}
              </div>
              <div className="mt-1 break-all text-[11px] text-muted-foreground">
                来源运行 {candidate.run_id}{candidate.published_prompt_id ? ` · 已发布 ${candidate.published_prompt_id}` : ""}
              </div>
              {editing && (
                <div className="mt-3 grid gap-2 rounded-sm border border-border/70 bg-background px-2 py-2">
                  <label className="grid gap-1 text-xs">
                    <span className="text-muted-foreground">候选名称</span>
                    <Input
                      value={draft.candidate_name}
                      onChange={(event) => setDrafts((prev) => ({ ...prev, [candidate.id]: { ...draft, candidate_name: event.target.value } }))}
                    />
                  </label>
                  <label className="grid gap-1 text-xs">
                    <span className="text-muted-foreground">候选提示词正文</span>
                    <Textarea
                      value={draft.candidate_content}
                      onChange={(event) => setDrafts((prev) => ({ ...prev, [candidate.id]: { ...draft, candidate_content: event.target.value } }))}
                      className="min-h-36 font-mono text-xs"
                    />
                  </label>
                  <label className="grid gap-1 text-xs">
                    <span className="text-muted-foreground">优化依据</span>
                    <Textarea
                      value={draft.rationale ?? ""}
                      onChange={(event) => setDrafts((prev) => ({ ...prev, [candidate.id]: { ...draft, rationale: event.target.value } }))}
                      className="min-h-16 text-xs"
                    />
                  </label>
                  <div className="flex flex-wrap items-center gap-2">
                    <Button
                      size="sm"
                      onClick={() => {
                        onUpdateCandidate(candidate.id, {
                          ...draft,
                          edit_note: "人工复核后调整候选名称、正文或优化依据。",
                        });
                      }}
                      disabled={updatingCandidateId === candidate.id}
                    >
                      {updatingCandidateId === candidate.id ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                      保存候选
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setEditingCandidateId(null)}>
                      取消编辑
                    </Button>
                  </div>
                </div>
              )}
              {writingRejectReason && (
                <div className="mt-3 grid gap-2 rounded-sm border border-destructive/30 bg-destructive/5 px-2 py-2">
                  <label className="grid gap-1 text-xs">
                    <span className="text-muted-foreground">暂不采纳原因</span>
                    <Textarea
                      value={rejectReason}
                      onChange={(event) => setRejectReasons((prev) => ({ ...prev, [candidate.id]: event.target.value }))}
                      className="min-h-20 text-xs"
                    />
                  </label>
                  <div className="flex flex-wrap items-center gap-2">
                    <Button
                      size="sm"
                      variant="destructive"
                      onClick={() => onRejectCandidate(candidate.id, rejectReason)}
                      disabled={rejectingCandidateId === candidate.id || rejectReason.trim() === ""}
                    >
                      {rejectingCandidateId === candidate.id ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
                      确认暂不采纳
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setRejectingDraftCandidateId(null)}>
                      取消
                    </Button>
                  </div>
                </div>
              )}
            </div>
            <div className="flex flex-wrap items-center gap-2 md:justify-end">
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  setDrafts((prev) => ({ ...prev, [candidate.id]: candidateToDraft(candidate) }));
                  setEditingCandidateId(editing ? null : candidate.id);
                }}
                disabled={!canHandle}
              >
                <BookOpenText className="size-3.5" />
                {editing ? "收起编辑" : "编辑候选"}
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => setRejectingDraftCandidateId(writingRejectReason ? null : candidate.id)}
                disabled={!canHandle || rejectingCandidateId === candidate.id}
              >
                {rejectingCandidateId === candidate.id ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
                {writingRejectReason ? "收起原因" : "暂不采纳"}
              </Button>
              <Button
                size="sm"
                onClick={() => onPublishCandidate(candidate.id)}
                disabled={!canHandle || publishingCandidateId === candidate.id}
              >
                {publishingCandidateId === candidate.id ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                发布新版本
              </Button>
            </div>
          </div>
        );
      })}
    </div>
  );
}

function candidateToDraft(candidate: PromptEvaluationOptimizationCandidate): UpdatePromptEvaluationOptimizationCandidateRequest {
  return {
    candidate_name: candidate.candidate_name,
    candidate_content: candidate.candidate_content,
    rationale: candidate.rationale,
  };
}

function RunEvidencePanel({
  evidence,
  loading,
  error,
}: {
  evidence: PromptEvaluationRunEvidence | null;
  loading: boolean;
  error: boolean;
}) {
  if (loading) {
    return <div className="md:col-span-2 rounded-md border bg-muted/30 px-3 py-4 text-sm text-muted-foreground">正在加载运行证据...</div>;
  }
  if (error || !evidence) {
    return <div className="md:col-span-2 rounded-md border border-dashed px-3 py-4 text-sm text-muted-foreground">运行证据暂不可用</div>;
  }
  const externalFailure = buildExternalDependencyFailureNotice(evidence);
  return (
    <div className="md:col-span-2 grid gap-3 rounded-md border bg-muted/20 p-3" data-testid={`run-evidence-${evidence.run.id}`}>
      {externalFailure && (
        <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs" data-testid="run-evidence-external-failure">
          <div className="font-medium text-destructive">{externalFailure.title}</div>
          <div className="mt-1 text-muted-foreground">{externalFailure.detail}</div>
        </div>
      )}
      <div className="grid gap-2 text-xs sm:grid-cols-2 lg:grid-cols-4">
        <MetricChip label="运行类型" value={evidence.run.run_kind} />
        <MetricChip label="运行状态" value={evidence.run.status} />
        <MetricChip label="模型" value={evidence.run.model || "未记录"} />
        <MetricChip label="运行时" value={evidence.run.runtime_provider || "未记录"} />
        <MetricChip label="触发来源" value={evidence.run.trigger_source || "未记录"} />
        <MetricChip label="创建者" value={evidence.run.created_by ?? "未记录"} />
        <MetricChip label="智能体标识" value={evidence.run.agent_id ?? "未记录"} />
        <MetricChip label="运行时标识" value={evidence.run.runtime_id ?? "未记录"} />
        <MetricChip label="会话标识" value={evidence.run.chat_session_id ?? "未记录"} />
        <MetricChip label="总用例数" value={String(evidence.run.total_cases)} />
        <MetricChip label="通过数" value={String(evidence.run.passed_cases)} />
        <MetricChip label="失败数" value={String(evidence.run.failed_cases)} />
        <MetricChip label="总耗时" value={`${evidence.run.total_duration_ms} ms`} />
        <MetricChip label="平均耗时" value={`${evidence.run.average_duration_ms} ms`} />
        <MetricChip label="输入 token" value={String(evidence.run.input_tokens)} />
        <MetricChip label="输出 token" value={String(evidence.run.output_tokens)} />
        <MetricChip label="预估成本" value={formatMoney(evidence.run.estimated_cost)} />
        <MetricChip label="trace/任务标识" value={evidence.run.task_id ?? evidence.run.id} />
        <MetricChip label="开始时间" value={evidence.run.started_at || "未记录"} />
        <MetricChip label="结束时间" value={evidence.run.completed_at || "未完成"} />
        <MetricChip label="创建时间" value={evidence.run.created_at || "未记录"} />
        <MetricChip label="更新时间" value={evidence.run.updated_at || "未记录"} />
        <MetricChip label="失败原因" value={evidence.run.failure_reason || "无"} />
        <MetricChip label="评估结论" value={evidence.run.conclusion || "未记录"} />
      </div>

      <EvidenceContextPanel context={evidence.上下文} />

      <div className="grid gap-2">
        <div className="text-xs font-medium text-muted-foreground">用例明细</div>
        {evidence.trials.length === 0 ? (
          <div className="rounded-md border border-dashed px-3 py-3 text-xs text-muted-foreground">暂无单次执行记录</div>
        ) : (
          <div className="divide-y rounded-md border bg-background">
            {evidence.trials.map((trial) => (
              <div key={trial.id} className="grid gap-1 px-3 py-2 text-xs">
                <div className="flex min-w-0 items-center gap-2">
                  <span className="truncate font-medium">{trial.case_name || `用例 ${trial.case_index + 1}`}</span>
                  <Badge variant={trial.status === "通过" ? "secondary" : trial.status === "待执行" ? "outline" : "destructive"}>{trial.status}</Badge>
                  <span className="ml-auto text-muted-foreground">{trial.duration_ms} ms</span>
                </div>
                {trial.failure_reason && trial.failure_reason !== "无" && <div className="text-muted-foreground">失败原因：{trial.failure_reason}</div>}
                {trial.rendered_prompt && <pre className="max-h-32 overflow-auto whitespace-pre-wrap rounded bg-muted/30 p-2 text-[11px] leading-5">{trial.rendered_prompt}</pre>}
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="grid gap-2 md:grid-cols-3">
        <EvidenceList
          title="任务用量"
          empty="暂无 token 用量"
          items={evidence.task_usage.map((usage) => `${usage.provider}/${usage.model} · 输入 ${usage.input_tokens} · 输出 ${usage.output_tokens} · 预估成本 ${formatMoney(usage.estimated_cost ?? 0)} · 缓存读 ${usage.cache_read_tokens} · 缓存写 ${usage.cache_write_tokens}${usage.priced === false ? " · 缺少价格" : ""}`)}
        />
        <EvidenceList
          title="任务消息"
          empty="暂无任务消息"
          items={evidence.task_messages.map((message) => `#${message.seq} ${message.type}${message.tool ? ` · ${message.tool}` : ""}：${truncateText(message.content || message.output || "", 160)}`)}
        />
        <EvidenceList
          title="trace 事件"
          empty="暂无 trace 事件"
          items={evidence.trace_events.map(formatTraceEventEvidence)}
        />
      </div>

      <details className="rounded-md border bg-background px-3 py-2 text-xs">
        <summary className="cursor-pointer font-medium text-muted-foreground">完整运行证据 JSON</summary>
        <pre className="mt-2 max-h-56 overflow-auto whitespace-pre-wrap text-[11px] leading-5">{truncateText(JSON.stringify(evidence, null, 2), 5000)}</pre>
      </details>
    </div>
  );
}

function EvidenceContextPanel({ context }: { context: Record<string, unknown> }) {
  const inputOutput = isRecord(context["输入输出摘要"]) ? context["输入输出摘要"] : {};
  const completeness = isRecord(context["证据完整性"]) ? context["证据完整性"] : {};
  const items = [
    `工作区 ${stringFromUnknown(context["工作区"]) || "未记录"}`,
    `提示词 ${stringFromUnknown(context["提示词名称"]) || stringFromUnknown(context["提示词"]) || "未绑定"}`,
    `评测资产 ${stringFromUnknown(context["评测资产名称"]) || stringFromUnknown(context["评测资产"]) || "未记录"}`,
    `Agent ${stringFromUnknown(context["执行Agent名称"]) || stringFromUnknown(context["执行Agent"]) || "未记录"}`,
    `运行时 ${stringFromUnknown(context["运行时名称"]) || stringFromUnknown(context["运行时标识"]) || stringFromUnknown(context["运行时"]) || "未记录"}`,
    `issue ${stringFromUnknown(context["issue标题"]) || stringFromUnknown(context["issue"]) || "未绑定"}`,
    `项目 ${stringFromUnknown(context["项目名称"]) || stringFromUnknown(context["项目"]) || "未绑定"}`,
    `小队 ${stringFromUnknown(context["小队名称"]) || stringFromUnknown(context["小队"]) || "未绑定"}`,
    `任务 ${stringFromUnknown(context["任务"]) || "未创建"}`,
    `任务状态 ${stringFromUnknown(context["任务状态"]) || stringFromUnknown(context["状态"]) || "未记录"}`,
    `执行模式 ${stringFromUnknown(context["任务执行模式"]) || "未记录"}`,
  ];
  const evidenceItems = [
    `用例 ${stringFromUnknown(completeness["用例数"]) || "0"}`,
    `用量证据 ${stringFromUnknown(completeness["任务用量条数"]) || "0"}`,
    `任务消息 ${stringFromUnknown(completeness["任务消息条数"]) || "0"}`,
    `trace 事件 ${stringFromUnknown(completeness["trace事件条数"]) || "0"}`,
  ];
  return (
    <div className="grid gap-2 rounded-md border bg-background p-2 text-xs" data-testid="run-evidence-context">
      <div className="font-medium text-muted-foreground">上下文摘要</div>
      <div className="grid gap-1 sm:grid-cols-2 lg:grid-cols-4">
        {items.map((item) => (
          <div key={item} className="truncate rounded bg-muted/30 px-2 py-1 text-[11px] leading-5">
            {item}
          </div>
        ))}
      </div>
      <div className="grid gap-1 sm:grid-cols-2 lg:grid-cols-4">
        {evidenceItems.map((item) => (
          <div key={item} className="truncate rounded bg-muted/30 px-2 py-1 text-[11px] leading-5">
            {item}
          </div>
        ))}
      </div>
      <div className="grid gap-1 text-[11px] leading-5 text-muted-foreground">
        <div>输入摘要：{truncateText(stringFromUnknown(inputOutput["用例输入摘要"]) || "未记录", 220)}</div>
        <div>输出摘要：{truncateText(stringFromUnknown(inputOutput["用例输出摘要"]) || "未记录", 220)}</div>
        <div>消息摘要：{truncateText(stringFromUnknown(inputOutput["消息摘要"]) || "未记录", 220)}</div>
      </div>
    </div>
  );
}

function MetricChip({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded-md border bg-background px-2 py-1.5" data-testid={`run-evidence-metric-${label}`}>
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className="truncate font-medium">{value || "未记录"}</div>
    </div>
  );
}

function EvidenceList({ title, empty, items }: { title: string; empty: string; items: string[] }) {
  return (
    <div className="grid gap-1.5 rounded-md border bg-background p-2 text-xs">
      <div className="font-medium text-muted-foreground">{title}</div>
      {items.length === 0 ? (
        <div className="text-muted-foreground">{empty}</div>
      ) : (
        <div className="grid gap-1">
          {items.slice(0, 6).map((item, index) => (
            <div key={`${title}-${index}`} className="break-words rounded bg-muted/30 px-2 py-1 text-[11px] leading-5">
              {item || "空消息"}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function buildExternalDependencyFailureNotice(evidence: PromptEvaluationRunEvidence): { title: string; detail: string } | null {
  const failureText = [
    evidence.run.failure_reason,
    evidence.run.conclusion,
    ...evidence.task_messages.map((message) => message.content || message.output || ""),
    ...evidence.trace_events.map((event) => [event.failure_reason, event.error_type, event.event_name].filter(Boolean).join(" ")),
  ].join("\n");
  if (failureText.includes("模型额度不足") || failureText.includes("无可用Token额度") || failureText.includes("Token额度")) {
    return {
      title: "外部依赖失败：模型额度不足",
      detail: "CodeBuddy 已领取并执行任务，但上游模型返回额度不足；本次不会产生 token 用量和成本，需补充 minimax/codebuddy 额度后重新运行。",
    };
  }
  if (evidence.run.status === "失败" && evidence.run.task_id && evidence.task_usage.length === 0) {
    return {
      title: "外部依赖失败：未采集到模型用量",
      detail: "Agent 任务失败且没有任务用量记录，请结合任务消息和 trace 事件确认运行时、模型或网络依赖状态。",
    };
  }
  return null;
}

function formatTraceEventEvidence(event: PromptEvaluationRunEvidence["trace_events"][number]): string {
  const pieces = [
    event.event_name || event.event_type || "未命名事件",
    event.status || "未知状态",
    event.provider || event.model ? `${event.provider || "未知提供方"}/${event.model || "未知模型"}` : "",
    `尝试次数 ${event.attempt}`,
    event.duration_ms != null ? `耗时 ${event.duration_ms} ms` : "",
    event.queue_wait_ms != null ? `排队 ${event.queue_wait_ms} ms` : "",
    event.run_ms != null ? `执行 ${event.run_ms} ms` : "",
    event.total_ms != null ? `总计 ${event.total_ms} ms` : "",
    `输入 ${event.input_tokens}`,
    `输出 ${event.output_tokens}`,
    event.failure_reason && event.failure_reason !== "无" ? `失败原因：${event.failure_reason}` : "",
    event.error_type ? `错误类型：${event.error_type}` : "",
  ].filter(Boolean);
  return pieces.join(" · ");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stringFromUnknown(value: unknown): string {
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return "";
}

function buildAgentExecutionStatus(readiness: PromptEvaluationRuntimeReadiness): string {
  const model = readiness.model || DEFAULT_AGENT_MODEL;
  if (readiness.status === "就绪") {
    return `CodeBuddy 运行时已在线，目标模型 ${model}；此记录是实验包快照，点击“创建真实 Agent 任务”后会入队并采集 trace、token、成本和输出`;
  }
  return `${readiness.label}，目标模型 ${model}；未创建真实 Agent 任务`;
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

type ManualCaseDraft = {
  caseName: string;
  variablesText: string;
  expectedText: string;
  tagsText: string;
};

function ManualCasePanel({
  asset,
  cases,
  draft,
  onDraftChange,
  onCreateCase,
  creating,
  onUpdateCase,
  updatingCaseId,
  onDeleteCase,
  deletingCaseId,
}: {
  asset: PromptEvaluationAsset;
  cases: PromptEvaluationStructuredCase[];
  draft: ManualCaseDraft;
  onDraftChange: (draft: ManualCaseDraft) => void;
  onCreateCase: () => void;
  creating: boolean;
  onUpdateCase: (caseId: string, data: UpdatePromptEvaluationCaseRequest) => void;
  updatingCaseId: string | null;
  onDeleteCase: (caseId: string) => void;
  deletingCaseId: string | null;
}) {
  const manualCases = cases.filter((item) => item.source === "manual");
  const [editingCaseId, setEditingCaseId] = useState<string | null>(null);
  const [editDrafts, setEditDrafts] = useState<Record<string, ManualCaseDraft>>({});
  return (
    <div data-testid={`prompt-evaluation-cases-${asset.id}`} className="md:col-span-2 grid gap-2 rounded-md border border-border/70 bg-muted/10 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-xs font-medium text-muted-foreground">结构化评测用例</div>
        <Badge variant="outline" className="text-[11px]">
          手工 {manualCases.length} · 总计 {cases.length}
        </Badge>
      </div>
      {cases.length > 0 ? (
        <div className="grid gap-1.5">
          {cases.map((item) => {
            const editing = editingCaseId === item.id;
            const editDraft = editDrafts[item.id] ?? manualCaseToDraft(item);
            return (
              <div key={item.id} className="grid gap-2 rounded border bg-background px-2 py-1.5 text-xs">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium text-foreground">{item.case_name || `用例 ${item.case_index + 1}`}</span>
                  <span className="text-muted-foreground">{item.source === "manual" ? "手工" : "资产载荷"} · {item.status}</span>
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">{summarizeStructuredCase(item)}</span>
                  {item.source === "manual" && (
                    <>
                      <Button
                        size="sm"
                        variant="secondary"
                        className="h-7"
                        onClick={() => {
                          setEditingCaseId(item.id);
                          setEditDrafts((prev) => ({ ...prev, [item.id]: manualCaseToDraft(item) }));
                        }}
                      >
                        编辑用例
                      </Button>
                      <Button size="sm" variant="destructive" className="h-7" onClick={() => onDeleteCase(item.id)} disabled={deletingCaseId === item.id}>
                        {deletingCaseId === item.id ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
                        删除用例
                      </Button>
                    </>
                  )}
                </div>
                {editing && (
                  <div className="grid gap-2 rounded-sm border border-border/70 bg-muted/20 p-2 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
                    <Input
                      value={editDraft.caseName}
                      onChange={(event) => setEditDrafts((prev) => ({ ...prev, [item.id]: { ...editDraft, caseName: event.target.value } }))}
                      placeholder="编辑用例名称"
                    />
                    <Textarea
                      value={editDraft.variablesText}
                      onChange={(event) => setEditDrafts((prev) => ({ ...prev, [item.id]: { ...editDraft, variablesText: event.target.value } }))}
                      className="min-h-20 text-xs"
                      placeholder="编辑变量：issue_title=登录失败"
                    />
                    <Input
                      value={editDraft.expectedText}
                      onChange={(event) => setEditDrafts((prev) => ({ ...prev, [item.id]: { ...editDraft, expectedText: event.target.value } }))}
                      placeholder="编辑期望包含"
                    />
                    <div className="flex gap-2">
                      <Input
                        value={editDraft.tagsText}
                        onChange={(event) => setEditDrafts((prev) => ({ ...prev, [item.id]: { ...editDraft, tagsText: event.target.value } }))}
                        placeholder="编辑标签"
                      />
                      <Button
                        size="sm"
                        className="h-10 shrink-0"
                        onClick={() => {
                          onUpdateCase(item.id, buildManualCaseUpdateRequest(asset, item, editDraft));
                          setEditingCaseId(null);
                        }}
                        disabled={updatingCaseId === item.id || !editDraft.caseName.trim()}
                      >
                        {updatingCaseId === item.id ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                        保存用例
                      </Button>
                      <Button size="sm" variant="ghost" className="h-10 shrink-0" onClick={() => setEditingCaseId(null)}>
                        取消
                      </Button>
                    </div>
                  </div>
                )}
              </div>
            );
          })}
        </div>
      ) : (
        <div className="rounded border border-dashed px-2 py-2 text-xs text-muted-foreground">暂无结构化用例，运行时会回退到资产载荷。</div>
      )}
      <div className="grid gap-2 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <Input
          value={draft.caseName}
          onChange={(event) => onDraftChange({ ...draft, caseName: event.target.value })}
          placeholder="手工用例名称"
        />
        <Textarea
          value={draft.variablesText}
          onChange={(event) => onDraftChange({ ...draft, variablesText: event.target.value })}
          className="min-h-20 text-sm"
          placeholder="变量：issue_title=登录失败"
        />
        <Input
          value={draft.expectedText}
          onChange={(event) => onDraftChange({ ...draft, expectedText: event.target.value })}
          placeholder="期望包含：验收条件, trace/任务标识"
        />
        <div className="flex gap-2">
          <Input
            value={draft.tagsText}
            onChange={(event) => onDraftChange({ ...draft, tagsText: event.target.value })}
            placeholder="标签：user-center, 回归"
          />
          <Button size="sm" className="h-10 shrink-0" onClick={onCreateCase} disabled={creating || !draft.caseName.trim()}>
            {creating ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
            新增用例
          </Button>
        </div>
      </div>
    </div>
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

function canManageStructuredCases(asset: PromptEvaluationAsset): boolean {
  return asset.asset_type === "数据集" || asset.asset_type === "测试套件" || asset.asset_type === "实验" || asset.asset_type === "优化运行";
}

function emptyManualCaseDraft(): ManualCaseDraft {
  return {
    caseName: "",
    variablesText: "",
    expectedText: "",
    tagsText: "",
  };
}

function buildManualCaseRequest(asset: PromptEvaluationAsset, draft: ManualCaseDraft, existingCount: number): CreatePromptEvaluationCaseRequest {
  return {
    asset_id: asset.id,
    prompt_id: asset.prompt_id,
    case_index: existingCount,
    case_name: draft.caseName.trim(),
    variables: parseDebugValues(draft.variablesText),
    expected_contains: splitList(draft.expectedText),
    input: {
      变量: parseDebugValues(draft.variablesText),
      来源: "训练与评估手工用例",
    },
    expected: {
      期望包含: splitList(draft.expectedText),
    },
    tags: splitList(draft.tagsText),
    status: "启用",
  };
}

function buildManualCaseUpdateRequest(asset: PromptEvaluationAsset, item: PromptEvaluationStructuredCase, draft: ManualCaseDraft): UpdatePromptEvaluationCaseRequest {
  const variables = parseDebugValues(draft.variablesText);
  const expectedContains = splitList(draft.expectedText);
  return {
    asset_id: asset.id,
    prompt_id: asset.prompt_id,
    case_index: item.case_index,
    case_name: draft.caseName.trim(),
    variables,
    expected_contains: expectedContains,
    input: {
      变量: variables,
      来源: "训练与评估手工用例",
      最近人工维护: new Date().toISOString(),
    },
    expected: {
      期望包含: expectedContains,
    },
    tags: splitList(draft.tagsText),
    status: item.status,
  };
}

function manualCaseToDraft(item: PromptEvaluationStructuredCase): ManualCaseDraft {
  return {
    caseName: item.case_name,
    variablesText: Object.entries(item.variables ?? {}).map(([key, value]) => `${key}=${String(value)}`).join("\n"),
    expectedText: item.expected_contains.map((value) => String(value)).join(", "),
    tagsText: item.tags.map((value) => String(value)).join(", "),
  };
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
      "输入 token",
      "输出 token",
      "预估成本",
      "执行Agent",
      "模型",
      "运行时",
      "trace/任务标识",
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
  readiness: PromptEvaluationRuntimeReadiness,
): CreatePromptEvaluationAssetRequest {
  return {
    prompt_id: prompt.id,
    name: `${prompt.name} Agent 调试包 ${new Date().toLocaleString("zh-CN")} #${Date.now()}`,
    description: `Agent 调试场记录：${buildAgentExecutionStatus(readiness)}`,
    asset_type: "实验",
    payload: {
      schema_version: 1,
      语义版本: "multica.training_evaluation.v1",
      cases: [
        {
          名称: "Agent 调试场用例",
          变量: values,
          期望包含: splitList(expectedOutput),
        },
      ],
      调试包: {
        提示词: prompt.name,
        变量: values,
        上下文: rendered,
        期望输出: expectedOutput,
        执行方式: buildAgentExecutionStatus(readiness),
      },
      运行环境: {
        目标运行时: "CodeBuddy",
        目标模型: readiness.model || DEFAULT_AGENT_MODEL,
        状态: readiness.status,
        说明: readiness.detail,
        修复路径: readiness.fix,
        运行时标识: readiness.runtime?.id ?? null,
      },
      对比维度: ["上下文完整性", "期望输出覆盖", "中文语义一致性"],
    },
    status: "启用",
  };
}

type CaseSummary = {
  total: number;
  manual: number;
  payload: number;
};

function buildCaseSummaries(cases: PromptEvaluationStructuredCase[]): Map<string, CaseSummary> {
  const counts = new Map<string, CaseSummary>();
  for (const item of cases) {
    const current = counts.get(item.asset_id) ?? { total: 0, manual: 0, payload: 0 };
    current.total += 1;
    if (item.source === "manual") {
      current.manual += 1;
    } else {
      current.payload += 1;
    }
    counts.set(item.asset_id, current);
  }
  return counts;
}

function buildCasesByAsset(cases: PromptEvaluationStructuredCase[]): Map<string, PromptEvaluationStructuredCase[]> {
  const result = new Map<string, PromptEvaluationStructuredCase[]>();
  for (const item of cases) {
    const bucket = result.get(item.asset_id) ?? [];
    bucket.push(item);
    result.set(item.asset_id, bucket);
  }
  for (const bucket of result.values()) {
    bucket.sort((a, b) => a.case_index - b.case_index || a.case_name.localeCompare(b.case_name, "zh-CN"));
  }
  return result;
}

function buildCandidatesByRun(candidates: PromptEvaluationOptimizationCandidate[]): Map<string, PromptEvaluationOptimizationCandidate[]> {
  const result = new Map<string, PromptEvaluationOptimizationCandidate[]>();
  for (const candidate of candidates) {
    const bucket = result.get(candidate.run_id) ?? [];
    bucket.push(candidate);
    result.set(candidate.run_id, bucket);
  }
  return result;
}

function canGenerateOptimizationCandidate(run: PromptEvaluationRun): boolean {
  if (!run.prompt_id) return false;
  if (run.failed_cases > 0) return true;
  if (run.status === "未通过" || run.status === "失败") return true;
  return Boolean(run.failure_reason && run.failure_reason !== "无");
}

function summarizeAssetPayload(asset: PromptEvaluationAsset, caseSummary?: CaseSummary): string {
  const payload = asset.payload ?? {};
  const cases = Array.isArray(payload.cases) ? payload.cases.length : Array.isArray(payload["数据集"]) ? payload["数据集"].length : 0;
  if (caseSummary && caseSummary.total > 0) {
    const sourceParts = [];
    if (caseSummary.manual > 0) sourceParts.push(`手工 ${caseSummary.manual}`);
    if (caseSummary.payload > 0) sourceParts.push(`资产载荷 ${caseSummary.payload}`);
    return `结构化用例 ${caseSummary.total} 个${sourceParts.length > 0 ? `（${sourceParts.join("，")}；运行优先使用）` : ""}`;
  }
  if (payload["最近Agent运行"]) return "包含真实 Agent 运行";
  if (payload["调试包"]) return "包含 Agent 调试包";
  if (payload["运行结果"]) return "包含运行结果";
  if (asset.asset_type === "实验") return `实验维度 ${Array.isArray(payload["对比维度"]) ? payload["对比维度"].length : 0} 个`;
  return cases > 0 ? `${cases} 个用例` : "未记录用例";
}

function summarizeStructuredCase(item: PromptEvaluationStructuredCase): string {
  const expected = item.expected_contains.map((value) => String(value)).filter(Boolean);
  const variables = Object.keys(item.variables ?? {});
  const parts = [];
  if (variables.length > 0) parts.push(`变量 ${variables.join("、")}`);
  if (expected.length > 0) parts.push(`期望 ${expected.join("、")}`);
  return parts.length > 0 ? parts.join(" · ") : "未记录变量和期望";
}

function summarizeAgentRun(asset: PromptEvaluationAsset): string | null {
  const payload = asset.payload ?? {};
  const run = payload["最近Agent运行"];
  if (!run || typeof run !== "object" || Array.isArray(run)) return null;
  const record = run as Record<string, unknown>;
  const status = stringFromRecord(record, "状态") || "未知状态";
  const taskId = stringFromRecord(record, "trace/任务标识") || stringFromRecord(record, "trace/task id");
  const agent = stringFromRecord(record, "执行Agent");
  const model = stringFromRecord(record, "模型");
  return `Agent 任务：${status}${taskId ? ` · 任务标识 ${taskId}` : ""}${agent ? ` · ${agent}` : ""}${model ? ` · ${model}` : ""}`;
}

function summarizeLatestRunForDemo(run: PromptEvaluationRun): string {
  const parts = [`${run.run_kind} · ${run.status}`];
  if (run.failure_reason && run.failure_reason !== "无") {
    parts.push(`失败原因：${truncateText(run.failure_reason, 42)}`);
  }
  if (run.task_id) {
    parts.push(`任务标识 ${truncateText(run.task_id, 8)}`);
  }
  return parts.join(" · ");
}

function summarizeStructuredRun(run: PromptEvaluationRun): string {
  const pieces = [
    `模型 ${run.model || "未记录"}`,
    `运行时 ${run.runtime_provider || "未记录"}`,
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

function truncateText(value: string, maxLength: number): string {
  return value.length > maxLength ? `${value.slice(0, maxLength)}...` : value;
}

function formatNumber(value: unknown): string {
  return typeof value === "number" && Number.isFinite(value) ? value.toLocaleString("zh-CN") : "0";
}

function formatMoney(value: unknown): string {
  const n = typeof value === "number" ? value : Number(value ?? 0);
  if (!Number.isFinite(n) || n <= 0) return "$0.00";
  if (n < 0.01) return `$${n.toFixed(6)}`;
  return `$${n.toFixed(2)}`;
}

function formatPercent(value: unknown): string {
  if (typeof value !== "number" || !Number.isFinite(value)) return "0%";
  return `${Math.round(value * 1000) / 10}%`;
}

function formatDuration(value: unknown): string {
  const ms = typeof value === "number" ? value : Number(value ?? 0);
  if (!Number.isFinite(ms) || ms <= 0) return "0 ms";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${Math.round(seconds * 10) / 10} 秒`;
  const minutes = seconds / 60;
  return `${Math.round(minutes * 10) / 10} 分钟`;
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
