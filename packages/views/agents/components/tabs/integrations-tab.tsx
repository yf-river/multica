"use client";

// 本文件是 Agent 详情页的“集成”标签页：把 Lark / Slack / 钉钉 / 企微 / Telegram
// 这些外部渠道与 Agent 的“绑定入口”集中呈现。各平台卡片复用设置页的 BindButton，
// 本页只围绕它们补上标题、说明与未配置/敬请期待/仅成员可见等说明性外壳。

import { useQuery } from "@tanstack/react-query";
import type { Agent } from "@multica/core/types";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { larkInstallationsOptions } from "@multica/core/lark";
import { slackInstallationsOptions } from "@multica/core/slack";
import {
  dingtalkAgentGroupsOptions,
  dingtalkInstallationsOptions,
} from "@multica/core/dingtalk";
import { wecomInstallationsOptions } from "@multica/core/wecom";
import { telegramInstallationsOptions } from "@multica/core/telegram";
import { memberListOptions } from "@multica/core/workspace/queries";
import { LarkAgentBindButton } from "../../../settings/components/lark-tab";
import { LarkMark } from "../../../settings/components/lark-mark";
import { SlackAgentBindButton } from "../../../settings/components/slack-tab";
import { SlackMark } from "../../../settings/components/slack-mark";
import {
  DingTalkAgentBindButton,
  DingTalkBotGroups,
  DingTalkConnectionLabel,
  getDingTalkBotIdentity,
} from "../../../settings/components/dingtalk-tab";
import { DingTalkMark } from "../../../settings/components/dingtalk-mark";
import { WecomAgentBindButton } from "../../../settings/components/wecom-tab";
import { WecomMark } from "../../../settings/components/wecom-mark";
import { TelegramAgentBindButton } from "../../../settings/components/telegram-tab";
import { TelegramMark } from "../../../settings/components/telegram-mark";
import { useT } from "../../../i18n";

/**
 * Agent 详情页的“集成”标签页。呈现与检查器 “Integrations” 小节相同的外部渠道
 * 绑定入口（目前是 Lark 机器人）——未绑定时扫码绑定、已绑定时展示连接信息——
 * 但用 Tab 页充足的空间放下标题、描述以及侧栏紧凑区放不下的“未配置 / 敬请期待 /
 * 仅成员可见”等状态。
 *
 * 真正可操作的是共享的 `LarkAgentBindButton`——“扫码绑定 vs 已连接”的唯一事实源。
 * 本页只给它加上说明性的外壳，两个入口因此永远不会跑偏。
 */
export function IntegrationsTab({ agent }: { agent: Agent }) {
  const { t } = useT("agents");
  const { t: ts } = useT("settings");
  const wsId = useWorkspaceId();
  const user = useAuthStore((s) => s.user);

  // 这两个查询 LarkAgentBindButton 本来就会发（且按工作区作 key），这里再读一次是
  // 免费的——TanStack 按 key 去重。我们只需要推导出的布尔值来选按钮旁的文案，
  // 分支顺序与 Settings 里 LarkTab 保持一致。
  const { data: listing } = useQuery({
    ...larkInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const { data: slackListing } = useQuery({
    ...slackInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const { data: dingtalkListing } = useQuery({
    ...dingtalkInstallationsOptions(wsId),
  });
  const { data: wecomListing } = useQuery({
    ...wecomInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const { data: telegramListing } = useQuery({
    ...telegramInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: !!wsId,
  });

  const configured = listing?.configured === true;
  const installSupported = listing?.install_supported === true;
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const isWorkspaceAdmin =
    currentMember?.role === "owner" || currentMember?.role === "admin";
  const isAgentOwner =
    currentMember != null &&
    !!user?.id &&
    agent.owner_id != null &&
    agent.owner_id === user.id;
  // Lark 的绑定/管理授权给“Agent 拥有者 或 工作区拥有者/管理员”
  //（server/internal/handler/lark.go canManageAgent，MUL-4213）。
  // Slack 的安装/撤销路由仍只限工作区拥有者/管理员，所以它的门槛保持仅管理员——
  // 不能给一个后端必然 403 的 Agent 拥有者展示 Slack 操作入口。
  const canManageLark = isWorkspaceAdmin || isAgentOwner;
  const canManageSlack = isWorkspaceAdmin;
  const canManageWecom = isWorkspaceAdmin;
  const canManageTelegram = isWorkspaceAdmin;
  const hasActiveInstall =
    listing?.installations.some(
      (inst) => inst.agent_id === agent.id && inst.status === "active",
    ) ?? false;

  const slackConfigured = slackListing?.configured === true;
  const slackInstallSupported = slackListing?.install_supported === true;
  const slackHasActiveInstall =
    slackListing?.installations.some(
      (inst) => inst.agent_id === agent.id && inst.status === "active",
    ) ?? false;

  const dingtalkConfigured = dingtalkListing?.configured === true;
  const dingtalkInstallation = dingtalkListing?.installations.find(
    (inst) => inst.agent_id === agent.id && inst.status === "active",
  );
  const dingtalkHasActiveInstall = !!dingtalkInstallation;
  const {
    data: dingtalkGroupsListing,
    isLoading: dingtalkGroupsLoading,
    isError: dingtalkGroupsError,
    refetch: retryDingtalkGroups,
  } = useQuery({
    ...dingtalkAgentGroupsOptions(wsId, agent.id),
    enabled: !!wsId && dingtalkHasActiveInstall,
  });
  const dingtalkGroups = dingtalkGroupsListing?.groups ?? [];
  const dingtalkGroupDiscoverySupported =
    dingtalkGroupsListing?.group_discovery_supported === true;
  const showDingtalkGroupDiscovery =
    dingtalkGroupDiscoverySupported ||
    dingtalkGroupsLoading ||
    dingtalkGroupsError;
  const dingtalkBotIdentity = dingtalkInstallation
    ? dingtalkGroupsListing?.bot_identities?.[dingtalkInstallation.id] ??
      getDingTalkBotIdentity(dingtalkGroups, dingtalkInstallation.id)
    : undefined;
  // 钉钉与 Lark 同规则（按 Agent 管理）：目标 Agent 的拥有者或工作区拥有者/管理员
  // 可连接/断开它。
  const canManageDingtalk = isWorkspaceAdmin || isAgentOwner;

  const wecomConfigured = wecomListing?.configured === true;
  const wecomInstallSupported = wecomListing?.install_supported === true;
  const wecomHasActiveInstall =
    wecomListing?.installations.some(
      (inst) => inst.agent_id === agent.id && inst.status === "active",
    ) ?? false;

  const telegramConfigured = telegramListing?.configured === true;
  const telegramInstallSupported = telegramListing?.install_supported === true;
  const telegramHasActiveInstall =
    telegramListing?.installations.some(
      (inst) => inst.agent_id === agent.id && inst.status === "active",
    ) ?? false;

  // 维持既有的集成管理门槛：一个平台都不能管理的成员，看到的是只读提示而不是安装
  // 控件。Agent 级绑定的钉钉关系仍保持可见——能进这个 Agent 详情，说明已通过
  // 服务端对 Agent 的查看权限门槛。
  if (
    !canManageLark &&
    !canManageSlack &&
    !canManageDingtalk &&
    !canManageWecom &&
    !canManageTelegram
  ) {
    return (
      <div className="space-y-6">
        <p className="text-caption text-muted-foreground">
          {t(($) => $.tab_body.integrations.intro)}
        </p>
        {dingtalkInstallation ? (
          <section className="rounded-lg border">
            <div className="flex items-start gap-3 p-4">
              <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground">
                <DingTalkMark className="h-5 w-5" />
              </span>
              <div className="min-w-0 flex-1 space-y-1">
                <h3 className="text-body font-medium">
                  {ts(($) => $.dingtalk.section_title)}
                </h3>
                <p className="text-caption leading-relaxed text-muted-foreground">
                  {ts(($) => $.dingtalk.agent_page_description)}
                </p>
              </div>
            </div>
            <div className="border-t px-4 py-3">
              <DingTalkConnectionLabel
                botName={dingtalkBotIdentity?.bot_name ?? ""}
                botIdentityIssue={dingtalkBotIdentity?.bot_identity_issue ?? ""}
                showBotIdentity={
                  dingtalkGroupDiscoverySupported && !dingtalkGroupsLoading
                }
                showPermissionHelp={false}
              />
            </div>
            {showDingtalkGroupDiscovery && (
              <div className="border-t px-4 pb-4">
                <DingTalkBotGroups
                  workspaceId={wsId}
                  agentId={agent.id}
                  installationId={dingtalkInstallation.id}
                  groups={dingtalkGroups}
                  inactiveCount={dingtalkGroupsListing?.inactive_group_counts?.[dingtalkInstallation.id] ?? 0}
                  isLoading={dingtalkGroupsLoading}
                  isError={dingtalkGroupsError}
                  onRetry={() => void retryDingtalkGroups()}
                />
              </div>
            )}
          </section>
        ) : (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.tab_body.integrations.members_note)}
          </p>
        )}
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <p className="text-caption text-muted-foreground">
        {t(($) => $.tab_body.integrations.intro)}
      </p>

      <section className="rounded-lg border">
        <div className="flex items-start gap-3 p-4">
          <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground">
            <LarkMark className="h-4 w-4" />
          </span>
          <div className="min-w-0 flex-1 space-y-1">
            <h3 className="text-body font-medium">{ts(($) => $.lark.section_title)}</h3>
            <p className="text-caption leading-relaxed text-muted-foreground">
              {ts(($) => $.lark.page_description)}
            </p>
          </div>
        </div>
        <div className="border-t px-4 py-3">
          {!configured ? (
            // 这个部署没有持久密钥。本 Tab 只在功能已配置时才被挂载，所以这是
            // 罕见的“安装存在后密钥被移除”竞态。
            <p className="text-caption text-muted-foreground">
              {ts(($) => $.lark.not_enabled_title)}
            </p>
          ) : !installSupported && !hasActiveInstall ? (
            // 密钥在，但本构建没接入设备码传输——新扫码会在“轮询后取 bot 信息”
            // 一步失败，所以显示“敬请期待”而不是一个注定坏掉的入口。
            // 已绑定的 Agent 豁免：install_supported 只管“新安装”，已绑定的状态
            // 仍要正常渲染（server/internal/handler/lark.go）。
            <div className="space-y-1">
              <p className="text-caption font-medium">{ts(($) => $.lark.preview_title)}</p>
              <p className="text-caption text-muted-foreground">
                {ts(($) => $.lark.preview_description)}
              </p>
            </div>
          ) : (
            // Agent 拥有者或工作区拥有者/管理员，且要么传输受支持、要么已有机器人：
            // 共享按钮会渲染“扫码绑定”入口或“已在 Lark 管理”徽章。它按
            // agentOwnerId + 角色自我鉴权。
            <LarkAgentBindButton
              agentId={agent.id}
              agentName={agent.name}
              agentOwnerId={agent.owner_id}
            />
          )}
        </div>
      </section>

      <section className="rounded-lg border">
        <div className="flex items-start gap-3 p-4">
          <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground">
            <SlackMark className="h-4 w-4" />
          </span>
          <div className="min-w-0 flex-1 space-y-1">
            <h3 className="text-body font-medium">{ts(($) => $.slack.section_title)}</h3>
            <p className="text-caption leading-relaxed text-muted-foreground">
              {ts(($) => $.slack.page_description)}
            </p>
          </div>
        </div>
        <div className="border-t px-4 py-3">
          {!canManageSlack ? (
            // Slack 安装/撤销仍限工作区拥有者/管理员，所以非管理员的 Agent 拥有者
            // 在这里只能看到只读提示（不同于上方 Lark）。复用共享的 members 提示。
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.integrations.members_note)}
            </p>
          ) : !slackConfigured ? (
            <p className="text-caption text-muted-foreground">
              {ts(($) => $.slack.not_enabled_title)}
            </p>
          ) : !slackInstallSupported && !slackHasActiveInstall ? (
            // 密钥在但缺 OAuth 客户端凭据，新点 “Connect Slack” 会 503。显示
            // “敬请期待”而不是坏入口；已绑定的 Agent 仍正常渲染。
            <div className="space-y-1">
              <p className="text-caption font-medium">{ts(($) => $.slack.preview_title)}</p>
              <p className="text-caption text-muted-foreground">
                {ts(($) => $.slack.preview_description)}
              </p>
            </div>
          ) : (
            <SlackAgentBindButton agentId={agent.id} agentName={agent.name} />
          )}
        </div>
      </section>

      <section className="rounded-lg border">
        <div className="flex items-start gap-3 p-4">
          <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground">
            <DingTalkMark className="h-5 w-5" />
          </span>
          <div className="min-w-0 flex-1 space-y-1">
            <h3 className="text-body font-medium">{ts(($) => $.dingtalk.section_title)}</h3>
            <p className="text-caption leading-relaxed text-muted-foreground">
              {ts(($) => $.dingtalk.agent_page_description)}
            </p>
          </div>
        </div>
        <div className="border-t px-4 py-3">
          {!canManageDingtalk ? (
            // 无法管理这个 Agent 的查看者，仍能以只读方式看到它连着的钉钉机器人身份
            // 与发现的群。
            dingtalkInstallation ? (
              <DingTalkConnectionLabel
                botName={dingtalkBotIdentity?.bot_name ?? ""}
                botIdentityIssue={dingtalkBotIdentity?.bot_identity_issue ?? ""}
                showBotIdentity={
                  dingtalkGroupDiscoverySupported && !dingtalkGroupsLoading
                }
                showPermissionHelp={false}
              />
            ) : (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.tab_body.integrations.members_note)}
              </p>
            )
          ) : !dingtalkConfigured ? (
            <p className="text-caption text-muted-foreground">
              {ts(($) => $.dingtalk.not_enabled_title)}
            </p>
          ) : (
            <DingTalkAgentBindButton
              agentId={agent.id}
              agentName={agent.name}
              agentOwnerId={agent.owner_id}
              botName={
                dingtalkGroupsLoading || !dingtalkGroupDiscoverySupported
                  ? undefined
                  : (dingtalkBotIdentity?.bot_name ?? "")
              }
              botIdentityIssue={
                dingtalkGroupsLoading || !dingtalkGroupDiscoverySupported
                  ? undefined
                  : (dingtalkBotIdentity?.bot_identity_issue ?? "")
              }
            />
          )}
        </div>
        {dingtalkInstallation && showDingtalkGroupDiscovery && (
          <div className="border-t px-4 pb-4">
            <DingTalkBotGroups
              workspaceId={wsId}
              agentId={agent.id}
              installationId={dingtalkInstallation.id}
              groups={dingtalkGroups}
              inactiveCount={dingtalkGroupsListing?.inactive_group_counts?.[dingtalkInstallation.id] ?? 0}
              isLoading={dingtalkGroupsLoading}
              isError={dingtalkGroupsError}
              onRetry={() => void retryDingtalkGroups()}
            />
          </div>
        )}
      </section>

      <section className="rounded-lg border">
        <div className="flex items-start gap-3 p-4">
          <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground">
            <WecomMark className="h-4 w-4" />
          </span>
          <div className="min-w-0 flex-1 space-y-1">
            <h3 className="text-body font-medium">{ts(($) => $.wecom.section_title)}</h3>
            <p className="text-caption leading-relaxed text-muted-foreground">
              {ts(($) => $.wecom.page_description)}
            </p>
          </div>
        </div>
        <div className="border-t px-4 py-3">
          {!canManageWecom ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.integrations.members_note)}
            </p>
          ) : !wecomConfigured ? (
            <p className="text-caption text-muted-foreground">
              {ts(($) => $.wecom.not_enabled_title)}
            </p>
          ) : !wecomInstallSupported && !wecomHasActiveInstall ? (
            <div className="space-y-1">
              <p className="text-caption font-medium">{ts(($) => $.wecom.preview_title)}</p>
              <p className="text-caption text-muted-foreground">
                {ts(($) => $.wecom.preview_description)}
              </p>
            </div>
          ) : (
            <WecomAgentBindButton agentId={agent.id} agentName={agent.name} />
          )}
        </div>
      </section>

      <section className="rounded-lg border">
        <div className="flex items-start gap-3 p-4">
          <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md border bg-muted/40 text-muted-foreground">
            <TelegramMark className="h-4 w-4" />
          </span>
          <div className="min-w-0 flex-1 space-y-1">
            <h3 className="text-body font-medium">{ts(($) => $.telegram.section_title)}</h3>
            <p className="text-caption leading-relaxed text-muted-foreground">
              {ts(($) => $.telegram.page_description)}
            </p>
          </div>
        </div>
        <div className="border-t px-4 py-3">
          {!canManageTelegram ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.tab_body.integrations.members_note)}
            </p>
          ) : !telegramConfigured ? (
            <p className="text-caption text-muted-foreground">
              {ts(($) => $.telegram.not_enabled_title)}
            </p>
          ) : !telegramInstallSupported && !telegramHasActiveInstall ? (
            <div className="space-y-1">
              <p className="text-caption font-medium">{ts(($) => $.telegram.preview_title)}</p>
              <p className="text-caption text-muted-foreground">
                {ts(($) => $.telegram.preview_description)}
              </p>
            </div>
          ) : (
            <TelegramAgentBindButton agentId={agent.id} agentName={agent.name} />
          )}
        </div>
      </section>
    </div>
  );
}
