import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it } from "vitest";
import type { IssueReaction, IssueSubscriber, TimelineEntry } from "../types";
import { applyIssueDetailEvent } from "./detail-ws-updaters";
import { issueKeys } from "./queries";

const issueId = "issue-1";

function comment(id: string, createdAt: string, overrides = {}) {
  return {
    id,
    issue_id: issueId,
    author_type: "member",
    author_id: "user-1",
    content: id,
    parent_id: null,
    created_at: createdAt,
    type: "comment",
    reactions: [],
    attachments: [],
    resolved_at: null,
    resolved_by_type: null,
    resolved_by_id: null,
    ...overrides,
  };
}

describe("applyIssueDetailEvent", () => {
  let qc: QueryClient;

  beforeEach(() => {
    qc = new QueryClient();
  });

  it("projects comment lifecycle events into the timeline in order", () => {
    qc.setQueryData<TimelineEntry[]>(issueKeys.timeline(issueId), [
      {
        type: "comment",
        id: "c1",
        actor_type: "member",
        actor_id: "user-1",
        created_at: "2026-05-06T01:00:00Z",
      },
      {
        type: "comment",
        id: "c3",
        actor_type: "member",
        actor_id: "user-1",
        created_at: "2026-05-06T03:00:00Z",
      },
    ]);

    applyIssueDetailEvent(qc, "comment:created", {
      comment: comment("c2", "2026-05-06T02:00:00Z"),
    });
    expect(
      qc.getQueryData<TimelineEntry[]>(issueKeys.timeline(issueId))?.map(
        (entry) => entry.id,
      ),
    ).toEqual(["c1", "c2", "c3"]);

    applyIssueDetailEvent(qc, "comment:resolved", {
      comment: comment("c2", "2026-05-06T02:00:00Z", {
        resolved_at: "2026-05-06T04:00:00Z",
        resolved_by_type: "member",
        resolved_by_id: "user-1",
      }),
    });
    expect(
      qc
        .getQueryData<TimelineEntry[]>(issueKeys.timeline(issueId))
        ?.find((entry) => entry.id === "c2")?.resolved_at,
    ).toBe("2026-05-06T04:00:00Z");
  });

  it("removes a deleted comment and its reply subtree", () => {
    qc.setQueryData<TimelineEntry[]>(issueKeys.timeline(issueId), [
      { type: "comment", id: "root", actor_type: "member", actor_id: "u", created_at: "1" },
      { type: "comment", id: "reply", actor_type: "member", actor_id: "u", parent_id: "root", created_at: "2" },
      { type: "comment", id: "nested", actor_type: "member", actor_id: "u", parent_id: "reply", created_at: "3" },
      { type: "comment", id: "other", actor_type: "member", actor_id: "u", created_at: "4" },
    ]);

    applyIssueDetailEvent(qc, "comment:deleted", {
      issue_id: issueId,
      comment_id: "root",
    });
    expect(
      qc.getQueryData<TimelineEntry[]>(issueKeys.timeline(issueId))?.map(
        (entry) => entry.id,
      ),
    ).toEqual(["other"]);
  });

  it("projects comment reactions without duplicates", () => {
    const reaction = {
      id: "reaction-1",
      comment_id: "c1",
      emoji: "👍",
      actor_type: "member",
      actor_id: "user-1",
      created_at: "2026-05-06T01:00:00Z",
    };
    qc.setQueryData<TimelineEntry[]>(issueKeys.timeline(issueId), [
      {
        type: "comment",
        id: "c1",
        actor_type: "member",
        actor_id: "user-1",
        created_at: "2026-05-06T01:00:00Z",
        reactions: [],
      },
    ]);

    applyIssueDetailEvent(qc, "reaction:added", {
      issue_id: issueId,
      reaction,
    });
    applyIssueDetailEvent(qc, "reaction:added", {
      issue_id: issueId,
      reaction,
    });
    expect(
      qc.getQueryData<TimelineEntry[]>(issueKeys.timeline(issueId))?.[0]
        ?.reactions,
    ).toHaveLength(1);

    applyIssueDetailEvent(qc, "reaction:removed", {
      issue_id: issueId,
      comment_id: "c1",
      emoji: "👍",
      actor_type: "member",
      actor_id: "user-1",
    });
    expect(
      qc.getQueryData<TimelineEntry[]>(issueKeys.timeline(issueId))?.[0]
        ?.reactions,
    ).toEqual([]);
  });

  it("projects issue reactions and subscribers through their canonical caches", () => {
    qc.setQueryData<IssueReaction[]>(issueKeys.reactions(issueId), []);
    qc.setQueryData<IssueSubscriber[]>(issueKeys.subscribers(issueId), []);

    applyIssueDetailEvent(qc, "issue_reaction:added", {
      issue_id: issueId,
      reaction: {
        id: "reaction-1",
        issue_id: issueId,
        emoji: "🚀",
        actor_type: "member",
        actor_id: "user-1",
        created_at: "2026-05-06T01:00:00Z",
      },
    });
    applyIssueDetailEvent(qc, "subscriber:added", {
      issue_id: issueId,
      user_type: "member",
      user_id: "user-1",
    });
    expect(qc.getQueryData<IssueReaction[]>(issueKeys.reactions(issueId))).toHaveLength(1);
    expect(qc.getQueryData<IssueSubscriber[]>(issueKeys.subscribers(issueId))).toHaveLength(1);

    applyIssueDetailEvent(qc, "issue_reaction:removed", {
      issue_id: issueId,
      emoji: "🚀",
      actor_type: "member",
      actor_id: "user-1",
    });
    applyIssueDetailEvent(qc, "subscriber:removed", {
      issue_id: issueId,
      user_type: "member",
      user_id: "user-1",
    });
    expect(qc.getQueryData<IssueReaction[]>(issueKeys.reactions(issueId))).toEqual([]);
    expect(qc.getQueryData<IssueSubscriber[]>(issueKeys.subscribers(issueId))).toEqual([]);
  });
});
