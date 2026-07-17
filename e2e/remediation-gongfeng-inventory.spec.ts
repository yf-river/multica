import { expect, test } from "@playwright/test";
import { loginAsDefault } from "./helpers";

test("settings repositories page opens the current Gongfeng inventory flow", async ({ page }) => {
  const workspaceSlug = await loginAsDefault(page);
  await page.goto(`/${workspaceSlug}/settings?tab=repositories`, { waitUntil: "domcontentloaded" });

  const inventory = page.getByTestId("settings-gongfeng-repository-inventory");
  await expect(inventory).toBeVisible({ timeout: 30000 });
  await expect(page.getByRole("heading", { name: "代码仓库" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "工蜂仓库资源库" })).toBeVisible();
  await inventory.getByRole("button", { name: "添加仓库" }).click();
  await expect(page.getByRole("dialog", { name: "添加工蜂仓库" })).toBeVisible();
  await expect(page.getByLabel("默认分支")).toBeVisible();
});
