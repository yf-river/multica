"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ChevronRight,
  FolderGit,
  FolderOpen,
  GitBranch,
  Loader2,
  Pencil,
  Plus,
  RefreshCw,
  Search,
  Trash2,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { toast } from "sonner";
import {
  projectResourcesOptions,
  useCreateProjectResource,
  useDeleteProjectResource,
  useSyncProjectResource,
  useUpdateProjectResource,
} from "@multica/core/projects";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import type {
  GongfengRepoResourceRef,
  LocalDirectoryResourceRef,
  ProjectResource,
  WorkspaceRepo,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from "@multica/ui/components/ui/tooltip";
import {
  useLocalDaemonStatus,
} from "../../platform";
import { useT } from "../../i18n";
import {
  buildGongfengResourceRefFromWorkspaceRepo,
  isGongfengRepoURL,
  normalizeRepoSearch,
  workspaceRepoSearchText,
  WorkspaceRepoDisplayText,
} from "./workspace-repo-resource";

// Project Resources sidebar section.
//
// Type-dispatched at the row + add-flow level. Add a new resource_type by:
//   (1) extending the server validator
//   (2) extending ProjectResourceType in @multica/core/types
//   (3) adding a render case in ResourceRow and an add-control here
function isGongfengRef(r: ProjectResource): r is ProjectResource & {
  resource_ref: GongfengRepoResourceRef;
} {
  return r.resource_type === "gongfeng_repo";
}

function isLocalDirectoryRef(r: ProjectResource): r is ProjectResource & {
  resource_ref: LocalDirectoryResourceRef;
} {
  return r.resource_type === "local_directory";
}

export function ProjectResourcesSection({ projectId }: { projectId: string }) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const workspace = useCurrentWorkspace();
  const daemonStatus = useLocalDaemonStatus();
  const [open, setOpen] = useState(true);
  const [addOpen, setAddOpen] = useState(false);
  const [repoSearch, setRepoSearch] = useState("");

  const { data: resources = [] } = useQuery(
    projectResourcesOptions(wsId, projectId),
  );
  const createResource = useCreateProjectResource(wsId, projectId);
  const updateResource = useUpdateProjectResource(wsId, projectId);
  const deleteResource = useDeleteProjectResource(wsId, projectId);
  const syncResource = useSyncProjectResource(wsId, projectId);

  const localDaemonId = daemonStatus.daemonId;

  const attachedUrls = new Set(
    resources.filter(isGongfengRef).map((r) => r.resource_ref.url),
  );

  const repoQuery = repoSearch.trim().toLowerCase();
  const normalizedRepoQuery = normalizeRepoSearch(repoSearch);
  const filteredRepos =
    workspace?.repos
      ?.filter((repo) => isGongfengRepoURL(repo.url))
      .filter((repo) => {
        if (!repoQuery) return true;
        const searchText = workspaceRepoSearchText(repo);
        return (
          searchText.includes(repoQuery) ||
          normalizeRepoSearch(searchText).includes(normalizedRepoQuery)
        );
      }) ?? [];

  const handleAttachGongfeng = async (url: string, repo?: WorkspaceRepo) => {
    try {
      await createResource.mutateAsync({
        resource_type: "gongfeng_repo",
        resource_ref: buildGongfengResourceRefFromWorkspaceRepo(url, repo),
      });
      toast.success(t(($) => $.resources.toast_gongfeng_attached));
    } catch (err) {
      const msg = err instanceof Error ? err.message : t(($) => $.resources.toast_attach_failed);
      toast.error(msg);
    }
  };

  const handleRemove = async (resource: ProjectResource) => {
    try {
      await deleteResource.mutateAsync(resource.id);
      toast.success(t(($) => $.resources.toast_removed));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.resources.toast_remove_failed),
      );
    }
  };

  const handleSync = async (resource: ProjectResource) => {
    try {
      await syncResource.mutateAsync(resource.id);
      toast.success(t(($) => $.resources.toast_synced));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.resources.toast_sync_failed),
      );
    }
  };

  const handleRenameLocalDirectory = async (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
    nextLabel: string,
  ) => {
    const trimmed = nextLabel.trim();
    const previous = resource.resource_ref.label ?? resource.label ?? "";
    if (trimmed === previous.trim()) return;
    try {
      await updateResource.mutateAsync({
        resourceId: resource.id,
        data: {
          resource_ref: {
            ...resource.resource_ref,
            label: trimmed,
          },
        },
      });
      toast.success(t(($) => $.resources.toast_local_renamed));
    } catch (err) {
      const msg =
        err instanceof Error
          ? err.message
          : t(($) => $.resources.toast_local_rename_failed);
      toast.error(msg);
    }
  };

  return (
    <div>
      <button
        type="button"
        className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-xs font-medium transition-colors mb-2 hover:bg-accent/70 ${open ? "" : "text-muted-foreground hover:text-foreground"}`}
        onClick={() => setOpen(!open)}
      >
        {t(($) => $.resources.section_header)}
        <ChevronRight
          className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`}
        />
      </button>
      {open && (
        <div className="pl-2 space-y-1.5">
          {resources.length === 0 && (
            <p className="text-xs text-muted-foreground">
              {t(($) => $.resources.empty)}
            </p>
          )}
          {resources.length > 0 && (
            <div className="max-h-64 space-y-1.5 overflow-y-auto pr-1">
              {resources.map((resource) => (
                <ResourceRow
                  key={resource.id}
                  resource={resource}
                  localDaemonId={localDaemonId}
                  canEdit={false}
                  onRemove={() => handleRemove(resource)}
                  onSync={() => handleSync(resource)}
                  pendingAction={
                    syncResource.isPending ||
                    deleteResource.isPending
                  }
                  onRenameLocalDirectory={handleRenameLocalDirectory}
                />
              ))}
            </div>
          )}
          <Popover
            open={addOpen}
            onOpenChange={(v) => {
              setAddOpen(v);
              if (!v) setRepoSearch("");
            }}
          >
            <PopoverTrigger
              render={
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-xs text-muted-foreground hover:text-foreground"
                >
                  <Plus className="size-3" />
                  {t(($) => $.resources.add_button)}
                </Button>
              }
            />
            <PopoverContent align="start" className="w-72 p-2 space-y-2">
              <div className="text-xs font-medium text-muted-foreground">
                {t(($) => $.resources.popover_title)}
              </div>
              {workspace?.repos && workspace.repos.length > 0 && (
                <>
                  <div className="relative">
                    <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                    <input
                      type="text"
                      value={repoSearch}
                      onChange={(e) => setRepoSearch(e.target.value)}
                      aria-label={t(($) => $.resources.repos_search_placeholder)}
                      placeholder={t(($) => $.resources.repos_search_placeholder)}
                      className="h-8 w-full rounded-md border bg-transparent pl-7 pr-2 text-xs outline-none placeholder:text-muted-foreground focus-visible:ring-1 focus-visible:ring-ring"
                    />
                  </div>
                  <div className="max-h-48 space-y-1 overflow-y-auto">
                    {filteredRepos.length === 0 && repoQuery && (
                      <p className="py-2 text-center text-xs text-muted-foreground">
                        {t(($) => $.resources.repos_search_empty)}
                      </p>
                    )}
                    {filteredRepos.map((repo) => {
                      const isAttached = attachedUrls.has(repo.url);
                      const isDisabled = isAttached || createResource.isPending;
                      return (
                        // Use aria-disabled instead of the native `disabled` attribute so
                        // hover events still reach the tooltip trigger on attached rows
                        // (browsers suppress pointer events on disabled form controls).
                        <button
                          key={repo.url}
                          type="button"
                          aria-disabled={isDisabled}
                          onClick={async () => {
                            if (isDisabled) return;
                            await handleAttachGongfeng(repo.url, repo);
                            setAddOpen(false);
                          }}
                          className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-xs text-left hover:bg-accent transition-colors aria-disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:hover:bg-transparent"
                        >
                          <FolderGit className="size-3.5 shrink-0 text-muted-foreground" />
                          <WorkspaceRepoDisplayText repo={repo} />
                          {repo.default_branch && (
                            <span className="shrink-0 rounded border px-1 font-mono text-[10px] text-muted-foreground">
                              {repo.default_branch}
                            </span>
                          )}
                          {isAttached && (
                            <span className="shrink-0 text-[10px] text-muted-foreground">
                              {t(($) => $.resources.attached_badge)}
                            </span>
                          )}
                        </button>
                      );
                    })}
                  </div>
                </>
              )}
            </PopoverContent>
          </Popover>
        </div>
      )}
    </div>
  );
}

interface ResourceRowProps {
  resource: ProjectResource;
  localDaemonId: string | null;
  canEdit: boolean;
  onRemove: () => void;
  onSync: () => void;
  pendingAction: boolean;
  onRenameLocalDirectory: (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
    nextLabel: string,
  ) => Promise<void>;
}

function ResourceRow({
  resource,
  localDaemonId,
  canEdit,
  onRemove,
  onSync,
  pendingAction,
  onRenameLocalDirectory,
}: ResourceRowProps) {
  const { t } = useT("projects");
  if (isLocalDirectoryRef(resource)) {
    return (
      <LocalDirectoryRow
        resource={resource}
        localDaemonId={localDaemonId}
        canEdit={canEdit}
        onRemove={onRemove}
        onRename={onRenameLocalDirectory}
      />
    );
  }

  if (isGongfengRef(resource)) {
    const ref = resource.resource_ref;
    const display =
      resource.label ||
      ref.title ||
      [ref.project_path, ref.ref].filter(Boolean).join(" @ ") ||
      ref.url;
    const branch = ref.branch || ref.ref || "";
    const commit = ref.commit_sha || ref.head_commit || "";
    const detail = [ref.resource_kind, branch].filter(Boolean).join(": ");
    const statusItems = [
      { label: "连接", value: ref.connection_status },
      { label: "同步", value: ref.sync_status },
      { label: "测试", value: ref.test_status },
    ].filter((item): item is { label: string; value: string } => Boolean(item.value));
    return (
      <div className="flex items-start gap-2 rounded-md px-1.5 py-1 text-xs group hover:bg-accent/40">
        <GitBranch className="mt-0.5 size-3.5 text-muted-foreground shrink-0" />
        <Tooltip>
          <TooltipTrigger
            render={
              <div className="min-w-0 flex-1 space-y-1">
                <a
                  href={ref.url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="block truncate font-medium hover:underline"
                  data-testid="gongfeng-resource-link"
                >
                  {display}
                </a>
                <div className="flex flex-wrap items-center gap-1 text-[10px] leading-4 text-muted-foreground">
                  {branch && <span className="rounded border px-1 font-mono">{branch}</span>}
                  {commit && <span className="rounded border px-1 font-mono">{shortCommit(commit)}</span>}
                  {statusItems.map((item) => (
                    <span
                      key={`${item.label}:${item.value}`}
                      className="rounded border px-1"
                      data-testid="gongfeng-resource-status"
                    >
                      {item.label}: {statusLabel(item.value)}
                    </span>
                  ))}
                </div>
              </div>
            }
          />
          <TooltipContent side="top">
            <div className="space-y-0.5 text-[11px]">
              <div className="font-mono">{ref.project_path}</div>
              {detail && <div className="text-muted-foreground">{detail}</div>}
              {commit && <div className="font-mono text-muted-foreground">{commit}</div>}
              {statusItems.map((item) => (
                <div key={item.label} className="text-muted-foreground">
                  {item.label}: {statusLabel(item.value)}
                </div>
              ))}
              <div className="text-muted-foreground">Gongfeng</div>
            </div>
          </TooltipContent>
        </Tooltip>
        <div className="flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100">
          <IconAction
            title={t(($) => $.resources.sync_tooltip)}
            disabled={pendingAction}
            onClick={onSync}
            icon={pendingAction ? Loader2 : RefreshCw}
            spin={pendingAction}
          />
          <IconAction
            title={t(($) => $.resources.remove_tooltip)}
            disabled={pendingAction}
            onClick={onRemove}
            icon={Trash2}
          />
        </div>
      </div>
    );
  }

  return (
    <div className="flex items-center gap-2 text-xs text-muted-foreground">
      <span className="truncate flex-1">
        {resource.label || t(($) => $.resources.legacy_resource_label)}
      </span>
      <button
        type="button"
        onClick={onRemove}
        className="rounded-sm p-0.5 hover:bg-accent"
        title={t(($) => $.resources.remove_tooltip)}
      >
        <Trash2 className="size-3" />
      </button>
    </div>
  );
}

function IconAction({
  title,
  icon: Icon,
  onClick,
  disabled,
  spin,
}: {
  title: string;
  icon: LucideIcon;
  onClick: () => void;
  disabled?: boolean;
  spin?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="rounded-sm p-0.5 hover:bg-accent disabled:cursor-not-allowed disabled:opacity-40"
      title={title}
      aria-label={title}
    >
      <Icon className={`size-3 text-muted-foreground ${spin ? "animate-spin" : ""}`} />
    </button>
  );
}

function shortCommit(value: string): string {
  return value.length > 12 ? value.slice(0, 12) : value;
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
    case "auth_required":
      return "需要凭据";
    case "unreachable":
      return "不可达";
    case "invalid_url":
      return "地址无效";
    case "failed":
    case "error":
      return "失败";
    case "pending_verification":
      return "待验证";
    case "needs_test":
      return "待测试";
    case "needs_sync":
      return "待同步";
    case "not_run":
      return "未运行";
    case "seeded_for_remediation":
      return "已建档";
    case "requires_real_click_acceptance":
      return "待 UI 验收";
    default:
      return value.replace(/_/g, " ");
  }
}

interface LocalDirectoryRowProps {
  resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef };
  localDaemonId: string | null;
  canEdit: boolean;
  onRemove: () => void;
  onRename: (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
    nextLabel: string,
  ) => Promise<void>;
}

function LocalDirectoryRow({
  resource,
  localDaemonId,
  canEdit,
  onRemove,
  onRename,
}: LocalDirectoryRowProps) {
  const { t } = useT("projects");
  const ref = resource.resource_ref;
  const display = (ref.label || resource.label || ref.local_path).trim() ||
    ref.local_path;
  const isForeignDaemon =
    localDaemonId !== null && ref.daemon_id !== localDaemonId;
  const isLocalUnknown = localDaemonId === null;
  // "disabled" in the spec sense — visual de-emphasis + no chat hint, and
  // rename is hidden on foreign / unknown-daemon rows because the label
  // belongs to the owning device. Delete stays available so the user can
  // drop a stale registration from any device.
  const mismatch = isForeignDaemon || isLocalUnknown;

  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(display);

  const startEdit = () => {
    setDraft(display);
    setEditing(true);
  };
  const commit = async () => {
    setEditing(false);
    await onRename(resource, draft);
  };
  const cancel = () => {
    setEditing(false);
    setDraft(display);
  };

  return (
    <div
      className={`flex items-center gap-2 text-xs group ${
        mismatch ? "opacity-60" : ""
      }`}
    >
      <FolderOpen className="size-3.5 text-muted-foreground shrink-0" />
      {editing ? (
        <input
          autoFocus
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={() => void commit()}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              void commit();
            } else if (e.key === "Escape") {
              e.preventDefault();
              cancel();
            }
          }}
          className="flex-1 min-w-0 rounded-sm border bg-transparent px-1 py-0.5 text-xs outline-none focus-visible:ring-1 focus-visible:ring-ring"
          aria-label={t(($) => $.resources.local_rename_label)}
        />
      ) : (
        <Tooltip>
          <TooltipTrigger
            render={
              <span className="truncate flex-1">{display}</span>
            }
          />
          <TooltipContent side="top">
            <div className="space-y-0.5 text-[11px]">
              <div className="font-mono">{ref.local_path}</div>
              {mismatch && (
                <div className="text-muted-foreground">
                  {isLocalUnknown
                    ? t(($) => $.resources.local_no_daemon_tooltip)
                    : t(($) => $.resources.local_other_machine_tooltip)}
                </div>
              )}
              <div className="text-muted-foreground">
                {t(($) => $.resources.local_compat_tooltip)}
              </div>
            </div>
          </TooltipContent>
        </Tooltip>
      )}
      {!editing && (
        <span className="shrink-0 rounded border px-1 text-[10px] text-muted-foreground">
          {t(($) => $.resources.local_compat_badge)}
        </span>
      )}
      {canEdit && !mismatch && !editing && (
        <button
          type="button"
          onClick={startEdit}
          className="opacity-0 group-hover:opacity-100 transition-opacity rounded-sm p-0.5 hover:bg-accent"
          title={t(($) => $.resources.local_rename_tooltip)}
        >
          <Pencil className="size-3 text-muted-foreground" />
        </button>
      )}
      <button
        type="button"
        onClick={onRemove}
        className="opacity-0 group-hover:opacity-100 transition-opacity rounded-sm p-0.5 hover:bg-accent"
        title={t(($) => $.resources.remove_tooltip)}
      >
        <Trash2 className="size-3 text-muted-foreground" />
      </button>
    </div>
  );
}
