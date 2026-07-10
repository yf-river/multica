"use client";

import { useMemo, type Dispatch, type SetStateAction } from "react";
import { Archive, Download, Loader2, Save, Trash2 } from "lucide-react";
import type {
  CreatePromptEvaluationCaseRequest,
  PromptEvaluationAsset,
  PromptEvaluationRun,
  PromptEvaluationStructuredCase,
  UpdatePromptEvaluationCaseRequest,
} from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { useWorkspacePaths } from "@multica/core/paths";
import { summarizeSkillScenarioTarget, type TrainingWorkbenchTab } from "@multica/core/training";
import { useT } from "../../i18n/use-t";
import { shortId } from "./record-utils";
import {
  buildCaseSummaries,
  buildCasesByAsset,
  buildManualCaseRequest,
  emptyManualCaseDraft,
  type ManualCaseDraft,
} from "./case-model";
import {
  ManualCasePanel,
  type ManualCasePanelCopy,
} from "./manual-case-panel";
import {
  canManageStructuredCases,
  emptyTrainingRouteText,
  summarizeAgentRun,
  summarizeAssetPayload,
  summarizeDatasetVersion,
  summarizeLinkedDatasetVersions,
} from "./training-workbench-support";

type WorkbenchTab = TrainingWorkbenchTab;

export type TrainingAssetPanelBaseProps = {
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

export type TrainingAssetPanelProps = TrainingAssetPanelBaseProps & {
  activeTab: WorkbenchTab;
  route: string;
  title: string;
};

export function TrainingAssetPanel({
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
