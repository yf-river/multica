import type { ChatSession, SendChatMessageResponse } from "../types";
import type { PendingChatOperation } from "./pending-operation-store";

export interface PendingChatOperationClient {
  createChatSession: (
    data: { agent_id: string; title?: string },
    idempotencyKey: string,
  ) => Promise<ChatSession>;
  sendChatMessage: (
    sessionId: string,
    content: string,
    idempotencyKey: string,
    attachmentIds?: string[],
  ) => Promise<SendChatMessageResponse>;
}

/** Execute the exact persisted intent; both requests reuse its stable UUID. */
export async function replayPendingChatOperation(
  operation: PendingChatOperation,
  client: PendingChatOperationClient,
  onSessionCreated: (sessionId: string) => void,
): Promise<{ sessionId: string; response: SendChatMessageResponse }> {
  let sessionId = operation.sessionId;
  if (operation.stage === "creating-session") {
    const session = await client.createChatSession(
      { agent_id: operation.agentId, title: operation.title },
      operation.id,
    );
    sessionId = session.id;
    onSessionCreated(sessionId);
  }
  if (!sessionId) {
    throw new Error("pending chat send has no session id");
  }
  const response = await client.sendChatMessage(
    sessionId,
    operation.content,
    operation.id,
    operation.attachmentIds,
  );
  return { sessionId, response };
}
