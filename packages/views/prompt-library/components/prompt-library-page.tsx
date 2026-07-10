"use client";

import { useEffect, useMemo, useRef, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, BookOpenText, Download, Loader2, Plus, Save, Search, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueExecutionTreeOptions } from "@multica/core/issues/queries";
import { useWorkspacePaths } from "@multica/core/paths";
import { projectListOptions, projectResourcesOptions } from "@multica/core/projects";
import {
  TRAINING_WORKBENCH_VIEW_BY_TAB,
  buildSkillScenarioAssetRequest,
  buildWritingModelBenchmarkAssetRequest,
  isSkillScenarioPayload,
  summarizeSkillScenarioTarget,
  summarizeWritingModelBenchmark,
  trainingWorkbenchSectionLabelFromView,
  trainingWorkbenchShowsPromptEditor,
  trainingWorkbenchTabFromView,
  trainingWorkbenchTitleFromView,
  trainingWorkbenchViewFromCanonicalRoute,
  type TrainingWorkbenchTab,
  type TrainingWorkbenchViewId,
} from "@multica/core/training";
import type {
  CreatePromptLibraryItemRequest,
  CreatePromptLibraryTrialRequest,
  CreatePromptLibraryVersionRequest,
  CreatePromptEvaluationAssetRequest,
  CreatePromptEvaluationCaseRequest,
  UpdatePromptEvaluationCaseRequest,
  PromptEvaluationAsset,
  PromptEvaluationOptimizationCandidate,
  PromptEvaluationStructuredCase,
  PromptEvaluationRun,
  PromptEvaluationAssetEvidenceArchivePackage,
  PromptEvaluationAssetType,
  IssueExecutionTreeResponse,
  PromptLibraryItem,
  PromptLibraryVersion,
  UpdatePromptEvaluationAssetRequest,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Badge } from "@multica/ui/components/ui/badge";
import { PageHeader } from "../../layout/page-header";
import { AppLink } from "../../navigation";
import { useNavigation } from "../../navigation";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { useT } from "../../i18n/use-t";
import {
  buildAssetPayload,
  draftToRequest,
  emptyDraft,
  itemToDraft,
  parseDebugValues,
  setDraftField,
  splitList,
  type PromptDraft,
} from "./prompt-library-request-builders";
import { trainingSelectedPromptStorageKey } from "./prompt-selection-storage";
import { PromptTrialPanel, PromptVersionHistory } from "./prompt-editor-panels";
import { Field } from "./form-field";
import { extractPromptVariables } from "./prompt-trial-model";
import { caseSourceLabel, type DatasetCaseSourceFilter } from "./case-source";
import { asRecord, shortId, stringFromUnknown } from "./record-utils";
import {
  buildSkillResourceOptions,
  type SkillResourceOption,
} from "./skill-candidate-model";
import { promptLibraryKeys } from "./prompt-library-query-keys";
import {
  type EvidenceFocus,
  type RunStatusFilter,
} from "./run-model";
import { RunHistoryPanel } from "./run-history-panel";

type WorkbenchTab = TrainingWorkbenchTab;

function isEvaluationRunRecordsTab(tab: WorkbenchTab): boolean {
  return tab === "评测记录";
}

const DEFAULT_CASE_LIBRARY_DRAFT_KEY = "__default_case_library__";
function trainingViewFromLocation(pathname: string, searchParams: URLSearchParams) {
  const match = pathname.match(/\/(debug|evaluation)\/([^/?#]+)/);
  if (!match?.[1] || !match[2]) return searchParams.get("view");
  const section = match[1] === "debug" || match[1] === "evaluation" ? match[1] : null;
  if (!section) return searchParams.get("view");
  const route = decodeURIComponent(match[2]);
  return trainingWorkbenchViewFromCanonicalRoute(section, route);
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

export function resolvePromptSelection(
  items: Array<Pick<PromptLibraryItem, "id">>,
  currentId: string | null,
  promptIdParam: string | null,
  storedId: string | null,
): string | null {
  const itemIds = new Set(items.map((item) => item.id));
  if (promptIdParam && itemIds.has(promptIdParam)) return promptIdParam;
  if (currentId && itemIds.has(currentId)) return currentId;
  if (storedId && itemIds.has(storedId)) return storedId;
  return items[0]?.id ?? null;
}

export function promptDraftSyncKey(
  selected: Pick<PromptLibraryItem, "id" | "version"> | null,
  activeVersion: Pick<PromptLibraryVersion, "id"> | null,
): string | null {
  if (activeVersion) return `version:${activeVersion.id}`;
  if (selected) return `prompt:${selected.id}:v${selected.version}`;
  return null;
}

export function PromptLibraryPage({
  activeView,
  showPromptEditor,
}: {
  activeView?: TrainingWorkbenchViewId;
  showPromptEditor?: boolean;
}) {
  const { t } = useT("prompt-library");
  const workspaceId = useWorkspaceId();
  const workspacePaths = useWorkspacePaths();
  const navigation = useNavigation();
  const queryClient = useQueryClient();
  const [query, setQuery] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [isDraftingNew, setIsDraftingNew] = useState(false);
  const [draft, setDraft] = useState<PromptDraft>(emptyDraft);
  const [activeVersionId, setActiveVersionId] = useState<string | null>(null);
  const [changeNote, setChangeNote] = useState("");
  const [trialAgentId, setTrialAgentId] = useState("");
  const [trialVariables, setTrialVariables] = useState<Record<string, string>>({});
  const [caseDrafts, setCaseDrafts] = useState<Record<string, ManualCaseDraft>>({});
  const lastSyncedDraftKeyRef = useRef<string | null>(null);
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
  const activeSectionLabel = trainingWorkbenchSectionLabelFromView(resolvedView);
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
    activeTab === "用例库" ||
    activeTab === "测试套件";
  const needsPromptItems =
    (shouldShowPromptEditor && activeTab === "提示词库") ||
    activeTab === "测试套件";
  const needsPromptVersions = shouldShowPromptEditor && activeTab === "提示词库";
  const needsEvaluationAssets = isEvaluationAssetTab;
  const needsStructuredCases =
    activeTab === "用例库" ||
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
    if (!focusedCaseId || activeTab !== "用例库") return;
    const timer = window.setTimeout(() => {
      document.querySelector(`[data-testid="case-library-case-${cssEscape(focusedCaseId)}"]`)?.scrollIntoView({
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
  const needsFocusedIssueTree = Boolean(focusedIssueId && (needsEvaluationAssets || isEvaluationRunRecords));
  const focusedIssueTreeQuery = useQuery({
    ...issueExecutionTreeOptions(focusedIssueId ?? ""),
    enabled: !!workspaceId && needsFocusedIssueTree,
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
  const assets = useMemo(() => assetQuery.data?.items ?? [], [assetQuery.data?.items]);
  const cases = caseQuery.data?.items ?? [];
  const runs = runQuery.data?.items ?? [];
  const candidates = candidateQuery.data?.items ?? [];
  const skillResourceOptions = useMemo(
    () => buildSkillResourceOptions(projectQuery.data ?? [], projectResourceQueries.map((query) => query.data ?? [])),
    [projectQuery.data, projectResourceQueries],
  );
  const visiblePromptItems = items;
  const selectedFromList = selectedId ? items.find((item) => item.id === selectedId) ?? null : null;
  const selected = selectedFromList ?? (isDraftingNew || selectedId ? null : visiblePromptItems[0] ?? null);
  const headerCount = activeTab === "用例库"
    ? assets.filter((asset) => asset.asset_type === "数据集").length
    : isEvaluationAssetTab
      ? assets.filter((asset) => asset.asset_type === tabToAssetType(activeTab)).length
      : visiblePromptItems.length;
  const versionQuery = useQuery({
    queryKey: promptLibraryKeys.versions(workspaceId ?? "", selectedFromList?.id ?? null),
    queryFn: () => api.listPromptLibraryVersions(selectedFromList?.id ?? ""),
    enabled: !!workspaceId && needsPromptVersions && !!selectedFromList,
  });
  const promptVersions = useMemo(
    () => versionQuery.data?.items ?? [],
    [versionQuery.data?.items],
  );
  const trialQuery = useQuery({
    queryKey: promptLibraryKeys.trials(workspaceId ?? "", selectedFromList?.id ?? null),
    queryFn: () => api.listPromptLibraryTrials(selectedFromList?.id ?? ""),
    enabled: !!workspaceId && needsPromptVersions && !!selectedFromList,
  });
  const agentQuery = useQuery({
    queryKey: promptLibraryKeys.agents(workspaceId ?? ""),
    queryFn: () => api.listAgents({ workspace_id: workspaceId ?? undefined }),
    enabled: !!workspaceId && needsPromptVersions,
  });
  const promptTrials = trialQuery.data?.items ?? [];
  const agents = useMemo(
    () => (agentQuery.data ?? []).filter((agent) => !agent.archived_at),
    [agentQuery.data],
  );
  const activeVersion = useMemo(() => {
    if (!selectedFromList) return null;
    return promptVersions.find((version) => version.id === activeVersionId) ?? promptVersions[0] ?? null;
  }, [activeVersionId, promptVersions, selectedFromList]);
  const selectedPromptStorageKey = trainingSelectedPromptStorageKey(workspaceId);

  useEffect(() => {
    if (!selectedPromptStorageKey || !selectedId) return;
    try {
      window.localStorage.setItem(selectedPromptStorageKey, selectedId);
    } catch {
      // Ignore storage failures in private or restricted browser contexts.
    }
  }, [selectedId, selectedPromptStorageKey]);

  useEffect(() => {
    if (isDraftingNew || listQuery.isLoading) return;

    let storedId: string | null = null;
    try {
      storedId = selectedPromptStorageKey ? window.localStorage.getItem(selectedPromptStorageKey) : null;
    } catch {
      storedId = null;
    }
    const nextId = resolvePromptSelection(visiblePromptItems, selectedId, promptIdParam, storedId);

    if (selectedId !== nextId) {
      setSelectedId(nextId);
    }
  }, [isDraftingNew, listQuery.isLoading, promptIdParam, selectedId, selectedPromptStorageKey, visiblePromptItems]);

  const filteredItems = useMemo(() => {
    const q = query.trim().toLowerCase();
    return visiblePromptItems.filter((item) => {
      if (!q) return true;
      const haystack = [item.name, item.description, item.content].join(" ");
      return haystack.toLowerCase().includes(q) || matchesPinyin(haystack, q);
    });
  }, [query, visiblePromptItems]);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.list(workspaceId ?? "") });
  const invalidateVersions = (promptId: string | null) => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.versions(workspaceId ?? "", promptId) });
  const invalidateTrials = (promptId: string | null) => queryClient.invalidateQueries({ queryKey: promptLibraryKeys.trials(workspaceId ?? "", promptId) });
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

  const createMut = useMutation({
    mutationFn: (data: CreatePromptLibraryItemRequest) => api.createPromptLibraryItem(data),
    onSuccess: (item) => {
      invalidate();
      invalidateVersions(item.id);
      setActiveVersionId(null);
      setIsDraftingNew(false);
      rememberSelectedPrompt(item.id);
      toast.success(t(($) => $.page.toast.prompt_created));
    },
  });

  const createVersionMut = useMutation({
    mutationFn: ({ id, data }: { id: string; data: CreatePromptLibraryVersionRequest }) => api.createPromptLibraryVersion(id, data),
    onSuccess: (result) => {
      const item = result.item;
      invalidate();
      invalidateVersions(item.id);
      setActiveVersionId(result.version.id);
      setDraft({
        name: result.version.name,
        description: result.version.description,
        content: result.version.content,
      });
      setChangeNote("");
      setIsDraftingNew(false);
      rememberSelectedPrompt(item.id);
      toast.success(t(($) => $.page.toast.version_created, { version: result.version.version }));
    },
  });

  const createTrialMut = useMutation({
    mutationFn: ({ id, versionId, data }: { id: string; versionId: string; data: CreatePromptLibraryTrialRequest }) =>
      api.createPromptLibraryTrial(id, versionId, data),
    onSuccess: (_trial, variables) => {
      invalidateTrials(variables.id);
      toast.success(t(($) => $.page.toast.trial_submitted));
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.deletePromptLibraryItem(id),
    onSuccess: () => {
      invalidate();
      rememberSelectedPrompt(null);
      setDraft(emptyDraft());
      toast.success(t(($) => $.page.toast.prompt_deleted));
    },
  });

  const updateAssetMut = useMutation({
    mutationFn: ({ id, data }: { id: string; data: UpdatePromptEvaluationAssetRequest }) => api.updatePromptEvaluationAsset(id, data),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      toast.success(t(($) => $.page.toast.updated));
    },
  });

  const deleteAssetMut = useMutation({
    mutationFn: (id: string) => api.deletePromptEvaluationAsset(id),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      toast.success(t(($) => $.page.toast.deleted));
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
      toast.success(t(($) => $.page.toast.trace_imported, { count: result.created_count }));
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : t(($) => $.page.toast.trace_import_failed));
    },
  });

  const createDatasetVersionMut = useMutation({
    mutationFn: ({ assetId, versionLabel }: { assetId: string; versionLabel: string }) => api.createPromptEvaluationDatasetVersion(assetId, {
      version_label: versionLabel,
      metadata: {
        来源: "训练与评估页面",
        用途: "锁定当前用例库版本，供后续评估运行和实验对比复盘",
        创建时间: new Date().toISOString(),
      },
    }),
    onSuccess: (version, variables) => {
      invalidateAssets();
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.datasetVersions(workspaceId ?? "", variables.assetId) });
      toast.success(t(($) => $.page.toast.dataset_version_locked, { version: version.version }));
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : t(($) => $.page.toast.dataset_version_failed));
    },
  });

  const createCaseMut = useMutation({
    mutationFn: (data: CreatePromptEvaluationCaseRequest) => api.createPromptEvaluationCase(data),
    onSuccess: () => {
      invalidateCases();
      toast.success(t(($) => $.page.toast.manual_case_created));
    },
  });

  const updateCaseMut = useMutation({
    mutationFn: ({ caseId, data }: { caseId: string; data: UpdatePromptEvaluationCaseRequest }) => api.updatePromptEvaluationCase(caseId, data),
    onSuccess: () => {
      invalidateCases();
      toast.success(t(($) => $.page.toast.manual_case_saved));
    },
  });

  const deleteCaseMut = useMutation({
    mutationFn: (id: string) => api.deletePromptEvaluationCase(id),
    onSuccess: () => {
      invalidateCases();
      toast.success(t(($) => $.page.toast.manual_case_deleted));
    },
  });

  const createCaseLibraryCaseMut = useMutation({
    mutationFn: async ({ asset, draft }: { asset: PromptEvaluationAsset; draft: ManualCaseDraft }) => {
      const existingCount = cases.filter((item) => item.asset_id === asset.id).length;
      return api.createPromptEvaluationCase(buildCaseLibraryCreateRequest(asset, draft, existingCount));
    },
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      toast.success(t(($) => $.page.toast.case_created));
    },
  });

  const syncRunMut = useMutation({
    mutationFn: (runId: string) => api.syncPromptEvaluationRun(runId),
    onSuccess: (_run, runId) => {
      invalidateRuns();
      invalidateCandidates();
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidence(workspaceId ?? "", runId) });
      invalidateRunEvidenceSnapshots(runId);
      toast.success(t(($) => $.page.toast.run_synced));
    },
  });

  const cancelRunMut = useMutation({
    mutationFn: (runId: string) => api.cancelPromptEvaluationRun(runId),
    onSuccess: (run) => {
      invalidateRuns();
      invalidateCandidates();
      queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidence(workspaceId ?? "", run.id) });
      invalidateRunEvidenceSnapshots(run.id);
      toast.success(t(($) => $.page.toast.run_cancelled));
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
      toast.success(t(($) => $.page.toast.reviewed, { status: run.review_decision || run.status }));
    },
  });

  const createEvidenceSnapshotMut = useMutation({
    mutationFn: (runId: string) => api.createPromptEvaluationEvidenceSnapshot(runId, "验收归档"),
    onSuccess: (snapshot) => {
      invalidateRunEvidenceSnapshots(snapshot.run_id);
      toast.success(t(($) => $.page.toast.snapshot_archived));
    },
  });

  const createAssetEvidenceSnapshotsMut = useMutation({
    mutationFn: (assetId: string) => api.createPromptEvaluationAssetEvidenceSnapshots(assetId, "验收归档", 20),
    onSuccess: (result) => {
      invalidateRuns();
      for (const snapshot of result.items) {
        invalidateRunEvidenceSnapshots(snapshot.run_id);
      }
      const skippedText = result.skipped_count > 0
        ? t(($) => $.page.toast.archive_skipped, { count: result.skipped_count })
        : "";
      toast.success(t(($) => $.page.toast.evidence_archived, { count: result.created_count, skipped: skippedText }));
    },
  });

  const handleDownloadAssetEvidencePackage = async (assetId: string) => {
    setExportingAssetEvidencePackageAssetId(assetId);
    try {
      const archivePackage: PromptEvaluationAssetEvidenceArchivePackage = await api.getPromptEvaluationAssetEvidenceArchivePackage(assetId, "验收归档", 20);
      const filename = `multica-training-asset-evidence-${assetId}-${new Date().toISOString().replace(/[:.]/g, "-")}.json`;
      downloadTextFile(JSON.stringify(archivePackage, null, 2), filename, "application/json;charset=utf-8");
      if (archivePackage.archived_run_count > 0) {
        toast.success(t(($) => $.page.toast.archive_exported, { count: archivePackage.archived_run_count }));
      } else {
        toast.info(t(($) => $.page.toast.archive_exported_empty));
      }
    } finally {
      setExportingAssetEvidencePackageAssetId(null);
    }
  };

  const createCandidateMut = useMutation({
    mutationFn: (runId: string) => api.createPromptEvaluationOptimizationCandidate(runId),
    onSuccess: () => {
      invalidateCandidates();
      toast.success(t(($) => $.page.toast.candidate_created));
    },
  });

  const saving = createMut.isPending || createVersionMut.isPending;
  const deleting = deleteMut.isPending;

  const createAssetMut = useMutation({
    mutationFn: (data: CreatePromptEvaluationAssetRequest) => api.createPromptEvaluationAsset(data),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      toast.success(t(($) => $.page.toast.asset_created));
    },
  });

  const createWorkbenchAsset = (assetType: PromptEvaluationAssetType) => {
    const prompt = selected ?? visiblePromptItems[0] ?? null;
    if (!prompt) {
      toast.error(t(($) => $.page.toast.save_prompt_first));
      return;
    }
    const assetLabel = assetTypeLabel(assetType);
    createAssetMut.mutate({
      prompt_id: prompt.id,
      name: `${prompt.name} ${assetLabel} ${new Date().toLocaleString("zh-CN")}`,
      description: `从训练工作台创建的${assetLabel}`,
      asset_type: assetType,
      payload: buildAssetPayload(assetType, prompt, {}),
      status: "启用",
    });
  };

  const createSkillScenarioAsset = (assetType: Extract<PromptEvaluationAssetType, "数据集" | "测试套件">) => {
    createAssetMut.mutate(buildSkillScenarioAssetRequest(assetType));
  };

  const createWritingBenchmarkAsset = () => {
    createAssetMut.mutate(buildWritingModelBenchmarkAssetRequest());
  };

  const createCaseLibraryDataset = (name: string, description: string) => {
    createAssetMut.mutate({
      name,
      description,
      asset_type: "数据集",
      status: "启用",
      payload: {
        schema_version: 1,
        schema: "multica.training_evaluation.payload.v1",
        语义版本: "multica.training_evaluation.v1",
        cases: [],
        payload_contract: {
          source: "case-library-editor",
        },
      },
    });
  };

  const creatingAsset = createAssetMut.isPending;
  const savingAsset = creatingAsset || updateAssetMut.isPending || deleteAssetMut.isPending || importDatasetFromTracesMut.isPending || createDatasetVersionMut.isPending;

  useEffect(() => {
    if (isDraftingNew) {
      lastSyncedDraftKeyRef.current = null;
      return;
    }
    if (activeVersion) {
      const nextKey = promptDraftSyncKey(selected, activeVersion);
      if (lastSyncedDraftKeyRef.current === nextKey) return;
      lastSyncedDraftKeyRef.current = nextKey;
      setDraft({
        name: activeVersion.name,
        description: activeVersion.description,
        content: activeVersion.content,
      });
      return;
    }
    if (selected) {
      const nextKey = promptDraftSyncKey(selected, null);
      if (lastSyncedDraftKeyRef.current === nextKey) return;
      lastSyncedDraftKeyRef.current = nextKey;
      setDraft(itemToDraft(selected));
    }
  }, [activeVersion, isDraftingNew, selected]);

  useEffect(() => {
    setActiveVersionId(null);
    setChangeNote("");
  }, [selected?.id]);

  useEffect(() => {
    if (!trialAgentId && agents[0]?.id) setTrialAgentId(agents[0].id);
    if (trialAgentId && agents.length > 0 && !agents.some((agent) => agent.id === trialAgentId)) {
      setTrialAgentId(agents[0]?.id ?? "");
    }
  }, [agents, trialAgentId]);

  const detectedVariables = useMemo(() => extractPromptVariables(draft.content), [draft.content]);

  useEffect(() => {
    setTrialVariables((current) => {
      const next: Record<string, string> = {};
      for (const name of detectedVariables) next[name] = current[name] ?? "";
      return next;
    });
  }, [detectedVariables]);

  const startNew = () => {
    setIsDraftingNew(true);
    rememberSelectedPrompt(null);
    setDraft(emptyDraft());
    setActiveVersionId(null);
    setChangeNote("");
    setTrialVariables({});
  };

  const saveDraft = () => {
    const payload = draftToRequest(draft);
    if (!payload.name.trim()) {
      toast.error(t(($) => $.page.toast.name_required));
      return;
    }
    if (!payload.content.trim()) {
      toast.error(t(($) => $.page.toast.content_required));
      return;
    }
    if (selected) {
      createVersionMut.mutate({
        id: selected.id,
        data: {
          name: payload.name,
          description: payload.description,
          content: payload.content,
          change_note: changeNote.trim(),
        },
      });
    } else {
      createMut.mutate(payload);
    }
  };

  const runPromptTrial = () => {
    if (!selected || !activeVersion) {
      toast.error(t(($) => $.page.toast.save_version_first));
      return;
    }
    if (!trialAgentId) {
      toast.error(t(($) => $.page.toast.agent_required));
      return;
    }
    const missingVariables = detectedVariables.filter((name) => !trialVariables[name]?.trim());
    if (missingVariables.length > 0) {
      toast.error(t(($) => $.page.toast.variables_required, { names: missingVariables.join("、") }));
      return;
    }
    createTrialMut.mutate({
      id: selected.id,
      versionId: activeVersion.id,
      data: {
        agent_id: trialAgentId,
        variables: trialVariables,
      },
    });
  };

  const deleteSelected = () => {
    if (!selected) return;
    if (!window.confirm(t(($) => $.page.confirm.delete_prompt, { name: selected.name }))) return;
    deleteMut.mutate(selected.id);
  };

  const toggleAssetStatus = (asset: PromptEvaluationAsset) => {
    updateAssetMut.mutate({
      id: asset.id,
      data: { status: asset.status === "启用" ? "归档" : "启用" },
    });
  };

  const deleteAsset = (asset: PromptEvaluationAsset) => {
    if (!window.confirm(t(($) => $.page.confirm.delete_asset, { name: asset.name }))) return;
    deleteAssetMut.mutate(asset.id);
  };

  const importDatasetFromTraces = (asset: PromptEvaluationAsset) => {
    importDatasetFromTracesMut.mutate(asset.id);
  };

  const updateCaseLibraryDataset = (asset: PromptEvaluationAsset, data: UpdatePromptEvaluationAssetRequest) => {
    updateAssetMut.mutate({ id: asset.id, data });
  };

  const deleteCaseLibraryDataset = (asset: PromptEvaluationAsset) => {
    if (!window.confirm(t(($) => $.page.confirm.delete_dataset, { name: asset.name }))) return;
    deleteAssetMut.mutate(asset.id);
  };

  const reviewRun = (run: PromptEvaluationRun, decision: "通过" | "未通过") => {
    const defaultNote = decision === "通过" ? "人工复核确认通过" : "人工复核驳回";
    const note = window.prompt(
      decision === "通过" ? t(($) => $.page.review.pass_prompt) : t(($) => $.page.review.fail_prompt),
      defaultNote,
    );
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
      onCreateCaseLibraryDataset={createCaseLibraryDataset}
      onUpdateCaseLibraryDataset={updateCaseLibraryDataset}
      updatingCaseLibraryDatasetId={updateAssetMut.isPending ? updateAssetMut.variables?.id ?? null : null}
      onDeleteCaseLibraryDataset={deleteCaseLibraryDataset}
      deletingCaseLibraryDatasetId={deleteAssetMut.isPending ? deleteAssetMut.variables ?? null : null}
      onToggleAssetStatus={toggleAssetStatus}
      onDeleteAsset={deleteAsset}
      onImportDatasetFromTraces={importDatasetFromTraces}
      importingTraceDatasetAssetId={importDatasetFromTracesMut.isPending ? importDatasetFromTracesMut.variables ?? null : null}
      onCreateDatasetVersion={(asset, versionLabel = "手动快照") => createDatasetVersionMut.mutate({
        assetId: asset.id,
        versionLabel: versionLabel.trim() || "手动快照",
      })}
      creatingDatasetVersionAssetId={createDatasetVersionMut.isPending ? createDatasetVersionMut.variables?.assetId ?? null : null}
      onCreateCase={(data) => createCaseMut.mutate(data)}
      onCreateCaseLibraryCase={(asset, draft) => createCaseLibraryCaseMut.mutateAsync({ asset, draft })}
      creatingCaseAssetId={createCaseMut.isPending ? createCaseMut.variables?.asset_id ?? null : null}
      creatingCaseLibraryCase={createCaseLibraryCaseMut.isPending}
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
    />
  );

  return (
    <div className="flex h-full min-h-0 flex-col bg-background" data-testid="training-page-shell" data-training-view={activeViewId}>
      <div className="sr-only" data-testid={`training-route-${activeViewId}`}>
        {t(($) => $.page.route_context, { section: activeSectionLabel, tab: activeTab })}
      </div>
      <PageHeader>
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <BookOpenText className="size-4 shrink-0 text-muted-foreground" />
          <h1 className="truncate text-sm font-semibold">{activeSectionLabel} / {activeTab}</h1>
          <span className="text-xs text-muted-foreground">{headerCount}</span>
        </div>
        {shouldShowPromptHeaderActions && (
          <div className="flex items-center gap-2">
            <Button size="sm" onClick={startNew}>
              <Plus className="size-3.5" />
              {t(($) => $.page.new)}
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
                  placeholder={t(($) => $.page.search_placeholder)}
                  className="h-8 pl-8 text-sm"
                />
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
                <div className="p-6 text-sm text-muted-foreground">{t(($) => $.page.empty)}</div>
              ) : (
                <div className="divide-y">
                  {filteredItems.map((item) => (
                    <button
                      key={item.id}
                      type="button"
	                      onClick={() => {
	                        setIsDraftingNew(false);
	                        setActiveVersionId(null);
	                        rememberSelectedPrompt(item.id);
	                      }}
                      className={`flex w-full flex-col gap-2 px-3 py-3 text-left transition-colors hover:bg-muted/60 ${
                        selectedId === item.id ? "bg-muted" : ""
                      }`}
                    >
	                      <div className="flex min-w-0 items-center gap-2">
	                        <span className="min-w-0 flex-1 truncate text-sm font-medium">{item.name}</span>
	                        <Badge variant="outline" className="shrink-0 text-[10px]">
                            {t(($) => $.page.version_badge, { version: item.version })}
                          </Badge>
	                      </div>
                      <div className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
                        <span className="truncate">{item.description || t(($) => $.page.no_description)}</span>
                      </div>
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
	                  <h2 className="truncate text-base font-semibold">{selected ? selected.name : t(($) => $.page.new_prompt)}</h2>
	                  <div className="mt-1 text-xs text-muted-foreground">
	                    {selected
                        ? t(($) => $.page.version_context, {
                            current: activeVersion ? `v${activeVersion.version}` : `v${selected.version}`,
                            latest: selected.version,
                          })
                        : t(($) => $.page.unsaved)}
	                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  {selected && (
                    <Button size="sm" variant="destructive" onClick={deleteSelected} disabled={deleting}>
                      <Trash2 className="size-3.5" />
                      {t(($) => $.page.delete)}
                    </Button>
                  )}
	                  <Button size="sm" onClick={saveDraft} disabled={saving}>
	                    {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
	                    {selected ? t(($) => $.page.save_version) : t(($) => $.page.create_prompt)}
	                  </Button>
                </div>
              </div>

              <PromptVersionHistory
                selected={selected}
                versions={promptVersions}
                activeVersionId={activeVersion?.id ?? null}
                onSelectVersion={setActiveVersionId}
                loading={versionQuery.isLoading}
              />

              <div className="grid gap-4 md:grid-cols-2">
                <Field label={t(($) => $.page.fields.name)}>
                  <Input value={draft.name} onChange={(event) => setDraftField(setDraft, "name", event.target.value)} />
                </Field>
                <Field label={t(($) => $.page.fields.description)}>
                  <Input value={draft.description} onChange={(event) => setDraftField(setDraft, "description", event.target.value)} />
                </Field>
              </div>

	              <Field label={t(($) => $.page.fields.content)}>
	                <Textarea
	                  value={draft.content}
	                  onChange={(event) => setDraftField(setDraft, "content", event.target.value)}
	                  className="min-h-[360px] resize-y font-mono text-sm leading-6"
	                />
	              </Field>

	              {selected && (
	                <Field label={t(($) => $.page.fields.change_note)}>
	                  <Input
	                    value={changeNote}
	                    onChange={(event) => setChangeNote(event.target.value)}
	                    placeholder={t(($) => $.page.change_note_placeholder)}
	                  />
	                </Field>
	              )}

	                <PromptTrialPanel
	                  selected={selected}
	                  activeVersion={activeVersion}
                agents={agents}
                agentsLoading={agentQuery.isLoading}
	                  selectedAgentId={trialAgentId}
	                  onSelectedAgentIdChange={setTrialAgentId}
	                  variableNames={detectedVariables}
                variables={trialVariables}
                onVariablesChange={setTrialVariables}
                trials={promptTrials}
                trialsLoading={trialQuery.isLoading}
                running={createTrialMut.isPending}
                onRun={runPromptTrial}
              />

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

type TrainingAssetPanelBaseProps = {
  assets: PromptEvaluationAsset[];
  cases: PromptEvaluationStructuredCase[];
  runs: PromptEvaluationRun[];
  focusedIssueId: string | null;
  focusedCaseId: string | null;
  focusedIssueRunReviewHref: string | null;
  loading: boolean;
  saving: boolean;
  onToggleAssetStatus: (asset: PromptEvaluationAsset) => void;
  onDeleteAsset: (asset: PromptEvaluationAsset) => void;
  onImportDatasetFromTraces: (asset: PromptEvaluationAsset) => void;
  importingTraceDatasetAssetId: string | null;
  onCreateDatasetVersion: (asset: PromptEvaluationAsset, versionLabel?: string) => void;
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
};

type WorkbenchPanelProps = TrainingAssetPanelBaseProps & {
  activeTab: WorkbenchTab;
  workspaceId: string;
  focusedRunId: string | null;
  evidenceFocus: EvidenceFocus;
  runStatusFilter: RunStatusFilter;
  focusedIssueTaskIds: string[];
  onRunStatusFilterChange: (status: RunStatusFilter) => void;
  candidates: PromptEvaluationOptimizationCandidate[];
  skillResources: SkillResourceOption[];
  onCreateAsset: (assetType: PromptEvaluationAssetType) => void;
  onCreateSkillScenarioAsset: (assetType: Extract<PromptEvaluationAssetType, "数据集" | "测试套件">) => void;
  onCreateWritingBenchmarkAsset: () => void;
  onCreateCaseLibraryDataset: (name: string, description: string) => void;
  onUpdateCaseLibraryDataset: (asset: PromptEvaluationAsset, data: UpdatePromptEvaluationAssetRequest) => void;
  updatingCaseLibraryDatasetId: string | null;
  onDeleteCaseLibraryDataset: (asset: PromptEvaluationAsset) => void;
  deletingCaseLibraryDatasetId: string | null;
  onCreateCaseLibraryCase: (asset: PromptEvaluationAsset, draft: ManualCaseDraft) => Promise<unknown>;
  creatingCaseLibraryCase: boolean;
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
};

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
  onCreateCaseLibraryDataset,
  onUpdateCaseLibraryDataset,
  updatingCaseLibraryDatasetId,
  onDeleteCaseLibraryDataset,
  deletingCaseLibraryDatasetId,
  onToggleAssetStatus,
  onDeleteAsset,
  onImportDatasetFromTraces,
  importingTraceDatasetAssetId,
  onCreateDatasetVersion,
  creatingDatasetVersionAssetId,
  onCreateCase,
  onCreateCaseLibraryCase,
  creatingCaseAssetId,
  creatingCaseLibraryCase,
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
}: WorkbenchPanelProps) {
  const { t } = useT("prompt-library");
  const tabAssetType = tabToAssetType(activeTab);
  const tabAssetLabel = tabAssetType ? assetTypeLabel(tabAssetType) : activeTab;
  const tabAssets = tabAssetType ? assets.filter((asset) => asset.asset_type === tabAssetType) : assets;
  const visibleAssets = tabAssets;
  const visibleCandidates = candidates;
  if (activeTab === "提示词库") {
    return null;
  }

  if (activeTab === "用例库") {
    return (
      <CaseLibraryEditorPanel
        assets={assets}
        cases={cases}
        loading={loading}
        saving={saving}
        draft={caseDrafts[DEFAULT_CASE_LIBRARY_DRAFT_KEY] ?? emptyManualCaseDraft()}
        onDraftChange={(draft) => onCaseDraftsChange((prev) => ({ ...prev, [DEFAULT_CASE_LIBRARY_DRAFT_KEY]: draft }))}
        onCreateDataset={onCreateCaseLibraryDataset}
        creatingDataset={saving}
        onUpdateDataset={onUpdateCaseLibraryDataset}
        updatingDatasetId={updatingCaseLibraryDatasetId}
        onDeleteDataset={onDeleteCaseLibraryDataset}
        deletingDatasetId={deletingCaseLibraryDatasetId}
        onCreateDatasetVersion={onCreateDatasetVersion}
        creatingDatasetVersionAssetId={creatingDatasetVersionAssetId}
        onCreateCase={async (asset, draft) => {
          await onCreateCaseLibraryCase(asset, draft);
          onCaseDraftsChange((prev) => ({ ...prev, [DEFAULT_CASE_LIBRARY_DRAFT_KEY]: emptyManualCaseDraft() }));
        }}
        creating={creatingCaseLibraryCase}
        focusedCaseId={focusedCaseId}
        onUpdateCase={onUpdateCase}
        updatingCaseId={updatingCaseId}
        onDeleteCase={onDeleteCase}
        deletingCaseId={deletingCaseId}
      />
    );
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
            {activeTab === "测试套件" && (
              <Button
                size="sm"
                variant="secondary"
                data-testid={`create-skill-scenario-${routeIntro.route}`}
                onClick={() => onCreateSkillScenarioAsset(tabAssetType as Extract<PromptEvaluationAssetType, "数据集" | "测试套件">)}
                disabled={saving}
              >
                {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
                {t(($) => $.workbench.new_skill_scenario)}
              </Button>
            )}
            <Button size="sm" onClick={() => onCreateAsset(tabAssetType)} disabled={saving}>
              {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
              {t(($) => $.workbench.new_asset, { label: tabAssetLabel })}
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
                {t(($) => $.workbench.new_writing_benchmark)}
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
          onDeleteAsset={onDeleteAsset}
          onImportDatasetFromTraces={onImportDatasetFromTraces}
          importingTraceDatasetAssetId={importingTraceDatasetAssetId}
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
        />
      )}
    </section>
  );
}

type TrainingAssetPanelProps = TrainingAssetPanelBaseProps & {
  activeTab: WorkbenchTab;
  route: string;
  title: string;
};

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
  onDeleteAsset,
  onImportDatasetFromTraces,
  importingTraceDatasetAssetId,
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
}: TrainingAssetPanelProps) {
  const { t } = useT("prompt-library");
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
    <section
      className="grid gap-3"
      aria-label={t(($) => $.workbench.panel_aria, { title })}
      data-testid={`training-route-panel-${route}`}
    >
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
                <div className="mt-1 truncate text-xs text-muted-foreground">
                  {asset.description || t(($) => $.workbench.no_description)}
                </div>
                <div className="mt-1 text-[11px] text-muted-foreground">
                  {t(($) => $.workbench.updated, {
                    time: asset.updated_at,
                    summary: summarizeAssetPayload(asset, caseSummaries.get(asset.id)),
                  })}
                </div>
                {summarizeSkillScenarioTarget(asset) && (
                  <div className="mt-1 text-[11px] text-muted-foreground" data-testid={`skill-scenario-target-${asset.id}`}>
                    {t(($) => $.workbench.skill_scenario, { value: summarizeSkillScenarioTarget(asset) })}
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
                    {t(($) => $.workbench.archive_evidence)}
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
                    {t(($) => $.workbench.download_archive)}
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
                      {t(($) => $.workbench.lock_version)}
                    </Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      data-testid={`import-dataset-from-traces-${asset.id}`}
                      onClick={() => onImportDatasetFromTraces(asset)}
                      disabled={saving || importingTraceDatasetAssetId === asset.id}
                    >
                      {importingTraceDatasetAssetId === asset.id ? <Loader2 className="size-3.5 animate-spin" /> : <Download className="size-3.5" />}
                      {t(($) => $.workbench.import_trace_cases)}
                    </Button>
                  </>
                )}
                <Button size="sm" variant="secondary" onClick={() => onToggleAssetStatus(asset)} disabled={saving}>
                  {asset.status === "启用" ? t(($) => $.workbench.archive) : t(($) => $.workbench.enable)}
                </Button>
                <Button size="sm" variant="destructive" onClick={() => onDeleteAsset(asset)} disabled={saving}>
                  <Trash2 className="size-3.5" />
                  {t(($) => $.workbench.delete)}
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

function emptyTrainingRouteText(activeTab: WorkbenchTab) {
  switch (activeTab) {
    case "用例库":
      return "暂无用例库，先新建用例库或从 trace 导入用例";
    case "测试套件":
      return "暂无测试套件，先把稳定用例组织成可回归的套件";
    default:
      return "暂无评估资产";
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
    case "用例库": {
      const datasetRows = context.visibleAssets.reduce((sum, asset) => sum + asset.dataset_row_count, 0);
      const traceCases = context.cases.filter((item) => item.source === "trace").length;
      return {
        route: "datasets",
        title: "用例库",
        subtitle: "把真实 trace 和手工样例沉淀成可复跑的评测用例，用于后续测试套件和实验。",
        facts: [
          ["用例库", String(context.visibleAssets.length)],
          ["启用", String(enabledAssets)],
          ["用例", formatNumber(datasetRows)],
          ["trace 样本", formatNumber(traceCases)],
        ],
        evidence: "公开 API 创建/回读用例库，页面可从真实 trace 导入并维护结构化用例。",
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
        route: "runs",
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
        subtitle: "评估资产页面。",
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
  const { t } = useT("prompt-library");
  const actionLabel = activeTab === "用例库"
    ? t(($) => $.workbench.focused_issue.dataset_action)
    : t(($) => $.workbench.focused_issue.context_action);
  return (
    <section className="rounded-md border border-info/30 bg-info/5 px-3 py-2 text-xs" data-testid="training-focused-issue-callout">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <div className="font-medium text-foreground">
            {t(($) => $.workbench.focused_issue.title, { id: issueId })}
          </div>
          <div className="mt-0.5 text-muted-foreground">
            {taskCount > 0
              ? t(($) => $.workbench.focused_issue.task_count, { count: taskCount })
              : t(($) => $.workbench.focused_issue.loading)}
            {" "}{actionLabel}
          </div>
        </div>
        <a className="shrink-0 rounded border bg-background px-2 py-1 text-[11px] hover:bg-accent" href={`../issues/${encodeURIComponent(issueId)}`}>
          {t(($) => $.workbench.focused_issue.back)}
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
  const { t } = useT("prompt-library");
  return (
    <section className="rounded-md border border-border/70 bg-muted/15 px-4 py-3" data-testid={`training-route-intro-${route}`}>
      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <div className="text-xs font-medium text-muted-foreground">{t(($) => $.workbench.module_label)}</div>
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
    case "用例库": {
      const datasetRows = context.visibleAssets.reduce((sum, asset) => sum + asset.dataset_row_count, 0);
      const traceCases = context.cases.filter((item) => item.source === "trace").length;
      return {
        kicker: "用例库工作台",
        title: "用例入库、锁定版本、下游复用",
        description: "用例库页面关注评测输入本身：从 trace 或手工样例形成结构化用例，锁定可追溯版本，再供测试套件和实验引用。",
        badge: "用例事实",
        className: "border-sky-500/30 bg-sky-500/5",
        steps: [
          { label: "入口", title: "trace 导入或手工用例", detail: `${formatNumber(traceCases)} 条 trace 用例，用例库 ${formatNumber(context.visibleAssets.length)} 个` },
          { label: "版本", title: "锁定用例库版本", detail: `${formatNumber(datasetRows)} 条用例可形成版本指纹，避免实验偷偷读最新数据` },
          { label: "复用", title: "供测试套件和实验绑定", detail: "下游资产通过用例库版本证明输入一致，便于回归和对比" },
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

export function CaseLibraryEditorPanel({
  assets,
  cases,
  loading,
  saving,
  draft,
  onDraftChange,
  onCreateDataset,
  creatingDataset,
  onUpdateDataset,
  updatingDatasetId,
  onDeleteDataset,
  deletingDatasetId,
  onCreateDatasetVersion,
  creatingDatasetVersionAssetId,
  onCreateCase,
  creating,
  focusedCaseId,
  onUpdateCase,
  updatingCaseId,
  onDeleteCase,
  deletingCaseId,
}: {
  assets: PromptEvaluationAsset[];
  cases: PromptEvaluationStructuredCase[];
  loading: boolean;
  saving: boolean;
  draft: ManualCaseDraft;
  onDraftChange: (draft: ManualCaseDraft) => void;
  onCreateDataset: (name: string, description: string) => void;
  creatingDataset: boolean;
  onUpdateDataset: (asset: PromptEvaluationAsset, data: UpdatePromptEvaluationAssetRequest) => void;
  updatingDatasetId: string | null;
  onDeleteDataset: (asset: PromptEvaluationAsset) => void;
  deletingDatasetId: string | null;
  onCreateDatasetVersion: (asset: PromptEvaluationAsset, versionLabel?: string) => void;
  creatingDatasetVersionAssetId: string | null;
  onCreateCase: (asset: PromptEvaluationAsset, draft: ManualCaseDraft) => Promise<unknown>;
  creating: boolean;
  focusedCaseId: string | null;
  onUpdateCase: (caseId: string, data: UpdatePromptEvaluationCaseRequest) => Promise<unknown>;
  updatingCaseId: string | null;
  onDeleteCase: (caseId: string) => void;
  deletingCaseId: string | null;
}) {
  const [keywordFilter, setKeywordFilter] = useState("");
  const [tagFilter, setTagFilter] = useState("全部");
  const [showCreateForm, setShowCreateForm] = useState(false);
  const [showDatasetForm, setShowDatasetForm] = useState(false);
  const [datasetDraft, setDatasetDraft] = useState({ name: "", description: "" });
  const [editingDataset, setEditingDataset] = useState(false);
  const [datasetEditDraft, setDatasetEditDraft] = useState({ name: "", description: "" });
  const [showVersionForm, setShowVersionForm] = useState(false);
  const [versionLabelDraft, setVersionLabelDraft] = useState("");
  const [selectedAssetId, setSelectedAssetId] = useState<string | null>(null);
  const [editingCaseId, setEditingCaseId] = useState<string | null>(null);
  const [editDrafts, setEditDrafts] = useState<Record<string, ManualCaseDraft>>({});
  const datasetAssets = useMemo(
    () => assets.filter((asset) => asset.asset_type === "数据集"),
    [assets],
  );
  const casesByAsset = useMemo(() => buildCasesByAsset(cases), [cases]);
  const selectedAsset = useMemo(
    () => datasetAssets.find((asset) => asset.id === selectedAssetId) ?? datasetAssets[0] ?? null,
    [datasetAssets, selectedAssetId],
  );
  const selectedCases = useMemo(
    () => selectedAsset ? casesByAsset.get(selectedAsset.id) ?? [] : cases,
    [cases, casesByAsset, selectedAsset],
  );
  const caseTags = useMemo(() => uniqueSortedStrings(selectedCases.flatMap((item) => item.tags.map((value) => String(value)).filter(Boolean))), [selectedCases]);
  const filteredCases = useMemo(() => {
    const keyword = keywordFilter.trim().toLowerCase();
    return selectedCases
      .filter((item) => {
        const tagOK = tagFilter === "全部" || item.tags.some((value) => String(value) === tagFilter);
        const keywordOK = !keyword || datasetCaseSearchText(item).includes(keyword);
        return tagOK && keywordOK;
      })
      .toSorted((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at) || a.case_index - b.case_index);
  }, [selectedCases, keywordFilter, tagFilter]);
  const datasetFilter = keywordFilter.trim().toLowerCase();
  const filteredAssets = useMemo(() => {
    if (!datasetFilter) return datasetAssets;
    return datasetAssets.filter((asset) => {
      const text = [asset.name, asset.description, summarizeDatasetVersion(asset)].join(" ").toLowerCase();
      return text.includes(datasetFilter) || matchesPinyin(text, datasetFilter);
    });
  }, [datasetAssets, datasetFilter]);

  useEffect(() => {
    if (selectedAssetId && datasetAssets.some((asset) => asset.id === selectedAssetId)) return;
    setSelectedAssetId(datasetAssets[0]?.id ?? null);
  }, [datasetAssets, selectedAssetId]);

  useEffect(() => {
    setTagFilter("全部");
    setEditingDataset(false);
    setShowVersionForm(false);
    setVersionLabelDraft("");
    setShowCreateForm(false);
    setEditingCaseId(null);
  }, [selectedAssetId]);

  useEffect(() => {
    if (!selectedAsset) {
      setDatasetEditDraft({ name: "", description: "" });
      return;
    }
    setDatasetEditDraft({
      name: selectedAsset.name,
      description: selectedAsset.description,
    });
  }, [selectedAsset]);

  const submitDataset = () => {
    const name = datasetDraft.name.trim();
    if (!name) {
      toast.error("请输入数据集名称");
      return;
    }
    onCreateDataset(name, datasetDraft.description.trim());
    setDatasetDraft({ name: "", description: "" });
    setShowDatasetForm(false);
  };

  const submitDatasetEdit = () => {
    if (!selectedAsset) return;
    const name = datasetEditDraft.name.trim();
    if (!name) {
      toast.error("请输入数据集名称");
      return;
    }
    onUpdateDataset(selectedAsset, {
      name,
      description: datasetEditDraft.description.trim(),
      asset_type: "数据集",
      prompt_id: selectedAsset.prompt_id,
      payload: selectedAsset.payload,
      status: selectedAsset.status,
    });
    setEditingDataset(false);
  };

  const submitDatasetVersion = () => {
    if (!selectedAsset) return;
    onCreateDatasetVersion(selectedAsset, versionLabelDraft.trim() || "手动快照");
    setShowVersionForm(false);
    setVersionLabelDraft("");
  };

  const submitCase = async (asset: PromptEvaluationAsset, caseDraft: ManualCaseDraft) => {
    if (!caseDraft.caseName.trim()) {
      toast.error("请输入用例名称");
      return;
    }
    if (!caseDraft.variablesText.trim()) {
      toast.error("请输入用例输入");
      return;
    }
    await onCreateCase(asset, caseDraft);
    setShowCreateForm(false);
  };

  return (
    <section className="grid min-h-[620px] gap-0 overflow-hidden rounded-md border md:grid-cols-[320px_minmax(0,1fr)]" data-testid="case-library-editor">
      <aside className="flex min-h-0 flex-col border-b md:border-b-0 md:border-r">
        <div className="grid gap-3 border-b p-3">
          <div className="flex items-center justify-between gap-2">
            <div className="min-w-0">
              <h2 className="text-base font-semibold">评估数据集</h2>
              <div className="mt-1 text-xs text-muted-foreground">
                {loading ? "正在读取数据集" : `${datasetAssets.length} 个数据集 · ${cases.length} 条用例`}
              </div>
            </div>
            <Button size="sm" onClick={() => setShowDatasetForm((value) => !value)} disabled={saving || creatingDataset}>
              <Plus className="size-3.5" />
              新建
            </Button>
          </div>
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={keywordFilter}
              onChange={(event) => setKeywordFilter(event.target.value)}
              placeholder="搜索数据集、用例、标签"
              aria-label="搜索数据集和用例"
              className="h-8 pl-8 text-sm"
            />
          </div>
          {showDatasetForm && (
            <div className="grid gap-2 rounded-md border bg-muted/10 p-2">
              <Input
                value={datasetDraft.name}
                onChange={(event) => setDatasetDraft((current) => ({ ...current, name: event.target.value }))}
                placeholder="数据集名称"
                className="h-8 text-sm"
              />
              <Input
                value={datasetDraft.description}
                onChange={(event) => setDatasetDraft((current) => ({ ...current, description: event.target.value }))}
                placeholder="描述"
                className="h-8 text-sm"
              />
              <div className="flex justify-end gap-2">
                <Button size="sm" variant="ghost" onClick={() => setShowDatasetForm(false)}>
                  取消
                </Button>
                <Button size="sm" onClick={submitDataset} disabled={creatingDataset || !datasetDraft.name.trim()}>
                  {creatingDataset ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                  保存
                </Button>
              </div>
            </div>
          )}
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {loading ? (
            <div className="space-y-2 p-3">
              {Array.from({ length: 4 }).map((_, index) => (
                <div key={index} className="h-16 rounded-md bg-muted/60" />
              ))}
            </div>
          ) : filteredAssets.length === 0 ? (
            <div className="p-6 text-sm text-muted-foreground">
              {datasetAssets.length === 0 ? "暂无数据集，可以先新建一个评估数据集。" : "当前搜索没有命中数据集。"}
            </div>
          ) : (
            <div className="divide-y" data-testid="case-library-dataset-list">
              {filteredAssets.map((asset) => {
                const assetCases = casesByAsset.get(asset.id) ?? [];
                const selected = selectedAsset?.id === asset.id;
                return (
                  <button
                    key={asset.id}
                    type="button"
                    onClick={() => {
                      setSelectedAssetId(asset.id);
                      setShowCreateForm(false);
                      setEditingCaseId(null);
                    }}
                    className={`flex w-full flex-col gap-1 px-3 py-3 text-left transition-colors hover:bg-muted/60 ${selected ? "bg-muted" : ""}`}
                    data-testid={`case-library-dataset-${asset.id}`}
                  >
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="min-w-0 flex-1 truncate text-sm font-medium">{asset.name}</span>
                      <Badge variant="outline" className="shrink-0 text-[10px]">{assetCases.length}</Badge>
                    </div>
                    <div className="truncate text-xs text-muted-foreground">{asset.description || "无描述"}</div>
                    <div className="truncate text-[11px] text-muted-foreground">
                      {summarizeDatasetVersion(asset) || `更新于 ${asset.updated_at || "未记录时间"}`}
                    </div>
                  </button>
                );
              })}
            </div>
          )}
        </div>
      </aside>

      <main className="min-h-0 overflow-y-auto p-4">
        {!selectedAsset && !loading ? (
          <div className="grid min-h-[360px] place-items-center rounded-md border border-dashed px-4 py-10 text-center text-sm text-muted-foreground" data-testid="case-library-empty">
            <div>
              <div className="font-medium text-foreground">还没有评估数据集</div>
              <div className="mt-1">先新建数据集，再沉淀可复现的评估用例。</div>
            </div>
          </div>
        ) : selectedAsset ? (
          <div className="grid gap-4">
            <div className="flex flex-col gap-3 border-b pb-4 md:flex-row md:items-start md:justify-between">
              <div className="min-w-0 flex-1">
                {editingDataset ? (
                  <div className="grid gap-2">
                    <div className="grid gap-2 md:grid-cols-2">
                      <Input
                        value={datasetEditDraft.name}
                        onChange={(event) => setDatasetEditDraft((current) => ({ ...current, name: event.target.value }))}
                        placeholder="数据集名称"
                      />
                      <Input
                        value={datasetEditDraft.description}
                        onChange={(event) => setDatasetEditDraft((current) => ({ ...current, description: event.target.value }))}
                        placeholder="描述"
                      />
                    </div>
                    <div className="flex flex-wrap gap-2">
                      <Button size="sm" onClick={submitDatasetEdit} disabled={saving || updatingDatasetId === selectedAsset.id || !datasetEditDraft.name.trim()}>
                        {updatingDatasetId === selectedAsset.id ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                        保存数据集
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => setEditingDataset(false)}>
                        取消
                      </Button>
                    </div>
                  </div>
                ) : (
                  <>
                    <h2 className="truncate text-base font-semibold">{selectedAsset.name}</h2>
                    <div className="mt-1 text-sm text-muted-foreground">{selectedAsset.description || "无描述"}</div>
                    <div className="mt-2 flex flex-wrap gap-2 text-xs text-muted-foreground">
                      <Badge variant="outline">{selectedCases.length} 条用例</Badge>
                      <span>更新于 {selectedAsset.updated_at || "未记录时间"}</span>
                    </div>
                  </>
                )}
              </div>
              <div className="flex shrink-0 flex-wrap gap-2">
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => setShowVersionForm((value) => !value)}
                  disabled={saving || creatingDatasetVersionAssetId === selectedAsset.id || selectedCases.length === 0}
                >
                  {creatingDatasetVersionAssetId === selectedAsset.id ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                  创建版本
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  data-testid={`edit-case-library-dataset-${selectedAsset.id}`}
                  onClick={() => setEditingDataset(true)}
                  disabled={saving || editingDataset}
                >
                  编辑
                </Button>
                <Button
                  size="sm"
                  variant="destructive"
                  data-testid={`delete-case-library-dataset-${selectedAsset.id}`}
                  onClick={() => onDeleteDataset(selectedAsset)}
                  disabled={saving || deletingDatasetId === selectedAsset.id}
                >
                  {deletingDatasetId === selectedAsset.id ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
                  删除
                </Button>
                <Button size="sm" onClick={() => setShowCreateForm((value) => !value)} disabled={saving}>
                  <Plus className="size-3.5" />
                  新增用例
                </Button>
              </div>
            </div>

            {showVersionForm && (
              <div className="grid gap-2 rounded-md border bg-muted/10 p-3">
                <Field label="版本说明">
                  <Input
                    value={versionLabelDraft}
                    onChange={(event) => setVersionLabelDraft(event.target.value)}
                    placeholder="例如：补充登录失败边界用例"
                  />
                </Field>
                <div className="flex justify-end gap-2">
                  <Button size="sm" variant="ghost" onClick={() => setShowVersionForm(false)}>
                    取消
                  </Button>
                  <Button size="sm" onClick={submitDatasetVersion} disabled={creatingDatasetVersionAssetId === selectedAsset.id}>
                    {creatingDatasetVersionAssetId === selectedAsset.id ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
                    保存版本
                  </Button>
                </div>
              </div>
            )}

            <DatasetVersionHistoryPanel asset={selectedAsset} />

            <div className="flex flex-col gap-2 md:flex-row md:items-center" data-testid="case-library-toolbar">
              <select
                aria-label="筛选用例标签"
                className="h-8 rounded-md border bg-background px-2 text-sm"
                value={tagFilter}
                onChange={(event) => setTagFilter(event.target.value)}
              >
                <option value="全部">全部标签</option>
                {caseTags.map((tag) => (
                  <option key={tag} value={tag}>{tag}</option>
                ))}
              </select>
              <Badge variant="outline" className="h-8 px-2">
                命中 {filteredCases.length} / {selectedCases.length}
              </Badge>
            </div>

            {showCreateForm && (
              <CaseDraftEditor
                title="新增用例"
                draft={draft}
                onDraftChange={onDraftChange}
                saving={creating}
                onSave={() => submitCase(selectedAsset, draft)}
                onCancel={() => setShowCreateForm(false)}
                saveLabel="保存用例"
              />
            )}

            {filteredCases.length === 0 ? (
              <div className="rounded-md border border-dashed px-3 py-8 text-center text-sm text-muted-foreground" data-testid="case-library-empty">
                {selectedCases.length === 0 ? "暂无用例，先新增一条评估用例。" : "当前筛选没有命中用例。"}
              </div>
            ) : (
              <div className="divide-y rounded-md border" data-testid="case-library-case-list">
                {filteredCases.map((item) => {
                  const editing = editingCaseId === item.id;
                  const editDraft = editDrafts[item.id] ?? manualCaseToDraft(item);
                  const focused = focusedCaseId === item.id;
                  return (
                    <div
                      key={item.id}
                      className={`grid gap-2 px-3 py-3 ${focused ? "bg-info/5 ring-1 ring-inset ring-info/40" : ""}`}
                      data-testid={`case-library-case-${item.id}`}
                    >
                      <div className="flex min-w-0 flex-col gap-2 md:flex-row md:items-start md:justify-between">
                        <div className="min-w-0">
                          <div className="flex min-w-0 flex-wrap items-center gap-2">
                            <span className="truncate text-sm font-medium">{item.case_name || `用例 ${item.case_index + 1}`}</span>
                            {item.source !== "manual" && (
                              <Badge variant="outline" className="text-[11px]">{caseSourceLabel(item.source)}</Badge>
                            )}
                          </div>
                          <div className="mt-2 grid gap-1 text-xs">
                            <div className="line-clamp-2 text-muted-foreground">
                              <span className="font-medium text-foreground">输入：</span>{caseLibraryInputText(item) || "未填写输入"}
                            </div>
                            <div className="line-clamp-2 text-muted-foreground">
                              <span className="font-medium text-foreground">期望：</span>{caseLibraryExpectedText(item) || "未填写期望"}
                            </div>
                          </div>
                          <div className="mt-1 truncate text-[11px] text-muted-foreground">
                            {item.tags.map(String).filter(Boolean).join("、") || "无标签"} · 更新于 {item.updated_at || "未记录时间"}
                          </div>
                        </div>
                        <div className="flex shrink-0 flex-wrap gap-2">
                          <Button
                            size="sm"
                            variant="secondary"
                            className="h-8"
                            onClick={() => {
                              setEditingCaseId(item.id);
                              setEditDrafts((prev) => ({ ...prev, [item.id]: manualCaseToDraft(item) }));
                            }}
                          >
                            编辑
                          </Button>
                          <Button size="sm" variant="destructive" className="h-8" onClick={() => onDeleteCase(item.id)} disabled={deletingCaseId === item.id || saving}>
                            {deletingCaseId === item.id ? <Loader2 className="size-3.5 animate-spin" /> : <Trash2 className="size-3.5" />}
                            删除
                          </Button>
                        </div>
                      </div>
                      {editing && (
                        <CaseDraftEditor
                          title="编辑用例"
                          draft={editDraft}
                          onDraftChange={(nextDraft) => setEditDrafts((prev) => ({ ...prev, [item.id]: nextDraft }))}
                          saving={updatingCaseId === item.id}
                          onSave={async () => {
                            await onUpdateCase(item.id, buildCaseLibraryUpdateRequest(item, editDraft));
                            setEditingCaseId(null);
                          }}
                          onCancel={() => setEditingCaseId(null)}
                          saveLabel="保存"
                        />
                      )}
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        ) : (
          <div className="h-28 rounded-md bg-muted/60" />
        )}
      </main>
    </section>
  );
}

function CaseDraftEditor({
  title,
  draft,
  onDraftChange,
  saving,
  onSave,
  onCancel,
  saveLabel,
}: {
  title: string;
  draft: ManualCaseDraft;
  onDraftChange: (draft: ManualCaseDraft) => void;
  saving: boolean;
  onSave: () => Promise<unknown>;
  onCancel: () => void;
  saveLabel: string;
}) {
  return (
    <div className="grid gap-3 rounded-md border bg-muted/10 p-3" data-testid="case-library-draft-editor">
      <div className="text-sm font-medium">{title}</div>
      <div className="grid gap-3 md:grid-cols-2">
        <Field label="名称">
          <Input
            value={draft.caseName}
            onChange={(event) => onDraftChange({ ...draft, caseName: event.target.value })}
            placeholder="例如：登录失败时说明原因"
          />
        </Field>
        <Field label="标签">
          <Input
            value={draft.tagsText}
            onChange={(event) => onDraftChange({ ...draft, tagsText: event.target.value })}
            placeholder="账号系统, 回归"
          />
        </Field>
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        <Field label="输入">
          <Textarea
            value={draft.variablesText}
            onChange={(event) => onDraftChange({ ...draft, variablesText: event.target.value })}
            className="min-h-28 font-mono text-sm"
            placeholder="用户输入、问题描述或待评估内容"
          />
        </Field>
        <Field label="期望">
          <Textarea
            value={draft.expectedText}
            onChange={(event) => onDraftChange({ ...draft, expectedText: event.target.value })}
            className="min-h-28 text-sm"
            placeholder="期望输出、判断标准或必须覆盖的要点；多行会拆成简单包含断言"
          />
        </Field>
      </div>
      <div className="flex justify-end gap-2">
        <Button size="sm" variant="ghost" onClick={onCancel}>
          取消
        </Button>
        <Button size="sm" onClick={() => void onSave()} disabled={saving || !draft.caseName.trim() || !draft.variablesText.trim()}>
          {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
          {saveLabel}
        </Button>
      </div>
    </div>
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
  focusedCaseId,
  focusedIssueId,
  focusedIssueRunReviewHref,
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
  onUpdateCase: (caseId: string, data: UpdatePromptEvaluationCaseRequest) => Promise<unknown>;
  updatingCaseId: string | null;
  onDeleteCase: (caseId: string) => void;
  deletingCaseId: string | null;
}) {
  const workspacePaths = useWorkspacePaths();
  const manualCases = cases.filter((item) => item.source === "manual");
  const traceCases = cases.filter((item) => item.source === "trace");
  const [caseSourceFilter, setCaseSourceFilter] = useState<DatasetCaseSourceFilter>("全部");
  const [caseTagFilter, setCaseTagFilter] = useState("全部");
  const [caseKeywordFilter, setCaseKeywordFilter] = useState("");
  const [editingCaseId, setEditingCaseId] = useState<string | null>(null);
  const [editDrafts, setEditDrafts] = useState<Record<string, ManualCaseDraft>>({});
  const [tagEditingCaseId, setTagEditingCaseId] = useState<string | null>(null);
  const [tagEditDrafts, setTagEditDrafts] = useState<Record<string, string>>({});
  const caseTags = useMemo(() => uniqueSortedStrings(cases.flatMap((item) => item.tags.map((value) => String(value)).filter(Boolean))), [cases]);
  const filteredCases = useMemo(() => {
    const keyword = caseKeywordFilter.trim().toLowerCase();
    return cases.filter((item) => {
      const sourceOK = caseSourceFilter === "全部" || caseSourceLabel(item.source) === caseSourceFilter;
      const tagOK = caseTagFilter === "全部" || item.tags.some((value) => String(value) === caseTagFilter);
      const keywordOK = !keyword || datasetCaseSearchText(item).includes(keyword);
      return sourceOK && tagOK && keywordOK;
    });
  }, [caseSourceFilter, caseTagFilter, caseKeywordFilter, cases]);
  return (
    <div data-testid={`prompt-evaluation-cases-${asset.id}`} className="md:col-span-2 grid gap-2 rounded-md border border-border/70 bg-muted/10 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-xs font-medium text-muted-foreground">结构化评测用例</div>
        <Badge variant="outline" className="text-[11px]">
          手工 {manualCases.length} · trace {traceCases.length} · draft {cases.filter((item) => item.status === "draft").length} · approved {cases.filter((item) => item.status === "approved").length} · active {cases.filter((item) => item.status === "active" || item.status === "启用").length}
        </Badge>
      </div>
      <CaseFilterBar
        totalCount={cases.length}
        visibleCount={filteredCases.length}
        tags={caseTags}
        sourceFilter={caseSourceFilter}
        onSourceFilterChange={setCaseSourceFilter}
        tagFilter={caseTagFilter}
        onTagFilterChange={setCaseTagFilter}
        keywordFilter={caseKeywordFilter}
        onKeywordFilterChange={setCaseKeywordFilter}
      />
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
                      placeholder="编辑用例标签"
                      aria-label="编辑用例标签"
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

function CaseFilterBar({
  totalCount,
  visibleCount,
  tags,
  sourceFilter,
  onSourceFilterChange,
  tagFilter,
  onTagFilterChange,
  keywordFilter,
  onKeywordFilterChange,
}: {
  totalCount: number;
  visibleCount: number;
  tags: string[];
  sourceFilter: DatasetCaseSourceFilter;
  onSourceFilterChange: (value: DatasetCaseSourceFilter) => void;
  tagFilter: string;
  onTagFilterChange: (value: string) => void;
  keywordFilter: string;
  onKeywordFilterChange: (value: string) => void;
}) {
  return (
    <section className="grid gap-2 rounded-md border bg-background px-3 py-2 text-xs" data-testid="case-library-filter-bar">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="font-medium text-foreground">用例筛选</div>
          <div className="mt-0.5 text-muted-foreground">按来源、标签和关键词快速定位用例。</div>
        </div>
        <Badge variant="outline" data-testid="case-library-filter-count">
          命中 {visibleCount} / {totalCount}
        </Badge>
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
          aria-label="筛选用例标签"
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
          aria-label="筛选用例关键词"
          className="h-8 min-w-60 flex-1 text-xs"
        />
      </div>
    </section>
  );
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

function tabToAssetType(tab: WorkbenchTab): PromptEvaluationAssetType | null {
  if (tab === "用例库") return "数据集";
  if (tab === "测试套件") return "测试套件";
  return null;
}

function assetTypeLabel(assetType: PromptEvaluationAssetType): string {
  return assetType === "数据集" ? "用例库" : assetType;
}

function canManageStructuredCases(asset: PromptEvaluationAsset): boolean {
  return asset.asset_type === "数据集" || asset.asset_type === "测试套件";
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
    status: "active",
  };
}

function buildCaseLibraryCreateRequest(asset: PromptEvaluationAsset, draft: ManualCaseDraft, existingCount: number): CreatePromptEvaluationCaseRequest {
  const inputText = draft.variablesText.trim();
  const expectedText = draft.expectedText.trim();
  const expectedContains = splitExpectationLines(expectedText);
  return {
    asset_id: asset.id,
    prompt_id: asset.prompt_id,
    case_index: existingCount,
    case_name: draft.caseName.trim(),
    variables: inputText ? { input: inputText } : {},
    expected_contains: expectedContains,
    input: {
      内容: inputText,
      来源: "用例库手工维护",
    },
    expected: {
      内容: expectedText,
      期望包含: expectedContains,
    },
    tags: splitList(draft.tagsText),
    status: "active",
  };
}

function buildCaseLibraryUpdateRequest(item: PromptEvaluationStructuredCase, draft: ManualCaseDraft): UpdatePromptEvaluationCaseRequest {
  const inputText = draft.variablesText.trim();
  const expectedText = draft.expectedText.trim();
  const expectedContains = splitExpectationLines(expectedText);
  return {
    asset_id: item.asset_id,
    prompt_id: item.prompt_id,
    case_index: item.case_index,
    case_name: draft.caseName.trim(),
    variables: inputText ? { input: inputText } : {},
    expected_contains: expectedContains,
    input: {
      内容: inputText,
      来源: "用例库手工维护",
      最近人工维护: new Date().toISOString(),
    },
    expected: {
      内容: expectedText,
      期望包含: expectedContains,
    },
    tags: splitList(draft.tagsText),
    status: item.status,
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
    variablesText: caseLibraryInputText(item),
    expectedText: caseLibraryExpectedText(item),
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

function summarizeJSONValue(value: unknown): string {
  if (!value || (typeof value === "object" && !Array.isArray(value) && Object.keys(value as Record<string, unknown>).length === 0)) {
    return "无额外配置";
  }
  const text = JSON.stringify(value);
  if (!text) return "无额外配置";
  return text.length > 120 ? `${text.slice(0, 117)}...` : text;
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

function summarizeStructuredCase(item: PromptEvaluationStructuredCase): string {
  const expected = item.expected_contains.map((value) => String(value)).filter(Boolean);
  const variables = Object.keys(item.variables ?? {});
  const parts = [];
  if (variables.length > 0) parts.push(`变量 ${variables.join("、")}`);
  if (expected.length > 0) parts.push(`期望 ${expected.join("、")}`);
  return parts.length > 0 ? parts.join(" · ") : "未填写输入和期望";
}

function caseLibraryInputText(item: PromptEvaluationStructuredCase): string {
  const inputRecord = item.input ?? {};
  const variableRecord = item.variables ?? {};
  return (
    stringFromRecord(inputRecord, "内容") ||
    stringFromRecord(inputRecord, "input") ||
    stringFromRecord(variableRecord, "input") ||
    Object.entries(variableRecord).map(([key, value]) => `${key}=${String(value)}`).join("\n")
  ).trim();
}

function caseLibraryExpectedText(item: PromptEvaluationStructuredCase): string {
  const expectedRecord = item.expected ?? {};
  return (
    stringFromRecord(expectedRecord, "内容") ||
    stringFromRecord(expectedRecord, "expected") ||
    item.expected_contains.map((value) => String(value)).join("\n")
  ).trim();
}

function splitExpectationLines(value: string): string[] {
  return value
    .split(/[\n\r,，]/)
    .map((part) => part.trim())
    .filter(Boolean);
}

function DatasetVersionHistoryPanel({ asset }: { asset: PromptEvaluationAsset }) {
  const workspaceId = asset.workspace_id;
  const versionsQuery = useQuery({
    queryKey: promptLibraryKeys.datasetVersions(workspaceId, asset.id),
    queryFn: () => api.listPromptEvaluationDatasetVersions(asset.id, 10),
    enabled: Boolean(workspaceId && asset.id),
  });
  const versions = versionsQuery.data?.items ?? [];
  return (
    <section className="grid gap-2 rounded-md border bg-muted/10 p-3" data-testid={`case-library-version-history-${asset.id}`}>
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-sm font-semibold">版本历史</h3>
          <div className="mt-1 text-xs text-muted-foreground">
            {versionsQuery.isLoading ? "正在读取版本" : versions.length > 0 ? `${versions.length} 个版本快照` : "暂无版本快照"}
          </div>
        </div>
        {versionsQuery.isFetching && <Loader2 className="size-3.5 animate-spin text-muted-foreground" />}
      </div>
      {versions.length === 0 ? (
        <div className="rounded-md border border-dashed bg-background px-3 py-3 text-sm text-muted-foreground">
          创建版本后，后续评估和调试可以固定使用这批用例。
        </div>
      ) : (
        <div className="grid gap-2">
          {versions.slice(0, 5).map((version) => (
            <div key={version.id} className="grid gap-1 rounded-md border bg-background px-3 py-2 text-xs md:grid-cols-[minmax(0,1fr)_auto]">
              <div className="min-w-0">
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <span className="font-medium text-foreground">v{version.version}</span>
                  <span className="truncate text-foreground">{version.version_label || "未命名版本"}</span>
                  {versions[0]?.id === version.id && <Badge variant="outline" className="text-[10px]">最新</Badge>}
                </div>
                <div className="mt-1 truncate text-muted-foreground">
                  {version.row_count} 条用例 · 指纹 {version.row_fingerprint ? version.row_fingerprint.slice(0, 10) : "未生成"}
                </div>
              </div>
              <div className="text-muted-foreground md:text-right">{version.created_at || "未记录时间"}</div>
            </div>
          ))}
        </div>
      )}
    </section>
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
  const parts = [`用例库版本 v${versionNumber || "?"}`];
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
      const label = datasetName || "用例库";
      const versionLabel = version ? `v${version}` : versionId ? `版本 ${versionId.slice(0, 8)}` : "未声明版本";
      return `${label} ${versionLabel}${fingerprint ? ` · 指纹 ${fingerprint.slice(0, 10)}` : ""}`;
    })
    .filter(Boolean);
  return parts.length > 0 ? `绑定用例库版本：${parts.join("；")}` : null;
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
