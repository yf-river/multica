// Chat 的"待处理任务"状态机辅助：会话头正在跑的那条任务 + 排队中的任务队列。
// 纯函数，描述一次发送/生命周期事件后聊天界面该长什么样。
import type { ChatMessage, ChatPendingTask, ChatQueuedTask } from "../types/chat";

const EMPTY_PENDING_TASK: ChatPendingTask = {};

/** 队列排序：按 created_at 升序（时间无效时退回按 task_id 字典序，保证稳定可比）。 */
function compareQueuedTasks(left: ChatQueuedTask, right: ChatQueuedTask): number {
  const leftTime = Date.parse(left.created_at);
  const rightTime = Date.parse(right.created_at);
  if (Number.isFinite(leftTime) && Number.isFinite(rightTime) && leftTime !== rightTime) {
    return leftTime - rightTime;
  }
  return left.task_id.localeCompare(right.task_id);
}

/** 按 task_id 去重；同一任务有多份观察时，取信息更丰富的那份。 */
function uniqueQueue(tasks: ChatQueuedTask[]): ChatQueuedTask[] {
  const byID = new Map<string, ChatQueuedTask>();
  // 发送响应携带消息预览，而 task:queued 事件是稀疏的。
  // 无论网络到达顺序如何，都保留更丰富的那份观察。
  for (const task of tasks) {
    if (!byID.has(task.task_id) || task.message_id) byID.set(task.task_id, task);
  }
  return [...byID.values()];
}

/** 归一化队列：去重后按时间排序。 */
function normalizeQueue(tasks: ChatQueuedTask[]): ChatQueuedTask[] {
  return uniqueQueue(tasks).sort(compareQueuedTasks);
}

/** 把一条"仅头部"的待处理任务压缩成可排队的摘要形态；没有 task_id 则忽略。 */
function asSummary(task: ChatPendingTask): ChatQueuedTask | undefined {
  if (!task.task_id) return undefined;
  return {
    task_id: task.task_id,
    status: task.status ?? "queued",
    created_at: task.created_at ?? "",
  };
}

/**
 * 把一条新任务并入"待处理"状态。
 *
 * 无当前头部时：排队的发送可能早于本客户端加载真实会话头到达——作为仅队列行先藏
 * 起来，等权威重取填出会话头；空闲的发送则直接成为会话头。
 * 与当前头部同一任务时：合并状态与时间（头部状态优先于 "queued"）。
 * 不同任务且 `queued=true`：加入队列排队。
 * @param current 当前待处理状态。
 * @param task 新到达的排队任务观察。
 * @param queued 该任务是否应作为排队而非替代头部。
 * @returns 更新后的待处理状态。
 */
export function enqueuePendingChatTask(
  current: ChatPendingTask | undefined,
  task: ChatQueuedTask,
  queued = Boolean(current?.task_id && current.task_id !== task.task_id),
): ChatPendingTask {
  if (!current?.task_id) {
    // 一条排队的发送可能在本客户端加载到真实会话头之前到达。
    // 在权威重取填出会话头前，先把它藏成"仅队列"行；空闲发送则立刻成为会话头。
    const queueSupport = current?.supports_queue === undefined
      ? {}
      : { supports_queue: current.supports_queue };
    return queued
      ? { ...queueSupport, queued_tasks: [task] }
      : {
          ...task,
          ...queueSupport,
          queued_tasks: normalizeQueue(
            (current?.queued_tasks ?? []).filter((queuedTask) =>
              queuedTask.task_id !== task.task_id
            ),
          ),
        };
  }
  if (current.task_id === task.task_id) {
    // 同一任务：头部已不是 queued 就用它的状态，否则采用新观察的；从队列剔除自己。
    const status = current.status && current.status !== "queued"
      ? current.status
      : task.status;
    const queuedTasks = (current.queued_tasks ?? []).filter(
      (queued) => queued.task_id !== task.task_id,
    );
    return {
      ...current,
      ...task,
      status,
      created_at: current.created_at || task.created_at,
      queued_tasks: queuedTasks,
    };
  }
  if (!queued) {
    // 不同任务且明确"非排队"：新任务顶掉旧头部（丢弃旧头部，保留队列支持标记）。
    return {
      ...task,
      supports_queue: current.supports_queue,
      queued_tasks: [],
    };
  }
  // 不同任务且需要排队：挂在现有头部后面。
  return {
    ...current,
    queued_tasks: normalizeQueue([...(current.queued_tasks ?? []), task]),
  };
}

// 一条"等待原因"说明活得比等待本身更久：daemon 只在进入等待时写一次 wait_reason、
// 从不清除，所以唯一能让"被任务 abc12345 持有"这句话不残留到一条已经跑起来的任务上
// 的，就是这扇门。只要状态不是等待态就丢掉这段文字——无论它来自本事件还是早已在缓存里。
function resolveWaitReason(
  status: string,
  incoming: string | undefined,
  existing: string | undefined,
): string | undefined {
  if (status !== "waiting_local_directory") return undefined;
  return incoming ?? existing;
}

/**
 * 把某条任务提升为新会话头（生命周期事件：它开始运行了）。
 * 原头部被压回队列；未知任务的事件不会凭空造出一个会话头。
 * @param current 当前待处理状态。
 * @param taskID 晋升为新头部的任务 id。
 * @param status 该任务的新状态。
 * @param createdAt 该任务创建时间（可选）。
 * @param waitReason 等待原因（可选）。
 * @returns 更新后的待处理状态。
 */
export function promotePendingChatTask(
  current: ChatPendingTask | undefined,
  taskID: string,
  status: string,
  createdAt?: string,
  waitReason?: string,
): ChatPendingTask {
  const queue = current?.queued_tasks ?? [];
  if (current?.task_id === taskID) {
    return {
      ...current,
      status,
      wait_reason: resolveWaitReason(status, waitReason, current.wait_reason),
      queued_tasks: queue.filter((task) => task.task_id !== taskID),
    };
  }

  const promoted = queue.find((task) => task.task_id === taskID);
  // 生命周期事件只是提示。权威的 pending 查询清空后，一条针对未知任务的迟到事件
  // 绝不能新造一个会话头。
  if (!current?.task_id || !promoted) return current ?? EMPTY_PENDING_TASK;
  const previousHead = asSummary(current ?? {});
  return {
    ...promoted,
    supports_queue: current.supports_queue,
    status,
    wait_reason: resolveWaitReason(status, waitReason, undefined),
    created_at: promoted.created_at || createdAt || "",
    queued_tasks: uniqueQueue([
      // 旧头部若还在排队，压回队列；晋升者从队列移除。
      ...(previousHead?.status === "queued" ? [previousHead] : []),
      ...queue.filter((task) => task.task_id !== taskID),
    ]),
  };
}

/**
 * 从待处理状态移除某条任务：若它在队列里则直接删除；若它是会话头，
 * 则队列里的下一条顶上成为新头部。
 * @param current 当前待处理状态。
 * @param taskID 要移除的任务 id。
 * @returns 更新后的待处理状态（没有剩余任务时为空对象）。
 */
export function removePendingChatTask(
  current: ChatPendingTask | undefined,
  taskID: string,
): ChatPendingTask {
  if (!current?.task_id) return EMPTY_PENDING_TASK;
  if (current.task_id !== taskID) {
    return {
      ...current,
      queued_tasks: (current.queued_tasks ?? []).filter((task) => task.task_id !== taskID),
    };
  }

  // 头部被移除：让队列中的下一条顶上。
  const [next, ...rest] = uniqueQueue(
    (current.queued_tasks ?? []).filter((task) => task.task_id !== taskID),
  );
  if (!next) return EMPTY_PENDING_TASK;
  const queueSupport = current.supports_queue === undefined
    ? {}
    : { supports_queue: current.supports_queue };
  return { ...next, ...queueSupport, queued_tasks: rest };
}

/** 把某条排队任务提到队列最前（用户手动插队 / 优先执行）。 */
export function prioritizePendingChatTask(
  current: ChatPendingTask | undefined,
  taskID: string,
): ChatPendingTask {
  if (!current?.task_id) return EMPTY_PENDING_TASK;
  const queue = current.queued_tasks ?? [];
  const selected = queue.find((task) => task.task_id === taskID);
  if (!selected) return current;
  return {
    ...current,
    queued_tasks: [selected, ...queue.filter((task) => task.task_id !== taskID)],
  };
}

/**
 * 从消息列表里隐藏"已有对应排队任务占位"的消息（发送后先以排队任务形态出现、
 * 又在消息流里预览过一次会造成重复，这里把那条预览藏掉）。
 */
export function hideQueuedChatMessages(
  messages: ChatMessage[],
  pending: ChatPendingTask | undefined,
): ChatMessage[] {
  // 收集排队任务各自的 message_id → task_id 映射。
  const queuedMessageTasks = new Map(
    (pending?.queued_tasks ?? []).flatMap((task) =>
      task.message_id ? [[task.message_id, task.task_id] as const] : []
    ),
  );
  if (queuedMessageTasks.size === 0) return messages;
  return messages.filter((message) => queuedMessageTasks.get(message.id) !== message.task_id);
}
