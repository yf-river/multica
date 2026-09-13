/** 官网「下载/安装」路由（服务端渲染壳，内容交给 DownloadClient）。 */
import type { Metadata } from "next";
import { DownloadClient } from "./download-client";

/** 下载页 SEO 元信息。 */
export const metadata: Metadata = {
  title: "Install Multica CLI",
  description:
    "Install the Multica CLI and connect an agent runtime from any supported machine.",
  openGraph: {
    title: "Install Multica CLI",
    description:
      "Install the Multica CLI and connect an agent runtime from any supported machine.",
    url: "/download",
  },
  alternates: {
    canonical: "/download",
  },
};

/**
 * 下载页默认导出。
 *
 * @returns 下载页整页。
 */
export default function DownloadPage() {
  return <DownloadClient />;
}
