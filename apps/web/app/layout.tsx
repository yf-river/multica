/**
 * 应用的根布局：负责加载字体、注入全局 Provider（主题 / i18n / 数据查询）与 Toaster，
 * 并声明站点元信息（SEO、OpenGraph、PWA 相关）。所有页面最终都渲染在这个 <html>/<body> 之下。
 */
import type { Metadata, Viewport } from "next";
import Script from "next/script";
import localFont from "next/font/local";
import { ThemeProvider } from "@/components/theme-provider";
import { Toaster } from "@multica/ui/components/ui/sonner";
import { cn } from "@multica/ui/lib/utils";
import { WebProviders } from "@/components/web-providers";
import { RESOURCES } from "@multica/views/locales";
import { getRequestLocale } from "@/lib/request-locale";
import { SITE_TITLE, TITLE_TEMPLATE } from "@/platform/document-title";
import {
  resolveBrowserApiBaseUrl,
  resolveBrowserWsUrl,
} from "@/config/runtime-urls";
import { readBuildInfo } from "@/platform/build-info";
import "./globals.css";

// Inter 是拉丁字母正文使用的 UI 字体。next/font 会生成一个带哈希的字体族名（`__Inter_xxx`）
// 以及一个尺寸适配的合成回退字体，用来避免加载前的 FOUT 布局抖动——两者都通过 `--font-inter` 这个 CSS 变量暴露。
//
// 完整的 `--font-sans` 字体栈（Inter + 按语言区分的 CJK 回退链）是在 ./globals.css 里用静态 CSS 拼出来的，
// 而不是在这里：它必须能根据 `<html lang>` 逐个覆盖（日文汉字属于汉字体系，需要「日文优先」的 CJK 回退栈），
// 而带哈希的字体族名只能在 CSS 里通过变量引用。把 CJK 回退链放在 CSS 里也保证了 CSP 安全。
//
// 斜体被显式加载：`style` 默认值是 `["normal"]`，如果不提供真实的斜体字面，
// 那约 20 处语义化的斜体标签（聊天空状态、模型选择器的 "Managed by runtime"、仪表盘/squad 占位符）
// 以及所有 markdown 的 <em> 和引用块都会退化成浏览器合成的伪斜体。
const inter = localFont({
  src: [
    {
      path: "../../../node_modules/@fontsource-variable/inter/files/inter-latin-wght-normal.woff2",
      style: "normal",
      weight: "100 900",
    },
    {
      path: "../../../node_modules/@fontsource-variable/inter/files/inter-latin-wght-italic.woff2",
      style: "italic",
      weight: "100 900",
    },
  ],
  display: "swap",
  variable: "--font-inter",
});
// 等宽字体不显式声明 CJK 回退：代码块里的中文字符本来就和等宽网格对不齐（中文是比例字体），
// 在这里列 CJK 字体反而会错误地暗示「保证对齐」。罕见的混合场景交给浏览器默认回退处理即可。
const geistMono = localFont({
  src: "../../../node_modules/@fontsource-variable/geist-mono/files/geist-mono-latin-wght-normal.woff2",
  display: "swap",
  weight: "100 900",
  variable: "--font-mono",
  fallback: ["ui-monospace", "SFMono-Regular", "Menlo", "Consolas", "monospace"],
});
// 用于 onboarding 标题的编辑类衬线字体。提供斜体支持，用于 h1 里的 em 强调（例如 "...on one shared board."）。
// 只有在渲染该字体的路由上才会加载；布局抖动防护和 Inter 一样，交给 next/font 的合成回退度量处理。
const sourceSerif = localFont({
  src: [
    {
      path: "../../../node_modules/@fontsource-variable/source-serif-4/files/source-serif-4-latin-wght-normal.woff2",
      style: "normal",
      weight: "200 900",
    },
    {
      path: "../../../node_modules/@fontsource-variable/source-serif-4/files/source-serif-4-latin-wght-italic.woff2",
      style: "italic",
      weight: "200 900",
    },
  ],
  display: "swap",
  variable: "--font-serif",
  fallback: [
    "ui-serif",
    "Iowan Old Style",
    "Apple Garamond",
    "Baskerville",
    "Times New Roman",
    "serif",
  ],
});

/**
 * 视口与主题配置：告诉浏览器按设备宽度渲染、初始缩放为 1，
 * 并根据系统明暗模式给出对应的状态栏/地址栏主题色。
 */
// 环境身份必须在请求时读取，同一个发布包可部署到不同环境。
export const dynamic = "force-dynamic";

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#ffffff" },
    { media: "(prefers-color-scheme: dark)", color: "#05070b" },
  ],
};

/**
 * 站点的全局元信息：默认标题、描述、图标、PWA 行为、OpenGraph 与 Twitter 分享卡片、
 * 以及搜索引擎收录规则。子页面可通过自己的 metadata 覆盖其中部分字段。
 */
export const metadata: Metadata = {
  metadataBase: new URL("https://www.multica.ai"),
  title: {
    default: SITE_TITLE,
    template: TITLE_TEMPLATE,
  },
  description:
    "让 AI 智能体真正参与任务、协作与长期工作的开源平台。",
  icons: {
    icon: [{ url: "/favicon.svg", type: "image/svg+xml" }],
    shortcut: ["/favicon.svg"],
    // iOS 添加到主屏幕时从不读 manifest 里的图标，它需要一张自己的不透明满幅方形图标，并自行裁剪圆角。
    apple: [{ url: "/icons/apple-touch-icon.png", sizes: "180x180" }],
  },
  // 主屏幕行为：启动时不带浏览器边框，图标名用 "Multica" 而不是冗长的 SEO <title>。
  // `capable` 会渲染标准化的 `mobile-web-app-capable` 标签——Next 16 不再输出已废弃的 apple 前缀写法，
  // 所以 iOS 的独立窗口模式改由 manifest 的 `display` 承担（iOS 16.4 起支持）。
  appleWebApp: {
    capable: true,
    title: "Multica",
    // `default` 让 Web 视图停留在状态栏下方。要做到全屏无边（`black-translucent` + viewport-fit=cover）
    // 需要配合 env(safe-area-inset-*) 内边距，而目前应用里还没有任何界面做了这层适配。
    statusBarStyle: "default",
  },
  openGraph: {
    type: "website",
    siteName: "Multica",
    locale: "zh_CN",
  },
  twitter: {
    card: "summary_large_image",
    site: "@multica_hq",
    creator: "@multica_hq",
  },
  alternates: {
    canonical: "/",
  },
  robots: {
    index: true,
    follow: true,
  },
};

// HTML 的 lang 属性使用 BCP-47 区域标签，屏幕阅读器和字体栈对这种标签识别度更高。
// i18next 内部保留 `zh-Hans` 作为自己的 locale（脚本子标签才是我们真正做翻译的键），
// 但 <html> 元素期望一个带区域后缀的标签，以便无障碍工具识别并正确命中 CJK 回退字体。
/**
 * 根布局组件。
 *
 * @param children - Next.js 注入的当前路由页面内容。
 * @returns 完整的 <html> 骨架：字体变量、主题、i18n 与数据 Provider、Toaster 通知容器。
 */
export default async function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const buildInfo = await readBuildInfo();
  const locale = await getRequestLocale();
  const resources = { "zh-Hans": RESOURCES["zh-Hans"] };
  const apiBaseUrl = resolveBrowserApiBaseUrl(process.env);
  const wsUrl = resolveBrowserWsUrl(process.env);

  return (
    <html
      lang="zh-CN"
      suppressHydrationWarning
      className={cn("antialiased font-sans h-full", inter.variable, geistMono.variable, sourceSerif.variable)}
    >
      <body className="h-full overflow-hidden">
        {/*
          react-grab：仅开发环境可用的元素检查器。按住 ⌘C（Mac）/ Ctrl+C 再点击任意元素，
          即可复制它的源码路径 + 行号 + 组件栈，方便粘贴给 AI。
          按开发者个人选择启用：只有在本地、被 gitignore 的 apps/web/.env.local 里设置了
          VITE_REACT_GRAB 时才会加载——对其他任何人都不会激活。两个判断都在服务端读取，
          所以除非你主动开启，否则这段 <Script> 会直接从 HTML 里省略。详见 https://www.react-grab.com/
        */}
        {process.env.NODE_ENV === "development" && process.env.VITE_REACT_GRAB && (
          <Script
            src="//unpkg.com/react-grab/dist/index.global.js"
            crossOrigin="anonymous"
            strategy="beforeInteractive"
          />
        )}
        <ThemeProvider>
          <WebProviders
            buildInfo={buildInfo}
            locale={locale}
            resources={resources}
            apiBaseUrl={apiBaseUrl}
            environmentId={process.env.MULTICA_ENV_ID || ""}
            wsUrl={wsUrl}
          >
            {children}
          </WebProviders>
          <Toaster />
        </ThemeProvider>
      </body>
    </html>
  );
}
