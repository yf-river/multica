"use client";

import { validateGitRef } from "@multica/core/github";
import { useId, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useUpdateProjectResource } from "@multica/core/projects";
import { runtimeListOptions } from "@multica/core/runtimes";
import { useRepositoryActions } from "@multica/core/workspace/repository-queries";
import type {
  WorkspaceRepo,
  WorkspaceRepositoryCenter,
} from "@multica/core/types/workspace";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

export function RepositoryConfiguration({
  wsId,
  repo,
  center,
  canManage,
  section,
}: {
  wsId: string;
  repo: WorkspaceRepo;
  center?: WorkspaceRepositoryCenter;
  canManage: boolean;
  section: "branch" | "location";
}) {
  const { t } = useT("settings");
  const id = useId();
  const [branch, setBranch] = useState(repo.ref ?? "");
  const [revision, setRevision] = useState(repo.revision);
  const [defaultForTasks, setDefaultForTasks] = useState(
    repo.default_for_tasks ?? false,
  );
  const [daemon, setDaemon] = useState("");
  const [path, setPath] = useState("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const actions = useRepositoryActions(wsId);
  const { data: runtimes = [] } = useQuery({
    ...runtimeListOptions(wsId),
    enabled: section === "location",
  });
  const machines = [
    ...new Map(
      runtimes.filter((r) => r.daemon_id).map((r) => [r.daemon_id!, r]),
    ).values(),
  ];
  const locations =
    center?.configured_locations?.filter((x) => x.repository_id === repo.id) ??
    [];
  const pending =
    actions.update.isPending ||
    actions.location.isPending ||
    actions.removeLocation.isPending;
  const perform = async (action: () => Promise<unknown>) => {
    setError("");
    setSaved(false);
    try {
      await action();
      setSaved(true);
    } catch (e) {
      setError(
        e instanceof Error ? e.message : t(($) => $.repository_config.failed),
      );
    }
  };
  if (section === "branch")
    return (
      <form
        className="space-y-3 rounded-lg border border-surface-border p-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!validateGitRef(branch).ok) {
            setError(t(($) => $.repository_config.invalid_branch));
            setSaved(false);
            return;
          }
          if (repo.id)
            void perform(() =>
              actions.update
                .mutateAsync({
                  id: repo.id!,
                  data: {
                    ref: branch.trim(),
                    revision,
                    default_for_tasks: defaultForTasks,
                  },
                })
                .then((saved) => setRevision(saved.revision)),
            );
        }}
      >
        <label className="block text-caption font-medium" htmlFor={id}>
          {t(($) => $.repository_config.development_branch)}
        </label>
        <Input
          id={id}
          name="development_branch"
          autoComplete="off"
          spellCheck={false}
          value={branch}
          onChange={(e) => setBranch(e.target.value)}
          disabled={!canManage || pending}
          placeholder={
            repo.default_branch || t(($) => $.repository_config.remote_default)
          }
        />
        <p className="text-caption text-muted-foreground">
          {t(($) => $.repository_config.branch_help)}
        </p>
        <label className="flex items-center gap-2 text-caption">
          <input
            type="checkbox"
            checked={defaultForTasks}
            disabled={!canManage || pending}
            onChange={(e) => setDefaultForTasks(e.target.checked)}
          />
          {t(($) => $.repository_config.default_for_tasks)}
        </label>
        {canManage ? (
          <Button type="submit" size="sm" disabled={pending || !repo.id}>
            {pending
              ? t(($) => $.repository_config.saving)
              : t(($) => $.repository_config.save)}
          </Button>
        ) : null}
        {error ? (
          <p role="alert" className="text-caption text-destructive">
            {error}
          </p>
        ) : null}
        {saved ? (
          <p role="status" className="text-caption">
            {t(($) => $.repository_config.saved)}
          </p>
        ) : null}
      </form>
    );
  return (
    <div className="space-y-3">
      <p className="font-medium">{t(($) => $.repository_config.locations)}</p>
      {locations.map((location) => (
        <div
          key={location.id}
          className="space-y-2 rounded-lg border border-surface-border p-3"
        >
          <p className="break-all text-caption">
            {machines.find((r) => r.daemon_id === location.daemon_id)?.name ||
              location.daemon_id}
          </p>
          <p className="break-all font-mono text-caption">
            {location.local_path}
          </p>
          <p className="text-caption text-muted-foreground">
            {location.execution_mode === "worktree"
              ? t(($) => $.repository_config.isolated)
              : t(($) => $.repository_config.in_place)}
          </p>
          {canManage ? (
            <div className="flex gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={pending}
                onClick={() => {
                  setDaemon(location.daemon_id);
                  setPath(location.local_path);
                }}
              >
                {t(($) => $.repository_config.edit)}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                disabled={pending}
                onClick={() =>
                  void perform(() =>
                    actions.removeLocation.mutateAsync({
                      id: repo.id!,
                      locationId: location.id,
                    }),
                  )
                }
              >
                {t(($) => $.repository_config.remove)}
              </Button>
            </div>
          ) : null}
        </div>
      ))}
      {locations.length === 0 ? (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.repository_config.no_location)}
        </p>
      ) : null}
      {canManage ? (
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
          if (!validateGitRef(branch).ok) {
            setError(t(($) => $.repository_config.invalid_branch));
            setSaved(false);
            return;
          }
            if (repo.id)
              void perform(() =>
                actions.location.mutateAsync({
                  id: repo.id!,
                  daemon_id: daemon,
                  local_path: path.trim(),
                  execution_mode: "worktree",
                }),
              );
          }}
        >
          <label htmlFor={id} className="block text-caption">
            {t(($) => $.repository_config.machine)}
          </label>
          <select
            name="daemon_id"
            id={id}
            className="h-9 w-full rounded-md border border-input bg-background px-3 text-caption focus-visible:ring-2 focus-visible:ring-ring"
            value={daemon}
            disabled={pending}
            onChange={(e) => setDaemon(e.target.value)}
          >
            <option value="">
              {t(($) => $.repository_config.choose_machine)}
            </option>
            {machines.map((r) => (
              <option key={r.daemon_id} value={r.daemon_id ?? ""}>
                {r.name}
              </option>
            ))}
          </select>
          <label htmlFor={id + "-path"} className="block text-caption">
            {t(($) => $.repository_config.path)}
          </label>
          <Input
            name="local_path"
            autoComplete="off"
            spellCheck={false}
            id={id + "-path"}
            value={path}
            onChange={(e) => setPath(e.target.value)}
            disabled={pending}
          />
          <p className="text-caption text-muted-foreground">
            {t(($) => $.repository_config.location_help)}
          </p>
          <Button
            type="submit"
            size="sm"
            disabled={!repo.id || !daemon || !path.trim() || pending}
          >
            {pending
              ? t(($) => $.repository_config.saving)
              : t(($) => $.repository_config.save)}
          </Button>
        </form>
      ) : null}
      {error ? (
        <p role="alert" className="text-caption text-destructive">
          {error}
        </p>
      ) : null}
      {saved ? (
        <p role="status" className="text-caption">
          {t(($) => $.repository_config.saved)}
        </p>
      ) : null}
    </div>
  );
}

export function ProjectRepositoryBranch({
  wsId,
  projectId,
  resource,
  canManage = true,
}: {
  wsId: string;
  projectId: string;
  resource: import("@multica/core/types").ProjectResource;
  canManage?: boolean;
}) {
  const { t } = useT("settings");
  const id = useId();
  const ref = resource.resource_ref as {
    repository_id?: string;
    project_ref?: string;
    ref?: string;
    url?: string;
  };
  const [branch, setBranch] = useState(ref.project_ref ?? "");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const actions = useUpdateProjectResource(wsId, projectId);
  if (!ref.repository_id) return null;
  return (
    <details className="text-caption">
      <summary className="cursor-pointer py-1 text-muted-foreground">
        {t(($) => $.repository_config.project_branch)}：
        {ref.ref || t(($) => $.repository_config.remote_default)}
      </summary>
      <form
        className="space-y-2 py-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (!validateGitRef(branch).ok) {
            setError(t(($) => $.repository_config.invalid_branch));
            setSaved(false);
            return;
          }
          setError("");
          setSaved(false);
          void actions
            .mutateAsync({
              resourceId: resource.id,
              data: {
                resource_ref: {
                  repository_id: ref.repository_id,
                  ref: branch.trim(),
                },
              },
            })
            .then(() => setSaved(true))
            .catch((e) =>
              setError(
                e instanceof Error
                  ? e.message
                  : t(($) => $.repository_config.failed),
              ),
            );
        }}
      >
        <label htmlFor={id}>
          {t(($) => $.repository_config.project_branch)}
        </label>
        <Input
          id={id}
          name="development_branch"
          autoComplete="off"
          spellCheck={false}
          value={branch}
          disabled={!canManage || actions.isPending}
          onChange={(e) => setBranch(e.target.value)}
        />
        <p className="text-muted-foreground">
          {t(($) => $.repository_config.project_branch_help)}
        </p>
        {canManage ? (
          <Button type="submit" size="sm" disabled={actions.isPending}>
            {t(($) => $.repository_config.save)}
          </Button>
        ) : null}
        {error ? (
          <p role="alert" className="text-destructive">
            {error}
          </p>
        ) : null}
        {saved ? (
          <p role="status">{t(($) => $.repository_config.saved)}</p>
        ) : null}
      </form>
    </details>
  );
}

export function RepositoryURLForm({
  wsId,
  onSaved,
}: {
  wsId: string;
  onSaved: () => void;
}) {
  const { t } = useT("settings");
  const id = useId();
  const [url, setURL] = useState("");
  const [error, setError] = useState("");
  const { save } = useRepositoryActions(wsId);
  return (
    <form
      className="space-y-3 border-t border-surface-border pt-4"
      onSubmit={(e) => {
        e.preventDefault();
        setError("");
        void save
          .mutateAsync([{ url: url.trim() }])
          .then(onSaved)
          .catch((e) =>
            setError(
              e instanceof Error
                ? e.message
                : t(($) => $.repository_config.failed),
            ),
          );
      }}
    >
      <label htmlFor={id} className="text-caption font-medium">
        {t(($) => $.repository_config.add_url)}
      </label>
      <Input
        id={id}
        name="repository_url"
        autoComplete="off"
        spellCheck={false}
        value={url}
        onChange={(e) => setURL(e.target.value)}
        disabled={save.isPending}
        placeholder="https://github.com/owner/repository"
        required
      />
      <p className="text-caption text-muted-foreground">
        {t(($) => $.repository_config.add_url_help)}
      </p>
      {error ? (
        <p role="alert" className="text-caption text-destructive">
          {error}
        </p>
      ) : null}
      <Button type="submit" size="sm" disabled={save.isPending}>
        {save.isPending
          ? t(($) => $.repository_config.saving)
          : t(($) => $.repository_config.save)}
      </Button>
    </form>
  );
}
