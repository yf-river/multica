// 一条"已落定"的 chat 消息进消息缓存时必经的唯一门（MUL-5711）。
// 以 message.id 为身份做合并，两条路径重复到达时收敛而不是重复/降级。
import type { InfiniteData, QueryClient } from "@tanstack/react-query";
import { chatKeys } from "./queries";
import type { ChatMessage, ChatMessagesPage } from "../types";

/**
 * 一条已落定的 chat 消息进消息缓存的唯一门（MUL-5711）。
 *
 * 在此之前每个写入方都自搞一套：`chat:done` 就地插入助手回复，`chat:message`
 * 把自己的载荷丢掉只做失效，三条发送路径各自手写追加——其中两条会写两个缓存、
 * Agent Builder 只写扁平缓存。这个 bug 的缺口正在这种不一致里，而不在任何一个
 * 写入方：用户自己的那条消息是任何 WS 处理器都不写的一行，它只能靠重取进入会话记录，
 * 而一次被取消的重取会把那条消息一起带走。
 *
 * 两条规则都与顺序无关，同一消息经不同路径到达两次会收敛而不是重复或降级：
 *
 *   1. 身份是 `message.id`。对已存在 id 的第二次写入做合并，而不是追加。
 *   2. 合并时缓存里已有的行在自己定义的每个字段上赢；入站行只补缺失字段。
 *      因此带 `attachments` 的发送响应不会被"根本没有 attachments 字段"的
 *      WebSocket 回声冲掉——无论哪种到达顺序。
 */

/** 入站消息上存在的字段只填充既有消息缺失的空隙。 */
function mergeChatMessage(existing: ChatMessage, incoming: ChatMessage): ChatMessage {
  const fills: Partial<ChatMessage> = {};
  let filled = false;
  for (const key of Object.keys(incoming) as (keyof ChatMessage)[]) {
    if (existing[key] !== undefined || incoming[key] === undefined) continue;
    Object.assign(fills, { [key]: incoming[key] });
    filled = true;
  }
  // 什么也没填时保持引用稳定，避免观察者被多余地重渲染。
  return filled ? { ...existing, ...fills } : existing;
}

/**
 * 若列表里已有该 id 的行则与之合并。绝不追加——真正的新消息该放哪儿由调用方决定，
 * 对分页缓存而言是某一个具体页。
 */
function mergeIntoList(messages: ChatMessage[], message: ChatMessage): ChatMessage[] {
  const index = messages.findIndex((m) => m.id === message.id);
  if (index < 0) return messages;
  const merged = mergeChatMessage(messages[index]!, message);
  if (merged === messages[index]) return messages;
  return messages.map((m, i) => (i === index ? merged : m));
}

/** 存在则合并，不存在则追加到列表末尾。 */
function upsertIntoList(messages: ChatMessage[], message: ChatMessage): ChatMessage[] {
  const merged = mergeIntoList(messages, message);
  if (merged !== messages) return merged;
  return messages.some((m) => m.id === message.id) ? messages : [...messages, message];
}

/** 往分页缓存里 upsert 一条消息（在它当前所在页就地合并；新消息追加到第 0 页）。 */
function upsertIntoPages(
  data: InfiniteData<ChatMessagesPage>,
  message: ChatMessage,
): InfiniteData<ChatMessagesPage> {
  const seen = data.pages.some((page) => page.messages.some((m) => m.id === message.id));
  if (seen) {
    // 就地合并，无论行现在躺在哪一页。这里若是追加，会给每一*其它*页都丢进一份副本。
    let changed = false;
    const pages = data.pages.map((page) => {
      const next = mergeIntoList(page.messages, message);
      if (next === page.messages) return page;
      changed = true;
      return { ...page, messages: next };
    });
    return changed ? { ...data, pages } : data;
  }
  // 第 0 页是最新的窗口（后面的页是更旧的游标页），所以新消息属于它的末尾。
  return {
    ...data,
    pages: data.pages.map((page, index) =>
      index === 0 ? { ...page, messages: [...page.messages, message] } : page,
    ),
  };
}

/** 用单条消息构造一个占位首页（供从未拉取过的会话首次渲染）。 */
function seedPage(message: ChatMessage): InfiniteData<ChatMessagesPage> {
  return {
    pages: [{ messages: [message], limit: 50, has_more: false, next_cursor: null }],
    pageParams: [null],
  };
}

/** upsert 到缓存时的可选行为配置。 */
export interface UpsertChatMessageOptions {
  /**
   * 会话从未被拉取过时是否创建缓存条目。
   *
   * 发送路径传 `true`：一个全新会话必须在任何 fetch 之前渲染出它的第一条消息，
   * 且发送自己的失效会在之后立刻对账这个占位页。
   *
   * WebSocket 处理器传 `false`（默认）。针对"本客户端没打开过的会话"的事件，
   * 绝不能捏造一份一消息历史、让之后的读者误以为它就是完整会话记录——
   * 首次真实拉取才拥有那份权利。
   */
  seedIfMissing?: boolean;
}

/**
 * 把一条已落定的消息 upsert 进两个消息缓存。
 *
 * 两个都要写，因为两个都会被读：`chatKeys.messagesPage` 支撑聊天界面，
 * `chatKeys.messages` 支撑 Agent Builder。只写一个不写另一个，它们就会漂移。
 * @param qc React Query 客户端。
 * @param sessionId 会话 id。
 * @param message 已落定的消息。
 * @param options 是否在缓存缺失时播种（默认否）。
 */
export function upsertChatMessageToCaches(
  qc: QueryClient,
  sessionId: string,
  message: ChatMessage,
  { seedIfMissing = false }: UpsertChatMessageOptions = {},
): void {
  qc.setQueryData<ChatMessage[] | undefined>(chatKeys.messages(sessionId), (old) => {
    if (!old) return seedIfMissing ? [message] : old;
    return upsertIntoList(old, message);
  });
  qc.setQueryData<InfiniteData<ChatMessagesPage> | undefined>(
    chatKeys.messagesPage(sessionId),
    (old) => {
      if (!old?.pages.length) return seedIfMissing ? seedPage(message) : old;
      return upsertIntoPages(old, message);
    },
  );
}
