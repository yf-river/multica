import { afterEach, expect, it, vi } from "vitest";
import { useContext } from "react";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BuildInfoContext } from "@multica/core/config";
import { BuildInfoProvider } from "./build-info-provider";

function Status() {
  const { build, updateAvailable } = useContext(BuildInfoContext);
  return <div>{build?.builtAt}:{updateAvailable ? "refresh" : "current"}</div>;
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });
it("detects new artifacts without relabeling the already loaded page", async () => {
  const initial = { builtAt: "2026-10-19T06:22:00Z", commit: "a", digest: "b" };
  const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => initial });
  vi.stubGlobal("fetch", fetch);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><BuildInfoProvider initialBuild={initial}><Status /></BuildInfoProvider></QueryClientProvider>);
  await waitFor(() => expect(fetch).toHaveBeenCalledOnce());
  expect(screen.getByText(`${initial.builtAt}:current`)).toBeTruthy();
  fetch.mockResolvedValue({ ok: true, json: async () => ({ ...initial, builtAt: "2026-10-19T07:00:00Z" }) });
  await client.invalidateQueries({ queryKey: ["web-build"] });
  await waitFor(() => expect(screen.getByText(`${initial.builtAt}:refresh`)).toBeTruthy());
  expect(fetch).toHaveBeenCalledWith("/api/version", { cache: "no-store" });
  client.clear();
});
