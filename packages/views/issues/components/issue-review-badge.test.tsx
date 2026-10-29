import { describe, expect, it } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";
import { IssueReviewBadge } from "./issue-review-badge";

function issueWith(metadata: Record<string, unknown>) {
  return { metadata } as never;
}

describe("IssueReviewBadge", () => {
  function renderBadge(metadata: Record<string, unknown>) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return renderWithI18n(<QueryClientProvider client={client}><IssueReviewBadge issue={issueWith(metadata)} /></QueryClientProvider>);
  }
  it("shows a green check once the reviewer passed", () => {
    const { getByRole } = renderBadge({ review_passed: true });
    expect(getByRole("img", { name: "Review passed" })).toBeTruthy();
  });

  it("accepts the string form written by the CLI", () => {
    const { getByRole } = renderBadge({ review_passed: "true" });
    expect(getByRole("img", { name: "Review passed" })).toBeTruthy();
  });

  it("stays hidden while review has not passed", () => {
    const { queryByRole } = renderBadge({ review_passed: false });
    expect(queryByRole("img", { name: "Review passed" })).toBeNull();
  });

  it("stays hidden when the issue never entered review", () => {
    const { queryByRole } = renderBadge({});
    expect(queryByRole("img", { name: "Review passed" })).toBeNull();
  });

  it("shows the three release checks with their actual states", () => {
    const { getByRole } = renderBadge({ release_workflow: true, review_passed: true, verification_passed: true, integration_passed: false });
    expect(getByRole("img", { name: "Review passed" }).className).toContain("emerald");
    expect(getByRole("img", { name: "Task verification passed" }).className).toContain("emerald");
    expect(getByRole("img", { name: "Awaiting integration" }).className).toContain("muted");
  });

  it("shows three pending checks for a project with the release workflow enabled", async () => {
    const client = new QueryClient();
    client.setQueryData(["workspaces", "workspace-1", "release-agent", "settings"], {
      enabled: true,
      project_id: "project-1",
    });
    const issue = { metadata: {}, workspace_id: "workspace-1", project_id: "project-1" } as never;
    const { getByRole } = renderWithI18n(<QueryClientProvider client={client}><IssueReviewBadge issue={issue} /></QueryClientProvider>);
    expect(getByRole("img", { name: "Awaiting code review" })).toBeTruthy();
    expect(getByRole("img", { name: "Awaiting task verification" })).toBeTruthy();
    expect(getByRole("img", { name: "Awaiting integration" })).toBeTruthy();
  });
});
