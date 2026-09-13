// 聊天草稿恢复的生命周期 hook（#5219）：当一次发送失败、或一次可恢复的"取消任务"
// 发生时，把用户还没发出去的提示词原样送还给输入框。聊天页与悬浮窗口共用，
// 保证"提示词绝不会丢"这条状态机只有一份实现。
import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { chatDraftRestoresOptions } from "@multica/core/chat/queries";
import { useConsumeChatDraftRestore } from "@multica/core/chat/mutations";
import { useChatStore } from "@multica/core/chat";
import { removeChatMessageFromCaches } from "@multica/core/realtime";
import type { Attachment } from "@multica/core/types";

/**
 * 要求输入框采纳的一条草稿。有两个来源：
 * - 一次失败的发送，恢复用户自己的文本（没有 `serverRestoreId`）；
 * - 一次持久化的"推迟的取消"恢复（#5219），它在服务端有一行记录。
 */
export interface RestoreDraftRequest {
  id: string;
  content: string;
  attachments?: Attachment[];
  /**
   * 这条恢复要落进的草稿槽位。输入框只在用户正在看该会话时才采纳它，
   * 这样恢复永远不会落到错误的聊天里。
   */
  sessionId?: string;
  /** 持久化恢复才有：草稿被采纳后要删除的那行服务端记录。 */
  serverRestoreId?: string;
}

/**
 * 管理单个输入框的持久化草稿恢复生命周期（#5219）。聊天页控制器与悬浮聊天窗口
 * ——两个入口必须共享同一套逻辑，因为下面这个状态机是"被取消的提示词"和
 * "彻底丢失"之间唯一的防线。
 *
 * 两种恢复喂给同一个输入框，它们的失败模式不同：
 *
 * - 持久化（"推迟取消"的行，#5219）：文本在服务端手里，所以一次没有被采纳的
 *   提供可以直接丢弃、之后重新拉取。
 * - 无服务端副本（失败的发送，或同步应答的取消）：发送时已经清掉了持久化草稿，
 *   所以这个 hook 的队列是用户文本唯一的副本。因此它从创建那一刻起就按会话
 *   持久化（pendingSendRestores），直到输入框报告它已被采纳——绝不放进那个
 *   共享槽位，因为槽位是组件状态，会随卸载一起消失。
 *
 * 单个 `restoreDraftRequest` 槽位只是"当前输入框现在就能采纳的那一条"的视图：
 * 它绝不持有别的会话的请求，所以一条面向"用户可能再也不回来"的会话的恢复，
 * 不会把用户正在看的会话饿死。
 *
 * 交接保证"至多一次"且不丢失：
 *
 * - 恢复被提供给输入框，输入框要么采纳它（草稿为空时），要么放着不动
 *   （用户正写了一半）。被跳过的恢复保持 pending——ChatInput 会随草稿变化
 *   反复评估，草稿一清空就采纳它。它永远不被标记为完成、不被消费，
 *   服务端行（或队列条目）也一直保留。
 * - 只有被采纳的恢复才会被记账——记在一份 *持久化* 的台账里，且在消费请求
 *   发出之前。因此一次丢失的消费（离线、应用关闭、重试耗尽）不可能在用户已经
 *   发出提示词之后，再让这条提示词被恢复第二次；让提供"至多一次"的是这份台账，
 *   而不是服务端行。
 * - 任何比台账活得久的行都会被对账：每次拉取都会再发一次消费，直到行消失，
 *   并且只有当服务端确认后，台账条目才被丢弃。
 *
 * 所有权。恢复属于 *用户*，而不是某台设备：端点是按创建者授权的，所以在有人
 * 消费之前，该用户的每个客户端都能看到这行。`enabled` 决定哪些输入框可以采纳：
 * 客户端只会把恢复认领进用户真正看得见的输入框。搞错的代价不是一份重复草稿——
 * 而是静默的窃取：一个后台的输入框采纳（并消费）了用户正在手头设备上等的那条
 * 提示词，等用户看时那行已经没了。因此隐藏的输入框绝不能参与。
 *
 * 然而，同一个会话上两个 *可见* 的输入框可以都采纳它。这是有意为之的产品决策，
 * 不是实现的意外：
 *
 * - 会发生什么：两者都把提示词写进自己设备本地的草稿。消费的 DELETE 是幂等的，
 *   所以先到服务端的那一个拿走那行，另一个的调用变成空操作。什么都不会丢；
 *   用户会在两个屏幕上看到恢复出来的提示词，最多把它发两遍。
 * - 为什么接受：两个输入框都可见，意味着用户正盯着其中一个。恢复出来的草稿
 *   不是已发送的消息——它躺在输入框里直到用户按发送——所以最坏情况是用户在
 *   另一台开着的设备上看到自己的提示词，顺手在那里关掉。
 * - 为什么不修：消除它需要以客户端身份为键、在本地草稿写入 *之前* 拿到的
 *   服务端 claim/lease。那会把整个状态机依赖的顺序颠倒（先采纳、后消费），
 *   并重新打开它本想堵住的洞：一个赢了 claim 却随即死掉——离线、关闭、崩溃——
 *   的客户端拿走了那行，却从未把提示词放进任何地方，文本就此彻底消失。
 *   拿"可恢复的重复"去换"不可恢复的丢失"是这笔赌注亏本的一边，
 *   而用带过期的 lease 去限制损失，又会把重复原样买回来。
 */
export function useChatDraftRestore(activeSessionId: string | null, enabled = true) {
  const qc = useQueryClient();
  const [restoreDraftRequest, setRestoreDraftRequest] = useState<RestoreDraftRequest | null>(null);

  const appliedRestoreIds = useChatStore((s) => s.appliedDraftRestoreIds);
  const markDraftRestoreApplied = useChatStore((s) => s.markDraftRestoreApplied);
  const forgetDraftRestoreApplied = useChatStore((s) => s.forgetDraftRestoreApplied);
  const pendingSendRestores = useChatStore((s) => s.pendingSendRestores);
  const enqueuePendingSendRestore = useChatStore((s) => s.enqueuePendingSendRestore);
  const dequeuePendingSendRestore = useChatStore((s) => s.dequeuePendingSendRestore);
  const consumeDraftRestore = useConsumeChatDraftRestore();

  // 这个查询在输入框挂载与网络重连时会重新拉取，且发起方在 `chat:cancel_finalized`
  // 到达时通过实时处理器使其失效——所以一个跨广播期间离线的客户端仍能在这里找回
  // 提示词。被禁用的输入框连拉取都不做：没有提供，也就没有认领。
  const { data: draftRestoresData } = useQuery({
    ...chatDraftRestoresOptions(activeSessionId ?? ""),
    enabled: enabled && !!activeSessionId,
  });
  const restores = draftRestoresData?.restores;

  // 进行中的消费集合：防止一次重渲染或第二次拉取对同一行发出重复请求。
  // 在 settle 时释放——包括失败时也释放，这正是让下一次拉取能对账一条
  // 重试全部用尽的消费的原因。
  const reconcilingRef = useRef<Set<string>>(new Set());
  const consume = useCallback(
    (sessionId: string, restoreId: string) => {
      reconcilingRef.current.add(restoreId);
      consumeDraftRestore.mutate(
        { sessionId, restoreId },
        {
          // 行已经没了：台账条目没有需要继续保护的"重复消费"风险了。
          onSuccess: () => forgetDraftRestoreApplied(restoreId),
          onSettled: () => reconcilingRef.current.delete(restoreId),
        },
      );
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps -- mutation 引用稳定，不入依赖
    [forgetDraftRestoreApplied],
  );

  // 对账那些消费从未落地的行。从拉取到的列表里（而不是只在采纳时刻）触发，
  // 正是堵住"消费丢失"漏洞的办法：直到行消失前，每次挂载、重拉取或重连
  // 都会再次消费它。
  useEffect(() => {
    if (!enabled || !activeSessionId || !restores) return;
    for (const r of restores) {
      if (r.chat_session_id !== activeSessionId) continue;
      if (!appliedRestoreIds.includes(r.id)) continue;
      if (reconcilingRef.current.has(r.id)) continue;
      consume(activeSessionId, r.id);
    }
  }, [restores, activeSessionId, appliedRestoreIds, consume, enabled]);

  useEffect(() => {
    if (!enabled) {
      // 输入框不在了（窗口关闭、标签页退到后台）。释放槽位，而不是攥着一份
      // 没人看得见的提供：持久化的请求会被重新拉取，无服务端副本的还在它
      // 持久化的队列里。什么都没被采纳，也就什么都没丢，用户此刻正盯着的
      // 那个输入框——本机或另一台设备上——可以来认领它。
      if (restoreDraftRequest) setRestoreDraftRequest(null);
      return;
    }
    if (!activeSessionId) return;
    if (restoreDraftRequest) {
      // 单个槽位只有一条规则：它只持有"屏幕上这个会话"的请求。ChatInput 把
      // 它采纳不了的请求当成一次 WAIT（等待），所以一份面向"用户可能再也不回"的
      // 会话的请求，会饿死用户当前正在看的会话的每一次交接。
      //
      // 释放它怎么都不亏：持久化的请求有服务端行，下次拉取会再次提供；
      // 无服务端副本的还在 pendingSendRestores 里、按自己的会话排队，
      // 用户回到那个会话时会再次被提供。
      const { sessionId } = restoreDraftRequest;
      if (!sessionId || sessionId === activeSessionId) return;
      setRestoreDraftRequest(null);
      return;
    }
    // 无服务端副本的恢复优先。两种都会在采纳前反复提供，但这份是用户自己的文本、
    // 别处没有任何副本，所以它最该尽早出现在用户面前。它待在队列里，直到
    // ChatInput 报告交接完成——仅仅被提供过的请求仍可能丢失。
    const queued = pendingSendRestores[activeSessionId]?.[0];
    if (queued) {
      setRestoreDraftRequest({
        id: queued.id,
        content: queued.content,
        attachments: queued.attachments,
        sessionId: activeSessionId,
      });
      return;
    }
    const restore = restores?.find(
      (r) => r.chat_session_id === activeSessionId && !appliedRestoreIds.includes(r.id),
    );
    if (!restore) return;
    // 如果这个客户端错过了 chat:cancel_finalized 事件，被删掉的气泡可能还躺在
    // 消息缓存里——恢复前先把它清掉。
    removeChatMessageFromCaches(qc, activeSessionId, restore.id);
    setRestoreDraftRequest({
      id: restore.id,
      content: restore.content ?? "",
      attachments: restore.attachments,
      sessionId: activeSessionId,
      serverRestoreId: restore.id,
    });
  }, [
    restores,
    activeSessionId,
    restoreDraftRequest,
    appliedRestoreIds,
    pendingSendRestores,
    qc,
    enabled,
  ]);

  /**
   * 一条没有服务端副本的恢复——失败的发送，或同步应答的取消。它直接进
   * 持久化的按会话队列，绝不进共享槽位：它的文本别处不存在，所以必须能扛过
   * 卸载，也绝不能为了一个用户没在看的会话霸占槽位。上面的 effect 会在用户
   * 在看那个会话时把它交给输入框。
   */
  const enqueueLocalRestore = useCallback(
    (restore: { id: string; content: string; attachments?: Attachment[]; sessionId: string }) => {
      enqueuePendingSendRestore(restore);
    },
    [enqueuePendingSendRestore],
  );

  /**
   * 输入框已经把草稿写进去了。这是唯一的终点转换：持久化的恢复先记账再消费；
   * 无服务端副本的，现在文本已躺在（持久化的）草稿里，把队列条目丢掉即可。
   * 一次被跳过的交接永远不会走到这里。
   */
  const handleRestoreDraftApplied = useCallback(() => {
    if (!restoreDraftRequest) return;
    const { id, serverRestoreId, sessionId } = restoreDraftRequest;
    if (serverRestoreId && sessionId) {
      markDraftRestoreApplied(serverRestoreId);
      consume(sessionId, serverRestoreId);
    } else if (sessionId) {
      dequeuePendingSendRestore(sessionId, id);
    }
    setRestoreDraftRequest(null);
  }, [restoreDraftRequest, markDraftRestoreApplied, consume, dequeuePendingSendRestore]);

  return { restoreDraftRequest, enqueueLocalRestore, handleRestoreDraftApplied };
}
