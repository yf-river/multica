import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { usePendingChatOperationStore } from "@multica/core/chat";

vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));
vi.mock("@multica/core/platform", () => ({
  getCurrentWsId: () => "ws-1",
}));
vi.mock("@multica/core/api", () => ({ api: {} }));

import { useChatOperationRecovery } from "./use-chat-operation-recovery";

describe("useChatOperationRecovery", () => {
  beforeEach(() => {
    usePendingChatOperationStore.setState({ operations: {} });
  });

  it("keeps an empty operation snapshot stable while mounted", () => {
    const { result } = renderHook(() => useChatOperationRecovery("ws-1"));

    expect(result.current).toBeUndefined();
  });
});
