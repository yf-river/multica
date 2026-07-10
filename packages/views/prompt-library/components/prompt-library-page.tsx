"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpenText, Loader2, Plus, Save, Search, Trash2 } from "lucide-react";
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
  assetTypeLabel,
  downloadTextFile,
  tabToAssetType,
  TrainingFocusedIssueCallout,
  TrainingRouteIntroCard,
  TrainingRouteWorkspaceBand,
  trainingRouteIntro,
} from "./training-workbench-support";
import {
  TrainingAssetPanel,
  type TrainingAssetPanelBaseProps,
} from "./training-asset-panel";
import {
  buildCaseLibraryCreateRequest,
  emptyManualCaseDraft,
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
  const reportMutationError = (error: unknown) => {
    toast.error(error instanceof Error ? error.message : t(($) => $.page.toast.action_failed));
  };
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
    onError: reportMutationError,
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
    onError: reportMutationError,
  });

  const createTrialMut = useMutation({
    mutationFn: ({ id, versionId, data }: { id: string; versionId: string; data: CreatePromptLibraryTrialRequest }) =>
      api.createPromptLibraryTrial(id, versionId, data),
    onSuccess: (_trial, variables) => {
      invalidateTrials(variables.id);
      toast.success(t(($) => $.page.toast.trial_submitted));
    },
    onError: reportMutationError,
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.deletePromptLibraryItem(id),
    onSuccess: () => {
      invalidate();
      rememberSelectedPrompt(null);
      setDraft(emptyDraft());
      toast.success(t(($) => $.page.toast.prompt_deleted));
    },
    onError: reportMutationError,
  });

  const updateAssetMut = useMutation({
    mutationFn: ({ id, data }: { id: string; data: UpdatePromptEvaluationAssetRequest }) => api.updatePromptEvaluationAsset(id, data),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      toast.success(t(($) => $.page.toast.updated));
    },
    onError: reportMutationError,
  });

  const deleteAssetMut = useMutation({
    mutationFn: (id: string) => api.deletePromptEvaluationAsset(id),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      toast.success(t(($) => $.page.toast.deleted));
    },
    onError: reportMutationError,
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
    onError: reportMutationError,
  });

  const updateCaseMut = useMutation({
    mutationFn: ({ caseId, data }: { caseId: string; data: UpdatePromptEvaluationCaseRequest }) => api.updatePromptEvaluationCase(caseId, data),
    onSuccess: () => {
      invalidateCases();
      toast.success(t(($) => $.page.toast.manual_case_saved));
    },
    onError: reportMutationError,
  });

  const deleteCaseMut = useMutation({
    mutationFn: (id: string) => api.deletePromptEvaluationCase(id),
    onSuccess: () => {
      invalidateCases();
      toast.success(t(($) => $.page.toast.manual_case_deleted));
    },
    onError: reportMutationError,
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
    onError: reportMutationError,
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
    onError: reportMutationError,
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
    onError: reportMutationError,
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
    onError: reportMutationError,
  });

  const createEvidenceSnapshotMut = useMutation({
    mutationFn: (runId: string) => api.createPromptEvaluationEvidenceSnapshot(runId, "验收归档"),
    onSuccess: (snapshot) => {
      invalidateRunEvidenceSnapshots(snapshot.run_id);
      toast.success(t(($) => $.page.toast.snapshot_archived));
    },
    onError: reportMutationError,
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
    onError: reportMutationError,
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
    } catch (error) {
      reportMutationError(error);
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
    onError: reportMutationError,
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
    onError: reportMutationError,
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

  const createCaseLibraryDataset = (name: string, description: string) =>
    createAssetMut.mutateAsync({
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

  const updateCaseLibraryDataset = (asset: PromptEvaluationAsset, data: UpdatePromptEvaluationAssetRequest) =>
    updateAssetMut.mutateAsync({ id: asset.id, data });

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
      onCreateDatasetVersion={(asset, versionLabel = "手动快照") => createDatasetVersionMut.mutateAsync({
        assetId: asset.id,
        versionLabel: versionLabel.trim() || "手动快照",
      })}
      creatingDatasetVersionAssetId={createDatasetVersionMut.isPending ? createDatasetVersionMut.variables?.assetId ?? null : null}
      onCreateCase={(data) => createCaseMut.mutateAsync(data)}
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
  onCreateCaseLibraryDataset: (name: string, description: string) => Promise<unknown>;
  onUpdateCaseLibraryDataset: (asset: PromptEvaluationAsset, data: UpdatePromptEvaluationAssetRequest) => Promise<unknown>;
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
        version: (version) => t(($) => $.case_library.version_history.version, { version }),
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
