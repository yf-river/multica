// 快捷操作"补充生成失败"的异步提示 hook：HTTP 请求已被接受、但 daemon 的
// 重新生成从未产出建议时（失败经 WS 事件异步到达），弹一条 toast 并清掉信号。
import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  chatKeys,
  chatQuickActionsFailureOptions,
} from "@multica/core/chat/queries";
import type { ChatQuickActionsFailureState } from "@multica/core/types";
import { toast } from "sonner";
import { useT } from "../../i18n";

/**
 * 当某个会话显式的快捷操作刷新在异步阶段失败时弹出 toast——HTTP 调用本身被接受了，
 * 但 daemon 的重新生成从未产出建议，所以那一轮的 pills 没有变化（MUL-5149）。
 * 失败经 `chat:quick_actions`（failed=true）到达，`applyChatQuickActionsToCache`
 * 把它变成一个一次性的纯客户端信号；这个 hook 消费并清掉它。同步的 HTTP 失败路径
 * （409 忙碌、网络错误）已经在 QuickActions.handleRegenerate 里弹过 toast 了，
 * 所以这里只覆盖异步那一半——否则一次失败的刷新看起来会和一次恰好没改建议的
 * 成功刷新完全一样。
 *
 * toast id 以失败 nonce 为键，可以在两个聊天界面（悬浮窗口 + 聊天标签页）同时挂载、
 * 对着同一个信号各跑一份本 hook 时折叠重复。每个读取该标记的界面各挂载一次即可。
 */
export function useQuickActionsFailureToast(sessionId: string | null): void {
  const qc = useQueryClient();
  const { t } = useT("chat");
  const { data: failure = null } = useQuery(
    chatQuickActionsFailureOptions(sessionId ?? ""),
  );

  useEffect(() => {
    if (!sessionId || !failure) return;
    toast.error(t(($) => $.message_list.quick_actions_regenerate_failed), {
      id: `qa-refresh-fail:${sessionId}:${failure.at}`,
    });
    // 只清掉这一份确切信号，这样同一 tick 内更新的失败不会被误吞。
    qc.setQueryData<ChatQuickActionsFailureState | null>(
      chatKeys.quickActionsFailure(sessionId),
      (current) => (current && current.at === failure.at ? null : current),
    );
  }, [qc, t, sessionId, failure]);
}
