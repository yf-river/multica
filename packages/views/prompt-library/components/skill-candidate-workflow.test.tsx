// @vitest-environment jsdom

import { fireEvent, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { PromptEvaluationOptimizationCandidate } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import type { SkillCandidateWorkflowDraft, SkillCandidateWorkflowEvidence } from "./skill-candidate-model";
import { SkillCandidateWorkflowPanel } from "./skill-candidate-workflow";

const candidate = {
  id: "candidate-1",
  skill_patch: {
    candidate_intent: "update_existing_skill",
    patch_hash: "1234567890abcdef",
    expected_improvement: "减少重复步骤",
    risk: "低",
    verification_plan: "运行回归测试",
  },
} as PromptEvaluationOptimizationCandidate;

const draft: SkillCandidateWorkflowDraft = {
  sourceResourceId: "",
  repoPath: "",
  targetBranch: "",
  skillPath: "skills/current/SKILL.md",
  changelogPath: "",
  reEvalAssetId: "",
  includeDraft: false,
  allowDirty: false,
  skipChangelog: false,
};

const evidence: SkillCandidateWorkflowEvidence = {
  snapshot: { base_commit: "abcdef1234567890", skill_hash: "hash1234567890" },
  freshness: {},
  apply: {},
  reEval: {},
  reEvalRun: {},
};

describe("SkillCandidateWorkflowPanel", () => {
  it("maps a selected resource into the workflow draft and dispatches explicit actions", () => {
    const onDraftChange = vi.fn();
    const onRunAction = vi.fn();
    renderWithI18n(
      <SkillCandidateWorkflowPanel
        candidate={candidate}
        draft={draft}
        evidence={evidence}
        resources={[
          {
            id: "resource-1",
            projectTitle: "项目甲",
            title: "仓库",
            kind: "local",
            repo: "/workspace/multica",
            repoPath: "/workspace/multica",
            branch: "HEAD",
            requiresRepoPath: false,
          },
        ]}
        pendingAction={null}
        disabled={false}
        onDraftChange={onDraftChange}
        onRunAction={onRunAction}
      />,
    );

    expect(screen.getByText("Skill 发布链路")).toBeInTheDocument();
    expect(screen.getByText("项目甲 · 本地 · 仓库")).toBeInTheDocument();
    fireEvent.change(screen.getByRole("combobox", { name: "项目资源" }), { target: { value: "resource-1" } });
    expect(onDraftChange).toHaveBeenCalledWith({
      ...draft,
      sourceResourceId: "resource-1",
      repoPath: "/workspace/multica",
      targetBranch: "HEAD",
    });

    fireEvent.click(screen.getByRole("button", { name: "Freshness" }));
    expect(onRunAction).toHaveBeenCalledWith("freshness");
    expect(screen.getByRole("button", { name: "Run re-eval" })).toBeDisabled();
  });
});
