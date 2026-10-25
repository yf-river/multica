// 聊天总控 hook：与布局无关，集中持有会话状态的解析、发送/停止/取消的
// "等待再渲染"流程、消息分页与自动已读等全部聊天逻辑，聊天页与悬浮窗口共用
// 同一套实现，保证两个界面的会话行为永远一致。
"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import { getCurrentWsId } from "@multica/core/platform";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects/queries";
import { canAssignAgent } from "@multica/views/issues/components";
import { api, dispatchReasonCode } from "@multica/core/api";
import {
  isAgentRuntimeBound as hasAgentRuntime,
  useAgentPresenceDetail,
  useCustomizeConversationStartersHref,
  useWorkspaceAgentAvailability,
} from "@multica/core/agents";
import {
  chatSessionsOptions,
  chatMessagesPageOptions,
  pendingChatTaskOptions,
  chatKeys,
  isTaskMessageTaskId,
  sortChatSessions,
} from "@multica/core/chat/queries";
import {
  useCreateChatSession,
  useMarkChatSessionRead,
  useSetChatSessionProject,
  useSetChatSessionArchived,
} from "@multica/core/chat/mutations";
import { useChatStore } from "@multica/core/chat";
import { upsertChatMessageToCaches } from "@multica/core/chat/message-cache";
import {
  enqueuePendingChatTask,
  hideQueuedChatMessages,
} from "@multica/core/chat/pending";
import { useChatDraftRestore } from "./use-chat-draft-restore";
import { useChatTaskActions } from "./use-chat-task-actions";
import { useChatProjectContextSupport } from "./use-chat-project-context-support";
import { createLogger } from "@multica/core/logger";
import type {
  Agent,
  Attachment,
  ChatMessage,
  ChatPendingTask,
} from "@multica/core/types";
import { useT } from "../../i18n";
import { useAppForeground } from "../../common/use-app-foreground";

const uiLogger = createLogger("chat.ui");
const apiLogger = createLogger("chat.api");

// 从第一条用户消息里派生一个简短的会话标题：取第一行、剥掉 Markdown、
// 压缩空白并封顶。标题是确定性的（不用 LLM）——服务端没有摘要模型，
// 所以在接入运行时生成标题之前，这是合理的默认。
const CHAT_TITLE_MAX = 30;

/**
 * 从内容派生会话标题（纯函数、确定性）：取第一行、去 Markdown 符号、压空白、截断。
 */
export function deriveChatTitle(content: string): string {
  const firstLine = (content.split("\n").find((l) => l.trim()) ?? content).trim();
  const cleaned = firstLine
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/[#*`>~_]/g, "")
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1") // markdown 链接/图片 → 只留文字
    .replace(/\s+/g, " ")
    .trim();
  if (cleaned.length <= CHAT_TITLE_MAX) return cleaned;
  return cleaned.slice(0, CHAT_TITLE_MAX - 1).trimEnd() + "…";
}

/**
 * 发送请求落定后：用户是否还在对着"发出时那个"会话输入？据此决定该清空输入框并
 * 打开新会话，还是把这次发送当"发完即走"（回复以未读的形式出现）。
 *
 * 这里特意只拿"活动会话"自己来判断。新聊天输入框是整个工作区共享的一个盒子
 * （见 DRAFT_NEW_SESSION），所以在 agent 选择器里换人只是改下一次发送的目标，
 * 并没有移动视图或草稿槽——这不算"导航离开"（MUL-4864）。要是把它算成导航，
 * 一次已完成的发送留下的文本就会继续躺在输入框里，随时可能被二次发给刚选中的 agent。
 *
 * 聊天页与悬浮窗口两条发送链路共用，规则不会在两处漂移。
 */
export function isStillOnComposeTarget(
  liveActiveSessionId: string | null,
  sentFromSessionId: string | null,
): boolean {
  return liveActiveSessionId === sentFromSessionId;
}

/**
 * 根据当前打开的会话，决定一次项目上下文切换该做什么。
 *
 *  - `awaitSession`: 有活动会话 id 但它的行还没加载完。先退出，避免一条持久化的
 *    选中记录在它的会话查询解析之前，把一次项目切换误记进新聊天草稿。
 *  - `detachCurrent`: 从打开的会话上移除项目上下文——原地操作是安全的，
 *    它只改变未来几轮接收到的内容。
 *  - `startFreshChat`: 切到*另一个*项目。开一个全新会话，好让旧项目的 provider 记忆 /
 *    复用的工作目录不会渗进来。它必须绑定在我们正要离开的会话所属的 agent
 *    （`agentId`）上：否则清空活动会话会让选中状态落回已存的 `selectedAgentId`，
 *    那可能是一个指向别的 agent 的过期偏好，把懒创建的会话发给错误的 agent。
 *  - `setDraftProject`: 没有打开的会话，所以只调整新聊天草稿的项目。
 *
 * 两条发送链路共用，过期 agent 的规则不会在两处漂移。
 */
export type ProjectContextChange =
  | { kind: "awaitSession" }
  | { kind: "detachCurrent"; sessionId: string }
  | { kind: "startFreshChat"; agentId: string; projectId: string }
  | { kind: "setDraftProject"; projectId: string | null };

export function planProjectContextChange(input: {
  targetProjectId: string | null;
  activeSessionId: string | null;
  currentSession: { id: string; agent_id: string } | null;
}): ProjectContextChange {
  if (input.activeSessionId) {
    if (!input.currentSession) return { kind: "awaitSession" };
    if (input.targetProjectId === null) {
      return { kind: "detachCurrent", sessionId: input.currentSession.id };
    }
    return {
      kind: "startFreshChat",
      agentId: input.currentSession.agent_id,
      projectId: input.targetProjectId,
    };
  }
  return { kind: "setDraftProject", projectId: input.targetProjectId };
}

/**
 * 判断会话当前是否有一条在途（in-flight）的 pending 任务——这正是"刚创建 / 正在发"
 * 的会话、在会话列表重拉取把它包含进来之前要等服务端确认的信号。`handleSend`
 * 在发送一被接受就从服务端响应种下这个任务，所以它早于 `setActiveSession`
 * 发布新会话前就已填充。刻意 NOT "有缓存消息"：在别处被删除的会话可能还带着
 * 真实的缓存历史，那不能让它豁免过期会话的自愈。
 */
export function hasInFlightPendingTask(
  qc: ReturnType<typeof useQueryClient>,
  sessionId: string,
): boolean {
  const pending = qc.getQueryData<ChatPendingTask>(chatKeys.pendingTask(sessionId));
  return Boolean(pending?.task_id);
}

/**
 * 把服务端已接受的发送种进 pending 任务缓存（乐观写入），带真实的 task_id 与
 * created_at，并让 StatusPill 以真实时钟为锚、也让会话列表重拉取之前这个刚创建的
 * 会话能豁免过期自愈。
 */
export function seedAcceptedPendingTask(
  qc: ReturnType<typeof useQueryClient>,
  sessionId: string,
  task: {
    task_id: string;
    created_at: string;
    message_id: string;
    content: string;
    supports_queue?: boolean;
    queued?: boolean;
  },
) {
  qc.setQueryData<ChatPendingTask>(
    chatKeys.pendingTask(sessionId),
    (old) => {
      const next = enqueuePendingChatTask(old, {
        task_id: task.task_id,
        status: "queued",
        created_at: task.created_at,
        message_id: task.message_id,
        content: task.content,
      }, task.queued);
      if (task.supports_queue === true || old?.supports_queue === true) {
        next.supports_queue = true;
      }
      return next;
    },
  );
  qc.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) });
}

const CHAT_VIRTUOSO_INITIAL_FIRST_ITEM_INDEX = 1_000_000;


/**
 * 与布局无关的聊天总控。持有会话相关的全部状态与行为——agent 解析、会话查找、
 * 发送/停止/取消的"等待-再渲染"流程、消息分页、自动已读——让两个界面渲染出
 * 一致的会话逻辑：
 *
 *  - ChatWindow: 悬浮 FAB 浮层（额外带缩放 / 展开 / 最小化）。
 *  - ChatPage:   一等公民的聊天标签页（会话列表 + 对话双栏）。
 *
 * 调用方只需给 `isActive`——它所在的界面当前是否在屏幕上——它把关自动已读，
 * 避免一个后台浮层把用户根本没看到的未读悄悄清掉。
 */
export function useChatController(opts?: { isActive?: boolean }) {
  const isActive = opts?.isActive ?? true;
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const activeSessionId = useChatStore((s) => s.activeSessionId);
  const selectedAgentId = useChatStore((s) => s.selectedAgentId);
  const selectedProjectId = useChatStore((s) => s.selectedProjectId);
  const setActiveSession = useChatStore((s) => s.setActiveSession);
  const setSelectedAgentId = useChatStore((s) => s.setSelectedAgentId);
  const setSelectedProjectId = useChatStore((s) => s.setSelectedProjectId);
  const user = useAuthStore((s) => s.user);
  const { data: agents = [], isSuccess: agentsLoaded } = useQuery(
    agentListOptions(wsId),
  );
  const { data: members = [], isSuccess: membersLoaded } = useQuery(
    memberListOptions(wsId),
  );
  const { data: sessions = [], isSuccess: sessionsLoaded } = useQuery(
    chatSessionsOptions(wsId),
  );
  const { data: projects = [], isSuccess: projectsLoaded } = useQuery(
    projectListOptions(wsId),
  );
  const {
    data: rawMessagePages,
    isLoading: messagesLoading,
    fetchNextPage: fetchOlderMessages,
    hasNextPage: hasOlderMessages,
    isFetchingNextPage: isFetchingOlderMessages,
  } = useInfiniteQuery(chatMessagesPageOptions(activeSessionId ?? ""));

  const messagePages = activeSessionId ? rawMessagePages?.pages ?? [] : [];
  const allMessages = [...messagePages].reverse().flatMap((page) => page.messages);

  const { data: pendingTask, isLoading: pendingTaskLoading } = useQuery(
    pendingChatTaskOptions(activeSessionId ?? ""),
  );
  const showSkeleton =
    !!activeSessionId && (messagesLoading || pendingTaskLoading);
  const messages = hideQueuedChatMessages(allMessages, pendingTask);
  const olderMessageCount = messagePages
    .slice(1)
    .reduce((sum, page) => sum + page.messages.length, 0);
  const firstItemIndex =
    messages.length > 0
      ? CHAT_VIRTUOSO_INITIAL_FIRST_ITEM_INDEX - olderMessageCount
      : 0;
  const pendingTaskId = pendingTask?.task_id ?? null;
  const stopRequestedBeforeTaskRef = useRef(false);
  // 持久化的"推迟取消"草稿恢复（#5219）。完整的生命周期——拉取、提供、
  // 跳过再提供、采纳、消费、对账——都在这只 hook 里，与悬浮聊天窗口共享。
  //
  // 以 isActive AND 应用在前台 双重把关：退到后台的浏览器标签页仍然渲染着
  // 这个控制器，它绝不能去拉取/采纳/消费一条用户正在前台界面等着的那份恢复。
  // 界面回到屏幕、应用重新聚焦后，会在下一次拉取时恢复。（appForeground 同样
  // 把关下方的自动已读。）
  const appForeground = useAppForeground();
  const { restoreDraftRequest, enqueueLocalRestore, handleRestoreDraftApplied } =
    useChatDraftRestore(activeSessionId, isActive && appForeground);
  const {
    cancelChatTask,
    handleEditQueuedTask,
    handleRemoveQueuedTask,
    handleClearQueuedTasks,
    handleSendQueuedTaskNow,
  } = useChatTaskActions(activeSessionId, enqueueLocalRestore);
  // 交给 ChatInput 的聚焦 nonce：新建聊天时把键盘焦点拉进输入框。只有
  // handleNewChat / handleStartNewChat 会加一，所以选中已有会话或深链接永不抢焦点。
  const [focusInputRequest, setFocusInputRequest] = useState(0);
  const requestInputFocus = useCallback(
    () => setFocusInputRequest((n) => n + 1),
    [],
  );
  const [conversationStarterRequest, setConversationStarterRequest] = useState<{
    id: number;
    content: string;
  } | null>(null);
  const nextConversationStarterRequestIdRef = useRef(0);
  const prefillConversationStarter = useCallback(
    (prompt: string) => {
      setConversationStarterRequest({
        id: ++nextConversationStarterRequestIdRef.current,
        content: prompt,
      });
      requestInputFocus();
    },
    [requestInputFocus],
  );
  const handleConversationStarterApplied = useCallback(
    () => setConversationStarterRequest(null),
    [],
  );

  const currentSession = activeSessionId
    ? sessions.find((s) => s.id === activeSessionId)
    : null;
  const isSessionArchived = currentSession?.status === "archived";
  const candidateProjectId = currentSession
    ? currentSession.project_id ?? null
    : selectedProjectId;
  const activeProjectId = candidateProjectId &&
    (!projectsLoaded || projects.some((project) => project.id === candidateProjectId))
    ? candidateProjectId
    : null;

  // 一个项目可能在别的客户端被删除，而本工作区"下一条新聊天"的偏好还持久化在
  // 本地。等权威的项目列表落定后尽快把它归一化，免得未来一次发送带上过期的选中。
  useEffect(() => {
    if (getCurrentWsId() !== wsId) return;
    if (!projectsLoaded || !selectedProjectId) return;
    if (projects.some((project) => project.id === selectedProjectId)) return;
    setSelectedProjectId(null);
  }, [wsId, projectsLoaded, projects, selectedProjectId, setSelectedProjectId]);

  const qc = useQueryClient();
  const createSession = useCreateChatSession();
  const markRead = useMarkChatSessionRead();
  const setSessionProject = useSetChatSessionProject();
  const setArchived = useSetChatSessionArchived();

  const currentMember = members.find((m) => m.user_id === user?.id);
  const memberRole = currentMember?.role;
  const availableAgents = agents.filter(
    (a) => !a.archived_at && canAssignAgent(a, user?.id, memberRole),
  );
  // `availableAgents` 只有在上面的两个查询都成功后才可信：权限过滤会读成员角色，
  // 所以"有 agents 但成员没加载完"会把一个 public_to 的 agent 误报成不可用。
  // 需要区分"还在加载"和"已落定但不可用"的消费方（`?agent=` 深链接）用这个
  // 而非嗅探列表是否为空把关。查询出错时故意保持 false——一次失败的拉取
  // 不能算权限裁决。
  const agentsSettled = agentsLoaded && membersLoaded;

  // 当前打开会话绑定的 agent，从完整 agent 列表里解析（含已归档，因为
  // agentListOptions 传了 include_archived）。已归档的 agent 会被过滤出
  // `availableAgents`，如果只从那个列表解析活动 agent，已归档的会话会静默渲染成
  // 某个*别的*可用 agent——头像/名字/在线状态全错，发送还对准了错误的 agent。
  // 绑定会话真正的 agent 才能保持对话诚实；归档状态随后把它变成只读
  // （见 isAgentArchived）。
  const sessionAgent = currentSession
    ? agents.find((a) => a.id === currentSession.agent_id) ?? null
    : null;
  const isAgentArchived = !!sessionAgent?.archived_at;

  // 解析选中的 agent：会话的 agent → 存储的偏好 → 第一个可用。新聊天没有会话，
  // 所以一路落到选择器。
  const activeAgent =
    sessionAgent ??
    availableAgents.find((a) => a.id === selectedAgentId) ??
    availableAgents[0] ??
    null;
  const isAgentRuntimeBound = !!activeAgent && hasAgentRuntime(activeAgent);

  // 会话可以活过创造它的权限。agent 可能被改成 personal、更换所有者、或把
  // 这个成员移出允许列表，服务端随后会拒绝每一次发送（invocation_not_allowed），
  // 同时仍正常提供 transcript（MUL-4525——读取走 view 闸门，发送重新跑 invoke
  // 闸门）。判断要基于*会话的* agent，而不只是选择器列表，让输入框提前变只读，
  // 而不是等用户打完字才撞上（MUL-6380）。与服务端通过共享谓词执行同一条规则。
  const isAgentAccessRevoked =
    !!activeAgent && !canAssignAgent(activeAgent, user?.id, memberRole);

  // 空状态开场建议按钮下方的"自定义"——唯一承认这些按钮是配置的地方。
  // 在这里解析，整页和悬浮窗口就不会对谁能看到它产生分歧。
  const customizeConversationStartersHref = useCustomizeConversationStartersHref(
    activeAgent,
    wsId,
  );

  const agentAvailability = useWorkspaceAgentAvailability();
  const noAgent = agentAvailability === "none";

  const projectContextSupport = useChatProjectContextSupport(wsId, activeAgent);

  const presenceDetail = useAgentPresenceDetail(wsId, activeAgent?.id);
  const availability =
    presenceDetail === "loading" ? undefined : presenceDetail.availability;

  // 每当用户正盯着一个有未读的会话就自动标记已读。`isActive` 让调用方声明
  // "我的界面在屏幕上"：悬浮浮层传 `isOpen`，标签页传 `true`。`appForeground`
  // 额外要求窗口可见且聚焦：应用在后台时落地的回复必须保持未读，让侧栏能给它
  // 打徽标（MUL-4485）；用户一回来、这个 effect 重跑就清掉。
  //
  // 已读调用延迟一拍（下一个 tick）并在清理时取消，所以一个只是*瞬间*活动的
  // 会话永远不会被标记已读。这是 MUL-4360 挂载竞态的修复：`activeSessionId` 是
  // 持久化的，所以在一次裸 `/chat` 导航里，页面会先恢复上一个会话渲染一帧，
  // 然后它的 URL→store effect（因为 hook 先被调用，这个 effect 后跑）才把它清回
  // null。没有延迟，那个"恢复过却从未打开"的会话就会在这一瞬被标记已读——
  // 徽标消失了，可用户根本没打开过它（右侧面板还显示着"select a chat"）。
  // 延迟让随后的 activeSessionId 变化通过清理取消那次待执行的已读；store 复查
  // 是双保险。只有撑过这一拍的会话——真正的选中、深链接或刷新——才会被读。
  const currentHasUnread =
    sessions.find((s) => s.id === activeSessionId)?.has_unread ?? false;
  useEffect(() => {
    if (!isActive || !appForeground || !activeSessionId) return;
    if (!currentHasUnread) return;
    const sessionId = activeSessionId;
    const timer = setTimeout(() => {
      if (useChatStore.getState().activeSessionId !== sessionId) return;
      uiLogger.info("auto markRead", { sessionId });
      markRead.mutate(sessionId);
    }, 0);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- markRead 引用稳定，不入依赖
  }, [isActive, appForeground, activeSessionId, currentHasUnread]);

  const sessionPromiseRef = useRef<Promise<string | null> | null>(null);
  const ensureSession = useCallback(
    async (titleSeed: string): Promise<string | null> => {
      // 只信任真实存在的会话 id：在已加载列表里，或是一个刚创建、还在等列表
      // 重拉取的（有乐观写入）。悬空的 id（已删除 / 无权限）绝不能当作已有会话
      // ——直接落下去新建一个，让消息有地方落地，而不是往 404 里 POST。
      if (
        activeSessionId &&
        (!sessionsLoaded ||
          sessions.some((s) => s.id === activeSessionId) ||
          hasInFlightPendingTask(qc, activeSessionId))
      ) {
        return activeSessionId;
      }
      if (!activeAgent) return null;
      if (sessionPromiseRef.current) return sessionPromiseRef.current;

      const promise = (async () => {
        try {
          const session = await createSession.mutateAsync({
            agent_id: activeAgent.id,
            title: deriveChatTitle(titleSeed),
            project_id: activeProjectId,
          });
          return session.id;
        } finally {
          sessionPromiseRef.current = null;
        }
      })();
      sessionPromiseRef.current = promise;
      return promise;
    },
    [
      activeSessionId,
      activeAgent,
      activeProjectId,
      createSession,
      sessions,
      sessionsLoaded,
      qc,
    ],
  );

  // 自愈悬空的 `activeSessionId`。会话列表一旦加载完、而它不在列表里——且没有
  // 在途的乐观写入豁免一个刚创建的会话——那这个 id 就是被删了、失去权限、或
  // 从未存在过（过期的 `?session=` 深链接，或持久化的悬浮窗口选中记录）。
  // 清掉它能阻止两个界面（标签页 + 悬浮窗口）都渲染一个可编辑的空聊天，
  // 发消息就会 POST 进一个不存在的会话。放在共享控制器里，每个界面都能自愈。
  useEffect(() => {
    if (getCurrentWsId() !== wsId) return;
    if (!activeSessionId || !sessionsLoaded) return;
    if (sessions.some((s) => s.id === activeSessionId)) return;
    if (hasInFlightPendingTask(qc, activeSessionId)) return;
    uiLogger.info("clearing dangling activeSessionId", { sessionId: activeSessionId });
    setActiveSession(null);
  }, [wsId, activeSessionId, sessionsLoaded, sessions, qc, setActiveSession]);

  // 上传传输已并入 ChatInput 内部的协调式上传引擎（MUL-5181 L2）；
  // 界面层只转发"有没有这个入口"。
  const uploadEnabled = !!activeAgent;

  const handleSend = useCallback(
    async (
      content: string,
      attachmentIds?: string[],
      commitInput?: (options?: { extraDraftKeys?: string[]; clearEditor?: boolean }) => void,
      draftAttachments: Attachment[] = [],
      healthReportIds: string[] = [],
    ): Promise<boolean> => {
      if (!activeAgent) {
        apiLogger.warn("sendChatMessage skipped: no active agent");
        return false;
      }
      // 只读会话：agent 已退役、无法再接活，拒绝入队一条会永远搁置的任务。
      // 输入框在这个状态下已被禁用；这里是双保险。
      if (isAgentArchived) {
        apiLogger.warn("sendChatMessage skipped: agent is archived", {
          sessionId: activeSessionId,
          agentId: activeAgent.id,
        });
        return false;
      }
      // 会话开着的时候 invoke 权限被撤销了。服务端会在持久化任何东西之前用一个
      // 403 拒绝；干脆不发能保住草稿、也省一次无谓往返。输入框在这个状态下已被
      // 禁用——这是双保险。
      if (isAgentAccessRevoked) {
        apiLogger.warn("sendChatMessage skipped: invoke permission revoked", {
          sessionId: activeSessionId,
          agentId: activeAgent.id,
        });
        return false;
      }
      if (pendingTaskId && pendingTask?.supports_queue !== true) {
        apiLogger.warn("sendChatMessage skipped: server does not support follow-up queues", {
          sessionId: activeSessionId,
        });
        return false;
      }
      if (!isAgentRuntimeBound) {
        toast.error(t(($) => $.input.runtime_required_toast));
        return false;
      }

      const finalContent = content;
      const isNewSession = !activeSessionId;

      apiLogger.info("sendChatMessage.start", {
        sessionId: activeSessionId,
        isNewSession,
        agentId: activeAgent.id,
        contentLength: finalContent.length,
        attachmentCount: attachmentIds?.length ?? 0,
      });

      let sessionId: string | null = null;
      try {
        sessionId = await ensureSession(finalContent);
      } catch (err) {
        apiLogger.error("sendChatMessage.ensureSession.error", err);
        // 被撤销的 invoke 权限会用结构化 403 挡住会话创建（MUL-4525）——
        // 点名原因，而不是一句笼统的失败。
        const reason = dispatchReasonCode(err);
        toast.error(
          reason === "invocation_not_allowed"
            ? t(($) => $.input.send_blocked_toast)
            : reason === "agent_runtime_required"
              ? t(($) => $.input.runtime_required_toast)
              : reason === "runtime_access_denied"
                ? t(($) => $.input.runtime_access_denied_toast)
                : t(($) => $.input.send_failed_toast),
        );
        return false;
      }
      if (!sessionId) {
        apiLogger.warn("sendChatMessage aborted: ensureSession returned null");
        return false;
      }

      // 等待-再渲染：在服务端接受发送之前，输入框保持用户文本与附件不动
      // （编辑器锁定、按钮通过 `submitting` 转圈）。往返落定前不会往缓存写任何
      // 东西，也绝不清草稿——一次慢发送永远不会读成"已发出但框还是满的"，
      // 一次被拒的发送则保住草稿供重试（ChatInput 从未清它）。
      let result;
      try {
        result = await api.sendChatMessage(sessionId, finalContent, attachmentIds, healthReportIds);
      } catch (err) {
        apiLogger.error("sendChatMessage.error", { sessionId, err });
        // invoke 权限可能在会话中途被撤销；发送会在持久化前被结构化 403 拒绝
        // （MUL-4525）。点名具体原因，让用户知道是权限变了、而不是该重试的
        // 临时故障。
        const reason = dispatchReasonCode(err);
        toast.error(
          reason === "invocation_not_allowed"
            ? t(($) => $.input.send_blocked_toast)
            : reason === "agent_runtime_required"
              ? t(($) => $.input.runtime_required_toast)
              : reason === "runtime_access_denied"
                ? t(($) => $.input.runtime_access_denied_toast)
                : t(($) => $.input.send_failed_toast),
        );
        return false;
      }
      apiLogger.info("sendChatMessage.success", {
        sessionId,
        messageId: result.message_id,
        taskId: result.task_id,
      });

      // 用服务端响应渲染这条已被接受的消息。在发布会话之前先喂饱消息缓存，
      // 让 activeSessionId 翻转后的第一次 useQuery 读取能同步拿到数据
      // （不闪"新建会话"骨架屏）；再种下带服务端真实 id 与 created_at 的 pending
      // 任务，让 StatusPill 一挂载就以真实时钟为锚，也让过期会话自愈在会话列表
      // 重拉取把它包含进来前，豁免这个刚创建的会话。
      const sent: ChatMessage = {
        id: result.message_id,
        chat_session_id: sessionId,
        role: "user",
        content: finalContent,
        task_id: result.task_id,
        created_at: result.created_at,
        attachments: draftAttachments,
      };
      // 进入消息缓存的唯一入口（MUL-5711）：按 id 幂等，所以这行和同一次发送的
      // chat:message 回显无论谁先到都会收敛；而更丰富的这行（带草稿附件）绝不会
      // 被没有附件字段的回显降级。
      upsertChatMessageToCaches(qc, sessionId, sent, { seedIfMissing: true });
      seedAcceptedPendingTask(qc, sessionId, {
        task_id: result.task_id,
        created_at: result.created_at,
        message_id: result.message_id,
        content: finalContent,
        supports_queue: result.supports_queue,
        queued: result.queued,
      });
      // 缓存喂饱 → 发布新的活动会话，但只在用户发送中途没有导航走掉时。
      // 见 isStillOnComposeTarget。commitInput 负责清掉已发送的草稿，并且只在
      // 用户仍在发出时的那个会话上时，才清扫共享编辑器。
      const live = useChatStore.getState();
      const stillOnSourceSession = isStillOnComposeTarget(live.activeSessionId, activeSessionId);
      if (stillOnSourceSession) {
        setActiveSession(sessionId);
      }
      commitInput?.({ extraDraftKeys: [sessionId], clearEditor: stillOnSourceSession });

      if (stopRequestedBeforeTaskRef.current) {
        stopRequestedBeforeTaskRef.current = false;
        await cancelChatTask(result.task_id, sessionId, {
          restoreDraftToInput: true,
          source: "deferred-send",
        });
        return false;
      }
      if (attachmentIds && attachmentIds.length > 0 && result.attachment_ids) {
        const boundIds = new Set(result.attachment_ids);
        const missing = attachmentIds.filter((id) => !boundIds.has(id));
        if (missing.length > 0) {
          apiLogger.warn("sendChatMessage.attachments missing after send", {
            sessionId,
            messageId: result.message_id,
            missing,
          });
          toast.error(t(($) => $.input.attachment_bind_failed_toast));
        }
      }
      qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
      qc.invalidateQueries({ queryKey: chatKeys.messagesPage(sessionId) });
      return true;
    },
    [
      activeSessionId,
      activeAgent,
      isAgentArchived,
      isAgentAccessRevoked,
      pendingTask,
      pendingTaskId,
      isAgentRuntimeBound,
      ensureSession,
      cancelChatTask,
      qc,
      setActiveSession,
      t,
    ],
  );

  const handleStop = useCallback(() => {
    if (!pendingTaskId || !activeSessionId) {
      apiLogger.debug("cancelTask skipped: no pending task");
      return;
    }
    if (!isTaskMessageTaskId(pendingTaskId)) {
      stopRequestedBeforeTaskRef.current = true;
      apiLogger.info("cancelTask.deferred until server task id", {
        taskId: pendingTaskId,
        sessionId: activeSessionId,
      });
      return;
    }
    void cancelChatTask(pendingTaskId, activeSessionId, {
      restoreDraftToInput: true,
      source: "active-input",
    });
  }, [pendingTaskId, activeSessionId, cancelChatTask]);

  const handleNewChat = useCallback(() => {
    uiLogger.info("newChat", {
      previousSessionId: activeSessionId,
      previousPendingTask: pendingTaskId,
    });
    // 新聊天默认不带项目，除非用户明确选一个。打开会话的项目是服务端持有的
    // 历史，不是下一条会话的默认。
    setSelectedProjectId(null);
    setActiveSession(null);
    requestInputFocus();
  }, [
    activeSessionId,
    pendingTaskId,
    setSelectedProjectId,
    setActiveSession,
    requestInputFocus,
  ]);

  // 开始一个绑定到指定 agent 的全新聊天。与 handleSelectAgent 不同，agent 没变时
  // 这里也不会空操作——"新聊天"总是清空活动会话，让用户落在该 agent 的空输入框。
  // 会话行在第一次发送时才懒创建（见 ensureSession）。
  const handleStartNewChat = useCallback(
    (agent: Agent) => {
      uiLogger.info("startNewChat", {
        agentId: agent.id,
        previousSessionId: activeSessionId,
      });
      setSelectedAgentId(agent.id);
      setSelectedProjectId(null);
      setActiveSession(null);
      requestInputFocus();
    },
    [
      activeSessionId,
      setSelectedAgentId,
      setSelectedProjectId,
      setActiveSession,
      requestInputFocus,
    ],
  );

  const handleSelectSession = useCallback(
    (session: { id: string; agent_id: string; project_id?: string | null }) => {
      // 会话与 agent 一一绑定——从一个别的 agent 的会话里选一个会话，
      // 会隐式地把 agent 一起切过去。
      if (activeAgent && session.agent_id !== activeAgent.id) {
        uiLogger.info("selectSession (cross-agent)", {
          from: activeAgent.id,
          toAgent: session.agent_id,
          toSession: session.id,
        });
        setSelectedAgentId(session.agent_id);
      }
      setActiveSession(session.id);
    },
    [activeAgent, setSelectedAgentId, setActiveSession],
  );

  const handleProjectChange = useCallback(
    (projectId: string | null) => {
      if (projectId === activeProjectId) return;
      uiLogger.info("selectProjectContext", {
        from: activeProjectId,
        to: projectId,
        previousSessionId: activeSessionId,
      });
      const plan = planProjectContextChange({
        targetProjectId: projectId,
        activeSessionId,
        currentSession: currentSession ?? null,
      });
      switch (plan.kind) {
        case "awaitSession":
          return;
        case "detachCurrent":
          setSessionProject.mutate({ sessionId: plan.sessionId, projectId: null });
          break;
        case "startFreshChat":
          setSelectedAgentId(plan.agentId);
          setSelectedProjectId(plan.projectId);
          setActiveSession(null);
          break;
        case "setDraftProject":
          setSelectedProjectId(plan.projectId);
          break;
      }
      requestInputFocus();
    }, [
      activeProjectId,
      activeSessionId,
      currentSession,
      setSessionProject,
      setSelectedAgentId,
      setSelectedProjectId,
      setActiveSession,
      requestInputFocus,
    ],
  );

  // 归档当前正看着的聊天，否则对话面板会卡在一个只读的"悬空"会话上。镜像
  // Inbox 列表：把选中推进到（已排序、未归档）历史里的下一个聊天，没有就回退到
  // 上一个，只剩一个也没有时才清空。非空的推进走 handleSelectSession，
  // 让 selectedAgentId 保持同步——即使下一个聊天属于别的 agent，之后的"新建聊天"
  // 仍会默认到正确的 agent。归档的不是当前打开的会话时是空操作——那个选中保持不动。
  const advanceSelectionAfterArchive = useCallback(
    (session: { id: string; agent_id: string }) => {
      if (activeSessionId !== session.id) return;
      const history = sortChatSessions(
        sessions.filter((s) => s.status !== "archived"),
      );
      const idx = history.findIndex((s) => s.id === session.id);
      const next = history[idx + 1] ?? history[idx - 1] ?? null;
      if (next) handleSelectSession(next);
      else setActiveSession(null);
    },
    [activeSessionId, sessions, handleSelectSession, setActiveSession],
  );

  const archiveSession = useCallback(
    (sessionId: string) => setArchived.mutate({ sessionId, archived: true }),
    [setArchived],
  );

  const hasMessages = messages.length > 0 || !!pendingTaskId;

  return {
    // 身份 / 各类列表
    wsId,
    user,
    agents,
    availableAgents,
    agentsSettled,
    sessions,
    projects,
    activeSessionId,
    selectedAgentId,
    activeProjectId,
    projectContextUnsupported: projectContextSupport === false,
    isProjectUpdating:
      setSessionProject.isPending || (!!activeSessionId && !currentSession),
    currentSession,
    isSessionArchived,
    isAgentArchived,
    isAgentAccessRevoked,
    isAgentRuntimeBound,
    activeAgent,
    customizeConversationStartersHref,
    noAgent,
    availability,
    // 消息
    messages,
    pendingTask,
    pendingTaskId,
    showSkeleton,
    hasMessages,
    firstItemIndex,
    hasOlderMessages: !!hasOlderMessages,
    isFetchingOlderMessages,
    fetchOlderMessages,
    // 草稿恢复
    restoreDraftRequest,
    handleRestoreDraftApplied,
    // 输入框聚焦 nonce（新建聊天时 +1）
    focusInputRequest,
    conversationStarterRequest,
    handleConversationStarterApplied,
    prefillConversationStarter,
    // 动作
    handleSend,
    handleStop,
    handleSendQueuedTaskNow,
    handleEditQueuedTask,
    handleRemoveQueuedTask,
    handleClearQueuedTasks,
    uploadEnabled,
    handleNewChat,
    handleStartNewChat,
    handleSelectSession,
    handleProjectChange,
    advanceSelectionAfterArchive,
    archiveSession,
    // store setter（供需要把选中同步到 URL 等的界面使用）
    setActiveSession,
    setSelectedAgentId,
  };
}

/** `useChatController` 的返回值类型，供子组件按类型引用。 */
export type ChatController = ReturnType<typeof useChatController>;
