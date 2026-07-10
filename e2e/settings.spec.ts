import { test, expect } from "@playwright/test";
import { createTestApi, loginAsDefault, waitForPageText } from "./helpers";

test.describe("Settings", () => {
  test("updating workspace name reflects in sidebar immediately", async ({
    page,
  }) => {
    const workspaceSlug = await loginAsDefault(page);
    const api = await createTestApi();
    const workspace = (await api.getWorkspaces()).find((item) => item.slug === workspaceSlug);
    if (!workspace) throw new Error(`workspace fixture not found: ${workspaceSlug}`);

    try {
      await page.goto(`/${workspaceSlug}/settings?tab=workspace`, { waitUntil: "domcontentloaded" });
      await waitForPageText(page, "通用");

      const nameInput = page.getByLabel("名称", { exact: true });
      await expect(nameInput).toHaveValue(workspace.name);
      await nameInput.clear();
      const newName = "Renamed WS " + Date.now();
      await nameInput.fill(newName);
      await page.getByRole("button", { name: "保存", exact: true }).click();

      await expect(page.getByText("已保存工作区设置").first()).toBeVisible({ timeout: 5000 });
      await expect(page.getByRole("button", { name: new RegExp(newName) }).first()).toBeVisible();

      await nameInput.clear();
      await nameInput.fill(workspace.name);
      await page.getByRole("button", { name: "保存", exact: true }).click();
      await expect(page.getByText("已保存工作区设置").first()).toBeVisible({ timeout: 5000 });
      await expect(page.getByRole("button", { name: new RegExp(workspace.name) }).first()).toBeVisible();
    } finally {
      await api.updateWorkspace(workspace.id, { name: workspace.name }).catch(() => undefined);
      await api.cleanup();
    }
  });

  test("members tab adds a user with account/password and shows it in the list", async ({
    page,
  }) => {
    const workspaceSlug = await loginAsDefault(page);
    const api = await createTestApi();
    const workspace = (await api.getWorkspaces()).find((item) => item.slug === workspaceSlug);
    if (!workspace) throw new Error(`workspace fixture not found: ${workspaceSlug}`);
    const account = "member_e2e_fixture";
    let createdMemberId: string | null = null;

    try {
      const stale = (await api.listWorkspaceMembers(workspace.id)).find((item) => item.account === account);
      if (stale) await api.deleteWorkspaceMember(workspace.id, stale.id);

      await page.goto(`/${workspaceSlug}/settings?tab=members`, { waitUntil: "domcontentloaded" });
      await waitForPageText(page, "添加用户");

      await page.getByPlaceholder("账号").fill(account);
      await page.getByPlaceholder("姓名（可选）").fill("新增成员");
      await page.getByPlaceholder("初始密码（新用户必填）").fill("MemberE2E1!");
      await page.getByRole("button", { name: "添加", exact: true }).click();

      await expect(page.getByText(account)).toBeVisible({ timeout: 10000 });
      await expect(page.getByText("新增成员")).toBeVisible();
      await expect(page.getByText("成员").last()).toBeVisible();
      createdMemberId = (await api.listWorkspaceMembers(workspace.id)).find((item) => item.account === account)?.id ?? null;
      expect(createdMemberId).not.toBeNull();
    } finally {
      if (!createdMemberId) {
        createdMemberId = (await api.listWorkspaceMembers(workspace.id).catch(() => []))
          .find((item) => item.account === account)?.id ?? null;
      }
      if (createdMemberId) {
        await api.deleteWorkspaceMember(workspace.id, createdMemberId).catch(() => undefined);
      }
      await api.cleanup();
    }
  });
});
