// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from "vitest";
import {
  selectPendingChatOperations,
  usePendingChatOperationStore,
  type PendingChatOperation,
} from "./pending-operation-store";

function operation(overrides: Partial<PendingChatOperation> = {}): PendingChatOperation {
  return {
    id: "11111111-1111-4111-8111-111111111111",
    accountId: "user-1",
    workspaceId: "ws-1",
    workspaceSlug: "team-one",
    agentId: "agent-1",
    sourceSessionId: null,
    sessionId: null,
    title: "hello",
    content: "hello",
    attachmentIds: [],
    attachments: [],
    stage: "creating-session",
    cancelRequested: false,
    createdAt: 1,
    updatedAt: 1,
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

  it("isolates selectors by account and workspace and prunes removed workspaces", () => {
    const first = operation();
    const second = operation({
      id: "22222222-2222-4222-8222-222222222222",
      workspaceId: "ws-2",
      workspaceSlug: "team-two",
    });
    usePendingChatOperationStore.getState().start(first);
    usePendingChatOperationStore.getState().start(second);

    expect(selectPendingChatOperations("user-1", "ws-1")(
      usePendingChatOperationStore.getState(),
    )).toEqual([first]);

    usePendingChatOperationStore.getState().pruneWorkspaces(["ws-2"]);
    expect(Object.keys(usePendingChatOperationStore.getState().operations)).toEqual([second.id]);
  });
});
