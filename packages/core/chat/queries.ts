// Chat 查询：query key 工厂与各 queryOptions。覆盖会话列表/详情、两种消息缓存、
// 待处理任务、快捷操作 pending/failure 标记、草稿恢复、任务执行时间线等。
// 多数查询 staleTime: Infinity，靠 WS 事件失效保持新鲜（见 realtime/use-realtime-sync）。
import { infiniteQueryOptions, queryOptions, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { TaskMessagePayload } from "../types/events";
import type {
  ChatQuickActionsFailureState,
  ChatQuickActionsPendingState,
  ChatSession,
} from "../types/chat";

/**
 * 快捷操作 pending 标记在客户端放弃并清除之前可保持未解析的最长时长（MUL-5149）。
 * 创建标记时用来盖上绝对 `expires_at` 截止，`useQuickActionsPendingTimeout` 也用它清除——
 * 共享一个截止能扛住聊天界面切换，而不是每次重挂载都重开一段全新窗口。
 *
 * 尺寸跟着服务端预算走：建议由一次有界（8s）的 LLM 调用生成，还要给写入和 realtime
 * 广播留余地。必须大于后端的 `chatQuickActionsTimeout`——比它等待的活儿还短的窗口
 * 会把每一次"慢但成功"的通过都看成像失败，然后再事后悄悄把药丸换掉。
 */
export const QUICK_ACTIONS_PENDING_TIMEOUT_MS = 12_000;

// 关于工作区作用域的说明：
// `wsId` 只作为 queryKey 的一部分，用于按工作区隔离缓存。真正的工作区上下文来自
// ApiClient 的 X-Workspace-Slug 头，由 URL 驱动的 [workspaceSlug] 布局设置。
// 调用方必须保证该头与自己传进来的 wsId 同步——否则在工作区切换的竞态窗口里，
// 缓存写入会被错误归属。

/** Chat 全部查询 key 的工厂。 */
export const chatKeys = {
  all: (wsId: string) => ["chat", wsId] as const,
  /** 完整会话列表（活跃 + 已归档）；下拉在本地拆分。 */
  sessions: (wsId: string) => [...chatKeys.all(wsId), "sessions"] as const,
  session: (wsId: string, id: string) => [...chatKeys.all(wsId), "session", id] as const,
  messagesAll: () => ["chat", "messages"] as const,
  messages: (sessionId: string) => [...chatKeys.messagesAll(), sessionId] as const,
  messagesPageAll: () => ["chat", "messages-page"] as const,
  messagesPage: (sessionId: string) => [...chatKeys.messagesPageAll(), sessionId] as const,
  pendingTaskAll: () => ["chat", "pending-task"] as const,
  pendingTask: (sessionId: string) => [...chatKeys.pendingTaskAll(), sessionId] as const,
  /** 仅客户端标记：该会话的最后一轮正等待快捷操作补充。 */
  quickActionsPendingAll: () => ["chat", "quick-actions-pending"] as const,
  quickActionsPending: (sessionId: string) =>
    [...chatKeys.quickActionsPendingAll(), sessionId] as const,
  /** 仅客户端信号：该会话最近一次刷新再生（regeneration）失败。 */
  quickActionsFailureAll: () => ["chat", "quick-actions-failure"] as const,
  quickActionsFailure: (sessionId: string) =>
    [...chatKeys.quickActionsFailureAll(), sessionId] as const,
  draftRestoresAll: () => ["chat", "draft-restores"] as const,
  /** 某个会话的持久化"延迟取消草稿恢复"（#5219）。 */
  draftRestores: (sessionId: string) => [...chatKeys.draftRestoresAll(), sessionId] as const,
  /** 当前用户正在飞行中的所有 chat 任务聚合——FAB 读它。 */
  pendingTasks: (wsId: string) => [...chatKeys.all(wsId), "pending-tasks"] as const,
  /** 快捷 Agent 栏里用户钉住的 Agent。 */
  pinnedAgents: (wsId: string) => [...chatKeys.all(wsId), "pinned-agents"] as const,
  /**
   * 布尔"用户是否有在飞的 chat 任务"——FAB 的廉价运行指示。与详细 `pendingTasks`
   * 列表分开缓存，让 FAB（窗口收起）与 ChatWindow（打开）可以独立订阅。
   */
  pendingTasksHasAny: (wsId: string) =>
    [...chatKeys.all(wsId), "pending-tasks", "has-any"] as const,
  /** 单任务的执行消息——与议题的 Agent 卡片共享。 */
  taskMessagesAll: () => ["task-messages"] as const,
  taskMessages: (taskId: string) => [...chatKeys.taskMessagesAll(), taskId] as const,
};

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** 判断某 taskId 是否是真实任务 UUID（区别于聊天消息 id 等）。 */
export function isTaskMessageTaskId(taskId: string | null | undefined): taskId is string {
  return typeof taskId === "string" && UUID_PATTERN.test(taskId);
}

/** 会话列表查询配置（含已归档；下拉在本地拆分）。 */
export function chatSessionsOptions(wsId: string) {
  return queryOptions({
    queryKey: chatKeys.sessions(wsId),
    queryFn: () => api.listChatSessions({ status: "all" }),
    staleTime: Infinity,
  });
}

/** 用于给 IM 列表排名的最近活动时间戳（最新在前）。 */
function sessionActivityTime(s: ChatSession): number {
  return new Date(s.last_message?.created_at ?? s.updated_at).getTime();
}

/**
 * 按与服务端一致的方式给会话列表排序：钉住的在前，其余按最近活动降序。
 * 既用于渲染列表，也用于乐观钉/取消钉或 WS 补丁之后重排缓存，让一份被改动过的
 * 扁平缓存永远渲染有序。返回新数组；相等 key 时稳定（Array.prototype.sort 稳定），
 * 因此当列表载荷不带钉住时间戳时，钉住行保持服务端顺序。
 */
export function sortChatSessions(sessions: ChatSession[]): ChatSession[] {
  return [...sessions].sort((a, b) => {
    const ap = a.pinned ? 1 : 0;
    const bp = b.pinned ? 1 : 0;
    if (ap !== bp) return bp - ap;
    return sessionActivityTime(b) - sessionActivityTime(a);
  });
}

/**
 * 应点亮 quick-chat FAB 未读徽标的会话数。
 * `chatSessionsOptions` 拉的是 `status=all`（活跃 + 已归档），让线程列表能渲染
 * 已归档视图；但已归档会话绝不能计入徽标：它们只读、且藏在默认历史列表外，
 * 从它们身上来的徽标是永远清不掉的——用户无法打开它来标记已读。归档现在还会在
 * 服务端拆掉外部渠道绑定，所以不会有新未读落到已归档会话；这个过滤是那份保证的
 * 前端一半（MUL-4372）。
 */
export function countUnreadChatSessions(sessions: ChatSession[]): number {
  return sessions.filter((s) => s.has_unread && s.status !== "archived").length;
}

/** 快捷 Agent 栏的钉住 Agent 查询配置。 */
export function chatPinnedAgentsOptions(wsId: string) {
  return queryOptions({
    queryKey: chatKeys.pinnedAgents(wsId),
    queryFn: () => api.listChatPinnedAgents(),
    staleTime: Infinity,
  });
}

/** 单个会话详情查询配置。 */
export function chatSessionOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: chatKeys.session(wsId, id),
    queryFn: () => api.getChatSession(id),
    enabled: !!id,
    staleTime: Infinity,
  });
}

/** 扁平消息数组查询配置（Agent Builder 等读它）。 */
export function chatMessagesOptions(sessionId: string) {
  return queryOptions({
    queryKey: chatKeys.messages(sessionId),
    queryFn: () => api.listChatMessages(sessionId),
    enabled: !!sessionId,
    staleTime: Infinity,
  });
}

/** 分页消息查询配置（聊天界面读它，按游标向更早翻页）。 */
export function chatMessagesPageOptions(sessionId: string, limit = 50) {
  return infiniteQueryOptions({
    queryKey: chatKeys.messagesPage(sessionId),
    queryFn: ({ pageParam }) =>
      api.listChatMessagesPage(sessionId, { before: pageParam, limit }),
    initialPageParam: null as { created_at: string; id: string } | null,
    getNextPageParam: (lastPage) =>
      lastPage.has_more ? lastPage.next_cursor ?? undefined : undefined,
    enabled: !!sessionId,
    staleTime: Infinity,
  });
}

/**
 * 某 chat 会话的待处理任务——"是不是还有东西在跑？"的信号。
 * chat:message / chat:done / task:completed / task:failed 到达时由 useRealtimeSync
 * 里的 WS 失效触发重取。
 */
export function pendingChatTaskOptions(sessionId: string) {
  return queryOptions({
    queryKey: chatKeys.pendingTask(sessionId),
    queryFn: () => api.getPendingChatTask(sessionId),
    enabled: !!sessionId,
    staleTime: Infinity,
  });
}

/**
 * 某会话的持久化"延迟取消草稿恢复"（#5219）。
 * staleTime: 0 刻意覆盖全局 Infinity 默认：这是为"错过 chat:cancel_finalized 广播"
 * 的客户端准备的恢复路径，所以每次输入框挂载都必须真去重取（Infinity 新鲜缓存会把
 * 第一次的结果永久钉住）。WS 重连还会在 useRealtimeSync 里失效
 * chatKeys.draftRestoresAll()；发起方的事件到达时其 realtime 处理器也会失效该 key。
 * 响应通常极小（常为空），多出的请求可忽略。
 */
export function chatDraftRestoresOptions(sessionId: string) {
  return queryOptions({
    queryKey: chatKeys.draftRestores(sessionId),
    queryFn: () => api.listChatDraftRestores(sessionId),
    enabled: !!sessionId,
    staleTime: 0,
  });
}

/**
 * 单条任务的时间线——实时聊天视图（任务运行中）与 AssistantMessage（已完成任务）
 * 都会渲染它。WS 的 `task:message` 事件经 useRealtimeSync 实时播种这个缓存。
 */
export function taskMessagesOptions(taskId: string) {
  return queryOptions({
    queryKey: chatKeys.taskMessages(taskId),
    queryFn: () => api.listTaskMessages(taskId),
    enabled: isTaskMessageTaskId(taskId),
    staleTime: Infinity,
    // 对这个缓存的每次写入——本次拉取、一次回填、或一条 realtime 批量——都折叠进
    // 已有的内容而不是替换它。没有这层，一条在"实时帧已写入之后"才解析的响应会丢掉
    // 那个 seq，而 staleTime:Infinity 意味着永远没人会再拉一次。
    structuralSharing: (prev, next) =>
      unionTaskMessagesBySeq(
        prev as TaskMessagePayload[] | undefined,
        next as TaskMessagePayload[],
      ),
  });
}

/**
 * 把任务消息批次合并进一份"按 seq 排序、按 seq 去重"的列表，供共享的
 * `["task-messages", taskId]` 缓存使用。冲突时已有条目赢；没有新内容到达时保留原数组
 * 引用，这样重复事件不会让 React Query 观察者重渲染。
 *
 * realtime 的 `task:message` 处理器（单条载荷）与记录回填（整次重取）都会写这份缓存。
 * 两者都走这一个助手，能防止一次强制的回填盲替换掉 WebSocket 已送达的 seq——
 * 也防止迟到的 WS 事件被一次在途的回填吞掉。
 */
export function mergeTaskMessagesBySeq(
  existing: readonly TaskMessagePayload[],
  incoming: readonly TaskMessagePayload[],
): TaskMessagePayload[] {
  if (incoming.length === 0) return existing as TaskMessagePayload[];
  const knownSeqs = new Set(existing.map((m) => m.seq));
  const fresh = incoming.filter((m) => !knownSeqs.has(m.seq));
  if (fresh.length === 0) return existing as TaskMessagePayload[];
  return [...existing, ...fresh].sort((a, b) => a.seq - b.seq);
}

/**
 * 按 seq 求两份任务消息列表的并集，冲突时 `authoritative`（权威方）赢。
 *
 * 这是对 `["task-messages", taskId]` 缓存的每一次写都要经过的规则，作为查询上的
 * `structuralSharing` 接入，让一次拉取结果不能整数组替换缓存。
 *
 * 这很重要，因为普通查询响应与 realtime 流是竞态的：时间线首次打开时拉取，而请求
 * 在途期间到达的任何帧都会在响应落地前先被写进缓存。裸替换会丢掉那些 seq，且
 * `staleTime: Infinity` 意味着永远没人重取它们——缺口要一直留到下次刷新。
 *
 * 冲突时服务器数据赢，因为持久化的行才是权威。响应没提到的行保留而不是删除——
 * 一份在 seq 持久化之前就快照的响应不能把它抹掉。
 *
 * 无差异时原样返回 `base` 引用，一条啥也不加的事件不会让所有订阅者重渲染。
 */
export function unionTaskMessagesBySeq(
  base: readonly TaskMessagePayload[] | undefined,
  authoritative: readonly TaskMessagePayload[],
): TaskMessagePayload[] {
  if (!base || base.length === 0) {
    return [...authoritative].sort((a, b) => a.seq - b.seq);
  }

  const bySeq = new Map(base.map((m) => [m.seq, m]));
  let changed = false;
  for (const msg of authoritative) {
    if (bySeq.get(msg.seq) !== msg) {
      bySeq.set(msg.seq, msg);
      changed = true;
    }
  }
  if (!changed) return base as TaskMessagePayload[];
  return [...bySeq.values()].sort((a, b) => a.seq - b.seq);
}

/**
 * 本客户端是否持有 `taskId` 的时间线缓存条目——即该任务在某个时点被打开过、且自那
 * 之后未被 GC。
 *
 * 刻意不是"此刻有视图挂载着"。观察者计数是更紧的边界，但不安全：`staleTime: Infinity`
 * 下，一个短暂掉到零观察者（导航中卸载/重挂载）的任务会丢掉帧，重挂载时又把存活的
 * 缓存读成新鲜的、永远不会把它们取回。条目存在性没有这种缺口——条目在就持续累积，
 * 条目没了则下次打开拉整条时间线。代价是一条有界的尾巴：写入不会推迟 GC 计时器，
 * 所以条目比它最后一个观看者多活至多一个 `gcTime`。
 *
 * realtime 层用它决定一条 `task:message` 帧是否值得缓存。工作区里每个客户端都会收到
 * 每次运行的所有帧，但只有少数运行真被打开；没有这扇门，每个客户端都会累积用户
 * 永远不会看的运行记录（MUL-6396）。
 *
 * 看"存在性"而非"数据"：挂载 useQuery 会在 fetch 解析前就注册条目，所以回填中途
 * 落地的帧仍会被保留。为未注册任务丢一帧是安全的——行在广播前就已持久化，
 * 之后谁打开该任务都会从服务器取到它。
 */
export function isTaskMessageTimelineHeld(
  qc: QueryClient,
  taskId: string,
): boolean {
  return qc.getQueryCache().find({ queryKey: chatKeys.taskMessages(taskId) }) !== undefined;
}

/**
 * 当前用户在本工作区所有在飞 chat 任务的聚合。驱动聊天窗口最小化时 FAB 的
 * "运行中"指示器——那时没有任何按会话的查询在跑，所以需要这份聚合。
 */
export function pendingChatTasksOptions(wsId: string) {
  return queryOptions({
    queryKey: chatKeys.pendingTasks(wsId),
    queryFn: () => api.listPendingChatTasks(),
    staleTime: Infinity,
  });
}

/**
 * 布尔"我现在是否有 chat 任务在跑"——pendingChatTasksOptions 的廉价兄弟。
 * FAB 用它（配 `enabled: !isOpen`），最小化的聊天按钮从不拉取或持有完整任务列表；
 * 详细列表留给打开的 ChatWindow（历史 + 停止流程）。两个缓存都由任务生命周期的
 * WS 处理器保持同步。
 */
export function hasPendingChatTasksOptions(wsId: string) {
  return queryOptions({
    queryKey: chatKeys.pendingTasksHasAny(wsId),
    queryFn: () => api.hasAnyPendingChatTasks(),
    staleTime: Infinity,
  });
}

/**
 * 仅客户端缓存条目：由 realtime 层写入（chat:done 升起它、chat:quick_actions
 * 解决它），从不从服务端拉取——`enabled: false` 让 queryFn 永不执行；观察者仍会
 * 在 setQueryData 时重渲染。
 */
export function chatQuickActionsPendingOptions(sessionId: string) {
  return queryOptions({
    queryKey: chatKeys.quickActionsPending(sessionId),
    queryFn: async (): Promise<ChatQuickActionsPendingState | null> => null,
    enabled: false,
    staleTime: Infinity,
  });
}

/**
 * 镜像 {@link chatQuickActionsPendingOptions} 的仅客户端缓存条目：
 * realtime 层在一次失败的 chat:quick_actions 时写它，视图读到一次后弹 toast
 * "无法刷新"，随即清掉。从不拉取。
 */
export function chatQuickActionsFailureOptions(sessionId: string) {
  return queryOptions({
    queryKey: chatKeys.quickActionsFailure(sessionId),
    queryFn: async (): Promise<ChatQuickActionsFailureState | null> => null,
    enabled: false,
    staleTime: Infinity,
  });
}
