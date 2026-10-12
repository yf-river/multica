// 同一个浏览器上下文交替使用两个部署，验证 Cookie 与工作区状态互不覆盖。
import { createRequire } from "node:module";
import fs from "node:fs";

const require = createRequire(process.cwd() + "/package.json");
const { chromium } = require("@playwright/test");
const account = process.env.MULTICA_TEST_ACCOUNT;
const password = process.env.MULTICA_TEST_PASSWORD;
if (!account || !password) {
  throw new Error("Set MULTICA_TEST_ACCOUNT and MULTICA_TEST_PASSWORD");
}
const urls = Object.fromEntries(["int", "prod"].map((env) => [
  env, JSON.parse(fs.readFileSync(`.run/deployments/goal-test-${env}.json`)).frontend_url,
]));
const directory = process.cwd() + "/.run/evidence/environment-isolation";
fs.mkdirSync(directory, { recursive: true });
const browser = await chromium.launch({ headless: true, args: ["--no-proxy-server"] });
const context = await browser.newContext();
await context.addInitScript(() => localStorage.setItem("multica:chat:isOpen", "false"));
const pages = {};
const results = [];
const insideApplication = (url) => !["/login", "/"].includes(url.pathname);

async function getConfig(env) {
  const response = await context.request.get(urls[env] + "/api/config");
  if (!response.ok()) throw new Error(`${env} config: ${response.status()}`);
  return response.json();
}

async function csrfToken(env, config) {
  const name = "multica_csrf" + (config.environment_id ? "_" + config.environment_id : "");
  return (await context.cookies(urls[env])).find((cookie) => cookie.name === name)?.value || "";
}

async function login(env) {
  const page = pages[env] ??= await context.newPage();
  await page.goto(urls[env] + "/login");
  await Promise.race([
    page.waitForURL(insideApplication, { timeout: 30_000 }),
    page.locator("#login-account").waitFor({ state: "visible", timeout: 30_000 }),
  ]);
  if (await page.locator("#login-account").isVisible()) {
    await page.locator("#login-account").fill(account);
    await page.locator("#login-password").fill(password);
    await page.locator("#login-form").evaluate((form) => form.requestSubmit());
  }
  await page.waitForURL(insideApplication, { timeout: 30_000 });
  const response = await context.request.get(urls[env] + "/api/workspaces");
  const body = await response.json();
  const workspaces = Array.isArray(body) ? body : body.items;
  if (!workspaces?.length) throw new Error(`${env} requires an existing workspace`);
  await page.goto(`${urls[env]}/${workspaces[0].slug}/issues`);
}

async function check(step, env) {
  const page = pages[env];
  await page.reload();
  await page.waitForURL(insideApplication, { timeout: 30_000 });
  const me = await context.request.get(urls[env] + "/api/me");
  if (me.status() !== 200) throw new Error(`${step} ${env}: ${me.status()}`);
  const config = await getConfig(env);
  await page.getByText(config.environment_name + "环境", { exact: true }).first()
    .waitFor({ state: "visible", timeout: 30_000 });
  await page.waitForFunction((label) => document.title.includes(label), config.environment_name);
  const user = await me.json();
  // 使用原名称验证受 CSRF 保护的写请求，不改变用户资料内容。
  const write = await context.request.patch(urls[env] + "/api/me", {
    headers: { "X-CSRF-Token": await csrfToken(env, config) },
    data: { name: user.name },
  });
  if (write.status() !== 200) throw new Error(`${env} CSRF write: ${write.status()}`);
  results.push({ step, env, me: me.status(), csrfWrite: write.status(),
    title: await page.title(), environment: config.environment_id || "legacy" });
}

try {
  await login("int");
  await login("prod");
  await check("both logged in", "int");
  await check("both logged in", "prod");
  for (const env of ["int", "prod"]) {
    const config = await getConfig(env);
    const logout = await context.request.post(urls[env] + "/auth/logout", {
      headers: { "X-CSRF-Token": await csrfToken(env, config) },
    });
    if (logout.status() !== 200) throw new Error(`${env} logout: ${logout.status()}`);
    await check(env + " logout does not affect peer", env === "int" ? "prod" : "int");
    await login(env);
    await check(env + " relogin", env);
  }
  for (const env of ["int", "prod"]) {
    await pages[env].screenshot({ path: `${directory}/${env}-isolated.png` });
  }
  const cookies = (await context.cookies()).map(({ name, domain, path }) => ({ name, domain, path }));
  fs.writeFileSync(`${directory}/browser.json`, JSON.stringify({ results, cookies }, null, 2));
  console.log(JSON.stringify(results));
} catch (error) {
  for (const [env, page] of Object.entries(pages)) {
    await page.screenshot({ path: `${directory}/${env}-failure.png` });
  }
  throw error;
} finally {
  await browser.close();
}
