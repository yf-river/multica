/**
 * 官网落地页（marketing）布局：加载展示字体、注入 JSON-LD 结构化数据（SEO），
 * 并用 LocaleProvider 给整个落地页树提供 i18n 字典。
 */
import localFont from "next/font/local";
import { LocaleProvider } from "@/features/landing/i18n";
import { getRequestLocale } from "@/lib/request-locale";

// Instrument Serif 是落地页展示字体且只含拉丁字符。完整 `--font-serif` 字体栈
// （Instrument Serif + 各语言 CJK 衬线字体尾巴）是在静态 CSS app/custom.css 里拼的，
// 而不是在这里——理由与 app/globals.css 里的 `--font-sans` 相同：CJK 尾巴必须能
// 按 `<html lang>` 覆盖，而带 hash 的 next/font 字体族只能通过 CSS 变量引用。
const instrumentSerif = localFont({
  src: "../../../../node_modules/@fontsource/instrument-serif/files/instrument-serif-latin-400-normal.woff2",
  display: "swap",
  weight: "400",
  variable: "--font-instrument-serif",
});

/** 首页的 JSON-LD 结构化数据：组织 + 软件应用（含 SEO 描述与价格）。 */
const jsonLd = {
  "@context": "https://schema.org",
  "@graph": [
    {
      "@type": "Organization",
      name: "Multica",
      url: "https://www.multica.ai",
      sameAs: ["https://github.com/multica-ai/multica"],
    },
    {
      "@type": "SoftwareApplication",
      name: "Multica",
      applicationCategory: "ProjectManagement",
      operatingSystem: "Web",
      description:
        "Open-source project management platform that turns coding agents into real teammates.",
      offers: {
        "@type": "Offer",
        price: "0",
        priceCurrency: "USD",
      },
    },
  ],
};

/**
 * 落地页布局。
 *
 * @param props.children - 落地页各路由内容。
 * @returns 带 json-ld、落地页字体与语言上下文的页面骨架。
 */
export default async function LandingLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const initialLocale = await getRequestLocale();

  return (
    <>
      <script
        type="application/ld+json"
        dangerouslySetInnerHTML={{ __html: JSON.stringify(jsonLd) }}
      />
      <div className={`${instrumentSerif.variable} landing-light h-full overflow-x-hidden overflow-y-auto bg-white`}>
        <LocaleProvider initialLocale={initialLocale}>{children}</LocaleProvider>
      </div>
    </>
  );
}
