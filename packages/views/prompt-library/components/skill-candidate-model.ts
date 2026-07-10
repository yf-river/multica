import type { Project, ProjectResource, PromptEvaluationOptimizationCandidate } from "@multica/core/types";
import { asRecord, isRecord, stringFromUnknown } from "./record-utils";

export type SkillCandidateWorkflowAction = "freshness" | "apply" | "prepare-re-eval" | "run-re-eval";

export type SkillResourceOption = {
  id: string;
  projectTitle: string;
  title: string;
  kind: "gongfeng" | "local";
  repo: string;
  repoPath: string;
  branch: string;
  requiresRepoPath: boolean;
};

export type SkillCandidateWorkflowDraft = {
  sourceResourceId: string;
  repoPath: string;
  targetBranch: string;
  skillPath: string;
  changelogPath: string;
  reEvalAssetId: string;
  includeDraft: boolean;
  allowDirty: boolean;
  skipChangelog: boolean;
};

export type SkillCandidateWorkflowEvidence = {
  snapshot: Record<string, unknown>;
  freshness: Record<string, unknown>;
  apply: Record<string, unknown>;
  reEval: Record<string, unknown>;
  reEvalRun: Record<string, unknown>;
};

export function defaultSkillCandidateWorkflowDraft(
  candidate: PromptEvaluationOptimizationCandidate,
): SkillCandidateWorkflowDraft {
  const evidence = candidateSkillWorkflowEvidence(candidate);
  const skillPatch = asRecord(candidate.skill_patch);
  return {
    sourceResourceId:
      stringFromUnknown(skillPatch["source_resource_id"]) || stringFromUnknown(evidence.snapshot["source_resource_id"]),
    repoPath: stringFromUnknown(skillPatch["repo_path"]) || stringFromUnknown(evidence.snapshot["repo_path"]),
    targetBranch:
      stringFromUnknown(evidence.freshness["target_branch"]) ||
      stringFromUnknown(skillPatch["target_branch"]) ||
      stringFromUnknown(evidence.snapshot["branch"]),
    skillPath:
      stringFromUnknown(evidence.freshness["skill_path"]) ||
      stringFromUnknown(skillPatch["skill_path"]) ||
      stringFromUnknown(evidence.snapshot["skill_path"]),
    changelogPath:
      stringFromUnknown(evidence.apply["changelog_path"]) || stringFromUnknown(skillPatch["changelog_path"]),
    reEvalAssetId: stringFromUnknown(evidence.reEval["asset_id"]),
    includeDraft: false,
    allowDirty: false,
    skipChangelog: false,
  };
}

export function buildSkillResourceOptions(projects: Project[], resourceGroups: ProjectResource[][]): SkillResourceOption[] {
  const projectTitles = new Map(projects.map((project) => [project.id, project.title]));
  return resourceGroups
    .flat()
    .filter((resource) => resource.resource_type === "gongfeng_repo" || resource.resource_type === "local_directory")
    .map((resource) => skillResourceOptionFromProjectResource(resource, projectTitles.get(resource.project_id) ?? ""))
    .filter((resource): resource is SkillResourceOption => resource !== null)
    .sort((a, b) => {
      const projectOrder = a.projectTitle.localeCompare(b.projectTitle, "zh-Hans");
      if (projectOrder !== 0) return projectOrder;
      const kindOrder = Number(a.kind === "gongfeng") - Number(b.kind === "gongfeng");
      return kindOrder !== 0 ? kindOrder : a.title.localeCompare(b.title, "zh-Hans");
    });
}

export function candidateSkillWorkflowEvidence(
  candidate: PromptEvaluationOptimizationCandidate,
): SkillCandidateWorkflowEvidence {
  const metrics = isRecord(candidate.metrics) ? candidate.metrics : {};
  const skillPatch = asRecord(candidate.skill_patch);
  const apply = asRecord(metrics["skill_apply"]);
  const freshness = firstRecord(asRecord(metrics["skill_freshness"]), asRecord(apply["freshness"]));
  const reEval = asRecord(metrics["skill_re_eval"]);
  const reEvalRun = asRecord(metrics["skill_re_eval_run"]);
  const sourceSnapshot = isRecord(candidate.source_prompt_snapshot) ? candidate.source_prompt_snapshot : {};
  const snapshot = firstRecord(
    asRecord(apply["snapshot"]),
    asRecord(freshness["snapshot"]),
    asRecord(reEval["re_eval_snapshot"]),
    asRecord(reEval["source_snapshot"]),
    asRecord(skillPatch["source_snapshot"]),
    asRecord(sourceSnapshot["skill_snapshot"]),
    hasSkillSnapshotShape(sourceSnapshot) ? sourceSnapshot : {},
  );
  return { snapshot, freshness, apply, reEval, reEvalRun };
}

function skillResourceOptionFromProjectResource(
  resource: ProjectResource,
  projectTitle: string,
): SkillResourceOption | null {
  const ref = isRecord(resource.resource_ref) ? resource.resource_ref : {};
  const resourceLabel = typeof resource.label === "string" && resource.label.trim() ? resource.label.trim() : "";

  if (resource.resource_type === "gongfeng_repo") {
    const repo = stringFromUnknown(ref["project_path"]) || stringFromUnknown(ref["url"]);
    if (!repo) return null;
    return {
      id: resource.id,
      projectTitle,
      title: resourceLabel || stringFromUnknown(ref["title"]) || repo,
      kind: "gongfeng",
      repo,
      repoPath: "",
      branch: stringFromUnknown(ref["ref"]),
      requiresRepoPath: true,
    };
  }

  const repoPath = stringFromUnknown(ref["local_path"]);
  if (!repoPath) return null;
  return {
    id: resource.id,
    projectTitle,
    title: resourceLabel || stringFromUnknown(ref["label"]) || repoPath,
    kind: "local",
    repo: repoPath,
    repoPath,
    branch: "HEAD",
    requiresRepoPath: false,
  };
}

function firstRecord(...values: Record<string, unknown>[]): Record<string, unknown> {
  return values.find((value) => Object.keys(value).length > 0) ?? {};
}

function hasSkillSnapshotShape(value: Record<string, unknown>): boolean {
  return Boolean(value["base_commit"] || value["skill_hash"] || value["skill_path"]);
}
