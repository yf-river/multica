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

  test("Multica 编码小队接收 issue 后生成队长任务、SOP 证据和观测指标", async ({ page }) => {
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

    for (const [stepId, stepName, nextStep] of [
      ["receive", "接收需求", "design_review"],
      ["design_review", "方案设计与确认", "implementation"],
      ["implementation", "分工开发", "independent_acceptance"],
    ] as const) {
      await api.recordSOPStepEvent(initialRun!.id, stepId, {
        event_type: "步骤完成",
        evidence: {
          "阶段": stepName,
          "结果": `已进入 ${nextStep}`,
        },
        reason: `${stepName}完成`,
      });
      const progressed = await api.listIssueSOPRuns(issue.id);
      const progressedRun = progressed.items.find((item) => item.id === initialRun!.id);
      expect(progressedRun?.current_step_key).toBe(nextStep);
      expect(progressedRun?.status).toBe("进行中");
    }

    await api.completeSquadLeaderTaskViaDaemon(
      leaderTask!,
      "队长输出：已完成 Multica 编码小队需求接收、方案分派、独立验收和可观测证据登记。",
    );
    const recordedEvent = await api.recordSOPStepEvent(initialRun!.id, "independent_acceptance", {
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
      created_by_id: leader!.id,
    });
    expect(recordedEvent.created_by_type).not.toBe("agent");

    const runsAfterEvidence = await api.listIssueSOPRuns(issue.id);
    const runAfterEvidence = runsAfterEvidence.items.find((item) => item.id === initialRun!.id);
    expect(runAfterEvidence).toBeTruthy();
    expect(runAfterEvidence!.events.some((event) => event.event_type === "测试结果")).toBe(true);
    expect(Number(runAfterEvidence!.metrics["证据数"])).toBeGreaterThanOrEqual(1);

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
    expect(Number(summary.指标["SOP 事件数"])).toBeGreaterThanOrEqual(2);
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

    await page.goto(`/${workspaceSlug}/issues/${issue.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByText(issue.title, { exact: true }).first()).toBeVisible();
    await expect(page.getByText("小队 SOP 执行", { exact: true }).first()).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("multica-coding").first()).toBeVisible();
    await expect(page.getByText("独立验收 · 测试结果", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("E2E 独立验收通过", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("观测事件", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("任务已完成", { exact: true }).first()).toBeVisible();

    await page.goto(`/${workspaceSlug}/squads/${squad.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByRole("heading", { name: squad.name }).first()).toBeVisible();
    await expect(page.getByText("队长").first()).toBeVisible();
    await expect(page.getByText("方案设计者").first()).toBeVisible();
    await expect(page.getByText("开发者").first()).toBeVisible();
    await expect(page.getByText("验收者").first()).toBeVisible();
    await expect(page.getByText("规约维护者").first()).toBeVisible();
    await expect(page.getByText("部署运行者").first()).toBeVisible();

    await page.getByRole("button", { name: "指令" }).click();
    await expect(page.getByText("multica-coding").first()).toBeVisible();
    await expect(page.getByText("角色矩阵", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("小队观测摘要", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("SOP 执行数", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("输入 token", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("预估成本", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("模型明细", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("runtime 明细", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("证据数", { exact: true }).first()).toBeVisible();

    await page.goto(`/${workspaceSlug}/agents/${leader!.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByText("Agent 观测摘要", { exact: true }).first()).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("按当前 Agent 聚合 trace、token、成本、耗时和证据", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("预估成本", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("gpt-5.3-codex-spark").first()).toBeVisible();
  });

  test("user-center 小队接收 issue 后按 SOP 阶段推进并形成观测证据", async ({ page }) => {
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

    for (const [stepId, stepName, nextStep] of [
      ["clarify", "需求澄清", "design"],
      ["design", "方案拆解", "skill_execution"],
      ["skill_execution", "skill 执行", "acceptance"],
    ] as const) {
      await api.recordSOPStepEvent(run!.id, stepId, {
        event_type: "步骤完成",
        evidence: {
          "阶段": stepName,
          "user-center skill": stepId,
          "结果": `已进入 ${nextStep}`,
        },
        reason: `${stepName}完成`,
      });
      const progressed = await api.listIssueSOPRuns(issue.id);
      const progressedRun = progressed.items.find((item) => item.id === run!.id);
      expect(progressedRun?.current_step_key).toBe(nextStep);
    }

    await api.completeSquadLeaderTaskViaDaemon(
      leaderTask!,
      "队长输出：user-center 小队已完成澄清、方案拆解、skill 执行和验收证据登记。",
    );
    await api.recordSOPStepEvent(run!.id, "acceptance", {
      event_type: "测试结果",
      status: "进行中",
      step_name: "验收",
      role_key: "acceptor",
      evidence: {
        "验收者": "验收者",
        "结果": "通过",
        "证据": "daemon usage、message、trace 已写入",
      },
      reason: "user-center 小队验收通过",
      duration_ms: 260,
      task_id: leaderTask!.id,
    });

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
    expect(Number(summary.指标["SOP 事件数"])).toBeGreaterThanOrEqual(2);
    expect(summary.task_trace_total).toBeGreaterThanOrEqual(1);

    await page.goto(`/${workspaceSlug}/issues/${issue.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByText(issue.title, { exact: true }).first()).toBeVisible();
    await expect(page.getByText("小队 SOP 执行", { exact: true }).first()).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText("user-center-sop-flow").first()).toBeVisible();
    await expect(page.getByText("验收 · 测试结果", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("user-center 小队验收通过", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("观测事件", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("任务已完成", { exact: true }).first()).toBeVisible();

    await page.goto(`/${workspaceSlug}/squads/${squad.id}`, { waitUntil: "domcontentloaded" });
    await expect(page.getByRole("heading", { name: "user-center 小队" }).first()).toBeVisible();
    await page.getByRole("button", { name: "指令" }).click();
    await expect(page.getByText("user-center-sop-flow").first()).toBeVisible();
    await expect(page.getByText("小队观测摘要", { exact: true }).first()).toBeVisible();
    await expect(page.getByText("gpt-5.3-codex-spark").first()).toBeVisible();
  });
});
