"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2, Plus, Save, Search, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import type {
  PromptEvaluationAsset,
  PromptEvaluationStructuredCase,
  UpdatePromptEvaluationAssetRequest,
  UpdatePromptEvaluationCaseRequest,
} from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import {
  buildCaseLibraryUpdateRequest,
  buildCaseSearchText,
  buildCasesByAsset,
  caseLibraryExpectedText,
  caseLibraryInputText,
  caseSourceKind,
  datasetVersionSummary,
  manualCaseToDraft,
  uniqueSortedStrings,
  type CaseSourceKind,
  type DatasetVersionSummary,
  type ManualCaseDraft,
} from "./case-model";
import { Field } from "./form-field";
import { promptLibraryKeys } from "./prompt-library-query-keys";

const ALL_TAGS = "__all_tags__";

export interface CaseDraftEditorCopy {
  nameLabel: string;
  namePlaceholder: string;
  tagsLabel: string;
  tagsPlaceholder: string;
  inputLabel: string;
  inputPlaceholder: string;
  expectedLabel: string;
  expectedPlaceholder: string;
  cancel: string;
}

export interface DatasetVersionHistoryCopy {
  title: string;
  loading: string;
  count: (count: number) => string;
  noSnapshots: string;
  emptyDescription: string;
  unnamedVersion: string;
  version: (version: number) => string;
  latest: string;
  rowFingerprint: (rowCount: number, fingerprint: string) => string;
  missingFingerprint: string;
  missingTime: string;
}

export interface CaseLibraryEditorCopy {
  title: string;
  loading: string;
  count: (datasetCount: number, caseCount: number) => string;
  createDataset: string;
  searchPlaceholder: string;
  searchAriaLabel: string;
  datasetNamePlaceholder: string;
  datasetDescriptionPlaceholder: string;
  cancel: string;
  save: string;
  missingDatasetNameError: string;
  missingCaseNameError: string;
  missingCaseInputError: string;
  noDatasets: string;
  noDatasetSearchResults: string;
  noDescription: string;
  updatedAt: (value: string) => string;
  missingTime: string;
  emptyTitle: string;
  emptyDescription: string;
  saveDataset: string;
  createVersion: string;
  edit: string;
  delete: string;
  addCase: string;
  versionLabel: string;
  versionPlaceholder: string;
  defaultVersionLabel: string;
  saveVersion: string;
  tagFilterAriaLabel: string;
  allTags: string;
  matchCount: (visible: number, total: number) => string;
  newCaseTitle: string;
  editCaseTitle: string;
  saveCase: string;
  caseCount: (count: number) => string;
  caseName: (index: number) => string;
  sourceLabel: (source: CaseSourceKind) => string;
  inputPrefix: string;
  expectedPrefix: string;
  missingInput: string;
  missingExpected: string;
  noTags: string;
  noCases: string;
  noCaseFilterResults: string;
  datasetVersionSummary: (summary: DatasetVersionSummary) => string;
  draft: CaseDraftEditorCopy;
  versionHistory: DatasetVersionHistoryCopy;
}

export interface CaseLibraryEditorPanelProps {
  assets: PromptEvaluationAsset[];
  cases: PromptEvaluationStructuredCase[];
  loading: boolean;
  saving: boolean;
  draft: ManualCaseDraft;
  onDraftChange: (draft: ManualCaseDraft) => void;
  onCreateDataset: (name: string, description: string) => void;
  creatingDataset: boolean;
  onUpdateDataset: (asset: PromptEvaluationAsset, data: UpdatePromptEvaluationAssetRequest) => void;
  updatingDatasetId: string | null;
  onDeleteDataset: (asset: PromptEvaluationAsset) => void;
  deletingDatasetId: string | null;
  onCreateDatasetVersion: (asset: PromptEvaluationAsset, versionLabel?: string) => void;
  creatingDatasetVersionAssetId: string | null;
  onCreateCase: (asset: PromptEvaluationAsset, draft: ManualCaseDraft) => Promise<unknown>;
  creating: boolean;
  focusedCaseId: string | null;
  onUpdateCase: (caseId: string, data: UpdatePromptEvaluationCaseRequest) => Promise<unknown>;
  updatingCaseId: string | null;
  onDeleteCase: (caseId: string) => void;
  deletingCaseId: string | null;
  copy: CaseLibraryEditorCopy;
}

export function CaseLibraryEditorPanel({
  assets,
  cases,
  loading,
  saving,
  draft,
  onDraftChange,
  onCreateDataset,
  creatingDataset,
  onUpdateDataset,
  updatingDatasetId,
  onDeleteDataset,
  deletingDatasetId,
  onCreateDatasetVersion,
  creatingDatasetVersionAssetId,
  onCreateCase,
  creating,
  focusedCaseId,
  onUpdateCase,
  updatingCaseId,
  onDeleteCase,
  deletingCaseId,
  copy,
}: CaseLibraryEditorPanelProps) {
  const [keywordFilter, setKeywordFilter] = useState("");
  const [tagFilter, setTagFilter] = useState(ALL_TAGS);
  const [showCreateForm, setShowCreateForm] = useState(false);
  const [showDatasetForm, setShowDatasetForm] = useState(false);
  const [datasetDraft, setDatasetDraft] = useState({ name: "", description: "" });
  const [editingDataset, setEditingDataset] = useState(false);
  const [datasetEditDraft, setDatasetEditDraft] = useState({ name: "", description: "" });
  const [showVersionForm, setShowVersionForm] = useState(false);
  const [versionLabelDraft, setVersionLabelDraft] = useState("");
  const [selectedAssetId, setSelectedAssetId] = useState<string | null>(null);
  const [editingCaseId, setEditingCaseId] = useState<string | null>(null);
  const [editDrafts, setEditDrafts] = useState<Record<string, ManualCaseDraft>>({});

  const datasetAssets = useMemo(
    () => assets.filter((asset) => asset.asset_type === "数据集"),
    [assets],
  );
  const casesByAsset = useMemo(() => buildCasesByAsset(cases), [cases]);
  const selectedAsset = useMemo(
    () => datasetAssets.find((asset) => asset.id === selectedAssetId) ?? datasetAssets[0] ?? null,
    [datasetAssets, selectedAssetId],
  );
  const selectedCases = useMemo(
    () => (selectedAsset ? casesByAsset.get(selectedAsset.id) ?? [] : cases),
    [cases, casesByAsset, selectedAsset],
  );
  const caseTags = useMemo(
    () => uniqueSortedStrings(selectedCases.flatMap((item) => item.tags.map(String))),
    [selectedCases],
  );
  const filteredCases = useMemo(() => {
    const keyword = keywordFilter.trim().toLowerCase();
    return selectedCases
      .filter((item) => {
        const tagMatches = tagFilter === ALL_TAGS || item.tags.some((value) => String(value) === tagFilter);
        const searchText = buildCaseSearchText(item, copy.sourceLabel(caseSourceKind(item.source)));
        return tagMatches && (!keyword || searchText.includes(keyword));
      })
      .toSorted(
        (a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at) || a.case_index - b.case_index,
      );
  }, [copy, keywordFilter, selectedCases, tagFilter]);
  const datasetFilter = keywordFilter.trim().toLowerCase();
  const filteredAssets = useMemo(() => {
    if (!datasetFilter) return datasetAssets;
    return datasetAssets.filter((asset) => {
      const summary = datasetVersionSummary(asset);
      const text = [
        asset.name,
        asset.description,
        summary ? copy.datasetVersionSummary(summary) : "",
      ]
        .join(" ")
        .toLowerCase();
      return text.includes(datasetFilter) || matchesPinyin(text, datasetFilter);
    });
  }, [copy, datasetAssets, datasetFilter]);

  useEffect(() => {
    if (selectedAssetId && datasetAssets.some((asset) => asset.id === selectedAssetId)) return;
    setSelectedAssetId(datasetAssets[0]?.id ?? null);
  }, [datasetAssets, selectedAssetId]);

  useEffect(() => {
    setTagFilter(ALL_TAGS);
    setEditingDataset(false);
    setShowVersionForm(false);
    setVersionLabelDraft("");
    setShowCreateForm(false);
    setEditingCaseId(null);
  }, [selectedAssetId]);

  useEffect(() => {
    setDatasetEditDraft(
      selectedAsset
        ? { name: selectedAsset.name, description: selectedAsset.description }
        : { name: "", description: "" },
    );
  }, [selectedAsset]);

  const submitDataset = () => {
    const name = datasetDraft.name.trim();
    if (!name) {
      toast.error(copy.missingDatasetNameError);
      return;
    }
    onCreateDataset(name, datasetDraft.description.trim());
    setDatasetDraft({ name: "", description: "" });
    setShowDatasetForm(false);
  };

  const submitDatasetEdit = () => {
    if (!selectedAsset) return;
    const name = datasetEditDraft.name.trim();
    if (!name) {
      toast.error(copy.missingDatasetNameError);
      return;
    }
    onUpdateDataset(selectedAsset, {
      name,
      description: datasetEditDraft.description.trim(),
      asset_type: "数据集",
      prompt_id: selectedAsset.prompt_id,
      payload: selectedAsset.payload,
      status: selectedAsset.status,
    });
    setEditingDataset(false);
  };

  const submitDatasetVersion = () => {
    if (!selectedAsset) return;
    onCreateDatasetVersion(selectedAsset, versionLabelDraft.trim() || copy.defaultVersionLabel);
    setShowVersionForm(false);
    setVersionLabelDraft("");
  };

  const submitCase = async (asset: PromptEvaluationAsset, caseDraft: ManualCaseDraft) => {
    if (!caseDraft.caseName.trim()) {
      toast.error(copy.missingCaseNameError);
      return;
    }
    if (!caseDraft.variablesText.trim()) {
      toast.error(copy.missingCaseInputError);
      return;
    }
    await onCreateCase(asset, caseDraft);
    setShowCreateForm(false);
  };

  return (
    <section
      className="grid min-h-[620px] gap-0 overflow-hidden rounded-md border md:grid-cols-[320px_minmax(0,1fr)]"
      data-testid="case-library-editor"
    >
      <aside className="flex min-h-0 flex-col border-b md:border-b-0 md:border-r">
        <div className="grid gap-3 border-b p-3">
          <div className="flex items-center justify-between gap-2">
            <div className="min-w-0">
              <h2 className="text-base font-semibold">{copy.title}</h2>
              <div className="mt-1 text-xs text-muted-foreground">
                {loading ? copy.loading : copy.count(datasetAssets.length, cases.length)}
              </div>
            </div>
            <Button
              size="sm"
              onClick={() => setShowDatasetForm((value) => !value)}
              disabled={saving || creatingDataset}
            >
              <Plus className="size-3.5" />
              {copy.createDataset}
            </Button>
          </div>
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={keywordFilter}
              onChange={(event) => setKeywordFilter(event.target.value)}
              placeholder={copy.searchPlaceholder}
              aria-label={copy.searchAriaLabel}
              className="h-8 pl-8 text-sm"
            />
          </div>
          {showDatasetForm && (
            <div className="grid gap-2 rounded-md border bg-muted/10 p-2">
              <Input
                value={datasetDraft.name}
                onChange={(event) =>
                  setDatasetDraft((current) => ({ ...current, name: event.target.value }))
                }
                placeholder={copy.datasetNamePlaceholder}
                className="h-8 text-sm"
              />
              <Input
                value={datasetDraft.description}
                onChange={(event) =>
                  setDatasetDraft((current) => ({ ...current, description: event.target.value }))
                }
                placeholder={copy.datasetDescriptionPlaceholder}
                className="h-8 text-sm"
              />
              <div className="flex justify-end gap-2">
                <Button size="sm" variant="ghost" onClick={() => setShowDatasetForm(false)}>
                  {copy.cancel}
                </Button>
                <Button size="sm" onClick={submitDataset} disabled={creatingDataset || !datasetDraft.name.trim()}>
                  {creatingDataset ? (
                    <Loader2 className="size-3.5 animate-spin" />
                  ) : (
                    <Save className="size-3.5" />
                  )}
                  {copy.save}
                </Button>
              </div>
            </div>
          )}
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {loading ? (
            <div className="space-y-2 p-3">
              {Array.from({ length: 4 }).map((_, index) => (
                <div key={index} className="h-16 rounded-md bg-muted/60" />
              ))}
            </div>
          ) : filteredAssets.length === 0 ? (
            <div className="p-6 text-sm text-muted-foreground">
              {datasetAssets.length === 0 ? copy.noDatasets : copy.noDatasetSearchResults}
            </div>
          ) : (
            <div className="divide-y" data-testid="case-library-dataset-list">
              {filteredAssets.map((asset) => {
                const assetCases = casesByAsset.get(asset.id) ?? [];
                const selected = selectedAsset?.id === asset.id;
                const versionSummary = datasetVersionSummary(asset);
                return (
                  <button
                    key={asset.id}
                    type="button"
                    onClick={() => {
                      setSelectedAssetId(asset.id);
                      setShowCreateForm(false);
                      setEditingCaseId(null);
                    }}
                    className={`flex w-full flex-col gap-1 px-3 py-3 text-left transition-colors hover:bg-muted/60 ${selected ? "bg-muted" : ""}`}
                    data-testid={`case-library-dataset-${asset.id}`}
                  >
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="min-w-0 flex-1 truncate text-sm font-medium">{asset.name}</span>
                      <Badge variant="outline" className="shrink-0 text-[10px]">
                        {assetCases.length}
                      </Badge>
                    </div>
                    <div className="truncate text-xs text-muted-foreground">
                      {asset.description || copy.noDescription}
                    </div>
                    <div className="truncate text-[11px] text-muted-foreground">
                      {versionSummary
                        ? copy.datasetVersionSummary(versionSummary)
                        : copy.updatedAt(asset.updated_at || copy.missingTime)}
                    </div>
                  </button>
                );
              })}
            </div>
          )}
        </div>
      </aside>

      <main className="min-h-0 overflow-y-auto p-4">
        {!selectedAsset && !loading ? (
          <div
            className="grid min-h-[360px] place-items-center rounded-md border border-dashed px-4 py-10 text-center text-sm text-muted-foreground"
            data-testid="case-library-empty"
          >
            <div>
              <div className="font-medium text-foreground">{copy.emptyTitle}</div>
              <div className="mt-1">{copy.emptyDescription}</div>
            </div>
          </div>
        ) : selectedAsset ? (
          <div className="grid gap-4">
            <div className="flex flex-col gap-3 border-b pb-4 md:flex-row md:items-start md:justify-between">
              <div className="min-w-0 flex-1">
                {editingDataset ? (
                  <div className="grid gap-2">
                    <div className="grid gap-2 md:grid-cols-2">
                      <Input
                        value={datasetEditDraft.name}
                        onChange={(event) =>
                          setDatasetEditDraft((current) => ({ ...current, name: event.target.value }))
                        }
                        placeholder={copy.datasetNamePlaceholder}
                      />
                      <Input
                        value={datasetEditDraft.description}
                        onChange={(event) =>
                          setDatasetEditDraft((current) => ({
                            ...current,
                            description: event.target.value,
                          }))
                        }
                        placeholder={copy.datasetDescriptionPlaceholder}
                      />
                    </div>
                    <div className="flex flex-wrap gap-2">
                      <Button
                        size="sm"
                        onClick={submitDatasetEdit}
                        disabled={
                          saving ||
                          updatingDatasetId === selectedAsset.id ||
                          !datasetEditDraft.name.trim()
                        }
                      >
                        {updatingDatasetId === selectedAsset.id ? (
                          <Loader2 className="size-3.5 animate-spin" />
                        ) : (
                          <Save className="size-3.5" />
                        )}
                        {copy.saveDataset}
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => setEditingDataset(false)}>
                        {copy.cancel}
                      </Button>
                    </div>
                  </div>
                ) : (
                  <>
                    <h2 className="truncate text-base font-semibold">{selectedAsset.name}</h2>
                    <div className="mt-1 text-sm text-muted-foreground">
                      {selectedAsset.description || copy.noDescription}
                    </div>
                    <div className="mt-2 flex flex-wrap gap-2 text-xs text-muted-foreground">
                      <Badge variant="outline">{copy.caseCount(selectedCases.length)}</Badge>
                      <span>{copy.updatedAt(selectedAsset.updated_at || copy.missingTime)}</span>
                    </div>
                  </>
                )}
              </div>
              <div className="flex shrink-0 flex-wrap gap-2">
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => setShowVersionForm((value) => !value)}
                  disabled={
                    saving ||
                    creatingDatasetVersionAssetId === selectedAsset.id ||
                    selectedCases.length === 0
                  }
                >
                  {creatingDatasetVersionAssetId === selectedAsset.id ? (
                    <Loader2 className="size-3.5 animate-spin" />
                  ) : (
                    <Save className="size-3.5" />
                  )}
                  {copy.createVersion}
                </Button>
                <Button
                  size="sm"
                  variant="secondary"
                  data-testid={`edit-case-library-dataset-${selectedAsset.id}`}
                  onClick={() => setEditingDataset(true)}
                  disabled={saving || editingDataset}
                >
                  {copy.edit}
                </Button>
                <Button
                  size="sm"
                  variant="destructive"
                  data-testid={`delete-case-library-dataset-${selectedAsset.id}`}
                  onClick={() => onDeleteDataset(selectedAsset)}
                  disabled={saving || deletingDatasetId === selectedAsset.id}
                >
                  {deletingDatasetId === selectedAsset.id ? (
                    <Loader2 className="size-3.5 animate-spin" />
                  ) : (
                    <Trash2 className="size-3.5" />
                  )}
                  {copy.delete}
                </Button>
                <Button
                  size="sm"
                  onClick={() => setShowCreateForm((value) => !value)}
                  disabled={saving}
                >
                  <Plus className="size-3.5" />
                  {copy.addCase}
                </Button>
              </div>
            </div>

            {showVersionForm && (
              <div className="grid gap-2 rounded-md border bg-muted/10 p-3">
                <Field label={copy.versionLabel}>
                  <Input
                    value={versionLabelDraft}
                    onChange={(event) => setVersionLabelDraft(event.target.value)}
                    placeholder={copy.versionPlaceholder}
                  />
                </Field>
                <div className="flex justify-end gap-2">
                  <Button size="sm" variant="ghost" onClick={() => setShowVersionForm(false)}>
                    {copy.cancel}
                  </Button>
                  <Button
                    size="sm"
                    onClick={submitDatasetVersion}
                    disabled={creatingDatasetVersionAssetId === selectedAsset.id}
                  >
                    {creatingDatasetVersionAssetId === selectedAsset.id ? (
                      <Loader2 className="size-3.5 animate-spin" />
                    ) : (
                      <Save className="size-3.5" />
                    )}
                    {copy.saveVersion}
                  </Button>
                </div>
              </div>
            )}

            <DatasetVersionHistoryPanel asset={selectedAsset} copy={copy.versionHistory} />

            <div className="flex flex-col gap-2 md:flex-row md:items-center" data-testid="case-library-toolbar">
              <select
                aria-label={copy.tagFilterAriaLabel}
                className="h-8 rounded-md border bg-background px-2 text-sm"
                value={tagFilter}
                onChange={(event) => setTagFilter(event.target.value)}
              >
                <option value={ALL_TAGS}>{copy.allTags}</option>
                {caseTags.map((tag) => (
                  <option key={tag} value={tag}>
                    {tag}
                  </option>
                ))}
              </select>
              <Badge variant="outline" className="h-8 px-2">
                {copy.matchCount(filteredCases.length, selectedCases.length)}
              </Badge>
            </div>

            {showCreateForm && (
              <CaseDraftEditor
                title={copy.newCaseTitle}
                draft={draft}
                onDraftChange={onDraftChange}
                saving={creating}
                onSave={() => submitCase(selectedAsset, draft)}
                onCancel={() => setShowCreateForm(false)}
                saveLabel={copy.saveCase}
                copy={copy.draft}
              />
            )}

            {filteredCases.length === 0 ? (
              <div
                className="rounded-md border border-dashed px-3 py-8 text-center text-sm text-muted-foreground"
                data-testid="case-library-empty"
              >
                {selectedCases.length === 0 ? copy.noCases : copy.noCaseFilterResults}
              </div>
            ) : (
              <div className="divide-y rounded-md border" data-testid="case-library-case-list">
                {filteredCases.map((item) => {
                  const editing = editingCaseId === item.id;
                  const editDraft = editDrafts[item.id] ?? manualCaseToDraft(item);
                  const focused = focusedCaseId === item.id;
                  return (
                    <div
                      key={item.id}
                      className={`grid gap-2 px-3 py-3 ${focused ? "bg-info/5 ring-1 ring-inset ring-info/40" : ""}`}
                      data-testid={`case-library-case-${item.id}`}
                    >
                      <div className="flex min-w-0 flex-col gap-2 md:flex-row md:items-start md:justify-between">
                        <div className="min-w-0">
                          <div className="flex min-w-0 flex-wrap items-center gap-2">
                            <span className="truncate text-sm font-medium">
                              {item.case_name || copy.caseName(item.case_index + 1)}
                            </span>
                            {item.source !== "manual" && (
                              <Badge variant="outline" className="text-[11px]">
                                {copy.sourceLabel(caseSourceKind(item.source))}
                              </Badge>
                            )}
                          </div>
                          <div className="mt-2 grid gap-1 text-xs">
                            <div className="line-clamp-2 text-muted-foreground">
                              <span className="font-medium text-foreground">{copy.inputPrefix}</span>
                              {caseLibraryInputText(item) || copy.missingInput}
                            </div>
                            <div className="line-clamp-2 text-muted-foreground">
                              <span className="font-medium text-foreground">{copy.expectedPrefix}</span>
                              {caseLibraryExpectedText(item) || copy.missingExpected}
                            </div>
                          </div>
                          <div className="mt-1 truncate text-[11px] text-muted-foreground">
                            {copy.updatedAt(item.updated_at || copy.missingTime)}
                            {item.tags.length > 0 ? ` · ${item.tags.map(String).join("、")}` : ` · ${copy.noTags}`}
                          </div>
                        </div>
                        <div className="flex shrink-0 flex-wrap gap-2">
                          <Button
                            size="sm"
                            variant="secondary"
                            className="h-8"
                            onClick={() => {
                              setEditingCaseId(item.id);
                              setEditDrafts((previous) => ({
                                ...previous,
                                [item.id]: manualCaseToDraft(item),
                              }));
                            }}
                          >
                            {copy.edit}
                          </Button>
                          <Button
                            size="sm"
                            variant="destructive"
                            className="h-8"
                            onClick={() => onDeleteCase(item.id)}
                            disabled={deletingCaseId === item.id || saving}
                          >
                            {deletingCaseId === item.id ? (
                              <Loader2 className="size-3.5 animate-spin" />
                            ) : (
                              <Trash2 className="size-3.5" />
                            )}
                            {copy.delete}
                          </Button>
                        </div>
                      </div>
                      {editing && (
                        <CaseDraftEditor
                          title={copy.editCaseTitle}
                          draft={editDraft}
                          onDraftChange={(nextDraft) =>
                            setEditDrafts((previous) => ({ ...previous, [item.id]: nextDraft }))
                          }
                          saving={updatingCaseId === item.id}
                          onSave={async () => {
                            await onUpdateCase(item.id, buildCaseLibraryUpdateRequest(item, editDraft));
                            setEditingCaseId(null);
                          }}
                          onCancel={() => setEditingCaseId(null)}
                          saveLabel={copy.save}
                          copy={copy.draft}
                        />
                      )}
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        ) : (
          <div className="h-28 rounded-md bg-muted/60" />
        )}
      </main>
    </section>
  );
}

export interface CaseDraftEditorProps {
  title: string;
  draft: ManualCaseDraft;
  onDraftChange: (draft: ManualCaseDraft) => void;
  saving: boolean;
  onSave: () => Promise<unknown> | unknown;
  onCancel: () => void;
  saveLabel: string;
  copy: CaseDraftEditorCopy;
}

export function CaseDraftEditor({
  title,
  draft,
  onDraftChange,
  saving,
  onSave,
  onCancel,
  saveLabel,
  copy,
}: CaseDraftEditorProps) {
  return (
    <div className="grid gap-3 rounded-md border bg-muted/10 p-3" data-testid="case-library-draft-editor">
      <div className="text-sm font-medium">{title}</div>
      <div className="grid gap-3 md:grid-cols-2">
        <Field label={copy.nameLabel}>
          <Input
            value={draft.caseName}
            onChange={(event) => onDraftChange({ ...draft, caseName: event.target.value })}
            placeholder={copy.namePlaceholder}
          />
        </Field>
        <Field label={copy.tagsLabel}>
          <Input
            value={draft.tagsText}
            onChange={(event) => onDraftChange({ ...draft, tagsText: event.target.value })}
            placeholder={copy.tagsPlaceholder}
          />
        </Field>
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        <Field label={copy.inputLabel}>
          <Textarea
            value={draft.variablesText}
            onChange={(event) => onDraftChange({ ...draft, variablesText: event.target.value })}
            className="min-h-28 font-mono text-sm"
            placeholder={copy.inputPlaceholder}
          />
        </Field>
        <Field label={copy.expectedLabel}>
          <Textarea
            value={draft.expectedText}
            onChange={(event) => onDraftChange({ ...draft, expectedText: event.target.value })}
            className="min-h-28 text-sm"
            placeholder={copy.expectedPlaceholder}
          />
        </Field>
      </div>
      <div className="flex justify-end gap-2">
        <Button size="sm" variant="ghost" onClick={onCancel}>
          {copy.cancel}
        </Button>
        <Button
          size="sm"
          onClick={() => void onSave()}
          disabled={saving || !draft.caseName.trim() || !draft.variablesText.trim()}
        >
          {saving ? <Loader2 className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
          {saveLabel}
        </Button>
      </div>
    </div>
  );
}

export function DatasetVersionHistoryPanel({
  asset,
  copy,
}: {
  asset: PromptEvaluationAsset;
  copy: DatasetVersionHistoryCopy;
}) {
  const versionsQuery = useQuery({
    queryKey: promptLibraryKeys.datasetVersions(asset.workspace_id, asset.id),
    queryFn: () => api.listPromptEvaluationDatasetVersions(asset.id, 10),
    enabled: Boolean(asset.workspace_id && asset.id),
  });
  const versions = versionsQuery.data?.items ?? [];

  return (
    <section
      className="grid gap-2 rounded-md border bg-muted/10 p-3"
      data-testid={`case-library-version-history-${asset.id}`}
    >
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-sm font-semibold">{copy.title}</h3>
          <div className="mt-1 text-xs text-muted-foreground">
            {versionsQuery.isLoading
              ? copy.loading
              : versions.length > 0
                ? copy.count(versions.length)
                : copy.noSnapshots}
          </div>
        </div>
        {versionsQuery.isFetching && <Loader2 className="size-3.5 animate-spin text-muted-foreground" />}
      </div>
      {versions.length === 0 ? (
        <div className="rounded-md border border-dashed bg-background px-3 py-3 text-sm text-muted-foreground">
          {copy.emptyDescription}
        </div>
      ) : (
        <div className="grid gap-2">
          {versions.slice(0, 5).map((version) => (
            <div
              key={version.id}
              className="grid gap-1 rounded-md border bg-background px-3 py-2 text-xs md:grid-cols-[minmax(0,1fr)_auto]"
            >
              <div className="min-w-0">
                <div className="flex min-w-0 flex-wrap items-center gap-2">
                  <span className="font-medium text-foreground">{copy.version(version.version)}</span>
                  <span className="truncate text-foreground">
                    {version.version_label || copy.unnamedVersion}
                  </span>
                  {versions[0]?.id === version.id && (
                    <Badge variant="outline" className="text-[10px]">
                      {copy.latest}
                    </Badge>
                  )}
                </div>
                <div className="mt-1 truncate text-muted-foreground">
                  {copy.rowFingerprint(
                    version.row_count,
                    version.row_fingerprint ? version.row_fingerprint.slice(0, 10) : copy.missingFingerprint,
                  )}
                </div>
              </div>
              <div className="text-muted-foreground md:text-right">
                {version.created_at || copy.missingTime}
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}
