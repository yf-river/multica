import { describe, expect, it } from "vitest";
import { chatSessionDisplayTitle } from "./chat-session-title";

describe("chatSessionDisplayTitle", () => {
  it("uses New chat for an explicitly empty channel-created Chat", () => {
    expect(chatSessionDisplayTitle("")).toBe("新对话");
    expect(chatSessionDisplayTitle(null)).toBe("新对话");
    expect(chatSessionDisplayTitle(undefined)).toBe("新对话");
  });

  it("preserves a stored or manually renamed title", () => {
    expect(chatSessionDisplayTitle("Investigate deploy")).toBe(
      "Investigate deploy",
    );
  });
});
