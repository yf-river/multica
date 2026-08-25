/**
 * @vitest-environment jsdom
 */
import {
  QueryClient,
  QueryClientProvider,
  type InfiniteData,
} from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import type { WSClient } from "../api/ws-client";
import { chatKeys } from "../chat/queries";
import { issueKeys } from "../issues/queries";
import { workspaceKeys } from "../workspace/queries";
import {
  DEFAULT_WORKSPACE_SETTINGS,
  type ChatDonePayload,
  type ChatMessage,
  type ChatMessagesPage,
  type ChatPendingTask,
  type Workspace,
  type WSMessage,
} from "../types";
import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

vi.mock("../platform/workspace-storage", () => ({
  getCurrentWsId: () => "ws-1",
  getCurrentSlug: () => "test-ws",
  registerWorkspacePersistStore: () => () => {},
  registerWorkspaceStoreLifecycle: () => () => {},
  registerAccountPersistStore: () => () => {},
}));

vi.mock("../paths", () => ({
  resolvePostAuthDestination: () => "/",
}));

function createMockWs(): WSClient {
  return {
    on: vi.fn(() => () => {}),
    onAny: vi.fn(() => () => {}),
    onReconnect: vi.fn(() => () => {}),
  } as unknown as WSClient;
}

function createCapturingMockWs() {
  let onAnyHandler: ((message: WSMessage) => void) | null = null;
  const eventHandlers = new Map<string, (payload: unknown) => unknown>();
  const ws = {
    on: vi.fn((event: string, handler: (payload: unknown) => unknown) => {
      eventHandlers.set(event, handler);
      return () => eventHandlers.delete(event);
    }),
    onAny: vi.fn((handler: (message: WSMessage) => void) => {
      onAnyHandler = handler;
      return () => {};
    }),
    onReconnect: vi.fn(() => () => {}),
  } as unknown as WSClient;

  return {
    ws,
    emit(message: WSMessage) {
      if (!onAnyHandler) throw new Error("onAny handler is not registered");
      onAnyHandler(message);
    },
    emitEvent(event: string, payload: unknown) {
      const handler = eventHandlers.get(event);
      if (!handler) throw new Error(`${event} handler is not registered`);
      return handler(payload);
    },
  };
}

const sessionId = "session-1";
const messagesKey = chatKeys.messagesPage(sessionId);
const pendingKey = chatKeys.pendingTask(sessionId);

function userMessage(id = "msg-user"): ChatMessage {
  return { id, role: "user", content: id, task_id: null };
}

function messagePages(messages: ChatMessage[]): InfiniteData<ChatMessagesPage> {
  return {
    pages: [{ messages, has_more: false, next_cursor: null }],
    pageParams: [null],
  };
}

function donePayload(overrides: Partial<ChatDonePayload> = {}): ChatDonePayload {
  return {
    chat_session_id: sessionId,
    task_id: "task-1",
    message_id: "msg-assistant",
    content: "done",
    elapsed_ms: 1234,
    ...overrides,
  };
}

function workspace(overrides: Partial<Workspace> = {}): Workspace {
  return {
    id: "ws-1",
    name: "Test",
    slug: "test",
    description: null,
    context: null,
    settings: { ...DEFAULT_WORKSPACE_SETTINGS },
    repos: [],
    issue_prefix: "TES",
    avatar_url: null,
    ...overrides,
  };
}

function createStores(): RealtimeSyncStores {
  return {
    authStore: Object.assign(() => ({}), {
      getState: () => ({ user: { id: "u1" } }),
      subscribe: () => () => {},
      setState: () => {},
      destroy: () => {},
    }),
  } as unknown as RealtimeSyncStores;
}

function createWrapper(qc: QueryClient) {
  // Named function (not arrow) so react/display-name lint rule passes —
  // anonymous render-fn components break that rule even in test files.
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

type RealtimeSyncProps = { ws: WSClient | null };

describe("useRealtimeSync — ws instance change", () => {
  let qc: QueryClient;
  let stores: RealtimeSyncStores;
  let invalidateSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    stores = createStores();
    invalidateSpy = vi.spyOn(qc, "invalidateQueries");
  });

  function renderRealtimeSync(initialWs: WSClient | null) {
    return renderHook(
      ({ ws }: RealtimeSyncProps) => useRealtimeSync(ws, stores),
      { initialProps: { ws: initialWs }, wrapper: createWrapper(qc) },
    );
  }

  function reconnectAfterNullGap(rerender: (props: RealtimeSyncProps) => void) {
    invalidateSpy.mockClear();
    rerender({ ws: null });

    const ws2 = createMockWs();
    rerender({ ws: ws2 });
  }

  function invalidatedQueryKeys() {
    return invalidateSpy.mock.calls.map(
      (call: [{ queryKey?: unknown }, ...unknown[]]) => call[0].queryKey,
    );
  }

  it("skips invalidation on first non-null ws instance", () => {
    const ws = createMockWs();
    renderRealtimeSync(ws);

    // The main effect calls invalidateQueries for its own setup, but the
    // ws-instance-change effect should NOT have fired invalidation.
    // The only invalidateQueries calls should come from the main effect's
    // event handlers, not from the instance-change effect.
    // We verify by checking that no call was made with workspaceKeys.list()
    // pattern from the instance-change path (it logs a specific message).
    // Simpler: count calls — first mount with a ws should not trigger the
    // workspace-scoped bulk invalidation.
    expect(invalidateSpy).not.toHaveBeenCalled();
  });

  it("does not invalidate when ws goes from instance to null", () => {
    const ws1 = createMockWs();
    const { rerender } = renderRealtimeSync(ws1);

    invalidateSpy.mockClear();
    rerender({ ws: null });

    expect(invalidateSpy).not.toHaveBeenCalled();
  });

  it("invalidates exactly once when a new ws instance appears after null gap", () => {
    const ws1 = createMockWs();
    const { rerender } = renderRealtimeSync(ws1);

    // Simulate workspace switch: ws -> null -> new ws
    invalidateSpy.mockClear();
    rerender({ ws: null });
    expect(invalidateSpy).not.toHaveBeenCalled();

    const ws2 = createMockWs();
    rerender({ ws: ws2 });

    // Should have called invalidateQueries for all workspace-scoped keys
    // (14 workspace-scoped + 5 per-issue prefixes + 1 workspaceKeys.list()
    // = 20 calls)
    expect(invalidateSpy).toHaveBeenCalledTimes(20);
  });

  it("does not re-invalidate when rerendered with the same ws instance", () => {
    const ws1 = createMockWs();
    const { rerender } = renderRealtimeSync(ws1);

    invalidateSpy.mockClear();
    // Rerender with same instance
    rerender({ ws: ws1 });

    expect(invalidateSpy).not.toHaveBeenCalled();
  });

  it("invalidates chat, pins, and labels queries on ws instance change", () => {
    const ws1 = createMockWs();
    const { rerender } = renderRealtimeSync(ws1);
    reconnectAfterNullGap(rerender);

    const calls = invalidatedQueryKeys();
    expect(calls).toContainEqual(["chat", "ws-1"]);
    expect(calls).toContainEqual(["labels", "ws-1"]);
  });

  it("invalidates per-issue caches (no wsId in key) on ws instance change", () => {
    // These keys are not under the ["issues", wsId] prefix, so they need
    // their own invalidation on recovery — otherwise events missed while
    // disconnected leave them stale forever (staleTime: Infinity, #3953).
    const ws1 = createMockWs();
    const { rerender } = renderRealtimeSync(ws1);
    reconnectAfterNullGap(rerender);

    const calls = invalidatedQueryKeys();
    expect(calls).toContainEqual(["issues", "timeline"]);
    expect(calls).toContainEqual(["issues", "reactions"]);
    expect(calls).toContainEqual(["issues", "subscribers"]);
    expect(calls).toContainEqual(["issues", "attachments"]);
    expect(calls).toContainEqual(["issues", "tasks"]);
  });

  it("debounces project resource events into one project cache invalidation", () => {
    vi.useFakeTimers();
    const { ws, emit } = createCapturingMockWs();
    const { unmount } = renderRealtimeSync(ws);
    invalidateSpy.mockClear();

    act(() => {
      emit({ type: "project_resource:created", payload: {} });
      emit({ type: "project_resource:updated", payload: {} });
      emit({ type: "project_resource:deleted", payload: {} });
      vi.advanceTimersByTime(100);
    });

    expect(invalidatedQueryKeys().filter(
      (key: unknown) => JSON.stringify(key) === JSON.stringify(["projects", "ws-1"]),
    )).toHaveLength(1);

    unmount();
    vi.useRealTimers();
  });

  it("routes chat message events to the current paged-message cache", () => {
    const { ws, emitEvent } = createCapturingMockWs();
    renderRealtimeSync(ws);
    invalidateSpy.mockClear();

    act(() => {
      emitEvent("chat:message", { chat_session_id: sessionId });
    });

    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: messagesKey });
  });

  it("applies chat completion once through the websocket lifecycle", () => {
    const { ws, emitEvent } = createCapturingMockWs();
    const older = userMessage("msg-older");
    const latest = userMessage("msg-latest");
    const latestCursor = {
      created_at: "2026-05-13T05:00:01Z",
      id: latest.id,
    };
    qc.setQueryData<InfiniteData<ChatMessagesPage>>(messagesKey, {
      pages: [
        { messages: [latest], has_more: true, next_cursor: latestCursor },
        { messages: [older], has_more: false, next_cursor: null },
      ],
      pageParams: [null, latestCursor],
    });
    qc.setQueryData<ChatPendingTask>(pendingKey, {
      task_id: "task-1",
      status: "running",
    });
    renderRealtimeSync(ws);

    act(() => {
      emitEvent("chat:done", donePayload());
      emitEvent("chat:done", donePayload());
    });

    const pages = qc.getQueryData<InfiniteData<ChatMessagesPage>>(messagesKey);
    expect(pages?.pages[0]?.messages.map((message) => message.id)).toEqual([
      "msg-latest",
      "msg-assistant",
    ]);
    expect(pages?.pages[1]?.messages.map((message) => message.id)).toEqual([
      "msg-older",
    ]);
    expect(qc.getQueryData<ChatPendingTask>(pendingKey)).toEqual({});
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: messagesKey });
  });

  it("keeps empty chat completions on the authoritative invalidation path", () => {
    const { ws, emitEvent } = createCapturingMockWs();
    qc.setQueryData(messagesKey, messagePages([userMessage()]));
    qc.setQueryData<ChatPendingTask>(pendingKey, {
      task_id: "task-1",
      status: "running",
    });
    renderRealtimeSync(ws);

    act(() => {
      emitEvent(
        "chat:done",
        donePayload({ message_id: undefined, content: undefined }),
      );
    });

    expect(
      qc
        .getQueryData<InfiniteData<ChatMessagesPage>>(messagesKey)
        ?.pages[0]?.messages.map((message) => message.id),
    ).toEqual(["msg-user"]);
    expect(qc.getQueryData<ChatPendingTask>(pendingKey)).toEqual({});
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: messagesKey });
  });

  it("invalidates issue identifiers only when a workspace prefix can be stale", () => {
    const { ws, emitEvent } = createCapturingMockWs();
    qc.setQueryData<Workspace[]>(workspaceKeys.list(), [workspace()]);
    renderRealtimeSync(ws);

    invalidateSpy.mockClear();
    act(() => {
      emitEvent("workspace:updated", {
        workspace: workspace({ name: "Renamed" }),
      });
    });
    expect(invalidateSpy).not.toHaveBeenCalledWith({
      queryKey: issueKeys.all("ws-1"),
    });
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: workspaceKeys.list(),
    });

    invalidateSpy.mockClear();
    act(() => {
      emitEvent("workspace:updated", {
        workspace: workspace({ issue_prefix: "NEW" }),
      });
    });
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: issueKeys.all("ws-1"),
    });
  });
});
