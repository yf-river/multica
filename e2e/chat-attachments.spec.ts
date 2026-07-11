/**
 * E2E: chat attachment upload + send back-fills the message link.
 *
 * Stays at the HTTP layer (auth → upload-file → send-chat-message → DB
 * check) so the test doesn't depend on a real agent runtime being online.
 * The UI wiring is covered by `chat-input.test.tsx` in @multica/views; this
 * spec is the end-to-end contract proof: the backend really does persist
 * chat_session_id at upload and back-fill chat_message_id at send.
 */
import "./env";
import { test, expect } from "@playwright/test";
import pg from "pg";
import { createTestApi } from "./helpers";
import type { TestApiClient } from "./fixtures";

const API_BASE =
  process.env.NEXT_PUBLIC_API_URL || `http://localhost:${process.env.PORT || "8080"}`;
const DATABASE_URL =
  process.env.DATABASE_URL ?? "postgres://multica:multica@localhost:5432/multica?sslmode=disable";

interface UploadRow {
  id: string;
  url: string;
  chat_session_id: string | null;
  chat_message_id: string | null;
}

async function authedFetch(api: TestApiClient, path: string, init?: RequestInit) {
  const token = api.getToken();
  if (!token) throw new Error("test api client not logged in");
  const headers: Record<string, string> = {
    Authorization: `Bearer ${token}`,
    ...((init?.headers as Record<string, string>) ?? {}),
  };
  return fetch(`${API_BASE}${path}`, { ...init, headers });
}

test.describe("Chat attachments", () => {
  let api: TestApiClient;
  let pgClient: pg.Client | null = null;
  let createdSessionId: string | null = null;
  let createdWorkspaceSlug: string | null = null;

  test.beforeEach(async () => {
    api = await createTestApi();
    pgClient = new pg.Client(DATABASE_URL);
    await pgClient.connect();
  });

  test.afterEach(async () => {
    try {
      if (createdSessionId && createdWorkspaceSlug) {
        await authedFetch(api, `/api/chat/sessions/${createdSessionId}`, {
          method: "DELETE",
          headers: { "X-Workspace-Slug": createdWorkspaceSlug },
        });
      }
    } finally {
      if (pgClient) await pgClient.end();
      pgClient = null;
      createdSessionId = null;
      createdWorkspaceSlug = null;
      await api.cleanup();
    }
  });

  test("upload-file binds attachment to the chat_session; send back-fills chat_message_id", async () => {
    expect(pgClient).not.toBeNull();
    const pgc = pgClient!;

    const workspaces = await api.getWorkspaces();
    const ws = workspaces[0]!;
    api.setWorkspaceSlug(ws.slug);
    api.setWorkspaceId(ws.id);
    createdWorkspaceSlug = ws.slug;

    // Build the current runtime → agent → chat chain through public APIs. The
    // only direct database access below is the persistence assertion itself.
    const { runtime } = await api.registerDaemonCodeBuddyRuntime(`E2E Chat Runtime ${Date.now()}`);
    const agent = await api.createAgent({
      name: `E2E Chat Agent ${Date.now()}`,
      runtime_id: runtime.id,
      scope: "workspace",
    });
    const sessionRes = await authedFetch(api, "/api/chat/sessions", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Idempotency-Key": crypto.randomUUID(),
        "X-Workspace-Slug": ws.slug,
      },
      body: JSON.stringify({ agent_id: agent.id, title: "E2E Chat Attachment Session" }),
    });
    expect(sessionRes.status).toBe(201);
    const session = (await sessionRes.json()) as { id: string };
    createdSessionId = session.id;

    // 1. Upload a small PNG against the chat session.
    const pngBytes = Buffer.from([
      0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
      0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR
    ]);
    const form = new FormData();
    form.append("file", new Blob([new Uint8Array(pngBytes)], { type: "image/png" }), "e2e.png");
    form.append("chat_session_id", session.id);
    const uploadRes = await authedFetch(api, "/api/upload-file", {
      method: "POST",
      body: form,
      headers: { "X-Workspace-Slug": ws.slug },
    });
    expect(uploadRes.status).toBe(200);
    const uploaded = (await uploadRes.json()) as UploadRow;
    expect(uploaded.chat_session_id).toBe(session.id);
    expect(uploaded.chat_message_id).toBeNull();
    expect(uploaded.url).toBeTruthy();

    // 2. Send a chat message that references the attachment.
    const sendRes = await authedFetch(api, `/api/chat/sessions/${session.id}/messages`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Idempotency-Key": crypto.randomUUID(),
        "X-Workspace-Slug": ws.slug,
      },
      body: JSON.stringify({
        content: `look at this ![](${uploaded.url})`,
        attachment_ids: [uploaded.id],
      }),
    });
    expect(sendRes.status).toBe(201);
    const sendBody = (await sendRes.json()) as { message_id: string; task_id: string };
    expect(sendBody.message_id).toBeTruthy();

    // 3. DB check: the attachment row's chat_message_id matches the new message.
    const after = await pgc.query<{ chat_message_id: string | null }>(
      `SELECT chat_message_id::text FROM attachment WHERE id = $1`,
      [uploaded.id],
    );
    expect(after.rows[0]?.chat_message_id).toBe(sendBody.message_id);

    // 4. Clean up the attachment we created (chat_session cascade handles the
    //    rest in afterEach via chat_session row deletion).
    await pgc.query(`DELETE FROM attachment WHERE id = $1`, [uploaded.id]);
  });
});
