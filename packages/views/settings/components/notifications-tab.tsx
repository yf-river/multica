// 本文件是「通知设置」标签页：逐事件类型开关收件箱通知（分配给我/状态变更/评论/提及
// 等），并单独开关系统通知；Web 端还额外提供浏览器通知授权行。

"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { notificationPreferenceOptions } from "@multica/core/notification-preferences/queries";
import { useUpdateNotificationPreferences } from "@multica/core/notification-preferences/mutations";
import type { NotificationGroupKey, NotificationPreferences } from "@multica/core/types";
import { Switch } from "@multica/ui/components/ui/switch";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { BrowserNotificationSetting } from "./browser-notification-setting";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";

// 逐事件开关列表里渲染的收件箱事件分组。`system_notifications`（系统通知）虽然也是
// 同名偏好键，但单独放在下面自己的分区里，不在此列表。
const INBOX_GROUP_KEYS = [
  "assignments",
  "status_changes",
  "comments",
  "mentions",
  "updates",
  "agent_activity",
] as const;
/** 收件箱事件分组键（由上方 INBOX_GROUP_KEYS 推导）。 */
type InboxGroupKey = (typeof INBOX_GROUP_KEYS)[number];

/**
 * 「通知设置」标签页：用服务端下发的偏好逐项渲染开关；每切换一个开关，就把整份
 * 偏好对象整体提交保存（保留其余未动过的键）。打开某事件类型时把键值设为 "all"
 * 后从对象里删掉该键——"all" 是服务端默认值，带着它提交只会让 payload 变脏。
 */
export function NotificationsTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const { data } = useQuery(notificationPreferenceOptions(wsId));
  const mutation = useUpdateNotificationPreferences();

  const preferences = data?.preferences ?? {};

  const handleToggle = (key: NotificationGroupKey, enabled: boolean) => {
    const updated: NotificationPreferences = {
      ...preferences,
      [key]: enabled ? "all" : "muted",
    };
    // 打开（回到默认 "all"）的键直接从对象里删掉，让提交内容保持干净、不夹带默认值。
    if (enabled) {
      delete updated[key];
    }
    mutation.mutate(updated, {
      onSuccess: () =>
        toast.success(t(($) => $.auto_save.toast_saved), {
          id: "settings-auto-save",
        }),
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.notifications.toast_failed),
        ),
    });
  };

  const systemEnabled = preferences.system_notifications !== "muted";

  return (
    <SettingsTab title={t(($) => $.page.tabs.notifications)}>
      <SettingsSection
        title={t(($) => $.notifications.title)}
        description={t(($) => $.notifications.description)}
      >
        <SettingsCard>
            {INBOX_GROUP_KEYS.map((key: InboxGroupKey) => {
              const enabled = preferences[key] !== "muted";
              return (
                <SettingsRow
                  key={key}
                  label={t(($) => $.notifications.groups[key].label)}
                  description={t(($) => $.notifications.groups[key].description)}
                >
                  <Switch
                    checked={enabled}
                    aria-label={t(($) => $.notifications.groups[key].label)}
                    onCheckedChange={(checked) => handleToggle(key, checked)}
                  />
                </SettingsRow>
              );
            })}
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.notifications.system.title)}
        description={t(($) => $.notifications.system.description)}
      >
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.notifications.system.label)}
            description={t(($) => $.notifications.system.hint)}
          >
              <Switch
                checked={systemEnabled}
                aria-label={t(($) => $.notifications.system.label)}
                onCheckedChange={(checked) => handleToggle("system_notifications", checked)}
              />
          </SettingsRow>
        </SettingsCard>

        {/* 仅 Web 端：浏览器权限提示只有网页环境需要。桌面端（走系统原生通知）
            或不支持 Notification API 的环境下该组件自行不渲染。 */}
        <BrowserNotificationSetting />
      </SettingsSection>
    </SettingsTab>
  );
}
