"use client";

import { CheckCircle2 } from "lucide-react";
import type { Issue } from "@multica/core/types";
import { useT } from "../../i18n";

/**
 * 审查通过标记。数据来自 issue.metadata.review_passed（审查 Agent 通过后由服务端写入，
 * 重新进入审核中时清除）。只表示「审查员判过」，不代表任务完成——任务仍在审核中等用户确认。
 */
export function IssueReviewBadge({
  issue,
  className = "",
}: {
  issue: Pick<Issue, "metadata">;
  className?: string;
}) {
  const { t } = useT("issues");
  const passed = issue.metadata?.review_passed;
  if (passed !== true && passed !== "true") return null;
  const label = t(($) => $.review.review_passed);
  return (
    <span
      role="img"
      aria-label={label}
      title={t(($) => $.review.review_passed_hint)}
      className={`inline-flex shrink-0 items-center text-emerald-600 dark:text-emerald-400 ${className}`}
    >
      <CheckCircle2 className="size-3.5" aria-hidden />
    </span>
  );
}
