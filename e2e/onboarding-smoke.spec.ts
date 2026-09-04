import { test, expect } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { waitForPageText } from "./helpers";

// Smoke test for the onboarding flow: welcome → About you (role +
// use case on ONE screen) → workspace → runtime. The source question
// is intentionally absent — it moved to the workspace source-backfill
// prompt (MUL-5159). Captures screenshots for review. Uses a unique
// email per run so the user is always a fresh, un-onboarded user
// landing on /onboarding.

const ACCOUNT = `onboarding-v3-${Date.now()}`;
const SHOTS_DIR = "../shots-rail";

test.skip(
  process.env.ALLOW_SIGNUP === "false",
  "当前部署关闭了新账号注册，因此不暴露新用户引导入口。",
);

test.use({ viewport: { width: 1440, height: 900 } });

test("onboarding — welcome → about you (answer path)", async ({ page }) => {
  const api = new TestApiClient();
  await api.login(ACCOUNT, "OBv3 Tester");
  const token = api.getToken();

  await page.addInitScript((t) => {
    localStorage.setItem("multica_token", t);
  }, token);
  await page.goto("/onboarding", { waitUntil: "domcontentloaded" });
  await waitForPageText(page, "开始探索");

  // 1. Welcome screen
  await expect(page.getByRole("button", { name: "开始探索" })).toBeVisible({ timeout: 15000 });
  await page.screenshot({ path: `${SHOTS_DIR}/01-welcome.png`, fullPage: false });

  // Click Start exploring to advance to About you
  await page.getByRole("button", { name: "开始探索" }).click();

  // 2. About you step — both questions live on this one screen and the
  //    source question must NOT exist anywhere in the flow.
  await expect(page.getByText("简单介绍一下你自己。")).toBeVisible({ timeout: 10000 });
  await expect(page.getByText("哪一项最符合你？")).toBeVisible();
  await expect(page.getByText("你打算用 Multica 做什么？")).toBeVisible();
  // The rail names every step and marks the current one; the ordinal
  // counter it replaced is gone.
  await expect(page.locator('[data-slot="stepper-title"]')).toHaveText([
    "关于你",
    "工作区",
    "认识 Mika",
  ]);
  await expect(
    page.locator('[aria-current="step"]').filter({ hasText: "关于你" }),
  ).toBeVisible();
  await expect(page.getByText("你是从哪里了解到 Multica 的？")).toHaveCount(0);
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${SHOTS_DIR}/02-about-you.png` });

  // Answer both groups, then Continue → workspace step.
  await page.getByRole("radio", { name: "工程师 / 开发者" }).click();
  await page.getByRole("checkbox", { name: "让 AI 智能体帮我写代码" }).click();
  await page.getByRole("button", { name: "继续" }).click();

  // 3. Workspace step
  await expect(page.getByRole("heading", { name: /给工作区起个名字/ })).toBeVisible({ timeout: 10000 });
  await expect(
    page.locator('[aria-current="step"]').filter({ hasText: "工作区" }),
  ).toBeVisible();
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${SHOTS_DIR}/03-workspace.png` });

  // 4. Runtime step — the rail should now show two completed steps and mark
  //    "Meet Mika" current.
  await page.getByRole("textbox").first().fill(`Rail QA ${Date.now()}`);
  await page.getByRole("button", { name: /^创建/ }).click();
  await expect(
    page.locator('[aria-current="step"]').filter({ hasText: "认识 Mika" }),
  ).toBeVisible({ timeout: 20000 });
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${SHOTS_DIR}/06-runtime.png` });
});

test("onboarding — one skip clears the whole questionnaire step", async ({ page }) => {
  const api = new TestApiClient();
  await api.login(`skip-${Date.now()}`, "Skipper");
  const token = api.getToken();

  await page.addInitScript((t) => localStorage.setItem("multica_token", t), token);
  await page.goto("/onboarding", { waitUntil: "domcontentloaded" });
  await waitForPageText(page, "开始探索");

  await page.getByRole("button", { name: "开始探索" }).click();
  await expect(page.getByText("简单介绍一下你自己。")).toBeVisible({ timeout: 10000 });

  // A single Skip covers role + use case — next stop is workspace.
  await page.getByRole("button", { name: "跳过" }).click();
  await expect(page.getByRole("heading", { name: /给工作区起个名字/ })).toBeVisible({ timeout: 10000 });
  await page.waitForTimeout(600);
  await page.screenshot({ path: `${SHOTS_DIR}/04-after-skip.png` });
});

test("onboarding — zh-Hans renders Chinese labels", async ({ page, context, baseURL }) => {
  await context.addCookies([
    {
      name: "multica-locale",
      value: "zh-Hans",
      url: baseURL ?? "http://localhost:3000",
    },
  ]);
  const api = new TestApiClient();
  await api.login(`zh-${Date.now()}`, "中文用户");
  const token = api.getToken();

  await page.addInitScript((t) => localStorage.setItem("multica_token", t), token);
  await page.goto("/onboarding", { waitUntil: "domcontentloaded" });
  await waitForPageText(page, "开始探索");

  // Click the CTA by name. `getByRole("button").first()` used to stand in for
  // it, but the welcome screen renders the pinned Log out button first in DOM
  // order — so this step was signing the user out and the assertions below
  // were waiting on a page that had already redirected to login.
  await page.getByRole("button", { name: "开始探索" }).click();

  // About-you screen — Chinese headline + both sub-questions.
  await expect(page.getByText("简单介绍一下你自己。")).toBeVisible({ timeout: 10000 });
  await expect(page.getByText("哪一项最符合你？")).toBeVisible();
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${SHOTS_DIR}/05-about-you-zh.png` });
});
