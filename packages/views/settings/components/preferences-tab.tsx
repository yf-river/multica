"use client";

import { useMemo } from "react";
import { toast } from "sonner";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Switch } from "@multica/ui/components/ui/switch";
import { useTheme } from "@multica/ui/components/common/theme-provider";
import { useAuthStore } from "@multica/core/auth";
import { useCommentComposerStore } from "@multica/core/issues/stores";
import { api } from "@multica/core/api";
import { browserTimezone, timezoneOptions } from "../../common/timezone-select";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";

export function PreferencesTab() {
  const { theme, setTheme } = useTheme();
  const { t } = useT("settings");

  const themeOptions = [
    { value: "light" as const, label: t(($) => $.preferences.theme.light) },
    { value: "dark" as const, label: t(($) => $.preferences.theme.dark) },
    { value: "system" as const, label: t(($) => $.preferences.theme.system) },
  ];

  return (
    <SettingsTab title={t(($) => $.page.tabs.preferences)}>
      <SettingsSection title={t(($) => $.preferences.general_title)}>
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.preferences.theme.title)}
            size="select"
          >
            <Select
              items={themeOptions}
              value={theme}
              onValueChange={(next) => {
                if (!next || next === theme) return;
                setTheme(next as (typeof themeOptions)[number]["value"]);
                toast.success(t(($) => $.auto_save.toast_saved), {
                  id: "settings-auto-save",
                });
              }}
            >
              <SelectTrigger
                size="sm"
                className="w-full"
                aria-label={t(($) => $.preferences.theme.title)}
              >
                <SelectValue>
                  {themeOptions.find((option) => option.value === theme)?.label}
                </SelectValue>
              </SelectTrigger>
              <SelectContent align="end">
                {themeOptions.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </SettingsRow>

          <TimezoneRow />

          <StickyCommentBarRow />
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}

function StickyCommentBarRow() {
  const { t } = useT("settings");
  const sticky = useCommentComposerStore((s) => s.sticky);
  const toggleSticky = useCommentComposerStore((s) => s.toggleSticky);

  return (
    <SettingsRow
      label={t(($) => $.preferences.sticky_comment_bar.title)}
      description={t(($) => $.preferences.sticky_comment_bar.hint)}
    >
      <Switch
        checked={sticky}
        onCheckedChange={() => {
          toggleSticky();
          toast.success(t(($) => $.auto_save.toast_saved), {
            id: "settings-auto-save",
          });
        }}
        aria-label={t(($) => $.preferences.sticky_comment_bar.title)}
      />
    </SettingsRow>
  );
}

// Base UI rejects "" as a SelectItem value, so route the "no preference"
// state through this sentinel and translate at the wire boundary.
const BROWSER_TZ_VALUE = "__browser__";

function TimezoneRow() {
  const { t } = useT("settings");
  const user = useAuthStore((s) => s.user);
  const setUser = useAuthStore((s) => s.setUser);
  const stored = user?.timezone ?? null;
  const browser = browserTimezone();
  const value = stored ?? BROWSER_TZ_VALUE;

  // Full IANA list (from timezoneOptions in common/timezone-select) so a
  // user needing a non-curated zone isn't stuck with ~18 common ones.
  // Memoized — timezoneOptions enumerates ~600 IANA zones per call.
  const options = useMemo(
    () => timezoneOptions(stored ?? browser),
    [stored, browser],
  );

  const handleChange = async (next: string) => {
    if (next === value) return;
    const payload = next === BROWSER_TZ_VALUE ? "" : next;
    try {
      const updated = await api.updateMe({ timezone: payload });
      setUser(updated);
      toast.success(t(($) => $.auto_save.toast_saved), {
        id: "settings-auto-save",
      });
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.preferences.timezone.sync_failed),
      );
    }
  };

  const formatTZLabel = (tz: string) => {
    if (tz === BROWSER_TZ_VALUE) {
      return `${browser}${t(($) => $.preferences.timezone.browser_suffix)}`;
    }
    return tz;
  };

  return (
    <SettingsRow
      label={t(($) => $.preferences.timezone.title)}
      description={t(($) => $.preferences.timezone.hint)}
      size="select-wide"
    >
      <Select
        items={[
          { value: BROWSER_TZ_VALUE, label: formatTZLabel(BROWSER_TZ_VALUE) },
          ...options.map((timezone) => ({
            value: timezone,
            label: formatTZLabel(timezone),
          })),
        ]}
        value={value}
        onValueChange={(next) => {
          if (next) void handleChange(next);
        }}
      >
        <SelectTrigger
          size="sm"
          className="w-full font-mono text-caption"
          aria-label={t(($) => $.preferences.timezone.title)}
        >
          <SelectValue>{formatTZLabel(value)}</SelectValue>
        </SelectTrigger>
        <SelectContent align="end" className="max-h-72">
          <SelectItem value={BROWSER_TZ_VALUE} className="font-mono text-caption">
            {formatTZLabel(BROWSER_TZ_VALUE)}
          </SelectItem>
          {options.map((timezone) => (
            <SelectItem key={timezone} value={timezone} className="font-mono text-caption">
              {formatTZLabel(timezone)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </SettingsRow>
  );
}
