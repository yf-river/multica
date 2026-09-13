/** `/homepage` 预览路由：营销首页的独立可访问副本（用于分享/预览）。 */
import type { Metadata } from "next";
import { MulticaLanding } from "@/features/landing/components/multica-landing";

/** 本页 SEO 元信息。 */
export const metadata: Metadata = {
  title: "Homepage",
  description:
    "Multica — open-source platform that turns coding agents into real teammates. Assign tasks, track progress, compound skills.",
  openGraph: {
    title: "Multica — Project Management for Human + Agent Teams",
    description:
      "Manage your human + agent workforce in one place.",
    url: "/homepage",
  },
  alternates: {
    canonical: "/homepage",
  },
};

/**
 * Homepage 页默认导出。
 *
 * @returns 与根路径相同的营销首页。
 */
export default function HomepagePage() {
  return <MulticaLanding />;
}
