"use client";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  mcpCheckOptions,
  useMcpCheck,
  useMcpMetadata,
} from "@multica/core/workspace/mcp-checks";
import type { WorkspaceMcpServer } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@multica/ui/components/ui/dialog";
import { useT } from "../../i18n";
export function McpMetadataDialog({
  wsId,
  server,
  onClose,
  onReplace,
}: {
  wsId: string;
  server: WorkspaceMcpServer;
  onClose: () => void;
  onReplace: () => void;
}) {
  const { t } = useT("settings");
  const save = useMcpMetadata(wsId);
  const [name, setName] = useState(server.name);
  const [description, setDescription] = useState(server.description ?? "");
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !save.isPending) onClose();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(($) => $.mcp.edit_server)}</DialogTitle>
          <DialogDescription>{t(($) => $.mcp.saved_note)}</DialogDescription>
        </DialogHeader>
        <Label htmlFor="mcp-name">{t(($) => $.mcp.name_label)}</Label>
        <Input
          id="mcp-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <Label htmlFor="mcp-purpose">{t(($) => $.mcp.description_label)}</Label>
        <Input
          id="mcp-purpose"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
        <Button variant="outline" disabled={save.isPending} onClick={onReplace}>
          {t(($) => $.mcp.replace_config)}
        </Button>
        <DialogFooter>
          <Button variant="ghost" disabled={save.isPending} onClick={onClose}>
            {t(($) => $.mcp.cancel)}
          </Button>
          <Button
            disabled={save.isPending || !name.trim()}
            onClick={async () => {
              try {
                await save.mutateAsync({
                  serverId: server.id,
                  name,
                  description,
                });
                onClose();
              } catch (e) {
                toast.error(
                  e instanceof Error
                    ? e.message
                    : t(($) => $.mcp.save_failed_toast),
                );
              }
            }}
          >
            {t(($) => $.mcp.save_metadata)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
export function McpConnectionCheck({
  wsId,
  serverId,
  runtimeId,
}: {
  wsId: string;
  serverId: string;
  runtimeId: string;
}) {
  const { t } = useT("settings");
  const results = useQuery(mcpCheckOptions(wsId, serverId));
  const test = useMcpCheck(wsId, serverId);
  const result = results.data?.find((c) => c.runtimeId === runtimeId);
  const pending =
    !!result &&
    !result.stale &&
    (result.status === "pending" || result.status === "running");
  const status = !result
    ? t(($) => $.mcp.not_tested)
    : result.stale
      ? t(($) => $.mcp.stale)
      : pending
        ? t(($) => $.mcp.testing)
        : result.status === "completed"
          ? t(($) => $.mcp.connected)
          : t(($) => $.mcp.failed);
  const errors: Record<string, string> = {
    timeout: t(($) => $.mcp.error_timeout),
    start_failed: t(($) => $.mcp.error_start),
    authentication_failed: t(($) => $.mcp.error_auth),
    protocol_error: t(($) => $.mcp.error_protocol),
    unsupported_transport: t(($) => $.mcp.error_transport),
    connection_failed: t(($) => $.mcp.error_connection),
  };
  return (
    <div className="flex flex-wrap items-center gap-2 text-caption">
      <Button
        size="sm"
        variant="outline"
        disabled={!runtimeId || pending || test.isPending || results.isPending}
        onClick={async () => {
          try {
            await test.mutateAsync(runtimeId);
          } catch (e) {
            toast.error(
              e instanceof Error ? e.message : t(($) => $.mcp.failed),
            );
          }
        }}
      >
        {t(($) => $.mcp.test_connection)}
      </Button>
      <span role="status">
        {results.isError ? t(($) => $.mcp.result_failed) : status}
        {result?.status === "completed" && !result.stale
          ? ` · ${t(($) => $.mcp.tools_count, { count: result.toolCount })}`
          : ""}
      </span>
      {result && (
        <time
          className="text-muted-foreground"
          dateTime={result.completedAt ?? result.createdAt}
        >
          {new Date(result.completedAt ?? result.createdAt).toLocaleString()}
        </time>
      )}
      {result?.errorCode && !result.stale && (
        <span className="text-destructive">
          {errors[result.errorCode] ?? t(($) => $.mcp.failed)}
        </span>
      )}
    </div>
  );
}
