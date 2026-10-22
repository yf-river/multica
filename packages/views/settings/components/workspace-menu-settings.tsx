"use client";

import { toast } from "sonner";
import { CONFIGURABLE_MENU_KEYS, isWorkspaceMenuVisible, useUpdateWorkspaceMenu } from "@multica/core/workspace";
import { WORKSPACE_PAGES } from "@multica/core/paths";
import type { Workspace } from "@multica/core/types";
import { Switch } from "@multica/ui/components/ui/switch";
import { useT } from "../../i18n";
import { SettingsSection, SettingsCard, SettingsRow, SettingsSaveState } from "./settings-layout";

export function WorkspaceMenuSettings({ workspace, canManage }: { workspace: Workspace; canManage: boolean }) {
  const { t } = useT("settings");
  const { t: nav } = useT("layout");
  const update = useUpdateWorkspaceMenu();
  return <SettingsSection title={t(($) => $.workspace.menu_title)} action={<SettingsSaveState status={update.isPending ? "saving" : update.isError ? "error" : update.isSuccess ? "saved" : "idle"} savingLabel={t(($) => $.auto_save.saving)} savedLabel={t(($) => $.auto_save.saved)} errorLabel={t(($) => $.auto_save.failed)} />}>
    <SettingsCard>
      <div className="px-4 py-3 text-body text-muted-foreground">{t(($) => $.workspace.menu_hint)}</div>
      {update.isError && <p role="alert" className="px-4 py-3 text-body text-destructive">{update.error.message}</p>}
      {CONFIGURABLE_MENU_KEYS.map((key) => {
        const label = nav(($) => $.nav[WORKSPACE_PAGES[key].navKey]);
        return <SettingsRow key={key} label={<label htmlFor={`workspace-menu-${key}`} className="cursor-pointer">{label}</label>}>
          <Switch id={`workspace-menu-${key}`} aria-label={label} checked={isWorkspaceMenuVisible(workspace.settings, key)} disabled={!canManage || update.isPending}
            onCheckedChange={async (checked) => {
              const hidden = CONFIGURABLE_MENU_KEYS.filter((item) => item === key ? !checked : !isWorkspaceMenuVisible(workspace.settings, item));
              try {
                await update.mutateAsync({ workspace, hidden });
                toast.success(t(($) => $.workspace.toast_saved));
              } catch (error) {
                toast.error(error instanceof Error ? error.message : t(($) => $.workspace.toast_save_failed));
              }
            }} />
        </SettingsRow>;
      })}
    </SettingsCard>
  </SettingsSection>;
}
