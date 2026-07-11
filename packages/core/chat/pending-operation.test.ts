import { describe, expect, it, vi } from "vitest";
import { replayPendingChatOperation, type PendingChatOperationClient } from "./pending-operation";
import type { PendingChatOperation } from "./pending-operation-store";

const operation: PendingChatOperation = {
  id: "11111111-1111-4111-8111-111111111111",
  accountId: "user-1",
  workspaceId: "ws-1",
  workspaceSlug: "team-one",
  agentId: "agent-1",
  sourceSessionId: null,
  sessionId: null,
  title: "hello",
  content: "hello",
  attachmentIds: ["att-1"],
  attachments: [],
  stage: "creating-session",
  cancelRequested: false,
  createdAt: 1,
  updatedAt: 1,
};

describe("replayPendingChatOperation", () => {
  it("reuses one logical operation id across create and send recovery", async () => {
    const client: PendingChatOperationClient = {
      createChatSession: vi.fn().mockResolvedValue({ id: "session-1" }),
      sendChatMessage: vi.fn().mockResolvedValue({
        message_id: "message-1",
        task_id: "task-1",
        created_at: "2026-07-11T00:00:00Z",
        attachment_ids: ["att-1"],
      }),
    };
    const onSessionCreated = vi.fn();

    await expect(replayPendingChatOperation(operation, client, onSessionCreated))
      .resolves.toMatchObject({ sessionId: "session-1" });

    expect(client.createChatSession).toHaveBeenCalledWith(
      { agent_id: "agent-1", title: "hello" },
      operation.id,
    );
    expect(onSessionCreated).toHaveBeenCalledWith("session-1");
    expect(client.sendChatMessage).toHaveBeenCalledWith(
      "session-1",
      "hello",
      operation.id,
      ["att-1"],
    );
  });

  it("does not send when account cleanup rejects the post-create continuation", async () => {
    const client: PendingChatOperationClient = {
      createChatSession: vi.fn().mockResolvedValue({ id: "session-1" }),
      sendChatMessage: vi.fn(),
    };

    await expect(replayPendingChatOperation(operation, client, () => {
      throw new Error("operation cleared on logout");
    })).rejects.toThrow("operation cleared on logout");

    expect(client.sendChatMessage).not.toHaveBeenCalled();
  });
});
