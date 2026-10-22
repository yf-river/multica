import { describe, expect, it } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { IssueReviewBadge } from "./issue-review-badge";

function issueWith(metadata: Record<string, unknown>) {
  return { metadata } as never;
}

describe("IssueReviewBadge", () => {
  it("shows a green check once the reviewer passed", () => {
    const { getByRole } = renderWithI18n(<IssueReviewBadge issue={issueWith({ review_passed: true })} />);
    expect(getByRole("img", { name: "Review passed" })).toBeTruthy();
  });

  it("accepts the string form written by the CLI", () => {
    const { getByRole } = renderWithI18n(<IssueReviewBadge issue={issueWith({ review_passed: "true" })} />);
    expect(getByRole("img", { name: "Review passed" })).toBeTruthy();
  });

  it("stays hidden while review has not passed", () => {
    const { queryByRole } = renderWithI18n(<IssueReviewBadge issue={issueWith({ review_passed: false })} />);
    expect(queryByRole("img", { name: "Review passed" })).toBeNull();
  });

  it("stays hidden when the issue never entered review", () => {
    const { queryByRole } = renderWithI18n(<IssueReviewBadge issue={issueWith({})} />);
    expect(queryByRole("img", { name: "Review passed" })).toBeNull();
  });
});
