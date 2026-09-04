import { describe, expect, it } from "vitest";
import { INSTALL_RUNTIME_ISSUE_BODY, INSTALL_RUNTIME_ISSUE_TITLE } from "./index";

describe("Chinese onboarding templates", () => {
  it("exports the runtime guide in Chinese", () => {
    expect(INSTALL_RUNTIME_ISSUE_TITLE).toBe("连接运行时，和 Mika 开始");
    expect(INSTALL_RUNTIME_ISSUE_BODY).toContain("欢迎来到 Multica");
  });
});
