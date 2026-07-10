import { Loader2, Play } from "lucide-react";
import type { ReactNode } from "react";
import type { PromptEvaluationOptimizationCandidate } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n/use-t";
import { asRecord, shortId, stringFromUnknown } from "./record-utils";
import type {
  SkillCandidateWorkflowAction,
  SkillCandidateWorkflowDraft,
  SkillCandidateWorkflowEvidence,
  SkillResourceOption,
} from "./skill-candidate-model";

export function SkillCandidateWorkflowPanel({
  candidate,
  draft,
  evidence,
  resources,
  pendingAction,
  disabled,
  onDraftChange,
  onRunAction,
}: {
  candidate: PromptEvaluationOptimizationCandidate;
  draft: SkillCandidateWorkflowDraft;
  evidence: SkillCandidateWorkflowEvidence;
  resources: SkillResourceOption[];
  pendingAction: SkillCandidateWorkflowAction | null;
  disabled: boolean;
  onDraftChange: (draft: SkillCandidateWorkflowDraft) => void;
  onRunAction: (action: SkillCandidateWorkflowAction) => void;
}) {
  const { t } = useT("prompt-library");
  const snapshotHash = stringFromUnknown(evidence.snapshot["skill_hash"]);
  const freshnessStatus = stringFromUnknown(evidence.freshness["status"]) || t(($) => $.skill_workflow.not_checked);
  const applyStatus = stringFromUnknown(evidence.apply["status"]) || t(($) => $.skill_workflow.not_applied);
  const reEvalAssetId = draft.reEvalAssetId || stringFromUnknown(evidence.reEval["asset_id"]);
  const reEvalRunId = stringFromUnknown(evidence.reEvalRun["run_id"]);
  const canRunReEval = !disabled && Boolean(reEvalAssetId);
  const selectedResource = resources.find((resource) => resource.id === draft.sourceResourceId) ?? null;
  const skillPatch = asRecord(candidate.skill_patch);
  const patchText = stringFromUnknown(skillPatch["patch"]);
  const expectedImprovement = stringFromUnknown(skillPatch["expected_improvement"]);
  const risk = stringFromUnknown(skillPatch["risk"]);
  const verificationPlan = stringFromUnknown(skillPatch["verification_plan"]);
  const patchHash = stringFromUnknown(skillPatch["patch_hash"]);
  const publicationStatus = stringFromUnknown(skillPatch["publication_status"]);
  const candidateIntent = stringFromUnknown(skillPatch["candidate_intent"]) || "update_existing_skill";
  const operationSkillKey = stringFromUnknown(skillPatch["operation_skill_key"]);
  const operationSkillPath = stringFromUnknown(skillPatch["operation_skill_path"]);
  const operationSkillReason = stringFromUnknown(skillPatch["operation_skill_reason"]);
  const missing = t(($) => $.skill_workflow.missing);
  const draftLabel = t(($) => $.skill_workflow.draft);
  const notRecorded = t(($) => $.skill_workflow.not_recorded);

  const resourceProjectTitle = (resource: SkillResourceOption) =>
    resource.projectTitle || t(($) => $.skill_workflow.unnamed_project);
  const resourceLabel = (resource: SkillResourceOption) =>
    resource.kind === "gongfeng"
      ? t(($) => $.skill_workflow.gongfeng_resource, { project: resourceProjectTitle(resource), title: resource.title })
      : t(($) => $.skill_workflow.local_resource, { project: resourceProjectTitle(resource), title: resource.title });
  const resourceDetail = selectedResource
    ? selectedResource.kind === "gongfeng"
      ? selectedResource.branch
        ? t(($) => $.skill_workflow.gongfeng_detail_branch, {
            project: resourceProjectTitle(selectedResource),
            repo: selectedResource.repo,
            branch: selectedResource.branch,
          })
        : t(($) => $.skill_workflow.gongfeng_detail, {
            project: resourceProjectTitle(selectedResource),
            repo: selectedResource.repo,
          })
      : t(($) => $.skill_workflow.local_detail, {
          project: resourceProjectTitle(selectedResource),
          path: selectedResource.repoPath,
        })
    : null;

  return (
    <section
      className="mt-3 grid gap-2 rounded-sm border border-border/70 bg-muted/10 px-2 py-2 text-xs"
      data-testid={`skill-candidate-workflow-${candidate.id}`}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="font-medium">{t(($) => $.skill_workflow.title)}</div>
        <div className="flex flex-wrap gap-1">
          <Badge variant={snapshotHash ? "secondary" : "outline"}>
            {t(($) => $.skill_workflow.snapshot, { id: shortId(snapshotHash) || missing })}
          </Badge>
          <Badge variant={candidateIntent === "create_operation_skill" ? "secondary" : "outline"}>
            {candidateIntent === "create_operation_skill"
              ? t(($) => $.skill_workflow.create_operation_skill)
              : t(($) => $.skill_workflow.update_skill)}
          </Badge>
          <Badge variant={freshnessStatus === "conflict" || freshnessStatus === "stale" ? "destructive" : "outline"}>
            {freshnessStatus}
          </Badge>
          <Badge
            variant={
              applyStatus === "applied"
                ? "secondary"
                : applyStatus === "conflict" || applyStatus === "blocked"
                  ? "destructive"
                  : "outline"
            }
          >
            {applyStatus}
          </Badge>
          {reEvalRunId && (
            <Badge variant="secondary">{t(($) => $.skill_workflow.re_eval_run, { id: shortId(reEvalRunId) })}</Badge>
          )}
        </div>
      </div>
      <label className="grid gap-1">
        <span className="text-muted-foreground">{t(($) => $.skill_workflow.project_resource)}</span>
        <select
          aria-label={t(($) => $.skill_workflow.project_resource)}
          value={draft.sourceResourceId}
          onChange={(event) => {
            const resourceId = event.target.value;
            const nextResource = resources.find((resource) => resource.id === resourceId) ?? null;
            onDraftChange({
              ...draft,
              sourceResourceId: resourceId,
              repoPath: nextResource?.repoPath || draft.repoPath,
              targetBranch: nextResource?.branch || draft.targetBranch,
            });
          }}
          className="h-8 rounded-sm border border-input bg-background px-2 text-xs"
        >
          <option value="">{t(($) => $.skill_workflow.manual_checkout)}</option>
          {resources.map((resource) => (
            <option key={resource.id} value={resource.id}>
              {resourceLabel(resource)}
            </option>
          ))}
        </select>
        <span className="text-[11px] text-muted-foreground">
          {resourceDetail
            ? `${resourceDetail}${selectedResource?.requiresRepoPath ? t(($) => $.skill_workflow.checkout_required_suffix) : ""}`
            : t(($) => $.skill_workflow.resource_help)}
        </span>
      </label>
      <div className="grid gap-2 md:grid-cols-3">
        <label className="grid gap-1">
          <span className="text-muted-foreground">{t(($) => $.skill_workflow.local_checkout)}</span>
          <Input
            value={draft.repoPath}
            onChange={(event) => onDraftChange({ ...draft, repoPath: event.target.value })}
            placeholder="/data/ida/goal-test"
            className="h-8 text-xs"
          />
        </label>
        <label className="grid gap-1">
          <span className="text-muted-foreground">{t(($) => $.skill_workflow.target_branch)}</span>
          <Input
            value={draft.targetBranch}
            onChange={(event) => onDraftChange({ ...draft, targetBranch: event.target.value })}
            placeholder="v5.0.0_dev_sop"
            className="h-8 text-xs"
          />
        </label>
        <label className="grid gap-1">
          <span className="text-muted-foreground">{t(($) => $.skill_workflow.skill_path)}</span>
          <Input
            value={draft.skillPath}
            onChange={(event) => onDraftChange({ ...draft, skillPath: event.target.value })}
            placeholder=".codebuddy/skills/05-verify/SKILL.md"
            className="h-8 text-xs"
          />
        </label>
      </div>
      <div className="grid gap-2 md:grid-cols-2">
        <label className="grid gap-1">
          <span className="text-muted-foreground">{t(($) => $.skill_workflow.changelog_path)}</span>
          <Input
            value={draft.changelogPath}
            onChange={(event) => onDraftChange({ ...draft, changelogPath: event.target.value })}
            placeholder={t(($) => $.skill_workflow.changelog_placeholder)}
            className="h-8 text-xs"
          />
        </label>
        <label className="grid gap-1">
          <span className="text-muted-foreground">{t(($) => $.skill_workflow.re_eval_asset)}</span>
          <Input
            value={draft.reEvalAssetId}
            onChange={(event) => onDraftChange({ ...draft, reEvalAssetId: event.target.value })}
            placeholder={stringFromUnknown(evidence.reEval["asset_id"]) || t(($) => $.skill_workflow.re_eval_placeholder)}
            className="h-8 text-xs"
          />
        </label>
      </div>
      <div className="flex flex-wrap gap-3 text-[11px] text-muted-foreground">
        <label className="inline-flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={draft.includeDraft}
            onChange={(event) => onDraftChange({ ...draft, includeDraft: event.target.checked })}
          />
          {t(($) => $.skill_workflow.include_draft)}
        </label>
        <label className="inline-flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={draft.allowDirty}
            onChange={(event) => onDraftChange({ ...draft, allowDirty: event.target.checked })}
          />
          {t(($) => $.skill_workflow.allow_dirty)}
        </label>
        <label className="inline-flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={draft.skipChangelog}
            onChange={(event) => onDraftChange({ ...draft, skipChangelog: event.target.checked })}
          />
          {t(($) => $.skill_workflow.skip_changelog)}
        </label>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <SkillWorkflowButton action="freshness" pendingAction={pendingAction} disabled={disabled} onRunAction={onRunAction}>
          {t(($) => $.skill_workflow.action_freshness)}
        </SkillWorkflowButton>
        <SkillWorkflowButton action="apply" pendingAction={pendingAction} disabled={disabled} onRunAction={onRunAction}>
          {t(($) => $.skill_workflow.action_apply)}
        </SkillWorkflowButton>
        <SkillWorkflowButton
          action="prepare-re-eval"
          pendingAction={pendingAction}
          disabled={disabled}
          onRunAction={onRunAction}
        >
          {t(($) => $.skill_workflow.action_prepare_re_eval)}
        </SkillWorkflowButton>
        <SkillWorkflowButton
          action="run-re-eval"
          pendingAction={pendingAction}
          disabled={!canRunReEval}
          onRunAction={onRunAction}
        >
          {t(($) => $.skill_workflow.action_run_re_eval)}
        </SkillWorkflowButton>
      </div>
      <div className="grid gap-1 break-all text-[11px] text-muted-foreground md:grid-cols-2">
        <div>
          {t(($) => $.skill_workflow.base_path, {
            base: shortId(stringFromUnknown(evidence.snapshot["base_commit"])) || missing,
            path: draft.skillPath || stringFromUnknown(evidence.snapshot["skill_path"]) || missing,
          })}
        </div>
        <div>
          {t(($) => $.skill_workflow.re_eval_summary, {
            asset: shortId(reEvalAssetId) || missing,
            run: shortId(reEvalRunId) || t(($) => $.skill_workflow.not_run),
          })}
        </div>
      </div>
      {(patchText ||
        expectedImprovement ||
        risk ||
        verificationPlan ||
        patchHash ||
        publicationStatus ||
        operationSkillKey ||
        operationSkillPath ||
        operationSkillReason) && (
        <div
          className="grid gap-1 rounded border bg-background px-2 py-2 text-[11px] leading-5"
          data-testid={`skill-candidate-diff-risk-${candidate.id}`}
        >
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge variant={patchHash ? "secondary" : "outline"}>
              {t(($) => $.skill_workflow.patch, { id: shortId(patchHash) || draftLabel })}
            </Badge>
            <Badge variant="outline">
              {t(($) => $.skill_workflow.publication, { status: publicationStatus || draftLabel })}
            </Badge>
            <Badge variant={candidateIntent === "create_operation_skill" ? "secondary" : "outline"}>
              {candidateIntent === "create_operation_skill" ? "create_operation_skill" : "update_existing_skill"}
            </Badge>
          </div>
          {(operationSkillKey || operationSkillPath || operationSkillReason) && (
            <div className="grid gap-1 rounded bg-muted/20 px-2 py-1.5">
              <div className="font-medium text-foreground">{t(($) => $.skill_workflow.operation_candidate)}</div>
              <div className="text-muted-foreground">
                {t(($) => $.skill_workflow.operation_location, {
                  key: operationSkillKey || notRecorded,
                  path: operationSkillPath || draft.skillPath || notRecorded,
                })}
              </div>
              {operationSkillReason && <div className="text-muted-foreground">{operationSkillReason}</div>}
            </div>
          )}
          <div className="grid gap-1 md:grid-cols-3">
            <CandidateFact label={t(($) => $.skill_workflow.expected_improvement)} value={expectedImprovement || notRecorded} />
            <CandidateFact label={t(($) => $.skill_workflow.risk)} value={risk || notRecorded} />
            <CandidateFact label={t(($) => $.skill_workflow.verification_plan)} value={verificationPlan || notRecorded} />
          </div>
          {patchText && (
            <pre className="max-h-36 overflow-auto whitespace-pre-wrap rounded bg-muted/30 px-2 py-1 font-mono text-[11px] leading-5 text-foreground">
              {patchText}
            </pre>
          )}
        </div>
      )}
    </section>
  );
}

function SkillWorkflowButton({
  action,
  pendingAction,
  disabled,
  children,
  onRunAction,
}: {
  action: SkillCandidateWorkflowAction;
  pendingAction: SkillCandidateWorkflowAction | null;
  disabled: boolean;
  children: ReactNode;
  onRunAction: (action: SkillCandidateWorkflowAction) => void;
}) {
  const pending = pendingAction === action;
  return (
    <Button
      size="sm"
      variant="secondary"
      className="h-7 text-xs"
      onClick={() => onRunAction(action)}
      disabled={disabled || pendingAction !== null}
    >
      {pending ? <Loader2 className="size-3.5 animate-spin" /> : <Play className="size-3.5" />}
      {children}
    </Button>
  );
}

function CandidateFact({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <div className="font-medium text-foreground">{label}</div>
      <div className="mt-1 text-muted-foreground">{value}</div>
    </div>
  );
}
