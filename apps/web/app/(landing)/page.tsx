/**
 * 官网根路径（`/`）页面（营销首页）：对已登录访客先做客户端兜底重定向
 * （见 RedirectIfAuthenticated），否则渲染完整 landing 内容。
 */
import type { Metadata } from "next";
import { MulticaLanding } from "@/features/landing/components/multica-landing";
import { RedirectIfAuthenticated } from "@/features/landing/components/redirect-if-authenticated";

/** 首页 SEO 元信息。 */
export const metadata: Metadata = {
  title: {
    absolute: "Multica — Project Management for Human + Agent Teams",
  },
  description:
    "Open-source platform that turns coding agents into real teammates. Assign tasks, track progress, compound skills.",
  openGraph: {
    title: "Multica — Project Management for Human + Agent Teams",
    description:
      "Manage your human + agent workforce in one place.",
    url: "/",
  },
  alternates: {
    canonical: "/",
  },
};

/**
 * 首页默认导出。
 *
 * @returns 登录态重定向（如需要）+ 营销首页内容。
 */
export default function LandingPage() {
  return (
    <>
      <RedirectIfAuthenticated />
      <MulticaLanding />
    </>
  );
}
