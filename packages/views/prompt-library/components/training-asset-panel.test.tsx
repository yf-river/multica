import { useState } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { PromptEvaluationAsset } from "@multica/core/types";
import { TrainingAssetPanel } from "./training-asset-panel";
import type { ManualCaseDraft } from "./case-model";
import { promptLibraryTestTranslation } from "./test/translation";

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ runReviews: () => "/workspaces/acme/run-reviews" }),
}));

vi.mock("../../i18n/use-t", () => ({
  useT: () => ({
    t: promptLibraryTestTranslation,
  }),
}));

const asset: PromptEvaluationAsset = {
  id: "asset-1",
  workspace_id: "workspace-1",
  prompt_id: null,
  name: "登录失败回归集",
  description: "登录失败场景",
  asset_type: "数据集",
  payload: {},
  status: "启用",
  updated_at: "2026-07-10T00:00:00Z",
  dataset_row_count: 0,
  test_suite_case_count: 0,
};

const originalDraft: ManualCaseDraft = {
  caseName: "并发登录失败",
  variablesText: "请求在认证阶段超时",
  expectedText: "说明超时根因",
  tagsText: "认证,并发",
};

function StatefulPanel({
  onCreateCase,
}: Pick<Parameters<typeof TrainingAssetPanel>[0], "onCreateCase">) {
  const [caseDrafts, setCaseDrafts] = useState<Record<string, ManualCaseDraft>>({
    [asset.id]: originalDraft,
  });
  return (
    <TrainingAssetPanel
      activeTab="用例库"
      route="datasets"
      title="用例库"
      assets={[asset]}
      cases={[]}
      runs={[]}
      focusedIssueId={null}
      focusedCaseId={null}
      focusedIssueRunReviewHref={null}
      loading={false}
      saving={false}
      onToggleAssetStatus={vi.fn()}
      onDeleteAsset={vi.fn()}
      onImportDatasetFromTraces={vi.fn()}
      importingTraceDatasetAssetId={null}
      onCreateDatasetVersion={vi.fn().mockResolvedValue(undefined)}
      creatingDatasetVersionAssetId={null}
      onCreateCase={onCreateCase}
      creatingCaseAssetId={null}
      caseDrafts={caseDrafts}
      onCaseDraftsChange={setCaseDrafts}
      onUpdateCase={vi.fn().mockResolvedValue(undefined)}
      updatingCaseId={null}
      onDeleteCase={vi.fn()}
      deletingCaseId={null}
      onCreateAssetEvidenceSnapshots={vi.fn()}
      creatingAssetEvidenceSnapshotsAssetId={null}
      onDownloadAssetEvidencePackage={vi.fn()}
      exportingAssetEvidencePackageAssetId={null}
    />
  );
}

describe("training asset panel case drafts", () => {
  it("keeps a manual case draft when creation fails", async () => {
    const onCreateCase = vi.fn().mockRejectedValue(new Error("save failed"));
    render(<StatefulPanel onCreateCase={onCreateCase} />);

    fireEvent.click(screen.getByRole("button", { name: "manual_case.add_case" }));

    await waitFor(() => expect(onCreateCase).toHaveBeenCalledTimes(1));
    expect(screen.getByDisplayValue(originalDraft.caseName)).toBeInTheDocument();
    expect(screen.getByDisplayValue(originalDraft.variablesText)).toBeInTheDocument();
    expect(screen.getByDisplayValue(originalDraft.expectedText)).toBeInTheDocument();
    expect(screen.getByDisplayValue(originalDraft.tagsText)).toBeInTheDocument();
  });

  it("clears a manual case draft only after creation succeeds", async () => {
    const onCreateCase = vi.fn().mockResolvedValue(undefined);
    render(<StatefulPanel onCreateCase={onCreateCase} />);

    fireEvent.click(screen.getByRole("button", { name: "manual_case.add_case" }));

    await waitFor(() => expect(screen.getByDisplayValue("")).toBeInTheDocument());
    expect(onCreateCase).toHaveBeenCalledTimes(1);
    expect(screen.queryByDisplayValue(originalDraft.caseName)).not.toBeInTheDocument();
  });
});
