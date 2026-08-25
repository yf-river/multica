import { describe, expect, it } from "vitest";
import type { Project, ProjectResource, PromptEvaluationOptimizationCandidate } from "@multica/core/types";
import {
  buildSkillResourceOptions,
  candidateSkillWorkflowEvidence,
  defaultSkillCandidateWorkflowDraft,
} from "./skill-candidate-model";

describe("skill candidate model", () => {
  it("normalizes supported project resources without embedding presentation copy", () => {
    const projects = [{ id: "project-1", title: "项目甲" }] as Project[];
    const resources = [
      {
        id: "resource-local",
        project_id: "project-1",
        label: "本地仓库",
        resource_type: "local_directory",
        resource_ref: { local_path: "/workspace/multica" },
      },
      {
        id: "resource-gongfeng",
        project_id: "project-1",
        label: "工蜂仓库",
        resource_type: "gongfeng_repo",
        resource_ref: { project_path: "team/multica", ref: "main" },
      },
      {
        id: "resource-doc",
        project_id: "project-1",
        label: "文档",
        resource_type: "document",
        resource_ref: {},
      },
    ] as ProjectResource[];

    expect(buildSkillResourceOptions(projects, [resources])).toEqual([
      {
        id: "resource-local",
        projectTitle: "项目甲",
        title: "本地仓库",
        kind: "local",
        repo: "/workspace/multica",
        repoPath: "/workspace/multica",
        branch: "HEAD",
        requiresRepoPath: false,
      },
      {
        id: "resource-gongfeng",
        projectTitle: "项目甲",
        title: "工蜂仓库",
        kind: "gongfeng",
        repo: "team/multica",
        repoPath: "",
        branch: "main",
        requiresRepoPath: true,
      },
    ]);
  });

  it("derives the workflow draft from the freshest recorded evidence", () => {
    const candidate = {
      metrics: {
        skill_apply: {
          snapshot: { base_commit: "apply-commit", skill_path: "skills/current/SKILL.md" },
          changelog_path: "skills/current/CHANGELOG.md",
        },
        skill_freshness: { target_branch: "release" },
        skill_re_eval: { asset_id: "asset-1" },
      },
      source_prompt_snapshot: { base_commit: "stale-commit" },
      skill_patch: { repo_path: "/workspace/multica" },
    } as unknown as PromptEvaluationOptimizationCandidate;

    const evidence = candidateSkillWorkflowEvidence(candidate);
    expect(evidence.snapshot).toEqual({ base_commit: "apply-commit", skill_path: "skills/current/SKILL.md" });
    expect(defaultSkillCandidateWorkflowDraft(candidate)).toEqual({
      sourceResourceId: "",
      repoPath: "/workspace/multica",
      targetBranch: "release",
      skillPath: "skills/current/SKILL.md",
      changelogPath: "skills/current/CHANGELOG.md",
      reEvalAssetId: "asset-1",
      includeDraft: false,
      allowDirty: false,
      skipChangelog: false,
    });
  });
});
