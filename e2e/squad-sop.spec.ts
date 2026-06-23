import { test, expect } from "@playwright/test";
import { loginAsDefault, createTestApi } from "./helpers";
import type { TestApiClient } from "./fixtures";

test.describe("小队 SOP 端到端", () => {
  let api: TestApiClient;
  let workspaceSlug: string;

  test.beforeEach(async ({ page }) => {
    api = await createTestApi();
    workspaceSlug = await loginAsDefault(page);
  });

  test.afterEach(async () => {
    if (api) {
      await api.cleanup();
    }
  });

  test("管理员可从产品入口幂等准备 Multica 编码小队", async ({ page }) => {
    test.setTimeout(120_000);

    await api.cleanupInternalSquadTemplates();
    await api.ensureOnlineCodexRuntime("E2E 内置小队 Codex Runtime");

    await page.goto(`/${workspaceSlug}/squads`, { waitUntil: "domcontentloaded" });
    await page.getByTestId("ensure-multica-coding-squad").click();

    await expect(page.getByRole("heading", { name: "Multica 编码小队" }).first()).toBeVisible({
      timeout: 30_000,
    });
    await expect(page.getByText("队长").first()).toBeVisible();
    await expect(page.getByText("方案设计者").first()).toBeVisible();
    await expect(page.getByText("开发者").first()).toBeVisible();
    await expect(page.getByText("验收者").first()).toBeVisible();
    await expect(page.getByText("规约维护者").first()).toBeVisible();
    await expect(page.getByText("部署运行者").first()).toBeVisible();

    await expect.poll(() => api.getInternalSquadTemplateStats(), {
      timeout: 15_000,
      message: "等待内置编码小队角色写入数据库",
    }).toEqual({
      squad_count: 1,
      agent_count: 6,
      member_count: 6,
    });

    await page.goto(`/${workspaceSlug}/squads`, { waitUntil: "domcontentloaded" });
    await page.getByTestId("ensure-multica-coding-squad").click();
    await expect(page.getByRole("heading", { name: "Multica 编码小队" }).first()).toBeVisible({
      timeout: 30_000,
    });
    await expect.poll(() => api.getInternalSquadTemplateStats(), {
      timeout: 15_000,
      message: "重复准备内置编码小队不应制造重复数据",
    }).toEqual({
      squad_count: 1,
      agent_count: 6,
      member_count: 6,
    });
  });

  test("Multica 编码小队接收 issue 后生成真实队长任务，并在 child done 后再次唤醒父 issue", async () => {
    test.setTimeout(120_000);

    const suffix = Date.now();
    await api.cleanupInternalSquadTemplates();
    await api.ensureOnlineCodexRuntime(`E2E Multica 编码小队 Runtime ${suffix}`);
    const template = await api.ensureInternalSquadTemplate("multica-coding");
    const squad = template.squad;
    const leader = template.agents.find((agent) => agent.role_key === "captain");
    expect(leader).toBeTruthy();
    const issue = await api.createIssue(`E2E 编码小队闭环 ${suffix}`, {
      description: "验证 Multica 编码小队从 issue 分派到 SOP、trace、usage、观测摘要的端到端闭环。",
      status: "todo",
      priority: "high",
      assignee_type: "squad",
      assignee_id: squad.id,
    });

    await expect.poll(
      async () => (await api.findLeaderTask(issue.id, leader!.id))?.id ?? "",
      {
        timeout: 15_000,
        message: "等待小队队长任务自动入队",
      },
    ).not.toBe("");
    const leaderTask = await api.findLeaderTask(issue.id, leader!.id);
    expect(leaderTask).toBeTruthy();
    expect(leaderTask!.is_leader_task).toBe(true);
    expect(leaderTask!.status).toBe("queued");

    await expect.poll(
      async () => {
        const data = await api.listIssueSOPRuns(issue.id);
        return data.items.find((item) => item.profile_key === "multica-coding")?.id ?? "";
      },
      {
        timeout: 15_000,
        message: "等待小队 SOP Run 自动生成",
      },
    ).not.toBe("");
    const initialRuns = await api.listIssueSOPRuns(issue.id);
    const initialRun = initialRuns.items.find((item) => item.profile_key === "multica-coding");
    expect(initialRun).toBeTruthy();
    expect(initialRun!.leader_task_id).toBe(leaderTask!.id);
    expect(initialRun!.current_step_key).toBe("receive");
    expect(initialRun!.events.some((event) => event.event_type === "步骤开始")).toBe(true);
    expect((initialRun!.profile.steps as unknown[]).length).toBe(7);

    await api.completeSquadLeaderTaskViaDaemon(
      leaderTask!,
      "队长输出：已完成 Multica 编码小队需求接收、方案分派、独立验收和可观测证据登记。",
    );

    const runsAfterEvidence = await api.listIssueSOPRuns(issue.id);
    const runAfterEvidence = runsAfterEvidence.items.find((item) => item.id === initialRun!.id);
    expect(runAfterEvidence).toBeTruthy();
    expect(runAfterEvidence!.status).toBe("进行中");

    await expect.poll(
      async () => {
        const data = await api.getWorkspaceObservabilitySummary({ squad_id: squad.id });
        return Number(data.指标["输入 token"] ?? 0);
      },
      {
        timeout: 15_000,
        message: "等待小队观测摘要聚合 SOP、trace 和 token",
      },
    ).toBeGreaterThanOrEqual(36);
    const summary = await api.getWorkspaceObservabilitySummary({ squad_id: squad.id });
    expect(Number(summary.指标["SOP 执行数"])).toBeGreaterThanOrEqual(1);
    expect(Number(summary.指标["SOP 事件数"])).toBeGreaterThanOrEqual(1);
    expect(Number(summary.指标["输入 token"])).toBeGreaterThanOrEqual(36);
    expect(Number(summary.指标["输出 token"])).toBeGreaterThanOrEqual(19);
    expect(Number(summary.指标["预估成本"])).toBeGreaterThan(0);
    expect(Number(summary.指标["证据数"])).toBeGreaterThanOrEqual(1);
    expect(summary.task_trace_total).toBeGreaterThanOrEqual(1);
    expect(summary.model_breakdown[0]).toMatchObject({
      "名称": "gpt-5.3-codex-spark",
      "价格已知": true,
    });
    expect(summary.runtime_breakdown[0]).toMatchObject({
      runtime: leaderTask!.runtime_id,
      "价格已知": true,
    });

    const child = await api.createIssue(`E2E Multica 子 issue ${suffix}`, {
      description: "验证 child issue done 会通过 system comment 再次唤醒父 issue 的 squad leader。",
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
        return task?.id && task.id !== leaderTask!.id ? task.id : "";
      },
      {
        timeout: 20_000,
        message: "等待 child-done system comment 再次唤醒父 issue",
      },
    ).not.toBe("");

    const requeuedTask = await api.findLeaderTask(issue.id, leader!.id);
    expect(requeuedTask).toBeTruthy();
    expect(requeuedTask!.id).not.toBe(leaderTask!.id);
    expect(requeuedTask!.is_leader_task).toBe(true);
    expect(["queued", "dispatched", "running", "completed"]).toContain(requeuedTask!.status);
  });

  test("user-center 小队接收 issue 后生成真实队长任务，并由 daemon 回写 trace/messages/usage", async () => {
    test.setTimeout(120_000);

    const suffix = Date.now();
    await api.cleanupInternalSquadTemplates();
    await api.ensureOnlineCodexRuntime(`E2E user-center 小队 Runtime ${suffix}`);
    const template = await api.ensureInternalSquadTemplate("user-center");
    const squad = template.squad;
    const leader = template.agents.find((agent) => agent.role_key === "captain");
    expect(leader).toBeTruthy();
    const issue = await api.createIssue(`E2E user-center 小队闭环 ${suffix}`, {
      description: "验证 user-center 小队从 issue 分派到 SOP 阶段、队长任务、trace 和观测摘要。",
      status: "todo",
      priority: "medium",
      assignee_type: "squad",
      assignee_id: squad.id,
    });

    await expect.poll(
      async () => (await api.findLeaderTask(issue.id, leader!.id))?.id ?? "",
      {
        timeout: 15_000,
        message: "等待 user-center 小队队长任务自动入队",
      },
    ).not.toBe("");
    const leaderTask = await api.findLeaderTask(issue.id, leader!.id);
    expect(leaderTask).toBeTruthy();
    expect(leaderTask!.status).toBe("queued");

    await expect.poll(
      async () => {
        const data = await api.listIssueSOPRuns(issue.id);
        return data.items.find((item) => item.profile_key === "user-center-sop-flow")?.id ?? "";
      },
      {
        timeout: 15_000,
        message: "等待 user-center SOP Run 自动生成",
      },
    ).not.toBe("");
    const runs = await api.listIssueSOPRuns(issue.id);
    const run = runs.items.find((item) => item.profile_key === "user-center-sop-flow");
    expect(run).toBeTruthy();
    expect(run!.current_step_key).toBe("clarify");
    expect((run!.profile.steps as unknown[]).length).toBe(5);

    await api.completeSquadLeaderTaskViaDaemon(
      leaderTask!,
      "队长输出：user-center 小队已完成澄清、方案拆解、skill 执行和验收证据登记。",
    );

    const runsAfterEvidence = await api.listIssueSOPRuns(issue.id);
    const runAfterEvidence = runsAfterEvidence.items.find((item) => item.id === run!.id);
    expect(runAfterEvidence).toBeTruthy();
    expect(runAfterEvidence!.status).toBe("进行中");

    await expect.poll(
      async () => {
        const data = await api.getWorkspaceObservabilitySummary({ squad_id: squad.id });
        return Number(data.指标["输入 token"] ?? 0);
      },
      {
        timeout: 15_000,
        message: "等待 user-center 小队观测摘要聚合",
      },
    ).toBeGreaterThanOrEqual(36);
    const summary = await api.getWorkspaceObservabilitySummary({ squad_id: squad.id });
    expect(Number(summary.指标["SOP 执行数"])).toBeGreaterThanOrEqual(1);
    expect(Number(summary.指标["SOP 事件数"])).toBeGreaterThanOrEqual(1);
    expect(Number(summary.指标["输入 token"])).toBeGreaterThanOrEqual(36);
    expect(Number(summary.指标["输出 token"])).toBeGreaterThanOrEqual(19);
    expect(Number(summary.指标["预估成本"])).toBeGreaterThan(0);
    expect(summary.task_trace_total).toBeGreaterThanOrEqual(1);
  });
});
