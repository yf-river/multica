// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
import { z } from "zod";
import { noopLogger } from "../logger";
import { parseOrThrow, setSchemaLogger } from "./schema";

afterEach(() => setSchemaLogger(noopLogger));

it("rejects malformed private evidence without logging its content", () => {
  const warn = vi.fn();
  setSchemaLogger({ ...noopLogger, warn });
  const secret = "用户备注和原始轨迹，不应进入遥测";
  expect(() => parseOrThrow({ text: secret }, z.object({ text: z.literal("expected") }), {
    endpoint: "GET /api/supervision/evidence", sensitive: true,
  })).toThrow("API response format invalid");
  expect(warn).toHaveBeenCalledOnce();
  expect(JSON.stringify(warn.mock.calls)).not.toContain(secret);
  expect(warn.mock.calls[0]?.[1]).toEqual({ endpoint: "GET /api/supervision/evidence", issues: [{ code: "invalid_value", path: ["text"] }], received: "[redacted]" });
});
