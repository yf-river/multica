import type { MetadataRoute } from "next";

/**
 * Web 应用的 PWA manifest：让移动端 Web 应用可以被安装到桌面/主屏幕，并以独立窗口运行，
 * 而不是窝在浏览器标签页里。
 *
 * `start_url` 故意不指向 "/"。官方营销站点即使用户已登录，根路径也仍停留在公开首页
 * （见 proxy.ts 里的 `isOfficialMarketingHost`），所以让安装后的应用指向 "/" 会打开落地页。
 * "/inbox" 属于 `LEGACY_ROUTE_SEGMENTS`，proxy.ts 会按会话解析它：已登录且知道 workspace 时
 * 落到该 workspace 的 inbox；已登录但还没有 workspace 时落到 /login（会再根据 workspace 列表解析）；
 * 未登录时同样落到 /login。这三种结果都在 manifest.test.ts 里做了断言，因为主屏幕图标没有地址栏，
 * 一旦跳错地方用户无法自行修正。
 *
 * 图标原稿是 public/icons/icon-source.png。普通启动图标分别缩放到 192/512 像素；
 * Apple 图标缩放到 180 像素并以 #ffcf3f 填充透明区域。maskable 图标把原稿缩放到
 * 384 像素，居中放在同色的 512 像素不透明底板上，为启动器裁剪留出安全空间。
 * favicon.svg 与 icons/icon.svg 分别内嵌原稿的 128/512 像素 PNG，沿用 SVG 引用地址。
 */

/** PWA 的启动路径。导出是为了让 manifest.test.ts 能把它送进 proxy 做解析验证。 */
export const PWA_START_URL = "/inbox";

/**
 * 生成 manifest 对象。
 *
 * @returns Next.js 约定的 Manifest 结构：应用名称、图标、启动地址、主题色等。
 */
export default function manifest(): MetadataRoute.Manifest {
  return {
    id: "/",
    name: "Multica",
    short_name: "Multica",
    description:
      "Assign tasks to coding agents, track progress, and keep your team's work in one place.",
    start_url: PWA_START_URL,
    scope: "/",
    display: "standalone",
    // 启动闪屏颜色。运行时状态栏的颜色由 app/layout.tsx 里按明暗模式配对输出的
    // `<meta name="theme-color">` 驱动，这是 manifest 表达不了的——这里只是启动前的兜底。
    background_color: "#ffffff",
    theme_color: "#ffffff",
    categories: ["productivity"],
    icons: [
      {
        src: "/icons/icon-192.png",
        sizes: "192x192",
        type: "image/png",
        purpose: "any",
      },
      {
        src: "/icons/icon-512.png",
        sizes: "512x512",
        type: "image/png",
        purpose: "any",
      },
      {
        src: "/icons/icon-maskable-512.png",
        sizes: "512x512",
        type: "image/png",
        purpose: "maskable",
      },
    ],
    // 长按已安装启动图标弹出的快捷操作。两者都是 legacy 段，所以会像 start_url 一样
    // 解析到最近使用的 workspace，而不需要 manifest 里根本无法知道的 slug。
    shortcuts: [
      { name: "Inbox", url: "/inbox" },
      { name: "My Issues", url: "/my-issues" },
    ],
  };
}
