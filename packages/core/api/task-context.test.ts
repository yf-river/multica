// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

describe("runtime context pagination", () => {
  it("preserves opaque cursor and optional provider metrics", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      events: [{ kind: "context", provider: "codex", pressure_tokens: 42 }],
      next_cursor: "next-cursor", has_more: true,
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const page = await new ApiClient("https://api.example.test").getTaskContext("issue", "task", "opaque/+?");
    expect(fetchMock.mock.calls[0]?.[0]).toBe("https://api.example.test/api/issues/issue/tasks/task/context?after=opaque%2F%2B%3F");
    expect(page.has_more).toBe(true);
    expect(page.events[0]?.duration_ms).toBeUndefined();
    expect(page.events[0]?.pressure_tokens).toBe(42);
  });

  it("accepts older responses and rejects malformed pagination", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ events: [{ kind: "compaction" }] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ events: [], has_more: "yes", next_cursor: 123 }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");
    expect((await client.getTaskContext("i", "t")).events).toEqual([{ kind: "compaction" }]);
    await expect(client.getTaskContext("i", "t")).rejects.toThrow("Invalid runtime context response");
  });
  it("reads chat runtime history through task visibility rather than an invented issue", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ events: [], has_more: false }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    await new ApiClient("https://api.example.test").getTaskContext("", "chat-task");
    expect(fetchMock.mock.calls[0]?.[0]).toBe("https://api.example.test/api/tasks/chat-task/context");
  });

});
