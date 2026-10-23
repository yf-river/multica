// @vitest-environment node
import { describe, expect, it } from "vitest";

import { reviewReplyRecipientLabel, reviewReplyToast } from "./review-reply-copy";
import en from "../locales-test/en/issues.json";

// GONG-63: a reply addressed to the managed reviewer goes to the issue's current
// owner. The composer (before sending) and the toast (after saving) must both name
// the real recipient, and must never read as if the reviewer itself will answer —
// a human owner or a missing owner is exactly the case where "comment saved" would
// otherwise look like "an agent is now working on it".
type Leaf = string | Record<string, unknown>;

// Minimal stand-in for the i18n `t`: resolves the selector against the real en
// bundle and substitutes `{{name}}`-style placeholders, so the test fails when a key
// or a placeholder name drifts.
const t = ((selector: (bundle: unknown) => Leaf, options?: Record<string, string>) => {
  const value = selector(en as unknown);
  if (typeof value !== "string") {
    throw new Error("selector did not resolve to a string");
  }
  return value.replace(/\{\{(\w+)\}\}/g, (_, key: string) => options?.[key] ?? `{{${key}}}`);
}) as Parameters<typeof reviewReplyRecipientLabel>[1];

describe("reviewer reply copy", () => {
  it("names the owner the reply will notify", () => {
    const label = reviewReplyRecipientLabel(
      { status: "will_notify", recipient_type: "agent", recipient_name: "multica-developer" },
      t,
    );
    expect(label).toContain("multica-developer");
    expect(label).toBe(en.comment.review_reply_to_assignee.replace("{{name}}", "multica-developer"));
  });

  it("says a human owner starts no agent", () => {
    const label = reviewReplyRecipientLabel(
      { status: "human_assignee", recipient_type: "member", recipient_name: "River" },
      t,
    );
    expect(label).toContain("River");
    expect(label.toLowerCase()).toContain("no agent");
  });

  it("reports a missing owner instead of staying silent", () => {
    expect(reviewReplyRecipientLabel({ status: "no_assignee" }, t)).toBe(
      en.comment.review_reply_no_assignee,
    );
  });

  it("explains why an uninvocable owner cannot be notified", () => {
    const label = reviewReplyRecipientLabel(
      { status: "blocked", recipient_type: "agent", reason_code: "invocation_not_allowed" },
      t,
    );
    expect(label).toContain(en.comment.trigger_blocked_short_invocation_not_allowed);
  });

  it("tells queued apart from merged and from not dispatched", () => {
    const owner = { recipient_type: "agent", recipient_name: "multica-developer" };
    const queued = reviewReplyToast({ ...owner, status: "queued" }, t);
    const coalesced = reviewReplyToast({ ...owner, status: "coalesced" }, t);
    const blocked = reviewReplyToast(
      { ...owner, status: "blocked", reason_code: "runtime_unusable" },
      t,
    );

    expect(queued?.tone).toBe("info");
    expect(queued?.text).toContain("multica-developer");
    expect(coalesced?.text).not.toBe(queued?.text);
    expect(blocked?.tone).toBe("warning");
    expect(blocked?.text).toContain(en.comment.trigger_blocked_runtime_unusable);
  });

  it("does not toast the preview status", () => {
    expect(reviewReplyToast({ status: "will_notify", recipient_type: "agent" }, t)).toBeNull();
  });
});
