import { test, expect } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { waitForPageText } from "./helpers";

// Smoke test for Onboarding V2: verifies the per-question flow and captures
// screenshots for review. Uses a unique account per run so the user is always
// fresh and lands on /onboarding.

const ACCOUNT = `onboarding_v2_${Date.now()}`;
const SHOTS_DIR = "/tmp/onboarding-v2-shots";

test.use({ viewport: { width: 1440, height: 900 } });

test("onboarding v2 — welcome → source → role → use_case (skip path)", async ({ page }) => {
  const api = new TestApiClient();
  await api.login(ACCOUNT, "OBv2 Tester");
  const token = api.getToken();

  await page.addInitScript((t) => {
    localStorage.setItem("multica_token", t);
  }, token);
  await page.goto("/onboarding", { waitUntil: "domcontentloaded" });
  await waitForPageText(page, "在 web 端继续");

  // 1. Welcome screen
  await expect(page.getByRole("button", { name: "在 web 端继续" })).toBeVisible({ timeout: 15000 });
  await page.screenshot({ path: `${SHOTS_DIR}/01-welcome.png`, fullPage: false });

  // Click continue to advance to Source.
  await page.getByRole("button", { name: "在 web 端继续" }).click();

  // 2. Source step
  await expect(page.getByText("你是从哪里了解到 Multica 的？")).toBeVisible({ timeout: 10000 });
  await expect(page.getByText(/第 1 步 \/ 共 \d+ 步/)).toBeVisible();
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${SHOTS_DIR}/02-source.png` });

  // Pick friends/colleagues then click Continue to advance.
  await page.getByRole("radio", { name: "朋友或同事" }).click();
  await page.getByRole("button", { name: "继续" }).click();

  // 3. Role step
  await expect(page.getByText("你是什么角色？")).toBeVisible({ timeout: 10000 });
  await expect(page.getByText(/第 2 步 \/ 共 \d+ 步/)).toBeVisible();
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${SHOTS_DIR}/03-role.png` });

  // Skip role
  await page.getByRole("button", { name: "跳过" }).click();

  // 4. Use case step
  await expect(page.getByText("你打算用 Multica 做什么？")).toBeVisible({ timeout: 10000 });
  await expect(page.getByText(/第 3 步 \/ 共 \d+ 步/)).toBeVisible();
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${SHOTS_DIR}/04-use-case.png` });

  // Pick ship_code then Continue -> workspace step.
  await page.getByRole("checkbox", { name: "让 AI agent 帮我写代码" }).click();
  await page.getByRole("button", { name: "继续" }).click();

  // 5. Workspace step (legacy)
  await expect(page.getByRole("heading", { name: /给工作区起个名字/ })).toBeVisible({ timeout: 10000 });
  await page.screenshot({ path: `${SHOTS_DIR}/05-workspace.png` });
});

test("onboarding v2 — rage-skip all 3 questions", async ({ page }) => {
  const api = new TestApiClient();
  await api.login(`rage_skip_${Date.now()}`, "Rage Skipper");
  const token = api.getToken();

  await page.addInitScript((t) => localStorage.setItem("multica_token", t), token);
  await page.goto("/onboarding", { waitUntil: "domcontentloaded" });
  await waitForPageText(page, "在 web 端继续");

  await page.getByRole("button", { name: "在 web 端继续" }).click();
  await expect(page.getByText("你是从哪里了解到 Multica 的？")).toBeVisible({ timeout: 10000 });

  // Skip x 3.
  await page.getByRole("button", { name: "跳过" }).click();
  await expect(page.getByText("你是什么角色？")).toBeVisible({ timeout: 10000 });
  await page.getByRole("button", { name: "跳过" }).click();
  await expect(page.getByText("你打算用 Multica 做什么？")).toBeVisible({ timeout: 10000 });
  await page.getByRole("button", { name: "跳过" }).click();

  // Lands on workspace step
  await expect(page.getByRole("heading", { name: /给工作区起个名字/ })).toBeVisible({ timeout: 10000 });
  await page.screenshot({ path: `${SHOTS_DIR}/06-after-rage-skip.png` });
});

test("onboarding v2 — zh-Hans renders Chinese labels", async ({ page, context }) => {
  await context.addCookies([
    { name: "multica-locale", value: "zh-Hans", url: "http://localhost:13442" },
  ]);
  const api = new TestApiClient();
  await api.login(`zh_${Date.now()}`, "中文用户");
  const token = api.getToken();

  await page.addInitScript((t) => localStorage.setItem("multica_token", t), token);
  await page.goto("/onboarding", { waitUntil: "domcontentloaded" });
  await waitForPageText(page, "在 web 端继续");

  await page.getByRole("button").first().click().catch(() => {});

  // Source screen — Chinese question
  await expect(page.getByText("你是从哪里了解到 Multica 的？")).toBeVisible({ timeout: 10000 });
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${SHOTS_DIR}/07-source-zh.png` });
});
