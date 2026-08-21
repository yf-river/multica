import { test, expect } from "@playwright/test";
import { loginAsDefault, waitForPageText } from "./helpers";

test.describe("Settings", () => {
  test("updating workspace name reflects in sidebar immediately", async ({
    page,
  }) => {
    const workspaceSlug = await loginAsDefault(page);

    // Read the current workspace name from the sidebar
    const sidebarName = page.getByRole("button", { name: /E2E Workspace/ }).first();
    const originalName = (await sidebarName.innerText()).split("\n").pop()?.trim() ?? "E2E Workspace";

    await page.goto(`/${workspaceSlug}/settings?tab=workspace`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "通用");

    // Change workspace name
    const nameInput = page
      .locator('input[type="text"]')
      .first();
    await nameInput.clear();
    const newName = "Renamed WS " + Date.now();
    await nameInput.fill(newName);

    // Save
    await page.locator("button", { hasText: "保存" }).click();

    await expect(page.getByText("已保存工作区设置").first()).toBeVisible({ timeout: 5000 });

    // Sidebar should reflect the new name WITHOUT page refresh
    await expect(page.getByRole("button", { name: new RegExp(newName) }).first()).toBeVisible();

    // Restore original name so other tests aren't affected
    await nameInput.clear();
    await nameInput.fill(originalName.trim());
    await page.locator("button", { hasText: "保存" }).click();
    await expect(page.getByText("已保存工作区设置").first()).toBeVisible({ timeout: 5000 });
    await expect(page.getByRole("button", { name: new RegExp(originalName) }).first()).toBeVisible();
  });

  test("members tab adds a user with account/password and shows it in the list", async ({
    page,
  }) => {
    const workspaceSlug = await loginAsDefault(page);
    const account = `member_${Date.now()}`;

    await page.goto(`/${workspaceSlug}/settings?tab=members`, { waitUntil: "domcontentloaded" });
    await waitForPageText(page, "添加用户");

    await page.getByPlaceholder("账号").fill(account);
    await page.getByPlaceholder("姓名（可选）").fill("新增成员");
    await page.getByPlaceholder("初始密码（新用户必填）").fill("member-password");
    await page.getByRole("button", { name: "添加" }).click();

    await expect(page.getByText(account)).toBeVisible({ timeout: 10000 });
    await expect(page.getByText("新增成员")).toBeVisible();
    await expect(page.getByText("成员").last()).toBeVisible();
  });
});
