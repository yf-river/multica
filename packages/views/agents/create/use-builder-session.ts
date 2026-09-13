"use client";

// 本文件是 AI-builder 会话生命周期的 hook：创建隐藏 carrier 会话、在上面收发消息、
// 重绑运行环境、按请求销毁。会话以 sessionId 识别（页面从自己 URL 读），不轮询——
// 全局实时同步已按会话 id 使聊天消息/待跑任务缓存失效。

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  agentBuilderSessionKeys,
  decodeBuilderInput,
  pickBuilderRestore,
  type BuilderRestore,
} from "@multica/core/agents";
import { api, ApiError } from "@multica/core/api";
import {
  chatKeys,
  chatMessagesOptions,
  pendingChatTaskOptions,
} from "@multica/core/chat/queries";
import { upsertChatMessageToCaches } from "@multica/core/chat/message-cache";
import { removeChatMessageFromCaches } from "@multica/core/realtime";
import { useWorkspaceId } from "@multica/core/hooks";
import type { ChatMessage } from "@multica/core/types";
import { useAppForeground } from "../../common/use-app-foreground";
import { useChatDraftRestore } from "../../chat/components/use-chat-draft-restore";
import { useT } from "../../i18n";

const EMPTY_CHAT_MESSAGES: ChatMessage[] = [];

/**
 * 一条 AI-builder 会话的生命周期：创建隐藏的 carrier 会话、在其上收发消息、
 * 重绑其运行环境、按请求销毁。
 *
 * 会话以 `sessionId` 识别，页面从自己的 URL 读取。正是这一点让它扛得住刷新、
 * 前进/后退、重开标签页——本 hook 不持有任何会话级状态，所以卸载它（点侧栏、
 * 换路由）不会动到服务端那条会话。销毁现在是一个显式动作，绝不是导航的副作用。
 *
 * 没有轮询：全局实时同步已像主聊天窗那样（主聊天从不轮询），按 session id 在
 * `chat:message`、`chat:done` 与任务生命周期事件上失效 `chatKeys.messages` /
 * `chatKeys.pendingTask`。
 */
export function useBuilderSession(options: {
  /** 要操作的那条会话；尚未开始时为空串。 */
  sessionId: string;
  /** 把输入框文本按当前草稿编码成 builder 线上格式。 */
  encodeInput: (text: string) => string;
}) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { sessionId } = options;

  // 任何会改变“这条会话是否入列”或“它的行怎么写”的事，都得同步到草稿列表——
  // 那列表渲染在别的屏上，那些屏要么已挂载、要么会从缓存挂载。
  const invalidateDraftList = () =>
    qc.invalidateQueries({ queryKey: agentBuilderSessionKeys.list(wsId) });

  const [starting, setStarting] = useState(false);
  const [closing, setClosing] = useState(false);
  const [switchingRuntime, setSwitchingRuntime] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [restoreDraft, setRestoreDraft] = useState<BuilderRestore | null>(null);

  // builder 聊天是真 chat_session，所以“取消一个已开始但还没跑”的会话，其
  // 空/非空判定与主聊天完全一致地推迟（#5219）：stop 的响应不携带 restore_to_input，
  // 提示词稍后以一条持久的 chat_draft_restore 行抵达。没有这个 hook，工作室的输入框
  // 就永远看不到它。两个来源互斥——同步取消立即应答，持久的那条等 daemon ack 后才
  // 落地——所以谁存在，就把谁交给输入框。
  //
  // 挂在 app 前台判断上：这是专用路由，挂载即意味着页面在屏上；但后台标签页绝不能
  // 去拉取/应用/消费一个用户正在别处等待的恢复。等标签页重新聚焦，下一次拉取会
  // 接上。
  const appForeground = useAppForeground();
  const {
    restoreDraftRequest: durableRestoreRequest,
    handleRestoreDraftApplied: handleDurableRestoreApplied,
  } = useChatDraftRestore(sessionId || null, appForeground);
  const restoreDraftRequest = useMemo(
    () => pickBuilderRestore(restoreDraft, durableRestoreRequest),
    [restoreDraft, durableRestoreRequest],
  );

  const messagesQuery = useQuery(chatMessagesOptions(sessionId));
  const pendingQuery = useQuery(pendingChatTaskOptions(sessionId));
  const messages = messagesQuery.data ?? EMPTY_CHAT_MESSAGES;
  const pending = !!pendingQuery.data?.task_id;
  // URL 指名的会话没了——在这里被丢弃、在别的标签页被删、或链接早已比它长寿。
  // 从“拉消息”读出来而不是“不在草稿列表里”，因为会话要有一条消息才入列：刚创建
  // 一秒的会话合理地缺席于列表，绝不能误当成已死。
  const missing =
    messagesQuery.error instanceof ApiError &&
    messagesQuery.error.status === 404;

  /** 创建会话。返回其 id 供调用方寻址。 */
  const start = async (
    runtimeId: string,
    model: string,
  ): Promise<string | null> => {
    setStarting(true);
    setError(null);
    try {
      const session = await api.createAgentBuilderSession({
        runtime_id: runtimeId,
        model: model.trim() || undefined,
      });
      if (!session.session_id) {
        throw new Error(t(($) => $.creation_studio.builder.start_failed));
      }
      return session.session_id;
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : t(($) => $.creation_studio.builder.start_failed),
      );
      return null;
    } finally {
      setStarting(false);
    }
  };

  /**
   * 销毁会话。服务端拒绝时返回 false，让调用方留在原地显示错误，而不是从一条
   * 仍然存在的会话上导航走。
   */
  const destroy = async (): Promise<boolean> => {
    if (!sessionId) return true;
    setClosing(true);
    setError(null);
    try {
      await api.deleteChatSession(sessionId);
      qc.removeQueries({ queryKey: chatKeys.messages(sessionId) });
      // 发送同时会播种两份缓存，所以这里两份都得清。只清分页那份的话，被删会话
      // 的 transcript 会永远新鲜地挂着——它 staleTime: Infinity，没有任何读者会
      // 自行纠正。
      qc.removeQueries({ queryKey: chatKeys.messagesPage(sessionId) });
      qc.removeQueries({ queryKey: chatKeys.pendingTask(sessionId) });
      void invalidateDraftList();
      return true;
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : t(($) => $.creation_studio.builder.stop_failed),
      );
      return false;
    } finally {
      setClosing(false);
    }
  };

  /**
   * Agent 建成后让会话退役。
   *
   * 归档而非删除：会话是这个 Agent 如何被设计出来的记录，归档的会话只读、且从
   * 草稿列表里消失——正是期望的终态。删除它白白毁掉这份记录：carrier 空闲时不花
   * 任何成本，用量按任务计费、归档会话永远跑不了任务。
   *
   * 刻意“尽力而为”：这一刻 Agent 已提交，失败绝不能把一次成功创建变成可重试错误。
   */
  const archiveAfterCreate = async () => {
    if (!sessionId) return;
    try {
      await api.setChatSessionArchived(sessionId, true);
      void invalidateDraftList();
    } catch {
      // 草稿仍留在列表里；用户可显式丢弃它。
    }
  };

  // 先在服务端重绑会话的执行运行环境，再让草稿反映新选择。先改草稿正是 MUL-5163
  // 的翻车方式：选择器显示运行环境 B，之后每条消息却仍跑在会话创建时那台机器上。
  const switchRuntime = async (runtimeId: string): Promise<string | null> => {
    if (switchingRuntime) return null;
    setSwitchingRuntime(true);
    setError(null);
    try {
      const result = await api.switchAgentBuilderRuntime(sessionId, {
        runtime_id: runtimeId,
      });
      toast.success(
        t(($) => $.creation_studio.builder.switch_runtime_success),
      );
      // 跟随服务端说它绑定的那台。能走到这里说明重绑已提交，若拒绝移动草稿，会让
      // 选择器指向一台“已不再执行任何东西”的机器——正是这条路径要消灭的分裂。
      // 客户端兜底已能把无法解析的成功响应解析成所请求的 id。
      return result.runtime_id || runtimeId;
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : t(($) => $.creation_studio.builder.switch_runtime_failed),
      );
      return null;
    } finally {
      setSwitchingRuntime(false);
    }
  };

  /**
   * 发一轮消息。
   *
   * `commitInput` 是输入框的清空动作（MUL-5181）：它在“服务端已接受消息、缓存已
   * 渲染它”的那一刻就跑，而不是等协调性失效全部 settle。等它们会让用户的文字在
   * 消息已上屏后还在框里多待三次网络往返。与主聊天的 handleSend 同一顺序。
   *
   * 参数可选，因为空态下的提示按钮直接调用本函数——没有输入框可清。
   */
  const send = async (
    content: string,
    commitInput?: () => void,
  ): Promise<boolean> => {
    const text = content.trim();
    // 重绑在途时阻塞发送：服务端反正会把两者串行化，但放消息过去，用户就没法
    // 判断是哪台运行环境回的它。
    if (!text || !sessionId || pending || switchingRuntime) return false;
    setError(null);
    try {
      const encodedContent = options.encodeInput(text);
      const result = await api.sendChatMessage(sessionId, encodedContent);
      const createdAt = new Date().toISOString();
      // 与聊天界面同一扇门（MUL-5711）。这条路以前只写平铺缓存，builder 一发送，
      // 聊天界面同会话在读的分页缓存里就缺这条消息。
      upsertChatMessageToCaches(
        qc,
        sessionId,
        {
          id: result.message_id,
          chat_session_id: sessionId,
          role: "user",
          content: encodedContent,
          task_id: result.task_id,
          created_at: createdAt,
        },
        { seedIfMissing: true },
      );
      qc.setQueryData(chatKeys.pendingTask(sessionId), {
        task_id: result.task_id,
        status: "queued",
        created_at: createdAt,
      });
      // 已接受且已渲染——协调性失效之前就先放行输入框。
      commitInput?.();
      void qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
      // 两份缓存上面都已播种，所以都需要权威重拉——否则一份“只有一条消息”的
      // 分页缓存可能比这次发送活得更久，成为后来读者看到的“全部历史”。
      void qc.invalidateQueries({ queryKey: chatKeys.messagesPage(sessionId) });
      void qc.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) });
      // 第一轮消息让一条全新会话得以入列；之后的每一轮把它带着新预览顶到最前。
      void invalidateDraftList();
      return true;
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : t(($) => $.creation_studio.builder.send_failed),
      );
      return false;
    }
  };

  const stop = async () => {
    const taskId = pendingQuery.data?.task_id;
    if (!taskId || !sessionId) return;
    qc.setQueryData(chatKeys.pendingTask(sessionId), {});
    try {
      const result = await api.cancelTaskById(taskId);
      const restored = result.cancelled_chat_message;
      if (restored) {
        // 服务端在恢复时已删掉这条提示词，所以协调前先从两份缓存移除——与聊天界面的
        // cancelChatTask 同一顺序。不移除的话，重拉落地前这行一直在；在分页缓存里
        // 则会永远留着。
        removeChatMessageFromCaches(qc, restored.chat_session_id, restored.message_id);
        if (restored.restore_to_input) {
          setRestoreDraft({
            id: restored.message_id,
            content: decodeBuilderInput(restored.content),
          });
        }
      }
      await Promise.all([
        qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) }),
        qc.invalidateQueries({ queryKey: chatKeys.messagesPage(sessionId) }),
        qc.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) }),
      ]);
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : t(($) => $.creation_studio.builder.stop_failed),
      );
      // 取消可能已在服务端落地，所以除 pending 标记外，消息也要一并重读。
      qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
      qc.invalidateQueries({ queryKey: chatKeys.messagesPage(sessionId) });
      qc.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) });
    }
  };

  return {
    messages,
    missing,
    messagesLoading: messagesQuery.isLoading,
    pendingTask: pendingQuery.data,
    pending,
    starting,
    closing,
    switchingRuntime,
    error,
    restoreDraftRequest,
    handleRestoreDraftApplied: () => {
      if (restoreDraft) {
        setRestoreDraft(null);
        return;
      }
      handleDurableRestoreApplied();
    },
    start,
    destroy,
    archiveAfterCreate,
    switchRuntime,
    send,
    stop,
  };
}
