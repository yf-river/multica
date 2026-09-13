// 本文件包含两块：① 设置页「集成」里的 Telegram 频道面板（TelegramTab），列出
// 已接入的机器人并支持管理员断开；② 供 agent 详情页复用的「绑定 Telegram 机器人」
// 按钮与其已连接态展示（TelegramAgentBindButton 及两个配套小组件）。

"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronRight, ExternalLink, Trash2 } from "lucide-react";
import { TelegramMark } from "./telegram-mark";
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
import { telegramInstallationsOptions, telegramKeys } from "@multica/core/telegram";
import { api } from "@multica/core/api";
import type { TelegramInstallation } from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import { openExternal } from "../../platform";
import { useLocale, useT } from "../../i18n";

/**
 * Telegram 频道设置面板，与 SlackTab 镜像：列表对所有成员可见；断开操作仅限管理员
 * （后端强制校验，前端把按钮藏掉与之对齐）。新装机器人不在这里发起——装机器人的
 * 路径在 agent 详情页、按 agent 走（每个 agent 一个机器人，(workspace_id, agent_id,
 * channel_type) 构成 channel_installation 表的唯一约束）。
 */
export function TelegramTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManage =
    currentMember?.role === "owner" || currentMember?.role === "admin";

  const { data, isLoading, isError } = useQuery({
    ...telegramInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const installations = data?.installations ?? [];
  const configured = data?.configured === true;

  const [disconnectTarget, setDisconnectTarget] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      // 先等服务端返回再动缓存/界面（仓库规则：确认后销毁类的流程不做乐观删除）。
      await api.deleteTelegramInstallation(wsId, disconnectTarget);
      await qc.invalidateQueries({ queryKey: telegramKeys.installations(wsId) });
      toast.success(t(($) => $.telegram.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.telegram.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div className="space-y-8">
      {isError ? (
        <Card>
          <CardContent>
            <p className="text-body text-muted-foreground">
              {t(($) => $.telegram.load_failed)}
            </p>
          </CardContent>
        </Card>
      ) : isLoading ? (
        <Card>
          <CardContent>
            <p className="text-body text-muted-foreground">{t(($) => $.telegram.loading)}</p>
          </CardContent>
        </Card>
      ) : !configured ? (
        <Card>
          <CardContent className="space-y-2">
            <p className="text-body font-medium">{t(($) => $.telegram.not_enabled_title)}</p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.telegram.not_enabled_description_prefix)}{" "}
              <code className="rounded bg-muted px-1 py-0.5 text-micro">
                MULTICA_TELEGRAM_SECRET_KEY
              </code>{" "}
              {t(($) => $.telegram.not_enabled_description_suffix)}{" "}
              {t(($) => $.telegram.not_enabled_self_host_hint)}
            </p>
          </CardContent>
        </Card>
      ) : (
        <section className="space-y-3">
          <h2 className="text-body font-semibold">{t(($) => $.telegram.connected_bots)}</h2>
          {installations.length === 0 ? (
            <Card>
              <CardContent className="space-y-2">
                <p className="text-body font-medium">{t(($) => $.telegram.empty_title)}</p>
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.telegram.empty_description_prefix)}{" "}
                  <strong>{t(($) => $.telegram.empty_description_cta)}</strong>{" "}
                  {t(($) => $.telegram.empty_description_suffix)}
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
              {t(($) => $.telegram.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.telegram.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.telegram.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.telegram.disconnecting)
                : t(($) => $.telegram.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/** 单个已接入机器人的行：agent 头像 + 名称（可带 @机器人名与「已撤销」角标）、
 *  接入时间；仅对「活跃且当前有管理权」的行显示断开按钮。 */
function InstallationRow({
  installation,
  canManage,
  onDisconnect,
}: {
  installation: TelegramInstallation;
  canManage: boolean;
  onDisconnect: () => void;
}) {
  const { t } = useT("settings");
  const locale = useLocale();
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
            {installation.bot_username ? (
              <span className="ml-2 text-caption text-muted-foreground">
                @{installation.bot_username}
              </span>
            ) : null}
            {!isActive && (
              <span className="ml-2 rounded bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">
                {t(($) => $.telegram.revoked_badge)}
              </span>
            )}
          </p>
          <p className="text-micro text-muted-foreground">
            {t(($) => $.telegram.installed_at_label, {
              when: new Date(installation.installed_at).toLocaleString(locale),
            })}
          </p>
        </div>
      </div>
      {canManage && isActive && (
        <Button variant="outline" size="sm" onClick={onDisconnect}>
          <Trash2 className="h-3 w-3" />
          {t(($) => $.telegram.disconnect)}
        </Button>
      )}
    </div>
  );
}

// telegramDocsUrl 指向文档站点的 Telegram 接入指南，前缀按界面语言本地化
// （与 Slack 文档链接的做法一致）。
function telegramDocsUrl(lang: string | undefined): string {
  const prefix = lang?.startsWith("zh")
    ? "/zh"
    : lang?.startsWith("ja")
      ? "/ja"
      : lang?.startsWith("ko")
        ? "/ko"
        : "";
  return `https://multica.ai/docs${prefix}/telegram-bot-integration`;
}

/**
 * agent 详情页上的「绑定 Telegram 机器人」按钮（供 agent 页复用）。
 * 采用「粘贴令牌」模型：管理员先用 @BotFather 建好机器人并把令牌粘进来，后端在
 * 持久化前会通过 getMe 校验令牌。可见性与 SlackAgentBindButton 对齐（仅 owner/admin）。
 * 若该 agent 已连接：有 onShowConnectedDetails 时渲染可点击的只读状态行，否则渲染
 * 完整的「已连接」角标。
 */
export function TelegramAgentBindButton({
  agentId,
  agentName,
  className,
  onShowConnectedDetails,
}: {
  agentId: string;
  agentName?: string;
  className?: string;
  /** agent 检查器会传一个「跳到集成设置页」的处理器，此时只渲染紧凑的只读已连接行、
   *  把管理动作都收口在集成设置页一处。 */
  onShowConnectedDetails?: () => void;
}) {
  const { t, i18n } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [botToken, setBotToken] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const { data: listing } = useQuery({
    ...telegramInstallationsOptions(wsId),
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
      <TelegramAgentBotStatusRow
        onClick={onShowConnectedDetails}
        className={className}
      />
    ) : (
      <TelegramAgentBotConnectedBadge installation={existing} className={className} />
    );
  }

  if (!installSupported) return null;

  function closeDialog() {
    if (submitting) return;
    setDialogOpen(false);
    setBotToken("");
  }

  async function handleSubmit() {
    const bot_token = botToken.trim();
    if (submitting || !agentId || !bot_token) return;
    setSubmitting(true);
    try {
      const installation = await api.registerTelegramBot(wsId, agentId, { bot_token });
      if (!installation.id || installation.status !== "active") {
        throw new Error("Telegram connection returned an invalid installation");
      }
      // telegram_installation 的实时事件也会刷新这个列表，但这里仍显式失效一次，
      // 让「已连接」角标立刻出现、不依赖事件到达。
      await qc.invalidateQueries({ queryKey: telegramKeys.installations(wsId) });
      toast.success(t(($) => $.telegram.connect_success_toast));
      setDialogOpen(false);
      setBotToken("");
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.telegram.connect_failed_toast),
      );
    } finally {
      setSubmitting(false);
    }
  }

  const canSubmit = botToken.trim() !== "" && !submitting;

  return (
    <div
      className={cn("flex flex-wrap items-center gap-2", className)}
      data-testid="telegram-agent-bind-buttons"
    >
      <Button
        variant="outline"
        size="sm"
        onClick={() => setDialogOpen(true)}
        disabled={!agentId}
        title={
          agentName
            ? t(($) => $.telegram.bind_button_title, { agent: agentName })
            : undefined
        }
        data-testid="telegram-agent-connect"
      >
        <TelegramMark className="h-3 w-3" />
        {t(($) => $.telegram.bind_button)}
      </Button>

      <Dialog
        open={dialogOpen}
        onOpenChange={(v) => (v ? setDialogOpen(true) : closeDialog())}
      >
        <DialogContent className="sm:max-w-lg" data-testid="telegram-connect-dialog">
          <DialogHeader>
            <DialogTitle>{t(($) => $.telegram.connect_dialog_title)}</DialogTitle>
          </DialogHeader>

          <p className="text-caption text-muted-foreground">
            {t(($) => $.telegram.connect_dialog_description)}
          </p>

          <button
            type="button"
            onClick={() => openExternal(telegramDocsUrl(i18n.language))}
            className="inline-flex w-fit items-center gap-2 text-body font-medium text-primary underline-offset-2 hover:underline"
            data-testid="telegram-docs-link"
          >
            <ExternalLink className="h-4 w-4" />
            {t(($) => $.telegram.connect_docs_link)}
          </button>

          <div className="space-y-1.5">
            <Label htmlFor="telegram-bot-token">
              {t(($) => $.telegram.bot_token_label)}
            </Label>
            <Input
              id="telegram-bot-token"
              data-testid="telegram-bot-token"
              type="password"
              value={botToken}
              onChange={(e) => setBotToken(e.target.value)}
              // 只是提示令牌格式的占位文案，不是可复制的真实示例（故对该行做了禁用检查）。
              // eslint-disable-next-line no-restricted-syntax
              placeholder="123456789:AA…"
              autoComplete="off"
              spellCheck={false}
              disabled={submitting}
            />
          </div>

          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              onClick={closeDialog}
              disabled={submitting}
            >
              {t(($) => $.telegram.connect_cancel)}
            </Button>
            <Button
              size="sm"
              onClick={handleSubmit}
              disabled={!canSubmit}
              data-testid="telegram-connect-submit"
            >
              {submitting
                ? t(($) => $.telegram.connect_submitting)
                : t(($) => $.telegram.connect_submit)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// TelegramAgentBotStatusRow 是 agent 检查器渲染的紧凑只读「已连接」入口，
// 点击跳到「集成」设置页里做管理。
function TelegramAgentBotStatusRow({
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
      data-testid="telegram-agent-bot-status"
    >
      <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
      <span className="truncate">{t(($) => $.telegram.agent_bot_connected_label)}</span>
      <ChevronRight className="ml-auto h-3.5 w-3.5 shrink-0" />
    </button>
  );
}

// TelegramAgentBotConnectedBadge 是完整的「已连接」展示：状态 + 断开按钮，
// 外加一个打开机器人对话的「在 Telegram 中打开」链接。
function TelegramAgentBotConnectedBadge({
  installation,
  className,
}: {
  installation: TelegramInstallation;
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
      await api.deleteTelegramInstallation(wsId, installation.id);
      await qc.invalidateQueries({ queryKey: telegramKeys.installations(wsId) });
      toast.success(t(($) => $.telegram.toast_disconnected));
      setConfirmOpen(false);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.telegram.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div
      className={cn("space-y-2", className)}
      data-testid="telegram-agent-bot-connected"
    >
      <div className="flex items-center justify-between gap-3">
        <span className="inline-flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
          <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
          <span className="truncate">
            {t(($) => $.telegram.agent_bot_connected_label)}
            {installation.bot_username ? ` · @${installation.bot_username}` : ""}
          </span>
        </span>
        <Button
          variant="destructive"
          size="sm"
          onClick={() => setConfirmOpen(true)}
          disabled={disconnecting}
          title={t(($) => $.telegram.agent_bot_disconnect_tooltip)}
          aria-label={t(($) => $.telegram.disconnect)}
          data-testid="telegram-agent-bot-disconnect"
        >
          <Trash2 className="h-3 w-3" />
          {disconnecting
            ? t(($) => $.telegram.disconnecting)
            : t(($) => $.telegram.disconnect)}
        </Button>
      </div>

      {installation.bot_username && (
        <button
          type="button"
          onClick={() => openExternal(`https://t.me/${installation.bot_username}`)}
          className="inline-flex items-center gap-1 text-caption text-muted-foreground underline-offset-2 transition-colors hover:text-foreground hover:underline"
          title={t(($) => $.telegram.agent_bot_manage_tooltip)}
        >
          <ExternalLink className="h-3 w-3" />
          {t(($) => $.telegram.agent_bot_manage_link)}
        </button>
      )}

      <AlertDialog
        open={confirmOpen}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setConfirmOpen(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.telegram.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.telegram.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.telegram.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.telegram.disconnecting)
                : t(($) => $.telegram.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
