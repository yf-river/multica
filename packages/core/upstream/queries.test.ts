import { InfiniteQueryObserver, QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { api } from "../api";
import { upstreamItemsOptions, type UpstreamItem } from "./queries";

vi.mock("../api", () => ({ api: { listUpstreamItems: vi.fn() } }));

describe("上游建议清单分页", () => {
  it("可以读取第 101 条，并保留筛选条件", async () => {
    const items = Array.from({ length: 101 }, (_, i) => ({ id: String(i) }) as UpstreamItem);
    const list = vi.spyOn(api, "listUpstreamItems").mockImplementation(async ({ offset = 0, limit = 100 }) => ({ items: items.slice(offset, offset + limit) }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const observer = new InfiniteQueryObserver(client, upstreamItemsOptions("workspace", { status: "adopted", q: "terminal" }));
    try {
      await observer.refetch();
      expect(observer.getCurrentResult().hasNextPage).toBe(true);
      const result = await observer.fetchNextPage();
      expect(result.data?.pages.flatMap((page) => page.items).map((item) => item.id)).toEqual(items.map((item) => item.id));
      expect(result.hasNextPage).toBe(false);
      expect(list.mock.calls.map(([params]) => params)).toEqual([
        { status: "adopted", q: "terminal", limit: 100, offset: 0 },
        { status: "adopted", q: "terminal", limit: 100, offset: 100 },
      ]);
    } finally {
      observer.destroy();
      client.clear();
      list.mockRestore();
    }
  });
});
