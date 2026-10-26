import { describe, expect, it } from "vitest";
import { knowledgeEntries } from "./resources";

describe("knowledge resource entries", () => {
  it("reads OpenViking original listings and search groups", () => {
    expect(
      knowledgeEntries({
        result: [
          { name: "notes", uri: "viking://resources/ws/notes", isDir: true },
        ],
      }),
    ).toEqual([
      { uri: "viking://resources/ws/notes", title: "notes", isDirectory: true },
    ]);
    expect(
      knowledgeEntries({
        result: {
          resources: [{ uri: "one", abstract: "summary", score: 0.75 }],
        },
      }),
    ).toMatchObject([{ uri: "one", abstract: "summary", score: 0.75 }]);
  });
  it("ignores malformed rows and deduplicates resource identities", () => {
    expect(
      knowledgeEntries(
        {
          result: [
            { uri: "one" },
            null,
            { uri: "one" },
            { name: ".." },
            "text",
          ],
        },
        "viking://resources/ws",
      ),
    ).toHaveLength(1);
    expect(knowledgeEntries(null)).toEqual([]);
    expect(knowledgeEntries({ result: "broken" })).toEqual([]);
  });
});
