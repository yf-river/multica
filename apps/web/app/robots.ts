import type { MetadataRoute } from "next";

/**
 * 生成 robots.txt 内容：告诉搜索引擎哪些公开路径可以抓取、哪些内部/应用路径要排除。
 *
 * @returns 允许与禁止的抓取规则，以及站点地图地址。
 */
export default function robots(): MetadataRoute.Robots {
  const baseUrl = "https://www.multica.ai";

  return {
    rules: [
      {
        userAgent: "*",
        allow: ["/", "/about", "/changelog"],
        disallow: [
          "/api/",
          "/ws",
          "/auth/",
          "/issues",
          "/board",
          "/inbox",
          "/agents",
          "/settings",
          "/my-issues",
          "/runtimes",
          "/skills",
        ],
      },
    ],
    sitemap: [`${baseUrl}/sitemap.xml`, `${baseUrl}/docs/sitemap.xml`],
  };
}
