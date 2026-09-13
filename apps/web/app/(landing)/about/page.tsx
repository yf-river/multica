/** 官网「关于」页路由（服务端渲染壳，内容交给 AboutPageClient）。 */
import type { Metadata } from "next";
import { AboutPageClient } from "@/features/landing/components/about-page-client";

/** 关于页 SEO 元信息。 */
export const metadata: Metadata = {
  title: "About",
  description:
    "Learn about Multica — multiplexed information and computing agent. An open-source project management platform for human + agent teams.",
  openGraph: {
    title: "About Multica",
    description:
      "The story behind Multica and why we're building project management for human + agent teams.",
    url: "/about",
  },
  alternates: {
    canonical: "/about",
  },
};

/**
 * 关于页默认导出。
 *
 * @returns 关于页整页。
 */
export default function AboutPage() {
  return <AboutPageClient />;
}
