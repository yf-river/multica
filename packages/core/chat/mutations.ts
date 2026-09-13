// Chat 会话管理的全部 mutation hook：消费草稿恢复、钉/取消钉 Agent、创建/改名/
// 设项目/设钉/归档/硬删会话、标记已读、重生成快捷操作。多数做乐观更新 + 出错回滚，
// 用 chat:session_updated 等 WS 事件同步其它标签页/设备。
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { chatKeys, sortChatSessions, QUICK_ACTIONS_PENDING_TIMEOUT_MS } from "./queries";
import { createLogger } from "../logger";
import type {
  ChatSession,
  ChatPinnedAgent,
  ChatDraftRestoresResponse,
  ChatQuickActionsPendingState,
} from "../types";

const logger = createLogger("chat.mut");

/**
 * 输入框应用过一份"延迟取消草稿恢复"（#5219）之后消费掉它。
 * 接口幂等，所以消费两次——或消费一条上次尝试已删掉的行——是安全的，
 * 这也正是这个 mutation 能重试的原因（应用层全局 `retry: false`）。
 *
 * 一次丢失的消费绝不能把一条用户已发过的提示词重新恢复出来，所以"已应用"的判定
 * 不由本次请求携带：调用方在发出请求前把它记进持久化的账本
 * （`markDraftRestoreApplied`），任何活得比账本条目久的行通过再次消费来对账。
 * 本 mutation 只需要让那次对账快速收敛。
 * @returns React Query mutation 对象（消费一条恢复记录）。
 */
export function useConsumeChatDraftRestore() {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: ({ sessionId, restoreId }: { sessionId: string; restoreId: string }) =>
      api.consumeChatDraftRestore(sessionId, restoreId),
    retry: 3,
    retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 8000),
    onMutate: ({ sessionId, restoreId }) => {
      // 乐观：先从缓存里拿掉该恢复条目，避免再次被弹出。
      qc.setQueryData<ChatDraftRestoresResponse>(
        chatKeys.draftRestores(sessionId),
        (old) =>
          old
            ? { ...old, restores: old.restores.filter((r) => r.id !== restoreId) }
            : old,
      );
    },
    onError: (err, { sessionId, restoreId }) => {
      // 重试耗尽。草稿是安全的（已在输入框里），账本也让该行不会被再次弹出；
      // 下一次挂载会做对账。
      logger.warn("consumeChatDraftRestore.error", { sessionId, restoreId, err });
    },
  });
}

/** 把 Agent 钉到快捷 Agent 栏（乐观追加到末尾）。 */
export function usePinChatAgent() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (agentId: string) => api.pinChatAgent(agentId),
    onMutate: async (agentId) => {
      await qc.cancelQueries({ queryKey: chatKeys.pinnedAgents(wsId) });
      const prev = qc.getQueryData<ChatPinnedAgent[]>(chatKeys.pinnedAgents(wsId));
      // 已钉过则原样保留；否则以当前最大 position + 1 追加在末尾。
      qc.setQueryData<ChatPinnedAgent[]>(chatKeys.pinnedAgents(wsId), (old) => {
        if (old?.some((p) => p.agent_id === agentId)) return old;
        const maxPos = old?.reduce((m, p) => Math.max(m, p.position), 0) ?? 0;
        return [...(old ?? []), { agent_id: agentId, position: maxPos + 1 }];
      });
      return { prev };
    },
    onError: (err, agentId, ctx) => {
      logger.error("pinChatAgent.error.rollback", { agentId, err });
      if (ctx?.prev) qc.setQueryData(chatKeys.pinnedAgents(wsId), ctx.prev);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.pinnedAgents(wsId) });
    },
  });
}

/** 把 Agent 从快捷 Agent 栏取消钉住（乐观移除）。 */
export function useUnpinChatAgent() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (agentId: string) => api.unpinChatAgent(agentId),
    onMutate: async (agentId) => {
      await qc.cancelQueries({ queryKey: chatKeys.pinnedAgents(wsId) });
      const prev = qc.getQueryData<ChatPinnedAgent[]>(chatKeys.pinnedAgents(wsId));
      qc.setQueryData<ChatPinnedAgent[]>(chatKeys.pinnedAgents(wsId), (old) =>
        old?.filter((p) => p.agent_id !== agentId),
      );
      return { prev };
    },
    onError: (err, agentId, ctx) => {
      logger.error("unpinChatAgent.error.rollback", { agentId, err });
      if (ctx?.prev) qc.setQueryData(chatKeys.pinnedAgents(wsId), ctx.prev);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.pinnedAgents(wsId) });
    },
  });
}

/** 创建一个新的 chat 会话（指定 Agent，可带标题与项目上下文）。 */
export function useCreateChatSession() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (data: { agent_id: string; title?: string; project_id?: string | null }) => {
      logger.info("createChatSession.start", {
        agent_id: data.agent_id,
        project_id: data.project_id,
        titleLength: data.title?.length ?? 0,
      });
      return api.createChatSession(data);
    },
    onSuccess: (session) => {
      logger.info("createChatSession.success", { sessionId: session.id, agentId: session.agent_id });
    },
    onError: (err) => {
      logger.error("createChatSession.error", err);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
    },
  });
}

/**
 * 在服务端清掉会话的未读状态。乐观地把缓存列表里该会话的 has_unread 翻成 false，
 * 让 FAB 徽标立刻掉落。服务端会广播 chat:session_read，其它设备也跟着同步。
 */
export function useMarkChatSessionRead() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (sessionId: string) => {
      logger.info("markChatSessionRead.start", { sessionId });
      return api.markChatSessionRead(sessionId);
    },
    onMutate: async (sessionId) => {
      await qc.cancelQueries({ queryKey: chatKeys.sessions(wsId) });

      const prevSessions = qc.getQueryData<ChatSession[]>(chatKeys.sessions(wsId));

      const clear = (old?: ChatSession[]) =>
        old?.map((s) => (s.id === sessionId ? { ...s, has_unread: false, unread_count: 0 } : s));
      qc.setQueryData<ChatSession[]>(chatKeys.sessions(wsId), clear);

      return { prevSessions };
    },
    onError: (err, sessionId, ctx) => {
      logger.error("markChatSessionRead.error.rollback", { sessionId, err });
      if (ctx?.prevSessions) qc.setQueryData(chatKeys.sessions(wsId), ctx.prevSessions);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
    },
  });
}

/**
 * 重命名一个 chat 会话。乐观地在缓存列表里换掉标题，让下拉立刻显示新标签；
 * 出错回滚。配对的 `chat:session_updated` WS 事件让其它标签页/设备同步——
 * 见 use-realtime-sync.ts。
 */
export function useUpdateChatSession() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (data: { sessionId: string; title: string }) => {
      logger.info("updateChatSession.start", {
        sessionId: data.sessionId,
        titleLength: data.title.length,
      });
      return api.updateChatSession(data.sessionId, { title: data.title });
    },
    onMutate: async ({ sessionId, title }) => {
      await qc.cancelQueries({ queryKey: chatKeys.sessions(wsId) });

      const prevSessions = qc.getQueryData<ChatSession[]>(chatKeys.sessions(wsId));

      const patch = (old?: ChatSession[]) =>
        old?.map((s) => (s.id === sessionId ? { ...s, title } : s));
      qc.setQueryData<ChatSession[]>(chatKeys.sessions(wsId), patch);

      return { prevSessions };
    },
    onError: (err, vars, ctx) => {
      logger.error("updateChatSession.error.rollback", { sessionId: vars.sessionId, err });
      if (ctx?.prevSessions) qc.setQueryData(chatKeys.sessions(wsId), ctx.prevSessions);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
    },
  });
}

/**
 * 改变既有 chat 的项目上下文（不重建会话）。乐观补丁让上下文胶囊在服务端校验
 * 软项目引用期间保持原位；失败则恢复旧行。
 */
export function useSetChatSessionProject() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (data: { sessionId: string; projectId: string | null }) => {
      logger.info("setChatSessionProject.start", data);
      return api.updateChatSession(data.sessionId, { project_id: data.projectId });
    },
    onMutate: async ({ sessionId, projectId }) => {
      await qc.cancelQueries({ queryKey: chatKeys.sessions(wsId) });

      const prevSessions = qc.getQueryData<ChatSession[]>(chatKeys.sessions(wsId));
      const patch = (old?: ChatSession[]) =>
        old?.map((session) =>
          session.id === sessionId ? { ...session, project_id: projectId } : session,
        );
      qc.setQueryData<ChatSession[]>(chatKeys.sessions(wsId), patch);

      return { prevSessions };
    },
    onError: (err, vars, ctx) => {
      logger.error("setChatSessionProject.error.rollback", {
        sessionId: vars.sessionId,
        err,
      });
      if (ctx?.prevSessions) qc.setQueryData(chatKeys.sessions(wsId), ctx.prevSessions);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
    },
  });
}

/**
 * 钉住或取消钉住一个 chat。乐观翻转 `pinned` 并重排缓存列表（钉住优先、再按活动度）
 * 让该行立刻跳到/离开顶部；出错回滚。配对的 `chat:session_updated` WS 事件把新钉住
 * 状态带给其它标签页/设备——见 use-realtime-sync.ts。
 */
export function useSetChatSessionPinned() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (data: { sessionId: string; pinned: boolean }) => {
      logger.info("setChatSessionPinned.start", data);
      return api.setChatSessionPinned(data.sessionId, data.pinned);
    },
    onMutate: async ({ sessionId, pinned }) => {
      await qc.cancelQueries({ queryKey: chatKeys.sessions(wsId) });

      const prevSessions = qc.getQueryData<ChatSession[]>(chatKeys.sessions(wsId));

      const patch = (old?: ChatSession[]) =>
        old &&
        sortChatSessions(old.map((s) => (s.id === sessionId ? { ...s, pinned } : s)));
      qc.setQueryData<ChatSession[]>(chatKeys.sessions(wsId), patch);

      return { prevSessions };
    },
    onError: (err, vars, ctx) => {
      logger.error("setChatSessionPinned.error.rollback", { sessionId: vars.sessionId, err });
      if (ctx?.prevSessions) qc.setQueryData(chatKeys.sessions(wsId), ctx.prevSessions);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
    },
  });
}

/**
 * 归档或取消归档一个 chat 会话。乐观翻转缓存列表里的 `status`，让该行立刻在活跃
 * 列表与 "Archived" 视图之间移动（两者都在本地按 status 过滤）；出错回滚。同时抬升
 * `updated_at`，让该行落到哪个视图都会按活动度重新排序。配对的 `chat:session_updated`
 * WS 事件把新状态带给其它标签页/设备——见 use-realtime-sync.ts。
 *
 * 归档还会在本地把该行未读清零，让每个徽标（FAB、侧边栏 Chat 标签、聊天窗头部）
 * 在该行移入 Archived 视图的同一帧内把它去掉。后端本来就强制已归档行的未读为 0
 * （见 ListAllChatSessionsByCreator / MUL-4360）；这是乐观的一半，避免重取落地前
 * 头/边栏还在计一个刚归档的会话的窗口。取消归档这里不恢复计数——真实未读状态随
 * 服务端重取回来（last_read_at 未动）。
 */
export function useSetChatSessionArchived() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (data: { sessionId: string; archived: boolean }) => {
      logger.info("setChatSessionArchived.start", data);
      return api.setChatSessionArchived(data.sessionId, data.archived);
    },
    onMutate: async ({ sessionId, archived }) => {
      await qc.cancelQueries({ queryKey: chatKeys.sessions(wsId) });

      const prevSessions = qc.getQueryData<ChatSession[]>(chatKeys.sessions(wsId));

      const nowIso = new Date().toISOString();
      const patch = (old?: ChatSession[]) =>
        old &&
        sortChatSessions(
          old.map((s) =>
            s.id === sessionId
              ? {
                  ...s,
                  status: archived ? "archived" : "active",
                  updated_at: nowIso,
                  ...(archived ? { unread_count: 0, has_unread: false } : {}),
                }
              : s,
          ),
        );
      qc.setQueryData<ChatSession[]>(chatKeys.sessions(wsId), patch);

      return { prevSessions };
    },
    onError: (err, vars, ctx) => {
      logger.error("setChatSessionArchived.error.rollback", { sessionId: vars.sessionId, err });
      if (ctx?.prevSessions) qc.setQueryData(chatKeys.sessions(wsId), ctx.prevSessions);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
    },
  });
}

/**
 * 硬删除一个 chat 会话。乐观从会话列表移除该行，让下拉立刻更新；出错回滚。
 * 配对的 `chat:session_deleted` WS 事件让其它标签页/设备同步——见 use-realtime-sync.ts。
 */
export function useDeleteChatSession() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (sessionId: string) => {
      logger.info("deleteChatSession.start", { sessionId });
      return api.deleteChatSession(sessionId);
    },
    onMutate: async (sessionId) => {
      await qc.cancelQueries({ queryKey: chatKeys.sessions(wsId) });

      const prevSessions = qc.getQueryData<ChatSession[]>(chatKeys.sessions(wsId));

      const drop = (old?: ChatSession[]) => old?.filter((s) => s.id !== sessionId);
      qc.setQueryData<ChatSession[]>(chatKeys.sessions(wsId), drop);

      logger.debug("deleteChatSession.optimistic", { sessionId });
      return { prevSessions };
    },
    onError: (err, sessionId, ctx) => {
      logger.error("deleteChatSession.error.rollback", { sessionId, err });
      if (ctx?.prevSessions) qc.setQueryData(chatKeys.sessions(wsId), ctx.prevSessions);
    },
    onSettled: (_data, _err, sessionId) => {
      logger.debug("deleteChatSession.settled", { sessionId });
      qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
    },
  });
}

/**
 * 为某会话最新一条助手消息刷新快捷操作建议（MUL-5149）。乐观地升起该轮的 pending
 * 标记——药丸转惰、刷新图标转起来——失败时回滚。刷新后的药丸经 chat:quick_actions
 * realtime 事件到达，事件会清除标记（applyChatQuickActionsToCache）。从不重试：
 * 刷新是显式、消耗配额的用户动作。
 */
export function useRegenerateChatQuickActions() {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: ({ sessionId, messageId }: { sessionId: string; messageId: string }) =>
      api.regenerateChatQuickActions(sessionId, messageId),
    onMutate: ({ sessionId, messageId }) => {
      const previous = qc.getQueryData<ChatQuickActionsPendingState | null>(
        chatKeys.quickActionsPending(sessionId),
      );
      // 服务端确认 messageId 确实是最新一轮（否则 409 → onError 回滚），因此标记的
      // message_id 保证与之后解决它的 chat:quick_actions 匹配——无需 ack 对账。
      // task_id 在这里未知、也不参与解决（applyChatQuickActionsToCache 按 message_id 匹配）。
      qc.setQueryData<ChatQuickActionsPendingState | null>(
        chatKeys.quickActionsPending(sessionId),
        {
          message_id: messageId,
          task_id: "",
          expires_at: Date.now() + QUICK_ACTIONS_PENDING_TIMEOUT_MS,
        },
      );
      return { previous, sessionId };
    },
    onError: (error, _vars, ctx) => {
      logger.error("regenerateChatQuickActions.error", { error });
      if (ctx) {
        // 失败：恢复之前的标记状态（原为 null 则清掉）。
        qc.setQueryData<ChatQuickActionsPendingState | null>(
          chatKeys.quickActionsPending(ctx.sessionId),
          ctx.previous ?? null,
        );
      }
    },
  });
}
