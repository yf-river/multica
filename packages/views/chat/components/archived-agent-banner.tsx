// 已归档 agent 提示条：当会话对应的 agent 被归档（退役）时提示用户该对话只能查看。
"use client";

import { Archive } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { CHAT_COLUMN, CHAT_GUTTER } from "./chat-column";
import { useT } from "../../i18n";

// OfflineBanner / NoAgentBanner 的同类组件，占据聊天输入框上方同一个横幅槽位。
// 当打开会话的 agent 已被归档（退役）时显示：上方的输入框被禁用，
// 这个横幅说明该对话是只读历史——agent 无法再回复。
//
// 布局来自共享的 CHAT_GUTTER / CHAT_COLUMN 对，与其同类组件一致，
// 所以横幅的边缘在任意界面宽度下都能和输入框对齐。
/**
 * 已归档 agent 提示条。
 * @param agentName - agent 的显示名称（可选）。
 */
export function ArchivedAgentBanner({ agentName }: { agentName?: string }) {
  const { t } = useT("chat");
  const name = agentName?.trim() || t(($) => $.offline_banner.fallback_name);
  return (
    <div className={cn(CHAT_GUTTER, "mb-1.5")}>
      <div className={cn(CHAT_COLUMN, "flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-caption bg-muted text-muted-foreground ring-1 ring-border")}>
        <Archive className="size-3.5 shrink-0" />
        <span className="truncate">
          {t(($) => $.archived_agent_banner, { name })}
        </span>
      </div>
    </div>
  );
}
