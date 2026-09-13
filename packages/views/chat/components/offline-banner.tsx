// 离线提示条：当活动 agent 不可达（离线或不稳定）时，在聊天输入框上方显示提示。
"use client";

import { AlertCircle, WifiOff } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import type { AgentAvailability } from "@multica/core/agents";
import { CHAT_COLUMN, CHAT_GUTTER } from "./chat-column";
import { useT } from "../../i18n";

/** OfflineBanner 组件的 props。 */
interface Props {
  /** 横幅文案中显示的 agent 名称。 */
  agentName?: string;
  /**
   * 已解析的在线状态。传 `undefined`（或 "loading"）会隐藏横幅——
   * 我们只展示已知的离线 / 不稳定状态，从不展示猜测性的文案。
   */
  availability: AgentAvailability | undefined;
}

// 当活动 agent 不可达时，渲染在聊天输入框上方的行内提示。
// 在 `online`、`undefined` 或在线状态加载中时隐藏——
// 用户得到静默的默认行为，只有当他们即将发送的消息存在真实影响时才看到文案。
/**
 * 离线提示条：活动 agent 不可达时显示提示。
 * @param agentName - agent 名称。
 * @param availability - 在线状态。
 */
export function OfflineBanner({ agentName, availability }: Props) {
  const { t } = useT("chat");
  if (availability !== "offline" && availability !== "unstable") return null;

  const name = agentName?.trim() || t(($) => $.offline_banner.fallback_name);
  if (availability === "unstable") {
    return (
      <div className={cn(CHAT_GUTTER, "mb-1.5")}>
        <div className={cn(CHAT_COLUMN, "flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-caption bg-amber-50 dark:bg-amber-950/40 text-amber-900 dark:text-amber-200 ring-1 ring-amber-200/60 dark:ring-amber-900/40")}>
          <AlertCircle className="size-3.5 shrink-0" />
          <span className="truncate">
            {t(($) => $.offline_banner.unstable, { name })}
          </span>
        </div>
      </div>
    );
  }
  return (
    <div className={cn(CHAT_GUTTER, "mb-1.5")}>
      <div className={cn(CHAT_COLUMN, "flex items-center gap-1.5 rounded-md px-2.5 py-1.5 text-caption bg-muted text-muted-foreground ring-1 ring-border")}>
        <WifiOff className="size-3.5 shrink-0" />
        <span className="truncate">
          {t(($) => $.offline_banner.offline, { name })}
        </span>
      </div>
    </div>
  );
}
