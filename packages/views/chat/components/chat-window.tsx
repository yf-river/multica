// 悬浮聊天窗口：右下角可拖拽/缩放/展开/最小化的完整聊天面板。与聊天标签页共用
// 同一套会话逻辑（发送/停止/取消、草稿恢复、消息分页等），并自带会话历史下拉、
// agent 切换下拉、软键盘避让等窗口特有的交互。
"use client";

import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AnimatePresence, motion } from "motion/react";
import { Minus, Maximize2, Minimize2, ChevronDown, Plus, Check, Archive, Pencil, Loader2, Square } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { projectListOptions } from "@multica/core/projects/queries";
import { canAssignAgent } from "@multica/views/issues/components";
import { api, dispatchReasonCode } from "@multica/core/api";
import {
  isAgentRuntimeBound,
  useAgentPresenceDetail,
  useCustomizeConversationStartersHref,
  useWorkspaceAgentAvailability,
} from "@multica/core/agents";
import { ActorAvatar } from "../../common/actor-avatar";
import { useAppForeground } from "../../common/use-app-foreground";
import {
  RowActionsMenu,
  handleRowActivationKey,
  type RowActionItem,
} from "../../common/row-actions-menu";
import {
  PickerEmpty,
  PickerItem,
  PickerSection,
  PropertyPicker,
} from "../../issues/components/pickers/property-picker";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { OfflineBanner } from "./offline-banner";
import { NoAgentBanner } from "./no-agent-banner";
import { ArchivedAgentBanner } from "./archived-agent-banner";
import { AgentAccessRevokedBanner } from "./agent-access-revoked-banner";
import { RuntimeRequiredBanner } from "./runtime-required-banner";
import {
  chatSessionsOptions,
  pendingChatTaskOptions,
  pendingChatTasksOptions,
  chatKeys,
  isTaskMessageTaskId,
} from "@multica/core/chat/queries";
import {
  useCreateChatSession,
  useMarkChatSessionRead,
  useSetChatSessionArchived,
  useSetChatSessionProject,
  useUpdateChatSession,
} from "@multica/core/chat/mutations";
import { useChatStore } from "@multica/core/chat";
import { upsertChatMessageToCaches } from "@multica/core/chat/message-cache";
import { chatQuickActionsPendingOptions } from "@multica/core/chat/queries";
import { useQuickActionsPendingTimeout } from "@multica/core/chat/use-quick-actions-pending-timeout";
import { useQuickActionsFailureToast } from "./use-quick-actions-failure-toast";
import { removeChatMessageFromCaches } from "@multica/core/realtime";
import { useChatDraftRestore } from "./use-chat-draft-restore";
import { useChatTaskActions } from "./use-chat-task-actions";
import { useChatInputFocus } from "./use-chat-input-focus";
import { ChatWindowMessages } from "./chat-window-messages";
import { ChatInput } from "./chat-input";
import { ChatQueue } from "./chat-queue";
import { EmptyState } from "./chat-empty-state";
import { SessionRenameInput } from "./session-rename-input";
import { ChatResizeHandles } from "./chat-resize-handles";
import { useChatContextItems } from "./use-chat-context-items";
import { useChatResize } from "./use-chat-resize";
import { useVisualViewportKeyboard } from "./use-visual-viewport-keyboard";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import {
  hasInFlightPendingTask,
  isStillOnComposeTarget,
  planProjectContextChange,
  seedAcceptedPendingTask,
} from "./use-chat-controller";
import { useChatProjectContextSupport } from "./use-chat-project-context-support";
import { createLogger } from "@multica/core/logger";
import type { Agent, Attachment, ChatMessage, ChatSession, PendingChatTasksResponse } from "@multica/core/types";
import { useLocale, useT } from "../../i18n";

const uiLogger = createLogger("chat.ui");
const apiLogger = createLogger("chat.api");

/**
 * 悬浮聊天窗口：挂在 dashboard 布局里的可调整大小的浮动面板。关闭时保持挂载
 * （只是隐藏），所以要在查询与草稿恢复等所有副作用上以 `isOpen` + 应用前台把关。
 * 结构与聊天标签页的对话面板一致（复用同一批子组件）。
 */
export function ChatWindow() {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const isOpen = useChatStore((s) => s.isOpen);
  const activeSessionId = useChatStore((s) => s.activeSessionId);
  const { data: quickActionsPending = null } = useQuery({
    ...chatQuickActionsPendingOptions(activeSessionId ?? ""),
    enabled: isOpen && !!activeSessionId,
  });
  // 丢弃卡住的"待处理"标记（daemon 挂了 / 补充生成失败），让 pill 的转圈停下、
  // 之后刷新从干净状态开始（MUL-5149）。
  useQuickActionsPendingTimeout(activeSessionId ?? null, quickActionsPending);
  // 一次被接受的刷新后来在 daemon 里失败时弹 toast（异步的那一半）。
  useQuickActionsFailureToast(activeSessionId ?? null);
  const selectedAgentId = useChatStore((s) => s.selectedAgentId);
  const selectedProjectId = useChatStore((s) => s.selectedProjectId);
  const setOpen = useChatStore((s) => s.setOpen);
  const setActiveSession = useChatStore((s) => s.setActiveSession);
  const setSelectedAgentId = useChatStore((s) => s.setSelectedAgentId);
  const setSelectedProjectId = useChatStore((s) => s.setSelectedProjectId);
  const user = useAuthStore((s) => s.user);
  const { data: agents = [] } = useQuery({
    ...agentListOptions(wsId),
    enabled: isOpen,
  });
  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: isOpen,
  });
  // 单一会话缓存——去掉了过去在 WS 失效窗口里会互相漂移的"活动/全部"两套查询。
  const { data: sessions = [], isSuccess: sessionsLoaded } = useQuery({
    ...chatSessionsOptions(wsId),
    enabled: isOpen,
  });
  const { data: projects = [], isSuccess: projectsLoaded } = useQuery({
    ...projectListOptions(wsId),
    enabled: isOpen,
  });
  const { data: pendingTask, isLoading: pendingTaskLoading } = useQuery({
    ...pendingChatTaskOptions(activeSessionId ?? ""),
    enabled: isOpen && !!activeSessionId,
  });
  const pendingTaskId = pendingTask?.task_id ?? null;
  const stopRequestedBeforeTaskRef = useRef(false);
  // 持久化的"推迟取消"草稿恢复（#5219）。与聊天页控制器同一只 hook——
  // 跳过/采纳/消费/对账的状态机不能在两个输入框之间漂移。
  //
  // 以 isOpen AND 应用前台 双重把关：这个窗口关闭时仍保持 MOUNTED（只是隐藏，
  // 见下方 isVisible），而退到后台的浏览器标签页里 isOpen 也照样是 true。否则它的
  // ChatInput 会在没人看着的情况下采纳并消费一条恢复——从用户真正在等的那台设备
  // 的输入框里把提示词偷走。只有用户看得见的输入框才认领；重新回到前台的窗口在
  // 下一次拉取时恢复。（appForeground 同样把关下方的自动已读。）
  const appForeground = useAppForeground();
  const { restoreDraftRequest, enqueueLocalRestore, handleRestoreDraftApplied } =
    useChatDraftRestore(activeSessionId, isOpen && appForeground);
  const {
    cancelChatTask,
    handleEditQueuedTask,
    handleRemoveQueuedTask,
    handleClearQueuedTasks,
    handleSendQueuedTaskNow,
  } = useChatTaskActions(activeSessionId, enqueueLocalRestore);
  // 交给 ChatInput 的聚焦 nonce：新建聊天时（⊕ 或切换 agent）、以及窗口每次
  // 打开时把键盘焦点拉进输入框。
  const { focusRequest, requestInputFocus } = useChatInputFocus(isOpen);
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

  // 旧的软归档特性已移除，但历史遗留的 status='archived' 行可能还在——把它们
  // 排除出历史下拉。如果其中一条仍是活动会话，ChatInput 会被禁用，服务端也会
  // 拒绝它的 POST /messages。
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

  useEffect(() => {
    if (!projectsLoaded || !selectedProjectId) return;
    if (projects.some((project) => project.id === selectedProjectId)) return;
    setSelectedProjectId(null);
  }, [projectsLoaded, projects, selectedProjectId, setSelectedProjectId]);

  const qc = useQueryClient();
  const createSession = useCreateChatSession();
  const markRead = useMarkChatSessionRead();
  const setSessionProject = useSetChatSessionProject();

  const currentMember = members.find((m) => m.user_id === user?.id);
  const memberRole = currentMember?.role;
  const availableAgents = agents.filter(
    (a) => !a.archived_at && canAssignAgent(a, user?.id, memberRole),
  );

  // 当前打开会话绑定的 agent，从完整 agent 列表解析（含已归档）。已归档的 agent
  // 会被过滤出 availableAgents，如果只从那个列表解析，已归档会话会渲染成某个
  // *别的*可用 agent——头像/名字全错、发送还对准错 agent。绑定会话真正的 agent
  // 才诚实；归档状态随后把对话变成只读。
  const sessionAgent = currentSession
    ? agents.find((a) => a.id === currentSession.agent_id) ?? null
    : null;
  const isAgentArchived = !!sessionAgent?.archived_at;

  // 解析选中 agent：会话的 agent → 存储的偏好 → 第一个可用。新聊天没有会话，
  // 所以落到选择器。
  const activeAgent =
    sessionAgent ??
    availableAgents.find((a) => a.id === selectedAgentId) ??
    availableAgents[0] ??
    null;
  const activeAgentRuntimeBound =
    !!activeAgent && isAgentRuntimeBound(activeAgent);

  // 会话可以活过创造它的权限：agent 可能被改成 personal、换所有者、或把这个成员
  // 移出允许列表，服务端随后拒绝每次发送（invocation_not_allowed）、但仍正常提供
  // transcript（MUL-4525）。判断基于*会话的* agent，而不只是选择器列表，让输入框
  // 提前只读、而不是等用户打完字（MUL-6380）。镜像 use-chat-controller.ts。
  const isAgentAccessRevoked =
    !!activeAgent && !canAssignAgent(activeAgent, user?.id, memberRole);

  // 开场建议按钮下的"自定义"——空状态里唯一承认这些按钮是配置的地方。
  const customizeConversationStartersHref = useCustomizeConversationStartersHref(
    activeAgent,
    wsId,
  );

  const projectContextSupport = useChatProjectContextSupport(
    wsId,
    activeAgent,
    isOpen,
  );

  // 三态在线状态——"loading"保持中性（不显示横幅、不禁用），避免输入框在 agent
  // 列表查询落定的几百毫秒里闪一个假的"无 agent"。只有 `"none"`（服务端确认：
  // 零个可用 agent）才驱动禁用 UI。
  const agentAvailability = useWorkspaceAgentAvailability(isOpen);
  const noAgent = agentAvailability === "none";

  // 在线状态同时驱动头像状态点（经 ActorAvatar）与 OfflineBanner / TaskStatusPill
  // 的文案。`useAgentPresenceDetail` 在查询解析期间返回 "loading"——往下传
  // `undefined`，让横幅和 pill 文案在加载期间保持沉默，而不是闪出猜测性的离线文案。
  const presenceDetail = useAgentPresenceDetail(wsId, activeAgent?.id, isOpen);
  const availability =
    presenceDetail === "loading" ? undefined : presenceDetail.availability;

  // 挂载 / 卸载日志。ChatWindow 住在 DashboardLayout 里，所以在布局挂载时触发
  // （登录 / 切换工作区 / 全新页面加载）。
  useEffect(() => {
    uiLogger.info("ChatWindow mount", {
      isOpen,
      activeSessionId,
      pendingTaskId,
      selectedAgentId,
      wsId,
    });
    return () => {
      uiLogger.info("ChatWindow unmount", {
        activeSessionId,
        pendingTaskId,
      });
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- 只在挂载时跑一次
  }, []);

  // 自愈悬空的 `activeSessionId`（从存储持久化/恢复而来、指向一个被删除或已无
  // 权限的会话）：一旦会话列表加载完、里面没有它——且没有在途的 pending 任务
  // 豁免一个刚创建的会话——就清掉它，让悬浮窗口显示新聊天状态，而不是渲染一个
  // 发送会 POST 进不存在会话的可编辑空聊天。聊天标签页由共享控制器做同样的修复
  // （用 `hasInFlightPendingTask` 保持同步）。早先"No self-heal"的注释讲的是
  // 一个基于过期 `allSessions` 的朴素版本；这里的在途 pending 信号豁免新创建的
  // 会话（handleSend 在 setActiveSession 之前就把它从发送响应种进 pending 任务），
  // 所以它永远不会被误当成过期。
  useEffect(() => {
    if (!activeSessionId || !sessionsLoaded) return;
    if (sessions.some((s) => s.id === activeSessionId)) return;
    if (hasInFlightPendingTask(qc, activeSessionId)) return;
    uiLogger.info("clearing dangling activeSessionId (floating)", { sessionId: activeSessionId });
    setActiveSession(null);
  }, [activeSessionId, sessionsLoaded, sessions, qc, setActiveSession]);

  // WS 事件在 useRealtimeSync 里全局处理——即使窗口关闭，查询缓存也保持最新。
  // 见 packages/core/realtime/。

  // 每当用户正盯着一个有未读的会话就自动标记已读：窗口开着 + 应用在前台 +
  // 有活动会话 + has_unread → PATCH。has_unread 来自列表查询；WS 处理器在
  // chat:done 时使它失效，所以用户盯着时落地的回复会再次触发这个 effect 并立刻
  // 被清掉。`appForeground` 把关"正在看"的假设：回复落在窗口开着但应用退到
  // 后台时，必须保持未读让侧栏能打徽标（MUL-4485），用户重新聚焦、effect 重跑
  // 时再清掉。
  const currentHasUnread =
    sessions.find((s) => s.id === activeSessionId)?.has_unread ?? false;
  useEffect(() => {
    if (!isOpen || !appForeground || !activeSessionId) return;
    if (!currentHasUnread) return;
    uiLogger.info("auto markRead", { sessionId: activeSessionId });
    markRead.mutate(activeSessionId);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- markRead 引用稳定，不入依赖
  }, [isOpen, appForeground, activeSessionId, currentHasUnread]);

  // 用户第一次需要一个会话 id 时懒创建 chat_session——无论是为了发消息还是给
  // 上传的文件挂靠。从 handleSend 里拆出来，让上传路径（在任何文本出现之前就会
  // 触发）也能拿到一个 session_id 挂附件。没有可用 agent 时返回 null；调用方
  // 这种情况必须提前返回。
  //
  // 并发调用方（例如用户先拖入文件 → handleUploadFile，随即快速点发送 →
  // handleSend）各自都会看到 activeSessionId === null 并各自发一次
  // createSession.mutateAsync，从而建出两个会话、把附件挂到错的会话上。
  // 在途 promise ref 给这些竞态去重：第一个调用方开始创建，之后的每个调用方
  // 都等同一个 promise 直到它落定。
  //
  // titleSeed 从 send 调用进来时是用户消息的前 50 个字符；上传路径传 ""，
  // 我们让标题保持空，让会话下拉里已有的本地化 `window.untitled` 兜底生效。
  // 后续任务可能用第一条用户消息回填真实标题——在那之前，这能让会话列表
  // 跨语言仍可扫读。
  //
  // 注意：ensureSession 自己不翻转 `activeSessionId`。调用方必须在调
  // `setActiveSession(sessionId)` 之前，先把 `chatKeys.messages(sessionId)` 种进
  // Query 缓存，否则新 key 的第一次 useQuery 订阅会报告 `isLoading: true`、
  // 渲染一帧 ChatMessageSkeleton（"新建聊天首条消息"的白屏闪烁）。
  const sessionPromiseRef = useRef<Promise<string | null> | null>(null);
  const ensureSession = useCallback(
    async (titleSeed: string): Promise<string | null> => {
      // 只有真实存在的 id 才信：在已加载列表里，或是一个刚创建、还在等重拉取的
      // （有乐观写入）。悬空 id（已删除 / 无权限）绝不能当作已有会话——落下去
      // 新建一个，而不是往 404 里 POST。
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
            title: titleSeed.slice(0, 50),
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

  // 上传传输已并入 ChatInput 内部的协调式上传引擎（MUL-5181 L2）；宿主只声明
  // 有没有这个入口。上传仍是工作区级的草稿——发送才是（如需要）创建会话、
  // 让 attachment_ids 绑定到消息的时点。

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
      // 输入框在这个状态已禁用；这里是双保险。
      if (isAgentArchived) {
        apiLogger.warn("sendChatMessage skipped: agent is archived", {
          sessionId: activeSessionId,
          agentId: activeAgent.id,
        });
        return false;
      }
      // 会话开着时 invoke 权限被撤销了——服务端会在持久化任何东西之前拒绝。
      // 保住草稿、跳过这次往返。输入框在这里已禁用；双保险守卫。
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
      if (!activeAgentRuntimeBound) {
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
      // （编辑器锁定、按钮通过 `submitting` 转圈）。往返落定前不往缓存写任何
      // 东西、也绝不清草稿——慢发送不会读成"已发出但框还是满的"，
      // 被拒的发送则保住草稿供重试（ChatInput 从未清它）。
      let result;
      try {
        result = await api.sendChatMessage(sessionId, finalContent, attachmentIds, healthReportIds);
      } catch (err) {
        apiLogger.error("sendChatMessage.error", { sessionId, err });
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

      // 用服务端响应渲染这条已被接受的消息。在翻转 activeSessionId 之前先把
      // 消息缓存种好：如果先设活动会话，useQuery 对新 key 的第一次订阅看不到
      // 缓存数据、会渲染一帧 ChatMessageSkeleton——"新建聊天首条消息"的白屏
      // 闪烁。先喂缓存，activeSessionId 翻转后的第一次读取就会同步命中数据、
      // ChatMessageList 直接挂载。再种下带服务端真实 id + created_at 的
      // pending 任务，让 StatusPill 以真实时钟为锚（无本地时钟漂移、无回跳），
      // 也让过期会话自愈在会话列表重拉取落定前豁免这个刚创建的会话。
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
      // 缓存喂饱 → 发布新的活动会话，但只在用户发送中途没有导航走掉时。拿实时
      // store 对比闭包捕获的目标；规则见 isStillOnComposeTarget，悬浮窗口与聊天
      // 标签页控制器共享。commitInput 清掉已发送的草稿，并且只在用户仍在发出时
      // 的那个会话上时清扫共享编辑器。
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
      // 服务端上报它实际绑定了哪些附件 id。和我们请求的对一下，让静默的绑定失败
      // 浮现给用户——不用额外拉取。对没有该字段的旧版服务端（attachment_ids
      // undefined）跳过检查，避免误报。
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
      activeAgentRuntimeBound,
      isAgentArchived,
      isAgentAccessRevoked,
      pendingTask,
      pendingTaskId,
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

  const handleSelectAgent = useCallback(
    (agent: Agent) => {
      // 点已经在用的 agent 时空操作——不能因为用户这样关掉了菜单，就把当前
      // 会话清掉。对比 activeAgent（UI 显示的），而不是 selectedAgentId
      // （首次加载时可能是 null 或指向已归档 agent）。
      if (activeAgent && agent.id === activeAgent.id) return;
      uiLogger.info("selectAgent", {
        from: selectedAgentId,
        to: agent.id,
        previousSessionId: activeSessionId,
      });
      setSelectedAgentId(agent.id);
      // 在写一条还没发出的聊天时保留显式选中的项目，但绝不继承那条正要离开的
      // 历史会话的项目上下文。
      setSelectedProjectId(currentSession ? null : activeProjectId);
      // 切换 agent 时重置会话
      setActiveSession(null);
      requestInputFocus();
    },
    [
      activeAgent,
      selectedAgentId,
      activeSessionId,
      activeProjectId,
      currentSession,
      setSelectedAgentId,
      setSelectedProjectId,
      setActiveSession,
      requestInputFocus,
    ],
  );

  const handleNewChat = useCallback(() => {
    uiLogger.info("newChat", {
      previousSessionId: activeSessionId,
      previousPendingTask: pendingTaskId,
    });
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

  const handleSelectSession = useCallback(
    (session: ChatSession) => {
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

  const handleMinimize = useCallback(() => {
    uiLogger.info("minimize (close)", {
      activeSessionId,
      pendingTaskId,
    });
    setOpen(false);
  }, [activeSessionId, pendingTaskId, setOpen]);

  const isExpanded = useChatStore((s) => s.isExpanded);

  const windowRef = useRef<HTMLDivElement>(null);
  const { renderWidth, renderHeight, isAtMax, boundsReady, isDragging, toggleExpand, startDrag } = useChatResize(windowRef);

  // 有东西可显示就立刻显示列表（而不是空状态）——一条真实消息，或一条会有
  // timeline 流进来的 pending 任务。

  const isVisible = isOpen && (isExpanded || boundsReady);

  // 小屏完全放弃悬浮卡片形态——375px 的 90% 宽"窗口"全是外壳没有内容，所以
  // 面板改全屏（类 Lark/IM），缩放/展开入口随之消失。
  const isMobile = useIsMobile();

  // `@container`：窗口可从 360px 拖到视口的 90%，所以聊天正文的 gutter
  // （CHAT_GUTTER）必须跟随窗口自身宽度，而不是背后的页面。
  const containerClass = cn(
    "absolute z-50 flex flex-col overflow-hidden bg-surface-raised @container",
    isMobile
      ? "inset-x-0"
      : "right-2 rounded-xl shadow-[var(--floating-shadow)] ring-1 ring-surface-border",
  );
  // 软键盘只收缩 *visual* viewport——layout viewport（以及本面板贴底的父容器）
  // 保持完整高度，所以不纠正的话，面板下半部（含输入框）会躲在键盘后面，
  // 而 iOS 平移页面、砍掉的是面板的顶部。键盘弹起期间把面板钉在 visual
  // viewport 上：底边贴住它的底边（occludedBottom 追踪任何平移），高度/
  // maxHeight 受可见条带约束，让输入框骑在键盘顶边上。
  //
  // 每个分支都用显式值写同一批 style 键：motion.div 命令式地应用样式，从不
  // 清除一个只是从 style prop 里消失的键，所以这里做条件展开会把键盘几何
  // 在键盘收起后卡在 DOM 上。
  const keyboard = useVisualViewportKeyboard();
  const containerStyle: React.CSSProperties = {
    transformOrigin: "bottom right",
    pointerEvents: isOpen ? "auto" : "none",
    ...(isMobile
      ? {
          // 全屏面板贴住可见底边；宽高活在下面的 motion animate 里。
          top: "auto",
          bottom: keyboard ? keyboard.occludedBottom : 0,
          maxHeight: "none",
        }
      : {
          // 悬浮卡片；只有锚点与高度上限住在这里。
          top: "auto",
          bottom: keyboard ? keyboard.occludedBottom + 8 : 8,
          maxHeight: keyboard ? keyboard.viewportHeight - 16 : "none",
        }),
  };

  // 宽高在两种模式下都始终由 motion 持有：useIsMobile 在首次客户端渲染后才
  // 解析，而一个只是从 `animate` 消失的键会保留它的上一个 DOM 值——否则手机
  // 的第一帧会把桌面卡片的行内宽度卡在全屏面板上。混合单位（px ↔ "100%"）
  // 会跳过插值直接跳变，这正是键盘吸合时想要的行为。
  const motionSize = isMobile
    ? {
        width: "100%",
        height: keyboard ? keyboard.viewportHeight : "100%",
      }
    : { width: renderWidth, height: renderHeight };

  const contextItems = useChatContextItems(wsId, isOpen);
  const queuedTasks = pendingTask?.queued_tasks ?? [];

  return (
    <motion.div
      ref={windowRef}
      className={containerClass}
      style={containerStyle}
      initial={{ opacity: 0, scale: 0.95, ...motionSize }}
      animate={{
        opacity: isVisible ? 1 : 0,
        scale: isVisible ? 1 : 0.95,
        ...motionSize,
      }}
      transition={{
        width: isDragging ? { duration: 0 } : { type: "spring", duration: 0.3, bounce: 0 },
        height: isDragging ? { duration: 0 } : { type: "spring", duration: 0.3, bounce: 0 },
        opacity: { duration: 0.15 },
        scale: { type: "spring", duration: 0.2, bounce: 0 },
      }}
    >
      {!isMobile && <ChatResizeHandles onDragStart={startDrag} />}
      {/* 头部——⊕ 新聊天 + 会话下拉 | 窗口工具 */}
      <div className="flex items-center justify-between border-b px-4 py-2.5 gap-2">
        <div className="flex items-center gap-1 min-w-0">
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="rounded-full text-muted-foreground"
                  onClick={handleNewChat}
                />
              }
            >
              <Plus />
            </TooltipTrigger>
            <TooltipContent side="top">{t(($) => $.window.new_chat_tooltip)}</TooltipContent>
          </Tooltip>
          <SessionDropdown
            enabled={isOpen}
            sessions={sessions}
            // 用完整 agent 列表（含已归档），历史会话才能解析出它们的头像。
            agents={agents}
            activeSessionId={activeSessionId}
            onSelectSession={handleSelectSession}
          />
        </div>
        <div className="flex items-center gap-0.5 shrink-0">
          {!isMobile && (
            <Tooltip>
              <TooltipTrigger
                render={
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    className="text-muted-foreground"
                    onClick={toggleExpand}
                  />
                }
              >
                {isExpanded || isAtMax ? <Minimize2 /> : <Maximize2 />}
              </TooltipTrigger>
              <TooltipContent side="top">
                {isExpanded || isAtMax ? t(($) => $.window.restore_tooltip) : t(($) => $.window.expand_tooltip)}
              </TooltipContent>
            </Tooltip>
          )}
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="text-muted-foreground"
                  onClick={handleMinimize}
                />
              }
            >
              <Minus />
            </TooltipTrigger>
            <TooltipContent side="top">{t(($) => $.window.minimize_tooltip)}</TooltipContent>
          </Tooltip>
        </div>
      </div>

      {/* 消息订阅随列表退出，输入草稿继续保留。 */}
      <AnimatePresence initial={false}>
        {isOpen && <ChatWindowMessages sessionId={activeSessionId} pendingTask={pendingTask}
          pendingTaskLoading={pendingTaskLoading} availability={availability}
          onQuickAction={(action) => handleSend(action.prompt)}
          quickActionsDisabled={!!pendingTaskId || isSessionArchived || isAgentArchived || isAgentAccessRevoked || !activeAgentRuntimeBound || noAgent}
          quickActionsPendingMessageId={quickActionsPending?.message_id ?? null}>
          <EmptyState agent={activeAgent} hasSessions={sessions.length > 0}
            onPickPrompt={prefillConversationStarter}
            customizeHref={customizeConversationStartersHref} />
        </ChatWindowMessages>}
      </AnimatePresence>

      {/* 输入框上方的状态横幅——互斥的单槽位。
       *  优先级：无 agent > 访问被撤销 > agent 已归档 > 需要 runtime > 离线 / 不稳定。
       *  agent 是硬前提（没有它什么都发不了），所以它永远压过在线提示。
       *  最近看过的 issue/项目在输入动作行里，不属于消息/会话状态。
       *
       *  我们按 `noAgent`（解析后的空状态）而不是 `!activeAgent` 判断，
       *  让挂载到首次 agent 列表响应之间的加载窗口不显示横幅。 */}
      {noAgent ? (
        <NoAgentBanner />
      ) : isAgentAccessRevoked ? (
        <AgentAccessRevokedBanner agentName={activeAgent?.name} />
      ) : isAgentArchived ? (
        <ArchivedAgentBanner agentName={activeAgent?.name} />
      ) : !activeAgentRuntimeBound && activeAgent ? (
        <RuntimeRequiredBanner
          agentId={activeAgent.id}
          agentName={activeAgent.name}
        />
      ) : (
        <OfflineBanner agentName={activeAgent?.name} availability={availability} />
      )}

      <ChatQueue
        tasks={queuedTasks}
        headStatus={pendingTask?.status}
        onSendNow={handleSendQueuedTaskNow}
        sendNowDisabled={isAgentAccessRevoked}
        onEdit={handleEditQueuedTask}
        onRemove={handleRemoveQueuedTask}
        onClear={handleClearQueuedTasks}
      />

      {/* 输入框——旧的已归档会话、以及 agent 已归档（只读）的会话禁用；
       *  完全没有 agent 时彻底锁死（上面的空状态带着 CTA）。 */}
      <ChatInput
        onSend={handleSend}
        restoreDraftRequest={restoreDraftRequest}
        conversationStarterRequest={conversationStarterRequest}
        onConversationStarterApplied={handleConversationStarterApplied}
        onRestoreDraftApplied={handleRestoreDraftApplied}
        uploadEnabled={!!activeAgent && !isAgentAccessRevoked}
        onStop={handleStop}
        isRunning={!!pendingTaskId}
        allowSubmitWhileRunning={pendingTask?.supports_queue === true}
        disabled={
          isSessionArchived ||
          isAgentArchived ||
          isAgentAccessRevoked ||
          !activeAgentRuntimeBound
        }
        noAgent={noAgent}
        agentArchived={isAgentArchived}
        agentAccessRevoked={isAgentAccessRevoked}
        agentRuntimeRequired={!activeAgentRuntimeBound}
        agentName={activeAgent?.name}
        projects={projects}
        projectId={activeProjectId}
        onProjectChange={handleProjectChange}
        projectContextUnsupported={projectContextSupport === false}
        isProjectUpdating={
          setSessionProject.isPending || (!!activeSessionId && !currentSession)
        }
        leftAdornment={
          <AgentDropdown
            agents={availableAgents}
            activeAgent={activeAgent}
            userId={user?.id}
            onSelect={handleSelectAgent}
          />
        }
        contextItems={contextItems}
        focusRequest={focusRequest}
      />
    </motion.div>
  );
}

/**
 * Agent 下拉：头像触发，列出所有可用 agent。选一个不同的 agent = 切换 agent +
 * 开始新聊天（session=null）。当前 agent 带勾选标记且不可点。
 */
export function AgentDropdown({
  agents,
  activeAgent,
  userId,
  onSelect,
}: {
  agents: Agent[];
  activeAgent: Agent | null;
  userId: string | undefined;
  onSelect: (agent: Agent) => void;
}) {
  const { t } = useT("chat");
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState("");
  // 拆成用户自己的 agent 和别人，让菜单分组——匹配旧的 AgentSelector 布局。
  const { mine, others } = useMemo(() => {
    const mine: Agent[] = [];
    const others: Agent[] = [];
    for (const a of agents) {
      if (a.owner_id === userId) mine.push(a);
      else others.push(a);
    }
    return { mine, others };
  }, [agents, userId]);

  const query = filter.trim().toLowerCase();
  const matches = (name: string) =>
    !query || name.toLowerCase().includes(query) || matchesPinyin(name, query);
  const filteredMine = mine.filter((agent) => matches(agent.name));
  const filteredOthers = others.filter((agent) => matches(agent.name));

  const handlePick = (agent: Agent) => {
    onSelect(agent);
    setOpen(false);
  };

  if (!activeAgent) {
    return <span className="text-caption text-muted-foreground">{t(($) => $.window.no_agents)}</span>;
  }

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-64"
      align="start"
      side="top"
      searchable
      searchPlaceholder={t(($) => $.window.agent_filter_placeholder)}
      onSearchChange={setFilter}
      triggerRender={
        <button
          type="button"
          className="flex items-center gap-1.5 rounded-md px-1.5 py-1 -ml-1 cursor-pointer outline-none transition-colors hover:bg-accent aria-expanded:bg-accent"
        />
      }
      trigger={
        <>
          <ActorAvatar
            actorType="agent"
            actorId={activeAgent.id}
            size="md"
            enableHoverCard
            showStatusDot
          />
          <span className="text-caption font-medium max-w-28 truncate">{activeAgent.name}</span>
          <ChevronDown className="size-3 text-muted-foreground shrink-0" />
        </>
      }
    >
      {filteredMine.length === 0 && filteredOthers.length === 0 ? (
        <PickerEmpty />
      ) : (
        <>
          {filteredMine.length > 0 && (
            <PickerSection label={t(($) => $.window.my_agents)}>
              {filteredMine.map((agent) => (
                <AgentPickerItem
                  key={agent.id}
                  agent={agent}
                  isCurrent={agent.id === activeAgent.id}
                  onSelect={handlePick}
                />
              ))}
            </PickerSection>
          )}
          {filteredOthers.length > 0 && (
            <PickerSection label={t(($) => $.window.others)}>
              {filteredOthers.map((agent) => (
                <AgentPickerItem
                  key={agent.id}
                  agent={agent}
                  isCurrent={agent.id === activeAgent.id}
                  onSelect={handlePick}
                />
              ))}
            </PickerSection>
          )}
        </>
      )}
    </PropertyPicker>
  );
}

function AgentPickerItem({
  agent,
  isCurrent,
  onSelect,
}: {
  agent: Agent;
  isCurrent: boolean;
  onSelect: (agent: Agent) => void;
}) {
  const { t } = useT("chat");
  const runtimeBound = isAgentRuntimeBound(agent);
  return (
    <PickerItem
      selected={isCurrent}
      disabled={!runtimeBound}
      tooltip={
        runtimeBound ? undefined : t(($) => $.window.agent_needs_runtime_hint)
      }
      onClick={() => onSelect(agent)}
    >
      <ActorAvatar
        actorType="agent"
        actorId={agent.id}
        size="md"
        enableHoverCard
        showStatusDot
      />
      <span className="truncate flex-1">{agent.name}</span>
      {!runtimeBound && (
        <span className="shrink-0 text-micro text-amber-600 dark:text-amber-400">
          {t(($) => $.window.agent_needs_runtime)}
        </span>
      )}
    </PickerItem>
  );
}

interface SessionRowAction extends RowActionItem {
  /** 悬停条里的额外可见文字（停止按钮要读作 "Stop"）。 */
  stripText?: string;
}

/**
 * 会话下拉：所有未归档会话的扁平"聊天历史"列表。从别的 agent 的会话里选一个
 * 会话会隐式切换 agent（会话与 agent 一一绑定）。"新建聊天"在头部 ⊕ 按钮里，
 * 不在这个下拉里。
 */
function SessionDropdown({
  enabled,
  sessions,
  agents,
  activeSessionId,
  onSelectSession,
}: {
  enabled: boolean;
  sessions: ChatSession[];
  agents: Agent[];
  activeSessionId: string | null;
  onSelectSession: (session: ChatSession) => void;
}) {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const agentById = useMemo(() => new Map(agents.map((a) => [a.id, a])), [agents]);
  const activeSession = sessions.find((s) => s.id === activeSessionId);
  const title = activeSession?.title?.trim() || t(($) => $.window.untitled);
  const triggerAgent = activeSession ? agentById.get(activeSession.agent_id) ?? null : null;

  // 旧的软归档特性已移除。status='archived' 的历史遗留行是死数据，排除出历史。
  const historySessions = useMemo(
    () => sessions.filter((s) => s.status !== "archived"),
    [sessions],
  );

  const [isHistoryOpen, setIsHistoryOpen] = useState(false);
  const [confirmingStopId, setConfirmingStopId] = useState<string | null>(null);
  const [stoppingTaskId, setStoppingTaskId] = useState<string | null>(null);
  const [completedFlashIds, setCompletedFlashIds] = useState<Set<string>>(() => new Set());
  const previousInFlightRef = useRef<Set<string>>(new Set());
  const completedFlashTimersRef = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map());
  // 行内重命名：同一时刻只有一行处于编辑态。只记会话 id（不记整个会话），
  // 这样过期的闭包不会覆盖通过 WS 拉进来的新重命名。
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const setArchived = useSetChatSessionArchived();
  const updateSession = useUpdateChatSession();
  const setActiveSession = useChatStore((s) => s.setActiveSession);
  const queryClient = useQueryClient();
  const formatTimeAgo = useFormatTimeAgo();

  // 聚合"哪些会话当前有在途任务"。复用 FAB 用的同一条工作区级查询，所以开合
  // 聊天窗口不会多发一个请求——TanStack 按 key 去重。
  const { data: pending } = useQuery({
    ...pendingChatTasksOptions(wsId),
    enabled,
  });
  const pendingTaskBySessionId = useMemo(
    () => new Map((pending?.tasks ?? []).map((task) => [task.chat_session_id, task])),
    [pending],
  );
  const inFlightSessionIds = useMemo(
    () => new Set(pendingTaskBySessionId.keys()),
    [pendingTaskBySessionId],
  );

  useEffect(() => {
    const previous = previousInFlightRef.current;
    const unreadSessionIds = new Set(sessions.filter((s) => s.has_unread).map((s) => s.id));

    for (const sessionId of previous) {
      if (inFlightSessionIds.has(sessionId) || !unreadSessionIds.has(sessionId)) continue;

      setCompletedFlashIds((current) => {
        if (current.has(sessionId)) return current;
        return new Set(current).add(sessionId);
      });

      const existingTimer = completedFlashTimersRef.current.get(sessionId);
      if (existingTimer) clearTimeout(existingTimer);

      const timer = setTimeout(() => {
        setCompletedFlashIds((current) => {
          if (!current.has(sessionId)) return current;
          const next = new Set(current);
          next.delete(sessionId);
          return next;
        });
        completedFlashTimersRef.current.delete(sessionId);
      }, 1600);
      completedFlashTimersRef.current.set(sessionId, timer);
    }

    previousInFlightRef.current = inFlightSessionIds;
  }, [inFlightSessionIds, sessions]);

  useEffect(() => {
    const timers = completedFlashTimersRef.current;
    return () => {
      for (const timer of timers.values()) clearTimeout(timer);
      timers.clear();
    };
  }, []);

  useEffect(() => {
    if (!confirmingStopId || pendingTaskBySessionId.has(confirmingStopId)) return;
    setConfirmingStopId(null);
  }, [confirmingStopId, pendingTaskBySessionId]);

  // 头部状态拆分：
  // - 触发器内部：当前聊天自己的实时状态
  // - 触发器旁边：来自其他聊天的聚合活跃度
  const currentSessionRunning = activeSessionId ? inFlightSessionIds.has(activeSessionId) : false;
  const otherRunningCount = sessions.filter(
    (s) => s.id !== activeSessionId && inFlightSessionIds.has(s.id),
  ).length;
  const otherUnreadCount = sessions.filter(
    (s) => s.id !== activeSessionId && s.has_unread,
  ).length;

  // 归档（而不是硬删除）在这里是可逆的一键默认——与 ChatThreadList 同一套
  // 安全模型。悬浮窗口不提供硬删除：取消归档/删除只存在于完整聊天页的归档视图
  // （通过展开按钮可达），所以一个过期的悬浮下拉没法绕过"先归档、只能从归档里删"
  // 的语义。
  const handleArchive = (session: ChatSession) => {
    if (activeSessionId === session.id) {
      // 归档正在看的会话：推进到下一个聊天（没有就回退到上一个，都没了才清空），
      // 而不是把输入框晾在一个现在只读的会话上——镜像聊天标签页与 Inbox 列表。
      // 非空推进走 onSelectSession，当下一个聊天属于别的 agent 时保持
      // selectedAgentId 同步。
      const idx = historySessions.findIndex((s) => s.id === session.id);
      const next = historySessions[idx + 1] ?? historySessions[idx - 1] ?? null;
      if (next) onSelectSession(next);
      else setActiveSession(null);
    }
    setArchived.mutate({ sessionId: session.id, archived: true });
  };

  const handleSubmitRename = (sessionId: string, raw: string) => {
    const trimmed = raw.trim();
    const current = sessions.find((s) => s.id === sessionId);
    setRenamingId(null);
    // 空提交（没改或空标题）跳过网络往返——空标题服务端本来就会拒，没改的标题
    // 只会平白 bump updated_at、对用户毫无可见意义。
    if (!trimmed || trimmed === current?.title) return;
    updateSession.mutate({ sessionId, title: trimmed });
  };

  const handleSelectSession = (session: ChatSession) => {
    onSelectSession(session);
    setIsHistoryOpen(false);
  };

  const handleConfirmStop = (session: ChatSession, task: PendingChatTasksResponse["tasks"][number]) => {
    setStoppingTaskId(task.task_id);
    previousInFlightRef.current = new Set(
      [...previousInFlightRef.current].filter((sessionId) => sessionId !== session.id),
    );

    // 与活动聊天的 Stop 按钮同样的乐观行为：立刻去掉"运行中"入口，然后让
    // task:cancelled / 重拉取把所有打开的界面收敛到服务端真相。
    queryClient.setQueryData<PendingChatTasksResponse>(chatKeys.pendingTasks(wsId), (current) => {
      if (!current) return current;
      return {
        ...current,
        tasks: current.tasks.filter((item) => item.task_id !== task.task_id),
      };
    });
    queryClient.setQueryData(chatKeys.pendingTask(session.id), {});
    queryClient.invalidateQueries({ queryKey: chatKeys.messages(session.id) });
    queryClient.invalidateQueries({ queryKey: chatKeys.messagesPage(session.id) });

    api.cancelTaskById(task.task_id).then(
      (result) => {
        const restored = result.cancelled_chat_message;
        if (restored?.restore_to_input) {
          removeChatMessageFromCaches(queryClient, restored.chat_session_id, restored.message_id);
        }
        apiLogger.info("cancelTask.success (history row)", { taskId: task.task_id, sessionId: session.id });
      },
      (err) =>
        apiLogger.warn("cancelTask.error (history row; task may have already finished)", {
          taskId: task.task_id,
          sessionId: session.id,
          err,
        }),
    ).finally(() => {
      queryClient.invalidateQueries({ queryKey: chatKeys.pendingTasks(wsId) });
      queryClient.invalidateQueries({ queryKey: chatKeys.pendingTask(session.id) });
      setStoppingTaskId(null);
      setConfirmingStopId(null);
    });
  };

  const renderRow = (session: ChatSession) => {
    const isCurrent = session.id === activeSessionId;
    const agent = agentById.get(session.agent_id) ?? null;
    const pendingTask = pendingTaskBySessionId.get(session.id);
    const isRunning = !!pendingTask;
    const showCompleted = completedFlashIds.has(session.id) && !isCurrent;
    const showUnread = session.has_unread && !isCurrent;
    const isRenaming = renamingId === session.id;
    const isConfirmingStop = confirmingStopId === session.id && !!pendingTask;
    const isConfirmingAction = isConfirmingStop;
    const titleText = session.title?.trim() || t(($) => $.window.untitled);
    const trailingStatus = isRunning
      ? t(($) => $.session_history.row_subtitle.working)
      : showCompleted
        ? t(($) => $.session_history.row_subtitle.completed)
        : showUnread
          ? t(($) => $.session_history.row_subtitle.new_reply)
          : formatTimeAgo(session.updated_at);

    // 一份动作列表同时驱动两个操作面——无悬停的紧凑菜单与带悬停的悬停条——
    // 所以它们不会漂移。
    const rowActions: SessionRowAction[] = isRunning
      ? [
          {
            key: "stop",
            icon: <Square className="size-2.5 fill-current" />,
            label: t(($) => $.session_history.row_stop_aria),
            stripText: t(($) => $.session_history.stop_action),
            danger: true,
            onSelect: () => setConfirmingStopId(session.id),
          },
        ]
      : [
          {
            key: "rename",
            icon: <Pencil className="size-3.5" />,
            label: t(($) => $.session_history.row_rename_aria),
            onSelect: () => setRenamingId(session.id),
          },
          {
            key: "archive",
            icon: <Archive className="size-3.5" />,
            label: t(($) => $.list.archive),
            onSelect: () => handleArchive(session),
          },
        ];

    return (
      <div
        key={session.id}
        aria-current={isCurrent ? "true" : undefined}
        tabIndex={0}
        onClick={() => {
          if (isRenaming || isConfirmingAction) return;
          handleSelectSession(session);
        }}
        onKeyDown={(e) => {
          if (isRenaming || isConfirmingAction) return;
          handleRowActivationKey(e, () => handleSelectSession(session));
        }}
        className={cn(
          "group/history-row relative flex min-h-11 min-w-0 cursor-default items-center gap-2 overflow-hidden rounded-md py-1.5 pl-2 pr-2 outline-none transition-colors hover:bg-accent/60 focus-visible:bg-accent/60 focus-visible:ring-1 focus-visible:ring-ring",
          isCurrent && "bg-accent/70",
          isConfirmingAction && "bg-destructive/5 hover:bg-destructive/5",
        )}
      >
        {isCurrent && <span className="absolute left-0 top-1.5 bottom-1.5 w-0.5 rounded-full bg-brand" />}
        {agent ? (
          <ActorAvatar
            actorType="agent"
            actorId={agent.id}
            size="md"
            enableHoverCard
            showStatusDot
          />
        ) : (
          <span className="size-6 shrink-0" />
        )}
        <div className="min-w-0 flex-1">
          {isRenaming ? (
            <SessionRenameInput
              initialValue={session.title ?? ""}
              onSubmit={(value) => handleSubmitRename(session.id, value)}
              onCancel={() => setRenamingId(null)}
            />
          ) : isConfirmingStop ? (
            <div className="truncate text-body font-medium text-destructive">
              {t(($) => $.session_history.stop_dialog.title)}
            </div>
          ) : (
            <div
              className={cn("truncate text-body", (showUnread || showCompleted) && !isRunning && "font-medium")}
              style={{
                maskImage: "linear-gradient(to right, black calc(100% - 18px), transparent)",
                WebkitMaskImage: "linear-gradient(to right, black calc(100% - 18px), transparent)",
              }}
            >
              {titleText}
            </div>
          )}
        </div>
        {!isRenaming && (
          isConfirmingStop && pendingTask ? (
            <div className="flex shrink-0 items-center gap-1">
              <button
                type="button"
                onPointerDown={(e) => {
                  e.preventDefault();
                  e.stopPropagation();
                }}
                onClick={(e) => {
                  e.stopPropagation();
                  e.preventDefault();
                  setConfirmingStopId(null);
                }}
                disabled={stoppingTaskId === pendingTask.task_id}
                className="inline-flex h-7 items-center rounded px-2 text-micro font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:opacity-50"
              >
                {t(($) => $.session_history.stop_dialog.cancel)}
              </button>
              <button
                type="button"
                onPointerDown={(e) => {
                  e.preventDefault();
                  e.stopPropagation();
                }}
                onClick={(e) => {
                  e.stopPropagation();
                  e.preventDefault();
                  handleConfirmStop(session, pendingTask);
                }}
                disabled={stoppingTaskId === pendingTask.task_id}
                className="inline-flex h-7 items-center rounded px-2 text-micro font-medium text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50"
              >
                {stoppingTaskId === pendingTask.task_id
                  ? t(($) => $.session_history.stop_dialog.confirming)
                  : t(($) => $.session_history.stop_dialog.confirm)}
              </button>
            </div>
          ) : (
            <div className="flex shrink-0 items-center">
              <div className="flex h-7 items-center justify-end gap-1.5 text-caption text-muted-foreground [@media(hover:hover)]:group-hover/history-row:hidden [@media(hover:hover)]:group-focus-within/history-row:hidden">
                {isRunning && <Loader2 className="size-3 animate-spin" />}
                {showCompleted && !isRunning && <Check className="size-3 text-emerald-500" />}
                {showUnread && !isRunning && !showCompleted && (
                  <span
                    aria-label={t(($) => $.window.unread)}
                    title={t(($) => $.window.unread)}
                    className="size-1.5 rounded-full bg-brand"
                  />
                )}
                <span className={cn("truncate", (showUnread || showCompleted || isRunning) && "font-medium text-foreground")}>{trailingStatus}</span>
              </div>
              {/* 触屏没有悬停：没有它时，上面的状态保持原位，
                  这些动作挪进行的紧凑菜单。 */}
              <RowActionsMenu
                label={t(($) => $.session_history.row_actions_aria)}
                groups={[rowActions]}
              />
              <div className="hidden h-7 items-center gap-0.5 [@media(hover:hover)]:group-hover/history-row:flex [@media(hover:hover)]:group-focus-within/history-row:flex">
                {rowActions.map((action) => (
                  <button
                    key={action.key}
                    type="button"
                    onPointerDown={(e) => {
                      e.preventDefault();
                      e.stopPropagation();
                    }}
                    onClick={(e) => {
                      e.stopPropagation();
                      e.preventDefault();
                      action.onSelect();
                    }}
                    className={
                      action.danger
                        ? "inline-flex h-7 items-center gap-1 rounded px-1.5 text-micro font-medium text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive focus-visible:bg-destructive/10 focus-visible:text-destructive focus-visible:outline-none"
                        : "inline-flex size-7 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:bg-accent focus-visible:text-foreground focus-visible:outline-none"
                    }
                    aria-label={action.label}
                    title={action.label}
                  >
                    {action.icon}
                    {action.stripText}
                  </button>
                ))}
              </div>
            </div>
          )
        )}
      </div>
    );
  };

  return (
    <>
      <Popover open={isHistoryOpen} onOpenChange={setIsHistoryOpen}>
        <div className="flex min-w-0 items-center gap-1">
          <PopoverTrigger className="flex max-w-96 min-w-0 items-center gap-1.5 rounded-md px-1.5 py-1 transition-colors hover:bg-accent data-[popup-open]:bg-accent data-open:bg-accent">
            {triggerAgent && (
              <ActorAvatar
                actorType="agent"
                actorId={triggerAgent.id}
                size="md"
                enableHoverCard
                showStatusDot
              />
            )}
            <span className="min-w-0 truncate text-body font-medium">{title}</span>
            {currentSessionRunning && (
              <Loader2
                aria-label={t(($) => $.session_history.row_subtitle.working)}
                className="size-3 shrink-0 animate-spin text-muted-foreground"
              />
            )}
            <ChevronDown className="size-3 text-muted-foreground shrink-0" />
          </PopoverTrigger>
          {otherRunningCount > 0 ? (
            <span
              aria-label={t(($) => $.window.another_running)}
              title={t(($) => $.window.another_running)}
              className="inline-flex h-6 shrink-0 items-center gap-1 rounded-md px-1.5 text-caption font-medium text-muted-foreground"
            >
              <Loader2 className="size-3 animate-spin" />
              {otherRunningCount > 1 && <span>{otherRunningCount}</span>}
            </span>
          ) : otherUnreadCount > 0 ? (
            <span
              aria-label={t(($) => $.window.another_unread)}
              title={t(($) => $.window.another_unread)}
              className="inline-flex h-6 shrink-0 items-center gap-1 rounded-md px-1.5 text-caption font-medium text-muted-foreground"
            >
              <span className="size-1.5 rounded-full bg-brand" />
              {otherUnreadCount > 1 && <span>{otherUnreadCount}</span>}
            </span>
          ) : null}
        </div>
        <PopoverContent
          align="start"
          className="max-h-96 w-auto min-w-[max(16rem,var(--anchor-width,16rem))] max-w-96 gap-0 overflow-y-auto p-1"
          onClick={(e) => e.stopPropagation()}
        >
          {historySessions.length === 0 ? (
            <div className="px-2 py-1.5 text-caption text-muted-foreground">
              {t(($) => $.window.no_previous)}
            </div>
          ) : (
            <div role="group" aria-label={t(($) => $.window.history_group)}>
              <div className="px-1.5 py-1 text-caption font-medium text-muted-foreground">
                {t(($) => $.window.history_group)}
              </div>
              {historySessions.map(renderRow)}
            </div>
          )}
        </PopoverContent>
      </Popover>
    </>
  );
}

function useFormatTimeAgo(): (dateStr: string) => string {
  const { t } = useT("chat");
  const locale = useLocale();
  return (dateStr: string) => {
    const date = new Date(dateStr);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffMins = Math.floor(diffMs / 60000);
    const diffHours = Math.floor(diffMs / 3600000);
    const diffDays = Math.floor(diffMs / 86400000);

    if (diffMins < 1) return t(($) => $.session_history.time.just_now);
    if (diffMins < 60) return t(($) => $.session_history.time.minutes, { count: diffMins });
    if (diffHours < 24) return t(($) => $.session_history.time.hours, { count: diffHours });
    if (diffDays < 7) return t(($) => $.session_history.time.days, { count: diffDays });
    return date.toLocaleDateString(locale);
  };
}
