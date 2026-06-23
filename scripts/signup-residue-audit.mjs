import { readFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const checks = [
  {
    path: "apps/web/app/layout.tsx",
    forbidden: [
      "Project Management for Human + Agent Teams",
      "Open-source platform that turns coding agents into real teammates",
      "twitter:site",
    ],
  },
  {
    path: "packages/core/analytics/index.ts",
    forbidden: ["multica_signup_source", "captureSignupSource", "utm_source", "acquisition funnel"],
  },
  {
    path: "server/internal/handler/auth.go",
    forbidden: ["signupSourceFromRequest", "multica_signup_source", "signup_source", "utm_source"],
  },
  {
    path: "server/internal/analytics/events.go",
    forbidden: ["signupSource", "signup_source", "UTM", "referrer"],
  },
  {
    path: "server/internal/metrics/labels_pr3.go",
    forbidden: ["NormalizeSignupSource", "knownSignupSources", "signup_source", "newsletter"],
  },
  {
    path: "docs/analytics.md",
    forbidden: ["multica_signup_source", "signup_source", "utm_source", "newsletter source"],
  },
  {
    path: "packages/views/onboarding/source-backfill-modal.tsx",
    forbidden: ["source_backfill", "How did you hear", "friends_colleagues", "blog_newsletter", "Newsletter"],
  },
  {
    path: "packages/views/onboarding/steps/step-source.tsx",
    forbidden: ["StepSource", "How did you hear", "friends_colleagues", "blog_newsletter"],
    allowMissing: true,
  },
];

const failures = [];
for (const check of checks) {
  const file = path.join(repoRoot, check.path);
  let content = "";
  try {
    content = readFileSync(file, "utf8");
  } catch (error) {
    if (check.allowMissing) continue;
    failures.push({ path: check.path, reason: error.message });
    continue;
  }
  for (const term of check.forbidden) {
    if (content.includes(term)) failures.push({ path: check.path, forbidden: term });
  }
}

const evidence = {
  schema: "multica.signup_residue_audit.v1",
  status: failures.length === 0 ? "通过" : "失败",
  scope: "当前部署会生效的中文 SEO、来源采集弹窗、signup_source 归因 cookie、服务端 signup_source 指标/文档",
  failures,
};

console.log(JSON.stringify(evidence, null, 2));
if (failures.length > 0) process.exit(1);
