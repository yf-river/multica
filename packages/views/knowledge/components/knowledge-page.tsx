"use client";

import { useMemo, useRef, useState } from "react";
import {
  BookOpenText,
  ChevronLeft,
  Folder,
  Loader2,
  RefreshCw,
  Search,
  Trash2,
  Upload,
} from "lucide-react";
import { toast } from "sonner";
import {
  knowledgeEntries,
  useKnowledge,
  type KnowledgeEntry,
} from "@multica/core/knowledge";
import { useCurrentMember } from "@multica/core/permissions";
import { useWorkspaceId } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { PageHeader } from "../../layout/page-header";
import { useT } from "../../i18n";

export function KnowledgePage() {
  const wsId = useWorkspaceId();
  return <WorkspaceKnowledgePage key={wsId} wsId={wsId} />;
}

function WorkspaceKnowledgePage({ wsId }: { wsId: string }) {
  const { t } = useT("workspace");
  const root = `viking://resources/workspaces/${wsId}`;
  const [directory, setDirectory] = useState(root);
  const [query, setQuery] = useState("");
  const [searchText, setSearchText] = useState("");
  const [url, setURL] = useState("");
  const [urlError, setUrlError] = useState<string | null>(null);
  const urlInput = useRef<HTMLInputElement>(null);
  const [pendingDelete, setPendingDelete] = useState<KnowledgeEntry | null>(
    null,
  );
  const request = useRef<{ url: string; id: string } | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const { role } = useCurrentMember(wsId);
  const canDelete = role === "owner" || role === "admin";
  const { status, resources, search, imports, upload, importURL, remove } =
    useKnowledge(wsId, searchText, directory);
  const healthy = status.data?.healthy === true;
  const result = searchText ? search : resources;
  const entries = useMemo(
    () =>
      knowledgeEntries(result.data?.result, searchText ? undefined : directory),
    [result.data, directory, searchText],
  );
  const showError = (error: unknown) =>
    toast.error(
      error instanceof Error ? error.message : t(($) => $.knowledge.error),
    );
  const submitURL = async () => {
    const value = url.trim();
    setUrlError(null);
    try {
      const parsed = new URL(value);
      if (!["http:", "https:"].includes(parsed.protocol)) throw new Error();
    } catch {
      setUrlError(t(($) => $.knowledge.url_invalid));
      urlInput.current?.focus();
      return;
    }
    if (!request.current || request.current.url !== value)
      request.current = { url: value, id: crypto.randomUUID() };
    try {
      const job = await importURL.mutateAsync({
        url: value,
        requestId: request.current.id,
      });
      if (job.status === "failed") {
        setUrlError(job.error || t(($) => $.knowledge.url_submit_failed));
        if (job.id) request.current = null;
      } else {
        setURL("");
        request.current = null;
      }
    } catch (error) {
      setUrlError(
        error instanceof Error
          ? error.message
          : t(($) => $.knowledge.url_submit_failed),
      );
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <PageHeader>
        <div className="flex min-w-0 items-center gap-2">
          <BookOpenText
            aria-hidden="true"
            className="h-4 w-4 text-muted-foreground"
          />
          <h1 className="truncate text-body font-semibold">
            {t(($) => $.knowledge.title)}
          </h1>
        </div>
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-4xl space-y-5 p-4 sm:p-6">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-body text-muted-foreground">
              {t(($) => $.knowledge.subtitle)}
            </p>
            <div className="flex items-center gap-2">
              <span
                className={`rounded border px-2 py-0.5 text-caption ${healthy ? "border-success/40 text-success" : "border-warning/40 text-warning"}`}
              >
                {healthy
                  ? t(($) => $.knowledge.status_ok)
                  : t(($) => $.knowledge.status_down)}
              </span>
              <input
                ref={fileInput}
                type="file"
                className="hidden"
                onChange={(event) => {
                  const file = event.target.files?.[0];
                  event.target.value = "";
                  if (file)
                    upload.mutate(file, {
                      onSuccess: () =>
                        toast.success(t(($) => $.knowledge.upload_done)),
                      onError: showError,
                    });
                }}
              />
              <Button
                type="button"
                variant="outline"
                disabled={!healthy || upload.isPending}
                onClick={() => fileInput.current?.click()}
              >
                {upload.isPending ? (
                  <Loader2
                    aria-hidden="true"
                    className="h-4 w-4 animate-spin motion-reduce:animate-none"
                  />
                ) : (
                  <Upload aria-hidden="true" className="h-4 w-4" />
                )}
                {upload.isPending
                  ? t(($) => $.knowledge.uploading)
                  : t(($) => $.knowledge.upload)}
              </Button>
            </div>
          </div>
          <form
            className="space-y-2 rounded-lg border p-3"
            onSubmit={(event) => {
              event.preventDefault();
              if (!importURL.isPending) void submitURL();
            }}
          >
            <label htmlFor="knowledge-url" className="text-caption font-medium">
              {t(($) => $.knowledge.url_label)}
            </label>
            <div className="flex flex-wrap gap-2">
              <Input
                id="knowledge-url"
                ref={urlInput}
                name="url"
                spellCheck={false}
                aria-invalid={!!urlError}
                aria-describedby={urlError ? "knowledge-url-error" : undefined}
                type="url"
                autoComplete="url"
                value={url}
                onChange={(event) => {
                  setURL(event.target.value);
                  setUrlError(null);
                }}
                placeholder={t(($) => $.knowledge.url_placeholder)}
                className="min-w-0 flex-1 basis-48"
                required
                disabled={importURL.isPending}
              />
              <Button
                type="submit"
                variant="outline"
                disabled={!healthy || !url.trim() || importURL.isPending}
              >
                {importURL.isPending && (
                  <Loader2
                    aria-hidden="true"
                    className="h-4 w-4 animate-spin motion-reduce:animate-none"
                  />
                )}
                {importURL.isPending
                  ? t(($) => $.knowledge.importing)
                  : t(($) => $.knowledge.import_url)}
              </Button>
            </div>
            {urlError && (
              <p
                id="knowledge-url-error"
                role="alert"
                className="text-caption text-destructive"
              >
                {urlError}
              </p>
            )}
          </form>
          <form
            className="flex gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              setSearchText(query.trim());
            }}
          >
            <Input
              value={query}
              onChange={(event) => {
                setQuery(event.target.value);
                if (!event.target.value.trim()) setSearchText("");
              }}
              placeholder={t(($) => $.knowledge.search_placeholder)}
              aria-label={t(($) => $.knowledge.search_placeholder)}
              name="knowledge-query"
              type="search"
              autoComplete="off"
              className="min-w-0"
            />
            <Button type="submit" disabled={!healthy || search.isFetching}>
              <Search aria-hidden="true" className="h-4 w-4" />
              {t(($) => $.knowledge.search)}
            </Button>
          </form>
          {status.isLoading ? (
            <Skeleton className="h-24 w-full" />
          ) : !healthy ? (
            <p
              role="status"
              className="rounded-lg border border-dashed p-4 text-body text-muted-foreground"
            >
              {status.data?.error || t(($) => $.knowledge.error)}
            </p>
          ) : (
            <section className="space-y-2" aria-busy={result.isFetching}>
              <div className="flex items-center gap-2">
                {!searchText && directory !== root && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label={t(($) => $.knowledge.back)}
                    onClick={() =>
                      setDirectory(
                        directory.slice(0, directory.lastIndexOf("/")) || root,
                      )
                    }
                  >
                    <ChevronLeft aria-hidden="true" className="h-4 w-4" />
                  </Button>
                )}
                <h2 className="min-w-0 flex-1 truncate text-caption text-muted-foreground">
                  {searchText
                    ? t(($) => $.knowledge.results)
                    : directory === root
                      ? t(($) => $.knowledge.resources)
                      : directory.split("/").pop()}
                </h2>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={t(($) => $.knowledge.refresh)}
                  disabled={result.isFetching}
                  onClick={() => {
                    void result.refetch();
                  }}
                >
                  <RefreshCw
                    aria-hidden="true"
                    className={`h-4 w-4 ${result.isFetching ? "animate-spin motion-reduce:animate-none" : ""}`}
                  />
                </Button>
              </div>
              {result.isError ? (
                <p
                  role="alert"
                  className="rounded-lg border p-4 text-body text-destructive"
                >
                  {result.error instanceof Error
                    ? result.error.message
                    : t(($) => $.knowledge.error)}
                </p>
              ) : result.isLoading ? (
                <Skeleton className="h-24 w-full" />
              ) : entries.length === 0 ? (
                <p className="rounded-lg border border-dashed p-4 text-body text-muted-foreground">
                  {searchText
                    ? t(($) => $.knowledge.empty)
                    : t(($) => $.knowledge.resources_empty)}
                </p>
              ) : (
                <ul className="divide-y rounded-lg border">
                  {entries.map((entry) => (
                    <li
                      key={entry.uri}
                      className="flex items-start gap-2 p-3 [content-visibility:auto] [contain-intrinsic-size:auto_80px]"
                    >
                      <div className="min-w-0 flex-1">
                        {entry.isDirectory ? (
                          <button
                            type="button"
                            className="flex max-w-full items-center gap-2 rounded text-left text-body font-medium hover:underline focus-visible:outline focus-visible:outline-2"
                            onClick={() => {
                              setDirectory(entry.uri);
                              setSearchText("");
                              setQuery("");
                            }}
                          >
                            <Folder
                              aria-hidden="true"
                              className="h-4 w-4 shrink-0"
                            />
                            <span className="break-all">{entry.title}</span>
                          </button>
                        ) : (
                          <p className="break-all text-body font-medium">
                            {entry.title}
                          </p>
                        )}
                        {entry.abstract && (
                          <p className="mt-1 line-clamp-3 text-caption text-muted-foreground">
                            {entry.abstract}
                          </p>
                        )}
                        <p
                          className="mt-1 truncate text-caption text-faint-foreground"
                          title={entry.uri}
                        >
                          {entry.uri}
                        </p>
                      </div>
                      {typeof entry.score === "number" && (
                        <span className="pt-2 text-caption tabular-nums text-muted-foreground">
                          {entry.score.toFixed(3)}
                        </span>
                      )}
                      {canDelete && (
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon"
                          className="size-11 shrink-0 text-muted-foreground hover:text-destructive"
                          aria-label={`${t(($) => $.knowledge.delete)} ${entry.title}`}
                          onClick={() => setPendingDelete(entry)}
                        >
                          <Trash2 aria-hidden="true" className="h-4 w-4" />
                        </Button>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </section>
          )}
          {!!imports.data?.length && (
            <section className="space-y-2" aria-live="polite">
              <h2 className="text-caption text-muted-foreground">
                {t(($) => $.knowledge.import_history)}
              </h2>
              <ul className="divide-y rounded-lg border">
                {imports.data.map((job) => (
                  <li key={job.id} className="space-y-1 p-3">
                    <div className="flex items-start gap-3">
                      <span className="min-w-0 flex-1 break-all text-caption">
                        {job.sourceUrl}
                      </span>
                      <span
                        className={`shrink-0 text-caption ${job.status === "failed" ? "text-destructive" : "text-muted-foreground"}`}
                      >
                        {job.status === "completed"
                          ? t(($) => $.knowledge.import_completed)
                          : job.status === "failed"
                            ? t(($) => $.knowledge.import_failed)
                            : job.status === "indexing"
                              ? t(($) => $.knowledge.import_indexing)
                              : t(($) => $.knowledge.import_fetching)}
                      </span>
                    </div>
                    {job.error && (
                      <p className="text-caption text-destructive">
                        {job.error}
                      </p>
                    )}
                  </li>
                ))}
              </ul>
            </section>
          )}
          {imports.isError && (
            <p role="alert" className="text-caption text-destructive">
              {t(($) => $.knowledge.error)}
            </p>
          )}
        </div>
      </div>
      <AlertDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open && !remove.isPending) setPendingDelete(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.knowledge.delete_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.knowledge.delete_description)}
              <span className="mt-2 block break-all font-medium">
                {pendingDelete?.title}
              </span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={remove.isPending}
              onClick={() => setPendingDelete(null)}
            >
              {t(($) => $.knowledge.cancel)}
            </Button>
            <Button
              type="button"
              variant="destructive"
              disabled={remove.isPending || !pendingDelete}
              onClick={async () => {
                if (!pendingDelete || remove.isPending) return;
                try {
                  await remove.mutateAsync({
                    uri: pendingDelete.uri,
                    recursive: pendingDelete.isDirectory,
                  });
                  setPendingDelete(null);
                  toast.success(t(($) => $.knowledge.delete_done));
                } catch (error) {
                  showError(error);
                }
              }}
            >
              {remove.isPending && (
                <Loader2
                  aria-hidden="true"
                  className="h-4 w-4 animate-spin motion-reduce:animate-none"
                />
              )}
              {remove.isPending
                ? t(($) => $.knowledge.deleting)
                : t(($) => $.knowledge.delete)}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
