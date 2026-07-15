// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
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

describe("pending chat operation store", () => {
  beforeEach(() => {
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
  it("reuses one logical operation id across create and send recovery", async () => {
    const pending = operation({ attachmentIds: ["att-1"] });
    const client = {
      createChatSession: vi.fn().mockResolvedValue({ id: "session-1" }),
      sendChatMessage: vi.fn().mockResolvedValue({
        message_id: "message-1",
        task_id: "task-1",
        created_at: "2026-07-11T00:00:00Z",
        attachment_ids: ["att-1"],
      }),
    };
    const onSessionCreated = vi.fn();

    await expect(
      replayPendingChatOperation(pending, client, onSessionCreated),
    ).resolves.toMatchObject({ sessionId: "session-1" });

    expect(client.createChatSession).toHaveBeenCalledWith(
      { agent_id: "agent-1", title: "hello" },
      pending.id,
    );
    expect(onSessionCreated).toHaveBeenCalledWith("session-1");
    expect(client.sendChatMessage).toHaveBeenCalledWith(
      "session-1",
      "hello",
      pending.id,
      ["att-1"],
    );
  });

  it("does not send when account cleanup rejects the post-create continuation", async () => {
    const client = {
      createChatSession: vi.fn().mockResolvedValue({ id: "session-1" }),
      sendChatMessage: vi.fn(),
    };

    await expect(
      replayPendingChatOperation(operation(), client, () => {
        throw new Error("operation cleared on logout");
      }),
    ).rejects.toThrow("operation cleared on logout");

    expect(client.sendChatMessage).not.toHaveBeenCalled();
  });
});
