"use client";

import { useCallback, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import type { PromptEvaluationOptimizationCandidate } from "@multica/core/types";
import { useT } from "../../i18n/use-t";
import { promptLibraryKeys } from "./prompt-library-query-keys";
import {
  defaultSkillCandidateWorkflowDraft,
  type SkillCandidateWorkflowAction,
  type SkillCandidateWorkflowDraft,
} from "./skill-candidate-model";

export function useSkillCandidateWorkflowActions(workspaceId: string, runId: string) {
  const { t } = useT("prompt-library");
  const queryClient = useQueryClient();
  const [drafts, setDrafts] = useState<Record<string, SkillCandidateWorkflowDraft>>({});
  const [activeAction, setActiveAction] = useState<{
    candidateId: string;
    action: SkillCandidateWorkflowAction;
  } | null>(null);

  const invalidate = useCallback(() => {
    if (!workspaceId || !runId) return;
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runCandidates(workspaceId, runId) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.candidates(workspaceId) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.assets(workspaceId) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runs(workspaceId) });
  }, [queryClient, runId, workspaceId]);

  const runAction = useCallback(
    async (candidate: PromptEvaluationOptimizationCandidate, action: SkillCandidateWorkflowAction) => {
      const draft = drafts[candidate.id] ?? defaultSkillCandidateWorkflowDraft(candidate);
      setActiveAction({ candidateId: candidate.id, action });
      try {
        if (action === "freshness") {
          const result = await api.checkPromptEvaluationSkillCandidateFreshness(candidate.id, {
            source_resource_id: draft.sourceResourceId || undefined,
            repo_path: draft.repoPath.trim() || undefined,
            target_branch: draft.targetBranch.trim() || undefined,
            skill_path: draft.skillPath.trim() || undefined,
          });
          toast.success(t(($) => $.run_evidence.toast.skill_freshness, {
            status: result.status,
            patch: result.patch_check,
          }));
        } else if (action === "apply") {
          const result = await api.applyPromptEvaluationSkillCandidate(candidate.id, {
            source_resource_id: draft.sourceResourceId || undefined,
            repo_path: draft.repoPath.trim() || undefined,
            target_branch: draft.targetBranch.trim() || undefined,
            skill_path: draft.skillPath.trim() || undefined,
            changelog_path: draft.changelogPath.trim() || undefined,
            allow_dirty: draft.allowDirty,
            skip_changelog: draft.skipChangelog,
          });
          toast.success(t(($) => $.run_evidence.toast.skill_apply, { status: result.apply.status }));
        } else if (action === "prepare-re-eval") {
          const result = await api.preparePromptEvaluationSkillReEvalAsset(candidate.id, {
            source_resource_id: draft.sourceResourceId || undefined,
            repo_path: draft.repoPath.trim() || undefined,
            target_branch: draft.targetBranch.trim() || undefined,
            skill_path: draft.skillPath.trim() || undefined,
            include_draft: draft.includeDraft,
          });
          setDrafts((current) => ({
            ...current,
            [candidate.id]: { ...draft, reEvalAssetId: result.asset.id },
          }));
          toast.success(t(($) => $.run_evidence.toast.re_eval_ready, { count: result.case_count }));
        } else {
          const result = await api.runPromptEvaluationSkillReEval(candidate.id, {
            asset_id: draft.reEvalAssetId.trim() || undefined,
          });
          toast.success(t(($) => $.run_evidence.toast.re_eval_run, { status: result.run.status }));
        }
        invalidate();
      } catch (cause) {
        toast.error(cause instanceof Error ? cause.message : t(($) => $.run_evidence.toast.workflow_failed));
      } finally {
        setActiveAction(null);
      }
    },
    [drafts, invalidate, t],
  );

  const draftFor = useCallback(
    (candidate: PromptEvaluationOptimizationCandidate) =>
      drafts[candidate.id] ?? defaultSkillCandidateWorkflowDraft(candidate),
    [drafts],
  );

  return { drafts, setDrafts, activeAction, runAction, draftFor };
}
