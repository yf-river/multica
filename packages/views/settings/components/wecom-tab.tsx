// 本文件包含两块：① 设置页「集成」里的企业微信（WeCom）频道面板（WecomTab），
// 列出已接入的智能机器人并支持管理员断开；② 供 agent 详情页复用的「接入企业微信
// 机器人」按钮及其已连接态展示（WecomAgentBindButton 及两个配套小组件）。

"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronRight, Trash2 } from "lucide-react";
import { WecomMark } from "./wecom-mark";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
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
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useActorName } from "@multica/core/workspace/hooks";
import { wecomInstallationsOptions, wecomKeys } from "@multica/core/wecom";
import { errorCode } from "@multica/core/api";
import { api } from "@multica/core/api";
import type { WecomInstallation } from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";

/**
 * 企业微信频道设置面板：列出已接入的智能机器人。列表对所有成员可见；断开仅限
 * 管理员（后端强制，前端对非管理员隐藏按钮以对齐）。
 *
 * 新装机器人的入口不在本页、而在 agent 详情页：接入是按 agent 走的（每个 Multica
 * agent 恰好一个机器人——(workspace_id, agent_id, channel_type) 在
 * channel_installation 表里构成唯一约束），若在这里让用户挑 agent，等于把那边
 * 的选人器重复造一遍。
 */
export function WecomTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManage =
    currentMember?.role === "owner" || currentMember?.role === "admin";

  const { data, isLoading } = useQuery({
    ...wecomInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const installations = data?.installations ?? [];
  const configured = data?.configured === true;
  const installSupported = data?.install_supported === true;

  const [disconnectTarget, setDisconnectTarget] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteWecomInstallation(wsId, disconnectTarget);
      await qc.invalidateQueries({ queryKey: wecomKeys.installations(wsId) });
      toast.success(t(($) => $.wecom.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.wecom.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div className="space-y-8">
      {!configured ? (
        <Card>
          <CardContent className="space-y-2">
            <p className="text-body font-medium">{t(($) => $.wecom.not_enabled_title)}</p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.wecom.not_enabled_description_prefix)}{" "}
              <code className="rounded bg-muted px-1 py-0.5 text-micro">
                MULTICA_WECOM_SECRET_KEY
              </code>{" "}
              {t(($) => $.wecom.not_enabled_description_suffix)}{" "}
              {t(($) => $.wecom.not_enabled_self_host_hint)}
            </p>
          </CardContent>
        </Card>
      ) : !installSupported && installations.length === 0 ? (
        <Card>
          <CardContent className="space-y-2">
            <p className="text-body font-medium">{t(($) => $.wecom.preview_title)}</p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.wecom.preview_description)}
            </p>
          </CardContent>
        </Card>
      ) : (
        <section className="space-y-3">
          <h2 className="text-body font-semibold">{t(($) => $.wecom.connected_bots)}</h2>
          {isLoading ? (
            <Card>
              <CardContent>
                <p className="text-body text-muted-foreground">{t(($) => $.wecom.loading)}</p>
              </CardContent>
            </Card>
          ) : installations.length === 0 ? (
            <Card>
              <CardContent className="space-y-2">
                <p className="text-body font-medium">{t(($) => $.wecom.empty_title)}</p>
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.wecom.empty_description_prefix)}{" "}
                  <strong>{t(($) => $.wecom.empty_description_cta)}</strong>{" "}
                  {t(($) => $.wecom.empty_description_suffix)}
                </p>
              </CardContent>
            </Card>
          ) : (
            <Card>
              <CardContent className="divide-y">
                {installations.map((inst) => (
                  <InstallationRow
                    key={inst.id}
                    installation={inst}
                    canManage={canManage}
                    onDisconnect={() => setDisconnectTarget(inst.id)}
                  />
                ))}
              </CardContent>
            </Card>
          )}
        </section>
      )}

      <AlertDialog
        open={!!disconnectTarget}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setDisconnectTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.wecom.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.wecom.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.wecom.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.wecom.disconnecting)
                : t(($) => $.wecom.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/** 单个已接入机器人的行：agent 头像 + 名称（可带「已撤销」角标）与机器人 bot_id；
 *  仅对「活跃且当前有管理权」的行显示断开按钮。 */
function InstallationRow({
  installation,
  canManage,
  onDisconnect,
}: {
  installation: WecomInstallation;
  canManage: boolean;
  onDisconnect: () => void;
}) {
  const { t } = useT("settings");
  const { getAgentName } = useActorName();
  const isActive = installation.status === "active";
  const agentName = getAgentName(installation.agent_id);
  return (
    <div className="flex items-start justify-between gap-4 py-3 first:pt-0 last:pb-0">
      <div className="flex items-start gap-3">
        <ActorAvatar
          actorType="agent"
          actorId={installation.agent_id}
          size="lg"
          enableHoverCard
          profileLink
        />
        <div className="space-y-1">
          <p className="text-body font-medium">
            {agentName}
            {!isActive && (
              <span className="ml-2 rounded bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">
                {t(($) => $.wecom.revoked_badge)}
              </span>
            )}
          </p>
          <p className="text-micro text-muted-foreground">
            {t(($) => $.wecom.bot_id_label, { botId: installation.bot_id })}
          </p>
        </div>
      </div>
      {canManage && isActive && (
        <Button variant="outline" size="sm" onClick={onDisconnect}>
          <Trash2 className="h-3 w-3" />
          {t(($) => $.wecom.disconnect)}
        </Button>
      )}
    </div>
  );
}

/**
 * agent 详情页上的「接入企业微信机器人」按钮（供 agent 页复用）。
 * 企业微信智能机器人走「自备机器人」模型：点开弹窗，管理员粘入从企微管理后台拿到的
 * 机器人稳定标识（bot_id）与长连接密钥。可见性：
 *   1. 非 owner/admin 什么也不显示（后端负责门控安装/撤销）；
 *   2. 该 agent 已有活跃安装时，显示已连接角标；
 *   3. 否则只要安装可用就显示连接按钮。
 */
export function WecomAgentBindButton({
  agentId,
  agentName,
  className,
  onShowConnectedDetails,
}: {
  agentId: string;
  agentName?: string;
  className?: string;
  onShowConnectedDetails?: () => void;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [botId, setBotId] = useState("");
  const [secret, setSecret] = useState("");
  const [botName, setBotName] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const { data: listing } = useQuery({
    ...wecomInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const installSupported = listing?.install_supported === true;

  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: !!wsId,
  });
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManage =
    currentMember?.role === "owner" || currentMember?.role === "admin";

  if (!canManage) return null;

  const existing = listing?.installations.find(
    (inst) => inst.agent_id === agentId && inst.status === "active",
  );
  if (existing) {
    return onShowConnectedDetails ? (
      <WecomAgentBotStatusRow
        onClick={onShowConnectedDetails}
        className={className}
      />
    ) : (
      <WecomAgentBotConnectedBadge installation={existing} className={className} />
    );
  }

  if (!installSupported) return null;

  function closeDialog() {
    if (submitting) return;
    setDialogOpen(false);
    setBotId("");
    setSecret("");
    setBotName("");
  }

  async function handleSubmit() {
    const bot_id = botId.trim();
    const secretTrimmed = secret.trim();
    if (submitting || !agentId || !bot_id || !secretTrimmed) return;
    setSubmitting(true);
    try {
      await api.registerWecomBYO(wsId, agentId, {
        bot_id,
        secret: secretTrimmed,
        bot_name: botName.trim() || undefined,
      });
      await qc.invalidateQueries({ queryKey: wecomKeys.installations(wsId) });
      toast.success(t(($) => $.wecom.byo_success_toast));
      setDialogOpen(false);
      setBotId("");
      setSecret("");
      setBotName("");
    } catch (e) {
      // 服务端除英文描述外还会带一个稳定的 `code`，据此把失败原因翻成本地语言给管理员。
      // 英文描述仍作兜底：旧服务端或还没分配 code 的失败不会带 code。
      let localized: string | undefined;
      switch (errorCode(e)) {
        case "wecom_bot_owned_by_same_workspace":
          localized = t(($) => $.wecom.byo_conflict_same_workspace);
          break;
        case "wecom_bot_owned_by_archived_agent":
          localized = t(($) => $.wecom.byo_conflict_archived_agent);
          break;
        case "wecom_bot_owned_by_another_workspace":
          localized = t(($) => $.wecom.byo_conflict_other_workspace);
          break;
        case "wecom_install_rejected":
          localized = t(($) => $.wecom.byo_rejected);
          break;
        // 刻意与上面几种区分开：「有字段漏填」、「企微拒绝了」与「我们连不上企微」
        // 是管理员要做的三件不同的事，所以给三句不同的话。
        case "wecom_credentials_rejected":
          localized = t(($) => $.wecom.byo_credentials_rejected);
          break;
        case "wecom_credentials_unverifiable":
          localized = t(($) => $.wecom.byo_credentials_unverifiable);
          break;
        case "wecom_install_failed":
          localized = t(($) => $.wecom.byo_install_failed);
          break;
      }
      toast.error(
        localized ??
          (e instanceof Error ? e.message : t(($) => $.wecom.byo_failed_toast)),
      );
    } finally {
      setSubmitting(false);
    }
  }

  const canSubmit =
    botId.trim() !== "" && secret.trim() !== "" && !submitting;

  return (
    <div
      className={cn("flex flex-wrap items-center gap-2", className)}
      data-testid="wecom-agent-bind-buttons"
    >
      <Button
        variant="outline"
        size="sm"
        onClick={() => setDialogOpen(true)}
        disabled={!agentId}
        title={
          agentName
            ? t(($) => $.wecom.bind_button_title, { agent: agentName })
            : undefined
        }
        data-testid="wecom-agent-connect"
      >
        <WecomMark className="h-3 w-3" />
        {t(($) => $.wecom.bind_button)}
      </Button>

      <Dialog
        open={dialogOpen}
        onOpenChange={(v) => (v ? setDialogOpen(true) : closeDialog())}
      >
        <DialogContent className="sm:max-w-lg" data-testid="wecom-byo-dialog">
          <DialogHeader>
            <DialogTitle>{t(($) => $.wecom.byo_dialog_title)}</DialogTitle>
          </DialogHeader>

          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="wecom-byo-bot-id">
                {t(($) => $.wecom.byo_bot_id_label)}
              </Label>
              <Input
                id="wecom-byo-bot-id"
                data-testid="wecom-byo-bot-id"
                value={botId}
                onChange={(e) => setBotId(e.target.value)}
                placeholder={t(($) => $.wecom.byo_bot_id_placeholder)}
                autoComplete="off"
                spellCheck={false}
                disabled={submitting}
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="wecom-byo-secret">
                {t(($) => $.wecom.byo_secret_label)}
              </Label>
              <Input
                id="wecom-byo-secret"
                data-testid="wecom-byo-secret"
                type="password"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                placeholder={t(($) => $.wecom.byo_secret_placeholder)}
                autoComplete="off"
                spellCheck={false}
                disabled={submitting}
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="wecom-byo-bot-name">
                {t(($) => $.wecom.byo_bot_name_label)}
              </Label>
              <Input
                id="wecom-byo-bot-name"
                data-testid="wecom-byo-bot-name"
                value={botName}
                onChange={(e) => setBotName(e.target.value)}
                placeholder={t(($) => $.wecom.byo_bot_name_placeholder)}
                autoComplete="off"
                spellCheck={false}
                disabled={submitting}
              />
              <p className="text-caption text-muted-foreground">
                {t(($) => $.wecom.byo_bot_name_hint)}
              </p>
            </div>
          </div>

          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              onClick={closeDialog}
              disabled={submitting}
            >
              {t(($) => $.wecom.byo_cancel)}
            </Button>
            <Button
              size="sm"
              onClick={handleSubmit}
              disabled={!canSubmit}
              data-testid="wecom-byo-submit"
            >
              {submitting
                ? t(($) => $.wecom.byo_submitting)
                : t(($) => $.wecom.byo_submit)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// WecomAgentBotStatusRow 是 agent 检查器渲染的紧凑只读「已连接」入口（onShowConnectedDetails
// 存在时使用），点击跳到集成设置页统一管理。
function WecomAgentBotStatusRow({
  onClick,
  className,
}: {
  onClick: () => void;
  className?: string;
}) {
  const { t } = useT("settings");
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-caption text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
        className,
      )}
      data-testid="wecom-agent-bot-status"
    >
      <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
      <span className="truncate">{t(($) => $.wecom.agent_bot_connected_label)}</span>
      <ChevronRight className="ml-auto h-3.5 w-3.5 shrink-0" />
    </button>
  );
}

// WecomAgentBotConnectedBadge 是「已连接」的完整展示：状态（含 bot_id）+ 断开按钮，
// 仅 owner/admin 会到达此组件。
function WecomAgentBotConnectedBadge({
  installation,
  className,
}: {
  installation: WecomInstallation;
  className?: string;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();

  const [confirmOpen, setConfirmOpen] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleDisconnect() {
    if (disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteWecomInstallation(wsId, installation.id);
      await qc.invalidateQueries({ queryKey: wecomKeys.installations(wsId) });
      toast.success(t(($) => $.wecom.toast_disconnected));
      setConfirmOpen(false);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.wecom.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div
      className={cn("space-y-2", className)}
      data-testid="wecom-agent-bot-connected"
    >
      <div className="flex items-center justify-between gap-3">
        <span className="inline-flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
          <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
          <span className="truncate">
            {t(($) => $.wecom.agent_bot_connected_label_with_id, {
              botId: installation.bot_id,
            })}
          </span>
        </span>
        <Button
          variant="destructive"
          size="sm"
          onClick={() => setConfirmOpen(true)}
          disabled={disconnecting}
          title={t(($) => $.wecom.agent_bot_disconnect_tooltip)}
          aria-label={t(($) => $.wecom.disconnect)}
          data-testid="wecom-agent-bot-disconnect"
        >
          <Trash2 className="h-3 w-3" />
          {disconnecting
            ? t(($) => $.wecom.disconnecting)
            : t(($) => $.wecom.disconnect)}
        </Button>
      </div>

      <AlertDialog
        open={confirmOpen}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setConfirmOpen(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.wecom.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.wecom.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.wecom.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.wecom.disconnecting)
                : t(($) => $.wecom.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
