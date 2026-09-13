/** 官网「更新日志」路由（服务端渲染壳，内容交给 ChangelogPageClient）。 */
import type { Metadata } from "next";
import { ChangelogPageClient } from "@/features/landing/components/changelog-page-client";

/** 更新日志页 SEO 元信息。 */
export const metadata: Metadata = {
  title: "Changelog",
  description:
    "See what's new in Multica — latest features, improvements, and fixes.",
  openGraph: {
    title: "Changelog | Multica",
    description: "Latest updates and releases from Multica.",
    url: "/changelog",
  },
  alternates: {
    canonical: "/changelog",
  },
};

/**
 * 更新日志页默认导出。
 *
 * @returns 更新日志整页。
 */
export default function ChangelogPage() {
  return <ChangelogPageClient />;
}
