import { beforeEach, describe, expect, it } from "vitest";
import { createChatStore, newSessionDraftKey } from "./store";
import type { StorageAdapter } from "../types";
import type { Attachment } from "../types";
import { setCurrentWorkspace } from "../platform/workspace-storage";

function memStorage(): StorageAdapter {
  const m = new Map<string, string>();
  return {
    getItem: (k) => m.get(k) ?? null,
    setItem: (k, v) => {
      m.set(k, v);
    },
    removeItem: (k) => {
      m.delete(k);
    },
  };
}

function makeAttachment(id: string): Attachment {
  return {
    id,
    workspace_id: "ws-1",
    issue_id: null,
    comment_id: null,
    chat_session_id: null,
    chat_message_id: null,
    uploader_type: "member",
    uploader_id: "user-1",
    filename: `${id}.png`,
    url: `/uploads/${id}.png`,
    download_url: `/api/attachments/${id}/download`,
    markdown_url: `/api/attachments/${id}/download`,
    content_type: "image/png",
    size_bytes: 1,
    created_at: new Date(0).toISOString(),
  };
}

describe("newSessionDraftKey", () => {
  it("derives a stable per-agent slot for an uncreated chat", () => {
    expect(newSessionDraftKey("agent-1")).toBe("__new__:agent-1");
    expect(newSessionDraftKey(null)).toBe("__new__:");
  });
});

describe("chat store — draft attachments", () => {
  let store: ReturnType<typeof createChatStore>;

  beforeEach(() => {
    setCurrentWorkspace(null, null);
    store = createChatStore({ storage: memStorage() });
  });

  it("deduplicates attachment drafts by id", () => {
    store.getState().addInputDraftAttachment("draft-1", makeAttachment("att-1"));
    store.getState().addInputDraftAttachment("draft-1", {
      ...makeAttachment("att-1"),
      filename: "updated.png",
    });

    expect(store.getState().inputDraftAttachments["draft-1"]).toHaveLength(1);
    expect(store.getState().inputDraftAttachments["draft-1"]?.[0]?.filename).toBe("updated.png");
  });

  it("clearInputDraft clears both text and attachment records", () => {
    store.getState().setInputDraft("draft-1", "hello");
    store.getState().addInputDraftAttachment("draft-1", makeAttachment("att-1"));

    store.getState().clearInputDraft("draft-1");

    expect(store.getState().inputDrafts["draft-1"]).toBeUndefined();
    expect(store.getState().inputDraftAttachments["draft-1"]).toBeUndefined();
  });
});

describe("chat store — workspace lifecycle", () => {
  const flush = () =>
    new Promise<void>((resolve) => queueMicrotask(() => resolve()));

  it("does not persist a draft before a workspace is selected", () => {
    setCurrentWorkspace(null, null);
    const storage = memStorage();
    const store = createChatStore({ storage });
    store.getState().setInputDraft("draft-1", "private");
    expect(storage.getItem("multica:chat:drafts")).toBeNull();
  });

  it("restores drafts only from the active workspace", async () => {
    const storage = memStorage();
    setCurrentWorkspace("team-a", "ws-a");
    const store = createChatStore({ storage });
    store.getState().setInputDraft("draft-a", "from A");

    setCurrentWorkspace("team-b", "ws-b");
    await flush();
    expect(store.getState().inputDrafts).toEqual({});
    store.getState().setInputDraft("draft-b", "from B");

    setCurrentWorkspace("team-a", "ws-a");
    await flush();
    expect(store.getState().inputDrafts).toEqual({ "draft-a": "from A" });
  });

  it("clears the captured workspace without mutating the workspace now on screen", async () => {
    const storage = memStorage();
    setCurrentWorkspace("team-a", "ws-a");
    const store = createChatStore({ storage });
    store.getState().setInputDraft("draft-1", "from A");

    setCurrentWorkspace("team-b", "ws-b");
    await flush();
    store.getState().setInputDraft("draft-1", "from B");

    store.getState().clearInputDraftForWorkspace("team-a", "draft-1");

    expect(store.getState().inputDrafts).toEqual({ "draft-1": "from B" });
    expect(storage.getItem("multica:chat:drafts:team-a")).toBeNull();
    expect(storage.getItem("multica:chat:drafts:team-b")).toBe(
      JSON.stringify({ "draft-1": "from B" }),
    );
  });
});
