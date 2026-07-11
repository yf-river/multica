import { describe, expect, it } from "vitest";
import type { InboxItem } from "@multica/core/types";
import {
  getInboxDisplayTitle,
  getQuickCreateFailureDetail,
} from "./inbox-display";

function item(overrides: Partial<InboxItem>): InboxItem {
  return {
    id: "inbox-1",
    workspace_id: "workspace-1",
    recipient_type: "member",
    recipient_id: "member-1",
    actor_type: "agent",
    actor_id: "agent-1",
    type: "new_comment",
    severity: "info",
    issue_id: "issue-1",
    title: "任务标题",
    body: null,
    issue_status: null,
    read: false,
    archived: false,
    created_at: "2026-04-29T12:00:00Z",
    details: null,
    ...overrides,
  };
}

describe("inbox display helpers", () => {
  it("renders the current quick-create issue title unchanged", () => {
    const quickCreateItem = item({
      type: "quick_create_done",
      title: "Fix agent list column widths",
      details: { identifier: "MUL-1583" },
    });

    expect(getInboxDisplayTitle(quickCreateItem)).toBe(
      "Fix agent list column widths",
    );
  });

  it("uses the original prompt as the failed quick-create row title", () => {
    const failedItem = item({
      type: "quick_create_failed",
      title: "Quick create failed",
      body: "agent finished without creating an issue",
      issue_id: null,
      details: {
        original_prompt: "Optimize QuickCapture UI\nand attached screenshot",
      },
    });

    expect(getInboxDisplayTitle(failedItem)).toBe(
      "Optimize QuickCapture UI and attached screenshot",
    );
  });

  it("uses the redacted failure detail for failed quick-create subtitles", () => {
    const failedItem = item({
      type: "quick_create_failed",
      body: "fallback body",
      details: { error: "CLI failed\nwith exit status 1" },
    });

    expect(getQuickCreateFailureDetail(failedItem)).toBe(
      "CLI failed with exit status 1",
    );
  });
});
