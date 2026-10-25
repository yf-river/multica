"use client";

// WS → 缓存的集中同步层（唯一的"事件分发中心"）：
// 监听 WebSocket 上各类事件，把载荷精确应用到 React Query 缓存或触发失效重取。
// 大原则是"WS 作失效信号 + 重取"；只有需要副作用（toast、导航、自我校验）才用
// 精确 handler。断线重连 / 切换 WSClient 时统一补失效，找回断线期间漏掉的事件。
//
// 注意：本文件所有精确写缓存的逻辑，都遵循各域（issues/chat/agents…）自带的
// "单一规则表"，避免在此处再造一套会漂移的副本。
import { useEffect, useRef } from "react";
import { useQueryClient, type InfiniteData, type QueryClient } from "@tanstack/react-query";
import type { WSClient } from "../api/ws-client";
import type { StoreApi, UseBoundStore } from "zustand";
import type { AuthState } from "../auth/store";
import { createLogger } from "../logger";
import { clearWorkspaceStorage } from "../platform/storage-cleanup";
import { defaultStorage } from "../platform/storage";
import { getCurrentWsId, getCurrentSlug } from "../platform/workspace-storage";
import { issueKeys } from "../issues/queries";
import { projectKeys } from "../projects/queries";
import { pinKeys } from "../pins/queries";
import { autopilotKeys } from "../autopilots/queries";
import { runtimeKeys } from "../runtimes/queries";
import { labelKeys } from "../labels/queries";
import { propertyKeys } from "../properties/queries";
import { issueStatusKeys } from "../issue-statuses/queries";
import {
  agentTaskSnapshotKeys,
  workspaceWorkingAgentsKeys,
  agentActivityKeys,
  agentRunCountsKeys,
  agentTasksKeys,
} from "../agents/queries";
import { githubKeys } from "../github/queries";
import { larkKeys } from "../lark/queries";
import { slackKeys } from "../slack/queries";
import { dingtalkKeys } from "../dingtalk/queries";
import { wecomKeys } from "../wecom/queries";
import { telegramKeys } from "../telegram/queries";
import {
  onIssueCreated,
  onIssueUpdated,
  onIssueDeleted,
  onIssueLabelsChanged,
  onIssuePropertiesChanged,
  onIssueMetadataChanged,
  onIssueAuxiliaryRevision,
  invalidateIssueOwnerProjections,
} from "../issues/ws-updaters";
import {
  invalidateLastActivitySortedIssueLists,
  invalidateUpdatedAtSortedIssueLists,
} from "../issues/cache-coordinator";
import { onInboxNew, onInboxInvalidate, onInboxIssueStatusChanged, onInboxIssueDeleted, onInboxSummaryInvalidate } from "../inbox/ws-updaters";
import {
  notificationPreferenceOptions,
  notificationPreferenceKeys,
} from "../notification-preferences/queries";
import { workspaceKeys, workspaceListOptions } from "../workspace/queries";
import { isWorkspaceDeletePending } from "../workspace/pending-delete";
import {
  showWebNotification,
  type SystemNotificationPayload,
} from "../platform/system-notification";
import type { Workspace } from "../types/workspace";
import {
  chatKeys,
  isTaskMessageTimelineHeld,
  mergeTaskMessagesBySeq,
  sortChatSessions,
  QUICK_ACTIONS_PENDING_TIMEOUT_MS,
} from "../chat/queries";
import { useChatStore } from "../chat";
import { upsertChatMessageToCaches } from "../chat/message-cache";
import {
  promotePendingChatTask,
  removePendingChatTask,
} from "../chat/pending";
import { resolvePostAuthDestination, useHasOnboarded } from "../paths";
import type {
  MemberAddedPayload,
  WorkspaceDeletedPayload,
  WorkspaceUpdatedPayload,
  MemberRemovedPayload,
  IssueUpdatedPayload,
  IssueCreatedPayload,
  IssueDeletedPayload,
  IssueAttachmentsChangedPayload,
  IssueLabelsChangedPayload,
  IssueMetadataChangedPayload,
  IssuePropertiesChangedPayload,
  InboxNewPayload,
  InboxItem,
  NotificationPreferenceResponse,
  CommentCreatedPayload,
  CommentUpdatedPayload,
  CommentDeletedPayload,
  CommentResolvedPayload,
  CommentUnresolvedPayload,
  ActivityCreatedPayload,
  ReactionAddedPayload,
  ReactionRemovedPayload,
  IssueReactionAddedPayload,
  IssueReactionRemovedPayload,
  SubscriberAddedPayload,
  SubscriberRemovedPayload,
  TaskMessagePayload,
  TaskQueuedPayload,
  TaskDispatchPayload,
  TaskRunningPayload,
  TaskWaitingLocalDirectoryPayload,
  TaskCompletedPayload,
  TaskFailedPayload,
  TaskCancelledPayload,
  ChatDonePayload,
  ChatQuickActionsPayload,
  ChatQuickActionsPendingState,
  ChatQuickActionsFailureState,
  ChatCancelFinalizedPayload,
  ChatMessage,
  ChatMessageEventPayload,
  ChatPendingTask,
  ChatMessagesPage,
  ChatSession,
  ChatSessionCreatedPayload,
  InvitationCreatedPayload,
} from "../types";

const chatWsLogger = createLogger("chat.ws");

/**
 * 把到达的 `task:message` 帧批处理进一次时间线缓存写的窗口（MUL-6396）。
 *
 * 固定窗口：首帧到达时武装、后续帧不重置它，这样持续流仍每 100ms 落一次，
 * 而不是一直推迟到流暂停。短到流式文本仍然实时；长到每秒发多帧的运行只需一次
 * 合并和一次渲染，而不是每帧一次。
 */
const TASK_MESSAGE_FLUSH_MS = 100;

const logger = createLogger("realtime-sync");

/** 失效某会话的两份消息缓存（扁平 + 分页）——请求它们重取。 */
export function invalidateChatMessageQueries(
  qc: QueryClient,
  sessionId: string,
) {
  qc.invalidateQueries({ queryKey: chatKeys.messages(sessionId) });
  qc.invalidateQueries({ queryKey: chatKeys.messagesPage(sessionId) });
}

// refetchPendingChatAggregate 把当前用户的跨会话待处理聚合标记为过期，使其从带权限
// 过滤的接口（/api/chat/pending-tasks[/has-any]）重取。
//
// 安全（PR #5018 / MUL-4159 评审）：这里是刻意用失效，而不是乐观 setQueryData。
// chat 的 `task:*` 事件是对整个工作区扇出的，每个成员都会收到，且载荷里没有
// 创建者/Agent 可见性——据此乐观写聚合，会让一个成员的任务把另一个成员的 FAB 翻成
// has_pending=true，绕过服务端权限过滤。失效强制"创建者 + Agent 作用域"的权威服务端
// 响应当真相来源。has-any key 嵌在 pendingTasks 之下，失效 pendingTasks 会同时刷新
// 详细列表和布尔快路径。
/** 使当前用户的跨会话待处理聚合失效（由带权限过滤的服务端接口重取）。 */
export function refetchPendingChatAggregate(
  qc: QueryClient,
  wsId: string | null | undefined,
) {
  if (!wsId) return;
  qc.invalidateQueries({ queryKey: chatKeys.pendingTasks(wsId) });
}

/**
 * 应用 chat:message 事件：把这一轮的用户消息写进消息缓存，再做权威对账（MUL-5711）。
 *
 * 载荷一直携带完整消息；这个 handler 曾经只读 `chat_session_id`、丢掉其余，使得
 * 人类自己的提示词成了"只能靠下面那次重取"才进得了会话记录的唯一一行。任何没有
 * 在本地写它的客户端——第二个窗口/设备、服务器提交后 HTTP 响应才失败的发送、
 * 半路挂载的界面——只要那次重取被丢弃（最常见是 chat:quick_actions 取消）就会丢失。
 * 两份缓存都是 staleTime: Infinity，之后没人再拉，提示词要一直缺到下次重挂载。
 *
 * 只写 `role: "user"`。SendChatMessage 是本事件的唯一生产者；从这个载荷捏造的
 * assistant 行不会携带 elapsed_ms / message_kind / quick_actions，却还占着
 * applyChatDoneToCache 即将正确写入的那个 id。
 *
 * 失效保留：本载荷没有 `attachments`，所以对账重取正是为"没有发过这条消息"的
 * 客户端补上它们的方式。
 */
export function applyChatMessageToCache(
  qc: QueryClient,
  payload: ChatMessageEventPayload,
) {
  const sessionId = payload.chat_session_id;
  if (payload.role === "user" && payload.message_id) {
    upsertChatMessageToCaches(qc, sessionId, {
      id: payload.message_id,
      chat_session_id: sessionId,
      role: "user",
      content: payload.content ?? "",
      task_id: payload.task_id ?? null,
      created_at: payload.created_at ?? new Date().toISOString(),
    });
  }
  invalidateChatMessageQueries(qc, sessionId);
  qc.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) });
}

/** 应用 chat:done 事件：把完成的助手消息写进缓存，并清掉该任务的待处理状态。 */
export function applyChatDoneToCache(
  qc: QueryClient,
  payload: ChatDonePayload,
) {
  const sessionId = payload.chat_session_id;
  const taskId = payload.task_id;
  const messageId = payload.message_id;
  const content = payload.content;
  if (messageId && (content !== undefined || (payload.quick_actions?.length ?? 0) > 0)) {
    const assistant: ChatMessage = {
      id: messageId,
      chat_session_id: sessionId,
      role: "assistant",
      content: content ?? "",
      task_id: taskId,
      created_at: payload.created_at ?? new Date().toISOString(),
      elapsed_ms: payload.elapsed_ms ?? null,
      // 带上 message_kind，让"无回应"的一轮就地渲染占位，不必等对账重取（MUL-4351）。
      // 旧服务器缺失 → "message"。
      message_kind: payload.message_kind ?? "message",
      ...(payload.quick_actions !== undefined
        ? { quick_actions: payload.quick_actions }
        : {}),
    };
    // 对重连重放、对"已把此行落地的重取"都幂等。
    upsertChatMessageToCaches(qc, sessionId, assistant);
  }
  // 替代消息已在消息列表里；这里只移除本任务。若还有后续排队任务，它会在同一
  // 渲染拍内成为下一任头部。
  qc.setQueryData<ChatPendingTask>(
    chatKeys.pendingTask(sessionId),
    (old) => removePendingChatTask(old, taskId),
  );
  // 升起/清除快捷操作占位标记。刻意放在消息缓存*之外*：下面的权威重取会替换那些
  // 缓存，存在消息上的标记会随之消失。显式 `=== true`——旧服务器整个省略该字段。
  qc.setQueryData<ChatQuickActionsPendingState | null>(
    chatKeys.quickActionsPending(sessionId),
    payload.quick_actions_pending === true && messageId
      ? {
          message_id: messageId,
          task_id: taskId,
          expires_at: Date.now() + QUICK_ACTIONS_PENDING_TIMEOUT_MS,
        }
      : null,
  );
  // 权威重取对账编校 / 迁移 / 走了上面回退分支的客户端。
  invalidateChatMessageQueries(qc, sessionId);
  qc.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) });
}

/**
 * 应用 chat:quick_actions 补充：把识别出的助手消息的快捷操作补进两份消息缓存，
 * 并解决占位标记。空/缺失列表是终态（"这轮没有建议"）——占位仍要解决。
 *
 * `payload.failed` 表示一次"再生失败"的解决：携带的操作是这轮**未变**的旧药丸，
 * 所以补丁是空操作，但要为某个视图升起失败信号来弹 toast——否则一次显式刷新失败
 * 会与一次成功且建议相同的刷新看起来一样（MUL-5149 评审）。
 */
export async function applyChatQuickActionsToCache(
  qc: QueryClient,
  payload: ChatQuickActionsPayload,
) {
  const sessionId = payload.chat_session_id;
  const actions = payload.quick_actions ?? [];
  const patch = (m: ChatMessage): ChatMessage =>
    m.id === payload.message_id ? { ...m, quick_actions: actions } : m;
  if (actions.length > 0) {
    // chat:done 的失效可能仍有一次消息重取在途，它读助手行读在 daemon 持久化这些
    // 操作*之前*。先取消它，别让那份"无操作"响应落在——并覆盖——下面的补丁之后。
    // 两份消息缓存都是 staleTime: Infinity，这种覆盖永远不会自愈
    // （MUL-5149 过期重取竞态）。必须先 cancel 再 setQueryData：cancelQueries
    // 会回退到取前状态，先打补丁会被那次回退撤销。
    await Promise.all([
      qc.cancelQueries({ queryKey: chatKeys.messages(sessionId) }),
      qc.cancelQueries({ queryKey: chatKeys.messagesPage(sessionId) }),
    ]);
    qc.setQueryData<ChatMessage[] | undefined>(
      chatKeys.messages(sessionId),
      (old) => old?.map(patch),
    );
    qc.setQueryData<InfiniteData<ChatMessagesPage> | undefined>(
      chatKeys.messagesPage(sessionId),
      (old) =>
        old
          ? {
              ...old,
              pages: old.pages.map((page) => ({
                ...page,
                messages: page.messages.map(patch),
              })),
            }
          : old,
    );
    // 收拾 cancel 的后果（MUL-5711）。cancelQueries 默认 `revert: true`，
    // 所以上面这行不止是忽略在途响应——它把缓存回滚到"那次拉取开始"时拍的快照，
    // 丢掉只有那份响应才带来的行（对等端的一条用户消息、界面卸载期间落地的任何东西）。
    // 而 staleTime: Infinity 且没有后续触发，这个洞会一直留到下次重挂载。重新失效
    // 每次补充只多一次请求，且不会丢药丸：服务端在广播本事件*之前*就已持久化操作
    // （SupplementChatQuickActions），所以这次调度出的重取会读回它们。
    invalidateChatMessageQueries(qc, sessionId);
  }
  // 只有当标记属于这条消息时才解决它：第 N 轮迟到的补充绝不能清掉第 N+1 轮的
  // chat:done 刚升起的标记（几乎不可达——daemon 会取消过期轮——但这次守卫只费一次比较）。
  qc.setQueryData<ChatQuickActionsPendingState | null>(
    chatKeys.quickActionsPending(sessionId),
    (current) =>
      current && current.message_id !== payload.message_id ? current : null,
  );
  // 对一次显式刷新且再生失败的，升起一次性失败信号。某视图消费它弹 toast 并清掉；
  // `at` 随机数让同一轮的重复失败不会被去重掉。
  if (payload.failed === true) {
    qc.setQueryData<ChatQuickActionsFailureState | null>(
      chatKeys.quickActionsFailure(sessionId),
      { message_id: payload.message_id, at: Date.now() },
    );
  }
}

type ChatSessionUpdatedPayload = {
  chat_session_id: string;
  title?: string;
  project_id?: string | null;
  pinned?: boolean;
  status?: "active" | "archived";
  updated_at?: string;
};

/**
 * 对 `chat:session_updated` 事件（改名、钉/取消钉、归档/取消归档——来自任一标签页/
 * 设备）就地修补缓存里的会话行，而不是整表重取。`pinned` 只在钉/取消钉事件里出现、
 * `status` 只在归档/取消归档里出现；纯改名两者皆缺，所以缺省字段不动既有状态。
 * 任一变化时重排，让行落到正确位置（钉 → 顶部；归档 → 另一份列表），与服务端顺序一致。
 *
 * 归档时**必须**也在这里把行的未读清零：服务端载荷只带 status/updated_at，而
 * chatSessionsOptions 是 `staleTime: Infinity`——否则另一标签页/设备的陈旧缓存会让
 * 一个已归档会话的未读徽标永远亮着——同样的 MUL-4360 卡徽标 bug，换了个界面。
 * 这镜像归档 mutation 的乐观补丁与后端为已归档行推导 unread_count=0 的行为。
 * 取消归档这里不捏造计数——真实未读状态由服务端重取带回（last_read_at 未动）——
 * 所以对 `active` 状态保留行上未读字段原样。
 */
export function applyChatSessionUpdatedToCache(
  qc: QueryClient,
  wsId: string,
  payload: ChatSessionUpdatedPayload,
): void {
  qc.setQueryData<ChatSession[]>(chatKeys.sessions(wsId), (old) => {
    if (!old) return old;
    const next = old.map((s) =>
      s.id === payload.chat_session_id
        ? {
            ...s,
            title: payload.title ?? s.title,
            ...("project_id" in payload ? { project_id: payload.project_id } : {}),
            pinned: payload.pinned ?? s.pinned,
            status: payload.status ?? s.status,
            updated_at: payload.updated_at ?? s.updated_at,
            ...(payload.status === "archived"
              ? { unread_count: 0, has_unread: false }
              : {}),
          }
        : s,
    );
    return payload.pinned === undefined && payload.status === undefined
      ? next
      : sortChatSessions(next);
  });
}

/** 从分页消息缓存里移除某条消息（就地过滤每一页）。 */
function removeChatMessageFromPageCache(
  qc: QueryClient,
  sessionId: string,
  messageId: string,
) {
  qc.setQueryData<InfiniteData<ChatMessagesPage> | undefined>(
    chatKeys.messagesPage(sessionId),
    (old) => {
      if (!old) return old;
      return {
        ...old,
        pages: old.pages.map((page) => ({
          ...page,
          messages: page.messages.filter((m) => m.id !== messageId),
        })),
      };
    },
  );
}

/** 从两份消息缓存（扁平 + 分页）里移除某条消息。 */
export function removeChatMessageFromCaches(
  qc: QueryClient,
  sessionId: string,
  messageId: string,
) {
  qc.setQueryData<ChatMessage[]>(
    chatKeys.messages(sessionId),
    (old) => old?.filter((m) => m.id !== messageId) ?? old,
  );
  removeChatMessageFromPageCache(qc, sessionId, messageId);
}

/**
 * 应用 chat:cancel_finalized 事件（#5219）：被取消 chat 任务的延迟结局，在 daemon
 * 的会话记录冲刷之后落定。
 *
 * - outcome "stopped"：一条迟到的 "Stopped." 助手行已被持久化——
 *   像 chat:done 消息一样插入。
 * - outcome "restored"：触发它的那条用户消息被删除——从缓存里移除它。
 *   被删的提示词本身从不搭这条工作区广播：它在服务端持久，只有发起者客户端经
 *   创建者授权的草稿恢复查询重取它，并由该会话的输入框应用与消费。
 *
 * 草稿恢复的失效被限定为任务的发起者，且 `initiator_user_id` 缺失时"关闭即失败"——
 * 两种情况下都没损失，因为持久恢复会在下次输入框挂载或网络重连时再次被取到。
 * 缓存补丁保持无条件——对没在看该会话的人是无操作。
 */
export function applyChatCancelFinalizedToCache(
  qc: QueryClient,
  payload: ChatCancelFinalizedPayload,
  currentUserId?: string,
) {
  const sessionId = payload.chat_session_id;
  if (!sessionId) return;
  if (payload.outcome === "stopped") {
    applyChatDoneToCache(qc, {
      chat_session_id: sessionId,
      task_id: payload.task_id,
      message_id: payload.message_id,
      content: payload.content,
      elapsed_ms: payload.elapsed_ms,
      created_at: payload.created_at,
      message_kind: payload.message_kind,
    });
    return;
  }
  if (payload.outcome === "restored") {
    if (payload.message_id) {
      removeChatMessageFromCaches(qc, sessionId, payload.message_id);
    }
    // 延迟的终局可能在某个排队后继已被提升之后到达。只移除被取消的任务，
    // 让迟到的恢复不能藏起更新的工作。
    qc.setQueryData<ChatPendingTask>(
      chatKeys.pendingTask(sessionId),
      (old) => removePendingChatTask(old, payload.task_id),
    );
    invalidateChatMessageQueries(qc, sessionId);
    qc.invalidateQueries({ queryKey: chatKeys.pendingTask(sessionId) });
    const isInitiator =
      !!payload.initiator_user_id &&
      !!currentUserId &&
      payload.initiator_user_id === currentUserId;
    if (isInitiator) {
      void qc.invalidateQueries({ queryKey: chatKeys.draftRestores(sessionId) });
    }
  }
}

/**
 * 把 workspace:updated 事件直接应用到缓存的工作区列表。
 * 若入站 `issue_prefix` 与当前缓存的不同，还会失效该工作区的 issueKeys.all——
 * 因为每条议题渲染出的标识符（`MUL-123`）都是在读取时用工作区前缀重算的。
 *
 * 若该工作区不在缓存列表里（首次观察到），保守地做失效——前缀相对缓存而言实际是
 * "新的"，任何在旧前缀下已加载的议题本来就该是过期的。
 */
export function applyWorkspaceUpdatedToCache(
  qc: QueryClient,
  payload: WorkspaceUpdatedPayload,
): void {
  const next = payload.workspace;
  if (next?.id) {
    const list = qc.getQueryData<Workspace[]>(workspaceKeys.list());
    const cached = list?.find((w) => w.id === next.id) ?? null;
    if (cached && cached.issue_prefix !== next.issue_prefix) {
      qc.invalidateQueries({ queryKey: issueKeys.all(next.id) });
    }
    if (cached && list) {
      qc.setQueryData<Workspace[]>(
        workspaceKeys.list(),
        list.map((workspace) => (workspace.id === next.id ? next : workspace)),
      );
      return;
    }
    // 不要用单个工作区去播种一份缺席的列表：staleTime 是 Infinity，那样做会一直
    // 藏起其它成员关系，直到硬刷新。
    qc.invalidateQueries({ queryKey: issueKeys.all(next.id) });
  }
  qc.invalidateQueries({ queryKey: workspaceKeys.list() });
}

/**
 * 经缓存的工作区列表（冷缓存时拉取一次）解析一条 inbox 条目源自哪个工作区的 slug。
 *
 * 通知路由必须钉在 inbox 条目的*来源*工作区，而不是当前活跃工作区：用户可能正在
 * 工作区 B，却收到一条属于工作区 A 的 `inbox:new`。工作区解析不出时返回 null——
 * 调用方绝不能回退到当前 slug（那会重新造出本功能要防止的错误路由，#3766），
 * 应当改为显示不带深链的通知。
 */
export async function resolveInboxSourceSlug(
  qc: QueryClient,
  workspaceId: string,
): Promise<string | null> {
  if (!workspaceId) return null;
  try {
    const workspaces = await qc.ensureQueryData(workspaceListOptions());
    return workspaces?.find((w) => w.id === workspaceId)?.slug ?? null;
  } catch {
    // 工作区列表不可用（例如网络抖动）：退化为不带链接的通知，而不是猜一个 slug。
    return null;
  }
}

/**
 * 端到端处理一条 `inbox:new` 事件：inbox 缓存失效、聚焦/静音检查、原生系统横幅。
 * 导出以便 handler 行为（不只是 slug 解析）可测。
 *
 * 这里每次"按工作区作用域"的读取都以条目的工作区（`item.workspace_id`）为 key，
 * 绝不用当前活跃工作区（#3766）：缓存失效必须刷新来源工作区的 inbox 列表/未读数/
 * dock 徽标，静音检查必须尊重来源工作区的偏好，深链必须携带来源工作区的 slug。
 */
export async function handleInboxNew(
  qc: QueryClient,
  item: InboxItem,
): Promise<void> {
  const sourceWsId = item.workspace_id;
  if (sourceWsId) void onInboxNew(qc, sourceWsId, item);
  // A new item in ANY workspace can light the workspace-switcher dot, so
  // refresh the cross-workspace summary regardless of the active workspace.
  void onInboxSummaryInvalidate(qc);
  // Fire a native OS notification only when the app isn't focused. When
  // the user is already looking at Multica, the inbox sidebar's unread
  // styling is enough — no need to interrupt with a banner. `desktopAPI`
  // is injected by the preload script; its absence (web app) skips silently.
  if (typeof document !== "undefined" && document.hasFocus()) return;
  // 只解析一次来源工作区的 slug：它把静音检查和深链都钉在 inbox 条目*属于*的
  // 工作区上，绝不在当前活跃工作区。在这里读 `getCurrentSlug()` 曾是错误路由
  // 的来源（#3766）：工作区 B 活跃时到达一条工作区 A 的 `inbox:new`，会发出一条
  // 带 B 的 slug 和 A 的议题 id 的通知，深链到一个 B 没有的议题。
  const slug = await resolveInboxSourceSlug(qc, sourceWsId);
  // 尊重*来源*工作区的系统通知偏好。把查询 key 在 `sourceWsId` 上并不够：请求会用
  // 跟随*活跃*工作区的 `X-Workspace-Slug` 头解析自己的工作区——所以查看 B 时一次
  // 冷缓存查询会读到 B 的静音设置、并缓存到 A 的 key 下。传来源 slug 把拉取限定在 A。
  // 解析不出 slug 时只读一份已热的缓存（早先已用正确的工作区上下文填过）而不是用
  // 错误的上下文去拉；网络失败时落入默认（"all"）而不是吞掉横幅。
  if (sourceWsId) {
    try {
      const prefData = slug
        ? await qc.ensureQueryData(
            notificationPreferenceOptions(sourceWsId, slug),
          )
        : qc.getQueryData<NotificationPreferenceResponse>(
            notificationPreferenceKeys.all(sourceWsId),
          );
      if (prefData?.preferences?.system_notifications === "muted") return;
    } catch {
      // 落入默认行为。
    }
  }
  // `issueKey` 与 inbox 页的 URL 选择器一致（条目挂在议题上时用议题 id，否则用
  // inbox 条目 id）。`itemId` 是 inbox 行的 id，点击时用它触发 markInboxRead。
  // 空的 slug（工作区列表不可用 / 条目来自本客户端不可见的工作区）仍显示横幅——
  // 用户应该知道有条 inbox 条目——但用空 slug，点击变无操作（inbox bridge 忽略
  // 空 slug），而不是路由错。
  const payload: SystemNotificationPayload = {
    slug: slug ?? "",
    itemId: item.id,
    issueKey: item.issue_id ?? item.id,
    title: item.title,
    body: item.body ?? "",
  };
  // 未授权或 SSR 时无操作——应用内 inbox 与未读徽标仍会反映新条目。
  showWebNotification(payload);
}

/**
 * 失效所有按工作区作用域的查询。重连后、以及检测到新 WSClient 实例（工作区切换）
 * 时使用，以找回断线期间错过的事件。
 */
function invalidateWorkspaceScopedQueries(qc: QueryClient): void {
  const wsId = getCurrentWsId();
  if (wsId) {
    qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
    // Through the inbox's own entry point, not a plain invalidate: a reconnect
    // can land during the list's first load, and a plain invalidate would be
    // answered by the request already on the wire (see refreshInboxQuery).
    void onInboxInvalidate(qc, wsId);
    qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    qc.invalidateQueries({ queryKey: workspaceKeys.members(wsId) });
    qc.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) });
    qc.invalidateQueries({ queryKey: workspaceKeys.skills(wsId) });
    qc.invalidateQueries({ queryKey: workspaceKeys.invitations(wsId) });
    qc.invalidateQueries({ queryKey: projectKeys.all(wsId) });
    qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
    qc.invalidateQueries({ queryKey: autopilotKeys.all(wsId) });
    qc.invalidateQueries({ queryKey: agentTaskSnapshotKeys.all(wsId) });
    qc.invalidateQueries({ queryKey: workspaceWorkingAgentsKeys.all(wsId) });
    qc.invalidateQueries({ queryKey: agentActivityKeys.all(wsId) });
    qc.invalidateQueries({ queryKey: agentRunCountsKeys.all(wsId) });
    qc.invalidateQueries({ queryKey: chatKeys.all(wsId) });
    qc.invalidateQueries({ queryKey: labelKeys.all(wsId) });
    qc.invalidateQueries({ queryKey: propertyKeys.all(wsId) });
    // 断线期间漏掉的目录编辑本会坐在 5 分钟 staleTime 后面——足以把一个服务器已
    // 归档的状态继续提供出去，或一直画着它的旧名字。
    qc.invalidateQueries({ queryKey: issueStatusKeys.all(wsId) });
  }
  // Cross-workspace, so outside the wsId guard: a reconnect may have missed
  // inbox events from any workspace, so re-pull the switcher-dot summary.
  void onInboxSummaryInvalidate(qc);
  // Per-issue caches are keyed without wsId, so the issueKeys.all(wsId)
  // prefix above does not reach them. They rely entirely on WS events for
  // freshness (staleTime: Infinity), so events missed while disconnected
  // left them stale until a full reload — the inbox showed an agent's new
  // comment while the issue timeline didn't (#3953). Inactive caches only
  // get marked stale here and refetch on next mount; the one mounted issue
  // refetches immediately, same as its own useWSReconnect already does.
  qc.invalidateQueries({ queryKey: issueKeys.timelineAll() });
  qc.invalidateQueries({ queryKey: issueKeys.reactionsAll() });
  qc.invalidateQueries({ queryKey: issueKeys.subscribersAll() });
  qc.invalidateQueries({ queryKey: issueKeys.usageAll() });
  qc.invalidateQueries({ queryKey: issueKeys.attachmentsAll() });
  qc.invalidateQueries({ queryKey: issueKeys.tasksAll() });
  // 按会话的消息缓存同样不带 wsId 作 key，所以上面 chatKeys.all(wsId) 前缀只够到
  // 会话列表/聚合。消息流依赖 staleTime: Infinity 下的 WS 失效；找回在 socket 断线
  // 期间错过 chat/task 事件的会话。
  qc.invalidateQueries({ queryKey: chatKeys.messagesAll() });
  qc.invalidateQueries({ queryKey: chatKeys.messagesPageAll() });
  qc.invalidateQueries({ queryKey: chatKeys.pendingTaskAll() });
  qc.invalidateQueries({ queryKey: chatKeys.taskMessagesAll() });
  // 断线期间错过的一条 chat:cancel_finalized 广播正是持久草稿恢复行存在的意义
  // （#5219）——重拉它们，让一个挂载着的输入框不用重挂载就能恢复提示词。
  qc.invalidateQueries({ queryKey: chatKeys.draftRestoresAll() });
  qc.invalidateQueries({ queryKey: workspaceKeys.list() });
}

/** 失效某工作区全部 squad 的"成员状态"查询。 */
function invalidateSquadMemberStatusQueries(qc: QueryClient, wsId: string): void {
  qc.invalidateQueries({
    predicate: (query) => {
      const key = query.queryKey;
      return (
        key[0] === "workspaces" &&
        key[1] === wsId &&
        key[2] === "squads" &&
        key[4] === "members-status"
      );
    },
  });
}

/** 集中的 WS → store 同步需要的平台 store 集合。 */
export interface RealtimeSyncStores {
  authStore: UseBoundStore<StoreApi<AuthState>>;
}

/**
 * 集中的 WS → store 同步。由 WSProvider 调用一次。
 *
 * 采用"WS 作失效信号 + 重取"的模式：
 * - onAny 处理器提取事件前缀并调用对应 store 刷新
 * - 按前缀去抖，防止连珠炮式重取（例如批量议题更新）
 * - 精确 handler 只留给副作用（toast、导航、自我校验）
 *
 * 按议题的事件（评论、活动、反应、订阅者）既在这里处理（失效兜底），也由各页的
 * useWSEvent hooks 处理（精细更新）。daemon 注册事件全局失效 runtimes；
 * 心跳跳过以免过度重取。
 *
 * @param ws - WebSocket 客户端实例（尚未连上时为 null）
 * @param stores - 平台创建的 auth/workspace 等 Zustand store 实例
 * @param onToast - 显示 toast 消息的可选回调（平台相关）
 */
export function useRealtimeSync(
  ws: WSClient | null,
  stores: RealtimeSyncStores,
  onToast?: (message: string, type?: "info" | "error") => void,
) {
  const { authStore } = stores;
  const qc = useQueryClient();

  // 用 ref 捕获 hasOnboarded，让（罕见的）hasOnboarded 变化不会让本 effect 里每个
  // WS handler 重新订阅。解析器在工作区消失触发那一刻读 `.current`——正是我们想要的。
  const hasOnboarded = useHasOnboarded();
  const hasOnboardedRef = useRef(hasOnboarded);
  hasOnboardedRef.current = hasOnboarded;

  // 主同步：onAny → refreshMap + 去抖。
  useEffect(() => {
    if (!ws) return;

    const refreshMap: Record<string, () => void> = {
      inbox: () => {
        const wsId = getCurrentWsId();
        if (wsId) void onInboxInvalidate(qc, wsId);
        // inbox:read / inbox:archived / inbox:unarchived / batch events arrive
        // here. They can originate from a workspace other than the active one
        // (personal events fan out to all the user's connections), so always
        // refresh the cross-workspace summary — its dot must clear when another
        // workspace's items are read/archived, and light again when an unread
        // item is restored from the archive.
        void onInboxSummaryInvalidate(qc);
      },
      agent: () => {
        const wsId = getCurrentWsId();
        if (wsId) {
          qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
          qc.invalidateQueries({ queryKey: workspaceWorkingAgentsKeys.all(wsId) });
          // Squad 成员状态是逐个 Agent 派生的，所以任何 Agent 变化（状态翻转、归档、
          // 换 runtime）都要刷新按 squad 的 members-status 缓存，而不重取静态的
          // squad 列表摘要。
          invalidateSquadMemberStatusQueries(qc, wsId);
        }
      },
      member: () => {
        const wsId = getCurrentWsId();
        if (wsId) qc.invalidateQueries({ queryKey: workspaceKeys.members(wsId) });
      },
      // workspace:updated 由下面特定 handler 处理（比较前缀决定是否也要失效议题）。
      // 这个通用兜底仍会为 workspace:deleted（配合特定导航 handler）以及任何还没有
      // 专属 handler 的未来 workspace:* 事件触发。
      workspace: () => {
        qc.invalidateQueries({ queryKey: workspaceKeys.list() });
      },
      skill: () => {
        const wsId = getCurrentWsId();
        if (wsId) qc.invalidateQueries({ queryKey: workspaceKeys.skills(wsId) });
      },
      project: () => {
        const wsId = getCurrentWsId();
        if (wsId) {
          qc.invalidateQueries({ queryKey: projectKeys.all(wsId) });
          qc.invalidateQueries({ queryKey: ["workspace-repositories", wsId] });
        }
      },
      squad: () => {
        const wsId = getCurrentWsId();
        if (wsId) {
          qc.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) });
          // squad:deleted 会触发负责人转移——议题也要一起刷新。
          qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
        }
      },
      label: () => {
        // 标签目录分别按议题、Agent、技能独立作用域。通用事件前缀不携带作用域进这个
        // 分发器，所以刷新全部三类资源投影。议题行内嵌标签快照；Agent/技能的标签
        // 选择器用自己的资源缓存 + 共享标签查询树。
        const wsId = getCurrentWsId();
        if (wsId) {
          qc.invalidateQueries({ queryKey: ["labels", wsId] });
          qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
          qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
          qc.invalidateQueries({ queryKey: workspaceKeys.skills(wsId) });
        }
      },
      // 议题状态目录（MUL-6243）。管理员在设置页里编辑它；其它每个标签页/设备都在
      // 基于它渲染状态。
      //
      // 只失效——事件不带可合并的条目。议题缓存刻意不跟着拖：行里存的是状态 KEY，
      // 名字/颜色/分类都在渲染时从该目录解析（`useStatusLabel`、`colorOf`），所以
      // 重取目录才是让改名重新绘制的动作。把它连同每个看板和列表一起拉，会把一次
      // 管理员改名变成每个已连接客户端的整工作区重取风暴。（MUL-6458）
      issue_status: () => {
        const wsId = getCurrentWsId();
        if (wsId) qc.invalidateQueries({ queryKey: issueStatusKeys.all(wsId) });
      },
      pin: () => {
        const wsId = getCurrentWsId();
        const userId = authStore.getState().user?.id;
        if (wsId && userId) qc.invalidateQueries({ queryKey: pinKeys.all(wsId, userId) });
      },
      daemon: () => {
        const wsId = getCurrentWsId();
        if (wsId) {
          qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
          // Shared agents may carry a redacted runtime liveness projection;
          // refetch it when a daemon changes state even if its private runtime
          // is absent from this member's runtime list.
          qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
          // Runtime online/offline transitions move the derived status
          // for every agent that hosts on this runtime, which shifts the
          // working/idle/offline pill on the squad page.
          invalidateSquadMemberStatusQueries(qc, wsId);
        }
      },
      autopilot: () => {
        const wsId = getCurrentWsId();
        if (wsId) qc.invalidateQueries({ queryKey: autopilotKeys.all(wsId) });
      },
      github_installation: () => {
        const wsId = getCurrentWsId();
        if (wsId) qc.invalidateQueries({ queryKey: githubKeys.installations(wsId) });
      },
      lark_installation: () => {
        const wsId = getCurrentWsId();
        if (wsId) qc.invalidateQueries({ queryKey: larkKeys.installations(wsId) });
      },
      slack_installation: () => {
        const wsId = getCurrentWsId();
        if (wsId) qc.invalidateQueries({ queryKey: slackKeys.installations(wsId) });
      },
      dingtalk_installation: () => {
        const wsId = getCurrentWsId();
        if (wsId) qc.invalidateQueries({ queryKey: dingtalkKeys.installations(wsId) });
      },
      vcs_connection: () => {
        const wsId = getCurrentWsId();
        if (wsId) qc.invalidateQueries({ queryKey: ["vcs", wsId] });
      },
      wecom_installation: () => {
        const wsId = getCurrentWsId();
        if (wsId) qc.invalidateQueries({ queryKey: wecomKeys.installations(wsId) });
      },
      telegram_installation: () => {
        const wsId = getCurrentWsId();
        if (wsId) qc.invalidateQueries({ queryKey: telegramKeys.installations(wsId) });
      },
      pull_request: () => {
        // PR 列表按议题 id 作 key、不按工作区，所以失效全部 PR 查询——开着的议题
        // 详情页会重取自己的那份列表。
        qc.invalidateQueries({ queryKey: ["github", "pull-requests"] });
      },
      // 驱动 Agent 呈现缓存：任何任务生命周期变化（dispatch/completed/failed/
      // cancelled）都刷新工作区级 agent-task-snapshot 查询，让逐 Agent 呈现反映变化。
      // task:message 不在这个前缀路径里——它留在 specificEvents 里，避免长运行期间
      // 的失效风暴。
      task: () => {
        const wsId = getCurrentWsId();
        if (!wsId) return;
        qc.invalidateQueries({ queryKey: agentTaskSnapshotKeys.list(wsId) });
        qc.invalidateQueries({ queryKey: workspaceWorkingAgentsKeys.all(wsId) });
        // Table 的"工作中 Agent"快捷方式从上面那份投影推导负责人集合。把服务端持有的
        // 关系图与那集合一起刷新，别让行/组/facet 停留在一次旧任务转换上，而投影却
        // 已在重取（全局 staleTime 是 Infinity）。
        qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
        // 30 天活动序列共享同一生命周期信号——任何任务完成/失败都会移动直方图。
        // （单独 dispatch 不会改变按 completed_at 锚定的序列，但在这里失效保持
        // WS handler 形状统一；引发的重取很便宜。）列表（尾部 7 天切片）和详情面板
        // 都读这一份缓存。
        qc.invalidateQueries({ queryKey: agentActivityKeys.last30d(wsId) });
        // 30 天运行计数同样随每个任务生命周期事件递增。
        qc.invalidateQueries({ queryKey: agentRunCountsKeys.last30d(wsId) });
      // 单 Agent 任务列表（Activity 标签"Recent work"）。前缀匹配会覆盖每个
      // Agent 的列表——单 Agent 详情 key 在 agentTasks/<wsId>/<agentId> 之下。
      qc.invalidateQueries({ queryKey: agentTasksKeys.all(wsId) });
      // 按议题的任务列表（议题详情"执行日志"）。跨所有议题做前缀匹配——维持
      // "任何 task: 事件都让所有任务列表查询过期"的契约，即使相关组件当前没挂载
      // 缓存也保持新鲜。

      // 按议题的 token 用量卡片（议题详情右侧栏）。与上面 tasks 失效同构——任何
      // 任务生命周期事件都会移动聚合用量数字。
      qc.invalidateQueries({ queryKey: ["issues", "usage"] });
      // Squad 成员状态用同一份任务生命周期把每个 Agent 成员在 working ↔ idle 间翻转。
      invalidateSquadMemberStatusQueries(qc, wsId);
      // 评论触发预览回答"现在发一条会唤醒谁"——待处理任务去重守卫让答案依赖队列，
      // 所以任何任务生命周期变化都要刷新打开的输入框的 chips（例如一个 Agent 跑完
      // 后，打字中途它又变成可触发的了）。
      qc.invalidateQueries({ queryKey: issueKeys.commentTriggerPreviewAll() });
      // 议题触发预览（指派/状态/创建/批量）刻意不在这里失效。与评论触发不同，指派
      // 来源（创建/负责人变更）在入队前会取消已有任务，所以任务事件永远改变不了它的
      // 判定；只有状态来源的 pending 去重可能改变，而那个预览只是建议性——写路径会
      // 权威地重新评估，罕见的一次陈旧标签无害。每次工作区任务事件都把每个挂载预览
      // 重取一次会造成可见闪烁，所以预览现在只在输入变化（签名）时重取，与其查询
      // 设计一致（MUL-3375）。
      },
    };

  const timers = new Map<string, ReturnType<typeof setTimeout>>();
  const debouncedRefresh = (prefix: string, fn: () => void) => {
    const existing = timers.get(prefix);
    if (existing) clearTimeout(existing);
    timers.set(
      prefix,
      setTimeout(() => {
        timers.delete(prefix);
        fn();
      }, 100),
    );
  };

  // 以下事件类型由下方特定 handler 处理——跳过通用刷新。
  const specificEvents = new Set([
    "workspace:updated",
    "issue:updated", "issue:created", "issue:deleted", "issue_attachments:changed", "issue_labels:changed", "issue_metadata:changed", "issue_properties:changed", "property:created", "property:updated", "inbox:new",
    "comment:created", "comment:updated", "comment:deleted",
    "comment:resolved", "comment:unresolved",
    "activity:created",
    "reaction:added", "reaction:removed",
    "issue_reaction:added", "issue_reaction:removed",
    "subscriber:added", "subscriber:removed",
    "daemon:heartbeat",
    // Chat 事件在下面显式处理；这里不要双重失效。
    "chat:message", "chat:done", "chat:quick_actions", "chat:cancel_finalized", "chat:session_read",
    "chat:session_created", "chat:session_deleted", "chat:session_updated",
    // task:message 不从前缀路径走，因为长运行中它每条流式消息都发一次——每条消息
    // 都失效快照会淹没网络。下面的 Chat 特定 handler 仍经 ws.on() 收到它
    // （一条独立订阅通道）。
    "task:message",
    // task:completed / task:failed 刻意不在此列。它们既走 task 前缀失效（刷新
    // agent-task-snapshot 缓存）也走下面的 chat 特定 ws.on() handler。两条通道
    // 相互独立——onAny 分发与 ws.on 是各自的订阅。
  ]);

  const unsubAny = ws.onAny((msg) => {
    if (specificEvents.has(msg.type)) return;
    if (msg.type.startsWith("task:")) {
      const payload = msg.payload as {issue_id?:string;task?:{issue_id?:string}};
      const issueId = payload?.issue_id ?? payload?.task?.issue_id;
      if (issueId) { qc.invalidateQueries({queryKey:issueKeys.tasks(issueId)}); qc.invalidateQueries({queryKey:["run-diff",issueId]}); }
    }
    const prefix = msg.type.split(":")[0] ?? "";
    const refresh = refreshMap[prefix];
    if (refresh) debouncedRefresh(prefix, refresh);
  });

  // --- 特定事件 handler（精细缓存更新） ---
  // 不做"自我事件"过滤：actor_id 标识的是"用户"，不是"标签页"。按 actor_id 过滤
  // 会挡住同一用户的其它标签页。取而代之的是 mutation 与 WS handler 都用去重检查
  // 来保证幂等。

  const unsubIssueUpdated = ws.on("issue:updated", (p) => {
      const payload = p as IssueUpdatedPayload;
      const { issue } = payload;
      if (!issue?.id) return;
      const wsId = getCurrentWsId();
      if (wsId) {
        onIssueUpdated(qc, wsId, issue, {
          assigneeChanged: payload.assignee_changed,
          statusChanged: payload.status_changed,
          projectChanged: payload.project_changed,
        });
        if (issue.status) {
          onInboxIssueStatusChanged(qc, wsId, issue.id, issue.status);
        }
      }
    });

    const unsubIssueCreated = ws.on("issue:created", (p) => {
      const { issue } = p as IssueCreatedPayload;
      if (!issue) return;
      const wsId = getCurrentWsId();
      if (wsId) onIssueCreated(qc, wsId, issue);
    });

    const unsubIssueDeleted = ws.on("issue:deleted", (p) => {
      const { issue_id } = p as IssueDeletedPayload;
      if (!issue_id) return;
      const wsId = getCurrentWsId();
      if (wsId) {
        onIssueDeleted(qc, wsId, issue_id);
        void onInboxIssueDeleted(qc, wsId, issue_id);
      }
    });

    const unsubIssueLabelsChanged = ws.on("issue_labels:changed", (p) => {
      const { issue_id, labels, issue_revision } = p as IssueLabelsChangedPayload;
      if (!issue_id) return;
      const wsId = getCurrentWsId();
      if (wsId) onIssueLabelsChanged(qc, wsId, issue_id, labels ?? [], issue_revision);
    });

    const unsubIssueAttachmentsChanged = ws.on("issue_attachments:changed", (p) => {
      const { issue_id, issue_revision } = p as IssueAttachmentsChangedPayload;
      if (!issue_id) return;
      qc.invalidateQueries({ queryKey: issueKeys.attachments(issue_id) });
      const wsId = getCurrentWsId();
      if (wsId) onIssueAuxiliaryRevision(qc, wsId, issue_id, issue_revision);
    });

    const unsubIssueMetadataChanged = ws.on("issue_metadata:changed", (p) => {
      const { issue_id, metadata, issue_revision } = p as IssueMetadataChangedPayload;
      if (!issue_id) return;
      const wsId = getCurrentWsId();
      if (wsId) onIssueMetadataChanged(qc, wsId, issue_id, metadata ?? {}, issue_revision);
    });

    const unsubIssuePropertiesChanged = ws.on("issue_properties:changed", (p) => {
      const { issue_id, properties, issue_revision } = p as IssuePropertiesChangedPayload;
      if (!issue_id) return;
      const wsId = getCurrentWsId();
      if (wsId) {
        onIssuePropertiesChanged(qc, wsId, issue_id, properties ?? {}, issue_revision);
        // 目录里嵌着每个定义的使用计数；每次设值/清值都会让它变化。目录很小，
        // 重取好过在客户端尝试补计数。
        qc.invalidateQueries({ queryKey: propertyKeys.all(wsId) });
      }
    });

    // 定义变化（创建/改名/选项/归档）——重取目录；议题缓存保留原始值袋子所以仍有效。
    const unsubPropertyChanged = ["property:created", "property:updated"].map((event) =>
      ws.on(event as "property:created" | "property:updated", () => {
        const wsId = getCurrentWsId();
        if (wsId) {
          qc.invalidateQueries({ queryKey: propertyKeys.all(wsId) });
          // 分组顺序、支持的分组类型、不可用的选项值都从属性定义推导，不只是议题行。
          qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
        }
      }),
    );

    const unsubInboxNew = ws.on("inbox:new", async (p) => {
      const { item } = p as InboxNewPayload;
      if (!item) return;
      await handleInboxNew(qc, item);
    });

    // --- 时间线事件 handler（全局兜底） ---
    // 这些事件在 IssueDetail 挂载时也由 useIssueTimeline 精细处理。这里的全局
    // handler 存在，是为了把"其 IssueDetail *没*挂载"的议题的时间线缓存标记为过期，
    // 免得下次挂载时端出陈旧数据（QueryClient 默认的 staleTime: Infinity 正依赖它）。
    //
    // `refetchType: "none"` 是关键细节：没有它，一个活跃的 IssueDetail 观察者会在
    // 每条评论/活动/反应事件时把整条时间线重取一遍。重取会替换每条条目的引用，
    // 弄爆每个 CommentCard 子树上的 React.memo（AI 流式输出时表现为所有兄弟线程一起
    // 闪，MUL-1941）。非活跃观察者无论哪种都不重取；IssueDetail 稍后挂载时，
    // 过期标记经 `refetchOnMount` 触发重取。活跃观察者则靠 `useIssueTimeline` 里的
    // 精细 setQueryData handler 保持新鲜。
    const invalidateTimeline = (issueId: string) => {
      qc.invalidateQueries({
        queryKey: issueKeys.timeline(issueId),
        refetchType: "none",
      });
    };

    const unsubCommentCreated = ws.on("comment:created", (p) => {
      const { comment, issue_revision: issueRevision } = p as CommentCreatedPayload;
      if (!comment?.issue_id) return;
      invalidateTimeline(comment.issue_id);
      // 新评论会在服务端抬升父议题的 updated_at（MUL-5009），所以任何按"更新时间"
      // 排序的开着的看板/列表都已漂移。只重取这些 key，把被评论的卡片重排到正确位置；
      // 其它排序不受影响。只有 comment:created 抬升 updated_at，所以下面其它评论事件
      // 刻意不做这件事。
      const wsId = getCurrentWsId();
      if (wsId) {
        invalidateUpdatedAtSortedIssueLists(qc, wsId);
        invalidateLastActivitySortedIssueLists(qc, wsId);
        // 一条评论只携带聚合的所有者 revision，不是完整 Issue 快照。把投影标记为过期
        // 以触发权威拉取，又不会让之后某份完整快照显得比缓存旧。
        if (issueRevision) {
          onIssueAuxiliaryRevision(qc, wsId, comment.issue_id, issueRevision);
        } else {
          invalidateIssueOwnerProjections(qc, wsId, comment.issue_id);
        }
      }
    });

    const unsubCommentUpdated = ws.on("comment:updated", (p) => {
      const { comment, issue_revision } = p as CommentUpdatedPayload;
      if (!comment?.issue_id) return;
      invalidateTimeline(comment.issue_id);
      const wsId = getCurrentWsId();
      if (wsId) {
        invalidateLastActivitySortedIssueLists(qc, wsId);
        if (issue_revision) {
          onIssueAuxiliaryRevision(qc, wsId, comment.issue_id, issue_revision);
        } else {
          invalidateIssueOwnerProjections(qc, wsId, comment.issue_id);
        }
      }
    });

    const unsubCommentDeleted = ws.on("comment:deleted", (p) => {
      const { issue_id, issue_revision } = p as CommentDeletedPayload;
      if (!issue_id) return;
      invalidateTimeline(issue_id);
      const wsId = getCurrentWsId();
      if (wsId) {
        invalidateLastActivitySortedIssueLists(qc, wsId);
        if (issue_revision) {
          onIssueAuxiliaryRevision(qc, wsId, issue_id, issue_revision);
        } else {
          invalidateIssueOwnerProjections(qc, wsId, issue_id);
        }
      }
    });

    const unsubCommentResolved = ws.on("comment:resolved", (p) => {
      const { comment } = p as CommentResolvedPayload;
      if (comment?.issue_id) invalidateTimeline(comment.issue_id);
    });

    const unsubCommentUnresolved = ws.on("comment:unresolved", (p) => {
      const { comment } = p as CommentUnresolvedPayload;
      if (comment?.issue_id) invalidateTimeline(comment.issue_id);
    });

    const unsubActivityCreated = ws.on("activity:created", (p) => {
      const { issue_id } = p as ActivityCreatedPayload;
      if (issue_id) invalidateTimeline(issue_id);
    });

    const unsubReactionAdded = ws.on("reaction:added", (p) => {
      const { issue_id } = p as ReactionAddedPayload;
      if (issue_id) invalidateTimeline(issue_id);
    });

    const unsubReactionRemoved = ws.on("reaction:removed", (p) => {
      const { issue_id } = p as ReactionRemovedPayload;
      if (issue_id) invalidateTimeline(issue_id);
    });

    // --- 议题级反应与订阅者（全局兜底） ---

    const unsubIssueReactionAdded = ws.on("issue_reaction:added", (p) => {
      const { issue_id, issue_revision } = p as IssueReactionAddedPayload;
      if (issue_id) {
        qc.invalidateQueries({ queryKey: issueKeys.reactions(issue_id) });
        const wsId = getCurrentWsId();
        if (wsId) onIssueAuxiliaryRevision(qc, wsId, issue_id, issue_revision);
      }
    });

    const unsubIssueReactionRemoved = ws.on("issue_reaction:removed", (p) => {
      const { issue_id, issue_revision } = p as IssueReactionRemovedPayload;
      if (issue_id) {
        qc.invalidateQueries({ queryKey: issueKeys.reactions(issue_id) });
        const wsId = getCurrentWsId();
        if (wsId) onIssueAuxiliaryRevision(qc, wsId, issue_id, issue_revision);
      }
    });

    const unsubSubscriberAdded = ws.on("subscriber:added", (p) => {
      const { issue_id } = p as SubscriberAddedPayload;
      if (issue_id) qc.invalidateQueries({ queryKey: issueKeys.subscribers(issue_id) });
    });

    const unsubSubscriberRemoved = ws.on("subscriber:removed", (p) => {
      const { issue_id } = p as SubscriberRemovedPayload;
      if (issue_id) qc.invalidateQueries({ queryKey: issueKeys.subscribers(issue_id) });
    });

    // --- 副作用 handler（toast、导航） ---

    // 当前工作区消失后（被删或我们被踢出），导航到用户仍可访问的另一个工作区，
    // 或去创建工作区页。用整页导航：这能可靠地拆掉所有绑定在已死工作区上的在途
    // 查询/订阅，而不必在 core 里依赖框架特定的路由器。
    const relocateAfterWorkspaceLoss = async (lostWsId: string) => {
      const wsList = await qc.fetchQuery({
        ...workspaceListOptions(),
        staleTime: 0,
      });
      const remaining = wsList.filter((w) => w.id !== lostWsId);
      const target = resolvePostAuthDestination(
        remaining,
        hasOnboardedRef.current,
      );
      if (typeof window !== "undefined") {
        window.location.assign(target);
      }
    };

    const unsubWsUpdated = ws.on("workspace:updated", (p) => {
      applyWorkspaceUpdatedToCache(qc, p as WorkspaceUpdatedPayload);
      const changedWorkspace=(p as WorkspaceUpdatedPayload)?.workspace?.id;
      if (!changedWorkspace) return;
      void qc.invalidateQueries({queryKey:["workspace-repositories",changedWorkspace]});
      void qc.invalidateQueries({queryKey:["projects",changedWorkspace]});
    });

    const unsubWsDeleted = ws.on("workspace:deleted", (p) => {
      const { workspace_id } = p as WorkspaceDeletedPayload;
      // 自己发起的删除：useDeleteWorkspace 负责存储清理和导航（都在 DELETE 解析后
      // 执行）。这里也响应会与那套流程的导航争抢整页 relocation——这个守卫正是为防
      // 止 CancelledError + reload 组合。本 handler 只服务在别处（其它用户/设备）
      // 发起的删除。
      if (isWorkspaceDeletePending(workspace_id)) return;
      // 事件载荷给的是 UUID；从缓存的工作区列表查 slug——clearWorkspaceStorage 的
      // key 按 slug 命名空间。
      const wsList = qc.getQueryData<{ id: string; slug: string }[]>(workspaceKeys.list()) ?? [];
      const deletedSlug = wsList.find((w) => w.id === workspace_id)?.slug;
      if (deletedSlug) clearWorkspaceStorage(defaultStorage, deletedSlug);
      if (getCurrentWsId() === workspace_id) {
        logger.warn("current workspace deleted, switching");
        onToast?.("This workspace was deleted", "info");
        relocateAfterWorkspaceLoss(workspace_id);
      }
    });

    const unsubMemberRemoved = ws.on("member:removed", (p) => {
      const { user_id } = p as MemberRemovedPayload;
      const myUserId = authStore.getState().user?.id;
      // 自己被移出工作区：清理该工作区的本地存储并导航离开。
      if (user_id === myUserId) {
        const slug = getCurrentSlug();
        const wsId = getCurrentWsId();
        if (slug && wsId) {
          clearWorkspaceStorage(defaultStorage, slug);
          logger.warn("removed from workspace, switching");
          onToast?.("You were removed from this workspace", "info");
          relocateAfterWorkspaceLoss(wsId);
        }
      }
    });

    const unsubMemberAdded = ws.on("member:added", (p) => {
      const { member, workspace_name } = p as MemberAddedPayload;
      const myUserId = authStore.getState().user?.id;
      // 自己加入了工作区：刷新列表/邀请，弹 toast。
      if (member.user_id === myUserId) {
        qc.invalidateQueries({ queryKey: workspaceKeys.list() });
        qc.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
        onToast?.(
          `You joined ${workspace_name ?? "a workspace"}`,
          "info",
        );
      }
    });

    // invitation:created —— 通知被邀请者有一条新的待处理邀请。
    const unsubInvitationCreated = ws.on("invitation:created", (p) => {
      const { workspace_name } = p as InvitationCreatedPayload;
      qc.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
      onToast?.(
        `You were invited to ${workspace_name ?? "a workspace"}`,
        "info",
      );
    });

    // invitation:accepted / declined / revoked — refresh invitation lists.
    // The workspace broadcast reaches every online member, so the admin lists
    // refresh unconditionally. The account-level pending list is gated on the
    // acting user: only the invitee who concluded the invite (possibly from
    // another surface or device) needs their stale pending row dropped —
    // staleTime is Infinity, so nothing refetches it on its own. Without the
    // gate every accept/decline fanout refetches the list once per online
    // member. The actor rides the frame envelope (ws-client hands it to the
    // handler as its second argument), not the event payload.
    const unsubInvitationAccepted = ws.on(
      "invitation:accepted",
      (_payload, actorId) => {
        const currentWsId = getCurrentWsId();
        if (currentWsId) {
          qc.invalidateQueries({ queryKey: workspaceKeys.invitations(currentWsId) });
          qc.invalidateQueries({ queryKey: workspaceKeys.members(currentWsId) });
        }
        if (actorId === authStore.getState().user?.id) {
          qc.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
        }
      },
    );
    const unsubInvitationDeclined = ws.on(
      "invitation:declined",
      (_payload, actorId) => {
        const currentWsId = getCurrentWsId();
        if (currentWsId) {
          qc.invalidateQueries({ queryKey: workspaceKeys.invitations(currentWsId) });
        }
        if (actorId === authStore.getState().user?.id) {
          qc.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
        }
      },
    );
    const unsubInvitationRevoked = ws.on("invitation:revoked", () => {
      qc.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
    });

    // --- Chat / task 事件（全局，ChatWindow 卸载后仍存活） ---
    //
    // 唯一真相来源：Query 缓存。这里没有 Zustand 写入——早先的镜像导致过
    // "失效 → 重取"窗口里缓存与 store 不一致、UI 渲染出重复的竞态。
    //
    // task:message 直接写进 task-messages 缓存，让实时时间线就地更新。
    // chat:message / chat:done / task:completed / task:failed 失效消息与待处理任务，
    // 让数据库保持权威。

    // 有两条守卫站在"工作区级消息消防水管"和渲染器之间（MUL-6396）。
    // `task:message` 会对工作区里每次运行的每个客户端广播，但用户真正打开并渲染的
    // 只是其中少数几场：
    //
    // 1. 帧只为本客户端已持有时间线条目的任务保留——即曾被打开过、且尚未被 GC 的。
    //    旧的 `(old = [])` 默认值会在第一次看见时就建条目，于是每个客户端都会累积
    //    用户永远不会打开的每场运行的记录，包括无界工具输入。
    // 2. 通过该闸门的帧被合并成每窗口一次缓存写，一波爆发只花一次合并和一次渲染
    //    而不是 N 次。
    //
    // 条目可能在两者之间被 GC 掉，所以冲刷时再查一次而不信闸门——见 flushTaskMessages。
    const taskMessageBatches = new Map<string, TaskMessagePayload[]>();
    let taskMessageFlushTimer: ReturnType<typeof setTimeout> | null = null;

    const writeTaskMessageBatch = (taskId: string, batch: TaskMessagePayload[]) => {
      // Re-check, because a queued batch may be up to one window old and
      // `setQueryData` does NOT postpone garbage collection — query-core arms
      // that timer when the last observer leaves and never again on write.
      // Closing a transcript while its run keeps streaming therefore has the
      // entry disappear mid-window, and writing then REBUILDS it holding only
      // this batch. With the app-wide `staleTime: Infinity` the next open
      // would read that stub as fresh and never fetch, so everything before it
      // would be missing until the window is reloaded. Dropping the batch
      // instead costs nothing: the rows are persisted, so the next open fetches
      // the whole timeline.
      if (!isTaskMessageTimelineHeld(qc, taskId)) return;
      qc.setQueryData<TaskMessagePayload[]>(
        chatKeys.taskMessages(taskId),
        (old = []) => mergeTaskMessagesBySeq(old, batch),
      );
    };

    const flushTaskMessages = () => {
      taskMessageFlushTimer = null;

      for (const [taskId, batch] of taskMessageBatches) {
        writeTaskMessageBatch(taskId, batch);
      }
      taskMessageBatches.clear();
    };

    const unsubTaskMessage = ws.on("task:message", (p) => {
      const payload = p as TaskMessagePayload;
      // 廉价的 Map 查找，且在任何分配之前跑——这是工作区里每次运行的热路径，
      // 不只是可见的那些。
      if (!isTaskMessageTimelineHeld(qc, payload.task_id)) return;

      // Leading edge: render the first frame after an idle window now. The
      // timer is still armed so the remainder of a burst is coalesced and a
      // continuous stream cannot render more than once per fixed window after
      // this one immediate write.
      if (!taskMessageFlushTimer) {
        writeTaskMessageBatch(payload.task_id, [payload]);
        taskMessageFlushTimer = setTimeout(flushTaskMessages, TASK_MESSAGE_FLUSH_MS);
      } else {
        const batch = taskMessageBatches.get(payload.task_id);
        if (batch) batch.push(payload);
        else taskMessageBatches.set(payload.task_id, [payload]);
      }

      chatWsLogger.debug("task:message (global)", {
        task_id: payload.task_id,
        seq: payload.seq,
        type: payload.type,
      });
    });

    // 供 chat 生命周期 handler 复用的助手。
    //
    // 安全（PR #5018 / MUL-4159 评审）：chat 的 `task:*` 事件是*工作区扇出*——工作区
    // 每个成员都会收到——且载荷不带创建者/Agent 可见性。所以我们绝不能从这些事件乐观
    // 写跨会话待处理*聚合*（chatKeys.pendingTasks / chatKeys.pendingTasksHasAny）：
    // 成员 B 启动一条 chat 任务会把成员 A 的 FAB 翻成 has_pending=true，
    // 绕过 /api/chat/pending-tasks[/has-any] 的服务端权限过滤。作为替代，我们权威地
    // （去抖后）失效聚合，让它经带过滤的接口重取——那个接口只返回调用者自己的、
    // 可访问的 Agent 的创建者任务。
    //
    // 按会话的 `pendingTask` 缓存仍由下面的 handler 直接写——它以 chat_session_id
    // 为 key，且只为用户有权打开的会话渲染（服务端口），所以不是跨用户聚合泄漏。
    //
    // chat:message 刻意不是触发源（它每条流式消息都发，会重建 MUL-4159 修掉的请求
    // 风暴）；聚合只在任务生命周期转换时刷新，那类事件按任务计、频率低，再由下面
    // 的去抖合并。
    let aggregateRefreshTimer: ReturnType<typeof setTimeout> | null = null;
    const invalidatePendingAggregate = () => {
      if (aggregateRefreshTimer) clearTimeout(aggregateRefreshTimer);
      aggregateRefreshTimer = setTimeout(() => {
        aggregateRefreshTimer = null;
        refetchPendingChatAggregate(qc, getCurrentWsId());
      }, 750);
    };
    const invalidateSessionLists = () => {
      const id = getCurrentWsId();
      if (id) qc.invalidateQueries({ queryKey: chatKeys.sessions(id) });
    };

    const unsubChatMessage = ws.on("chat:message", (p) => {
      const payload = p as ChatMessageEventPayload;
      chatWsLogger.info("chat:message (global)", {
        chat_session_id: payload.chat_session_id,
        role: payload.role,
      });
      // 先写用户这一轮、再失效，让提示词不依赖重取存活（MUL-5711）——与 chat:done
      // 同构。
      applyChatMessageToCache(qc, payload);
      // 注意：刻意不碰待处理聚合。chat:message 每条流式消息都发、且无状态；聚合由
      // 下面任务生命周期 handler 维护（MUL-4159）。
    });

    const unsubChatDone = ws.on("chat:done", (p) => {
      const payload = p as ChatDonePayload;
      chatWsLogger.info("chat:done (global)", {
        task_id: payload.task_id,
        chat_session_id: payload.chat_session_id,
        has_message: !!payload.message_id,
      });
      // 在清待处理任务*之前*把助手消息就地插入消息缓存。两次写入落在同一个 React
      // 渲染拍内，所以 ChatMessageList 会看到 `pendingAlreadyPersisted === true`，
      // 实时 TimelineView 只在 AssistantMessage 挂载后才卸载——没有闪烁窗口。
      // 这应用了 TkDodo 的"setQueryData（活跃查询）+ invalidateQueries（其它）"
      // 模式（https://tkdodo.eu/blog/using-web-sockets-with-react-query）。
      //
      // 服务器省略消息载荷（旧版本）时回退为只失效。旧客户端撞上新服务器同样可用：
      // 它们忽略多余字段、依赖下面那次失效，让旧行为继续存活。
      applyChatDoneToCache(qc, payload);
      // 注意：待处理聚合留给 task:completed / task:failed handler（它们带移除正确
      // 条目所需的 task_id）。chat:done 不再失效它，这样话痨会话不会每轮都重取聚合
      // （MUL-4159）。
      // 助手消息刚落地 → has_unread 可能已翻成 true。
      invalidateSessionLists();
    });

    // daemon 后台建议轮迟到的快捷操作补充——就地修补已结束那轮的消息；无需失效
    // （载荷权威且小）。
    const unsubChatQuickActions = ws.on("chat:quick_actions", (p) => {
      const payload = p as ChatQuickActionsPayload;
      chatWsLogger.info("chat:quick_actions (global)", {
        task_id: payload.task_id,
        chat_session_id: payload.chat_session_id,
        count: payload.quick_actions?.length ?? 0,
      });
      void applyChatQuickActionsToCache(qc, payload);
    });

    // 延迟取消结局（#5219）：服务器只在 daemon 冲刷会话记录后才判定空/非空，所以本
    // 事件在取消的 HTTP 响应之后几秒才到——那时没有别的东西会再拉一次。
    const unsubChatCancelFinalized = ws.on("chat:cancel_finalized", (p) => {
      const payload = p as ChatCancelFinalizedPayload;
      chatWsLogger.info("chat:cancel_finalized (global)", {
        task_id: payload.task_id,
        chat_session_id: payload.chat_session_id,
        outcome: payload.outcome,
      });
      applyChatCancelFinalizedToCache(qc, payload, authStore.getState().user?.id);
      if (payload.outcome === "stopped") {
        // 一条 "Stopped." 助手行刚落地 → 会话预览变了。
        invalidateSessionLists();
      }
    });

    // 生命周期事件是失效提示。它们刻意省略队列预览与消息 id，所以只有待处理任务
    // 接口能写出完整的队列形状。
    const unsubTaskQueued = ws.on("task:queued", (p) => {
      const payload = p as TaskQueuedPayload;
      if (!payload.chat_session_id) return;
      qc.invalidateQueries({ queryKey: chatKeys.pendingTask(payload.chat_session_id) });
      invalidatePendingAggregate();
    });

    // task:dispatch 在 daemon 认领排队任务时发出。daemon 随即 StartTask，
    // 所以 dispatched→running 是亚秒级的。我们把那窗口塌缩成直接写 "running"——
    // 药丸从 "Queued" 直接跳到 "Thinking"，跳过无意义的 "Starting" 帧。
    // TaskStatusPill 的阶段判定把 "running" + 空 taskMessages 映射成 "Thinking · Ns"。
    const unsubTaskDispatch = ws.on("task:dispatch", (p) => {
      const payload = p as TaskDispatchPayload;
      if (!payload.chat_session_id) return;
      qc.setQueryData<ChatPendingTask>(
        chatKeys.pendingTask(payload.chat_session_id),
        (old) => promotePendingChatTask(old, payload.task_id, "running"),
      );
      invalidateChatMessageQueries(qc, payload.chat_session_id);
      qc.invalidateQueries({ queryKey: chatKeys.pendingTask(payload.chat_session_id) });
      invalidatePendingAggregate();
    });

    // task:running 在 daemon 把一条此前停驻的任务（waiting_local_directory）带回运行
    // 阶段时发出。dispatch→running 的路径已在上面的 handler 里塌缩，所以这个 handler
    // 主要存在意义是清除过期的 `waiting_local_directory` 药丸——没有它，即使 daemon
    // 已恢复工作，药丸仍会停驻。
    const unsubTaskRunning = ws.on("task:running", (p) => {
      const payload = p as TaskRunningPayload;
      if (!payload.chat_session_id) return;
      qc.setQueryData<ChatPendingTask>(
        chatKeys.pendingTask(payload.chat_session_id),
        (old) => promotePendingChatTask(old, payload.task_id, "running"),
      );
      invalidateChatMessageQueries(qc, payload.chat_session_id);
      qc.invalidateQueries({ queryKey: chatKeys.pendingTask(payload.chat_session_id) });
      invalidatePendingAggregate();
    });

    // task:waiting_local_directory 在 daemon 出队任务却拿不到 local_directory 路径锁时
    // 发出——本 daemon 上另一任务正在同一目录。写这个状态，让 TaskStatusPill 能渲染
    // "Waiting for local directory" 阶段，而不是钉死一份过期的 "Starting / Thinking"。
    const unsubTaskWaitingLocalDir = ws.on(
      "task:waiting_local_directory",
      (p) => {
        const payload = p as TaskWaitingLocalDirectoryPayload;
        if (!payload.chat_session_id) return;
        qc.setQueryData<ChatPendingTask>(
          chatKeys.pendingTask(payload.chat_session_id),
          (old) =>
            promotePendingChatTask(
              old,
              payload.task_id,
              "waiting_local_directory",
              undefined,
              payload.wait_reason,
            ),
        );
        invalidateChatMessageQueries(qc, payload.chat_session_id);
        qc.invalidateQueries({ queryKey: chatKeys.pendingTask(payload.chat_session_id) });
        invalidatePendingAggregate();
      },
    );

    // task:cancelled 到达我们的情形：
    //   1. handleStop 已在本地面清了缓存（这是无操作确认）
    //   2. 另一标签页 / 管理员 / 系统取消——这些情况下它是唯一能去掉待处理药丸的路径。
    //      没有它，第二标签页场景里药丸会永远转圈。
    // CancelTask 在停止的 chat 任务已经流过会话记录行时，还会持久化一条尽力而为的
    // 助手快照，所以清待处理的同时要刷新消息页。
    const unsubTaskCancelled = ws.on("task:cancelled", (p) => {
      const payload = p as TaskCancelledPayload;
      if (!payload.chat_session_id) return;
      chatWsLogger.info("task:cancelled (global, chat)", {
        task_id: payload.task_id,
        chat_session_id: payload.chat_session_id,
      });
      qc.setQueryData<ChatPendingTask>(
        chatKeys.pendingTask(payload.chat_session_id),
        (old) => removePendingChatTask(old, payload.task_id),
      );
      qc.invalidateQueries({ queryKey: chatKeys.pendingTask(payload.chat_session_id) });
      invalidateChatMessageQueries(qc, payload.chat_session_id);
      invalidatePendingAggregate();
      invalidateSessionLists();
    });

    const unsubTaskCompleted = ws.on("task:completed", (p) => {
      const payload = p as TaskCompletedPayload;
      if (!payload.chat_session_id) return; // issue 任务在别处处理
      chatWsLogger.info("task:completed (global, chat)", {
        task_id: payload.task_id,
        chat_session_id: payload.chat_session_id,
      });
      qc.setQueryData<ChatPendingTask>(
        chatKeys.pendingTask(payload.chat_session_id),
        (old) => removePendingChatTask(old, payload.task_id),
      );
      qc.invalidateQueries({ queryKey: chatKeys.pendingTask(payload.chat_session_id) });
      invalidatePendingAggregate();
    });

    const unsubTaskFailed = ws.on("task:failed", (p) => {
      const payload = p as TaskFailedPayload;
      if (!payload.chat_session_id) return;
      chatWsLogger.warn("task:failed (global, chat)", {
        task_id: payload.task_id,
        chat_session_id: payload.chat_session_id,
      });
      // FailTask 会写一条失败 chat_message（镜像 CompleteTask 的成功消息），所以这条
      // 路径镜像 task:completed handler：清待处理信号 AND 失效消息列表，让失败气泡
      // 不用刷新页面就出现。在 #1823 之前这个分支只翻 pending——那时 "No new message"
      // 的注释还成立，但现在 FailTask 会持久化一行。
      qc.setQueryData<ChatPendingTask>(
        chatKeys.pendingTask(payload.chat_session_id),
        (old) => removePendingChatTask(old, payload.task_id),
      );
      invalidateChatMessageQueries(qc, payload.chat_session_id);
      qc.invalidateQueries({ queryKey: chatKeys.pendingTask(payload.chat_session_id) });
      invalidatePendingAggregate();
      // FailTask 持久化了一条失败 chat_message，所以线程列表的 last_message /
      // unread_count / 排序也变了。镜像 chat:done（成功）路径刷新会话列表——否则
      // 左栏会一直留着过期预览，直到下一次整次重取。
      invalidateSessionLists();
    });

    const unsubChatSessionRead = ws.on("chat:session_read", (p) => {
      const payload = p as { chat_session_id: string };
      chatWsLogger.info("chat:session_read (global)", payload);
      invalidateSessionLists();
    });

    const unsubChatSessionCreated = ws.on("chat:session_created", (p) => {
      const payload = p as ChatSessionCreatedPayload;
      chatWsLogger.info("chat:session_created (global)", payload);
      // 只关心当前工作区的会话创建（跨工作区扇出时过滤）。
      if (payload.workspace_id !== getCurrentWsId()) return;
      invalidateSessionLists();
    });

    // chat:session_updated 在创建者于任一标签页/设备改名、钉住或归档会话后发出。
    // 就地修补缓存行，让下拉与徽标不用整次会话列表重取就反映变化——
    // 为何归档还须清零未读见 applyChatSessionUpdatedToCache。
    const unsubChatSessionUpdated = ws.on("chat:session_updated", (p) => {
      const payload = p as ChatSessionUpdatedPayload;
      chatWsLogger.info("chat:session_updated (global)", payload);
      const id = getCurrentWsId();
      if (!id) return;
      applyChatSessionUpdatedToCache(qc, id, payload);
    });

    // chat:session_deleted 在硬删除后发出。发起标签页已用 useDeleteChatSession 乐观
    // 移除该行；本 handler 让其它标签页/设备保持同步，并清掉活跃会话指针，让被删
    // 会话不会让聊天窗继续指向已经消失的消息。
    const unsubChatSessionDeleted = ws.on("chat:session_deleted", (p) => {
      const payload = p as { chat_session_id: string };
      chatWsLogger.info("chat:session_deleted (global)", payload);
      const id = getCurrentWsId();
      if (id) {
        const drop = (old?: { id: string }[]) =>
          old?.filter((s) => s.id !== payload.chat_session_id);
        qc.setQueryData(chatKeys.sessions(id), drop);
      }
      qc.removeQueries({ queryKey: chatKeys.messages(payload.chat_session_id) });
      qc.removeQueries({ queryKey: chatKeys.pendingTask(payload.chat_session_id) });
      invalidatePendingAggregate();

      const chatState = useChatStore.getState?.();
      if (chatState && chatState.activeSessionId === payload.chat_session_id) {
        chatState.setActiveSession(null);
      }
    });

    return () => {
      unsubAny();
      unsubIssueUpdated();
      unsubIssueCreated();
      unsubIssueDeleted();
      unsubIssueAttachmentsChanged();
      unsubIssueLabelsChanged();
      unsubIssueMetadataChanged();
      unsubIssuePropertiesChanged();
      unsubPropertyChanged.forEach((unsub) => unsub());
      unsubInboxNew();
      unsubCommentCreated();
      unsubCommentUpdated();
      unsubCommentDeleted();
      unsubCommentResolved();
      unsubCommentUnresolved();
      unsubActivityCreated();
      unsubReactionAdded();
      unsubReactionRemoved();
      unsubIssueReactionAdded();
      unsubIssueReactionRemoved();
      unsubSubscriberAdded();
      unsubSubscriberRemoved();
      unsubWsUpdated();
      unsubWsDeleted();
      unsubMemberRemoved();
      unsubMemberAdded();
      unsubInvitationCreated();
      unsubInvitationAccepted();
      unsubInvitationDeclined();
      unsubInvitationRevoked();
      unsubTaskMessage();
      unsubChatMessage();
      unsubChatDone();
      unsubChatQuickActions();
      unsubChatCancelFinalized();
      unsubTaskQueued();
      unsubTaskDispatch();
      unsubTaskRunning();
      unsubTaskWaitingLocalDir();
      unsubTaskCancelled();
      unsubTaskCompleted();
      unsubTaskFailed();
      unsubChatSessionRead();
      unsubChatSessionCreated();
      unsubChatSessionDeleted();
      unsubChatSessionUpdated();
      if (taskMessageFlushTimer) clearTimeout(taskMessageFlushTimer);
      if (aggregateRefreshTimer) clearTimeout(aggregateRefreshTimer);
      timers.forEach(clearTimeout);
      timers.clear();
    };
  }, [ws, qc, authStore, onToast]);

  // 重连 → 重取所有数据以找回错过的事件。
  useEffect(() => {
    if (!ws) return;

    const unsub = ws.onReconnect(async () => {
      logger.info("reconnected, refetching all data");
      try {
        invalidateWorkspaceScopedQueries(qc);
      } catch (e) {
        logger.error("reconnect refetch failed", e);
      }
    });

    return unsub;
  }, [ws, qc]);

  // 新 WSClient 实例（工作区切换）→ 失效按工作区作用域的查询，找回前一实例被拆掉
  // 期间错过的事件。跳过首次赋值，避免首次挂载多一次冗余重取。
  const wsInstanceRef = useRef<WSClient | null>(null);
  useEffect(() => {
    if (!ws) return;
    if (wsInstanceRef.current === null) {
      // 首个非空实例——记下并跳过失效。
      wsInstanceRef.current = ws;
      return;
    }
    if (wsInstanceRef.current === ws) return;
    wsInstanceRef.current = ws;

    logger.info("new WSClient instance detected, invalidating workspace queries");
    invalidateWorkspaceScopedQueries(qc);
  }, [ws, qc]);
}
