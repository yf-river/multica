"use client";

import { useMemo, useState, type ReactNode } from "react";
import {
  Ban,
  CheckCircle2,
  FolderGit,
  Info,
  Power,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
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
    <section className="space-y-2" data-testid="settings-gongfeng-repository-inventory">
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
          <div className="flex items-center gap-1 rounded-md border bg-muted/20 p-0.5">
            <IconButton label="查看仓库详情" onClick={() => setDetailOpen(true)}>
              <Info className="size-3.5" />
            </IconButton>
            <IconButton
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
            </IconButton>
            <IconButton
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
            </IconButton>
            {ref.disabled ? (
              <IconButton
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
              </IconButton>
            ) : (
              <IconButton
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
              </IconButton>
            )}
            <IconButton
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
            </IconButton>
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
  return (
    <span className="rounded border bg-muted/40 px-1.5 py-0.5 text-[11px] leading-none">
      {label}: {statusLabel(value)}
    </span>
  );
}

function IconButton({
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
      className={`flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent disabled:opacity-40 ${
        danger ? "hover:text-destructive" : "hover:text-foreground"
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
      return "需凭据";
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
