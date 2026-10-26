import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, setApiInstance } from "@multica/core/api";
import { I18nProvider } from "@multica/core/i18n/react";
import { setCurrentWorkspace } from "@multica/core/platform";
import { useRuntimeInputDraftStore } from "@multica/core/runtimes/input-draft-store";
import chat from "../../locales/zh-Hans/chat.json";
import { useChatRuntimeInput } from "./use-chat-runtime-input";

vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));
const clients: QueryClient[] = [];
afterEach(() => { vi.restoreAllMocks(); for (const client of clients.splice(0)) client.clear(); });
beforeEach(async () => {
  setCurrentWorkspace("input-hook-test", "workspace");
  await Promise.resolve();
  useRuntimeInputDraftStore.setState({ drafts: {} });
});

function setup(capabilities: string[]) {
  const api = new ApiClient("https://api.example.test");
  setApiInstance(api);
  vi.spyOn(api, "getRuntimeInputCapabilities").mockResolvedValue({ capabilities, available: true });
  const send = vi.spyOn(api, "createRuntimeInput");
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(client);
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}><I18nProvider locale="zh-Hans" resources={{ "zh-Hans": { chat } }}>{children}</I18nProvider></QueryClientProvider>;
  return { send, ...renderHook(() => useChatRuntimeInput("workspace", "task", true), { wrapper }) };
}

describe("current run chat inputs", () => {
  it("reuses one durable identity after an ambiguous save and clears it after confirmation", async () => {
    const { result, send } = setup(["text", "command"]);
    await waitFor(() => expect(result.current.commandsAvailable).toBe(true));
    send.mockRejectedValueOnce(new Error("response lost"));
    let accepted: boolean | undefined;
    await act(async () => { accepted = await result.current.trySend("/goal pause", false); });
    expect(accepted).toBe(false);
    const first = send.mock.calls[0]![1];
    send.mockImplementationOnce(async (_, input) => ({ ...input, taskId: "task", authorId: "human", status: "pending", output: "", failureReason: "", createdAt: "2026-10-26T00:00:00Z" }));
    await act(async () => { accepted = await result.current.trySend("/goal pause", false); });
    expect(accepted).toBe(true);
    expect(send.mock.calls[1]?.[1].id).toBe(first.id);
    expect(useRuntimeInputDraftStore.getState().drafts.task).toBeUndefined();
  });
  it("preserves ordinary queued messages and rejects attachments in current-run delivery", async () => {
    const { result, send } = setup(["text"]);
    await waitFor(() => expect(result.current.textAvailable).toBe(true));
    expect(await result.current.trySend("follow up", false, "queue")).toBeUndefined();
    expect(await result.current.trySend("/goal finish work", false, "queue")).toBeUndefined();
    expect(await result.current.trySend("add evidence", true, "current")).toBe(false);
    expect(send).not.toHaveBeenCalled();
  });
});
