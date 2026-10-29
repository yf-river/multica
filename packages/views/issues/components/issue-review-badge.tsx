"use client";

import { CheckCircle2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { releaseAgentSettingsOptions } from "@multica/core/release-agent/queries";
import type { Issue } from "@multica/core/types";
import { useT } from "../../i18n";

export function IssueReviewBadge({
  issue,
  className = "",
}: {
  issue: Pick<Issue, "metadata"> & Partial<Pick<Issue, "workspace_id" | "project_id">>;
  className?: string;
}) {
  const { t } = useT("issues");
  const settings = useQuery(releaseAgentSettingsOptions(issue.workspace_id ?? "", !!issue.workspace_id && !!issue.project_id));
  const metadata = issue.metadata;
  const isPassed = (value: unknown) => value === true || value === "true";
  const managed = issue.project_id && settings.data
    ? settings.data.enabled && settings.data.project_id === issue.project_id
    : isPassed(metadata?.release_workflow);
  const review = isPassed(metadata?.review_passed);
  if (!managed && !review) return null;
  const checks = managed
    ? [
        { passed: review, label: review ? t(($) => $.review.review_passed) : t(($) => $.review.review_waiting) },
        { passed: isPassed(metadata?.verification_passed), label: isPassed(metadata?.verification_passed) ? t(($) => $.review.verification_passed) : t(($) => $.review.verification_waiting) },
        { passed: isPassed(metadata?.integration_passed), label: isPassed(metadata?.integration_passed) ? t(($) => $.review.integration_passed) : t(($) => $.review.integration_waiting) },
      ]
    : [{ passed: true, label: t(($) => $.review.review_passed) }];
  return (
    <span className={`inline-flex shrink-0 items-center gap-1 ${className}`}>
      {checks.map(({ passed, label }) => (
        <span key={label} role="img" aria-label={label} title={label} className={passed ? "text-emerald-600 dark:text-emerald-400" : "text-muted-foreground/50"}>
          <CheckCircle2 className="size-3.5" aria-hidden />
        </span>
      ))}
    </span>
  );
}
