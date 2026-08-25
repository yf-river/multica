import { describe, expect, it } from "vitest";
import { redactExceptionProperties } from "./redact-exception";

describe("redactExceptionProperties", () => {
  it("redacts only token-bearing message content", () => {
    const cases = [
      ["Invalid email: alice@example.com", "Invalid email: alice@example.com"],
      [
        "fetch failed https://api.multica.ai/issues?token=abc123secret",
        "fetch failed https://api.multica.ai/issues?[redacted]",
      ],
      ["auth header eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9", "auth header [redacted]"],
      ["Cannot read property 'x' of undefined", "Cannot read property 'x' of undefined"],
      [undefined, undefined],
      [42, 42],
    ] as const;

    for (const [input, expected] of cases) {
      const props = { $exception_message: input };
      redactExceptionProperties(props);
      expect(props.$exception_message).toBe(expected);
    }
  });

  it("scrubs the message and each $exception_list value, leaving frames untouched", () => {
    const props = {
      $exception_message: "Bad input bob@corp.com",
      $exception_list: [
        {
          type: "TypeError",
          value: "Token leaked: ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
          stacktrace: { frames: [{ filename: "app.tsx", lineno: 5, function: "render" }] },
        },
      ],
    };

    redactExceptionProperties(props);

    const entry = props.$exception_list[0]!;
    expect(props.$exception_message).toBe("Bad input bob@corp.com");
    expect(entry.value).toBe("Token leaked: [redacted]");
    // Frames are code locations, not user data — left intact.
    expect(entry.stacktrace.frames[0]).toEqual({
      filename: "app.tsx",
      lineno: 5,
      function: "render",
    });
    expect(entry.type).toBe("TypeError");
  });

  it("is safe on undefined / malformed properties", () => {
    expect(redactExceptionProperties(undefined)).toBeUndefined();
    expect(() =>
      redactExceptionProperties({ $exception_list: "not-an-array" as unknown as [] }),
    ).not.toThrow();
  });
});
