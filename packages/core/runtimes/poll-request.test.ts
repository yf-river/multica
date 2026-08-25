import { afterEach, describe, expect, it, vi } from "vitest";
import { pollRuntimeRequest } from "./poll-request";

const pending = { id: "request-1", status: "pending" };

describe("pollRuntimeRequest", () => {
  afterEach(() => vi.useRealTimers());

  it("polls the original request until it reaches a terminal state", async () => {
    vi.useFakeTimers();
    const getResult = vi
      .fn()
      .mockResolvedValueOnce({ ...pending, status: "running" })
      .mockResolvedValueOnce({ ...pending, status: "completed" });

    const result = pollRuntimeRequest(pending, getResult, 2_000, "timed out");
    await vi.advanceTimersByTimeAsync(1_000);

    await expect(result).resolves.toEqual({ ...pending, status: "completed" });
    expect(getResult).toHaveBeenNthCalledWith(1, "request-1");
    expect(getResult).toHaveBeenNthCalledWith(2, "request-1");
  });

  it("rejects a request that stays pending beyond its deadline", async () => {
    vi.useFakeTimers();
    const result = pollRuntimeRequest(
      pending,
      async () => pending,
      499,
      "request timed out",
    );
    const rejection = expect(result).rejects.toThrow("request timed out");

    await vi.advanceTimersByTimeAsync(500);
    await rejection;
  });
});
