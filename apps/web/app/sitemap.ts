import type { MetadataRoute } from "next";

/**
 * 生成 sitemap.xml 内容：列出需要被搜索引擎收录的公开页面及其更新频率/优先级。
 *
 * @returns 站点地图条目数组。
 */
export default function sitemap(): MetadataRoute.Sitemap {
  const baseUrl = "https://www.multica.ai";

  return [
    {
      url: baseUrl,
      lastModified: new Date("2026-04-01"),
      changeFrequency: "weekly",
      priority: 1.0,
    },
    {
      url: `${baseUrl}/about`,
      lastModified: new Date("2026-04-01"),
      changeFrequency: "monthly",
      priority: 0.7,
    },
    {
      url: `${baseUrl}/changelog`,
      lastModified: new Date("2026-04-01"),
      changeFrequency: "weekly",
      priority: 0.6,
    },
    {
      url: `${baseUrl}/contact-sales`,
      lastModified: new Date("2026-05-21"),
      changeFrequency: "monthly",
      priority: 0.7,
    },
  ];
}
