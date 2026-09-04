import { beforeEach, describe, expect, it, vi } from "vitest";

const existingDocs = vi.hoisted(() => new Set<string>());

vi.mock("node:fs", () => ({
  existsSync: vi.fn((path: string) => {
    const normalized = path.replaceAll("\\", "/");
    return [...existingDocs].some((suffix) => normalized.endsWith(suffix));
  }),
}));

vi.mock("@/lib/source", () => ({
  source: {
    getPage: vi.fn((slugs: string[], lang: string) => {
      if (lang !== "zh") return null;
      const suffix = slugs.length === 0 ? "" : `/${slugs.join("/")}`;
      return { url: `/zh${suffix}` };
    }),
  },
}));

beforeEach(() => {
  existingDocs.clear();
  existingDocs.add("index.zh.mdx");
  existingDocs.add("agents.zh.mdx");
});

describe("docsAlternates", () => {
  it("publishes only the Chinese page and uses it as the canonical", async () => {
    const { docsAlternates } = await import("./site");

    expect(docsAlternates(["agents"])).toEqual({
      canonical: "https://www.multica.ai/docs/zh/agents",
      languages: {
        zh: "https://www.multica.ai/docs/zh/agents",
        "x-default": "https://www.multica.ai/docs/zh/agents",
      },
    });
  });

  it("does not advertise removed languages", async () => {
    const { docsAlternates } = await import("./site");
    const result = docsAlternates([]);

    expect(result.languages).toEqual({
      zh: "https://www.multica.ai/docs/zh",
      "x-default": "https://www.multica.ai/docs/zh",
    });
    expect(result.languages).not.toHaveProperty("en");
    expect(result.languages).not.toHaveProperty("ja");
    expect(result.languages).not.toHaveProperty("ko");
  });
});
