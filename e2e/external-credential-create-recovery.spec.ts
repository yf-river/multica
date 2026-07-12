import { expect, test } from "@playwright/test";
import { createTestApi, loginAsDefault, waitForPageText } from "./helpers";

test("credential creation recovers after the committed response is lost", async ({ page }) => {
  const api = await createTestApi();
  const workspaceSlug = await loginAsDefault(page);
  const provider = "tapd" as const;
  const profileName = "tapd-default";
  let createCalls = 0;
  const requestKeys: string[] = [];

  const removeFixtureProfiles = async () => {
    const profiles = await api.listExternalCredentialProfiles(provider);
    for (const profile of profiles.filter((item) => item.name === profileName)) {
      await api.deleteExternalCredentialProfile(profile.id);
    }
  };

  await page.route("**/api/external-credential-profiles", async (route) => {
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
    await removeFixtureProfiles();
    await page.goto(`/${workspaceSlug}/settings?tab=tokens`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "TAPD 访问凭据");

    const panel = page.getByTestId("settings-tapd-credential-panel");
    await panel.getByRole("button", { name: "环境变量" }).click();
    await panel.getByRole("button", { name: "保存凭据", exact: true }).click();
    await expect(panel.getByText(/校验失败 · TAPD_ACCESS_TOKEN/)).toBeVisible({ timeout: 15_000 });
    await expect(panel.getByText("环境变量", { exact: true }).last()).toBeVisible();
    await expect(panel.getByText(/服务器环境变量 TAPD_ACCESS_TOKEN 未设置/)).toBeVisible();

    expect(createCalls).toBe(2);
    expect(requestKeys[0]).toMatch(/^[0-9a-f-]{36}$/);
    expect(requestKeys[1]).toBe(requestKeys[0]);

    const profiles = (await api.listExternalCredentialProfiles(provider))
      .filter((item) => item.name === profileName);
    expect(profiles).toHaveLength(1);
    expect(profiles[0]?.id).toBe(requestKeys[0]);
    expect(profiles[0]?.secret_binding).toMatchObject({
      configured: true,
      mode: "secret_ref",
    });
    expect(JSON.stringify(profiles[0])).not.toContain("TAPD_ACCESS_TOKEN=");
  } finally {
    await removeFixtureProfiles().catch(() => undefined);
    await api.cleanup();
  }
});
