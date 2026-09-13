"use client";

/**
 * 官网落地页主组件：按顺序拼装 Header/Hero（共用同一深色容器）、
 * 功能亮点、工作原理、开源、FAQ 与页脚各区块。
 */
import { LandingHeader } from "./landing-header";
import { LandingHero } from "./landing-hero";
import { FeaturesSection } from "./features-section";
import { HowItWorksSection } from "./how-it-works-section";
import { OpenSourceSection } from "./open-source-section";
import { FAQSection } from "./faq-section";
import { LandingFooter } from "./landing-footer";

/**
 * 官网落地页（对已登录用户通常会被重定向，见 RedirectIfAuthenticated）。
 *
 * @returns 首页所有区块的组合。
 */
export function MulticaLanding() {
  return (
    <>
      <div className="relative">
        <LandingHeader />
        <LandingHero />
      </div>

      <FeaturesSection />
      <HowItWorksSection />
      <OpenSourceSection />
      <FAQSection />
      <LandingFooter />
    </>
  );
}
