/**
 * Fumadocs 内容收集配置：为官网「客户案例」内容定义文档目录与 frontmatter
 * 校验 schema（含额外的封面图/更新日期/分类字段）。
 */
import { defineDocs, defineConfig } from "fumadocs-mdx/config";
import { z } from "zod";

// 默认的 fumadocs frontmatter schema 很严格，会剥掉 `{title, description, icon, full}`
// 之外的任何字段。案例页需要 hero_image 与 updated_at 来渲染列表卡片与排序，
// 外加可选的 category 标签——在这里声明它们才能通过校验并获得 TS 类型，
// 同时 looseObject 仍放行任意临时字段。
// YAML 会把裸 ISO 日期（`updated_at: 2026-05-19`）自动解析成 Date 对象，
// 那样就会过不了 z.string()。用 preprocess 把 Date 规整回 `YYYY-MM-DD` 字符串，
// 让作者两种写法都能用。
const dateString = z.preprocess(
  (v) => (v instanceof Date ? v.toISOString().slice(0, 10) : v),
  z.string(),
);

/** 案例内容 schema：额外允许 hero_image（封面）、updated_at（更新日期）、category（分类）。 */
const useCaseFrontmatterSchema = z.looseObject({
  title: z.string(),
  description: z.string().optional(),
  hero_image: z.string(),
  updated_at: dateString,
  category: z.string().optional(),
});

/** 「案例」文档集：指向 content/use-cases 目录，应用上面的 frontmatter schema。 */
export const useCases = defineDocs({
  dir: "content/use-cases",
  docs: {
    schema: useCaseFrontmatterSchema,
  },
});

/** Fumadocs 总配置（当前无需额外 MDX 选项）。 */
export default defineConfig({
  mdxOptions: {},
});
