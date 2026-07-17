import { expect, test } from "@playwright/test";
import { createTestApi, loginAsDefault, waitForIssueSOPRun, waitForSquadLeaderTask } from "./helpers";

test.describe("Multica 编码小队页面证据", () => {
  test("公开模板 API 创建的编码小队可在页面绑定 issue 并回看观测证据", async ({ page }) => {
    test.setTimeout(120_000);

    const workspaceSlug = await loginAsDefault(page);
    const api = await createTestApi();

    try {
      const suffix = Date.now();
      await api.registerDaemonCodeBuddyRuntime(`E2E Multica 编码小队 Runtime ${suffix}`);
      const template = await api.ensureInternalSquadTemplate("multica-coding");
      const squad = template.squad;
      const leader = template.agents.find((agent) => agent.role_key === "captain");
      const designer = template.agents.find((agent) => agent.role_key === "designer");
      expect(leader).toBeTruthy();
      expect(designer).toBeTruthy();

      await page.goto(`/${workspaceSlug}/squads/${squad.id}`, { waitUntil: "domcontentloaded" });
      await expect(page.getByRole("heading", { name: "Multica 编码小队" }).first()).toBeVisible({
        timeout: 30_000,
      });
      for (const role of ["队长", "方案设计者", "开发者", "验收者", "规约维护者", "部署运行者"]) {
        await expect(page.getByText(role).first()).toBeVisible({ timeout: 15_000 });
      }
      await page.getByRole("button", { name: "指令" }).click();
      const squadInstructions = page.getByRole("textbox", { name: /先澄清需求和验收口径/ });
      await expect(squadInstructions).toContainText("队长先澄清需求和验收口径");
      await expect(squadInstructions).toContainText("验收者必须独立给出证据");
      await expect(squadInstructions).toContainText("所有指标和输出使用中文");

      const project = await api.createProject(`UI Multica 编码项目 ${suffix}`);

      const issue = await api.createIssue(`UI Multica 编码小队需求 ${suffix}`, {
        description:
          "页面验收：Multica 编码小队必须先进入方案设计与确认，由方案设计者产生人工确认事件，再允许后续开发。",
        status: "todo",
        priority: "high",
        project_id: project.id,
        assignee_type: "squad",
        assignee_id: squad.id,
      });

      const leaderTask = await waitForSquadLeaderTask(api, issue.id, leader!.id, {
        message: "等待 Multica 编码小队队长任务入队",
      });
      expect(leaderTask.is_leader_task).toBe(true);
      const run = await waitForIssueSOPRun(api, issue.id, "multica-coding", {
        message: "等待 Multica 编码小队 SOP Run 自动生成",
      });

      await api.completeSquadLeaderTaskViaDaemon(
        leaderTask,
        "队长输出：已接收需求，并分派方案设计者先输出技术方案、影响面和测试方案，等待人工确认后再进入开发。",
      );
      await api.reportDaemonTaskMessages(leaderTask!.id, [
        {
          seq: 2,
          type: "tool_use",
          tool: "multica squad activity",
          input: { action: "record_design_gate" },
        },
        {
          seq: 3,
          type: "tool_result",
          tool: "multica squad activity",
          output: "已记录编码小队方案确认门禁，等待人工确认后再开发。",
        },
      ]);
      const runsAfterLeader = await api.listIssueSOPRuns(issue.id);
      const runAfterLeader = runsAfterLeader.items.find((item) => item.id === run.id);
      expect(runAfterLeader?.current_step_key).toBe("design_review");
      expect(
        runAfterLeader?.events.some(
          (event) => event.step_key === "receive" && event.event_type === "步骤完成" && event.status === "已完成",
        ),
      ).toBe(true);
      await page.goto(`/${workspaceSlug}/issues/${issue.id}`, { waitUntil: "domcontentloaded" });
      await expect(page.getByRole("link", { name: new RegExp(`${issue.identifier}.*${issue.title}`) })).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.getByText(squad.name).first()).toBeVisible({ timeout: 15_000 });

      await expect(page.getByTestId("issue-execution-log-section")).toHaveCount(0);
      const reviewSummary = page.getByTestId("issue-run-review-summary-card");
      await expect(reviewSummary).toContainText("运行复盘", { timeout: 15_000 });
      await expect(reviewSummary).toContainText("任务数：1");
      await expect(reviewSummary).toContainText("Token：60");
      await reviewSummary.getByRole("link", { name: "查看完整复盘" }).click();

      await expect(page).toHaveURL(new RegExp(`/${workspaceSlug}/run-reviews\\?issue=${issue.id}$`), { timeout: 15_000 });
      await expect(page.getByRole("heading", { name: issue.title })).toBeVisible();
      await expect(page.getByTestId("run-review-horizontal-timeline")).toBeVisible();
      await expect(page.getByTestId("run-review-event-group").first()).toBeVisible();
      await expect(page.getByText("multica squad activity", { exact: true }).first()).toBeVisible();
      await expect(page.getByText(/等待人工确认后再开发/).first()).toBeVisible();
    } finally {
      await api.cleanup();
    }
  });
});
