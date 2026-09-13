// 本文件是「MCP 服务器库」工作区设置标签页（需求单 GH #6062）：管理工作区级
// MCP 服务器的增删改——即把「地址/命令/头信息/环境变量」等连接配置保存下来复用。

"use client";

import { useMemo, useState } from "react";
import { Loader2, Pencil, Plus, Server, Trash2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
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
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useCurrentMember } from "@multica/core/permissions";
import { workspaceMcpServersOptions } from "@multica/core/workspace/queries";
import {
  useCreateWorkspaceMcpServer,
  useDeleteWorkspaceMcpServer,
  useUpdateWorkspaceMcpServer,
} from "@multica/core/workspace/mutations";
import type { WorkspaceMcpServer } from "@multica/core/types";
import { McpServerDialog } from "../../agents/components/tabs/mcp-server-dialog";
import type { ManagedMcpServer } from "../../agents/components/tabs/mcp-config-model";
import { useT } from "../../i18n";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";

/**
 * 工作区 MCP 服务器库（需求单 GH #6062）。
 *
 * 这个页面的两个关键设定，先说明白：
 *
 *  - 在这里添加的服务器不会隐式交给任何 agent。它只是库里的一个条目，作用类似
 *    「工作区技能」：由 agent 的拥有者在 agent 自己的 MCP 页里选定使用，并在那里
 *    获得每个 agent 一个开关。本页的操作不会暗地里触达任何 agent。
 *  - 保存的配置是「只写」的。接口只会返回名称和传输方式，绝不返回 url / 命令 /
 *    头信息 / 环境变量，所以编辑时没有「当前值」可以回填——改一个服务器就意味着
 *    要重新填一遍它的配置。界面如实说明了这一点，而不是把空表单伪装成已保存的状态。
 */
export function McpTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const currentMember = useCurrentMember(wsId);
  const canManage =
    currentMember.role === "owner" || currentMember.role === "admin";

  const serversQuery = useQuery(workspaceMcpServersOptions(wsId));
  const createServer = useCreateWorkspaceMcpServer(wsId);
  const updateServer = useUpdateWorkspaceMcpServer(wsId);
  const deleteServer = useDeleteWorkspaceMcpServer(wsId);

  const servers = serversQuery.data ?? [];
  const existingNames = useMemo(
    () => new Set(servers.map((server) => server.name)),
    [servers],
  );

  const [editorOpen, setEditorOpen] = useState(false);
  const [editingServer, setEditingServer] = useState<WorkspaceMcpServer | null>(
    null,
  );
  const [deletingServer, setDeletingServer] = useState<WorkspaceMcpServer | null>(
    null,
  );

  // 编辑弹窗与 agent 的 MCP 页共用（那边会传入已保存的条目来回填表单）。这里没有
  // 可回填的内容——编辑总是从空表单开始、保存时整体替换原条目。传输方式仍取自
  // 服务端返回的安全摘要，让表单一开始就停在正确的传输类型上。
  const dialogServer: ManagedMcpServer | null = editingServer
    ? {
        name: editingServer.name,
        config: {},
        container: "mcpServers",
        transport: editingServer.transport,
        // 服务器库没有「每 agent 开关」，这个字段只是为了满足弹窗的数据结构。
        enabled: true,
      }
    : null;

  const handleSaveServer = async (
    name: string,
    config: Record<string, unknown>,
  ) => {
    try {
      if (editingServer) {
        // 这里改名是安全的：各 agent 的引用挂的是服务器 id，改名后仍指向同一个服务器。
        await updateServer.mutateAsync({ serverId: editingServer.id, name, config });
      } else {
        await createServer.mutateAsync({ name, config });
      }
      toast.success(
        editingServer
          ? t(($) => $.mcp.updated_toast)
          : t(($) => $.mcp.added_toast),
      );
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.mcp.save_failed_toast),
      );
      throw error;
    }
  };

  const handleDelete = async () => {
    if (!deletingServer) return;
    try {
      await deleteServer.mutateAsync(deletingServer.id);
      toast.success(t(($) => $.mcp.removed_toast));
      setDeletingServer(null);
    } catch (error) {
      toast.error(
        error instanceof Error && error.message
          ? error.message
          : t(($) => $.mcp.remove_failed_toast),
      );
    }
  };

  return (
    <SettingsTab
      title={t(($) => $.mcp.title)}
      description={t(($) => $.mcp.description)}
    >
      <SettingsSection
        title={t(($) => $.mcp.servers_title)}
        description={t(($) => $.mcp.write_only_note)}
        action={
          canManage ? (
            <Button
              size="sm"
              onClick={() => {
                setEditingServer(null);
                setEditorOpen(true);
              }}
            >
              <Plus className="h-4 w-4" />
              {t(($) => $.mcp.add_server)}
            </Button>
          ) : null
        }
      >
        <SettingsCard>
          {serversQuery.isLoading ? (
            <div className="flex items-center justify-center py-8 text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
            </div>
          ) : servers.length === 0 ? (
            <div className="px-4 py-8 text-center">
              <Server className="mx-auto h-5 w-5 text-muted-foreground" />
              <p className="mt-3 text-body font-medium">
                {t(($) => $.mcp.empty_title)}
              </p>
              <p className="mx-auto mt-1 max-w-md text-caption leading-5 text-muted-foreground">
                {t(($) => $.mcp.empty_description)}
              </p>
            </div>
          ) : (
            <ul className="divide-y divide-surface-border">
              {servers.map((server) => (
                <McpServerRow
                  key={server.name}
                  server={server}
                  canManage={canManage}
                  onEdit={() => {
                    setEditingServer(server);
                    setEditorOpen(true);
                  }}
                  onDelete={() => setDeletingServer(server)}
                />
              ))}
            </ul>
          )}
        </SettingsCard>
        {!canManage && !currentMember.isLoading ? (
          <p className="px-0.5 text-caption text-muted-foreground">
            {t(($) => $.mcp.admin_only_note)}
          </p>
        ) : null}
      </SettingsSection>

      <McpServerDialog
        open={editorOpen}
        server={dialogServer}
        existingNames={existingNames}
        onOpenChange={setEditorOpen}
        onSave={handleSaveServer}
      />

      <AlertDialog
        open={deletingServer !== null}
        onOpenChange={(open) => {
          if (!open) setDeletingServer(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.mcp.delete_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.mcp.delete_description, { name: deletingServer?.name ?? "" })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteServer.isPending}>
              {t(($) => $.mcp.cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault();
                void handleDelete();
              }}
              disabled={deleteServer.isPending}
            >
              {deleteServer.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : null}
              {t(($) => $.mcp.delete_confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

/**
 * 单个 MCP 服务器在列表里的一行：左边显示名称（可带「已禁用」角标）与传输方式，
 * 有管理权限时右侧提供编辑 / 删除按钮。
 */
function McpServerRow({
  server,
  canManage,
  onEdit,
  onDelete,
}: {
  server: WorkspaceMcpServer;
  canManage: boolean;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const { t } = useT("settings");
  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span className="truncate text-body font-medium">{server.name}</span>
          {server.enabled === false ? (
            <Badge variant="secondary">{t(($) => $.mcp.disabled_badge)}</Badge>
          ) : null}
        </div>
        <p className="mt-0.5 text-caption text-muted-foreground">
          {transportLabel(server.transport)}
        </p>
      </div>
      {canManage ? (
        <div className="flex shrink-0 items-center gap-1">
          <Button
            variant="ghost"
            size="icon"
            onClick={onEdit}
            aria-label={t(($) => $.mcp.edit_server)}
          >
            <Pencil className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            onClick={onDelete}
            aria-label={t(($) => $.mcp.remove_server)}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      ) : null}
    </li>
  );
}

/**
 * `transport` 是服务端下发的字符串，这里做一层展示归一：认识的类型给一个更
 * 标准的写法（stdio 原样、http→HTTP、sse→SSE）；遇到更新的后端发来不认识的值时，
 * 原样渲染而不是让它消失，避免新类型在旧前端上无迹可寻。
 */
function transportLabel(transport: string): string {
  switch (transport) {
    case "stdio":
      return "stdio";
    case "http":
      return "HTTP";
    case "sse":
      return "SSE";
    default:
      return transport || "unknown";
  }
}
