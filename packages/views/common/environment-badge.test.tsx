import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, fireEvent } from "@testing-library/react";
import { BuildInfoContext, configStore } from "@multica/core/config";
import { EnvironmentBadge } from "./environment-badge";

afterEach(cleanup);
it("shows Beijing build time and keeps page identity when an update is available", () => {
  configStore.getState().setEnvironmentName("生产");
  const reload = vi.fn();
  render(<BuildInfoContext.Provider value={{ build: { builtAt: "2026-10-19T06:22:00Z", commit: "abcdef1234567890", digest: "digest" }, updateAvailable: true, reload }}>
    <EnvironmentBadge />
  </BuildInfoContext.Provider>);
  expect(screen.getByText("生产环境")).toBeTruthy();
  expect(screen.getByText("构建 10.19-14:22").getAttribute("title")).toContain("2026-10-19 14:22 UTC+8 · abcdef123456");
  fireEvent.click(screen.getByRole("button", { name: "新版本已上线，刷新更新" }));
  expect(reload).toHaveBeenCalledOnce();
});
it("does not display a made-up timestamp when metadata is absent", () => {
  configStore.getState().setEnvironmentName("生产");
  render(<EnvironmentBadge />);
  expect(screen.queryByText(/构建/)).toBeNull();
});
