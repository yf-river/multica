"use client";

import { useEffect, useMemo, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, BookOpenText, CheckCircle, Download, Loader2, Play, Plus, Save, Search, Trash2, XCircle } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  TRAINING_WORKBENCH_VIEW_BY_TAB,
  trainingWorkbenchPath,
  trainingWorkbenchShowsPromptEditor,
  trainingWorkbenchTabFromView,
  trainingWorkbenchTitleFromView,
  type TrainingWorkbenchTab,
  type TrainingWorkbenchViewId,
} from "@multica/core/training";
import type {
  CreatePromptLibraryItemRequest,
  CreatePromptEvaluationCaseRequest,
  UpdatePromptEvaluationCaseRequest,
  PromptEvaluationAsset,
  PromptEvaluationDimensionScoreSummary,
  PromptEvaluationDimensionScoreTrend,
  PromptEvaluationEvidenceSnapshot,
  PromptEvaluationExperimentDimension,
  PromptEvaluationOptimizationCandidate,
  PromptEvaluationStructuredCase,
  PromptEvaluationRun,
  PromptEvaluationRunEvidence,
  PromptEvaluationAssetEvidenceArchivePackage,
  PromptEvaluationRuntimeReadiness,
  PromptEvaluationSummary,
  PromptEvaluationAssetType,
  PromptEvaluationDatasetVersionDiff,
  PromptEvaluationDatasetVersionRow,
  ObservabilitySummary,
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
import {
  AcceptanceFixtureNotice,
  isAcceptanceFixtureRecord,
  isAcceptanceFixtureText,
} from "../../common/acceptance-fixtures";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import {
  DEFAULT_AGENT_MODEL,
  draftToRequest,
  emptyDraft,
  itemToDraft,
  parseDebugValues,
  requestToDraft,
  setDraftField,
  splitList,
  valuesToDebugText,
  type PromptDraft,
} from "./prompt-library-request-builders";
import { legacyTrainingSelectedPromptStorageKeys, trainingSelectedPromptStorageKey } from "./prompt-selection-storage";
import { usePromptPlaygroundActions } from "./use-prompt-playground-actions";

const promptLibraryKeys = {
  list: (workspaceId: string) => ["prompt-library", workspaceId, "list"] as const,
  versions: (workspaceId: string, promptId: string | null) => ["prompt-library", workspaceId, "versions", promptId ?? ""] as const,
  assets: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-assets"] as const,
  datasetVersions: (workspaceId: string, assetId: string) => ["prompt-library", workspaceId, "evaluation-dataset-versions", assetId] as const,
  datasetVersionRows: (workspaceId: string, assetId: string, versionId: string | null) => ["prompt-library", workspaceId, "evaluation-dataset-version-rows", assetId, versionId ?? ""] as const,
  cases: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-cases"] as const,
  experimentDimensions: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-experiment-dimensions"] as const,
  dimensionScoreSummaries: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-dimension-score-summaries"] as const,
  dimensionScoreTrends: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-dimension-score-trends"] as const,
  runs: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-runs"] as const,
  runEvidence: (workspaceId: string, runId: string | null) => ["prompt-library", workspaceId, "run-evidence", runId ?? ""] as const,
  runEvidenceSnapshots: (workspaceId: string, runId: string | null) => ["prompt-library", workspaceId, "run-evidence-snapshots", runId ?? ""] as const,
  candidates: (workspaceId: string) => ["prompt-library", workspaceId, "optimization-candidates"] as const,
  summary: (workspaceId: string) => ["prompt-library", workspaceId, "evaluation-summary"] as const,
};

const PROMPT_TYPES = ["全部", "需求澄清", "系统提示词", "评测提示词", "小队 SOP", "通用"];
type WorkbenchTab = TrainingWorkbenchTab;
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
type DemoTimeRange = "24h" | "7d" | "30d" | "all";

const DEMO_TIME_RANGES: Array<{ value: DemoTimeRange; label: string; sinceMs: number | null }> = [
  { value: "24h", label: "最近24小时", sinceMs: 24 * 60 * 60 * 1000 },
  { value: "7d", label: "最近7天", sinceMs: 7 * 24 * 60 * 60 * 1000 },
  { value: "30d", label: "最近30天", sinceMs: 30 * 24 * 60 * 60 * 1000 },
  { value: "all", label: "全部", sinceMs: null },
];
const DEFAULT_DEMO_TIME_RANGE = DEMO_TIME_RANGES[1]!;
const TRAINING_DATA_SCOPE_PARAM = "training_data";
const TRAINING_DATA_SCOPE_ACCEPTANCE = "acceptance";
const EMPTY_EVIDENCE_FOCUS: EvidenceFocus = {
  traceSeq: null,
  toolChainId: null,
  trialAnchor: null,
  assertionAnchor: null,
  messageSeq: null,
  spanAnchor: null,
  failureAnchor: null,
};

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

function trainingPathWithAcceptanceScope(pathname: string, searchParams: URLSearchParams, showAcceptanceFixtures: boolean) {
  const nextParams = new URLSearchParams(searchParams);
  if (showAcceptanceFixtures) {
    nextParams.set(TRAINING_DATA_SCOPE_PARAM, TRAINING_DATA_SCOPE_ACCEPTANCE);
  } else {
    nextParams.delete(TRAINING_DATA_SCOPE_PARAM);
  }
  const query = nextParams.toString();
  return query ? `${pathname}?${query}` : pathname;
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
  const showAcceptanceFromURL = navigation.searchParams.get(TRAINING_DATA_SCOPE_PARAM) === TRAINING_DATA_SCOPE_ACCEPTANCE;
  const focusedRunId = navigation.searchParams.get("run");
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
  const [demoTimeRange, setDemoTimeRange] = useState<DemoTimeRange>("7d");
  const [exportingDemoEvidence, setExportingDemoEvidence] = useState(false);
  const [exportingAssetEvidencePackageAssetId, setExportingAssetEvidencePackageAssetId] = useState<string | null>(null);
  const [showAcceptanceFixtures, setShowAcceptanceFixtures] = useState(showAcceptanceFromURL);
  const shouldShowPromptEditor = showPromptEditor ?? trainingWorkbenchShowsPromptEditor(resolvedView);
  const demoSince = useMemo(() => {
    const option = DEMO_TIME_RANGES.find((item) => item.value === demoTimeRange);
    if (!option?.sinceMs) return null;
    return new Date(Date.now() - option.sinceMs).toISOString();
  }, [demoTimeRange]);

  useEffect(() => {
    setActiveTab(trainingWorkbenchTabFromView(resolvedView));
  }, [resolvedView]);

  useEffect(() => {
    setShowAcceptanceFixtures(showAcceptanceFromURL);
  }, [showAcceptanceFromURL]);

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

  const isDashboardTab = activeTab === "运行看板";
  const activeViewId = TRAINING_WORKBENCH_VIEW_BY_TAB[activeTab];
  const effectiveRunStatusFilter = activeTab === "运行历史" ? runStatusFilter : "全部";
  const shouldShowPromptHeaderActions = activeTab === "提示词库";
  const isEvaluationAssetTab =
    activeTab === "数据集" ||
    activeTab === "测试套件" ||
    activeTab === "实验" ||
    activeTab === "优化运行";
  const needsPromptItems =
    (shouldShowPromptEditor && activeTab === "提示词库") ||
    isEvaluationAssetTab;
  const needsPromptVersions = shouldShowPromptEditor && activeTab === "提示词库";
  const needsEvaluationAssets = isEvaluationAssetTab;
  const needsStructuredCases =
    activeTab === "数据集" ||
    activeTab === "测试套件" ||
    activeTab === "实验" ||
    activeTab === "运行历史";
  const needsExperimentDimensions = activeTab === "实验";
  const needsDimensionScoreSummaries = isDashboardTab || activeTab === "实验";
  const needsDimensionScoreTrends = isDashboardTab || activeTab === "实验";
  const needsRuns =
    isDashboardTab ||
    activeTab === "实验" ||
    activeTab === "运行历史" ||
    activeTab === "优化运行";
  const needsCandidates = isDashboardTab || activeTab === "运行历史" || activeTab === "优化运行";
  const needsRuntimeReadiness = isDashboardTab;

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
  const experimentDimensionQuery = useQuery({
    queryKey: promptLibraryKeys.experimentDimensions(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationExperimentDimensions(),
    enabled: !!workspaceId && needsExperimentDimensions,
  });
  const dimensionScoreSummaryQuery = useQuery({
    queryKey: promptLibraryKeys.dimensionScoreSummaries(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationDimensionScoreSummaries(),
    enabled: !!workspaceId && needsDimensionScoreSummaries,
  });
  const dimensionScoreTrendQuery = useQuery({
    queryKey: [...promptLibraryKeys.dimensionScoreTrends(workspaceId ?? ""), demoSince ?? "all"] as const,
    queryFn: () => api.listPromptEvaluationDimensionScoreTrends({ since: demoSince }),
    enabled: !!workspaceId && needsDimensionScoreTrends,
  });
  const runQuery = useQuery({
    queryKey: [...promptLibraryKeys.runs(workspaceId ?? ""), demoSince ?? "all", effectiveRunStatusFilter] as const,
    queryFn: () => api.listPromptEvaluationRuns({
      limit: 100,
      since: demoSince,
      status: effectiveRunStatusFilter === "全部" ? undefined : effectiveRunStatusFilter,
    }),
    enabled: !!workspaceId && needsRuns,
  });
  const candidateQuery = useQuery({
    queryKey: promptLibraryKeys.candidates(workspaceId ?? ""),
    queryFn: () => api.listPromptEvaluationOptimizationCandidates({ limit: 100 }),
    enabled: !!workspaceId && needsCandidates,
  });
  const summaryQuery = useQuery({
    queryKey: [...promptLibraryKeys.summary(workspaceId ?? ""), demoSince ?? "all", showAcceptanceFixtures ? "with-acceptance" : "business"] as const,
    queryFn: () => api.getPromptEvaluationSummary({
      ...(demoSince ? { since: demoSince } : {}),
      include_acceptance_fixtures: showAcceptanceFixtures,
    }),
    enabled: !!workspaceId,
  });
  const runtimeReadinessQuery = useQuery({
    queryKey: ["training-evaluation", workspaceId ?? "", "runtime-readiness"],
    queryFn: () => api.getPromptEvaluationRuntimeReadiness(),
    enabled: !!workspaceId && needsRuntimeReadiness,
  });
  const observabilitySummaryQuery = useQuery({
    queryKey: ["training-evaluation", workspaceId ?? "", "workspace-observability-summary", demoSince ?? "all"],
    queryFn: () => api.getWorkspaceObservabilitySummary(workspaceId ?? "", demoSince ? { since: demoSince } : undefined),
    enabled: !!workspaceId && isDashboardTab,
    staleTime: 30_000,
  });

  const items = listQuery.data?.items ?? [];
  const assets = assetQuery.data?.items ?? [];
  const cases = caseQuery.data?.items ?? [];
  const experimentDimensions = experimentDimensionQuery.data?.items ?? [];
  const dimensionScoreSummaries = dimensionScoreSummaryQuery.data?.items ?? [];
  const dimensionScoreTrends = dimensionScoreTrendQuery.data?.items ?? [];
  const runs = runQuery.data?.items ?? [];
  const candidates = candidateQuery.data?.items ?? [];
  const summary = summaryQuery.data ?? null;
  const acceptanceFixtureItems = useMemo(
    () =>
      items.filter((item) =>
        isAcceptanceFixtureRecord(item as unknown as Record<string, unknown>, [
          "name",
          "description",
          "content",
          "tags",
        ]),
      ),
    [items],
  );
  const visiblePromptItems = useMemo(
    () =>
      showAcceptanceFixtures
        ? items
        : items.filter((item) => !acceptanceFixtureItems.includes(item)),
    [acceptanceFixtureItems, items, showAcceptanceFixtures],
  );
  const selectedFromList = selectedId ? items.find((item) => item.id === selectedId) ?? null : null;
  const visibleCandidatesForDashboard = useMemo(
    () =>
      showAcceptanceFixtures
        ? candidates
        : candidates.filter((candidate) =>
            !isAcceptanceFixtureRecord(candidate as unknown as Record<string, unknown>, [
              "candidate_name",
              "candidate_content",
              "rationale",
              "metrics",
            ]),
          ),
    [candidates, showAcceptanceFixtures],
  );
  const selected = selectedFromList ?? (isDraftingNew ? null : visiblePromptItems[0] ?? null);
  const versionQuery = useQuery({
    queryKey: promptLibraryKeys.versions(workspaceId ?? "", selectedFromList?.id ?? null),
    queryFn: () => api.listPromptLibraryVersions(selectedFromList?.id ?? ""),
    enabled: !!workspaceId && needsPromptVersions && !!selectedFromList,
  });
  const promptVersions = versionQuery.data?.items ?? [];
  const experimentPromptIds = useMemo(() => {
    if (activeTab !== "实验") return [];
    return uniqueSortedStrings(
      assets
        .filter((asset) => asset.asset_type === "实验" && !!asset.prompt_id)
        .filter((asset) => showAcceptanceFixtures || !isAcceptanceFixtureText(asset.name, asset.description, asset.payload))
        .map((asset) => asset.prompt_id ?? ""),
    );
  }, [activeTab, assets, showAcceptanceFixtures]);
  const experimentVersionQueries = useQueries({
    queries: experimentPromptIds.map((promptId) => ({
      queryKey: promptLibraryKeys.versions(workspaceId ?? "", promptId),
      queryFn: () => api.listPromptLibraryVersions(promptId),
      enabled: !!workspaceId && activeTab === "实验" && !!promptId,
    })),
  });
  const experimentVersionsByPromptId = useMemo(() => {
    const result = new Map<string, PromptLibraryVersion[]>();
    experimentPromptIds.forEach((promptId, index) => {
      result.set(promptId, experimentVersionQueries[index]?.data?.items ?? []);
    });
    return result;
  }, [experimentPromptIds, experimentVersionQueries]);
  const experimentVersionsLoading = experimentVersionQueries.some((query) => query.isLoading || query.isFetching);
  const agentRuntimeReadiness = runtimeReadinessQuery.data ?? DEFAULT_AGENT_RUNTIME_READINESS;
  const selectedPromptStorageKey = trainingSelectedPromptStorageKey(workspaceId);
  const selectedPromptLegacyStorageKeys = useMemo(() => legacyTrainingSelectedPromptStorageKeys(workspaceId), [workspaceId]);

  useEffect(() => {
    if (!selectedPromptStorageKey || isDraftingNew) return;
    try {
      const storedId = [selectedPromptStorageKey, ...selectedPromptLegacyStorageKeys]
        .map((key) => window.localStorage.getItem(key))
        .find(Boolean);
      if (storedId && storedId !== selectedId && items.some((item) => item.id === storedId)) {
        setSelectedId(storedId);
      }
    } catch {
      // localStorage is best-effort; route usability must not depend on it.
    }
  }, [isDraftingNew, items, selectedId, selectedPromptLegacyStorageKeys, selectedPromptStorageKey]);

  useEffect(() => {
    if (!selectedPromptStorageKey || !selectedId) return;
    try {
      window.localStorage.setItem(selectedPromptStorageKey, selectedId);
      for (const key of selectedPromptLegacyStorageKeys) {
        window.localStorage.setItem(key, selectedId);
      }
    } catch {
      // Ignore storage failures in private or restricted browser contexts.
    }
  }, [selectedId, selectedPromptLegacyStorageKeys, selectedPromptStorageKey]);

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
  const invalidateExperimentDimensions = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.experimentDimensions(workspaceId ?? "") });
  const invalidateDimensionScoreSummaries = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.dimensionScoreSummaries(workspaceId ?? "") });
  const invalidateDimensionScoreTrends = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.dimensionScoreTrends(workspaceId ?? "") });
  const invalidateRuns = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runs(workspaceId ?? "") });
  const invalidateCandidates = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.candidates(workspaceId ?? "") });
  const invalidateSummary = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.summary(workspaceId ?? "") });
  const invalidateRunEvidenceSnapshots = (runId: string) => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidenceSnapshots(workspaceId ?? "", runId) });
  const rememberSelectedPrompt = (promptId: string | null) => {
    setSelectedId(promptId);
    if (!selectedPromptStorageKey) return;
    try {
      for (const key of [selectedPromptStorageKey, ...selectedPromptLegacyStorageKeys]) {
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
      invalidateExperimentDimensions();
      invalidateDimensionScoreSummaries();
      invalidateDimensionScoreTrends();
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
      invalidateExperimentDimensions();
      invalidateDimensionScoreSummaries();
      invalidateDimensionScoreTrends();
      invalidateRuns();
      invalidateSummary();
      toast.success("资产已删除");
    },
  });

  const importDatasetFromTracesMut = useMutation({
    mutationFn: (assetId: string) =>
      api.createPromptEvaluationDatasetFromTraces(assetId, {
        limit: 5,
        expected_contains: ["任务", "trace"],
        tags: ["trace导入", "真实执行记录"],
      }),
    onSuccess: (result) => {
      invalidateAssets();
      invalidateCases();
      invalidateSummary();
      toast.success(`已从 trace 导入 ${result.created_count} 条数据集样本`);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "trace 导入失败，请先产生真实任务记录");
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
      invalidateSummary();
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
      invalidateDimensionScoreSummaries();
      invalidateDimensionScoreTrends();
      invalidateCandidates();
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidence(workspaceId ?? "", runId) });
      invalidateRunEvidenceSnapshots(runId);
      invalidateSummary();
      toast.success("运行记录已同步");
    },
  });

  const cancelRunMut = useMutation({
    mutationFn: (runId: string) => api.cancelPromptEvaluationRun(runId),
    onSuccess: (run) => {
      invalidateRuns();
      invalidateDimensionScoreSummaries();
      invalidateDimensionScoreTrends();
      invalidateCandidates();
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidence(workspaceId ?? "", run.id) });
      invalidateRunEvidenceSnapshots(run.id);
      invalidateSummary();
      toast.success("训练评估运行已取消");
    },
  });

  const reviewRunMut = useMutation({
    mutationFn: ({ runId, decision, note }: { runId: string; decision: "通过" | "未通过"; note: string }) =>
      api.reviewPromptEvaluationRun(runId, { decision, note }),
    onSuccess: (run) => {
      invalidateRuns();
      invalidateDimensionScoreSummaries();
      invalidateDimensionScoreTrends();
      invalidateCandidates();
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidence(workspaceId ?? "", run.id) });
      invalidateRunEvidenceSnapshots(run.id);
      invalidateSummary();
      toast.success(`人工复核已处理：${run.review_decision || run.status}`);
    },
  });

  const createEvidenceSnapshotMut = useMutation({
    mutationFn: (runId: string) => api.createPromptEvaluationEvidenceSnapshot(runId, "验收归档"),
    onSuccess: (snapshot) => {
      invalidateRunEvidenceSnapshots(snapshot.run_id);
      invalidateSummary();
      toast.success("服务端证据快照已归档");
    },
  });

  const createAssetEvidenceSnapshotsMut = useMutation({
    mutationFn: (assetId: string) => api.createPromptEvaluationAssetEvidenceSnapshots(assetId, "验收归档", 20),
    onSuccess: (result) => {
      invalidateRuns();
      invalidateSummary();
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
      invalidateDimensionScoreSummaries();
      invalidateDimensionScoreTrends();
      invalidateSummary();
      toast.success(`真实智能体优化任务已入队：${result.task_id}`);
      setActiveTab("运行历史");
      navigation.push(trainingWorkbenchPath(workspacePaths.training(), TRAINING_WORKBENCH_VIEW_BY_TAB["运行历史"]));
    },
  });

  const retryOptimizationAssetMut = useMutation({
    mutationFn: (assetId: string) => api.runPromptEvaluationAssetAgent(assetId),
    onSuccess: (result) => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      invalidateDimensionScoreSummaries();
      invalidateDimensionScoreTrends();
      invalidateSummary();
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidence(workspaceId ?? "", result.run.id) });
      toast.success(`优化运行重试已入队：${result.task_id}`);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "优化运行重试失败，请检查 Codex 运行时状态");
    },
  });

  const publishCandidateMut = useMutation({
    mutationFn: (candidateId: string) => api.publishPromptEvaluationOptimizationCandidate(candidateId),
    onSuccess: (result) => {
      invalidate();
      invalidateVersions(result.prompt.id);
      invalidateCandidates();
      invalidateSummary();
      rememberSelectedPrompt(result.prompt.id);
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
  const promptPlaygroundActions = usePromptPlaygroundActions({
    draft,
    selected,
    items: visiblePromptItems,
    selectedPromptStorageKey,
    onAssetsChanged: invalidateAssets,
    onCasesChanged: invalidateCases,
    onExperimentDimensionsChanged: invalidateExperimentDimensions,
    onRunsChanged: invalidateRuns,
    onSummaryChanged: invalidateSummary,
  });
  const {
    debugValuesText,
    setDebugValuesText,
    debugResult,
    runningDebug,
    creatingAsset,
    runDebug,
    createWorkbenchAsset,
  } = promptPlaygroundActions;
  const savingAsset = creatingAsset || updateAssetMut.isPending || deleteAssetMut.isPending || importDatasetFromTracesMut.isPending || createDatasetVersionMut.isPending;

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
              execution_spans: [],
              tool_call_chains: [],
              tool_call_summary: [],
              execution_summary: {},
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
          acc.execution_span条数 += evidence.execution_spans.length;
          acc.tool_call_chain条数 += evidence.tool_call_chains.length;
          acc.tool_call_summary条数 += evidence.tool_call_summary.length;
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
          execution_span条数: 0,
          tool_call_chain条数: 0,
          tool_call_summary条数: 0,
        },
      );
      const optimizationCandidateEvidence = candidates.map((candidate) => {
        const metrics = candidate.metrics as Record<string, unknown>;
        return {
          id: candidate.id,
          run_id: candidate.run_id,
          prompt_id: candidate.prompt_id,
          状态: candidate.status,
          失败用例数: candidate.failed_case_count,
          候选优先级: stringFromRecord(metrics, "候选优先级") || "未标记",
          失败维度: Array.isArray(metrics["失败维度"]) ? metrics["失败维度"] : [],
          候选优先级依据: stringFromRecord(metrics, "候选优先级依据"),
          rationale: candidate.rationale,
        };
      });
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
        训练维度评分摘要: dimensionScoreSummaries,
        训练维度评分趋势: dimensionScoreTrends,
        优化候选证据: optimizationCandidateEvidence,
        证据统计: evidenceStats,
        资产统计: {
          提示词数: items.length,
          评测资产数: assets.length,
          结构化用例数: cases.length,
          优化候选数: candidates.length,
          维度评分摘要数: dimensionScoreSummaries.length,
          维度评分趋势数: dimensionScoreTrends.length,
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

  const openManualReviewQueue = () => {
    setRunStatusFilter("需人工复核");
    setActiveTab("运行历史");
    navigation.push(trainingPathWithAcceptanceScope(
      trainingWorkbenchPath(workspacePaths.training(), TRAINING_WORKBENCH_VIEW_BY_TAB["运行历史"]),
      navigation.searchParams,
      showAcceptanceFixtures,
    ));
  };

  const updateAcceptanceFixtureScope = (next: boolean) => {
    setShowAcceptanceFixtures(next);
    const nextPath = trainingPathWithAcceptanceScope(navigation.pathname, navigation.searchParams, next);
    const currentQuery = navigation.searchParams.toString();
    const currentPath = currentQuery ? `${navigation.pathname}?${currentQuery}` : navigation.pathname;
    if (nextPath !== currentPath) {
      navigation.replace(nextPath);
    }
  };

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
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => updateAcceptanceFixtureScope(!showAcceptanceFixtures)}
        >
          {showAcceptanceFixtures ? "隐藏验收数据" : "显示验收数据"}
        </Button>
      </PageHeader>

      <TrainingSummaryStrip
        summary={summary}
        loading={summaryQuery.isLoading}
        includesAcceptanceFixtures={showAcceptanceFixtures}
        onOpenManualReviewQueue={openManualReviewQueue}
      />

      {activeTab === "运行看板" ? (
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
            candidates={visibleCandidatesForDashboard}
            includesAcceptanceFixtures={showAcceptanceFixtures}
            onOpenManualReviewQueue={openManualReviewQueue}
          />
        </main>
      ) : shouldShowPromptEditor ? (
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
              <AcceptanceFixtureNotice
                count={acceptanceFixtureItems.length}
                noun="提示词"
                showing={showAcceptanceFixtures}
                onShow={() => updateAcceptanceFixtureScope(true)}
                onHide={() => updateAcceptanceFixtureScope(false)}
              />
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

              <WorkbenchPanel
                activeTab={activeTab}
                workspaceId={workspaceId ?? ""}
                assets={assets}
                cases={cases}
                experimentDimensions={experimentDimensions}
                dimensionScoreSummaries={dimensionScoreSummaries}
                dimensionScoreTrends={dimensionScoreTrends}
                experimentVersionsByPromptId={experimentVersionsByPromptId}
                runs={runs}
                focusedRunId={focusedRunId}
                evidenceFocus={evidenceFocus}
                runStatusFilter={runStatusFilter}
                onRunStatusFilterChange={setRunStatusFilter}
                candidates={candidates}
                loading={assetQuery.isLoading || caseQuery.isLoading || experimentDimensionQuery.isLoading || dimensionScoreSummaryQuery.isLoading || dimensionScoreTrendQuery.isLoading || experimentVersionsLoading || runQuery.isLoading || candidateQuery.isLoading}
                saving={savingAsset}
                showAcceptanceFixtures={showAcceptanceFixtures}
                onShowAcceptanceFixtures={() => updateAcceptanceFixtureScope(true)}
                onHideAcceptanceFixtures={() => updateAcceptanceFixtureScope(false)}
                onCreateAsset={createWorkbenchAsset}
                onToggleAssetStatus={toggleAssetStatus}
                onUpdateAsset={(assetId, data) => updateAssetMut.mutateAsync({ id: assetId, data })}
                onDeleteAsset={deleteAsset}
                onImportDatasetFromTraces={importDatasetFromTraces}
                importingTraceDatasetAssetId={importDatasetFromTracesMut.isPending ? importDatasetFromTracesMut.variables ?? null : null}
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
                onRunOptimizationAgent={(runId) => runOptimizationAgentMut.mutate(runId)}
                runningOptimizationAgentRunId={runOptimizationAgentMut.isPending ? runOptimizationAgentMut.variables ?? null : null}
                onRetryOptimizationAsset={(assetId) => retryOptimizationAssetMut.mutate(assetId)}
                retryingOptimizationAssetId={retryOptimizationAssetMut.isPending ? retryOptimizationAssetMut.variables ?? null : null}
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
      ) : (
        <main className="min-h-0 flex-1 overflow-y-auto p-4 md:p-6">
          <div className="mx-auto flex max-w-5xl flex-col gap-4">
            <WorkbenchPanel
              activeTab={activeTab}
              workspaceId={workspaceId ?? ""}
              assets={assets}
              cases={cases}
              experimentDimensions={experimentDimensions}
              dimensionScoreSummaries={dimensionScoreSummaries}
              dimensionScoreTrends={dimensionScoreTrends}
              experimentVersionsByPromptId={experimentVersionsByPromptId}
              runs={runs}
              focusedRunId={focusedRunId}
              evidenceFocus={evidenceFocus}
              runStatusFilter={runStatusFilter}
              onRunStatusFilterChange={setRunStatusFilter}
              candidates={candidates}
              loading={assetQuery.isLoading || caseQuery.isLoading || experimentDimensionQuery.isLoading || dimensionScoreSummaryQuery.isLoading || dimensionScoreTrendQuery.isLoading || experimentVersionsLoading || runQuery.isLoading || candidateQuery.isLoading}
              saving={savingAsset}
              showAcceptanceFixtures={showAcceptanceFixtures}
              onShowAcceptanceFixtures={() => updateAcceptanceFixtureScope(true)}
              onHideAcceptanceFixtures={() => updateAcceptanceFixtureScope(false)}
              onCreateAsset={createWorkbenchAsset}
              onToggleAssetStatus={toggleAssetStatus}
              onUpdateAsset={(assetId, data) => updateAssetMut.mutateAsync({ id: assetId, data })}
              onDeleteAsset={deleteAsset}
              onImportDatasetFromTraces={importDatasetFromTraces}
              importingTraceDatasetAssetId={importDatasetFromTracesMut.isPending ? importDatasetFromTracesMut.variables ?? null : null}
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
              onRunOptimizationAgent={(runId) => runOptimizationAgentMut.mutate(runId)}
              runningOptimizationAgentRunId={runOptimizationAgentMut.isPending ? runOptimizationAgentMut.variables ?? null : null}
              onRetryOptimizationAsset={(assetId) => retryOptimizationAssetMut.mutate(assetId)}
              retryingOptimizationAssetId={retryOptimizationAssetMut.isPending ? retryOptimizationAssetMut.variables ?? null : null}
              onUpdateCandidate={(candidateId, data) => updateCandidateMut.mutate({ candidateId, data })}
              updatingCandidateId={updateCandidateMut.isPending ? updateCandidateMut.variables?.candidateId ?? null : null}
              onPublishCandidate={(candidateId) => publishCandidateMut.mutate(candidateId)}
              publishingCandidateId={publishCandidateMut.isPending ? publishCandidateMut.variables ?? null : null}
              onRejectCandidate={(candidateId, reason) => rejectCandidateMut.mutate({ candidateId, reason })}
              rejectingCandidateId={rejectCandidateMut.isPending ? rejectCandidateMut.variables?.candidateId ?? null : null}
            />
          </div>
        </main>
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
  includesAcceptanceFixtures,
  onOpenManualReviewQueue,
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
  includesAcceptanceFixtures: boolean;
  onOpenManualReviewQueue: () => void;
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
  const hasAgentEvidence = runs.some((run) => isAgentEvaluationRun(run) && Boolean(run.task_id));
  const hasOptimizationLoop = publishedCandidates > 0 || pendingCandidates > 0 || rejectedCandidates > 0;
  const readinessLabel = runtimeLoading ? "检查中" : runtimeReadiness.label;
  const activeRange = DEMO_TIME_RANGES.find((item) => item.value === timeRange) ?? DEFAULT_DEMO_TIME_RANGE;

  const trainingItems: Array<[string, string]> = [
    ["运行总数", formatNumber(runStatus["运行总数"])],
    ["通过率", formatPercent(trainingMetrics["通过率"])],
    ["失败数", formatNumber(trainingMetrics["失败数"])],
    ["智能体运行数", formatNumber(trainingMetrics["智能体运行数"] ?? trainingMetrics["Agent运行数"])],
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
    ["数据集行", formatNumber(trainingAssets["数据集行"] ?? assets.reduce((sum, asset) => sum + (asset.asset_type === "数据集" ? asset.dataset_row_count : 0), 0))],
    ["测试套件用例", formatNumber(trainingAssets["测试套件用例"] ?? assets.reduce((sum, asset) => sum + (asset.asset_type === "测试套件" ? asset.test_suite_case_count : 0), 0))],
    ["实验维度事实", formatNumber(trainingAssets["实验维度事实"] ?? assets.reduce((sum, asset) => sum + (asset.asset_type === "实验" ? asset.experiment_dimension_count : 0), 0))],
    ["结构化用例", formatNumber(trainingAssets["结构化用例"] ?? cases.length)],
    ["结构化画像", `${formatNumber(trainingAssets["画像用例数"] ?? cases.length)} 用例 · ${formatNumber(trainingAssets["画像变量数"])} 变量 · ${formatNumber(trainingAssets["画像断言数"])} 断言 · ${formatNumber(trainingAssets["评估维度数"])} 维度`],
    ["优化候选", `${pendingCandidates} 待确认 · ${publishedCandidates} 已发布 · ${rejectedCandidates} 已拒绝`],
    ["服务端证据快照", `${formatNumber(trainingAssets["服务端证据快照"] ?? trainingMetrics["服务端证据快照"])} 条 · 验收归档 ${formatNumber(trainingAssets["验收归档快照"] ?? trainingMetrics["验收归档快照"])}`],
    ["真实智能体证据", hasAgentEvidence ? "已有任务/trace 运行记录" : "暂无真实智能体运行记录"],
    ["最近运行", latestRun ? summarizeLatestRunForDemo(latestRun) : "暂无运行"],
  ];

  return (
    <section className="mx-auto flex max-w-7xl flex-col gap-4" data-testid="training-demo-dashboard">
      <div className="flex flex-col gap-2 border-b pb-3 md:flex-row md:items-end md:justify-between">
        <div>
          <h2 className="text-base font-semibold">训练运行看板</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            汇总训练评估、真实智能体、SOP 观测和验收证据；团队级 token、成本和任务耗时在左侧「用量」查看。当前观测范围：{activeRange.label}。
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
            <Badge variant={runtimeReadiness.status === "就绪" ? "secondary" : "outline"}>真实智能体：{readinessLabel}</Badge>
            <Badge variant="outline">训练摘要：{includesAcceptanceFixtures ? "含验收数据" : "业务口径"}</Badge>
            <Badge variant={maybeTruncated ? "outline" : "secondary"}>观测完整性：{completenessStatus}</Badge>
            <Badge variant={hasOptimizationLoop ? "secondary" : "outline"}>优化闭环：{hasOptimizationLoop ? "已有证据" : "待补齐"}</Badge>
            <Button size="sm" variant="secondary" className="h-7 text-xs" onClick={onExportEvidence} disabled={exportingEvidence}>
              {exportingEvidence ? <Loader2 className="size-3.5 animate-spin" /> : <Download className="size-3.5" />}
              {exportingEvidence ? "导出中" : "导出证据 JSON"}
            </Button>
            <Button size="sm" variant="secondary" className="h-7 text-xs" onClick={onOpenManualReviewQueue}>
              打开人工复核队列
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
            <DemoChecklistItem ok={runtimeReadiness.status === "就绪"} label="Codex 运行时可创建真实智能体任务" detail={runtimeReadiness.detail} />
            <DemoChecklistItem ok={hasAgentEvidence} label="运行历史已有任务/trace 证据" detail={latestRun?.task_id ? `最近任务 ${latestRun.task_id}` : "需要执行一次真实智能体评估"} />
            <DemoChecklistItem ok={cases.length > 0} label="数据集/测试套件已有结构化用例" detail={`${cases.length} 条结构化用例`} />
            <DemoChecklistItem ok={Number(trainingAssets["服务端证据快照"] ?? trainingMetrics["服务端证据快照"] ?? 0) > 0} label="运行证据已服务端归档" detail={`${formatNumber(trainingAssets["服务端证据快照"] ?? trainingMetrics["服务端证据快照"])} 条快照，验收归档 ${formatNumber(trainingAssets["验收归档快照"] ?? trainingMetrics["验收归档快照"])}`} />
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

function TrainingSummaryStrip({
  summary,
  loading,
  includesAcceptanceFixtures,
  onOpenManualReviewQueue,
}: {
  summary: PromptEvaluationSummary | null;
  loading: boolean;
  includesAcceptanceFixtures: boolean;
  onOpenManualReviewQueue: () => void;
}) {
  const metrics = summary?.指标 ?? {};
  const assets = summary?.资产统计 ?? {};
  const runStatus = summary?.运行状态 ?? {};
  const candidates = summary?.优化候选 ?? {};
  const items = [
    { label: "运行总数", value: formatNumber(runStatus["运行总数"]) },
    { label: "通过率", value: formatPercent(metrics["通过率"]) },
    { label: "失败数", value: formatNumber(metrics["失败数"]) },
    { label: "智能体运行数", value: formatNumber(metrics["智能体运行数"] ?? metrics["Agent运行数"]) },
    { label: "需人工复核", value: formatNumber(metrics["需人工复核"]) },
    { label: "输入 token", value: formatNumber(metrics["输入token"]) },
    { label: "输出 token", value: formatNumber(metrics["输出token"]) },
    { label: "预估成本", value: formatMoney(metrics["预估成本"]) },
    { label: "待确认优化候选", value: formatNumber(candidates["待确认"]) },
    { label: "已发布优化候选", value: formatNumber(candidates["已发布"]) },
    { label: "资产总数", value: formatNumber(assets["资产总数"]) },
    { label: "数据集行", value: formatNumber(assets["数据集行"]) },
    { label: "测试套件用例", value: formatNumber(assets["测试套件用例"]) },
    { label: "实验维度事实", value: formatNumber(assets["实验维度事实"]) },
    { label: "结构化用例", value: formatNumber(assets["结构化用例"]) },
  ];

  return (
    <section className="shrink-0 border-b bg-muted/20 px-3 py-3" data-testid="training-summary-strip">
      <div className="mb-2 flex min-w-0 items-center justify-between gap-3">
        <div className="min-w-0">
          <h2 className="truncate text-sm font-semibold">项目总览</h2>
          <p className="mt-0.5 truncate text-xs text-muted-foreground">
            {summary?.last_run_at ? `最近运行 ${summary.last_run_at}` : "暂无运行记录"}
          </p>
        </div>
        <Badge variant="outline" className="shrink-0">
          {loading ? "刷新中" : includesAcceptanceFixtures ? "含验收数据" : "业务口径"}
        </Badge>
      </div>
      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
        {items.map((item) => (
          item.label === "需人工复核" ? (
            <button
              key={item.label}
              type="button"
              className="min-w-0 rounded-md border bg-background px-3 py-2 text-left transition-colors hover:bg-muted/60"
              data-testid={`training-summary-${item.label}`}
              onClick={onOpenManualReviewQueue}
            >
              <div className="truncate text-[11px] text-muted-foreground">{item.label}</div>
              <div className="mt-1 truncate text-sm font-semibold">{item.value}</div>
            </button>
          ) : (
            <div key={item.label} className="min-w-0 rounded-md border bg-background px-3 py-2" data-testid={`training-summary-${item.label}`}>
              <div className="truncate text-[11px] text-muted-foreground">{item.label}</div>
              <div className="mt-1 truncate text-sm font-semibold">{item.value}</div>
            </div>
          )
        ))}
      </div>
    </section>
  );
}

function WorkbenchPanel({
  activeTab,
  workspaceId,
  assets,
  cases,
  experimentDimensions,
  dimensionScoreSummaries,
  dimensionScoreTrends,
  experimentVersionsByPromptId,
  runs,
  focusedRunId,
  evidenceFocus,
  runStatusFilter,
  onRunStatusFilterChange,
  candidates,
  loading,
  saving,
  showAcceptanceFixtures,
  onShowAcceptanceFixtures,
  onHideAcceptanceFixtures,
  onCreateAsset,
  onToggleAssetStatus,
  onUpdateAsset,
  onDeleteAsset,
  onImportDatasetFromTraces,
  importingTraceDatasetAssetId,
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
  onRunOptimizationAgent,
  runningOptimizationAgentRunId,
  onRetryOptimizationAsset,
  retryingOptimizationAssetId,
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
  experimentDimensions: PromptEvaluationExperimentDimension[];
  dimensionScoreSummaries: PromptEvaluationDimensionScoreSummary[];
  dimensionScoreTrends: PromptEvaluationDimensionScoreTrend[];
  experimentVersionsByPromptId: Map<string, PromptLibraryVersion[]>;
  runs: PromptEvaluationRun[];
  focusedRunId: string | null;
  evidenceFocus: EvidenceFocus;
  runStatusFilter: RunStatusFilter;
  onRunStatusFilterChange: (status: RunStatusFilter) => void;
  candidates: PromptEvaluationOptimizationCandidate[];
  loading: boolean;
  saving: boolean;
  showAcceptanceFixtures: boolean;
  onShowAcceptanceFixtures: () => void;
  onHideAcceptanceFixtures: () => void;
  onCreateAsset: (assetType: PromptEvaluationAssetType) => void;
  onToggleAssetStatus: (asset: PromptEvaluationAsset) => void;
  onUpdateAsset: (assetId: string, data: UpdatePromptEvaluationAssetRequest) => Promise<unknown>;
  onDeleteAsset: (asset: PromptEvaluationAsset) => void;
  onImportDatasetFromTraces: (asset: PromptEvaluationAsset) => void;
  importingTraceDatasetAssetId: string | null;
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
  onRunOptimizationAgent: (runId: string) => void;
  runningOptimizationAgentRunId: string | null;
  onRetryOptimizationAsset: (assetId: string) => void;
  retryingOptimizationAssetId: string | null;
  onUpdateCandidate: (candidateId: string, data: UpdatePromptEvaluationOptimizationCandidateRequest) => void;
  updatingCandidateId: string | null;
  onPublishCandidate: (candidateId: string) => void;
  publishingCandidateId: string | null;
  onRejectCandidate: (candidateId: string, reason: string) => void;
  rejectingCandidateId: string | null;
}) {
  const tabAssetType = tabToAssetType(activeTab);
  const tabAssets = tabAssetType ? assets.filter((asset) => asset.asset_type === tabAssetType) : assets;
  const acceptanceFixtureAssets = tabAssets.filter((asset) =>
    isAcceptanceFixtureText(asset.name, asset.description, asset.payload),
  );
  const visibleAssets = showAcceptanceFixtures
    ? tabAssets
    : tabAssets.filter((asset) => !acceptanceFixtureAssets.includes(asset));
  const acceptanceFixtureCandidates = candidates.filter((candidate) =>
    isAcceptanceFixtureText(
      candidate.candidate_name,
      candidate.candidate_content,
      candidate.rationale,
      candidate.metrics,
    ),
  );
  const visibleCandidates = showAcceptanceFixtures
    ? candidates
    : candidates.filter((candidate) => !acceptanceFixtureCandidates.includes(candidate));

  if (activeTab === "提示词库" || activeTab === "提示词调试场" || activeTab === "智能体调试场") {
    return null;
  }

  const routeIntro = trainingRouteIntro(activeTab, {
    visibleAssets,
    cases,
    experimentDimensions,
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
          <Button size="sm" onClick={() => onCreateAsset(tabAssetType)} disabled={saving}>
            {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
            新建{tabAssetType}
          </Button>
        ) : null}
      />

      <TrainingRouteWorkspaceBand
        activeTab={activeTab}
        route={routeIntro.route}
        visibleAssets={visibleAssets}
        cases={cases}
        experimentDimensions={experimentDimensions}
        runs={runs}
        candidates={visibleCandidates}
        runStatusFilter={runStatusFilter}
      />
      <AcceptanceFixtureNotice
        count={acceptanceFixtureAssets.length + acceptanceFixtureCandidates.length}
        noun="训练证据"
        showing={showAcceptanceFixtures}
        onShow={onShowAcceptanceFixtures}
        onHide={onHideAcceptanceFixtures}
      />

      {activeTab === "运行历史" && (
        <RunHistoryPanel
          workspaceId={workspaceId}
          runs={runs}
          focusedRunId={focusedRunId}
          evidenceFocus={evidenceFocus}
          runStatusFilter={runStatusFilter}
          onRunStatusFilterChange={onRunStatusFilterChange}
          candidates={visibleCandidates}
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
          onRunOptimizationAgent={onRunOptimizationAgent}
          runningOptimizationAgentRunId={runningOptimizationAgentRunId}
        />
      )}

      {activeTab !== "运行历史" && (
        <TrainingAssetPanel
          activeTab={activeTab}
          route={routeIntro.route}
          title={routeIntro.title}
          assets={visibleAssets}
          runs={runs}
          cases={cases}
          experimentDimensions={experimentDimensions}
          loading={loading}
          saving={saving}
          onToggleAssetStatus={onToggleAssetStatus}
          onUpdateAsset={onUpdateAsset}
          onDeleteAsset={onDeleteAsset}
          onImportDatasetFromTraces={onImportDatasetFromTraces}
          importingTraceDatasetAssetId={importingTraceDatasetAssetId}
          onCreateDatasetVersion={onCreateDatasetVersion}
          creatingDatasetVersionAssetId={creatingDatasetVersionAssetId}
          onCreateCase={onCreateCase}
          creatingCaseAssetId={creatingCaseAssetId}
          caseDrafts={caseDrafts}
          onCaseDraftsChange={onCaseDraftsChange}
          onUpdateCase={onUpdateCase}
          updatingCaseId={updatingCaseId}
          onDeleteCase={onDeleteCase}
          deletingCaseId={deletingCaseId}
          onCreateAssetEvidenceSnapshots={onCreateAssetEvidenceSnapshots}
          creatingAssetEvidenceSnapshotsAssetId={creatingAssetEvidenceSnapshotsAssetId}
          onDownloadAssetEvidencePackage={onDownloadAssetEvidencePackage}
          exportingAssetEvidencePackageAssetId={exportingAssetEvidencePackageAssetId}
          beforeAssetList={activeTab === "实验" ? (
            <ExperimentComparisonPanel
              experiments={visibleAssets}
              dimensions={experimentDimensions}
              dimensionScoreSummaries={dimensionScoreSummaries}
              dimensionScoreTrends={dimensionScoreTrends}
              versionsByPromptId={experimentVersionsByPromptId}
              runs={runs}
            />
          ) : activeTab === "优化运行" ? (
            <>
              <OptimizationStudioPanel
                workspaceId={workspaceId}
                assets={visibleAssets}
                runs={runs}
                candidates={visibleCandidates}
                onCancelRun={onCancelRun}
                cancellingRunId={cancellingRunId}
                onCreateEvidenceSnapshot={onCreateEvidenceSnapshot}
                creatingEvidenceSnapshotRunId={creatingEvidenceSnapshotRunId}
                onRetryOptimizationAsset={onRetryOptimizationAsset}
                retryingOptimizationAssetId={retryingOptimizationAssetId}
              />
              <OptimizationCandidateList
                candidates={visibleCandidates}
                onUpdateCandidate={onUpdateCandidate}
                updatingCandidateId={updatingCandidateId}
                onPublishCandidate={onPublishCandidate}
                publishingCandidateId={publishingCandidateId}
                onRejectCandidate={onRejectCandidate}
                rejectingCandidateId={rejectingCandidateId}
              />
            </>
          ) : null}
        />
      )}
    </section>
  );
}

type ExperimentComparisonRow = {
  asset: PromptEvaluationAsset;
  dimensions: PromptEvaluationExperimentDimension[];
  dimensionScoreRows: ExperimentDimensionScoreRow[];
  dimensionTrendRows: ExperimentDimensionScoreTrendRow[];
  promptVersions: PromptLibraryVersion[];
  versionRows: ExperimentPromptVersionRow[];
  promptVersionRunCount: number;
  qualityExplanation: ExperimentQualityExplanation;
  runs: PromptEvaluationRun[];
  totalCases: number;
  passedCases: number;
  failedCases: number;
  passRate: number;
  totalDurationMs: number;
  inputTokens: number;
  outputTokens: number;
  estimatedCost: number;
  latestRun: PromptEvaluationRun | null;
};

type ExperimentQualityExplanation = {
  qualityLabel: string;
  costPerPassedCase: number;
  costConclusion: string;
  durationConclusion: string;
  failureConclusion: string;
  recommendation: string;
};

type ExperimentDimensionScoreRow = {
  dimensionIndex: number;
  dimensionName: string;
  score: number;
  passedCases: number;
  totalCases: number;
  runCount: number;
  scoredRunCount: number;
  status: string;
  rule: string;
  evidence: string;
};

type ExperimentDimensionScoreTrendRow = {
  dimensionIndex: number;
  dimensionName: string;
  period: string;
  promptVersion: number;
  score: number;
  passedCases: number;
  totalCases: number;
  runCount: number;
  scoredRunCount: number;
  status: string;
};

type ExperimentPromptVersionRow = {
  version: PromptLibraryVersion;
  previousVersion: PromptLibraryVersion | null;
  runCount: number;
  passRate: number;
  estimatedCost: number;
  contentDelta: number;
};

function ExperimentComparisonPanel({
  experiments,
  dimensions,
  dimensionScoreSummaries,
  dimensionScoreTrends,
  versionsByPromptId,
  runs,
}: {
  experiments: PromptEvaluationAsset[];
  dimensions: PromptEvaluationExperimentDimension[];
  dimensionScoreSummaries: PromptEvaluationDimensionScoreSummary[];
  dimensionScoreTrends: PromptEvaluationDimensionScoreTrend[];
  versionsByPromptId: Map<string, PromptLibraryVersion[]>;
  runs: PromptEvaluationRun[];
}) {
  const dimensionsByAsset = useMemo(() => buildExperimentDimensionsByAsset(dimensions), [dimensions]);
  const dimensionScoreSummariesByAsset = useMemo(() => buildDimensionScoreSummariesByAsset(dimensionScoreSummaries), [dimensionScoreSummaries]);
  const dimensionScoreTrendsByAsset = useMemo(() => buildDimensionScoreTrendsByAsset(dimensionScoreTrends), [dimensionScoreTrends]);
  const rows = useMemo(
    () => buildExperimentComparisonRows(experiments, dimensionsByAsset, dimensionScoreSummariesByAsset, dimensionScoreTrendsByAsset, versionsByPromptId, runs),
    [experiments, dimensionsByAsset, dimensionScoreSummariesByAsset, dimensionScoreTrendsByAsset, versionsByPromptId, runs],
  );
  const totalRuns = rows.reduce((sum, row) => sum + row.runs.length, 0);
  const totalCost = rows.reduce((sum, row) => sum + row.estimatedCost, 0);
  const bestRow = rows.find((row) => row.runs.length > 0);

  return (
    <section className="grid gap-3 rounded-md border border-border/70 bg-muted/10 p-3" data-testid="experiment-comparison-panel">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">实验对比排行</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            按实验资产聚合最近运行，直接对比通过率、失败数、耗时、令牌和预估成本。
          </p>
        </div>
        <div className="grid grid-cols-3 gap-2 text-right text-xs">
          <div className="rounded border bg-background px-2 py-1">
            <div className="text-muted-foreground">实验</div>
            <div className="font-semibold">{formatNumber(experiments.length)}</div>
          </div>
          <div className="rounded border bg-background px-2 py-1">
            <div className="text-muted-foreground">运行</div>
            <div className="font-semibold">{formatNumber(totalRuns)}</div>
          </div>
          <div className="rounded border bg-background px-2 py-1">
            <div className="text-muted-foreground">成本</div>
            <div className="font-semibold">{formatMoney(totalCost)}</div>
          </div>
        </div>
      </div>

      {bestRow ? (
        <div className="rounded-md border bg-background px-3 py-2 text-xs" data-testid="experiment-comparison-best">
          当前最优：<span className="font-medium">{bestRow.asset.name}</span>，
          通过率 {formatPercent(bestRow.passRate)}，失败 {formatNumber(bestRow.failedCases)}，成本 {formatMoney(bestRow.estimatedCost)}。
        </div>
      ) : (
        <div className="rounded-md border border-dashed px-3 py-3 text-xs text-muted-foreground" data-testid="experiment-comparison-empty">
          暂无实验运行。先运行实验后，这里会按质量、耗时和成本形成排行。
        </div>
      )}

      {rows.length > 0 && (
        <div className="grid gap-2">
          {rows.map((row, index) => (
            <div
              key={row.asset.id}
              className="grid gap-2 rounded-md border bg-background px-3 py-2 text-xs lg:grid-cols-[minmax(0,1.2fr)_repeat(5,minmax(90px,auto))]"
              data-testid={`experiment-comparison-row-${row.asset.id}`}
            >
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <Badge variant={index === 0 && row.runs.length > 0 ? "secondary" : "outline"}>第 {index + 1}</Badge>
                  <span className="truncate font-medium">{row.asset.name}</span>
                </div>
                <div className="mt-1 text-muted-foreground">
                  {row.dimensions.length} 个维度 · {summarizeLinkedDatasetVersions(row.asset) || "未绑定数据集版本"}
                </div>
                {row.latestRun && (
                  <div className="mt-1 text-muted-foreground">
                    最近运行：{row.latestRun.status} · {displayRunKind(row.latestRun.run_kind)} · {row.latestRun.model || "未记录模型"}
                  </div>
                )}
              </div>
              <MetricCell label="通过率" value={formatPercent(row.passRate)} />
              <MetricCell label="失败数" value={formatNumber(row.failedCases)} />
              <MetricCell label="平均耗时" value={formatDuration(row.runs.length > 0 ? row.totalDurationMs / row.runs.length : 0)} />
              <MetricCell label="令牌" value={`${formatNumber(row.inputTokens)} / ${formatNumber(row.outputTokens)}`} />
              <MetricCell label="预估成本" value={formatMoney(row.estimatedCost)} />
              <ExperimentQualityExplanationPanel row={row} />
              <ExperimentDimensionScoreComparison row={row} />
              <ExperimentPromptVersionComparison row={row} />
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function ExperimentQualityExplanationPanel({ row }: { row: ExperimentComparisonRow }) {
  const explanation = row.qualityExplanation;
  return (
    <div className="grid gap-2 rounded border border-border/70 bg-muted/10 px-2 py-2 text-xs lg:col-span-6" data-testid={`experiment-quality-explanation-${row.asset.id}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="font-medium text-foreground">成本质量解释</div>
          <div className="mt-0.5 text-muted-foreground">
            解释本实验的质量状态、单位通过成本、耗时和失败主因，便于复盘是否值得继续优化。
          </div>
        </div>
        <Badge variant={row.passRate >= 0.8 ? "secondary" : "outline"}>{explanation.qualityLabel}</Badge>
      </div>
      <div className="grid gap-1.5 md:grid-cols-4">
        <MetricCell label="单位通过成本" value={row.passedCases > 0 ? formatMoney(explanation.costPerPassedCase) : "暂无通过"} />
        <MetricCell label="成本判断" value={explanation.costConclusion} />
        <MetricCell label="耗时判断" value={explanation.durationConclusion} />
        <MetricCell label="失败主因" value={explanation.failureConclusion} />
      </div>
      <div className="rounded border bg-background px-2 py-1.5 text-muted-foreground">
        建议动作：<span className="text-foreground">{explanation.recommendation}</span>
      </div>
    </div>
  );
}

function ExperimentDimensionScoreComparison({ row }: { row: ExperimentComparisonRow }) {
  return (
    <div className="grid gap-2 rounded border border-border/70 bg-muted/10 px-2 py-2 text-xs lg:col-span-6" data-testid={`experiment-dimension-score-comparison-${row.asset.id}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="font-medium text-foreground">实验维度评分</div>
          <div className="mt-0.5 text-muted-foreground">
            按服务端中文评分事实聚合，区分已评分维度和待执行维度。
          </div>
        </div>
        <Badge variant="outline">{formatNumber(row.dimensionScoreRows.length)} 个评分维度</Badge>
      </div>
      {row.dimensionScoreRows.length === 0 ? (
        <div className="rounded border border-dashed px-2 py-2 text-muted-foreground">暂无维度评分。运行实验后会根据维度事实生成评分。</div>
      ) : (
        <div className="grid gap-1.5 md:grid-cols-3">
          {row.dimensionScoreRows.map((item) => (
            <div key={`${item.dimensionIndex}:${item.dimensionName}`} className="grid gap-1 rounded border bg-background px-2 py-1.5">
              <div className="flex items-center justify-between gap-2">
                <span className="truncate font-medium text-foreground">{item.dimensionName}</span>
                <Badge variant={item.status === "已评分" ? "secondary" : "outline"}>{item.status}</Badge>
              </div>
              <div className="text-lg font-semibold leading-6">{item.scoredRunCount > 0 ? formatPercent(item.score) : "待评分"}</div>
              <div className="text-muted-foreground">
                {formatNumber(item.passedCases)}/{formatNumber(item.totalCases)} 用例 · {formatNumber(item.scoredRunCount)}/{formatNumber(item.runCount)} 次运行
              </div>
              <div className="truncate text-muted-foreground" title={item.rule}>{item.rule}</div>
              <div className="truncate text-muted-foreground" title={item.evidence}>{item.evidence}</div>
            </div>
          ))}
        </div>
      )}
      <ExperimentDimensionScoreTrend row={row} />
    </div>
  );
}

function ExperimentDimensionScoreTrend({ row }: { row: ExperimentComparisonRow }) {
  const visibleTrends = row.dimensionTrendRows.slice(0, 6);
  return (
    <div className="grid gap-1.5 rounded border bg-background px-2 py-1.5" data-testid={`experiment-dimension-score-trend-${row.asset.id}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="font-medium text-foreground">维度趋势</div>
        <Badge variant="outline">{formatNumber(row.dimensionTrendRows.length)} 条日版本趋势</Badge>
      </div>
      {visibleTrends.length === 0 ? (
        <div className="text-muted-foreground">暂无趋势。运行实验后会按日期和提示词版本生成趋势。</div>
      ) : (
        <div className="grid gap-1 md:grid-cols-2">
          {visibleTrends.map((item) => (
            <div key={`${item.period}:${item.promptVersion}:${item.dimensionIndex}:${item.dimensionName}`} className="flex min-w-0 items-center justify-between gap-2 rounded border px-2 py-1">
              <div className="min-w-0">
                <div className="truncate font-medium">{item.dimensionName}</div>
                <div className="text-muted-foreground">
                  {item.period} · v{item.promptVersion || "未记录"} · {formatNumber(item.scoredRunCount)}/{formatNumber(item.runCount)} 次运行
                </div>
              </div>
              <div className="shrink-0 text-right">
                <div className="font-semibold">{item.scoredRunCount > 0 ? formatPercent(item.score) : "待评分"}</div>
                <div className="text-muted-foreground">{formatNumber(item.passedCases)}/{formatNumber(item.totalCases)}</div>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function ExperimentPromptVersionComparison({ row }: { row: ExperimentComparisonRow }) {
  if (!row.asset.prompt_id) {
    return (
      <div className="rounded border border-dashed px-2 py-2 text-xs text-muted-foreground lg:col-span-6" data-testid={`experiment-prompt-version-comparison-${row.asset.id}`}>
        该实验未绑定提示词，暂不能做提示词版本对比。
      </div>
    );
  }
  return (
    <div className="grid gap-2 rounded border border-border/70 bg-muted/10 px-2 py-2 text-xs lg:col-span-6" data-testid={`experiment-prompt-version-comparison-${row.asset.id}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="font-medium text-foreground">提示词版本对比</div>
          <div className="mt-0.5 text-muted-foreground">
            对比同一提示词最近版本的来源、内容变化和已记录的版本级运行归因。
          </div>
        </div>
        <Badge variant="outline">已加载 {formatNumber(row.promptVersions.length)} 个版本</Badge>
      </div>
      {row.promptVersions.length === 0 ? (
        <div className="rounded border border-dashed px-2 py-2 text-muted-foreground">暂无提示词版本历史。</div>
      ) : (
        <div className="grid gap-1.5">
          {row.versionRows.slice(0, 4).map((item) => (
            <div key={item.version.id} className="grid gap-1 rounded border bg-background px-2 py-1.5 md:grid-cols-[minmax(0,1fr)_repeat(4,minmax(78px,auto))]">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium text-foreground">v{item.version.version}</span>
                  <Badge variant={item.version.source === "优化候选发布" ? "secondary" : "outline"}>{item.version.source}</Badge>
                  {item.previousVersion ? <span className="text-muted-foreground">相对 v{item.previousVersion.version}</span> : <span className="text-muted-foreground">基线版本</span>}
                </div>
                <div className="mt-1 truncate text-muted-foreground">{item.version.content}</div>
              </div>
              <MetricCell label="内容变化" value={formatSignedNumber(item.contentDelta)} />
              <MetricCell label="版本运行" value={formatNumber(item.runCount)} />
              <MetricCell label="通过率" value={item.runCount > 0 ? formatPercent(item.passRate) : "未记录"} />
              <MetricCell label="成本" value={formatMoney(item.estimatedCost)} />
            </div>
          ))}
          {row.runs.length > row.promptVersionRunCount && (
            <div className="rounded border border-dashed px-2 py-2 text-muted-foreground" data-testid={`experiment-prompt-version-unattributed-${row.asset.id}`}>
              {formatNumber(row.runs.length - row.promptVersionRunCount)} 次运行未记录提示词版本；旧运行会按实验归属保留，但不能冒充版本级对比证据。
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function TrainingAssetPanel({
  activeTab,
  route,
  title,
  assets,
  runs,
  cases,
  experimentDimensions,
  loading,
  saving,
  onToggleAssetStatus,
  onUpdateAsset,
  onDeleteAsset,
  onImportDatasetFromTraces,
  importingTraceDatasetAssetId,
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
  experimentDimensions: PromptEvaluationExperimentDimension[];
  loading: boolean;
  saving: boolean;
  onToggleAssetStatus: (asset: PromptEvaluationAsset) => void;
  onUpdateAsset: (assetId: string, data: UpdatePromptEvaluationAssetRequest) => Promise<unknown>;
  onDeleteAsset: (asset: PromptEvaluationAsset) => void;
  onImportDatasetFromTraces: (asset: PromptEvaluationAsset) => void;
  importingTraceDatasetAssetId: string | null;
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
  onCreateAssetEvidenceSnapshots: (assetId: string) => void;
  creatingAssetEvidenceSnapshotsAssetId: string | null;
  onDownloadAssetEvidencePackage: (assetId: string) => void;
  exportingAssetEvidencePackageAssetId: string | null;
  beforeAssetList?: ReactNode;
}) {
  const caseSummaries = useMemo(() => buildCaseSummaries(cases), [cases]);
  const casesByAsset = useMemo(() => buildCasesByAsset(cases), [cases]);
  const experimentDimensionsByAsset = useMemo(() => buildExperimentDimensionsByAsset(experimentDimensions), [experimentDimensions]);
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
                  onUpdateAsset={onUpdateAsset}
                  onUpdateCase={onUpdateCase}
                  updatingCaseId={updatingCaseId}
                  onDeleteCase={onDeleteCase}
                  deletingCaseId={deletingCaseId}
                />
              )}
              {asset.asset_type === "实验" && (
                <ExperimentDimensionPanel asset={asset} dimensions={experimentDimensionsByAsset.get(asset.id) ?? []} />
              )}
            </div>
          ))}
        </div>
      )}
    </section>
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
  onRunOptimizationAgent,
  runningOptimizationAgentRunId,
}: {
  workspaceId: string;
  runs: PromptEvaluationRun[];
  focusedRunId: string | null;
  evidenceFocus: EvidenceFocus;
  runStatusFilter: RunStatusFilter;
  onRunStatusFilterChange: (status: RunStatusFilter) => void;
  candidates: PromptEvaluationOptimizationCandidate[];
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
  onRunOptimizationAgent: (runId: string) => void;
  runningOptimizationAgentRunId: string | null;
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
                      <>
                        <Button
                          size="sm"
                          variant="secondary"
                          onClick={() => onRunOptimizationAgent(run.id)}
                          disabled={runningOptimizationAgentRunId === run.id || run.status === "已入队" || run.status === "运行中"}
                        >
                          {runningOptimizationAgentRunId === run.id ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
                          智能体优化任务
                        </Button>
                        <Button
                          size="sm"
                          variant="secondary"
                          onClick={() => onGenerateCandidate(run.id)}
                          disabled={generatingCandidateRunId === run.id || hasPendingCandidate}
                        >
                          {generatingCandidateRunId === run.id ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
                          {hasPendingCandidate ? "已有候选" : "生成优化候选"}
                        </Button>
                      </>
                    )}
                  </div>
                  {expandedRunId === run.id && (
                    <RunEvidencePanel
                      evidence={evidenceQuery.data ?? null}
                      snapshots={evidenceSnapshotQuery.data?.items ?? []}
                      snapshotsLoading={evidenceSnapshotQuery.isLoading || evidenceSnapshotQuery.isFetching}
                      loading={evidenceQuery.isLoading || evidenceQuery.isFetching}
                      error={evidenceQuery.isError}
                      evidenceFocus={evidenceFocus}
                      optimizationActions={{
                        canGenerate: canGenerateOptimizationCandidate(run),
                        hasPendingCandidate,
                        generatingCandidate: generatingCandidateRunId === run.id,
                        runningOptimizationAgent: runningOptimizationAgentRunId === run.id,
                        onGenerateCandidate: () => onGenerateCandidate(run.id),
                        onRunOptimizationAgent: () => onRunOptimizationAgent(run.id),
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

function emptyTrainingRouteText(activeTab: WorkbenchTab) {
  switch (activeTab) {
    case "数据集":
      return "暂无数据集题库，先新建数据集或从 trace 导入样本";
    case "测试套件":
      return "暂无测试套件，先把稳定用例组织成可回归的套件";
    case "实验":
      return "暂无实验，先创建实验来对比提示词、变量和执行方式";
    case "优化运行":
      return "暂无优化运行作业，先创建优化运行资产并从失败结果生成候选";
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
    experimentDimensions: PromptEvaluationExperimentDimension[];
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
        evidence: "页面可创建套件资产、维护手工用例，并通过运行历史回读每次套件执行结果。",
      };
    }
    case "实验": {
      return {
        route: "experiments",
        title: "实验对比",
        subtitle: "对比不同提示词、变量、数据集或执行方式，沉淀质量、成本和中文一致性的实验事实。",
        facts: [
          ["实验资产", String(context.visibleAssets.length)],
          ["启用", String(enabledAssets)],
          ["维度事实", formatNumber(context.experimentDimensions.length)],
          ["结构化用例", formatNumber(context.cases.length)],
        ],
        evidence: "页面展示实验维度事实，并与运行历史、证据快照和优化候选形成可复盘链路。",
      };
    }
    case "优化运行": {
      const activeRuns = context.runs.filter((run) => run.status === "已入队" || run.status === "运行中").length;
      return {
        route: "optimization-runs",
        title: "优化运行作业台",
        subtitle: "按优化运行资产聚合作业、运行、候选和证据，用失败结果推动下一版提示词改进。",
        facts: [
          ["优化作业", String(context.visibleAssets.length)],
          ["活动运行", formatNumber(activeRuns)],
          ["优化候选", formatNumber(context.candidates.length)],
          ["已发布", formatNumber(context.candidates.filter((candidate) => candidate.status === "已发布").length)],
        ],
        evidence: "作业台可展开运行证据、取消活动运行，并查看候选的确认、发布或拒绝结果。",
      };
    }
    case "运行历史": {
      const reviewRuns = context.runs.filter((run) => run.status === "需人工复核").length;
      return {
        route: "run-history",
        title: context.runStatusFilter === "需人工复核" ? "人工复核队列" : "运行历史与证据",
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
  experimentDimensions,
  runs,
  candidates,
  runStatusFilter,
}: {
  activeTab: WorkbenchTab;
  route: string;
  visibleAssets: PromptEvaluationAsset[];
  cases: PromptEvaluationStructuredCase[];
  experimentDimensions: PromptEvaluationExperimentDimension[];
  runs: PromptEvaluationRun[];
  candidates: PromptEvaluationOptimizationCandidate[];
  runStatusFilter: RunStatusFilter;
}) {
  const config = trainingRouteOperatingModel(activeTab, {
    visibleAssets,
    cases,
    experimentDimensions,
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
    experimentDimensions: PromptEvaluationExperimentDimension[];
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
          { label: "执行", title: "反复运行同一试卷", detail: "运行历史会记录通过率、失败原因、耗时和 token" },
          { label: "定位", title: "断言级复盘", detail: "失败后可跳到运行证据、生成候选或进入人工复核" },
        ],
      };
    }
    case "实验": {
      return {
        kicker: "实验工作台",
        title: "变量矩阵、版本绑定、横向排行",
        description: "实验页面关注对比：同一数据集版本下比较不同提示词、变量、执行方式和模型策略，沉淀质量、耗时和成本事实。",
        badge: "对比矩阵",
        className: "border-amber-500/30 bg-amber-500/5",
        steps: [
          { label: "维度", title: "实验维度事实", detail: `${formatNumber(context.experimentDimensions.length)} 个维度用于说明比较对象` },
          { label: "绑定", title: "明确数据集版本", detail: "实验应引用固定版本，避免输入样本变化污染对比结论" },
          { label: "排行", title: "质量和成本横向比较", detail: `${formatNumber(context.runs.length)} 条运行可用于通过率、失败数和成本排行` },
        ],
      };
    }
    case "优化运行": {
      const pending = context.candidates.filter((candidate) => candidate.status === "待确认").length;
      const published = context.candidates.filter((candidate) => candidate.status === "已发布").length;
      return {
        kicker: "优化运行工作台",
        title: "失败样本、候选生成、人工发布",
        description: "优化运行页面关注闭环作业：从失败运行提取证据，生成候选提示词，由人确认、编辑、发布或拒绝。",
        badge: "优化闭环",
        className: "border-rose-500/30 bg-rose-500/5",
        steps: [
          { label: "来源", title: "失败运行和复盘证据", detail: `${formatNumber(context.runs.filter((run) => run.status === "未通过" || run.status === "失败").length)} 条失败/异常运行可作为优化来源` },
          { label: "候选", title: "待确认优化候选", detail: `${formatNumber(pending)} 个待确认，${formatNumber(published)} 个已发布` },
          { label: "发布", title: "人工把关新版本", detail: "候选发布后进入提示词版本链，拒绝原因也会留下处理证据" },
        ],
      };
    }
    case "运行历史": {
      const taskRuns = context.runs.filter((run) => Boolean(run.task_id)).length;
      const reviewRuns = context.runs.filter((run) => run.status === "需人工复核").length;
      return {
        kicker: "运行历史工作台",
        title: "运行检索、证据展开、人工复核",
        description: "运行历史页面关注证据检索：按状态筛选运行，展开 task、message、trace、usage、span 和服务端快照。",
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

function OptimizationStudioPanel({
  workspaceId,
  assets,
  runs,
  candidates,
  onCancelRun,
  cancellingRunId,
  onCreateEvidenceSnapshot,
  creatingEvidenceSnapshotRunId,
  onRetryOptimizationAsset,
  retryingOptimizationAssetId,
}: {
  workspaceId: string;
  assets: PromptEvaluationAsset[];
  runs: PromptEvaluationRun[];
  candidates: PromptEvaluationOptimizationCandidate[];
  onCancelRun: (runId: string) => void;
  cancellingRunId: string | null;
  onCreateEvidenceSnapshot: (runId: string) => void;
  creatingEvidenceSnapshotRunId: string | null;
  onRetryOptimizationAsset: (assetId: string) => void;
  retryingOptimizationAssetId: string | null;
}) {
  const [expandedRunId, setExpandedRunId] = useState<string | null>(null);
  const runsByAsset = useMemo(() => {
    const result = new Map<string, PromptEvaluationRun[]>();
    for (const run of runs) {
      const bucket = result.get(run.asset_id) ?? [];
      bucket.push(run);
      result.set(run.asset_id, bucket);
    }
    for (const bucket of result.values()) {
      bucket.sort((a, b) => b.created_at.localeCompare(a.created_at));
    }
    return result;
  }, [runs]);
  const candidatesByRun = useMemo(() => buildCandidatesByRun(candidates), [candidates]);
  const activeRuns = runs.filter(canCancelPromptEvaluationRun);
  const publishedCandidates = candidates.filter((candidate) => candidate.status === "已发布").length;
  const evidenceQuery = useQuery({
    queryKey: promptLibraryKeys.runEvidence(workspaceId, expandedRunId),
    queryFn: () => api.getPromptEvaluationRunEvidence(expandedRunId ?? ""),
    enabled: !!workspaceId && !!expandedRunId,
  });
  const evidenceSnapshotQuery = useQuery({
    queryKey: promptLibraryKeys.runEvidenceSnapshots(workspaceId, expandedRunId),
    queryFn: () => api.listPromptEvaluationEvidenceSnapshots(expandedRunId ?? "", 5),
    enabled: !!workspaceId && !!expandedRunId,
  });

  return (
    <section className="grid gap-3 rounded-md border border-border/70 bg-muted/10 p-3" data-testid="optimization-studio-panel">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <h4 className="text-sm font-semibold">优化运行作业台</h4>
          <p className="mt-1 text-xs text-muted-foreground">
            汇总优化运行资产、真实运行、候选版本和取消入口，避免优化运行只是一组分散候选。
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Badge variant="outline">资产 {assets.length}</Badge>
          <Badge variant="outline">运行 {runs.length}</Badge>
          <Badge variant={activeRuns.length > 0 ? "secondary" : "outline"}>活动 {activeRuns.length}</Badge>
          <Badge variant="outline">候选 {candidates.length}</Badge>
          <Badge variant="outline">已发布 {publishedCandidates}</Badge>
        </div>
      </div>

      {assets.length === 0 ? (
        <div className="rounded-md border border-dashed px-3 py-5 text-center text-sm text-muted-foreground">
          暂无优化运行资产。可以先从提示词调试场记录一次优化运行，或在运行历史里对失败运行创建智能体优化任务。
        </div>
      ) : (
        <div className="grid gap-2">
          {assets.map((asset) => {
            const assetRuns = runsByAsset.get(asset.id) ?? [];
            const latestRun = assetRuns[0] ?? null;
            const candidateCount = assetRuns.reduce((total, run) => total + (candidatesByRun.get(run.id)?.length ?? 0), 0);
            const optimizationContract = buildOptimizationContractSummary(asset);
            const optimizationRounds = optimizationRunRounds(asset);
            const optimizationLogs = optimizationRunLogs(asset);
            return (
              <div key={asset.id} className="grid gap-2 rounded-md border bg-background px-3 py-3" data-testid={`optimization-studio-job-${asset.id}`}>
                <div className="grid gap-2 md:grid-cols-[minmax(0,1fr)_auto] md:items-start">
                  <div className="min-w-0">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="truncate text-sm font-medium">{asset.name}</span>
                      <Badge variant={asset.status === "启用" ? "secondary" : "outline"} className="shrink-0">{asset.status}</Badge>
                    </div>
                    <div className="mt-1 text-xs text-muted-foreground">
                      {asset.description || "未记录优化说明"}
                    </div>
                    <div className="mt-1 text-[11px] text-muted-foreground">
                      配置摘要：{summarizeJSONValue(asset.payload)} · 最近运行 {latestRun ? `${displayRunKind(latestRun.run_kind)} / ${latestRun.status}` : "暂无"}
                    </div>
                  </div>
                  <div className="flex flex-wrap justify-start gap-2 md:justify-end">
                    <Badge variant="outline">运行 {assetRuns.length}</Badge>
                    <Badge variant="outline">轮次 {optimizationRounds.length}</Badge>
                    <Badge variant="outline">候选 {candidateCount}</Badge>
                    <Badge variant="outline">用例 {asset.structured_case_count}</Badge>
                    <Button
                      size="sm"
                      variant="secondary"
                      data-testid={`retry-optimization-run-${asset.id}`}
                      onClick={() => onRetryOptimizationAsset(asset.id)}
                      disabled={!asset.prompt_id || retryingOptimizationAssetId === asset.id}
                    >
                      {retryingOptimizationAssetId === asset.id ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
                      重试优化运行
                    </Button>
                  </div>
                </div>

                <div className="grid gap-2 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
                  <OptimizationRoundPanel assetId={asset.id} contract={optimizationContract} rounds={optimizationRounds} />
                  <OptimizationLogStreamPanel assetId={asset.id} logs={optimizationLogs} />
                </div>

                {assetRuns.length > 0 && (
                  <div className="grid gap-1.5">
                    {assetRuns.slice(0, 3).map((run) => (
                      <div key={run.id} className="grid gap-2 rounded-sm border bg-muted/20 px-2 py-2 text-xs md:grid-cols-[minmax(0,1fr)_auto] md:items-center" data-testid={`optimization-studio-run-${run.id}`}>
                        <div className="min-w-0">
                          <div className="truncate font-medium text-foreground">{displayRunKind(run.run_kind)} · {run.status}</div>
                          <div className="mt-1 truncate text-muted-foreground">
                            运行 {run.id} · 任务 {run.task_id ?? "未绑定"} · 模型 {run.model || "未记录"} · {run.total_duration_ms} ms
                          </div>
                        </div>
                        <div className="flex shrink-0 items-center gap-2">
                          <Badge variant="outline">候选 {candidatesByRun.get(run.id)?.length ?? 0}</Badge>
                          <Button size="sm" variant="secondary" onClick={() => setExpandedRunId(expandedRunId === run.id ? null : run.id)}>
                            {expandedRunId === run.id ? "收起证据" : "查看证据"}
                          </Button>
                          {canCancelPromptEvaluationRun(run) && (
                            <Button size="sm" variant="destructive" onClick={() => onCancelRun(run.id)} disabled={cancellingRunId === run.id}>
                              {cancellingRunId === run.id ? <Loader2 className="size-3.5 animate-spin" /> : <XCircle className="size-3.5" />}
                              取消运行
                            </Button>
                          )}
                        </div>
                        {expandedRunId === run.id && (
                          <div className="md:col-span-2">
                            <RunEvidencePanel
                              evidence={evidenceQuery.data ?? null}
                              snapshots={evidenceSnapshotQuery.data?.items ?? []}
                              snapshotsLoading={evidenceSnapshotQuery.isLoading || evidenceSnapshotQuery.isFetching}
                              loading={evidenceQuery.isLoading || evidenceQuery.isFetching}
                              error={evidenceQuery.isError}
                              creatingSnapshot={creatingEvidenceSnapshotRunId === run.id}
                              onCreateSnapshot={() => onCreateEvidenceSnapshot(run.id)}
                            />
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            );
          })}
        </div>
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
        const candidateMetrics = candidate.metrics as Record<string, unknown>;
        const hasManualEdit = Boolean(candidateMetrics["人工编辑"]);
        const candidatePriority = stringFromRecord(candidateMetrics, "候选优先级");
        const weakDimensions = promptEvaluationCandidateWeakDimensions(candidateMetrics["失败维度"]);
        return (
          <div key={candidate.id} data-testid={`prompt-evaluation-candidate-${candidate.id}`} className="grid gap-2 px-3 py-3 md:grid-cols-[minmax(0,1fr)_auto]">
            <div className="min-w-0">
              <div className="flex min-w-0 items-center gap-2">
                <span className="truncate text-sm font-medium">{candidate.candidate_name}</span>
                <Badge variant={canHandle ? "secondary" : "outline"} className="shrink-0">
                  {candidate.status} · 失败 {candidate.failed_case_count}
                </Badge>
                {candidatePriority && <Badge variant={candidatePriority === "高" ? "destructive" : "outline"} className="shrink-0">优先级 {candidatePriority}</Badge>}
                {hasManualEdit && <Badge variant="outline" className="shrink-0">已人工编辑</Badge>}
              </div>
              <div className="mt-1 text-xs text-muted-foreground">{candidate.rationale || "基于失败用例生成，等待人工确认。"}</div>
              {weakDimensions.length > 0 && (
                <div className="mt-2 flex flex-wrap gap-1" data-testid={`optimization-candidate-weak-dimensions-${candidate.id}`}>
                  {weakDimensions.slice(0, 4).map((item) => (
                    <Badge key={`${item.name}:${item.priority}`} variant="outline" className="max-w-full truncate">
                      {item.name} · {item.priority} · {item.score}
                    </Badge>
                  ))}
                </div>
              )}
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

type OptimizationContractSummary = {
  schema: string;
  retryEntry: string;
  humanReview: string;
  sourceRun: string;
};

type OptimizationRoundSummary = {
  round: string;
  retry: string;
  status: string;
  runId: string;
  taskId: string;
  model: string;
  createdAt: string;
};

type OptimizationLogSummary = {
  seq: string;
  event: string;
  status: string;
  round: string;
  message: string;
  createdAt: string;
};

function OptimizationRoundPanel({
  assetId,
  contract,
  rounds,
}: {
  assetId: string;
  contract: OptimizationContractSummary;
  rounds: OptimizationRoundSummary[];
}) {
  return (
    <section className="rounded-md border border-border/70 bg-muted/15 p-2 text-xs" data-testid={`optimization-studio-rounds-${assetId}`}>
      <div className="flex items-center justify-between gap-2">
        <div className="font-medium text-muted-foreground">优化轮次</div>
        <Badge variant={contract.schema ? "secondary" : "outline"}>{contract.schema || "未声明契约"}</Badge>
      </div>
      <div className="mt-1 text-[11px] leading-5 text-muted-foreground">
        重试入口：{contract.retryEntry || "未记录"} · 人工确认：{contract.humanReview || "未记录"}
      </div>
      {rounds.length === 0 ? (
        <div className="mt-2 rounded border border-dashed px-2 py-2 text-muted-foreground">暂无轮次记录；创建或重试优化运行后会写入。</div>
      ) : (
        <div className="mt-2 grid gap-1.5">
          {rounds.slice(0, 4).map((round) => (
            <div key={`${round.runId}-${round.round}-${round.retry}`} className="rounded border bg-background px-2 py-1.5">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium text-foreground">轮次 {round.round}</span>
                <Badge variant={round.status === "已入队" || round.status === "运行中" ? "outline" : "secondary"}>重试 {round.retry}</Badge>
                <span className="text-muted-foreground">{round.status || "未知状态"}</span>
              </div>
              <div className="mt-1 break-all text-[11px] leading-5 text-muted-foreground">
                运行 {round.runId || "未记录"} · 任务 {round.taskId || "未记录"} · 模型 {round.model || "未记录"} · {round.createdAt || "未记录时间"}
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function OptimizationLogStreamPanel({ assetId, logs }: { assetId: string; logs: OptimizationLogSummary[] }) {
  return (
    <section className="rounded-md border border-border/70 bg-muted/15 p-2 text-xs" data-testid={`optimization-studio-log-stream-${assetId}`}>
      <div className="flex items-center justify-between gap-2">
        <div className="font-medium text-muted-foreground">日志流</div>
        <Badge variant="outline">{logs.length} 条</Badge>
      </div>
      {logs.length === 0 ? (
        <div className="mt-2 rounded border border-dashed px-2 py-2 text-muted-foreground">暂无日志流；优化运行入队、重试和同步会继续追加。</div>
      ) : (
        <div className="mt-2 grid gap-1.5">
          {logs.slice(-5).reverse().map((log) => (
            <div key={`${log.seq}-${log.createdAt}-${log.event}`} className="rounded border bg-background px-2 py-1.5">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium text-foreground">#{log.seq || "?"} {log.event || "未命名事件"}</span>
                <Badge variant="outline">轮次 {log.round || "?"}</Badge>
                <span className="text-muted-foreground">{log.status || "未知状态"}</span>
              </div>
              <div className="mt-1 text-[11px] leading-5 text-muted-foreground">{log.message || "未记录消息"} · {log.createdAt || "未记录时间"}</div>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function buildOptimizationContractSummary(asset: PromptEvaluationAsset): OptimizationContractSummary {
  const payload = asset.payload ?? {};
  const contract = isRecord(payload["优化运行契约"]) ? payload["优化运行契约"] : {};
  return {
    schema: stringFromUnknown(contract["schema"]) || stringFromUnknown(payload["schema"]) || stringFromUnknown(payload["语义版本"]),
    retryEntry: stringFromUnknown(contract["重试入口"]) || stringFromUnknown((isRecord(payload["重试策略"]) ? payload["重试策略"] : {})["重试入口"]),
    humanReview: stringFromUnknown(contract["人工确认要求"]),
    sourceRun: stringFromUnknown(contract["来源运行"]) || stringFromUnknown(payload["来源运行"]),
  };
}

function optimizationRunRounds(asset: PromptEvaluationAsset): OptimizationRoundSummary[] {
  const payload = asset.payload ?? {};
  const rawRounds = Array.isArray(payload["优化轮次"]) ? payload["优化轮次"] : [];
  if (rawRounds.length === 0) {
    const latest = payload["最近Agent运行"];
    if (!isRecord(latest)) return [];
    return [optimizationRoundFromRecord(latest, 1)];
  }
  return rawRounds.filter(isRecord).map((item, index) => optimizationRoundFromRecord(item, index + 1));
}

function optimizationRoundFromRecord(record: Record<string, unknown>, fallbackRound: number): OptimizationRoundSummary {
  return {
    round: stringFromUnknown(record["轮次"]) || String(fallbackRound),
    retry: stringFromUnknown(record["重试序号"]) || "0",
    status: stringFromUnknown(record["状态"]),
    runId: stringFromUnknown(record["运行ID"]) || stringFromUnknown(record["run_id"]),
    taskId: stringFromUnknown(record["任务ID"]) || stringFromUnknown(record["trace/task id"]) || stringFromUnknown(record["trace/任务标识"]),
    model: stringFromUnknown(record["模型"]),
    createdAt: stringFromUnknown(record["创建时间"]) || stringFromUnknown(record["运行时间"]),
  };
}

function optimizationRunLogs(asset: PromptEvaluationAsset): OptimizationLogSummary[] {
  const payload = asset.payload ?? {};
  const rawLogs = Array.isArray(payload["日志流"]) ? payload["日志流"] : [];
  return rawLogs.filter(isRecord).map((item, index) => ({
    seq: stringFromUnknown(item["seq"]) || String(index + 1),
    event: stringFromUnknown(item["事件"]) || stringFromUnknown(item["event"]),
    status: stringFromUnknown(item["状态"]) || stringFromUnknown(item["status"]),
    round: stringFromUnknown(item["轮次"]),
    message: stringFromUnknown(item["消息"]) || stringFromUnknown(item["message"]),
    createdAt: stringFromUnknown(item["记录时间"]) || stringFromUnknown(item["created_at"]),
  }));
}

function candidateToDraft(candidate: PromptEvaluationOptimizationCandidate): UpdatePromptEvaluationOptimizationCandidateRequest {
  return {
    candidate_name: candidate.candidate_name,
    candidate_content: candidate.candidate_content,
    rationale: candidate.rationale,
  };
}

function promptEvaluationCandidateWeakDimensions(value: unknown): Array<{ name: string; priority: string; score: string }> {
  if (!Array.isArray(value)) return [];
  return value
    .map((item) => {
      if (!item || typeof item !== "object" || Array.isArray(item)) return null;
      const record = item as Record<string, unknown>;
      const name = stringFromRecord(record, "维度名称");
      if (!name) return null;
      return {
        name,
        priority: stringFromRecord(record, "优先级") || "中",
        score: stringFromRecord(record, "得分") || "未评分",
      };
    })
    .filter((item): item is { name: string; priority: string; score: string } => item !== null);
}

function RunEvidencePanel({
  evidence,
  snapshots,
  snapshotsLoading,
  loading,
  error,
  evidenceFocus = EMPTY_EVIDENCE_FOCUS,
  optimizationActions,
  creatingSnapshot,
  onCreateSnapshot,
}: {
  evidence: PromptEvaluationRunEvidence | null;
  snapshots: PromptEvaluationEvidenceSnapshot[];
  snapshotsLoading: boolean;
  loading: boolean;
  error: boolean;
  evidenceFocus?: EvidenceFocus;
  optimizationActions?: FailureReviewActions;
  creatingSnapshot: boolean;
  onCreateSnapshot: () => void;
}) {
  useEffect(() => {
    if (!evidence || loading) return;
    const selector = evidenceFocusSelector(evidenceFocus);
    if (!selector) return;
    window.requestAnimationFrame(() => {
      document.querySelector(selector)?.scrollIntoView({ block: "center" });
    });
  }, [evidence, evidenceFocus, loading]);

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
      <EvidenceSnapshotBar
        snapshots={snapshots}
        loading={snapshotsLoading}
        creating={creatingSnapshot}
        onCreate={onCreateSnapshot}
      />
      <div className="grid gap-2 text-xs sm:grid-cols-2 lg:grid-cols-4">
        <MetricChip label="运行类型" value={displayRunKind(evidence.run.run_kind)} />
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
      <EvidenceAnchorSummary evidence={evidence} evidenceFocus={evidenceFocus} />
      <FailureReviewPanel evidence={evidence} evidenceFocus={evidenceFocus} actions={optimizationActions} />
      <ExecutionSpanTreePanel evidence={evidence} evidenceFocus={evidenceFocus} />
      <TraceEventTreePanel evidence={evidence} focusedTraceSeq={evidenceFocus.traceSeq} />

      <div className="grid gap-2">
        <div className="text-xs font-medium text-muted-foreground">用例明细</div>
        {evidence.trials.length === 0 ? (
          <div className="rounded-md border border-dashed px-3 py-3 text-xs text-muted-foreground">暂无单次执行记录</div>
        ) : (
          <div className="divide-y rounded-md border bg-background">
            {evidence.trials.map((trial) => {
              const assertionRows = buildTrialAssertionRows(trial);
              return (
                <div
                  key={trial.id}
                  className={`grid gap-1 px-3 py-2 text-xs ${isFocusedTrial(evidenceFocus.trialAnchor, trial) ? "bg-emerald-500/10 ring-2 ring-inset ring-emerald-500/30" : ""}`}
                  data-testid={`run-evidence-trial-${trial.id}`}
                  data-evidence-anchor={`trial:${trial.id}`}
                  data-evidence-anchor-alias={`trial:${trial.case_index + 1}`}
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate font-medium">{trial.case_name || `用例 ${trial.case_index + 1}`}</span>
                    <Badge variant={trial.status === "通过" ? "secondary" : trial.status === "待执行" ? "outline" : "destructive"}>{trial.status}</Badge>
                    <span className="ml-auto text-muted-foreground">{trial.duration_ms} ms</span>
                  </div>
                  {trial.failure_reason && trial.failure_reason !== "无" && <div className="text-muted-foreground">失败原因：{trial.failure_reason}</div>}
                  {assertionRows.length > 0 && (
                    <div className="grid gap-1" data-testid={`run-evidence-trial-assertions-${trial.id}`}>
                      {assertionRows.map((assertion) => {
                        const focused = isFocusedAssertion(evidenceFocus.assertionAnchor, trial, assertion.index);
                        return (
                          <div
                            key={`${trial.id}-${assertion.index}`}
                            className={`flex min-w-0 flex-wrap items-center gap-2 rounded border px-2 py-1 text-[11px] leading-5 ${
                              focused ? "border-emerald-500 bg-emerald-500/10 ring-2 ring-emerald-500/30" : "bg-muted/20"
                            }`}
                            data-testid={`run-evidence-assertion-${trial.id}-${assertion.index}`}
                            data-evidence-anchor={`assertion:${trial.id}:${assertion.index}`}
                            data-evidence-anchor-alias={`assertion:${trial.case_index + 1}.${assertion.index}`}
                          >
                            <Badge variant={assertion.matched ? "secondary" : "destructive"}>{assertion.matched ? "已命中" : "未命中"}</Badge>
                            <span className="min-w-0 break-words">断言 #{assertion.index}：包含 {assertion.expectedText}</span>
                          </div>
                        );
                      })}
                    </div>
                  )}
                  {trial.rendered_prompt && <pre className="max-h-32 overflow-auto whitespace-pre-wrap rounded bg-muted/30 p-2 text-[11px] leading-5">{trial.rendered_prompt}</pre>}
                </div>
              );
            })}
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
          anchors={evidence.task_messages.map((message) => `message:${message.seq}`)}
          focusedAnchor={evidenceFocus.messageSeq ? `message:${evidenceFocus.messageSeq}` : null}
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

function EvidenceSnapshotBar({
  snapshots,
  loading,
  creating,
  onCreate,
}: {
  snapshots: PromptEvaluationEvidenceSnapshot[];
  loading: boolean;
  creating: boolean;
  onCreate: () => void;
}) {
  const latest = snapshots[0] ?? null;
  const latestSummary = latest?.summary ?? {};
  const status = latest ? stringFromUnknown(latestSummary["运行状态"]) || "未记录" : "暂无服务端归档";
  const taskId = latest ? stringFromUnknown(latestSummary["trace/task id"]) || "无任务标识" : "无任务标识";
  return (
    <div className="flex flex-col gap-2 rounded-md border bg-background px-3 py-2 text-xs sm:flex-row sm:items-center sm:justify-between" data-testid="run-evidence-snapshots">
      <div className="min-w-0">
        <div className="font-medium text-muted-foreground">服务端证据快照</div>
        <div className="mt-1 truncate text-[11px] text-muted-foreground">
          {loading
            ? "正在读取归档状态..."
            : latest
              ? `${latest.snapshot_type} · ${latest.created_at || "未记录时间"} · ${status} · task ${taskId}`
              : "暂无服务端归档；建议在演示前归档一份可复核证据。"}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        <Badge variant={latest ? "secondary" : "outline"} className="text-[11px]">
          {latest ? `${snapshots.length} 条快照` : "未归档"}
        </Badge>
        <Button size="sm" variant="secondary" onClick={onCreate} disabled={creating}>
          {creating ? <Loader2 className="size-3.5 animate-spin" /> : <Archive className="size-3.5" />}
          归档快照
        </Button>
      </div>
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
    `智能体 ${stringFromUnknown(context["执行Agent名称"]) || stringFromUnknown(context["执行Agent"]) || "未记录"}`,
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

function EvidenceList({
  title,
  empty,
  items,
  anchors,
  focusedAnchor,
}: {
  title: string;
  empty: string;
  items: string[];
  anchors?: Array<string | null>;
  focusedAnchor?: string | null;
}) {
  return (
    <div className="grid gap-1.5 rounded-md border bg-background p-2 text-xs">
      <div className="font-medium text-muted-foreground">{title}</div>
      {items.length === 0 ? (
        <div className="text-muted-foreground">{empty}</div>
      ) : (
        <div className="grid gap-1">
          {items.slice(0, 6).map((item, index) => (
            <div
              key={`${title}-${index}`}
              className={`break-words rounded px-2 py-1 text-[11px] leading-5 ${
                focusedAnchor && anchors?.[index] === focusedAnchor ? "bg-emerald-500/10 ring-2 ring-emerald-500/30" : "bg-muted/30"
              }`}
              data-evidence-anchor={anchors?.[index] ?? undefined}
            >
              {item || "空消息"}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

type FailureReviewActions = {
  canGenerate: boolean;
  hasPendingCandidate: boolean;
  generatingCandidate: boolean;
  runningOptimizationAgent: boolean;
  onGenerateCandidate: () => void;
  onRunOptimizationAgent: () => void;
};

function FailureReviewPanel({
  evidence,
  evidenceFocus,
  actions,
}: {
  evidence: PromptEvaluationRunEvidence;
  evidenceFocus: EvidenceFocus;
  actions?: FailureReviewActions;
}) {
  const items = buildFailureReviewItems(evidence);
  if (items.length === 0) return null;
  const handleDownloadReport = () => {
    const markdown = buildFailureReviewMarkdown(evidence, items, window.location.href);
    downloadTextFile(
      markdown,
      `multica-failure-review-${evidence.run.id.slice(0, 8)}-${new Date().toISOString().replace(/[:.]/g, "-")}.md`,
      "text/markdown;charset=utf-8",
    );
    toast.success("失败复盘报告已导出");
  };
  return (
    <div className="grid gap-1.5 rounded-md border border-destructive/30 bg-destructive/5 p-2 text-xs" data-testid="run-evidence-failure-review">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="font-medium text-destructive">失败复盘入口</div>
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant="destructive">{items.length} 条线索</Badge>
          <Button size="sm" variant="outline" onClick={handleDownloadReport} data-testid="run-evidence-failure-download-report">
            <Download className="size-3.5" />
            导出复盘报告
          </Button>
        </div>
      </div>
      <div className="grid gap-1.5 md:grid-cols-2">
        {items.map((item, index) => {
          const focused = evidenceFocus.failureAnchor === item.kind;
          return (
            <div
              key={`${item.kind}-${index}`}
              className={`grid gap-1 rounded border px-2 py-1.5 text-[11px] leading-5 ${
                focused ? "border-destructive bg-destructive/10 ring-2 ring-destructive/30" : "bg-background"
              }`}
              data-testid={`run-evidence-failure-${item.kind}`}
              data-evidence-anchor={item.anchor}
            >
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant="outline">{item.label}</Badge>
                <span className="font-medium text-foreground">{item.title}</span>
              </div>
              <div className="break-words text-muted-foreground">{truncateText(item.detail, 220)}</div>
            </div>
          );
        })}
      </div>
      {actions?.canGenerate && (
        <div className="flex flex-wrap items-center justify-end gap-2 border-t border-destructive/20 pt-2" data-testid="run-evidence-failure-review-actions">
          <Button
            size="sm"
            variant="secondary"
            onClick={actions.onRunOptimizationAgent}
            disabled={actions.runningOptimizationAgent}
            data-testid="run-evidence-failure-run-optimization-agent"
          >
            {actions.runningOptimizationAgent ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
            智能体优化任务
          </Button>
          <Button
            size="sm"
            variant="secondary"
            onClick={actions.onGenerateCandidate}
            disabled={actions.generatingCandidate || actions.hasPendingCandidate}
            data-testid="run-evidence-failure-generate-candidate"
          >
            {actions.generatingCandidate ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
            {actions.hasPendingCandidate ? "已有候选" : "生成优化候选"}
          </Button>
        </div>
      )}
    </div>
  );
}

function downloadTextFile(content: string, filename: string, type: string) {
  const blob = new Blob([content], { type });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

function EvidenceAnchorSummary({
  evidence,
  evidenceFocus,
}: {
  evidence: PromptEvaluationRunEvidence;
  evidenceFocus: EvidenceFocus;
}) {
  const traceCount = evidence.trace_events.length;
  const toolCount = evidence.tool_call_chains.length;
  const trialCount = evidence.trials.length;
  const assertionCount = evidence.trials.reduce((sum, trial) => sum + buildTrialAssertionRows(trial).length, 0);
  const messageCount = evidence.task_messages.length;
  const spanCount = evidence.execution_spans.length;
  const failureCount = buildFailureReviewItems(evidence).length;
  if (traceCount === 0 && toolCount === 0 && trialCount === 0 && assertionCount === 0 && messageCount === 0 && spanCount === 0 && failureCount === 0) return null;
  return (
    <div className="grid gap-1.5 rounded-md border bg-background p-2 text-xs" data-testid="run-evidence-anchor-summary">
      <div className="font-medium text-muted-foreground">证据锚点</div>
      <div className="grid gap-1 text-[11px] leading-5 text-muted-foreground sm:grid-cols-2 lg:grid-cols-3">
        <div>
          trace 锚点：{traceCount > 0 ? `trace=1 到 trace=${traceCount}，或 trace=<事件id>` : "暂无 trace"}
          {evidenceFocus.traceSeq ? ` · 当前定位 trace=${evidenceFocus.traceSeq}` : ""}
        </div>
        <div>
          工具锚点：{toolCount > 0 ? "使用 tool=<工具链id>" : "暂无工具链"}
          {evidenceFocus.toolChainId ? ` · 当前定位 tool=${evidenceFocus.toolChainId}` : ""}
        </div>
        <div>
          用例锚点：{trialCount > 0 ? "使用 trial=<用例id或序号>" : "暂无用例"}
          {evidenceFocus.trialAnchor ? ` · 当前定位 trial=${evidenceFocus.trialAnchor}` : ""}
        </div>
        <div>
          断言锚点：{assertionCount > 0 ? "使用 assertion=<用例id>:<序号> 或 assertion=<用例序号>.<断言序号>" : "暂无断言"}
          {evidenceFocus.assertionAnchor ? ` · 当前定位 assertion=${evidenceFocus.assertionAnchor}` : ""}
        </div>
        <div>
          消息锚点：{messageCount > 0 ? "使用 message=<消息seq>" : "暂无消息"}
          {evidenceFocus.messageSeq ? ` · 当前定位 message=${evidenceFocus.messageSeq}` : ""}
        </div>
        <div>
          span 锚点：{spanCount > 0 ? "使用 span=<span seq或id>" : "暂无 span"}
          {evidenceFocus.spanAnchor ? ` · 当前定位 span=${evidenceFocus.spanAnchor}` : ""}
        </div>
        <div>
          失败锚点：{failureCount > 0 ? "使用 failure=run|trial|assertion|tool|trace" : "暂无失败线索"}
          {evidenceFocus.failureAnchor ? ` · 当前定位 failure=${evidenceFocus.failureAnchor}` : ""}
        </div>
      </div>
    </div>
  );
}

function ExecutionSpanTreePanel({ evidence, evidenceFocus }: { evidence: PromptEvaluationRunEvidence; evidenceFocus: EvidenceFocus }) {
  const spans = evidence.execution_spans ?? [];
  const toolCallChains = evidence.tool_call_chains ?? [];
  const toolCallSummary = evidence.tool_call_summary ?? [];
  const summary = evidence.execution_summary ?? {};
  const tokenMarked = Number(summary["token标记合计"] ?? 0);
  return (
    <section className="grid gap-2 rounded-md border bg-background p-2 text-xs" data-testid="run-evidence-execution-spans">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="font-medium text-muted-foreground">执行观测树</div>
          <div className="mt-1 break-all text-[11px] leading-5 text-muted-foreground">
            根任务 {String(summary["根任务"] ?? evidence.run.task_id ?? evidence.run.id)} · {evidence.run.status} · {evidence.run.trigger_source || "未记录触发来源"}
          </div>
        </div>
        <div className="flex flex-wrap gap-1.5">
          <Badge variant="outline">span {spans.length}</Badge>
          <Badge variant="outline">生命周期 {String(summary["生命周期span数"] ?? 0)}</Badge>
          <Badge variant="outline">工具 {String(summary["工具span数"] ?? 0)}</Badge>
          <Badge variant={toolCallChains.length > 0 ? "secondary" : "outline"}>工具链 {String(summary["工具调用链数"] ?? toolCallChains.length)}</Badge>
          <Badge variant="outline">消息 {String(summary["消息span数"] ?? 0)}</Badge>
          <Badge variant={Number(summary["用量span数"] ?? 0) > 0 ? "secondary" : "outline"}>用量 {String(summary["用量span数"] ?? 0)}</Badge>
          <Badge variant={summary["是否缺失用量"] ? "destructive" : "outline"}>{summary["是否缺失用量"] ? "缺失用量" : "用量正常"}</Badge>
          <Badge variant={tokenMarked > 0 ? "secondary" : "outline"}>token标记 {tokenMarked}</Badge>
        </div>
      </div>
      <ToolCallSummaryPanel rows={toolCallSummary} />
      <ToolCallChainPanel chains={toolCallChains} focusedToolChainId={evidenceFocus.toolChainId} />
      {spans.length === 0 ? (
        <div className="rounded-md border border-dashed px-3 py-3 text-muted-foreground">暂无执行 span；真实任务开始后会从 trace、消息和用量证据中生成观测树。</div>
      ) : (
        <div className="grid gap-1.5">
          {spans.map((span) => (
            <ExecutionSpanNode key={span.id} span={span} focused={isFocusedSpan(evidenceFocus.spanAnchor, span)} />
          ))}
        </div>
      )}
    </section>
  );
}

function ToolCallSummaryPanel({ rows }: { rows: PromptEvaluationRunEvidence["tool_call_summary"] }) {
  if (rows.length === 0) {
    return null;
  }
  return (
    <div className="grid gap-1.5 rounded-md border bg-muted/10 p-2" data-testid="run-evidence-tool-call-summary">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="font-medium text-muted-foreground">工具调用摘要</div>
        <Badge variant="outline">{rows.length} 个工具</Badge>
      </div>
      <div className="grid gap-1.5">
        {rows.map((row) => {
          const categoryText = Object.entries(row.result_categories ?? {})
            .map(([name, count]) => `${name} ${count}`)
            .join(" · ");
          return (
            <div key={row.tool} className="grid gap-1 rounded border bg-background px-2 py-1.5 text-[11px] leading-5" data-testid={`run-evidence-tool-call-summary-${row.tool}`}>
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium text-foreground">{row.tool || "未记录工具"}</span>
                <Badge variant={row.needs_attention ? "destructive" : "secondary"}>{row.needs_attention ? "需要关注" : "结果正常"}</Badge>
                <span className="text-muted-foreground">
                  调用 {row.total_calls} · 配对 {row.paired_calls} · 缺少 {row.missing_result_calls} · 孤立 {row.orphan_result_calls} · 异常线索 {row.failure_signal_calls}
                </span>
              </div>
              <div className="flex flex-wrap items-center gap-2 text-muted-foreground">
                <span>平均耗时 {formatDuration(row.average_duration_ms ?? 0)}</span>
                <span>最慢 {formatDuration(row.max_duration_ms ?? 0)}</span>
                {row.slowest_tool_call_chain_id && <span>最慢链路 {row.slowest_tool_call_chain_id}</span>}
              </div>
              {categoryText && <div className="break-words text-muted-foreground">结果分类：{categoryText}</div>}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function ToolCallChainPanel({
  chains,
  focusedToolChainId,
}: {
  chains: PromptEvaluationRunEvidence["tool_call_chains"];
  focusedToolChainId: string | null;
}) {
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState("全部");
  const visibleChains = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    return chains.filter((chain) => {
      if (statusFilter !== "全部" && chain.status !== statusFilter) return false;
      if (!normalizedQuery) return true;
      const haystack = [chain.tool, chain.status, chain.result_category, chain.summary, chain.output, chain.id, chain.use_seq, chain.result_seq]
        .filter((value) => value != null)
        .map((value) => String(value).toLowerCase())
        .join(" ");
      return haystack.includes(normalizedQuery);
    });
  }, [chains, query, statusFilter]);
  const statuses = useMemo(() => ["全部", ...Array.from(new Set(chains.map((chain) => chain.status).filter(Boolean)))], [chains]);
  if (chains.length === 0) {
    return (
      <div className="rounded-md border border-dashed px-3 py-2 text-[11px] text-muted-foreground" data-testid="run-evidence-tool-call-chains">
        暂无工具调用链；当任务产生工具调用和工具结果时会自动配对展示。
      </div>
    );
  }
  return (
    <div className="grid gap-1.5 rounded-md border bg-muted/10 p-2" data-testid="run-evidence-tool-call-chains">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="font-medium text-muted-foreground">工具调用链</div>
        <Badge variant="outline">
          {visibleChains.length}/{chains.length} 条
        </Badge>
      </div>
      <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_160px]" data-testid="run-evidence-tool-call-chain-filters">
        <Input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索工具、结果、摘要" className="h-8 text-[11px]" aria-label="搜索工具调用链" />
        <select
          value={statusFilter}
          onChange={(event) => setStatusFilter(event.target.value)}
          className="h-8 rounded-md border border-input bg-background px-2 text-[11px]"
          aria-label="筛选工具调用链状态"
        >
          {statuses.map((status) => (
            <option key={status} value={status}>
              {status}
            </option>
          ))}
        </select>
      </div>
      <div className="grid gap-1.5">
        {visibleChains.length === 0 ? (
          <div className="rounded border border-dashed px-2 py-2 text-[11px] text-muted-foreground">没有匹配的工具调用链。</div>
        ) : null}
        {visibleChains.map((chain) => {
          const focused = focusedToolChainId === chain.id;
          return (
            <div
              key={chain.id}
              className={`grid gap-1 rounded border px-2 py-1.5 text-[11px] leading-5 ${
                focused ? "border-emerald-500 bg-emerald-500/10 ring-2 ring-emerald-500/30" : "bg-background"
              }`}
              data-testid={`run-evidence-tool-call-chain-${chain.id}`}
              data-evidence-anchor={`tool:${chain.id}`}
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium text-foreground">{chain.tool || "未记录工具"}</span>
                <Badge variant={chain.status === "已配对" ? "secondary" : chain.status === "缺少结果" ? "destructive" : "outline"}>{chain.status || "未记录"}</Badge>
                {chain.result_category && <Badge variant="outline">{chain.result_category}</Badge>}
                {chain.failure_signal && <Badge variant="destructive">异常线索</Badge>}
                <span className="text-muted-foreground">
                  调用 #{chain.use_seq ?? "-"} · 结果 #{chain.result_seq ?? "-"} · 耗时 {formatDuration(chain.duration_ms ?? 0)}
                </span>
              </div>
              <div className="break-words text-muted-foreground">{chain.summary || "未记录摘要"}</div>
              {chain.failure_reason && <div className="break-words text-muted-foreground">异常原因：{chain.failure_reason}</div>}
              {chain.output && <div className="break-words text-muted-foreground">输出：{truncateText(chain.output, 180)}</div>}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function ExecutionSpanNode({ span, focused }: { span: PromptEvaluationRunEvidence["execution_spans"][number]; focused: boolean }) {
  const tone = executionSpanTone(span.span_kind, span.status);
  return (
    <div
      className={`grid gap-1 rounded-md border border-l-4 ${tone} px-3 py-2 ${
        focused ? "border-emerald-500 bg-emerald-500/10 ring-2 ring-emerald-500/30" : "bg-muted/15"
      }`}
      data-testid={`run-evidence-execution-span-${span.seq}`}
      data-evidence-anchor={`span:${span.seq}`}
      data-evidence-anchor-alias={`span:${span.id}`}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[11px] text-muted-foreground">#{span.seq}</span>
        <span className="font-medium text-foreground">{span.span_name || span.span_kind}</span>
        <Badge variant={span.span_kind.includes("缺失") || span.status === "失败" ? "destructive" : span.span_kind.includes("用量") ? "secondary" : "outline"}>{span.span_kind}</Badge>
        <span className="text-muted-foreground">{span.status || "未记录"}</span>
      </div>
      <div className="grid gap-1 text-[11px] leading-5 text-muted-foreground sm:grid-cols-2 lg:grid-cols-4">
        <div className="truncate">时间 {span.created_at || "未记录"}</div>
        <div className="truncate">耗时 {span.duration_ms ? `${span.duration_ms}ms` : "未记录"}</div>
        <div className="truncate">模型 {span.provider || "未记录"}/{span.model || "未记录"}</div>
        <div className="truncate">token {span.token_total}</div>
      </div>
      {span.tool && <div className="break-words text-[11px] leading-5 text-muted-foreground">工具：{span.tool}</div>}
      {span.summary && <div className="break-words text-[11px] leading-5 text-muted-foreground">摘要：{span.summary}</div>}
    </div>
  );
}

function executionSpanTone(spanKind: string, status: string): string {
  if (spanKind.includes("缺失") || status === "失败") return "border-l-destructive/70";
  if (spanKind.includes("用量")) return "border-l-sky-500/70";
  if (spanKind.includes("工具")) return "border-l-amber-500/70";
  if (spanKind.includes("消息")) return "border-l-violet-500/70";
  return "border-l-emerald-500/70";
}

function TraceEventTreePanel({ evidence, focusedTraceSeq }: { evidence: PromptEvaluationRunEvidence; focusedTraceSeq: string | null }) {
  const events = evidence.trace_events;
  const tokenTotal = events.reduce((sum, event) => sum + traceEventTokenTotal(event), 0);
  const lifecycleCount = events.filter((event) => event.event_type.startsWith("task.")).length;
  const usageCount = events.filter((event) => event.event_type === "llm.usage_reported" || traceEventTokenTotal(event) > 0).length;
  const rootTaskId = evidence.run.task_id ?? events[0]?.task_id ?? evidence.run.id;
  return (
    <section className="grid gap-2 rounded-md border bg-background p-2 text-xs" data-testid="run-evidence-trace-tree">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="font-medium text-muted-foreground">任务事件树</div>
          <div className="mt-1 break-all text-[11px] leading-5 text-muted-foreground">
            根任务 {rootTaskId} · {evidence.run.trigger_source || "未记录触发来源"} · {evidence.run.status}
          </div>
        </div>
        <div className="flex flex-wrap gap-1.5">
          <Badge variant="outline">事件 {events.length}</Badge>
          <Badge variant="outline">生命周期 {lifecycleCount}</Badge>
          <Badge variant={usageCount > 0 ? "secondary" : "outline"}>用量事件 {usageCount}</Badge>
          <Badge variant={tokenTotal > 0 ? "secondary" : "outline"}>token {tokenTotal}</Badge>
        </div>
      </div>
      {events.length === 0 ? (
        <div className="rounded-md border border-dashed px-3 py-3 text-muted-foreground">暂无 trace 事件；真实任务开始后会按时间顺序追加生命周期、模型用量和失败原因。</div>
      ) : (
        <div className="grid gap-1.5">
          {events.map((event, index) => (
            <TraceEventTreeNode key={event.id} event={event} index={index} focused={isFocusedTraceEvent(focusedTraceSeq, event, index)} />
          ))}
        </div>
      )}
    </section>
  );
}

function TraceEventTreeNode({
  event,
  index,
  focused,
}: {
  event: PromptEvaluationRunEvidence["trace_events"][number];
  index: number;
  focused: boolean;
}) {
  const tokenTotal = traceEventTokenTotal(event);
  const metadataSummary = traceMetadataSummary(event.metadata);
  return (
    <div
      className={`grid gap-1 rounded-md border border-l-4 border-l-emerald-500/70 px-3 py-2 ${
        focused ? "border-emerald-500 bg-emerald-500/10 ring-2 ring-emerald-500/30" : "bg-muted/15"
      }`}
      data-testid={`run-evidence-trace-node-${index + 1}`}
      data-evidence-anchor={`trace:${index + 1}`}
      data-evidence-anchor-alias={`trace:${event.id}`}
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[11px] text-muted-foreground">#{index + 1}</span>
        <span className="font-medium text-foreground">{event.event_name || traceEventStageLabel(event.event_type)}</span>
        <Badge variant={event.status === "completed" || event.status === "success" ? "secondary" : event.status === "failed" ? "destructive" : "outline"}>
          {traceEventStageLabel(event.event_type)}
        </Badge>
        <span className="text-muted-foreground">{event.status || "未知状态"}</span>
      </div>
      <div className="grid gap-1 text-[11px] leading-5 text-muted-foreground sm:grid-cols-2 lg:grid-cols-4">
        <div className="truncate">时间 {event.created_at || "未记录"}</div>
        <div className="truncate">耗时 {formatTraceEventDuration(event)}</div>
        <div className="truncate">模型 {event.provider || "未记录"}/{event.model || "未记录"}</div>
        <div className="truncate">token {tokenTotal}</div>
      </div>
      {(event.failure_reason && event.failure_reason !== "无") || event.error_type ? (
        <div className="rounded bg-destructive/10 px-2 py-1 text-[11px] leading-5 text-destructive">
          失败原因：{event.failure_reason || "未记录"}{event.error_type ? ` · 错误类型：${event.error_type}` : ""}
        </div>
      ) : null}
      {metadataSummary && <div className="break-words text-[11px] leading-5 text-muted-foreground">元数据：{metadataSummary}</div>}
    </div>
  );
}

function traceEventStageLabel(eventType: string): string {
  switch (eventType) {
    case "task.queued":
      return "任务入队";
    case "task.dispatched":
      return "任务领取";
    case "task.started":
      return "任务开始";
    case "task.waiting_local_directory":
      return "等待本地目录";
    case "task.completed":
      return "任务完成";
    case "task.failed":
      return "任务失败";
    case "task.cancelled":
      return "任务取消";
    case "llm.usage_reported":
      return "模型用量";
    default:
      return eventType || "未分类事件";
  }
}

function traceEventTokenTotal(event: PromptEvaluationRunEvidence["trace_events"][number]): number {
  return event.input_tokens + event.output_tokens + event.cache_read_tokens + event.cache_write_tokens;
}

function formatTraceEventDuration(event: PromptEvaluationRunEvidence["trace_events"][number]): string {
  const parts = [
    event.queue_wait_ms != null ? `排队 ${event.queue_wait_ms}ms` : "",
    event.run_ms != null ? `执行 ${event.run_ms}ms` : "",
    event.duration_ms != null ? `阶段 ${event.duration_ms}ms` : "",
    event.total_ms != null ? `总计 ${event.total_ms}ms` : "",
  ].filter(Boolean);
  return parts.join(" / ") || "未记录";
}

function traceMetadataSummary(metadata: Record<string, unknown>): string {
  return Object.entries(metadata)
    .slice(0, 4)
    .map(([key, value]) => `${key}=${truncateText(stringFromUnknown(value) || JSON.stringify(value) || "", 80)}`)
    .join("，");
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
      detail: "Codex 已领取并执行任务，但上游模型返回额度不足；本次不会产生 token 用量和成本，需补充 Codex/OpenAI 额度后重新运行。",
    };
  }
  if (evidence.run.status === "失败" && evidence.run.task_id && evidence.task_usage.length === 0) {
    return {
      title: "外部依赖失败：未采集到模型用量",
      detail: "智能体任务失败且没有任务用量记录，请结合任务消息和 trace 事件确认运行时、模型或网络依赖状态。",
    };
  }
  return null;
}

function evidenceFocusSelector(focus: EvidenceFocus): string {
  const selectors: string[] = [];
  if (focus.toolChainId) selectors.push(evidenceAnchorSelector("data-evidence-anchor", `tool:${focus.toolChainId}`));
  if (focus.traceSeq) {
    selectors.push(evidenceAnchorSelector("data-evidence-anchor", `trace:${focus.traceSeq}`));
    selectors.push(evidenceAnchorSelector("data-evidence-anchor-alias", `trace:${focus.traceSeq}`));
  }
  if (focus.trialAnchor) {
    selectors.push(evidenceAnchorSelector("data-evidence-anchor", `trial:${focus.trialAnchor}`));
    selectors.push(evidenceAnchorSelector("data-evidence-anchor-alias", `trial:${focus.trialAnchor}`));
  }
  if (focus.assertionAnchor) {
    selectors.push(evidenceAnchorSelector("data-evidence-anchor", `assertion:${focus.assertionAnchor}`));
    selectors.push(evidenceAnchorSelector("data-evidence-anchor-alias", `assertion:${focus.assertionAnchor}`));
  }
  if (focus.messageSeq) selectors.push(evidenceAnchorSelector("data-evidence-anchor", `message:${focus.messageSeq}`));
  if (focus.spanAnchor) {
    selectors.push(evidenceAnchorSelector("data-evidence-anchor", `span:${focus.spanAnchor}`));
    selectors.push(evidenceAnchorSelector("data-evidence-anchor-alias", `span:${focus.spanAnchor}`));
  }
  if (focus.failureAnchor) selectors.push(evidenceAnchorSelector("data-evidence-anchor", `failure:${focus.failureAnchor}`));
  return selectors.join(",");
}

function evidenceAnchorSelector(attribute: string, value: string): string {
  return `[${attribute}="${cssEscape(value)}"]`;
}

function isFocusedTrial(focusedTrialAnchor: string | null, trial: PromptEvaluationRunEvidence["trials"][number]): boolean {
  if (!focusedTrialAnchor) return false;
  return focusedTrialAnchor === trial.id || focusedTrialAnchor === String(trial.case_index + 1);
}

function isFocusedSpan(focusedSpanAnchor: string | null, span: PromptEvaluationRunEvidence["execution_spans"][number]): boolean {
  if (!focusedSpanAnchor) return false;
  return focusedSpanAnchor === span.id || focusedSpanAnchor === String(span.seq);
}

function isFocusedTraceEvent(focusedTraceAnchor: string | null, event: PromptEvaluationRunEvidence["trace_events"][number], index: number): boolean {
  if (!focusedTraceAnchor) return false;
  return focusedTraceAnchor === event.id || focusedTraceAnchor === String(index + 1);
}

function isFocusedAssertion(focusedAssertionAnchor: string | null, trial: PromptEvaluationRunEvidence["trials"][number], assertionIndex: number): boolean {
  if (!focusedAssertionAnchor) return false;
  return focusedAssertionAnchor === `${trial.id}:${assertionIndex}` || focusedAssertionAnchor === `${trial.case_index + 1}.${assertionIndex}`;
}

function buildTrialAssertionRows(trial: PromptEvaluationRunEvidence["trials"][number]): Array<{ index: number; expectedText: string; matched: boolean }> {
  const expected = stringListFromUnknownMap(trial.expected, "期望包含", "expected_contains");
  const matched = new Set(stringListFromUnknownMap(trial.output, "已匹配", "matched_contains"));
  return expected.map((expectedText, index) => ({
    index: index + 1,
    expectedText,
    matched: matched.has(expectedText),
  }));
}

function stringListFromUnknownMap(value: unknown, ...keys: string[]): string[] {
  if (!value || typeof value !== "object") return [];
  const record = value as Record<string, unknown>;
  for (const key of keys) {
    const raw = record[key];
    if (Array.isArray(raw)) {
      return raw.map((item) => String(item)).filter(Boolean);
    }
  }
  return [];
}

type FailureReviewItem = {
  kind: "run" | "trial" | "assertion" | "tool" | "trace";
  label: string;
  title: string;
  detail: string;
  anchor: string;
};

function buildFailureReviewItems(evidence: PromptEvaluationRunEvidence): FailureReviewItem[] {
  const items: FailureReviewItem[] = [];
  if (evidence.run.failure_reason && evidence.run.failure_reason !== "无") {
    items.push({
      kind: "run",
      label: "运行",
      title: "运行失败原因",
      detail: evidence.run.failure_reason,
      anchor: "failure:run",
    });
  }
  for (const trial of evidence.trials) {
    if ((trial.status === "未通过" || trial.status === "失败" || trial.status === "需人工复核") && trial.failure_reason && trial.failure_reason !== "无") {
      items.push({
        kind: "trial",
        label: "用例",
        title: trial.case_name || `用例 ${trial.case_index + 1}`,
        detail: trial.failure_reason,
        anchor: "failure:trial",
      });
    }
    for (const assertion of buildTrialAssertionRows(trial)) {
      if (!assertion.matched) {
        items.push({
          kind: "assertion",
          label: "断言",
          title: trial.case_name || `用例 ${trial.case_index + 1}`,
          detail: `断言 #${assertion.index} 未命中：包含 ${assertion.expectedText}`,
          anchor: "failure:assertion",
        });
      }
    }
  }
  for (const chain of evidence.tool_call_chains) {
    if (chain.failure_signal || (chain.failure_reason && chain.failure_reason !== "无")) {
      items.push({
        kind: "tool",
        label: "工具",
        title: chain.tool || "未记录工具",
        detail: chain.failure_reason || chain.summary || "工具调用存在异常线索",
        anchor: "failure:tool",
      });
    }
  }
  for (const event of evidence.trace_events) {
    if ((event.failure_reason && event.failure_reason !== "无") || event.error_type) {
      items.push({
        kind: "trace",
        label: "trace",
        title: event.event_name || traceEventStageLabel(event.event_type),
        detail: [event.failure_reason, event.error_type].filter(Boolean).join(" · "),
        anchor: "failure:trace",
      });
    }
  }
  return items.slice(0, 8);
}

function buildFailureReviewMarkdown(evidence: PromptEvaluationRunEvidence, items: FailureReviewItem[], sourceUrl: string): string {
  const run = evidence.run;
  const context = evidence.上下文 ?? {};
  const completeness = isRecord(context["证据完整性"]) ? context["证据完整性"] : {};
  const firstTraceId = evidence.trace_events[0]?.id ?? "";
  const failedTrials = evidence.trials.filter((trial) => trial.status === "未通过" || trial.status === "失败" || trial.status === "需人工复核");
  const failedToolChains = evidence.tool_call_chains.filter((chain) => chain.failure_signal || (chain.failure_reason && chain.failure_reason !== "无"));
  const failedTraceEvents = evidence.trace_events.filter((event) => (event.failure_reason && event.failure_reason !== "无") || event.error_type);
  const lines = [
    "# Multica 失败复盘报告",
    "",
    `- 语义版本：multica.failure_review_report.v1`,
    `- 导出时间：${new Date().toISOString()}`,
    `- 页面链接：${sourceUrl || "未记录"}`,
    `- 运行 ID：${run.id}`,
    `- 运行状态：${run.status}`,
    `- 运行类型：${displayRunKind(run.run_kind)}`,
    `- 触发来源：${run.trigger_source || "未记录"}`,
    `- 任务 ID：${run.task_id || "未绑定"}`,
    `- 模型：${run.model || "未记录"}`,
    `- 运行时：${run.runtime_provider || "未记录"}`,
    `- 总耗时：${formatDuration(run.total_duration_ms)}`,
    `- token：输入 ${run.input_tokens}，输出 ${run.output_tokens}`,
    `- 预估成本：${formatMoney(run.estimated_cost)}`,
    `- 评估结论：${run.conclusion || "未记录"}`,
    "",
    "## 上下文",
    "",
    `- 工作区：${stringFromUnknown(context["工作区"]) || "未记录"}`,
    `- 提示词：${stringFromUnknown(context["提示词名称"]) || stringFromUnknown(context["提示词"]) || "未绑定"}`,
    `- 评测资产：${stringFromUnknown(context["评测资产名称"]) || stringFromUnknown(context["评测资产"]) || "未记录"}`,
    `- 智能体：${stringFromUnknown(context["执行Agent名称"]) || stringFromUnknown(context["执行Agent"]) || "未记录"}`,
    `- issue：${stringFromUnknown(context["issue标题"]) || stringFromUnknown(context["issue"]) || "未绑定"}`,
    `- 项目：${stringFromUnknown(context["项目名称"]) || stringFromUnknown(context["项目"]) || "未绑定"}`,
    `- 小队：${stringFromUnknown(context["小队名称"]) || stringFromUnknown(context["小队"]) || "未绑定"}`,
    `- 证据完整性：用例 ${stringFromUnknown(completeness["用例数"]) || evidence.trials.length}，任务消息 ${stringFromUnknown(completeness["任务消息条数"]) || evidence.task_messages.length}，trace 事件 ${stringFromUnknown(completeness["trace事件条数"]) || evidence.trace_events.length}`,
    "",
    "## 可复核锚点",
    "",
    `- 运行：run=${run.id}`,
    `- trace 序号：${evidence.trace_events.length > 0 ? `trace=1 到 trace=${evidence.trace_events.length}` : "暂无"}`,
    `- trace 事件 ID：${firstTraceId ? `trace=${firstTraceId}` : "暂无"}`,
    `- 工具链：${evidence.tool_call_chains.length > 0 ? "tool=<工具链id>" : "暂无"}`,
    `- 用例：${evidence.trials.length > 0 ? "trial=<用例id或序号>" : "暂无"}`,
    `- 断言：${evidence.trials.some((trial) => buildTrialAssertionRows(trial).length > 0) ? "assertion=<用例id>:<断言序号>" : "暂无"}`,
    "",
    "## 失败线索",
    "",
    ...items.map((item, index) => `${index + 1}. 【${item.label}】${item.title}：${item.detail}（锚点 ${item.anchor}）`),
    "",
    "## 失败用例",
    "",
    ...(failedTrials.length > 0
      ? failedTrials.map((trial) => `- ${trial.case_name || `用例 ${trial.case_index + 1}`}：${trial.status}，${trial.failure_reason || "未记录失败原因"}`)
      : ["- 暂无失败用例"]),
    "",
    "## 工具异常",
    "",
    ...(failedToolChains.length > 0
      ? failedToolChains.map((chain) => `- ${chain.tool || "未记录工具"}：${chain.failure_reason || chain.summary || "存在异常线索"}；工具链 ${chain.id}`)
      : ["- 暂无工具异常线索"]),
    "",
    "## trace 失败事件",
    "",
    ...(failedTraceEvents.length > 0
      ? failedTraceEvents.map((event) => `- ${event.event_name || traceEventStageLabel(event.event_type)}：${event.failure_reason || "未记录失败原因"}${event.error_type ? `；错误类型 ${event.error_type}` : ""}；trace=${event.id}`)
      : ["- 暂无 trace 失败事件"]),
    "",
    "## 建议动作",
    "",
    "- 若失败线索来自提示词输出或断言未命中，优先生成优化候选并进入人工确认。",
    "- 若失败线索来自工具或运行时，先检查工具输入输出、运行时配置、环境变量和外部依赖。",
    "- 复盘结论应回写到对应 issue、实验或优化运行，不只停留在本地下载文件。",
  ];
  return `${lines.join("\n")}\n`;
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

function ManualCasePanel({
  asset,
  cases,
  draft,
  onDraftChange,
  onCreateCase,
  creating,
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
  onUpdateAsset: (assetId: string, data: UpdatePromptEvaluationAssetRequest) => Promise<unknown>;
  onUpdateCase: (caseId: string, data: UpdatePromptEvaluationCaseRequest) => Promise<unknown>;
  updatingCaseId: string | null;
  onDeleteCase: (caseId: string) => void;
  deletingCaseId: string | null;
}) {
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
      let changedCount = 0;
      for (const item of filteredCases) {
        const currentTags = item.tags.map((value) => String(value)).filter(Boolean);
        const nextTags = mode === "追加"
          ? uniqueSortedStrings([...currentTags, ...targetTags])
          : currentTags.filter((tag) => !targetTags.includes(tag));
        if (sameStringList(currentTags, nextTags)) continue;
        await onUpdateCase(item.id, buildCaseTagUpdateRequest(asset, item, nextTags.join(", ")));
        changedCount += 1;
      }
      toast.success(`已${mode} ${changedCount} 条用例标签`);
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
      let changedCount = 0;
      const updatedCases = cases.map((item) => {
        const currentTags = item.tags.map((value) => String(value)).filter(Boolean);
        if (!currentTags.includes(sourceTag)) return item;
        const nextTags = uniqueSortedStrings(currentTags.map((tag) => (tag === sourceTag ? targetTag : tag)));
        changedCount += 1;
        return { ...item, tags: nextTags };
      });
      for (const item of updatedCases) {
        const original = cases.find((candidate) => candidate.id === item.id);
        if (!original || sameStringList(original.tags.map(String), item.tags.map(String))) continue;
        await onUpdateCase(item.id, buildCaseTagUpdateRequest(asset, item, item.tags.join(", ")));
      }
      await onUpdateAsset(asset.id, { payload: datasetPayloadWithSavedFilters(asset, savedFilters, updatedCases) });
      if (caseTagFilter === sourceTag) setCaseTagFilter(targetTag);
      setRenameSourceTag(targetTag);
      setRenameTargetTag("");
      toast.success(`已整理 ${changedCount} 条用例标签`);
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
          手工 {manualCases.length} · trace导入 {traceCases.length} · 总计 {cases.length}
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
            return (
              <div key={item.id} className="grid gap-2 rounded border bg-background px-2 py-1.5 text-xs">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium text-foreground">{item.case_name || `用例 ${item.case_index + 1}`}</span>
                  <span className="text-muted-foreground">{caseSourceLabel(item.source)} · {item.status}</span>
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

function sameStringList(a: string[], b: string[]) {
  if (a.length !== b.length) return false;
  return a.every((item, index) => item === b[index]);
}

function ExperimentDimensionPanel({ asset, dimensions }: { asset: PromptEvaluationAsset; dimensions: PromptEvaluationExperimentDimension[] }) {
  return (
    <div data-testid={`prompt-evaluation-experiment-dimensions-${asset.id}`} className="md:col-span-2 grid gap-2 rounded-md border border-border/70 bg-muted/10 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-xs font-medium text-muted-foreground">实验维度事实</div>
        <Badge variant="outline" className="text-[11px]">
          {dimensions.length} 个维度 · {asset.experiment_dimension_count} 条事实
        </Badge>
      </div>
      {dimensions.length > 0 ? (
        <div className="grid gap-1.5">
          {dimensions.map((item) => (
            <div key={item.id} className="grid gap-1 rounded border bg-background px-2 py-1.5 text-xs md:grid-cols-[180px_minmax(0,1fr)_minmax(0,1fr)]">
              <div className="min-w-0">
                <div className="truncate font-medium text-foreground">{item.dimension_name || `维度 ${item.dimension_index + 1}`}</div>
                <div className="truncate text-muted-foreground">{caseSourceLabel(item.source)} · {item.status}</div>
              </div>
              <div className="min-w-0 text-muted-foreground">
                <span className="text-foreground">对象：</span>
                <span className="truncate">{item.experiment_target || asset.name}</span>
              </div>
              <div className="min-w-0 text-muted-foreground">
                <span className="text-foreground">基线：</span>
                <span className="truncate">{item.baseline_output || "未记录"}</span>
              </div>
              <div className="min-w-0 text-muted-foreground md:col-span-3">
                对比配置：{summarizeJSONValue(item.comparison_payload)}
              </div>
            </div>
          ))}
        </div>
      ) : (
        <div className="rounded border border-dashed px-2 py-2 text-xs text-muted-foreground">暂无实验维度事实，请在资产载荷中补充对比维度。</div>
      )}
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

function tabToAssetType(tab: WorkbenchTab): PromptEvaluationAssetType | null {
  if (tab === "数据集" || tab === "测试套件" || tab === "实验" || tab === "优化运行") return tab;
  return null;
}

function canManageStructuredCases(asset: PromptEvaluationAsset): boolean {
  return asset.asset_type === "数据集" || asset.asset_type === "测试套件" || asset.asset_type === "实验" || asset.asset_type === "优化运行";
}

function caseSourceLabel(source: string): string {
  if (source === "manual") return "手工";
  if (source === "trace") return "trace导入";
  return "资产载荷";
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

function buildExperimentDimensionsByAsset(dimensions: PromptEvaluationExperimentDimension[]): Map<string, PromptEvaluationExperimentDimension[]> {
  const result = new Map<string, PromptEvaluationExperimentDimension[]>();
  for (const item of dimensions) {
    const bucket = result.get(item.experiment_asset_id) ?? [];
    bucket.push(item);
    result.set(item.experiment_asset_id, bucket);
  }
  for (const bucket of result.values()) {
    bucket.sort((a, b) => a.dimension_index - b.dimension_index || a.dimension_name.localeCompare(b.dimension_name, "zh-CN"));
  }
  return result;
}

function buildDimensionScoreSummariesByAsset(summaries: PromptEvaluationDimensionScoreSummary[]): Map<string, PromptEvaluationDimensionScoreSummary[]> {
  const result = new Map<string, PromptEvaluationDimensionScoreSummary[]>();
  for (const item of summaries) {
    const bucket = result.get(item.asset_id) ?? [];
    bucket.push(item);
    result.set(item.asset_id, bucket);
  }
  for (const bucket of result.values()) {
    bucket.sort((a, b) => {
      return a.dimension_index - b.dimension_index || a.dimension_name.localeCompare(b.dimension_name, "zh-CN");
    });
  }
  return result;
}

function buildDimensionScoreTrendsByAsset(trends: PromptEvaluationDimensionScoreTrend[]): Map<string, PromptEvaluationDimensionScoreTrend[]> {
  const result = new Map<string, PromptEvaluationDimensionScoreTrend[]>();
  for (const item of trends) {
    const bucket = result.get(item.asset_id) ?? [];
    bucket.push(item);
    result.set(item.asset_id, bucket);
  }
  for (const bucket of result.values()) {
    bucket.sort((a, b) => {
      const periodDelta = b.period.localeCompare(a.period);
      if (periodDelta !== 0) return periodDelta;
      const versionDelta = b.prompt_version - a.prompt_version;
      if (versionDelta !== 0) return versionDelta;
      return a.dimension_index - b.dimension_index || a.dimension_name.localeCompare(b.dimension_name, "zh-CN");
    });
  }
  return result;
}

function buildExperimentComparisonRows(
  experiments: PromptEvaluationAsset[],
  dimensionsByAsset: Map<string, PromptEvaluationExperimentDimension[]>,
  dimensionScoreSummariesByAsset: Map<string, PromptEvaluationDimensionScoreSummary[]>,
  dimensionScoreTrendsByAsset: Map<string, PromptEvaluationDimensionScoreTrend[]>,
  versionsByPromptId: Map<string, PromptLibraryVersion[]>,
  runs: PromptEvaluationRun[],
): ExperimentComparisonRow[] {
  const runsByAsset = buildRunsByAsset(runs);
  return experiments
    .map((asset) => {
      const assetRuns = [...(runsByAsset.get(asset.id) ?? [])].sort(comparePromptEvaluationRunByRecent);
      const assetDimensions = dimensionsByAsset.get(asset.id) ?? [];
      const assetDimensionScoreSummaries = dimensionScoreSummariesByAsset.get(asset.id) ?? [];
      const assetDimensionScoreTrends = dimensionScoreTrendsByAsset.get(asset.id) ?? [];
      const promptVersions = asset.prompt_id ? [...(versionsByPromptId.get(asset.prompt_id) ?? [])].sort((a, b) => b.version - a.version) : [];
      const versionRows = buildExperimentPromptVersionRows(promptVersions, assetRuns);
      const dimensionScoreRows = buildExperimentDimensionScoreRows(assetDimensions, assetDimensionScoreSummaries);
      const dimensionTrendRows = buildExperimentDimensionTrendRows(assetDimensionScoreTrends);
      const totalCases = assetRuns.reduce((sum, run) => sum + run.total_cases, 0);
      const passedCases = assetRuns.reduce((sum, run) => sum + run.passed_cases, 0);
      const failedCases = assetRuns.reduce((sum, run) => sum + run.failed_cases, 0);
      const totalDurationMs = assetRuns.reduce((sum, run) => sum + run.total_duration_ms, 0);
      const inputTokens = assetRuns.reduce((sum, run) => sum + run.input_tokens, 0);
      const outputTokens = assetRuns.reduce((sum, run) => sum + run.output_tokens, 0);
      const estimatedCost = assetRuns.reduce((sum, run) => sum + run.estimated_cost, 0);
      const passRate = totalCases > 0 ? passedCases / totalCases : 0;
      return {
        asset,
        dimensions: assetDimensions,
        dimensionScoreRows,
        dimensionTrendRows,
        promptVersions,
        versionRows,
        promptVersionRunCount: versionRows.reduce((sum, row) => sum + row.runCount, 0),
        qualityExplanation: buildExperimentQualityExplanation(assetRuns, passedCases, failedCases, passRate, totalDurationMs, estimatedCost),
        runs: assetRuns,
        totalCases,
        passedCases,
        failedCases,
        passRate,
        totalDurationMs,
        inputTokens,
        outputTokens,
        estimatedCost,
        latestRun: assetRuns[0] ?? null,
      };
    })
    .sort((a, b) => {
      const runDelta = Number(b.runs.length > 0) - Number(a.runs.length > 0);
      if (runDelta !== 0) return runDelta;
      const passDelta = b.passRate - a.passRate;
      if (passDelta !== 0) return passDelta;
      const failureDelta = a.failedCases - b.failedCases;
      if (failureDelta !== 0) return failureDelta;
      const costDelta = a.estimatedCost - b.estimatedCost;
      if (costDelta !== 0) return costDelta;
      return comparePromptEvaluationAssetByRecent(a.asset, b.asset);
    });
}

function buildExperimentQualityExplanation(
  runs: PromptEvaluationRun[],
  passedCases: number,
  failedCases: number,
  passRate: number,
  totalDurationMs: number,
  estimatedCost: number,
): ExperimentQualityExplanation {
  const costPerPassedCase = passedCases > 0 ? estimatedCost / passedCases : 0;
  const averageRunMs = runs.length > 0 ? totalDurationMs / runs.length : 0;
  const dominantFailure = dominantExperimentFailureReason(runs);
  const qualityLabel = runs.length === 0 ? "暂无运行" : passRate >= 0.8 ? "质量稳定" : passRate >= 0.5 ? "质量待优化" : "质量风险高";
  const costConclusion = runs.length === 0
    ? "暂无成本"
    : passedCases === 0
      ? "成本未转化"
      : costPerPassedCase <= 0.05
        ? "单位成本低"
        : costPerPassedCase <= 0.2
          ? "单位成本可控"
          : "单位成本偏高";
  const durationConclusion = runs.length === 0
    ? "暂无耗时"
    : averageRunMs <= 1000
      ? "响应很快"
      : averageRunMs <= 5000
        ? "耗时可控"
        : "耗时偏高";
  const failureConclusion = failedCases === 0 ? "暂无失败" : dominantFailure;
  let recommendation = "先补一次真实运行，形成质量和成本基线。";
  if (runs.length > 0 && failedCases === 0 && costPerPassedCase <= 0.2) {
    recommendation = "当前质量和成本可作为回归基线，后续重点观察版本变化。";
  } else if (failedCases > 0) {
    recommendation = `优先复盘失败主因「${dominantFailure}」，再生成优化候选并回归同一数据集版本。`;
  } else if (costPerPassedCase > 0.2) {
    recommendation = "质量可用但单位通过成本偏高，优先压缩提示词上下文或改用更轻模型复测。";
  }
  return {
    qualityLabel,
    costPerPassedCase,
    costConclusion,
    durationConclusion,
    failureConclusion,
    recommendation,
  };
}

function dominantExperimentFailureReason(runs: PromptEvaluationRun[]): string {
  const counts = new Map<string, number>();
  for (const run of runs) {
    if (run.failed_cases <= 0 && run.status !== "未通过" && run.status !== "失败") continue;
    const reason = (run.failure_reason && run.failure_reason !== "无" ? run.failure_reason : run.conclusion || "未记录失败原因").trim();
    counts.set(reason, (counts.get(reason) ?? 0) + 1);
  }
  return [...counts.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0], "zh-CN"))[0]?.[0] ?? "未记录失败原因";
}

function buildExperimentDimensionScoreRows(
  dimensions: PromptEvaluationExperimentDimension[],
  summaries: PromptEvaluationDimensionScoreSummary[],
): ExperimentDimensionScoreRow[] {
  const dimensionKeys = new Map<string, { index: number; name: string }>();
  dimensions.forEach((dimension) => {
    const name = dimension.dimension_name || `维度 ${dimension.dimension_index + 1}`;
    dimensionKeys.set(experimentDimensionScoreKey(dimension.dimension_index, name), { index: dimension.dimension_index, name });
  });
  const rawScores = summaries.map((summary) => dimensionScoreSummaryToRow(summary));
  rawScores.forEach((score) => {
    dimensionKeys.set(experimentDimensionScoreKey(score.dimensionIndex, score.dimensionName), {
      index: score.dimensionIndex,
      name: score.dimensionName,
    });
  });
  return Array.from(dimensionKeys.values())
    .map((dimension) => {
      const scores = rawScores.filter((score) => score.dimensionIndex === dimension.index || score.dimensionName === dimension.name);
      const scored = scores.filter((score) => score.status === "已评分" && score.totalCases > 0);
      const passedCases = scored.reduce((sum, score) => sum + score.passedCases, 0);
      const totalCases = scored.reduce((sum, score) => sum + score.totalCases, 0);
      const runCount = scores.reduce((sum, score) => sum + score.runCount, 0);
      const scoredRunCount = scores.reduce((sum, score) => sum + score.scoredRunCount, 0);
      const latest = scores[0] ?? null;
      return {
        dimensionIndex: dimension.index,
        dimensionName: dimension.name,
        score: totalCases > 0 ? passedCases / totalCases : 0,
        passedCases,
        totalCases,
        runCount,
        scoredRunCount,
        status: scoredRunCount > 0 ? "已评分" : latest?.status ?? "待评分",
        rule: latest?.rule ?? "等待运行生成评分规则",
        evidence: latest?.evidence ?? "暂无运行评分证据",
      };
    })
    .sort((a, b) => a.dimensionIndex - b.dimensionIndex || a.dimensionName.localeCompare(b.dimensionName, "zh-CN"));
}

function dimensionScoreSummaryToRow(summary: PromptEvaluationDimensionScoreSummary): ExperimentDimensionScoreRow {
  return {
    dimensionIndex: summary.dimension_index,
    dimensionName: summary.dimension_name || `维度 ${summary.dimension_index + 1}`,
    score: summary.score,
    passedCases: summary.passed_cases,
    totalCases: summary.total_cases,
    runCount: summary.run_count,
    scoredRunCount: summary.scored_run_count,
    status: summary.scored_run_count > 0 ? "已评分" : summary.latest_status,
    rule: summary.latest_rule || "未记录评分规则",
    evidence: summary.latest_evidence || "暂无运行评分证据",
  };
}

function buildExperimentDimensionTrendRows(trends: PromptEvaluationDimensionScoreTrend[]): ExperimentDimensionScoreTrendRow[] {
  return trends
    .map((trend) => ({
      dimensionIndex: trend.dimension_index,
      dimensionName: trend.dimension_name || `维度 ${trend.dimension_index + 1}`,
      period: trend.period,
      promptVersion: trend.prompt_version,
      score: trend.score,
      passedCases: trend.passed_cases,
      totalCases: trend.total_cases,
      runCount: trend.run_count,
      scoredRunCount: trend.scored_run_count,
      status: trend.scored_run_count > 0 ? "已评分" : trend.latest_status,
    }))
    .sort((a, b) => {
      const periodDelta = b.period.localeCompare(a.period);
      if (periodDelta !== 0) return periodDelta;
      const versionDelta = b.promptVersion - a.promptVersion;
      if (versionDelta !== 0) return versionDelta;
      return a.dimensionIndex - b.dimensionIndex || a.dimensionName.localeCompare(b.dimensionName, "zh-CN");
    });
}

function experimentDimensionScoreKey(index: number, name: string): string {
  return `${index}:${name}`;
}

function buildExperimentPromptVersionRows(
  versions: PromptLibraryVersion[],
  runs: PromptEvaluationRun[],
): ExperimentPromptVersionRow[] {
  return versions.map((version, index) => {
    const versionRuns = runs.filter((run) => promptVersionFromRun(run) === version.version);
    const totalCases = versionRuns.reduce((sum, run) => sum + run.total_cases, 0);
    const passedCases = versionRuns.reduce((sum, run) => sum + run.passed_cases, 0);
    const previousVersion = versions[index + 1] ?? null;
    return {
      version,
      previousVersion,
      runCount: versionRuns.length,
      passRate: totalCases > 0 ? passedCases / totalCases : 0,
      estimatedCost: versionRuns.reduce((sum, run) => sum + run.estimated_cost, 0),
      contentDelta: version.content.length - (previousVersion?.content.length ?? 0),
    };
  });
}

function promptVersionFromRun(run: PromptEvaluationRun): number | null {
  const fromMetrics = numberFromRecord(run.metrics, "提示词版本") ?? numberFromRecord(run.metrics, "prompt_version");
  if (fromMetrics !== null) return fromMetrics;
  return numberFromRecord(run.evidence, "提示词版本") ?? numberFromRecord(run.evidence, "prompt_version");
}

function buildRunsByAsset(runs: PromptEvaluationRun[]): Map<string, PromptEvaluationRun[]> {
  const result = new Map<string, PromptEvaluationRun[]>();
  for (const run of runs) {
    const bucket = result.get(run.asset_id) ?? [];
    bucket.push(run);
    result.set(run.asset_id, bucket);
  }
  return result;
}

function comparePromptEvaluationRunByRecent(a: PromptEvaluationRun, b: PromptEvaluationRun): number {
  return timestampForSort(b.completed_at || b.updated_at || b.created_at) - timestampForSort(a.completed_at || a.updated_at || a.created_at);
}

function comparePromptEvaluationAssetByRecent(a: PromptEvaluationAsset, b: PromptEvaluationAsset): number {
  return timestampForSort(b.updated_at || b.created_at) - timestampForSort(a.updated_at || a.created_at);
}

function timestampForSort(value: string): number {
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp) ? timestamp : 0;
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

function MetricCell({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded border bg-muted/20 px-2 py-1">
      <div className="truncate text-[11px] text-muted-foreground">{label}</div>
      <div className="mt-0.5 truncate font-semibold text-foreground">{value}</div>
    </div>
  );
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
  if (caseSummary && caseSummary.total > 0) {
    const sourceParts = [];
    if (caseSummary.manual > 0) sourceParts.push(`手工 ${caseSummary.manual}`);
    if (caseSummary.trace > 0) sourceParts.push(`trace导入 ${caseSummary.trace}`);
    if (caseSummary.payload > 0) sourceParts.push(`资产载荷 ${caseSummary.payload}`);
    return `结构化用例 ${caseSummary.total} 个${sourceParts.length > 0 ? `（${sourceParts.join("，")}；运行优先使用）` : ""}`;
  }
  if (payload["最近Agent运行"]) return "包含真实智能体运行";
  if (payload["调试包"]) return "包含 智能体调试包";
  if (payload["运行结果"]) return "包含运行结果";
  if (asset.asset_type === "实验") return `实验维度事实 ${asset.experiment_dimension_count || (Array.isArray(payload["对比维度"]) ? payload["对比维度"].length : 0)} 个`;
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
  const versionRowsQuery = useQuery({
    queryKey: promptLibraryKeys.datasetVersionRows(workspaceId, asset.id, selectedVersionId),
    queryFn: () => api.listPromptEvaluationDatasetVersionRows(asset.id, selectedVersionId!),
    enabled: Boolean(loaded && workspaceId && asset.id && selectedVersionId),
  });
  const invalidateDataset = () => {
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.datasetVersions(workspaceId, asset.id) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.datasetVersionRows(workspaceId, asset.id, selectedVersionId) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.assets(workspaceId) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.cases(workspaceId) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.summary(workspaceId) });
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

function summarizeLatestRunForDemo(run: PromptEvaluationRun): string {
  const parts = [`${displayRunKind(run.run_kind)} · ${run.status}`];
  if (run.failure_reason && run.failure_reason !== "无") {
    parts.push(`失败原因：${truncateText(run.failure_reason, 42)}`);
  }
  if (run.task_id) {
    parts.push(`任务标识 ${truncateText(run.task_id, 8)}`);
  }
  return parts.join(" · ");
}

function isAgentEvaluationRun(run: PromptEvaluationRun): boolean {
  const kind = String(run.run_kind);
  return kind === "Agent执行" || kind === "智能体执行" || Boolean(run.task_id);
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

function numberFromRecord(record: Record<string, unknown>, key: string): number | null {
  const value = record[key];
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string") {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : null;
  }
  return null;
}

function truncateText(value: string, maxLength: number): string {
  return value.length > maxLength ? `${value.slice(0, maxLength)}...` : value;
}

function formatNumber(value: unknown): string {
  return typeof value === "number" && Number.isFinite(value) ? value.toLocaleString("zh-CN") : "0";
}

function formatSignedNumber(value: number): string {
  if (!Number.isFinite(value) || value === 0) return "0";
  return `${value > 0 ? "+" : ""}${value.toLocaleString("zh-CN")}`;
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
