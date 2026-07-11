import { describe, expect, it } from "vitest";
import type { StorageAdapter } from "../types/storage";
import {
  clearAccountStorage,
  clearWorkspaceStorage,
} from "./storage-cleanup";

function memoryStorage(initial: Record<string, string> = {}) {
  const data = new Map(Object.entries(initial));
  const adapter: StorageAdapter = {
    getItem: (key) => data.get(key) ?? null,
    setItem: (key, value) => data.set(key, value),
    removeItem: (key) => data.delete(key),
    keys: () => [...data.keys()],
  };
  return { adapter, data };
}

describe("clearWorkspaceStorage", () => {
  it("removes every slug- and id-scoped value for one workspace", () => {
    const workspaceKeys = [
      "multica_issue_draft",
      "multica_issues_view",
      "multica_issues_scope",
      "multica_my_issues_view",
      "multica_actor_issues_view",
      "multica_quick_create",
      "multica_comment_collapse",
      "multica_comment_drafts",
      "multica_feedback_draft",
      "multica_project_draft",
      "multica_projects_view",
      "multica_agents_view",
      "multica_skills_view",
      "multica_squads_view",
      "multica_autopilots_view",
      "multica_autopilot_pending_operations",
      "project_issues_view",
      "multica:chat:selectedAgentId",
      "multica:chat:activeSessionId",
      "multica:chat:drafts",
      "multica:chat:draft-attachments",
      "multica:chat:expanded",
      "multica_navigation",
    ];
    const local = memoryStorage({
      ...Object.fromEntries(
        workspaceKeys.map((key) => [`${key}:acme`, "private"]),
      ),
      "multica:training:selected-prompt:ws-1": "prompt-1",
      "multica:mention-recency:ws-1": "mentions",
      "multica_issue_draft:other": "keep",
    });
    const session = memoryStorage({
      "multica:training:case-drafts:ws-1": "case draft",
      "multica:training:case-drafts:ws-2": "keep",
    });

    clearWorkspaceStorage(
      { local: local.adapter, session: session.adapter },
      { slug: "acme", id: "ws-1" },
    );

    expect([...local.data.keys()]).toEqual(["multica_issue_draft:other"]);
    expect([...session.data.keys()]).toEqual([
      "multica:training:case-drafts:ws-2",
    ]);
  });
});

describe("clearAccountStorage", () => {
  it("removes account-owned data even when the workspace list is unavailable", () => {
    const local = memoryStorage({
      "multica_issue_draft:unknown-workspace": "draft",
      "multica:chat:drafts:unknown-workspace": "chat",
      "multica:chat:draft-attachments:unknown-workspace": "attachments",
      "multica:training:selected-prompt:ws-1": "prompt",
      "multica:mention-recency:ws-1": "mentions",
      multica_recent_issues: "issues",
      multica_recent_contexts: "contexts",
      multica_tabs: "tabs",
      multica_transcript_view: "keep preference",
      multica_token: "cleared by auth store",
    });
    const session = memoryStorage({
      "multica:training:case-drafts:ws-1": "draft",
      "multica_cmdF_warned:issue-1": "1",
      "multica:mermaid:layout:hash": "keep cache",
    });

    clearAccountStorage({ local: local.adapter, session: session.adapter });

    expect(Object.fromEntries(local.data)).toEqual({
      multica_transcript_view: "keep preference",
      multica_token: "cleared by auth store",
    });
    expect(Object.fromEntries(session.data)).toEqual({
      "multica:mermaid:layout:hash": "keep cache",
    });
  });
});
