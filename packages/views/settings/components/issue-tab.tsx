// 本文件是「Issue 设置」标签页：配置两种创建 Issue 模式下对话框工具栏上显示哪些字段。

"use client";

import { Switch } from "@multica/ui/components/ui/switch";
import {
  MANUAL_CREATE_FIELDS,
  QUICK_CREATE_FIELDS,
  useIssueCreateSettingsStore,
} from "@multica/core/issues/stores/issue-create-settings-store";
import { toast } from "sonner";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";

/**
 * 「Issue 设置」标签页——位于「我的账户」分组下。
 * 按两种创建 Issue 的模式（AI 快速创建 / 手动创建）各列一组开关，用来控制
 * 该模式下「新建 Issue」对话框工具栏上保留哪些字段。偏好按工作区保存在客户端：
 * 被关掉的字段不会真正消失——仍能从对话框的 ⋯（更多操作）里调出，且当它
 * 带着值时会自动重新浮现，所以隐藏字段永远不具破坏性、不会弄丢内容。
 */
export function IssueTab() {
  const { t } = useT("settings");
  const quickFields = useIssueCreateSettingsStore((s) => s.quickCreateFields);
  const setQuickVisible = useIssueCreateSettingsStore((s) => s.setQuickCreateFieldVisible);
  const manualFields = useIssueCreateSettingsStore((s) => s.manualCreateFields);
  const setManualVisible = useIssueCreateSettingsStore((s) => s.setManualCreateFieldVisible);

  const savedToast = () =>
    toast.success(t(($) => $.auto_save.toast_saved), { id: "settings-auto-save" });

  return (
    <SettingsTab
      title={t(($) => $.page.tabs.issue)}
      description={t(($) => $.issue.description)}
    >
      <SettingsSection
        title={t(($) => $.issue.quick_create_title)}
        description={t(($) => $.issue.quick_create_description)}
      >
        <SettingsCard>
          {QUICK_CREATE_FIELDS.map((field) => (
            <SettingsRow key={field} label={t(($) => $.issue.fields[field])}>
              <Switch
                checked={quickFields.includes(field)}
                onCheckedChange={(checked) => {
                  setQuickVisible(field, checked);
                  savedToast();
                }}
                aria-label={t(($) => $.issue.fields[field])}
              />
            </SettingsRow>
          ))}
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.issue.manual_create_title)}
        description={t(($) => $.issue.manual_create_description)}
      >
        <SettingsCard>
          {MANUAL_CREATE_FIELDS.map((field) => (
            <SettingsRow key={field} label={t(($) => $.issue.fields[field])}>
              <Switch
                checked={manualFields.includes(field)}
                onCheckedChange={(checked) => {
                  setManualVisible(field, checked);
                  savedToast();
                }}
                aria-label={t(($) => $.issue.fields[field])}
              />
            </SettingsRow>
          ))}
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}
