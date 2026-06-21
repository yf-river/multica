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

  test("Multica 编码小队接收 issue 后生成队长任务、SOP 证据和观测指标", async ({ page }) => {
    const suffix = Date.now();
    const squad = await api.createCodingSquadFixture(`E2E Multica 编码小队 ${suffix}`);
    const issue = await api.createIssue(`E2E 编码小队闭环 ${suffix}`, {
      description: "验证 Multica 编码小队从 issue 分派到 SOP、trace、usage、观测摘要的端到端闭环。",
      status: "todo",
      priority: "high",
      assignee_type: "squad",
      assignee_id: squad.squadId,
    });

    await expect.poll(
      async () => (await api.findLeaderTask(issue.id, squad.leaderAgentId))?.id ?? "",
      {
        timeout: 15_000,
        message: "等待小队队长任务自动入队",
      },
    ).not.toBe("");
    const leaderTask = await api.findLeaderTask(issue.id, squad.leaderAgentId);
    expect(leaderTask).toBeTruthy();
    expect(leaderTask!.is_leader_task).toBe(true);
    expect(leaderTask!.status).toBe("queued");

    await expect.poll(
      async () => {
        const data = await api.listIssueSOPRuns(issue.id);
        return data.items.find((item) => item.profile_key === "multica-coding-squad-v1")?.id ?? "";
      },
      {
        timeout: 15_000,
        message: "等待小队 SOP Run 自动生成",
      },
    ).not.toBe("");
    const initialRuns = await api.listIssueSOPRuns(issue.id);
    const initialRun = initialRuns.items.find((item) => item.profile_key === "multica-coding-squad-v1");
    expect(initialRun).toBeTruthy();
    expect(initialRun!.leader_task_id).toBe(leaderTask!.id);
    expect(initialRun!.current_step_key).toBe("receive");
    expect(initialRun!.events.some((event) => event.event_type === "步骤开始")).toBe(true);
    expect((initialRun!.profile.steps as unknown[]).length).toBe(6);

    await api.completeSquadLeaderTaskWithEvidence(leaderTask!, { squadId: squad.squadId });
    const recordedEvent = await api.recordSOPStepEvent(initialRun!.id, "acceptance", {
      event_type: "测试结果",
      status: "进行中",
      step_name: "独立验收",
      role_key: "acceptor",
      evidence: {
        "验收者": "验收者",
        "测试命令": "pnpm exec playwright test e2e/squad-sop.spec.ts",
        "结果": "通过",
      },
      reason: "E2E 独立验收通过",
      duration_ms: 321,
      task_id: leaderTask!.id,
      created_by_type: "agent",
      created_by_id: squad.leaderAgentId,
    });
    expect(recordedEvent.created_by_type).not.toBe("agent");

    const runsAfterEvidence = await api.listIssueSOPRuns(issue.id);
    const runAfterEvidence = runsAfterEvidence.items.find((item) => item.id === initialRun!.id);
    expect(runAfterEvidence).toBeTruthy();
    expect(runAfterEvidence!.events.some((event) => event.event_type === "测试结果")).toBe(true);
    expect(Number(runAfterEvidence!.metrics["证据数"])).toBeGreaterThanOrEqual(1);

    await expect.poll(
      async () => {
        const data = await api.getWorkspaceObservabilitySummary({ squad_id: squad.squadId });
        return Number(data.指标["输入 token"] ?? 0);
      },
      {
        timeout: 15_000,
        message: "等待小队观测摘要聚合 SOP、trace 和 token",
      },
    ).toBeGreaterThanOrEqual(36);
    const summary = await api.getWorkspaceObservabilitySummary({ squad_id: squad.squadId });
    expect(Number(summary.指标["SOP 执行数"])).toBeGreaterThanOrEqual(1);
    expect(Number(summary.指标["SOP 事件数"])).toBeGreaterThanOrEqual(2);
    expect(Number(summary.指标["输入 token"])).toBeGreaterThanOrEqual(36);
    expect(Number(summary.指标["输出 token"])).toBeGreaterThanOrEqual(19);
    expect(Number(summary.指标["预估成本"])).toBeGreaterThan(0);
    expect(Number(summary.指标["证据数"])).toBeGreaterThanOrEqual(1);
    expect(summary.task_trace_total).toBeGreaterThanOrEqual(1);
    expect(summary.model_breakdown[0]).toMatchObject({
      "名称": "minimax/m2.7",
      "价格已知": true,
    });
    expect(summary.runtime_breakdown[0]).toMatchObject({
      runtime: squad.runtimeId,
      "价格已知": true,
    });

    await page.goto(`/${workspaceSlug}/issues/${issue.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByText(issue.title, { exact: true }).first()).toBeVisible();
    await expect(page.getByText("小队 SOP 执行", { exact: true }).first()).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("multica-coding-squad-v1").first()).toBeVisible();
    await expect(page.getByText("独立验收 · 测试结果", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("E2E 独立验收通过", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("观测事件", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("编码小队队长任务完成", { exact: true }).first()).toBeVisible();

    await page.goto(`/${workspaceSlug}/squads/${squad.squadId}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByRole("heading", { name: squad.squadName }).first()).toBeVisible();
    await expect(page.getByText("队长").first()).toBeVisible();
    await expect(page.getByText("方案设计者").first()).toBeVisible();
    await expect(page.getByText("开发者").first()).toBeVisible();
    await expect(page.getByText("验收者").first()).toBeVisible();
    await expect(page.getByText("规约维护者").first()).toBeVisible();
    await expect(page.getByText("部署运行者").first()).toBeVisible();

    await page.getByRole("button", { name: "指令" }).click();
    await expect(page.getByText("multica-coding-squad-v1").first()).toBeVisible();
    await expect(page.getByText("角色矩阵", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("小队观测摘要", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("SOP 执行数", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("输入 token", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("预估成本", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("模型明细", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("runtime 明细", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("证据数", { exact: true }).first()).toBeVisible();

    await page.goto(`/${workspaceSlug}/agents/${squad.leaderAgentId}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByText("Agent 观测摘要", { exact: true }).first()).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("按当前 Agent 聚合 trace、token、成本、耗时和证据", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("预估成本", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("minimax/m2.7").first()).toBeVisible();
  });
});
