// 本文件是「代码仓库」设置标签页：维护当前工作区关联的远程仓库列表。
// GitHub 通过 App 安装批量导入，工蜂通过凭据探活后导入；本地目录继续由项目资源管理。
// 列表增删改通过现有 workspace 更新接口保存到服务端。

"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
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
  useQueryClient,
} from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { memberListOptions, workspaceKeys } from "@multica/core/workspace/queries";
import { repositoryCenterOptions } from "@multica/core/workspace/repository-queries";
import { externalCredentialProfilesOptions } from "@multica/core/external-credentials";
import {
  githubInstallationRepositoriesOptions,
  githubInstallationsOptions,
} from "@multica/core/github";
import { api } from "@multica/core/api";
import { normalizeWorkspaceRepoProvider } from "@multica/core/types";
import type {
  GitHubRepository,
  Workspace,
  WorkspaceRepo,
  WorkspaceRepoProbeResponse,
} from "@multica/core/types";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import {
  SettingsSaveState,
} from "./settings-layout";
import { GitHubMark } from "./github-mark";

// 空列表的默认值：避免 workspace 尚未加载时在组件内创建新数组。
const EMPTY_REPOSITORIES: WorkspaceRepo[] = [];

/**
 * 把仓库地址归一化成"主机/路径"形式的身份标识（小写、去首尾斜杠与 .git 后缀），
 * 兼容 "git@host:path" 的 scp 写法与 https 等完整 URL，用于判断两个仓库地址是否指向同一个仓库。
 * 地址为空、既不是 scp 写法也无法按 URL 解析时返回 null。
 */
export function repositoryIdentity(rawURL: string): string | null {
  const value = rawURL.trim();
  if (!value) return null;

  let host = "";
  let path = "";
  if (!value.includes("://")) {
    const scpLike = value.match(/^(?:[^@\s/]+@)?([^:\s/]+):(.+)$/);
    if (scpLike) {
      host = scpLike[1] ?? "";
      path = scpLike[2] ?? "";
    }
  }
  if (!host) {
    try {
      const parsed = new URL(value);
      host = parsed.hostname;
      path = parsed.pathname;
    } catch {
      return null;
    }
  }

  const normalizedPath = path
    .replace(/^\/+|\/+$/g, "")
    .replace(/\.git$/i, "");
  if (!host || !normalizedPath) return null;
  return `${host.toLowerCase()}/${normalizedPath}`;
}

export function repositoryStatus(repository: WorkspaceRepo): "connected" | "failed" | "pending" {
  const values = [repository.connection_status, repository.test_status, repository.sync_status]
    .map((value) => value?.toLowerCase());
  if (values.some((value) => value === "failed" || value === "error" || value === "disconnected")) return "failed";
  if (values.some((value) => value === "connected" || value === "success" || value === "ready")) return "connected";
  return "pending";
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
  const { t } = useT("settings");
  const user = useAuthStore((state) => state.user);
  const workspace = useCurrentWorkspace();
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const navigation = useNavigation();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: center, isPending: centerPending, isError: centerError, refetch: refetchCenter } = useQuery(repositoryCenterOptions(wsId));
  const { data: gongfengCredentials = [], isPending: gongfengCredentialsPending } = useQuery(externalCredentialProfilesOptions("gongfeng"));
  const [repositories, setRepositories] = useState<WorkspaceRepo[]>(
    workspace?.repos ?? EMPTY_REPOSITORIES,
  );
  const [pendingRemovalIndex, setPendingRemovalIndex] = useState<number | null>(null);
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
  const [listSearch, setListSearch] = useState("");
  const [providerFilter, setProviderFilter] = useState<"all" | "github" | "gongfeng" | "unknown">("all");
  const [statusFilter, setStatusFilter] = useState<"all" | "connected" | "failed" | "pending">("all");
  const [detailIndex, setDetailIndex] = useState<number | null>(null);
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
  const gongfengCredential = gongfengCredentials[0] ?? null;
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

  useEffect(() => {
    setRepositories(workspace?.repos ?? EMPTY_REPOSITORIES);
    // 自动保存成功后缓存更新会整体替换 workspace 对象并再次走到这里；只依赖 id 触发，
    // 是为了避免服务端回包把用户在保存往返期间新敲进去的字符覆盖掉。
    // eslint-disable-next-line react-hooks/exhaustive-deps -- 有意只按 workspace 身份触发
  }, [workspace?.id]);

  useEffect(() => {
    if (center?.repos) setRepositories(center.repos);
  }, [center?.repos]);

  const bindingCount = useCallback((repo: WorkspaceRepo) => {
    const identity = repositoryIdentity(repo.url);
    if (!identity || !center || !Array.isArray(center.projects)) return 0;
    return center.projects.reduce((count, project) => count + project.resources.filter((resource) => {
      const ref = resource.resource_ref as { url?: unknown };
      return typeof ref.url === "string" && repositoryIdentity(ref.url) === identity;
    }).length, 0);
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
      if (!workspace) return;
      setRetryRepositories(next);
      setSaveStatus("saving");
      try {
        const updated = await api.updateWorkspace(workspace.id, { repos: next });
        queryClient.setQueryData(workspaceKeys.list(), (old: Workspace[] | undefined) => old?.map((item) => item.id === updated.id ? updated : item));
        setRetryRepositories(null);
        setSaveStatus("saved");
        toast.success(t(($) => $.repositories.toast_saved), { id: "repository-center-save" });
      } catch (error) {
        setSaveStatus("error");
        throw error;
      }
    },
    [queryClient, t, workspace],
  );
  const visibleRepositories = useMemo(() => repositories.filter((repo) => {
    const provider = normalizeWorkspaceRepoProvider(repo.provider);
    const status = repositoryStatus(repo);
    const query = listSearch.trim().toLowerCase();
    return (providerFilter === "all" || provider === providerFilter) &&
      (statusFilter === "all" || status === statusFilter) &&
      (!query || `${repo.url} ${repo.project_path ?? ""} ${repo.description ?? ""}`.toLowerCase().includes(query));
  }), [repositories, listSearch, providerFilter, statusFilter]);
  const detailRepository = detailIndex !== null ? repositories[detailIndex] ?? null : null;
  const detailProvider = detailRepository ? normalizeWorkspaceRepoProvider(detailRepository.provider) : "unknown";
  const detailProviderConnected = detailProvider === "github" ? githubConnected : detailProvider === "gongfeng" ? gongfengConnected : false;
  const detailBranchesQuery = useQuery({
    queryKey: ["workspace-repository-branches", wsId, detailRepository?.url ?? ""],
    queryFn: () => api.probeWorkspaceRepo(wsId, { url: detailRepository!.url }),
    enabled: !!wsId && !!detailRepository && detailTab === "branches" && canManageWorkspace && detailProviderConnected,
  });

  const openRepositoryDetails = (index: number, tab = "overview") => {
    setDetailTab(tab);
    setDetailIndex(index);
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

  const pendingRemovalRepository = pendingRemovalIndex !== null ? repositories[pendingRemovalIndex] ?? null : null;
  const pendingRemovalBindingCount = pendingRemovalRepository ? (centerPending ? null : bindingCount(pendingRemovalRepository)) : 0;

  // 「连接」按钮：先让服务端探活这个工蜂仓库，拿到默认分支与分支列表，供用户确认后导入。
  const probeGongfengRepository = async () => {
    if (!workspace || !gongfengURL.trim() || gongfengPending) return;
    setGongfengPending(true);
    try {
      const result = await api.probeWorkspaceRepo(workspace.id, {
        url: gongfengURL.trim(),
      });
      setGongfengProbe(result);
      setGongfengBranch(result.default_branch || result.branches[0] || "");
    } catch (error) {
      setGongfengProbe(null);
      setGongfengBranch("");
      toast.error(error instanceof Error ? error.message : "工蜂仓库连接失败");
    } finally {
      setGongfengPending(false);
    }
  };

  // 「导入」按钮：把已探活的工蜂仓库解析成工作区仓库记录，追加进列表并立刻自动保存。
  const importGongfengRepository = async () => {
    if (!workspace || !gongfengProbe || !gongfengBranch || gongfengPending) return;
    setGongfengPending(true);
    try {
      const resolved = await api.resolveWorkspaceRepo(workspace.id, {
        url: gongfengProbe.url,
        default_branch: gongfengBranch,
      });
      const identity = repositoryIdentity(resolved.url);
      if (identity && existingRepositoryIdentities.has(identity)) {
        toast.error("这个工蜂仓库已经添加过了");
        return;
      }
      const next = [...repositories, resolved];
      await saveRepositories(next);
      setRepositories(next);
      setGongfengURL("");
      setGongfengProbe(null);
      setGongfengBranch("");
      setGongfengPickerOpen(false);
      toast.success("工蜂仓库已添加");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "添加工蜂仓库失败");
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
    // 已装过 GitHub App 就直接弹出"选仓库"弹窗；没装过则先跳转 OAuth 授权（在新窗口完成）。
    if (githubInstallations.length > 0) {
      openGitHubPicker();
      return;
    }
    setConnectingGitHub(true);
    try {
      const response = await api.getGitHubConnectURL(wsId, "repositories");
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
    // 只导入尚未添加且未归档的仓库：按"身份标识"去重，避免同一仓库被重复加进列表。
    const additions: WorkspaceRepo[] = [];
    const known = new Set(existingRepositoryIdentities);
    for (const repository of selectedRepositories.values()) {
      const identity = repositoryIdentity(repository.clone_url);
      if (!identity || known.has(identity) || repository.archived) continue;
      known.add(identity);
      additions.push({
        url: repository.clone_url,
        ...(repository.description?.trim()
          ? { description: repository.description.trim() }
          : {}),
      });
    }
    if (additions.length === 0) {
      closeGitHubPicker();
      return;
    }
    const next = [...repositories, ...additions];
    try {
      await saveRepositories(next);
      setRepositories(next);
      closeGitHubPicker();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "GitHub 仓库保存失败");
    }
  };

  const removeRepository = async (index: number) => {
    const next = repositories.filter((_, repoIndex) => repoIndex !== index);
    await saveRepositories(next);
    setRepositories(next);
  };

  if (!workspace) return null;

  const repositoryStats = [
    { key: "all", label: "全部仓库", value: repositories.length },
    { key: "github", label: "GitHub", value: repositories.filter((r) => normalizeWorkspaceRepoProvider(r.provider) === "github").length },
    { key: "gongfeng", label: "工蜂", value: repositories.filter((r) => normalizeWorkspaceRepoProvider(r.provider) === "gongfeng").length },
    { key: "error", label: "连接异常", value: repositories.filter((r) => repositoryStatus(r) === "failed").length },
  ] as const;

  return (
    <>
    <div className="space-y-8">
      <header className="flex items-start justify-between gap-5">
        <div className="min-w-0">
          <div className="text-caption font-semibold uppercase tracking-[0.14em] text-muted-foreground">Code repositories</div>
          <h2 className="mt-1 text-title-lg font-semibold tracking-tight">代码仓库</h2>
          <p className="mt-1 max-w-2xl text-body leading-6 text-muted-foreground">把代码来源连接起来，让智能体在各个运行环境中开始工作。</p>
        </div>
        {canManageWorkspace ? <Button size="sm" className="mt-1 shrink-0" disabled={connectingGitHub} onClick={() => setAddRepositoryOpen(true)}><Plus className="size-3.5" />添加仓库</Button> : null}
      </header>

      <div className="grid gap-3 md:grid-cols-2">
        {[
          {
            provider: "github",
            label: "GitHub",
            connected: githubConnected,
            status: githubInstallationsPending ? "读取中" : githubConnected ? "已连接" : githubConnectConfigured ? "未连接" : "未配置",
            caption: githubConnected ? `GitHub App · ${githubInstallations.length} 个安装` : githubConnectConfigured ? "连接 GitHub App 后读取仓库" : "GitHub App 尚未配置",
          },
          {
            provider: "gongfeng",
            label: "腾讯工蜂",
            connected: gongfengConnected,
            status: gongfengCredentialsPending ? "读取中" : gongfengConnected ? "已连接" : gongfengCredential?.status === "failed" ? "连接异常" : gongfengCredential ? "待验证" : "未配置",
            caption: gongfengConnected ? "访问令牌 · 已验证" : gongfengCredential?.status === "failed" ? "访问令牌验证失败" : gongfengCredential ? "访问令牌 · 待验证" : "尚未配置访问令牌",
          },
        ].map((connection) => (
          <div key={connection.provider} className="flex min-w-0 items-center gap-3 rounded-xl border border-surface-border bg-surface-raised p-4">
            <span className="grid size-10 shrink-0 place-items-center rounded-lg border border-surface-border bg-surface-hover text-body font-semibold">{connection.provider === "github" ? <GitHubMark className="size-5" /> : <GitBranch className="size-5" />}</span>
            <div className="min-w-0 flex-1"><p className="font-medium">{connection.label}</p><p className="truncate text-caption text-muted-foreground">{connection.caption}</p></div>
            <Badge variant={connection.connected ? "secondary" : connection.status === "连接异常" ? "destructive" : "outline"}>{connection.status}</Badge>
            <Button variant="ghost" size="icon-sm" aria-label={`管理${connection.label}`} disabled={!canManageWorkspace} onClick={() => { if (connection.provider === "github") void handleGitHubAction(); else openCredentialSettings(); }}><ExternalLink className="size-4" /></Button>
          </div>
        ))}
      </div>

      <div className="flex flex-wrap items-center gap-1 pb-1">
        {repositoryStats.map((stat) => {
          const active = stat.key === "error" ? statusFilter === "failed" : stat.key === "all" ? providerFilter === "all" && statusFilter === "all" : providerFilter === stat.key && statusFilter === "all";
          return <button key={stat.key} type="button" className={`inline-flex min-h-8 items-center gap-2 rounded-md border-0 px-2.5 text-caption transition-colors hover:bg-surface-hover ${active ? "bg-surface-selected font-semibold text-surface-selected-foreground" : "text-muted-foreground"}`} onClick={() => { if (stat.key === "error") { setProviderFilter("all"); setStatusFilter("failed"); } else { setProviderFilter(stat.key === "all" ? "all" : stat.key); setStatusFilter("all"); } }}><span>{stat.label}</span><span className="min-w-6 rounded border border-surface-border bg-surface-raised px-1.5 text-center tabular-nums text-foreground">{stat.value}</span></button>;
        })}
      </div>

      <div className="overflow-hidden rounded-xl border border-surface-border bg-surface-raised shadow-none">
          <div className="flex flex-wrap gap-2 border-b px-4 py-3">
            <div className="relative min-w-[min(100%,18rem)] flex-1">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input value={listSearch} onChange={(event) => setListSearch(event.target.value)} placeholder="搜索仓库、组织或路径" aria-label="搜索仓库" className="pl-8" />
            </div>
            <Select value={providerFilter} onValueChange={(value) => setProviderFilter((value ?? "all") as typeof providerFilter)} items={[{ value: "all", label: "全部来源" }, { value: "github", label: "GitHub" }, { value: "gongfeng", label: "工蜂" }, { value: "unknown", label: "其他 Git" }]}><SelectTrigger className="w-32" aria-label="来源筛选"><SelectValue /></SelectTrigger><SelectContent /></Select>
            <Select value={statusFilter} onValueChange={(value) => setStatusFilter((value ?? "all") as typeof statusFilter)} items={[{ value: "all", label: "全部状态" }, { value: "connected", label: "已连接" }, { value: "failed", label: "连接异常" }, { value: "pending", label: "待验证" }]}><SelectTrigger className="w-32" aria-label="状态筛选"><SelectValue /></SelectTrigger><SelectContent /></Select>
          </div>

          <div className="hidden grid-cols-[minmax(0,1fr)_9rem_9rem_7rem_2.5rem] gap-4 border-b border-surface-border bg-surface-sunken px-5 py-2.5 text-caption text-muted-foreground lg:grid"><span>仓库</span><span>默认分支</span><span>连接状态</span><span>绑定项目</span><span /></div>
          {centerPending ? (
            <div className="flex items-center justify-center gap-2 px-4 py-12 text-caption text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />正在读取仓库</div>
          ) : centerError ? (
            <div className="flex flex-col items-center gap-3 px-4 py-12 text-center text-caption text-muted-foreground"><p>暂时无法读取仓库详情</p><Button variant="outline" size="sm" onClick={() => void refetchCenter()}>重新加载</Button></div>
          ) : repositories.length === 0 ? (
            <div className="flex flex-col items-center px-5 py-14 text-center">
              <FolderGit2 className="mb-4 size-8 text-muted-foreground" />
              <h3 className="text-body font-semibold">连接你的第一份代码</h3>
              <p className="mt-2 max-w-md text-caption leading-5 text-muted-foreground">添加 GitHub 或工蜂仓库，智能体就能在运行环境中按需拉取代码、创建 worktree，并以分支交付成果。</p>
              {canManageWorkspace ? <div className="mt-5 flex flex-wrap justify-center gap-2"><Button variant="outline" size="sm" onClick={() => void handleGitHubAction()}><GitHubMark className="size-4" />从 GitHub 选择</Button><Button variant="outline" size="sm" onClick={openGongfengFlow}><GitBranch className="size-4" />添加工蜂仓库</Button></div> : null}
            </div>
          ) : null}

          {visibleRepositories.map((repository) => {
            const index = repositories.indexOf(repository);
            const provider = normalizeWorkspaceRepoProvider(repository.provider);
            const status = repositoryStatus(repository);
            return (
            <div
              key={index}
              className="grid gap-2 border-b border-surface-border px-4 py-4 lg:grid-cols-[minmax(0,1fr)_9rem_9rem_7rem_2.5rem] lg:items-center lg:gap-4 lg:px-5"
            >
              <div className="flex min-w-0 items-center gap-2">
                <span className="grid size-8 shrink-0 place-items-center rounded-lg border border-surface-border bg-surface-hover text-muted-foreground">{provider === "github" ? <GitHubMark className="size-4" /> : <GitBranch className="size-4" />}</span>
                <button type="button" className="min-w-0 text-left" onClick={() => openRepositoryDetails(index)}><span className="flex flex-wrap items-center gap-2 text-body font-medium"><span className="truncate">{repositoryDisplayName(repository)}</span>{repositoryIsArchived(repository) ? <Badge variant="outline">已归档</Badge> : null}</span><span className="block truncate text-caption text-muted-foreground">{repositoryDisplayPath(repository)} · {provider === "github" ? "GitHub" : provider === "gongfeng" ? "工蜂" : "Git"}</span></button>
              </div>
              <div className="min-w-0 lg:row-start-1 lg:col-start-2"><p className="flex items-center gap-1 truncate text-caption"><GitBranch className="size-3.5 text-muted-foreground" />{repository.default_branch || "未验证"}</p></div>
              <div className="min-w-0 lg:row-start-1 lg:col-start-3"><p className={`flex items-center gap-1.5 text-caption ${status === "connected" ? "text-success" : status === "failed" ? "text-destructive" : "text-warning"}`}><span className="size-1.5 rounded-full bg-current" />{status === "connected" ? "连接正常" : status === "failed" ? "连接失败" : "待验证"}</p><p className="truncate text-caption text-muted-foreground">{repository.last_tested_at || "尚未验证"}</p></div>
              <div className="min-w-0 text-caption text-muted-foreground lg:row-start-1 lg:col-start-4">{bindingCount(repository) ? `${bindingCount(repository)} 个项目` : "尚未绑定"}</div>
              <div className="min-w-0 lg:col-span-1 lg:row-start-2 lg:col-start-1"><p className="truncate font-mono text-caption text-muted-foreground">{repository.url}</p><p className="truncate text-caption">{repository.description || "未填写仓库说明"}</p></div>
              {canManageWorkspace ? (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={t(($) => $.repositories.delete_aria)}
                  className="justify-self-end text-muted-foreground hover:text-destructive"
                  onClick={() => setPendingRemovalIndex(index)}
                >
                  <Trash2 className="size-3.5" />
                </Button>
              ) : null}
              <Button type="button" variant="ghost" size="icon-sm" aria-label={`查看${repositoryDisplayName(repository)}详情`} className="lg:row-start-1 lg:col-start-5" onClick={() => openRepositoryDetails(index)}><ExternalLink className="size-3.5" /></Button>
            </div>
            );
          })}
          {repositories.length > 0 && visibleRepositories.length === 0 ? <div className="px-4 py-8 text-center text-caption text-muted-foreground">没有匹配的仓库</div> : null}

          <div className="flex flex-wrap items-center justify-between gap-3 border-t px-4 py-3 text-caption text-muted-foreground"><span>{visibleRepositories.length} 个仓库{!canManageWorkspace ? " · 当前为只读权限" : ""}</span><span className="flex items-center gap-1.5"><SettingsSaveState status={saveStatus} savingLabel={t(($) => $.auto_save.saving)} savedLabel={t(($) => $.auto_save.saved)} errorLabel={t(($) => $.auto_save.failed)} /></span></div>
      </div>

      {saveStatus === "error" && retryRepositories ? <div className="-mt-5 flex items-center gap-3 text-caption text-destructive"><span>保存失败，请重试。</span><Button variant="outline" size="sm" onClick={() => void saveRepositories(retryRepositories).then(() => setRepositories(retryRepositories)).catch(() => undefined)}>重试保存</Button></div> : null}

        <div className="flex items-start gap-3 rounded-lg bg-surface-sunken p-4 text-caption">
          <FolderGit2 className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0"><p className="font-medium">仓库决定代码来源，运行环境决定在哪里执行。</p><p className="mt-1 text-muted-foreground">远程任务按需拉取代码并创建独立 worktree；已有服务器目录可在项目中单独关联。</p></div>
          {canManageWorkspace ? <Button variant="ghost" size="sm" className="ml-auto shrink-0" onClick={openProjectResources}>关联本地目录 <ExternalLink className="size-3.5" /></Button> : null}
        </div>
    </div>

      <Dialog
        open={githubPickerOpen}
        onOpenChange={(open) => {
          if (!open) closeGitHubPicker();
        }}
      >
        <DialogContent className="flex max-h-[85vh] flex-col gap-0 p-0 sm:max-w-2xl">
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
                onValueChange={(value) =>
                  setSelectedInstallationID(value ?? "")
                }
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

          <div className="min-h-0 flex-1 overflow-y-auto border-y">
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
                        disabled={disabled}
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
            <Button variant="ghost" onClick={closeGitHubPicker}>
              {t(($) => $.repositories.github_cancel)}
            </Button>
            <Button
              onClick={importGitHubRepositories}
              disabled={selectedRepositories.size === 0}
            >
              {t(($) => $.repositories.github_import)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={gongfengPickerOpen}
        onOpenChange={(open) => {
          setGongfengPickerOpen(open);
          if (!open) {
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
                  value={gongfengURL}
                  onChange={(event) => {
                    setGongfengURL(event.target.value);
                    setGongfengProbe(null);
                    setGongfengBranch("");
                  }}
                  placeholder="https://git.code.tencent.com/团队/项目"
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
            <Button variant="ghost" onClick={() => setGongfengPickerOpen(false)}>
              {t(($) => $.repositories.gongfeng_cancel)}
            </Button>
            <Button
              onClick={() => void importGongfengRepository()}
              disabled={!gongfengProbe || !gongfengBranch || gongfengPending}
            >
              {t(($) => $.repositories.gongfeng_import)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Sheet open={detailIndex !== null} onOpenChange={(open) => { if (!open) setDetailIndex(null); }}>
        <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-xl">
          {detailIndex !== null && repositories[detailIndex] ? (() => { const repo = repositories[detailIndex]!; return <>
            <SheetHeader><SheetTitle className="pr-8">{repo.project_path || repo.url}</SheetTitle><SheetDescription className="break-all">{repo.url}</SheetDescription></SheetHeader>
            <div className="px-4 pb-6"><Tabs value={detailTab} onValueChange={setDetailTab}><TabsList variant="line" className="w-full justify-start"><TabsTrigger value="overview">概览</TabsTrigger><TabsTrigger value="branches">分支</TabsTrigger><TabsTrigger value="projects">项目绑定</TabsTrigger><TabsTrigger value="runtime">运行位置</TabsTrigger></TabsList>
              <TabsContent value="overview" className="space-y-4 pt-5"><div className="flex flex-wrap gap-2"><Badge variant="outline">{normalizeWorkspaceRepoProvider(repo.provider)}</Badge><Badge variant={repositoryStatus(repo) === "failed" ? "destructive" : "secondary"}>{repositoryStatus(repo) === "connected" ? "连接成功" : repositoryStatus(repo) === "failed" ? "连接失败" : "待验证"}</Badge></div><dl className="grid gap-3 text-caption sm:grid-cols-2"><div><dt className="text-muted-foreground">默认分支</dt><dd>{repo.default_branch || "未验证"}</dd></div><div><dt className="text-muted-foreground">Head commit</dt><dd className="font-mono">{repo.head_commit || "未读取"}</dd></div><div><dt className="text-muted-foreground">最近验证</dt><dd>{repo.last_tested_at || "尚未验证"}</dd></div><div><dt className="text-muted-foreground">同步状态</dt><dd>{repo.sync_status || "尚未拉取"}</dd></div></dl></TabsContent>
              <TabsContent value="branches" className="space-y-3 pt-5"><div className="flex items-center justify-between gap-3"><p className="text-caption text-muted-foreground">可用分支</p>{canManageWorkspace && detailProviderConnected ? <Button variant="outline" size="sm" onClick={() => void detailBranchesQuery.refetch()} disabled={detailBranchesQuery.isFetching}>{detailBranchesQuery.isFetching ? <LoaderCircle className="size-3.5 animate-spin" /> : null}刷新分支</Button> : null}</div>{!detailProviderConnected ? <div className="rounded-lg bg-surface-sunken p-3 text-caption text-muted-foreground">{canManageWorkspace ? "请先连接对应 provider，再读取完整分支列表。" : "当前账号没有读取分支的管理权限。"}</div> : detailBranchesQuery.isPending ? <div className="flex items-center gap-2 text-caption text-muted-foreground"><LoaderCircle className="size-4 animate-spin" />正在读取分支</div> : detailBranchesQuery.isError ? <div className="rounded-lg bg-surface-sunken p-3 text-caption text-destructive">分支读取失败，请检查仓库访问权限和 provider 凭据。</div> : detailBranchesQuery.data?.branches?.length ? <div className="divide-y rounded-lg border border-surface-border">{detailBranchesQuery.data.branches.map((branch) => <div key={branch} className="flex items-center justify-between gap-3 px-3 py-2.5 text-caption"><span className="truncate">{branch}</span>{branch === (detailBranchesQuery.data.default_branch || repo.default_branch) ? <Badge variant="secondary">默认</Badge> : null}</div>)}</div> : <><p className="font-medium">{repo.default_branch || "未验证"}</p><p className="text-caption text-muted-foreground">当前只保存了默认分支；刷新后可从 provider 读取完整分支列表。</p></>}</TabsContent>
              <TabsContent value="projects" className="space-y-3 pt-5"><p className="text-caption text-muted-foreground">使用此仓库的项目</p>{center && Array.isArray(center.projects) ? center.projects.flatMap((project) => project.resources.filter((resource) => { const ref = resource.resource_ref as { url?: unknown }; return typeof ref.url === "string" && repositoryIdentity(ref.url) === repositoryIdentity(repo.url); }).map(() => <div key={project.id} className="rounded-lg border border-surface-border p-3"><p className="font-medium">{project.title}</p><p className="text-caption text-muted-foreground">远程仓库资源 · 执行方式由项目资源决定</p></div>)) : null}{bindingCount(repo) === 0 ? <p className="text-caption text-muted-foreground">尚未绑定项目。可以从项目资源选择此仓库。</p> : null}</TabsContent>
              <TabsContent value="runtime" className="space-y-3 pt-5"><p className="flex items-center gap-2"><FolderGit2 className="size-4" />运行位置</p>{center && Array.isArray(center.locations) && Array.isArray(center.projects) ? center.locations.filter((location) => location.project_id && center.projects.some((project) => project.id === location.project_id && project.resources.some((resource) => { const ref = resource.resource_ref as { url?: unknown }; return typeof ref.url === "string" && repositoryIdentity(ref.url) === repositoryIdentity(repo.url); }))).map((location) => <div key={location.task_id} className="rounded-lg border border-surface-border p-3"><p className="font-medium">{location.runtime_name || location.daemon_id || "runtime"}</p><p className="break-all font-mono text-caption text-muted-foreground">{location.work_dir || "按需创建 worktree"}</p><p className="text-caption text-muted-foreground">{location.status} · Git 拉取状态需以 runtime 记录为准</p></div>) : null}<p className="text-caption text-muted-foreground">远程仓库不会写入本地目录；没有本地目录时，runtime 会按需创建 worktree。</p></TabsContent>
            </Tabs></div>
          </>; })() : null}
        </SheetContent>
      </Sheet>

      <Dialog open={addRepositoryOpen} onOpenChange={setAddRepositoryOpen}><DialogContent className="sm:max-w-lg"><DialogHeader><DialogTitle>添加代码仓库</DialogTitle><DialogDescription>选择代码来源。GitHub 和工蜂仓库保存为远程身份，本地目录由项目资源管理。</DialogDescription></DialogHeader><div className="grid gap-3"><Button variant="outline" className="h-auto justify-start p-4" onClick={() => { setAddRepositoryOpen(false); void handleGitHubAction(); }}><GitHubMark className="size-5" /><span><strong className="block text-left">GitHub</strong><span className="text-caption text-muted-foreground">从已安装的 GitHub App 选择仓库</span></span></Button><Button variant="outline" className="h-auto justify-start p-4" onClick={() => { setAddRepositoryOpen(false); openGongfengFlow(); }}><GitBranch className="size-5" /><span><strong className="block text-left">工蜂</strong><span className="text-caption text-muted-foreground">连接仓库并读取默认分支</span></span></Button><Button variant="outline" className="h-auto justify-start p-4" onClick={() => { setAddRepositoryOpen(false); openProjectResources(); }}><FolderGit2 className="size-5" /><span><strong className="block text-left">本地目录</strong><span className="text-caption text-muted-foreground">通过项目资源关联 runtime 目录</span></span></Button></div></DialogContent></Dialog>

      <AlertDialog
        open={pendingRemovalIndex !== null}
        onOpenChange={(open) => {
          if (!open) setPendingRemovalIndex(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {pendingRemovalBindingCount && pendingRemovalBindingCount > 0 ? "暂不能移除仓库" : t(($) => $.repositories.delete_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {pendingRemovalBindingCount === null ? "正在检查项目绑定，请稍候。" : pendingRemovalBindingCount > 0 ? `这个仓库仍绑定 ${pendingRemovalBindingCount} 个项目。请先在项目资源中解除绑定。` : t(($) => $.repositories.delete_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(($) => $.repositories.delete_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant={pendingRemovalBindingCount && pendingRemovalBindingCount > 0 ? "default" : "destructive"}
              disabled={pendingRemovalBindingCount === null}
              onClick={() => {
                if (pendingRemovalIndex !== null) {
                  if (pendingRemovalBindingCount && pendingRemovalBindingCount > 0) {
                    const index = pendingRemovalIndex;
                    setPendingRemovalIndex(null);
                    openRepositoryDetails(index, "projects");
                    return;
                  }
                  void removeRepository(pendingRemovalIndex).catch((error) => toast.error(error instanceof Error ? error.message : "移除仓库失败"));
                }
                setPendingRemovalIndex(null);
              }}
            >
              {pendingRemovalBindingCount && pendingRemovalBindingCount > 0 ? "查看项目绑定" : t(($) => $.repositories.delete_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
