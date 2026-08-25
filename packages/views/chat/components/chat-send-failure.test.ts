import { describe, expect, it, vi } from "vitest";
import { ApiError, ApiResponseValidationError, ApiTransportError } from "@multica/core/api";
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

  it("keeps intent for a mutation transport failure", () => {
    const refreshServerState = vi.fn();
    const rollbackOptimisticState = vi.fn();

    expect(reconcileChatSendFailure(
      new ApiTransportError("POST /api/chat/sessions/:id/messages", true, new TypeError("fetch failed")),
      { refreshServerState, rollbackOptimisticState },
    )).toBe("outcome-unknown");

    expect(refreshServerState).toHaveBeenCalledOnce();
    expect(rollbackOptimisticState).not.toHaveBeenCalled();
  });

  it("keeps the same idempotent intent for a gateway or server 5xx", () => {
    const refreshServerState = vi.fn();
    const rollbackOptimisticState = vi.fn();

    expect(reconcileChatSendFailure(
      new ApiError("bad gateway", 502, "Bad Gateway"),
      { refreshServerState, rollbackOptimisticState },
    )).toBe("outcome-unknown");

    expect(refreshServerState).toHaveBeenCalledOnce();
    expect(rollbackOptimisticState).not.toHaveBeenCalled();
  });
});
