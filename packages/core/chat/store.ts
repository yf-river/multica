import { create } from "zustand";
import type { StorageAdapter } from "../types";
import type { Attachment } from "../types/attachment";
import {
  createWorkspaceAwareStorage,
  getCurrentSlug,
  registerWorkspaceStoreLifecycle,
} from "../platform/workspace-storage";

const AGENT_STORAGE_KEY = "multica:chat:selectedAgentId";
const SESSION_STORAGE_KEY = "multica:chat:activeSessionId";
/** Drafts are stored as one JSON blob per workspace: { [sessionId]: text }. */
const DRAFTS_KEY = "multica:chat:drafts";
/** Draft attachment records per workspace: { [sessionId]: Attachment[] }. */
const DRAFT_ATTACHMENTS_KEY = "multica:chat:draft-attachments";
/** Placeholder sessionId for a chat that hasn't been created yet. */
export const DRAFT_NEW_SESSION = "__new__";

/**
 * Draft storage key for an as-yet-uncreated chat with the given agent.
 * Shared by ChatInput (which writes the draft) and ensureSession (which
 * migrates it onto the real session id the moment the session is created),
 * so the two never disagree on the slot name.
 */
export function newSessionDraftKey(selectedAgentId: string | null): string {
  return `${DRAFT_NEW_SESSION}:${selectedAgentId ?? ""}`;
}
const CHAT_WIDTH_KEY = "multica:chat:width";
const CHAT_HEIGHT_KEY = "multica:chat:height";
const CHAT_EXPANDED_KEY = "multica:chat:expanded";
/**
 * Open/closed preference, persisted globally (not per-workspace) — most users
 * have one habitual chat-panel preference across workspaces. Missing key =
 * new user (or cleared storage); default to OPEN so the chat is discoverable.
 * Once the user toggles even once, their explicit choice is respected on
 * every subsequent reload.
 */
const OPEN_KEY = "multica:chat:isOpen";

function readRecord(storage: StorageAdapter, key: string): Record<string, unknown> {
  const raw = storage.getItem(key);
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    return typeof parsed === "object" && parsed !== null ? parsed : {};
  } catch {
    return {};
  }
}

function readDrafts(storage: StorageAdapter, key: string): Record<string, string> {
  return readRecord(storage, key) as Record<string, string>;
}

function writeNonEmptyRecord<Value>(
  storage: StorageAdapter,
  key: string,
  values: Record<string, Value>,
  keep: (value: Value) => boolean,
) {
  const entries = Object.entries(values).filter(([, value]) => keep(value));
  if (entries.length === 0) {
    storage.removeItem(key);
  } else {
    storage.setItem(key, JSON.stringify(Object.fromEntries(entries)));
  }
}

const writeDrafts = (
  storage: StorageAdapter,
  key: string,
  drafts: Record<string, string>,
) => writeNonEmptyRecord(storage, key, drafts, Boolean);

function isAttachmentDraft(value: unknown): value is Attachment {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as { id?: unknown }).id === "string" &&
    typeof (value as { filename?: unknown }).filename === "string"
  );
}

function readDraftAttachments(storage: StorageAdapter, key: string): Record<string, Attachment[]> {
  const out: Record<string, Attachment[]> = {};
  for (const [draftKey, value] of Object.entries(readRecord(storage, key))) {
    if (!Array.isArray(value)) continue;
    const attachments = value.filter(isAttachmentDraft);
    if (attachments.length > 0) out[draftKey] = attachments;
  }
  return out;
}

function writeDraftAttachments(
  storage: StorageAdapter,
  key: string,
  drafts: Record<string, Attachment[]>,
) {
  writeNonEmptyRecord(
    storage,
    key,
    drafts,
    (attachments) => attachments.length > 0,
  );
}

export const CHAT_MIN_W = 360;
export const CHAT_MIN_H = 480;
export const CHAT_DEFAULT_W = 380;
export const CHAT_DEFAULT_H = 600;

interface ChatState {
  isOpen: boolean;
  activeSessionId: string | null;
  selectedAgentId: string | null;
  /** Drafts per session: sessionId (or DRAFT_NEW_SESSION) → markdown text. */
  inputDrafts: Record<string, string>;
  /** Attachment rows referenced by each input draft. */
  inputDraftAttachments: Record<string, Attachment[]>;
  /** Raw user-chosen size — no clamp applied. UI layer clamps at render time. */
  chatWidth: number;
  chatHeight: number;
  isExpanded: boolean;
  setOpen: (open: boolean) => void;
  toggle: () => void;
  setActiveSession: (id: string | null) => void;
  setSelectedAgentId: (id: string) => void;
  /** sessionId accepts a real session UUID or DRAFT_NEW_SESSION. */
  setInputDraft: (sessionId: string, draft: string) => void;
  setInputDraftAttachments: (sessionId: string, attachments: Attachment[]) => void;
  addInputDraftAttachment: (sessionId: string, attachment: Attachment) => void;
  clearInputDraft: (sessionId: string) => void;
  /** Clear a captured workspace's draft without following a later route switch. */
  clearInputDraftForWorkspace: (workspaceSlug: string, sessionId: string) => void;
  /** Persist raw size and auto-exit expanded mode. */
  setChatSize: (width: number, height: number) => void;
  setExpanded: (expanded: boolean) => void;
}

interface ChatStoreOptions {
  storage: StorageAdapter;
}

export function createChatStore(options: ChatStoreOptions) {
  const { storage } = options;
  const workspaceStorage = createWorkspaceAwareStorage(storage);

  // Resolve initial isOpen from storage. The three-state read (null /
  // "true" / "false") is what enables the "new user → open" default while
  // still honouring an explicit "I closed it" choice on every reload.
  const storedOpen = storage.getItem(OPEN_KEY);
  const initialIsOpen = storedOpen === null ? true : storedOpen === "true";

  const store = create<ChatState>((set, get) => ({
    isOpen: initialIsOpen,
    activeSessionId: workspaceStorage.getItem(SESSION_STORAGE_KEY),
    selectedAgentId: workspaceStorage.getItem(AGENT_STORAGE_KEY),
    inputDrafts: readDrafts(workspaceStorage, DRAFTS_KEY),
    inputDraftAttachments: readDraftAttachments(
      workspaceStorage,
      DRAFT_ATTACHMENTS_KEY,
    ),
    chatWidth: Number(storage.getItem(CHAT_WIDTH_KEY)) || CHAT_DEFAULT_W,
    chatHeight: Number(storage.getItem(CHAT_HEIGHT_KEY)) || CHAT_DEFAULT_H,
    isExpanded: workspaceStorage.getItem(CHAT_EXPANDED_KEY) === "true",
    setOpen: (open) => {
      storage.setItem(OPEN_KEY, String(open));
      set({ isOpen: open });
    },
    toggle: () => get().setOpen(!get().isOpen),
    setActiveSession: (id) => {
      if (id) {
        workspaceStorage.setItem(SESSION_STORAGE_KEY, id);
      } else {
        workspaceStorage.removeItem(SESSION_STORAGE_KEY);
      }
      set({ activeSessionId: id });
    },
    setSelectedAgentId: (id) => {
      workspaceStorage.setItem(AGENT_STORAGE_KEY, id);
      set({ selectedAgentId: id });
    },
    setInputDraft: (sessionId, draft) => {
      const next = { ...get().inputDrafts, [sessionId]: draft };
      writeDrafts(workspaceStorage, DRAFTS_KEY, next);
      set({ inputDrafts: next });
    },
    setInputDraftAttachments: (sessionId, attachments) => {
      const next = { ...get().inputDraftAttachments };
      if (attachments.length > 0) next[sessionId] = attachments;
      else delete next[sessionId];
      writeDraftAttachments(workspaceStorage, DRAFT_ATTACHMENTS_KEY, next);
      set({ inputDraftAttachments: next });
    },
    addInputDraftAttachment: (sessionId, attachment) => {
      if (!attachment.id) return;
      const current = get().inputDraftAttachments;
      const existing = current[sessionId] ?? [];
      const nextForKey = existing.some((a) => a.id === attachment.id)
        ? existing.map((a) => (a.id === attachment.id ? attachment : a))
        : [...existing, attachment];
      const next = { ...current, [sessionId]: nextForKey };
      writeDraftAttachments(workspaceStorage, DRAFT_ATTACHMENTS_KEY, next);
      set({ inputDraftAttachments: next });
    },
    clearInputDraft: (sessionId) => {
      const currentDrafts = get().inputDrafts;
      const currentAttachments = get().inputDraftAttachments;
      if (!(sessionId in currentDrafts) && !(sessionId in currentAttachments)) {
        return;
      }
      const nextDrafts = { ...currentDrafts };
      const nextAttachments = { ...currentAttachments };
      delete nextDrafts[sessionId];
      delete nextAttachments[sessionId];
      writeDrafts(workspaceStorage, DRAFTS_KEY, nextDrafts);
      writeDraftAttachments(
        workspaceStorage,
        DRAFT_ATTACHMENTS_KEY,
        nextAttachments,
      );
      set({ inputDrafts: nextDrafts, inputDraftAttachments: nextAttachments });
    },
    clearInputDraftForWorkspace: (workspaceSlug, sessionId) => {
      const draftsKey = `${DRAFTS_KEY}:${workspaceSlug}`;
      const attachmentsKey = `${DRAFT_ATTACHMENTS_KEY}:${workspaceSlug}`;
      const nextDrafts = readDrafts(storage, draftsKey);
      const nextAttachments = readDraftAttachments(storage, attachmentsKey);
      delete nextDrafts[sessionId];
      delete nextAttachments[sessionId];
      writeDrafts(storage, draftsKey, nextDrafts);
      writeDraftAttachments(storage, attachmentsKey, nextAttachments);

      // Only update the mounted editor store if it still represents the same
      // workspace. A late request from workspace A must never mutate B's UI.
      if (getCurrentSlug() === workspaceSlug) {
        set({ inputDrafts: nextDrafts, inputDraftAttachments: nextAttachments });
      }
    },
    setChatSize: (w, h) => {
      storage.setItem(CHAT_WIDTH_KEY, String(w));
      storage.setItem(CHAT_HEIGHT_KEY, String(h));
      // Dragging = user chose a manual size → exit expanded mode
      workspaceStorage.removeItem(CHAT_EXPANDED_KEY);
      set({ chatWidth: w, chatHeight: h, isExpanded: false });
    },
    setExpanded: (expanded) => {
      if (expanded) {
        workspaceStorage.setItem(CHAT_EXPANDED_KEY, "true");
      } else {
        workspaceStorage.removeItem(CHAT_EXPANDED_KEY);
      }
      set({ isExpanded: expanded });
    },
  }));

  registerWorkspaceStoreLifecycle({
    reset: () => {
      store.setState({
        activeSessionId: null,
        selectedAgentId: null,
        inputDrafts: {},
        inputDraftAttachments: {},
        isExpanded: false,
      });
    },
    rehydrate: () => {
      const nextSession = workspaceStorage.getItem(SESSION_STORAGE_KEY);
      const nextAgent = workspaceStorage.getItem(AGENT_STORAGE_KEY);
      const nextDrafts = readDrafts(workspaceStorage, DRAFTS_KEY);
      const nextDraftAttachments = readDraftAttachments(
        workspaceStorage,
        DRAFT_ATTACHMENTS_KEY,
      );
      store.setState({
        activeSessionId: nextSession,
        selectedAgentId: nextAgent,
        inputDrafts: nextDrafts,
        inputDraftAttachments: nextDraftAttachments,
        isExpanded: workspaceStorage.getItem(CHAT_EXPANDED_KEY) === "true",
      });
    },
  });

  return store;
}

type ChatStoreInstance = ReturnType<typeof createChatStore>;

let chatStore: ChatStoreInstance | null = null;

export function registerChatStore(store: ChatStoreInstance) {
  chatStore = store;
}

export const useChatStore: ChatStoreInstance = new Proxy(
  (() => {}) as unknown as ChatStoreInstance,
  {
    apply(_target, _thisArg, args) {
      if (!chatStore) {
        throw new Error(
          "Chat store not initialised — call registerChatStore() first",
        );
      }
      return (chatStore as unknown as (...values: unknown[]) => unknown)(
        ...args,
      );
    },
    get(_target, property) {
      return chatStore ? Reflect.get(chatStore, property) : undefined;
    },
  },
);
