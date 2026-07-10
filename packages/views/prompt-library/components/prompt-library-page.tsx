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
import { useNavigation } from "../../navigation";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { useT } from "../../i18n/use-t";
import {
  buildAssetPayload,
  draftToRequest,
  emptyDraft,
  itemToDraft,
  setDraftField,
  type PromptDraft,
} from "./prompt-library-request-builders";
import { trainingSelectedPromptStorageKey } from "./prompt-selection-storage";
import { PromptTrialPanel, PromptVersionHistory } from "./prompt-editor-panels";
import { Field } from "./form-field";
import { extractPromptVariables } from "./prompt-trial-model";
import { shortId } from "./record-utils";
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
import {
  CaseLibraryEditorPanel,
  type CaseLibraryEditorCopy,
} from "./case-library-editor";
import {
  ManualCasePanel,
  type ManualCasePanelCopy,
} from "./manual-case-panel";
import {
  buildCaseLibraryCreateRequest,
  buildCaseSummaries,
  buildCasesByAsset,
  buildManualCaseRequest,
  emptyManualCaseDraft,
  type CaseSummary,
  type ManualCaseDraft,
} from "./case-model";

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

function escapeCssIdentifier(value: string): string {
  if (typeof CSS !== "undefined" && typeof CSS.escape === "function") return CSS.escape(value);
  return value.replace(/["\\]/g, "\\$&");
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
      document.querySelector(`[data-testid="case-library-case-${escapeCssIdentifier(focusedCaseId)}"]`)?.scrollIntoView({
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
    const caseLibraryCopy: CaseLibraryEditorCopy = {
      title: t(($) => $.case_library.title),
      loading: t(($) => $.case_library.loading),
      count: (datasetCount, caseCount) =>
        t(($) => $.case_library.count, { datasets: datasetCount, cases: caseCount }),
      createDataset: t(($) => $.case_library.create_dataset),
      searchPlaceholder: t(($) => $.case_library.search_placeholder),
      searchAriaLabel: t(($) => $.case_library.search_aria_label),
      datasetNamePlaceholder: t(($) => $.case_library.dataset_name_placeholder),
      datasetDescriptionPlaceholder: t(($) => $.case_library.dataset_description_placeholder),
      cancel: t(($) => $.case_library.cancel),
      save: t(($) => $.case_library.save),
      missingDatasetNameError: t(($) => $.case_library.missing_dataset_name_error),
      missingCaseNameError: t(($) => $.case_library.missing_case_name_error),
      missingCaseInputError: t(($) => $.case_library.missing_case_input_error),
      noDatasets: t(($) => $.case_library.no_datasets),
      noDatasetSearchResults: t(($) => $.case_library.no_dataset_search_results),
      noDescription: t(($) => $.case_library.no_description),
      updatedAt: (value) => t(($) => $.case_library.updated_at, { time: value }),
      missingTime: t(($) => $.case_library.missing_time),
      emptyTitle: t(($) => $.case_library.empty_title),
      emptyDescription: t(($) => $.case_library.empty_description),
      saveDataset: t(($) => $.case_library.save_dataset),
      createVersion: t(($) => $.case_library.create_version),
      edit: t(($) => $.case_library.edit),
      delete: t(($) => $.case_library.delete),
      addCase: t(($) => $.case_library.add_case),
      versionLabel: t(($) => $.case_library.version_label),
      versionPlaceholder: t(($) => $.case_library.version_placeholder),
      defaultVersionLabel: t(($) => $.case_library.default_version_label),
      saveVersion: t(($) => $.case_library.save_version),
      tagFilterAriaLabel: t(($) => $.case_library.tag_filter_aria_label),
      allTags: t(($) => $.case_library.all_tags),
      matchCount: (visible, total) => t(($) => $.case_library.match_count, { visible, total }),
      newCaseTitle: t(($) => $.case_library.new_case_title),
      editCaseTitle: t(($) => $.case_library.edit_case_title),
      saveCase: t(($) => $.case_library.save_case),
      caseCount: (count) => t(($) => $.case_library.case_count, { count }),
      caseName: (index) => t(($) => $.case_library.case_name, { index }),
      sourceLabel: (source) => t(($) => $.case_library.source[source]),
      inputPrefix: t(($) => $.case_library.input_prefix),
      expectedPrefix: t(($) => $.case_library.expected_prefix),
      missingInput: t(($) => $.case_library.missing_input),
      missingExpected: t(($) => $.case_library.missing_expected),
      noTags: t(($) => $.case_library.no_tags),
      noCases: t(($) => $.case_library.no_cases),
      noCaseFilterResults: t(($) => $.case_library.no_case_filter_results),
      datasetVersionSummary: (summary) => {
        const version = summary.version ?? "?";
        const rows = summary.rowCount ?? "0";
        const fingerprint = summary.fingerprint ? summary.fingerprint.slice(0, 10) : t(($) => $.case_library.version_history.missing_fingerprint);
        return t(($) => $.case_library.dataset_version_summary, {
          version,
          rows,
          fingerprint,
        });
      },
      draft: {
        nameLabel: t(($) => $.case_library.draft.name_label),
        namePlaceholder: t(($) => $.case_library.draft.name_placeholder),
        tagsLabel: t(($) => $.case_library.draft.tags_label),
        tagsPlaceholder: t(($) => $.case_library.draft.tags_placeholder),
        inputLabel: t(($) => $.case_library.draft.input_label),
        inputPlaceholder: t(($) => $.case_library.draft.input_placeholder),
        expectedLabel: t(($) => $.case_library.draft.expected_label),
        expectedPlaceholder: t(($) => $.case_library.draft.expected_placeholder),
        cancel: t(($) => $.case_library.draft.cancel),
      },
      versionHistory: {
        title: t(($) => $.case_library.version_history.title),
        loading: t(($) => $.case_library.version_history.loading),
        count: (count) => t(($) => $.case_library.version_history.count, { count }),
        noSnapshots: t(($) => $.case_library.version_history.no_snapshots),
        emptyDescription: t(($) => $.case_library.version_history.empty_description),
        unnamedVersion: t(($) => $.case_library.version_history.unnamed_version),
        latest: t(($) => $.case_library.version_history.latest),
        rowFingerprint: (rowCount, fingerprint) =>
          t(($) => $.case_library.version_history.row_fingerprint, { count: rowCount, fingerprint }),
        missingFingerprint: t(($) => $.case_library.version_history.missing_fingerprint),
        missingTime: t(($) => $.case_library.version_history.missing_time),
      },
    };
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
        copy={caseLibraryCopy}
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
  const workspacePaths = useWorkspacePaths();
  const manualCaseCopy: ManualCasePanelCopy = {
    title: t(($) => $.manual_case.title),
    counts: ({ manual, trace, draft, approved, active }) =>
      t(($) => $.manual_case.counts, { manual, trace, draft, approved, active }),
    filter: {
      title: t(($) => $.manual_case.filter.title),
      description: t(($) => $.manual_case.filter.description),
      matchCount: (visible, total) => t(($) => $.manual_case.filter.match_count, { visible, total }),
      sourceLabel: t(($) => $.manual_case.filter.source_label),
      sourceOptions: {
        all: t(($) => $.manual_case.filter.source_all),
        manual: t(($) => $.manual_case.filter.source_manual),
        trace: t(($) => $.manual_case.filter.source_trace),
        payload: t(($) => $.manual_case.filter.source_payload),
      },
      tagsLabel: t(($) => $.manual_case.filter.tags_label),
      tagFilterAriaLabel: t(($) => $.manual_case.filter.tag_filter_aria_label),
      allTags: t(($) => $.manual_case.filter.all_tags),
      keywordPlaceholder: t(($) => $.manual_case.filter.keyword_placeholder),
      keywordAriaLabel: t(($) => $.manual_case.filter.keyword_aria_label),
    },
    noFilterResults: t(($) => $.manual_case.no_filter_results),
    caseName: (index) => t(($) => $.manual_case.case_name, { index }),
    sourceName: (source) => t(($) => $.manual_case.source[source]),
    statusName: (status) => {
      if (status === "draft") return t(($) => $.manual_case.status.draft);
      if (status === "approved") return t(($) => $.manual_case.status.approved);
      if (status === "active" || status === "启用") return t(($) => $.manual_case.status.active);
      if (status === "归档") return t(($) => $.manual_case.status.archived);
      return status;
    },
    summary: ({ variableNames, expectedValues }) => {
      if (variableNames.length > 0 && expectedValues.length > 0) {
        return t(($) => $.manual_case.summary.both, {
          names: variableNames.join("、"),
          values: expectedValues.join("、"),
        });
      }
      if (variableNames.length > 0) {
        return t(($) => $.manual_case.summary.variables, { names: variableNames.join("、") });
      }
      if (expectedValues.length > 0) {
        return t(($) => $.manual_case.summary.expected, { values: expectedValues.join("、") });
      }
      return t(($) => $.manual_case.summary.empty);
    },
    approveDraft: t(($) => $.manual_case.approve_draft),
    activateCase: t(($) => $.manual_case.activate_case),
    editCase: t(($) => $.manual_case.edit_case),
    deleteCase: t(($) => $.manual_case.delete_case),
    editTags: t(($) => $.manual_case.edit_tags),
    sourceIssue: (issueId) => t(($) => $.manual_case.source_issue, { id: shortId(issueId) }),
    openRunReview: t(($) => $.manual_case.open_run_review),
    validation: (value) => {
      if (value.kind === "contains") {
        return t(($) => $.manual_case.validation_contains, { values: value.values.join("、") });
      }
      if (value.kind === "expected-behavior") {
        return t(($) => $.manual_case.validation_behavior, { value: value.value });
      }
      return t(($) => $.manual_case.validation_label, { value: value.value });
    },
    evidence: (facts) =>
      t(($) => $.manual_case.evidence, {
        stages: facts.stageCount,
        lanes: facts.childLaneCount,
        timeline: facts.timelineNodeCount,
      }),
    tagsPlaceholder: t(($) => $.manual_case.tags_placeholder),
    tagsAriaLabel: t(($) => $.manual_case.tags_aria_label),
    saveTags: t(($) => $.manual_case.save_tags),
    cancel: t(($) => $.manual_case.cancel),
    editCaseNamePlaceholder: t(($) => $.manual_case.edit_case_name_placeholder),
    editVariablesPlaceholder: t(($) => $.manual_case.edit_variables_placeholder),
    editExpectedPlaceholder: t(($) => $.manual_case.edit_expected_placeholder),
    editTagsPlaceholder: t(($) => $.manual_case.edit_tags_placeholder),
    saveCase: t(($) => $.manual_case.save_case),
    noCases: t(($) => $.manual_case.no_cases),
    caseNamePlaceholder: t(($) => $.manual_case.case_name_placeholder),
    variablesPlaceholder: t(($) => $.manual_case.variables_placeholder),
    expectedPlaceholder: t(($) => $.manual_case.expected_placeholder),
    newTagsPlaceholder: t(($) => $.manual_case.new_tags_placeholder),
    addCase: t(($) => $.manual_case.add_case),
  };
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
                  runReviewHrefForIssue={(issueId) => `${workspacePaths.runReviews()}?issue=${encodeURIComponent(issueId)}`}
                  onUpdateCase={onUpdateCase}
                  updatingCaseId={updatingCaseId}
                  onDeleteCase={onDeleteCase}
                  deletingCaseId={deletingCaseId}
                  copy={manualCaseCopy}
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

function cssEscape(value: string): string {
  if (typeof CSS !== "undefined" && typeof CSS.escape === "function") {
    return CSS.escape(value);
  }
  return value.replace(/["\\]/g, "\\$&");
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
