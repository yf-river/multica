"use client";

import { useQuery } from "@tanstack/react-query";
import { BookOpenText, RefreshCw } from "lucide-react";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { PageHeader } from "../../layout/page-header";
import { useT } from "../../i18n";

export function KnowledgePage() {
  const { t } = useT("workspace");
  const wsId = useWorkspaceId();
  const status = useQuery({
    queryKey: ["knowledge", "status", wsId],
    queryFn: () => api.getKnowledgeStatus(wsId),
    refetchInterval: 15000,
  });
  const healthy = status.data?.healthy === true;

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <PageHeader>
        <div className="flex min-w-0 items-center gap-2">
          <BookOpenText className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
          <h1 className="truncate text-body font-semibold">{t(($) => $.knowledge.title)}</h1>
        </div>
        <Button variant="ghost" size="icon" aria-label={t(($) => $.knowledge.refresh)} disabled={status.isFetching} onClick={() => void status.refetch()}>
          <RefreshCw className="h-4 w-4" aria-hidden="true" />
        </Button>
      </PageHeader>
      {status.isPending ? (
        <div className="flex-1 p-4"><Skeleton className="h-full w-full" /></div>
      ) : healthy ? (
        <iframe className="min-h-0 w-full flex-1 border-0" src={status.data?.studio_url ?? "/studio/"} title="OpenViking Studio" />
      ) : (
        <div className="m-auto max-w-lg space-y-3 p-6 text-center" role="status">
          <p className="text-body font-medium">{t(($) => $.knowledge.unavailable)}</p>
          <p className="break-words text-caption text-muted-foreground">{status.data?.error ?? status.error?.message}</p>
          <Button variant="outline" disabled={status.isFetching} onClick={() => void status.refetch()}>{t(($) => $.knowledge.refresh)}</Button>
        </div>
      )}
      {healthy && status.data?.sync && status.data.sync.pending > 0 && (
        <p className="border-t px-4 py-2 text-caption text-muted-foreground" role="status">
          {t(($) => $.knowledge.sync_pending, { count: status.data.sync.pending })}
          {status.data.sync.failed > 0 && ` · ${t(($) => $.knowledge.sync_failed, { count: status.data.sync.failed })}`}
        </p>
      )}
    </div>
  );
}
