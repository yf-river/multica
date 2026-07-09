import { afterEach, describe, expect, it } from "vitest";
import { useModalStore } from "../modals";
import { openCreateIssue, type CreateIssueSeed } from "./create-issue";

describe("openCreateIssue", () => {
  afterEach(() => useModalStore.getState().close());

  it("opens the single create-issue modal without seed data", () => {
    openCreateIssue();

    expect(useModalStore.getState()).toMatchObject({
      modal: "create-issue",
      data: null,
    });
  });

  it("forwards every supported seed field unchanged", () => {
    const seed: CreateIssueSeed = {
      prompt: "Investigate the regression",
      agent_id: "agent-1",
      squad_id: "squad-1",
      project_id: "project-1",
      status: "in_progress",
      priority: "high",
      start_date: "2026-07-02",
      due_date: "2026-07-10",
      parent_issue_id: "issue-1",
      parent_issue_identifier: "MUL-42",
    };

    openCreateIssue(seed);

    expect(useModalStore.getState()).toMatchObject({
      modal: "create-issue",
      data: seed,
    });
  });
});
