"use client";

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  publishPromptEvaluationOptimizationCandidateWithRecovery,
  rejectPromptEvaluationOptimizationCandidateWithRecovery,
} from "@multica/core/prompt-library";
import { useT } from "../../i18n/use-t";
import { promptLibraryKeys } from "./prompt-library-query-keys";

export function useCandidateDecisionActions(workspaceId: string, runId: string) {
  const { t } = useT("prompt-library");
  const queryClient = useQueryClient();
  const [activeDecision, setActiveDecision] = useState<"publish" | "reject" | null>(null);

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.runCandidates(workspaceId, runId) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.candidates(workspaceId) });
    queryClient.invalidateQueries({ queryKey: promptLibraryKeys.list(workspaceId) });
  };

  const publish = async (candidateId: string) => {
    setActiveDecision("publish");
    try {
      const promptName = await publishPromptEvaluationOptimizationCandidateWithRecovery(candidateId);
      toast.success(t(($) => $.run_evidence.candidate_published, { name: promptName }));
      invalidate();
    } catch (cause) {
      toast.error(cause instanceof Error ? cause.message : t(($) => $.page.toast.action_failed));
    } finally {
      setActiveDecision(null);
    }
  };

  const reject = async (candidateId: string) => {
    setActiveDecision("reject");
    try {
      await rejectPromptEvaluationOptimizationCandidateWithRecovery(candidateId, "");
      toast.success(t(($) => $.run_evidence.candidate_rejected));
      invalidate();
    } catch (cause) {
      toast.error(cause instanceof Error ? cause.message : t(($) => $.page.toast.action_failed));
    } finally {
      setActiveDecision(null);
    }
  };

  return { activeDecision, publish, reject };
}
