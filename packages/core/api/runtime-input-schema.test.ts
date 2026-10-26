import { afterEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { ApiClient } from "./client";
import { setApiInstance } from "./index";
import { createRuntimeInputOptions } from "../runtimes/inputs";

afterEach(() => vi.unstubAllGlobals());
const wire = { id: "input", task_id: "task", author_id: "human", kind: "command", content: "/goal pause", status: "delivering", output: "", failure_reason: "", created_at: "2026-10-26T00:00:00Z" };

describe("durable runtime input API", () => {
  it("retains complete pagination and unknown future states", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ inputs: [{ ...wire, status: "future-state" }], next_cursor: "older/+?" })));
    vi.stubGlobal("fetch", fetch);
    const page = await new ApiClient("https://api.example.test").getRuntimeInputs("task/one", "older/+?");
    expect(fetch.mock.calls[0]?.[0]).toBe("https://api.example.test/api/tasks/task%2Fone/runtime-inputs?cursor=older%2F%2B%3F");
    expect(page).toMatchObject({ available: true, nextCursor: "older/+?", inputs: [{ taskId: "task", authorId: "human", status: "future-state" }] });
  });
  it("distinguishes malformed pages from an empty history and fails closed on old servers", async () => {
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ inputs: [wire] })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ capabilities: "command" })))
      .mockResolvedValueOnce(new Response("not found", { status: 404 })));
    const client = new ApiClient("https://api.example.test");
    expect((await client.getRuntimeInputs("task")).available).toBe(false);
    expect((await client.getRuntimeInputCapabilities("task")).available).toBe(false);
    expect(await client.getRuntimeInputCapabilities("task")).toEqual({ capabilities: [], available: false });
  });
  it("rejects another input's receipt without retrying the native action", async () => {
    const instance = new ApiClient("https://api.example.test");
    setApiInstance(instance);
    const create = vi.spyOn(instance, "createRuntimeInput").mockResolvedValue({ id: "other", taskId: "task", authorId: "human", kind: "command", content: "/goal pause", status: "pending", output: "", failureReason: "", createdAt: wire.created_at });
    const client = new QueryClient();
    try {
      const mutation = client.getMutationCache().build(client, createRuntimeInputOptions(client, "ws", "task"));
      await expect(mutation.execute({ id: "input", kind: "command", content: "/goal pause" })).rejects.toThrow("invalid runtime input receipt");
      expect(create).toHaveBeenCalledOnce();
    } finally {
      create.mockRestore();
      client.clear();
    }
  });
});
