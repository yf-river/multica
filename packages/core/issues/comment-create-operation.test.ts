// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiTransportError, getApi, setApiInstance } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { Comment } from "../types";
import {
  createCommentWithRecovery,
} from "./comment-create-operation";
import { useCommentDraftStore } from "./stores/comment-draft-store";

const comment = (id: string, content: string) => ({ id, content }) as Comment;

describe("createCommentWithRecovery", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setApiInstance(new ApiClient("http://core.test"));
    setCurrentWorkspace("test-account", "workspace-1");
    localStorage.clear();
    useCommentDraftStore.setState({ pendingCreates: {} });
  });

  it("keeps the exact request and key after an unknown outcome", async () => {
    vi.spyOn(getApi(), "createComment").mockRejectedValue(
      new ApiTransportError("POST /api/issues/:id/comments", true, new Error("lost")),
    );
    await expect(createCommentWithRecovery("issue-1", { content: "hello" }))
      .rejects.toBeInstanceOf(ApiTransportError);
    const pending = useCommentDraftStore.getState().pendingCreates["issue-1:root"];
    expect(pending?.request).toEqual({ content: "hello" });
    expect(pending?.requestKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(localStorage.getItem("multica_comment_drafts:test-account")).toContain("hello");
  });

  it("recovers older speech before posting different content in the same thread", async () => {
    useCommentDraftStore.getState().setPendingCreate("issue-1:root", {
      issueId: "issue-1",
      requestKey: "10000000-0000-4000-8000-000000000003",
      request: { content: "earlier" },
      createdAt: Date.now(),
    });
    const createComment = vi.spyOn(getApi(), "createComment")
      .mockResolvedValueOnce(comment("comment-1", "earlier"))
      .mockResolvedValueOnce(comment("comment-2", "current"));
    await expect(createCommentWithRecovery("issue-1", { content: "current" }))
      .resolves.toMatchObject({ id: "comment-2" });
    expect(createComment).toHaveBeenCalledTimes(2);
    expect(createComment.mock.calls[0]).toEqual([
      "issue-1",
      { content: "earlier" },
      "10000000-0000-4000-8000-000000000003",
    ]);
    expect(useCommentDraftStore.getState().pendingCreates).toEqual({});
  });

  it("rehydrates existing comment drafts without inventing a pending operation", async () => {
    localStorage.setItem("multica_comment_drafts:test-account", JSON.stringify({
      state: {
        drafts: { "new:issue-1": { content: "draft", updatedAt: Date.now() } },
      },
      version: 0,
    }));
    await useCommentDraftStore.persist.rehydrate();
    expect(useCommentDraftStore.getState().getDraft("new:issue-1")).toBe("draft");
    expect(useCommentDraftStore.getState().pendingCreates).toEqual({});
  });
});
