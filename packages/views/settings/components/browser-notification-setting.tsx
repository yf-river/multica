// 本文件提供「浏览器通知权限」的设置行：针对 Web 端，让用户在页面里直接申请/查看
// 浏览器通知授权。仅当浏览器支持 Notification API（且当前挂在 Web 环境）时才渲染，
// 桌面端走系统原生通知，根本用不到这一段。

"use client";

import { useEffect, useState } from "react";
import {
  getWebNotificationPermission,
  isWebNotificationSupported,
  requestWebNotificationPermission,
  type WebNotificationPermission,
} from "@multica/core/platform";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";
import { SettingsCard, SettingsRow } from "./settings-layout";

/**
 * 浏览器通知授权行。当浏览器不支持 Notification API（如 SSR 阶段、老浏览器）
 * 时什么都不渲染。
 *
 * 能力和授权状态都要从 `window` 读取，因此首帧不能直接读——先挂载再用 effect
 * 补读，保证服务端渲染与客户端首帧标记一致，避免 hydration 不匹配。
 * @param props - 无入参；展示文案和交互全部由 i18n 与浏览器状态驱动。
 */
export function BrowserNotificationSetting() {
  const { t } = useT("settings");
  const [mounted, setMounted] = useState(false);
  const [permission, setPermission] =
    useState<WebNotificationPermission>("default");

  useEffect(() => {
    setMounted(true);
    setPermission(getWebNotificationPermission());
  }, []);

  // 尚未完成挂载，或当前环境根本没有 Notification API → 没有可管理的东西，直接不渲染。
  if (!mounted || !isWebNotificationSupported()) return null;

  const handleEnable = async () => {
    setPermission(await requestWebNotificationPermission());
  };

  const statusHint =
    permission === "granted"
      ? t(($) => $.notifications.browser.granted)
      : permission === "denied"
        ? t(($) => $.notifications.browser.denied)
        : t(($) => $.notifications.browser.hint);

  return (
    <SettingsCard>
      <SettingsRow
        label={t(($) => $.notifications.browser.label)}
        description={statusHint}
      >
          {permission === "default" && (
            <Button size="sm" variant="outline" onClick={handleEnable}>
              {t(($) => $.notifications.browser.enable)}
            </Button>
          )}
          {permission === "granted" && (
            <span className="shrink-0 text-caption font-medium text-muted-foreground">
              {t(($) => $.notifications.browser.enabled_badge)}
            </span>
          )}
      </SettingsRow>
    </SettingsCard>
  );
}
