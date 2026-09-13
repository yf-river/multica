// Chat 的客户端全局 store：浮窗开合、尺寸/展开、选中的 Agent/项目/会话、
// 各会话输入草稿与上传、草稿恢复账本等。直接读写 localStorage（不走 Zustand persist），
// 因为数据要么按 slug 隔离、要么按用户全局，且要兼容旧版草稿形态的迁移。
import { create } from "zustand";
import type { StorageAdapter } from "../types";
import type { Attachment } from "../types/attachment";
import { getCurrentSlug, registerForWorkspaceRehydration } from "../platform/workspace-storage";
import { registerDraftCleanup } from "../drafts/cleanup-registry";
import {
  normalizeStoredUploads,
  attachmentToDraftUpload,
  type DraftUpload,
  type PendingDraftUpload,
} from "../drafts/draft-upload";
import { createLogger } from "../logger";

const logger = createLogger("chat.store");

/** 上次选中 Agent 的存储 key（按工作区隔离）。 */
const AGENT_STORAGE_KEY = "multica:chat:selectedAgentId";
/** 上次选中项目上下文的存储 key（按工作区隔离）。 */
const PROJECT_STORAGE_KEY = "multica:chat:selectedProjectId";
/** 当前活跃会话 id 的存储 key（按工作区隔离）。 */
const SESSION_STORAGE_KEY = "multica:chat:activeSessionId";
/** 草稿以"每个工作区一个 JSON blob"存储：{ [sessionId]: text }。 */
const DRAFTS_KEY = "multica:chat:drafts";
/** 草稿附件记录，按工作区：{ [sessionId]: DraftUpload[] }。 */
const DRAFT_ATTACHMENTS_KEY = "multica:chat:draft-attachments";
/**
 * 本客户端已写进某个输入框的"持久草稿恢复"（#5219）的 id。
 * 需要持久化，因为紧随其后的服务端消费可能丢失（重试耗尽、应用中途关闭），
 * 该行下次拉取又会被重新弹出——把一条用户已发过的提示词再次恢复出来。
 * 有账本在，交接最多一次：在这里的 id 永不再弹出，只会继续对账（再次消费）
 * 直到该行真正消失。
 */
const APPLIED_RESTORES_KEY = "multica:chat:applied-draft-restores";
/**
 * 本地"等待到达某输入框"的恢复请求，按会话排队（#5219）。
 *
 * 这些是"没有服务端副本"的恢复——发送失败，或取消当场同步应答。发送已经清掉了
 * 持久化草稿，所以这个队列是它们正文唯一存在的地方。正因如此必须持久化：一条输入框
 * 还无法处理的请求（用户在另一个会话，或本会话正在编辑）必须扛过卸载、刷新、关闭，
 * 等用户回来时再弹出。
 *
 * 有服务端行的持久恢复刻意不进这里——它们可重取，丢一条无损失。
 */
const PENDING_SEND_RESTORES_KEY = "multica:chat:pending-send-restores";
/**
 * 尚未创建出来的 chat 的草稿槽位。每个工作区恰好一个：新聊天输入框的身份是
 * "我还没创建的那个聊天"，而不是"我和 Agent X 还没创建的那个聊天"。
 * `selectedAgentId` 是发送目标，不是草稿归属，所以写到一半换 Agent 会保留正文
 * （MUL-4864）。已创建的会话用自己的槽位，以 session id 为 key。
 */
export const DRAFT_NEW_SESSION = "__new__";

/** MUL-4864 之前的"按 Agent 分"新聊天槽位，形如 `__new__:<agentId>`。 */
const LEGACY_NEW_SESSION_PREFIX = `${DRAFT_NEW_SESSION}:`;
const CHAT_WIDTH_KEY = "multica:chat:width";
const CHAT_HEIGHT_KEY = "multica:chat:height";
const CHAT_EXPANDED_KEY = "multica:chat:expanded";
/**
 * 浮窗开/合的偏好，全局持久化（不按工作区）——多数用户跨工作区只有一种习惯性的
 * 聊天面板偏好。key 缺失 = 新用户（或存储被清）；默认关，让打开工作区绝不擅自弹出
 * 聊天窗（FAB 负责让人发现它）。一旦用户开关过一次，之后每次刷新都尊重其显式选择。
 */
const OPEN_KEY = "multica:chat:isOpen";
/**
 * 浮动聊天窗（FAB + 覆盖层）是否整体可用，与 OPEN_KEY 一样全局持久化。
 * 这是 Settings → Chat 的偏好：关掉后 FAB/覆盖层永不挂载，Chat 只活在自己的标签里。
 * key 缺失 = 默认开——浮窗默认开启，可从 Settings → Chat 关。
 */
const FLOATING_KEY = "multica:chat:floatingChatEnabled";

/** 从存储读草稿 map；解析失败或缺失返回空对象。 */
function readDrafts(storage: StorageAdapter, key: string): Record<string, string> {
  const raw = storage.getItem(key);
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    return typeof parsed === "object" && parsed !== null ? parsed : {};
  } catch {
    return {};
  }
}

/** 写草稿 map，先清掉空字符串条目防止 blob 无限增长；全空则直接删 key。 */
function writeDrafts(storage: StorageAdapter, key: string, drafts: Record<string, string>) {
  // 修剪空条目，避免 blob 无界增长。
  const pruned: Record<string, string> = {};
  for (const [k, v] of Object.entries(drafts)) {
    if (v) pruned[k] = v;
  }
  if (Object.keys(pruned).length === 0) {
    storage.removeItem(key);
  } else {
    storage.setItem(key, JSON.stringify(pruned));
  }
}

/** 对持久化恢复里的服务端 Attachment 行做形状检查。 */
function isAttachmentDraft(value: unknown): value is Attachment {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as { id?: unknown }).id === "string" &&
    typeof (value as { filename?: unknown }).filename === "string"
  );
}

/** 从存储读草稿附件 map，并逐条归一化（见 normalizeStoredUploads 规则）。 */
function readDraftAttachments(storage: StorageAdapter, key: string): Record<string, DraftUpload[]> {
  const raw = storage.getItem(key);
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) return {};
    const out: Record<string, DraftUpload[]> = {};
    for (const [draftKey, value] of Object.entries(parsed)) {
      if (!Array.isArray(value)) continue;
      // 每次加载都归一化（MUL-5181 L2）：L2 前持久化的裸 Attachment 行变成
      // `uploaded` 占位；加载时仍 `uploading` 的上传被丢弃（字节已没，正文也没引用它）。
      const uploads = normalizeStoredUploads(value);
      if (uploads.length > 0) out[draftKey] = uploads;
    }
    return out;
  } catch {
    return {};
  }
}

/** 读"已应用恢复"账本（字符串 id 数组）。 */
function readAppliedRestores(storage: StorageAdapter, key: string): string[] {
  const raw = storage.getItem(key);
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((id): id is string => typeof id === "string");
  } catch {
    return [];
  }
}

/** 写"已应用恢复"账本；为空则删 key。 */
function writeAppliedRestores(storage: StorageAdapter, key: string, ids: string[]) {
  if (ids.length === 0) storage.removeItem(key);
  else storage.setItem(key, JSON.stringify(ids));
}

/** 判断值是否是合法的"待发送恢复"条目形状。 */
function isPendingSendRestore(value: unknown): value is PendingSendRestore {
  if (typeof value !== "object" || value === null) return false;
  const v = value as { id?: unknown; content?: unknown; sessionId?: unknown };
  return (
    typeof v.id === "string" && typeof v.content === "string" && typeof v.sessionId === "string"
  );
}

/** 从存储读"待发送恢复"队列（按会话），并做形状过滤 + 附件清洗。 */
function readPendingSendRestores(
  storage: StorageAdapter,
  key: string,
): Record<string, PendingSendRestore[]> {
  const raw = storage.getItem(key);
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) return {};
    const out: Record<string, PendingSendRestore[]> = {};
    for (const [sessionId, value] of Object.entries(parsed)) {
      if (!Array.isArray(value)) continue;
      const queued = value.filter(isPendingSendRestore).map((r) => ({
        ...r,
        attachments: Array.isArray(r.attachments) ? r.attachments.filter(isAttachmentDraft) : [],
      }));
      if (queued.length > 0) out[sessionId] = queued;
    }
    return out;
  } catch {
    return {};
  }
}

/** 写"待发送恢复"队列（按会话）；清空后删 key。 */
function writePendingSendRestores(
  storage: StorageAdapter,
  key: string,
  queues: Record<string, PendingSendRestore[]>,
) {
  const pruned: Record<string, PendingSendRestore[]> = {};
  for (const [k, v] of Object.entries(queues)) {
    if (v.length > 0) pruned[k] = v;
  }
  if (Object.keys(pruned).length === 0) storage.removeItem(key);
  else storage.setItem(key, JSON.stringify(pruned));
}

/** 写草稿附件 map；清空后删 key。 */
function writeDraftAttachments(
  storage: StorageAdapter,
  key: string,
  drafts: Record<string, DraftUpload[]>,
) {
  const pruned: Record<string, DraftUpload[]> = {};
  for (const [k, v] of Object.entries(drafts)) {
    if (v.length > 0) pruned[k] = v;
  }
  if (Object.keys(pruned).length === 0) {
    storage.removeItem(key);
  } else {
    storage.setItem(key, JSON.stringify(pruned));
  }
}

/**
 * 把遗留的"按 Agent 分"新聊天槽位折叠进统一的 DRAFT_NEW_SESSION 槽，然后删掉它们。
 *
 * 遗留槽位不带时间戳，多个并存时无法判断用户最后敲的是哪个——而"全部保留"在现在
 * 只有一个输入框的情况下无处安放失败者。采纳属于持久化 `selectedAgentId` 的那个槽
 * （打开工作区时会显示的那份草稿，因此是用户唯一可能期待的那份），其余丢弃：
 * 多出来的槽正是本次改动要移除的"隐形多草稿状态"。
 *
 * 两个写路径都会修剪空值，所以 key 存在即代表有内容。
 * 幂等：遗留 key 一旦删完，就是一次零分配的 no-op。
 */
function migrateLegacyNewChatSlots<T>(
  slots: Record<string, T>,
  selectedAgentId: string | null,
): { slots: Record<string, T>; changed: boolean } {
  const legacyKeys = Object.keys(slots).filter((k) => k.startsWith(LEGACY_NEW_SESSION_PREFIX));
  if (legacyKeys.length === 0) return { slots, changed: false };
  const next = { ...slots };
  const adopted = next[`${LEGACY_NEW_SESSION_PREFIX}${selectedAgentId ?? ""}`];
  // 绝不用遗留内容覆盖统一槽位：统一槽里不管是什么都是在现行方案下写的，
  // 必然比任何遗留残留更新。
  if (!(DRAFT_NEW_SESSION in next) && adopted !== undefined) {
    next[DRAFT_NEW_SESSION] = adopted;
  }
  for (const key of legacyKeys) delete next[key];
  logger.info("migrating legacy per-agent new-chat drafts", {
    legacyCount: legacyKeys.length,
    selectedAgentId,
    adopted: DRAFT_NEW_SESSION in next,
  });
  return { slots: next, changed: true };
}

/**
 * 同时读取两份草稿 map，并对着同一个 `selectedAgentId` 一起迁移——正文与附件
 * 绝不能对"哪份遗留新聊天草稿存活"产生分歧，否则用户会得到 Agent A 的文字配
 * Agent B 的文件。
 */
function loadDraftSlots(
  storage: StorageAdapter,
  draftsKey: string,
  attachmentsKey: string,
  selectedAgentId: string | null,
): { inputDrafts: Record<string, string>; inputDraftAttachments: Record<string, DraftUpload[]> } {
  const drafts = migrateLegacyNewChatSlots(readDrafts(storage, draftsKey), selectedAgentId);
  const attachments = migrateLegacyNewChatSlots(
    readDraftAttachments(storage, attachmentsKey),
    selectedAgentId,
  );
  if (drafts.changed) writeDrafts(storage, draftsKey, drafts.slots);
  if (attachments.changed) writeDraftAttachments(storage, attachmentsKey, attachments.slots);
  return { inputDrafts: drafts.slots, inputDraftAttachments: attachments.slots };
}

/** 聊天浮窗的最小尺寸。 */
export const CHAT_MIN_W = 360;
export const CHAT_MIN_H = 480;
/** 聊天浮窗的默认尺寸。 */
export const CHAT_DEFAULT_W = 380;
export const CHAT_DEFAULT_H = 600;

/**
 * 保留为公开类型，因为既有消费者（chat-message-list、views/chat 类型）会 import 它。
 * 条目本身已不住在 store 里——它们经 React Query 缓存以任务 id 为 key 流动。
 */
export interface ChatTimelineItem {
  seq: number;
  type: "tool_use" | "tool_result" | "thinking" | "text" | "error";
  tool?: string;
  content?: string;
  input?: Record<string, unknown>;
  output?: string;
  created_at?: string;
}

/**
 * 一份"没有服务端副本"的恢复，等待能接住它的输入框。它的正文只存在于别处之外
 * （已在持久存储里，直到被应用——见 PENDING_SEND_RESTORES_KEY）。
 */
export interface PendingSendRestore {
  id: string;
  content: string;
  attachments?: Attachment[];
  /** 这条恢复所属会话的输入框。永不为空。 */
  sessionId: string;
}

/** Chat 客户端 store 的全部状态与方法。 */
export interface ChatState {
  /** 聊天浮窗是否打开。 */
  isOpen: boolean;
  /** 设置偏好：浮动聊天窗整体是否可用。 */
  floatingChatEnabled: boolean;
  /** 当前活跃会话 id。 */
  activeSessionId: string | null;
  /** 当前选中（要发起新聊天）的 Agent id。 */
  selectedAgentId: string | null;
  /** 下一条会话的项目上下文。已有会话保持绑定自己服务端持久化的 project_id。 */
  selectedProjectId: string | null;
  /** 各会话的输入草稿：sessionId（或 DRAFT_NEW_SESSION）→ markdown 正文。 */
  inputDrafts: Record<string, string>;
  /** 各草稿槽位引用的附件（上传协调器持有：占位 + 已完成）。 */
  inputDraftAttachments: Record<string, DraftUpload[]>;
  /** 已写进某输入框的持久草稿恢复 id（#5219）。 */
  appliedDraftRestoreIds: string[];
  /** 各会话排队等待输入框的"无服务端副本"恢复（#5219）。 */
  pendingSendRestores: Record<string, PendingSendRestore[]>;
  /** 用户手选的原尺寸——不做夹取，UI 层渲染时再夹。 */
  chatWidth: number;
  chatHeight: number;
  /** 是否处于展开（最大化）模式。 */
  isExpanded: boolean;
  /** 打开/关闭浮窗。 */
  setOpen: (open: boolean) => void;
  /** 切换浮窗开合。 */
  toggle: () => void;
  /** 设置浮动聊天窗整体是否可用。 */
  setFloatingChatEnabled: (enabled: boolean) => void;
  /** 切换当前活跃会话。 */
  setActiveSession: (id: string | null) => void;
  /** 设置当前选中 Agent（发起新聊天的对象）。 */
  setSelectedAgentId: (id: string) => void;
  /** 设置下一条会话的项目上下文。 */
  setSelectedProjectId: (id: string | null) => void;
  /** sessionId 接受真实会话 UUID 或 DRAFT_NEW_SESSION。 */
  setInputDraft: (sessionId: string, draft: string) => void;
  /** 往某草稿槽位追加 markdown 片段（上传写回）。 */
  appendToInputDraft: (sessionId: string, markdown: string) => void;
  setInputDraftAttachments: (sessionId: string, uploads: DraftUpload[]) => void;
  /** 把一条已完成的服务端行记为 uploaded 条目（恢复路径用）。 */
  addInputDraftAttachment: (sessionId: string, attachment: Attachment) => void;
  /** 选中文件的瞬间记录一个占位（协调器持有）。 */
  addInputDraftUpload: (sessionId: string, upload: DraftUpload) => void;
  /** 把占位换成完成的附件；占位不在则什么都不做。 */
  settleInputDraftUpload: (sessionId: string, clientUploadId: string, attachment: Attachment) => void;
  /** 把占位标记为失败；占位不在则什么都不做。 */
  failInputDraftUpload: (sessionId: string, clientUploadId: string, error?: string) => void;
  /** 丢弃一个占位（关闭失败/中断提示）。 */
  removeInputDraftUpload: (sessionId: string, clientUploadId: string) => void;
  /** 清空某会话的草稿（正文 + 附件）。 */
  clearInputDraft: (sessionId: string) => void;
  /** 记录一条持久恢复已到达输入框；刷新后依然生效。 */
  markDraftRestoreApplied: (restoreId: string) => void;
  /** 服务端行确认消失后，删掉账本条目。 */
  forgetDraftRestoreApplied: (restoreId: string) => void;
  /** 为某会话排队一条无服务端副本的恢复；扛过卸载/刷新。 */
  enqueuePendingSendRestore: (restore: PendingSendRestore) => void;
  /** 恢复正文安全落进（持久化的）草稿后，从队列移除。 */
  dequeuePendingSendRestore: (sessionId: string, restoreId: string) => void;
  /** 持久化原始尺寸，并自动退出展开模式。 */
  setChatSize: (width: number, height: number) => void;
  setExpanded: (expanded: boolean) => void;
}

/** 创建 chat store 所需的配置。 */
export interface ChatStoreOptions {
  storage: StorageAdapter;
}

/** 创建 Chat 客户端 store（应用启动时调用一次并注册为单例）。 */
export function createChatStore(options: ChatStoreOptions) {
  const { storage } = options;

  /** 把基础 key 带上当前工作区 slug 作后缀，实现按工作区隔离。 */
  const wsKey = (base: string) => {
    const slug = getCurrentSlug();
    return slug ? `${base}:${slug}` : base;
  };

  // 从存储解析初始 isOpen。三态读取（null / "true" / "false"）既保留"新用户 → 关"
  // 的默认，又在每次刷新时尊重显式的"我把它打开了"选择。
  const storedOpen = storage.getItem(OPEN_KEY);
  const initialIsOpen = storedOpen === "true";

  // 默认开：除非用户在 Settings → Chat 里显式关掉（"false"），否则浮窗可用。
  // key 缺失（新用户）解析为可用。
  const initialFloatingEnabled = storage.getItem(FLOATING_KEY) !== "false";

  const initialAgentId = storage.getItem(wsKey(AGENT_STORAGE_KEY));
  const initialDraftSlots = loadDraftSlots(
    storage,
    wsKey(DRAFTS_KEY),
    wsKey(DRAFT_ATTACHMENTS_KEY),
    initialAgentId,
  );

  const store = create<ChatState>((set, get) => ({
    isOpen: initialIsOpen,
    floatingChatEnabled: initialFloatingEnabled,
    activeSessionId: storage.getItem(wsKey(SESSION_STORAGE_KEY)),
    selectedAgentId: initialAgentId,
    selectedProjectId: storage.getItem(wsKey(PROJECT_STORAGE_KEY)),
    inputDrafts: initialDraftSlots.inputDrafts,
    inputDraftAttachments: initialDraftSlots.inputDraftAttachments,
    appliedDraftRestoreIds: readAppliedRestores(storage, wsKey(APPLIED_RESTORES_KEY)),
    pendingSendRestores: readPendingSendRestores(storage, wsKey(PENDING_SEND_RESTORES_KEY)),
    chatWidth: Number(storage.getItem(CHAT_WIDTH_KEY)) || CHAT_DEFAULT_W,
    chatHeight: Number(storage.getItem(CHAT_HEIGHT_KEY)) || CHAT_DEFAULT_H,
    isExpanded: storage.getItem(wsKey(CHAT_EXPANDED_KEY)) === "true",
    setOpen: (open) => {
      logger.debug("setOpen", { from: get().isOpen, to: open });
      storage.setItem(OPEN_KEY, String(open));
      set({ isOpen: open });
    },
    toggle: () => {
      const next = !get().isOpen;
      logger.debug("toggle", { to: next });
      storage.setItem(OPEN_KEY, String(next));
      set({ isOpen: next });
    },
    setFloatingChatEnabled: (enabled) => {
      logger.info("setFloatingChatEnabled", { to: enabled });
      storage.setItem(FLOATING_KEY, String(enabled));
      // 关掉功能时也应把开着的覆盖层收起，免得它赖到下一次开关。
      set(enabled ? { floatingChatEnabled: true } : { floatingChatEnabled: false, isOpen: false });
      if (!enabled) storage.setItem(OPEN_KEY, "false");
    },
    setActiveSession: (id) => {
      logger.info("setActiveSession", { from: get().activeSessionId, to: id });
      if (id) {
        storage.setItem(wsKey(SESSION_STORAGE_KEY), id);
      } else {
        storage.removeItem(wsKey(SESSION_STORAGE_KEY));
      }
      set({ activeSessionId: id });
    },
    setSelectedAgentId: (id) => {
      logger.info("setSelectedAgentId", { from: get().selectedAgentId, to: id });
      storage.setItem(wsKey(AGENT_STORAGE_KEY), id);
      set({ selectedAgentId: id });
    },
    setSelectedProjectId: (id) => {
      logger.info("setSelectedProjectId", { from: get().selectedProjectId, to: id });
      if (id) storage.setItem(wsKey(PROJECT_STORAGE_KEY), id);
      else storage.removeItem(wsKey(PROJECT_STORAGE_KEY));
      set({ selectedProjectId: id });
    },
    // 只增不删直到服务端确认。刻意不设容量上限：这里每条都是 UNconfirmed 消费，
    // 逐出一条会静默重新武装它压制着的恢复——服务端行还在，下次拉取会把一条已应用
    // 的提示词再次弹出，用户可能发它两次。条目只经 forgetDraftRestoreApplied 离开，
    // 即只有行被证实消失才删，这也就把账本边界限制在"消费仍在失败中的恢复数"。
    markDraftRestoreApplied: (restoreId) => {
      const current = get().appliedDraftRestoreIds;
      if (current.includes(restoreId)) return;
      const next = [...current, restoreId];
      writeAppliedRestores(storage, wsKey(APPLIED_RESTORES_KEY), next);
      set({ appliedDraftRestoreIds: next });
    },
    /** 只在确认消费成功（服务端行已消失）时调用。 */
    forgetDraftRestoreApplied: (restoreId) => {
      const current = get().appliedDraftRestoreIds;
      if (!current.includes(restoreId)) return;
      const next = current.filter((id) => id !== restoreId);
      writeAppliedRestores(storage, wsKey(APPLIED_RESTORES_KEY), next);
      set({ appliedDraftRestoreIds: next });
    },
    // 按会话排队、不用一个共享槽：对用户当前没看的会话的请求绝不能占住输入框唯一的
    // 恢复槽（会饿死用户正在看的那个会话），而它又没有服务端副本可回退，所以也不能
    // 干脆丢掉。FIFO，让同一会话的两次失败都被恢复，最旧的先来。
    enqueuePendingSendRestore: (restore) => {
      if (!restore.sessionId || !restore.id) return;
      const current = get().pendingSendRestores;
      const existing = current[restore.sessionId] ?? [];
      if (existing.some((r) => r.id === restore.id)) return;
      logger.info("enqueuePendingSendRestore", {
        sessionId: restore.sessionId,
        restoreId: restore.id,
      });
      const next = { ...current, [restore.sessionId]: [...existing, restore] };
      writePendingSendRestores(storage, wsKey(PENDING_SEND_RESTORES_KEY), next);
      set({ pendingSendRestores: next });
    },
    /** 只在正文已落进（本身已持久化的）草稿之后调用。 */
    dequeuePendingSendRestore: (sessionId, restoreId) => {
      const current = get().pendingSendRestores;
      const existing = current[sessionId];
      if (!existing?.some((r) => r.id === restoreId)) return;
      logger.info("dequeuePendingSendRestore", { sessionId, restoreId });
      const remaining = existing.filter((r) => r.id !== restoreId);
      const next = { ...current };
      if (remaining.length > 0) next[sessionId] = remaining;
      else delete next[sessionId];
      writePendingSendRestores(storage, wsKey(PENDING_SEND_RESTORES_KEY), next);
      set({ pendingSendRestores: next });
    },
    setInputDraft: (sessionId, draft) => {
      // Debug 级——onUpdate 每次按键都触发。
      logger.debug("setInputDraft", { sessionId, length: draft.length });
      const next = { ...get().inputDrafts, [sessionId]: draft };
      writeDrafts(storage, wsKey(DRAFTS_KEY), next);
      set({ inputDrafts: next });
    },
    appendToInputDraft: (sessionId, markdown) => {
      const existing = get().inputDrafts[sessionId] ?? "";
      // 已有正文时清掉行尾空白再补空行分隔，避免 markdown 粘成一团。
      const draft = existing.trim()
        ? `${existing.replace(/\s+$/, "")}\n\n${markdown}`
        : markdown;
      logger.debug("appendToInputDraft", { sessionId, length: draft.length });
      const next = { ...get().inputDrafts, [sessionId]: draft };
      writeDrafts(storage, wsKey(DRAFTS_KEY), next);
      set({ inputDrafts: next });
    },
    setInputDraftAttachments: (sessionId, uploads) => {
      logger.debug("setInputDraftAttachments", { sessionId, count: uploads.length });
      const next = { ...get().inputDraftAttachments };
      if (uploads.length > 0) next[sessionId] = uploads;
      else delete next[sessionId];
      writeDraftAttachments(storage, wsKey(DRAFT_ATTACHMENTS_KEY), next);
      set({ inputDraftAttachments: next });
    },
    addInputDraftAttachment: (sessionId, attachment) => {
      if (!attachment.id) return;
      const current = get().inputDraftAttachments;
      const existing = current[sessionId] ?? [];
      const wrapped = attachmentToDraftUpload(attachment);
      // 同 id 的 uploaded 条目已存在则替换为最新包装，否则追加。
      const nextForKey = existing.some(
        (u) => u.status === "uploaded" && u.attachment.id === attachment.id,
      )
        ? existing.map((u) =>
            u.status === "uploaded" && u.attachment.id === attachment.id ? wrapped : u,
          )
        : [...existing, wrapped];
      const next = { ...current, [sessionId]: nextForKey };
      writeDraftAttachments(storage, wsKey(DRAFT_ATTACHMENTS_KEY), next);
      set({ inputDraftAttachments: next });
    },
    addInputDraftUpload: (sessionId, upload) => {
      const current = get().inputDraftAttachments;
      const existing = current[sessionId] ?? [];
      // 同 clientUploadId 已存在则跳过（幂等）。
      if (existing.some((u) => u.clientUploadId === upload.clientUploadId)) return;
      const next = { ...current, [sessionId]: [...existing, upload] };
      writeDraftAttachments(storage, wsKey(DRAFT_ATTACHMENTS_KEY), next);
      set({ inputDraftAttachments: next });
    },
    settleInputDraftUpload: (sessionId, clientUploadId, attachment) => {
      const current = get().inputDraftAttachments;
      const existing = current[sessionId] ?? [];
      // 占位已不在则什么都不做。
      if (!existing.some((u) => u.clientUploadId === clientUploadId)) return;
      const nextForKey = existing.map((u) =>
        u.clientUploadId === clientUploadId
          ? { ...attachmentToDraftUpload(attachment), clientUploadId }
          : u,
      );
      const next = { ...current, [sessionId]: nextForKey };
      writeDraftAttachments(storage, wsKey(DRAFT_ATTACHMENTS_KEY), next);
      set({ inputDraftAttachments: next });
    },
    failInputDraftUpload: (sessionId, clientUploadId, error) => {
      const current = get().inputDraftAttachments;
      const existing = current[sessionId] ?? [];
      const target = existing.find((u) => u.clientUploadId === clientUploadId);
      if (!target) return;
      const failed: PendingDraftUpload = {
        clientUploadId,
        status: "failed",
        filename: target.filename,
        size: target.size,
        contentType: target.contentType,
        error,
      };
      const nextForKey = existing.map((u) => (u.clientUploadId === clientUploadId ? failed : u));
      const next = { ...current, [sessionId]: nextForKey };
      writeDraftAttachments(storage, wsKey(DRAFT_ATTACHMENTS_KEY), next);
      set({ inputDraftAttachments: next });
    },
    removeInputDraftUpload: (sessionId, clientUploadId) => {
      const current = get().inputDraftAttachments;
      const existing = current[sessionId] ?? [];
      // 占位已不在则什么都不做。
      if (!existing.some((u) => u.clientUploadId === clientUploadId)) return;
      const remaining = existing.filter((u) => u.clientUploadId !== clientUploadId);
      const next = { ...current };
      if (remaining.length > 0) next[sessionId] = remaining;
      else delete next[sessionId];
      writeDraftAttachments(storage, wsKey(DRAFT_ATTACHMENTS_KEY), next);
      set({ inputDraftAttachments: next });
    },
    clearInputDraft: (sessionId) => {
      const currentDrafts = get().inputDrafts;
      const currentAttachments = get().inputDraftAttachments;
      if (!(sessionId in currentDrafts) && !(sessionId in currentAttachments)) {
        logger.debug("clearInputDraft skipped (no draft)", { sessionId });
        return;
      }
      logger.info("clearInputDraft", { sessionId });
      const nextDrafts = { ...currentDrafts };
      const nextAttachments = { ...currentAttachments };
      delete nextDrafts[sessionId];
      delete nextAttachments[sessionId];
      writeDrafts(storage, wsKey(DRAFTS_KEY), nextDrafts);
      writeDraftAttachments(storage, wsKey(DRAFT_ATTACHMENTS_KEY), nextAttachments);
      set({ inputDrafts: nextDrafts, inputDraftAttachments: nextAttachments });
    },
    setChatSize: (w, h) => {
      logger.debug("setChatSize", { w, h });
      storage.setItem(CHAT_WIDTH_KEY, String(w));
      storage.setItem(CHAT_HEIGHT_KEY, String(h));
      // 拖拽 = 用户选择了手动尺寸 → 退出展开模式。
      storage.removeItem(wsKey(CHAT_EXPANDED_KEY));
      set({ chatWidth: w, chatHeight: h, isExpanded: false });
    },
    setExpanded: (expanded) => {
      logger.info("setExpanded", { to: expanded });
      if (expanded) {
        storage.setItem(wsKey(CHAT_EXPANDED_KEY), "true");
      } else {
        storage.removeItem(wsKey(CHAT_EXPANDED_KEY));
      }
      set({ isExpanded: expanded });
    },
  }));

  // 把 chat 草稿的持久化 key 注册进草稿清理系统，让登出/删工作区像其它草稿 store
  // 一样清掉（此前会泄漏——chat 草稿、附件、已应用恢复账本在客户端登出后仍存活到
  // 同一标签页的下一次登录）。全部按工作区作用域（经 `wsKey` 持久化，即
  // `${base}:${slug}`），每个条目只重置自己那片内存态。无服务端副本的恢复队列也注册，
  // 让它的可恢复正文不活得比用户更久。
  registerDraftCleanup({
    storageKey: DRAFTS_KEY,
    workspaceScoped: true,
    resetInMemory: () => store.setState({ inputDrafts: {} }),
  });
  registerDraftCleanup({
    storageKey: DRAFT_ATTACHMENTS_KEY,
    workspaceScoped: true,
    resetInMemory: () => store.setState({ inputDraftAttachments: {} }),
  });
  registerDraftCleanup({
    storageKey: APPLIED_RESTORES_KEY,
    workspaceScoped: true,
    resetInMemory: () => store.setState({ appliedDraftRestoreIds: [] }),
  });
  registerDraftCleanup({
    storageKey: PENDING_SEND_RESTORES_KEY,
    workspaceScoped: true,
    resetInMemory: () => store.setState({ pendingSendRestores: {} }),
  });

  registerForWorkspaceRehydration(() => {
    const nextSession = storage.getItem(wsKey(SESSION_STORAGE_KEY));
    const nextAgent = storage.getItem(wsKey(AGENT_STORAGE_KEY));
    const nextProject = storage.getItem(wsKey(PROJECT_STORAGE_KEY));
    // 草稿按工作区命名空间，因此要切换去的那个工作区有自己的遗留槽位要折叠——
    // 应对着那个工作区自己的持久化 Agent 迁移，而不是我们正要离开的那个。
    const { inputDrafts: nextDrafts, inputDraftAttachments: nextDraftAttachments } = loadDraftSlots(
      storage,
      wsKey(DRAFTS_KEY),
      wsKey(DRAFT_ATTACHMENTS_KEY),
      nextAgent,
    );
    logger.info("workspace rehydration", {
      prevSession: store.getState().activeSessionId,
      nextSession,
      prevAgent: store.getState().selectedAgentId,
      nextAgent,
      prevProject: store.getState().selectedProjectId,
      nextProject,
      draftCount: Object.keys(nextDrafts).length,
      draftAttachmentCount: Object.keys(nextDraftAttachments).length,
    });
    // 整体换成新工作区的持久化状态。
    store.setState({
      activeSessionId: nextSession,
      selectedAgentId: nextAgent,
      selectedProjectId: nextProject,
      inputDrafts: nextDrafts,
      inputDraftAttachments: nextDraftAttachments,
      appliedDraftRestoreIds: readAppliedRestores(storage, wsKey(APPLIED_RESTORES_KEY)),
      pendingSendRestores: readPendingSendRestores(storage, wsKey(PENDING_SEND_RESTORES_KEY)),
    });
  });

  return store;
}
