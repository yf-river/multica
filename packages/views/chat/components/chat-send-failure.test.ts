import { describe, expect, it, vi } from "vitest";
import { ApiResponseValidationError } from "@multica/core/api";
import { reconcileChatSendFailure } from "./chat-send-failure";

describe("reconcileChatSendFailure", () => {
  it("refreshes authoritative state when a successful mutation response is malformed", () => {
    const refreshServerState = vi.fn();
    const rollbackOptimisticState = vi.fn();

    expect(reconcileChatSendFailure(
      new ApiResponseValidationError("POST /api/chat/sessions/:id/messages", true),
      { refreshServerState, rollbackOptimisticState },
    )).toBe("outcome-unknown");

    expect(refreshServerState).toHaveBeenCalledOnce();
    expect(rollbackOptimisticState).not.toHaveBeenCalled();
  });

  it("rolls back when the request is known not to have committed", () => {
    const refreshServerState = vi.fn();
    const rollbackOptimisticState = vi.fn();

    expect(reconcileChatSendFailure(
      new ApiResponseValidationError("POST /preview", false),
      { refreshServerState, rollbackOptimisticState },
    )).toBe("failed");

    expect(rollbackOptimisticState).toHaveBeenCalledOnce();
    expect(refreshServerState).not.toHaveBeenCalled();
  });
});
