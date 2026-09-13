/** 官网「联系销售」路由（服务端渲染壳，内容交给 ContactSalesPageClient）。 */
import type { Metadata } from "next";
import { ContactSalesPageClient } from "@/features/landing/components/contact-sales-page-client";

/** 联系销售页 SEO 元信息。 */
export const metadata: Metadata = {
  title: "Contact Sales",
  description:
    "Talk to the Multica team about rolling out human + agent workflows at your company.",
  openGraph: {
    title: "Contact Sales — Multica",
    description:
      "Tell us about your team. We’ll respond within three business days.",
    url: "/contact-sales",
  },
  alternates: {
    canonical: "/contact-sales",
  },
};

/**
 * 联系销售页默认导出。
 *
 * @returns 联系销售整页。
 */
export default function ContactSalesPage() {
  return <ContactSalesPageClient />;
}
