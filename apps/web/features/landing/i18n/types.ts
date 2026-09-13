/**
 * 官网 landing 的 i18n 类型定义：描述 zh.ts 字典的完整结构，并提供语言别名、
 * 语言判断与文档链接工具。当前仅实现了 zh-Hans。
 */
import type { SupportedLocale } from "@multica/core/i18n";
export { docsHrefForLocale } from "@/lib/docs-href";

/** 当前实际支持的语言（沿用 core 的语言联合类型，未来可扩展）。 */
export type Locale = SupportedLocale;

/** 是否中文环境：目前唯一语言就是中文，恒返回 true（保留参数以便未来扩展）。 */
export function isZhLocale(_locale: Locale): boolean {
  return true;
}

/** 「功能亮点」区块的结构：标签 + 标题 + 描述 + 三点卖点卡片。 */
type FeatureSection = {
  label: string;
  title: string;
  description: string;
  cards: { title: string; description: string }[];
};

/** 页脚链接组结构。 */
type FooterGroup = {
  label: string;
  links: { label: string; href: string }[];
};

/** 联系销售表单里的下拉选项（值为提交值，label 为展示文案）。 */
export type ContactSalesOption = { value: string; label: string };

/** 官网落地页全部文案的结构（header/hero/各区块/表单等）。 */
export type LandingDict = {
  header: {
    github: string;
    cta: string;
    dashboard: string;
    docs: string;
    changelog: string;
    useCases: string;
    navigation: string;
    openMenu: string;
    closeMenu: string;
  };
  hero: {
    headlineLine1: string;
    headlineLine2: string;
    subheading: string;
    cta: string;
    installCli: string;
    talkToSales: string;
    worksWith: string;
    imageAlt: string;
  };
  features: {
    teammates: FeatureSection;
    autonomous: FeatureSection;
    skills: FeatureSection;
    runtimes: FeatureSection;
  };
  howItWorks: {
    label: string;
    headlineMain: string;
    headlineFaded: string;
    steps: { title: string; description: string }[];
    cta: string;
    ctaGithub: string;
    ctaDocs: string;
  };
  openSource: {
    label: string;
    headlineLine1: string;
    headlineLine2: string;
    description: string;
    cta: string;
    highlights: { title: string; description: string }[];
  };
  faq: {
    label: string;
    headline: string;
    items: { question: string; answer: string }[];
  };
  footer: {
    tagline: string;
    cta: string;
    groups: {
      product: FooterGroup;
      resources: FooterGroup;
      company: FooterGroup;
    };
    copyright: string;
  };
  about: {
    title: string;
    nameLine: {
      prefix: string;
      mul: string;
      tiplexed: string;
      i: string;
      nformationAnd: string;
      c: string;
      omputing: string;
      a: string;
      gent: string;
    };
    paragraphs: string[];
    cta: string;
  };
  changelog: {
    title: string;
    subtitle: string;
    toc: string;
    categories: {
      features: string;
      improvements: string;
      fixes: string;
    };
    entries: {
      version: string;
      date: string;
      title: string;
      changes: string[];
      features?: string[];
      improvements?: string[];
      fixes?: string[];
    }[];
  };
  download: {
    cli: {
      title: string;
      sub: string;
      installLabel: string;
      startLabel: string;
      sshNote: string;
      copyLabel: string;
      copiedLabel: string;
    };
    cloud: { title: string; sub: string };
  };
  contactSales: {
    pageTitle: string;
    pageDescription: string;
    eyebrow: string;
    title: string;
    subtitle: string;
    notice: { badge: string; body: string };
    fields: {
      firstName: string;
      lastName: string;
      businessEmail: string;
      businessEmailHint: string;
      companyName: string;
      companySize: string;
      countryRegion: string;
      useCase: string;
      goals: string;
      goalsHint: string;
      selectPlaceholder: string;
      submit: string;
      submitting: string;
    };
    companySizes: ContactSalesOption[];
    useCases: ContactSalesOption[];
    countries: string[];
    consent: {
      intro: string;
      outreach: string;
      updates: string;
      unsubscribe: string;
      submitConsent: string;
      privacyLinkLabel: string;
      privacyLinkHref: string;
    };
    success: { title: string; message: string; cta: string };
    errors: {
      generic: string;
      rateLimit: string;
      freeEmail: string;
      invalidEmail: string;
    };
  };
};
