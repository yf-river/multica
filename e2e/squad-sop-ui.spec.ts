import { readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test } from "@playwright/test";
import { loginAsDefault } from "./helpers";

type SquadCurlEvidence = {
  workspace_slug: string;
  issue: { id: string; identifier: string; title: string };
  squad: { name: string };
  cross_project_children: {
    gateway: { id: string; identifier: string; title: string };
    config: { id: string; identifier: string; title: string };
  };
  child_done_wake: {
    parent_comment_id: string;
    child_identifier: string;
    parent_comment_mentions_squad: boolean;
    requeued_task_id: string;
  };
};

function latestUserCenterEvidence(): SquadCurlEvidence {
  const path = join(process.cwd(), "artifacts/acceptance/codex-squad-curl-e2e-latest.json");
  return JSON.parse(readFileSync(path, "utf8")) as SquadCurlEvidence;
}

test.describe("user-center 小队 SOP 页面证据", () => {
  test("可以在页面回看父子任务、SOP 执行、观测事件和评论唤醒", async ({ page }) => {
    const evidence = latestUserCenterEvidence();
    expect(evidence.child_done_wake.parent_comment_mentions_squad).toBe(true);
    expect(evidence.child_done_wake.requeued_task_id).toBeTruthy();

    const workspaceSlug = await loginAsDefault(page);
    expect(workspaceSlug).toBe(evidence.workspace_slug);

    await page.goto(`/${workspaceSlug}/issues/${evidence.issue.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByRole("link", { name: new RegExp(`${evidence.issue.identifier}.*${evidence.issue.title}`) })).toBeVisible({ timeout: 15000 });
    await expect(page.getByText(evidence.squad.name).first()).toBeVisible({ timeout: 15000 });

    const subIssues = page.getByTestId("issue-sub-issues-section");
    await expect(subIssues).toContainText("子任务", { timeout: 15000 });
    await expect(subIssues).toContainText(evidence.cross_project_children.gateway.identifier);
    await expect(subIssues).toContainText(evidence.cross_project_children.gateway.title);
    await expect(subIssues).toContainText(evidence.cross_project_children.config.identifier);
    await expect(subIssues).toContainText(evidence.cross_project_children.config.title);

    const executionLog = page.getByTestId("issue-execution-log-section");
    await expect(executionLog).toContainText("小队 SOP 执行", { timeout: 15000 });
    await expect(page.getByTestId("issue-sop-run-summary")).toContainText("user-center-sop-flow");
    await expect(page.getByTestId("issue-trace-event-summary")).toContainText("观测事件");

    const systemComment = page
      .getByTestId("issue-comment-card")
      .filter({ hasText: evidence.child_done_wake.child_identifier })
      .filter({ hasText: "子任务" });
    await expect(systemComment).toContainText(evidence.child_done_wake.child_identifier, { timeout: 15000 });

    await page.goto(`/${workspaceSlug}/issues/${evidence.cross_project_children.gateway.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByText(evidence.cross_project_children.gateway.identifier)).toBeVisible({ timeout: 15000 });
    await expect(page.getByRole("link", { name: new RegExp(`属于父任务 ${evidence.issue.identifier}`) })).toBeVisible({ timeout: 15000 });
  });
});
