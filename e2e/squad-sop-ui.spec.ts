import { expect, test } from "@playwright/test";
import { createTestApi, loginAsDefault } from "./helpers";

test.describe("user-center 小队 SOP 页面证据", () => {
  test("可以在页面回看父子任务、SOP 执行、观测事件和评论唤醒", async ({ page }) => {
    test.setTimeout(120_000);

    const workspaceSlug = await loginAsDefault(page);
    const api = await createTestApi();
    const createdIssueIds: string[] = [];
    const createdProjectIds: string[] = [];

    try {
      const suffix = Date.now();
      const template = await api.ensureInternalSquadTemplate("user-center");
      const squad = template.squad;
      const leader = template.agents.find((agent) => agent.role_key === "captain");
      expect(leader).toBeTruthy();

      const usercenterProject = await api.createProject(`UI usercenter ${suffix}`);
      const gatewayProject = await api.createProject(`UI gateway ${suffix}`);
      const configProject = await api.createProject(`UI config ${suffix}`);
      createdProjectIds.push(configProject.id, gatewayProject.id, usercenterProject.id);

      const parent = await api.createIssue(`UI user-center SOP 父任务 ${suffix}`, {
        description: "页面验收：user-center 小队父任务必须能展示 SOP、trace、跨项目子任务和 child-done 中文系统评论。",
        status: "todo",
        priority: "medium",
        project_id: usercenterProject.id,
        assignee_type: "squad",
        assignee_id: squad.id,
      });
      createdIssueIds.push(parent.id);

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
      createdIssueIds.push(configChild.id, gatewayChild.id);

      await api.updateIssue(gatewayChild.id, { status: "done" });
      let systemCommentId = "";
      await expect
        .poll(async () => {
          const comment = await api.getLatestSystemComment(parent.id);
          if (comment?.content?.includes(`子任务 [${gatewayChild.identifier}]`)) {
            systemCommentId = comment.id;
          }
          return comment?.content ?? "";
        }, { timeout: 15000 })
        .toContain(`子任务 [${gatewayChild.identifier}]`);

      await page.goto(`/${workspaceSlug}/issues/${parent.id}#comment-${systemCommentId}`, { waitUntil: "domcontentloaded" });
      await expect(page.getByRole("link", { name: new RegExp(`${parent.identifier}.*${parent.title}`) })).toBeVisible({ timeout: 15000 });
      await expect(page.getByText(squad.name).first()).toBeVisible({ timeout: 15000 });

      const subIssues = page.getByTestId("issue-sub-issues-section");
      await expect(subIssues).toContainText("子任务", { timeout: 15000 });
      await expect(subIssues).toContainText(gatewayChild.identifier);
      await expect(subIssues).toContainText(gatewayChild.title);
      await expect(subIssues).toContainText(configChild.identifier);
      await expect(subIssues).toContainText(configChild.title);

      const executionLog = page.getByTestId("issue-execution-log-section");
      await expect(executionLog).toContainText("小队 SOP 执行", { timeout: 15000 });
      const executionTree = page.getByTestId("issue-collaboration-execution-tree");
      await expect(executionTree).toContainText("协作执行树", { timeout: 15000 });
      await expect(executionTree).toContainText("父任务");
      await expect(executionTree).toContainText(parent.identifier);
      await expect(executionTree).toContainText("子任务");
      await expect(executionTree).toContainText(gatewayChild.identifier);
      await expect(executionTree).toContainText("唤醒评论");
      await expect(page.getByTestId("issue-sop-run-summary")).toContainText("user-center-sop-flow");
      await expect(page.getByTestId("issue-trace-event-summary")).toContainText("观测事件");
      await expect(page.getByTestId("issue-trace-event-summary")).toContainText("任务事件树");

      const systemComment = page.locator(`#comment-${systemCommentId}`).getByTestId("issue-comment-card");
      await expect(systemComment).toContainText(gatewayChild.identifier, { timeout: 15000 });
      await expect(systemComment).toContainText("子任务");
      await expect(systemComment).toContainText("已完成");
      await expect(systemComment).not.toContainText("Sub-issue");

      await page.goto(`/${workspaceSlug}/issues/${gatewayChild.id}`, { waitUntil: "domcontentloaded" });
      await expect(page.getByText(gatewayChild.identifier).first()).toBeVisible({ timeout: 15000 });
      await expect(page.getByRole("link", { name: new RegExp(`属于父任务 ${parent.identifier}`) })).toBeVisible({ timeout: 15000 });
    } finally {
      for (const id of createdIssueIds) {
        await api.deleteIssue(id).catch(() => undefined);
      }
      for (const id of createdProjectIds) {
        await api.deleteProject(id).catch(() => undefined);
      }
    }
  });
});
