"use client";

import { Fragment } from "react";
import { toast } from "sonner";
import {
  WORKSPACE_MENU_GROUPS,
  isWorkspaceMenuVisible,
  nextHiddenMenuKeys,
  useUpdateWorkspaceMenu,
} from "@multica/core/workspace";
import type { Workspace } from "@multica/core/types";
import { Switch } from "@multica/ui/components/ui/switch";
import { useT } from "../../i18n";
import {
  SettingsSection,
  SettingsCard,
  SettingsGroupLabel,
  SettingsRow,
  SettingsSaveState,
} from "./settings-layout";

/**
 * 「工作区菜单」设置：直接按侧边栏的同一份分组定义渲染，一行一个开关。
 * 分组标题与侧边栏一致；固定项（聊天、设置）显示为已开启且不可关闭。
 * 这里始终列出全部分组，包括当前整组被隐藏的，否则无法重新打开。
 */
export function WorkspaceMenuSettings({ workspace, canManage }: { workspace: Workspace; canManage: boolean }) {
  const { t } = useT("settings");
  const { t: nav } = useT("layout");
  const update = useUpdateWorkspaceMenu();
  const toggle = async (key: string, checked: boolean) => {
    try {
      await update.mutateAsync({ workspace, hidden: nextHiddenMenuKeys(workspace.settings, key, checked) });
      toast.success(t(($) => $.workspace.toast_saved));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t(($) => $.workspace.toast_save_failed));
    }
  };
  return <SettingsSection title={t(($) => $.workspace.menu_title)} action={<SettingsSaveState status={update.isPending ? "saving" : update.isError ? "error" : update.isSuccess ? "saved" : "idle"} savingLabel={t(($) => $.auto_save.saving)} savedLabel={t(($) => $.auto_save.saved)} errorLabel={t(($) => $.auto_save.failed)} />}>
    <SettingsCard>
      <div className="px-4 py-3 text-body text-muted-foreground">{t(($) => $.workspace.menu_hint)}</div>
      {update.isError && <p role="alert" className="px-4 py-3 text-body text-destructive">{update.error.message}</p>}
      {WORKSPACE_MENU_GROUPS.map((group) => {
        const groupLabel = group.label;
        const label = groupLabel
          ? nav(($) => groupLabel.section === "nav" ? $.nav[groupLabel.key] : $.sidebar[groupLabel.key])
          : null;
        return <Fragment key={group.id}>
          {label ? <SettingsGroupLabel>{label}</SettingsGroupLabel> : null}
          {group.entries.map((entry) => {
            const entryLabel = nav(($) => $.nav[entry.navKey]);
            const fixed = entry.fixed === true;
            return <SettingsRow
              key={entry.key}
              label={<label htmlFor={`workspace-menu-${entry.key}`} className={fixed ? undefined : "cursor-pointer"}>{entryLabel}</label>}
              description={fixed ? t(($) => $.workspace.menu_fixed) : undefined}
            >
              <Switch
                id={`workspace-menu-${entry.key}`}
                aria-label={entryLabel}
                checked={fixed || isWorkspaceMenuVisible(workspace.settings, entry.key)}
                disabled={fixed || !canManage || update.isPending}
                onCheckedChange={fixed ? undefined : (checked) => void toggle(entry.key, checked)}
              />
            </SettingsRow>;
          })}
        </Fragment>;
      })}
    </SettingsCard>
  </SettingsSection>;
}
