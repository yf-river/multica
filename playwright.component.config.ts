import { defineConfig } from "@playwright/test";

// 组件浏览器验收只加载测试页面，不读取部署凭据或启动应用服务。
export default defineConfig({
  testDir: "./e2e",
  testMatch: ["shimmer-text.spec.ts"],
  timeout: 30_000,
  workers: 1,
  retries: 0,
  use: { headless: true },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
});
