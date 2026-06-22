import type { NextConfig } from "next";
import { config } from "dotenv";
import { resolve } from "path";
import { resolveRemoteApiUrl } from "./config/runtime-urls";
import { createMDX } from "fumadocs-mdx/next";

// Load root .env so REMOTE_API_URL is available to next.config.ts
config({ path: resolve(__dirname, "../../.env") });

const remoteApiUrl = resolveRemoteApiUrl(process.env);
const docsUrl = process.env.DOCS_URL || "http://localhost:4000";

function parseDevOriginHosts(origin: string): string[] {
  const trimmed = origin.trim();
  try {
    const url = new URL(trimmed);
    return Array.from(new Set([url.host, url.hostname].filter(Boolean)));
  } catch {
    return trimmed ? [trimmed] : [];
  }
}

// Parse hostnames from CORS_ALLOWED_ORIGINS so that Next.js dev server
// allows cross-origin HMR / webpack requests (e.g. from Tailscale IPs).
// Also include loopback hosts for the configured dev port. Worktree-specific
// E2E often opens 127.0.0.1 while Next advertises localhost, and Next 16 blocks
// that unless it is explicitly allowlisted.
const frontendPort = process.env.FRONTEND_PORT?.trim() || "3000";
const allowedDevOrigins = Array.from(
  new Set(
    [
      ...(process.env.CORS_ALLOWED_ORIGINS ?? "")
        .split(",")
        .flatMap((origin) => parseDevOriginHosts(origin))
        .filter(Boolean),
      "localhost",
      "127.0.0.1",
      `localhost:${frontendPort}`,
      `127.0.0.1:${frontendPort}`,
    ].filter(Boolean),
  ),
);

const nextConfig: NextConfig = {
  ...(process.env.STANDALONE === "true" ? { output: "standalone" as const } : {}),
  transpilePackages: ["@multica/core", "@multica/ui", "@multica/views"],
  allowedDevOrigins,
  images: {
    formats: ["image/avif", "image/webp"],
    qualities: [75, 80, 85],
  },
  async rewrites() {
    return {
      // Run before file-system routes so /docs isn't shadowed by the
      // [workspaceSlug] dynamic segment.
      beforeFiles: [
        {
          source: "/docs",
          destination: `${docsUrl}/docs`,
        },
        {
          source: "/docs/:path*",
          destination: `${docsUrl}/docs/:path*`,
        },
      ],
      afterFiles: [
        {
          source: "/api/:path*",
          destination: `${remoteApiUrl}/api/:path*`,
        },
        {
          source: "/ws",
          destination: `${remoteApiUrl}/ws`,
        },
        {
          source: "/auth/:path*",
          destination: `${remoteApiUrl}/auth/:path*`,
        },
        {
          source: "/uploads/:path*",
          destination: `${remoteApiUrl}/uploads/:path*`,
        },
      ],
      fallback: [],
    };
  },
};

// fumadocs-mdx@12 is incompatible with Next 16's Turbopack: its loader fails to
// dynamic-import `.source/source.config.mjs` under the Turbopack Node evaluator
// (see fumadocs#2658). `dev`/`build` scripts pass `--webpack` to opt out.
// Drop the flag once fumadocs-mdx ships a Turbopack-compatible loader.
const withMDX = createMDX() as (config: NextConfig) => NextConfig;

export default withMDX(nextConfig);
