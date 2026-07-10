import { expect, test } from "@playwright/test";
import { createTestApi, loginAsDefault } from "./helpers";

test.describe("user-center 小队 SOP 页面证据", () => {
  test("可以在页面回看父子任务、SOP 执行、观测事件和评论唤醒", async ({ page }) => {
    test.setTimeout(120_000);

    const workspaceSlug = await loginAsDefault(page);
    const api = await createTestApi();

    try {
      const suffix = Date.now();
      await api.registerDaemonCodeBuddyRuntime(`E2E PM 小队页面 Runtime ${suffix}`);
      const template = await api.ensureInternalSquadTemplate("user-center");
      const squad = template.squad;
      const leader = template.agents.find((agent) => agent.role_key === "pm");
      expect(leader).toBeTruthy();

      const usercenterProject = await api.createProject(`UI usercenter ${suffix}`);
      const gatewayProject = await api.createProject(`UI gateway ${suffix}`);
      const configProject = await api.createProject(`UI config ${suffix}`);

      const parent = await api.createIssue(`UI user-center SOP 父任务 ${suffix}`, {
        description: "页面验收：user-center 小队父任务必须能展示 SOP、trace、跨项目子任务和 child-done 中文系统评论。",
        status: "todo",
        priority: "medium",
        project_id: usercenterProject.id,
        assignee_type: "squad",
        assignee_id: squad.id,
      });

      await expect
        .poll(async () => (await api.findLeaderTask(parent.id, leader!.id))?.id ?? "", {
          timeout: 15000,
          message: "等待 user-center 小队队长任务入队",
        })
        .not.toBe("");
      const leaderTask = await api.findLeaderTask(parent.id, leader!.id);
      expect(leaderTask).toBeTruthy();
      await api.completeSquadLeaderTaskViaDaemon(
        leaderTask!,
        "队长输出：已完成页面验收所需的 user-center SOP 执行证据、trace 和用量回写。",
      );

      const gatewayChild = await api.createIssue(`UI gateway 子任务 ${suffix}`, {
        description: "补充 user-center API 网关路由、鉴权、转发信息。",
        status: "todo",
        priority: "medium",
        parent_issue_id: parent.id,
        project_id: gatewayProject.id,
      });
      const configChild = await api.createIssue(`UI config 子任务 ${suffix}`, {
        description: "补充 user-center API 配置键、默认值、灰度和回滚方式。",
        status: "todo",
        priority: "medium",
        parent_issue_id: parent.id,
        project_id: configProject.id,
      });

      await api.updateIssue(gatewayChild.id, { status: "done" });
      await expect.poll(
        async () => (await api.getLatestSystemComment(parent.id))?.content ?? "",
        { timeout: 3000 },
      ).toBe("");
      await api.updateIssue(configChild.id, { status: "done" });
      let systemCommentId = "";
      await expect
        .poll(async () => {
          const comment = await api.getLatestSystemComment(parent.id);
          if (comment?.content?.includes(`子任务 [${configChild.identifier}]`)) {
            systemCommentId = comment.id;
          }
          return comment?.content ?? "";
        }, { timeout: 15000 })
        .toContain(`子任务 [${configChild.identifier}]`);

      await page.goto(`/${workspaceSlug}/issues/${parent.id}#comment-${systemCommentId}`, { waitUntil: "domcontentloaded" });
      await expect(page.getByRole("link", { name: new RegExp(`${parent.identifier}.*${parent.title}`) })).toBeVisible({ timeout: 15000 });
      await expect(page.getByText(squad.name).first()).toBeVisible({ timeout: 15000 });

      const runs = await api.listIssueSOPRuns(parent.id);
      expect(runs.items.some((run) => run.profile_key === "generic-project-sop-flow-v2")).toBe(true);

      const subIssues = page.getByTestId("issue-sub-issues-section");
      await expect(subIssues).toContainText("子任务", { timeout: 15000 });
      await expect(subIssues).toContainText(gatewayChild.identifier);
      await expect(subIssues).toContainText(gatewayChild.title);
      await expect(subIssues).toContainText(configChild.identifier);
      await expect(subIssues).toContainText(configChild.title);

      const systemComment = page.locator(`#comment-${systemCommentId}`).getByTestId("issue-comment-card");
      await expect(systemComment).toContainText(configChild.identifier, { timeout: 15000 });
      await expect(systemComment).toContainText("子任务");
      await expect(systemComment).toContainText("已完成");
      await expect(systemComment).toContainText("所有子任务均已结束");
      await expect(systemComment).not.toContainText("Sub-issue");

      const activeExecution = page.getByTestId("issue-execution-log-section");
      await expect(activeExecution).toContainText("执行日志");
      await expect(activeExecution).toContainText("排队中");
      await expect(activeExecution).toContainText("01-clarify");
      const reviewSummary = page.getByTestId("issue-run-review-summary-card");
      await expect(reviewSummary).toContainText("运行复盘", { timeout: 15000 });
      await reviewSummary.getByRole("link", { name: "查看完整复盘" }).click();
      await expect(page.getByTestId("run-review-horizontal-timeline")).toBeVisible({ timeout: 15000 });
      await expect(page.getByText(parent.title, { exact: true })).toBeVisible();
      await expect(page.getByText(gatewayChild.identifier, { exact: true }).first()).toBeVisible();
      await expect(page.getByText(configChild.identifier, { exact: true }).first()).toBeVisible();
      await expect(page.getByText(/所有子任务均已结束/).first()).toBeVisible();

      await page.goto(`/${workspaceSlug}/issues/${gatewayChild.id}`, { waitUntil: "domcontentloaded" });
      await expect(page.getByText(gatewayChild.identifier).first()).toBeVisible({ timeout: 15000 });
      await expect(page.getByRole("link", { name: new RegExp(`属于父任务 ${parent.identifier}`) })).toBeVisible({ timeout: 15000 });
    } finally {
      await api.cleanup();
    }
  });
});
