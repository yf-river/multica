"use client";

import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import {
  Ban,
  CheckCircle2,
  FolderGit,
  Info,
  KeyRound,
  Power,
  RefreshCw,
  Save,
  Trash2,
} from "lucide-react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  externalCredentialProfilesOptions,
  useCreateExternalCredentialProfile,
  useDeleteExternalCredentialProfile,
  useUpdateExternalCredentialProfile,
} from "@multica/core/external-credentials";
import {
  projectListOptions,
  projectResourcesOptions,
  useDeleteProjectResource,
  useDisableProjectResource,
  useEnableProjectResource,
  useSyncProjectResource,
  useTestProjectResource,
} from "@multica/core/projects";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type {
  ExternalCredentialProfile,
  GongfengRepoResourceRef,
  Project,
  ProjectResource,
} from "@multica/core/types";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { AppLink } from "../../navigation";

function isGongfengResource(resource: ProjectResource): resource is ProjectResource & {
  resource_ref: GongfengRepoResourceRef;
} {
  return resource.resource_type === "gongfeng_repo";
}

type GongfengResourceRow = ProjectResource & {
  resource_ref: GongfengRepoResourceRef;
  project: Project;
};

export function ProjectGongfengRepositories() {
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const resourceQueries = useQueries({
    queries: projects.map((project) => projectResourcesOptions(wsId, project.id)),
  });

  const rows = useMemo<GongfengResourceRow[]>(() => {
    return resourceQueries.flatMap((query, idx) => {
      const project = projects[idx];
      if (!project) return [];
      return (query.data ?? [])
        .filter(isGongfengResource)
        .map((resource) => ({ ...resource, project }));
    });
  }, [projects, resourceQueries]);

  return (
    <section className="space-y-3" data-testid="settings-gongfeng-repository-inventory">
      <div>
        <div className="flex items-center gap-2">
          <FolderGit className="size-4 text-muted-foreground" />
          <h3 className="text-sm font-medium">已关联工蜂仓库</h3>
          <span className="font-mono text-xs tabular-nums text-muted-foreground">
            {rows.length}
          </span>
        </div>
        <p className="text-xs text-muted-foreground">
          项目资源中的 Gongfeng 仓库会作为智能体任务、训练评估和证据链的代码来源。
        </p>
      </div>
      <GongfengCredentialPanel />
      {rows.length === 0 ? (
        <div className="rounded-md border border-dashed px-3 py-6 text-center text-xs text-muted-foreground">
          暂无已关联的项目工蜂仓库。
        </div>
      ) : (
        <div className="divide-y rounded-md border bg-background">
          {rows.map((row) => (
            <GongfengRepositoryRow
              key={row.id}
              row={row}
              projectHref={wsPaths.projectDetail(row.project.id)}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function GongfengCredentialPanel() {
  const { data } = useQuery(externalCredentialProfilesOptions("gongfeng"));
  const createProfile = useCreateExternalCredentialProfile("gongfeng");
  const updateProfile = useUpdateExternalCredentialProfile("gongfeng");
  const deleteProfile = useDeleteExternalCredentialProfile("gongfeng");
  const profiles = data?.profiles ?? [];
  const profile =
    profiles.find((item) => item.provider === "gongfeng" && item.secret_binding?.configured) ??
    profiles.find((item) => item.provider === "gongfeng");
  const configured = Boolean(profile?.secret_binding?.configured);
  const [mode, setMode] = useState<"token" | "secret_ref">("token");
  const [token, setToken] = useState("");
  const [secretRef, setSecretRef] = useState("GONGFENG_ACCESS_TOKEN");
  const pending = createProfile.isPending || updateProfile.isPending || deleteProfile.isPending;

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const isToken = mode === "token";
    const credentialValue = isToken ? token.trim() : normalizeEnvName(secretRef);
    if (!credentialValue) {
      toast.error(isToken ? "请输入工蜂访问令牌" : "请输入服务端环境变量名");
      return;
    }
    const payload = {
      name: profile?.name || "gongfeng-default",
      capabilities: { mcp_server: "gongfeng", source: "settings-repositories" },
      verify_now: true,
      ...(isToken ? { token: credentialValue } : { secret_ref: `env:${credentialValue}` }),
    };
    const options = {
      onSuccess: (saved: ExternalCredentialProfile) => {
        if (saved.status === "failed") {
          toast.error(saved.last_error || "工蜂凭据不可用");
        } else {
          toast.success("工蜂凭据已保存");
        }
        setToken("");
      },
      onError: (err: unknown) =>
        toast.error(err instanceof Error ? err.message : "保存工蜂凭据失败"),
    };
    if (profile) {
      updateProfile.mutate({ id: profile.id, data: payload }, options);
    } else {
      createProfile.mutate({ provider: "gongfeng", ...payload }, options);
    }
  };

  return (
    <div className="rounded-md border bg-muted/20 p-3 text-xs">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
        <div className="space-y-1">
          <div className="flex flex-wrap items-center gap-2 font-medium">
            <KeyRound className="size-4 text-muted-foreground" />
            <span>工蜂访问凭据</span>
            <CredentialStatus profile={profile} configured={configured} />
          </div>
          <p className="max-w-3xl text-muted-foreground">
            连接显示“需要凭据”表示工蜂返回登录/鉴权页，服务可达但还不能读取私有仓库内容。这里配置账号级工蜂 token 后，智能体运行时会注入 GONGFENG_ACCESS_TOKEN / GONGFENG_PRIVATE_TOKEN。
          </p>
          {profile?.last_error && (
            <p className={profile.status === "failed" ? "text-destructive" : "text-muted-foreground"}>
              {profile.last_error}
            </p>
          )}
        </div>
        {profile && (
          <button
            type="button"
            className="inline-flex h-7 items-center justify-center rounded-md border px-2 text-[11px] text-destructive hover:bg-destructive/10 disabled:cursor-not-allowed disabled:opacity-50"
            disabled={pending}
            onClick={() => {
              deleteProfile.mutate(profile, {
                onSuccess: () => toast.success("工蜂凭据已移除"),
                onError: (err) =>
                  toast.error(err instanceof Error ? err.message : "移除工蜂凭据失败"),
              });
            }}
          >
            移除凭据
          </button>
        )}
      </div>

      <form className="mt-3 grid gap-2 lg:grid-cols-[auto_minmax(220px,1fr)_auto]" onSubmit={handleSubmit}>
        <div className="inline-flex w-fit rounded-md border bg-background p-0.5">
          <button
            type="button"
            className={`h-7 rounded px-2 text-[11px] ${mode === "token" ? "bg-foreground text-background" : "text-muted-foreground hover:bg-muted"}`}
            onClick={() => setMode("token")}
          >
            访问令牌
          </button>
          <button
            type="button"
            className={`h-7 rounded px-2 text-[11px] ${mode === "secret_ref" ? "bg-foreground text-background" : "text-muted-foreground hover:bg-muted"}`}
            onClick={() => setMode("secret_ref")}
          >
            环境变量
          </button>
        </div>
        {mode === "token" ? (
          <input
            type="password"
            value={token}
            onChange={(event) => setToken(event.target.value)}
            placeholder={configured ? "输入新 token 可替换当前凭据" : "粘贴工蜂 access token"}
            className="h-8 min-w-0 rounded-md border bg-background px-2 text-xs outline-none focus:ring-2 focus:ring-ring"
            autoComplete="off"
          />
        ) : (
          <input
            type="text"
            value={secretRef}
            onChange={(event) => setSecretRef(event.target.value)}
            placeholder="GONGFENG_ACCESS_TOKEN"
            className="h-8 min-w-0 rounded-md border bg-background px-2 font-mono text-xs outline-none focus:ring-2 focus:ring-ring"
          />
        )}
        <button
          type="submit"
          className="inline-flex h-8 items-center justify-center gap-1 rounded-md bg-foreground px-3 text-xs text-background hover:bg-foreground/90 disabled:cursor-not-allowed disabled:opacity-50"
          disabled={pending}
        >
          <Save className="size-3.5" />
          保存凭据
        </button>
      </form>
    </div>
  );
}

function normalizeEnvName(value: string): string {
  return value.trim().replace(/^env:/i, "").trim();
}

function CredentialStatus({
  profile,
  configured,
}: {
  profile: ExternalCredentialProfile | undefined;
  configured: boolean;
}) {
  if (!profile) {
    return (
      <span className="rounded border px-1.5 py-0.5 text-[11px] font-normal text-muted-foreground">
        未设置
      </span>
    );
  }
  const hint = profile.secret_binding?.hint;
  const label =
    profile.status === "failed"
      ? "校验失败"
      : configured
        ? "已设置"
        : "未设置";
  const tone =
    profile.status === "failed"
      ? "border-red-200 bg-red-50 text-red-700"
      : "border bg-background text-muted-foreground";
  return (
    <span className={`rounded px-1.5 py-0.5 text-[11px] font-normal ${tone}`}>
      {label}
      {hint ? ` · ${hint}` : ""}
    </span>
  );
}

function GongfengRepositoryRow({
  row,
  projectHref,
}: {
  row: GongfengResourceRow;
  projectHref: string;
}) {
  const wsId = useWorkspaceId();
  const deleteResource = useDeleteProjectResource(wsId, row.project.id);
  const testResource = useTestProjectResource(wsId, row.project.id);
  const syncResource = useSyncProjectResource(wsId, row.project.id);
  const disableResource = useDisableProjectResource(wsId, row.project.id);
  const enableResource = useEnableProjectResource(wsId, row.project.id);
  const [detailOpen, setDetailOpen] = useState(false);
  const ref = row.resource_ref;
  const branch = ref.branch || ref.ref || "";
  const commit = ref.commit_sha || ref.head_commit || "";
  const title = row.label || ref.title || ref.project_path || ref.url;
  const statusItems = [
    { label: "连接", value: ref.connection_status },
    { label: "同步", value: ref.sync_status },
    { label: "测试", value: ref.test_status },
  ].filter((item): item is { label: string; value: string } => Boolean(item.value));

  return (
    <>
      <div
        className="flex flex-col gap-3 px-3 py-3 text-xs sm:flex-row sm:items-center sm:justify-between"
        data-testid="settings-gongfeng-repository-row"
      >
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
            <a
              href={ref.url}
              target="_blank"
              rel="noopener noreferrer"
              className="min-w-0 max-w-full truncate font-medium hover:underline"
            >
              {title}
            </a>
            <span className="rounded border px-1.5 py-0.5 font-mono text-[11px] leading-none">
              {ref.resource_kind}
            </span>
          </div>
          <div className="min-w-0 break-all font-mono text-[11px] text-muted-foreground sm:break-normal sm:truncate">
            {ref.project_path}
          </div>
          <div className="flex min-w-0 flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
            <span>项目</span>
            <AppLink href={projectHref} className="font-medium text-foreground hover:underline">
              {row.project.title}
            </AppLink>
            {branch && <MetaBadge>{branch}</MetaBadge>}
            {commit && <MetaBadge>{shortCommit(commit)}</MetaBadge>}
          </div>
        </div>

        <div className="flex shrink-0 flex-wrap items-center gap-2 sm:justify-end">
          <div className="flex flex-wrap gap-1">
            {statusItems.length === 0 ? (
              <span className="text-[11px] text-muted-foreground">状态未采集</span>
            ) : (
              statusItems.map((item) => (
                <StatusBadge key={item.label} label={item.label} value={item.value} />
              ))
            )}
          </div>
          <div className="flex flex-wrap items-center justify-end gap-1">
            <ActionButton label="查看仓库详情" onClick={() => setDetailOpen(true)}>
              <Info className="size-3.5" />
              详情
            </ActionButton>
            <ActionButton
              label="测试连接"
              disabled={testResource.isPending || Boolean(ref.disabled)}
              onClick={() => {
                testResource.mutate(row.id, {
                  onSuccess: () => toast.success("工蜂仓库连接测试已更新"),
                  onError: (err) => toast.error(err instanceof Error ? err.message : "连接测试失败"),
                });
              }}
            >
              <CheckCircle2 className="size-3.5" />
              测试
            </ActionButton>
            <ActionButton
              label="刷新同步状态"
              disabled={syncResource.isPending || Boolean(ref.disabled)}
              onClick={() => {
                syncResource.mutate(row.id, {
                  onSuccess: () => toast.success("工蜂仓库同步状态已更新"),
                  onError: (err) => toast.error(err instanceof Error ? err.message : "同步失败"),
                });
              }}
            >
              <RefreshCw className="size-3.5" />
              同步
            </ActionButton>
            {ref.disabled ? (
              <ActionButton
                label="启用仓库"
                disabled={enableResource.isPending}
                onClick={() => {
                  enableResource.mutate(row.id, {
                    onSuccess: () => toast.success("已启用工蜂仓库"),
                    onError: (err) => toast.error(err instanceof Error ? err.message : "启用失败"),
                  });
                }}
              >
                <Power className="size-3.5" />
                启用
              </ActionButton>
            ) : (
              <ActionButton
                label="禁用仓库"
                disabled={disableResource.isPending}
                onClick={() => {
                  disableResource.mutate(row.id, {
                    onSuccess: () => toast.success("已禁用工蜂仓库"),
                    onError: (err) => toast.error(err instanceof Error ? err.message : "禁用失败"),
                  });
                }}
              >
                <Ban className="size-3.5" />
                禁用
              </ActionButton>
            )}
            <ActionButton
              label="删除仓库关联"
              danger
              onClick={() => {
                deleteResource.mutate(row.id, {
                  onSuccess: () => toast.success("已删除工蜂仓库关联"),
                  onError: (err) => toast.error(err instanceof Error ? err.message : "删除失败"),
                });
              }}
            >
              <Trash2 className="size-3.5" />
              删除
            </ActionButton>
          </div>
        </div>
      </div>
      <Dialog open={detailOpen} onOpenChange={setDetailOpen}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>工蜂仓库详情</DialogTitle>
            <DialogDescription>{title}</DialogDescription>
          </DialogHeader>
          <GongfengRepositoryDetail row={row} />
        </DialogContent>
      </Dialog>
    </>
  );
}

function MetaBadge({ children }: { children: ReactNode }) {
  return (
    <span className="rounded border px-1.5 py-0.5 font-mono text-[11px] leading-none text-foreground">
      {children}
    </span>
  );
}

function StatusBadge({ label, value }: { label: string; value: string }) {
  const tone = statusTone(value);
  return (
    <span className={`rounded border px-1.5 py-0.5 text-[11px] leading-none ${tone}`}>
      {label}: {statusLabel(value)}
    </span>
  );
}

function ActionButton({
  children,
  danger,
  disabled,
  label,
  onClick,
}: {
  children: ReactNode;
  danger?: boolean;
  disabled?: boolean;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className={`inline-flex h-7 items-center justify-center gap-1 rounded-md border bg-background px-2 text-[11px] transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-40 ${
        danger ? "text-destructive hover:bg-destructive/10" : "text-foreground"
      }`}
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
    >
      {children}
    </button>
  );
}

function GongfengRepositoryDetail({ row }: { row: GongfengResourceRow }) {
  const ref = row.resource_ref;
  const items = [
    ["Provider", ref.provider],
    ["Project", row.project.title],
    ["Project path", ref.project_path],
    ["URL", ref.url],
    ["Kind", ref.resource_kind],
    ["Branch/ref", ref.branch || ref.ref || "未设置"],
    ["Commit", ref.commit_sha || ref.head_commit || "未采集"],
    ["Connection", statusLabel(ref.connection_status || "pending_verification")],
    ["Sync", statusLabel(ref.sync_status || "pending_verification")],
    ["Test", statusLabel(ref.test_status || "pending_verification")],
    ["Last tested", ref.last_tested_at || "未测试"],
    ["Last synced", ref.last_synced_at || "未同步"],
    ["Training", ref.disabled ? "不可选" : "可选"],
  ];
  return (
    <div className="space-y-2 text-xs" data-testid="settings-gongfeng-repository-detail">
      {items.map(([label, value]) => (
        <div key={label} className="grid grid-cols-[7rem_minmax(0,1fr)] gap-3 border-b py-1.5 last:border-b-0">
          <div className="text-muted-foreground">{label}</div>
          <div className="min-w-0 break-words font-mono">{value}</div>
        </div>
      ))}
    </div>
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

function statusTone(value: string): string {
  switch (value) {
    case "auth_required":
      return "border-amber-300 bg-amber-50 text-amber-800";
    case "failed":
    case "error":
    case "unreachable":
    case "invalid_url":
      return "border-red-200 bg-red-50 text-red-700";
    case "disabled":
      return "bg-muted text-muted-foreground";
    default:
      return "bg-muted/40 text-foreground";
  }
}
