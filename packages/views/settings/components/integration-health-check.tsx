"use client";

import { useMemo } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { Activity, AlertCircle, CheckCircle2, Clock3, RefreshCw } from "lucide-react";
import { api } from "@multica/core/api";
import { externalCredentialProfilesOptions } from "@multica/core/external-credentials";
import { useWorkspaceId } from "@multica/core/hooks";
import { projectListOptions, projectResourcesOptions } from "@multica/core/projects";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import type { ExternalCredentialProfile, GongfengRepoResourceRef, Project, ProjectResource, PromptEvaluationRuntimeReadiness } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";

type HealthTone = "ok" | "warn" | "bad" | "checking";

type HealthItem = {
  key: string;
  label: string;
  tone: HealthTone;
  value: string;
  detail: string;
  updatedAt?: string | null;
};

type GongfengResourceRow = ProjectResource & {
  project: Project;
  resource_ref: GongfengRepoResourceRef;
};

const EXPECTED_GONGFENG_REPOS = [
  { key: "usercenter", label: "usercenter", aliases: ["usercenter", "user-center"] },
  { key: "gateway", label: "gateway", aliases: ["gateway"] },
  { key: "ida-deployment", label: "ida-deployment", aliases: ["ida-deployment"] },
];

export function IntegrationHealthCheck() {
  const wsId = useWorkspaceId();
  const tapdProfilesQuery = useQuery(externalCredentialProfilesOptions("tapd"));
  const gongfengProfilesQuery = useQuery(externalCredentialProfilesOptions("gongfeng"));
  const runtimeReadinessQuery = useQuery({
    queryKey: ["settings", wsId ?? "", "prompt-evaluation-runtime-readiness"],
    queryFn: () => api.getPromptEvaluationRuntimeReadiness(),
    enabled: Boolean(wsId),
  });
  const runtimeQuery = useQuery({
    ...runtimeListOptions(wsId ?? "", "me"),
    enabled: Boolean(wsId),
  });
  const projectQuery = useQuery({
    ...projectListOptions(wsId ?? ""),
    enabled: Boolean(wsId),
  });
  const projects = projectQuery.data ?? [];
  const resourceQueries = useQueries({
    queries: projects.map((project) => ({
      ...projectResourcesOptions(wsId ?? "", project.id),
      enabled: Boolean(wsId),
    })),
  });

  const gongfengRows = useMemo<GongfengResourceRow[]>(() => {
    return resourceQueries.flatMap((query, index) => {
      const project = projects[index];
      if (!project) return [];
      return (query.data ?? [])
        .filter(isGongfengResource)
        .map((resource) => ({ ...resource, project }));
    });
  }, [projects, resourceQueries]);

  const tapdProfile = preferredCredentialProfile(tapdProfilesQuery.data?.profiles ?? []);
  const gongfengProfile = preferredCredentialProfile(gongfengProfilesQuery.data?.profiles ?? []);
  const onlineRuntimes = (runtimeQuery.data ?? []).filter((runtime) => runtime.status === "online");
  const runtimeReadiness = runtimeReadinessQuery.data;

  const items: HealthItem[] = [
    credentialHealthItem("tapd-readable", "TAPD 可读", tapdProfile, tapdProfilesQuery.isLoading),
    credentialHealthItem("gongfeng-token", "工蜂 token 可用", gongfengProfile, gongfengProfilesQuery.isLoading),
    ...EXPECTED_GONGFENG_REPOS.map((repo) => repoHealthItem(repo, gongfengRows, projectQuery.isLoading || resourceQueries.some((query) => query.isLoading))),
    mcpProfileHealthItem(tapdProfile, gongfengProfile, tapdProfilesQuery.isLoading || gongfengProfilesQuery.isLoading),
    daemonHealthItem(onlineRuntimes.length, runtimeQuery.data?.length ?? 0, runtimeQuery.isLoading),
    runtimeVersionHealthItem(runtimeReadiness, runtimeReadinessQuery.isLoading),
  ];

  const counts = items.reduce(
    (acc, item) => {
      acc[item.tone] += 1;
      return acc;
    },
    { ok: 0, warn: 0, bad: 0, checking: 0 } as Record<HealthTone, number>,
  );

  return (
    <section className="grid gap-3 rounded-md border bg-background p-3" data-testid="settings-integration-health-check">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <Activity className="size-4 text-muted-foreground" />
            <h2 className="text-sm font-semibold">接入健康检查</h2>
          </div>
          <p className="mt-1 text-xs text-muted-foreground">
            汇总 TAPD、工蜂、MCP profile、daemon 和 runtime 状态；失败时直接显示原因和下一步。
          </p>
        </div>
        <div className="flex flex-wrap gap-1 text-xs">
          <Badge variant="secondary">通过 {counts.ok}</Badge>
          <Badge variant="outline">待处理 {counts.warn + counts.checking}</Badge>
          <Badge variant={counts.bad > 0 ? "destructive" : "outline"}>失败 {counts.bad}</Badge>
        </div>
      </div>
      <div className="grid gap-2 md:grid-cols-2" data-testid="settings-integration-health-items">
        {items.map((item) => (
          <HealthRow key={item.key} item={item} />
        ))}
      </div>
    </section>
  );
}

function HealthRow({ item }: { item: HealthItem }) {
  const Icon = item.tone === "ok" ? CheckCircle2 : item.tone === "bad" ? AlertCircle : item.tone === "checking" ? RefreshCw : Clock3;
  return (
    <div className="grid gap-1 rounded-md border bg-muted/10 px-3 py-2 text-xs" data-testid={`settings-health-${item.key}`}>
      <div className="flex min-w-0 items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <Icon className={`size-3.5 shrink-0 ${item.tone === "bad" ? "text-destructive" : item.tone === "ok" ? "text-info" : "text-muted-foreground"} ${item.tone === "checking" ? "animate-spin" : ""}`} />
          <span className="truncate font-medium">{item.label}</span>
        </div>
        <Badge variant={item.tone === "bad" ? "destructive" : item.tone === "ok" ? "secondary" : "outline"} className="shrink-0">
          {item.value}
        </Badge>
      </div>
      <div className="break-words text-muted-foreground">{item.detail}</div>
      {item.updatedAt && <div className="text-[11px] text-muted-foreground">更新时间：{item.updatedAt}</div>}
    </div>
  );
}

function preferredCredentialProfile(profiles: ExternalCredentialProfile[]): ExternalCredentialProfile | undefined {
  return profiles.find((profile) => profile.secret_binding.configured && profile.status === "verified") ??
    profiles.find((profile) => profile.secret_binding.configured && profile.status !== "failed" && profile.status !== "disabled") ??
    profiles[0];
}

function credentialHealthItem(key: string, label: string, profile: ExternalCredentialProfile | undefined, loading: boolean): HealthItem {
  if (loading) {
    return { key, label, tone: "checking", value: "检查中", detail: "正在读取账号级凭据 profile。" };
  }
  if (!profile || !profile.secret_binding.configured) {
    return { key, label, tone: "bad", value: "未设置", detail: "请在设置中保存账号级凭据，可使用服务端环境变量或直接粘贴 token。" };
  }
  if (profile.status === "failed" || profile.status === "disabled") {
    return {
      key,
      label,
      tone: "bad",
      value: profile.status === "disabled" ? "已停用" : "失败",
      detail: profile.last_error || "凭据不可用，请重新保存并校验。",
      updatedAt: profile.last_verified_at,
    };
  }
  if (profile.status === "verified") {
    return {
      key,
      label,
      tone: "ok",
      value: "通过",
      detail: `账号级 ${profile.provider} profile 已配置，secret ${profile.secret_binding.hint || "已脱敏"}。`,
      updatedAt: profile.last_verified_at,
    };
  }
  return {
    key,
    label,
    tone: "warn",
    value: "待校验",
    detail: profile.last_error || "凭据绑定已保存；实时 TAPD/工蜂 API 校验尚未接入时会显示待校验。",
    updatedAt: profile.last_verified_at,
  };
}

function repoHealthItem(
  repo: (typeof EXPECTED_GONGFENG_REPOS)[number],
  rows: GongfengResourceRow[],
  loading: boolean,
): HealthItem {
  if (loading) {
    return { key: `repo-${repo.key}`, label: `${repo.label} 可读`, tone: "checking", value: "检查中", detail: "正在读取项目工蜂仓库资源。" };
  }
  const row = rows.find((candidate) => matchesExpectedRepo(candidate, repo.aliases));
  if (!row) {
    return { key: `repo-${repo.key}`, label: `${repo.label} 可读`, tone: "bad", value: "缺失", detail: "未在项目资源中找到该工蜂仓库。" };
  }
  const ref = row.resource_ref;
  const connection = ref.connection_status || "pending_verification";
  const sync = ref.sync_status || "pending_verification";
  const test = ref.test_status || "pending_verification";
  const bad = [connection, sync, test].some((status) => ["failed", "error", "unreachable", "invalid_url"].includes(status));
  const needsCredential = [connection, sync, test].includes("auth_required");
  const pending = [connection, sync, test].includes("pending_verification");
  const tone: HealthTone = bad ? "bad" : needsCredential || pending ? "warn" : "ok";
  return {
    key: `repo-${repo.key}`,
    label: `${repo.label} 可读`,
    tone,
    value: bad ? "失败" : needsCredential ? "需凭据" : pending ? "待验证" : "通过",
    detail: `${row.project.title} · ${ref.project_path || ref.url} · 连接 ${statusLabel(connection)} / 同步 ${statusLabel(sync)} / 测试 ${statusLabel(test)}`,
    updatedAt: ref.last_tested_at || ref.last_synced_at || row.created_at,
  };
}

function mcpProfileHealthItem(
  tapdProfile: ExternalCredentialProfile | undefined,
  gongfengProfile: ExternalCredentialProfile | undefined,
  loading: boolean,
): HealthItem {
  if (loading) {
    return { key: "mcp-profile", label: "MCP profile 可用", tone: "checking", value: "检查中", detail: "正在读取账号级凭据和 SOP MCP 配置。" };
  }
  const tapdConfigured = Boolean(tapdProfile?.secret_binding.configured && tapdProfile.status !== "failed" && tapdProfile.status !== "disabled");
  const gongfengConfigured = Boolean(gongfengProfile?.secret_binding.configured && gongfengProfile.status !== "failed" && gongfengProfile.status !== "disabled");
  if (tapdConfigured && gongfengConfigured) {
    return {
      key: "mcp-profile",
      label: "MCP profile 可用",
      tone: "ok",
      value: "通过",
      detail: "SOP 小队默认声明 mcp-server-tapd 与 gongfeng；账号级凭据可在任务运行时注入。",
    };
  }
  return {
    key: "mcp-profile",
    label: "MCP profile 可用",
    tone: "bad",
    value: "缺凭据",
    detail: "需要同时配置 TAPD 和工蜂账号级凭据，否则 PM/01-05 不能稳定读取需求和仓库上下文。",
  };
}

function daemonHealthItem(onlineCount: number, totalCount: number, loading: boolean): HealthItem {
  if (loading) {
    return { key: "daemon-online", label: "daemon 在线", tone: "checking", value: "检查中", detail: "正在读取当前账号可用 runtime。" };
  }
  if (onlineCount > 0) {
    return {
      key: "daemon-online",
      label: "daemon 在线",
      tone: "ok",
      value: `${onlineCount}/${totalCount}`,
      detail: "至少一个 runtime 在线，可领取训练评估或小队任务。",
    };
  }
  return {
    key: "daemon-online",
    label: "daemon 在线",
    tone: "bad",
    value: `0/${totalCount}`,
    detail: "没有在线 runtime；请启动 multica daemon 或检查当前账号是否有可见 runtime。",
  };
}

function runtimeVersionHealthItem(readiness: PromptEvaluationRuntimeReadiness | undefined, loading: boolean): HealthItem {
  if (loading) {
    return { key: "runtime-version", label: "runtime 版本满足", tone: "checking", value: "检查中", detail: "正在检查训练评估 runtime readiness。" };
  }
  if (!readiness) {
    return { key: "runtime-version", label: "runtime 版本满足", tone: "bad", value: "缺失", detail: "未返回 runtime readiness；请刷新或检查后端日志。" };
  }
  return {
    key: "runtime-version",
    label: "runtime 版本满足",
    tone: readiness.status === "就绪" ? "ok" : "warn",
    value: readiness.status,
    detail: `${readiness.detail} 目标模型：${readiness.model || "未记录"}。`,
    updatedAt: readiness.checked_at,
  };
}

function isGongfengResource(resource: ProjectResource): resource is ProjectResource & { resource_ref: GongfengRepoResourceRef } {
  return resource.resource_type === "gongfeng_repo";
}

function matchesExpectedRepo(row: GongfengResourceRow, aliases: string[]): boolean {
  const haystack = [
    row.label,
    row.project.title,
    row.resource_ref.title,
    row.resource_ref.project_path,
    row.resource_ref.url,
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
  return aliases.some((alias) => haystack.includes(alias));
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
    default:
      return value.replace(/_/g, " ");
  }
}
