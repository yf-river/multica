"use client";

import type { Issue } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";

const TAPD_LOGO_URL = "https://static-fe.tapd.cn/img/TAPD_Logo2_mini.f5e386a7.svg";

function isTAPDSourceIssue(issue: Pick<Issue, "metadata">): boolean {
  return String(issue.metadata?.source_provider ?? "").toLowerCase() === "tapd";
}

export function TAPDSourceBadge({
  issue,
  variant = "card",
  className,
}: {
  issue: Pick<Issue, "metadata">;
  variant?: "inline" | "card";
  className?: string;
}) {
  if (!isTAPDSourceIssue(issue)) return null;

  if (variant === "inline") {
    return (
      <span
        aria-label="TAPD"
        title="TAPD"
        data-testid="tapd-source-badge"
        className={cn(
          "inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center overflow-hidden rounded-full border-0 bg-transparent shadow-none",
          className,
        )}
      >
        <img src={TAPD_LOGO_URL} alt="" className="block h-full w-full object-cover" />
      </span>
    );
  }

  return (
    <span
      aria-label="TAPD"
      title="TAPD"
      data-testid="tapd-source-badge"
      className={cn(
        "inline-flex h-7 w-7 shrink-0 items-center justify-center overflow-hidden rounded-md bg-background p-0.5",
        className,
      )}
    >
      <img src={TAPD_LOGO_URL} alt="" className="h-full w-full object-contain" />
    </span>
  );
}
