import { describe, expect, it } from "vitest";

import { resolveRemoteApiUrl } from "./runtime-urls";

describe("resolveRemoteApiUrl", () => {
  it("prefers goal-test deployment API URL over worktree env", () => {
    expect(
      resolveRemoteApiUrl({
        GOAL_TEST_REMOTE_API_URL: "http://127.0.0.1:18762",
        REMOTE_API_URL: "http://127.0.0.1:18760",
      }),
    ).toBe("http://127.0.0.1:18762");
  });

  it("prefers REMOTE_API_URL when explicitly configured", () => {
    expect(
      resolveRemoteApiUrl({
        REMOTE_API_URL: "http://backend:8080",
        NEXT_PUBLIC_API_URL: "http://localhost:19000",
        PORT: "18080",
      }),
    ).toBe("http://backend:8080");
  });

  it("uses NEXT_PUBLIC_API_URL when REMOTE_API_URL is unset", () => {
    expect(
      resolveRemoteApiUrl({
        NEXT_PUBLIC_API_URL: "http://localhost:19000",
        PORT: "18080",
      }),
    ).toBe("http://localhost:19000");
  });

  it("derives localhost backend URL from PORT when no API URL is set", () => {
    expect(resolveRemoteApiUrl({ PORT: "19080" })).toBe("http://localhost:19080");
  });

  it("prefers the explicit backend port over the process port", () => {
    expect(resolveRemoteApiUrl({ BACKEND_PORT: "28080", PORT: "19080" })).toBe(
      "http://localhost:28080",
    );
  });

  it("ignores whitespace-only backend URL values", () => {
    expect(
      resolveRemoteApiUrl({
        REMOTE_API_URL: "  ",
        NEXT_PUBLIC_API_URL: "  ",
        BACKEND_PORT: "  ",
        PORT: "19080",
      }),
    ).toBe("http://localhost:19080");

    expect(resolveRemoteApiUrl({ PORT: "  " })).toBe("http://localhost:8080");
  });

  it("falls back to the historical backend port when no env is configured", () => {
    expect(resolveRemoteApiUrl({})).toBe("http://localhost:8080");
  });
});
