// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  customRuntimeDocsHref,
  daemonRuntimesDocsHref,
} from "./runtime-docs";

describe("runtime docs links", () => {
  it("always links to the Chinese daemon guide", () => {
    expect(daemonRuntimesDocsHref()).toBe(
      "https://multica.ai/docs/zh/daemon-runtimes",
    );
  });

  it("adds the localized custom runtime section", () => {
    expect(customRuntimeDocsHref()).toBe(
      `https://multica.ai/docs/zh/daemon-runtimes#${encodeURIComponent("自定义运行时配置")}`,
    );
  });
});
