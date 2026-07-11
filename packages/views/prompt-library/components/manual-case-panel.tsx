"use client";

import { useMemo, useState, type ReactNode } from "react";
import { Loader2, Plus, Save, Trash2 } from "lucide-react";
import type {
  PromptEvaluationAsset,
  PromptEvaluationStructuredCase,
  UpdatePromptEvaluationCaseRequest,
} from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { AppLink } from "../../navigation";
import {
  buildCaseSearchText,
  buildCaseTagUpdateRequest,
  buildManualCaseUpdateRequest,
  caseContentSummary,
  caseEvidenceFacts,
  caseIssueId,
  caseMatchesSource,
  caseSourceKind,
  caseValidation,
  manualCaseToDraft,
  uniqueSortedStrings,
  type CaseContentSummary,
  type CaseEvidenceFacts,
  type CaseSourceFilter,
  type CaseSourceKind,
  type CaseValidation,
  type ManualCaseDraft,
} from "./case-model";
import { shortId } from "./record-utils";

const ALL_TAGS = "__all_tags__";

interface CaseFilterBarCopy {
  title: string;
  description: string;
  matchCount: (visible: number, total: number) => string;
  sourceLabel: string;
  sourceOptions: Record<CaseSourceFilter, string>;
  tagsLabel: string;
  tagFilterAriaLabel: string;
  allTags: string;
  keywordPlaceholder: string;
  keywordAriaLabel: string;
}

export interface ManualCasePanelCopy {
  title: string;
  counts: (counts: {
    manual: number;
    trace: number;
    draft: number;
    approved: number;
    active: number;
  }) => string;
  filter: CaseFilterBarCopy;
  noFilterResults: string;
  caseName: (index: number) => string;
  sourceName: (source: CaseSourceKind) => string;
  statusName: (status: PromptEvaluationStructuredCase["status"]) => string;
  summary: (summary: CaseContentSummary) => string;
  approveDraft: string;
  activateCase: string;
  editCase: string;
  deleteCase: string;
  editTags: string;
  sourceIssue: (issueId: string) => string;
  openRunReview: string;
  validation: (validation: CaseValidation) => string;
  evidence: (facts: CaseEvidenceFacts) => string;
  tagsPlaceholder: string;
  tagsAriaLabel: string;
  saveTags: string;
  cancel: string;
  editCaseNamePlaceholder: string;
  editVariablesPlaceholder: string;
  editExpectedPlaceholder: string;
  editTagsPlaceholder: string;
  saveCase: string;
  noCases: string;
  caseNamePlaceholder: string;
  variablesPlaceholder: string;
  expectedPlaceholder: string;
  newTagsPlaceholder: string;
  addCase: string;
}

export interface ManualCasePanelProps {
  asset: PromptEvaluationAsset;
  cases: PromptEvaluationStructuredCase[];
  draft: ManualCaseDraft;
  onDraftChange: (draft: ManualCaseDraft) => void;
  onCreateCase: () => void;
  creating: boolean;
  focusedCaseId: string | null;
  focusedIssueId: string | null;
  focusedIssueRunReviewHref: string | null;
  runReviewHrefForIssue: (issueId: string) => string;
  onUpdateCase: (caseId: string, data: UpdatePromptEvaluationCaseRequest) => Promise<unknown>;
  updatingCaseId: string | null;
  onDeleteCase: (caseId: string) => void;
  deletingCaseId: string | null;
  copy: ManualCasePanelCopy;
}

export function ManualCasePanel({
  asset,
  cases,
  draft,
  onDraftChange,
  onCreateCase,
  creating,
  focusedCaseId,
  focusedIssueId,
  focusedIssueRunReviewHref,
  runReviewHrefForIssue,
  onUpdateCase,
  updatingCaseId,
  onDeleteCase,
  deletingCaseId,
  copy,
}: ManualCasePanelProps) {
  const manualCases = cases.filter((item) => item.source === "manual");
  const traceCases = cases.filter((item) => item.source === "trace");
  const [sourceFilter, setSourceFilter] = useState<CaseSourceFilter>("all");
  const [tagFilter, setTagFilter] = useState(ALL_TAGS);
  const [keywordFilter, setKeywordFilter] = useState("");
  const [editingCaseId, setEditingCaseId] = useState<string | null>(null);
  const [editDrafts, setEditDrafts] = useState<Record<string, ManualCaseDraft>>({});
  const [tagEditingCaseId, setTagEditingCaseId] = useState<string | null>(null);
  const [tagEditDrafts, setTagEditDrafts] = useState<Record<string, string>>({});
  const tags = useMemo(
    () => uniqueSortedStrings(cases.flatMap((item) => item.tags.map(String))),
    [cases],
  );
  const filteredCases = useMemo(() => {
    const keyword = keywordFilter.trim().toLowerCase();
    return cases.filter((item) => {
      const tagMatches = tagFilter === ALL_TAGS || item.tags.some((value) => String(value) === tagFilter);
      const sourceName = copy.sourceName(caseSourceKind(item.source));
      return (
        caseMatchesSource(item, sourceFilter) &&
        tagMatches &&
        (!keyword || buildCaseSearchText(item, sourceName).includes(keyword))
      );
    });
  }, [cases, copy, keywordFilter, sourceFilter, tagFilter]);

  return (
    <div
      data-testid={`prompt-evaluation-cases-${asset.id}`}
      className="md:col-span-2 grid gap-2 rounded-md border border-border/70 bg-muted/10 p-3"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-xs font-medium text-muted-foreground">{copy.title}</div>
        <Badge variant="outline" className="text-[11px]">
          {copy.counts({
            manual: manualCases.length,
            trace: traceCases.length,
            draft: cases.filter((item) => item.status === "draft").length,
            approved: cases.filter((item) => item.status === "approved").length,
            active: cases.filter((item) => item.status === "active" || item.status === "启用").length,
          })}
        </Badge>
      </div>
      <CaseFilterBar
        totalCount={cases.length}
        visibleCount={filteredCases.length}
        tags={tags}
        sourceFilter={sourceFilter}
        onSourceFilterChange={setSourceFilter}
        tagFilter={tagFilter}
        onTagFilterChange={setTagFilter}
        keywordFilter={keywordFilter}
        onKeywordFilterChange={setKeywordFilter}
        copy={copy.filter}
      />
      {cases.length > 0 ? (
        <div className="grid gap-1.5">
          {filteredCases.length === 0 ? (
            <div
              className="rounded border border-dashed px-2 py-2 text-xs text-muted-foreground"
              data-testid={`dataset-case-filter-empty-${asset.id}`}
            >
              {copy.noFilterResults}
            </div>
          ) : (
            filteredCases.map((item) => {
              const editing = editingCaseId === item.id;
              const editDraft = editDrafts[item.id] ?? manualCaseToDraft(item);
              const focused = focusedCaseId === item.id;
              const sourceIssueId = caseIssueId(item) || focusedIssueId;
              const runReviewHref = sourceIssueId
                ? sourceIssueId === focusedIssueId && focusedIssueRunReviewHref
                  ? focusedIssueRunReviewHref
                  : runReviewHrefForIssue(sourceIssueId)
                : null;
              const validation = caseValidation(item);
              const evidence = caseEvidenceFacts(item);
              return (
                <div
                  key={item.id}
                  data-testid={`prompt-evaluation-case-${item.id}`}
                  className={`grid gap-2 rounded px-2 py-1.5 text-xs ${focused ? "border border-info/60 bg-info/5 ring-1 ring-info/40" : "border bg-background"}`}
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium text-foreground">
                      {item.case_name || copy.caseName(item.case_index + 1)}
                    </span>
                    <span className="text-muted-foreground">
                      {copy.sourceName(caseSourceKind(item.source))}
                    </span>
                    <Badge
                      variant={item.status === "active" || item.status === "启用" ? "secondary" : "outline"}
                      className="text-[11px]"
                    >
                      {copy.statusName(item.status)}
                    </Badge>
                    <span className="min-w-0 flex-1 truncate text-muted-foreground">
                      {copy.summary(caseContentSummary(item))}
                    </span>
                    {item.source === "manual" && (
                      <>
                        {item.status === "draft" && (
                          <Button
                            size="sm"
                            variant="secondary"
                            className="h-7"
                            data-testid={`approve-eval-case-${item.id}`}
                            onClick={() => void onUpdateCase(item.id, { status: "approved" })}
                            disabled={updatingCaseId === item.id}
                          >
                            {copy.approveDraft}
                          </Button>
                        )}
                        {item.status === "approved" && (
                          <Button
                            size="sm"
                            variant="secondary"
                            className="h-7"
                            data-testid={`activate-eval-case-${item.id}`}
                            onClick={() => void onUpdateCase(item.id, { status: "active" })}
                            disabled={updatingCaseId === item.id}
                          >
                            {copy.activateCase}
                          </Button>
                        )}
                        <Button
                          size="sm"
                          variant="secondary"
                          className="h-7"
                          onClick={() => {
                            setEditingCaseId(item.id);
                            setEditDrafts((previous) => ({
                              ...previous,
                              [item.id]: manualCaseToDraft(item),
                            }));
                          }}
                        >
                          {copy.editCase}
                        </Button>
                        <Button
                          size="sm"
                          variant="destructive"
                          className="h-7"
                          onClick={() => onDeleteCase(item.id)}
                          disabled={deletingCaseId === item.id}
                        >
                          {deletingCaseId === item.id ? (
                            <Loader2 className="size-3.5 animate-spin" />
                          ) : (
                            <Trash2 className="size-3.5" />
                          )}
                          {copy.deleteCase}
                        </Button>
                      </>
                    )}
                    {asset.asset_type === "数据集" && item.source !== "manual" && (
                      <Button
                        size="sm"
                        variant="secondary"
                        className="h-7"
                        onClick={() => {
                          setTagEditingCaseId(item.id);
                          setTagEditDrafts((previous) => ({
                            ...previous,
                            [item.id]: item.tags.map(String).join(", "),
                          }));
                        }}
                      >
                        {copy.editTags}
                      </Button>
                    )}
                  </div>
                  {(sourceIssueId || validation || evidence) && (
                    <div
                      className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-sm border border-border/70 bg-muted/20 px-2 py-1.5 text-[11px] text-muted-foreground"
                      data-testid={`prompt-evaluation-case-source-${item.id}`}
                    >
                      {sourceIssueId && <span>{copy.sourceIssue(shortId(sourceIssueId))}</span>}
                      {runReviewHref && (
                        <AppLink
                          href={runReviewHref}
                          className="font-medium text-primary underline-offset-2 hover:underline"
                        >
                          {copy.openRunReview}
                        </AppLink>
                      )}
                      {validation && <span>{copy.validation(validation)}</span>}
                      {evidence && <span>{copy.evidence(evidence)}</span>}
                    </div>
                  )}
                  {tagEditingCaseId === item.id && (
                    <div
                      className="flex flex-wrap items-center gap-2 rounded-sm border border-border/70 bg-muted/20 p-2"
                      data-testid={`dataset-case-tag-editor-${item.id}`}
                    >
                      <Input
                        value={tagEditDrafts[item.id] ?? item.tags.map(String).join(", ")}
                        onChange={(event) =>
                          setTagEditDrafts((previous) => ({
                            ...previous,
                            [item.id]: event.target.value,
                          }))
                        }
                        placeholder={copy.tagsPlaceholder}
                        aria-label={copy.tagsAriaLabel}
                        className="h-9 min-w-52 flex-1 text-xs"
                      />
                      <Button
                        size="sm"
                        className="h-9 shrink-0"
                        onClick={() => {
                          void onUpdateCase(
                            item.id,
                            buildCaseTagUpdateRequest(asset, item, tagEditDrafts[item.id] ?? ""),
                          );
                          setTagEditingCaseId(null);
                        }}
                        disabled={updatingCaseId === item.id}
                      >
                        {updatingCaseId === item.id ? (
                          <Loader2 className="size-3.5 animate-spin" />
                        ) : (
                          <Save className="size-3.5" />
                        )}
                        {copy.saveTags}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-9 shrink-0"
                        onClick={() => setTagEditingCaseId(null)}
                      >
                        {copy.cancel}
                      </Button>
                    </div>
                  )}
                  {editing && (
                    <div className="grid gap-2 rounded-sm border border-border/70 bg-muted/20 p-2 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
                      <Input
                        value={editDraft.caseName}
                        onChange={(event) =>
                          setEditDrafts((previous) => ({
                            ...previous,
                            [item.id]: { ...editDraft, caseName: event.target.value },
                          }))
                        }
                        placeholder={copy.editCaseNamePlaceholder}
                      />
                      <Textarea
                        value={editDraft.variablesText}
                        onChange={(event) =>
                          setEditDrafts((previous) => ({
                            ...previous,
                            [item.id]: { ...editDraft, variablesText: event.target.value },
                          }))
                        }
                        className="min-h-20 text-xs"
                        placeholder={copy.editVariablesPlaceholder}
                      />
                      <Input
                        value={editDraft.expectedText}
                        onChange={(event) =>
                          setEditDrafts((previous) => ({
                            ...previous,
                            [item.id]: { ...editDraft, expectedText: event.target.value },
                          }))
                        }
                        placeholder={copy.editExpectedPlaceholder}
                      />
                      <div className="flex gap-2">
                        <Input
                          value={editDraft.tagsText}
                          onChange={(event) =>
                            setEditDrafts((previous) => ({
                              ...previous,
                              [item.id]: { ...editDraft, tagsText: event.target.value },
                            }))
                          }
                          placeholder={copy.editTagsPlaceholder}
                        />
                        <Button
                          size="sm"
                          className="h-10 shrink-0"
                          onClick={() => {
                            void onUpdateCase(
                              item.id,
                              buildManualCaseUpdateRequest(asset, item, editDraft),
                            );
                            setEditingCaseId(null);
                          }}
                          disabled={updatingCaseId === item.id || !editDraft.caseName.trim()}
                        >
                          {updatingCaseId === item.id ? (
                            <Loader2 className="size-3.5 animate-spin" />
                          ) : (
                            <Save className="size-3.5" />
                          )}
                          {copy.saveCase}
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-10 shrink-0"
                          onClick={() => setEditingCaseId(null)}
                        >
                          {copy.cancel}
                        </Button>
                      </div>
                    </div>
                  )}
                </div>
              );
            })
          )}
        </div>
      ) : (
        <div className="rounded border border-dashed px-2 py-2 text-xs text-muted-foreground">
          {copy.noCases}
        </div>
      )}
      <div className="grid gap-2 md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <Input
          value={draft.caseName}
          onChange={(event) => onDraftChange({ ...draft, caseName: event.target.value })}
          placeholder={copy.caseNamePlaceholder}
        />
        <Textarea
          value={draft.variablesText}
          onChange={(event) => onDraftChange({ ...draft, variablesText: event.target.value })}
          className="min-h-20 text-sm"
          placeholder={copy.variablesPlaceholder}
        />
        <Input
          value={draft.expectedText}
          onChange={(event) => onDraftChange({ ...draft, expectedText: event.target.value })}
          placeholder={copy.expectedPlaceholder}
        />
        <div className="flex gap-2">
          <Input
            value={draft.tagsText}
            onChange={(event) => onDraftChange({ ...draft, tagsText: event.target.value })}
            placeholder={copy.newTagsPlaceholder}
          />
          <Button
            size="sm"
            className="h-10 shrink-0"
            onClick={onCreateCase}
            disabled={creating || !draft.caseName.trim()}
          >
            {creating ? <Loader2 className="size-3.5 animate-spin" /> : <Plus className="size-3.5" />}
            {copy.addCase}
          </Button>
        </div>
      </div>
    </div>
  );
}

interface CaseFilterBarProps {
  totalCount: number;
  visibleCount: number;
  tags: string[];
  sourceFilter: CaseSourceFilter;
  onSourceFilterChange: (value: CaseSourceFilter) => void;
  tagFilter: string;
  onTagFilterChange: (value: string) => void;
  keywordFilter: string;
  onKeywordFilterChange: (value: string) => void;
  copy: CaseFilterBarCopy;
}

function CaseFilterBar({
  totalCount,
  visibleCount,
  tags,
  sourceFilter,
  onSourceFilterChange,
  tagFilter,
  onTagFilterChange,
  keywordFilter,
  onKeywordFilterChange,
  copy,
}: CaseFilterBarProps) {
  const sources: CaseSourceFilter[] = ["all", "manual", "trace", "payload"];
  return (
    <section
      className="grid gap-2 rounded-md border bg-background px-3 py-2 text-xs"
      data-testid="case-library-filter-bar"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="font-medium text-foreground">{copy.title}</div>
          <div className="mt-0.5 text-muted-foreground">{copy.description}</div>
        </div>
        <Badge variant="outline" data-testid="case-library-filter-count">
          {copy.matchCount(visibleCount, totalCount)}
        </Badge>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-muted-foreground">{copy.sourceLabel}</span>
        {sources.map((source) => (
          <FilterButton
            key={source}
            active={sourceFilter === source}
            onClick={() => onSourceFilterChange(source)}
          >
            {copy.sourceOptions[source]}
          </FilterButton>
        ))}
        <span className="ml-1 text-muted-foreground">{copy.tagsLabel}</span>
        <select
          aria-label={copy.tagFilterAriaLabel}
          className="h-8 rounded-md border bg-background px-2 text-xs"
          value={tagFilter}
          onChange={(event) => onTagFilterChange(event.target.value)}
        >
          <option value={ALL_TAGS}>{copy.allTags}</option>
          {tags.map((tag) => (
            <option key={tag} value={tag}>
              {tag}
            </option>
          ))}
        </select>
        <Input
          value={keywordFilter}
          onChange={(event) => onKeywordFilterChange(event.target.value)}
          placeholder={copy.keywordPlaceholder}
          aria-label={copy.keywordAriaLabel}
          className="h-8 min-w-60 flex-1 text-xs"
        />
      </div>
    </section>
  );
}

function FilterButton({
  active,
  onClick,
  href,
  children,
}: {
  active: boolean;
  onClick: () => void;
  href?: string;
  children: ReactNode;
}) {
  const className = `inline-flex h-7 items-center rounded-md border px-2.5 text-xs transition-colors ${
    active
      ? "border-foreground bg-foreground text-background"
      : "border-border bg-background text-muted-foreground hover:text-foreground"
  }`;
  if (href) {
    return (
      <AppLink
        href={href}
        onClick={onClick}
        className={className}
        data-active={active ? "true" : undefined}
        aria-current={active ? "page" : undefined}
      >
        {children}
      </AppLink>
    );
  }
  return (
    <button
      type="button"
      onClick={onClick}
      className={className}
      data-active={active ? "true" : undefined}
    >
      {children}
    </button>
  );
}
