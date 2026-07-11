import { ExternalLink, Loader2 } from "lucide-react";
import type { Issue } from "@multica/core/types";
import { useT } from "../../i18n";
import { TAPDSourceBadge } from "./tapd-source-badge";

export type IssueDetailT = ReturnType<typeof useT<"issues">>["t"];

interface IssueSourceReference {
  url: string;
  title: string;
  summary: string | null;
  sourceId: string | null;
  resourceType: string | null;
  status: string | null;
}

export function metadataText(issue: Issue, key: string): string {
  const value = issue.metadata?.[key];
  if (typeof value === "string") return value.trim();
  if (typeof value === "number") return String(value);
  return "";
}

function firstMetadataText(issue: Issue, keys: string[]): string {
  for (const key of keys) {
    const value = metadataText(issue, key);
    if (value) return value;
  }
  return "";
}

function tapdResourceTypeLabel(resourceType: string | null, t: IssueDetailT): string {
  switch (resourceType) {
    case "markdown_wiki":
      return t(($) => $.detail.tapd_source_type_markdown_wiki);
    case "story":
    case "stories":
      return t(($) => $.detail.tapd_source_type_story);
    default:
      return t(($) => $.detail.tapd_source_type_default);
  }
}

function sourceFetchStatusLabel(status: string | null, t: IssueDetailT): string | null {
  switch (status) {
    case "fetched":
      return t(($) => $.detail.tapd_source_status_fetched);
    case "pending_mcp_fetch":
      return t(($) => $.detail.tapd_source_status_pending);
    case "blocked_missing_profile":
      return t(($) => $.detail.tapd_source_status_blocked);
    case "fetch_failed":
      return t(($) => $.detail.tapd_source_status_failed);
    default:
      return null;
  }
}

function getTAPDSourceReference(issue: Issue, t: IssueDetailT): IssueSourceReference | null {
  if (metadataText(issue, "source_provider").toLowerCase() !== "tapd") return null;
  const url = firstMetadataText(issue, ["source_url", "source_fetch_url"]);
  if (!url) return null;

  const resourceType = firstMetadataText(issue, [
    "tapd_resource_type",
    "source_fetch_resource_type",
    "tapd_type",
  ]);
  const sourceId = firstMetadataText(issue, [
    "tapd_resource_id",
    "source_fetch_resource_id",
    "tapd_wiki_id",
  ]);
  const title = firstMetadataText(issue, [
    "source_fetch_title",
    "tapd_title",
    "source_title",
  ]) || t(($) => $.detail.tapd_source_title_fallback);
  const summary = firstMetadataText(issue, [
    "source_fetch_summary",
    "source_fetch_body_excerpt",
    "source_fetch_error",
  ]);
  const status = metadataText(issue, "source_fetch_status");

  return {
    url,
    title,
    summary: summary || null,
    sourceId: sourceId || null,
    resourceType: resourceType || null,
    status: status || null,
  };
}

export function TAPDSourceReference({ issue, t }: { issue: Issue; t: IssueDetailT }) {
  const source = getTAPDSourceReference(issue, t);
  if (!source) return null;
  const statusLabel = sourceFetchStatusLabel(source.status, t);

  return (
    <section
      aria-label={t(($) => $.detail.tapd_source_aria)}
      data-testid="tapd-source-card"
      className="mt-4 rounded-md border border-border/70 bg-muted/20 px-3 py-2.5"
    >
      <div className="flex min-w-0 items-start gap-2.5">
        <TAPDSourceBadge issue={issue} className="mt-0.5" />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
            <span className="font-medium text-foreground/80">{t(($) => $.detail.tapd_source_label)}</span>
            <span>{tapdResourceTypeLabel(source.resourceType, t)}</span>
            {source.sourceId && (
              <>
                <span aria-hidden>·</span>
                <span className="tabular-nums">{t(($) => $.detail.tapd_source_id, { id: source.sourceId })}</span>
              </>
            )}
            {statusLabel && (
              <>
                <span aria-hidden>·</span>
                <span>{statusLabel}</span>
              </>
            )}
          </div>
          <a
            href={source.url}
            target="_blank"
            rel="noreferrer"
            className="mt-1 inline-flex max-w-full items-center gap-1.5 text-sm font-medium text-foreground hover:underline"
          >
            <span className="truncate" data-testid="tapd-source-title">{source.title}</span>
            <ExternalLink className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          </a>
          {source.summary && (
            <p className="mt-1 line-clamp-2 text-xs leading-relaxed text-muted-foreground">
              {source.summary}
            </p>
          )}
        </div>
      </div>
    </section>
  );
}

export function SourceSummaryLoading({ label }: { label: string }) {
  return (
    <div
      className="flex min-h-28 items-center justify-center rounded-lg border border-dashed border-border bg-muted/20 px-4 py-8 text-sm text-muted-foreground"
      data-testid="source-summary-loading"
      role="status"
      aria-live="polite"
    >
      <div className="flex items-center gap-2">
        <Loader2 className="h-4 w-4 animate-spin" />
        <span>{label}</span>
      </div>
    </div>
  );
}
