// 本文件是「集成」设置标签页：第三方平台连接的总入口。GitHub 有自己的一级标签页
// （见 github-tab.tsx），其余平台——目前有飞书、Composio、Slack、Telegram、自托管
// Git 服务（Forgejo/Gitea/GitLab）与企业微信智能机器人，Linear 等后续接入——都
// 在这里各占一个分区。每个集成的标题和接入流程由各自组件自持，本页只做「宿主」。

"use client";

import { useQuery } from "@tanstack/react-query";
import { LarkTab } from "./lark-tab";
import { ComposioTab } from "./composio-tab";
import { SlackTab } from "./slack-tab";
import { DingTalkTab } from "./dingtalk-tab";
import { VCSTab } from "./vcs-tab";
import { WecomTab } from "./wecom-tab";
import { TelegramTab } from "./telegram-tab";
import { ApiError } from "@multica/core/api";
import { composioToolkitsOptions } from "@multica/core/composio";
import { useConfigStore, useFeatureEnabled } from "@multica/core/config";
import { COMPOSIO_MCP_APPS_FLAG } from "@multica/core/feature-flags";
import { useT } from "../../i18n";
import { SettingsSection, SettingsTab } from "./settings-layout";
import { IntegrationChannelIcon } from "./integration-channel-icon";

// 状态判定说明：新增集成时给每个平台加一个分区即可，无需改动本文件的导航结构——
// 各分区标题自带品牌图标与 i18n 文案，分区内部完全自治。
/**
 * 「集成」标签页：按顺序渲染各第三方平台的分区（飞书 / Composio / Slack /
 * 钉钉 / 自托管 Git / 企业微信 / Telegram）。Composio 分区只在功能开关打开、
 * 且该实例确已配置 Composio 时出现；自托管 Git 分区只对自托管部署开放。
 */
export function IntegrationsTab() {
  const { t } = useT("settings");

  const composioEnabled = useFeatureEnabled(COMPOSIO_MCP_APPS_FLAG, false);
  const composioToolkits = useQuery({
    ...composioToolkitsOptions(),
    enabled: composioEnabled,
  });
  // 503 表示服务端还没配好 Composio 的密钥等；此时隐藏整个分区，而不是给
  // 一个「运营商未配置」的报错面板。
  const composioUnconfigured =
    composioToolkits.error instanceof ApiError && composioToolkits.error.status === 503;

  // 自托管专属集成：托管云把该字段从 /api/config 里省略、上报为 false，于是整段
  // 分区（含标题）在托管云隐藏，而不是对普通用户展示一条「缺运营商配置」的提示。
  const vcsAvailable = useConfigStore((s) => s.vcsIntegrationAvailable);

  return (
    <SettingsTab title={t(($) => $.page.tabs.integrations)}>
      <SettingsSection
        title={
          <span className="flex items-center gap-2">
            <IntegrationChannelIcon channel="lark" />
            {t(($) => $.lark.section_title)}
          </span>
        }
        description={t(($) => $.lark.page_description)}
      >
        <LarkTab />
      </SettingsSection>
      {composioEnabled && !composioUnconfigured && (
        <SettingsSection title={t(($) => $.composio.section_title)}>
          <ComposioTab />
        </SettingsSection>
      )}
      <SettingsSection
        title={
          <span className="flex items-center gap-2">
            <IntegrationChannelIcon channel="slack" />
            {t(($) => $.slack.section_title)}
          </span>
        }
        description={t(($) => $.slack.page_description)}
      >
        <SlackTab />
      </SettingsSection>
      <SettingsSection
        title={
          <span className="flex items-center gap-2">
            <IntegrationChannelIcon channel="dingtalk" />
            {t(($) => $.dingtalk.section_title)}
          </span>
        }
        description={t(($) => $.dingtalk.page_description)}
      >
        <DingTalkTab />
      </SettingsSection>
      {vcsAvailable && (
        <SettingsSection title={t(($) => $.vcs.section_title)}>
          <VCSTab />
        </SettingsSection>
      )}
      <SettingsSection
        title={
          <span className="flex items-center gap-2">
            <IntegrationChannelIcon channel="wecom" />
            {t(($) => $.wecom.section_title)}
          </span>
        }
        description={t(($) => $.wecom.page_description)}
      >
        <WecomTab />
      </SettingsSection>
      <SettingsSection
        title={
          <span className="flex items-center gap-2">
            <IntegrationChannelIcon channel="telegram" />
            {t(($) => $.telegram.section_title)}
          </span>
        }
        description={t(($) => $.telegram.page_description)}
      >
        <TelegramTab />
      </SettingsSection>
    </SettingsTab>
  );
}
