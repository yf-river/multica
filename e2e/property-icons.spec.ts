import { expect, test } from "@playwright/test";
import { loginAsDefault, waitForPageText } from "./helpers";

test("creates, persists, and clears a custom property icon", async ({ page }) => {
  const workspaceSlug = await loginAsDefault(page);
  const propertyName = `Icon ${Date.now().toString(36)}`;

  await page.goto(`/${workspaceSlug}/settings?tab=properties`, {
    waitUntil: "domcontentloaded",
  });
  await waitForPageText(page, "属性");
  await page.getByRole("button", { name: "新建属性" }).click();
  const createDialog = page.getByRole("dialog", { name: "新建属性" });
  await expect(createDialog).toBeVisible();

  await createDialog.getByRole("button", { name: "选择图标" }).click();
  await page.getByRole("button", { name: "Flag", exact: true }).click();
  await createDialog.getByLabel("名称").fill(propertyName);
  await createDialog.getByPlaceholder("选项名称").fill("Critical");
  await createDialog.getByRole("button", { name: "保存属性" }).click();

  await expect(createDialog).not.toBeVisible();
  const propertyNameCell = page.getByText(propertyName, { exact: true }).locator("..");
  await expect(propertyNameCell).toBeVisible();
  await expect(propertyNameCell.locator('[data-property-icon="flag"]')).toBeVisible();

  await page.getByRole("button", { name: `${propertyName} 的操作` }).click();
  await page.getByRole("menuitem", { name: "编辑" }).click();
  const editDialog = page.getByRole("dialog", { name: "编辑属性" });
  await expect(editDialog).toBeVisible();
  await expect(
    editDialog.locator('[data-property-icon="flag"]'),
  ).toBeVisible();

  await editDialog.getByRole("button", { name: "选择图标" }).click();
  await page.getByRole("button", { name: "移除图标" }).click();
  await editDialog.getByRole("button", { name: "保存属性" }).click();
  await expect(editDialog).not.toBeVisible();
  await expect(propertyNameCell.locator("[data-property-icon]")).toHaveCount(0);
});
