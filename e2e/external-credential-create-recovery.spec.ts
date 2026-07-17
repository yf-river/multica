import { expect, test } from "@playwright/test";
import {
  createTestApi,
  expectCommittedResponseRecovery,
  interceptCommittedResponseLoss,
  loginAsDefault,
  waitForPageText,
} from "./helpers";

test("credential creation recovers after the committed response is lost", async ({ page }) => {
  const api = await createTestApi();
  const workspaceSlug = await loginAsDefault(page);
  const provider = "tapd" as const;
  const profileName = "tapd-default";
  const recovery = await interceptCommittedResponseLoss(page, "**/api/external-credential-profiles", 201);

  const removeFixtureProfiles = async () => {
    const profiles = await api.listExternalCredentialProfiles(provider);
    for (const profile of profiles.filter((item) => item.name === profileName)) {
      await api.deleteExternalCredentialProfile(profile.id);
    }
  };

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

    expectCommittedResponseRecovery(recovery);

    const profiles = (await api.listExternalCredentialProfiles(provider))
      .filter((item) => item.name === profileName);
    expect(profiles).toHaveLength(1);
    expect(profiles[0]?.id).toBe(recovery.requestKeys[0]);
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
