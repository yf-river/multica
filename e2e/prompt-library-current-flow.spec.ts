import { randomUUID } from "node:crypto";
import { expect, test } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { authenticateBrowserSession, waitForPageText } from "./helpers";
import {
  DEFAULT_E2E_ACCOUNT,
  DEFAULT_E2E_NAME,
  DEFAULT_E2E_PASSWORD,
  DEFAULT_E2E_WORKSPACE,
  DEFAULT_E2E_WORKSPACE_NAME,
} from "./test-identity";

test("Prompt Library item and version survive exact replay and render in the current UI", async ({ page }) => {
  const api = new TestApiClient();
  const prefix = `当前提示词闭环 ${Date.now()}`;
  await api.login(DEFAULT_E2E_ACCOUNT, DEFAULT_E2E_NAME, DEFAULT_E2E_PASSWORD);
  const workspace = await api.ensureWorkspace(DEFAULT_E2E_WORKSPACE_NAME, DEFAULT_E2E_WORKSPACE);

  try {
    const itemRequest = {
      name: `${prefix} Item`,
      description: "隔离环境真实创建与恢复验收",
      prompt_type: "需求澄清",
      content: "请澄清 {{issue_title}} 的目标与边界。",
      variables: [{ name: "issue_title", label: "任务标题", required: true }],
      tags: ["E2E", "当前契约"],
      status: "启用",
    };
    const itemKey = randomUUID();
    const created = await api.createPromptLibraryItem(itemRequest, itemKey);
    const replayed = await api.createPromptLibraryItem(itemRequest, itemKey);
    expect(replayed).toEqual(created);

    const versionRequest = {
      content: "请澄清 {{issue_title}} 的目标、边界、验收条件与风险。",
      change_note: "补齐当前验收契约",
    };
    const versionKey = randomUUID();
    const version = await api.createPromptLibraryVersion(created.id, versionRequest, versionKey);
    const versionReplay = await api.createPromptLibraryVersion(created.id, versionRequest, versionKey);
    expect(versionReplay).toEqual(version);

    const versions = await api.listPromptLibraryVersions(created.id);
    expect(versions).toHaveLength(2);
    expect(versions[0]).toMatchObject({
      id: version.version.id,
      version: 2,
      content: versionRequest.content,
      change_note: versionRequest.change_note,
    });

    const { runtime } = await api.registerDaemonCodeBuddyRuntime(`${prefix} Runtime`);
    const agent = await api.createAgent({
      name: `${prefix} Agent`,
      runtime_id: runtime.id,
      instructions: "只处理当前隔离的 Prompt Library 试跑任务。",
      scope: "workspace",
    });
    const trialRequest = {
      agent_id: agent.id,
      variables: { issue_title: "登录失败" },
    };
    const trialKey = randomUUID();
    const trial = await api.createPromptLibraryTrial(created.id, version.version.id, trialRequest, trialKey);
    const trialReplay = await api.createPromptLibraryTrial(created.id, version.version.id, trialRequest, trialKey);
    expect(trialReplay).toEqual(trial);
    expect(trial).toMatchObject({
      prompt_id: created.id,
      version_id: version.version.id,
      agent_id: agent.id,
      status: "queued",
      variables: trialRequest.variables,
    });
    expect(trial.task_id).toBeTruthy();
    expect(trial.rendered_message).toContain("登录失败");
    expect(await api.listPromptLibraryTrials(created.id)).toHaveLength(1);

    const token = api.getToken();
    expect(token).toBeTruthy();
    await authenticateBrowserSession(page, token!, workspace.slug);
    await page.goto(`/${workspace.slug}/debug/prompts`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, created.name, 30_000);
    await page.getByRole("button", { name: new RegExp(created.name) }).click();
    await expect(page.getByTestId("prompt-version-history")).toContainText("版本 2");
    await expect(page.getByTestId("prompt-library-editor")).toContainText(versionRequest.content);
    await expect(page.getByTestId("prompt-library-editor")).toContainText(`${prefix} Agent`);
    await expect(page.getByTestId("prompt-library-editor")).toContainText("登录失败");
  } finally {
    await api.cleanupPromptArtifactsByPrefix(prefix);
    await api.cleanup();
  }
});
