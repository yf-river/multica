"use client";

import { useEffect, useState } from "react";
import {
  getWebNotificationPermission,
  isWebNotificationSupported,
  requestWebNotificationPermission,
  type WebNotificationPermission,
} from "@multica/core/platform";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { useT } from "../../i18n";

/**
 * Web-only control for the browser permission that native notification banners
 * require. Renders nothing when the Notification API is unavailable (SSR,
 * older browsers).
 *
 * Capability and permission are read from `window`, so the first paint defers
 * to a post-mount effect to keep SSR and client markup identical (no hydration
 * mismatch).
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

  // Pre-mount or where the API is missing → nothing to manage.
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
    <Card>
      <CardContent>
        <div className="flex items-center justify-between gap-4">
          <div className="space-y-0.5 pr-4">
            <p className="text-sm font-medium">
              {t(($) => $.notifications.browser.label)}
            </p>
            <p className="text-xs text-muted-foreground">{statusHint}</p>
          </div>
          {permission === "default" && (
            <Button size="sm" variant="outline" onClick={handleEnable}>
              {t(($) => $.notifications.browser.enable)}
            </Button>
          )}
          {permission === "granted" && (
            <span className="shrink-0 text-xs font-medium text-muted-foreground">
              {t(($) => $.notifications.browser.enabled_badge)}
            </span>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
