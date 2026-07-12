// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from "vitest";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { useIssueCreatePendingStore } from "./issue-create-pending-store";

describe("issue create pending store", () => {
  beforeEach(() => {
    setCurrentWorkspace("test-account", "workspace-1");
    localStorage.clear();
    useIssueCreatePendingStore.getState().setPendingCreate();
  });

  it("persists the exact request and key in the active account/workspace scope", async () => {
    useIssueCreatePendingStore.getState().setPendingCreate({
      requestKey: "10000000-0000-4000-8000-000000000002",
      request: { title: "Child", parent_issue_id: "parent-1" },
    });
    await useIssueCreatePendingStore.persist.rehydrate();

    expect(useIssueCreatePendingStore.getState().pendingCreate).toEqual({
      requestKey: "10000000-0000-4000-8000-000000000002",
      request: { title: "Child", parent_issue_id: "parent-1" },
    });
    expect(localStorage.getItem("multica_issue_create_pending:test-account"))
      .toContain("parent-1");
  });
});
