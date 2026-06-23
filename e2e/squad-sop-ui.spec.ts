import { readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test } from "@playwright/test";
import { createTestApi, loginAsDefault } from "./helpers";

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
    const api = await createTestApi();
    try {
      const uiChild = await api.createIssue(`UI 验收 child-done 中文评论 ${Date.now()}`, {
        description: "通过公开 API 创建的页面回看夹具，用于验证子任务完成后的中文系统评论。",
        status: "todo",
        priority: "medium",
        parent_issue_id: evidence.issue.id,
      });
      await api.updateIssue(uiChild.id, { status: "done" });
      await expect
        .poll(async () => {
          const comment = await api.getLatestSystemComment(evidence.issue.id);
          return comment?.content ?? "";
        }, { timeout: 15000 })
        .toContain(`子任务 [${uiChild.identifier}]`);

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
        .filter({ hasText: uiChild.identifier })
        .filter({ hasText: "子任务" })
        .filter({ hasText: "已完成" });
      await expect(systemComment).toContainText(uiChild.identifier, { timeout: 15000 });
      await expect(systemComment).not.toContainText("Sub-issue");

      await page.goto(`/${workspaceSlug}/issues/${evidence.cross_project_children.gateway.id}`, { waitUntil: "domcontentloaded" });
      await expect(page.getByText(evidence.cross_project_children.gateway.identifier)).toBeVisible({ timeout: 15000 });
      await expect(page.getByRole("link", { name: new RegExp(`属于父任务 ${evidence.issue.identifier}`) })).toBeVisible({ timeout: 15000 });
    } finally {
      await api.cleanup();
    }
  });
});
