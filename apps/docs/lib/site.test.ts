import { beforeEach, describe, expect, it, vi } from "vitest";

const existingDocs = vi.hoisted(() => new Set<string>());

vi.mock("node:fs", () => ({
  existsSync: vi.fn((path: string) => {
    const normalized = path.replaceAll("\\", "/");
    return [...existingDocs].some((suffix) => normalized.endsWith(suffix));
  }),
}));

const pages = new Map<string, { url: string }>([
  ["zh:", { url: "/" }],
  ["zh:agents", { url: "/agents" }],
]);

vi.mock("@/lib/source", () => ({
  source: {
    getPage: vi.fn((slugs: string[], lang: string) => {
      return pages.get(`${lang}:${slugs.join("/")}`) ?? null;
    }),
  },
}));

beforeEach(() => {
  existingDocs.clear();
  existingDocs.add("index.zh.mdx");
  existingDocs.add("agents.zh.mdx");
});

describe("docsAlternates", () => {
  it.each([
    { slugs: ["agents"], url: "https://www.multica.ai/docs/agents" },
    { slugs: [], url: "https://www.multica.ai/docs" },
  ])("emits Chinese canonical alternates for $url", async ({ slugs, url }) => {
    const { docsAlternates } = await import("./site");

    expect(docsAlternates(slugs)).toEqual({
      canonical: url,
      languages: {
        zh: url,
        "x-default": url,
      },
    });
  });
});
