"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ChevronRight,
  FolderGit,
  FolderOpen,
  GitBranch,
  Pencil,
  Plus,
  Search,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import {
  projectResourcesOptions,
  useCreateProjectResource,
  useDeleteProjectResource,
  useUpdateProjectResource,
} from "@multica/core/projects";
import { useWorkspaceId } from "@multica/core/hooks";
import { repositoryCenterOptions,repositoryProvider } from "@multica/core/workspace/repository-queries";
import type {
  GithubRepoResourceRef,
  GongfengRepoResourceRef,
  LocalDirectoryExecutionMode,
  LocalDirectoryResourceRef,
  ProjectResource,
} from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
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
  LocalDirectoryModeDialog,
  type WorktreeUnavailableReason,
} from "./local-directory-mode-dialog";
import { localDirectoryLabel } from "./local-directory-label";
import { useT } from "../../i18n";
import { ProjectRepositoryBranch } from "../../settings/components/repository-configuration";
import { githubShortLabel } from "../../common/github-url";

// 项目详情侧栏里的「资源」小节：列出并管理挂在项目上的外部仓库 / 本地目录 /
// 工蜂仓库等资源（添加、改名、移除、切换执行模式）。
//
// 按类型分派发生在「行渲染」与「添加流程」两级。新增一种 resource_type 需要：
//   (1) 扩展服务端校验器
//   (2) 扩展 @multica/core/types 里的 ProjectResourceType
//   (3) 在 ResourceRow 加一个渲染分支、在这里加对应的添加控件
/** 判断是否是 GitHub 仓库类型的资源。 */
function isGithubRef(r: ProjectResource): r is ProjectResource & {
  resource_ref: GithubRepoResourceRef;
} {
  return r.resource_type === "github_repo";
}

/** 判断是否是本地目录类型的资源。 */
function isLocalDirectoryRef(r: ProjectResource): r is ProjectResource & {
  resource_ref: LocalDirectoryResourceRef;
} {
  return r.resource_type === "local_directory";
}
/** 判断是否是工蜂（腾讯内部 Git）仓库类型的资源。 */
function isGongfengRef(r: ProjectResource): r is ProjectResource & { resource_ref: GongfengRepoResourceRef } {
  return r.resource_type === "gongfeng_repo";
}

/**
 * 从存下来的 ref 读出执行模式。缺失或未被识别的值一律按 in_place 处理，
 * 与服务端一致：该字段可选，而较新客户端写入的模式在这里不能渲染成别的什么，
 * 只能落回保守的默认值。
 */
function executionModeOf(
  ref: LocalDirectoryResourceRef,
): LocalDirectoryExecutionMode {
  return ref.execution_mode === "worktree" ? "worktree" : "in_place";
}

/** 对已有本地目录资源的、待确认的模式修改。 */
type ModeDialogState = {
  path: string;
  mode: LocalDirectoryExecutionMode;
  /** 浏览器无法检查远程路径；由 daemon 来校验它是不是 Git 仓库。 */
  isGitRepo: boolean | undefined;
  /** 编辑已存在资源时设置；新增资源时缺省。 */
  resource?: ProjectResource & { resource_ref: LocalDirectoryResourceRef };
};

/**
 * 项目资源区：展示已关联的仓库/目录行，并提供「添加 GitHub 仓库 / 粘贴自定义
 * URL / 关联工蜂仓库」入口与本地目录的改名、模式编辑、移除操作。
 */
export function ProjectResourcesSection({ projectId }: { projectId: string }) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const {data:repositoryCenter}=useQuery(repositoryCenterOptions(wsId));
  const workspace={repos:repositoryCenter?.repos??[]};
  const [open, setOpen] = useState(true);
  const [addOpen, setAddOpen] = useState(false);
  const [repoSearch, setRepoSearch] = useState("");
  const [gongfengURL, setGongfengURL] = useState("");
  const [modeDialog, setModeDialog] = useState<ModeDialogState | null>(null);
  const [modeSaving, setModeSaving] = useState(false);
  const [modeError, setModeError] = useState<string | null>(null);

  const { data: resources = [] } = useQuery(
    projectResourcesOptions(wsId, projectId),
  );
  const createResource = useCreateProjectResource(wsId, projectId);
  const updateResource = useUpdateProjectResource(wsId, projectId);
  const deleteResource = useDeleteProjectResource(wsId, projectId);

  const attachedUrls = new Set(
    resources.filter(r=>isGithubRef(r)||isGongfengRef(r)).map((r) => (r.resource_ref as GithubRepoResourceRef).url),
  );

  const repoQuery = repoSearch.trim().toLowerCase();
  const filteredRepos =
    workspace?.repos?.filter((repo) => repo.url.toLowerCase().includes(repoQuery)) ?? [];

  const handleAttach = async (url: string) => {
    try {
      await createResource.mutateAsync({
        resource_type: repositoryProvider(workspace.repos.find(r=>r.url===url)??{url})==="gongfeng"?"gongfeng_repo":"github_repo",
        resource_ref: { url, repository_id:workspace.repos.find(r=>r.url===url)?.id },
      });
      toast.success(t(($) => $.resources.toast_attached));
    } catch (err) {
      const msg = err instanceof Error ? err.message : t(($) => $.resources.toast_attach_failed);
      toast.error(msg);
    }
  };

  const handleAttachGongfeng = async () => {
    const url = gongfengURL.trim();
    if (!url) return;
    const projectPath = gongfengProjectPath(url);
    try {
      await createResource.mutateAsync({
        resource_type: "gongfeng_repo",
        resource_ref: { provider: "gongfeng", url, project_path: projectPath },
        label: projectPath || "工蜂仓库",
      });
      setGongfengURL("");
      setAddOpen(false);
      toast.success("工蜂仓库已关联");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "工蜂仓库关联失败");
    }
  };

  const handleConfirmMode = async (mode: LocalDirectoryExecutionMode) => {
    if (!modeDialog?.resource || modeSaving) return;
    setModeSaving(true);
    setModeError(null);
    try {
      const ref = modeDialog.resource.resource_ref;
      if (executionModeOf(ref) === mode) {
        setModeDialog(null);
        return;
      }
      await updateResource.mutateAsync({
        resourceId: modeDialog.resource.id,
        data: {
          // 先展开原 ref，保证编辑后其它字段都在——服务端整块替换 ref，不会深合并。
          resource_ref: { ...ref, execution_mode: mode },
        },
      });
      toast.success(t(($) => $.resources.toast_local_mode_updated));
      setModeDialog(null);
    } catch (err) {
      // 保留弹窗并把原因内联展示：最可能的失败是服务端对 daemon 版本的门槛，
      // 直接关弹窗只会留个 toast，用户无从处理。
      setModeError(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.resources.toast_local_mode_update_failed),
      );
    } finally {
      setModeSaving(false);
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

  const handleRenameLocalDirectory = async (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
    nextLabel: string,
  ) => {
    const trimmed = nextLabel.trim();
    if (trimmed === localDirectoryLabel(resource)) return;
    try {
      // 只改顶层 label——改名绝不能重新发 resource_ref。
      //
      // 服务端会把它能解析到的 ref 整体替换，所以如果服务端不认识某个 ref 字段，
      // 它会丢弃该字段然后回 200。在回滚到 v0.4.25 以下（在运行时保持当前版本时
      // 这是文档支持的）的后端上，「给文件夹改名」就会变成「悄悄忘掉这个文件夹
      // 被隔离过」，下一个任务就直接去改工作副本了（#7113）。省略 ref 能让存储的
      // ref 在所有服务端版本上都保持原样——这也正是任何无关编辑都不该重发它的原因。
      await updateResource.mutateAsync({
        resourceId: resource.id,
        data: { label: trimmed },
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
        className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors mb-2 hover:bg-accent/70 ${open ? "" : "text-muted-foreground hover:text-foreground"}`}
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
            <p className="text-caption text-muted-foreground">
              {t(($) => $.resources.empty)}
            </p>
          )}
          {resources.length > 0 && (
            <div className="max-h-64 space-y-1.5 overflow-y-auto pr-1">
              {resources.map((resource) => (
                <div key={resource.id}><ResourceRow
                  key={resource.id}
                  resource={resource}
                  onRemove={() => handleRemove(resource)}
                  onRenameLocalDirectory={handleRenameLocalDirectory}
                  onEditLocalDirectoryMode={(target) => {
                    setModeError(null);
                    setModeDialog({
                      path: target.resource_ref.local_path,
                      mode: executionModeOf(target.resource_ref),
                      // 路径已经保存过了；所选模式能不能跑，权威是 daemon。
                      isGitRepo: undefined,
                      resource: target,
                    });
                  }}
                />
                <ProjectRepositoryBranch key={resource.id+":"+String((resource.resource_ref as GithubRepoResourceRef).project_ref)} wsId={wsId} projectId={projectId} resource={resource}/>
                </div>
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
                  className="h-7 px-2 text-caption text-muted-foreground hover:text-foreground"
                >
                  <Plus className="size-3" />
                  {t(($) => $.resources.add_button)}
                </Button>
              }
            />
            <PopoverContent align="start" className="w-72 p-2 space-y-2">
              <div className="text-caption font-medium text-muted-foreground">
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
                      className="h-8 w-full rounded-md border bg-transparent pl-7 pr-2 text-caption outline-none placeholder:text-muted-foreground focus-visible:ring-1 focus-visible:ring-ring"
                    />
                  </div>
                  <div className="max-h-48 space-y-1 overflow-y-auto">
                    {filteredRepos.length === 0 && repoQuery && (
                      <p className="py-2 text-center text-caption text-muted-foreground">
                        {t(($) => $.resources.repos_search_empty)}
                      </p>
                    )}
                    {filteredRepos.map((repo) => {
                      const isAttached = attachedUrls.has(repo.url);
                      const isDisabled = isAttached || createResource.isPending;
                      return (
                        // 用 aria-disabled 而不是原生 `disabled` 属性：原生禁用会
                        // 吞掉指针事件，导致「已关联」行上的 tooltip 触发器收不到
                        // hover（浏览器会抑制禁用表单控件的指针事件）。
                        <button
                          key={repo.url}
                          type="button"
                          aria-disabled={isDisabled}
                          onClick={async () => {
                            if (isDisabled) return;
                            await handleAttach(repo.url);
                            setAddOpen(false);
                          }}
                          className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-caption text-left hover:bg-accent transition-colors aria-disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:hover:bg-transparent"
                        >
                          <FolderGit className="size-3.5" />
                          <Tooltip>
                            <TooltipTrigger
                              render={
                                <span className="truncate flex-1">{githubShortLabel(repo.url)}</span>
                              }
                            />
                            <TooltipContent side="top">{repo.url}</TooltipContent>
                          </Tooltip>
                          {isAttached && (
                            <span className="text-micro text-muted-foreground">
                              {t(($) => $.resources.attached_badge)}
                            </span>
                          )}
                        </button>
                      );
                    })}
                  </div>
                </>
              )}
              <CustomRepoForm
                onSubmit={async (url) => {
                  await handleAttach(url);
                  setAddOpen(false);
                }}
              />
              <div className="space-y-2 border-t border-surface-border pt-2">
                <div className="text-caption font-medium">
                  {t(($) => $.resources.gongfeng_title)}
                </div>
                <input
                  type="url"
                  value={gongfengURL}
                  onChange={(event) => setGongfengURL(event.target.value)}
                  placeholder="https://git.code.tencent.com/组织/项目"
                  className="h-8 w-full rounded-md border bg-transparent px-2 text-caption outline-none placeholder:text-muted-foreground focus-visible:ring-1 focus-visible:ring-ring"
                />
                <Button size="sm" className="w-full" disabled={!gongfengURL.trim() || createResource.isPending} onClick={() => void handleAttachGongfeng()}>
                  {t(($) => $.resources.gongfeng_attach)}
                </Button>
              </div>
            </PopoverContent>
          </Popover>
        </div>
      )}
      {modeDialog && (
        <LocalDirectoryModeDialog
          open
          onOpenChange={(next) => {
            if (!next) {
              setModeDialog(null);
              setModeError(null);
            }
          }}
          path={modeDialog.path}
          value={modeDialog.mode}
          unavailableReason={worktreeUnavailableReason(modeDialog.isGitRepo)}
          errorMessage={modeError ?? undefined}
          saving={modeSaving}
          confirmLabel={t(($) => $.resources.mode_save)}
          onConfirm={(mode) => void handleConfirmMode(mode)}
        />
      )}
    </div>
  );
}

/** 从工蜂仓库 URL 里解析出「组织/项目」路径；不是工蜂域名时返回空串。 */
function gongfengProjectPath(rawURL: string): string {
  try {
    const parsed = new URL(rawURL);
    if (!parsed.hostname.toLowerCase().includes("git.code.tencent.com")) return "";
    const parts = parsed.pathname.split("/").filter(Boolean);
    const stop = parts.findIndex((part) => ["-", "tree", "blob", "merge_requests"].includes(part));
    return (stop >= 0 ? parts.slice(0, stop) : parts).join("/");
  } catch {
    return "";
  }
}

/**
 * worktree 选项当前受什么阻碍（如果有）。
 *
 * 所选执行模式能否运行，权威仍是 daemon。已知不是仓库的路径不能用 worktree；
 * 未知路径保持可用，这样已有资源能任浏览器打开编辑。
 */
function worktreeUnavailableReason(
  isGitRepo: boolean | undefined,
): WorktreeUnavailableReason | undefined {
  if (isGitRepo === false) return "not_git";
  return undefined;
}

interface ResourceRowProps {
  resource: ProjectResource;
  onRemove: () => void;
  onRenameLocalDirectory: (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
    nextLabel: string,
  ) => Promise<void>;
  onEditLocalDirectoryMode: (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
  ) => void;
}

/** 单条资源行：按类型分别渲染成 GitHub 仓库链接 / 本地目录行 / 工蜂仓库链接。 */
function ResourceRow({
  resource,
  onRemove,
  onRenameLocalDirectory,
  onEditLocalDirectoryMode,
}: ResourceRowProps) {
  const { t } = useT("projects");
  if (isGithubRef(resource)) {
    const ref = resource.resource_ref;
    const display = resource.label || (ref.ref ? `${githubShortLabel(ref.url)} @ ${ref.ref}` : githubShortLabel(ref.url));
    const tooltip = ref.ref ? `${ref.url}\nref: ${ref.ref}` : ref.url;
    return (
      <div className="flex items-center gap-2 text-caption group">
        <FolderGit className="size-3.5 text-muted-foreground shrink-0" />
        <Tooltip>
          <TooltipTrigger
            render={
              <a
                href={ref.url}
                target="_blank"
                rel="noopener noreferrer"
                className="truncate flex-1 hover:underline"
              >
                {display}
              </a>
            }
          />
          <TooltipContent side="top" className="whitespace-pre-line">{tooltip}</TooltipContent>
        </Tooltip>
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

  if (isLocalDirectoryRef(resource)) {
    return (
      <LocalDirectoryRow
        resource={resource}
        onRemove={onRemove}
        onRename={onRenameLocalDirectory}
        onEditMode={onEditLocalDirectoryMode}
      />
    );
  }

  if (isGongfengRef(resource)) {
    const ref = resource.resource_ref;
    return <div className="flex items-center gap-2 text-caption group"><FolderGit className="size-3.5 text-muted-foreground shrink-0" /><a href={ref.url} target="_blank" rel="noopener noreferrer" className="truncate flex-1 hover:underline">{resource.label || ref.project_path || ref.url}</a><button type="button" onClick={onRemove} className="rounded-sm p-0.5 hover:bg-accent" title={t(($) => $.resources.remove_tooltip)}><Trash2 className="size-3" /></button></div>;
  }

  return (
    <div className="flex items-center gap-2 text-caption text-muted-foreground">
      <span className="truncate flex-1">
        {resource.label || resource.resource_type}
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

interface LocalDirectoryRowProps {
  resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef };
  onRemove: () => void;
  onRename: (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
    nextLabel: string,
  ) => Promise<void>;
  onEditMode: (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
  ) => void;
}

/** 本地目录资源的行：可改名、可编辑执行模式、可移除；worktree 模式常显徽标。 */
function LocalDirectoryRow({
  resource,
  onRemove,
  onRename,
  onEditMode,
}: LocalDirectoryRowProps) {
  const { t } = useT("projects");
  const ref = resource.resource_ref;
  const mode = executionModeOf(ref);
  const display = localDirectoryLabel(resource);
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
      className="flex items-center gap-2 text-caption group"
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
          className="flex-1 min-w-0 rounded-sm border bg-transparent px-1 py-0.5 text-caption outline-none focus-visible:ring-1 focus-visible:ring-ring"
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
            <div className="space-y-0.5 text-micro">
              <div className="font-mono">{ref.local_path}</div>
            </div>
          </TooltipContent>
        </Tooltip>
      )}
      {/* 常显而不是像其它操作那样 hover 才出现：没有它，用户无从分辨这个文件夹
          上的任务到底是直接改副本、还是交回一条分支——这正是任务排队（或没排队）
          时大家第一个会问的问题。 */}
      {mode === "worktree" && !editing && (
        <Tooltip>
          <TooltipTrigger
            render={
              <Badge variant="secondary" className="shrink-0 gap-1 font-normal">
                <GitBranch className="size-3" />
                {t(($) => $.resources.mode_badge_worktree)}
              </Badge>
            }
          />
          <TooltipContent side="top">
            {t(($) => $.resources.mode_badge_worktree_tooltip)}
          </TooltipContent>
        </Tooltip>
      )}
      {!editing && (
        <button
          type="button"
          onClick={() => onEditMode(resource)}
          className="opacity-0 group-hover:opacity-100 transition-opacity rounded-sm p-0.5 hover:bg-accent"
          title={t(($) => $.resources.mode_edit_tooltip)}
        >
          <GitBranch className="size-3 text-muted-foreground" />
        </button>
      )}
      {!editing && (
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

/** 「粘贴自定义仓库 URL」的提交表单（行内小表单）。 */
function CustomRepoForm({
  onSubmit,
}: {
  onSubmit: (url: string) => Promise<void> | void;
}) {
  const { t } = useT("projects");
  const [url, setUrl] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const handle = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = url.trim();
    if (!trimmed) return;
    setSubmitting(true);
    try {
      await onSubmit(trimmed);
      setUrl("");
    } finally {
      setSubmitting(false);
    }
  };
  return (
    <form onSubmit={handle} className="flex items-center gap-1.5 pt-1 border-t">
      <input
        type="text"
        value={url}
        onChange={(e) => setUrl(e.target.value)}
        placeholder={t(($) => $.resources.url_placeholder)}
        className="flex-1 bg-transparent text-caption px-2 py-1 outline-none placeholder:text-muted-foreground"
      />
      <Button
        type="submit"
        size="sm"
        variant="ghost"
        className="h-6 px-2 text-caption"
        disabled={!url.trim() || submitting}
      >
        {t(($) => $.resources.url_submit)}
      </Button>
    </form>
  );
}
