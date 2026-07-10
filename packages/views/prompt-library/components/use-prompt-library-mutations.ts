"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import type {
  CreatePromptLibraryItemRequest,
  CreatePromptLibraryTrialRequest,
  CreatePromptLibraryVersionRequest,
  CreatePromptLibraryVersionResponse,
  CreatePromptEvaluationAssetRequest,
  CreatePromptEvaluationCaseRequest,
  PromptEvaluationAsset,
  PromptEvaluationStructuredCase,
  PromptLibraryItem,
  UpdatePromptEvaluationAssetRequest,
  UpdatePromptEvaluationCaseRequest,
} from "@multica/core/types";
import { useT } from "../../i18n/use-t";
import { buildCaseLibraryCreateRequest, type ManualCaseDraft } from "./case-model";
import { promptLibraryKeys } from "./prompt-library-query-keys";

interface PromptLibraryMutationOptions {
  workspaceId: string | null;
  focusedIssueId: string | null;
  focusedIssueTaskIds: string[];
  cases: PromptEvaluationStructuredCase[];
  onPromptCreated: (item: PromptLibraryItem) => void;
  onPromptVersionCreated: (result: CreatePromptLibraryVersionResponse) => void;
  onPromptDeleted: () => void;
}

/**
 * Owns Prompt Library writes, cache projection, and user-visible mutation errors.
 * Page-local editor state remains in PromptLibraryPage and is updated through
 * the three explicit callbacks above.
 */
export function usePromptLibraryMutations({
  workspaceId,
  focusedIssueId,
  focusedIssueTaskIds,
  cases,
  onPromptCreated,
  onPromptVersionCreated,
  onPromptDeleted,
}: PromptLibraryMutationOptions) {
  const { t } = useT("prompt-library");
  const queryClient = useQueryClient();
  const workspaceKey = workspaceId ?? "";
  const invalidateItems = () =>
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.list(workspaceKey) });
  const invalidateVersions = (promptId: string | null) =>
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.versions(workspaceKey, promptId) });
  const invalidateTrials = (promptId: string | null) =>
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.trials(workspaceKey, promptId) });
  const invalidateAssets = () =>
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.assets(workspaceKey) });
  const invalidateCases = () =>
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.cases(workspaceKey) });
  const invalidateRuns = () =>
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runs(workspaceKey) });
  const invalidateCandidates = () =>
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.candidates(workspaceKey) });
  const invalidateRunEvidence = (runId: string) => {
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runEvidence(workspaceKey, runId) });
    queryClient.invalidateQueries({
      queryKey: promptLibraryKeys.runEvidenceSnapshots(workspaceKey, runId),
    });
  };
  const reportError = (error: unknown) => {
    toast.error(error instanceof Error ? error.message : t(($) => $.page.toast.action_failed));
  };

  const createPrompt = useMutation({
    mutationFn: (data: CreatePromptLibraryItemRequest) => api.createPromptLibraryItem(data),
    onSuccess: (item) => {
      invalidateItems();
      invalidateVersions(item.id);
      onPromptCreated(item);
      toast.success(t(($) => $.page.toast.prompt_created));
    },
    onError: reportError,
  });

  const createPromptVersion = useMutation({
    mutationFn: ({ id, data }: { id: string; data: CreatePromptLibraryVersionRequest }) =>
      api.createPromptLibraryVersion(id, data),
    onSuccess: (result) => {
      invalidateItems();
      invalidateVersions(result.item.id);
      onPromptVersionCreated(result);
      toast.success(t(($) => $.page.toast.version_created, { version: result.version.version }));
    },
    onError: reportError,
  });

  const createPromptTrial = useMutation({
    mutationFn: ({ id, versionId, data }: { id: string; versionId: string; data: CreatePromptLibraryTrialRequest }) =>
      api.createPromptLibraryTrial(id, versionId, data),
    onSuccess: (_trial, variables) => {
      invalidateTrials(variables.id);
      toast.success(t(($) => $.page.toast.trial_submitted));
    },
    onError: reportError,
  });

  const deletePrompt = useMutation({
    mutationFn: (id: string) => api.deletePromptLibraryItem(id),
    onSuccess: () => {
      invalidateItems();
      onPromptDeleted();
      toast.success(t(($) => $.page.toast.prompt_deleted));
    },
    onError: reportError,
  });

  const createAsset = useMutation({
    mutationFn: (data: CreatePromptEvaluationAssetRequest) => api.createPromptEvaluationAsset(data),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      toast.success(t(($) => $.page.toast.asset_created));
    },
    onError: reportError,
  });

  const updateAsset = useMutation({
    mutationFn: ({ id, data }: { id: string; data: UpdatePromptEvaluationAssetRequest }) =>
      api.updatePromptEvaluationAsset(id, data),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      toast.success(t(($) => $.page.toast.updated));
    },
    onError: reportError,
  });

  const deleteAsset = useMutation({
    mutationFn: (id: string) => api.deletePromptEvaluationAsset(id),
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      invalidateRuns();
      toast.success(t(($) => $.page.toast.deleted));
    },
    onError: reportError,
  });

  const importDatasetFromTraces = useMutation({
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

  const createDatasetVersion = useMutation({
    mutationFn: ({ assetId, versionLabel }: { assetId: string; versionLabel: string }) =>
      api.createPromptEvaluationDatasetVersion(assetId, {
        version_label: versionLabel,
        metadata: {
          来源: "训练与评估页面",
          用途: "锁定当前用例库版本，供后续评估运行和实验对比复盘",
          创建时间: new Date().toISOString(),
        },
      }),
    onSuccess: (version, variables) => {
      invalidateAssets();
      queryClient.invalidateQueries({
        queryKey: promptLibraryKeys.datasetVersions(workspaceKey, variables.assetId),
      });
      toast.success(t(($) => $.page.toast.dataset_version_locked, { version: version.version }));
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : t(($) => $.page.toast.dataset_version_failed));
    },
  });

  const createCase = useMutation({
    mutationFn: (data: CreatePromptEvaluationCaseRequest) => api.createPromptEvaluationCase(data),
    onSuccess: () => {
      invalidateCases();
      toast.success(t(($) => $.page.toast.manual_case_created));
    },
    onError: reportError,
  });

  const updateCase = useMutation({
    mutationFn: ({ caseId, data }: { caseId: string; data: UpdatePromptEvaluationCaseRequest }) =>
      api.updatePromptEvaluationCase(caseId, data),
    onSuccess: () => {
      invalidateCases();
      toast.success(t(($) => $.page.toast.manual_case_saved));
    },
    onError: reportError,
  });

  const deleteCase = useMutation({
    mutationFn: (id: string) => api.deletePromptEvaluationCase(id),
    onSuccess: () => {
      invalidateCases();
      toast.success(t(($) => $.page.toast.manual_case_deleted));
    },
    onError: reportError,
  });

  const createCaseLibraryCase = useMutation({
    mutationFn: ({ asset, draft }: { asset: PromptEvaluationAsset; draft: ManualCaseDraft }) => {
      const existingCount = cases.filter((item) => item.asset_id === asset.id).length;
      return api.createPromptEvaluationCase(
        buildCaseLibraryCreateRequest(asset, draft, existingCount),
      );
    },
    onSuccess: () => {
      invalidateAssets();
      invalidateCases();
      toast.success(t(($) => $.page.toast.case_created));
    },
    onError: reportError,
  });

  const syncRun = useMutation({
    mutationFn: (runId: string) => api.syncPromptEvaluationRun(runId),
    onSuccess: (_run, runId) => {
      invalidateRuns();
      invalidateCandidates();
      invalidateRunEvidence(runId);
      toast.success(t(($) => $.page.toast.run_synced));
    },
    onError: reportError,
  });

  const cancelRun = useMutation({
    mutationFn: (runId: string) => api.cancelPromptEvaluationRun(runId),
    onSuccess: (run) => {
      invalidateRuns();
      invalidateCandidates();
      invalidateRunEvidence(run.id);
      toast.success(t(($) => $.page.toast.run_cancelled));
    },
    onError: reportError,
  });

  const reviewRun = useMutation({
    mutationFn: ({ runId, decision, note }: { runId: string; decision: "通过" | "未通过"; note: string }) =>
      api.reviewPromptEvaluationRun(runId, { decision, note }),
    onSuccess: (run) => {
      invalidateRuns();
      invalidateCandidates();
      invalidateRunEvidence(run.id);
      toast.success(t(($) => $.page.toast.reviewed, { status: run.review_decision || run.status }));
    },
    onError: reportError,
  });

  const createEvidenceSnapshot = useMutation({
    mutationFn: (runId: string) => api.createPromptEvaluationEvidenceSnapshot(runId, "验收归档"),
    onSuccess: (snapshot) => {
      queryClient.invalidateQueries({
        queryKey: promptLibraryKeys.runEvidenceSnapshots(workspaceKey, snapshot.run_id),
      });
      toast.success(t(($) => $.page.toast.snapshot_archived));
    },
    onError: reportError,
  });

  const createAssetEvidenceSnapshots = useMutation({
    mutationFn: (assetId: string) =>
      api.createPromptEvaluationAssetEvidenceSnapshots(assetId, "验收归档", 20),
    onSuccess: (result) => {
      invalidateRuns();
      for (const snapshot of result.items) {
        queryClient.invalidateQueries({
          queryKey: promptLibraryKeys.runEvidenceSnapshots(workspaceKey, snapshot.run_id),
        });
      }
      const skippedText = result.skipped_count > 0
        ? t(($) => $.page.toast.archive_skipped, { count: result.skipped_count })
        : "";
      toast.success(t(($) => $.page.toast.evidence_archived, {
        count: result.created_count,
        skipped: skippedText,
      }));
    },
    onError: reportError,
  });

  const createCandidate = useMutation({
    mutationFn: (runId: string) => api.createPromptEvaluationOptimizationCandidate(runId),
    onSuccess: () => {
      invalidateCandidates();
      toast.success(t(($) => $.page.toast.candidate_created));
    },
    onError: reportError,
  });

  return {
    reportError,
    createPrompt,
    createPromptVersion,
    createPromptTrial,
    deletePrompt,
    createAsset,
    updateAsset,
    deleteAsset,
    importDatasetFromTraces,
    createDatasetVersion,
    createCase,
    updateCase,
    deleteCase,
    createCaseLibraryCase,
    syncRun,
    cancelRun,
    reviewRun,
    createEvidenceSnapshot,
    createAssetEvidenceSnapshots,
    createCandidate,
  };
}
