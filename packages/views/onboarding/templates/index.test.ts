import { describe, expect, it } from "vitest";
import { pickContentLang } from "./index";

describe("pickContentLang", () => {
  it("always selects Chinese persisted content", () => {
    expect(pickContentLang("zh-CN")).toBe("zh");
    expect(pickContentLang("zh-Hant")).toBe("zh");
    expect(pickContentLang("unsupported")).toBe("zh");
    expect(pickContentLang("fr-FR")).toBe("zh");
    expect(pickContentLang(null)).toBe("zh");
    expect(pickContentLang(undefined)).toBe("zh");
  });
});
