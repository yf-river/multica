/**
 * Web 应用的 Next.js 配置：跨包转译、图片优化参数、CORS 开发来源，
 * 以及「dev 用本地回退、生产保持严格」的 /docs 与后端上游改写规则。
 */
import type { NextConfig } from "next";
import { config } from "dotenv";
import { resolve } from "path";
import {
  resolveDevDocsUrl,
  resolveDevRemoteApiUrl,
  resolveDocsUrl,
  resolveRemoteApiUrl,
} from "./config/runtime-urls";
import { createMDX } from "fumadocs-mdx/next";

// 加载根 .env，让本地 next.config.ts 的改写能看到 REMOTE_API_URL / DOCS_URL。
// 生产请求改用 proxy.ts 的运行时改写——它读的是 Next.js 服务运行时
// 的 process.env，而不是把这些 URL 在构建期写死。
config({ path: resolve(__dirname, "../../.env") });

// `next dev` 回退到约定俗成的 localhost 上游；构建走严格解析器，
// 让预构建镜像保持「未配置的上游不做代理」。
const isDev = process.env.NODE_ENV === "development";
const remoteApiUrl = isDev
  ? resolveDevRemoteApiUrl(process.env)
  : resolveRemoteApiUrl(process.env);
const docsUrl = isDev
  ? resolveDevDocsUrl(process.env)
  : resolveDocsUrl(process.env);

// 从 CORS_ALLOWED_ORIGINS 解析出 hostname，让 Next.js dev server 允许跨源
// HMR / webpack 请求（例如来自 Tailscale IP）。
const allowedDevOrigins = process.env.CORS_ALLOWED_ORIGINS
  ? process.env.CORS_ALLOWED_ORIGINS.split(",")
      .map((origin) => {
        try {
          return new URL(origin.trim()).hostname;
        } catch {
          return origin.trim();
        }
      })
      .filter(Boolean)
  : undefined;

const nextConfig: NextConfig = {
  agentRules: false,
  outputFileTracingRoot: resolve(__dirname, "../.."),
  ...(process.env.STANDALONE === "true" ? { output: "standalone" as const } : {}),
  transpilePackages: ["@multica/core", "@multica/ui", "@multica/views"],
  experimental: {
    // 工作区业务数据由 React Query 和 WebSocket 更新；短期复用页面路由避免菜单切换重复往返。
    staleTimes: { dynamic: 30 },
  },
  onDemandEntries: {
    maxInactiveAge: 10 * 60 * 1000,
    pagesBufferLength: 32,
  },
  ...(allowedDevOrigins && allowedDevOrigins.length > 0
    ? { allowedDevOrigins }
    : {}),
  images: {
    formats: ["image/avif", "image/webp"],
    qualities: [75, 80, 85],
  },
  async rewrites() {
    return {
      // 先于文件系统路由执行，让 /docs 不被 [workspaceSlug] 动态段遮蔽。
      beforeFiles: docsUrl
        ? [
            {
              source: "/docs",
              destination: `${docsUrl}/docs`,
            },
            {
              source: "/docs/:path*",
              destination: `${docsUrl}/docs/:path*`,
            },
          ]
        : [],
      afterFiles: remoteApiUrl
        ? [
            {
              source: "/v1/:path*",
              destination: `${remoteApiUrl}/v1/:path*`,
            },
            {
              source: "/api/:path*",
              destination: `${remoteApiUrl}/api/:path*`,
            },
            {
              source: "/ws",
              destination: `${remoteApiUrl}/ws`,
            },
            {
              source: "/health",
              destination: `${remoteApiUrl}/health`,
            },
            {
              source: "/auth/:path*",
              destination: `${remoteApiUrl}/auth/:path*`,
            },
            {
              source: "/uploads/:path*",
              destination: `${remoteApiUrl}/uploads/:path*`,
            },
          ]
        : [],
      fallback: [],
    };
  },
};

// fumadocs-mdx@12 与 Next 16 的 Turbopack 不兼容：它的 loader 在 Turbopack 的
// Node 求值器下无法动态 import `.source/source.config.mjs`（见 fumadocs#2658）。
// `dev`/`build` 脚本都传了 `--webpack` 来绕开。
// 等 fumadocs-mdx 发布兼容 Turbopack 的 loader 后可去掉该参数。
const withMDX = createMDX() as (config: NextConfig) => NextConfig;

export default withMDX(nextConfig);
