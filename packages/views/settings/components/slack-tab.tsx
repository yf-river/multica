// 本文件包含两块：① 设置页「集成」里的 Slack 频道面板（SlackTab），列出已接入的
// 机器人并支持管理员断开；② 供 agent 详情页复用的「连接 Slack 机器人」按钮及其
// 已连接态展示（SlackAgentBindButton 及两个配套小组件）。

"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronRight, ExternalLink, Trash2 } from "lucide-react";
import { SlackMark } from "./slack-mark";
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
import { slackInstallationsOptions, slackKeys } from "@multica/core/slack";
import { api } from "@multica/core/api";
import type { SlackInstallation } from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import { openExternal } from "../../platform";
import { useLocale, useT } from "../../i18n";

/**
 * Slack 频道设置面板：列出已接入的 Slack 机器人。列表对所有成员可见；断开仅限
 * 管理员（后端强制，前端对非管理员隐藏按钮以对齐）。
 *
 * 新装机器人的入口不在本页、而在 agent 详情页：接入是按 agent 走的（每个 Multica
 * agent 恰好一个机器人——(workspace_id, agent_id, channel_type) 在
 * channel_installation 表里构成唯一约束），若在这里让用户挑 agent，等于把那边
 * 的选人器重复造一遍。
 */
export function SlackTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManage =
    currentMember?.role === "owner" || currentMember?.role === "admin";

  const { data, isLoading } = useQuery({
    ...slackInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const installations = data?.installations ?? [];
  const configured = data?.configured === true;
  // install_supported 表示服务端是否已配好 OAuth 客户端凭据。为 false 时点「连接
  // Slack」只会得到 503，因此隐藏所有连接入口、改放一条「即将到来」提示；已装好的
  // 机器人仍然列出并保持可管理。
  const installSupported = data?.install_supported === true;

  const [disconnectTarget, setDisconnectTarget] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteSlackInstallation(wsId, disconnectTarget);
      await qc.invalidateQueries({ queryKey: slackKeys.installations(wsId) });
      toast.success(t(($) => $.slack.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.slack.toast_disconnect_failed),
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
            <p className="text-body font-medium">{t(($) => $.slack.not_enabled_title)}</p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.slack.not_enabled_description_prefix)}{" "}
              <code className="rounded bg-muted px-1 py-0.5 text-micro">
                MULTICA_SLACK_SECRET_KEY
              </code>{" "}
              {t(($) => $.slack.not_enabled_description_suffix)}{" "}
              {t(($) => $.slack.not_enabled_self_host_hint)}
            </p>
          </CardContent>
        </Card>
      ) : !installSupported && installations.length === 0 ? (
        <Card>
          <CardContent className="space-y-2">
            <p className="text-body font-medium">{t(($) => $.slack.preview_title)}</p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.slack.preview_description)}
            </p>
          </CardContent>
        </Card>
      ) : (
        <section className="space-y-3">
          <h2 className="text-body font-semibold">{t(($) => $.slack.connected_bots)}</h2>
          {isLoading ? (
            <Card>
              <CardContent>
                <p className="text-body text-muted-foreground">{t(($) => $.slack.loading)}</p>
              </CardContent>
            </Card>
          ) : installations.length === 0 ? (
            <Card>
              <CardContent className="space-y-2">
                <p className="text-body font-medium">{t(($) => $.slack.empty_title)}</p>
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.slack.empty_description_prefix)}{" "}
                  <strong>{t(($) => $.slack.empty_description_cta)}</strong>{" "}
                  {t(($) => $.slack.empty_description_suffix)}
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
              {t(($) => $.slack.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.slack.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.slack.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.slack.disconnecting)
                : t(($) => $.slack.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/** 单个已接入机器人的行：agent 头像 + 名称（可带「已撤销」角标）与接入时间；
 *  仅对「活跃且当前有管理权」的行显示断开按钮。 */
function InstallationRow({
  installation,
  canManage,
  onDisconnect,
}: {
  installation: SlackInstallation;
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
            {!isActive && (
              <span className="ml-2 rounded bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">
                {t(($) => $.slack.revoked_badge)}
              </span>
            )}
          </p>
          <p className="text-micro text-muted-foreground">
            {t(($) => $.slack.installed_at_label, {
              when: new Date(installation.installed_at).toLocaleString(locale),
            })}
          </p>
        </div>
      </div>
      {canManage && isActive && (
        <Button variant="outline" size="sm" onClick={onDisconnect}>
          <Trash2 className="h-3 w-3" />
          {t(($) => $.slack.disconnect)}
        </Button>
      )}
    </div>
  );
}

// SLACK_BYO_VIDEO_URL 是从连接弹窗里指向「设置教程视频」的可选链接。留 "" 即隐藏
// 链接；等录好「如何创建 Slack App 并复制两个令牌」的演示后再填上。
const SLACK_BYO_VIDEO_URL = "";

// slackDocsUrl 指向文档站点的 Slack 接入指南，按阅读者语言加 /<lang>/ 前缀本地化
// （英文无前缀），与 App 里其它文档链接约定一致（例如 autopilot webhook 文档链接）。
function slackDocsUrl(lang: string | undefined): string {
  const prefix = lang?.startsWith("zh")
    ? "/zh"
    : lang?.startsWith("ja")
      ? "/ja"
      : lang?.startsWith("ko")
        ? "/ko"
        : "";
  return `https://multica.ai/docs${prefix}/slack-bot-integration`;
}

/**
 * agent 详情页上的「连接 Slack 机器人」按钮（供 agent 页复用）。
 * Slack 走「自备 App」模型：点开弹窗，管理员粘入自己创建的 Slack App 的机器人令牌
 * （xoxb-）与应用级令牌（xapp-）（后端会校验两者同属一个 App）。可见性：
 *   1. 非 owner/admin 什么也不显示（后端负责门控安装/撤销）；
 *   2. 该 agent 已有活跃安装时，显示已连接角标（已装机器人保持可管理）；
 *   3. 否则只要安装可用就显示连接按钮。
 */
export function SlackAgentBindButton({
  agentId,
  agentName,
  className,
  onShowConnectedDetails,
}: {
  agentId: string;
  agentName?: string;
  className?: string;
  /**
   * 传入时，已连接状态渲染成一行紧凑的只读状态行（点击触发该回调），而不是带
   * 行内操作的完整角标——agent 检查器传的是「跳到集成设置页」的处理器，把管理
   * 动作统一收口在设置页一处。
   */
  onShowConnectedDetails?: () => void;
}) {
  const { t, i18n } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [botToken, setBotToken] = useState("");
  const [appToken, setAppToken] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const { data: listing } = useQuery({
    ...slackInstallationsOptions(wsId),
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
      <SlackAgentBotStatusRow
        onClick={onShowConnectedDetails}
        className={className}
      />
    ) : (
      <SlackAgentBotConnectedBadge installation={existing} className={className} />
    );
  }

  if (!installSupported) return null;

  function closeDialog() {
    if (submitting) return;
    setDialogOpen(false);
    setBotToken("");
    setAppToken("");
  }

  async function handleSubmit() {
    const bot_token = botToken.trim();
    const app_token = appToken.trim();
    if (submitting || !agentId || !bot_token || !app_token) return;
    setSubmitting(true);
    try {
      await api.registerSlackBYO(wsId, agentId, { bot_token, app_token });
      // slack_installation 的实时事件也会刷新这个列表，但这里仍显式失效一次，
      // 让「已连接」角标立刻出现、不依赖事件到达。
      await qc.invalidateQueries({ queryKey: slackKeys.installations(wsId) });
      toast.success(t(($) => $.slack.byo_success_toast));
      setDialogOpen(false);
      setBotToken("");
      setAppToken("");
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.slack.byo_failed_toast),
      );
    } finally {
      setSubmitting(false);
    }
  }

  const canSubmit =
    botToken.trim() !== "" && appToken.trim() !== "" && !submitting;

  return (
    <div
      className={cn("flex flex-wrap items-center gap-2", className)}
      data-testid="slack-agent-bind-buttons"
    >
      <Button
        variant="outline"
        size="sm"
        onClick={() => setDialogOpen(true)}
        disabled={!agentId}
        title={
          agentName
            ? t(($) => $.slack.bind_button_title, { agent: agentName })
            : undefined
        }
        data-testid="slack-agent-connect"
      >
        <SlackMark className="h-3 w-3" />
        {t(($) => $.slack.bind_button)}
      </Button>

      <Dialog
        open={dialogOpen}
        onOpenChange={(v) => (v ? setDialogOpen(true) : closeDialog())}
      >
        <DialogContent className="sm:max-w-lg" data-testid="slack-byo-dialog">
          <DialogHeader>
            <DialogTitle>{t(($) => $.slack.byo_dialog_title)}</DialogTitle>
          </DialogHeader>

          {SLACK_BYO_VIDEO_URL ? (
            <button
              type="button"
              onClick={() => openExternal(SLACK_BYO_VIDEO_URL)}
              className="inline-flex w-fit items-center gap-2 text-body font-medium text-primary underline-offset-2 hover:underline"
            >
              <ExternalLink className="h-4 w-4" />
              {t(($) => $.slack.byo_video_cta)}
            </button>
          ) : null}

          <button
            type="button"
            onClick={() => openExternal(slackDocsUrl(i18n.language))}
            className="inline-flex w-fit items-center gap-2 text-body font-medium text-primary underline-offset-2 hover:underline"
            data-testid="slack-byo-docs-link"
          >
            <ExternalLink className="h-4 w-4" />
            {t(($) => $.slack.byo_docs_link)}
          </button>

          <div className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="slack-byo-bot-token">
                {t(($) => $.slack.byo_bot_token_label)}
              </Label>
              <Input
                id="slack-byo-bot-token"
                data-testid="slack-byo-bot-token"
                value={botToken}
                onChange={(e) => setBotToken(e.target.value)}
                // 只是提示 Slack 令牌前缀的占位文案，不是可复制的真实示例（故禁用检查）。
                // eslint-disable-next-line no-restricted-syntax
                placeholder="xoxb-…"
                autoComplete="off"
                spellCheck={false}
                disabled={submitting}
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="slack-byo-app-token">
                {t(($) => $.slack.byo_app_token_label)}
              </Label>
              <Input
                id="slack-byo-app-token"
                data-testid="slack-byo-app-token"
                value={appToken}
                onChange={(e) => setAppToken(e.target.value)}
                // 只是提示 Slack 令牌前缀的占位文案，不是可复制的真实示例（故禁用检查）。
                // eslint-disable-next-line no-restricted-syntax
                placeholder="xapp-…"
                autoComplete="off"
                spellCheck={false}
                disabled={submitting}
              />
            </div>
          </div>

          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              onClick={closeDialog}
              disabled={submitting}
            >
              {t(($) => $.slack.byo_cancel)}
            </Button>
            <Button
              size="sm"
              onClick={handleSubmit}
              disabled={!canSubmit}
              data-testid="slack-byo-submit"
            >
              {submitting
                ? t(($) => $.slack.byo_submitting)
                : t(($) => $.slack.byo_submit)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// SlackAgentBotStatusRow 是 agent 检查器渲染的紧凑只读「已连接」入口，代替完整角标；
// 点击深链到集成设置页，管理 / 断开都在那边。
function SlackAgentBotStatusRow({
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
      data-testid="slack-agent-bot-status"
    >
      <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
      <span className="truncate">{t(($) => $.slack.agent_bot_connected_label)}</span>
      <ChevronRight className="ml-auto h-3.5 w-3.5 shrink-0" />
    </button>
  );
}

// SlackAgentBotConnectedBadge 是「已连接」的完整展示（集成设置页用它替换连接按钮）：
// 两行——状态 + 偏破坏色的断开按钮，以及次要的「在 Slack 中打开」链接到已装工作区。
// 只有 owner/admin 会到达这个组件。
function SlackAgentBotConnectedBadge({
  installation,
  className,
}: {
  installation: SlackInstallation;
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
      await api.deleteSlackInstallation(wsId, installation.id);
      await qc.invalidateQueries({ queryKey: slackKeys.installations(wsId) });
      toast.success(t(($) => $.slack.toast_disconnected));
      setConfirmOpen(false);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.slack.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div
      className={cn("space-y-2", className)}
      data-testid="slack-agent-bot-connected"
    >
      <div className="flex items-center justify-between gap-3">
        <span className="inline-flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
          <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
          <span className="truncate">{t(($) => $.slack.agent_bot_connected_label)}</span>
        </span>
        <Button
          variant="destructive"
          size="sm"
          onClick={() => setConfirmOpen(true)}
          disabled={disconnecting}
          title={t(($) => $.slack.agent_bot_disconnect_tooltip)}
          aria-label={t(($) => $.slack.disconnect)}
          data-testid="slack-agent-bot-disconnect"
        >
          <Trash2 className="h-3 w-3" />
          {disconnecting
            ? t(($) => $.slack.disconnecting)
            : t(($) => $.slack.disconnect)}
        </Button>
      </div>

      {installation.team_id && (
        <button
          type="button"
          onClick={() =>
            openExternal(`https://app.slack.com/client/${installation.team_id}`)
          }
          className="inline-flex items-center gap-1 text-caption text-muted-foreground underline-offset-2 transition-colors hover:text-foreground hover:underline"
          title={t(($) => $.slack.agent_bot_manage_tooltip)}
        >
          <ExternalLink className="h-3 w-3" />
          {t(($) => $.slack.agent_bot_manage_link)}
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
              {t(($) => $.slack.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.slack.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.slack.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.slack.disconnecting)
                : t(($) => $.slack.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
