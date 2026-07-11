// @vitest-environment jsdom

import type { PropsWithChildren } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { PromptEvaluationOptimizationCandidate } from "@multica/core/types";
import { useSkillCandidateWorkflowActions } from "./use-skill-candidate-workflow-actions";

const mocks = vi.hoisted(() => ({
  apply: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: { applyPromptEvaluationSkillCandidate: mocks.apply },
}));

vi.mock("sonner", () => ({
  toast: { success: mocks.toastSuccess, error: mocks.toastError },
}));

vi.mock("../../i18n/use-t", () => ({
  useT: () => ({ t: () => "translated" }),
}));

const candidate = {
  id: "candidate-1",
  metrics: {},
  skill_patch: {},
} as PromptEvaluationOptimizationCandidate;

describe("skill candidate workflow actions", () => {
  beforeEach(() => vi.clearAllMocks());

  it("owns apply mutation feedback and cache invalidation", async () => {
    mocks.apply.mockResolvedValue({ apply: { status: "applied" } });
    const queryClient = new QueryClient();
    const invalidateQueries = vi.spyOn(queryClient, "invalidateQueries");
    const wrapper = ({ children }: PropsWithChildren) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(
      () => useSkillCandidateWorkflowActions("workspace-1", "run-1"),
      { wrapper },
    );

    await act(() => result.current.runAction(candidate, "apply"));

    expect(mocks.apply).toHaveBeenCalledWith("candidate-1", {
      source_resource_id: undefined,
      repo_path: undefined,
      target_branch: undefined,
      skill_path: undefined,
      changelog_path: undefined,
      allow_dirty: false,
      skip_changelog: false,
    });
    expect(mocks.toastSuccess).toHaveBeenCalledTimes(1);
    expect(invalidateQueries).toHaveBeenCalledTimes(4);
    expect(result.current.activeAction).toBeNull();
  });
});
