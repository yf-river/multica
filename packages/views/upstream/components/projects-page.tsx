"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { upstreamKeys } from "@multica/core/upstream/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { AppLink } from "../../navigation/app-link";
import { useT } from "../../i18n";

export function UpstreamProjectsPage() {
  const { t } = useT("upstream");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const qc = useQueryClient();
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState(false);
  const [enabled, setEnabled] = useState(true);
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  const projects = useQuery({
    queryKey: [...upstreamKeys.all(wsId), "projects"],
    queryFn: () => api.listUpstreamProjects(),
    refetchInterval: 5000,
  });
  const refresh = () =>
    qc.invalidateQueries({ queryKey: upstreamKeys.all(wsId) });
  const save = useMutation({
    mutationFn: api.saveUpstreamProject.bind(api),
    onSuccess: () => {
      setAdding(false);
      setId("");
      setName("");
      void refresh();
    },
  });
  const sync = useMutation({
    mutationFn: (projectId: string) => api.syncUpstreamProject(projectId),
    onSettled: refresh,
  });
  const canWrite = projects.data?.can_write === true;
  const globalBusy =
    projects.data?.projects.some(
      (project) => Boolean(project.snapshot.active_run_id) || project.syncing,
    ) === true;
  const failure = projects.error ?? save.error ?? sync.error;
  return (
    <div className="h-full overflow-y-auto p-4 sm:p-6">
      <header className="mb-5 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-title font-medium">{t(($) => $.page.title)}</h1>
          <p className="mt-1 text-caption text-muted-foreground">
            {t(($) => $.projects.manual)}
          </p>
        </div>
        {canWrite ? (
          <Button
            variant="outline"
            onClick={() => {
              setAdding(true);
              setEditing(false);
              setId("");
              setName("");
              setEnabled(true);
            }}
          >
            {t(($) => $.projects.add)}
          </Button>
        ) : null}
      </header>
      {failure ? (
        <p role="alert" className="mb-4 text-body text-destructive">
          {failure.message}
        </p>
      ) : null}
      {adding ? (
        <form
          className="mb-5 flex flex-wrap items-end gap-3 rounded-lg border p-4"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate({ id, name, enabled });
          }}
        >
          <label className="min-w-0 text-caption">
            {t(($) => $.projects.id)}
            <Input
              disabled={editing}
              required
              pattern="[a-z][a-z0-9-]{0,63}"
              value={id}
              onChange={(e) => setId(e.target.value)}
            />
          </label>
          <label className="min-w-0 text-caption">
            {t(($) => $.projects.name)}
            <Input
              required
              maxLength={200}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <Button type="submit" disabled={save.isPending}>
            {t(($) => $.projects.save)}
          </Button>
          <Button
            type="button"
            variant="ghost"
            onClick={() => setAdding(false)}
          >
            {t(($) => $.projects.cancel)}
          </Button>
        </form>
      ) : null}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
        {(projects.data?.projects ?? []).map((project) => {
          const snap = project.snapshot;
          const running =
            Boolean(snap.active_run_id) ||
            project.syncing ||
            (sync.isPending && sync.variables === project.id);
          return (
            <article
              key={project.id}
              className="flex min-w-0 flex-col gap-4 rounded-xl border border-surface-border bg-background p-5"
            >
              <div>
                <div className="flex items-center justify-between gap-2">
                  <h2 className="min-w-0 truncate text-body font-semibold">
                    {project.name}
                  </h2>
                  {canWrite ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => {
                        setEditing(true);
                        setAdding(true);
                        setId(project.id);
                        setName(project.name);
                        setEnabled(project.enabled);
                      }}
                    >
                      {t(($) => $.projects.edit)}
                    </Button>
                  ) : null}
                  {canWrite ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={save.isPending}
                      onClick={() =>
                        save.mutate({
                          id: project.id,
                          name: project.name,
                          enabled: !project.enabled,
                        })
                      }
                    >
                      {project.enabled
                        ? t(($) => $.projects.disable)
                        : t(($) => $.projects.enable)}
                    </Button>
                  ) : null}
                </div>
                <p className="mt-1 text-caption text-muted-foreground">
                  {!project.enabled
                    ? t(($) => $.projects.disabled)
                    : running
                      ? t(($) => $.projects.syncing)
                      : t(($) => $.projects.pending, { count: snap.pending })}
                </p>
              </div>
              <dl className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-2 text-caption">
                <dt className="text-muted-foreground">
                  {t(($) => $.projects.baseline)}
                </dt>
                <dd
                  className="truncate tabular-nums"
                  title={snap.config.bootstrap_base_sha}
                >
                  {snap.config.bootstrap_base_sha ? (
                    <>
                      {snap.config.baseline_committed_at
                        ? new Date(
                            snap.config.baseline_committed_at,
                          ).toLocaleDateString(undefined, {
                            month: "2-digit",
                            day: "2-digit",
                          }) + " · "
                        : ""}
                      {snap.config.bootstrap_base_sha.slice(0, 8)}
                    </>
                  ) : (
                    "—"
                  )}
                </dd>
                <dt className="text-muted-foreground">
                  {t(($) => $.projects.head)}
                </dt>
                <dd
                  className="truncate tabular-nums"
                  title={snap.config.head_sha}
                >
                  {snap.config.head_sha ? (
                    <>
                      {snap.config.head_committed_at
                        ? new Date(
                            snap.config.head_committed_at,
                          ).toLocaleDateString(undefined, {
                            month: "2-digit",
                            day: "2-digit",
                          }) + " · "
                        : ""}
                      {snap.config.head_sha.slice(0, 8)}
                    </>
                  ) : (
                    "—"
                  )}
                </dd>
                <dt className="text-muted-foreground">
                  {t(($) => $.projects.to_analyze)}
                </dt>
                <dd className="tabular-nums">
                  {snap.config.pending_commits == null
                    ? "—"
                    : t(($) => $.projects.commits, {
                        count: snap.config.pending_commits,
                      })}
                </dd>
                <dt className="text-muted-foreground">
                  {t(($) => $.projects.last)}
                </dt>
                <dd>
                  {snap.config.last_run_at
                    ? new Date(snap.config.last_run_at).toLocaleString()
                    : t(($) => $.projects.never)}
                </dd>
              </dl>
              {snap.config.error ? (
                <p className="break-words text-caption text-destructive">
                  {snap.config.error}
                </p>
              ) : null}
              <footer className="mt-auto flex items-center justify-between gap-2">
                <AppLink
                  href={`${paths.upstream()}/${encodeURIComponent(project.id)}`}
                  className="text-body text-brand hover:underline"
                >
                  {t(($) => $.projects.details)}
                </AppLink>
                <Button
                  disabled={
                    !canWrite ||
                    !project.enabled ||
                    running ||
                    globalBusy ||
                    sync.isPending
                  }
                  onClick={() => sync.mutate(project.id)}
                >
                  {running
                    ? t(($) => $.projects.syncing)
                    : t(($) => $.projects.sync)}
                </Button>
              </footer>
            </article>
          );
        })}
      </div>
    </div>
  );
}
