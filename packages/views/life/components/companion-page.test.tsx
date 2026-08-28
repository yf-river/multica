import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CompanionPage } from "./companion-page";

const state = vi.hoisted(() => ({
  setOpen: vi.fn(), setExpanded: vi.fn(), setSelectedAgentId: vi.fn(), setActiveSession: vi.fn(),
}));

vi.mock("../../i18n", async () => {
  const resource = (await import("../../locales/zh-Hans/life.json")).default;
  return { useT: () => ({ t: (selector: (value: typeof resource) => string) => selector(resource) }) };
});
vi.mock("@multica/core/paths", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/chat", () => ({
  useChatStore: (selector: (value: unknown) => unknown) => selector({
    activeSessionId: null,
    setOpen: state.setOpen,
    setExpanded: state.setExpanded,
    setSelectedAgentId: state.setSelectedAgentId,
    setActiveSession: state.setActiveSession,
  }),
}));
vi.mock("@multica/core/chat/queries", () => ({ chatSessionsOptions: () => ({ queryKey: ["sessions"] }) }));
vi.mock("@multica/core/workspace/queries", () => ({ agentListOptions: () => ({ queryKey: ["agents"] }) }));
vi.mock("@multica/core/life", () => ({
  companionProfileOptions: () => ({ queryKey: ["companion"] }),
  useSetCompanionProfile: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => <span /> }));
vi.mock("@tanstack/react-query", () => ({
  useQuery: ({ queryKey }: { queryKey: string[] }) => {
    if (queryKey[0] === "companion") return { data: { profile: { agent_id: "agent-1" } }, isLoading: false };
    if (queryKey[0] === "agents") return { data: [{ id: "agent-1", name: "搭子", description: "", avatar_url: null, archived_at: null }], isLoading: false };
    return { data: [{ id: "session-1", agent_id: "agent-1" }], isLoading: false };
  },
}));

describe("CompanionPage", () => {
  beforeEach(() => vi.clearAllMocks());

  it("opens the configured companion conversation without an initial form", async () => {
    const view = render(<CompanionPage />);
    expect(screen.getByText("正在打开你们的对话...")).toBeInTheDocument();
    expect(screen.queryByText("选择谁成为你的搭子")).not.toBeInTheDocument();
    await waitFor(() => {
      expect(state.setSelectedAgentId).toHaveBeenCalledWith("agent-1");
      expect(state.setActiveSession).toHaveBeenCalledWith("session-1");
      expect(state.setOpen).toHaveBeenCalledWith(true);
      expect(state.setExpanded).toHaveBeenCalledWith(true);
    });
    view.rerender(<CompanionPage />);
    expect(state.setActiveSession).toHaveBeenCalledTimes(1);
    view.unmount();
    expect(state.setOpen).toHaveBeenLastCalledWith(false);
    expect(state.setExpanded).toHaveBeenLastCalledWith(false);
  });
});
