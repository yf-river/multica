import { InfiniteQueryObserver, QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { api } from "../api";
import { upstreamItemsOptions, upstreamRunsOptions, type UpstreamItem, type UpstreamRun } from "./queries";

vi.mock("../api", () => ({ api: { listUpstreamItems: vi.fn(), listUpstreamRuns: vi.fn() } }));

describe("上游建议清单分页", () => {
  it("可以读取第 31 次同步记录", async () => {
    const runs = Array.from({ length: 31 }, (_, i) => ({ id: String(i), status: "completed" }) as UpstreamRun);
    const list = vi.spyOn(api, "listUpstreamRuns").mockImplementation(async (limit = 30, offset = 0) => ({ runs: runs.slice(offset, offset + limit) }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const observer = new InfiniteQueryObserver(client, upstreamRunsOptions("workspace"));
    try {
      await observer.refetch();
      const result = await observer.fetchNextPage();
      expect(result.data?.pages.flatMap((page) => page.runs).map((run) => run.id)).toEqual(runs.map((run) => run.id));
      expect(result.hasNextPage).toBe(false);
      expect(list.mock.calls).toEqual([[30, 0], [30, 30]]);
    } finally {
      observer.destroy();
      client.clear();
      list.mockRestore();
    }
  });
  it("可以读取第 101 条，并保留筛选条件", async () => {
    const items = Array.from({ length: 101 }, (_, i) => ({ id: String(i) }) as UpstreamItem);
    const list = vi.spyOn(api, "listUpstreamItems").mockImplementation(async ({ offset = 0, limit = 100 }) => ({ items: items.slice(offset, offset + limit), total: items.length }));
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
