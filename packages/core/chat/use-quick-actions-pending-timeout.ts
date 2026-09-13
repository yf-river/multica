// 清除"快捷操作待定"标记的定时 hook：标记过期且没有 chat:quick_actions 补充时，
// 直接抹掉查询缓存里的真实状态。
import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { chatKeys, QUICK_ACTIONS_PENDING_TIMEOUT_MS } from "./queries";
import type { ChatQuickActionsPendingState } from "../types/chat";

/**
 * 当没有 chat:quick_actions 补充在标记自己的截止前解决它时，从查询缓存清掉该会话的
 * `quickActionsPending` 标记（MUL-5149）。与组件本地定时器不同，这里抹的是**真实**的
 * 缓存状态——药丸转圈/骨架会停，而且之后的刷新——哪怕关掉重开聊天——也干净起步，
 * 而不是重读一个卡死的标记。
 *
 * 截止用标记里的 `expires_at`（绝对时间），而不是每次挂载重开一段新窗口：在悬浮聊天
 * 与聊天标签之间切换会卸载一个宿主、挂载另一个，按挂载计的定时器会每次重启整段等待，
 * 让一条丢事件把标记无限期搁浅。读取已存的截止意味着重挂载只等剩下的时间。最后清除
 * 是按 message 作用域的，绝不会抹掉新一轮的标记。
 *
 * 每个读取该标记的界面（聊天页 / 悬浮窗）挂载一次。
 * @param sessionId 会话 id（为空时不启动）。
 * @param pending 当前待定标记（为空时不启动）。
 */
export function useQuickActionsPendingTimeout(
  sessionId: string | null,
  pending: ChatQuickActionsPendingState | null,
): void {
  const qc = useQueryClient();
  const messageId = pending?.message_id ?? null;
  const expiresAt = pending?.expires_at ?? null;
  useEffect(() => {
    if (!sessionId || !messageId) return;
    const clear = () =>
      qc.setQueryData<ChatQuickActionsPendingState | null>(
        chatKeys.quickActionsPending(sessionId),
        // 只清"仍是这条消息"的标记，避免把更新的那轮标记误删。
        (current) =>
          current && current.message_id === messageId ? null : current,
      );
    // 距这条标记绝对截止的剩余时间；没有截止的旧标记回退到完整窗口。
    // 已过期的截止下一拍就清除。
    const remaining =
      expiresAt != null
        ? expiresAt - Date.now()
        : QUICK_ACTIONS_PENDING_TIMEOUT_MS;
    const timer = setTimeout(clear, Math.max(0, remaining));
    return () => clearTimeout(timer);
  }, [qc, sessionId, messageId, expiresAt]);
}
