"use client";

import { useCallback, useEffect, useMemo, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, BookOpenText, CheckCircle, Download, Loader2, Play, Plus, RefreshCw, Save, Search, Trash2, XCircle } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueExecutionTreeOptions } from "@multica/core/issues/queries";
import { useWorkspacePaths } from "@multica/core/paths";
import { projectListOptions, projectResourcesOptions } from "@multica/core/projects";
import { renderPromptTemplate } from "@multica/core/prompt-library";
import {
  TRAINING_WORKBENCH_VIEW_BY_TAB,
  buildSkillScenarioAssetRequest,
  buildWritingModelBenchmarkAssetRequest,
  isSkillScenarioPayload,
  summarizeSkillScenarioTarget,
  summarizeWritingModelBenchmark,
  trainingWorkbenchShowsPromptEditor,
  trainingWorkbenchTabFromView,
  trainingWorkbenchTitleFromView,
  type TrainingWorkbenchTab,
  type TrainingWorkbenchViewId,
} from "@multica/core/training";
import type {
  CreatePromptLibraryItemRequest,
  CreatePromptEvaluationAssetRequest,
  CreatePromptEvaluationCaseRequest,
  UpdatePromptEvaluationCaseRequest,
  PromptEvaluationAsset,
  PromptEvaluationEvidenceSnapshot,
  PromptEvaluationOptimizationCandidate,
  PromptEvaluationStructuredCase,
  PromptEvaluationCaseOperation,
  PromptEvaluationCaseTagSummary,
  PromptEvaluationCaseTagDatasetSummary,
  PromptEvaluationCaseSortBy,
  PromptEvaluationRun,
  PromptEvaluationRunEvidence,
  PromptEvaluationAssetEvidenceArchivePackage,
  PromptEvaluationAssetType,
  PromptEvaluationDatasetVersionDiff,
  PromptEvaluationDatasetVersionRow,
  PromptEvaluationDatasetVersionTagTrend,
  IssueExecutionTreeResponse,
  Project,
  ProjectResource,
  PromptLibraryItem,
  PromptLibraryStatus,
  PromptLibraryVersion,
  UpdatePromptEvaluationAssetRequest,
  UpdatePromptEvaluationOptimizationCandidateRequest,
  UpdatePromptLibraryItemRequest,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Badge } from "@multica/ui/components/ui/badge";
import { PageHeader } from "../../layout/page-header";
import { AppLink } from "../../navigation";
import { useNavigation } from "../../navigation";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import {
  buildAssetPayload,
  draftToRequest,
  emptyDraft,
  itemToDraft,
  parseDebugValues,
  parseVariables,
  requestToDraft,
  setDraftField,
  splitList,
  valuesToDebugText,
  type PromptDraft,
} from "./prompt-library-request-builders";
import { trainingSelectedPromptStorageKey } from "./prompt-selection-storage";

const promptLibraryKeys = {
  list: (workspaceId: string) => ["prompt-library", workspaceId, "list"] as const,
  versions: (workspaceId: string, promptId: string | null) => ["prompt-library", workspaceId, "versions", promptId ?? ""] as const,
  assets: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-assets"] as const,
  datasetVersions: (workspaceId: string, assetId: string) => ["prompt-library", workspaceId, "evaluation-dataset-versions", assetId] as const,
  datasetVersionRows: (workspaceId: string, assetId: string, versionId: string | null) => ["prompt-library", workspaceId, "evaluation-dataset-version-rows", assetId, versionId ?? ""] as const,
  datasetVersionTagTrends: (workspaceId: string, assetId: string) => ["prompt-library", workspaceId, "evaluation-dataset-version-tag-trends", assetId] as const,
  cases: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-cases"] as const,
  caseTagDatasetSummaries: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-case-tag-dataset-summaries"] as const,
  runs: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-runs"] as const,
  runEvidence: (workspaceId: string, runId: string | null) => ["prompt-library", workspaceId, "run-evidence", runId ?? ""] as const,
  runEvidenceSnapshots: (workspaceId: string, runId: string | null) => ["prompt-library", workspaceId, "run-evidence-snapshots", runId ?? ""] as const,
  candidates: (workspaceId: string) => ["prompt-library", workspaceId, "optimization-candidates"] as const,
};

const PROMPT_TYPES = ["全部", "需求澄清", "系统提示词", "评测提示词", "小队 SOP", "通用"];
type WorkbenchTab = TrainingWorkbenchTab;

function isEvaluationRunRecordsTab(tab: WorkbenchTab): boolean {
  return tab === "评测记录";
}

type RunStatusFilter = "全部" | PromptEvaluationRun["status"];
type EvidenceFocus = {
  traceSeq: string | null;
  toolChainId: string | null;
  trialAnchor: string | null;
  assertionAnchor: string | null;
  messageSeq: string | null;
  spanAnchor: string | null;
  failureAnchor: string | null;
};

const RUN_STATUS_FILTERS: RunStatusFilter[] = ["全部", "已入队", "运行中", "通过", "未通过", "失败", "已取消", "需人工复核"];
const USER_CENTER_TEMPLATE: CreatePromptLibraryItemRequest = {
  name: "用户中心需求澄清提示词",
  description: "用户中心小队队长使用",
  prompt_type: "需求澄清",
  content: "请围绕 {{任务标题}} 先澄清目标、边界、验收条件、风险、影响范围和可观测指标。项目背景：{{项目背景}}。输出必须使用中文，并列出需要团队确认的问题。",
  variables: [
    { name: "任务标题", label: "任务标题", required: true },
    { name: "项目背景", label: "项目背景" },
  ],
  tags: ["用户中心", "小队", "需求澄清"],
  status: "启用",
};

function trainingViewFromLocation(pathname: string, searchParams: URLSearchParams) {
  const match = pathname.match(/\/training\/([^/?#]+)/);
  return match?.[1] ? decodeURIComponent(match[1]) : searchParams.get("view");
}

function collectIssueExecutionTaskIds(tree: IssueExecutionTreeResponse | undefined): string[] {
  if (!tree?.root) return [];
  const ids = new Set<string>();
  const visit = (node: IssueExecutionTreeResponse["root"]) => {
    for (const task of node.tasks ?? []) {
      if (task.id) ids.add(task.id);
    }
    for (const child of node.children ?? []) visit(child as IssueExecutionTreeResponse["root"]);
  };
  visit(tree.root);
  return [...ids];
}

export function PromptLibraryPage({
  activeView,
  showPromptEditor,
}: {
  activeView?: TrainingWorkbenchViewId;
  showPromptEditor?: boolean;
}) {
  const workspaceId = useWorkspaceId();
  const workspacePaths = useWorkspacePaths();
  const navigation = useNavigation();
  const queryClient = useQueryClient();
  const [query, setQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState("全部");
  const [statusFilter, setStatusFilter] = useState<"全部" | PromptLibraryStatus>("全部");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [isDraftingNew, setIsDraftingNew] = useState(false);
  const [draft, setDraft] = useState<PromptDraft>(emptyDraft);
  const [caseDrafts, setCaseDrafts] = useState<Record<string, ManualCaseDraft>>({});
  const caseDraftStorageKey = workspaceId ? `multica:training:case-drafts:${workspaceId}` : null;
  const viewParam = trainingViewFromLocation(navigation.pathname, navigation.searchParams);
  const resolvedView = activeView ?? viewParam;
  const promptIdParam = navigation.searchParams.get("prompt_id");
  const focusedRunId = navigation.searchParams.get("run");
  const focusedIssueId = navigation.searchParams.get("issue");
  const focusedCaseId = navigation.searchParams.get("case");
  const focusedIssueRunReviewHref = focusedIssueId
    ? `${workspacePaths.runReviews()}?issue=${encodeURIComponent(focusedIssueId)}`
    : null;
  const evidenceFocus: EvidenceFocus = {
    traceSeq: navigation.searchParams.get("trace"),
    toolChainId: navigation.searchParams.get("tool"),
    trialAnchor: navigation.searchParams.get("trial"),
    assertionAnchor: navigation.searchParams.get("assertion"),
    messageSeq: navigation.searchParams.get("message"),
    spanAnchor: navigation.searchParams.get("span"),
    failureAnchor: navigation.searchParams.get("failure"),
  };
  const [activeTab, setActiveTab] = useState<WorkbenchTab>(() => trainingWorkbenchTabFromView(resolvedView));
  const [runStatusFilter, setRunStatusFilter] = useState<RunStatusFilter>("全部");
  const [exportingAssetEvidencePackageAssetId, setExportingAssetEvidencePackageAssetId] = useState<string | null>(null);
  const shouldShowPromptEditor = showPromptEditor ?? trainingWorkbenchShowsPromptEditor(resolvedView);

  useEffect(() => {
    setActiveTab(trainingWorkbenchTabFromView(resolvedView));
  }, [resolvedView]);

  useEffect(() => {
    if (!focusedRunId) return;
    setRunStatusFilter("全部");
  }, [focusedRunId]);

  useEffect(() => {
    document.title = trainingWorkbenchTitleFromView(resolvedView);
  }, [resolvedView]);

  useEffect(() => {
    if (!caseDraftStorageKey) return;
    try {
      const stored = window.sessionStorage.getItem(caseDraftStorageKey);
      if (!stored) return;
      const parsed = JSON.parse(stored);
      if (parsed && typeof parsed === "object") {
        setCaseDrafts(parsed as Record<string, ManualCaseDraft>);
      }
    } catch {
      // 草稿只用于输入体验，恢复失败时继续使用空草稿。
    }
  }, [caseDraftStorageKey]);

  useEffect(() => {
    if (!caseDraftStorageKey) return;
    try {
      window.sessionStorage.setItem(caseDraftStorageKey, JSON.stringify(caseDrafts));
    } catch {
      // 忽略受限浏览器环境下的 sessionStorage 写入失败。
    }
  }, [caseDrafts, caseDraftStorageKey]);

  const activeViewId = TRAINING_WORKBENCH_VIEW_BY_TAB[activeTab];
  const isEvaluationRunRecords = isEvaluationRunRecordsTab(activeTab);
  const effectiveRunStatusFilter = isEvaluationRunRecords ? runStatusFilter : "全部";
  const shouldShowPromptHeaderActions = activeTab === "提示词库";
  const isEvaluationAssetTab =
    activeTab === "数据集" ||
    activeTab === "测试套件";
  const needsPromptItems =
    (shouldShowPromptEditor && activeTab === "提示词库") ||
    isEvaluationAssetTab;
  const needsPromptVersions = shouldShowPromptEditor && activeTab === "提示词库";
  const needsEvaluationAssets = isEvaluationAssetTab;
  const needsStructuredCases =
    activeTab === "数据集" ||
    activeTab === "测试套件" ||
    isEvaluationRunRecords;
  const needsRuns = isEvaluationRunRecords;
  const needsCandidates = isEvaluationRunRecords;
  const needsSkillResources = isEvaluationRunRecords;

  const listQuery = useQuery({
    queryKey: promptLibraryKeys.list(workspaceId ?? ""),
    queryFn: () => api.listPromptLibraryItems(),
    enabled: !!workspaceId && needsPromptItems,
  });

  const assetQuery = useQuery({
    queryKey: promptLibraryKeys.assets(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationAssets(),
    enabled: !!workspaceId && needsEvaluationAssets,
  });
  const caseQuery = useQuery({
    queryKey: promptLibraryKeys.cases(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationCases(),
    enabled: !!workspaceId && needsStructuredCases,
  });
  useEffect(() => {
    if (!focusedCaseId || activeTab !== "数据集") return;
    const timer = window.setTimeout(() => {
      document.querySelector(`[data-testid="prompt-evaluation-case-${cssEscape(focusedCaseId)}"]`)?.scrollIntoView({
        block: "center",
      });
    }, 0);
    return () => window.clearTimeout(timer);
  }, [activeTab, focusedCaseId, caseQuery.data?.items.length]);
  const runQuery = useQuery({
    queryKey: [...promptLibraryKeys.runs(workspaceId ?? ""), effectiveRunStatusFilter] as const,
    queryFn: () => api.listPromptEvaluationRuns({
      limit: 100,
      status: effectiveRunStatusFilter === "全部" ? undefined : effectiveRunStatusFilter,
    }),
    enabled: !!workspaceId && needsRuns,
  });
  const candidateQuery = useQuery({
    queryKey: promptLibraryKeys.candidates(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationOptimizationCandidates({ limit: 100 }),
    enabled: !!workspaceId && needsCandidates,
  });
  const focusedIssueTreeQuery = useQuery({
    ...issueExecutionTreeOptions(focusedIssueId ?? ""),
    enabled: !!workspaceId && !!focusedIssueId && activeTab === "数据集",
  });
  const focusedIssueTaskIds = useMemo(
    () => collectIssueExecutionTaskIds(focusedIssueTreeQuery.data),
    [focusedIssueTreeQuery.data],
  );
  const projectQuery = useQuery({
    ...projectListOptions(workspaceId ?? ""),
    enabled: !!workspaceId && needsSkillResources,
  });
  const projectResourceQueries = useQueries({
    queries: (projectQuery.data ?? [])
      .filter((project) => project.resource_count > 0)
      .map((project) => ({
        ...projectResourcesOptions(workspaceId ?? "", project.id),
        enabled: !!workspaceId && needsSkillResources,
      })),
  });
  const items = listQuery.data?.items ?? [];
  const assets = assetQuery.data?.items ?? [];
  const cases = caseQuery.data?.items ?? [];
  const runs = runQuery.data?.items ?? [];
  const candidates = candidateQuery.data?.items ?? [];
  const skillResourceOptions = useMemo(
    () => buildSkillResourceOptions(projectQuery.data ?? [], projectResourceQueries.map((query) => query.data ?? [])),
    [projectQuery.data, projectResourceQueries],
  );
  const visiblePromptItems = items;
  const selectedFromList = selectedId ? items.find((item) => item.id === selectedId) ?? null : null;
  const selected = selectedFromList ?? (isDraftingNew ? null : visiblePromptItems[0] ?? null);
  const versionQuery = useQuery({
    queryKey: promptLibraryKeys.versions(workspaceId ?? "", selectedFromList?.id ?? null),
    queryFn: () => api.listPromptLibraryVersions(selectedFromList?.id ?? ""),
    enabled: !!workspaceId && needsPromptVersions && !!selectedFromList,
  });
  const promptVersions = versionQuery.data?.items ?? [];
  const selectedPromptStorageKey = trainingSelectedPromptStorageKey(workspaceId);
  const [debugValuesText, setDebugValuesText] = useState("");
  const debugResult = useMemo(
    () => renderPromptTemplate({
      content: draft.content,
      variables: parseVariables(draft.variablesText),
      values: parseDebugValues(debugValuesText),
    }),
    [debugValuesText, draft.content, draft.variablesText],
  );

  useEffect(() => {
    if (!selectedPromptStorageKey || isDraftingNew) return;
    try {
      const storedId = window.localStorage.getItem(selectedPromptStorageKey);
      if (storedId && storedId !== selectedId && items.some((item) => item.id === storedId)) {
        setSelectedId(storedId);
      }
    } catch {
      // localStorage is best-effort; route usability must not depend on it.
    }
  }, [isDraftingNew, items, selectedId, selectedPromptStorageKey]);

  useEffect(() => {
    if (!selectedPromptStorageKey || !selectedId) return;
    try {
      window.localStorage.setItem(selectedPromptStorageKey, selectedId);
    } catch {
      // Ignore storage failures in private or restricted browser contexts.
    }
  }, [selectedId, selectedPromptStorageKey]);

  useEffect(() => {
    if (isDraftingNew) return;
    if (!selectedId && visiblePromptItems.length > 0) {
      setSelectedId(visiblePromptItems[0]?.id ?? null);
    }
    if (selectedId && !selectedFromList && visiblePromptItems.length > 0 && !listQuery.isFetching) {
      setSelectedId(visiblePromptItems[0]?.id ?? null);
    }
  }, [isDraftingNew, listQuery.isFetching, selectedFromList, selectedId, visiblePromptItems]);

  const filteredItems = useMemo(() => {
    const q = query.trim().toLowerCase();
    return visiblePromptItems.filter((item) => {
      if (typeFilter !== "全部" && item.prompt_type !== typeFilter) return false;
      if (statusFilter !== "全部" && item.status !== statusFilter) return false;
      if (!q) return true;
      const haystack = [item.name, item.description, item.prompt_type, item.content, ...item.tags].join(" ");
      return haystack.toLowerCase().includes(q) || matchesPinyin(haystack, q);
    });
  }, [query, statusFilter, typeFilter, visiblePromptItems]);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.list(workspaceId ?? "") });
  const invalidateVersions = (promptId: string | null) => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.versions(workspaceId ?? "", promptId) });
  const invalidateAssets = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.assets(workspaceId ?? "") });
  const invalidateCases = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.cases(workspaceId ?? "") });
  const invalidateRuns = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runs(workspaceId ?? "") });
  const invalidateCandidates = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.candidates(workspaceId ?? "") });
  const invalidateRunEvidenceSnapshots = (runId: string) => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidenceSnapshots(workspaceId ?? "", runId) });
  const rememberSelectedPrompt = (promptId: string | null) => {
    setSelectedId(promptId);
    if (!selectedPromptStorageKey) return;
    try {
      if (promptId) {
        window.localStorage.setItem(selectedPromptStorageKey, promptId);
      } else {
        window.localStorage.removeItem(selectedPromptStorageKey);
      }
    } catch {
      // localStorage persistence is best-effort; in-memory selection is still updated.
    }
  };

  useEffect(() => {
    if (!promptIdParam || promptIdParam === selectedId || !items.some((item) => item.id === promptIdParam)) return;
    setIsDraftingNew(false);
    rememberSelectedPrompt(promptIdParam);
  }, [items, promptIdParam, selectedId]);

  const createMut = useMutation({
    mutationFn: (data: CreatePromptLibraryItemRequest) => api.createPromptLibraryItem(data),
    onSuccess: (item) => {
      invalidate();
      invalidateVersions(item.id);
      setIsDraftingNew(false);
      rememberSelectedPrompt(item.id);
      toast.success("提示词已创建");
    },
  });

  const updateMut = useMutation({
    mutationFn: ({ id, data }: { id: string; data: UpdatePromptLibraryItemRequest }) => api.updatePromptLibraryItem(id, data),
    onSuccess: (item) => {
      invalidate();
      invalidateVersions(item.id);
      setIsDraftingNew(false);
      rememberSelectedPrompt(item.id);
      toast.success("提示词已保存");
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.deletePromptLibraryItem(id),
    onSuccess: () => {
      invalidate();
      rememberSelectedPrompt(null);
      setDraft(emptyDraft());
      toast.success("提示词已删除");
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

  const importDatasetFromTracesMut = useMutation({
    mutationFn: (assetId: string) =>
      api.createPromptEvaluationDatasetFromTraces(assetId, {
        limit: focusedIssueTaskIds.length > 0 ? focusedIssueTaskIds.length : 5,
        ...(focusedIssueTaskIds.length > 0 ? { task_ids: focusedIssueTaskIds } : {}),
        expected_contains: ["任务", "trace"],
        tags: focusedIssueId
          ? ["trace导入", "真实执行记录", `issue:${focusedIssueId}`]
          : ["trace导入", "真实执行记录"],
      }),
    onSuccess: (result) => {
      invalidateAssets();
      invalidateCases();
      toast.success(`已从 trace 导入 ${result.created_count} 条数据集样本`);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "trace 导入失败，请先产生真实任务记录");
    },
  });

  const exportDatasetProtocolMut = useMutation({
    mutationFn: (assetId: string) => api.exportPromptEvaluationDataset(assetId),
    onSuccess: (result) => {
      downloadTextFile(
        JSON.stringify(result, null, 2),
        `multica-dataset-export-${result.asset.id}-${new Date().toISOString().replace(/[:.]/g, "-")}.json`,
        "application/json;charset=utf-8",
      );
      toast.success(`数据集完整协议已导出：${result.case_count} 条`);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "数据集完整协议导出失败");
    },
  });

  const importDatasetCopyMut = useMutation({
    mutationFn: async (asset: PromptEvaluationAsset) => {
      const exported = await api.exportPromptEvaluationDataset(asset.id);
      return api.importPromptEvaluationDataset({
        name: `${asset.name} 导入副本 ${new Date().toISOString().slice(0, 19).replace("T", " ")}`,
        description: `通过完整数据集协议从「${asset.name}」导入的副本。`,
        prompt_id: asset.prompt_id ?? null,
        status: asset.status,
        export: exported,
      });
    },
    onSuccess: (result) => {
      invalidateAssets();
      invalidateCases();
      toast.success(`数据集副本已导入：${result.case_count} 条`);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "数据集副本导入失败");
    },
  });

  const createDatasetVersionMut = useMutation({
    mutationFn: (assetId: string) => api.createPromptEvaluationDatasetVersion(assetId, {
      version_label: "手动快照",
      metadata: {
        来源: "训练与评估页面",
        用途: "固定当前数据集样本，供后续评估运行和实验对比复盘",
        创建时间: new Date().toISOString(),
      },
    }),
    onSuccess: (version, assetId) => {
      invalidateAssets();
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.datasetVersions(workspaceId ?? "", assetId) });
      toast.success(`数据集版本 v${version.version} 已生成`);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "数据集版本生成失败，请先补充启用样本行");
    },
  });

  const createCaseMut = useMutation({
    mutationFn: (data: CreatePromptEvaluationCaseRequest) => api.createPromptEvaluationCase(data),
    onSuccess: () => {
      invalidateCases();
      toast.success("手工评测用例已创建");
    },
  });

  const updateCaseMut = useMutation({
    mutationFn: ({ caseId, data }: { caseId: string; data: UpdatePromptEvaluationCaseRequest }) => api.updatePromptEvaluationCase(caseId, data),
    onSuccess: () => {
      invalidateCases();
      toast.success("手工评测用例已保存");
    },
  });

  const deleteCaseMut = useMutation({
    mutationFn: (id: string) => api.deletePromptEvaluationCase(id),
    onSuccess: () => {
      invalidateCases();
      toast.success("手工评测用例已删除");
    },
  });

  const syncRunMut = useMutation({
    mutationFn: (runId: string) => api.syncPromptEvaluationRun(runId),
    onSuccess: (_run, runId) => {
      invalidateRuns();
      invalidateCandidates();
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidence(workspaceId ?? "", runId) });
      invalidateRunEvidenceSnapshots(runId);
      toast.success("运行记录已同步");
    },
  });

  const cancelRunMut = useMutation({
    mutationFn: (runId: string) => api.cancelPromptEvaluationRun(runId),
    onSuccess: (run) => {
      invalidateRuns();
      invalidateCandidates();
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidence(workspaceId ?? "", run.id) });
      invalidateRunEvidenceSnapshots(run.id);
      toast.success("训练评估运行已取消");
    },
  });

  const reviewRunMut = useMutation({
    mutationFn: ({ runId, decision, note }: { runId: string; decision: "通过" | "未通过"; note: string }) =>
      api.reviewPromptEvaluationRun(runId, { decision, note }),
    onSuccess: (run) => {
      invalidateRuns();
      invalidateCandidates();
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidence(workspaceId ?? "", run.id) });
      invalidateRunEvidenceSnapshots(run.id);
      toast.success(`人工复核已处理：${run.review_decision || run.status}`);
    },
  });

  const createEvidenceSnapshotMut = useMutation({
    mutationFn: (runId: string) => api.createPromptEvaluationEvidenceSnapshot(runId, "验收归档"),
    onSuccess: (snapshot) => {
      invalidateRunEvidenceSnapshots(snapshot.run_id);
      toast.success("服务端证据快照已归档");
    },
  });

  const createAssetEvidenceSnapshotsMut = useMutation({
    mutationFn: (assetId: string) => api.createPromptEvaluationAssetEvidenceSnapshots(assetId, "验收归档", 20),
    onSuccess: (result) => {
      invalidateRuns();
      for (const snapshot of result.items) {
        invalidateRunEvidenceSnapshots(snapshot.run_id);
      }
      const skippedText = result.skipped_count > 0 ? `，跳过 ${result.skipped_count} 条已归档` : "";
      toast.success(`已归档 ${result.created_count} 条运行证据${skippedText}`);
    },
  });

  const handleDownloadAssetEvidencePackage = async (assetId: string) => {
    setExportingAssetEvidencePackageAssetId(assetId);
    try {
      const archivePackage: PromptEvaluationAssetEvidenceArchivePackage = await api.getPromptEvaluationAssetEvidenceArchivePackage(assetId, "验收归档", 20);
      const filename = `multica-training-asset-evidence-${assetId}-${new Date().toISOString().replace(/[:.]/g, "-")}.json`;
      downloadTextFile(JSON.stringify(archivePackage, null, 2), filename, "application/json;charset=utf-8");
      if (archivePackage.archived_run_count > 0) {
        toast.success(`资产归档包已导出：${archivePackage.archived_run_count} 条运行证据`);
      } else {
        toast.info("资产归档包已导出，但该资产还没有服务端证据快照");
      }
    } finally {
      setExportingAssetEvidencePackageAssetId(null);
    }
  };

  const createCandidateMut = useMutation({
    mutationFn: (runId: string) => api.createPromptEvaluationOptimizationCandidate(runId),
    onSuccess: () => {
      invalidateCandidates();
      toast.success("优化候选已生成，等待人工确认");
    },
  });

  const publishCandidateMut = useMutation({
    mutationFn: (candidateId: string) => api.publishPromptEvaluationOptimizationCandidate(candidateId),
    onSuccess: (result) => {
      invalidate();
      invalidateVersions(result.prompt.id);
      invalidateCandidates();
      rememberSelectedPrompt(result.prompt.id);
      toast.success(`已发布新提示词版本：${result.prompt.name}`);
    },
  });

  const updateCandidateMut = useMutation({
    mutationFn: ({ candidateId, data }: { candidateId: string; data: UpdatePromptEvaluationOptimizationCandidateRequest }) =>
      api.updatePromptEvaluationOptimizationCandidate(candidateId, data),
    onSuccess: (candidate) => {
      invalidateCandidates();
      toast.success(`优化候选已保存：${candidate.candidate_name}`);
    },
  });

  const rejectCandidateMut = useMutation({
    mutationFn: ({ candidateId, reason }: { candidateId: string; reason: string }) =>
      api.rejectPromptEvaluationOptimizationCandidate(candidateId, reason),
    onSuccess: (candidate) => {
      invalidateCandidates();
      toast.success(`已暂不采纳优化候选：${candidate.candidate_name}`);
    },
  });

  const saving = createMut.isPending || updateMut.isPending;
  const deleting = deleteMut.isPending;

  const createAssetMut = useMutation({
    mutationFn: (data: CreatePromptEvaluationAssetRequest) => api.createPromptEvaluationAsset(data),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      toast.success("资产已创建");
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
      toast.success("本地渲染检查已记录");
    },
  });

  const createWorkbenchAsset = (assetType: PromptEvaluationAssetType) => {
    const prompt = selected ?? visiblePromptItems[0] ?? null;
    if (!prompt) {
      toast.error("请先保存提示词");
      return;
    }
    createAssetMut.mutate({
      prompt_id: prompt.id,
      name: `${prompt.name} ${assetType} ${new Date().toLocaleString("zh-CN")}`,
      description: `从训练工作台创建的${assetType}`,
      asset_type: assetType,
      payload: buildAssetPayload(assetType, prompt, parseDebugValues(debugValuesText), debugResult.rendered),
      status: "启用",
    });
  };

  const createSkillScenarioAsset = (assetType: Extract<PromptEvaluationAssetType, "数据集" | "测试套件">) => {
    createAssetMut.mutate(buildSkillScenarioAssetRequest(assetType));
  };

  const createWritingBenchmarkAsset = () => {
    createAssetMut.mutate(buildWritingModelBenchmarkAssetRequest());
  };

  const runDebug = () => {
    if (!selected) {
      toast.error("请先保存提示词");
      return;
    }
    const values = parseDebugValues(debugValuesText);
    runDebugMut.mutate({
      prompt_id: selected.id,
      name: `${selected.name} 渲染检查 ${new Date().toLocaleString("zh-CN")}`,
      description: "从提示词库保存的本地渲染检查",
      asset_type: "测试套件",
      payload: {
        cases: [{
          名称: "本地渲染检查",
          变量: values,
          期望包含: debugResult.missingVariables.length === 0 ? debugResult.usedVariables.map((key) => values[key]).filter(Boolean) : [],
        }],
        调试输出: debugResult.rendered,
      },
      status: "启用",
    });
  };

  const creatingAsset = createAssetMut.isPending;
  const runningDebug = runDebugMut.isPending;
  const savingAsset = creatingAsset || updateAssetMut.isPending || deleteAssetMut.isPending || importDatasetFromTracesMut.isPending || exportDatasetProtocolMut.isPending || importDatasetCopyMut.isPending || createDatasetVersionMut.isPending;

  useEffect(() => {
    if (!selected) return;
    setDraft(itemToDraft(selected));
    const nextValues = valuesToDebugText(selected.variables);
    setDebugValuesText(nextValues);
  }, [selected, setDebugValuesText]);

  const startNew = () => {
    setIsDraftingNew(true);
    rememberSelectedPrompt(null);
    setDraft(emptyDraft());
    setDebugValuesText("");
  };

  const applyUserCenterTemplate = () => {
    setIsDraftingNew(true);
    rememberSelectedPrompt(null);
    setDraft(requestToDraft(USER_CENTER_TEMPLATE));
    const nextValues = valuesToDebugText(USER_CENTER_TEMPLATE.variables ?? []);
    setDebugValuesText(nextValues);
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

  const importDatasetFromTraces = (asset: PromptEvaluationAsset) => {
    importDatasetFromTracesMut.mutate(asset.id);
  };

  const reviewRun = (run: PromptEvaluationRun, decision: "通过" | "未通过") => {
    const defaultNote = decision === "通过" ? "人工复核确认通过" : "人工复核驳回";
    const note = window.prompt(`请输入${decision === "通过" ? "通过" : "驳回"}说明`, defaultNote);
    if (note === null) return;
    reviewRunMut.mutate({ runId: run.id, decision, note: note.trim() || defaultNote });
  };

  const workbenchPanel = (
    <WorkbenchPanel
      activeTab={activeTab}
      workspaceId={workspaceId ?? ""}
      assets={assets}
      cases={cases}
      runs={runs}
      focusedRunId={focusedRunId}
      evidenceFocus={evidenceFocus}
      runStatusFilter={runStatusFilter}
      focusedIssueId={focusedIssueId}
      focusedCaseId={focusedCaseId}
      focusedIssueRunReviewHref={focusedIssueRunReviewHref}
      focusedIssueTaskIds={focusedIssueTaskIds}
      onRunStatusFilterChange={setRunStatusFilter}
      candidates={candidates}
      skillResources={skillResourceOptions}
      loading={assetQuery.isLoading || caseQuery.isLoading || runQuery.isLoading || candidateQuery.isLoading}
      saving={savingAsset}
      onCreateAsset={createWorkbenchAsset}
      onCreateSkillScenarioAsset={createSkillScenarioAsset}
      onCreateWritingBenchmarkAsset={createWritingBenchmarkAsset}
      onToggleAssetStatus={toggleAssetStatus}
      onUpdateAsset={(assetId, data) => updateAssetMut.mutateAsync({ id: assetId, data })}
      onDeleteAsset={deleteAsset}
      onImportDatasetFromTraces={importDatasetFromTraces}
      importingTraceDatasetAssetId={importDatasetFromTracesMut.isPending ? importDatasetFromTracesMut.variables ?? null : null}
      onExportDatasetProtocol={(asset) => exportDatasetProtocolMut.mutate(asset.id)}
      exportingDatasetProtocolAssetId={exportDatasetProtocolMut.isPending ? exportDatasetProtocolMut.variables ?? null : null}
      onImportDatasetCopy={(asset) => importDatasetCopyMut.mutate(asset)}
      importingDatasetCopyAssetId={importDatasetCopyMut.isPending ? importDatasetCopyMut.variables?.id ?? null : null}
      onCreateDatasetVersion={(asset) => createDatasetVersionMut.mutate(asset.id)}
      creatingDatasetVersionAssetId={createDatasetVersionMut.isPending ? createDatasetVersionMut.variables ?? null : null}
      onCreateCase={(data) => createCaseMut.mutate(data)}
      creatingCaseAssetId={createCaseMut.isPending ? createCaseMut.variables?.asset_id ?? null : null}
      caseDrafts={caseDrafts}
      onCaseDraftsChange={setCaseDrafts}
      onUpdateCase={(caseId, data) => updateCaseMut.mutateAsync({ caseId, data })}
      updatingCaseId={updateCaseMut.isPending ? updateCaseMut.variables?.caseId ?? null : null}
      onDeleteCase={(caseId) => deleteCaseMut.mutate(caseId)}
      deletingCaseId={deleteCaseMut.isPending ? deleteCaseMut.variables ?? null : null}
      onSyncRun={(runId) => syncRunMut.mutate(runId)}
      syncingRunId={syncRunMut.isPending ? syncRunMut.variables ?? null : null}
      onCancelRun={(runId) => cancelRunMut.mutate(runId)}
      cancellingRunId={cancelRunMut.isPending ? cancelRunMut.variables ?? null : null}
      onReviewRun={reviewRun}
      reviewingRunId={reviewRunMut.isPending ? reviewRunMut.variables?.runId ?? null : null}
      onCreateEvidenceSnapshot={(runId) => createEvidenceSnapshotMut.mutate(runId)}
      creatingEvidenceSnapshotRunId={createEvidenceSnapshotMut.isPending ? createEvidenceSnapshotMut.variables ?? null : null}
      onCreateAssetEvidenceSnapshots={(assetId) => createAssetEvidenceSnapshotsMut.mutate(assetId)}
      creatingAssetEvidenceSnapshotsAssetId={createAssetEvidenceSnapshotsMut.isPending ? createAssetEvidenceSnapshotsMut.variables ?? null : null}
      onDownloadAssetEvidencePackage={handleDownloadAssetEvidencePackage}
      exportingAssetEvidencePackageAssetId={exportingAssetEvidencePackageAssetId}
      onGenerateCandidate={(runId) => createCandidateMut.mutate(runId)}
      generatingCandidateRunId={createCandidateMut.isPending ? createCandidateMut.variables ?? null : null}
      onUpdateCandidate={(candidateId, data) => updateCandidateMut.mutate({ candidateId, data })}
      updatingCandidateId={updateCandidateMut.isPending ? updateCandidateMut.variables?.candidateId ?? null : null}
      onPublishCandidate={(candidateId) => publishCandidateMut.mutate(candidateId)}
      publishingCandidateId={publishCandidateMut.isPending ? publishCandidateMut.variables ?? null : null}
      onRejectCandidate={(candidateId, reason) => rejectCandidateMut.mutate({ candidateId, reason })}
      rejectingCandidateId={rejectCandidateMut.isPending ? rejectCandidateMut.variables?.candidateId ?? null : null}
    />
  );

  return (
    <div className="flex h-full min-h-0 flex-col bg-background" data-testid="training-page-shell" data-training-view={activeViewId}>
      <div className="sr-only" data-testid={`training-route-${activeViewId}`}>
        当前训练与评估子模块：{activeTab}
      </div>
      <PageHeader>
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <BookOpenText className="size-4 shrink-0 text-muted-foreground" />
          <h1 className="truncate text-sm font-semibold">训练与评估 / {activeTab}</h1>
          <span className="text-xs text-muted-foreground">{visiblePromptItems.length}</span>
        </div>
        {shouldShowPromptHeaderActions && (
          <div className="flex items-center gap-2">
            <Button size="sm" onClick={startNew}>
              <Plus className="size-3.5" />
              新建
            </Button>
          </div>
        )}
      </PageHeader>

      {shouldShowPromptEditor ? (
        <div className="flex min-h-0 flex-1 flex-col md:grid md:grid-cols-[360px_minmax(0,1fr)]" data-testid="prompt-library-editor">
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
                      onClick={() => {
                        setIsDraftingNew(false);
                        rememberSelectedPrompt(item.id);
                      }}
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
              {activeTab === "提示词库" && (
                <section className="rounded-md border bg-muted/20 p-3" data-testid="prompt-template-actions">
                  <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                    <div className="min-w-0">
                      <div className="text-sm font-medium">团队提示词模板</div>
                      <div className="mt-1 text-xs text-muted-foreground">
                        在提示词库中起草一份需求澄清模板，保存前可以继续调整名称、变量和内容。
                      </div>
                    </div>
                    <Button size="sm" variant="secondary" onClick={applyUserCenterTemplate}>
                      <BookOpenText className="size-3.5" />
                      起草需求澄清模板
                    </Button>
                  </div>
                </section>
              )}
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

              <PromptVersionHistory
                selected={selected}
                versions={promptVersions}
                loading={versionQuery.isLoading || versionQuery.isFetching}
              />

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
                    placeholder="任务标题=任务标题, 项目背景=项目背景"
                  />
                </Field>
                <Field label="标签">
                  <Input
                    value={draft.tagsText}
                    onChange={(event) => setDraftField(setDraft, "tagsText", event.target.value)}
                    placeholder="账号系统, 小队, 需求澄清"
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
                    placeholder="任务标题=登录失败&#10;项目背景=账号系统"
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
                      保存本地渲染检查
                    </Button>
                  </div>
                  <pre className="min-h-[180px] overflow-auto whitespace-pre-wrap rounded-md border bg-muted/20 p-3 font-mono text-sm leading-6">
                    {debugResult.rendered || "暂无输出"}
                  </pre>
                </div>
              </section>

              {workbenchPanel}
            </div>
          </main>
        </div>
      ) : (
        <main className="min-h-0 flex-1 overflow-y-auto p-4 md:p-6">
          <div className="mx-auto flex max-w-5xl flex-col gap-4">
            {workbenchPanel}
          </div>
        </main>
      )}
    </div>
  );
}

function PromptVersionHistory({
  selected,
  versions,
  loading,
}: {
  selected: PromptLibraryItem | null;
  versions: PromptLibraryVersion[];
  loading: boolean;
}) {
  if (!selected) {
    return (
      <section className="rounded-md border border-dashed bg-muted/10 px-3 py-3 text-sm text-muted-foreground">
        保存后会生成第一个不可变版本记录。
      </section>
    );
  }
  const latest = versions[0] ?? null;
  return (
    <section className="rounded-md border bg-muted/10 p-3" data-testid="prompt-version-history">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <h3 className="text-sm font-semibold">版本历史</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            {loading ? "正在读取版本链" : `${versions.length} 个版本记录 · 当前版本 ${selected.version}`}
          </p>
        </div>
        <Badge variant="outline" className="w-fit shrink-0">
          {latest ? latest.source : "暂无版本"}
        </Badge>
      </div>
      {versions.length === 0 ? (
        <div className="mt-3 rounded-md border border-dashed bg-background px-3 py-3 text-sm text-muted-foreground">
          暂无版本历史；旧数据会在迁移中回填为“历史回填”。
        </div>
      ) : (
        <div className="mt-3 grid gap-2">
          {versions.slice(0, 4).map((item) => (
            <div key={item.id} className="grid gap-1 rounded-md border bg-background px-3 py-2 text-xs md:grid-cols-[minmax(0,1fr)_auto]">
              <div className="min-w-0">
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <span className="font-medium text-foreground">版本 {item.version}</span>
                  <Badge variant={item.source === "优化候选发布" ? "secondary" : "outline"}>{item.source}</Badge>
                  {item.source_candidate_id && <span className="text-muted-foreground">候选 {item.source_candidate_id}</span>}
                </div>
                <div className="mt-1 truncate text-muted-foreground">{item.content}</div>
              </div>
              <div className="text-muted-foreground md:text-right">{item.created_at || "未记录时间"}</div>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function WorkbenchPanel({
  activeTab,
  workspaceId,
  assets,
  cases,
  runs,
  focusedRunId,
  evidenceFocus,
  runStatusFilter,
  focusedIssueId,
  focusedCaseId,
  focusedIssueRunReviewHref,
  focusedIssueTaskIds,
  onRunStatusFilterChange,
  candidates,
  skillResources,
  loading,
  saving,
  onCreateAsset,
  onCreateSkillScenarioAsset,
  onCreateWritingBenchmarkAsset,
  onToggleAssetStatus,
  onUpdateAsset,
  onDeleteAsset,
  onImportDatasetFromTraces,
  importingTraceDatasetAssetId,
  onExportDatasetProtocol,
  exportingDatasetProtocolAssetId,
  onImportDatasetCopy,
  importingDatasetCopyAssetId,
  onCreateDatasetVersion,
  creatingDatasetVersionAssetId,
  onCreateCase,
  creatingCaseAssetId,
  caseDrafts,
  onCaseDraftsChange,
  onUpdateCase,
  updatingCaseId,
  onDeleteCase,
  deletingCaseId,
  onSyncRun,
  syncingRunId,
  onCancelRun,
  cancellingRunId,
  onReviewRun,
  reviewingRunId,
  onCreateEvidenceSnapshot,
  creatingEvidenceSnapshotRunId,
  onCreateAssetEvidenceSnapshots,
  creatingAssetEvidenceSnapshotsAssetId,
  onDownloadAssetEvidencePackage,
  exportingAssetEvidencePackageAssetId,
  onGenerateCandidate,
  generatingCandidateRunId,
  onUpdateCandidate,
  updatingCandidateId,
  onPublishCandidate,
  publishingCandidateId,
  onRejectCandidate,
  rejectingCandidateId,
}: {
  activeTab: WorkbenchTab;
  workspaceId: string;
  assets: PromptEvaluationAsset[];
  cases: PromptEvaluationStructuredCase[];
  runs: PromptEvaluationRun[];
  focusedRunId: string | null;
  evidenceFocus: EvidenceFocus;
  runStatusFilter: RunStatusFilter;
  focusedIssueId: string | null;
  focusedCaseId: string | null;
  focusedIssueRunReviewHref: string | null;
  focusedIssueTaskIds: string[];
  onRunStatusFilterChange: (status: RunStatusFilter) => void;
  candidates: PromptEvaluationOptimizationCandidate[];
  skillResources: SkillResourceOption[];
  loading: boolean;
  saving: boolean;
  onCreateAsset: (assetType: PromptEvaluationAssetType) => void;
  onCreateSkillScenarioAsset: (assetType: Extract<PromptEvaluationAssetType, "数据集" | "测试套件">) => void;
  onCreateWritingBenchmarkAsset: () => void;
  onToggleAssetStatus: (asset: PromptEvaluationAsset) => void;
  onUpdateAsset: (assetId: string, data: UpdatePromptEvaluationAssetRequest) => Promise<unknown>;
  onDeleteAsset: (asset: PromptEvaluationAsset) => void;
  onImportDatasetFromTraces: (asset: PromptEvaluationAsset) => void;
  importingTraceDatasetAssetId: string | null;
  onExportDatasetProtocol: (asset: PromptEvaluationAsset) => void;
  exportingDatasetProtocolAssetId: string | null;
  onImportDatasetCopy: (asset: PromptEvaluationAsset) => void;
  importingDatasetCopyAssetId: string | null;
  onCreateDatasetVersion: (asset: PromptEvaluationAsset) => void;
  creatingDatasetVersionAssetId: string | null;
  onCreateCase: (data: CreatePromptEvaluationCaseRequest) => void;
  creatingCaseAssetId: string | null;
  caseDrafts: Record<string, ManualCaseDraft>;
  onCaseDraftsChange: Dispatch<SetStateAction<Record<string, ManualCaseDraft>>>;
  onUpdateCase: (caseId: string, data: UpdatePromptEvaluationCaseRequest) => Promise<unknown>;
  updatingCaseId: string | null;
  onDeleteCase: (caseId: string) => void;
  deletingCaseId: string | null;
  onSyncRun: (runId: string) => void;
  syncingRunId: string | null;
  onCancelRun: (runId: string) => void;
  cancellingRunId: string | null;
  onReviewRun: (run: PromptEvaluationRun, decision: "通过" | "未通过") => void;
  reviewingRunId: string | null;
  onCreateEvidenceSnapshot: (runId: string) => void;
  creatingEvidenceSnapshotRunId: string | null;
  onCreateAssetEvidenceSnapshots: (assetId: string) => void;
  creatingAssetEvidenceSnapshotsAssetId: string | null;
  onDownloadAssetEvidencePackage: (assetId: string) => void;
  exportingAssetEvidencePackageAssetId: string | null;
  onGenerateCandidate: (runId: string) => void;
  generatingCandidateRunId: string | null;
  onUpdateCandidate: (candidateId: string, data: UpdatePromptEvaluationOptimizationCandidateRequest) => void;
  updatingCandidateId: string | null;
  onPublishCandidate: (candidateId: string) => void;
  publishingCandidateId: string | null;
  onRejectCandidate: (candidateId: string, reason: string) => void;
  rejectingCandidateId: string | null;
}) {
  const tabAssetType = tabToAssetType(activeTab);
  const tabAssets = tabAssetType ? assets.filter((asset) => asset.asset_type === tabAssetType) : assets;
  const visibleAssets = tabAssets;
  const visibleCandidates = candidates;
  void onUpdateCandidate;
  void updatingCandidateId;
  void onPublishCandidate;
  void publishingCandidateId;
  void onRejectCandidate;
  void rejectingCandidateId;

  if (activeTab === "提示词库") {
    return null;
  }

  const routeIntro = trainingRouteIntro(activeTab, {
    visibleAssets,
    cases,
    runs,
    candidates: visibleCandidates,
    runStatusFilter,
  });

  return (
    <section className="grid gap-3 border-t pt-4" data-testid={`training-route-workspace-${routeIntro.route}`}>
      <TrainingRouteIntroCard
        route={routeIntro.route}
        title={routeIntro.title}
        subtitle={routeIntro.subtitle}
        facts={routeIntro.facts}
        evidence={routeIntro.evidence}
        action={tabAssetType ? (
          <div className="flex flex-wrap gap-2">
            {(activeTab === "数据集" || activeTab === "测试套件") && (
              <Button
                size="sm"
                variant="secondary"
                data-testid={`create-skill-scenario-${routeIntro.route}`}
                onClick={() => onCreateSkillScenarioAsset(tabAssetType as Extract<PromptEvaluationAssetType, "数据集" | "测试套件">)}
                disabled={saving}
              >
                {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
                新建 Skill 场景
              </Button>
            )}
            <Button size="sm" onClick={() => onCreateAsset(tabAssetType)} disabled={saving}>
              {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
              新建{tabAssetType}
            </Button>
            {activeTab === "测试套件" && (
              <Button
                size="sm"
                variant="secondary"
                data-testid="create-writing-model-benchmark"
                onClick={onCreateWritingBenchmarkAsset}
                disabled={saving}
              >
                {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
                新建写作模型评测
              </Button>
            )}
          </div>
        ) : null}
      />

      {focusedIssueId && (
        <TrainingFocusedIssueCallout
          activeTab={activeTab}
          issueId={focusedIssueId}
          taskCount={focusedIssueTaskIds.length}
        />
      )}

      <TrainingRouteWorkspaceBand
        activeTab={activeTab}
        route={routeIntro.route}
        visibleAssets={visibleAssets}
        cases={cases}
        runs={runs}
        candidates={visibleCandidates}
        runStatusFilter={runStatusFilter}
      />
      {isEvaluationRunRecordsTab(activeTab) && (
        <RunHistoryPanel
          workspaceId={workspaceId}
          runs={runs}
          focusedRunId={focusedRunId}
          evidenceFocus={evidenceFocus}
          runStatusFilter={runStatusFilter}
          onRunStatusFilterChange={onRunStatusFilterChange}
          candidates={visibleCandidates}
          skillResources={skillResources}
          loading={loading}
          onSyncRun={onSyncRun}
          syncingRunId={syncingRunId}
          onCancelRun={onCancelRun}
          cancellingRunId={cancellingRunId}
          onReviewRun={onReviewRun}
          reviewingRunId={reviewingRunId}
          onCreateEvidenceSnapshot={onCreateEvidenceSnapshot}
          creatingEvidenceSnapshotRunId={creatingEvidenceSnapshotRunId}
          onGenerateCandidate={onGenerateCandidate}
          generatingCandidateRunId={generatingCandidateRunId}
        />
      )}

      {!isEvaluationRunRecordsTab(activeTab) && (
        <TrainingAssetPanel
          activeTab={activeTab}
          route={routeIntro.route}
          title={routeIntro.title}
          assets={visibleAssets}
          runs={runs}
          cases={cases}
          loading={loading}
          saving={saving}
          onToggleAssetStatus={onToggleAssetStatus}
          onUpdateAsset={onUpdateAsset}
          onDeleteAsset={onDeleteAsset}
          onImportDatasetFromTraces={onImportDatasetFromTraces}
          importingTraceDatasetAssetId={importingTraceDatasetAssetId}
          onExportDatasetProtocol={onExportDatasetProtocol}
          exportingDatasetProtocolAssetId={exportingDatasetProtocolAssetId}
          onImportDatasetCopy={onImportDatasetCopy}
          importingDatasetCopyAssetId={importingDatasetCopyAssetId}
          onCreateDatasetVersion={onCreateDatasetVersion}
          creatingDatasetVersionAssetId={creatingDatasetVersionAssetId}
          onCreateCase={onCreateCase}
          creatingCaseAssetId={creatingCaseAssetId}
          caseDrafts={caseDrafts}
          onCaseDraftsChange={onCaseDraftsChange}
          focusedCaseId={focusedCaseId}
          focusedIssueId={focusedIssueId}
          focusedIssueRunReviewHref={focusedIssueRunReviewHref}
          onUpdateCase={onUpdateCase}
          updatingCaseId={updatingCaseId}
          onDeleteCase={onDeleteCase}
          deletingCaseId={deletingCaseId}
          onCreateAssetEvidenceSnapshots={onCreateAssetEvidenceSnapshots}
          creatingAssetEvidenceSnapshotsAssetId={creatingAssetEvidenceSnapshotsAssetId}
          onDownloadAssetEvidencePackage={onDownloadAssetEvidencePackage}
          exportingAssetEvidencePackageAssetId={exportingAssetEvidencePackageAssetId}
          beforeAssetList={activeTab === "数据集" ? (
            <DatasetTagDatasetSummaryPanel workspaceId={workspaceId} />
          ) : null}
        />
      )}
    </section>
  );
}

function DatasetTagDatasetSummaryPanel({ workspaceId }: { workspaceId: string }) {
  const [loaded, setLoaded] = useState(false);
  const summaryQuery = useQuery({
    queryKey: promptLibraryKeys.caseTagDatasetSummaries(workspaceId),
    queryFn: () => api.listPromptEvaluationCaseTagDatasetSummaries({ limit: 100, top_dataset_limit: 3 }),
    enabled: Boolean(loaded && workspaceId),
  });
  const summaries: PromptEvaluationCaseTagDatasetSummary[] = summaryQuery.data?.items ?? [];
  const loading = summaryQuery.isLoading || summaryQuery.isFetching;
  const load = () => {
    if (loaded) {
      summaryQuery.refetch();
      return;
    }
    setLoaded(true);
  };
  return (
    <section className="grid gap-2 rounded-md border bg-background px-3 py-2 text-xs" data-testid="dataset-tag-dataset-summary-panel">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="font-medium text-foreground">跨数据集标签分布</div>
          <div className="mt-0.5 text-muted-foreground">按标签聚合所有数据集，查看哪些标签覆盖多个题库。</div>
        </div>
        <Button size="sm" variant="secondary" onClick={load} data-testid="load-dataset-tag-dataset-summaries">
          {loading ? <Loader2 className="size-3.5 animate-spin" /> : <Search className="size-3.5" />}
          {loaded ? "刷新分布" : "查看分布"}
        </Button>
      </div>
      {loaded && !loading && summaries.length === 0 && (
        <div className="rounded border border-dashed bg-muted/10 px-2 py-2 text-muted-foreground">暂无可统计的跨数据集标签。</div>
      )}
      {summaries.length > 0 && (
        <div className="grid gap-1.5" data-testid="dataset-tag-dataset-summary-results">
          {summaries.map((item) => (
            <div key={item.tag} className="flex flex-wrap items-center gap-2 rounded border bg-muted/10 px-2 py-1.5">
              <span className="font-medium text-foreground">{item.tag}</span>
              <span className="text-muted-foreground">{item.dataset_count} 个数据集</span>
              <span className="text-muted-foreground">{item.case_count} 条用例</span>
              {item.top_datasets.length > 0 && (
                <span className="text-muted-foreground">
                  样例：{item.top_datasets.map((dataset) => `${dataset.asset_name} ${dataset.case_count}`).join(" / ")}
                </span>
              )}
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function TrainingAssetPanel({
  activeTab,
  route,
  title,
  assets,
  runs,
  cases,
  loading,
  saving,
  onToggleAssetStatus,
  onUpdateAsset,
  onDeleteAsset,
  onImportDatasetFromTraces,
  importingTraceDatasetAssetId,
  onExportDatasetProtocol,
  exportingDatasetProtocolAssetId,
  onImportDatasetCopy,
  importingDatasetCopyAssetId,
  onCreateDatasetVersion,
  creatingDatasetVersionAssetId,
  onCreateCase,
  creatingCaseAssetId,
  caseDrafts,
  onCaseDraftsChange,
  focusedCaseId,
  focusedIssueId,
  focusedIssueRunReviewHref,
  onUpdateCase,
  updatingCaseId,
  onDeleteCase,
  deletingCaseId,
  onCreateAssetEvidenceSnapshots,
  creatingAssetEvidenceSnapshotsAssetId,
  onDownloadAssetEvidencePackage,
  exportingAssetEvidencePackageAssetId,
  beforeAssetList,
}: {
  activeTab: WorkbenchTab;
  route: string;
  title: string;
  assets: PromptEvaluationAsset[];
  runs: PromptEvaluationRun[];
  cases: PromptEvaluationStructuredCase[];
  loading: boolean;
  saving: boolean;
  onToggleAssetStatus: (asset: PromptEvaluationAsset) => void;
  onUpdateAsset: (assetId: string, data: UpdatePromptEvaluationAssetRequest) => Promise<unknown>;
  onDeleteAsset: (asset: PromptEvaluationAsset) => void;
  onImportDatasetFromTraces: (asset: PromptEvaluationAsset) => void;
  importingTraceDatasetAssetId: string | null;
  onExportDatasetProtocol: (asset: PromptEvaluationAsset) => void;
  exportingDatasetProtocolAssetId: string | null;
  onImportDatasetCopy: (asset: PromptEvaluationAsset) => void;
  importingDatasetCopyAssetId: string | null;
  onCreateDatasetVersion: (asset: PromptEvaluationAsset) => void;
  creatingDatasetVersionAssetId: string | null;
  onCreateCase: (data: CreatePromptEvaluationCaseRequest) => void;
  creatingCaseAssetId: string | null;
  caseDrafts: Record<string, ManualCaseDraft>;
  onCaseDraftsChange: Dispatch<SetStateAction<Record<string, ManualCaseDraft>>>;
  focusedCaseId: string | null;
  focusedIssueId: string | null;
  focusedIssueRunReviewHref: string | null;
  onUpdateCase: (caseId: string, data: UpdatePromptEvaluationCaseRequest) => Promise<unknown>;
  updatingCaseId: string | null;
  onDeleteCase: (caseId: string) => void;
  deletingCaseId: string | null;
  onCreateAssetEvidenceSnapshots: (assetId: string) => void;
  creatingAssetEvidenceSnapshotsAssetId: string | null;
  onDownloadAssetEvidencePackage: (assetId: string) => void;
  exportingAssetEvidencePackageAssetId: string | null;
  beforeAssetList?: ReactNode;
}) {
  const caseSummaries = useMemo(() => buildCaseSummaries(cases), [cases]);
  const casesByAsset = useMemo(() => buildCasesByAsset(cases), [cases]);
  const runCountByAsset = useMemo(() => {
    const counts = new Map<string, number>();
    for (const run of runs) {
      counts.set(run.asset_id, (counts.get(run.asset_id) ?? 0) + 1);
    }
    return counts;
  }, [runs]);

  return (
    <section className="grid gap-3" aria-label={`${title}内容`} data-testid={`training-route-panel-${route}`}>
      {beforeAssetList}
      {loading ? (
        <div className="h-20 rounded-md bg-muted/60" />
      ) : assets.length === 0 ? (
        <div className="rounded-md border border-dashed px-3 py-6 text-center text-sm text-muted-foreground" data-testid={`training-route-empty-${route}`}>
          {emptyTrainingRouteText(activeTab)}
        </div>
      ) : (
        <div className="divide-y rounded-md border" data-testid={`training-route-list-${route}`}>
          {assets.map((asset) => (
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
                {summarizeSkillScenarioTarget(asset) && (
                  <div className="mt-1 text-[11px] text-muted-foreground" data-testid={`skill-scenario-target-${asset.id}`}>
                    Skill 场景：{summarizeSkillScenarioTarget(asset)}
                  </div>
                )}
                {summarizeAgentRun(asset) && (
                  <div className="mt-1 text-[11px] text-muted-foreground">
                    {summarizeAgentRun(asset)}
                  </div>
                )}
                {asset.asset_type === "数据集" && summarizeDatasetVersion(asset) && (
                  <div className="mt-1 text-[11px] text-muted-foreground" data-testid={`dataset-version-summary-${asset.id}`}>
                    {summarizeDatasetVersion(asset)}
                  </div>
                )}
                {asset.asset_type !== "数据集" && summarizeLinkedDatasetVersions(asset) && (
                  <div className="mt-1 text-[11px] text-muted-foreground" data-testid={`linked-dataset-version-summary-${asset.id}`}>
                    {summarizeLinkedDatasetVersions(asset)}
                  </div>
                )}
                <ModelComparisonJudgePanel asset={asset} />
                {asset.asset_type === "数据集" && (
                  <DatasetVersionControls asset={asset} saving={saving} />
                )}
              </div>
              <div className="flex flex-wrap items-center justify-end gap-2">
                {(runCountByAsset.get(asset.id) ?? 0) > 0 && (
                  <Button
                    size="sm"
                    variant="secondary"
                    data-testid={`archive-asset-evidence-${asset.id}`}
                    onClick={() => onCreateAssetEvidenceSnapshots(asset.id)}
                    disabled={saving || creatingAssetEvidenceSnapshotsAssetId === asset.id}
                  >
                    {creatingAssetEvidenceSnapshotsAssetId === asset.id ? <Loader2 className="size-3.5 animate-spin" /> : <Archive className="size-3.5" />}
                    归档运行证据
                  </Button>
                )}
                {(runCountByAsset.get(asset.id) ?? 0) > 0 && (
                  <Button
                    size="sm"
                    variant="secondary"
                    data-testid={`download-asset-evidence-package-${asset.id}`}
                    onClick={() => onDownloadAssetEvidencePackage(asset.id)}
                    disabled={saving || exportingAssetEvidencePackageAssetId === asset.id}
                  >
                    {exportingAssetEvidencePackageAssetId === asset.id ? <Loader2 className="size-3.5 animate-spin" /> : <Download className="size-3.5" />}
                    下载归档包
                  </Button>
                )}
                {asset.asset_type === "数据集" && (
                  <>
                    <Button
                      size="sm"
                      variant="secondary"
                      data-testid={`create-dataset-version-${asset.id}`}
                      onClick={() => onCreateDatasetVersion(asset)}
                      disabled={saving || creatingDatasetVersionAssetId === asset.id}
                    >
                      {creatingDatasetVersionAssetId === asset.id ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                      生成版本快照
                    </Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      data-testid={`import-dataset-from-traces-${asset.id}`}
                      onClick={() => onImportDatasetFromTraces(asset)}
                      disabled={saving || importingTraceDatasetAssetId === asset.id}
                    >
                      {importingTraceDatasetAssetId === asset.id ? <Loader2 className="size-3.5 animate-spin" /> : <Download className="size-3.5" />}
                      从 trace 导入样本
                    </Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      data-testid={`export-dataset-protocol-${asset.id}`}
                      onClick={() => onExportDatasetProtocol(asset)}
                      disabled={saving || exportingDatasetProtocolAssetId === asset.id}
                    >
                      {exportingDatasetProtocolAssetId === asset.id ? <Loader2 className="size-3.5 animate-spin" /> : <Download className="size-3.5" />}
                      导出完整协议
                    </Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      data-testid={`import-dataset-copy-${asset.id}`}
                      onClick={() => onImportDatasetCopy(asset)}
                      disabled={saving || importingDatasetCopyAssetId === asset.id}
                    >
                      {importingDatasetCopyAssetId === asset.id ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
                      导入副本
                    </Button>
                  </>
                )}
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
                  onDraftChange={(draft) => onCaseDraftsChange((prev) => ({ ...prev, [asset.id]: draft }))}
                  onCreateCase={() => {
                    const draft = caseDrafts[asset.id] ?? emptyManualCaseDraft();
                    onCreateCase(buildManualCaseRequest(asset, draft, casesByAsset.get(asset.id)?.length ?? 0));
                    onCaseDraftsChange((prev) => ({ ...prev, [asset.id]: emptyManualCaseDraft() }));
                  }}
                  creating={creatingCaseAssetId === asset.id}
                  focusedCaseId={focusedCaseId}
                  focusedIssueId={focusedIssueId}
                  focusedIssueRunReviewHref={focusedIssueRunReviewHref}
                  onUpdateAsset={onUpdateAsset}
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
    </section>
  );
}

function ModelComparisonJudgePanel({ asset }: { asset: PromptEvaluationAsset }) {
  const summary = modelComparisonJudgeSummary(asset);
  if (!summary) return null;
  return (
    <div className="mt-2 grid gap-2 border-l pl-3 text-xs" data-testid={`model-comparison-judge-${asset.id}`}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-foreground">模型对比评分</span>
        <Badge variant="secondary">Judge：{summary.judgeModel}</Badge>
        {summary.winner && <Badge variant="outline">推荐：{summary.winner}</Badge>}
      </div>
      {summary.conclusion && <div className="text-muted-foreground">{summary.conclusion}</div>}
      <div className="grid gap-1 md:grid-cols-2">
        {summary.scores.map((score) => (
          <div key={score.model} className="min-w-0">
            <div className="flex items-center gap-2">
              <span className="truncate font-medium text-foreground">{score.model}</span>
              <Badge variant="outline">{score.totalScore}/100</Badge>
            </div>
            {score.dimensionSummary && <div className="mt-0.5 truncate text-muted-foreground">{score.dimensionSummary}</div>}
            {score.recommendation && <div className="mt-0.5 line-clamp-2 text-muted-foreground">建议：{score.recommendation}</div>}
          </div>
        ))}
      </div>
    </div>
  );
}

function RunHistoryPanel({
  workspaceId,
  runs,
  focusedRunId,
  evidenceFocus,
  runStatusFilter,
  onRunStatusFilterChange,
  candidates,
  skillResources,
  loading,
  onSyncRun,
  syncingRunId,
  onCancelRun,
  cancellingRunId,
  onReviewRun,
  reviewingRunId,
  onCreateEvidenceSnapshot,
  creatingEvidenceSnapshotRunId,
  onGenerateCandidate,
  generatingCandidateRunId,
}: {
  workspaceId: string;
  runs: PromptEvaluationRun[];
  focusedRunId: string | null;
  evidenceFocus: EvidenceFocus;
  runStatusFilter: RunStatusFilter;
  onRunStatusFilterChange: (status: RunStatusFilter) => void;
  candidates: PromptEvaluationOptimizationCandidate[];
  skillResources: SkillResourceOption[];
  loading: boolean;
  onSyncRun: (runId: string) => void;
  syncingRunId: string | null;
  onCancelRun: (runId: string) => void;
  cancellingRunId: string | null;
  onReviewRun: (run: PromptEvaluationRun, decision: "通过" | "未通过") => void;
  reviewingRunId: string | null;
  onCreateEvidenceSnapshot: (runId: string) => void;
  creatingEvidenceSnapshotRunId: string | null;
  onGenerateCandidate: (runId: string) => void;
  generatingCandidateRunId: string | null;
}) {
  const candidatesByRun = useMemo(() => buildCandidatesByRun(candidates), [candidates]);
  const [expandedRunId, setExpandedRunId] = useState<string | null>(null);
  useEffect(() => {
    if (!focusedRunId || !runs.some((run) => run.id === focusedRunId)) return;
    setExpandedRunId(focusedRunId);
    window.requestAnimationFrame(() => {
      document.querySelector(`[data-testid="prompt-evaluation-run-${focusedRunId}"]`)?.scrollIntoView({ block: "center" });
    });
  }, [focusedRunId, runs]);
  const evidenceQuery = useQuery({
    queryKey: promptLibraryKeys.runEvidence(workspaceId, expandedRunId),
    queryFn: () => api.getPromptEvaluationRunEvidence(expandedRunId ?? ""),
    enabled: !!expandedRunId,
  });
  const evidenceSnapshotQuery = useQuery({
    queryKey: promptLibraryKeys.runEvidenceSnapshots(workspaceId, expandedRunId),
    queryFn: () => api.listPromptEvaluationEvidenceSnapshots(expandedRunId ?? "", 5),
    enabled: !!expandedRunId,
  });

  return (
    <section className="grid gap-3" aria-label="运行历史内容" data-testid="training-route-panel-run-history">
      {loading ? (
        <div className="h-20 rounded-md bg-muted/60" />
      ) : runs.length === 0 ? (
        <div className="grid gap-3">
          <RunStatusFilterBar value={runStatusFilter} onChange={onRunStatusFilterChange} />
          <div className="rounded-md border border-dashed px-3 py-6 text-center text-sm text-muted-foreground" data-testid="training-route-empty-run-history">
            {runStatusFilter === "全部" ? "暂无结构化运行记录" : `暂无${runStatusFilter}运行记录`}
          </div>
        </div>
      ) : (
        <div className="grid gap-3">
          <RunStatusFilterBar value={runStatusFilter} onChange={onRunStatusFilterChange} />
          <div className="divide-y rounded-md border" data-testid="prompt-evaluation-run-list">
            {runs.map((run) => {
              const hasPendingCandidate = candidatesByRun.get(run.id)?.some((candidate) => candidate.status === "待确认") ?? false;
              return (
                <div key={run.id} data-testid={`prompt-evaluation-run-${run.id}`} className="grid gap-2 px-3 py-3 md:grid-cols-[minmax(0,1fr)_auto]">
                  <div className="min-w-0">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="truncate text-sm font-medium">{displayRunKind(run.run_kind)} · {run.status}</span>
                      <Badge variant={run.status === "通过" ? "secondary" : run.status === "已入队" || run.status === "运行中" ? "outline" : "destructive"} className="shrink-0">
                        {run.total_cases} 用例 · 通过率 {Math.round(run.pass_rate * 100)}%
                      </Badge>
                    </div>
                    <div className="mt-1 text-xs text-muted-foreground">{summarizeStructuredRun(run)}</div>
                    {run.review_decision && (
                      <div className="mt-1 text-xs text-muted-foreground">
                        人工复核：{run.review_decision}{run.review_note ? ` · ${run.review_note}` : ""}{run.reviewed_at ? ` · ${run.reviewed_at}` : ""}
                      </div>
                    )}
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
                    {canCancelPromptEvaluationRun(run) && (
                      <Button
                        size="sm"
                        variant="destructive"
                        onClick={() => onCancelRun(run.id)}
                        disabled={cancellingRunId === run.id}
                        data-testid={`cancel-prompt-evaluation-run-${run.id}`}
                      >
                        {cancellingRunId === run.id ? <Loader2 className="size-3.5 animate-spin" /> : <XCircle className="size-3.5" />}
                        取消运行
                      </Button>
                    )}
                    {canReviewPromptEvaluationRun(run) && (
                      <>
                        <Button
                          size="sm"
                          variant="secondary"
                          onClick={() => onReviewRun(run, "通过")}
                          disabled={reviewingRunId === run.id}
                          data-testid={`review-prompt-evaluation-run-pass-${run.id}`}
                        >
                          {reviewingRunId === run.id ? <Loader2 className="size-3.5 animate-spin" /> : <CheckCircle className="size-3.5" />}
                          人工通过
                        </Button>
                        <Button
                          size="sm"
                          variant="destructive"
                          onClick={() => onReviewRun(run, "未通过")}
                          disabled={reviewingRunId === run.id}
                          data-testid={`review-prompt-evaluation-run-fail-${run.id}`}
                        >
                          {reviewingRunId === run.id ? <Loader2 className="size-3.5 animate-spin" /> : <XCircle className="size-3.5" />}
                          人工驳回
                        </Button>
                      </>
                    )}
                    {canGenerateOptimizationCandidate(run) && (
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => onGenerateCandidate(run.id)}
                        disabled={generatingCandidateRunId === run.id || hasPendingCandidate}
                      >
                        {generatingCandidateRunId === run.id ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
                        {hasPendingCandidate ? "已有候选" : "生成优化候选"}
                      </Button>
                    )}
                  </div>
                  {expandedRunId === run.id && (
                    <RunEvidencePanel
                      evidence={evidenceQuery.data ?? null}
                      snapshots={evidenceSnapshotQuery.data?.items ?? []}
                      snapshotsLoading={evidenceSnapshotQuery.isLoading || evidenceSnapshotQuery.isFetching}
                      loading={evidenceQuery.isLoading || evidenceQuery.isFetching}
                      error={evidenceQuery.isError}
                      skillResources={skillResources}
                      evidenceFocus={evidenceFocus}
                      optimizationActions={{
                        canGenerate: canGenerateOptimizationCandidate(run),
                        hasPendingCandidate,
                        generatingCandidate: generatingCandidateRunId === run.id,
                        onGenerateCandidate: () => onGenerateCandidate(run.id),
                      }}
                      creatingSnapshot={creatingEvidenceSnapshotRunId === run.id}
                      onCreateSnapshot={() => onCreateEvidenceSnapshot(run.id)}
                    />
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}
    </section>
  );
}

function RunEvidencePanel({
  evidence,
  snapshots,
  snapshotsLoading,
  loading,
  error,
  skillResources,
  evidenceFocus,
  optimizationActions,
  creatingSnapshot,
  onCreateSnapshot,
}: {
  evidence: PromptEvaluationRunEvidence | null;
  snapshots: PromptEvaluationEvidenceSnapshot[];
  snapshotsLoading: boolean;
  loading: boolean;
  error: boolean;
  skillResources: SkillResourceOption[];
  evidenceFocus?: EvidenceFocus;
  optimizationActions?: {
    canGenerate: boolean;
    hasPendingCandidate: boolean;
    generatingCandidate: boolean;
    onGenerateCandidate: () => void;
  };
  creatingSnapshot: boolean;
  onCreateSnapshot: () => void;
}) {
  const workspaceId = useWorkspaceId();
  const queryClient = useQueryClient();
  const run = evidence?.run ?? null;
  const runId = run?.id ?? "";
  const [skillDrafts, setSkillDrafts] = useState<Record<string, SkillCandidateWorkflowDraft>>({});
  const [skillAction, setSkillAction] = useState<{ candidateId: string; action: SkillCandidateWorkflowAction } | null>(null);
  const candidatesQuery = useQuery({
    queryKey: ["prompt-library", workspaceId ?? "", "optimization-candidates", "run", runId],
    queryFn: () => api.listPromptEvaluationOptimizationCandidates({ run_id: runId, limit: 5 }),
    enabled: Boolean(workspaceId && runId),
  });
  const candidates = candidatesQuery.data?.items ?? [];
  const candidate = candidates[0] ?? null;
  const invalidateRunCandidates = useCallback(() => {
    if (!workspaceId || !runId) return;
    queryClient.invalidateQueries({ queryKey: ["prompt-library", workspaceId, "optimization-candidates", "run", runId] });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.candidates(workspaceId) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.assets(workspaceId) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runs(workspaceId) });
  }, [queryClient, runId, workspaceId]);
  const runSkillWorkflowAction = useCallback(async (
    item: PromptEvaluationOptimizationCandidate,
    action: SkillCandidateWorkflowAction,
  ) => {
    const draft = skillDrafts[item.id] ?? defaultSkillCandidateWorkflowDraft(item);
    setSkillAction({ candidateId: item.id, action });
    try {
      if (action === "freshness") {
        const result = await api.checkPromptEvaluationSkillCandidateFreshness(item.id, {
          source_resource_id: draft.sourceResourceId || undefined,
          repo_path: draft.repoPath.trim() || undefined,
          target_branch: draft.targetBranch.trim() || undefined,
          skill_path: draft.skillPath.trim() || undefined,
        });
        toast.success(`Skill freshness: ${result.status} / ${result.patch_check}`);
      } else if (action === "apply") {
        const result = await api.applyPromptEvaluationSkillCandidate(item.id, {
          source_resource_id: draft.sourceResourceId || undefined,
          repo_path: draft.repoPath.trim() || undefined,
          target_branch: draft.targetBranch.trim() || undefined,
          skill_path: draft.skillPath.trim() || undefined,
          changelog_path: draft.changelogPath.trim() || undefined,
          allow_dirty: draft.allowDirty,
          skip_changelog: draft.skipChangelog,
        });
        toast.success(`Skill apply: ${result.apply.status}`);
      } else if (action === "prepare-re-eval") {
        const result = await api.preparePromptEvaluationSkillReEvalAsset(item.id, {
          source_resource_id: draft.sourceResourceId || undefined,
          repo_path: draft.repoPath.trim() || undefined,
          target_branch: draft.targetBranch.trim() || undefined,
          skill_path: draft.skillPath.trim() || undefined,
          include_draft: draft.includeDraft,
        });
        setSkillDrafts((prev) => ({ ...prev, [item.id]: { ...draft, reEvalAssetId: result.asset.id } }));
        toast.success(`Skill re-eval asset ready: ${result.case_count} cases`);
      } else {
        const result = await api.runPromptEvaluationSkillReEval(item.id, {
          asset_id: draft.reEvalAssetId.trim() || undefined,
        });
        toast.success(`Skill re-eval run: ${result.run.status}`);
      }
      invalidateRunCandidates();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Skill workflow action failed");
    } finally {
      setSkillAction(null);
    }
  }, [invalidateRunCandidates, skillDrafts]);

  if (loading) {
    return <div className="rounded-md border border-dashed px-3 py-4 text-sm text-muted-foreground md:col-span-2">正在读取运行证据...</div>;
  }
  if (error || !evidence || !run) {
    return <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-4 text-sm text-destructive md:col-span-2">运行证据读取失败。</div>;
  }

  const totalTokens = Number(run.input_tokens ?? 0) + Number(run.output_tokens ?? 0);
  const focusLabels = [
    evidenceFocus?.traceSeq ? `trace ${evidenceFocus.traceSeq}` : "",
    evidenceFocus?.toolChainId ? `tool ${evidenceFocus.toolChainId}` : "",
    evidenceFocus?.messageSeq ? `message ${evidenceFocus.messageSeq}` : "",
    evidenceFocus?.failureAnchor ? `failure ${evidenceFocus.failureAnchor}` : "",
  ].filter(Boolean);
  const rawPayload = {
    run: evidence.run,
    trials: evidence.trials,
    task_usage: evidence.task_usage,
    task_messages: evidence.task_messages,
    trace_events: evidence.trace_events,
    execution_spans: evidence.execution_spans,
    tool_call_chains: evidence.tool_call_chains,
    tool_call_summary: evidence.tool_call_summary,
    execution_summary: evidence.execution_summary,
    evidence: evidence.evidence,
    context: evidence.上下文,
  };

  return (
    <section className="grid gap-3 rounded-md border bg-muted/10 p-3 md:col-span-2" data-testid="run-evidence-panel">
      <div className="grid gap-2 md:grid-cols-[minmax(0,1fr)_auto]">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-foreground">运行证据摘要</span>
            <Badge variant={run.status === "通过" ? "secondary" : run.failed_cases > 0 ? "destructive" : "outline"}>{run.status}</Badge>
            <Badge variant={totalTokens > 0 ? "secondary" : "outline"}>{formatNumber(totalTokens)} token</Badge>
            <Badge variant={snapshots.length > 0 ? "secondary" : "outline"}>{snapshotsLoading ? "快照读取中" : `${snapshots.length} 个快照`}</Badge>
            {focusLabels.map((label) => <Badge key={label} variant="outline">{label}</Badge>)}
          </div>
          <div className="mt-1 break-all text-[11px] leading-5 text-muted-foreground">
            run {run.id} · task {run.task_id || "未绑定"} · model {run.model || "未记录"} · runtime {run.runtime_provider || "未记录"}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2 md:justify-end">
          {optimizationActions?.canGenerate && (
            <Button
              size="sm"
              variant="secondary"
              onClick={optimizationActions.onGenerateCandidate}
              disabled={optimizationActions.generatingCandidate || optimizationActions.hasPendingCandidate}
            >
              {optimizationActions.generatingCandidate ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
              {optimizationActions.hasPendingCandidate ? "已有候选" : "生成候选"}
            </Button>
          )}
          <Button size="sm" variant="secondary" onClick={() => candidatesQuery.refetch()} disabled={candidatesQuery.isFetching}>
            {candidatesQuery.isFetching ? <Loader2 className="size-3.5 animate-spin" /> : <RefreshCw className="size-3.5" />}
            刷新候选
          </Button>
          <Button size="sm" variant="secondary" onClick={onCreateSnapshot} disabled={creatingSnapshot}>
            {creatingSnapshot ? <Loader2 className="size-3.5 animate-spin" /> : <Archive className="size-3.5" />}
            归档快照
          </Button>
        </div>
      </div>

      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        <EvidenceMetric label="用例" value={`${formatNumber(run.passed_cases)}/${formatNumber(run.total_cases)}`} />
        <EvidenceMetric label="耗时" value={formatDuration(run.total_duration_ms)} />
        <EvidenceMetric label="成本" value={formatMoney(run.estimated_cost)} />
        <EvidenceMetric label="证据" value={`${evidence.trials.length} trial · ${evidence.task_messages.length} message · ${evidence.trace_events.length} trace`} />
      </div>

      {candidate && (
        <div className="grid gap-2 rounded border bg-background px-2 py-2 text-xs" data-testid="run-evidence-candidate">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{candidate.candidate_name}</span>
            <Badge variant={candidate.status === "待确认" ? "secondary" : "outline"}>{candidate.status}</Badge>
            <Badge variant="outline">失败 {candidate.failed_case_count}</Badge>
          </div>
          <div className="text-muted-foreground">{candidate.rationale || "基于失败运行生成，等待人工确认。"}</div>
          <SkillCandidateWorkflowPanel
            candidate={candidate}
            draft={skillDrafts[candidate.id] ?? defaultSkillCandidateWorkflowDraft(candidate)}
            evidence={candidateSkillWorkflowEvidence(candidate)}
            resources={skillResources}
            pendingAction={skillAction?.candidateId === candidate.id ? skillAction.action : null}
            disabled={candidate.status !== "待确认"}
            onDraftChange={(next) => setSkillDrafts((prev) => ({ ...prev, [candidate.id]: next }))}
            onRunAction={(action) => void runSkillWorkflowAction(candidate, action)}
          />
        </div>
      )}

      <details className="rounded border bg-background px-3 py-2 text-xs" open={focusLabels.length > 0}>
        <summary className="cursor-pointer font-medium text-muted-foreground">完整原始 evidence JSON</summary>
        <pre className="mt-2 max-h-96 overflow-auto whitespace-pre-wrap font-mono text-[11px] leading-5">
          {JSON.stringify(rawPayload, null, 2)}
        </pre>
      </details>
    </section>
  );
}

function EvidenceMetric({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded border bg-background px-2 py-1.5 text-xs">
      <div className="truncate text-[11px] text-muted-foreground">{label}</div>
      <div className="mt-0.5 truncate font-semibold">{value}</div>
    </div>
  );
}

function emptyTrainingRouteText(activeTab: WorkbenchTab) {
  switch (activeTab) {
    case "数据集":
      return "暂无数据集题库，先新建数据集或从 trace 导入样本";
    case "测试套件":
      return "暂无测试套件，先把稳定用例组织成可回归的套件";
    default:
      return "暂无训练与评估资产";
  }
}

type TrainingRouteIntro = {
  route: string;
  title: string;
  subtitle: string;
  facts: Array<[string, string]>;
  evidence: string;
};

function trainingRouteIntro(
  activeTab: WorkbenchTab,
  context: {
    visibleAssets: PromptEvaluationAsset[];
    cases: PromptEvaluationStructuredCase[];
    runs: PromptEvaluationRun[];
    candidates: PromptEvaluationOptimizationCandidate[];
    runStatusFilter: RunStatusFilter;
  },
): TrainingRouteIntro {
  const enabledAssets = context.visibleAssets.filter((asset) => asset.status === "启用").length;
  switch (activeTab) {
    case "数据集": {
      const datasetRows = context.visibleAssets.reduce((sum, asset) => sum + asset.dataset_row_count, 0);
      const traceCases = context.cases.filter((item) => item.source === "trace").length;
      return {
        route: "datasets",
        title: "数据集题库",
        subtitle: "把真实 trace 和手工样例沉淀成可复跑的评测题库，用于后续测试套件和实验。",
        facts: [
          ["数据集资产", String(context.visibleAssets.length)],
          ["启用", String(enabledAssets)],
          ["数据集行", formatNumber(datasetRows)],
          ["trace 样本", formatNumber(traceCases)],
        ],
        evidence: "公开 API 创建/回读数据集，页面可从真实 trace 导入样本并生成结构化用例。",
      };
    }
    case "测试套件": {
      const suiteCases = context.visibleAssets.reduce((sum, asset) => sum + asset.test_suite_case_count, 0);
      return {
        route: "test-suites",
        title: "测试套件回归",
        subtitle: "把一组稳定用例固定为回归套件，用来反复验证提示词、智能体和小队 SOP 是否退化。",
        facts: [
          ["测试套件", String(context.visibleAssets.length)],
          ["启用", String(enabledAssets)],
          ["套件用例", formatNumber(suiteCases)],
          ["结构化用例", formatNumber(context.cases.length)],
        ],
        evidence: "页面可创建套件资产、维护手工用例，并通过评测记录回读每次套件执行结果。",
      };
    }
    case "评测记录": {
      const reviewRuns = context.runs.filter((run) => run.status === "需人工复核").length;
      return {
        route: "evaluation-runs",
        title: context.runStatusFilter === "需人工复核" ? "人工复核队列" : "评测记录与证据",
        subtitle: "按运行记录回看任务、模型、耗时、评估结论和服务端证据快照，支持同步、取消和人工复核。",
        facts: [
          ["当前筛选", context.runStatusFilter === "全部" ? "全部运行" : context.runStatusFilter],
          ["运行记录", formatNumber(context.runs.length)],
          ["人工复核", formatNumber(reviewRuns)],
          ["带任务记录", formatNumber(context.runs.filter((run) => Boolean(run.task_id)).length)],
        ],
        evidence: "每条运行可展开 task/message/trace/usage 证据，并可归档服务端证据快照。",
      };
    }
    default:
      return {
        route: "assets",
        title: activeTab,
        subtitle: "训练与评估资产页面。",
        facts: [["资产", String(context.visibleAssets.length)]],
        evidence: "通过公开 API 创建和回读。",
      };
  }
}

function TrainingFocusedIssueCallout({
  activeTab,
  issueId,
  taskCount,
}: {
  activeTab: WorkbenchTab;
  issueId: string;
  taskCount: number;
}) {
  const actionLabel = activeTab === "数据集"
    ? "点击数据集里的“从 trace 导入样本”会优先使用该 issue 的任务 trace。"
    : "当前页面带有 issue 复盘上下文，可回到 issue 查看完整链路。";
  return (
    <section className="rounded-md border border-info/30 bg-info/5 px-3 py-2 text-xs" data-testid="training-focused-issue-callout">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <div className="font-medium text-foreground">来自 issue {issueId} 的运行复盘</div>
          <div className="mt-0.5 text-muted-foreground">
            {taskCount > 0 ? `已识别 ${taskCount} 个任务 trace。` : "正在读取该 issue 的任务 trace。"}
            {" "}{actionLabel}
          </div>
        </div>
        <a className="shrink-0 rounded border bg-background px-2 py-1 text-[11px] hover:bg-accent" href={`../issues/${encodeURIComponent(issueId)}`}>
          返回 issue
        </a>
      </div>
    </section>
  );
}

function TrainingRouteIntroCard({
  route,
  title,
  subtitle,
  facts,
  evidence,
  action,
}: TrainingRouteIntro & { action?: ReactNode }) {
  return (
    <section className="rounded-md border border-border/70 bg-muted/15 px-4 py-3" data-testid={`training-route-intro-${route}`}>
      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <div className="text-xs font-medium text-muted-foreground">训练与评估子模块</div>
          <h3 className="mt-1 text-base font-semibold">{title}</h3>
          <p className="mt-1 max-w-3xl text-xs leading-5 text-muted-foreground">{subtitle}</p>
          <p className="mt-2 max-w-3xl text-xs leading-5 text-muted-foreground">{evidence}</p>
        </div>
        {action ? <div className="shrink-0">{action}</div> : null}
      </div>
      <div className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        {facts.map(([label, value]) => (
          <div key={label} className="min-w-0 rounded-md border bg-background px-3 py-2" data-testid={`training-route-intro-fact-${route}-${label}`}>
            <div className="truncate text-[11px] text-muted-foreground">{label}</div>
            <div className="mt-1 truncate text-sm font-semibold">{value}</div>
          </div>
        ))}
      </div>
    </section>
  );
}

function TrainingRouteWorkspaceBand({
  activeTab,
  route,
  visibleAssets,
  cases,
  runs,
  candidates,
  runStatusFilter,
}: {
  activeTab: WorkbenchTab;
  route: string;
  visibleAssets: PromptEvaluationAsset[];
  cases: PromptEvaluationStructuredCase[];
  runs: PromptEvaluationRun[];
  candidates: PromptEvaluationOptimizationCandidate[];
  runStatusFilter: RunStatusFilter;
}) {
  const config = trainingRouteOperatingModel(activeTab, {
    visibleAssets,
    cases,
    runs,
    candidates,
    runStatusFilter,
  });
  if (!config) return null;
  return (
    <section className={`grid gap-3 rounded-md border px-4 py-3 ${config.className}`} data-testid={`training-route-operating-model-${route}`}>
      <div className="flex flex-col gap-2 md:flex-row md:items-start md:justify-between">
        <div className="min-w-0">
          <div className="text-xs font-medium text-muted-foreground">{config.kicker}</div>
          <h3 className="mt-1 text-sm font-semibold">{config.title}</h3>
          <p className="mt-1 max-w-3xl text-xs leading-5 text-muted-foreground">{config.description}</p>
        </div>
        <Badge variant="outline" className="w-fit shrink-0">{config.badge}</Badge>
      </div>
      <div className="grid gap-2 md:grid-cols-3">
        {config.steps.map((step, index) => (
          <div key={step.label} className="min-w-0 rounded-md border bg-background px-3 py-2" data-testid={`training-route-operating-step-${route}-${index + 1}`}>
            <div className="text-[11px] font-medium text-muted-foreground">{step.label}</div>
            <div className="mt-1 truncate text-sm font-semibold">{step.title}</div>
            <div className="mt-1 line-clamp-2 text-xs leading-5 text-muted-foreground">{step.detail}</div>
          </div>
        ))}
      </div>
    </section>
  );
}

type TrainingRouteOperatingModel = {
  kicker: string;
  title: string;
  description: string;
  badge: string;
  className: string;
  steps: Array<{ label: string; title: string; detail: string }>;
};

function trainingRouteOperatingModel(
  activeTab: WorkbenchTab,
  context: {
    visibleAssets: PromptEvaluationAsset[];
    cases: PromptEvaluationStructuredCase[];
    runs: PromptEvaluationRun[];
    candidates: PromptEvaluationOptimizationCandidate[];
    runStatusFilter: RunStatusFilter;
  },
): TrainingRouteOperatingModel | null {
  switch (activeTab) {
    case "数据集": {
      const datasetRows = context.visibleAssets.reduce((sum, asset) => sum + asset.dataset_row_count, 0);
      const traceCases = context.cases.filter((item) => item.source === "trace").length;
      return {
        kicker: "数据集工作台",
        title: "样本入库、版本快照、下游复用",
        description: "数据集页面关注题库资产本身：从 trace 或手工样例形成行级事实，生成可追溯版本，再供测试套件和实验引用。",
        badge: "题库事实",
        className: "border-sky-500/30 bg-sky-500/5",
        steps: [
          { label: "入口", title: "trace 导入或手工样本", detail: `${formatNumber(traceCases)} 条 trace 样本，资产 ${formatNumber(context.visibleAssets.length)} 个` },
          { label: "版本", title: "生成数据集版本快照", detail: `${formatNumber(datasetRows)} 行样本可形成版本指纹，避免实验偷偷读最新数据` },
          { label: "复用", title: "供测试套件和实验绑定", detail: "下游资产通过数据集版本证明输入一致，便于回归和对比" },
        ],
      };
    }
    case "测试套件": {
      const suiteCases = context.visibleAssets.reduce((sum, asset) => sum + asset.test_suite_case_count, 0);
      return {
        kicker: "测试套件工作台",
        title: "固定试卷、断言回归、失败定位",
        description: "测试套件页面关注稳定回归：把多条用例和断言组织成一张试卷，反复验证提示词、智能体或小队流程是否退化。",
        badge: "回归试卷",
        className: "border-violet-500/30 bg-violet-500/5",
        steps: [
          { label: "组织", title: "用例组成套件", detail: `${formatNumber(suiteCases)} 条套件用例，${formatNumber(context.cases.length)} 条结构化用例` },
          { label: "执行", title: "反复运行同一试卷", detail: "评测记录会记录通过率、失败原因、耗时和 token" },
          { label: "定位", title: "断言级复盘", detail: "失败后可跳到运行证据、生成候选或进入人工复核" },
        ],
      };
    }
    case "评测记录": {
      const taskRuns = context.runs.filter((run) => Boolean(run.task_id)).length;
      const reviewRuns = context.runs.filter((run) => run.status === "需人工复核").length;
      return {
        kicker: "评测记录工作台",
        title: "运行检索、证据展开、人工复核",
        description: "评测记录页面关注证据检索：按状态筛选运行，展开 task、message、trace、usage、span 和服务端快照。",
        badge: context.runStatusFilter === "全部" ? "证据检索" : context.runStatusFilter,
        className: "border-emerald-500/30 bg-emerald-500/5",
        steps: [
          { label: "筛选", title: "按运行状态定位", detail: `当前筛选：${context.runStatusFilter === "全部" ? "全部运行" : context.runStatusFilter}` },
          { label: "证据", title: "任务和 Trace 展开", detail: `${formatNumber(taskRuns)} 条运行绑定任务，可展开消息、工具调用和用量` },
          { label: "复核", title: "人工复核队列", detail: `${formatNumber(reviewRuns)} 条运行等待人工判断，可通过或驳回` },
        ],
      };
    }
    default:
      return null;
  }
}

function RunStatusFilterBar({
  value,
  onChange,
}: {
  value: RunStatusFilter;
  onChange: (status: RunStatusFilter) => void;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-md border border-border/70 bg-muted/10 px-3 py-2" data-testid="run-status-filter-bar">
      <span className="text-xs font-medium text-muted-foreground">运行状态</span>
      {RUN_STATUS_FILTERS.map((status) => (
        <FilterButton key={status} active={value === status} onClick={() => onChange(status)}>
          {status === "需人工复核" ? "人工复核队列" : status}
        </FilterButton>
      ))}
    </div>
  );
}

type SkillCandidateWorkflowAction = "freshness" | "apply" | "prepare-re-eval" | "run-re-eval";

type SkillResourceOption = {
  id: string;
  projectTitle: string;
  label: string;
  resourceType: string;
  repo: string;
  repoPath: string;
  branch: string;
  detail: string;
  requiresRepoPath: boolean;
};

type SkillCandidateWorkflowDraft = {
  sourceResourceId: string;
  repoPath: string;
  targetBranch: string;
  skillPath: string;
  changelogPath: string;
  reEvalAssetId: string;
  includeDraft: boolean;
  allowDirty: boolean;
  skipChangelog: boolean;
};

type SkillCandidateWorkflowEvidence = {
  snapshot: Record<string, unknown>;
  freshness: Record<string, unknown>;
  apply: Record<string, unknown>;
  reEval: Record<string, unknown>;
  reEvalRun: Record<string, unknown>;
};

function SkillCandidateWorkflowPanel({
  candidate,
  draft,
  evidence,
  resources,
  pendingAction,
  disabled,
  onDraftChange,
  onRunAction,
}: {
  candidate: PromptEvaluationOptimizationCandidate;
  draft: SkillCandidateWorkflowDraft;
  evidence: SkillCandidateWorkflowEvidence;
  resources: SkillResourceOption[];
  pendingAction: SkillCandidateWorkflowAction | null;
  disabled: boolean;
  onDraftChange: (draft: SkillCandidateWorkflowDraft) => void;
  onRunAction: (action: SkillCandidateWorkflowAction) => void;
}) {
  const snapshotHash = stringFromUnknown(evidence.snapshot["skill_hash"]);
  const freshnessStatus = stringFromUnknown(evidence.freshness["status"]) || "未检查";
  const applyStatus = stringFromUnknown(evidence.apply["status"]) || "未应用";
  const reEvalAssetId = draft.reEvalAssetId || stringFromUnknown(evidence.reEval["asset_id"]);
  const reEvalRunId = stringFromUnknown(evidence.reEvalRun["run_id"]);
  const canRunReEval = !disabled && Boolean(reEvalAssetId);
  const selectedResource = resources.find((resource) => resource.id === draft.sourceResourceId) ?? null;
  const skillPatch = asRecord(candidate.skill_patch);
  const patchText = stringFromUnknown(skillPatch["patch"]);
  const expectedImprovement = stringFromUnknown(skillPatch["expected_improvement"]);
  const risk = stringFromUnknown(skillPatch["risk"]);
  const verificationPlan = stringFromUnknown(skillPatch["verification_plan"]);
  const patchHash = stringFromUnknown(skillPatch["patch_hash"]);
  const publicationStatus = stringFromUnknown(skillPatch["publication_status"]);
  const candidateIntent = stringFromUnknown(skillPatch["candidate_intent"]) || "update_existing_skill";
  const operationSkillKey = stringFromUnknown(skillPatch["operation_skill_key"]);
  const operationSkillPath = stringFromUnknown(skillPatch["operation_skill_path"]);
  const operationSkillReason = stringFromUnknown(skillPatch["operation_skill_reason"]);
  return (
    <section className="mt-3 grid gap-2 rounded-sm border border-border/70 bg-muted/10 px-2 py-2 text-xs" data-testid={`skill-candidate-workflow-${candidate.id}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="font-medium">Skill 发布链路</div>
        <div className="flex flex-wrap gap-1">
          <Badge variant={snapshotHash ? "secondary" : "outline"}>snapshot {shortId(snapshotHash) || "missing"}</Badge>
          <Badge variant={candidateIntent === "create_operation_skill" ? "secondary" : "outline"}>
            {candidateIntent === "create_operation_skill" ? "新建 operation skill" : "更新已有 skill"}
          </Badge>
          <Badge variant={freshnessStatus === "conflict" || freshnessStatus === "stale" ? "destructive" : "outline"}>{freshnessStatus}</Badge>
          <Badge variant={applyStatus === "applied" ? "secondary" : applyStatus === "conflict" || applyStatus === "blocked" ? "destructive" : "outline"}>{applyStatus}</Badge>
          {reEvalRunId && <Badge variant="secondary">re-eval {shortId(reEvalRunId)}</Badge>}
        </div>
      </div>
      <label className="grid gap-1">
        <span className="text-muted-foreground">项目资源</span>
        <select
          value={draft.sourceResourceId}
          onChange={(event) => {
            const nextResource = resources.find((resource) => resource.id === event.target.value) ?? null;
            onDraftChange({
              ...draft,
              sourceResourceId: event.target.value,
              repoPath: nextResource?.repoPath || draft.repoPath,
              targetBranch: nextResource?.branch || draft.targetBranch,
            });
          }}
          className="h-8 rounded-sm border border-input bg-background px-2 text-xs"
        >
          <option value="">手动填写本地 checkout</option>
          {resources.map((resource) => (
            <option key={resource.id} value={resource.id}>
              {resource.label}
            </option>
          ))}
        </select>
        <span className="text-[11px] text-muted-foreground">
          {selectedResource
            ? `${selectedResource.detail}${selectedResource.requiresRepoPath ? " · 仍需填写本地 checkout" : ""}`
            : "可选择 Gongfeng/local project resource；Gongfeng 资源当前仍需本地 checkout 承载读写。"}
        </span>
      </label>
      <div className="grid gap-2 md:grid-cols-3">
        <label className="grid gap-1">
          <span className="text-muted-foreground">本地工蜂 checkout</span>
          <Input
            value={draft.repoPath}
            onChange={(event) => onDraftChange({ ...draft, repoPath: event.target.value })}
            placeholder="/data/ida/goal-test"
            className="h-8 text-xs"
          />
        </label>
        <label className="grid gap-1">
          <span className="text-muted-foreground">目标分支</span>
          <Input
            value={draft.targetBranch}
            onChange={(event) => onDraftChange({ ...draft, targetBranch: event.target.value })}
            placeholder="v5.0.0_dev_sop"
            className="h-8 text-xs"
          />
        </label>
        <label className="grid gap-1">
          <span className="text-muted-foreground">Skill 路径</span>
          <Input
            value={draft.skillPath}
            onChange={(event) => onDraftChange({ ...draft, skillPath: event.target.value })}
            placeholder=".codebuddy/skills/05-verify/SKILL.md"
            className="h-8 text-xs"
          />
        </label>
      </div>
      <div className="grid gap-2 md:grid-cols-2">
        <label className="grid gap-1">
          <span className="text-muted-foreground">CHANGELOG 路径</span>
          <Input
            value={draft.changelogPath}
            onChange={(event) => onDraftChange({ ...draft, changelogPath: event.target.value })}
            placeholder="默认写入 skill 旁边 CHANGELOG.md"
            className="h-8 text-xs"
          />
        </label>
        <label className="grid gap-1">
          <span className="text-muted-foreground">Re-eval 资产</span>
          <Input
            value={draft.reEvalAssetId}
            onChange={(event) => onDraftChange({ ...draft, reEvalAssetId: event.target.value })}
            placeholder={stringFromUnknown(evidence.reEval["asset_id"]) || "准备 re-eval 后自动填充"}
            className="h-8 text-xs"
          />
        </label>
      </div>
      <div className="flex flex-wrap gap-3 text-[11px] text-muted-foreground">
        <label className="inline-flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={draft.includeDraft}
            onChange={(event) => onDraftChange({ ...draft, includeDraft: event.target.checked })}
          />
          re-eval 包含 draft case
        </label>
        <label className="inline-flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={draft.allowDirty}
            onChange={(event) => onDraftChange({ ...draft, allowDirty: event.target.checked })}
          />
          允许 dirty worktree
        </label>
        <label className="inline-flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={draft.skipChangelog}
            onChange={(event) => onDraftChange({ ...draft, skipChangelog: event.target.checked })}
          />
          跳过 CHANGELOG
        </label>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <SkillWorkflowButton action="freshness" pendingAction={pendingAction} disabled={disabled} onRunAction={onRunAction}>
          Freshness
        </SkillWorkflowButton>
        <SkillWorkflowButton action="apply" pendingAction={pendingAction} disabled={disabled} onRunAction={onRunAction}>
          Apply + CHANGELOG
        </SkillWorkflowButton>
        <SkillWorkflowButton action="prepare-re-eval" pendingAction={pendingAction} disabled={disabled} onRunAction={onRunAction}>
          Prepare re-eval
        </SkillWorkflowButton>
        <SkillWorkflowButton action="run-re-eval" pendingAction={pendingAction} disabled={!canRunReEval} onRunAction={onRunAction}>
          Run re-eval
        </SkillWorkflowButton>
      </div>
      <div className="grid gap-1 break-all text-[11px] text-muted-foreground md:grid-cols-2">
        <div>base {shortId(stringFromUnknown(evidence.snapshot["base_commit"])) || "missing"} · path {draft.skillPath || stringFromUnknown(evidence.snapshot["skill_path"]) || "missing"}</div>
        <div>re-eval asset {shortId(reEvalAssetId) || "missing"} · run {shortId(reEvalRunId) || "not run"}</div>
      </div>
      {(patchText || expectedImprovement || risk || verificationPlan || patchHash || publicationStatus || operationSkillKey || operationSkillPath || operationSkillReason) && (
        <div className="grid gap-1 rounded border bg-background px-2 py-2 text-[11px] leading-5" data-testid={`skill-candidate-diff-risk-${candidate.id}`}>
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge variant={patchHash ? "secondary" : "outline"}>patch {shortId(patchHash) || "draft"}</Badge>
            <Badge variant="outline">发布 {publicationStatus || "draft"}</Badge>
            <Badge variant={candidateIntent === "create_operation_skill" ? "secondary" : "outline"}>
              {candidateIntent === "create_operation_skill" ? "create_operation_skill" : "update_existing_skill"}
            </Badge>
          </div>
          {(operationSkillKey || operationSkillPath || operationSkillReason) && (
            <div className="grid gap-1 rounded bg-muted/20 px-2 py-1.5">
              <div className="font-medium text-foreground">Operation Skill 候选</div>
              <div className="text-muted-foreground">
                key {operationSkillKey || "未记录"} · path {operationSkillPath || draft.skillPath || "未记录"}
              </div>
              {operationSkillReason && <div className="text-muted-foreground">{operationSkillReason}</div>}
            </div>
          )}
          <div className="grid gap-1 md:grid-cols-3">
            <div className="min-w-0">
              <div className="font-medium text-foreground">预期改善</div>
              <div className="mt-1 text-muted-foreground">{expectedImprovement || "未记录"}</div>
            </div>
            <div className="min-w-0">
              <div className="font-medium text-foreground">风险</div>
              <div className="mt-1 text-muted-foreground">{risk || "未记录"}</div>
            </div>
            <div className="min-w-0">
              <div className="font-medium text-foreground">验证计划</div>
              <div className="mt-1 text-muted-foreground">{verificationPlan || "未记录"}</div>
            </div>
          </div>
          {patchText && (
            <pre className="max-h-36 overflow-auto whitespace-pre-wrap rounded bg-muted/30 px-2 py-1 font-mono text-[11px] leading-5 text-foreground">{patchText}</pre>
          )}
        </div>
      )}
    </section>
  );
}

function SkillWorkflowButton({
  action,
  pendingAction,
  disabled,
  children,
  onRunAction,
}: {
  action: SkillCandidateWorkflowAction;
  pendingAction: SkillCandidateWorkflowAction | null;
  disabled: boolean;
  children: ReactNode;
  onRunAction: (action: SkillCandidateWorkflowAction) => void;
}) {
  const pending = pendingAction === action;
  return (
    <Button size="sm" variant="secondary" className="h-7 text-xs" onClick={() => onRunAction(action)} disabled={disabled || pendingAction !== null}>
      {pending ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
      {children}
    </Button>
  );
}

function defaultSkillCandidateWorkflowDraft(candidate: PromptEvaluationOptimizationCandidate): SkillCandidateWorkflowDraft {
  const evidence = candidateSkillWorkflowEvidence(candidate);
  const skillPatch = asRecord(candidate.skill_patch);
  return {
    sourceResourceId: stringFromUnknown(skillPatch["source_resource_id"]) || stringFromUnknown(evidence.snapshot["source_resource_id"]),
    repoPath: stringFromUnknown(skillPatch["repo_path"]) || stringFromUnknown(evidence.snapshot["repo_path"]),
    targetBranch: stringFromUnknown(evidence.freshness["target_branch"]) || stringFromUnknown(skillPatch["target_branch"]) || stringFromUnknown(evidence.snapshot["branch"]),
    skillPath: stringFromUnknown(evidence.freshness["skill_path"]) || stringFromUnknown(skillPatch["skill_path"]) || stringFromUnknown(evidence.snapshot["skill_path"]),
    changelogPath: stringFromUnknown(evidence.apply["changelog_path"]) || stringFromUnknown(skillPatch["changelog_path"]),
    reEvalAssetId: stringFromUnknown(evidence.reEval["asset_id"]),
    includeDraft: false,
    allowDirty: false,
    skipChangelog: false,
  };
}

function buildSkillResourceOptions(projects: Project[], resourceGroups: ProjectResource[][]): SkillResourceOption[] {
  const projectTitles = new Map(projects.map((project) => [project.id, project.title]));
  return resourceGroups
    .flat()
    .filter((resource) => resource.resource_type === "gongfeng_repo" || resource.resource_type === "local_directory")
    .map((resource) => skillResourceOptionFromProjectResource(resource, projectTitles.get(resource.project_id) || "未命名项目"))
    .filter((resource): resource is SkillResourceOption => resource !== null)
    .sort((a, b) => `${a.projectTitle}:${a.label}`.localeCompare(`${b.projectTitle}:${b.label}`, "zh-Hans"));
}

function skillResourceOptionFromProjectResource(resource: ProjectResource, projectTitle: string): SkillResourceOption | null {
  const ref = isRecord(resource.resource_ref) ? resource.resource_ref : {};
  const resourceLabel = typeof resource.label === "string" && resource.label.trim() ? resource.label.trim() : "";
  if (resource.resource_type === "gongfeng_repo") {
    const repo = stringFromUnknown(ref["project_path"]) || stringFromUnknown(ref["url"]);
    if (!repo) return null;
    const branch = stringFromUnknown(ref["ref"]);
    const title = resourceLabel || stringFromUnknown(ref["title"]) || repo;
    return {
      id: resource.id,
      projectTitle,
      label: `${projectTitle} · 工蜂 · ${title}`,
      resourceType: resource.resource_type,
      repo,
      repoPath: "",
      branch,
      detail: `${projectTitle} · ${repo}${branch ? ` · ${branch}` : ""}`,
      requiresRepoPath: true,
    };
  }
  const repoPath = stringFromUnknown(ref["local_path"]);
  if (!repoPath) return null;
  const title = resourceLabel || stringFromUnknown(ref["label"]) || repoPath;
  return {
    id: resource.id,
    projectTitle,
    label: `${projectTitle} · 本地 · ${title}`,
    resourceType: resource.resource_type,
    repo: title,
    repoPath,
    branch: "HEAD",
    detail: `${projectTitle} · ${repoPath}`,
    requiresRepoPath: false,
  };
}

function candidateSkillWorkflowEvidence(candidate: PromptEvaluationOptimizationCandidate): SkillCandidateWorkflowEvidence {
  const metrics = isRecord(candidate.metrics) ? candidate.metrics : {};
  const skillPatch = asRecord(candidate.skill_patch);
  const apply = asRecord(metrics["skill_apply"]);
  const freshness = firstRecord(asRecord(metrics["skill_freshness"]), asRecord(apply["freshness"]));
  const reEval = asRecord(metrics["skill_re_eval"]);
  const reEvalRun = asRecord(metrics["skill_re_eval_run"]);
  const sourceSnapshot = isRecord(candidate.source_prompt_snapshot) ? candidate.source_prompt_snapshot : {};
  const snapshot = firstRecord(
    asRecord(apply["snapshot"]),
    asRecord(freshness["snapshot"]),
    asRecord(reEval["re_eval_snapshot"]),
    asRecord(reEval["source_snapshot"]),
    asRecord(skillPatch["source_snapshot"]),
    asRecord(sourceSnapshot["skill_snapshot"]),
    hasSkillSnapshotShape(sourceSnapshot) ? sourceSnapshot : {},
  );
  return {
    snapshot,
    freshness,
    apply,
    reEval,
    reEvalRun,
  };
}

function asRecord(value: unknown): Record<string, unknown> {
  return isRecord(value) ? value : {};
}

function firstRecord(...values: Record<string, unknown>[]): Record<string, unknown> {
  return values.find((value) => Object.keys(value).length > 0) ?? {};
}

function hasSkillSnapshotShape(value: Record<string, unknown>): boolean {
  return Boolean(value["base_commit"] || value["skill_hash"] || value["skill_path"]);
}

function shortId(value: string): string {
  if (!value) return "";
  return value.length > 10 ? value.slice(0, 10) : value;
}

function issueIdFromStructuredCase(item: PromptEvaluationStructuredCase): string | null {
  const variableIssueId = stringFromUnknown(item.variables["issue_id"]);
  if (variableIssueId) return variableIssueId;
  const issue = asRecord(item.input["issue"]);
  const inputIssueId = stringFromUnknown(issue["id"]);
  if (inputIssueId) return inputIssueId;
  const issueTag = item.tags.map((tag) => stringFromUnknown(tag)).find((tag) => tag.startsWith("issue:"));
  return issueTag?.slice("issue:".length) || null;
}

function caseValidationSummary(item: PromptEvaluationStructuredCase): string {
  const validation = stringFromUnknown(item.expected["validation"]);
  if (validation) return validation;
  const expectedBehavior = stringFromUnknown(item.expected["expected_behavior"]);
  if (expectedBehavior) return expectedBehavior;
  if (item.expected_contains.length > 0) return `包含 ${item.expected_contains.map((value) => stringFromUnknown(value)).filter(Boolean).slice(0, 5).join("、")}`;
  return "";
}

function caseEvidenceSummary(item: PromptEvaluationStructuredCase): string {
  const runReview = asRecord(item.input["run_review"]);
  const stageFacts = Array.isArray(runReview["stage_facts"]) ? runReview["stage_facts"].length : 0;
  const childLanes = Array.isArray(runReview["child_lanes"]) ? runReview["child_lanes"].length : 0;
  const timelineNodeCount = Number(runReview["timeline_node_count"] ?? 0);
  const pieces = [
    stageFacts > 0 ? `${stageFacts} 个阶段` : "",
    childLanes > 0 ? `${childLanes} 条子任务 lane` : "",
    timelineNodeCount > 0 ? `${timelineNodeCount} 个事件` : "",
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

function cssEscape(value: string): string {
  if (typeof CSS !== "undefined" && typeof CSS.escape === "function") {
    return CSS.escape(value);
  }
  return value.replace(/["\\]/g, "\\$&");
}

function FilterButton({
  active,
  onClick,
  href,
  children,
}: {
  active: boolean;
  onClick: () => void;
  href?: string;
  children: ReactNode;
}) {
  const className = `inline-flex h-7 items-center rounded-md border px-2.5 text-xs transition-colors ${
    active ? "border-foreground bg-foreground text-background" : "border-border bg-background text-muted-foreground hover:text-foreground"
  }`;
  if (href) {
    return (
      <AppLink href={href} onClick={onClick} className={className} data-active={active ? "true" : undefined} aria-current={active ? "page" : undefined}>
        {children}
      </AppLink>
    );
  }
  return (
    <button type="button" onClick={onClick} className={className} data-active={active ? "true" : undefined}>
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

type DatasetServerCaseSearchResult = {
  items: PromptEvaluationStructuredCase[];
  total: number;
  totalCount: number;
  limit: number;
  offset: number;
  hasMore: boolean;
  nextCursor: string | null;
  sortBy: PromptEvaluationCaseSortBy;
  sortDirection: "asc" | "desc";
  tagStats: PromptEvaluationCaseTagSummary[];
  executedAt: string;
};

function ManualCasePanel({
  asset,
  cases,
  draft,
  onDraftChange,
  onCreateCase,
  creating,
  focusedCaseId,
  focusedIssueId,
  focusedIssueRunReviewHref,
  onUpdateAsset,
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
  focusedCaseId: string | null;
  focusedIssueId: string | null;
  focusedIssueRunReviewHref: string | null;
  onUpdateAsset: (assetId: string, data: UpdatePromptEvaluationAssetRequest) => Promise<unknown>;
  onUpdateCase: (caseId: string, data: UpdatePromptEvaluationCaseRequest) => Promise<unknown>;
  updatingCaseId: string | null;
  onDeleteCase: (caseId: string) => void;
  deletingCaseId: string | null;
}) {
  const workspaceId = useWorkspaceId();
  const workspacePaths = useWorkspacePaths();
  const queryClient = useQueryClient();
  const manualCases = cases.filter((item) => item.source === "manual");
  const traceCases = cases.filter((item) => item.source === "trace");
  const [caseSourceFilter, setCaseSourceFilter] = useState<"全部" | "手工" | "trace导入" | "资产载荷">("全部");
  const [caseTagFilter, setCaseTagFilter] = useState("全部");
  const [caseKeywordFilter, setCaseKeywordFilter] = useState("");
  const [editingCaseId, setEditingCaseId] = useState<string | null>(null);
  const [editDrafts, setEditDrafts] = useState<Record<string, ManualCaseDraft>>({});
  const [tagEditingCaseId, setTagEditingCaseId] = useState<string | null>(null);
  const [tagEditDrafts, setTagEditDrafts] = useState<Record<string, string>>({});
  const [bulkTagsText, setBulkTagsText] = useState("");
  const [bulkTagMode, setBulkTagMode] = useState<"追加" | "移除" | null>(null);
  const [renameSourceTag, setRenameSourceTag] = useState("");
  const [renameTargetTag, setRenameTargetTag] = useState("");
  const [renamingTag, setRenamingTag] = useState(false);
  const [serverSearchResult, setServerSearchResult] = useState<DatasetServerCaseSearchResult | null>(null);
  const [serverSearchLoading, setServerSearchLoading] = useState(false);
  const [serverCaseSortBy, setServerCaseSortBy] = useState<PromptEvaluationCaseSortBy>("case_index");
  const [serverCaseSortDirection, setServerCaseSortDirection] = useState<"asc" | "desc">("asc");
  const [caseOperations, setCaseOperations] = useState<PromptEvaluationCaseOperation[]>([]);
  const [caseOperationsLoading, setCaseOperationsLoading] = useState(false);
  const [savedFilterName, setSavedFilterName] = useState("");
  const [savingFilter, setSavingFilter] = useState<"保存" | "删除" | null>(null);
  const caseTags = useMemo(() => uniqueSortedStrings(cases.flatMap((item) => item.tags.map((value) => String(value)).filter(Boolean))), [cases]);
  const tagStats = useMemo(() => buildDatasetCaseTagStats(cases), [cases]);
  const savedFilters = useMemo(() => datasetSavedFilters(asset), [asset]);
  const filteredCases = useMemo(() => {
    const keyword = caseKeywordFilter.trim().toLowerCase();
    return cases.filter((item) => {
      const sourceOK = caseSourceFilter === "全部" || caseSourceLabel(item.source) === caseSourceFilter;
      const tagOK = caseTagFilter === "全部" || item.tags.some((value) => String(value) === caseTagFilter);
      const keywordOK = !keyword || datasetCaseSearchText(item).includes(keyword);
      return sourceOK && tagOK && keywordOK;
    });
  }, [caseSourceFilter, caseTagFilter, caseKeywordFilter, cases]);
  const sampleCases = filteredCases.slice(0, 3);
  const downloadDatasetSample = () => {
    const payload = {
      schema_version: "multica.prompt_evaluation.dataset_sample_export.v1",
      导出时间: new Date().toISOString(),
      数据集: {
        id: asset.id,
        名称: asset.name,
        描述: asset.description,
        状态: asset.status,
      },
      筛选条件: {
        来源: caseSourceFilter,
        标签: caseTagFilter,
        关键词: caseKeywordFilter.trim() || "全部",
      },
      统计: {
        总用例数: cases.length,
        命中用例数: filteredCases.length,
        采样预览数: sampleCases.length,
      },
      采样预览: sampleCases.map(datasetCaseExportRow),
      命中用例: filteredCases.map(datasetCaseExportRow),
    };
    downloadTextFile(
      JSON.stringify(payload, null, 2),
      `multica-dataset-sample-${asset.id}-${new Date().toISOString().replace(/[:.]/g, "-")}.json`,
      "application/json;charset=utf-8",
    );
    toast.success(`数据集采样已导出：${filteredCases.length} 条`);
  };
  const runServerCaseSearch = async (cursor?: string | null) => {
    setServerSearchLoading(true);
    try {
      const source = datasetCaseSourceFilterToApiSource(caseSourceFilter);
      const keyword = caseKeywordFilter.trim() || undefined;
      const [result, tagSummaryResult] = await Promise.all([
        api.listPromptEvaluationCases({
          asset_id: asset.id,
          source,
          tag: caseTagFilter === "全部" ? undefined : caseTagFilter,
          keyword,
          limit: 20,
          cursor: cursor || undefined,
          sort_by: serverCaseSortBy,
          sort_direction: serverCaseSortDirection,
        }),
        api.listPromptEvaluationCaseTagSummaries({
          asset_id: asset.id,
          source,
          keyword,
          limit: 12,
        }),
      ]);
      setServerSearchResult((current) => ({
        items: cursor && current ? [...current.items, ...result.items] : result.items,
        total: result.total,
        totalCount: result.total_count,
        limit: result.limit,
        offset: result.offset,
        hasMore: result.has_more,
        nextCursor: result.next_cursor,
        sortBy: result.sort_by,
        sortDirection: result.sort_direction,
        tagStats: tagSummaryResult.items,
        executedAt: new Date().toISOString(),
      }));
      toast.success(cursor ? `已追加 ${result.items.length} 条服务端样本` : `服务端检索返回 ${result.items.length} 条样本`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "服务端检索失败");
    } finally {
      setServerSearchLoading(false);
    }
  };
  const loadCaseOperationHistory = async () => {
    setCaseOperationsLoading(true);
    try {
      const result = await api.listPromptEvaluationCaseOperations(asset.id, { limit: 5 });
      setCaseOperations(result.items);
      toast.success(`已读取 ${result.items.length} 条批量操作审计`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "批量操作审计读取失败");
    } finally {
      setCaseOperationsLoading(false);
    }
  };
  const refreshDatasetQueries = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.cases(workspaceId ?? "") }),
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.assets(workspaceId ?? "") }),
    ]);
  };
  const pollBulkOperation = async (operationId: string, label: string) => {
    for (let attempt = 0; attempt < 30; attempt += 1) {
      await new Promise((resolve) => setTimeout(resolve, 1000));
      const result = await api.listPromptEvaluationCaseOperations(asset.id, { limit: 5 });
      setCaseOperations(result.items);
      const operation = result.items.find((item) => item.id === operationId);
      if (!operation || operation.status === "已入队" || operation.status === "运行中") continue;
      if (operation.status === "已完成") {
        await refreshDatasetQueries();
        toast.success(`${label}已完成：变更 ${operation.changed_count} 条，跳过 ${operation.skipped_count} 条`);
        return;
      }
      toast.error(operation.error_message || `${label}失败`);
      return;
    }
    toast.message(`${label}仍在后台执行，可稍后查看审计历史`);
  };
  const updateFilteredCaseTags = async (mode: "追加" | "移除") => {
    const targetTags = splitList(bulkTagsText);
    if (targetTags.length === 0) {
      toast.error("请先输入要批量处理的标签");
      return;
    }
    if (filteredCases.length === 0) {
      toast.error("当前筛选没有命中用例");
      return;
    }
    setBulkTagMode(mode);
    try {
      const result = await api.bulkUpdatePromptEvaluationCaseTags({
        asset_id: asset.id,
        source: datasetCaseSourceFilterToApiSource(caseSourceFilter),
        tag: caseTagFilter === "全部" ? undefined : caseTagFilter,
        keyword: caseKeywordFilter.trim() || undefined,
        tags: targetTags,
        mode,
        execution_mode: "后台",
        limit: 500,
      });
      setCaseOperations((current) => [result.operation, ...current.filter((item) => item.id !== result.operation.id)].slice(0, 5));
      toast.success(`已提交后台${mode}标签操作，审计已记录`);
      void pollBulkOperation(result.operation.id, `后台${mode}标签`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "批量标签处理失败");
    } finally {
      setBulkTagMode(null);
    }
  };
  const renameDatasetTag = async () => {
    const sourceTag = renameSourceTag.trim();
    const targetTag = renameTargetTag.trim();
    if (!sourceTag) {
      toast.error("请先选择要整理的原标签");
      return;
    }
    if (!targetTag) {
      toast.error("请先输入整理后的新标签");
      return;
    }
    if (sourceTag === targetTag) {
      toast.error("新标签不能和原标签相同");
      return;
    }
    const matchingCases = cases.filter((item) => item.tags.some((tag) => String(tag) === sourceTag));
    if (matchingCases.length === 0) {
      toast.error("当前数据集没有使用这个标签的用例");
      return;
    }
    setRenamingTag(true);
    try {
      const result = await api.bulkUpdatePromptEvaluationCaseTags({
        asset_id: asset.id,
        source_tag: sourceTag,
        target_tag: targetTag,
        mode: "重命名",
        execution_mode: "后台",
        limit: 500,
      });
      setCaseOperations((current) => [result.operation, ...current.filter((item) => item.id !== result.operation.id)].slice(0, 5));
      if (caseTagFilter === sourceTag) setCaseTagFilter(targetTag);
      setRenameSourceTag(targetTag);
      setRenameTargetTag("");
      toast.success("已提交后台标签整理操作，审计已记录");
      void pollBulkOperation(result.operation.id, "后台标签整理");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "标签整理失败");
    } finally {
      setRenamingTag(false);
    }
  };
  const saveCurrentFilter = async () => {
    const name = savedFilterName.trim() || `筛选方案 ${savedFilters.length + 1}`;
    const filter: DatasetSavedFilter = {
      id: `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
      name,
      source_filter: caseSourceFilter,
      tag_filter: caseTagFilter,
      keyword_filter: caseKeywordFilter.trim(),
      created_at: new Date().toISOString(),
    };
    const nextFilters = [filter, ...savedFilters.filter((item) => item.name !== name)].slice(0, 12);
    setSavingFilter("保存");
    try {
      await onUpdateAsset(asset.id, { payload: datasetPayloadWithSavedFilters(asset, nextFilters, cases) });
      setSavedFilterName("");
      toast.success(`筛选方案已保存：${name}`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "筛选方案保存失败");
    } finally {
      setSavingFilter(null);
    }
  };
  const applySavedFilter = (filter: DatasetSavedFilter) => {
    setCaseSourceFilter(filter.source_filter);
    setCaseTagFilter(filter.tag_filter || "全部");
    setCaseKeywordFilter(filter.keyword_filter || "");
  };
  const deleteSavedFilter = async (filter: DatasetSavedFilter) => {
    setSavingFilter("删除");
    try {
      await onUpdateAsset(asset.id, { payload: datasetPayloadWithSavedFilters(asset, savedFilters.filter((item) => item.id !== filter.id), cases) });
      toast.success(`筛选方案已删除：${filter.name}`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "筛选方案删除失败");
    } finally {
      setSavingFilter(null);
    }
  };
  return (
    <div data-testid={`prompt-evaluation-cases-${asset.id}`} className="md:col-span-2 grid gap-2 rounded-md border border-border/70 bg-muted/10 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-xs font-medium text-muted-foreground">结构化评测用例</div>
        <Badge variant="outline" className="text-[11px]">
          手工 {manualCases.length} · trace {traceCases.length} · draft {cases.filter((item) => item.status === "draft").length} · approved {cases.filter((item) => item.status === "approved").length} · active {cases.filter((item) => item.status === "active" || item.status === "启用").length}
        </Badge>
      </div>
      {asset.asset_type === "数据集" && (
        <DatasetCaseGovernanceBar
          assetId={asset.id}
          totalCount={cases.length}
          visibleCount={filteredCases.length}
          tags={caseTags}
          sourceFilter={caseSourceFilter}
          onSourceFilterChange={setCaseSourceFilter}
          tagFilter={caseTagFilter}
          onTagFilterChange={setCaseTagFilter}
          keywordFilter={caseKeywordFilter}
          onKeywordFilterChange={setCaseKeywordFilter}
          tagStats={tagStats}
          serverTagStats={serverSearchResult?.tagStats ?? []}
          caseOperations={caseOperations}
          caseOperationsLoading={caseOperationsLoading}
          onLoadCaseOperations={loadCaseOperationHistory}
          serverSearchResult={serverSearchResult}
          serverSearchLoading={serverSearchLoading}
          serverCaseSortBy={serverCaseSortBy}
          onServerCaseSortByChange={setServerCaseSortBy}
          serverCaseSortDirection={serverCaseSortDirection}
          onServerCaseSortDirectionChange={setServerCaseSortDirection}
          onServerSearch={runServerCaseSearch}
          bulkTagsText={bulkTagsText}
          onBulkTagsTextChange={setBulkTagsText}
          bulkTagMode={bulkTagMode}
          onBulkTagUpdate={updateFilteredCaseTags}
          renameSourceTag={renameSourceTag}
          onRenameSourceTagChange={setRenameSourceTag}
          renameTargetTag={renameTargetTag}
          onRenameTargetTagChange={setRenameTargetTag}
          renamingTag={renamingTag}
          onRenameDatasetTag={renameDatasetTag}
          savedFilterName={savedFilterName}
          onSavedFilterNameChange={setSavedFilterName}
          savedFilters={savedFilters}
          savingFilter={savingFilter}
          onSaveCurrentFilter={saveCurrentFilter}
          onApplySavedFilter={applySavedFilter}
          onDeleteSavedFilter={deleteSavedFilter}
          samples={sampleCases}
          onDownloadSample={downloadDatasetSample}
        />
      )}
      {cases.length > 0 ? (
        <div className="grid gap-1.5">
          {filteredCases.length === 0 ? (
            <div className="rounded border border-dashed px-2 py-2 text-xs text-muted-foreground" data-testid={`dataset-case-filter-empty-${asset.id}`}>
              当前筛选没有命中用例，请切换来源或标签。
            </div>
          ) : filteredCases.map((item) => {
            const editing = editingCaseId === item.id;
            const editDraft = editDrafts[item.id] ?? manualCaseToDraft(item);
            const focused = focusedCaseId === item.id;
            const sourceIssueId = issueIdFromStructuredCase(item) || focusedIssueId;
            const runReviewHref = sourceIssueId
              ? sourceIssueId === focusedIssueId && focusedIssueRunReviewHref
                ? focusedIssueRunReviewHref
                : `${workspacePaths.runReviews()}?issue=${encodeURIComponent(sourceIssueId)}`
              : null;
            const validationSummary = caseValidationSummary(item);
            const evidenceSummary = caseEvidenceSummary(item);
            return (
              <div
                key={item.id}
                data-testid={`prompt-evaluation-case-${item.id}`}
                className={`grid gap-2 rounded px-2 py-1.5 text-xs ${focused ? "border border-info/60 bg-info/5 ring-1 ring-info/40" : "border bg-background"}`}
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium text-foreground">{item.case_name || `用例 ${item.case_index + 1}`}</span>
                  <span className="text-muted-foreground">{caseSourceLabel(item.source)}</span>
                  <Badge variant={item.status === "active" || item.status === "启用" ? "secondary" : "outline"} className="text-[11px]">
                    {caseReviewStatusLabel(item.status)}
                  </Badge>
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">{summarizeStructuredCase(item)}</span>
                  {item.source === "manual" && (
                    <>
                      {item.status === "draft" && (
                        <Button
                          size="sm"
                          variant="secondary"
                          className="h-7"
                          data-testid={`approve-eval-case-${item.id}`}
                          onClick={() => onUpdateCase(item.id, { status: "approved" })}
                          disabled={updatingCaseId === item.id}
                        >
                          批准 Draft
                        </Button>
                      )}
                      {item.status === "approved" && (
                        <Button
                          size="sm"
                          variant="secondary"
                          className="h-7"
                          data-testid={`activate-eval-case-${item.id}`}
                          onClick={() => onUpdateCase(item.id, { status: "active" })}
                          disabled={updatingCaseId === item.id}
                        >
                          激活评测
                        </Button>
                      )}
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
                  {asset.asset_type === "数据集" && item.source !== "manual" && (
                    <Button
                      size="sm"
                      variant="secondary"
                      className="h-7"
                      onClick={() => {
                        setTagEditingCaseId(item.id);
                        setTagEditDrafts((prev) => ({ ...prev, [item.id]: item.tags.map((value) => String(value)).join(", ") }));
                      }}
                    >
                      编辑标签
                    </Button>
                  )}
                </div>
                {(sourceIssueId || validationSummary || evidenceSummary) && (
                  <div
                    className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-sm border border-border/70 bg-muted/20 px-2 py-1.5 text-[11px] text-muted-foreground"
                    data-testid={`prompt-evaluation-case-source-${item.id}`}
                  >
                    {sourceIssueId && (
                      <span>
                        来源 issue <span className="font-medium text-foreground">{shortId(sourceIssueId)}</span>
                      </span>
                    )}
                    {runReviewHref && (
                      <AppLink href={runReviewHref} className="font-medium text-primary underline-offset-2 hover:underline">
                        查看运行复盘
                      </AppLink>
                    )}
                    {validationSummary && <span>验证：{validationSummary}</span>}
                    {evidenceSummary && <span>证据：{evidenceSummary}</span>}
                  </div>
                )}
                {tagEditingCaseId === item.id && (
                  <div className="flex flex-wrap items-center gap-2 rounded-sm border border-border/70 bg-muted/20 p-2" data-testid={`dataset-case-tag-editor-${item.id}`}>
                    <Input
                      value={tagEditDrafts[item.id] ?? item.tags.map((value) => String(value)).join(", ")}
                      onChange={(event) => setTagEditDrafts((prev) => ({ ...prev, [item.id]: event.target.value }))}
                      placeholder="编辑数据集标签"
                      aria-label="编辑数据集标签"
                      className="h-9 min-w-52 flex-1 text-xs"
                    />
                    <Button
                      size="sm"
                      className="h-9 shrink-0"
                      onClick={() => {
                        void onUpdateCase(item.id, buildCaseTagUpdateRequest(asset, item, tagEditDrafts[item.id] ?? ""));
                        setTagEditingCaseId(null);
                      }}
                      disabled={updatingCaseId === item.id}
                    >
                      {updatingCaseId === item.id ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                      保存标签
                    </Button>
                    <Button size="sm" variant="ghost" className="h-9 shrink-0" onClick={() => setTagEditingCaseId(null)}>
                      取消
                    </Button>
                  </div>
                )}
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
                      placeholder="编辑变量：任务标题=登录失败"
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
                          void onUpdateCase(item.id, buildManualCaseUpdateRequest(asset, item, editDraft));
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
          placeholder="变量：任务标题=登录失败"
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
            placeholder="标签：账号系统, 回归"
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

function DatasetCaseGovernanceBar({
  assetId,
  totalCount,
  visibleCount,
  tags,
  sourceFilter,
  onSourceFilterChange,
  tagFilter,
  onTagFilterChange,
  keywordFilter,
  onKeywordFilterChange,
  tagStats,
  serverTagStats,
  caseOperations,
  caseOperationsLoading,
  onLoadCaseOperations,
  serverSearchResult,
  serverSearchLoading,
  serverCaseSortBy,
  onServerCaseSortByChange,
  serverCaseSortDirection,
  onServerCaseSortDirectionChange,
  onServerSearch,
  bulkTagsText,
  onBulkTagsTextChange,
  bulkTagMode,
  onBulkTagUpdate,
  renameSourceTag,
  onRenameSourceTagChange,
  renameTargetTag,
  onRenameTargetTagChange,
  renamingTag,
  onRenameDatasetTag,
  savedFilterName,
  onSavedFilterNameChange,
  savedFilters,
  savingFilter,
  onSaveCurrentFilter,
  onApplySavedFilter,
  onDeleteSavedFilter,
  samples,
  onDownloadSample,
}: {
  assetId: string;
  totalCount: number;
  visibleCount: number;
  tags: string[];
  sourceFilter: "全部" | "手工" | "trace导入" | "资产载荷";
  onSourceFilterChange: (value: "全部" | "手工" | "trace导入" | "资产载荷") => void;
  tagFilter: string;
  onTagFilterChange: (value: string) => void;
  keywordFilter: string;
  onKeywordFilterChange: (value: string) => void;
  tagStats: Array<{ tag: string; count: number }>;
  serverTagStats: PromptEvaluationCaseTagSummary[];
  caseOperations: PromptEvaluationCaseOperation[];
  caseOperationsLoading: boolean;
  onLoadCaseOperations: () => void;
  serverSearchResult: DatasetServerCaseSearchResult | null;
  serverSearchLoading: boolean;
  serverCaseSortBy: PromptEvaluationCaseSortBy;
  onServerCaseSortByChange: (value: PromptEvaluationCaseSortBy) => void;
  serverCaseSortDirection: "asc" | "desc";
  onServerCaseSortDirectionChange: (value: "asc" | "desc") => void;
  onServerSearch: (cursor?: string | null) => void;
  bulkTagsText: string;
  onBulkTagsTextChange: (value: string) => void;
  bulkTagMode: "追加" | "移除" | null;
  onBulkTagUpdate: (mode: "追加" | "移除") => void;
  renameSourceTag: string;
  onRenameSourceTagChange: (value: string) => void;
  renameTargetTag: string;
  onRenameTargetTagChange: (value: string) => void;
  renamingTag: boolean;
  onRenameDatasetTag: () => void;
  savedFilterName: string;
  onSavedFilterNameChange: (value: string) => void;
  savedFilters: DatasetSavedFilter[];
  savingFilter: "保存" | "删除" | null;
  onSaveCurrentFilter: () => void;
  onApplySavedFilter: (filter: DatasetSavedFilter) => void;
  onDeleteSavedFilter: (filter: DatasetSavedFilter) => void;
  samples: PromptEvaluationStructuredCase[];
  onDownloadSample: () => void;
}) {
  return (
    <section className="grid gap-2 rounded-md border bg-background px-3 py-2 text-xs" data-testid={`dataset-case-governance-${assetId}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="font-medium text-foreground">数据集用例治理</div>
          <div className="mt-0.5 text-muted-foreground">按来源和标签筛选题库，并抽样预览当前将进入版本快照的样本。</div>
        </div>
        <Badge variant="outline" data-testid={`dataset-case-filter-count-${assetId}`}>
          命中 {visibleCount} / {totalCount}
        </Badge>
        <Button
          size="sm"
          variant="secondary"
          className="h-8"
          data-testid={`download-dataset-sample-${assetId}`}
          onClick={onDownloadSample}
          disabled={visibleCount === 0}
        >
          <Download className="size-3.5" />
          下载当前采样
        </Button>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-muted-foreground">来源</span>
        {(["全部", "手工", "trace导入", "资产载荷"] as const).map((source) => (
          <FilterButton key={source} active={sourceFilter === source} onClick={() => onSourceFilterChange(source)}>
            {source}
          </FilterButton>
        ))}
        <span className="ml-1 text-muted-foreground">标签</span>
        <select
          aria-label="筛选数据集用例标签"
          className="h-8 rounded-md border bg-background px-2 text-xs"
          value={tagFilter}
          onChange={(event) => onTagFilterChange(event.target.value)}
        >
          <option value="全部">全部标签</option>
          {tags.map((tag) => (
            <option key={tag} value={tag}>{tag}</option>
          ))}
        </select>
        <Input
          value={keywordFilter}
          onChange={(event) => onKeywordFilterChange(event.target.value)}
          placeholder="搜索名称、变量、期望或标签"
          aria-label="筛选数据集用例关键词"
          className="h-8 min-w-60 flex-1 text-xs"
        />
      </div>
      {tagStats.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5" data-testid={`dataset-case-tag-stats-${assetId}`}>
          <span className="mr-1 text-[11px] font-medium text-muted-foreground">标签统计</span>
          {tagStats.slice(0, 8).map((item) => (
            <button
              key={item.tag}
              type="button"
              className={`rounded-md border px-2 py-1 text-[11px] ${
                tagFilter === item.tag ? "border-foreground bg-foreground text-background" : "bg-muted/20 text-muted-foreground hover:text-foreground"
              }`}
              onClick={() => onTagFilterChange(item.tag)}
            >
              {item.tag} {item.count}
            </button>
          ))}
        </div>
      )}
      {serverTagStats.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5" data-testid={`dataset-case-server-tag-stats-${assetId}`}>
          <span className="mr-1 text-[11px] font-medium text-muted-foreground">服务端标签统计</span>
          {serverTagStats.slice(0, 8).map((item) => (
            <button
              key={item.tag}
              type="button"
              className={`rounded-md border px-2 py-1 text-[11px] ${
                tagFilter === item.tag ? "border-foreground bg-foreground text-background" : "bg-background text-muted-foreground hover:text-foreground"
              }`}
              onClick={() => onTagFilterChange(item.tag)}
            >
              {item.tag} {item.case_count}
            </button>
          ))}
        </div>
      )}
      <div className="grid gap-1.5 rounded-md border border-dashed bg-muted/10 px-2 py-2" data-testid={`dataset-case-server-search-${assetId}`}>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[11px] font-medium text-muted-foreground">服务端检索</span>
          <span className="text-muted-foreground">按当前来源、标签和关键词从服务端分页读取样本。</span>
          <select
            aria-label="服务端检索排序字段"
            className="h-8 rounded-md border bg-background px-2 text-xs"
            value={serverCaseSortBy}
            onChange={(event) => onServerCaseSortByChange(event.target.value as PromptEvaluationCaseSortBy)}
            data-testid={`dataset-case-server-sort-by-${assetId}`}
          >
            <option value="case_index">用例序号</option>
            <option value="case_name">用例名称</option>
            <option value="source">来源</option>
            <option value="created_at">创建时间</option>
            <option value="updated_at">更新时间</option>
          </select>
          <select
            aria-label="服务端检索排序方向"
            className="h-8 rounded-md border bg-background px-2 text-xs"
            value={serverCaseSortDirection}
            onChange={(event) => onServerCaseSortDirectionChange(event.target.value as "asc" | "desc")}
            data-testid={`dataset-case-server-sort-direction-${assetId}`}
          >
            <option value="asc">升序</option>
            <option value="desc">降序</option>
          </select>
          <Button
            size="sm"
            variant="secondary"
            className="h-8"
            onClick={() => onServerSearch()}
            disabled={serverSearchLoading}
            data-testid={`dataset-case-server-search-button-${assetId}`}
          >
            {serverSearchLoading ? <Loader2 className="size-3.5 animate-spin" /> : <Search className="size-3.5" />}
            服务端检索当前筛选
          </Button>
        </div>
        {serverSearchResult && (
          <div className="grid gap-1 text-muted-foreground" data-testid={`dataset-case-server-search-result-${assetId}`}>
            <div>
              已加载 {serverSearchResult.items.length} / {serverSearchResult.totalCount} 条 · 本页 {serverSearchResult.limit} 条 · offset {serverSearchResult.offset}
              · {serverSearchResult.sortBy}/{serverSearchResult.sortDirection} · 记录时间 {serverSearchResult.executedAt}
            </div>
            {serverSearchResult.items.length === 0 ? (
              <div className="rounded border border-dashed px-2 py-2">当前服务端筛选没有命中样本。</div>
            ) : (
              <>
                <div className="flex flex-wrap gap-1.5">
                  {serverSearchResult.items.slice(0, 8).map((item) => (
                    <span key={item.id} className="rounded-md border bg-background px-2 py-1">
                      {item.case_name || `用例 ${item.case_index + 1}`} · {caseSourceLabel(item.source)}
                    </span>
                  ))}
                </div>
                <Button
                  size="sm"
                  variant="outline"
                  className="h-8 w-fit"
                  onClick={() => onServerSearch(serverSearchResult.nextCursor)}
                  disabled={serverSearchLoading || !serverSearchResult.hasMore || !serverSearchResult.nextCursor}
                  data-testid={`dataset-case-server-load-more-${assetId}`}
                >
                  {serverSearchLoading ? <Loader2 className="size-3.5 animate-spin" /> : <Search className="size-3.5" />}
                  加载下一页
                </Button>
              </>
            )}
          </div>
        )}
      </div>
      <div className="grid gap-2 rounded-md border border-dashed bg-muted/10 px-2 py-2" data-testid={`dataset-case-saved-filters-${assetId}`}>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[11px] font-medium text-muted-foreground">筛选方案</span>
          <Input
            value={savedFilterName}
            onChange={(event) => onSavedFilterNameChange(event.target.value)}
            placeholder="方案名称"
            aria-label="数据集筛选方案名称"
            className="h-8 min-w-48 flex-1 text-xs"
          />
          <Button
            size="sm"
            variant="secondary"
            className="h-8"
            onClick={onSaveCurrentFilter}
            disabled={savingFilter !== null}
            data-testid={`dataset-case-save-filter-${assetId}`}
          >
            {savingFilter === "保存" ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
            保存当前筛选
          </Button>
        </div>
        {savedFilters.length > 0 && (
          <div className="flex flex-wrap items-center gap-1.5">
            {savedFilters.map((filter) => (
              <span key={filter.id} className="inline-flex items-center gap-1 rounded-md border bg-background px-1.5 py-1 text-[11px]">
                <button
                  type="button"
                  className="font-medium text-foreground hover:underline"
                  onClick={() => onApplySavedFilter(filter)}
                  data-testid={`dataset-case-apply-filter-${assetId}-${filter.id}`}
                >
                  {filter.name}
                </button>
                <span className="text-muted-foreground">
                  {filter.source_filter}/{filter.tag_filter || "全部"}/{filter.keyword_filter || "无关键词"}
                </span>
                <button
                  type="button"
                  className="rounded px-1 text-muted-foreground hover:bg-muted hover:text-foreground"
                  aria-label={`删除筛选方案 ${filter.name}`}
                  onClick={() => onDeleteSavedFilter(filter)}
                  disabled={savingFilter !== null}
                >
                  <Trash2 className="size-3" />
                </button>
              </span>
            ))}
          </div>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2 rounded-md border border-dashed bg-muted/10 px-2 py-2" data-testid={`dataset-case-bulk-tags-${assetId}`}>
        <span className="text-[11px] font-medium text-muted-foreground">批量标签</span>
        <Input
          value={bulkTagsText}
          onChange={(event) => onBulkTagsTextChange(event.target.value)}
          placeholder="输入标签，用逗号分隔"
          aria-label="批量处理数据集用例标签"
          className="h-8 min-w-56 flex-1 text-xs"
        />
        <Button
          size="sm"
          variant="secondary"
          className="h-8"
          onClick={() => onBulkTagUpdate("追加")}
          disabled={visibleCount === 0 || bulkTagMode !== null || bulkTagsText.trim() === ""}
          data-testid={`dataset-case-bulk-add-tags-${assetId}`}
        >
          {bulkTagMode === "追加" ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
          追加到当前筛选
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="h-8"
          onClick={() => onBulkTagUpdate("移除")}
          disabled={visibleCount === 0 || bulkTagMode !== null || bulkTagsText.trim() === ""}
          data-testid={`dataset-case-bulk-remove-tags-${assetId}`}
        >
          {bulkTagMode === "移除" ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
          从当前筛选移除
        </Button>
      </div>
      <div className="grid gap-1.5 rounded-md border border-dashed bg-muted/10 px-2 py-2" data-testid={`dataset-case-operation-audit-${assetId}`}>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[11px] font-medium text-muted-foreground">批量操作审计</span>
          {caseOperationsLoading && <Loader2 className="size-3.5 animate-spin text-muted-foreground" />}
          <span className="text-muted-foreground">记录最近服务端批量标签操作的筛选条件、输入和变更数量。</span>
          <Button
            size="sm"
            variant="secondary"
            className="h-8"
            onClick={onLoadCaseOperations}
            disabled={caseOperationsLoading}
            data-testid={`dataset-case-load-operation-audit-${assetId}`}
          >
            {caseOperationsLoading ? <Loader2 className="size-3.5 animate-spin" /> : <Search className="size-3.5" />}
            查看审计历史
          </Button>
        </div>
        {caseOperations.length === 0 ? (
          <div className="rounded border border-dashed px-2 py-2 text-muted-foreground">暂无批量操作记录。</div>
        ) : (
          <div className="grid gap-1">
            {caseOperations.slice(0, 3).map((operation) => (
              <div key={operation.id} className="rounded border bg-background px-2 py-1.5" data-testid={`dataset-case-operation-audit-row-${assetId}-${operation.id}`}>
                <span className="font-medium text-foreground">{operation.operation_type}</span>
                <span className="ml-2 text-muted-foreground">
                  {operation.status} · 变更 {operation.changed_count} · 跳过 {operation.skipped_count} · {operation.created_at}
                </span>
                <div className="mt-1 truncate text-muted-foreground">
                  筛选 {summarizeJSONValue(operation.filter)} · 输入 {summarizeJSONValue(operation.input)}
                </div>
                {operation.error_message && <div className="mt-1 text-destructive">{operation.error_message}</div>}
              </div>
            ))}
          </div>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2 rounded-md border border-dashed bg-muted/10 px-2 py-2" data-testid={`dataset-case-rename-tags-${assetId}`}>
        <span className="text-[11px] font-medium text-muted-foreground">标签整理</span>
        <select
          aria-label="选择要整理的数据集标签"
          className="h-8 rounded-md border bg-background px-2 text-xs"
          value={renameSourceTag}
          onChange={(event) => onRenameSourceTagChange(event.target.value)}
        >
          <option value="">选择原标签</option>
          {tags.map((tag) => (
            <option key={tag} value={tag}>{tag}</option>
          ))}
        </select>
        <Input
          value={renameTargetTag}
          onChange={(event) => onRenameTargetTagChange(event.target.value)}
          placeholder="新标签；已存在则合并"
          aria-label="输入整理后的数据集标签"
          className="h-8 min-w-56 flex-1 text-xs"
        />
        <Button
          size="sm"
          variant="secondary"
          className="h-8"
          onClick={onRenameDatasetTag}
          disabled={renamingTag || renameSourceTag.trim() === "" || renameTargetTag.trim() === ""}
          data-testid={`dataset-case-rename-tag-${assetId}`}
        >
          {renamingTag ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
          重命名或合并标签
        </Button>
      </div>
      <div className="grid gap-1.5" data-testid={`dataset-case-sampling-preview-${assetId}`}>
        <div className="text-[11px] font-medium text-muted-foreground">采样预览</div>
        {samples.length === 0 ? (
          <div className="rounded border border-dashed px-2 py-2 text-muted-foreground">当前筛选没有可预览样本。</div>
        ) : samples.map((item, index) => (
          <div key={item.id} className="rounded border bg-muted/20 px-2 py-1.5">
            <span className="font-medium text-foreground">样本 {index + 1}：{item.case_name || `用例 ${item.case_index + 1}`}</span>
            <span className="ml-2 text-muted-foreground">{caseSourceLabel(item.source)} · {item.tags.map(String).join("、") || "无标签"}</span>
          </div>
        ))}
      </div>
    </section>
  );
}

function datasetCaseExportRow(item: PromptEvaluationStructuredCase) {
  return {
    id: item.id,
    名称: item.case_name || `用例 ${item.case_index + 1}`,
    序号: item.case_index,
    来源: caseSourceLabel(item.source),
    状态: item.status,
    标签: item.tags,
    变量: item.variables,
    期望包含: item.expected_contains,
    输入: item.input,
    期望: item.expected,
  };
}

function datasetCaseSearchText(item: PromptEvaluationStructuredCase) {
  return [
    item.case_name,
    item.status,
    caseSourceLabel(item.source),
    ...item.tags.map(String),
    summarizeStructuredCase(item),
    summarizeJSONValue(item.variables),
    summarizeJSONValue(item.expected_contains),
    summarizeJSONValue(item.input),
    summarizeJSONValue(item.expected),
  ].join(" ").toLowerCase();
}

function buildDatasetCaseTagStats(cases: PromptEvaluationStructuredCase[]) {
  const counts = new Map<string, number>();
  for (const item of cases) {
    for (const tag of item.tags.map((value) => String(value)).filter(Boolean)) {
      counts.set(tag, (counts.get(tag) ?? 0) + 1);
    }
  }
  return [...counts.entries()]
    .map(([tag, count]) => ({ tag, count }))
    .sort((a, b) => b.count - a.count || a.tag.localeCompare(b.tag, "zh-Hans"));
}

type DatasetCaseSourceFilter = "全部" | "手工" | "trace导入" | "资产载荷";

function datasetCaseSourceFilterToApiSource(value: DatasetCaseSourceFilter): "manual" | "trace" | "payload" | undefined {
  if (value === "手工") return "manual";
  if (value === "trace导入") return "trace";
  if (value === "资产载荷") return "payload";
  return undefined;
}

type DatasetSavedFilter = {
  id: string;
  name: string;
  source_filter: DatasetCaseSourceFilter;
  tag_filter: string;
  keyword_filter: string;
  created_at: string;
};

function datasetSavedFilters(asset: PromptEvaluationAsset): DatasetSavedFilter[] {
  const payload = isRecord(asset.payload) ? asset.payload : {};
  const raw = payload["数据集筛选方案"];
  if (!Array.isArray(raw)) return [];
  return raw.flatMap((item, index) => {
    if (!isRecord(item)) return [];
    const source = datasetCaseSourceFilterFromUnknown(item.source_filter ?? item["来源"]);
    const name = stringFromUnknown(item.name ?? item["名称"]).trim();
    if (!name) return [];
    return [{
      id: stringFromUnknown(item.id).trim() || `saved-filter-${index}`,
      name,
      source_filter: source,
      tag_filter: stringFromUnknown(item.tag_filter ?? item["标签"]).trim() || "全部",
      keyword_filter: stringFromUnknown(item.keyword_filter ?? item["关键词"]).trim(),
      created_at: stringFromUnknown(item.created_at ?? item["创建时间"]).trim(),
    }];
  }).slice(0, 12);
}

function datasetPayloadWithSavedFilters(asset: PromptEvaluationAsset, filters: DatasetSavedFilter[], cases: PromptEvaluationStructuredCase[]) {
  const payload = isRecord(asset.payload) ? { ...asset.payload } : {};
  payload["数据集筛选方案"] = filters.map((filter) => ({
    id: filter.id,
    name: filter.name,
    source_filter: filter.source_filter,
    tag_filter: filter.tag_filter,
    keyword_filter: filter.keyword_filter,
    created_at: filter.created_at,
  }));
  const payloadCases = cases.filter((item) => item.source === "payload");
  if (payloadCases.length > 0) {
    payload.cases = payloadCases.map(datasetPayloadCaseFromStructuredCase);
  }
  return payload;
}

function datasetPayloadCaseFromStructuredCase(item: PromptEvaluationStructuredCase) {
  return {
    case_name: item.case_name,
    variables: item.variables,
    expected_contains: item.expected_contains,
    input: item.input,
    expected: item.expected,
    tags: item.tags,
    status: item.status,
  };
}

function datasetCaseSourceFilterFromUnknown(value: unknown): DatasetCaseSourceFilter {
  if (value === "手工" || value === "trace导入" || value === "资产载荷") return value;
  return "全部";
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="grid gap-1.5 text-sm">
      <span className="text-xs font-medium text-muted-foreground">{label}</span>
      {children}
    </label>
  );
}

function tabToAssetType(tab: WorkbenchTab): PromptEvaluationAssetType | null {
  if (tab === "数据集" || tab === "测试套件") return tab;
  return null;
}

function canManageStructuredCases(asset: PromptEvaluationAsset): boolean {
  return asset.asset_type === "数据集" || asset.asset_type === "测试套件";
}

function caseSourceLabel(source: string): string {
  if (source === "manual") return "手工";
  if (source === "trace") return "trace导入";
  return "资产载荷";
}

function caseReviewStatusLabel(status: string): string {
  if (status === "draft") return "待确认";
  if (status === "approved") return "已批准";
  if (status === "active" || status === "启用") return "已激活";
  if (status === "归档") return "已归档";
  return status;
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
  const variables = parseDebugValues(draft.variablesText);
  const expectedContains = splitList(draft.expectedText);
  const skillScenario = isSkillScenarioPayload(asset.payload) ? asset.payload : null;
  return {
    asset_id: asset.id,
    prompt_id: asset.prompt_id,
    case_index: existingCount,
    case_name: draft.caseName.trim(),
    variables,
    expected_contains: expectedContains,
    input: {
      变量: variables,
      来源: "训练与评估手工用例",
      ...(skillScenario ? { skill_scenario: {
        target: skillScenario.target,
        scenario: skillScenario.scenario,
        rubric: skillScenario.rubric,
      } } : {}),
    },
    expected: {
      期望包含: expectedContains,
      ...(skillScenario ? { skill_scenario: {
        rubric_keys: skillScenario.rubric.map((item) => item.key),
        target_skill_path: skillScenario.target.skill_path,
      } } : {}),
    },
    tags: splitList(draft.tagsText),
    status: "启用",
  };
}

function buildManualCaseUpdateRequest(asset: PromptEvaluationAsset, item: PromptEvaluationStructuredCase, draft: ManualCaseDraft): UpdatePromptEvaluationCaseRequest {
  const variables = parseDebugValues(draft.variablesText);
  const expectedContains = splitList(draft.expectedText);
  const skillScenario = isSkillScenarioPayload(asset.payload) ? asset.payload : null;
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
      ...(skillScenario ? { skill_scenario: {
        target: skillScenario.target,
        scenario: skillScenario.scenario,
        rubric: skillScenario.rubric,
      } } : {}),
    },
    expected: {
      期望包含: expectedContains,
      ...(skillScenario ? { skill_scenario: {
        rubric_keys: skillScenario.rubric.map((entry) => entry.key),
        target_skill_path: skillScenario.target.skill_path,
      } } : {}),
    },
    tags: splitList(draft.tagsText),
    status: item.status,
  };
}

function buildCaseTagUpdateRequest(asset: PromptEvaluationAsset, item: PromptEvaluationStructuredCase, tagsText: string): UpdatePromptEvaluationCaseRequest {
  return {
    asset_id: asset.id,
    prompt_id: item.prompt_id ?? asset.prompt_id,
    case_index: item.case_index,
    case_name: item.case_name,
    tags: splitList(tagsText),
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

type CaseSummary = {
  total: number;
  manual: number;
  payload: number;
  trace: number;
};

function buildCaseSummaries(cases: PromptEvaluationStructuredCase[]): Map<string, CaseSummary> {
  const counts = new Map<string, CaseSummary>();
  for (const item of cases) {
    const current = counts.get(item.asset_id) ?? { total: 0, manual: 0, payload: 0, trace: 0 };
    current.total += 1;
    if (item.source === "manual") {
      current.manual += 1;
    } else if (item.source === "trace") {
      current.trace += 1;
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

function uniqueSortedStrings(values: string[]): string[] {
  return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean))).sort((a, b) => a.localeCompare(b, "zh-CN"));
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

function summarizeJSONValue(value: unknown): string {
  if (!value || (typeof value === "object" && !Array.isArray(value) && Object.keys(value as Record<string, unknown>).length === 0)) {
    return "无额外配置";
  }
  const text = JSON.stringify(value);
  if (!text) return "无额外配置";
  return text.length > 120 ? `${text.slice(0, 117)}...` : text;
}

function canGenerateOptimizationCandidate(run: PromptEvaluationRun): boolean {
  if (!run.prompt_id) return false;
  if (run.failed_cases > 0) return true;
  if (run.status === "未通过" || run.status === "失败") return true;
  return Boolean(run.failure_reason && run.failure_reason !== "无");
}

function canCancelPromptEvaluationRun(run: PromptEvaluationRun): boolean {
  return run.status === "已入队" || run.status === "运行中";
}

function canReviewPromptEvaluationRun(run: PromptEvaluationRun): boolean {
  return run.status === "需人工复核";
}

function summarizeAssetPayload(asset: PromptEvaluationAsset, caseSummary?: CaseSummary): string {
  const payload = asset.payload ?? {};
  const cases = Array.isArray(payload.cases) ? payload.cases.length : Array.isArray(payload["数据集"]) ? payload["数据集"].length : 0;
  const skillTarget = summarizeSkillScenarioTarget(asset);
  if (skillTarget) return `Skill 场景评测 · ${skillTarget}`;
  const writingBenchmark = summarizeWritingModelBenchmark(asset);
  if (writingBenchmark) return `多模型写作评测 · ${writingBenchmark}`;
  if (caseSummary && caseSummary.total > 0) {
    const sourceParts = [];
    if (caseSummary.manual > 0) sourceParts.push(`手工 ${caseSummary.manual}`);
    if (caseSummary.trace > 0) sourceParts.push(`trace导入 ${caseSummary.trace}`);
    if (caseSummary.payload > 0) sourceParts.push(`资产载荷 ${caseSummary.payload}`);
    return `结构化用例 ${caseSummary.total} 个${sourceParts.length > 0 ? `（${sourceParts.join("，")}；运行优先使用）` : ""}`;
  }
  if (payload["最近Agent运行"]) return "包含真实智能体运行";
  if (payload["运行结果"]) return "包含运行结果";
  return cases > 0 ? `${cases} 个用例` : "未记录用例";
}

type ModelComparisonJudgeScore = {
  model: string;
  totalScore: number;
  dimensionSummary: string;
  recommendation: string;
};

type ModelComparisonJudgeSummary = {
  judgeModel: string;
  winner: string;
  conclusion: string;
  scores: ModelComparisonJudgeScore[];
};

function modelComparisonJudgeSummary(_asset: PromptEvaluationAsset): ModelComparisonJudgeSummary | null {
  return null;
}

function summarizeStructuredCase(item: PromptEvaluationStructuredCase): string {
  const expected = item.expected_contains.map((value) => String(value)).filter(Boolean);
  const variables = Object.keys(item.variables ?? {});
  const parts = [];
  if (variables.length > 0) parts.push(`变量 ${variables.join("、")}`);
  if (expected.length > 0) parts.push(`期望 ${expected.join("、")}`);
  return parts.length > 0 ? parts.join(" · ") : "未记录变量和期望";
}

function DatasetVersionControls({ asset, saving }: { asset: PromptEvaluationAsset; saving: boolean }) {
  const workspaceId = useWorkspaceId() ?? "";
  const queryClient = useQueryClient();
  const [diff, setDiff] = useState<PromptEvaluationDatasetVersionDiff | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [selectedVersionId, setSelectedVersionId] = useState<string | null>(null);
  const versionsQuery = useQuery({
    queryKey: promptLibraryKeys.datasetVersions(workspaceId, asset.id),
    queryFn: () => api.listPromptEvaluationDatasetVersions(asset.id, 20),
    enabled: Boolean(loaded && workspaceId && asset.id),
  });
  const tagTrendsQuery = useQuery({
    queryKey: promptLibraryKeys.datasetVersionTagTrends(workspaceId, asset.id),
    queryFn: () => api.listPromptEvaluationDatasetVersionTagTrends(asset.id, { version_limit: 20, limit: 200 }),
    enabled: Boolean(loaded && workspaceId && asset.id),
  });
  const versionRowsQuery = useQuery({
    queryKey: promptLibraryKeys.datasetVersionRows(workspaceId, asset.id, selectedVersionId),
    queryFn: () => api.listPromptEvaluationDatasetVersionRows(asset.id, selectedVersionId!),
    enabled: Boolean(loaded && workspaceId && asset.id && selectedVersionId),
  });
  const invalidateDataset = () => {
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.datasetVersions(workspaceId, asset.id) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.datasetVersionRows(workspaceId, asset.id, selectedVersionId) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.datasetVersionTagTrends(workspaceId, asset.id) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.assets(workspaceId) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.cases(workspaceId) });
  };
  const diffMut = useMutation({
    mutationFn: () => {
      const versions = versionsQuery.data?.items ?? [];
      if (versions.length < 2) {
        throw new Error("至少需要两个数据集版本才能对比");
      }
      return api.diffPromptEvaluationDatasetVersion(asset.id, versions[1]!.id, versions[0]!.id);
    },
    onSuccess: (result) => {
      setDiff(result);
      toast.success("数据集版本对比已生成");
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : "数据集版本对比失败"),
  });
  const restoreMut = useMutation({
    mutationFn: (versionId: string) =>
      api.restorePromptEvaluationDatasetVersion(asset.id, versionId, {
        version_label: "从历史版本恢复",
        metadata: {
          来源: "训练与评估页面",
          用途: "恢复历史数据集版本并生成新的可追溯快照",
          恢复时间: new Date().toISOString(),
        },
      }),
    onSuccess: (result) => {
      setDiff(null);
      invalidateDataset();
      toast.success(`已恢复 v${result.restored_from.version}，并生成 v${result.restored_version.version}`);
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : "数据集版本恢复失败"),
  });
  const versions = versionsQuery.data?.items ?? [];
  const latest = versions[0];
  const oldestLoaded = versions[versions.length - 1] ?? null;
  const selectedVersion = versions.find((version) => version.id === selectedVersionId) ?? null;
  const selectedRows = versionRowsQuery.data?.items ?? [];
  const tagTrends = tagTrendsQuery.data?.items ?? [];
  const busy = versionsQuery.isLoading || versionsQuery.isFetching || diffMut.isPending || restoreMut.isPending;
  const disabled = saving || busy;

  return (
    <div className="mt-2 grid gap-2 rounded-md border border-border/70 bg-muted/20 p-2 text-[11px]" data-testid={`dataset-version-controls-${asset.id}`}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-foreground">版本治理</span>
        {!loaded ? (
          <span className="text-muted-foreground">按需加载版本快照，避免列表页批量请求</span>
        ) : latest ? (
          <span className="text-muted-foreground">最新 v{latest.version} · {latest.row_count} 行 · 指纹 {latest.row_fingerprint.slice(0, 10)}</span>
        ) : (
          <span className="text-muted-foreground">暂无版本快照</span>
        )}
        <Button
          size="sm"
          variant="secondary"
          data-testid={`load-dataset-versions-${asset.id}`}
          onClick={() => setLoaded(true)}
          disabled={saving || loaded || busy}
        >
          {versionsQuery.isLoading ? <Loader2 className="size-3.5 animate-spin" /> : <Search className="size-3.5" />}
          查看版本
        </Button>
        <Button
          size="sm"
          variant="secondary"
          data-testid={`diff-dataset-version-${asset.id}`}
          onClick={() => diffMut.mutate()}
          disabled={disabled || !loaded || versions.length < 2}
        >
          {diffMut.isPending ? <Loader2 className="size-3.5 animate-spin" /> : <Search className="size-3.5" />}
          对比最近版本
        </Button>
      </div>
      {loaded && (
        <div className="rounded border bg-background px-2 py-1.5 text-muted-foreground" data-testid={`dataset-version-chain-${asset.id}`}>
          版本链回放：已加载最近 {versions.length} 个快照
          {latest ? ` · 最新 v${latest.version}` : ""}
          {oldestLoaded && oldestLoaded.id !== latest?.id ? ` · 最早 v${oldestLoaded.version}` : ""}
          {versions.length >= 20 ? " · 继续缩小数据集后再做长链复盘" : ""}
        </div>
      )}
      {loaded && (
        <DatasetVersionTagTrendsPanel
          assetId={asset.id}
          trends={tagTrends}
          loading={tagTrendsQuery.isLoading || tagTrendsQuery.isFetching}
        />
      )}
      {versions.length > 0 && (
        <div className="grid gap-2" data-testid={`dataset-version-timeline-${asset.id}`}>
          <div className="grid gap-1">
            {versions.map((version) => (
              <div key={version.id} className="flex flex-wrap items-center gap-2 rounded border bg-background px-2 py-1.5">
                <button
                  type="button"
                  className="text-left font-medium text-foreground hover:underline"
                  data-testid={`show-dataset-version-rows-${asset.id}-${version.version}`}
                  onClick={() => setSelectedVersionId(version.id)}
                >
                  v{version.version} · {version.version_label || "未命名快照"}
                </button>
                <span className="text-muted-foreground">{version.row_count} 行 · 指纹 {version.row_fingerprint.slice(0, 10)}</span>
                <span className="text-muted-foreground">{version.created_at || "未记录时间"}</span>
                {version.id === latest?.id && <Badge variant="outline" className="text-[10px]">最新</Badge>}
                {selectedVersionId === version.id && <Badge variant="secondary" className="text-[10px]">正在查看</Badge>}
                <Button
                  size="sm"
                  variant={version.id === latest?.id ? "outline" : "secondary"}
                  className="ml-auto h-7"
                  data-testid={`restore-dataset-version-${asset.id}-${version.version}`}
                  onClick={() => restoreMut.mutate(version.id)}
                  disabled={disabled}
                >
                  {restoreMut.isPending && restoreMut.variables === version.id ? <Loader2 className="size-3.5 animate-spin" /> : <Archive className="size-3.5" />}
                  恢复 v{version.version}
                </Button>
              </div>
            ))}
          </div>
          {selectedVersion && (
            <DatasetVersionRowsPanel
              assetId={asset.id}
              version={selectedVersion}
              rows={selectedRows}
              loading={versionRowsQuery.isLoading || versionRowsQuery.isFetching}
            />
          )}
        </div>
      )}
      {diff && (
        <div className="text-muted-foreground" data-testid={`dataset-version-diff-${asset.id}`}>
          对比 v{diff.base_version.version} → v{diff.target_version.version}：新增 {diff.summary["新增"] ?? 0} · 删除 {diff.summary["删除"] ?? 0} · 变更 {diff.summary["变更"] ?? 0} · 未变更 {diff.summary["未变更"] ?? 0}
        </div>
      )}
    </div>
  );
}

function DatasetVersionTagTrendsPanel({
  assetId,
  trends,
  loading,
}: {
  assetId: string;
  trends: PromptEvaluationDatasetVersionTagTrend[];
  loading: boolean;
}) {
  const grouped = useMemo(() => {
    const byTag = new Map<string, PromptEvaluationDatasetVersionTagTrend[]>();
    for (const item of trends) {
      const tag = item.tag.trim();
      if (!tag) continue;
      const bucket = byTag.get(tag) ?? [];
      bucket.push(item);
      byTag.set(tag, bucket);
    }
    return Array.from(byTag.entries())
      .map(([tag, items]) => ({
        tag,
        total: items.reduce((sum, item) => sum + item.case_count, 0),
        items: items.slice().sort((a, b) => b.version - a.version),
      }))
      .sort((a, b) => b.total - a.total || a.tag.localeCompare(b.tag, "zh-Hans-CN"))
      .slice(0, 8);
  }, [trends]);
  return (
    <div className="rounded border bg-background px-2 py-1.5" data-testid={`dataset-version-tag-trends-${assetId}`}>
      <div className="flex flex-wrap items-center gap-2 text-muted-foreground">
        <span className="font-medium text-foreground">版本标签趋势</span>
        {loading ? <Loader2 className="size-3.5 animate-spin" /> : <span>基于不可变版本快照统计</span>}
      </div>
      {!loading && grouped.length === 0 && (
        <div className="mt-1 text-muted-foreground">暂无可统计标签。</div>
      )}
      {grouped.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-1.5">
          {grouped.map((group) => (
            <span key={group.tag} className="rounded border bg-muted/20 px-2 py-1 text-muted-foreground">
              <span className="font-medium text-foreground">{group.tag}</span>{" "}
              {group.items.map((item) => `v${item.version}:${item.case_count}`).join(" / ")}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

function DatasetVersionRowsPanel({
  assetId,
  version,
  rows,
  loading,
}: {
  assetId: string;
  version: { id: string; version: number; row_count: number; version_label: string };
  rows: PromptEvaluationDatasetVersionRow[];
  loading: boolean;
}) {
  return (
    <div className="grid gap-1.5 rounded border border-border/70 bg-muted/20 px-2 py-2" data-testid={`dataset-version-rows-${assetId}`}>
      <div className="flex flex-wrap items-center gap-2 text-muted-foreground">
        <span className="font-medium text-foreground">行级快照 v{version.version}</span>
        <span>{version.version_label || "未命名快照"}</span>
        <span>已加载 {rows.length} / {version.row_count} 行</span>
        {loading && <Loader2 className="size-3.5 animate-spin" />}
      </div>
      {loading ? (
        <div className="rounded border border-dashed px-2 py-2 text-muted-foreground">正在读取版本行级快照。</div>
      ) : rows.length === 0 ? (
        <div className="rounded border border-dashed px-2 py-2 text-muted-foreground">该版本没有可展示的行级快照。</div>
      ) : (
        rows.slice(0, 8).map((row) => (
          <div key={row.id} className="grid gap-1 rounded border bg-background px-2 py-1.5">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium text-foreground">#{row.row_index + 1} {row.row_name || "未命名用例"}</span>
              <span className="text-muted-foreground">{caseSourceLabel(row.source)} · {row.tags.map(String).join("、") || "无标签"}</span>
            </div>
            <div className="text-muted-foreground">
              变量 {Object.keys(row.variables ?? {}).join("、") || "无"} · 期望 {row.expected_contains.map(String).filter(Boolean).join("、") || "无"}
            </div>
          </div>
        ))
      )}
      {rows.length > 8 && <div className="text-muted-foreground">已截取前 8 行展示；完整数据仍通过公开 API 回读。</div>}
    </div>
  );
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
  return `智能体任务：${status}${taskId ? ` · 任务标识 ${taskId}` : ""}${agent ? ` · ${agent}` : ""}${model ? ` · ${model}` : ""}`;
}

function summarizeDatasetVersion(asset: PromptEvaluationAsset): string | null {
  const payload = asset.payload ?? {};
  const version = payload["最近数据集版本"];
  if (!version || typeof version !== "object" || Array.isArray(version)) return null;
  const record = version as Record<string, unknown>;
  const versionNumber = stringFromRecord(record, "version");
  const rowCount = stringFromRecord(record, "row_count");
  const fingerprint = stringFromRecord(record, "row_fingerprint");
  const createdAt = stringFromRecord(record, "created_at");
  if (!versionNumber && !rowCount && !fingerprint) return null;
  const parts = [`数据集版本 v${versionNumber || "?"}`];
  if (rowCount) parts.push(`${rowCount} 行`);
  if (fingerprint) parts.push(`指纹 ${fingerprint.slice(0, 12)}`);
  if (createdAt) parts.push(createdAt);
  return parts.join(" · ");
}

function summarizeLinkedDatasetVersions(asset: PromptEvaluationAsset): string | null {
  const payload = asset.payload ?? {};
  const raw = payload["linked_dataset_versions"] ?? payload["数据集版本"] ?? payload["关联数据集版本"];
  if (!Array.isArray(raw) || raw.length === 0) return null;
  const parts = raw
    .map((item) => {
      if (!item || typeof item !== "object" || Array.isArray(item)) return "";
      const record = item as Record<string, unknown>;
      const datasetName = stringFromRecord(record, "dataset_name") || stringFromRecord(record, "数据集名称") || stringFromRecord(record, "name") || stringFromRecord(record, "名称");
      const version = stringFromRecord(record, "version") || stringFromRecord(record, "版本");
      const fingerprint = stringFromRecord(record, "row_fingerprint") || stringFromRecord(record, "行指纹");
      const versionId = stringFromRecord(record, "dataset_version_id") || stringFromRecord(record, "数据集版本ID");
      const label = datasetName || "数据集";
      const versionLabel = version ? `v${version}` : versionId ? `版本 ${versionId.slice(0, 8)}` : "未声明版本";
      return `${label} ${versionLabel}${fingerprint ? ` · 指纹 ${fingerprint.slice(0, 10)}` : ""}`;
    })
    .filter(Boolean);
  return parts.length > 0 ? `绑定数据集版本：${parts.join("；")}` : null;
}

function displayRunKind(runKind: string): string {
  return runKind === "Agent执行" ? "智能体执行" : runKind;
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
  if (typeof value === "string") return value;
  if (typeof value === "number" && Number.isFinite(value)) return String(value);
  return "";
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

function formatDuration(value: unknown): string {
  const ms = typeof value === "number" ? value : Number(value ?? 0);
  if (!Number.isFinite(ms) || ms <= 0) return "0 ms";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const seconds = ms / 1000;
  if (seconds < 60) return `${Math.round(seconds * 10) / 10} 秒`;
  const minutes = seconds / 60;
  return `${Math.round(minutes * 10) / 10} 分钟`;
}

function downloadTextFile(content: string, filename: string, mimeType: string) {
  const blob = new Blob([content], { type: mimeType });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}
