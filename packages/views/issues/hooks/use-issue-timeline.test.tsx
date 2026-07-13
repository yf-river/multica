import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { TestI18nProvider } from "../../test/i18n";

// Mock @multica/core/issues/mutations to mimic TanStack Query v5's contract:
// useMutation returns a fresh result wrapper on every render, but the
// `mutate` / `mutateAsync` functions inside it are stable across renders.
// This is exactly the shape that previously fooled the original deps lists
// in useIssueTimeline — guarding against a regression here means future code
// can't accidentally pull the whole mutation result into a useCallback dep.
const stableHandles = vi.hoisted(() => ({
  createMutateAsync: vi.fn(async () => ({})),
  updateMutateAsync: vi.fn(async () => ({})),
  deleteMutateAsync: vi.fn(async () => ({})),
  resolveMutateAsync: vi.fn(async () => ({})),
  toggleMutate: vi.fn(),
}));

vi.mock("@multica/core/issues/mutations", () => ({
  useCreateComment: () => ({
    mutateAsync: stableHandles.createMutateAsync,
    mutate: vi.fn(),
    isPending: false,
  }),
  useUpdateComment: () => ({
    mutateAsync: stableHandles.updateMutateAsync,
    mutate: vi.fn(),
    isPending: false,
  }),
  useDeleteComment: () => ({
    mutateAsync: stableHandles.deleteMutateAsync,
    mutate: vi.fn(),
    isPending: false,
  }),
  useResolveComment: () => ({
    mutateAsync: stableHandles.resolveMutateAsync,
    mutate: vi.fn(),
    isPending: false,
  }),
  useToggleCommentReaction: () => ({
    mutateAsync: vi.fn(),
    mutate: stableHandles.toggleMutate,
    isPending: false,
  }),
}));

vi.mock("@multica/core/issues/queries", () => ({
  issueTimelineOptions: (id: string) => ({
    queryKey: ["issues", "timeline", id],
    queryFn: () => Promise.resolve([]),
  }),
  issueKeys: {
    timeline: (id: string) => ["issues", "timeline", id],
  },
}));

// Hoisted state controllable from tests — represents what useQuery would
// return for the current render.
const queryState = vi.hoisted(() => ({
  data: undefined as unknown,
  isLoading: false,
}));

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>(
    "@tanstack/react-query",
  );
  return {
    ...actual,
    useQuery: () => ({
      data: queryState.data,
      isLoading: queryState.isLoading,
    }),
    useMutationState: () => [],
  };
});

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

import { useIssueTimeline } from "./use-issue-timeline";

const hookOptions = { wrapper: TestI18nProvider };

function renderIssueTimelineHook() {
  return renderHook(() => useIssueTimeline("issue-1", "user-1"), hookOptions);
}

describe("useIssueTimeline", () => {
  beforeEach(() => {
    stableHandles.createMutateAsync.mockClear();
    stableHandles.updateMutateAsync.mockClear();
    stableHandles.deleteMutateAsync.mockClear();
    stableHandles.resolveMutateAsync.mockClear();
    stableHandles.toggleMutate.mockClear();
    queryState.data = [];
    queryState.isLoading = false;
  });

  // CommentCard is wrapped in React.memo (perf fix for long timelines, see
  // multica#1968). The memo only pays off if the callbacks passed down keep
  // the same identity across unrelated parent re-renders. TanStack Query v5
  // returns a *new* mutation result wrapper on every render, so a useCallback
  // listing the whole mutation object as a dep flips its identity every time
  // — that is the exact regression this test guards against.
  it("submitReply / editComment / deleteComment / toggleReaction keep identity across unrelated re-renders", () => {
    const { result, rerender } = renderIssueTimelineHook();

    const first = {
      submitComment: result.current.submitComment,
      submitReply: result.current.submitReply,
      editComment: result.current.editComment,
      deleteComment: result.current.deleteComment,
      toggleReaction: result.current.toggleReaction,
    };

    rerender();
    rerender();

    expect(result.current.submitReply).toBe(first.submitReply);
    expect(result.current.editComment).toBe(first.editComment);
    expect(result.current.deleteComment).toBe(first.deleteComment);
    expect(result.current.toggleReaction).toBe(first.toggleReaction);
    expect(result.current.submitComment).toBe(first.submitComment);
  });

  it("returns the timeline as a flat array directly from the query cache", () => {
    queryState.data = [
      { type: "comment", id: "c1", actor_type: "member", actor_id: "u", created_at: "2026-05-06T01:00:00Z" },
      { type: "comment", id: "c2", actor_type: "member", actor_id: "u", created_at: "2026-05-06T02:00:00Z" },
      { type: "comment", id: "c3", actor_type: "member", actor_id: "u", created_at: "2026-05-06T03:00:00Z" },
    ];
    const { result } = renderIssueTimelineHook();
    expect(result.current.timeline.map((e) => e.id)).toEqual(["c1", "c2", "c3"]);
  });

  it("passes suppressed agent ids through editComment", async () => {
    const { result } = renderIssueTimelineHook();

    await act(async () => {
      await result.current.editComment("comment-1", "updated", ["attachment-1"], ["agent-1"]);
    });

    expect(stableHandles.updateMutateAsync).toHaveBeenCalledWith({
      commentId: "comment-1",
      content: "updated",
      attachmentIds: ["attachment-1"],
      suppressAgentIds: ["agent-1"],
    });
  });

});
