"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, Loader2 } from "lucide-react";
import { pluginMCPToolsOptions, useApprovePluginMCPTools } from "@multica/core/plugins";
import type { PluginHook, PluginMCPTool } from "@multica/core/types";
import { Alert, AlertDescription, AlertTitle } from "@multica/ui/components/ui/alert";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { toast } from "sonner";
import { useT } from "../i18n";

/**
 * plugins 模块：为 `mcp` 传输类型的 hook 审批（勾选）工具集。
 *
 * `http` hook 不需要这种东西：它在 manifest 里声明一个端点，管理员在同意
 * 界面已经读过那个 manifest，端点就是全部 surface。而 MCP 服务器会在运行时
 * 自己决定工具列表、想改随时能改，所以「安装了插件」不能等于授权——否则
 * 今天批准的东西，等于同时批准了服务器下周提供的任何东西。
 *
 * 因此：发现（工具列表）是只读的、不采纳任何东西；管理员要在这个面板里
 * 钉住一个具体的集合。服务端把每个工具连同其 schema 摘要一起存储，daemon
 * 会拒绝启动「已批准工具丢失或 schema 漂移」的连接。
 */

/** 哪些 hook 需要审批面板：mcp 传输 + 声明了 `agent` 触发器。 */
export function mcpHooks(hooks: readonly PluginHook[]): PluginHook[] {
  return hooks.filter((hook) => hook.transport === "mcp" && (hook.triggers ?? []).includes("agent"));
}

/**
 * 保存按钮将要发送的内容。
 *
 * 完整的批准集合，绝不是增量——取消勾选一个工具再保存就是撤销它，所以
 * 复选框状态本身就是请求。这也是为什么漂移的工具初始为不勾选：它的 schema
 * 不是当初批准的那份，若保持勾选，一次点击就会重新批准一个没人看过的形状。
 */
export function initialSelection(tools: readonly PluginMCPTool[]): string[] {
  return tools.filter((tool) => tool.approved && !tool.drifted).map((tool) => tool.name);
}

/** MCP 工具的审批面板：勾选允许的 MCP 工具并保存给服务端。 */
export function PluginMCPApproval({
  wsId,
  installationId,
  hook,
  canManage,
}: {
  wsId: string;
  installationId: string;
  hook: PluginHook;
  canManage: boolean;
}) {
  const { t } = useT("settings");
  const { data, isLoading, isError, error } = useQuery(pluginMCPToolsOptions(wsId, installationId, hook.key));
  const approve = useApprovePluginMCPTools(wsId, installationId, hook.key);

  const tools = useMemo(() => data ?? [], [data]);
  // null 表示「还没编辑过」，此时复选框跟着服务端走，直到管理员动其中一个。
  // 这样后台重新拉取不会悄悄撤销正在进行的编辑；保存后清回 null，服务端
  // 再次成为权威。
  const [selected, setSelected] = useState<string[] | null>(null);
  const current = selected ?? initialSelection(tools);

  const toggle = (name: string, checked: boolean) => {
    setSelected(checked ? [...current, name] : current.filter((entry) => entry !== name));
  };

  const save = () => {
    approve
      .mutateAsync(current)
      .then(() => {
        setSelected(null);
        toast.success(t(($) => $.plugins.mcp.approved));
      })
      .catch((cause: unknown) => {
        toast.error(cause instanceof Error ? cause.message : t(($) => $.plugins.action_failed));
      });
  };

  return (
    <div className="space-y-2 rounded-md border border-surface-border px-3 py-3">
      <div className="flex items-center gap-2">
        <span className="text-caption font-medium">{hook.name}</span>
        <Badge variant="outline">{t(($) => $.plugins.mcp.badge)}</Badge>
      </div>
      <p className="text-caption text-muted-foreground">{t(($) => $.plugins.mcp.description)}</p>

      {isLoading ? (
        <Skeleton className="h-16 w-full" aria-label={t(($) => $.plugins.mcp.loading)} />
      ) : isError ? (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>{t(($) => $.plugins.mcp.unreachable)}</AlertTitle>
          {/* 端点不可达正是需要提示的部分：那是插件作者自己的服务器，管理员
              只能告诉作者去修。 */}
          <AlertDescription>{error instanceof Error ? error.message : ""}</AlertDescription>
        </Alert>
      ) : tools.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.plugins.mcp.empty)}</p>
      ) : (
        <>
          <ul className="space-y-1.5">
            {tools.map((tool) => (
              <li key={tool.name} className="flex items-start gap-2">
                <Checkbox
                  id={`${installationId}-${hook.key}-${tool.name}`}
                  className="mt-0.5"
                  disabled={!canManage || approve.isPending}
                  checked={current.includes(tool.name)}
                  onCheckedChange={(checked) => toggle(tool.name, checked === true)}
                />
                <label
                  htmlFor={`${installationId}-${hook.key}-${tool.name}`}
                  className="min-w-0 text-caption"
                >
                  <span className="font-mono">{tool.name}</span>
                  {tool.drifted ? (
                    <Badge variant="destructive" className="ml-2">
                      {t(($) => $.plugins.mcp.drifted)}
                    </Badge>
                  ) : null}
                  {tool.description ? (
                    <span className="block text-muted-foreground">{tool.description}</span>
                  ) : null}
                </label>
              </li>
            ))}
          </ul>
          <div className="flex justify-end">
            <Button size="sm" disabled={!canManage || approve.isPending} onClick={save}>
              {approve.isPending ? <Loader2 className="animate-spin" /> : null}
              {t(($) => $.plugins.mcp.save)}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}
