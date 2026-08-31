import { describe, expect, it } from "vitest";

import { GET } from "./route";

describe("favicon redirect", () => {
  it("keeps the redirect relative to the public web origin", async () => {
    const response = GET();

    expect(response.status).toBe(308);
    expect(response.headers.get("location")).toBe("/favicon.svg");
    expect(await response.text()).toBe("Redirecting to favicon.svg");
  });
});
