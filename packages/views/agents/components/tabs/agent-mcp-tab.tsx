"use client";

// 本文件是 Agent 详情页里“仅创建者可看”的 MCP 标签页（MUL-3870）：让 Agent 拥有者
// 挑选“自己的”哪些有效 Composio 连接可作为 MCP 服务挂给这个 Agent，选中结果写入
// agent.composio_toolkit_allowlist。

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Loader2, Lock, Plug } from "lucide-react";
import { toast } from "sonner";
import type { Agent, ComposioToolkit } from "@multica/core/types";
import { useUpdateAgentAllowlist } from "@multica/core/agents";
import { useFeatureEnabled } from "@multica/core/config";
import {
  composioConnectionsOptions,
  composioToolkitsOptions,
} from "@multica/core/composio";
import { COMPOSIO_MCP_APPS_FLAG } from "@multica/core/feature-flags";
import { useWorkspacePaths } from "@multica/core/paths";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { ComposioToolkitLogo } from "../../../common/composio-toolkit-logo";
import { AppLink } from "../../../navigation";
import { useT } from "../../../i18n";

/**
 * Agent 详情页的 MCP 标签页（仅创建者，MUL-3870）。Agent 拥有者可挑选“自己的”
 * 哪些有效 Composio 连接允许挂成 MCP 服务——选择写入
 * `agent.composio_toolkit_allowlist`。派发时，只要任务通过 Agent 的调用权限，
 * overlay 就会挂载（MUL-3963），且始终使用 Agent“拥有者”的 Composio 连接——不再
 * 要求任务发起者就是拥有者。正因如此，把 Agent 分享出去（public_to）就会触发下方
 * 的警告横幅：能调用它的所有人，都能借它驱动这些应用。
 *
 * 可见性由父层把关（不是拥有者就不渲染本标签入口），所以本组件假定自己是拥有者。
 * 若服务端把 allowlist 脱敏了，仍防御性地渲染“已配置但隐藏”的状态；勾选状态直接
 * 读 agent prop，`useUpdateAgentAllowlist` 里的乐观缓存写入因此能让每个勾选框
 * 即时翻转。
 */
export function AgentMcpTab({ agent }: { agent: Agent }) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const composioEnabled = useFeatureEnabled(COMPOSIO_MCP_APPS_FLAG, false);
  const updateAllowlist = useUpdateAgentAllowlist(agent.id);

  const connectionsQuery = useQuery({
    ...composioConnectionsOptions(),
    enabled: composioEnabled,
  });
  const toolkitsQuery = useQuery({
    ...composioToolkitsOptions(),
    enabled: composioEnabled,
  });

  // toolkit 元信息（名字 / logo）按 slug 建索引，让每条连接显示友好名而不是裸 slug。
  // 目录只是尽力而为的增强——缺条目就回退显示 slug。
  const toolkitBySlug = useMemo(() => {
    const m = new Map<string, ComposioToolkit>();
    for (const tk of toolkitsQuery.data ?? []) m.set(tk.slug, tk);
    return m;
  }, [toolkitsQuery.data]);

  // 只有“有效（active）”的连接可选——过期/已撤销的连接撑不起 MCP 挂载，
  // 给它个勾选框只会是死开关。按 slug 去重（用户理论上可能对同一 toolkit 持两行）。
  const activeSlugs = useMemo(() => {
    const seen = new Set<string>();
    const out: string[] = [];
    for (const c of connectionsQuery.data ?? []) {
      if (c.status !== "active") continue;
      if (seen.has(c.toolkit_slug)) continue;
      seen.add(c.toolkit_slug);
      out.push(c.toolkit_slug);
    }
    return out;
  }, [connectionsQuery.data]);

  const allowlist = useMemo(
    () => agent.composio_toolkit_allowlist ?? [],
    [agent.composio_toolkit_allowlist],
  );

  const settingsHref = `${paths.settings()}?tab=integrations`;

  // Composio 访问警告（MUL-3963）。Agent 一旦被分享出去，任何能调用它的人都能
  // 以拥有者的名义驱动这里启用的 Composio 应用——所以只要 Agent 不是 private、
  // 且有可启用（或已启用）的应用，就提示一句。对“整个工作区公开”用更强的文案，
  // 因为那等于把应用开放给每一位成员。
  const isPrivate = agent.permission_mode === "private";
  const isWorkspacePublic =
    agent.permission_mode === "public_to" &&
    (agent.invocation_targets ?? []).some(
      (target) => target.target_type === "workspace",
    );
  const showSharedWarning =
    !isPrivate && (allowlist.length > 0 || activeSlugs.length > 0);

  if (!composioEnabled) {
    return null;
  }

  const handleToggle = (slug: string, checked: boolean) => {
    const set = new Set(allowlist);
    if (checked) set.add(slug);
    else set.delete(slug);
    const next = Array.from(set);
    updateAllowlist.mutate(next, {
      onError: () => toast.error(t(($) => $.tab_body.composio_mcp.save_failed_toast)),
    });
  };

  // 防御性：本标签由拥有者把关，脱敏的 allowlist 本不该走到这里。万一走到了
  //（过期缓存、未来接口扩展），就展示和 MCP 配置标签一致的“已配置但隐藏”提示，
  // 而不是一个点了保存就会被覆盖的空编辑器。
  if (agent.composio_toolkit_allowlist_redacted === true) {
    return (
      <div className="space-y-3">
        <p className="flex items-center gap-2 text-body font-medium">
          <Lock className="h-3.5 w-3.5 text-muted-foreground" />
          {t(($) => $.tab_body.composio_mcp.redacted_title)}
        </p>
        <p className="text-caption text-muted-foreground">
          {t(($) => $.tab_body.composio_mcp.redacted_hint)}
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <p className="text-caption text-muted-foreground">
        {t(($) => $.tab_body.composio_mcp.subtitle)}
      </p>

      {showSharedWarning && (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-caption text-amber-700 dark:text-amber-400"
        >
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span>
            {isWorkspacePublic
              ? t(($) => $.tab_body.composio_mcp.workspace_warning)
              : t(($) => $.tab_body.composio_mcp.shared_warning)}
          </span>
        </div>
      )}

      {connectionsQuery.isLoading ? (
        <p className="text-body text-muted-foreground">
          {t(($) => $.tab_body.composio_mcp.loading)}
        </p>
      ) : connectionsQuery.isError ? (
        <p className="text-body text-destructive">
          {t(($) => $.tab_body.composio_mcp.load_failed)}
        </p>
      ) : activeSlugs.length === 0 ? (
        <div className="space-y-2 rounded-lg border border-dashed p-6 text-center">
          <p className="text-body font-medium">
            {t(($) => $.tab_body.composio_mcp.empty_title)}
          </p>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.composio_mcp.empty_hint)}
          </p>
          <AppLink
            href={settingsHref}
            className="inline-flex items-center gap-1.5 text-caption font-medium text-primary hover:underline"
          >
            <Plug className="h-3 w-3" />
            {t(($) => $.tab_body.composio_mcp.empty_link_to_settings)}
          </AppLink>
        </div>
      ) : (
        <ul className="divide-y rounded-lg border">
          {activeSlugs.map((slug) => {
            const tk = toolkitBySlug.get(slug);
            const name = tk?.name || slug;
            const checked = allowlist.includes(slug);
            return (
              <li key={slug} className="flex items-center gap-3 p-3">
                <ComposioToolkitLogo slug={slug} name={name} fallbackLogo={tk?.logo} />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-body font-medium">{name}</p>
                  <p className="truncate text-micro uppercase tracking-wide text-emerald-600">
                    {t(($) => $.tab_body.composio_mcp.connected)}
                  </p>
                </div>
                <Checkbox
                  checked={checked}
                  disabled={updateAllowlist.isPending}
                  onCheckedChange={(value) => handleToggle(slug, value === true)}
                  aria-label={t(($) => $.tab_body.composio_mcp.toggle_aria, {
                    toolkit: name,
                  })}
                />
              </li>
            );
          })}
        </ul>
      )}

      {updateAllowlist.isPending && (
        <p className="flex items-center gap-1.5 text-caption text-muted-foreground">
          <Loader2 className="h-3 w-3 animate-spin" />
          {t(($) => $.tab_body.composio_mcp.saving)}
        </p>
      )}
    </div>
  );
}
