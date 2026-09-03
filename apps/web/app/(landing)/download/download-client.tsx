"use client";

import { Terminal } from "lucide-react";
import { LandingHeader } from "@/features/landing/components/landing-header";
import { LandingFooter } from "@/features/landing/components/landing-footer";
import { CliSection } from "@/features/landing/components/download/cli-section";
import { CloudSection } from "@/features/landing/components/download/cloud-section";
import { useLocale } from "@/features/landing/i18n";

export function DownloadClient() {
  const { t } = useLocale();

  return (
    <>
      <div className="relative">
        <LandingHeader variant="dark" />
        <section className="relative overflow-hidden bg-[#05070b] text-white">
          <div
            aria-hidden
            className="pointer-events-none absolute inset-0 bg-[radial-gradient(ellipse_70%_50%_at_50%_0%,rgba(80,120,255,0.18),transparent_60%)]"
          />
          <div className="relative z-10 mx-auto max-w-[920px] px-4 pb-24 pt-32 text-center sm:px-6 sm:pt-40 lg:px-8 lg:pb-28">
            <Terminal className="mx-auto size-8 text-white/70" aria-hidden />
            <h1 className="mx-auto mt-6 max-w-[780px] landing-serif text-[3rem] leading-[1.02] tracking-[-0.035em] sm:text-[4rem] lg:text-[5rem]">
              {t.download.cli.title}
            </h1>
            <p className="mx-auto mt-6 max-w-[620px] text-body-lg leading-7 text-white/84 sm:text-title">
              {t.download.cli.sub}
            </p>
          </div>
        </section>
      </div>
      <CliSection />
      <CloudSection />
      <LandingFooter />
    </>
  );
}
