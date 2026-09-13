// 无 agent 提示条：当工作区没有任何当前用户可聊的 agent 时显示，说明原因。
"use client";

import { Bot } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { CHAT_COLUMN, CHAT_GUTTER } from "./chat-column";
import { useT } from "../../i18n";

// ChatInput 的同类组件，与 OfflineBanner 共用同一个横幅槽位。
// 当工作区没有任何当前用户可聊的 agent 时显示——上方的输入框被禁用，
// 这个横幅解释原因。
//
// 有意做成纯文案：横幅不链接到 /agents，因为这里真正可行动的信息是
// “你需要一个 agent”，而不是目标地址——在用户思考到一半时把人踢出聊天、
// 赶到设置页，比直接说明前置条件更打扰人。想操作的用户会自己去 Agents。
//
// 布局来自共享的 CHAT_GUTTER / CHAT_COLUMN 对，与 OfflineBanner、ChatInput 一致，
// 所以横幅的边缘在任意界面宽度下都能和输入框对齐。
/**
 * 无 agent 提示条。
 */
export function NoAgentBanner() {
  const { t } = useT("chat");
  return (
    <div className={cn(CHAT_GUTTER, "mb-1.5")}>
      <div className={cn(CHAT_COLUMN, "flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-caption bg-muted text-muted-foreground ring-1 ring-border")}>
        <Bot className="size-3.5 shrink-0" />
        <span className="truncate">{t(($) => $.no_agent_banner)}</span>
      </div>
    </div>
  );
}
