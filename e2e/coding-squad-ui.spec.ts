import { expect, test } from "@playwright/test";
import { createTestApi, loginAsDefault } from "./helpers";

test.describe("Multica 编码小队页面证据", () => {
  test("可以创建编码小队、绑定 issue，并回看方案确认与观测证据", async ({ page }) => {
    test.setTimeout(120_000);

    const workspaceSlug = await loginAsDefault(page);
    const api = await createTestApi();
    const createdIssueIds: string[] = [];
    const createdProjectIds: string[] = [];

    try {
      const suffix = Date.now();

      await page.goto(`/${workspaceSlug}/squads`, { waitUntil: "domcontentloaded" });
      await page.getByTestId("ensure-multica-coding-squad").click();
      await expect(page.getByRole("heading", { name: "Multica 编码小队" }).first()).toBeVisible({
        timeout: 30_000,
      });
      for (const role of ["队长", "方案设计者", "开发者", "验收者", "规约维护者", "部署运行者"]) {
        await expect(page.getByText(role).first()).toBeVisible({ timeout: 15_000 });
      }
      await page.getByRole("button", { name: "指令" }).click();
      await expect(page.getByText("方案设计与确认").first()).toBeVisible({ timeout: 15_000 });
      await expect(page.getByText("方案经确认").first()).toBeVisible({ timeout: 15_000 });
      await expect(page.getByText("未独立验收就完成").first()).toBeVisible({ timeout: 15_000 });

      const template = await api.ensureInternalSquadTemplate("multica-coding");
      const squad = template.squad;
      const leader = template.agents.find((agent) => agent.role_key === "captain");
      const designer = template.agents.find((agent) => agent.role_key === "designer");
      expect(leader).toBeTruthy();
      expect(designer).toBeTruthy();

      const project = await api.createProject(`UI Multica 编码项目 ${suffix}`);
      createdProjectIds.push(project.id);

      const issue = await api.createIssue(`UI Multica 编码小队需求 ${suffix}`, {
        description:
          "页面验收：Multica 编码小队必须先进入方案设计与确认，由方案设计者产生人工确认事件，再允许后续开发。",
        status: "todo",
        priority: "high",
        project_id: project.id,
        assignee_type: "squad",
        assignee_id: squad.id,
      });
      createdIssueIds.push(issue.id);

      await expect
        .poll(async () => (await api.findLeaderTask(issue.id, leader!.id))?.id ?? "", {
          timeout: 15_000,
          message: "等待 Multica 编码小队队长任务入队",
        })
        .not.toBe("");
      const leaderTask = await api.findLeaderTask(issue.id, leader!.id);
      expect(leaderTask).toBeTruthy();
      expect(leaderTask!.is_leader_task).toBe(true);

      await expect
        .poll(async () => {
          const data = await api.listIssueSOPRuns(issue.id);
          return data.items.find((item) => item.profile_key === "multica-coding")?.id ?? "";
        }, {
          timeout: 15_000,
          message: "等待 Multica 编码小队 SOP Run 自动生成",
        })
        .not.toBe("");
      const runs = await api.listIssueSOPRuns(issue.id);
      const run = runs.items.find((item) => item.profile_key === "multica-coding");
      expect(run).toBeTruthy();

      await api.completeSquadLeaderTaskViaDaemon(
        leaderTask!,
        "队长输出：已接收需求，并分派方案设计者先输出技术方案、影响面和测试方案，等待人工确认后再进入开发。",
      );
      await api.recordSOPStepEvent(run!.id, "receive", {
        event_type: "步骤完成",
        status: "已完成",
        reason: "队长已完成需求接收和角色分派",
        duration_ms: 1200,
        evidence: { 角色: "队长", 下一阶段: "方案设计与确认" },
      });
      await api.recordSOPStepEvent(run!.id, "design_review", {
        event_type: "人工确认",
        status: "进行中",
        role_key: "designer",
        reason: "方案设计者已提交方案，等待人工确认后再开发",
        duration_ms: 1800,
        evidence: {
          角色: "方案设计者",
          方案状态: "待人工确认",
          开发门禁: "未确认前不允许开发者开始实现",
          测试方案: "覆盖前端、后端、E2E 和日志门禁",
        },
      });

      await page.goto(`/${workspaceSlug}/issues/${issue.id}`, { waitUntil: "domcontentloaded" });
      await expect(page.getByRole("link", { name: new RegExp(`${issue.identifier}.*${issue.title}`) })).toBeVisible({
        timeout: 15_000,
      });
      await expect(page.getByText(squad.name).first()).toBeVisible({ timeout: 15_000 });

      const executionLog = page.getByTestId("issue-execution-log-section");
      await expect(executionLog).toContainText("小队 SOP 执行", { timeout: 15_000 });
      const sopSummary = page.getByTestId("issue-sop-run-summary");
      await expect(sopSummary).toContainText("multica-coding");
      await expect(sopSummary).toContainText("design_review");
      await expect(sopSummary).toContainText("方案设计与确认");
      await expect(sopSummary).toContainText("人工确认");
      await expect(sopSummary).toContainText("等待人工确认后再开发");
      await expect(page.getByTestId("issue-trace-event-summary")).toContainText("观测事件");
      await expect(page.getByTestId("issue-trace-event-summary")).toContainText("任务事件树");
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
