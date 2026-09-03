import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const runtimesPage = vi.fn<(props: Record<string, unknown>) => null>(() => null);

vi.mock("@multica/views/runtimes", () => ({
  RuntimesPage: (props: Record<string, unknown>) => runtimesPage(props),
}));

import { DesktopRuntimesPage } from "./desktop-runtimes-page";

describe("DesktopRuntimesPage", () => {
  beforeEach(() => {
    runtimesPage.mockClear();
  });

  it("keeps daemon controls out of the machine collection", () => {
    render(<DesktopRuntimesPage />);

    expect(runtimesPage).toHaveBeenCalledWith({});
  });
});
