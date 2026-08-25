import { expect, test } from "@playwright/test";
import {
  authenticateBrowserSession,
  createTestApi,
  expectCommittedResponseRecovery,
  interceptCommittedResponseLoss,
  waitForPageText,
} from "./helpers";
import { TestApiClient } from "./fixtures";
import { E2E_FIXTURE_PASSWORD } from "./test-identity";

test("credential creation recovers after the committed response is lost", async ({ page }) => {
  const ownerApi = await createTestApi();
  const [workspace] = await ownerApi.getWorkspaces();
  if (!workspace) throw new Error("E2E workspace was not created");
  const account = "credential_recovery_user";
  const member = await ownerApi.createWorkspaceMember(workspace.id, {
    account,
    name: "凭据恢复用户",
    password: E2E_FIXTURE_PASSWORD,
  });
  const api = new TestApiClient();
  await api.login(account, "凭据恢复用户", E2E_FIXTURE_PASSWORD);
  const token = api.getToken();
  if (!token) throw new Error("credential recovery fixture login returned no token");
  await authenticateBrowserSession(page, token);
  const workspaceSlug = workspace.slug;
  const provider = "tapd" as const;
  const profileName = "tapd-default";
  const recovery = await interceptCommittedResponseLoss(page, "**/api/external-credential-profiles", 201);

  const removeFixtureProfiles = async () => {
    const profiles = await api.listExternalCredentialProfiles(provider);
    for (const profile of profiles) {
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
    await ownerApi.deleteWorkspaceMember(workspace.id, member.id).catch(() => undefined);
    await ownerApi.cleanup();
  }
});
