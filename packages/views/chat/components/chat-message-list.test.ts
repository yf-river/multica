import { describe, expect, it } from "vitest";

import { shouldFetchAssistantTaskMessages } from "./chat-message-list";

describe("shouldFetchAssistantTaskMessages", () => {
  it("does not fetch every completed transcript while chat history mounts", () => {
    expect(shouldFetchAssistantTaskMessages(true, false, false, false)).toBe(false);
  });

  it("loads the transcript for live, failed, or explicitly opened replies", () => {
    expect(shouldFetchAssistantTaskMessages(true, true, false, false)).toBe(true);
    expect(shouldFetchAssistantTaskMessages(true, false, true, false)).toBe(true);
    expect(shouldFetchAssistantTaskMessages(true, false, false, true)).toBe(true);
    expect(shouldFetchAssistantTaskMessages(false, true, true, true)).toBe(false);
  });
});
