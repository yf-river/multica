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
import "./globals.css";

// Inter is the Latin UI face. next/font produces a hashed family (`__Inter_xxx`)
// plus a synthetic size-adjusted fallback face to prevent FOUT layout shift —
// both are exposed under the `--font-inter` CSS variable.
//
// The full `--font-sans` stack (Inter + the per-locale CJK fallback chain) is
// assembled in static CSS in ./globals.css, not here: it must be overridable per
// `<html lang>` (Japanese Kanji are Han ideographs and need a Japanese-first CJK
// stack), and a hashed family name can only be referenced from CSS via a variable.
// Keeping the CJK chain in CSS also keeps it CSP-safe.
//
// Italic is loaded explicitly: `style` defaults to `["normal"]`, and without a real
// italic face the ~20 semantic italic labels (chat empty states, model-picker's
// "Managed by runtime", dashboard/squad placeholders) plus every markdown <em> and
// blockquote rendered as browser-synthesized oblique.
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
// Mono font has no explicit CJK fallback: CJK chars in code blocks are inherently
// non-aligned with a mono grid (Chinese is proportional), so listing CJK fonts
// here would falsely signal alignment guarantees. Browser default fallback handles
// the rare mixed case correctly.
const geistMono = localFont({
  src: "../../../node_modules/@fontsource-variable/geist-mono/files/geist-mono-latin-wght-normal.woff2",
  display: "swap",
  weight: "100 900",
  variable: "--font-mono",
  fallback: ["ui-monospace", "SFMono-Regular", "Menlo", "Consolas", "monospace"],
});
// Editorial serif used for onboarding headlines. Italic support for h1 em
// accents (e.g. "...on one shared board."). Only loaded on routes that
// render the font; layout-shift-prevention handled by next/font's synthetic
// fallback metrics, same as Inter.
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

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#ffffff" },
    { media: "(prefers-color-scheme: dark)", color: "#05070b" },
  ],
};

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
    // iOS never reads the manifest's icons for the home screen; it needs its
    // own opaque, full-bleed square and rounds the corners itself.
    apple: [{ url: "/icons/apple-touch-icon.png", sizes: "180x180" }],
  },
  // Home-screen behaviour: launch without browser chrome, and label the icon
  // "Multica" rather than the long SEO <title>. `capable` renders the
  // standardised `mobile-web-app-capable` tag — Next 16 no longer emits the
  // deprecated apple-prefixed spelling, so iOS standalone rides on the
  // manifest's `display` instead (honoured since iOS 16.4).
  appleWebApp: {
    capable: true,
    title: "Multica",
    // `default` keeps the web view below the status bar. Going edge-to-edge
    // (`black-translucent` + viewport-fit=cover) needs env(safe-area-inset-*)
    // padding, which no surface in the app has yet.
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

// HTML lang attribute uses BCP-47 region tags that screen readers and font
// stacks recognize widely. i18next keeps `zh-Hans` as its internal locale
// (script subtag is what we actually translate against), but the html element
// expects a region-flavoured tag for accessibility tooling and CJK fallback.
export default async function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
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
          react-grab: dev-only element inspector. Hold ⌘C (Mac) / Ctrl+C and click
          any element to copy its source path + line + component stack for pasting
          to an AI. Opt-in per developer: only loads when VITE_REACT_GRAB is set in
          a local, gitignored apps/web/.env.local — it never activates for anyone
          else. Both guards are read server-side, so the <Script> is omitted from
          the HTML entirely unless you opted in. See https://www.react-grab.com/
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
            locale={locale}
            resources={resources}
            apiBaseUrl={apiBaseUrl}
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
