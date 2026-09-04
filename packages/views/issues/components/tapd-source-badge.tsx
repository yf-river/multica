"use client";

import type { Issue } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";

const TAPD_LOGO_URL = "https://static-fe.tapd.cn/img/TAPD_Logo2_mini.f5e386a7.svg";

function sourceProvider(issue: Pick<Issue, "metadata">): "tapd" | "gongfeng" | null {
  const provider = String(issue.metadata?.source_provider ?? "").toLowerCase();
  return provider === "tapd" || provider === "gongfeng" ? provider : null;
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
  const provider = sourceProvider(issue);
  if (!provider) return null;
  const label = provider === "tapd" ? "TAPD" : "工蜂";
  const mark = provider === "gongfeng" ? label.slice(1) : "";

  if (variant === "inline") {
    return (
      <span
        aria-label={label}
        title={label}
        data-testid="tapd-source-badge"
        className={cn(
          "inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center overflow-hidden rounded-full border-0 bg-transparent shadow-none",
          className,
        )}
      >
        {provider === "tapd" ? (
          <img src={TAPD_LOGO_URL} alt="" className="block h-full w-full object-cover" />
        ) : (
          <span className="text-micro font-bold text-primary">{mark}</span>
        )}
      </span>
    );
  }

  return (
    <span
      aria-label={label}
      title={label}
      data-testid="tapd-source-badge"
      className={cn(
        "inline-flex h-7 w-7 shrink-0 items-center justify-center overflow-hidden rounded-md bg-background p-0.5",
        className,
      )}
    >
      {provider === "tapd" ? (
        <img src={TAPD_LOGO_URL} alt="" className="h-full w-full object-contain" />
      ) : (
        <span className="text-caption font-bold text-primary">{mark}</span>
      )}
    </span>
  );
}
