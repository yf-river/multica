import { expect, test } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { createTestApi, loginAsDefault, waitForPageText } from "./helpers";

test("member creation is atomic and recovers after the committed response is lost", async ({ page }) => {
  const api = await createTestApi();
  const workspaceSlug = await loginAsDefault(page);
  const workspace = (await api.getWorkspaces()).find((item) => item.slug === workspaceSlug);
  if (!workspace) throw new Error(`workspace fixture not found: ${workspaceSlug}`);
  const suffix = Date.now().toString(36);
  const account = `member_recovery_${suffix}`;
  const name = `Recovered Member ${suffix}`;
  const password = `Recovered-${suffix}-Member1!`;
  let memberId: string | null = null;
  let createCalls = 0;
  const requestKeys: string[] = [];

  await page.route("**/api/workspaces/*/members", async (route) => {
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
    await page.goto(`/${workspaceSlug}/settings?tab=members`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "添加用户");
    await page.getByPlaceholder("账号").fill(account);
    await page.getByPlaceholder("姓名（可选）").fill(name);
    await page.getByPlaceholder("初始密码（新用户必填）").fill(password);
    await page.getByRole("button", { name: "添加", exact: true }).click();
    await expect(page.getByText(account)).toBeVisible({ timeout: 15_000 });
    await expect(page.getByText(name)).toBeVisible();

    expect(createCalls).toBe(2);
    expect(requestKeys[0]).toMatch(/^[0-9a-f-]{36}$/);
    expect(requestKeys[1]).toBe(requestKeys[0]);
    const created = (await api.listWorkspaceMembers(workspace.id))
      .filter((member) => member.account === account);
    expect(created).toHaveLength(1);
    memberId = created[0]!.id;
    expect(memberId).toBe(requestKeys[0]);

    const persistedBrowserState = await page.evaluate(() => JSON.stringify(localStorage));
    expect(persistedBrowserState).not.toContain(password);

    const memberApi = new TestApiClient();
    await memberApi.login(account, name, password);
    await expect(memberApi.getWorkspaces()).resolves.toEqual(
      expect.arrayContaining([expect.objectContaining({ id: workspace.id })]),
    );
    await memberApi.cleanup();
  } finally {
    if (!memberId) {
      memberId = (await api.listWorkspaceMembers(workspace.id).catch(() => []))
        .find((member) => member.account === account)?.id ?? null;
    }
    if (memberId) {
      await api.deleteWorkspaceMember(workspace.id, memberId).catch(() => undefined);
    }
    await api.cleanupWorkspaceMemberFixture(account, requestKeys[0]).catch(() => undefined);
    await api.cleanup();
  }
});
