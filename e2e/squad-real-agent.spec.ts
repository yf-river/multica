import { test, expect } from "@playwright/test";

import { TestApiClient } from "./fixtures";
import { authenticateBrowserSession, waitForPageText } from "./helpers";

const RUN_REAL_AGENT_E2E = process.env.RUN_REAL_AGENT_E2E === "1";
const REAL_AGENT_ACCOUNT = process.env.REAL_AGENT_E2E_ACCOUNT || "develop";
const REAL_AGENT_WORKSPACE = process.env.REAL_AGENT_E2E_WORKSPACE || "ai-studio";
const EXPECTED_AGENT_PROVIDER = process.env.MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER || "codebuddy";
const EXPECTED_AGENT_MODEL = process.env.MULTICA_PROMPT_EVALUATION_AGENT_MODEL || "deepseek-v4-pro-ioa";

test.describe("小队真实 Agent 闭环", () => {
  test.skip(!RUN_REAL_AGENT_E2E, "设置 RUN_REAL_AGENT_E2E=1 后才运行真实 daemon/CodeBuddy 小队验收");

  test("CodeBuddy daemon 可以真实执行 user-center 小队队长任务，并在 child done 后被系统评论再次唤醒", async ({ page }) => {
    test.setTimeout(240_000);

    const api = new TestApiClient();
    const suffix = Date.now();
    await api.login(REAL_AGENT_ACCOUNT, "胡云飞");
    const workspace = await api.ensureWorkspace("AI Studio 工作区", REAL_AGENT_WORKSPACE);
    await api.markUserOnboarded();

    try {
      const readiness = await api.getPromptEvaluationRuntimeReadiness();
      expect(readiness).toMatchObject({
        status: "就绪",
      });
      expect(readiness.model).toBe(EXPECTED_AGENT_MODEL);
      expect(readiness.runtime).toMatchObject({
        provider: EXPECTED_AGENT_PROVIDER,
        status: "online",
      });

      await api.cleanupInternalSquadTemplates();
      const template = await api.ensureInternalSquadTemplate("user-center");
      const squad = template.squad;
      const leader = template.agents.find((agent) => agent.role_key === "captain");
      expect(leader).toBeTruthy();

      const issue = await api.createIssue(`真实小队验收 user-center ${suffix}`, {
        description: "真实 daemon 验证：user-center 小队队长必须接收 issue、执行任务，并写回 trace、消息和可观测证据。",
        status: "todo",
        priority: "medium",
        assignee_type: "squad",
        assignee_id: squad.id,
      });

      await expect.poll(
        async () => (await api.findLeaderTask(issue.id, leader!.id))?.id ?? "",
        {
          timeout: 20_000,
          message: "等待真实 user-center 小队队长任务入队",
        },
      ).not.toBe("");
      const queuedTask = await api.findLeaderTask(issue.id, leader!.id);
      expect(queuedTask).toBeTruthy();
      expect(["queued", "dispatched", "running", "completed", "failed"]).toContain(queuedTask!.status);

      await expect.poll(
        async () => {
          const runs = await api.listIssueSOPRuns(issue.id);
          return runs.items.find((item) => item.profile_key === "user-center-sop-flow")?.id ?? "";
        },
        {
          timeout: 20_000,
          message: "等待真实小队 SOP Run 生成",
        },
      ).not.toBe("");

      const terminalTask = await expect
        .poll(
          async () => {
            const task = await api.findLeaderTask(issue.id, leader!.id);
            if (!task || ["queued", "dispatched", "running"].includes(task.status)) {
              return null;
            }
            return task;
          },
          {
            timeout: 180_000,
            intervals: [3_000, 5_000, 10_000],
            message: "等待真实 daemon 完成或失败小队队长任务",
          },
        )
        .not.toBeNull()
        .then(async () => (await api.findLeaderTask(issue.id, leader!.id))!);

      expect(["completed", "failed", "cancelled"]).toContain(terminalTask.status);
      const evidence = await api.getTaskExecutionEvidence(terminalTask.id);
      expect(evidence.trace_events.length).toBeGreaterThan(0);
      expect(JSON.stringify(evidence.trace_events)).toMatch(/任务已领取|任务已开始|任务已完成|任务已失败/);
      expect(evidence.messages.length).toBeGreaterThan(0);
      if (terminalTask.status === "completed") {
        expect(evidence.usage.length).toBeGreaterThan(0);
        expect(JSON.stringify(evidence.usage)).toContain(EXPECTED_AGENT_PROVIDER);
        expect(JSON.stringify(evidence.usage)).toContain(EXPECTED_AGENT_MODEL);
      } else {
        expect(JSON.stringify(evidence.trace_events) + terminalTask.error).toMatch(/失败|取消|额度|error|failed/i);
      }

      const summary = await api.getWorkspaceObservabilitySummary({ squad_id: squad.id });
      expect(Number(summary.指标["SOP 执行数"])).toBeGreaterThanOrEqual(1);
      expect(summary.task_trace_total).toBeGreaterThanOrEqual(1);

      const child = await api.createIssue(`真实小队子任务验收 ${suffix}`, {
        description: "验证 child issue 完成后，父 issue 会通过 system comment 再次唤醒 squad leader。",
        status: "todo",
        priority: "medium",
        parent_issue_id: issue.id,
      });
      await api.updateIssue(child.id, { status: "done" });

      await expect.poll(
        async () => {
          const comment = await api.getLatestSystemComment(issue.id);
          return comment?.content ?? "";
        },
        {
          timeout: 20_000,
          message: "等待 child-done system comment 写回父 issue",
        },
      ).not.toBe("");
      const parentComment = await api.getLatestSystemComment(issue.id);
      expect(parentComment).toBeTruthy();
      expect(parentComment!.author_type).toBe("system");
      expect(parentComment!.parent_id).toBeNull();
      expect(parentComment!.content).toContain(child.identifier);
      expect(parentComment!.content).toContain(`mention://squad/${squad.id}`);

      await expect.poll(
        async () => {
          const task = await api.findLeaderTask(issue.id, leader!.id);
          return task?.id && task.id !== terminalTask.id ? task.id : "";
        },
        {
          timeout: 20_000,
          message: "等待 parent issue 被 child-done system comment 再次唤醒",
        },
      ).not.toBe("");
      const requeuedTask = await api.findLeaderTask(issue.id, leader!.id);
      expect(requeuedTask).toBeTruthy();
      expect(requeuedTask!.id).not.toBe(terminalTask.id);
      expect(requeuedTask!.is_leader_task).toBe(true);
      expect(["queued", "dispatched", "running", "completed"]).toContain(requeuedTask!.status);

      const token = api.getToken();
      expect(token).toBeTruthy();
      await authenticateBrowserSession(page, token!, workspace.slug);
      await page.goto(`/${workspace.slug}/issues/${issue.id}`, { waitUntil: "domcontentloaded" });
      await waitForPageText(page, issue.title, 15_000);
      await expect(page.getByText("小队 SOP 执行", { exact: true }).first()).toBeVisible({ timeout: 15_000 });
      await expect(page.getByText("观测事件", { exact: true }).first()).toBeVisible();
      await expect(page.getByText("任务事件树", { exact: true }).first()).toBeVisible();
      await expect(page.getByText(/任务已完成|任务已失败|任务已取消/).first()).toBeVisible();
    } finally {
      await api.cleanup();
    }
  });
});
