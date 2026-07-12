import { expect, test } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { authenticateBrowserSession } from "./helpers";
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

  let createCalls = 0;
  const requestKeys: string[] = [];
  await page.route("**/api/workspaces", async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    createCalls += 1;
    requestKeys.push(route.request().headers()["idempotency-key"] ?? "");
    if (createCalls === 1) {
      const committed = await route.fetch();
      expect(committed.status()).toBe(201);
      await route.abort("connectionfailed");
      return;
    }
    await route.continue();
  });

  try {
    await authenticateBrowserSession(page, token!);
    await page.goto("/workspaces/new", { waitUntil: "domcontentloaded" });
    await page.getByLabel("工作区名称").fill("Recovered Workspace");
    await page.getByLabel("工作区 URL").fill(slug);
    await page.getByRole("button", { name: "创建工作区" }).click();
    await expect(page).toHaveURL(new RegExp(`/${slug}/issues`), { timeout: 30_000 });

    expect(createCalls).toBe(2);
    expect(requestKeys[0]).toMatch(/^[0-9a-f-]{36}$/);
    expect(requestKeys[1]).toBe(requestKeys[0]);
    const created = (await api.getWorkspaces()).filter((workspace) => workspace.slug === slug);
    expect(created).toHaveLength(1);
    expect(created[0]?.id).toBe(requestKeys[0]);
  } finally {
    const created = (await api.getWorkspaces()).find((workspace) => workspace.slug === slug);
    if (created) await api.deleteWorkspace(created.id);
    await api.cleanup();
  }
});
