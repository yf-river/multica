// 本文件是「代码仓库」设置标签页：维护当前工作区关联的远程仓库列表。
// GitHub 通过 App 安装批量导入，工蜂通过凭据探活后导入；本地目录继续由项目资源管理。
// 列表增删改通过现有 workspace 更新接口保存到服务端。

"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { GitBranch, LoaderCircle, Plus, Search, Trash2, ExternalLink, FolderGit2 } from "lucide-react";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@multica/ui/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { toast } from "sonner";
import {
  useInfiniteQuery,
  useQuery,
} from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { memberListOptions } from "@multica/core/workspace/queries";
import { repositoryCenterOptions, useRepositoryActions, repositoryIdentity, repositoryProvider, repositoryStatus as getRepositoryStatus } from "@multica/core/workspace/repository-queries";
import { externalCredentialProfilesOptions } from "@multica/core/external-credentials";
import {
  githubInstallationRepositoriesOptions,
  githubInstallationsOptions,
} from "@multica/core/github";
import { api } from "@multica/core/api";
import type {
  GitHubRepository,
  WorkspaceRepo,
  WorkspaceRepoProbeResponse,
} from "@multica/core/types";
import { useNavigation } from "../../navigation";
import { useT, useLocale } from "../../i18n";
import {
  SettingsSaveState,
} from "./settings-layout";
import { RepositoryConfiguration, RepositoryURLForm } from "./repository-configuration";
import { GitHubMark } from "./github-mark";

// 空列表的默认值：避免 workspace 尚未加载时在组件内创建新数组。
const EMPTY_REPOSITORIES: WorkspaceRepo[] = [];

export { repositoryIdentity };

export function repositoryStatus(repository: WorkspaceRepo): "connected" | "failed" | "pending" {
  const status = getRepositoryStatus(repository);
  return status === "ok" ? "connected" : status === "error" ? "failed" : "pending";
}

function repositoryDisplayPath(repository: WorkspaceRepo): string {
  if (repository.project_path) return repository.project_path;
  const identity = repositoryIdentity(repository.url);
  return identity?.split("/").slice(1).join("/") || repository.url;
}

function repositoryDisplayName(repository: WorkspaceRepo): string {
  const path = repositoryDisplayPath(repository).replace(/\/$/, "");
  return path.split("/").at(-1) || path;
}

function repositoryIsArchived(repository: WorkspaceRepo): boolean {
  return (repository as WorkspaceRepo & { archived?: boolean }).archived === true;
}

/**
 * 「代码仓库」设置标签页：展示并编辑当前工作区的仓库列表，支持删除、
 * 从已安装的 GitHub App 中搜索勾选批量导入，以及探活后导入工蜂仓库；改动即时保存。
 */
export function RepositoriesTab() {
  const wsId = useWorkspaceId();
  return <WorkspaceRepositoriesTab key={wsId} />;
}

function WorkspaceRepositoriesTab() {
  const { t } = useT("settings");
  const locale = useLocale();
  const user = useAuthStore((state) => state.user);
  const workspace = useCurrentWorkspace();
  const wsId = useWorkspaceId();
  const { save: saveMutation, remove: removeMutation, probe: probeMutation, resolve: resolveMutation, connect: connectMutation } = useRepositoryActions(wsId);
  const savingRef = useRef(false);
  const navigation = useNavigation();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: center, isPending: centerPending, isError: centerError, refetch: refetchCenter } = useQuery(repositoryCenterOptions(wsId));
  const { data: gongfengCredentials = [], isPending: gongfengCredentialsPending, isError: gongfengCredentialsError } = useQuery(externalCredentialProfilesOptions("gongfeng"));
  const repositories = center?.repos ?? EMPTY_REPOSITORIES;
  const [pendingRemovalURL, setPendingRemovalURL] = useState<string | null>(null);
  const [connectingGitHub, setConnectingGitHub] = useState(false);
  const [githubPickerOpen, setGitHubPickerOpen] = useState(false);
  const [selectedInstallationID, setSelectedInstallationID] = useState("");
  const [selectedRepositories, setSelectedRepositories] = useState<
    Map<number, GitHubRepository>
  >(new Map());
  const [repositorySearch, setRepositorySearch] = useState("");
  const [gongfengPickerOpen, setGongfengPickerOpen] = useState(false);
  const [gongfengURL, setGongfengURL] = useState("");
  const [gongfengProbe, setGongfengProbe] = useState<WorkspaceRepoProbeResponse | null>(null);
  const [gongfengBranch, setGongfengBranch] = useState("");
  const [gongfengPending, setGongfengPending] = useState(false);
  const [gongfengError, setGongfengError] = useState<string | null>(null);
  const [listSearch, setListSearch] = useState("");
  const [providerFilter, setProviderFilter] = useState<"all" | "github" | "gongfeng" | "unknown">("all");
  const [statusFilter, setStatusFilter] = useState<"all" | "connected" | "failed" | "pending">("all");
  const [detailURL, setDetailURL] = useState<string | null>(null);
  const [detailTab, setDetailTab] = useState("overview");
  const [addRepositoryOpen, setAddRepositoryOpen] = useState(false);
  const [saveStatus, setSaveStatus] = useState<"idle" | "saving" | "saved" | "error">("idle");
  const [retryRepositories, setRetryRepositories] = useState<WorkspaceRepo[] | null>(null);

  const currentMember = members.find((member) => member.user_id === user?.id) ?? null;
  const canManageWorkspace =
    currentMember?.role === "owner" || currentMember?.role === "admin";
  const {
    data: githubData,
    isPending: githubInstallationsPending,
    isFetching: githubInstallationsFetching,
    isError: githubInstallationsError,
  } = useQuery({
    ...githubInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const githubInstallations = useMemo(
    () => githubData?.installations ?? [],
    [githubData?.installations],
  );
  const githubConnectConfigured = githubData?.configured === true;
  const githubConnected = githubInstallations.length > 0;
  const gongfengCredential = gongfengCredentials.find((profile) => profile.secret_binding.configured && profile.status === "verified") ?? gongfengCredentials[0] ?? null;
  const gongfengConnected = gongfengCredential?.secret_binding?.configured === true && gongfengCredential.status === "verified";
  const githubBrowseConfigured =
    githubData?.repository_browse_configured === true;
  const githubRepositoriesQuery = useInfiniteQuery({
    ...githubInstallationRepositoriesOptions(wsId, selectedInstallationID),
    enabled:
      githubPickerOpen &&
      canManageWorkspace &&
      githubBrowseConfigured &&
      !!selectedInstallationID,
  });
  const githubRepositories = useMemo(
    () =>
      githubRepositoriesQuery.data?.pages.flatMap(
        (page) => page.repositories,
      ) ?? [],
    [githubRepositoriesQuery.data?.pages],
  );
  const existingRepositoryIdentities = useMemo(
    () =>
      new Set(
        repositories
          .map((repository) => repositoryIdentity(repository.url))
          .filter((identity): identity is string => !!identity),
      ),
    [repositories],
  );
  const filteredGitHubRepositories = useMemo(() => {
    const search = repositorySearch.trim().toLowerCase();
    if (!search) return githubRepositories;
    return githubRepositories.filter((repository) =>
      repository.full_name.toLowerCase().includes(search),
    );
  }, [githubRepositories, repositorySearch]);

  const bindingCount = useCallback((repo: WorkspaceRepo) => {
    const identity = repositoryIdentity(repo.url);
    if (!identity || !center || !Array.isArray(center.projects)) return 0;
    return center.projects.filter((project) => project.resources.some((resource) => {
      const ref = resource.resource_ref as { url?: unknown };
      return (resource.resource_type === "github_repo" || resource.resource_type === "gongfeng_repo") && typeof ref.url === "string" && repositoryIdentity(ref.url) === identity;
    })).length;
  }, [center]);

  useEffect(() => {
    // 当前选中的安装若已不在安装列表里（列表刷新、安装被移除等），回退选中第一个安装，保证下拉有值。
    if (
      selectedInstallationID &&
      githubInstallations.some(
        (installation) => installation.id === selectedInstallationID,
      )
    ) {
      return;
    }
    setSelectedInstallationID(githubInstallations[0]?.id ?? "");
  }, [githubInstallations, selectedInstallationID]);

  useEffect(() => {
    // GitHub OAuth 回调完成后会带着 github_connected / github_error 查询参数跳回本页，这里统一收尾：
    // 失败弹错误提示；成功且开通了浏览权限就直接打开仓库选择器；最后把参数从地址栏清掉，避免刷新页面重复提示。
    const connected = navigation.searchParams.get("github_connected") === "1";
    const githubError = navigation.searchParams.get("github_error");
    if ((!connected && !githubError) || !canManageWorkspace) return;
    if (
      !githubError &&
      (githubInstallationsPending || githubInstallationsFetching)
    ) {
      return;
    }

    if (githubError) {
      toast.error(t(($) => $.repositories.github_connect_failed));
    } else if (githubInstallations.length > 0 && githubBrowseConfigured) {
      setSelectedInstallationID(githubInstallations[0]!.id);
      setGitHubPickerOpen(true);
    } else if (githubInstallations.length > 0) {
      toast.error(t(($) => $.repositories.github_browse_not_configured));
    }

    const next = new URLSearchParams(navigation.searchParams);
    next.delete("github_connected");
    next.delete("github_error");
    const search = next.toString();
    navigation.replace(`${navigation.pathname}${search ? `?${search}` : ""}`);
  }, [
    canManageWorkspace,
    githubBrowseConfigured,
    githubInstallations,
    githubInstallationsFetching,
    githubInstallationsPending,
    navigation,
    t,
  ]);

  const saveRepositories = useCallback(
    async (next: WorkspaceRepo[]) => {
      if (!workspace || !canManageWorkspace || savingRef.current) return;
      savingRef.current = true;
      setRetryRepositories(next);
      setSaveStatus("saving");
      try {
        await saveMutation.mutateAsync(next);
        setRetryRepositories(null);
        setSaveStatus("saved");
        toast.success(t(($) => $.repositories.toast_saved), { id: "repository-center-save" });
      } catch (error) {
        setSaveStatus("error");
        throw error;
      } finally {
        savingRef.current = false;
      }
    },
    [canManageWorkspace, saveMutation, t, workspace],
  );
  const visibleRepositories = useMemo(() => repositories.filter((repo) => {
    const provider = repositoryProvider(repo);
    const status = repositoryStatus(repo);
    const query = listSearch.trim().toLowerCase();
    return (providerFilter === "all" || provider === providerFilter) &&
      (statusFilter === "all" || status === statusFilter) &&
      (!query || `${repo.url} ${repo.project_path ?? ""} ${repo.description ?? ""}`.toLowerCase().includes(query));
  }), [repositories, listSearch, providerFilter, statusFilter]);
  const detailRepository = repositories.find((repo) => repo.url === detailURL) ?? null;
  const detailProvider = detailRepository ? repositoryProvider(detailRepository) : "unknown";
  const detailProviderConnected = detailProvider === "github" ? githubConnected : detailProvider === "gongfeng" ? gongfengConnected : false;
  const detailBranchesQuery = useQuery({
    queryKey: ["workspace-repository-branches", wsId, detailRepository?.url ?? ""],
    queryFn: () => api.probeWorkspaceRepo(wsId, { url: detailRepository!.url }),
    enabled: !!wsId && !!detailRepository && detailTab === "branches" && canManageWorkspace && detailProviderConnected,
  });

  const openRepositoryDetails = (index: number, tab = "overview") => {
    setDetailTab(tab);
    setDetailURL(repositories[index]?.url ?? null);
  };

  const openProjectResources = () => {
    if (!workspace) return;
    navigation.push(`/${workspace.slug}/projects`);
  };

  const openCredentialSettings = () => {
    const next = new URLSearchParams(navigation.searchParams);
    next.set("tab", "tokens");
    navigation.replace(`${navigation.pathname}?${next.toString()}`);
  };

  const openGongfengFlow = () => {
    if (!gongfengConnected) {
      openCredentialSettings();
      return;
    }
    setGongfengPickerOpen(true);
  };

  const formatDate = (value?: string) => {
    if (!value) return t(($) => $.repository_center.unverified_time);
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(date);
  };

  const pendingRemovalRepository = repositories.find((repo) => repo.url === pendingRemovalURL) ?? null;
  const pendingRemovalBindingCount = pendingRemovalRepository ? (centerPending || centerError ? null : bindingCount(pendingRemovalRepository)) : 0;

  // 「连接」按钮：先让服务端探活这个工蜂仓库，拿到默认分支与分支列表，供用户确认后导入。
  const probeGongfengRepository = async () => {
    if (!workspace || !gongfengURL.trim() || gongfengPending) return;
    setGongfengError(null);
    if (repositoryProvider({ url: gongfengURL }) !== "gongfeng") {
      setGongfengError(t(($) => $.repository_center.gongfeng_url_invalid));
      return;
    }
    setGongfengPending(true);
    try {
      const result = await probeMutation.mutateAsync(gongfengURL.trim());
      setGongfengProbe(result);
      setGongfengBranch(result.default_branch || result.branches[0] || "");
    } catch (error) {
      setGongfengProbe(null);
      setGongfengBranch("");
      setGongfengError(error instanceof Error ? error.message : t(($) => $.repository_center.probe_failed));
    } finally {
      setGongfengPending(false);
    }
  };

  // 「导入」按钮：把已探活的工蜂仓库解析成工作区仓库记录，追加进列表并立刻自动保存。
  const importGongfengRepository = async () => {
    if (!workspace || !gongfengProbe || !gongfengBranch || gongfengPending) return;
    setGongfengPending(true);
    try {
      const resolved = await resolveMutation.mutateAsync({
        url: gongfengProbe.url,
        default_branch: gongfengBranch,
      });
      const identity = repositoryIdentity(resolved.url);
      if (identity && existingRepositoryIdentities.has(identity)) {
        toast.error(t(($) => $.repository_center.already_added));
        return;
      }
      const next = [resolved];
      await saveRepositories(next);
      setGongfengURL("");
      setGongfengProbe(null);
      setGongfengBranch("");
      setGongfengPickerOpen(false);
      toast.success(t(($) => $.repository_center.imported));
    } catch (error) {
      setGongfengError(error instanceof Error ? error.message : t(($) => $.repository_center.import_failed));
    } finally {
      setGongfengPending(false);
    }
  };

  const openGitHubPicker = () => {
    setSelectedInstallationID(
      selectedInstallationID || githubInstallations[0]?.id || "",
    );
    setGitHubPickerOpen(true);
  };

  const handleGitHubAction = async () => {
    if (githubInstallationsPending || githubInstallationsError || connectingGitHub) return;
    // 已装过 GitHub App 就直接弹出"选仓库"弹窗；没装过则先跳转 OAuth 授权（在新窗口完成）。
    if (githubInstallations.length > 0) {
      if (!githubBrowseConfigured) {
        toast.error(t(($) => $.repositories.github_browse_not_configured));
        return;
      }
      openGitHubPicker();
      return;
    }
    setConnectingGitHub(true);
    try {
      const response = await connectMutation.mutateAsync();
      if (!response.configured || !response.url) {
        toast.error(t(($) => $.repositories.github_not_configured));
        return;
      }
      window.open(response.url, "_blank", "noopener");
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.repositories.github_connect_failed),
      );
    } finally {
      setConnectingGitHub(false);
    }
  };

  const closeGitHubPicker = () => {
    setGitHubPickerOpen(false);
    setSelectedRepositories(new Map());
    setRepositorySearch("");
  };

  const toggleGitHubRepository = (
    repository: GitHubRepository,
    checked: boolean,
  ) => {
    setSelectedRepositories((current) => {
      const next = new Map(current);
      if (checked) next.set(repository.id, repository);
      else next.delete(repository.id);
      return next;
    });
  };

  const importGitHubRepositories = async () => {
    if (savingRef.current || !canManageWorkspace) return;
    // 只导入尚未添加且未归档的仓库：按"身份标识"去重，避免同一仓库被重复加进列表。
    const additions: WorkspaceRepo[] = [];
    const known = new Set(existingRepositoryIdentities);
    for (const repository of selectedRepositories.values()) {
      const identity = repositoryIdentity(repository.clone_url);
      if (!identity || known.has(identity) || repository.archived) continue;
      known.add(identity);
      additions.push({
        url: repository.clone_url,
        provider: "github",
        default_branch: repository.default_branch,
        ...(repository.description?.trim()
          ? { description: repository.description.trim() }
          : {}),
      });
    }
    if (additions.length === 0) {
      closeGitHubPicker();
      return;
    }
    const next = additions;
    try {
      await saveRepositories(next);
      closeGitHubPicker();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.repository_center.save_failed));
    }
  };

  const removeRepository = async (url: string) => {
    const repo=repositories.find(item=>item.url===url);
    if(!repo?.id) throw new Error("仓库记录未加载，请刷新后重试");
    await removeMutation.mutateAsync(repo.id);
  };

  if (!workspace) return null;

  const repositoryStats = [
    { key: "all", label: t(($) => $.repository_center.all_repositories), value: repositories.length },
    { key: "github", label: t(($) => $.repository_center.github), value: repositories.filter((r) => repositoryProvider(r) === "github").length },
    { key: "gongfeng", label: t(($) => $.repository_center.gongfeng), value: repositories.filter((r) => repositoryProvider(r) === "gongfeng").length },
    { key: "error", label: t(($) => $.repository_center.connection_error), value: repositories.filter((r) => repositoryStatus(r) === "failed").length },
  ] as const;

  return (
    <>
    <div className="space-y-8">
      <header className="flex items-start justify-between gap-5">
        <div className="min-w-0">
          <div className="text-caption font-semibold uppercase tracking-[0.14em] text-muted-foreground">{t(($) => $.repository_center.eyebrow)}</div>
          <h2 className="mt-1 text-title-lg font-semibold tracking-tight">{t(($) => $.repository_center.title)}</h2>
          <p className="mt-1 max-w-2xl text-body leading-6 text-muted-foreground">{t(($) => $.repository_center.subtitle)}</p>
        </div>
        {canManageWorkspace ? <Button size="sm" className="mt-1 shrink-0" disabled={connectingGitHub || saveStatus === "saving" || centerPending || centerError} onClick={() => setAddRepositoryOpen(true)}><Plus className="size-3.5" />{t(($) => $.repository_center.add)}</Button> : null}
      </header>

      <p className="text-caption text-muted-foreground">{t($=>$.repository_config.provider_help)}</p>
      <div className="grid gap-3 md:grid-cols-2">
        {[
          {
            provider: "github",
            label: t(($) => $.repository_center.github),
            connected: githubConnected,
            status: githubInstallationsError ? t(($) => $.repository_center.read_failed) : githubInstallationsPending ? t(($) => $.repository_center.loading) : githubConnected ? t(($) => $.repository_center.connected) : githubConnectConfigured ? t(($) => $.repository_center.not_connected) : t(($) => $.repository_center.not_configured),
            caption: githubInstallationsError ? t(($) => $.repository_center.github_read_failed) : githubConnected ? t(($) => $.repository_center.installation_count, { count: githubInstallations.length }) : githubConnectConfigured ? t(($) => $.repository_center.github_connect_hint) : t(($) => $.repository_center.github_missing),
          },
          {
            provider: "gongfeng",
            label: t(($) => $.repository_center.gongfeng_name),
            connected: gongfengConnected,
            status: gongfengCredentialsError ? t(($) => $.repository_center.read_failed) : gongfengCredentialsPending ? t(($) => $.repository_center.loading) : gongfengConnected ? t(($) => $.repository_center.connected) : gongfengCredential?.status === "failed" ? t(($) => $.repository_center.connection_error) : gongfengCredential ? t(($) => $.repository_center.pending) : t(($) => $.repository_center.not_configured),
            caption: gongfengCredentialsError ? t(($) => $.repository_center.credential_read_failed) : gongfengConnected ? t(($) => $.repository_center.credential_verified) : gongfengCredential?.status === "failed" ? t(($) => $.repository_center.credential_failed) : gongfengCredential ? t(($) => $.repository_center.credential_pending) : t(($) => $.repository_center.credential_missing),
          },
        ].map((connection) => (
          <div key={connection.provider} className="flex min-w-0 items-center gap-3 rounded-xl border border-surface-border bg-surface-raised p-4">
            <span className="grid size-10 shrink-0 place-items-center rounded-lg border border-surface-border bg-surface-hover text-body font-semibold">{connection.provider === "github" ? <GitHubMark className="size-5" /> : <GitBranch className="size-5" />}</span>
            <div className="min-w-0 flex-1"><p className="font-medium">{connection.label}</p><p className="truncate text-caption text-muted-foreground">{connection.caption}</p></div>
            <Badge variant={connection.connected ? "secondary" : connection.status === t(($) => $.repository_center.connection_error) ? "destructive" : "outline"}>{connection.status}</Badge>
            <Button variant="ghost" size="icon-sm" className="size-11 sm:size-7" aria-label={t(($) => $.repository_center.manage_provider, { provider: connection.label })} disabled={!canManageWorkspace || (connection.provider === "github" ? githubInstallationsPending || githubInstallationsError || connectingGitHub : gongfengCredentialsPending || gongfengCredentialsError)} onClick={() => { if (connection.provider === "github") void handleGitHubAction(); else openCredentialSettings(); }}><ExternalLink className="size-4" /></Button>
          </div>
        ))}
      </div>

      <div className="flex flex-wrap items-center gap-1 pb-1">
        {repositoryStats.map((stat) => {
          const active = stat.key === "error" ? statusFilter === "failed" : stat.key === "all" ? providerFilter === "all" && statusFilter === "all" : providerFilter === stat.key && statusFilter === "all";
          return <button key={stat.key} type="button" aria-pressed={active} className={`inline-flex min-h-10 items-center gap-2 rounded-md border-0 px-2.5 text-caption focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring transition-colors hover:bg-surface-hover ${active ? "bg-surface-selected font-semibold text-surface-selected-foreground" : "text-muted-foreground"}`} onClick={() => { if (stat.key === "error") { setProviderFilter("all"); setStatusFilter("failed"); } else { setProviderFilter(stat.key === "all" ? "all" : stat.key); setStatusFilter("all"); } }}><span>{stat.label}</span><span className="min-w-6 rounded border border-surface-border bg-surface-raised px-1.5 text-center tabular-nums text-foreground">{stat.value}</span></button>;
        })}
      </div>

      <div className="overflow-hidden rounded-xl border border-surface-border bg-surface-raised shadow-none">
          <div className="flex flex-wrap gap-2 border-b px-4 py-3">
            <div className="relative min-w-[min(100%,18rem)] flex-1">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input value={listSearch} onChange={(event) => setListSearch(event.target.value)} placeholder={t(($) => $.repository_center.search_placeholder)} aria-label={t(($) => $.repository_center.search_label)} className="pl-8" />
            </div>
            <Select value={providerFilter} onValueChange={(value) => setProviderFilter((value ?? "all") as typeof providerFilter)} items={[{ value: "all", label: t(($) => $.repository_center.all_providers) }, { value: "github", label: t(($) => $.repository_center.github) }, { value: "gongfeng", label: t(($) => $.repository_center.gongfeng) }, { value: "unknown", label: t(($) => $.repository_center.other_git) }]}><SelectTrigger className="w-32" aria-label={t(($) => $.repository_center.provider_filter)}><SelectValue /></SelectTrigger><SelectContent>{[{value: "all", label: t(($) => $.repository_center.all_providers)}, {value: "github", label: t(($) => $.repository_center.github)}, {value: "gongfeng", label: t(($) => $.repository_center.gongfeng)}, {value: "unknown", label: t(($) => $.repository_center.other_git)}].map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent></Select>
            <Select value={statusFilter} onValueChange={(value) => setStatusFilter((value ?? "all") as typeof statusFilter)} items={[{ value: "all", label: t(($) => $.repository_center.all_statuses) }, { value: "connected", label: t(($) => $.repository_center.connected) }, { value: "failed", label: t(($) => $.repository_center.connection_error) }, { value: "pending", label: t(($) => $.repository_center.pending) }]}><SelectTrigger className="w-32" aria-label={t(($) => $.repository_center.status_filter)}><SelectValue /></SelectTrigger><SelectContent>{[{value: "all", label: t(($) => $.repository_center.all_statuses)}, {value: "connected", label: t(($) => $.repository_center.connected)}, {value: "failed", label: t(($) => $.repository_center.connection_error)}, {value: "pending", label: t(($) => $.repository_center.pending)}].map((item) => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent></Select>
          </div>

          <div className="hidden grid-cols-[minmax(0,1fr)_9rem_9rem_7rem_2.5rem] gap-4 border-b border-surface-border bg-surface-sunken px-5 py-2.5 text-caption text-muted-foreground lg:grid"><span>{t(($) => $.repository_center.repository)}</span><span>{t(($) => $.repository_config.development_branch)}</span><span>{t(($) => $.repository_center.connection_status)}</span><span>{t(($) => $.repository_center.bound_projects)}</span><span /></div>
          {centerPending ? (
            <div role="status" className="flex items-center justify-center gap-2 px-4 py-12 text-caption text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />{t(($) => $.repository_center.loading_repositories)}</div>
          ) : centerError ? (
            <div role="alert" className="flex flex-col items-center gap-3 px-4 py-12 text-center text-caption text-muted-foreground"><p>{t(($) => $.repository_center.center_failed)}</p><Button variant="outline" size="sm" onClick={() => void refetchCenter()}>{t(($) => $.repository_center.reload)}</Button></div>
          ) : repositories.length === 0 ? (
            <div className="flex flex-col items-center px-5 py-14 text-center">
              <FolderGit2 className="mb-4 size-8 text-muted-foreground" />
              <h3 className="text-body font-semibold">{t(($) => $.repository_center.empty_title)}</h3>
              <p className="mt-2 max-w-md text-caption leading-5 text-muted-foreground">{t(($) => $.repository_center.empty_description)}</p>
              {canManageWorkspace ? <div className="mt-5 flex flex-wrap justify-center gap-2"><Button variant="outline" size="sm" onClick={() => void handleGitHubAction()}><GitHubMark className="size-4" />{t(($) => $.repository_center.choose_github)}</Button><Button variant="outline" size="sm" onClick={openGongfengFlow}><GitBranch className="size-4" />{t(($) => $.repository_center.add_gongfeng)}</Button></div> : null}
            </div>
          ) : null}

          {!centerPending && !centerError && visibleRepositories.map((repository) => {
            const index = repositories.indexOf(repository);
            const provider = repositoryProvider(repository);
            const status = repositoryStatus(repository);
            return (
            <div
              key={repository.url}
              className="grid gap-2 border-b border-surface-border px-4 py-4 lg:grid-cols-[minmax(0,1fr)_9rem_9rem_7rem_2.5rem] lg:items-center lg:gap-4 lg:px-5"
            >
              <div className="flex min-w-0 items-center gap-2">
                <span className="grid size-8 shrink-0 place-items-center rounded-lg border border-surface-border bg-surface-hover text-muted-foreground">{provider === "github" ? <GitHubMark className="size-4" /> : <GitBranch className="size-4" />}</span>
                <button type="button" className="min-w-0 rounded-md text-left hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" onClick={() => openRepositoryDetails(index)}><span className="flex flex-wrap items-center gap-2 text-body font-medium"><span className="truncate">{repositoryDisplayName(repository)}</span>{repositoryIsArchived(repository) ? <Badge variant="outline">{t(($) => $.repository_center.archived)}</Badge> : null}</span><span className="block truncate text-caption text-muted-foreground">{repositoryDisplayPath(repository)} · {provider === "github" ? t(($) => $.repository_center.github) : provider === "gongfeng" ? t(($) => $.repository_center.gongfeng) : "Git"}</span></button>
              </div>
              <div className="min-w-0 lg:row-start-1 lg:col-start-2"><p className="flex min-w-0 items-center gap-1 text-caption"><GitBranch className="size-3.5 shrink-0 text-muted-foreground" /><span className="truncate" title={repository.ref || repository.default_branch}>{repository.ref || repository.default_branch || t(($) => $.repository_center.unverified)}</span></p></div>
              <div className="min-w-0 lg:row-start-1 lg:col-start-3"><p className={`flex items-center gap-1.5 text-caption ${status === "connected" ? "text-success" : status === "failed" ? "text-destructive" : "text-warning"}`}><span className="size-1.5 rounded-full bg-current" />{status === "connected" ? t(($) => $.repository_center.healthy) : status === "failed" ? t(($) => $.repository_center.failed) : t(($) => $.repository_center.pending)}</p><p className="truncate text-caption text-muted-foreground">{formatDate(repository.last_tested_at)}</p></div>
              <div className="min-w-0 text-caption text-muted-foreground lg:row-start-1 lg:col-start-4">{bindingCount(repository) ? t(($) => $.repository_center.project_count, { count: bindingCount(repository) }) : t(($) => $.repository_center.unbound)}</div>
              <div className="min-w-0 lg:col-span-1 lg:row-start-2 lg:col-start-1"><p className="truncate font-mono text-caption text-muted-foreground">{repository.url}</p><p className="truncate text-caption">{repository.description || t(($) => $.repository_center.no_description)}</p></div>
              <div className="flex justify-end gap-1 lg:col-start-5 lg:row-start-1">
              {canManageWorkspace ? (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t(($) => $.repositories.delete_aria)}
                  className="size-11 text-muted-foreground hover:text-destructive sm:size-7"
                  disabled={saveStatus === "saving"}
                  onClick={() => setPendingRemovalURL(repository.url)}
                >
                  <Trash2 className="size-3.5" />
                </Button>
              ) : null}
              <Button type="button" variant="ghost" size="icon-sm" aria-label={t(($) => $.repository_center.view_details, { name: repositoryDisplayName(repository) })} className="size-11 sm:size-7" onClick={() => openRepositoryDetails(index)}><ExternalLink className="size-3.5" /></Button>
              </div>
            </div>
            );
          })}
          {!centerPending && !centerError && repositories.length > 0 && visibleRepositories.length === 0 ? <div className="px-4 py-8 text-center text-caption text-muted-foreground">{t(($) => $.repository_center.no_matches)}</div> : null}

          <div className="flex flex-wrap items-center justify-between gap-3 border-t px-4 py-3 text-caption text-muted-foreground"><span>{visibleRepositories.length} {t(($) => $.repository_center.count_suffix)}{!canManageWorkspace ? t(($) => $.repository_center.read_only) : ""}</span><span className="flex items-center gap-1.5"><SettingsSaveState status={saveStatus} savingLabel={t(($) => $.auto_save.saving)} savedLabel={t(($) => $.auto_save.saved)} errorLabel={t(($) => $.auto_save.failed)} /></span></div>
      </div>

      {saveStatus === "error" && retryRepositories ? <div role="alert" className="-mt-5 flex items-center gap-3 text-caption text-destructive"><span>{t(($) => $.repository_center.retry_hint)}</span><Button variant="outline" size="sm" onClick={() => void saveRepositories(retryRepositories).catch(() => undefined)}>{t(($) => $.repository_center.retry_save)}</Button></div> : null}

        <div className="flex flex-wrap items-start gap-3 rounded-lg bg-surface-sunken p-4 text-caption">
          <FolderGit2 className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0 flex-1 basis-48"><p className="font-medium">{t(($) => $.repository_center.runtime_title)}</p><p className="mt-1 text-muted-foreground">{t(($) => $.repository_center.runtime_description)}</p></div>
          {canManageWorkspace ? <Button variant="ghost" size="sm" className="ml-auto shrink-0" onClick={openProjectResources}>{t(($) => $.repository_center.link_directory)} <ExternalLink className="size-3.5" /></Button> : null}
        </div>
    </div>

      <Dialog
        open={githubPickerOpen}
        onOpenChange={(open) => {
          if (!open && saveStatus !== "saving") closeGitHubPicker();
        }}
      >
        <DialogContent className="flex max-h-[85dvh] flex-col gap-0 p-0 sm:max-w-2xl">
          <DialogHeader className="border-b px-6 py-5">
            <DialogTitle>
              {t(($) => $.repositories.github_picker_title)}
            </DialogTitle>
            <DialogDescription>
              {t(($) => $.repositories.github_picker_description)}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-3 px-6 py-4">
            {githubInstallations.length > 1 ? (
              <Select
                items={githubInstallations.map((installation) => ({
                  value: installation.id,
                  label: installation.account_login,
                }))}
                value={selectedInstallationID}
                disabled={saveStatus === "saving"}
                onValueChange={(value) => {
                  setSelectedInstallationID(value ?? "");
                  setSelectedRepositories(new Map());
                }}
              >
                <SelectTrigger
                  aria-label={t(($) => $.repositories.github_account)}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {githubInstallations.map((installation) => (
                    <SelectItem
                      key={installation.id}
                      value={installation.id}
                    >
                      {installation.account_login}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : githubInstallations[0] ? (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.repositories.github_account)}:{" "}
                <span className="font-medium text-foreground">
                  {githubInstallations[0].account_login}
                </span>
              </p>
            ) : null}

            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={repositorySearch}
                onChange={(event) => setRepositorySearch(event.target.value)}
                placeholder={t(
                  ($) => $.repositories.github_search_placeholder,
                )}
                aria-label={t(
                  ($) => $.repositories.github_search_placeholder,
                )}
                className="pl-8"
              />
            </div>
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain border-y">
            {githubRepositoriesQuery.isPending ? (
              <div className="flex items-center justify-center gap-2 px-6 py-12 text-body text-muted-foreground">
                <LoaderCircle className="size-4 animate-spin" />
                {t(($) => $.repositories.github_loading)}
              </div>
            ) : githubRepositoriesQuery.isError ? (
              <div className="px-6 py-12 text-center text-body text-muted-foreground">
                {t(($) => $.repositories.github_load_failed)}
              </div>
            ) : filteredGitHubRepositories.length === 0 ? (
              <div className="px-6 py-12 text-center text-body text-muted-foreground">
                {repositorySearch
                  ? t(($) => $.repositories.github_no_search_results)
                  : t(($) => $.repositories.github_empty)}
              </div>
            ) : (
              <div className="divide-y">
                {filteredGitHubRepositories.map((repository) => {
                  const identity = repositoryIdentity(repository.clone_url);
                  const alreadyAdded =
                    !!identity && existingRepositoryIdentities.has(identity);
                  const disabled = alreadyAdded || repository.archived;
                  return (
                    <label
                      key={repository.id}
                      htmlFor={`github-repository-${repository.id}`}
                      className="flex items-start gap-3 px-6 py-3.5"
                    >
                      <Checkbox
                        id={`github-repository-${repository.id}`}
                        checked={
                          alreadyAdded ||
                          selectedRepositories.has(repository.id)
                        }
                        disabled={disabled || saveStatus === "saving"}
                        onCheckedChange={(checked) =>
                          toggleGitHubRepository(
                            repository,
                            checked === true,
                          )
                        }
                        className="mt-0.5"
                      />
                      <span className="min-w-0 flex-1 space-y-1">
                        <span className="flex flex-wrap items-center gap-2">
                          <span className="truncate text-body font-medium">
                            {repository.full_name}
                          </span>
                          {repository.private ? (
                            <Badge variant="secondary">
                              {t(($) => $.repositories.github_private)}
                            </Badge>
                          ) : null}
                          {repository.archived ? (
                            <Badge variant="outline">
                              {t(($) => $.repositories.github_archived)}
                            </Badge>
                          ) : null}
                          {alreadyAdded ? (
                            <Badge variant="outline">
                              {t(($) => $.repositories.github_added)}
                            </Badge>
                          ) : null}
                        </span>
                        {repository.description ? (
                          <span className="block truncate text-caption text-muted-foreground">
                            {repository.description}
                          </span>
                        ) : null}
                      </span>
                    </label>
                  );
                })}
              </div>
            )}

            {githubRepositoriesQuery.hasNextPage ? (
              <div className="flex justify-center border-t p-3">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => githubRepositoriesQuery.fetchNextPage()}
                  disabled={githubRepositoriesQuery.isFetchingNextPage}
                >
                  {githubRepositoriesQuery.isFetchingNextPage
                    ? t(($) => $.repositories.github_loading)
                    : t(($) => $.repositories.github_load_more)}
                </Button>
              </div>
            ) : null}
          </div>

          <DialogFooter className="m-0 border-t bg-muted/30 px-6 py-4">
            <p className="mr-auto text-caption text-muted-foreground">
              {t(($) => $.repositories.github_selected_count, {
                count: selectedRepositories.size,
              })}
            </p>
            <Button variant="ghost" disabled={saveStatus === "saving"} onClick={closeGitHubPicker}>
              {t(($) => $.repositories.github_cancel)}
            </Button>
            <Button
              onClick={importGitHubRepositories}
              disabled={selectedRepositories.size === 0 || saveStatus === "saving"}
            >
              {saveStatus === "saving" ? <LoaderCircle className="size-4 animate-spin" /> : null}
              {t(($) => $.repositories.github_import)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={gongfengPickerOpen}
        onOpenChange={(open) => {
          if (gongfengPending) return;
          setGongfengPickerOpen(open);
          if (!open) {
            setGongfengError(null);
            setGongfengProbe(null);
            setGongfengBranch("");
          }
        }}
      >
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t(($) => $.repositories.gongfeng_title)}</DialogTitle>
            <DialogDescription>
              {t(($) => $.repositories.gongfeng_description)}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <label htmlFor="gongfeng-repository-url" className="text-caption font-medium">
                {t(($) => $.repositories.gongfeng_url_label)}
              </label>
              <div className="flex gap-2">
                <Input
                  id="gongfeng-repository-url"
                  aria-invalid={!!gongfengError}
                  aria-describedby={gongfengError ? "gongfeng-repository-error" : undefined}
                  name="repository-url"
                  autoComplete="off"
                  spellCheck={false}
                  disabled={gongfengPending}
                  value={gongfengURL}
                  onChange={(event) => {
                    setGongfengError(null);
                    setGongfengURL(event.target.value);
                    setGongfengProbe(null);
                    setGongfengBranch("");
                  }}
                  placeholder={t(($) => $.repository_center.url_placeholder)}
                  className="font-mono text-caption"
                />
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => void probeGongfengRepository()}
                  disabled={!gongfengURL.trim() || gongfengPending}
                >
                  {gongfengPending ? <LoaderCircle className="size-3.5 animate-spin" /> : null}
                  {t(($) => $.repositories.gongfeng_connect)}
                </Button>
              </div>
            </div>
            {gongfengError ? <p id="gongfeng-repository-error" role="alert" className="text-caption text-destructive">{gongfengError}</p> : null}
            {gongfengProbe ? (
              <div className="space-y-2">
                <label className="text-caption font-medium">
                  {t(($) => $.repositories.gongfeng_branch_label)}
                </label>
                <Select
                  items={gongfengProbe.branches.map((branch) => ({ value: branch, label: branch }))}
                  value={gongfengBranch}
                  onValueChange={(value) => setGongfengBranch(value ?? "")}
                >
                  <SelectTrigger aria-label={t(($) => $.repositories.gongfeng_branch_label)}>
                    <SelectValue placeholder={t(($) => $.repositories.gongfeng_branch_placeholder)} />
                  </SelectTrigger>
                  <SelectContent>
                    {gongfengProbe.branches.map((branch) => (
                      <SelectItem key={branch} value={branch}>{branch}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.repositories.gongfeng_connected, {
                    project: gongfengProbe.project_path,
                    count: gongfengProbe.branches.length,
                  })}
                </p>
              </div>
            ) : null}
          </div>
          <DialogFooter>
            <Button variant="ghost" disabled={gongfengPending} onClick={() => setGongfengPickerOpen(false)}>
              {t(($) => $.repositories.gongfeng_cancel)}
            </Button>
            <Button
              onClick={() => void importGongfengRepository()}
              disabled={!gongfengProbe || !gongfengBranch || gongfengPending || saveStatus === "saving"}
            >
              {t(($) => $.repositories.gongfeng_import)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Sheet open={!!detailRepository} onOpenChange={(open) => { if (!open) setDetailURL(null); }}>
        <SheetContent side="right" className="w-full overflow-y-auto overscroll-contain sm:max-w-xl">
          {detailRepository ? (() => { const repo = detailRepository; return <>
            <SheetHeader><SheetTitle className="break-all pr-8">{repo.project_path || repo.url}</SheetTitle><SheetDescription className="break-all">{repo.url}</SheetDescription></SheetHeader>
            <div className="px-4 pb-6"><Tabs value={detailTab} onValueChange={setDetailTab}><TabsList variant="line" className="w-full justify-start"><TabsTrigger value="overview">{t(($) => $.repository_center.overview)}</TabsTrigger><TabsTrigger value="branches">{t(($) => $.repository_center.branches)}</TabsTrigger><TabsTrigger value="projects">{t(($) => $.repository_center.projects)}</TabsTrigger><TabsTrigger value="runtime">{t(($) => $.repository_center.runtime)}</TabsTrigger></TabsList>
              <TabsContent value="overview" className="space-y-4 pt-5"><div className="flex flex-wrap gap-2"><Badge variant="outline">{repositoryProvider(repo)}</Badge><Badge variant={repositoryStatus(repo) === "failed" ? "destructive" : "secondary"}>{repositoryStatus(repo) === "connected" ? t(($) => $.repository_center.connection_success) : repositoryStatus(repo) === "failed" ? t(($) => $.repository_center.failed) : t(($) => $.repository_center.pending)}</Badge></div><dl className="grid gap-3 text-caption sm:grid-cols-2"><div><dt className="text-muted-foreground">{t(($) => $.repository_center.default_branch)}</dt><dd className="break-all">{repo.default_branch || t(($) => $.repository_center.unverified)}</dd></div><div><dt className="text-muted-foreground">{t(($) => $.repository_center.head_commit)}</dt><dd className="break-all font-mono">{repo.head_commit || t(($) => $.repository_center.not_read)}</dd></div><div><dt className="text-muted-foreground">{t(($) => $.repository_center.last_verified)}</dt><dd>{formatDate(repo.last_tested_at)}</dd></div><div><dt className="text-muted-foreground">{t(($) => $.repository_center.sync_status)}</dt><dd>{repo.sync_status || t(($) => $.repository_center.not_pulled)}</dd></div></dl></TabsContent>
              <TabsContent value="branches" className="space-y-3 pt-5"><RepositoryConfiguration key={repo.id+":branch"} wsId={wsId} repo={repo} center={center} canManage={canManageWorkspace} section="branch"/><div className="flex items-center justify-between gap-3"><p className="text-caption text-muted-foreground">{t(($) => $.repository_center.available_branches)}</p>{canManageWorkspace && detailProviderConnected ? <Button variant="outline" size="sm" onClick={() => void detailBranchesQuery.refetch()} disabled={detailBranchesQuery.isFetching}>{detailBranchesQuery.isFetching ? <LoaderCircle className="size-3.5 animate-spin" /> : null}{t(($) => $.repository_center.refresh_branches)}</Button> : null}</div>{!canManageWorkspace || !detailProviderConnected ? <div className="rounded-lg bg-surface-sunken p-3 text-caption text-muted-foreground">{canManageWorkspace ? t(($) => $.repository_center.connect_first) : t(($) => $.repository_center.branches_permission)}</div> : detailBranchesQuery.isPending ? <div className="flex items-center gap-2 text-caption text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />{t(($) => $.repository_center.loading_branches)}</div> : detailBranchesQuery.isError ? <div className="rounded-lg bg-surface-sunken p-3 text-caption text-destructive">{t(($) => $.repository_center.branches_failed)}</div> : detailBranchesQuery.data?.branches?.length ? <div className="divide-y rounded-lg border border-surface-border">{detailBranchesQuery.data.branches.map((branch) => <div key={branch} className="flex items-center justify-between gap-3 px-3 py-2.5 text-caption"><span className="truncate">{branch}</span>{branch === (detailBranchesQuery.data.default_branch || repo.default_branch) ? <Badge variant="secondary">{t(($) => $.repository_center.default)}</Badge> : null}</div>)}</div> : <><p className="font-medium">{repo.default_branch || t(($) => $.repository_center.unverified)}</p><p className="text-caption text-muted-foreground">{t(($) => $.repository_center.branches_hint)}</p></>}</TabsContent>
              <TabsContent value="projects" className="space-y-3 pt-5"><p className="text-caption text-muted-foreground">{t(($) => $.repository_center.projects_title)}</p>{center && Array.isArray(center.projects) ? center.projects.filter((project) => project.resources.some((resource) => { const ref = resource.resource_ref as { url?: unknown }; return typeof ref.url === "string" && repositoryIdentity(ref.url) === repositoryIdentity(repo.url); })).map((project) => <div key={project.id} className="rounded-lg border border-surface-border p-3"><p className="break-words font-medium">{project.title}</p><p className="text-caption text-muted-foreground">{t(($) => $.repository_center.project_resource)}</p></div>) : null}{bindingCount(repo) === 0 ? <p className="text-caption text-muted-foreground">{t(($) => $.repository_center.projects_empty)}</p> : null}</TabsContent>
              <TabsContent value="runtime" className="space-y-3 pt-5"><RepositoryConfiguration key={repo.id+":location"} wsId={wsId} repo={repo} center={center} canManage={canManageWorkspace} section="location"/><p className="flex items-center gap-2"><FolderGit2 className="size-4" />{t(($) => $.repository_center.task_directories)}</p>{center && Array.isArray(center.locations) && Array.isArray(center.projects) ? center.locations.filter((location) => location.project_id && center.projects.some((project) => project.id === location.project_id && project.resources.some((resource) => { const ref = resource.resource_ref as { url?: unknown }; return typeof ref.url === "string" && repositoryIdentity(ref.url) === repositoryIdentity(repo.url); }))).map((location) => <div key={location.task_id} className="rounded-lg border border-surface-border p-3"><p className="font-medium">{location.runtime_name || location.daemon_id || "runtime"}</p><p className="break-all font-mono text-caption text-muted-foreground">{location.work_dir || t(($) => $.repository_center.worktree_on_demand)}</p><p className="text-caption text-muted-foreground">{location.status} {t(($) => $.repository_center.pull_evidence)}</p></div>) : null}<p className="text-caption text-muted-foreground">{t(($) => $.repository_center.runtime_hint)}</p></TabsContent>
            </Tabs></div>
          </>; })() : null}
        </SheetContent>
      </Sheet>

      <Dialog open={addRepositoryOpen} onOpenChange={setAddRepositoryOpen}><DialogContent className="sm:max-w-lg"><DialogHeader><DialogTitle>{t(($) => $.repository_center.add_title)}</DialogTitle><DialogDescription>{t(($) => $.repository_center.add_description)}</DialogDescription></DialogHeader><div className="grid gap-3"><Button variant="outline" className="h-auto justify-start p-4" onClick={() => { setAddRepositoryOpen(false); void handleGitHubAction(); }}><GitHubMark className="size-5" /><span className="min-w-0 whitespace-normal"><strong className="block text-left">{t(($) => $.repository_center.github)}</strong><span className="text-caption text-muted-foreground">{t(($) => $.repository_center.github_option)}</span></span></Button><Button variant="outline" className="h-auto justify-start p-4" onClick={() => { setAddRepositoryOpen(false); openGongfengFlow(); }}><GitBranch className="size-5" /><span className="min-w-0 whitespace-normal"><strong className="block text-left">{t(($) => $.repository_center.gongfeng)}</strong><span className="text-caption text-muted-foreground">{t(($) => $.repository_center.gongfeng_option)}</span></span></Button><Button variant="outline" className="h-auto justify-start p-4" onClick={() => { setAddRepositoryOpen(false); openProjectResources(); }}><FolderGit2 className="size-5" /><span className="min-w-0 whitespace-normal"><strong className="block text-left">{t(($) => $.repository_center.local_directory)}</strong><span className="text-caption text-muted-foreground">{t(($) => $.repository_center.local_option)}</span></span></Button></div><RepositoryURLForm wsId={wsId} onSaved={()=>setAddRepositoryOpen(false)}/></DialogContent></Dialog>

      <AlertDialog
        open={!!pendingRemovalRepository}
        onOpenChange={(open) => {
          if (!open) setPendingRemovalURL(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {pendingRemovalBindingCount && pendingRemovalBindingCount > 0 ? t(($) => $.repository_center.removal_blocked) : t(($) => $.repositories.delete_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {pendingRemovalBindingCount === null ? centerError ? t(($) => $.repository_center.bindings_failed) : t(($) => $.repository_center.bindings_loading) : pendingRemovalBindingCount > 0 ? t(($) => $.repository_center.removal_hint, { count: pendingRemovalBindingCount }) : t(($) => $.repositories.delete_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(($) => $.repositories.delete_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant={pendingRemovalBindingCount && pendingRemovalBindingCount > 0 ? "default" : "destructive"}
              disabled={pendingRemovalBindingCount === null || saveStatus === "saving"}
              onClick={() => {
                if (pendingRemovalRepository) {
                  if (pendingRemovalBindingCount && pendingRemovalBindingCount > 0) {
                    const index = repositories.indexOf(pendingRemovalRepository);
                    setPendingRemovalURL(null);
                    openRepositoryDetails(index, "projects");
                    return;
                  }
                  void removeRepository(pendingRemovalRepository.url).catch((error) => toast.error(error instanceof Error ? error.message : t(($) => $.repository_center.remove_failed)));
                }
                setPendingRemovalURL(null);
              }}
            >
              {pendingRemovalBindingCount && pendingRemovalBindingCount > 0 ? t(($) => $.repository_center.view_bindings) : t(($) => $.repositories.delete_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
