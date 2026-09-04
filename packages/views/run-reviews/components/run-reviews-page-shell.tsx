"use client";

import { lazy, Suspense, useEffect, useState } from "react";
import { Skeleton } from "@multica/ui/components/ui/skeleton";

import { PageHeader } from "../../layout/page-header";
import { useT } from "../../i18n";

const RunReviewsPageContent = lazy(() =>
  import("./run-reviews-page").then((module) => ({ default: module.RunReviewsPage })),
);

export function RunReviewsPage() {
  const { t } = useT("run-reviews");
  const [showContent, setShowContent] = useState(false);
  useEffect(() => {
    const timer = window.setTimeout(() => setShowContent(true), 0);
    return () => window.clearTimeout(timer);
  }, []);

  const fallback = (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <PageHeader>
        <h1 className="truncate text-body font-semibold">{t(($) => $.page_title)}</h1>
      </PageHeader>
      <div className="grid min-h-0 flex-1 grid-cols-1 lg:grid-cols-[360px_minmax(0,1fr)]">
        <div className="space-y-2 border-r p-3">
          {Array.from({ length: 6 }, (_, index) => (
            <Skeleton key={index} className="h-14 w-full" />
          ))}
        </div>
        <div className="space-y-4 p-5">
          <Skeleton className="h-7 w-56" />
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-52 w-full" />
        </div>
      </div>
    </div>
  );

  if (!showContent) return fallback;
  return (
    <Suspense fallback={fallback}>
      <RunReviewsPageContent />
    </Suspense>
  );
}
