// 本文件是「飞书 / Lark 机器人接入」相关组件集合：
// LarkTab 是工作区设置里的安装管理面板（列表对成员可见，断开仅限管理员）；
// LarkAgentBindButton 等是 agent 详情页上的入口按钮，"已连接"徽标以及设备流扫码安装弹窗。

"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronRight, ExternalLink, RefreshCw, Trash2 } from "lucide-react";
// 必须用具名导入而不是默认导入：react-qr-code 是 CJS 模块，部分打包器会把整个模块
// 命名空间当作默认导出暴露；这里的具名导出才直接对应 `exports.QRCode`。
import { QRCode } from "react-qr-code";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
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
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@multica/ui/components/ui/dialog";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useActorName } from "@multica/core/workspace/hooks";
import { larkInstallationsOptions, larkKeys } from "@multica/core/lark";
import { api, ApiError } from "@multica/core/api";
import type { LarkInstallation, LarkInstallStatusResponse } from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import { useLocale, useT } from "../../i18n";

// MUL-3083：Lark（国际版，open.larksuite.com）的「接入机器人」入口暂时隐藏，
// 因为它的"安装 → 消息入站"链路还在打磨中——有些安装明明在 Lark 侧完成了，却一直
// 没有落下一行 lark_installation 记录，结果是机器人收不到消息。
// 大陆版飞书不受影响、保留绑定入口；已存在的安装（无论哪种云）都仍可完整管理。
// 想恢复「接入 Lark」按钮，把这里改回 true 即可，别处无需改动。
const LARK_INTL_CONNECT_ENABLED: boolean = false;

/**
 * 工作区设置里的「飞书 / Lark 机器人安装」面板。
 * 列表对成员可见；断开连接仅限管理员（后端强制校验，UI 对非管理员隐藏按钮保持一致）。
 *
 * 新增安装不在本页进行，而是走 Agent 详情页：安装是按 agent 一对一绑定的
 * （每个 Multica Agent 恰好一个机器人，见 lark_installation 上 (workspace_id,
 * agent_id) 的 UNIQUE），在这里再让用户挑一次 agent 只是在重复那边的选择器。
 * 空态里的"给第一个 agent 绑定"文案就是引导用户去正确的入口。
 */
export function LarkTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManage =
    currentMember?.role === "owner" || currentMember?.role === "admin";

  const { data, isLoading } = useQuery({
    ...larkInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const installations = data?.installations ?? [];
  const configured = data?.configured === true;
  // install_supported 表示"扫码安装"这条设备流链路在服务端是否端到端接通。
  // 为 false 时，扫码绑定会在轮询后的 bot 信息拉取步骤失败，所以隐藏一切安装入口，
  // 原地换成"即将上线"的提示，别把用户送进一条注定失败的流程。已装好的机器人
  // 仍会出现在下面的列表里，继续可管理。
  const installSupported = data?.install_supported === true;

  const [disconnectTarget, setDisconnectTarget] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteLarkInstallation(wsId, disconnectTarget);
      await qc.invalidateQueries({ queryKey: larkKeys.installations(wsId) });
      toast.success(t(($) => $.lark.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.lark.toast_disconnect_failed));
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div className="space-y-8">
      {!configured ? (
        <Card>
          <CardContent className="space-y-2">
            <p className="text-body font-medium">{t(($) => $.lark.not_enabled_title)}</p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.lark.not_enabled_description_prefix)}{" "}
              <code className="rounded bg-muted px-1 py-0.5 text-micro">
                MULTICA_LARK_SECRET_KEY
              </code>{" "}
              {t(($) => $.lark.not_enabled_description_suffix)}{" "}
              {t(($) => $.lark.not_enabled_self_host_hint)}
            </p>
          </CardContent>
        </Card>
      ) : !installSupported && installations.length === 0 ? (
        // 设备流安装链路没有接通（HTTP 客户端还是 stub，或 RegistrationService
        // 没初始化）。这里故意不把用户导向 agent 详情页的「绑定」按钮，因为后端
        // 反正也会返回 503。已存在的安装仍会走下面的分支正常渲染；这里只负责在
        // "没有任何可管理对象"时把空态的引导入口藏起来。
        <Card>
          <CardContent className="space-y-2">
            <p className="text-body font-medium">{t(($) => $.lark.preview_title)}</p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.lark.preview_description)}
            </p>
          </CardContent>
        </Card>
      ) : (
        <section className="space-y-3">
          <h2 className="text-body font-semibold">{t(($) => $.lark.connected_bots)}</h2>
          {isLoading ? (
            <Card>
              <CardContent>
                <p className="text-body text-muted-foreground">{t(($) => $.lark.loading)}</p>
              </CardContent>
            </Card>
          ) : installations.length === 0 ? (
            <Card>
              <CardContent className="space-y-2">
                <p className="text-body font-medium">{t(($) => $.lark.empty_title)}</p>
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.lark.empty_description_prefix)}{" "}
                  <strong>{t(($) => $.lark.empty_description_cta)}</strong>{" "}
                  {t(($) => $.lark.empty_description_suffix)}
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
              {t(($) => $.lark.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.lark.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.lark.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.lark.disconnecting)
                : t(($) => $.lark.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/** 单条 Lark 安装行：展示绑定 agent 的头像/名称、区域徽标（飞书/Lark）、安装时间；活跃时管理员可断开。 */
function InstallationRow({
  installation,
  canManage,
  onDisconnect,
}: {
  installation: LarkInstallation;
  canManage: boolean;
  onDisconnect: () => void;
}) {
  const { t } = useT("settings");
  const locale = useLocale();
  // 机器人跟某个 Multica Agent 是 1:1 绑定的（lark_installation 里 (workspace_id,
  // agent_id) 的 UNIQUE）。这里展示 agent 的身份，而不是 Lark 原始的 app_id /
  // bot_open_id——那串东西对产品用户毫无意义。agent 被删后 getAgentName 会退回
  // 显示"未知 Agent"，下面的「断开连接」就是清理这条孤儿记录的出口。
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
            <span className="ml-2 rounded bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">
              {installation.region === "lark"
                ? t(($) => $.lark.region_lark)
                : t(($) => $.lark.region_feishu)}
            </span>
            {!isActive && (
              <span className="ml-2 rounded bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">
                {t(($) => $.lark.revoked_badge)}
              </span>
            )}
          </p>
          <p className="text-micro text-muted-foreground">
            {t(($) => $.lark.installed_at_label, {
              when: new Date(installation.installed_at).toLocaleString(locale),
            })}
          </p>
        </div>
      </div>
      {canManage && isActive && (
        <Button variant="outline" size="sm" onClick={onDisconnect}>
          <Trash2 className="h-3 w-3" />
          {t(($) => $.lark.disconnect)}
        </Button>
      )}
    </div>
  );
}

/**
 * agent 详情页暴露的「接入飞书 / Lark」按钮。上面的 Settings 面板是管理视图，
 * 这个按钮才是安装入口。
 *
 * 可见性规则（按顺序）：
 *   1. 只有该 agent 的 owner 或工作区 owner/admin 能看到任何东西——后端对
 *      `POST /lark/install/begin`、状态轮询和断开连接都按 canManageAgent
 *      （agent owner 或工作区 owner/admin，见 server/internal/handler/lark.go，
 *      MUL-4213）鉴权，所以这里的闸门要与后端对齐。`agentOwnerId` 就是用来放行
 *      非管理员 owner 的；不传时按钮保持"仅工作区 owner/admin 可用"。
 *   2. 若该 agent 已有生效中的安装，就展示「已连接 + 去 Lark 管理」徽标——
 *      无论 install_supported 是否可用。install_supported 只决定"新扫码安装"
 *      能否走通；设备流传输未接通时，已安装的机器人照样可管理（见
 *      server/internal/handler/lark.go——"已装的机器人仍然出现且可管理"）。
 *      让徽标也依赖它的话，传输一断，已绑定 agent 的连接态也会跟着消失。
 *   3. 其余情况，只有 install_supported 为 true 时才展示绑定按钮——对着 stub
 *      传输做全新扫码，会在轮询后的 bot 信息步骤失败，所以不把注定失败的流程亮出来。
 */
export function LarkAgentBindButton({
  agentId,
  agentName,
  agentOwnerId,
  className,
  onShowConnectedDetails,
}: {
  agentId: string;
  agentName?: string;
  /**
   * 被绑定 agent 的所有者（agent.owner_id）。当它与当前用户一致时，即使该用户
   * 不是工作区 owner/admin，按钮也视其为可管理机器人——照抄后端的 canManageAgent
   * 鉴权（MUL-4213）。省略该字段则按钮保持"仅工作区 owner/admin 可用"。
   */
  agentOwnerId?: string | null;
  className?: string;
  /**
   * 一旦传入，已连接状态就渲染成一行紧凑只读状态条，点击时回调它，而不是带行内
   * 「管理/断开」的完整徽标。agent 详情面板传的是"跳到 Integrations 标签页"的处理器，
   * 让左侧栏保持一眼可读的摘要，管理操作统一收在标签页里（tab 自身不传这个 prop，
   * 所以拿到的是完整徽标）。
   */
  onShowConnectedDetails?: () => void;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const user = useAuthStore((s) => s.user);
  // dialogRegion 用同一个变量装了两份状态：弹窗要面向哪朵云（决定设备流 begin 的
  // 域名和弹窗文案），以及弹窗是否打开（null = 关闭）。若拆成独立的布尔值，
  // 就得时时跟 region 保持同步；合并成一个，就能杜绝"弹窗开着但没选区域"的中间态。
  const [dialogRegion, setDialogRegion] = useState<"feishu" | "lark" | null>(
    null,
  );

  const { data: listing } = useQuery({
    ...larkInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const installSupported = listing?.install_supported === true;

  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: !!wsId,
  });
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const isWorkspaceAdmin =
    currentMember?.role === "owner" || currentMember?.role === "admin";
  const isAgentOwner =
    !!user?.id && agentOwnerId != null && agentOwnerId === user.id;
  // 与后端 canManageAgent 闸门对齐：agent 的 owner 或工作区 owner/admin 可以
  // 绑定 / 管理机器人（MUL-4213）。
  const canManage = isWorkspaceAdmin || isAgentOwner;

  if (!canManage) return null;

  // "是否已安装"的判断要先于 install_supported 闸门：就算新扫码安装不可用，
  // 已装好的机器人也仍可管理（server/internal/handler/lark.go）。在这里展示徽标
  // 还能堵住"重复扫码制造僵尸 bot"的坑——对同一 agent 重复扫码会 upsert 同一行
  // 记录，却让之前创建的 PersonalAgent 变成孤儿；所以要关掉安装入口，并把管理引导到
  // 机器人所在的 Lark 应用页（那里才真正管理权限范围、显示名、额外权限）。
  const existing = listing?.installations.find(
    (inst) => inst.agent_id === agentId && inst.status === "active",
  );
  if (existing) {
    return onShowConnectedDetails ? (
      <LarkAgentBotStatusRow
        installation={existing}
        onClick={onShowConnectedDetails}
        className={className}
      />
    ) : (
      <LarkAgentBotConnectedBadge installation={existing} className={className} />
    );
  }

  // 没有已装的机器人，且设备流传输没端到端接通：新扫码必然在轮询后的 bot 信息
  // 步骤失败，所以直接隐藏按钮。
  if (!installSupported) return null;

  // 每朵云一个按钮——飞书（大陆）在左、Lark（国际）在右。故意做成两个明确入口
  // 而不是"自动识别"的一个二维码，因为 Lark 只在用户授权后的轮询中途才吐
  // tenant_brand="lark"；在那之前，Lark 用户扫到的是 accounts.feishu.cn 下发的
  // 二维码，对国际用户造成过不少困惑（MUL-3083 的后续）。每个按钮把自己的 region
  // 传给安装弹窗，再由弹窗透传给后端，让设备流 begin 直接 POST 到对应的 accounts
  // 域名；RegistrationService 里"轮询中途按租户品牌自动切换"的兜底仍然保留，
  // 以防用户选错入口。
  return (
    <>
      <div
        className={cn("flex flex-wrap items-center gap-2", className)}
        data-testid="lark-agent-bind-buttons"
      >
        <Button
          variant="outline"
          size="sm"
          onClick={() => setDialogRegion("feishu")}
          disabled={!agentId}
          title={
            agentName
              ? t(($) => $.lark.bind_button_feishu_title, { agent: agentName })
              : undefined
          }
          data-testid="lark-agent-bind-feishu"
        >
          <ExternalLink className="h-3 w-3" />
          {t(($) => $.lark.bind_button_feishu)}
        </Button>
        {/* MUL-3083：Lark（国际版）绑定入口暂时隐藏——见 LARK_INTL_CONNECT_ENABLED。
            上面的大陆版飞书不受影响。 */}
        {LARK_INTL_CONNECT_ENABLED && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => setDialogRegion("lark")}
            disabled={!agentId}
            title={
              agentName
                ? t(($) => $.lark.bind_button_lark_title, { agent: agentName })
                : undefined
            }
            data-testid="lark-agent-bind-lark"
          >
            <ExternalLink className="h-3 w-3" />
            {t(($) => $.lark.bind_button_lark)}
          </Button>
        )}
      </div>
      {dialogRegion && (
        <LarkInstallDialog
          wsId={wsId}
          agentId={agentId}
          agentName={agentName}
          region={dialogRegion}
          onClose={() => setDialogRegion(null)}
        />
      )}
    </>
  );
}

/**
 * 紧凑只读的「已连接」状态条：agent 详情面板（左栏）用它替代完整徽标。
 * 只展示状态——绿点、飞书/Lark 区域徽标、一句"已连接"——整体是一个全宽的
 * 点击按钮，点击深链跳去 Integrations 标签页，那里才有管理/断开。
 * 把破坏性操作从常驻侧边栏里拿掉，意味着它只存在于一个地方。
 */
function LarkAgentBotStatusRow({
  installation,
  onClick,
  className,
}: {
  installation: LarkInstallation;
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
      data-testid="lark-agent-bot-status"
    >
      <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
      <span className="rounded bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">
        {installation.region === "lark"
          ? t(($) => $.lark.region_lark)
          : t(($) => $.lark.region_feishu)}
      </span>
      <span className="truncate">{t(($) => $.lark.agent_bot_connected_label)}</span>
      <ChevronRight className="ml-auto h-3.5 w-3.5 shrink-0" />
    </button>
  );
}

/** 依据机器人所在云返回对应的开放平台域名：Lark 国际版走 larksuite，其余一律按飞书处理。 */
function larkDevConsoleHost(region?: string): string {
  return region === "lark"
    ? "https://open.larksuite.com"
    : "https://open.feishu.cn";
}

/**
 * 完整版「已连接」徽标：当该 agent 已有生效中的 Lark 安装时，Integrations 标签页
 * 用它替代「绑定」按钮。（详情面板左栏改用深链跳转的紧凑版 LarkAgentBotStatusRow。）
 * 布局分两行：第 1 行左侧是绿点状态（含飞书/Lark 区域徽标），右侧是弱化风格的断开按钮；
 * 第 2 行是次要的「去 Lark 管理」链接，指向机器人开发控制台的应用页（新标签页打开）。
 * 断开按钮在确认弹窗通过后才会删除安装。
 *
 * 可见性规则继承自父组件 LarkAgentBindButton：只有 agent 的 owner 或工作区
 * owner/admin 才可能到达这个组件，因此解绑入口无条件展示——后端对 DELETE 用
 * 同一套 canManageAgent 鉴权（MUL-4213），其他人来了也是 403，再多加一道闸就是死代码。
 */
function LarkAgentBotConnectedBadge({
  installation,
  className,
}: {
  installation: LarkInstallation;
  className?: string;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const manageHref = `${larkDevConsoleHost(installation.region)}/app/${encodeURIComponent(installation.app_id)}`;

  const [confirmOpen, setConfirmOpen] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleDisconnect() {
    if (disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteLarkInstallation(wsId, installation.id);
      // 先失效缓存、后关弹窗：徽标会在安装列表查询更新后卸载（父组件切回"绑定"按钮），
      // 所以残留"打开中"的状态没关系——但在关闭前先把网络调用做完，
      // 能避免一闪而过的"仍显示已连接"的过期闪现。
      await qc.invalidateQueries({ queryKey: larkKeys.installations(wsId) });
      toast.success(t(($) => $.lark.toast_disconnected));
      setConfirmOpen(false);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.lark.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div
      className={cn("space-y-2", className)}
      data-testid="lark-agent-bot-connected"
    >
      {/* 第 1 行：左侧连接状态、右侧解绑（破坏性）操作。断开按钮用弱化描边的
          destructive 变体——看起来危险但不至于满版刺眼红，同时保持可见，因为它是
          install_supported=false / 重复扫码"僵尸 bot"陷阱的用户出口
          （server/internal/handler/lark.go）。确认弹窗不可省：后端断开会拆掉
          WebSocket、停止消息投递。 */}
      <div className="flex items-center justify-between gap-3">
        <span className="inline-flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
          <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
          <span className="rounded bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">
            {installation.region === "lark"
              ? t(($) => $.lark.region_lark)
              : t(($) => $.lark.region_feishu)}
          </span>
          <span className="truncate">{t(($) => $.lark.agent_bot_connected_label)}</span>
        </span>
        <Button
          variant="destructive"
          size="sm"
          onClick={() => setConfirmOpen(true)}
          disabled={disconnecting}
          title={t(($) => $.lark.agent_bot_disconnect_tooltip)}
          aria-label={t(($) => $.lark.disconnect)}
          data-testid="lark-agent-bot-disconnect"
        >
          <Trash2 className="h-3 w-3" />
          {disconnecting
            ? t(($) => $.lark.disconnecting)
            : t(($) => $.lark.disconnect)}
        </Button>
      </div>

      {/* 第 2 行：次要的「去 Lark 管理」链接，指向机器人开发控制台的应用页。
          放低到状态行之下，不再与主要的连接/断开意图抢注意力；
          区域感知的 tooltip 保留了飞书 / Lark 的区别。 */}
      <a
        href={manageHref}
        target="_blank"
        rel="noopener noreferrer"
        className="inline-flex items-center gap-1 text-caption text-muted-foreground underline-offset-2 transition-colors hover:text-foreground hover:underline"
        title={
          installation.region === "lark"
            ? t(($) => $.lark.agent_bot_manage_tooltip_lark)
            : t(($) => $.lark.agent_bot_manage_tooltip_feishu)
        }
      >
        <ExternalLink className="h-3 w-3" />
        {installation.region === "lark"
          ? t(($) => $.lark.agent_bot_manage_link_lark)
          : t(($) => $.lark.agent_bot_manage_link_feishu)}
      </a>

      <AlertDialog
        open={confirmOpen}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setConfirmOpen(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.lark.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.lark.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.lark.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDisconnect}
              disabled={disconnecting}
            >
              {disconnecting
                ? t(($) => $.lark.disconnecting)
                : t(($) => $.lark.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/**
 * 引导用户走完「设备流」安装流程的弹窗：
 * 1) POST /lark/install/begin?region=<feishu|lark> → 拿到会话，渲染二维码
 * 2) 轮询 /lark/install/{sessionId}/status，直到 success / error / 过期
 * 3) 成功后：toast 提示、关闭弹窗、失效 installations 缓存
 *
 * 每次「重试」都刻意重新申请一个全新会话，而不是复用旧 device_code——Lark 的
 * device_code 是一次性的，出错后把同一个二维码再渲染一遍，下一轮轮询照样失败。
 *
 * region 是必填参数：让 begin 的 POST 打到正确的云（accounts.feishu.cn 与
 * accounts.larksuite.com），弹窗文案（标题、扫码提示、链接兜底）也反映用户选的云。
 * 给默认值会把 Lark 用户悄悄引向飞书的二维码——正是这次拆分 CTA 想要消除的困惑。
 */
function LarkInstallDialog({
  wsId,
  agentId,
  agentName,
  region,
  onClose,
}: {
  wsId: string;
  agentId: string;
  agentName?: string;
  region: "feishu" | "lark";
  onClose: () => void;
}) {
  const { t } = useT("settings");
  const qc = useQueryClient();

  // 会话生命周期放在本地 state 而不是 TanStack Query：Query 是为"缓存的服务端读"
  // 优化的，而这里是一次性流程、关闭即全部作废；拿 useQuery 去轮询还会跟它默认的
  // 重取启发式（窗口聚焦、上下线、自动重试）打架——那些行为对一段有边界的会话不合适。
  const [session, setSession] = useState<null | {
    sessionId: string;
    qrCodeURL: string;
    expiresInSeconds: number;
    pollIntervalSeconds: number;
  }>(null);
  const [status, setStatus] = useState<LarkInstallStatusResponse["status"]>("pending");
  const [errorReason, setErrorReason] = useState<string | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [beginning, setBeginning] = useState(false);
  const closedRef = useRef(false);

  // beginSession 既会被初次挂载调用、也会被"重新扫码"调用。
  // 包成函数（而不是铺一串 useEffect 依赖）让重试路径一目了然。
  async function beginSession() {
    setBeginning(true);
    setStatus("pending");
    setErrorReason(null);
    setErrorMessage(null);
    setSession(null);
    try {
      const res = await api.beginLarkInstall(wsId, agentId, region);
      if (closedRef.current) return;
      setSession({
        sessionId: res.session_id,
        qrCodeURL: res.qr_code_url,
        expiresInSeconds: res.expires_in_seconds,
        pollIntervalSeconds: res.poll_interval_seconds,
      });
    } catch (e) {
      if (closedRef.current) return;
      setStatus("error");
      setErrorReason("internal_error");
      setErrorMessage(e instanceof Error ? e.message : String(e));
    } finally {
      setBeginning(false);
    }
  }

  // 挂载后立刻开始安装流程。
  //
  // closedRef 必须在每次挂载开始时重置，而不是只在构造时置一次。React 18+/19 的
  // StrictMode 开发模式会对同一组件实例把 effect 跑两遍（挂载 → 清理 → 再挂载），
  // 而 useRef 跨过这次"模拟重挂载"仍然保留原值。不重置的话，第一次挂载的清理把
  // closedRef.current 置成 true，第二次挂载里所有 beginSession 的 promise 在
  // await 边界看到 true 就直接提前退出，setSession 不会执行——弹窗正文将是空的
  // （没有"正在启动"占位、没有二维码、也没有错误），正是那个"二维码死活不出现"的 bug。
  // 所以要在进入时重置，让第二次挂载重新装好"飞行中可取消"的闸门。
  useEffect(() => {
    closedRef.current = false;
    void beginSession();
    return () => {
      closedRef.current = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 轮询循环。上界由设备流自身的有效期兜住：一旦过期，Lark 服务端会返回
  // expired_token、后端把会话标成 error，所以前端不需要另设一套过期计时器。
  useEffect(() => {
    if (!session || status !== "pending") return;
    const intervalMs = Math.max(2000, session.pollIntervalSeconds * 1000);
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    const poll = async () => {
      if (cancelled) return;
      try {
        const res = await api.getLarkInstallStatus(wsId, session.sessionId);
        if (cancelled) return;
        setStatus(res.status);
        if (res.status === "success") {
          await qc.invalidateQueries({ queryKey: larkKeys.installations(wsId) });
          toast.success(
            region === "lark"
              ? t(($) => $.lark.install_success_toast_lark)
              : t(($) => $.lark.install_success_toast_feishu),
          );
          // 成功后停顿一拍再关闭，让用户短暂看到"成功"状态、弹窗再消失。
          setTimeout(() => {
            if (!cancelled) onClose();
          }, 800);
          return;
        }
        if (res.status === "error") {
          setErrorReason(res.error_reason ?? "internal_error");
          setErrorMessage(res.error_message ?? null);
          return;
        }
        timer = setTimeout(poll, intervalMs);
      } catch (e) {
        if (cancelled) return;
        // 终态 HTTP 错误绝不能重试——会话已经不在了，或调用者失去了权限，
        // 无限轮询只会让用户对着过期二维码干等、还没有任何错误反馈。
        // 404：服务端重启、多实例路由到别处，或进程内 GC 回收了会话。
        //     按"会话丢失"处理——用户必须重新扫一个新码。
        // 403：会话中途权限被撤（角色被降级等）。入口的鉴权闸门挡住了绝大多数情况，
        //     但弹窗开着期间角色变动就会落在这里。
        // 401：全局 ApiClient 拦截器负责重新登录，能带着 401 走到 catch 说明
        //     重新登录本身也失败了——按终态处理，别让用户循环重试。
        if (e instanceof ApiError) {
          if (e.status === 404) {
            setStatus("error");
            setErrorReason("session_lost");
            setErrorMessage(e.message);
            return;
          }
          if (e.status === 403 || e.status === 401) {
            setStatus("error");
            setErrorReason("forbidden");
            setErrorMessage(e.message);
            return;
          }
        }
        // 瞬时错误（网络抖动、5xx）——排下一轮轮询而不是杀掉会话。
        // 后端下一次状态读取要么确认仍在 pending，要么把轮询协程记下的终态错误抛出来。
        timer = setTimeout(poll, intervalMs);
        // 用非阻塞 toast 把报错带出来，方便排查问题。
        toast.message(t(($) => $.lark.install_poll_retry), {
          description: e instanceof Error ? e.message : String(e),
        });
      }
    };

    timer = setTimeout(poll, intervalMs);
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session?.sessionId, status]);

  return (
    <Dialog
      open
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>
            {region === "lark"
              ? t(($) => $.lark.install_dialog_title_lark)
              : t(($) => $.lark.install_dialog_title_feishu)}
          </DialogTitle>
          <DialogDescription>
            {region === "lark"
              ? agentName
                ? t(($) => $.lark.install_dialog_description_for_agent_lark, { agent: agentName })
                : t(($) => $.lark.install_dialog_description_lark)
              : agentName
                ? t(($) => $.lark.install_dialog_description_for_agent_feishu, { agent: agentName })
                : t(($) => $.lark.install_dialog_description_feishu)}
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col items-center gap-4 py-2">
          {beginning && !session && (
            <p className="text-body text-muted-foreground">{t(($) => $.lark.install_starting)}</p>
          )}

          {session && status === "pending" && (
            <>
              <div className="rounded-md border bg-white p-3">
                {/* react-qr-code 渲染的是内联 SVG——不依赖外部网络图片，任意 DPI 都清晰。 */}
                <QRCode value={session.qrCodeURL} size={192} />
              </div>
              <p className="text-center text-caption text-muted-foreground">
                {region === "lark"
                  ? t(($) => $.lark.install_scan_hint_lark)
                  : t(($) => $.lark.install_scan_hint_feishu)}
              </p>
              <a
                href={session.qrCodeURL}
                target="_blank"
                rel="noopener noreferrer"
                className="text-caption underline text-muted-foreground"
              >
                {region === "lark"
                  ? t(($) => $.lark.install_open_link_fallback_lark)
                  : t(($) => $.lark.install_open_link_fallback_feishu)}
              </a>
            </>
          )}

          {status === "success" && (
            <p className="text-body font-medium">{t(($) => $.lark.install_success)}</p>
          )}

          {status === "error" && (
            <div className="space-y-2 text-center">
              <p className="text-body font-medium text-destructive">
                {(() => {
                  switch (errorReason) {
                    case "expired":
                      return t(($) => $.lark.install_error_expired);
                    case "access_denied":
                      return t(($) => $.lark.install_error_access_denied);
                    case "lark_protocol_error":
                      return t(($) => $.lark.install_error_protocol);
                    case "bot_info_failed":
                      return t(($) => $.lark.install_error_bot_info);
                    case "installation_conflict":
                      return t(($) => $.lark.install_error_conflict);
                    case "installer_bind_failed":
                      return t(($) => $.lark.install_error_installer_bind);
                    case "session_lost":
                      return t(($) => $.lark.install_error_session_lost);
                    case "forbidden":
                      return t(($) => $.lark.install_error_forbidden);
                    default:
                      return t(($) => $.lark.install_error_generic);
                  }
                })()}
              </p>
              {errorMessage && (
                <p className="text-micro text-muted-foreground break-all">
                  {errorMessage}
                </p>
              )}
            </div>
          )}
        </div>

        <DialogFooter>
          {status === "error" ? (
            <>
              <Button variant="outline" size="sm" onClick={onClose}>
                {t(($) => $.lark.install_close)}
              </Button>
              <Button size="sm" onClick={beginSession} disabled={beginning}>
                <RefreshCw className="h-3 w-3" />
                {t(($) => $.lark.install_retry)}
              </Button>
            </>
          ) : (
            <Button variant="outline" size="sm" onClick={onClose}>
              {t(($) => $.lark.install_close)}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
