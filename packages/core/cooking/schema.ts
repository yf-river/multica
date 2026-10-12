import { z } from "zod";
export const CookingEntrySchema = z
  .object({
    id: z.string(),
    name: z.string(),
    kind: z.string(),
    category: z.string(),
    category_name: z.string().default(""),
    markdown: z.string().default(""),
    ingredients: z.string().default(""),
    difficulty: z.string().default(""),
    difficulty_rank: z.number().default(0),
    cover: z.string().default(""),
    illustrations: z.array(z.string()).default([]),
    source_url: z.string().default(""),
    image_source_url: z.string().default(""),
    available: z.boolean().default(true),
    favorite: z.boolean().default(false),
  })
  .transform((e) => ({
    id: e.id,
    name: e.name,
    kind: e.kind,
    category: e.category,
    categoryName: e.category_name,
    markdown: e.markdown,
    ingredients: e.ingredients,
    difficulty: e.difficulty,
    difficultyRank: e.difficulty_rank,
    cover: e.cover,
    illustrations: e.illustrations,
    sourceUrl: e.source_url,
    imageSourceUrl: e.image_source_url,
    available: e.available,
    favorite: e.favorite,
  }));
export type CookingEntry = z.output<typeof CookingEntrySchema>;
export const CookingListSchema = z
  .object({
    items: z.array(CookingEntrySchema),
    total: z.number(),
    page: z.number(),
    page_size: z.number(),
    version: z.string(),
  })
  .transform((x) => ({ ...x, pageSize: x.page_size }));
export type CookingList = z.output<typeof CookingListSchema>;
export const CookingDetailSchema = z.object({
  entry: CookingEntrySchema,
  related: z.array(CookingEntrySchema),
  version: z.string(),
});
export type CookingDetail = z.output<typeof CookingDetailSchema>;
export const CookingCategoriesSchema = z.array(
  z.object({ id: z.string(), name: z.string(), count: z.number() }),
);
export type CookingCategory = z.output<typeof CookingCategoriesSchema>[number];
