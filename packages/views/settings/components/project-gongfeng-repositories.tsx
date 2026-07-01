"use client";

import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import {
  AlertCircle,
  Ban,
  CheckCircle2,
  FolderGit,
  Info,
  KeyRound,
  Plus,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  projectListOptions,
  projectResourcesOptions,
  useDeleteProjectResource,
} from "@multica/core/projects";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { workspaceKeys } from "@multica/core/workspace/queries";
import type {
  GongfengRepoResourceRef,
  Project,
  ProjectResource,
  Workspace,
  WorkspaceRepo,
  WorkspaceRepoProbeResponse,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

function isGongfengResource(resource: ProjectResource): resource is ProjectResource & {
  resource_ref: GongfengRepoResourceRef;
} {
  return resource.resource_type === "gongfeng_repo";
}

function isGongfengURL(url: string): boolean {
  try {
    return new URL(url).hostname === "git.code.tencent.com";
  } catch {
    return url.includes("git.code.tencent.com");
  }
}

type GongfengResourceUsage = ProjectResource & {
  resource_ref: GongfengRepoResourceRef;
  project: Project;
};

type RepositoryLibraryRow = {
  url: string;
  repo?: WorkspaceRepo;
  inLibrary: boolean;
  usages: GongfengResourceUsage[];
};

const GONGFENG_REPO_PRESETS = [
  { key: "usercenter", label: "usercenter", url: "https://git.code.tencent.com/ChainWeaver/ida/user-center" },
  { key: "gateway", label: "gateway", url: "https://git.code.tencent.com/ChainWeaver/ida/gateway" },
  { key: "ida-deployment", label: "ida-deployment", url: "https://git.code.tencent.com/ChainWeaver/ida/ida-deployment" },
];

function workspaceRepoList(workspace: Workspace | null | undefined): WorkspaceRepo[] {
  return Array.isArray(workspace?.repos) ? workspace.repos : [];
}

export function ProjectGongfengRepositories() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const [addOpen, setAddOpen] = useState(false);
  const [resolvedReposByURL, setResolvedReposByURL] = useState<Record<string, WorkspaceRepo>>({});
  const resourceQueries = useQueries({
    queries: projects.map((project) => projectResourcesOptions(wsId, project.id)),
  });

  const usages = useMemo<GongfengResourceUsage[]>(() => {
    return resourceQueries.flatMap((query, idx) => {
      const project = projects[idx];
      if (!project) return [];
      return (query.data ?? [])
        .filter(isGongfengResource)
        .map((resource) => ({ ...resource, project }));
    });
  }, [projects, resourceQueries]);

  const rows = useMemo<RepositoryLibraryRow[]>(() => {
    const usagesByProjectPath = new Map<string, GongfengResourceUsage[]>();
    for (const usage of usages) {
      const projectPath = gongfengResourceProjectPath(usage.resource_ref);
      if (!projectPath) continue;
      usagesByProjectPath.set(projectPath, [...(usagesByProjectPath.get(projectPath) ?? []), usage]);
    }

    const rows: RepositoryLibraryRow[] = [];
    for (const repo of workspaceRepoList(workspace)) {
      if (!isGongfengURL(repo.url)) continue;
      const resolved = resolvedReposByURL[repo.url];
      const merged = resolved ? { ...repo, ...resolved } : repo;
      const projectPath = gongfengWorkspaceRepoProjectPath(merged);
      rows.push({
        url: repo.url,
        repo: merged,
        inLibrary: true,
        usages: projectPath ? usagesByProjectPath.get(projectPath) ?? [] : [],
      });
    }
    return rows.sort((a, b) => a.url.localeCompare(b.url));
  }, [resolvedReposByURL, usages, workspace?.repos]);

  if (!workspace) return null;

  const handleRepoResolved = (repo: WorkspaceRepo) => {
    setResolvedReposByURL((current) => ({
      ...current,
      [repo.url]: { ...current[repo.url], ...repo },
    }));
  };

  return (
    <section className="space-y-3" data-testid="settings-gongfeng-repository-inventory">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <FolderGit className="size-4 text-muted-foreground" />
            <h3 className="text-sm font-medium">{t(($) => $.repositories.gongfeng_inventory.title)}</h3>
            <span className="font-mono text-xs tabular-nums text-muted-foreground">
              {rows.length}
            </span>
          </div>
          <p className="text-xs text-muted-foreground">
            {t(($) => $.repositories.gongfeng_inventory.description)}
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={() => setAddOpen(true)}>
          <Plus className="size-3" />
          {t(($) => $.repositories.gongfeng_inventory.add)}
        </Button>
      </div>
      {rows.length === 0 ? (
        <div className="rounded-md border border-dashed px-3 py-6 text-center text-xs text-muted-foreground">
          {t(($) => $.repositories.gongfeng_inventory.empty)}
        </div>
      ) : (
        <div className="divide-y rounded-md border bg-background">
          {rows.map((row) => (
            <GongfengRepositoryLibraryRow
              key={row.url}
              row={row}
              workspace={workspace}
              onRepoResolved={handleRepoResolved}
              projectHref={(id) => wsPaths.projectDetail(id)}
            />
          ))}
        </div>
      )}
      <AddGongfengRepositoryDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        workspace={workspace}
      />
    </section>
  );
}

function AddGongfengRepositoryDialog({
  open,
  onOpenChange,
  workspace,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  workspace: Workspace;
}) {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const [url, setUrl] = useState("");
  const [selectedBranch, setSelectedBranch] = useState("");
  const [probe, setProbe] = useState<WorkspaceRepoProbeResponse | null>(null);
  const [probeError, setProbeError] = useState("");
  const [probing, setProbing] = useState(false);
  const [saving, setSaving] = useState(false);
  const trimmedURL = url.trim();
  const probeMatches = Boolean(probe && probe.url === trimmedURL);
  const canSubmit = Boolean(trimmedURL && selectedBranch && probeMatches) && !saving && !probing;
  const existingProjectPaths = new Set(
    workspaceRepoList(workspace)
      .map((repo) => gongfengWorkspaceRepoProjectPath(repo))
      .filter(Boolean),
  );

  const resetProbeState = () => {
    setProbe(null);
    setProbeError("");
    setSelectedBranch("");
  };

  const handleURLChange = (value: string) => {
    setUrl(value);
    resetProbeState();
  };

  const handlePreset = (presetURL: string) => {
    setUrl(presetURL);
    resetProbeState();
  };

  const handleProbe = async () => {
    if (!trimmedURL || probing || saving) return;
    setProbing(true);
    setProbeError("");
    try {
      const result = await api.probeWorkspaceRepo(workspace.id, { url: trimmedURL });
      setProbe(result);
      setSelectedBranch(result.default_branch || result.branches[0] || "");
    } catch (err) {
      setProbe(null);
      setSelectedBranch("");
      setProbeError(err instanceof Error ? err.message : t(($) => $.repositories.gongfeng_inventory.probe_failed));
    } finally {
      setProbing(false);
    }
  };

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!canSubmit || !probe) return;
    const projectPath = probe.project_path || inferProjectPathFromGongfengURL(trimmedURL);
    if (projectPath && existingProjectPaths.has(projectPath)) {
      toast.error(t(($) => $.repositories.gongfeng_inventory.duplicate_error));
      return;
    }
    setSaving(true);
    try {
      const resolved = await api.resolveWorkspaceRepo(workspace.id, {
        url: trimmedURL,
        default_branch: selectedBranch,
      });
      const repos = [...workspaceRepoList(workspace), resolved];
      const updated = await api.updateWorkspace(workspace.id, { repos });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      toast.success(t(($) => $.repositories.gongfeng_inventory.create_success));
      setUrl("");
      resetProbeState();
      onOpenChange(false);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.repositories.gongfeng_inventory.create_failed));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t(($) => $.repositories.gongfeng_inventory.add_title)}</DialogTitle>
          <DialogDescription>{t(($) => $.repositories.gongfeng_inventory.add_description)}</DialogDescription>
        </DialogHeader>
        <form className="space-y-3" onSubmit={handleSubmit}>
          <label className="block space-y-1.5 text-xs">
            <span className="font-medium text-muted-foreground">
              {t(($) => $.repositories.gongfeng_inventory.url_field)}
            </span>
            <div className="flex gap-2">
              <input
                type="text"
                value={url}
                onChange={(event) => handleURLChange(event.target.value)}
                placeholder={t(($) => $.repositories.gongfeng_inventory.url_placeholder)}
                className="h-9 min-w-0 flex-1 rounded-md border bg-background px-2 font-mono text-sm outline-none focus:ring-2 focus:ring-ring"
              />
              <Button
                type="button"
                variant={probeMatches ? "secondary" : "outline"}
                size="sm"
                className="h-9 shrink-0"
                disabled={!trimmedURL || probing || saving}
                onClick={() => void handleProbe()}
              >
                {probing ? (
                  <RefreshCw className="size-3 animate-spin" />
                ) : probeMatches ? (
                  <CheckCircle2 className="size-3 text-emerald-600" />
                ) : (
                  <KeyRound className="size-3" />
                )}
                {probeMatches
                  ? t(($) => $.repositories.gongfeng_inventory.probe_done)
                  : t(($) => $.repositories.gongfeng_inventory.probe)}
              </Button>
            </div>
          </label>
          <div className="space-y-1.5 text-xs">
            <div className="font-medium text-muted-foreground">
              {t(($) => $.repositories.gongfeng_inventory.quick_fill)}
            </div>
            <div className="flex flex-wrap gap-2">
              {GONGFENG_REPO_PRESETS.map((preset) => {
                const presetProjectPath = inferProjectPathFromGongfengURL(preset.url);
                const exists = existingProjectPaths.has(presetProjectPath);
                return (
                  <Button
                    key={preset.key}
                    type="button"
                    variant="outline"
                    size="sm"
                    className="h-7 px-2 text-[11px]"
                    disabled={exists || saving || probing}
                    title={exists ? t(($) => $.repositories.gongfeng_inventory.duplicate_error) : preset.url}
                    onClick={() => handlePreset(preset.url)}
                  >
                    {preset.label}
                  </Button>
                );
              })}
            </div>
          </div>
          <label className="block space-y-1.5 text-xs">
            <span className="font-medium text-muted-foreground">
              {t(($) => $.repositories.gongfeng_inventory.default_branch_field)}
            </span>
            <select
              value={selectedBranch}
              onChange={(event) => setSelectedBranch(event.target.value)}
              disabled={!probeMatches || probing || saving}
              className="h-9 w-full rounded-md border bg-background px-2 font-mono text-sm outline-none focus:ring-2 focus:ring-ring disabled:cursor-not-allowed disabled:bg-muted disabled:text-muted-foreground"
            >
              {!probeMatches && (
                <option value="">{t(($) => $.repositories.gongfeng_inventory.default_branch_probe_required)}</option>
              )}
              {probeMatches && probe?.branches.map((branch) => (
                <option key={branch} value={branch}>
                  {branch}
                </option>
              ))}
            </select>
          </label>
          {probeError && (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
              {probeError}
            </div>
          )}
          {probeMatches && (
            <div className="rounded-md border bg-muted/20 px-3 py-2 text-[11px] text-muted-foreground">
              {t(($) => $.repositories.gongfeng_inventory.probe_success, {
                project: probe?.project_path ?? "",
                count: probe?.branches.length ?? 0,
              })}
            </div>
          )}
          <div className="flex justify-end gap-2 pt-1">
            <Button type="button" variant="outline" size="sm" onClick={() => onOpenChange(false)}>
              {t(($) => $.repositories.gongfeng_inventory.cancel)}
            </Button>
            <Button type="submit" size="sm" disabled={!canSubmit}>
              {saving
                ? t(($) => $.repositories.gongfeng_inventory.creating)
                : t(($) => $.repositories.gongfeng_inventory.create)}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function GongfengRepositoryLibraryRow({
  row,
  workspace,
  onRepoResolved,
  projectHref,
}: {
  row: RepositoryLibraryRow;
  workspace: Workspace;
  onRepoResolved: (repo: WorkspaceRepo) => void;
  projectHref: (projectId: string) => string;
}) {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const [detailsOpen, setDetailsOpen] = useState(false);
  const defaultBranch = repositoryDefaultBranch(row);
  const displayName = repositoryDisplayName(row);
  const removeBlocked = row.usages.length > 0;
  const showRemoveButton = row.inLibrary || row.usages.length > 0;

  const handleRemoveFromLibrary = async () => {
    if (removeBlocked) {
      toast.error(t(($) => $.repositories.gongfeng_inventory.remove_blocked));
      return;
    }
    const repos = workspaceRepoList(workspace).filter((repo) => repo.url !== row.url);
    try {
      const updated = await api.updateWorkspace(workspace.id, { repos });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      toast.success(t(($) => $.repositories.gongfeng_inventory.remove_success));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.repositories.gongfeng_inventory.remove_failed));
    }
  };

  return (
    <div
      className="flex flex-col gap-3 px-3 py-3 text-xs sm:flex-row sm:items-center sm:justify-between"
      data-testid="settings-gongfeng-repository-row"
    >
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex min-w-0 flex-wrap items-center gap-1.5">
          <a href={row.url} target="_blank" rel="noreferrer" className="min-w-0 truncate text-sm font-medium leading-5 hover:underline">
            {displayName}
          </a>
          <RepositoryPrimaryHealthButton
            row={row}
            workspace={workspace}
            onRepoResolved={onRepoResolved}
          />
        </div>
        <div className="min-w-0">
          <div className="min-w-0 break-all text-[12px] leading-5 text-muted-foreground sm:break-normal sm:truncate">
            {row.url}
          </div>
        </div>
        {(defaultBranch || row.inLibrary) && (
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            {defaultBranch && (
              <MetaBadge>
                {t(($) => $.repositories.gongfeng_inventory.default_branch_badge, { branch: defaultBranch })}
              </MetaBadge>
            )}
            {row.inLibrary && (
              <MetaBadge>{t(($) => $.repositories.gongfeng_inventory.library_badge)}</MetaBadge>
            )}
          </div>
        )}
      </div>
      <div className="flex shrink-0 items-center gap-1">
        <button
          type="button"
          className="inline-flex h-7 items-center justify-center gap-1 rounded-md border bg-background px-2 text-[11px] transition-colors hover:bg-accent"
          onClick={() => setDetailsOpen(true)}
        >
          <Info className="size-3.5" />
          {t(($) => $.repositories.gongfeng_inventory.details)}
        </button>
        {showRemoveButton && (
          <button
            type="button"
            className="inline-flex h-7 shrink-0 items-center justify-center gap-1 rounded-md border bg-background px-2 text-[11px] text-destructive transition-colors hover:bg-destructive/10"
            title={removeBlocked
              ? t(($) => $.repositories.gongfeng_inventory.remove_blocked)
              : t(($) => $.repositories.gongfeng_inventory.remove_aria)}
            onClick={() => void handleRemoveFromLibrary()}
          >
            <Trash2 className="size-3.5" />
            {t(($) => $.repositories.gongfeng_inventory.remove)}
          </button>
        )}
      </div>
      <RepositoryDetailsDialog
        open={detailsOpen}
        onOpenChange={setDetailsOpen}
        row={row}
        workspace={workspace}
        onRepoResolved={onRepoResolved}
        projectHref={projectHref}
      />
    </div>
  );
}

function RepositoryDetailsDialog({
  open,
  onOpenChange,
  row,
  workspace,
  onRepoResolved,
  projectHref,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  row: RepositoryLibraryRow;
  workspace: Workspace;
  onRepoResolved: (repo: WorkspaceRepo) => void;
  projectHref: (projectId: string) => string;
}) {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const [resolving, setResolving] = useState(false);
  const defaultBranch = repositoryDefaultBranch(row);
  const projectPath = repositoryProjectPath(row);
  const commitID = repositoryCommitID(row);
  const needsResolve = row.inLibrary && !row.repo?.resolve_status;

  const handleResolve = async () => {
    if (!row.inLibrary || resolving) return;
    setResolving(true);
    try {
      const resolved = await api.resolveWorkspaceRepo(workspace.id, { url: row.url });
      const repos = workspaceRepoList(workspace).map((repo) =>
        repo.url === row.url ? { ...repo, ...resolved } : repo,
      );
      onRepoResolved(resolved);
      const updated = await api.updateWorkspace(workspace.id, { repos });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      toast.success(t(($) => $.repositories.gongfeng_inventory.resolve_success));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.repositories.gongfeng_inventory.resolve_failed));
    } finally {
      setResolving(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.repositories.gongfeng_inventory.details_title)}</DialogTitle>
          <DialogDescription>{row.url}</DialogDescription>
        </DialogHeader>
        <div className="space-y-4 text-xs">
          <div className="grid gap-2 rounded-md border bg-muted/20 p-3 sm:grid-cols-3">
            <DetailItem label={t(($) => $.repositories.gongfeng_inventory.default_branch_label)}>
              {defaultBranch || "-"}
            </DetailItem>
            <DetailItem label={t(($) => $.repositories.gongfeng_inventory.project_path_label)}>
              {projectPath || "-"}
            </DetailItem>
            <DetailItem label={t(($) => $.repositories.gongfeng_inventory.commit_id_label)}>
              {commitID || "-"}
            </DetailItem>
          </div>
          {needsResolve && (
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-[11px] text-amber-900">
              <span>{t(($) => $.repositories.gongfeng_inventory.resolve_hint)}</span>
              <button
                type="button"
                className="inline-flex h-7 items-center justify-center rounded-md border border-amber-300 bg-background px-2 text-[11px] text-amber-900 hover:bg-amber-100 disabled:cursor-not-allowed disabled:opacity-50"
                disabled={resolving}
                onClick={() => void handleResolve()}
              >
                {resolving
                  ? t(($) => $.repositories.gongfeng_inventory.resolving)
                  : t(($) => $.repositories.gongfeng_inventory.resolve)}
              </button>
            </div>
          )}
          <div className="space-y-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="font-medium">{t(($) => $.repositories.gongfeng_inventory.usages_title)}</div>
            </div>
            {row.usages.length === 0 ? (
              <div className="rounded-md border border-dashed px-3 py-4 text-center text-muted-foreground">
                {t(($) => $.repositories.gongfeng_inventory.usages_empty)}
              </div>
            ) : (
              <div className="divide-y rounded-md border">
                {row.usages.map((usage) => (
                  <RepositoryUsageDetailRow
                    key={usage.id}
                    usage={usage}
                    href={projectHref(usage.project.id)}
                  />
                ))}
              </div>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function DetailItem({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0 space-y-1">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className="truncate font-mono text-[11px]">{children}</div>
    </div>
  );
}

function RepositoryPrimaryHealthButton({
  row,
  workspace,
  onRepoResolved,
}: {
  row: RepositoryLibraryRow;
  workspace: Workspace;
  onRepoResolved: (repo: WorkspaceRepo) => void;
}) {
  const { t } = useT("settings");
  const qc = useQueryClient();
  const [syncing, setSyncing] = useState(false);
  const health = deriveRepositoryHealth(row.repo ?? row.usages[0]?.resource_ref);

  const handleTestAndSyncRepository = async () => {
    if (syncing) return;
    setSyncing(true);
    try {
      const branch = repositoryDefaultBranch(row);
      const resolved = await api.resolveWorkspaceRepo(workspace.id, {
        url: row.url,
        ...(branch ? { default_branch: branch } : {}),
      });
      const exists = workspaceRepoList(workspace).some((repo) => repo.url === row.url);
      const repos = exists
        ? workspaceRepoList(workspace).map((repo) => (repo.url === row.url ? { ...repo, ...resolved } : repo))
        : [...workspaceRepoList(workspace), resolved];
      onRepoResolved(resolved);
      const updated = await api.updateWorkspace(workspace.id, { repos });
      qc.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) =>
        old?.map((ws) => (ws.id === updated.id ? updated : ws)),
      );
      toast.success(t(($) => $.repositories.gongfeng_inventory.repo_sync_success));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.repositories.gongfeng_inventory.repo_sync_failed));
    } finally {
      setSyncing(false);
    }
  };

  return (
    <RepositoryHealthButton
      health={health}
      isPending={syncing}
      onClick={() => void handleTestAndSyncRepository()}
    />
  );
}

function RepositoryUsageDetailRow({ usage, href }: { usage: GongfengResourceUsage; href: string }) {
  const { t } = useT("settings");
  const deleteResource = useDeleteProjectResource(usage.workspace_id, usage.project.id);
  const pending = deleteResource.isPending;

  const handleDelete = async () => {
    try {
      await deleteResource.mutateAsync(usage.id);
      toast.success(t(($) => $.repositories.gongfeng_inventory.detach_success));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.repositories.gongfeng_inventory.detach_failed));
    }
  };

  return (
    <div className="flex flex-col gap-2 px-3 py-2 sm:flex-row sm:items-center sm:justify-between">
      <div className="min-w-0 space-y-1">
        <AppLink href={href} className="font-medium hover:underline">
          {usage.project.title}
        </AppLink>
        <div className="flex flex-wrap gap-1 text-[11px] text-muted-foreground">
          {(usage.resource_ref.branch || usage.resource_ref.ref) && (
            <MetaBadge>{usage.resource_ref.branch || usage.resource_ref.ref}</MetaBadge>
          )}
          <span>{deriveRepositoryHealth(usage.resource_ref).summary}</span>
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-1">
        <button
          type="button"
          className="inline-flex h-7 items-center justify-center gap-1 rounded-md border px-2 text-[11px] text-destructive hover:bg-destructive/10 disabled:cursor-not-allowed disabled:opacity-50"
          aria-label={t(($) => $.repositories.gongfeng_inventory.detach)}
          title={t(($) => $.repositories.gongfeng_inventory.detach)}
          disabled={pending}
          onClick={() => void handleDelete()}
        >
          <Trash2 className="size-3.5" />
          {t(($) => $.repositories.gongfeng_inventory.detach)}
        </button>
      </div>
    </div>
  );
}

function MetaBadge({ children }: { children: ReactNode }) {
  return (
    <span className="text-[11px] leading-4 text-muted-foreground">
      {children}
    </span>
  );
}

function repositoryDefaultBranch(row: RepositoryLibraryRow): string {
  const fromLibrary = row.repo?.default_branch?.trim();
  if (fromLibrary) return fromLibrary;
  return firstNonEmpty(row.usages.map((usage) => usage.resource_ref.branch || usage.resource_ref.ref));
}

function repositoryProjectPath(row: RepositoryLibraryRow): string {
  return gongfengWorkspaceRepoProjectPath(row.repo) || inferProjectPathFromGongfengURL(row.url);
}

function gongfengWorkspaceRepoProjectPath(repo: WorkspaceRepo | undefined): string {
  const fromLibrary = repo?.project_path?.trim();
  if (fromLibrary) return fromLibrary;
  return repo?.url ? inferProjectPathFromGongfengURL(repo.url) : "";
}

function gongfengResourceProjectPath(ref: GongfengRepoResourceRef): string {
  const fromRef = ref.project_path?.trim();
  if (fromRef) return fromRef;
  return inferProjectPathFromGongfengURL(ref.url);
}

function repositoryDisplayName(row: RepositoryLibraryRow): string {
  const projectPath = repositoryProjectPath(row);
  if (projectPath) return projectPath.split("/").filter(Boolean).pop() || projectPath;
  return inferRepoNameFromURL(row.url) || row.url;
}

function repositoryCommitID(row: RepositoryLibraryRow): string {
  return firstNonEmpty([
    row.repo?.head_commit,
    row.repo?.commit_sha,
    ...row.usages.flatMap((usage) => [usage.resource_ref.head_commit, usage.resource_ref.commit_sha]),
  ]);
}

function inferProjectPathFromGongfengURL(url: string): string {
  try {
    const parsed = new URL(url);
    const parts = parsed.pathname.split("/").filter(Boolean);
    const boundary = parts.findIndex((part) =>
      ["commits", "commit", "tree", "blob", "merge_requests"].includes(part),
    );
    return (boundary > 0 ? parts.slice(0, boundary) : parts).join("/");
  } catch {
    return "";
  }
}

function inferRepoNameFromURL(url: string): string {
  const projectPath = inferProjectPathFromGongfengURL(url);
  if (projectPath) return projectPath.split("/").filter(Boolean).pop() || "";
  return "";
}

function firstNonEmpty(values: Array<string | undefined>): string {
  return values.map((value) => value?.trim()).find((value): value is string => Boolean(value)) ?? "";
}

type RepositoryHealth = {
  label: string;
  summary: string;
  tone: "success" | "warning" | "danger" | "muted";
};

function RepositoryHealthButton({
  disabled,
  health,
  isPending,
  onClick,
}: {
  disabled?: boolean;
  health: RepositoryHealth;
  isPending: boolean;
  onClick: () => void;
}) {
  const tone = healthTone(health.tone);
  const Icon = isPending ? RefreshCw : healthIcon(health.tone);
  const label = disabled ? `工蜂仓库已停用，${health.summary}` : `测试并同步工蜂仓库，${health.summary}`;
  return (
    <button
      type="button"
      className={`inline-flex size-4 items-center justify-center rounded-full border transition-colors ${tone} disabled:cursor-not-allowed disabled:opacity-60`}
      aria-label={label}
      title={label}
      disabled={disabled || isPending}
      onClick={onClick}
    >
      <Icon className={`size-3 ${isPending ? "animate-spin" : ""}`} />
      <span className="sr-only">{health.label}</span>
    </button>
  );
}

type RepositoryHealthSource = {
  connection_status?: string;
  sync_status?: string;
  test_status?: string;
  disabled?: boolean;
};

function deriveRepositoryHealth(ref?: RepositoryHealthSource): RepositoryHealth {
  const connection = ref?.disabled ? "disabled" : ref?.connection_status;
  const sync = ref?.disabled ? "disabled" : ref?.sync_status;
  const test = ref?.disabled ? "disabled" : ref?.test_status;
  const statuses = [connection, sync, test].filter((value): value is string => Boolean(value));
  const summary = [
    `连接: ${statusLabel(connection || "pending_verification")}`,
    `同步: ${statusLabel(sync || "pending_verification")}`,
    `测试: ${statusLabel(test || "pending_verification")}`,
  ].join(" · ");

  if (ref?.disabled || statuses.includes("disabled")) {
    return { label: "已停用", summary, tone: "muted" };
  }
  if (statuses.some((value) => ["failed", "error", "unreachable", "invalid_url"].includes(value))) {
    return { label: "测试失败", summary, tone: "danger" };
  }
  if (statuses.includes("auth_required")) {
    return { label: "需要凭据", summary, tone: "warning" };
  }
  if (statuses.length > 0 && statuses.every(isPassingStatus)) {
    return { label: "通过", summary, tone: "success" };
  }
  return { label: "待验证", summary, tone: "warning" };
}

function isPassingStatus(value: string): boolean {
  return ["ok", "passed", "connected", "synced", "reachable", "credential_backed"].includes(value);
}

function healthIcon(tone: RepositoryHealth["tone"]) {
  switch (tone) {
    case "success":
      return CheckCircle2;
    case "warning":
      return KeyRound;
    case "danger":
      return AlertCircle;
    case "muted":
      return Ban;
  }
}

function healthTone(tone: RepositoryHealth["tone"]): string {
  switch (tone) {
    case "success":
      return "border-emerald-200 bg-emerald-50 text-emerald-700 hover:bg-emerald-100";
    case "warning":
      return "border-amber-300 bg-amber-50 text-amber-800 hover:bg-amber-100";
    case "danger":
      return "border-red-200 bg-red-50 text-red-700 hover:bg-red-100";
    case "muted":
      return "border-border bg-muted text-muted-foreground";
  }
}

function statusLabel(value: string): string {
  switch (value) {
    case "ok":
    case "passed":
    case "connected":
    case "synced":
      return "通过";
    case "reachable":
      return "可达";
    case "credential_backed":
      return "已配置凭据";
    case "auth_required":
      return "需要凭据";
    case "unreachable":
      return "不可达";
    case "invalid_url":
      return "地址无效";
    case "failed":
    case "error":
      return "失败";
    case "disabled":
      return "已停用";
    case "pending_verification":
      return "待验证";
    case "seeded_for_remediation":
      return "已建档";
    case "requires_real_click_acceptance":
      return "待 UI 验收";
    default:
      return value.replace(/_/g, " ");
  }
}
