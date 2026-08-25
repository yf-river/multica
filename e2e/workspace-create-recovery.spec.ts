import { expect, test } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import {
  authenticateBrowserSession,
  expectCommittedResponseRecovery,
  interceptCommittedResponseLoss,
} from "./helpers";
import {
  DEFAULT_E2E_ACCOUNT,
  DEFAULT_E2E_NAME,
  DEFAULT_E2E_PASSWORD,
} from "./test-identity";

test("workspace creation recovers after the committed response is lost", async ({ page }) => {
  const api = new TestApiClient();
  const slug = `recovered-workspace-${Date.now()}`;
  await api.login(DEFAULT_E2E_ACCOUNT, DEFAULT_E2E_NAME, DEFAULT_E2E_PASSWORD);
  const token = api.getToken();
  expect(token).toBeTruthy();

  const recovery = await interceptCommittedResponseLoss(page, "**/api/workspaces", 201);

  try {
    await authenticateBrowserSession(page, token!);
    await page.goto("/workspaces/new", { waitUntil: "domcontentloaded" });
    await page.getByLabel("工作区名称").fill("Recovered Workspace");
    await page.getByLabel("工作区 URL").fill(slug);
    await page.getByRole("button", { name: "创建工作区" }).click();
    await expect(page).toHaveURL(new RegExp(`/${slug}/issues`), { timeout: 30_000 });

    expectCommittedResponseRecovery(recovery);
    const created = (await api.getWorkspaces()).filter((workspace) => workspace.slug === slug);
    expect(created).toHaveLength(1);
    expect(created[0]?.id).toBe(recovery.requestKeys[0]);
  } finally {
    const created = (await api.getWorkspaces()).find((workspace) => workspace.slug === slug);
    if (created) await api.deleteWorkspace(created.id);
    await api.cleanup();
  }
});
