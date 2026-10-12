// @vitest-environment jsdom
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { Agent, MemberWithUser } from "../types";
import { workspaceKeys } from "../workspace/queries";
import { useMikaConversation } from "./use-mika-conversation";

vi.mock("../auth", () => ({ useAuthStore: (selector: (state: unknown) => unknown) => selector({ user: { id: "u" } }) }));
vi.mock("../api", () => ({ api: { listAgents: vi.fn(), listMembers: vi.fn(), getChatSession: vi.fn(), createChatSession: vi.fn() } }));
afterEach(() => { cleanup(); vi.clearAllMocks(); });
const mika = { id: "m", name: "A different name", system_key: "mika", owner_id: "owner", permission_mode: "public_to", invocation_targets: [{ target_type: "workspace" }], archived_at: null } as Agent;

function setup(sessionId: string | null = null) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity }, mutations: { retry: false } } });
  client.setQueryData(workspaceKeys.agents("ws"), [mika]);
  client.setQueryData(workspaceKeys.members("ws"), [{ user_id: "u", role: "member" } as MemberWithUser]);
  vi.mocked(api.listAgents).mockResolvedValue([mika]);
  vi.mocked(api.listMembers).mockResolvedValue([{ user_id: "u", role: "member" } as MemberWithUser]);
  const hook = renderHook(() => useMikaConversation("ws", "one", sessionId), { wrapper: ({ children }) => <QueryClientProvider client={client}>{children}</QueryClientProvider> });
  return { ...hook, client };
}

describe("Mika conversation", () => {
  it("uses system_key and follows archive and membership changes", async () => {
    const { result, client } = setup();
    await waitFor(() => expect(result.current.mika?.id).toBe("m"));
    act(() => { client.setQueryData(workspaceKeys.agents("ws"), [{ ...mika, archived_at: "now" }]); });
    await waitFor(() => expect(result.current.mika).toBeUndefined());
    act(() => { client.setQueryData(workspaceKeys.agents("ws"), [{ ...mika, system_key: "", name: "Mika" }]); });
    await waitFor(() => expect(result.current.mika).toBeUndefined());
    act(() => { client.setQueryData(workspaceKeys.agents("ws"), [mika]); client.setQueryData(workspaceKeys.members("ws"), []); });
    await waitFor(() => expect(result.current.mika).toBeUndefined());
  });

  it("requests member-scoped reuse with an explicit workspace and validates the result", async () => {
    vi.mocked(api.createChatSession).mockResolvedValue({ id: "s", agent_id: "m", workspace_id: "ws", creator_id: "u" } as never);
    const { result } = setup();
    await waitFor(() => expect(result.current.mika).toBeDefined());
    await act(async () => { await result.current.open.mutateAsync(); });
    expect(api.createChatSession).toHaveBeenCalledWith({ agent_id: "m", title: "A different name", reuse_existing: true }, "one");
    vi.mocked(api.createChatSession).mockResolvedValue({ id: "wrong", agent_id: "m", workspace_id: "other", creator_id: "u" } as never);
    await expect(result.current.open.mutateAsync()).rejects.toThrow("Invalid Mika conversation");
  });
});
