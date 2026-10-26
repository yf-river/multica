// 聊天消息列表：Virtuoso 虚拟化的对话正文。每条消息一行，assistant 回合同时支持
// "实时流式 timeline"与"持久化后的气泡"两种形态（同一行 key 原地交接），附带
// 开场建议 chips / 新手引导卡片 / 状态 pill / 吸底滚动等。
"use client";

import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { useQuery } from "@tanstack/react-query";
import { Virtuoso, type Components, type VirtuosoHandle } from "react-virtuoso";
import { cn } from "@multica/ui/lib/utils";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Button } from "@multica/ui/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@multica/ui/components/ui/collapsible";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from "@multica/ui/components/ui/tooltip";
import {
  ChevronRight,
  ChevronDown,
  Brain,
  AlertCircle,
  AlertTriangle,
  ArrowUpRight,
  Copy,
  RotateCw,
} from "lucide-react";
import { useScrollFade } from "@multica/ui/hooks/use-scroll-fade";
import { isTaskMessageTaskId, taskMessagesOptions } from "@multica/core/chat/queries";
import { RichContent } from "../../rich-content";
import { RichContentScrollRootProvider } from "../../rich-content/scroll-root";
import { copyText } from "@multica/ui/lib/clipboard";
import { AttachmentList } from "../../issues/components/comment-card";
import { ImageSequenceProvider } from "../../editor";
import { collectImageSequence } from "@multica/core/attachments/image-sequence";
import type { AgentAvailability } from "@multica/core/agents";
import { resolveFailureReasonKey } from "@multica/core/agents";
import type {
  ChatMessage,
  ChatPendingTask,
  ChatQuickAction,
  TaskMessagePayload,
} from "@multica/core/types";
import type { ChatTimelineItem } from "@multica/core/chat";
import { buildTimeline } from "../../common/task-transcript";
import { traceToolArgSummary } from "../../common/task-transcript/trace-event-presenter";
import { TaskInteractionPanel } from "../../common/task-interaction-panel";
import { RuntimeInputPanel } from "../../common/runtime-input-panel";
import { TaskStatusPill } from "./task-status-pill";
import { CHAT_COLUMN, CHAT_GUTTER } from "./chat-column";
import { FOLLOW_EDGE_THRESHOLD } from "../../common/task-transcript/transcript-follow";
import { LIVE_END_ROW_ATTR, useStickToBottom } from "./stick-to-bottom";
import { formatElapsedMs } from "../lib/format";
import { splitTimeline, extractCopyText } from "../lib/copy-text";
import { stripChatQuickActionsProtocol } from "../lib/quick-actions";
import { useT } from "../../i18n";

// ─── 公开组件 ────────────────────────────────────────────────────────────

interface ChatMessageListProps {
  /** Pause bottom-stick effects while a closing floating window fades out. */
  active?: boolean;
  messages: ChatMessage[];
  /**
   * 服务端权威的 pending 任务快照。`null` / undefined 表示没有在途任务——
   * 列表不渲染 StatusPill。
   */
  pendingTask: ChatPendingTask | null | undefined;
  /** 已解析的在线状态；加载中传 `undefined` 保持 pill 文案中性。 */
  availability: AgentAvailability | undefined;
  firstItemIndex?: number;
  hasOlderMessages?: boolean;
  isFetchingOlderMessages?: boolean;
  onLoadOlderMessages?: () => void;
  /** 渲染/复制前，对内嵌聊天协议文本做一次转换（清掉 quick-actions 协议页脚等）。 */
  transformContent?: (content: string) => string;
  /** 发送某条助手"跟进建议"背后隐藏的完整提示词。 */
  onQuickAction?: (action: ChatQuickAction) => void | Promise<unknown>;
  quickActionsDisabled?: boolean;
  /**
   * 为会话最新一条助手回合重新生成跟进建议（"刷新"入口，MUL-5149）。
   * 只在该回合上提供——重新生成会续接最新的 provider 状态，所以旧回合的
   * pills 不能就地刷新。
   */
  onRegenerateQuickActions?: (message: ChatMessage) => void | Promise<unknown>;
  /**
   * 正在等待 quick-actions 补充的消息（chat:done 或一次刷新标起的纯客户端标记）——
   * 在该条回复下渲染 pill 骨架，直到 chat:quick_actions 解决它。
   */
  quickActionsPendingMessageId?: string | null;
}

// ─── Virtuoso 外壳 ───────────────────────────────────────────────────────
//
// Header/Footer 必须是稳定的组件引用（模块级），绝不能是 `components` prop 里
// 的行内箭头函数：行内的 `components={{ Footer: () => … }}` 每次渲染都会新建一个
// 组件*类型*，React 于是每次卸载再重挂整棵 Header/Footer 子树。任务流式输出期间，
// 这会让整条实时 timeline——每一行、每一次 Markdown 解析——在每条 `task:message`
// 事件上都拆掉重建，冻结渲染器数秒（MUL-3960）。每次渲染的数据改走 Virtuoso 的
// `context` prop，以普通 prop 形式到达这些组件（重渲染，而非重挂载）。

interface ChatListContext {
  active: boolean;
  isFetchingOlderMessages: boolean;
  showStatusPill: boolean;
  pendingTask: ChatPendingTask | null | undefined;
  liveTaskMessages: readonly TaskMessagePayload[] | undefined;
  availability: AgentAvailability | undefined;
}

/**
 * 一行 Virtuoso 项。实时（仍在流式输出）任务与它持久化后变成的 assistant 消息
 * 共用一个 key——`task:<taskId>`——所以交接是用新数据原地替换这一项，
 * 而不是卸载 Footer 子树再挂一个不同的行（MUL-4922）。正是这个身份让已渲染的
 * Mermaid 图或 HTML iframe 在任务完成后仍保持挂载。
 */
type ChatRenderItem =
  | { key: string; kind: "message"; message: ChatMessage; taskId: string | null }
  | { key: string; kind: "live"; taskId: string };

/**
 * 持久化消息的行 key。带 task_id 的 assistant 回合按任务键，以便继承实时行；
 * 其它按自己的 id。
 */
function messageRowKey(message: ChatMessage): string {
  return message.role === "assistant" && message.task_id
    ? `task:${message.task_id}`
    : message.id;
}

function ChatListHeader({ context }: { context?: ChatListContext }) {
  const { t } = useT("chat");
  return (
    <div className={cn(CHAT_COLUMN, "pt-4")}>
      {context?.isFetchingOlderMessages && (
        <div className="text-center text-caption text-muted-foreground">
          {t(($) => $.message_list.loading_older)}
        </div>
      )}
    </div>
  );
}

// Footer 承载任务状态和待确认事项；实时 timeline 使用独立行身份（见 ChatRenderItem）。
//
// 容器总是渲染（即使没有 pill），让列表保持恒定的底部内缩：没有它，最后一行的
// py-2 会是最终回复（及它的跟进 pills）与输入框之间唯一的间隔。
function ChatListFooter({ context }: { context?: ChatListContext }) {
  return (
    <div className={cn(CHAT_COLUMN, "pb-4 space-y-4")}>
      {context?.pendingTask?.status === "running" && isTaskMessageTaskId(context.pendingTask.task_id) && (
        <TaskInteractionPanel taskId={context.pendingTask.task_id} active={context.active} />
      )}
      {context?.pendingTask?.status === "running" && isTaskMessageTaskId(context.pendingTask.task_id) && <RuntimeInputPanel taskId={context.pendingTask.task_id} active={context.active} />}
      {context?.showStatusPill && context.pendingTask ? (
        <TaskStatusPill
          pendingTask={context.pendingTask}
          taskMessages={context.liveTaskMessages ?? []}
          availability={context.availability}
        />
      ) : null}
    </div>
  );
}

const LIST_COMPONENTS: Components<ChatRenderItem, ChatListContext> = {
  Header: ChatListHeader,
  Footer: ChatListFooter,
};

/**
 * 消息列表主体：虚拟化渲染会话消息。滚动吸底、翻旧分页、状态 pill、图片序列、
 * 实时/持久化 timeline 的原地交接都在这里编排。详见 Props 各字段。
 */
export function ChatMessageList({
  active = true,
  messages,
  pendingTask,
  availability,
  firstItemIndex = 0,
  hasOlderMessages = false,
  isFetchingOlderMessages = false,
  onLoadOlderMessages,
  transformContent,
  onQuickAction,
  quickActionsDisabled = false,
  onRegenerateQuickActions,
  quickActionsPendingMessageId = null,
}: ChatMessageListProps) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const [scrollContainerEl, setScrollContainerEl] = useState<HTMLDivElement | null>(null);
  const setScrollContainerRef = useCallback((node: HTMLDivElement | null) => {
    scrollRef.current = node;
    setScrollContainerEl(node);
  }, []);
  const virtuosoRef = useRef<VirtuosoHandle>(null);
  // 吸底纠正是经 Virtuoso 完成的，绝不直接在容器上写 `scrollTop`：`scrollHeight`
  // 对未渲染的行是估算，所以像素级的"底部"会随 Virtuoso 的测量移动
  // （见 stick-to-bottom.ts）。
  const pinToLiveEnd = useCallback(() => {
    virtuosoRef.current?.scrollToIndex({ index: "LAST", align: "end" });
  }, []);
  const { isFollowing, onContentHeightChanged, hasReachedLiveEnd } = useStickToBottom(
    scrollContainerEl,
    pinToLiveEnd,
    active,
  );
  // 上下沿的柔和渐隐提示更多内容。保持很小，几乎不碰边缘的整幅预览
  // （图片 / HTML）。
  const fadeStyle = useScrollFade(scrollRef, 16);

  const pendingTaskId = pendingTask?.task_id ?? null;

  // 会话最新一条助手回合——唯一能刷新快捷操作的那条（重新生成会续接最新的
  // provider 状态）。基于持久化列表计算，让入口跟着真实尾部走，而不是实时行。
  const latestAssistantMessageId = useMemo(() => {
    for (let i = messages.length - 1; i >= 0; i--) {
      const m = messages[i];
      if (m && m.role === "assistant" && m.task_id) return m.id;
    }
    return null;
  }, [messages]);

  // 一旦这条 pending 任务的 assistant 消息落进了消息列表，渲染就交给
  // AssistantMessage——抑制实时 timeline（和 pill），避免在 invalidate → refetch
  // 窗口里同一份内容渲染两遍。
  const pendingAlreadyPersisted = !!pendingTaskId && messages.some(
    (m) => m.role === "assistant" && m.task_id === pendingTaskId,
  );

  // 在途任务的实时 timeline。useRealtimeSync 通过 task:message 事件上的
  // setQueryData 保持这个缓存最新。这里只用来决定实时行是否存在、以及喂状态
  // pill——行本身经 AssistantMessage 读同一条缓存条目。
  const showLiveTimeline = !!pendingTaskId && !pendingAlreadyPersisted;
  const canFetchLiveTimeline = isTaskMessageTaskId(pendingTaskId) && !pendingAlreadyPersisted;
  const { data: liveTaskMessages } = useQuery({
    ...taskMessagesOptions(pendingTaskId ?? ""),
    enabled: canFetchLiveTimeline,
  });
  const hasLive = showLiveTimeline && (liveTaskMessages?.length ?? 0) > 0;
  const showStatusPill = !!pendingTaskId && !pendingAlreadyPersisted && !!pendingTask;

  // 持久化消息加上——任务在途时——它的一条合成尾部行。assistant 消息持久化后，
  // `hasLive` 变 false，消息以 SAME key、SAME 位置原地换入——是数据交换，不是
  // 重挂载。新手引导 kickoff 是服务端为 Mika 第一个任务写的载体，不是成员敲的，
  // 所以它永远不会变成可见气泡。
  const renderItems: ChatRenderItem[] = useMemo(() => {
    const items: ChatRenderItem[] = messages
      .filter((message) => message.message_kind !== "onboarding_kickoff")
      .map((message) => ({
        key: messageRowKey(message),
        kind: "message" as const,
        message,
        taskId: message.task_id ?? null,
      }));
    if (hasLive && pendingTaskId) {
      items.push({ key: `task:${pendingTaskId}`, kind: "live", taskId: pendingTaskId });
    }
    return items;
  }, [messages, hasLive, pendingTaskId]);

  const firstIndex = renderItems.length > 0 ? firstItemIndex : 0;
  const liveEndKey = renderItems[renderItems.length - 1]?.key ?? null;

  const listContext: ChatListContext = {
    active,
    isFetchingOlderMessages,
    showStatusPill,
    pendingTask,
    liveTaskMessages,
    availability,
  };

  // 本会话里按消息顺序排列的每张图片，打开一张就能让读者翻完其余（MUL-5752）。
  // 基于消息数据构建，而不是 Virtuoso 当前挂载的内容。
  //
  // 只算持久化消息：任务 transcript 自己的附件活在另一条查询后面、它的块默认
  // 折叠，所以那里的图片保留独立的预览，不进入读者看不见其余部分的序列。
  const imageSequence = useMemo(
    () =>
      collectImageSequence(
        messages.map((message) => ({
          content: message.content,
          attachments: message.attachments,
        })),
      ),
    [messages],
  );

  return (
    <ImageSequenceProvider items={imageSequence}>
    <div
      ref={setScrollContainerRef}
      data-tab-scroll-root
      style={fadeStyle}
      // gutter 放在滚动容器上，这样整份列表只应用一次——行、header、footer——
      // 滚动条也仍贴着表面边缘，而不是和文字一起被内缩。
      // Virtuoso 真正落到最新消息之前保持隐藏。那个窗口里容器本来也不画任何
      // 东西——行还没被测量——所以这不花可见时间，还能让读者少看一帧错误的
      // 消息（见 stick-to-bottom.ts）。
      className={cn("flex-1 overflow-y-auto", CHAT_GUTTER, !hasReachedLiveEnd && "invisible")}
    >
      {/* 已经在 gutter + column 内，所以这个挂载前帧渲染骨架 BODY 而不是
       *  <ChatMessageSkeleton>——后者自带外壳，用于作为列表的独立兄弟节点。 */}
      {!scrollContainerEl ? (
        <div className={cn(CHAT_COLUMN, "pt-4")}>
          <ChatSkeletonBody />
        </div>
      ) : (
      // 聊天在自己的元素里滚动，所以富块测量"接近视口"要对着那个元素、而不是
      // 浏览器视口——否则一个图要等已经在屏幕上了才开始加载。
      <RichContentScrollRootProvider scrollRoot={scrollContainerEl}>
      <Virtuoso
        ref={virtuosoRef}
        customScrollParent={scrollContainerEl}
        data={renderItems}
        firstItemIndex={firstIndex}
        // 打开时钉到最新消息。列表按会话重挂载（上游 `key={activeSessionId}`），
        // 所以这个初始位置在每次切换会话时都会重新应用。没有它，全新的 Virtuoso
        // 从顶部渲染，唯一能把它滚下去的是 `followOutput`——它只对挂载后的数据
        // 增长做出反应——于是落点变得竞态：已缓存的会话同步解析、钉在顶部，
        // 而拉取来的会话有时赶上一个增长 tick、落在底部。`align: "end"` 连一条
        // 比视口还高的最新消息也底对齐，切换会话永远显示最新回复。
        initialTopMostItemIndex={{ index: "LAST", align: "end" }}
        increaseViewportBy={{ top: 400, bottom: 600 }}
        atBottomThreshold={FOLLOW_EDGE_THRESHOLD}
        // 只有当 Virtuoso 说读者在实时端时才跟随快速流出的输出。一次进行中的
        // 平滑动画会在下一次追加时暂时报告"不在底部"并永久丢掉跟随（#6697），
        // 所以实时增长必须用即时滚动。`isFollowing` 进一步收窄：读者可能因为
        // 输入而滚走——那是 120px 的 `atBottom` 宽容带原谅不了的（见
        // stick-to-bottom.ts）。
        followOutput={(atBottom) =>
          !isFetchingOlderMessages && atBottom && isFollowing() ? "auto" : false
        }
        // `followOutput` 永远不会为一行在流式中途长高而触发，所以内容尺寸变化
        // 改经 Virtuoso 自己的高度信号路由到吸底逻辑。
        totalListHeightChanged={onContentHeightChanged}
        startReached={() => {
          if (hasOlderMessages && !isFetchingOlderMessages) {
            onLoadOlderMessages?.();
          }
        }}
        computeItemKey={(_, item) => item.key}
        context={listContext}
        components={LIST_COMPONENTS}
        itemContent={(_, item) => (
          <div
            className={cn(CHAT_COLUMN, "py-2")}
            {...(item.key === liveEndKey ? { [LIVE_END_ROW_ATTR]: "" } : {})}
          >
            <MessageBubble
              item={item}
              isPending={!!pendingTaskId && item.taskId === pendingTaskId}
              transformContent={transformContent}
              onQuickAction={onQuickAction}
              quickActionsDisabled={quickActionsDisabled}
              onRegenerateQuickActions={onRegenerateQuickActions}
              latestAssistantMessageId={latestAssistantMessageId}
              quickActionsPendingMessageId={quickActionsPendingMessageId}
            />
          </div>
        )}
      />
      </RichContentScrollRootProvider>
      )}
    </div>
    </ImageSequenceProvider>
  );
}

/**
 * 骨架屏占位：在拉取某会话 `chat_message` 期间显示（首次刷新、或切到一个未缓存的
 * 会话）。形状大致镜像 assistant → user → assistant 的往来，让真实消息到达时
 * 窗口不会在用户脚下移位。
 */
export function ChatMessageSkeleton() {
  return (
    <div className={cn("flex-1 overflow-hidden", CHAT_GUTTER)}>
      <div className={cn(CHAT_COLUMN, "py-4")}>
        <ChatSkeletonBody />
      </div>
    </div>
  );
}

// 行本身，让列表的挂载前帧可以直接把它们丢进它已建好的 gutter + column。
function ChatSkeletonBody() {
  return (
    <div className="space-y-5">
      <div className="space-y-2">
        <Skeleton className="h-3.5 w-3/4" />
        <Skeleton className="h-3.5 w-1/2" />
      </div>
      <div className="flex justify-end">
        <Skeleton className="h-8 w-48 rounded-2xl" />
      </div>
      <div className="space-y-2">
        <Skeleton className="h-3.5 w-2/3" />
        <Skeleton className="h-3.5 w-5/6" />
        <Skeleton className="h-3.5 w-1/3" />
      </div>
    </div>
  );
}

// ─── 消息气泡 ───────────────────────────────────────────────────────────

// memo：每条流式 task:message 都会重渲染 ChatMessageList，连带 itemContent 里
// 每一条 VISIBLE 行。未变消息的 Message 对象引用稳定、isPending 是布尔值，
// 所以浅比较 memo 能跳过流没碰过的行——持久化历史保持惰性，只有实时 footer 更新。
const MessageBubble = memo(function MessageBubble({
  item,
  isPending,
  transformContent,
  onQuickAction,
  quickActionsDisabled,
  onRegenerateQuickActions,
  latestAssistantMessageId,
  quickActionsPendingMessageId,
}: {
  item: ChatRenderItem;
  isPending: boolean;
  transformContent?: (content: string) => string;
  onQuickAction?: (action: ChatQuickAction) => void | Promise<unknown>;
  quickActionsDisabled: boolean;
  onRegenerateQuickActions?: (message: ChatMessage) => void | Promise<unknown>;
  latestAssistantMessageId: string | null;
  quickActionsPendingMessageId: string | null;
}) {
  // 实时行与持久化后的 assistant 行都落在这里、共用一个 key，且两者都渲染
  // <AssistantMessage>——相同组件类型、相同位置——所以任务完成时 React 做的是
  // 协调而不是重挂载。
  if (item.kind === "live") {
    return (
      <AssistantMessage
        taskId={item.taskId}
        isPending={isPending}
        transformContent={transformContent}
        onQuickAction={onQuickAction}
        quickActionsDisabled={quickActionsDisabled}
      />
    );
  }

  const { message } = item;

  if (message.role === "user") {
    return (
      <div className="flex justify-end">
        <div className="rounded-2xl bg-muted px-3.5 py-2 text-body max-w-[80%] break-words">
          {/* 用户消息在 ContentEditor 里以 markdown 编写，所以它们经同一套
           * RichContent 渲染——和 assistant 回复、Issue/Comment 一样——用户粘贴的
           * Mermaid 围栏在这里也是图。`compact` 收掉首尾块边距，让单行气泡
           * 跟纯文本版一样紧凑。 */}
          <RichContent
            content={message.content}
            attachments={message.attachments}
            density="compact"
            phase="settled"
          />
          <AttachmentList
            attachments={message.attachments}
            content={message.content}
            className="mt-1.5"
          />
        </div>
      </div>
    );
  }

  return (
    <AssistantMessage
      taskId={message.task_id ?? null}
      message={message}
      isPending={isPending}
      transformContent={transformContent}
      onQuickAction={onQuickAction}
      quickActionsDisabled={quickActionsDisabled}
      onRegenerateQuickActions={onRegenerateQuickActions}
      canRegenerateQuickActions={message.id === latestAssistantMessageId}
      quickActionsPending={quickActionsPendingMessageId === message.id}
    />
  );
});

/**
 * 一条 assistant 回合的主体——同时渲染一个任务的"在途（实时）"与"持久化"
 * 两种形态（MUL-4922）。
 *
 * 任务流式期间 `message` 为 undefined，落地后变成持久化的 `chat_message`。
 * 两种形态都由这一个组件渲染、挂在一个稳定的行 key（`task:<taskId>`）下，
 * 所以 实时 → 持久化 的交接是 prop 变化而不是卸载：RichContent 子树以及里面的
 * 任何 Mermaid 图 / HTML iframe 保持挂载、保留它们的平移缩放状态、绝不重跑昂贵
 * 的渲染。此前实时 timeline 活在 Virtuoso 的 Footer 里、持久化行按 `message.id`
 * 键，于是每个完成的任务都会拆掉并重建它的图。
 *
 * timeline 两种形态都来自 `taskMessagesOptions(taskId)`——useRealtimeSync 在
 * 执行期间种下的同一条缓存条目——所以交接时既不需要重拉取、也没有数据断层。
 */
function AssistantMessage({
  taskId,
  message,
  isPending,
  transformContent,
  onQuickAction,
  quickActionsDisabled,
  onRegenerateQuickActions,
  canRegenerateQuickActions = false,
  quickActionsPending = false,
}: {
  taskId: string | null;
  message?: ChatMessage;
  isPending: boolean;
  transformContent?: (content: string) => string;
  onQuickAction?: (action: ChatQuickAction) => void | Promise<unknown>;
  quickActionsDisabled: boolean;
  onRegenerateQuickActions?: (message: ChatMessage) => void | Promise<unknown>;
  canRegenerateQuickActions?: boolean;
  quickActionsPending?: boolean;
}) {
  const { t } = useT("chat");
  const canFetchTaskMessages = isTaskMessageTaskId(taskId);
  // 流式期间挂载的回合在完成后仍保留它的实时 transcript。历史已完成回合只有
  // 在用户点开时才加载那段可选的过程，避免为每条可见的 assistant 消息发一个请求。
  const [processRequested, setProcessRequested] = useState(isPending);
  const shouldFetchTaskMessages = shouldFetchAssistantTaskMessages(
    canFetchTaskMessages,
    isPending,
    !!message?.failure_reason,
    processRequested,
  );

  // 共用 taskMessagesOptions，让这条缓存条目与 useRealtimeSync 在任务执行期间
  // 种下的那一条相同——任务完成时零重拉取，因为 WS 已经填好它了。
  const { data: taskMessages } = useQuery({
    ...taskMessagesOptions(taskId ?? ""),
    enabled: shouldFetchTaskMessages,
  });

  // 按缓存数组的身份 memo：mergeTaskMessagesBySeq 在重复事件到达时保留数组引用，
  // 所以只有真正的新消息落地时这里才重算。
  const timeline: ChatTimelineItem[] = useMemo(
    () => transformTimeline(buildTimeline(taskMessages ?? []), transformContent),
    [taskMessages, transformContent],
  );

  // 持久化消息存在即"内容已落定"；在那之前文本仍在到达、末尾围栏可能写了一半。
  const phase: "streaming" | "settled" = message ? "settled" : "streaming";

  // 失败气泡路径：当服务端 FailTask 写入一条带 failure_reason 的 chat_message 时，
  // 渲染一个带人类可读原因标签 + 可折叠原始 errMsg + 同一份 timeline 的破坏性
  // 气泡，让用户能精确看到运行在哪断的。
  if (message?.failure_reason) {
    return (
      <FailureBubble
        reason={message.failure_reason}
        rawError={message.content}
        timeline={timeline}
        elapsedMs={message.elapsed_ms}
      />
    );
  }

  // no_response 路径（MUL-4351）：agent 完成这轮直聊却没有给出任何文字。
  // 保留运行产生的工具/思考 timeline，并显示一条本地化的"没有文字回复"提示，
  // 而不是一块空白 markdown。
  const isNoResponse = message?.message_kind === "no_response";

  return (
    <div className="w-full space-y-1.5">
      {timeline.length > 0 && (
        <TimelineView
          items={timeline}
          attachments={message?.attachments}
          phase={phase}
          isStreaming={!message}
        />
      )}
      {isNoResponse ? (
        <NoResponseNotice />
      ) : message && timeline.length === 0 ? (
        <RichContent
          content={message.content}
          attachments={message.attachments}
          density="compact"
          phase="settled"
          className="leading-relaxed"
        />
      ) : null}
      {message && !isPending && canFetchTaskMessages && !processRequested ? (
        <button
          type="button"
          className="flex items-center gap-1 text-caption text-muted-foreground transition-colors hover:text-foreground"
          onClick={() => setProcessRequested(true)}
        >
          <ChevronRight className="size-3" />
          <span>{t(($) => $.message_list.show_process)}</span>
        </button>
      ) : null}
      {message && processRequested && shouldFetchTaskMessages && !taskMessages ? (
        <Skeleton className="h-4 w-16" />
      ) : null}
      {message && (
        <>
          <AttachmentList
            attachments={message.attachments}
            content={message.content}
          />
          <MessageFooter
            message={message}
            timeline={timeline}
            isPending={isPending}
          />
          {onQuickAction && (message.quick_actions?.length ?? 0) > 0 ? (
            <QuickActions
              actions={message.quick_actions ?? []}
              disabled={quickActionsDisabled || isPending}
              onSelect={onQuickAction}
              onRegenerate={
                onRegenerateQuickActions && canRegenerateQuickActions
                  ? () => onRegenerateQuickActions(message)
                  : undefined
              }
              pending={quickActionsPending}
            />
          ) : onQuickAction && quickActionsPending ? (
            <QuickActionsSkeleton />
          ) : null}
        </>
      )}
    </div>
  );
}

/**
 * 判断某条已持久化的 assistant 回合是否应该拉取它的任务消息过程。只有带持久化
 * task_id 的回合才会：仍在跑（isPending）、失败（isFailed，要展示断点）、
 * 或用户显式点了"查看过程"（processRequested）。
 */
export function shouldFetchAssistantTaskMessages(
  hasPersistedTaskId: boolean,
  isPending: boolean,
  isFailed: boolean,
  processRequested: boolean,
): boolean {
  return hasPersistedTaskId && (isPending || isFailed || processRequested);
}

function transformTimeline(
  timeline: ChatTimelineItem[],
  transformContent?: (content: string) => string,
): ChatTimelineItem[] {
  return timeline.map((item) =>
    item.type === "text" && item.content
      ? {
          ...item,
          content: transformContent
            ? transformContent(stripChatQuickActionsProtocol(item.content))
            : stripChatQuickActionsProtocol(item.content),
        }
      : item,
  );
}

function QuickActions({
  actions,
  disabled,
  onSelect,
  onRegenerate,
  pending = false,
}: {
  actions: ChatQuickAction[];
  disabled: boolean;
  onSelect: (action: ChatQuickAction) => void | Promise<unknown>;
  /** 只出现在会话最新一回合上——重跑一轮建议生成。 */
  onRegenerate?: () => void | Promise<unknown>;
  /**
   * 这一回合正在等补充（一次刷新在途）：旧 pills 保持可见但惰性，刷新图标转圈
   * 直到 chat:quick_actions 落地。与本地 `regenerating` 守卫不同——后者只覆盖
   * "点击 → HTTP 已确认"这个还没观察到 pending 标记的窗口。
   */
  pending?: boolean;
}) {
  const { t } = useT("chat");
  const [submitting, setSubmitting] = useState(false);
  const [regenerating, setRegenerating] = useState(false);
  // pending 标记是唯一真相来源：chat:quick_actions 成功时清掉它，
  // useQuickActionsPendingTimeout 在没有补充到达时从查询缓存里清掉它。所以
  // `pending` 变 false 才是停转圈的信号——没有一个只在本地把 UI 蒙住、而缓存
  // 仍卡着的"过期"标志（MUL-5149 review）。
  const blocked = disabled || submitting || regenerating || pending;

  const handleSelect = async (action: ChatQuickAction) => {
    if (blocked) return;
    setSubmitting(true);
    try {
      await onSelect(action);
    } catch {
      // 发送路径负责面向用户的错误反馈与乐观回滚。
      // 这里重新启用 chip，让一次临时失败可以被重试。
    } finally {
      setSubmitting(false);
    }
  };

  const handleRegenerate = async () => {
    if (blocked || !onRegenerate) return;
    setRegenerating(true);
    try {
      await onRegenerate();
    } catch {
      // 调用方的 mutation 会回滚 pending 标记；弹一条 toast，
      // 别让静默的重新启用被误读成"这次没有建议"。
      toast.error(t(($) => $.message_list.quick_actions_regenerate_failed));
    } finally {
      setRegenerating(false);
    }
  };

  const regenerateLabel = t(($) => $.message_list.quick_actions_regenerate);

  return (
    <div className="mt-2 border-t border-border/40 pt-2 animate-in fade-in slide-in-from-bottom-1 duration-300">
      <div
        className="flex flex-wrap items-center gap-2"
        aria-label={t(($) => $.message_list.quick_actions_aria)}
      >
        <QuickActionsHeading />
        {actions.slice(0, 3).map((action, index) => (
          // 整颗 pill 在悬停时预览它隐藏的提示词：点击会以用户名义发送一条他
          // 从未见过的消息——tooltip 把"先提交后知晓"翻转成"先知晓后提交"。
          <Tooltip key={`${action.label}-${index}`}>
            <TooltipTrigger
              render={
                <Button
                  type="button"
                  variant={action.primary ? "brandSubtle" : "outline"}
                  size="sm"
                  className="max-w-full rounded-full px-3"
                  disabled={blocked}
                  onClick={() => void handleSelect(action)}
                />
              }
            >
              <span className="truncate">{action.label}</span>
              {action.primary ? <ArrowUpRight aria-hidden="true" /> : null}
            </TooltipTrigger>
            <TooltipContent side="top" className="max-w-sm whitespace-pre-wrap break-words">
              {action.prompt}
            </TooltipContent>
          </Tooltip>
        ))}
        {onRegenerate ? (
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-xs"
                  className="shrink-0 rounded-full text-faint-foreground hover:text-foreground"
                  disabled={blocked}
                  aria-label={regenerateLabel}
                  onClick={() => void handleRegenerate()}
                />
              }
            >
              <RotateCw
                aria-hidden="true"
                className={
                  pending || regenerating ? "animate-spin" : undefined
                }
              />
            </TooltipTrigger>
            <TooltipContent side="top">{regenerateLabel}</TooltipContent>
          </Tooltip>
        ) : null}
      </div>
    </div>
  );
}

// 跟进 pills 行的轻量行内前缀标签——这行坐在回复 footer（"Replied in Xs · Copy"）
// 下方、隔着一条淡淡的顶边框，让 pills 读起来是一条带标签的"下一步"条，
// 而不是回复正文的一部分。shrink-0 在窄宽度换行时保持标签完整在行首。
function QuickActionsHeading() {
  const { t } = useT("chat");
  return (
    <span className="shrink-0 text-caption text-muted-foreground">
      {t(($) => $.message_list.quick_actions_heading)}
    </span>
  );
}

// chat:done（宣告有待生成的补充）与 chat:quick_actions 之间显示的 pill 形占位。
// 宽度交错，让这行读起来像"按钮马上来"，而不是进度条。aria-hidden：还没有
// 可宣告的交互。
function QuickActionsSkeleton() {
  // 没有本地超时：可见性由共享的 pending 标记驱动，useQuickActionsPendingTimeout
  // 在没有 chat:quick_actions 解决它时把它从查询缓存清掉——所以这里自己会卸载，
  // 而不是只在缓存仍卡着时把自己藏起来（MUL-5149 review）。
  return (
    <div className="mt-2 border-t border-border/40 pt-2 animate-in fade-in duration-300">
      <div className="flex flex-wrap items-center gap-2" aria-hidden="true">
        <QuickActionsHeading />
        <Skeleton className="h-8 w-24 rounded-full" />
        <Skeleton className="h-8 w-32 rounded-full" />
        <Skeleton className="h-8 w-28 rounded-full" />
      </div>
    </div>
  );
}

// 安静的本地化提示：当一轮"没有回复"就完成时（message_kind === "no_response"），
// 在 assistant 文本的位置显示它。解释这轮为什么空，而不是渲染一块空白气泡
// （MUL-4351）。
function NoResponseNotice() {
  const { t } = useT("chat");
  return (
    <div className="text-body italic text-muted-foreground">
      {t(($) => $.message_list.no_response)}
    </div>
  );
}

// assistant 回复下方的行内 footer 行："Replied in 38s · [Copy]"。
// 动作图标就放这里（而不是悬停浮层），让它们首次阅读即可见、也不挪动内容。
// 按钮悬停前保持安静（弱化）。流式期间抑制复制，因为最终文本还在追加。
function MessageFooter({
  message,
  timeline,
  isPending,
}: {
  message: ChatMessage;
  timeline: ChatTimelineItem[];
  isPending: boolean;
}) {
  // no_response 回合没有可复制的东西，它的 caption 用中性的 "Finished in Xs"
  // 而不是 "Replied in Xs"（MUL-4351）。
  const isNoResponse = message.message_kind === "no_response";
  const showCopy = !isPending && !isNoResponse;
  if (message.elapsed_ms == null && !showCopy) return null;
  return (
    <div className="flex items-center gap-1.5">
      {message.elapsed_ms != null && (
        <ElapsedCaption
          variant={isNoResponse ? "finished" : "replied"}
          elapsedMs={message.elapsed_ms}
        />
      )}
      {showCopy && <MessageCopyButton message={message} timeline={timeline} />}
    </div>
  );
}

function MessageCopyButton({
  message,
  timeline,
}: {
  message: ChatMessage;
  timeline: ChatTimelineItem[];
}) {
  const { t } = useT("chat");
  const handleCopy = async () => {
    if (await copyText(extractCopyText(message, timeline))) {
      toast.success(t(($) => $.message_list.copied_toast));
    } else {
      toast.error(t(($) => $.message_list.copy_failed_toast));
    }
  };
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant="ghost"
            size="icon-xs"
            className="text-faint-foreground hover:text-foreground"
            onClick={handleCopy}
            aria-label={t(($) => $.message_list.copy_action)}
          />
        }
      >
        <Copy />
      </TooltipTrigger>
      <TooltipContent side="top">
        {t(($) => $.message_list.copy_action)}
      </TooltipContent>
    </Tooltip>
  );
}

// assistant 气泡下的持久化 "Replied in 38s" / "Failed after 12s" 行。直接从
// chat_message 读 `elapsed_ms`——服务端在任务完成时算一次，所以这个 caption
// 在重载和设备间完全相同。为 null 时静默跳过（在 migration 063 之前的旧消息 +
// 用户消息）。
function ElapsedCaption({
  variant,
  elapsedMs,
  className,
}: {
  variant: "replied" | "failed" | "finished";
  elapsedMs: number;
  className?: string;
}) {
  const { t } = useT("chat");
  const elapsed = formatElapsedMs(elapsedMs);
  const text =
    variant === "replied"
      ? t(($) => $.message_list.replied_in, { elapsed })
      : variant === "finished"
        ? t(($) => $.message_list.finished_in, { elapsed })
        : t(($) => $.message_list.failed_after, { elapsed });
  return (
    <div className={cn("text-caption text-muted-foreground", className)}>
      {text}
    </div>
  );
}

function FailureBubble({
  reason,
  rawError,
  timeline,
  elapsedMs,
}: {
  reason: string;
  rawError: string;
  timeline: ChatTimelineItem[];
  elapsedMs?: number | null;
}) {
  const { t } = useT("chat");
  const [open, setOpen] = useState(false);
  // 聊天为每种失败原因准备了自己的友好、安抚性文案——大白话 + 一个"再试一次"
  // 的提示——而不是 agent 详情 / 执行日志界面用的那种简短的开发者标签
  // （`failureReasonLabel`）。原始错误仍收在下方可折叠区里，供想要技术细节的人看。
  //
  // 按键按原始线上值，而不是封闭枚举——`failure_reason` 是开放字符串，会随
  // 分类规则落地而增长，和 `failureReasonLabel` 的映射表一样。刻意做成分片的：
  // 分类体系比值得为聊天单独写文案的那一小撮大得多，所以一个条目只有在能说出
  // `agent_error` 家族那句说不出的东西——通常是不同的下一步（重新授权、充值、
  // 检查网络）——时才配得上占位。
  //
  // 与运维界面的分歧点：它们回退到原始线上值——机器味重但可搜索。聊天气泡是
  // 刚发了消息的那个人读的，所以它经 `resolveFailureReasonKey` 先落到家族行、
  // 最后落到友好通用文案。原始错误仍隔一次点击、躺在可折叠区下面。
  const chatFailureCopy: Record<string, string> = {
    agent_error: t(($) => $.message_list.failure.agent_error),
    timeout: t(($) => $.message_list.failure.timeout),
    codex_semantic_inactivity: t(($) => $.message_list.failure.codex_semantic_inactivity),
    runtime_offline: t(($) => $.message_list.failure.runtime_offline),
    runtime_access_denied: t(($) => $.message_list.failure.runtime_access_denied),
    runtime_access_revoked: t(($) => $.message_list.failure.runtime_access_revoked),
    runtime_recovery: t(($) => $.message_list.failure.runtime_recovery),
    manual: t(($) => $.message_list.failure.manual),
    cancelled: t(($) => $.message_list.failure.manual),
    skill_bundle_unavailable: t(($) => $.message_list.failure.skill_bundle_unavailable),
    runtime_cli_timeout: t(($) => $.message_list.failure.runtime_cli_timeout),
    environment_prepare_failed: t(($) => $.message_list.failure.environment_prepare_failed),
    "agent_error.provider_network": t(($) => $.message_list.failure.provider_network),
    "agent_error.provider_auth_or_access": t(($) => $.message_list.failure.provider_auth_or_access),
    "agent_error.provider_quota_limit": t(($) => $.message_list.failure.provider_quota_limit),
    "agent_error.provider_capacity_or_rate_limit": t(
      ($) => $.message_list.failure.provider_capacity_or_rate_limit,
    ),
    "agent_error.context_overflow": t(($) => $.message_list.failure.context_overflow),
    "agent_error.runtime_missing_executable": t(
      ($) => $.message_list.failure.runtime_missing_executable,
    ),
    "agent_error.runtime_version_unsupported": t(
      ($) => $.message_list.failure.runtime_version_unsupported,
    ),
  };
  const copyKey = resolveFailureReasonKey(reason, chatFailureCopy);
  const label =
    (copyKey && chatFailureCopy[copyKey]) ??
    t(($) => $.message_list.failure.fallback);

  return (
    <div className="w-full space-y-1.5">
      {/* 失败读成一行行内、低调的提示——不是破坏性警报。刻意无边框 / 无底色：
       * 聊天失败是告知性的（"这个没成"），不是系统错误。图标 + 弱化的破坏性
       * 文字已是足够的信号，其余保持正常回复的节奏。 */}
      <div className="flex items-start gap-1.5 text-body">
        <AlertTriangle className="size-3.5 shrink-0 text-destructive mt-0.5" />
        <div className="flex-1 min-w-0">
          <div className="text-destructive">{label}</div>
          {rawError.trim() && (
            <Collapsible open={open} onOpenChange={setOpen}>
              <CollapsibleTrigger className="mt-0.5 flex items-center gap-1 text-caption text-muted-foreground hover:text-foreground transition-colors">
                {open ? (
                  <ChevronDown className="size-3" />
                ) : (
                  <ChevronRight className="size-3" />
                )}
                <span>{t(($) => $.message_list.show_details)}</span>
              </CollapsibleTrigger>
              <CollapsibleContent>
                <pre className="mt-1 max-h-40 overflow-auto rounded bg-muted/40 p-2 text-caption text-muted-foreground whitespace-pre-wrap break-all">
                  {rawError}
                </pre>
              </CollapsibleContent>
            </Collapsible>
          )}
        </div>
      </div>
      {timeline.length > 0 && <TimelineView items={timeline} />}
      {elapsedMs != null && (
        <ElapsedCaption variant="failed" elapsedMs={elapsedMs} />
      )}
    </div>
  );
}

// ─── Timeline：外层过程折叠 + 最终正文（指挥家式） ─────────────────────
//
// splitTimeline（lib/copy-text.ts）把条目切成：
//   preface — 第一个 thinking/tool 条目之前的文本
//   middle  — 第一个 → 最后一个非文本条目（含首尾，中间可能夹着文本）
//   final   — 最后一个非文本条目之后的文本
//
// 我们把 preface + final 渲染在包住 middle 的外层 Collapsible（"X steps"）之外。
// 内层行的 Collapsibles（ThinkingRow / ToolCallRow / ToolResultRow）不变——
// 点击它们与外层折叠互不干扰。复制与"外层折叠收起时看到的内容"一致：
// preface + final，绝不含 middle。权威的复制逻辑见 extractCopyText。

function TimelineView({
  items,
  isStreaming,
  attachments,
  phase = "settled",
}: {
  items: ChatTimelineItem[];
  isStreaming?: boolean;
  attachments?: import("@multica/core/types").Attachment[];
  phase?: "streaming" | "settled";
}) {
  const { preface, middle, final } = splitTimeline(items);

  return (
    <>
      {preface.length > 0 && (
        <RichContent
          content={preface.map((t) => t.content ?? "").join("")}
          attachments={attachments}
          density="compact"
          phase={phase}
          className="leading-relaxed"
        />
      )}
      {middle.length > 0 && (
        <OuterProcessFold
          items={middle}
          isStreaming={!!isStreaming}
          attachments={attachments}
          phase={phase}
        />
      )}
      {final.length > 0 && (
        <RichContent
          content={final.map((t) => t.content ?? "").join("")}
          attachments={attachments}
          density="compact"
          phase={phase}
          className="leading-relaxed"
        />
      )}
    </>
  );
}

function OuterProcessFold({
  items,
  isStreaming,
  attachments,
  phase = "settled",
}: {
  items: ChatTimelineItem[];
  isStreaming?: boolean;
  attachments?: import("@multica/core/types").Attachment[];
  phase?: "streaming" | "settled";
}) {
  const { t } = useT("chat");
  // 任务流式期间打开（让用户看到进度），落定后收起。过去这靠重挂载顺带实现：
  // 实时 TimelineView 被拆掉、持久化的那个以收起状态挂上。行现在在交接中保持
  // 稳定（MUL-4922）——这正是目的，保住 Mermaid 与 HTML 块——所以收起状态
  // 必须直接表达。
  const [open, setOpen] = useState(!!isStreaming);
  const wasStreaming = useRef(!!isStreaming);
  useEffect(() => {
    if (wasStreaming.current && !isStreaming) setOpen(false);
    wasStreaming.current = !!isStreaming;
  }, [isStreaming]);
  const stepCount = items.length;

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger className="flex items-center gap-1 text-caption text-muted-foreground hover:text-foreground transition-colors">
        {open ? <ChevronDown className="size-3" /> : <ChevronRight className="size-3" />}
        <span>{t(($) => $.message_list.process_steps, { count: stepCount })}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-1 rounded-lg border bg-muted/20 p-2 space-y-0.5">
          {items.map((item) =>
            item.type === "text" ? (
              <MiddleTextRow
                key={item.seq}
                item={item}
                attachments={attachments}
                phase={phase}
              />
            ) : (
              <ItemRow key={item.seq} item={item} />
            ),
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

// 渲染在外层折叠里的中间文本段。视觉上下沉（xs / 弱化），让它读起来是 agent
// 过程的一部分、而不是最终答案——最终答案以完整正文字号渲染在折叠下方。
function MiddleTextRow({
  item,
  attachments,
  phase = "settled",
}: {
  item: ChatTimelineItem;
  attachments?: import("@multica/core/types").Attachment[];
  phase?: "streaming" | "settled";
}) {
  return (
    <div className="py-0.5 text-caption text-muted-foreground">
      <RichContent
        content={item.content ?? ""}
        attachments={attachments}
        density="compact"
        phase={phase}
      />
    </div>
  );
}

// ─── 单条条目行 ──────────────────────────────────────────────────────────

function ItemRow({ item }: { item: ChatTimelineItem }) {
  switch (item.type) {
    case "tool_use":
      return <ToolCallRow item={item} />;
    case "tool_result":
      return <ToolResultRow item={item} />;
    case "thinking":
      return <ThinkingRow item={item} />;
    case "error":
      return <ErrorRow item={item} />;
    default:
      return null;
  }
}

function ToolCallRow({ item }: { item: ChatTimelineItem }) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  // Tool input is arbitrary JSON: an MCP tool can pass `query` or `path` as an
  // object (#8835), so the summary must come from the type-checked presenter.
  const summary = traceToolArgSummary(item.input, {
    morePaths: (path, extraCount) =>
      t(($) => $.transcript.patch_summary_more, { path, extra: extraCount }),
  });
  const hasInput = item.input && Object.keys(item.input).length > 0;

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger className="flex w-full items-center gap-1.5 rounded px-1 -mx-1 py-0.5 text-caption hover:bg-accent/30 transition-colors">
        <ChevronRight
          className={cn(
            "h-3 w-3 shrink-0 text-muted-foreground transition-transform",
            open && "rotate-90",
            !hasInput && "invisible",
          )}
        />
        <span className="font-medium text-foreground shrink-0">{item.tool}</span>
        {summary && <span className="truncate text-muted-foreground">{summary}</span>}
      </CollapsibleTrigger>
      {hasInput && (
        <CollapsibleContent>
          <pre className="ml-[18px] mt-0.5 max-h-32 overflow-auto rounded bg-muted/50 p-2 text-caption text-muted-foreground whitespace-pre-wrap break-all">
            {JSON.stringify(item.input, null, 2)}
          </pre>
        </CollapsibleContent>
      )}
    </Collapsible>
  );
}

function ToolResultRow({ item }: { item: ChatTimelineItem }) {
  const { t } = useT("chat");
  const [open, setOpen] = useState(false);
  const output = item.output ?? "";
  if (!output) return null;

  const preview = output.length > 120 ? output.slice(0, 120) + "..." : output;
  const labelPrefix = item.tool
    ? t(($) => $.message_list.tool_result_named, { tool: item.tool })
    : t(($) => $.message_list.tool_result_unnamed);

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger className="flex w-full items-start gap-1.5 rounded px-1 -mx-1 py-0.5 text-caption hover:bg-accent/30 transition-colors">
        <ChevronRight
          className={cn("h-3 w-3 shrink-0 text-muted-foreground transition-transform mt-0.5", open && "rotate-90")}
        />
        <span className="text-muted-foreground truncate">
          {labelPrefix}{preview}
        </span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <pre className="ml-[18px] mt-0.5 max-h-40 overflow-auto rounded bg-muted/50 p-2 text-caption text-muted-foreground whitespace-pre-wrap break-all">
          {output.length > 4000 ? output.slice(0, 4000) + "\n... (truncated)" : output}
        </pre>
      </CollapsibleContent>
    </Collapsible>
  );
}

function ThinkingRow({ item }: { item: ChatTimelineItem }) {
  const [open, setOpen] = useState(false);
  const text = item.content ?? "";
  if (!text) return null;

  const preview = text.length > 150 ? text.slice(0, 150) + "..." : text;

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger className="flex w-full items-start gap-1.5 rounded px-1 -mx-1 py-0.5 text-caption hover:bg-accent/30 transition-colors">
        <Brain className="h-3 w-3 shrink-0 text-faint-foreground mt-0.5" />
        <span className="text-muted-foreground italic truncate">{preview}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <pre className="ml-[18px] mt-0.5 max-h-40 overflow-auto rounded bg-muted/30 p-2 text-caption text-muted-foreground whitespace-pre-wrap break-words">
          {text}
        </pre>
      </CollapsibleContent>
    </Collapsible>
  );
}

function ErrorRow({ item }: { item: ChatTimelineItem }) {
  return (
    <div className="flex items-start gap-1.5 px-1 -mx-1 py-0.5 text-caption">
      <AlertCircle className="h-3 w-3 shrink-0 text-destructive mt-0.5" />
      <span className="text-destructive">{item.content}</span>
    </div>
  );
}

// ─── 共享 ────────────────────────────────────────────────────────────────
