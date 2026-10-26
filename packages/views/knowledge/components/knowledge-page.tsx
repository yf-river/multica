"use client";

// 工作区知识库：OpenViking 的只读浏览与语义搜索。
// 服务端代理负责工作区身份与凭据，浏览器只拿结果。

import { useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpenText, Loader2, Search, Upload } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { PageHeader } from "../../layout/page-header";
import { useT } from "../../i18n";

type Hit = { uri?: string; title?: string; abstract?: string; score?: number; kind?: string };

function asRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

function collectHits(result: unknown): Hit[] {
  const outer = asRecord(result);
  const inner = asRecord(outer?.result);
  if (!inner) return [];
  const hits: Hit[] = [];
  for (const key of ["memories", "resources", "skills"] as const) {
    const rows = inner[key];
    if (!Array.isArray(rows)) continue;
    for (const row of rows) {
      const record = asRecord(row);
      if (!record) continue;
      hits.push({
        uri: typeof record.uri === "string" ? record.uri : typeof record.id === "string" ? record.id : undefined,
        title: typeof record.title === "string" ? record.title : typeof record.name === "string" ? record.name : undefined,
        abstract: typeof record.abstract === "string" ? record.abstract : typeof record.summary === "string" ? record.summary : undefined,
        score: typeof record.score === "number" ? record.score : undefined,
        kind: key,
      });
    }
  }
  return hits;
}

export function KnowledgePage() {
  const { t } = useT("workspace");
  const wsId = useWorkspaceId();
  const [query, setQuery] = useState("");
  const status = useQuery({
    queryKey: ["knowledge", "status", wsId],
    queryFn: () => api.getKnowledgeStatus(wsId),
  });
  const queryClient = useQueryClient();
  const fileInput = useRef<HTMLInputElement>(null);
  const search = useMutation({
    mutationFn: (value: string) => api.searchKnowledge(wsId, value, 10),
  });
  const upload = useMutation({
    mutationFn: (file: File) => api.uploadKnowledgeResource(wsId, file),
    onSuccess: () => {
      toast.success(t(($) => $.knowledge.upload_done));
      void queryClient.invalidateQueries({ queryKey: ["knowledge", "resources", wsId] });
      void queryClient.invalidateQueries({ queryKey: ["knowledge", "status", wsId] });
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : t(($) => $.knowledge.upload_failed));
    },
  });
  const hits = useMemo(() => collectHits(search.data?.result), [search.data]);
  const healthy = status.data?.healthy === true;

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <PageHeader>
        <div className="flex min-w-0 items-center gap-2">
          <BookOpenText className="h-4 w-4 text-muted-foreground" />
          <h1 className="truncate text-body font-semibold">{t(($) => $.knowledge.title)}</h1>
        </div>
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-4xl space-y-4 p-6">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-body text-muted-foreground">{t(($) => $.knowledge.subtitle)}</p>
            <div className="flex items-center gap-2">
              <span className={`rounded border px-2 py-0.5 text-caption ${healthy ? "border-success/40 text-success" : "border-warning/40 text-warning"}`}>
                {healthy ? t(($) => $.knowledge.status_ok) : t(($) => $.knowledge.status_down)}
              </span>
              <input
                ref={fileInput}
                type="file"
                className="hidden"
                onChange={(event) => {
                  const file = event.target.files?.[0];
                  event.target.value = "";
                  if (file) upload.mutate(file);
                }}
              />
              <Button
                type="button"
                variant="outline"
                disabled={!healthy || upload.isPending}
                onClick={() => fileInput.current?.click()}
              >
                {upload.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Upload className="h-4 w-4" />}
                {upload.isPending ? t(($) => $.knowledge.uploading) : t(($) => $.knowledge.upload)}
              </Button>
            </div>
          </div>
          <form
            className="flex gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              const value = query.trim();
              if (value) search.mutate(value);
            }}
          >
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t(($) => $.knowledge.search_placeholder)}
              aria-label={t(($) => $.knowledge.search_placeholder)}
            />
            <Button type="submit" disabled={!query.trim() || search.isPending}>
              {search.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Search className="h-4 w-4" />}
              {t(($) => $.knowledge.search)}
            </Button>
          </form>
          {status.isLoading ? (
            <Skeleton className="h-24 w-full" />
          ) : !healthy ? (
            <div className="rounded-lg border border-dashed p-4 text-body text-muted-foreground">
              {status.data?.error || t(($) => $.knowledge.error)}
            </div>
          ) : null}
          {search.isError && (
            <div className="rounded-lg border border-dashed p-4 text-body text-destructive">
              {search.error instanceof Error ? search.error.message : t(($) => $.knowledge.error)}
            </div>
          )}
          {search.isSuccess && (
            <section className="space-y-2">
              <h2 className="text-caption text-muted-foreground">{t(($) => $.knowledge.results)}</h2>
              {hits.length === 0 ? (
                <p className="rounded-lg border border-dashed p-4 text-body text-muted-foreground">
                  {t(($) => $.knowledge.empty)}
                </p>
              ) : (
                <ul className="divide-y rounded-lg border">
                  {hits.map((hit, index) => (
                    <li key={`${hit.uri ?? hit.title ?? index}`} className="p-3">
                      <div className="flex items-center justify-between gap-3">
                        <span className="text-body font-medium">{hit.title ?? hit.uri ?? "—"}</span>
                        {typeof hit.score === "number" && (
                          <span className="text-caption text-muted-foreground">{hit.score.toFixed(3)}</span>
                        )}
                      </div>
                      {hit.abstract && <p className="mt-1 line-clamp-3 text-caption text-muted-foreground">{hit.abstract}</p>}
                      {hit.uri && <p className="mt-1 truncate text-caption text-faint-foreground">{hit.uri}</p>}
                    </li>
                  ))}
                </ul>
              )}
            </section>
          )}
        </div>
      </div>
    </div>
  );
}
