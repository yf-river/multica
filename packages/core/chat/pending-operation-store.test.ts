// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, getApi, setApiInstance } from "../api";
import type { ChatSession } from "../types";
import {
  replayPendingChatOperation,
  usePendingChatOperationStore,
} from "./pending-operation-store";

type PendingChatOperation = Parameters<
  ReturnType<typeof usePendingChatOperationStore.getState>["start"]
>[0];

function operation(overrides: Partial<PendingChatOperation> = {}): PendingChatOperation {
  return {
    id: "11111111-1111-4111-8111-111111111111",
    accountId: "user-1",
    workspaceId: "ws-1",
    agentId: "agent-1",
    sessionId: null,
    title: "hello",
    content: "hello",
    attachmentIds: [],
    stage: "creating-session",
    cancelRequested: false,
    createdAt: 1,
    ...overrides,
  };
}

const session: ChatSession = {
  id: "session-1",
  agent_id: "agent-1",
  title: "hello",
  has_unread: false,
  updated_at: "2026-07-11T00:00:00Z",
};

describe("pending chat operation store", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setApiInstance(new ApiClient("http://core.test"));
    localStorage.clear();
    usePendingChatOperationStore.setState({ operations: {} });
  });

  it("persists the exact intent and advances it after session creation", () => {
    const pending = operation();
    usePendingChatOperationStore.getState().start(pending);
    usePendingChatOperationStore.getState().update(pending.id, {
      sessionId: "session-1",
      stage: "sending-message",
    });

    expect(usePendingChatOperationStore.getState().operations[pending.id]).toMatchObject({
      content: "hello",
      sessionId: "session-1",
      stage: "sending-message",
    });
    expect(localStorage.getItem("multica_pending_chat_operations")).toContain(pending.id);
  });

  it("does not recreate an operation when a late response arrives after cleanup", () => {
    const pending = operation();
    usePendingChatOperationStore.getState().start(pending);
    usePendingChatOperationStore.getState().remove(pending.id);
    usePendingChatOperationStore.getState().update(pending.id, { sessionId: "late-session" });

    expect(usePendingChatOperationStore.getState().operations).toEqual({});
  });

  it("prunes operations for removed workspaces", () => {
    const first = operation();
    const second = operation({
      id: "22222222-2222-4222-8222-222222222222",
      workspaceId: "ws-2",
    });
    usePendingChatOperationStore.getState().start(first);
    usePendingChatOperationStore.getState().start(second);

    usePendingChatOperationStore.getState().pruneWorkspaces(["ws-2"]);
    expect(Object.keys(usePendingChatOperationStore.getState().operations)).toEqual([second.id]);
  });
});

describe("replayPendingChatOperation", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setApiInstance(new ApiClient("http://core.test"));
  });

  it("reuses one logical operation id across create and send recovery", async () => {
    const pending = operation({ attachmentIds: ["att-1"] });
    const createChatSession = vi.spyOn(getApi(), "createChatSession")
      .mockResolvedValue(session);
    const sendChatMessage = vi.spyOn(getApi(), "sendChatMessage")
      .mockResolvedValue({
        message_id: "message-1",
        task_id: "task-1",
        created_at: "2026-07-11T00:00:00Z",
        attachment_ids: ["att-1"],
      });
    const onSessionCreated = vi.fn();

    await expect(
      replayPendingChatOperation(pending, onSessionCreated),
    ).resolves.toMatchObject({ sessionId: "session-1" });

    expect(createChatSession).toHaveBeenCalledWith(
      { agent_id: "agent-1", title: "hello" },
      pending.id,
    );
    expect(onSessionCreated).toHaveBeenCalledWith("session-1");
    expect(sendChatMessage).toHaveBeenCalledWith(
      "session-1",
      "hello",
      pending.id,
      ["att-1"],
    );
  });

  it("does not send when account cleanup rejects the post-create continuation", async () => {
    vi.spyOn(getApi(), "createChatSession").mockResolvedValue(session);
    const sendChatMessage = vi.spyOn(getApi(), "sendChatMessage");

    await expect(
      replayPendingChatOperation(operation(), () => {
        throw new Error("operation cleared on logout");
      }),
    ).rejects.toThrow("operation cleared on logout");

    expect(sendChatMessage).not.toHaveBeenCalled();
  });
});
