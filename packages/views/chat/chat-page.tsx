// 聊天页：以“双栏”形式展示会话列表（左）与对话内容（右），是本项目聊天功能的主要入口页面。
"use client";

import { useEffect, useRef, useState } from "react";
import { useDefaultLayout } from "react-resizable-panels";
import { ArrowLeft, MessageSquare } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import {
  ResizablePanelGroup,
  ResizablePanel,
  ResizableHandle,
} from "@multica/ui/components/ui/resizable";
import { useIsCompact } from "@multica/ui/hooks/use-mobile";
import { useRequiredWorkspaceSlug, useWorkspacePaths } from "@multica/core/paths";
import { getCurrentSlug } from "@multica/core/platform";
import { useChatStore } from "@multica/core/chat";
import { chatQuickActionsPendingOptions } from "@multica/core/chat/queries";
import { useRegenerateChatQuickActions } from "@multica/core/chat/mutations";
import { useQuickActionsPendingTimeout } from "@multica/core/chat/use-quick-actions-pending-timeout";
import { useQuickActionsFailureToast } from "./components/use-quick-actions-failure-toast";
import { useQuery } from "@tanstack/react-query";
import type { Agent, ChatSession } from "@multica/core/types";
import { PageHeader } from "../layout/page-header";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";
import { ChatMessageList, ChatMessageSkeleton } from "./components/chat-message-list";
import { ChatInput } from "./components/chat-input";
import { ChatQueue } from "./components/chat-queue";
import { ChatThreadList } from "./components/chat-thread-list";
import { ChatSessionHeader } from "./components/chat-session-header";
import { EmptyState } from "./components/chat-empty-state";
import { NewChatButton } from "./components/new-chat-button";
import { useChatController } from "./components/use-chat-controller";
import { OfflineBanner } from "./components/offline-banner";
import { NoAgentBanner } from "./components/no-agent-banner";
import { ArchivedAgentBanner } from "./components/archived-agent-banner";
import { AgentAccessRevokedBanner } from "./components/agent-access-revoked-banner";
import { RuntimeRequiredBanner } from "./components/runtime-required-banner";

/**
 * 聊天标签页：首选的双栏界面（左侧会话列表、右侧对话内容），布局与 Inbox 页一致。
 * 通过 `useChatController` 与悬浮聊天窗口（FAB）共享同一套对话逻辑；左侧栏复用 `ChatThreadList`。
 *
 * 会话选中状态可以通过 URL 的 `?session=<id>` 定位，因此会话可以被深链接、从通知打开，
 * 并在刷新后保持。聊天 store 里的 `activeSessionId` 是唯一数据源（两个界面都读它）；
 * URL 与 store 之间保持双向同步。`?agent=<id>` 是另一个一次性的“新建聊天”深链接：
 * 它会打开一个绑定到该 agent 的空白输入框，随后该参数会从 URL 中移除。
 *
 * 开始聊天时才会选择 agent：头部 ⊕ 按钮打开 agent 选择器（见 NewChatButton），
 * 因此输入框不再需要自己的 agent 选择器。与 FAB 不同，本页面不向 `ChatInput`
 * 传 `contextItems`，所以它的 `@` 提及会退化为手动搜索（类似 issue 评论的写法）。
 */
export function ChatPage() {
  const { t } = useT("chat");
  const { pathname, searchParams, replace } = useNavigation();
  const workspaceSlug = useRequiredWorkspaceSlug();
  const wsPaths = useWorkspacePaths();
  // App Router can retain this page after navigation. Once the URL belongs
  // to another route, this instance must stop reconciling shared chat state.
  // Also check the live workspace mirror in each effect: the incoming layout
  // can rehydrate the store before this page observes the destination URL.
  const isCurrentChatRoute = pathname === wsPaths.chat();
  const isCompact = useIsCompact();

  const c = useChatController({
    isActive: isCurrentChatRoute && getCurrentSlug() === workspaceSlug,
  });
  const { data: quickActionsPending = null } = useQuery(
    chatQuickActionsPendingOptions(c.activeSessionId ?? ""),
  );
  // 丢弃卡住的“待处理”标记（daemon 挂了或补充生成失败），让 pill 上的转圈停下，
  // 这样下一次刷新时能从干净状态开始（MUL-5149）。
  useQuickActionsPendingTimeout(c.activeSessionId ?? null, quickActionsPending);
  // 当已接受的刷新后来在 daemon 里失败时弹出 toast（异步的另一半）。
  useQuickActionsFailureToast(c.activeSessionId ?? null);
  const regenerateQuickActions = useRegenerateChatQuickActions();
  const urlSession = searchParams.get("session") || null;
  const urlAgent = searchParams.get("agent") || null;

  // “正在新建一个全新的聊天”——用户点了 ⊕ 但还没发送，所以还不存在会话。
  // 在窄屏下这个状态决定展示列表还是对话；在桌面端对话面板始终挂载，
  // 所以它只需要在真正的会话接管时重置自己一次。
  const [composingNew, setComposingNew] = useState(false);
  useEffect(() => {
    // 读取 store 的“实时”值，原因和下面的 session 同步 effect 相同：在 StrictMode
    // 的双重调用下，这个 effect 会用渲染时捕获的快照重新执行，而一个过期的非空 session
    // （URL→store effect 已经清掉的持久化聊天）会把稍后 `?agent=` 意图 effect 刚设置好的
    // composingNew=true 给改回去。
    if (useChatStore.getState().activeSessionId) setComposingNew(false);
  }, [c.activeSessionId]);

  // URL（`?session=`）与聊天 store 的 activeSessionId 之间的双向同步。
  // 两个 effect 都通过 `useChatStore.getState()` 读取 store 的“实时”值，
  // 而不是渲染时捕获的 `c.activeSessionId`。正是这一点让它们不会在挂载时互相打架：
  // 一个天真的镜像 effect 会拿着过期的（null）快照执行，在 URL→store effect 生效前
  // 就把 session 从 URL 上剥掉“纠正”回去——于是深链接失效、选中 / 新建聊天显得没反应。
  // 读 getState() 能看到兄弟 effect 刚写入的值，所以同步只需一轮就能收敛，
  // 并且在 StrictMode 双重调用下也是幂等的。

  // URL → store：深链接、刷新、通知点击、前进/后退。
  useEffect(() => {
    if (!isCurrentChatRoute || getCurrentSlug() !== workspaceSlug) return;
    if (urlSession !== useChatStore.getState().activeSessionId) {
      c.setActiveSession(urlSession);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- reconcile URL/route changes, not store updates
  }, [isCurrentChatRoute, workspaceSlug, urlSession]);

  // store → URL：选中会话、“新建聊天”、以及发送后创建的会话。
  useEffect(() => {
    if (!isCurrentChatRoute || getCurrentSlug() !== workspaceSlug) return;
    const live = useChatStore.getState().activeSessionId;
    const current = searchParams.get("session") || null;
    if (live !== current) {
      const base = wsPaths.chat();
      replace(live ? `${base}?session=${live}` : base);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- reconcile store/route changes, not URL updates
  }, [isCurrentChatRoute, workspaceSlug, c.activeSessionId]);

  const { defaultLayout, onLayoutChanged } = useDefaultLayout({
    id: "multica_chat_layout",
  });

  // `?agent=` 意图的记账逻辑。ref 保存已经消费（或已被取代）的参数值，
  // 这样下面的 effect 每个深链接最多只触发一次——它也填补了 replace() 和
  // searchParams 真正丢掉参数之间的异步窗口。任何显式的用户操作都必须覆盖一个
  // 仍然待处理的意图：agent/member 查询可能很晚才返回，如果一个被推迟的意图
  // 在用户选中会话（或开了另一个聊天）之后才触发，就会把用户的选择给冲掉。
  const consumedAgentIntent = useRef<string | null>(null);
  const supersedeAgentIntent = () => {
    if (urlAgent) consumedAgentIntent.current = urlAgent;
  };

  const handleSelect = (session: ChatSession) => {
    supersedeAgentIntent();
    c.handleSelectSession(session);
    setComposingNew(false);
  };

  // 两个入口（会话列表行 + 对话头部）共用的单一归档路径。当被归档的聊天正是当前
  // 正在看的那个时，把面板从它身上移开：桌面端前进到下一个聊天（Inbox 风格）；
  // 窄屏下退回到列表，这比被扔进一个毫不相关的全屏对话更自然。归档其他聊天则保持视图不动。
  const handleArchive = (session: ChatSession) => {
    supersedeAgentIntent();
    if (session.id === c.activeSessionId) {
      if (isCompact) {
        c.setActiveSession(null);
        setComposingNew(false);
      } else {
        c.advanceSelectionAfterArchive(session);
      }
    }
    c.archiveSession(session.id);
  };

  const startNewChat = (agent: Agent | null) => {
    // 手动点 ⊕ 的选择优先级高于待处理的深链接；当从意图 effect 内部调用时，
    // ref 已经被设置成这个参数，所以这里相当于空操作。
    supersedeAgentIntent();
    if (agent) c.handleStartNewChat(agent);
    else c.handleNewChat();
    setComposingNew(true);
  };

  const changeProjectContext = (projectId: string | null) => {
    if (projectId === c.activeProjectId) return;
    c.handleProjectChange(projectId);
    // 移除项目后仍停留在当前对话。为已有对话选择一个项目会开启一个全新的会话，
    // 而在 activeSessionId 被清空后，窄屏布局必须停留在输入面板上。
    if (!c.currentSession || projectId !== null) setComposingNew(true);
  };

  // URL → 新建聊天：`?agent=<id>` 是“私聊”入口（例如 agent 详情页）使用的深链接，
  // 用来落在一个绑定到该 agent 的空白输入框上。经过权限过滤的 agent 列表是异步加载的，
  // 所以意图在 agent 解析出来的那次渲染中被消费，随后参数被剥离，这样刷新 / 上面的
  // session 同步不会重复触发它。参数消失后 ref 会重置，于是稍后一个相同的深链接又能再次触发。
  // 一次已确定的未命中（权限被撤销、agent 已归档、id 非法）属于拒绝：它用 toast 说明原因，
  // 并消费掉意图，这样之后一次重新拉取如果又出现了该 agent，也不会在没有新点击的情况下开启聊天。
  // 当查询仍在加载时，意图就简单地保持待处理。
  useEffect(() => {
    if (!isCurrentChatRoute || getCurrentSlug() !== workspaceSlug) return;
    if (!urlAgent) {
      consumedAgentIntent.current = null;
      return;
    }
    if (consumedAgentIntent.current === urlAgent) return;
    const agent = c.availableAgents.find((a) => a.id === urlAgent);
    if (agent) {
      consumedAgentIntent.current = urlAgent;
      startNewChat(agent);
      replace(wsPaths.chat());
      return;
    }
    if (c.agentsSettled) {
      consumedAgentIntent.current = urlAgent;
      toast.error(t(($) => $.page.agent_link_no_access));
      replace(wsPaths.chat());
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- consume when the URL param or the resolving agent list changes
  }, [isCurrentChatRoute, workspaceSlug, urlAgent, c.availableAgents, c.agentsSettled]);

  const newChatButton = (
    <NewChatButton
      agents={c.availableAgents}
      userId={c.user?.id}
      onStart={startNewChat}
      side="bottom"
    />
  );

  const listHeader = (
    <PageHeader>
      <h1 className="flex-1 text-body font-semibold">{t(($) => $.page.title)}</h1>
      {newChatButton}
    </PageHeader>
  );

  const listBody = (
    <div className="px-2 py-1">
      <ChatThreadList
        sessions={c.sessions}
        agents={c.agents}
        activeSessionId={c.activeSessionId}
        onSelectSession={handleSelect}
        onArchive={handleArchive}
      />
    </div>
  );

  // 对话面板：消息列表 / 骨架屏 / 空状态，位于持久的横幅 + 输入框之上。
  // 结构与悬浮窗口的主体完全相同，所以一个全新的聊天（没有活动会话）会显示
  // 感知 agent 的空状态 + 输入框。输入框没有 agent 选择器——agent 在聊天开始时就已经固定。
  // `@container`：对话栏的留白（CHAT_GUTTER）会随“这个”面板一起变宽，
  // 而该面板是用户独立于浏览器窗口来调整大小的。
  const queuedTasks = c.pendingTask?.queued_tasks ?? [];
  const conversation = (
    <div className="flex flex-1 flex-col min-h-0 @container">
      {c.currentSession && (
        <ChatSessionHeader
          session={c.currentSession}
          agent={c.activeAgent}
          onArchive={handleArchive}
        />
      )}
      {c.showSkeleton ? (
        <ChatMessageSkeleton />
      ) : c.hasMessages ? (
        <ChatMessageList
          key={c.activeSessionId}
          messages={c.messages}
          pendingTask={c.pendingTask}
          availability={c.availability}
          firstItemIndex={c.firstItemIndex}
          hasOlderMessages={c.hasOlderMessages}
          isFetchingOlderMessages={c.isFetchingOlderMessages}
          onLoadOlderMessages={() => void c.fetchOlderMessages()}
          onQuickAction={(action) => c.handleSend(action.prompt)}
          quickActionsDisabled={
            !!c.pendingTaskId ||
            c.isSessionArchived ||
            c.isAgentArchived ||
            c.isAgentAccessRevoked ||
            !c.isAgentRuntimeBound ||
            c.noAgent
          }
          onRegenerateQuickActions={(message) =>
            c.activeSessionId
              ? regenerateQuickActions.mutateAsync({
                  sessionId: c.activeSessionId,
                  messageId: message.id,
                })
              : undefined
          }
          quickActionsPendingMessageId={quickActionsPending?.message_id ?? null}
        />
      ) : (
        <EmptyState
          agent={c.activeAgent}
          hasSessions={c.sessions.length > 0}
          onPickPrompt={c.prefillConversationStarter}
          customizeHref={c.customizeConversationStartersHref}
        />
      )}

      {c.isAgentAccessRevoked ? (
        <AgentAccessRevokedBanner agentName={c.activeAgent?.name} />
      ) : c.noAgent ? (
        <NoAgentBanner />
      ) : c.isAgentArchived ? (
        <ArchivedAgentBanner agentName={c.activeAgent?.name} />
      ) : !c.isAgentRuntimeBound && c.activeAgent ? (
        <RuntimeRequiredBanner
          agentId={c.activeAgent.id}
          agentName={c.activeAgent.name}
        />
      ) : (
        <OfflineBanner agentName={c.activeAgent?.name} availability={c.availability} />
      )}

      <ChatQueue
        tasks={queuedTasks}
        headStatus={c.pendingTask?.status}
        onSendNow={c.handleSendQueuedTaskNow}
        sendNowDisabled={c.isAgentAccessRevoked}
        onEdit={c.handleEditQueuedTask}
        onRemove={c.handleRemoveQueuedTask}
        onClear={c.handleClearQueuedTasks}
      />

      <ChatInput
        onSend={c.handleSend}
        restoreDraftRequest={c.restoreDraftRequest}
        conversationStarterRequest={c.conversationStarterRequest}
        onConversationStarterApplied={c.handleConversationStarterApplied}
        onRestoreDraftApplied={c.handleRestoreDraftApplied}
        uploadEnabled={c.uploadEnabled && !c.isAgentAccessRevoked}
        onStop={c.handleStop}
        isRunning={!!c.pendingTaskId}
        allowSubmitWhileRunning={c.pendingTask?.supports_queue === true}
        disabled={
          c.isSessionArchived ||
          c.isAgentArchived ||
          c.isAgentAccessRevoked ||
          !c.isAgentRuntimeBound
        }
        noAgent={c.noAgent}
        agentArchived={c.isAgentArchived}
        agentAccessRevoked={c.isAgentAccessRevoked}
        agentRuntimeRequired={!c.isAgentRuntimeBound}
        agentName={c.activeAgent?.name}
        projects={c.projects}
        projectId={c.activeProjectId}
        projectContextUnsupported={c.projectContextUnsupported}
        onProjectChange={changeProjectContext}
        isProjectUpdating={c.isProjectUpdating}
        focusRequest={c.focusInputRequest}
      />
    </div>
  );

  // -- 窄屏：列表 / 对话 切换 -------------------------------------------------
  if (isCompact) {
    if (c.activeSessionId || composingNew) {
      return (
        <div className="flex flex-1 flex-col min-h-0">
          <div className="flex h-12 shrink-0 items-center border-b px-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                c.setActiveSession(null);
                setComposingNew(false);
              }}
              className="gap-1.5 text-muted-foreground"
            >
              <ArrowLeft className="h-4 w-4" />
              {t(($) => $.page.title)}
            </Button>
          </div>
          {conversation}
        </div>
      );
    }
    return (
      <div className="flex flex-1 flex-col min-h-0">
        {listHeader}
        <div className="flex-1 min-h-0 overflow-y-auto">{listBody}</div>
      </div>
    );
  }

  // -- 桌面端：可调整大小的双栏。对话面板只在存在聊天目标时才出现——
  // 一个已打开的会话，或一个刚通过 ⊕ 选好 agent 的新聊天。什么都没选中时就没有 agent，
  // 所以我们显示一个中性的提示，而不是一个孤零零的输入框。 -------------------------------
  const hasTarget = !!c.activeSessionId || composingNew;
  return (
    <ResizablePanelGroup
      orientation="horizontal"
      className="flex-1 min-h-0"
      defaultLayout={defaultLayout}
      onLayoutChanged={onLayoutChanged}
    >
      <ResizablePanel
        id="list"
        defaultSize={320}
        minSize={240}
        maxSize={480}
        groupResizeBehavior="preserve-pixel-size"
      >
        <div className="flex flex-col border-r h-full">
          {listHeader}
          <div className="flex-1 min-h-0 overflow-y-auto">{listBody}</div>
        </div>
      </ResizablePanel>
      <ResizableHandle />
      <ResizablePanel id="detail" minSize="40%">
        <div className="flex flex-col min-h-0 h-full">
          {hasTarget ? (
            conversation
          ) : (
            <div className="flex h-full flex-col items-center justify-center gap-3 text-muted-foreground">
              <MessageSquare className="h-10 w-10 text-faint-foreground" />
              <p className="text-body">{t(($) => $.page.select_prompt)}</p>
            </div>
          )}
        </div>
      </ResizablePanel>
    </ResizablePanelGroup>
  );
}
