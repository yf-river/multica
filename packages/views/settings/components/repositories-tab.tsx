// 本文件是「代码仓库」设置标签页：维护当前工作区关联的仓库列表。
// 支持手动填写 git 地址、从 GitHub 安装里批量勾选导入、以及探活后导入工蜂（腾讯 Git）仓库；
// 列表增删改通过 useAutoSave 自动保存到服务端。

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
  SettingsCard,
  SettingsSaveState,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";
import { useAutoSave } from "./use-auto-save";
import { GitHubMark } from "./github-mark";

// 空列表的默认值：复用同一个常量引用，方便自动保存时做引用级比较判断"有没有改动"。
const EMPTY_REPOSITORIES: WorkspaceRepo[] = [];

/** 判断两份仓库列表在关键字段上是否逐项一致，供自动保存对比前后差异。 */
function repositoriesEqual(left: WorkspaceRepo[], right: WorkspaceRepo[]) {
  if (left.length !== right.length) return false;
  const fields: Array<keyof WorkspaceRepo> = [
    "url", "description", "provider", "project_path", "default_branch", "ref",
    "head_commit", "connection_status", "sync_status", "test_status",
    "last_tested_at", "last_synced_at",
  ];
  return left.every((repo, index) => {
    const other = right[index];
    return !!other && fields.every((field) => (repo[field] ?? "") === (other[field] ?? ""));
  });
}

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

/**
 * 「代码仓库」设置标签页：展示并编辑当前工作区的仓库列表，支持手动添加/删除、
 * 从已安装的 GitHub App 中搜索勾选批量导入、以及探活后导入工蜂仓库；改动自动保存。
 */
export function RepositoriesTab() {
  const { t } = useT("settings");
  const user = useAuthStore((state) => state.user);
  const workspace = useCurrentWorkspace();
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const navigation = useNavigation();
  const { data: members = [] } = useQuery(memberListOptions(wsId));
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

  const currentMember = members.find((member) => member.user_id === user?.id) ?? null;
  const canManageWorkspace =
    currentMember?.role === "owner" || currentMember?.role === "admin";
  const {
    data: githubData,
    isPending: githubInstallationsPending,
    isFetching: githubInstallationsFetching,
  } = useQuery({
    ...githubInstallationsOptions(wsId),
    enabled: !!wsId && canManageWorkspace,
  });
  const githubInstallations = useMemo(
    () => githubData?.installations ?? [],
    [githubData?.installations],
  );
  const githubConnectConfigured = githubData?.configured === true;
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

  const savedRepositories = workspace?.repos ?? EMPTY_REPOSITORIES;
  const draft = useMemo(() => repositories, [repositories]);
  const saveRepositories = useCallback(
    async (next: WorkspaceRepo[]) => {
      if (!workspace) return;
      const updated = await api.updateWorkspace(workspace.id, { repos: next });
      queryClient.setQueryData(
        workspaceKeys.list(),
        (old: Workspace[] | undefined) =>
          old?.map((item) => (item.id === updated.id ? updated : item)),
      );
    },
    [queryClient, workspace],
  );
  const allUrlsValid = repositories.every((repo) => repo.url.trim().length > 0);
  const visibleRepositories = useMemo(() => repositories.filter((repo) => {
    const provider = normalizeWorkspaceRepoProvider(repo.provider);
    const status = repositoryStatus(repo);
    const query = listSearch.trim().toLowerCase();
    return (providerFilter === "all" || provider === providerFilter) &&
      (statusFilter === "all" || status === statusFilter) &&
      (!query || `${repo.url} ${repo.project_path ?? ""} ${repo.description ?? ""}`.toLowerCase().includes(query));
  }), [repositories, listSearch, providerFilter, statusFilter]);
  const autoSave = useAutoSave({
    value: draft,
    savedValue: savedRepositories,
    onSave: saveRepositories,
    onSuccess: () =>
      toast.success(t(($) => $.repositories.toast_saved), {
        id: "settings-auto-save",
      }),
    onError: (error) =>
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.repositories.toast_save_failed),
      ),
    enabled: !!workspace && canManageWorkspace && allUrlsValid,
    isEqual: repositoriesEqual,
  });

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
      setRepositories(next);
      await autoSave.saveNow(next);
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

  const importGitHubRepositories = () => {
    if (!allUrlsValid) {
      toast.error(t(($) => $.repositories.complete_manual_entry_first));
      return;
    }
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
    setRepositories(next);
    autoSave.saveNow(next);
    closeGitHubPicker();
  };

  const removeRepository = (index: number) => {
    const next = repositories.filter((_, repoIndex) => repoIndex !== index);
    setRepositories(next);
    autoSave.saveNow(next);
  };

  if (!workspace) return null;

  return (
    <SettingsTab title={t(($) => $.page.tabs.repositories)}>
      <SettingsSection
        description={t(($) => $.repositories.description)}
        action={
          <SettingsSaveState
            status={autoSave.status}
            savingLabel={t(($) => $.auto_save.saving)}
            savedLabel={t(($) => $.auto_save.saved)}
            errorLabel={t(($) => $.auto_save.failed)}
          />
        }
      >
        <SettingsCard>
          <div className="grid grid-cols-2 gap-2 border-b px-4 py-3 sm:grid-cols-4">
            {(["总数", repositories.length, "GitHub", repositories.filter((r) => normalizeWorkspaceRepoProvider(r.provider) === "github").length, "工蜂", repositories.filter((r) => normalizeWorkspaceRepoProvider(r.provider) === "gongfeng").length, "连接异常", repositories.filter((r) => repositoryStatus(r) === "failed").length] as const).map((item, index) => typeof item === "number" ? <div key={index} className="text-title font-semibold">{item}</div> : <div key={index} className="text-caption text-muted-foreground">{item}</div>)}
          </div>
          <div className="flex flex-wrap gap-2 border-b px-4 py-3">
            <div className="relative min-w-[min(100%,18rem)] flex-1">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input value={listSearch} onChange={(event) => setListSearch(event.target.value)} placeholder="搜索仓库、组织或路径" aria-label="搜索仓库" className="pl-8" />
            </div>
            <Select value={providerFilter} onValueChange={(value) => setProviderFilter((value ?? "all") as typeof providerFilter)} items={[{ value: "all", label: "全部来源" }, { value: "github", label: "GitHub" }, { value: "gongfeng", label: "工蜂" }, { value: "unknown", label: "其他 Git" }]}><SelectTrigger className="w-32" aria-label="来源筛选"><SelectValue /></SelectTrigger><SelectContent /></Select>
            <Select value={statusFilter} onValueChange={(value) => setStatusFilter((value ?? "all") as typeof statusFilter)} items={[{ value: "all", label: "全部状态" }, { value: "connected", label: "已连接" }, { value: "failed", label: "连接异常" }, { value: "pending", label: "待验证" }]}><SelectTrigger className="w-32" aria-label="状态筛选"><SelectValue /></SelectTrigger><SelectContent /></Select>
          </div>
          <div className="flex flex-wrap gap-2 border-b px-4 py-3 text-caption">
            <Badge variant={githubConnectConfigured ? "secondary" : "outline"}>GitHub {githubConnectConfigured ? "已连接" : "未配置"}</Badge>
            <Badge variant="outline">工蜂 {gongfengProbe ? "API 已连接" : "凭据状态待验证"}</Badge>
          </div>
          <div className="flex items-center justify-between gap-3 border-b px-4 py-3">
            <div><p className="text-body font-medium">代码来源</p><p className="text-caption text-muted-foreground">远程仓库负责代码来源，runtime 决定执行位置。</p></div>
            {canManageWorkspace ? <Button size="sm" disabled={connectingGitHub} onClick={() => setAddRepositoryOpen(true)}><Plus className="size-3.5" />添加仓库</Button> : null}
          </div>
          {repositories.length === 0 ? (
            <div className="px-4 py-8 text-center text-caption text-muted-foreground">
              {t(($) => $.repositories.empty)}
            </div>
          ) : null}

          {visibleRepositories.map((repository) => {
            const index = repositories.indexOf(repository);
            const provider = normalizeWorkspaceRepoProvider(repository.provider);
            const status = repositoryStatus(repository);
            return (
            <div
              key={index}
              className="grid gap-2 px-4 py-3.5 sm:grid-cols-[minmax(0,1fr)_minmax(0,0.8fr)_auto] sm:items-center"
            >
              <div className="flex min-w-0 items-center gap-2">
                <Badge variant="outline">{provider === "github" ? "GitHub" : provider === "gongfeng" ? "工蜂" : "Git"}</Badge>
                <button type="button" className="min-w-0 truncate text-left text-body font-medium hover:underline" onClick={() => setDetailIndex(index)}>{repository.project_path || repository.url}</button>
                <Badge variant={status === "connected" ? "secondary" : status === "failed" ? "destructive" : "outline"}>{status === "connected" ? "已连接" : status === "failed" ? "连接异常" : "待验证"}</Badge>
              </div>
              <div className="min-w-0"><p className="truncate font-mono text-caption text-muted-foreground">{repository.url}</p><p className="truncate text-caption">{repository.description || "未填写仓库说明"}</p></div>
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
              <Button type="button" variant="ghost" size="sm" onClick={() => setDetailIndex(index)}><ExternalLink className="size-3.5" />详情</Button>
            </div>
            );
          })}
          {repositories.length > 0 && visibleRepositories.length === 0 ? <div className="px-4 py-8 text-center text-caption text-muted-foreground">没有匹配的仓库</div> : null}

          {canManageWorkspace ? (
            <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3.5">
              {!allUrlsValid ? (
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.repositories.url_empty)}
                </span>
              ) : null}
            </div>
          ) : (
            <div className="px-4 py-3 text-caption text-muted-foreground">
              {t(($) => $.repositories.manage_hint)}
            </div>
          )}
        </SettingsCard>
      </SettingsSection>

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
              disabled={selectedRepositories.size === 0 || !allUrlsValid}
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
              <TabsContent value="branches" className="space-y-3 pt-5"><p className="text-caption text-muted-foreground">默认分支</p><p className="font-medium">{repo.default_branch || "未验证"}</p><p className="text-caption text-muted-foreground">分支读取由 provider 连接流程完成；runtime 拉取状态独立显示。</p></TabsContent>
              <TabsContent value="projects" className="space-y-3 pt-5"><p className="text-caption text-muted-foreground">当前工作区可确认信息</p><p>{repo.project_path ? `项目路径：${repo.project_path}` : "暂无项目绑定聚合数据"}</p></TabsContent>
              <TabsContent value="runtime" className="space-y-3 pt-5"><p className="flex items-center gap-2"><FolderGit2 className="size-4" />运行位置由项目资源配置</p><p className="text-caption text-muted-foreground">远程仓库不会写入本地目录；没有本地目录时，runtime 会按需创建 worktree。</p></TabsContent>
            </Tabs></div>
          </>; })() : null}
        </SheetContent>
      </Sheet>

      <Dialog open={addRepositoryOpen} onOpenChange={setAddRepositoryOpen}><DialogContent className="sm:max-w-lg"><DialogHeader><DialogTitle>添加代码仓库</DialogTitle><DialogDescription>选择代码来源。GitHub 和工蜂仓库保存为远程身份，本地目录由项目资源管理。</DialogDescription></DialogHeader><div className="grid gap-3"><Button variant="outline" className="h-auto justify-start p-4" onClick={() => { setAddRepositoryOpen(false); void handleGitHubAction(); }}><GitHubMark className="size-5" /><span><strong className="block text-left">GitHub</strong><span className="text-caption text-muted-foreground">从已安装的 GitHub App 选择仓库</span></span></Button><Button variant="outline" className="h-auto justify-start p-4" onClick={() => { setAddRepositoryOpen(false); setGongfengPickerOpen(true); }}><GitBranch className="size-5" /><span><strong className="block text-left">工蜂</strong><span className="text-caption text-muted-foreground">连接仓库并读取默认分支</span></span></Button><Button variant="outline" className="h-auto justify-start p-4" onClick={() => { setAddRepositoryOpen(false); toast.info("本地目录请从项目资源入口关联"); }}><FolderGit2 className="size-5" /><span><strong className="block text-left">本地目录</strong><span className="text-caption text-muted-foreground">通过项目资源关联 runtime 目录</span></span></Button></div></DialogContent></Dialog>

      <AlertDialog
        open={pendingRemovalIndex !== null}
        onOpenChange={(open) => {
          if (!open) setPendingRemovalIndex(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.repositories.delete_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.repositories.delete_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t(($) => $.repositories.delete_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (pendingRemovalIndex !== null) {
                  removeRepository(pendingRemovalIndex);
                }
                setPendingRemovalIndex(null);
              }}
            >
              {t(($) => $.repositories.delete_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}
