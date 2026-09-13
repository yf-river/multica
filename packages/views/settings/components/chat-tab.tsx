// 本文件是「聊天」设置标签页：控制悬浮聊天窗口开关。

"use client";

import { Switch } from "@multica/ui/components/ui/switch";
import { useChatStore } from "@multica/core/chat";
import { toast } from "sonner";
import { useT } from "../../i18n";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";

/**
 * 聊天设置——「我的账户」下的独立标签页。目前只有一个悬浮窗口开关：
 * 关闭后悬浮按钮/浮层不再挂载，聊天只能从专属标签页进入；默认开启。
 * 该偏好是持久化的客户端设置（`floatingChatEnabled`），改完立即生效、无需请求服务端。
 */
export function ChatTab() {
  const { t } = useT("settings");
  const enabled = useChatStore((s) => s.floatingChatEnabled);
  const setEnabled = useChatStore((s) => s.setFloatingChatEnabled);

  return (
    <SettingsTab title={t(($) => $.page.tabs.chat)}>
      <SettingsSection title={t(($) => $.chat.floating_title)}>
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.chat.floating_label)}
            description={t(($) => $.chat.floating_hint)}
          >
          <Switch
            checked={enabled}
            onCheckedChange={(checked) => {
              setEnabled(checked);
              toast.success(t(($) => $.auto_save.toast_saved), {
                id: "settings-auto-save",
              });
            }}
            aria-label={t(($) => $.chat.floating_label)}
          />
          </SettingsRow>
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}
