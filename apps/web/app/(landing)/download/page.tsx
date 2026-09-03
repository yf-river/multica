import type { Metadata } from "next";
import { DownloadClient } from "./download-client";

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

export default function DownloadPage() {
  return <DownloadClient />;
}
