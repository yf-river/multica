// Agent 访问权限被撤销提示条：在聊天输入框上方提示用户无法再调用该 agent。
"use client";

import { Lock } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { CHAT_COLUMN, CHAT_GUTTER } from "./chat-column";
import { useT } from "../../i18n";

// ArchivedAgentBanner / OfflineBanner / NoAgentBanner 的同类组件，位于聊天输入框
// 上方的同一个横幅槽位。当“打开的会话”对应的 agent 不再允许当前用户调用时显示——
// 例如 agent 被改成个人（personal）、所有权被转移，或允许列表移除了该用户（MUL-6380）。
//
// 聊天记录仍然可读，因为“读取”由更宽松的 view 谓词控制；只有“运行”由 invoke 谓词控制，
// 服务端会在每次发送时重新校验（MUL-4525）。如果没有这个横幅，输入框看起来还是可用的，
// 用户打完消息后才会通过一个 403 得知这一变化。
/**
 * Agent 访问权限被撤销提示条。
 * @param agentName - agent 的显示名称（可选）。
 */
export function AgentAccessRevokedBanner({ agentName }: { agentName?: string }) {
  const { t } = useT("chat");
  const name = agentName?.trim() || t(($) => $.offline_banner.fallback_name);
  return (
    <div className={cn(CHAT_GUTTER, "mb-1.5")}>
      <div className={cn(CHAT_COLUMN, "flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-caption bg-muted text-muted-foreground ring-1 ring-border")}>
        <Lock className="size-3.5 shrink-0" />
        <span className="truncate">
          {t(($) => $.agent_access_revoked_banner, { name })}
        </span>
      </div>
    </div>
  );
}
